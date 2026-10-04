package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/Aerobit/TunnelTab/internal/model"
	"github.com/Aerobit/TunnelTab/internal/sshx/sshtest"
)

const (
	user     = "tester"
	password = "s3cret-Pa55word"
)

// --- Test harness -----------------------------------------------------------

// env is a fake vault: servers, confirmed host keys and a "locked" switch,
// exposed to the Manager through a TargetFunc.
type env struct {
	mu      sync.Mutex
	servers map[string]model.Server
	known   []model.KnownHost
	paused  bool
	events  []Event
}

func newEnv() *env { return &env{servers: map[string]model.Server{}} }

func (e *env) targets(id string) (Target, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.paused {
		return Target{}, ErrPaused
	}
	s, ok := e.servers[id]
	if !ok {
		return Target{}, errors.New("no such server")
	}
	addr := model.HostKeyAddress(s.Host, s.Port)
	var known []model.KnownHost
	for _, k := range e.known {
		if k.Host == addr {
			known = append(known, k)
		}
	}
	return Target{Server: s, KnownHosts: known}, nil
}

func (e *env) addServer(srv *sshtest.Server, auth model.Auth) model.Server {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := model.Server{ID: model.NewID(), Name: "test", Host: srv.Host, Port: srv.Port, Username: user, Auth: auth}
	e.servers[s.ID] = s
	return s
}

func (e *env) setAuth(serverID string, auth model.Auth) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.servers[serverID]
	s.Auth = auth
	e.servers[serverID] = s
}

func (e *env) trust(host string, port int, key ssh.PublicKey) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := model.Data{KnownHosts: e.known}
	if _, err := d.SetHostKey(model.HostKeyAddress(host, port), key); err != nil {
		panic(err)
	}
	e.known = d.KnownHosts
}

func (e *env) setPaused(p bool) {
	e.mu.Lock()
	e.paused = p
	e.mu.Unlock()
}

func (e *env) record(ev Event) {
	e.mu.Lock()
	e.events = append(e.events, ev)
	e.mu.Unlock()
}

func (e *env) sawEvent(kind, id string, st State) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		if ev.Kind == kind && ev.ID == id && ev.State == st {
			return true
		}
	}
	return false
}

func newManager(t *testing.T, e *env, tweak func(*Config)) *Manager {
	t.Helper()
	cfg := Config{
		Targets:           e.targets,
		OnEvent:           e.record,
		DialTimeout:       5 * time.Second,
		ReconnectMin:      10 * time.Millisecond,
		ReconnectMax:      100 * time.Millisecond,
		KeepAliveInterval: time.Hour,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	m := NewManager(cfg)
	t.Cleanup(m.Close)
	return m
}

// passwordServer starts a test SSH server and registers it (already trusted).
func passwordServer(t *testing.T, e *env) (*sshtest.Server, model.Server) {
	t.Helper()
	srv := sshtest.Start(t, sshtest.Options{User: user, Password: password})
	s := e.addServer(srv, model.Auth{Type: model.AuthPassword, Password: password})
	e.trust(srv.Host, srv.Port, srv.HostKey.PublicKey())
	return srv, s
}

// backend is an HTTP server reachable from the SSH server (both on this
// machine), standing in for a web UI such as n8n.
func backend(t *testing.T, body string) (host string, port int) {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	t.Cleanup(hs.Close)
	h, p, _ := net.SplitHostPort(hs.Listener.Addr().String())
	port, _ = strconv.Atoi(p)
	return h, port
}

func service(serverID, host string, port int) model.Service {
	return model.Service{ID: model.NewID(), ServerID: serverID, Label: "web", RemoteHost: host, RemotePort: port, Protocol: model.HTTP}
}

// get fetches http://127.0.0.1:port/ over a fresh connection.
func get(port int) (string, error) {
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func mustGet(t *testing.T, port int, want string) {
	t.Helper()
	got, err := get(port)
	if err != nil {
		t.Fatalf("GET through tunnel: %v", err)
	}
	if got != want {
		t.Fatalf("GET through tunnel = %q, want %q", got, want)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func forwardState(m *Manager, id string) State {
	st, ok := m.Forward(id)
	if !ok {
		return StateStopped
	}
	return st.State
}

// --- Host keys --------------------------------------------------------------

func TestUnknownHostKeyThenConfirm(t *testing.T) {
	e := newEnv()
	srv := sshtest.Start(t, sshtest.Options{User: user, Password: password})
	s := e.addServer(srv, model.Auth{Type: model.AuthPassword, Password: password})
	m := newManager(t, e, nil)
	host, port := backend(t, "hello")
	svc := service(s.ID, host, port)

	_, err := m.StartForward(svc)
	var unknown *UnknownHostKeyError
	if !errors.As(err, &unknown) {
		t.Fatalf("got %v, want *UnknownHostKeyError", err)
	}
	if want := ssh.FingerprintSHA256(srv.HostKey.PublicKey()); unknown.Fingerprint != want {
		t.Fatalf("fingerprint %s, want %s", unknown.Fingerprint, want)
	}
	if srv.Logins() != 0 {
		t.Fatal("logged in (sent credentials) before the host key was confirmed")
	}
	if _, ok := m.Forward(svc.ID); ok {
		t.Fatal("forward left running after a failed start")
	}

	// The user confirms: store exactly the key from the error, then retry.
	e.trust(srv.Host, srv.Port, unknown.Key)
	st, err := m.StartForward(svc)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateActive {
		t.Fatalf("state %s, want active", st.State)
	}
	mustGet(t, st.LocalPort, "hello")
}

func TestChangedHostKeyIsRefused(t *testing.T) {
	e := newEnv()
	srv := sshtest.Start(t, sshtest.Options{User: user, Password: password})
	s := e.addServer(srv, model.Auth{Type: model.AuthPassword, Password: password})
	e.trust(srv.Host, srv.Port, sshtest.NewHostKey(t).PublicKey()) // a different key
	m := newManager(t, e, nil)

	_, err := m.StartForward(service(s.ID, "127.0.0.1", 1))
	var changed *HostKeyChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("got %v, want *HostKeyChangedError", err)
	}
	if srv.Logins() != 0 {
		t.Fatal("credentials were sent to a server with a changed host key")
	}
	if !strings.Contains(err.Error(), "WARNING") {
		t.Errorf("error should warn the user: %v", err)
	}
}

func TestHostKeyAlgorithms(t *testing.T) {
	if algos := hostKeyAlgorithms(nil); algos != nil {
		t.Errorf("no known keys should use library defaults, got %v", algos)
	}
	d := model.Data{}
	d.SetHostKey("h", sshtest.NewHostKey(t).PublicKey())
	if algos := hostKeyAlgorithms(d.KnownHosts); len(algos) != 1 || algos[0] != ssh.KeyAlgoED25519 {
		t.Errorf("got %v, want [%s]", algos, ssh.KeyAlgoED25519)
	}
}

// --- Authentication ---------------------------------------------------------

func TestWrongPassword(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	wrong := "Wr0ng-Password-Value"
	e.setAuth(s.ID, model.Auth{Type: model.AuthPassword, Password: wrong})
	m := newManager(t, e, nil)

	_, err := m.StartForward(service(s.ID, "127.0.0.1", 1))
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("got %v, want ErrAuthFailed", err)
	}
	if strings.Contains(err.Error(), wrong) {
		t.Fatal("error message contains the password")
	}
	_ = srv
}

func TestKeyboardInteractivePassword(t *testing.T) {
	e := newEnv()
	srv := sshtest.Start(t, sshtest.Options{User: user, Password: password, KeyboardOnly: true})
	s := e.addServer(srv, model.Auth{Type: model.AuthPassword, Password: password})
	e.trust(srv.Host, srv.Port, srv.HostKey.PublicKey())
	m := newManager(t, e, nil)
	host, port := backend(t, "ki")
	st, err := m.StartForward(service(s.ID, host, port))
	if err != nil {
		t.Fatal(err)
	}
	mustGet(t, st.LocalPort, "ki")
}

func newClientKey(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return priv, sshPub
}

func encodeKey(t *testing.T, priv ed25519.PrivateKey, passphrase string) string {
	t.Helper()
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func keyServer(t *testing.T, e *env, pub ssh.PublicKey) *sshtest.Server {
	t.Helper()
	srv := sshtest.Start(t, sshtest.Options{User: user, AuthorizedKeys: []ssh.PublicKey{pub}})
	e.trust(srv.Host, srv.Port, srv.HostKey.PublicKey())
	return srv
}

func TestVaultKey(t *testing.T) {
	e := newEnv()
	priv, pub := newClientKey(t)
	srv := keyServer(t, e, pub)
	host, port := backend(t, "vault-key")
	m := newManager(t, e, nil)

	s := e.addServer(srv, model.Auth{Type: model.AuthKeyVault, PrivateKey: encodeKey(t, priv, "")})
	st, err := m.StartForward(service(s.ID, host, port))
	if err != nil {
		t.Fatal(err)
	}
	mustGet(t, st.LocalPort, "vault-key")

	// A key the server doesn't accept.
	other, _ := newClientKey(t)
	s2 := e.addServer(srv, model.Auth{Type: model.AuthKeyVault, PrivateKey: encodeKey(t, other, "")})
	if _, err := m.StartForward(service(s2.ID, host, port)); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("got %v, want ErrAuthFailed", err)
	}
}

func TestEncryptedKeyFile(t *testing.T) {
	e := newEnv()
	priv, pub := newClientKey(t)
	srv := keyServer(t, e, pub)
	host, port := backend(t, "key-file")
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "keys"), 0o700)
	if err := os.WriteFile(filepath.Join(base, "keys", "id_ed25519"), []byte(encodeKey(t, priv, "open sesame")), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newManager(t, e, func(c *Config) { c.BaseDir = base })

	// Relative path resolves inside the portable folder (BaseDir).
	auth := model.Auth{Type: model.AuthKeyFile, KeyPath: filepath.Join("keys", "id_ed25519")}
	cases := []struct {
		passphrase string
		wantErr    error
	}{
		{"", ErrKeyPassphrase},
		{"Inc0rrect-Phrase-7c1", ErrKeyPassphrase},
		{"open sesame", nil},
	}
	for _, c := range cases {
		a := auth
		a.Passphrase = c.passphrase
		s := e.addServer(srv, a)
		st, err := m.StartForward(service(s.ID, host, port))
		if !errors.Is(err, c.wantErr) {
			t.Fatalf("passphrase %q: got %v, want %v", c.passphrase, err, c.wantErr)
		}
		if err == nil {
			mustGet(t, st.LocalPort, "key-file")
		} else if c.passphrase != "" && strings.Contains(err.Error(), c.passphrase) {
			t.Fatal("error message contains the passphrase")
		}
	}

	s := e.addServer(srv, model.Auth{Type: model.AuthKeyFile, KeyPath: "keys/missing"})
	if _, err := m.StartForward(service(s.ID, host, port)); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing key file: got %v", err)
	}
}

