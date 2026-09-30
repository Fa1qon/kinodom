package torrents

import (
	"context"
	"testing"

	"kinodom/internal/watch"
)

// reported — последнее место, которое дошло до истории.
type reported struct {
	WatchTracker
	offset int64
}

func (r *reported) Report(_ context.Context, _, _ string, _ int, offset, _ int64) { r.offset = offset }

// Скачанный целиком файл VLC читает с диска впереди на весь буфер (хвост Х15): место — с той же
// поправкой, что у файла медиатеки; недокачанный — без неё (его ограничивает скорость раздачи).
func TestDiskLeadForCompleteFile(t *testing.T) {
	const size = 2 << 30
	for _, c := range []struct {
		complete bool
		want     int64
	}{{true, 100<<20 - watch.ExtraLeadFor(size)}, {false, 100 << 20}} {
		r := &reported{}
		d := diskLead{WatchTracker: r, complete: func(string, int) bool { return c.complete }}
		d.Report(context.Background(), "pc", "ab", 0, 100<<20, size)
		if r.offset != c.want {
			t.Errorf("скачан целиком %v: место %d, нужно %d", c.complete, r.offset, c.want)
		}
	}
	if got := watch.ExtraLeadFor(100 << 20); got != 2<<20 {
		t.Errorf("поправка не больше 2 %% файла: %d", got)
	}
}
