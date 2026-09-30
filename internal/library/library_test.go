package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"kinodom/internal/history"
	"kinodom/internal/meta"
)

var ctx = context.Background()

type fakeDownloads struct {
	mu    sync.Mutex
	units []TorrentUnit
	calls int
	block chan struct{} // не nil — TorrentUnits ждёт, пока канал не закроют (обход «идёт»)
	err   error
}

func (f *fakeDownloads) TorrentUnits(context.Context) ([]TorrentUnit, error) {
	f.mu.Lock()
	f.calls++
	b := f.block
	f.mu.Unlock()
	if b != nil {
		<-b
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.units), f.err
}

func (f *fakeDownloads) set(us ...TorrentUnit) {
	f.mu.Lock()
	f.units = us
	f.mu.Unlock()
}

type fakeRatings struct {
	mu    sync.Mutex
	films map[int]meta.Rating
}

func (f *fakeRatings) Films(_ context.Context, ids []int) (map[int]meta.Rating, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]meta.Rating{}
	for _, id := range ids {
		if r, ok := f.films[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func (f *fakeRatings) AddFilm(_ context.Context, m meta.Film) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.films[m.ID] = meta.Rating{KinopoiskID: m.ID, Kinopoisk: m.Rating, IMDb: m.RatingIMDb, NameRu: m.NameRu, NameOrig: m.NameOrig, Year: m.Year}
	return nil
}

type fakePosters struct {
	mu      sync.Mutex
	fetched []string
}

func (f *fakePosters) Fetch(_ context.Context, src string, via meta.Via) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = append(f.fetched, src)
	if via != meta.Direct {
		return "", errors.New("постер Кинопоиска — напрямую")
	}
	return meta.ImageKey(src), nil
}

type libClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *libClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *libClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	l    *Library
	d    db
	kp   *fakeKP
	dl   *fakeDownloads
	rt   *fakeRatings
	ps   *fakePosters
	hist *history.Service
	clk  *libClock
	root string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := openDB(t)
	e := &env{d: d, kp: &fakeKP{search: map[string][]meta.Film{}, errs: map[string]error{}, details: map[int]meta.FilmDetails{}},
		dl: &fakeDownloads{}, rt: &fakeRatings{films: map[int]meta.Rating{}}, ps: &fakePosters{}, hist: history.New(d.DB),
		clk: &libClock{t: time.Now()}, root: t.TempDir()}
	dlDir := mkdir(t, e.root, "Downloads")
	e.l = New(Options{DB: d.DB, KP: e.kp, Ratings: e.rt, Posters: e.ps, Downloads: e.dl, History: e.hist,
		DownloadsDir: func() string { return dlDir }, KeepDays: func() int { return 14 }, Now: e.clk.now})
	return e
}

// folder — папка в категории; файлы — по путям внутри неё.
func (e *env) folder(t *testing.T, category int64, name string, files ...string) string {
	t.Helper()
	dir := mkdir(t, e.root, name)
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		mkdir(t, filepath.Dir(p))
		os.WriteFile(p, []byte("x"), 0o644)
		os.Chtimes(p, old, old)
	}
	cs, err := e.d.categories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.ID == category {
			paths := []string{dir}
			for _, f := range c.Folders {
				paths = append(paths, f.Path)
			}
			if err := e.d.updateCategory(ctx, c.ID, CategoryInput{Name: c.Name, Layout: c.Layout, Kinopoisk: c.Kinopoisk, Hidden: c.Hidden, Folders: paths}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

func (e *env) scan(t *testing.T) {
	t.Helper()
	if err := e.l.scanNow(ctx); err != nil {
		t.Fatal(err)
	}
}

func (e *env) list(t *testing.T, device string, category int64) ListView {
	t.Helper()
	v, err := e.l.List(ctx, device, category)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func keysOf(cs []CardSummary) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Key)
	}
	return strings.Join(out, " ")
}

const (
	catFilms  = 1
	catSeries = 2
)

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func malahit(kp int) TorrentUnit {
	return TorrentUnit{Hash: hashA, Name: "Malahit.S01.2026.WEB-DL.1080p", Dir: `C:\dl\Malahit`,
		Files: []TorrentFile{
			{Index: 0, Path: "Malahit/Malahit.S01E01.mkv", Size: 100, Done: 100, Stored: true, Readiness: "done"},
			{Index: 1, Path: "Malahit/Malahit.S01E02.mkv", Size: 100, Done: 50, Stored: true, Readiness: "wait"},
		},
		Release: &ReleaseData{KP: kp, Title: "Малахит (2026) WEB-DL 1080p", NameRu: "Малахит", Year: 2026, Description: "desc5",
			ImageKey: "rel-img", Quality: "WEB-DL 1080p"}}
}

