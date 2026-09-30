package tray

import "testing"

// Значок для трея — картинка из kinodom.ico ближайшего размера не меньше нужного (16 px при 100 %,
// 24 — при 150 %, 32 — при 200 %); больше 256 нет.
func TestPickIcon(t *testing.T) {
	cases := map[int]int{16: 16, 20: 24, 24: 24, 32: 32, 40: 48, 300: 256}
	for want, got := range cases {
		off, n, size, err := pickIcon(Icon, want)
		if err != nil || size != got || n == 0 || off+n > len(Icon) {
			t.Errorf("%d px: картинка %d px (%d байт с %d), %v", want, size, n, off, err)
		}
	}
	if _, _, _, err := pickIcon([]byte("не ico"), 16); err == nil {
		t.Fatal("мусор принят за .ico")
	}
}

// Значок создаётся из встроенного файла (Windows, без окна).
func TestLoadIcon(t *testing.T) {
	h, err := loadIcon(Icon, 16)
	if err != nil || h == 0 {
		t.Fatalf("значок: %v", err)
	}
	destroyIcon(h)
}

// При входе в Windows проводник может ещё не принимать значки: добавление повторяется; не вышло —
// значок завершается и не держит «один значок на сеанс» (второе ревью, мелочь 4).
func TestRetryAdd(t *testing.T) {
	calls := 0
	ok := retry(func() bool { calls++; return calls == 3 }, 5, 0)
	if !ok || calls != 3 {
		t.Fatalf("успех с третьей попытки: %v, попыток %d", ok, calls)
	}
	calls = 0
	if retry(func() bool { calls++; return false }, 4, 0) || calls != 4 {
		t.Fatalf("всегда отказ: попыток %d", calls)
	}
}
