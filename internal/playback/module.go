package playback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"kinodom/internal/httpx"
)

// ProbeTimeout — сколько ждать сведений и ключевого кадра: у раздачи заголовок и индекс могут ещё качаться
// (Review Focus 1). Тесты уменьшают.
var ProbeTimeout = 30 * time.Second

// Ref — файл плеера: раздача и номер файла или файл медиатеки.
type Ref struct {
	Kind  string // "torrent" или "library"
	Hash  string
	Index int
	File  int64
}

// Src — адрес файла в маршрутах: torrent/<hash>/<index> или library/<file>.
func (r Ref) Src() string {
	if r.Kind == "torrent" {
		return "torrent/" + r.Hash + "/" + strconv.Itoa(r.Index)
	}
	return "library/" + strconv.FormatInt(r.File, 10)
}

// Source — что модуль раздач или медиатеки знает о файле для плеера.
type Source struct {
	Title     string
	Hash      string // ключ истории
	Index     int
	Path      string   // поток на этом сервере, без own=1: /stream/… или /media/… (имя экранировано)
	M3U       string   // .m3u8 с местом (как у «Смотреть»)
	Launch    *string  // kinodom:// — только запросу с этого ПК
	StartSec  int      // откуда открыть; 0 — с начала
	SubFiles  []string // внешние субтитры на диске
	Prev      *Ref
	PrevTitle string

	Next      *Ref
	NextTitle string
}

// Resolver — модули раздач и медиатеки. prepare — «Смотреть» (у раздачи — выбрать файл, качать первым);
// без него — только поток уже выбранного файла.
type Resolver interface {
	Resolve(r *http.Request, ref Ref, prepare, fromStart bool) (Source, error)
}

// StatusError — ошибка модуля с кодом ответа.
type StatusError struct {
	Code int
	Text string
}

func (e *StatusError) Error() string { return e.Text }

// Router — маршруты API.
type Router interface {
	Handle(pattern, module string, h http.Handler)
}

type Options struct {
	Tools  Tools
	OK     bool // ffmpeg и ffprobe найдены
	Res    Resolver
	Origin func() string // http://127.0.0.1:<порт> — откуда ffmpeg читает файл
	Log    *slog.Logger
}

// Module — маршруты своего плеера (спека 18, 3.2–3.5).
type Module struct {
	o    Options
	runs *Manager
	mu   sync.Mutex
	seen map[string]cached     // сведения по Src
	cues map[string]cachedCues // реплики внешних субтитров по пути файла
	tr   *bool                 // ffmpeg умеет перекод видео (libvpx); nil — ещё не спрашивали
}

type cachedCues struct {
	c  []cue
	at time.Time
}

type cached struct {
	m  Media
	at time.Time
}

func New(o Options) *Module {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Module{o: o, runs: NewManager(MaxPlayers), seen: map[string]cached{}, cues: map[string]cachedCues{}}
}

// Available — ffmpeg есть: в «Состоянии» — transcoder.
func (m *Module) Available() bool { return m.o.OK }

