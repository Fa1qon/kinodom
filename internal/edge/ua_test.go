package edge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUserAgentFromNewestVersionDir(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"153.0.3000.1", "154.0.4258.37", "Installer"} {
		os.Mkdir(filepath.Join(dir, d), 0o755)
	}
	os.WriteFile(filepath.Join(dir, "155.0.0.0"), nil, 0o644) // файл, а не папка версии
	got, err := userAgentFrom(dir)
	want := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/154.0.0.0 Safari/537.36 Edg/154.0.0.0"
	if err != nil || got != want {
		t.Fatalf("UA %q, %v", got, err)
	}
}

func TestUserAgentWithoutVersionDir(t *testing.T) {
	if _, err := userAgentFrom(t.TempDir()); err == nil {
		t.Fatal("ошибки нет")
	}
}
