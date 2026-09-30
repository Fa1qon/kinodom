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
