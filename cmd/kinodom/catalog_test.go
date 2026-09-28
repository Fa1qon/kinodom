package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kinodom/internal/source/t1/t1test"
	"kinodom/internal/source/t2/t2test"
)

// localProxy — HTTP-прокси только для локальных адресов: внешние (картинки с хостингов) — 404,
// чтобы тест не ходил в интернет.
func localProxy(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Host, "127.0.0.1") {
			http.NotFound(w, r)
			return
		}
		out, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func runCatalog(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runCLI(append([]string{"catalog"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// refresh на фейковых трекерах: каталог наполняется, первые карточки — с названиями; list — то
// же из базы без запуска модулей; search — по обоим трекерам.
func TestCatalogCommand(t *testing.T) {
	t1 := t1test.NewServer(t)
	rt := t2test.NewServer(t)
	rt.Login, rt.Password = "user", "pass"
	t.Setenv("KINODOM_t2_LOGIN", "user")
	t.Setenv("KINODOM_t2_PASSWORD", "pass")
	t.Setenv("KINODOM_KP_KEY", "")
	home := t.TempDir()
	kp := httptest.NewServer(http.NotFoundHandler()) // Кинопоиск ничего не знает — но и в интернет не ходим
	t.Cleanup(kp.Close)
	trackers := []string{"--home", home, "--t1", t1.Mirror.URL, "--t1-download", t1.Download.URL,
		"--t2", rt.Forum.URL, "--t2-api", rt.API.URL, "--t2-feed", rt.Feed.URL, "--no-edge",
		"--proxy", localProxy(t), "--kinopoisk", kp.URL}
	code, out, errOut := runCatalog(t, append(append([]string{"refresh"}, trackers...), "--wait", "30s", "--limit", "3")...)
	if code != 0 || !strings.Contains(out, "Карточек") || !strings.Contains(out, "[t1:") {
		t.Fatalf("refresh: код %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut = runCatalog(t, "list", "--home", home, "--tracker", "t1", "--limit", "2")
	if code != 0 || strings.Count(out, "[t1:") != 2 {
		t.Fatalf("list: код %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut = runCatalog(t, append(append([]string{"search"}, trackers...), "космос")...)
	if code != 0 || !strings.Contains(out, "[t2:") {
		t.Fatalf("search: код %d\n%s\n%s", code, out, errOut)
	}
}

func TestCatalogUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"refresh"}, {"list", "x"}, {"search", "--home", "h"}, {"dance", "--home", "h"}} {
		if code, _, errOut := runCatalog(t, args...); code != 2 || !strings.Contains(errOut, "Использование") {
			t.Errorf("%v: код %d", args, code)
		}
	}
}
