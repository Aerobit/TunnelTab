// Package health reads a Linux server's load, memory, uptime and disk use
// for the opt-in "Server health" view. It defines the one command TunnelTab
// runs on a server by itself (Command) and parses its output strictly.
//
// The command is a constant: it never contains anything a user typed, only
// reads standard files and tools (/proc, nproc, df), and changes nothing.
package health

import (
	"bufio"
	"bytes"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Command prints each part after a "@@<name>" marker line.
const Command = "echo @@loadavg; cat /proc/loadavg; " +
	"echo @@meminfo; cat /proc/meminfo; " +
	"echo @@uptime; cat /proc/uptime; " +
	"echo @@nproc; nproc; " +
	"echo @@df; df -P -k"

// MaxOutput is how much output is accepted (bytes).
const MaxOutput = 64 << 10

// MaxDisks is how many disks are reported.
const MaxDisks = 5

// ErrNotLinux means the output doesn't look like a Linux server's (no /proc).
var ErrNotLinux = errors.New("server health needs a Linux server")

// Health is one reading.
type Health struct {
	Load1          float64 `json:"load1"`
	Load5          float64 `json:"load5"`
	Load15         float64 `json:"load15"`
	Cores          int     `json:"cores"`
	MemTotalKB     int64   `json:"memTotalKB"`
	MemAvailableKB int64   `json:"memAvailableKB"`
	UptimeSec      int64   `json:"uptimeSec"`
	Disks          []Disk  `json:"disks"`
}

// Disk is one mounted filesystem.
type Disk struct {
	Mount   string `json:"mount"`
	SizeKB  int64  `json:"sizeKB"`
	UsedKB  int64  `json:"usedKB"`
	AvailKB int64  `json:"availKB"`
}

// Parse reads Command's output.
func Parse(out []byte) (Health, error) {
	if len(out) > MaxOutput {
		return Health{}, errors.New("output too large")
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
		return Health{}, err
	}

	var h Health
	// /proc/loadavg: "0.42 0.38 0.31 1/234 5678"
	la := strings.Fields(first(sections["loadavg"]))
	if len(la) < 3 {
		return Health{}, ErrNotLinux
	}
	var err error
	for i, p := range []*float64{&h.Load1, &h.Load5, &h.Load15} {
		if *p, err = number(la[i], 0, 1e6); err != nil {
			return Health{}, ErrNotLinux
		}
	}

	// /proc/meminfo: "MemTotal:  4028488 kB"
	mem := map[string]int64{}
	for _, line := range sections["meminfo"] {
		key, rest, ok := strings.Cut(line, ":")
		f := strings.Fields(rest)
		if !ok || len(f) == 0 {
			continue
		}
		if v, err := strconv.ParseInt(f[0], 10, 64); err == nil && v >= 0 {
			mem[key] = v
		}
	}
	h.MemTotalKB = mem["MemTotal"]
	if h.MemTotalKB <= 0 {
		return Health{}, ErrNotLinux
	}
	if avail, ok := mem["MemAvailable"]; ok {
		h.MemAvailableKB = avail
	} else { // kernels before 3.14
		h.MemAvailableKB = mem["MemFree"] + mem["Buffers"] + mem["Cached"]
	}
	h.MemAvailableKB = min(h.MemAvailableKB, h.MemTotalKB)

	// /proc/uptime: "1987654.32 3456789.01"
	if up := strings.Fields(first(sections["uptime"])); len(up) > 0 {
		if v, err := number(up[0], 0, 1e10); err == nil {
			h.UptimeSec = int64(v)
		}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(first(sections["nproc"]))); err == nil && n > 0 && n <= 65536 {
		h.Cores = n
	}
	h.Disks = parseDisks(sections["df"])
	return h, nil
}

// parseDisks reads "df -P -k": real filesystems only (device paths, plus
// "/"), the root first, then the largest.
func parseDisks(lines []string) []Disk {
	var disks []Disk
	seen := map[string]bool{}
	for i, line := range lines {
		f := strings.Fields(line)
		if i == 0 || len(f) < 6 {
			continue // the header, or not a filesystem line
		}
		device, mount := f[0], strings.Join(f[5:], " ")
		if !strings.HasPrefix(mount, "/") || (!strings.HasPrefix(device, "/") && mount != "/") ||
			strings.HasPrefix(mount, "/snap/") || seen[mount] || len(mount) > 200 {
			continue
		}
		size, err1 := strconv.ParseInt(f[1], 10, 64)
		used, err2 := strconv.ParseInt(f[2], 10, 64)
		avail, err3 := strconv.ParseInt(f[3], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || size <= 0 || used < 0 || avail < 0 {
			continue
		}
		seen[mount] = true
		disks = append(disks, Disk{Mount: mount, SizeKB: size, UsedKB: used, AvailKB: avail})
	}
	sort.SliceStable(disks, func(i, j int) bool {
		if (disks[i].Mount == "/") != (disks[j].Mount == "/") {
			return disks[i].Mount == "/"
		}
		return disks[i].SizeKB > disks[j].SizeKB
	})
	if len(disks) > MaxDisks {
		disks = disks[:MaxDisks]
	}
	return disks
}

func first(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func number(s string, lo, hi float64) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < lo || v > hi || v != v {
		return 0, errors.New("out of range")
	}
	return v, nil
}
