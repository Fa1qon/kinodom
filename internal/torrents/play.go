package torrents

import (
	"fmt"
	"net"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/httpx"
	"kinodom/internal/playback"
	"kinodom/internal/player"
)

var seasonFolder = regexp.MustCompile(`(?i)^(?:(?:s|season|сезон)[ ._-]*\d+(?:[ ._-]*-?[ ._-]*\d+)?|\d{1,2}[ ._-]*(?:season|сезон)?)$`)

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
	all := s.fileInfos(ih)
	if n, ok := prevPlayable(all, index); ok {
		out.Prev = &playback.Ref{Kind: "torrent", Hash: ih.HexString(), Index: n.Index}
		out.PrevTitle = strings.TrimSuffix(baseName(n.Name), extOf(n.Name))
	}
	if n, ok := nextPlayable(all, index); ok {
		out.Next = &playback.Ref{Kind: "torrent", Hash: ih.HexString(), Index: n.Index}
		out.NextTitle = strings.TrimSuffix(baseName(n.Name), extOf(n.Name))
	}
	for _, f := range playlistPlayable(all, index) {
		out.Playlist = append(out.Playlist, playback.Choice{
			Ref:   playback.Ref{Kind: "torrent", Hash: ih.HexString(), Index: f.Index},
			Title: strings.TrimSuffix(baseName(f.Name), extOf(f.Name)),
		})
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

// PlayableFiles — видеофайлы уже открытой раздачи в порядке серий. Плеер использует
// этот снимок для перехода между сезонами, которые лежат отдельными раздачами;
// неоткрытые раздачи намеренно не запускаются автоматически.
func (s *Service) PlayableFiles(hash string) []FileInfo {
	var ih metainfo.Hash
	if err := ih.FromHexString(strings.TrimSpace(hash)); err != nil {
		return nil
	}
	return playableFiles(s.fileInfos(ih))
}

// episodeGroup — папка произведения для навигации. Папки S01/Season 1/Сезон 1 считаются
// сезонами одного произведения, поэтому переход между ними не обрывает сериал.
func episodeGroup(name string) string {
	n := strings.ReplaceAll(name, `\`, "/")
	dir := path.Dir(n)
	if seasonFolder.MatchString(path.Base(dir)) {
		return "season:" + path.Dir(dir)
	}
	return "flat:" + dir
}

// nextPlayable — следующая серия после index: следующий видеофайл в порядке страницы раздачи (playableFiles)
// в группе произведения, включая первый файл следующей папки сезона.
func prevPlayable(all []FileInfo, index int) (FileInfo, bool) {
	ps := playableFiles(all)
	for i, f := range ps {
		if f.Index == index && i > 0 {
			for j := i - 1; j >= 0; j-- {
				if episodeGroup(ps[j].Name) == episodeGroup(f.Name) {
					return ps[j], true
				}
			}
		}
	}
	return FileInfo{}, false
}
func nextPlayable(all []FileInfo, index int) (FileInfo, bool) {
	ps := playableFiles(all)
	for i, f := range ps {
		if f.Index == index {
			for j := i + 1; j < len(ps); j++ {
				if episodeGroup(ps[j].Name) == episodeGroup(f.Name) {
					return ps[j], true
				}
			}
		}
	}
	return FileInfo{}, false
}

// playlistPlayable — серии произведения в порядке имени. Папки S01/Season 1/Сезон 1
// считаются сезонами одного произведения; в список попадают те же файлы, что доступны
// как «Смотреть», включая текущий.
func playlistPlayable(all []FileInfo, index int) []FileInfo {
	ps := playableFiles(all)
	var current string
	for _, f := range ps {
		if f.Index == index {
			current = episodeGroup(f.Name)
			break
		}
	}
	if current == "" {
		return nil
	}
	out := make([]FileInfo, 0, len(ps))
	for _, f := range ps {
		if episodeGroup(f.Name) == current {
			out = append(out, f)
		}
	}
	return out
}
