package sshx

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrNotConnected means the server has no open connection right now.
var ErrNotConnected = errors.New("not connected")

// ErrOutputTooLarge means a command printed more than its limit.
var ErrOutputTooLarge = errors.New("output too large")

// RunIfConnected runs a fixed command on the server over its existing
// connection and returns what it printed (stdout, at most limit bytes). It
// never opens a connection: if the server isn't connected right now it
// returns ErrNotConnected. A non-zero exit status is not an error (the
// output is still returned); a timeout is.
//
// It is used only for the opt-in server health check, whose command is a
// constant (see internal/health): never pass it anything a user typed.
func (m *Manager) RunIfConnected(serverID, command string, limit int, timeout time.Duration) ([]byte, error) {
	m.mu.Lock()
	sc := m.servers[serverID]
	m.mu.Unlock()
	if sc == nil || sc.status().State != StateConnected {
		return nil, ErrNotConnected
	}
	return sc.run(command, limit, timeout)
}

// Run is RunIfConnected for a command the user asked for: it connects to the
// server first if needed (and lets the connection go afterwards, so it
// closes unless something else uses it). It returns the same connection
// errors as TestConnection, so it can drive the host-key confirmation.
//
// It is used only for "Find services", whose command is a constant (see
// internal/discover), and only when the user clicks: never pass it anything
// a user typed, and never call it in the background.
func (m *Manager) Run(serverID, command string, limit int, timeout time.Duration) ([]byte, error) {
	sc, err := m.acquire(serverID)
	if err != nil {
		return nil, err
	}
	defer m.release(sc)
	return sc.run(command, limit, timeout)
}

var errTooSlow = errors.New("the server took too long to answer")

func (sc *serverConn) run(command string, limit int, timeout time.Duration) ([]byte, error) {
	client := sc.currentClient()
	if client == nil {
		return nil, ErrNotConnected
	}
	// The timeout covers opening the session too: a server can keep
	// answering keepalives yet never answer the request for one.
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	type opened struct {
		session *ssh.Session
		err     error
	}
	ch := make(chan opened, 1)
	go func() {
		s, err := client.NewSession()
		ch <- opened{s, err}
	}()
	var session *ssh.Session
	select {
	case o := <-ch:
		if o.err != nil {
			return nil, fmt.Errorf("can't open a session: %w", o.err)
		}
		session = o.session
	case <-deadline.C:
		go func() { // close it if it opens after all
			if o := <-ch; o.session != nil {
				o.session.Close()
			}
		}()
		return nil, errTooSlow
	}
	defer session.Close()
	out := &limitedBuffer{limit: limit}
	session.Stdout = out
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	var err error
	select {
	case err = <-done:
	case <-deadline.C:
		return nil, errTooSlow
	}
	var exit *ssh.ExitError
	if err != nil && !errors.As(err, &exit) {
		return nil, err
	}
	if out.overflow {
		return nil, ErrOutputTooLarge
	}
	return out.buf, nil
}

// limitedBuffer keeps the first limit bytes written to it and notes whether
// more came (it keeps accepting, so the command isn't cut off mid-write).
type limitedBuffer struct {
	buf      []byte
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := b.limit - len(b.buf)
	if n > room {
		b.overflow = true
		p = p[:max(room, 0)]
	}
	b.buf = append(b.buf, p...)
	return n, nil // all of it "written", so the command isn't interrupted
}
