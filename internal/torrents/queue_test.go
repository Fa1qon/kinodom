package torrents

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"

	"kinodom/internal/torrents/torrenttest"
)

// Цвет кнопки «Смотреть» (спека этапа 7, раздел 5.5): не хранится — кнопки нет; скачан — зелёная;
// успеет докачаться, пока смотрят, — синяя; иначе жёлтая и через сколько можно смотреть.
func TestReadinessRules(t *testing.T) {
	const size, bitrate = 3600 * 1000, 1000.0 // час при 1000 байт/с
	cases := []struct {
		name   string
		stored bool
		done   int64
		speed  float64
		want   Readiness
		wait   int
	}{
		{"не хранится", false, 0, 5000, ReadyNone, 0},
		{"скачан", true, size, 0, ReadyDone, 0},
		{"быстрее просмотра", true, 0, 2000, ReadySmooth, 0},
		{"медленнее просмотра", true, 0, 500, ReadyWait, 3600},
		{"скорости нет", true, size / 2, 0, ReadyWait, -1},
	}
	for _, c := range cases {
		got, wait := readinessOf(c.stored, c.done, size, bitrate, c.speed)
		if got != c.want || wait != c.wait {
			t.Errorf("%s: %s, %d; ждали %s, %d", c.name, got, wait, c.want, c.wait)
		}
	}
}

// «Скачать» всю раздачу: хранятся все серии, качается одна — первая по порядку, остальные ждут
// очереди; «Смотреть» у серии из очереди переносит фокус на неё (спека этапа 7, раздел 5.5).
func TestDownloadQueuesEpisodesAndWatchMovesFocus(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Download(ctx, ih, nil))
	must(t, s.Download(ctx, ih, nil)) // повторное «Скачать» ничего не меняет
	tt, _ := s.Engine().Client().Torrent(ih)
	st, _ := s.Status(ih)
	if st.Focus != ep[0] || len(stored(t, s, ih)) != 4 {
		t.Fatalf("фокус %d, хранятся %v", st.Focus, stored(t, s, ih))
	}
	for k, i := range ep {
		want := torrent.PiecePriorityNone
		if k == 0 {
			want = torrent.PiecePriorityNormal
		}
		if p := tt.Files()[i].Priority(); p != want {
			t.Fatalf("серия %d: приоритет %v, ждали %v", k+1, p, want)
		}
	}
	for _, f := range st.Files {
		if f.Readiness != ReadyWait || f.Queued != (f.Index != ep[0]) {
			t.Fatalf("серия %s: %+v", f.Name, f)
		}
	}
	must(t, s.Prepare(ctx, ih, ep[2])) // жёлтая «Смотреть» у серии 3
	st, _ = s.Status(ih)
	if st.Focus != ep[2] || tt.Files()[ep[2]].Priority() != torrent.PiecePriorityNormal ||
		tt.Files()[ep[0]].Priority() != torrent.PiecePriorityNone {
		t.Fatalf("фокус не перешёл: %d", st.Focus)
	}
	if f, _, _ := s.reg.StoredFile(ctx, ih, ep[0]); f.Index != ep[0] {
		t.Fatal("серия 1 снята с хранения")
	}
}