func TestAgentAuth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix socket agent")
	}
	e := newEnv()
	priv, pub := newClientKey(t)
	srv := keyServer(t, e, pub)
	host, port := backend(t, "agent")

	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(keyring, c)
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)

	m := newManager(t, e, nil)
	s := e.addServer(srv, model.Auth{Type: model.AuthAgent})
	st, err := m.StartForward(service(s.ID, host, port))
	if err != nil {
		t.Fatal(err)
	}
	mustGet(t, st.LocalPort, "agent")

	t.Setenv("SSH_AUTH_SOCK", "")
	s2 := e.addServer(srv, model.Auth{Type: model.AuthAgent})
	if _, err := m.StartForward(service(s2.ID, host, port)); err == nil || !strings.Contains(err.Error(), "ssh-agent") {
		t.Fatalf("missing agent: got %v", err)
	}
}

// --- Forwards ---------------------------------------------------------------

func TestForwardBasicsAndSharedConnection(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	h1, p1 := backend(t, "one")
	h2, p2 := backend(t, "two")
	m := newManager(t, e, nil)

	svc1, svc2 := service(s.ID, h1, p1), service(s.ID, h2, p2)
	st1, err := m.StartForward(svc1)
	if err != nil {
		t.Fatal(err)
	}
	st2, err := m.StartForward(svc2)
	if err != nil {
		t.Fatal(err)
	}
	mustGet(t, st1.LocalPort, "one")
	mustGet(t, st2.LocalPort, "two")

	if srv.Logins() != 1 || srv.ActiveConnections() != 1 {
		t.Fatalf("logins=%d active=%d; want one shared connection", srv.Logins(), srv.ActiveConnections())
	}
	again, err := m.StartForward(svc1)
	if err != nil || again.LocalPort != st1.LocalPort {
		t.Fatalf("starting a running forward: %+v, %v", again, err)
	}

	// Listens on loopback only.
	m.mu.Lock()
	addr := m.forwards[svc1.ID].ln.Addr().(*net.TCPAddr)
	m.mu.Unlock()
	if !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("forward listens on %s, want 127.0.0.1", addr.IP)
	}

	m.StopForward(svc1.ID)
	if _, err := get(st1.LocalPort); err == nil {
		t.Fatal("stopped forward still answers")
	}
	mustGet(t, st2.LocalPort, "two")
	if srv.ActiveConnections() != 1 {
		t.Fatal("connection closed while another forward still uses it")
	}

	m.StopForward(svc2.ID)
	waitFor(t, "SSH connection to close", func() bool { return srv.ActiveConnections() == 0 })
	if len(m.Servers()) != 0 {
		t.Fatalf("server still listed: %+v", m.Servers())
	}
	if !e.sawEvent("forward", svc1.ID, StateActive) || !e.sawEvent("forward", svc1.ID, StateStopped) {
		t.Error("missing forward events")
	}
}

