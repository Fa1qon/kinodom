package iptv

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"kinodom/internal/iptv/m3u"
	"kinodom/internal/store"
)

func openDB(t *testing.T) db {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return db{s}
}

func entry(name, url string) m3u.Entry { return m3u.Entry{Name: name, URL: url} }

// Одна ссылка — один источник, сколько бы плейлистов её ни содержали; обновление плейлиста заменяет
// его записи, источник без записей уходит; удаление плейлиста — то же (спека этапа 8, раздел 5.1).
func TestPoolEntries(t *testing.T) {
	ctx := context.Background()
	d := openDB(t)
	a := &Playlist{Name: "a", URL: "http://pl/a.m3u", AddedAt: time.Now()}
	b := &Playlist{Name: "b", AddedAt: time.Now()}
	for _, pl := range []*Playlist{a, b} {
		if err := d.insertPlaylist(ctx, pl); err != nil {
			t.Fatal(err)
		}
	}
	ids, removed, err := d.replaceEntries(ctx, a.ID, []m3u.Entry{entry("Первый", "http://s/1.m3u8"), entry("НТВ", "http://s/2.m3u8"), entry("Первый HD", "http://s/1.m3u8")})
	if err != nil || len(ids) != 2 || len(removed) != 0 {
		t.Fatalf("ids %v, removed %v, err %v", ids, removed, err)
	}
	ids2, _, err := d.replaceEntries(ctx, b.ID, []m3u.Entry{{Name: "Первый канал", URL: "http://s/1.m3u8", TvgID: "pervy", Shift: 4,
		Headers: m3u.Headers{UserAgent: "UA"}}})
	if err != nil || ids2["http://s/1.m3u8"] != ids["http://s/1.m3u8"] {
		t.Fatalf("общая ссылка — другой источник: %v против %v (%v)", ids2, ids, err)
	}
	p, err := d.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := p.byURL["http://s/1.m3u8"]
	if s == nil || len(s.Entries) != 3 || s.State != StateNew || s.Kind != m3u.KindHLS {
		t.Fatalf("источник 1: %+v", s)
	}
	last := s.Entries[2]
	if last.Playlist != b.ID || last.TvgID != "pervy" || last.Shift != 4 || last.Headers.UserAgent != "UA" || last.URL != "http://s/1.m3u8" {
		t.Errorf("запись из b: %+v", last)
	}
	// Обновление a: ссылки 2 больше нет — источник 2 удалён; 1 остался (он есть в b).
	_, removed, err = d.replaceEntries(ctx, a.ID, []m3u.Entry{entry("Первый", "http://s/1.m3u8")})
	if err != nil || !reflect.DeepEqual(removed, []int64{ids["http://s/2.m3u8"]}) {
		t.Fatalf("удалены %v, нужно источник 2 (%v)", removed, err)
	}
	removed, err = d.deletePlaylist(ctx, b.ID)
	if err != nil || len(removed) != 0 {
		t.Fatalf("удаление b: %v, %v", removed, err)
	}
	removed, err = d.deletePlaylist(ctx, a.ID)
	if err != nil || !reflect.DeepEqual(removed, []int64{ids["http://s/1.m3u8"]}) {
		t.Fatalf("удаление a: %v, %v", removed, err)
	}
	if _, err := d.deletePlaylist(ctx, a.ID); err != errNoPlaylist {
		t.Errorf("повторное удаление: %v", err)
	}
}

