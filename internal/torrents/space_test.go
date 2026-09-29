package torrents

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/torrents/torrenttest"
)

const mib = 1 << 20

// fakeDisk — диск на capacity байт, занятый хранимыми файлами целиком (как будто скачаны):
// удалённый файл сразу освобождает своё место.
func fakeDisk(s *Service, capacity int64) {
	s.freeSpace = func(string) (int64, error) {
		var used int64
		s.reg.db.R.QueryRow("SELECT COALESCE(SUM(size), 0) FROM stored_files").Scan(&used)
		return capacity - used, nil
	}
}

// archive — открытая раздача из четырёх серий по 1 МиБ без раздающих: выбранные серии хранятся
// и числятся недокачанными.
func archive(t *testing.T, s *Service) (metainfo.Hash, []int) {
	t.Helper()
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Архив", 64<<10,
		torrenttest.File{Path: "Серия 1.mkv", Size: mib}, torrenttest.File{Path: "Серия 2.mkv", Size: mib},
		torrenttest.File{Path: "Серия 3.mkv", Size: mib}, torrenttest.File{Path: "Серия 4.mkv", Size: mib})
	ih, err := s.Open(context.Background(), Source{Torrent: torrentBytes(t, mi)})
	must(t, err)
	tt, _ := s.Engine().Client().Torrent(ih)
	var eps []int
	for _, n := range []string{"Серия 1.mkv", "Серия 2.mkv", "Серия 3.mkv", "Серия 4.mkv"} {
		eps = append(eps, fileIndex(t, tt, n))
	}
	return ih, eps
}

func openedAgo(t *testing.T, s *Service, ih metainfo.Hash, index int, ago time.Duration) {
	t.Helper()
	_, err := s.reg.db.W.Exec("UPDATE stored_files SET last_opened_at = ? WHERE infohash = ? AND file_index = ?",
		s.now().Add(-ago).UnixMilli(), ih.HexString(), index)
	must(t, err)
}

func stored(t *testing.T, s *Service, ih metainfo.Hash) []int {
	t.Helper()
	idx, err := s.reg.StoredFiles(context.Background(), ih)
	must(t, err)
	slices.Sort(idx)
	return idx
}

func sorted(xs ...int) []int { slices.Sort(xs); return xs }

// Места не хватает на новую серию — удаляются самые давно открытые, пока не хватит (спека, раздел 9).
func TestPrepareFreesOldestFirst(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Prepare(ctx, ih, ep[0]))
	must(t, s.Prepare(ctx, ih, ep[1]))
	openedAgo(t, s, ih, ep[0], 5*24*time.Hour)
	openedAgo(t, s, ih, ep[1], 3*24*time.Hour)
	// Диск 10 МиБ, занято 2, докачать 2, новая серия — 1: свободно останется 5 при запасе 6.
	fakeDisk(s, 10*mib)
	s.SetPolicy(Policy{MinFree: 6 * mib})
	must(t, s.Prepare(ctx, ih, ep[2]))
	if got := stored(t, s, ih); !slices.Equal(got, sorted(ep[1], ep[2])) {
		t.Fatalf("хранятся %v — удалить нужно было только самую давно открытую серию", got)
	}
}

// Даже удаление всего не спасёт — отказ, и ничего не удалено.
func TestPrepareRefusesWithoutDeletingWhenCleanupCannotHelp(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Prepare(ctx, ih, ep[0]))
	openedAgo(t, s, ih, ep[0], 5*24*time.Hour) // удалить её можно, но это не поможет
	fakeDisk(s, 10*mib)
	s.SetPolicy(Policy{MinFree: 100 * mib})
	err := s.Prepare(ctx, ih, ep[1])
	if !errors.Is(err, ErrLowSpace) || !strings.Contains(err.Error(), "ГБ") {
		t.Fatalf("ошибка %v", err)
	}
	if got := stored(t, s, ih); !slices.Equal(got, []int{ep[0]}) {
		t.Fatalf("хранятся %v — удалять было незачем", got)
	}
}

// Файл, который смотрят, очистка не трогает — удаляется следующий по давности.
func TestCleanupSkipsWatchedFile(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Prepare(ctx, ih, ep[0]))
	must(t, s.Prepare(ctx, ih, ep[1]))
	openedAgo(t, s, ih, ep[0], 5*24*time.Hour)
	openedAgo(t, s, ih, ep[1], 3*24*time.Hour)
	must(t, s.reg.TouchStream(ctx, ih, ep[0], s.now().Add(-time.Hour))) // смотрели час назад
	openedAgo(t, s, ih, ep[0], 5*24*time.Hour)
	fakeDisk(s, 10*mib)
	s.SetPolicy(Policy{MinFree: 6 * mib})
	must(t, s.Prepare(ctx, ih, ep[2]))
	if got := stored(t, s, ih); !slices.Equal(got, sorted(ep[0], ep[2])) {
		t.Fatalf("хранятся %v — удалить нужно было вторую серию, а не ту, что смотрят", got)
	}
}

