// Package setup — установка и удаление Kinodom на этом ПК (спека этапа 11a, разделы 4.2–4.3):
// служба, права на папки, брандмауэр, ссылка kinodom://. Системные действия — через
// winsvc.System: в тестах — подделки.
package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kinodom/internal/app"
	"kinodom/internal/config"
	"kinodom/internal/settings"
	"kinodom/internal/store"
	"kinodom/internal/torrents"
	"kinodom/internal/winsvc"
)

// Имена в системе.
const (
	ServiceName        = "Kinodom"
	ServiceDisplayName = "Kinodom — домашний медиасервер"
	ServiceDescription = "Каталог раздач, просмотр, каналы и медиатека для телевизоров и телефонов домашней сети."
	RuleAPI            = "Kinodom — пульт"
	RuleTorrents       = "Kinodom — раздачи"
	Scheme             = "kinodom"
)

// StopWait — сколько ждать остановки службы.
const StopWait = 60 * time.Second

var (
	ErrNotAdmin = errors.New("нужны права администратора: запустите от имени администратора")
	ErrPortBusy = errors.New("порт пульта занят")
)

// InstallOptions — что и куда ставить.
type InstallOptions struct {
	Downloads    string        // папка загрузок; "" — не менять (при первой установке — по умолчанию)
	NoStart      bool          // не запускать службу
	Home         string        // %ProgramData%\Kinodom
	ProgramDir   string        // папка kinodom.exe и kinodomw.exe
	APIPort      int           // порт пульта (kinodom.json)
	TorrentPort  int           // порт раздач (kinodom.json)
	ReadyTimeout time.Duration // сколько ждать ответа запущенной службы; 0 — 60 с
}

// Install ставит или чинит Kinodom: повторный запуск ничего не теряет. log — ход для человека.
func Install(ctx context.Context, sys winsvc.System, o InstallOptions, log func(string)) error {
	if !sys.IsAdmin() {
		return ErrNotAdmin
	}
	paths := config.NewPaths(o.Home)
	exe := filepath.Join(o.ProgramDir, "kinodom.exe")
	if o.Downloads != "" {
		o.Downloads = filepath.Clean(o.Downloads)
		if !filepath.IsAbs(o.Downloads) {
			return fmt.Errorf("папка загрузок %s — нужен полный путь", o.Downloads)
		}
		// До системных действий: сетевой диск, FAT32, нет права записи — отказ сразу.
		if err := torrents.CheckDownloadsDir(o.Downloads); err != nil {
			return err
		}
	}
	exists, err := sys.SCM.Exists(ServiceName)
	if err != nil {
		return fmt.Errorf("диспетчер служб: %w", err)
	}
	if exists {
		log("Останавливаю службу Kinodom")
		if err := sys.SCM.Stop(ServiceName, StopWait); err != nil {
			return fmt.Errorf("служба Kinodom не остановилась: %w", err)
		}
	}
	// Порт — после остановки своей службы: занят ею — не помеха.
	busy, program, err := sys.Ports.Owner(o.APIPort)
	if err != nil {
		return fmt.Errorf("порт %d: %w", o.APIPort, err)
	}
	if busy {
		if program == "" {
			program = "другая программа"
		}
		return fmt.Errorf("%w: порт %d занят: %s", ErrPortBusy, o.APIPort, program)
	}

	// Служба — до прав: учётная запись NT SERVICE\Kinodom появляется вместе с ней.
	cfg := winsvc.ServiceConfig{Name: ServiceName, DisplayName: ServiceDisplayName, Description: ServiceDescription,
		Exe: exe, Args: []string{"service"}, Account: winsvc.ServiceAccount, DelayedStart: true,
		RestartDelay: 5 * time.Second, ResetPeriod: 24 * time.Hour}
	if exists {
		log("Обновляю службу")
		err = sys.SCM.Update(cfg)
	} else {
		log("Создаю службу Kinodom")
		err = sys.SCM.Install(cfg)
	}
	if err != nil {
		return fmt.Errorf("служба Kinodom: %w", err)
	}

	log("Выдаю права на папки")
	if err := os.MkdirAll(paths.Data, 0o755); err != nil {
		return fmt.Errorf("папка данных: %w", err)
	}
	if err := sys.ACL.Restrict(paths.Data, []string{winsvc.SIDSystem, winsvc.SIDAdmins, winsvc.ServiceAccount}); err != nil {
		return fmt.Errorf("права на %s: %w", paths.Data, err)
	}
	// База — после прав на data\: файлы базы и журналов наследуют их.
	downloads, err := downloadsDir(ctx, paths, o.Downloads)
	if err != nil {
		return err
	}
	if o.Downloads == "" { // папка из настроек или по умолчанию: создать, если её нет
		if err := torrents.CheckDownloadsDir(downloads); err != nil {
			return err
		}
	}
	if err := sys.ACL.Grant(downloads, winsvc.ServiceAccount, true); err != nil {
		return fmt.Errorf("права на %s: %w", downloads, err)
	}

	log("Настраиваю брандмауэр")
	rules := []winsvc.FirewallRule{
		{Name: RuleAPI, Program: exe, Protocols: []string{"TCP"}, Port: o.APIPort, Remote: "LocalSubnet"},
		{Name: RuleTorrents, Program: exe, Protocols: []string{"TCP", "UDP"}, Port: o.TorrentPort, Remote: "Any"},
	}
	for _, r := range rules {
		if err := sys.Firewall.Set(r); err != nil {
			return fmt.Errorf("брандмауэр, правило «%s»: %w", r.Name, err)
		}
	}
	if err := sys.Registry.SetProtocol(Scheme, OpenCommand(o.ProgramDir)); err != nil {
		return fmt.Errorf("ссылки kinodom://: %w", err)
	}
	if o.NoStart {
		return nil
	}
	log("Запускаю службу")
	if err := sys.SCM.Start(ServiceName); err != nil {
		return fmt.Errorf("служба Kinodom не запустилась: %w", err)
	}
	timeout := o.ReadyTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	if err := waitReady(ctx, o.APIPort, timeout); err != nil {
		if line := lastError(paths.Logs); line != "" {
			return fmt.Errorf("служба Kinodom запущена, но не отвечает: %s", line)
		}
		return fmt.Errorf("служба Kinodom запущена, но не отвечает %d с", int(timeout.Seconds()))
	}
	return nil
}

