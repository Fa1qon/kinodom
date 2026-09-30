package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"kinodom/internal/meta"
)

// К1: номера единиц и файлов не выдаются повторно — новый фильм не наследует историю удалённого.
func TestIDsNotReused(t *testing.T) {
	e := newEnv(t)
	dir := e.folder(t, catFilms, "Movies", "a.mkv", "b.mkv")
	e.scan(t)
	var unit, file int64
	e.d.R.QueryRow(`SELECT u.id, f.id FROM lib_units u JOIN lib_files f ON f.unit = u.id WHERE u.name = 'b.mkv'`).Scan(&unit, &file)
	e.hist.SetPosition(ctx, "pc", "lib-"+strconv.FormatInt(unit, 10), int(file), 3000, 5400)
	os.Remove(filepath.Join(dir, "b.mkv"))
	e.scan(t)
	p := filepath.Join(dir, "c.mkv")
	os.WriteFile(p, []byte("x"), 0o644)
	os.Chtimes(p, old, old)
	e.scan(t)
	var nu, nf int64
	e.d.R.QueryRow(`SELECT u.id, f.id FROM lib_units u JOIN lib_files f ON f.unit = u.id WHERE u.name = 'c.mkv'`).Scan(&nu, &nf)
	if nu == unit || nf == file {
		t.Errorf("номера выданы повторно: единица %d→%d, файл %d→%d", unit, nu, file, nf)
	}
	if v := e.list(t, "pc", 0); len(v.Continue) != 0 {
		t.Errorf("новый фильм унаследовал историю удалённого: %+v", v.Continue)
	}
}

// В1: «Это другой фильм» у скачанной раздачи переживает обход.
func TestLinkSurvivesScan(t *testing.T) {
	e := newEnv(t)
	e.kp.details[777] = meta.FilmDetails{Film: meta.Film{ID: 777, NameRu: "Другой", Year: 2020, Type: "FILM"}}
	e.dl.set(malahit(5))
	e.scan(t)
	var unit int64
	e.d.R.QueryRow(`SELECT id FROM lib_units`).Scan(&unit)
	if err := e.l.LinkKP(ctx, unit, "https://www.kinopoisk.ru/film/777/"); err != nil {
		t.Fatal(err)
	}
	e.scan(t)
	var kp int
	e.d.R.QueryRow(`SELECT kp_id FROM lib_units WHERE id = ?`, unit).Scan(&kp)
	if kp != 777 {
		t.Errorf("ручная привязка откатилась: kp %d", kp)
	}
}

// В2: раздача ещё не загружена (старт, отключённый диск) — единица и данные карточки не пропадают.
func TestMissingTorrentKeepsUnit(t *testing.T) {
	e := newEnv(t)
	e.dl.set(malahit(5))
	e.scan(t)
	var added int64
	e.d.R.QueryRow(`SELECT added_at FROM lib_units`).Scan(&added)
	e.dl.set(TorrentUnit{Hash: hashA, Missing: true})
	e.scan(t)
	calls := e.dl.calls
	if v := e.list(t, "pc", 0); len(v.Cards) != 0 {
		t.Errorf("незагруженная раздача показывается: %+v", v.Cards)
	}
	if e.l.scanState().Running || e.dl.calls != calls+1 {
		t.Errorf("незагруженная раздача запускает обходы: вызовов %d → %d", calls, e.dl.calls)
	}
	u := malahit(5)
	u.Release = nil
	e.dl.set(u)
	e.scan(t)
	var n int
	var added2 int64
	e.d.R.QueryRow(`SELECT COUNT(*), MAX(added_at) FROM lib_units`).Scan(&n, &added2)
	c, err := e.l.Card(ctx, "pc", "kp-5")
	if n != 1 || added2 != added || err != nil || c.Title != "Малахит" {
		t.Errorf("после загрузки: единиц %d, появилась %d→%d, карточка %+v (%v)", n, added, added2, c.CardSummary, err)
	}
}

