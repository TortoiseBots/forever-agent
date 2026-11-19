package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// TestWithLoginCarriesTheRealmsBuilds: the world socket takes both builds from the login result, never from the flag.
// With --build absent (0) the login claims its newest known build and a realm on 70707 answers 70707; the world
// options must carry both numbers, or the world socket would claim a build the realm refused.
func TestWithLoginCarriesTheRealmsBuilds(t *testing.T) {
	c := foreverCommon{clientBuild: 0, character: "C", allowRedirect: true}
	var out bytes.Buffer
	opts := c.withLogin(&bnet.Result{
		ClientBuild: 70707,
		RealmBuild:  70707,
		ServerIP:    "127.0.0.1",
		ServerPort:  4321,
	}, &out, world.Options{Stay: 1})
	if opts.ClientBuild != 70707 || opts.RealmBuild != 70707 {
		t.Fatalf("builds %d/%d, want the login result's 70707/70707", opts.ClientBuild, opts.RealmBuild)
	}
	if opts.Address != "127.0.0.1:4321" || opts.Character != "C" || !opts.AllowInstanceRedirect || opts.Stay != 1 {
		t.Fatalf("options %+v", opts)
	}
	if opts.Logf == nil {
		t.Fatal("no log sink")
	}
}

// TestLoginErrorsAreClassified: a login failure is told apart from a world failure (the commands' exit codes), and a
// realm refusal keeps bnet.RealmError reachable through the wrapper.
func TestLoginErrorsAreClassified(t *testing.T) {
	var stderr bytes.Buffer
	cause := &bnet.RealmError{Err: &bnet.RealmMismatch{Realm: "R", Claimed: 70938, RealmBuild: 70707}}
	err := &loginError{errors.New("forever world-check: " + cause.Error())}
	if code, isLogin := loginExit(err, &stderr); !isLogin || code != exitAuth {
		t.Fatalf("a wrapped non-realm error: code %d, login %v", code, isLogin)
	}
	err = &loginError{cause}
	if code, isLogin := loginExit(err, &stderr); !isLogin || code != exitRealm {
		t.Fatalf("a realm refusal: code %d, login %v", code, isLogin)
	}
	if !errors.As(err, new(*bnet.RealmError)) {
		t.Fatal("bnet.RealmError is not reachable through loginError")
	}
	if _, isLogin := loginExit(world.ErrLinkLost, &stderr); isLogin {
		t.Fatal("a world error was classified as a login failure")
	}
	if !bytes.Contains(stderr.Bytes(), []byte("does not accept client build")) {
		t.Fatalf("stderr %q", stderr.String())
	}
}
