package torrents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/torrents/torrenttest"
)

// epFile — файл версии раздачи: путь внутри раздачи и содержимое.
type epFile struct {
	path string
	data []byte
}

func randBytes(n int, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

// makeVersion — версия раздачи name из файлов (папка сидера — своя).
func makeVersion(t *testing.T, name string, pieceLen int64, files []epFile) (metainfo.MetaInfo, string) {
	t.Helper()
	dir := t.TempDir()
	var fs []torrenttest.File
	for _, f := range files {
		fs = append(fs, torrenttest.File{Path: f.path, Size: len(f.data), Data: f.data})
	}
	mi, _ := torrenttest.MakeTorrent(t, dir, name, pieceLen, fs...)
	return mi, dir
}

// downloadVersion — сервис скачал версию целиком от её сидера; сидер больше не подключён.
func downloadVersion(t *testing.T, s *Service, mi metainfo.MetaInfo, src string) metainfo.Hash {
	t.Helper()
	seeder, _ := torrenttest.NewSeeder(t, src, mi)
	ih, err := s.Open(context.Background(), Source{Torrent: torrentBytes(t, mi)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Download(context.Background(), ih, nil); err != nil {
		t.Fatal(err)
	}
	connect(t, s, ih, seeder)
	tt, _ := s.Engine().Client().Torrent(ih)
	for _, f := range tt.Files() {
		waitComplete(t, f)
	}
	seeder.Close()
	return ih
}

// verified — перепроверка перенесённого закончилась.
func verified(s *Service, ih metainfo.Hash) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[ih]
	return ss != nil && len(ss.verifyQ) == 0
}

func noRekey(context.Context, *sql.Tx, metainfo.Hash, metainfo.Hash, map[int]int) error { return nil }

// Переход на обновлённую раздачу (спека 11b, 6.3; сценарии опыта, исследование 22.1): скачанное
// переносится и проверяется, докачивается только новое.
func TestUpgradeScenarios(t *testing.T) {
	const piece = 64 << 10
	eps := make([][]byte, 9)
	for i := range eps {
		eps[i] = randBytes(250_000+i*17_000, uint64(i+1))
	}
	v1 := []epFile{{"e01.mkv", eps[1]}, {"e02.mkv", eps[2]}, {"e03.mkv", eps[3]}, {"e04.mkv", eps[4]}, {"e05.mkv", eps[5]}, {"e06.mkv", eps[6]}}
	cases := []struct {
		name     string
		newName  string
		newPiece int64
		files    []epFile
		newBytes int // сколько байт новых и перезалитых файлов (докачать только их)
	}{
		{"1: серия в конце", "Сериал", piece, append(append([]epFile{}, v1...), epFile{"e07.mkv", eps[7]}), len(eps[7])},
		{"2: другой кусок и корень", "Сериал S01", 2 * piece, append(append([]epFile{}, v1...), epFile{"e07.mkv", eps[7]}), len(eps[7])},
		{"3а: новый файл в начале", "Сериал", piece, append([]epFile{{"a00.mkv", eps[0]}}, v1...), len(eps[0])},
		{"3б: новый файл в середине", "Сериал", piece, append(append(append([]epFile{}, v1[:3]...), epFile{"e03b.mkv", eps[8]}), v1[3:]...), len(eps[8])},
		{"3в: все переименованы", "Сериал", piece, []epFile{{"s01e01.mkv", eps[1]}, {"s01e02.mkv", eps[2]}, {"s01e03.mkv", eps[3]},
			{"s01e04.mkv", eps[4]}, {"s01e05.mkv", eps[5]}, {"s01e06.mkv", eps[6]}, {"s01e07.mkv", eps[7]}}, len(eps[7])},
		{"4б: серия перезалита другого размера", "Сериал", piece, append(append(append([]epFile{}, v1[:5]...),
			epFile{"e06.mkv", randBytes(len(eps[6])+3000, 99)}), epFile{"e07.mkv", eps[7]}), len(eps[6]) + 3000 + len(eps[7])},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestService(t)
			runService(t, s)
			mi1, src1 := makeVersion(t, "Сериал", piece, v1)
			old := downloadVersion(t, s, mi1, src1)
			oldInfo, _ := mi1.UnmarshalInfo()
			oldRoot := torrentDir(s.Engine().DownloadsDir(), &oldInfo, old)
			mi2, src2 := makeVersion(t, c.newName, c.newPiece, c.files)
			info2, _ := mi2.UnmarshalInfo()
			var download []int // новые файлы — по имени, которого не было
			known := map[string]bool{}
			for _, f := range v1 {
				known[f.path] = true
			}
			for i, f := range info2.UpvertedFiles() {
				if !known[f.BestPath()[0]] {
					download = append(download, i)
				}
			}
			ih, err := s.Upgrade(context.Background(), old, torrentBytes(t, mi2), download, noRekey)
			if err != nil {
				t.Fatal(err)
			}
			if ih != mi2.HashInfoBytes() {
				t.Fatalf("новая раздача %s", ih.HexString())
			}
			waitFor(t, "перепроверка перенесённого", func() bool { return verified(s, ih) })
			if _, err := os.Stat(oldRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("папка прежней версии осталась: %v", err)
			}
			if _, ok := s.Engine().Client().Torrent(old); ok {
				t.Fatal("прежняя версия осталась в движке")
			}
			seeder, _ := torrenttest.NewSeeder(t, src2, mi2)
			connect(t, s, ih, seeder)
			tt, _ := s.Engine().Client().Torrent(ih)
			for _, f := range tt.Files() {
				waitComplete(t, f)
			}
			st := tt.Stats()
			got := st.BytesReadUsefulData.Int64()
			if limit := int64(c.newBytes) + 3*c.newPiece; got > limit {
				t.Fatalf("докачано %d байт — больше нового (%d) и стыков (%d)", got, c.newBytes, limit)
			}
			if ups, err := s.reg.upgrades(context.Background()); err != nil || len(ups) != 0 {
				t.Fatalf("пометка перехода осталась: %+v %v", ups, err)
			}
		})
	}
}

