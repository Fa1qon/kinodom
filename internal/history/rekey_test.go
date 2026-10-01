package history

import (
	"testing"
	"time"
)

// Переход раздачи на обновлённую версию (спека 11b, 6.3.4; Review Focus 4): места и отметки всех
// устройств и длительности — у новой версии по сопоставлению номеров; строка новой версии уже есть —
// объединение («просмотрено» — по «или», место — свежее); номера без пары — уходят.
func TestRekeyTx(t *testing.T) {
	s, now := newService(t)
	if err := s.SetWatched(ctx, "tv", h1, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPosition(ctx, "phone", h1, 1, 300, 1200); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPosition(ctx, "phone", h1, 2, 100, 1200); err != nil { // этого файла в новой версии нет
		t.Fatal(err)
	}
	s.SetDuration(ctx, h1, 1, 1200)
	*now = now.Add(-time.Hour)
	if err := s.SetPosition(ctx, "tv", h2, 5, 50, 1300); err != nil { // новую версию уже открывали, раньше
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	tx, err := s.db.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RekeyTx(ctx, tx, h1, h2, map[int]int{0: 5, 1: 6}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tv, _ := s.Files(ctx, "tv", h2)
	phone, _ := s.Files(ctx, "phone", h2)
	if len(tv) != 1 || tv[0].Index != 5 || !tv[0].Watched {
		t.Fatalf("телевизор: %+v", tv)
	}
	if len(phone) != 1 || phone[0].Index != 6 || phone[0].PositionSec != 300 {
		t.Fatalf("телефон: %+v", phone)
	}
	if old, _ := s.Files(ctx, "phone", h1); len(old) != 0 {
		t.Fatalf("строки прежней версии остались: %+v", old)
	}
	if d, ok := s.Duration(ctx, h2, 6); !ok || d != 1200 {
		t.Fatalf("длительность: %v %v", d, ok)
	}
}

// Новая серия просмотрена на любом устройстве — оповещение «Новые серии» уходит (спека 11b, 6.1).
func TestWatchedAny(t *testing.T) {
	s, _ := newService(t)
	if w, err := s.WatchedAny(ctx, h1, 3); err != nil || w {
		t.Fatalf("никто не смотрел: %v %v", w, err)
	}
	s.SetPosition(ctx, "tv", h1, 3, 100, 1200)
	if w, _ := s.WatchedAny(ctx, h1, 3); w {
		t.Fatal("начали, но не досмотрели")
	}
	s.SetWatched(ctx, "phone", h1, 3, true)
	if w, _ := s.WatchedAny(ctx, h1, 3); !w {
		t.Fatal("досмотрели на телефоне")
	}
}
