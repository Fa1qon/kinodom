package api

import (
	"log/slog"
	"mime"
	"net/http"
	"runtime/debug"
	"strings"

	"kinodom/internal/httpx"
)

// recoverer: паника в обработчике превращается в ответ 500 и запись в журнал,
// а не в оборванное соединение.
func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { // штатный способ оборвать ответ — пропускаем дальше
					panic(v)
				}
				log.Error("паника в обработчике", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				httpx.WriteError(w, http.StatusInternalServerError, "внутренняя ошибка сервера")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// jsonGuard: изменяющие запросы к /api/ — только с Content-Type: application/json.
// Обычная HTML-форма на чужом сайте не может отправить такой запрос без спроса браузера (CORS),
// так что это защищает API от подделки запросов со страниц, открытых на этом ПК.
func jsonGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			if strings.HasPrefix(r.URL.Path, "/api/") {
				mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if mt != "application/json" {
					httpx.WriteError(w, http.StatusUnsupportedMediaType, "нужен Content-Type: application/json")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackOnly пропускает только запросы с этого же ПК.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpx.IsLoopback(r) {
			httpx.WriteError(w, http.StatusForbidden, "это действие доступно только на компьютере, где работает Kinodom")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// moduleTitles — названия модулей для сообщений людям.
var moduleTitles = map[string]string{
	"torrents":  "Торренты",
	"catalog":   "Каталог",
	"ratings":   "Рейтинги",
	"edge":      "Rutracker",
	"iptv":      "Каналы",
	"multicast": "Каналы провайдера",
	"library":   "Медиатека",
	"dlna":      "DLNA",
}

func (s *Server) moduleGuard(module string, next http.Handler) http.Handler {
	if module == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Sup != nil && !s.deps.Sup.IsRunning(module) {
			title := moduleTitles[module]
			if title == "" {
				title = module
			}
			httpx.WriteError(w, http.StatusServiceUnavailable, "Раздел «"+title+"» временно недоступен")
			return
		}
		next.ServeHTTP(w, r)
	})
}
