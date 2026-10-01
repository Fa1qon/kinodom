package catalog

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"kinodom/internal/source"
)

// Число скачиваний (план 14Б): пришло — сохраняется; строка без числа (pvc, t1) его не затирает;
// страница раздачи обновляет; в карточке — число и дата добавления.
func TestDownloadsKeptWhenUnknown(t *testing.T) {
	db := openDB(t)
	st := catalogStore{db}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r := rel("t2", "1", "Кино (2020) WEB-DL", 5, 1, "aa")
	r.Downloads, r.Added = 500, now.Add(-time.Hour)
	ids, err := st.saveFound(ctx, []source.Release{r}, now)
	if err != nil {
		t.Fatal(err)
	}
	r.Downloads = 0
	if _, err := st.saveFound(ctx, []source.Release{r}, now); err != nil {
		t.Fatal(err)
	}
	got, _ := st.rowsByID(ctx, ids)
	if got[ids[0]].Downloads != 500 {
		t.Fatalf("после строки без числа: %d", got[ids[0]].Downloads)
	}
	d := source.Details{Release: r}
	d.Downloads = 839
	if err := st.saveDetails(ctx, ids[0], d, 0, "", "", now); err != nil {
		t.Fatal(err)
	}
	got, _ = st.rowsByID(ctx, ids)
	e := Entry{Downloads: got[ids[0]].Downloads, Added: got[ids[0]].Added}
	if v := e.View(); v.Downloads != 839 || v.Added == nil || !v.Added.Equal(now.Add(-time.Hour)) {
		t.Fatalf("карточка: %+v", v)
	}
	if v := (Entry{}).View(); v.Added != nil {
		t.Fatalf("без даты: %v", v.Added)
	}
}

// listOrder — порция раздела в порядке order, как у пульта.
func listOrder(t *testing.T, h http.Handler, tracker, section, order string, after int) ListView {
	t.Helper()
	var v ListView
	if code := getJSONErr(t, h, fmt.Sprintf("/api/v1/catalog?tracker=%s&section=%s&after=%d&order=%s", tracker, section, after, order), &v); code != 200 {
		t.Fatalf("код %d", code)
	}
	return v
}

// walkOrder — раздел в порядке order целиком: названия по порядку показа (без «Кино »); повтор — ошибка.
func walkOrder(t *testing.T, h http.Handler, tracker, section, order string) []string {
	t.Helper()
	var got []string
	seen := map[int64]bool{}
	after, more := -1, true
	for i := 0; more; i++ {
		if i > 50 {
			t.Fatal("порции не кончаются")
		}
		v := listOrder(t, h, tracker, section, order, after)
		if v.Order != order {
			t.Fatalf("порядок %q, просили %q", v.Order, order)
		}
		for _, e := range v.Entries {
			if seen[e.ID] {
				t.Fatalf("карточка %d показана дважды", e.ID)
			}
			seen[e.ID] = true
			got = append(got, strings.TrimPrefix(e.Title, "Кино "))
		}
		after, more = v.Next, v.More
	}
	return got
}

// t1 «Новые» — страницы сайта в его порядке; раздачи без раздающих пропущены, а список из-за них не
// кончается (Review Focus 1).
func TestOrderNewt1(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = manyDesc("t1", 120)
	t1.sortOrders = []string{source.OrderLeechers, source.OrderNew}
	var fresh []source.Release
	for i := range 250 { // каждая третья — без раздающих
		fresh = append(fresh, rel("t1", fmt.Sprint(1000+i), fmt.Sprintf("Кино N%03d (2026) WEB-DL", i), i%3, 1<<30, fmt.Sprintf("n%d", i)))
	}
	t1.sorted = map[string]map[string][]source.Release{source.OrderNew: {"12": fresh}}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"t1", "12", false}} }, t1)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	got := walkOrder(t, mux, "t1", "12", source.OrderNew)
	var want []string
	for i := range 250 {
		if i%3 != 0 {
			want = append(want, fmt.Sprintf("N%03d (2026) WEB-DL", i))
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("показано %d из %d; начало %v", len(got), len(want), got[:min(5, len(got))])
	}
	if n := t1.Calls("sorted:new"); n != 3 {
		t.Fatalf("страниц сайта %d, нужно 3", n)
	}
	var v ListView
	getJSONErr(t, mux, "/api/v1/catalog?tracker=t1&section=12&after=-1", &v)
	if v.Order != source.OrderSeeders || len(v.Orders) != 3 || v.Orders[0] != (OrderView{"seeders", "Раздающие"}) {
		t.Fatalf("без порядка: %q, порядки %v", v.Order, v.Orders)
	}
}

