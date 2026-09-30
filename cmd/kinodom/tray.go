package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/sys/windows"

	"kinodom/internal/config"
	"kinodom/internal/setup"
	"kinodom/internal/tray"
	"kinodom/internal/winsvc"
)

// cmdTray — значок в трее (спека этапа 11a, раздел 5.2): kinodomw.exe tray при входе в Windows,
// kinodomw.exe tray --open — ярлык «Kinodom» и конец установки (запустить сервер, значок и пульт).
func cmdTray(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tray", flag.ContinueOnError)
	fs.SetOutput(stderr)
	open := fs.Bool("open", false, "открыть пульт в браузере")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "использование: kinodomw tray [--open]")
		return 2
	}
	return trayMain(realTray(stderr), *open)
}

// trayDeps — всё системное, что нужно значку (тесты подменяют).
type trayDeps struct {
	state   func() (string, error)
	start   func() error
	halt    func() error
	ready   func() // дождаться ответа пульта (сервер мог только что запуститься)
	openURL func()
	single  func() (release func(), first bool) // один значок на сеанс Windows
	run     func(items []tray.Item, def func()) error
	quit    func()
	message func(text string)
}

func trayMain(d trayDeps, open bool) int {
	st, err := d.state()
	switch {
	case err != nil:
		d.message("Не удалось узнать, работает ли сервер Kinodom: " + err.Error())
		return 1
	case st == winsvc.StateNotFound:
		d.message("Kinodom не установлен на этом компьютере.")
		return 1
	case st != winsvc.StateRunning:
		if err := d.start(); err != nil {
			d.message("Сервер Kinodom не запускается: " + err.Error())
			return 1
		}
	}
	if open {
		d.ready()
		d.openURL()
	}
	release, first := d.single()
	if !first {
		return 0 // значок уже есть
	}
	defer release()
	items := []tray.Item{
		{Title: "Открыть Kinodom", Do: d.openURL},
		{Title: "Выход", Do: func() {
			if err := d.halt(); err != nil {
				d.message("Сервер Kinodom не остановился: " + err.Error())
				return
			}
			d.quit()
		}},
	}
	if err := d.run(items, d.openURL); err != nil {
		d.message("Значок Kinodom не показался: " + err.Error())
		return 1
	}
	return 0
}

// realTray — значок этого ПК: служба Kinodom, пульт на порту из kinodom.json.
func realTray(stderr io.Writer) trayDeps {
	boot := config.DefaultBootstrap()
	if b, err := config.LoadBootstrap(config.NewPaths(config.DefaultHome()).Bootstrap); err == nil {
		boot = b
	}
	url := "http://localhost:" + strconv.Itoa(boot.APIPort)
	scm := newSystem().SCM
	return trayDeps{
		state: func() (string, error) { return scm.State(setup.ServiceName) },
		start: func() error { return scm.Start(setup.ServiceName) },
		halt:  func() error { return scm.Halt(setup.ServiceName, setup.StopWait) },
		ready: func() {
			c := &http.Client{Timeout: 2 * time.Second}
			for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
				if resp, err := c.Get("http://127.0.0.1:" + strconv.Itoa(boot.APIPort) + "/api/v1/status"); err == nil {
					resp.Body.Close()
					return
				}
			}
		},
		openURL: func() {
			windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(url), nil, nil, windows.SW_SHOWNORMAL)
		},
		single: func() (func(), bool) {
			h, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(`Local\KinodomTray`))
			if err != nil { // ERROR_ALREADY_EXISTS — значок уже показан в этом сеансе
				if h != 0 {
					windows.CloseHandle(h)
				}
				return func() {}, false
			}
			return func() { windows.CloseHandle(h) }, true
		},
		run: func(items []tray.Item, def func()) error {
			return tray.Run(tray.Options{Icon: tray.Icon, Tip: "Kinodom", Items: items, Default: def})
		},
		quit: tray.Quit,
		message: func(text string) {
			fmt.Fprintln(stderr, text)
			if gui != "" {
				messageBox(text)
			}
		},
	}
}
