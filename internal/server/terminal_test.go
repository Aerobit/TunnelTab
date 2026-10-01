package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Aerobit/TunnelTab/internal/model"
)

// trustedServer creates a server backed by a test SSH server whose host key
// is already confirmed, and returns its ID.
func trustedServer(t *testing.T, h *harness) string {
	t.Helper()
	sshSrv, serverID, _ := sshSetup(t, h)
	h.srv.currentVault().Update(func(d *model.Data) error {
		_, err := d.SetHostKey(model.HostKeyAddress(sshSrv.Host, sshSrv.Port), sshSrv.HostKey.PublicKey())
		return err
	})
	return serverID
}

func openTicket(t *testing.T, h *harness, serverID string) string {
	t.Helper()
	m := h.mustCall("POST", "/api/terminals", map[string]any{"serverId": serverID, "cols": 80, "rows": 24}, 200)
	if m["serverName"] != "s" {
		t.Fatalf("serverName %v", m["serverName"])
	}
	return m["ticket"].(string)
}

func dialTerminal(h *harness, ticket, origin string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	url := "ws" + strings.TrimPrefix(h.base, "http") + "/api/terminals/connect?ticket=" + ticket
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: hdr})
}

// readUntil reads terminal output until it contains want; it returns any
// text (control) messages seen.
func readUntil(t *testing.T, c *websocket.Conn, want string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out strings.Builder
	var control []string
	for !strings.Contains(out.String(), want) {
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v (got %q, control %v)", want, err, out.String(), control)
		}
		if typ == websocket.MessageBinary {
			out.Write(data)
		} else {
			control = append(control, string(data))
			if want == "" {
				return control
			}
		}
	}
	return control
}

// readExit reads until the exit control message and returns it.
func readExit(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("no exit message: %v", err)
		}
		if typ == websocket.MessageText {
			var m map[string]any
			json.Unmarshal(data, &m)
			if m["type"] == "exit" {
				return m
			}
		}
	}
}

func TestTerminalSession(t *testing.T) {
	h := ready(t)
	serverID := trustedServer(t, h)
	c, _, err := dialTerminal(h, openTicket(t, h, serverID), h.base)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	ctx := context.Background()

	readUntil(t, c, "$ ")
	// The dashboard lists the open terminal.
	if m := h.mustCall("GET", "/api/data", nil, 200); len(m["terminals"].([]any)) != 1 {
		t.Fatalf("terminals: %v", m["terminals"])
	}
	c.Write(ctx, websocket.MessageBinary, []byte("echo over websocket\r"))
	readUntil(t, c, "over websocket\r\n")

	c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":30}`))
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.Write(ctx, websocket.MessageBinary, []byte("size\r"))
		readCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		_, data, err := c.Read(readCtx)
		cancel()
		if err == nil && strings.Contains(string(data), "100x30") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resize never reached the shell")
		}
	}

	c.Write(ctx, websocket.MessageBinary, []byte("exit 5\r"))
	exit := readExit(t, c)
	if exit["code"].(float64) != 5 || exit["message"] != "" {
		t.Fatalf("exit %v", exit)
	}
	waitShells(t, h, 0)

	// Gone from the list; opening and ending it are in the activity log.
	m := h.mustCall("GET", "/api/data", nil, 200)
	if len(m["terminals"].([]any)) != 0 {
		t.Fatalf("terminals after exit: %v", m["terminals"])
	}
	var states []string
	for _, e := range m["activity"].([]any) {
		if e := e.(map[string]any); e["kind"] == "terminal" {
			states = append(states, e["state"].(string))
		}
	}
	if strings.Join(states, ",") != "opened,ended" {
		t.Fatalf("terminal activity: %v", states)
	}
}

