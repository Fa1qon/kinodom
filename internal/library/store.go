// Package library — медиатека (спека этапа 9): скачанное в Kinodom и папки заказчика одними
// карточками по номеру Кинопоиска, с просмотром и продолжением по устройствам.
package library

import (
	"time"

	"kinodom/internal/store"
)

// db — таблицы медиатеки (миграция 0013).
type db struct{ *store.DB }

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}
