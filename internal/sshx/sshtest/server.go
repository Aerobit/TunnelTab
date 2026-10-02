// Package sshtest runs a small in-process SSH server for tests. It supports
// password, keyboard-interactive and public-key login, keep-alive requests,
// direct-tcpip port forwarding and interactive shells with a PTY (a tiny
// fake shell, see shell.go), and can drop all connections on demand
// to exercise reconnection. It is only for tests; never use it in the app.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Options configure the test server.
type Options struct {
	User                string          // required username
	Password            string          // enables password login when set
	KeyboardInteractive bool            // also accept Password via keyboard-interactive
	KeyboardOnly        bool            // accept Password only via keyboard-interactive
	AuthorizedKeys      []ssh.PublicKey // enables public-key login
	HostKey             ssh.Signer      // generated when nil

	// Fake shell appearance (defaults: "Welcome to the fake shell", "$ ").
	Banner string
	Prompt string
	// Commands maps an exact command line to canned output (demo mode).
	Commands map[string]string
	// Exec maps an exact one-off command (an SSH "exec" request) to its
	// output. Other commands fail with exit status 127.
	Exec map[string]string

	// Addr to listen on (default 127.0.0.1:0, a random port).
	Addr string

	// Redirect sends forwarded connections for a port (any host) to another
	// address instead, e.g. a demo app standing in for "n8n on 5678".
	Redirect map[int]string
}

// Server is a running test SSH server.
type Server struct {
	Addr    string // 127.0.0.1:port
	Host    string // 127.0.0.1
	Port    int
	HostKey ssh.Signer

	opts     Options
	ln       net.Listener
	logins   atomic.Int64
	mu       sync.Mutex
	conns    map[*ssh.ServerConn]bool
	closed   bool
	wg       sync.WaitGroup
	refusing atomic.Bool
}

// NewHostKey returns a fresh ed25519 signer.
func NewHostKey(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Start launches a server on 127.0.0.1 and stops it when the test ends.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()
	if opts.HostKey == nil {
		opts.HostKey = NewHostKey(t)
	}
	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: opts, ln: ln, HostKey: opts.HostKey, conns: map[*ssh.ServerConn]bool{}}
	s.Addr = ln.Addr().String()
	host, port, _ := net.SplitHostPort(s.Addr)
	s.Host = host
	s.Port, _ = strconv.Atoi(port)
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(s.Close)
	return s
}

// Logins returns how many successful SSH logins the server has handled.
func (s *Server) Logins() int { return int(s.logins.Load()) }

// ActiveConnections returns the number of currently open SSH connections.
func (s *Server) ActiveConnections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// DropConnections closes every open SSH connection (simulating a network
// outage or server restart). New connections are still accepted.
func (s *Server) DropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.Close()
	}
}

// SetRefusing makes the server close new TCP connections immediately
// (simulating the server being unreachable) until called with false.
func (s *Server) SetRefusing(refuse bool) { s.refusing.Store(refuse) }

// Close stops the server and closes all connections.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.ln.Close()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) config() *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{}
	if s.opts.Password != "" {
		if !s.opts.KeyboardOnly {
			cfg.PasswordCallback = func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
				if c.User() == s.opts.User && string(pw) == s.opts.Password {
					return nil, nil
				}
				return nil, errDenied
			}
		}
		if s.opts.KeyboardInteractive || s.opts.KeyboardOnly {
			cfg.KeyboardInteractiveCallback = func(c ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
				ans, err := ch("", "", []string{"Password: "}, []bool{false})
				if err == nil && c.User() == s.opts.User && len(ans) == 1 && ans[0] == s.opts.Password {
					return nil, nil
				}
				return nil, errDenied
			}
		}
	}
	if len(s.opts.AuthorizedKeys) > 0 {
		cfg.PublicKeyCallback = func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() != s.opts.User {
				return nil, errDenied
			}
			for _, k := range s.opts.AuthorizedKeys {
				if string(k.Marshal()) == string(key.Marshal()) {
					return nil, nil
				}
			}
			return nil, errDenied
		}
	}
	cfg.AddHostKey(s.opts.HostKey)
	return cfg
}

type deniedError struct{}

func (deniedError) Error() string { return "denied" }

var errDenied = deniedError{}

func (s *Server) serve() {
	defer s.wg.Done()
	cfg := s.config()
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		if s.refusing.Load() {
			nc.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(nc, cfg)
		}()
	}
}

func (s *Server) handle(nc net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		conn.Close()
		return
	}
	s.conns[conn] = true
	s.mu.Unlock()
	s.logins.Add(1)
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()

	go func() {
		for req := range reqs {
			// keepalive@openssh.com and anything else: acknowledge if asked.
			if req.WantReply {
				req.Reply(req.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	for nc := range chans {
		switch nc.ChannelType() {
		case "direct-tcpip":
			go s.handleDirectTCPIP(nc)
		case "session":
			go handleSession(nc, s.opts)
		default:
			nc.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
	conn.Wait()
}

// handleDirectTCPIP connects a forwarded channel to its target address.
func (s *Server) handleDirectTCPIP(nc ssh.NewChannel) {
	extra := nc.ExtraData()
	host, rest, ok := readString(extra)
	if !ok || len(rest) < 4 {
		nc.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}
	port := binary.BigEndian.Uint32(rest)
	addr := net.JoinHostPort(host, strconv.Itoa(int(port)))
	if to, ok := s.opts.Redirect[int(port)]; ok {
		addr = to
	}
	target, err := net.Dial("tcp", addr)
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		target.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() {
		io.Copy(ch, target)
		ch.CloseWrite()
	}()
	io.Copy(target, ch)
	target.Close()
	ch.Close()
}

func readString(b []byte) (string, []byte, bool) {
	if len(b) < 4 {
		return "", nil, false
	}
	n := binary.BigEndian.Uint32(b)
	if uint32(len(b)-4) < n {
		return "", nil, false
	}
	return string(b[4 : 4+n]), b[4+n:], true
}
