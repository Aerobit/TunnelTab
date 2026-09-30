package sshx

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/Aerobit/TunnelTab/internal/model"
)

// Auto-assigned local ports: each service first tries a stable port derived
// from its ID, so the address (and browser cookies/bookmarks) stay the same
// between runs; if that's taken, the OS picks any free port.
const (
	autoPortBase  = 20000
	autoPortRange = 10000
)

// ForwardStatus describes one port forward.
type ForwardStatus struct {
	ServiceID   string `json:"serviceId"`
	ServerID    string `json:"serverId"`
	LocalPort   int    `json:"localPort"`
	State       State  `json:"state"`
	Error       string `json:"error,omitempty"`
	Connections int    `json:"connections"` // open browser connections
}

type forward struct {
	m    *Manager
	svc  model.Service
	sc   *serverConn
	ln   net.Listener
	port int

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	done  bool
	wg    sync.WaitGroup
}

// StartForward starts forwarding 127.0.0.1:<local port> to the service's
// remote host and port through its server's SSH connection, connecting to
// the server first if needed. Starting a running forward returns its status.
//
// Errors include *UnknownHostKeyError (ask the user to confirm, store the
// key, retry), *HostKeyChangedError, ErrAuthFailed, ErrKeyPassphrase and
// ErrPortInUse.
func (m *Manager) StartForward(svc model.Service) (ForwardStatus, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ForwardStatus{}, ErrClosed
	}
	if f, ok := m.forwards[svc.ID]; ok {
		m.mu.Unlock()
		if f == nil {
			return ForwardStatus{}, errors.New("this service is already starting")
		}
		return f.status(), nil
	}
	m.forwards[svc.ID] = nil // reserve while starting
	m.mu.Unlock()

	unreserve := func() {
		m.mu.Lock()
		if m.forwards[svc.ID] == nil {
			delete(m.forwards, svc.ID)
		}
		m.mu.Unlock()
	}

	// Bind the port first so a busy port fails fast, before any SSH work.
	ln, err := listenLocal(svc)
	if err != nil {
		unreserve()
		return ForwardStatus{}, err
	}
	sc, err := m.acquire(svc.ServerID)
	if err != nil {
		ln.Close()
		unreserve()
		return ForwardStatus{}, err
	}

	f := &forward{m: m, svc: svc, sc: sc, ln: ln, port: ln.Addr().(*net.TCPAddr).Port, conns: map[net.Conn]struct{}{}}
	m.mu.Lock()
	closed := m.closed
	if !closed {
		m.forwards[svc.ID] = f
	}
	m.mu.Unlock()
	if closed {
		ln.Close()
		m.release(sc)
		return ForwardStatus{}, ErrClosed
	}
	f.wg.Add(1)
	go f.serve()
	m.log.Info("forward started", "service", svc.ID, "localPort", f.port)
	st := f.status()
	m.emit(Event{Kind: "forward", ID: svc.ID, ServerID: svc.ServerID, State: st.State, LocalPort: f.port})
	return st, nil
}

// StopForward stops a forward and closes its browser connections. The
// server connection closes too if nothing else uses it.
func (m *Manager) StopForward(serviceID string) {
	m.stopForward(serviceID, StateStopped, nil)
}

func (m *Manager) stopForward(serviceID string, final State, cause error) {
	m.mu.Lock()
	f := m.forwards[serviceID]
	if f == nil {
		m.mu.Unlock()
		return
	}
	delete(m.forwards, serviceID)
	m.mu.Unlock()

	f.close()
	m.release(f.sc)
	m.log.Info("forward stopped", "service", serviceID)
	m.emit(Event{Kind: "forward", ID: serviceID, ServerID: f.svc.ServerID, State: final, Error: errString(cause)})
}

// Forwards returns the status of every running forward.
func (m *Manager) Forwards() []ForwardStatus {
	var out []ForwardStatus
	for _, f := range m.allForwards() {
		out = append(out, f.status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServiceID < out[j].ServiceID })
	return out
}

// Forward returns the status of one forward, if it is running.
func (m *Manager) Forward(serviceID string) (ForwardStatus, bool) {
	m.mu.Lock()
	f := m.forwards[serviceID]
	m.mu.Unlock()
	if f == nil {
		return ForwardStatus{}, false
	}
	return f.status(), true
}

