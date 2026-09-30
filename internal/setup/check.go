package setup

import (
	"context"
	"fmt"

	"kinodom/internal/config"
	"kinodom/internal/winsvc"
)

// Check — то, что предсказуемо сорвёт установку, до копирования файлов (установщик вызывает
// kinodom install --check в PrepareToInstall; спека этапа 11a, раздел 5.1): права администратора,
// папка загрузок, занятый порт пульта. Ничего не меняет в системе и не создаёт базу.
func Check(ctx context.Context, sys winsvc.System, o InstallOptions) error {
	if !sys.IsAdmin() {
		return ErrNotAdmin
	}
	if _, err := chooseDownloads(ctx, config.NewPaths(o.Home), o); err != nil {
		return err
	}
	return portFree(sys, o.APIPort)
}

// portFree — порт пульта не занят другой программой.
func portFree(sys winsvc.System, port int) error {
	busy, program, err := sys.Ports.Owner(port)
	if err != nil {
		return fmt.Errorf("порт %d: %w", port, err)
	}
	if busy {
		if program == "" {
			program = "другая программа"
		}
		return fmt.Errorf("%w: порт %d занят: %s", ErrPortBusy, port, program)
	}
	return nil
}
