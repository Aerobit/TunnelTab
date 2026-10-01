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
	client := sc.currentClient()
	if client == nil {
		return nil, ErrNotConnected
	}
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("can't open a session: %w", err)
	}
	defer session.Close()
	out := &limitedBuffer{limit: limit}
	session.Stdout = out
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case err = <-done:
	case <-time.After(timeout):
		return nil, errors.New("the server took too long to answer")
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