// Сопоставление файлов: по пути внутри раздачи (без корня), запасной путь — единственный размер.
func TestMatchFiles(t *testing.T) {
	info := func(name string, files map[string]int64) *metainfo.Info {
		in := &metainfo.Info{Name: name}
		for _, p := range []string{"a.mkv", "b.mkv", "c.mkv", "d.mkv", "x.mkv", "y.mkv"} {
			if n, ok := files[p]; ok {
				in.Files = append(in.Files, metainfo.FileInfo{Path: []string{p}, Length: n})
			}
		}
		return in
	}
	old := info("Сериал", map[string]int64{"a.mkv": 10, "b.mkv": 20, "c.mkv": 30})
	new := info("Сериал 2", map[string]int64{"a.mkv": 10, "b.mkv": 25, "x.mkv": 30, "y.mkv": 40})
	got := matchFiles(old, new)
	want := map[int]int{0: 0, 1: 1, 2: 2} // a — по пути; b — по пути (другой размер: перекачать); c → x — единственный размер 30
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("сопоставление %v, нужно %v", got, want)
	}
}

// Переход откладывается, пока раздачу смотрят (поток открыт или был меньше 2 минут назад — VLC
// переподключается); поток не обрывается (спека 11b, 6.3.2; Review Focus 2).
func TestUpgradeWaitsForViewers(t *testing.T) {
	s := newTestService(t)
	clk := &testClock{t: time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)}
	s.now = clk.now
	runService(t, s)
	v1 := []epFile{{"e01.mkv", randBytes(200_000, 1)}, {"e02.mkv", randBytes(210_000, 2)}}
	mi1, src1 := makeVersion(t, "Сериал", 64<<10, v1)
	old := downloadVersion(t, s, mi1, src1)
	mi2, _ := makeVersion(t, "Сериал", 64<<10, append(append([]epFile{}, v1...), epFile{"e03.mkv", randBytes(220_000, 3)}))
	release := s.openReader(old, 0)
	if _, err := s.Upgrade(context.Background(), old, torrentBytes(t, mi2), []int{2}, noRekey); !errors.Is(err, ErrBusy) {
		t.Fatalf("поток открыт — нужна ErrBusy: %v", err)
	}
	if err := s.reg.TouchStream(context.Background(), old, 0, clk.now()); err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := s.Upgrade(context.Background(), old, torrentBytes(t, mi2), []int{2}, noRekey); !errors.Is(err, ErrBusy) {
		t.Fatalf("поток был только что — нужна ErrBusy: %v", err)
	}
	if _, ok := s.Engine().Client().Torrent(old); !ok {
		t.Fatal("отложенный переход выгрузил раздачу")
	}
	clk.add(3 * time.Minute)
	if _, err := s.Upgrade(context.Background(), old, torrentBytes(t, mi2), []int{2}, noRekey); err != nil {
		t.Fatalf("никто не смотрит — переход: %v", err)
	}
}

