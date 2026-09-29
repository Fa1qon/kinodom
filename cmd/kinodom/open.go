package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"kinodom/internal/config"
	"kinodom/internal/player"
)

// Плеер ищется и запускается через переменные: тесты подменяют их, чтобы не запускать настоящий VLC.
var (
	findPlayer   = player.Find
	launchPlayer = player.Launch
)

// cmdOpen — обработчик ссылки kinodom:// (браузер на этом ПК вызывает его в сеансе пользователя):
// разбирает ссылку строго по спеке, спрашивает у сервера, какой плеер выбран, и запускает его.
func cmdOpen(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "использование: kinodom open <kinodom://play?url=…>")
		return 2
	}
	boot, err := config.LoadBootstrap(config.NewPaths(config.DefaultHome()).Bootstrap)
	if err != nil {
		return fail(stderr, err)
	}
	stream, title, err := player.ParseLaunch(args[0], boot.APIPort)
	if err != nil {
		return fail(stderr, err)
	}
	p, err := findPlayer(playerSetting(boot.APIPort))
	if err != nil {
		return fail(stderr, err)
	}
	if err := launchPlayer(p, stream, title, player.LaunchStart(args[0])); err != nil {
		return fail(stderr, fmt.Errorf("%s не запустился: %w", p.Name, err))
	}
	fmt.Fprintf(stdout, "%s: %s\n", p.Name, title)
	return 0
}

// playerSetting — плеер из настроек работающего сервера; сервер не ответил — «auto».
func playerSetting(apiPort int) string {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/settings", apiPort))
	if err != nil {
		return "auto"
	}
	defer resp.Body.Close()
	var v struct {
		Player string `json:"player"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&v) != nil || v.Player == "" {
		return "auto"
	}
	return v.Player
}

// cmdProtocol регистрирует ссылку kinodom:// у текущего пользователя (без прав администратора) —
// чтобы «Смотреть» в браузере на этом ПК открывало плеер до инсталлятора (этап 11).
func cmdProtocol(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "install" && args[0] != "uninstall") {
		fmt.Fprintln(stderr, "использование: kinodom protocol install | uninstall")
		return 2
	}
	if args[0] == "uninstall" {
		if err := player.UninstallProtocol(); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stdout, "Ссылка kinodom:// снята")
		return 0
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(stderr, err)
	}
	if err := player.InstallProtocol(exe); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "Ссылка kinodom:// открывается через", exe)
	return 0
}
