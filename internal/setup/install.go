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
	AutorunName        = "Kinodom" // значок в трее при входе любого пользователя
)

// StopWait — сколько ждать остановки службы.
const StopWait = 60 * time.Second

var (
	ErrNotAdmin = errors.New("нужны права администратора: запустите от имени администратора")
	ErrPortBusy = errors.New("порт пульта занят")
)

// InstallOptions — что и куда ставить.
type InstallOptions struct {
	Downloads        string        // папка загрузок: заменяет сохранённую ("" — не менять)
	DownloadsDefault string        // папка загрузок, только если её ещё нет в настройках (установщик)
	NoStart          bool          // не запускать службу
	Home             string        // %ProgramData%\Kinodom
	ProgramDir       string        // папка kinodom.exe и kinodomw.exe
	APIPort          int           // порт пульта (kinodom.json)
	TorrentPort      int           // порт раздач (kinodom.json)
	ReadyTimeout     time.Duration // сколько ждать ответа запущенной службы; 0 — 60 с
}

// Install ставит или чинит Kinodom: повторный запуск ничего не теряет. log — ход для человека.
// Первая установка, не дошедшая до конца, убирает за собой службу, правила и ссылку.
func Install(ctx context.Context, sys winsvc.System, o InstallOptions, log func(string)) (err error) {
	if !sys.IsAdmin() {
		return ErrNotAdmin
	}
	paths := config.NewPaths(o.Home)
	exe := filepath.Join(o.ProgramDir, "kinodom.exe")
	dl, err := chooseDownloads(ctx, paths, o)
	if err != nil {
		return err
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
	if err := portFree(sys, o.APIPort); err != nil {
		return err
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
		defer func() {
			if err != nil {
				log("Установка не удалась — убираю службу, правила и ссылку")
				rollback(sys)
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("служба Kinodom: %w", err)
	}
	// Значок в трее запускает и останавливает службу от имени пользователя, без окна прав.
	if err := sys.SCM.AllowUserControl(ServiceName); err != nil {
		return fmt.Errorf("служба Kinodom, права пользователей: %w", err)
	}

	log("Выдаю права на папки")
	if err := os.MkdirAll(paths.Data, 0o755); err != nil {
		return fmt.Errorf("папка данных: %w", err)
	}
	// Корень данных читают все (kinodom.json без секретов), пишут — только администраторы; data\ —
	// только служба и администраторы. Владелец обеих — Administrators.
	if err := sys.ACL.Restrict(paths.Home, []string{winsvc.SIDSystem, winsvc.SIDAdmins},
		[]string{winsvc.SIDUsers, winsvc.ServiceAccount}); err != nil {
		return fmt.Errorf("права на %s: %w", paths.Home, err)
	}
	if err := sys.ACL.Restrict(paths.Data, []string{winsvc.SIDSystem, winsvc.SIDAdmins, winsvc.ServiceAccount}, nil); err != nil {
		return fmt.Errorf("права на %s: %w", paths.Data, err)
	}
	// База — после прав на data\: новые файлы базы наследуют их.
	if dl.save {
		if err := saveDownloads(ctx, paths, dl.dir); err != nil {
			return err
		}
	}
	if !dl.save {
		// Папка из настроек: диск могли отключить — установка всё равно проходит.
		if cerr := torrents.CheckDownloadsDir(dl.dir); cerr != nil {
			log("Папка загрузок " + dl.dir + " недоступна — права на неё будут выданы при следующей установке")
			dl.dir = ""
		}
	}
	if dl.dir != "" {
		if err := sys.ACL.Grant(dl.dir, winsvc.ServiceAccount, true); err != nil {
			return fmt.Errorf("права на %s: %w", dl.dir, err)
		}
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
	if err := sys.Registry.SetAutorun(AutorunName, TrayCommand(o.ProgramDir)); err != nil {
		return fmt.Errorf("значок в трее: %w", err)
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

// rollback — первая установка не удалась: служба (иначе она перезапускалась бы каждые 5 с без
// программы удаления), правила брандмауэра и ссылка убираются. Данные остаются.
func rollback(sys winsvc.System) {
	sys.SCM.Stop(ServiceName, StopWait)
	sys.SCM.Delete(ServiceName)
	sys.Firewall.Delete(RuleAPI)
	sys.Firewall.Delete(RuleTorrents)
	sys.Registry.DeleteProtocol(Scheme)
	sys.Registry.DeleteAutorun(AutorunName)
}

// TrayCommand — значок в трее при входе в Windows (спека этапа 11a, раздел 5.2).
func TrayCommand(programDir string) string {
	return `"` + filepath.Join(programDir, "kinodomw.exe") + `" tray`
}

// OpenCommand — команда ссылок kinodom://: программа без консоли.
func OpenCommand(programDir string) string {
	return `"` + filepath.Join(programDir, "kinodomw.exe") + `" open "%1"`
}

// downloadsChoice — папка загрузок установки: save — новый выбор (записать в настройки и проверить
// до любых действий), иначе — сохранённая или по умолчанию.
type downloadsChoice struct {
	dir  string
	save bool
}

// chooseDownloads решает, какая папка загрузок, до системных действий. Базу только читает и не
// переводит на новую схему: это делает служба (откат обновления оставит базу прежней версии).
func chooseDownloads(ctx context.Context, paths config.Paths, o InstallOptions) (downloadsChoice, error) {
	saved := ""
	if db, err := store.OpenAsIs(ctx, paths.DB); err == nil {
		saved, _, err = db.Setting(ctx, settings.KeyDownloadsDir)
		db.Close()
		if err != nil {
			return downloadsChoice{}, fmt.Errorf("база: %w", err)
		}
	}
	var c downloadsChoice
	switch {
	case o.Downloads != "":
		c = downloadsChoice{dir: o.Downloads, save: true}
	case saved != "":
		c = downloadsChoice{dir: saved}
	case o.DownloadsDefault != "":
		c = downloadsChoice{dir: o.DownloadsDefault, save: true}
	default:
		c = downloadsChoice{dir: app.DefaultDownloadsDir}
	}
	if !c.save {
		return c, nil
	}
	c.dir = filepath.Clean(c.dir)
	if !filepath.IsAbs(c.dir) {
		return c, fmt.Errorf("папка загрузок %s — нужен полный путь", c.dir)
	}
	if vol := filepath.VolumeName(c.dir); vol != "" && c.dir == vol+`\` {
		return c, fmt.Errorf("папка загрузок %s — весь диск: выберите папку на нём, например %sKinodom", c.dir, c.dir)
	}
	// Сетевой диск, FAT32, нет права записи — отказ до любых действий.
	return c, torrents.CheckDownloadsDir(c.dir)
}

// saveDownloads пишет папку загрузок в настройки: база есть — без миграций, нет — создаётся.
func saveDownloads(ctx context.Context, paths config.Paths, dir string) error {
	db, err := store.OpenAsIs(ctx, paths.DB)
	if err != nil {
		db, err = store.Open(ctx, paths.DB)
	}
	if err != nil {
		return fmt.Errorf("база: %w", err)
	}
	defer db.Close()
	if err := db.SetSetting(ctx, settings.KeyDownloadsDir, dir); err != nil {
		return fmt.Errorf("база: %w", err)
	}
	return nil
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
