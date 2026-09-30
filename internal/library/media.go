package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"kinodom/internal/httpx"
	"kinodom/internal/media"
	"kinodom/internal/player"
	"kinodom/internal/watch"
)

// mediaTypes — Content-Type файлов медиатеки: иначе ServeContent угадывал бы тип по байтам.
var mediaTypes = map[string]string{
	".mkv": "video/x-matroska", ".mp4": "video/mp4", ".m4v": "video/mp4", ".avi": "video/x-msvideo", ".ts": "video/mp2t",
	".m2ts": "video/mp2t", ".mov": "video/quicktime", ".wmv": "video/x-ms-wmv", ".webm": "video/webm", ".mpg": "video/mpeg",
	".mpeg": "video/mpeg", ".vob": "video/mpeg", ".flv": "video/x-flv", ".3gp": "video/3gpp",
}

// mediaExtraLead — сверх общей поправки watch.Lead: файл с локального диска VLC читает впереди на весь
// свой буфер, около 16 МБ (вживую 2026-09-30: записано 86 с при картинке на ~40-й секунде; у раздач
// скорость ограничена, и VLC впереди на 3–4 МБ). Место медиатеки — с поправкой 16 МБ: лучше повторить
// несколько секунд, чем пропустить.
var mediaExtraLead int64 = 12 << 20

// mediaReporter — история с поправкой на чтение впереди для файлов с локального диска.
type mediaReporter struct{ h History }

func (m mediaReporter) Report(ctx context.Context, device, hash string, index int, offset, size int64) {
	m.h.Report(ctx, device, hash, index, max(offset-mediaExtraLead, 0), size)
}

// mediaReader — открытый файл медиатеки (тесты подменяют открытие, чтобы оборвать чтение).
type mediaReader interface {
	io.ReadSeeker
	io.ReaderAt
	io.Closer
	Stat() (os.FileInfo, error)
}

func (l *Library) openFile(name string) (mediaReader, error) {
	if l.open != nil {
		return l.open(name)
	}
	return os.Open(name)
}

// mediaFile — файл медиатеки с единицей.
type mediaFile struct {
	libFile
	Unit    int64
	UnitKey string
	Source  string
	Missing bool
	KP      int
}

// hash — «раздача» файла в истории и номер файла в ней.
func (f mediaFile) hash() (string, int) {
	if f.Source == "torrent" {
		return f.UnitKey, f.TIndex
	}
	return "lib-" + strconv.FormatInt(f.Unit, 10), int(f.ID)
}

// streamPath — адрес потока файла: из папки — /media/…, из раздачи — прежний поток раздачи.
func (f mediaFile) streamPath() string {
	name := url.PathEscape(baseName(f.Path))
	if f.Source == "torrent" {
		return fmt.Sprintf("/stream/%s/%d/%s", f.UnitKey, f.TIndex, name)
	}
	return fmt.Sprintf("/media/%d/%s", f.ID, name)
}

var errNoFile = errors.New("такого файла в медиатеке нет")

func (d db) mediaFile(ctx context.Context, id int64) (mediaFile, error) {
	var f mediaFile
	err := d.R.QueryRowContext(ctx, `SELECT f.id, f.path, f.tindex, f.season, f.section, f.episode, f.size, u.id, u.key, u.source, u.missing, u.kp_id
		FROM lib_files f JOIN lib_units u ON u.id = f.unit WHERE f.id = ?`, id).Scan(&f.ID, &f.Path, &f.TIndex, &f.Season, &f.Section,
		&f.Episode, &f.Size, &f.Unit, &f.UnitKey, &f.Source, &f.Missing, &f.KP)
	if errors.Is(err, sql.ErrNoRows) {
		return f, errNoFile
	}
	return f, err
}

// fileFromPath — файл из {file} адреса («123» или «123.m3u8»); ответ об ошибке уже записан.
func (l *Library) fileFromPath(w http.ResponseWriter, r *http.Request) (mediaFile, bool) {
	id, err := strconv.ParseInt(strings.TrimSuffix(r.PathValue("file"), ".m3u8"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "неверный номер файла")
		return mediaFile{}, false
	}
	f, err := l.d.mediaFile(r.Context(), id)
	switch {
	case errors.Is(err, errNoFile):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
		return f, false
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "медиатека не читается: "+err.Error())
		return f, false
	}
	return f, true
}

// handleMedia — поток файла из папки категории (спека, раздел 5.6): Range, запрет сна, место — в
// историю устройства. Путь берётся из базы по номеру, имя в адресе — только для плеера.
func (l *Library) handleMedia(w http.ResponseWriter, r *http.Request) {
	f, ok := l.fileFromPath(w, r)
	if !ok {
		return
	}
	if f.Source != "folder" || f.Missing {
		httpx.WriteError(w, http.StatusNotFound, errNoFile.Error())
		return
	}
	fh, err := l.openFile(f.Path)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "файла нет на диске: "+baseName(f.Path))
		return
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "файла нет на диске: "+baseName(f.Path))
		return
	}
	ct := mediaTypes[strings.ToLower(filepath.Ext(f.Path))]
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	defer l.o.Power.Acquire()() // ПК не засыпает, пока смотрят
	hash, index := f.hash()
	l.learnDuration(r.Context(), hash, index, fh, fi.Size(), f.Path)
	body, done := l.tracker.Wrap(watch.Key{Device: httpx.Device(r), Hash: hash, Index: index}, fi.Size(), fh)
	defer done()
	http.ServeContent(w, r, "", fi.ModTime(), body)
}

