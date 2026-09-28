package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultHomeFromEnv(t *testing.T) {
	t.Setenv(EnvHome, `D:\kd-test`)
	if got := DefaultHome(); got != `D:\kd-test` {
		t.Fatalf("DefaultHome() = %q", got)
	}
}

func TestDefaultHomeInProgramData(t *testing.T) {
	t.Setenv(EnvHome, "")
	t.Setenv("ProgramData", `C:\PD`)
	if got := DefaultHome(); got != `C:\PD\Kinodom` {
		t.Fatalf("DefaultHome() = %q", got)
	}
}

func TestNewPathsLayout(t *testing.T) {
	p := NewPaths(`C:\K`)
	cases := map[string][2]string{
		"Bootstrap":   {p.Bootstrap, `C:\K\kinodom.json`},
		"Data":        {p.Data, `C:\K\data`},
		"DB":          {p.DB, `C:\K\data\kinodom.db`},
		"Logs":        {p.Logs, `C:\K\data\logs`},
		"EdgeProfile": {p.EdgeProfile, `C:\K\data\edge-profile`},
		"Images":      {p.Images, `C:\K\data\images`},
		"Torrent":     {p.Torrent, `C:\K\data\torrent`},
	}
	for name, c := range cases {
		if c[0] != c[1] {
			t.Errorf("%s = %q, ожидалось %q", name, c[0], c[1])
		}
	}
}

func TestEnsureCreatesDirs(t *testing.T) {
	p := NewPaths(filepath.Join(t.TempDir(), "Kinodom"))
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{p.Home, p.Data, p.Logs, p.Images, p.Torrent} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("папка %s не создана: %v", d, err)
		}
	}
}

func TestLoadBootstrapCreatesDefaultFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kinodom.json")
	b, err := LoadBootstrap(path)
	if err != nil {
		t.Fatal(err)
	}
	if b != DefaultBootstrap() {
		t.Fatalf("получено %+v, ожидались значения по умолчанию", b)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("файл не создан: %v", err)
	}
}

func TestLoadBootstrapFillsMissingFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kinodom.json")
	if err := os.WriteFile(path, []byte(`{"apiPort": 9000}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := LoadBootstrap(path)
	if err != nil {
		t.Fatal(err)
	}
	if b.APIPort != 9000 || b.TorrentPort != 42000 {
		t.Fatalf("получено %+v", b)
	}
}

func TestLoadBootstrapBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kinodom.json")
	os.WriteFile(path, []byte(`{`), 0o644)
	_, err := LoadBootstrap(path)
	if err == nil || !strings.Contains(err.Error(), "kinodom.json") {
		t.Fatalf("ожидалась ошибка с именем файла, получено %v", err)
	}
}

func TestLoadBootstrapBadPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kinodom.json")
	os.WriteFile(path, []byte(`{"apiPort": 70000}`), 0o644)
	_, err := LoadBootstrap(path)
	if err == nil || !strings.Contains(err.Error(), "порты") {
		t.Fatalf("ожидалась ошибка про порты, получено %v", err)
	}
}

// PowerShell 5.1 (Set-Content -Encoding UTF8) и старый Блокнот пишут UTF-8 с BOM.
func TestLoadBootstrapAcceptsBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kinodom.json")
	os.WriteFile(path, append([]byte("\xEF\xBB\xBF"), []byte(`{"apiPort": 9001}`)...), 0o644)
	b, err := LoadBootstrap(path)
	if err != nil || b.APIPort != 9001 {
		t.Fatalf("файл с BOM: %+v, %v", b, err)
	}
}
