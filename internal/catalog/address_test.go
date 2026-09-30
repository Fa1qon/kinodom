package catalog

import (
	"testing"

	"kinodom/internal/source"
)

// Адрес трекера не введён (этап 11a): трекер выключен — ни разделов, ни топов, ни страниц
// раздач, ни поиска; вместо «не отвечает» — «укажите адрес», и каталог не считает это неудачей.
func TestTrackerWithoutAddressIsOff(t *testing.T) {
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.off = true
	rutor.top["12"] = []source.Release{rel("rutor", "1", "Фильм (2020) BDRip", 50, 1, "aa")}
	rt.top["2110"] = []source.Release{rel("rutracker", "3", "", 30, 2<<30, "cc")}
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, rutor, rt)
	if wait := refresh(t, c, true); wait != checkEvery {
		t.Fatalf("выключенный трекер посчитан неудачей: следующая проверка через %v", wait)
	}
	if rutor.Calls("top") != 0 || rutor.Calls("categories") != 0 {
		t.Fatalf("к выключенному трекеру ходили: топ %d, разделы %d", rutor.Calls("top"), rutor.Calls("categories"))
	}
	if got := problemText(t, db, "catalog.rutor.address"); got != "Укажите адрес Rutor в настройках" {
		t.Fatalf("проблема адреса: %q", got)
	}
	if got := problemText(t, db, "catalog.rutor"); got != "" {
		t.Fatalf("выключенный трекер «не отвечает»: %q", got)
	}
	if es := list(t, c, ListOptions{}); len(es) != 1 || es[0].Tracker != "rutracker" {
		t.Fatalf("каталог: %+v", es)
	}
	if did, err := c.enrichStep(ctx, "rutor"); err != nil || did || rutor.Calls("details") != 0 {
		t.Fatalf("догрузка выключенного: did %v, err %v, страниц %d", did, err, rutor.Calls("details"))
	}

	rutor.set(func() { rutor.off = false })
	refresh(t, c, true)
	if rutor.Calls("top") != 1 || problemText(t, db, "catalog.rutor.address") != "" {
		t.Fatalf("адрес ввели: топ %d, проблема %q", rutor.Calls("top"), problemText(t, db, "catalog.rutor.address"))
	}
}

// Поиск идёт только по трекерам с адресом; без адресов — пустой ответ без ошибки.
func TestSearchSkipsTrackersWithoutAddress(t *testing.T) {
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.off, rt.off = true, true
	c, _ := newCatalog(t, openDB(t), nil, rutor, rt)
	st := waitSearch(t, c, "матрица")
	if len(st.Results) != 0 || len(st.Trackers) != 0 || rutor.Calls("search")+rt.Calls("search") != 0 {
		t.Fatalf("поиск без адресов: %+v, запросов %d", st, rutor.Calls("search")+rt.Calls("search"))
	}
	rt.set(func() { rt.off = false })
	rt.search = []source.Release{rel("rutracker", "9", "Матрица [1999]", 90, 1, "x")}
	st = waitSearch(t, c, "матрица 2")
	if len(st.Results) != 1 || st.Trackers["rutracker"] != SearchOK || rutor.Calls("search") != 0 {
		t.Fatalf("поиск по одному трекеру: %+v", st)
	}
}
