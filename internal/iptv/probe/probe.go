// Package probe — проверка источников IPTV «будет ли тормозить» (спека этапа 8, раздел 5.8): лёгкая
// (жив ли) и полная (запас скорости у HLS, паузы у живого потока).
package probe

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"kinodom/internal/iptv/m3u"
)

// Оценки.
const (
	GradeAlive  = "alive" // лёгкая прошла
	GradeGreen  = "green"
	GradeYellow = "yellow"
	GradeRed    = "red"
	GradeBlack  = "black" // не отвечает
)

// StubError — причина у заглушки вместо эфира: плейлист потока — запись (замечание № 15 этапа 11b).
const StubError = "заглушка вместо эфира"

// UserAgent — как у VLC: так источник откроет и плеер (спека, раздел 5.8).
const UserAgent = "VLC/3.0.20 LibVLC/3.0.20"

// Target — что проверять.
type Target struct {
	URL     string
	Kind    string // hls, dash, live; "" — определить
	Headers m3u.Headers
}

// Result — итог проверки.
type Result struct {
	Grade  string
	Kind   string
	TTFB   time.Duration // до первых данных первого запроса
	Ratio  float64       // запас скорости у HLS; 0 — не мерили
	Mbps   float64       // битрейт, Мбит/с; 0 — неизвестен
	Height int           // высота кадра лучшего варианта HLS; 0 — неизвестна
	Audio  *bool         // есть ли звук (полная проверка); nil — не понять
	Error  string        // текст для человека; "" — нет ошибки
}

// Prober — проверки. Поля с нулём — значения спеки.
type Prober struct {
	Client     *http.Client
	Timeout    time.Duration // на запрос; 0 — 8 с
	SegmentCap int64         // сколько качать от сегмента HLS; 0 — 512 КБ
	LiveFor    time.Duration // приём живого потока; 0 — 3 с
	Pause      time.Duration // пауза живого потока, которая считается остановкой; 0 — 1 с
}

func (p *Prober) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 8 * time.Second
}

func (p *Prober) segmentCap() int64 {
	if p.SegmentCap > 0 {
		return p.SegmentCap
	}
	return 512 << 10
}

func (p *Prober) liveFor() time.Duration {
	if p.LiveFor > 0 {
		return p.LiveFor
	}
	return 3 * time.Second
}

func (p *Prober) pause() time.Duration {
	if p.Pause > 0 {
		return p.Pause
	}
	return time.Second
}

// errText — ошибка для человека: «HTTP 403», «нет ответа за 8 с», «адрес не найден (DNS)».
type errText string

func (e errText) Error() string { return string(e) }

func (p *Prober) describe(err error) string {
	var et errText
	var dns *net.DNSError
	switch {
	case errors.As(err, &et):
		return string(et)
	case errors.As(err, &dns):
		return "адрес не найден (DNS)"
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return "нет ответа за " + strings.Replace(strconv.FormatFloat(p.timeout().Seconds(), 'f', -1, 64), ".", ",", 1) + " с"
	case errors.Is(err, syscall.ECONNREFUSED), strings.Contains(err.Error(), "refused"):
		return "соединение отклонено"
	case strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "x509"):
		return "ошибка защищённого соединения"
	}
	return "обрыв соединения"
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// open — GET с заголовками источника; ответ не 2xx — ошибка «HTTP N».
func (p *Prober) open(ctx context.Context, raw string, h m3u.Headers) (*http.Response, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, 0, errText("неверная ссылка")
	}
	ua := h.UserAgent
	if ua == "" {
		ua = UserAgent
	}
	req.Header.Set("User-Agent", ua)
	if h.Referrer != "" {
		req.Header.Set("Referer", h.Referrer)
	}
	start := time.Now()
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		return nil, 0, errText("HTTP " + strconv.Itoa(resp.StatusCode))
	}
	return resp, time.Since(start), nil
}

// fetch — небольшой документ (плейлист) целиком, не больше 1 МБ.
func (p *Prober) fetch(ctx context.Context, raw string, h m3u.Headers) ([]byte, *url.URL, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	resp, ttfb, err := p.open(ctx, raw, h)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, 0, err
	}
	return b, resp.Request.URL, ttfb, nil
}

