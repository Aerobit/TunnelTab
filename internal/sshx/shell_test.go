package sshx

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Aerobit/TunnelTab/internal/model"
)

// termReader collects a shell's output in the background.
type termReader struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func readAll(sh *Shell) *termReader {
	r := &termReader{}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := sh.Read(b)
			r.mu.Lock()
			r.buf.Write(b[:n])
			r.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return r
}

func (r *termReader) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func (r *termReader) waitFor(t *testing.T, want string) {
	t.Helper()
	waitFor(t, "terminal output "+want, func() bool { return strings.Contains(r.String(), want) })
}

func TestShellBasics(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	m := newManager(t, e, nil)

	sh, err := m.OpenShell(s.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	out := readAll(sh)
	out.waitFor(t, "$ ")

	io.WriteString(sh, "echo hello terminal\r")
	out.waitFor(t, "hello terminal\r\n")

	io.WriteString(sh, "size\r")
	out.waitFor(t, "80x24")
	if err := sh.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	// The resize and the next keystrokes travel separately, so the shell
	// may see "size" before the new size: ask until it reports it.
	waitFor(t, "resize to 120x40", func() bool {
		io.WriteString(sh, "size\r")
		time.Sleep(20 * time.Millisecond)
		return strings.Contains(out.String(), "120x40")
	})

	if m.ShellCount() != 1 || srv.ActiveConnections() != 1 {
		t.Fatalf("shells=%d connections=%d", m.ShellCount(), srv.ActiveConnections())
	}

	io.WriteString(sh, "exit 3\r")
	select {
	case <-sh.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shell did not end after exit")
	}
	if code, err := sh.ExitStatus(); code != 3 || err != nil {
		t.Fatalf("exit status %d, %v; want 3", code, err)
	}
	if m.ShellCount() != 0 {
		t.Fatal("finished shell still counted")
	}
	waitFor(t, "connection to close", func() bool { return srv.ActiveConnections() == 0 })
}

func TestShellSharesConnectionWithForwards(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	host, port := backend(t, "x")
	m := newManager(t, e, nil)

	m.StartForward(service(s.ID, host, port))
	sh, err := m.OpenShell(s.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Logins() != 1 {
		t.Fatalf("logins=%d; terminal should reuse the tunnel's connection", srv.Logins())
	}
	m.StopAll() // stops forwards only
	if m.ShellCount() != 1 || srv.ActiveConnections() != 1 {
		t.Fatal("stopping tunnels closed the terminal or its connection")
	}
	sh.Close()
	waitFor(t, "connection to close", func() bool { return srv.ActiveConnections() == 0 })
}

func TestCloseShellsAndStopServer(t *testing.T) {
	e := newEnv()
	_, s := passwordServer(t, e)
	m := newManager(t, e, nil)
	a, _ := m.OpenShell(s.ID, 80, 24)
	b, _ := m.OpenShell(s.ID, 80, 24)

	m.CloseShells()
	for _, sh := range []*Shell{a, b} {
		select {
		case <-sh.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("CloseShells left a terminal open")
		}
	}

	c, _ := m.OpenShell(s.ID, 80, 24)
	m.StopServer(s.ID)
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("StopServer left a terminal open")
	}
}

func TestShellEndsWhenConnectionDrops(t *testing.T) {
	e := newEnv()
	srv, s := passwordServer(t, e)
	m := newManager(t, e, nil)
	sh, err := m.OpenShell(s.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	srv.DropConnections()
	select {
	case <-sh.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("terminal did not end when the connection dropped")
	}
	if code, _ := sh.ExitStatus(); code != -1 {
		t.Fatalf("exit code %d, want -1 (connection lost)", code)
	}
	waitFor(t, "no shells", func() bool { return m.ShellCount() == 0 })
}

func TestShellErrors(t *testing.T) {
	e := newEnv()
	_, s := passwordServer(t, e)
	m := newManager(t, e, nil)
	if _, err := m.OpenShell(s.ID, 0, 24); err == nil {
		t.Error("zero width accepted")
	}
	if _, err := m.OpenShell(s.ID, 80, 5000); err == nil {
		t.Error("huge height accepted")
	}
	sh, _ := m.OpenShell(s.ID, 80, 24)
	if err := sh.Resize(-1, 10); err == nil {
		t.Error("negative resize accepted")
	}
	sh.Close()

	// A second server entry for the same host, with the wrong password.
	wrong := model.Server{ID: model.NewID(), Name: "wrong", Host: s.Host, Port: s.Port, Username: user,
		Auth: model.Auth{Type: model.AuthPassword, Password: "wrong-pw-123"}}
	e.mu.Lock()
	e.servers[wrong.ID] = wrong
	e.mu.Unlock()
	if _, err := m.OpenShell(wrong.ID, 80, 24); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("got %v, want ErrAuthFailed", err)
	}
}
