// Command tunneltab is the TunnelTab executable: a portable SSH terminal and
// tunnel manager whose dashboard runs in the user's own browser.
//
// See docs/ARCHITECTURE.md for how the pieces fit together.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Aerobit/TunnelTab/internal/config"
	"github.com/Aerobit/TunnelTab/internal/platform"
	"github.com/Aerobit/TunnelTab/internal/server"
)

// version is set at build time with -ldflags "-X main.version=<version>".
// Builds made with plain `go build` / `go run` report "dev".
var version = "dev"

func main() {
	// The tray icon must run on the main thread, so the program itself runs
	// on another goroutine and exits from there, removing the icon first.
	tray := platform.NewTray()
	go func() {
		code := run(tray)
		tray.Stop()
		os.Exit(code)
	}()
	tray.Loop()
	select {} // the goroutine above ends the program
}

func run(tray *platform.Tray) int {
	showVersion := flag.Bool("version", false, "print the version and exit")
	dataFlag := flag.String("data", "", "data folder (default: \"data\" next to the executable)")
	portFlag := flag.Int("port", 0, "dashboard port on 127.0.0.1 (default: from settings, 47811)")
	noBrowser := flag.Bool("no-browser", false, "print the dashboard link instead of opening a browser")
	noTray := flag.Bool("no-tray", false, "don't show an icon in the system tray")
	updateURL := flag.String("update-url", "", "release API used by \"Check for updates\" (for testing; default: GitHub)")
	flag.Parse()

	if *showVersion {
		fmt.Println("TunnelTab", version)
		return 0
	}

	fail := func(msg string, err error) int {
		platform.ShowError("TunnelTab", fmt.Sprintf("%s:\n\n%v", msg, err))
		return 1
	}

	// Double-clicking the .exe inside the ZIP runs a temporary copy; the
	// vault would be created in a folder Windows deletes later.
	if *dataFlag == "" && runtime.GOOS == "windows" && config.RunningFromTempFolder() {
		platform.ShowError("TunnelTab", "TunnelTab is running from a temporary folder, probably because it was "+
			"opened from inside the ZIP file.\n\nPlease extract the ZIP first (right-click → Extract All…), then "+
			"start tunneltab.exe from the extracted folder. Otherwise your data would be lost.")
		return 1
	}

	dataDir, err := config.ResolveDataDir(*dataFlag)
	if err != nil {
		return fail("Can't find the data folder", err)
	}
	if err := config.EnsureDataDir(dataDir); err != nil {
		return fail("Can't use the data folder (is TunnelTab on read-only media?)", err)
	}
	paths := config.PathsFor(dataDir)

	log, logCloser, err := config.OpenLogger(paths.LogDir, slog.LevelInfo)
	if err != nil {
		return fail("Can't open the log file", err)
	}
	defer logCloser.Close()
	log.Info("starting", "version", version, "dataDir", dataDir)
	if err := config.RestrictToOwner(dataDir); err != nil {
		// E.g. a FAT32/exFAT USB stick has no permissions. The vault is
		// encrypted regardless; say so in the log and carry on.
		log.Warn("could not restrict the data folder to this user", "error", err)
	}

	settings, err := config.LoadSettings(paths.Settings)
	if err != nil {
		log.Warn("using default settings", "error", err)
	}

	// Only one copy may use the data folder: two would each save their own
	// vault and the last save would win. If one is already running, open
	// its dashboard instead.
	dataLock, url, err := claimDataDir(paths, lockWait, log)
	if err != nil {
		platform.ShowError("TunnelTab", "Another TunnelTab is using this data folder but doesn't answer.\n\n"+
			"If it is still starting or quitting, try again in a moment.")
		return 1
	}
	if url != "" {
		show(url, *noBrowser, log)
		return 0
	}
	defer dataLock.Close()

	port := settings.Port
	if *portFlag != 0 {
		port = *portFlag
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		log.Warn("dashboard port busy; using another", "port", port, "error", err)
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return fail("Can't start the dashboard", err)
		}
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	inst, err := platform.NewInstance(actualPort)
	if err != nil {
		return fail("Can't start", err)
	}
	if err := platform.WriteInstance(paths.Instance, inst); err != nil {
		return fail("Can't write to the data folder", err)
	}
	defer platform.RemoveInstance(paths.Instance)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// "Update now" replaces the files next to the running program, then
	// shuts down like Quit and starts the new version (restart.go).
	var restart atomic.Bool
	exe, appDir := programPath()

	srv, err := server.New(server.Config{
		Paths:          paths,
		BaseDir:        filepath.Dir(dataDir),
		Settings:       settings,
		Logger:         log,
		Version:        version,
		InstanceSecret: inst.Secret,
		OnQuit:         stop,
		TrayShown:      tray.Shown,
		UpdateURL:      *updateURL,
		AppDir:         appDir,
		ExeName:        filepath.Base(exe),
		OnUpdateInstalled: func() {
			restart.Store(true)
			stop()
		},
	})
	if err != nil {
		return fail("Can't open the vault", err)
	}
	srv.SetAddr(ln.Addr())

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("dashboard server stopped", "error", err)
			stop()
		}
	}()
	log.Info("dashboard listening", "port", actualPort)

	autoLockStop := make(chan struct{})
	go srv.RunAutoLock(autoLockStop)

	if !*noTray {
		tray.Show(platform.TrayMenu{
			Tooltip:  "TunnelTab " + version,
			IconFile: paths.TrayIcon,
			Open:     func() { show(srv.LaunchURL(), false, log) },
			Lock:     srv.Lock,
			Quit:     stop,
		})
	}
	show(srv.LaunchURL(), *noBrowser, log)
	if appDir != "" {
		go cleanupAfterUpdate(appDir, srv.UpdateConfirmed(), log)
	}

	<-ctx.Done()
	log.Info("shutting down")
	// finish runs once the server has stopped: after "Update now" it hands
	// the data folder to the new version and waits for it to start (rolling
	// back if it doesn't). It runs once, from whichever gets there first:
	// the shutdown below or the watchdog.
	finish := sync.OnceValue(func() int {
		platform.RemoveInstance(paths.Instance)
		if !restart.Load() {
			return 0
		}
		dataLock.Close()
		return restartAfterUpdate(exe, paths.Instance, log)
	})
	// Quitting must end the program, and with it every tunnel, even if
	// something below hangs. After "Update now" a hang must not leave the
	// new files in place unchecked, so the update is still finished.
	watchdog := time.AfterFunc(shutdownTimeout, func() {
		if !restart.Load() {
			log.Error("shutdown is taking too long; exiting anyway")
			platform.RemoveInstance(paths.Instance)
			os.Exit(1)
		}
		log.Error("shutdown is taking too long; finishing the update anyway")
		httpSrv.Close() // no more requests (or vault saves) from this copy
		os.Exit(finish())
	})
	close(autoLockStop)
	srv.Close() // stops tunnels and ends event streams so Shutdown doesn't wait on them
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutdownCtx)
	watchdog.Stop()
	log.Info("stopped")
	return finish()
}