func TestLargeTransfer(t *testing.T) {
	e := newEnv()
	_, s := passwordServer(t, e)
	big := strings.Repeat("0123456789abcdef", 256*1024) // 4 MiB
	host, port := backend(t, big)
	m := newManager(t, e, nil)
	st, err := m.StartForward(service(s.ID, host, port))
	if err != nil {
		t.Fatal(err)
	}
	got, err := get(st.LocalPort)
	if err != nil {
		t.Fatal(err)
	}
	if got != big {
		t.Fatalf("received %d bytes, want %d intact", len(got), len(big))
	}
}

// A service that keeps its connections open (a web UI's websocket, say)
// must not keep a stopped forward, or TunnelTab on Quit, from finishing.
func TestStopWithIdleConnectionToStubbornService(t *testing.T) {
	e := newEnv()
	_, s := passwordServer(t, e)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		// Accepts, then never writes and ignores the end-of-stream.
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	m := newManager(t, e, nil)
	addr := ln.Addr().(*net.TCPAddr)
	svc := service(s.ID, "127.0.0.1", addr.Port)
	st, err := m.StartForward(svc)
	if err != nil {
		t.Fatal(err)
	}
	browser, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(st.LocalPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	var service net.Conn
	select {
	case service = <-accepted:
		defer service.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("the connection never reached the service")
	}

	stopped := make(chan struct{})
	go func() {
		m.StopForward(svc.ID)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("StopForward hangs on an idle connection")
	}
	if _, err := get(st.LocalPort); err == nil {
		t.Fatal("stopped forward still answers")
	}
}

// A server that keeps the connection alive but never answers the request
// to reach the service must not keep a forward from stopping.
func TestStopWhileServiceNeverAnswers(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	srv.StallForwards(true)
	m := newManager(t, e, nil)
	svc := service(s.ID, "127.0.0.1", 1)
	st, err := m.StartForward(svc)
	if err != nil {
		t.Fatal(err)
	}
	browser, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(st.LocalPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	waitFor(t, "the request to reach the service", func() bool { return srv.Stalled() > 0 })

	stopped := make(chan struct{})
	go func() {
		m.StopForward(svc.ID)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("StopForward hangs while the server doesn't answer")
	}
	waitFor(t, "the connection to close", func() bool { return srv.ActiveConnections() == 0 })
}

// Stopping a service (or its server, or everything) while it is still
// starting cancels the start: no tunnel is left running afterwards.
func TestStopCancelsStartingForward(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(m *Manager, svc model.Service)
	}{
		{"StopForward", func(m *Manager, svc model.Service) { m.StopForward(svc.ID) }},
		{"StopServer", func(m *Manager, svc model.Service) { m.StopServer(svc.ServerID) }},
		{"StopAll", func(m *Manager, svc model.Service) { m.StopAll() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv()
			srv, s := passwordServer(t, e)
			resume := srv.PauseHandshakes()
			t.Cleanup(resume)
			m := newManager(t, e, nil)
			svc := service(s.ID, "127.0.0.1", 1)
			starting := func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				return m.starting[svc.ID] != nil
			}
			errc := make(chan error, 1)
			go func() {
				_, err := m.StartForward(svc)
				errc <- err
			}()
			waitFor(t, "the start to begin", starting)
			tc.stop(m, svc)
			if starting() {
				t.Fatal("the start is still reserved after stopping")
			}
			resume()
			select {
			case err := <-errc:
				if !errors.Is(err, ErrStartCancelled) {
					t.Fatalf("got %v, want ErrStartCancelled", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("StartForward never returned")
			}
			if _, ok := m.Forward(svc.ID); ok {
				t.Fatal("a stopped service's tunnel is running")
			}
			waitFor(t, "the connection to close", func() bool { return srv.ActiveConnections() == 0 })
		})
	}
}

// A start that was cancelled must not replace a newer start of the same
// service when it finishes.
func TestCancelledStartLeavesNewerStart(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	host, port := backend(t, "hello")
	resume := srv.PauseHandshakes()
	t.Cleanup(resume)
	m := newManager(t, e, nil)
	svc := service(s.ID, host, port)
	reserved := func() *startingForward {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.starting[svc.ID]
	}
	type result struct {
		st  ForwardStatus
		err error
	}
	start := func() chan result {
		ch := make(chan result, 1)
		go func() {
			st, err := m.StartForward(svc)
			ch <- result{st, err}
		}()
		return ch
	}
	first := start()
	waitFor(t, "the first start", func() bool { return reserved() != nil })
	old := reserved()
	m.StopForward(svc.ID)
	second := start()
	waitFor(t, "the second start", func() bool { p := reserved(); return p != nil && p != old })
	resume()

	if r := <-first; !errors.Is(r.err, ErrStartCancelled) {
		t.Fatalf("first start: got %v, want ErrStartCancelled", r.err)
	}
	r := <-second
	if r.err != nil {
		t.Fatalf("second start: %v", r.err)
	}
	if st, ok := m.Forward(svc.ID); !ok || st.LocalPort != r.st.LocalPort {
		t.Fatalf("running forward %+v (%v), want the second start's port %d", st, ok, r.st.LocalPort)
	}
	mustGet(t, r.st.LocalPort, "hello")
}

func TestPortInUse(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	svc := service(s.ID, "127.0.0.1", 1)
	svc.LocalPort = busy.Addr().(*net.TCPAddr).Port
	m := newManager(t, e, nil)

	if _, err := m.StartForward(svc); !errors.Is(err, ErrPortInUse) {
		t.Fatalf("got %v, want ErrPortInUse", err)
	}
	if srv.Logins() != 0 {
		t.Fatal("connected to the server although the local port was busy")
	}
}

func TestAutoPortIsStable(t *testing.T) {
	e := newEnv()
	_, s := passwordServer(t, e)
	host, port := backend(t, "auto")
	m := newManager(t, e, nil)
	svc := service(s.ID, host, port)

	st, err := m.StartForward(svc)
	if err != nil {
		t.Fatal(err)
	}
	if want := preferredPort(svc.ID); st.LocalPort != want {
		t.Logf("preferred port %d was busy; got %d", want, st.LocalPort)
	}
	first := st.LocalPort
	m.StopForward(svc.ID)
	st, err = m.StartForward(svc)
	if err != nil {
		t.Fatal(err)
	}
	if st.LocalPort != first {
		t.Fatalf("auto port changed between runs: %d then %d", first, st.LocalPort)
	}
	mustGet(t, st.LocalPort, "auto")
}

func TestConcurrentStartsShareOneLogin(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	host, port := backend(t, "x")
	m := newManager(t, e, nil)
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.StartForward(service(s.ID, host, port)); err != nil {
				failures.Add(1)
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if failures.Load() == 0 && (srv.Logins() != 1 || len(m.Forwards()) != 10) {
		t.Fatalf("logins=%d forwards=%d; want 1 and 10", srv.Logins(), len(m.Forwards()))
	}
}

// --- Reconnection, pausing, failure -----------------------------------------

func TestReconnectAfterDrop(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	host, port := backend(t, "back")
	m := newManager(t, e, nil)
	svc := service(s.ID, host, port)
	st, err := m.StartForward(svc)
	if err != nil {
		t.Fatal(err)
	}
	before := m.Servers()
	if len(before) != 1 || before[0].Since == nil || before[0].Reconnects != 0 {
		t.Fatalf("status after connecting: %+v", before)
	}

	srv.DropConnections()
	waitFor(t, "reconnect", func() bool { return srv.Logins() >= 2 && forwardState(m, svc.ID) == StateActive })
	mustGet(t, st.LocalPort, "back")
	if !e.sawEvent("server", s.ID, StateReconnecting) {
		t.Error("no reconnecting event")
	}
	// "Connected since" survives the reconnect, which is counted.
	after := m.Servers()
	if len(after) != 1 || after[0].Since == nil || !after[0].Since.Equal(*before[0].Since) || after[0].Reconnects != 1 {
		t.Fatalf("status after reconnecting: %+v (before %+v)", after, before)
	}
}

func TestReconnectRetriesWhileUnreachable(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	host, port := backend(t, "later")
	m := newManager(t, e, nil)
	svc := service(s.ID, host, port)
	st, _ := m.StartForward(svc)

	srv.SetRefusing(true)
	srv.DropConnections()
	time.Sleep(300 * time.Millisecond) // several failed attempts
	if forwardState(m, svc.ID) != StateReconnecting {
		t.Fatalf("state %s, want reconnecting", forwardState(m, svc.ID))
	}
	srv.SetRefusing(false)
	waitFor(t, "recovery", func() bool { return forwardState(m, svc.ID) == StateActive })
	mustGet(t, st.LocalPort, "later")
}

func TestPausedWhileLockedThenResume(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	host, port := backend(t, "resumed")
	m := newManager(t, e, nil)
	svc := service(s.ID, host, port)
	st, _ := m.StartForward(svc)

	e.setPaused(true) // vault locked
	srv.DropConnections()
	waitFor(t, "paused", func() bool { return forwardState(m, svc.ID) == StatePaused })
	logins := srv.Logins()
	time.Sleep(100 * time.Millisecond)
	if srv.Logins() != logins {
		t.Fatal("kept reconnecting while the vault was locked")
	}

	e.setPaused(false) // unlocked
	m.Resume()
	waitFor(t, "resume", func() bool { return forwardState(m, svc.ID) == StateActive })
	mustGet(t, st.LocalPort, "resumed")
}

func TestPermanentFailureStopsForwards(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	host, port := backend(t, "x")
	m := newManager(t, e, nil)
	svc := service(s.ID, host, port)
	st, _ := m.StartForward(svc)

	e.setAuth(s.ID, model.Auth{Type: model.AuthPassword, Password: "changed-on-server"})
	srv.DropConnections()
	waitFor(t, "failure", func() bool { _, ok := m.Forward(svc.ID); return !ok })
	if !e.sawEvent("forward", svc.ID, StateFailed) {
		t.Error("no failed event for the forward")
	}
	if _, err := get(st.LocalPort); err == nil {
		t.Fatal("failed forward still listening")
	}
}

// freezer is a TCP proxy that can silently stop passing data, like a dead
// network path (laptop sleep, NAT timeout) where nothing is closed.
type freezer struct {
	ln     net.Listener
	target string
	frozen atomic.Bool
}

func newFreezer(t *testing.T, target string) *freezer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &freezer{ln: ln, target: target}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.pipe(c)
		}
	}()
	return f
}

