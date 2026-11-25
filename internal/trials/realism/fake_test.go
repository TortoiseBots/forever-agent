package realism

import "github.com/pjerra/forever-agent/data/aotc"

// fakeTables is a small player table for the tests that need a value exactly on a threshold line.
// The shipped tables have no pick at exactly 50%, no slot at exactly a dead-zone line, so the
// boundary tests write the one row they need here; the data-driven tests read Default().
type fakeTables struct {
	specs    map[string]bool
	picks    []aotc.TalentPick
	splits   map[string][]aotc.TalentSplit
	enchants []aotc.Enchant
	priority map[string][]aotc.StatWeight
	gear     map[string][]aotc.GearItem
	items    map[int64]aotc.ItemInfo
}

func (f *fakeTables) SpecKnown(class, spec string) bool { return f.specs[class+"|"+spec] }

func (f *fakeTables) CommonTalents(class, spec, levels string, minShare float64) []aotc.TalentPick {
	var out []aotc.TalentPick
	for _, p := range f.picks {
		if p.Class == class && p.Spec == spec && p.Levels == levels && p.PickPct >= minShare {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeTables) SplitsFor(class, spec string) []aotc.TalentSplit {
	return f.splits[class+"|"+spec]
}

func (f *fakeTables) EnchantsFor(class, spec, slot string) []aotc.Enchant {
	var out []aotc.Enchant
	for _, e := range f.enchants {
		if e.Class == class && e.Spec == spec && e.Slot == slot {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeTables) StatPriorityFor(class, spec string) []aotc.StatWeight {
	return f.priority[class+"|"+spec]
}

func (f *fakeTables) GearFor(class, spec, slot string) []aotc.GearItem {
	var out []aotc.GearItem
	for _, g := range f.gear[class+"|"+spec] {
		if slot == "" || g.Slot == slot {
			out = append(out, g)
		}
	}
	return out
}

func (f *fakeTables) GearRows(class, spec string) []aotc.GearItem {
	return f.gear[class+"|"+spec]
}

func (f *fakeTables) Item(id int64) (aotc.ItemInfo, bool) {
	info, ok := f.items[id]
	return info, ok
}
