//go:build windows

package power

import "testing"

// Настоящий запрос Windows создаётся, ставится и снимается без ошибок (что его видно в
// powercfg /requests, проверяется вживую: команде нужны права администратора).
func TestWindowsRequest(t *testing.T) {
	r, err := newRequester("Kinodom: тест")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.set(); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := r.clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := r.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
