// Package store — база SQLite сервера: открытие, миграции и общие таблицы
// (настройки, проблемы, ошибки). Таблицы модулей добавляются миграциями в этом же
// пакете, чтобы номера версий шли одной последовательностью.
package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // драйвер "sqlite" на чистом Go, без CGO
)

// DB — два пула к одному файлу.
type DB struct {
	W    *sql.DB // единственное соединение на запись: SQLite всё равно пишет по одному
	R    *sql.DB // несколько соединений на чтение: в режиме WAL чтение не ждёт запись
	Path string
}

// Параметры соединений (разбирает драйвер modernc). busy_timeout — сколько ждать занятую
// базу вместо мгновенной ошибки «database is locked»; _txlock=immediate — транзакция на
// запись сразу берёт блокировку и не упирается в неё посередине.
const (
	writeParams = "_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_txlock=immediate"
	readParams  = "_pragma=busy_timeout(10000)&_pragma=foreign_keys(ON)&_pragma=query_only(ON)"
)

// Open открывает (или создаёт) базу и приводит схему к последней версии.
func Open(ctx context.Context, path string) (*DB, error) {
	ms, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	return openWith(ctx, path, ms)
}

func openWith(ctx context.Context, path string, ms []migration) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w, err := sql.Open("sqlite", path+"?"+writeParams)
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	r, err := sql.Open("sqlite", path+"?"+readParams)
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(4)
	db := &DB{W: w, R: r, Path: path}
	if err := db.migrate(ctx, ms); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Close() error {
	return errors.Join(db.R.Close(), db.W.Close())
}
