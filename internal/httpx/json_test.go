package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusTeapot, "чайник")
	if rec.Code != http.StatusTeapot {
		t.Fatalf("код %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type %q", ct)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"error":"чайник"}` {
		t.Fatalf("тело %s", body)
	}
}

func TestReadJSONRejectsUnknownFieldsAndGarbage(t *testing.T) {
	for _, body := range []string{`{"x":1,"extra":2}`, `{`} {
		var v struct {
			X int `json:"x"`
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		if ReadJSON(rec, req, &v) {
			t.Fatalf("тело %s должно быть отклонено", body)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("код %d для %s", rec.Code, body)
		}
	}
}

func TestReadJSONAccepts(t *testing.T) {
	var v struct {
		X int `json:"x"`
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"x":7}`))
	if !ReadJSON(httptest.NewRecorder(), req, &v) || v.X != 7 {
		t.Fatalf("v = %+v", v)
	}
}