// Light — жив ли источник: у HLS — плейлист с сегментами (у мастер-плейлиста — плейлист первого
// варианта), у DASH — документ MPD, у живого потока — первые 64 КБ.
func (p *Prober) Light(ctx context.Context, t Target) Result {
	r, _, _ := p.start(ctx, t, false)
	return r
}

// Full — полная проверка: у HLS — вариант с наибольшим BANDWIDTH, последний сегмент (не больше
// SegmentCap), запас скорости; у живого потока — приём LiveFor и паузы; DASH — как лёгкая.
func (p *Prober) Full(ctx context.Context, t Target) Result {
	r, media, base := p.start(ctx, t, true)
	if r.Grade != GradeAlive || r.Kind != m3u.KindHLS {
		return r
	}
	return p.segment(ctx, t, r, media, base)
}

// start — первый запрос и разбор: вид источника, плейлист варианта. Для полной проверки живого
// потока — сразу приём с паузами. Таймаут первого ответа — таймер: у живого потока он снимается,
// как только пошли данные (отмена контекста оборвала бы и приём).
func (p *Prober) start(ctx context.Context, t Target, full bool) (Result, *playlist, *url.URL) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var fired atomic.Bool
	timer := time.AfterFunc(p.timeout(), func() {
		fired.Store(true)
		cancel()
	})
	defer timer.Stop()
	fail := func(r Result, err error) (Result, *playlist, *url.URL) {
		if fired.Load() {
			err = context.DeadlineExceeded
		}
		r.Grade, r.Error = GradeBlack, p.describe(err)
		return r, nil, nil
	}
	resp, ttfb, err := p.open(ctx, t.URL, t.Headers)
	if err != nil {
		return fail(Result{Kind: t.Kind}, err)
	}
	defer resp.Body.Close()
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	head, _ := br.Peek(512)
	r := Result{TTFB: ttfb, Kind: t.Kind}
	text := strings.TrimSpace(string(bytes.TrimPrefix(head, []byte{0xEF, 0xBB, 0xBF})))
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case len(head) == 0:
		return fail(r, errText("пустой ответ"))
	case strings.HasPrefix(text, "#EXTM3U"):
		r.Kind = m3u.KindHLS
	case strings.Contains(text, "<MPD"):
		r.Kind, r.Grade = m3u.KindDASH, GradeAlive
		return r, nil, nil
	case strings.HasPrefix(ct, "text/html") || strings.HasPrefix(text, "<"):
		r.Grade, r.Error = GradeBlack, "ответ — не видео"
		return r, nil, nil
	default:
		r.Kind = m3u.KindLive
		if full {
			if !timer.Stop() {
				return fail(r, context.DeadlineExceeded)
			}
			return p.live(ctx, br, r), nil, nil
		}
		n, err := io.CopyN(io.Discard, br, 64<<10)
		if n < 64<<10 {
			if err == nil || errors.Is(err, io.EOF) {
				err = errText("поток оборвался")
			}
			return fail(r, err)
		}
		r.Grade = GradeAlive
		return r, nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(br, 1<<20))
	if err != nil {
		return fail(r, err)
	}
	timer.Stop()
	base := resp.Request.URL
	pl := parsePlaylist(string(body))
	if len(pl.variants) > 0 {
		v := pl.variants[0] // лёгкая — первый вариант
		separate := false
		if full {
			v = pl.best()
			r.Audio = codecsAudio(v.codecs, v.audio != "" && pl.audioMedia)
			separate = v.audio != "" && pl.audioURI[v.audio]
		}
		for _, x := range pl.variants {
			r.Height = max(r.Height, x.height)
		}
		u, err := base.Parse(v.uri)
		if err != nil {
			r.Grade, r.Error = GradeBlack, "плейлист не читается"
			return r, nil, nil
		}
		b, final, _, err := p.fetch(ctx, u.String(), t.Headers)
		if err != nil {
			return fail(r, err)
		}
		media := parsePlaylist(string(b))
		media.bandwidth, media.separateAudio = v.bandwidth, separate
		pl, base = media, final
	}
	if len(pl.segments) == 0 {
		r.Grade, r.Error = GradeBlack, "плейлист пуст"
		return r, nil, nil
	}
	if pl.recorded {
		// Запись, а не эфир: заглушка провайдера («не показывает видео на этой территории» — Ростелеком
		// вне своей зоны), плеер покажет её по кругу (замечание № 15 этапа 11b).
		r.Grade, r.Error = GradeBlack, StubError
		return r, nil, nil
	}
	r.Grade = GradeAlive
	return r, &pl, base
}

