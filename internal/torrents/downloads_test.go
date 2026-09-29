package torrents

import (
	"context"
	"testing"
	"time"
)

// Цвет «Смотреть» у серии одинаковый на экране раздачи и в «Загрузках»: серия — 45 минут, а не
// два часа фильма (финальное ревью 7a).
func TestReadinessSameInDownloadsAndRelease(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	s.freeSpace = func(string) (int64, error) { return 50 << 30, nil }
	ih, ep := archive(t, s)
	must(t, s.Download(ctx, ih, []int{ep[0]}))
	s.mu.Lock()
	ss := s.sessions[ih]
	size := ss.t.Files()[ep[0]].Length()
	ss.speed = float64(size) / 4000 // серия докачается за 4000 с: дольше серии, быстрее фильма
	var screen Readiness
	for _, p := range s.progressLocked(ss) {
		if p.Index == ep[0] {
			screen = p.Readiness
		}
	}
	s.mu.Unlock()
	v, err := s.Downloads(ctx)
	must(t, err)
	if len(v.Items) != 1 || screen != ReadyWait || v.Items[0].Readiness != screen {
		t.Fatalf("экран раздачи %q, «Загрузки» %+v", screen, v.Items)
	}
}

// Экран «Загрузки»: хранимые файлы с состоянием очереди, место на диске, «удалится через N дн.»
// только когда осталось три дня и меньше; то, что смотрят, — первым и без удаления.
func TestDownloadsList(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	s.freeSpace = func(string) (int64, error) { return 50 << 30, nil }
	ih, ep := archive(t, s)
	must(t, s.Download(ctx, ih, []int{ep[0], ep[1], ep[2]}))
	s.mu.Lock()
	s.sessions[ih].readers[ep[2]]++ // третью серию смотрят
	s.mu.Unlock()
	openedAgo(t, s, ih, ep[1], 12*24*time.Hour)
	v, err := s.Downloads(ctx)
	must(t, err)
	if len(v.Items) != 3 || v.FreeBytes != 50<<30 || v.LowSpace {
		t.Fatalf("загрузки: %+v", v)
	}
	byIndex := map[int]DownloadItem{}
	for _, it := range v.Items {
		byIndex[it.Index] = it
	}
	if w := v.Items[0]; w.Index != ep[2] || w.State != DownloadWatching || w.CanDelete || w.DeleteInDays != nil {
		t.Fatalf("смотрят: %+v", w)
	}
	if d := byIndex[ep[0]]; d.State != DownloadDownloading || !d.CanDelete || d.DeleteInDays != nil || d.File != "Серия 1.mkv" {
		t.Fatalf("в фокусе: %+v", d)
	}
	if q := byIndex[ep[1]]; q.State != DownloadQueued || q.DeleteInDays == nil || *q.DeleteInDays != 2 {
		t.Fatalf("в очереди, открыт 12 дней назад: %+v", q)
	}
}
