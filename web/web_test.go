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
	"index.html", "style.css", "app.js", "api.js", "ui.js", "icons.js",
	"fonts/golos-text-cyrillic.woff2", "fonts/golos-text-latin.woff2",
	"fonts/unbounded-cyrillic.woff2", "fonts/unbounded-latin.woff2",
	"fonts/OFL-golos-text.txt", "fonts/OFL-unbounded.txt",
	"views/catalog.js",
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

// Модули разбираются как JavaScript (Node на этом ПК; без Node — пропуск): синтаксическая ошибка
// в модуле — белый экран, а серверные тесты её не видят.
func TestPultModulesParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node не установлен — синтаксис модулей не проверяется")
	}
	for file, src := range scripts(t) {
		cmd := exec.Command(node, "--input-type=module", "--check")
		cmd.Stdin = strings.NewReader(src)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", file, err, out)
		}
	}
}
