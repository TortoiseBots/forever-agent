package world

// The command pump's own loop: a cancel must end it at once instead of waiting for the command deadline.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
)

func TestCmdLinkPumpHonoursContext(t *testing.T) {
	// A session with no packets and no dead link: Pump's poll finds nothing, and the run's context is the only
	// end condition.
	s := &session{ctx: context.Background(), events: make(chan event)}
	link := &cmdLink{s: s}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := link.Pump(ctx, func(chat.Line) {}, func() bool { return false })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the cancel took %s; the pump ignored the context", took)
	}
}

func TestCmdLinkPumpReturnsOnDone(t *testing.T) {
	s := &session{ctx: context.Background(), events: make(chan event)}
	link := &cmdLink{s: s}
	if err := link.Pump(context.Background(), func(chat.Line) {}, func() bool { return true }); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if s.cmdFeed != nil {
		t.Fatal("the feed was left set")
	}
}
