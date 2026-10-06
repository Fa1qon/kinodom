//go:build !windows

package app

// protocolCheck — на Android нет ссылок kinodom:// этого ПК (портирование сервера, план
// 2026-10-06): «во внешнем плеере на этом устройстве» пульту не предлагается.
func protocolCheck() func() bool { return func() bool { return false } }
