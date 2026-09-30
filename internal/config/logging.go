package config

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Log rotation limits: tunneltab.log is rotated at LogMaxBytes, keeping
// LogKeep older files (tunneltab.log.1 is the newest).
const (
	LogFileName = "tunneltab.log"
	LogMaxBytes = 1 << 20 // 1 MiB
	LogKeep     = 3
)

// OpenLogger returns a structured logger writing to logDir/tunneltab.log with
// size-based rotation. Close the returned io.Closer on shutdown.
//
// Never pass secrets (passwords, keys, passphrases, the master password) to
// the logger — see the security invariants in CLAUDE.md.
func OpenLogger(logDir string, level slog.Level) (*slog.Logger, io.Closer, error) {
	w, err := newRotatingWriter(filepath.Join(logDir, LogFileName), LogMaxBytes, LogKeep)
	if err != nil {
		return nil, nil, err
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(h), w, nil
}

// rotatingWriter is an io.Writer that appends to a file and rotates it when
// it would grow beyond max bytes. Safe for concurrent use.
type rotatingWriter struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func newRotatingWriter(path string, max int64, keep int) (*rotatingWriter, error) {
	w := &rotatingWriter{path: path, max: max, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log: %w", err)
	}
	w.f, w.size = f, info.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.max {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate shifts tunneltab.log → .1 → .2 … dropping the oldest, then reopens.
func (w *rotatingWriter) rotate() error {
	w.f.Close()
	w.f = nil
	os.Remove(fmt.Sprintf("%s.%d", w.path, w.keep))
	for i := w.keep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		return fmt.Errorf("rotate log: %w", err)
	}
	return w.open()
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
