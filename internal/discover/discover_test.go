package discover

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Aerobit/TunnelTab/internal/model"
)

// Output of Command on an Ubuntu 24.04 VPS with Docker (user in the docker
// group, netstat not installed). ss shows processes only for the user's own
// programs.
const ubuntu = `@@ss
State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process
LISTEN 0      4096      127.0.0.53%lo:53         0.0.0.0:*
LISTEN 0      4096         127.0.0.1:5678        0.0.0.0:*
LISTEN 0      4096           0.0.0.0:3000        0.0.0.0:*
LISTEN 0      4096           0.0.0.0:22          0.0.0.0:*
LISTEN 0      4096         127.0.0.1:9443        0.0.0.0:*
LISTEN 0      511          127.0.0.1:41235       0.0.0.0:*    users:(("node",pid=91234,fd=19))
LISTEN 0      4096              [::]:3000           [::]:*
LISTEN 0      4096              [::]:22             [::]:*
LISTEN 0      200          10.8.0.1:8081         0.0.0.0:*    users:(("myapp",pid=812,fd=3))
LISTEN 0      4096          [::1]:631            [::]:*
@@netstat
@@docker
{"name":"n8n","image":"docker.n8n.io/n8nio/n8n:latest","ports":"127.0.0.1:5678->5678/tcp"}
{"name":"grafana","image":"grafana/grafana-oss:11.2.0","ports":"0.0.0.0:3000->3000/tcp, :::3000->3000/tcp"}
{"name":"portainer","image":"portainer/portainer-ce:2.21.0","ports":"127.0.0.1:9443->9443/tcp, 8000/tcp, 9000/tcp"}
{"name":"db","image":"postgres:16","ports":"5432/tcp"}
@@end
`

func find(t *testing.T, r Result, port int) Candidate {
	t.Helper()
	for _, c := range r.Candidates {
		if c.Port == port {
			return c
		}
	}
	t.Fatalf("port %d not found in %+v", port, r.Candidates)
	return Candidate{}
}

func TestParseUbuntuWithDocker(t *testing.T) {
	r, err := Parse([]byte(ubuntu))
	if err != nil {
		t.Fatal(err)
	}
	if r.Docker != DockerOK || r.Truncated {
		t.Errorf("docker %q truncated %v", r.Docker, r.Truncated)
	}
	want := map[int]Candidate{
		5678:  {Name: "n8n", Host: "127.0.0.1", Port: 5678, Protocol: "http", Kind: KindWeb, App: "n8n", Container: "n8n", Image: "n8nio/n8n", Listen: "local"},
		3000:  {Name: "Grafana", Host: "127.0.0.1", Port: 3000, Protocol: "http", Kind: KindWeb, App: "Grafana", Container: "grafana", Image: "grafana/grafana-oss", Listen: "all"},
		9443:  {Name: "Portainer", Host: "127.0.0.1", Port: 9443, Protocol: "https", Kind: KindWeb, App: "Portainer", Container: "portainer", Image: "portainer/portainer-ce", Listen: "local"},
		22:    {Name: "SSH", Host: "127.0.0.1", Port: 22, Protocol: "http", Kind: KindOther, App: "SSH", Listen: "all"},
		53:    {Name: "DNS", Host: "127.0.0.53", Port: 53, Protocol: "http", Kind: KindOther, App: "DNS", Listen: "local"},
		41235: {Name: "node", Host: "127.0.0.1", Port: 41235, Protocol: "http", Kind: KindMaybe, Process: "node", Listen: "local"},
		8081:  {Name: "myapp", Host: "10.8.0.1", Port: 8081, Protocol: "http", Kind: KindMaybe, Process: "myapp", Listen: "other"},
		631:   {Name: "Printers (CUPS)", Host: "::1", Port: 631, Protocol: "http", Kind: KindWeb, App: "Printers (CUPS)", Listen: "local"},
	}
	if len(r.Candidates) != len(want) {
		t.Fatalf("got %d candidates, want %d: %+v", len(r.Candidates), len(want), r.Candidates)
	}
	for port, w := range want {
		if got := find(t, r, port); got != w {
			t.Errorf("port %d:\n got %+v\nwant %+v", port, got, w)
		}
	}
	// Web apps first, then unknown, then not-web; by port within each.
	var order []int
	for _, c := range r.Candidates {
		order = append(order, c.Port)
	}
	if got, w := order, []int{631, 3000, 5678, 9443, 8081, 41235, 22, 53}; !equalInts(got, w) {
		t.Errorf("order %v, want %v", got, w)
	}
}