// learnDuration — длительность файла из заголовка при первом просмотре (для «с 23 мин»).
func (l *Library) learnDuration(ctx context.Context, hash string, index int, r io.ReaderAt, size int64, name string) {
	if l.o.History == nil {
		return
	}
	if _, loaded := l.durTried.LoadOrStore(hash+"/"+strconv.Itoa(index), true); loaded {
		return
	}
	if _, ok := l.o.History.Duration(ctx, hash, index); ok {
		return
	}
	d, err := media.Duration(r, size, strings.ToLower(filepath.Ext(name)))
	if err != nil {
		return
	}
	if err := l.o.History.SetDuration(ctx, hash, index, d); err != nil {
		l.log.Warn("медиатека: длительность файла не записалась", "err", err)
	}
}

// fileTitle — название для плеера: карточка и серия («Малахит — 1×02»).
func (l *Library) fileTitle(ctx context.Context, f mediaFile) string {
	var title, manual, name string
	l.d.R.QueryRowContext(ctx, `SELECT title, manual_title, name FROM lib_units WHERE id = ?`, f.Unit).Scan(&title, &manual, &name)
	var card string
	l.d.R.QueryRowContext(ctx, `SELECT title FROM lib_cards WHERE key = ?`, cardKey(f.Unit, f.KP)).Scan(&card)
	t := card
	for _, s := range []string{manual, title, name} {
		if t == "" {
			t = s
		}
	}
	switch {
	case f.Episode > 0 && f.Season > 0:
		t += fmt.Sprintf(" — %d×%02d", f.Season, f.Episode)
	case f.Episode > 0:
		t += fmt.Sprintf(" — %d", f.Episode)
	case f.Source == "torrent" || f.Section != "" || !strings.EqualFold(baseName(f.Path), name):
		t += " — " + strings.TrimSuffix(baseName(f.Path), filepath.Ext(f.Path))
	}
	return t
}

// playResponse — «Смотреть» у файла медиатеки.
type playResponse struct {
	StreamURL string  `json:"streamUrl"`
	M3UURL    string  `json:"m3uUrl"`
	LaunchURL *string `json:"launchUrl"` // только запросу с этого ПК: kinodom:// открывает плеер здесь
	Title     string  `json:"title"`
	StartSec  int     `json:"startSec"` // откуда открыть, с; 0 — с начала
	Hash      string  `json:"hash"`
	Index     int     `json:"index"`
}

// handlePlay — «Смотреть» / «Продолжить» (спека, раздел 5.6): ?fromStart=1 — «С начала». На этом ПК
// плеер открывает .m3u8 (серии подряд); место — в .m3u8 у первой серии, а не в ссылке: иначе VLC
// начал бы с него каждую серию.
func (l *Library) handlePlay(w http.ResponseWriter, r *http.Request) {
	f, ok := l.fileFromPath(w, r)
	if !ok {
		return
	}
	hash, index := f.hash()
	out := playResponse{StreamURL: "http://" + r.Host + f.streamPath(), Title: l.fileTitle(r.Context(), f), Hash: hash, Index: index}
	if r.URL.Query().Get("fromStart") == "" && l.o.History != nil {
		out.StartSec = l.o.History.StartSec(r.Context(), httpx.Device(r), hash, index)
	}
	m3u := "/m3u/library/" + strconv.FormatInt(f.ID, 10) + ".m3u8"
	if out.StartSec > 0 {
		m3u += "?start=" + strconv.Itoa(out.StartSec)
	}
	out.M3UURL = "http://" + r.Host + m3u
	if httpx.FromThisPC(r) {
		if _, port, err := net.SplitHostPort(r.Host); err == nil {
			link := player.LaunchURLAt("http://127.0.0.1:"+port+m3u, out.Title, 0)
			out.LaunchURL = &link
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleM3U — .m3u8 файла и следующих за ним в том же сезоне или разделе (у фильма из частей —
// следующие части): VLC идёт по сериям сам. ?start= — место у первого пункта.
func (l *Library) handleM3U(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.PathValue("file"), ".m3u8") {
		httpx.WriteError(w, http.StatusBadRequest, "нужен адрес /m3u/library/{номер файла}.m3u8")
		return
	}
	f, ok := l.fileFromPath(w, r)
	if !ok {
		return
	}
	files, err := l.d.files(r.Context(), f.Unit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "медиатека не читается: "+err.Error())
		return
	}
	start, _ := strconv.Atoi(r.URL.Query().Get("start"))
	var items []player.M3UItem
	on := false
	for _, x := range files {
		if x.ID == f.ID {
			on = true
		}
		if !on {
			continue
		}
		if x.Season != f.Season || x.Section != f.Section {
			break
		}
		mf := f
		mf.libFile = x
		it := player.M3UItem{Title: l.fileTitle(r.Context(), mf), URL: "http://" + r.Host + mf.streamPath()}
		if len(items) == 0 {
			it.StartSec = max(start, 0)
		}
		items = append(items, it)
	}
	title := l.fileTitle(r.Context(), f)
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", player.M3UDisposition(title))
	w.Write(player.M3UList(items))
}
