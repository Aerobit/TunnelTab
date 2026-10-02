package platform

import (
	"errors"
	"os"
)

// ErrLocked is returned by LockDataDir when another process holds the lock.
var ErrLocked = errors.New("data folder is in use by another TunnelTab")

// DataDirLock keeps other TunnelTab processes from using the same data
// folder. The operating system releases it when the process exits, even
// after a crash, so a leftover lock file never blocks a start.
type DataDirLock struct {
	f *os.File
}

// LockDataDir takes the lock file at path without waiting. It returns
// ErrLocked if another process holds it; other errors mean the file system
// can't lock files (e.g. some network shares).
func LockDataDir(path string) (*DataDirLock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return &DataDirLock{f: f}, nil
}

// Close releases the lock. It may be called more than once, and on nil.
// The lock file itself stays: deleting it would let a second process lock
// a new file while the first still holds the old one.
func (l *DataDirLock) Close() {
	if l == nil || l.f == nil {
		return
	}
	l.f.Close() // closing the file releases the lock
	l.f = nil
}
