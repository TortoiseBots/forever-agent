//go:build unix

package addonhost

import (
	"os"
	"syscall"
)

// openNoFollow opens path for reading without following a symlink at its last component and without blocking on
// a FIFO. The caller stats the path first (a FIFO is refused before the open), but a writer can still swap what
// the path names between that stat and this open; O_NOFOLLOW refuses a swapped-in symlink, and O_NONBLOCK makes a
// swapped-in FIFO fail at the read rather than hang the daemon.
func openNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
