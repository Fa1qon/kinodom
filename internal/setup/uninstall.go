//go:build windows

package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"kinodom/internal/config"
	"kinodom/internal/winsvc"
)

// UninstallOptions — что удалять.
type UninstallOptions struct {
	Purge      bool   // ещё настройки (папку данных Kinodom)
	Home       string // %ProgramData%\Kinodom
	ProgramDir string // папка kinodomw.exe: значки в трее закрываются
}

// Uninstall убирает значки в трее, службу, правила брандмауэра, ссылку kinodom:// и автозапуск (спека
// этапа 11a, разделы 4.3 и 5.2). Скачанное и папки медиатеки — это файлы человека в его папках,
// удаление программы их не трогает. Purge удаляет только папку данных самой программы и только
// если это действительно она: путь совпадает с домашней папкой Kinodom этого ПК.
func Uninstall(ctx context.Context, sys winsvc.System, o UninstallOptions, log func(string)) error {
	if !sys.IsAdmin() {
		return ErrNotAdmin
	}
	purge, home := o.Purge, o.Home
	if err := sys.Procs.Close(filepath.Join(o.ProgramDir, "kinodomw.exe")); err != nil {
		log("Значок в трее не закрылся: " + err.Error())
	}
	log("Останавливаю службу Kinodom")
	switch err := sys.SCM.Stop(ServiceName, StopWait); {
	case errors.Is(err, winsvc.ErrNotInstalled):
	case err != nil:
		return fmt.Errorf("служба Kinodom не остановилась: %w", err)
	default:
		if err := sys.SCM.Delete(ServiceName); err != nil {
			return fmt.Errorf("служба Kinodom не удалилась: %w", err)
		}
	}
	if err := sys.Procs.Close(filepath.Join(o.ProgramDir, "kinodom.exe")); err != nil {
		return fmt.Errorf("процесс Kinodom не завершился: %w", err)
	}
	for _, name := range []string{RuleAPI, RuleTorrents, RuleDiscovery} {
		if err := sys.Firewall.Delete(name); err != nil {
			return fmt.Errorf("брандмауэр, правило «%s»: %w", name, err)
		}
	}
	if err := sys.Registry.DeleteProtocol(Scheme); err != nil {
		return fmt.Errorf("ссылки kinodom://: %w", err)
	}
	if err := sys.Registry.DeleteAutorun(AutorunName); err != nil {
		return fmt.Errorf("значок в трее: %w", err)
	}
	if !purge {
		return nil
	}
	log("Удаляю настройки Kinodom")
	if filepath.Clean(home) != filepath.Clean(config.DefaultHome()) {
		// Домашняя папка задаётся человеком (KINODOM_HOME) — по чужому пути настройки не удаляются.
		log("Настройки не удалены: " + home + " — не папка данных Kinodom этого ПК")
		return nil
	}
	if err := os.RemoveAll(home); err != nil {
		return fmt.Errorf("данные Kinodom %s не удалились: %w", home, err)
	}
	return nil
}