func crimeKP(e *env) {
	e.kp.search["Crime 101"] = []meta.Film{film(10, "Ограбление в Лос-Анджелесе", "Crime 101", 2026, "FILM")}
	e.kp.details[10] = meta.FilmDetails{Film: meta.Film{ID: 10, NameRu: "Ограбление в Лос-Анджелесе", NameOrig: "Crime 101", Year: 2026,
		Type: "FILM", Rating: 7.2}, Description: "desc10", Genres: []string{"триллер"}, PosterURL: "http://kp/10.jpg"}
}

// Единица из папки и из раздачи: у папки — данные с Кинопоиска, у скачанного — со страницы раздачи.
func TestFolderAndTorrentUnits(t *testing.T) {
	e := newEnv(t)
	crimeKP(e)
	e.folder(t, catFilms, "Movies", "Crime.101.2026.WEBRip.mkv")
	e.dl.set(malahit(5))
	e.scan(t)
	v := e.list(t, "pc", 0)
	if keysOf(v.Cards) != "kp-5 kp-10" && keysOf(v.Cards) != "kp-10 kp-5" {
		t.Fatalf("карточки: %+v", v.Cards)
	}
	for _, c := range v.Cards {
		switch c.Key {
		case "kp-10":
			if c.Title != "Ограбление в Лос-Анджелесе" || c.Year != 2026 || c.Poster != "/img/"+meta.ImageKey("http://kp/10.jpg") ||
				c.Category != catFilms || c.Rating != 7.2 || c.Dupes || c.Downloading {
				t.Errorf("фильм из папки: %+v", c)
			}
		case "kp-5":
			if c.Title != "Малахит" || c.Poster != "/img/rel-img" || c.Category != catSeries || !c.Downloading || c.DeleteInDays != nil {
				t.Errorf("скачанный сериал: %+v", c)
			}
		}
	}
	card, err := e.l.Card(ctx, "pc", "kp-10")
	if err != nil || card.Description != "desc10" || !slices.Equal(card.Genres, []string{"триллер"}) || len(card.Versions) != 1 ||
		len(card.Versions[0].Episodes) != 1 || card.Versions[0].Hash == "" || !strings.HasPrefix(card.Versions[0].Hash, "lib-") {
		t.Errorf("карточка фильма: %+v (%v)", card, err)
	}
}

// Данные карточки скачанного живут после того, как каталог забыл раздачу; второй обход ничего не
// скачивает заново (критерий 3).
func TestCardDataStored(t *testing.T) {
	e := newEnv(t)
	crimeKP(e)
	e.folder(t, catFilms, "Movies", "Crime.101.2026.WEBRip.mkv")
	e.dl.set(malahit(5))
	e.scan(t)
	calls, fetched := len(e.kp.calls), len(e.ps.fetched)
	u := malahit(5)
	u.Release = nil // каталог почистил раздачу
	e.dl.set(u)
	e.scan(t)
	if len(e.kp.calls) != calls || len(e.ps.fetched) != fetched {
		t.Errorf("второй обход ходил в Кинопоиск или за картинками: %q %q", e.kp.calls, e.ps.fetched)
	}
	card, err := e.l.Card(ctx, "pc", "kp-5")
	if err != nil || card.Title != "Малахит" || card.Description != "desc5" || card.Poster != "/img/rel-img" {
		t.Errorf("карточка без раздачи в каталоге: %+v (%v)", card, err)
	}
	keys, _ := e.l.ImageKeys(ctx)
	if !keys["rel-img"] || !keys[meta.ImageKey("http://kp/10.jpg")] {
		t.Errorf("ключи постеров: %v", keys)
	}
}

