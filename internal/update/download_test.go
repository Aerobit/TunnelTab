package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRelease is a release server: the latest-release API at /latest and
// the release files under /dl/.
type fakeRelease struct {
	t       *testing.T
	srv     *httptest.Server
	tag     string
	files   map[string][]byte // name → contents (served under /dl/)
	listed  []string          // asset names in the API answer (default: all files)
	assetAt func(name string) string
}

func newFakeRelease(t *testing.T, tag string) *fakeRelease {
	f := &fakeRelease{t: t, tag: tag, files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRelease) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/latest" {
		type asset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}
		var assets []asset
		names := f.listed
		if names == nil {
			for n := range f.files {
				names = append(names, n)
			}
		}
		for _, n := range names {
			u := f.srv.URL + "/dl/" + n
			if f.assetAt != nil {
				u = f.assetAt(n)
			}
			assets = append(assets, asset{n, u})
		}
		json.NewEncoder(w).Encode(map[string]any{"tag_name": f.tag, "html_url": ReleasesPage + "tag/" + f.tag, "assets": assets})
		return
	}
	b, ok := f.files[strings.TrimPrefix(r.URL.Path, "/dl/")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Write(b)
}

// publish adds a signed release: the zip (files inside "tunneltab/"), its
// checksum file and the signature.
func (f *fakeRelease) publish(priv ed25519.PrivateKey, version string, inZip map[string]string) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range inZip {
		w, err := zw.Create(name)
		if err != nil {
			f.t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	zw.Close()
	zipName := ZipName(version)
	f.files[zipName] = buf.Bytes()
	sums := fmt.Sprintf("%x  %s\n", sha256.Sum256(buf.Bytes()), zipName)
	f.files[SumsName] = []byte(sums)
	f.files[SigName] = SignSums(priv, []byte(sums))
}

func (f *fakeRelease) check(current string) Result {
	f.t.Helper()
	r, err := Check(context.Background(), f.srv.Client(), f.srv.URL+"/latest", current)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func goodPackage() map[string]string {
	return map[string]string{
		"tunneltab/" + ExeName():              "new program",
		"tunneltab/README.txt":                "new readme",
		"tunneltab/data/vault.enc":            "must be ignored",
		"tunneltab/../evil":                   "must be ignored",
		"evil.exe":                            "must be ignored",
		"tunneltab/THIRD_PARTY_NOTICES.txt":   "notices",
		"tunneltab/not-a-package-file.dll":    "must be ignored",
		"tunneltab/sub/tunneltab-linux-amd64": "must be ignored",
	}
}

func TestDownloadAndInstall(t *testing.T) {
	if ExeName() == "" {
		t.Skip("no release build for this system")
	}
	pub, priv := testKey(t)
	rel := newFakeRelease(t, "v0.2.0")
	rel.publish(priv, "0.2.0", goodPackage())

	r := rel.check("0.1.0")
	if !r.Newer || !r.CanInstall {
		t.Fatalf("got %+v", r)
	}
	app := t.TempDir()
	for name, content := range map[string]string{"tunneltab": "old program", "README.txt": "old readme"} {
		os.WriteFile(filepath.Join(app, name), []byte(content), 0o755)
	}
	os.MkdirAll(filepath.Join(app, "data"), 0o700)
	os.WriteFile(filepath.Join(app, "data", "vault.enc"), []byte("my vault"), 0o600)

	dl := filepath.Join(app, ".update")
	st, err := Download(context.Background(), rel.srv.Client(), r, pub, dl, "tunneltab")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"README.txt", "THIRD_PARTY_NOTICES.txt", "tunneltab"}
	if len(st.Files) != len(want) {
		t.Fatalf("staged %v, want %v", st.Files, want)
	}
	if err := Install(st, app); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(app, name)); return string(b) }
	if read("tunneltab") != "new program" || read("tunneltab.old") != "old program" ||
		read("README.txt") != "new readme" || read("THIRD_PARTY_NOTICES.txt") != "notices" {
		t.Fatal("files not replaced as expected")
	}
	if read("data/vault.enc") != "my vault" {
		t.Fatal("the data folder was touched")
	}
	for _, name := range []string{"evil", "../evil", "evil.exe", "not-a-package-file.dll", ExeName()} {
		if name != "tunneltab" && exists(filepath.Join(app, name)) {
			t.Errorf("%s was installed", name)
		}
	}
	if !Pending(app) {
		t.Fatal("Pending = false after install")
	}

	// Rollback restores the old files.
	if err := Rollback(app); err != nil {
		t.Fatal(err)
	}
	if read("tunneltab") != "old program" || read("README.txt") != "old readme" || Pending(app) {
		t.Fatal("rollback didn't restore the old files")
	}

	// Install again, then clean up.
	st, err = Download(context.Background(), rel.srv.Client(), r, pub, dl, "tunneltab")
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(st, app); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(app, dl); err != nil {
		t.Fatal(err)
	}
	if Pending(app) || exists(dl) || read("tunneltab") != "new program" {
		t.Fatal("cleanup left files behind or removed the new program")
	}
}

