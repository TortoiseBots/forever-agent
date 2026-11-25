package dungeon

import (
	"path/filepath"
	"strings"
	"testing"
)

// turtleCustomPlans are the plans of Turtle-custom content: a Forever server has none of it, so the plan says n/a.
var turtleCustomPlans = []string{
	"black_morass", "crescent_grove", "dragonmaw_retreat", "emerald_sanctum", "frostmane_hollow",
	"gilneas_city", "hateforge_quarry", "karazhan_crypt", "lower_karazhan_halls", "stormwind_vault",
	"stormwrought_ruins", "timbermaw_hold", "upper_karazhan_halls", "windhorn_canyon",
}

// TestEveryPlanHasForeverStatus: every plan file says where it stands on the Forever side, and it says so with one of
// the five words: the 14 Turtle-custom plans are n/a, and every other plan is todo, exported, refused or live-checked
// once it has been through that step. The plans checked against the Forever world export in batches (issue #223) are the
// exported and refused ones; the rest still wait for a batch.
func TestEveryPlanHasForeverStatus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(plansDir, "*.json"))
	if err != nil || len(files) < 40 {
		t.Fatalf("%d plan files: %v", len(files), err)
	}
	custom := map[string]bool{}
	for _, n := range turtleCustomPlans {
		custom[n] = true
	}
	words := []string{ForeverNA, ForeverTodo, ForeverExported, ForeverRefused, ForeverLiveChecked}
	seen := map[string]int{}
	for _, f := range files {
		p, err := Load(f)
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		word, _, _ := strings.Cut(p.ForeverStatus, " ")
		if p.ForeverStatus == "" || !contains(words, word) {
			t.Errorf("%s: forever_status %q: start with %s", f, p.ForeverStatus, strings.Join(words, ", "))
			continue
		}
		seen[word]++
		switch {
		case p.Name == "ragefire_chasm" && word != ForeverExported:
			t.Errorf("%s: %q, want %s", f, p.ForeverStatus, ForeverExported)
		case custom[p.Name] && word != ForeverNA:
			t.Errorf("%s: %q, want %s", f, p.ForeverStatus, ForeverNA)
		}
	}
	if seen[ForeverNA] != len(turtleCustomPlans) {
		t.Errorf("n/a plans: %v, want the %d Turtle-custom ones", seen[ForeverNA], len(turtleCustomPlans))
	}
	if seen[ForeverExported] < 1 {
		t.Errorf("forever_status words: %v, want at least one exported", seen)
	}
}

// TestTurtleCustomPlansAreNotApplicable: the 14 plans of Turtle-custom content name it, so a reader knows a Forever
// server cannot run them at all.
func TestTurtleCustomPlansAreNotApplicable(t *testing.T) {
	for _, n := range turtleCustomPlans {
		p, err := Load(filepath.Join(plansDir, n+".json"))
		if err != nil {
			t.Errorf("%s: %v", n, err)
			continue
		}
		if !strings.HasPrefix(p.ForeverStatus, ForeverNA) || !strings.Contains(p.ForeverStatus, "Turtle") {
			t.Errorf("%s: forever_status %q", n, p.ForeverStatus)
		}
	}
}

// TestSharedPlansKeepTurtleContent: a plan file serves both stacks, so checking a plan against ForeverCore data must
// not delete a spawn a Turtle run needs. Where ForeverCore has no spawn of a Turtle content row, the plan keeps the row
// and says refused for Forever: a Forever run cannot clear it, a Turtle run still can. The counts are the units the
// plan's pull groups and boss rows carry, so dropping one member of a pack fails the test, not just dropping the entry.
func TestSharedPlansKeepTurtleContent(t *testing.T) {
	tests := []struct {
		plan    string
		entries map[uint32]int // Turtle content ForeverCore has no spawn for: entry -> units in the plan
	}{
		// Deadmines' Defias Companions (3450) are summoned by the old server's scripts; ForeverCore's summon none.
		{"deadmines", map[uint32]int{3450: 8}},
		// Sever (14682) and Prelate Ironmane (61969) are Turtle bosses, the Mistblade Rippers (61970) their pack.
		{"shadowfang_keep", map[uint32]int{14682: 1, 61969: 1, 61970: 9}},
		// The Velthelaxx the Defiler wing: the boss (62530) and its members (62528, 62529).
		{"blackfathom_deeps", map[uint32]int{62528: 5, 62529: 9, 62530: 1}},
		// Razorfen Kraul's Turtle-only units: Bramblehide Rootshaper (9), its Gnarled Bramblehide (5) and Rotthorn (1).
		{"razorfen_kraul", map[uint32]int{62501: 9, 62502: 5, 62503: 1}},
		// Scarlet Graveyard's Turtle spawns: the Scarlet Jailer pack (14), Duke Dreadmoore and Scorn.
		{"scarlet_graveyard", map[uint32]int{61971: 14, 61972: 1, 14693: 1}},
		// Scarlet Library's Turtle boss, Brother Wystan.
		{"scarlet_library", map[uint32]int{61983: 1}},
		// Scarlet Armory's Turtle boss, Armory Quartermaster Daghelm.
		{"scarlet_armory", map[uint32]int{61982: 1}},
		// Scarlet Cathedral has no Turtle-only entry: its refused field is the Mograine hold, pinned by
		// TestScarletCathedralKeepsMograineHold.
		{"scarlet_cathedral", nil},
	}
	for _, tc := range tests {
		p, err := Load(filepath.Join(plansDir, tc.plan+".json"))
		if err != nil {
			t.Errorf("%s: %v", tc.plan, err)
			continue
		}
		got := map[uint32]int{}
		for _, l := range p.Legs {
			for _, g := range l.PullGroups {
				for _, e := range g.Entries {
					got[e]++
				}
			}
			if l.Boss != nil {
				got[l.Boss.Entry]++
				for _, e := range l.Boss.First {
					got[e]++
				}
			}
		}
		for e, want := range tc.entries {
			if got[e] != want {
				t.Errorf("%s: entry %d has %d units, want %d: it is Turtle content ForeverCore lacks and must stay in the plan", tc.plan, e, got[e], want)
			}
		}
		if word, _, _ := strings.Cut(p.ForeverStatus, " "); word != ForeverRefused {
			t.Errorf("%s: forever_status %q, want %s: the Turtle content has no ForeverCore spawn", tc.plan, p.ForeverStatus, ForeverRefused)
		}
	}
}

