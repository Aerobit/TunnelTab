package platform

import (
	"errors"
	"net/url"
	"strconv"
)

// ErrNotDashboardURL is returned by OpenBrowser for anything but a link to
// the dashboard on this computer.
var ErrNotDashboardURL = errors.New("not a link to the local dashboard")

// checkDashboardURL accepts only http://127.0.0.1:<port>/... links. On
// Windows the link is handed to ShellExecute, which would also run a file
// path or any other kind of link, so nothing else may get that far.
func checkDashboardURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" {
		return ErrNotDashboardURL
	}
	if port, err := strconv.Atoi(u.Port()); err != nil || port < 1 || port > 65535 {
		return ErrNotDashboardURL
	}
	return nil
}
