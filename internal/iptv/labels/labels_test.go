package labels

import (
	"reflect"
	"strings"
	"testing"
)

// Вырезки из channels.json и feeds.json iptv-org (поля — как в настоящих файлах).
const channelsJSON = `[
{"id":"Perviykanal.ru","name":"Perviy kanal","alt_names":["Первый канал","1TV"],"network":null,"owners":[],"country":"RU","categories":["general"],"is_nsfw":false,"launched":null,"closed":null,"replaced_by":null,"website":null},
{"id":"MatchTV.ru","name":"Match TV","alt_names":["Матч ТВ"],"country":"RU","categories":["sports","general"],"is_nsfw":false},
{"id":"RossiyaK.ru","name":"Rossiya K","alt_names":["Россия К","Культура"],"country":"RU","categories":["culture"],"is_nsfw":false},
{"id":"Rossiya24.ru","name":"Rossiya 24","alt_names":["Россия 24"],"country":"RU","categories":["general","news"],"is_nsfw":false},
{"id":"Karusel.ru","name":"Karusel","alt_names":["Карусель"],"country":"RU","categories":["kids","entertainment"],"is_nsfw":false},
{"id":"STS.ru","name":"STS","alt_names":["СТС"],"country":"RU","categories":["family"],"is_nsfw":false},
{"id":"Blue.ru","name":"Blue Hustler","alt_names":[],"country":"RU","categories":[],"is_nsfw":true},
{"id":"BBCNews.uk","name":"BBC News","alt_names":[],"country":"UK","categories":["news"],"is_nsfw":false},
{"id":"AlJazeera.qa","name":"Al Jazeera","alt_names":["الجزيرة"],"country":"QA","categories":["news"],"is_nsfw":false},
{"id":"NoCat.ru","name":"No Cat","alt_names":["Безымянный"],"country":"RU","categories":[],"is_nsfw":false},
{"id":"Twin1.ru","name":"Twin","alt_names":["Близнец"],"country":"RU","categories":["music"],"is_nsfw":false},
{"id":"Twin2.by","name":"Twin","alt_names":["Близнец"],"country":"BY","categories":["music"],"is_nsfw":false}
]`

const feedsJSON = `[
{"channel":"Perviykanal.ru","id":"SD","name":"SD","alt_names":[],"is_main":true,"broadcast_area":["c/RU"],"timezones":["Europe/Moscow"],"languages":["rus"],"format":"576i"},
{"channel":"Perviykanal.ru","id":"Plus4","name":"+4","is_main":false,"languages":["tat"]},
{"channel":"AlJazeera.qa","id":"SD","is_main":true,"languages":["ara","eng"]},
{"channel":"BBCNews.uk","id":"HD","is_main":true,"languages":["eng"]},
{"channel":"MatchTV.ru","id":"HD","is_main":true,"languages":["rus"]}
]`

