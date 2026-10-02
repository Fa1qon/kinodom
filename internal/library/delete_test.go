package library

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// unitsOf — единицы папок категории: ключ → номер.
func unitsOf(t *testing.T, e *env, category int64) map[string]int64 {
	t.Helper()
	rows, err := e.d.R.QueryContext(ctx,
		`SELECT u.id, u.key FROM lib_units u JOIN lib_folders f ON f.id = u.folder WHERE f.category = ? AND u.missing = 0`, category)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var k string
		if err := rows.Scan(&id, &k); err != nil {
			t.Fatal(err)
		}
		out[k] = id
	}
	return out
}

// План 14В: скачанное Kinodom в папку медиатеки — его папка «Название [хэш]» обходом пропускается:
// скачанное и так приходит в медиатеку единицей раздачи, второй карточки нет.
func TestScanSkipsTorrentFolders(t *testing.T) {
	e := newEnv(t)
	dir := e.folder(t, catFilms, "Movies", "Мой фильм (2020).mkv", "Скачанный (2021) [abcdef12]/film.mkv")
	e.l.o.TorrentFolders = func(context.Context) ([]string, error) {
		return []string{filepath.Join(dir, "Скачанный (2021) [abcdef12]")}, nil
	}
	e.scan(t)
	us := unitsOf(t, e, catFilms)
	if len(us) != 1 || us[filepath.Join(dir, "Мой фильм (2020).mkv")] == 0 {
		t.Fatalf("единицы: %v", us)
	}
}

// Папка для скачанного — первая папка стандартной категории (как в настройках: первая добавленная); нет
// папок — "".
func TestTargetFolder(t *testing.T) {
	e := newEnv(t)
	if got, err := e.l.TargetFolder(ctx, "films"); err != nil || got != "" {
		t.Fatalf("без папок: %q, %v", got, err)
	}
	first := e.folder(t, catFilms, "Old")
	e.folder(t, catFilms, "New")
	series := e.folder(t, catSeries, "Shows")
	if got, _ := e.l.TargetFolder(ctx, "films"); got != first {
		t.Fatalf("фильмы: %q, нужно %q", got, first)
	}
	if got, _ := e.l.TargetFolder(ctx, "series"); got != series {
		t.Fatalf("сериалы: %q", got)
	}
}

// Служба не может писать в папку «Фильмов» — у папки no_write и проблема (Review Focus 1); у своей
// категории запись не проверяется.
func TestNoWriteProblem(t *testing.T) {
	e := newEnv(t)
	films := e.folder(t, catFilms, "Movies")
	own, err := e.d.addCategory(ctx, CategoryInput{Name: "Концерты", Layout: LayoutFilms})
	if err != nil {
		t.Fatal(err)
	}
	e.folder(t, own, "Concerts")
	e.l.o.Writable = func(string) bool { return false }
	e.scan(t)
	cs, err := e.l.Categories(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		for _, f := range c.Folders {
			want := ""
			if f.Path == films {
				want = "no_write"
			}
			if f.Problem != want {
				t.Errorf("%s: проблема %q, нужно %q", f.Path, f.Problem, want)
			}
		}
	}
	ps, _ := e.d.Problems(ctx)
	found := false
	for _, p := range ps {
		if strings.HasPrefix(p.ID, "library.folder.") && strings.Contains(p.Text, "Нет права записи") && strings.Contains(p.Text, films) {
			found = true
		}
	}
	if !found {
		t.Fatalf("проблемы: %+v", ps)
	}
	e.l.o.Writable = func(string) bool { return true }
	e.scan(t)
	if ps, _ := e.d.Problems(ctx); len(ps) != 0 {
		t.Fatalf("запись есть — проблем нет: %+v", ps)
	}
}

