package playdata

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/data/aotc"
	"github.com/pjerra/forever-agent/internal/foreverspells"
	"github.com/pjerra/forever-agent/internal/kbspells"
)

// fakeTables is the player-data view of a synthetic bot: hand-built picks, priorities, gear lists
// and items, keyed class/spec (gear and items also by slot/id).
type fakeTables struct {
	specs    map[string]bool
	picks    map[string][]aotc.TalentPick
	priority map[string][]aotc.StatWeight
	gear     map[string][]aotc.GearItem
	items    map[int64]aotc.ItemInfo
}

func key(parts ...string) string { return strings.Join(parts, "|") }

func (f *fakeTables) SpecKnown(class, spec string) bool { return f.specs[key(class, spec)] }
func (f *fakeTables) CommonTalents(class, spec, levels string, minShare float64) []aotc.TalentPick {
	var out []aotc.TalentPick
	for _, p := range f.picks[key(class, spec)] {
		if p.Levels == levels && p.PickPct >= minShare {
			out = append(out, p)
		}
	}
	return out
}
func (f *fakeTables) StatPriorityFor(class, spec string) []aotc.StatWeight {
	return f.priority[key(class, spec)]
}
func (f *fakeTables) GearFor(class, spec, slot string) []aotc.GearItem {
	var out []aotc.GearItem
	for _, g := range f.gear[key(class, spec)] {
		if g.SetSlot() || g.Population != aotc.PopulationBestGeared {
			continue
		}
		if slot != "" && !strings.EqualFold(g.Slot, slot) {
			continue
		}
		out = append(out, g)
	}
	return out
}
func (f *fakeTables) Item(id int64) (aotc.ItemInfo, bool) {
	info, ok := f.items[id]
	return info, ok
}

// graph is a priest/holy-shaped spec: a caster whose head list is item 1, priority spell power then
// spirit.
func fixture() *fakeTables {
	return &fakeTables{
		specs: map[string]bool{key("priest", "holy"): true, key("priest", "discipline"): true,
			key("warrior", "protection"): true, key("warrior", "arms"): true, key("mage", "fire"): true},
		picks: map[string][]aotc.TalentPick{
			// Holy Nova 15237: active, no cooldown. Inner Focus 14751: active, 3 min cooldown. Both
			// are situational in the priest's spell lists. Prayer of Mending 401859 is in no list.
			key("priest", "holy"): {
				{Talent: "Holy Nova", SpellID: 15237, Levels: "60", PickPct: 73.9},
				{Talent: "Inner Focus", SpellID: 14751, Levels: "60", PickPct: 91.8},
				{Talent: "Spiritual Guidance", SpellID: 14901, Levels: "60", PickPct: 99.7}, // passive
				{Talent: "Mental Agility", SpellID: 14520, Levels: "60", PickPct: 45.2},     // below the share
			},
			key("priest", "discipline"): {
				{Talent: "Prayer of Mending", SpellID: 401859, Levels: "60", PickPct: 79.4},
			},
		},
		priority: map[string][]aotc.StatWeight{
			key("priest", "holy"):        {{Order: 1, Stat: "Healing"}, {Order: 2, Stat: "Spirit"}, {Order: 3, Stat: "Intellect"}},
			key("priest", "discipline"):  {{Order: 1, Stat: "Healing"}, {Order: 2, Stat: "Intellect"}},
			key("warrior", "protection"): {{Order: 1, Stat: "Stamina"}, {Order: 2, Stat: "Strength"}, {Order: 3, Stat: "Defense"}},
			key("warrior", "arms"):       {{Order: 1, Stat: "Stamina"}, {Order: 2, Stat: "Strength"}, {Order: 3, Stat: "Crit"}},
			key("mage", "fire"):          {{Order: 1, Stat: "Spell Power"}, {Order: 2, Stat: "Intellect"}},
		},
		gear: map[string][]aotc.GearItem{
			key("priest", "holy"): {
				{Class: "priest", Spec: "holy", Population: aotc.PopulationBestGeared, Slot: "Head", Name: "Good Hat", ID: 1, Pct: 40},
			},
		},
		items: map[int64]aotc.ItemInfo{
			1: {ID: 1, Name: "Good Hat", Slots: []string{"Head"}},
			// 2: a tank head piece two stamina specs wear: a clear mismatch for a caster.
			2: {ID: 2, Name: "Plate Tank Helm", Slots: []string{"Head"}, Wearers: []aotc.ItemWearer{
				{Class: "warrior", Spec: "protection", Slot: "Head", Population: aotc.PopulationBestGeared, Pct: 30},
				{Class: "warrior", Spec: "arms", Slot: "Head", Population: aotc.PopulationBestGeared, Pct: 12},
				{Class: "mage", Spec: "fire", Slot: "Head", Population: aotc.PopulationBestGeared, Pct: 1},
			}},
			// 3: mixed wearers (a stamina spec and a caster spec) prove nothing: not a mismatch.
			3: {ID: 3, Name: "Shared Tank Crown", Slots: []string{"Head"}, Wearers: []aotc.ItemWearer{
				{Class: "warrior", Spec: "protection", Slot: "Head", Population: aotc.PopulationBestGeared, Pct: 12},
				{Class: "mage", Spec: "fire", Slot: "Head", Population: aotc.PopulationBestGeared, Pct: 9},
			}},
			4:  {ID: 4, Name: "Unlisted Thing", Slots: []string{"Head"}},
			10: {ID: 10, Name: "Cloth Belt", Slots: []string{"Waist"}},
		},
	}
}

