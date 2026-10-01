package sshx

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// MaxShells limits how many terminals can be open at once.
const MaxShells = 32

// ErrShellClosed is the exit error of a terminal closed by TunnelTab itself
// (Close, CloseShells, StopServer), as opposed to the shell exiting or the
// connection dropping.
var ErrShellClosed = errors.New("terminal closed")

// ErrReconnecting is returned by Reopen while the server's connection is
// down and being re-established.
var ErrReconnecting = errors.New("the server is reconnecting")

// Shell is an interactive terminal session on a server. Read gives the
// terminal output, Write sends keystrokes, Resize reports the window size.
// Close ends it; Done is closed when the session ends for any reason.
//
// A shell whose connection dropped keeps the connection (so it reconnects
// in the background, as for tunnels) until Reopen replaces it or Close
// lets go; Dropped is closed then.
type Shell struct {
	m        *Manager
	sc       *serverConn
	serverID string
	session  *ssh.Session
	stdin    io.WriteCloser
	stdout   io.Reader

	done     chan struct{}
	once     sync.Once
	exitCode int
	exitErr  error

	held    bool          // keeps sc after the connection dropped (guarded by m.mu)
	dropped chan struct{} // closed when the shell no longer holds sc
}

// OpenShell starts an interactive shell with a pseudo-terminal of the given
// size, connecting to the server if needed. It returns the same connection
// errors as StartForward (including *UnknownHostKeyError).
func (m *Manager) OpenShell(serverID string, cols, rows int) (*Shell, error) {
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 1000 {
		return nil, fmt.Errorf("invalid terminal size %dx%d", cols, rows)
	}
	m.mu.Lock()
	if len(m.shells) >= MaxShells {
		m.mu.Unlock()
		return nil, fmt.Errorf("too many open terminals (max %d)", MaxShells)
	}
	m.mu.Unlock()

	sc, err := m.acquire(serverID)
	if err != nil {
		return nil, err
	}
	if sc.currentClient() == nil {
		m.release(sc)
		return nil, errors.New("the server is reconnecting; try again in a moment")
	}
	return m.startShell(sc, cols, rows)
}

// Reopen opens a new shell in place of s, whose connection dropped, once
// the connection is back. It returns ErrReconnecting while it isn't yet, and
// the reason if reconnecting gave up. On success s lets go of the connection.
func (s *Shell) Reopen(cols, rows int) (*Shell, error) {
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 1000 {
		return nil, fmt.Errorf("invalid terminal size %dx%d", cols, rows)
	}
	m, sc := s.m, s.sc
	m.mu.Lock()
	if !s.held {
		m.mu.Unlock()
		return nil, ErrShellClosed
	}
	sc.mu.Lock()
	state, failure, done := sc.state, sc.err, sc.done
	if !done {
		sc.users++ // the new shell's reference
	}
	sc.mu.Unlock()
	m.mu.Unlock()
	switch {
	case done:
		return nil, ErrClosed
	case state == StateFailed:
		m.release(sc)
		return nil, failure
	case sc.currentClient() == nil:
		m.release(sc)
		return nil, ErrReconnecting
	}
	ns, err := m.startShell(sc, cols, rows)
	if err != nil {
		return nil, err
	}
	s.drop()
	return ns, nil
}

