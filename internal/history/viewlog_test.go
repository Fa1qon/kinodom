package history

import (
	"context"
	"testing"
	"time"
)

// play — человек смотрит подряд от from до to (доли) фильма длиной dur, сообщения раз в 10 с, как у потока.
func play(t *testing.T, s *Service, now *time.Time, device, hash string, dur, from, to float64) {
	t.Helper()
	for pos := from * dur; pos <= to*dur; pos += 10 {
		if err := s.SetPosition(ctx, device, hash, 0, pos, dur); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(10 * time.Second)
	}
}

func view(t *testing.T, s *Service, owner, hash string) View {
	t.Helper()
	vs, err := s.Views(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if v.Hash == hash {
			return v
		}
	}
	t.Fatalf("нет записи %s у %s: %+v", hash, owner, vs)
	return View{}
}

func TestViewLogFinishedVersusSeekToEnd(t *testing.T) {
	s, now := newService(t)
	const dur = 600.0 // короткий фильм — тест быстрый
	play(t, s, now, "tv", h1, dur, 0, 0.95)
	v := view(t, s, "d:tv", h1)
	if !v.Finished || v.Times != 1 || v.Seen < 0.9 {
		t.Fatalf("досмотрел подряд: %+v", v)
	}
	// второй фильм: 5 % посмотрел, перемотал к финалу, минуту посмотрел и закрыл
	play(t, s, now, "tv", h2, dur, 0, 0.05)
	play(t, s, now, "tv", h2, dur, 0.92, 0.99)
	v = view(t, s, "d:tv", h2)
	if v.Finished || v.Max < 0.98 || v.Seen > 0.2 {
		t.Fatalf("перемотка к финалу не делает «досмотренным»: %+v", v)
	}
	// в истории устройства (для «Продолжить») — по-прежнему «просмотрено»: это не меняли
	fs, _ := s.Files(ctx, "tv", h2)
	if len(fs) != 1 || !fs[0].Watched {
		t.Fatalf("история устройства: %+v", fs)
	}
}

func TestViewLogAbandonSessionsRewatch(t *testing.T) {
	s, now := newService(t)
	const dur = 600.0
	play(t, s, now, "tv", h1, dur, 0, 0.08) // бросил на 8 %
	v := view(t, s, "d:tv", h1)
	if v.Finished || v.Sessions != 1 || v.Last > 0.1 || v.Seen < 0.06 {
		t.Fatalf("брошенный: %+v", v)
	}
	// через день вернулся с того же места и досмотрел — второй сеанс, досмотрен
	*now = now.Add(24 * time.Hour)
	play(t, s, now, "tv", h1, dur, 0.08, 1)
	v = view(t, s, "d:tv", h1)
	if !v.Finished || v.Sessions != 2 {
		t.Fatalf("вернулся и досмотрел: %+v", v)
	}
	// через месяц пересмотрел целиком
	*now = now.Add(30 * 24 * time.Hour)
	play(t, s, now, "tv", h1, dur, 0, 1)
	if v = view(t, s, "d:tv", h1); v.Times != 2 || v.Sessions != 3 {
		t.Fatalf("пересмотр: %+v", v)
	}
}

func TestViewLogOwnerFilmAndEnd(t *testing.T) {
	s, now := newService(t)
	s.SetOwner(func(_ context.Context, device string) string {
		if device == "tv" {
			return "p:2" // на телевизоре — профиль «Маша»
		}
		return ""
	})
	film := 0
	s.SetFilmOf(func(_ context.Context, hash string) int { return film })
	play(t, s, now, "tv", h1, 600, 0, 0.1)
	play(t, s, now, "pc", h2, 600, 0, 0.1)
	if v := view(t, s, "p:2", h1); v.KP != 0 {
		t.Fatalf("фильм ещё не опознан: %+v", v)
	}
	view(t, s, "d:pc", h2)
	// раздачу опознали позже — журнал дописывает фильм
	film = 301
	if err := s.ResolveFilms(ctx); err != nil {
		t.Fatal(err)
	}
	if v := view(t, s, "p:2", h1); v.KP != 301 {
		t.Fatalf("фильм не дописан: %+v", v)
	}
	// удалил сам; уборка потом не перезаписывает «удалил»
	if err := s.MarkEnded(ctx, h1, 0, EndedDeleted, ""); err != nil {
		t.Fatal(err)
	}
	s.MarkEnded(ctx, h1, -1, EndedExpired, "")
	if v := view(t, s, "p:2", h1); v.Ended != EndedDeleted || v.EndedAt.IsZero() {
		t.Fatalf("удаление: %+v", v)
	}
	// «убрать из истории» — запись в журнале остаётся с отметкой
	if err := s.Remove(ctx, "pc", h2); err != nil {
		t.Fatal(err)
	}
	if v := view(t, s, "d:pc", h2); v.Ended != EndedRemoved {
		t.Fatalf("убрал из истории: %+v", v)
	}
	// снова начал смотреть удалённое — отметка снимается
	play(t, s, now, "tv", h1, 600, 0.1, 0.2)
	if v := view(t, s, "p:2", h1); v.Ended != "" {
		t.Fatalf("смотрит снова: %+v", v)
	}
}

func TestViewLogManualMark(t *testing.T) {
	s, _ := newService(t)
	if err := s.SetWatched(ctx, "tv", h1, 3, true); err != nil {
		t.Fatal(err)
	}
	vs, _ := s.Views(ctx, "d:tv")
	if len(vs) != 1 || !vs[0].Finished || vs[0].Seen != 0 {
		t.Fatalf("отметка «просмотрено»: %+v", vs)
	}
}
