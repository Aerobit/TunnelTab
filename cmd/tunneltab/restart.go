package main

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Aerobit/TunnelTab/internal/platform"
	"github.com/Aerobit/TunnelTab/internal/server"
	"github.com/Aerobit/TunnelTab/internal/update"
)

// How long the new version gets to start before the update is undone.
const restartTimeout = 30 * time.Second

// How long a killed new version gets to exit, and how long restoring the
// previous files is retried (Windows keeps a program's file locked for a
// moment after the process ends).
const (
	killWait      = 10 * time.Second
	rollbackTries = 20
	rollbackPause = 500 * time.Millisecond
)

// restartAfterUpdate starts the newly installed program (this process has
// already shut down its server and released the instance file) and waits
// until it serves its dashboard, which means it has also opened the vault.
// If it doesn't get that far, the update is rolled back and the old
// version is started again.
//
// Starting itself is the one exception to "the only program TunnelTab
// launches is the browser" (CLAUDE.md, invariant 7).
func restartAfterUpdate(exe string, instancePath string, log *slog.Logger) int {
	args := os.Args[1:]
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err == nil {
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		switch awaitStart(cmd.Process.Pid, exited, instancePath, restartTimeout, server.ConfirmStarted) {
		case started:
			log.Info("new version started", "pid", cmd.Process.Pid)
			return 0
		case exitedEarly:
			log.Warn("new version exited during start")
		case timedOut:
			log.Warn("new version didn't start in time")
			cmd.Process.Kill()
			select {
			case <-exited:
			case <-time.After(killWait):
				log.Warn("new version is still running after being stopped")
			}
		}
	} else {
		log.Warn("can't start the new version", "error", err)
	}

	if err := rollback(filepath.Dir(exe), rollbackTries, rollbackPause); err != nil {
		log.Error("rollback failed", "error", err)
		platform.ShowError("TunnelTab", "The update didn't start and the previous version couldn't be restored:\n\n"+
			err.Error()+"\n\nDownload TunnelTab again from its release page. Your data folder is unchanged.")
		return 1
	}
	log.Info("update rolled back; starting the previous version")
	old := exec.Command(exe, args...)
	old.Stdout, old.Stderr = os.Stdout, os.Stderr
	if err := old.Start(); err != nil {
		log.Error("can't start the previous version", "error", err)
		return 1
	}
	return 1
}

type startResult int

const (
	started startResult = iota
	exitedEarly
	timedOut
)

// awaitStart waits until the process pid has written the instance file and
// accepted confirm (POST /api/instance/started), which it only can once it
// serves requests. It gives up when the process exits or after timeout.
func awaitStart(pid int, exited <-chan struct{}, instancePath string, timeout time.Duration,
	confirm func(port int, secret string) error) startResult {
	deadline := time.After(timeout)
	for {
		if inst, ok, _ := platform.ReadInstance(instancePath); ok && inst.PID == pid {
			if confirm(inst.Port, inst.Secret) == nil {
				return started
			}
		}
		select {
		case <-exited:
			return exitedEarly
		case <-deadline:
			return timedOut
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// rollback puts the previous version's files back, retrying while they are
// still locked.
func rollback(appDir string, tries int, pause time.Duration) error {
	var err error
	for i := 0; i < tries; i++ {
		if err = update.Rollback(appDir); err == nil {
			return nil
		}
		time.Sleep(pause)
	}
	return err
}

// How long a new version waits for the previous one to confirm the start
// before it removes the previous files anyway (e.g. it was started by hand
// after an interrupted update).
const confirmWait = 2 * restartTimeout

// cleanupAfterUpdate removes what an update left behind once this (new)
// version runs. It waits until the previous process has confirmed the
// start, so a rollback always finds the previous files. On Windows the old
// program stays locked until the previous process has exited, so it keeps
// trying for a while.
func cleanupAfterUpdate(appDir string, confirmed <-chan struct{}, log *slog.Logger) {
	if !update.Pending(appDir) {
		return
	}
	select {
	case <-confirmed:
	case <-time.After(confirmWait):
		log.Info("no confirmation from the previous version; removing its files anyway")
	}
	for i := 0; i < 60; i++ {
		if err := update.Cleanup(appDir, filepath.Join(appDir, ".update")); err == nil {
			log.Info("removed the previous version's files")
			return
		}
		time.Sleep(2 * time.Second)
	}
	log.Warn("could not remove the previous version's files")
}
