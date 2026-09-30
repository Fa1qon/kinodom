package winsvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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
