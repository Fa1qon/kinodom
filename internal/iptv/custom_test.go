package iptv

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// customModule — модуль с плейлистом: «Мой канал HD» (два потока, ни к чему не привязан) и НТВ.
func customModule(t *testing.T) (*Module, string) {
	t.Helper()
	f := newFakeNet(t)
	m, _ := startModule(t, f)
	waitFor(t, "телепрограмма скачалась", func() bool { return m.Guide() != nil })
	ok := f.srv.URL + "/s/ok.m3u8"
	f.setPlaylist(fmt.Sprintf("#EXTM3U\n#EXTINF:-1,Мой канал HD\n%s?a\n#EXTINF:-1,Мой канал HD\n%s?b\n#EXTINF:-1,НТВ HD\n%s\n", ok, ok, ok))
	if _, err := m.AddPlaylist(context.Background(), PlaylistInput{URL: f.srv.URL + "/pl.m3u"}); err != nil {
		t.Fatal(err)
	}
	m.rebuild(context.Background())
	for _, g := range m.Lineup().Unrecognized {
		if g.Sample == "Мой канал HD" {
			return m, g.Name
		}
	}
	t.Fatalf("нераспознанные: %+v", m.Lineup().Unrecognized)
	return nil, ""
}

// План 14Д: «Новый канал» из «Не распознано» — свой канал с названием, логотипом, категорией и страной;
// потоки группы — у него, группы в «Не распознано» нет, поиск каналов его находит.
func TestCreateCustomChannel(t *testing.T) {
	m, group := customModule(t)
	key, err := m.CreateCustom(context.Background(), CustomInput{Name: "Мой канал", Logo: "https://x.example/logo.png",
		Category: "news", Country: "RU", Group: group})
	if err != nil || key != "my-1" {
		t.Fatalf("ключ %q, %v", key, err)
	}
	l := m.Lineup()
	c := l.ByKey[key]
	if c == nil || c.Name != "Мой канал" || c.Logo != "https://x.example/logo.png" || c.Labels.Category != "news" || c.Labels.Country != "RU" ||
		len(c.Sources)+len(c.Others) != 2 {
		t.Fatalf("канал: %+v", c)
	}
	if slices.ContainsFunc(l.Unrecognized, func(g Group) bool { return g.Name == group }) {
		t.Fatal("группа осталась в «Не распознано»")
	}
	if hits := m.SearchEPG("Мой", 10); !slices.ContainsFunc(hits, func(e EPGChannel) bool { return e.Key == key }) {
		t.Fatalf("поиск каналов: %+v", hits)
	}
	if k2, err := m.CreateCustom(context.Background(), CustomInput{Name: "Второй"}); err != nil || k2 != "my-2" {
		t.Fatalf("второй: %q %v", k2, err)
	}
}

// Название своего канала как у канала телепрограммы — свой ключ, без программы; канал телепрограммы — свой
// (Review Focus 4).
func TestCustomNameLikeEPG(t *testing.T) {
	m, group := customModule(t)
	key, err := m.CreateCustom(context.Background(), CustomInput{Name: "НТВ", Group: group})
	if err != nil {
		t.Fatal(err)
	}
	l := m.Lineup()
	if c := l.ByKey[key]; c == nil || c.EPGID != key || len(c.Sources)+len(c.Others) != 2 {
		t.Fatalf("свой «НТВ»: %+v", c)
	}
	if c := l.ByKey["ntv"]; c == nil || len(c.Sources)+len(c.Others) != 1 {
		t.Fatalf("НТВ телепрограммы: %+v", c)
	}
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	return b.Bytes()
}

// Загруженный логотип — /logo/{ключ} отдаёт его; не картинка или больше 1 МБ — отказ, канала нет (Review Focus 5).
func TestCustomLogoUpload(t *testing.T) {
	m, group := customModule(t)
	pic := pngBytes(t)
	for name, data := range map[string][]byte{"текст": []byte("<html>не картинка</html>"), "2 МБ": append(slices.Clone(pic), make([]byte, 2<<20)...)} {
		var fe *FieldError
		if _, err := m.CreateCustom(context.Background(), CustomInput{Name: "Плохой", LogoData: data}); !errors.As(err, &fe) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(m.customKeys()) != 0 {
		t.Fatalf("после отказов каналы: %v", m.customKeys())
	}
	key, err := m.CreateCustom(context.Background(), CustomInput{Name: "С логотипом", LogoData: pic, Group: group})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.Register(testRouter{mux}, func(w http.ResponseWriter, r *http.Request, src string) { http.Error(w, "из сети: "+src, 500) })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/logo/"+key, nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), pic) {
		t.Fatalf("логотип: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// POST /api/v1/iptv/custom из дома — {key}; пустое название — 400.
func TestCustomRoute(t *testing.T) {
	m, group := customModule(t)
	mux := http.NewServeMux()
	m.Register(testRouter{mux}, nil)
	post := func(body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/v1/iptv/custom", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = fromPhone
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	rec := post(map[string]any{"name": "Мой", "group": group, "category": "news", "country": "RU",
		"logoData": base64.StdEncoding.EncodeToString(pngBytes(t))})
	var out struct{ Key string }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || !strings.HasPrefix(out.Key, "my-") {
		t.Fatalf("создание: %d %s", rec.Code, rec.Body)
	}
	if rec := post(map[string]any{"name": " "}); rec.Code != http.StatusBadRequest {
		t.Fatalf("пустое название: %d", rec.Code)
	}
}
