package app

import (
	"net/http"

	"kinodom/internal/httpx"
	"kinodom/internal/player"
)

// handlePlayers — внешние плееры этого ПК и есть ли каждый из них: выбор в «Параметрах»
// показывает только установленные (просьба 2026-10-07).
func (a *App) handlePlayers(w http.ResponseWriter, r *http.Request) {
	type found struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Found bool   `json:"found"`
	}
	out := []found{}
	for _, p := range []struct{ id, name string }{
		{"vlc", "VLC"},
		{"mpc-hc", "MPC-HC"},
	} {
		_, err := player.Find(p.id)
		out = append(out, found{ID: p.id, Name: p.name, Found: err == nil})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
