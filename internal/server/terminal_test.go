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

	// No Origin (not a browser) and a foreign Origin are refused, and a
	// refused attempt doesn't use up... nothing: the ticket is only taken
	// after the Origin checks pass.
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
	waitShells(t, h, 0) // closing the socket closed the shell

	// An unused ticket expires and its shell is closed.
	old := terminalTicketTTL
	terminalTicketTTL = 50 * time.Millisecond
	defer func() { terminalTicketTTL = old }()
	stale := openTicket(t, h, serverID)
	waitShells(t, h, 0)
	if _, resp, err := dialTerminal(h, stale, h.base); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired ticket: %v %v", resp, err)
	}
}

func TestTerminalClosesOnLock(t *testing.T) {
	h := ready(t)
	serverID := trustedServer(t, h)
	c, _, err := dialTerminal(h, openTicket(t, h, serverID), h.base)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	readUntil(t, c, "$ ")

	h.mustCall("POST", "/api/vault/lock", nil, 200)
	exit := readExit(t, c)
	if exit["message"] != "TunnelTab was locked" {
		t.Fatalf("exit %v", exit)
	}
	waitShells(t, h, 0)
	h.mustCall("POST", "/api/terminals", map[string]any{"serverId": serverID, "cols": 80, "rows": 24}, http.StatusLocked)
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
	for path, wantInline := range map[string]bool{"/terminal.html": true, "/": false, "/index.html": false} {
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
