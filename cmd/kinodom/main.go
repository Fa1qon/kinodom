// Команда kinodom — домашний медиасервер: торренты, IPTV, медиатека, DLNA.
package main

import (
	"fmt"
	"io"
	"os"
)

// version подставляется при сборке: -ldflags "-X main.version=…" (см. build.ps1).
var version = "dev"

// command — одна подкоманда: kinodom <name> [аргументы].
type command struct {
	name  string
	about string
	run   func(args []string, stdout, stderr io.Writer) int
}

// commands — все подкоманды. Новые команды добавляются сюда.
func commands() []command {
	return []command{
		{"run", "запустить сервер в консоли (для разработки)", cmdRun},
		{"service", "сервер как служба Windows (запускает диспетчер служб)", cmdService},
		{"install", "установить или починить службу на этом ПК (от администратора): kinodom install --downloads D:\\Kinodom", cmdInstall},
		{"uninstall", "удалить службу (от администратора); --purge — ещё настройки и скачанное", cmdUninstall},
		{"stop", "остановить службу перед заменой файлов (перезапуск при сбое вернёт install)", cmdStop},
		{"check", "проверить установку: служба, пульт, брандмауэр, ссылки kinodom://", cmdCheck},
		{"grant", "права службы на папку (от администратора): kinodom grant [--write] ПАПКА", cmdGrant},
		{"tray", "значок в трее: открыть пульт, выход с остановкой сервера (kinodomw tray [--open])", cmdTray},
		{"play", "открыть раздачу на запущенном сервере и получить ссылку для VLC", cmdPlay},
		{"source", "проверить источник раздач вживую: kinodom source rutor top 12", cmdSource},
		{"meta", "проверить метаданные вживую: kinodom meta kp film 301", cmdMeta},
		{"torrent", "проверить движок вживую: kinodom torrent info <magnet> — метаинфо от пиров", cmdTorrent},
		{"catalog", "каталог вживую на отдельной папке: kinodom catalog refresh --home …", cmdCatalog},
		{"open", "открыть поток в плеере по ссылке kinodom:// (её открывает браузер на этом ПК)", cmdOpen},
		{"protocol", "ссылка kinodom:// для этого пользователя: kinodom protocol install | uninstall", cmdProtocol},
		{"version", "показать версию", cmdVersion},
	}
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

// runCLI выбирает подкоманду и возвращает код выхода: 0 — успех, 1 — ошибка, 2 — неверный вызов.
func runCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	for _, c := range commands() {
		if c.name == args[0] {
			return c.run(args[1:], stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "неизвестная команда %q\n\n", args[0])
	printUsage(stderr)
	return 2
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Использование: kinodom <команда> [аргументы]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Команды:")
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.about)
	}
}

func cmdVersion(_ []string, stdout, _ io.Writer) int {
	fmt.Fprintf(stdout, "kinodom %s\n", version)
	return 0
}