func testInputs(t *testing.T, tables Tables) Inputs {
	t.Helper()
	o, err := kbspells.ForeverDefault()
	if err != nil {
		t.Skipf("no Forever spell data in this build: %v", err)
	}
	tab := foreverspells.Default()
	if !tab.Loaded() {
		t.Skip("no Forever spell table in this build")
	}
	return Inputs{Tables: tables, Spells: tab, Oracle: o}
}

// TestGearFitsSpec is the gear rule's table: a listed item is fine, an item's own stats can make it
// fine, a contrary-family item and a stat mismatch are clear failures, and every missing piece of data
// is INCONCLUSIVE (never a pass).
func TestGearFitsSpec(t *testing.T) {
	tables := fixture()
	in := testInputs(t, tables)
	cases := []struct {
		name     string
		rec      Record
		verdict  string
		k, n     int64
		wantLine []string
	}{
		{
			name: "good",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 1}}},
			verdict: VerdictPass, k: 1, n: 1,
			wantLine: []string{"Head: Good Hat (1)", "worn by 40.0%"},
		},
		{
			name: "fine by its own stats",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 9, Name: "Spirit Hood", Slot: "Head", Stats: []string{"Spirit", "Stamina"}}}},
			verdict: VerdictPass, k: 1, n: 1,
			wantLine: []string{"its stats (Spirit, Stamina) match the priority (Healing, Spirit, Intellect)"},
		},
		{
			name: "clear mismatch by its stats",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 10, Name: "Strength Plate Helm", Slot: "Head", Stats: []string{"Strength", "Stamina"}}}},
			verdict: VerdictFail, k: 0, n: 1,
			wantLine: []string{"Strength Plate Helm", "match none of the priority"},
		},
		{
			name: "clear mismatch by its wearers",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 2}}},
			verdict: VerdictFail, k: 0, n: 1,
			wantLine: []string{"Plate Tank Helm (2)", "commonly worn by", "warrior/protection", "none of their stat families (strength) is in its priority"},
		},
		{
			name: "a mage in a strength plate piece",
			rec: Record{Class: "mage", Spec: "fire", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 2}}},
			verdict: VerdictFail, k: 0, n: 1,
			wantLine: []string{"Plate Tank Helm (2)", "none of their stat families (strength) is in its priority"},
		},
		{
			name: "mixed wearers prove no mismatch",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 3}}},
			verdict: VerdictInconclusive, k: 0, n: 1,
			wantLine: []string{"not among the Head items holy players wear"},
		},
		{
			name: "unlisted and no stats",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 4}}},
			verdict: VerdictInconclusive, k: 0, n: 1,
			wantLine: []string{"not among the Head items holy players wear", "no item stats"},
		},
		{
			name: "slot unknown",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 999999}}},
			verdict: VerdictInconclusive, k: 0, n: 1,
			wantLine: []string{"slot unknown: item 999999"},
		},
		{
			name:     "no item data at all",
			rec:      Record{Class: "priest", Spec: "holy", Level: 60},
			verdict:  VerdictInconclusive,
			wantLine: []string{"no equipped items in the record"},
		},
		{
			name:     "known but empty",
			rec:      Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true},
			verdict:  VerdictInconclusive,
			wantLine: []string{"the record proves no equipped items"},
		},
		{
			name: "below level 60",
			rec: Record{Class: "priest", Spec: "holy", Level: 55, EquippedKnown: true,
				Equipped: []Item{{Entry: 1}}},
			verdict:  VerdictInconclusive,
			wantLine: []string{"level 60 only (bot level 55)"},
		},
		{
			name: "unknown spec",
			rec: Record{Class: "priest", Spec: "smite", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 1}}},
			verdict:  VerdictInconclusive,
			wantLine: []string{"no player data for priest/smite"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := gearRow(tc.rec, in)
			if row.Verdict != tc.verdict || row.K != tc.k || row.N != tc.n {
				t.Fatalf("verdict %s k/n %d/%d, want %s %d/%d (%s)", row.Verdict, row.K, row.N, tc.verdict, tc.k, tc.n, row.Reason)
			}
			joined := strings.Join(row.Evidence, "\n") + "\n" + row.Reason
			for _, want := range tc.wantLine {
				if !strings.Contains(joined, want) {
					t.Fatalf("evidence %q does not name %q", joined, want)
				}
			}
			if tc.verdict != VerdictPass && row.Reason == "" {
				t.Fatal("a non-pass rule has no reason")
			}
		})
	}
}

