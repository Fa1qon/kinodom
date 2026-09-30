package setup

import (
	"context"
	"errors"
	"fmt"
	"os"

	"kinodom/internal/app"
	"kinodom/internal/config"
	"kinodom/internal/settings"
	"kinodom/internal/store"
	"kinodom/internal/torrents"
	"kinodom/internal/winsvc"
)

// Uninstall убирает службу, правила брандмауэра и ссылку kinodom:// (спека этапа 11a, раздел 4.3).
// purge — ещё данные (home) и скачанное: папки раздач из реестра, затем папка загрузок, если она
// пуста. Чужие файлы в папке загрузок не удаляются.
func Uninstall(ctx context.Context, sys winsvc.System, purge bool, home string, log func(string)) error {
	if !sys.IsAdmin() {
		return ErrNotAdmin
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
