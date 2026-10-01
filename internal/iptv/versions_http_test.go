package iptv

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// startVersions — модуль с «Первым каналом» в трёх версиях (МСК, МСК+4, МСК−1) и НТВ; пояс каналов —
// UTC+7 (по умолчанию), версия по умолчанию — МСК+4.
func startVersions(t *testing.T) (*Module, client, string) {
	t.Helper()
	f := newFakeNet(t)
	f.epg = epgWithProgrammes()
	m, _ := startModule(t, f)
	waitFor(t, "телепрограмма", func() bool { return m.Guide() != nil })
	ok := f.srv.URL + "/s/ok.m3u8"
	f.setPlaylist(fmt.Sprintf("#EXTM3U\n#EXTINF:-1,Первый канал\n%s?msk\n#EXTINF:-1,Первый канал (+4)\n%s?pl4\n#EXTINF:-1,Первый канал (-1)\n%s?mn1\n#EXTINF:-1,НТВ\n%s?ntv\n", ok, ok, ok, ok))
	if _, err := m.AddPlaylist(context.Background(), PlaylistInput{URL: f.srv.URL + "/pl.m3u"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "версии проверены", func() bool {
		m.rebuild(context.Background())
		f := m.Lineup().Families["pervy"]
		return f != nil && len(f.Versions) == 3
	})
	mux := http.NewServeMux()
	m.Register(testRouter{mux}, func(w http.ResponseWriter, r *http.Request, src string) { fmt.Fprint(w, "логотип "+src) })
	return m, client{t, mux}, ok
}

type versionCard struct {
	ChannelView
	Versions []VersionView `json:"versions"`
	Sources  []SourceView  `json:"sources"`
	Override OverrideView  `json:"override"`
}

// Версии канала в API (спека 11b, 13.2–13.3; Review Focus 5): в списке одна карточка с ключом канала и
// ключом версии по умолчанию; страница — версия по умолчанию или выбранная ?version=; «Смотреть» — по
// ключу версии.
func TestChannelVersionsAPI(t *testing.T) {
	_, c, ok := startVersions(t)
	var resp channelsResp
	if code := c.json("GET", "/api/v1/channels", fromPhone, nil, &resp); code != 200 {
		t.Fatalf("каналы: %d", code)
	}
	var pervy []ChannelView
	for _, ch := range resp.Channels {
		if strings.HasPrefix(ch.Key, "pervy") {
			pervy = append(pervy, ch)
		}
	}
	if len(pervy) != 1 || pervy[0].Key != "pervy" || pervy[0].Version != "pervy-pl4" || pervy[0].VersionLabel != "МСК+4" ||
		pervy[0].Name != "Первый канал" || pervy[0].Number != 1 {
		t.Fatalf("карточка Первого: %+v", pervy)
	}
	var card versionCard
	if code := c.json("GET", "/api/v1/channels/pervy", fromPhone, nil, &card); code != 200 || card.Version != "pervy-pl4" {
		t.Fatalf("страница: %d %+v", code, card.ChannelView)
	}
	var labels []string
	for _, v := range card.Versions {
		labels = append(labels, v.Key+":"+v.Label)
	}
	if got := strings.Join(labels, " "); got != "pervy-mn1:МСК−1 pervy:МСК pervy-pl4:МСК+4" {
		t.Errorf("версии: %s", got)
	}
	if code := c.json("GET", "/api/v1/channels/pervy?version=pervy", fromPhone, nil, &card); code != 200 || card.Version != "pervy" ||
		card.Key != "pervy" || len(card.Sources) != 1 || card.Sources[0].URL != ok+"?msk" {
		t.Errorf("МСК явно: %d %+v %+v", code, card.ChannelView, card.Sources)
	}
	if code := c.json("GET", "/api/v1/channels/pervy?version=ntv", fromPhone, nil, nil); code != 404 {
		t.Errorf("чужая версия: %d", code)
	}
	var play struct {
		Items []PlayItem `json:"items"`
	}
	if code := c.json("GET", "/api/v1/channels/pervy-mn1/play", fromPhone, nil, &play); code != 200 || len(play.Items) != 1 || play.Items[0].URL != ok+"?mn1" {
		t.Errorf("Смотреть МСК−1: %d %+v", code, play.Items)
	}
}

// Правки канала и версии (спека 11b, 13.1): «Скрыть канал» и метки с ключа версии — на канал, основной
// источник — на версию; ★ — у канала.
func TestOverrideFamilyFields(t *testing.T) {
	m, c, _ := startVersions(t)
	if code := c.json("PUT", "/api/v1/channels/pervy-pl4", fromPC, map[string]any{"hidden": true, "category": "news"}, nil); code != 204 {
		t.Fatalf("правка: %d", code)
	}
	if o := m.Override("pervy"); !o.Hidden || o.Category == nil || *o.Category != "news" {
		t.Errorf("правка канала: %+v", o)
	}
	if o := m.Override("pervy-pl4"); !o.empty() {
		t.Errorf("у версии лишнее: %+v", o)
	}
	var card versionCard
	c.json("GET", "/api/v1/channels/pervy?version=pervy-pl4", fromPhone, nil, &card)
	if !card.Override.Hidden || card.Override.Category == nil || *card.Override.Category != "news" || len(card.Sources) != 1 {
		t.Fatalf("страница версии: %+v %+v", card.Override, card.Sources)
	}
	if code := c.json("PUT", "/api/v1/channels/pervy-pl4", fromPC, map[string]any{"pinnedSource": card.Sources[0].ID}, nil); code != 204 {
		t.Fatalf("основной: %d", code)
	}
	if m.Override("pervy-pl4").PinnedURL == "" || m.Override("pervy").PinnedURL != "" {
		t.Errorf("основной источник — у версии: %+v %+v", m.Override("pervy-pl4"), m.Override("pervy"))
	}
	if code := c.json("PUT", "/api/v1/channels/pervy", fromPC, map[string]any{"hidden": false}, nil); code != 204 {
		t.Fatalf("вернуть: %d", code)
	}
	if code := c.json("PUT", "/api/v1/iptv/favorites/pervy", fromPhone, nil, nil); code != 204 {
		t.Fatalf("★: %d", code)
	}
	var resp channelsResp
	c.json("GET", "/api/v1/channels", fromPhone, nil, &resp)
	if len(resp.Channels) == 0 || resp.Channels[0].Key != "pervy" || resp.Channels[0].Block != "favorite" || resp.Channels[0].Version != "pervy-pl4" {
		t.Errorf("избранное: %s", keys(resp.Channels))
	}
}
