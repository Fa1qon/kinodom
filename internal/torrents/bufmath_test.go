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
		name            string
		remaining, size int64
		bitrate, speed  float64
		want            int
	}{
		{"всё скачано", 0, 7200, 1, 0, 0},
		{"скорость нулевая — оценить нельзя", 7200, 7200, 1, 0, -1},
		{"качаем со скоростью показа", 7200, 7200, 1, 1, 0},
		{"вдвое медленнее показа", 7200, 7200, 1, 0.5, 7200}, // 14400 с на скачивание − 7200 с фильма
		{"вчетверо медленнее", 7200, 7200, 1, 0.25, 21600},   // 28800 − 7200
		{"быстрее показа", 7200, 7200, 1, 2, 0},
		{"половина уже скачана", 3600, 7200, 1, 0.5, 0}, // 7200 − 7200
	}
	for _, c := range cases {
		if got := smoothInSec(c.remaining, c.size, c.bitrate, c.speed); got != c.want {
			t.Errorf("%s: smoothInSec = %d, ожидалось %d", c.name, got, c.want)
		}
	}
}

// Проверка формулы симуляцией, а не на глаз: подождав обещанное время, фильм досматривается
// без единой остановки, а подождав на 5 % меньше — останавливается. Модель: файл качается
// по порядку с постоянной скоростью, показ идёт с постоянным битрейтом.
func TestSmoothInSecBySimulation(t *testing.T) {
	type film struct {
		size, done     float64 // байт всего и уже скачано
		bitrate, speed float64 // байт/с
	}
	films := []film{
		{4000e6, 0, 4000e6 / 7200, 4000e6 / 7200 / 2},        // фильм 4 ГБ, скорость — половина битрейта
		{4000e6, 0, 4000e6 / 7200, 4000e6 / 7200 / 4},        // четверть битрейта
		{4000e6, 1500e6, 4000e6 / 7200, 4000e6 / 7200 * 0.7}, // часть уже скачана
		{900e6, 0, 900e6 / 2700, 900e6 / 2700 * 0.9},         // серия, почти хватает скорости
	}
	// stalls — остановится ли показ, если начать смотреть через wait секунд.
	stalls := func(f film, wait float64) bool {
		duration := f.size / f.bitrate
		for tt := 0.0; tt <= duration; tt += 1 {
			downloaded := min(f.size, f.done+f.speed*(wait+tt))
			if downloaded+1 < f.bitrate*tt {
				return true
			}
		}
		return false
	}
	for i, f := range films {
		w := smoothInSec(int64(f.size-f.done), int64(f.size), f.bitrate, f.speed)
		if w < 0 {
			t.Fatalf("фильм %d: оценка не получена", i)
		}
		if stalls(f, float64(w)) {
			t.Errorf("фильм %d: подождали обещанные %d с, а показ остановился", i, w)
		}
		if w > 60 && !stalls(f, float64(w)*0.95) {
			t.Errorf("фильм %d: обещание %d с завышено — хватило бы и на 5 %% меньше", i, w)
		}
	}
}

func TestBufferPercent(t *testing.T) {
	if bufferPercent(0, 0) != 100 || bufferPercent(50, 200) != 25 || bufferPercent(200, 200) != 100 {
		t.Fatal("неверный процент буфера")
	}
}