// startShell opens a shell on sc, using a reference to sc the caller took
// (released if it fails).
func (m *Manager) startShell(sc *serverConn, cols, rows int) (*Shell, error) {
	client := sc.currentClient()
	if client == nil {
		m.release(sc)
		return nil, ErrReconnecting
	}
	session, err := client.NewSession()
	if err != nil {
		m.release(sc)
		if sc.currentClient() != client {
			return nil, ErrReconnecting // it dropped just now
		}
		return nil, fmt.Errorf("can't open a session: %w", err)
	}
	fail := func(what string, err error) (*Shell, error) {
		session.Close()
		m.release(sc)
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		return fail("the server refused a terminal", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		return fail("terminal input", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return fail("terminal output", err)
	}
	session.Stderr = nil // with a PTY, errors arrive on stdout
	if err := session.Shell(); err != nil {
		return fail("the server refused a shell", err)
	}

	sh := &Shell{m: m, sc: sc, serverID: sc.id, session: session, stdin: stdin, stdout: stdout,
		done: make(chan struct{}), dropped: make(chan struct{})}
	m.mu.Lock()
	m.shells[sh] = struct{}{}
	m.mu.Unlock()
	m.log.Info("terminal opened", "server", sc.id)
	go func() {
		err := session.Wait()
		var exit *ssh.ExitError
		switch {
		case err == nil:
			sh.end(0, nil)
		case errors.As(err, &exit):
			sh.end(exit.ExitStatus(), nil)
		case errors.As(err, new(*ssh.ExitMissingError)) && !connectionClosed(client, lostConnectionWait):
			// No exit code, but the connection is fine: the server just
			// ended the session. (A dropped connection looks the same at
			// first.)
			sh.end(-1, nil)
		default:
			sh.end(-1, err)
		}
	}()
	return sh, nil
}

// Read reads terminal output.
func (s *Shell) Read(p []byte) (int, error) { return s.stdout.Read(p) }

// Write sends input (keystrokes, pasted text) to the terminal.
func (s *Shell) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Resize tells the server the terminal's new size.
func (s *Shell) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 1000 {
		return fmt.Errorf("invalid terminal size %dx%d", cols, rows)
	}
	return s.session.WindowChange(rows, cols)
}

// Done is closed when the session has ended.
func (s *Shell) Done() <-chan struct{} { return s.done }

// ExitStatus returns the shell's exit code and error: (code, nil) when the
// shell exited (code -1 if the server sent none), (-1, ErrShellClosed) when TunnelTab closed it, or (-1, err)
// when the connection was lost. Valid after Done is closed.
func (s *Shell) ExitStatus() (int, error) { return s.exitCode, s.exitErr }

// ServerID returns the server this terminal is connected to.
func (s *Shell) ServerID() string { return s.serverID }

// Close ends the session, and lets go of the connection if the session had
// kept it after a drop.
func (s *Shell) Close() {
	s.end(-1, ErrShellClosed)
	s.session.Close()
	s.drop()
}

// Dropped is closed when the shell has ended and no longer keeps its
// connection: at once for a normal end, or (after a drop) on Reopen or Close.
func (s *Shell) Dropped() <-chan struct{} { return s.dropped }

// end records how the session ended (first caller wins). The exit fields are
// written only here, before done is closed, so reading them after Done is
// race-free. If the connection dropped, the shell keeps it (and its place in
// m.shells, so StopServer and CloseShells still find it) until drop.
func (s *Shell) end(code int, err error) {
	s.once.Do(func() {
		s.exitCode, s.exitErr = code, err
		lost := err != nil && !errors.Is(err, ErrShellClosed)
		s.m.mu.Lock()
		s.held = true
		s.m.mu.Unlock()
		close(s.done)
		if lost {
			s.m.log.Info("terminal lost its connection", "server", s.serverID)
			return
		}
		s.drop()
		s.m.log.Info("terminal closed", "server", s.serverID)
	})
}

// drop lets go of the connection (once).
func (s *Shell) drop() {
	s.m.mu.Lock()
	held := s.held
	s.held = false
	if held {
		delete(s.m.shells, s)
	}
	s.m.mu.Unlock()
	if held {
		close(s.dropped)
		s.m.release(s.sc)
	}
}

// CloseShells ends every open terminal (used when shutting down).
func (m *Manager) CloseShells() {
	m.mu.Lock()
	list := make([]*Shell, 0, len(m.shells))
	for s := range m.shells {
		list = append(list, s)
	}
	m.mu.Unlock()
	for _, s := range list {
		s.Close()
	}
}

// ShellCount returns the number of open terminals.
func (m *Manager) ShellCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.shells)
}

// lostConnectionWait is how long a session that ended without an exit code
// waits to see whether its connection went down too (a variable for tests).
var lostConnectionWait = 2 * time.Second

// connectionClosed reports whether client's connection closes within wait.
func connectionClosed(client *ssh.Client, wait time.Duration) bool {
	closed := make(chan struct{})
	go func() {
		client.Wait()
		close(closed)
	}()
	select {
	case <-closed:
		return true
	case <-time.After(wait):
		return false
	}
}
