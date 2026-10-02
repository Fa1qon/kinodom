package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"kinodom/internal/player"
	"kinodom/internal/setup"
	"kinodom/internal/torrents"
	"kinodom/internal/winsvc"
)

// openGrant — ссылка «Разрешить доступ» (спека этапа 11a, раздел 4.7): служба подтверждает, что
// папка из её настроек; затем kinodomw.exe перезапускает себя от администратора (окно «Да/Нет»)
// командой grant и просит службу обойти медиатеку.
func openGrant(link string, apiPort int, stdout, stderr io.Writer) int {
	path, err := player.ParseGrant(link, apiPort)
	if err != nil {
		return report(stderr, "Ссылка не от Kinodom — ничего не сделано.", fmt.Errorf("%w: %s", err, link))
	}
	base := "http://127.0.0.1:" + strconv.Itoa(apiPort)
	kind, err := folderKind(base, path)
	if err != nil {
		return report(stderr, "Kinodom не отвечает — доступ не выдан.", err)
	}
	if kind == "" {
		return report(stderr, "Эта папка не из настроек Kinodom — доступ не выдан.", fmt.Errorf("папка не из настроек: %s", path))
	}
	args := []string{"grant", path}
	if kind == "downloads" || kind == "library" { // в папки медиатеки Kinodom качает и из них удаляет (план 14В)
		args = []string{"grant", "--write", path}
	}
	exe, err := os.Executable()
	if err != nil {
		return report(stderr, "Доступ не выдан.", err)
	}
	switch err := elevate(exe, args); {
	case errors.Is(err, winsvc.ErrCancelled):
		fmt.Fprintln(stderr, "Доступ не выдан: в окне Windows нажали «Нет»")
		return 1
	case errors.Is(err, winsvc.ErrElevatedFailed):
		fmt.Fprintln(stderr, "Доступ к папке не выдан:", err) // окно уже показал повышенный grant
		return 1
	case err != nil:
		return report(stderr, "Доступ к папке не выдан.", err)
	}
	c := &http.Client{Timeout: 10 * time.Second}
	if resp, err := c.Post(base+"/api/v1/library/scan", "application/json", bytes.NewReader([]byte("{}"))); err == nil {
		resp.Body.Close()
	}
	fmt.Fprintln(stdout, "Доступ выдан:", path)
	return 0
}

// folderKind — чья папка по словам службы: library, downloads или "" (не из настроек).
func folderKind(base, path string) (string, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(base + "/api/v1/library/access?path=" + url.QueryEscape(path))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("служба ответила %d", resp.StatusCode)
	}
	var v struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Kind, nil
}

// cmdGrant — права учётной записи службы на папку (от администратора): чтение, с --write —
// изменение. Её запускает openGrant после окна Windows «Да/Нет».
func cmdGrant(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("grant", flag.ContinueOnError)
	fs.SetOutput(stderr)
	write := fs.Bool("write", false, "право на изменение (папки загрузок и медиатеки)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(stderr, "использование: kinodom grant [--write] ПАПКА")
		return 2
	}
	dir := fs.Arg(0)
	if !filepath.IsAbs(dir) || torrents.IsNetworkPath(dir) {
		return report(stderr, "Доступ не выдан: папка не на диске этого компьютера.", fmt.Errorf("не папка на диске этого ПК: %s", dir))
	}
	if clean := filepath.Clean(dir); clean == filepath.VolumeName(clean)+`\` {
		return report(stderr, "Доступ не выдан: выберите папку, а не весь диск.", fmt.Errorf("весь диск: %s", dir))
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return report(stderr, "Доступ не выдан: папки нет.", fmt.Errorf("папки нет: %s", dir))
	}
	sys := newSystem()
	if !sys.IsAdmin() {
		return report(stderr, "Доступ не выдан: нужны права администратора.", setup.ErrNotAdmin)
	}
	if err := sys.ACL.Grant(dir, winsvc.ServiceAccount, *write); err != nil {
		return report(stderr, "Доступ к папке не выдан.", fmt.Errorf("права на %s: %w", dir, err))
	}
	fmt.Fprintln(stdout, "Доступ выдан:", dir)
	return 0
}
