package driver

// The core-gains round's driver tests: the pushed threat table replaces the `.tb threat` poll at 10 s, and the
// encounter packets become run events and watched-run records.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/trace"
)

// fiveMinuteFight plays one group whose mob dies at five minutes and returns the fake's `.tb threat` count.
func fiveMinuteFight(t *testing.T, push bool) (*Fake, Result) {
	t.Helper()
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	f.PushThreat = push
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "pull" {
			f.Fight(arg, 5*time.Minute)
		}
	}
	res := play(t, run)
	if res.Stop != "clear" {
		t.Fatalf("push=%v: stop %q, want clear (states %v)", push, res.Stop, res.States)
	}
	return f, res
}

// TestThreatTableSavesPolls: the same five-minute fight once with the client's pushed table and once without it.
// The table ticks read no command at all; the module read is one per 10 s instead of one per 2 s tick.
func TestThreatTableSavesPolls(t *testing.T) {
	fPushed, _ := fiveMinuteFight(t, true)
	fPolled, _ := fiveMinuteFight(t, false)
	pushed, polled := fPushed.ThreatCalls, fPolled.ThreatCalls
	t.Logf("five-minute fight: %d ticks at 2 s; .tb threat reads %d without the pushed table, %d with it (saved %d)",
		(5*time.Minute)/(2*time.Second), polled, pushed, polled-pushed)
	if polled < 140 {
		t.Fatalf("the polled fight made only %d threat reads, want about one per 2 s tick", polled)
	}
	if limit := int((5 * time.Minute) / (10 * time.Second)); pushed > limit+2 {
		t.Fatalf("the pushed fight made %d threat reads, want at most one per 10 s", pushed)
	}
	if saved := polled - pushed; saved < 100 {
		t.Fatalf("the pushed table saved %d reads, want about 120", saved)
	}
}

// TestThreatFallbackWhenNothingIsPushed: without a pushed list every call polls, so the fallback is the old 2 s
// path (the fight above's polled run is that path; this test pins the count of the one read a pull's wait needs).
func TestThreatFallbackWhenNothingIsPushed(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "pull" {
			f.Fight(arg, 10*time.Second)
		}
	}
	if res := play(t, run); res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	if f.ThreatCalls < 3 {
		t.Fatalf("%d threat reads in a 10 s fight without pushes, want one per tick", f.ThreatCalls)
	}
}

// TestEncounterEventsOnTheTrace: a start and a successful end become boss_pull and boss_kill at their own times.
func TestEncounterEventsOnTheTrace(t *testing.T) {
	dir := t.TempDir()
	route := testRoute(t, oneGroupPlan())
	cfg := scenarioConfig("none")
	cfg.Out = dir
	f := NewFake(route, cfg)
	run := New(cfg, route, f, f, f, f.Clock)
	rec, err := NewRecorder(dir, "r-gains", route.Name, route.Map, 1, f.Start, run.partyMembers())
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	run.SetRecorder(rec)
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "pull" {
			f.Fight(arg, 60*time.Second)
		}
	}
	f.PushEncounter(5*time.Second, EncounterEvent{Kind: "start", EncounterID: 1234, Difficulty: 2, GroupSize: 20})
	f.PushEncounter(55*time.Second, EncounterEvent{Kind: "end", EncounterID: 1234, Difficulty: 2, GroupSize: 20,
		DurationMS: 50000, Success: true})
	res := play(t, run)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	tf, err := trace.Read(filepath.Join(dir, "r-gains.jsonl"))
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	var pull, kill *trace.Event
	for i := range tf.Events {
		switch tf.Events[i].Ev {
		case "boss_pull":
			pull = &tf.Events[i]
		case "boss_kill":
			kill = &tf.Events[i]
		}
	}
	if pull == nil || kill == nil {
		t.Fatalf("trace events: %+v", tf.Events)
	}
	if pull.T > kill.T {
		t.Fatalf("the pull is at %d ms, after the kill at %d", pull.T, kill.T)
	}
	if got := pull.D["encounter"]; got != float64(1234) && got != 1234 {
		t.Fatalf("pull encounter %v", got)
	}
	// The single group's mob dies at 60 s: the kill event's own end is on the trace.
	if kill.T < pull.T+40*1000 {
		t.Fatalf("the kill at %d ms is not the scripted 50 s after the pull at %d", kill.T, pull.T)
	}
}

