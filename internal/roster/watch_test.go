package roster

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var baseTime = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func sample(bots ...Bot) *Roster { return &Roster{Bots: bots} }

func testBot(name, guid, group, state string, x, y, z float64) Bot {
	return Bot{Name: name, GUID: Text(guid), Group: Text(group), Kind: KindPool, State: Text(state), X: x, Y: y, Z: z}
}

func kinds(events []Event) []EventKind {
	out := make([]EventKind, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

func TestTrackerReportsADeathOnce(t *testing.T) {
	tr := NewTracker(Config{})
	b := func(state string) *Roster { return sample(testBot("Ember", "1", "", state, 0, 0, 0)) }

	if ev := tr.Observe(baseTime, b(StateIdle)); len(ev) != 0 {
		t.Fatalf("baseline produced %v", kinds(ev))
	}
	ev := tr.Observe(baseTime.Add(time.Second), b(StateDead))
	if len(ev) != 1 || ev[0].Kind != EventDeath || ev[0].Bot != "Ember" {
		t.Fatalf("death = %+v, want one death for Ember", ev)
	}
	if ev := tr.Observe(baseTime.Add(2*time.Second), b(StateDead)); len(ev) != 0 {
		t.Fatalf("a still dead bot produced %v, want nothing", kinds(ev))
	}
	if ev := tr.Observe(baseTime.Add(3*time.Second), b(StateIdle)); len(ev) != 0 {
		t.Fatalf("a resurrected bot produced %v, want nothing", kinds(ev))
	}
	ev = tr.Observe(baseTime.Add(4*time.Second), b(StateDead))
	if len(ev) != 1 || ev[0].Kind != EventDeath {
		t.Fatalf("the second death = %+v, want one death", ev)
	}
}

func TestTrackerDoesNotReportADeathAlreadyInProgress(t *testing.T) {
	tr := NewTracker(Config{})
	if ev := tr.Observe(baseTime, sample(testBot("Ember", "1", "", StateDead, 0, 0, 0))); len(ev) != 0 {
		t.Fatalf("the baseline produced %v, want nothing", kinds(ev))
	}
}

func TestTrackerStuckAfterTheThreshold(t *testing.T) {
	tr := NewTracker(Config{StuckAfter: 10 * time.Second})
	b := func(x float64) *Roster { return sample(testBot("Ember", "1", "", StateMoving, x, 0, 0)) }

	tr.Observe(baseTime, b(0))
	if ev := tr.Observe(baseTime.Add(5*time.Second), b(0)); len(ev) != 0 {
		t.Fatalf("5s produced %v", kinds(ev))
	}
	if ev := tr.Observe(baseTime.Add(10*time.Second), b(0)); len(ev) != 1 || ev[0].Kind != EventStuck {
		t.Fatalf("10s = %+v, want one stuck event", ev)
	}
	if ev := tr.Observe(baseTime.Add(15*time.Second), b(0)); len(ev) != 0 {
		t.Fatalf("15s produced %v, want nothing (reported once)", kinds(ev))
	}
	// A real move re-arms the rule; the next long standstill fires again.
	tr.Observe(baseTime.Add(16*time.Second), b(5))
	if ev := tr.Observe(baseTime.Add(20*time.Second), b(5)); len(ev) != 0 {
		t.Fatalf("20s produced %v, want nothing (the timer restarted)", kinds(ev))
	}
	if ev := tr.Observe(baseTime.Add(26*time.Second), b(5)); len(ev) != 1 || ev[0].Kind != EventStuck {
		t.Fatalf("26s = %+v, want the second stuck event", ev)
	}
}

func TestTrackerStuckIgnoresJitterAndIdleBots(t *testing.T) {
	tr := NewTracker(Config{StuckAfter: 10 * time.Second})
	tr.Observe(baseTime, sample(testBot("Ember", "1", "", StateMoving, 0, 0, 0)))
	if ev := tr.Observe(baseTime.Add(10*time.Second), sample(testBot("Ember", "1", "", StateMoving, 0.2, 0, 0))); len(ev) != 1 {
		t.Fatalf("a 0.2-yard jitter = %v, want the bot counted as standing still", kinds(ev))
	}

	idle := NewTracker(Config{StuckAfter: 10 * time.Second})
	idle.Observe(baseTime, sample(testBot("Cinder", "2", "", StateIdle, 0, 0, 0)))
	if ev := idle.Observe(baseTime.Add(time.Hour), sample(testBot("Cinder", "2", "", StateIdle, 0, 0, 0))); len(ev) != 0 {
		t.Fatalf("an idle bot produced %v, want nothing", kinds(ev))
	}
}

func TestTrackerLoneCombatNeedsTheGraceAndReArms(t *testing.T) {
	tr := NewTracker(Config{LoneCombatAfter: 5 * time.Second})
	allCombat := sample(
		testBot("Ember", "1", "g", StateCombat, 0, 0, 0),
		testBot("Cinder", "2", "g", StateCombat, 1, 0, 0),
		testBot("Ash", "3", "g", StateCombat, 2, 0, 0),
	)
	tr.Observe(baseTime, allCombat)

	lone := sample(
		testBot("Ember", "1", "g", StateCombat, 0, 0, 0),
		testBot("Cinder", "2", "g", StateIdle, 1, 0, 0),
		testBot("Ash", "3", "g", StateIdle, 2, 0, 0),
	)
	if ev := tr.Observe(baseTime.Add(2*time.Second), lone); len(ev) != 0 {
		t.Fatalf("2s alone produced %v, want the grace to hold", kinds(ev))
	}
	ev := tr.Observe(baseTime.Add(7*time.Second), lone)
	if len(ev) != 1 || ev[0].Kind != EventLoneCombat || ev[0].Bot != "Ember" {
		t.Fatalf("7s = %+v, want one lone fight for Ember", ev)
	}
	if ev := tr.Observe(baseTime.Add(9*time.Second), lone); len(ev) != 0 {
		t.Fatalf("a continuing lone fight produced %v, want nothing (reported once)", kinds(ev))
	}

	// A group mate joins: the condition clears, so the next lone fight is reported again.
	joined := sample(
		testBot("Ember", "1", "g", StateCombat, 0, 0, 0),
		testBot("Cinder", "2", "g", StateCombat, 1, 0, 0),
		testBot("Ash", "3", "g", StateIdle, 2, 0, 0),
	)
	tr.Observe(baseTime.Add(10*time.Second), joined)
	tr.Observe(baseTime.Add(11*time.Second), lone) // Cinder leaves again: the grace restarts
	if ev := tr.Observe(baseTime.Add(15*time.Second), lone); len(ev) != 0 {
		t.Fatalf("4s into the second lone fight produced %v", kinds(ev))
	}
	if ev := tr.Observe(baseTime.Add(16*time.Second), lone); len(ev) != 1 || ev[0].Kind != EventLoneCombat {
		t.Fatalf("the second lone fight = %+v, want one event", ev)
	}
}

func TestTrackerLoneCombatSkipsGroupsItCannotJudge(t *testing.T) {
	cases := map[string]*Roster{
		"solo":     sample(testBot("Ember", "1", "", StateCombat, 0, 0, 0)),
		"one bot":  sample(testBot("Ember", "1", "g", StateCombat, 0, 0, 0)),
		"two bots": sample(testBot("Ember", "1", "g", StateCombat, 0, 0, 0), testBot("Cinder", "2", "g", StateCombat, 1, 0, 0)),
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			tr := NewTracker(Config{LoneCombatAfter: time.Second})
			tr.Observe(baseTime, r)
			if ev := tr.Observe(baseTime.Add(time.Minute), r); len(ev) != 0 {
				t.Fatalf("produced %v, want nothing", kinds(ev))
			}
		})
	}
}

