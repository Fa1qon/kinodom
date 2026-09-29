package settings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"kinodom/internal/api"
	"kinodom/internal/store"
	"kinodom/internal/supervisor"
)

var ctx = context.Background()

var defs = Defaults{DownloadsDir: `C:\Kinodom`, Sections: "rutracker:2110,rutor:12"}

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "kinodom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func load(t *testing.T, db *store.DB, overrides map[string]string) Values {
	t.Helper()
	v, err := Load(ctx, db, defs, overrides)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func patch(t *testing.T, js string) Patch {
	t.Helper()
	var p Patch
	if err := json.Unmarshal([]byte(js), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// Пустая база — значения по умолчанию (основная спека, раздел 15); overrides — поверх базы.
func TestLoadDefaultsAndOverrides(t *testing.T) {
	db := openDB(t)
	v := load(t, db, nil)
	if v.KeepDays != 14 || v.KeepBehind != 1 || v.MinFreeGB != 20 || v.UploadMBps == nil || *v.UploadMBps != 2 || v.Player != "auto" ||
		v.DownloadsDir != `C:\Kinodom` || v.Sections != "rutracker:2110,rutor:12" || v.Proxy != "" {
		t.Fatalf("по умолчанию: %+v", v)
	}
	db.SetSetting(ctx, KeyMinFreeGB, "0")
	db.SetSetting(ctx, KeyKeepBehind, "0")
	db.SetSetting(ctx, KeyKeepDays, "abc")
	db.SetSetting(ctx, KeyUploadLimit, "")
	db.SetSetting(ctx, KeyPlayer, "winamp")
	v = load(t, db, map[string]string{KeyRutrackerLogin: "user"})
	if v.MinFreeGB != 0 || v.KeepBehind != 0 || v.KeepDays != 14 || v.UploadMBps != nil || v.Player != "auto" || v.RutrackerLogin != "user" {
		t.Fatalf("из базы: %+v", v)
	}
	db.SetSetting(ctx, KeyUploadLimit, "0")
	if v = load(t, db, nil); v.UploadMBps == nil || *v.UploadMBps != 0 {
		t.Fatalf("0 — не раздавать: %+v", v.UploadMBps)
	}
}

// Сколько серий позади просмотренной оставлять при нехватке места: 0 разрешён, в ответе пульту —
// storage.keepBehind.
func TestKeepBehindField(t *testing.T) {
	v := load(t, openDB(t), nil)
	n, err := v.With(patch(t, `{"storage":{"keepBehind":0}}`))
	if err != nil || n.KeepBehind != 0 || n.View().Storage.KeepBehind != 0 {
		t.Fatalf("0: %+v, %v", n, err)
	}
	n, err = v.With(patch(t, `{"storage":{"keepBehind":3}}`))
	if b, _ := json.Marshal(n.View()); err != nil || !strings.Contains(string(b), `"keepBehind":3`) {
		t.Fatalf("3: %s, %v", b, err)
	}
}

// Пароли и ключ пульт не получает — только «задан» (спека этапа 7, раздел 5.1).
func TestViewHidesSecrets(t *testing.T) {
	v := Values{RutrackerLogin: "user", RutrackerPassword: "SECRET-1", KinopoiskKey: "SECRET-2",
		Proxy: "http://proxyuser:SECRET-3@192.168.1.20:3128", Sections: "rutracker:2110,rutracker:46+,rutor:12", Player: "vlc"}
	b, _ := json.Marshal(v.View())
	if strings.Contains(string(b), "SECRET") {
		t.Fatalf("секрет в ответе: %s", b)
	}
	got := v.View()
	if !got.Rutracker.PasswordSet || !got.Kinopoisk.KeySet || !got.Proxy.PasswordSet ||
		got.Proxy.Type != "http" || got.Proxy.Address != "192.168.1.20:3128" || got.Proxy.Login != "proxyuser" {
		t.Fatalf("вид: %+v", got)
	}
	if s := got.Catalog.Sections; len(s["rutracker"]) != 2 || s["rutracker"][1] != "46+" || s["rutor"][0] != "12" {
		t.Fatalf("разделы: %+v", s)
	}
}

// Неверное поле — отказ с понятным текстом, прежние значения не меняются.
func TestPatchRejectsBadFields(t *testing.T) {
	v := load(t, openDB(t), nil)
	cases := map[string]string{
		`{"storage":{"keepDays":0}}`:                                 "Хранить, дней",
		`{"storage":{"minFreeGB":-1}}`:                               "Запас места",
		`{"storage":{"keepBehind":-1}}`:                              "Серий позади",
		`{"storage":{"uploadLimitMBps":-2}}`:                         "Раздача",
		`{"storage":{"downloadsDir":"Kinodom"}}`:                     "полный путь",
		`{"player":"winamp"}`:                                        "Плеер",
		`{"proxy":{"type":"ftp","address":"h:1"}}`:                   "тип",
		`{"proxy":{"type":"http","address":"192.168.1.20"}}`:         "хост:порт",
		`{"proxy":{"type":"http","address":"h:99999"}}`:              "порт",
		`{"proxy":{"type":"socks5","address":"h:1","password":"p"}}`: "пароль без логина",
		`{"catalog":{"sections":{"rutracker":[]}}}`:                  "хотя бы один",
		`{"catalog":{"sections":{"rutor":["1,2"]}}}`:                 "не понят",
	}
	for js, want := range cases {
		n, err := v.With(patch(t, js))
		var fe *FieldError
		if !errors.As(err, &fe) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: ошибка %v, ждали «%s»", js, err, want)
		}
		if n != v {
			t.Errorf("%s: значения изменились при ошибке", js)
		}
	}
}

// Прокси из полей пульта: не присланное поле — прежнее, пароль "" — стереть, «Нет» — без прокси.
func TestProxyFields(t *testing.T) {
	v := Values{Proxy: "http://u:p@192.168.1.20:3128"}
	steps := []struct{ js, want string }{
		{`{"proxy":{"address":"10.0.0.1:8080"}}`, "http://u:p@10.0.0.1:8080"},
		{`{"proxy":{"type":"socks5"}}`, "socks5://u:p@10.0.0.1:8080"},
		{`{"proxy":{"password":""}}`, "socks5://u@10.0.0.1:8080"},
		{`{"proxy":{"login":"вася","password":"п@ро:ль"}}`, "socks5://%D0%B2%D0%B0%D1%81%D1%8F:%D0%BF%40%D1%80%D0%BE%3A%D0%BB%D1%8C@10.0.0.1:8080"},
		{`{"proxy":{"type":"none"}}`, ""},
	}
	for _, s := range steps {
		n, err := v.With(patch(t, s.js))
		if err != nil || n.Proxy != s.want {
			t.Fatalf("%s: %q, %v; ждали %q", s.js, n.Proxy, err, s.want)
		}
		v = n
	}
	v = Values{}
	if n, err := v.With(patch(t, `{"proxy":{"type":"http","address":"proxy.lan:3128","login":"u","password":"p"}}`)); err != nil ||
		n.View().Proxy != (ProxyView{Type: "http", Address: "proxy.lan:3128", Login: "u", PasswordSet: true}) {
		t.Fatalf("новый прокси: %+v, %v", n.View().Proxy, err)
	}
}

// Лимит отдачи: null — без ограничения, поля нет — не менять, 0 — не раздавать.
func TestUploadLimitNullVersusAbsent(t *testing.T) {
	two := 2.0
	v := Values{UploadMBps: &two}
	n, _ := v.With(patch(t, `{"storage":{"keepDays":3}}`))
	if n.UploadMBps == nil || *n.UploadMBps != 2 {
		t.Fatalf("поля нет — не менять: %v", n.UploadMBps)
	}
	n, _ = v.With(patch(t, `{"storage":{"uploadLimitMBps":null}}`))
	if n.UploadMBps != nil {
		t.Fatalf("null — без ограничения: %v", *n.UploadMBps)
	}
	n, _ = v.With(patch(t, `{"storage":{"uploadLimitMBps":0}}`))
	if n.UploadMBps == nil || *n.UploadMBps != 0 {
		t.Fatalf("0 — не раздавать: %v", n.UploadMBps)
	}
}

type fakeApplier struct {
	checkErr error
	applied  []Values
}

func (f *fakeApplier) Check(_ context.Context, _, _ Values) error { return f.checkErr }
func (f *fakeApplier) Apply(_ context.Context, _, n Values)       { f.applied = append(f.applied, n) }

// Сохраняется только изменённое: логин из overrides не попадает в базу. Отказ Check — ничего не
// сохранено и не применено.
func TestUpdateSavesOnlyChangedAndApplies(t *testing.T) {
	db := openDB(t)
	a := &fakeApplier{}
	s := New(db, load(t, db, map[string]string{KeyRutrackerLogin: "из-окружения"}), a)
	a.checkErr = &FieldError{Field: "Папка загрузок", Text: "нет права записи"}
	if _, err := s.Update(ctx, patch(t, `{"storage":{"keepDays":3,"downloadsDir":"D:\\Kino"}}`)); err == nil {
		t.Fatal("Check отказал, а Update прошёл")
	}
	if _, ok, _ := db.Setting(ctx, KeyKeepDays); ok || len(a.applied) != 0 || s.Current().KeepDays != 14 {
		t.Fatal("при отказе что-то сохранилось или применилось")
	}
	a.checkErr = nil
	if _, err := s.Update(ctx, patch(t, `{"storage":{"keepDays":3,"minFreeGB":0}}`)); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := db.Setting(ctx, KeyKeepDays); v != "3" {
		t.Fatalf("срок не сохранён: %q", v)
	}
	if v, _, _ := db.Setting(ctx, KeyMinFreeGB); v != "0" {
		t.Fatalf("запас 0 не сохранён: %q", v)
	}
	if _, ok, _ := db.Setting(ctx, KeyRutrackerLogin); ok {
		t.Fatal("логин из overrides записан в базу")
	}
	if len(a.applied) != 1 || a.applied[0].KeepDays != 3 || s.Current().MinFreeGB != 0 {
		t.Fatalf("применено: %+v", a.applied)
	}
}

// GET — с любого устройства (телевизору и kinodom open нужен плеер); PUT — только с этого ПК, форма
// без JSON отклоняется (основная спека, раздел 13).
func TestRoutesThroughAPIServer(t *testing.T) {
	db := openDB(t)
	s := New(db, load(t, db, nil), &fakeApplier{})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := api.New("127.0.0.1:0", api.Deps{Log: log, DB: db, Sup: supervisor.New(log), Web: fstest.MapFS{}})
	s.Register(srv)
	h := srv.Handler()
	do := func(method, remote, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/settings", strings.NewReader(body))
		req.Host, req.RemoteAddr = "127.0.0.1:8090", remote
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("GET", "192.168.0.7:5000", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"player":"auto"`) {
		t.Fatalf("GET из сети: %d %s", rec.Code, rec.Body)
	}
	if rec := do("PUT", "192.168.0.7:5000", `{"player":"vlc"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("PUT из сети: %d", rec.Code)
	}
	if rec := do("PUT", "127.0.0.1:5000", `{"player":"vlc"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"player":"vlc"`) {
		t.Fatalf("PUT с ПК: %d %s", rec.Code, rec.Body)
	}
	if rec := do("PUT", "127.0.0.1:5000", `{"storage":{"keepDays":0}}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "Хранить, дней") {
		t.Fatalf("неверное поле: %d %s", rec.Code, rec.Body)
	}
	if rec := do("PUT", "127.0.0.1:5000", `{"plaeyr":"vlc"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("опечатка в поле: %d", rec.Code)
	}
}
