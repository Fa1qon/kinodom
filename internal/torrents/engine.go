package torrents

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"

	"kinodom/internal/netx"
)

// Config — параметры движка.
type Config struct {
	DownloadsDir    string  // куда качать
	StateDir        string  // отметки кусков (bolt) и узлы DHT
	ListenPort      int     // 42000; 0 — любой свободный (тесты)
	UploadLimit     float64 // байт/с; 0 — без ограничения
	ConnsPerTorrent int     // соединений на раздачу; 0 — 20
	TrackerProxy    string  // прокси для HTTP-анонсов; пусто — напрямую
	Offline         bool    // без DHT, трекеров и проброса порта, только 127.0.0.1 (тесты)
	Log             *slog.Logger
}

// Engine — торрент-клиент с хранилищем Kinodom.
type Engine struct {
	cl        *torrent.Client
	fc        storage.ClientImplCloser
	pc        storage.PieceCompletion // отметки кусков: удаление файла сбрасывает их (этап 6)
	dirs      *torrentDirs
	recreated bool // файл отметок был повреждён и создан заново: хранимое — перепроверить
	up        *rate.Limiter
	cfg       Config
	nodesFile string
	closeOnce sync.Once
	closeErr  error
}

func NewEngine(c Config) (*Engine, error) {
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if err := CheckDownloadsDir(c.DownloadsDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.StateDir, 0o755); err != nil {
		return nil, fmt.Errorf("папка %s недоступна: %w", c.StateDir, err)
	}
	recreated, err := checkMarks(c.StateDir)
	if err != nil {
		return nil, err
	}
	if recreated {
		c.Log.Warn("файл отметок кусков был повреждён — создан заново, скачанное перепроверяется по хэшам")
	}
	pc, err := storage.NewBoltPieceCompletion(c.StateDir)
	if err != nil {
		return nil, fmt.Errorf("отметки кусков: %w", err)
	}
	dirs := newTorrentDirs(c.DownloadsDir)
	fc := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: c.DownloadsDir,
		// Папка — своя у каждой раздачи: та, куда она качалась (torrentDirs).
		TorrentDirMaker: func(_ string, info *metainfo.Info, ih metainfo.Hash) string {
			return torrentDir(dirs.get(ih), info, ih)
		},
		FilePathMaker:   filePath,
		PieceCompletion: pc,
		// Без part-файлов: с ними после перезапуска недокачанный файл считается нескачанным.
		UsePartFiles: g.Some(false),
		Logger:       c.Log,
	})
	if c.ListenPort == 0 {
		// Случайный порт (тесты): движок открывает на одном номере TCP и UDP, а случайный
		// TCP-порт может попасть в диапазон UDP, зарезервированный Windows (Hyper-V, WSL).
		host := ""
		if c.Offline {
			host = "127.0.0.1"
		}
		port, err := netx.FreeTCPUDPPort(host)
		if err != nil {
			fc.Close()
			return nil, err
		}
		c.ListenPort = port
	}
	up := rate.NewLimiter(limitOf(c.UploadLimit), 0) // burst 0 — клиент подставит свой
	cfg, err := buildClientConfig(c, prepStorage{inner: fc, pc: pc, dirs: dirs}, up)
	if err != nil {
		fc.Close()
		return nil, err
	}
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		fc.Close()
		return nil, fmt.Errorf("торрент-клиент: %w", err)
	}
	e := &Engine{cl: cl, fc: fc, pc: pc, dirs: dirs, recreated: recreated, up: up, cfg: c, nodesFile: filepath.Join(c.StateDir, "dht-nodes.dat")}
	e.loadNodes()
	return e, nil
}

// buildClientConfig отдельно от NewEngine, чтобы проверять настройки без запуска клиента.
func buildClientConfig(c Config, st storage.ClientImpl, up *rate.Limiter) (*torrent.ClientConfig, error) {
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = c.DownloadsDir
	if st != nil {
		cfg.DefaultStorage = st
	}
	cfg.Slogger = c.Log
	cfg.ListenPort = c.ListenPort
	cfg.Seed = true // раздаём, пока файл хранится (спека, раздел 9)
	cfg.UploadRateLimiter = up
	// Общего лимита соединений в движке нет — только на раздачу (проверено).
	cfg.EstablishedConnsPerTorrent = cmp.Or(c.ConnsPerTorrent, 20)
	cfg.TotalHalfOpenConns = 100
	// В anacrolix v1.61 пробуждение горутины записи теряется (гонка Broadcast и Signaled):
	// соединение перестаёт запрашивать куски и оживает только по таймеру keepalive — по умолчанию
	// через минуту. С 5 с простой пира не дольше; цена — 4 байта keepalive на простаивающее
	// соединение раз в 5 с (этап 6, найдено по стекам зависших тестов).
	cfg.KeepAliveTimeout = 5 * time.Second
	cfg.DhtStartingNodes = fastBootstrap
	if c.Offline {
		cfg.NoDHT = true
		cfg.DisableTrackers = true
		cfg.NoDefaultPortForwarding = true
		cfg.ListenHost = func(string) string { return "127.0.0.1" }
		cfg.DisableIPv6 = true
		// Только TCP: uTP через loopback под нагрузкой теряет пакеты, и anacrolix/utp (чистый Go,
		// CGO выключен) после потери не восстанавливается — загрузка в тестах вставала посреди куска.
		cfg.DisableUTP = true
		cfg.KeepAliveTimeout = 100 * time.Millisecond // тесты: простой из-за потерянного пробуждения — доли секунды
	}
	u, err := netx.ParseProxy(c.TrackerProxy)
	if err != nil {
		return nil, err
	}
	if u != nil {
		// Через прокси — только HTTP-анонсы: адреса анонсов Rutracker в РФ заблокированы.
		cfg.HTTPProxy = http.ProxyURL(u)
		// Иначе HTTPProxy попал бы и во внутренний HTTP-клиент движка (веб-сиды) — а это данные.
		cfg.WebTransport = &http.Transport{Proxy: nil, MaxConnsPerHost: 10}
		// UDP-анонсы через HTTP/SOCKS-прокси не ходят; публичные UDP-трекеры не заблокированы,
		// поэтому TrackerListenPacket не трогаем — они идут напрямую.
	}
	return cfg, nil
}

