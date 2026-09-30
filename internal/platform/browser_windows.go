//go:build windows

package platform

import (
	"os/exec"
	"syscall"
	"unsafe"
)

// OpenBrowser opens url in the default browser. The URL is passed as a
// single argument (no shell), so it can't inject commands.
func OpenBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
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
