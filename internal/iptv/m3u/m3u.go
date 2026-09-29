// Package m3u — разбор плейлистов IPTV (спека этапа 8, раздел 5.2) и нормализация названий каналов
// для сопоставления с телепрограммой (раздел 5.3).
package m3u

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// Headers — заголовки, с которыми источник нужно открывать (из плейлиста).
type Headers struct {
	UserAgent string `json:"userAgent,omitempty"`
	Referrer  string `json:"referrer,omitempty"`
}

// Entry — запись плейлиста: поток и то, как его подписали.
type Entry struct {
	Name    string // название после запятой в #EXTINF
	TvgID   string
	TvgName string
	Logo    string
	Group   string // group-title, иначе #EXTGRP
	Shift   int    // tvg-shift, часы; 0 — нет
	Headers Headers
	URL     string // http(s)
}

// Playlist — разобранный плейлист: только записи с http(s); остальные схемы (udp, rtp, rtmp…)
// посчитаны в Unsupported.
type Playlist struct {
	Entries     []Entry
	Unsupported int
}

// Parse разбирает плейлист. Текст не UTF-8 — windows-1251. Записи без ссылки пропускаются.
func Parse(b []byte) Playlist {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}) // BOM
	if !utf8.Valid(b) {
		if d, err := charmap.Windows1251.NewDecoder().Bytes(b); err == nil {
			b = d
		}
	}
	var p Playlist
	var cur *Entry
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXTINF"):
			cur = parseInf(line)
		case cur == nil:
			// строка до первой #EXTINF — заголовок #EXTM3U и прочее
		case strings.HasPrefix(line, "#EXTGRP:"):
			if cur.Group == "" {
				cur.Group = strings.TrimSpace(strings.TrimPrefix(line, "#EXTGRP:"))
			}
		case strings.HasPrefix(line, "#EXTVLCOPT:"):
			opt := strings.TrimPrefix(line, "#EXTVLCOPT:")
			if v, ok := strings.CutPrefix(opt, "http-user-agent="); ok {
				cur.Headers.UserAgent = v
			} else if v, ok := strings.CutPrefix(opt, "http-referrer="); ok {
				cur.Headers.Referrer = v
			}
		case strings.HasPrefix(line, "#EXTHTTP:"):
			var h map[string]string
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "#EXTHTTP:")), &h) == nil {
				for k, v := range h {
					switch strings.ToLower(k) {
					case "user-agent":
						cur.Headers.UserAgent = v
					case "referer", "referrer":
						cur.Headers.Referrer = v
					}
				}
			}
		case strings.HasPrefix(line, "#"):
		default:
			e := *cur
			cur = nil
			e.URL, e.Headers = splitKodi(line, e.Headers)
			u, err := url.Parse(e.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				p.Unsupported++
				continue
			}
			p.Entries = append(p.Entries, e)
		}
	}
	return p
}

var reAttr = regexp.MustCompile(`([A-Za-z0-9_-]+)="([^"]*)"`)

// parseInf — строка #EXTINF:-1 атрибуты,Название. Название — после первой запятой вне кавычек.
func parseInf(line string) *Entry {
	head, name := line, ""
	quoted := false
	for i, r := range line {
		if r == '"' {
			quoted = !quoted
		} else if r == ',' && !quoted {
			head, name = line[:i], line[i+1:]
			break
		}
	}
	e := &Entry{Name: strings.TrimSpace(name)}
	for _, m := range reAttr.FindAllStringSubmatch(head, -1) {
		v := strings.TrimSpace(m[2])
		switch strings.ToLower(m[1]) {
		case "tvg-id":
			e.TvgID = v
		case "tvg-name":
			e.TvgName = v
		case "tvg-logo":
			e.Logo = v
		case "group-title":
			e.Group = v
		case "tvg-shift":
			if f, err := strconv.ParseFloat(strings.TrimPrefix(v, "+"), 64); err == nil && f == float64(int(f)) {
				e.Shift = int(f)
			}
		case "user-agent", "http-user-agent":
			// В «меге» бывает значение вместе с префиксом строки VLC: "#EXTVLCOPT:http-user-agent=…".
			e.Headers.UserAgent = strings.TrimPrefix(v, "#EXTVLCOPT:http-user-agent=")
		case "http-referrer", "referrer", "http-referer", "referer":
			e.Headers.Referrer = v
		}
	}
	return e
}

// splitKodi — заголовки после «|» в ссылке (как пишут для Kodi): http://…|User-Agent=…&Referer=….
func splitKodi(line string, h Headers) (string, Headers) {
	u, opts, ok := strings.Cut(line, "|")
	if !ok {
		return line, h
	}
	for _, kv := range strings.Split(opts, "&") {
		k, v, _ := strings.Cut(kv, "=")
		if dv, err := url.QueryUnescape(v); err == nil {
			v = dv
		}
		switch strings.ToLower(k) {
		case "user-agent":
			h.UserAgent = v
		case "referer", "referrer":
			h.Referrer = v
		}
	}
	return strings.TrimSpace(u), h
}

// Виды источников по ссылке; "" — неизвестен, определит первая проверка.
const (
	KindHLS  = "hls"
	KindDASH = "dash"
	KindLive = "live" // живой поток (не HLS) — так его называет проверка
)

// KindOf — вид источника по ссылке: .m3u8 в пути или запросе — HLS, .mpd — DASH.
func KindOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := strings.ToLower(u.Path)
	q := strings.ToLower(u.RawQuery)
	switch {
	case strings.HasSuffix(p, ".m3u8") || strings.Contains(q, "m3u8"):
		return KindHLS
	case strings.HasSuffix(p, ".mpd"):
		return KindDASH
	}
	return ""
}

// QualityOf — качество по названию: 4K, FHD, HD, SD; "" — не указано.
func QualityOf(name string) string {
	best := ""
	rank := map[string]int{"": 0, "SD": 1, "HD": 2, "FHD": 3, "4K": 4}
	for _, w := range words(strings.ToLower(name)) {
		q := ""
		switch w {
		case "4k", "uhd", "2160p", "2160i":
			q = "4K"
		case "fhd", "1080p", "1080i":
			q = "FHD"
		case "hd", "720p", "720i":
			q = "HD"
		case "sd", "576p", "576i", "480p", "480i", "360p":
			q = "SD"
		}
		if rank[q] > rank[best] {
			best = q
		}
	}
	return best
}
