package store

import (
	"context"
	"time"
)

// Problem — неполадка, которую надо показать людям: баннер в пульте и на телевизоре.
type Problem struct {
	ID    string    `json:"id"`
	Text  string    `json:"text"`
	Since time.Time `json:"since"`
}

// SetProblem включает проблему или обновляет её текст; время появления не меняется.
func (db *DB) SetProblem(ctx context.Context, id, text string) error {
	_, err := db.W.ExecContext(ctx,
		`INSERT INTO problems(id, text, since) VALUES(?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET text = excluded.text`, id, text, time.Now().UnixMilli())
	return err
}

func (db *DB) ClearProblem(ctx context.Context, id string) error {
	_, err := db.W.ExecContext(ctx, "DELETE FROM problems WHERE id = ?", id)
	return err
}

// Problems — все текущие проблемы, старые первыми. Пустой список — не nil (в JSON это []).
func (db *DB) Problems(ctx context.Context) ([]Problem, error) {
	rows, err := db.R.QueryContext(ctx, "SELECT id, text, since FROM problems ORDER BY since, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ps := []Problem{}
	for rows.Next() {
		var p Problem
		var ms int64
		if err := rows.Scan(&p.ID, &p.Text, &ms); err != nil {
			return nil, err
		}
		p.Since = time.UnixMilli(ms)
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

// ErrorRecord — запись журнала ошибок модулей для страницы «Состояние».
type ErrorRecord struct {
	At     time.Time `json:"at"`
	Module string    `json:"module"`
	Text   string    `json:"text"`
}

// keepErrors — сколько последних ошибок хранить (спека, раздел 13).
const keepErrors = 200

func (db *DB) AddError(ctx context.Context, module, text string) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO errors(at, module, text) VALUES(?, ?, ?)",
		time.Now().UnixMilli(), module, text); err != nil {
		return err
	}
	// Всё, что старше 200-й записи с конца, удаляется.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM errors WHERE id <= (SELECT id FROM errors ORDER BY id DESC LIMIT 1 OFFSET ?)`,
		keepErrors); err != nil {
		return err
	}
	return tx.Commit()
}

// RecentErrors — последние ошибки, новые первыми.
func (db *DB) RecentErrors(ctx context.Context, limit int) ([]ErrorRecord, error) {
	rows, err := db.R.QueryContext(ctx, "SELECT at, module, text FROM errors ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	es := []ErrorRecord{}
	for rows.Next() {
		var e ErrorRecord
		var ms int64
		if err := rows.Scan(&ms, &e.Module, &e.Text); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(ms)
		es = append(es, e)
	}
	return es, rows.Err()
}
