package driver

// The raid watch over the scripted module (SPEC #108 R3): the group is the plan's raid, the card is checked and
// converted before PREPARE, the layout is applied with raid_move, and a short roster or a layout that names nobody
// ends the watch before anything is started.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// raidPlan is a ten-member raid plan: one leg, one group, its raid block said out.
func raidPlan() string {
	return `{
	 "version": 1,
	 "name": "raidscenario",
	 "kind": "raid",
	 "raid": {"size": 10, "tanks": 2, "healers": 3, "dps": 5},
	 "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "legs": [{
	   "name": "one",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "pull_groups": [{"entries": [100], "positions": [{"x": 0, "y": 100, "z": 0}], "radius": 10}]
	 }]
	}`
}

// raidWatchRoute is the raid plan loaded as a raid watch's route.
func raidWatchRoute(t *testing.T) *Route {
	t.Helper()
	p, err := dungeon.Parse("raidscenario", []byte(raidPlan()))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	r, err := NewRaidWatchRoute(p)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	return r
}

// raidWatchConfig is the raid watch's config: a watch over the plan's raid, entered in place.
func raidWatchConfig() Config {
	lim := DefaultLimits()
	return Config{
		Leader: "Tank", Commander: "Lead", Enter: "none", Watch: true, Raid: true,
		Limits: lim, Watchdog: lim.Watchdog,
	}
}

// newRaidWatch builds the raid watch over the raid plan and collects its log lines.
func newRaidWatch(t *testing.T, cfg Config) (*Run, *Fake, *[]string) {
	t.Helper()
	route := raidWatchRoute(t)
	logs := &[]string{}
	cfg.Logf = func(format string, args ...any) {
		*logs = append(*logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
	}
	f := NewFake(route, cfg)
	cfg.Party = f.PartyNames()
	run := New(cfg, route, f, f, f, f.Clock)
	return run, f, logs
}

// TestRaidWatchDryRunReachesEnd is the headless raid walk: ten bots, the group already a raid, the module's run
// scripted to its end, and the watch reporting the run done — the acceptance's dry half.
func TestRaidWatchDryRunReachesEnd(t *testing.T) {
	run, f, logs := newRaidWatch(t, raidWatchConfig())
	if len(f.PartyNames()) != 10 {
		t.Fatalf("the scripted raid has %d bots, want 10", len(f.PartyNames()))
	}
	f.Raid = true
	f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" || !res.Clear {
		t.Fatalf("stop %q clear %v\n%s", res.Stop, res.Clear, strings.Join(*logs, "\n"))
	}
	if res.Summary.Watch == nil || res.Summary.Watch.Steps != 12 {
		t.Fatalf("watch summary %+v", res.Summary.Watch)
	}
	if n := countActions(f, "run start"); n != 1 {
		t.Fatalf("%d run start actions, want 1", n)
	}
	// An already-raid group is never converted.
	if f.ConvertCalls != 0 {
		t.Fatalf("%d convert_raid call(s) for a group that was a raid", f.ConvertCalls)
	}
	if len(f.RaidMoves) != 0 {
		t.Fatalf("layout moves without a layout: %+v", f.RaidMoves)
	}
	// The ten-member report is a whole report: a five-member check would have stopped PREPARE at report.
	joined := strings.Join(*logs, "\n")
	if strings.Contains(joined, "prepare:") {
		t.Fatalf("PREPARE refused the ten-bot party:\n%s", joined)
	}
}

// TestRaidWatchConvertsWhenNotRaid: a party that is not a raid yet is converted once before PREPARE, the card is
// re-read after the convert, and only then is the layout applied. A character that does not lead the group converts
// nothing and stops with the card's own state.
func TestRaidWatchConvertsWhenNotRaid(t *testing.T) {
	cfg := raidWatchConfig()
	dir := t.TempDir()
	layoutFile := filepath.Join(dir, "layout.txt")
	if err := os.WriteFile(layoutFile, []byte("# main tank, and one damage dealer to the third wing\nTank 1\nWatchdps1 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	layout, err := LoadRaidLayout(layoutFile)
	if err != nil {
		t.Fatalf("layout: %v", err)
	}
	cfg.Layout = layout
	run, f, logs := newRaidWatch(t, cfg)
	f.Raid = false
	f.Subgroups = nil // not a raid yet: the group has no subgroup rows until the convert
	f.PlayerLed = true
	f.ScriptRun(FakeRunScript{Steps: 6, DoneAt: 20 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if f.ConvertCalls != 1 {
		t.Fatalf("%d convert_raid call(s), want 1", f.ConvertCalls)
	}
	// The order of the watch's raid work: card, convert, the card again (the readback the layout must wait for),
	// then the moves. A layout sent before the re-read is a layout built on the pre-convert card.
	want := []string{"card", "convert", "card", "move:Watchdps1:3"}
	if strings.Join(f.RaidOps, ",") != strings.Join(want, ",") {
		t.Fatalf("raid calls %v, want %v", f.RaidOps, want)
	}
	// "Tank" is already in subgroup 1 on the default card, so only the second entry goes out.
	if len(f.RaidMoves) != 1 || f.RaidMoves[0].Name != "Watchdps1" || f.RaidMoves[0].Subgroup != 3 {
		t.Fatalf("layout moves %+v", f.RaidMoves)
	}
}

// TestRaidWatchRefusesShortRoster: a card that shows fewer members than the plan's raid size ends the watch before
// the module's run is started, naming both numbers.
func TestRaidWatchRefusesShortRoster(t *testing.T) {
	run, f, logs := newRaidWatch(t, raidWatchConfig())
	f.Raid = true
	f.CardLimit = 8
	res := run.Watch(context.Background())
	if res.Stop != "raid:roster" {
		t.Fatalf("stop %q, want raid:roster\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start"); n != 0 {
		t.Fatalf("%d run start actions for a refused roster", n)
	}
	if joined := strings.Join(*logs, "\n"); !strings.Contains(joined, "shows 8 members, the plan needs 10") {
		t.Fatalf("the refusal does not name the numbers:\n%s", joined)
	}
}

// TestRaidWatchAppliesLayout: every layout entry moves its bot with raid_move in file order, an entry already in its
// subgroup sends nothing, and the watch goes on to the run's end.
func TestRaidWatchAppliesLayout(t *testing.T) {
	cfg := raidWatchConfig()
	cfg.Layout = []RaidSlot{{Name: "Watchdps1", Subgroup: 1}, {Name: "Watchdps2", Subgroup: 3}, {Name: "Tank", Subgroup: 4}}
	run, f, logs := newRaidWatch(t, cfg)
	f.Raid = true
	f.ScriptRun(FakeRunScript{Steps: 6, DoneAt: 20 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if f.ConvertCalls != 0 {
		t.Fatalf("%d convert_raid call(s) for a raid", f.ConvertCalls)
	}
	if len(f.RaidMoves) != 2 || f.RaidMoves[0].Name != "Watchdps2" || f.RaidMoves[0].Subgroup != 3 ||
		f.RaidMoves[1].Name != "Tank" || f.RaidMoves[1].Subgroup != 4 {
		t.Fatalf("layout moves %+v", f.RaidMoves)
	}
	// Watchdps1 is in subgroup 1 already (the scripted raid's default layout): no packet for it.
	for _, m := range f.RaidMoves {
		if m.Name == "Watchdps1" {
			t.Fatalf("a move was sent for a member already in its subgroup: %+v", f.RaidMoves)
		}
	}
}