// t2 «Качающие» и «Новые» — из списка раздела API (он уже в памяти), без запросов к форуму; склейка
// одного фильма — карточка по лучшему месту (Review Focus 4).
func TestOrdert2FromSectionList(t *testing.T) {
	rt := newFake("t2")
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var rs []source.Release
	for i := range 150 {
		r := rel("t2", fmt.Sprint(i+1), fmt.Sprintf("Кино %03d (2020) WEB-DL", i), 1000-i, 1<<30, fmt.Sprintf("h%d", i))
		r.Leechers = i                                     // качающих больше у хвоста
		r.Added = day.Add(time.Duration(i%50) * time.Hour) // новизна — по кругу
		rs = append(rs, r)
	}
	rs[149].Title = "Кино 000 (2020) BDRip" // тот же фильм, что первый, — самая «качаемая» раздача
	rt.top["2110"] = rs
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"t2", "2110", false}} }, rt)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	got := walkOrder(t, mux, "t2", "2110", source.OrderLeechers)
	if len(got) != 149 || got[0] != "000 (2020) WEB-DL" && got[0] != "000 (2020) BDRip" || got[1] != "148 (2020) WEB-DL" {
		t.Fatalf("качающие: %d, начало %v", len(got), got[:min(3, len(got))])
	}
	gotNew := walkOrder(t, mux, "t2", "2110", source.OrderNew)
	if len(gotNew) != 149 || !strings.HasPrefix(gotNew[0], "049") {
		t.Fatalf("новые: %d, начало %v", len(gotNew), gotNew[:min(3, len(gotNew))])
	}
	if rt.Calls("sorted:leechers")+rt.Calls("sorted:new") != 0 {
		t.Fatal("качающие и новые t2 — не с форума")
	}
}

// «Скачивания» — только у трекера, что их отдаёт; иначе — умолчание из настроек, а его нет — раздающие
// (Review Focus 2). Подраздел t2 — все его видеофорумы одним запросом (Review Focus 5).
func TestOrderFallbackAndForums(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = manyDesc("t1", 30)
	t1.sortOrders = []string{source.OrderLeechers, source.OrderNew}
	t1.sorted = map[string]map[string][]source.Release{source.OrderNew: {"12": manyDesc("t1", 30)}}
	rt := newFake("t2")
	rt.tree = t2Tree()
	rt.top["56"] = manyDesc("t2", 10)
	rt.top["2076"] = manyDesc("t2", 5)
	rt.sortOrders = []string{source.OrderDownloads}
	rt.sorted = map[string]map[string][]source.Release{source.OrderDownloads: {"46": manyDesc("t2", 7)}}
	c, _ := newCatalog(t, openDB(t), func(o *Options) {
		o.Sections = []Section{{"t1", "12", false}, {"t2", "46", true}}
	}, t1, rt)
	refresh(t, c, false)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	if v := listOrder(t, mux, "t1", "12", source.OrderDownloads, -1); v.Order != source.OrderSeeders {
		t.Fatalf("у t1 скачиваний нет — %q", v.Order)
	}
	c.SetDefaultOrder(source.OrderNew)
	if v := listOrder(t, mux, "t1", "12", source.OrderDownloads, -1); v.Order != source.OrderNew {
		t.Fatalf("умолчание «новые» — %q", v.Order)
	}
	var v ListView
	getJSONErr(t, mux, "/api/v1/catalog?tracker=t1&section=12&after=-1", &v)
	if v.Order != source.OrderNew {
		t.Fatalf("без порядка в адресе — умолчание: %q", v.Order)
	}
	if got := walkOrder(t, mux, "t2", "46", source.OrderDownloads); len(got) != 7 {
		t.Fatalf("скачивания: %d", len(got))
	}
	if rt.Calls("sortedForums:46,56,2076") == 0 {
		t.Fatalf("форумы подраздела: %v", rt.calls)
	}
	if got := c.Orders("t2"); !slices.Equal(got, []string{"seeders", "leechers", "new", "downloads"}) {
		t.Fatalf("порядки t2: %v", got)
	}
	rt.set(func() { rt.sortOrders = nil }) // вышли — скачиваний нет
	if got := c.Orders("t2"); slices.Contains(got, source.OrderDownloads) {
		t.Fatalf("без входа: %v", got)
	}
}

// Обновление раздела сбрасывает списки порядков: следующий заход — новый список (Review Focus 3).
func TestRefreshDropsOrderLists(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = manyDesc("t1", 30)
	t1.sortOrders = []string{source.OrderNew}
	t1.sorted = map[string]map[string][]source.Release{source.OrderNew: {"12": manyDesc("t1", 30)}}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"t1", "12", false}} }, t1)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	if got := walkOrder(t, mux, "t1", "12", source.OrderNew); len(got) != 30 {
		t.Fatalf("до обновления: %d", len(got))
	}
	t1.set(func() { t1.sorted[source.OrderNew]["12"] = manyDesc("t1", 12) })
	refresh(t, c, true)
	if got := walkOrder(t, mux, "t1", "12", source.OrderNew); len(got) != 12 {
		t.Fatalf("после обновления: %d", len(got))
	}
}
