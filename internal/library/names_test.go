package library

import (
	"slices"
	"testing"
)

// Имена из D:\Share (проба 2026-09-30, спека, раздел 5.4).
func TestParseName(t *testing.T) {
	for _, c := range []struct {
		name  string
		title string
		year  int
		kp    int
	}{
		{"Crime.101.2026.WEBRip.H264.AC3.mkv", "Crime 101", 2026, 0},
		{"Frankenstein.2025.1080p.NF.WEB-DL.DDP5.1.H.264-EniaHD.mkv", "Frankenstein", 2025, 0},
		{"Kimitachi wa Dou Ikiru ka 1080p.mkv", "Kimitachi wa Dou Ikiru ka", 0, 0},
		{"Star.Trek.Picard", "Star Trek Picard", 0, 0},
		{"Xena.Warrior.Princess", "Xena Warrior Princess", 0, 0},
		{"Andor (Season 1) WEB-DLRip", "Andor", 0, 0},
		{"Пространство (Сезон 1-6) LostFilm", "Пространство", 0, 0},
		{"На помощь!_2026_WEB-DLRip", "На помощь!", 2026, 0},
		{"Холоп.3.2026.WEBRip.[1080p].NNMClub.mkv", "Холоп 3", 2026, 0},
		{"Trudno.byt.bogom.S01.2026.WEB-DL.1080p.ExKinoRay", "Trudno byt bogom", 2026, 0},
		{"3.Body.Problem.S01.WEB-DLRip.LF", "3 Body Problem", 0, 0},
		{"Fisher.S01.WEB-DL.1080.25Kuzmich", "Fisher", 0, 0},
		{"The Sopranos BDRip", "The Sopranos", 0, 0},
		{"The Boys (Season 5) WEB-DL 1080p", "The Boys", 0, 0},
		{"Стерлинг-Поинт.S01.WEB-DLRip.RHS", "Стерлинг-Поинт", 0, 0},
		{"Blade.Runner.2049.2017.BDRip.mkv", "Blade Runner 2049", 2017, 0},
		{"1917.2019.1080p.mkv", "1917", 2019, 0},
		{"1917.mkv", "1917", 0, 0},
		{"Мой фильм [kp12345]", "Мой фильм", 0, 12345},
		{"Better Call Saul", "Better Call Saul", 0, 0},
		{"Volshebnyj.uchastok.S01.2023.WEB-DL.1080p", "Volshebnyj uchastok", 2023, 0},
	} {
		p := ParseName(c.name)
		if p.Title != c.title || p.Year != c.year || p.KP != c.kp {
			t.Errorf("%s → %+v, нужно «%s» %d kp%d", c.name, p, c.title, c.year, c.kp)
		}
	}
}

// Транслит: варианты кириллицы с мягким знаком и без; кириллица — только она сама.
func TestVariants(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Trudno byt bogom", "трудно быть богом"},
		{"Vstat na nogi", "встать на ноги"},
		{"Ubit Ritu", "убить риту"},
		{"Zhizn po vyzovu", "жизнь по вызову"},
		{"Volshebnyj uchastok", "волшебный участок"},
		{"Volshebnyi uchastok", "волшебный участок"},
		{"Poslednij rubezh", "последний рубеж"},
		{"Advokaty", "адвокаты"},
		{"Malahit", "малахит"},
		{"Granitsa mirov", "граница миров"},
		{"Eterna", "этерна"},
		{"Robonyanya", "робоняня"},
	} {
		v := Variants(c.in)
		if len(v) == 0 || v[0] != c.in || len(v) > 5 || !slices.Contains(v, c.want) {
			t.Errorf("%s → %q, нужно среди них «%s» (первым — как есть, всего не больше 5)", c.in, v, c.want)
		}
	}
	if v := Variants("Волшебный участок"); !slices.Equal(v, []string{"Волшебный участок"}) {
		t.Errorf("кириллица: %q", v)
	}
}

func TestNorm(t *testing.T) {
	for _, c := range [][2]string{
		{"Star Trek: Picard", "star trek picard"},
		{"Звёздный  путь — Пикар!", "звездный путь пикар"},
		{" Xena: Warrior Princess ", "xena warrior princess"},
	} {
		if got := Norm(c[0]); got != c[1] {
			t.Errorf("Norm(%q) = %q, нужно %q", c[0], got, c[1])
		}
	}
}

// Сезон по имени папки: широкое определение — внутри единицы, строгое — «папка сама сериал».
func TestSeasonDir(t *testing.T) {
	for _, c := range []struct {
		name  string
		n     int
		ok    bool
		exact bool // SeasonOnly
	}{
		{"S01", 1, true, true},
		{"s2", 2, true, true},
		{"Season 1", 1, true, true},
		{"Season.03", 3, true, true},
		{"Сезон 4", 4, true, true},
		{"1 сезон", 1, true, true},
		{"Andor (Season 1) WEB-DLRip", 1, true, false},
		{"Better.Call.Saul.S02.BDRip.1080p", 2, true, false},
		{"Advokaty.S01.2026.WEB-DL.1080p.ExKinoRay", 1, true, false},
		{"2. Работа с памятью в Go", 0, false, false},
		{"Extras", 0, false, false},
		{"S01E02", 0, false, false},
	} {
		n, ok := SeasonDir(c.name)
		_, exact := SeasonOnly(c.name)
		if n != c.n || ok != c.ok || exact != c.exact {
			t.Errorf("%s → %d %v строго %v, нужно %d %v %v", c.name, n, ok, exact, c.n, c.ok, c.exact)
		}
	}
}

func TestEpisode(t *testing.T) {
	for _, c := range []struct {
		name string
		s, e int
	}{
		{"Trudno.byt.bogom.S01.E03.2026.WEB-DL.1080p.ExKinoRay.mkv", 1, 3},
		{"Silo.S02E10.1080p.mkv", 2, 10},
		{"show.1x05.avi", 1, 5},
		{"Серия 7.mp4", 0, 7},
		{"12 серия.mkv", 0, 12},
		{"E04.mkv", 0, 4},
		{"0.-Вступительное-слово.mp4", 0, 0},
		{"Frankenstein.2025.1080p.mkv", 0, 0},
	} {
		s, e := Episode(c.name)
		if s != c.s || e != c.e {
			t.Errorf("%s → %d×%d, нужно %d×%d", c.name, s, e, c.s, c.e)
		}
	}
}

func TestIsVideo(t *testing.T) {
	for name, want := range map[string]bool{"a.MKV": true, "b.m2ts": true, "c.mp3": false, "d.srt": false, "e.pdf": false, ".DS_Store": false, "f.webm": true} {
		if isVideo(name) != want {
			t.Errorf("%s: %v", name, !want)
		}
	}
}
