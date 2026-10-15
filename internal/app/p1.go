package app

// P1 server primitives. The web and TV layouts can consume these APIs later; the
// storage and protocol stay independent of presentation.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/catalog"
	"kinodom/internal/httpx"
	"kinodom/internal/library"
)

type p1State struct {
	db *sql.DB
	remoteState
}

type remoteState struct {
	mu     sync.Mutex
	next   int64
	tokens map[string]remoteSession
	cmds   []remoteCommand
}

type remoteSession struct{ expires time.Time }
type remoteCommand struct {
	Seq       int64     `json:"seq"`
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"createdAt"`
}
type remoteCommandInput struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

func (p *remoteState) issueRemoteToken(_ string) string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	t := hex.EncodeToString(b[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tokens == nil {
		p.tokens = map[string]remoteSession{}
	}
	p.tokens[t] = remoteSession{expires: time.Now().Add(30 * 24 * time.Hour)}
	return t
}
func (p *remoteState) validRemoteToken(token string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.tokens[token]
	if !ok || !time.Now().Before(s.expires) {
		if ok {
			delete(p.tokens, token)
		}
		return false
	}
	return true
}
func (p *remoteState) appendRemoteCommand(token string, in remoteCommandInput) (remoteCommand, bool) {
	if !p.validRemoteToken(token) || (in.Kind != "key" && in.Kind != "text") || strings.TrimSpace(in.Value) == "" || len(in.Value) > 256 {
		return remoteCommand{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	c := remoteCommand{Seq: p.next, Kind: in.Kind, Value: in.Value, CreatedAt: time.Now()}
	p.cmds = append(p.cmds, c)
	if len(p.cmds) > 256 {
		p.cmds = p.cmds[len(p.cmds)-256:]
	}
	return c, true
}
func (p *remoteState) remoteCommands(since int64) ([]remoteCommand, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]remoteCommand, 0)
	for _, c := range p.cmds {
		if c.Seq > since {
			out = append(out, c)
		}
	}
	return out, p.next
}

type capabilityInput struct {
	Codecs        []string `json:"codecs"`
	Audio         []string `json:"audio"`
	MaxWidth      int      `json:"maxWidth"`
	MaxHeight     int      `json:"maxHeight"`
	Subtitles     []string `json:"subtitles"`
	PreferredMode string   `json:"preferredMode"`
}
type capabilityProfile struct {
	Device        string    `json:"device"`
	Codecs        []string  `json:"codecs"`
	Audio         []string  `json:"audio"`
	MaxWidth      int       `json:"maxWidth"`
	MaxHeight     int       `json:"maxHeight"`
	Subtitles     []string  `json:"subtitles"`
	PreferredMode string    `json:"preferredMode"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func cleanList(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func normalizeCapabilities(in capabilityInput) (capabilityInput, error) {
	if in.MaxWidth < 0 || in.MaxHeight < 0 || in.MaxWidth > 16384 || in.MaxHeight > 16384 {
		return capabilityInput{}, errors.New("разрешение должно быть от 0 до 16384")
	}
	in.Codecs, in.Audio, in.Subtitles = cleanList(in.Codecs), cleanList(in.Audio), cleanList(in.Subtitles)
	in.PreferredMode = strings.ToLower(strings.TrimSpace(in.PreferredMode))
	if in.PreferredMode == "" {
		in.PreferredMode = "auto"
	}
	if in.PreferredMode != "auto" && in.PreferredMode != "stream" && in.PreferredMode != "direct" {
		return capabilityInput{}, errors.New("preferredMode: auto, stream или direct")
	}
	return in, nil
}

func hashPIN(pin string) (string, error) {
	if len(pin) < 4 || len(pin) > 12 {
		return "", errors.New("PIN должен содержать от 4 до 12 символов")
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return "", errors.New("PIN должен содержать только цифры")
		}
	}
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(salt[:])
	h.Write([]byte(pin))
	return hex.EncodeToString(salt[:]) + ":" + hex.EncodeToString(h.Sum(nil)), nil
}
func verifyPIN(stored, pin string) bool {
	p := strings.Split(stored, ":")
	if len(p) != 2 {
		return false
	}
	salt, err := hex.DecodeString(p[0])
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(p[1])
	if err != nil {
		return false
	}
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(pin))
	got := h.Sum(nil)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (a *App) initP1() {
	a.p1 = &p1State{db: a.DB.W}
	if a.Torrents != nil {
		a.Torrents.SetDeleteAllowed(a.canDelete)
	}
	a.API.Handle("GET /api/v1/watchlist", "", http.HandlerFunc(a.handleWatchlist))
	a.API.HandleHome("PUT /api/v1/watchlist/{id}", "", http.HandlerFunc(a.handleWatchlistPut))
	a.API.HandleHome("DELETE /api/v1/watchlist/{id}", "", http.HandlerFunc(a.handleWatchlistDelete))
	a.API.Handle("GET /api/v1/works/{id}", "", http.HandlerFunc(a.handleWork))
	a.API.Handle("GET /api/v1/capabilities", "", http.HandlerFunc(a.handleCapabilities))
	a.API.HandleHome("PUT /api/v1/capabilities", "", http.HandlerFunc(a.handleCapabilitiesPut))
	a.API.Handle("GET /api/v1/profiles", "", http.HandlerFunc(a.handleProfiles))
	a.API.HandleHome("POST /api/v1/profiles", "", http.HandlerFunc(a.handleProfilePost))
	a.API.HandleHome("DELETE /api/v1/profiles/{id}", "", http.HandlerFunc(a.handleProfileDelete))
	a.API.HandleHome("POST /api/v1/profile/activate", "", http.HandlerFunc(a.handleProfileActivate))
	a.API.Handle("GET /api/v1/profile", "", http.HandlerFunc(a.handleProfileCurrent))
	a.API.HandleHome("POST /api/v1/remote/input", "", http.HandlerFunc(a.handleRemoteInput))
	a.API.Handle("GET /api/v1/remote/commands", "", http.HandlerFunc(a.handleRemoteCommands))
}

type watchlistItem struct {
	Release   catalog.ReleaseRef `json:"release"`
	Kind      string             `json:"kind"`
	CreatedAt time.Time          `json:"createdAt"`
}

func (a *App) handleWatchlist(w http.ResponseWriter, r *http.Request) {
	device := httpx.Device(r)
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && kind != "film" && kind != "series" {
		httpx.WriteError(w, 400, "kind: film или series")
		return
	}
	q := `SELECT release_id, kind, created_at FROM watchlist WHERE device = ?`
	args := []any{device}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := a.DB.R.QueryContext(r.Context(), q, args...)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []watchlistItem{}
	for rows.Next() {
		var id int64
		var k string
		var at int64
		if err := rows.Scan(&id, &k, &at); err != nil {
			httpx.WriteError(w, 500, err.Error())
			return
		}
		ref, err := a.Catalog.ReleaseBrief(r.Context(), id)
		if err != nil {
			continue
		}
		out = append(out, watchlistItem{Release: ref, Kind: k, CreatedAt: time.UnixMilli(at)})
	}
	httpx.WriteJSON(w, 200, map[string]any{"items": out})
}
func (a *App) handleWatchlistPut(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		httpx.WriteError(w, 400, "неверный номер раздачи")
		return
	}
	var in struct {
		Kind string `json:"kind"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	if in.Kind != "film" && in.Kind != "series" {
		httpx.WriteError(w, 400, "kind: film или series")
		return
	}
	if _, err := a.Catalog.ReleaseBrief(r.Context(), id); err != nil {
		httpx.WriteError(w, 404, "раздача не найдена")
		return
	}
	_, err = a.DB.W.ExecContext(r.Context(), `INSERT OR IGNORE INTO watchlist(device,kind,release_id,created_at) VALUES(?,?,?,?)`, httpx.Device(r), in.Kind, id, time.Now().UnixMilli())
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) handleWatchlistDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		httpx.WriteError(w, 400, "неверный номер раздачи")
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && kind != "film" && kind != "series" {
		httpx.WriteError(w, 400, "kind: film или series")
		return
	}
	q := `DELETE FROM watchlist WHERE device = ? AND release_id = ?`
	args := []any{httpx.Device(r), id}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	if _, err := a.DB.W.ExecContext(r.Context(), q, args...); err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type workCard struct {
	Release   catalog.ReleaseView `json:"release"`
	Follow    string              `json:"follow"`
	Watchlist []string            `json:"watchlist"`
	History   any                 `json:"history"`
	Library   any                 `json:"library"`
	Torrent   any                 `json:"torrent"`
}

func (a *App) handleWork(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.WriteError(w, 400, "неверный номер раздачи")
		return
	}
	rel, err := a.Catalog.Release(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, 404, "раздача не найдена")
		return
	}
	card := workCard{Release: rel.View(), Watchlist: []string{}}
	if a.Follow != nil {
		card.Follow, _ = a.Follow.State(r.Context(), id)
	}
	rows, err := a.DB.R.QueryContext(r.Context(), `SELECT kind FROM watchlist WHERE device=? AND release_id=?`, httpx.Device(r), id)
	if err == nil {
		for rows.Next() {
			var k string
			if rows.Scan(&k) == nil {
				card.Watchlist = append(card.Watchlist, k)
			}
		}
		rows.Close()
	}
	if a.History != nil && rel.InfoHash != "" {
		if xs, e := a.History.List(r.Context(), httpx.Device(r)); e == nil {
			for _, x := range xs {
				if strings.EqualFold(x.Hash, rel.InfoHash) {
					card.History = x
					break
				}
			}
		}
	}
	if rel.InfoHash != "" {
		var ih metainfo.Hash
		if ih.FromHexString(rel.InfoHash) == nil && a.Torrents != nil {
			if st, ok := a.Torrents.Status(ih); ok {
				card.Torrent = st
			}
		}
		if a.Library != nil {
			if cards, e := a.Library.HistoryInfo(r.Context(), httpx.Device(r), []string{rel.InfoHash}); e == nil {
				if c, ok := cards[rel.InfoHash]; ok {
					var v library.CardSummary = c
					card.Library = v
				}
			}
		}
	}
	httpx.WriteJSON(w, 200, card)
}

func (a *App) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	device := httpx.Device(r)
	var c capabilityInput
	var at int64
	var codecs, audio, subs string
	err := a.DB.R.QueryRowContext(r.Context(), `SELECT codecs,audio,max_width,max_height,subtitles,preferred_mode,updated_at FROM capability_profiles WHERE device=?`, device).Scan(&codecs, &audio, &c.MaxWidth, &c.MaxHeight, &subs, &c.PreferredMode, &at)
	if errors.Is(err, sql.ErrNoRows) {
		c.PreferredMode = "auto"
	} else if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	} else {
		_ = json.Unmarshal([]byte(codecs), &c.Codecs)
		_ = json.Unmarshal([]byte(audio), &c.Audio)
		_ = json.Unmarshal([]byte(subs), &c.Subtitles)
	}
	httpx.WriteJSON(w, 200, capabilityProfile{Device: device, Codecs: c.Codecs, Audio: c.Audio, MaxWidth: c.MaxWidth, MaxHeight: c.MaxHeight, Subtitles: c.Subtitles, PreferredMode: c.PreferredMode, UpdatedAt: time.UnixMilli(at)})
}
func (a *App) handleCapabilitiesPut(w http.ResponseWriter, r *http.Request) {
	var in capabilityInput
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	c, err := normalizeCapabilities(in)
	if err != nil {
		httpx.WriteError(w, 400, err.Error())
		return
	}
	enc := func(v []string) string { b, _ := json.Marshal(v); return string(b) }
	now := time.Now().UnixMilli()
	_, err = a.DB.W.ExecContext(r.Context(), `INSERT INTO capability_profiles(device,codecs,audio,max_width,max_height,subtitles,preferred_mode,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(device) DO UPDATE SET codecs=excluded.codecs,audio=excluded.audio,max_width=excluded.max_width,max_height=excluded.max_height,subtitles=excluded.subtitles,preferred_mode=excluded.preferred_mode,updated_at=excluded.updated_at`, httpx.Device(r), enc(c.Codecs), enc(c.Audio), c.MaxWidth, c.MaxHeight, enc(c.Subtitles), c.PreferredMode, now)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type profileView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	AllowAdult bool   `json:"allowAdult"`
	CanDelete  bool   `json:"canDelete"`
	Active     bool   `json:"active"`
}

func (a *App) handleProfiles(w http.ResponseWriter, r *http.Request) {
	rows, err := a.DB.R.QueryContext(r.Context(), `SELECT p.id,p.name,p.allow_adult,p.can_delete,COALESCE(d.device,'') FROM family_profiles p LEFT JOIN family_devices d ON d.profile_id=p.id`)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	dev := httpx.Device(r)
	out := []profileView{}
	for rows.Next() {
		var p profileView
		var aa, dd int
		var active string
		if rows.Scan(&p.ID, &p.Name, &aa, &dd, &active) != nil {
			continue
		}
		p.AllowAdult = aa != 0
		p.CanDelete = dd != 0
		p.Active = active == dev
		out = append(out, p)
	}
	httpx.WriteJSON(w, 200, map[string]any{"items": out})
}
func (a *App) handleProfilePost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string `json:"name"`
		PIN        string `json:"pin"`
		AllowAdult bool   `json:"allowAdult"`
		CanDelete  bool   `json:"canDelete"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		httpx.WriteError(w, 400, "имя профиля обязательно")
		return
	}
	h, err := hashPIN(in.PIN)
	if err != nil {
		httpx.WriteError(w, 400, err.Error())
		return
	}
	res, err := a.DB.W.ExecContext(r.Context(), `INSERT INTO family_profiles(name,pin_hash,allow_adult,can_delete,created_at) VALUES(?,?,?,?,?)`, in.Name, h, boolInt(in.AllowAdult), boolInt(in.CanDelete), time.Now().UnixMilli())
	if err != nil {
		httpx.WriteError(w, 409, "профиль с таким именем уже есть")
		return
	}
	id, _ := res.LastInsertId()
	httpx.WriteJSON(w, 201, profileView{ID: id, Name: in.Name, AllowAdult: in.AllowAdult, CanDelete: in.CanDelete})
}

func (a *App) handleProfileDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		httpx.WriteError(w, 400, "неверный профиль")
		return
	}
	var in struct {
		PIN string `json:"pin"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	var h string
	if err := a.DB.R.QueryRowContext(r.Context(), `SELECT pin_hash FROM family_profiles WHERE id=?`, id).Scan(&h); err != nil {
		httpx.WriteError(w, 404, "профиль не найден")
		return
	}
	if !verifyPIN(h, in.PIN) {
		httpx.WriteError(w, 401, "неверный PIN")
		return
	}
	if _, err := a.DB.W.ExecContext(r.Context(), `DELETE FROM family_profiles WHERE id=?`, id); err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (a *App) handleProfileActivate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID  int64  `json:"id"`
		PIN string `json:"pin"`
	}
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	var h string
	if err := a.DB.R.QueryRowContext(r.Context(), `SELECT pin_hash FROM family_profiles WHERE id=?`, in.ID).Scan(&h); err != nil {
		httpx.WriteError(w, 404, "профиль не найден")
		return
	}
	if !verifyPIN(h, in.PIN) {
		httpx.WriteError(w, 401, "неверный PIN")
		return
	}
	_, err := a.DB.W.ExecContext(r.Context(), `INSERT INTO family_devices(device,profile_id,updated_at) VALUES(?,?,?) ON CONFLICT(device) DO UPDATE SET profile_id=excluded.profile_id,updated_at=excluded.updated_at`, httpx.Device(r), in.ID, time.Now().UnixMilli())
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) handleProfileCurrent(w http.ResponseWriter, r *http.Request) {
	var p profileView
	var aa, dd int
	if err := a.DB.R.QueryRowContext(r.Context(), `SELECT p.id,p.name,p.allow_adult,p.can_delete FROM family_devices d JOIN family_profiles p ON p.id=d.profile_id WHERE d.device=?`, httpx.Device(r)).Scan(&p.ID, &p.Name, &aa, &dd); errors.Is(err, sql.ErrNoRows) {
		httpx.WriteJSON(w, 200, nil)
		return
	} else if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	p.AllowAdult = aa != 0
	p.CanDelete = dd != 0
	p.Active = true
	httpx.WriteJSON(w, 200, p)
}

func (a *App) handleRemoteInput(w http.ResponseWriter, r *http.Request) {
	if a.p1 == nil {
		httpx.WriteError(w, 503, "пульт не готов")
		return
	}
	var in remoteCommandInput
	if !httpx.ReadJSON(w, r, &in) {
		return
	}
	c, ok := a.p1.appendRemoteCommand(r.Header.Get("X-Kinodom-Remote"), in)
	if !ok {
		httpx.WriteError(w, 401, "недействительный пульт")
		return
	}
	httpx.WriteJSON(w, 202, c)
}
func (a *App) handleRemoteCommands(w http.ResponseWriter, r *http.Request) {
	if a.p1 == nil {
		httpx.WriteError(w, 503, "пульт не готов")
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	cs, cursor := a.p1.remoteCommands(since)
	httpx.WriteJSON(w, 200, map[string]any{"cursor": cursor, "commands": cs})
}

// canDelete is used by destructive handlers as soon as a profile is active. No
// active profile preserves the existing single-user behaviour.
func (a *App) canDelete(ctx context.Context, device string) bool {
	var v int
	err := a.DB.R.QueryRowContext(ctx, `SELECT p.can_delete FROM family_devices d JOIN family_profiles p ON p.id=d.profile_id WHERE d.device=?`, device).Scan(&v)
	return errors.Is(err, sql.ErrNoRows) || (err == nil && v != 0)
}
