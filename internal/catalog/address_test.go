package catalog

import (
	"testing"

	"kinodom/internal/source"
)

// Адрес трекера не введён (этап 11a): трекер выключен — ни разделов, ни топов, ни страниц
// раздач, ни поиска; это не сбой и не проблема (трекера просто нет в списке «Трекеры»), каталог не считает
// это неудачей.
func TestTrackerWithoutAddressIsOff(t *testing.T) {
	t1, rt := newFake("t1"), newFake("t2")
	t1.off = true
	t1.top["12"] = []source.Release{rel("t1", "1", "Фильм (2020) BDRip", 50, 1, "aa")}
	rt.top["2110"] = []source.Release{rel("t2", "3", "", 30, 2<<30, "cc")}
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, t1, rt)
	if wait := refresh(t, c, true); wait != checkEvery {
		t.Fatalf("выключенный трекер посчитан неудачей: следующая проверка через %v", wait)
	}
	if t1.Calls("top") != 0 || t1.Calls("categories") != 0 {
		t.Fatalf("к выключенному трекеру ходили: топ %d, разделы %d", t1.Calls("top"), t1.Calls("categories"))
	}
	if got := problemText(t, db, "catalog.t1.address"); got != "" {
		t.Fatalf("трекер без адреса — проблема: %q", got)
	}
	if got := problemText(t, db, "catalog.t1"); got != "" {
		t.Fatalf("выключенный трекер «не отвечает»: %q", got)
	}
	if es := list(t, c, ListOptions{}); len(es) != 1 || es[0].Tracker != "t2" {
		t.Fatalf("каталог: %+v", es)
	}
	if did, err := c.enrichStep(ctx, "t1"); err != nil || did || t1.Calls("details") != 0 {
		t.Fatalf("догрузка выключенного: did %v, err %v, страниц %d", did, err, t1.Calls("details"))
	}

	t1.set(func() { t1.off = false })
	refresh(t, c, true)
	if t1.Calls("top") != 1 || problemText(t, db, "catalog.t1.address") != "" {
		t.Fatalf("адрес ввели: топ %d, проблема %q", t1.Calls("top"), problemText(t, db, "catalog.t1.address"))
	}
}

// Поиск идёт только по трекерам с адресом; без адресов — пустой ответ без ошибки.
func TestSearchSkipsTrackersWithoutAddress(t *testing.T) {
	t1, rt := newFake("t1"), newFake("t2")
	t1.off, rt.off = true, true
	c, _ := newCatalog(t, openDB(t), nil, t1, rt)
	st := waitSearch(t, c, "матрица")
	if len(st.Results) != 0 || len(st.Trackers) != 0 || t1.Calls("search")+rt.Calls("search") != 0 {
		t.Fatalf("поиск без адресов: %+v, запросов %d", st, t1.Calls("search")+rt.Calls("search"))
	}
	rt.set(func() { rt.off = false })
	rt.search = []source.Release{rel("t2", "9", "Матрица [1999]", 90, 1, "x")}
	st = waitSearch(t, c, "матрица 2")
	if len(st.Results) != 1 || st.Trackers["t2"] != SearchOK || t1.Calls("search") != 0 {
		t.Fatalf("поиск по одному трекеру: %+v", st)
	}
}
