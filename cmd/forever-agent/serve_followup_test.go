package main

// The follow-ups to #225: one serve per slot dir, the lease keeper's lifetime, the commands that end a
// serve when its lease is lost, and the logout a cancelled serving context owes the server.
//
// The command tests stage the login and the world half (serveLogin, serveWorldEnter): they run the
// whole command (flags, settings, dir lock, lease adopt, keeper, shutdown, exit code) without a game
// server, which is what the lease rules are about. The serve loop against the fake world server,
// cancel and logout included, is covered in internal/forever/world.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// serveStubResult is a login result a staged command can carry: no socket, only the fields
// foreverCommon.withLogin reads.
func serveStubResult() *bnet.Result {
	return &bnet.Result{
		RealmID:      70,
		RealmAddress: 0x46010046,
		JoinTicket:   []byte(`{"gameAccount":"000#0"}`),
		ClientSecret: []byte(strings.Repeat("c", 32)),
		JoinSecret:   []byte(strings.Repeat("j", 32)),
		ClientBuild:  70707,
		RealmBuild:   70707,
		ServerIP:     "127.0.0.1",
		ServerPort:   1,
		GameAccounts: 1,
	}
}

// serveStage swaps the command's login and world half for the test and restores both after it.
func serveStage(t *testing.T, login func(ctx context.Context) (*bnet.Result, error), enter func(context.Context, world.Options) (*world.Report, error)) {
	t.Helper()
	oldLogin, oldEnter := serveLogin, serveWorldEnter
	t.Cleanup(func() { serveLogin, serveWorldEnter = oldLogin, oldEnter })
	if login != nil {
		serveLogin = func(ctx context.Context, _ *foreverCommon, _ io.Writer, _ string) (*bnet.Result, error) {
			return login(ctx)
		}
	}
	if enter != nil {
		serveWorldEnter = enter
	}
}

// serveShortRenew shortens the renewal period for a test and restores it.
func serveShortRenew(t *testing.T, d time.Duration) {
	t.Helper()
	old := serveLeaseRenew
	serveLeaseRenew = d
	t.Cleanup(func() { serveLeaseRenew = old })
}

