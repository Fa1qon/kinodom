package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPairingCodeLifetimeAndRotation(t *testing.T) {
	p := newPairing()
	now := time.Unix(100, 0)
	a := p.view(now)
	if len(a.Code) != 6 || strings.Trim(a.Code, "0123456789") != "" {
		t.Fatalf("код не шестизначный: %q", a.Code)
	}
	if !p.verify(a.Code, now.Add(1*time.Second)) {
		t.Fatal("действующий код не принят")
	}
	if p.verify("000000", now.Add(time.Second)) && a.Code != "000000" {
		t.Fatal("чужой код принят")
	}
	b := p.view(now.Add(pairingLifetime + time.Second))
	if p.verify(a.Code, now.Add(pairingLifetime+time.Second)) || !p.verify(b.Code, now.Add(pairingLifetime+2*time.Second)) {
		t.Fatal("истёкший код всё ещё принят")
	}
}

func TestPairingHandlers(t *testing.T) {
	a := &App{pairing: newPairing(), p1: &p1State{}}
	view := httptest.NewRecorder()
	a.handlePairing(view, httptest.NewRequest(http.MethodGet, "/api/v1/pairing", nil))
	if view.Code != http.StatusOK {
		t.Fatalf("получение кода: %d", view.Code)
	}
	var code pairingView
	if err := json.Unmarshal(view.Body.Bytes(), &code); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": code.Code})
	verify := httptest.NewRecorder()
	a.handlePairingVerify(verify, httptest.NewRequest(http.MethodPost, "/api/v1/pairing/verify", bytes.NewReader(body)))
	if verify.Code != http.StatusOK {
		t.Fatalf("проверка кода: %d %s", verify.Code, verify.Body.String())
	}
	var paired struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(verify.Body.Bytes(), &paired); err != nil || !paired.OK || len(paired.Token) != 64 {
		t.Fatalf("pairing token is missing: %s", verify.Body.String())
	}
}
