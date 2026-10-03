//go:build !windows && !linux

package platform

// Other systems have no tray support (yet).
func trayAvailable() bool { return false }

func runTray(TrayMenu, <-chan struct{}) {}