// TestScarletCathedralKeepsMograineHold: Scarlet Cathedral's Forever check stopped on plan-export's refusal of
// legs[1].boss.hold, because the module's plan table has no hold rows. The plan keeps the hold for the Turtle stack
// (the Mograine/Whitemane event: instance data 1 at 4, Whitemane 3977 summoned when it ends), so a Forever check must
// not delete it to make the plan export.
func TestScarletCathedralKeepsMograineHold(t *testing.T) {
	p, err := Load(filepath.Join(plansDir, "scarlet_cathedral.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Legs) < 2 || p.Legs[1].Boss == nil {
		t.Fatalf("scarlet_cathedral: leg 1 has no boss, so the Mograine hold is gone")
	}
	h := p.Legs[1].Boss.Hold
	if h == nil {
		t.Fatalf("scarlet_cathedral: legs[1].boss.hold is the Mograine/Whitemane event and must stay in the plan")
	}
	if h.Data != 1 || h.Value == nil || *h.Value != 4 {
		t.Errorf("scarlet_cathedral: the hold reads instance data %d at %v, want 1 at 4", h.Data, h.Value)
	}
	whitemane := false
	for _, e := range h.Entries {
		if e == 3977 {
			whitemane = true
		}
	}
	if !whitemane {
		t.Errorf("scarlet_cathedral: the hold's entries %v name no Whitemane (3977)", h.Entries)
	}
	if word, _, _ := strings.Cut(p.ForeverStatus, " "); word != ForeverRefused {
		t.Errorf("scarlet_cathedral: forever_status %q, want %s: the module has no hold rows", p.ForeverStatus, ForeverRefused)
	}
}

// TestForeverStatusRejectsUnknownWord: the first word is a closed set, so a plan cannot say where it stands in words
// no reader knows.
func TestForeverStatusRejectsUnknownWord(t *testing.T) {
	bad := `{"version":1,"name":"t","forever_status":"shipped (2026-11-20)","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]}]}`
	_, err := Parse("bad.json", []byte(bad))
	if err == nil || !strings.Contains(err.Error(), "forever_status") {
		t.Fatalf("%v", err)
	}
	for _, s := range []string{
		ForeverNA + " (Turtle custom content)", ForeverTodo, ForeverExported + " (2026-11-20)", ForeverRefused + " (the gossip step has no row)", ForeverLiveChecked + " (one pull)",
	} {
		doc := `{"version":1,"name":"t","forever_status":"` + s + `","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]}]}`
		if _, err := Parse("t.json", []byte(doc)); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
}

// TestMarshalKeepsForeverStatus: the key is written back where the file keeps it, so a regenerated file does not lose it.
func TestMarshalKeepsForeverStatus(t *testing.T) {
	const doc = `{"version":1,"name":"t","status":"mapped (not live-checked)","forever_status":"exported (2026-11-20)","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]}]}`
	p, err := Parse("t.json", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"forever_status": "exported (2026-11-20)"`) {
		t.Fatalf("Marshal dropped the key:\n%s", out)
	}
	q, err := Parse("t.json", out)
	if err != nil {
		t.Fatal(err)
	}
	if q.ForeverStatus != p.ForeverStatus {
		t.Errorf("%q, want %q", q.ForeverStatus, p.ForeverStatus)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
