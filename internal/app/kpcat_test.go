package app

import (
	"testing"

	"kinodom/internal/kpcat"
)

// План 14Г: каталог «Кинопоиск» подключён — пять разделов и четыре порядка (до первого обновления — пустые).
func TestKPCatalogRoute(t *testing.T) {
	a := startApp(t)
	var v kpcat.CatalogView
	getJSON(t, "http://"+a.API.Addr()+"/api/v1/kpcat", &v)
	if len(v.Sections) != 5 || len(v.Orders) != 4 {
		t.Fatalf("каталог: %+v", v)
	}
}
