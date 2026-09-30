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
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/config"
	"kinodom/internal/settings"
	"kinodom/internal/store"
	"kinodom/internal/torrents"
	"kinodom/internal/torrents/torrenttest"
	"kinodom/internal/winsvc"
)

var ctx = context.Background()

// fakeSys — система на подделках: записывает действия по порядку.
type fakeSys struct {
	mu        sync.Mutex
	acts      []string
	admin     bool
	services  map[string]winsvc.ServiceConfig
	running   map[string]bool
	stuck     bool   // служба не останавливается
	portOwner string // "" — порт свободен
	rules     map[string]winsvc.FirewallRule
	protocols map[string]string
}

func newFakeSys() *fakeSys {
	return &fakeSys{admin: true, services: map[string]winsvc.ServiceConfig{}, running: map[string]bool{},
		rules: map[string]winsvc.FirewallRule{}, protocols: map[string]string{}}
}

func (f *fakeSys) act(format string, args ...any) {
	f.acts = append(f.acts, fmt.Sprintf(format, args...))
}

func (f *fakeSys) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.acts)
}

func (f *fakeSys) reset() { f.mu.Lock(); f.acts = nil; f.mu.Unlock() }

func (f *fakeSys) system() winsvc.System {
	return winsvc.System{SCM: fakeSCM{f}, ACL: fakeACL{f}, Firewall: fakeFW{f}, Registry: fakeReg{f}, Ports: fakePorts{f},
		IsAdmin: func() bool { return f.admin }}
}

type fakeSCM struct{ *fakeSys }

func (s fakeSCM) Install(c winsvc.ServiceConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.act("scm.install %s", c.Name)
	s.services[c.Name] = c
	return nil
}

func (s fakeSCM) Update(c winsvc.ServiceConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.act("scm.update %s", c.Name)
	s.services[c.Name] = c
	return nil
}

func (s fakeSCM) Exists(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.services[name]
	return ok, nil
}

func (s fakeSCM) Start(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.act("scm.start %s", name)
	s.running[name] = true
	return nil
}

func (s fakeSCM) Stop(name string, wait time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.services[name]; !ok {
		return winsvc.ErrNotInstalled
	}
	s.act("scm.stop %s", name)
	if s.stuck && s.running[name] {
		return errors.New("служба не остановилась за 60 с")
	}
	s.running[name] = false
	return nil
}

func (s fakeSCM) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.act("scm.delete %s", name)
	delete(s.services, name)
	return nil
}

func (s fakeSCM) State(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.services[name]; !ok {
		return winsvc.StateNotFound, nil
	}
	if s.running[name] {
		return winsvc.StateRunning, nil
	}
	return winsvc.StateStopped, nil
}

type fakeACL struct{ *fakeSys }

func (a fakeACL) Grant(path, account string, write bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	mode := "read"
	if write {
		mode = "write"
	}
	a.act("acl.grant %s %s %s", path, account, mode)
	return nil
}

func (a fakeACL) Restrict(path string, accounts []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.act("acl.restrict %s %s", path, strings.Join(accounts, ","))
	return nil
}

type fakeFW struct{ *fakeSys }

func (w fakeFW) Set(r winsvc.FirewallRule) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.act("fw.set %s %s %d %s %s", r.Name, strings.Join(r.Protocols, ","), r.Port, r.Remote, r.Program)
	w.rules[r.Name] = r
	return nil
}

func (w fakeFW) Delete(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.act("fw.delete %s", name)
	delete(w.rules, name)
	return nil
}

func (w fakeFW) Exists(name string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.rules[name]
	return ok, nil
}

type fakeReg struct{ *fakeSys }

func (r fakeReg) SetProtocol(scheme, command string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.act("reg.set %s %s", scheme, command)
	r.protocols[scheme] = command
	return nil
}

func (r fakeReg) DeleteProtocol(scheme string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.act("reg.delete %s", scheme)
	delete(r.protocols, scheme)
	return nil
}

func (r fakeReg) Protocol(scheme string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.protocols[scheme], nil
}

type fakePorts struct{ *fakeSys }

func (p fakePorts) Owner(int) (bool, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.portOwner != "", p.portOwner, nil
}

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
	f := newFakeSys()
	dl := filepath.Join(t.TempDir(), "Kinodom")
	o := options(t, dl)
	if err := Install(ctx, f.system(), o, nolog); err != nil {
		t.Fatal(err)
	}
	acts := f.actions()
	data := config.NewPaths(o.Home).Data
	exe := filepath.Join(prog, "kinodom.exe")
	want := []string{
		"scm.install Kinodom",
		"acl.restrict " + data + " S-1-5-18,S-1-5-32-544,NT SERVICE\\Kinodom",
		"acl.grant " + dl + " NT SERVICE\\Kinodom write",
		fmt.Sprintf("fw.set Kinodom — пульт TCP %d LocalSubnet %s", o.APIPort, exe),
		"fw.set Kinodom — раздачи TCP,UDP 42000 Any " + exe,
		`reg.set kinodom "` + filepath.Join(prog, "kinodomw.exe") + `" open "%1"`,
		"scm.start Kinodom",
	}
	if !slices.Equal(acts, want) {
		t.Fatalf("действия:\n%s\nждали:\n%s", strings.Join(acts, "\n"), strings.Join(want, "\n"))
	}
	c := f.services["Kinodom"]
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
	f := newFakeSys()
	dl := filepath.Join(t.TempDir(), "Kinodom")
	o := options(t, dl)
	if err := Install(ctx, f.system(), o, nolog); err != nil {
		t.Fatal(err)
	}
	f.reset()
	o.Downloads = ""
	if err := Install(ctx, f.system(), o, nolog); err != nil {
		t.Fatal(err)
	}
	acts := f.actions()
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
	f := newFakeSys()
	f.admin = false
	err := Install(ctx, f.system(), options(t, filepath.Join(t.TempDir(), "K")), nolog)
	if !errors.Is(err, ErrNotAdmin) || len(f.actions()) != 0 {
		t.Fatalf("err %v, действия %v", err, f.actions())
	}
	if err := Uninstall(ctx, f.system(), false, t.TempDir(), nolog); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("удаление: %v", err)
	}
}

