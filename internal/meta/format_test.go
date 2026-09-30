package meta

import "testing"

// Формат раздачи — по её видеофайлам: несколько — по убыванию общего размера; не видео не считается
// (спека этапа 7, раздел 10.2).
func TestFormat(t *testing.T) {
	cases := []struct {
		files []File
		want  string
	}{
		{[]File{{"S01E01.mkv", 700}, {"S01E02.MKV", 700}, {"sample.txt", 1}}, "MKV"},
		{[]File{{"Фильм/film.avi", 1400}, {"Фильм/extra.mkv", 700}, {"Фильм/extra2.mkv", 600}}, "AVI, MKV"},
		{[]File{{`Сезон\01.m4v`, 10}, {"02.mp4", 10}, {"03.mpeg", 5}}, "MP4, MPG"},
		{[]File{{"cover.jpg", 10}, {"readme.nfo", 1}}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := Format(c.files); got != c.want {
			t.Errorf("%v: %q, нужно %q", c.files, got, c.want)
		}
	}
}

// Формат из описания: «Формат видео», «Формат» или «Контейнер» с форматом видео; строки звука и
// пропорций пропускаются.
func TestFormatInText(t *testing.T) {
	cases := map[string]string{
		"Качество: WEB-DL 1080p\nКонтейнер: MKV\nВидео: AVC":                   "MKV",
		"Формат видео: AVI\nВидео: XviD":                                       "AVI",
		"Аудио 1: Русский\nФормат: AC3, 448 kbps\n...\nКонтейнер: MKV (Сэмпл)": "MKV",
		"Формат: 16:9\nформат: mp4":                                            "MP4",
		"ФОРМАТ: MPEG":                                                         "MPG",
		"Формат субтитров: softsub (SRT)":                                      "",
		"Описание без технических данных":                                      "",
		// MediaInfo (хвост Х24): значение поля целиком — «MPEG-4», «MPEG Audio», «MPEG-TS» не MPG.
		"Формат : MPEG-4\nКонтейнер: MKV":        "MKV",
		"Формат : MPEG Audio\nФормат видео: AVI": "AVI",
		"Формат: MPEG-TS":                        "",
		"Формат : MPEG-4 Visual":                 "",
		"Формат: MPEG-PS":                        "MPG",
		"Контейнер: MKV (Matroska)":              "MKV",
		"Формат видео: MKV, 1920x1080":           "MKV",
	}
	for text, want := range cases {
		if got := FormatInText(text); got != want {
			t.Errorf("%q: %q, нужно %q", text, got, want)
		}
	}
}