// TestGearFitsSpecRealData pins the shipped tables on the cases a cold review found: a feral bot in
// rogue/hunter agility leather and rings is no clear mismatch (Agility is feral's #2 stat; Stamina
// names no family), an enhancement shaman in mail its own spec also wears is fine, and a mage in a
// strength plate piece is still a clear mismatch.
func TestGearFitsSpecRealData(t *testing.T) {
	in := Inputs{Tables: aotc.Default()}
	cases := []struct {
		name    string
		rec     Record
		verdict string
	}{
		{
			name: "feral in rogue/hunter leather and rings",
			rec: Record{Class: "druid", Spec: "bear", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 19984}, {Entry: 18500}}},
			verdict: VerdictPass,
		},
		{
			name: "enhancement in hunter-shared mail",
			rec: Record{Class: "shaman", Spec: "enh", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 276739}}},
			verdict: VerdictPass,
		},
		{
			// Fury Visor is warrior plate: arms/protection are strength, fury's priority starts at
			// agility, and no mage stat family is among them.
			name: "mage in strength plate",
			rec: Record{Class: "mage", Spec: "fire", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 20521}}},
			verdict: VerdictFail,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := gearRow(tc.rec, in)
			if row.Verdict != tc.verdict {
				t.Fatalf("verdict %s, want %s (%s)\n%s", row.Verdict, tc.verdict, row.Reason, strings.Join(row.Evidence, "\n"))
			}
		})
	}
}

// TestGearFitsSpecEverySpecOwnGearPasses is the rule's property: every class/spec's own most-worn
// items are exactly what the spec's players wear, so the rule must pass every one of them.
func TestGearFitsSpecEverySpecOwnGearPasses(t *testing.T) {
	tables := aotc.Default()
	in := Inputs{Tables: tables}
	for _, spec := range tables.Specs {
		t.Run(spec.Class+"/"+spec.Spec, func(t *testing.T) {
			var items []Item
			seen := map[string]bool{}
			for _, g := range tables.GearFor(spec.Class, spec.Spec, "") {
				if g.ID <= 0 || seen[g.Slot] {
					continue
				}
				seen[g.Slot] = true
				items = append(items, Item{Entry: g.ID, Slot: g.Slot})
			}
			if len(items) == 0 {
				t.Fatal("no most-worn items")
			}
			row := gearRow(Record{Class: spec.Class, Spec: spec.Spec, Level: GearLevel, EquippedKnown: true, Equipped: items}, in)
			if row.Verdict != VerdictPass {
				t.Fatalf("its own gear is %s: %s\n%s", row.Verdict, row.Reason, strings.Join(row.Evidence, "\n"))
			}
		})
	}
}

