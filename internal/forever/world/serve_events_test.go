package world

// The events handler's own paging rules, without a socket: 5,000 rows against the 4,096-row ring.

import (
	"context"
	"testing"
	"time"
)

func TestServeEventsPaging(t *testing.T) {
	// 5,000 rows cross the 4,096-row ring: the first page starts at the oldest row still held and says how many fell
	// out above the caller's seq.
	s := newSession(Options{Logf: func(string, ...any) {}}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	for range 5000 {
		s.CombatLog().Append(CombatRow{Kind: "melee"})
	}
	read := func(p serveEventsParams) ServeEventsPage {
		t.Helper()
		limit, _, err := serveEventsBounds(p)
		if err != nil {
			t.Fatal(err)
		}
		page, _ := s.serveEventsPage(p.Since, limit)
		return page
	}
	page := read(serveEventsParams{})
	// 5,000 rows against a 4,096-row ring: the page starts at the oldest row still held (905) and the 904 above
	// the caller's seq are the loss it reports.
	if len(page.Events) != CombatPageMax || page.Oldest != 905 || page.Dropped != 904 || page.Next != 1904 {
		t.Fatalf("first page: %d rows oldest=%d dropped=%d next=%d", len(page.Events), page.Oldest, page.Dropped, page.Next)
	}
	if page.Events[0].Seq != 905 || page.Events[len(page.Events)-1].Seq != 1904 {
		t.Fatalf("first page seqs %d..%d", page.Events[0].Seq, page.Events[len(page.Events)-1].Seq)
	}
	// A limit above the page bound is clamped, never refused.
	if page = read(serveEventsParams{Since: page.Next, Limit: 5000}); len(page.Events) != CombatPageMax || page.Dropped != 0 {
		t.Fatalf("clamped page: %d rows dropped=%d", len(page.Events), page.Dropped)
	}
	next := page.Next
	for next < 5000 {
		page = read(serveEventsParams{Since: next})
		next = page.Next
	}
	if next != 5000 {
		t.Fatalf("the pages end at %d, want 5000", next)
	}
}

func TestServeEventsBounds(t *testing.T) {
	if _, _, err := serveEventsBounds(serveEventsParams{Limit: -1}); err == nil {
		t.Fatal("a negative limit was accepted")
	}
	if _, _, err := serveEventsBounds(serveEventsParams{WaitMS: 2001}); err == nil {
		t.Fatal("a wait_ms above 2000 was accepted")
	}
	if _, _, err := serveEventsBounds(serveEventsParams{WaitMS: -1}); err == nil {
		t.Fatal("a negative wait_ms was accepted")
	}
	if limit, _, err := serveEventsBounds(serveEventsParams{Limit: 0}); err != nil || limit != CombatPageMax {
		t.Fatalf("limit 0 answered %d, %v; want the page maximum", limit, err)
	}
	if limit, _, err := serveEventsBounds(serveEventsParams{Limit: 5000}); err != nil || limit != CombatPageMax {
		t.Fatalf("limit 5000 answered %d, %v; want the page maximum", limit, err)
	}
}

// TestServeEventsWaitOffTheLoop proves the wait itself never runs on the session goroutine: while one empty
// long-poll waits, an unrelated call is served, and a row appended to the ring ends the wait at once.
func TestServeEventsWaitOffTheLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st := &serveState{calls: make(chan *serveCall, 8), done: make(chan struct{})}
	s := newSession(Options{}, &Report{Counts: make(map[string]int)}, ctx)
	defer s.pumpCancel()
	st.install(s, 1)
	st.setState(serveOnline, "")
	go func() {
		for {
			select {
			case c := <-st.calls:
				st.run(c)
			case <-st.done:
				return
			}
		}
	}()
	defer close(st.done)

	got := make(chan ServeEventsPage, 1)
	go func() {
		page, err := st.serveEvents(ctx, serveEventsParams{WaitMS: 1500})
		if err != nil {
			t.Errorf("the events wait: %v", err)
		}
		got <- page
	}()
	time.Sleep(150 * time.Millisecond)

	// A call posted beside the wait is served at once, not after its cap.
	start := time.Now()
	if _, err := st.call(ctx, func(*session) (any, error) { return nil, nil }); err != nil {
		t.Fatalf("a call beside an empty events wait: %v", err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("a call beside an empty events wait took %s", took)
	}

	// A new row is the wait's own signal: it returns the row long before the cap.
	s.CombatLog().Append(CombatRow{Kind: "melee"})
	select {
	case page := <-got:
		if len(page.Events) != 1 || page.Events[0].Kind != "melee" {
			t.Fatalf("the wait answered %+v, want the one appended row", page)
		}
	case <-time.After(time.Second):
		t.Fatal("a row did not wake the waiting events call")
	}
}
