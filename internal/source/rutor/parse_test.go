package rutor

import (
	"errors"
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