// One port can hold different services on different addresses; they must
// stay apart and keep their own address. Only a port that listens on every
// address, or 127.0.0.1 together with ::1, is one service.
func TestSamePortOnDifferentAddresses(t *testing.T) {
	out := "@@ss\n" +
		"LISTEN 0 1 127.0.0.1:53 0.0.0.0:*\n" +
		"LISTEN 0 1 127.0.0.53%lo:53 0.0.0.0:*\n" +
		"LISTEN 0 1 10.0.0.5:53 0.0.0.0:*\n" +
		"LISTEN 0 1 127.0.0.2:8080 0.0.0.0:* users:((\"app2\",pid=2,fd=3))\n" +
		"LISTEN 0 1 127.0.0.1:631 0.0.0.0:*\n" +
		"LISTEN 0 1 [::1]:631 [::]:*\n" +
		"LISTEN 0 1 127.0.0.1:3000 0.0.0.0:*\n" +
		"LISTEN 0 1 [::]:3000 [::]:*\n" +
		"@@docker\n" +
		`{"name":"web","image":"nginx:1","ports":"127.0.0.3:8081->80/tcp, 127.0.0.4:8081->81/tcp"}` + "\n"
	r, err := Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range r.Candidates {
		got = append(got, fmt.Sprintf("%s:%d %s %s", c.Host, c.Port, c.Listen, c.Name))
	}
	want := []string{
		"127.0.0.1:631 local Printers (CUPS)",
		"127.0.0.1:3000 all ",
		"127.0.0.2:8080 local app2",
		"127.0.0.3:8081 local nginx",
		"127.0.0.4:8081 local nginx",
		"10.0.0.5:53 other DNS",
		"127.0.0.1:53 local DNS",
		"127.0.0.53:53 local DNS",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseNetstatFallback(t *testing.T) {
	// Debian 9 without ss -p support isn't a problem; this is an older
	// server where ss is missing and net-tools' netstat answers.
	out := `@@ss
@@netstat
Active Internet connections (only servers)
Proto Recv-Q Send-Q Local Address           Foreign Address         State
tcp        0      0 0.0.0.0:22              0.0.0.0:*               LISTEN
tcp        0      0 127.0.0.1:8080          0.0.0.0:*               LISTEN
tcp6       0      0 :::80                   :::*                    LISTEN
tcp6       0      0 ::1:8123                :::*                    LISTEN
udp        0      0 0.0.0.0:68              0.0.0.0:*
@@docker
bash: docker: command not found
@@end
`
	r, err := Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if r.Docker != DockerNone || len(r.Candidates) != 4 {
		t.Fatalf("%+v", r)
	}
	if c := find(t, r, 80); c.Host != "127.0.0.1" || c.Kind != KindWeb || c.Listen != "all" || c.Name != "" {
		t.Errorf("port 80: %+v", c)
	}
	if c := find(t, r, 8080); c.Kind != KindWeb || c.Listen != "local" {
		t.Errorf("port 8080: %+v", c)
	}
	if c := find(t, r, 8123); c.Host != "::1" || c.Name != "Home Assistant" {
		t.Errorf("port 8123: %+v", c)
	}
}

func TestParseBusyBox(t *testing.T) {
	// Alpine: no ss; BusyBox netstat; no docker (ash's message).
	out := "@@ss\n@@netstat\nActive Internet connections (only servers)\n" +
		"Proto Recv-Q Send-Q Local Address           Foreign Address         State\n" +
		"tcp        0      0 0.0.0.0:22              0.0.0.0:*               LISTEN\n" +
		"tcp        0      0 127.0.0.1:3001          0.0.0.0:*               LISTEN\n" +
		"@@docker\nash: docker: not found\n@@end\n"
	r, err := Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if r.Docker != DockerNone {
		t.Errorf("docker %q", r.Docker)
	}
	if c := find(t, r, 3001); c.Name != "Uptime Kuma" || c.Kind != KindWeb {
		t.Errorf("port 3001: %+v", c)
	}
}

func TestParseDockerStates(t *testing.T) {
	ss := "@@ss\nLISTEN 0 4096 0.0.0.0:22 0.0.0.0:*\n@@docker\n"
	for msg, want := range map[string]Docker{
		"permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock: Get \"http://%2Fvar%2Frun%2Fdocker.sock/v1.47/containers/json\": dial unix /var/run/docker.sock: connect: permission denied": DockerDenied,
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?":                                                                                                                                 DockerStopped,
		"zsh: command not found: docker":      DockerNone,
		"fish: Unknown command: docker":       DockerNone,
		"something odd happened":              DockerError,
		"":                                    DockerNone,
		`{"name":"x","image":"y","ports":""}`: DockerOK,
	} {
		r, err := Parse([]byte(ss + msg + "\n@@end\n"))
		if err != nil {
			t.Fatal(err)
		}
		if r.Docker != want {
			t.Errorf("%q: docker %q, want %q", msg, r.Docker, want)
		}
	}
}

func TestParseNothing(t *testing.T) {
	for _, out := range []string{"", "@@ss\n@@netstat\n@@docker\n@@end\n", "garbage\n", "@@docker\npermission denied\n"} {
		if _, err := Parse([]byte(out)); !errors.Is(err, ErrNoPorts) {
			t.Errorf("%q: err %v", out, err)
		}
	}
	// Docker alone is enough (e.g. ss and netstat both missing).
	r, err := Parse([]byte(`@@docker` + "\n" + `{"name":"n8n","image":"n8nio/n8n","ports":"127.0.0.1:5678->5678/tcp"}` + "\n"))
	if err != nil || len(r.Candidates) != 1 || r.Candidates[0].Name != "n8n" {
		t.Errorf("%+v %v", r, err)
	}
}

func TestParseTooLarge(t *testing.T) {
	if _, err := Parse(make([]byte, MaxOutput+1)); err == nil {
		t.Error("accepted output over the limit")
	}
}

func TestParseManyPortsIsCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString("@@ss\n")
	for p := 10000; p < 10000+MaxCandidates+50; p++ {
		b.WriteString("LISTEN 0 4096 127.0.0.1:" + itoa(p) + " 0.0.0.0:*\n")
	}
	r, err := Parse([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Candidates) != MaxCandidates || !r.Truncated {
		t.Errorf("%d candidates, truncated %v", len(r.Candidates), r.Truncated)
	}
}

func itoa(n int) string {
	var buf [8]byte
	i := len(buf)
	for {
		i--
		buf[i] = byte('0' + n%10)
		if n /= 10; n == 0 {
			return string(buf[i:])
		}
	}
}

func TestHostileNamesAreCleaned(t *testing.T) {
	out := "@@ss\nLISTEN 0 1 127.0.0.1:7000 0.0.0.0:* users:((\"evil\x1b[31m\x07name\",pid=1,fd=3))\n" +
		"@@docker\n" + `{"name":"a\u0000b\u202ec` + strings.Repeat("x", 500) + `","image":"evil.example:5000/x/y:tag","ports":"127.0.0.1:7001->80/tcp"}` + "\n"
	r, err := Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if c := find(t, r, 7000); c.Name != "evil[31mname" {
		t.Errorf("process name %q", c.Name)
	}
	c := find(t, r, 7001)
	if strings.ContainsAny(c.Name, "\x00\u202e") || len([]rune(c.Name)) > maxNameLen {
		t.Errorf("container name %q", c.Name)
	}
	if c.Image != "x/y" {
		t.Errorf("image %q", c.Image)
	}
}

func TestSplitAddr(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:80":            "127.0.0.1 80",
		"0.0.0.0:80":              "<all> 80",
		"*:80":                    "<all> 80",
		"[::]:80":                 "<all> 80",
		":::80":                   "<all> 80",
		"[::1]:631":               "::1 631",
		"::1:631":                 "::1 631",
		"127.0.0.53%lo:53":        "127.0.0.53 53",
		"[::ffff:127.0.0.1]:8080": "127.0.0.1 8080",
		"10.0.0.5:8080":           "10.0.0.5 8080",
		"[fe80::1%eth0]:80":       "",
		"fe80::1:80":              "",
		"127.0.0.1:0":             "",
		"127.0.0.1:70000":         "",
		"127.0.0.1:x":             "",
		"nonsense":                "",
		"host.example:80":         "",
		"%:80":                    "",
	} {
		ip, port, ok := splitAddr(in)
		got := ""
		if ok {
			host := "<all>"
			if ip != nil {
				host = ip.String()
			}
			got = host + " " + itoa(port)
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestParsePorts(t *testing.T) {
	ps := parsePorts("0.0.0.0:8000-8002->9000-9002/tcp, [::]:53->53/udp, 5432/tcp, 0.0.0.0:1-60000->1-60000/tcp, 127.0.0.1:81->80/tcp")
	var got []string
	for _, p := range ps {
		host := "<all>"
		if p.ip != nil {
			host = p.ip.String()
		}
		got = append(got, host+" "+itoa(p.hostPort)+"->"+itoa(p.containerPort))
	}
	want := "<all> 8000->9000,<all> 8001->9001,<all> 8002->9002,127.0.0.1 81->80"
	if strings.Join(got, ",") != want {
		t.Errorf("got %v", got)
	}
}

func TestAppForImage(t *testing.T) {
	for image, want := range map[string]string{
		"grafana/grafana":               "Grafana",
		"grafana/loki":                  "",
		"vaultwarden/server":            "Vaultwarden",
		"someone/server":                "",
		"home-assistant/home-assistant": "Home Assistant",
		"linuxserver/jellyfin":          "Jellyfin",
		"postgres":                      "PostgreSQL",
		"":                              "",
	} {
		got := ""
		if a := appForImage(image); a != nil {
			got = a.name
		}
		if got != want {
			t.Errorf("%q: %q, want %q", image, got, want)
		}
	}
}

func TestShortImage(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/home-assistant/home-assistant:stable": "home-assistant/home-assistant",
		"docker.n8n.io/n8nio/n8n:latest":               "n8nio/n8n",
		"localhost:5000/app:1":                         "app",
		"localhost/app":                                "app",
		"nginx":                                        "nginx",
		"nginx@sha256:abcd":                            "nginx",
		"sha256:0123456789abcdef":                      "",
	} {
		if got := shortImage(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// Every candidate must make a valid service once it has a name, so "Add
// selected" never fails on what the scan suggested.
func checkCandidates(t *testing.T, r Result) {
	t.Helper()
	if len(r.Candidates) > MaxCandidates {
		t.Fatalf("%d candidates", len(r.Candidates))
	}
	type where struct {
		host string
		port int
	}
	seen := map[where]bool{}
	for _, c := range r.Candidates {
		if seen[where{c.Host, c.Port}] {
			t.Fatalf("%s port %d listed twice", c.Host, c.Port)
		}
		seen[where{c.Host, c.Port}] = true
		label := c.Name
		if label == "" {
			label = "x"
		}
		svc := model.Service{Label: label, RemoteHost: c.Host, RemotePort: c.Port, Protocol: model.Protocol(c.Protocol), Path: c.Path}
		if err := svc.Validate(); err != nil {
			t.Fatalf("candidate %+v makes an invalid service: %v", c, err)
		}
		if c.Kind != KindWeb && c.Kind != KindMaybe && c.Kind != KindOther {
			t.Fatalf("kind %q", c.Kind)
		}
	}
}

func TestCandidatesMakeValidServices(t *testing.T) {
	r, err := Parse([]byte(ubuntu))
	if err != nil {
		t.Fatal(err)
	}
	checkCandidates(t, r)
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(ubuntu))
	f.Add([]byte("@@ss\nLISTEN 0 1 [::1]:1 x users:((\"a\n@@docker\n{\"name\":\"\",\"image\":\"a/b:c\",\"ports\":\"1.2.3.4:1-2->3-4/tcp\"}\n"))
	f.Add([]byte("@@netstat\ntcp 0 0 :::80 :::* LISTEN\n"))
	f.Fuzz(func(t *testing.T, out []byte) {
		r, err := Parse(out)
		if err != nil {
			return
		}
		checkCandidates(t, r)
	})
}
