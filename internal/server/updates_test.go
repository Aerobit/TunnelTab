package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
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
