package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Aerobit/TunnelTab/internal/config"
	"github.com/Aerobit/TunnelTab/internal/health"
	"github.com/Aerobit/TunnelTab/internal/model"
	"github.com/Aerobit/TunnelTab/internal/sshx/sshtest"
	"github.com/Aerobit/TunnelTab/internal/vault"
)

const (
	masterPW       = "correct horse battery"
	instanceSecret = "instance-secret-for-tests"
	// Secrets used in tests. No API response may ever contain them; every
	// test checks this on cleanup.
	sshPassword   = "Ssh-Pa55word-Canary"
	keyPassphrase = "Key-Passphrase-Canary"
)

var fastVault = vault.Options{Params: vault.Params{Time: 1, MemoryKiB: 8 * 1024, Threads: 1}}

type harness struct {
	t       *testing.T
	srv     *Server
	ts      *httptest.Server
	base    string // http://127.0.0.1:port
	session string
	quit    chan struct{}
	clock   *testClock

	mu     sync.Mutex
	bodies []string
}

// testClock is the server's clock in tests. It follows the real time until
// frozen; tests then move it with advance. It is safe to use while the
// server's goroutines read it (replacing Server.now mid-test is a data race).
type testClock struct {
	mu     sync.Mutex
	frozen bool
	t      time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.frozen {
		return time.Now()
	}
	return c.t
}

// freeze stops the clock at the current time.
func (c *testClock) freeze() {
	c.mu.Lock()
	c.frozen, c.t = true, time.Now()
	c.mu.Unlock()
}

// advance moves a frozen clock forward.
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newHarness(t *testing.T, opts ...func(*Config)) *harness {
	t.Helper()
	dir := t.TempDir()
	if err := config.EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, quit: make(chan struct{}, 1), clock: &testClock{}}
	cfg := Config{
		Now:             h.clock.Now,
		Paths:           config.PathsFor(dir),
		BaseDir:         dir,
		Settings:        config.DefaultSettings(),
		VaultOptions:    fastVault,
		InstanceSecret:  instanceSecret,
		OnQuit:          func() { h.quit <- struct{}{} },
		UnlockBaseDelay: 200 * time.Millisecond,
		Version:         "test",
	}
	for _, o := range opts {
		o(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	s.SetAddr(ts.Listener.Addr())
	h.srv, h.ts, h.base = s, ts, ts.URL
	t.Cleanup(func() {
		s.Close()
		ts.Close()
		h.assertNoSecretsLeaked()
	})
	return h
}

func (h *harness) assertNoSecretsLeaked() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, b := range h.bodies {
		for _, secret := range []string{sshPassword, keyPassphrase, masterPW, "PRIVATE KEY"} {
			if strings.Contains(b, secret) {
				h.t.Errorf("API response leaked a secret (%q): %s", secret, b)
			}
		}
	}
}

// request sends a raw request; headers are exactly what's given.
func (h *harness) request(method, path string, body any, headers map[string]string) (int, []byte) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.base+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	h.mu.Lock()
	h.bodies = append(h.bodies, string(out))
	h.mu.Unlock()
	return resp.StatusCode, out
}

// call sends an authenticated same-origin API request, like the dashboard.
func (h *harness) call(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	hdr := map[string]string{"Origin": h.base, "Authorization": "Bearer " + h.session}
	if body != nil {
		hdr["Content-Type"] = "application/json"
	}
	status, raw := h.request(method, path, body, hdr)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return status, m
}

func (h *harness) mustCall(method, path string, body any, want int) map[string]any {
	h.t.Helper()
	status, m := h.call(method, path, body)
	if status != want {
		h.t.Fatalf("%s %s: status %d, want %d: %v", method, path, status, want, m)
	}
	return m
}

func (h *harness) login() {
	h.t.Helper()
	u, _ := url.Parse(h.srv.LaunchURL())
	status, raw := h.request("POST", "/api/session", map[string]string{"launch": u.Query().Get("launch")},
		map[string]string{"Origin": h.base})
	if status != 200 {
		h.t.Fatalf("login: %d %s", status, raw)
	}
	var out map[string]string
	json.Unmarshal(raw, &out)
	h.session = out["session"]
}

// ready returns a logged-in harness with an unlocked vault.
func ready(t *testing.T) *harness {
	h := newHarness(t)
	h.login()
	h.mustCall("POST", "/api/vault/create", map[string]string{"password": masterPW}, 200)
	return h
}

func id(m map[string]any) string { s, _ := m["id"].(string); return s }

// --- Security checks --------------------------------------------------------

func TestSecurityHeadersAndStatic(t *testing.T) {
	h := newHarness(t)
	status, body := h.request("GET", "/", nil, nil)
	if status != 200 || !strings.Contains(string(body), "TunnelTab") {
		t.Fatalf("index: %d %s", status, body)
	}
	resp, _ := http.Get(h.base + "/")
	for _, hdr := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if resp.Header.Get(hdr) == "" {
			t.Errorf("missing %s header", hdr)
		}
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Error("CSP does not restrict scripts")
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS header present")
	}
}

func TestRejectsWrongHost(t *testing.T) {
	h := newHarness(t)
	h.login()
	// DNS rebinding: an attacker's domain resolving to 127.0.0.1.
	for _, host := range []string{"evil.example.com", "evil.example.com:" + strings.Split(h.base, ":")[2], "127.0.0.1:1"} {
		status, _ := h.request("GET", "/api/state", nil, map[string]string{"Host": host, "Authorization": "Bearer " + h.session})
		if status != http.StatusForbidden {
			t.Errorf("Host %q: status %d, want 403", host, status)
		}
	}
	port := strings.Split(h.base, ":")[2]
	status, _ := h.request("GET", "/api/state", nil, map[string]string{"Host": "localhost:" + port, "Authorization": "Bearer " + h.session})
	if status != 200 {
		t.Errorf("localhost:%s rejected: %d", port, status)
	}
}

