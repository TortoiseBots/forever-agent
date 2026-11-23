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
// the four words: the 14 Turtle-custom plans are n/a, Ragefire Chasm is exported, and every other plan is todo
// (exported and live-checked follow once a plan has been through them).
func TestEveryPlanHasForeverStatus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(plansDir, "*.json"))
	if err != nil || len(files) < 40 {
		t.Fatalf("%d plan files: %v", len(files), err)
	}
	custom := map[string]bool{}
	for _, n := range turtleCustomPlans {
		custom[n] = true
	}
	words := []string{ForeverNA, ForeverTodo, ForeverExported, ForeverLiveChecked}
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
		case !custom[p.Name] && p.Name != "ragefire_chasm" && word != ForeverTodo:
			t.Errorf("%s: %q, want %s", f, p.ForeverStatus, ForeverTodo)
		}
	}
	if seen[ForeverTodo] < 30 || seen[ForeverNA] != len(turtleCustomPlans) || seen[ForeverExported] != 1 {
		t.Errorf("forever_status words: %v", seen)
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

// TestForeverStatusRejectsUnknownWord: the first word is a closed set, so a plan cannot say where it stands in words
// no reader knows.
func TestForeverStatusRejectsUnknownWord(t *testing.T) {
	bad := `{"version":1,"name":"t","forever_status":"shipped (2026-11-20)","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]}]}`
	_, err := Parse("bad.json", []byte(bad))
	if err == nil || !strings.Contains(err.Error(), "forever_status") {
		t.Fatalf("%v", err)
	}
	for _, s := range []string{
		ForeverNA + " (Turtle custom content)", ForeverTodo, ForeverExported + " (2026-11-20)", ForeverLiveChecked + " (one pull)",
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
