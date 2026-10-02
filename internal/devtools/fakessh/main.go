// Command fakessh runs a local test SSH server with a small demo web app
// behind it, so the dashboard can be tried without a real VPS. It is a
// development tool and is not part of release builds.
//
//	go run ./internal/devtools/fakessh              # random port
//	go run ./internal/devtools/fakessh -port 2222   # fixed port
//	go run ./internal/devtools/fakessh -demo        # realistic prompt and canned
//	                                                 # command output (README screenshots)
//
// It prints the host, port, username and password to enter in TunnelTab,
// and the remote port of the demo web app to add as a service.
//
// To try what happens when the connection drops, type a command and Enter:
// "drop" cuts every SSH connection (a short blip), "down" also refuses new
// ones (the server is unreachable) until "up".
package main

import (
	"bufio"
	"flag"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/Aerobit/TunnelTab/internal/discover"
	"github.com/Aerobit/TunnelTab/internal/health"
	"github.com/Aerobit/TunnelTab/internal/sshx/sshtest"
)

func main() {
	port := flag.Int("port", 0, "SSH port on 127.0.0.1 (0 = random)")
	demo := flag.Bool("demo", false, "realistic prompt and canned command output, for screenshots")
	flag.Parse()

	web, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go http.Serve(web, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><title>Demo app</title><h1>Hello through the tunnel!</h1><p>You asked for %s</p>",
			html.EscapeString(r.URL.Path))
	}))

	opts := sshtest.Options{User: "demo", Password: "demo-password", Addr: fmt.Sprintf("127.0.0.1:%d", *port)}
	// Answers the opt-in server health check like a small Ubuntu VPS, and
	// "Find services" with the demo web app (a real tunnel target) plus a
	// few Docker containers.
	opts.Exec = map[string]string{
		health.Command:   demoHealth,
		discover.Command: fmt.Sprintf(demoDiscover, web.Addr().(*net.TCPAddr).Port),
	}
	if *demo {
		opts.Banner = demoBanner
		opts.Prompt = "\x1b[1;32mdemo@homelab\x1b[0m:\x1b[1;34m~\x1b[0m$ "
		opts.Commands = demoCommands
	}

	// sshtest is written for tests; a throwaway testing.T is enough here.
	srv := sshtest.Start(&testing.T{}, opts)

	fmt.Println("Fake SSH server running. In TunnelTab add a server with:")
	fmt.Printf("  Host:      %s\n  SSH port:  %d\n  Username:  demo\n  Log in:    Password = demo-password\n", srv.Host, srv.Port)
	fmt.Printf("  Fingerprint to expect: %s\n", ssh.FingerprintSHA256(srv.HostKey.PublicKey()))
	fmt.Printf("Then add a service with remote port %d (a demo web page).\n", web.Addr().(*net.TCPAddr).Port)
	fmt.Println(`Type "drop", "down" or "up" and Enter to cut or refuse connections.`)
	fmt.Println("Press Ctrl+C to stop.")

	go func() {
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			switch strings.TrimSpace(in.Text()) {
			case "drop":
				srv.DropConnections()
				fmt.Println("Dropped all connections.")
			case "down":
				srv.SetRefusing(true)
				srv.DropConnections()
				fmt.Println("Down: dropped all connections, refusing new ones.")
			case "up":
				srv.SetRefusing(false)
				fmt.Println("Up: accepting connections again.")
			}
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	srv.Close()
}

// Demo mode content: made up, for screenshots only.
const demoBanner = `Linux homelab 6.8.0-45-generic x86_64

Last login: Mon Sep 29 21:14:03 2026 from 10.0.0.12`

var demoCommands = map[string]string{
	"uptime": " 09:41:07 up 23 days,  4:12,  1 user,  load average: 0.08, 0.11, 0.09\n",
	"df -h": `Filesystem      Size  Used Avail Use% Mounted on
/dev/sda1        78G   21G   54G  28% /
/dev/sdb1       916G  402G  468G  47% /srv/data
`,
	"docker ps": `NAMES       STATUS        PORTS
n8n         Up 6 days     127.0.0.1:5678->5678/tcp
grafana     Up 23 days    127.0.0.1:3000->3000/tcp
postgres    Up 23 days    5432/tcp
`,
}

// demoHealth is what health.Command prints on a small Ubuntu VPS.
const demoHealth = `@@loadavg
0.42 0.38 0.31 1/234 5678
@@meminfo
MemTotal:        4028488 kB
MemFree:          512340 kB
MemAvailable:    1587652 kB
@@uptime
1987654.32 3456789.01
@@nproc
2
@@df
Filesystem     1024-blocks      Used Available Capacity Mounted on
tmpfs               402852      1104    401748       1% /run
/dev/sda1         81106868  22020096  59070388      28% /
/dev/sdb1        960303848 421527552 538776296      44% /srv/data
`

// demoDiscover is what discover.Command prints on a homelab VPS; %d is the
// demo web app's port.
const demoDiscover = `@@ss
State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process
LISTEN 0      4096     127.0.0.53%%lo:53         0.0.0.0:*
LISTEN 0      4096         127.0.0.1:5678        0.0.0.0:*
LISTEN 0      4096         127.0.0.1:3000        0.0.0.0:*
LISTEN 0      4096         127.0.0.1:9443        0.0.0.0:*
LISTEN 0      4096           0.0.0.0:22          0.0.0.0:*
LISTEN 0      511          127.0.0.1:%d       0.0.0.0:*    users:(("demo-app",pid=4242,fd=3))
@@netstat
@@docker
{"name":"n8n","image":"docker.n8n.io/n8nio/n8n:latest","ports":"127.0.0.1:5678->5678/tcp"}
{"name":"grafana","image":"grafana/grafana-oss:11.2.0","ports":"127.0.0.1:3000->3000/tcp"}
{"name":"portainer","image":"portainer/portainer-ce:2.21.0","ports":"127.0.0.1:9443->9443/tcp, 8000/tcp"}
{"name":"postgres","image":"postgres:16","ports":"127.0.0.1:5432->5432/tcp"}
@@end
`
