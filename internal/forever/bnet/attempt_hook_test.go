package bnet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// A caller that keeps a login rate books every real login. With the build left to the selection, each build the world
// server refuses is another full login, so N refused builds are N reservations, not one.
func TestBeforeAttemptRunsOncePerBuildProbe(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realms = []fakeRealm{{
		address: 0x46010046, name: "TestRealm", population: 1,
		build: wire.Build70959, alsoAccept: []uint32{wire.Build70938, wire.Build70707},
	}}
	w := newFakeWorld(t, func(uint32) bool { return true }) // the world server refuses every build

	opts := f.options()
	opts.WorldAccept = worldAcceptThrough(w, nil)
	opts.WorldSkipBuildAuthKeyCheck = true
	reservations := 0
	opts.BeforeAttempt = func(context.Context) error { reservations++; return nil }
	if _, err := Login(context.Background(), opts); err == nil {
		t.Fatal("a login whose every build the world server refused was reported as done")
	}
	wantLogonBuilds(t, f, wire.Build70959, wire.Build70938, wire.Build70707)
	if reservations != 3 {
		t.Fatalf("%d reservations for 3 real logins", reservations)
	}
}

// The realm refusing a build is a real login too (the logon was made before the realm list showed the mismatch).
func TestBeforeAttemptRunsForTheRealmsBuildHint(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realmBuild = wire.Build70707
	opts := f.options()
	logins := 0
	opts.BeforeAttempt = func(context.Context) error { logins++; return nil }
	if _, err := Login(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if logins != 2 {
		t.Fatalf("%d reservations, want 2 (the refused claim and the realm's own build)", logins)
	}
	wantLogonBuilds(t, f, wire.Build70959, wire.Build70707)
}

// A pinned build is one login, and so one reservation.
func TestBeforeAttemptRunsOnceForAPinnedBuild(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	opts := f.options()
	opts.ClientBuild = wire.Build70938 // the fake realm's own build
	n := 0
	opts.BeforeAttempt = func(context.Context) error { n++; return nil }
	if _, err := Login(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d reservations for one login", n)
	}
}

// The hook's refusal ends the login before anything is sent, and is returned as it is.
func TestBeforeAttemptRefusalMakesNoLogin(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	opts := f.options()
	want := errors.New("no turn")
	opts.BeforeAttempt = func(context.Context) error { return want }
	if _, err := Login(context.Background(), opts); !errors.Is(err, want) {
		t.Fatalf("err = %v, want the hook's", err)
	}
	wantLogonBuilds(t, f)
}

// A wait for a turn is not the attempt's time: with a hook the Deadline bounds each attempt, so a hook that waits longer
// than the Deadline does not fail the login.
func TestBeforeAttemptWaitIsOutsideTheDeadline(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	opts := f.options()
	opts.Deadline = 3 * time.Second
	opts.BeforeAttempt = func(ctx context.Context) error {
		select {
		case <-time.After(3500 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if _, err := Login(context.Background(), opts); err != nil {
		t.Fatalf("the wait for a turn ate the attempt's deadline: %v", err)
	}
}
