package app

import (
	"net"
	"net/http"
	"os"
	"path/filepath"

	"kinodom/internal/playback"
)

// initPlayback — свой плеер фильмов (спека цикла 18): ffmpeg рядом с kinodom.exe; нет — плеер в браузере выключен,
// «Смотреть» работает как раньше.
func (a *App) initPlayback(o Options) {
	dir := o.FFmpegDir
	if dir == "" {
		if exe, err := os.Executable(); err == nil {
			dir = filepath.Dir(exe)
		}
	}
	tools, ok := playback.Find(dir)
	if !ok {
		a.Log.Info("ffmpeg не найден — плеер в браузере выключен", "dir", dir)
	}
	a.Playback = playback.New(playback.Options{Tools: tools, OK: ok, Res: playResolver{a},
		Origin: func() string { return "http://" + loopback(a.API.Addr()) }, Log: a.Log.With("module", "playback")})
	a.Playback.Register(a.API)
}

// loopback — адрес API на 127.0.0.1 с его портом: ffmpeg читает файл через этот сервер.
func loopback(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// playResolver — файлы раздач и медиатеки для плеера.
type playResolver struct{ a *App }

func (p playResolver) Resolve(r *http.Request, ref playback.Ref, prepare, fromStart bool) (playback.Source, error) {
	switch ref.Kind {
	case "torrent":
		if p.a.Torrents == nil || p.a.Torrents.Engine() == nil {
			return playback.Source{}, &playback.StatusError{Code: http.StatusServiceUnavailable, Text: "Загрузки не работают"}
		}
		return p.a.Torrents.PlaySource(r, ref.Hash, ref.Index, prepare, fromStart)
	case "library":
		if p.a.Library == nil {
			return playback.Source{}, &playback.StatusError{Code: http.StatusServiceUnavailable, Text: "Медиатека не работает"}
		}
		return p.a.Library.PlaySource(r, ref.File, fromStart)
	}
	return playback.Source{}, &playback.StatusError{Code: http.StatusNotFound, Text: "такого файла нет"}
}