func (f *freezer) pipe(c net.Conn) {
	defer c.Close()
	u, err := net.Dial("tcp", f.target)
	if err != nil {
		return
	}
	defer u.Close()
	cp := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if err != nil {
				dst.Close()
				return
			}
			if f.frozen.Load() {
				continue // swallow data: the path is dead
			}
			dst.Write(buf[:n])
		}
	}
	go cp(u, c)
	cp(c, u)
}

func TestKeepAliveDetectsDeadConnection(t *testing.T) {
	e := newEnv()
	srv := sshtest.Start(t, sshtest.Options{User: user, Password: password})
	fz := newFreezer(t, srv.Addr)
	fzPort := fz.ln.Addr().(*net.TCPAddr).Port
	s := model.Server{ID: model.NewID(), Name: "t", Host: "127.0.0.1", Port: fzPort, Username: user,
		Auth: model.Auth{Type: model.AuthPassword, Password: password}}
	e.servers[s.ID] = s
	e.trust("127.0.0.1", fzPort, srv.HostKey.PublicKey())
	host, port := backend(t, "alive")
	m := newManager(t, e, func(c *Config) {
		c.KeepAliveInterval = 50 * time.Millisecond
		c.KeepAliveMax = 2
	})
	svc := service(s.ID, host, port)
	st, err := m.StartForward(svc)
	if err != nil {
		t.Fatal(err)
	}

	fz.frozen.Store(true)
	waitFor(t, "keep-alive to notice", func() bool { return e.sawEvent("server", s.ID, StateReconnecting) })
	fz.frozen.Store(false)
	waitFor(t, "recovery", func() bool { return forwardState(m, svc.ID) == StateActive })
	mustGet(t, st.LocalPort, "alive")
}

// --- Shutdown ---------------------------------------------------------------

