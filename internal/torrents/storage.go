package torrents

import (
	"context"
	"fmt"
	"os"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// prepStorage — обёртка файлового хранилища движка. OpenTorrent вызывается до того,
// как движок прочитает отметки кусков (и для magnet — сразу после получения метаданных).
// В этот момент обёртка:
//  1. создаёт ВСЕ файлы раздачи разрежёнными — иначе соседние серии занимают на диске
//     по гигабайту из-за общих кусков на границах файлов;
//  2. помечает куски без отметки как «известно, что не скачан» — иначе движок хэширует
//     гигабайты нулей заранее созданных файлов, прежде чем начать качать.
type prepStorage struct {
	inner storage.ClientImpl
	pc    storage.PieceCompletion
	base  string
}

func (s prepStorage) OpenTorrent(ctx context.Context, info *metainfo.Info, ih metainfo.Hash) (storage.TorrentImpl, error) {
	var off int64
	for _, fi := range info.UpvertedFiles() {
		p := enginePath(s.base, info, ih, fi)
		// Файл удалили (почистили папку в Проводнике) или он короче нужного: отметки его
		// кусков в базе врут. Сбрасываем их до того, как пересоздадим файл, — иначе движок
		// счёл бы файл скачанным и отдавал бы нули.
		if st, err := os.Stat(p); fi.Length > 0 && (err != nil || st.Size() < fi.Length) {
			sp := spanFor(info.PieceLength, off, fi.Length)
			for i := sp.begin; i < sp.end; i++ {
				if err := s.pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, false); err != nil {
					return storage.TorrentImpl{}, fmt.Errorf("отметки кусков: %w", err)
				}
			}
		}
		off += fi.Length
		if err := createSparse(p, fi.Length); err != nil {
			return storage.TorrentImpl{}, fmt.Errorf("разрежённый файл %s: %w", p, err)
		}
	}
	if err := premarkIncomplete(s.pc, ih, info.NumPieces()); err != nil {
		return storage.TorrentImpl{}, fmt.Errorf("отметки кусков: %w", err)
	}
	return s.inner.OpenTorrent(ctx, info, ih)
}

// premarkIncomplete ставит «не скачан» кускам, о которых ничего не известно. Куски с отметкой
// (раздача уже качалась) не трогает. По транзакции на кусок: замер — 4 тыс. кусков за 0,12 с.
func premarkIncomplete(pc storage.PieceCompletion, ih metainfo.Hash, n int) error {
	for i := 0; i < n; i++ {
		k := metainfo.PieceKey{InfoHash: ih, Index: i}
		c, err := pc.Get(k)
		if err != nil {
			return err
		}
		if c.Ok {
			continue
		}
		if err := pc.Set(k, false); err != nil {
			return err
		}
	}
	return nil
}
