// Package discover finds the web apps a server runs, for "Find services".
// It defines the read-only command TunnelTab runs on a server when the user
// clicks Find services (Command) and parses its output strictly into
// candidates the user can tick and add.
//
// The command is a constant: it never contains anything a user typed, only
// lists listening TCP ports (ss, or netstat where ss is missing) and Docker
// containers, and changes nothing. Everything it prints comes from the
// server and is treated as untrusted: names are cleaned, addresses parsed,
// sizes capped.
package discover

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Command prints each part after a "@@<name>" marker line. It uses only ";"
// so it runs the same in sh, bash, zsh and fish. Both ss and netstat run;
// the parser prefers ss. docker's errors go to the output (2>&1) so the
// parser can tell "no Docker" from "no permission to use Docker".
const Command = "echo @@ss; ss -tlnp; " +
	"echo @@netstat; netstat -tln; " +
	"echo @@docker; docker ps --format " + dockerFormat + " 2>&1; " +
	"echo @@end"

// dockerFormat asks docker for only the three fields used, one JSON object
// per container.
const dockerFormat = `'{"name":{{json .Names}},"image":{{json .Image}},"ports":{{json .Ports}}}'`

// MaxOutput is how much output is accepted (bytes).
const MaxOutput = 256 << 10

// MaxCandidates is how many candidates are reported.
const MaxCandidates = 200

// maxNameLen is the longest suggested name (model.MaxNameLen is 100).
const maxNameLen = 60

// ErrNoPorts means neither ss nor netstat printed anything usable.
var ErrNoPorts = errors.New("couldn't list the server's open ports (ss and netstat are missing)")

// Kind says how likely a candidate is to be a web page.
type Kind string

const (
	KindWeb   Kind = "web"   // a known web app or a usual web port
	KindMaybe Kind = "maybe" // unknown: may or may not be a web page
	KindOther Kind = "other" // known not to be a web page (SSH, databases, …)
)

// Docker says whether Docker containers could be listed.
type Docker string

const (
	DockerNone    Docker = "none"    // docker isn't installed
	DockerOK      Docker = "ok"      // containers listed
	DockerDenied  Docker = "denied"  // installed, but this user may not use it
	DockerStopped Docker = "stopped" // installed, but the daemon isn't running
	DockerError   Docker = "error"   // anything else
)

// Result is one scan.
type Result struct {
	Candidates []Candidate `json:"candidates"`
	Docker     Docker      `json:"docker"`
	Truncated  bool        `json:"truncated,omitempty"` // more than MaxCandidates were found
}

// Candidate is one port that could become a service. Name, Host, Port,
// Protocol and Path are suggestions for the service; the rest says where
// it was found.
type Candidate struct {
	Name      string `json:"name"` // suggested label; "" when nothing names it
	Host      string `json:"host"` // address to tunnel to, as seen from the server
	Port      int    `json:"port"`
	Protocol  string `json:"protocol"` // "http" or "https"
	Path      string `json:"path,omitempty"`
	Kind      Kind   `json:"kind"`
	App       string `json:"app,omitempty"`       // recognised app, e.g. "Grafana"
	Process   string `json:"process,omitempty"`   // listening program, when ss could see it
	Container string `json:"container,omitempty"` // Docker container name
	Image     string `json:"image,omitempty"`     // Docker image, without registry and tag
	Listen    string `json:"listen"`              // "local" (loopback), "all" (every address) or "other"
}

// listener is one listening socket from ss or netstat.
type listener struct {
	ip      net.IP // nil = every address
	port    int
	process string
}

// container is one docker ps line.
type container struct {
	name, image string
	ports       []published
}

// published is one "host:port->port/tcp" mapping.
type published struct {
	ip            net.IP // nil = every address
	hostPort      int
	containerPort int
}

// Parse reads Command's output.
func Parse(out []byte) (Result, error) {
	if len(out) > MaxOutput {
		return Result{}, errors.New("output too large")
	}
	sections := map[string][]string{}
	var current string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 4096), MaxOutput)
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "@@"); ok {
			current = name
			continue
		}
		if current != "" {
			sections[current] = append(sections[current], line)
		}
	}
	if err := sc.Err(); err != nil {
		return Result{}, err
	}

	listeners := parseSS(sections["ss"])
	if len(listeners) == 0 {
		listeners = parseNetstat(sections["netstat"])
	}
	containers, docker := parseDocker(sections["docker"])
	if len(listeners) == 0 && len(containers) == 0 {
		return Result{Docker: docker}, ErrNoPorts
	}
	res := merge(listeners, containers)
	res.Docker = docker
	return res, nil
}

