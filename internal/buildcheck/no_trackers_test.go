// Package buildcheck — проверки всей программы целиком, без своего кода.
package buildcheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// reTracker — домен трекера: rutor.info, rutracker.org, d.rutor.is, api.rutracker.cc… Зона — из
// списка: «rutracker.login» и «rutor.address» — ключи настроек, не адреса.
var reTracker = regexp.MustCompile(`(?i)\brut(or|racker)\.(info|is|org|net|cc|ru|su|me|to|in|co|com|biz|lol|nl|tv|pw|io|xyz|online|site|top|club)\b`)

// root — корень модуля (тест запускается из своей папки).
const root = "../.."

// Kinodom — инструмент без содержимого: адресов трекеров в программе нет, их вводит пользователь
// (спека этапа 11a, раздел 6). Строковые литералы кода и файлы пульта — без доменов трекеров;
// комментарии и тесты — можно.
func TestNoTrackerAddressesInProgram(t *testing.T) {
	var bad []string
	for _, dir := range []string{"cmd", "internal"} {
		filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || isTestHelper(path) {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, err := strconv.Unquote(lit.Value)
					if err == nil && reTracker.MatchString(s) {
						bad = append(bad, rel(path)+": "+reTracker.FindString(s))
					}
				}
				return true
			})
			return nil
		})
	}
	filepath.WalkDir(filepath.Join(root, "web", "static"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if d.IsDir() || !(strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".css")) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range reTracker.FindAllString(string(b), -1) {
			bad = append(bad, rel(path)+": "+m)
		}
		return nil
	})
	if len(bad) > 0 {
		t.Fatalf("в программе адреса трекеров:\n%s", strings.Join(bad, "\n"))
	}
}

// isTestHelper — пакеты-фейки для тестов (rutortest, rutrackertest, offlinetest…): в программу не
// входят, в них — образцы страниц трекеров.
func isTestHelper(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.HasSuffix(part, "test") && part != "buildcheck" {
			return true
		}
	}
	return false
}

func rel(path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}

// Сама проверка ловит домены — иначе она прошла бы и на пустом месте.
func TestTrackerPatternMatches(t *testing.T) {
	for _, s := range []string{"https://rutor.info", "d.rutor.is/download", "api.rutracker.cc", "RUTRACKER.ORG"} {
		if !reTracker.MatchString(s) {
			t.Errorf("не пойман: %s", s)
		}
	}
	for _, s := range []string{"rutor", "rutracker", "rutracker.login", "rutor.address", "rutracker.passwordSet", "rutor.isOff", "Rutor: ошибка"} {
		if reTracker.MatchString(s) {
			t.Errorf("пойман лишний: %s", s)
		}
	}
}
