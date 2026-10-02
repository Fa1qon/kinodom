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

// listAfter — порция раздела через маршрут, как у пульта: карточки после места after (-1 — с начала).
func listAfter(t *testing.T, h http.Handler, tracker, section string, after int) (ListView, int) {
	t.Helper()
	var v ListView
	code := getJSONErr(t, h, fmt.Sprintf("/api/v1/catalog?tracker=%s&section=%s&after=%d", tracker, section, after), &v)
	return v, code
}

// walkSection — раздел, как его листает пульт: порция за порцией по курсору, пока есть ещё (не больше
// limit порций). seen — сколько раз показана каждая карточка; after — курсор после последней порции.
func walkSection(t *testing.T, h http.Handler, tracker, section string, after, limit int) (seen map[int64]int, next int, more bool) {
	t.Helper()
	seen = map[int64]int{}
	next, more = after, true
	for i := 0; i < limit && more; i++ {
		v, code := listAfter(t, h, tracker, section, next)
		if code != 200 {
			t.Fatalf("порция %d (после %d): код %d", i+1, next, code)
		}
		for _, e := range v.Entries {
			seen[e.ID]++
		}
		if len(v.Entries) > 0 && v.Next <= next {
			t.Fatalf("курсор не двигается: %d → %d", next, v.Next)
		}
		next, more = v.Next, v.More
	}
	return seen, next, more
}

// shownOnce — каждая карточка раздела показана ровно один раз (склеенная по ходу может пропасть — её
// раздача уже в показанной карточке).
func shownOnce(t *testing.T, c *Catalog, tracker, section string, seen map[int64]int) {
	t.Helper()
	all := list(t, c, ListOptions{Tracker: tracker, Category: section, Limit: 100000})
	missing := 0
	for _, e := range all {
		if seen[e.ID] == 0 {
			missing++
		}
	}
	var dups []int64
	for id, n := range seen {
		if n > 1 {
			dups = append(dups, id)
		}
	}
	if missing > 0 || len(dups) > 0 {
		t.Fatalf("карточек в разделе %d: не показано %d, показано дважды %v", len(all), missing, dups)
	}
}

// dupDesc — n раздач, по perWork раздач на произведение (разные качества), раздающие убывают.
func dupDesc(tracker string, n, perWork int) []source.Release {
	q := []string{"WEB-DL 1080p", "BDRip 720p", "HDRip", "WEB-DLRip"}
	var rs []source.Release
	for i := range n {
		rs = append(rs, rel(tracker, fmt.Sprint(i+1), fmt.Sprintf("Кино %d (2020) %s", i/perWork, q[i%perWork]), 1000-i, 1<<30, fmt.Sprintf("h%d", i)))
	}
	return rs
}

func rutorSection(t *testing.T, rs []source.Release) (*Catalog, *fakeSource, *http.ServeMux) {
	t.Helper()
	rutor := newFake("rutor")
	rutor.top["12"] = rs
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	return c, rutor, mux
}

// Раздел Rutor прокручивается дальше первой сотни: следующая страница трекера — по запросу, одна на сотню
// (спека 11b, 7.2).
func TestDeepPortionsRutor(t *testing.T) {
	c, rutor, mux := rutorSection(t, manyDesc("rutor", 250))
	seen, _, more := walkSection(t, mux, "rutor", "12", -1, 30)
	if more || len(seen) != 250 {
		t.Fatalf("карточек %d, нужно 250 (ещё %v)", len(seen), more)
	}
	shownOnce(t, c, "rutor", "12", seen)
	if n := rutor.Calls("toppage"); n > 3 {
		t.Fatalf("страниц трекера запрошено %d — по одной на сотню", n)
	}
}

