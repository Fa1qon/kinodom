package jacred

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"kinodom/internal/netx"
	"kinodom/internal/source"
)

var ctx = context.Background()

// fakeSource — Jacred или Jackett для тестов: что спросили и что ответить.
type fakeSource struct {
	mu      sync.Mutex
	queries []url.Values
}

func (f *fakeSource) last(t *testing.T) url.Values {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		t.Fatal("поиска не было")
	}
	return f.queries[len(f.queries)-1]
}

// jacredServer — Jacred по живым ответам 30.09 (testdata): conf и поиск карточкой. key != "" — сервер
// с ключом: без ключа или с чужим ключом поиск отдаёт пустой список, как jac.red с 2026-10-01.
func jacredServer(t *testing.T, key string, results []byte) (*httptest.Server, *fakeSource) {
	t.Helper()
	f := &fakeSource{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1.0/conf":
			json.NewEncoder(w).Encode(map[string]any{"jacred": true, "configured": false, "apikey": key != "", "version": "3.15.0"})
		case "/api/v2.0/indexers/all/results":
			f.mu.Lock()
			f.queries = append(f.queries, r.URL.Query())
			f.mu.Unlock()
			if key != "" && r.URL.Query().Get("apikey") != key {
				w.Write([]byte(`{"Results":[],"jacred":true}`))
				return
			}
			w.Write(results)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s, f
}

// jackettServer — Jackett: conf нет (404), поиск — только с верным ключом, иначе 401.
func jackettServer(t *testing.T, key string, results []byte) (*httptest.Server, *fakeSource) {
	t.Helper()
	f := &fakeSource{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2.0/indexers/all/results" {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		f.queries = append(f.queries, r.URL.Query())
		f.mu.Unlock()
		if r.URL.Query().Get("apikey") != key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write(results)
	}))
	t.Cleanup(s.Close)
	return s, f
}

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func byTopic(rs []source.Release) map[string]source.Release {
	m := map[string]source.Release{}
	for _, r := range rs {
		m[r.Tracker+":"+r.TopicID] = r
	}
	return m
}

