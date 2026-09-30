// Package offlinetest закрывает тестам интернет: встроенные адреса Кинопоиска ведут на локальную
// «растяжку», которая записывает запрос и рвёт соединение. Тест, забывший подставить свои адреса,
// не уходит в интернет по недосмотру — прогон пакета падает со списком таких запросов. Адресов
// трекеров в программе нет (этап 11a): без них трекеры выключены и в сеть не ходят.
package offlinetest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"kinodom/internal/iptv"
	"kinodom/internal/meta"
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
	meta.DefaultKinopoiskAPI = trip.URL
	meta.DefaultRatingBase = trip.URL
	// Телепрограмма и база iptv-org: модуль IPTV скачивает их сам при старте — это не забытый адрес,
	// а обычная работа. Заглушка отвечает 404 и в «растяжку» не считается.
	stub := httptest.NewServer(http.NotFoundHandler())
	iptv.DefaultEPGURL = stub.URL + "/epg.xml.gz"
	iptv.DefaultOrgBase = stub.URL
	code := m.Run()
	trip.Close()
	stub.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(hits) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: тесты пошли бы в интернет по встроенным адресам Кинопоиска (%d запросов): %s\n",
			len(hits), strings.Join(hits[:min(len(hits), 5)], "; "))
		code = 1
	}
	os.Exit(code)
}
