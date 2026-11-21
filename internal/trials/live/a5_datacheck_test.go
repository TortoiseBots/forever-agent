package live

// a5_datacheck_test.go: the pure data-check plan/report and the sweep's
// order and skips. The live halves are exercised by fake probes and the
// embedded trials; no server is touched.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeProbe struct {
	learn   map[int]string
	items   map[int]ItemAnswer
	mobs    map[int]SpawnAnswer
	killed  []int
	learned []int
}

func (p *fakeProbe) Learn(ctx context.Context, spell int) (string, error) {
	p.learned = append(p.learned, spell)
	if res, ok := p.learn[spell]; ok {
		return res, nil
	}
	return "no-row", nil
}

func (p *fakeProbe) ItemScan(ctx context.Context, entry int) (ItemAnswer, error) {
	if v, ok := p.items[entry]; ok {
		return v, nil
	}
	return ItemAnswer{Note: "no record"}, nil
}

func (p *fakeProbe) Spawn(ctx context.Context, entry int) (SpawnAnswer, error) {
	if v, ok := p.mobs[entry]; ok {
		return v, nil
	}
	return SpawnAnswer{Reason: "nothing appeared"}, nil
}

func (p *fakeProbe) Kill(ctx context.Context, entry int) error {
	p.killed = append(p.killed, entry)
	return nil
}

// TestDataCheckReport: ok, unknown and differs are told apart. Only the
// server's own `unknown` word for a spell, or an explicit exists=no for an
// overlay item, is a data diff; a missing row, a refusal or an unanswered
// scan is unknown with its reason, never `differs`.
func TestDataCheckReport(t *testing.T) {
	probe := &fakeProbe{
		learn: map[int]string{770: "learned", 118: "unknown", 133: "refused", 134: "no-row", 135: "not-learned"},
		items: map[int]ItemAnswer{1708: {Answered: true, Exists: true}, 17083: {Answered: true, Exists: false}},
		mobs:  map[int]SpawnAnswer{2734: {OK: true, Level: 40}, 9999: {OK: true, Level: 50}},
	}
	plan := DataCheckPlan{
		Spells: []SpellCheck{{ID: 770, Source: "trial"}, {ID: 118, Source: "trial", Known: true},
			{ID: 133, Source: "trial", Known: true}, {ID: 134, Source: "trial", Known: true},
			{ID: 135, Source: "trial", Known: true}},
		Items: []ItemCheck{{Entry: 1708, Source: "forever/items.json", FromOverlay: true},
			{Entry: 17083, Source: "forever/items.json", FromOverlay: true},
			{Entry: 5555, Source: "forever/items.json", FromOverlay: true},
			{Entry: 0, Name: "Missing Thing", Source: "trial/bot.items"}},
		Mobs: []MobCheck{{Entry: 2734, Role: "melee", LevelMin: 40, LevelMax: 41},
			{Entry: 9999, Role: "melee", LevelMin: 40, LevelMax: 41}},
	}
	report, err := RunDataCheck(context.Background(), probe, plan, "srv", "2026-11-21T00:00:00Z")
	if err != nil {
		t.Fatalf("data check: %v", err)
	}
	if len(report.Ok) != 3 {
		t.Fatalf("ok rows = %d, want 3 (learned spell, existing item, in-range mob): %v", len(report.Ok), report.Ok)
	}
	// differs: the server's `unknown` for a known spell, an explicit exists=no
	// overlay item, a mob level out of range. Nothing else.
	if len(report.Differs) != 3 {
		t.Fatalf("differs rows = %d, want 3: %v", len(report.Differs), report.Differs)
	}
	for _, row := range report.Differs {
		if row.Kind == "spell" && row.ID != 118 {
			t.Fatalf("spell %v is a data diff without the server's `unknown` answer: %v", row.ID, row)
		}
		if row.Kind == "item" && row.ID != 17083 {
			t.Fatalf("item %v is a data diff without an explicit exists=no: %v", row.ID, row)
		}
	}
	// unknown: refused, no-row, not-learned spells; the unanswered overlay
	// item; the unresolved item name.
	if len(report.Unknown) != 5 {
		t.Fatalf("unknown rows = %d, want 5: %v", len(report.Unknown), report.Unknown)
	}
	if len(probe.killed) != len(plan.Mobs) {
		t.Fatalf("killed %v, want every planned mob entry %d", probe.killed, len(plan.Mobs))
	}
	path := filepath.Join(t.TempDir(), "forever-data-report.json")
	if err := WriteDataCheck(path, report); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"ok"`, `"unknown"`, `"differs"`, `"counts"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("report lacks %s: %s", key, raw)
		}
	}
	if DataCheckExit(report, false) == 0 || DataCheckExit(&DataCheckReport{Counts: map[string]int{"unknown": 2}}, false) == 0 {
		t.Fatal("an all-unknown report must not exit 0")
	}
	if DataCheckExit(&DataCheckReport{Counts: map[string]int{"unknown": 2}}, true) != 0 {
		t.Fatal("--allow-unknown accepts unknown rows")
	}
}

