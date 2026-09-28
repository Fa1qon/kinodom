package torrents

import (
	"math"
	"time"
)

const (
	minHead     = 8 << 20 // начало файла — не меньше 8 МБ
	tailLen     = 4 << 20 // конец файла: индекс MKV/AVI, moov в MP4 — без него нет перемотки
	headSeconds = 30      // начало — 30 секунд показа
)

// estimateBitrate — байт в секунду: размер ÷ 2 ч для фильма, ÷ 45 мин для серии.
// Точнее без разбора контейнера не узнать, а для решения «хватит ли скорости» этого достаточно.
func estimateBitrate(size int64, episode bool) float64 {
	d := 2 * time.Hour
	if episode {
		d = 45 * time.Minute
	}
	return float64(size) / d.Seconds()
}

// headTailBytes — сколько байт начала и конца файла нужно до старта просмотра.
func headTailBytes(size int64, bitrate float64) (head, tail int64) {
	head = max(int64(minHead), int64(headSeconds*bitrate))
	tail = int64(tailLen)
	if head+tail >= size {
		return size, 0 // маленький файл — целиком
	}
	return head, tail
}

// pieceSpan — куски [begin, end).
type pieceSpan struct{ begin, end int }

// spanFor — куски, покрывающие байты [off, off+n) раздачи.
func spanFor(pieceLen, off, n int64) pieceSpan {
	if n <= 0 {
		return pieceSpan{}
	}
	return pieceSpan{int(off / pieceLen), int((off+n-1)/pieceLen) + 1}
}

func bufferPercent(done, total int64) int {
	if total <= 0 {
		return 100
	}
	return int(done * 100 / total)
}

// smoothInSec — через сколько секунд можно смотреть без остановок (спека, раздел 9):
// длительность оставшегося × (1 − скорость/битрейт); 0, если качаем не медленнее показа.
func smoothInSec(remaining int64, bitrate, speed float64) int {
	if bitrate <= 0 || speed >= bitrate {
		return 0
	}
	return int(math.Ceil(float64(remaining) / bitrate * (1 - speed/bitrate)))
}