// Порт пульта занят другой программой (Review Focus 3): отказ с её названием, ничего не сломано;
// порт освободили — установка проходит.
func TestInstallPortBusy(t *testing.T) {
	f := newFakeSys()
	f.portOwner = "other-server.exe"
	o := options(t, filepath.Join(t.TempDir(), "K"))
	err := Install(ctx, f.system(), o, nolog)
	if !errors.Is(err, ErrPortBusy) || !strings.Contains(err.Error(), "other-server.exe") ||
		!strings.Contains(err.Error(), strconv.Itoa(o.APIPort)) {
		t.Fatalf("err %v", err)
	}
	if acts := f.actions(); len(acts) != 0 {
		t.Fatalf("при занятом порте что-то сделано: %v", acts)
	}
	f.portOwner = ""
	if err := Install(ctx, f.system(), o, nolog); err != nil {
		t.Fatalf("после освобождения порта: %v", err)
	}
}

// Сетевая папка загрузок — отказ до любых действий.
func TestInstallRejectsNetworkDownloads(t *testing.T) {
	f := newFakeSys()
	err := Install(ctx, f.system(), options(t, `\\server\share\Kinodom`), nolog)
	if err == nil || !strings.Contains(err.Error(), "сетев") || len(f.actions()) != 0 {
		t.Fatalf("err %v, действия %v", err, f.actions())
	}
}

// Служба не ответила за отведённое время — отказ с последней ошибкой из журнала сервера.
func TestInstallServiceNotReady(t *testing.T) {
	f := newFakeSys()
	o := options(t, filepath.Join(t.TempDir(), "K"))
	o.APIPort, o.ReadyTimeout = closedPort(t), 300*time.Millisecond
	logs := config.NewPaths(o.Home).Logs
	os.MkdirAll(logs, 0o755)
	os.WriteFile(filepath.Join(logs, "kinodom.log"),
		[]byte("time=2026-09-30T10:00:00 level=INFO msg=старт\ntime=2026-09-30T10:00:01 level=ERROR msg=\"Kinodom не запустился\" err=\"база: диск полон\"\n"), 0o644)
	err := Install(ctx, f.system(), o, nolog)
	if err == nil || !strings.Contains(err.Error(), "база: диск полон") {
		t.Fatalf("err %v", err)
	}
}

// Удаление без данных: служба, правила и ссылка убраны; настройки, база и скачанное — на месте.
func TestUninstallKeepsData(t *testing.T) {
	f := newFakeSys()
	dl := filepath.Join(t.TempDir(), "Kinodom")
	o := options(t, dl)
	if err := Install(ctx, f.system(), o, nolog); err != nil {
		t.Fatal(err)
	}
	f.reset()
	if err := Uninstall(ctx, f.system(), false, o.Home, nolog); err != nil {
		t.Fatal(err)
	}
	want := []string{"scm.stop Kinodom", "scm.delete Kinodom", "fw.delete Kinodom — пульт", "fw.delete Kinodom — раздачи", "reg.delete kinodom"}
	if acts := f.actions(); !slices.Equal(acts, want) {
		t.Fatalf("действия %v", acts)
	}
	if _, err := os.Stat(config.NewPaths(o.Home).DB); err != nil {
		t.Fatalf("база удалена: %v", err)
	}
	if _, err := os.Stat(dl); err != nil {
		t.Fatalf("папка загрузок удалена: %v", err)
	}
	// Второй раз — службы уже нет: не ошибка.
	if err := Uninstall(ctx, f.system(), false, o.Home, nolog); err != nil {
		t.Fatalf("повторное удаление: %v", err)
	}
}

// Удаление с данными (Review Focus 4): папки раздач из реестра и данные удаляются, чужие файлы в
// папке загрузок остаются; пустая папка загрузок удаляется.
func TestUninstallPurgeKeepsForeign(t *testing.T) {
	for _, foreign := range []bool{true, false} {
		f := newFakeSys()
		dl := filepath.Join(t.TempDir(), "Kinodom")
		o := options(t, dl)
		if err := Install(ctx, f.system(), o, nolog); err != nil {
			t.Fatal(err)
		}
		folder := addTorrent(t, o.Home, dl)
		mine := filepath.Join(dl, "мои фото.jpg")
		if foreign {
			os.WriteFile(mine, []byte("x"), 0o644)
		}
		if err := Uninstall(ctx, f.system(), true, o.Home, nolog); err != nil {
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
