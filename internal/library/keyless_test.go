package library

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"kinodom/internal/meta"
)

func unitState(t *testing.T, e *env, name string) string {
	t.Helper()
	var st string
	if err := e.d.R.QueryRow(`SELECT state FROM lib_units WHERE name = ?`, name).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// Кинопоиск без токена на паузе (отказ сайта) — единицы ждут, а не уходят в «Не распознано»
// (Review Focus 4; спека 11b, 5.6).
func TestLibraryKeylessWaitsOnPause(t *testing.T) {
	e := newEnv(t)
	e.kp.errs["*"] = meta.ErrKPBlocked
	e.folder(t, catFilms, "Movies", "Crime.101.2026.WEBRip.mkv", "Pandorum.2009.BDRip.mkv")
	e.scan(t)
	e.scan(t)
	e.scan(t)
	for _, n := range []string{"Crime.101.2026.WEBRip.mkv", "Pandorum.2009.BDRip.mkv"} {
		if st := unitState(t, e, n); st != StateWait && st != StateNew {
			t.Errorf("%s: %s — на паузе сайта единица ждёт", n, st)
		}
	}
	if len(e.kp.calls) != 1 {
		t.Errorf("на паузе — один запрос, потом пауза для всех: %q", e.kp.calls)
	}
}

// Х9: постер Кинопоиска карточки не скачался — повтор с паузой (без нового запроса описания).
func TestKPPosterRetried(t *testing.T) {
	e := newEnv(t)
	crimeKP(e)
	e.ps.fail = map[string]int{"http://kp/10.jpg": 1, "http://kp-by-number/10.jpg": 1}
	e.l.o.KPPoster = func(id int) string { return fmt.Sprintf("http://kp-by-number/%d.jpg", id) }
	e.folder(t, catFilms, "Movies", "Crime.101.2026.WEBRip.mkv")
	e.scan(t)
	if c, _ := e.l.Card(ctx, "pc", "kp-10"); c.Poster != "" {
		t.Fatalf("постер не скачался — пусто: %q", c.Poster)
	}
	details, fetched := len(e.kp.calls), len(e.ps.fetched)
	e.scan(t)
	if len(e.kp.calls) != details || len(e.ps.fetched) != fetched {
		t.Fatalf("повтор раньше паузы: запросы %q, картинки %q", e.kp.calls, e.ps.fetched)
	}
	e.clk.add(11 * time.Minute)
	e.scan(t) // второй адрес ещё раз не отдаёт
	e.clk.add(61 * time.Minute)
	e.scan(t)
	if c, _ := e.l.Card(ctx, "pc", "kp-10"); c.Poster != "/img/"+meta.ImageKey("http://kp-by-number/10.jpg") {
		t.Fatalf("после паузы — постер по номеру: %q (картинки %q)", c.Poster, e.ps.fetched)
	}
	if len(e.kp.calls) != details {
		t.Fatalf("повтор постера не запрашивает описание снова: %q", e.kp.calls)
	}
}

// Х9: описание не загрузилось — повтор с паузой, а не на каждом обходе.
func TestKPDetailsFailureBacksOff(t *testing.T) {
	e := newEnv(t)
	crimeKP(e)
	e.kp.errs["details"] = errors.New("сбой сервиса")
	e.folder(t, catFilms, "Movies", "Crime.101.2026.WEBRip.mkv")
	e.scan(t)
	n := len(e.kp.calls)
	e.scan(t)
	if len(e.kp.calls) != n {
		t.Fatalf("описание просится на каждом обходе: %q", e.kp.calls)
	}
	e.kp.mu.Lock()
	delete(e.kp.errs, "details")
	e.kp.mu.Unlock()
	e.clk.add(11 * time.Minute)
	e.scan(t)
	if c, err := e.l.Card(ctx, "pc", "kp-10"); err != nil || c.Description != "desc10" {
		t.Fatalf("после паузы: %+v, %v", c, err)
	}
}

// Х10: правка единицы распознаёт только её, а не всю очередь (остальное — фоновым обходом).
func TestUnitEditRecognizesOnlyThatUnit(t *testing.T) {
	e := newEnv(t)
	crimeKP(e)
	e.kp.errs["*"] = meta.ErrQuota
	e.folder(t, catFilms, "Movies", "Crime.101.2026.WEBRip.mkv", "Pandorum.2009.BDRip.mkv", "Andor.2022.WEB-DL.mkv")
	e.scan(t) // первая — «ждать», пауза для всех: остальные — «новые»
	e.kp.mu.Lock()
	delete(e.kp.errs, "*")
	e.kp.calls = nil
	e.kp.mu.Unlock()
	var unit int64
	if err := e.d.R.QueryRow(`SELECT id FROM lib_units WHERE name = 'Crime.101.2026.WEBRip.mkv'`).Scan(&unit); err != nil {
		t.Fatal(err)
	}
	if err := e.l.SearchAgain(ctx, unit); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.kp.calls {
		if strings.HasPrefix(c, "Pandorum") || strings.HasPrefix(c, "Andor") {
			t.Fatalf("правка одной единицы распознаёт очередь: %q", e.kp.calls)
		}
	}
	if st := unitState(t, e, "Crime.101.2026.WEBRip.mkv"); st != StateFound {
		t.Fatalf("правленая единица: %s (%q)", st, e.kp.calls)
	}
	if st := unitState(t, e, "Pandorum.2009.BDRip.mkv"); st != StateNew {
		t.Fatalf("чужая единица распознана синхронно: %s", st)
	}
}

// Х11: скачанный фильм с дополнениями (несколько файлов) и номером Кинопоиска — «Фильмы» по виду с
// Кинопоиска, а не «Сериалы» по числу файлов.
func TestDownloadedFilmWithExtrasIsFilm(t *testing.T) {
	e := newEnv(t)
	e.rt.films[10] = meta.Rating{KinopoiskID: 10, Kinopoisk: 7.2, Type: "FILM"}
	u := malahit(10)
	u.Name = "Crime.101.2026.WEB-DL.1080p"
	u.Files = []TorrentFile{
		{Index: 0, Path: "Crime/Crime.101.2026.mkv", Size: 100, Done: 100, Stored: true, Readiness: "done"},
		{Index: 1, Path: "Crime/Extras/Making.of.mkv", Size: 10, Done: 10, Stored: true, Readiness: "done"},
		{Index: 2, Path: "Crime/Extras/Deleted.scenes.mkv", Size: 10, Done: 10, Stored: true, Readiness: "done"},
	}
	e.dl.set(u)
	e.scan(t)
	v := e.list(t, "pc", 0)
	if len(v.Cards) != 1 || v.Cards[0].Category != catFilms {
		t.Fatalf("фильм с дополнениями: %+v", v.Cards)
	}
}
