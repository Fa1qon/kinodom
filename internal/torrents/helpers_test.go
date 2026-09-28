package torrents

import (
	"io"
	"log/slog"
	"testing"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newOfflineEngine — движок без сети; закрывается раньше, чем удаляются временные папки.
func newOfflineEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine(Config{DownloadsDir: t.TempDir(), StateDir: t.TempDir(), Offline: true, Log: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}
