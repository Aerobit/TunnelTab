package config

import (
	"os"
	"path/filepath"
	"strings"
)

// RunningFromTempFolder reports whether the executable is running from the
// system's temporary folder. On Windows this usually means it was
// double-clicked inside the ZIP without extracting it: Explorer then runs a
// temporary copy, and the "data" folder next to it (with the vault) would
// be deleted later. Builds made by `go run` are not flagged.
func RunningFromTempFolder() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return !isGoRunBinary(exe) && isInside(filepath.Dir(exe), os.TempDir())
}

// isInside reports whether dir is parent or below it (case-insensitively,
// as Windows paths are).
func isInside(dir, parent string) bool {
	if parent == "" {
		return false
	}
	rel, err := filepath.Rel(strings.ToLower(filepath.Clean(parent)), strings.ToLower(filepath.Clean(dir)))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
