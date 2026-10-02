package torrents

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	return archiveNamed(t, s, "Архив")
}

// archiveNamed — то же с другим названием: другая раздача (другой infohash).
func archiveNamed(t *testing.T, s *Service, name string) (metainfo.Hash, []int) {
	t.Helper()
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), name, 64<<10,
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
	// Диск 10 МиБ, занято 2, докачивается сейчас 1 (серия 2 в фокусе, серия 1 ждёт очереди и места
	// не занимает), новая серия — 1: свободно останется 6 при запасе 7.
	fakeDisk(s, 10*mib)
	s.SetPolicy(Policy{MinFree: 7 * mib})
	must(t, s.Prepare(ctx, ih, ep[2]))
	if got := stored(t, s, ih); !slices.Equal(got, sorted(ep[1], ep[2])) {
		t.Fatalf("хранятся %v — удалить нужно было только самую давно открытую серию", got)
	}
}

// Места на три серии, в сезоне шесть: качаются первые три, дальше — пауза и предупреждение.
// Досмотрели до третьей — удаляется самая ранняя серия позади (одна позади остаётся: второй
// телевизор может отставать), на освободившееся место качается четвёртая. У серий позади нет
// правила 6 часов (решение заказчика, этап 7a).
func TestSpaceWindowSlidesPastWatchedEpisodes(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	const ep = 256 << 10
	src := t.TempDir()
	var files []torrenttest.File
	for i := 1; i <= 6; i++ {
		files = append(files, torrenttest.File{Path: "Серия " + strconv.Itoa(i) + ".mkv", Size: ep})
	}
	mi, _ := torrenttest.MakeTorrent(t, src, "Сезон", 64<<10, files...)
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	ih, err := s.Open(ctx, Source{Torrent: torrentBytes(t, mi)})
	must(t, err)
	tt, _ := s.Engine().Client().Torrent(ih)
	idx := make([]int, 6)
	for i := range idx {
		idx[i] = fileIndex(t, tt, "Серия "+strconv.Itoa(i+1)+".mkv")
	}
	s.SetPolicy(Policy{MinFree: mib, KeepBehind: 1})
	s.freeSpace = func(string) (int64, error) { return mib + 3*ep + ep/2 - tt.BytesCompleted(), nil }
	runService(t, s)
	connect(t, s, ih, seeder)
	must(t, s.Download(ctx, ih, nil))
	warned := func() bool {
		return strings.Contains(problemText(t, s.reg.db, "torrents.space"), "удалите лишнее в «Загрузках»")
	}
	waitFor(t, "первые три серии скачаны, четвёртая на паузе, предупреждение", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		ss := s.sessions[ih]
		return tt.Files()[idx[2]].BytesCompleted() == ep && ss.focus == idx[3] && ss.paused[idx[3]] && warned()
	})
	if tt.Files()[idx[3]].BytesCompleted() == ep {
		t.Fatal("четвёртая серия скачалась сверх запаса")
	}
	// В «Загрузках» на паузе — только серия, до которой дошла очередь; дальние ждут очереди.
	v, err := s.Downloads(ctx)
	must(t, err)
	byIndex := map[int]DownloadState{}
	for _, it := range v.Items {
		byIndex[it.Index] = it.State
	}
	if byIndex[idx[3]] != DownloadPaused || byIndex[idx[4]] != DownloadQueued || !v.LowSpace {
		t.Fatalf("«Загрузки»: четвёртая %q, пятая %q, мало места %v", byIndex[idx[3]], byIndex[idx[4]], v.LowSpace)
	}
	// Досмотрели до третьей; первые две смотрели два и час назад.
	now := s.now()
	must(t, s.reg.TouchStream(ctx, ih, idx[0], now.Add(-2*time.Hour)))
	must(t, s.reg.TouchStream(ctx, ih, idx[1], now.Add(-time.Hour)))
	must(t, s.reg.TouchStream(ctx, ih, idx[2], now))
	must(t, s.checkSpace(ctx))
	if got := stored(t, s, ih); slices.Contains(got, idx[0]) || !slices.Contains(got, idx[1]) || !slices.Contains(got, idx[2]) {
		t.Fatalf("хранятся %v: удалить нужно было только серию 1", got)
	}
	waitFor(t, "четвёртая серия скачана на освободившееся место", func() bool {
		return tt.Files()[idx[3]].BytesCompleted() == ep
	})
	time.Sleep(1500 * time.Millisecond) // такт Run: переход к пятой серии и проверка места
	if tt.Files()[idx[4]].BytesCompleted() == ep || !warned() {
		t.Fatalf("пятая серия скачана: %v, предупреждение: %v", tt.Files()[idx[4]].BytesCompleted() == ep, warned())
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
	s.SetPolicy(Policy{MinFree: 7 * mib}) // как в TestPrepareFreesOldestFirst
	must(t, s.Prepare(ctx, ih, ep[2]))
	if got := stored(t, s, ih); !slices.Equal(got, sorted(ep[0], ep[2])) {
		t.Fatalf("хранятся %v — удалить нужно было вторую серию, а не ту, что смотрят", got)
	}
}