func TestDownloadRejects(t *testing.T) {
	if ExeName() == "" {
		t.Skip("no release build for this system")
	}
	pub, priv := testKey(t)
	_, otherPriv := testKey(t)

	cases := map[string]func(rel *fakeRelease){
		"signed with another key": func(rel *fakeRelease) {
			rel.publish(otherPriv, "0.2.0", goodPackage())
		},
		"zip swapped after signing": func(rel *fakeRelease) {
			rel.publish(priv, "0.2.0", goodPackage())
			evil := newFakeRelease(t, "v0.2.0")
			evil.publish(otherPriv, "0.2.0", map[string]string{"tunneltab/" + ExeName(): "evil"})
			rel.files[ZipName("0.2.0")] = evil.files[ZipName("0.2.0")]
		},
		"checksums edited after signing": func(rel *fakeRelease) {
			rel.publish(priv, "0.2.0", goodPackage())
			rel.files[SumsName] = append(rel.files[SumsName], "0000000000000000000000000000000000000000000000000000000000000000  x.zip\n"...)
		},
		"old release re-labelled as new": func(rel *fakeRelease) {
			// A genuine, signed 0.1.5 served as "v0.2.0".
			rel.publish(priv, "0.1.5", goodPackage())
			rel.files[ZipName("0.2.0")] = rel.files[ZipName("0.1.5")]
		},
		"no program for this system": func(rel *fakeRelease) {
			rel.publish(priv, "0.2.0", map[string]string{"tunneltab/README.txt": "readme"})
		},
		"program outside the folder": func(rel *fakeRelease) {
			rel.publish(priv, "0.2.0", map[string]string{ExeName(): "x", "other/" + ExeName(): "x"})
		},
		"not a zip": func(rel *fakeRelease) {
			rel.publish(priv, "0.2.0", goodPackage())
			rel.files[ZipName("0.2.0")] = []byte("not a zip")
			sums := fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte("not a zip")), ZipName("0.2.0"))
			rel.files[SumsName] = []byte(sums)
			rel.files[SigName] = SignSums(priv, []byte(sums))
		},
		"missing signature file": func(rel *fakeRelease) {
			rel.publish(priv, "0.2.0", goodPackage())
			rel.listed = []string{ZipName("0.2.0"), SumsName, SigName}
			delete(rel.files, SigName)
		},
		"huge checksum file": func(rel *fakeRelease) {
			rel.publish(priv, "0.2.0", goodPackage())
			rel.files[SumsName] = bytes.Repeat([]byte("x"), maxSums+1)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			rel := newFakeRelease(t, "v0.2.0")
			setup(rel)
			r := rel.check("0.1.0")
			app := t.TempDir()
			os.WriteFile(filepath.Join(app, "tunneltab"), []byte("old program"), 0o755)
			_, err := Download(context.Background(), rel.srv.Client(), r, pub, filepath.Join(app, ".update"), "tunneltab")
			if err == nil {
				t.Fatal("accepted")
			}
			t.Log(err)
			if b, _ := os.ReadFile(filepath.Join(app, "tunneltab")); string(b) != "old program" {
				t.Fatal("the program was changed")
			}
		})
	}
}

func TestCanInstall(t *testing.T) {
	_, priv := testKey(t)
	cases := map[string]struct {
		current string
		setup   func(rel *fakeRelease)
		want    bool
	}{
		"complete release":  {"0.1.0", nil, true},
		"not newer":         {"0.2.0", nil, false},
		"dev build":         {"dev", nil, false},
		"missing signature": {"0.1.0", func(rel *fakeRelease) { delete(rel.files, SigName) }, false},
		"missing zip":       {"0.1.0", func(rel *fakeRelease) { delete(rel.files, ZipName("0.2.0")) }, false},
		"files on other host": {"0.1.0", func(rel *fakeRelease) {
			rel.assetAt = func(n string) string { return "https://evil.example.com/" + n }
		}, false},
	}
	for name, c := range cases {
		rel := newFakeRelease(t, "v0.2.0")
		rel.publish(priv, "0.2.0", goodPackage())
		if c.setup != nil {
			c.setup(rel)
		}
		if got := rel.check(c.current).CanInstall; got != c.want {
			t.Errorf("%s: CanInstall = %v, want %v", name, got, c.want)
		}
	}
	// Download refuses a result that can't be installed.
	rel := newFakeRelease(t, "v0.2.0")
	rel.publish(priv, "0.2.0", goodPackage())
	r := rel.check("0.2.0")
	if _, err := Download(context.Background(), rel.srv.Client(), r, nil, t.TempDir(), "tunneltab"); err == nil {
		t.Fatal("Download accepted a release that isn't newer")
	}
}

func TestDownloadPrefix(t *testing.T) {
	if got := downloadPrefix(DefaultURL); got != DefaultDownloadPrefix {
		t.Errorf("default: %q", got)
	}
	if got := downloadPrefix("http://127.0.0.1:1234/latest"); got != "http://127.0.0.1:1234/" {
		t.Errorf("test server: %q", got)
	}
}

func TestInstallRejectsBadNames(t *testing.T) {
	app := t.TempDir()
	staged := filepath.Join(t.TempDir(), "x")
	os.WriteFile(staged, []byte("x"), 0o644)
	for _, name := range []string{"../x", `..\x`, "a/b", "", ".."} {
		if err := Install(Staged{Files: map[string]string{name: staged}}, app); err == nil {
			t.Errorf("Install accepted %q", name)
		}
	}
	if !errors.Is(Rollback(app), nil) {
		t.Error("rollback of nothing failed")
	}
}
