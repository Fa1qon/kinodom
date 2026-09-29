package torrents

import (
	"cmp"
	"context"
	"math"
	"slices"
	"time"
)

// DownloadState — строка экрана «Загрузки».
type DownloadState string

const (
	DownloadWatching    DownloadState = "watching"    // смотрят (поток за последние 6 часов)
	DownloadDownloading DownloadState = "downloading" // в фокусе очереди
	DownloadQueued      DownloadState = "queued"      // ждёт очереди
	DownloadPaused      DownloadState = "paused"      // мало места или диск с папкой не подключён
	DownloadDone        DownloadState = "done"        // скачан
)

// deleteWarnDays — «удалится через N дн.» показывается, только когда до удаления по сроку
// осталось столько дней или меньше (спека этапа 7, раздел 5.5).
const deleteWarnDays = 3

// DownloadItem — хранимый файл для экрана «Загрузки».
type DownloadItem struct {
	Hash         string        `json:"hash"`
	Index        int           `json:"index"`
	File         string        `json:"file"` // имя файла без папок
	Size         int64         `json:"size"`
	Done         int64         `json:"done"`
	Percent      int           `json:"percent"`
	State        DownloadState `json:"state"`
	Readiness    Readiness     `json:"readiness"`
	Speed        int64         `json:"speed,omitempty"` // байт/с; только у файла в фокусе
	LastOpenedAt time.Time     `json:"lastOpenedAt"`
	DeleteInDays *int          `json:"deleteInDays"` // null — до удаления по сроку больше трёх дней
	CanDelete    bool          `json:"canDelete"`    // файл, который смотрят, удалить нельзя
}

// DownloadsView — экран «Загрузки».
type DownloadsView struct {
	Items     []DownloadItem `json:"items"`
	UsedBytes int64          `json:"usedBytes"` // сколько занимает скачанное
	FreeBytes int64          `json:"freeBytes"` // свободно на диске папки загрузок
	LowSpace  bool           `json:"lowSpace"`  // места меньше запаса: докачки на паузе
}

var stateOrder = []DownloadState{DownloadWatching, DownloadDownloading, DownloadQueued, DownloadPaused, DownloadDone}

// DiskInfo — место в папке загрузок для «Состояния».
type DiskInfo struct {
	UsedBytes    int64 `json:"usedBytes"` // скачанное Kinodom
	FreeBytes    int64 `json:"freeBytes"`
	TotalBytes   int64 `json:"totalBytes"`
	MinFreeBytes int64 `json:"minFreeBytes"`
	Low          bool  `json:"low"` // места меньше запаса: докачки на паузе
}

// Disk — место в папке загрузок: сколько занято скачанным, свободно и всего на диске.
func (s *Service) Disk(ctx context.Context) (DiskInfo, error) {
	v, err := s.Downloads(ctx)
	if err != nil {
		return DiskInfo{}, err
	}
	d := DiskInfo{UsedBytes: v.UsedBytes, FreeBytes: v.FreeBytes, MinFreeBytes: s.pol().MinFree, Low: v.LowSpace}
	if eng := s.Engine(); eng != nil {
		if total, err := s.totalSpace(eng.DownloadsDir()); err == nil {
			d.TotalBytes = total
		}
	}
	return d, nil
}

// Speeds — скорость загрузки и отдачи всех раздач вместе, байт/с.
func (s *Service) Speeds() (down, up int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.sessions {
		down += int64(ss.speed)
		up += int64(ss.upSpeed)
	}
	return down, up
}

// Downloads — хранимые файлы: сначала те, что смотрят и качаются, потом очередь и пауза, потом
// скачанное — недавно открытое первым.
func (s *Service) Downloads(ctx context.Context) (DownloadsView, error) {
	files, err := s.reg.StoredByAge(ctx)
	if err != nil {
		return DownloadsView{}, err
	}
	now, pol := s.now(), s.pol()
	out := DownloadsView{Items: []DownloadItem{}}
	s.mu.Lock()
	for _, f := range files {
		it := DownloadItem{Hash: f.InfoHash.HexString(), Index: f.Index, File: baseName(f.Path), Size: f.Size,
			LastOpenedAt: f.LastOpened, State: DownloadPaused, Readiness: ReadyWait}
		ss := s.sessions[f.InfoHash]
		watching := s.watching(ss, f.Index, f.LastStream)
		if ss != nil && ss.t.Info() != nil && f.Index < len(ss.t.Files()) {
			tf := ss.t.Files()[f.Index]
			it.Done = min(tf.BytesCompleted(), tf.Length())
			it.Readiness, _ = readinessLocked(ss, f.Index, len(playableFiles(allFiles(ss.t))) > 1)
			switch {
			case watching:
				it.State = DownloadWatching
			case it.Done >= tf.Length():
				it.State = DownloadDone
			case ss.paused[f.Index] && f.Index == ss.focus: // остальные на паузе просто ждут очереди
				it.State = DownloadPaused
			case f.Index == ss.focus:
				it.State, it.Speed = DownloadDownloading, int64(ss.speed)
			default:
				it.State = DownloadQueued
			}
		}
		it.Percent = bufferPercent(it.Done, it.Size)
		it.CanDelete = !watching
		if left := pol.KeepFor - now.Sub(f.LastOpened); !watching && left <= deleteWarnDays*24*time.Hour {
			days := max(0, int(math.Ceil(left.Hours()/24)))
			it.DeleteInDays = &days
		}
		out.UsedBytes += it.Done
		out.Items = append(out.Items, it)
	}
	s.mu.Unlock()
	slices.SortStableFunc(out.Items, func(a, b DownloadItem) int {
		return cmp.Or(cmp.Compare(slices.Index(stateOrder, a.State), slices.Index(stateOrder, b.State)),
			b.LastOpenedAt.Compare(a.LastOpenedAt))
	})
	if eng := s.Engine(); eng != nil {
		dir := eng.DownloadsDir()
		if short, free, err := s.queueShortfall(dir); err == nil {
			out.FreeBytes, out.LowSpace = free, short > 0
		}
	}
	return out, nil
}
