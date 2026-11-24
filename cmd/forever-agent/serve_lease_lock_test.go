//go:build unix

package main

// The lease keeper must not outlive the command, and must not be held by the slots store's lock.
// Its renewal takes the store's .lock (a blocking flock, internal/slots/lock_unix.go); another
// process holding it (a lease command, a reap, a slot dir on a network mount) used to keep the
// keeper inside Renew, so the command's wait for the keeper never ended: no interrupt, teardown or
// early return got the command to exit.
//
// The wait for the keeper is bounded and the keeper's own wait for the lock ends with the run's
// context, so both a held lock and a write that never returns let the command go.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// serveLockTheStore holds the slots store's lock as another lease command would, until release runs.
// The keeper's renewal of the serve's own lease then waits for it.
func serveLockTheStore(t *testing.T, slotsPath string) (release func()) {
	t.Helper()
	dir := filepath.Dir(slotsPath)
	path := filepath.Join(dir, "leases", ".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		t.Fatalf("taking the store lock: %v", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

// TestServeStopsWhenTheLeaseLockIsHeld: the keeper's renewal waits on the store's .lock, which
// another process holds. Cancelling the serve has to end the keeper's wait and the command; a
// command still blocked after the deadline is the shutdown hang the review found.
func TestServeStopsWhenTheLeaseLockIsHeld(t *testing.T) {
	dir, slotsPath, _, port := serveLeaseFixture(t, serveLeaseTTL)
	settings := serveSettingsListening(t, dir, fmt.Sprintf("127.0.0.1:%d", port))
	serveShortRenew(t, 10*time.Millisecond)
	entered := make(chan struct{})
	var once sync.Once
	serveStage(t,
		func(context.Context) (*bnet.Result, error) { return serveStubResult(), nil },
		func(ctx context.Context, _ world.Options) (*world.Report, error) {
			once.Do(func() { close(entered) })
			<-ctx.Done()
			return &world.Report{Counts: map[string]int{}}, nil
		})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- foreverServeCmd(ctx, []string{"--settings", settings, "--dir", dir}, &out, &errOut) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("the serve never reached the world; stdout %q stderr %q", out.String(), errOut.String())
	}

	// Another process takes the store's lock and never gives it back while the serve shuts down.
	release := serveLockTheStore(t, slotsPath)
	defer release()
	time.Sleep(100 * time.Millisecond) // let the keeper's tick reach the lock

	cancel()
	select {
	case got := <-code:
		if got != exitOK {
			t.Fatalf("exit %d, want %d; stdout %q stderr %q", got, exitOK, out.String(), errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the command still waits for a keeper held by the store's lock; stdout %q", out.String())
	}
	if strings.Contains(out.String(), "renew failed") {
		t.Fatalf("a renewal that ended with the run was logged as a failure: %q", out.String())
	}
}

// TestServeStopsWhenTheKeeperIsStuckWriting: the wait for the keeper is bounded even when the
// keeper cannot get out of a write (a held pipe, a full disk). The command logs that it stopped
// waiting and returns; the write's own release is then not the shutdown's problem.
func TestServeStopsWhenTheKeeperIsStuckWriting(t *testing.T) {
	dir, _, _, port := serveLeaseFixture(t, serveLeaseTTL)
	settings := serveSettingsListening(t, dir, fmt.Sprintf("127.0.0.1:%d", port))
	serveShortRenew(t, 10*time.Millisecond)
	oldWait := serveLeaseStopWait
	serveLeaseStopWait = 100 * time.Millisecond
	t.Cleanup(func() { serveLeaseStopWait = oldWait })
	entered := make(chan struct{})
	var once sync.Once
	serveStage(t,
		func(context.Context) (*bnet.Result, error) { return serveStubResult(), nil },
		func(ctx context.Context, _ world.Options) (*world.Report, error) {
			once.Do(func() { close(entered) })
			<-ctx.Done()
			return &world.Report{Counts: map[string]int{}}, nil
		})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := newGatedWriter()
	var errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- foreverServeCmd(ctx, []string{"--settings", settings, "--dir", dir}, out, &errOut) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("the serve never reached the world; stdout %q stderr %q", out.String(), errOut.String())
	}

	out.holdWrites()
	select {
	case <-out.blocked:
	case <-time.After(5 * time.Second):
		t.Fatalf("no renewal write to hold; stdout %q", out.String())
	}
	cancel()
	select {
	case got := <-code:
		if got != exitOK {
			t.Fatalf("exit %d, want %d; stdout %q stderr %q", got, exitOK, out.String(), errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the command waited for a keeper stuck in a write past its bound; stdout %q", out.String())
	}
	if !strings.Contains(out.String(), "did not stop within 100ms") {
		t.Fatalf("the bounded wait did not say so: %q", out.String())
	}
	close(out.release)
	time.Sleep(50 * time.Millisecond) // let the released write finish before the test's dirs go
}