func TestRejectsCrossOrigin(t *testing.T) {
	h := newHarness(t)
	h.login()
	auth := "Bearer " + h.session
	cases := []map[string]string{
		{"Origin": "http://evil.example.com", "Authorization": auth},
		{"Origin": "http://127.0.0.1:5678", "Authorization": auth}, // a tunneled web app
		{"Origin": "null", "Authorization": auth},
		{"Authorization": auth}, // POST without Origin
		{"Origin": h.base, "Authorization": auth, "Sec-Fetch-Site": "cross-site"},
		{"Origin": h.base, "Authorization": auth, "Sec-Fetch-Site": "same-site"},
	}
	for i, hdr := range cases {
		status, _ := h.request("POST", "/api/vault/lock", nil, hdr)
		if status != http.StatusForbidden {
			t.Errorf("case %d (%v): status %d, want 403", i, hdr, status)
		}
	}
	if status, _ := h.request("OPTIONS", "/api/state", nil, map[string]string{"Origin": h.base}); status == 200 || status == 204 {
		t.Errorf("preflight answered with %d", status)
	}
}

func TestRequiresSession(t *testing.T) {
	h := newHarness(t)
	for _, auth := range []string{"", "Bearer", "Bearer nope", "Basic abc"} {
		status, _ := h.request("GET", "/api/state", nil, map[string]string{"Authorization": auth})
		if status != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", auth, status)
		}
	}
}

func TestLaunchTokenIsOneTimeAndExpires(t *testing.T) {
	h := newHarness(t)
	h.clock.freeze()

	u, _ := url.Parse(h.srv.LaunchURL())
	if u.Host != strings.TrimPrefix(h.base, "http://") {
		t.Fatalf("launch URL host %q, want %q", u.Host, h.base)
	}
	tok := u.Query().Get("launch")
	redeem := func(tok string) int {
		status, _ := h.request("POST", "/api/session", map[string]string{"launch": tok}, map[string]string{"Origin": h.base})
		return status
	}
	if redeem(tok) != 200 {
		t.Fatal("valid launch token rejected")
	}
	if redeem(tok) != http.StatusUnauthorized {
		t.Fatal("launch token accepted twice")
	}
	u, _ = url.Parse(h.srv.LaunchURL())
	h.clock.advance(launchTokenTTL + time.Second)
	if redeem(u.Query().Get("launch")) != http.StatusUnauthorized {
		t.Fatal("expired launch token accepted")
	}
	if redeem("") != http.StatusUnauthorized {
		t.Fatal("empty launch token accepted")
	}
}

func TestInstanceLaunch(t *testing.T) {
	h := newHarness(t)
	port := h.ts.Listener.Addr().(*net.TCPAddr).Port
	u, err := RequestLaunchURL(port, instanceSecret)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u, "?launch=") {
		t.Fatalf("unexpected URL %q", u)
	}
	if _, err := RequestLaunchURL(port, "wrong"); err == nil {
		t.Fatal("wrong instance secret accepted")
	}
	// The Origin exemption applies only to the instance endpoints.
	status, _ := h.request("POST", "/api/vault/lock", nil, map[string]string{instanceHeader: instanceSecret})
	if status != http.StatusForbidden {
		t.Fatalf("Origin-less POST elsewhere: %d", status)
	}
}

func TestBadRequests(t *testing.T) {
	h := ready(t)
	status, raw := h.request("POST", "/api/projects", nil, map[string]string{
		"Origin": h.base, "Authorization": "Bearer " + h.session,
	})
	if status != http.StatusBadRequest {
		t.Errorf("empty body: %d %s", status, raw)
	}
	if status, _ := h.call("POST", "/api/projects", map[string]any{"name": "x", "unexpected": 1}); status != http.StatusBadRequest {
		t.Errorf("unknown field accepted: %d", status)
	}
	big := map[string]string{"name": strings.Repeat("x", maxBodyBytes+10)}
	if status, _ := h.call("POST", "/api/projects", big); status != http.StatusRequestEntityTooLarge {
		t.Errorf("huge body: %d, want 413", status)
	}
}

// --- Vault workflow ---------------------------------------------------------