// Правки, избранное устройства и проверки переживают перезапуск (читаются заново из базы).
func TestPoolRulesAndFavorites(t *testing.T) {
	ctx := context.Background()
	d := openDB(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(d.setStreamRule(ctx, "http://s/1", &Rule{Channel: "rossia1"}))
	must(d.setNameRule(ctx, "россия 1 orig", &Rule{Hidden: true}))
	must(d.setNameRule(ctx, "снятое", &Rule{Channel: "x"}))
	must(d.setNameRule(ctx, "снятое", nil))
	sports := "sports"
	must(d.setOverride(ctx, "match-tv", Override{Category: &sports, Languages: []string{"rus", "eng"}, PinnedURL: "http://s/9"}))
	must(d.setOverride(ctx, "pervy", Override{Hidden: true}))
	must(d.setOverride(ctx, "pervy", Override{}))
	p, err := d.load(ctx)
	must(err)
	if p.streamRules["http://s/1"] != (Rule{Channel: "rossia1"}) || p.nameRules["россия 1 orig"] != (Rule{Hidden: true}) || len(p.nameRules) != 1 {
		t.Errorf("правки: %+v %+v", p.streamRules, p.nameRules)
	}
	o := p.overrides["match-tv"]
	if o.Category == nil || *o.Category != "sports" || o.Country != nil || !reflect.DeepEqual(o.Languages, []string{"rus", "eng"}) || o.PinnedURL != "http://s/9" {
		t.Errorf("правки канала: %+v", o)
	}
	if _, ok := p.overrides["pervy"]; ok {
		t.Errorf("пустая правка не удалена")
	}
	must(d.setFavorites(ctx, "pc", []string{"ntv", "pervy", "ntv"}))
	must(d.setFavorites(ctx, "192.168.0.50", []string{"sts"}))
	pc, err := d.favorites(ctx, "pc")
	must(err)
	phone, err := d.favorites(ctx, "192.168.0.50")
	must(err)
	tv, err := d.favorites(ctx, "192.168.0.60")
	must(err)
	if !slices.Equal(pc, []string{"ntv", "pervy"}) || !slices.Equal(phone, []string{"sts"}) || len(tv) != 0 || tv == nil {
		t.Errorf("избранное: пк %v, телефон %v, телевизор %v", pc, phone, tv)
	}
}

// «Днём / вечером» за неделю и доля хороших проверок: вечер — с 19 до 23 по местному времени.
func TestPoolChecks(t *testing.T) {
	ctx := context.Background()
	d := openDB(t)
	pl := &Playlist{Name: "a", AddedAt: time.Now()}
	if err := d.insertPlaylist(ctx, pl); err != nil {
		t.Fatal(err)
	}
	ids, _, err := d.replaceEntries(ctx, pl.ID, []m3u.Entry{entry("A", "http://s/a")})
	if err != nil {
		t.Fatal(err)
	}
	s := &Stream{ID: ids["http://s/a"], URL: "http://s/a"}
	utc7 := time.FixedZone("UTC+7", 7*3600)
	at := func(h int) time.Time { return time.Date(2026, 9, 29, h, 0, 0, 0, utc7) }
	for _, c := range []check{
		{At: at(12), Level: "full", Grade: GradeGreen}, {At: at(13), Level: "full", Grade: GradeRed},
		{At: at(20), Level: "full", Grade: GradeYellow}, {At: at(21), Level: "full", Grade: GradeBlack},
		{At: at(22), Level: "light", Grade: GradeAlive}, {At: at(1).AddDate(0, 0, -10), Level: "full", Grade: GradeGreen},
	} {
		s.State, s.Grade = StateAlive, c.Grade
		if err := d.saveCheck(ctx, s, c); err != nil {
			t.Fatal(err)
		}
	}
	since := at(0).AddDate(0, 0, -7)
	w, err := d.weeks(ctx, []int64{s.ID}, since, utc7)
	if err != nil {
		t.Fatal(err)
	}
	if w[s.ID] != (Week{Day: 2, DayGood: 1, Evening: 2, EveningGood: 1}) {
		t.Errorf("неделя: %+v", w[s.ID])
	}
	share, err := d.goodShare(ctx, since)
	if err != nil || share[s.ID] != 0.5 {
		t.Errorf("доля хороших %v, нужно 0,5 (%v)", share[s.ID], err)
	}
	if err := d.pruneChecks(ctx, since); err != nil {
		t.Fatal(err)
	}
	var n int
	d.R.QueryRow(`SELECT COUNT(*) FROM probe_results`).Scan(&n)
	if n != 5 {
		t.Errorf("после чистки проверок %d, нужно 5", n)
	}
	p, err := d.load(ctx)
	if err != nil || p.streams[s.ID].Grade != GradeGreen || p.streams[s.ID].State != StateAlive {
		t.Errorf("состояние источника не записалось: %+v (%v)", p.streams[s.ID], err)
	}
}