// transcoding — ffmpeg умеет перекод видео: в сведениях поле trans; старая сборка ffmpeg (без
// libvpx) — нет, и браузер честно зовёт во внешний плеер. Один запрос на процесс.
func (m *Module) transcoding() bool {
	m.mu.Lock()
	if m.tr != nil {
		v := *m.tr
		m.mu.Unlock()
		return v
	}
	m.mu.Unlock()
	if !m.o.OK {
		m.mu.Lock()
		v := false
		m.tr = &v
		m.mu.Unlock()
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
	defer cancel()
	out, err := m.o.Tools.output(ctx, m.o.Tools.FFmpeg, "-hide_banner", "-encoders")
	v := err == nil && strings.Contains(string(out), "libvpx")
	m.mu.Lock()
	m.tr = &v
	m.mu.Unlock()
	return v
}

func (m *Module) Register(r Router) {
	for _, p := range []struct {
		path string
		ref  func(*http.Request) (Ref, bool)
	}{{"torrent/{hash}/{index}", torrentRef}, {"library/{file}", libraryRef}} {
		r.Handle("GET /api/v1/play/"+p.path, "", m.handle(p.ref, m.info))
		r.Handle("GET /api/v1/play/"+p.path+"/keyframe", "", m.handle(p.ref, m.keyframe))
		r.Handle("GET /play/"+p.path+"/stream.ts", "", m.handle(p.ref, m.stream("ts")))
		r.Handle("GET /play/"+p.path+"/stream.mkv", "", m.handle(p.ref, m.stream("mkv")))
		r.Handle("GET /play/"+p.path+"/stream.webm", "", m.handle(p.ref, m.stream("webm")))
		r.Handle("GET /play/"+p.path+"/subs/{sub}", "", m.handle(p.ref, m.subs))
	}
}

var hexHash = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func torrentRef(r *http.Request) (Ref, bool) {
	i, err := strconv.Atoi(r.PathValue("index"))
	h := r.PathValue("hash")
	return Ref{Kind: "torrent", Hash: strings.ToLower(h), Index: i}, err == nil && i >= 0 && hexHash.MatchString(h)
}

func libraryRef(r *http.Request) (Ref, bool) {
	f, err := strconv.ParseInt(r.PathValue("file"), 10, 64)
	return Ref{Kind: "library", File: f}, err == nil && f > 0
}

type handler func(w http.ResponseWriter, r *http.Request, ref Ref)

func (m *Module) handle(parse func(*http.Request) (Ref, bool), h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Чужая страница (Sec-Fetch-Site: cross-site) — нет: эти GET выбирают файл раздачи и запускают ffmpeg (ревью 18А).
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			httpx.WriteError(w, http.StatusForbidden, "Плеер открывается только из пульта Kinodom")
			return
		}
		if !m.o.OK {
			httpx.WriteError(w, http.StatusServiceUnavailable, "Плеер в браузере недоступен: нет ffmpeg рядом с программой")
			return
		}
		ref, ok := parse(r)
		if !ok {
			httpx.WriteError(w, http.StatusBadRequest, "неверный адрес файла")
			return
		}
		h(w, r, ref)
	})
}

// fail — ошибка модуля или ffprobe ответом.
func fail(w http.ResponseWriter, err error) {
	var se *StatusError
	switch {
	case errors.As(err, &se):
		httpx.WriteError(w, se.Code, se.Text)
	case errors.Is(err, context.DeadlineExceeded):
		httpx.WriteError(w, http.StatusGatewayTimeout, "Сведения о файле не пришли — раздача ещё качается? Попробуйте через минуту")
	default:
		// Подробности (адреса, вывод ffmpeg) — в журнал, не человеку (правило «без адресов», ревью 18А).
		httpx.WriteError(w, http.StatusUnprocessableEntity, "Файл не читается этим плеером")
	}
}

// lazyWriter — ответ потока: заголовки уходят с первым байтом ffmpeg. Не начал — ещё можно ответить ошибкой; начал
// и оборвался — ответ обрывается (ревью 18А, Important 2).
type lazyWriter struct {
	w       http.ResponseWriter
	ct      string
	started bool
}

func (l *lazyWriter) Write(p []byte) (int, error) {
	if !l.started {
		l.started = true
		l.w.Header().Set("Content-Type", l.ct)
		l.w.Header().Set("Cache-Control", "no-store")
		l.w.WriteHeader(http.StatusOK)
	}
	return l.w.Write(p)
}

func (l *lazyWriter) Flush() {
	if f, ok := l.w.(http.Flusher); ok && l.started {
		f.Flush()
	}
}

// pipe — отдать вывод ffmpeg ответом; ошибка до первого байта — 502 с понятным текстом, после — обрыв ответа:
// плеер увидит обрыв, а не конец фильма (ревью 18А, Important 2).
func (m *Module) pipe(ctx context.Context, w http.ResponseWriter, ct string, args []string, stdin io.Reader, what string, ref Ref) {
	lw := &lazyWriter{w: w, ct: ct}
	err := m.o.Tools.stream(ctx, args, stdin, lw)
	if err == nil {
		if !lw.started && ctx.Err() == nil {
			httpx.WriteError(w, http.StatusBadGateway, "Поток не запустился — файл не читается этим плеером")
		}
		return
	}
	m.o.Log.Warn(what+" оборвался", "src", ref.Src(), "err", err)
	if !lw.started {
		httpx.WriteError(w, http.StatusBadGateway, "Поток не запустился — файл не читается этим плеером")
		return
	}
	panic(http.ErrAbortHandler)
}

