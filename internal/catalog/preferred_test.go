package catalog

import (
	"net/http"
	"strconv"
	"testing"
)

// План 14А, задача 2: правило «формат в приоритете» — основной (самый большой) формат раздачи, как у
// порядка «Других раздач»; «Нет» — меток нет.
func TestPrefersMainFormat(t *testing.T) {
	cases := []struct {
		format, pref string
		want         bool
	}{
		{"MKV", "MKV", true}, {"MKV, AVI", "MKV", true}, {"AVI, MKV", "MKV", false},
		{"AVI", "MKV", false}, {"MKV", "", false}, {"", "MKV", false},
	}
	for _, c := range cases {
		if got := prefers(c.format, c.pref); got != c.want {
			t.Errorf("prefers(%q, %q) = %v", c.format, c.pref, got)
		}
	}
}

// План 14А, задача 2: формат в приоритете подсвечен везде — у карточки по показанной раздаче, а если он есть
// только у другой раздачи фильма — отдельной меткой с названием формата (ревью 14А, Important 2: синим не
// красится чужой формат); в «Других раздачах» и на странице раздачи — по самой раздаче; «Нет» — меток нет.
func TestPreferredFormatMarks(t *testing.T) {
	c, db, ids := filmsFixture(t)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	// MKV — только у раздачи «Матрицы» на Rutracker: в разделе Rutor карточка показывает раздачу Rutor,
	// а метку получает по другой раздаче фильма.
	mustExec(t, db, `UPDATE releases SET format = 'AVI, MKV' WHERE topic_id IN ('1', '2', '3')`)
	mustExec(t, db, `UPDATE releases SET format = 'MKV' WHERE topic_id = '9'`)
	cards := func() map[string]string {
		t.Helper()
		var lv ListView
		if code := getJSON(t, mux, "/api/v1/catalog?tracker=rutor&section=12", &lv); code != 200 {
			t.Fatalf("раздел: %d", code)
		}
		out := map[string]string{}
		for _, e := range lv.Entries {
			mark := e.PreferredAlt
			if e.Preferred {
				mark = "своя"
			}
			out[topicOf(ids, e.ID)] = mark
		}
		return out
	}
	c.SetPreferredFormat("MKV")
	got := cards()
	if matrix := got["1"] + got["2"] + got["3"]; matrix != "MKV" || got["4"] != "" {
		t.Fatalf("карточки: %v (у «Матрицы» MKV только у другой раздачи — метка «MKV», у «Другого» — нет)", got)
	}
	mustExec(t, db, `UPDATE releases SET format = 'MKV' WHERE topic_id IN ('2', '3')`)
	var vv VariantsView
	if code := getJSON(t, mux, "/api/v1/releases/"+strconv.FormatInt(ids["1"], 10)+"/variants", &vv); code != 200 {
		t.Fatalf("другие раздачи: %d", code)
	}
	marks := map[string]bool{}
	for _, e := range vv.Items {
		marks[topicOf(ids, e.ID)] = e.Preferred
	}
	if !marks["2"] || !marks["3"] || marks["1"] || !marks["9"] {
		t.Fatalf("«Другие раздачи»: %v", marks)
	}
	// Страницу раздачи отдаёт приложение (app.handleRelease) из Release.View().
	rel, err := c.Release(ctx, ids["2"])
	if err != nil || !rel.View().Preferred {
		t.Fatalf("страница раздачи MKV: %+v, %v", rel.Entry, err)
	}
	c.SetPreferredFormat("")
	for topic, mark := range cards() {
		if mark != "" {
			t.Fatalf("формат «Нет» — метка %q у %s", mark, topic)
		}
	}
}

// План 14А, задача 2: в «Загрузках» формат — по расширению файла раздачи.
func TestFilePreferred(t *testing.T) {
	cases := []struct {
		name, pref string
		want       bool
	}{
		{"Фильм.2026.WEB-DL.1080p.mkv", "MKV", true}, {"Film.MKV", "MKV", true}, {"film.avi", "MKV", false},
		{"film.mkv", "", false}, {"без расширения", "MKV", false}, {`Сериал\S01E01.mp4`, "MP4", true},
	}
	for _, c := range cases {
		if got := FilePreferred(c.name, c.pref); got != c.want {
			t.Errorf("FilePreferred(%q, %q) = %v", c.name, c.pref, got)
		}
	}
}
