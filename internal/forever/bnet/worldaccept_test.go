package bnet

// The automatic build selection must end where the build is really checked: the world server's build_info table,
// not the realm list. A realm that also-accepts several builds (Realm.AlsoAcceptBuilds) lists the client's own
// claim for each of them, so the realm list cannot tell which one the world server knows; the world server answers
// ERROR_BAD_VERSION for the others (WorldSocket.cpp:739-763). These tests run the fake login service against the
// fake world socket and drive Options.WorldAccept exactly as the command layer does.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// worldAcceptThrough is the command layer's WorldAccept: authenticate the world socket for the login's build and
// hand it back (world.Enter adopts it, so no second authentication follows).
func worldAcceptThrough(w *fakeWorld) func(context.Context, *Result) (*worldconn.Realm, error) {
	return func(ctx context.Context, r *Result) (*worldconn.Realm, error) {
		w.prepare(r.ClientBuild, r.ClientSecret, r.JoinSecret)
		return worldconn.Authenticate(ctx, worldconn.Config{
			Address:         w.addr(),
			RealmID:         r.RealmID,
			RealmAddress:    r.RealmAddress,
			RealmJoinTicket: r.JoinTicket,
			ClientSecret:    r.ClientSecret,
			JoinSecret:      r.JoinSecret,
			ClientBuild:     r.ClientBuild,
			RealmBuild:      r.RealmBuild,
			Deadline:        10 * time.Second,
		})
	}
}

// TestLoginWorldRefusesNewestBuild: the realm accepts 70959 and 70938, the world server knows only 70938. The
// login claims the newest (70959), the world refuses it with ERROR_BAD_VERSION, and the selection moves on to the
// next known build instead of ending at the realm list. The accepted build's socket comes back for the entry.
func TestLoginWorldRefusesNewestBuild(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realms = []fakeRealm{{
		address: 0x46010046, name: "TestRealm", population: 1,
		build: wire.Build70959, alsoAccept: []uint32{wire.Build70938},
	}}
	w := newFakeWorld(t, func(build uint32) bool { return build != wire.Build70938 })

	var steps []string
	opts := f.options()
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	opts.WorldAccept = worldAcceptThrough(w)

	result, err := Login(context.Background(), opts)
	if err != nil {
		t.Fatalf("Login: %v\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
	if result.ClientBuild != wire.Build70938 || result.RealmBuild != wire.Build70938 {
		t.Fatalf("claimed %d (realm list %d), want the world server's 70938", result.ClientBuild, result.RealmBuild)
	}
	if result.WorldConn == nil {
		t.Fatal("the accepted build's world socket was not handed back")
	}
	defer result.WorldConn.Close()

	wantLogonBuilds(t, f, wire.Build70959, wire.Build70938)
	if got, want := w.wait(t), []uint32{wire.Build70959, wire.Build70938}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the world socket saw builds %v, want %v", got, want)
	}
	if !strings.Contains(strings.Join(steps, "\n"), "the world server does not accept build 70959") {
		t.Errorf("the step log does not say the world server refused 70959:\n%s", strings.Join(steps, "\n"))
	}
}

// TestLoginWorldRefusesEveryBuild: the realm accepts all three known builds, the world server knows none of them.
// The selection is bounded by the known builds, and the error names every build tried and the side that refused it.
func TestLoginWorldRefusesEveryBuild(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realms = []fakeRealm{{
		address: 0x46010046, name: "TestRealm", population: 1,
		build: wire.Build70959, alsoAccept: []uint32{wire.Build70938, wire.Build70707},
	}}
	w := newFakeWorld(t, func(uint32) bool { return true })

	opts := f.options()
	opts.WorldAccept = worldAcceptThrough(w)
	_, err := Login(context.Background(), opts)
	if err == nil {
		t.Fatal("a login whose every build the world server refused was reported as done")
	}
	var realmErr *RealmError
	if !errors.As(err, &realmErr) {
		t.Fatalf("err = %v, want a RealmError", err)
	}
	for _, want := range []string{
		`realm "TestRealm"`,
		"no client build this client knows was accepted",
		"build 70959: the world server refused it",
		"build 70938: the world server refused it",
		"build 70707: the world server refused it",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not contain %q: %v", want, err)
		}
	}

	wantLogonBuilds(t, f, wire.Build70959, wire.Build70938, wire.Build70707)
	if got, want := w.wait(t), []uint32{wire.Build70959, wire.Build70938, wire.Build70707}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the world socket saw builds %v, want %v", got, want)
	}
}

// TestLoginPinnedBuildWorldRefusal: an explicit build is never replaced. The world server's refusal is the login's
// error, and only that one build is tried.
func TestLoginPinnedBuildWorldRefusal(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realmBuild = wire.Build70959
	w := newFakeWorld(t, func(uint32) bool { return true })

	opts := f.options()
	opts.ClientBuild = wire.Build70959
	opts.WorldAccept = worldAcceptThrough(w)
	_, err := Login(context.Background(), opts)
	if err == nil {
		t.Fatal("a pinned build the world server refused was reported as done")
	}
	var refused *worldconn.Refused
	if !errors.As(err, &refused) || !refused.BadVersion() {
		t.Fatalf("err = %v, want the world server's ERROR_BAD_VERSION refusal", err)
	}
	if !strings.Contains(err.Error(), "ERROR_BAD_VERSION") {
		t.Errorf("the error does not name ERROR_BAD_VERSION: %v", err)
	}

	wantLogonBuilds(t, f, wire.Build70959)
	if got, want := w.wait(t), []uint32{wire.Build70959}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the world socket saw builds %v, want %v", got, want)
	}
}