// Ревью 11b-Г, Critical 1: раздачи одного фильма склеиваются в карточку — порции по раздачам, а курсор по
// карточкам: ни одна не теряется, пустых порций и выкачивания трекера нет.
func TestDeepWithDuplicatesNoLoss(t *testing.T) {
	for _, per := range []int{2, 3} {
		t.Run(fmt.Sprint(per), func(t *testing.T) {
			c, rutor, mux := rutorSection(t, dupDesc("rutor", 600, per))
			seen, _, more := walkSection(t, mux, "rutor", "12", -1, 60)
			if more || len(seen) != 600/per {
				t.Fatalf("показано %d карточек из %d (ещё %v)", len(seen), 600/per, more)
			}
			shownOnce(t, c, "rutor", "12", seen)
			if n := rutor.Calls("toppage"); n > 6 {
				t.Fatalf("страниц трекера %d — у него их 6", n)
			}
		})
	}
}

// Ревью 11b-Г, Important 1: у Rutor список по раздающим кончается страницами без раздающих — это конец,
// дальше трекер не спрашивается.
func TestDeepZeroSeederTail(t *testing.T) {
	rs := manyDesc("rutor", 1500)
	for i := 250; i < len(rs); i++ {
		rs[i].Seeders = 0
	}
	_, rutor, mux := rutorSection(t, rs)
	seen, _, more := walkSection(t, mux, "rutor", "12", -1, 40)
	if more || len(seen) != 250 {
		t.Fatalf("карточек %d, нужно 250 (ещё %v)", len(seen), more)
	}
	if n := rutor.Calls("toppage"); n > 3 {
		t.Fatalf("после раздач с раздающими трекер спрошен %d раз", n)
	}
}

// Ревью 11b-Г, Important 1: страница за концом списка повторяет прежние раздачи — порции без новых раздач
// дважды подряд — конец, бесконечного цикла нет.
func TestDeepRepeatedPagesEnd(t *testing.T) {
	_, rutor, mux := rutorSection(t, manyDesc("rutor", 300))
	rutor.set(func() { rutor.repeatAfter = 2 }) // страницы с четвёртой — снова третья (полная)
	walkSection(t, mux, "rutor", "12", -1, 40)
	if n := rutor.Calls("toppage"); n > 4 {
		t.Fatalf("страниц трекера %d — повторы должны кончить подгрузку", n)
	}
}

// Список кончился — больше не просим; порция не пришла — ошибка, если показать нечего, пульт повторит по
// тому же курсору (Review Focus 2).
func TestDeepPortionsEnd(t *testing.T) {
	c, rutor, mux := rutorSection(t, manyDesc("rutor", 130))
	rutor.set(func() { rutor.pageErr = errors.New("Rutor не отвечает") })
	seen, next, more := walkSection(t, mux, "rutor", "12", -1, 4) // первая сотня — из базы
	if len(seen) != 96 || !more {
		t.Fatalf("первая сотня без трекера: %d, ещё %v", len(seen), more)
	}
	v, code := listAfter(t, mux, "rutor", "12", next)
	if code == 200 && len(v.Entries) == 0 {
		t.Fatal("порция не пришла и показать нечего — нужна ошибка, чтобы пульт повторил")
	}
	rutor.set(func() { rutor.pageErr = nil })
	rest, _, more := walkSection(t, mux, "rutor", "12", next, 10)
	for id, n := range rest {
		seen[id] += n
	}
	if more || len(seen) != 130 {
		t.Fatalf("после ошибки — дальше по курсору: %d, ещё %v", len(seen), more)
	}
	shownOnce(t, c, "rutor", "12", seen)
	calls := rutor.Calls("toppage")
	if v, _ := listAfter(t, mux, "rutor", "12", -1); v.More && len(v.Entries) == 0 || rutor.Calls("toppage") != calls {
		t.Fatalf("после конца списка трекер не спрашивается: запросов %d → %d", calls, rutor.Calls("toppage"))
	}
}

