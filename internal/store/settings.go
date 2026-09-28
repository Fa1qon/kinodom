package store

import (
	"context"
	"database/sql"
	"errors"
)

// Setting возвращает значение настройки; ok = false, если её ещё не задавали.
func (db *DB) Setting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = db.R.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func (db *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := db.W.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