// A halting spawn answer stops the remaining spawns: they are unknown with
// the reason, never spawned.
func TestDataCheckHaltStopsTheSpawns(t *testing.T) {
	probe := &haltProbe{}
	plan := DataCheckPlan{Mobs: []MobCheck{{Entry: 1}, {Entry: 2}, {Entry: 3}}}
	report, err := RunDataCheck(context.Background(), probe, plan, "s", "now")
	if err != nil {
		t.Fatal(err)
	}
	if probe.spawned != 1 {
		t.Fatalf("spawned %d mobs after a halt, want 1", probe.spawned)
	}
	if len(report.Unknown) != 3 || report.Counts["ok"] != 0 {
		t.Fatalf("rows: %v", report)
	}
}

type haltProbe struct{ spawned int }

func (p *haltProbe) Learn(context.Context, int) (string, error) { return "learned", nil }
func (p *haltProbe) ItemScan(context.Context, int) (ItemAnswer, error) {
	return ItemAnswer{}, nil
}
func (p *haltProbe) Spawn(context.Context, int) (SpawnAnswer, error) {
	p.spawned++
	return SpawnAnswer{Halt: true, Reason: "a real player came near"}, nil
}
func (p *haltProbe) Kill(context.Context, int) error { return nil }

// TestBuildDataCheckPlanNamesEveryTrialSpell: the plan carries the trial's
// talents, stance, debuff and rule spells, the kit entries and the mob table.
func TestBuildDataCheckPlanNamesEveryTrialSpell(t *testing.T) {
	trials := EmbeddedTrials()
	if len(trials) == 0 {
		t.Fatal("no embedded trials")
	}
	plan := BuildDataCheckPlan(trials)
	if len(plan.Spells) == 0 || len(plan.Items) == 0 || len(plan.Mobs) == 0 {
		t.Fatalf("empty plan: %d spells, %d items, %d mobs", len(plan.Spells), len(plan.Items), len(plan.Mobs))
	}
	has := func(id int) bool {
		for _, s := range plan.Spells {
			if s.ID == id {
				return true
			}
		}
		return false
	}
	// 770 is the tested dispel debuff and 16462 an arms talent every player
	// setup learns.
	if !has(770) || !has(16462) {
		t.Fatalf("plan misses the trial spells 770/16462")
	}
	foundMob := false
	for _, m := range plan.Mobs {
		if m.Entry == 2734 {
			foundMob = true
		}
	}
	if !foundMob {
		t.Fatalf("the Forever mob overlay entry 2734 is not in the plan: %v", plan.Mobs)
	}
}

// TestSweepOrderAndSkips: the asked classes only, tanks first, by id, and a
// class the flag did not ask is listed as a skip, never dropped.
func TestSweepOrderAndSkips(t *testing.T) {
	trials := EmbeddedTrials()
	picked, skipped := SweepPlan([]string{"warrior", "priest", "mage", "rogue"}, trials, nil)
	if len(picked) == 0 {
		t.Fatal("no trials picked")
	}
	seenOther := false
	for _, row := range picked {
		switch row.Class {
		case "warrior", "priest", "mage", "rogue":
		default:
			t.Fatalf("picked trial %s of class %s", row.ID, row.Class)
		}
		if row.Role == "tank" {
			if seenOther {
				t.Fatalf("a tank %s follows a non-tank in the sweep order", row.ID)
			}
		} else {
			seenOther = true
		}
	}
	ids := map[string]bool{}
	for _, row := range picked {
		if ids[row.ID] {
			t.Fatalf("trial %s picked twice", row.ID)
		}
		ids[row.ID] = true
	}
	druidSkipped := false
	for _, s := range skipped {
		if s.Class == "druid" && strings.Contains(s.Reason, "not requested") {
			druidSkipped = true
		}
		if s.Class == "warrior" && !strings.Contains(s.Reason, "not matched") {
			t.Fatalf("a warrior trial was skipped for the wrong reason: %v", s)
		}
	}
	if !druidSkipped {
		t.Fatalf("no druid trial in the skip list: %v", skipped)
	}
	// A trial argument narrows the order without losing the skip list.
	narrow, _ := SweepPlan([]string{"warrior"}, trials, []string{"tank-warrior-pack-spread"})
	if len(narrow) != 1 || narrow[0].ID != "tank-warrior-pack-spread" {
		t.Fatalf("narrowed sweep = %v", narrow)
	}
	md := SweepMarkdown([]SweepRow{{Trial: picked[0], Verdict: "INVALID", Reason: "no arena",
		Card: picked[0].ID + "/card.md"}}, skipped)
	if !strings.Contains(md, "| 1 | "+picked[0].ID) || !strings.Contains(md, "Skipped by the class flag") {
		t.Fatalf("sweep.md text:\n%s", md)
	}
}
