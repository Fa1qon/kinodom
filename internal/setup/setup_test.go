package setup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/config"
	"kinodom/internal/settings"
	"kinodom/internal/store"
	"kinodom/internal/torrents"
	"kinodom/internal/torrents/torrenttest"
	"kinodom/internal/winsvc"
	"kinodom/internal/winsvc/winsvctest"
)

var ctx = context.Background()

const prog = `C:\Program Files\Kinodom`

// kinodomAPI — «служба» на случайном порту: отвечает на /api/v1/status.
func kinodomAPI(t *testing.T) int {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	port, _ := strconv.Atoi(u.Port())
	return port
}

func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func options(t *testing.T, downloads string) InstallOptions {
	return InstallOptions{Downloads: downloads, Home: t.TempDir(), ProgramDir: prog, APIPort: kinodomAPI(t), TorrentPort: 42000,
		ReadyTimeout: 5 * time.Second}
}

func setting(t *testing.T, home, key string) string {
	t.Helper()
	db, err := store.Open(ctx, config.NewPaths(home).DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, _, err := db.Setting(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func nolog(string) {}

// indexOf — номер действия, начинающегося с prefix; −1 — нет.
func indexOf(acts []string, prefix string) int {
	return slices.IndexFunc(acts, func(a string) bool { return strings.HasPrefix(a, prefix) })
}

// Первая установка (спека этапа 11a, раздел 4.2): служба, права, брандмауэр, ссылка, запуск — и
// папка загрузок в настройках. Права выдаются после создания службы: учётная запись
// NT SERVICE\Kinodom появляется вместе с ней.
func TestInstallFirstTime(t *testing.T) {
	f := winsvctest.New()
	dl := filepath.Join(t.TempDir(), "Kinodom")
	o := options(t, dl)
	if err := Install(ctx, f.System(), o, nolog); err != nil {
		t.Fatal(err)
	}
	acts := f.Actions()
	data := config.NewPaths(o.Home).Data
	exe := filepath.Join(prog, "kinodom.exe")
	want := []string{
		"scm.install Kinodom",
		"acl.restrict " + o.Home + " full=S-1-5-18,S-1-5-32-544 read=S-1-5-32-545,NT SERVICE\\Kinodom",
		"acl.restrict " + data + " full=S-1-5-18,S-1-5-32-544,NT SERVICE\\Kinodom read=",
		"acl.grant " + dl + " NT SERVICE\\Kinodom write",
		fmt.Sprintf("fw.set Kinodom — пульт TCP %d LocalSubnet %s", o.APIPort, exe),
		"fw.set Kinodom — раздачи TCP,UDP 42000 Any " + exe,
		`reg.set kinodom "` + filepath.Join(prog, "kinodomw.exe") + `" open "%1"`,
		"scm.start Kinodom",
	}
	if !slices.Equal(acts, want) {
		t.Fatalf("действия:\n%s\nждали:\n%s", strings.Join(acts, "\n"), strings.Join(want, "\n"))
	}
	c := f.Services["Kinodom"]
	if c.Exe != exe || !slices.Equal(c.Args, []string{"service"}) || c.Account != winsvc.ServiceAccount || !c.DelayedStart ||
		c.RestartDelay != 5*time.Second || c.ResetPeriod != 24*time.Hour || c.DisplayName != "Kinodom — домашний медиасервер" {
		t.Fatalf("служба: %+v", c)
	}
	if got := setting(t, o.Home, settings.KeyDownloadsDir); got != dl {
		t.Fatalf("папка загрузок в настройках: %q", got)
	}
	if fi, err := os.Stat(dl); err != nil || !fi.IsDir() {
		t.Fatalf("папка загрузок не создана: %v", err)
	}
}

// Повторный запуск чинит установку: служба останавливается и обновляется, права и правила —
// заново; без --downloads папка загрузок в настройках не меняется.
func TestInstallAgainRepairs(t *testing.T) {
	f := winsvctest.New()
	dl := filepath.Join(t.TempDir(), "Kinodom")
	o := options(t, dl)
	if err := Install(ctx, f.System(), o, nolog); err != nil {
		t.Fatal(err)
	}
	f.Reset()
	o.Downloads = ""
	if err := Install(ctx, f.System(), o, nolog); err != nil {
		t.Fatal(err)
	}
	acts := f.Actions()
	stop, update := indexOf(acts, "scm.stop Kinodom"), indexOf(acts, "scm.update Kinodom")
	if stop != 0 || update < stop || indexOf(acts, "scm.install") >= 0 || indexOf(acts, "acl.grant "+dl) < 0 ||
		indexOf(acts, "scm.start") != len(acts)-1 {
		t.Fatalf("действия: %v", acts)
	}
	if got := setting(t, o.Home, settings.KeyDownloadsDir); got != dl {
		t.Fatalf("папка загрузок изменилась: %q", got)
	}
}

func TestInstallNeedsAdmin(t *testing.T) {
	f := winsvctest.New()
	f.Admin = false
	err := Install(ctx, f.System(), options(t, filepath.Join(t.TempDir(), "K")), nolog)
	if !errors.Is(err, ErrNotAdmin) || len(f.Actions()) != 0 {
		t.Fatalf("err %v, действия %v", err, f.Actions())
	}
	if err := Uninstall(ctx, f.System(), false, t.TempDir(), nolog); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("удаление: %v", err)
	}
}

// Порт пульта занят другой программой (Review Focus 3): отказ с её названием, ничего не сломано;
// порт освободили — установка проходит.
func TestInstallPortBusy(t *testing.T) {
	f := winsvctest.New()
	f.PortOwner = "other-server.exe"
	o := options(t, filepath.Join(t.TempDir(), "K"))
	err := Install(ctx, f.System(), o, nolog)
	if !errors.Is(err, ErrPortBusy) || !strings.Contains(err.Error(), "other-server.exe") ||
		!strings.Contains(err.Error(), strconv.Itoa(o.APIPort)) {
		t.Fatalf("err %v", err)
	}
	if acts := f.Actions(); len(acts) != 0 {
		t.Fatalf("при занятом порте что-то сделано: %v", acts)
	}
	f.PortOwner = ""
	if err := Install(ctx, f.System(), o, nolog); err != nil {
		t.Fatalf("после освобождения порта: %v", err)
	}
}

// Сетевая папка загрузок — отказ до любых действий.
func TestInstallRejectsNetworkDownloads(t *testing.T) {
	f := winsvctest.New()
	err := Install(ctx, f.System(), options(t, `\\server\share\Kinodom`), nolog)
	if err == nil || !strings.Contains(err.Error(), "сетев") || len(f.Actions()) != 0 {
		t.Fatalf("err %v, действия %v", err, f.Actions())
	}
}

// Служба не ответила за отведённое время — отказ с последней ошибкой из журнала сервера.
func TestInstallServiceNotReady(t *testing.T) {
	f := winsvctest.New()
	o := options(t, filepath.Join(t.TempDir(), "K"))
	o.APIPort, o.ReadyTimeout = closedPort(t), 300*time.Millisecond
	logs := config.NewPaths(o.Home).Logs
	os.MkdirAll(logs, 0o755)
	os.WriteFile(filepath.Join(logs, "kinodom.log"),
		[]byte("time=2026-09-30T10:00:00 level=INFO msg=старт\ntime=2026-09-30T10:00:01 level=ERROR msg=\"Kinodom не запустился\" err=\"база: диск полон\"\n"), 0o644)
	err := Install(ctx, f.System(), o, nolog)
	if err == nil || !strings.Contains(err.Error(), "база: диск полон") {
		t.Fatalf("err %v", err)
	}
}

// Удаление без данных: служба, правила и ссылка убраны; настройки, база и скачанное — на месте.
func TestUninstallKeepsData(t *testing.T) {
	f := winsvctest.New()
	dl := filepath.Join(t.TempDir(), "Kinodom")
	o := options(t, dl)
	if err := Install(ctx, f.System(), o, nolog); err != nil {
		t.Fatal(err)
	}
	f.Reset()
	if err := Uninstall(ctx, f.System(), false, o.Home, nolog); err != nil {
		t.Fatal(err)
	}
	want := []string{"scm.stop Kinodom", "scm.delete Kinodom", "fw.delete Kinodom — пульт", "fw.delete Kinodom — раздачи", "reg.delete kinodom"}
	if acts := f.Actions(); !slices.Equal(acts, want) {
		t.Fatalf("действия %v", acts)
	}
	if _, err := os.Stat(config.NewPaths(o.Home).DB); err != nil {
		t.Fatalf("база удалена: %v", err)
	}
	if _, err := os.Stat(dl); err != nil {
		t.Fatalf("папка загрузок удалена: %v", err)
	}
	// Второй раз — службы уже нет: не ошибка.
	if err := Uninstall(ctx, f.System(), false, o.Home, nolog); err != nil {
		t.Fatalf("повторное удаление: %v", err)
	}
}

// Удаление с данными (Review Focus 4): папки раздач из реестра и данные удаляются, чужие файлы в
// папке загрузок остаются; пустая папка загрузок удаляется.
func TestUninstallPurgeKeepsForeign(t *testing.T) {
	for _, foreign := range []bool{true, false} {
		f := winsvctest.New()
		dl := filepath.Join(t.TempDir(), "Kinodom")
		o := options(t, dl)
		if err := Install(ctx, f.System(), o, nolog); err != nil {
			t.Fatal(err)
		}
		folder := addTorrent(t, o.Home, dl)
		mine := filepath.Join(dl, "мои фото.jpg")
		if foreign {
			os.WriteFile(mine, []byte("x"), 0o644)
		}
		if err := Uninstall(ctx, f.System(), true, o.Home, nolog); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(folder); !os.IsNotExist(err) {
			t.Fatalf("папка раздачи осталась: %v", err)
		}
		if _, err := os.Stat(o.Home); !os.IsNotExist(err) {
			t.Fatalf("данные остались: %v", err)
		}
		_, errMine := os.Stat(mine)
		_, errDL := os.Stat(dl)
		if foreign && (errMine != nil || errDL != nil) {
			t.Fatalf("чужой файл удалён: %v, %v", errMine, errDL)
		}
		if !foreign && !os.IsNotExist(errDL) {
			t.Fatalf("пустая папка загрузок осталась: %v", errDL)
		}
	}
}

// addTorrent — раздача в реестре и её папка с файлом в папке загрузок.
func addTorrent(t *testing.T, home, dl string) string {
	t.Helper()
	mi, _ := torrenttest.MakeTorrent(t, t.TempDir(), "Фильм", 1<<14, torrenttest.File{Path: "film.mkv", Size: 10})
	db, err := store.Open(ctx, config.NewPaths(home).DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ih metainfo.Hash
	ih.FromHexString(strings.Repeat("a", 40))
	r := torrents.NewRegistry(db)
	if _, err := r.Remember(ctx, ih, "magnet:a", dl); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveMetainfo(ctx, ih, "Фильм", mi.InfoBytes); err != nil {
		t.Fatal(err)
	}
	folders, err := r.Folders(ctx, dl)
	if err != nil || len(folders) != 1 {
		t.Fatalf("папки %v, %v", folders, err)
	}
	os.MkdirAll(folders[0], 0o755)
	os.WriteFile(filepath.Join(folders[0], "film.mkv"), []byte("x"), 0o644)
	return folders[0]
}

// Папка загрузок «по умолчанию» (установщик при первой установке) не перезаписывает сохранённую:
// переустановка после удаления без данных и обновление сохраняют выбор (ревью C1).
func TestInstallDownloadsDefaultKeepsSaved(t *testing.T) {
	f := winsvctest.New()
	chosen, offered := filepath.Join(t.TempDir(), "Мой выбор"), filepath.Join(t.TempDir(), "Kinodom")
	o := options(t, "")
	o.DownloadsDefault = chosen
	if err := Install(ctx, f.System(), o, nolog); err != nil {
		t.Fatal(err)
	}
	if got := setting(t, o.Home, settings.KeyDownloadsDir); got != chosen {
		t.Fatalf("первая установка: %q", got)
	}
	f.Reset()
	o.DownloadsDefault = offered
	if err := Install(ctx, f.System(), o, nolog); err != nil {
		t.Fatal(err)
	}
	if got := setting(t, o.Home, settings.KeyDownloadsDir); got != chosen {
		t.Fatalf("папка загрузок перезаписана: %q", got)
	}
	if indexOf(f.Actions(), "acl.grant "+chosen) < 0 || indexOf(f.Actions(), "acl.grant "+offered) >= 0 {
		t.Fatalf("права: %v", f.Actions())
	}
}

// Первая установка не удалась после создания службы — всё сделанное убирается: ни службы (иначе
// она перезапускалась бы каждые 5 с без программы удаления), ни правил, ни ссылки (ревью I1).
func TestInstallFirstFailureRollsBack(t *testing.T) {
	f := winsvctest.New()
	o := options(t, filepath.Join(t.TempDir(), "K"))
	o.APIPort, o.ReadyTimeout = closedPort(t), 200*time.Millisecond
	if err := Install(ctx, f.System(), o, nolog); err == nil {
		t.Fatal("установка без ответа службы прошла")
	}
	if len(f.Services) != 0 || len(f.Rules) != 0 || len(f.Protocols) != 0 {
		t.Fatalf("осталось: службы %v, правила %v, ссылки %v", f.Services, f.Rules, f.Protocols)
	}
	// Починка существующей установки при отказе ничего не удаляет.
	f.Services[ServiceName] = winsvc.ServiceConfig{Name: ServiceName}
	if err := Install(ctx, f.System(), o, nolog); err == nil {
		t.Fatal("починка без ответа службы прошла")
	}
	if _, ok := f.Services[ServiceName]; !ok || len(f.Rules) != 2 {
		t.Fatalf("починка откатила установку: службы %v, правила %v", f.Services, f.Rules)
	}
}

// Починка и обновление не переводят базу на новую схему — это делает служба: если обновление
// откатится, прежняя версия найдёт базу своей версии (ревью I2). Диск загрузок отключён — обновление
// всё равно проходит, права на папку — когда диск вернётся (ревью, мелочь 14).
func TestInstallRepairKeepsSchemaAndSurvivesMissingDisk(t *testing.T) {
	f := winsvctest.New()
	o := options(t, "")
	db, err := store.Open(ctx, config.NewPaths(o.Home).DB)
	if err != nil {
		t.Fatal(err)
	}
	db.SetSetting(ctx, settings.KeyDownloadsDir, `Q:\Kinodom`)
	db.W.Exec("PRAGMA user_version = 5")
	db.Close()
	f.Services[ServiceName] = winsvc.ServiceConfig{Name: ServiceName}
	var logged []string
	if err := Install(ctx, f.System(), o, func(s string) { logged = append(logged, s) }); err != nil {
		t.Fatalf("обновление при отключённом диске: %v", err)
	}
	if indexOf(f.Actions(), `acl.grant Q:\`) >= 0 || !strings.Contains(strings.Join(logged, "\n"), `Q:\Kinodom`) {
		t.Fatalf("действия %v, журнал %v", f.Actions(), logged)
	}
	check, err := store.OpenAsIs(ctx, config.NewPaths(o.Home).DB)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var v int
	check.R.QueryRow("PRAGMA user_version").Scan(&v)
	if v != 5 {
		t.Fatalf("установка перевела базу на версию %d", v)
	}
}

// Корень диска — не папка загрузок: права раздались бы на весь диск (ревью, мелочь 15).
func TestInstallRejectsDriveRoot(t *testing.T) {
	for _, root := range []string{`D:\`, `D:\.`, "C:/"} {
		f := winsvctest.New()
		o := options(t, root)
		if err := Install(ctx, f.System(), o, nolog); err == nil || !strings.Contains(err.Error(), "весь диск") || len(f.Actions()) != 0 {
			t.Errorf("%s: %v, действия %v", root, err, f.Actions())
		}
		o = options(t, "")
		o.DownloadsDefault = root
		if err := Install(ctx, f.System(), o, nolog); err == nil || len(f.Actions()) != 0 {
			t.Errorf("по умолчанию %s: %v", root, err)
		}
	}
}