// segment — последний сегмент HLS: скорость ÷ битрейт.
func (p *Prober) segment(ctx context.Context, t Target, r Result, pl *playlist, base *url.URL) Result {
	seg := pl.segments[len(pl.segments)-1]
	u, err := base.Parse(seg.uri)
	if err != nil {
		r.Grade, r.Error = GradeRed, "сегмент не скачался"
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	resp, _, err := p.open(ctx, u.String(), t.Headers)
	if err != nil {
		r.Grade, r.Error = GradeRed, "сегмент не скачался"
		return r
	}
	defer resp.Body.Close()
	first := make([]byte, 1)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		r.Grade, r.Error = GradeRed, "сегмент не скачался"
		return r
	}
	start := time.Now()
	data := bytes.NewBuffer(first)
	n, err := io.CopyN(data, resp.Body, p.segmentCap()-1)
	elapsed := time.Since(start)
	whole := errors.Is(err, io.EOF)
	if err != nil && !whole {
		r.Grade, r.Error = GradeRed, "сегмент не скачался"
		return r
	}
	// Таблица дорожек сегмента точнее CODECS — кроме звука отдельной дорожкой: в сегментах видео его нет.
	if a := tsAudio(data.Bytes()); a != nil && !pl.separateAudio {
		r.Audio = a
	}
	got := float64(n + 1)
	// Битрейт, байт/с: BANDWIDTH варианта, иначе размер сегмента ÷ длительность, иначе 4 Мбит/с.
	bitrate := float64(pl.bandwidth) / 8
	if bitrate <= 0 && seg.duration > 0 {
		size := float64(resp.ContentLength)
		if size <= 0 && whole {
			size = got
		}
		if size > 0 {
			bitrate = size / seg.duration
		}
	}
	if bitrate <= 0 {
		bitrate = 4e6 / 8
	}
	r.Mbps = bitrate * 8 / 1e6
	if elapsed < time.Millisecond {
		elapsed = time.Millisecond
	}
	r.Ratio = got / elapsed.Seconds() / bitrate
	switch {
	case r.Ratio >= 1.5:
		r.Grade = GradeGreen
	case r.Ratio >= 1:
		r.Grade = GradeYellow
	default:
		r.Grade = GradeRed
	}
	return r
}