// Сбой посреди перехода (после транзакции; на середине переноса; до проверки) — при старте переход
// доводится: файлы на месте, перенесённое проверяется заново, ничего не качается целиком (Review Focus 1).
func TestUpgradeResumesAfterCrash(t *testing.T) {
	for _, stopAt := range []string{"db", "half", "moved"} {
		t.Run(stopAt, func(t *testing.T) {
			db := newTestDB(t)
			state, dl := t.TempDir(), t.TempDir()
			e1, err := NewEngine(Config{DownloadsDir: dl, StateDir: state, Offline: true, Log: quiet()})
			if err != nil {
				t.Fatal(err)
			}
			s1 := serviceFor(e1, NewRegistry(db))
			ctx1, cancel1 := context.WithCancel(context.Background())
			done1 := make(chan struct{})
			go func() { s1.Run(ctx1); close(done1) }()
			v1 := []epFile{{"e01.mkv", randBytes(200_000, 1)}, {"e02.mkv", randBytes(210_000, 2)}, {"e03.mkv", randBytes(205_000, 3)}}
			mi1, src1 := makeVersion(t, "Сериал", 64<<10, v1)
			old := downloadVersion(t, s1, mi1, src1)
			mi2, _ := makeVersion(t, "Сериал", 64<<10, append(append([]epFile{}, v1...), epFile{"e04.mkv", randBytes(220_000, 4)}))
			s1.upgradeStop = func(phase string, rec upgradeRec) error {
				if stopAt == "half" && phase == "db" {
					m := rec.Moves[0] // перенесли один файл и «пропало питание»
					os.MkdirAll(filepath.Dir(m.To), 0o755)
					if err := os.Rename(m.From, m.To); err != nil {
						t.Fatal(err)
					}
					return errors.New("сбой")
				}
				if phase == stopAt {
					return errors.New("сбой")
				}
				return nil
			}
			if _, err := s1.Upgrade(context.Background(), old, torrentBytes(t, mi2), []int{3}, noRekey); err == nil {
				t.Fatal("сбой не случился")
			}
			cancel1()
			<-done1
			e1.Close()
			e2, err := NewEngine(Config{DownloadsDir: dl, StateDir: state, Offline: true, Log: quiet()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { e2.Close() })
			s2 := serviceFor(e2, NewRegistry(db))
			runService(t, s2)
			ih := mi2.HashInfoBytes()
			waitFor(t, "переход доведён и перепроверен", func() bool { return verified(s2, ih) })
			tt, ok := e2.Client().Torrent(ih)
			if !ok {
				t.Fatal("новой версии нет в движке")
			}
			for i, f := range tt.Files()[:3] {
				if f.BytesCompleted() < f.Length()-2*(64<<10) {
					t.Fatalf("серия %d после сбоя не на месте: %d из %d", i+1, f.BytesCompleted(), f.Length())
				}
			}
			if ups, _ := s2.reg.upgrades(context.Background()); len(ups) != 0 {
				t.Fatalf("пометка перехода осталась: %+v", ups)
			}
		})
	}
}
