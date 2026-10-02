package platform

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLockDataDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	first, err := LockDataDir(path)
	if err != nil {
		t.Fatal(err)
	}
	// A second open of the file is refused, as it would be in another process.
	if second, err := LockDataDir(path); !errors.Is(err, ErrLocked) {
		second.Close()
		t.Fatalf("second lock: %v, want ErrLocked", err)
	}
	first.Close()
	first.Close() // twice is fine

	again, err := LockDataDir(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again.Close()

	var none *DataDirLock
	none.Close()
}
