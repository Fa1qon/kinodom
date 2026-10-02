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
	"os"
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
	// SVG браузер выберет по accept="image/*": отказ должен называть, какие картинки подходят (ревью 14Д, п. 11).
	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)
	if _, err := m.CreateCustom(context.Background(), CustomInput{Name: "SVG", LogoData: svg}); err == nil || !strings.Contains(err.Error(), "PNG") {
		t.Fatalf("SVG: %v", err)
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
	// Логотип почти в 1 МБ: в base64 запрос больше 1 МБ — всё равно принят.
	big := append(pngBytes(t), make([]byte, 900<<10)...)
	if rec := post(map[string]any{"name": "Большой", "logoData": base64.StdEncoding.EncodeToString(big)}); rec.Code != 200 {
		t.Fatalf("логотип 900 КБ: %d %s", rec.Code, rec.Body)
	}
	// Фото с телефона (1,5 МБ): запрос больше предела — отказ словами пульта (ревью 14Д, п. 10).
	huge := append(pngBytes(t), make([]byte, 1536<<10)...)
	if rec := post(map[string]any{"name": "Фото", "logoData": base64.StdEncoding.EncodeToString(huge)}); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "больше 1 МБ") {
		t.Fatalf("логотип 1,5 МБ: %d %s", rec.Code, rec.Body)
	}
}

