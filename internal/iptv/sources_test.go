package iptv

import (
	"context"
	"slices"
	"testing"
	"time"

	"kinodom/internal/iptv/m3u"
	"kinodom/internal/iptv/probe"
)

func ptr[T any](v T) *T { return &v }

// Звук (отзыв заказчика 2026-09-30): полная проверка его определяет; лёгкая, не понявшая, не стирает.
func TestApplyResultAudio(t *testing.T) {
	s := &Stream{State: StateNew}
	now := time.Now()
	applyResult(s, "full", probe.Result{Grade: probe.GradeGreen, Audio: ptr(false)}, now)
	if s.Audio == nil || *s.Audio {
		t.Fatalf("без звука не записано: %v", s.Audio)
	}
	applyResult(s, "light", probe.Result{Grade: probe.GradeAlive}, now)
	if s.Audio == nil || *s.Audio {
		t.Errorf("лёгкая проверка стёрла звук: %v", s.Audio)
	}
	applyResult(s, "full", probe.Result{Grade: probe.GradeGreen, Audio: ptr(true)}, now)
	if s.Audio == nil || !*s.Audio {
		t.Errorf("звук есть — не записано: %v", s.Audio)
	}
}

// Источник без звука — после всех остальных, даже 🔴; закреплённый — всё равно первый.
func TestSourceOrderSilentLast(t *testing.T) {
	p := testPool(src{name: "НТВ"}, src{name: "НТВ HD"}, src{name: "НТВ SD"})
	p.streams[1].Grade = GradeGreen
	p.streams[1].Audio = ptr(false)
	p.streams[2].Grade = GradeRed
	p.streams[3].Grade = GradeYellow
	p.streams[3].Audio = ptr(true)
	l := buildTest(t, p, Hidden{}, 4)
	var ids []int64
	for _, s := range l.ByKey["ntv"].Sources {
		ids = append(ids, s.ID)
	}
	if want := []int64{3, 2, 1}; !equal(ids, want) {
		t.Errorf("порядок %v, нужно %v", ids, want)
	}
	p.overrides["ntv"] = Override{PinnedURL: p.streams[1].URL}
	if l = buildTest(t, p, Hidden{}, 4); l.ByKey["ntv"].Sources[0].ID != 1 {
		t.Errorf("закреплённый без звука не первый")
	}
}

// Скрытый у канала источник (кнопка «Скрыть»): не предлагается и не проверяется, но виден в настройках
// канала, чтобы вернуть.
func TestHiddenSource(t *testing.T) {
	p := testPool(src{name: "НТВ", state: StateSilent}, src{name: "НТВ HD"}, src{name: "Спас"})
	p.overrides["ntv"] = Override{HiddenURLs: []string{p.streams[1].URL}}
	p.overrides["spas"] = Override{HiddenURLs: []string{p.streams[3].URL}}
	l := buildTest(t, p, Hidden{}, 4)
	c := l.ByKey["ntv"]
	if len(c.Sources) != 1 || c.Sources[0].ID != 2 || len(c.Others) != 0 || len(c.Rejected) != 1 || c.Rejected[0].ID != 1 {
		t.Errorf("НТВ: предлагаются %d, остальные %d, скрытые %d", len(c.Sources), len(c.Others), len(c.Rejected))
	}
	for _, id := range (&Module{lineup: l, pool: p}).fullTargets(false, nil) {
		if id == 1 {
			t.Errorf("скрытый источник в полной проверке")
		}
	}
	if k := l.ByKey["spas"]; k == nil || k.Offered() || len(k.Rejected) != 1 {
		t.Errorf("канал, у которого скрыт единственный источник, предлагается")
	}
}

// Звук и скрытые источники переживают перезапуск.
func TestPoolAudioAndHiddenURLs(t *testing.T) {
	ctx := context.Background()
	d := openDB(t)
	pl := &Playlist{Name: "a", AddedAt: time.Now()}
	if err := d.insertPlaylist(ctx, pl); err != nil {
		t.Fatal(err)
	}
	ids, _, err := d.replaceEntries(ctx, pl.ID, []m3u.Entry{entry("A", "http://s/a"), entry("B", "http://s/b")})
	if err != nil {
		t.Fatal(err)
	}
	a := &Stream{ID: ids["http://s/a"], URL: "http://s/a", State: StateAlive, Audio: ptr(false)}
	if err := d.saveCheck(ctx, a, check{At: time.Now(), Level: "full", Grade: GradeGreen}); err != nil {
		t.Fatal(err)
	}
	hidden := []string{"http://s/a", "http://s/x,y"}
	if err := d.setOverride(ctx, "ntv", Override{HiddenURLs: hidden}); err != nil {
		t.Fatal(err)
	}
	p, err := d.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s := p.streams[a.ID]; s.Audio == nil || *s.Audio {
		t.Errorf("звук после перезапуска: %v", s.Audio)
	}
	if s := p.streams[ids["http://s/b"]]; s.Audio != nil {
		t.Errorf("непроверенный источник: звук %v, нужно «не знаю»", *s.Audio)
	}
	if o := p.overrides["ntv"]; !slices.Equal(o.HiddenURLs, hidden) {
		t.Errorf("скрытые источники после перезапуска: %q", o.HiddenURLs)
	}
}

