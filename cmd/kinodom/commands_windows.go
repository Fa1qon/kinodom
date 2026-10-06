//go:build windows

package main

// Служба, установка и значок — только Windows (портирование сервера, план 2026-10-06): на других
// ОС сервер запускается командой run, всё системное ставит инсталлятор.

func osCommands() []command {
	return []command{
		{"service", "сервер как служба Windows (запускает диспетчер служб)", cmdService},
		{"install", "установить или починить службу на этом ПК (от администратора): kinodom install --downloads D:\\Kinodom", cmdInstall},
		{"uninstall", "удалить службу (от администратора); --purge — ещё настройки", cmdUninstall},
		{"stop", "остановить службу перед заменой файлов (перезапуск при сбое вернёт install)", cmdStop},
		{"check", "проверить установку: служба, пульт, брандмауэр, ссылки kinodom://", cmdCheck},
		{"grant", "права службы на папку (от администратора): kinodom grant [--write] ПАПКА", cmdGrant},
		{"tray", "значок в трее: открыть пульт, выход с остановкой сервера (kinodomw tray [--open])", cmdTray},
		{"open", "открыть поток в плеере по ссылке kinodom:// (её открывает браузер на этом ПК)", cmdOpen},
		{"protocol", "ссылка kinodom:// для этого пользователя: kinodom protocol install | uninstall", cmdProtocol},
	}
}
