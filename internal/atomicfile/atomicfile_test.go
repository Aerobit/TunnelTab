package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")

	if err := WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("got %q, want %q", got, "second")
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestWriteFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions only")
	}
	path := filepath.Join(t.TempDir(), "secret")
	if err := WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permissions = %o, want 600", perm)
	}
}

func TestWriteFileMissingDirFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "f")
	if err := WriteFile(path, []byte("x"), 0o600); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}
