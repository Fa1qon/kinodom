package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kinodom/internal/meta"
	"kinodom/internal/source"
)

// Фоновая догрузка — по месту в разделе (спека 11b, 14.4; № 18): первые карточки всех разделов, потом
// вторые. Раньше шла по раздающим через все разделы — первый экран раздела, где раздающих меньше
// («Мультфильмы», документальные), ждал всю глубину популярных.
func TestEnrichFirstScreensFirst(t *testing.T) {
	rutor := newFake("rutor")
	for i := range 3 {
		rutor.top["12"] = append(rutor.top["12"], rel("rutor", fmt.Sprintf("a%d", i), fmt.Sprintf("Кино %d (2020) WEB-DL", i), 100-i, 1<<30, fmt.Sprintf("ha%d", i)))
		rutor.top["1"] = append(rutor.top["1"], rel("rutor", fmt.Sprintf("b%d", i), fmt.Sprintf("Мульт %d (2020) WEB-DL", i), 10-i, 1<<30, fmt.Sprintf("hb%d", i)))
	}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}, {"rutor", "1", false}} }, rutor)
	refresh(t, c, true)
	var order []string
	for range 6 {
		before := map[string]int{}
		for _, id := range []string{"a0", "a1", "a2", "b0", "b1", "b2"} {
			before[id] = rutor.Calls("details:" + id)
		}
		if did, err := c.enrichStep(ctx, "rutor"); !did || err != nil {
			t.Fatal(did, err)
		}
		for id, n := range before {
			if rutor.Calls("details:"+id) > n {
				order = append(order, id)
			}
		}
	}
	if want := []string{"a0", "b0", "a1", "b1", "a2", "b2"}; !slices.Equal(order, want) {
		t.Fatalf("порядок догрузки %v, нужно %v", order, want)
	}
}

// Порция каталога ставит в догрузку вне очереди все свои карточки без страницы (спека 11b, 14.1) — было 20
// из 24: последние четыре ждали общую очередь.
func TestPortionQueuesWholePage(t *testing.T) {
	c, _, mux := rutorSection(t, manyDesc("rutor", 60))
	v, code := listAfter(t, mux, "rutor", "12", -1)
	if code != 200 || len(v.Entries) != PageSize {
		t.Fatalf("порция: %d, карточек %d", code, len(v.Entries))
	}
	c.mu.Lock()
	found := slices.Clone(c.found["rutor"])
	c.mu.Unlock()
	var shown []int64
	for _, e := range v.Entries {
		shown = append(shown, e.ID)
	}
	if !slices.Equal(found[:min(len(found), PageSize)], shown) {
		t.Fatalf("вне очереди %d карточек %v, на экране %v", len(found), found, shown)
	}
}

// Карточки по номерам (спека 11b, 14.1): пульт спрашивает незаконченные карточки у экрана и перерисовывает
// их на месте. Порядок — как в запросе, неизвестные, ушедшие с трекера и мусор пропущены; свои без страницы —
// в догрузку вне очереди первыми, в том же порядке; раздачу источника поиска (чужой трекер) догружать некому.
func TestCardsByIDs(t *testing.T) {
	h := newPosterHost(t)
	rutor := newFake("rutor")
	rutor.top["12"] = manyDesc("rutor", 3)
	rutor.details["2"] = source.Details{Release: source.Release{Title: "Кино 1 / Movie (2020) WEB-DL 1080p"}, PosterURL: h.URL + "/page.jpg"}
	rutor.details["3"] = source.Details{Release: source.Release{Title: "Кино 2 (2020) WEB-DL"}}
	c, _, _ := posterCatalog(t, h, rutor)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	id := map[string]int64{}
	for _, e := range list(t, c, ListOptions{Tracker: "rutor", Category: "12"}) {
		id[e.TopicID] = e.ID
	}
	foreign, err := c.st.saveFound(ctx, []source.Release{rel("kinozal", "9", "Чужое (2020) WEB-DL", 5, 1<<30, "kz9")}, c.now())
	if err != nil {
		t.Fatal(err)
	}
	ask := func(ids ...any) []EntryView {
		t.Helper()
		var parts []string
		for _, x := range ids {
			parts = append(parts, fmt.Sprint(x))
		}
		var v struct{ Entries []EntryView }
		if code := getJSON(t, mux, "/api/v1/catalog/cards?ids="+strings.Join(parts, ","), &v); code != 200 {
			t.Fatalf("ответ %d", code)
		}
		return v.Entries
	}
	if err := c.st.markRemoved(ctx, id["1"]); err != nil {
		t.Fatal(err)
	}
	got := ask(id["3"], 999999, "abc", id["1"], id["2"], foreign[0])
	var order []int64
	for _, e := range got {
		order = append(order, e.ID)
	}
	if want := []int64{id["3"], id["2"], foreign[0]}; !slices.Equal(order, want) {
		t.Fatalf("карточки %v, нужно %v", order, want)
	}
	if !got[0].DetailsPending || got[1].Title != "Кино 1 (2020) WEB-DL" {
		t.Fatalf("до догрузки: %+v", got[:2])
	}
	c.mu.Lock()
	found, alien := slices.Clone(c.found["rutor"]), len(c.found["kinozal"])
	c.mu.Unlock()
	if len(found) != 2 || found[0] != id["3"] || found[1] != id["2"] || alien != 0 {
		t.Fatalf("вне очереди rutor %v, kinozal %d", found, alien)
	}
	enrichAll(t, c, "rutor")
	got = ask(id["2"])
	if len(got) != 1 || got[0].DetailsPending || got[0].Title != "Кино 1 / Movie (2020) WEB-DL 1080p" ||
		got[0].Quality != "WEB-DL 1080p" || got[0].ImageKey != meta.ImageKey(h.URL+"/page.jpg") {
		t.Fatalf("после догрузки: %+v", got)
	}
}

