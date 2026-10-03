// Package webcheck tells whether a port answers like a web page: it sends
// one HEAD request, first over TLS (https) and then in plain text (http),
// over connections the caller opens — in TunnelTab, through the server's SSH
// connection, so the request goes to the service's address as the server
// sees it, exactly where a tunnel would.
//
// Only a HEAD request with fixed headers is sent: no cookies, no body,
// nothing the user typed except the service's path (validated). Certificates
// aren't verified: the check only asks "does it answer?", and self-signed
// certificates are normal for self-hosted apps.
package webcheck

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// State is what a check found.
type State string

const (
	Responding State = "responding" // answered an HTTP request (any status)
	NotWeb     State = "not_web"    // accepted the connection, but no HTTP answer
	NoAnswer   State = "no_answer"  // couldn't connect: nothing listens there
)

// Result is the outcome of one check.
type Result struct {
	State    State  `json:"state"`
	Protocol string `json:"protocol,omitempty"` // "https" or "http", when Responding
	Status   int    `json:"status,omitempty"`   // HTTP status, when Responding
}

// Dialer opens a new connection to the port being checked.
type Dialer func() (net.Conn, error)

// MaxHeader bounds how much of the answer is read.
const MaxHeader = 64 << 10

// Check checks one port: https first (a plain-HTTP server refuses the TLS
// handshake at once), then http. Each attempt, opening the connection
// included, may take up to timeout. If the first connection can't be opened
// (refused, or not open within timeout), the port gets no second try.
func Check(dial Dialer, path string, timeout time.Duration) Result {
	path = cleanPath(path)
	connected := false
	for _, https := range []bool{true, false} {
		status, err := try(dial, https, path, timeout)
		if err == nil {
			p := "http"
			if https {
				p = "https"
			}
			return Result{State: Responding, Protocol: p, Status: status}
		}
		var de *dialError
		if !errors.As(err, &de) {
			connected = true
		} else if !connected {
			return Result{State: NoAnswer} // nothing to talk to: don't try again
		}
	}
	return Result{State: NotWeb}
}

// dialError marks a failure to connect at all.
type dialError struct{ err error }

func (e *dialError) Error() string { return "connect: " + e.err.Error() }
func (e *dialError) Unwrap() error { return e.err }

// errDialTimeout: the connection wasn't open in time (for SSH, the server
// didn't answer the channel-open request).
var errDialTimeout = errors.New("timed out")

// try makes one HEAD request and returns the status code. The whole
// attempt, opening the connection included, takes at most timeout.
func try(dial Dialer, https bool, path string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	conn, err := dialWithin(dial, timeout)
	if err != nil {
		return 0, &dialError{err}
	}
	// SSH channels don't support deadlines: a timer closes the connection.
	timer := time.AfterFunc(time.Until(deadline), func() { conn.Close() })
	defer timer.Stop()
	defer conn.Close()

	var c net.Conn = conn
	if https {
		tc := tls.Client(conn, &tls.Config{
			InsecureSkipVerify: true, // only "does it answer?"; nothing secret is sent
			ServerName:         "localhost",
			MinVersion:         tls.VersionTLS10, // old appliances still answer
		})
		if err := tc.Handshake(); err != nil {
			return 0, fmt.Errorf("tls: %w", err)
		}
		c = tc
	}
	req := "HEAD " + path + " HTTP/1.1\r\nHost: localhost\r\nUser-Agent: TunnelTab\r\nAccept: */*\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		return 0, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(io.LimitReader(c, MaxHeader)), &http.Request{Method: http.MethodHead})
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// dialWithin calls dial but gives up after timeout: ssh.Client.Dial has no
// timeout of its own and waits as long as the server doesn't answer. A
// connection that opens after giving up is closed.
func dialWithin(dial Dialer, timeout time.Duration) (net.Conn, error) {
	type dialed struct {
		conn net.Conn
		err  error
	}
	ch := make(chan dialed, 1)
	go func() {
		c, err := dial()
		ch <- dialed{c, err}
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case d := <-ch:
		return d.conn, d.err
	case <-t.C:
		go func() {
			if d := <-ch; d.conn != nil {
				d.conn.Close()
			}
		}()
		return nil, errDialTimeout
	}
}

// cleanPath returns path if it is a plain absolute URL path, otherwise "/".
func cleanPath(path string) string {
	if path == "" || path[0] != '/' || len(path) > 2048 {
		return "/"
	}
	for _, r := range path {
		if r <= ' ' || r == 0x7f || r > '~' {
			return "/"
		}
	}
	if strings.HasPrefix(path, "//") {
		return "/"
	}
	return path
}
