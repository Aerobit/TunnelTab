// Command mkzip packs a directory into a .zip file. It is used by the build
// scripts so that packaging works the same on Windows and Linux without
// depending on an installed zip tool.
//
// Usage: go run ./scripts/mkzip <source-dir> <output.zip>
//
// The zip contains the source directory itself as its top-level folder, and
// files whose names start with "tunneltab" and have no extension (the Linux
// binaries) are marked executable.
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mkzip <source-dir> <output.zip>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "mkzip:", err)
		os.Exit(1)
	}
}

func run(srcDir, outPath string) error {
	srcDir = filepath.Clean(srcDir)
	root := filepath.Base(srcDir)

	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)

	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(root, rel))
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = name
		if d.IsDir() {
			hdr.Name += "/"
			_, err = zw.CreateHeader(hdr)
			return err
		}
		hdr.Method = zip.Deflate
		base := d.Name()
		if strings.HasPrefix(base, "tunneltab") && filepath.Ext(base) == "" {
			hdr.SetMode(0o755)
		} else {
			hdr.SetMode(0o644)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})

	if err := zw.Close(); err != nil && walkErr == nil {
		walkErr = err
	}
	if err := out.Close(); err != nil && walkErr == nil {
		walkErr = err
	}
	if walkErr != nil {
		os.Remove(outPath)
	}
	return walkErr
}
