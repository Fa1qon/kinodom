package jacred

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"unicode/utf8"
)

// Финальное ревью 11b-Д: «Проверить» на сервере, который отвечает 500 или 404 на всё (неверный адрес, Jackett за
// другим путём), — итог для пульта должен быть читаемым текстом, а не битым UTF-8.
func TestJacredCheckTextReadable(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(fail.Close)
	notFound := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(notFound.Close)
	for name, addr := range map[string]string{"500": fail.URL, "404": notFound.URL} {
		ok, text := New(Options{Address: addr, Timeout: 2 * time.Second}).Check(ctx)
		if ok {
			t.Errorf("%s: ok", name)
		}
		if !utf8.ValidString(text) {
			t.Errorf("%s: итог «Проверить» — битый UTF-8: %q", name, text)
		}
		if name == "404" && text != "Адрес не похож на Jacred или Jackett" {
			t.Errorf("404 на всё — не источник: %q", text)
		}
	}
}