func TestTrackerFarFromGroup(t *testing.T) {
	tr := NewTracker(Config{GroupDistance: 60})
	at := func(x float64) *Roster {
		return sample(
			testBot("Ember", "1", "g", StateIdle, x, 0, 0),
			testBot("Cinder", "2", "g", StateIdle, 100, 0, 0),
		)
	}
	ev := tr.Observe(baseTime, at(0))
	if len(ev) != 2 {
		t.Fatalf("100 yards apart = %+v, want both bots reported far from the other", ev)
	}
	for _, e := range ev {
		if e.Kind != EventFarFromGroup {
			t.Fatalf("event = %+v", e)
		}
		if e.Distance < 99.9 || e.Distance > 100.1 {
			t.Fatalf("%s distance = %v, want 100", e.Bot, e.Distance)
		}
	}
	if ev := tr.Observe(baseTime.Add(time.Second), at(0)); len(ev) != 0 {
		t.Fatalf("still far produced %v, want nothing (reported once)", kinds(ev))
	}
	// 50 yards is inside the threshold but above the re-arm line: the flag stays quiet.
	if ev := tr.Observe(baseTime.Add(2*time.Second), at(50)); len(ev) != 0 {
		t.Fatalf("50 yards produced %v", kinds(ev))
	}
	// 40 yards re-arms it; the next time the bot strays, the flag fires again.
	tr.Observe(baseTime.Add(3*time.Second), at(60))
	if ev := tr.Observe(baseTime.Add(4*time.Second), at(0)); len(ev) != 2 {
		t.Fatalf("the second stray = %+v, want both bots reported again", ev)
	}
}

