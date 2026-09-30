package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"kinodom/internal/source"
)

func TestParseSections(t *testing.T) {
	got, err := ParseSections(" t2:2110, t2:46+ , t2:c20+,t1:12 ")
	want := []Section{{"t2", "2110", false}, {"t2", "46", true}, {"t2", "c20", true}, {"t1", "12", false}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("%v, %v", got, err)
	}
	if FormatSections(got) != "t2:2110,t2:46+,t2:c20+,t1:12" {
		t.Fatalf("обратно в строку: %s", FormatSections(got))
	}
	if got, _ := ParseSections(""); !slices.Equal(got, DefaultSections) || FormatSections(got) != "t2:7+,t2:22+,t2:9+,t2:189+,t2:46+,t1:12" {
		t.Fatal("пусто — разделы по умолчанию")
	}
	for s, want := range map[string]string{
		"t22110":    "трекер:номер",
		"t2:abc":    "не число",
		"t2:c20":    "нужно t2:c20+",
		"t1:c1+":        "только у t2",
		"t2:2110, ": "трекер:номер",
	} {
		if _, err := ParseSections(s); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: ошибка %v, ждали «%s»", s, err, want)
		}
	}
}

// t2Tree — категория c20 с разделами 46 (подразделы 56, 2076) и 2323 (подраздел 2110).
func t2Tree() []source.Category {
	return []source.Category{
		{ID: "c20", Name: "Документалистика и юмор"},
		{ID: "46", Name: "Документальные фильмы и телепередачи", ParentID: "c20"},
		{ID: "56", Name: "Научно-популярные фильмы", ParentID: "46"},
		{ID: "2076", Name: "Космос", ParentID: "46"},
		{ID: "2323", Name: "Документальные фильмы (HD Video)", ParentID: "c20"},
		{ID: "2110", Name: "Естествознание (HD)", ParentID: "2323"},
	}
}

func ids(cats []CategoryRef) []string {
	var out []string
	for _, c := range cats {
		out = append(out, c.ID)
	}
	return out
}

// Разделы каталога t2 — подразделы первого уровня групп (спека 11b, 7.1): подфорум — его подраздел,
// категория — все её подразделы; повторы убираются; дерева ещё нет — как в настройке.
func TestSectionsExpandByTree(t *testing.T) {
	rt := newFake("t2")
	rt.tree = t2Tree()
	c, _ := newCatalog(t, openDB(t), func(o *Options) {
		o.Sections = []Section{{"t2", "46", true}, {"t2", "2110", false}}
	}, rt)
	if got := ids(c.enabled()); !slices.Equal(got, []string{"46", "2110"}) {
		t.Fatalf("до первого прохода (дерева ещё нет): %v", got)
	}
	refresh(t, c, false)
	if got := ids(c.enabled()); !slices.Equal(got, []string{"46", "2323"}) {
		t.Fatalf("46+ и 2110: %v", got)
	}
	if err := c.SetSections(ctx, []Section{{"t2", "c20", true}}); err != nil {
		t.Fatal(err)
	}
	if got := ids(c.enabled()); !slices.Equal(got, []string{"46", "2323"}) {
		t.Fatalf("c20+: %v", got)
	}
}

// Прежний выбор заказчика — в подразделы первого уровня (Review Focus 1).
func TestNormalizeSections(t *testing.T) {
	tree := groupsTree()
	rt := func(id string, all bool) Section { return Section{"t2", id, all} }
	cases := []struct {
		name string
		in   []Section
		want string
	}{
		{"прежние значения по умолчанию", []Section{rt("2110", false), rt("2164", false), rt("56", false), rt("2076", false), {"t1", "12", false}},
			"t2:314+,t2:46+,t1:12"},
		{"подраздел с «+» и без", []Section{rt("46", true), rt("7", false)}, "t2:46+,t2:7+"},
		{"категория", []Section{rt("c20", true)}, "t2:19+,t2:46+,t2:314+"},
		{"нет в дереве — как есть", []Section{rt("99999", false)}, "t2:99999"},
		{"вне трёх групп — нет", []Section{rt("51", false), rt("c9", true), rt("9", true)}, "t2:9+"},
		{"повторы", []Section{rt("252", false), rt("1950", false), rt("7", true)}, "t2:7+"},
	}
	for _, c := range cases {
		if got := FormatSections(NormalizeSections(c.in, tree)); got != c.want {
			t.Errorf("%s: %s, нужно %s", c.name, got, c.want)
		}
	}
	old := []Section{rt("2110", false), {"t1", "12", false}}
	if got := FormatSections(NormalizeSections(old, nil)); got != "t2:2110,t1:12" {
		t.Errorf("дерева нет — как есть: %s", got)
	}
}

