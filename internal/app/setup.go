package app

import (
	"errors"
	"net"
	"net/http"
	"strconv"

	"kinodom/internal/httpx"
	"kinodom/internal/netx"
	"kinodom/internal/source"
	"kinodom/internal/source/rutracker"
)

// initSetup — маршруты мастера начальных настроек и обзора папок (спека этапа 11a, раздел 7):
// только из домашней сети.
func (a *App) initSetup() {
	a.API.HandleHome("POST /api/v1/setup/check", "", http.HandlerFunc(a.handleSetupCheck))
	a.API.HandleHome("GET /api/v1/setup/addresses", "", http.HandlerFunc(a.handleSetupAddresses))
	a.API.HandleHome("GET /api/v1/fs/dirs", "", http.HandlerFunc(handleDirs))
}

// setupCheckResult — итог «Проверить» одной строкой.
type setupCheckResult struct {
	OK   bool   `json:"ok"`
	Text string `json:"text"`
}

// handleSetupCheck — «Проверить»: {"tracker": "rutor" | "rutracker"}. Проверяются уже сохранённые
// настройки: мастер сначала сохраняет поля. Шага «Кинопоиск» нет (спека 11b, 5.7).
func (a *App) handleSetupCheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tracker string `json:"tracker"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	var res setupCheckResult
	switch {
	case in.Tracker == "rutor":
		res = trackerCheck("Rutor", a.rutor.Configured(), a.rutor.Check(r.Context()))
	case in.Tracker == "rutracker":
		res = trackerCheck("Rutracker", a.rutracker.Configured(), a.rutracker.Check(r.Context()))
		if res.OK {
			res = rutrackerLogin(a.rutracker.LoginState(), a.rutracker.Relogin(r.Context()))
		}
	default:
		httpx.WriteError(w, http.StatusBadRequest, "проверить можно rutor или rutracker")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// trackerCheck — ответ сайта трекера для человека.
func trackerCheck(name string, configured bool, err error) setupCheckResult {
	switch {
	case !configured || errors.Is(err, source.ErrNotConfigured):
		return setupCheckResult{Text: "Укажите адрес " + name}
	case err == nil:
		return setupCheckResult{OK: true, Text: "Отвечает"}
	case errors.Is(err, netx.ErrProxyDown):
		return setupCheckResult{Text: "Прокси не отвечает"}
	case errors.Is(err, netx.ErrNotTracker):
		return setupCheckResult{Text: "Адрес не похож на сайт трекера"}
	case errors.Is(err, netx.ErrTrackerDown):
		return setupCheckResult{Text: "Сайт не отвечает — нужен прокси?"}
	case errors.Is(err, netx.ErrChallenge):
		return setupCheckResult{Text: "Сайт закрыт проверкой Cloudflare"}
	}
	return setupCheckResult{Text: err.Error()}
}

// rutrackerLogin — вход после ответа сайта: без логина и пароля — «Отвечает».
func rutrackerLogin(before, after rutracker.LoginInfo) setupCheckResult {
	switch after.State {
	case rutracker.LoginOK:
		return setupCheckResult{OK: true, Text: "Отвечает, вход выполнен"}
	case rutracker.LoginBlocked:
		return setupCheckResult{Text: after.Text}
	case rutracker.LoginFailing:
		return setupCheckResult{Text: "Вход не выполнен: " + after.Text}
	}
	return setupCheckResult{OK: true, Text: "Отвечает"}
}

// handleSetupAddresses — адреса пульта для телефонов и ТВ (экран «Готово»): порт — тот, что слушает API.
func (a *App) handleSetupAddresses(w http.ResponseWriter, r *http.Request) {
	port := strconv.Itoa(a.Boot.APIPort)
	if _, p, err := net.SplitHostPort(a.API.Addr()); err == nil {
		port = p
	}
	out := []string{}
	for _, ip := range httpx.HomeAddresses() {
		out = append(out, "http://"+ip+":"+port)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
