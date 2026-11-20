package reportcard

// The card over scripted runs in the fake world: a clear, a stop at a pack after re-pulls, a wipe with its rescue,
// a watchdog pause, and a saved record read offline; then the comparison of two cards.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/driver"
)

const cardPlan = `{
 "version": 1, "name": "cardplan", "map": 389,
 "entrance": {"x": 0, "y": 0, "z": 0},
 "legs": [{
   "name": "tunnel",
   "to": {"x": 0, "y": 40, "z": 0},
   "pull_groups": [{"entries": [100], "positions": [{"x": 0, "y": 100, "z": 0}], "radius": 10}]
 }]
}`

func cardRoute(t *testing.T) *driver.Route {
	t.Helper()
	p, err := dungeon.Parse("cardplan", []byte(cardPlan))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	r, err := driver.NewRoute(p)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	return r
}

// playWatch runs one scripted watch and returns its result.
func playWatch(t *testing.T, sc driver.FakeRunScript) driver.Result {
	t.Helper()
	route := cardRoute(t)
	lim := driver.DefaultLimits()
	cfg := driver.Config{
		Leader: "Tank", Commander: "Lead", Enter: "none", Watch: true,
		Limits: lim, Watchdog: lim.Watchdog, Poll: lim.WatchPoll,
		Logf: func(string, ...any) {},
	}
	f := driver.NewFake(route, cfg)
	cfg.Party = f.PartyNames()
	run := driver.New(cfg, route, f, f, f, f.Clock)
	rec, err := driver.NewWatchRecorder(t.TempDir(), "r-card", route.Name, route.Map, 1, f.Start, run.PartyMembers())
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	run.SetRecorder(rec)
	f.ScriptRun(sc)
	return run.Watch(context.Background())
}

// flagIDs is the card's flag ids in order.
func flagIDs(c Card) []string {
	out := make([]string, 0, len(c.Flags))
	for _, f := range c.Flags {
		out = append(out, f.ID)
	}
	return out
}

func hasFlag(c Card, id string) bool {
	for _, f := range c.Flags {
		if f.ID == id {
			return true
		}
	}
	return false
}

func TestCardClear(t *testing.T) {
	res := playWatch(t, driver.FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
	c := Build(res.Summary, DefaultThresholds())
	if c.Verdict != "CLEARED" || c.VerdictText != "CLEARED" {
		t.Fatalf("verdict %+v", c)
	}
	if len(c.Legs) != 1 || c.Legs[0].Name != "tunnel" || c.Legs[0].Seconds == 0 {
		t.Fatalf("legs %+v", c.Legs)
	}
	if c.Legs[0].Pulls == 0 || c.Legs[0].Repulls != 0 {
		t.Fatalf("pulls %+v", c.Legs[0])
	}
	for _, id := range []string{"repulls", "watchdog-pause", "rescue", "uncleared-pause", "pack-deaths", "member-deaths"} {
		if hasFlag(c, id) {
			t.Fatalf("clear run has flag %q: %s", id, c.Text())
		}
	}
	if !hasFlag(c, "stop-reason") {
		t.Fatalf("no stop-reason flag: %s", c.Text())
	}
	if !strings.Contains(c.Text(), "verdict: CLEARED") || !strings.Contains(c.Text(), "leg 0 \"tunnel\"") {
		t.Fatalf("text:\n%s", c.Text())
	}
}

func TestCardRepullStop(t *testing.T) {
	// The run reaches step 3, cannot clear that pack, and stops there after six pulls of it.
	res := playWatch(t, driver.FakeRunScript{
		Steps: 12, Pulls: 5, ProgressEvery: 2 * time.Second,
		HoldAt: 3, StopAt: 20 * time.Second, StopWhy: "wipe: the pack would not die",
	})
	c := Build(res.Summary, DefaultThresholds())
	if c.Verdict != "STOPPED" {
		t.Fatalf("verdict %q: %+v", c.Verdict, c)
	}
	if !strings.Contains(c.VerdictText, `STOPPED AT leg 0 "tunnel"/step 3`) ||
		!strings.Contains(c.VerdictText, "wipe: the pack would not die") {
		t.Fatalf("verdict text %q", c.VerdictText)
	}
	if !hasFlag(c, "repulls") {
		t.Fatalf("no re-pulls flag: %s", c.Text())
	}
	got := c.Legs[0]
	if got.Pulls < 4 || got.Repulls < 3 {
		t.Fatalf("leg %+v", got)
	}
	if !strings.Contains(c.Text(), "re-pulled") || !strings.Contains(c.Text(), "event") && len(c.Flags) == 0 {
		t.Fatalf("text:\n%s", c.Text())
	}
}

func TestCardDeaths(t *testing.T) {
	// The tank dies twice to the same pack and a third time later; nobody else dies.
	res := playWatch(t, driver.FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 40 * time.Second,
		Deaths: []driver.FakeDeath{
			{After: 4 * time.Second, Unit: 0x100}, {After: 5 * time.Second, Unit: 0x100},
			{After: 10 * time.Second, Unit: 0x100},
		},
	})
	c := Build(res.Summary, DefaultThresholds())
	if len(c.DeathLog) != 3 || c.DeathLog[0].Who != "tank" {
		t.Fatalf("death log %+v", c.DeathLog)
	}
	if !hasFlag(c, "pack-deaths") || !hasFlag(c, "member-deaths") {
		t.Fatalf("flags %v:\n%s", flagIDs(c), c.Text())
	}
	var pack Flag
	for _, f := range c.Flags {
		if f.ID == "pack-deaths" {
			pack = f
		}
	}
	if !strings.Contains(pack.Text, "tank 2 times") || len(pack.Evidence) != 2 {
		t.Fatalf("pack flag %+v", pack)
	}
	if !strings.Contains(c.Text(), "leg 0/step 1 t=4s tank") {
		t.Fatalf("text:\n%s", c.Text())
	}
}