// Номеров больше предела — 400: пульт спрашивает не больше 48 за раз.
func TestCardsLimit(t *testing.T) {
	_, _, mux := rutorSection(t, manyDesc("rutor", 3))
	ids := make([]string, cardsLimit+1)
	for i := range ids {
		ids[i] = fmt.Sprint(i + 1)
	}
	if code := getJSON(t, mux, "/api/v1/catalog/cards?ids="+strings.Join(ids, ","), nil); code != http.StatusBadRequest {
		t.Fatalf("ответ %d", code)
	}
	if code := getJSON(t, mux, "/api/v1/catalog/cards?ids="+strings.Join(ids[:cardsLimit], ","), nil); code != 200 {
		t.Fatalf("ровно предел: %d", code)
	}
}

// stallHost — хостинг картинок: /slow/… висит до конца теста, /down/… — 502, пока не поднят up, остальное —
// картинка; считает запросы по путям.
type stallHost struct {
	*httptest.Server
	up   atomic.Bool
	mu   sync.Mutex
	hits map[string]int
}

func newStallHost(t *testing.T) *stallHost {
	pic := pngBytes(t)
	hang := make(chan struct{})
	h := &stallHost{hits: map[string]int{}}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.hits[r.URL.Path]++
		h.mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/slow/"):
			select {
			case <-hang:
			case <-r.Context().Done():
			}
			return
		case strings.HasPrefix(r.URL.Path, "/down/") && !h.up.Load():
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write(pic)
	}))
	t.Cleanup(func() { close(hang); h.Close() })
	return h
}

func (h *stallHost) Hits(path string) int { h.mu.Lock(); defer h.mu.Unlock(); return h.hits[path] }

