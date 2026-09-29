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
	if got, _ := ParseSections(""); !slices.Equal(got, DefaultSections) || FormatSections(got) != FormatCategories(DefaultCategories) {
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

// Раздел с «+» раскрывается по дереву, в том числе подразделом, появившимся позже; категория — во
// все свои разделы; повторы убираются (спека этапа 7, раздел 5.4).
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
	if got := ids(c.enabled()); !slices.Equal(got, []string{"46", "56", "2076", "2110"}) {
		t.Fatalf("46+ и 2110: %v", got)
	}
	rt.set(func() {
		rt.tree = append(rt.tree, source.Category{ID: "999", Name: "Новый подраздел", ParentID: "46"})
	})
	refresh(t, c, false)
	if got := ids(c.enabled()); !slices.Equal(got, []string{"46", "56", "2076", "999", "2110"}) {
		t.Fatalf("новый подраздел не вошёл: %v", got)
	}
	if err := c.SetSections(ctx, []Section{{"rutracker", "c20", true}}); err != nil {
		t.Fatal(err)
	}
	if got := ids(c.enabled()); !slices.Equal(got, []string{"46", "56", "2076", "999", "2323", "2110"}) {
		t.Fatalf("c20+: %v", got)
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
		secs[0] != (SectionInfo{ID: "56", Name: "Научно-популярные фильмы", Count: 2}) {
		t.Fatalf("разделы вкладки: %d %+v", code, secs)
	}
	if code := getJSON(t, mux, "/api/v1/sources/kinozal/categories", nil); code != http.StatusNotFound {
		t.Fatalf("незнакомый трекер: %d", code)
	}
}
