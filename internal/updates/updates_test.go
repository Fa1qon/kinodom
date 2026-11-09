package updates

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type memStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memStore) Setting(_ context.Context, k string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok, nil
}

func (s *memStore) SetSetting(_ context.Context, k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
	return nil
}

// fakeHub — GitHub с одним релизом баз из папки dir и релизом программы app.
func fakeHub(t *testing.T, dir, dataTag, app string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/releases" {
			var assets []map[string]any
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				assets = append(assets, map[string]any{"name": e.Name(), "browser_download_url": srv.URL + "/dl/" + e.Name()})
			}
			json.NewEncoder(w).Encode([]map[string]any{
				{"tag_name": "v" + app, "html_url": "https://example/v" + app, "assets": assets},
				{"tag_name": dataTag, "assets": assets},
				{"tag_name": "v9.9.9", "draft": true},
			})
			return
		}
		if name, ok := strings.CutPrefix(r.URL.Path, "/dl/"); ok {
			http.ServeFile(w, r, filepath.Join(dir, name))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setup(t *testing.T, key ed25519.PrivateKey) (dir string, payload string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "recs-model.db")
	payload = "граф похожих"
	os.WriteFile(src, []byte(payload), 0o644)
	dir = t.TempDir()
	src2 := filepath.Join(filepath.Dir(src), "recs-model-2.db")
	os.WriteFile(src2, []byte("схема 2"), 0o644)
	if _, err := BuildRelease(dir, "2026.11.09", key, []Input{{Path: src, Pack: "recs", Schema: 1}, {Path: src2, Pack: "recs", Schema: 2}}); err != nil {
		t.Fatal(err)
	}
	return dir, payload
}

func service(t *testing.T, api string, pub ed25519.PublicKey, got *[]string) (*Service, *memStore) {
	st := &memStore{m: map[string]string{}}
	return New(Options{Repo: "o/r", API: api, Key: pub, Dir: t.TempDir(), Store: st, Version: "0.16.28-26-gd7b35f0a",
		Packs: []Pack{{Name: "recs", Schema: 1, Install: func(_ context.Context, path string) error {
			b, err := os.ReadFile(path)
			*got = append(*got, string(b))
			return err
		}}}}), st
}

func TestDataInstallsSignedPack(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir, payload := setup(t, priv)
	srv := fakeHub(t, dir, "data-2026.11.09", "0.17.0")
	var got []string
	s, _ := service(t, srv.URL, pub, &got)
	if err := s.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	// схема 2 программе непонятна — ставится только схема 1, распакованной
	if len(got) != 1 || got[0] != payload {
		t.Fatalf("поставлено %q", got)
	}
	st := s.Status(context.Background())
	if st.Data != "2026.11.09" || st.DataNew != "" || st.AppNew != "0.17.0" || !st.Auto {
		t.Fatalf("состояние %+v", st)
	}
	// второй раз тот же выпуск не ставится
	s.Check(context.Background(), true)
	if len(got) != 1 {
		t.Fatalf("поставлено повторно: %d", len(got))
	}
}

func TestDataRejectsWrongSignatureAndHash(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	dir, _ := setup(t, other) // подписан чужим ключом
	srv := fakeHub(t, dir, "data-2026.11.09", "0.16.28")
	var got []string
	s, st := service(t, srv.URL, pub, &got)
	if err := s.Check(context.Background(), true); err == nil || len(got) != 0 || st.m[keyData] != "" {
		t.Fatalf("чужая подпись принята: err=%v got=%q", err, got)
	}

	pub2, priv2, _ := ed25519.GenerateKey(rand.Reader)
	dir2, _ := setup(t, priv2)
	os.WriteFile(filepath.Join(dir2, "recs-model.db.gz"), []byte("подмена"), 0o644) // файл подменён после подписи
	srv2 := fakeHub(t, dir2, "data-2026.11.09", "0.16.28")
	s2, st2 := service(t, srv2.URL, pub2, &got)
	if err := s2.Check(context.Background(), true); err == nil || len(got) != 0 || st2.m[keyData] != "" {
		t.Fatalf("подменённый файл принят: err=%v got=%q", err, got)
	}
	if s2.Status(context.Background()).AppNew != "" {
		t.Fatal("та же версия программы показана как новая")
	}
}

func TestAutoOffOnlyTells(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir, _ := setup(t, priv)
	srv := fakeHub(t, dir, "data-2026.11.09", "0.16.28")
	var got []string
	s, _ := service(t, srv.URL, pub, &got)
	s.SetAuto(context.Background(), false)
	s.Check(context.Background(), s.auto(context.Background()))
	st := s.Status(context.Background())
	if len(got) != 0 || st.DataNew != "2026.11.09" || st.Auto {
		t.Fatalf("при выключенном автообновлении: got=%q %+v", got, st)
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"v0.17.0", "0.16.28-26-gd7b35f0a", true}, {"v0.16.28", "0.16.28-26-gd7b35f0a", false}, {"v0.16.9", "0.16.28", false}, {"v1.0.0", "dev", true}} {
		if newer(c.a, c.b) != c.want {
			t.Errorf("newer(%s, %s) != %v", c.a, c.b, c.want)
		}
	}
}

func TestAppInstallerSignedAndLaunched(t *testing.T) {
	if !canInstall {
		t.Skip("самообновление только на Windows")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	setupExe := filepath.Join(t.TempDir(), "Kinodom-0.17.0-setup.exe")
	os.WriteFile(setupExe, []byte("установщик"), 0o644)
	dir := t.TempDir()
	if _, err := BuildRelease(dir, "0.17.0", priv, []Input{{Path: setupExe, Pack: "app"}}); err != nil {
		t.Fatal(err)
	}
	// релиз программы — в той же папке, что и релиз баз (для fakeHub — один набор файлов)
	srv := fakeHub(t, dir, "data-2026.01.01", "0.17.0")
	var got []string
	s, _ := service(t, srv.URL, pub, &got)
	if err := s.InstallApp(context.Background()); !errors.Is(err, ErrNoApp) {
		t.Fatalf("до проверки: %v", err)
	}
	s.Check(context.Background(), false)
	if st := s.Status(context.Background()); st.AppNew != "0.17.0" || !st.AppSigned {
		t.Fatalf("состояние %+v", st)
	}
	var launched string
	start = func(p string) error { b, _ := os.ReadFile(p); launched = string(b); return nil }
	defer func() { start = launch }()
	if err := s.InstallApp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if launched != "установщик" {
		t.Fatalf("запущен %q", launched)
	}
}
