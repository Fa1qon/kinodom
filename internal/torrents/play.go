package torrents

import (
	"fmt"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/httpx"
	"kinodom/internal/playback"
	"kinodom/internal/player"
)

// PlaySource — файл раздачи для своего плеера (спека 18, 3.2). prepare — как «Смотреть» (выбрать файл, качать
// первым, место из истории); без него — только уже выбранный файл (поток, ключевой кадр, субтитры).
func (s *Service) PlaySource(r *http.Request, hash string, index int, prepare, fromStart bool) (playback.Source, error) {
	var ih metainfo.Hash
	if err := ih.FromHexString(hash); err != nil {
		return playback.Source{}, &playback.StatusError{Code: http.StatusBadRequest, Text: "неверный идентификатор раздачи"}
	}
	if prepare {
		if err := s.Prepare(r.Context(), ih, index); err != nil {
			return playback.Source{}, &playback.StatusError{Code: prepareCode(err), Text: err.Error()}
		}
	} else if !s.isStored(ih, index) {
		return playback.Source{}, &playback.StatusError{Code: http.StatusGone, Text: "Файл не выбран — откройте раздачу заново"}
	}
	name, ok := s.fileName(ih, index)
	if !ok {
		return playback.Source{}, &playback.StatusError{Code: http.StatusNotFound, Text: ErrNoSuchFile.Error()}
	}
	title := strings.TrimSuffix(baseName(name), extOf(name))
	p := streamPath(ih, index, name)
	out := playback.Source{Title: title, Hash: ih.HexString(), Index: index, Path: p,
		M3U:      fmt.Sprintf("/m3u/%s/%d.m3u8", ih.HexString(), index),
		Complete: s.fileOnDisk(ih, index)}
	if prepare && s.watch != nil && !fromStart {
		out.StartSec = s.watch.StartSec(r.Context(), httpx.Device(r), ih.HexString(), index)
	}
	if out.StartSec > 0 {
		out.M3U += "?start=" + strconv.Itoa(out.StartSec)
	}
	if httpx.FromThisPC(r) {
		if _, port, err := net.SplitHostPort(r.Host); err == nil {
			l := player.LaunchURLAt("http://127.0.0.1:"+port+p, title, out.StartSec)
			out.Launch = &l
		}
	}
	if n, ok := prevPlayable(s.fileInfos(ih), index); ok {
		out.Prev = &playback.Ref{Kind: "torrent", Hash: ih.HexString(), Index: n.Index}
		out.PrevTitle = strings.TrimSuffix(baseName(n.Name), extOf(n.Name))
	}
	if n, ok := nextPlayable(s.fileInfos(ih), index); ok {
		out.Next = &playback.Ref{Kind: "torrent", Hash: ih.HexString(), Index: n.Index}
		out.NextTitle = strings.TrimSuffix(baseName(n.Name), extOf(n.Name))
	}
	return out, nil
}

// fileInfos — все файлы открытой раздачи; списка ещё нет — пусто.
func (s *Service) fileInfos(ih metainfo.Hash) []FileInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[ih]
	if !ok || ss.t.Info() == nil {
		return nil
	}
	fs := ss.t.Files()
	out := make([]FileInfo, len(fs))
	for i, f := range fs {
		out[i] = FileInfo{Index: i, Name: f.DisplayPath(), Size: f.Length()}
	}
	return out
}

// nextPlayable — следующая серия после index: следующий видеофайл в порядке страницы раздачи (playableFiles) в
// той же папке — папка как сезон (спека 18, 3.2).
func prevPlayable(all []FileInfo, index int) (FileInfo, bool) {
	ps := playableFiles(all)
	dir := func(n string) string { return path.Dir(strings.ReplaceAll(n, `\`, "/")) }
	for i, f := range ps {
		if f.Index == index && i > 0 && dir(ps[i-1].Name) == dir(f.Name) {
			return ps[i-1], true
		}
	}
	return FileInfo{}, false
}
func nextPlayable(all []FileInfo, index int) (FileInfo, bool) {
	ps := playableFiles(all)
	dir := func(n string) string { return path.Dir(strings.ReplaceAll(n, `\`, "/")) }
	for i, f := range ps {
		if f.Index == index && i+1 < len(ps) && dir(ps[i+1].Name) == dir(f.Name) {
			return ps[i+1], true
		}
	}
	return FileInfo{}, false
}
