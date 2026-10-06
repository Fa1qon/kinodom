//go:build !windows

package main

// На Android сервер запускается командой run из приложения: служб и значков нет
// (портирование сервера, план 2026-10-06).

func osCommands() []command { return nil }