func base(t *testing.T) *Base {
	t.Helper()
	b, err := LoadBase(strings.NewReader(channelsJSON), strings.NewReader(feedsJSON))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Канал iptv-org находится по id из tvg-id потоков, иначе по названию, если он такой один.
func TestFind(t *testing.T) {
	b := base(t)
	cases := []struct {
		ids   []string
		names []string
		want  string
	}{
		{[]string{"MatchTV.ru"}, nil, "MatchTV.ru"},
		{[]string{"MatchTV.ru", "MatchTV.ru"}, []string{"Первый канал"}, "MatchTV.ru"},
		{nil, []string{"Первый канал HD"}, "Perviykanal.ru"},
		{nil, []string{"культура"}, "RossiyaK.ru"},
		{nil, []string{"Близнец"}, ""}, // два канала с таким названием
		{[]string{"MatchTV.ru", "Karusel.ru"}, nil, ""},
		{[]string{"MatchTV.ru", "Karusel.ru", "MatchTV.ru"}, []string{"Карусель"}, "MatchTV.ru"}, // большинство
		{[]string{"MatchTV.ru", "Karusel.ru"}, []string{"Карусель"}, "Karusel.ru"},               // поровну — по названию
		{[]string{"Нет.такого"}, []string{"Карусель"}, "Karusel.ru"},
		{nil, []string{"Неизвестный"}, ""},
	}
	for _, c := range cases {
		got := ""
		if ch := b.Find(c.ids, c.names); ch != nil {
			got = ch.ID
		}
		if got != c.want {
			t.Errorf("Find(%v, %v) = %q, нужно %q", c.ids, c.names, got, c.want)
		}
	}
}

// Категории iptv-org → наша, по старшинству; is_nsfw — 18+; UK → GB; языки — основной ленты.
func TestFromOrg(t *testing.T) {
	b := base(t)
	cases := map[string]Source{
		"MatchTV.ru":     {Category: "sports", Country: "RU", Languages: []string{"rus"}},
		"RossiyaK.ru":    {Category: "science", Country: "RU"},
		"Rossiya24.ru":   {Category: "news", Country: "RU"},
		"Karusel.ru":     {Category: "kids", Country: "RU"},
		"STS.ru":         {Category: "entertainment", Country: "RU"},
		"Blue.ru":        {Category: "adult", Country: "RU"},
		"BBCNews.uk":     {Category: "news", Country: "GB", Languages: []string{"eng"}},
		"AlJazeera.qa":   {Category: "news", Country: "QA", Languages: []string{"ara", "eng"}},
		"Perviykanal.ru": {Category: "general", Country: "RU", Languages: []string{"rus"}},
		"NoCat.ru":       {Country: "RU"},
	}
	for id, want := range cases {
		if got := FromOrg(b.ByID(id)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, нужно %+v", id, got, want)
		}
	}
}

// Запасной путь: страна по суффиксу id телепрограммы и пометке в названии; категория и страна по
// словам групп — самая частая; флажки в группах не считаются.
func TestFallbacks(t *testing.T) {
	cases := []struct {
		id    string
		names []string
		want  string
	}{
		{"tet-ua", nil, "UA"},
		{"city-tv-bg", nil, "BG"},
		{"btvmd", nil, ""},
		{"match-tv", nil, ""}, // «-tv» — не страна
		{"pervy-pl4", nil, ""},
		{"rtl2", []string{"RTL 2 [HR]"}, "HR"},
		{"x", []string{"Канал [Not 24/7]"}, ""},
	}
	for _, c := range cases {
		if got := FromID(c.id, c.names).Country; got != c.want {
			t.Errorf("FromID(%s, %v) — страна %q, нужно %q", c.id, c.names, got, c.want)
		}
	}
	groups := []struct {
		in   map[string]int
		want Source
	}{
		{map[string]int{"Спорт": 3, "Общие": 1}, Source{Category: "sports"}},
		{map[string]int{"Кино и сериалы": 1}, Source{Category: "movies"}},
		{map[string]int{"Познавательные": 2}, Source{Category: "science"}},
		{map[string]int{"Местные": 5}, Source{Country: "RU"}},
		{map[string]int{"Украина": 2, "News": 1}, Source{Category: "news", Country: "UA"}},
		{map[string]int{"Wink (VPN 🇷🇺)": 9}, Source{}},
		{map[string]int{"StarNet (VPN 🇳🇱)": 9}, Source{}},
		{map[string]int{"Детям": 1, "Мультфильмы": 1, "Кино": 3}, Source{Category: "movies"}},
		{map[string]int{"18+": 1}, Source{Category: "adult"}},
		{map[string]int{"Развлечение": 1}, Source{Category: "entertainment"}},
	}
	for _, c := range groups {
		if got := FromGroups(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("FromGroups(%v) = %+v, нужно %+v", c.in, got, c.want)
		}
	}
}

// Метка — из первого источника, где она есть; страна Россия без языка — русский.
func TestMerge(t *testing.T) {
	got := Merge(Source{Country: "RU"}, Source{Category: "sports", Country: "UA"}, Source{Languages: []string{"ukr"}})
	want := Labels{Category: "sports", Country: "RU", Languages: []string{"ukr"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Merge = %+v, нужно %+v", got, want)
	}
	if got := Merge(Source{Country: "RU"}); !reflect.DeepEqual(got.Languages, []string{"rus"}) {
		t.Errorf("Россия без языка: %v", got.Languages)
	}
	if got := Merge(Source{Country: "KZ"}); len(got.Languages) != 0 {
		t.Errorf("Казахстан без языка: %v", got.Languages)
	}
	if got := Merge(); got.Category != "" || got.Country != "" || len(got.Languages) != 0 || got.Languages == nil {
		t.Errorf("пусто: %+v (языки — пустой список, не null)", got)
	}
}

func TestNames(t *testing.T) {
	checks := [][2]string{
		{CategoryName("science"), "Познавательные"}, {CategoryName(""), "Без категории"}, {CategoryName("adult"), "18+"},
		{CountryName("RU"), "Россия"}, {CountryName("UA"), "Украина"}, {CountryName("GB"), "Великобритания"},
		{CountryName("US"), "США"}, {CountryName(""), "Страна не указана"}, {CountryName("ZZ"), "ZZ"},
		{LanguageName("rus"), "русский"}, {LanguageName("ara"), "арабский"}, {LanguageName("kaz"), "казахский"},
		{LanguageName(""), "Язык не указан"},
	}
	for _, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%q, нужно %q", c[0], c[1])
		}
	}
	if len(CategoryOrder) != 11 || CategoryOrder[0] != "movies" || CategoryOrder[10] != "" {
		t.Errorf("порядок категорий: %v", CategoryOrder)
	}
}