// How long shutting down may take before the watchdog ends the program.
const shutdownTimeout = 15 * time.Second

// How long a launch keeps trying while another copy holds the data folder
// but doesn't answer yet: it may still be starting, or quitting (which can
// take up to the 15 s shutdown watchdog).
const (
	lockWait = 20 * time.Second
	lockPoll = 250 * time.Millisecond
)

// claimDataDir takes the data folder's lock for this process. If another
// copy holds it, it returns a login link from that copy instead (found via
// instance.json), retrying both for up to wait, and platform.ErrLocked if
// neither works out. If the file system can't lock files, it warns and
// returns neither lock nor link, and only instance.json guards the folder.
func claimDataDir(paths config.Paths, wait time.Duration, log *slog.Logger) (*platform.DataDirLock, string, error) {
	deadline := time.Now().Add(wait)
	for {
		lock, err := platform.LockDataDir(paths.Lock)
		if err == nil {
			return lock, "", nil
		}
		if inst, ok, _ := platform.ReadInstance(paths.Instance); ok {
			if url, err := server.RequestLaunchURL(inst.Port, inst.Secret); err == nil {
				log.Info("already running; opening its dashboard", "pid", inst.PID)
				return nil, url, nil
			}
		}
		if !errors.Is(err, platform.ErrLocked) {
			log.Warn("can't lock the data folder", "error", err)
			return nil, "", nil
		}
		if time.Now().After(deadline) {
			log.Warn("data folder is locked by a copy that doesn't answer")
			return nil, "", err
		}
		time.Sleep(lockPoll)
	}
}

// show opens the dashboard link in the browser, or prints it. The link
// contains a one-time login token, so it is never written to the log.
func show(url string, noBrowser bool, log *slog.Logger) {
	if !noBrowser {
		if err := platform.OpenBrowser(url); err == nil {
			return
		} else {
			log.Warn("could not open a browser", "error", err)
		}
	}
	fmt.Println("Open this link in your browser (valid for 2 minutes, one use):")
	fmt.Println(url)
}

// programPath returns the running program and its folder, or "" if unknown
// (then "Update now" is off).
func programPath() (exe, dir string) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "", ""
	}
	return exe, filepath.Dir(exe)
}
