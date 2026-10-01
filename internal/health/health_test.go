package health

import (
	"errors"
	"strings"
	"testing"
)

// Output of Command on an Ubuntu 24.04 VPS (shortened /proc/meminfo).
const sample = `@@loadavg
0.42 0.38 0.31 1/234 5678
@@meminfo
MemTotal:        4028488 kB
MemFree:          512340 kB
MemAvailable:    1587652 kB
Buffers:          102400 kB
Cached:          1100000 kB
@@uptime
1987654.32 3456789.01
@@nproc
2
@@df
Filesystem     1024-blocks     Used Available Capacity Mounted on
tmpfs               402852     1104    401748       1% /run
/dev/sda1         81106868 22020096  59070388      28% /
tmpfs              2014244        0   2014244       0% /dev/shm
/dev/sda15          106832     6186    100646       6% /boot/efi
/dev/sdb1        960303848 421527552 538776296     44% /srv/data
/dev/loop0           65536    65536         0     100% /snap/core20/2318
overlay           81106868 22020096  59070388      28% /var/lib/docker/overlay2/abc/merged
`

func TestParse(t *testing.T) {
	h, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if h.Load1 != 0.42 || h.Load5 != 0.38 || h.Load15 != 0.31 || h.Cores != 2 {
		t.Errorf("load %+v", h)
	}
	if h.MemTotalKB != 4028488 || h.MemAvailableKB != 1587652 {
		t.Errorf("memory %d / %d", h.MemAvailableKB, h.MemTotalKB)
	}
	if h.UptimeSec != 1987654 {
		t.Errorf("uptime %d", h.UptimeSec)
	}
	var mounts []string
	for _, d := range h.Disks {
		mounts = append(mounts, d.Mount)
	}
	if strings.Join(mounts, ",") != "/,/srv/data,/boot/efi" {
		t.Errorf("disks %v", mounts)
	}
	if h.Disks[0].SizeKB != 81106868 || h.Disks[0].UsedKB != 22020096 || h.Disks[0].AvailKB != 59070388 {
		t.Errorf("root disk %+v", h.Disks[0])
	}
}

func TestParseOldKernel(t *testing.T) {
	out := strings.Replace(sample, "MemAvailable:    1587652 kB\n", "", 1)
	h, err := Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if h.MemAvailableKB != 512340+102400+1100000 {
		t.Errorf("available %d", h.MemAvailableKB)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"not Linux":      "@@loadavg\ncat: /proc/loadavg: No such file or directory\n@@meminfo\n",
		"no memory":      "@@loadavg\n0.1 0.1 0.1 1/1 1\n@@meminfo\n",
		"negative load":  strings.Replace(sample, "0.42 0.38", "-1 0.38", 1),
		"NaN load":       strings.Replace(sample, "0.42 0.38", "NaN 0.38", 1),
		"huge":           sample + strings.Repeat("x", MaxOutput),
		"one giant line": "@@loadavg\n" + strings.Repeat("9", MaxOutput+10),
	}
	for name, out := range cases {
		if _, err := Parse([]byte(out)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse([]byte(cases["not Linux"])); !errors.Is(err, ErrNotLinux) {
		t.Errorf("not Linux: %v", err)
	}
}

func TestParseTolerates(t *testing.T) {
	// Odd but harmless output: garbage df lines, no nproc, odd uptime, many disks.
	var df strings.Builder
	df.WriteString("Filesystem 1024-blocks Used Available Capacity Mounted on\n")
	for i := range 20 {
		df.WriteString("/dev/sdx" + string(rune('a'+i)) + " 1000 10 990 1% /mnt/d" + string(rune('a'+i)) + "\n")
	}
	df.WriteString("garbage line\n/dev/bad x y z 1% /bad\n/dev/sdz 100 1 99 1% /mnt/with space\n")
	out := strings.Replace(sample, "@@nproc\n2\n", "@@nproc\nlots\n", 1)
	out = strings.Replace(out, "1987654.32 3456789.01", "soon", 1)
	out = out[:strings.Index(out, "@@df")] + "@@df\n" + df.String()
	h, err := Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if h.Cores != 0 || h.UptimeSec != 0 || len(h.Disks) != MaxDisks {
		t.Errorf("got cores %d uptime %d disks %d", h.Cores, h.UptimeSec, len(h.Disks))
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(sample))
	f.Add([]byte("@@loadavg\n1 2 3\n@@meminfo\nMemTotal: 1 kB\n@@df\nh\n/dev/x 1 1 0 100% /\n"))
	f.Fuzz(func(t *testing.T, out []byte) {
		h, err := Parse(out)
		if err != nil {
			return
		}
		if h.MemAvailableKB > h.MemTotalKB || len(h.Disks) > MaxDisks || h.Load1 < 0 {
			t.Fatalf("inconsistent result %+v", h)
		}
	})
}