func TestTrackerFarFromGroupUsesTheGroupCentre(t *testing.T) {
	tr := NewTracker(Config{GroupDistance: 60})
	spread := sample(
		testBot("Ash", "3", "g", StateIdle, 100, 0, 0),
		testBot("Cinder", "2", "g", StateIdle, 200, 0, 0),
		testBot("Ember", "1", "g", StateIdle, 0, 0, 0),
	)
	ev := tr.Observe(baseTime, spread)
	if len(ev) != 2 {
		t.Fatalf("events = %+v, want the two ends, 150 yards from their mates' centre", ev)
	}
	for _, e := range ev {
		if e.Kind != EventFarFromGroup || e.Bot == "Ash" {
			t.Fatalf("event = %+v, want Cinder and Ember, not the bot between them", e)
		}
		if e.Distance < 149.9 || e.Distance > 150.1 {
			t.Fatalf("%s distance = %v, want 150", e.Bot, e.Distance)
		}
	}
}

func TestTrackerFarFromGroupIgnoresAnotherInstance(t *testing.T) {
	tr := NewTracker(Config{GroupDistance: 60})
	r := sample(
		testBot("Ember", "1", "g", StateIdle, 0, 0, 0),
		testBot("Cinder", "2", "g", StateIdle, 500, 0, 0),
	)
	r.Bots[0].Instance = ""
	r.Bots[1].Instance = "1"
	if ev := tr.Observe(baseTime, r); len(ev) != 0 {
		t.Fatalf("events = %+v, want nothing: the only mate is in another instance", kinds(ev))
	}
}

func TestTrackerForgetsBotsThatLeft(t *testing.T) {
	tr := NewTracker(Config{})
	both := sample(
		testBot("Ember", "1", "g", StateIdle, 0, 0, 0),
		testBot("Cinder", "2", "g", StateIdle, 1, 0, 0),
	)
	tr.Observe(baseTime, both)
	tr.Observe(baseTime.Add(time.Second), sample(both.Bots[0]))
	// Cinder comes back dead: without the baseline it is a new bot, so its death is not a
	// change the watch saw.
	ev := tr.Observe(baseTime.Add(2*time.Second), sample(
		both.Bots[0],
		testBot("Cinder", "2", "g", StateDead, 1, 0, 0),
	))
	for _, e := range ev {
		if e.Bot == "Cinder" && e.Kind == EventDeath {
			t.Fatalf("a returning bot was reported dead without a live baseline")
		}
	}
}

type scriptedSource struct {
	samples []*Roster
	err     error
	calls   int
}

func (s *scriptedSource) Roster(context.Context) (*Roster, error) {
	if s.err != nil {
		return nil, s.err
	}
	r := s.samples[s.calls]
	if s.calls < len(s.samples)-1 {
		s.calls++
	}
	return r, nil
}

func TestMonitorPollsAndFoldsASummary(t *testing.T) {
	src := &scriptedSource{samples: []*Roster{
		sample(testBot("Ember", "1", "", StateIdle, 0, 0, 0)),
		sample(testBot("Ember", "1", "", StateDead, 0, 0, 0)),
	}}
	m := NewMonitor(src, Config{})
	events, err := m.Poll(context.Background(), baseTime)
	if err != nil || len(events) != 0 {
		t.Fatalf("first poll = %v, %v", kinds(events), err)
	}
	events, err = m.Poll(context.Background(), baseTime.Add(time.Second))
	if err != nil || len(events) != 1 || events[0].Kind != EventDeath {
		t.Fatalf("second poll = %+v, %v", events, err)
	}
	sum := NewSummary()
	sum.EventsOf(events)
	if sum.Deaths["1"] != 1 {
		t.Fatalf("summary deaths = %v", sum.Deaths)
	}
	if !strings.Contains(sum.String(), "deaths 1") {
		t.Fatalf("summary string = %q", sum.String())
	}

	var nilMonitor *Monitor
	if _, err := nilMonitor.Poll(context.Background(), baseTime); err == nil {
		t.Fatal("a nil monitor polled without an error")
	}
	failing := &scriptedSource{err: errors.New("read failed")}
	if _, err := NewMonitor(failing, Config{}).Poll(context.Background(), baseTime); err == nil {
		t.Fatal("a failing source was not reported")
	}
}