// parseSS reads "ss -tlnp":
//
//	State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process
//	LISTEN 0      4096   127.0.0.1:5678     0.0.0.0:*         users:(("node",pid=812,fd=20))
func parseSS(lines []string) []listener {
	var ls []listener
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "LISTEN" {
			continue
		}
		ip, port, ok := splitAddr(f[3])
		if !ok {
			continue
		}
		l := listener{ip: ip, port: port}
		if len(f) > 5 {
			l.process = ssProcess(strings.Join(f[5:], " "))
		}
		ls = append(ls, l)
	}
	return ls
}

// ssProcess takes the first program name from `users:(("nginx",pid=1,fd=6),…)`.
func ssProcess(s string) string {
	_, rest, ok := strings.Cut(s, `(("`)
	if !ok {
		return ""
	}
	name, _, ok := strings.Cut(rest, `"`)
	if !ok {
		return ""
	}
	return cleanName(name)
}

// parseNetstat reads "netstat -tln" (GNU net-tools and BusyBox):
//
//	Proto Recv-Q Send-Q Local Address  Foreign Address State
//	tcp        0      0 0.0.0.0:22     0.0.0.0:*       LISTEN
//	tcp6       0      0 :::80          :::*            LISTEN
func parseNetstat(lines []string) []listener {
	var ls []listener
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 6 || (f[0] != "tcp" && f[0] != "tcp6") || f[len(f)-1] != "LISTEN" {
			continue
		}
		if ip, port, ok := splitAddr(f[3]); ok {
			ls = append(ls, listener{ip: ip, port: port})
		}
	}
	return ls
}

// splitAddr parses a listening address as ss, netstat and docker print it:
// "127.0.0.1:80", "0.0.0.0:80", "*:80", "[::]:80", ":::80", "[::1]:631",
// "127.0.0.53%lo:53". The IP is nil for "every address". Addresses that
// can't be tunnelled to (link-local, with a zone) are refused.
func splitAddr(s string) (net.IP, int, bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return nil, 0, false
	}
	host, portStr := s[:i], s[i+1:]
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, 0, false
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if h, zone, ok := strings.Cut(host, "%"); ok {
		if zone == "" {
			return nil, 0, false
		}
		host = h
	}
	if host == "*" || host == "" || host == "::" || host == "0.0.0.0" {
		return nil, port, true
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return nil, 0, false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip, port, true
}

// parseDocker reads the docker section: one JSON object per container, or
// an error message.
func parseDocker(lines []string) ([]container, Docker) {
	var cs []container
	state := DockerNone
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "{") {
			var raw struct{ Name, Image, Ports string }
			if json.Unmarshal([]byte(line), &raw) != nil {
				continue
			}
			state = DockerOK
			cs = append(cs, container{
				name:  cleanName(firstName(raw.Name)),
				image: shortImage(raw.Image),
				ports: parsePorts(raw.Ports),
			})
			continue
		}
		if state == DockerOK {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(lower, "not found") || strings.Contains(lower, "unknown command"):
			state = DockerNone
		case strings.Contains(lower, "permission denied"):
			state = DockerDenied
		case strings.Contains(lower, "cannot connect to the docker daemon") ||
			strings.Contains(lower, "is the docker daemon running"):
			state = DockerStopped
		default:
			state = DockerError
		}
	}
	return cs, state
}

// firstName: docker lists several names comma-separated.
func firstName(s string) string {
	name, _, _ := strings.Cut(s, ",")
	return name
}

// shortImage strips the registry, digest and tag:
// "ghcr.io/home-assistant/home-assistant:stable" → "home-assistant/home-assistant".
func shortImage(s string) string {
	s, _, _ = strings.Cut(s, "@")
	if i := strings.LastIndexByte(s, ':'); i > strings.LastIndexByte(s, '/') {
		s = s[:i]
	}
	if first, rest, ok := strings.Cut(s, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		s = rest
	}
	if strings.HasPrefix(s, "sha256") || len(s) > 200 {
		return ""
	}
	return cleanName(s)
}

// maxRange is the longest published port range expanded ("8000-8010->…").
const maxRange = 16

// parsePorts reads docker's Ports column:
// "0.0.0.0:3000->3000/tcp, :::3000->3000/tcp, 5432/tcp". Unpublished ports
// ("5432/tcp") and UDP are skipped.
func parsePorts(s string) []published {
	var ps []published
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		hostSide, ctrSide, ok := strings.Cut(part, "->")
		if !ok || !strings.HasSuffix(ctrSide, "/tcp") {
			continue
		}
		ctrSide = strings.TrimSuffix(ctrSide, "/tcp")
		i := strings.LastIndexByte(hostSide, ':')
		if i < 0 {
			continue
		}
		hostLo, hostHi, ok1 := portRange(hostSide[i+1:])
		ctrLo, ctrHi, ok2 := portRange(ctrSide)
		if !ok1 || !ok2 || hostHi-hostLo != ctrHi-ctrLo || hostHi-hostLo >= maxRange {
			continue
		}
		ip, _, ok := splitAddr(hostSide[:i] + ":1")
		if !ok {
			continue
		}
		for d := 0; d <= hostHi-hostLo; d++ {
			ps = append(ps, published{ip: ip, hostPort: hostLo + d, containerPort: ctrLo + d})
		}
	}
	return ps
}