// Срок хранения — по раздаче целиком, от последнего открытия любого её файла (спека этапа 9,
// раздел 5.8): брошенная раздача удаляется вся, включая не открытые серии; сериал, который смотрят
// по серии в день, не теряет первые серии; ни разу не открытая раздача не удаляется; раздача, одну
// серию которой смотрят сейчас, ждёт.
func TestExpireByRelease(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	gone, a := archiveNamed(t, s, "Брошенный")
	daily, b := archiveNamed(t, s, "По серии в день")
	later, c := archiveNamed(t, s, "На потом")
	busy, d := archiveNamed(t, s, "Смотрят")
	type rel struct {
		ih  metainfo.Hash
		eps []int
	}
	for _, x := range []rel{{gone, a}, {daily, b}, {later, c}, {busy, d}} {
		for _, i := range x.eps[:2] {
			must(t, s.Prepare(ctx, x.ih, i))
		}
	}
	openedAgo(t, s, gone, a[0], 15*24*time.Hour) // a[1] не открывали
	openedAgo(t, s, daily, b[0], 20*24*time.Hour)
	openedAgo(t, s, daily, b[1], 24*time.Hour) // вчера — вся раздача живёт
	must(t, s.reg.TouchStream(ctx, busy, d[1], s.now().Add(-time.Hour)))
	openedAgo(t, s, busy, d[0], 20*24*time.Hour)
	openedAgo(t, s, busy, d[1], 20*24*time.Hour)
	must(t, s.expire(ctx))
	if got := stored(t, s, gone); len(got) != 0 {
		t.Errorf("брошенная раздача: хранятся %v", got)
	}
	for name, x := range map[string]rel{"по серии в день": {daily, b}, "на потом": {later, c}, "смотрят": {busy, d}} {
		if got := stored(t, s, x.ih); !slices.Equal(got, sorted(x.eps[0], x.eps[1])) {
			t.Errorf("%s: хранятся %v", name, got)
		}
	}
}

// Нехватка места не трогает ни разу не открытое (скачанное заранее): удаляется открытая давно
// раздача; не помогает — отказ, ничего не удалено.
func TestLowSpaceSparesNeverOpened(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	early, e := archiveNamed(t, s, "Скачано заранее")
	seen, f := archiveNamed(t, s, "Смотрели")
	must(t, s.Prepare(ctx, early, e[0]))
	must(t, s.Prepare(ctx, early, e[1]))
	must(t, s.Prepare(ctx, seen, f[0]))
	openedAgo(t, s, seen, f[0], 5*24*time.Hour)
	// Диск 10 МиБ, занято 3, докачиваются 2 (по файлу в фокусе каждой раздачи), новая серия — 1:
	// свободно останется 4 при запасе 5 — удалить нужно 1 МиБ.
	fakeDisk(s, 10*mib)
	s.SetPolicy(Policy{MinFree: 5 * mib})
	must(t, s.Prepare(ctx, seen, f[1]))
	if got := stored(t, s, early); !slices.Equal(got, sorted(e[0], e[1])) {
		t.Errorf("скачанное заранее тронуто: %v", got)
	}
	if got := stored(t, s, seen); !slices.Equal(got, []int{f[1]}) {
		t.Errorf("открытая давно: хранятся %v", got)
	}
	s.SetPolicy(Policy{MinFree: 8 * mib})
	if err := s.Prepare(ctx, seen, f[2]); !errors.Is(err, ErrLowSpace) {
		t.Errorf("удалять нечего — нужен отказ: %v", err)
	}
	if got := stored(t, s, early); !slices.Equal(got, sorted(e[0], e[1])) {
		t.Errorf("после отказа скачанное заранее тронуто: %v", got)
	}
}

// Места мало, очистка не помогла — фоновые докачки на паузе, поток не трогается; место
// появилось — докачки продолжаются, проблема снята.
func TestLowSpacePausesBackgroundDownloads(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Prepare(ctx, ih, ep[1]))
	must(t, s.Prepare(ctx, ih, ep[0])) // фокус очереди — на первой серии
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
	if p := problemText(t, s.reg.db, "torrents.space"); !strings.Contains(p, "Мало места: на диске") {
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
	// Целиком качается одна (фокус очереди), но буфер — начало файла — набирают обе: второй
	// телевизор не ждёт, пока докачается чужая серия (этап 7).
	normal := 0
	for _, i := range ep[:2] {
		if tt.Files()[i].Priority() == torrent.PiecePriorityNormal {
			normal++
		}
		if p := tt.PieceState(tt.Files()[i].BeginPieceIndex()).Priority; p < torrent.PiecePriorityHigh {
			t.Fatalf("серия %d: начало файла не в приоритете (%v)", i, p)
		}
	}
	if normal != 1 {
		t.Fatalf("целиком качаются %d серий, а не одна", normal)
	}
	if got := stored(t, s, ih); !slices.Equal(got, sorted(ep[0], ep[1])) {
		t.Fatalf("хранятся %v", got)
	}
}