// serveSettingsListening writes a settings file like serveSettingsFixture, with its own listen address.
func serveSettingsListening(t *testing.T, dir, listen string) string {
	t.Helper()
	body := fmt.Sprintf(`{"account":"TESTACC","character":"Gbmage","rest":"127.0.0.1:1","rpc":"127.0.0.1:2",
		"ca":"%s/none.pem","listen":"%s","dir":"%s","password":"%s"}`, dir, listen, dir, serveTestPassword)
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// serveRenewed reads the slot's lease renewal clock; the keeper's writes are the only writer after adopt.
func serveRenewed(t *testing.T, slotsPath string) time.Time {
	t.Helper()
	cur, err := serveLeaseStore(t, slotsPath).Get("test1", "1")
	if err != nil || cur == nil {
		t.Fatalf("lease: %+v %v", cur, err)
	}
	return cur.Renewed
}

// TestServeRenewsTheLeaseForTheWholeRun is the item-5 failing case: deleting the lease.keep call
// leaves adopt's one renewal behind, and this test then sees the lease's renewal clock stop while
// the serve is still running.
func TestServeRenewsTheLeaseForTheWholeRun(t *testing.T) {
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
	first := serveRenewed(t, slotsPath)
	waitFor(t, "a lease renewal while the serve runs", func() bool {
		return serveRenewed(t, slotsPath).After(first)
	})

	cancel()
	select {
	case got := <-code:
		if got != exitOK {
			t.Fatalf("exit %d, want %d; stdout %q stderr %q", got, exitOK, out.String(), errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the serve did not return; stdout %q", out.String())
	}
	if !strings.Contains(out.String(), "renewed until") {
		t.Fatalf("no renewal line: %q", out.String())
	}
	if !strings.Contains(out.String(), "serve: logged out") {
		t.Fatalf("the staged session did not end through the command's own path: %q", out.String())
	}
}

// TestServeLostLeaseExits13WhileServing: a lease released under a running serve is not renewed again,
// the keeper cancels the run, the world half returns and the command exits 13 with the reason already
// logged.
func TestServeLostLeaseExits13WhileServing(t *testing.T) {
	dir, slotsPath, acq, port := serveLeaseFixture(t, serveLeaseTTL)
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

	// The slot is released from outside the serve (teardown or `lease release`), as an operator would.
	store := serveLeaseStore(t, slotsPath)
	if _, err := store.Release("test1", "1", "w-a", acq.Lease.LeaseID, true); err != nil {
		t.Fatalf("release: %v", err)
	}

	select {
	case got := <-code:
		if got != exitServeLeaseLost {
			t.Fatalf("exit %d, want %d; stdout %q stderr %q", got, exitServeLeaseLost, out.String(), errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the serve ran on with a lost lease; stdout %q", out.String())
	}
	if !strings.Contains(out.String(), "not ours any more") {
		t.Fatalf("no lost-lease line: %q", out.String())
	}
}

// TestServeLostLeaseExits13DuringLogin: the keeper covers the login too; a lease lost while the
// command is still logging in stops it with 13 rather than an auth exit code.
func TestServeLostLeaseExits13DuringLogin(t *testing.T) {
	dir, slotsPath, acq, port := serveLeaseFixture(t, serveLeaseTTL)
	settings := serveSettingsListening(t, dir, fmt.Sprintf("127.0.0.1:%d", port))
	serveShortRenew(t, 10*time.Millisecond)
	started := make(chan struct{})
	var once sync.Once
	serveStage(t,
		func(ctx context.Context) (*bnet.Result, error) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return nil, errors.New("login interrupted by the cancelled run")
		},
		nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- foreverServeCmd(ctx, []string{"--settings", settings, "--dir", dir}, &out, &errOut) }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatalf("the login was never called; stdout %q stderr %q", out.String(), errOut.String())
	}
	store := serveLeaseStore(t, slotsPath)
	if _, err := store.Release("test1", "1", "w-a", acq.Lease.LeaseID, true); err != nil {
		t.Fatalf("release: %v", err)
	}

	select {
	case got := <-code:
		if got != exitServeLeaseLost {
			t.Fatalf("exit %d, want %d; stdout %q stderr %q", got, exitServeLeaseLost, out.String(), errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the login ran on with a lost lease; stdout %q", out.String())
	}
	if !strings.Contains(out.String(), "not ours any more") {
		t.Fatalf("no lost-lease line: %q", out.String())
	}
}

// gatedWriter holds a keeper renewal inside Write until the test releases it. It records writes that
// arrive after the command returned, which is the "no write after return" rule.
type gatedWriter struct {
	mu       sync.Mutex
	b        strings.Builder
	hold     bool
	blocked  chan struct{} // closed when a held write reached Write
	release  chan struct{}
	returned bool
	after    int
}

func newGatedWriter() *gatedWriter {
	return &gatedWriter{blocked: make(chan struct{}), release: make(chan struct{})}
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	hold := w.hold && strings.Contains(string(p), "renewed until")
	if hold {
		select {
		case <-w.blocked:
		default:
			close(w.blocked)
		}
	}
	w.mu.Unlock()
	if hold {
		<-w.release
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.returned {
		w.after++
	}
	return w.b.Write(p)
}

func (w *gatedWriter) holdWrites() {
	w.mu.Lock()
	w.hold = true
	w.mu.Unlock()
}

func (w *gatedWriter) returnedNow() {
	w.mu.Lock()
	w.hold = false
	w.returned = true
	w.mu.Unlock()
}

func (w *gatedWriter) postReturnWrites() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.after
}

func (w *gatedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// TestServeWaitsForTheKeeper: the command must not return while the keeper still holds the output. The
// keeper is held inside a renewal write, the run is cancelled, and the command has to stay blocked
// until the keeper goroutine has finished; a return before the release is the failing case (and the
// race the review found).
func TestServeWaitsForTheKeeper(t *testing.T) {
	dir, _, _, port := serveLeaseFixture(t, serveLeaseTTL)
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
		t.Fatalf("the command returned (exit %d) while the keeper was still writing", got)
	case <-time.After(200 * time.Millisecond):
		// still blocked behind the keeper: correct
	}
	close(out.release)
	select {
	case got := <-code:
		if got != exitOK {
			t.Fatalf("exit %d, want %d; stdout %q stderr %q", got, exitOK, out.String(), errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the command stayed blocked after the keeper was released; stdout %q", out.String())
	}
	out.returnedNow()
	time.Sleep(50 * time.Millisecond)
	if n := out.postReturnWrites(); n != 0 {
		t.Fatalf("%d write(s) to the output after the command returned", n)
	}
}

// TestServeDirLock: the lock is exclusive, released by the caller, and a stale file left by a dead
// serve does not block a later one (the kernel dropped that lock; only the file is left).
func TestServeDirLock(t *testing.T) {
	dir := t.TempDir()
	release, err := lockServeDir(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if _, err := lockServeDir(dir); err == nil || !strings.Contains(err.Error(), "another forever serve") {
		t.Fatalf("second lock on one dir: %v", err)
	}
	release()
	release2, err := lockServeDir(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
	// A lock file whose holder died: flock is gone, the file remains.
	if err := os.WriteFile(filepath.Join(dir, serveLockName), []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release3, err := lockServeDir(dir)
	if err != nil {
		t.Fatalf("a stale lock file blocked a serve: %v", err)
	}
	release3()
}

// TestServeRefusesASecondServeOnTheDir: two serves on one dir share the manifest, the token file and
// the lease, so a second is refused whatever its listen address. The first is on 127.0.0.1, the
// second (which passes serveCheckDir, same port) asks for 127.0.0.2 and hits the dir lock.
func TestServeRefusesASecondServeOnTheDir(t *testing.T) {
	dir, _, _, port := serveLeaseFixture(t, serveLeaseTTL)
	first := serveSettingsListening(t, dir, fmt.Sprintf("127.0.0.1:%d", port))
	second := serveSettingsListening(t, dir, fmt.Sprintf("127.0.0.2:%d", port))
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
	go func() { code <- foreverServeCmd(ctx, []string{"--settings", first, "--dir", dir}, &out, &errOut) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("the first serve never reached the world; stdout %q stderr %q", out.String(), errOut.String())
	}

	var out2, errOut2 syncBuf
	if got := foreverServeCmd(context.Background(), []string{"--settings", second, "--dir", dir}, &out2, &errOut2); got != exitSlotRefused {
		t.Fatalf("second serve exit %d, want %d; stdout %q stderr %q", got, exitSlotRefused, out2.String(), errOut2.String())
	}
	if !strings.Contains(errOut2.String(), "another forever serve is already running in") {
		t.Fatalf("second serve stderr %q does not name the running serve", errOut2.String())
	}
	if strings.Contains(out2.String()+errOut2.String(), serveTestPassword) {
		t.Fatal("the refusal leaks the password")
	}

	cancel()
	select {
	case got := <-code:
		if got != exitOK {
			t.Fatalf("first serve exit %d, want %d; stderr %q", got, exitOK, errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first serve did not return")
	}
}

// TestServeLockIsReleasedWhenTheServeEnds: after the command returns, the next serve on the dir gets
// the lock (proven by it reaching the world instead of exiting 15).
func TestServeLockIsReleasedWhenTheServeEnds(t *testing.T) {
	dir, _, _, port := serveLeaseFixture(t, serveLeaseTTL)
	settings := serveSettingsListening(t, dir, fmt.Sprintf("127.0.0.1:%d", port))
	serveShortRenew(t, 10*time.Millisecond)
	entered := make(chan struct{}, 2)
	serveStage(t,
		func(context.Context) (*bnet.Result, error) { return serveStubResult(), nil },
		func(ctx context.Context, _ world.Options) (*world.Report, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return &world.Report{Counts: map[string]int{}}, nil
		})

	run := func() (int, *syncBuf) {
		ctx, cancel := context.WithCancel(context.Background())
		var out, errOut syncBuf
		code := make(chan int, 1)
		go func() { code <- foreverServeCmd(ctx, []string{"--settings", settings, "--dir", dir}, &out, &errOut) }()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("the serve never reached the world; stdout %q stderr %q", out.String(), errOut.String())
		}
		cancel()
		select {
		case got := <-code:
			return got, &errOut
		case <-time.After(10 * time.Second):
			t.Fatal("the serve did not return")
		}
		return 0, nil
	}
	if got, errOut := run(); got != exitOK {
		t.Fatalf("first serve exit %d; stderr %q", got, errOut.String())
	}
	if got, errOut := run(); got != exitOK {
		t.Fatalf("second serve after the first ended: exit %d; stderr %q", got, errOut.String())
	}
}

// TestServeLeaseFixtureSanity pins the fixture the follow-up tests lean on: the slot.json names a
// live lease with a lease id, so a "lost lease" in these tests is a real change, not a missing lease.
func TestServeLeaseFixtureSanity(t *testing.T) {
	dir, slotsPath, acq, _ := serveLeaseFixture(t, serveLeaseTTL)
	lease, err := openServeLease(dir)
	if err != nil || lease == nil {
		t.Fatalf("openServeLease: %+v %v", lease, err)
	}
	if lease.leaseID != acq.Lease.LeaseID || lease.holder != "w-a" {
		t.Fatalf("fixture lease %+v does not match %+v", lease, acq.Lease)
	}
	store := serveLeaseStore(t, slotsPath)
	cur, err := store.Get("test1", "1")
	if err != nil || cur == nil || cur.Expires.Before(time.Now()) {
		t.Fatalf("fixture lease not live: %+v %v", cur, err)
	}
	// And the file the keeper reads is where the fixture's slots table says it is.
	var raw map[string]any
	b, err := os.ReadFile(filepath.Join(filepath.Dir(slotsPath), "leases", "test1", "1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("lease file: %v", err)
	}
}
