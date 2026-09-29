package torrents

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

// ErrLowSpace — места не хватит даже после очистки (спека, раздел 9).
var ErrLowSpace = errors.New("мало места на диске")

// Policy — правила хранения из настроек (спека, раздел 15).
type Policy struct {
	KeepFor    time.Duration // хранить после последнего открытия; 0 — 14 дней
	MinFree    int64         // минимум свободного места, байт; 0 — без запаса; меньше 0 — 20 ГБ
	MaxSeeding int           // раздавать не больше стольких раздач; 0 — 10
}

const (
	defaultKeepFor = 14 * 24 * time.Hour
	defaultMinFree = 20 << 30
)

// SetPolicy задаёт правила хранения — и до Run, и на ходу (настройки из пульта, этап 7).
func (s *Service) SetPolicy(p Policy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.KeepFor <= 0 {
		p.KeepFor = defaultKeepFor
	}
	if p.MinFree < 0 {
		p.MinFree = defaultMinFree
	}
	if p.MaxSeeding <= 0 {
		p.MaxSeeding = defaultMaxSeeding
	}
	s.policy = p
}

// Policy — действующие правила хранения.
func (s *Service) Policy() Policy { return s.pol() }

func (s *Service) pol() Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policy
}

// volumeOf — диск папки: место считается по диску.
func volumeOf(dir string) string { return strings.ToUpper(filepath.VolumeName(dir)) }

// shortfall — сколько байт не хватает на диске папки dir: свободно − недокачанный остаток
// всех докачек на этом диске − extra должно быть не меньше запаса (спека, раздел 9).
// Не больше нуля — хватает.
func (s *Service) shortfall(dir string, extra int64) (short, free int64, err error) {
	free, err = s.freeSpace(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("свободное место на %s: %w", volumeOf(dir), err)
	}
	return s.pol().MinFree - (free - s.remaining(volumeOf(dir)) - extra), free, nil
}

// remaining — сколько ещё докачать хранимым файлам на диске vol.
func (s *Service) remaining(vol string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for ih, ss := range s.sessions {
		if ss.t.Info() == nil || volumeOf(s.eng.TorrentDir(ih)) != vol {
			continue
		}
		files := ss.t.Files()
		for i := range ss.storedFiles {
			// Куски на перепроверке после повреждённого файла отметок — скачанные, пока не доказано
			// обратное: иначе уборка приняла бы всю медиатеку за недокачанную и удаляла фильмы.
			n += max(0, files[i].Length()-files[i].BytesCompleted()-ss.queuedBytes(files[i]))
		}
	}
	return n
}

// ensureSpace — хватит ли места на диске папки dir, если добавить extra байт: не хватает —
// сначала очистка самых давно открытых файлов, потом отказ ErrLowSpace. Вызывать под s.spaceMu.
func (s *Service) ensureSpace(ctx context.Context, dir string, extra int64) error {
	short, free, err := s.shortfall(dir, extra)
	if err != nil || short <= 0 {
		return err
	}
	if err := s.freeUp(ctx, dir, extra, short); err != nil {
		return err
	}
	if short, free, err = s.shortfall(dir, extra); err != nil || short <= 0 {
		return err
	}
	return fmt.Errorf("%w %s: свободно %s, а докачкам и этому файлу нужно ещё %s сверх запаса %s — удалите лишнее с диска",
		ErrLowSpace, volumeOf(dir), gb(free), gb(short), gb(s.pol().MinFree))
}

// freeUp удаляет самые давно открытые файлы на диске папки dir, пока места не хватит. Не трогает
// файлы, которые смотрят, и выбранные за последние 6 часов — телевизор, может быть, ещё
// буферизует их; если даже удаление всех остальных не покроет нехватку short, не удаляет ничего —
// отказ без потерь (спека, раздел 9).
func (s *Service) freeUp(ctx context.Context, dir string, extra, short int64) error {
	files, err := s.reg.StoredByAge(ctx)
	if err != nil {
		return err
	}
	vol := volumeOf(dir)
	var cands []StoredFile
	var gain int64
	s.mu.Lock()
	for _, f := range files {
		if volumeOf(f.Path) == vol && !s.watching(s.sessions[f.InfoHash], f.Index, f.LastStream) &&
			s.now().Sub(f.LastOpened) >= watchingFor {
			cands = append(cands, f)
			gain += f.Size
		}
	}
	s.mu.Unlock()
	if gain < short {
		return nil
	}
	for _, f := range cands {
		if short, _, err := s.shortfall(dir, extra); err != nil || short <= 0 {
			return err
		}
		switch err := s.DeleteFile(ctx, f.InfoHash, f.Index); {
		case err == nil:
			s.log.Info("места мало — удалён давно открытый файл", "path", f.Path, "opened", f.LastOpened)
		case errors.Is(err, ErrWatching), errors.Is(err, ErrNotStored), errors.Is(err, errDirMissing):
		default:
			s.log.Warn("файл не удалился при очистке", "path", f.Path, "err", err)
		}
	}
	return nil
}

