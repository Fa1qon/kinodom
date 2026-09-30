package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "kinodom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestEmbeddedMigrationsAreConsecutive(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 || ms[0].version != 1 {
		t.Fatalf("миграции должны начинаться с 0001: %+v", ms)
	}
}

func TestOpenCreatesSchemaInWALMode(t *testing.T) {
	db := openTemp(t)
	ms, _ := loadMigrations()
	var v int
	if err := db.R.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != len(ms) {
		t.Fatalf("user_version = %d, ожидалось %d", v, len(ms))
	}
	var mode string
	if err := db.W.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, ожидался wal", mode)
	}
}

func TestMigrationBacksUpExistingDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "k.db")
	m1 := migration{1, "0001_a.sql", "CREATE TABLE a(x INTEGER);"}
	m2 := migration{2, "0002_b.sql", "CREATE TABLE b(y INTEGER);"}
	db, err := openWith(ctx, path, []migration{m1})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = openWith(ctx, path, []migration{m1, m2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := os.Stat(path + ".bak-v1"); err != nil {
		t.Fatalf("нет копии базы перед миграцией: %v", err)
	}
	if _, err := db.W.Exec("INSERT INTO b(y) VALUES(1)"); err != nil {
		t.Fatalf("миграция 2 не применилась: %v", err)
	}
}

func TestFailedMigrationKeepsPreviousVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "k.db")
	m1 := migration{1, "0001_a.sql", "CREATE TABLE a(x INTEGER);"}
	bad := migration{2, "0002_bad.sql", "CREATE TABL oops;"}
	db, err := openWith(ctx, path, []migration{m1})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := openWith(ctx, path, []migration{m1, bad}); err == nil || !strings.Contains(err.Error(), "0002_bad.sql") {
		t.Fatalf("ожидалась ошибка с именем миграции, получено %v", err)
	}
	db, err = openWith(ctx, path, []migration{m1})
	if err != nil {
		t.Fatalf("база должна остаться на версии 1: %v", err)
	}
	db.Close()
}

func TestRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "k.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.W.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(ctx, path); !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("ожидалась ErrSchemaNewer, получено %v", err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, ok, err := db.Setting(ctx, "proxy"); err != nil || ok {
		t.Fatalf("незаданная настройка: ok=%v err=%v", ok, err)
	}
	for _, v := range []string{"socks5://127.0.0.1:1080", ""} {
		if err := db.SetSetting(ctx, "proxy", v); err != nil {
			t.Fatal(err)
		}
		got, ok, err := db.Setting(ctx, "proxy")
		if err != nil || !ok || got != v {
			t.Fatalf("Setting = %q, %v, %v; ожидалось %q", got, ok, err, v)
		}
	}
}

func TestConcurrentWritesDoNotLock(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs <- db.SetSetting(ctx, fmt.Sprintf("k%d", i), "v")
		}()
		go func() {
			defer wg.Done()
			_, err := db.Problems(ctx)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("одновременная запись: %v", err)
		}
	}
}

func TestProblemKeepsSinceOnUpdate(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := db.SetProblem(ctx, "proxy", "Прокси не отвечает"); err != nil {
		t.Fatal(err)
	}
	first, _ := db.Problems(ctx)
	time.Sleep(5 * time.Millisecond)
	if err := db.SetProblem(ctx, "proxy", "Прокси не отвечает уже час"); err != nil {
		t.Fatal(err)
	}
	ps, _ := db.Problems(ctx)
	if len(ps) != 1 || ps[0].Text != "Прокси не отвечает уже час" || !ps[0].Since.Equal(first[0].Since) {
		t.Fatalf("после обновления: %+v (было %+v)", ps, first)
	}
	if err := db.ClearProblem(ctx, "proxy"); err != nil {
		t.Fatal(err)
	}
	ps, _ = db.Problems(ctx)
	if ps == nil || len(ps) != 0 {
		t.Fatalf("ожидался пустой (не nil) список, получено %#v", ps)
	}
}

func TestErrorsKeepLast200(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	for i := 1; i <= 205; i++ {
		if err := db.AddError(ctx, "iptv", fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	es, err := db.RecentErrors(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 200 || es[0].Text != "205" || es[199].Text != "6" {
		t.Fatalf("хранится %d, первая %q, последняя %q", len(es), es[0].Text, es[len(es)-1].Text)
	}
}

// OpenAsIs — база прежней версии открывается без миграций: установщик при обновлении читает и
// пишет настройки, но схему переводит только новая служба (этап 11a, ревью I2: откат обновления не
// должен оставить базу новее программы).
func TestOpenAsIsKeepsSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kinodom.db")
	ms, _ := loadMigrations()
	old, err := openWith(context.Background(), path, ms[:5])
	if err != nil {
		t.Fatal(err)
	}
	old.SetSetting(context.Background(), "downloads.dir", `D:\K`)
	old.Close()
	db, err := OpenAsIs(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	db.R.QueryRow("PRAGMA user_version").Scan(&v)
	if v != 5 {
		t.Fatalf("версия схемы %d — установщик перевёл базу", v)
	}
	if got, ok, err := db.Setting(context.Background(), "downloads.dir"); err != nil || !ok || got != `D:\K` {
		t.Fatalf("настройка: %q %v %v", got, ok, err)
	}
	if err := db.SetSetting(context.Background(), "downloads.dir", `E:\K`); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAsIs(context.Background(), filepath.Join(t.TempDir(), "нет.db")); err == nil {
		t.Fatal("несуществующая база открылась")
	}
}
