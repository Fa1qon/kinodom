package app

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"kinodom/internal/httpx"
)

const pairingLifetime = 10 * time.Minute

type pairingState struct {
	mu      sync.Mutex
	code    string
	expires time.Time
}

type pairingView struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func newPairing() *pairingState { return &pairingState{} }

func (p *pairingState) view(now time.Time) pairingView {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.code == "" || !now.Before(p.expires) {
		p.code = randomPairingCode()
		p.expires = now.Add(pairingLifetime)
	}
	return pairingView{Code: p.code, ExpiresAt: p.expires}
}

func (p *pairingState) verify(code string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code != "" && now.Before(p.expires) && strings.TrimSpace(code) == p.code
}

func randomPairingCode() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "000000"
	}
	return fmt.Sprintf("%06d", int64(uint32(b[0])<<24|uint32(b[1])<<16|uint32(b[2])<<8|uint32(b[3]))%1000000)
}

func (a *App) handlePairing(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, a.pairing.view(time.Now()))
}

func (a *App) handlePairingVerify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	if !a.pairing.verify(in.Code, time.Now()) {
		httpx.WriteError(w, http.StatusUnauthorized, "неверный или просроченный код")
		return
	}
	resp := map[string]any{"ok": true}
	if a.p1 != nil {
		if token := a.p1.issueRemoteToken(httpx.Device(r)); token != "" {
			resp["token"] = token
			resp["expiresAt"] = time.Now().Add(30 * 24 * time.Hour)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
