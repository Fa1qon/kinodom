package iptv

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kinodom/internal/iptv/m3u"
	"kinodom/internal/iptv/probe"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
)

// fakeNet — телепрограмма, база iptv-org, плейлист и источники на одном фейковом сервере.
type fakeNet struct {
	srv      *httptest.Server
	mu       sync.Mutex
	playlist string
	epgCode  int
	epg      string       // "" — testEPG
	limited  atomic.Int32 // запросов к источнику «ограниченного» плейлиста
}

func newFakeNet(t *testing.T) *fakeNet {
	t.Helper()
	f := &fakeNet{epgCode: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/epg.xml", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		code := f.epgCode
		f.mu.Unlock()
		if code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		if f.epg != "" {
			io.WriteString(w, f.epg)
			return
		}
		io.WriteString(w, testEPG)
	})
	mux.HandleFunc("/api/channels.json", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, testOrgChannels) })
	mux.HandleFunc("/api/feeds.json", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, testOrgFeeds) })
	mux.HandleFunc("/pl.m3u", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		io.WriteString(w, f.playlist)
	})
	mux.HandleFunc("/s/ok.m3u8", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "#EXTM3U\n#EXTINF:1,\nseg.ts\n")
	})
	mux.HandleFunc("/s/seg.ts", func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, 64<<10)) })
	mux.HandleFunc("/s/dead.m3u8", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	mux.HandleFunc("/s/limited.m3u8", func(w http.ResponseWriter, r *http.Request) {
		f.limited.Add(1)
		io.WriteString(w, "#EXTM3U\n#EXTINF:1,\nseg.ts\n")
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNet) setPlaylist(s string) {
	f.mu.Lock()
	f.playlist = s
	f.mu.Unlock()
}

// startModule — модуль под сторожем, как в приложении.
func startModule(t *testing.T, f *fakeNet) (*Module, *store.DB) {
	t.Helper()
	dir := t.TempDir()
	d, err := store.Open(context.Background(), filepath.Join(dir, "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	m := New(Options{DB: d, Dir: dir, EPGURL: f.srv.URL + "/epg.xml", OrgBase: f.srv.URL + "/api",
		Prober: &probe.Prober{Client: &http.Client{}, Timeout: 2 * time.Second, LiveFor: 200 * time.Millisecond}})
	sup := supervisor.New(slog.New(slog.DiscardHandler))
	sup.Add(m, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, "модуль работает", func() bool { return sup.IsRunning("iptv") })
	return m, d
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (m *Module) streamState(url string) (string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.pool.byURL[url]
	if s == nil {
		return "", 0
	}
	return s.State, s.Fails
}

// Плейлист по ссылке: разбор, сопоставление, лёгкая проверка новых источников, затем полная у видимых
// каналов; «ограниченный» плейлист в фоне не проверяется, но его каналы на экране; обновление
// плейлиста убирает пропавшие источники (спека этапа 8, разделы 5.1 и 5.8).
func TestModulePlaylists(t *testing.T) {
	f := newFakeNet(t)
	m, _ := startModule(t, f)
	waitFor(t, "телепрограмма скачалась", func() bool { return m.Guide() != nil })
	ok, dead := f.srv.URL+"/s/ok.m3u8", f.srv.URL+"/s/dead.m3u8"
	f.setPlaylist(fmt.Sprintf("#EXTM3U\n#EXTINF:-1,НТВ HD\n%s\n#EXTINF:-1,Матч ТВ\n%s\n#EXTINF:-1,Неизвестный\n%s\n#EXTINF:-1,UDP\nudp://@239.1.1.1:1234\n", ok, dead, ok+"?x"))
	res, err := m.AddPlaylist(context.Background(), PlaylistInput{URL: f.srv.URL + "/pl.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Entries != 3 || res.Unsupported != 1 || res.Recognized != 2 {
		t.Errorf("результат добавления: %+v", res)
	}
	waitFor(t, "лёгкая проверка", func() bool {
		st, _ := m.streamState(ok)
		ds, fails := m.streamState(dead)
		return st == StateAlive && ds == StateSilent && fails == 1
	})
	waitFor(t, "полная проверка видимого канала", func() bool {
		m.rebuild(context.Background())
		c := m.Lineup().ByKey["ntv"]
		return c != nil && c.Grade == GradeGreen
	})
	// Часовой пояс каналов по умолчанию — UTC+7 (Windows на ПК заказчика — московское время).
	if l := m.Lineup(); l.LocalShift != 4 {
		t.Errorf("местный сдвиг %d, нужно 4", l.LocalShift)
	}
	m.SetLocation(Zone(3))
	if l := m.Lineup(); l.LocalShift != 0 {
		t.Errorf("после смены пояса на UTC+3 сдвиг %d", l.LocalShift)
	}
	m.SetLocation(Zone(7))
	if c := m.Lineup().ByKey["match-tv"]; c == nil || c.Offered() {
		t.Errorf("канал с молчащим источником предлагается: %+v", c)
	}
	// «Ограниченный» плейлист: файлом, в фоне не проверяется; канал на экране без проверки.
	limited := f.srv.URL + "/s/limited.m3u8"
	res, err = m.AddPlaylist(context.Background(), PlaylistInput{Name: "Платный", Limited: true,
		Data: []byte("#EXTM3U\n#EXTINF:-1,Спас\n" + limited + "\n")})
	if err != nil || res.Recognized != 1 {
		t.Fatalf("ограниченный: %+v, %v", res, err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := f.limited.Load(); n != 0 {
		t.Errorf("«ограниченный» плейлист проверялся в фоне: %d запросов", n)
	}
	if c := m.Lineup().ByKey["spas"]; c == nil || !c.Offered() || c.Grade != GradeUnrated {
		t.Errorf("канал ограниченного плейлиста: %+v", c)
	}
	// Кнопка «Проверить» — проверяет и ограниченный.
	if !m.ProbeChannel("spas") {
		t.Fatal("канал spas не найден")
	}
	waitFor(t, "проверка по кнопке", func() bool { return f.limited.Load() > 0 })
	// Обновление: источника dead больше нет — он удалён вместе с проверками.
	f.setPlaylist(fmt.Sprintf("#EXTM3U\n#EXTINF:-1,НТВ HD\n%s\n", ok))
	if err := m.RefreshPlaylist(context.Background(), res.ID-1); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.streamState(dead); st != "" {
		t.Errorf("пропавший источник остался: %s", st)
	}
	if err := m.DeletePlaylist(context.Background(), res.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddPlaylist(context.Background(), PlaylistInput{Data: []byte("<html>")}); err != ErrBadPlaylist {
		t.Errorf("мусор вместо плейлиста: %v", err)
	}
}

// Телепрограмма не скачивается и её не было — проблема в «Состоянии»; скачалась — проблема снята.
func TestModuleEPGProblem(t *testing.T) {
	f := newFakeNet(t)
	f.epgCode = http.StatusNotFound
	m, d := startModule(t, f)
	hasProblem := func() bool {
		ps, err := d.Problems(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if p.ID == "iptv.epg" && strings.Contains(p.Text, "Телепрограмма не обновляется: сервер ответил 404") {
				return true
			}
		}
		return false
	}
	waitFor(t, "проблема телепрограммы", hasProblem)
	f.mu.Lock()
	f.epgCode = http.StatusOK
	f.mu.Unlock()
	m.SetEPGURL(f.srv.URL + "/epg.xml")
	waitFor(t, "проблема снята", func() bool { return !hasProblem() && m.Guide() != nil })
}

// Состояние источника после проверок: три «не отвечает» подряд — мёртв; удача сбрасывает счёт.
func TestApplyResult(t *testing.T) {
	s := &Stream{State: StateNew}
	now := time.Now()
	black := probe.Result{Grade: probe.GradeBlack, Error: "HTTP 403"}
	for i, want := range []string{StateSilent, StateSilent, StateDead} {
		applyResult(s, "light", black, now)
		if s.State != want || s.Fails != i+1 || s.Error != "HTTP 403" {
			t.Fatalf("после %d неудач: %+v", i+1, s)
		}
	}
	applyResult(s, "full", probe.Result{Grade: probe.GradeYellow, Ratio: 1.2, Height: 1080, Kind: "hls"}, now)
	if s.State != StateAlive || s.Fails != 0 || s.Grade != GradeYellow || s.Quality != "FHD" || s.Kind != "hls" || !s.FullAt.Equal(now) {
		t.Errorf("после удачи: %+v", s)
	}
	applyResult(s, "light", probe.Result{Grade: probe.GradeAlive}, now)
	if s.Grade != GradeYellow {
		t.Errorf("лёгкая проверка стёрла оценку полной: %q", s.Grade)
	}
}

// Расписание полной проверки: раз в 3 часа и каждый час с 19 до 22; лёгкая — в 4:00.
func TestNextAt(t *testing.T) {
	loc := time.FixedZone("UTC+7", 7*3600)
	at := func(d, h, m int) time.Time { return time.Date(2026, 9, d, h, m, 0, 0, loc) }
	cases := []struct {
		now   time.Time
		hours []int
		want  time.Time
	}{
		{at(29, 1, 30), fullHours, at(29, 3, 0)},
		{at(29, 18, 0), fullHours, at(29, 19, 0)},
		{at(29, 19, 10), fullHours, at(29, 20, 0)},
		{at(29, 22, 0), fullHours, at(30, 0, 0)},
		{at(29, 3, 59), []int{lightHour}, at(29, 4, 0)},
		{at(29, 4, 0), []int{lightHour}, at(30, 4, 0)},
	}
	for _, c := range cases {
		if got := nextAt(c.now, c.hours); !got.Equal(c.want) {
			t.Errorf("после %v: %v, нужно %v", c.now, got, c.want)
		}
	}
}

// Телепрограмма уже скачана: как только модуль готов, каналы в составе — без секунд пустоты после
// перезапуска (иначе карточка канала на ТВ показала бы «Такого канала нет»).
func TestModuleReadyWithChannels(t *testing.T) {
	f := newFakeNet(t)
	dir := t.TempDir()
	d, err := store.Open(context.Background(), filepath.Join(dir, "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	pl := &Playlist{Name: "a", AddedAt: time.Now()}
	if err := (db{d}).insertPlaylist(context.Background(), pl); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (db{d}).replaceEntries(context.Background(), pl.ID, []m3u.Entry{{Name: "НТВ", URL: f.srv.URL + "/s/ok.m3u8"}}); err != nil {
		t.Fatal(err)
	}
	// Большая программа: разбор занимает заметное время, и «готов раньше, чем прочитал» было бы видно.
	var big strings.Builder
	start := time.Now().Add(-time.Hour)
	for i := range 150000 {
		at := start.Add(time.Duration(i) * time.Second)
		fmt.Fprintf(&big, `<programme start="%s" stop="%s" channel="ntv"><title>П%d</title></programme>`,
			at.Format("20060102150405 -0700"), at.Add(time.Second).Format("20060102150405 -0700"), i)
	}
	epg := strings.Replace(testEPG, "</tv>", big.String()+"</tv>", 1)
	if err := os.WriteFile(filepath.Join(dir, "epg.xml.gz"), []byte(epg), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(Options{DB: d, Dir: dir, EPGURL: f.srv.URL + "/epg.xml", OrgBase: f.srv.URL + "/api"})
	sup := supervisor.New(slog.New(slog.DiscardHandler))
	sup.Add(m, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(15 * time.Second)
	for !sup.IsRunning("iptv") {
		if time.Now().After(deadline) {
			t.Fatal("модуль не стал работать")
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := m.Lineup().ByKey["ntv"]; !ok {
		t.Fatal("модуль готов, а канала из сохранённой телепрограммы нет")
	}
}
