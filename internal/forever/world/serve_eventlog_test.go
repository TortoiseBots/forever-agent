package world

// The serve event_log method and the act method over the fake world server: the kinds come from the session's own
// dispatch (not from the combat ring alone), and a script's steps answer the lines `forever act` prints.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ServeEventPage mirrors event_log's result for the tests.
type testEventPage struct {
	Events  []Event `json:"events"`
	Next    uint64  `json:"next"`
	More    bool    `json:"more"`
	Dropped int     `json:"dropped"`
}

// eventPage reads one page of the daemon's event log.
func eventPage(t *testing.T, a *serveAgent, since uint64, waitMS int) testEventPage {
	t.Helper()
	var page testEventPage
	json.Unmarshal(a.mustCall(t, "event_log", map[string]any{"since": since, "limit": 500, "wait_ms": waitMS}), &page)
	return page
}

// waitEvent waits for one event the match accepts above since, and returns it with the page it came from.
func waitEvent(t *testing.T, a *serveAgent, since uint64, match func(Event) bool) (Event, testEventPage) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		page := eventPage(t, a, since, 500)
		for _, e := range page.Events {
			if match(e) {
				return e, page
			}
		}
		since = page.Next
	}
	t.Fatalf("no matching event above %d", since)
	return Event{}, testEventPage{}
}