func TestStopServerAndClose(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	host, port := backend(t, "x")
	m := newManager(t, e, nil)
	a, b := service(s.ID, host, port), service(s.ID, host, port)
	m.StartForward(a)
	m.StartForward(b)

	m.StopServer(s.ID)
	if len(m.Forwards()) != 0 {
		t.Fatal("StopServer left forwards running")
	}
	waitFor(t, "disconnect", func() bool { return srv.ActiveConnections() == 0 })

	m.StartForward(a)
	m.Close()
	if len(m.Forwards()) != 0 {
		t.Fatal("Close left forwards running")
	}
	if _, err := m.StartForward(a); !errors.Is(err, ErrClosed) {
		t.Fatalf("StartForward after Close: got %v, want ErrClosed", err)
	}
}

func TestUnreachableServer(t *testing.T) {
	e := newEnv()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens here now
	s := model.Server{ID: model.NewID(), Name: "t", Host: "127.0.0.1", Port: port, Username: user,
		Auth: model.Auth{Type: model.AuthPassword, Password: password}}
	e.servers[s.ID] = s
	m := newManager(t, e, nil)
	_, err := m.StartForward(service(s.ID, "127.0.0.1", 1))
	if err == nil || !strings.Contains(err.Error(), "can't connect") {
		t.Fatalf("got %v", err)
	}
	if permanent(err) {
		t.Fatal("network errors must be retryable")
	}
}

func TestStopAllAndTestConnection(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	host, port := backend(t, "x")
	m := newManager(t, e, nil)

	if err := m.TestConnection(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "test connection to close", func() bool { return srv.ActiveConnections() == 0 })

	m.StartForward(service(s.ID, host, port))
	m.StartForward(service(s.ID, host, port))
	m.StopAll()
	if len(m.Forwards()) != 0 {
		t.Fatal("StopAll left forwards running")
	}
	if _, err := m.StartForward(service(s.ID, host, port)); err != nil {
		t.Fatalf("manager unusable after StopAll: %v", err)
	}

	e.setAuth(s.ID, model.Auth{Type: model.AuthPassword, Password: "nope-nope"})
	s2 := e.addServer(srv, model.Auth{Type: model.AuthPassword, Password: "nope-nope"})
	if err := m.TestConnection(s2.ID); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("got %v, want ErrAuthFailed", err)
	}
}

func TestKeyFileMustBeRegular(t *testing.T) {
	dir := t.TempDir()
	_, _, err := authMethods(model.Auth{Type: model.AuthKeyFile, KeyPath: dir}, "")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("got %v", err)
	}
}

func TestErrorKindHasNoDetails(t *testing.T) {
	err := friendlyDialError("secret-host.example.com:22", errors.New("dial tcp 203.0.113.9:22: refused"))
	if k := ErrorKind(err); k != "connection_error" {
		t.Fatalf("kind %q", k)
	}
	for _, e := range []error{ErrAuthFailed, ErrKeyPassphrase, ErrPortInUse, ErrPaused, &UnknownHostKeyError{}, &HostKeyChangedError{}} {
		if k := ErrorKind(e); strings.ContainsAny(k, " .:") || k == "" {
			t.Errorf("ErrorKind(%T) = %q", e, k)
		}
	}
}

func TestPingMeasured(t *testing.T) {
	e := newEnv()
	_, s := passwordServer(t, e)
	host, port := backend(t, "ping")
	m := newManager(t, e, nil) // keep-alive every hour: only the first one, sent right away
	if _, err := m.StartForward(service(s.ID, host, port)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ping", func() bool {
		st := m.Servers()
		return len(st) == 1 && st[0].PingMs > 0
	})
}

func TestPingMs(t *testing.T) {
	for d, want := range map[time.Duration]float64{
		0: 0, time.Microsecond: 0.1, 38 * time.Millisecond: 38, 1234567 * time.Nanosecond: 1.2,
	} {
		if got := pingMs(d); got != want {
			t.Errorf("pingMs(%v) = %v, want %v", d, got, want)
		}
	}
}

func TestTrafficCounted(t *testing.T) {
	e := newEnv()
	_, s := passwordServer(t, e)
	host, port := backend(t, "counted")
	m := newManager(t, e, nil)
	svc := service(s.ID, host, port)
	st, err := m.StartForward(svc)
	if err != nil {
		t.Fatal(err)
	}
	mustGet(t, st.LocalPort, "counted")
	var tr []TrafficStatus
	waitFor(t, "traffic", func() bool {
		tr = m.Traffic()
		return len(tr) == 1 && tr[0].TodayIn > 0 && tr[0].TodayOut > 0
	})
	if tr[0].ServiceID != svc.ID || len(tr[0].LastHour) != 60 || tr[0].LastHour[59] != tr[0].TodayIn+tr[0].TodayOut {
		t.Fatalf("traffic %+v", tr[0])
	}
}

func TestTrafficMeterWindows(t *testing.T) {
	now := time.Date(2026, 10, 1, 23, 58, 30, 0, time.Local)
	tm := newTrafficMeter()
	tm.now = func() time.Time { return now }
	tm.add("svc", 100, true)
	tm.add("svc", 50, false)
	now = now.Add(time.Minute) // 23:59
	tm.add("svc", 10, true)
	got := tm.list()[0]
	if got.TodayIn != 110 || got.TodayOut != 50 || got.LastHour[58] != 150 || got.LastHour[59] != 10 {
		t.Fatalf("same day: %+v", got)
	}
	now = now.Add(2 * time.Minute) // 00:01 the next day: today starts again
	got = tm.list()[0]
	if got.TodayIn != 0 || got.TodayOut != 0 || got.LastHour[56] != 150 || got.LastHour[57] != 10 || got.LastHour[59] != 0 {
		t.Fatalf("next day: %+v", got)
	}
	now = now.Add(61 * time.Minute) // over an hour later: nothing left in the window
	for _, b := range tm.list()[0].LastHour {
		if b != 0 {
			t.Fatalf("old minutes still counted: %v", tm.list()[0].LastHour)
		}
	}
	tm.add("svc", 0, true) // nothing to count
	if len(tm.list()) != 1 {
		t.Fatal("unexpected services")
	}
}

func TestRunIfConnected(t *testing.T) {
	e := newEnv()
	srv := sshtest.Start(t, sshtest.Options{User: user, Password: password, Exec: map[string]string{
		"health": "load 0.42\n",
		"big":    strings.Repeat("x", 5000),
	}})
	s := e.addServer(srv, model.Auth{Type: model.AuthPassword, Password: password})
	e.trust(srv.Host, srv.Port, srv.HostKey.PublicKey())
	host, port := backend(t, "x")
	m := newManager(t, e, nil)

	// Never opens a connection by itself.
	if _, err := m.RunIfConnected(s.ID, "health", 1024, time.Second); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("not connected: %v", err)
	}
	if srv.Logins() != 0 {
		t.Fatal("RunIfConnected logged in")
	}

	if _, err := m.StartForward(service(s.ID, host, port)); err != nil {
		t.Fatal(err)
	}
	out, err := m.RunIfConnected(s.ID, "health", 1024, 5*time.Second)
	if err != nil || string(out) != "load 0.42\n" {
		t.Fatalf("got %q, %v", out, err)
	}
	if _, err := m.RunIfConnected(s.ID, "big", 1024, 5*time.Second); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	// A failing command (exit status 127) isn't an error; its output is empty.
	if out, err := m.RunIfConnected(s.ID, "nope", 1024, 5*time.Second); err != nil || len(out) != 0 {
		t.Fatalf("unknown command: %q, %v", out, err)
	}
	if srv.Logins() != 1 {
		t.Fatalf("%d logins, want 1 (the tunnel's)", srv.Logins())
	}
}