func TestCardWipeRescue(t *testing.T) {
	res := playWatch(t, driver.FakeRunScript{
		Steps: 30, Pulls: 10, ProgressEvery: 5 * time.Second,
		WipeAt: 20 * time.Second, DoneAt: 400 * time.Second,
	})
	c := Build(res.Summary, DefaultThresholds())
	if !hasFlag(c, "rescue") {
		t.Fatalf("no rescue flag: %v\n%s", flagIDs(c), c.Text())
	}
	if len(c.PauseLog) != 1 || c.PauseLog[0].Cleared != true {
		t.Fatalf("pauses %+v", c.PauseLog)
	}
	if !strings.Contains(c.Text(), "practice rescue at leg 0/step") {
		t.Fatalf("text:\n%s", c.Text())
	}
}

func TestCardWatchdog(t *testing.T) {
	res := playWatch(t, driver.FakeRunScript{Silent: true})
	c := Build(res.Summary, DefaultThresholds())
	if !hasFlag(c, "watchdog-pause") || !hasFlag(c, "uncleared-pause") {
		t.Fatalf("flags %v:\n%s", flagIDs(c), c.Text())
	}
	if c.Verdict != "STOPPED" || !strings.Contains(c.VerdictText, "(watchdog)") {
		t.Fatalf("verdict %q", c.VerdictText)
	}
	if !strings.Contains(c.Text(), `"watchdog no new event"`) {
		t.Fatalf("text:\n%s", c.Text())
	}
}

