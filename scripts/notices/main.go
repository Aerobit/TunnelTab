// Command notices prints the third-party license notices for everything
// shipped in TunnelTab: the Go runtime and standard library, every Go module
// compiled into the Windows or Linux executable, and the bundled xterm.js.
// The build scripts write its output to THIRD_PARTY_NOTICES.txt in the
// portable folder.
//
// Usage (from the repository root): go run ./scripts/notices
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type notice struct {
	name, version, url, licenseFile string
}

func main() {
	notices, err := collect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
	fmt.Println("TunnelTab includes the following third-party software.")
	fmt.Println("Each component's license is reproduced below.")
	for _, n := range notices {
		text, err := os.ReadFile(n.licenseFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "notices:", err)
			os.Exit(1)
		}
		fmt.Println()
		fmt.Println(strings.Repeat("=", 78))
		fmt.Printf("%s %s\n%s\n", n.name, n.version, n.url)
		fmt.Println(strings.Repeat("=", 78))
		fmt.Println()
		fmt.Println(strings.TrimSpace(string(text)))
	}
}

func collect() ([]notice, error) {
	goroot, err := output(nil, "go", "env", "GOROOT")
	if err != nil {
		return nil, err
	}
	goVersion, _ := output(nil, "go", "env", "GOVERSION")
	list := []notice{{"Go (runtime and standard library)", goVersion, "https://go.dev", filepath.Join(goroot, "LICENSE")}}

	// Modules compiled into the executable, for each release target.
	seen := map[string]notice{}
	for _, goos := range []string{"windows", "linux"} {
		out, err := output([]string{"GOOS=" + goos, "GOARCH=amd64", "CGO_ENABLED=0"}, "go", "list", "-deps",
			"-f", "{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}", "./cmd/tunneltab")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			parts := strings.Split(line, "|")
			if len(parts) != 3 {
				continue
			}
			lic, err := findLicense(parts[2])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", parts[0], err)
			}
			seen[parts[0]] = notice{parts[0], parts[1], "https://" + parts[0], lic}
		}
	}
	var mods []notice
	for _, n := range seen {
		mods = append(mods, n)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].name < mods[j].name })
	list = append(list, mods...)

	// Bundled web libraries (see web/static/vendor/xterm/README.md).
	list = append(list,
		notice{"xterm.js (@xterm/xterm)", "6.0.0", "https://github.com/xtermjs/xterm.js", "web/static/vendor/xterm/LICENSE-xterm.txt"},
		notice{"xterm.js fit addon (@xterm/addon-fit)", "0.11.0", "https://github.com/xtermjs/xterm.js", "web/static/vendor/xterm/LICENSE-addon-fit.txt"},
	)
	return list, nil
}

func findLicense(dir string) (string, error) {
	for _, name := range []string{"LICENSE", "LICENSE.txt", "LICENSE.md", "COPYING"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no license file in %s", dir)
}

func output(env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
