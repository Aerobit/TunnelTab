package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Aerobit/TunnelTab/internal/platform"
	"github.com/Aerobit/TunnelTab/internal/update"
)

func writeInstance(t *testing.T, path string, pid int) {
	t.Helper()
	if err := platform.WriteInstance(path, platform.Instance{PID: pid, Port: 1234, Secret: "s"}); err != nil {
		t.Fatal(err)
	}
}

func TestAwaitStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.json")
	serving := errors.New("not serving yet")
	confirm := func(port int, secret string) error {
		if port != 1234 || secret != "s" {
			t.Errorf("confirm(%d, %q)", port, secret)
		}
		return serving
	}

	// The instance file alone isn't enough: the new version may still fail
	// to open its vault.
	writeInstance(t, path, 42)
	if got := awaitStart(42, nil, path, 300*time.Millisecond, confirm); got != timedOut {
		t.Fatalf("instance file without serving: %v", got)
	}

	// Another process's instance file doesn't count.
	serving = nil
	writeInstance(t, path, 7)
	if got := awaitStart(42, nil, path, 300*time.Millisecond, confirm); got != timedOut {
		t.Fatalf("other PID: %v", got)
	}

	writeInstance(t, path, 42)
	if got := awaitStart(42, nil, path, time.Second, confirm); got != started {
		t.Fatalf("serving: %v", got)
	}

	exited := make(chan struct{})
	close(exited)
	os.Remove(path)
	if got := awaitStart(42, exited, path, time.Second, confirm); got != exitedEarly {
		t.Fatalf("exited: %v", got)
	}
}

func TestRollbackRetries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tunneltab.old"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := rollback(dir, 3, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "tunneltab")); err != nil || string(b) != "old" {
		t.Fatalf("not restored: %q %v", b, err)
	}
}

// The new version must keep the previous files until the previous process
// has confirmed the start; otherwise a late rollback has nothing to restore.
func TestCleanupWaitsForConfirmation(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "tunneltab.old")
	if err := os.WriteFile(old, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	confirmed := make(chan struct{})
	done := make(chan struct{})
	go func() { cleanupAfterUpdate(dir, confirmed, log); close(done) }()

	time.Sleep(200 * time.Millisecond)
	if !update.Pending(dir) {
		t.Fatal("previous files removed before the confirmation")
	}
	close(confirmed)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup didn't finish")
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous files still there: %v", err)
	}
}
