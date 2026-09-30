package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRestrictToOwnerUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("see perm_windows_test.go")
	}
	dir := filepath.Join(t.TempDir(), "data")
	os.Mkdir(dir, 0o755)
	if err := RestrictToOwner(dir); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("permissions %o, want 700", info.Mode().Perm())
	}
}
