//go:build !windows

package power

import "errors"

// На Android нет запросов Windows «не засыпать» (портирование сервера, план 2026-10-06):
// хранитель работает без него — поддержание экрана остаётся на приложении.
func newRequester(string) (requester, error) { return nil, errors.New("нет на этой ОС") }