// В3: единица, на которой Кинопоиск стабильно отвечает 500, не останавливает остальные; после трёх
// неудач — «Не распознано».
func TestBrokenUnitDoesNotBlock(t *testing.T) {
	e := newEnv(t)
	crimeKP(e)
	e.kp.errs["Матрица"] = &meta.ServiceError{Status: 500}
	e.folder(t, catFilms, "Movies", "Матрица.mkv")
	e.clk.add(time.Minute)
	e.folder(t, catFilms, "Movies2", "Crime.101.2026.WEBRip.mkv")
	e.scan(t)
	state := func(name string) string {
		var s string
		e.d.R.QueryRow(`SELECT state FROM lib_units WHERE name = ?`, name).Scan(&s)
		return s
	}
	if state("Crime.101.2026.WEBRip.mkv") != StateFound {
		t.Fatalf("соседняя единица не распознана: %s", state("Crime.101.2026.WEBRip.mkv"))
	}
	count := func() int {
		n := 0
		for _, c := range e.kp.calls {
			if c == "Матрица" {
				n++
			}
		}
		return n
	}
	was := count()
	e.scan(t)
	if count() != was {
		t.Errorf("сбойная единица ищется на каждом обходе: %d → %d", was, count())
	}
	for i := 0; i < 3; i++ {
		e.clk.add(7 * time.Hour)
		e.scan(t)
	}
	if state("Матрица.mkv") != StateUnrecognized {
		t.Errorf("после трёх неудач: %s", state("Матрица.mkv"))
	}
}

// В4: короткий файл, досмотренный до конца, — «просмотрено», несмотря на поправку чтения впереди.
func TestMediaShortFileWatched(t *testing.T) {
	quick(t)
	mediaExtraLead = 12 << 20
	e, file, unit, _ := withFile(t, "lesson.mp4", make([]byte, 1000))
	get(t, mediaMux(e.l), mediaURL(file, "lesson.mp4"), fromPhone)
	e.clk.add(time.Minute)
	e.l.tracker.Tick(e.clk.now())
	fs, _ := e.hist.Files(ctx, "192.168.0.50", "lib-"+strconv.FormatInt(unit, 10))
	if len(fs) != 1 || !fs[0].Watched {
		t.Errorf("досмотренный урок не отмечен: %+v", fs)
	}
}

// В5: версия из скрытой категории не видна в карточке видимой и не уводит её в скрытую.
func TestHiddenVersionNotMerged(t *testing.T) {
	e := newEnv(t)
	crimeKP(e)
	adult, _ := e.d.addCategory(ctx, CategoryInput{Name: "18+", Layout: LayoutFilms, Kinopoisk: true, Hidden: true})
	e.folder(t, adult, "Private", "Crime.101.2026.mkv")
	e.clk.add(time.Minute)
	e.folder(t, catFilms, "Movies", "Crime.101.2026.WEBRip.mkv")
	e.scan(t)
	v := e.list(t, "pc", catFilms)
	c, err := e.l.Card(ctx, "pc", "kp-10")
	if len(v.Cards) != 1 || v.Cards[0].Dupes || err != nil || len(c.Versions) != 1 || c.Category != catFilms {
		t.Errorf("на устройстве без «18+»: карточки %+v, версий %d (%v)", v.Cards, len(c.Versions), err)
	}
	e.d.setDeviceCategory(ctx, "pc", adult, true)
	if c, _ := e.l.Card(ctx, "pc", "kp-10"); len(c.Versions) != 2 {
		t.Errorf("с включённой «18+» версий %d, нужно 2", len(c.Versions))
	}
}

type panicDownloads struct{}

func (panicDownloads) TorrentUnits(context.Context) ([]TorrentUnit, error) {
	panic("сбой в обходе")
}

// М11: паника в обходе не роняет сервер.
func TestScanPanicDoesNotCrash(t *testing.T) {
	e := newEnv(t)
	e.l.o.Downloads = panicDownloads{}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.l.start(runCtx)
	e.l.Scan()
	deadline := time.Now().Add(5 * time.Second)
	for e.l.scanState().Running {
		if time.Now().After(deadline) {
			t.Fatal("обход не закончился")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// М7: папка загрузок внутри папки категории — не обходится (скачанное и так в медиатеке, а
// недокачанные файлы там пустые).
func TestDownloadsDirInsideCategorySkipped(t *testing.T) {
	e := newEnv(t)
	share := e.folder(t, catFilms, "Share", "film.mkv")
	dl := mkdir(t, share, "Kinodom")
	p := filepath.Join(dl, "partial.mkv")
	os.WriteFile(p, []byte("x"), 0o644)
	os.Chtimes(p, old, old)
	e.l.o.DownloadsDir = func() string { return dl }
	e.scan(t)
	var n int
	e.d.R.QueryRow(`SELECT COUNT(*) FROM lib_units`).Scan(&n)
	if n != 1 {
		t.Errorf("единиц %d: папка загрузок обошлась", n)
	}
}

var _ = errors.New
