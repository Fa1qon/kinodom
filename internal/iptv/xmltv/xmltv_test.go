package xmltv

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// Отрывок в формате iptvx.one: несколько названий у канала, логотип, время с поясом +0300.
const sample = `<?xml version="1.0" encoding="utf-8"?>
<tv generator-info-name="iptvx.one">
<channel id="spas"><display-name>Спас</display-name><display-name>Spas TV</display-name><icon src="https://iptvx.one/icons/spas.png"/></channel>
<channel id="pervy-pl4"><display-name>Первый канал +4</display-name><display-name>Первый (+4)</display-name></channel>
<channel id="empty"><display-name></display-name></channel>
<programme start="20260927200000 +0300" stop="20260927210000 +0300" channel="spas"><title lang="ru">Вечер</title></programme>
<programme start="20260929200000 +0300" stop="20260929210000 +0300" channel="spas"><title lang="ru">Новости</title><desc>длинное описание</desc></programme>
<programme start="20260929210000 +0300" stop="20260929220000 +0300" channel="spas"><title lang="ru">Кино &amp; жизнь</title></programme>
<programme start="20260929220000 +0300" stop="20260929230000 +0300" channel="spas"><title lang="ru">Новости</title></programme>
<programme start="20261005200000 +0300" stop="20261005210000 +0300" channel="spas"><title lang="ru">Далеко</title></programme>
<programme start="20260929170000 +0300" stop="20260929180000 +0300" channel="pervy-pl4"><title>Время</title></programme>
<programme start="20260929180000" stop="20260929190000" channel="pervy-pl4"><title>Без пояса</title></programme>
</tv>`

var utc7 = time.FixedZone("UTC+7", 7*3600)

func window() (time.Time, time.Time) {
	day := time.Date(2026, 9, 29, 0, 0, 0, 0, utc7)
	return day.Add(-12 * time.Hour), day.AddDate(0, 0, 3)
}

func parse(t *testing.T, b []byte) *Guide {
	t.Helper()
	from, to := window()
	g, err := Parse(bytes.NewReader(b), from, to)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestParseChannels(t *testing.T) {
	g := parse(t, []byte(sample))
	if len(g.Channels) != 3 {
		t.Fatalf("каналов %d, нужно 3", len(g.Channels))
	}
	c, ok := g.Channel("spas")
	if !ok || strings.Join(c.Names, "|") != "Спас|Spas TV" || c.Icon != "https://iptvx.one/icons/spas.png" {
		t.Fatalf("канал spas: %+v", c)
	}
	if c, _ := g.Channel("empty"); len(c.Names) != 0 {
		t.Errorf("пустое название не пропущено: %+v", c.Names)
	}
}

// Передачи — только в окне: вчерашний вечер по местному времени (запас 12 часов) и дальше трёх
// суток — не хранятся; время — абсолютное.
func TestParseProgrammesWindow(t *testing.T) {
	g := parse(t, []byte(sample))
	from, to := window()
	day := g.Programmes("spas", 0, from, to)
	var titles []string
	for _, p := range day {
		titles = append(titles, p.Title)
	}
	if got := strings.Join(titles, "|"); got != "Новости|Кино & жизнь|Новости" {
		t.Fatalf("передачи spas: %s", got)
	}
	if want := time.Date(2026, 9, 29, 17, 0, 0, 0, time.UTC); !day[0].Start.Equal(want) {
		t.Errorf("начало %v, нужно %v", day[0].Start, want)
	}
	// Одинаковые названия — одна строка в памяти.
	if day[0].Title != day[2].Title || !sameString(day[0].Title, day[2].Title) {
		t.Errorf("названия не объединены")
	}
	// Без пояса — UTC.
	pl := g.Programmes("pervy-pl4", 0, from, to)
	if len(pl) != 2 || !pl[1].Start.Equal(time.Date(2026, 9, 29, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("передачи pervy-pl4: %+v", pl)
	}
}

func TestParseGzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(sample))
	zw.Close()
	g := parse(t, buf.Bytes())
	if _, ok := g.Channel("pervy-pl4"); !ok {
		t.Fatal("из .gz не разобрался канал")
	}
}

// «Сейчас и следом» по местному времени; у сдвинутого канала «Спас +4» (своей версии в телепрограмме
// нет) — программа московского Спаса на 4 часа раньше: в 21:00 по UTC+7 идёт то, что в Москве в
// 21:00 МСК (спека этапа 8, критерий 6). Так устроены и версии «+4» в самой телепрограмме: «Время» у
// pervy — 21:00 МСК, у pervy-pl4 — 17:00 МСК.
func TestNowNextShift(t *testing.T) {
	g := parse(t, []byte(sample))
	at := time.Date(2026, 9, 29, 21, 30, 0, 0, utc7) // 17:30 МСК
	now, next := g.NowNext("spas", 0, at)
	if now != nil || next == nil || next.Title != "Новости" {
		t.Fatalf("московский Спас в 17:30 МСК: сейчас %v, следом %v", now, next)
	}
	now, next = g.NowNext("spas", 4, at)
	if now == nil || now.Title != "Кино & жизнь" || next == nil || next.Title != "Новости" {
		t.Fatalf("Спас +4 в 21:30 по UTC+7: сейчас %+v, следом %+v", now, next)
	}
	if want := time.Date(2026, 9, 29, 21, 0, 0, 0, utc7); !now.Start.Equal(want) {
		t.Errorf("начало у сдвинутого канала %v, нужно %v", now.Start.In(utc7), want)
	}
	if now, next := g.NowNext("нет-такого", 0, at); now != nil || next != nil {
		t.Errorf("у неизвестного канала нашлись передачи")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	from, to := window()
	if _, err := Parse(strings.NewReader("<html>не телепрограмма"), from, to); err == nil {
		t.Fatal("мусор разобран без ошибки")
	}
}

// sameString — у строк общая память (название передачи хранится один раз).
func sameString(a, b string) bool { return unsafe.StringData(a) == unsafe.StringData(b) }
