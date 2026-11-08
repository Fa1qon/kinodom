package torrents

import (
	"context"
	"testing"

	"github.com/anacrolix/torrent"

	"kinodom/internal/torrents/torrenttest"
)

// Окно просмотра (TorrServer, 2026-11-08): от позиции потока ступени Now, Next, Readahead, High — ближние куски
// всегда выше дальних; позади позиции и дальше окна явных приоритетов нет.
func TestStreamWindowTiers(t *testing.T) {
	s := newTestService(t)
	src := t.TempDir()
	const piece = 1 << 20
	mi, _ := torrenttest.MakeTorrent(t, src, "film.mkv", piece, torrenttest.File{Path: "film.mkv", Size: 64 << 20})
	ih, err := s.Open(context.Background(), Source{Torrent: torrentBytes(t, mi)})
	if err != nil {
		t.Fatal(err)
	}
	tt, _ := s.eng.cl.Torrent(ih)
	<-tt.GotInfo()
	f := tt.Files()[0]
	w := streamWindow(tt, f, 10*piece+5, 40*piece)
	if w[9] != 0 {
		t.Error("кусок позади позиции получил приоритет")
	}
	if w[10] != torrent.PiecePriorityNow || w[11] != torrent.PiecePriorityNext {
		t.Fatalf("первые куски: %v %v, ожидались Now и Next", w[10], w[11])
	}
	if w[12] != torrent.PiecePriorityReadahead || w[26] != torrent.PiecePriorityReadahead {
		t.Errorf("до 16 МБ — Readahead: %v %v", w[12], w[26])
	}
	if w[27] != torrent.PiecePriorityHigh || w[50] != torrent.PiecePriorityHigh {
		t.Errorf("дальше 16 МБ — High: %v %v", w[27], w[50])
	}
	if _, ok := w[51]; ok {
		t.Error("за окном не должно быть явных приоритетов")
	}
	if got := bufferedAhead(tt, f, 10*piece); got != 0 {
		t.Errorf("впереди скачано %d, ожидалось 0", got)
	}
}
