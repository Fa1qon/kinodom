package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"kinodom/internal/app"
	"kinodom/internal/config"
	"kinodom/internal/settings"
	"kinodom/internal/store"
	"kinodom/internal/torrents"
	"kinodom/internal/winsvc"
)

// UninstallOptions — что удалять.
type UninstallOptions struct {
	Purge      bool   // ещё данные и скачанное
	Home       string // %ProgramData%\Kinodom
	ProgramDir string // папка kinodomw.exe: значки в трее закрываются
}

// Uninstall убирает значки в трее, службу, правила брандмауэра, ссылку kinodom:// и автозапуск (спека
// этапа 11a, разделы 4.3 и 5.2). Purge — ещё данные (Home) и скачанное: папки раздач из реестра, затем
// папка загрузок, если она пуста. Чужие файлы в папке загрузок не удаляются.
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
	for _, name := range []string{RuleAPI, RuleTorrents} {
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
	log("Удаляю скачанное и настройки")
	paths := config.NewPaths(home)
	downloads, folders, err := downloaded(ctx, paths)
	if err != nil {
		return err
	}
	for _, f := range folders {
		if err := os.RemoveAll(f); err != nil {
			return fmt.Errorf("папка раздачи %s не удалилась: %w", f, err)
		}
	}
	if downloads != "" {
		os.Remove(downloads) // только пустая: чужие файлы остаются вместе с папкой
	}
	if err := os.RemoveAll(home); err != nil {
		return fmt.Errorf("данные Kinodom %s не удалились: %w", home, err)
	}
	return nil
}

// downloaded — папка загрузок из настроек и папки раздач из реестра; базы нет — ничего.
func downloaded(ctx context.Context, paths config.Paths) (string, []string, error) {
	if _, err := os.Stat(paths.DB); err != nil {
		return "", nil, nil
	}
	db, err := store.Open(ctx, paths.DB)
	if err != nil {
		return "", nil, fmt.Errorf("база: %w", err)
	}
	defer db.Close()
	dir, ok, err := db.Setting(ctx, settings.KeyDownloadsDir)
	if err != nil {
		return "", nil, fmt.Errorf("база: %w", err)
	}
	if !ok || dir == "" {
		dir = app.DefaultDownloadsDir
	}
	folders, err := torrents.NewRegistry(db).Folders(ctx, dir)
	if err != nil {
		return "", nil, fmt.Errorf("реестр раздач: %w", err)
	}
	return dir, folders, nil
}
