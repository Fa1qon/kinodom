package buildcheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readDoc(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("нет %s: %v", name, err)
	}
	return string(b)
}

var reAddr = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}:\d+\b|https?://[^\s)]*\.m3u8?\b`)

// План 14Е: README — для человека, без техники: что умеет, как поставить и настроить; оговорка, что адреса
// трекеров, прокси и плейлисты не распространяются. Сборка и устройство кода — в BUILD.md и DEVELOPMENT.md
// (папка docs/ в репозиторий не входит).
func TestReadmeForPeople(t *testing.T) {
	readme := readDoc(t, "README.md")
	for _, want := range []string{"## Возможности", "## Установка", "не распространя", "BUILD.md", "DEVELOPMENT.md"} {
		if !strings.Contains(readme, want) {
			t.Errorf("в README нет «%s»", want)
		}
	}
	for _, bad := range []string{"build.ps1", "go build", "gradlew", "internal/", "vendor/", "Inno Setup"} {
		if strings.Contains(readme, bad) {
			t.Errorf("в README техника: «%s» — её место в BUILD.md или DEVELOPMENT.md", bad)
		}
	}
	if m := reTracker.FindString(readme); m != "" {
		t.Errorf("в README адрес трекера: %s", m)
	}
	if m := reAddr.FindString(readme); m != "" {
		t.Errorf("в README адрес прокси или плейлиста: %s", m)
	}
	build := readDoc(t, "BUILD.md")
	for _, want := range []string{"build.ps1", "test.ps1", "VERSION"} {
		if !strings.Contains(build, want) {
			t.Errorf("в BUILD.md нет «%s»", want)
		}
	}
	dev := readDoc(t, "DEVELOPMENT.md")
	for _, want := range []string{"internal/catalog", "internal/iptv", "internal/library", "internal/kpcat", "web/static", "android"} {
		if !strings.Contains(dev, want) {
			t.Errorf("в DEVELOPMENT.md нет «%s»", want)
		}
	}
	for name, s := range map[string]string{"BUILD.md": build, "DEVELOPMENT.md": dev} {
		if m := reTracker.FindString(s); m != "" {
			t.Errorf("в %s адрес трекера: %s", name, m)
		}
	}
}
