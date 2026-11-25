package dungeon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
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
		// Razorfen Downs' Turtle-only Death's Head room: the Worshipper pack, the Servitor and Battleguard
		// pairs and packs, and the boss Death Prophet Rakameg.
		{"razorfen_downs", map[uint32]int{62676: 8, 62677: 14, 62678: 11, 62679: 1}},
		// Uldaman has no Turtle-only entry: its refused fields are Revilosh's items, the two rituals and the holds.
		{"uldaman", nil},
		// Sunken Temple has no Turtle-only entry: its refused fields are the Atal'alarion unit wait and hold.
		{"sunken_temple", nil},
		// Zul'Farrak's Turtle content: Zel'jeb the Ancient (62495) and the 11 Sandfury Ritualists (62500) of his
		// wing, the Farraki Arena (the boss 62498, its first adds 62496/62497), Gahz'rilla (7273, summoned by the
		// Gong) and the stairs event's Sezz'ziz (7275) and Nekrum Gutchewer (7796).
		{"zulfarrak", map[uint32]int{7273: 1, 7275: 1, 7796: 1, 62495: 1, 62496: 1, 62497: 1, 62498: 1, 62500: 11}},
		// Maraudon Purple has no Turtle-only entry: its refused reason is the core's own spawn positions.
		{"maraudon_purple", nil},
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

// TestBatchPlansKeepTheirContent pins the Turtle content of the ten plans the #223 batches checked: batch 2's five
// (razorfen_kraul, the four Scarlet Monastery wings) and batch 3's five (razorfen_downs, uldaman, sunken_temple,
// zulfarrak, maraudon_purple). A Forever check may add forever_status or a note, but no leg, step, unit or point may
// change: the counts catch a deleted move, door or wait step, and the hash catches a spawn moved with the counts
// unchanged (notes, status and forever_status are left out, so text a Forever check writes does not fail it).
func TestBatchPlansKeepTheirContent(t *testing.T) {
	tests := []struct {
		plan         string
		legs, steps  int
		pulls, units int
		hash         string
	}{
		{"razorfen_kraul", 9, 0, 122, 194,
			"26c0ebdf712e41ebc16647f49425739ba891525a066736b824dffd2c10452e41"},
		{"scarlet_graveyard", 5, 0, 49, 140,
			"7c5bf4fb0c9ac3a9379209e220f80943bd5fea4727848317f017d4df9750909a"},
		{"scarlet_library", 3, 0, 42, 83,
			"19750c7881152e656d219e3071919f197709de41deb66f9f37490fb477e077a8"},
		{"scarlet_armory", 2, 0, 42, 82,
			"c439e3505e0a25e8140534426568aec110b7e456b4e297980b1d7363eb2a1f28"},
		{"scarlet_cathedral", 2, 0, 53, 95,
			"895edd895cbec3c64f7c277e1d8bce4691978e81d463f284ed9649ab9ec1810d"},
		{"razorfen_downs", 4, 0, 79, 238,
			"05a96166810bfa00ed55ec37bfa5891b984c6116aec30e70365320a311e7fa90"},
		{"uldaman", 14, 10, 65, 209,
			"230fefadf7b03ce62d8064caf75895df7184c62529c6b98974d382955dee2388"},
		{"sunken_temple", 6, 7, 92, 315,
			"099bbcb3a971007a520b7218fd7f76a7689122de39a7cb5e7c457944254eab2d"},
		{"zulfarrak", 20, 46, 67, 178,
			"636cfd123748fc35c1b7c8ac7705c380121780ee642b7807af1a1c29c027329d"},
		{"maraudon_purple", 6, 0, 141, 392,
			"83e50abe58b76d89c66becb65efb41be9fb62e2cf4fd2873972f43761fc30dd9"},
	}
	for _, tc := range tests {
		path := filepath.Join(plansDir, tc.plan+".json")
		p, err := Load(path)
		if err != nil {
			t.Errorf("%s: %v", tc.plan, err)
			continue
		}
		steps := 0
		for _, l := range p.Legs {
			steps += len(l.Steps) + len(l.AfterBoss)
		}
		pulls, units := 0, 0
		for _, l := range p.Legs {
			pulls += len(l.PullGroups)
			for _, g := range l.PullGroups {
				units += len(g.Entries)
				if g.Hold != nil {
					units += len(g.Hold.Entries)
				}
			}
			if b := l.Boss; b != nil {
				units += 1 + len(b.First)
				if b.Hold != nil {
					units += len(b.Hold.Entries)
				}
			}
		}
		if len(p.Legs) != tc.legs || steps != tc.steps || pulls != tc.pulls || units != tc.units {
			t.Errorf("%s: %d legs, %d steps, %d pulls, %d units; want %d, %d, %d, %d: the plan is shared with the Turtle stack, so its content must not change for a Forever check",
				tc.plan, len(p.Legs), steps, pulls, units, tc.legs, tc.steps, tc.pulls, tc.units)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", tc.plan, err)
			continue
		}
		got, err := sharedContentHash(raw)
		if err != nil {
			t.Errorf("%s: %v", tc.plan, err)
			continue
		}
		if got != tc.hash {
			t.Errorf("%s: Turtle content hash %s; want %s: a spawn, a step or a point moved", tc.plan, got, tc.hash)
		}
	}
}

// sharedContentHash hashes a plan file after the text a Forever check writes is removed: the per-server forever_status,
// the status line and the notes (at every level). Everything else - legs, steps, pulls, units, points, holds - stays in.
func sharedContentHash(raw []byte) (string, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	stripServerText(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:]), nil
}

func stripServerText(v any) {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "notes")
		delete(x, "status")
		delete(x, "forever_status")
		for _, c := range x {
			stripServerText(c)
		}
	case []any:
		for _, c := range x {
			stripServerText(c)
		}
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
