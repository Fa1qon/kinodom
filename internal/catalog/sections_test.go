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
	got, err := ParseSections(" rutracker:2110, rutracker:46+ , rutracker:c20+,rutor:12 ")
	want := []Section{{"rutracker", "2110", false}, {"rutracker", "46", true}, {"rutracker", "c20", true}, {"rutor", "12", false}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("%v, %v", got, err)
	}
	if FormatSections(got) != "rutracker:2110,rutracker:46+,rutracker:c20+,rutor:12" {
		t.Fatalf("обратно в строку: %s", FormatSections(got))
	}
	if got, _ := ParseSections(""); !slices.Equal(got, DefaultSections) || FormatSections(got) != "rutracker:7+,rutracker:22+,rutracker:9+,rutracker:189+,rutracker:46+,rutor:12" {
		t.Fatal("пусто — разделы по умолчанию")
	}
	for s, want := range map[string]string{
		"rutracker2110":    "трекер:номер",
		"rutracker:abc":    "не число",
		"rutracker:c20":    "нужно rutracker:c20+",
		"rutor:c1+":        "только у Rutracker",
		"rutracker:2110, ": "трекер:номер",
	} {
		if _, err := ParseSections(s); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: ошибка %v, ждали «%s»", s, err, want)
		}
	}
}