func TestVaultLifecycle(t *testing.T) {
	h := newHarness(t)
	h.login()
	if m := h.mustCall("GET", "/api/state", nil, 200); m["vault"] != "none" {
		t.Fatalf("state %v, want none", m["vault"])
	}
	h.mustCall("GET", "/api/data", nil, http.StatusConflict)
	if m := h.mustCall("POST", "/api/vault/create", map[string]string{"password": "short"}, 400); m["error"] != "weak_password" {
		t.Fatalf("got %v", m)
	}
	h.mustCall("POST", "/api/vault/create", map[string]string{"password": masterPW}, 200)
	h.mustCall("POST", "/api/vault/create", map[string]string{"password": masterPW}, http.StatusConflict)
	h.mustCall("GET", "/api/data", nil, 200)

	h.mustCall("POST", "/api/vault/lock", nil, 200)
	if m := h.mustCall("GET", "/api/state", nil, 200); m["vault"] != "locked" {
		t.Fatalf("state %v, want locked", m["vault"])
	}
	h.mustCall("GET", "/api/data", nil, http.StatusLocked)

	// Wrong password, then rate limiting, then success after the delay.
	if m := h.mustCall("POST", "/api/vault/unlock", map[string]string{"password": "nope nope"}, 401); m["error"] != "wrong_password" {
		t.Fatalf("got %v", m)
	}
	m := h.mustCall("POST", "/api/vault/unlock", map[string]string{"password": masterPW}, http.StatusTooManyRequests)
	if m["retryAfterMs"].(float64) <= 0 {
		t.Fatal("no retry delay reported")
	}
	time.Sleep(250 * time.Millisecond)
	h.mustCall("POST", "/api/vault/unlock", map[string]string{"password": masterPW}, 200)

	// Change password.
	// A wrong current password is rate-limited like unlocking.
	h.mustCall("POST", "/api/vault/password", map[string]string{"old": "wrong one", "new": "another long password"}, 401)
	h.mustCall("POST", "/api/vault/password", map[string]string{"old": masterPW, "new": "another long password"}, http.StatusTooManyRequests)
	time.Sleep(250 * time.Millisecond)
	h.mustCall("POST", "/api/vault/password", map[string]string{"old": masterPW, "new": "tiny"}, 400)
	h.mustCall("POST", "/api/vault/password", map[string]string{"old": masterPW, "new": "another long password"}, 200)
	h.mustCall("POST", "/api/vault/lock", nil, 200)
	h.mustCall("POST", "/api/vault/unlock", map[string]string{"password": "another long password"}, 200)
}

func TestExistingVaultOpensLocked(t *testing.T) {
	h := ready(t)
	h.mustCall("POST", "/api/projects", map[string]string{"name": "Keep"}, 201)
	h.srv.Close()

	s2, err := New(h.srv.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.vaultState() != "locked" {
		t.Fatalf("state %s, want locked", s2.vaultState())
	}
}

// --- Data -------------------------------------------------------------------

func TestCRUDAndNoSecretsInResponses(t *testing.T) {
	h := ready(t)
	p := h.mustCall("POST", "/api/projects", map[string]string{"name": "Prod"}, 201)
	h.mustCall("POST", "/api/projects", map[string]string{"name": ""}, 400)
	h.mustCall("PUT", "/api/projects/"+id(p), map[string]string{"name": "Production"}, 200)

	srv := h.mustCall("POST", "/api/servers", map[string]any{
		"projectId": id(p), "name": "web", "host": "vps.example.com", "port": 22, "username": "root",
		"auth": map[string]string{"type": "password", "password": sshPassword},
	}, 201)
	if auth := srv["auth"].(map[string]any); auth["hasPassword"] != true {
		t.Fatalf("hasPassword flag missing: %v", srv)
	}
	h.mustCall("PUT", "/api/servers/"+id(srv), map[string]any{
		"name": "web", "host": "vps.example.com", "port": 2222, "username": "root",
		"auth": map[string]string{"type": "keyFile", "keyPath": "keys/id", "passphrase": keyPassphrase},
	}, 200)
	h.mustCall("POST", "/api/servers/"+id(srv)+"/clear-passphrase", nil, 204)

	svc := h.mustCall("POST", "/api/services", map[string]any{"serverId": id(srv), "label": "n8n", "remotePort": 5678}, 201)
	if svc["remoteHost"] != "127.0.0.1" || svc["protocol"] != "http" {
		t.Fatalf("defaults not applied: %v", svc)
	}
	h.mustCall("PUT", "/api/services/"+id(svc), map[string]any{"label": "n8n", "remotePort": 5679, "path": "/home"}, 200)

	p2 := h.mustCall("POST", "/api/projects", map[string]string{"name": "Other"}, 201)
	h.mustCall("POST", "/api/servers/"+id(srv)+"/move", map[string]string{"projectId": id(p2)}, 204)

	data := h.mustCall("GET", "/api/data", nil, 200)
	d := data["data"].(map[string]any)
	if len(d["projects"].([]any)) != 2 || len(d["servers"].([]any)) != 1 || len(d["services"].([]any)) != 1 {
		t.Fatalf("unexpected data: %v", d)
	}

	h.mustCall("DELETE", "/api/services/"+id(svc), nil, 204)
	h.mustCall("DELETE", "/api/services/"+id(svc), nil, 404)
	h.mustCall("DELETE", "/api/projects/"+id(p2), nil, 204) // cascades the server
	d = h.mustCall("GET", "/api/data", nil, 200)["data"].(map[string]any)
	if len(d["servers"].([]any)) != 0 {
		t.Fatal("project delete did not cascade")
	}
	// Secrets are checked in cleanup (assertNoSecretsLeaked).
}

func TestSettings(t *testing.T) {
	h := ready(t)
	st := h.mustCall("GET", "/api/settings", nil, 200)
	if st["port"].(float64) != config.DefaultPort {
		t.Fatalf("settings %v", st)
	}
	h.mustCall("PUT", "/api/settings", map[string]any{"port": 80, "autoLockMinutes": 5}, 400)
	m := h.mustCall("PUT", "/api/settings", map[string]any{"port": 50000, "autoLockMinutes": 5, "closeTunnelsOnLock": true}, 200)
	if m["restartRequired"] != true {
		t.Fatal("port change should require a restart")
	}
	saved, err := config.LoadSettings(h.srv.cfg.Paths.Settings)
	if err != nil || saved.Port != 50000 || !saved.CloseTunnelsOnLock {
		t.Fatalf("settings not saved: %+v %v", saved, err)
	}
}

func TestQuit(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.mustCall("POST", "/api/quit", nil, 200)
	select {
	case <-h.quit:
	case <-time.After(2 * time.Second):
		t.Fatal("OnQuit not called")
	}
}

// "When the dashboard tab is closed": with "quit" (the default), a page saying it is closing
// makes TunnelTab quit unless a page is connected after the grace period.
func TestCloseAction(t *testing.T) {
	var shown atomic.Bool
	h := newHarness(t, func(c *Config) {
		c.CloseGrace = 50 * time.Millisecond
		c.TrayShown = shown.Load
	})
	h.login()
	if h.mustCall("GET", "/api/state", nil, 200)["tray"] != false {
		t.Fatal("tray shown")
	}
	shown.Store(true)
	if h.mustCall("GET", "/api/state", nil, 200)["tray"] != true {
		t.Fatal("tray not shown")
	}
	quits := func() bool {
		t.Helper()
		h.mustCall("POST", "/api/closing", nil, 200)
		select {
		case <-h.quit:
			return true
		case <-time.After(300 * time.Millisecond):
			return false
		}
	}
	setAction := func(a string, want int) {
		t.Helper()
		h.mustCall("PUT", "/api/settings", map[string]any{"port": config.DefaultPort, "autoLockMinutes": 15, "closeAction": a}, want)
	}

	setAction("bogus", 400)
	if !quits() {
		t.Fatal("didn't quit by default")
	}
	setAction(config.CloseKeepRunning, 200)
	if quits() {
		t.Fatal("quit with keep running chosen")
	}
	setAction("", 200) // e.g. a settings file from before the option existed
	if !quits() {
		t.Fatal("didn't quit with the option unset")
	}
	setAction(config.CloseQuit, 200)
	if saved, _ := config.LoadSettings(h.srv.cfg.Paths.Settings); saved.CloseAction != config.CloseQuit {
		t.Fatalf("not saved: %+v", saved)
	}

	// Another page still open (or the closing one reloaded): keep running.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", h.base+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+h.session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if quits() {
		t.Fatal("quit while a page is open")
	}
	cancel()
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for h.srv.events.count() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("event stream still counted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !quits() {
		t.Fatal("didn't quit after the last page closed")
	}
}

// --- Tunnels and host keys --------------------------------------------------

// sshSetup creates a project, a server backed by a test SSH server and a
// service pointing at an HTTP backend. The host key is not yet confirmed.
func sshSetup(t *testing.T, h *harness) (sshSrv *sshtest.Server, serverID, serviceID string) {
	t.Helper()
	sshSrv = sshtest.Start(t, sshtest.Options{User: "tester", Password: sshPassword,
		Exec: map[string]string{health.Command: healthOutput}})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from "+r.URL.Path)
	}))
	t.Cleanup(backend.Close)
	bport := backend.Listener.Addr().(*net.TCPAddr).Port

	p := h.mustCall("POST", "/api/projects", map[string]string{"name": "P"}, 201)
	srv := h.mustCall("POST", "/api/servers", map[string]any{
		"projectId": id(p), "name": "s", "host": sshSrv.Host, "port": sshSrv.Port, "username": "tester",
		"auth": map[string]string{"type": "password", "password": sshPassword},
	}, 201)
	svc := h.mustCall("POST", "/api/services", map[string]any{
		"serverId": id(srv), "label": "web", "remotePort": bport, "path": "/admin",
	}, 201)
	return sshSrv, id(srv), id(svc)
}

func TestTunnelWithHostKeyConfirmation(t *testing.T) {
	h := ready(t)
	sshSrv, _, svcID := sshSetup(t, h)

	// First start: unknown host key.
	m := h.mustCall("POST", "/api/services/"+svcID+"/start", nil, http.StatusConflict)
	if m["error"] != "unknown_host_key" || m["fingerprint"] == "" || m["token"] == "" {
		t.Fatalf("got %v", m)
	}
	if sshSrv.Logins() != 0 {
		t.Fatal("logged in before the host key was confirmed")
	}
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]string{"token": "made-up"}, 404)
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": m["token"]}, 200)
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": m["token"]}, 404) // one-time

	// Now it starts, and the returned URL works.
	m = h.mustCall("POST", "/api/services/"+svcID+"/start", nil, 200)
	u := m["url"].(string)
	if !strings.HasPrefix(u, "http://127.0.0.1:") || !strings.HasSuffix(u, "/admin") {
		t.Fatalf("url %q", u)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello from /admin" {
		t.Fatalf("through tunnel: %q", body)
	}

	data := h.mustCall("GET", "/api/data", nil, 200)
	if fw := data["forwards"].([]any); len(fw) != 1 || fw[0].(map[string]any)["state"] != "active" {
		t.Fatalf("forwards %v", fw)
	}
	if kh := data["data"].(map[string]any)["knownHosts"].([]any); len(kh) != 1 {
		t.Fatalf("known hosts %v", kh)
	}

	h.mustCall("POST", "/api/services/"+svcID+"/stop", nil, 204)
	if _, err := http.Get(u); err == nil {
		t.Fatal("tunnel still open after stop")
	}
}