// input — адрес файла для ffmpeg: через этот сервер, чтение — не в угадывание места (own=1).
func (m *Module) input(s Source) string {
	sep := "?"
	if strings.Contains(s.Path, "?") {
		sep = "&"
	}
	return m.o.Origin() + s.Path + sep + "own=1"
}

// media — сведения о файле: из памяти (10 мин) или ffprobe не дольше ProbeTimeout.
func (m *Module) media(ctx context.Context, ref Ref, s Source) (Media, error) {
	key := ref.Src()
	m.mu.Lock()
	c, ok := m.seen[key]
	m.mu.Unlock()
	if ok && time.Since(c.at) < 10*time.Minute {
		return c.m, nil
	}
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	md, err := m.o.Tools.Probe(ctx, m.input(s))
	if err != nil {
		if ctx.Err() != nil {
			return Media{}, context.DeadlineExceeded
		}
		return Media{}, err
	}
	m.mu.Lock()
	m.seen[key] = cached{md, time.Now()}
	m.mu.Unlock()
	return md, nil
}

type nextJSON struct {
	Src   string `json:"src"`
	Title string `json:"title"`
}

type infoResponse struct {
	Src         string    `json:"src"`
	Title       string    `json:"title"`
	Hash        string    `json:"hash"`
	Index       int       `json:"index"`
	DurationSec float64   `json:"durationSec"`
	StartSec    int       `json:"startSec"`
	Direct      string    `json:"direct"`
	M3UURL      string    `json:"m3uUrl"`
	LaunchURL   *string   `json:"launchUrl"`
	Video       Video     `json:"video"`
	Audio       []Track   `json:"audio"`
	Subs        []Sub     `json:"subs"`
	Trans       bool      `json:"trans"` // сервер умеет перекод видео: браузер может открыть stream.webm
	Prev        *nextJSON `json:"prev"`
	Next        *nextJSON `json:"next"`
}

func (m *Module) info(w http.ResponseWriter, r *http.Request, ref Ref) {
	s, err := m.o.Res.Resolve(r, ref, true, r.URL.Query().Get("fromStart") != "")
	if err != nil {
		fail(w, err)
		return
	}
	md, err := m.media(r.Context(), ref, s)
	if err != nil {
		fail(w, err)
		return
	}
	out := infoResponse{Src: ref.Src(), Title: s.Title, Hash: s.Hash, Index: s.Index, DurationSec: md.Duration, StartSec: s.StartSec,
		Direct: "http://" + r.Host + s.Path + "?own=1", M3UURL: "http://" + r.Host + s.M3U, LaunchURL: s.Launch, Video: md.Video,
		Audio: append([]Track{}, md.Audio...), Subs: append(append([]Sub{}, md.Subs...), externalSubs(s.SubFiles)...),
		Trans: m.transcoding()}
	if s.Prev != nil {

		out.Prev = &nextJSON{Src: s.Prev.Src(), Title: s.PrevTitle}

	}

	if s.Next != nil {

		out.Next = &nextJSON{Src: s.Next.Src(), Title: s.NextTitle}

	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// at — ?t= секунд, не меньше нуля; нет — 0, если allowEmpty.
func at(r *http.Request, allowEmpty bool) (float64, bool) {
	v := r.URL.Query().Get("t")
	if v == "" {
		return 0, allowEmpty
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil && f >= 0
}

func (m *Module) keyframe(w http.ResponseWriter, r *http.Request, ref Ref) {
	t, ok := at(r, false)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "нужно место: ?t=<секунды>")
		return
	}
	s, err := m.o.Res.Resolve(r, ref, false, false)
	if err != nil {
		fail(w, err)
		return
	}
	md, err := m.media(r.Context(), ref, s)
	if err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ProbeTimeout)
	defer cancel()
	k, err := m.o.Tools.Keyframe(ctx, m.input(s), md.Video.ID, t)
	if err != nil {
		m.o.Log.Warn("ключевой кадр не нашёлся — поток с места", "src", ref.Src(), "err", err)
		k = t
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]float64{"t": k})
}

var sidRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (m *Module) stream(format string) handler {
	return func(w http.ResponseWriter, r *http.Request, ref Ref) {
		q := r.URL.Query()
		t, ok := at(r, true)
		sid := q.Get("sid")
		if !ok || !sidRe.MatchString(sid) {
			httpx.WriteError(w, http.StatusBadRequest, "нужны место (?t=) и номер плеера (?sid=)")
			return
		}
		s, err := m.o.Res.Resolve(r, ref, false, false)
		if err != nil {
			fail(w, err)
			return
		}
		md, err := m.media(r.Context(), ref, s)
		if err != nil {
			fail(w, err)
			return
		}
		o := streamOpts{Input: m.input(s), From: t, Video: md.Video.ID, Format: format, Burst: burstFor(md.BitRate)}
		if o.Audio, ok = pickAudio(md.Audio, q.Get("a")); !ok {
			httpx.WriteError(w, http.StatusBadRequest, "такой озвучки в файле нет")
			return
		}
		if id := q.Get("s"); id != "" && format == "mkv" {
			sub, ok := findSub(md, s, id)
			if !ok {
				httpx.WriteError(w, http.StatusBadRequest, "таких субтитров в файле нет")
				return
			}
			o.Sub = &sub
		}
		var stdin io.Reader
		if o.Sub != nil && o.Sub.File != "" {
			cues, err := m.fileCues(r.Context(), o.Sub.File)
			if err != nil {
				fail(w, err)
				return
			}
			var b bytes.Buffer
			writeVTT(&b, cues, seekAt(t), seekAt(t)) // время потока — от кадра t (seekAt)
			stdin = &b
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		release, err := m.runs.Acquire("video", sid, cancel)
		if err != nil {
			httpx.WriteError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		defer release()
		ct := "video/mp2t"
		if format == "mkv" {
			ct = "video/x-matroska"
		} else if format == "webm" {
			ct = "video/webm"
		}
		m.pipe(ctx, w, ct, streamArgs(o), stdin, "поток плеера", ref)
	}
}

// pickAudio — озвучка по номеру дорожки; пусто — главная (иначе первая); звука нет — nil.
func pickAudio(ts []Track, id string) (*Track, bool) {
	if len(ts) == 0 {
		return nil, id == ""
	}
	if id == "" {
		for i := range ts {
			if ts[i].Default {
				return &ts[i], true
			}
		}
		return &ts[0], true
	}
	n, err := strconv.Atoi(id)
	for i := range ts {
		if err == nil && ts[i].ID == n {
			return &ts[i], true
		}
	}
	return nil, false
}

// findSub — текстовые субтитры по id: встроенные или файл рядом.
func findSub(md Media, s Source, id string) (Sub, bool) {
	for _, x := range append(append([]Sub{}, md.Subs...), externalSubs(s.SubFiles)...) {
		if x.ID == id && !x.Image {
			return x, true
		}
	}
	return Sub{}, false
}

func (m *Module) subs(w http.ResponseWriter, r *http.Request, ref Ref) {
	id, ok := strings.CutSuffix(r.PathValue("sub"), ".vtt")
	t, tok := at(r, true)
	sid := r.URL.Query().Get("sid")
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "таких субтитров нет")
		return
	}
	if !tok || !sidRe.MatchString(sid) {
		httpx.WriteError(w, http.StatusBadRequest, "нужны место (?t=) и номер плеера (?sid=)")
		return
	}
	s, err := m.o.Res.Resolve(r, ref, false, false)
	if err != nil {
		fail(w, err)
		return
	}
	md, err := m.media(r.Context(), ref, s)
	if err != nil {
		fail(w, err)
		return
	}
	sub, ok := findSub(md, s, id)
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "таких субтитров нет")
		return
	}
	if sub.File != "" { // файл рядом: реплики, которые не кончились к t, во времени файла (план 18А, ruling задачи 3)
		cues, err := m.fileCues(r.Context(), sub.File)
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		writeVTT(w, cues, t, 0)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	release, err := m.runs.Acquire("subs", sid, cancel)
	if err != nil {
		httpx.WriteError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	defer release()
	m.pipe(ctx, w, "text/vtt; charset=utf-8", subsArgs(m.input(s), t, sub.ID), nil, "поток субтитров", ref)
}

// fileCues — реплики файла субтитров рядом с видео: из памяти (10 мин) или один проход ffmpeg.
func (m *Module) fileCues(ctx context.Context, path string) ([]cue, error) {
	m.mu.Lock()
	c, ok := m.cues[path]
	m.mu.Unlock()
	if ok && time.Since(c.at) < 10*time.Minute {
		return c.c, nil
	}
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	cues, err := m.o.Tools.fileCues(ctx, path)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.cues[path] = cachedCues{cues, time.Now()}
	m.mu.Unlock()
	return cues, nil
}
