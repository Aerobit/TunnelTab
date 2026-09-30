//go:build !windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
)

// OpenBrowser opens url in the default browser with xdg-open. The URL is
// passed as a single argument (no shell), so it can't inject commands.
func OpenBrowser(url string) error {
	return exec.Command("xdg-open", url).Start()
}

// ShowError reports a fatal startup problem on stderr.
func ShowError(title, msg string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, msg)
}
