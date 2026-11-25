//go:build unix

package slots

// The store's lock wait ends with its context. A long-lived holder (a serve's lease keeper) renews
// under that lock, and another process holding it (a lease command, a reap) must not keep the
// renewal, and with it the shutdown, waiting.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestLockDirContextGivesUp: a lock held elsewhere ends the wait at ctx's deadline, and the lock is
// taken normally once the holder releases it.
func TestLockDirContextGivesUp(t *testing.T) {
	dir := t.TempDir()
	release, err := lockDir(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := lockDirContext(ctx, dir); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait under a held lock: %v, want the deadline", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the wait took %s under a 50ms deadline", d)
	}
	release()
	release2, err := lockDirContext(context.Background(), dir)
	if err != nil {
		t.Fatalf("lock after the holder released it: %v", err)
	}
	release2()
}
