// Package update checks GitHub for a newer TunnelTab release and installs
// it. Everything here runs only when the user asks (Settings → Check for
// updates, Update now): TunnelTab never contacts anything except your
// servers on its own. An update is installed only if its checksum file is
// signed with the release key (signature.go); see Download and Install.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultURL is GitHub's "latest release" API for TunnelTab. It ignores
// drafts and pre-releases.
const DefaultURL = "https://api.github.com/repos/Aerobit/TunnelTab/releases/latest"

// ReleasesPage is where links in the result must point.
const ReleasesPage = "https://github.com/Aerobit/TunnelTab/releases/"

const maxResponse = 1 << 20

// Result is what the dashboard shows.
type Result struct {
	Current     string `json:"current"`     // running version
	Latest      string `json:"latest"`      // newest release, e.g. "0.2.0"
	Newer       bool   `json:"newer"`       // Latest is newer than Current
	DevBuild    bool   `json:"devBuild"`    // Current isn't a release version
	CanInstall  bool   `json:"canInstall"`  // Newer, and "Update now" can install it
	URL         string `json:"url"`         // release page (download + notes)
	PublishedAt string `json:"publishedAt"` // RFC 3339

	assets map[string]string // file name → download URL (TunnelTab's own only)
}

// ErrNoRelease means GitHub has no published release yet.
var ErrNoRelease = errors.New("no release has been published yet")

// Check asks url (normally DefaultURL) for the latest release and compares
// it with current.
func Check(ctx context.Context, client *http.Client, url, current string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "TunnelTab-update-check")
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("can't reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Result{}, ErrNoRelease
	case resp.StatusCode != http.StatusOK:
		return Result{}, fmt.Errorf("GitHub answered %s; try again later", resp.Status)
	}

	var rel struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&rel); err != nil {
		return Result{}, errors.New("GitHub sent an unexpected answer")
	}
	latest, ok := parse(rel.TagName)
	if !ok || rel.Draft || rel.Prerelease {
		return Result{}, errors.New("GitHub sent an unexpected answer")
	}
	// Only ever link to TunnelTab's own release pages.
	if !strings.HasPrefix(rel.HTMLURL, ReleasesPage) {
		rel.HTMLURL = ReleasesPage + "latest"
	}

	r := Result{Current: current, Latest: latest.String(), URL: rel.HTMLURL, PublishedAt: rel.PublishedAt}
	if cur, ok := parse(current); ok {
		r.Newer = latest.newerThan(cur)
		r.assets = map[string]string{}
		prefix := downloadPrefix(url)
		for _, a := range rel.Assets {
			if strings.HasPrefix(a.URL, prefix) {
				r.assets[a.Name] = a.URL
			}
		}
		r.CanInstall = r.Newer && r.assets[ZipName(r.Latest)] != "" && r.assets[SumsName] != "" && r.assets[SigName] != ""
	} else {
		r.DevBuild = true
	}
	return r, nil
}

// version is a MAJOR.MINOR.PATCH release number.
type version [3]int

// parse accepts "1.2.3" or "v1.2.3" (a pre-release or build suffix such as
// "-rc1" or "+abc" makes it not a release version).
func parse(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var v version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 1_000_000 || (len(p) > 1 && p[0] == '0') {
			return version{}, false
		}
		v[i] = n
	}
	return v, true
}

func (v version) newerThan(o version) bool {
	for i := range v {
		if v[i] != o[i] {
			return v[i] > o[i]
		}
	}
	return false
}

func (v version) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }
