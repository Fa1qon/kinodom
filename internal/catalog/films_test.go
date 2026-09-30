package catalog

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"kinodom/internal/meta"
	"kinodom/internal/source"
	"kinodom/internal/store"
)

// filmsFixture — фильм 301 на обоих трекерах: у трёх раздач номер из описания, у одной — найден очередью
// рейтингов; раздача с номером 302 в описании, которую очередь по ошибке связала с 301; сериал 500 из
// двух сезонов; две раздачи без номера. ids — номер раздачи в Kinodom по номеру на трекере.
func filmsFixture(t *testing.T) (*Catalog, *store.DB, map[string]int64) {
	t.Helper()
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.top["12"] = []source.Release{
		rel("rutor", "1", "Матрица / The Matrix (1999) BDRip", 90, 1, "h1"),
		rel("rutor", "2", "Матрица / The Matrix (1999) WEB-DL 1080p", 40, 1, "h2"),
		rel("rutor", "3", "Матрица / The Matrix (1999) HDRip", 10, 1, "h3"),
		rel("rutor", "4", "Другой (2020) WEB-DL", 50, 1, "h4"),
		rel("rutor", "5", "Третий (2021) WEB-DL", 30, 1, "h5"),
		rel("rutor", "6", "Матрица: Перезагрузка / The Matrix Reloaded (2003) BDRip", 20, 1, "h6"),
		rel("rutor", "7", "Динозавры / The Dinosaurs [S01] (2026) WEB-DL 720p", 70, 1, "h7"),
		rel("rutor", "8", "Динозавры / The Dinosaurs [S02] (2027) WEB-DL 720p", 5, 1, "h8"),
	}
	rt.top["2110"] = []source.Release{rel("rutracker", "9", "Матрица / The Matrix [1999, BDRip 1080p]", 60, 1, "h9")}
	kp := map[string]string{"1": "301", "2": "301", "6": "302", "7": "500", "8": "500"}
	for _, r := range rutor.top["12"] {
		rutor.details[r.TopicID] = source.Details{Release: source.Release{Title: r.Title}, KinopoiskID: kp[r.TopicID]}
	}
	rt.details["9"] = source.Details{Release: source.Release{Title: rt.top["2110"][0].Title}, KinopoiskID: "301"}
	db := openDB(t)
	ratings := meta.NewRatings(meta.RatingsOptions{KP: meta.NewKinopoisk(meta.KinopoiskOptions{}), DB: db})
	c, _ := newCatalog(t, db, func(o *Options) { o.Ratings = ratings }, rutor, rt)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	enrichAll(t, c, "rutracker")
	// Очередь рейтингов нашла фильм 301 для раздачи 3 и — по ошибке — для 6: у неё в описании 302.
	mustExec(t, db, `INSERT INTO kp_films(kp_id, name_ru) VALUES (301, 'Матрица'), (302, 'Матрица: Перезагрузка')`)
	mustExec(t, db, `INSERT INTO kp_releases(release_id, kp_id) VALUES ('rutor:3', 301), ('rutor:6', 301)`)
	ids := map[string]int64{}
	rows, err := db.R.Query(`SELECT topic_id, id FROM releases`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var topic string
		var id int64
		rows.Scan(&topic, &id)
		ids[topic] = id
	}
	return c, db, ids
}

func mustExec(t *testing.T, db *store.DB, q string) {
	t.Helper()
	if _, err := db.W.Exec(q); err != nil {
		t.Fatal(err)
	}
}

// cards — карточки раздела: «номер на трекере:раздач фильма».
func cards(t *testing.T, c *Catalog) string {
	t.Helper()
	var out []string
	for _, e := range list(t, c, ListOptions{Tracker: "rutor", Category: "12"}) {
		out = append(out, e.TopicID+":"+strconv.Itoa(e.Variants))
	}
	return strings.Join(out, " ")
}

