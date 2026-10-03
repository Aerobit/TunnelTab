package webcheck

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dialer(addr string) Dialer {
	return func() (net.Conn, error) { return net.DialTimeout("tcp", addr, time.Second) }
}

func host(s *httptest.Server) string {
	return strings.TrimPrefix(strings.TrimPrefix(s.URL, "https://"), "http://")
}

// rawServer accepts connections and hands each to handle.
func rawServer(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); handle(c) }()
		}
	}()
	return l.Addr().String()
}

func TestCheck(t *testing.T) {
	var gotMethod, gotCookie string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotCookie = r.Method, r.Header.Get("Cookie")
		if r.URL.Path == "/login" {
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	plain := httptest.NewServer(h)
	defer plain.Close()
	secure := httptest.NewTLSServer(h) // self-signed
	defer secure.Close()

	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closedAddr := closed.Addr().String()
	closed.Close()

	ssh := rawServer(t, func(c net.Conn) { c.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n")); time.Sleep(50 * time.Millisecond) })
	silent := rawServer(t, func(c net.Conn) { time.Sleep(2 * time.Second) })
	flood := rawServer(t, func(c net.Conn) {
		c.Write([]byte("HTTP/1.1 200 OK\r\n"))
		for i := 0; i < 20000; i++ {
			if _, err := c.Write([]byte("X-Junk: " + strings.Repeat("a", 64) + "\r\n")); err != nil {
				return
			}
		}
	})

	for _, tc := range []struct {
		name, addr, path string
		want             Result
	}{
		{"http", host(plain), "/", Result{State: Responding, Protocol: "http", Status: 200}},
		{"https, self-signed", host(secure), "/", Result{State: Responding, Protocol: "https", Status: 200}},
		{"path and redirect", host(plain), "/login", Result{State: Responding, Protocol: "http", Status: 302}},
		{"nothing listening", closedAddr, "/", Result{State: NoAnswer}},
		{"ssh, not web", ssh, "/", Result{State: NotWeb}},
		{"silent", silent, "/", Result{State: NotWeb}},
		{"endless headers", flood, "/", Result{State: NotWeb}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			got := Check(dialer(tc.addr), tc.path, 300*time.Millisecond)
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatalf("took %v", time.Since(start))
			}
		})
	}
	if gotMethod != http.MethodHead || gotCookie != "" {
		t.Fatalf("sent %s with cookie %q", gotMethod, gotCookie)
	}
}

// A server that never answers the SSH channel-open request: ssh.Client.Dial
// blocks. The check must still end within its timeout, try only once, and
// close the connection if it opens later.
func TestDialCountsTowardsTimeout(t *testing.T) {
	release := make(chan struct{})
	ours, theirs := net.Pipe()
	defer theirs.Close()
	var dials atomic.Int32 // counted on dialWithin's goroutine
	dial := func() (net.Conn, error) {
		dials.Add(1)
		<-release
		return ours, nil
	}
	start := time.Now()
	got := Check(dial, "/", 200*time.Millisecond)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("took %v", took)
	}
	if got != (Result{State: NoAnswer}) || dials.Load() != 1 {
		t.Fatalf("got %+v after %d dials", got, dials.Load())
	}
	close(release)
	theirs.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := theirs.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("late connection not closed: %v", err)
	}
}

func TestPathIsSentOnlyWhenClean(t *testing.T) {
	paths := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { paths <- r.RequestURI }))
	defer srv.Close()
	for in, want := range map[string]string{
		"/app/":               "/app/",
		"":                    "/",
		"app":                 "/",
		"//evil.example.com/": "/",
		"/a b":                "/",
		"/a\r\nX-Injected: 1": "/",
		"/ünïcode":            "/",
	} {
		if r := Check(dialer(host(srv)), in, time.Second); r.State != Responding {
			t.Fatalf("%q: %+v", in, r)
		}
		if got := <-paths; got != want {
			t.Fatalf("%q: sent %q, want %q", in, got, want)
		}
	}
}

func FuzzCleanPath(f *testing.F) {
	for _, s := range []string{"/", "/a/b?c=d", "x", "/\r\n", "//h"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p := cleanPath(s)
		if p == "" || p[0] != '/' || strings.HasPrefix(p, "//") || strings.ContainsAny(p, " \r\n\t\x00") {
			t.Fatalf("cleanPath(%q) = %q", s, p)
		}
	})
}