// Один номер в папке и в загрузках — одна карточка с дублями; данные — со страницы раздачи.
func TestDupes(t *testing.T) {
	e := newEnv(t)
	crimeKP(e)
	e.folder(t, catFilms, "Movies", "Crime.101.2026.WEBRip.mkv")
	u := malahit(10)
	u.Release.NameRu, u.Release.Description = "Ограбление (раздача)", "desc-release"
	e.dl.set(u)
	e.scan(t)
	v := e.list(t, "pc", 0)
	if len(v.Cards) != 1 || !v.Cards[0].Dupes || v.Cards[0].Title != "Ограбление (раздача)" || v.Cards[0].Category != catFilms {
		t.Fatalf("дубли: %+v", v.Cards)
	}
	card, _ := e.l.Card(ctx, "pc", "kp-10")
	if len(card.Versions) != 2 || card.Description != "desc-release" {
		t.Errorf("версии: %+v", card)
	}
}

// Категория карточки: ручной перенос, папка, тип; своя категория без Кинопоиска — название из имени,
// Кинопоиск не спрашивают; новые сверху.
func TestCategoriesAndPlain(t *testing.T) {
	e := newEnv(t)
	study, err := e.d.addCategory(ctx, CategoryInput{Name: "Обучение", Layout: LayoutSeries})
	if err != nil {
		t.Fatal(err)
	}
	e.folder(t, study, "Study", "Go курс (2024) WEBRip/1. Введение/a.mp4")
	e.scan(t)
	e.clk.add(time.Hour)
	e.dl.set(malahit(5))
	e.scan(t)
	if len(e.kp.calls) != 0 {
		t.Errorf("категория без Кинопоиска спрашивала его: %q", e.kp.calls)
	}
	v := e.list(t, "pc", 0)
	if len(v.Cards) != 2 || v.Cards[0].Key != "kp-5" || !strings.HasPrefix(v.Cards[1].Key, "u-") {
		t.Fatalf("порядок — новые сверху: %+v", v.Cards)
	}
	plain := v.Cards[1]
	if plain.Title != "Go курс" || plain.Year != 2024 || plain.Category != study {
		t.Errorf("своя категория: %+v", plain)
	}
	if err := e.l.SetCardCategory(ctx, "kp-5", &study); err != nil {
		t.Fatal(err)
	}
	if v := e.list(t, "pc", study); keysOf(v.Cards) != "kp-5 "+plain.Key {
		t.Errorf("после переноса в «Обучение»: %s", keysOf(v.Cards))
	}
	if err := e.l.SetCardCategory(ctx, "kp-5", nil); err != nil {
		t.Fatal(err)
	}
	if v := e.list(t, "pc", catSeries); keysOf(v.Cards) != "kp-5" {
		t.Errorf("перенос снят: %s", keysOf(v.Cards))
	}
}

// Review Focus 1: скрытая категория не видна нигде на устройстве, где её не включили.
func TestHiddenCategoryEverywhere(t *testing.T) {
	e := newEnv(t)
	adult, _ := e.d.addCategory(ctx, CategoryInput{Name: "18+", Layout: LayoutFilms, Hidden: true})
	e.folder(t, adult, "Adult", "film.mkv")
	e.scan(t)
	var unit int64
	e.d.R.QueryRow(`SELECT id FROM lib_units`).Scan(&unit)
	var file int64
	e.d.R.QueryRow(`SELECT id FROM lib_files`).Scan(&file)
	hash := "lib-" + itoa(int(unit))
	e.hist.SetPosition(ctx, "pc", hash, int(file), 600, 5400)
	key := "u-" + itoa(int(unit))
	v := e.list(t, "pc", 0)
	info, _ := e.l.HistoryInfo(ctx, "pc", []string{hash})
	_, cardErr := e.l.Card(ctx, "pc", key)
	for _, c := range v.Categories {
		if c.ID == adult {
			t.Errorf("скрытая категория в списке категорий")
		}
	}
	if len(v.Cards) != 0 || len(v.Continue) != 0 || len(info) != 0 || !errors.Is(cardErr, ErrNoCard) {
		t.Errorf("скрытая видна: карточки %d, продолжить %d, история %d, карточка %v", len(v.Cards), len(v.Continue), len(info), cardErr)
	}
	if hidden, _ := e.l.HiddenHashes(ctx, "pc", []string{hash, "ffff"}); !hidden[hash] || hidden["ffff"] {
		t.Errorf("скрытые «раздачи» истории: %v", hidden)
	}
	if names, n, _ := e.l.HistoryFiles(ctx, hash); n != 1 || names[int(file)] != "film.mkv" {
		t.Errorf("файлы для истории: %v %d", names, n)
	}
	if err := e.d.setDeviceCategory(ctx, "pc", adult, true); err != nil {
		t.Fatal(err)
	}
	v = e.list(t, "pc", 0)
	info, _ = e.l.HistoryInfo(ctx, "pc", []string{hash})
	if len(v.Cards) != 1 || len(v.Continue) != 1 || len(info) != 1 {
		t.Errorf("включённая на устройстве: карточки %d, продолжить %d, история %d", len(v.Cards), len(v.Continue), len(info))
	}
	if hidden, _ := e.l.HiddenHashes(ctx, "pc", []string{hash}); hidden[hash] {
		t.Errorf("включённая считается скрытой")
	}
	if v := e.list(t, "192.168.0.60", 0); len(v.Cards) != 0 {
		t.Errorf("на другом устройстве видна")
	}
}

