//go:build windows

package app

import (
	"os"
	"path/filepath"

	"kinodom/internal/winsvc"
)

// Константы службы — те же, что ставит установщик (internal/setup): здесь их повторить нельзя —
// internal/setup импортирует internal/app.
const (
	serviceName        = "Kinodom"
	serviceDisplayName = "Kinodom — домашний медиасервер"
	serviceDescription = "Каталог раздач, просмотр, каналы и медиатека для телевизоров и телефонов домашней сети."
)

// applyAutorun — запуск Kinodom при старте ОС (просьба 2026-10-07): значок в трее при входе
// любого пользователя и служба — автозапуск или по требованию (пульт и kinodom:// её будят).
func (a *App) applyAutorun(on bool) {
	sys := winsvc.Real()
	exe, err := os.Executable()
	if err != nil {
		a.Log.Warn("автозапуск: не нашёл программу", "err", err)
		return
	}
	dir := filepath.Dir(exe)
	if on {
		if err := sys.Registry.SetAutorun("Kinodom", `"`+filepath.Join(dir, "kinodomw.exe")+`" tray`); err != nil {
			a.Log.Warn("автозапуск не включился", "err", err)
		}
	} else if err := sys.Registry.DeleteAutorun("Kinodom"); err != nil {
		a.Log.Warn("автозапуск не выключился", "err", err)
	}
	a.setServiceManual(sys, !on)
}

// setServiceManual — служба: автозапуск (отложенный) или вручную.
func (a *App) setServiceManual(sys winsvc.System, manual bool) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cfg := winsvc.ServiceConfig{Name: serviceName, DisplayName: serviceDisplayName,
		Description: serviceDescription, Exe: exe, Args: []string{"service"},
		Account: winsvc.ServiceAccount, DelayedStart: !manual, Manual: manual}
	if err := sys.SCM.Update(cfg); err != nil {
		a.Log.Warn("тип запуска службы не изменился", "err", err)
	}
}
