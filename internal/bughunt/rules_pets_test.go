package bughunt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trace"
)

// TestRule7PetFightsAlone: a pet in combat on a mob for more than 5 s while its alive owner is out of combat (or has
// no target) produces one hit keyed by the owner's class. Near-misses (owner fighting the mob, duration <= 5 s, dead
// owner, unknown owner, pet targeting a party member) produce no hit.
func TestRule7PetFightsAlone(t *testing.T) {
	mob := gid(0xB01)
	pet := gid(0xE01)
	roles := map[string]string{gTank: "tank", gDps: "dps"}

	makeRun := func(n int, step float64, ownerCombat bool, ownerTarget string, ownerAlive bool, petOwner string, petTarget string) *Run {
		var frames []*Frame
		for i := range n {
			tank := testUnit(gTank)
			tank.Combat = true
			owner := testUnit(gDps)
			owner.Combat = ownerCombat
			owner.Target = ownerTarget
			owner.Alive = ownerAlive
			if !ownerAlive {
				owner.Life = "dead"
			}
			f := frameAt(float64(i)*step, tank, owner)
			f.Mobs = []*Mob{{G: mob, Entry: 1234, HPPct: 90, Combat: true}}
			f.Pets = []*Pet{{G: pet, Owner: petOwner, HPPct: 100, Combat: true, Target: petTarget, X: 1, Y: 2, Z: 3}}
			frames = append(frames, f)
		}
		r := testRun("watch", roles, frames...)
		r.Classes[gDps] = "hunter"
		return r
	}

	// 6 s of frames (t = 0, 2, 4, 6): pet in combat on mob M, owner out of combat -> 1 hit.
	hits := Rule7(makeRun(4, 2, false, "", true, gDps, mob))
	if len(hits) != 1 {
		t.Fatalf("%d hits, want 1: %+v", len(hits), hits)
	}
	h := hits[0]
	if h.Rule != 7 || h.Bot != gDps || h.Cls != "hunter" || h.Role != "dps" {
		t.Fatalf("hit %+v, want rule 7 on hunter owner %s", h, gDps)
	}
	if len(h.Key) != 2 || h.Key[0] != "7" || h.Key[1] != "hunter" {
		t.Fatalf("key %v, want [7 hunter]", h.Key)
	}
	if h.Dur != 6 || h.Cost != 6 {
		t.Fatalf("dur/cost %v/%v, want 6/6", h.Dur, h.Cost)
	}
	for _, want := range []string{pet, gDps, mob, "6 s"} {
		if !strings.Contains(h.Ev, want) {
			t.Fatalf("evidence %q missing %q", h.Ev, want)
		}
	}

	// Owner in combat with empty target -> 1 hit.
	if got := Rule7(makeRun(4, 2, true, "", true, gDps, mob)); len(got) != 1 {
		t.Fatalf("owner in combat with empty target: %d hits, want 1: %+v", len(got), got)
	}

	// Negative / near-miss cases -> 0 hits.
	cases := []struct {
		name        string
		n           int
		step        float64
		ownerCombat bool
		ownerTarget string
		ownerAlive  bool
		petOwner    string
		petTarget   string
	}{
		{"owner in combat on mob M", 4, 2, true, mob, true, gDps, mob},
		{"4 s episode (<= 5 s)", 3, 2, false, "", true, gDps, mob},
		{"5 s exact boundary (not > 5 s)", 2, 5, false, "", true, gDps, mob},
		{"owner dead", 4, 2, false, "", false, gDps, mob},
		{"unknown owner (empty)", 4, 2, false, "", true, "", mob},
		{"unknown owner (not in frame)", 4, 2, false, "", true, gid(0xA99), mob},
		{"pet target is a party member, not a mob", 4, 2, false, "", true, gDps, gTank},
		{"pet target empty", 4, 2, false, "", true, gDps, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Rule7(makeRun(tc.n, tc.step, tc.ownerCombat, tc.ownerTarget, tc.ownerAlive, tc.petOwner, tc.petTarget)); len(got) != 0 {
				t.Fatalf("%d hits, want 0: %+v", len(got), got)
			}
		})
	}
}

// TestLoadWatchCarriesPetsAndRunsRule7: a watch trace JSON containing `pt` carries the pets through trace.Read and
// bughunt.LoadWatch, and Rule 7 fires on the loaded run.
func TestLoadWatchCarriesPetsAndRunsRule7(t *testing.T) {
	dir := t.TempDir()
	runID := "r-20261122T100000-pets"
	tracePath := filepath.Join(dir, runID+".jsonl")
	mob := gid(0xB01)
	pet := gid(0xE01)
	env := func(k string, ms int64) trace.Envelope {
		return trace.Envelope{V: trace.Version, K: k, Run: runID, Src: trace.Producer, T: ms}
	}
	lines := []any{
		trace.Header{Envelope: env(trace.KindHeader, 0), Schema: trace.Schema, Scope: "party", StartUnix: 1_700_000_000, IntervalMS: 2000},
		trace.PartyRecord{Envelope: env(trace.KindParty, 0), Member: trace.Member{G: gDps, Name: "Hunter", Role: "dps", Class: "hunter"}},
		trace.MobRecord{Envelope: env(trace.KindMobs, 0), MobInfo: trace.MobInfo{G: mob, Entry: 1234, Name: "Mob"}},
	}
	for s := range 4 {
		lines = append(lines, trace.Frame{
			Envelope: env(trace.KindFrame, int64(s*2000)),
			Via:      "session",
			Bots:     []trace.Bot{{G: gDps, Alive: true, Combat: false, State: "n", HP: 100, MaxHP: 100}},
			Mobs:     []trace.Mob{{G: mob, HPPct: 90, Combat: true}},
			Pets:     []trace.Pet{{G: pet, Owner: gDps, HPPct: 100, Combat: true, Target: mob, X: 1, Y: 2, Z: 3}},
		})
	}
	f, err := os.Create(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := LoadWatch(tracePath, "watchds")
	if err != nil {
		t.Fatalf("LoadWatch: %v", err)
	}
	if len(r.Frames) != 4 || len(r.Frames[0].Pets) != 1 || r.Frames[0].Pets[0].Owner != gDps {
		t.Fatalf("loaded pets %+v", r.Frames[0].Pets)
	}
	hits, counts := Hunt([]*Run{r}, FrameRules())
	got := hitsFor(hits, 7, gDps)
	if len(got) != 1 {
		t.Fatalf("rule 7 hits %d (counts %+v), want 1: %+v", len(got), counts, hits)
	}
}
