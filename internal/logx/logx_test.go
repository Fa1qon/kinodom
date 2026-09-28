package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritesToFile(t *testing.T) {
	dir := t.TempDir()
	log, closer, err := New(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("проверка журнала", "module", "test")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "kinodom.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "проверка журнала") {
		t.Fatalf("в журнале нет записи: %s", data)
	}
}
