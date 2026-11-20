//go:build !unix

package live

import (
	"os"
	"path/filepath"
	"time"
)

// lockFile on a platform without flock: a lock directory beside the file,
// removed by the owning process. A crash leaves the directory behind, so a
// holder older than six hours is taken over (the flock rule cannot be had
// here; the platform is not the bench's).
func lockFile(path string) (*os.File, bool) {
	dir := path + ".l"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		if st, serr := os.Stat(dir); serr == nil && time.Since(st.ModTime()) > 6*time.Hour {
			_ = os.RemoveAll(dir)
			if err := os.Mkdir(dir, 0o700); err != nil {
				return nil, false
			}
		} else {
			return nil, false
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, false
	}
	return f, true
}

func unlockFile(f *os.File) error {
	_ = os.RemoveAll(f.Name() + ".l")
	return nil
}
