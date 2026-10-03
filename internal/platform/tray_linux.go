//go:build linux

package platform

import (
	"fyne.io/systray"
	"github.com/godbus/dbus/v5"
)

// trayAvailable reports whether the desktop has a tray: a D-Bus session bus
// with a StatusNotifierWatcher (KDE, XFCE, Cinnamon, GNOME with the
// AppIndicator extension, …).
//
// The connection is made with auto-launch off: otherwise godbus may start
// dbus-launch when no session bus is known, and TunnelTab runs no external
// programs but the browser opener. A bus found this way is put in
// DBUS_SESSION_BUS_ADDRESS by godbus, so systray's own connection finds it
// without auto-launching either.
func trayAvailable() bool {
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return false
	}
	defer conn.Close()
	if err := conn.Auth(nil); err != nil {
		return false
	}
	if err := conn.Hello(); err != nil {
		return false
	}
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}

// setTrayIcon sends the icon over D-Bus from memory (no file needed).
func setTrayIcon(string) {
	if icon, err := trayIconPNGSized(64); err == nil {
		systray.SetIcon(icon)
	}
}