func TestTalentsImplySpells(t *testing.T) {
	tables := fixture()
	in := testInputs(t, tables)
	casts := map[string]int{"Renew": 4, "Flash Heal": 6}
	combat := func(from, to float64) []CombatWindow { return []CombatWindow{{From: from, To: to}} }
	cases := []struct {
		name     string
		rec      Record
		verdict  string
		k, n     int64
		wantLine []string
		wantNot  []string
	}{
		{
			name: "a trained talent spell never cast FAILs",
			rec: Record{Class: "priest", Spec: "disc", Role: "healer", Level: 60, ActiveS: 60,
				ActiveKnown: true, Combat: combat(0, 60), TalentsKnown: true, TalentsComplete: true,
				Talents: []int64{401859}, Casts: casts},
			verdict: VerdictFail, k: 0, n: 1,
			wantLine: []string{"Prayer of Mending (Prayer of Mending, 79.4% of discipline): never cast in 60 s of combat"},
		},
		{
			name: "a trained talent spell cast passes",
			rec: Record{Class: "priest", Spec: "disc", Role: "healer", Level: 60, ActiveS: 60,
				ActiveKnown: true, Combat: combat(0, 60), TalentsKnown: true, TalentsComplete: true,
				Talents: []int64{401859},
				Casts:   map[string]int{"Renew": 4, "Prayer of Mending": 3}},
			verdict: VerdictPass, k: 1, n: 1,
			wantLine: []string{"Prayer of Mending (Prayer of Mending): cast"},
		},
		{
			name: "a situational talent spell is reported, never failed",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 60,
				ActiveKnown: true, Combat: combat(0, 60), TalentsKnown: true, TalentsComplete: true,
				Talents: []int64{15237}, Casts: casts},
			verdict:  VerdictInconclusive,
			wantLine: []string{"Holy Nova (Holy Nova, 73.9% of holy): situational in the spell lists", "reported, not judged"},
		},
		{
			name: "a partial marker kit never turns a false PASS",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 60,
				ActiveKnown: true, Combat: combat(0, 60), TalentsKnown: true,
				Talents: []int64{14751}, Casts: casts},
			verdict:  VerdictInconclusive,
			wantLine: []string{"talent data is partial", "Holy Nova (Holy Nova, 73.9% of holy): not in the recorded talent learning"},
		},
		{
			name: "a partial marker kit: a cast talent and an unproven sibling is INCONCLUSIVE",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 400,
				ActiveKnown: true, Combat: combat(0, 400), TalentsKnown: true,
				Talents: []int64{15237},
				Casts:   map[string]int{"Renew": 4, "Holy Nova": 3}},
			verdict:  VerdictInconclusive,
			wantLine: []string{"Holy Nova (Holy Nova, 73.9% of holy): situational in the spell lists", "Inner Focus (Inner Focus, 91.8% of holy): not in the recorded talent learning"},
		},
		{
			name: "a complete build: a talent the bot does not have is not expected",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 60,
				ActiveKnown: true, Combat: combat(0, 60), TalentsKnown: true, TalentsComplete: true,
				Talents: []int64{14751}, Casts: casts},
			verdict:  VerdictPass,
			wantLine: []string{"Holy Nova: not trained, so not expected", "Inner Focus: cooldown 180 s"},
		},
		{
			name: "a long cooldown in a long fight fits, and stays report only",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 400,
				ActiveKnown: true, Combat: combat(0, 400), TalentsKnown: true, TalentsComplete: true,
				Talents: []int64{14751}, Casts: casts},
			verdict:  VerdictInconclusive,
			wantLine: []string{"Inner Focus (Inner Focus, 91.8% of holy): situational in the spell lists"},
		},
		{
			name: "a long cooldown in a short fight is not expected",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 60,
				ActiveKnown: true, Combat: combat(0, 60), TalentsKnown: true, TalentsComplete: true,
				Talents: []int64{14751, 15237},
				Casts:   map[string]int{"Renew": 4, "Flash Heal": 6, "Holy Nova": 1}},
			verdict:  VerdictInconclusive,
			wantLine: []string{"Inner Focus: cooldown 180 s, longer than every combat window"},
		},
		{
			name: "a 180 s cooldown is not expected in a 90 s fight",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 90,
				ActiveKnown: true, Combat: combat(0, 90), TalentsKnown: true, TalentsComplete: true,
				Talents: []int64{14751}, Casts: casts},
			verdict:  VerdictPass,
			wantLine: []string{"Inner Focus: cooldown 180 s, longer than every combat window"},
		},
		{
			name: "a cooldown is judged per combat window, not per summed repeat",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 120,
				ActiveKnown: true, Combat: []CombatWindow{{From: 0, To: 60}, {From: 100, To: 160}},
				TalentsKnown: true, TalentsComplete: true,
				Talents: []int64{14751}, Casts: casts},
			verdict:  VerdictPass,
			wantLine: []string{"Inner Focus: cooldown 180 s, longer than every combat window the rule judges (longest 60 s)"},
		},
		{
			name: "no talent data reports, never fails",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 60,
				ActiveKnown: true, Combat: combat(0, 60), Casts: casts},
			verdict:  VerdictInconclusive,
			wantLine: []string{"no talent data in the record", "Holy Nova (Holy Nova, 73.9% of holy)"},
		},
		{
			name: "a passive talent is no expectation",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 60,
				ActiveKnown: true, Combat: combat(0, 60), TalentsKnown: true, TalentsComplete: true,
				Talents: []int64{14901}, Casts: casts},
			verdict:  VerdictPass,
			wantLine: []string{"Holy Nova: not trained, so not expected", "Inner Focus: cooldown 180 s"},
			wantNot:  []string{"Spiritual Guidance"},
		},
		{
			name: "no proven combat says nothing",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60,
				TalentsKnown: true, TalentsComplete: true, Talents: []int64{15237}, Casts: casts},
			verdict:  VerdictInconclusive,
			wantLine: []string{"no combat participation in the record", "Holy Nova (Holy Nova, 73.9% of holy)"},
		},
		{
			name: "a short combat window is not a fight",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 10,
				ActiveKnown: true, Combat: combat(0, 10), TalentsKnown: true, TalentsComplete: true,
				Talents: []int64{15237}, Casts: casts},
			verdict:  VerdictInconclusive,
			wantLine: []string{"none of the 3 common talents' spells fits a 10 s combat window", "no combat window reached the 30 s"},
		},
		{
			name: "a long repeat with short combat is not judged",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 60, ActiveS: 20,
				ActiveKnown: true, Combat: []CombatWindow{{From: 0, To: 10}, {From: 500, To: 510}},
				TalentsKnown: true, TalentsComplete: true,
				Talents: []int64{15237}, Casts: casts},
			verdict:  VerdictInconclusive,
			wantLine: []string{"no combat window reached the 30 s"},
		},
		{
			name: "a level with no points",
			rec: Record{Class: "priest", Spec: "holy", Role: "healer", Level: 5, ActiveS: 60,
				ActiveKnown: true, Combat: combat(0, 60), TalentsKnown: true, Casts: casts},
			verdict:  VerdictInconclusive,
			wantLine: []string{"level 5 has no talent points"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := talentRow(tc.rec, in)
			if row.Verdict != tc.verdict || row.K != tc.k || row.N != tc.n {
				t.Fatalf("verdict %s k/n %d/%d, want %s %d/%d (%s)", row.Verdict, row.K, row.N, tc.verdict, tc.k, tc.n, row.Reason)
			}
			joined := strings.Join(row.Evidence, "\n") + "\n" + row.Reason
			for _, want := range tc.wantLine {
				if !strings.Contains(joined, want) {
					t.Fatalf("evidence %q does not name %q", joined, want)
				}
			}
			for _, not := range tc.wantNot {
				if strings.Contains(joined, not) {
					t.Fatalf("evidence %q names %q, which is not an expectation", joined, not)
				}
			}
			if tc.verdict != VerdictPass && row.Reason == "" {
				t.Fatal("a non-pass rule has no reason")
			}
		})
	}
}