func TestParseQuery(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Query
	}{
		{"Джентльмены 2 сезон", Query{Title: "Джентльмены", Season: 2}},
		{"Матрица 1999", Query{Title: "Матрица", Year: 1999}},
		{"  Трудно быть богом  ", Query{Title: "Трудно быть богом"}},
		{"1917", Query{Title: "1917"}},
		{"Бегущий по лезвию 2049", Query{Title: "Бегущий по лезвию 2049"}},
		{"The Gentlemen S02", Query{Title: "The Gentlemen", Season: 2}},
		{"Фонари (2026) сезон 1", Query{Title: "Фонари", Year: 2026, Season: 1}},
		{"Холод 2-й сезон", Query{Title: "Холод", Season: 2}},
		{"Lanterns season 1", Query{Title: "Lanterns", Season: 1}},
		{"Сезон: 3 Фарго", Query{Title: "Фарго", Season: 3}},
	} {
		if got := ParseQuery(c.in); got != c.want {
			t.Errorf("ParseQuery(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// Источник спрашивается карточкой: название (оно же — оригинальное: запрос бывает и по-английски) и год;
// у раздачи — первый трекер списка, ссылка на тему, magnet и infohash; у Rutor и Rutracker — номер темы.
func TestJacredSearchCard(t *testing.T) {
	s, f := jacredServer(t, "", testdata(t, "card-gentlemen.json"))
	c := New(Options{Address: s.URL})
	rs, err := c.Search(ctx, "Джентльмены 2024")
	if err != nil {
		t.Fatal(err)
	}
	q := f.last(t)
	for k, want := range map[string]string{"Query": "Джентльмены", "title": "Джентльмены", "title_original": "Джентльмены", "year": "2024", "is_serial": "", "apikey": ""} {
		if q.Get(k) != want {
			t.Errorf("параметр %s = %q, want %q (%v)", k, q.Get(k), want, q)
		}
	}
	if len(rs) != 8 {
		t.Fatalf("раздач %d, want 8", len(rs))
	}
	m := byTopic(rs)
	rt, ok := m["rutracker:6903474"]
	if !ok {
		t.Fatalf("нет темы Rutracker 6903474: %v", m)
	}
	if rt.Link != "https://rutracker.org/forum/viewtopic.php?t=6903474" || !strings.HasPrefix(rt.Magnet, "magnet:?xt=urn:btih:") ||
		len(rt.InfoHash) != 40 || rt.InfoHash != strings.ToLower(rt.InfoHash) || rt.Seeders == 0 || rt.Size == 0 || rt.Added.IsZero() ||
		!strings.HasPrefix(rt.Title, "Джентльмены (2 сезон") {
		t.Errorf("Rutracker: %+v", rt)
	}
	if _, ok := m["rutor:973679"]; !ok {
		t.Errorf("нет темы Rutor 973679: %v", m)
	}
	var kz []source.Release
	for _, r := range rs {
		if r.Tracker == "kinozal" {
			kz = append(kz, r)
		}
	}
	if len(kz) != 2 || kz[0].TopicID != kz[0].InfoHash || kz[0].Link != "https://kinozal.guru/details.php?id=2155295" || kz[0].Magnet == "" {
		t.Errorf("Kinozal: %+v", kz)
	}
}

// «Джентльмены 2 сезон»: источник спрашивается сериалом без сезона, сезон отбирается у нас — по
// info.seasons (Jacred), а без них (Jackett) — по названию; без сезона в названии раздача остаётся.
func TestJacredSeasonFilter(t *testing.T) {
	s, f := jacredServer(t, "", testdata(t, "card-gentlemen.json"))
	rs, err := New(Options{Address: s.URL}).Search(ctx, "Джентльмены 2 сезон")
	if err != nil {
		t.Fatal(err)
	}
	if q := f.last(t); q.Get("title") != "Джентльмены" || q.Get("is_serial") != "2" {
		t.Errorf("запрос: %v", q)
	}
	var got []string
	for _, r := range rs {
		got = append(got, r.Tracker+":"+r.TopicID[:min(len(r.TopicID), 7)])
	}
	slices.Sort(got)
	if want := []string{"kinozal:" + byTopicHash(t, rs, "kinozal"), "lostfilm:" + byTopicHash(t, rs, "lostfilm"), "rutracker:6903474", "rutracker:6903749"}; !slices.Equal(got, want) {
		t.Errorf("сезон 2: %v, want %v", got, want)
	}

	// Jackett: info нет, трекер — TrackerId; раздача без magnet с чужого трекера не годится (скачать
	// нечем, а ссылка Jackett на .torrent несёт ключ); тема Rutracker без magnet — наша, по номеру темы.
	jk := `{"Results":[
	 {"Tracker":"Kinozal","TrackerId":"kinozal","Details":"https://kinozal.tv/details.php?id=1","Title":"Джентльмены (2 сезон: 1-8 серии из 8) / The Gentlemen / 2026 / ДБ / WEB-DL (1080p)","MagnetUri":"magnet:?xt=urn:btih:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","Seeders":5,"Peers":8,"Size":100,"PublishDate":"2026-09-30T12:41:00+03:00"},
	 {"Tracker":"Kinozal","TrackerId":"kinozal","Details":"https://kinozal.tv/details.php?id=2","Title":"Джентльмены (1 сезон: 1-8 серии из 8) / The Gentlemen / 2024 / ДБ / WEB-DL (1080p)","MagnetUri":"magnet:?xt=urn:btih:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB","Seeders":5,"Peers":8,"Size":100},
	 {"Tracker":"NNM-Club","TrackerId":"nnmclub","Details":"https://nnmclub.to/forum/viewtopic.php?t=3","Title":"Джентльмены / The Gentlemen / 2026 / WEB-DL","MagnetUri":"magnet:?xt=urn:btih:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC","Seeders":1,"Peers":1,"Size":100},
	 {"Tracker":"Kinozal","TrackerId":"kinozal","Details":"https://kinozal.tv/details.php?id=4","Title":"Джентльмены (2 сезон) / The Gentlemen / 2026","Link":"http://127.0.0.1:9117/dl/kinozal/?jackett_apikey=k&path=x","Seeders":1,"Peers":1,"Size":100},
	 {"Tracker":"RuTracker.org","TrackerId":"rutracker","Details":"https://rutracker.org/forum/viewtopic.php?t=6903474","Title":"Джентльмены / The Gentlemen / Сезон: 2 / Серии: 1-8 из 8","Link":"http://127.0.0.1:9117/dl/rutracker/?jackett_apikey=k&path=y","Seeders":40,"Peers":50,"Size":100}
	]}`
	js, _ := jackettServer(t, "k", []byte(jk))
	rs, err = New(Options{Address: js.URL, Key: "k"}).Search(ctx, "Джентльмены 2 сезон")
	if err != nil {
		t.Fatal(err)
	}
	m := byTopic(rs)
	if len(rs) != 3 {
		t.Errorf("Jackett, сезон 2: %v", m)
	}
	kz := m["kinozal:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]
	if kz.InfoHash != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || kz.Seeders != 5 || kz.Leechers != 3 || kz.Added.IsZero() {
		t.Errorf("Jackett Kinozal: %+v", kz)
	}
	if _, ok := m["nnmclub:cccccccccccccccccccccccccccccccccccccccc"]; !ok {
		t.Errorf("без сезона в названии раздача должна остаться: %v", m)
	}
	if rt, ok := m["rutracker:6903474"]; !ok || rt.Magnet != "" || strings.Contains(rt.Link, "apikey") {
		t.Errorf("Jackett Rutracker: %+v", rt)
	}
}

// byTopicHash — первые 7 знаков infohash единственной раздачи трекера.
func byTopicHash(t *testing.T, rs []source.Release, tracker string) string {
	t.Helper()
	for _, r := range rs {
		if r.Tracker == tracker && r.TopicID == r.InfoHash {
			return r.InfoHash[:7]
		}
	}
	return "?"
}

func TestJacredCheck(t *testing.T) {
	results := testdata(t, "card-gentlemen.json")
	jacredOpen, _ := jacredServer(t, "", results)
	jacredKey, _ := jacredServer(t, "jk", results)
	jackett, _ := jackettServer(t, "k", []byte(`{"Results":[]}`))
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>Роутер</body></html>"))
	}))
	t.Cleanup(html.Close)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	for _, c := range []struct {
		name, addr, key string
		ok              bool
		text            string
	}{
		{"нет адреса", "", "", false, "Укажите адрес источника"},
		{"Jacred без ключа", jacredOpen.URL, "", true, "Jacred отвечает"},
		{"Jacred просит ключ", jacredKey.URL, "", false, "Нужен ключ"},
		{"Jacred, чужой ключ", jacredKey.URL, "чужой", false, "Ключ не подошёл"},
		{"Jacred, ключ верный", jacredKey.URL, "jk", true, "Jacred отвечает"},
		{"Jackett", jackett.URL, "k", true, "Jackett отвечает"},
		{"Jackett без ключа", jackett.URL, "", false, "Нужен ключ"},
		{"Jackett, чужой ключ", jackett.URL, "x", false, "Ключ не подошёл"},
		{"не источник", html.URL, "", false, "Адрес не похож на Jacred или Jackett"},
		{"не отвечает", down.URL, "", false, "Не отвечает — нужен прокси?"},
	} {
		ok, text := New(Options{Address: c.addr, Key: c.key, Timeout: 5 * time.Second}).Check(ctx)
		if ok != c.ok || text != c.text {
			t.Errorf("%s: %v %q, want %v %q", c.name, ok, text, c.ok, c.text)
		}
	}

	// Поиск у Jacred, который просит ключ, без ключа — не «ничего не найдено», а причина.
	if _, err := New(Options{Address: jacredKey.URL}).Search(ctx, "Джентльмены"); !errors.Is(err, ErrNeedKey) {
		t.Errorf("поиск без ключа: %v", err)
	}
	if _, err := New(Options{Address: jackett.URL, Key: "x"}).Search(ctx, "Джентльмены"); !errors.Is(err, ErrBadKey) {
		t.Errorf("поиск с чужим ключом: %v", err)
	}
	if _, err := New(Options{}).Search(ctx, "Джентльмены"); !errors.Is(err, source.ErrNotConfigured) {
		t.Errorf("без адреса: %v", err)
	}
}

// Ключ — параметр запроса: ни адрес запроса, ни ключ не попадают в текст ошибки и в журнал.
func TestJacredKeyNeverShown(t *testing.T) {
	const key = "sekret-7f3a"
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom "+r.URL.RawQuery, http.StatusInternalServerError)
	}))
	t.Cleanup(fail.Close)
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hang.Close)
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{not json " + r.URL.RawQuery))
	}))
	t.Cleanup(junk.Close)

	for _, addr := range []string{down.URL, fail.URL, hang.URL, junk.URL} {
		c := New(Options{Address: addr, Key: key, Timeout: 300 * time.Millisecond, Log: log})
		_, err := c.Search(ctx, "Матрица 1999")
		if err == nil {
			t.Errorf("%s: ошибки нет", addr)
			continue
		}
		if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "apikey") {
			t.Errorf("ключ в тексте ошибки: %v", err)
		}
		if _, text := c.Check(ctx); strings.Contains(text, key) {
			t.Errorf("ключ в итоге проверки: %q", text)
		}
	}
	if strings.Contains(logBuf.String(), key) || strings.Contains(logBuf.String(), "apikey") {
		t.Errorf("ключ в журнале:\n%s", logBuf.String())
	}
}

// Запросы — через прокси трекеров из настроек; адрес домашней сети (свой Jackett) — напрямую.
func TestJacredViaProxy(t *testing.T) {
	results := testdata(t, "card-gentlemen.json")
	var proxied []string
	var mu sync.Mutex
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxied = append(proxied, r.URL.Host) // HTTP-прокси получает полный адрес: отвечаем сами
		mu.Unlock()
		w.Write(results)
	}))
	t.Cleanup(proxy.Close)
	p, err := netx.NewProxy(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := New(Options{Address: "http://jacred.example", Proxy: p}).Search(ctx, "Джентльмены")
	if err != nil || len(rs) != 8 || len(proxied) != 1 || proxied[0] != "jacred.example" {
		t.Fatalf("через прокси: %v, раздач %d, прокси %v", err, len(rs), proxied)
	}

	local, f := jacredServer(t, "", results)
	if rs, err := New(Options{Address: local.URL, Proxy: p}).Search(ctx, "Джентльмены"); err != nil || len(rs) != 8 || len(proxied) != 1 {
		t.Fatalf("домашняя сеть: %v, раздач %d, прокси %v", err, len(rs), proxied)
	}
	f.last(t)
}
