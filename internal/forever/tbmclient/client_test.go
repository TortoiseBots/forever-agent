package tbmclient

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tbmcontract "github.com/pjerra/forever-agent/contract/tbm"
	"github.com/pjerra/forever-agent/internal/forever/tbmproto"
)

// TestHelloAndDo runs the acceptance sequence the spec names: the chat hello
// (ok-hello-chat-none), the add-on hello (ok-hello-addon-party), and the
// action follow acknowledgement (ok-command-echo-ack) against the fake module.
func TestHelloAndDo(t *testing.T) {
	cases, err := tbmproto.LoadCases(tbmcontract.V1, "v1/cases")
	if err != nil {
		t.Fatal(err)
	}
	fake := NewFakeModule(cases)
	c := New(fake)

	hello, err := c.Hello(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("chat hello: %v", err)
	}
	if !hello.Complete || hello.Transport != "none" {
		t.Fatalf("chat hello = %+v, want complete on none", hello)
	}
	if hello.Proto != 1 || hello.MinAddon != 1 || hello.Version != "v0.1.0-forever" {
		t.Fatalf("chat hello trailer = %+v", hello)
	}
	if len(hello.Caps) != 1 || hello.Caps[0] != "behavior" {
		t.Fatalf("chat hello caps = %v", hello.Caps)
	}
	if err := hello.Check(); err != nil {
		t.Fatalf("chat hello check: %v", err)
	}

	c.transport = tbmproto.TransportAddon
	hello, err = c.Hello(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("add-on hello: %v", err)
	}
	if !hello.Complete || hello.Transport != "party" {
		t.Fatalf("add-on hello = %+v, want complete on party", hello)
	}
	if hello.Proto != 1 || hello.MinAddon != 1 || hello.Version != "v0.1.0-forever" {
		t.Fatalf("add-on hello trailer = %+v", hello)
	}
	if err := hello.Check(); err != nil {
		t.Fatalf("add-on hello check: %v", err)
	}

	res, err := c.Do(context.Background(), "action follow", 2*time.Second)
	if err != nil {
		t.Fatalf("action follow: %v", err)
	}
	if len(res.Acks) != 1 {
		t.Fatalf("action follow acks = %d, want 1", len(res.Acks))
	}
	a := res.Acks[0]
	if a.Intent != "follow" || a.Scope != "party" || a.Count != 3 || a.Detail != "-" {
		t.Fatalf("action follow ack = %+v", a)
	}
	if len(res.Problems) != 0 {
		t.Fatalf("action follow problems = %v", res.Problems)
	}
}

// TestHelloWithoutRegistrationTimesOut is the failing case the spec names: a
// client that never registers the prefix gets no replies, because the fake
// drops unregistered prefixes as the core does, so the hello times out.
func TestHelloWithoutRegistrationTimesOut(t *testing.T) {
	cases, err := tbmproto.LoadCases(tbmcontract.V1, "v1/cases")
	if err != nil {
		t.Fatal(err)
	}
	fake := NewFakeModule(cases)
	fake.IgnoreRegistration = true
	c := New(fake)
	c.transport = tbmproto.TransportAddon

	_, err = c.Hello(context.Background(), 150*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("hello without registration = %v, want ErrTimeout", err)
	}
}

// TestDoRefusesOverlongCommand: a command over the 255-byte cap is rejected before anything is sent.
func TestDoRefusesOverlongCommand(t *testing.T) {
	cases, err := tbmproto.LoadCases(tbmcontract.V1, "v1/cases")
	if err != nil {
		t.Fatal(err)
	}
	c := New(NewFakeModule(cases))
	c.transport = tbmproto.TransportAddon
	long := "action follow " + strings.Repeat("a", 300)
	if _, err := c.Do(context.Background(), long, time.Second); err == nil {
		t.Fatal("an over-long command must be refused")
	}
}

// TestDoUnknownCommandTimesOut: the fake answers only golden cases, so an unknown command gets no reply.
func TestDoUnknownCommandTimesOut(t *testing.T) {
	cases, err := tbmproto.LoadCases(tbmcontract.V1, "v1/cases")
	if err != nil {
		t.Fatal(err)
	}
	c := New(NewFakeModule(cases))
	c.transport = tbmproto.TransportAddon
	if _, err := c.Do(context.Background(), "action nonsense", 150*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("unknown command = %v, want ErrTimeout", err)
	}
}