// План 14Д: у нераспознанного названия — поток для «Смотреть» (живой, если есть) и его вид.
func TestUnrecognizedWatch(t *testing.T) {
	m, group := customModule(t)
	mux := http.NewServeMux()
	m.Register(testRouter{mux}, nil)
	req := httptest.NewRequest("GET", "/api/v1/iptv/unrecognized", nil)
	req.RemoteAddr = fromPhone
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out struct {
		Items []struct {
			Name  string `json:"name"`
			Watch int64  `json:"watch"`
			URL   string `json:"watchUrl"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, it := range out.Items {
		if it.Name == group {
			if it.Watch == 0 || !strings.Contains(it.URL, "/s/ok.m3u8") {
				t.Fatalf("поток для «Смотреть»: %+v", it)
			}
			return
		}
	}
	t.Fatalf("группы нет: %s", rec.Body)
}

// Ревью 14Д, п. 1: «upload:» в tvg-logo чужого плейлиста не открывает файлы сервера — загруженный логотип
// отдаётся только своему каналу, которому его загрузили.
func TestLogoUploadPrefixFromPlaylist(t *testing.T) {
	f := newFakeNet(t)
	m, _ := startModule(t, f)
	waitFor(t, "телепрограмма скачалась", func() bool { return m.Guide() != nil })
	ok := f.srv.URL + "/s/ok.m3u8"
	f.setPlaylist(fmt.Sprintf("#EXTM3U\n#EXTINF:-1 tvg-logo=\"upload:../k.db\",Чужой канал\n%s?a\n#EXTINF:-1 tvg-logo=\"upload:../k.db\",НТВ HD\n%s\n", ok, ok))
	if _, err := m.AddPlaylist(context.Background(), PlaylistInput{URL: f.srv.URL + "/pl.m3u"}); err != nil {
		t.Fatal(err)
	}
	m.rebuild(context.Background())
	group := ""
	for _, g := range m.Lineup().Unrecognized {
		if g.Sample == "Чужой канал" {
			group = g.Name
		}
	}
	if group == "" {
		t.Fatalf("нераспознанные: %+v", m.Lineup().Unrecognized)
	}
	key, err := m.CreateCustom(context.Background(), CustomInput{Name: "Свой", Group: group})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.Register(testRouter{mux}, func(w http.ResponseWriter, r *http.Request, src string) { http.Error(w, "из сети: "+src, 500) })
	// Значок телепрограммы или запись, прошедшие мимо проверки адреса, — отдача файла всё равно только по ключу
	// своего канала, которому его загрузили.
	for _, k := range []string{key, "ntv"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/logo/"+k, nil))
		if rec.Code == 200 || strings.Contains(rec.Body.String(), "SQLite") {
			t.Errorf("/logo/%s: %d, %.40q", k, rec.Code, rec.Body.String())
		}
	}
	m.mu.Lock()
	m.pool.custom[key] = Custom{Key: key, Name: "Свой", Logo: "upload:../k.db"}
	m.mu.Unlock()
	m.rebuild(context.Background())
	for _, k := range []string{key} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/logo/"+k, nil))
		if rec.Code == 200 || strings.Contains(rec.Body.String(), "SQLite") {
			t.Errorf("/logo/%s: %d, %.40q", k, rec.Code, rec.Body.String())
		}
	}
}

// Ревью 14Д, п. 5: второй «Создать канал» по той же группе (двойное OK на пульте) — отказ, пустого канала нет.
func TestCreateCustomTwice(t *testing.T) {
	m, group := customModule(t)
	if _, err := m.CreateCustom(context.Background(), CustomInput{Name: "Мой канал", Group: group}); err != nil {
		t.Fatal(err)
	}
	var fe *FieldError
	if _, err := m.CreateCustom(context.Background(), CustomInput{Name: "Мой канал", Group: group}); !errors.As(err, &fe) {
		t.Fatalf("второй раз: %v", err)
	}
	if keys := m.customKeys(); len(keys) != 1 {
		t.Fatalf("каналы: %v", keys)
	}
}

// Ревью 14Д, п. 5: «Удалить канал» — своего канала нет ни сейчас, ни после перезапуска; его потоки снова в
// «Не распознано», загруженный логотип стёрт; номер не достаётся новому каналу.
func TestDeleteCustomChannel(t *testing.T) {
	m, group := customModule(t)
	key, err := m.CreateCustom(context.Background(), CustomInput{Name: "Мой канал", LogoData: pngBytes(t), Category: "news", Group: group})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.Register(testRouter{mux}, nil)
	del := func(k string) int {
		req := httptest.NewRequest("DELETE", "/api/v1/iptv/custom/"+k, nil)
		req.RemoteAddr = fromPhone
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := del(key); code != http.StatusNoContent {
		t.Fatalf("удаление: %d", code)
	}
	l := m.Lineup()
	if l.ByKey[key] != nil {
		t.Fatal("канал остался")
	}
	if !slices.ContainsFunc(l.Unrecognized, func(g Group) bool { return g.Name == group }) {
		t.Fatalf("потоки не вернулись в «Не распознано»: %+v", l.Unrecognized)
	}
	if _, err := os.Stat(m.customLogoPath(key)); !os.IsNotExist(err) {
		t.Fatalf("логотип: %v", err)
	}
	p, err := m.d.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.custom[key]; ok || p.nameRules[group].Channel == key || p.overrides[key].Category != nil {
		t.Fatalf("после перезапуска: свой %v, правило %+v, метки %+v", ok, p.nameRules[group], p.overrides[key])
	}
	if k2, err := m.CreateCustom(context.Background(), CustomInput{Name: "Новый"}); err != nil || k2 == key {
		t.Fatalf("новый канал: %q %v", k2, err)
	}
	if code := del("my-99"); code != http.StatusNotFound {
		t.Fatalf("неизвестный: %d", code)
	}
}

// Ревью 14Д, п. 16: свой канал в поиске помечен — не спутать с одноимённым каналом телепрограммы.
func TestSearchMarksOwn(t *testing.T) {
	m, group := customModule(t)
	key, err := m.CreateCustom(context.Background(), CustomInput{Name: "НТВ", Group: group})
	if err != nil {
		t.Fatal(err)
	}
	var own, epg bool
	for _, h := range m.SearchEPG("НТВ", 10) {
		own = own || (h.Key == key && h.Own)
		epg = epg || (h.Key == "ntv" && !h.Own)
	}
	if !own || !epg {
		t.Fatalf("%+v", m.SearchEPG("НТВ", 10))
	}
}