// Раз в сутки удаляется то, что не открывали дольше срока хранения, кроме того, что смотрят.
func TestExpireDeletesFilesPastKeepFor(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	for _, i := range ep[:3] {
		must(t, s.Prepare(ctx, ih, i))
	}
	openedAgo(t, s, ih, ep[0], 15*24*time.Hour)
	openedAgo(t, s, ih, ep[1], 13*24*time.Hour)
	must(t, s.reg.TouchStream(ctx, ih, ep[2], s.now().Add(-time.Hour)))
	openedAgo(t, s, ih, ep[2], 20*24*time.Hour) // открывали давно, но смотрят сейчас
	must(t, s.expire(ctx))
	if got := stored(t, s, ih); !slices.Equal(got, sorted(ep[1], ep[2])) {
		t.Fatalf("хранятся %v", got)
	}
}

// Места мало, очистка не помогла — фоновые докачки на паузе, поток не трогается; место
// появилось — докачки продолжаются, проблема снята.
func TestLowSpacePausesBackgroundDownloads(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Prepare(ctx, ih, ep[0]))
	must(t, s.Prepare(ctx, ih, ep[1]))
	s.mu.Lock()
	s.sessions[ih].readers[ep[1]]++ // вторую серию смотрят
	s.mu.Unlock()
	free := int64(mib)
	var mu sync.Mutex
	s.freeSpace = func(string) (int64, error) { mu.Lock(); defer mu.Unlock(); return free, nil }
	must(t, s.checkSpace(ctx))
	tt, _ := s.Engine().Client().Torrent(ih)
	if p := tt.Files()[ep[0]].Priority(); p != torrent.PiecePriorityNone {
		t.Fatalf("докачка не на паузе: приоритет %v", p)
	}
	if p := tt.Files()[ep[1]].Priority(); p != torrent.PiecePriorityNormal {
		t.Fatalf("серию, которую смотрят, поставили на паузу: приоритет %v", p)
	}
	if p := problemText(t, s.reg.db, "torrents.space"); !strings.Contains(p, "Мало места на диске") {
		t.Fatalf("проблема %q", p)
	}
	if got := stored(t, s, ih); len(got) != 2 {
		t.Fatalf("при нехватке, которую очистка не покроет, удалено: %v", got)
	}
	mu.Lock()
	free = 1 << 50
	mu.Unlock()
	must(t, s.checkSpace(ctx))
	if p := tt.Files()[ep[0]].Priority(); p != torrent.PiecePriorityNormal {
		t.Fatalf("место есть, а докачка стоит: приоритет %v", p)
	}
	if p := problemText(t, s.reg.db, "torrents.space"); p != "" {
		t.Fatalf("проблема не снята: %q", p)
	}
}

// Открытая, но не выбранная раздача без дела дольше часа убирается вместе с папкой пустых
// файлов; открытая недавно — остаётся (хвост этапа 2).
func TestIdleOpenedTorrentIsForgotten(t *testing.T) {
	ctx := context.Background()
	clk := &testClock{t: time.Now()}
	s := newTestService(t)
	s.now = clk.now
	old, _ := torrenttest.MakeTorrent(t, t.TempDir(), "old.mkv", 64<<10, torrenttest.File{Path: "old.mkv", Size: 100_000})
	ihOld, err := s.Open(ctx, Source{Torrent: torrentBytes(t, old)})
	must(t, err)
	ttOld, _ := s.Engine().Client().Torrent(ihOld)
	dirOld := torrentDir(s.Engine().TorrentDir(ihOld), ttOld.Info(), ihOld)
	clk.add(50 * time.Minute)
	fresh, _ := torrenttest.MakeTorrent(t, t.TempDir(), "new.mkv", 64<<10, torrenttest.File{Path: "new.mkv", Size: 100_000})
	ihNew, err := s.Open(ctx, Source{Torrent: torrentBytes(t, fresh)})
	must(t, err)
	clk.add(11 * time.Minute)
	must(t, s.sweep(ctx))
	if _, ok := s.Status(ihOld); ok {
		t.Fatal("раздача без дела больше часа не убрана")
	}
	if _, err := os.Stat(dirOld); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("папка пустых файлов осталась: %v", err)
	}
	if _, ok := s.Status(ihNew); !ok {
		t.Fatal("убрана раздача, открытая 11 минут назад")
	}
}

