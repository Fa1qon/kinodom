// Package offlinetest закрывает тестам интернет: встроенные адреса трекеров и Кинопоиска ведут на
// локальную «растяжку», которая записывает запрос и рвёт соединение (для кода это «трекер
// недоступен», без проверки Cloudflare и Edge). Тест, забывший подставить свои адреса, не уходит
// в интернет по недосмотру — прогон пакета падает со списком таких запросов.
package offlinetest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"kinodom/internal/meta"
	"kinodom/internal/source/rutor"
	"kinodom/internal/source/rutracker"
)

// Main — тело TestMain пакета: func TestMain(m *testing.M) { offlinetest.Main(m) }.
func Main(m *testing.M) {
	var mu sync.Mutex
	var hits []string
	trip := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
	}))
	rutor.DefaultMirrors = []string{trip.URL}
	rutor.DefaultDownloadBase = trip.URL
	rutracker.DefaultMirrors = []string{trip.URL}
	rutracker.DefaultAPIBase = trip.URL
	rutracker.DefaultFeedBase = trip.URL
	meta.DefaultKinopoiskAPI = trip.URL
	meta.DefaultRatingBase = trip.URL
	code := m.Run()
	trip.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(hits) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: тесты пошли бы в интернет по встроенным адресам трекеров или Кинопоиска (%d запросов): %s\n",
			len(hits), strings.Join(hits[:min(len(hits), 5)], "; "))
		code = 1
	}
	os.Exit(code)
}
