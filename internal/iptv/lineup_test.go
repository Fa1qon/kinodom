package iptv

import (
	"strings"
	"testing"
	"time"

	"kinodom/internal/iptv/labels"
	"kinodom/internal/iptv/m3u"
	"kinodom/internal/iptv/xmltv"
)

// Справочник телепрограммы для тестов: как у iptvx.one — региональные версии отдельными каналами.
const testEPG = `<tv>
<channel id="pervy"><display-name>Первый канал</display-name><display-name>Первый</display-name><icon src="http://logo/pervy.png"/></channel>
<channel id="pervy-pl4"><display-name>Первый канал +4</display-name></channel>
<channel id="pervy-pl2"><display-name>Первый канал +2</display-name></channel>
<channel id="rossia1"><display-name>Россия 1</display-name></channel>
<channel id="match-tv"><display-name>Матч ТВ</display-name><display-name>Матч!</display-name></channel>
<channel id="spas"><display-name>Спас</display-name></channel>
<channel id="ntv"><display-name>НТВ</display-name></channel>
<channel id="tet-ua"><display-name>ТЕТ</display-name></channel>
<channel id="kino1"><display-name>Кино</display-name></channel>
<channel id="kino2"><display-name>Кино</display-name></channel>
<channel id="bbc"><display-name>BBC News</display-name></channel>
<channel id="aljazeera"><display-name>Al Jazeera</display-name></channel>
<channel id="local-news"><display-name>Местные новости</display-name></channel>
</tv>`

const testOrgChannels = `[
{"id":"Perviykanal.ru","name":"Perviy kanal","alt_names":["Первый канал"],"country":"RU","categories":["general"]},
{"id":"Match.ru","name":"Match!","alt_names":["Матч!"],"country":"RU","categories":["sports"]},
{"id":"Spas.ru","name":"Spas","alt_names":["Спас"],"country":"RU","categories":["religious"]},
{"id":"NTV.ru","name":"NTV","alt_names":["НТВ"],"country":"RU","categories":["general"]},
{"id":"BBCNews.uk","name":"BBC News","country":"UK","categories":["news"]},
{"id":"AlJazeera.qa","name":"Al Jazeera","country":"QA","categories":["news"]}
]`

const testOrgFeeds = `[
{"channel":"BBCNews.uk","is_main":true,"languages":["eng"]},
{"channel":"AlJazeera.qa","is_main":true,"languages":["ara","eng"]}
]`

func testIndex(t *testing.T) (*epgIndex, *labels.Base) {
	t.Helper()
	day := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	g, err := xmltv.Parse(strings.NewReader(testEPG), day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := labels.LoadBase(strings.NewReader(testOrgChannels), strings.NewReader(testOrgFeeds))
	if err != nil {
		t.Fatal(err)
	}
	return newEPGIndex(g), b
}

// testPool — пул из описаний: «имя|tvg-id|группа» → ссылка по порядку, все живые.
type src struct {
	name, tvgID, group string
	shift              int
	playlist           int64
	state              string
}

func testPool(srcs ...src) *pool {
	p := newPool()
	p.playlists[1] = &Playlist{ID: 1, Name: "a"}
	p.playlists[2] = &Playlist{ID: 2, Name: "платный", Limited: true}
	for i, s := range srcs {
		id := int64(i + 1)
		url := "http://s/" + strings.Repeat("x", i) + "/" + s.name
		pl := s.playlist
		if pl == 0 {
			pl = 1
		}
		st := s.state
		if st == "" {
			st = StateAlive
		}
		stream := &Stream{ID: id, URL: url, State: st, Entries: []Entry{{Playlist: pl,
			Entry: m3u.Entry{Name: s.name, TvgID: s.tvgID, Group: s.group, Shift: s.shift, URL: url}}}}
		p.streams[id] = stream
		p.byURL[url] = stream
	}
	return p
}

// Очередь признаков сопоставления (спека этапа 8, раздел 5.3).
func TestMatch(t *testing.T) {
	ix, base := testIndex(t)
	cases := []struct {
		s    src
		want string
	}{
		{src{name: "Первый HD", tvgID: "pervy"}, "pervy"},
		{src{name: "Первый канал +4", tvgID: "pervy"}, "pervy-pl4"}, // tvg-id московский, в названии «+4» — не склеивать
		{src{name: "Первый", tvgID: "pervy", shift: 4}, "pervy-pl4"},
		{src{name: "Первый канал (+4)"}, "pervy-pl4"},
		{src{name: "Спас +4"}, "spas+4"}, // своей версии нет — сдвинутый канал
		{src{name: "Спас", tvgID: "spas", shift: 4}, "spas+4"},
		{src{name: "Match TV (1080p)", tvgID: "Match.ru@HD"}, "match-tv"}, // мост iptv-org
		{src{name: "PERVIY KANAL", tvgID: "Perviykanal.ru@SD"}, "pervy"},
		{src{name: "НТВ HD"}, "ntv"},
		{src{name: "Кино"}, ""},              // два канала с таким названием
		{src{name: "Неизвестный канал"}, ""}, // нет в телепрограмме
		{src{name: "Канал", tvgID: "нет-такого"}, ""},
		{src{name: "Россия 1 (+7) (Владивосток)"}, "rossia1+7"}, // город в скобках — последней попыткой
		{src{name: "НТВ +0 (Липецк)"}, "ntv"},
		{src{name: "НТВ (2)"}, "ntv"},
	}
	for _, c := range cases {
		p := testPool(c.s)
		if got := match(p.streams[1], p, ix, base).key; got != c.want {
			t.Errorf("%+v: канал %q, нужно %q", c.s, got, c.want)
		}
	}
	// Правки сильнее всего: по ссылке, потом по названию; «скрыт».
	p := testPool(src{name: "Кино", tvgID: "pervy"})
	p.nameRules["кино"] = Rule{Channel: "kino2"}
	if r := match(p.streams[1], p, ix, base); r.key != "kino2" || r.by != "name-rule" {
		t.Errorf("правка по названию: %+v", r)
	}
	p.streamRules[p.streams[1].URL] = Rule{Channel: "ntv"}
	if r := match(p.streams[1], p, ix, base); r.key != "ntv" || r.by != "rule" {
		t.Errorf("правка по ссылке: %+v", r)
	}
	p.streamRules[p.streams[1].URL] = Rule{Hidden: true}
	if r := match(p.streams[1], p, ix, base); !r.hidden {
		t.Errorf("скрытый поток: %+v", r)
	}
}

func keysOf(cs []*Channel) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Key)
	}
	return strings.Join(out, " ")
}