// Запись о раздаче без файлов, которой нет среди открытых (ошибка «нет раздающих»), убирается.
func TestOrphanRecordIsForgotten(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	remember(t, s.reg, hashOf(t, "e"), "magnet:?xt=urn:btih:ee")
	must(t, s.sweep(ctx))
	var n int
	s.reg.db.R.QueryRow("SELECT COUNT(*) FROM torrents").Scan(&n)
	if n != 1 {
		t.Fatal("свежую запись убирать рано")
	}
	clk := &testClock{t: time.Now().Add(2 * time.Hour)}
	s.now = clk.now
	must(t, s.sweep(ctx))
	s.reg.db.R.QueryRow("SELECT COUNT(*) FROM torrents").Scan(&n)
	if n != 0 {
		t.Fatal("запись без файлов и без раздачи осталась")
	}
}

// Две серии выбирают одновременно с двух телевизоров — обе хранятся и качаются (хвост этапа 2).
func TestConcurrentPrepareKeepsBoth(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for k := range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); errs[k] = s.Prepare(ctx, ih, ep[k]) }()
	}
	wg.Wait()
	must(t, errors.Join(errs...))
	tt, _ := s.Engine().Client().Torrent(ih)
	for _, i := range ep[:2] {
		if p := tt.Files()[i].Priority(); p != torrent.PiecePriorityNormal {
			t.Fatalf("серия %d: приоритет %v", i, p)
		}
	}
	if got := stored(t, s, ih); !slices.Equal(got, sorted(ep[0], ep[1])) {
		t.Fatalf("хранятся %v", got)
	}
}

// Поток — это и открытие файла: срок хранения считается от него, но не чаще раза в минуту.
func TestStreamCountsAsOpening(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(newTestDB(t))
	ih := hashOf(t, "f")
	remember(t, reg, ih, "x")
	t0 := time.Now().Add(-24 * time.Hour)
	must(t, reg.MarkStored(ctx, ih, 0, `D:\K\f.mkv`, 1, t0))
	must(t, reg.TouchStream(ctx, ih, 0, t0.Add(30*time.Second)))
	f, _, _ := reg.StoredFile(ctx, ih, 0)
	if !f.LastOpened.Equal(time.UnixMilli(t0.UnixMilli())) {
		t.Fatalf("открытие обновилось раньше чем через минуту: %v", f.LastOpened)
	}
	later := t0.Add(2 * time.Hour)
	must(t, reg.TouchStream(ctx, ih, 0, later))
	f, _, _ = reg.StoredFile(ctx, ih, 0)
	if !f.LastOpened.Equal(time.UnixMilli(later.UnixMilli())) || !f.LastStream.Equal(time.UnixMilli(later.UnixMilli())) {
		t.Fatalf("после потока: открыт %v, поток %v", f.LastOpened, f.LastStream)
	}
}

func TestPrepareLowSpaceRoute(t *testing.T) {
	s := newTestService(t)
	fakeDisk(s, 10*mib)
	s.SetPolicy(Policy{MinFree: 100 * mib})
	mux := http.NewServeMux()
	s.Register(testRouter{mux})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ih, ep := archive(t, s)
	var e struct{ Error string }
	code := call(t, "POST", srv.URL+"/api/v1/torrents/"+ih.HexString()+"/files/"+strconv.Itoa(ep[0])+"/prepare", nil, &e)
	if code != http.StatusInsufficientStorage || !strings.Contains(e.Error, "мало места на диске") {
		t.Fatalf("%d %q", code, e.Error)
	}
}

// Файл, который только что выбрали (телевизор ещё буферизует), очистка не трогает, даже если
// его ещё ни разу не смотрели.
func TestCleanupSparesJustChosenFile(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Prepare(ctx, ih, ep[0])) // выбран сейчас
	must(t, s.Prepare(ctx, ih, ep[1]))
	openedAgo(t, s, ih, ep[1], 3*24*time.Hour)
	fakeDisk(s, 10*mib)
	s.SetPolicy(Policy{MinFree: 6 * mib})
	must(t, s.Prepare(ctx, ih, ep[2]))
	if got := stored(t, s, ih); !slices.Equal(got, sorted(ep[0], ep[2])) {
		t.Fatalf("хранятся %v — удалить нужно было давно открытую серию, а не только что выбранную", got)
	}
}
