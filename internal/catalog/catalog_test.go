package catalog

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"kinodom/internal/netx"
	"kinodom/internal/source"
)

func TestParseCategories(t *testing.T) {
	got, err := ParseCategories(" t2:2110, t1:12 ")
	if err != nil || !slices.Equal(got, []CategoryRef{{"t2", "2110"}, {"t1", "12"}}) {
		t.Fatalf("%v, %v", got, err)
	}
	if got, _ := ParseCategories(""); !slices.Equal(got, DefaultCategories) {
		t.Fatal("пусто — категории по умолчанию")
	}
	if _, err := ParseCategories("t22110"); err == nil {
		t.Fatal("ошибки нет")
	}
}

// Основной каталог: все включённые разделы вместе, по убыванию раздающих; у топа t2 нет
// названий — карточка без названия, пока не загрузится страница раздачи (спека, раздел 7).
func TestRefreshBuildsCatalogBySeeders(t *testing.T) {
	t1, rt := newFake("t1"), newFake("t2")
	t1.top["12"] = []source.Release{rel("t1", "1", "Океаны / Oceans (2009) BDRip-AVC от New-Team", 50, 5<<30, "aa"), rel("t1", "2", "Б (2020) WEB-DL 1080p", 10, 1<<30, "bb")}
	rt.top["2110"] = []source.Release{rel("t2", "3", "", 30, 2<<30, "cc"), rel("t2", "4", "", 5, 3<<30, "dd")}
	c, _ := newCatalog(t, openDB(t), nil, t1, rt)
	if wait := refresh(t, c, true); wait != checkEvery {
		t.Fatalf("после удачи следующая проверка через %v", wait)
	}
	es := list(t, c, ListOptions{})
	var got []string
	for _, e := range es {
		got = append(got, e.TopicID)
	}
	if !slices.Equal(got, []string{"1", "3", "2", "4"}) {
		t.Fatalf("порядок %v", got)
	}
	if es[0].Quality != "BDRip-AVC" || es[0].Category != "Раздел 12" || es[1].Title != "" || es[1].Category != "Раздел 2110" {
		t.Fatalf("карточки %+v", es[:2])
	}
	if es := list(t, c, ListOptions{Tracker: "t2", Category: "2110"}); len(es) != 2 {
		t.Fatalf("фильтр по разделу: %d", len(es))
	}
	cats, err := c.Categories(ctx)
	if err != nil || len(cats) != 2 || cats[0].Count != 2 {
		t.Fatalf("разделы %+v, %v", cats, err)
	}
}

// Одинаковый infohash с двух трекеров — одна карточка с большим числом раздающих.
func TestSameInfohashCollapses(t *testing.T) {
	t1, rt := newFake("t1"), newFake("t2")
	t1.top["12"] = []source.Release{rel("t1", "1", "Фильм (2020) BDRip", 50, 1, "same")}
	rt.top["2110"] = []source.Release{rel("t2", "9", "", 80, 1, "same")}
	c, _ := newCatalog(t, openDB(t), nil, t1, rt)
	refresh(t, c, true)
	es := list(t, c, ListOptions{})
	if len(es) != 1 || es[0].Tracker != "t2" || es[0].Seeders != 80 {
		t.Fatalf("карточки %+v", es)
	}
}

func many(tracker string, n int) []source.Release {
	var rs []source.Release
	for i := range n {
		rs = append(rs, rel(tracker, fmt.Sprint(i+1), fmt.Sprintf("Фильм %d (2020) WEB-DL", i), 100-i, 1<<30, fmt.Sprintf("h%d", i)))
	}
	return rs
}

// Пришло меньше 30 % от прошлого раза — позиции не заменяются, в «Проблемах» — сколько пришло;
// повтор — по расписанию неудач (спека, раздел 7).
func TestFewResultsKeepPositions(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = many("t1", 100)
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, t1)
	refresh(t, c, true)
	t1.set(func() { t1.top["12"] = many("t1", 20) })
	if wait := refresh(t, c, true); wait != time.Minute {
		t.Fatalf("повтор через %v, нужно 1 минуту", wait)
	}
	if n := len(list(t, c, ListOptions{Limit: 500})); n != 100 {
		t.Fatalf("карточек %d — позиции заменились", n)
	}
	if p := problemText(t, db, "catalog.t1:12"); !strings.Contains(p, "пришло 20 раздач вместо 100") {
		t.Fatalf("проблема %q", p)
	}
	if problemText(t, db, "catalog.t1") != "" {
		t.Fatal("трекер ответил — проблема раздела, не трекера")
	}
}

// У всех раздач нули — сломался разбор (переименованное поле, хвост этапа 3): позиции прежние.
func TestAllZeroTopKeepsPositions(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = many("t1", 10)
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, t1)
	refresh(t, c, true)
	var zeros []source.Release
	for _, r := range many("t1", 10) {
		r.Seeders, r.Size = 0, 0
		zeros = append(zeros, r)
	}
	t1.set(func() { t1.top["12"] = zeros })
	refresh(t, c, true)
	if es := list(t, c, ListOptions{}); len(es) != 10 || es[0].Seeders != 100 {
		t.Fatalf("карточки заменились нулями: %+v", es[0])
	}
	if p := problemText(t, db, "catalog.t1:12"); !strings.Contains(p, "изменил разметку") {
		t.Fatalf("проблема %q", p)
	}
}

