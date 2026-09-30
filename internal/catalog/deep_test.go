package catalog

import (
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
