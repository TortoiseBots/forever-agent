package spelljudge

import (
	"bytes"
	"strings"
	"testing"
)

// finding is one member's judgment for a diff test.
func finding(bot, class, spec, level, text string, casts int, spells map[string]int) Finding {
	f := Finding{Casts: casts, Spells: spells}
	f.Bot, f.Class, f.Spec, f.Level, f.Text = bot, class, spec, level, text
	return f
}

func TestDiffNamesSpellAndMemberChanges(t *testing.T) {
	before := []Finding{
		finding("Maintank", "warrior", "protection", "pass", "ok", 60, map[string]int{"Shield Slam": 10, "Taunt": 3}),
		finding("Healbot", "priest", "holy", "pass", "ok", 40, map[string]int{"Renew": 20}),
	}
	after := []Finding{
		finding("Maintank", "warrior", "protection", "warn", "missing core: Shield Slam", 63, map[string]int{"Shield Slam": 22}),
		finding("Healbot", "priest", "holy", "pass", "ok", 40, map[string]int{"Renew": 20}),
	}
	d := Diff(before, after, "run-a", "run-b")
	if d.Format != DiffFormat || d.Before != "run-a" || d.After != "run-b" {
		t.Fatalf("header: %+v", d)
	}
	if len(d.Classes) != 2 {
		t.Fatalf("classes: %+v", d.Classes)
	}
	warrior := d.Classes[0]
	if warrior.Class != "priest" {
		// Classes are sorted: priest before warrior.
		t.Fatalf("class order: %+v", d.Classes)
	}
	var w ClassDiff
	for _, c := range d.Classes {
		if c.Class == "warrior" {
			w = c
		}
	}
	if w.CastsBefore != 60 || w.CastsAfter != 63 {
		t.Fatalf("cast totals: %+v", w)
	}
	changes := map[string][2]int{}
	for _, s := range w.Spells {
		changes[s.Spell] = [2]int{s.Before, s.After}
	}
	if changes["Shield Slam"] != [2]int{10, 22} || changes["Taunt"] != [2]int{3, 0} {
		t.Fatalf("spell changes: %+v", changes)
	}
	if len(w.Members) != 1 || w.Members[0].Bot != "Maintank" || w.Members[0].Before != "pass" || w.Members[0].After != "warn" {
		t.Fatalf("member change: %+v", w.Members)
	}
	for _, c := range d.Classes {
		if c.Class == "priest" && (c.Changed() || c.CastsBefore != c.CastsAfter) {
			t.Fatalf("an unchanged class must not read as changed: %+v", c)
		}
	}
}

func TestDiffReportsAbsentMembersAndSpells(t *testing.T) {
	before := []Finding{finding("Oldbot", "mage", "frost", "pass", "ok", 10, map[string]int{"Frostbolt": 10})}
	after := []Finding{finding("Newbot", "mage", "fire", "pass", "ok", 12, map[string]int{"Fireball": 12})}
	d := Diff(before, after, "a", "b")
	cd := d.Classes[0]
	if cd.CastsBefore != 10 || cd.CastsAfter != 12 {
		t.Fatalf("totals: %+v", cd)
	}
	if len(cd.Spells) != 2 || len(cd.Members) != 2 {
		t.Fatalf("both sides must be named: %+v", cd)
	}
	for _, m := range cd.Members {
		switch m.Bot {
		case "Oldbot":
			if m.After != "absent" {
				t.Fatalf("Oldbot: %+v", m)
			}
		case "Newbot":
			if m.Before != "absent" {
				t.Fatalf("Newbot: %+v", m)
			}
		}
	}
	var w bytes.Buffer
	if err := d.WriteText(&w); err != nil {
		t.Fatal(err)
	}
	out := w.String()
	for _, want := range []string{"mage: 10 cast(s) -> 12 cast(s)", "Frostbolt: removed (10)", "Fireball: added (12)",
		"Oldbot (frost): pass -> absent", "Newbot (fire): absent -> pass"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the text does not name %q:\n%s", want, out)
		}
	}
}

func TestDiffNoChange(t *testing.T) {
	same := []Finding{finding("Healbot", "priest", "holy", "pass", "ok", 40, map[string]int{"Renew": 20})}
	d := Diff(same, same, "a", "b")
	var w bytes.Buffer
	if err := d.WriteText(&w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "no change") {
		t.Fatalf("identical runs must print no change:\n%s", w.String())
	}
}

// TestDiffCountsAMemberWithNoClass pins that a member the run has no class for is grouped under "?"
// instead of vanishing.
func TestDiffCountsAMemberWithNoClass(t *testing.T) {
	before := []Finding{finding("Unknown", "", "", "na", "no class in the trace", 0, nil)}
	after := []Finding{finding("Unknown", "", "", "na", "no class in the trace", 0, map[string]int{"Fireball": 2})}
	d := Diff(before, after, "a", "b")
	if len(d.Classes) != 1 || d.Classes[0].Class != "?" {
		t.Fatalf("classes: %+v", d.Classes)
	}
	if len(d.Classes[0].Spells) != 1 {
		t.Fatalf("spells: %+v", d.Classes[0].Spells)
	}
}

// TestDiffPairsDuplicateNamesInOrder pins the duplicate-safe pairing: two members of one name are
// matched first with first, second with second, never collapsed into one.
func TestDiffPairsDuplicateNamesInOrder(t *testing.T) {
	before := []Finding{
		finding("Twin", "mage", "frost", "pass", "ok", 10, map[string]int{"Frostbolt": 10}),
		finding("Twin", "mage", "fire", "warn", "missing core: Fireball", 8, map[string]int{"Fireball": 8}),
	}
	after := []Finding{
		finding("Twin", "mage", "frost", "pass", "ok", 10, map[string]int{"Frostbolt": 10}),
		finding("Twin", "mage", "fire", "pass", "ok", 9, map[string]int{"Fireball": 9}),
	}
	d := Diff(before, after, "a", "b")
	cd := d.Classes[0]
	if len(cd.Members) != 1 || cd.Members[0].Before != "warn" || cd.Members[0].After != "pass" {
		t.Fatalf("the second Twin's change must be the one reported: %+v", cd.Members)
	}
	if len(cd.Spells) != 1 || cd.Spells[0].Spell != "Fireball" || cd.Spells[0].Before != 8 || cd.Spells[0].After != 9 {
		t.Fatalf("spells: %+v", cd.Spells)
	}
}

// TestDiffPairsAMemberThatChangedClass pins the global pairing: a member judged under another class in
// the newer run is still the same member, reported under its new class.
func TestDiffPairsAMemberThatChangedClass(t *testing.T) {
	before := []Finding{finding("Bot", "shaman", "enhancement", "warn", "missing core: Stormstrike", 20, map[string]int{"Lightning Bolt": 20})}
	after := []Finding{finding("Bot", "", "", "na", "no class in the trace", 20, map[string]int{"Lightning Bolt": 20})}
	d := Diff(before, after, "a", "b")
	var found bool
	for _, cd := range d.Classes {
		for _, m := range cd.Members {
			if m.Bot == "Bot" && cd.Class == "?" && m.Before == "warn" && m.After == "na" {
				found = true
			}
			if m.Bot == "Bot" && cd.Class == "shaman" && m.After == "absent" {
				t.Fatalf("the member must not also read as absent under its old class: %+v", m)
			}
		}
	}
	if !found {
		t.Fatalf("the member's class change is not reported under its new class: %+v", d.Classes)
	}
}
