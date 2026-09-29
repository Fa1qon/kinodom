package torrents

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"go.etcd.io/bbolt"
)

// marksFile — файл отметок кусков anacrolix в папке состояния.
const marksFile = ".torrent.bolt.db"

// checkMarks проверяет файл отметок кусков до запуска движка. Повреждённый (питание пропало
// посреди записи: bolt в anacrolix пишет без fsync) откладывается в сторону как *.corrupt — движок
// создаст новый, а хранимые файлы перепроверятся по хэшам (спека, раздел 9). true — файл пересоздан.
func checkMarks(stateDir string) (recreated bool, err error) {
	p := filepath.Join(stateDir, marksFile)
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	rerr := readAllMarks(p)
	if rerr == nil {
		return false, nil
	}
	if errors.Is(rerr, bbolt.ErrTimeout) {
		return false, fmt.Errorf("файл отметок кусков %s занят — запущен второй Kinodom?", p)
	}
	bad := p + ".corrupt"
	os.Remove(bad)
	if err := os.Rename(p, bad); err != nil {
		return false, fmt.Errorf("файл отметок кусков повреждён (%v) и не убирается: %w", rerr, err)
	}
	return true, nil
}

// readAllMarks открывает bolt и читает все ключи. Паника bolt на повреждённой странице — тоже
// повреждение: здесь, в своей горутине, её можно перехватить (Tx.Check проверяет в отдельной
// горутине, и паника там уронила бы процесс).
func readAllMarks(p string) (err error) {
	// Страница, указывающая за пределы отображённого файла, — сбой доступа к памяти, а не паника:
	// без SetPanicOnFault recover его не ловит, и служба падала бы по кругу (ревью этапа 6).
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("файл отметок повреждён: %v", r)
		}
	}()
	db, err := bbolt.Open(p, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	return db.View(func(tx *bbolt.Tx) error {
		return tx.ForEach(func(_ []byte, b *bbolt.Bucket) error { return walkBucket(b) })
	})
}

func walkBucket(b *bbolt.Bucket) error {
	return b.ForEach(func(k, v []byte) error {
		if v == nil {
			if nb := b.Bucket(k); nb != nil {
				return walkBucket(nb)
			}
		}
		return nil
	})
}

// pieceRef — кусок раздачи, который надо перепроверить по хэшу.
type pieceRef struct {
	ih    metainfo.Hash
	index int
}

// queueVerify ставит в очередь перепроверки куски хранимых файлов раздачи (отметки пропали вместе
// с повреждённым файлом). Пока раздача перепроверяется, её хранимые файлы не качаются: иначе
// движок перекачал бы с пиров то, что уже лежит на диске. Вызывать под s.mu.
func (s *Service) queueVerify(ss *session) {
	t := ss.t
	files := t.Files()
	for i := range ss.storedFiles {
		files[i].SetPriority(torrent.PiecePriorityNone)
		for p := files[i].BeginPieceIndex(); p < files[i].EndPieceIndex(); p++ {
			if !ss.verifyQ[p] {
				ss.verifyQ[p] = true
				s.toVerify = append(s.toVerify, pieceRef{t.InfoHash(), p})
			}
		}
	}
}

// verifyDone — раздача перепроверена: её хранимые файлы снова качаются (кроме стоящих на паузе).
// Вызывать под s.mu.
func (ss *session) verifyDone() {
	files := ss.t.Files()
	for i := range ss.storedFiles {
		if !ss.paused[i] {
			files[i].SetPriority(torrent.PiecePriorityNormal)
		}
	}
}

// queuedBytes — сколько байт файла лежит в кусках, ждущих перепроверки: до неё они числятся
// нескачанными, хотя почти наверняка на диске. Вызывать под s.mu.
func (ss *session) queuedBytes(f *torrent.File) int64 {
	var n int64
	info := ss.t.Info()
	for i := f.BeginPieceIndex(); i < f.EndPieceIndex(); i++ {
		if !ss.verifyQ[i] {
			continue
		}
		p := info.Piece(i)
		n += max(0, min(p.Offset()+p.Length(), f.Offset()+f.Length())-max(p.Offset(), f.Offset()))
	}
	return n
}

// verifySome перепроверяет куски из очереди не дольше budget: Run зовёт его раз в секунду, и
// остальная работа цикла не встаёт. Раздача перепроверена целиком — её хранимые файлы снова
// качаются.
func (s *Service) verifySome(budget time.Duration) {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.toVerify) == 0 {
			s.mu.Unlock()
			return
		}
		ref := s.toVerify[0]
		s.toVerify = s.toVerify[1:]
		ss := s.sessions[ref.ih]
		if ss == nil || !ss.verifyQ[ref.index] {
			s.mu.Unlock()
			continue // раздачу убрали или кусок сняли с очереди (файл удалён)
		}
		s.verifyNow = ref // удаление и уборка не трогают этот кусок и не выгружают раздачу
		s.mu.Unlock()
		if err := ss.t.Piece(ref.index).VerifyData(); err != nil {
			s.log.Warn("кусок не перепроверился", "hash", ref.ih.HexString(), "piece", ref.index, "err", err)
		}
		s.mu.Lock()
		s.verifyNow = pieceRef{}
		delete(ss.verifyQ, ref.index)
		if len(ss.verifyQ) == 0 {
			ss.verifyDone()
		}
		s.mu.Unlock()
	}
}
