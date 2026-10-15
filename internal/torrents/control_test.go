package torrents

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/anacrolix/torrent"
	"kinodom/internal/torrents/torrenttest"
)

func TestPauseAndResumeDownloadControlsQueue(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	if err := s.Download(ctx, ih, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.PauseDownload(ctx, ih, ep[0]); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Status(ih)
	if st.Focus == ep[0] {
		t.Fatalf("пауза оставила серию в фокусе: %+v", st)
	}
	tt, _ := s.Engine().Client().Torrent(ih)
	if got := tt.Files()[ep[0]].Priority(); got != torrent.PiecePriorityNone {
		t.Fatalf("приоритет на паузе %v", got)
	}
	if err := s.ResumeDownload(ctx, ih, ep[0]); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Status(ih)
	if st.Files[0].Queued == false || st.Files[0].Stored == false {
		t.Fatalf("возобновление не вернуло серию в очередь: %+v", st.Files[0])
	}
	if err := s.PauseDownload(ctx, ih, ep[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.PauseDownload(ctx, ih, ep[0]); !errors.Is(err, ErrDownloadPaused) {
		t.Fatalf("повторная пауза: %v", err)
	}
}

func TestPauseDownloadRejectsWatchingFile(t *testing.T) {
	s := newTestService(t)
	ih, ep := archive(t, s)
	if err := s.Download(context.Background(), ih, []int{ep[0]}); err != nil {
		t.Fatal(err)
	}
	closeReader := s.openReader(ih, ep[0])
	defer closeReader()
	if err := s.PauseDownload(context.Background(), ih, ep[0]); !errors.Is(err, ErrWatching) {
		t.Fatalf("пауза просматриваемого файла: %v", err)
	}
}

func TestControlRoutes(t *testing.T) {
	s := newTestService(t)
	mux := http.NewServeMux()
	s.Register(testRouter{mux})
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Сезон", 64<<10, torrenttest.File{Path: "Серия 1.mkv", Size: 100_000})
	ih, err := s.Open(context.Background(), Source{Torrent: torrentBytes(t, mi)})
	if err != nil {
		t.Fatal(err)
	}
	tt, _ := s.Engine().Client().Torrent(ih)
	i := fileIndex(t, tt, "Серия 1.mkv")
	if err := s.Download(context.Background(), ih, []int{i}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/downloads/" + ih.HexString() + "/" + strconv.Itoa(i)
	for _, tc := range []struct {
		method, suffix string
		want           int
	}{
		{"POST", "/pause", http.StatusNoContent},
		{"POST", "/resume", http.StatusNoContent},
	} {
		req := httptest.NewRequest(tc.method, path+tc.suffix, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s %s: %d, ожидался %d", tc.method, tc.suffix, rec.Code, tc.want)
		}
	}
}