func TestLevelGroup(t *testing.T) {
	for level, want := range map[int]string{
		5: "", 10: "10-19", 19: "10-19", 20: "20-29", 39: "30-39", 40: "40-49", 50: "50-59", 59: "50-59", 60: "60", 70: "60",
	} {
		if got := LevelGroup(level); got != want {
			t.Errorf("level %d: group %q, want %q", level, got, want)
		}
	}
}

// TestSpecNameAndFamily pins the naming and stat-family maps the rules read.
func TestSpecNameAndFamily(t *testing.T) {
	for spec, want := range map[string]string{
		"bm": "beast-mastery", "MM": "marksmanship", "assa": "assassination", "boomkin": "balance",
		"cat": "feral", "bear": "feral", "resto": "restoration", "holy": "holy",
	} {
		if got := SpecName(spec); got != want {
			t.Errorf("%q: %q, want %q", spec, got, want)
		}
	}
	for stat, want := range map[string]string{
		"Spell Power": FamilyCaster, "Intellect": FamilyCaster, "Healing": FamilyCaster, "Spirit": FamilyCaster,
		"Agility": FamilyAgility, "Attack Power": FamilyAgility,
		"Strength": FamilyStrength, "Defense": FamilyStrength,
		// Stamina says nothing about the family: every priority lists it, for survivability.
		"Stamina": FamilyAny, "Mana per 5": FamilyCaster,
		"Crit": FamilyAny, "Hit": FamilyAny,
	} {
		if got := StatFamily(stat); got != want {
			t.Errorf("%s: %q, want %q", stat, got, want)
		}
	}
	family := SpecFamily([]aotc.StatWeight{{Stat: "Hit"}, {Stat: "Agility"}, {Stat: "Stamina"}})
	if family != FamilyAgility {
		t.Errorf("priority family %q, want %q", family, FamilyAgility)
	}
	// A stamina-first feral priority resolves to its first family stat, Agility, not to Strength.
	if family := SpecFamily([]aotc.StatWeight{{Stat: "Stamina"}, {Stat: "Agility"}, {Stat: "Strength"}}); family != FamilyAgility {
		t.Errorf("feral priority family %q, want %q", family, FamilyAgility)
	}
}

