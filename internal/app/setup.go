package app

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"

	"kinodom/internal/httpx"
	"kinodom/internal/meta"
	"kinodom/internal/netx"
	"kinodom/internal/source"
	"kinodom/internal/source/t2"
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
	OK        bool   `json:"ok"`
	Text      string `json:"text"`
	DailyLeft *int   `json:"dailyLeft,omitempty"` // Кинопоиск: сколько запросов осталось на сутки
}

// handleSetupCheck — «Проверить»: {"tracker": "t1" | "t2"} или {"kinopoisk": true}.
// Проверяются уже сохранённые настройки: мастер сначала сохраняет поля.
func (a *App) handleSetupCheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tracker   string `json:"tracker"`
		Kinopoisk bool   `json:"kinopoisk"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	var res setupCheckResult
	switch {
	case in.Kinopoisk:
		res = a.checkKinopoisk(r)
	case in.Tracker == "t1":
		res = trackerCheck("t1", a.t1.Configured(), a.t1.Check(r.Context()))
	case in.Tracker == "t2":
		res = trackerCheck("t2", a.t2.Configured(), a.t2.Check(r.Context()))
		if res.OK {
			res = t2Login(a.t2.LoginState(), a.t2.Relogin(r.Context()))
		}
	default:
		httpx.WriteError(w, http.StatusBadRequest, "проверить можно t1, t2 или kinopoisk")
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

// t2Login — вход после ответа сайта: без логина и пароля — «Отвечает».
func t2Login(before, after t2.LoginInfo) setupCheckResult {
	switch after.State {
	case t2.LoginOK:
		return setupCheckResult{OK: true, Text: "Отвечает, вход выполнен"}
	case t2.LoginBlocked:
		return setupCheckResult{Text: after.Text}
	case t2.LoginFailing:
		return setupCheckResult{Text: "Вход не выполнен: " + after.Text}
	}
	return setupCheckResult{OK: true, Text: "Отвечает"}
}

// checkKinopoisk — ключ из настроек: подходит ли и сколько запросов осталось на сутки (сам запрос
// лимитов квоту не тратит).
func (a *App) checkKinopoisk(r *http.Request) setupCheckResult {
	q, err := a.kp.Quota(r.Context())
	switch {
	case errors.Is(err, meta.ErrNoKey):
		return setupCheckResult{Text: "Ключ не указан"}
	case errors.Is(err, meta.ErrBadKey):
		return setupCheckResult{Text: "Ключ не подходит"}
	case err != nil:
		return setupCheckResult{Text: "Кинопоиск не отвечает"}
	}
	left := max(q.DailyLimit-q.DailyUsed, 0)
	return setupCheckResult{OK: true, Text: fmt.Sprintf("Ключ подходит, осталось %d запросов в сутки", left), DailyLeft: &left}
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