// «Удалить» свою единицу: файл, папка-фильм, сериал «папка = категория» (только его видеофайлы — Review
// Focus 2); скачанное — не здесь; ключ вне папки категории — отказ (Review Focus 5).
func TestDeleteUnit(t *testing.T) {
	e := newEnv(t)
	dir := e.folder(t, catFilms, "Movies", "Файл (2020).mkv", "Папка (2021)/movie.mkv", "Чужой (2019).mkv")
	e.scan(t)
	us := unitsOf(t, e, catFilms)
	file, folder := filepath.Join(dir, "Файл (2020).mkv"), filepath.Join(dir, "Папка (2021)")
	if err := e.l.DeleteUnit(ctx, us[file]); err != nil {
		t.Fatal(err)
	}
	if err := e.l.DeleteUnit(ctx, us[folder]); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, folder} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s не удалён: %v", p, err)
		}
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("папка категории: %v", err)
	}
	if got := unitsOf(t, e, catFilms); len(got) != 1 {
		t.Fatalf("после удаления единицы: %v", got)
	}
	// Ключ переписан вне папки категории — отказ, файл на месте.
	outside := filepath.Join(e.root, "outside.mkv")
	os.WriteFile(outside, []byte("x"), 0o644)
	other := us[filepath.Join(dir, "Чужой (2019).mkv")]
	if _, err := e.d.W.Exec(`UPDATE lib_units SET key = ? WHERE id = ?`, outside, other); err != nil {
		t.Fatal(err)
	}
	if err := e.l.DeleteUnit(ctx, other); !errors.Is(err, ErrOutside) {
		t.Fatalf("вне папки: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("файл вне папки тронут: %v", err)
	}
	// Сериал, у которого папка категории и есть сериал: удаляются его видеофайлы, папка остаётся.
	show := e.folder(t, catSeries, "Сериал (2020)", "Сериал.S01E01.mkv", "Сериал.S01E02.mkv")
	e.scan(t)
	ss := unitsOf(t, e, catSeries)
	if ss[show] == 0 {
		t.Fatalf("сериал «папка = категория»: %v", ss)
	}
	if err := e.l.DeleteUnit(ctx, ss[show]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(show); err != nil {
		t.Fatalf("папка категории сериалов удалена: %v", err)
	}
	if left, _ := os.ReadDir(show); len(left) != 0 {
		t.Fatalf("остались файлы: %v", left)
	}
}

// Скачанное удаляется в «Загрузках» (как там), а не здесь.
func TestDeleteTorrentUnitRefused(t *testing.T) {
	e := newEnv(t)
	e.dl.set(malahit(5))
	e.scan(t)
	var id int64
	if err := e.d.R.QueryRowContext(ctx, `SELECT id FROM lib_units WHERE source = 'torrent'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := e.l.DeleteUnit(ctx, id); !errors.Is(err, ErrTorrentUnit) {
		t.Fatalf("скачанное: %v", err)
	}
}

// DELETE /api/v1/library/units/{id}: 200, повторно — 404.
func TestDeleteUnitRoute(t *testing.T) {
	e := newEnv(t)
	dir := e.folder(t, catFilms, "Movies", "Файл (2020).mkv")
	e.scan(t)
	id := unitsOf(t, e, catFilms)[filepath.Join(dir, "Файл (2020).mkv")]
	mux := http.NewServeMux()
	e.l.Register(testRouter{mux})
	for i, want := range []int{http.StatusOK, http.StatusNotFound} {
		if code := call(t, mux, "DELETE", "/api/v1/library/units/"+strconv.FormatInt(id, 10), fromPhone, nil, nil); code != want {
			t.Fatalf("запрос %d: код %d", i+1, code)
		}
	}
}

// Ревью 14В, Important 2: папки раздач не прочитались (движок ещё не поднялся) — папки стандартных «Фильмов» и
// «Сериалов» (туда качает Kinodom) в этом обходе не синхронизируются: скачанное не появляется своим файлом,
// а единица «папка = сериал» не пересоздаётся (история и отметки целы).
func TestScanKeepsTargetFoldersWithoutTorrentFolders(t *testing.T) {
	e := newEnv(t)
	show := e.folder(t, catSeries, "Сериал (2020)", "Сериал.S01E01.mkv", "Сериал.S01E02.mkv")
	e.scan(t)
	before := unitsOf(t, e, catSeries)
	if before[show] == 0 {
		t.Fatalf("сериал «папка = сериал»: %v", before)
	}
	mkdir(t, show, "Другой (2021) [abcdef12]")
	os.WriteFile(filepath.Join(show, "Другой (2021) [abcdef12]", "Другой.S01E01.mkv"), []byte("x"), 0o644)
	e.l.o.TorrentFolders = func(context.Context) ([]string, error) {
		return nil, errors.New("загрузки не работают")
	}
	e.scan(t)
	after := unitsOf(t, e, catSeries)
	if len(after) != 1 || after[show] != before[show] {
		t.Fatalf("до %v, после %v", before, after)
	}
}

// Ревью 14В, Important 4: «Удалить» в своей категории без права изменения — у папки проблема no_write и
// «Разрешить доступ», а не тупик; удаление прошло — проблемы нет.
func TestDeleteNoWriteMarksFolder(t *testing.T) {
	e := newEnv(t)
	own, err := e.d.addCategory(ctx, CategoryInput{Name: "Концерты", Layout: LayoutFilms})
	if err != nil {
		t.Fatal(err)
	}
	dir := e.folder(t, own, "Concerts", "Концерт (2020).mkv")
	e.scan(t)
	id := unitsOf(t, e, own)[filepath.Join(dir, "Концерт (2020).mkv")]
	e.l.o.Remove = func(string) error { return &os.PathError{Op: "remove", Path: dir, Err: os.ErrPermission} }
	if err := e.l.DeleteUnit(ctx, id); !errors.Is(err, ErrNoWrite) {
		t.Fatalf("без права: %v", err)
	}
	problem := func() string {
		cs, _ := e.l.Categories(ctx, "")
		for _, c := range cs {
			for _, f := range c.Folders {
				if f.Path == dir {
					return f.Problem
				}
			}
		}
		return "?"
	}
	if p := problem(); p != "no_write" {
		t.Fatalf("проблема папки %q", p)
	}
	e.scan(t) // обход проблему от удаления не снимает
	if p := problem(); p != "no_write" {
		t.Fatalf("после обхода: %q", p)
	}
	ps, _ := e.d.Problems(ctx)
	if len(ps) != 1 || !strings.Contains(ps[0].Text, "Нет права изменять") || strings.Contains(ps[0].Text, "скачанное") {
		t.Fatalf("проблемы: %+v", ps)
	}
	e.l.o.Remove = nil
	if err := e.l.DeleteUnit(ctx, id); err != nil {
		t.Fatal(err)
	}
	if p := problem(); p != "" {
		t.Fatalf("удалилось — проблема %q", p)
	}
}

// Ревью 14В, п. 6: «нет записи — скачанное идёт в папку загрузок» — только у первой папки «Фильмов» (туда
// качает Kinodom), не у остальных.
func TestNoWriteOnlyTargetFolder(t *testing.T) {
	e := newEnv(t)
	first := e.folder(t, catFilms, "A")
	second := e.folder(t, catFilms, "B")
	e.l.o.Writable = func(string) bool { return false }
	e.scan(t)
	cs, _ := e.l.Categories(ctx, "")
	for _, c := range cs {
		for _, f := range c.Folders {
			want := map[string]string{first: "no_write", second: ""}[f.Path]
			if f.Problem != want {
				t.Errorf("%s: %q, нужно %q", f.Path, f.Problem, want)
			}
		}
	}
}

// Ревью 14В, Important 5: в папке-единице лежит папка другой категории (или раздача Kinodom) — «Удалить»
// удаляет только файлы единицы, вложенная папка цела.
func TestDeleteUnitKeepsNestedFolders(t *testing.T) {
	e := newEnv(t)
	own, err := e.d.addCategory(ctx, CategoryInput{Name: "Видео", Layout: LayoutFilms})
	if err != nil {
		t.Fatal(err)
	}
	root := e.folder(t, own, "Video", "Кино/a.mkv")
	nested := filepath.Join(root, "Кино", "Новое")
	mkdir(t, nested)
	os.WriteFile(filepath.Join(nested, "b.mkv"), []byte("x"), 0o644)
	cs, _ := e.d.categories(ctx)
	for _, c := range cs {
		if c.ID == catFilms {
			if err := e.d.updateCategory(ctx, c.ID, CategoryInput{Name: c.Name, Layout: c.Layout, Folders: []string{nested}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.scan(t)
	id := unitsOf(t, e, own)[filepath.Join(root, "Кино")]
	if id == 0 {
		t.Fatalf("единица «Кино»: %v", unitsOf(t, e, own))
	}
	if err := e.l.DeleteUnit(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "Кино", "a.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("файл единицы: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, "b.mkv")); err != nil {
		t.Fatalf("папка «Фильмов» внутри: %v", err)
	}
}

// Ревью 14В, п. 12: несколько папок одним сохранением (мастер) — в порядке ввода: в первую качает Kinodom.
func TestFoldersKeepInputOrder(t *testing.T) {
	e := newEnv(t)
	var dirs []string
	for _, n := range []string{"Zeta", "Alpha", "Mid", "Beta", "Omega"} {
		dirs = append(dirs, mkdir(t, e.root, n))
	}
	for i := 0; i < 5; i++ { // порядок обхода map случаен — несколько попыток
		cs, _ := e.d.categories(ctx)
		for _, c := range cs {
			if c.ID == catFilms {
				if err := e.d.updateCategory(ctx, c.ID, CategoryInput{Name: c.Name, Layout: c.Layout}); err != nil {
					t.Fatal(err)
				}
				if err := e.d.updateCategory(ctx, c.ID, CategoryInput{Name: c.Name, Layout: c.Layout, Folders: dirs}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if got, _ := e.l.TargetFolder(ctx, "films"); got != dirs[0] {
			t.Fatalf("первая папка %q, нужно %q", got, dirs[0])
		}
	}
}

// Ревью 14В: после удаления сериала «папка = сериал» оставались субтитры и пустые папки сезонов. Удаляются видео
// сериала, их субтитры (то же имя) и опустевшие папки; посторонний файл человека и сама папка категории — нет
// (Review Focus 1, 2).
func TestDeleteSeriesFolderCleansUp(t *testing.T) {
	e := newEnv(t)
	show := e.folder(t, catSeries, "Сериал (2020)", "Сериал.S01E01.mkv", "Сериал.S01E01.rus.srt", "Сезон 2/Сериал.S02E01.mkv",
		"Сезон 2/Сериал.S02E01.srt", "Subs/Сериал.S01E01.eng.srt", "заметки.txt")
	e.scan(t)
	ss := unitsOf(t, e, catSeries)
	if ss[show] == 0 {
		t.Fatalf("сериал «папка = сериал»: %v", ss)
	}
	if err := e.l.DeleteUnit(ctx, ss[show]); err != nil {
		t.Fatal(err)
	}
	var left []string
	filepath.WalkDir(show, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != show {
			rel, _ := filepath.Rel(show, p)
			left = append(left, filepath.ToSlash(rel))
		}
		return nil
	})
	if !slices.Equal(left, []string{"заметки.txt"}) {
		t.Fatalf("осталось: %v", left)
	}
}

// Ревью 14В: тексты ошибок удаления — без внутренних слов; файл открыт другой программой — так и сказано.
func TestDeleteErrorTexts(t *testing.T) {
	if strings.Contains(ErrOutside.Error(), "единиц") {
		t.Fatalf("ErrOutside: %q", ErrOutside)
	}
	e := newEnv(t)
	busy := &fs.PathError{Op: "remove", Path: "x", Err: syscall.Errno(32)} // ERROR_SHARING_VIOLATION
	// Ревью 15Б, Minor 1: файл держит и сам Kinodom, когда серию смотрят на ТВ, — текст про обе причины.
	if err := e.l.removeError(ctx, 0, `D:\Сериалы`, busy); !strings.Contains(err.Error(), "открыт в другой программе") ||
		!strings.Contains(err.Error(), "смотрят") {
		t.Fatalf("занятый файл: %v", err)
	}
}

// Ревью 14В: первая папка «Сериалов» — сама сериал («папка = сериал»): другие сериалы туда не качаются —
// следующая папка категории, а нет её — папка загрузок (Review Focus 3).
func TestTargetFolderSkipsSeriesFolder(t *testing.T) {
	e := newEnv(t)
	e.folder(t, catSeries, "Сериал (2020)", "Сериал.S01E01.mkv", "Сериал.S01E02.mkv")
	e.scan(t)
	if got, _ := e.l.TargetFolder(ctx, "series"); got != "" {
		t.Fatalf("единственная папка — сериал: %q", got)
	}
	shows := e.folder(t, catSeries, "Shows")
	if got, _ := e.l.TargetFolder(ctx, "series"); got != shows {
		t.Fatalf("получено %q, нужно %q", got, shows)
	}
}

// Ревью 15Б, Important 1: субтитры удалённых серий ищутся только рядом с ними и в их «Subs»/«Subtitles» — не по
// всей папке сериала: раздача Kinodom другого сериала внутри неё и «Extras» не трогаются, даже если имена совпали.
func TestDeleteSeriesKeepsForeignSubtitles(t *testing.T) {
	e := newEnv(t)
	show := e.folder(t, catSeries, "Сериал (2020)", "Сериал.S01E01.mkv", "Сериал.S01E02.mkv", "Subs/Сериал.S01E02.eng.srt",
		"Сезон 2/Сериал.S02E01.mkv", "Сезон 2/Subtitles/Сериал.S02E01.rus.srt",
		"Другой [abcdef12]/Сериал.S01E01.rus.srt", // раздача Kinodom другого сериала с тем же именем серии
		"Extras/Сериал.S01E01.Making.of.mkv", "Extras/Сериал.S01E01.Making.of.srt")
	torrent := filepath.Join(show, "Другой [abcdef12]")
	e.l.o.TorrentFolders = func(context.Context) ([]string, error) { return []string{torrent}, nil }
	e.scan(t)
	ss := unitsOf(t, e, catSeries)
	if ss[show] == 0 {
		t.Fatalf("сериал «папка = сериал»: %v", ss)
	}
	if err := e.l.DeleteUnit(ctx, ss[show]); err != nil {
		t.Fatal(err)
	}
	var left []string
	filepath.WalkDir(show, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(show, p)
			left = append(left, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(left)
	want := []string{"Extras/Сериал.S01E01.Making.of.mkv", "Extras/Сериал.S01E01.Making.of.srt", "Другой [abcdef12]/Сериал.S01E01.rus.srt"}
	if !slices.Equal(left, want) {
		t.Fatalf("осталось: %v", left)
	}
	for _, gone := range []string{"Subs", "Сезон 2"} {
		if _, err := os.Stat(filepath.Join(show, gone)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("папка %s осталась: %v", gone, err)
		}
	}
}

// Ревью 15Б, Important 2: первая папка «Сериалов» — сама сериал, скачанное идёт во вторую — право записи
// проверяется у второй: закрыта она — «нет права записи» у неё; закрыта папка-сериал — проблемы нет.
func TestNoWriteChecksTargetFolder(t *testing.T) {
	for _, closed := range []string{"Shows", "Сериал (2020)"} {
		t.Run(closed, func(t *testing.T) {
			e := newEnv(t)
			show := e.folder(t, catSeries, "Сериал (2020)", "Сериал.S01E01.mkv", "Сериал.S01E02.mkv")
			shows := e.folder(t, catSeries, "Shows")
			e.scan(t) // единица «папка = сериал» — в базе
			e.l.o.Writable = func(dir string) bool { return filepath.Base(dir) != closed }
			e.scan(t)
			want := map[string]string{show: "", shows: ""}
			if closed == "Shows" {
				want[shows] = "no_write"
			}
			cs, _ := e.l.Categories(ctx, "")
			for _, c := range cs {
				for _, f := range c.Folders {
					if w, ok := want[f.Path]; ok && f.Problem != w {
						t.Errorf("%s: %q, нужно %q", filepath.Base(f.Path), f.Problem, w)
					}
				}
			}
		})
	}
}
