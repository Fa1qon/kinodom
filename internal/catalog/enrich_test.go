package catalog

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kinodom/internal/meta"
	"kinodom/internal/netx"
	"kinodom/internal/source"
)

// Догрузка — в порядке каталога: страница, .torrent Rutor заранее; удалённая раздача уходит из
// каталога.
func TestEnrichInCatalogOrder(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{rel("rutor", "1", "Малый (2020) WEB-DL", 5, 1, "a"), rel("rutor", "2", "Большой (2020) WEB-DL", 50, 1, "b"), rel("rutor", "3", "Удалённый (2020) WEB-DL", 20, 1, "c")}
	rutor.details["2"] = source.Details{Release: source.Release{Title: "Большой / Big (2020) WEB-DL 1080p", Seeders: 55}, KinopoiskID: "301", Magnet: "magnet:?xt=urn:btih:bb"}
	rutor.details["1"] = source.Details{Release: source.Release{Title: "Малый (2020) WEB-DL"}}
	rutor.torrents["2"] = []byte("d4:infod4:name3:bigee")
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, rutor)
	refresh(t, c, true)
	if did, err := c.enrichStep(ctx, "rutor"); !did || err != nil {
		t.Fatal(did, err)
	}
	if rutor.Calls("details:2") != 1 || rutor.Calls("details") != 1 {
		t.Fatal("первой догружена не раздача с наибольшим числом раздающих")
	}
	enrichAll(t, c, "rutor")
	es := list(t, c, ListOptions{})
	if len(es) != 2 || es[0].Title != "Большой / Big (2020) WEB-DL 1080p" || es[0].Seeders != 55 {
		t.Fatalf("карточки %+v", es)
	}
	var torrent []byte
	db.R.QueryRow(`SELECT torrent FROM releases WHERE topic_id = '2'`).Scan(&torrent)
	if string(torrent) != "d4:infod4:name3:bigee" {
		t.Fatalf(".torrent %q", torrent)
	}
	if rutor.Calls("details") != 3 {
		t.Fatalf("страниц %d — каждая раздача один раз", rutor.Calls("details"))
	}
}

// Форум закрыт проверкой Cloudflare, пропуск не добыт: страницы не запрашиваются 10 минут (иначе
// сотни лишних запросов), названия — из ленты Atom, проблема в «Состоянии» (хвост этапа 4).
func TestChallengeStopsForumAndUsesFeed(t *testing.T) {
	rt := newFake("rutracker")
	rt.top["2110"] = []source.Release{rel("rutracker", "7", "", 30, 1, "a"), rel("rutracker", "8", "", 20, 1, "b")}
	rt.detailsErr["7"] = fmt.Errorf("Rutracker: не удаётся пройти защиту Cloudflare: %w", netx.ErrChallenge)
	rt.detailsErr["8"] = rt.detailsErr["7"]
	rt.recent["2110"] = []source.Release{{Tracker: "rutracker", TopicID: "8", Title: "Название из ленты"}}
	db := openDB(t)
	c, clk := newCatalog(t, db, nil, rt)
	refresh(t, c, true)
	enrichAll(t, c, "rutracker")
	if rt.Calls("details") != 1 || rt.Calls("recent") != 1 {
		t.Fatalf("страниц %d, лент %d", rt.Calls("details"), rt.Calls("recent"))
	}
	es := list(t, c, ListOptions{})
	if es[1].Title != "Название из ленты" {
		t.Fatalf("карточки %+v", es)
	}
	if p := problemText(t, db, "catalog.rutracker.forum"); !strings.Contains(p, "Cloudflare") {
		t.Fatalf("проблема %q", p)
	}
	clk.add(11 * time.Minute)
	rt.set(func() {
		rt.detailsErr = map[string]error{}
		rt.details["7"] = source.Details{Release: source.Release{Title: "Семь"}}
	})
	enrichAll(t, c, "rutracker")
	if problemText(t, db, "catalog.rutracker.forum") != "" {
		t.Fatal("форум открылся — проблема должна сняться")
	}
}

// Трекер не отвечает при догрузке — пауза, а не перебор сотен раздач с таймаутами.
func TestTrackerDownPausesEnrichment(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = many("rutor", 5)
	for i := range 5 {
		rutor.detailsErr[fmt.Sprint(i+1)] = fmt.Errorf("Rutor недоступен: %w", netx.ErrTrackerDown)
	}
	c, _ := newCatalog(t, openDB(t), nil, rutor)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	if rutor.Calls("details") != 1 {
		t.Fatalf("страниц %d при недоступном трекере", rutor.Calls("details"))
	}
}

