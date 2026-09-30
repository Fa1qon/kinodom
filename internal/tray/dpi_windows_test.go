package tray

import "testing"

// Значок трея по масштабу экрана (хвост Х38): процесс объявляет поддержку масштаба — размер значка от
// системы соответствует её DPI (16 px при 100 %, 24 px при 150 %), а не 16 px, растянутые Windows.
func TestDPIAwareIconSize(t *testing.T) {
	dpiAware()
	dpi, _, _ := user32.NewProc("GetDpiForSystem").Call()
	size, _, _ := procGetSystemMetrics.Call(smCXSmIcon)
	if want := 16 * int(dpi) / 96; int(size) != want {
		t.Fatalf("размер значка %d при DPI %d, нужно %d", size, dpi, want)
	}
}
