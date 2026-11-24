package kbspells

import (
	"slices"
	"strings"
	"testing"
)

func TestInferSpec(t *testing.T) {
	o := fixtureOracle(t)
	for _, c := range []struct {
		class, role string
		casts       map[string]int
		spec, how   string
	}{
		// Hela in the walk-2 fight: warrior dps with Bloodthirst.
		{"warrior", "dps", map[string]int{"Bloodthirst": 1}, "fury", "signature Bloodthirst 1"},
		{"warrior", "dps", map[string]int{"Heroic Strike": 3}, "class", "no signature cast"},
		// A tie between two specs' signatures tells nothing: class-wide.
		{"warrior", "dps", map[string]int{"Mortal Strike": 1, "Bloodthirst": 1}, "class", "no signature cast"},
		{"warrior", "tank", map[string]int{"Bloodthirst": 1}, "fury_tank", "signature Bloodthirst 1"},
		{"warrior", "tank", map[string]int{"Shield Slam": 4}, "protection", "role tank"},
		{"hunter", "dps", map[string]int{"Bestial Wrath": 1}, "beast_mastery", "signature Bestial Wrath 1"},
		{"hunter", "dps", map[string]int{"Aimed Shot": 2}, "marksmanship", "signature Aimed Shot 2"},
	} {
		spec, how := o.InferSpec(c.class, c.role, c.casts)
		if spec != c.spec || how != c.how {
			t.Errorf("%s %s %v: spec %q (%q), want %q (%q)", c.class, c.role, c.casts, spec, how, c.spec, c.how)
		}
	}
}

// TestJudgeMissingCore: the protection tank of walk-2 fight 5-4 cast Shield Slam and Sunder Armor but no Heroic Strike. The core
// list of the spec says it should have. A capped spell list makes the same reading unknown, and a bot active for less than the
// threshold is not judged at all.
func TestJudgeMissingCore(t *testing.T) {
	o := fixtureOracle(t)
	b := BotCasts{Name: "Ragemeister", Class: "warrior", Role: "tank", Casts: map[string]int{"Shield Slam": 4, "Sunder Armor": 8}, ActiveS: 39}
	f := o.Judge(b, 20)
	if f.Level != "warn" || !slices.Equal(f.Missing, []string{"Heroic Strike"}) || len(f.Never) != 0 {
		t.Fatalf("%+v", f)
	}
	if !strings.Contains(f.Text, "spec protection (role tank)") || !strings.Contains(f.Text, "missing core: Heroic Strike") {
		t.Fatalf("text %q", f.Text)
	}
	b.Truncated = true
	f = o.Judge(b, 20)
	if f.Level != "na" || len(f.Missing) != 0 || !slices.Equal(f.Unknown, []string{"Heroic Strike"}) {
		t.Fatalf("%+v", f)
	}
	if !strings.Contains(f.Text, "unknown (list capped at 8)") {
		t.Fatalf("text %q", f.Text)
	}
	b.Truncated = false
	b.ActiveS = 10
	f = o.Judge(b, 20)
	if f.Level != "na" || len(f.Missing) != 0 {
		t.Fatalf("%+v", f)
	}
}

// TestJudgeNever: the BM file says never Aimed Shot or Serpent Sting.
func TestJudgeNever(t *testing.T) {
	o := fixtureOracle(t)
	f := o.Judge(BotCasts{Name: "Tilla", Class: "hunter", Role: "dps", Casts: map[string]int{
		"Bestial Wrath": 2, "Steady Shot": 4, "Arcane Shot": 3, "Kill Command": 1, "Aimed Shot": 1,
	}, ActiveS: 39}, 20)
	if f.Level != "warn" || !slices.Equal(f.Never, []string{"Aimed Shot"}) {
		t.Fatalf("%+v", f)
	}
	if len(f.Missing) != 0 {
		t.Fatalf("missing %v", f.Missing)
	}
}