// Rutracker: глубже сотни — из списка в памяти; после перезапуска — список раздела из API один раз
// (Review Focus 5).
func TestDeepRutrackerAfterRestart(t *testing.T) {
	rt := newFake("rutracker")
	rt.tree = rutrackerTree()
	rt.top["56"] = manyDesc("rutracker", 250)
	db := openDB(t)
	c, _ := newCatalog(t, db, func(o *Options) { o.Sections = []Section{{"rutracker", "46", true}} }, rt)
	refresh(t, c, false)
	refresh(t, c, true)
	tops := rt.Calls("top")
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	seen, next, _ := walkSection(t, mux, "rutracker", "46", -1, 6)
	if len(seen) <= 100 || rt.Calls("top") != tops {
		t.Fatalf("глубже сотни — из памяти: карточек %d, запросов API %d → %d", len(seen), tops, rt.Calls("top"))
	}
	c2, _ := newCatalog(t, db, func(o *Options) { o.Sections = []Section{{"rutracker", "46", true}} }, rt)
	c2.reexpand(ctx)
	mux2 := http.NewServeMux()
	c2.Register(muxRouter{mux2})
	before := rt.Calls("top")
	rest, _, more := walkSection(t, mux2, "rutracker", "46", next, 10)
	for id, n := range rest {
		seen[id] += n
	}
	if n := rt.Calls("top") - before; n != 3 { // 46, 56, 2076 — один раз
		t.Fatalf("после перезапуска — список раздела один раз: запросов %d", n)
	}
	if more || len(seen) != 250 {
		t.Fatalf("после перезапуска — дальше по курсору: %d, ещё %v", len(seen), more)
	}
	shownOnce(t, c2, "rutracker", "46", seen)
}

// Обновление раздела заменяет и глубокие порции (Review Focus 3).
func TestRefreshDropsDeepPortions(t *testing.T) {
	c, rutor, mux := rutorSection(t, manyDesc("rutor", 250))
	walkSection(t, mux, "rutor", "12", -1, 9) // глубже сотни
	rutor.set(func() { rutor.top["12"] = manyDesc("rutor", 120) })
	refresh(t, c, true)
	if es := list(t, c, ListOptions{Tracker: "rutor", Category: "12", Limit: 1000}); len(es) != 100 {
		t.Fatalf("после обновления — первая сотня: %d", len(es))
	}
	seen, _, more := walkSection(t, mux, "rutor", "12", -1, 10)
	if more || len(seen) != 120 {
		t.Fatalf("новый список: %d, ещё %v", len(seen), more)
	}
}

// Флейк TestRefreshDropsDeepPortions (1 из 7 прогонов): подкачка следующей страницы раздела в фоне начата до
// обновления и кончилась после — она записывала курсор и «список кончился» прежнего списка поверх сброса, и
// после обновления раздел обрывался на первой сотне. Обновление ждёт идущую подкачку.
func TestRefreshWaitsForDeepPrefetch(t *testing.T) {
	c, rutor, mux := rutorSection(t, manyDesc("rutor", 250))
	cat := CategoryRef{"rutor", "12"}
	c.prefetchDeep(cat) // вторая сотня — в базе
	c.deepWG.Wait()
	block := make(chan struct{})
	rutor.set(func() { rutor.pageBlock = block })
	c.prefetchDeep(cat) // третья страница — повисла у трекера
	for rutor.Calls("toppage") < 2 {
		time.Sleep(time.Millisecond)
	}
	rutor.set(func() { rutor.top["12"] = manyDesc("rutor", 120) })
	done := make(chan error, 1)
	go func() {
		_, err := c.refreshPass(ctx, true)
		done <- err
	}()
	var err error
	finished := false
	select {
	case err = <-done:
		finished = true // обновление не ждало подкачку
	case <-time.After(300 * time.Millisecond):
	}
	close(block)
	rutor.set(func() { rutor.pageBlock = nil })
	if !finished {
		err = <-done
	}
	if err != nil {
		t.Fatal(err)
	}
	c.deepWG.Wait()
	seen, _, more := walkSection(t, mux, "rutor", "12", -1, 10)
	if more || len(seen) != 120 {
		t.Fatalf("новый список: %d, ещё %v", len(seen), more)
	}
}