func TestRun(t *testing.T) {
	e := newEnv()
	srv := sshtest.Start(t, sshtest.Options{User: user, Password: password, Exec: map[string]string{"scan": "ports\n"}})
	s := e.addServer(srv, model.Auth{Type: model.AuthPassword, Password: password})
	m := newManager(t, e, nil)

	// An unknown host key stops it before anything runs, like TestConnection.
	var unknown *UnknownHostKeyError
	if _, err := m.Run(s.ID, "scan", 1024, 5*time.Second); !errors.As(err, &unknown) {
		t.Fatalf("got %v, want *UnknownHostKeyError", err)
	}
	e.trust(srv.Host, srv.Port, srv.HostKey.PublicKey())

	// Connects when needed, and lets the connection go afterwards.
	out, err := m.Run(s.ID, "scan", 1024, 5*time.Second)
	if err != nil || string(out) != "ports\n" {
		t.Fatalf("got %q, %v", out, err)
	}
	if srv.Logins() != 1 {
		t.Fatalf("%d logins, want 1", srv.Logins())
	}
	if list := m.Servers(); len(list) != 0 {
		t.Fatalf("connection kept open: %+v", list)
	}

	// Uses an existing connection without logging in again, and keeps it.
	host, port := backend(t, "x")
	if _, err := m.StartForward(service(s.ID, host, port)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Run(s.ID, "scan", 1024, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if srv.Logins() != 2 {
		t.Fatalf("%d logins, want 2", srv.Logins())
	}
	if list := m.Servers(); len(list) != 1 || list[0].State != StateConnected {
		t.Fatalf("existing connection changed: %+v", list)
	}
}

func TestHold(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	host, port := backend(t, "x")
	m := newManager(t, e, nil)

	if err := m.Hold(s.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Hold(s.ID); err != nil { // holding again does nothing
		t.Fatal(err)
	}
	if !m.IsHeld(s.ID) || srv.ActiveConnections() != 1 {
		t.Fatalf("held %v, %d connections", m.IsHeld(s.ID), srv.ActiveConnections())
	}
	if st := m.Servers(); len(st) != 1 || !st[0].Held || st[0].State != StateConnected {
		t.Fatalf("status %+v", st)
	}

	// A forward shares the held connection; unholding leaves it open.
	if _, err := m.StartForward(service(s.ID, host, port)); err != nil {
		t.Fatal(err)
	}
	m.Unhold(s.ID)
	if m.IsHeld(s.ID) || len(m.Servers()) != 1 || m.Servers()[0].Held {
		t.Fatalf("after Unhold: held %v, status %+v", m.IsHeld(s.ID), m.Servers())
	}
	m.StopAll()
	waitFor(t, "connection to close", func() bool { return srv.ActiveConnections() == 0 })

	// Unholding the only user closes the connection.
	if err := m.Hold(s.ID); err != nil {
		t.Fatal(err)
	}
	m.Unhold(s.ID)
	waitFor(t, "held connection to close", func() bool { return srv.ActiveConnections() == 0 })
	if !e.sawEvent("server", s.ID, StateStopped) {
		t.Fatal("no stopped event")
	}

	// StopServer and StopAll drop holds.
	for _, stop := range []func(){func() { m.StopServer(s.ID) }, m.StopAll} {
		if err := m.Hold(s.ID); err != nil {
			t.Fatal(err)
		}
		stop()
		if len(m.Held()) != 0 {
			t.Fatal("hold kept")
		}
		waitFor(t, "connection to close", func() bool { return srv.ActiveConnections() == 0 })
	}

	// Errors are those of TestConnection.
	s2 := e.addServer(srv, model.Auth{Type: model.AuthPassword, Password: "nope-nope"})
	if err := m.Hold(s2.ID); !errors.Is(err, ErrAuthFailed) || m.IsHeld(s2.ID) {
		t.Fatalf("got %v, held %v", err, m.IsHeld(s2.ID))
	}
}

func TestThrough(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	host, port := backend(t, "hello")
	m := newManager(t, e, nil)

	var body string
	err := m.Through(s.ID, func(dial func(string, int) (net.Conn, error)) {
		c, err := dial(host, port)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		io.WriteString(c, "GET / HTTP/1.0\r\n\r\n")
		b, _ := io.ReadAll(c)
		body = string(b)
		if srv.ActiveConnections() != 1 {
			t.Error("not connected while fn runs")
		}
	})
	if err != nil || !strings.HasSuffix(body, "hello") {
		t.Fatalf("err %v, body %q", err, body)
	}
	waitFor(t, "connection to close", func() bool { return srv.ActiveConnections() == 0 })

	s2 := e.addServer(srv, model.Auth{Type: model.AuthPassword, Password: "nope-nope"})
	called := false
	if err := m.Through(s2.ID, func(func(string, int) (net.Conn, error)) { called = true }); !errors.Is(err, ErrAuthFailed) || called {
		t.Fatalf("got %v, called %v", err, called)
	}
}

// Editing or deleting a server (StopServer) while a Connect (Hold) or a new
// terminal (OpenShell) is still connecting cancels them: nothing of the old
// server is left running once the connection comes up.
func TestStopServerCancelsConnecting(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(m *Manager, serverID string)
		open func(m *Manager, serverID string) error
	}{
		{"Hold/StopServer", (*Manager).StopServer, (*Manager).Hold},
		{"Hold/StopAll", func(m *Manager, _ string) { m.StopAll() }, (*Manager).Hold},
		{"OpenShell/StopServer", (*Manager).StopServer, func(m *Manager, id string) error {
			sh, err := m.OpenShell(id, 80, 24)
			if err == nil {
				sh.Close()
			}
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv()
			srv, s := passwordServer(t, e)
			resume := srv.PauseHandshakes()
			t.Cleanup(resume)
			m := newManager(t, e, nil)
			errc := make(chan error, 1)
			go func() { errc <- tc.open(m, s.ID) }()
			waitFor(t, "the connection to start", func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				return m.servers[s.ID] != nil
			})
			tc.stop(m, s.ID)
			resume()
			select {
			case err := <-errc:
				if !errors.Is(err, ErrConnectCancelled) {
					t.Fatalf("got %v, want ErrConnectCancelled", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("never returned")
			}
			if len(m.Held()) != 0 || m.ShellCount() != 0 {
				t.Fatalf("held %v, %d shells after the server was stopped", m.Held(), m.ShellCount())
			}
			waitFor(t, "the connection to close", func() bool { return srv.ActiveConnections() == 0 })
		})
	}
}

// Health checks and Find services must end within their timeout even if the
// server never answers the request to open a session.
func TestRunTimeoutCoversOpeningSession(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	srv.StallSessions(true)
	m := newManager(t, e, nil)
	start := time.Now()
	_, err := m.Run(s.ID, "scan", 1024, 200*time.Millisecond)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("got %v after %v, want a timeout after 200ms", err, time.Since(start))
	}
	if srv.Stalled() != 1 {
		t.Fatalf("%d session requests left hanging, want 1", srv.Stalled())
	}
}

// openShellAsync opens a terminal in the background; the result arrives on
// the channel (the shell is closed at once if it opened).
func openShellAsync(m *Manager, serverID string) chan error {
	errc := make(chan error, 1)
	go func() {
		sh, err := m.OpenShell(serverID, 80, 24)
		if err == nil {
			sh.Close()
		}
		errc <- err
	}()
	return errc
}

func waitErr(t *testing.T, errc chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(within):
		t.Fatalf("still waiting after %v", within)
		return nil
	}
}

// A server that keeps the connection alive but never answers the request
// for a session must not leave opening a terminal hanging: it gives up
// after the connect timeout, or at once when the server is stopped or
// TunnelTab shuts down, and lets go of the connection.
func TestOpenShellNeverAnswered(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		stop    func(m *Manager, serverID string)
		want    error
	}{
		{"timeout", 300 * time.Millisecond, nil, nil},
		{"StopServer", time.Hour, (*Manager).StopServer, ErrConnectCancelled},
		{"Close", time.Hour, func(m *Manager, _ string) { m.Close() }, ErrClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv()
			srv, s := passwordServer(t, e)
			srv.StallSessions(true)
			m := newManager(t, e, func(c *Config) { c.DialTimeout = tc.timeout })
			errc := openShellAsync(m, s.ID)
			waitFor(t, "the session request", func() bool { return srv.Stalled() > 0 })
			if tc.stop != nil {
				tc.stop(m, s.ID)
			}
			err := waitErr(t, errc, 5*time.Second)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if m.ShellCount() != 0 {
				t.Fatal("a terminal was registered")
			}
			waitFor(t, "the connection to close", func() bool { return srv.ActiveConnections() == 0 })
		})
	}
}

