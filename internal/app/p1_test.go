package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestP1CapabilitiesValidate(t *testing.T) {
	in := capabilityInput{Codecs: []string{"h264", "h264", " HEVC "}, Audio: []string{"aac"}, MaxWidth: 1920, MaxHeight: 1080, Subtitles: []string{"ru"}, PreferredMode: "stream"}
	got, err := normalizeCapabilities(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Codecs) != 2 || got.Codecs[0] != "h264" || got.Codecs[1] != "hevc" || got.PreferredMode != "stream" {
		t.Fatalf("unexpected capabilities: %+v", got)
	}
	if _, err := normalizeCapabilities(capabilityInput{MaxWidth: -1}); err == nil {
		t.Fatal("negative dimensions accepted")
	}
}

func TestP1PinHashAndVerify(t *testing.T) {
	h, err := hashPIN("1234")
	if err != nil || !verifyPIN(h, "1234") || verifyPIN(h, "1235") {
		t.Fatal("PIN hash verification failed")
	}
}

func TestP1RemoteCommandQueue(t *testing.T) {
	p := &remoteState{}
	token := p.issueRemoteToken("tv-1")
	if token == "" || !p.validRemoteToken(token) {
		t.Fatal("token was not issued")
	}
	if _, ok := p.appendRemoteCommand(token, remoteCommandInput{Kind: "key", Value: "ArrowUp"}); !ok {
		t.Fatal("command rejected for valid token")
	}
	cmds, cursor := p.remoteCommands(0)
	if len(cmds) != 1 || cmds[0].Value != "ArrowUp" || cursor != 1 {
		t.Fatalf("unexpected commands: %#v cursor=%d", cmds, cursor)
	}
	if _, ok := p.appendRemoteCommand("bad", remoteCommandInput{Kind: "text", Value: "x"}); ok {
		t.Fatal("invalid token accepted")
	}
}

func TestP1WatchlistRequiresKind(t *testing.T) {
	a := &App{}
	w := httptest.NewRecorder()
	a.handleWatchlistPut(w, httptest.NewRequest(http.MethodPut, "/api/v1/watchlist/1", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d", w.Code)
	}
}

func TestP1MigrationAndCapabilityProfileEndpoint(t *testing.T) {
	a := startAppRaw(t, Options{Home: t.TempDir(), Offline: true, ListenAddr: ":0", Settings: map[string]string{"downloads.dir": t.TempDir()}})
	for _, table := range []string{"watchlist", "capability_profiles", "family_profiles", "family_devices"} {
		var n string
		if err := a.DB.R.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil {
			t.Fatalf("migration table %s: %v", table, err)
		}
	}
	body, _ := json.Marshal(capabilityInput{Codecs: []string{"h264"}, PreferredMode: "direct"})
	put := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/capabilities", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	a.handleCapabilitiesPut(put, req)
	if put.Code != http.StatusNoContent {
		t.Fatalf("capability PUT: %d %s", put.Code, put.Body.String())
	}
	get := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	a.handleCapabilities(get, req)
	if get.Code != http.StatusOK || !bytes.Contains(get.Body.Bytes(), []byte(`"direct"`)) {
		t.Fatalf("capability GET: %d %s", get.Code, get.Body.String())
	}
}

func TestP1FamilyProfileDeletePermission(t *testing.T) {
	a := startAppRaw(t, Options{Home: t.TempDir(), Offline: true, ListenAddr: ":0", Settings: map[string]string{"downloads.dir": t.TempDir()}})
	h, err := hashPIN("1234")
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.DB.W.Exec(`INSERT INTO family_profiles(name,pin_hash,can_delete,created_at) VALUES(?,?,0,?)`, "kids", h, 1)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := a.DB.W.Exec(`INSERT INTO family_devices(device,profile_id,updated_at) VALUES('pc',?,1)`, id); err != nil {
		t.Fatal(err)
	}
	if a.canDelete(testContext(), "pc") {
		t.Fatal("profile without delete permission was allowed")
	}
	if _, err := a.DB.W.Exec(`UPDATE family_profiles SET can_delete=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if !a.canDelete(testContext(), "pc") {
		t.Fatal("profile with delete permission was refused")
	}
}

func testContext() context.Context { return context.Background() }
