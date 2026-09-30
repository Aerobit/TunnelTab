package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub answers like the releases/latest API.
func fakeGitHub(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("User-Agent") == "" {
			t.Errorf("unexpected request %s, UA %q", r.Method, r.Header.Get("User-Agent"))
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func release(tag, url string) string {
	return fmt.Sprintf(`{"tag_name":%q,"html_url":%q,"published_at":"2026-10-15T10:00:00Z","draft":false,"prerelease":false,"assets":[]}`, tag, url)
}

func TestCheck(t *testing.T) {
	page := ReleasesPage + "tag/v0.2.0"
	cases := []struct {
		current, tag  string
		newer, devBld bool
	}{
		{"0.1.0", "v0.2.0", true, false},
		{"0.1.0", "v0.1.0", false, false},
		{"0.2.0", "v0.1.9", false, false},
		{"0.9.0", "v0.10.0", true, false}, // numeric, not text, comparison
		{"1.0.0", "v0.99.99", false, false},
		{"dev", "v0.2.0", false, true},
		{"0.1.0-3-gabc123-dirty", "v0.2.0", false, true},
	}
	for _, c := range cases {
		url := fakeGitHub(t, 200, release(c.tag, page))
		r, err := Check(context.Background(), http.DefaultClient, url, c.current)
		if err != nil {
			t.Fatalf("%s vs %s: %v", c.current, c.tag, err)
		}
		if r.Newer != c.newer || r.DevBuild != c.devBld || r.Latest != strings.TrimPrefix(c.tag, "v") || r.URL != page {
			t.Errorf("%s vs %s: got %+v", c.current, c.tag, r)
		}
	}
}

func TestCheckOnlyLinksToTunnelTabReleases(t *testing.T) {
	url := fakeGitHub(t, 200, release("v9.9.9", "https://evil.example.com/tunneltab.exe"))
	r, err := Check(context.Background(), http.DefaultClient, url, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if r.URL != ReleasesPage+"latest" {
		t.Fatalf("linked to %q", r.URL)
	}
}

func TestCheckErrors(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"no release":  {404, `{"message":"Not Found"}`},
		"rate limit":  {403, `{"message":"API rate limit exceeded"}`},
		"not json":    {200, `<html>`},
		"bad tag":     {200, release("latest", ReleasesPage)},
		"prerelease":  {200, `{"tag_name":"v0.2.0","prerelease":true}`},
		"huge answer": {200, `{"tag_name":"` + strings.Repeat("9", 2<<20) + `"}`},
	}
	for name, c := range cases {
		url := fakeGitHub(t, c.status, c.body)
		_, err := Check(context.Background(), http.DefaultClient, url, "0.1.0")
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
		if name == "no release" && !errors.Is(err, ErrNoRelease) {
			t.Errorf("no release: got %v", err)
		}
	}
	if _, err := Check(context.Background(), http.DefaultClient, "http://127.0.0.1:1/", "0.1.0"); err == nil ||
		!strings.Contains(err.Error(), "can't reach GitHub") {
		t.Errorf("unreachable: got %v", err)
	}
}

func TestParse(t *testing.T) {
	good := map[string]version{"1.2.3": {1, 2, 3}, "v0.10.0": {0, 10, 0}, " v2.0.0 ": {2, 0, 0}}
	for s, want := range good {
		if v, ok := parse(s); !ok || v != want {
			t.Errorf("parse(%q) = %v %v", s, v, ok)
		}
	}
	for _, s := range []string{"", "dev", "1.2", "1.2.3.4", "1.2.x", "01.2.3", "-1.2.3", "1.2.3-rc1", "v1.2.3+build"} {
		if _, ok := parse(s); ok {
			t.Errorf("parse(%q) accepted", s)
		}
	}
}