func waitShells(t *testing.T, h *harness, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.srv.mgr.ShellCount() != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d terminals open, want %d", h.srv.mgr.ShellCount(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTerminalTicketChecks(t *testing.T) {
	h := ready(t)
	serverID := trustedServer(t, h)

	// No Origin (not a browser) and a foreign Origin are refused before the
	// ticket is used up.
	ticket := openTicket(t, h, serverID)
	if _, resp, err := dialTerminal(h, ticket, ""); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no Origin: %v %v", resp, err)
	}
	if _, resp, err := dialTerminal(h, ticket, "http://127.0.0.1:5678"); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tunneled-app Origin: %v %v", resp, err)
	}

	// The ticket works once.
	c, _, err := dialTerminal(h, ticket, h.base)
	if err != nil {
		t.Fatal(err)
	}
	c.CloseNow()
	if _, resp, err := dialTerminal(h, ticket, h.base); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused ticket: %v %v", resp, err)
	}
	if _, resp, err := dialTerminal(h, "made-up", h.base); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("made-up ticket: %v %v", resp, err)
	}

	// An expired ticket is refused.
	h.clock.freeze()
	stale := openTicket(t, h, serverID)
	h.clock.advance(terminalTicketTTL + time.Second)
	if _, resp, err := dialTerminal(h, stale, h.base); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired ticket: %v %v", resp, err)
	}
}