// OpenCommand — команда ссылок kinodom://: программа без консоли.
func OpenCommand(programDir string) string {
	return `"` + filepath.Join(programDir, "kinodomw.exe") + `" open "%1"`
}

// downloadsDir — папка загрузок: заданная (пишется в настройки), иначе из настроек, иначе по
// умолчанию.
func downloadsDir(ctx context.Context, paths config.Paths, given string) (string, error) {
	db, err := store.Open(ctx, paths.DB)
	if err != nil {
		return "", fmt.Errorf("база: %w", err)
	}
	defer db.Close()
	if given != "" {
		if err := db.SetSetting(ctx, settings.KeyDownloadsDir, given); err != nil {
			return "", fmt.Errorf("база: %w", err)
		}
		return given, nil
	}
	v, ok, err := db.Setting(ctx, settings.KeyDownloadsDir)
	if err != nil {
		return "", fmt.Errorf("база: %w", err)
	}
	if !ok || v == "" {
		return app.DefaultDownloadsDir, nil
	}
	return v, nil
}

// waitReady ждёт, пока API ответит на этом ПК.
func waitReady(ctx context.Context, port int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/api/v1/status"
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

var reLogValue = regexp.MustCompile(`(msg|err)=("(?:[^"\\]|\\.)*"|\S+)`)

// lastError — последняя ошибка из журнала сервера для человека: «сообщение: причина».
func lastError(logs string) string {
	f, err := os.Open(filepath.Join(logs, "kinodom.log"))
	if err != nil {
		return ""
	}
	defer f.Close()
	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "level=ERROR") {
			last = sc.Text()
		}
	}
	if last == "" {
		return ""
	}
	var parts []string
	for _, m := range reLogValue.FindAllStringSubmatch(last, -1) {
		v := m[2]
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, ": ")
}