func TestChangedHostKeyNeedsExplicitReplace(t *testing.T) {
	h := ready(t)
	sshSrv, serverID, _ := sshSetup(t, h)
	// Pretend a different key was confirmed earlier.
	h.srv.currentVault().Update(func(d *model.Data) error {
		_, err := d.SetHostKey(model.HostKeyAddress(sshSrv.Host, sshSrv.Port), sshtest.NewHostKey(t).PublicKey())
		return err
	})

	m := h.mustCall("POST", "/api/servers/"+serverID+"/test", nil, http.StatusConflict)
	if m["error"] != "host_key_changed" {
		t.Fatalf("got %v", m)
	}
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": m["token"]}, 400)
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": m["token"], "replace": true}, 200)
	h.mustCall("POST", "/api/servers/"+serverID+"/test", nil, 200)

	// Forgetting the key means the next connection asks again.
	addr := model.HostKeyAddress(sshSrv.Host, sshSrv.Port)
	h.mustCall("POST", "/api/hostkeys/forget", map[string]string{"host": addr}, 204)
	h.mustCall("POST", "/api/hostkeys/forget", map[string]string{"host": addr}, 404)
	if m := h.mustCall("POST", "/api/servers/"+serverID+"/test", nil, http.StatusConflict); m["error"] != "unknown_host_key" {
		t.Fatalf("got %v", m)
	}
}

// A "trust this server?" question answered after the server's confirmed key
// changed (e.g. in another tab) must not replace that key: invariant 6.
func TestStaleHostKeyQuestion(t *testing.T) {
	h := ready(t)
	sshSrv, serverID, _ := sshSetup(t, h)
	addr := model.HostKeyAddress(sshSrv.Host, sshSrv.Port)
	setKey := func(key ssh.PublicKey) {
		t.Helper()
		if err := h.srv.currentVault().Update(func(d *model.Data) error { _, err := d.SetHostKey(addr, key); return err }); err != nil {
			t.Fatal(err)
		}
	}
	keys := func() []string {
		data := h.mustCall("GET", "/api/data", nil, 200)
		var out []string
		for _, k := range data["data"].(map[string]any)["knownHosts"].([]any) {
			out = append(out, k.(map[string]any)["key"].(string))
		}
		return out
	}
	ask := func(want string) any {
		t.Helper()
		m := h.mustCall("POST", "/api/servers/"+serverID+"/test", nil, http.StatusConflict)
		if m["error"] != want {
			t.Fatalf("got %v, want %s", m, want)
		}
		return m["token"]
	}
	keyLine := func(k ssh.PublicKey) string { return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) }

	// Two "new server" questions; the first is answered: the second has
	// nothing left to do and changes nothing.
	first, second := ask("unknown_host_key"), ask("unknown_host_key")
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": first}, 200)
	vaultFile := func() []byte {
		b, err := os.ReadFile(h.srv.cfg.Paths.Vault)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	before := vaultFile()
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": second}, 200)
	if !bytes.Equal(vaultFile(), before) {
		t.Fatal("confirming the already confirmed key saved the vault (and replaced its backup)")
	}
	if got := keys(); len(got) != 1 || got[0] != keyLine(sshSrv.HostKey.PublicKey()) {
		t.Fatalf("known hosts %v", got)
	}

	// A "new server" question, then another key is confirmed: answering the
	// old question must not replace it without the "key changed" warning.
	h.mustCall("POST", "/api/hostkeys/forget", map[string]string{"host": addr}, 204)
	stale := ask("unknown_host_key")
	other := sshtest.NewHostKey(t).PublicKey()
	setKey(other)
	if m := h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": stale}, http.StatusConflict); m["error"] != "host_key_question_stale" {
		t.Fatalf("got %v", m)
	}
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": stale}, 404) // used up
	if got := keys(); len(got) != 1 || got[0] != keyLine(other) {
		t.Fatalf("stale answer changed the known hosts: %v", got)
	}

	// The same for "key changed": its answer applies only to the key it named.
	changed := ask("host_key_changed")
	third := sshtest.NewHostKey(t).PublicKey()
	setKey(third)
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": changed, "replace": true}, http.StatusConflict)
	if got := keys(); len(got) != 1 || got[0] != keyLine(third) {
		t.Fatalf("stale replace changed the known hosts: %v", got)
	}

	// Asked again, the current question works.
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": ask("host_key_changed"), "replace": true}, 200)
	h.mustCall("POST", "/api/servers/"+serverID+"/test", nil, 200)
}

func TestSSHErrors(t *testing.T) {
	h := ready(t)
	sshSrv, serverID, svcID := sshSetup(t, h)
	h.srv.currentVault().Update(func(d *model.Data) error {
		_, err := d.SetHostKey(model.HostKeyAddress(sshSrv.Host, sshSrv.Port), sshSrv.HostKey.PublicKey())
		return err
	})
	h.mustCall("PUT", "/api/servers/"+serverID, map[string]any{
		"name": "s", "host": sshSrv.Host, "port": sshSrv.Port, "username": "tester",
		"auth": map[string]string{"type": "password", "password": "Wrong-Pw-9x"},
	}, 200)
	if m := h.mustCall("POST", "/api/services/"+svcID+"/start", nil, http.StatusBadGateway); m["error"] != "auth_failed" {
		t.Fatalf("got %v", m)
	}
	h.mustCall("POST", "/api/services/"+model.NewID()+"/start", nil, 404)
}

