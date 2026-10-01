package catalog

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"kinodom/internal/source"
)

// Число скачиваний (план 14Б): пришло — сохраняется; строка без числа (pvc, Rutor) его не затирает;
// страница раздачи обновляет; в карточке — число и дата добавления.
func TestDownloadsKeptWhenUnknown(t *testing.T) {
	db := openDB(t)
	st := catalogStore{db}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	r := rel("rutracker", "1", "Кино (2020) WEB-DL", 5, 1, "aa")
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

// Rutor «Новые» — страницы сайта в его порядке; раздачи без раздающих пропущены, а список из-за них не
// кончается (Review Focus 1).
func TestOrderNewRutor(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = manyDesc("rutor", 120)
	rutor.sortOrders = []string{source.OrderLeechers, source.OrderNew}
	var fresh []source.Release
	for i := range 250 { // каждая третья — без раздающих
		fresh = append(fresh, rel("rutor", fmt.Sprint(1000+i), fmt.Sprintf("Кино N%03d (2026) WEB-DL", i), i%3, 1<<30, fmt.Sprintf("n%d", i)))
	}
	rutor.sorted = map[string]map[string][]source.Release{source.OrderNew: {"12": fresh}}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	got := walkOrder(t, mux, "rutor", "12", source.OrderNew)
	var want []string
	for i := range 250 {
		if i%3 != 0 {
			want = append(want, fmt.Sprintf("N%03d (2026) WEB-DL", i))
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("показано %d из %d; начало %v", len(got), len(want), got[:min(5, len(got))])
	}
	if n := rutor.Calls("sorted:new"); n != 3 {
		t.Fatalf("страниц сайта %d, нужно 3", n)
	}
	var v ListView
	getJSONErr(t, mux, "/api/v1/catalog?tracker=rutor&section=12&after=-1", &v)
	if v.Order != source.OrderSeeders || len(v.Orders) != 3 || v.Orders[0] != (OrderView{"seeders", "Раздающие"}) {
		t.Fatalf("без порядка: %q, порядки %v", v.Order, v.Orders)
	}
}

// Rutracker «Качающие» и «Новые» — из списка раздела API (он уже в памяти), без запросов к форуму; склейка
// одного фильма — карточка по лучшему месту (Review Focus 4).
func TestOrderRutrackerFromSectionList(t *testing.T) {
	rt := newFake("rutracker")
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var rs []source.Release
	for i := range 150 {
		r := rel("rutracker", fmt.Sprint(i+1), fmt.Sprintf("Кино %03d (2020) WEB-DL", i), 1000-i, 1<<30, fmt.Sprintf("h%d", i))
		r.Leechers = i                                     // качающих больше у хвоста
		r.Added = day.Add(time.Duration(i%50) * time.Hour) // новизна — по кругу
		rs = append(rs, r)
	}
	rs[149].Title = "Кино 000 (2020) BDRip" // тот же фильм, что первый, — самая «качаемая» раздача
	rt.top["2110"] = rs
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutracker", "2110", false}} }, rt)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	got := walkOrder(t, mux, "rutracker", "2110", source.OrderLeechers)
	if len(got) != 149 || got[0] != "000 (2020) WEB-DL" && got[0] != "000 (2020) BDRip" || got[1] != "148 (2020) WEB-DL" {
		t.Fatalf("качающие: %d, начало %v", len(got), got[:min(3, len(got))])
	}
	gotNew := walkOrder(t, mux, "rutracker", "2110", source.OrderNew)
	if len(gotNew) != 149 || !strings.HasPrefix(gotNew[0], "049") {
		t.Fatalf("новые: %d, начало %v", len(gotNew), gotNew[:min(3, len(gotNew))])
	}
	if rt.Calls("sorted:leechers")+rt.Calls("sorted:new") != 0 {
		t.Fatal("качающие и новые Rutracker — не с форума")
	}
}

// «Скачивания» — только у трекера, что их отдаёт; иначе — умолчание из настроек, а его нет — раздающие
// (Review Focus 2). Подраздел Rutracker — все его видеофорумы одним запросом (Review Focus 5).
func TestOrderFallbackAndForums(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = manyDesc("rutor", 30)
	rutor.sortOrders = []string{source.OrderLeechers, source.OrderNew}
	rutor.sorted = map[string]map[string][]source.Release{source.OrderNew: {"12": manyDesc("rutor", 30)}}
	rt := newFake("rutracker")
	rt.tree = rutrackerTree()
	inForum := func(f string, rs []source.Release, downloads ...int) []source.Release {
		for i := range rs {
			rs[i].CategoryID = f
			if i < len(downloads) {
				rs[i].Downloads = downloads[i]
			}
		}
		return rs
	}
	rt.top["56"] = inForum("56", manyDesc("rutracker", 10))
	other := manyDesc("rutracker", 5)
	for i := range other {
		other[i].TopicID, other[i].InfoHash = fmt.Sprint(2000+i), fmt.Sprintf("o%d", i)
	}
	rt.top["2076"] = inForum("2076", other)
	rt.sortOrders = []string{source.OrderDownloads}
	// Форум без запроса сортирует только сам по себе (вживую 2026-10-01): «Скачивания» подраздела —
	// первые страницы его форумов, слитые по числу скачиваний.
	d56 := inForum("56", []source.Release{rel("rutracker", "701", "Кино Сто", 9, 1, "d1"), rel("rutracker", "702", "Кино Пятьдесят", 9, 1, "d2")}, 100, 50)
	d2076 := inForum("2076", []source.Release{rel("rutracker", "703", "Кино Восемьдесят", 9, 1, "d3")}, 80)
	rt.sorted = map[string]map[string][]source.Release{source.OrderDownloads: {"56": d56, "2076": d2076}}
	c, _ := newCatalog(t, openDB(t), func(o *Options) {
		o.Sections = []Section{{"rutor", "12", false}, {"rutracker", "46", true}}
	}, rutor, rt)
	refresh(t, c, false)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	if v := listOrder(t, mux, "rutor", "12", source.OrderDownloads, -1); v.Order != source.OrderSeeders {
		t.Fatalf("у Rutor скачиваний нет — %q", v.Order)
	}
	c.SetDefaultOrder(source.OrderNew)
	if v := listOrder(t, mux, "rutor", "12", source.OrderDownloads, -1); v.Order != source.OrderNew {
		t.Fatalf("умолчание «новые» — %q", v.Order)
	}
	var v ListView
	getJSONErr(t, mux, "/api/v1/catalog?tracker=rutor&section=12&after=-1", &v)
	if v.Order != source.OrderNew {
		t.Fatalf("без порядка в адресе — умолчание: %q", v.Order)
	}
	if got := walkOrder(t, mux, "rutracker", "46", source.OrderDownloads); !slices.Equal(got, []string{"Сто", "Восемьдесят", "Пятьдесят"}) {
		t.Fatalf("скачивания: %v", got)
	}
	if rt.Calls("sortedForums:56") != 1 || rt.Calls("sortedForums:2076") != 1 || rt.Calls("sorted:downloads") != 2 {
		t.Fatalf("форумы подраздела — по одному (46 без раздач не спрашивается): %v", rt.calls)
	}
	if got := c.Orders("rutracker"); !slices.Equal(got, []string{"seeders", "leechers", "new", "downloads"}) {
		t.Fatalf("порядки Rutracker: %v", got)
	}
	rt.set(func() { rt.sortOrders = nil }) // вышли — скачиваний нет
	if got := c.Orders("rutracker"); slices.Contains(got, source.OrderDownloads) {
		t.Fatalf("без входа: %v", got)
	}
}

// Обновление раздела сбрасывает списки порядков: следующий заход — новый список (Review Focus 3).
func TestRefreshDropsOrderLists(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = manyDesc("rutor", 30)
	rutor.sortOrders = []string{source.OrderNew}
	rutor.sorted = map[string]map[string][]source.Release{source.OrderNew: {"12": manyDesc("rutor", 30)}}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	if got := walkOrder(t, mux, "rutor", "12", source.OrderNew); len(got) != 30 {
		t.Fatalf("до обновления: %d", len(got))
	}
	rutor.set(func() { rutor.sorted[source.OrderNew]["12"] = manyDesc("rutor", 12) })
	refresh(t, c, true)
	if got := walkOrder(t, mux, "rutor", "12", source.OrderNew); len(got) != 12 {
		t.Fatalf("после обновления: %d", len(got))
	}
}

// Вживую 14Б: в порядке карточка показывает раздачу, которая дала ей место (в «Новых» — самую новую), а не
// раздачу с наибольшим числом раздающих: иначе первой в «Новых» стоит карточка со старой датой.
func TestOrderCardShowsReleaseOfItsPlace(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = manyDesc("rutor", 10)
	rutor.sortOrders = []string{source.OrderNew}
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	fresh := rel("rutor", "501", "Кино Альфа (2020) WEB-DL", 1, 1<<30, "a1")
	fresh.Added = day.Add(48 * time.Hour)
	other := rel("rutor", "502", "Кино Бета (2021) WEB-DL", 5, 1<<30, "b1")
	other.Added = day.Add(24 * time.Hour)
	old := rel("rutor", "503", "Кино Альфа (2020) BDRip", 50, 1<<30, "a2")
	old.Added = day
	rutor.sorted = map[string]map[string][]source.Release{source.OrderNew: {"12": {fresh, other, old}}}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	got := walkOrder(t, mux, "rutor", "12", source.OrderNew)
	if !slices.Equal(got, []string{"Альфа (2020) WEB-DL", "Бета (2021) WEB-DL"}) {
		t.Fatalf("новые: %v", got)
	}
}

// Ревью 14Б, Important 1: первая порция порядка не пришла (трекер не ответил), а показать нечего — раздел
// показывается по раздающим (они в базе), с текстом ошибки порядка; переключатель остаётся (порядки в ответе).
func TestOrderFailureFallsBackToSeeders(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = manyDesc("rutor", 30)
	rutor.sortOrders = []string{source.OrderNew} // а списка «Новых» нет — SortedPage падает
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	rutor.set(func() { rutor.sortedErr = errors.New("Rutor не отвечает") })
	var v ListView
	code := getJSONErr(t, mux, "/api/v1/catalog?tracker=rutor&section=12&after=-1&order=new", &v)
	if code != 200 || v.Order != source.OrderSeeders || len(v.Entries) == 0 || v.OrderError == "" || len(v.Orders) != 2 {
		t.Fatalf("код %d, порядок %q, карточек %d, ошибка %q, порядки %v", code, v.Order, len(v.Entries), v.OrderError, v.Orders)
	}
}

// Ревью 14Б (Minor 1 → Important): выбор в памяти устройства запомнен при прежнем умолчании (since); умолчание
// сменили в «Параметрах» — действует новое, а не память.
func TestOrderSinceDefaultChanged(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = manyDesc("rutor", 30)
	rutor.sortOrders = []string{source.OrderLeechers, source.OrderNew}
	rutor.sorted = map[string]map[string][]source.Release{source.OrderNew: {"12": manyDesc("rutor", 30)},
		source.OrderLeechers: {"12": manyDesc("rutor", 30)}}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	var v ListView
	getJSONErr(t, mux, "/api/v1/catalog?tracker=rutor&section=12&after=-1&order=new&since=seeders", &v)
	if v.Order != source.OrderNew || v.DefaultOrder != source.OrderSeeders {
		t.Fatalf("умолчание то же — память: %q, умолчание %q", v.Order, v.DefaultOrder)
	}
	c.SetDefaultOrder(source.OrderLeechers)
	getJSONErr(t, mux, "/api/v1/catalog?tracker=rutor&section=12&after=-1&order=new&since=seeders", &v)
	if v.Order != source.OrderLeechers || v.DefaultOrder != source.OrderLeechers {
		t.Fatalf("умолчание сменили — оно: %q, умолчание %q", v.Order, v.DefaultOrder)
	}
	getJSONErr(t, mux, "/api/v1/catalog?tracker=rutor&section=12&after=-1&order=new", &v)
	if v.Order != source.OrderNew {
		t.Fatalf("порядок из адреса (без since) — он: %q", v.Order)
	}
}
