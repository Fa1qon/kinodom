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
	// Missing — хранимые файлы есть, а раздача не загружена (старт, отключённый диск): медиатека её
	// не показывает, но и не забывает.
	Missing bool
}

// LibraryTorrents — раздачи с хранимыми файлами: скачанное в Kinodom для медиатеки. Раздача, которую
// движок не загрузил или у которой ещё нет списка файлов, — с пометкой Missing.
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
			out = append(out, LibraryTorrent{Hash: ih.HexString(), Dir: dirs[ih.HexString()], LastOpened: o, Missing: true})
			continue
		}
		out = append(out, LibraryTorrent{Hash: st.Hash, Name: st.Name, Dir: dirs[st.Hash], Files: st.Files, LastOpened: o})
	}
	return out, nil
}
