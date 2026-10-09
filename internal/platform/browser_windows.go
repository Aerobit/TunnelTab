//go:build windows

package platform

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OpenBrowser opens url in the default browser. Windows is asked directly
// (ShellExecute), so no other program is started, and only a link to the
// local dashboard is accepted (see checkDashboardURL).
func OpenBrowser(url string) error {
	if err := checkDashboardURL(url); err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

// ShowError reports a fatal startup problem. The Windows build has no
// console window, so a message box is used.
func ShowError(title, msg string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	box := user32.NewProc("MessageBoxW")
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(msg)
	const mbIconError = 0x10
	box.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbIconError)
}
