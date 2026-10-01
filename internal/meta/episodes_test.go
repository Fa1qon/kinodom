package meta

import "testing"

// Серии из названия (Х2): сколько вышло и сколько всего — для подписки на новые серии (спека 11b, 6.2).
func TestEpisodes(t *testing.T) {
	cases := []struct {
		title           string
		from, to, total int
	}{
		{"Холод [01-07 из 08] (2026) WEB-DL 1080p от Files-x", 1, 7, 8},
		{"Удар / La frappe [S01] [1x01-06 из 08] (2026) WEBRip", 1, 6, 8},
		{"Трудно быть богом (Ким Дружинин) [2026, драма, WEB-DL 1080p] (Сезон: 1, Серии: 1-8 из 10)", 1, 8, 10},
		{"Законник (Сезон 1, Серии 1-4) [2023, детектив, WEB-DL]", 1, 4, 0},
		{"Озеро (Серия 5 из 10) [2025, драма, WEBRip]", 5, 5, 10},
		{"Чужой: Земля / Alien: Earth [S01E01-07] (2025) WEB-DL", 1, 7, 0},
		{"Сериал [7 серий из 8] (2026)", 1, 7, 8},
		{"Динозавры / The Dinosaurs [01-04 из 04] (2026) WEB-DL 720p", 1, 4, 4},
		{"Законник [S01] (2023) WEB-DL 1080p", 0, 0, 0},
		{"Одиссея / The Odyssey (Кристофер Нолан) [2026, США, Приключения, Telesync]", 0, 0, 0},
		{"Фильмы 2021-2025 из коллекции (2025)", 0, 0, 0},
	}
	for _, c := range cases {
		from, to, total := Episodes(c.title)
		if from != c.from || to != c.to || total != c.total {
			t.Errorf("%q: %d-%d из %d, нужно %d-%d из %d", c.title, from, to, total, c.from, c.to, c.total)
		}
	}
	if SeasonNumber("Сезон: 2, Серии: 1-8 из 10") != 2 || SeasonNumber("S03") != 3 {
		t.Error("SeasonNumber")
	}
}