// Недоступная папка категории: единицы не удаляются и не показываются; вернулась — те же единицы.
func TestMissingFolderKeepsUnits(t *testing.T) {
	e := newEnv(t)
	dir := e.folder(t, catFilms, "Movies", "a.mkv", "b.mkv")
	e.scan(t)
	var before []int64
	rows, _ := e.d.R.Query(`SELECT id FROM lib_files ORDER BY id`)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		before = append(before, id)
	}
	rows.Close()
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	e.scan(t)
	if v := e.list(t, "pc", 0); len(v.Cards) != 0 {
		t.Errorf("единицы недоступной папки показываются")
	}
	if cs, _ := e.l.Categories(ctx, "pc"); cs[0].Folders[0].Problem != "not_found" {
		t.Errorf("проблема папки: %+v", cs[0].Folders)
	}
	problem := func() string {
		ps, _ := e.d.Problems(ctx)
		for _, p := range ps {
			if strings.HasPrefix(p.ID, "library.folder.") {
				return p.Text
			}
		}
		return ""
	}
	if p := problem(); !strings.Contains(p, dir) || !strings.Contains(p, "Фильмы") {
		t.Errorf("проблема в «Состоянии»: %q", p)
	}
	os.Rename(moved, dir)
	e.scan(t)
	var after []int64
	rows, _ = e.d.R.Query(`SELECT id FROM lib_files ORDER BY id`)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		after = append(after, id)
	}
	rows.Close()
	if v := e.list(t, "pc", 0); len(v.Cards) != 2 || !slices.Equal(before, after) {
		t.Errorf("папка вернулась: карточек %d, файлы %v → %v", len(v.Cards), before, after)
	}
	if p := problem(); p != "" {
		t.Errorf("проблема осталась: %q", p)
	}
}