func portRange(s string) (lo, hi int, ok bool) {
	a, b, isRange := strings.Cut(s, "-")
	lo, err := strconv.Atoi(a)
	if err != nil || lo < 1 || lo > 65535 {
		return 0, 0, false
	}
	hi = lo
	if isRange {
		if hi, err = strconv.Atoi(b); err != nil || hi < lo || hi > 65535 {
			return 0, 0, false
		}
	}
	return lo, hi, true
}

// cleanName keeps a name from the server safe to show and to use as a
// service label: printable characters only, trimmed and capped.
func cleanName(s string) string {
	if !utf8.ValidString(s) {
		return ""
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if !unicode.IsPrint(r) {
			continue
		}
		if n == maxNameLen {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// entry collects what was found on one port.
type entry struct {
	port      int
	ips       []net.IP // listening addresses; nil entry = every address
	process   string
	container *container
	ctrPort   int
}

// merge joins listening sockets and published container ports into one
// candidate per port.
func merge(ls []listener, cs []container) Result {
	byPort := map[int]*entry{}
	get := func(port int) *entry {
		e := byPort[port]
		if e == nil {
			e = &entry{port: port}
			byPort[port] = e
		}
		return e
	}
	for _, l := range ls {
		e := get(l.port)
		e.ips = append(e.ips, l.ip)
		if e.process == "" && l.process != "docker-proxy" {
			e.process = l.process
		}
	}
	for i := range cs {
		for _, p := range cs[i].ports {
			e := get(p.hostPort)
			if e.container == nil {
				e.container, e.ctrPort = &cs[i], p.containerPort
			}
			e.ips = append(e.ips, p.ip)
		}
	}

	var res Result
	for _, e := range byPort {
		res.Candidates = append(res.Candidates, e.candidate())
	}
	rank := map[Kind]int{KindWeb: 0, KindMaybe: 1, KindOther: 2}
	sort.Slice(res.Candidates, func(i, j int) bool {
		a, b := res.Candidates[i], res.Candidates[j]
		if rank[a.Kind] != rank[b.Kind] {
			return rank[a.Kind] < rank[b.Kind]
		}
		return a.Port < b.Port
	})
	if len(res.Candidates) > MaxCandidates {
		res.Candidates, res.Truncated = res.Candidates[:MaxCandidates], true
	}
	return res
}

// candidate turns what was found on a port into a suggestion.
func (e *entry) candidate() Candidate {
	c := Candidate{Port: e.port, Protocol: "http", Kind: KindMaybe, Process: e.process}
	c.Host, c.Listen = pickHost(e.ips)

	var known *app
	var ep endpoint
	var matched bool
	if e.container != nil {
		c.Container, c.Image = e.container.name, e.container.image
		if known = appForImage(e.container.image); known != nil {
			ep, matched = known.ports[e.ctrPort]
		}
	}
	if known == nil {
		if a, ok := portApps[e.port]; ok {
			known, ep, matched = a, a.ports[e.port], true
		}
	}
	switch {
	case known != nil && known.notWeb:
		c.App, c.Kind = known.name, KindOther
	case known != nil && matched:
		c.App, c.Kind, c.Protocol, c.Path = known.name, KindWeb, ep.protocol, ep.path
	case known != nil:
		c.App = known.name // a known app's other port (e.g. Portainer's agent port)
	}
	if c.Kind == KindMaybe {
		if p, ok := webPorts[e.port]; ok {
			c.Kind, c.Protocol = KindWeb, p
		}
	}

	switch {
	case c.App != "":
		c.Name = c.App
	case c.Container != "":
		c.Name = c.Container
	default:
		c.Name = c.Process
	}
	return c
}

// pickHost chooses the address to tunnel to: 127.0.0.1 when the port
// listens on loopback or on every address, otherwise the address it
// listens on.
func pickHost(ips []net.IP) (host, listen string) {
	var loop6, other net.IP
	for _, ip := range ips {
		switch {
		case ip == nil:
			return "127.0.0.1", "all"
		case ip.To4() != nil && ip.IsLoopback():
			host, listen = "127.0.0.1", "local"
		case ip.IsLoopback():
			loop6 = ip
		case other == nil:
			other = ip
		}
	}
	switch {
	case host != "":
		return host, listen
	case loop6 != nil:
		return loop6.String(), "local"
	case other != nil:
		return other.String(), "other"
	}
	return "127.0.0.1", "all"
}
