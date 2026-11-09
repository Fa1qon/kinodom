package updates

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// launch запускает установщик тихо и отдельно от службы: он её остановит, и вместе с ней не должен умереть
// (служба — в объекте задания «убить детей при закрытии», internal/edge). /AUTOUPDATE — без значка в трее:
// установщик идёт от имени службы, и значок оказался бы в чужом сеансе.
func launch(path string) error {
	args := []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/AUTOUPDATE=1"}
	start := func(flags uint32) error {
		cmd := exec.Command(path, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	base := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	err := start(base | windows.CREATE_BREAKAWAY_FROM_JOB)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		err = start(base) // чужое задание без права выхода — пробуем как есть
	}
	return err
}

const canInstall = true
