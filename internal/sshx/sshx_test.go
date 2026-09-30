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

	srv.DropConnections()
	waitFor(t, "reconnect", func() bool { return srv.Logins() >= 2 && forwardState(m, svc.ID) == StateActive })
	mustGet(t, st.LocalPort, "back")
	if !e.sawEvent("server", s.ID, StateReconnecting) {
		t.Error("no reconnecting event")
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