// TestOracleSpec pins the oracle spec mapping for the splits a bench spec word hides.
func TestOracleSpec(t *testing.T) {
	for _, c := range []struct{ class, spec, role, want string }{
		{"druid", "bear", "tank", "feral_bear"},
		{"druid", "cat", "dps", "feral_cat"},
		{"druid", "restoration", "healer", "restoration"},
		{"hunter", "bm", "dps", "beast_mastery"},
		{"warrior", "fury", "tank", "fury_tank"},
		{"priest", "holy", "healer", "holy"},
	} {
		if got := OracleSpec(c.class, c.spec, c.role); got != c.want {
			t.Errorf("%s/%s (%s): %q, want %q", c.class, c.spec, c.role, got, c.want)
		}
	}
}

// TestRowsOrder pins the two rows a repeat contributes, in card order.
func TestRowsOrder(t *testing.T) {
	in := testInputs(t, fixture())
	rows := Rows(Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
		Equipped: []Item{{Entry: 1}}}, in)
	if len(rows) != 2 || rows[0].ID != RuleGear || rows[1].ID != RuleTalents {
		t.Fatalf("rows %+v", rows)
	}
	doc := rows[0].Doc()
	if doc["source"] != "aotc.gg" || doc["id"] != RuleGear {
		t.Fatalf("doc %+v", doc)
	}
	if list, ok := doc["evidence"].([]string); !ok || len(list) != 1 {
		t.Fatalf("evidence %+v", doc["evidence"])
	}
}
