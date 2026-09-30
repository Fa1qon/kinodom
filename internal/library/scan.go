package library

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// ScannedFile — видеофайл единицы. Season и Episode — 0, если не указаны; Section — папки на пути,
// которые не сезоны (главы курса), через « / ».
type ScannedFile struct {
	Path    string
	Size    int64
	ModTime time.Time
	Season  int
	Section string
	Episode int
}

// ScannedUnit — единица из папки категории: Key — полный путь папки или файла.
type ScannedUnit struct {
	Key   string
	Name  string // имя папки или файла
	Files []ScannedFile
}

// Пропуски обхода (спека, раздел 5.2).
var (
	sampleMax  int64 = 300 << 20   // файл со словом sample меньше этого — пример из раздачи
	copyingFor       = time.Minute // файл моложе — ещё копируется
	skipDirs         = map[string]bool{"sample": true, "extras": true, "featurettes": true}
)

// Scan — единицы папки категории (спека, раздел 5.2). skip — добавленные в другие категории папки
// внутри root: их содержимое принадлежит своей категории. Ошибка — только когда недоступна сама root.
func Scan(root string, layout Layout, skip func(path string) bool, now time.Time) ([]ScannedUnit, error) {
	if err := readable(root); err != nil {
		return nil, err
	}
	w := walker{skip: skip, now: now}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var direct []ScannedFile
	var dirs []string
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		if w.ignored(e, p) {
			continue
		}
		if e.IsDir() {
			if w.hasVideo(p) {
				dirs = append(dirs, p)
			}
			continue
		}
		if f, ok := w.file(e, p); ok {
			direct = append(direct, f)
		}
	}
	if layout == LayoutSeries && selfSeries(direct, dirs) {
		return []ScannedUnit{{Key: root, Name: filepath.Base(root), Files: w.collect(root)}}, nil
	}
	var out []ScannedUnit
	for _, f := range direct {
		f.Season, f.Episode = Episode(filepath.Base(f.Path))
		out = append(out, ScannedUnit{Key: f.Path, Name: filepath.Base(f.Path), Files: []ScannedFile{f}})
	}
	for _, d := range dirs {
		if files := w.collect(d); len(files) > 0 {
			out = append(out, ScannedUnit{Key: d, Name: filepath.Base(d), Files: files})
		}
	}
	slices.SortFunc(out, func(a, b ScannedUnit) int { return natCompare(a.Name, b.Name) })
	return out, nil
}

// selfSeries — добавленная папка сама сериал: все её подпапки с видео — сезоны («S01», «Сезон 1»), и
// есть хотя бы один сезон или серии одного сериала с SxxEyy прямо в ней.
func selfSeries(direct []ScannedFile, dirs []string) bool {
	for _, d := range dirs {
		if _, ok := SeasonOnly(filepath.Base(d)); !ok {
			return false
		}
	}
	if len(dirs) > 0 {
		return true
	}
	title := ""
	for _, f := range direct {
		name := filepath.Base(f.Path)
		if s, e := Episode(name); s == 0 || e == 0 {
			return false
		}
		t := Norm(ParseName(name).Title)
		if title != "" && t != title {
			return false
		}
		title = t
	}
	return title != ""
}

type walker struct {
	skip func(string) bool
	now  time.Time
}

// ignored — не смотреть: с точки, скрытые и системные, папки Sample/Extras, чужие добавленные папки.
func (w walker) ignored(e fs.DirEntry, path string) bool {
	if strings.HasPrefix(e.Name(), ".") {
		return true
	}
	if info, err := e.Info(); err == nil {
		if a, ok := info.Sys().(*syscall.Win32FileAttributeData); ok &&
			a.FileAttributes&(syscall.FILE_ATTRIBUTE_HIDDEN|syscall.FILE_ATTRIBUTE_SYSTEM) != 0 {
			return true
		}
	}
	if e.IsDir() {
		return skipDirs[strings.ToLower(e.Name())] || (w.skip != nil && w.skip(path))
	}
	return false
}

// file — видеофайл, который берём: не пример из раздачи и уже докопирован.
func (w walker) file(e fs.DirEntry, path string) (ScannedFile, bool) {
	if !isVideo(e.Name()) {
		return ScannedFile{}, false
	}
	info, err := e.Info()
	if err != nil {
		return ScannedFile{}, false
	}
	if strings.Contains(strings.ToLower(e.Name()), "sample") && info.Size() < sampleMax {
		return ScannedFile{}, false
	}
	if w.now.Sub(info.ModTime()) < copyingFor {
		return ScannedFile{}, false
	}
	return ScannedFile{Path: path, Size: info.Size(), ModTime: info.ModTime()}, true
}

// hasVideo — в папке (на любой глубине) есть видео, которое берём.
func (w walker) hasVideo(dir string) bool {
	found := false
	w.walk(dir, func(string, fs.DirEntry, ScannedFile) {
		found = true
	})
	return found
}

// walk — видеофайлы папки на любой глубине; недоступные подпапки пропускаются.
func (w walker) walk(dir string, fn func(rel string, e fs.DirEntry, f ScannedFile)) {
	filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if e != nil && e.IsDir() && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if p == dir {
			return nil
		}
		if w.ignored(e, p) {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if e.IsDir() {
			return nil
		}
		if f, ok := w.file(e, p); ok {
			rel, _ := filepath.Rel(dir, p)
			fn(rel, e, f)
		}
		return nil
	})
}

// collect — серии единицы по порядку показа: сезон — по ближайшей папке-сезону, иначе по SxxEyy в
// имени; папки на пути, которые не сезоны, — раздел.
func (w walker) collect(unit string) []ScannedFile {
	var out []ScannedFile
	w.walk(unit, func(rel string, e fs.DirEntry, f ScannedFile) {
		f.Season, f.Section, f.Episode = place(strings.Split(filepath.Dir(rel), string(filepath.Separator)), e.Name())
		out = append(out, f)
	})
	slices.SortFunc(out, fileOrder)
	return out
}

// natCompare — естественный порядок без учёта регистра: «2» раньше «10».
func natCompare(a, b string) int {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		if unicode.IsDigit(ra[i]) && unicode.IsDigit(rb[j]) {
			si := i
			for i < len(ra) && unicode.IsDigit(ra[i]) {
				i++
			}
			sj := j
			for j < len(rb) && unicode.IsDigit(rb[j]) {
				j++
			}
			na, _ := strconv.Atoi(string(ra[si:i]))
			nb, _ := strconv.Atoi(string(rb[sj:j]))
			if na != nb {
				return na - nb
			}
			continue
		}
		if ra[i] != rb[j] {
			if ra[i] < rb[j] {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	return (len(ra) - i) - (len(rb) - j)
}
