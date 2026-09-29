package torrents

import (
	"net/http"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"kinodom/internal/netx"
	"kinodom/internal/torrents/torrenttest"
)

func TestBuildConfigSendsOnlyHTTPAnnouncesThroughProxy(t *testing.T) {
	px, err := netx.NewProxy("socks5://127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Proxy: px, Log: quiet()}
	cfg, err := buildClientConfig(c, nil, rate.NewLimiter(rate.Inf, 0))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "http://bt4.t-ru.org/ann?magnet", nil)
	u, err := cfg.HTTPProxy(req)
	if err != nil || u == nil || u.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("анонс не идёт через прокси: %v, %v", u, err)
	}
	// Прокси сменили в настройках — следующий анонс идёт через новый, без перезапуска движка.
	px.Set("http://user:pw@127.0.0.1:3128")
	if u, _ := cfg.HTTPProxy(req); u == nil || u.String() != "http://user:pw@127.0.0.1:3128" {
		t.Fatalf("после смены прокси анонс идёт через %v", u)
	}
	tr, ok := cfg.WebTransport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatal("внутренний HTTP-клиент движка (веб-сиды — это данные) не должен ходить через прокси")
	}
	if cfg.TrackerListenPacket != nil {
		t.Fatal("UDP-анонсы должны идти напрямую")
	}
}

func TestBuildConfigDefaults(t *testing.T) {
	cfg, err := buildClientConfig(Config{ListenPort: 42000, Log: quiet()}, nil, rate.NewLimiter(rate.Inf, 0))
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case !cfg.Seed:
		t.Error("раздача должна быть включена")
	case cfg.EstablishedConnsPerTorrent != 20:
		t.Errorf("соединений на раздачу %d, ожидалось 20", cfg.EstablishedConnsPerTorrent)
	case cfg.TotalHalfOpenConns != 100:
		t.Errorf("полуоткрытых %d, ожидалось 100", cfg.TotalHalfOpenConns)
	case cfg.ListenPort != 42000:
		t.Errorf("порт %d", cfg.ListenPort)
	case cfg.HTTPProxy != nil:
		t.Error("без прокси в настройках анонсы идут напрямую")
	case cfg.NoDHT:
		t.Error("DHT должен быть включён")
	case cfg.DhtStartingNodes == nil:
		t.Error("нужен свой быстрый поиск стартовых узлов DHT")
	}
}

func TestEngineReopensSameStateDir(t *testing.T) {
	down, state := t.TempDir(), t.TempDir()
	for i := 1; i <= 2; i++ {
		e, err := NewEngine(Config{DownloadsDir: down, StateDir: state, Offline: true, Log: quiet()})
		if err != nil {
			t.Fatalf("запуск %d: %v", i, err) // второй запуск возможен, только если первый отпустил bolt
		}
		if !e.NetworkReady() {
			t.Error("офлайн-движок считается готовым сразу")
		}
		if err := e.Close(); err != nil {
			t.Fatalf("закрытие %d: %v", i, err)
		}
	}
}

func TestSetUploadLimit(t *testing.T) {
	e := newOfflineEngine(t)
	e.SetUploadLimit(512 << 10)
	if got := e.up.Limit(); got != rate.Limit(512<<10) {
		t.Fatalf("лимит %v", got)
	}
	e.SetUploadLimit(0)
	if e.up.Limit() != rate.Inf {
		t.Fatal("0 — без ограничения")
	}
}

// Офлайн (тесты) — только TCP. uTP через loopback под нагрузкой теряет пакеты, а anacrolix/utp
// (чистый Go: CGO выключен) после потери не восстанавливается — загрузка вставала посреди куска
// (плавающее «не докачался» этапов 2–6, стеки: утп Read/Write ждут друг друга). В службе uTP
// включён, как требует спека (раздел 9).
func TestOfflineEngineUsesTCPOnly(t *testing.T) {
	off, err := buildClientConfig(Config{Offline: true, Log: quiet()}, nil, rate.NewLimiter(rate.Inf, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !off.DisableUTP {
		t.Fatal("офлайн-движок должен ходить только по TCP")
	}
	on, err := buildClientConfig(Config{ListenPort: 42000, Log: quiet()}, nil, rate.NewLimiter(rate.Inf, 0))
	if err != nil {
		t.Fatal(err)
	}
	if on.DisableUTP {
		t.Fatal("в службе uTP должен быть включён (спека, раздел 9)")
	}
	if !torrenttest.OfflineConfig(t.TempDir()).DisableUTP {
		t.Fatal("тестовые клиенты — тоже только TCP")
	}
}

// Пробуждение горутины записи в anacrolix v1.61 теряется (гонка Broadcast и Signaled): соединение
// перестаёт запрашивать куски и оживает только по таймеру keepalive — по умолчанию через минуту.
// В службе таймер — 5 с (простой пира не дольше), в тестах — 100 мс (этап 6).
func TestKeepAliveBoundsRequestStalls(t *testing.T) {
	on, err := buildClientConfig(Config{ListenPort: 42000, Log: quiet()}, nil, rate.NewLimiter(rate.Inf, 0))
	if err != nil {
		t.Fatal(err)
	}
	off, err := buildClientConfig(Config{Offline: true, Log: quiet()}, nil, rate.NewLimiter(rate.Inf, 0))
	if err != nil {
		t.Fatal(err)
	}
	if on.KeepAliveTimeout != 5*time.Second || off.KeepAliveTimeout != 100*time.Millisecond {
		t.Fatalf("keepalive: служба %v, тесты %v", on.KeepAliveTimeout, off.KeepAliveTimeout)
	}
	if got := torrenttest.OfflineConfig(t.TempDir()).KeepAliveTimeout; got != 100*time.Millisecond {
		t.Fatalf("keepalive тестовых клиентов %v", got)
	}
}
