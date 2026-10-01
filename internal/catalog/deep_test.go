package catalog

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"kinodom/internal/source"
)

// Подраздел Rutracker — все его видеофорумы одним списком по раздающим (спека 11b, 7.1): служебный
// подфорум и раздачи без раздающих — нет.
func TestRutrackerSectionMergesForums(t *testing.T) {
	rt := newFake("rutracker")
	rt.tree = append(rutrackerTree(), source.Category{ID: "77", Name: "Ищу / Предлагаю / Анонсы ТВ", ParentID: "46"})
	rt.top["46"] = []source.Release{rel("rutracker", "1", "А", 10, 1, "a")}
	rt.top["56"] = []source.Release{rel("rutracker", "2", "Б", 50, 1, "b"), rel("rutracker", "9", "Мёртвая", 0, 1, "z")}
	rt.top["2076"] = []source.Release{rel("rutracker", "3", "В", 30, 1, "c")}
	rt.top["77"] = []source.Release{rel("rutracker", "8", "Просьба", 99, 1, "s")}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutracker", "46", true}} }, rt)
	refresh(t, c, false) // дерево
	refresh(t, c, true)
	var got []string
	for _, e := range list(t, c, ListOptions{Tracker: "rutracker", Category: "46"}) {
		got = append(got, e.TopicID)
	}
	if strings.Join(got, ",") != "2,3,1" {
		t.Fatalf("подраздел 46: %v", got)
	}
}

// manyDesc — n раздач с убывающими раздающими и разными названиями.
func manyDesc(tracker string, n int) []source.Release {
	var rs []source.Release
	for i := range n {
		rs = append(rs, rel(tracker, fmt.Sprint(i+1), fmt.Sprintf("Кино %d (2020) WEB-DL", i), 1000-i, 1<<30, fmt.Sprintf("h%d", i)))
	}
	return rs
}

// listPage — страница каталога через маршрут, как у пульта.
func listPage(t *testing.T, h http.Handler, tracker, section string, page int) (ListView, int) {
	t.Helper()
	var v ListView
	code := getJSONErr(t, h, fmt.Sprintf("/api/v1/catalog?tracker=%s&section=%s&page=%d", tracker, section, page), &v)
	return v, code
}

// Раздел Rutor прокручивается дальше первой сотни: следующая страница трекера — по запросу, одна на сотню
// (спека 11b, 7.2).
func TestDeepPortionsRutor(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = manyDesc("rutor", 250)
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	seen := map[int64]bool{}
	var pages []int
	for p := 1; p <= 12; p++ {
		v, code := listPage(t, mux, "rutor", "12", p)
		if code != 200 {
			t.Fatalf("страница %d: %d", p, code)
		}
		pages = append(pages, v.Pages)
		for _, e := range v.Entries {
			seen[e.ID] = true
		}
		if p >= v.Pages {
			break
		}
	}
	if len(seen) != 250 {
		t.Fatalf("карточек %d, нужно 250 (страницы %v)", len(seen), pages)
	}
	if n := rutor.Calls("toppage"); n > 3 {
		t.Fatalf("страниц трекера запрошено %d — по одной на сотню", n)
	}
}

// Список кончился — pages не растёт; порция не пришла — ошибка, пульт повторит (Review Focus 2).
func TestDeepPortionsEnd(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = manyDesc("rutor", 130)
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	rutor.set(func() { rutor.pageErr = errors.New("Rutor не отвечает") })
	if _, code := listPage(t, mux, "rutor", "12", 5); code == 200 {
		t.Fatal("порция не пришла — нужна ошибка, чтобы пульт повторил")
	}
	rutor.set(func() { rutor.pageErr = nil })
	last := 0
	for p := 1; p <= 10; p++ {
		v, code := listPage(t, mux, "rutor", "12", p)
		if code != 200 {
			t.Fatalf("страница %d: %d", p, code)
		}
		last = v.Pages
		if p >= v.Pages {
			break
		}
	}
	if last != 6 { // 130 карточек по 24
		t.Fatalf("страниц %d, нужно 6", last)
	}
	calls := rutor.Calls("toppage")
	if v, _ := listPage(t, mux, "rutor", "12", 6); v.Pages != 6 || rutor.Calls("toppage") != calls {
		t.Fatalf("после конца списка трекер не спрашивается: страниц %d, запросов %d → %d", v.Pages, calls, rutor.Calls("toppage"))
	}
}

