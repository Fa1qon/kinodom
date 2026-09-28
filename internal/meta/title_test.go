package meta

import (
	"slices"
	"testing"
)

// Настоящие названия из образцов обоих трекеров (testdata этапов 3–4).
func TestParseTitle(t *testing.T) {
	cases := []struct {
		in       string
		ru, orig string
		year     int
		quality  string
	}{
		// Rutracker
		{"Космос: Персональное путешествие с Карлом Саганом / Cosmos: A Personal Voyage (Carl Sagan) [1980, научно-популярный, DVDRip-AVC, RUS/ENG]",
			"Космос: Персональное путешествие с Карлом Саганом", "Cosmos: A Personal Voyage", 1980, "DVDRip-AVC"},
		{"Вселенная (1-5 серий из 5) / Universe (Poppy Pinnock / Поппи Пиннок) [2021, Документальный, космос, DVB] DVO (СВ-Дубль) + Sub Rus + Localized version",
			"Вселенная", "Universe", 2021, "DVB"},
		{"BBC. Планеты / The Planets (1-5 серии из 5) (Гидеон Брэдшоу / Gideon Bradshaw) [2019, Документальный, космос, HDTVRip]",
			"BBC. Планеты", "The Planets", 2019, "HDTVRip"},
		{"BBC: Битва за космос \\ Space Race (Марк Эверест, Кристофер Спенсер) [2005, Документалистика, DVDRip]",
			"BBC: Битва за космос", "Space Race", 2005, "DVDRip"},
		{"Дорога в космос (Серии 1-15 из 15) [2021, Исторический, IPTVRip]", "Дорога в космос", "", 2021, "IPTVRip"},
		{"Космос. Первая кровь (Д. Грачев) [2006, документалный фильм]", "Космос. Первая кровь", "", 2006, ""},
		{"Космос: Пространство и время / Cosmos: A SpaceTime Odyssey (Билл Поуп / Bill Pope) / Сезон: 1 / Серии: 1-13 из 13 [2014, Документальный, WEB-DL 1080p]",
			"Космос: Пространство и время", "Cosmos: A SpaceTime Odyssey", 2014, "WEB-DL 1080p"},
		{"[Обновлено] СОУЛМ8ЙТ / Soulm8te (Кейт Долан / Kate Dolan) [2026, США, фантастика, триллер, WEB-DLRip 720p] MVO (WinMedia, TVShows)",
			"СОУЛМ8ЙТ", "Soulm8te", 2026, "WEB-DLRip 720p"},
		{"Зверь / Дикий драйв / La fiera / To the Max (Сальвадор Калво / Salvador Calvo) [2026, Испания, драма, приключения, спорт, WEB-DL 720p] MVO (MUZOBOZ)",
			"Зверь", "La fiera", 2026, "WEB-DL 720p"},
		// Rutor
		{"Динозавры / The Dinosaurs [S01] (2026) WEB-DL 720p от New-Team | P1 | LostFilm, WinMedia", "Динозавры", "The Dinosaurs", 2026, "WEB-DL 720p"},
		{"Расследования авиакатастроф / Mayday / Air Crash Investigation [S01-24] (2003-2024) HDTVRip-AVC, WEBRip-AVC | КПК | P2",
			"Расследования авиакатастроф", "Mayday", 2003, "HDTVRip-AVC, WEBRip-AVC"},
		{"Большой куш. Бангкок [02x13 из 13] [Эфир от 27.09] (2026) HDTV 1080р от Files-x", "Большой куш. Бангкок", "", 2026, "HDTV 1080р"},
		{"Матрица: Трилогия / The Matrix: Trilogy (1999-2003) HD-DVDRip-HEVC 1080p от RIPS CLUB | D, P, P2, A, L2, L1",
			"Матрица: Трилогия", "The Matrix: Trilogy", 1999, "HD-DVDRip-HEVC 1080p"},
		{"Холоп 3 (2026) WEBRip 1080p", "Холоп 3", "", 2026, "WEBRip 1080p"},
		{"1812 [01-04 из 04] (2012) HDTVRip 720p от New-Team", "1812", "", 2012, "HDTVRip 720p"},
		{"Матрица / The Matrix (1999) BDRip от HQCLUB | Лицензия", "Матрица", "The Matrix", 1999, "BDRip"},
		// Без года и скобок — только название.
		{"  Просто   название  ", "Просто название", "", 0, ""},
	}
	for _, c := range cases {
		got := ParseTitle(c.in)
		if got.Ru != c.ru || got.Orig != c.orig || got.Year != c.year || got.Quality != c.quality {
			t.Errorf("ParseTitle(%q)\n  = Ru %q, Orig %q, Year %d, Quality %q\n  нужно Ru %q, Orig %q, Year %d, Quality %q",
				c.in, got.Ru, got.Orig, got.Year, got.Quality, c.ru, c.orig, c.year, c.quality)
		}
	}
}

func TestParseTitleKeepsAllNames(t *testing.T) {
	got := ParseTitle("Зверь / Дикий драйв / La fiera / To the Max (Сальвадор Калво / Salvador Calvo) [2026, Испания, WEB-DL 720p]")
	if want := []string{"Зверь", "Дикий драйв", "La fiera", "To the Max"}; !slices.Equal(got.Names, want) {
		t.Fatalf("названия %q, нужно %q", got.Names, want)
	}
}

func TestNormTitle(t *testing.T) {
	cases := map[string]string{
		"The Matrix: Reloaded": "the matrix reloaded",
		"Ёлки-палки!":          "елки палки",
		"  СОУЛМ8ЙТ  ":         "соулм8йт",
		"Космос. Первая кровь": "космос первая кровь",
		"Don't Die (2025)":     "don t die 2025",
	}
	for in, want := range cases {
		if got := NormTitle(in); got != want {
			t.Errorf("NormTitle(%q) = %q, нужно %q", in, got, want)
		}
	}
}
