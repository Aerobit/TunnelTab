package sshx

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Aerobit/TunnelTab/internal/model"
)

// Target is everything needed to connect to one server: the server (with its
// secrets) and the confirmed host keys for its address. The Manager asks for
// a Target on every (re)connect and does not keep it afterwards, so secrets
// are not held once the connection is up.
type Target struct {
	Server     model.Server
	KnownHosts []model.KnownHost
}

// TargetFunc looks up a server by ID, normally from the vault. While the
// vault is locked it should return ErrPaused.
type TargetFunc func(serverID string) (Target, error)

// Config configures a Manager. Zero durations use the defaults shown.
type Config struct {
	Targets TargetFunc   // required
	BaseDir string       // relative key paths resolve against this (the portable folder)
	Logger  *slog.Logger // optional
	OnEvent func(Event)  // optional; called without locks held, must not block

	DialTimeout       time.Duration // TCP connect + SSH handshake (15 s)
	KeepAliveInterval time.Duration // keep-alive ping interval (30 s)
	KeepAliveMax      int           // missed pings before reconnecting (3)
	ReconnectMin      time.Duration // first reconnect delay (1 s)
	ReconnectMax      time.Duration // maximum reconnect delay (30 s)
}

// State is the state of a server connection or a forward.
type State string

const (
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"    // server connection is up
	StateActive       State = "active"       // forward is listening and its server is connected
	StateReconnecting State = "reconnecting" // connection lost, retrying with back-off
	StatePaused       State = "paused"       // waiting for the vault to be unlocked
	StateFailed       State = "failed"       // gave up; see Error
	StateStopped      State = "stopped"
)

// Event reports a state change to the dashboard.
type Event struct {
	Kind      string `json:"kind"` // "server" or "forward"
	ID        string `json:"id"`   // server ID or service ID
	ServerID  string `json:"serverId"`
	State     State  `json:"state"`
	Error     string `json:"error,omitempty"`
	LocalPort int    `json:"localPort,omitempty"`
}

// ServerStatus describes one server connection.
type ServerStatus struct {
	ID    string `json:"id"`
	State State  `json:"state"`
	Error string `json:"error,omitempty"`
}

// Manager owns all SSH connections and port forwards. One SSH connection per
// server is shared by all of that server's forwards (and, later, terminals)
// and closed when the last user stops. It is safe for concurrent use.
type Manager struct {
	cfg Config
	log *slog.Logger

	mu       sync.Mutex
	servers  map[string]*serverConn
	forwards map[string]*forward // by service ID; nil value = starting
	closed   bool
}

// NewManager returns a Manager with defaults applied.
func NewManager(cfg Config) *Manager {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&cfg.DialTimeout, 15*time.Second)
	def(&cfg.KeepAliveInterval, 30*time.Second)
	def(&cfg.ReconnectMin, time.Second)
	def(&cfg.ReconnectMax, 30*time.Second)
	if cfg.KeepAliveMax <= 0 {
		cfg.KeepAliveMax = 3
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Manager{cfg: cfg, log: log, servers: map[string]*serverConn{}, forwards: map[string]*forward{}}
}

func (m *Manager) emit(e Event) {
	if m.cfg.OnEvent != nil {
		m.cfg.OnEvent(e)
	}
}

