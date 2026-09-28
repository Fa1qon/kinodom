package rutor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"kinodom/internal/netx"
	"kinodom/internal/source"
	"kinodom/internal/source/rutor/rutortest"
)

var ctx = context.Background()

func newRutor(t *testing.T, s *rutortest.Server, mirrors ...string) *Rutor {
	t.Helper()
	if len(mirrors) == 0 {
		mirrors = []string{s.Mirror.URL}
	}
	r, err := New(Options{Mirrors: mirrors, DownloadBase: s.Download.URL, Rate: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func sortedBySeeders(rs []source.Release) bool {
	return slices.IsSortedFunc(rs, func(a, b source.Release) int { return b.Seeders - a.Seeders })
}

func TestTopSortedAndLimited(t *testing.T) {
	s := rutortest.NewServer(t)
	rs, err := newRutor(t, s).Top(ctx, "12", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 10 || rs[0].TopicID != "1077013" || !sortedBySeeders(rs) {
		t.Fatalf("топ: %d строк, первая %s", len(rs), rs[0].TopicID)
	}
	for _, r := range rs {
		if r.CategoryID != "12" {
			t.Fatalf("категория %q у %s", r.CategoryID, r.TopicID)
		}
	}
	if got := s.Paths(); !slices.Equal(got, []string{"/browse/0/12/0/2"}) {
		t.Fatalf("запросы %v", got)
	}
}

func TestTopRejectsBadCategory(t *testing.T) {
	s := rutortest.NewServer(t)
	if _, err := newRutor(t, s).Top(ctx, "12/../x", 10); err == nil {
		t.Fatal("ошибки нет")
	}
	if len(s.Paths()) != 0 {
		t.Fatalf("ушёл запрос %v", s.Paths())
	}
}

func TestSearchMergesCategoriesWithoutDuplicates(t *testing.T) {
	s := rutortest.NewServer(t)
	rs, err := newRutor(t, s).Search(ctx, "  Матрица  1999/24 ")
	if err != nil {
		t.Fatal(err)
	}
	// Шесть запросов — по одному на видеокатегорию, запрос один и тот же.
	var cats []string
	for _, p := range s.Paths() {
		rest, ok := strings.CutSuffix(p, "/100/2/Матрица 1999 24")
		if !ok {
			t.Fatalf("адрес поиска %q", p)
		}
		cats = append(cats, strings.TrimPrefix(rest, "/search/0/"))
	}
	slices.Sort(cats)
	if !slices.Equal(cats, []string{"1", "12", "16", "4", "5", "7"}) {
		t.Fatalf("категории поиска %v", cats)
	}
	// Категории 1 и 5 отдают одну и ту же страницу: дубликаты схлопнуты, осталась категория 1.
	matrix, _ := parseList(rutortest.Page(t, "search_all_matrix.html"))
	disc, _ := parseList(rutortest.Page(t, "search_cat12_discovery.html"))
	want := map[string]string{}
	for _, r := range matrix {
		if _, ok := want[r.TopicID]; !ok {
			want[r.TopicID] = "1"
		}
	}
	for _, r := range disc {
		if _, ok := want[r.TopicID]; !ok {
			want[r.TopicID] = "12"
		}
	}
	if len(rs) != len(want) || !sortedBySeeders(rs) {
		t.Fatalf("найдено %d, нужно %d без дубликатов, по раздающим", len(rs), len(want))
	}
	for _, r := range rs {
		if want[r.TopicID] != r.CategoryID {
			t.Fatalf("%s: категория %q, нужно %q", r.TopicID, r.CategoryID, want[r.TopicID])
		}
	}
}

func TestSearchQuery(t *testing.T) {
	cases := map[string]string{
		"  Матрица   1999 ":      "Матрица 1999",
		"AC/DC":                  "AC DC",
		`C:\Фильмы`:              "C: Фильмы",
		" / ":                    "",
		strings.Repeat("я", 150): strings.Repeat("я", 100),
	}
	for in, want := range cases {
		if got := searchQuery(in); got != want {
			t.Errorf("searchQuery(%q) = %q, нужно %q", in, got, want)
		}
	}
}

func TestSearchEmptyQueryMakesNoRequests(t *testing.T) {
	s := rutortest.NewServer(t)
	if _, err := newRutor(t, s).Search(ctx, " / "); err == nil {
		t.Fatal("ошибки нет")
	}
	if len(s.Paths()) != 0 {
		t.Fatalf("ушли запросы %v", s.Paths())
	}
}

func TestSearchPartialFailureKeepsResults(t *testing.T) {
	s := rutortest.NewServer(t)
	s.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/search/0/12/") {
			w.WriteHeader(http.StatusBadGateway)
			return true
		}
		return false
	}
	rs, err := newRutor(t, s).Search(ctx, "Матрица")
	if len(rs) == 0 {
		t.Fatalf("результаты других категорий потеряны: %v", err)
	}
	// Найденное приходит вместе с ошибкой: каталог покажет его, но не сочтёт поиск полным.
	var pe *source.PartialError
	if !errors.As(err, &pe) || pe.Failed != 1 || pe.Total != 6 || !errors.Is(err, netx.ErrTrackerDown) {
		t.Fatalf("ожидалась PartialError (1 из 6, трекер недоступен), получено %v", err)
	}
	for _, r := range rs {
		if r.CategoryID == "12" {
			t.Fatal("результат из категории, которая не ответила")
		}
	}
}

// Время на поиск вышло посередине — найденное не теряется, но видно, что поиск неполный.
func TestSearchCancelledReturnsPartialResults(t *testing.T) {
	s := rutortest.NewServer(t)
	s.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/search/0/12/") {
			<-r.Context().Done() // категория молчит, пока клиент не бросит запрос
			return true
		}
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	rs, err := newRutor(t, s).Search(cctx, "Матрица")
	var pe *source.PartialError
	if len(rs) == 0 || !errors.As(err, &pe) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("найдено %d, ошибка %v — нужна PartialError с причиной «время вышло»", len(rs), err)
	}
}

