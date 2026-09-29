package catalog

import (
	"strings"
	"testing"

	"kinodom/internal/source"
)

// Формат раздачи сохраняется вместе со страницей: у t1 — по файлам .torrent, у t2 — по
// описанию; не вышло по файлам — по описанию (спека этапа 7, раздел 10.2).
func TestFormatSavedWithDetails(t *testing.T) {
	t1, rt := newFake("t1"), newFake("t2")
	t1.top["12"] = []source.Release{rel("t1", "1", "Динозавры (2026) WEB-DL", 50, 1, "a"), rel("t1", "2", "Без файлов (2026) WEB-DL", 40, 1, "b")}
	t1.details["1"] = source.Details{Release: source.Release{Title: "Динозавры (2026) WEB-DL"}, Description: "Формат: AVI"}
	t1.details["2"] = source.Details{Release: source.Release{Title: "Без файлов (2026) WEB-DL"}, Description: "Контейнер: MP4"}
	t1.torrents["1"] = []byte("mkv-torrent")
	t1.torrents["2"] = []byte("broken")
	rt.top["2110"] = []source.Release{rel("t2", "7", "Космос [2025, WEB-DL]", 30, 1, "c"), rel("t2", "8", "Без формата [2025]", 20, 1, "d")}
	rt.details["7"] = source.Details{Release: source.Release{Title: "Космос [2025, WEB-DL]"}, Description: "Качество: WEB-DL\nФормат видео: MKV"}
	rt.details["8"] = source.Details{Release: source.Release{Title: "Без формата [2025]"}, Description: "Описание"}
	c, _ := newCatalog(t, openDB(t), func(o *Options) {
		o.TorrentFormat = func(b []byte) string {
			if string(b) == "mkv-torrent" {
				return "MKV"
			}
			return ""
		}
	}, t1, rt)
	refresh(t, c, true)
	enrichAll(t, c, "t1")
	enrichAll(t, c, "t2")
	got := map[string]string{}
	for _, e := range list(t, c, ListOptions{}) {
		got[e.TopicID] = e.View().Format
	}
	want := map[string]string{"1": "MKV", "2": "MP4", "7": "MKV", "8": ""}
	for id, f := range want {
		if got[id] != f {
			t.Errorf("раздача %s: формат %q, нужно %q", id, got[id], f)
		}
	}
}

// Раздачи, загруженные до появления формата (миграция 0008), получают его одним проходом после
// старта; определённый формат не пересчитывается.
func TestFormatFilledForOldReleases(t *testing.T) {
	t1 := newFake("t1")
	t1.top["12"] = []source.Release{rel("t1", "1", "Старая (2020) WEB-DL", 50, 1, "a"), rel("t1", "2", "Без формата (2020) WEB-DL", 40, 1, "b"),
		rel("t1", "3", "Не загружена (2020) WEB-DL", 30, 1, "c")}
	t1.details["1"] = source.Details{Release: source.Release{Title: "Старая (2020) WEB-DL"}, Description: "Контейнер: AVI"}
	t1.details["2"] = source.Details{Release: source.Release{Title: "Без формата (2020) WEB-DL"}, Description: "Описание"}
	t1.torrents["1"] = []byte("mkv-torrent")
	db := openDB(t)
	c, _ := newCatalog(t, db, func(o *Options) {
		o.TorrentFormat = func(b []byte) string {
			if strings.HasPrefix(string(b), "mkv") {
				return "MKV"
			}
			return ""
		}
	}, t1)
	refresh(t, c, true)
	for range 2 {
		if did, err := c.enrichStep(ctx, "t1"); !did || err != nil {
			t.Fatal(did, err)
		}
	}
	db.W.Exec(`UPDATE releases SET format = NULL`)
	if err := c.fillFormats(ctx); err != nil {
		t.Fatal(err)
	}
	formats := map[string]*string{}
	rows, _ := db.R.Query(`SELECT topic_id, format FROM releases`)
	for rows.Next() {
		var id string
		var f *string
		rows.Scan(&id, &f)
		formats[id] = f
	}
	rows.Close()
	if f := formats["1"]; f == nil || *f != "MKV" {
		t.Fatalf("раздача с .torrent: %v", f)
	}
	if f := formats["2"]; f == nil || *f != "" {
		t.Fatalf("формат не найден — должен стать «неизвестен», а не остаться NULL: %v", f)
	}
	if formats["3"] != nil {
		t.Fatalf("страницу не загружали — формат не определяли: %q", *formats["3"])
	}
	db.W.Exec(`UPDATE releases SET format = 'AVI' WHERE topic_id = '1'`)
	if err := c.fillFormats(ctx); err != nil {
		t.Fatal(err)
	}
	var f string
	db.R.QueryRow(`SELECT format FROM releases WHERE topic_id = '1'`).Scan(&f)
	if f != "AVI" {
		t.Fatalf("определённый формат пересчитан: %q", f)
	}
}

// Формат в приоритете — первым в результатах поиска, дальше по раздающим (спека этапа 7, раздел 10.3).
func TestSearchPrefersFormat(t *testing.T) {
	t1 := newFake("t1")
	t1.search = []source.Release{rel("t1", "1", "Матрица (1999) BDRip", 90, 1, "a"), rel("t1", "2", "Матрица (1999) WEB-DL", 40, 1, "b"),
		rel("t1", "3", "Матрица (1999) HDRip", 10, 1, "c")}
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, t1)
	waitSearch(t, c, "матрица")
	db.W.Exec(`UPDATE releases SET format = 'MKV' WHERE topic_id IN ('2', '3')`)
	db.W.Exec(`UPDATE releases SET format = 'AVI, MKV' WHERE topic_id = '1'`)
	order := func() string {
		var ids []string
		for _, e := range waitSearch(t, c, "матрица").Results {
			ids = append(ids, e.TopicID)
		}
		return strings.Join(ids, ",")
	}
	if got := order(); got != "1,2,3" {
		t.Fatalf("без приоритета: %s", got)
	}
	c.SetPreferredFormat("MKV")
	if got := order(); got != "2,3,1" {
		t.Fatalf("MKV в приоритете: %s", got)
	}
}
