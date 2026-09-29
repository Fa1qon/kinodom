// Package torrenttest — помощники для тестов с настоящими раздачами без сети:
// раздача собирается из временных файлов, раздаёт её второй клиент на 127.0.0.1.
package torrenttest

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	_ "envfirst.local" // тот же ввод-вывод, что и у сервера
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"kinodom/internal/netx"
)

// File — файл будущей раздачи: путь внутри раздачи (через «/») и размер.
type File struct {
	Path string
	Size int
}

// MakeTorrent создаёт dir/name со случайным содержимым и возвращает метаинфо и путь к корню.
// Один файл, у которого Path == name, даёт однофайловую раздачу.
func MakeTorrent(t testing.TB, dir, name string, pieceLen int64, files ...File) (metainfo.MetaInfo, string) {
	t.Helper()
	root := filepath.Join(dir, name)
	single := len(files) == 1 && files[0].Path == name
	for _, f := range files {
		p := root
		if !single {
			p = filepath.Join(root, filepath.FromSlash(f.Path))
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, f.Size)
		rand.Read(b)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	info := metainfo.Info{PieceLength: pieceLen}
	if err := info.BuildFromFilePath(root); err != nil {
		t.Fatal(err)
	}
	ib, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	return metainfo.MetaInfo{InfoBytes: ib}, root
}

// OfflineConfig — клиент без DHT, трекеров и проброса порта, слушает только 127.0.0.1.
func OfflineConfig(dataDir string) *torrent.ClientConfig {
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dataDir
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.NoDefaultPortForwarding = true
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.ListenPort = 0
	cfg.DisableIPv6 = true
	cfg.Seed = true
	// Только TCP: uTP через loopback под нагрузкой теряет пакеты, и anacrolix/utp (чистый Go)
	// после потери не восстанавливается — загрузка вставала посреди куска (этап 6).
	cfg.DisableUTP = true
	// Пробуждение горутины записи в anacrolix теряется, и соединение стоит до таймера keepalive
	// (минута по умолчанию) — в тестах таймер короткий.
	cfg.KeepAliveTimeout = 100 * time.Millisecond
	return cfg
}

// NewSeeder раздаёт mi из dataDir (там уже лежат файлы из MakeTorrent). Хранилище — без
// part-файлов, иначе готовые файлы не считались бы скачанными; закрывается вместе с клиентом,
// чтобы Windows отпустила файл отметок до удаления временной папки.
func NewSeeder(t testing.TB, dataDir string, mi metainfo.MetaInfo) (*torrent.Client, *torrent.Torrent) {
	t.Helper()
	st := storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dataDir, UsePartFiles: g.Some(false)})
	cfg := OfflineConfig(dataDir)
	cfg.DefaultStorage = st
	// Порт выбираем сами: случайный TCP-порт может попасть в диапазон UDP, зарезервированный
	// Windows (Hyper-V, WSL), и тогда движок не откроет UDP на том же номере.
	port, err := netx.FreeTCPUDPPort("127.0.0.1")
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	cfg.ListenPort = port
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close(); st.Close() })
	tt, err := cl.AddTorrent(&mi)
	if err != nil {
		t.Fatal(err)
	}
	<-tt.GotInfo()
	if err := tt.VerifyData(); err != nil {
		t.Fatal(err)
	}
	return cl, tt
}

// Connect подключает раздающего к раздаче и, пока у неё нет живых пиров, предлагает его снова.
// anacrolix не переподключается к пиру после обрыва: в жизни адрес снова приносят DHT, трекеры
// и PEX, а в тестах сети нет — без повторов редкий обрыв соединения на Windows превращал
// загрузку в вечное ожидание. Горутина останавливается при завершении теста.
func Connect(t testing.TB, tt *torrent.Torrent, seeder *torrent.Client) {
	t.Helper()
	tt.AddClientPeer(seeder)
	done := make(chan struct{})
	stopped := make(chan struct{})
	var reoffers atomic.Int32
	go func() {
		defer close(stopped)
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if tt.Stats().ActivePeers == 0 {
					reoffers.Add(1)
					tt.AddClientPeer(seeder)
				}
			}
		}
	}()
	t.Cleanup(func() {
		close(done)
		<-stopped
		// Редкое зависание потока в тестах ещё не объяснено до конца: при сбое печатаем, было ли
		// живое соединение (застрявшее) или его не было вовсе (раздающего предлагали заново).
		if t.Failed() {
			g := tt.Stats().TorrentGauges
			t.Logf("раздача при сбое: пиров %d, ожидают %d, активных %d, раздающих %d, полуоткрытых %d, готово кусков %d; раздающего предлагали заново %d раз",
				g.TotalPeers, g.PendingPeers, g.ActivePeers, g.ConnectedSeeders, g.HalfOpenPeers, g.PiecesComplete, reoffers.Load())
		}
	})
}
