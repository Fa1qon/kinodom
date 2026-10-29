package history

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"kinodom/internal/store"
)

// Миграция 0021 переносит прежнюю историю в журнал вкуса: досмотренное — досмотренным, место — просмотренным.
func TestBackfillFromWatchProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	// база версии 20: схема до журнала вкуса и строка истории
	d, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	raw, _ := sql.Open("sqlite", path)
	for _, q := range []string{
		`DROP TABLE view_log`, `DROP TABLE rec_ratings`, `DROP TABLE rec_marks`, `DROP TABLE rec_shown`, `DROP TABLE rec_cards`,
		`PRAGMA user_version = 20`,
		`INSERT INTO watch_progress VALUES ('tv', 'aaaa', 0, 1, 7000, 7200, 1, 1000), ('tv', 'bbbb', 0, 0.05, 300, 7200, 0, 2000)`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	raw.Close()
	d, err = store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d)
	vs, err := s.Views(context.Background(), "d:tv")
	if err != nil || len(vs) != 2 {
		t.Fatalf("перенос: %+v %v", vs, err)
	}
	for _, v := range vs {
		if v.Hash == "aaaa" && (!v.Finished || v.Seen < 0.99) || v.Hash == "bbbb" && (v.Finished || v.Seen != 0.05) {
			t.Fatalf("запись: %+v", v)
		}
	}
}
