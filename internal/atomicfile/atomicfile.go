// Package atomicfile writes files so that readers (and crashes) only ever see
// the complete old contents or the complete new contents, never a mix.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// WriteFile writes data to a temporary file in the same directory as path,
// flushes it to disk, and renames it over path. perm is applied to the new
// file (on Windows only the read-only bit is meaningful).
func WriteFile(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if err = tmp.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("set permissions: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("flush temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	syncDir(dir)
	return nil
}

// syncDir flushes the directory entry so the rename survives a power loss.
// Best effort: not supported on Windows, and failure here is not fatal.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
