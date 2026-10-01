package torrents

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// FetchInfo — метаинфо раздачи по magnet от пиров (спека 11b, 6.3.1): временная раздача без хранилища —
// ничего не пишет на диск и не раздаёт, убирается после ответа или по ctx (тогда ErrNoInfo). Раздача с
// этим infohash уже открыта и метаинфо у неё есть — её метаинфо, без временной. Ответ — .torrent
// (bencode) с info.
func (s *Service) FetchInfo(ctx context.Context, magnet string) ([]byte, error) {
	m, err := metainfo.ParseMagnetUri(magnet)
	if err != nil || m.InfoHash == (metainfo.Hash{}) {
		return nil, fmt.Errorf("magnet-ссылка не читается: в ней нет infohash раздачи")
	}
	ih := m.InfoHash
	s.mu.Lock()
	if s.eng == nil {
		s.mu.Unlock()
		return nil, ErrNoEngine
	}
	if t, ok := s.eng.cl.Torrent(ih); ok {
		s.mu.Unlock()
		select {
		case <-t.GotInfo():
			return bencode.Marshal(t.Metainfo())
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %v", ErrNoInfo, ctx.Err())
		}
	}
	if s.fetching == nil {
		s.fetching = map[metainfo.Hash]chan struct{}{}
	}
	done := make(chan struct{})
	s.fetching[ih] = done
	t, _ := s.eng.cl.AddTorrentOpt(torrent.AddTorrentOpts{InfoHash: ih, Storage: nullStorage{},
		DisallowDataDownload: true, DisallowDataUpload: true})
	s.mu.Unlock()
	defer func() {
		t.Drop()
		s.mu.Lock()
		delete(s.fetching, ih)
		s.mu.Unlock()
		close(done)
	}()
	if len(m.Trackers) > 0 {
		t.AddTrackers([][]string{m.Trackers})
	}
	select {
	case <-t.GotInfo():
		return bencode.Marshal(t.Metainfo())
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %v", ErrNoInfo, ctx.Err())
	}
}

// waitFetch — пока у этого infohash идёт FetchInfo, раздача в движке — временная, без хранилища: Open
// ждёт её конца, иначе скачанное ушло бы в никуда. Вызывать под s.mu; возвращается тоже под s.mu.
func (s *Service) waitFetch(ctx context.Context, ih metainfo.Hash) error {
	for {
		done, ok := s.fetching[ih]
		if !ok {
			return nil
		}
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			s.mu.Lock()
			return ctx.Err()
		}
		s.mu.Lock()
	}
}

// nullStorage — хранилище временной раздачи: данных нет, записать нельзя.
type nullStorage struct{}

func (nullStorage) OpenTorrent(context.Context, *metainfo.Info, metainfo.Hash) (storage.TorrentImpl, error) {
	return storage.TorrentImpl{Piece: func(metainfo.Piece) storage.PieceImpl { return nullPiece{} }, Close: func() error { return nil }}, nil
}

type nullPiece struct{}

var errNullStorage = errors.New("временная раздача без хранилища")

func (nullPiece) ReadAt([]byte, int64) (int, error)  { return 0, io.EOF }
func (nullPiece) WriteAt([]byte, int64) (int, error) { return 0, errNullStorage }
func (nullPiece) MarkComplete() error                { return errNullStorage }
func (nullPiece) MarkNotComplete() error             { return nil }
func (nullPiece) Completion() storage.Completion     { return storage.Completion{Ok: true} }