// Трекер не отвечает — повтор через 1, 5, 15, 15 минут, проблема по трекеру; другой трекер
// обновляется; после восстановления — снова раз в 10 минут, проблема снята.
func TestTrackerDownRetriesAndSetsProblem(t *testing.T) {
	t1, rt := newFake("t1"), newFake("t2")
	t1.topErr = fmt.Errorf("t1 недоступен (t1.info — соединение сброшено): %w", netx.ErrTrackerDown)
	rt.top["2110"] = many("t2", 3)
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, t1, rt)
	var waits []time.Duration
	for range 4 {
		waits = append(waits, refresh(t, c, false))
	}
	if !slices.Equal(waits, []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 15 * time.Minute}) {
		t.Fatalf("паузы %v", waits)
	}
	if p := problemText(t, db, "catalog.t1"); !strings.Contains(p, "соединение сброшено") {
		t.Fatalf("проблема %q", p)
	}
	if rt.Calls("top") != 1 {
		t.Fatalf("t2 обновлялся %d раз — обновлённый раздел ждёт шести часов", rt.Calls("top"))
	}
	t1.set(func() { t1.topErr = nil; t1.top["12"] = many("t1", 3) })
	if wait := refresh(t, c, false); wait != checkEvery || problemText(t, db, "catalog.t1") != "" {
		t.Fatalf("после восстановления: пауза %v, проблема %q", wait, problemText(t, db, "catalog.t1"))
	}
}

// Раздел обновляется, когда прошлое удачное обновление старше шести часов.
func TestRefreshEverySixHours(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = many("t1", 3)
	c, clk := newCatalog(t, openDB(t), nil, t1)
	refresh(t, c, false)
	clk.add(5 * time.Hour)
	refresh(t, c, false)
	if t1.Calls("top") != 1 {
		t.Fatalf("топ запрошен %d раз за 5 часов", t1.Calls("top"))
	}
	clk.add(time.Hour + time.Minute)
	refresh(t, c, false)
	if t1.Calls("top") != 2 {
		t.Fatalf("через 6 часов: %d", t1.Calls("top"))
	}
}

// Каталог не обновлялся двое суток — проблема «Каталог не обновлялся N дн.» с причиной.
func TestStaleCatalogProblem(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = many("t1", 3)
	db := openDB(t)
	c, clk := newCatalog(t, db, nil, t1)
	refresh(t, c, false)
	t1.set(func() { t1.topErr = fmt.Errorf("прокси не отвечает: %w", netx.ErrProxyDown) })
	clk.add(49 * time.Hour)
	refresh(t, c, false)
	if p := problemText(t, db, "catalog.stale"); !strings.Contains(p, "Каталог не обновлялся 2 дн.") || !strings.Contains(p, "прокси не отвечает") {
		t.Fatalf("проблема %q", p)
	}
}

// Ошибки базы — сбой модуля, а не проблема трекера.
func TestDBErrorIsModuleFailure(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = many("t1", 1)
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, t1)
	db.Close()
	if _, err := c.refreshPass(ctx, true); err == nil {
		t.Fatal("ошибки нет")
	} else if errors.Is(err, netx.ErrTrackerDown) {
		t.Fatal(err)
	}
}

// Переименовано одно поле — нули только у раздающих или только у размера, другое разобралось.
// В топе по раздающим так не бывает: позиции прежние, проблема раздела (Review Focus 1).
func TestOneZeroFieldKeepsPositions(t *testing.T) {
	for name, zero := range map[string]func(*source.Release){
		"раздающие": func(r *source.Release) { r.Seeders = 0 },
		"размер":    func(r *source.Release) { r.Size = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			t1 := newFake("t1")
			t1.top["12"] = many("t1", 10)
			db := openDB(t)
			c, _ := newCatalog(t, db, nil, t1)
			refresh(t, c, true)
			var broken []source.Release
			for _, r := range many("t1", 10) {
				zero(&r)
				broken = append(broken, r)
			}
			t1.set(func() { t1.top["12"] = broken })
			refresh(t, c, true)
			if es := list(t, c, ListOptions{}); len(es) != 10 || es[0].Seeders != 100 || es[0].Size != 1<<30 {
				t.Fatalf("карточки заменились нулями: %+v", es[0])
			}
			if p := problemText(t, db, "catalog.t1:12"); !strings.Contains(p, "изменил разметку") {
				t.Fatalf("проблема %q", p)
			}
		})
	}
}

// Трекер не отвечает — остальные его разделы в этом проходе не запрашиваются (у каждого — таймауты
// по зеркалам, минуты), и другой трекер обновляется, не дожидаясь их.
func TestTrackerDownSkipsItsOtherSections(t *testing.T) {
	t1, rt := newFake("t1"), newFake("t2")
	rt.topErr = fmt.Errorf("t2 недоступен (api.t2.cc — таймаут): %w", netx.ErrTrackerDown)
	t1.top["12"] = many("t1", 3)
	db := openDB(t)
	c, _ := newCatalog(t, db, func(o *Options) {
		o.Categories = []CategoryRef{{"t2", "1"}, {"t2", "2"}, {"t2", "3"}, {"t1", "12"}}
	}, t1, rt)
	if wait := refresh(t, c, false); wait != time.Minute {
		t.Fatalf("повтор через %v", wait)
	}
	if rt.Calls("top") != 1 {
		t.Fatalf("разделов лежащего трекера запрошено %d за проход", rt.Calls("top"))
	}
	if n := len(list(t, c, ListOptions{Tracker: "t1"})); n != 3 {
		t.Fatalf("t1 не обновился: карточек %d", n)
	}
	if p := problemText(t, db, "catalog.t2"); !strings.Contains(p, "таймаут") {
		t.Fatalf("проблема %q", p)
	}
}
