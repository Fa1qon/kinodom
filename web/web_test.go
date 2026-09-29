package web

import (
	"bytes"
	"io/fs"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"testing"
)

// required — файлы пульта, без которых он не работает (спека этапа 7, раздел 6.1).
var required = []string{
	"index.html", "style.css", "app.js", "api.js", "ui.js", "icons.js", "nav.js",
	"fonts/golos-text-cyrillic.woff2", "fonts/golos-text-latin.woff2",
	"fonts/unbounded-cyrillic.woff2", "fonts/unbounded-latin.woff2",
	"fonts/OFL-golos-text.txt", "fonts/OFL-unbounded.txt",
	"views/catalog.js", "views/release.js", "views/search.js", "views/downloads.js",
	"views/settings-layout.js", "views/settings-status.js", "views/settings-params.js",
}

// scripts — все модули пульта.
func scripts(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(Static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return err
		}
		b, err := fs.ReadFile(Static, p)
		out[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Пульт встроен в exe целиком: каждый нужный файл на месте, страница подключает стиль и главный
// модуль.
func TestPultFilesEmbedded(t *testing.T) {
	for _, f := range required {
		if _, err := fs.Stat(Static, f); err != nil {
			t.Errorf("нет %s", f)
		}
	}
	index, err := fs.ReadFile(Static, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(index, []byte(`<script type="module" src="app.js">`)) || !bytes.Contains(index, []byte(`href="style.css"`)) {
		t.Errorf("index.html не подключает app.js как модуль или style.css:\n%s", index)
	}
}

var reImport = regexp.MustCompile(`(?:from|import)\s*\(?\s*'(\.[^']+)'`)

// Каждый импорт модуля ведёт к встроенному файлу: опечатка в пути — белый экран без ошибки на
// сервере.
func TestPultImportsResolve(t *testing.T) {
	for file, src := range scripts(t) {
		for _, m := range reImport.FindAllStringSubmatch(src, -1) {
			target := path.Join(path.Dir(file), m[1])
			if _, err := fs.Stat(Static, target); err != nil {
				t.Errorf("%s: импорт %s — файла %s нет", file, m[1], target)
			}
		}
	}
}

var (
	reIconUse = regexp.MustCompile(`icon\('([a-z_]+)'`)
	reIconDef = regexp.MustCompile(`(?m)^\s+([a-z_]+): '`)
)

// Иконки, которые зовут экраны, есть в icons.js: иначе кнопка без значка.
func TestPultIconsExist(t *testing.T) {
	src := scripts(t)
	have := map[string]bool{}
	for _, m := range reIconDef.FindAllStringSubmatch(src["icons.js"], -1) {
		have[m[1]] = true
	}
	for file, s := range src {
		for _, m := range reIconUse.FindAllStringSubmatch(s, -1) {
			if !have[m[1]] {
				t.Errorf("%s: иконки %q нет в icons.js", file, m[1])
			}
		}
	}
}

// lookNode — Node на этом ПК; без него проверка JavaScript пропускается.
func lookNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node не установлен — JavaScript пульта не проверяется")
	}
	return node
}

// Модули разбираются как JavaScript: синтаксическая ошибка в модуле — белый экран, а серверные
// тесты её не видят.
func TestPultModulesParse(t *testing.T) {
	node := lookNode(t)
	for file, src := range scripts(t) {
		cmd := exec.Command(node, "--input-type=module", "--check")
		cmd.Stdin = strings.NewReader(src)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", file, err, out)
		}
	}
}

