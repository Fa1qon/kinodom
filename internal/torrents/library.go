package torrents

import (
	"context"
	"path/filepath"
	"time"
)

// LibraryTorrent — раздача с хранимыми файлами для медиатеки (спека этапа 9, раздел 5.3).
type LibraryTorrent struct {
	Hash       string
	Name       string
	Dir        string         // папка раздачи на диске
	Files      []FileProgress // видеофайлы раздачи: хранится ли, скачано, готовность
	LastOpened time.Time      // последнее открытие любого файла; нулевое — ни разу
}

// LibraryTorrents — раздачи с хранимыми файлами: скачанное в Kinodom для медиатеки. Раздача, у
// которой ещё нет списка файлов (нет метаинфо), пропускается.
func (s *Service) LibraryTorrents(ctx context.Context) ([]LibraryTorrent, error) {
	files, err := s.reg.StoredByAge(ctx)
	if err != nil {
		return nil, err
	}
	opened := releaseOpened(files)
	dirs := map[string]string{}
	for _, f := range files {
		d := filepath.Dir(f.Path)
		if cur, ok := dirs[f.InfoHash.HexString()]; !ok || len(d) < len(cur) {
			dirs[f.InfoHash.HexString()] = d
		}
	}
	var out []LibraryTorrent
	for ih, o := range opened {
		st, ok := s.Status(ih)
		if !ok || len(st.Files) == 0 {
			continue
		}
		out = append(out, LibraryTorrent{Hash: st.Hash, Name: st.Name, Dir: dirs[st.Hash], Files: st.Files, LastOpened: o})
	}
	return out, nil
}
