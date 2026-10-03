package platform

import (
	"sync"
	"sync/atomic"
	"time"
)

// TrayMenu is what the tray icon shows and does.
type TrayMenu struct {
	Tooltip string
	// IconFile is where the icon may be written, in the data folder (Windows
	// loads tray icons only from a file).
	IconFile string
	// Open opens the dashboard, Lock locks the vault, Quit quits TunnelTab.
	// Each runs on its own goroutine.
	Open, Lock, Quit func()
}

// Tray is the icon in the notification area ("system tray"). The tray must
// run on the program's main thread, so main calls Loop and everything else
// runs on other goroutines:
//
//	tray := platform.NewTray()
//	go func() { code := run(tray); tray.Stop(); os.Exit(code) }()
//	tray.Loop()
//
// Where there is no tray (an unsupported OS, a Linux desktop without one,
// no desktop at all), Show does nothing visible and the program runs as
// before.
type Tray struct {
	show     chan TrayMenu
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	shown    atomic.Bool
}

// NewTray returns a tray that shows nothing until Show is called.
func NewTray() *Tray {
	return &Tray{
		show: make(chan TrayMenu, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// Show puts the icon in the tray with menu m. Only the first call counts.
func (t *Tray) Show(m TrayMenu) {
	select {
	case t.show <- m:
	default:
	}
}

// Shown reports whether the icon is in the tray (false where there is none).
func (t *Tray) Shown() bool { return t.shown.Load() }

// haveTray is trayAvailable; tests replace it so they never show a real icon.
var haveTray = trayAvailable

// trayStopWait is how long Stop waits for the icon to be removed.
const trayStopWait = 2 * time.Second

// Stop removes the icon and ends Loop. It waits until the icon is gone (up
// to a couple of seconds), so the program can exit without leaving a stale
// icon behind.
func (t *Tray) Stop() {
	t.stopOnce.Do(func() { close(t.stop) })
	select {
	case <-t.done:
	case <-time.After(trayStopWait):
	}
}

// Loop runs the tray until Stop is called. Call it from the main goroutine
// (the tray library pins that goroutine to the main thread).
func (t *Tray) Loop() {
	defer close(t.done)
	select {
	case m := <-t.show:
		if haveTray() {
			t.shown.Store(true)
			runTray(m, t.stop)
			return
		}
	case <-t.stop:
		return
	}
	<-t.stop
}

// debounce returns f wrapped so that calls within d of the last accepted
// one are ignored (e.g. the two clicks of a double-click opening two tabs).
func debounce(d time.Duration, f func()) func() {
	var mu sync.Mutex
	var last time.Time
	return func() {
		mu.Lock()
		now := time.Now()
		if !last.IsZero() && now.Sub(last) < d {
			mu.Unlock()
			return
		}
		last = now
		mu.Unlock()
		f()
	}
}