func limitOf(bytesPerSec float64) rate.Limit {
	if bytesPerSec <= 0 {
		return rate.Inf
	}
	return rate.Limit(bytesPerSec)
}

func (e *Engine) Client() *torrent.Client { return e.cl }
func (e *Engine) DownloadsDir() string    { return e.dirs.defaultDir() }

// SetDownloadsDir — новая папка для новых раздач; открытые раньше остаются, где качались
// (основная спека, раздел 9). Проверка папки — CheckDownloadsDir, до вызова.
func (e *Engine) SetDownloadsDir(dir string) { e.dirs.setDefault(dir) }

// SetTorrentDir — папка загрузок раздачи; вызывать до добавления раздачи в движок.
func (e *Engine) SetTorrentDir(ih metainfo.Hash, dir string) { e.dirs.set(ih, dir) }

// TorrentDir — папка загрузок, в которой лежат файлы раздачи.
func (e *Engine) TorrentDir(ih metainfo.Hash) string { return e.dirs.get(ih) }

// SetUploadLimit меняет лимит отдачи на лету (урезание во время просмотра — этап 6).
func (e *Engine) SetUploadLimit(bytesPerSec float64) { e.up.SetLimit(limitOf(bytesPerSec)) }

// NetworkReady — DHT нашёл хорошие узлы (или движок офлайн). До этого «нет пиров»
// ничего не значит: движок ещё никого не спросил.
func (e *Engine) NetworkReady() bool {
	if e.cfg.Offline {
		return true
	}
	for _, s := range e.cl.DhtServers() {
		if st, ok := s.Stats().(dht.ServerStats); ok && st.GoodNodes > 0 {
			return true
		}
	}
	return false
}

// Close сохраняет узлы DHT (следующий запуск найдёт сеть быстрее) и закрывает клиент и bolt.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		e.saveNodes()
		errs := e.cl.Close()
		errs = append(errs, e.fc.Close())
		e.closeErr = errors.Join(errs...)
	})
	return e.closeErr
}

func (e *Engine) loadNodes() {
	for _, s := range e.cl.DhtServers() {
		w, ok := s.(torrent.AnacrolixDhtServerWrapper)
		if !ok {
			continue
		}
		if _, err := w.Server.AddNodesFromFile(e.nodesFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
			e.cfg.Log.Warn("узлы DHT не прочитались", "err", err)
		}
	}
}

func (e *Engine) saveNodes() {
	for _, s := range e.cl.DhtServers() {
		w, ok := s.(torrent.AnacrolixDhtServerWrapper)
		if !ok {
			continue
		}
		if ns := w.Server.Nodes(); len(ns) > 0 {
			if err := dht.WriteNodesToFile(ns, e.nodesFile); err != nil {
				e.cfg.Log.Warn("узлы DHT не сохранились", "err", err)
			}
			return
		}
	}
}

// fastBootstrap ищет стартовые узлы DHT параллельно с таймаутом 3 с. Стандартный поиск
// перебирает имена по очереди, и два мёртвых имени съедают по 11 с (проверено).
func fastBootstrap(network string) dht.StartingNodesGetter {
	return func() ([]dht.Addr, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var (
			mu  sync.Mutex
			out []dht.Addr
			wg  sync.WaitGroup
		)
		for _, hp := range dht.DefaultGlobalBootstrapHostPorts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				host, port, err := net.SplitHostPort(hp)
				if err != nil {
					return
				}
				ips, err := net.DefaultResolver.LookupHost(ctx, host)
				if err != nil {
					return
				}
				for _, ip := range ips {
					if ua, err := net.ResolveUDPAddr("udp", net.JoinHostPort(ip, port)); err == nil {
						mu.Lock()
						out = append(out, dht.NewAddr(ua))
						mu.Unlock()
					}
				}
			}()
		}
		wg.Wait()
		if len(out) == 0 {
			return nil, errors.New("не найдено ни одного стартового узла DHT")
		}
		return out, nil
	}
}
