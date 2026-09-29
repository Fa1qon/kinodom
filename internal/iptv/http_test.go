package iptv

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kinodom/internal/httpx"
)

// testRouter — маршруты модуля на ServeMux; «дом» — как у api.Server.
type testRouter struct{ mux *http.ServeMux }

func (t testRouter) Handle(p, _ string, h http.Handler) { t.mux.Handle(p, h) }
func (t testRouter) HandleHome(p, _ string, h http.Handler) {
	t.mux.Handle(p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpx.FromHome(r) {
			httpx.WriteError(w, http.StatusForbidden, "Изменить можно только из домашней сети")
			return
		}
		h.ServeHTTP(w, r)
	}))
}

// Адреса устройств.
const (
	fromPC       = "127.0.0.1:5000"
	fromPhone    = "192.168.0.50:5000"
	fromTV       = "192.168.0.60:5000"
	fromOutside  = "8.8.8.8:5000"
	testHostPort = "127.0.0.1:8090"
)

type client struct {
	t   *testing.T
	mux *http.ServeMux
}

func (c client) do(method, path, from string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rd)
	r.RemoteAddr = from
	r.Host = testHostPort
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c.mux.ServeHTTP(w, r)
	return w
}

func (c client) json(method, path, from string, body any, out any) int {
	c.t.Helper()
	w := c.do(method, path, from, body)
	if out != nil && w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			c.t.Fatalf("%s %s: %v\n%s", method, path, err, w.Body.String())
		}
	}
	return w.Code
}

// epgWithProgrammes — справочник тестов и передачи НТВ вокруг текущего момента.
func epgWithProgrammes() string {
	msk := time.FixedZone("MSK", 3*3600)
	now := time.Now().In(msk)
	f := func(t time.Time) string { return t.Format("20060102150405 -0700") }
	progs := fmt.Sprintf(`<programme start="%s" stop="%s" channel="ntv"><title>Сегодня</title></programme>
<programme start="%s" stop="%s" channel="ntv"><title>Следствие</title></programme>
</tv>`, f(now.Add(-30*time.Minute)), f(now.Add(30*time.Minute)), f(now.Add(30*time.Minute)), f(now.Add(90*time.Minute)))
	return strings.Replace(testEPG, "</tv>", progs, 1)
}

func startHTTP(t *testing.T) (*Module, client, *fakeNet) {
	t.Helper()
	f := newFakeNet(t)
	f.epg = epgWithProgrammes()
	m, _ := startModule(t, f)
	waitFor(t, "телепрограмма", func() bool { return m.Guide() != nil })
	ok := f.srv.URL + "/s/ok.m3u8"
	f.setPlaylist(fmt.Sprintf("#EXTM3U\n#EXTINF:-1,НТВ HD\n#EXTVLCOPT:http-user-agent=WINK/1\n%s\n#EXTINF:-1,Неизвестный\n%s?u\n#EXTINF:-1,BBC News\n%s?b\n", ok, ok, ok))
	if _, err := m.AddPlaylist(context.Background(), PlaylistInput{URL: f.srv.URL + "/pl.m3u"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "каналы проверены", func() bool {
		m.rebuild(context.Background())
		l := m.Lineup()
		return l.ByKey["ntv"] != nil && l.ByKey["ntv"].Offered() && l.ByKey["bbc"] != nil && l.ByKey["bbc"].Offered()
	})
	mux := http.NewServeMux()
	m.Register(testRouter{mux}, func(w http.ResponseWriter, r *http.Request, src string) { fmt.Fprint(w, "логотип "+src) })
	return m, client{t, mux}, f
}

type channelsResp struct {
	Channels  []ChannelView `json:"channels"`
	Countries []Facet       `json:"countries"`
}

func keys(cs []ChannelView) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Key+":"+c.Block)
	}
	return strings.Join(out, " ")
}

// Избранное у каждого устройства своё (спека этапа 8, критерий 10); менять — из домашней сети.
func TestHTTPChannelsAndFavorites(t *testing.T) {
	_, c, _ := startHTTP(t)
	var resp channelsResp
	if code := c.json("GET", "/api/v1/channels", fromPhone, nil, &resp); code != 200 || keys(resp.Channels) != "ntv:federal bbc:" {
		t.Fatalf("каналы: %d %s", code, keys(resp.Channels))
	}
	ntv := resp.Channels[0]
	if ntv.Number != 4 || ntv.Now == nil || ntv.Now.Title != "Сегодня" || ntv.Next == nil || ntv.Next.Title != "Следствие" ||
		ntv.CountryName != "Россия" || ntv.CategoryName != "Общие" || ntv.Grade == "" || ntv.Logo != "" {
		t.Errorf("НТВ: %+v", ntv)
	}
	if len(resp.Countries) != 2 || resp.Countries[0].ID != "GB" && resp.Countries[0].ID != "RU" {
		t.Errorf("страны: %+v", resp.Countries)
	}
	if code := c.json("PUT", "/api/v1/iptv/favorites", fromPhone, map[string]any{"keys": []string{"bbc"}}, nil); code != 204 {
		t.Fatalf("избранное: %d", code)
	}
	c.json("GET", "/api/v1/channels", fromPhone, nil, &resp)
	if got := keys(resp.Channels); got != "bbc:favorite ntv:federal" {
		t.Errorf("телефон: %s", got)
	}
	c.json("GET", "/api/v1/channels", fromTV, nil, &resp)
	if got := keys(resp.Channels); got != "ntv:federal bbc:" {
		t.Errorf("телевизор (своё избранное пусто): %s", got)
	}
	if code := c.json("PUT", "/api/v1/iptv/favorites", fromOutside, map[string]any{"keys": []string{"ntv"}}, nil); code != 403 {
		t.Errorf("избранное снаружи: %d", code)
	}
}

