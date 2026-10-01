package iptv

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kinodom/internal/httpx"
	"kinodom/internal/iptv/m3u"
	"kinodom/internal/iptv/probe"
)

// Пересылка источника в пульт (план 14Д): «Смотреть» и «Кадры» в настройках каналов. Сервер ходит только к
// известным ему источникам — по номеру; ссылки внутри списков HLS подписаны ключом этого запуска, поэтому
// чужой адрес через пересылку не открыть. Заголовки — User-Agent и Referer записи плейлиста.

// relayWait — сколько ждать ответа источника (заголовков); тело потока не ограничено — он живой.
var relayWait = 15 * time.Second

// newRelayKey — ключ подписи ссылок пересылки: свой у каждого запуска.
func newRelayKey() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func (m *Module) sign(id int64, u string) string {
	mac := hmac.New(sha256.New, m.relayKey)
	fmt.Fprintf(mac, "%d|%s", id, u)
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// relayLink — ссылка пересылки адреса abs источника id.
func (m *Module) relayLink(id int64, abs string) string {
	return "/api/v1/iptv/relay?" + url.Values{"s": {strconv.FormatInt(id, 10)}, "u": {abs}, "sig": {m.sign(id, abs)}}.Encode()
}

var reURIAttr = regexp.MustCompile(`URI="([^"]*)"`)

// rewritePlaylist — список HLS со ссылками через link: строки-адреса (сегменты, вложенные списки) и атрибуты
// URI="…" (ключи, карты, дорожки); относительные — от base. Остальные строки — как были.
func rewritePlaylist(body []byte, base *url.URL, link func(abs string) string) []byte {
	resolve := func(ref string) string {
		u, err := base.Parse(strings.TrimSpace(ref))
		if err != nil {
			return ref
		}
		return u.String()
	}
	lines := strings.Split(string(body), "\n")
	for i, l := range lines {
		t := strings.TrimRight(l, "\r")
		switch {
		case strings.TrimSpace(t) == "":
		case strings.HasPrefix(t, "#"):
			if strings.Contains(t, `URI="`) {
				lines[i] = reURIAttr.ReplaceAllStringFunc(t, func(a string) string {
					return `URI="` + link(resolve(reURIAttr.FindStringSubmatch(a)[1])) + `"`
				})
			}
		default:
			lines[i] = link(resolve(t))
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// streamFor — источник по номеру и заголовки его записей (первая запись с User-Agent или Referer).
func (m *Module) streamFor(id int64) (url, kind string, h m3u.Headers, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pool == nil {
		return "", "", h, false
	}
	s := m.pool.streams[id]
	if s == nil {
		return "", "", h, false
	}
	for _, e := range s.Entries {
		if e.Headers.UserAgent != "" || e.Headers.Referrer != "" {
			h = e.Headers
			break
		}
	}
	return s.URL, s.Kind, h, true
}

// crossSite — запрос пришёл с чужого сайта (страница в браузере человека открывает пересылку): 403. Пульт и
// плеер ходят со своего адреса или без этого заголовка.
func crossSite(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") != "cross-site" {
		return false
	}
	httpx.WriteError(w, http.StatusForbidden, "запрос с чужого сайта")
	return true
}

// relayType — тип ответа пересылки: видео и звук — как у источника, остальное — просто байты. Страница
// источника, отданная страницей, работала бы от имени пульта (ревью 14Д, п. 2).
func relayType(ct string) string {
	if t := strings.ToLower(ct); strings.HasPrefix(t, "video/") || strings.HasPrefix(t, "audio/") {
		return ct
	}
	return "application/octet-stream"
}

func relayID(w http.ResponseWriter, s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер источника")
		return 0, false
	}
	return id, true
}

// handleWatch — GET /api/v1/iptv/streams/{id}/watch: источник для плеера пульта. HLS — список через
// пересылку, поток — как есть, DASH — 409 (в браузере не смотрим, «Открыть в VLC»).
func (m *Module) handleWatch(w http.ResponseWriter, r *http.Request) {
	if crossSite(w, r) {
		return
	}
	id, ok := relayID(w, r.PathValue("id"))
	if !ok {
		return
	}
	u, kind, h, ok := m.streamFor(id)
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "такого источника нет")
		return
	}
	if kind == "dash" || strings.HasSuffix(strings.ToLower(strings.SplitN(u, "?", 2)[0]), ".mpd") {
		httpx.WriteError(w, http.StatusConflict, "DASH в пульте не показывается — откройте в VLC")
		return
	}
	m.relay(w, r, id, u, h)
}

// handleRelay — GET /api/v1/iptv/relay?s=&u=&sig=: адрес из списка источника s, подпись — этого запуска.
func (m *Module) handleRelay(w http.ResponseWriter, r *http.Request) {
	if crossSite(w, r) {
		return
	}
	q := r.URL.Query()
	id, ok := relayID(w, q.Get("s"))
	if !ok {
		return
	}
	u, sig := q.Get("u"), q.Get("sig")
	if !hmac.Equal([]byte(sig), []byte(m.sign(id, u))) {
		httpx.WriteError(w, http.StatusForbidden, "ссылка не от этого сервера")
		return
	}
	if _, _, h, ok := m.streamFor(id); ok {
		m.relay(w, r, id, u, h)
		return
	}
	httpx.WriteError(w, http.StatusNotFound, "такого источника нет")
}

// relay — адрес u источника id: список HLS — переписанный, остальное — байты как есть, пока клиент читает.
func (m *Module) relay(w http.ResponseWriter, r *http.Request, id int64, u string, h m3u.Headers) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		httpx.WriteError(w, http.StatusBadRequest, "адрес источника не http(s)")
		return
	}
	ua := h.UserAgent
	if ua == "" {
		ua = probe.UserAgent
	}
	req.Header.Set("User-Agent", ua)
	if h.Referrer != "" {
		req.Header.Set("Referer", h.Referrer)
	}
	// Ответа (заголовков) ждём relayWait; дальше поток идёт, пока клиент читает.
	late := time.AfterFunc(relayWait, cancel)
	resp, err := m.client.Do(req)
	waited := !late.Stop()
	switch {
	case r.Context().Err() != nil:
		if err == nil {
			resp.Body.Close()
		}
		return
	case waited:
		if err == nil {
			resp.Body.Close()
		}
		httpx.WriteError(w, http.StatusGatewayTimeout, fmt.Sprintf("источник не ответил за %d с", int(relayWait/time.Second)))
		return
	case err != nil:
		httpx.WriteError(w, http.StatusBadGateway, "источник не отвечает")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	if resp.StatusCode >= 400 {
		httpx.WriteError(w, http.StatusBadGateway, fmt.Sprintf("источник ответил %d", resp.StatusCode))
		return
	}
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	head, _ := br.Peek(7)
	final := resp.Request.URL // после переадресаций — относительные ссылки от него
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "mpegurl") ||
		strings.HasSuffix(strings.ToLower(final.Path), ".m3u8") || bytes.Equal(head, []byte("#EXTM3U")) {
		body, err := io.ReadAll(io.LimitReader(br, 4<<20))
		if err != nil {
			httpx.WriteError(w, http.StatusBadGateway, "список источника оборвался")
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(rewritePlaylist(body, final, func(abs string) string { return m.relayLink(id, abs) }))
		return
	}
	w.Header().Set("Content-Type", relayType(resp.Header.Get("Content-Type")))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // клиент ушёл — запрос к источнику закроется вместе с ним
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