// Review Focus 3: два обхода сразу — идёт один, второй отвечает «уже идёт»; единицы не удваиваются.
func TestScanSingleFlight(t *testing.T) {
	e := newEnv(t)
	e.folder(t, catFilms, "Movies", "a.mkv")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.l.start(runCtx)
	e.dl.block = make(chan struct{})
	s1 := e.l.Scan()
	s2 := e.l.Scan()
	e.l.startScan(true) // часовой обход во время обхода по открытию медиатеки
	if !s1.Running || !s2.Running {
		t.Errorf("обход: %+v %+v", s1, s2)
	}
	close(e.dl.block)
	deadline := time.Now().Add(5 * time.Second)
	for e.l.scanState().Running {
		if time.Now().After(deadline) {
			t.Fatal("обход не закончился")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s := e.l.Scan(); s.Running {
		t.Errorf("обход меньше минуты назад — не запускать снова: %+v", s)
	}
	var n int
	e.d.R.QueryRow(`SELECT COUNT(*) FROM lib_units`).Scan(&n)
	if n != 1 || e.dl.calls != 1 {
		t.Errorf("единиц %d, обходов %d — нужно по одному", n, e.dl.calls)
	}
}

// Review Focus 5: раздачу удалили в «Загрузках» — карточка и «Продолжить» пропадают, карточка — 404.
func TestTorrentUnitRemoved(t *testing.T) {
	e := newEnv(t)
	e.dl.set(malahit(5))
	e.scan(t)
	e.hist.SetPosition(ctx, "pc", hashA, 0, 600, 3000)
	if v := e.list(t, "pc", 0); len(v.Cards) != 1 || len(v.Continue) != 1 {
		t.Fatalf("до удаления: %+v", v)
	}
	e.dl.set()
	v := e.list(t, "pc", 0)
	_, err := e.l.Card(ctx, "pc", "kp-5")
	if len(v.Cards) != 0 || len(v.Continue) != 0 || !errors.Is(err, ErrNoCard) {
		t.Errorf("после удаления: карточек %d, продолжить %d, карточка %v", len(v.Cards), len(v.Continue), err)
	}
}

// «Продолжить просмотр»: начатый недосмотренный файл, иначе следующая серия после досмотренной.
func TestContinue(t *testing.T) {
	e := newEnv(t)
	study, _ := e.d.addCategory(ctx, CategoryInput{Name: "Сериалы-2", Layout: LayoutSeries})
	e.folder(t, study, "S", "Show/Show.S01E01.mkv", "Show/Show.S01E02.mkv", "Show/Show.S01E03.mkv")
	e.scan(t)
	var unit int64
	e.d.R.QueryRow(`SELECT id FROM lib_units`).Scan(&unit)
	files := map[int]int64{}
	rows, _ := e.d.R.Query(`SELECT episode, id FROM lib_files`)
	for rows.Next() {
		var ep int
		var id int64
		rows.Scan(&ep, &id)
		files[ep] = id
	}
	rows.Close()
	hash := "lib-" + itoa(int(unit))
	e.hist.SetWatched(ctx, "pc", hash, int(files[1]), true)
	v := e.list(t, "pc", 0)
	if len(v.Continue) != 1 || v.Continue[0].File != files[2] || v.Continue[0].Episode != 2 || v.Continue[0].Hash != hash {
		t.Fatalf("после первой серии: %+v", v.Continue)
	}
	e.hist.SetPosition(ctx, "pc", hash, int(files[3]), 700, 3000)
	v = e.list(t, "pc", 0)
	if len(v.Continue) != 1 || v.Continue[0].File != files[3] || v.Continue[0].PositionSec != 700 {
		t.Errorf("начатая третья: %+v", v.Continue)
	}
	e.hist.SetWatched(ctx, "pc", hash, int(files[3]), true)
	e.hist.SetWatched(ctx, "pc", hash, int(files[2]), true)
	if v := e.list(t, "pc", 0); len(v.Continue) != 0 {
		t.Errorf("всё досмотрено — продолжать нечего: %+v", v.Continue)
	}
}

// Постер ручной и нераспознанной единицы — из папки.
func TestLocalPoster(t *testing.T) {
	e := newEnv(t)
	e.folder(t, catFilms, "Movies", "Home video/film.mkv", "Home video/folder.jpg")
	e.scan(t)
	v := e.list(t, "pc", 0)
	var unit int64
	e.d.R.QueryRow(`SELECT id FROM lib_units`).Scan(&unit)
	if len(v.Cards) != 1 || v.Cards[0].Poster != "/api/v1/library/units/"+itoa(int(unit))+"/poster" {
		t.Errorf("локальный постер: %+v", v.Cards)
	}
	if p := e.l.LocalPoster(ctx, unit); filepath.Base(p) != "folder.jpg" {
		t.Errorf("путь постера: %q", p)
	}
}

// Загрузки недоступны (движок не запустился) — медиатека работает: папки видны, скачанное — нет,
// а единицы скачанного из базы не удаляются.
func TestDownloadsDown(t *testing.T) {
	e := newEnv(t)
	e.folder(t, catFilms, "Movies", "a.mkv")
	e.dl.set(malahit(5))
	e.scan(t)
	e.dl.err = errors.New("движок не запустился")
	e.scan(t)
	v, err := e.l.List(ctx, "pc", 0)
	if err != nil || len(v.Cards) != 1 {
		t.Fatalf("без загрузок: %v %+v", err, v.Cards)
	}
	var n int
	e.d.R.QueryRow(`SELECT COUNT(*) FROM lib_units WHERE source = 'torrent'`).Scan(&n)
	if n != 1 {
		t.Errorf("единица скачанного удалена, пока загрузки недоступны")
	}
}

// Скачали новое — оно появляется при следующем открытии медиатеки, даже если обход был только что:
// список загрузок изменился — обход сразу.
func TestNewDownloadAppears(t *testing.T) {
	e := newEnv(t)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.l.start(runCtx)
	e.scan(t)
	e.l.mu.Lock()
	e.l.lastScan = e.clk.now()
	e.l.mu.Unlock()
	e.dl.set(malahit(5))
	e.list(t, "pc", 0) // пульт открыл медиатеку
	deadline := time.Now().Add(5 * time.Second)
	for len(e.list(t, "pc", 0).Cards) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("новая раздача не появилась")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
