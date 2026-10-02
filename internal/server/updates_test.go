package server

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Aerobit/TunnelTab/internal/update"
)

func TestCheckForUpdates(t *testing.T) {
	calls := 0
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"tag_name":"v0.2.0","html_url":"https://github.com/Aerobit/TunnelTab/releases/tag/v0.2.0","published_at":"2026-10-15T10:00:00Z"}`)
	}))
	defer gh.Close()

	h := newHarness(t, func(c *Config) { c.UpdateURL = gh.URL; c.Version = "0.1.0" })
	h.login()
	if calls != 0 {
		t.Fatal("contacted the release server without being asked")
	}
	m := h.mustCall("POST", "/api/updates/check", nil, 200)
	if m["latest"] != "0.2.0" || m["newer"] != true || m["current"] != "0.1.0" || calls != 1 {
		t.Fatalf("got %v (calls %d)", m, calls)
	}
	// Needs a session, like everything else.
	if status, _ := h.request("POST", "/api/updates/check", nil, map[string]string{"Origin": h.base}); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated check: %d", status)
	}
}

func TestCheckForUpdatesErrors(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	h := newHarness(t, func(c *Config) { c.UpdateURL = gh.URL; c.Version = "0.1.0" })
	h.login()
	if m := h.mustCall("POST", "/api/updates/check", nil, 200); m["noRelease"] != true {
		t.Fatalf("no release: %v", m)
	}
	gh.Close()
	if m := h.mustCall("POST", "/api/updates/check", nil, http.StatusBadGateway); m["error"] != "update_check_failed" {
		t.Fatalf("unreachable: %v", m)
	}
}

// fakeSignedRelease serves release v<version> with a zip holding the
// program for this system, signed with priv.
func fakeSignedRelease(t *testing.T, version string, priv ed25519.PrivateKey) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("tunneltab/" + update.ExeName())
	w.Write([]byte("new program"))
	zw.Close()
	zipName := update.ZipName(version)
	sums := fmt.Sprintf("%x  %s\n", sha256.Sum256(buf.Bytes()), zipName)
	files := map[string][]byte{zipName: buf.Bytes(), update.SumsName: []byte(sums), update.SigName: update.SignSums(priv, []byte(sums))}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			var assets []map[string]string
			for n := range files {
				assets = append(assets, map[string]string{"name": n, "browser_download_url": srv.URL + "/dl/" + n})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v" + version, "html_url": update.ReleasesPage + "tag/v" + version, "assets": assets})
			return
		}
		if b, ok := files[strings.TrimPrefix(r.URL.Path, "/dl/")]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInstallUpdate(t *testing.T) {
	if update.ExeName() == "" {
		t.Skip("no release build for this system")
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	rel := fakeSignedRelease(t, "0.2.0", priv)
	app := t.TempDir()
	exe := filepath.Join(app, "tunneltab")
	os.WriteFile(exe, []byte("old program"), 0o755)
	restarted := make(chan struct{}, 1)
	h := newHarness(t, func(c *Config) {
		c.UpdateURL, c.Version = rel.URL+"/latest", "0.1.0"
		c.AppDir, c.ExeName, c.ReleaseKey = app, "tunneltab", pub
		c.OnUpdateInstalled = func() { restarted <- struct{}{} }
	})
	h.login()
	if m := h.mustCall("POST", "/api/updates/check", nil, 200); m["canInstall"] != true {
		t.Fatalf("check: %v", m)
	}
	dashboard := h.srv.events.subscribe()
	defer h.srv.events.unsubscribe(dashboard)
	if m := h.mustCall("POST", "/api/updates/install", nil, 200); m["version"] != "0.2.0" {
		t.Fatalf("install: %v", m)
	}
	// Every open dashboard is told each step, in order.
	if got, want := strings.Join(updateSteps(dashboard), ","), "checking,verifying,downloading,unpacking,installing,restarting"; got != want {
		t.Errorf("update steps %s, want %s", got, want)
	}
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("OnUpdateInstalled not called")
	}
	if b, _ := os.ReadFile(exe); string(b) != "new program" {
		t.Fatalf("program not replaced: %q", b)
	}
	if m := h.mustCall("POST", "/api/updates/install", nil, http.StatusConflict); m["error"] != "update_busy" {
		t.Fatalf("second install: %v", m)
	}
}

func TestInstallUpdateRefused(t *testing.T) {
	if update.ExeName() == "" {
		t.Skip("no release build for this system")
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	rel := fakeSignedRelease(t, "0.2.0", otherPriv)
	app := t.TempDir()
	exe := filepath.Join(app, "tunneltab")
	os.WriteFile(exe, []byte("old program"), 0o755)
	called := false
	h := newHarness(t, func(c *Config) {
		c.UpdateURL, c.Version = rel.URL+"/latest", "0.1.0"
		c.AppDir, c.ExeName, c.ReleaseKey = app, "tunneltab", pub
		c.OnUpdateInstalled = func() { called = true }
	})
	h.login()
	dashboard := h.srv.events.subscribe()
	defer h.srv.events.unsubscribe(dashboard)
	// Signed with another key: refused, nothing changed, and it can be retried.
	for i := 0; i < 2; i++ {
		if m := h.mustCall("POST", "/api/updates/install", nil, http.StatusBadGateway); m["error"] != "update_failed" ||
			!strings.Contains(fmt.Sprint(m["message"]), "not signed by TunnelTab") {
			t.Fatalf("bad signature: %v", m)
		}
	}
	if b, _ := os.ReadFile(exe); string(b) != "old program" || called {
		t.Fatal("a badly signed update was installed")
	}
	if got, want := strings.Join(updateSteps(dashboard), ","), "checking,verifying,failed,checking,verifying,failed"; got != want {
		t.Errorf("update steps %s, want %s", got, want)
	}

	// Without AppDir (dev builds, tests) there's no "Update now".
	h2 := newHarness(t, func(c *Config) { c.UpdateURL, c.Version = rel.URL+"/latest", "0.1.0" })
	h2.login()
	if m := h2.mustCall("POST", "/api/updates/check", nil, 200); m["canInstall"] != false || m["newer"] != true {
		t.Fatalf("check: %v", m)
	}
	if m := h2.mustCall("POST", "/api/updates/install", nil, http.StatusConflict); m["error"] != "update_unavailable" {
		t.Fatalf("install: %v", m)
	}
	// Needs a session, like everything else.
	if status, _ := h2.request("POST", "/api/updates/install", nil, map[string]string{"Origin": h2.base}); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated install: %d", status)
	}
}

// updateSteps drains a dashboard's events and returns the update steps sent
// (repeats, such as "downloading", merged).
func updateSteps(ch chan []byte) []string {
	var steps []string
	for {
		select {
		case raw := <-ch:
			var ev struct{ Type, Step string }
			json.Unmarshal(raw, &ev)
			if ev.Type == "update" && (len(steps) == 0 || steps[len(steps)-1] != ev.Step) {
				steps = append(steps, ev.Step)
			}
		default:
			return steps
		}
	}
}
