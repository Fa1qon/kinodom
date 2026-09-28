package torrents

import (
	"context"
	"fmt"

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
	for _, fi := range info.UpvertedFiles() {
		p := enginePath(s.base, info, ih, fi)
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
