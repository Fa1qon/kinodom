// Package rutortest — страницы Rutor, снятые вживую 2026-09-28 (spikes/misc/testdata/rutor),
// для тестов. Только для тестов.
package rutortest

import (
	"embed"
	"testing"
)

//go:embed testdata
var files embed.FS

// Page — образец testdata/<name>.
func Page(t testing.TB, name string) []byte {
	t.Helper()
	b, err := files.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ListPages — образцы со списками раздач: вместе 1289 строк (так же разобрано в spikes/misc).
var ListPages = []string{
	"browse_12_sort2.html", "browse_12_sort2_page2.html", "browse_1_sort2.html",
	"cat_nauchno_popularnoe.html", "index.html", "mirror_rutor_is.html", "top.html",
	"search_all_matrix.html", "search_all_matrix_1999.html", "search_cat12_discovery.html",
}
