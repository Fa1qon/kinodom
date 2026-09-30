package meta

import "testing"

// Выдача поиска сайта (исследование 22.4, сокращённо): номер, вид, названия, годы, рейтинг.
var (
	hitsUdar = []Film{
		{ID: 8174380, Type: "TV_SERIES", NameRu: "Удар", Year: 2025},
		{ID: 12541899, Type: "FILM", NameRu: "Удар", NameOrig: "La frappe", Year: 2026},
		{ID: 705287, Type: "FILM", NameRu: "Удар", NameOrig: "Kick", Year: 2014, Rating: 7.369},
		{ID: 888205, Type: "TV_SERIES", NameRu: "Удар", NameOrig: "Peonchi", Year: 2014, YearEnd: 2015, Rating: 7.121},
	}
	hitsZakonnik = []Film{
		{ID: 5325705, Type: "TV_SERIES", NameRu: "Законник", Year: 2023, YearEnd: 2023, Rating: 7.969},
		{ID: 22271, Type: "FILM", NameRu: "Законник", NameOrig: "Restraining Order", Year: 1999, Rating: 5.559},
		{ID: 4646038, Type: "TV_SERIES", NameRu: "Законники: Басс Ривз", NameOrig: "Lawmen: Bass Reeves", Year: 2023, YearEnd: 2023, Rating: 6.421},
	}
	hitsTrudno = []Film{
		{ID: 7954692, Type: "TV_SERIES", NameRu: "Трудно быть богом", Year: 2026, Rating: 6.716},
		{ID: 40783, Type: "FILM", NameRu: "Трудно быть богом", Year: 2013, Rating: 5.39},
		{ID: 42000, Type: "FILM", NameRu: "Трудно быть богом", Year: 1989, Rating: 6.225},
	}
	hitsGruppa = []Film{
		{ID: 5364887, Type: "FILM", NameRu: "Группа крови", Year: 2025, Rating: 8.305},
		{ID: 565127, Type: "TV_SERIES", NameRu: "Пятая группа крови", Year: 2010, YearEnd: 2010, Rating: 7.689},
		{ID: 12825070, Type: "FILM", NameRu: "Группа крови", Year: 2025},
	}
	hitsMatrix = []Film{
		{ID: 301, Type: "FILM", NameRu: "Матрица", NameOrig: "The Matrix", Year: 1999, Rating: 8.501},
		{ID: 1294123, Type: "FILM", NameRu: "Матрица: Воскрешение", NameOrig: "The Matrix Resurrections", Year: 2021, Rating: 5.704},
		{ID: 400787, Type: "TV_SERIES", NameRu: "Матрица", NameOrig: "Matrix", Year: 1993, YearEnd: 1993, Rating: 6.054},
	}
	hitsMayday = []Film{
		{ID: 700001, Type: "MINI_SERIES", NameRu: "Mayday", NameOrig: "Mayday", Year: 2013, YearEnd: 2013},
		{ID: 700002, Type: "TV_SERIES", NameRu: "Расследования авиакатастроф", NameOrig: "Mayday", Year: 2003, Rating: 8.4},
	}
)

// Сверка найденного без токена (спека 11b, 5.1; Х4; исследование 22.3): год обязателен, вид и название —
// одна поблажка за раз; лучше без номера, чем чужой фильм.
func TestMatchKP(t *testing.T) {
	cases := []struct {
		release string
		hits    []Film
		want    int // 0 — номера нет
	}{
		{"Удар [S01] (2026) WEB-DL 1080p", hitsUdar, 8174380},        // сериал идёт с 2025
		{"Удар / La frappe (2026) WEB-DL 1080p", hitsUdar, 12541899}, // фильм
		{"Удар / Kick (2014) BDRip", hitsUdar, 705287},               // фильм 2014, не сериал 2014
		{"Законник [S01] (2023) WEB-DL", hitsZakonnik, 5325705},      // не «Законники: Басс Ривз»
		{"Законник (2023) WEB-DL", hitsZakonnik, 5325705},            // без признака сериала — одна поблажка (вид)
		{"Трудно быть богом [01-08 из 08] (2026) WEB-DL", hitsTrudno, 7954692},
		{"Трудно быть богом (2013) BDRip", hitsTrudno, 40783},
		{"Трудно быть богом (2020) WEB-DL", hitsTrudno, 0},        // год не сошёлся ни с одним
		{"Группа крови (2025) WEB-DL 1080p", hitsGruppa, 5364887}, // два фильма 2025 — берётся единственный с рейтингом
		{"Матрица / The Matrix (1999) BDRip", hitsMatrix, 301},
		{"Матрица (2021) WEB-DL", hitsMatrix, 1294123},                                                   // «Матрица: Воскрешение» — вхождением, год и вид сошлись
		{"Расследования авиакатастроф / Mayday [26 сезон: 10 серии] (2026) HDTVRip", hitsMayday, 700002}, // сезон 26 — не мини-сериал
		{"Mayday [S02] (2013) HDTVRip", hitsMayday, 700002},                                              // мини-сериал со вторым сезоном не бывает — сериал с 2003
		{"Матрица / The Matrix", hitsMatrix, 301},                                                        // без года — только точно по обоим названиям
		{"Матрица", hitsMatrix, 0},                                                                       // без года и одно название — фильм и сериал «Матрица»: номера нет
	}
	for _, c := range cases {
		got, ok := MatchKP(c.hits, ParseTitle(c.release))
		if c.want == 0 && ok {
			t.Errorf("%q: номер %d, нужно — без номера", c.release, got.ID)
		}
		if c.want != 0 && (!ok || got.ID != c.want) {
			t.Errorf("%q: номер %d (%v), нужно %d", c.release, got.ID, ok, c.want)
		}
	}
}