// Раздачи одного фильма в разделе — одна карточка: с наибольшим числом раздающих или в формате в
// приоритете; variants — раздач фильма на обоих трекерах. Раздача с другим номером в описании в чужой
// фильм не попадает (спека этапа 7, раздел 10.4).
func TestOneCardPerFilm(t *testing.T) {
	c, db, _ := filmsFixture(t)
	if got, want := cards(t, c), "1:4 7:2 4:1 5:1 6:1"; got != want {
		t.Fatalf("карточки %s, нужно %s", got, want)
	}
	mustExec(t, db, `UPDATE releases SET format = 'MKV' WHERE topic_id IN ('2', '3', '8')`)
	c.SetPreferredFormat("MKV")
	if got, want := cards(t, c), "4:1 2:4 5:1 6:1 8:2"; got != want {
		t.Fatalf("MKV в приоритете: карточки %s, нужно %s", got, want)
	}
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	var v ListView
	if code := getJSON(t, mux, "/api/v1/catalog?tracker=rutor&section=12", &v); code != 200 || v.Pages != 1 || len(v.Entries) != 5 ||
		v.Entries[1].Variants != 4 || v.Entries[1].Format != "MKV" {
		t.Fatalf("список: %d %+v", code, v)
	}
}

// «Другие раздачи» — раздачи того же фильма на обоих трекерах, включая эту: формат в приоритете, потом
// раздающие; у сериала — сезон из заголовка; без номера Кинопоиска — только эта раздача.
func TestVariantsRoute(t *testing.T) {
	c, db, ids := filmsFixture(t)
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	variants := func(topic string) string {
		t.Helper()
		var v VariantsView
		if code := getJSON(t, mux, "/api/v1/releases/"+strconv.FormatInt(ids[topic], 10)+"/variants", &v); code != 200 {
			t.Fatalf("раздача %s: код %d", topic, code)
		}
		var out []string
		for _, e := range v.Items {
			s := e.Tracker + ":" + topicOf(ids, e.ID)
			if e.Season != "" {
				s += "(" + e.Season + ")"
			}
			out = append(out, s)
		}
		return strings.Join(out, " ")
	}
	if got, want := variants("2"), "rutor:1 rutracker:9 rutor:2 rutor:3"; got != want {
		t.Fatalf("фильм 301: %s, нужно %s", got, want)
	}
	if got, want := variants("7"), "rutor:7(S01) rutor:8(S02)"; got != want {
		t.Fatalf("сериал: %s, нужно %s", got, want)
	}
	if got, want := variants("4"), "rutor:4"; got != want {
		t.Fatalf("без номера КП: %s, нужно %s", got, want)
	}
	if got, want := variants("6"), "rutor:6"; got != want {
		t.Fatalf("номер 302 из описания: %s, нужно %s", got, want)
	}
	mustExec(t, db, `UPDATE releases SET format = 'MKV' WHERE topic_id IN ('2', '3')`)
	c.SetPreferredFormat("MKV")
	if got, want := variants("9"), "rutor:2 rutor:3 rutor:1 rutracker:9"; got != want {
		t.Fatalf("MKV в приоритете: %s, нужно %s", got, want)
	}
	if code := getJSON(t, mux, "/api/v1/releases/999999/variants", nil); code != http.StatusNotFound {
		t.Fatalf("нет раздачи: %d", code)
	}
}

// topicOf — номер на трекере по номеру в Kinodom.
func topicOf(ids map[string]int64, id int64) string {
	for topic, x := range ids {
		if x == id {
			return topic
		}
	}
	return "?"
}

// Одна раздача на двух трекерах (одинаковый infohash) — в «Других раздачах» одной строкой, у открытой
// раздачи — ею самой, а не двойником с другого трекера: строк столько же, сколько «N раздач» на карточке.
func TestVariantsShowOpenedTwin(t *testing.T) {
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.top["12"] = []source.Release{rel("rutor", "1", "Планета / Planet [S01] (2022) WEB-DL 1080p", 47, 1, "same")}
	rt.top["2110"] = []source.Release{rel("rutracker", "8", "Планета / Planet / Сезон: 1 [2022, WEB-DL 1080p]", 152, 1, "same"),
		rel("rutracker", "9", "Планета / Planet / Сезон: 2 [2023, WEB-DL 1080p]", 84, 1, "other")}
	for _, r := range rutor.top["12"] {
		rutor.details[r.TopicID] = source.Details{Release: source.Release{Title: r.Title}, KinopoiskID: "700"}
	}
	for _, r := range rt.top["2110"] {
		rt.details[r.TopicID] = source.Details{Release: source.Release{Title: r.Title}, KinopoiskID: "700"}
	}
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, rutor, rt)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	enrichAll(t, c, "rutracker")
	var id int64
	db.R.QueryRow(`SELECT id FROM releases WHERE tracker = 'rutor' AND topic_id = '1'`).Scan(&id)
	es, err := c.Variants(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range es {
		got = append(got, e.Tracker+":"+e.TopicID)
	}
	if strings.Join(got, " ") != "rutracker:9 rutor:1" {
		t.Fatalf("другие раздачи: %v", got)
	}
	if cs := list(t, c, ListOptions{Tracker: "rutor"}); len(cs) != 1 || cs[0].Variants != len(es) {
		t.Fatalf("на карточке %+v, в списке %d", cs, len(es))
	}
}

