package torrents

import "testing"

func TestEstimateBitrate(t *testing.T) {
	if got := estimateBitrate(7200*1000, false); got != 1000 {
		t.Errorf("фильм: %v байт/с, ожидалось 1000", got)
	}
	if got := estimateBitrate(2700*1000, true); got != 1000 {
		t.Errorf("серия: %v байт/с, ожидалось 1000", got)
	}
}

func TestHeadTailBytes(t *testing.T) {
	size := int64(40 << 30) // ремукс на 40 ГБ
	br := estimateBitrate(size, false)
	if h, tl := headTailBytes(size, br); h != int64(headSeconds*br) || tl != 4<<20 {
		t.Errorf("ремукс: начало %d, конец %d", h, tl)
	}
	small := int64(1 << 30) // 1 ГБ за 2 ч — 30 с это ~4 МБ, берём минимум 8 МБ
	if h, _ := headTailBytes(small, estimateBitrate(small, false)); h != 8<<20 {
		t.Errorf("минимум начала: %d", h)
	}
	if h, tl := headTailBytes(10<<20, 1000); h != 10<<20 || tl != 0 {
		t.Errorf("маленький файл целиком: начало %d, конец %d", h, tl)
	}
}

func TestSpanFor(t *testing.T) {
	cases := []struct {
		off, n int64
		want   pieceSpan
	}{
		{0, 1, pieceSpan{0, 1}},
		{0, 100, pieceSpan{0, 1}},
		{0, 101, pieceSpan{0, 2}},
		{250, 100, pieceSpan{2, 4}},
		{10, 0, pieceSpan{}},
	}
	for _, c := range cases {
		if got := spanFor(100, c.off, c.n); got != c.want {
			t.Errorf("spanFor(100, %d, %d) = %v, ожидалось %v", c.off, c.n, got, c.want)
		}
	}
}

func TestSmoothInSec(t *testing.T) {
	cases := []struct {
		remaining      int64
		bitrate, speed float64
		want           int
	}{
		{1000, 100, 100, 0}, // качаем со скоростью показа
		{1000, 100, 150, 0},
		{1000, 100, 50, 5}, // 10 с показа × (1 − 0,5)
		{1000, 100, 0, 10},
		{1000, 0, 0, 0},
	}
	for _, c := range cases {
		if got := smoothInSec(c.remaining, c.bitrate, c.speed); got != c.want {
			t.Errorf("smoothInSec(%d, %v, %v) = %d, ожидалось %d", c.remaining, c.bitrate, c.speed, got, c.want)
		}
	}
}

func TestBufferPercent(t *testing.T) {
	if bufferPercent(0, 0) != 100 || bufferPercent(50, 200) != 25 || bufferPercent(200, 200) != 100 {
		t.Fatal("неверный процент буфера")
	}
}
