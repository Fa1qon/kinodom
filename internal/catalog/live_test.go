package catalog

import (
	"fmt"
	"slices"
	"testing"
)

// Фоновая догрузка — по месту в разделе (спека 11b, 14.4; № 18): первые карточки всех разделов, потом
// вторые. Раньше шла по раздающим через все разделы — первый экран раздела, где раздающих меньше
// («Мультфильмы», документальные), ждал всю глубину популярных.
func TestEnrichFirstScreensFirst(t *testing.T) {
	t1 := newFake("t1")
	for i := range 3 {
		t1.top["12"] = append(t1.top["12"], rel("t1", fmt.Sprintf("a%d", i), fmt.Sprintf("Кино %d (2020) WEB-DL", i), 100-i, 1<<30, fmt.Sprintf("ha%d", i)))
		t1.top["1"] = append(t1.top["1"], rel("t1", fmt.Sprintf("b%d", i), fmt.Sprintf("Мульт %d (2020) WEB-DL", i), 10-i, 1<<30, fmt.Sprintf("hb%d", i)))
	}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"t1", "12", false}, {"t1", "1", false}} }, t1)
	refresh(t, c, true)
	var order []string
	for range 6 {
		before := map[string]int{}
		for _, id := range []string{"a0", "a1", "a2", "b0", "b1", "b2"} {
			before[id] = t1.Calls("details:" + id)
		}
		if did, err := c.enrichStep(ctx, "t1"); !did || err != nil {
			t.Fatal(did, err)
		}
		for id, n := range before {
			if t1.Calls("details:"+id) > n {
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
	c, _, mux := t1Section(t, manyDesc("t1", 60))
	v, code := listAfter(t, mux, "t1", "12", -1)
	if code != 200 || len(v.Entries) != PageSize {
		t.Fatalf("порция: %d, карточек %d", code, len(v.Entries))
	}
	c.mu.Lock()
	found := slices.Clone(c.found["t1"])
	c.mu.Unlock()
	var shown []int64
	for _, e := range v.Entries {
		shown = append(shown, e.ID)
	}
	if !slices.Equal(found[:min(len(found), PageSize)], shown) {
		t.Fatalf("вне очереди %d карточек %v, на экране %v", len(found), found, shown)
	}
}
