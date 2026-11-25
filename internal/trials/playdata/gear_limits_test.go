package playdata

// The gear rule's round-1 limits, measured on the shipped tables instead of asserted from memory:
// what a spec's own priority rows make of it, the known false failure on real hunter-worn agility
// items, and whether the largest-family clause changes any verdict on this data at all.
//
// The rule's readings come from the data alone (no item stats, no armour type), so these tests pin
// today's behaviour where the data is the whole evidence. A data refresh that moves one of them
// fails here with the spec, slot or item named.

import (
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/data/aotc"
)

// TestGearSpecFamilyReadings pins what each spec's family set is, from its own priority rows:
// every family among its top PriorityStats rows. The round-1 text called enhancement a hybrid
// that is "not read as a pure caster"; its rows are Stamina, Intellect and Spell Power, so it IS
// read as a caster (its own players wear caster mail), and the rule keeps catching a shaman in
// warrior plate because of it. Protection and retribution paladins keep strength in their set, so
// there the hybrid reading holds.
func TestGearSpecFamilyReadings(t *testing.T) {
	tables := aotc.Default()
	reader := newFamilyReader(tables)
	cases := []struct {
		class, spec string
		want        []string
		why         string
	}{
		{"shaman", "enhancement", []string{"caster"}, "stamina, intellect and spell power are its three priority rows, and stamina names no family"},
		{"paladin", "protection", []string{"caster", "strength"}, "intellect and strength are both in its three rows"},
		{"paladin", "retribution", []string{"caster", "strength"}, "strength and intellect are both in its three rows"},
		{"druid", "feral", []string{"agility", "strength"}, "agility and strength are both in its three rows"},
		{"hunter", "marksmanship", []string{"agility"}, "agility is the only family in its rows"},
		{"priest", "holy", []string{"caster"}, "healing and spell power are caster families"},
	}
	for _, tc := range cases {
		t.Run(tc.class+"/"+tc.spec, func(t *testing.T) {
			got := reader.specFamilies(tc.class, tc.spec)
			var names []string
			for family := range got {
				names = append(names, family)
			}
			sort.Strings(names)
			sort.Strings(tc.want)
			if strings.Join(names, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("families %v, want %v (%s)", names, tc.want, tc.why)
			}
		})
	}
}

// TestGearEnhancementHunterOnlyItems pins the rule's known false failure on real items: an
// enhancement shaman in an agility piece only hunters wear. The piece's family is agility, and the
// shaman's own slot data names a caster family above the floor in Wrist, Hands, Waist and Feet, so
// those four slots are contrary; the same kind of piece is FINE in Shoulders, Chest and Legs, where
// the shaman's own rows name agility too. That is 4 of the 8 armour slots (4 of the 7 that have
// such an item at all): the Head pieces hunters wear are also in the shaman's own tables, so rule
// (a) settles them and they cannot show the family reading.
//
// Each case's item reading is checked first: it must be agility alone, so a PASS below is a rule-(a)
// pass and a FAIL is the family comparison, never a habit of the test.
func TestGearEnhancementHunterOnlyItems(t *testing.T) {
	tables := aotc.Default()
	in := Inputs{Tables: tables}
	reader := newFamilyReader(tables)
	cases := []struct {
		slot  string
		entry int64
		want  string
		why   string
	}{
		{"Shoulders", 16679, VerdictPass, "the shaman's own shoulder rows name agility"},
		{"Chest", 252483, VerdictPass, "the shaman's own chest rows name agility"},
		{"Wrist", 226885, VerdictFail, "its own wrist rows name a caster family only"},
		{"Hands", 226883, VerdictFail, "its own hand rows name a caster family only"},
		{"Waist", 272397, VerdictFail, "its own waist rows name a caster family only"},
		{"Legs", 17754, VerdictPass, "the shaman's own leg rows name agility"},
		{"Feet", 16675, VerdictFail, "its own feet rows name a caster family only"},
	}
	failing := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.slot, func(t *testing.T) {
			families, why, known := reader.itemReading(Item{Entry: tc.entry, Slot: tc.slot})
			if !known {
				t.Fatalf("item %d in %s has no family reading: %s", tc.entry, tc.slot, why)
			}
			if len(families) != 1 || !families[FamilyAgility] {
				t.Fatalf("item %d in %s reads %v, want agility alone (%s)", tc.entry, tc.slot, families, why)
			}
			row := gearRow(Record{Class: "shaman", Spec: "enhancement", Level: GearLevel, EquippedKnown: true,
				Equipped: []Item{{Entry: tc.entry, Slot: tc.slot}}}, in)
			if row.Verdict != tc.want {
				t.Fatalf("%s item %d is %s, want %s (%s): %s\n%s", tc.slot, tc.entry, row.Verdict,
					tc.want, tc.why, row.Reason, strings.Join(row.Evidence, "\n"))
			}
			if tc.want == VerdictFail {
				failing[tc.slot] = true
			}
		})
	}
	if len(failing) != 4 {
		t.Fatalf("%d slots fail, want the four known ones (Wrist, Hands, Waist, Feet): %v", len(failing), failing)
	}
	for _, slot := range []string{"Wrist", "Hands", "Waist", "Feet"} {
		if !failing[slot] {
			t.Fatalf("%s is not among the failing slots: %v", slot, failing)
		}
	}
}