func TestEditingServerOnlyRestartsWhenConnectionChanges(t *testing.T) {
	h := ready(t)
	sshSrv, serverID, svcID := sshSetup(t, h)
	h.srv.currentVault().Update(func(d *model.Data) error {
		_, err := d.SetHostKey(model.HostKeyAddress(sshSrv.Host, sshSrv.Port), sshSrv.HostKey.PublicKey())
		return err
	})
	h.mustCall("POST", "/api/services/"+svcID+"/start", nil, 200)
	edit := func(name, password string) {
		h.mustCall("PUT", "/api/servers/"+serverID, map[string]any{
			"name": name, "host": sshSrv.Host, "port": sshSrv.Port, "username": "tester",
			"auth": map[string]string{"type": "password", "password": password},
		}, 200)
	}

	edit("renamed", "") // blank password = keep the saved one
	if len(h.srv.mgr.Forwards()) != 1 {
		t.Fatal("renaming a server closed its tunnel")
	}
	edit("renamed", sshPassword) // a newly entered password (even the same) is still the same login
	if len(h.srv.mgr.Forwards()) != 1 {
		t.Fatal("re-entering the same password closed the tunnel")
	}
	edit("renamed", "Different-Pw-1") // login changed
	if len(h.srv.mgr.Forwards()) != 0 {
		t.Fatal("changing the login kept the old connection's tunnel")
	}
}

func TestLockClosesTunnelsWhenConfigured(t *testing.T) {
	h := ready(t)
	sshSrv, _, svcID := sshSetup(t, h)
	h.srv.currentVault().Update(func(d *model.Data) error {
		_, err := d.SetHostKey(model.HostKeyAddress(sshSrv.Host, sshSrv.Port), sshSrv.HostKey.PublicKey())
		return err
	})
	h.mustCall("POST", "/api/services/"+svcID+"/start", nil, 200)

	// Default: tunnels keep running while locked.
	h.mustCall("POST", "/api/vault/lock", nil, 200)
	if len(h.srv.mgr.Forwards()) != 1 {
		t.Fatal("tunnel closed on lock although the setting is off")
	}
	h.mustCall("POST", "/api/vault/unlock", map[string]string{"password": masterPW}, 200)

	h.mustCall("PUT", "/api/settings", map[string]any{"port": config.DefaultPort, "autoLockMinutes": 15, "closeTunnelsOnLock": true}, 200)
	h.mustCall("POST", "/api/vault/lock", nil, 200)
	if len(h.srv.mgr.Forwards()) != 0 {
		t.Fatal("tunnel still running after lock with closeTunnelsOnLock")
	}
}

func TestAutoStartAfterUnlock(t *testing.T) {
	h := ready(t)
	sshSrv, serverID, _ := sshSetup(t, h)
	h.srv.currentVault().Update(func(d *model.Data) error {
		_, err := d.SetHostKey(model.HostKeyAddress(sshSrv.Host, sshSrv.Port), sshSrv.HostKey.PublicKey())
		return err
	})
	h.mustCall("POST", "/api/services", map[string]any{"serverId": serverID, "label": "auto", "remotePort": 1, "autoStart": true}, 201)
	h.mustCall("POST", "/api/vault/lock", nil, 200)
	h.mustCall("POST", "/api/vault/unlock", map[string]string{"password": masterPW}, 200)
	deadline := time.Now().Add(5 * time.Second)
	for len(h.srv.mgr.Forwards()) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("auto-start service was not started after unlock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- Events -----------------------------------------------------------------

func TestEventStream(t *testing.T) {
	h := ready(t)
	req, _ := http.NewRequest("GET", h.base+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+h.session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	events := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				events <- line
			}
		}
		close(events)
	}()
	expect := func(want string) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatalf("stream ended waiting for %s", want)
				}
				if strings.Contains(ev, want) {
					return
				}
			case <-timeout:
				t.Fatalf("no %s event", want)
			}
		}
	}

	h.mustCall("POST", "/api/projects", map[string]string{"name": "x"}, 201)
	expect(`"type":"data"`)
	h.mustCall("POST", "/api/vault/lock", nil, 200)
	expect(`"state":"locked"`)
	h.mustCall("POST", "/api/vault/unlock", map[string]string{"password": masterPW}, 200)
	expect(`"state":"unlocked"`)

	// Unauthenticated streams are refused.
	status, _ := h.request("GET", "/api/events", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated events: %d", status)
	}
}

func TestBrokerDropsSlowSubscribers(t *testing.T) {
	b := newBroker()
	ch := b.subscribe()
	for i := 0; i < 200; i++ {
		b.publish(dataEvent{Type: "data"}) // nobody reads: must not block
	}
	var sawResync bool
	for len(ch) > 0 {
		if string(<-ch) == `{"type":"resync"}` {
			sawResync = true
		}
	}
	if !sawResync {
		t.Fatal("overflowing subscriber was not told to resync")
	}
	b.close()
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed")
	}
}

