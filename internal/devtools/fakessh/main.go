// Command fakessh runs a local test SSH server with a small demo web app
// behind it, so the dashboard can be tried without a real VPS. It is a
// development tool and is not part of release builds.
//
//	go run ./internal/devtools/fakessh
//
// It prints the host, port, username and password to enter in TunnelTab,
// and the remote port of the demo web app to add as a service.
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/Aerobit/TunnelTab/internal/sshx/sshtest"
)

func main() {
	web, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go http.Serve(web, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><title>Demo app</title><h1>Hello through the tunnel!</h1><p>You asked for %s</p>",
			template(r.URL.Path))
	}))

	// sshtest is written for tests; a throwaway testing.T is enough here.
	t := &testing.T{}
	srv := sshtest.Start(t, sshtest.Options{User: "demo", Password: "demo-password"})

	fmt.Println("Fake SSH server running. In TunnelTab add a server with:")
	fmt.Printf("  Host:      %s\n  SSH port:  %d\n  Username:  demo\n  Log in:    Password = demo-password\n", srv.Host, srv.Port)
	fmt.Printf("  Fingerprint to expect: %s\n", ssh.FingerprintSHA256(srv.HostKey.PublicKey()))
	fmt.Printf("Then add a service with remote port %d (a demo web page).\n", web.Addr().(*net.TCPAddr).Port)
	fmt.Println("Press Ctrl+C to stop.")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	srv.Close()
}

// template escapes text for HTML.
func template(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '<', '>', '&', '"', '\'':
			out = append(out, []rune(fmt.Sprintf("&#%d;", r))...)
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
