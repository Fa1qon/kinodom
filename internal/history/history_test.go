package history

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"kinodom/internal/store"
)

func newService(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	d, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	now := time.Date(2026, 9, 29, 21, 0, 0, 0, time.UTC)
	s := New(d)
	s.now = func() time.Time { return now }
	return s, &now
}

const (
	h1 = "aaaa000000000000000000000000000000000001"
	h2 = "bbbb000000000000000000000000000000000002"
)

var ctx = context.Background()

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// Место по чтению потока: доля файла; есть длительность — секунды; дошли до 90 % — просмотрено.
// У каждого устройства своё (спека этапа 8, раздел 7.1).
func TestReportFromStream(t *testing.T) {
	s, _ := newService(t)
	if err := s.SetDuration(ctx, h1, 0, 3600); err != nil {
		t.Fatal(err)
	}
	s.Report(ctx, "192.168.0.60", h1, 0, 450<<20, 1000<<20)
	fs, err := s.Files(ctx, "192.168.0.60", h1)
	if err != nil {
		t.Fatal(err)
	}
	f := fs[0]
	if len(fs) != 1 || !near(f.Fraction, 0.45) || !near(f.PositionSec, 1620) || f.DurationSec != 3600 || f.Watched {
		t.Fatalf("после 45 %%: %+v", fs)
	}
	if fs, _ := s.Files(ctx, "pc", h1); len(fs) != 0 {
		t.Errorf("у другого устройства есть место: %+v", fs)
	}
	s.Report(ctx, "192.168.0.60", h1, 0, 930<<20, 1000<<20)
	fs, _ = s.Files(ctx, "192.168.0.60", h1)
	if !fs[0].Watched {
		t.Errorf("93 %% — не просмотрено: %+v", fs[0])
	}
	// Перемотали назад и пересмотрели начало — отметка «просмотрено» остаётся, место — новое.
	s.Report(ctx, "192.168.0.60", h1, 0, 100<<20, 1000<<20)
	fs, _ = s.Files(ctx, "192.168.0.60", h1)
	if !fs[0].Watched || !near(fs[0].Fraction, 0.1) {
		t.Errorf("после перемотки: %+v", fs[0])
	}
	// Без длительности — только доля.
	s.Report(ctx, "pc", h2, 3, 500, 1000)
	fs, _ = s.Files(ctx, "pc", h2)
	if len(fs) != 1 || fs[0].Index != 3 || fs[0].PositionSec != 0 || !near(fs[0].Fraction, 0.5) {
		t.Errorf("без длительности: %+v", fs)
	}
}

// Свой плеер сообщает точное место; ручные «Просмотрено» и «Не просмотрено» (7.1, 7.2).
func TestPlayerAndMarks(t *testing.T) {
	s, _ := newService(t)
	if err := s.SetPosition(ctx, "pc", h1, 2, 1800, 2400); err != nil {
		t.Fatal(err)
	}
	fs, _ := s.Files(ctx, "pc", h1)
	if !near(fs[0].Fraction, 0.75) || fs[0].PositionSec != 1800 || fs[0].Watched {
		t.Fatalf("от плеера: %+v", fs[0])
	}
	if d, ok := s.Duration(ctx, h1, 2); !ok || d != 2400 {
		t.Errorf("длительность от плеера не запомнилась: %v %v", d, ok)
	}
	if err := s.SetWatched(ctx, "pc", h1, 2, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWatched(ctx, "pc", h1, 5, true); err != nil { // серию, которую не открывали
		t.Fatal(err)
	}
	fs, _ = s.Files(ctx, "pc", h1)
	if len(fs) != 2 || !fs[0].Watched || !fs[1].Watched || fs[1].Index != 5 {
		t.Fatalf("отметки: %+v", fs)
	}
	if err := s.SetWatched(ctx, "pc", h1, 2, false); err != nil {
		t.Fatal(err)
	}
	fs, _ = s.Files(ctx, "pc", h1)
	if fs[0].Watched || fs[0].Fraction != 0 || fs[0].PositionSec != 0 {
		t.Errorf("«Не просмотрено» не сбросило место: %+v", fs[0])
	}
	if err := s.SetPosition(ctx, "pc", h1, 2, -5, 100); err == nil {
		t.Errorf("отрицательное место принято")
	}
}

// Откуда продолжать: за 10 с до места; с начала — если просмотрено, место в первой минуте, в самом
// конце или неизвестно в секундах (7.3).
func TestStartSec(t *testing.T) {
	s, _ := newService(t)
	s.SetPosition(ctx, "pc", h1, 0, 1800, 3600)
	s.SetPosition(ctx, "pc", h1, 1, 40, 3600)
	s.SetPosition(ctx, "pc", h1, 2, 3500, 3600)
	s.Report(ctx, "pc", h1, 3, 500, 1000)
	cases := map[int]int{0: 1790, 1: 0, 2: 0, 3: 0, 9: 0}
	for i, want := range cases {
		if got := s.StartSec(ctx, "pc", h1, i); got != want {
			t.Errorf("файл %d: с %d, нужно %d", i, got, want)
		}
	}
	if got := s.StartSec(ctx, "192.168.0.60", h1, 0); got != 0 {
		t.Errorf("другое устройство продолжает с %d", got)
	}
}

// История устройства: раздачи по последнему просмотру; последний файл; сколько просмотрено;
// убрать раздачу из истории — только у этого устройства (7.4).
func TestList(t *testing.T) {
	s, now := newService(t)
	s.SetPosition(ctx, "pc", h1, 0, 3500, 3600)
	s.SetWatched(ctx, "pc", h1, 0, true)
	*now = now.Add(time.Minute)
	s.SetPosition(ctx, "pc", h2, 0, 100, 3600)
	*now = now.Add(time.Minute)
	s.SetPosition(ctx, "pc", h1, 1, 600, 3600)
	s.SetPosition(ctx, "192.168.0.60", h2, 0, 50, 3600)
	items, err := s.List(ctx, "pc")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Hash != h1 || items[0].Last.Index != 1 || items[0].Watched != 1 || items[0].Files != 2 || items[1].Hash != h2 {
		t.Fatalf("история: %+v", items)
	}
	if err := s.Remove(ctx, "pc", h1); err != nil {
		t.Fatal(err)
	}
	items, _ = s.List(ctx, "pc")
	other, _ := s.List(ctx, "192.168.0.60")
	if len(items) != 1 || items[0].Hash != h2 || len(other) != 1 {
		t.Errorf("после удаления: пк %+v, телевизор %+v", items, other)
	}
}