func TestOrderEndpoints(t *testing.T) {
	h := ready(t)
	p1 := id(h.mustCall("POST", "/api/projects", map[string]string{"name": "One"}, 201))
	p2 := id(h.mustCall("POST", "/api/projects", map[string]string{"name": "Two"}, 201))
	srv := func(project, name string) string {
		return id(h.mustCall("POST", "/api/servers", map[string]any{"projectId": project, "name": name, "host": "h", "port": 22,
			"username": "u", "auth": map[string]string{"type": "agent"}}, 201))
	}
	a, b, c := srv(p1, "a"), srv(p1, "b"), srv(p2, "c")
	svc := func(name string) string {
		return id(h.mustCall("POST", "/api/services", map[string]any{"serverId": a, "label": name, "remotePort": 80}, 201))
	}
	x, y := svc("x"), svc("y")

	h.mustCall("PUT", "/api/projects/order", map[string]any{"ids": []string{p2, p1}}, 204)
	h.mustCall("PUT", "/api/projects/"+p1+"/servers/order", map[string]any{"ids": []string{b, c, a}}, 204) // c moves in
	h.mustCall("PUT", "/api/servers/"+a+"/services/order", map[string]any{"ids": []string{y, x}}, 204)
	h.mustCall("PUT", "/api/projects/order", map[string]any{"ids": []string{p1}}, 400) // incomplete
	h.mustCall("PUT", "/api/servers/"+model.NewID()+"/services/order", map[string]any{"ids": []string{}}, 404)

	d := h.mustCall("GET", "/api/data", nil, 200)["data"].(map[string]any)
	order := func(list []any, key string, filter func(map[string]any) bool) string {
		type item struct {
			id    string
			order float64
		}
		var items []item
		for _, v := range list {
			m := v.(map[string]any)
			if filter(m) {
				items = append(items, item{m[key].(string), m["order"].(float64)})
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].order < items[j].order })
		var out []string
		for _, it := range items {
			out = append(out, it.id)
		}
		return strings.Join(out, ",")
	}
	all := func(map[string]any) bool { return true }
	if got := order(d["projects"].([]any), "id", all); got != p2+","+p1 {
		t.Errorf("projects %s", got)
	}
	if got := order(d["servers"].([]any), "id", func(m map[string]any) bool { return m["projectId"] == p1 }); got != b+","+c+","+a {
		t.Errorf("servers %s", got)
	}
	if got := order(d["services"].([]any), "id", all); got != y+","+x {
		t.Errorf("services %s", got)
	}
}

func TestServerNotesAPI(t *testing.T) {
	h := ready(t)
	_, serverID, _ := sshSetup(t, h)
	h.mustCall("PUT", "/api/servers/"+serverID+"/notes", map[string]string{"notes": "Backups at 02:00"}, 204)
	h.mustCall("PUT", "/api/servers/"+serverID+"/notes", map[string]string{"notes": "bad \a"}, 400)
	h.mustCall("PUT", "/api/servers/nope/notes", map[string]string{"notes": "x"}, 404)

	// Saved in the vault: still there after locking and unlocking.
	h.mustCall("POST", "/api/vault/lock", nil, 200)
	h.mustCall("PUT", "/api/servers/"+serverID+"/notes", map[string]string{"notes": "x"}, 423)
	h.mustCall("POST", "/api/vault/unlock", map[string]string{"password": masterPW}, 200)
	servers := h.mustCall("GET", "/api/data", nil, 200)["data"].(map[string]any)["servers"].([]any)
	if notes := servers[0].(map[string]any)["notes"]; notes != "Backups at 02:00" {
		t.Fatalf("notes %v", notes)
	}
}