// TestGearHeadHunterItemIsFineByItsOwnData is the other half of the count: the armour slot that has
// no shaman-free hunter item is settled by rule (a), because the shaman's own tables name the piece
// too. It pins why the known list is "4 of the 7" and not "4 of the 8".
func TestGearHeadHunterItemIsFineByItsOwnData(t *testing.T) {
	tables := aotc.Default()
	in := Inputs{Tables: tables}
	const entry = 16677 // Beaststalker's Cap: a hunter agility helm the shaman's own rows also name
	owned := false
	for _, g := range tables.GearRows("shaman", "enhancement") {
		if g.ID == entry {
			owned = true
		}
	}
	if !owned {
		t.Fatalf("item %d is not in the shaman's own gear rows any more; the known list's slot count changes", entry)
	}
	row := gearRow(Record{Class: "shaman", Spec: "enhancement", Level: GearLevel, EquippedKnown: true,
		Equipped: []Item{{Entry: entry, Slot: "Head"}}}, in)
	if row.Verdict != VerdictPass {
		t.Fatalf("item %d is %s, want PASS by rule (a) (%s)", entry, row.Verdict, row.Reason)
	}
	if !strings.Contains(strings.Join(row.Evidence, "\n"), "include it") {
		t.Fatalf("the item is not fine by its own data: %s", strings.Join(row.Evidence, "\n"))
	}
}

// floorOnlyFamiliesSet is weightedFamilies without the single-largest fallback: the families the
// share floor alone keeps. It exists so the largest-family clause can be measured on the shipped
// data instead of taken on trust.
func floorOnlyFamiliesSet(contribs []familyShare) map[string]bool {
	sums := map[string]float64{}
	var total float64
	for _, c := range contribs {
		for family := range c.families {
			sums[family] += c.share
			total += c.share
		}
	}
	out := map[string]bool{}
	for family, share := range sums {
		if share >= familyShareFloor*total-1e-9 {
			out[family] = true
		}
	}
	return out
}

// TestGearLargestFamilyClauseOnShippedTables measures the largest-family clause against every set
// the shipped tables produce: every spec's acceptable families in every slot it has rows for, and
// every item's own reading. On this data the clause adds no family anywhere, so the rule's verdicts
// are the share floor's alone.
//
// Why it cannot: a contribution's share is counted once per family it names and a spec's family set
// holds at most three families (caster, agility, strength), so the largest family always holds at
// least a third of the summed share — above familyShareFloor (0.20). The measured minimum is
// asserted here, so a floor raised past it, or a family set that grew, fails this test and the case
// then gets its own pin.
func TestGearLargestFamilyClauseOnShippedTables(t *testing.T) {
	tables := aotc.Default()
	reader := newFamilyReader(tables)
	minShare := 1.0
	check := func(label string, contribs []familyShare) {
		t.Helper()
		with, without := weightedFamilies(contribs), floorOnlyFamiliesSet(contribs)
		for family := range with {
			if !without[family] {
				t.Fatalf("%s: the largest-family clause accepts %q; want the share floor alone", label, family)
			}
		}
		// The ratio is measured over the rows that carry weight: a row at 0 % names nothing that
		// could be the largest family.
		sums := map[string]float64{}
		var total float64
		for _, c := range contribs {
			if c.share <= 0 {
				continue
			}
			for family := range c.families {
				sums[family] += c.share
				total += c.share
			}
		}
		if total <= 0 {
			return
		}
		largest := 0.0
		for _, share := range sums {
			if share > largest {
				largest = share
			}
		}
		if ratio := largest / total; ratio < minShare {
			minShare = ratio
		}
	}

	// The slot side: every spec, every slot its own rows name.
	for _, spec := range tables.SpecList() {
		slots := map[string]bool{}
		for _, g := range tables.GearRows(spec.Class, spec.Spec) {
			if g.SetSlot() || g.ID <= 0 {
				continue
			}
			slots[strings.ToLower(g.Slot)] = true
		}
		for slot := range slots {
			var contribs []familyShare
			for _, g := range tables.GearRows(spec.Class, spec.Spec) {
				if g.SetSlot() || g.ID <= 0 || !strings.EqualFold(g.Slot, slot) {
					continue
				}
				if families := reader.slotFamilies(Item{Entry: g.ID, Name: g.Name, Slot: g.Slot}); len(families) > 0 {
					contribs = append(contribs, familyShare{share: g.Pct, families: families})
				}
			}
			check(spec.Class+"/"+spec.Spec+" "+slot, contribs)
		}
	}

	// The item side: every item's own reading, in every slot the tables place it.
	ids := map[int64]bool{}
	for _, spec := range tables.SpecList() {
		for _, g := range tables.GearRows(spec.Class, spec.Spec) {
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
			check("item "+strconv.FormatInt(id, 10)+" in "+slot, contribs)
		}
	}

	if minShare < 1.0/3.0-1e-9 {
		t.Fatalf("the smallest largest-family share on the shipped data is %.3f, under a third: the clause can bind and needs its own pin", minShare)
	}
	if familyShareFloor >= 1.0/3.0 {
		t.Fatalf("familyShareFloor is %.2f, at or above a third: the largest-family clause can bind now", familyShareFloor)
	}
}