func TestSearchAllFailedReturnsError(t *testing.T) {
	s := rutortest.NewServer(t)
	s.Override = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusBadGateway)
		return true
	}
	if _, err := newRutor(t, s).Search(ctx, "Матрица"); !errors.Is(err, netx.ErrTrackerDown) {
		t.Fatalf("ожидалась ErrTrackerDown, получено %v", err)
	}
}

func TestDetails(t *testing.T) {
	s := rutortest.NewServer(t)
	d, err := newRutor(t, s).Details(ctx, "1077013")
	if err != nil {
		t.Fatal(err)
	}
	if d.TopicID != "1077013" || d.Tracker != "rutor" || d.Seeders != 67 || d.KinopoiskID != "5898244" ||
		d.TorrentURL != s.Download.URL+"/download/1077013" || !strings.HasPrefix(d.Title, "Динозавры") {
		t.Fatalf("раздача: %+v", d)
	}
}

func TestDetailsOfRemovedRelease(t *testing.T) {
	s := rutortest.NewServer(t)
	_, err := newRutor(t, s).Details(ctx, "99999999")
	if !errors.Is(err, source.ErrRemoved) {
		t.Fatalf("ожидалась ErrRemoved, получено %v", err)
	}
	if got := s.Paths(); !slices.Equal(got, []string{"/torrent/99999999", "/d.php"}) {
		t.Fatalf("запросы %v", got)
	}
}

func TestTorrent(t *testing.T) {
	s := rutortest.NewServer(t)
	b, err := newRutor(t, s).Torrent(ctx, "1077013")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, rutortest.Page(t, "download_1077013.torrent")) {
		t.Fatal("скачан не тот .torrent")
	}
}

// d.rutor.info отправляет удалённую раздачу на rutor.info/d.php — это «удалена», не «чужой сайт».
func TestTorrentOfRemovedRelease(t *testing.T) {
	s := rutortest.NewServer(t)
	if _, err := newRutor(t, s).Torrent(ctx, "99999999"); !errors.Is(err, source.ErrRemoved) {
		t.Fatalf("ожидалась ErrRemoved, получено %v", err)
	}
}

func TestTorrentThatIsNotATorrent(t *testing.T) {
	s := rutortest.NewServer(t)
	s.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/download/1" {
			return false
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		io.WriteString(w, "не торрент")
		return true
	}
	if _, err := newRutor(t, s).Torrent(ctx, "1"); err == nil || !strings.Contains(err.Error(), "вместо .torrent") {
		t.Fatalf("ожидалась ошибка «вместо .torrent», получено %v", err)
	}
}

func TestParkedMirrorIsSkippedAndRemembered(t *testing.T) {
	s := rutortest.NewServer(t)
	parked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><body>Этот домен продаётся</body></html>")
	}))
	defer parked.Close()
	r := newRutor(t, s, parked.URL, s.Mirror.URL)
	if _, err := r.Top(ctx, "12", 10); err != nil {
		t.Fatal(err)
	}
	if r.Mirror() != s.Mirror.URL {
		t.Fatalf("текущее зеркало %s", r.Mirror())
	}
}

func TestBrokenMarkupKeepsMirror(t *testing.T) {
	s, spare := rutortest.NewServer(t), rutortest.NewServer(t)
	s.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.URL.Path, "/browse/") {
			return false
		}
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.Write(rutortest.Page(t, "torrent_notfound.html")) // каркас сайта есть, таблицы нет
		return true
	}
	r := newRutor(t, s, s.Mirror.URL, spare.Mirror.URL)
	_, err := r.Top(ctx, "12", 10)
	var pe *source.ErrParse
	if !errors.As(err, &pe) || pe.Block != "таблица раздач" {
		t.Fatalf("ожидалась ErrParse, получено %v", err)
	}
	if len(spare.Paths()) != 0 || r.Mirror() != s.Mirror.URL {
		t.Fatal("сломанная разметка сменила зеркало")
	}
}

func TestNotFoundKeepsMirror(t *testing.T) {
	s, spare := rutortest.NewServer(t), rutortest.NewServer(t)
	s.Override = func(w http.ResponseWriter, r *http.Request) bool {
		http.NotFound(w, r)
		return true
	}
	_, err := newRutor(t, s, s.Mirror.URL, spare.Mirror.URL).Top(ctx, "12", 10)
	if err == nil || !strings.Contains(err.Error(), "ответ 404") {
		t.Fatalf("ожидалась ошибка «ответ 404», получено %v", err)
	}
	if len(spare.Paths()) != 0 {
		t.Fatal("404 сменил зеркало")
	}
}

func TestCategories(t *testing.T) {
	s := rutortest.NewServer(t)
	cats, err := newRutor(t, s).Categories(ctx)
	if err != nil || len(cats) != 9 || cats[2].ID != "12" || cats[2].Name != "Научно-популярные фильмы" {
		t.Fatalf("категории %v, %v", cats, err)
	}
	if len(s.Paths()) != 0 {
		t.Fatal("за списком категорий не нужно ходить на трекер")
	}
}
