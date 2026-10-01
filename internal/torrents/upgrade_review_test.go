package torrents

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
)

// Уборка по расписанию (maintain → restore) во время проверки перенесённого не ставит проверенные куски в
// очередь снова: иначе проверка сезона на HDD дольше 5 минут не кончилась бы никогда (финальное ревью 11b-В).
func TestUpgradeMaintainDoesNotRequeueVerify(t *testing.T) {
	s := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	var v1 []epFile
	for i := 1; i <= 6; i++ {
		v1 = append(v1, epFile{"e0" + string(rune('0'+i)) + ".mkv", randBytes(300_000, uint64(i))})
	}
	mi1, src1 := makeVersion(t, "Сериал", 64<<10, v1)
	old := downloadVersion(t, s, mi1, src1)
	cancel()
	<-done // Run остановлен: проверка идёт только вручную
	mi2, _ := makeVersion(t, "Сериал", 64<<10, append(append([]epFile{}, v1...), epFile{"e07.mkv", randBytes(300_000, 7)}))
	ih, err := s.Upgrade(context.Background(), old, torrentBytes(t, mi2), []int{6}, noRekey)
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.sessions[ih].verifyQ)
	}
	n0 := count()
	for count() > n0/3 {
		s.verifySome(time.Millisecond)
	}
	n1 := count()
	// Прошло 5 минут — maintain зовёт restore.
	if err := s.restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	n2 := count()
	s.mu.Lock()
	prio := s.sessions[ih].t.Files()[6].Priority()
	s.mu.Unlock()
	t.Logf("в очереди проверки: после перехода %d, после части проверки %d, после уборки %d; приоритет новой серии %v (None=%v)", n0, n1, n2, prio, torrent.PiecePriorityNone)
	if n2 > n1 {
		t.Errorf("уборка вернула в очередь проверенные куски: %d -> %d", n1, n2)
	}
}

// Уборка (restore раз в 5 минут) посреди перехода не трогает его: не доводит переход сама, не добавляет новую
// версию в движок, пока файлы переносятся, — иначе гонка с переносом теряла скачанное (финальное ревью 11b-В).
func TestUpgradeRestoreDuringMoves(t *testing.T) {
	s := newTestService(t)
	runService(t, s)
	v1 := []epFile{{"e01.mkv", randBytes(200_000, 1)}, {"e02.mkv", randBytes(210_000, 2)}, {"e03.mkv", randBytes(205_000, 3)}}
	mi1, src1 := makeVersion(t, "Сериал", 64<<10, v1)
	old := downloadVersion(t, s, mi1, src1)
	mi2, _ := makeVersion(t, "Сериал", 64<<10, append(append([]epFile{}, v1...), epFile{"e04.mkv", randBytes(220_000, 4)}))
	var added, moved, once bool
	s.upgradeStop = func(phase string, rec upgradeRec) error {
		if phase != "db" || once {
			return nil
		}
		once = true
		if err := s.restore(context.Background()); err != nil {
			t.Error(err)
		}
		_, added = s.Engine().Client().Torrent(rec.New)
		_, err := os.Stat(rec.Moves[0].From)
		moved = err != nil
		return nil
	}
	ih, err := s.Upgrade(context.Background(), old, torrentBytes(t, mi2), []int{3}, noRekey)
	if err != nil {
		t.Fatal(err)
	}
	if added || moved {
		t.Fatalf("уборка посреди перехода: новая версия в движке %v, файлы перенесены %v", added, moved)
	}
	info2, _ := mi2.UnmarshalInfo()
	for i, f := range info2.UpvertedFiles()[:3] {
		b, err := os.ReadFile(enginePath(s.Engine().DownloadsDir(), &info2, ih, f))
		if err != nil || !bytes.Equal(b, v1[i].data) {
			t.Fatalf("серия %d после перехода: %v", i+1, err)
		}
	}
}

// Движок не создан (папки загрузок нет): подписка зовёт FetchInfo и Open — ошибка, а не паника под s.mu,
// после которой висели бы «Загрузки», «Состояние» и сам движок (финальное ревью 11b-В).
func TestNoEngineIsError(t *testing.T) {
	s := NewLazyService(func() (*Engine, error) { return nil, errors.New("диск не подключён") }, NewRegistry(newTestDB(t)), quiet(), nil)
	if _, err := s.FetchInfo(context.Background(), "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"); !errors.Is(err, ErrNoEngine) {
		t.Fatalf("FetchInfo без движка: %v", err)
	}
	if _, err := s.Open(context.Background(), Source{Magnet: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"}); !errors.Is(err, ErrNoEngine) {
		t.Fatalf("Open без движка: %v", err)
	}
	done := make(chan struct{})
	go func() { s.Engine(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("s.mu занят навсегда")
	}
}

// Мало места (меньше запаса): переход состоялся — ключи перенесены, файлы на месте, — а докачка новых серий
// не встала; это не ошибка перехода: подписка оповещает о новой серии (финальное ревью 11b-В).
func TestUpgradeLowSpaceAfterCommit(t *testing.T) {
	s := newTestService(t)
	runService(t, s)
	v1 := []epFile{{"e01.mkv", randBytes(200_000, 1)}, {"e02.mkv", randBytes(210_000, 2)}}
	mi1, src1 := makeVersion(t, "Сериал", 64<<10, v1)
	old := downloadVersion(t, s, mi1, src1)
	mi2, _ := makeVersion(t, "Сериал", 64<<10, append(append([]epFile{}, v1...), epFile{"e03.mkv", randBytes(220_000, 3)}))
	s.freeSpace = func(string) (int64, error) { return 5 << 30, nil } // 5 ГБ свободно — меньше запаса
	ih, err := s.Upgrade(context.Background(), old, torrentBytes(t, mi2), []int{2}, noRekey)
	if err != nil || ih != mi2.HashInfoBytes() {
		t.Fatalf("переход при нехватке места: %v %v", ih.HexString(), err)
	}
}