// «Загрузки» различают сезоны и качество одного сериала: у раздачи по infohash — сезон и качество из
// заголовка (финальное ревью 7b).
func TestReleasesByHashSeasonAndQuality(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{
		rel("rutor", "1", "Динозавры / The Dinosaurs [S01] (2026) WEB-DL 1080p", 50, 1, "aa"),
		rel("rutor", "2", "Динозавры / The Dinosaurs [S02] (2027) WEB-DL 720p", 40, 1, "bb"),
	}
	c, _ := newCatalog(t, openDB(t), nil, rutor)
	refresh(t, c, true)
	refs, err := c.ReleasesByHash(ctx, []string{"aa", "bb"})
	if err != nil {
		t.Fatal(err)
	}
	if r := refs["aa"]; r.Season != "S01" || r.Quality != "WEB-DL 1080p" {
		t.Fatalf("первый сезон: %+v", r)
	}
	if r := refs["bb"]; r.Season != "S02" || r.Quality != "WEB-DL 720p" {
		t.Fatalf("второй сезон: %+v", r)
	}
}

// workFixture — раздачи Rutor: рипы одного фильма без номера, сезоны разных лет, фильм и сериал с одним
// названием и годом, раздача с номером в описании и раздача того же сериала без номера.
func workFixture(t *testing.T) (*Catalog, *store.DB) {
	t.Helper()
	rutor := newFake("rutor")
	add := func(id, title string, seeders int, kp string) {
		r := rel("rutor", id, title, seeders, 1<<30, "h"+id)
		rutor.top["12"] = append(rutor.top["12"], r)
		rutor.details[id] = source.Details{Release: r, Description: "Описание", KinopoiskID: kp}
	}
	add("1", "Динозавры / The Dinosaurs (2026) WEB-DL 720p", 50, "")
	add("2", "Динозавры / The Dinosaurs (2026) WEB-DL 2160p", 40, "")
	add("3", "Холод [S01] (2026) WEB-DL 1080p", 30, "")
	add("4", "Холод [S02] (2027) WEB-DL 1080p", 29, "")
	add("5", "Удар [S01] (2026) WEB-DL 1080p", 20, "")
	add("6", "Удар / La frappe (2026) WEB-DL 1080p", 19, "")
	add("7", "Законник [S01] (2023) WEB-DL 1080p", 10, "5325705")
	add("8", "Законник [01-10 из 10] (2023) WEB-DL 2160p", 9, "")
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, rutor)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	return c, db
}

// Одна карточка на произведение и без номера Кинопоиска (№ 7, Х5; спека 11b, 5.3; Review Focus 3).
func TestOneCardPerWork(t *testing.T) {
	c, _ := workFixture(t)
	var got []string
	for _, e := range list(t, c, ListOptions{Tracker: "rutor"}) {
		got = append(got, fmt.Sprintf("%s:%d", e.TopicID, e.Variants))
	}
	if want := "1:2 3:1 4:1 5:1 6:1 7:2"; strings.Join(got, " ") != want {
		t.Fatalf("карточки %q, нужно %q", strings.Join(got, " "), want)
	}
}

// «Другие раздачи» раздачи без номера — раздачи того же произведения на этом трекере.
func TestVariantsWithoutNumber(t *testing.T) {
	c, db := workFixture(t)
	es, err := c.Variants(ctx, releaseID(t, db, "rutor", "2"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range es {
		got = append(got, e.TopicID)
	}
	slices.Sort(got)
	if strings.Join(got, ",") != "1,2" {
		t.Fatalf("другие раздачи: %v", got)
	}
}