// Ревью 11b-Г, Important 2: показанная порция — в начало догрузки вне очереди (видимое — первым), по
// порядку показа; прежние просьбы — за ней.
func TestDeepPortionEnqueuedFirst(t *testing.T) {
	cat, _, mux := rutorSection(t, manyDesc("rutor", 250))
	_, next, _ := walkSection(t, mux, "rutor", "12", -1, 5)
	v, _ := listAfter(t, mux, "rutor", "12", next)
	cat.mu.Lock()
	found := slices.Clone(cat.found["rutor"])
	cat.mu.Unlock()
	if len(found) == 0 || len(v.Entries) == 0 || found[0] != v.Entries[0].ID {
		t.Fatalf("в догрузке первой %v, на экране первая %v", found[:min(3, len(found))], v.Entries[0].ID)
	}
	if len(found) > foundLimit {
		t.Fatalf("очередь догрузки растёт без предела: %d", len(found))
	}
}

// Вживую 11b-Г: Rutor считает раздающих неточно — на следующей странице трекера бывает раздача «выше»
// хвоста первой сотни. Порция не сдвигает уже показанные карточки: не повторяются и не теряются.
func TestDeepPortionKeepsShownPages(t *testing.T) {
	rs := manyDesc("rutor", 250)
	rs[150].Seeders = 950 // вторая страница трекера, а раздающих больше, чем у половины первой сотни
	c, _, mux := rutorSection(t, rs)
	seen, _, _ := walkSection(t, mux, "rutor", "12", -1, 30)
	if len(seen) != 250 {
		t.Fatalf("карточек %d из 250", len(seen))
	}
	shownOnce(t, c, "rutor", "12", seen)
}

// Ревью 11b-Г, Important 3а: порция приносит раздачу уже показанного фильма с большим числом раздающих —
// карточка остаётся на своём месте (место карточки — наименьшее место её раздач).
func TestDeepBetterRipKeepsPlace(t *testing.T) {
	rs := manyDesc("rutor", 250)
	rs[150].Title = "Кино 5 (2020) BDRip" // тот же фильм, что на 6-м месте, другая раздача
	rs[150].Seeders = 999
	_, _, mux := rutorSection(t, rs)
	// Лучшая раздача становится лицом карточки (номер карточки меняется) — сверка по фильмам.
	films := map[string]int{}
	after := -1
	for i := 0; i < 30; i++ {
		v, code := listAfter(t, mux, "rutor", "12", after)
		if code != 200 {
			t.Fatalf("код %d", code)
		}
		for _, e := range v.Entries {
			films[e.Name]++
		}
		if after = v.Next; !v.More {
			break
		}
	}
	var dups []string
	for name, n := range films {
		if n > 1 {
			dups = append(dups, name)
		}
	}
	if len(films) != 249 || len(dups) > 0 {
		t.Fatalf("фильмов показано %d из 249, дважды %v", len(films), dups)
	}
}

// Ревью 11b-Г, Important 3б: раздачи Rutracker приходят без названий; пока листают, догрузка даёт названия,
// и раздачи одного фильма склеиваются в уже показанной части — следующие порции ничего не перескакивают.
func TestDeepMergeDuringScroll(t *testing.T) {
	rt := newFake("rutracker")
	rt.tree = rutrackerTree()
	var rs []source.Release
	for i := range 200 {
		rs = append(rs, rel("rutracker", fmt.Sprint(i+1), "", 1000-i, 1<<30, fmt.Sprintf("h%d", i)))
	}
	rt.top["56"] = rs
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutracker", "46", true}} }, rt)
	refresh(t, c, false)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	seen, next, _ := walkSection(t, mux, "rutracker", "46", -1, 3)
	for i := range 72 { // догрузка дала названия первым 72 раздачам: по две раздачи на фильм
		if err := c.st.setTitleIfEmpty(ctx, "rutracker", fmt.Sprint(i+1), fmt.Sprintf("Фильм %d (2025) WEB-DL %d", i/2, i%2)); err != nil {
			t.Fatal(err)
		}
	}
	rest, _, _ := walkSection(t, mux, "rutracker", "46", next, 20)
	for id, n := range rest {
		seen[id] += n
	}
	shownOnce(t, c, "rutracker", "46", seen)
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
