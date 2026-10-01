package iptv

// Финальное ревью 11b-Е: старые правки и избранное на ключах версий (Review Focus 2) — через API, как это делает пульт.

import (
	"context"
	"testing"
)

// ★ стояла на версии «pervy-pl4» (до 11b-Е — так пульт её и ставил у заказчика в UTC+7). Пульт снимает ★ по
// ключу канала (toggleFavorite(c.favorite, c.key)) — снять её нельзя.
func TestFavoriteOnOldVersionKeyRemovable(t *testing.T) {
	_, c, _ := startVersions(t)
	if code := c.json("PUT", "/api/v1/iptv/favorites/pervy-pl4", fromPhone, nil, nil); code != 204 {
		t.Fatalf("★ на версии: %d", code)
	}
	var resp channelsResp
	c.json("GET", "/api/v1/channels", fromPhone, nil, &resp)
	if len(resp.Channels) == 0 || resp.Channels[0].Key != "pervy" || !resp.Channels[0].Favorite {
		t.Fatalf("старая ★ не видна: %s", keys(resp.Channels))
	}
	if code := c.json("DELETE", "/api/v1/iptv/favorites/pervy", fromPhone, nil, nil); code != 204 {
		t.Fatalf("снять ★: %d", code)
	}
	c.json("GET", "/api/v1/channels", fromPhone, nil, &resp)
	for _, ch := range resp.Channels {
		if ch.Key == "pervy" && ch.Favorite {
			t.Errorf("★ не снимается: после DELETE /iptv/favorites/pervy канал всё ещё в избранном (%s)", keys(resp.Channels))
		}
	}
}

// Метки правились у версии (до 11b-Е): «Как было» на странице канала не снимает такую правку.
func TestResetLabelsOnOldVersionKey(t *testing.T) {
	m, c, _ := startVersions(t)
	news := "news"
	if err := m.SetOverride(context.Background(), "pervy-pl4", Override{Category: &news}); err != nil {
		t.Fatal(err)
	}
	var card versionCard
	c.json("GET", "/api/v1/channels/pervy", fromPhone, nil, &card)
	if card.Version != "pervy-pl4" || card.Override.Category == nil || *card.Override.Category != "news" {
		t.Fatalf("старая правка меток не видна: %+v %+v", card.ChannelView, card.Override)
	}
	// «Как было» (channel-settings.js): PUT /channels/<версия> {category:null, country:null, languages:null}.
	if code := c.json("PUT", "/api/v1/channels/pervy-pl4", fromPC, map[string]any{"category": nil, "country": nil, "languages": nil}, nil); code != 204 {
		t.Fatalf("Как было: %d", code)
	}
	c.json("GET", "/api/v1/channels/pervy", fromPhone, nil, &card)
	if card.Override.Category != nil {
		t.Errorf("«Как было» не сработало: правка %+v, категория карточки %q", card.Override, card.Category)
	}
}

// Метки правились у версии: пульт шлёт только изменённое поле (labelPatch) — остальная старая правка теряется.
func TestPartialLabelEditKeepsOldVersionLabels(t *testing.T) {
	m, c, _ := startVersions(t)
	news := "news"
	if err := m.SetOverride(context.Background(), "pervy-pl4", Override{Category: &news}); err != nil {
		t.Fatal(err)
	}
	if code := c.json("PUT", "/api/v1/channels/pervy-pl4", fromPC, map[string]any{"languages": []string{"rus"}}, nil); code != 204 {
		t.Fatalf("языки: %d", code)
	}
	var card versionCard
	c.json("GET", "/api/v1/channels/pervy", fromPhone, nil, &card)
	if card.Override.Category == nil || *card.Override.Category != "news" || card.Category != "news" {
		t.Errorf("ручная категория потеряна после правки языков: правка %+v, категория карточки %q", card.Override, card.Category)
	}
}

// Все источники версии МСК+4 скрыты вручную («Скрыть источник» — единственный способ у версии), у МСК есть
// рабочий: версия по умолчанию всё равно МСК+4 — плееру уходят скрытые источники «запасными».
func TestDefaultIgnoresVersionWithOnlyHiddenSources(t *testing.T) {
	p := testPool(src{name: "Первый канал"}, src{name: "Первый канал +4"})
	pl4 := p.streams[2].URL
	p.overrides["pervy-pl4"] = Override{HiddenURLs: []string{pl4}}
	l := buildTest(t, p, Hidden{}, 4)
	if f := l.Families["pervy"]; f.Default == nil || f.Default.Key != "pervy" {
		t.Errorf("версия по умолчанию %q: у неё все источники скрыты, а у МСК есть рабочий", f.Default.Key)
	}
}

// Имя канала без телепрограммы — имя версии без сдвига; дефис внутри названия («Россия-1») — не сдвиг
// (финальное ревью 11b-Е).
func TestFamilyNameWithoutGuide(t *testing.T) {
	ix := &epgIndex{byNorm: map[string][]string{}}
	for name, want := range map[string]string{"Россия-1": "Россия-1", "Спас −1": "Спас", "Первый канал +4": "Первый канал", "Россия 1 (-1)": "Россия 1"} {
		if got := familyName(&Family{Key: "x", Default: &Channel{Name: name}}, ix); got != want {
			t.Errorf("%q: %q, нужно %q", name, got, want)
		}
	}
}
