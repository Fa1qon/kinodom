package torrents

import (
	"context"
	"testing"
	"time"
)

// Раздачи с хранимыми файлами для медиатеки (спека этапа 9, раздел 5.3): все видеофайлы с
// готовностью, папка, последнее открытие; раздача без хранимых файлов — не в медиатеке.
func TestLibraryTorrents(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	archiveNamed(t, s, "Только открыта")
	must(t, s.Prepare(ctx, ih, ep[1]))
	ts, err := s.LibraryTorrents(ctx)
	must(t, err)
	if len(ts) != 1 || ts[0].Hash != ih.HexString() || ts[0].Name != "Архив" || ts[0].Dir == "" || !ts[0].LastOpened.IsZero() || len(ts[0].Files) != 4 {
		t.Fatalf("медиатеке: %+v", ts)
	}
	for _, f := range ts[0].Files {
		if f.Stored != (f.Index == ep[1]) || f.Size != mib {
			t.Errorf("файл %+v", f)
		}
	}
	at := time.Now().Add(-time.Hour)
	must(t, s.reg.TouchStream(ctx, ih, ep[1], at))
	ts, _ = s.LibraryTorrents(ctx)
	if !ts[0].LastOpened.Equal(time.UnixMilli(at.UnixMilli())) {
		t.Errorf("последнее открытие: %v", ts[0].LastOpened)
	}
}
