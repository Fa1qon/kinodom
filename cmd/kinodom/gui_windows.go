package main

import "golang.org/x/sys/windows"

// showMessage — окно Windows с предупреждением.
func showMessage(text string) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("Kinodom")
	windows.MessageBox(0, t, c, windows.MB_OK|windows.MB_ICONWARNING|windows.MB_SETFOREGROUND)
}
