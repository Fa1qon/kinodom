package library

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
