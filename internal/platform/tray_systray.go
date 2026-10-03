//go:build windows || linux

package platform

import (
	"time"

	"fyne.io/systray"
)

// runTray shows the icon and menu until stop is closed. It blocks on the
// main thread (systray pins the main goroutine to it).
func runTray(m TrayMenu, stop <-chan struct{}) {
	open := debounce(time.Second, m.Open)
	// Set before Run: on Linux the tray reads it when it starts (left-click
	// opens the dashboard, right-click shows the menu).
	systray.SetOnTapped(func() { go open() })
	systray.Run(func() {
		setTrayIcon(m.IconFile)
		systray.SetTooltip(m.Tooltip)
		openItem := systray.AddMenuItem("Open dashboard", "Open TunnelTab in your browser")
		lockItem := systray.AddMenuItem("Lock now", "Lock the vault")
		systray.AddSeparator()
		quitItem := systray.AddMenuItem("Quit TunnelTab", "Stop all tunnels and terminals, then quit")
		go func() {
			for {
				select {
				case <-openItem.ClickedCh:
					go open()
				case <-lockItem.ClickedCh:
					go m.Lock()
				case <-quitItem.ClickedCh:
					go m.Quit()
				case <-stop:
					systray.Quit()
					return
				}
			}
		}()
	}, nil)
}