// Rutracker: глубже сотни — из списка в памяти; после перезапуска — список раздела из API один раз
// (Review Focus 5).
func TestDeepRutrackerAfterRestart(t *testing.T) {
	rt := newFake("rutracker")
	rt.tree = rutrackerTree()
	rt.top["56"] = manyDesc("rutracker", 150)
	db := openDB(t)
	c, _ := newCatalog(t, db, func(o *Options) { o.Sections = []Section{{"rutracker", "46", true}} }, rt)
	refresh(t, c, false)
	refresh(t, c, true)
	tops := rt.Calls("top")
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	if v, _ := listPage(t, mux, "rutracker", "46", 5); len(v.Entries) == 0 || rt.Calls("top") != tops {
		t.Fatalf("глубже сотни — из памяти: карточек %d, запросов API %d → %d", len(v.Entries), tops, rt.Calls("top"))
	}
	c2, _ := newCatalog(t, db, func(o *Options) { o.Sections = []Section{{"rutracker", "46", true}} }, rt)
	c2.reexpand(ctx)
	mux2 := http.NewServeMux()
	c2.Register(muxRouter{mux2})
	before := rt.Calls("top")
	listPage(t, mux2, "rutracker", "46", 6)
	listPage(t, mux2, "rutracker", "46", 7)
	if n := rt.Calls("top") - before; n != 3 { // 46, 56, 2076 — один раз
		t.Fatalf("после перезапуска — список раздела один раз: запросов %d", n)
	}
}

// Обновление раздела заменяет и глубокие порции (Review Focus 3).
func TestRefreshDropsDeepPortions(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = manyDesc("rutor", 250)
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	listPage(t, mux, "rutor", "12", 9) // глубже сотни
	rutor.set(func() { rutor.top["12"] = manyDesc("rutor", 120) })
	refresh(t, c, true)
	if es := list(t, c, ListOptions{Tracker: "rutor", Category: "12", Limit: 1000}); len(es) != 100 {
		t.Fatalf("после обновления — первая сотня: %d", len(es))
	}
	listPage(t, mux, "rutor", "12", 5)
	if es := list(t, c, ListOptions{Tracker: "rutor", Category: "12", Limit: 1000}); len(es) != 120 {
		t.Fatalf("вторая сотня нового списка: всего %d", len(es))
	}
}

// Карточки порции без страницы раздачи — в догрузку вне очереди по порядку показа (как у поиска).
func TestDeepPortionEnqueuedInOrder(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = manyDesc("rutor", 150)
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	v, _ := listPage(t, mux, "rutor", "12", 5)
	c.mu.Lock()
	found := slices.Clone(c.found["rutor"])
	c.mu.Unlock()
	if len(found) == 0 || len(v.Entries) == 0 || found[0] != v.Entries[0].ID {
		t.Fatalf("в догрузке %v, на странице первая %v", found, v.Entries[0].ID)
	}
}

// Вживую 11b-Г: Rutor считает раздающих неточно — на следующей странице трекера бывает раздача «выше»
// хвоста первой сотни. Порция не сдвигает уже показанные страницы: карточки не повторяются и не теряются.
func TestDeepPortionKeepsShownPages(t *testing.T) {
	rutor := newFake("rutor")
	rs := manyDesc("rutor", 250)
	rs[150].Seeders = 950 // вторая страница трекера, а раздающих больше, чем у половины первой сотни
	rutor.top["12"] = rs
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	seen := map[int64]int{}
	for p := 1; p <= 20; p++ {
		v, code := listPage(t, mux, "rutor", "12", p)
		if code != 200 {
			t.Fatalf("страница %d: %d", p, code)
		}
		for _, e := range v.Entries {
			seen[e.ID]++
		}
		if p >= v.Pages {
			break
		}
	}
	var dups []int64
	for id, n := range seen {
		if n > 1 {
			dups = append(dups, id)
		}
	}
	if len(dups) > 0 || len(seen) != 250 {
		t.Fatalf("карточек %d из 250, повторы %v", len(seen), dups)
	}
}

// Вживую 11b-Г: форум без раздач API отдаёт 404 — подраздел обновляется без него, а не встаёт целиком.
func TestSectionSkipsMissingForum(t *testing.T) {
	rt := newFake("rutracker")
	rt.tree = groupsTree()
	rt.top["252"] = []source.Release{rel("rutracker", "252", "Фильм (2026) WEB-DL", 10, 1<<30, "h252")}
	rt.topErrs = map[string]error{"1950": fmt.Errorf("Rutracker API: /v1/static/pvc/f/1950 — ответ 404 (%w)", source.ErrNoSection)}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutracker", "7", true}} }, rt)
	refresh(t, c, false)
	refresh(t, c, true)
	if es := list(t, c, ListOptions{Tracker: "rutracker", Category: "7"}); len(es) != 1 {
		t.Fatalf("подраздел без форума с 404: %d карточек", len(es))
	}
}