// openTerminal opens a session through the API and returns its ID and a
// connected WebSocket that has seen the prompt.
func openTerminal(t *testing.T, h *harness, serverID string) (string, *websocket.Conn) {
	t.Helper()
	m := h.mustCall("POST", "/api/terminals", map[string]any{"serverId": serverID, "cols": 80, "rows": 24}, 200)
	c, _, err := dialTerminal(h, m["ticket"].(string), h.base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	readUntil(t, c, "$ ")
	return m["terminalId"].(string), c
}

// attachTerminal re-attaches to a session and returns the connection and
// the replayed output.
func attachTerminal(t *testing.T, h *harness, id, want string) *websocket.Conn {
	t.Helper()
	m := h.mustCall("POST", "/api/terminals/"+id+"/attach", nil, 200)
	c, _, err := dialTerminal(h, m["ticket"].(string), h.base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	control := readUntil(t, c, want)
	if len(control) == 0 || control[0] != `{"type":"attached"}` {
		t.Fatalf("expected an attached message first, got %v", control)
	}
	return c
}

func TestTerminalSurvivesLock(t *testing.T) {
	h := ready(t)
	serverID := trustedServer(t, h)
	id, c := openTerminal(t, h, serverID)
	ctx := context.Background()
	c.Write(ctx, websocket.MessageBinary, []byte("echo started before lock\r"))
	readUntil(t, c, "started before lock\r\n")

	// Start a long job (like "docker pull"), then lock while it runs.
	c.Write(ctx, websocket.MessageBinary, []byte("count 8\r"))
	readUntil(t, c, "tick 1\r\n")

	// Locking hides the terminal but keeps the shell (and the job) running.
	h.mustCall("POST", "/api/vault/lock", nil, 200)
	if msg := readText(t, c); msg != `{"type":"locked"}` {
		t.Fatalf("expected a locked message, got %s", msg)
	}
	if h.srv.mgr.ShellCount() != 1 {
		t.Fatalf("%d shells after lock, want 1 (still running)", h.srv.mgr.ShellCount())
	}
	h.mustCall("POST", "/api/terminals/"+id+"/attach", nil, http.StatusLocked)
	time.Sleep(2 * time.Second) // the job finishes while locked

	// After unlocking, the same shell is re-attached and everything the job
	// printed — including while locked — is replayed.
	h.mustCall("POST", "/api/vault/unlock", map[string]string{"password": masterPW}, 200)
	c2 := attachTerminal(t, h, id, "tick 8\r\n")
	c2.Write(ctx, websocket.MessageBinary, []byte("echo same shell after unlock\r"))
	readUntil(t, c2, "same shell after unlock\r\n")
	if h.srv.mgr.ShellCount() != 1 {
		t.Fatalf("%d shells, want the same single one", h.srv.mgr.ShellCount())
	}
}

func TestTerminalReattachAfterReload(t *testing.T) {
	h := ready(t)
	serverID := trustedServer(t, h)
	id, c := openTerminal(t, h, serverID)
	c.Write(context.Background(), websocket.MessageBinary, []byte("echo before reload\r"))
	readUntil(t, c, "before reload\r\n")
	c.CloseNow() // the page reloads or the network blips

	c2 := attachTerminal(t, h, id, "before reload")
	c2.Write(context.Background(), websocket.MessageBinary, []byte("exit 4\r"))
	if exit := readExit(t, c2); exit["code"].(float64) != 4 {
		t.Fatalf("exit %v", exit)
	}
	waitShells(t, h, 0)
	h.mustCall("POST", "/api/terminals/"+id+"/attach", nil, 404)
}

func TestTerminalSecondTabTakesOver(t *testing.T) {
	h := ready(t)
	serverID := trustedServer(t, h)
	id, first := openTerminal(t, h, serverID)
	second := attachTerminal(t, h, id, "$ ")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := first.Read(ctx); err != nil {
			break // the first page was disconnected
		}
	}
	second.Write(context.Background(), websocket.MessageBinary, []byte("echo still here\r"))
	readUntil(t, second, "still here\r\n")
}

func TestTerminalExplicitClose(t *testing.T) {
	h := ready(t)
	serverID := trustedServer(t, h)
	id, c := openTerminal(t, h, serverID)
	h.mustCall("DELETE", "/api/terminals/"+id, nil, 204)
	if exit := readExit(t, c); exit["message"] != "the terminal was closed by TunnelTab" {
		t.Fatalf("exit %v", exit)
	}
	waitShells(t, h, 0)
	h.mustCall("DELETE", "/api/terminals/"+id, nil, 404)
}

func TestTerminalClosesWhenItsTabIsGone(t *testing.T) {
	h := ready(t)
	serverID := trustedServer(t, h)
	h.clock.freeze()
	id, c := openTerminal(t, h, serverID)
	stopWatching := h.srv.watchTerminal(id) // the tab's event stream
	c.CloseNow()                            // WebSocket gone (e.g. locked)
	waitDetached(t, h)

	// The tab is still open: kept, even while locked and long past the grace.
	h.mustCall("POST", "/api/vault/lock", nil, 200)
	h.clock.advance(10 * terminalDetachGrace)
	h.srv.reapTerminalsOnce()
	if h.srv.mgr.ShellCount() != 1 {
		t.Fatal("closed a terminal whose tab is still open")
	}

	// The tab is closed: the session ends after the grace, even while locked.
	stopWatching()
	h.srv.reapTerminalsOnce()
	if h.srv.mgr.ShellCount() != 1 {
		t.Fatal("closed before the grace period (a reload would lose the session)")
	}
	h.clock.advance(terminalDetachGrace + time.Second)
	h.srv.reapTerminalsOnce()
	waitShells(t, h, 0)
}

func TestEventStreamKeepsTerminalWatched(t *testing.T) {
	h := ready(t)
	serverID := trustedServer(t, h)
	id, _ := openTerminal(t, h, serverID)
	watchers := func() int {
		s := h.srv.terminal(id)
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.watchers
	}

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", h.base+"/api/events?terminal="+id, nil)
	req.Header.Set("Authorization", "Bearer "+h.session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	waitFor := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for watchers() != want {
			if time.Now().After(deadline) {
				t.Fatalf("watchers %d, want %d", watchers(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor(1)
	cancel() // the tab closes
	resp.Body.Close()
	waitFor(0)
}

// readText returns the next control (text) message, skipping output.
func readText(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("no control message: %v", err)
		}
		if typ == websocket.MessageText {
			return string(data)
		}
	}
}

// waitDetached waits until no page is attached to any terminal.
func waitDetached(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		attached := false
		for _, s := range h.srv.sessionsSnapshot() {
			s.mu.Lock()
			attached = attached || s.conn != nil
			s.mu.Unlock()
		}
		if !attached {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal still attached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTerminalErrors(t *testing.T) {
	h := ready(t)
	sshSrv, serverID, _ := sshSetup(t, h) // host key not confirmed yet
	m := h.mustCall("POST", "/api/terminals", map[string]any{"serverId": serverID, "cols": 80, "rows": 24}, http.StatusConflict)
	if m["error"] != "unknown_host_key" || sshSrv.Logins() != 0 {
		t.Fatalf("got %v, logins %d", m, sshSrv.Logins())
	}
	h.mustCall("POST", "/api/terminals", map[string]any{"serverId": model.NewID(), "cols": 80, "rows": 24}, 404)
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": m["token"]}, 200)
	if m := h.mustCall("POST", "/api/terminals", map[string]any{"serverId": serverID, "cols": 0, "rows": 24}, http.StatusBadGateway); !strings.Contains(m["message"].(string), "size") {
		t.Fatalf("bad size: %v", m)
	}
}

func TestTerminalPageCSP(t *testing.T) {
	h := newHarness(t)
	// Inline styles (for xterm.js) only on the two pages that show terminals.
	for path, wantInline := range map[string]bool{"/terminal.html": true, "/": true, "/index.html": true, "/logo.svg": false, "/js/app.js": false} {
		resp, err := http.Get(h.base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self';") {
			t.Errorf("%s: scripts not restricted: %s", path, csp)
		}
		if got := strings.Contains(csp, "'unsafe-inline'"); got != wantInline {
			t.Errorf("%s: unsafe-inline=%v, want %v (%s)", path, got, wantInline, csp)
		}
	}
}

func TestTerminalOwnedByDashboardTab(t *testing.T) {
	h := ready(t)
	serverID := trustedServer(t, h)
	h.clock.freeze()
	const client = "dashboard-tab-0123456789"

	h.mustCall("POST", "/api/terminals", map[string]any{"serverId": serverID, "cols": 80, "rows": 24, "client": "bad id!"}, 400)
	m := h.mustCall("POST", "/api/terminals", map[string]any{"serverId": serverID, "cols": 80, "rows": 24, "client": client}, 200)
	c, _, err := dialTerminal(h, m["ticket"].(string), h.base)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, c, "$ ")
	if list := h.mustCall("GET", "/api/data", nil, 200)["terminals"].([]any); len(list) != 1 || list[0].(map[string]any)["client"] != client {
		t.Fatalf("terminals: %v", list)
	}

	// The dashboard tab is open (its event stream): the terminal is kept with
	// no page attached, while locked and long past the grace period.
	stop := h.srv.watchClient(client)
	c.CloseNow()
	waitDetached(t, h)
	h.mustCall("POST", "/api/vault/lock", nil, 200)
	h.clock.advance(10 * terminalDetachGrace)
	h.srv.reapTerminalsOnce()
	if h.srv.mgr.ShellCount() != 1 {
		t.Fatal("closed a terminal whose dashboard tab is still open")
	}

	// The dashboard tab is closed: kept for the grace period (a reload), then closed.
	stop()
	h.srv.reapTerminalsOnce()
	if h.srv.mgr.ShellCount() != 1 {
		t.Fatal("closed before the grace period")
	}
	h.clock.advance(terminalDetachGrace + time.Second)
	h.srv.reapTerminalsOnce()
	waitShells(t, h, 0)
}

func TestEventStreamWatchesClient(t *testing.T) {
	h := ready(t)
	const client = "dashboard-tab-0123456789"
	streams := func() int {
		h.srv.terms.mu.Lock()
		defer h.srv.terms.mu.Unlock()
		if c := h.srv.terms.clients[client]; c != nil {
			return c.streams
		}
		return 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", h.base+"/api/events?client="+client, nil)
	req.Header.Set("Authorization", "Bearer "+h.session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for streams() != want {
			if time.Now().After(deadline) {
				t.Fatalf("streams %d, want %d", streams(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitUntil(1)
	cancel()
	resp.Body.Close()
	waitUntil(0)
}
