package catalog

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

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