// Открытие — только поток (спека этапа 9, раздел 5.8): «Скачать» и выбор файла открытием не считаются;
// срок хранения считается от потока, но не чаще раза в минуту.
func TestStreamCountsAsOpening(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(newTestDB(t))
	ih := hashOf(t, "f")
	remember(t, reg, ih, "x")
	t0 := time.Now().Add(-24 * time.Hour)
	must(t, reg.MarkStored(ctx, ih, 0, `D:\K\f.mkv`, 1))
	if f, _, _ := reg.StoredFile(ctx, ih, 0); !f.LastOpened.IsZero() {
		t.Fatalf("выбор файла посчитан открытием: %v", f.LastOpened)
	}
	must(t, reg.TouchStream(ctx, ih, 0, t0))
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
	s.SetPolicy(Policy{MinFree: 7 * mib}) // как в TestPrepareFreesOldestFirst
	must(t, s.Prepare(ctx, ih, ep[2]))
	if got := stored(t, s, ih); !slices.Equal(got, sorted(ep[0], ep[2])) {
		t.Fatalf("хранятся %v — удалить нужно было давно открытую серию, а не только что выбранную", got)
	}
}

// Уже скачанный (хранимый) файл после перезапуска открывается и при малом месте: ему не нужно
// ни байта, очистка и отказ «мало места» к нему не относятся (ревью этапа 6, I1).
func TestPrepareStoredFileIgnoresLowSpace(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Prepare(ctx, ih, ep[0]))
	s.mu.Lock()
	delete(s.sessions[ih].prepared, ep[0]) // как после перезапуска: хранится, но не выбран
	s.mu.Unlock()
	fakeDisk(s, 10*mib)
	s.SetPolicy(Policy{MinFree: 100 * mib})
	if err := s.Prepare(ctx, ih, ep[0]); err != nil {
		t.Fatalf("хранимый файл не открылся: %v", err)
	}
}

// Очистка ради новой серии удаляет последнюю хранимую серию той же раздачи — раздачу при этом не
// выгружают: её листает телевизор, и Prepare не должен ответить «раздача не открыта» (ревью, I2).
func TestCleanupKeepsTorrentInUseLoaded(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	ih, ep := archive(t, s)
	must(t, s.Prepare(ctx, ih, ep[0]))
	openedAgo(t, s, ih, ep[0], 5*24*time.Hour)
	fakeDisk(s, 10*mib)
	s.SetPolicy(Policy{MinFree: 7*mib + mib/2})
	if err := s.Prepare(ctx, ih, ep[1]); err != nil {
		t.Fatalf("следующая серия не выбралась: %v (хранятся %v)", err, stored(t, s, ih))
	}
	if got := stored(t, s, ih); !slices.Equal(got, []int{ep[1]}) {
		t.Fatalf("хранятся %v", got)
	}
}

// Диск с загрузками не вернулся: записи о файлах, которые не открывали дольше срока хранения,
// снимаются — иначе баннер «папка недоступна» висел бы вечно; удалить такой файл вручную нельзя,
// и текст говорит честно, что делать (ревью, I5).
func TestExpireForgetsRecordsOnMissingDisk(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	reg := NewRegistry(db)
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "film.mkv", 64<<10, torrenttest.File{Path: "film.mkv", Size: 300_000})
	ih := mi.HashInfoBytes()
	usb := filepath.Join(t.TempDir(), "USB")
	if _, err := reg.Remember(ctx, ih, "torrent-file", usb); err != nil {
		t.Fatal(err)
	}
	must(t, reg.SaveMetainfo(ctx, ih, "film.mkv", torrentBytes(t, mi)))
	must(t, reg.MarkStored(ctx, ih, 0, filepath.Join(usb, "film.mkv"), 300_000))
	if _, err := db.W.Exec("UPDATE stored_files SET last_opened_at = ?", time.Now().Add(-15*24*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	s := serviceFor(newOfflineEngine(t), reg)
	must(t, s.restore(ctx))
	if err := s.DeleteFile(ctx, ih, 0); err == nil || !strings.Contains(err.Error(), "подключите диск") {
		t.Fatalf("удаление файла с отключённого диска: %v", err)
	}
	must(t, s.expire(ctx))
	if idx, _ := reg.StoredFiles(ctx, ih); len(idx) != 0 {
		t.Fatalf("запись с отключённого диска старше срока хранения осталась: %v", idx)
	}
	must(t, s.restore(ctx))
	if p := problemText(t, db, "torrents.dirs"); p != "" {
		t.Fatalf("баннер остался: %q", p)
	}
}

// Запас места 0 — разрешён (хвост этапа 6: раньше 0 превращался в 20 ГБ); меньше 0 — по умолчанию.
func TestZeroReserveIsAllowed(t *testing.T) {
	s := newTestService(t)
	s.SetPolicy(Policy{MinFree: 0})
	if got := s.Policy().MinFree; got != 0 {
		t.Fatalf("запас 0 стал %d", got)
	}
	s.SetPolicy(Policy{MinFree: -1})
	if got := s.Policy().MinFree; got != 20<<30 {
		t.Fatalf("по умолчанию %d", got)
	}
}
