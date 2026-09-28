package rutor

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"kinodom/internal/source"
	"kinodom/internal/source/rutor/rutortest"
)

// gib — 1 ГиБ во float64: 3.87 ГиБ не целое число байт, и Go не переводит такую константу
// в int64 при компиляции — считаем во время выполнения, как parseSize.
var gib = float64(1 << 30)

func TestParseListAllSamples(t *testing.T) {
	total := 0
	for _, name := range rutortest.ListPages {
		rs, err := parseList(rutortest.Page(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for i, r := range rs {
			if r.TopicID == "" || r.Title == "" || len(r.InfoHash) != 40 || r.Added.IsZero() || r.Size <= 0 || r.Tracker != Name {
				t.Errorf("%s, строка %d: неполная %+v", name, i, r)
			}
		}
		total += len(rs)
	}
	if total != 1289 {
		t.Errorf("разобрано %d строк, в образцах 1289", total)
	}
}

func TestParseListFirstRow(t *testing.T) {
	rs, err := parseList(rutortest.Page(t, "browse_12_sort2.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 100 {
		t.Fatalf("строк %d, нужно 100", len(rs))
	}
	got := rs[0]
	want := source.Release{
		Tracker: "rutor", TopicID: "1077013",
		Title:   "Динозавры / The Dinosaurs [S01] (2026) WEB-DL 720p от New-Team | P1 | LostFilm, WinMedia",
		Seeders: 103, Leechers: 5, Size: int64(3.87 * gib),
		InfoHash: "58cc11266c861f44df75f6f24328cd9ae3778218",
	}
	if wantDate := time.Date(2026, 3, 15, 0, 0, 0, 0, msk); !got.Added.Equal(wantDate) {
		t.Errorf("дата %v, нужно %v", got.Added, wantDate)
	}
	got.Added = time.Time{}
	if got != want {
		t.Errorf("первая строка:\n%+v\nнужно\n%+v", got, want)
	}
}

// Поиск без результатов: таблица с заголовком есть, строк нет — это не ошибка.
func TestParseListEmptySearch(t *testing.T) {
	rs, err := parseList(rutortest.Page(t, "search_empty.html"))
	if err != nil || len(rs) != 0 {
		t.Fatalf("пустой поиск: %d строк, %v", len(rs), err)
	}
}

func TestParseListGarbage(t *testing.T) {
	cases := map[string]struct{ body, block string }{
		"пустой ответ":         {"", "таблица раздач"},
		"чужая страница":       {"<html><body>Технические работы</body></html>", "таблица раздач"},
		"страница без таблицы": {string(rutortest.Page(t, "torrent_notfound.html")), "таблица раздач"},
		"строки без ссылок": {`<div id="index"><table><tr class="backgr"><td>Добавлен</td></tr>` +
			`<tr class="gai"><td>1</td><td>2</td><td>3</td><td>4</td></tr></table></div>`, "строки таблицы раздач"},
	}
	for name, c := range cases {
		_, err := parseList([]byte(c.body))
		var pe *source.ErrParse
		if !errors.As(err, &pe) || pe.Block != c.block || pe.Tracker != "Rutor" {
			t.Errorf("%s: ожидалась ErrParse{%q}, получено %v", name, c.block, err)
		}
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"3.87 GB":  int64(3.87 * gib),
		"812.5 MB": int64(812.5 * (1 << 20)),
		"1.00 TB":  1 << 40,
		"512 kB":   512 << 10,
		"мусор":    0,
		"":         0,
	}
	for in, want := range cases {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d, нужно %d", in, got, want)
		}
	}
}

func TestParseListDate(t *testing.T) {
	if got, want := parseListDate("28 Сен 26"), time.Date(2026, 9, 28, 0, 0, 0, 0, msk); !got.Equal(want) {
		t.Errorf("28 Сен 26 → %v", got)
	}
	for _, bad := range []string{"", "28 Sep 26", "вчера"} {
		if got := parseListDate(bad); !got.IsZero() {
			t.Errorf("%q → %v, нужна пустая дата", bad, got)
		}
	}
}

func mustTopic(t *testing.T, name string) source.Details {
	t.Helper()
	u, _ := url.Parse("https://rutor.info/torrent/1")
	d, err := parseTopic(rutortest.Page(t, name), u)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return d
}

func TestParseTopic(t *testing.T) {
	d := mustTopic(t, "torrent_1077013.html")
	checks := []struct{ field, got, want string }{
		{"Title", d.Title, "Динозавры / The Dinosaurs [S01] (2026) WEB-DL 720p от New-Team | P1 | LostFilm, WinMedia"},
		{"Tracker", d.Tracker, "rutor"},
		{"CategoryID", d.CategoryID, "12"},
		{"InfoHash", d.InfoHash, "58cc11266c861f44df75f6f24328cd9ae3778218"},
		{"PosterURL", d.PosterURL, "https://i8.imageban.ru/out/2026/03/14/8e3986dbaaf516b0a8fbc3d6928911f6.jpg"},
		{"KinopoiskID", d.KinopoiskID, "5898244"},
		{"IMDbID", d.IMDbID, "tt32493765"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, нужно %q", c.field, c.got, c.want)
		}
	}
	if d.Seeders != 67 || d.Leechers != 4 || d.Size != 4153309470 {
		t.Errorf("раздают %d, качают %d, размер %d", d.Seeders, d.Leechers, d.Size)
	}
	if want := time.Date(2026, 3, 15, 0, 52, 32, 0, msk); !d.Added.Equal(want) {
		t.Errorf("добавлена %v, нужно %v", d.Added, want)
	}
	if !strings.HasPrefix(d.Magnet, "magnet:?xt=urn:btih:58cc11266c861f44df75f6f24328cd9ae3778218&dn=") {
		t.Errorf("magnet %q", d.Magnet)
	}
}

func TestParseTopicAllSamples(t *testing.T) {
	names := []string{"torrent_1020689.html", "torrent_1077013.html", "torrent_1084240.html", "torrent_1096166.html",
		"torrent_1104880.html", "torrent_1104883.html", "torrent_1104912.html", "torrent_1107538.html", "torrent_1107862.html"}
	withKP := 0
	for _, name := range names {
		d := mustTopic(t, name)
		if d.Title == "" || len(d.InfoHash) != 40 || !strings.HasPrefix(d.PosterURL, "https://") ||
			d.CategoryID == "" || d.Size <= 0 || d.Added.IsZero() || d.Seeders <= 0 || len([]rune(d.Description)) < 100 {
			t.Errorf("%s: неполная раздача %+v", name, d.Release)
		}
		if d.KinopoiskID != "" {
			withKP++
		}
	}
	if withKP != 8 {
		t.Errorf("ссылка на Кинопоиск найдена в %d раздачах из 9, в образцах — в 8", withKP)
	}
}

func TestParseTopicDescriptionIsPlainText(t *testing.T) {
	d := mustTopic(t, "torrent_1084240.html")
	for _, want := range []string{"Год выхода: 1999", "Описание: Что есть реальность?"} {
		if !strings.Contains(d.Description, want) {
			t.Errorf("в описании нет %q:\n%s", want, d.Description)
		}
	}
	for _, bad := range []string{"<", "imageban", "\n\n\n"} {
		if strings.Contains(d.Description, bad) {
			t.Errorf("в описании есть %q (разметка или спойлер):\n%s", bad, d.Description)
		}
	}
}

func TestParseTopicResolvesRelativePoster(t *testing.T) {
	body := `<h1>Фильм</h1><div id="download"><a href="magnet:?xt=urn:btih:` + strings.Repeat("ab", 20) + `">m</a></div>` +
		`<table id="details"><tr><td></td><td><img src="//cdn.example/p.jpg"></td></tr></table>`
	u, _ := url.Parse("https://rutor.is/torrent/1")
	d, err := parseTopic([]byte(body), u)
	if err != nil || d.PosterURL != "https://cdn.example/p.jpg" {
		t.Fatalf("постер %q, %v", d.PosterURL, err)
	}
}

func TestParseTopicMissingBlocks(t *testing.T) {
	magnet := `<div id="download"><a href="magnet:?xt=urn:btih:` + strings.Repeat("ab", 20) + `">m</a></div>`
	details := `<table id="details"><tr><td></td><td>текст</td></tr></table>`
	cases := map[string]string{
		"table#details": string(rutortest.Page(t, "torrent_notfound.html")),
		"magnet":        `<h1>Фильм</h1>` + details,
		"h1":            magnet + details,
	}
	u, _ := url.Parse("https://rutor.info/torrent/1")
	for want, body := range cases {
		_, err := parseTopic([]byte(body), u)
		var pe *source.ErrParse
		if !errors.As(err, &pe) || !strings.Contains(pe.Block, want) {
			t.Errorf("ожидалась ErrParse с блоком %q, получено %v", want, err)
		}
	}
}