// live — приём живого потока LiveFor от первого байта: паузы дольше Pause — остановки.
func (p *Prober) live(ctx context.Context, body io.Reader, r Result) Result {
	type chunk struct {
		n    int
		data []byte // копия — пока не набрали начало потока для таблицы дорожек
		at   time.Time
		err  error
	}
	ch := make(chan chunk, 16)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		buf := make([]byte, 32<<10)
		kept := 0
		for {
			n, err := body.Read(buf)
			var data []byte
			if kept < liveHead {
				data = append([]byte(nil), buf[:n]...)
				kept += n
			}
			select {
			case ch <- chunk{n, data, time.Now(), err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var first, last time.Time
	var total int64
	var head []byte
	pauses := 0
	grade := func(r Result, pauses int, total int64, d time.Duration) Result {
		r.Audio = tsAudio(head)
		return grade(r, pauses, total, d)
	}
	timer := time.NewTimer(p.timeout())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			r.Grade, r.Error = GradeBlack, "нет ответа"
			return r
		case <-timer.C:
			if first.IsZero() {
				r.Grade, r.Error = GradeBlack, p.describe(context.DeadlineExceeded)
				return r
			}
			if time.Since(last) > p.pause() { // поток встал к концу приёма
				pauses++
			}
			return grade(r, pauses, total, time.Since(first))
		case c := <-ch:
			if c.n > 0 {
				if first.IsZero() {
					first = c.at
					timer.Reset(p.liveFor())
				} else if c.at.Sub(last) > p.pause() {
					pauses++
				}
				last = c.at
				total += int64(c.n)
				head = append(head, c.data...)
			}
			if c.err != nil {
				if first.IsZero() || c.at.Sub(first) < p.liveFor()/2 {
					err := c.err
					if errors.Is(err, io.EOF) {
						err = errText("поток оборвался")
					}
					r.Grade, r.Error = GradeBlack, p.describe(err)
					return r
				}
				return grade(r, pauses+1, total, c.at.Sub(first))
			}
		}
	}
}

func grade(r Result, pauses int, total int64, d time.Duration) Result {
	if d > 0 {
		r.Mbps = float64(total) * 8 / d.Seconds() / 1e6
	}
	switch pauses {
	case 0:
		r.Grade = GradeGreen
	case 1:
		r.Grade = GradeYellow
	default:
		r.Grade = GradeRed
	}
	return r
}

type variant struct {
	uri       string
	bandwidth int64
	height    int
	codecs    string // CODECS; "" — не указаны
	audio     string // группа AUDIO (EXT-X-MEDIA); "" — нет
}

// liveHead — сколько начала живого потока держать для таблицы дорожек (звук).
const liveHead = 1 << 20

type segment struct {
	uri      string
	duration float64
}

type playlist struct {
	variants   []variant
	segments   []segment
	bandwidth  int64 // у плейлиста варианта — BANDWIDTH из мастер-плейлиста
	audioMedia bool  // у мастер-плейлиста есть EXT-X-MEDIA:TYPE=AUDIO
	// audioURI — группы AUDIO, у которых звук отдельной дорожкой (EXT-X-MEDIA с URI).
	audioURI map[string]bool
	// separateAudio — у плейлиста варианта звук отдельной дорожкой: в его сегментах звука нет.
	separateAudio bool
	// recorded — конец записи (#EXT-X-ENDLIST, #EXT-X-PLAYLIST-TYPE:VOD): у эфира его не бывает.
	recorded bool
}

func (pl playlist) best() variant {
	b := pl.variants[0]
	for _, v := range pl.variants[1:] {
		if v.bandwidth > b.bandwidth {
			b = v
		}
	}
	return b
}

// mediaAttr — значение атрибута тега без кавычек; ok — атрибут есть.
func mediaAttr(line, name string) (string, bool) {
	_, attrs, _ := strings.Cut(line, ":")
	for _, kv := range splitAttrs(attrs) {
		if k, val, _ := strings.Cut(kv, "="); strings.EqualFold(k, name) {
			return strings.Trim(val, `"`), true
		}
	}
	return "", false
}

func parsePlaylist(s string) playlist {
	var pl playlist
	var pending *variant
	dur := 0.0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			v := variant{}
			for _, kv := range splitAttrs(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:")) {
				k, val, _ := strings.Cut(kv, "=")
				switch strings.ToUpper(k) {
				case "BANDWIDTH":
					v.bandwidth, _ = strconv.ParseInt(val, 10, 64)
				case "RESOLUTION":
					if _, h, ok := strings.Cut(val, "x"); ok {
						v.height, _ = strconv.Atoi(h)
					}
				case "CODECS":
					v.codecs = strings.Trim(val, `"`)
				case "AUDIO":
					v.audio = strings.Trim(val, `"`)
				}
			}
			pending = &v
		case strings.HasPrefix(line, "#EXT-X-MEDIA:") && strings.Contains(strings.ToUpper(line), "TYPE=AUDIO"):
			pl.audioMedia = true
			if group, ok := mediaAttr(line, "GROUP-ID"); ok {
				if _, uri := mediaAttr(line, "URI"); uri {
					if pl.audioURI == nil {
						pl.audioURI = map[string]bool{}
					}
					pl.audioURI[group] = true
				}
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			d, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			dur, _ = strconv.ParseFloat(strings.TrimSpace(d), 64)
		case line == "#EXT-X-ENDLIST":
			pl.recorded = true
		case strings.HasPrefix(line, "#EXT-X-PLAYLIST-TYPE:"):
			pl.recorded = pl.recorded || strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-PLAYLIST-TYPE:")), "VOD")
		case strings.HasPrefix(line, "#"):
		case pending != nil:
			pending.uri = line
			pl.variants = append(pl.variants, *pending)
			pending = nil
		default:
			pl.segments = append(pl.segments, segment{uri: line, duration: dur})
			dur = 0
		}
	}
	return pl
}

// splitAttrs — атрибуты через запятую, запятые в кавычках не делят (CODECS="avc1,mp4a").
func splitAttrs(s string) []string {
	var out []string
	quoted, start := false, 0
	for i, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// String — для журнала.
func (r Result) String() string {
	return fmt.Sprintf("%s %s %.2f× %s", r.Grade, r.Kind, r.Ratio, r.Error)
}
