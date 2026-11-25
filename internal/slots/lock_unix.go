//go:build unix

package slots

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockPoll is how often a context-aware wait retries the non-blocking flock. A lease command holds
// the lock for one small file read and write, so the poll adds a few milliseconds at most.
const lockPoll = 5 * time.Millisecond

func lockDir(dir string) (func(), error) {
	return lockDirContext(context.Background(), dir)
}

// lockDirContext is lockDir with a way out: the wait for the lock ends with ctx. flock itself cannot
// be interrupted, so the wait is a poll of LOCK_EX|LOCK_NB. A serve's keeper renews under this lock
// while the serve runs; a lock held elsewhere (another lease command, a lease file on a network
// mount) must not keep the keeper past the run's cancellation.
func lockDirContext(ctx context.Context, dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("flock %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("flock %s: %w", path, ctx.Err())
		case <-time.After(lockPoll):
		}
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
