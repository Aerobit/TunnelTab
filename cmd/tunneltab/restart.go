package main

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Aerobit/TunnelTab/internal/platform"
	"github.com/Aerobit/TunnelTab/internal/update"
)

// How long the new version gets to start before the update is undone.
const restartTimeout = 30 * time.Second

// restartAfterUpdate starts the newly installed program (this process has
// already shut down its server and released the instance file) and waits
// until it has written its own instance file, i.e. is running. If it
// doesn't get that far, the update is rolled back and the old version is
// started again.
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
		deadline := time.After(restartTimeout)
		for {
			if inst, ok, _ := platform.ReadInstance(instancePath); ok && inst.PID == cmd.Process.Pid {
				log.Info("new version started", "pid", inst.PID)
				return 0
			}
			select {
			case <-exited:
				log.Warn("new version exited during start")
			case <-deadline:
				log.Warn("new version didn't start in time")
				cmd.Process.Kill()
			case <-time.After(100 * time.Millisecond):
				continue
			}
			break
		}
	} else {
		log.Warn("can't start the new version", "error", err)
	}

	if err := update.Rollback(filepath.Dir(exe)); err != nil {
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

// cleanupAfterUpdate removes what an update left behind once this (new)
// version runs. On Windows the old program stays locked until the previous
// process has exited, so it keeps trying for a while.
func cleanupAfterUpdate(appDir string, log *slog.Logger) {
	if !update.Pending(appDir) {
		return
	}
	// Give the previous process time to see that we started, so it doesn't
	// roll back after we've removed the old files.
	time.Sleep(3 * time.Second)
	for i := 0; i < 60; i++ {
		if err := update.Cleanup(appDir, filepath.Join(appDir, ".update")); err == nil {
			log.Info("removed the previous version's files")
			return
		}
		time.Sleep(2 * time.Second)
	}
	log.Warn("could not remove the previous version's files")
}
