package catalog

import (
	"fmt"
	"testing"

	"kinodom/internal/meta"
	"kinodom/internal/source"
)

// Финальное ревью 11b-Д: тема Rutor пришла из источника, пока у Rutor не было адреса, — сохранена раздачей без
// страницы (link, details_at). Потом адрес Rutor ввели, и та же тема пришла уже от самого Rutor и снова
// из источника. Ожидание разумного человека: это обычная раздача Rutor — со страницей, догрузкой и
// «Следить». Журнал (ruling задачи 4) обещает: «пока тема не придёт заново из списка».
func TestExtraOwnTopicGetsPageBack(t *testing.T) {
	db := openDB(t)
	rutor := newFake("rutor")
	rutor.off = true
	x := &fakeExtra{results: []source.Release{{Tracker: "rutor", TopicID: "5", InfoHash: "aaaa5", Title: "Фонари [01x01-06 из 08] (2026) WEB-DL",
		Seeders: 9, Magnet: "magnet:?xt=urn:btih:aaaa5", Link: "http://rutor.example/torrent/5"}}}
	c, _ := newCatalog(t, db, func(o *Options) { o.Extra = x }, rutor)
	waitSearch(t, c, "Фонари")

	// Адрес Rutor ввели; тема приходит от самого Rutor (поиск) и снова из источника.
	rutor.set(func() {
		rutor.off = false
		rutor.search = []source.Release{rel("rutor", "5", "Фонари [01x01-06 из 08] (2026) WEB-DL", 30, 1, "aaaa5")}
	})
	waitSearch(t, c, "Фонари 2026")

	var link string
	var details int64
	var id int64
	if err := db.R.QueryRow(`SELECT id, link, details_at FROM releases WHERE tracker = 'rutor' AND topic_id = '5'`).Scan(&id, &link, &details); err != nil {
		t.Fatal(err)
	}
	rs, err := c.st.rowsByID(ctx, []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	if c.pageless(rs[id]) {
		t.Errorf("тема Rutor после ввода адреса осталась без страницы навсегда: link %q, details_at %d", link, details)
	}
}

// Финальное ревью 11b-Д: человек поискал «Фонари», ввёл в «Параметрах» адрес источника, «Проверить» — «Jacred
// отвечает», и снова ищет «Фонари» (недавний запрос). Ожидание: в поиске есть Jacred и раздачи Kinozal.
func TestExtraSearchCacheSeesNewSource(t *testing.T) {
	rutor := newFake("rutor")
	rutor.search = []source.Release{rel("rutor", "1", "Фонари [01x01-06 из 08] (2026) WEB-DL", 30, 1, "aa01")}
	x := &fakeExtra{off: true, results: []source.Release{kinozal(lanternsHash, "Фонари / Lanterns / 2026 / ПМ / WEB-DLRip", 12)}}
	c, _ := newCatalog(t, openDB(t), func(o *Options) { o.Extra = x }, rutor)
	waitSearch(t, c, "Фонари")
	x.set(func() { x.off = false }) // адрес источника ввели в «Параметрах»
	st := waitSearch(t, c, "Фонари")
	if _, ok := st.Trackers["jacred"]; !ok {
		t.Errorf("тот же запрос после включения источника — из кэша, без Jacred: %+v, раздач %d", st.Trackers, len(st.Results))
	}
	// Сменили ключ источника (набор источников тот же): настройки сбрасывают кэш поиска.
	calls := func() int { x.mu.Lock(); defer x.mu.Unlock(); return x.calls }
	before := calls()
	c.ForgetSearches()
	waitSearch(t, c, "Фонари")
	if calls() != before+1 {
		t.Errorf("после смены настроек источника поиск — из кэша: запросов к источнику %d, было %d", calls(), before)
	}
}

// Финальное ревью 11b-Д: наша раздача Rutor со страницей и .torrent; адрес Rutor на время стёрли (Rutor заблокирован),
// и поиск принёс ту же тему из источника с устаревшим infohash. Ожидание (Review Focus 3): наши magnet и
// .torrent не сбрасываются, раздача остаётся нашей.
func TestExtraOwnRowWhileAddressOff(t *testing.T) {
	db := openDB(t)
	rutor := newFake("rutor")
	rutor.top["12"] = []source.Release{rel("rutor", "5", "Фонари [01x01-06 из 08] (2026) WEB-DL", 30, 1, "new5")}
	x := &fakeExtra{}
	c, _ := newCatalog(t, db, func(o *Options) { o.Extra = x }, rutor)
	refresh(t, c, true)
	if _, err := db.W.Exec(`UPDATE releases SET magnet = 'magnet:?xt=urn:btih:new5', torrent = x'01', details_at = 1 WHERE tracker = 'rutor' AND topic_id = '5'`); err != nil {
		t.Fatal(err)
	}
	rutor.set(func() { rutor.off = true })
	x.set(func() {
		x.results = []source.Release{{Tracker: "rutor", TopicID: "5", InfoHash: "old5", Title: "Фонари [01x01-05 из 08] (2026) WEB-DL", Seeders: 31,
			Magnet: "magnet:?xt=urn:btih:old5", Link: "http://rutor.example/torrent/5"}}
	})
	waitSearch(t, c, "Фонари")
	var ih, magnet, link string
	var torrent []byte
	if err := db.R.QueryRow(`SELECT infohash, magnet, torrent, link FROM releases WHERE tracker = 'rutor' AND topic_id = '5'`).Scan(&ih, &magnet, &torrent, &link); err != nil {
		t.Fatal(err)
	}
	if ih != "new5" || magnet != "magnet:?xt=urn:btih:new5" || torrent == nil || link != "" {
		t.Errorf("наша раздача при стёртом адресе Rutor: infohash %q, magnet %q, torrent %v, link %q", ih, magnet, torrent, link)
	}
}

// Финальное ревью 11b-Д: тема нашего трекера со страницей и .torrent версии «01-06»; источник (база отстаёт) прислал ту
// же тему с названием «01-05». Ожидание: название раздачи соответствует тому, что скачает «Скачать».
func TestExtraOwnTitleNotFromSource(t *testing.T) {
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
		x.results = []source.Release{{Tracker: "rutor", TopicID: "5", InfoHash: "old5", Title: "Фонари [01x01-05 из 08] (2026) WEB-DL", Seeders: 31,
			Magnet: "magnet:?xt=urn:btih:old5", Link: "http://rutor.example/torrent/5"}}
	})
	waitSearch(t, c, "Фонари")
	var title string
	if err := db.R.QueryRow(`SELECT title FROM releases WHERE tracker = 'rutor' AND topic_id = '5'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Фонари [01x01-06 из 08] (2026) WEB-DL" {
		t.Errorf("название нашей раздачи (magnet и .torrent — «01-06») заменено устаревшим из источника: %q", title)
	}
}

// Финальное ревью 11b-Д: источник отдаёт строки не по раздающим, а экран сортирует по ним — постер
// Кинопоиска получают все строки без картинки с известным номером, а не первые 20 в порядке источника.
func TestExtraPostersBeyondFirst20(t *testing.T) {
	db := openDB(t)
	ratings := meta.NewRatings(meta.RatingsOptions{KP: meta.NewKinopoisk(meta.KinopoiskOptions{}), DB: db})
	if _, err := db.W.Exec(`INSERT INTO kp_films(kp_id, name_ru, name_orig, year, type, rating) VALUES(501, 'Фонари', 'Lanterns', 2026, 'TV_SERIES', 7.4)`); err != nil {
		t.Fatal(err)
	}
	var rs []source.Release
	for i := range 25 {
		h := fmt.Sprintf("%040x", i+1)
		rs = append(rs, kinozal(h, "Фонари / Lanterns / 2026 / ПМ / WEB-DLRip", i+1)) // у последних — больше раздающих
		if _, err := db.W.Exec(`INSERT INTO kp_releases(release_id, kp_id) VALUES(?, 501)`, "kinozal:"+h); err != nil {
			t.Fatal(err)
		}
	}
	x := &fakeExtra{results: rs}
	im, kpPoster := kpPosters(t)
	c, _ := newCatalog(t, db, func(o *Options) { o.Extra, o.Ratings, o.Images, o.KinopoiskPoster = x, ratings, im, kpPoster }, newFake("rutor"))
	st := waitSearch(t, c, "Фонари")
	c.posterWG.Wait()
	for _, e := range st.Results {
		if got := storedImage(t, c, e.ID); got != meta.ImageKey(kpPoster(501)) {
			t.Errorf("раздача %d (раздающих %d) без постера Кинопоиска", e.ID, e.Seeders)
		}
	}
}
