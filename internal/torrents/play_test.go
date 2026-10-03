package torrents

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"kinodom/internal/playback"
)

// Следующая серия — в порядке страницы раздачи (playableFiles: «sample» и мелочь пропущены, 2 < 10), в той же
// папке: «S2/…» не продолжает «S1/…».
func TestNextPlayable(t *testing.T) {
	fs := []FileInfo{
		{0, "Сериал/Серия 1.mkv", 1000}, {1, "Сериал/Серия 2.mkv", 1000}, {2, "Сериал/sample.mkv", 1000},
		{3, "Сериал/Серия 10.mkv", 1000}, {4, "Сериал/readme.txt", 10}, {5, "Сериал/tiny.mkv", 10},
	}
	for _, c := range []struct{ index, want int }{{0, 1}, {1, 3}, {3, -1}, {2, -1}} {
		n, ok := nextPlayable(fs, c.index)
		if (c.want < 0) == ok || (ok && n.Index != c.want) {
			t.Errorf("после %d: %+v %v, ждали %d", c.index, n, ok, c.want)
		}
	}
	seasons := []FileInfo{{0, "S1/e1.mkv", 1000}, {1, "S1/e2.mkv", 1000}, {2, "S2/e1.mkv", 1000}}
	if _, ok := nextPlayable(seasons, 1); ok {
		t.Error("из сезона 1 — в сезон 2")
	}
}

func TestPlaySource(t *testing.T) {
	s := newTestService(t)
	runService(t, s)
	ih, tt, _ := seriesFixture(t, s)
	one, three := fileIndex(t, tt, "Серия 1.mkv"), fileIndex(t, tt, "Серия 3.mkv")
	r := httptest.NewRequest("GET", "/api/v1/play/x", nil)
	r.Host, r.RemoteAddr = "127.0.0.1:8090", "127.0.0.1:5000"
	src, err := s.PlaySource(r, ih.HexString(), one, false, false)
	var se *playback.StatusError
	if !errors.As(err, &se) || se.Code != 410 {
		t.Fatalf("не выбранный файл без prepare: %v", err)
	}
	if src, err = s.PlaySource(r, ih.HexString(), one, true, false); err != nil {
		t.Fatal(err)
	}
	two := fileIndex(t, tt, "Серия 2.mkv")
	if src.Title != "Серия 1" || src.Hash != ih.HexString() || src.Index != one || !strings.HasPrefix(src.Path, "/stream/"+ih.HexString()+"/") ||
		src.M3U == "" || src.Launch == nil || src.Next == nil || src.Next.Kind != "torrent" || src.Next.Index != two || src.NextTitle != "Серия 2" {
		t.Errorf("серия 1: %+v", src)
	}
	if _, err := s.PlaySource(r, ih.HexString(), one, false, false); err != nil {
		t.Errorf("после prepare — без prepare: %v", err)
	}
	if src, err := s.PlaySource(r, ih.HexString(), three, true, false); err != nil || src.Next != nil {
		t.Errorf("последняя серия: %+v %v", src, err)
	}
	r.RemoteAddr = "192.168.0.50:5000"
	if src, _ := s.PlaySource(r, ih.HexString(), one, true, false); src.Launch != nil {
		t.Errorf("с телефона — без kinodom://: %+v", src)
	}
	if _, err := s.PlaySource(r, "zz", one, true, false); !errors.As(err, &se) || se.Code != 400 {
		t.Errorf("неверный хеш: %v", err)
	}
}
