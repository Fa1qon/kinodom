package player

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// Player — найденный плеер.
type Player struct {
	Name string // «VLC», «MPC-HC»
	Path string
}

// ErrNoPlayer — ни VLC, ни MPC-HC на этом ПК нет.
var ErrNoPlayer = errors.New("на этом компьютере нет VLC и MPC-HC — установите VLC или скачайте .m3u8")

// finder — где искать плееры. Реестр и файлы подменяются в тестах.
type finder struct {
	reg    func(key, value string) string // HKLM/HKCU\key, value; "" — нет
	exists func(path string) bool
	env    func(string) string
}

func (f finder) vlc() string {
	for _, k := range []string{`HKLM\SOFTWARE\VideoLAN\VLC`, `HKLM\SOFTWARE\WOW6432Node\VideoLAN\VLC`} {
		if p := f.reg(k, ""); p != "" && f.exists(p) {
			return p
		}
	}
	return f.first(`VideoLAN\VLC\vlc.exe`)
}

func (f finder) mpc() string {
	for _, k := range []string{`HKCU\Software\MPC-HC\MPC-HC`, `HKLM\SOFTWARE\MPC-HC\MPC-HC`} {
		if p := f.reg(k, "ExePath"); p != "" && f.exists(p) {
			return p
		}
	}
	return f.first(`MPC-HC\mpc-hc64.exe`, `MPC-HC\mpc-hc.exe`, `K-Lite Codec Pack\MPC-HC64\mpc-hc64.exe`, `K-Lite Codec Pack\MPC-HC\mpc-hc.exe`)
}

// first — первый существующий файл среди стандартных папок программ (64- и 32-битной).
func (f finder) first(rel ...string) string {
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		base := f.env(env)
		if base == "" {
			continue
		}
		for _, r := range rel {
			if p := filepath.Join(base, r); f.exists(p) {
				return p
			}
		}
	}
	return ""
}

// find — плеер по настройке: auto — VLC, если нет — MPC-HC; vlc или mpc-hc — только он.
func (f finder) find(choice string) (Player, error) {
	vlc := func() (Player, bool) { p := f.vlc(); return Player{"VLC", p}, p != "" }
	mpc := func() (Player, bool) { p := f.mpc(); return Player{"MPC-HC", p}, p != "" }
	order := []func() (Player, bool){vlc, mpc}
	switch choice {
	case "vlc":
		order = order[:1]
	case "mpc-hc":
		order = order[1:]
	}
	for _, try := range order {
		if p, ok := try(); ok {
			return p, nil
		}
	}
	return Player{}, ErrNoPlayer
}

// Find — плеер этого ПК по настройке player (auto, vlc, mpc-hc).
func Find(choice string) (Player, error) {
	return finder{reg: regString, exists: fileExists, env: os.Getenv}.find(choice)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Launch запускает плеер с потоком. Аргументы — отдельными строками, без cmd: ничего из ссылки не
// становится командой (основная спека, раздел 14). VLC получает и название — для заголовка окна;
// startSec > 0 — открыть с этого места (спека этапа 8, раздел 7.3).
func Launch(p Player, streamURL, title string, startSec int) error {
	return exec.Command(p.Path, launchArgs(p, streamURL, title, startSec)...).Start()
}

func launchArgs(p Player, streamURL, title string, startSec int) []string {
	args := []string{streamURL}
	if p.Name == "VLC" && title != "" {
		args = append(args, "--meta-title="+title)
	}
	if startSec > 0 {
		switch p.Name {
		case "VLC":
			args = append(args, "--start-time="+strconv.Itoa(startSec))
		case "MPC-HC":
			args = append(args, "/start", strconv.Itoa(startSec*1000))
		}
	}
	return args
}
