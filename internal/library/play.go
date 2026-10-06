package library

import (
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"kinodom/internal/httpx"
	"kinodom/internal/playback"
	"kinodom/internal/player"
)

// playerSubs — субтитры рядом с видео, которые плеер покажет (текстовые).
var playerSubs = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".vtt": true}

// PlaySource — файл медиатеки для своего плеера (спека 18, 3.2): как «Смотреть» (handlePlay) и следующий
// файл по правилу .m3u8 (handleM3U), с субтитрами рядом.
func (l *Library) PlaySource(r *http.Request, id int64, fromStart bool) (playback.Source, error) {
	ctx := r.Context()
	f, err := l.d.mediaFile(ctx, id)
	if errors.Is(err, errNoFile) {
		return playback.Source{}, &playback.StatusError{Code: http.StatusNotFound, Text: errNoFile.Error()}
	}
	if err != nil {
		return playback.Source{}, &playback.StatusError{Code: http.StatusInternalServerError, Text: "медиатека не читается: " + err.Error()}
	}
	if l.gone(ctx, f) {
		return playback.Source{}, &playback.StatusError{Code: http.StatusGone, Text: errFileGone.Error()}
	}
	hash, index := f.hash()
	out := playback.Source{Title: l.fileTitle(ctx, f), Hash: hash, Index: index, Path: f.streamPath(),
		M3U: "/m3u/library/" + strconv.FormatInt(f.ID, 10) + ".m3u8"}
	if !fromStart && l.o.History != nil {
		out.StartSec = l.o.History.StartSec(ctx, httpx.Device(r), hash, index)
	}
	if out.StartSec > 0 {
		out.M3U += "?start=" + strconv.Itoa(out.StartSec)
	}
	if httpx.FromThisPC(r) {
		if _, port, err := net.SplitHostPort(r.Host); err == nil {
			link := player.LaunchURLAt("http://127.0.0.1:"+port+out.M3U, out.Title, 0)
			out.Launch = &link
		}
	}
	for _, p := range findSidecars([]string{f.Path}) {
		if playerSubs[strings.ToLower(filepath.Ext(p))] {
			out.SubFiles = append(out.SubFiles, p)
		}
	}
	if files, err := l.d.files(ctx, f.Unit); err == nil {
		if n, ok := prevLibFile(files, f.libFile); ok {
			nf := f
			nf.libFile = n
			out.Prev = &playback.Ref{Kind: "library", File: n.ID}
			out.PrevTitle = l.fileTitle(ctx, nf)
		}
		if n, ok := nextLibFile(files, f.libFile); ok {
			nf := f
			nf.libFile = n
			out.Next = &playback.Ref{Kind: "library", File: n.ID}
			out.NextTitle = l.fileTitle(ctx, nf)
		}
	}
	return out, nil
}

// nextLibFile — следующий файл единицы по правилу .m3u8 (handleM3U): тот же сезон; у серий с номером — тот же раздел.
func prevLibFile(files []libFile, f libFile) (libFile, bool) {
	for i, x := range files {
		if x.ID != f.ID || i == 0 {
			continue
		}
		n := files[i-1]
		if n.Season != f.Season || (n.Section != f.Section && f.Episode > 0) {
			return libFile{}, false
		}
		return n, true
	}
	return libFile{}, false
}

func nextLibFile(files []libFile, f libFile) (libFile, bool) {
	for i, x := range files {
		if x.ID != f.ID || i+1 >= len(files) {
			continue
		}
		n := files[i+1]
		if n.Season != f.Season || (n.Section != f.Section && f.Episode > 0) {
			return libFile{}, false
		}
		return n, true
	}
	return libFile{}, false
}
