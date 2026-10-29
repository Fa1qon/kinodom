package app

import (
	"cmp"
	"context"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"kinodom/internal/catalog"
	"kinodom/internal/playback"
	"kinodom/internal/torrents"
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
	var src playback.Source
	var err error
	switch ref.Kind {
	case "torrent":
		if p.a.Torrents == nil || p.a.Torrents.Engine() == nil {
			return playback.Source{}, &playback.StatusError{Code: http.StatusServiceUnavailable, Text: "Загрузки не работают"}
		}
		src, err = p.a.Torrents.PlaySource(r, ref.Hash, ref.Index, prepare, fromStart)
	case "library":
		if p.a.Library == nil {
			return playback.Source{}, &playback.StatusError{Code: http.StatusServiceUnavailable, Text: "Медиатека не работает"}
		}
		src, err = p.a.Library.PlaySource(r, ref.File, fromStart)
	default:
		return playback.Source{}, &playback.StatusError{Code: http.StatusNotFound, Text: "такого файла нет"}
	}
	// Соседние сезоны нужны только карточке плеера (info — единственный вызов с prepare): поток, кадры и
	// субтитры при каждой перемотке не перебирают варианты произведения (ревью 2026-10-29).
	if err == nil && ref.Kind == "torrent" && prepare {
		p.addCrossSeason(r.Context(), ref, &src)
	}
	return src, err
}

// addCrossSeason добавляет в плеер серии из соседних раздач того же сериала.
// Сезоны часто скачиваются отдельными торрентами: каталог связывает их по произведению,
// а в переход попадают только уже открытые движком раздачи — плеер не запускает загрузку молча.
func (p playResolver) addCrossSeason(ctx context.Context, ref playback.Ref, src *playback.Source) {
	if p.a.Catalog == nil || p.a.Torrents == nil || src == nil {
		return
	}
	rels, err := p.a.Catalog.SeriesReleasesByHash(ctx, ref.Hash)
	if err != nil || len(rels) < 2 {
		return
	}
	var all []seasonFiles
	for _, rel := range rels {
		files := p.a.Torrents.PlayableFiles(rel.InfoHash)
		if len(files) == 0 {
			continue
		}
		all = append(all, seasonFiles{rel: rel, files: files})
	}
	linkSeasons(ref, all, src)
}

// seasonFiles — открытая раздача сериала и её видеофайлы.
type seasonFiles struct {
	rel   catalog.SeriesRelease
	files []torrents.FileInfo
}

// linkSeasons — серии соседних раздач в меню плеера и переходы «предыдущая/следующая» через границу
// раздачи. all — по порядку SeriesReleasesByHash (сезон, первая серия).
func linkSeasons(ref playback.Ref, all []seasonFiles, src *playback.Source) {
	if len(all) < 2 {
		return
	}
	current := -1
	for i, x := range all {
		if strings.EqualFold(x.rel.InfoHash, ref.Hash) {
			current = i
			break
		}
	}
	if current < 0 {
		return
	}
	cur := all[current].rel
	// Раздача без номера сезона (весь сериал одним паком, спецвыпуски) — неизвестно, где она в порядке:
	// рядом с пронумерованными сезонами её не ставим (ревью 2026-10-29).
	if cur.Season > 0 {
		all = slices.DeleteFunc(slices.Clone(all), func(x seasonFiles) bool { return x.rel.Season == 0 })
		current = slices.IndexFunc(all, func(x seasonFiles) bool { return strings.EqualFold(x.rel.InfoHash, ref.Hash) })
	}
	// Место раздачи в сериале: сезон, затем первая серия — раздачи по одной серии одного сезона тоже по порядку.
	order := func(x catalog.SeriesRelease) int {
		if c := cmp.Compare(x.Season, cur.Season); c != 0 {
			return c
		}
		return cmp.Compare(x.EpisodeFrom, cur.EpisodeFrom)
	}
	// В меню — все открытые раздачи по порядку сезонов (раньше текущей — до её серий, позже — после),
	// без повторов одного диапазона в другом качестве.
	key := func(x catalog.SeriesRelease) string {
		return strconv.Itoa(x.Season) + ":" + strconv.Itoa(x.EpisodeFrom) + ":" + strconv.Itoa(x.EpisodeTo)
	}
	seen := map[string]bool{key(cur): true}
	var before, after []playback.Choice
	for i, x := range all {
		if i == current || seen[key(x.rel)] {
			continue
		}
		seen[key(x.rel)] = true
		for _, f := range x.files {
			c := playback.Choice{Ref: playback.Ref{Kind: "torrent", Hash: x.rel.InfoHash, Index: f.Index}, Title: crossSeasonTitle(x.rel, f)}
			if order(x.rel) < 0 {
				before = append(before, c)
			} else {
				after = append(after, c)
			}
		}
	}
	src.Playlist = append(append(before, src.Playlist...), after...)
	// Соседи — ближайшие раздачи строго раньше и строго позже по (сезон, первая серия).
	previousIndex, nextIndex := -1, -1
	for i := current - 1; i >= 0; i-- {
		if order(all[i].rel) < 0 {
			previousIndex = i
			break
		}
	}
	for i := current + 1; i < len(all); i++ {
		if order(all[i].rel) > 0 {
			nextIndex = i
			break
		}
	}
	if previousIndex >= 0 && src.Prev == nil {
		previous := all[previousIndex]
		last := previous.files[len(previous.files)-1]
		src.Prev = &playback.Ref{Kind: "torrent", Hash: previous.rel.InfoHash, Index: last.Index}
		src.PrevTitle = crossSeasonTitle(previous.rel, last)
	}
	if nextIndex >= 0 && src.Next == nil {
		next := all[nextIndex]
		first := next.files[0]
		src.Next = &playback.Ref{Kind: "torrent", Hash: next.rel.InfoHash, Index: first.Index}
		src.NextTitle = crossSeasonTitle(next.rel, first)
	}
}

func crossSeasonTitle(rel catalog.SeriesRelease, file torrents.FileInfo) string {
	name := strings.ReplaceAll(file.Name, `\`, "/")
	name = strings.TrimSuffix(path.Base(name), path.Ext(name))
	if rel.Season > 0 {
		return "Сезон " + strconv.Itoa(rel.Season) + " · " + name
	}
	return name
}
