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
func (f *fakeTables) GearRows(class, spec string) []aotc.GearItem {
	return append([]aotc.GearItem(nil), f.gear[key(class, spec)]...)
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
				// Below GearFamilyShare and in another population: the spec's own data all the same.
				{Class: "priest", Spec: "holy", Population: aotc.PopulationAll, Slot: "Waist", Name: "Rare Sash", ID: 5, Pct: 0.4},
			},
			key("mage", "fire"): {
				{Class: "mage", Spec: "fire", Population: aotc.PopulationBestGeared, Slot: "Head", Name: "Fire Hat", ID: 11, Pct: 55},
			},
		},
		items: map[int64]aotc.ItemInfo{
			1: {ID: 1, Name: "Good Hat", Slots: []string{"Head"}, Wearers: []aotc.ItemWearer{
				{Class: "priest", Spec: "holy", Slot: "Head", Population: aotc.PopulationBestGeared, Pct: 40},
			}},
			5: {ID: 5, Name: "Rare Sash", Slots: []string{"Waist"}},
			11: {ID: 11, Name: "Fire Hat", Slots: []string{"Head"}, Wearers: []aotc.ItemWearer{
				{Class: "mage", Spec: "fire", Slot: "Head", Population: aotc.PopulationBestGeared, Pct: 55},
			}},
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
			wantLine: []string{"Head: Good Hat (1)", "the items priest holy players wear include it (Head, 40.0%)"},
		},
		{
			name: "fine by its own stats",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 9, Name: "Spirit Hood", Slot: "Head", Stats: []string{"Spirit", "Stamina"}}}},
			verdict: VerdictPass, k: 1, n: 1,
			wantLine: []string{"its stats (Spirit, Stamina)", "its families (caster)", "the priority tie-break lists (Healing, Spirit, Intellect)"},
		},
		{
			name: "clear mismatch by its stats",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 10, Name: "Strength Plate Helm", Slot: "Head", Stats: []string{"Strength", "Stamina"}}}},
			verdict: VerdictFail, k: 0, n: 1,
			wantLine: []string{"Strength Plate Helm", "its families (strength)", "are none of the families its players wear in Head (caster)"},
		},
		{
			name: "clear mismatch by its wearers",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 2}}},
			verdict: VerdictFail, k: 0, n: 1,
			wantLine: []string{"Plate Tank Helm (2)", "commonly worn by", "warrior/protection", "its families (strength)"},
		},
		{
			name: "a mage in a strength plate piece",
			rec: Record{Class: "mage", Spec: "fire", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 2}}},
			verdict: VerdictFail, k: 0, n: 1,
			wantLine: []string{"Plate Tank Helm (2)", "its families (strength)"},
		},
		{
			name: "mixed wearers naming a wanted family are no mismatch",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 3}}},
			verdict: VerdictPass, k: 1, n: 1,
			wantLine: []string{"Shared Tank Crown (3)", "its families (caster, strength)", "are among the families"},
		},
		{
			// No family in the stats: the record says nothing about whether the piece fits, so the
			// rule reports it rather than failing it.
			name: "stats naming no family are inconclusive",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 10, Name: "Stamina Belt", Slot: "Head", Stats: []string{"Stamina", "Crit"}}}},
			verdict: VerdictInconclusive, k: 0, n: 1,
			wantLine: []string{"Stamina Belt (10)", "its stats (Stamina, Crit) name no stat family"},
		},
		{
			name: "unlisted and no stats",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 4}}},
			verdict: VerdictInconclusive, k: 0, n: 1,
			wantLine: []string{"not among the Head items priest holy players wear", "the record carries no item stats and no common wearers"},
		},
		{
			// The lead case: data anywhere in the spec's own tables is fine, whatever the share or
			// the population (here 0.4% in all_60, another slot table from the equipped one).
			name: "own data at any share and population",
			rec: Record{Class: "priest", Spec: "holy", Level: 60, EquippedKnown: true,
				Equipped: []Item{{Entry: 5}}},
			verdict: VerdictPass, k: 1, n: 1,
			wantLine: []string{"Rare Sash (5)", "the items priest holy players wear include it (Waist, 0.4%)"},
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

// TestGearFitsSpecRealData pins the shipped tables: a feral bot in rogue/hunter agility leather and
// rings is no clear mismatch, an enhancement shaman in mail its own spec also wears is fine, and a
// mage in a strength plate piece is still a clear mismatch.
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

// TestGearFitsSpecEverySpecOwnGearPasses is the rule's property over the shipped data: every item
// every row of a spec's own gear tables names (both populations, any share, any slot) is FINE, so a
// record of all of them passes. It fails if the rule ever reads a spec's own data as a mismatch.
func TestGearFitsSpecEverySpecOwnGearPasses(t *testing.T) {
	tables := aotc.Default()
	in := Inputs{Tables: tables}
	for _, spec := range tables.SpecList() {
		t.Run(spec.Class+"/"+spec.Spec, func(t *testing.T) {
			var items []Item
			for _, g := range tables.GearRows(spec.Class, spec.Spec) {
				if g.SetSlot() || g.ID <= 0 {
					continue
				}
				items = append(items, Item{Entry: g.ID, Slot: g.Slot})
			}
			if len(items) == 0 {
				t.Fatal("no gear rows")
			}
			row := gearRow(Record{Class: spec.Class, Spec: spec.Spec, Level: GearLevel, EquippedKnown: true, Equipped: items}, in)
			if row.Verdict != VerdictPass || row.K != row.N {
				t.Fatalf("its own gear is %s %d/%d: %s\n%s", row.Verdict, row.K, row.N, row.Reason, strings.Join(row.Evidence, "\n"))
			}
		})
	}
}

// TestGearFitsSpecForeignItemsFail is the property's inverse: for six specs, a piece from a spec of
// another armour class and role (not in the judged spec's own data, worn at or above
// GearFamilyShare by the named spec) is a clear mismatch. Every case below is a plate, mail, leather
// or cloth piece whose family the judged spec's own players do not wear in that slot.
//
// Mutation check: inverting the family decision in gearRow — accepting an item when its families do
// NOT intersect the acceptable families — makes every case here PASS instead of FAIL, so the
// `row.Verdict != VerdictFail` assertion catches an inverted family test. Deleting or skipping the
// acceptable-set comparison is caught the same way.
func TestGearFitsSpecForeignItemsFail(t *testing.T) {
	tables := aotc.Default()
	in := Inputs{Tables: tables}
	cases := []struct {
		class, spec         string
		item                int64
		fromClass, fromSpec string
		what                string
	}{
		{"mage", "fire", 13166, "warrior", "protection", "plate tank shoulders"},
		{"priest", "holy", 16707, "rogue", "assassination", "leather dps helm"},
		{"warrior", "protection", 15067, "druid", "balance", "leather caster shoulders"},
		{"hunter", "marksmanship", 13969, "paladin", "holy", "plate healer bracers"},
		{"rogue", "assassination", 11842, "shaman", "elemental", "mail caster shoulders"},
		{"shaman", "enhancement", 16736, "warrior", "protection", "plate tank belt"},
	}
	for _, tc := range cases {
		name := tc.class + "/" + tc.spec + " in " + tc.what
		t.Run(name, func(t *testing.T) {
			for _, g := range tables.GearRows(tc.class, tc.spec) {
				if g.ID == tc.item {
					t.Fatalf("item %d is in %s/%s own data (%s %s), so it is not foreign", tc.item, tc.class, tc.spec, g.Population, g.Slot)
				}
			}
			info, ok := tables.Item(tc.item)
			if !ok {
				t.Fatalf("item %d is not in the item tables", tc.item)
			}
			var share float64
			var slot string
			for _, w := range info.Wearers {
				if w.Class == tc.fromClass && w.Spec == tc.fromSpec && w.Pct >= GearFamilyShare && w.Pct > share {
					share, slot = w.Pct, w.Slot
				}
			}
			if share == 0 {
				t.Fatalf("item %d is not commonly worn by %s/%s", tc.item, tc.fromClass, tc.fromSpec)
			}
			row := gearRow(Record{Class: tc.class, Spec: tc.spec, Level: GearLevel, EquippedKnown: true,
				Equipped: []Item{{Entry: tc.item, Slot: slot}}}, in)
			if row.Verdict != VerdictFail {
				t.Fatalf("%s/%s in %s (%.1f%% of %s/%s) is %s, want FAIL: %s\n%s",
					tc.class, tc.spec, tc.what, share, tc.fromClass, tc.fromSpec, row.Verdict, row.Reason,
					strings.Join(row.Evidence, "\n"))
			}
		})
	}
}

// TestGearFitsSpecOutfits pins the outfits the share-weighted rule is meant to catch and the ones
// it must not, with real item ids from the shipped tables, one per armour slot the data has rows
// for. The slot's acceptable families are share-weighted (familyShareFloor), so a caster family one
// shared item names does not make spirit cloth fine for a warrior.
//
// The must-catch outfits fail in every armour slot of the outfit, which is the "at least 8" the rule
// needs. The feral in plate case is a known limit, not a pass: the feral's own data names agility and
// strength, and the tables carry no armour type, so a strength plate piece shares a family with the
// feral's own pieces (see gear.go). It pins today's FINE so the limit shows when item stats arrive.
// The must-not-fail outfits are the gear the spec's own players wear (rule (a)): agility mail and the
// caster-hybrid pieces for enhancement, protection paladin's own data, and agility leather for feral.
func TestGearFitsSpecOutfits(t *testing.T) {
	tables := aotc.Default()
	in := Inputs{Tables: tables}
	type outfitItem struct {
		slot  string
		entry int64
	}
	cases := []struct {
		name        string
		class, spec string
		want        string
		items       []outfitItem
	}{
		{
			// Cloth caster pieces (mage/priest/warlock) a warrior does not wear.
			name: "warrior in spirit cloth", class: "warrior", spec: "fury", want: VerdictFail,
			items: []outfitItem{
				{"Head", 22267}, {"Shoulders", 22405}, {"Chest", 13346}, {"Wrist", 226929},
				{"Hands", 226930}, {"Waist", 226934}, {"Legs", 13170}, {"Feet", 11822},
			},
		},
		{
			name: "rogue in cloth caster gear", class: "rogue", spec: "combat", want: VerdictFail,
			items: []outfitItem{
				{"Head", 22267}, {"Shoulders", 22405}, {"Chest", 13346}, {"Wrist", 226929},
				{"Hands", 226930}, {"Waist", 226934}, {"Legs", 13170}, {"Feet", 11822},
			},
		},
		{
			// Lightforge/Soulforge plate: a paladin's caster-and-strength pieces.
			name: "hunter in strength plate", class: "hunter", spec: "marksmanship", want: VerdictFail,
			items: []outfitItem{
				{"Head", 18718}, {"Shoulders", 16729}, {"Chest", 16726}, {"Wrist", 226970},
				{"Hands", 226975}, {"Waist", 16723}, {"Legs", 16728}, {"Feet", 16725},
			},
		},
		{
			// Valor/Heroism plate: warrior tank pieces, no caster family.
			name: "mage in strength plate", class: "mage", spec: "fire", want: VerdictFail,
			items: []outfitItem{
				{"Head", 20521}, {"Shoulders", 20517}, {"Chest", 16730}, {"Wrist", 226865},
				{"Hands", 226870}, {"Waist", 16736}, {"Legs", 16732}, {"Feet", 16734},
			},
		},
		{
			// Known limit: strength is one of the feral's own families, so the plate is FINE.
			name: "feral in plate (known limit: strength is a feral family)", class: "druid", spec: "feral", want: VerdictPass,
			items: []outfitItem{
				{"Head", 20521}, {"Shoulders", 20517}, {"Chest", 16730}, {"Wrist", 226865},
				{"Hands", 226870}, {"Waist", 16736}, {"Legs", 16732}, {"Feet", 16734},
			},
		},
		{
			// The data has no own agility item for Waist and Legs (the shaman's own rows there are
			// caster), so those two are the own caster pieces; the rest are its own agility mail.
			name: "enhancement in agility mail its own players wear", class: "shaman", spec: "enhancement", want: VerdictPass,
			items: []outfitItem{
				{"Head", 12587}, {"Shoulders", 14503}, {"Chest", 13944}, {"Wrist", 13211},
				{"Hands", 15063}, {"Waist", 22242}, {"Legs", 252486}, {"Feet", 13967},
			},
		},
		{
			name: "enhancement in its caster-hybrid pieces", class: "shaman", spec: "enhancement", want: VerdictPass,
			items: []outfitItem{
				{"Head", 20134}, {"Shoulders", 16669}, {"Chest", 16666}, {"Wrist", 227017},
				{"Hands", 227022}, {"Waist", 227018}, {"Legs", 14612}, {"Feet", 14616},
			},
		},
		{
			name: "protection paladin in its own data's gear", class: "paladin", spec: "protection", want: VerdictPass,
			items: []outfitItem{
				{"Head", 18718}, {"Shoulders", 16729}, {"Chest", 16726}, {"Wrist", 226985},
				{"Hands", 226990}, {"Waist", 16723}, {"Legs", 16728}, {"Feet", 16725},
			},
		},
		{
			// Rogue agility leather: the families the feral's own agility leather shares (some of these
			// pieces are also in the feral's own data, which rule (a) settles).
			name: "feral in agility leather", class: "druid", spec: "feral", want: VerdictPass,
			items: []outfitItem{
				{"Head", 19984}, {"Shoulders", 226826}, {"Chest", 16721}, {"Wrist", 226830},
				{"Hands", 226828}, {"Waist", 272395}, {"Legs", 226827}, {"Feet", 226831},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var equipped []Item
			fails := 0
			for _, it := range tc.items {
				if _, ok := tables.Item(it.entry); !ok {
					t.Fatalf("item %d is not in the item tables", it.entry)
				}
				item := Item{Entry: it.entry, Slot: it.slot}
				row := gearRow(Record{Class: tc.class, Spec: tc.spec, Level: GearLevel, EquippedKnown: true,
					Equipped: []Item{item}}, in)
				if row.Verdict != tc.want {
					t.Errorf("%s item %d is %s, want %s: %s\n%s", it.slot, it.entry, row.Verdict, tc.want,
						row.Reason, strings.Join(row.Evidence, "\n"))
				}
				if row.Verdict == VerdictFail {
					fails++
				}
				equipped = append(equipped, item)
			}
			if tc.want == VerdictFail && fails < 8 {
				t.Errorf("%d of %d armour slots fail, want at least 8", fails, len(tc.items))
			}
			if tc.want == VerdictPass && fails != 0 {
				t.Errorf("%d of %d armour slots fail, want none", fails, len(tc.items))
			}
			row := gearRow(Record{Class: tc.class, Spec: tc.spec, Level: GearLevel, EquippedKnown: true,
				Equipped: equipped}, in)
			if row.Verdict != tc.want {
				t.Errorf("the outfit is %s, want %s: %s\n%s", row.Verdict, tc.want, row.Reason,
					strings.Join(row.Evidence, "\n"))
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
	if families := priorityFamilies([]aotc.StatWeight{{Stat: "Hit"}, {Stat: "Agility"}, {Stat: "Stamina"}}); len(families) != 1 || !families[FamilyAgility] {
		t.Errorf("priority families %v, want only %q", families, FamilyAgility)
	}
	// A stamina-first feral priority names both of its physical families, not only the first.
	families := priorityFamilies([]aotc.StatWeight{{Stat: "Stamina"}, {Stat: "Agility"}, {Stat: "Strength"}})
	if len(families) != 2 || !families[FamilyAgility] || !families[FamilyStrength] {
		t.Errorf("feral priority families %v, want %q and %q", families, FamilyAgility, FamilyStrength)
	}
	// Only the top PriorityStats stats count: Intellect sixth in a physical priority adds nothing.
	if families := priorityFamilies([]aotc.StatWeight{
		{Stat: "Stamina"}, {Stat: "Agility"}, {Stat: "Strength"}, {Stat: "Attack Power"}, {Stat: "Crit"}, {Stat: "Intellect"},
	}); families[FamilyCaster] {
		t.Errorf("priority families %v, want no %q from beyond the top %d", families, FamilyCaster, PriorityStats)
	}
}

// TestWeightedFamilies pins the share weighting that replaced the acceptable-set union: a family
// below familyShareFloor of the summed weight is dropped unless it is the single largest, and the
// total is the sum of the family sums, so a hybrid contribution counts once per family it names.
func TestWeightedFamilies(t *testing.T) {
	set := func(names ...string) map[string]bool {
		out := map[string]bool{}
		for _, name := range names {
			out[name] = true
		}
		return out
	}
	cases := []struct {
		name     string
		contribs []familyShare
		want     []string
	}{
		{
			name:     "a family at the floor is acceptable",
			contribs: []familyShare{{share: 80, families: set("strength")}, {share: 20, families: set("caster")}},
			want:     []string{"caster", "strength"},
		},
		{
			name:     "a family below the floor is not",
			contribs: []familyShare{{share: 90, families: set("strength")}, {share: 10, families: set("caster")}},
			want:     []string{"strength"},
		},
		{
			name:     "the single largest family is acceptable below the floor",
			contribs: []familyShare{{share: 12, families: set("strength")}, {share: 8, families: set("a")}, {share: 8, families: set("b")}, {share: 8, families: set("c")}, {share: 8, families: set("d")}},
			want:     []string{"strength"},
		},
		{
			name:     "a tie at the top accepts nothing below the floor",
			contribs: []familyShare{{share: 10, families: set("a")}, {share: 10, families: set("b")}, {share: 10, families: set("c")}, {share: 10, families: set("d")}, {share: 10, families: set("e")}, {share: 10, families: set("f")}},
			want:     nil,
		},
		{
			name:     "one hybrid contribution names both of its families",
			contribs: []familyShare{{share: 20, families: set("strength", "agility")}},
			want:     []string{"agility", "strength"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := weightedFamilies(tc.contribs)
			if len(got) != len(tc.want) {
				t.Fatalf("families %v, want %v", got, tc.want)
			}
			for _, family := range tc.want {
				if !got[family] {
					t.Fatalf("families %v, want %v", got, tc.want)
				}
			}
		})
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

// TestGearFitsSpecEnhancementHunterOnlyMail pins the known false failure of the gear rule on real data: an enhancement
// shaman in mail only hunters wear. The item's family is agility, which the shaman's own slot data names in some armour
// slots but not in Wrist, Hands, Waist and Feet (its own rows there name a caster family above the floor, so those
// slots accept caster only). So the same kind of piece is FINE in Shoulders, Chest and Legs and FAILs in Wrist, Hands,
// Waist and Feet: 4 of the 8 armour slots (4 of the 7 that have a hunter-only agility item at all).
//
// The false failure is a data gap, not a threshold: the tables carry no item stats or armour type, so the rule cannot
// tell agility mail from a caster piece in those two slots. With the item read-out the slot's own armour family would
// settle it. The test pins today's verdicts so a threshold or data change shows up here (and so the manual's
// known-limits line is checked against the rule).
func TestGearFitsSpecEnhancementHunterOnlyMail(t *testing.T) {
	tables := aotc.Default()
	in := Inputs{Tables: tables}
	// One hunter-only agility item per armour slot that has one (worn at least GearFamilyShare by hunter specs only,
	// and absent from the shaman's own rows): the mail pieces of the Beaststalker's and Beastmaster's sets and their
	// data peers.
	cases := []struct {
		slot  string
		entry int64
		want  string
	}{
		{"Shoulders", 16679, VerdictPass}, // Beaststalker's Mantle
		{"Chest", 252483, VerdictPass},    // Dawn Armor
		{"Wrist", 226885, VerdictFail},    // Beastmaster's Bindings
		{"Hands", 226883, VerdictFail},    // Beastmaster's Gauntlets
		{"Waist", 272397, VerdictFail},    // Ranger's Belt
		{"Legs", 17754, VerdictPass},      // Infernal Trickster Leggings
		{"Feet", 16675, VerdictFail},      // Beaststalker's Boots
	}
	fails := 0
	for _, tc := range cases {
		if _, ok := tables.Item(tc.entry); !ok {
			t.Fatalf("item %d is not in the item tables", tc.entry)
		}
		row := gearRow(Record{Class: "shaman", Spec: "enhancement", Level: GearLevel, EquippedKnown: true,
			Equipped: []Item{{Entry: tc.entry, Slot: tc.slot}}}, in)
		if row.Verdict != tc.want {
			t.Errorf("%s item %d is %s, want %s: %s\n%s", tc.slot, tc.entry, row.Verdict, tc.want,
				row.Reason, strings.Join(row.Evidence, "\n"))
		}
		if row.Verdict == VerdictFail {
			fails++
		}
	}
	if fails != 4 {
		t.Errorf("%d of the %d slots fail, want the four known ones (Wrist, Hands, Waist, Feet)", fails, len(cases))
	}
}

// floorOnlyFamilies is weightedFamilies without the single-largest fallback: the families the share floor alone keeps.
// It exists so the largest-family clause can be measured against the shipped data.
func floorOnlyFamilies(contribs []familyShare) map[string]bool {
	sums := map[string]float64{}
	var total float64
	for _, c := range contribs {
		for family := range c.families {
			sums[family] += c.share
			total += c.share
		}
	}
	out := map[string]bool{}
	floor := familyShareFloor * total
	for family, share := range sums {
		if share >= floor-1e-9 {
			out[family] = true
		}
	}
	return out
}

// TestLargestFamilyClauseOnRealData measures the largest-family clause against every set the shipped tables produce:
// the acceptable families of every spec and slot, and every item's own reading. On this data the clause never adds a
// family (no set has a single largest family under the floor), so the rule's verdicts on real gear are the share
// floor's alone; the clause's own behaviour is pinned on synthetic shares by TestWeightedFamilies.
//
// The sweep is here so the claim can be checked, never assumed: a data refresh that makes the clause matter fails this
// test with the spec, slot or item named, and the case then gets its own pin instead of being folded into a claim.
func TestLargestFamilyClauseOnRealData(t *testing.T) {
	tables := aotc.Default()
	reader := newFamilyReader(tables)
	slots := []string{"Head", "Shoulders", "Chest", "Wrist", "Hands", "Waist", "Legs", "Feet", "Back", "Finger", "Neck", "Trinket", "Main Hand", "Off Hand"}
	for _, key := range tables.SpecList() {
		for _, slot := range slots {
			var contribs []familyShare
			for _, g := range tables.GearRows(key.Class, key.Spec) {
				if g.SetSlot() || g.ID <= 0 || !strings.EqualFold(g.Slot, slot) {
					continue
				}
				if families := reader.slotFamilies(Item{Entry: g.ID, Name: g.Name, Slot: g.Slot}); len(families) > 0 {
					contribs = append(contribs, familyShare{share: g.Pct, families: families})
				}
			}
			with, without := weightedFamilies(contribs), floorOnlyFamilies(contribs)
			for family := range with {
				if !without[family] {
					t.Errorf("%s/%s %s: the largest-family clause accepts %q; want the share floor alone", key.Class, key.Spec, slot, family)
				}
			}
		}
	}
	// The item side: every item's own reading is the floor's alone too.
	ids := map[int64]bool{}
	for _, key := range tables.SpecList() {
		for _, g := range tables.GearRows(key.Class, key.Spec) {
			if !g.SetSlot() && g.ID > 0 {
				ids[g.ID] = true
			}
		}
	}
	for id := range ids {
		info, ok := tables.Item(id)
		if !ok {
			continue
		}
		for _, slot := range info.Slots {
			var contribs []familyShare
			for _, w := range info.Wearers {
				if !strings.EqualFold(w.Slot, slot) || w.Pct < GearFamilyShare {
					continue
				}
				contribs = append(contribs, familyShare{share: w.Pct, families: reader.specFamilies(w.Class, w.Spec)})
			}
			with, without := weightedFamilies(contribs), floorOnlyFamilies(contribs)
			for family := range with {
				if !without[family] {
					t.Errorf("item %d (%s) in %s: the largest-family clause accepts %q; want the share floor alone", id, info.Name, slot, family)
				}
			}
		}
	}
}