// «Смотреть»: источники с заголовками, .m3u8 для VLC; ссылка kinodom:// — только этому ПК.
func TestHTTPPlay(t *testing.T) {
	_, c, f := startHTTP(t)
	var play struct {
		Items     []PlayItem `json:"items"`
		M3UURL    string     `json:"m3uUrl"`
		LaunchURL *string    `json:"launchUrl"`
	}
	if code := c.json("GET", "/api/v1/channels/ntv/play", fromPC, nil, &play); code != 200 {
		t.Fatalf("play: %d", code)
	}
	if len(play.Items) != 1 || play.Items[0].Headers.UserAgent != "WINK/1" || play.M3UURL != "http://"+testHostPort+"/m3u/channel/ntv.m3u8" ||
		play.LaunchURL == nil || !strings.Contains(*play.LaunchURL, "m3u%2Fchannel%2Fntv.m3u8") {
		t.Errorf("play с ПК: %+v", play)
	}
	c.json("GET", "/api/v1/channels/ntv/play", fromPhone, nil, &play)
	if play.LaunchURL != nil {
		t.Errorf("телефону отдана ссылка kinodom://")
	}
	w := c.do("GET", "/m3u/channel/ntv.m3u8", fromPhone, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "#EXTVLCOPT:http-user-agent=WINK/1\n"+f.srv.URL+"/s/ok.m3u8\n") {
		t.Errorf(".m3u8: %d\n%s", w.Code, w.Body.String())
	}
	if w := c.do("GET", "/m3u/channel/nope.m3u8", fromPhone, nil); w.Code != 404 {
		t.Errorf("нет канала: %d", w.Code)
	}
}

// Правки канала и назначения из «Не распознано» (спека этапа 8, разделы 5.3, 5.4, 5.6).
func TestHTTPEdits(t *testing.T) {
	m, c, _ := startHTTP(t)
	if code := c.json("PUT", "/api/v1/channels/ntv", fromPhone, map[string]any{"category": "news", "languages": []string{"rus", "eng"}}, nil); code != 204 {
		t.Fatalf("правка: %d", code)
	}
	var card struct {
		ChannelView
		Sources  []SourceView `json:"sources"`
		Override OverrideView `json:"override"`
	}
	c.json("GET", "/api/v1/channels/ntv", fromPhone, nil, &card)
	if card.CategoryName != "Новости" || strings.Join(card.LanguageNames, ",") != "русский,английский" || card.Override.Category == nil ||
		len(card.Sources) != 1 || len(card.Sources[0].Playlists) != 1 || card.Sources[0].State != StateAlive {
		t.Errorf("карточка: %+v", card)
	}
	c.json("PUT", "/api/v1/channels/ntv", fromPhone, map[string]any{"category": nil, "hidden": true}, nil)
	c.json("GET", "/api/v1/channels/ntv", fromPhone, nil, &card)
	if card.Override.Category != nil || !card.Override.Hidden || card.CategoryName != "Общие" {
		t.Errorf("снятие правки и скрытие: %+v", card.Override)
	}
	for _, bad := range []map[string]any{{"category": "cars"}, {"country": "russia"}, {"languages": []string{"русский"}}, {"pinnedSource": 999}} {
		if code := c.json("PUT", "/api/v1/channels/ntv", fromPhone, bad, nil); code != 400 && code != 404 {
			t.Errorf("%v: %d", bad, code)
		}
	}
	var un struct {
		Items []UnrecognizedView `json:"items"`
		Total int                `json:"total"`
	}
	c.json("GET", "/api/v1/iptv/unrecognized?q=неизв", fromPhone, nil, &un)
	if un.Total != 1 || un.Items[0].Name != "неизвестный" || un.Items[0].Streams != 1 {
		t.Fatalf("не распознано: %+v", un)
	}
	var found struct {
		Items []EPGChannel `json:"items"`
	}
	c.json("GET", "/api/v1/iptv/epg-channels?q=перв", fromPhone, nil, &found)
	if len(found.Items) < 3 || found.Items[0].Key != "pervy" {
		t.Errorf("поиск канала: %+v", found.Items)
	}
	if code := c.json("PUT", "/api/v1/iptv/names", fromPhone, map[string]any{"name": "Неизвестный HD", "channel": "kino1"}, nil); code != 204 {
		t.Fatalf("назначение: %d", code)
	}
	if l := m.Lineup(); l.ByKey["kino1"] == nil || len(l.Unrecognized) != 0 {
		t.Errorf("после назначения: kino1 %v, не распознано %d", l.ByKey["kino1"], len(l.Unrecognized))
	}
	if code := c.json("PUT", "/api/v1/iptv/names", fromPhone, map[string]any{"name": "x", "channel": "нет-такого"}, nil); code != 404 {
		t.Errorf("назначение несуществующему каналу: %d", code)
	}
	if code := c.json("PUT", "/api/v1/iptv/names", fromOutside, map[string]any{"name": "x", "hidden": true}, nil); code != 403 {
		t.Errorf("снаружи: %d", code)
	}
}

