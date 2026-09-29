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
	t1, rt := newFake("t1"), newFake("t2")
	t1.top["12"] = []source.Release{
		rel("t1", "1", "Матрица / The Matrix (1999) BDRip", 90, 1, "h1"),
		rel("t1", "2", "Матрица / The Matrix (1999) HEVC", 30, 1, "h2"),
		rel("t1", "3", "Матрица (1999) CAMRip", 20, 1, "h3"),
	}
	for id, kp := range map[string]string{"1": "301", "2": "301", "3": "999"} {
		t1.details[id] = source.Details{Release: source.Release{Title: t1.top["12"][id[0]-'1'].Title}, KinopoiskID: kp}
	}
	t1.search = []source.Release{
		rel("t1", "3", "Матрица (1999) CAMRip", 20, 1, "h3"),                      // в описании номер 999 — другой фильм
		rel("t1", "10", "Матрица / The Matrix (1999) WEB-DL 1080p", 50, 1, "h10"), // без страницы: название и год
		rel("t1", "11", "Матрица времени (2017) HDRip", 70, 1, "h11"),             // другое название
	}
	rt.search = []source.Release{
		rel("t2", "20", "Матрица / The Matrix [2000, BDRip]", 40, 1, "h20"), // год отличается на 1
		rel("t2", "21", "The Matrix [1985, VHSRip]", 60, 1, "h21"),          // год далеко
	}
	rt.top["2110"] = []source.Release{rel("t2", "30", "", 5, 1, "h30")} // названия ещё нет
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, t1, rt)
	refresh(t, c, true)
	enrichAll(t, c, "t1")
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	url := fmt.Sprintf("/api/v1/releases/%d/variants", releaseID(t, db, "t1", "1"))

	var v VariantsView
	if code := getJSON(t, mux, url, &v); code != 200 || v.Search != nil || keys(t, db, v.Items) != "t1:1 t1:2" {
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
	if got, want := keys(t, db, v.Items), "t1:1 t1:10* t2:20* t1:2"; got != want {
		t.Fatalf("раздачи: %s, нужно %s", got, want)
	}
	if v.Search.Trackers["t1"] != SearchOK || v.Search.Trackers["t2"] != SearchOK {
		t.Fatalf("трекеры: %+v", v.Search.Trackers)
	}
	if t1.Calls("search:Матрица 1999") != 1 || rt.Calls("search:Матрица 1999") != 1 {
		t.Fatalf("поиски: t1 %d, t2 %d — опросы идут по одному поиску", t1.Calls("search"), rt.Calls("search"))
	}
	if h, err := c.History(ctx); err != nil || len(h) != 0 {
		t.Fatalf("поиск других раздач попал в историю: %+v %v", h, err)
	}
	c.mu.Lock()
	urgent := map[string][]int64{"t1": slices.Clone(c.urgent["t1"]), "t2": slices.Clone(c.urgent["t2"])}
	c.mu.Unlock()
	if !slices.Equal(urgent["t1"], []int64{releaseID(t, db, "t1", "10")}) ||
		!slices.Equal(urgent["t2"], []int64{releaseID(t, db, "t2", "20")}) {
		t.Fatalf("в догрузке вне очереди: %v", urgent)
	}
	var e struct{ Error string }
	noTitle := fmt.Sprintf("/api/v1/releases/%d/variants?search=1", releaseID(t, db, "t2", "30"))
	if code := getJSONErr(t, mux, noTitle, &e); code != http.StatusConflict || !strings.Contains(e.Error, "нет названия") {
		t.Fatalf("без названия: %d %q", code, e.Error)
	}
}

// Повторные опросы (poll=1) идут по тому же поиску, даже если трекер ответил ошибкой: пульт опрашивает,
// пока догружаются найденные, и без этого искал бы на трекерах каждые 3 с. Новое нажатие «Искать на
// трекерах» (без poll) ищет заново.
func TestSearchOtherReleasesPollKeepsSearch(t *testing.T) {
	t1, rt := newFake("t1"), newFake("t2")
	t1.top["12"] = []source.Release{rel("t1", "1", "Матрица / The Matrix (1999) BDRip", 90, 1, "h1")}
	t1.details["1"] = source.Details{Release: source.Release{Title: t1.top["12"][0].Title}}
	t1.search = []source.Release{rel("t1", "10", "Матрица / The Matrix (1999) WEB-DL 1080p", 50, 1, "h10")}
	rt.searchErr = errors.New("t2: капча — вход не выполнен")
	db := openDB(t)
	c, clk := newCatalog(t, db, nil, t1, rt)
	refresh(t, c, true)
	enrichAll(t, c, "t1")
	mux := http.NewServeMux()
	c.Register(muxRouter{mux})
	url := fmt.Sprintf("/api/v1/releases/%d/variants?search=1", releaseID(t, db, "t1", "1"))
	var v VariantsView
	for deadline := time.Now().Add(5 * time.Second); v.Search == nil || !v.Search.Complete; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("поиск не закончился: %+v", v.Search)
		}
		getJSON(t, mux, url+"&poll=1", &v)
	}
	clk.add(time.Minute) // законченный с ошибкой поиск без poll=1 уже искал бы заново
	getJSON(t, mux, url+"&poll=1", &v)
	if rt.Calls("search") != 1 || !v.Search.Complete || keys(t, db, v.Items) != "t1:1 t1:10*" {
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
