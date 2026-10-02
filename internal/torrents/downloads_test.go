package torrents

import (
	"context"
	"encoding/json"
	"strings"
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
	// Срок — по раздаче целиком (спека этапа 9, раздел 5.8): раздачу открывали 12 дней назад —
	// удалится через 2 дня вся, и не открытая серия тоже.
	if d := byIndex[ep[0]]; d.State != DownloadDownloading || !d.CanDelete || d.DeleteInDays == nil || *d.DeleteInDays != 2 || d.File != "Серия 1.mkv" {
		t.Fatalf("в фокусе: %+v", d)
	}
	if q := byIndex[ep[1]]; q.State != DownloadQueued || q.DeleteInDays == nil || *q.DeleteInDays != 2 {
		t.Fatalf("в очереди, открыт 12 дней назад: %+v", q)
	}
}

// Ни разу не открытый файл — без даты открытия в ответе: пульт иначе показал бы «739000 дн. назад».
func TestDownloadItemNeverOpenedJSON(t *testing.T) {
	b, err := json.Marshal(DownloadItem{Hash: "h", File: "f.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "lastOpenedAt") {
		t.Errorf("нулевая дата открытия в ответе: %s", b)
	}
}

// «Загрузки» (спека 11b, 15.1): streaming — поток открыт сейчас, watchedAt — последний поток за 6 часов,
// удалить можно всё, где поток не открыт; метка состояния — прежняя (смотрели за 6 часов — watching).
func TestDownloadsStreaming(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Download(ctx, ih, []int{ep[0], ep[1]}))
	at := s.now().Add(-10 * time.Minute).Truncate(time.Millisecond)
	must(t, s.reg.TouchStream(ctx, ih, ep[0], at))
	s.mu.Lock()
	s.sessions[ih].readers[ep[1]]++
	s.mu.Unlock()
	v, err := s.Downloads(ctx)
	must(t, err)
	byIndex := map[int]DownloadItem{}
	for _, it := range v.Items {
		byIndex[it.Index] = it
	}
	if d := byIndex[ep[0]]; d.State != DownloadWatching || d.Streaming || !d.CanDelete || !d.WatchedAt.Equal(at) {
		t.Fatalf("смотрели 10 минут назад: %+v", d)
	}
	if d := byIndex[ep[1]]; !d.Streaming || d.CanDelete {
		t.Fatalf("смотрят сейчас: %+v", d)
	}
}

// Ревью 14В: скачанное идёт и в папки медиатеки на других дисках — место показывается по каждому диску, где лежат
// докачки, и по диску папки загрузок.
func TestDiskListPerVolume(t *testing.T) {
	s := &Service{}
	s.freeSpace = func(dir string) (int64, error) {
		if strings.HasPrefix(strings.ToUpper(dir), "E:") {
			return 1 << 30, nil
		}
		return 500 << 30, nil
	}
	got := s.diskList([]string{`D:\Kinodom`, `E:\Сериалы\Шоу`, `D:\Фильмы`})
	if len(got) != 2 || got[0].Volume != "D:" || got[1].Volume != "E:" || got[1].FreeBytes != 1<<30 || got[0].FreeBytes != 500<<30 {
		t.Fatalf("%+v", got)
	}
}
