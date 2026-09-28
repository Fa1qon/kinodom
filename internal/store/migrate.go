package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// ErrSchemaNewer — базу уже обновила более новая версия Kinodom; старая с ней не работает.
var ErrSchemaNewer = errors.New("схема базы новее, чем знает эта версия Kinodom")

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations читает migrations/NNNN_имя.sql; номера должны идти подряд с 1.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("миграция %s: имя должно начинаться с номера", e.Name())
		}
		body, err := fs.ReadFile(migrationFiles, "migrations/"+e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("миграции должны идти подряд с 1: на месте %d стоит %s", i+1, m.name)
		}
	}
	return ms, nil
}

// migrate применяет недостающие миграции. Номер версии хранится в PRAGMA user_version.
func (db *DB) migrate(ctx context.Context, ms []migration) error {
	if len(ms) == 0 {
		return nil
	}
	latest := ms[len(ms)-1].version
	var cur int
	if err := db.W.QueryRowContext(ctx, "PRAGMA user_version").Scan(&cur); err != nil {
		return fmt.Errorf("версия схемы: %w", err)
	}
	if cur > latest {
		return fmt.Errorf("%w: в базе %d, эта версия знает до %d", ErrSchemaNewer, cur, latest)
	}
	if cur == latest {
		return nil
	}
	if cur > 0 {
		if err := db.backup(ctx, fmt.Sprintf("%s.bak-v%d", db.Path, cur)); err != nil {
			return fmt.Errorf("копия базы перед миграцией: %w", err)
		}
	}
	for _, m := range ms[cur:] {
		if err := db.apply(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// apply выполняет одну миграцию в транзакции вместе со сменой номера версии:
// упавшая миграция не оставляет базу наполовину изменённой.
func (db *DB) apply(ctx context.Context, m migration) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // после Commit ничего не делает
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("миграция %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("миграция %s: %w", m.name, err)
	}
	return tx.Commit()
}

// backup делает согласованную копию базы (VACUUM INTO), заменяя старую копию с тем же именем.
func (db *DB) backup(ctx context.Context, dst string) error {
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, err := db.W.ExecContext(ctx, "VACUUM INTO ?", dst)
	return err
}
