package world

// The session event log's own tests: its page bounds, its overflow rule and its seq axis. The feed (chat, system,
// combat and the rest) is judged over the daemon in serve_eventlog_test.go.

import (
	"encoding/json"
	"testing"
	"time"
)

// TestEventLogPagingMoreDropped: a page is at most the limit, next is the last row read, more says another page
// follows, and dropped counts the rows that fell out of the ring between the reader's seq and the oldest it holds.
func TestEventLogPagingMoreDropped(t *testing.T) {
	var log EventLog
	for i := range 5 {
		log.Append(Event{Kind: "chat", Text: itoaTest(i)})
	}
	events, next, dropped, more := log.Since(0, 2)
	if len(events) != 2 || next != 2 || dropped != 0 || !more {
		t.Fatalf("first page: %d event(s) next=%d dropped=%d more=%v", len(events), next, dropped, more)
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("first page seqs %d, %d", events[0].Seq, events[1].Seq)
	}
	events, next, _, more = log.Since(next, 2)
	if len(events) != 2 || next != 4 || !more || events[0].Seq != 3 {
		t.Fatalf("second page: %d event(s) next=%d more=%v", len(events), next, more)
	}
	events, next, _, more = log.Since(next, 2)
	if len(events) != 1 || next != 5 || more {
		t.Fatalf("last page: %d event(s) next=%d more=%v", len(events), next, more)
	}
	// Caught up: nothing, the same seq back, and no claim of more.
	if events, got, _, more := log.Since(next, 2); len(events) != 0 || got != 5 || more {
		t.Fatalf("caught up: %d event(s) next=%d more=%v", len(events), got, more)
	}
	// A reader ahead of the log kept its cursor from another life of it (a daemon restart): it is told so (reset) and
	// read from the start, never left on an empty page. TestEventLogRestartMarker judges the marker itself.
	if events, got, _, _ := log.Since(99, 2); len(events) != 2 || got != 2 {
		t.Fatalf("ahead: %d event(s) next=%d, want the start of the log", len(events), got)
	}

	// The ring keeps EventRingSize events; a reader behind the oldest learns how many it lost.
	total := EventRingSize + 100
	for i := 5; i < total; i++ {
		log.Append(Event{Kind: "party"})
	}
	events, next, dropped, more = log.Since(0, EventPageMax)
	if dropped != 100 {
		t.Fatalf("dropped %d, want 100", dropped)
	}
	if len(events) != EventPageMax || events[0].Seq != 101 || !more {
		t.Fatalf("page %d event(s) from seq %d, more=%v", len(events), events[0].Seq, more)
	}
	if next != events[len(events)-1].Seq {
		t.Fatalf("next %d, last event %d", next, events[len(events)-1].Seq)
	}
}

// TestEventLogSeqAcrossReconnect: the ring belongs to the daemon, not to a link, so a reader's seq from before a
// reconnect still points into it and no seq is used twice.
func TestEventLogSeqAcrossReconnect(t *testing.T) {
	var log EventLog
	log.Append(Event{Kind: "session", Data: map[string]any{"state": "in_world"}})
	events, next, _, _ := log.Since(0, 10)
	if len(events) != 1 || next != 1 {
		t.Fatalf("before the reconnect: %d event(s) next=%d", len(events), next)
	}
	// The link dropped and the new session was handed the same ring.
	log.Append(Event{Kind: "session", Data: map[string]any{"state": "reconnected"}})
	events, next, dropped, _ := log.Since(next, 10)
	if len(events) != 1 || events[0].Seq != 2 || next != 2 || dropped != 0 {
		t.Fatalf("after the reconnect: %d event(s) next=%d dropped=%d", len(events), next, dropped)
	}
	// The old page is still readable history, not a hole.
	events, _, _, _ = log.Since(0, 10)
	if len(events) != 2 || events[0].Seq != 1 {
		t.Fatalf("history after the reconnect: %+v", events)
	}
}

// TestEventShape: an event answers the internal/events keys, and a GUID is 0x plus 32 hex digits.
func TestEventShape(t *testing.T) {
	var log EventLog
	log.Append(Event{
		Kind: "melee", Source: "Atank", SourceGUID: "0x00000000000000000000000000000011",
		Target: "Testmob", SpellID: 5019, Amount: 12, Party: true,
		Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	events, _, _, _ := log.Since(0, 1)
	if len(events) != 1 {
		t.Fatalf("%d events", len(events))
	}
	b, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"seq", "time", "mono_ms", "kind", "source", "source_guid", "target", "spell_id", "amount", "party"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("event %s lacks %q", b, key)
		}
	}
	if got["source_guid"] != "0x00000000000000000000000000000011" {
		t.Fatalf("source_guid %v", got["source_guid"])
	}
	if _, ok := got["target_guid"]; ok {
		t.Fatalf("an empty guid carries no target_guid key: %s", b)
	}
}

// itoaTest is a tiny decimal formatter for the ring tests' texts.
func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
