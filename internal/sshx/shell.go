package sshx

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// MaxShells limits how many terminals can be open at once.
const MaxShells = 32

// ErrShellClosed is the exit error of a terminal closed by TunnelTab itself
// (Close, CloseShells, StopServer), as opposed to the shell exiting or the
// connection dropping.
var ErrShellClosed = errors.New("terminal closed")

// Shell is an interactive terminal session on a server. Read gives the
// terminal output, Write sends keystrokes, Resize reports the window size.
// Close ends it; Done is closed when the session ends for any reason.
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
	client := sc.currentClient()
	if client == nil {
		m.release(sc)
		return nil, errors.New("the server is reconnecting; try again in a moment")
	}
	session, err := client.NewSession()
	if err != nil {
		m.release(sc)
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

	sh := &Shell{m: m, sc: sc, serverID: serverID, session: session, stdin: stdin, stdout: stdout, done: make(chan struct{})}
	m.mu.Lock()
	m.shells[sh] = struct{}{}
	m.mu.Unlock()
	m.log.Info("terminal opened", "server", serverID)
	go func() {
		err := session.Wait()
		var exit *ssh.ExitError
		switch {
		case err == nil:
			sh.end(0, nil)
		case errors.As(err, &exit):
			sh.end(exit.ExitStatus(), nil)
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
// shell exited, (-1, ErrShellClosed) when TunnelTab closed it, or (-1, err)
// when the connection was lost. Valid after Done is closed.
func (s *Shell) ExitStatus() (int, error) { return s.exitCode, s.exitErr }

// ServerID returns the server this terminal is connected to.
func (s *Shell) ServerID() string { return s.serverID }

// Close ends the session.
func (s *Shell) Close() {
	s.end(-1, ErrShellClosed)
	s.session.Close()
}

// end records how the session ended (first caller wins) and releases it.
// The exit fields are written only here, before done is closed, so reading
// them after Done is race-free.
func (s *Shell) end(code int, err error) {
	s.once.Do(func() {
		s.exitCode, s.exitErr = code, err
		s.m.mu.Lock()
		delete(s.m.shells, s)
		s.m.mu.Unlock()
		close(s.done)
		s.m.release(s.sc)
		s.m.log.Info("terminal closed", "server", s.serverID)
	})
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