// Servers returns the state of every open server connection.
func (m *Manager) Servers() []ServerStatus {
	m.mu.Lock()
	list := make([]*serverConn, 0, len(m.servers))
	for _, sc := range m.servers {
		list = append(list, sc)
	}
	m.mu.Unlock()
	out := make([]ServerStatus, 0, len(list))
	for _, sc := range list {
		st, err := sc.status()
		out = append(out, ServerStatus{ID: sc.id, State: st, Error: errString(err)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Resume wakes connections that are paused waiting for the vault. Call it
// after the vault is unlocked.
func (m *Manager) Resume() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sc := range m.servers {
		select {
		case sc.resume <- struct{}{}:
		default:
		}
	}
}

// StopServer stops every forward using the server and closes its connection.
// Call it when a server is edited or deleted.
func (m *Manager) StopServer(serverID string) {
	for _, f := range m.forwardsFor(serverID) {
		m.StopForward(f.svc.ID)
	}
}

// StopAll stops every forward (and so every connection). The Manager stays
// usable. Called when the vault locks with "close tunnels on lock" enabled.
func (m *Manager) StopAll() {
	for _, f := range m.allForwards() {
		m.StopForward(f.svc.ID)
	}
}

// TestConnection connects to a server (or reuses its open connection) to
// check the address, host key and login, then releases it. It returns the
// same errors as StartForward, so it can drive the host-key confirmation for
// servers that have no services yet.
func (m *Manager) TestConnection(serverID string) error {
	sc, err := m.acquire(serverID)
	if err != nil {
		return err
	}
	m.release(sc)
	return nil
}

// Close stops all forwards and connections. The Manager can't be used again.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	ids := make([]string, 0, len(m.forwards))
	for id, f := range m.forwards {
		if f != nil {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.StopForward(id)
	}
}

// --- Server connections -----------------------------------------------------

type serverConn struct {
	m  *Manager
	id string

	ready   chan struct{} // closed when the first connection attempt finishes
	initErr error         // result of that attempt
	stop    chan struct{} // closed on shutdown
	resume  chan struct{} // signalled by Manager.Resume

	mu     sync.Mutex
	client *ssh.Client
	closer io.Closer // ssh-agent connection, if any
	state  State
	err    error
	users  int
	done   bool
}

// acquire returns a connected (or reconnecting) connection to the server,
// creating it if needed. Each successful acquire must be paired with release.
func (m *Manager) acquire(serverID string) (*serverConn, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if sc, ok := m.servers[serverID]; ok {
		sc.mu.Lock()
		sc.users++
		sc.mu.Unlock()
		m.mu.Unlock()
		<-sc.ready
		if sc.initErr != nil {
			m.release(sc)
			return nil, sc.initErr
		}
		return sc, nil
	}
	sc := &serverConn{
		m: m, id: serverID, users: 1, state: StateConnecting,
		ready: make(chan struct{}), stop: make(chan struct{}), resume: make(chan struct{}, 1),
	}
	m.servers[serverID] = sc
	m.mu.Unlock()
	m.emit(Event{Kind: "server", ID: serverID, ServerID: serverID, State: StateConnecting})

	err := sc.dial()
	sc.initErr = err
	close(sc.ready)
	if err != nil {
		m.log.Info("connect failed", "server", serverID, "error", err)
		m.mu.Lock()
		if m.servers[serverID] == sc {
			delete(m.servers, serverID)
		}
		m.mu.Unlock()
		sc.shutdown()
		m.emit(Event{Kind: "server", ID: serverID, ServerID: serverID, State: StateFailed, Error: err.Error()})
		return nil, err
	}
	go sc.supervise()
	return sc, nil
}

// release drops one user; the last one closes the connection.
//
// The count is changed and the map entry removed under m.mu (lock order:
// m.mu, then sc.mu — the same as acquire), so a concurrent acquire can never
// pick up a connection that is about to be shut down.
func (m *Manager) release(sc *serverConn) {
	m.mu.Lock()
	sc.mu.Lock()
	sc.users--
	last := sc.users <= 0
	sc.mu.Unlock()
	if last && m.servers[sc.id] == sc {
		delete(m.servers, sc.id)
	}
	m.mu.Unlock()
	if !last {
		return
	}
	if sc.shutdown() {
		m.emit(Event{Kind: "server", ID: sc.id, ServerID: sc.id, State: StateStopped})
	}
}

// shutdown closes the connection for good. It reports whether this call did it.
func (sc *serverConn) shutdown() bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.done {
		return false
	}
	sc.done = true
	sc.state = StateStopped
	close(sc.stop)
	if sc.client != nil {
		sc.client.Close()
		sc.client = nil
	}
	if sc.closer != nil {
		sc.closer.Close()
		sc.closer = nil
	}
	return true
}

func (sc *serverConn) currentClient() *ssh.Client {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.client
}

func (sc *serverConn) status() (State, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.state, sc.err
}

func (sc *serverConn) setState(st State, err error) {
	sc.mu.Lock()
	if sc.done {
		sc.mu.Unlock()
		return
	}
	sc.state, sc.err = st, err
	sc.mu.Unlock()
	sc.m.emit(Event{Kind: "server", ID: sc.id, ServerID: sc.id, State: st, Error: errString(err)})
	sc.m.emitForwards(sc.id)
}

// dial fetches the target and opens a new SSH connection.
func (sc *serverConn) dial() error {
	m := sc.m
	t, err := m.cfg.Targets(sc.id)
	if err != nil {
		return err
	}
	s := t.Server
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	hkAddr := model.HostKeyAddress(s.Host, s.Port)

	auths, closer, err := authMethods(s.Auth, m.cfg.BaseDir)
	if err != nil {
		return err
	}
	cfg := &ssh.ClientConfig{
		User:              s.Username,
		Auth:              auths,
		HostKeyCallback:   hostKeyCallback(hkAddr, t.KnownHosts),
		HostKeyAlgorithms: hostKeyAlgorithms(t.KnownHosts),
		ClientVersion:     "SSH-2.0-TunnelTab",
		Timeout:           m.cfg.DialTimeout,
	}
	client, err := dialSSH(addr, cfg, m.cfg.DialTimeout)
	if err != nil {
		if closer != nil {
			closer.Close()
		}
		return friendlyDialError(addr, err)
	}

	sc.mu.Lock()
	if sc.done {
		sc.mu.Unlock()
		client.Close()
		if closer != nil {
			closer.Close()
		}
		return ErrClosed
	}
	if sc.closer != nil {
		sc.closer.Close()
	}
	sc.client, sc.closer, sc.state, sc.err = client, closer, StateConnected, nil
	sc.mu.Unlock()
	m.log.Info("connected", "server", sc.id, "address", addr)
	m.emit(Event{Kind: "server", ID: sc.id, ServerID: sc.id, State: StateConnected})
	m.emitForwards(sc.id)
	return nil
}

// dialSSH connects with a deadline covering both TCP connect and handshake
// (ssh.Dial's Timeout only covers the TCP connect).
func dialSSH(addr string, cfg *ssh.ClientConfig, timeout time.Duration) (*ssh.Client, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(timeout))
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// supervise watches the connection, sends keep-alives, and reconnects with
// back-off when it drops, until shutdown.
func (sc *serverConn) supervise() {
	m := sc.m
	for {
		client := sc.currentClient()
		if client == nil {
			return
		}
		if !sc.watch(client) {
			return // shut down
		}

		// Connection lost: forget the dead client so forwards fail fast
		// instead of using it, then reconnect.
		m.log.Info("connection lost", "server", sc.id)
		sc.mu.Lock()
		if sc.client == client {
			sc.client = nil
		}
		sc.mu.Unlock()
		sc.setState(StateReconnecting, nil)
		delay := m.cfg.ReconnectMin
		for {
			select {
			case <-sc.stop:
				return
			case <-sc.resume:
			case <-time.After(delay):
			}
			err := sc.dial()
			if err == nil {
				break
			}
			switch {
			case errors.Is(err, ErrClosed):
				return
			case errors.Is(err, ErrPaused):
				sc.setState(StatePaused, nil)
				select {
				case <-sc.stop:
					return
				case <-sc.resume:
				}
				delay = 0
				continue
			case permanent(err):
				m.log.Info("giving up on server", "server", sc.id, "error", err)
				sc.setState(StateFailed, err)
				m.failServer(sc.id, err)
				return
			}
			sc.setState(StateReconnecting, err)
			delay = min(max(delay*2, m.cfg.ReconnectMin), m.cfg.ReconnectMax)
		}
	}
}

// watch blocks until the connection drops (true) or is shut down (false),
// pinging the server and closing the connection if pings go unanswered.
func (sc *serverConn) watch(client *ssh.Client) bool {
	m := sc.m
	closed := make(chan struct{})
	go func() {
		client.Wait()
		close(closed)
	}()
	ticker := time.NewTicker(m.cfg.KeepAliveInterval)
	defer ticker.Stop()
	missed := 0
	for {
		select {
		case <-sc.stop:
			return false
		case <-closed:
			return true
		case <-ticker.C:
			reply := make(chan error, 1)
			go func() {
				_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
				reply <- err
			}()
			select {
			case err := <-reply:
				if err != nil {
					missed++
				} else {
					missed = 0
				}
			case <-time.After(m.cfg.KeepAliveInterval):
				missed++
			case <-sc.stop:
				return false
			}
			if missed >= m.cfg.KeepAliveMax {
				m.log.Info("keep-alive timeout", "server", sc.id)
				client.Close()
			}
		}
	}
}

// failServer stops all forwards of a server that can't be reconnected.
func (m *Manager) failServer(serverID string, err error) {
	for _, f := range m.forwardsFor(serverID) {
		m.stopForward(f.svc.ID, StateFailed, err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