func (m *Manager) allForwards() []*forward {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*forward, 0, len(m.forwards))
	for _, f := range m.forwards {
		if f != nil {
			out = append(out, f)
		}
	}
	return out
}

func (m *Manager) forwardsFor(serverID string) []*forward {
	var out []*forward
	for _, f := range m.allForwards() {
		if f.svc.ServerID == serverID {
			out = append(out, f)
		}
	}
	return out
}

// emitForwards reports the state of a server's forwards after the server's
// connection state changed.
func (m *Manager) emitForwards(serverID string) {
	for _, f := range m.forwardsFor(serverID) {
		st := f.status()
		m.emit(Event{Kind: "forward", ID: st.ServiceID, ServerID: serverID, State: st.State, Error: st.Error, LocalPort: st.LocalPort})
	}
}

func (f *forward) status() ForwardStatus {
	st, err := f.sc.status()
	if st == StateConnected {
		st = StateActive
	}
	f.mu.Lock()
	n := len(f.conns)
	f.mu.Unlock()
	return ForwardStatus{
		ServiceID: f.svc.ID, ServerID: f.svc.ServerID, LocalPort: f.port,
		State: st, Error: errString(err), Connections: n,
	}
}

func (f *forward) serve() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return // listener closed
		}
		if !f.track(c) {
			c.Close()
			return
		}
		f.wg.Add(1)
		go f.handle(c)
	}
}

// handle connects one browser connection to the remote service.
func (f *forward) handle(local net.Conn) {
	defer f.wg.Done()
	defer f.untrack(local)
	defer local.Close()

	client := f.sc.currentClient()
	if client == nil {
		return // reconnecting; the browser will retry
	}
	target := net.JoinHostPort(f.svc.RemoteHost, strconv.Itoa(f.svc.RemotePort))
	remote, err := client.Dial("tcp", target)
	if err != nil {
		f.m.log.Info("forward: remote connect failed", "service", f.svc.ID)
		return
	}
	defer remote.Close()

	// Copy both ways. When one direction ends, pass the end-of-stream on
	// (half-close) and keep the other direction open until it finishes too,
	// so responses are never cut short.
	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, local); closeWrite(remote); done <- struct{}{} }()
	go func() { io.Copy(local, remote); closeWrite(local); done <- struct{}{} }()
	<-done
	<-done
}

// closeWrite half-closes c if it supports it (TCP and SSH channels do),
// otherwise closes it.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
		return
	}
	c.Close()
}

func (f *forward) track(c net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return false
	}
	f.conns[c] = struct{}{}
	return true
}

func (f *forward) untrack(c net.Conn) {
	f.mu.Lock()
	delete(f.conns, c)
	f.mu.Unlock()
}

// close stops accepting and closes all open connections, then waits for the
// handlers to finish.
func (f *forward) close() {
	f.mu.Lock()
	f.done = true
	f.ln.Close()
	for c := range f.conns {
		c.Close()
	}
	f.mu.Unlock()
	f.wg.Wait()
}

// listenLocal binds the service's local port on 127.0.0.1 only.
func listenLocal(svc model.Service) (net.Listener, error) {
	if svc.LocalPort != 0 {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(svc.LocalPort)))
		if err != nil {
			if isAddrInUse(err) {
				return nil, fmt.Errorf("%w: port %d", ErrPortInUse, svc.LocalPort)
			}
			return nil, fmt.Errorf("can't listen on 127.0.0.1:%d: %w", svc.LocalPort, err)
		}
		return ln, nil
	}
	if ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(preferredPort(svc.ID)))); err == nil {
		return ln, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("can't listen on 127.0.0.1: %w", err)
	}
	return ln, nil
}

// preferredPort maps a service ID to a stable port in [20000, 30000).
func preferredPort(serviceID string) int {
	h := fnv.New32a()
	h.Write([]byte(serviceID))
	return autoPortBase + int(h.Sum32()%autoPortRange)
}

func isAddrInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") || // Linux, macOS
		strings.Contains(msg, "only one usage of each socket address") // Windows (WSAEADDRINUSE)
}
