package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPathsFor(t *testing.T) {
	p := PathsFor(filepath.FromSlash("/x/data"))
	want := map[string]string{
		p.Vault:       "vault.enc",
		p.VaultBackup: "vault.enc.bak",
		p.Settings:    "settings.json",
		p.LogDir:      "logs",
		p.Instance:    "instance.json",
		p.Lock:        "instance.lock",
	}
	for path, name := range want {
		if filepath.Dir(path) != p.DataDir || filepath.Base(path) != name {
			t.Errorf("unexpected path %q for %s", path, name)
		}
	}
}

func TestResolveDataDirFlag(t *testing.T) {
	got, err := ResolveDataDir("relative/dir")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) || !strings.HasSuffix(filepath.ToSlash(got), "relative/dir") {
		t.Fatalf("got %q, want an absolute path ending in relative/dir", got)
	}
}

func TestResolveDataDirDefaultIsAbsolute(t *testing.T) {
	got, err := ResolveDataDir("")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) || filepath.Base(got) != DataDirName {
		t.Fatalf("got %q, want an absolute path ending in %q", got, DataDirName)
	}
}

func TestIsGoRunBinary(t *testing.T) {
	// Paths are written with forward slashes and converted to the native
	// separator, so the same cases cover Windows and Linux.
	cases := map[string]bool{
		"/tmp/go-build123/b001/exe/tunneltab":                     true,
		"C:/Users/me/AppData/Local/Temp/go-build9/b001/exe/x.exe": true,
		"/home/me/tunneltab/tunneltab-linux-amd64":                false,
		"D:/Apps/tunneltab/tunneltab.exe":                         false,
	}
	for exe, want := range cases {
		if got := isGoRunBinary(filepath.FromSlash(exe)); got != want {
			t.Errorf("isGoRunBinary(%q) = %v, want %v", exe, got, want)
		}
	}
}

func TestEnsureDataDirCreatesFolders(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, PathsFor(dir).LogDir} {
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() {
			t.Fatalf("%s was not created", d)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Errorf("%s permissions = %o, want 700", d, info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 { // just logs/
		t.Errorf("write-test file left behind: %v", entries)
	}
}

func TestEnsureDataDirNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only folders behave differently on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write to read-only folders")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })

	err := EnsureDataDir(filepath.Join(parent, "data"))
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("got %v, want ErrNotWritable", err)
	}
}

func TestSettingsMissingFileGivesDefaults(t *testing.T) {
	s, err := LoadSettings(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s != DefaultSettings() {
		t.Fatalf("got %+v, want defaults", s)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	want := Settings{Port: 50000, AutoLockMinutes: 0, CloseTunnelsOnLock: true}
	if err := SaveSettings(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSettingsPartialFileKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"port": 50001}`), 0o600)
	s, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Port != 50001 || s.AutoLockMinutes != DefaultSettings().AutoLockMinutes {
		t.Fatalf("got %+v", s)
	}
}

func TestSettingsInvalid(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"bad-json":  `{"port": `,
		"low-port":  `{"port": 80}`,
		"high-port": `{"port": 70000}`,
		"lock-neg":  `{"autoLockMinutes": -1}`,
		"lock-big":  `{"autoLockMinutes": 5000}`,
	} {
		path := filepath.Join(dir, name+".json")
		os.WriteFile(path, []byte(content), 0o600)
		s, err := LoadSettings(path)
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
		if s != DefaultSettings() {
			t.Errorf("%s: invalid file should fall back to defaults, got %+v", name, s)
		}
	}
	if err := SaveSettings(filepath.Join(dir, "x.json"), Settings{Port: 1}); err == nil {
		t.Error("SaveSettings accepted an invalid port")
	}
}

func TestLoggerWritesAndRotates(t *testing.T) {
	dir := t.TempDir()
	w, err := newRotatingWriter(filepath.Join(dir, LogFileName), 200, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 99) + "\n" // 100 bytes
	for i := 0; i < 10; i++ {
		if _, err := fmt.Fprint(w, line); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()

	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{LogFileName, LogFileName + ".1", LogFileName + ".2"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("log files = %v, want %v", names, want)
	}
	for _, n := range names {
		info, _ := os.Stat(filepath.Join(dir, n))
		if info.Size() > 200 {
			t.Errorf("%s is %d bytes, over the 200-byte limit", n, info.Size())
		}
	}
}

func TestOpenLogger(t *testing.T) {
	dir := t.TempDir()
	log, closer, err := OpenLogger(dir, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "k", "v")
	closer.Close()
	data, _ := os.ReadFile(filepath.Join(dir, LogFileName))
	if !strings.Contains(string(data), "msg=hello") {
		t.Fatalf("log content = %q", data)
	}
	if _, err := fmt.Fprint(closer.(*rotatingWriter), "x"); err == nil {
		t.Error("write after Close should fail")
	}
}

func TestIsInside(t *testing.T) {
	sep := string(filepath.Separator)
	tmp := filepath.FromSlash("/users/me/appdata/local/temp")
	cases := map[string]bool{
		filepath.Join(tmp, "Temp1_tunneltab-0.1.0.zip", "tunneltab"): true, // Explorer's "run from ZIP"
		filepath.Join(strings.ToUpper(tmp), "x"):                     true, // case-insensitive
		tmp:                                                          true,
		filepath.FromSlash("/users/me/apps/tunneltab"):               false,
		filepath.FromSlash("/users/me/appdata/local/temporary"):      false, // prefix of the name only
		sep: false,
	}
	for dir, want := range cases {
		if got := isInside(dir, tmp); got != want {
			t.Errorf("isInside(%q) = %v, want %v", dir, got, want)
		}
	}
	if isInside(tmp, "") {
		t.Error("empty parent matched")
	}
}