// TestEventLogKindsFromChatAndCombat: one say, one GM reply and one melee reach the log in that order with the kinds
// chat, system and melee. A log fed only from the combat ring has the melee and neither of the other two.
func TestEventLogKindsFromChatAndCombat(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSwingMelee = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	units := waitServeUnits(t, a)

	// Everything after the login burst.
	start := eventPage(t, a, 0, 0).Next

	a.mustCall(t, "chat", map[string]any{"type": "say", "text": "hello log"})
	chatEvent, _ := waitEvent(t, a, start, func(e Event) bool {
		return e.Kind == "chat" && e.Text == "hello log"
	})

	var gm ServeCommand
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": "test"}), &gm)
	systemEvent, _ := waitEvent(t, a, start, func(e Event) bool {
		return e.Kind == "system" && strings.Contains(e.Text, "SERVE-GM-ANSWER")
	})

	a.mustCall(t, "attack", map[string]any{"guid": units[0].GUID, "for_ms": 5000})
	meleeEvent, page := waitEvent(t, a, start, func(e Event) bool {
		return e.Kind == "melee" && e.Amount == 7
	})

	if !(chatEvent.Seq < systemEvent.Seq && systemEvent.Seq < meleeEvent.Seq) {
		t.Fatalf("the three events are out of order: chat %d, system %d, melee %d (%+v)",
			chatEvent.Seq, systemEvent.Seq, meleeEvent.Seq, page.Events)
	}
	// The combat copy carries both sides as the 0x text; a name is present only when this client's model knows one
	// (the fake never answers a creature query), as in the combat ring's own rows.
	for _, guid := range []string{meleeEvent.SourceGUID, meleeEvent.TargetGUID} {
		if !strings.HasPrefix(guid, "0x") || len(guid) != 34 {
			t.Fatalf("melee event %+v: guid %q", meleeEvent, guid)
		}
	}
	if !meleeEvent.Party {
		t.Fatalf("the melee row names this character but is not marked party: %+v", meleeEvent)
	}
	// The say row names its sender and carries the text.
	if chatEvent.Source == "" {
		t.Fatalf("chat event %+v", chatEvent)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeEventLogPagingAndWaits: the limits are enforced and a wait answers as soon as a row arrives.
func TestServeEventLogPagingAndWaits(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	a.mustFail(t, "event_log", map[string]any{"since": 0, "limit": -1}, -32003, "limit")
	a.mustFail(t, "event_log", map[string]any{"since": 0, "wait_ms": 2001}, -32003, "wait_ms")

	page := eventPage(t, a, 0, 0)
	if len(page.Events) == 0 || page.Next == 0 {
		t.Fatalf("the login burst wrote no event: %+v", page)
	}

	// A wait answers the next row instead of an empty page.
	start := page.Next
	done := make(chan testEventPage, 1)
	go func() { done <- eventPage(t, a, start, 2000) }()
	time.Sleep(100 * time.Millisecond)
	a.mustCall(t, "chat", map[string]any{"type": "say", "text": "wake the waiter"})
	select {
	case got := <-done:
		if len(got.Events) == 0 || got.Events[0].Kind != "chat" {
			t.Fatalf("the wait answered %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting read did not answer the new row")
	}
	a.stop(t)
	f.wait(t)
}

// TestEventLogAcrossReconnect: the ring outlives a dropped link: the history is still readable and the new session's
// events continue the same seq axis.
func TestEventLogAcrossReconnect(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 2
	fastReconnect(t, f, 200*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	before := eventPage(t, a, 0, 0)
	if len(before.Events) == 0 {
		t.Fatal("the first session wrote no event")
	}
	next := before.Next

	dropServingSocket(t, f)
	waitServeState(t, a, "reconnecting", steps)
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	// The history before the drop is still there, from seq 1.
	history := eventPage(t, a, 0, 0)
	if len(history.Events) == 0 || history.Events[0].Seq != 1 {
		t.Fatalf("the history after the reconnect starts at %+v", history.Events)
	}
	// The new session wrote its own login events above the reader's seq.
	after := eventPage(t, a, next, 0)
	if len(after.Events) == 0 {
		t.Fatalf("the re-entered session wrote no event above %d", next)
	}
	if after.Events[0].Seq <= next {
		t.Fatalf("the re-entry reused seq %d (reader was at %d)", after.Events[0].Seq, next)
	}
	a.stop(t)
	f.close()
	f.wait(t)
}

// TestServeActRunsSteps: act parses the caller's steps, runs them in order on the session and answers the lines
// `forever act` prints.
func TestServeActRunsSteps(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	var res ServeActResult
	json.Unmarshal(a.mustCall(t, "act", map[string]any{
		"steps": []string{"wait dur=20ms", "gm line=.tb about"},
	}), &res)
	if !res.OK {
		t.Fatalf("act failed: %+v", res)
	}
	byStep := map[string]string{}
	for _, line := range res.Lines {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "step" {
			byStep[f[1]] = strings.Join(f[2:], " ")
		}
	}
	if !strings.HasPrefix(byStep["1"], "wait ok") || !strings.HasPrefix(byStep["2"], "gm ok") {
		t.Fatalf("act lines %v", res.Lines)
	}
	// A step's own failure is a script result, not an RPC error: the lines say which step failed.
	json.Unmarshal(a.mustCall(t, "act", map[string]any{
		"steps": []string{"npc entry=65000", "wait dur=10ms"},
	}), &res)
	if res.OK {
		t.Fatalf("act whose first step failed answered ok: %+v", res)
	}
	joined := strings.Join(res.Lines, "\n")
	if !strings.Contains(joined, "step 1 npc fail") || !strings.Contains(joined, "no unit of entry 65000") {
		t.Fatalf("act lines:\n%s", joined)
	}
	if strings.Contains(joined, "step 2") {
		t.Fatalf("the run continued after a failed step:\n%s", joined)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeActReachesTheNewVerbs: the trainer and flight steps parse through serve's act like the older verbs, and a
// step's own failure (no current NPC) is the run's result, never an unknown-verb refusal.
func TestServeActReachesTheNewVerbs(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	for _, script := range []string{"trainer", "taxi-nodes", "taxi-status", "fly node=1", "learn spell=1"} {
		var res ServeActResult
		json.Unmarshal(a.mustCall(t, "act", map[string]any{"steps": []string{script}}), &res)
		if res.OK {
			t.Fatalf("%q succeeded without an NPC", script)
		}
		if len(res.Lines) == 0 || !strings.Contains(res.Lines[len(res.Lines)-1], "fail") {
			t.Fatalf("%q answered %v", script, res.Lines)
		}
		if strings.Contains(strings.Join(res.Lines, "\n"), "unknown act verb") {
			t.Fatalf("%q parsed as an unknown verb: %v", script, res.Lines)
		}
	}
	a.stop(t)
	f.wait(t)
}

// TestServeActBadStepSendsNothing: a script with one unparseable step is refused before anything is sent, even when
// the earlier steps are valid.
func TestServeActBadStepSendsNothing(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	f.mu.Lock()
	sayBefore, selBefore := len(f.serveGMLines), len(f.serveSelections)
	f.mu.Unlock()

	a.mustFail(t, "act", map[string]any{"steps": []string{"wait dur=10ms", "not-a-verb"}}, -32003, "unknown act verb")

	f.mu.Lock()
	sayAfter, selAfter := len(f.serveGMLines), len(f.serveSelections)
	f.mu.Unlock()
	if sayAfter != sayBefore || selAfter != selBefore {
		t.Fatalf("the refused script sent something (gm lines %d->%d, selections %d->%d)",
			sayBefore, sayAfter, selBefore, selAfter)
	}
	// The bounds are refused too.
	a.mustFail(t, "act", map[string]any{"steps": []string{"wait"}, "timeout_ms": 700000}, -32003, "timeout_ms")
	a.mustFail(t, "act", map[string]any{"steps": []string{}}, -32003, "step")
	a.stop(t)
	f.wait(t)
}