func buildTest(t *testing.T, p *pool, h Hidden, local int) *Lineup {
	t.Helper()
	ix, base := testIndex(t)
	return build(buildInput{pool: p, epg: ix, base: base, hidden: h, localShift: local, now: time.Now()})
}

// Порядок: федеральные по номеру (версия под местный пояс, если жива), потом остальные по категориям,
// внутри — Россия, другие страны, без страны; избранное устройства — сверху.
func TestLineupOrder(t *testing.T) {
	p := testPool(
		src{name: "Первый канал"}, src{name: "Первый канал +4"}, src{name: "Матч ТВ"}, src{name: "НТВ"},
		src{name: "Спас +4"}, src{name: "Спас"}, src{name: "ТЕТ"}, src{name: "BBC News"}, src{name: "Al Jazeera"},
		src{name: "Местные новости", group: "Местные"}, src{name: "Первый канал +2"}, src{name: "Россия 1", state: StateSilent},
	)
	l := buildTest(t, p, Hidden{OtherZones: true}, 4)
	// Федеральные: Первый +4 (1), Матч (3), НТВ (4), Спас +4 (12); Россия 1 молчит — её нет. Дальше по
	// категориям: новости (Великобритания, Катар), общие, религия, без категории (Россия, Украина).
	if got := keysOf(l.WithFavorites(nil, false)); got != "pervy-pl4 match-tv ntv spas+4 bbc aljazeera pervy spas local-news tet-ua" {
		t.Errorf("порядок: %s", got)
	}
	if c := l.ByKey["pervy-pl4"]; c.Federal != 1 || c.Name != "Первый канал +4" || c.Labels.Category != "general" || c.Labels.Country != "RU" {
		t.Errorf("Первый +4: %+v — метки от московского", c)
	}
	if c := l.ByKey["spas+4"]; c.Federal != 12 || c.Name != "Спас +4" || c.Shift != 4 || c.EPGID != "spas" {
		t.Errorf("Спас +4: %+v", c)
	}
	if c := l.ByKey["pervy-pl2"]; c.Hidden != HiddenZone {
		t.Errorf("Первый +2 — чужой пояс, а скрыт %q", c.Hidden)
	}
	if c := l.ByKey["local-news"]; c.Labels.Country != "RU" || c.Labels.Category != "" {
		t.Errorf("местный канал: %+v", c.Labels)
	}
	// Избранное устройства — сверху, без повторов; федеральный из избранного уходит из блока.
	if got := keysOf(l.WithFavorites([]string{"bbc", "pervy-pl4", "нет-такого"}, false)); got != "bbc pervy-pl4 match-tv ntv spas+4 aljazeera pervy spas local-news tet-ua" {
		t.Errorf("с избранным: %s", got)
	}
	// Москва в пояс заказчика: федеральные — московские версии.
	l = buildTest(t, p, Hidden{}, 0)
	if got := keysOf(l.Order[:4]); got != "pervy match-tv ntv spas" {
		t.Errorf("пояс Москвы: %s", got)
	}
}

// Федеральный канал: версия «+4» без источника — в блоке московская.
func TestFederalFallsBackToMoscow(t *testing.T) {
	p := testPool(src{name: "Первый канал"}, src{name: "Первый канал +4", state: StateDead})
	l := buildTest(t, p, Hidden{OtherZones: true}, 4)
	if c := l.ByKey["pervy"]; c.Federal != 1 {
		t.Errorf("московский Первый не в блоке: %+v", c)
	}
	if got := keysOf(l.Order); got != "pervy" {
		t.Errorf("порядок: %s", got)
	}
}

