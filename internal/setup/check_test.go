//go:build windows

package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kinodom/internal/config"
	"kinodom/internal/winsvc/winsvctest"
)

// Проверка до копирования файлов (установщик, PrepareToInstall; спека этапа 11a, раздел 5.1): то,
// что предсказуемо сорвёт установку, — отказ без единого системного действия и без базы.
func TestCheckBeforeCopy(t *testing.T) {
	f := winsvctest.New()
	o := options(t, filepath.Join(t.TempDir(), "K"))
	if err := Check(ctx, f.System(), o); err != nil {
		t.Fatalf("всё в порядке: %v", err)
	}
	f.PortOwner = "other-server.exe"
	if err := Check(ctx, f.System(), o); !errors.Is(err, ErrPortBusy) || !strings.Contains(err.Error(), "other-server.exe") {
		t.Fatalf("порт занят: %v", err)
	}
	f.PortOwner = ""
	for _, bad := range []string{`D:\`, `\server\share\K`, "Kinodom"} {
		o.Downloads = bad
		if err := Check(ctx, f.System(), o); err == nil {
			t.Errorf("папка загрузок %s принята", bad)
		}
	}
	o.Downloads = ""
	f.Admin = false
	if err := Check(ctx, f.System(), o); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("не администратор: %v", err)
	}
	if acts := f.Actions(); len(acts) != 0 {
		t.Fatalf("проверка что-то сделала: %v", acts)
	}
	if _, err := os.Stat(config.NewPaths(o.Home).DB); !os.IsNotExist(err) {
		t.Fatalf("проверка создала базу: %v", err)
	}
}
