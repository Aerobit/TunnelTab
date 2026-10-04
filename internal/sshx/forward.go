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

	"golang.org/x/crypto/ssh"

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
	stop  chan struct{} // closed by close
	wg    sync.WaitGroup
}

// startingForward reserves a service ID while its forward starts (binding
// the port, connecting). Stopping the service, its server or everything
// takes the reservation away; the start then undoes what it set up rather
// than publishing a forward nobody wants any more.
type startingForward struct {
	serverID string
}

// cancelStarting takes away the reservations of the starts that match.
func (m *Manager) cancelStarting(match func(*startingForward) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, p := range m.starting {
		if match(p) {
			delete(m.starting, id)
		}
	}
}

// StartForward starts forwarding 127.0.0.1:<local port> to the service's
// remote host and port through its server's SSH connection, connecting to
// the server first if needed. Starting a running forward returns its status.
//
// Errors include *UnknownHostKeyError (ask the user to confirm, store the
// key, retry), *HostKeyChangedError, ErrAuthFailed, ErrKeyPassphrase and
// ErrPortInUse; ErrStartCancelled if the service was stopped meanwhile.
func (m *Manager) StartForward(svc model.Service) (ForwardStatus, error) {
	return m.StartForwardIf(svc, nil)
}

// StartForwardIf is StartForward for settings that may have changed since
// they were read: current (if not nil) is asked, under m.mu, first (a
// forward already running for the service may be for newer settings) and
// again just before the forward is published; the start is cancelled
// (ErrStartCancelled) if it says no. current must not call into m. A caller
// that edits a service makes current answer no before calling StopForward,
// so either the forward is published first and StopForward stops it, or
// current refuses it.
func (m *Manager) StartForwardIf(svc model.Service, current func() bool) (ForwardStatus, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ForwardStatus{}, ErrClosed
	}
	if current != nil && !current() {
		// Stale settings: the running forward (if any) isn't theirs.
		m.mu.Unlock()
		m.log.Info("forward start cancelled", "service", svc.ID)
		return ForwardStatus{}, ErrStartCancelled
	}
	if f, ok := m.forwards[svc.ID]; ok {
		m.mu.Unlock()
		return f.status(), nil
	}
	if _, ok := m.starting[svc.ID]; ok {
		m.mu.Unlock()
		return ForwardStatus{}, errors.New("this service is already starting")
	}
	p := &startingForward{serverID: svc.ServerID}
	m.starting[svc.ID] = p
	m.mu.Unlock()

	unreserve := func() {
		m.mu.Lock()
		if m.starting[svc.ID] == p {
			delete(m.starting, svc.ID)
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

	f := &forward{m: m, svc: svc, sc: sc, ln: ln, port: ln.Addr().(*net.TCPAddr).Port, conns: map[net.Conn]struct{}{}, stop: make(chan struct{})}
	m.mu.Lock()
	closed := m.closed
	ok := !closed && m.starting[svc.ID] == p && (current == nil || current())
	if m.starting[svc.ID] == p {
		delete(m.starting, svc.ID)
	}
	if ok {
		m.forwards[svc.ID] = f
	}
	m.mu.Unlock()
	if !ok {
		ln.Close()
		m.release(sc)
		if closed {
			return ForwardStatus{}, ErrClosed
		}
		m.log.Info("forward start cancelled", "service", svc.ID)
		return ForwardStatus{}, ErrStartCancelled
	}
	f.wg.Add(1)
	go f.serve()
	m.log.Info("forward started", "service", svc.ID, "localPort", f.port)
	st := f.status()
	m.fwdEvents.Lock() // see Manager.fwdEvents
	m.mu.Lock()
	running := m.forwards[svc.ID] == f // not stopped already
	m.mu.Unlock()
	if running {
		m.emit(Event{Kind: "forward", ID: svc.ID, ServerID: svc.ServerID, State: st.State, LocalPort: f.port})
	}
	m.fwdEvents.Unlock()
	return st, nil
}

// StopForward stops a forward and closes its browser connections. The
// server connection closes too if nothing else uses it.
func (m *Manager) StopForward(serviceID string) {
	m.stopForward(serviceID, StateStopped, nil)
}

func (m *Manager) stopForward(serviceID string, final State, cause error) {
	m.stopForwardIf(serviceID, nil, final, cause)
}

// stopForwardIf is stopForward; with only set, it stops the service's
// forward just if that is still only, and otherwise leaves the service
// alone (its forward and any start under way are newer than only).
func (m *Manager) stopForwardIf(serviceID string, only *forward, final State, cause error) {
	m.fwdEvents.Lock() // see Manager.fwdEvents
	m.mu.Lock()
	f := m.forwards[serviceID]
	if only != nil && f != only {
		m.mu.Unlock()
		m.fwdEvents.Unlock()
		return
	}
	if only == nil {
		delete(m.starting, serviceID) // a start still under way is cancelled
	}
	if f == nil {
		m.mu.Unlock()
		m.fwdEvents.Unlock()
		return
	}
	delete(m.forwards, serviceID)
	m.mu.Unlock()
	if testHookForwardRemoved != nil {
		testHookForwardRemoved(serviceID)
	}
	// The final event goes out now, before closing (which waits for the
	// forward's connections): a replacement started meanwhile publishes
	// its own events only after this one.
	m.emit(Event{Kind: "forward", ID: serviceID, ServerID: f.svc.ServerID, State: final, Error: errString(cause)})
	m.fwdEvents.Unlock()

	f.close()
	m.release(f.sc)
	m.log.Info("forward stopped", "service", serviceID)
}

// testHookForwardRemoved, if set (by tests), runs in stopForwardIf once the
// forward is removed, before its final event.
var testHookForwardRemoved func(serviceID string)

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
		out = append(out, f)
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
	// Under fwdEvents, no forward found here can be stopped (and its final
	// event published) before its state is: see Manager.fwdEvents.
	m.fwdEvents.Lock()
	defer m.fwdEvents.Unlock()
	for _, f := range m.forwardsFor(serverID) {
		st := f.status()
		m.emit(Event{Kind: "forward", ID: st.ServiceID, ServerID: serverID, State: st.State, Error: st.Error, LocalPort: st.LocalPort})
	}
}

