package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DataDirName is the name of the data folder created next to the executable.
const DataDirName = "data"

// Paths lists every file and folder TunnelTab keeps in its data folder.
type Paths struct {
	DataDir     string // the data folder itself
	Vault       string // encrypted projects, servers, services and secrets
	VaultBackup string // previous vault, kept by every save
	KnownHosts  string // confirmed server host keys (OpenSSH format)
	Settings    string // non-secret preferences
	LogDir      string // rotated log files
}

// PathsFor returns the standard file locations inside dataDir.
func PathsFor(dataDir string) Paths {
	return Paths{
		DataDir:     dataDir,
		Vault:       filepath.Join(dataDir, "vault.enc"),
		VaultBackup: filepath.Join(dataDir, "vault.enc.bak"),
		KnownHosts:  filepath.Join(dataDir, "known_hosts"),
		Settings:    filepath.Join(dataDir, "settings.json"),
		LogDir:      filepath.Join(dataDir, "logs"),
	}
}

// ResolveDataDir decides where the data folder is.
//
//   - If flagValue (from --data) is set, that path is used.
//   - Otherwise it is "data" next to the executable (portable mode).
//   - Exception: under `go run` the executable lives in a temporary build
//     folder, so "data" in the current directory is used instead.
//
// The returned path is absolute. It is not created; see EnsureDataDir.
func ResolveDataDir(flagValue string) (string, error) {
	if flagValue != "" {
		return filepath.Abs(flagValue)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if isGoRunBinary(exe) {
		return filepath.Abs(DataDirName)
	}
	return filepath.Join(filepath.Dir(exe), DataDirName), nil
}

// isGoRunBinary reports whether exe was built by `go run` (which places the
// binary in a temporary go-build directory).
func isGoRunBinary(exe string) bool {
	return strings.Contains(filepath.ToSlash(exe), "/go-build")
}

// ErrNotWritable is returned by EnsureDataDir when the data folder can't be
// written, e.g. the portable folder is on read-only media.
var ErrNotWritable = errors.New("data folder is not writable")

// EnsureDataDir creates the data folder (and its logs folder) if needed and
// checks that files can be written there. Folders are created with owner-only
// permissions.
func EnsureDataDir(dir string) error {
	p := PathsFor(dir)
	for _, d := range []string{p.DataDir, p.LogDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrNotWritable, dir, err)
		}
	}
	probe, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrNotWritable, dir, err)
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)
	return nil
}
