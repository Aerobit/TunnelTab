package main

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Aerobit/TunnelTab/internal/config"
	"github.com/Aerobit/TunnelTab/internal/platform"
)

func TestClaimDataDir(t *testing.T) {
	paths := config.PathsFor(t.TempDir())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	lock, url, err := claimDataDir(paths, time.Second, log)
	if err != nil || lock == nil || url != "" {
		t.Fatalf("free folder: lock=%v url=%q err=%v", lock, url, err)
	}

	// Held, and no copy answers (e.g. the instance file of a crashed one).
	writeInstance(t, paths.Instance, 42)
	if _, _, err := claimDataDir(paths, 300*time.Millisecond, log); !errors.Is(err, platform.ErrLocked) {
		t.Fatalf("held, nobody answers: %v", err)
	}

	// Held by a copy that answers: open its dashboard.
	running := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/instance/launch" || r.Header.Get("X-TunnelTab-Instance") != "s" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"url":"http://127.0.0.1/launch"}`))
	}))
	defer running.Close()
	port := running.Listener.Addr().(*net.TCPAddr).Port
	platform.WriteInstance(paths.Instance, platform.Instance{PID: 42, Port: port, Secret: "s"})
	if l, url, err := claimDataDir(paths, time.Second, log); err != nil || l != nil || url != "http://127.0.0.1/launch" {
		t.Fatalf("held, running copy: lock=%v url=%q err=%v", l, url, err)
	}

	// A copy that quits while we wait leaves the folder to us.
	go func() { time.Sleep(300 * time.Millisecond); lock.Close() }()
	running.Close()
	l, url, err := claimDataDir(paths, 5*time.Second, log)
	if err != nil || l == nil || url != "" {
		t.Fatalf("released while waiting: lock=%v url=%q err=%v", l, url, err)
	}
	l.Close()
}

// Relative key paths resolve in the TunnelTab folder, wherever --data puts
// the data folder.
func TestKeyBaseDirIgnoresDataFolder(t *testing.T) {
	app, err := config.AppFolder()
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if got := keyBaseDir(filepath.Join(elsewhere, "vault")); got != app {
		t.Fatalf("keyBaseDir = %s, want the TunnelTab folder %s", got, app)
	}
}
