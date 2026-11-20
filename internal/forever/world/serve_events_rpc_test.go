package world

// events' long-poll through the RPC: an empty wait must leave the session's call queue to everyone else. The test
// runs several empty 2 s waits at once and, while they are parked, asks for status and sends a chat line; both must
// answer at once, and every waiter must return at its own 2 s cap rather than after the ones before it.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/rpc"
)

func TestServeEventsLongPollDoesNotHoldTheQueue(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeUnits(t, a)

	type answer struct {
		page trialsEventPage
		err  *rpcError
		took time.Duration
	}
	const waiters = 3
	start := make(chan struct{})
	answers := make(chan answer, waiters)
	for range waiters {
		go func() {
			<-start
			began := time.Now()
			raw, rerr, err := a.tryCall("events", map[string]any{"since": 0, "wait_ms": 2000})
			if err != nil {
				answers <- answer{err: &rpcError{Code: rpc.CodeInternal, Message: err.Error()}}
				return
			}
			var page trialsEventPage
			json.Unmarshal(raw, &page)
			answers <- answer{page: page, err: rerr, took: time.Since(began)}
		}()
	}
	close(start)
	time.Sleep(200 * time.Millisecond) // the waiters have posted their first snapshot and are parked

	// A read and an action beside the parked waits: neither may wait for their caps.
	began := time.Now()
	var st ServeStatus
	raw, rerr, err := a.tryCall("status", nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if rerr != nil {
		t.Fatalf("status: %d %s", rerr.Code, rerr.Message)
	}
	json.Unmarshal(raw, &st)
	if took := time.Since(began); took > time.Second {
		t.Fatalf("status took %s beside three empty events waits\n%s", took, steps.joined())
	}
	began = time.Now()
	if _, rerr, err = a.tryCall("chat", map[string]any{"type": "say", "text": "queue-check"}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if rerr != nil {
		t.Fatalf("chat: %d %s", rerr.Code, rerr.Message)
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("chat took %s beside three empty events waits\n%s", took, steps.joined())
	}

	// All three waits end at their own cap, together, not one after another.
	deadline := time.After(3 * time.Second)
	for range waiters {
		select {
		case ans := <-answers:
			if ans.err != nil {
				t.Fatalf("events: %d %s", ans.err.Code, ans.err.Message)
			}
			if len(ans.page.Events) != 0 {
				t.Fatalf("an empty wait answered %d rows", len(ans.page.Events))
			}
			if ans.took < 1500*time.Millisecond {
				t.Fatalf("an empty wait returned after %s, want its 2 s cap", ans.took)
			}
		case <-deadline:
			t.Fatalf("an empty events wait did not return with the others\n%s", steps.joined())
		}
	}
	a.stop(t)
	f.wait(t)
}
