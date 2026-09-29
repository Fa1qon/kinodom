package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsArgs(t *testing.T) {
	for _, args := range [][]string{{"run", "лишнее"}, {"run", "--port", "1"}} {
		var out, errOut strings.Builder
		if code := runCLI(args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "использование") {
			t.Fatalf("%v: код %d, %q", args, code, errOut.String())
		}
	}
}

// --downloads — папка загрузок для проверки вживую, мимо настройки службы.
func TestRunDownloadsFlag(t *testing.T) {
	o, err := runOptions([]string{"--downloads", `D:\Проверка`})
	if err != nil || o.DownloadsDir != `D:\Проверка` || !o.Console {
		t.Fatalf("%+v, %v", o, err)
	}
}

// Порт занят другой программой только по IPv4 — run должен выйти с кодом 1 и понятной причиной.
func TestRunExitsWhenPortIsBusy(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	home := t.TempDir()
	t.Setenv("KINODOM_HOME", home)
	cfg := fmt.Sprintf(`{"apiPort": %d, "torrentPort": 42000}`, ln.Addr().(*net.TCPAddr).Port)
	if err := os.WriteFile(filepath.Join(home, "kinodom.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	code := runCLI([]string{"run"}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "занят") {
		t.Fatalf("код %d, stderr %q", code, errOut.String())
	}
	if strings.Contains(out.String(), "работает") {
		t.Fatalf("при занятом порте нельзя писать «работает»: %q", out.String())
	}
}