func TestTrafficAPI(t *testing.T) {
	h := ready(t)
	sshSrv, _, serviceID := sshSetup(t, h)
	h.srv.currentVault().Update(func(d *model.Data) error {
		_, err := d.SetHostKey(model.HostKeyAddress(sshSrv.Host, sshSrv.Port), sshSrv.HostKey.PublicKey())
		return err
	})
	if m := h.mustCall("GET", "/api/traffic", nil, 200); len(m["services"].([]any)) != 0 {
		t.Fatalf("traffic before any: %v", m)
	}
	fwd := h.mustCall("POST", "/api/services/"+serviceID+"/start", nil, 200)["forward"].(map[string]any)
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/x", int(fwd["localPort"].(float64))))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		list := h.mustCall("GET", "/api/traffic", nil, 200)["services"].([]any)
		if len(list) == 1 && list[0].(map[string]any)["todayIn"].(float64) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no traffic counted: %v", list)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// healthOutput is what health.Command prints on a small Linux VPS.
const healthOutput = "@@loadavg\n0.42 0.38 0.31 1/234 5678\n@@meminfo\nMemTotal: 4028488 kB\nMemAvailable: 1587652 kB\n" +
	"@@uptime\n1987654.32 3456789.01\n@@nproc\n2\n@@df\nFilesystem 1024-blocks Used Available Capacity Mounted on\n" +
	"/dev/sda1 81106868 22020096 59070388 28% /\n"

func TestServerHealth(t *testing.T) {
	h := ready(t)
	sshSrv, serverID, serviceID := sshSetup(t, h)
	h.srv.currentVault().Update(func(d *model.Data) error {
		_, err := d.SetHostKey(model.HostKeyAddress(sshSrv.Host, sshSrv.Port), sshSrv.HostKey.PublicKey())
		return err
	})
	dashboard := h.srv.events.subscribe() // a dashboard is open
	defer h.srv.events.unsubscribe(dashboard)
	readings := func() map[string]any { return h.mustCall("GET", "/api/data", nil, 200)["health"].(map[string]any) }
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Off by default: nothing is read, even while connected.
	h.mustCall("POST", "/api/services/"+serviceID+"/start", nil, 200)
	h.srv.checkAllHealth()
	time.Sleep(200 * time.Millisecond)
	if len(readings()) != 0 {
		t.Fatal("health read while switched off")
	}

	// On: read right away, over the existing connection.
	logins := sshSrv.Logins()
	h.mustCall("PUT", "/api/servers/"+serverID+"/health", map[string]bool{"enabled": true}, 204)
	waitFor("a reading", func() bool {
		r, ok := readings()[serverID].(map[string]any)
		return ok && r["health"] != nil && r["health"].(map[string]any)["memTotalKB"] == float64(4028488)
	})
	if sshSrv.Logins() != logins {
		t.Fatal("the health check opened a new connection")
	}

	// Off: dropped at once, and not read again.
	h.mustCall("PUT", "/api/servers/"+serverID+"/health", map[string]bool{"enabled": false}, 204)
	if len(readings()) != 0 {
		t.Fatal("reading kept after switching off")
	}
	h.srv.checkAllHealth()
	time.Sleep(200 * time.Millisecond)
	if len(readings()) != 0 {
		t.Fatal("read again after switching off")
	}

	// Not connected: switching on never connects.
	h.mustCall("POST", "/api/services/"+serviceID+"/stop", nil, 204)
	waitFor("disconnect", func() bool { return len(h.srv.mgr.Servers()) == 0 })
	logins = sshSrv.Logins()
	h.mustCall("PUT", "/api/servers/"+serverID+"/health", map[string]bool{"enabled": true}, 204)
	h.srv.checkAllHealth()
	time.Sleep(300 * time.Millisecond)
	if sshSrv.Logins() != logins || len(readings()) != 0 {
		t.Fatal("the health check connected to a server that wasn't connected")
	}
	// Connecting again (for a tunnel) reads it right away.
	h.mustCall("POST", "/api/services/"+serviceID+"/start", nil, 200)
	waitFor("a reading after reconnecting", func() bool { _, ok := readings()[serverID]; return ok })

	// No dashboard open: nothing is read.
	h.srv.events.unsubscribe(dashboard)
	h.srv.setHealth(serverID, nil)
	h.srv.checkAllHealth()
	time.Sleep(300 * time.Millisecond)
	if len(h.srv.healthReadings()) != 0 {
		t.Fatal("read with no dashboard open")
	}
}

func TestServerConnect(t *testing.T) {
	h := ready(t)
	sshSrv, serverID, serviceID := sshSetup(t, h)
	dashboard := h.srv.events.subscribe() // a dashboard is open
	defer h.srv.events.unsubscribe(dashboard)
	readings := func() map[string]any { return h.mustCall("GET", "/api/data", nil, 200)["health"].(map[string]any) }
	held := func() bool {
		for _, s := range h.mustCall("GET", "/api/data", nil, 200)["servers"].([]any) {
			if st := s.(map[string]any); st["id"] == serverID {
				return st["held"] == true
			}
		}
		return false
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// An unknown host key asks for confirmation, like starting a service.
	if code, body := h.call("POST", "/api/servers/"+serverID+"/connect", nil); code != 409 || body["error"] != "unknown_host_key" {
		t.Fatalf("connect to an unconfirmed server: %d", code)
	}
	h.srv.currentVault().Update(func(d *model.Data) error {
		_, err := d.SetHostKey(model.HostKeyAddress(sshSrv.Host, sshSrv.Port), sshSrv.HostKey.PublicKey())
		return err
	})
	h.mustCall("POST", "/api/servers/nope/connect", nil, 404)

	// Connect: connects and reads health, though it's off in Settings.
	h.mustCall("POST", "/api/servers/"+serverID+"/connect", nil, 204)
	if !held() {
		t.Fatal("not held after Connect")
	}
	waitFor("a reading", func() bool { r, ok := readings()[serverID].(map[string]any); return ok && r["health"] != nil })

	// Disconnect: reading dropped and the connection closed.
	h.mustCall("DELETE", "/api/servers/"+serverID+"/connect", nil, 204)
	if len(readings()) != 0 || held() {
		t.Fatal("reading or hold kept after Disconnect")
	}
	waitFor("disconnect", func() bool { return sshSrv.ActiveConnections() == 0 })

	// With a tunnel open and health off, Disconnect keeps the tunnel's
	// connection but stops reading.
	h.mustCall("POST", "/api/services/"+serviceID+"/start", nil, 200)
	h.mustCall("POST", "/api/servers/"+serverID+"/connect", nil, 204)
	waitFor("a reading", func() bool { _, ok := readings()[serverID]; return ok })
	h.mustCall("DELETE", "/api/servers/"+serverID+"/connect", nil, 204)
	h.srv.checkAllHealth()
	time.Sleep(200 * time.Millisecond)
	if len(readings()) != 0 || len(h.srv.mgr.Servers()) != 1 {
		t.Fatalf("readings %v, connections %v", readings(), h.srv.mgr.Servers())
	}
	h.mustCall("POST", "/api/services/"+serviceID+"/stop", nil, 204)

	// No dashboard open: the hold is let go.
	h.mustCall("POST", "/api/servers/"+serverID+"/connect", nil, 204)
	h.srv.events.unsubscribe(dashboard)
	h.srv.checkAllHealth()
	if len(h.srv.mgr.Held()) != 0 {
		t.Fatal("hold kept with no dashboard open")
	}
	waitFor("disconnect", func() bool { return sshSrv.ActiveConnections() == 0 })
}

func TestInstanceStarted(t *testing.T) {
	h := newHarness(t)
	port := h.ts.Listener.Addr().(*net.TCPAddr).Port
	if err := ConfirmStarted(port, "wrong"); err == nil {
		t.Fatal("wrong instance secret accepted")
	}
	select {
	case <-h.srv.UpdateConfirmed():
		t.Fatal("confirmed without the instance secret")
	default:
	}
	for i := 0; i < 2; i++ { // a repeated confirmation is harmless
		if err := ConfirmStarted(port, instanceSecret); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-h.srv.UpdateConfirmed():
	default:
		t.Fatal("not confirmed")
	}
}