// Докачался файл в фокусе — качается следующий, пока не скачано всё; прогресс и цвет — по файлам.
func TestQueueAdvancesToTheEnd(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "Сезон", 64<<10,
		torrenttest.File{Path: "Серия 1.mkv", Size: 256 << 10}, torrenttest.File{Path: "Серия 2.mkv", Size: 256 << 10},
		torrenttest.File{Path: "Серия 3.mkv", Size: 256 << 10})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	ih, err := s.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
	must(t, err)
	runService(t, s)
	connect(t, s, ih, seeder)
	must(t, s.Download(ctx, ih, nil))
	var st TorrentStatus
	deadline := time.Now().Add(20 * time.Second)
	for {
		st, _ = s.Status(ih)
		done := 0
		for _, f := range st.Files {
			if f.Readiness == ReadyDone && f.Percent == 100 {
				done++
			}
		}
		if done == 3 && st.Focus == -1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("очередь не дошла до конца: %+v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// «Скачать» раньше, чем пришёл список файлов (magnet): намерение запоминается и применяется, когда
// метаинфо пришла; раздача без дела не убирается уборкой, пока ждёт.
func TestDownloadBeforeMetadata(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "Сезон", 64<<10,
		torrenttest.File{Path: "Серия 1.mkv", Size: 128 << 10}, torrenttest.File{Path: "Серия 2.mkv", Size: 128 << 10})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	ih, err := s.Open(ctx, Source{Magnet: magnetOf(mi)})
	must(t, err)
	must(t, s.Download(ctx, ih, nil))
	if err := s.Download(ctx, ih, []int{0}); err != ErrNoInfo {
		t.Fatalf("файл по номеру без списка файлов: %v", err)
	}
	if p, _ := s.reg.Pending(ctx); len(p) != 1 {
		t.Fatalf("намерение не запомнено: %+v", p)
	}
	s.mu.Lock()
	s.sessions[ih].lastSeen = s.now().Add(-2 * time.Hour) // о раздаче давно не спрашивали
	s.mu.Unlock()
	must(t, s.sweep(ctx))
	if _, ok := s.Status(ih); !ok {
		t.Fatal("уборка убрала раздачу, ждущую списка файлов")
	}
	runService(t, s)
	connect(t, s, ih, seeder)
	waitFor(t, "файлы стали хранимыми", func() bool { return len(stored(t, s, ih)) == 2 })
	if p, _ := s.reg.Pending(ctx); len(p) != 0 {
		t.Fatalf("намерение не снято: %+v", p)
	}
}

// «Скачать» без списка файлов переживает перезапуск: раздача снова открывается и ждёт метаинфо.
func TestPendingDownloadSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	src := t.TempDir()
	mi, _ := torrenttest.MakeTorrent(t, src, "Фильм", 64<<10, torrenttest.File{Path: "film.mkv", Size: 128 << 10})
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	e1, err := NewEngine(Config{DownloadsDir: t.TempDir(), StateDir: t.TempDir(), Offline: true, Log: quiet()})
	must(t, err)
	s1 := serviceFor(e1, NewRegistry(db))
	ih, err := s1.Open(ctx, Source{Magnet: magnetOf(mi)})
	must(t, err)
	must(t, s1.Download(ctx, ih, nil))
	e1.Close()

	s2 := serviceFor(newOfflineEngine(t), NewRegistry(db))
	runService(t, s2)
	waitFor(t, "раздача открыта снова", func() bool { _, ok := s2.Status(ih); return ok })
	connect(t, s2, ih, seeder)
	waitFor(t, "файл стал хранимым", func() bool { return len(stored(t, s2, ih)) == 1 })
}

// Перезапуск посреди сериала: фокус остаётся на серии, которую выбрали жёлтой «Смотреть», а не
// возвращается к первой недокачанной (Review Focus 3 плана 7a).
func TestFocusSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(newTestDB(t))
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Сериал", 64<<10,
		torrenttest.File{Path: "Серия 1.mkv", Size: 300_000},
		torrenttest.File{Path: "Серия 2.mkv", Size: 300_000},
		torrenttest.File{Path: "Серия 3.mkv", Size: 300_000})
	ih := mi.HashInfoBytes()
	must(t, reg.SaveMetainfo(ctx, ih, "Сериал", torrentBytes(t, mi)))
	for i := range 3 {
		must(t, reg.MarkStored(ctx, ih, i, filepath.Join(`D:\K`, "Серия.mkv"), 300_000, time.Now()))
	}
	must(t, reg.SetFocus(ctx, ih, 2))

	s := serviceFor(newOfflineEngine(t), reg)
	must(t, s.restore(ctx))
	st, ok := s.Status(ih)
	tt, _ := s.Engine().Client().Torrent(ih)
	if !ok || st.Focus != 2 || tt.Files()[2].Priority() != torrent.PiecePriorityNormal || tt.Files()[0].Priority() != torrent.PiecePriorityNone {
		t.Fatalf("после перезапуска фокус %d, приоритеты серий 1 и 3: %v, %v", st.Focus, tt.Files()[0].Priority(), tt.Files()[2].Priority())
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Файл, который смотрят, качается вне очереди: иначе у второго телевизора кончилась бы подкачка;
// зритель ушёл — файл снова ждёт очереди.
func TestWatchedFileDownloadsOutsideQueue(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Download(ctx, ih, nil))
	tt, _ := s.Engine().Client().Torrent(ih)
	closeReader := s.openReader(ih, ep[2])
	if p := tt.Files()[ep[2]].Priority(); p != torrent.PiecePriorityNormal {
		t.Fatalf("серию смотрят, а она ждёт очереди: %v", p)
	}
	closeReader()
	if p := tt.Files()[ep[2]].Priority(); p != torrent.PiecePriorityNone {
		t.Fatalf("зритель ушёл, а серия качается вне очереди: %v", p)
	}
}

// Раздача качалась на диск, которого сейчас нет: понятный текст, а не ошибка хранилища (ревью этапа 6).
func TestOpenFromMissingDiskIsExplained(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 100_000})
	gone := filepath.Join(t.TempDir(), "usb")
	if _, err := s.reg.Remember(ctx, mi.HashInfoBytes(), "torrent-file", gone); err != nil {
		t.Fatal(err)
	}
	_, err := s.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
	if err == nil || !strings.Contains(err.Error(), "папка раздачи недоступна") {
		t.Fatalf("ошибка %v", err)
	}
}
