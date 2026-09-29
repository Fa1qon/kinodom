package catalog

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"kinodom/internal/source"
	"kinodom/internal/store"
)

func releaseID(t *testing.T, db *store.DB, tracker, topic string) int64 {
	t.Helper()
	var id int64
	if err := db.R.QueryRow(`SELECT id FROM releases WHERE tracker = ? AND topic_id = ?`, tracker, topic).Scan(&id); err != nil {
		t.Fatalf("%s:%s: %v", tracker, topic, err)
	}
	return id
}

// keys — «трекер:номер на трекере» раздач ответа, через пробел; у раздачи без страницы — «*».
func keys(t *testing.T, db *store.DB, es []EntryView) string {
	t.Helper()
	var out []string
	for _, e := range es {
		var topic string
		db.R.QueryRow(`SELECT topic_id FROM releases WHERE id = ?`, e.ID).Scan(&topic)
		k := e.Tracker + ":" + topic
		if e.DetailsPending {
			k += "*"
		}
		out = append(out, k)
	}
	return strings.Join(out, " ")
}

// «Искать на трекерах»: поиск по первому названию и году, без истории; из найденного остаются раздачи
// того же фильма — тот же номер Кинопоиска, а без номера — то же название и год ±1; найденные без
// страницы уходят в догрузку вне очереди (спека этапа 7, раздел 10.5).
func TestSearchOtherReleases(t *testing.T) {
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.top["12"] = []source.Release{
		rel("rutor", "1", "Матрица / The Matrix (1999) BDRip", 90, 1, "h1"),
		rel("rutor", "2", "Матрица / The Matrix (1999) HEVC", 30, 1, "h2"),
		rel("rutor", "3", "Матрица (1999) CAMRip", 20, 1, "h3"),
	}
	for id, kp := range map[string]string{"1": "301", "2": "301", "3": "999"} {
		rutor.details[id] = source.Details{Release: source.Release{Title: rutor.top["12"][id[0]-'1'].Title}, KinopoiskID: kp}
	}
	rutor.search = []source.Release{
		rel("rutor", "3", "Матрица (1999) CAMRip", 20, 1, "h3"),                      // в описании номер 999 — другой фильм
		rel("rutor", "10", "Матрица / The Matrix (1999) WEB-DL 1080p", 50, 1, "h10"), // без страницы: название и год
		rel("rutor", "11", "Матрица времени (2017) HDRip", 70, 1, "h11"),             // другое название
	}
	rt.search = []source.Release{
		rel("rutracker", "20", "Матрица / The Matrix [2000, BDRip]", 40, 1, "h20"), // год отличается на 1
		rel("rutracker", "21", "The Matrix [1985, VHSRip]", 60, 1, "h21"),          // год далеко
	}
	rt.top["2110"] = []source.Release{rel("rutracker", "30", "", 5, 1, "h30")} // названия ещё нет
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, rutor, rt)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	url := fmt.Sprintf("/api/v1/releases/%d/variants", releaseID(t, db, "rutor", "1"))

	var v VariantsView
	if code := getJSON(t, mux, url, &v); code != 200 || v.Search != nil || keys(t, db, v.Items) != "rutor:1 rutor:2" {
		t.Fatalf("без поиска: %d %s %+v", code, keys(t, db, v.Items), v.Search)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if code := getJSON(t, mux, url+"?search=1", &v); code != 200 {
			t.Fatalf("поиск: %d", code)
		}
		if v.Search != nil && v.Search.Complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("поиск не закончился: %+v", v.Search)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, want := keys(t, db, v.Items), "rutor:1 rutor:10* rutracker:20* rutor:2"; got != want {
		t.Fatalf("раздачи: %s, нужно %s", got, want)
	}
	if v.Search.Trackers["rutor"] != SearchOK || v.Search.Trackers["rutracker"] != SearchOK {
		t.Fatalf("трекеры: %+v", v.Search.Trackers)
	}
	if rutor.Calls("search:Матрица 1999") != 1 || rt.Calls("search:Матрица 1999") != 1 {
		t.Fatalf("поиски: Rutor %d, Rutracker %d — опросы идут по одному поиску", rutor.Calls("search"), rt.Calls("search"))
	}
	if h, err := c.History(ctx); err != nil || len(h) != 0 {
		t.Fatalf("поиск других раздач попал в историю: %+v %v", h, err)
	}
	c.mu.Lock()
	urgent := map[string][]int64{"rutor": slices.Clone(c.urgent["rutor"]), "rutracker": slices.Clone(c.urgent["rutracker"])}
	c.mu.Unlock()
	if !slices.Equal(urgent["rutor"], []int64{releaseID(t, db, "rutor", "10")}) ||
		!slices.Equal(urgent["rutracker"], []int64{releaseID(t, db, "rutracker", "20")}) {
		t.Fatalf("в догрузке вне очереди: %v", urgent)
	}
	var e struct{ Error string }
	noTitle := fmt.Sprintf("/api/v1/releases/%d/variants?search=1", releaseID(t, db, "rutracker", "30"))
	if code := getJSONErr(t, mux, noTitle, &e); code != http.StatusConflict || !strings.Contains(e.Error, "нет названия") {
		t.Fatalf("без названия: %d %q", code, e.Error)
	}
}

// Повторные опросы (poll=1) идут по тому же поиску, даже если трекер ответил ошибкой: пульт опрашивает,
// пока догружаются найденные, и без этого искал бы на трекерах каждые 3 с. Новое нажатие «Искать на
// трекерах» (без poll) ищет заново.
func TestSearchOtherReleasesPollKeepsSearch(t *testing.T) {
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.top["12"] = []source.Release{rel("rutor", "1", "Матрица / The Matrix (1999) BDRip", 90, 1, "h1")}
	rutor.details["1"] = source.Details{Release: source.Release{Title: rutor.top["12"][0].Title}}
	rutor.search = []source.Release{rel("rutor", "10", "Матрица / The Matrix (1999) WEB-DL 1080p", 50, 1, "h10")}
	rt.searchErr = errors.New("Rutracker: капча — вход не выполнен")
	db := openDB(t)
	c, clk := newCatalog(t, db, nil, rutor, rt)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	url := fmt.Sprintf("/api/v1/releases/%d/variants?search=1", releaseID(t, db, "rutor", "1"))
	var v VariantsView
	for deadline := time.Now().Add(5 * time.Second); v.Search == nil || !v.Search.Complete; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("поиск не закончился: %+v", v.Search)
		}
		getJSON(t, mux, url+"&poll=1", &v)
	}
	clk.add(time.Minute) // законченный с ошибкой поиск без poll=1 уже искал бы заново
	getJSON(t, mux, url+"&poll=1", &v)
	if rt.Calls("search") != 1 || !v.Search.Complete || keys(t, db, v.Items) != "rutor:1 rutor:10*" {
		t.Fatalf("повторный опрос: поисков %d, %+v, %s", rt.Calls("search"), v.Search, keys(t, db, v.Items))
	}
	getJSON(t, mux, url, &v)
	// Новый поиск идёт в своей горутине — ждём, пока он дойдёт до трекера.
	for deadline := time.Now().Add(5 * time.Second); rt.Calls("search") < 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("новое нажатие не искало заново: поисков %d", rt.Calls("search"))
		}
	}
}
