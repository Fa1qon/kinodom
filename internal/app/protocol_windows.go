//go:build windows

package app

import (
	"kinodom/internal/winsvc"
)

// protocolCheck — зарегистрирован ли обработчик kinodom:// на этом ПК: «Открыть во внешнем
// плеере» на этом же компьютере ведёт по ссылке без ссылки на файл (спека этапа 8).
func protocolCheck() func() bool { return winsvc.KinodomProtocol }