func (f *forward) status() ForwardStatus {
	ss := f.sc.status()
	st, errText := ss.State, ss.Error
	if st == StateConnected {
		st = StateActive
	}
	f.mu.Lock()
	n := len(f.conns)
	f.mu.Unlock()
	return ForwardStatus{
		ServiceID: f.svc.ID, ServerID: f.svc.ServerID, LocalPort: f.port,
		State: st, Error: errText, Connections: n,
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
	remote, err := f.dial(client, target)
	if err != nil {
		f.m.log.Info("forward: remote connect failed", "service", f.svc.ID)
		return
	}
	defer remote.Close()

	// Copy both ways. When one direction ends, pass the end-of-stream on
	// (half-close) and keep the other direction open until it finishes too,
	// so responses are never cut short.
	done := make(chan struct{}, 2)
	// The bytes are counted for the dashboard's traffic figures.
	id := f.svc.ID
	toServer := countingWriter{remote, func(n int) { f.m.traffic.add(id, n, false) }}
	toBrowser := countingWriter{local, func(n int) { f.m.traffic.add(id, n, true) }}
	go func() { io.Copy(toServer, local); closeWrite(remote); done <- struct{}{} }()
	go func() { io.Copy(toBrowser, remote); closeWrite(local); done <- struct{}{} }()
	stop := f.stop
	for finished := 0; finished < 2; {
		select {
		case <-done:
			finished++
		case <-stop:
			// The forward is stopping. A service may keep its side open after
			// the end-of-stream (an idle websocket, say); don't wait for it.
			remote.Close()
			stop = nil
		}
	}
}

// dial opens the SSH channel to the service. It gives up when the forward
// stops: a server can keep answering keepalives yet never answer the
// channel request, and close waits for every handler. The abandoned
// request ends when the connection closes (or is closed if it succeeds).
func (f *forward) dial(client *ssh.Client, target string) (net.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := client.Dial("tcp", target)
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-f.stop:
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, errors.New("the forward is stopping")
	}
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
	close(f.stop)
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
