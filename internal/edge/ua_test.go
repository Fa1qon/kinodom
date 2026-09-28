package edge

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Обновление Edge ждёт перезапуска: рядом с msedge.exe уже лежит папка новой версии, а
// запускается старый exe. UA — по версии самого exe (ревью этапа 4).
func TestUserAgentFollowsExeNotNewestDir(t *testing.T) {
	exe := ExecPath()
	if exe == "" {
		t.Skip("Edge не установлен")
	}
	dir := t.TempDir()
	copyFile(t, exe, filepath.Join(dir, "msedge.exe"))
	if err := os.Mkdir(filepath.Join(dir, "999.0.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	ua, err := userAgentOf(filepath.Join(dir, "msedge.exe"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`Chrome/(\d+)\.0\.0\.0 Safari/537\.36 Edg/(\d+)\.0\.0\.0$`).FindStringSubmatch(ua)
	if m == nil || m[1] != m[2] || m[1] == "999" || len(m[1]) < 3 {
		t.Fatalf("UA %q — нужна версия самого msedge.exe", ua)
	}
}

func TestUserAgentOfFileWithoutVersion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "msedge.exe")
	if err := os.WriteFile(p, []byte("не exe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := userAgentOf(p); err == nil {
		t.Fatal("ошибки нет")
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}
