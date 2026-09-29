//go:build windows

package power

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procPowerCreateRequest = kernel32.NewProc("PowerCreateRequest")
	procPowerSetRequest    = kernel32.NewProc("PowerSetRequest")
	procPowerClearRequest  = kernel32.NewProc("PowerClearRequest")
)

const (
	powerRequestContextSimpleString = 0x1 // POWER_REQUEST_CONTEXT_SIMPLE_STRING
	powerRequestSystemRequired      = 1   // PowerRequestSystemRequired
)

// reasonContext — REASON_CONTEXT с простой строкой. Объединение внутри — размером с его большую
// ветку (модуль, номер строки, число строк, указатель), отсюда запас после указателя.
type reasonContext struct {
	version uint32
	flags   uint32
	reason  *uint16
	_       [16]byte
}

type winRequester struct{ h windows.Handle }

// newRequester — запрос «не засыпать» с причиной, которую видно в powercfg /requests.
func newRequester(reason string) (requester, error) {
	s, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return nil, err
	}
	rc := reasonContext{flags: powerRequestContextSimpleString, reason: s}
	r, _, e := procPowerCreateRequest.Call(uintptr(unsafe.Pointer(&rc)))
	runtime.KeepAlive(s)
	if h := windows.Handle(r); h != windows.InvalidHandle && h != 0 {
		return &winRequester{h: h}, nil
	}
	return nil, e
}

func (w *winRequester) set() error {
	if r, _, e := procPowerSetRequest.Call(uintptr(w.h), powerRequestSystemRequired); r == 0 {
		return e
	}
	return nil
}

func (w *winRequester) clear() error {
	if r, _, e := procPowerClearRequest.Call(uintptr(w.h), powerRequestSystemRequired); r == 0 {
		return e
	}
	return nil
}

func (w *winRequester) close() error { return windows.CloseHandle(w.h) }