// checkSpace — пока идут докачки, раз в 5 минут: ниже запаса — очистка самых давно открытых;
// не помогло — фоновые докачки на этом диске на паузе (потоки не трогаются) и проблема «Мало
// места на диске»; место появилось — докачки продолжаются (спека, раздел 9).
func (s *Service) checkSpace(ctx context.Context) error {
	s.spaceMu.Lock()
	defer s.spaceMu.Unlock()
	var low []string
	for _, dir := range s.downloadDirs() {
		short, free, err := s.shortfall(dir, 0)
		if err != nil {
			s.log.Warn("не удалось узнать свободное место", "dir", dir, "err", err)
			continue
		}
		if short > 0 {
			if err := s.freeUp(ctx, dir, 0, short); err != nil {
				return err
			}
			if short, free, err = s.shortfall(dir, 0); err != nil {
				continue
			}
		}
		s.pauseDownloads(volumeOf(dir), short > 0)
		if short > 0 {
			low = append(low, fmt.Sprintf("%s (свободно %s)", volumeOf(dir), gb(free)))
		}
	}
	if len(low) == 0 {
		s.reg.clearProblem(ctx, "torrents.space")
		return nil
	}
	s.reg.setProblem(ctx, "torrents.space", "Мало места на диске "+strings.Join(low, ", ")+
		" при запасе "+gb(s.pol().MinFree)+": докачки на паузе, новые фильмы не откроются. Удалите лишнее с диска или уменьшите запас в настройках")
	return nil
}

// downloadDirs — по одной папке на каждый диск, где есть недокачанные хранимые файлы или
// докачки стоят на паузе.
func (s *Service) downloadDirs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	byVol := map[string]string{}
	for ih, ss := range s.sessions {
		if ss.t.Info() == nil {
			continue
		}
		dir := s.eng.TorrentDir(ih)
		files := ss.t.Files()
		for i := range ss.storedFiles {
			if files[i].BytesCompleted() < files[i].Length() || ss.paused[i] {
				byVol[volumeOf(dir)] = dir
			}
		}
	}
	dirs := make([]string, 0, len(byVol))
	for _, d := range byVol {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)
	return dirs
}

// pauseDownloads ставит на паузу (или продолжает) докачку хранимых файлов на диске vol. Файлы,
// которые сейчас смотрят, не трогает: их поток тянет свои куски сам.
func (s *Service) pauseDownloads(vol string, pause bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ih, ss := range s.sessions {
		if ss.t.Info() == nil || volumeOf(s.eng.TorrentDir(ih)) != vol {
			continue
		}
		files := ss.t.Files()
		changed := false
		for i := range ss.storedFiles {
			switch {
			case pause && !ss.paused[i] && ss.readers[i] == 0 && files[i].BytesCompleted() < files[i].Length():
				ss.paused[i], changed = true, true
			case !pause && ss.paused[i]:
				delete(ss.paused, i)
				changed = true
			}
		}
		if changed {
			s.applyLocked(ss)
		}
	}
}

// fileNeed — сколько байт займёт файл, если выбрать его для просмотра: у уже хранимого — 0
// (он уже в остатке докачек). done — файл уже выбран. Ошибки — те же, что у Prepare.
func (s *Service) fileNeed(ih metainfo.Hash, index int) (dir string, need int64, done bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[ih]
	if !ok {
		return "", 0, false, ErrNotOpen
	}
	if ss.t.Info() == nil {
		return "", 0, false, ErrNoInfo
	}
	files := ss.t.Files()
	if index < 0 || index >= len(files) {
		return "", 0, false, ErrNoSuchFile
	}
	if _, ok := ss.prepared[index]; ok {
		return "", 0, true, nil
	}
	if !ss.storedFiles[index] {
		need = files[index].Length() - files[index].BytesCompleted()
	}
	return s.eng.TorrentDir(ih), need, false, nil
}

// gb — размер в гигабайтах для человека: «12,3 ГБ».
func gb(n int64) string {
	return strings.Replace(strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64), ".", ",", 1) + " ГБ"
}
