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
// hand it back (world.Enter adopts it, so no second authentication follows). key is the build auth key the callback
// would find in auth.build_auth_key, nil for the command layer's zero digest.
func worldAcceptThrough(w *fakeWorld, key []byte) func(context.Context, *Result) (*worldconn.Realm, error) {
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
			BuildAuthKey:    key,
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
	opts.WorldAccept = worldAcceptThrough(w, nil)
	opts.WorldSkipBuildAuthKeyCheck = true // the setup doc's worldserver.conf: the server does not check the key

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
	opts.WorldAccept = worldAcceptThrough(w, nil)
	opts.WorldSkipBuildAuthKeyCheck = true // the setup doc's worldserver.conf: the server does not check the key
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
	opts.WorldAccept = worldAcceptThrough(w, nil)
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

// TestLoginWorldBadVersionAmbiguousIsNotRetried: the world server answers ERROR_BAD_VERSION and nothing says which
// of its two checks refused the session — the callback sent no build auth key and the server setting was not
// established. The login must not claim another build and must not report the builds as rejected: the same code is
// what a missing build_auth_key row produces while Network.SkipBuildAuthKeyCheck is off (WorldSocket.cpp:760-768),
// and this client's zero digest cannot pass that check, so a retry could not have succeeded either.
func TestLoginWorldBadVersionAmbiguousIsNotRetried(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realms = []fakeRealm{{
		address: 0x46010046, name: "TestRealm", population: 1,
		build: wire.Build70959, alsoAccept: []uint32{wire.Build70938, wire.Build70707},
	}}
	w := newFakeWorld(t, func(uint32) bool { return true })

	opts := f.options()
	opts.WorldAccept = worldAcceptThrough(w, nil) // no key, like the command layer's callback
	_, err := Login(context.Background(), opts)
	if err == nil {
		t.Fatal("a login whose world refusal is ambiguous was reported as done")
	}
	var refused *worldconn.Refused
	if !errors.As(err, &refused) || !refused.BadVersion() {
		t.Fatalf("err = %v, want the world server's ERROR_BAD_VERSION refusal as the cause", err)
	}
	for _, want := range []string{
		"ERROR_BAD_VERSION",
		"build_info",
		"build_auth_key",
		"Network.SkipBuildAuthKeyCheck",
		"build 70959 is not thereby unsupported",
		"pass --build N",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not contain %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "no client build this client knows was accepted") {
		t.Errorf("the error still reports the builds as rejected by the world server: %v", err)
	}

	// one claim, one world authentication: no retry on a code that may not be about the build
	wantLogonBuilds(t, f, wire.Build70959)
	if got, want := w.wait(t), []uint32{wire.Build70959}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the world socket saw builds %v, want %v", got, want)
	}
	for i, d := range w.authDigests() {
		if !allZero(d) {
			t.Errorf("connection %d sent a non-zero digest %x; this path is about the missing-key case", i, d)
		}
	}
}

// TestLoginWorldBadVersionWithKeyIsAttributed: the callback sends the 16-byte build auth key, so the world server
// cannot have refused for a missing key row (a wrong digest is ERROR_DENIED, WorldSocket.cpp:775-791). The 0x1C is
// therefore attributable to build_info, and the selection moves on to the build the world server knows.
func TestLoginWorldBadVersionWithKeyIsAttributed(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()
	f.realms = []fakeRealm{{
		address: 0x46010046, name: "TestRealm", population: 1,
		build: wire.Build70959, alsoAccept: []uint32{wire.Build70938},
	}}
	w := newFakeWorld(t, func(build uint32) bool { return build != wire.Build70938 })

	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(i + 1)
	}
	opts := f.options()
	opts.WorldAccept = worldAcceptThrough(w, key)
	result, err := Login(context.Background(), opts)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.ClientBuild != wire.Build70938 {
		t.Fatalf("claimed %d, want the world server's 70938", result.ClientBuild)
	}
	if result.WorldConn == nil {
		t.Fatal("the accepted build's world socket was not handed back")
	}
	defer result.WorldConn.Close()

	wantLogonBuilds(t, f, wire.Build70959, wire.Build70938)
	if got, want := w.wait(t), []uint32{wire.Build70959, wire.Build70938}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the world socket saw builds %v, want %v", got, want)
	}
	digests := w.authDigests()
	if len(digests) != 2 {
		t.Fatalf("%d connections, want 2", len(digests))
	}
	for i, d := range digests {
		if len(d) != 24 || allZero(d) {
			t.Errorf("connection %d sent digest %x, want 24 bytes from the build auth key", i, d)
		}
	}
}

// allZero reports whether every byte of b is zero.
func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