// Shutting down while a terminal is still connecting: it must not be
// registered afterwards, and its connection must not outlive Close.
func TestCloseCancelsConnectingShell(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	resume := srv.PauseHandshakes()
	t.Cleanup(resume)
	m := newManager(t, e, nil)
	errc := openShellAsync(m, s.ID)
	waitFor(t, "the connection to start", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.servers[s.ID] != nil
	})
	m.Close()
	resume()
	if err := waitErr(t, errc, 10*time.Second); err == nil {
		t.Fatal("a terminal opened after Close")
	}
	if m.ShellCount() != 0 {
		t.Fatal("a terminal was registered after Close")
	}
	waitFor(t, "the connection to close", func() bool { return srv.ActiveConnections() == 0 })
}

// Terminals opened at the same time can't get past MaxShells: those still
// connecting count too.
func TestMaxShellsWithConcurrentOpens(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	resume := srv.PauseHandshakes()
	t.Cleanup(resume)
	m := newManager(t, e, nil)
	const n = MaxShells + 8
	results := make(chan error, n)
	var opened []*Shell
	var mu sync.Mutex
	for range n {
		go func() {
			sh, err := m.OpenShell(s.ID, 80, 24)
			if err == nil {
				mu.Lock()
				opened = append(opened, sh)
				mu.Unlock()
			}
			results <- err
		}()
	}
	// All of them have asked before any could connect.
	waitFor(t, "every request", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.opening == MaxShells
	})
	time.Sleep(50 * time.Millisecond) // the other 8 are turned away meanwhile
	resume()
	refused := 0
	for range n {
		if err := waitErr(t, results, 10*time.Second); err != nil {
			if !strings.Contains(err.Error(), "too many open terminals") {
				t.Fatalf("unexpected error: %v", err)
			}
			refused++
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(opened) != MaxShells || refused != n-MaxShells || m.ShellCount() != MaxShells {
		t.Fatalf("opened %d, refused %d, %d registered; want %d opened", len(opened), refused, m.ShellCount(), MaxShells)
	}
	for _, sh := range opened {
		sh.Close()
	}
	m.mu.Lock()
	opening := m.opening
	m.mu.Unlock()
	if opening != 0 || m.ShellCount() != 0 {
		t.Fatalf("after closing: %d reserved, %d registered", opening, m.ShellCount())
	}
}

// Close also ends connections still in use by work that isn't a forward,
// terminal or hold (a Find services scan waiting for the server, say).
func TestCloseEndsConnectionsInUse(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	srv.StallSessions(true)
	m := newManager(t, e, nil)
	errc := make(chan error, 1)
	go func() {
		_, err := m.Run(s.ID, "scan", 1024, time.Hour)
		errc <- err
	}()
	waitFor(t, "the session request", func() bool { return srv.Stalled() > 0 })
	m.Close()
	if err := waitErr(t, errc, 5*time.Second); err == nil {
		t.Fatal("the scan succeeded after Close")
	}
	waitFor(t, "the connection to close", func() bool { return srv.ActiveConnections() == 0 })
}

// A connection StopServer retired while other work (a check) still used it
// must stay quiet when that work ends: its server ID now belongs to the
// replacement, which must not be reported stopped.
func TestRetiredConnectionStaysQuiet(t *testing.T) {
	e := newEnv()
	_, s := passwordServer(t, e)
	m := newManager(t, e, nil)
	host, port := backend(t, "hello")

	inUse, finish := make(chan struct{}), make(chan struct{})
	throughDone := make(chan error, 1)
	go func() {
		throughDone <- m.Through(s.ID, func(func(string, int) (net.Conn, error)) {
			close(inUse)
			<-finish
		})
	}()
	select {
	case <-inUse:
	case <-time.After(10 * time.Second):
		t.Fatal("Through never got the connection")
	}
	m.StopServer(s.ID) // e.g. the server was edited: the connection is retired

	// A tunnel started now gets a connection of its own, which connects
	// before the old work ends.
	if _, err := m.StartForward(service(s.ID, host, port)); err != nil {
		t.Fatal(err)
	}
	connected := func() bool {
		for _, st := range m.Servers() {
			if st.ID == s.ID && st.State == StateConnected {
				return true
			}
		}
		return false
	}
	waitFor(t, "the replacement to connect", connected)
	e.mu.Lock()
	mark := len(e.events)
	e.mu.Unlock()

	close(finish) // the old work ends and releases the retired connection
	select {
	case <-throughDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Through never returned")
	}
	e.mu.Lock()
	after := append([]Event(nil), e.events[mark:]...)
	e.mu.Unlock()
	for _, ev := range after {
		if ev.Kind == "server" && ev.ID == s.ID && ev.State != StateConnected {
			t.Fatalf("the retired connection reported on the server: %+v", ev)
		}
	}
	if !connected() {
		t.Fatal("the replacement isn't listed as connected any more")
	}
}

// A retired connection whose transport drops must not reconnect (with
// settings that may have changed), and its failure must not stop the
// tunnels or hold of the connection that replaced it.
func TestRetiredConnectionDoesNotReconnect(t *testing.T) {
	e := newEnv()
	_, s := passwordServer(t, e)
	m := newManager(t, e, nil)
	host, port := backend(t, "hello")

	inUse, finish := make(chan struct{}), make(chan struct{})
	throughDone := make(chan error, 1)
	go func() {
		throughDone <- m.Through(s.ID, func(func(string, int) (net.Conn, error)) {
			close(inUse)
			<-finish
		})
	}()
	select {
	case <-inUse:
	case <-time.After(10 * time.Second):
		t.Fatal("Through never got the connection")
	}
	m.mu.Lock()
	old := m.servers[s.ID]
	m.mu.Unlock()
	m.StopServer(s.ID) // retires old: the check still uses it

	// The replacement: a tunnel and a hold on a new connection.
	svc := service(s.ID, host, port)
	if _, err := m.StartForward(svc); err != nil {
		t.Fatal(err)
	}
	if err := m.Hold(s.ID); err != nil {
		t.Fatal(err)
	}

	// The old transport drops, and logging in would now fail for good: an
	// old connection that reconnected would give up and fail the server.
	e.setAuth(s.ID, model.Auth{Type: model.AuthPassword, Password: "wrong-password"})
	old.currentClient().Close()
	deadline := time.Now().Add(500 * time.Millisecond) // reconnects start after 10 ms
	for time.Now().Before(deadline) {
		if st, ok := m.Forward(svc.ID); !ok || st.State != StateActive {
			t.Fatalf("the replacement's tunnel was stopped: %+v", st)
		}
		if !m.IsHeld(s.ID) {
			t.Fatal("the replacement's hold was dropped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	old.mu.Lock()
	state := old.state
	old.mu.Unlock()
	if state == StateReconnecting || state == StateFailed {
		t.Fatalf("the retired connection tried to reconnect (state %s)", state)
	}
	close(finish)
	select {
	case <-throughDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Through never returned")
	}
}

// failServer stops the forwards it found on the failed connection, not
// whatever runs under their service IDs by the time it gets to them: a
// service stopped and started again meanwhile keeps its new tunnel.
func TestFailServerSparesReplacedForward(t *testing.T) {
	e := newEnv()
	_, s := passwordServer(t, e)
	m := newManager(t, e, nil)
	host, port := backend(t, "hello")
	svc := service(s.ID, host, port)
	if _, err := m.StartForward(svc); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	old := m.servers[s.ID]
	m.mu.Unlock()

	found, cont := make(chan struct{}), make(chan struct{})
	testHookFailing = func() {
		close(found)
		<-cont
	}
	t.Cleanup(func() { testHookFailing = nil })
	e.mu.Lock()
	good := e.servers[s.ID].Auth
	e.mu.Unlock()
	e.setAuth(s.ID, model.Auth{Type: model.AuthPassword, Password: "wrong-password"})
	old.currentClient().Close() // reconnecting now fails for good
	select {
	case <-found:
	case <-time.After(10 * time.Second):
		t.Fatal("the failed connection was never cleaned up")
	}

	// Meanwhile the service is stopped and started again (password fixed).
	m.StopForward(svc.ID)
	e.setAuth(s.ID, good)
	if _, err := m.StartForward(svc); err != nil {
		t.Fatal(err)
	}
	close(cont)
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if st, ok := m.Forward(svc.ID); !ok || st.State != StateActive {
			t.Fatalf("the cleanup of the failed connection stopped the new tunnel: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A stopped forward's final event can't land after the events of the
// forward that replaced it (the dashboard would drop the running tunnel).
func TestStoppedForwardEventComesFirst(t *testing.T) {
	e := newEnv()
	_, s := passwordServer(t, e)
	m := newManager(t, e, nil)
	host, port := backend(t, "hello")
	svc := service(s.ID, host, port)
	if _, err := m.StartForward(svc); err != nil {
		t.Fatal(err)
	}

	removed, cont := make(chan struct{}), make(chan struct{})
	testHookForwardRemoved = func(id string) {
		if id == svc.ID {
			close(removed)
			<-cont
		}
	}
	t.Cleanup(func() { testHookForwardRemoved = nil })
	e.mu.Lock()
	mark := len(e.events)
	e.mu.Unlock()
	stopped := make(chan struct{})
	go func() {
		m.StopForward(svc.ID)
		close(stopped)
	}()
	select {
	case <-removed: // the old forward is gone, its final event not sent yet
	case <-time.After(10 * time.Second):
		t.Fatal("StopForward never removed the forward")
	}

	// The replacement starts while the old forward's stop isn't finished.
	started := make(chan error, 1)
	go func() {
		_, err := m.StartForward(svc)
		started <- err
	}()
	waitFor(t, "the replacement to be running", func() bool {
		_, ok := m.Forward(svc.ID)
		return ok
	})
	close(cont)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("StopForward never returned")
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StartForward never returned")
	}

	e.mu.Lock()
	var states []State
	for _, ev := range e.events[mark:] {
		if ev.Kind == "forward" && ev.ID == svc.ID {
			states = append(states, ev.State)
		}
	}
	e.mu.Unlock()
	if len(states) == 0 || states[len(states)-1] != StateActive {
		t.Fatalf("forward events %v: the last must be the replacement's %s", states, StateActive)
	}
	if st, ok := m.Forward(svc.ID); !ok || st.State != StateActive {
		t.Fatalf("the replacement isn't running: %+v", st)
	}
}
