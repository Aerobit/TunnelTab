package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/Aerobit/TunnelTab/internal/model"
)

// syncBuffer is a bytes.Buffer safe for the logger's concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestLogsContainNoSecretsOrAddresses runs every kind of action with a
// debug-level logger and checks the log never contains secrets, tokens,
// fingerprints or server addresses (logs are not encrypted).
func TestLogsContainNoSecretsOrAddresses(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := newHarness(t, func(c *Config) { c.Logger = logger })
	launch, _ := url.Parse(h.srv.LaunchURL())
	h.login()
	h.mustCall("POST", "/api/vault/create", map[string]string{"password": masterPW}, 200)
	h.mustCall("POST", "/api/vault/lock", nil, 200)
	h.mustCall("POST", "/api/vault/unlock", map[string]string{"password": "wrong password!"}, 401)
	time.Sleep(250 * time.Millisecond)
	h.mustCall("POST", "/api/vault/unlock", map[string]string{"password": masterPW}, 200)

	sshSrv, serverID, svcID := sshSetup(t, h)
	m := h.mustCall("POST", "/api/services/"+svcID+"/start", nil, http.StatusConflict)
	h.mustCall("POST", "/api/hostkeys/confirm", map[string]any{"token": m["token"]}, 200)
	st := h.mustCall("POST", "/api/services/"+svcID+"/start", nil, 200)
	resp, err := http.Get(st["url"].(string))
	if err == nil {
		resp.Body.Close()
	}
	c, _, err := dialTerminal(h, openTicket(t, h, serverID), h.base)
	if err != nil {
		t.Fatal(err)
	}
	c.Write(context.Background(), websocket.MessageBinary, []byte("exit\r"))
	readExit(t, c)
	h.mustCall("PUT", "/api/servers/"+serverID, map[string]any{
		"name": "s", "host": sshSrv.Host, "port": sshSrv.Port, "username": "tester",
		"auth": map[string]string{"type": "password", "password": "Wrong-Pw-Leak-Check"},
	}, 200)
	h.mustCall("POST", "/api/services/"+svcID+"/start", nil, http.StatusBadGateway)
	h.mustCall("POST", "/api/hostkeys/forget", map[string]string{"host": model.HostKeyAddress(sshSrv.Host, sshSrv.Port)}, 204)
	h.srv.Close()

	log := buf.String()
	if !strings.Contains(log, "vault unlocked") || !strings.Contains(log, "forward started") {
		t.Fatalf("log looks empty; the test isn't exercising anything:\n%s", log)
	}
	forbidden := map[string]string{
		"master password":      masterPW,
		"SSH password":         sshPassword,
		"new SSH password":     "Wrong-Pw-Leak-Check",
		"launch token":         launch.Query().Get("launch"),
		"session token":        h.session,
		"server address":       "127.0.0.1",
		"server port":          ":" + strconv.Itoa(sshSrv.Port),
		"fingerprint":          ssh.FingerprintSHA256(sshSrv.HostKey.PublicKey()),
		"fingerprint (prefix)": "SHA256:",
	}
	for what, s := range forbidden {
		if s != "" && strings.Contains(log, s) {
			t.Errorf("log contains the %s (%q):\n%s", what, s, log)
		}
	}
}

func TestParallelUnlockAttemptsAreRateLimited(t *testing.T) {
	h := ready(t)
	h.mustCall("POST", "/api/vault/lock", nil, 200)
	var wg sync.WaitGroup
	var mu sync.Mutex
	statuses := map[int]int{}
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, _ := h.call("POST", "/api/vault/unlock", map[string]string{"password": "guess-" + strconv.Itoa(i)})
			mu.Lock()
			statuses[st]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if statuses[http.StatusUnauthorized] != 1 || statuses[http.StatusTooManyRequests] != 5 {
		t.Fatalf("statuses %v; want exactly one password check, the rest rate-limited", statuses)
	}
}

func TestTerminalTypingPostponesAutoLock(t *testing.T) {
	old := terminalTouchEvery
	terminalTouchEvery = 0
	defer func() { terminalTouchEvery = old }()

	h := ready(t)
	serverID := trustedServer(t, h)
	c, _, err := dialTerminal(h, openTicket(t, h, serverID), h.base)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	v := h.srv.currentVault()
	v.SetAutoLock(300 * time.Millisecond)

	for i := 0; i < 12; i++ { // ~600 ms of typing
		c.Write(context.Background(), websocket.MessageBinary, []byte("x"))
		time.Sleep(50 * time.Millisecond)
		if v.LockIfIdle() {
			t.Fatal("auto-locked while typing in a terminal")
		}
	}
	time.Sleep(400 * time.Millisecond)
	if !v.LockIfIdle() {
		t.Fatal("did not auto-lock after typing stopped")
	}
}

func TestNoDirectoryListings(t *testing.T) {
	h := newHarness(t)
	for path, want := range map[string]int{
		"/vendor/xterm/":          404,
		"/vendor/":                404,
		"/js/":                    404,
		"/vendor/xterm/xterm.css": 200,
		"/":                       200,
	} {
		resp, err := http.Get(h.base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
}
