// Command tunneltab is the TunnelTab executable: a portable SSH terminal and
// tunnel manager whose dashboard runs in the user's own browser.
//
// See docs/ARCHITECTURE.md for how the pieces fit together.
package main

import (
	"flag"
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=<version>".
// Builds made with plain `go build` / `go run` report "dev".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	dataDir := flag.String("data", "", "data folder (default: \"data\" next to the executable)")
	flag.Parse()

	if *showVersion {
		fmt.Println("TunnelTab", version)
		return
	}

	// Phase 0 scaffold: the application itself is built in later phases (see PLAN.md).
	_ = dataDir
	fmt.Fprintln(os.Stderr, "TunnelTab", version, "- not implemented yet (Phase 0 scaffold)")
	os.Exit(1)
}