// Разделы из пульта проверяются по дереву; дерева ещё нет — проверять не по чему.
func TestCheckSections(t *testing.T) {
	rt := newFake("t2")
	rt.tree = t2Tree()
	c, _ := newCatalog(t, openDB(t), nil, rt)
	if err := c.CheckSections(ctx, []Section{{"t2", "99999", false}}); err != nil {
		t.Fatalf("дерева ещё нет, а отказ: %v", err)
	}
	refresh(t, c, false)
	if err := c.CheckSections(ctx, []Section{{"t2", "46", true}, {"t2", "c20", true}}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckSections(ctx, []Section{{"t2", "99999", false}}); err == nil || !strings.Contains(err.Error(), "99999") {
		t.Fatalf("раздела нет в дереве: %v", err)
	}
	if err := c.CheckSections(ctx, []Section{{"tracker3", "1", false}}); err == nil || !strings.Contains(err.Error(), "tracker3") {
		t.Fatalf("незнакомый трекер: %v", err)
	}
	rt.set(func() {
		rt.tree = append(rt.tree, source.Category{ID: "c9", Name: "Спорт"}, source.Category{ID: "50", Name: "Футбол", ParentID: "c9"})
	})
	refresh(t, c, true)
	if err := c.CheckSections(ctx, []Section{{"t2", "50", true}}); err == nil || !strings.Contains(err.Error(), "Кино") {
		t.Fatalf("раздел вне трёх групп: %v", err)
	}
}

// Разделы сменили: раздачи снятого раздела пропадают из каталога сразу, новый раздел обновляется
// в ближайший проход без «Обновить сейчас».
func TestSetSectionsTakesEffectAtOnce(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = many("t1", 3)
	t1.top["1"] = []source.Release{rel("t1", "100", "Фильм (2020) WEB-DL", 500, 1<<30, "ff")}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"t1", "12", false}} }, t1)
	refresh(t, c, false)
	if err := c.SetSections(ctx, []Section{{"t1", "1", false}}); err != nil {
		t.Fatal(err)
	}
	if es := list(t, c, ListOptions{Tracker: "t1"}); len(es) != 0 {
		t.Fatalf("снятый раздел ещё в каталоге: %d", len(es))
	}
	select {
	case <-c.sectionsChanged:
	default:
		t.Fatal("цикл каталога не разбужен")
	}
	refresh(t, c, false) // без force: у нового раздела нет удачного обновления
	if es := list(t, c, ListOptions{Tracker: "t1"}); len(es) != 1 || es[0].TopicID != "100" {
		t.Fatalf("новый раздел: %+v", es)
	}
}

type muxRouter struct{ *http.ServeMux }

func (m muxRouter) Handle(pattern, _ string, h http.Handler) { m.ServeMux.Handle(pattern, h) }

// getJSONErr — как getJSON, но разбирает и ответ с ошибкой.
func getJSONErr(t *testing.T, h http.Handler, url string, v any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatal(err)
	}
	return rec.Code
}

func getJSON(t *testing.T, h http.Handler, url string, v any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	if rec.Code == http.StatusOK && v != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code
}

// Дерево разделов для настроек и разделы вкладки трекера — те, где есть раздачи.
func TestTreeAndSectionsRoutes(t *testing.T) {
	rt := newFake("t2")
	rt.tree = t2Tree()
	rt.top["56"] = many("t2", 2)
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"t2", "46", true}} }, rt)
	refresh(t, c, false)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	var tree []TreeNode
	if code := getJSON(t, mux, "/api/v1/sources/t2/categories", &tree); code != 200 || len(tree) != 6 ||
		tree[2] != (TreeNode{ID: "56", Name: "Научно-популярные фильмы", ParentID: "46"}) {
		t.Fatalf("дерево: %d %+v", code, tree)
	}
	var secs []SectionInfo
	if code := getJSON(t, mux, "/api/v1/catalog/sections?tracker=t2", &secs); code != 200 || len(secs) != 1 ||
		secs[0] != (SectionInfo{ID: "46", Name: "Документальные фильмы и телепередачи", Count: 2, Group: "c20", GroupName: "Документалистика"}) {
		t.Fatalf("разделы вкладки: %d %+v", code, secs)
	}
	if code := getJSON(t, mux, "/api/v1/sources/tracker3/categories", nil); code != http.StatusNotFound {
		t.Fatalf("незнакомый трекер: %d", code)
	}
}

// Разделы вкладки t2 — в порядке групп «Кино · Сериалы · Документалистика» и дерева, с группой
// (спека 11b, 7.1).
func TestSectionsRouteGroups(t *testing.T) {
	rt := newFake("t2")
	rt.tree = groupsTree()
	for i, f := range []string{"252", "81", "56"} { // разные раздачи: одинаковый infohash схлопнулся бы в один раздел
		rt.top[f] = []source.Release{rel("t2", f, "Фильм "+f+" (2020) WEB-DL", 10+i, 1<<30, "h"+f)}
	}
	c, _ := newCatalog(t, openDB(t), func(o *Options) {
		o.Sections = []Section{{"t2", "46", true}, {"t2", "9", true}, {"t2", "7", true}}
	}, rt)
	refresh(t, c, false)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	var secs []SectionInfo
	getJSON(t, mux, "/api/v1/catalog/sections?tracker=t2", &secs)
	var got []string
	for _, s := range secs {
		got = append(got, s.ID+":"+s.Group+":"+s.GroupName)
	}
	if strings.Join(got, " ") != "7:c2:Кино 9:c18:Сериалы 46:c20:Документалистика" {
		t.Fatalf("разделы: %v", got)
	}
}