// Числа и имена в пульте — по-русски и коротко: размеры, скорость, склонение, имена серий без
// общего начала и конца.
func TestPultFormatting(t *testing.T) {
	node := lookNode(t)
	script := `
import { size, speed, plural, shortNames, fileFormat } from './ui.js';
const checks = [
  [size(18683035238), '17,4 ГБ'], [size(1034944512), '987 МБ'], [size(0), '0 МБ'],
  [speed(3250586), '3,1 МБ/с'], [speed(870400), '850 КБ/с'],
  [plural(1, 'пир', 'пира', 'пиров'), '1 пир'], [plural(3, 'пир', 'пира', 'пиров'), '3 пира'],
  [plural(12, 'пир', 'пира', 'пиров'), '12 пиров'], [plural(22, 'пир', 'пира', 'пиров'), '22 пира'],
  [shortNames(['The.Dinosaurs.S01E01.720p.NF.WEB-DL.mkv', 'The.Dinosaurs.S01E02.720p.NF.WEB-DL.mkv']).join('|'), 'S01E01|S01E02'],
  [shortNames(['Сезон 1/01. Начало.mkv', 'Сезон 1/02. Финал.mkv']).join('|'), '01 Начало|02 Финал'],
  [shortNames(['film.mkv']).join('|'), 'film'],
  [shortNames(['a.mkv', 'a.mkv']).join('|'), 'a|a'],
  [fileFormat('Сезон 1/01. Начало.mkv'), 'MKV'], [fileFormat('film.m4v'), 'M4V'], [fileFormat('Сезон\\01.avi'), 'AVI'], [fileFormat('README'), ''],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error(JSON.stringify(got), '≠', JSON.stringify(want));
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// «Загрузки» по раздачам: строки файлов собираются в раздачи в порядке первой строки, серии — по имени, как
// на экране раздачи (номер файла в торренте бывает не по порядку серий);
// общее состояние — первое, что есть: смотрят, качается, на паузе, в очереди, скачано; процент — от
// общего размера; удалить раздачу можно, если хоть одну её серию не смотрят (спека этапа 7, раздел 10.7).
func TestPultDownloadGroups(t *testing.T) {
	node := lookNode(t)
	script := `
import { groupDownloads } from './views/downloads.js';
const f = (hash, index, state, size, done, extra = {}) =>
  ({ hash, index, state, size, done, file: hash + index + '.mkv', release: null, canDelete: true, ...extra });
const gs = groupDownloads([
  f('a', 2, 'downloading', 100, 50, { speed: 10 }),
  f('b', 0, 'done', 300, 300),
  f('a', 0, 'done', 100, 100),
  f('a', 1, 'queued', 100, 0),
  f('c', 0, 'paused', 200, 20, { canDelete: false }),
  f('c', 1, 'watching', 200, 0, { canDelete: false }),
  f('d', 0, 'done', 10, 10, { file: 'S01E10.mkv' }),
  f('d', 1, 'done', 10, 10, { file: 'S01E02.mkv' }),
  f('d', 2, 'done', 10, 10, { file: 'S01E01.mkv' }),
]);
const got = gs.map((g) => [g.hash, g.items.map((d) => d.index).join(''), g.state, g.size, g.percent, g.speed, g.canDelete].join(' ')).join(' | ');
const want = 'a 012 downloading 300 50 10 true | b 0 done 300 100 0 true | c 01 watching 400 5 0 false | d 210 done 30 100 0 true';
if (got !== want) {
  console.error(got, '≠', want);
  process.exitCode = 1;
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// Пульт телевизора: стрелка переводит фокус на ближайший элемент в эту сторону; элемент на одной
// линии важнее более близкого наискосок; в сторону, где ничего нет, фокус не уходит; влево и
// вправо — только в своём ряду (с единственного раздела вправо — не в сетку наискосок).
func TestPultSpatialNav(t *testing.T) {
	node := lookNode(t)
	script := `
import { pick } from './nav.js';
const r = (left, top, w = 100, hh = 150) => ({ left, top, right: left + w, bottom: top + hh });
// Шапка: логотип, «Каталог», поиск над правыми карточками; под ней сетка 3×2 карточек.
const logo = r(0, 0, 80, 40), menu = r(120, 0, 80, 40), search = r(260, 0, 200, 40);
const grid = [r(0, 100), r(120, 100), r(240, 100), r(0, 280), r(120, 280), r(240, 280)];
const chip = r(0, 60, 90, 30); // единственный раздел над сеткой
const all = [logo, menu, search, chip, ...grid];
const at = (from, dir) => { const i = pick(from, all.filter((x) => x !== from), dir); return i < 0 ? -1 : all.indexOf(all.filter((x) => x !== from)[i]); };
const checks = [
  [at(grid[0], 'right'), all.indexOf(grid[1])],
  [at(grid[0], 'down'), all.indexOf(grid[3])],
  [at(grid[4], 'up'), all.indexOf(grid[1])],
  [at(grid[2], 'right'), -1],
  [at(grid[5], 'down'), -1],
  [at(grid[0], 'up'), all.indexOf(chip)],
  [at(chip, 'up'), all.indexOf(logo)],
  [at(grid[2], 'up'), all.indexOf(search)],
  [at(logo, 'right'), all.indexOf(menu)],
  [at(grid[3], 'left'), -1],
  [at(chip, 'right'), -1],
  [at(chip, 'down'), all.indexOf(grid[0])],
];
for (const [got, want] of checks) {
  if (got !== want) {
    console.error('получили', got, 'ждали', want);
    process.exitCode = 1;
  }
}
`
	cmd := exec.Command(node, "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}
