package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/Aerobit/TunnelTab/internal/atomicfile"
)

// oldSuffix marks a replaced file, kept until the new version has started.
const oldSuffix = ".old"

// replacedList (in the app folder) lists the files an update replaced, one
// name per line, so Rollback and Cleanup find them whatever they are called
// (the program may have been renamed).
const replacedList = ".update-replaced"

// Install moves the staged files into appDir (the folder with the running
// program). Each file it replaces is first renamed to <name>.old: a running
// program can be renamed even on Windows, where it can't be overwritten.
// If anything fails, the old files are put back. The names replaced are
// recorded in appDir first, for Rollback and Cleanup.
//
// Afterwards, start the new program; once it runs, it calls Cleanup. If it
// doesn't start, call Rollback.
func Install(st Staged, appDir string) error {
	names := make([]string, 0, len(st.Files))
	for name := range st.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.ContainsAny(name, `/\`) || name == "" || name == "." || name == ".." {
			return fmt.Errorf("bad file name %q", name)
		}
	}
	list := strings.Join(names, "\n") + "\n"
	if err := atomicfile.WriteFile(filepath.Join(appDir, replacedList), []byte(list), 0o644); err != nil {
		return fmt.Errorf("can't record the files to replace: %w", err)
	}
	for _, name := range names {
		target := filepath.Join(appDir, name)
		if err := replace(st.Files[name], target); err != nil {
			rbErr := Rollback(appDir)
			return errors.Join(fmt.Errorf("can't replace %s: %w", name, err), rbErr)
		}
	}
	os.RemoveAll(st.Dir)
	return nil
}

func replace(staged, target string) error {
	old := target + oldSuffix
	if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err // e.g. a previous update's program is still running
	}
	if err := os.Rename(target, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		mode := os.FileMode(0o644)
		if !strings.HasSuffix(target, ".txt") {
			mode = 0o755
		}
		return os.Chmod(target, mode)
	}
	return nil
}

// Rollback puts back every <name>.old in appDir, undoing Install.
func Rollback(appDir string) error {
	var errs []error
	for _, old := range oldFiles(appDir) {
		target := strings.TrimSuffix(old, oldSuffix)
		if err := os.Rename(old, target); err != nil {
			errs = append(errs, fmt.Errorf("can't restore %s: %w", filepath.Base(target), err))
		}
	}
	if len(errs) == 0 {
		removeList(appDir)
	}
	return errors.Join(errs...)
}

// Cleanup removes what an update left behind: the replaced files and the
// download folder. On Windows the old program can't be deleted while it is
// still running, so call it again later if it fails.
func Cleanup(appDir, downloadDir string) error {
	var errs []error
	for _, old := range oldFiles(appDir) {
		if err := os.Remove(old); err != nil {
			errs = append(errs, err)
		}
	}
	if err := os.RemoveAll(downloadDir); err != nil {
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		removeList(appDir)
	}
	return errors.Join(errs...)
}

func removeList(appDir string) {
	os.Remove(filepath.Join(appDir, replacedList))
}

// Pending reports whether an update left files behind in appDir.
func Pending(appDir string) bool { return len(oldFiles(appDir)) > 0 }

// oldFiles lists the <name>.old files in appDir of the files the update
// replaced: those in the list Install wrote, and (for an update installed
// by a version that wrote no list) package files and "tunneltab*" names.
func oldFiles(appDir string) []string {
	entries, err := os.ReadDir(appDir)
	if err != nil {
		return nil
	}
	replaced := map[string]bool{}
	if list, err := os.ReadFile(filepath.Join(appDir, replacedList)); err == nil {
		for _, name := range strings.Split(string(list), "\n") {
			replaced[strings.TrimSpace(name)] = true
		}
	}
	var out []string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), oldSuffix)
		if !ok || name == "" || !e.Type().IsRegular() {
			continue
		}
		if replaced[name] || strings.HasPrefix(name, "tunneltab") || slices.Contains(PackageFiles, name) {
			out = append(out, filepath.Join(appDir, e.Name()))
		}
	}
	return out
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
