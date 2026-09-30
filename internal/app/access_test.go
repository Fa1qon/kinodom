package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"kinodom/internal/library"
)

// «Разрешить доступ» (спека этапа 11a, раздел 4.7): kinodomw.exe спрашивает службу, её ли это
// папка — медиатеки или загрузок. Чужая папка — пустой ответ: веб-страница не выдаст службе права
// на что угодно. Спросить можно только с этого ПК.
func TestLibraryAccess(t *testing.T) {
	dl := t.TempDir()
	a := startAppWith(t, Options{Home: t.TempDir(), ListenAddr: "127.0.0.1:0", Offline: true, DownloadsDir: dl})
	ctx := context.Background()
	movies := t.TempDir()
	cs, err := a.Library.Categories(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Builtin == "films" {
			if err := a.Library.UpdateCategory(ctx, c.ID, library.CategoryInput{Folders: []string{movies}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	type access struct {
		Kind     string `json:"kind"`
		Readable bool   `json:"readable"`
	}
	ask := func(remote, path string) (int, access) {
		req := httptest.NewRequest("GET", "/api/v1/library/access?path="+url.QueryEscape(path), nil)
		req.Host, req.RemoteAddr = "localhost", remote
		rec := httptest.NewRecorder()
		a.API.Handler().ServeHTTP(rec, req)
		var out access
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	const pc = "127.0.0.1:50000"
	cases := map[string]access{
		movies:                        {Kind: "library", Readable: true},
		strings.ToUpper(movies) + `\`: {Kind: "library", Readable: true},
		dl:                            {Kind: "downloads", Readable: true},
		filepath.Join(movies, "..", "другая"): {},
		t.TempDir(): {},
		filepath.Join(movies, "подпапка"): {},
		`\\server\share`: {},
	}
	for path, want := range cases {
		code, got := ask(pc, path)
		if code != http.StatusOK || got != want {
			t.Errorf("%s: код %d, %+v, ждали %+v", path, code, got, want)
		}
	}
	if code, _ := ask("192.168.0.7:50000", movies); code != http.StatusForbidden {
		t.Fatalf("не с этого ПК: код %d", code)
	}
}