// TestEncounterWipeToldApart: an end with success false is boss_wipe, never boss_kill.
func TestEncounterWipeToldApart(t *testing.T) {
	dir := t.TempDir()
	route := testRoute(t, oneGroupPlan())
	cfg := scenarioConfig("none")
	cfg.Out = dir
	cfg.Limits = DefaultLimits()
	f := NewFake(route, cfg)
	run := New(cfg, route, f, f, f, f.Clock)
	rec, err := NewRecorder(dir, "r-wipe", route.Name, route.Map, 1, f.Start, run.partyMembers())
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	run.SetRecorder(rec)
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "pull" {
			f.Fight(arg, 30*time.Second)
		}
	}
	f.PushEncounter(5*time.Second, EncounterEvent{Kind: "start", EncounterID: 77, GroupSize: 5})
	f.PushEncounter(25*time.Second, EncounterEvent{Kind: "end", EncounterID: 77, GroupSize: 5, Success: false})
	if res := play(t, run); res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	tf, err := trace.Read(filepath.Join(dir, "r-wipe.jsonl"))
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	var wipes, kills int
	for _, ev := range tf.Events {
		switch ev.Ev {
		case "boss_wipe":
			wipes++
		case "boss_kill":
			kills++
		}
	}
	if wipes != 1 || kills != 0 {
		t.Fatalf("%d boss_wipe and %d boss_kill event(s), want one wipe and no kill", wipes, kills)
	}
}

// TestWatchEncounterRecord: a watched run records the pulls and kills and stops reading `.tb instance` while the
// packets flow; the forced read at the end still happens.
func TestWatchEncounterRecord(t *testing.T) {
	cfg := watchConfig()
	run, f, _ := newWatch(t, cfg)
	f.PushEncounter(4*time.Second, EncounterEvent{Kind: "start", EncounterID: 1234, Difficulty: 2, GroupSize: 20})
	f.PushEncounter(24*time.Second, EncounterEvent{Kind: "end", EncounterID: 1234, Difficulty: 2, GroupSize: 20,
		DurationMS: 20000, Success: true})
	f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 40 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done", res.Stop)
	}
	if res.Summary.Watch == nil {
		t.Fatal("no watch summary")
	}
	enc := res.Summary.Watch.Encounters
	if len(enc) != 2 {
		t.Fatalf("%d encounter records, want the pull and the kill: %+v", len(enc), enc)
	}
	if enc[0].Kind != "start" || enc[0].EncounterID != 1234 || enc[0].GroupSize != 20 {
		t.Fatalf("start record %+v", enc[0])
	}
	if enc[1].Kind != "end" || !enc[1].Success || enc[1].DurationS != 20 {
		t.Fatalf("end record %+v", enc[1])
	}
	if enc[1].AtS <= enc[0].AtS {
		t.Fatalf("the kill at %.1fs is not after the pull at %.1fs", enc[1].AtS, enc[0].AtS)
	}
	// The start read and the forced final read remain; the minute cadence is the fallback and is skipped while the
	// encounter packets flow.
	reads := 0
	for _, o := range f.Orders() {
		if o.Verb == "action" && strings.HasSuffix(o.Arg, " info") {
			reads++
		}
	}
	if reads != 2 {
		t.Fatalf("%d instance report read(s), want the start read and the final read", reads)
	}
}

// TestWatchBossReadWithoutEncounters: an instance whose script sends nothing keeps the old reads.
func TestWatchBossReadWithoutEncounters(t *testing.T) {
	cfg := watchConfig()
	run, f, _ := newWatch(t, cfg)
	f.ScriptRun(FakeRunScript{Steps: 20, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 130 * time.Second})
	if res := run.Watch(context.Background()); res.Stop != "done" {
		t.Fatalf("stop %q, want done", res.Stop)
	}
	reads := 0
	for _, o := range f.Orders() {
		if o.Verb == "action" && strings.HasSuffix(o.Arg, " info") {
			reads++
		}
	}
	if reads < 3 {
		t.Fatalf("%d instance report read(s) in a 130 s watch with no encounter packet, want the cadence and the end", reads)
	}
}
