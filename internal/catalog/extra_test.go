package catalog

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"kinodom/internal/meta"
	"kinodom/internal/source"
)

// fakeExtra — источник поиска Jacred / Jackett в памяти.
type fakeExtra struct {
	mu      sync.Mutex
	off     bool
	results []source.Release
	err     error
	calls   int
}

func (f *fakeExtra) Name() string     { return "jacred" }
func (f *fakeExtra) Configured() bool { f.mu.Lock(); defer f.mu.Unlock(); return !f.off }
func (f *fakeExtra) set(fn func())    { f.mu.Lock(); fn(); f.mu.Unlock() }

func (f *fakeExtra) Search(context.Context, string) ([]source.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return slices.Clone(f.results), f.err
}

const lanternsHash = "0123456789abcdef0123456789abcdef01234567"

// kinozal — раздача Kinozal из источника: без страницы, с magnet и ссылкой на тему.
func kinozal(hash, title string, seeders int) source.Release {
	return source.Release{Tracker: "kinozal", TopicID: hash, InfoHash: hash, Title: title, Seeders: seeders, Leechers: 3, Size: 1 << 30,
		Magnet: "magnet:?xt=urn:btih:" + hash + "&tr=http://ann.kinozal.example", Link: "https://kinozal.example/details.php?id=" + hash[:4]}
}

func entryOf(es []Entry, tracker string) (Entry, bool) {
	for _, e := range es {
		if e.Tracker == tracker {
			return e, true
		}
	}
	return Entry{}, false
}

func TestOwnTracker(t *testing.T) {
	c, _ := newCatalog(t, openDB(t), nil, newFake("rutor"), newFake("rutracker"))
	if !c.Own("rutor") || !c.Own("rutracker") || c.Own("kinozal") || c.Own("jacred") || c.Own("") {
		t.Fatal("свои трекеры — rutor и rutracker")
	}
}

