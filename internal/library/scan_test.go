package library

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

var old = time.Now().Add(-time.Hour)

// tree — папка из списка путей («Movies/a.mkv»); файлы пустые и старые (докопированы).
func tree(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if strings.HasSuffix(p, "/") {
			mkdir(t, full)
			continue
		}
		mkdir(t, filepath.Dir(full))
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(full, old, old)
	}
	return root
}

// units — «ключ относительно root: файлы относительно ключа [сезон/раздел/серия]».
func units(t *testing.T, root string, us []ScannedUnit) []string {
	t.Helper()
	var out []string
	for _, u := range us {
		if len(u.Files) == 0 {
			continue // одни копирующиеся файлы: единица — только чтобы не потерять номера (Х14)
		}
		rel, _ := filepath.Rel(root, u.Key)
		var fs []string
		for _, f := range u.Files {
			fr, _ := filepath.Rel(u.Key, f.Path)
			if fr == "." {
				fr = filepath.Base(f.Path)
			}
			tag := ""
			if f.Season > 0 || f.Section != "" || f.Episode > 0 {
				tag = "[" + itoa(f.Season) + "/" + f.Section + "/" + itoa(f.Episode) + "]"
			}
			fs = append(fs, filepath.ToSlash(fr)+tag)
		}
		out = append(out, filepath.ToSlash(rel)+": "+strings.Join(fs, ", "))
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func scanOK(t *testing.T, root string, l Layout, skip func(string) bool) []string {
	t.Helper()
	us, err := Scan(root, l, skip, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return units(t, root, us)
}

func same(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n%s\nнужно:\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// «Как фильмы»: файл — фильм; подпапка — фильм, её видео — части по порядку; не видео — мимо.
func TestScanFilms(t *testing.T) {
	root := tree(t, "Crime.101.2026.mkv", "Frankenstein.2025.mp4", "На помощь!_2026_WEB-DLRip/film.avi",
		"Two parts/CD2.avi", "Two parts/CD1.avi", "Two parts/cover.jpg", "notes.txt", "Empty/readme.pdf")
	same(t, "фильмы", scanOK(t, root, LayoutFilms, nil),
		"Crime.101.2026.mkv: Crime.101.2026.mkv",
		"Frankenstein.2025.mp4: Frankenstein.2025.mp4",
		"Two parts: CD1.avi, CD2.avi",
		"На помощь!_2026_WEB-DLRip: film.avi")
}

// «Как сериалы»: подпапка — сериал, серии на любой глубине, сезон — по папке или SxxEyy; файл прямо в
// папке категории — единица из одной серии; естественный порядок.
func TestScanSeries(t *testing.T) {
	root := tree(t,
		"Better Call Saul/Better.Call.Saul.S02.BDRip/e10.mkv", "Better Call Saul/Better.Call.Saul.S02.BDRip/e2.mkv",
		"Better Call Saul/Better.Call.Saul.S01.BDRip/e1.mkv",
		"Advokaty.S01.2026/Advokaty.S01.E02.mkv", "Advokaty.S01.2026/Advokaty.S01.E01.mkv",
		"Loose.S01E01.mkv")
	same(t, "сериалы", scanOK(t, root, LayoutSeries, nil),
		"Advokaty.S01.2026: Advokaty.S01.E01.mkv[1//1], Advokaty.S01.E02.mkv[1//2]",
		"Better Call Saul: Better.Call.Saul.S01.BDRip/e1.mkv[1//1], Better.Call.Saul.S02.BDRip/e2.mkv[2//2], Better.Call.Saul.S02.BDRip/e10.mkv[2//10]",
		"Loose.S01E01.mkv: Loose.S01E01.mkv[1//1]")
}

// Папка, добавленная в «Сериалы», которая сама сериал: подпапки-сезоны или серии SxxEyy прямо в ней.
func TestScanSelfSeries(t *testing.T) {
	silo := tree(t, "S02/Silo.S02E01.mkv", "S01/Silo.S01E02.mkv", "S01/Silo.S01E01.mkv", "S03/x.mkv")
	same(t, "Silo", scanOK(t, silo, LayoutSeries, nil),
		".: S01/Silo.S01E01.mkv[1//1], S01/Silo.S01E02.mkv[1//2], S02/Silo.S02E01.mkv[2//1], S03/x.mkv[3//]")
	trudno := tree(t, "Trudno.byt.bogom.S01.E02.mkv", "Trudno.byt.bogom.S01.E01.mkv")
	same(t, "Трудно быть богом", scanOK(t, trudno, LayoutSeries, nil),
		".: Trudno.byt.bogom.S01.E01.mkv[1//1], Trudno.byt.bogom.S01.E02.mkv[1//2]")
}

// Подпапка Specials / Bonus / «Спецвыпуски» в папке-сериале (хвост Х13) не разваливает сериал на
// единицы-сезоны: папка — одна единица, её файлы — внутри неё.
func TestScanSelfSeriesWithSpecials(t *testing.T) {
	for _, extra := range []string{"Specials", "Bonus", "Спецвыпуски"} {
		silo := tree(t, "S01/Silo.S01E01.mkv", "S02/Silo.S02E01.mkv", extra+"/Silo.S00E01.mkv")
		us := scanOK(t, silo, LayoutSeries, nil)
		if len(us) != 1 || !strings.HasPrefix(us[0], ".: ") || !strings.Contains(us[0], "Silo.S00E01.mkv") {
			t.Errorf("%s: %v — сериал развалился", extra, us)
		}
	}
	notSeries := tree(t, "S01/Silo.S01E01.mkv", "Фильмы/film.mkv")
	if us := scanOK(t, notSeries, LayoutSeries, nil); len(us) != 2 {
		t.Errorf("папка «Фильмы» — не спецвыпуски: %v", us)
	}
}

// Главы курса — разделы с именем папки; «2» раньше «10».
func TestScanCourse(t *testing.T) {
	root := tree(t, "Go/10. Доп/a.mp4", "Go/2. Память/b.mp4", "Go/1. О курсе/c2.mp4", "Go/1. О курсе/c10.mp4", "Go/intro.mp4")
	same(t, "курс", scanOK(t, root, LayoutSeries, nil),
		"Go: intro.mp4, 1. О курсе/c2.mp4[/1. О курсе/], 1. О курсе/c10.mp4[/1. О курсе/], 2. Память/b.mp4[/2. Память/], 10. Доп/a.mp4[/10. Доп/]")
}

// Пропускаются: скрытые и с точки, sample меньше 300 МБ, папки Sample/Extras, файл моложе минуты,
// добавленная в другую категорию папка внутри.
func TestScanSkips(t *testing.T) {
	root := tree(t, "Film/film.mkv", "Film/film-sample.mkv", "Film/Sample/s.mkv", "Film/Extras/e.mkv", ".hidden.mkv",
		"Hidden/h.mkv", "Inner/i.mkv")
	big := filepath.Join(root, "Film", "Sample.Big.mkv")
	f, _ := os.Create(big)
	f.Truncate(400 << 20)
	f.Close()
	os.Chtimes(big, old, old)
	fresh := filepath.Join(root, "Copying.mkv")
	os.WriteFile(fresh, []byte("x"), 0o644)
	p, _ := syscall.UTF16PtrFromString(filepath.Join(root, "Hidden"))
	syscall.SetFileAttributes(p, syscall.FILE_ATTRIBUTE_HIDDEN)
	inner := filepath.Join(root, "Inner")
	same(t, "пропуски", scanOK(t, root, LayoutFilms, func(p string) bool { return pathKey(p) == pathKey(inner) }),
		"Film: film.mkv, Sample.Big.mkv")
}

func TestScanMissing(t *testing.T) {
	if _, err := Scan(filepath.Join(t.TempDir(), "нет"), LayoutFilms, nil, time.Now()); !errors.Is(err, ErrFolderMissing) {
		t.Errorf("нет папки: %v", err)
	}
}