type cardResp struct {
	ChannelView
	Sources   []SourceView `json:"sources"`
	UTCOffset *int         `json:"utcOffset"`
}

// Настройки канала (отзыв заказчика 2026-09-30): «Сделать основным», «Скрыть» и «Вернуть» источник;
// скрытый виден в карточке с пометкой; скрыли основной — он больше не закреплён.
func TestHTTPSourceEdits(t *testing.T) {
	m, c, f := startHTTP(t)
	second := f.srv.URL + "/s/ok.m3u8?2"
	if _, err := m.AddPlaylist(context.Background(), PlaylistInput{Name: "второй", Data: []byte("#EXTM3U\n#EXTINF:-1,НТВ\n" + second + "\n")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "второй источник НТВ", func() bool {
		m.rebuild(context.Background())
		return len(m.Lineup().ByKey["ntv"].Sources) == 2
	})
	var card cardResp
	c.json("GET", "/api/v1/channels/ntv", fromPhone, nil, &card)
	var id int64
	for _, s := range card.Sources {
		if s.URL == second {
			id = s.ID
		}
	}
	if code := c.json("PUT", "/api/v1/channels/ntv", fromPhone, map[string]any{"pinnedSource": id}, nil); code != 204 {
		t.Fatalf("сделать основным: %d", code)
	}
	if code := c.json("PUT", "/api/v1/channels/ntv", fromPhone, map[string]any{"hideSource": id}, nil); code != 204 {
		t.Fatalf("скрыть: %d", code)
	}
	c.json("GET", "/api/v1/channels/ntv", fromPhone, nil, &card)
	if n := len(card.Sources); n != 2 || card.Sources[1].ID != id || !card.Sources[1].Hidden || card.Sources[1].Offered || card.Sources[0].Hidden {
		t.Fatalf("после «Скрыть»: %+v", card.Sources)
	}
	if o := m.Override("ntv"); o.PinnedURL != "" || !slices.Equal(o.HiddenURLs, []string{second}) {
		t.Errorf("правка после «Скрыть» основного: %+v", o)
	}
	if code := c.json("PUT", "/api/v1/channels/ntv", fromPhone, map[string]any{"showSource": id}, nil); code != 204 {
		t.Fatalf("вернуть: %d", code)
	}
	c.json("GET", "/api/v1/channels/ntv", fromPhone, nil, &card)
	for _, s := range card.Sources {
		if s.Hidden || !s.Offered {
			t.Errorf("после «Вернуть»: %+v", s)
		}
	}
	if _, ok := m.pool.overrides["ntv"]; ok {
		t.Errorf("пустая правка после «Вернуть» не удалена: %+v", m.Override("ntv"))
	}
	for _, bad := range []map[string]any{{"hideSource": 999}, {"showSource": 999}} {
		if code := c.json("PUT", "/api/v1/channels/ntv", fromPhone, bad, nil); code != 404 {
			t.Errorf("%v: %d, нужно 404", bad, code)
		}
	}
}

// Время программы пульт показывает по поясу каналов из настроек, а не по часам устройства
// (отзыв заказчика 2026-09-30: на ПК московское время, в настройках UTC+7).
func TestHTTPUTCOffset(t *testing.T) {
	m, c, _ := startHTTP(t)
	m.SetLocation(Zone(5))
	for _, path := range []string{"/api/v1/channels", "/api/v1/channels/ntv", "/api/v1/channels/ntv/epg"} {
		var out struct {
			UTCOffset *int `json:"utcOffset"`
		}
		if c.json("GET", path, fromPhone, nil, &out); out.UTCOffset == nil || *out.UTCOffset != 5 {
			t.Errorf("%s: utcOffset %v, нужно 5", path, out.UTCOffset)
		}
	}
}
