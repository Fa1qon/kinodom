package tray

import (
	"errors"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	procRegisterClassEx        = user32.NewProc("RegisterClassExW")
	procCreateWindowEx         = user32.NewProc("CreateWindowExW")
	procDefWindowProc          = user32.NewProc("DefWindowProcW")
	procDestroyWindow          = user32.NewProc("DestroyWindow")
	procGetMessage             = user32.NewProc("GetMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessage        = user32.NewProc("DispatchMessageW")
	procPostMessage            = user32.NewProc("PostMessageW")
	procPostQuitMessage        = user32.NewProc("PostQuitMessage")
	procRegisterWindowMessage  = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	procAppendMenu             = user32.NewProc("AppendMenuW")
	procSetMenuDefaultItem     = user32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	procDestroyMenu            = user32.NewProc("DestroyMenu")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procGetCursorPos           = user32.NewProc("GetCursorPos")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procCreateIconFromResource = user32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon            = user32.NewProc("DestroyIcon")
	procShellNotifyIcon        = shell32.NewProc("Shell_NotifyIconW")
)

const (
	wmNull          = 0x0000
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmApp           = 0x8000
	wmTray          = wmApp + 1 // сообщения значка: lParam — событие мыши

	nimAdd    = 0
	nimDelete = 2
	nifMsg    = 1
	nifIcon   = 2
	nifTip    = 4

	tpmRightButton = 0x0002
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	smCXSmIcon = 49
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type point struct{ x, y int32 }

type msg struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	lPrivate uint32
}

// notifyIconData — NOTIFYICONDATAW.
type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     uintptr
}

// Item — пункт меню значка. Do вызывается в своей горутине: цикл сообщений не ждёт.
type Item struct {
	Title string
	Do    func()
}

// Options — значок: файл .ico, подсказка, пункты меню (первый — жирным), двойной щелчок.
type Options struct {
	Icon    []byte
	Tip     string
	Items   []Item
	Default func()
}

var window atomic.Uintptr // окно значка: Quit посылает ему WM_CLOSE

// Run показывает значок и крутит цикл сообщений до Quit. Окно невидимое: только для сообщений значка.
func Run(o Options) error {
	runtime.LockOSThread() // окно и цикл сообщений — в одном системном потоке
	defer runtime.UnlockOSThread()
	var inst windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &inst); err != nil {
		return err
	}
	size, _, _ := procGetSystemMetrics.Call(smCXSmIcon)
	icon, err := loadIcon(o.Icon, int(size))
	if err != nil {
		return err
	}
	defer destroyIcon(icon)
	taskbarCreated, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("TaskbarCreated"))))

	nid := notifyIconData{uID: 1, uFlags: nifMsg | nifIcon | nifTip, uCallbackMessage: wmTray, hIcon: icon}
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	copy(nid.szTip[:len(nid.szTip)-1], windows.StringToUTF16(o.Tip))
	add := func() bool {
		r, _, _ := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
		return r != 0
	}

	wndProc := windows.NewCallback(func(hwnd, m, wParam, lParam uintptr) uintptr {
		switch {
		case m == wmTray && lParam == wmLButtonDblClk && o.Default != nil:
			go o.Default()
			return 0
		case m == wmTray && lParam == wmRButtonUp:
			showMenu(hwnd, o.Items)
			return 0
		case m == taskbarCreated && taskbarCreated != 0: // проводник перезапустился — значок заново
			add()
			return 0
		case m == wmClose:
			procDestroyWindow.Call(hwnd)
			return 0
		case m == wmDestroy:
			procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
			procPostQuitMessage.Call(0)
			return 0
		}
		r, _, _ := procDefWindowProc.Call(hwnd, m, wParam, lParam)
		return r
	})
	class := windows.StringToUTF16Ptr("KinodomTray")
	wc := wndClassEx{lpfnWndProc: wndProc, hInstance: inst, lpszClassName: class}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if r, _, e := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return e
	}
	hwnd, _, e := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Kinodom"))),
		0, 0, 0, 0, 0, 0, 0, uintptr(inst), 0)
	if hwnd == 0 {
		return e
	}
	nid.hWnd = hwnd
	// При входе в Windows проводник может ещё не принимать значки — повторять до 30 с. Не вышло —
	// выйти: невидимый процесс держал бы «один значок на сеанс», и ярлык значок бы не вернул.
	if !retry(add, 30, time.Second) {
		procDestroyWindow.Call(hwnd)
		return errors.New("проводник Windows не принял значок")
	}
	window.Store(hwnd)
	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // WM_QUIT или ошибка
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	window.Store(0)
	return nil
}

// Quit убирает значок и завершает Run.
func Quit() {
	if hwnd := window.Load(); hwnd != 0 {
		procPostMessage.Call(hwnd, wmClose, 0, 0)
	}
}

// showMenu — меню у курсора; выбранный пункт выполняется в своей горутине.
func showMenu(hwnd uintptr, items []Item) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	for i, it := range items {
		procAppendMenu.Call(menu, 0, uintptr(i+1), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(it.Title))))
	}
	procSetMenuDefaultItem.Call(menu, 1, 0)
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(hwnd) // иначе меню не закрывается щелчком мимо
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton|tpmNoNotify, uintptr(pt.x), uintptr(pt.y), 0, hwnd, 0)
	procPostMessage.Call(hwnd, wmNull, 0, 0)
	if i := int(cmd) - 1; i >= 0 && i < len(items) && items[i].Do != nil {
		go items[i].Do()
	}
}

// loadIcon — значок нужного размера из файла .ico (картинки PNG и BMP).
func loadIcon(ico []byte, size int) (uintptr, error) {
	off, n, got, err := pickIcon(ico, size)
	if err != nil {
		return 0, err
	}
	h, _, e := procCreateIconFromResource.Call(uintptr(unsafe.Pointer(&ico[off])), uintptr(n), 1, 0x00030000, uintptr(got), uintptr(got), 0)
	if h == 0 {
		return 0, errors.Join(errors.New("значок не создан"), e)
	}
	return h, nil
}

func destroyIcon(h uintptr) { procDestroyIcon.Call(h) }
