package winsvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Close завершает процессы именно этого exe (по полному пути): копия ping.exe во временной папке —
// как kinodomw.exe значка в трее, который мешает заменить файл.
func TestProcsClose(t *testing.T) {
	src := filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Skip("нет ping.exe:", err)
	}
	exe := filepath.Join(t.TempDir(), "kinodomw.exe")
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if err := (procs{}).Close(exe); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatal("процесс не завершён")
	}
	if err := os.Remove(exe); err != nil {
		t.Fatalf("файл занят после закрытия: %v", err)
	}
}

// Значки других вошедших пользователей: их процессы администратор без SeDebugPrivilege не завершит
// (в DACL процесса пользователя группы Administrators нет) — Close включает её (второе ревью, I-3).
func TestEnableDebugPrivilege(t *testing.T) {
	if !isAdmin() {
		t.Skip("привилегия есть только у администратора")
	}
	if err := enableDebugPrivilege(); err != nil {
		t.Fatal(err)
	}
	if !debugPrivilegeEnabled(t) {
		t.Fatal("SeDebugPrivilege не включена")
	}
}

func debugPrivilegeEnabled(t *testing.T) bool {
	t.Helper()
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr("SeDebugPrivilege"), &luid); err != nil {
		t.Fatal(err)
	}
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
		t.Fatal(err)
	}
	defer tok.Close()
	var n uint32
	windows.GetTokenInformation(tok, windows.TokenPrivileges, nil, 0, &n)
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenPrivileges, &buf[0], n, &n); err != nil {
		t.Fatal(err)
	}
	privs := (*windows.Tokenprivileges)(unsafe.Pointer(&buf[0]))
	for _, p := range privs.AllPrivileges() {
		if p.Luid == luid {
			return p.Attributes&windows.SE_PRIVILEGE_ENABLED != 0
		}
	}
	return false
}