// rutrackerTree — категория c20 с разделами 46 (подразделы 56, 2076) и 2323 (подраздел 2110).
func rutrackerTree() []source.Category {
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

// Разделы каталога Rutracker — подразделы первого уровня групп (спека 11b, 7.1): подфорум — его подраздел,
// категория — все её подразделы; повторы убираются; дерева ещё нет — как в настройке.
func TestSectionsExpandByTree(t *testing.T) {
	rt := newFake("rutracker")
	rt.tree = rutrackerTree()
	c, _ := newCatalog(t, openDB(t), func(o *Options) {
		o.Sections = []Section{{"rutracker", "46", true}, {"rutracker", "2110", false}}
	}, rt)
	if got := ids(c.enabled()); !slices.Equal(got, []string{"46", "2110"}) {
		t.Fatalf("до первого прохода (дерева ещё нет): %v", got)
	}
	refresh(t, c, false)
	if got := ids(c.enabled()); !slices.Equal(got, []string{"46", "2323"}) {
		t.Fatalf("46+ и 2110: %v", got)
	}
	if err := c.SetSections(ctx, []Section{{"rutracker", "c20", true}}); err != nil {
		t.Fatal(err)
	}
	if got := ids(c.enabled()); !slices.Equal(got, []string{"46", "2323"}) {
		t.Fatalf("c20+: %v", got)
	}
}

// Прежний выбор заказчика — в подразделы первого уровня (Review Focus 1).
func TestNormalizeSections(t *testing.T) {
	tree := groupsTree()
	rt := func(id string, all bool) Section { return Section{"rutracker", id, all} }
	cases := []struct {
		name string
		in   []Section
		want string
	}{
		{"прежние значения по умолчанию", []Section{rt("2110", false), rt("2164", false), rt("56", false), rt("2076", false), {"rutor", "12", false}},
			"rutracker:314+,rutracker:46+,rutor:12"},
		{"подраздел с «+» и без", []Section{rt("46", true), rt("7", false)}, "rutracker:46+,rutracker:7+"},
		{"категория", []Section{rt("c20", true)}, "rutracker:19+,rutracker:46+,rutracker:314+"},
		{"нет в дереве — как есть", []Section{rt("99999", false)}, "rutracker:99999"},
		{"вне трёх групп — нет", []Section{rt("51", false), rt("c9", true), rt("9", true)}, "rutracker:9+"},
		{"повторы", []Section{rt("252", false), rt("1950", false), rt("7", true)}, "rutracker:7+"},
	}
	for _, c := range cases {
		if got := FormatSections(NormalizeSections(c.in, tree)); got != c.want {
			t.Errorf("%s: %s, нужно %s", c.name, got, c.want)
		}
	}
	old := []Section{rt("2110", false), {"rutor", "12", false}}
	if got := FormatSections(NormalizeSections(old, nil)); got != "rutracker:2110,rutor:12" {
		t.Errorf("дерева нет — как есть: %s", got)
	}
}

// Разделы из пульта проверяются по дереву; дерева ещё нет — проверять не по чему.
func TestCheckSections(t *testing.T) {
	rt := newFake("rutracker")
	rt.tree = rutrackerTree()
	c, _ := newCatalog(t, openDB(t), nil, rt)
	if err := c.CheckSections(ctx, []Section{{"rutracker", "99999", false}}); err != nil {
		t.Fatalf("дерева ещё нет, а отказ: %v", err)
	}
	refresh(t, c, false)
	if err := c.CheckSections(ctx, []Section{{"rutracker", "46", true}, {"rutracker", "c20", true}}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckSections(ctx, []Section{{"rutracker", "99999", false}}); err == nil || !strings.Contains(err.Error(), "99999") {
		t.Fatalf("раздела нет в дереве: %v", err)
	}
	if err := c.CheckSections(ctx, []Section{{"kinozal", "1", false}}); err == nil || !strings.Contains(err.Error(), "kinozal") {
		t.Fatalf("незнакомый трекер: %v", err)
	}
	rt.set(func() {
		rt.tree = append(rt.tree, source.Category{ID: "c9", Name: "Спорт"}, source.Category{ID: "50", Name: "Футбол", ParentID: "c9"})
	})
	refresh(t, c, true)
	if err := c.CheckSections(ctx, []Section{{"rutracker", "50", true}}); err == nil || !strings.Contains(err.Error(), "Кино") {
		t.Fatalf("раздел вне трёх групп: %v", err)
	}
}

// Разделы сменили: раздачи снятого раздела пропадают из каталога сразу, новый раздел обновляется
// в ближайший проход без «Обновить сейчас».
func TestSetSectionsTakesEffectAtOnce(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = many("rutor", 3)
	rutor.top["1"] = []source.Release{rel("rutor", "100", "Фильм (2020) WEB-DL", 500, 1<<30, "ff")}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutor", "12", false}} }, rutor)
	refresh(t, c, false)
	if err := c.SetSections(ctx, []Section{{"rutor", "1", false}}); err != nil {
		t.Fatal(err)
	}
	if es := list(t, c, ListOptions{Tracker: "rutor"}); len(es) != 0 {
		t.Fatalf("снятый раздел ещё в каталоге: %d", len(es))
	}
	select {
	case <-c.sectionsChanged:
	default:
		t.Fatal("цикл каталога не разбужен")
	}
	refresh(t, c, false) // без force: у нового раздела нет удачного обновления
	if es := list(t, c, ListOptions{Tracker: "rutor"}); len(es) != 1 || es[0].TopicID != "100" {
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
	rt := newFake("rutracker")
	rt.tree = rutrackerTree()
	rt.top["56"] = many("rutracker", 2)
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutracker", "46", true}} }, rt)
	refresh(t, c, false)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	var tree []TreeNode
	if code := getJSON(t, mux, "/api/v1/sources/rutracker/categories", &tree); code != 200 || len(tree) != 6 ||
		tree[2] != (TreeNode{ID: "56", Name: "Научно-популярные фильмы", ParentID: "46"}) {
		t.Fatalf("дерево: %d %+v", code, tree)
	}
	var secs []SectionInfo
	if code := getJSON(t, mux, "/api/v1/catalog/sections?tracker=rutracker", &secs); code != 200 || len(secs) != 1 ||
		secs[0] != (SectionInfo{ID: "46", Name: "Документальные фильмы и телепередачи", Count: 2, Group: "c20", GroupName: "Документалистика"}) {
		t.Fatalf("разделы вкладки: %d %+v", code, secs)
	}
	if code := getJSON(t, mux, "/api/v1/sources/kinozal/categories", nil); code != http.StatusNotFound {
		t.Fatalf("незнакомый трекер: %d", code)
	}
}

// Разделы вкладки Rutracker — в порядке групп «Кино · Сериалы · Документалистика» и дерева, с группой
// (спека 11b, 7.1).
func TestSectionsRouteGroups(t *testing.T) {
	rt := newFake("rutracker")
	rt.tree = groupsTree()
	for i, f := range []string{"252", "81", "56"} { // разные раздачи: одинаковый infohash схлопнулся бы в один раздел
		rt.top[f] = []source.Release{rel("rutracker", f, "Фильм "+f+" (2020) WEB-DL", 10+i, 1<<30, "h"+f)}
	}
	c, _ := newCatalog(t, openDB(t), func(o *Options) {
		o.Sections = []Section{{"rutracker", "46", true}, {"rutracker", "9", true}, {"rutracker", "7", true}}
	}, rt)
	refresh(t, c, false)
	refresh(t, c, true)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	var secs []SectionInfo
	getJSON(t, mux, "/api/v1/catalog/sections?tracker=rutracker", &secs)
	var got []string
	for _, s := range secs {
		got = append(got, s.ID+":"+s.Group+":"+s.GroupName)
	}
	if strings.Join(got, " ") != "7:c2:Кино 9:c18:Сериалы 46:c20:Документалистика" {
		t.Fatalf("разделы: %v", got)
	}
}

// Экран «Разделы каталога» (спека 11b, 7.1): ?level=1 — три группы Rutracker и их подразделы первого
// уровня без служебных, в порядке групп и дерева; Rutor — как есть.
func TestTreeLevelOne(t *testing.T) {
	rt := newFake("rutracker")
	rt.tree = groupsTree()
	c, _ := newCatalog(t, openDB(t), nil, rt)
	refresh(t, c, false)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	var nodes []TreeNode
	if code := getJSON(t, mux, "/api/v1/sources/rutracker/categories?level=1", &nodes); code != 200 {
		t.Fatalf("код %d", code)
	}
	var got []string
	for _, n := range nodes {
		got = append(got, n.ID+"<"+n.ParentID)
	}
	want := "c2< 22<c2 7<c2 2198<c2 c18< 9<c18 189<c18 c20< 19<c20 46<c20 314<c20"
	if strings.Join(got, " ") != want {
		t.Fatalf("узлы %q\nнужно %q", strings.Join(got, " "), want)
	}
	if nodes[0].Name != "Кино" || nodes[7].Name != "Документалистика" {
		t.Fatalf("группы — названиями пульта: %q, %q", nodes[0].Name, nodes[7].Name)
	}
}
