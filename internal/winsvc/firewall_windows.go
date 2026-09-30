package winsvc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// firewall — правила через netsh advfirewall без cmd. Командная строка собирается целиком: netsh
// ждёт name="…" одним словом, а обычное экранирование Go взяло бы в кавычки всё «name=…».
type firewall struct{}

func netshPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "netsh.exe")
}

// netsh выполняет команду; код выхода ≠ 0 — *exec.ExitError с выводом в тексте.
func netsh(args string) error {
	cmd := exec.Command(netshPath())
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "netsh " + args, HideWindow: true}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("netsh: код %d: %s", ee.ExitCode(), strings.TrimSpace(string(out)))
	}
	return err
}

// ruleArgs — «add rule» одного протокола правила.
func ruleArgs(r FirewallRule, protocol string) string {
	return "advfirewall firewall add rule name=" + quote(r.Name) + " dir=in action=allow protocol=" + protocol +
		" localport=" + strconv.Itoa(r.Port) + " remoteip=" + r.Remote + " program=" + quote(r.Program) +
		" profile=any enable=yes"
}

func quote(s string) string { return `"` + s + `"` }

func (f firewall) Set(r FirewallRule) error {
	if strings.ContainsRune(r.Name, '"') || strings.ContainsRune(r.Program, '"') {
		return fmt.Errorf("кавычки в имени правила или пути программы")
	}
	if err := f.Delete(r.Name); err != nil {
		return err
	}
	for _, p := range r.Protocols {
		if err := netsh(ruleArgs(r, p)); err != nil {
			return err
		}
	}
	return nil
}

func (f firewall) Delete(name string) error {
	ok, err := f.Exists(name)
	if err != nil || !ok {
		return err
	}
	return netsh("advfirewall firewall delete rule name=" + quote(name))
}

// Exists — «show rule» отвечает кодом 1, если правил с таким именем нет.
func (firewall) Exists(name string) (bool, error) {
	err := netsh("advfirewall firewall show rule name=" + quote(name))
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "код 1:") {
		return false, nil
	}
	return false, err
}