// Сломался разбор страницы — «не найден блок X» в «Проблемах», раздача откладывается.
func TestParseErrorIsProblem(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = many("rutor", 1)
	rutor.detailsErr["1"] = &source.ParseError{Tracker: "Rutor", Block: "описание раздачи (table#details)"}
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, rutor)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	if p := problemText(t, db, "catalog.rutor.parse"); !strings.Contains(p, "table#details") {
		t.Fatalf("проблема %q", p)
	}
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	return b.Bytes()
}

// Постер со страницы раздачи; нет его или хостинг не отдал картинку — постер Кинопоиска.
func TestPosterFallsBackToKinopoisk(t *testing.T) {
	pic := pngBytes(t)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dead.jpg" {
			http.NotFound(w, r)
			return
		}
		w.Write(pic)
	}))
	t.Cleanup(host.Close)
	im, err := meta.NewImages(meta.ImagesOptions{Dir: t.TempDir(), Rate: 1000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{rel("rutor", "1", "А (2020) WEB-DL", 9, 1, "a"), rel("rutor", "2", "Б (2020) WEB-DL", 8, 1, "b")}
	rutor.details["1"] = source.Details{Release: source.Release{Title: "А (2020) WEB-DL"}, PosterURL: host.URL + "/p.png"}
	rutor.details["2"] = source.Details{Release: source.Release{Title: "Б (2020) WEB-DL"}, PosterURL: host.URL + "/dead.jpg", KinopoiskID: "301"}
	c, _ := newCatalog(t, openDB(t), func(o *Options) {
		o.Images = im
		o.KinopoiskPoster = func(id int) string { return fmt.Sprintf("%s/kp/%d.jpg", host.URL, id) }
	}, rutor)
	refresh(t, c, true)
	enrichAll(t, c, "rutor")
	es := list(t, c, ListOptions{})
	if es[0].ImageKey != meta.ImageKey(host.URL+"/p.png") || es[1].ImageKey != meta.ImageKey(host.URL+"/kp/301.jpg") {
		t.Fatalf("картинки %q, %q", es[0].ImageKey, es[1].ImageKey)
	}
}

// Рейтинги: после обновления — весь каталог в очереди в его порядке, но только раздачи со
// страницей (без неё нет ни номера Кинопоиска, ни названия у Rutracker); после догрузки —
// раздача в очередь сразу.
func TestRatingsQueueFollowsCatalog(t *testing.T) {
	db := openDB(t)
	ratings := meta.NewRatings(meta.RatingsOptions{KP: meta.NewKinopoisk(meta.KinopoiskOptions{}), DB: db})
	rutor := newFake("rutor")
	rutor.top["12"] = many("rutor", 3)
	rutor.details["1"] = source.Details{Release: source.Release{Title: "Фильм 0 (2020) WEB-DL"}, KinopoiskID: "301"}
	c, _ := newCatalog(t, db, func(o *Options) { o.Ratings = ratings }, rutor)
	refresh(t, c, true)
	queued := func() int {
		st, err := ratings.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st.Queue
	}
	if queued() != 0 {
		t.Fatal("раздачи без страницы в очереди рейтингов")
	}
	if did, err := c.enrichStep(ctx, "rutor"); !did || err != nil {
		t.Fatal(did, err)
	}
	if queued() != 1 {
		t.Fatalf("после догрузки в очереди %d", queued())
	}
	refresh(t, c, true)
	if queued() != 1 {
		t.Fatalf("после обновления в очереди %d", queued())
	}
}

// Форум Rutracker не отвечает, а API топов живой: обновление топов этого не видит — проблему ставит
// догрузка, названия новых раздач — из ленты, как при закрытом Cloudflare (Review Focus 2).
func TestForumDownWhileAPIUpIsVisible(t *testing.T) {
	rt := newFake("rutracker")
	rt.top["2110"] = []source.Release{rel("rutracker", "7", "", 30, 1, "a"), rel("rutracker", "8", "", 20, 1, "b")}
	down := fmt.Errorf("Rutracker недоступен (rutracker.org, rutracker.net — таймаут): %w", netx.ErrTrackerDown)
	rt.detailsErr["7"], rt.detailsErr["8"] = down, down
	rt.recent["2110"] = []source.Release{{Tracker: "rutracker", TopicID: "8", Title: "Название из ленты"}}
	db := openDB(t)
	c, _ := newCatalog(t, db, nil, rt)
	refresh(t, c, true)
	enrichAll(t, c, "rutracker")
	if rt.Calls("details") != 1 {
		t.Fatalf("страниц %d при лежащем форуме", rt.Calls("details"))
	}
	if p := problemText(t, db, "catalog.rutracker.forum"); !strings.Contains(p, "таймаут") || !strings.Contains(p, "без описаний") {
		t.Fatalf("проблема %q", p)
	}
	if es := list(t, c, ListOptions{}); es[1].Title != "Название из ленты" {
		t.Fatalf("карточки %+v", es)
	}
}