// Раздача чужого трекера (спека 11b, раздел 8): сохранена без «догружается», «Скачать» — по magnet,
// «На трекере» — ссылка источника, номер Кинопоиска ищется по названию; найден — постер сразу, а при
// открытии — описание Кинопоиска.
func TestExtraSearchForeign(t *testing.T) {
	db := openDB(t)
	rutor := newFake("rutor")
	rutor.search = []source.Release{rel("rutor", "1", "Фонари [01x01-06 из 08] (2026) WEB-DL", 30, 1, "aa01")}
	kz := kinozal(lanternsHash, "Фонари (1 сезон: 1-7 серии из 8) / Lanterns / 2026 / ПМ (LostFilm), СТ / WEB-DLRip", 12)
	x := &fakeExtra{results: []source.Release{kz}}
	ratings := meta.NewRatings(meta.RatingsOptions{KP: meta.NewKinopoisk(meta.KinopoiskOptions{}), DB: db})
	var descAsked []int
	var descMu sync.Mutex
	c, _ := newCatalog(t, db, func(o *Options) {
		o.Extra, o.Ratings = x, ratings
		o.FilmDescription = func(_ context.Context, kp int) (string, error) {
			descMu.Lock()
			descAsked = append(descAsked, kp)
			descMu.Unlock()
			return "Описание Кинопоиска " + strconv.Itoa(kp), nil
		}
	}, rutor)
	st := waitSearch(t, c, "Фонари")
	if st.Trackers["jacred"] != SearchOK || st.Trackers["rutor"] != SearchOK || len(st.Results) != 2 {
		t.Fatalf("поиск: %+v", st)
	}
	e, ok := entryOf(st.Results, "kinozal")
	if !ok || e.ID == 0 || e.DetailsPending || e.Seeders != 12 || e.TopicID != lanternsHash {
		t.Fatalf("Kinozal: %+v", e)
	}
	if q, err := ratings.Status(ctx); err != nil || q.Queue != 1 {
		t.Fatalf("очередь рейтингов: %+v, %v", q, err)
	}
	select {
	case <-c.postersWake:
	default:
	}
	r, err := c.Release(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.DetailsPending || r.Magnet != kz.Magnet || r.TrackerURL != kz.Link || r.Description != "" {
		t.Fatalf("раздача: pending %v, magnet %q, на трекере %q", r.DetailsPending, r.Magnet, r.TrackerURL)
	}
	c.mu.Lock()
	queued := len(c.urgent["kinozal"]) + len(c.found["kinozal"])
	c.mu.Unlock()
	if queued != 0 || rutor.Calls("details") != 0 {
		t.Fatal("страницу чужой раздачи догружать нечем")
	}

	// Очередь рейтингов нашла номер — постер Кинопоиска догружается сразу.
	key := "kinozal:" + lanternsHash
	if _, err := db.W.Exec(`INSERT INTO kp_films(kp_id, name_ru, name_orig, year, type, rating) VALUES(501, 'Фонари', 'Lanterns', 2026, 'TV_SERIES', 7.4)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.W.Exec(`INSERT INTO kp_releases(release_id, kp_id) VALUES(?, 501)`, key); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.postersWake:
	default:
	}
	c.ratingResolved("rutor:1")
	select {
	case <-c.postersWake:
		t.Fatal("постеры разбудила чужая задача")
	default:
	}
	c.ratingResolved(key)
	select {
	case <-c.postersWake:
	default:
		t.Fatal("номер найден — постеры не разбужены")
	}

	// Открыли — описание Кинопоиска: экран ждёт его, как страницу.
	r, err = c.Release(ctx, e.ID)
	if err != nil || r.Rating.KinopoiskID != 501 || !r.DetailsPending {
		t.Fatalf("с номером: kp %d, pending %v, %v", r.Rating.KinopoiskID, r.DetailsPending, err)
	}
	c.posterWG.Wait()
	r, err = c.Release(ctx, e.ID)
	if err != nil || r.Description != "Описание Кинопоиска 501" || r.DetailsPending {
		t.Fatalf("описание: %q, pending %v, %v", r.Description, r.DetailsPending, err)
	}
	descMu.Lock()
	defer descMu.Unlock()
	if len(descAsked) != 1 {
		t.Fatalf("описание спрошено %d раз", len(descAsked))
	}
}

// Одинаковый infohash — одна строка, наша (со страницей), даже если у чужой раздающих больше; тема
// Rutracker из источника — наша раздача по номеру темы, в догрузку; «Загрузки» по infohash — наша.
func TestExtraOurTrackerWins(t *testing.T) {
	const same = "1111111111111111111111111111111111111111"
	rutor, rt := newFake("rutor"), newFake("rutracker")
	rutor.search = []source.Release{rel("rutor", "5", "Фонари [01x01-06 из 08] (2026) WEB-DL 1080p", 3, 1, same)}
	x := &fakeExtra{results: []source.Release{
		kinozal(same, "Фонари (1 сезон: 1-6 серии из 8) / Lanterns / 2026 / ПМ / WEB-DL (1080p)", 50),
		{Tracker: "rutracker", TopicID: "6903474", InfoHash: "2222222222222222222222222222222222222222", Title: "Фонари / Lanterns / Сезон: 1 / Серии: 1-7 из 8 [2026, WEB-DLRip]",
			Seeders: 40, Magnet: "magnet:?xt=urn:btih:2222222222222222222222222222222222222222", Link: "https://rutracker.org/forum/viewtopic.php?t=6903474"},
	}}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Extra = x }, rutor, rt)
	st := waitSearch(t, c, "Фонари")
	var withSame []Entry
	for _, e := range st.Results {
		if e.InfoHash == same {
			withSame = append(withSame, e)
		}
	}
	if len(withSame) != 1 || withSame[0].Tracker != "rutor" {
		t.Fatalf("одинаковый infohash: %+v", withSame)
	}
	e, ok := entryOf(st.Results, "rutracker")
	if !ok || e.TopicID != "6903474" || !e.DetailsPending {
		t.Fatalf("Rutracker из источника: %+v", e)
	}
	c.mu.Lock()
	inQueue := slices.Contains(c.found["rutracker"], e.ID)
	c.mu.Unlock()
	if !inQueue {
		t.Fatal("тема Rutracker из источника не поставлена в догрузку")
	}
	r, err := c.Release(ctx, e.ID)
	if err != nil || r.TrackerURL != "https://rutracker.example/topic/6903474" || r.Magnet != "magnet:?xt=urn:btih:2222222222222222222222222222222222222222" {
		t.Fatalf("наша раздача: %q %q %v", r.TrackerURL, r.Magnet, err)
	}
	refs, err := c.ReleasesByHash(ctx, []string{same})
	if err != nil || refs[same].ID != withSame[0].ID {
		t.Fatalf("по infohash: %+v, %v", refs, err)
	}
}

// Устаревший infohash темы нашего трекера из источника (база Jacred отстаёт) не сбрасывает наши magnet,
// .torrent и страницу; новая тема из источника — с infohash.
func TestExtraKeepsOurRelease(t *testing.T) {
	db := openDB(t)
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{rel("rutor", "5", "Фонари [01x01-06 из 08] (2026) WEB-DL", 30, 1, "new5")}
	x := &fakeExtra{}
	c, _ := newCatalog(t, db, func(o *Options) { o.Extra = x }, rutor)
	refresh(t, c, true)
	if _, err := db.W.Exec(`UPDATE releases SET magnet = 'magnet:?xt=urn:btih:new5', torrent = x'01', details_at = 1 WHERE tracker = 'rutor' AND topic_id = '5'`); err != nil {
		t.Fatal(err)
	}
	x.set(func() {
		x.results = []source.Release{
			{Tracker: "rutor", TopicID: "5", InfoHash: "old5", Title: "Фонари [01x01-05 из 08] (2026) WEB-DL", Seeders: 31, Magnet: "magnet:?xt=urn:btih:old5", Link: "http://rutor.example/torrent/5"},
			{Tracker: "rutor", TopicID: "6", InfoHash: "new6", Title: "Фонари [01x01-07 из 08] (2026) WEB-DL", Seeders: 9, Magnet: "magnet:?xt=urn:btih:new6", Link: "http://rutor.example/torrent/6"},
		}
	})
	waitSearch(t, c, "Фонари")
	var ih, magnet, link string
	var torrent []byte
	var details int64
	if err := db.R.QueryRow(`SELECT infohash, magnet, torrent, details_at, link FROM releases WHERE tracker = 'rutor' AND topic_id = '5'`).
		Scan(&ih, &magnet, &torrent, &details, &link); err != nil {
		t.Fatal(err)
	}
	if ih != "new5" || magnet != "magnet:?xt=urn:btih:new5" || torrent == nil || details != 1 || link != "" {
		t.Fatalf("наша раздача: %q %q %v %d %q", ih, magnet, torrent, details, link)
	}
	if err := db.R.QueryRow(`SELECT infohash, magnet, details_at, link FROM releases WHERE tracker = 'rutor' AND topic_id = '6'`).
		Scan(&ih, &magnet, &details, &link); err != nil {
		t.Fatal(err)
	}
	if ih != "new6" || magnet != "" || details != 0 || link != "" {
		t.Fatalf("новая тема: %q %q %d %q", ih, magnet, details, link)
	}
}

// Источник отказал — наши трекеры ищут, у источника — причина; источник без адреса — его в поиске нет.
func TestExtraSourceDown(t *testing.T) {
	rutor := newFake("rutor")
	rutor.search = []source.Release{rel("rutor", "1", "Фонари [01x01-06 из 08] (2026) WEB-DL", 30, 1, "aa01")}
	x := &fakeExtra{err: errors.New("нужен ключ")}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Extra = x }, rutor)
	st := waitSearch(t, c, "Фонари")
	if st.Trackers["jacred"] != "нужен ключ" || st.Trackers["rutor"] != SearchOK || len(st.Results) != 1 {
		t.Fatalf("источник отказал: %+v", st)
	}
	x.set(func() { x.off = true })
	st = waitSearch(t, c, "Матрица")
	if _, ok := st.Trackers["jacred"]; ok {
		t.Fatalf("источник без адреса в поиске: %+v", st.Trackers)
	}
}

// «Искать на трекерах» у «Других раздач» спрашивает и источник.
func TestExtraInVariants(t *testing.T) {
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{rel("rutor", "1", "Фонари / Lanterns (2026) WEB-DL 1080p", 30, 1, "aa01")}
	x := &fakeExtra{results: []source.Release{kinozal(lanternsHash, "Фонари / Lanterns / 2026 / ПМ / WEB-DLRip", 12)}}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Extra = x }, rutor)
	refresh(t, c, true)
	es := list(t, c, ListOptions{})
	deadline := time.Now().Add(5 * time.Second)
	for poll := false; ; poll = true {
		vs, st, err := c.SearchVariants(ctx, es[0].ID, poll)
		if err != nil {
			t.Fatal(err)
		}
		if st.Complete {
			if _, ok := entryOf(vs, "kinozal"); !ok || st.Trackers["jacred"] != SearchOK {
				t.Fatalf("другие раздачи: %+v %+v", vs, st.Trackers)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("поиск не закончился: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
