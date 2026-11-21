//go:build unix

package live

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockFile takes the exclusive, non-blocking flock on path's file. A
// missing parent directory is created; any failure is a busy lock.
func lockFile(path string) (*os.File, bool) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false
	}
	return f, true
}

// unlockFile drops the flock (closing the file would too; both run).
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
