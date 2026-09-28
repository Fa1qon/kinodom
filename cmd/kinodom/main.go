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
		{"play", "открыть раздачу на запущенном сервере и получить ссылку для VLC", cmdPlay},
		{"source", "проверить источник раздач вживую: kinodom source rutor top 12", cmdSource},
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
