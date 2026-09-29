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
	// KeepBehind — сколько серий позади самой дальней просмотренной не удалять, когда места не
	// хватает на следующую (второй телевизор может отставать); меньше 0 — 1.
	KeepBehind int
}

const (
	defaultKeepFor    = 14 * 24 * time.Hour
	defaultMinFree    = 20 << 30
	defaultKeepBehind = 1
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
	if p.KeepBehind < 0 {
		p.KeepBehind = defaultKeepBehind
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

// shortfall — сколько байт не хватает на диске папки dir: свободно − недокачанный остаток того,
// что качается сейчас на этом диске, − extra должно быть не меньше запаса (спека, раздел 9).
// Не больше нуля — хватает.
func (s *Service) shortfall(dir string, extra int64) (short, free int64, err error) {
	return s.shortfallOf(dir, extra, false)
}

// queueShortfall — то же для всей очереди: сколько не хватает, чтобы докачать всё хранимое.
// Больше нуля — предупреждение «Мало места», но ничего не удаляется и не встаёт на паузу.
func (s *Service) queueShortfall(dir string) (short, free int64, err error) {
	return s.shortfallOf(dir, 0, true)
}

func (s *Service) shortfallOf(dir string, extra int64, queue bool) (short, free int64, err error) {
	free, err = s.freeSpace(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("свободное место на %s: %w", volumeOf(dir), err)
	}
	return s.pol().MinFree - (free - s.remaining(volumeOf(dir), queue) - extra), free, nil
}

// remaining — сколько ещё докачать на диске vol: файлу в фокусе каждой раздачи и файлам, которые
// смотрят, а с queue — и сериям в очереди. Серии в очереди места ещё не занимают: когда очередь
// дойдёт до серии, место проверит переход фокуса (решение заказчика, этап 7a).
func (s *Service) remaining(vol string, queue bool) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for ih, ss := range s.sessions {
		if ss.t.Info() == nil || volumeOf(s.eng.TorrentDir(ih)) != vol {
			continue
		}
		files := ss.t.Files()
		for i := range ss.storedFiles {
			if !queue && i != ss.focus && ss.readers[i] == 0 {
				continue // ждёт очереди
			}
			// Куски на перепроверке после повреждённого файла отметок — скачанные, пока не доказано
			// обратное: иначе уборка приняла бы всю медиатеку за недокачанную и удаляла фильмы.
			n += max(0, files[i].Length()-files[i].BytesCompleted()-ss.queuedBytes(files[i]))
		}
	}
	return n
}

// ensureSpace — хватит ли места на диске папки dir, если добавить extra байт: не хватает —
// сначала очистка (freeUp), потом отказ ErrLowSpace. Вызывать под s.spaceMu.
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

// freeUp удаляет файлы на диске папки dir, пока места не хватит (спека, раздел 9):
//   - сначала серии позади самой дальней просмотренной серии своей раздачи, от самой ранней: к ним
//     не возвращаются; последние Policy.KeepBehind перед ней остаются — второй телевизор может
//     отставать; правила 6 часов у них нет, не трогается только открытый поток (решение
//     заказчика, этап 7a);
//   - потом самые давно открытые файлы, кроме тех, что смотрят, и выбранных за последние 6 часов —
//     телевизор, может быть, ещё буферизует их (этап 6).
//
// Если даже удаление всех таких файлов не покроет нехватку short, не удаляет ничего — отказ без
// потерь.
func (s *Service) freeUp(ctx context.Context, dir string, extra, short int64) error {
	files, err := s.reg.StoredByAge(ctx)
	if err != nil {
		return err
	}
	vol := volumeOf(dir)
	var old []StoredFile
	var gain int64
	s.mu.Lock()
	behind := s.behindLocked(files, vol)
	isBehind := map[fileRef]bool{}
	for _, f := range behind {
		isBehind[fileRef{f.InfoHash, f.Index}] = true
		gain += f.Size
	}
	for _, f := range files {
		if volumeOf(f.Path) == vol && !isBehind[fileRef{f.InfoHash, f.Index}] &&
			!s.watching(s.sessions[f.InfoHash], f.Index, f.LastStream) && s.now().Sub(f.LastOpened) >= watchingFor {
			old = append(old, f)
			gain += f.Size
		}
	}
	s.mu.Unlock()
	if gain < short {
		return nil
	}
	for k, f := range append(behind, old...) {
		if short, _, err := s.shortfall(dir, extra); err != nil || short <= 0 {
			return err
		}
		var err error
		if k < len(behind) {
			err = s.deleteBehind(ctx, f.InfoHash, f.Index)
		} else {
			err = s.DeleteFile(ctx, f.InfoHash, f.Index)
		}
		switch {
		case err == nil && k < len(behind):
			s.log.Info("места мало — удалена просмотренная серия", "path", f.Path)
		case err == nil:
			s.log.Info("места мало — удалён давно открытый файл", "path", f.Path, "opened", f.LastOpened)
		case errors.Is(err, ErrWatching), errors.Is(err, ErrNotStored), errors.Is(err, errDirMissing):
		default:
			s.log.Warn("файл не удалился при очистке", "path", f.Path, "err", err)
		}
	}
	return nil
}

// fileRef — файл раздачи.
type fileRef struct {
	ih    metainfo.Hash
	index int
}

// behindLocked — хранимые файлы на диске vol позади самой дальней просмотренной серии своей
// раздачи (по порядку имён), кроме последних Policy.KeepBehind перед ней и файлов с открытым
// потоком. Раздачи, которые смотрели давнее, — первыми, внутри раздачи — от самой ранней серии.
// Вызывать под s.mu.
func (s *Service) behindLocked(files []StoredFile, vol string) []StoredFile {
	byTorrent := map[metainfo.Hash][]StoredFile{}
	for _, f := range files {
		if volumeOf(f.Path) == vol {
			byTorrent[f.InfoHash] = append(byTorrent[f.InfoHash], f)
		}
	}
	type series struct {
		watched time.Time // когда смотрели самую дальнюю серию
		files   []StoredFile
	}
	var all []series
	for ih, fs := range byTorrent {
		ss := s.sessions[ih]
		if ss == nil || ss.t.Info() == nil {
			continue
		}
		tf := ss.t.Files()
		slices.SortFunc(fs, func(a, b StoredFile) int {
			switch pa, pb := tf[a.Index].DisplayPath(), tf[b.Index].DisplayPath(); {
			case naturalLess(pa, pb):
				return -1
			case naturalLess(pb, pa):
				return 1
			}
			return a.Index - b.Index
		})
		far := -1
		for k, f := range fs {
			if !f.LastStream.IsZero() {
				far = k
			}
		}
		if far < 0 {
			continue // серии ещё не смотрели — позади ничего нет
		}
		var cands []StoredFile
		for _, f := range fs[:max(0, far-s.policy.KeepBehind)] {
			if ss.readers[f.Index] == 0 {
				cands = append(cands, f)
			}
		}
		if len(cands) > 0 {
			all = append(all, series{fs[far].LastStream, cands})
		}
	}
	slices.SortFunc(all, func(a, b series) int { return a.watched.Compare(b.watched) })
	var out []StoredFile
	for _, sr := range all {
		out = append(out, sr.files...)
	}
	return out
}

// checkSpace — пока идут докачки, раз в 5 минут и при переходе очереди к следующему файлу: то,
// что качается сейчас, не влезает в запас — очистка (freeUp); не помогло — фоновые докачки на этом
// диске на паузе (потоки не трогаются); место появилось — докачки продолжаются (спека, раздел 9).
// Не влезает вся очередь — проблема «Мало места», пока лишнее не удалят (решение заказчика, этап 7a).
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
		if qshort, _, err := s.queueShortfall(dir); short > 0 || (err == nil && qshort > 0) {
			low = append(low, fmt.Sprintf("на диске %s свободно %s", volumeOf(dir), gb(free)))
		}
	}
	if len(low) == 0 {
		s.reg.clearProblem(ctx, "torrents.space")
		return nil
	}
	// Коротко: подробности заказчик в интерфейсе видеть не хочет (спека этапа 7, раздел 2).
	s.reg.setProblem(ctx, "torrents.space", "Мало места в папке загрузок: "+strings.Join(low, ", ")+" — удалите лишнее в «Загрузках»")
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
