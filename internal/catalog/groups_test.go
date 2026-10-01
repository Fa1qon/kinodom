package catalog

import (
	"slices"
	"strings"
	"testing"

	"kinodom/internal/source"
)

// groupsTree — часть дерева Rutracker (testdata rutrackertest, 2026-09-28): группы c2, c18, c20, подразделы
// первого уровня и подфорумы, в том числе служебные.
func groupsTree() []source.Category {
	c := func(id, name, parent string) source.Category {
		return source.Category{ID: id, Name: name, ParentID: parent}
	}
	return []source.Category{
		c("c2", "Кино, Видео и ТВ", ""), c("c18", "Сериалы", ""), c("c20", "Документалистика и юмор", ""), c("c9", "Спорт", ""),
		c("756", `Предложения по улучшению категории "Кино, Видео и ТВ"`, "c2"),
		c("214", "Кино, Видео и TV - помощь по разделу", "c2"), c("1253", "Архив (Кино, Видео и TV - помощь по разделу)", "214"),
		c("22", "Наше кино", "c2"), c("65", "Ищу (Наше кино) + Тематические ссылки", "22"), c("941", "Кино СССР", "22"), c("267", "Архив (Наше кино)", "22"),
		c("7", "Зарубежное кино", "c2"), c("252", "Фильмы 2026", "7"), c("1950", "Фильмы 2021-2025", "7"),
		c("185", "Звуковые дорожки и Переводы", "7"), c("44", "Поиск и обсуждение фильмов", "7"), c("69", "Архив (Зарубежное кино)", "7"),
		c("2198", "HD Video", "c2"), c("653", "Анонсы (U)HD Video", "2198"), c("313", "Зарубежное кино (HD Video)", "2198"),
		c("917", `Предложения по улучшению категории "Сериалы"`, "c18"),
		c("9", "Русские сериалы", "c18"), c("81", "Русские сериалы 2026", "9"), c("26", "Ищу (Русские сериалы)", "9"), c("32", "Обсуждение (Русские сериалы)", "9"),
		c("189", "Зарубежные сериалы", "c18"), c("842", "Новинки и сериалы в стадии показа", "189"), c("1147", "Обсуждение сериалов", "189"),
		c("1629", `Предложения по улучшению категории "Документалистика и юмор"`, "c20"),
		c("19", "СМИ", "c20"),
		c("46", "Документальные фильмы и телепередачи", "c20"), c("2076", "[Док] Космос", "46"), c("56", "[Док] Природа", "46"),
		c("2172", "-= Правила, инструкции, FAQ'и =-", "46"), c("77", "Ищу / Предлагаю / Анонсы ТВ", "46"),
		c("314", "Документальные (HD Video)", "c20"), c("2110", "[HD] Природа", "314"), c("2164", "[HD] Космос", "314"),
		c("50", "Футбол", "c9"), c("51", "Футбол 2026", "50"),
	}
}

func catIDs(cs []source.Category) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return strings.Join(out, ",")
}

// Подразделы первого уровня трёх групп, без служебных (спека 11b, 7.1).
func TestFirstLevel(t *testing.T) {
	got := firstLevel(groupsTree())
	want := map[string]string{"c2": "22,7,2198", "c18": "9,189", "c20": "19,46,314"}
	if len(got) != len(want) {
		t.Fatalf("группы: %v", got)
	}
	for g, ids := range want {
		if catIDs(got[g]) != ids {
			t.Errorf("группа %s: %s, нужно %s", g, catIDs(got[g]), ids)
		}
	}
	if groupOf(groupsTree(), "189") != "c18" || groupOf(groupsTree(), "2076") != "" || groupOf(groupsTree(), "50") != "" {
		t.Error("группа подраздела")
	}
}

// Форумы подраздела: он сам и его видеоподфорумы; «Звуковые дорожки», «Поиск и обсуждение», «Ищу», «Анонсы»,
// «Правила» — нет; архив — да (там раздачи); СМИ без подфорумов — сам (Review Focus 4).
func TestVideoForums(t *testing.T) {
	cases := map[string]string{
		"7":    "7,252,1950,69",
		"22":   "22,941,267",
		"9":    "9,81",
		"46":   "46,2076,56",
		"2198": "2198,313",
		"19":   "19",
		"214":  "",
	}
	for id, want := range cases {
		if got := strings.Join(videoForums(groupsTree(), id), ","); got != want {
			t.Errorf("подраздел %s: %q, нужно %q", id, got, want)
		}
	}
	for _, name := range []string{"Звуковые дорожки и Переводы", "Поиск и обсуждение фильмов", "Ищу / Предлагаю (Театр)", "Анонсы (U)HD Video", "-= Правила, инструкции, FAQ'и =-", "Кино, Видео и TV - помощь по разделу",
		"Тематические подборки ссылок"} { // подборки — вживую 11b-Г: раздач нет, API отвечает 404
		if !serviceForum(name) {
			t.Errorf("служебный: %q", name)
		}
	}
	for _, name := range []string{"Архив (Зарубежное кино)", "Фильмы 2026", "СМИ", "[Док] Космос"} {
		if serviceForum(name) {
			t.Errorf("видео: %q", name)
		}
	}
	if !slices.Equal(RutrackerGroups, []Group{{"c2", "Кино"}, {"c18", "Сериалы"}, {"c20", "Документалистика"}}) {
		t.Errorf("группы: %v", RutrackerGroups)
	}
}

// План 14В: сериал или фильм — куда качать (папка «Сериалов» или «Фильмов»): сезон или серии в названии,
// раздел сериалов Rutor (4, 16), форум группы Rutracker «Сериалы» (c18) на любой глубине.
func TestIsSeries(t *testing.T) {
	rt := newFake("rutracker")
	rt.tree = []source.Category{
		{ID: "c2", Name: "Кино, Видео и ТВ"}, {ID: "7", Name: "Зарубежное кино", ParentID: "c2"}, {ID: "313", Name: "HD", ParentID: "7"},
		{ID: "c18", Name: "Сериалы"}, {ID: "189", Name: "Зарубежные сериалы", ParentID: "c18"}, {ID: "2100", Name: "HD", ParentID: "189"},
	}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Sections = []Section{{"rutracker", "7", false}} }, rt, newFake("rutor"))
	refresh(t, c, false) // дерево
	cases := []struct {
		e    Entry
		want bool
	}{
		{Entry{Tracker: "rutor", CategoryID: "1", Title: "Фильм (2026) WEB-DL 1080p"}, false},
		{Entry{Tracker: "rutor", CategoryID: "4", Title: "Фильм (2026) WEB-DL 1080p"}, true},
		{Entry{Tracker: "rutor", CategoryID: "16", Title: "Наш сериал (2026)"}, true},
		{Entry{Tracker: "rutor", CategoryID: "1", Title: "Сериал / Show [S01] (2026) WEB-DL"}, true},
		{Entry{Tracker: "rutor", CategoryID: "12", Title: "Nat Geo Wild: Дикая Япония [01-02 из 02] (2021) HDTV"}, true},
		{Entry{Tracker: "rutracker", CategoryID: "313", Title: "Фильм (2026) WEB-DL"}, false},
		{Entry{Tracker: "rutracker", CategoryID: "2100", Title: "Шоу / Show (2026) WEB-DL"}, true},
	}
	for _, x := range cases {
		if got := c.IsSeries(ctx, x.e); got != x.want {
			t.Errorf("%s %s %q: %v", x.e.Tracker, x.e.CategoryID, x.e.Title, got)
		}
	}
}
