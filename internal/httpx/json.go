// Package httpx — общие мелочи HTTP для модулей: ответы JSON и ошибки в одном формате.
package httpx

import (
	"encoding/json"
	"net/http"
)

// WriteJSON отвечает JSON с кодом code.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ErrorBody — формат ошибок API (спека, раздел 13): {"error": "понятный текст"}.
type ErrorBody struct {
	Error string `json:"error"`
}

func WriteError(w http.ResponseWriter, code int, text string) {
	WriteJSON(w, code, ErrorBody{Error: text})
}

// ReadJSON читает тело запроса (не больше 1 МБ) в v. Неизвестные поля — ошибка:
// опечатка в имени поля не должна молча превращаться в значение по умолчанию.
// При ошибке сам отвечает 400 и возвращает false.
func ReadJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, "не удалось разобрать запрос: "+err.Error())
		return false
	}
	return true
}