// TestCardSavedRecord reads a record from disk: the card and its JSON work offline, including the plan's recorded
// leg time, which the scripted plans do not carry.
func TestCardSavedRecord(t *testing.T) {
	sum := driver.RunSummary{
		Schema: driver.SummarySchema, Version: driver.SummaryVersion, RunID: "r-saved", Plan: "hall",
		Mode: "watch", Wipes: 1, Deaths: 4, Practice: 1, DurationS: 1500, StopReason: "stopped",
		Watch: &driver.RunWatch{
			State: "stopped", Step: 12, Steps: 40, Leg: 0, LegName: "hall", Pull: 13, Pulls: 20,
			Deaths: 4, Wipes: 1, Pauses: 2, ElapsedS: 1400, Why: "wipe: the pack would not die",
			Rescues: 1,
			Legs: []driver.LegRecord{
				{Leg: 0, Name: "hall", FirstS: 0, LastS: 1400, Seconds: 1200, Pulls: 13, Steps: 40,
					Deaths: 4, Wipes: 1, Pauses: 2, PlanS: 300},
			},
			StepLog: []driver.StepRecord{
				{Leg: 0, Step: 12, Pulls: 4, Deaths: 3, PullEvents: []driver.PullNote{
					{Seq: 10, AtS: 100}, {Seq: 11, AtS: 130}, {Seq: 12, AtS: 160}, {Seq: 13, AtS: 190},
				}},
			},
			DeathLog: []driver.DeathRecord{
				{AtS: 110, Leg: 0, Step: 12, Unit: 0x100, Who: "tank"},
				{AtS: 140, Leg: 0, Step: 12, Unit: 0x100, Who: "tank"},
				{AtS: 170, Leg: 0, Step: 12, Unit: 0x100, Who: "tank"},
				{AtS: 200, Leg: 0, Step: 12, Unit: 0x101, Who: "healer"},
			},
			PauseLog: []driver.PauseNote{
				{AtS: 60, Leg: 0, Step: 8, Why: "watchdog no new event"},
				{AtS: 120, Leg: 0, Step: 12, Why: "wipe: the party is down"},
			},
			RescueLog: []driver.RescueNote{{AtS: 420, Leg: 0, Step: 12, Why: "wipe: the party is down"}},
		},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "r-saved.forever-run.json")
	b, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, DefaultThresholds())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Verdict != "STOPPED" || !strings.Contains(c.VerdictText, `leg 0 "hall"/step 12`) {
		t.Fatalf("verdict %q", c.VerdictText)
	}
	for _, id := range []string{"repulls", "pack-deaths", "member-deaths", "slow-leg", "watchdog-pause", "uncleared-pause", "rescue", "stop-reason"} {
		if !hasFlag(c, id) {
			t.Fatalf("no %q flag: %v\n%s", id, flagIDs(c), c.Text())
		}
	}
	txt, js, err := Write(dir, c)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	back, err := Load(js, DefaultThresholds())
	if err != nil {
		t.Fatalf("load card: %v", err)
	}
	if back.VerdictText != c.VerdictText || len(back.Flags) != len(c.Flags) {
		t.Fatalf("card round trip: %+v", back)
	}
	if _, err := os.Stat(txt); err != nil {
		t.Fatal(err)
	}
}

func TestCompare(t *testing.T) {
	a := Build(driver.RunSummary{
		Schema: driver.SummarySchema, Version: 1, RunID: "a", Plan: "hall", Mode: "watch",
		Clear: true, StopReason: "done",
		Watch: &driver.RunWatch{State: "done", Steps: 10, Leg: 0, LegName: "hall", Pull: 2, Pulls: 2,
			Legs: []driver.LegRecord{{Leg: 0, Name: "hall", Seconds: 600, Pulls: 10, Steps: 10}}},
	}, DefaultThresholds())
	b := Build(driver.RunSummary{
		Schema: driver.SummarySchema, Version: 1, RunID: "b", Plan: "hall", Mode: "watch",
		StopReason: "wipes",
		Watch: &driver.RunWatch{State: "running", Steps: 10, Leg: 0, LegName: "hall", Pull: 3, Pulls: 2,
			Legs:     []driver.LegRecord{{Leg: 0, Name: "hall", Seconds: 900, Pulls: 12, Steps: 9, Deaths: 1, Wipes: 1}},
			StepLog:  []driver.StepRecord{{Leg: 0, Step: 7, Pulls: 4}},
			PauseLog: []driver.PauseNote{{AtS: 30, Leg: 0, Step: 7, Why: "wipe: the party is down"}},
		},
	}, DefaultThresholds())
	rows := Compare(a, b)
	joined := ""
	for _, r := range rows {
		joined += r.Text + "\n"
	}
	for _, want := range []string{
		"verdict CLEARED -> STOPPED AT leg 0 \"hall\"/step 0 (wipes)",
		"leg 0 \"hall\" seconds: 600 -> 900 (+300)",
		"leg 0 \"hall\" pulls: 10 -> 12 (+2)",
		"leg 0 \"hall\" deaths: 0 -> 1 (+1)",
		"+ repulls leg:0/step:7",
		"~ stop-reason",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("no %q row:\n%s", want, joined)
		}
	}
	for _, r := range rows {
		if strings.Contains(r.Text, "seconds:") && (!r.Worse || r.Better) {
			t.Fatalf("slower leg not worse: %+v", r)
		}
	}
	text := RenderCompare(a, b, rows)
	if !strings.Contains(text, "CLEARED -> STOPPED AT") || !strings.Contains(text, "+ repulls") {
		t.Fatalf("compare text:\n%s", text)
	}
}
