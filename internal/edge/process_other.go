//go:build !windows

package edge

import "errors"

// На Android нет скрытого Edge (портирование сервера, план 2026-10-06): модуль не включается —
// Rutracker работает по API и cookie из поиска, без пропуска Cloudflare.

// BindChildren — привязка дочерних процессов к заданию Windows; на Android нечего привязывать.
func BindChildren() error { return nil }

// profileBusy — профиль занят другим Edge; на Android профилей нет.
func profileBusy(string) bool { return false }

var errNoProcess = errors.New("процессы Edge на этой ОС не ищутся")