// TestJudgeLevelSkip: Shield Slam's first rank is learned at level 30 in the Turtle spell data (id 23922, rank 1), so a level 20
// warrior is not missing it. Heroic Strike is learnt at level 1, so it would be judged.
func TestJudgeLevelSkip(t *testing.T) {
	if lvl, ok := firstRankLevel("Shield Slam"); !ok || lvl != 30 {
		t.Fatalf("Shield Slam first rank level %d (%v), want 30", lvl, ok)
	}
	if lvl, ok := firstRankLevel("Heroic Strike"); !ok || lvl != 1 {
		t.Fatalf("Heroic Strike first rank level %d (%v), want 1", lvl, ok)
	}
	o := fixtureOracle(t)
	f := o.Judge(BotCasts{Name: "Lowbie", Class: "warrior", Role: "tank", Level: 20, Casts: map[string]int{
		"Shield Slam": 4, "Sunder Armor": 8, "Heroic Strike": 1,
	}, ActiveS: 39}, 20)
	if f.Level != "ok" || len(f.Missing) != 0 {
		t.Fatalf("%+v", f)
	}
	// Without Heroic Strike, the level-20 warrior is still missing it: the skip is per spell, not per bot.
	f = o.Judge(BotCasts{Name: "Lowbie", Class: "warrior", Role: "tank", Level: 20, Casts: map[string]int{
		"Shield Slam": 4, "Sunder Armor": 8,
	}, ActiveS: 39}, 20)
	if f.Level != "warn" || !slices.Equal(f.Missing, []string{"Heroic Strike"}) {
		t.Fatalf("%+v", f)
	}
}

// TestJudgeExpected: a caller's own should-have-cast list joins the core judgment. Last Stand is only situational in the
// protection fixture; a caller that knows the bot has the talent makes it a missing core spell. An expectation the core
// list already names is not duplicated, and one the situational list names is judged from the core side, once.
func TestJudgeExpected(t *testing.T) {
	o := fixtureOracle(t)
	base := BotCasts{Name: "Ragemeister", Class: "warrior", Role: "tank", ActiveS: 60,
		Casts: map[string]int{"Shield Slam": 4, "Sunder Armor": 8, "Heroic Strike": 3}}

	f := o.Judge(base, 20)
	if f.Level != "ok" || len(f.Missing) != 0 {
		t.Fatalf("without an expectation: %+v", f)
	}

	with := base
	with.Expected = []string{"Last Stand"}
	f = o.Judge(with, 20)
	if f.Level != "warn" || !slices.Equal(f.Missing, []string{"Last Stand"}) {
		t.Fatalf("expected Last Stand: %+v", f)
	}
	if !strings.Contains(f.Text, "missing core: Last Stand") {
		t.Fatalf("text %q", f.Text)
	}

	// Not cast, already core: one entry, not two.
	with = base
	with.Casts = map[string]int{"Shield Slam": 4, "Sunder Armor": 8}
	with.Expected = []string{"Heroic Strike"}
	f = o.Judge(with, 20)
	if !slices.Equal(f.Missing, []string{"Heroic Strike"}) {
		t.Fatalf("core expectation duplicated: %v", f.Missing)
	}

	// An expectation judged by the bot's level guard is skipped, like a core spell: Shield Wall's first rank is
	// level 24 in the Turtle table.
	with = base
	with.Level = 20
	with.Expected = []string{"Shield Wall"}
	f = o.Judge(with, 20)
	if len(f.Missing) != 0 {
		t.Fatalf("level guard not applied: %v", f.Missing)
	}
}

// TestJudgeStatedSpec: a record that states the spec is judged on it (the inference does not run), an
// unknown stated spec is na rather than guessed, and an empty Spec still infers as before.
func TestJudgeStatedSpec(t *testing.T) {
	o := fixtureOracle(t)
	casts := map[string]int{"Shield Slam": 4, "Sunder Armor": 8, "Heroic Strike": 3}

	f := o.Judge(BotCasts{Class: "warrior", Role: "tank", Spec: "protection", Casts: casts, ActiveS: 60}, 20)
	if f.Spec != "protection" || f.SpecHow != "record" || f.Level != "ok" {
		t.Fatalf("stated spec: %+v", f)
	}
	if !strings.Contains(f.Text, "spec protection (record)") {
		t.Fatalf("text %q", f.Text)
	}

	f = o.Judge(BotCasts{Class: "warrior", Role: "tank", Spec: "retribution", Casts: casts, ActiveS: 60}, 20)
	if f.Level != "na" || len(f.Missing) != 0 {
		t.Fatalf("an unknown stated spec was inferred: %+v", f)
	}

	f = o.Judge(BotCasts{Class: "warrior", Role: "tank", Casts: casts, ActiveS: 60}, 20)
	if f.Spec != "protection" || f.SpecHow == "record" {
		t.Fatalf("an empty Spec did not infer: %+v", f)
	}
}