// Правило скрытия (спека этапа 8, раздел 5.6).
func TestLineupHidden(t *testing.T) {
	p := testPool(src{name: "Матч ТВ"}, src{name: "BBC News"}, src{name: "Al Jazeera"}, src{name: "ТЕТ"}, src{name: "Местные новости"}, src{name: "НТВ"})
	no := func(s string) *string { return &s }
	p.overrides["ntv"] = Override{Hidden: true}
	p.overrides["tet-ua"] = Override{Category: no("sports")}
	h := Hidden{Categories: []string{"sports", ""}, Countries: []string{"UA"}, Languages: []string{"ara"}}
	l := buildTest(t, p, h, 4)
	want := map[string]string{
		"match-tv":   "",             // федеральный — скрытие категорий не трогает
		"bbc":        "",             // английский
		"aljazeera":  "",             // арабский и английский — не все языки скрыты
		"tet-ua":     HiddenCategory, // категория поправлена на «Спорт»
		"local-news": HiddenCategory, // «Без категории» скрыта
		"ntv":        HiddenChannel,  // поштучно — даже федеральный
	}
	for k, w := range want {
		if got := l.ByKey[k].Hidden; got != w {
			t.Errorf("%s: скрыт %q, нужно %q (метки %+v)", k, got, w, l.ByKey[k].Labels)
		}
	}
	h = Hidden{Languages: []string{"ara", "eng"}, Countries: []string{"UA"}}
	l = buildTest(t, p, h, 4)
	if l.ByKey["aljazeera"].Hidden != HiddenLanguage || l.ByKey["tet-ua"].Hidden != HiddenCountry {
		t.Errorf("языки и страна: %q %q", l.ByKey["aljazeera"].Hidden, l.ByKey["tet-ua"].Hidden)
	}
	if got := keysOf(l.WithFavorites([]string{"aljazeera", "ntv"}, false)); got != "aljazeera ntv match-tv local-news" {
		t.Errorf("избранное сильнее скрытия: %s", got)
	}
	if got := len(l.WithFavorites(nil, true)); got != 6 {
		t.Errorf("со скрытыми: %d каналов, нужно 6", got)
	}
}

// Источник: жив — предлагается; молчит, мёртв, новый — нет; новый из «ограниченного» плейлиста —
// предлагается без проверки. «Не распознано» — по нормализованному названию.
func TestLineupSourcesAndUnrecognized(t *testing.T) {
	p := testPool(src{name: "НТВ", state: StateNew}, src{name: "НТВ HD", state: StateNew, playlist: 2},
		src{name: "НТВ SD", state: StateDead}, src{name: "Кино HD"}, src{name: "Кино", state: StateSilent}, src{name: "Неизвестный"})
	l := buildTest(t, p, Hidden{}, 4)
	c := l.ByKey["ntv"]
	if len(c.Sources) != 1 || c.Sources[0].ID != 2 || len(c.Others) != 2 || c.Grade != GradeUnrated {
		t.Errorf("НТВ: предлагаются %d, остальные %d, оценка %q", len(c.Sources), len(c.Others), c.Grade)
	}
	if len(l.Unrecognized) != 2 || l.Unrecognized[0].Name != "кино" || len(l.Unrecognized[0].Streams) != 2 || l.Unrecognized[0].Alive != 1 ||
		l.Unrecognized[1].Name != "неизвестный" {
		t.Errorf("не распознано: %+v", l.Unrecognized)
	}
}

// Порядок источников (спека этапа 8, раздел 5.9): закреплённый; 🟢, 🟡, без полной проверки, 🔴; доля
// хороших за неделю; качество; время до данных.
func TestSourceOrder(t *testing.T) {
	p := testPool(src{name: "НТВ"}, src{name: "НТВ HD"}, src{name: "НТВ SD"}, src{name: "НТВ FHD"}, src{name: "НТВ 4K"}, src{name: "НТВ orig"})
	p.streams[1].Grade = GradeRed
	p.streams[2].Grade = GradeGreen
	p.streams[3].Grade = GradeGreen // SD, но доля хороших выше
	p.streams[4].Grade = GradeYellow
	p.streams[5].Grade = "" // 4K без полной проверки
	p.streams[6].Grade = GradeGreen
	p.streams[6].TTFB = 100
	ix, base := testIndex(t)
	in := buildInput{pool: p, epg: ix, base: base, localShift: 4, now: time.Now(), goodShare: map[int64]float64{3: 1, 2: 0.5, 6: 0.5}}
	l := build(in)
	var ids []int64
	for _, s := range l.ByKey["ntv"].Sources {
		ids = append(ids, s.ID)
	}
	if got, want := ids, []int64{3, 2, 6, 4, 5, 1}; !equal(got, want) {
		t.Errorf("порядок источников %v, нужно %v", got, want)
	}
	p.overrides["ntv"] = Override{PinnedURL: p.streams[1].URL}
	l = build(in)
	if l.ByKey["ntv"].Sources[0].ID != 1 || l.ByKey["ntv"].Grade != GradeRed {
		t.Errorf("закреплённый не первый")
	}
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
