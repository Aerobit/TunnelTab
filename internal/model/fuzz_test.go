package model

import (
	"strings"
	"testing"
	"unicode"
)

// FuzzServerValidate checks that anything the validator accepts is safe to
// hand to SSH and to show in the dashboard, and that it never panics.
func FuzzServerValidate(f *testing.F) {
	for _, seed := range [][3]string{
		{"web", "vps.example.com", "root"},
		{"x", "10.0.0.1", "deploy_bot"},
		{"x", "::1", "me@corp"},
		{"x", "-oProxyCommand=sh", "-l"},
		{"x", "a b", "a\nb"},
		{"\x00", "exämple.com", "üser"},
	} {
		f.Add(seed[0], seed[1], seed[2], 22)
	}
	f.Fuzz(func(t *testing.T, name, host, user string, port int) {
		s := Server{Name: name, Host: host, Port: port, Username: user, Auth: Auth{Type: AuthAgent}}
		if s.Validate() != nil {
			return
		}
		for _, v := range []string{host, user} {
			if v == "" || v[0] == '-' || strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r > unicode.MaxASCII }) {
				t.Fatalf("accepted unsafe value %q", v)
			}
		}
		if strings.ContainsFunc(name, unicode.IsControl) {
			t.Fatalf("accepted name with control characters %q", name)
		}
		if port < 1 || port > 65535 {
			t.Fatalf("accepted port %d", port)
		}
	})
}

// FuzzServiceValidate checks accepted paths can't change the URL's host or
// scheme when appended to http://127.0.0.1:<port>.
func FuzzServiceValidate(f *testing.F) {
	for _, seed := range []string{"", "/", "/admin?x=1#top", "//evil.com", "/a b", `/a\b`, "javascript:alert(1)", "/é"} {
		f.Add(seed, "127.0.0.1", 80)
	}
	f.Fuzz(func(t *testing.T, path, remoteHost string, port int) {
		s := Service{Label: "x", RemoteHost: remoteHost, RemotePort: port, Protocol: HTTP, Path: path}
		if s.Validate() != nil {
			return
		}
		if path != "" && path[0] != '/' {
			t.Fatalf("accepted path not starting with /: %q", path)
		}
		for i := 0; i < len(path); i++ {
			if c := path[i]; c <= ' ' || c >= 0x7f || c == '\\' {
				t.Fatalf("accepted path byte %q in %q", c, path)
			}
		}
	})
}
