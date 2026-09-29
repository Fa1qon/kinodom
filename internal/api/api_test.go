package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"kinodom/internal/store"
	"kinodom/internal/supervisor"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type idleModule struct{ name string }

func (m idleModule) Name() string { return m.name }
func (m idleModule) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func newTestServer(t *testing.T, mods ...supervisor.Module) (*Server, *store.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sup := supervisor.New(quiet())
	for _, m := range mods {
		sup.Add(m, true) // зарегистрированы, но Run не вызван — состояние stopped
	}
	web := fstest.MapFS{"index.html": {Data: []byte("<h1>Kinodom</h1>")}}
	return New("127.0.0.1:0", Deps{Log: quiet(), DB: db, Sup: sup, Web: web}), db
}

// do выполняет запрос; httptest по умолчанию ставит Host example.com — заменяем на свой адрес.
func do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	if req.Host == "example.com" {
		req.Host = "127.0.0.1:8090"
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestStatusShowsModulesAndProblems(t *testing.T) {
	s, db := newTestServer(t, idleModule{"torrents"})
	if err := db.SetProblem(context.Background(), "proxy", "Прокси не отвечает"); err != nil {
		t.Fatal(err)
	}
	rec := do(s.Handler(), httptest.NewRequest("GET", "/api/v1/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
	var st struct {
		Problems []store.Problem     `json:"problems"`
		Modules  []supervisor.Status `json:"modules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Problems) != 1 || st.Problems[0].Text != "Прокси не отвечает" {
		t.Fatalf("проблемы: %+v", st.Problems)
	}
	if len(st.Modules) != 1 || st.Modules[0].Name != "torrents" {
		t.Fatalf("модули: %+v", st.Modules)
	}
}

func TestRejectsUnknownHost(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	req.Host = "evil.example"
	rec := do(s.Handler(), req)
	if rec.Code != http.StatusMisdirectedRequest || !strings.Contains(rec.Body.String(), "неизвестный адрес") {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
}

func TestAllowsLoopbackHostForms(t *testing.T) {
	s, _ := newTestServer(t)
	for _, host := range []string{"localhost:8090", "127.0.0.1", "[::1]:8090", "LOCALHOST:8090"} {
		req := httptest.NewRequest("GET", "/api/v1/status", nil)
		req.Host = host
		if rec := do(s.Handler(), req); rec.Code != http.StatusOK {
			t.Errorf("Host %q: код %d", host, rec.Code)
		}
	}
}

func TestMutatingRequestNeedsJSON(t *testing.T) {
	s, _ := newTestServer(t)
	s.Handle("POST /api/v1/test", "", okHandler())
	req := httptest.NewRequest("POST", "/api/v1/test", strings.NewReader("a=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := do(s.Handler(), req); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("форма: код %d", rec.Code)
	}
	req = httptest.NewRequest("POST", "/api/v1/test", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if rec := do(s.Handler(), req); rec.Code != http.StatusOK {
		t.Fatalf("JSON: код %d", rec.Code)
	}
}

// ownAddr — сетевой (не loopback) адрес этого ПК: пульт, открытый на ПК по адресу в сети. "" — адресов
// в сети нет.
func ownAddr(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			return n.IP.String()
		}
	}
	return ""
}

func TestLocalOnlyRoute(t *testing.T) {
	s, _ := newTestServer(t)
	s.HandleLocal("GET /api/v1/local", "", okHandler())
	remotes := map[string]int{"192.168.0.7:50000": http.StatusForbidden, "127.0.0.1:50000": http.StatusOK}
	if a := ownAddr(t); a != "" {
		remotes[net.JoinHostPort(a, "50000")] = http.StatusOK // этот ПК по адресу в сети
	}
	for remote, want := range remotes {
		req := httptest.NewRequest("GET", "/api/v1/local", nil)
		req.RemoteAddr = remote
		if rec := do(s.Handler(), req); rec.Code != want {
			t.Fatalf("%s: код %d", remote, rec.Code)
		}
	}
}

// Изменения — из домашней сети; с других адресов — 403 с понятным текстом (спека этапа 7, раздел 10.1).
func TestHomeOnlyRoute(t *testing.T) {
	s, _ := newTestServer(t)
	s.HandleHome("PUT /api/v1/home", "", okHandler())
	for remote, want := range map[string]int{"192.168.0.7:50000": http.StatusOK, "127.0.0.1:50000": http.StatusOK,
		"[fe80::5%eth0]:50000": http.StatusOK, "8.8.8.8:50000": http.StatusForbidden} {
		req := httptest.NewRequest("PUT", "/api/v1/home", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = remote
		rec := do(s.Handler(), req)
		if rec.Code != want {
			t.Fatalf("%s: код %d", remote, rec.Code)
		}
		if want == http.StatusForbidden && !strings.Contains(rec.Body.String(), "Изменить можно только из домашней сети") {
			t.Fatalf("%s: %s", remote, rec.Body)
		}
	}
}

func TestModuleGuard(t *testing.T) {
	s, _ := newTestServer(t, idleModule{"torrents"})
	s.Handle("GET /api/v1/mod", "torrents", okHandler())
	rec := do(s.Handler(), httptest.NewRequest("GET", "/api/v1/mod", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "«Торренты» временно недоступен") {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
}

func TestPanicBecomes500(t *testing.T) {
	s, _ := newTestServer(t)
	s.Handle("GET /api/v1/boom", "", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("бум") }))
	rec := do(s.Handler(), httptest.NewRequest("GET", "/api/v1/boom", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
}

func TestServesPultRoot(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(s.Handler(), httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Kinodom") {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
}

func TestRunListensAndStops(t *testing.T) {
	s, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Run(ctx) }()
	select {
	case <-s.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("сервер не поднялся")
	}
	resp, err := http.Get("http://" + s.Addr() + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код %d", resp.StatusCode)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("Run вернул %v", err)
	}
}

// «Состояние»: поля приложения добавляются к проблемам и модулям; local — запрос с этого ПК, canEdit —
// из домашней сети (спека этапа 7, раздел 10.1).
func TestStatusAddsAppFieldsAndLocal(t *testing.T) {
	s, _ := newTestServer(t)
	s.SetStatus(func(context.Context) (map[string]any, error) {
		return map[string]any{"disk": map[string]int{"freeBytes": 7}}, nil
	})
	type who struct{ local, canEdit bool }
	remotes := map[string]who{"192.168.0.7:5000": {false, true}, "127.0.0.1:5000": {true, true}, "8.8.8.8:5000": {false, false}}
	if a := ownAddr(t); a != "" {
		remotes[net.JoinHostPort(a, "5000")] = who{true, true}
	}
	for remote, want := range remotes {
		req := httptest.NewRequest("GET", "/api/v1/status", nil)
		req.RemoteAddr = remote
		rec := do(s.Handler(), req)
		var st struct {
			Local    bool                    `json:"local"`
			CanEdit  bool                    `json:"canEdit"`
			Problems []store.Problem         `json:"problems"`
			Disk     struct{ FreeBytes int } `json:"disk"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || (who{st.Local, st.CanEdit}) != want || st.Disk.FreeBytes != 7 || st.Problems == nil {
			t.Fatalf("%s: %s", remote, rec.Body)
		}
	}
}
