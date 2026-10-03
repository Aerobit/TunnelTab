//go:build windows

package platform

import (
	"bytes"
	"os"

	"fyne.io/systray"

	"github.com/Aerobit/TunnelTab/internal/atomicfile"
)

// trayAvailable reports whether there is a tray: Windows always has one.
func trayAvailable() bool { return true }

// setTrayIcon writes the icon into the data folder and loads it from there.
// (systray.SetIcon would write it to the system temp folder instead; the
// portable app writes only to its data folder.) Without an icon file the
// tray shows a blank icon; the menu still works.
func setTrayIcon(path string) {
	ico, err := trayIconICO(16, 20, 24, 32, 40, 48, 64)
	if err != nil || path == "" {
		return
	}
	if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, ico) {
		if err := atomicfile.WriteFile(path, ico, 0o600); err != nil {
			return
		}
	}
	systray.SetIconFromFilePath(path)
}
