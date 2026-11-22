package kbspells

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/foreverspells"
)

// TestForeverDefaultValidates: the Classic 1.60 oracle the agent ships is embedded and holds every class, and its
// names and levels pass the same validation the Turtle oracle gets, against the Forever spell table in this build.
func TestForeverDefaultValidates(t *testing.T) {
	o, err := ForeverDefault()
	if err != nil {
		t.Fatalf("ForeverDefault: %v", err)
	}
	want := map[string][]string{
		"warrior": {"arms", "fury", "fury_tank", "protection"},
		"paladin": {"holy", "protection", "retribution"},
		"hunter":  {"beast_mastery", "marksmanship", "survival"},
		"rogue":   {"assassination", "combat", "subtlety"},
		"priest":  {"discipline", "holy", "shadow"},
		"shaman":  {"elemental", "enhancement", "restoration"},
		"mage":    {"arcane", "fire", "frost"},
		"warlock": {"affliction", "demonology", "destruction"},
		"druid":   {"balance", "feral_bear", "feral_cat", "restoration"},
	}
	if len(o.Classes) != len(want) {
		t.Fatalf("%d classes, want %d", len(o.Classes), len(want))
	}
	for class, specs := range want {
		got := o.Classes[class]
		if len(got) != len(specs) {
			t.Fatalf("%s: %d specs, want %d", class, len(got), len(specs))
		}
		for _, name := range specs {
			if _, ok := o.spec(class, name); !ok {
				t.Errorf("%s: no spec %s", class, name)
			}
		}
	}
	// The healer and tank readings InferSpec makes must be answered by this data.
	for _, c := range []struct{ class, role, spec string }{
		{"priest", "heal", "holy"}, {"paladin", "heal", "holy"}, {"druid", "heal", "restoration"},
		{"shaman", "heal", "restoration"}, {"warrior", "tank", "protection"},
		{"paladin", "tank", "protection"}, {"druid", "tank", "feral_bear"},
	} {
		if spec, how := o.InferSpec(c.class, c.role, nil); spec != c.spec {
			t.Errorf("InferSpec(%s, %s) = %s (%s), want %s", c.class, c.role, spec, how, c.spec)
		}
	}
	if len(o.Common.Taunts) == 0 {
		t.Fatal("no taunts in the common data")
	}
}

// TestForeverNamesInData pins five ids per class in the real spell table the build carries: the table names each one
// as its class with a level, so the oracle's class and level readings rest on data that is committed, not assumed.
func TestForeverNamesInData(t *testing.T) {
	tab := foreverspells.Default()
	if !tab.Loaded() {
		t.Fatal("this build has no Forever spell data")
	}
	pins := map[string][]uint32{
		"warrior": {78, 7386, 6572, 355, 6673},
		"paladin": {635, 19750, 20165, 20271, 19740},
		"hunter":  {3044, 75, 19434, 1978, 1130},
		"rogue":   {1752, 2098, 5171, 53, 6770},
		"priest":  {139, 17, 2061, 1243, 585},
		"shaman":  {403, 8042, 331, 421, 8050},
		"mage":    {116, 205, 837, 118, 2136},
		"warlock": {686, 172, 348, 1454, 689},
		"druid":   {5176, 8921, 5185, 774, 1126},
	}
	for class, ids := range pins {
		for _, id := range ids {
			s, ok := tab.Lookup(id)
			if !ok {
				t.Fatalf("%s: %d is not in the spell table", class, id)
			}
			if s.Class != class {
				t.Errorf("%d %s: class %q, want %s", id, s.Name, s.Class, class)
			}
			if s.Level == 0 {
				t.Errorf("%d %s: level 0, want a class level", id, s.Name)
			}
		}
	}
	// The nine spells the round names, by name as well as by id.
	for _, p := range []struct {
		id    uint32
		name  string
		class string
	}{
		{116, "Frostbolt", "mage"}, {1752, "Sinister Strike", "rogue"}, {139, "Renew", "priest"},
		{78, "Heroic Strike", "warrior"}, {635, "Holy Light", "paladin"}, {3044, "Arcane Shot", "hunter"},
		{403, "Lightning Bolt", "shaman"}, {686, "Shadow Bolt", "warlock"}, {5176, "Wrath", "druid"},
	} {
		if got := tab.Name(p.id); got != p.name {
			t.Errorf("%d: name %q, want %q", p.id, got, p.name)
		}
		if got := tab.ClassOf(p.id); got != p.class {
			t.Errorf("%d: class %q, want %q", p.id, got, p.class)
		}
	}
}