// Плейлисты: добавить файлом, переименовать, удалить; мусор — 400.
func TestHTTPPlaylists(t *testing.T) {
	_, c, f := startHTTP(t)
	data := base64.StdEncoding.EncodeToString([]byte("#EXTM3U\n#EXTINF:-1,Спас\n" + f.srv.URL + "/s/ok.m3u8?spas\n"))
	var res PlaylistResult
	if code := c.json("POST", "/api/v1/iptv/playlists", fromPhone, map[string]any{"name": "Файл", "data": data}, &res); code != 201 || res.Recognized != 1 {
		t.Fatalf("добавление файлом: %d %+v", code, res)
	}
	bad := base64.StdEncoding.EncodeToString([]byte("<html>"))
	if code := c.json("POST", "/api/v1/iptv/playlists", fromPhone, map[string]any{"data": bad}, nil); code != 400 {
		t.Errorf("мусор: %d", code)
	}
	if code := c.json("PUT", fmt.Sprintf("/api/v1/iptv/playlists/%d", res.ID), fromPhone, map[string]any{"name": "Спас", "limited": true}, nil); code != 200 {
		t.Errorf("переименование: %d", code)
	}
	var list PlaylistsView
	c.json("GET", "/api/v1/iptv/playlists", fromPhone, nil, &list)
	if len(list.Items) != 2 || list.Items[1].Name != "Спас" || !list.Items[1].Limited || list.Items[0].Entries != 3 || list.EPG.Channels == 0 {
		t.Errorf("плейлисты: %+v", list)
	}
	if code := c.json("POST", fmt.Sprintf("/api/v1/iptv/playlists/%d/refresh", res.ID), fromPhone, map[string]any{}, nil); code != 409 {
		t.Errorf("обновить файл: %d", code)
	}
	if code := c.json("DELETE", fmt.Sprintf("/api/v1/iptv/playlists/%d", res.ID), fromPhone, nil, nil); code != 204 {
		t.Errorf("удаление: %d", code)
	}
	if code := c.json("DELETE", fmt.Sprintf("/api/v1/iptv/playlists/%d", res.ID), fromPhone, nil, nil); code != 404 {
		t.Errorf("повторное удаление: %d", code)
	}
	if code := c.json("POST", "/api/v1/iptv/probe", fromPhone, map[string]any{"channel": "нет"}, nil); code != 404 {
		t.Errorf("проверка несуществующего: %d", code)
	}
}

// Избранное — как сохранено, и каналы без живого источника тоже; ★ добавляет и убирает по одному
// каналу и не трогает остальные (финальное ревью этапа 8).
func TestHTTPFavoritesKeepsOffline(t *testing.T) {
	_, c, _ := startHTTP(t)
	if code := c.json("PUT", "/api/v1/iptv/favorites", fromPhone, map[string]any{"keys": []string{"pervy", "bbc"}}, nil); code != 204 {
		t.Fatalf("список: %d", code)
	}
	if code := c.json("PUT", "/api/v1/iptv/favorites/ntv", fromPhone, map[string]any{}, nil); code != 204 {
		t.Fatalf("добавить: %d", code)
	}
	if code := c.json("DELETE", "/api/v1/iptv/favorites/bbc", fromPhone, nil, nil); code != 204 {
		t.Fatalf("убрать: %d", code)
	}
	var got struct {
		Keys []string `json:"keys"`
	}
	c.json("GET", "/api/v1/iptv/favorites", fromPhone, nil, &got)
	if strings.Join(got.Keys, ",") != "pervy,ntv" { // pervy без живого источника — на месте
		t.Errorf("избранное: %v", got.Keys)
	}
	c.json("GET", "/api/v1/iptv/favorites", fromTV, nil, &got)
	if len(got.Keys) != 0 {
		t.Errorf("у телевизора: %v", got.Keys)
	}
	if code := c.json("PUT", "/api/v1/iptv/favorites/ntv", fromOutside, map[string]any{}, nil); code != 403 {
		t.Errorf("снаружи: %d", code)
	}
}