func stallCatalog(t *testing.T, h *stallHost, rutor *fakeSource) (*Catalog, *clock) {
	t.Helper()
	im, err := meta.NewImages(meta.ImagesOptions{Dir: t.TempDir(), Rate: 1000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	return newCatalog(t, openDB(t), func(o *Options) { o.Images = im }, rutor)
}

// waitImage — открытая раздача получает картинку за 3 с (экран раздачи спрашивает её, как пульт).
func waitImage(t *testing.T, c *Catalog, id int64, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if r, err := c.Release(ctx, id); err == nil && r.ImageKey != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Постер открытой раздачи — на своих местах (спека 11b, 14.2): постеры показанной сетки на зависшем
// хостинге его не держат (вживую 2026-10-01: открытая раздача ждала постеры всей сетки).
func TestOpenedPosterNotBehindShown(t *testing.T) {
	h := newStallHost(t)
	rutor := newFake("rutor")
	var rels []source.Release
	for i := range 7 {
		rels = append(rels, rel("rutor", fmt.Sprint(100+i), fmt.Sprintf("Ф%d (2020) WEB-DL", i), 5, 1, fmt.Sprintf("h%d", i)))
	}
	rutor.details["106"] = source.Details{Release: rels[6], PosterURL: h.URL + "/fast.jpg"}
	c, _ := stallCatalog(t, h, rutor)
	ids, err := c.st.saveFound(ctx, rels, c.now())
	if err != nil {
		t.Fatal(err)
	}
	bctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	for i, id := range ids[:6] {
		c.posterLater(bctx, id, fmt.Sprintf("%s/slow/%d.jpg", h.URL, i), 0, posterSoon)
	}
	if _, err := c.Release(ctx, ids[6]); err != nil { // открыли: страница — вне очереди первой
		t.Fatal(err)
	}
	if did, err := c.enrichStep(ctx, "rutor"); !did || err != nil {
		t.Fatal(did, err)
	}
	waitImage(t, c, ids[6], "постер открытой раздачи ждёт постеры показанной сетки")
	cancel()
	c.posterWG.Wait()
}

// Открыли раздачу со страницей без картинки (постер не скачался раньше) — постер сразу (спека 11b, 14.2), а не
// когда кончится фоновый проход повторов: тот висит на мёртвом хостинге до минуты на постер.
func TestOpenedPosterNotBehindRetryPass(t *testing.T) {
	h := newStallHost(t)
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{rel("rutor", "1", "Икс (2020) WEB-DL", 9, 1, "x"), rel("rutor", "2", "Игрек (2020) WEB-DL", 5, 1, "y")}
	rutor.details["1"] = source.Details{Release: source.Release{Title: "Икс (2020) WEB-DL"}, PosterURL: h.URL + "/slow/x.jpg"}
	rutor.details["2"] = source.Details{Release: source.Release{Title: "Игрек (2020) WEB-DL"}, PosterURL: h.URL + "/down/y.jpg"}
	c, clk := stallCatalog(t, h, rutor)
	refresh(t, c, true)
	bctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	for range 2 {
		if did, err := c.enrichStep(bctx, "rutor"); !did || err != nil {
			t.Fatal(did, err)
		}
	}
	y := list(t, c, ListOptions{})[1].ID
	deadline := time.Now().Add(3 * time.Second)
	for h.Hits("/down/y.jpg") == 0 || !func() bool { c.mu.Lock(); defer c.mu.Unlock(); _, ok := c.retries["poster:"+fmt.Sprint(y)]; return ok }() {
		if time.Now().After(deadline) {
			t.Fatal("постер Игрека не пробовали")
		}
		time.Sleep(10 * time.Millisecond)
	}
	clk.add(time.Hour) // пауза повтора прошла: проход повторов возьмётся за обе раздачи, первой — Икс
	go c.fixPosters(bctx)
	for h.Hits("/slow/x.jpg") < 2 {
		if time.Now().After(deadline.Add(2 * time.Second)) {
			t.Fatal("проход повторов не дошёл до Икса")
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.up.Store(true)
	waitImage(t, c, y, "открытая раздача без картинки ждёт фоновый проход повторов")
	cancel()
	c.posterWG.Wait()
}

// Экран раздачи спрашивает её каждую секунду: постер открытой раздачи без картинки пробуется не чаще раза в
// минуту (мёртвый хостинг — не шторм запросов; спека 11b, 14.2).
func TestOpenedPosterOncePerMinute(t *testing.T) {
	h := newStallHost(t)
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{rel("rutor", "2", "Игрек (2020) WEB-DL", 5, 1, "y")}
	rutor.details["2"] = source.Details{Release: source.Release{Title: "Игрек (2020) WEB-DL"}, PosterURL: h.URL + "/down/y.jpg"}
	c, clk := stallCatalog(t, h, rutor)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	y := list(t, c, ListOptions{})[0].ID
	if n := h.Hits("/down/y.jpg"); n != 1 {
		t.Fatalf("догрузка: попыток %d", n)
	}
	open := func() {
		t.Helper()
		for range 5 {
			if _, err := c.Release(ctx, y); err != nil {
				t.Fatal(err)
			}
			c.posterWG.Wait()
		}
	}
	open()
	if n := h.Hits("/down/y.jpg"); n != 2 {
		t.Fatalf("открыли 5 раз: попыток %d, нужна одна", n-1)
	}
	clk.add(openPosterEvery + time.Second)
	open()
	if n := h.Hits("/down/y.jpg"); n != 3 {
		t.Fatalf("через минуту: попыток %d, нужна ещё одна", n-2)
	}
}

// Открыли раздачу без страницы: постер пробует шаг догрузки, а экран раздачи, пока постера нет, второй
// попытки не начинает — одна попытка в минуту на открытую раздачу (спека 11b, 14.2).
func TestOpenedPosterOnceWithPage(t *testing.T) {
	h := newStallHost(t)
	rutor := newFake("rutor")
	found := rel("rutor", "2", "Игрек (2020) WEB-DL", 5, 1, "y")
	rutor.details["2"] = source.Details{Release: found, PosterURL: h.URL + "/down/y.jpg"}
	c, _ := stallCatalog(t, h, rutor)
	ids, err := c.st.saveFound(ctx, []source.Release{found}, c.now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Release(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if did, err := c.enrichStep(ctx, "rutor"); !did || err != nil {
		t.Fatal(did, err)
	}
	for range 3 {
		if _, err := c.Release(ctx, ids[0]); err != nil {
			t.Fatal(err)
		}
		c.posterWG.Wait()
	}
	if n := h.Hits("/down/y.jpg"); n != 1 {
		t.Fatalf("попыток постера %d, нужна одна", n)
	}
}
