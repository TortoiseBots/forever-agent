package playdata

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/data/aotc"
)

// gearRow judges one repeat's equipped items against what the spec's players wear at 60 (rule
// AOTC-GEAR-FITS-SPEC). It names the slot, the item and the reason for every item that is not fine;
// the verdict is FAIL only for a clear mismatch, INCONCLUSIVE when an item could not be judged (and
// for a record with no item data at all), PASS otherwise.
func gearRow(rec Record, in Inputs) Row {
	row := Row{ID: RuleGear, Verdict: VerdictInconclusive}
	class, spec := strings.ToLower(strings.TrimSpace(rec.Class)), SpecName(rec.Spec)
	if in.Tables == nil {
		row.Reason = "no player data tables"
		return row
	}
	if !in.Tables.SpecKnown(class, spec) {
		row.Reason = fmt.Sprintf("no player data for %s/%s (spec-population.csv)", class, rec.Spec)
		return row
	}
	if rec.Level != GearLevel {
		row.Reason = fmt.Sprintf("player gear data is level %d only (bot level %d)", GearLevel, rec.Level)
		return row
	}
	if !rec.EquippedKnown {
		row.Reason = "no equipped items in the record (the serve has no equipment read and the ledger has no equip rows)"
		return row
	}
	if len(rec.Equipped) == 0 {
		row.Reason = "the record proves no equipped items (nothing equipped or nothing read back)"
		return row
	}
	priority := in.Tables.StatPriorityFor(class, spec)
	top := priorityNames(priority, PriorityStats)
	families := topFamilies(top)

	var evidence []string
	var fine, unproven, mismatched int
	for _, raw := range rec.Equipped {
		item, hasSlot := resolve(raw, in.Tables)
		if !hasSlot {
			unproven++
			evidence = append(evidence, fmt.Sprintf("slot unknown: %s: no slot in the item tables, so no spec list to judge it against",
				itemLabel(item)))
			continue
		}
		common := in.Tables.GearFor(class, spec, item.Slot)
		if worn := wornBy(common, item); worn != nil {
			fine++
			evidence = append(evidence, fmt.Sprintf("%s: %s: fine, worn by %.1f%% of %s %s players",
				item.Slot, itemLabel(item), worn.Pct, class, spec))
			continue
		}
		if len(item.Stats) > 0 {
			if ok, why := statsMatch(item.Stats, top); ok {
				fine++
				evidence = append(evidence, fmt.Sprintf("%s: %s: fine, its stats (%s) match the priority (%s)",
					item.Slot, itemLabel(item), strings.Join(item.Stats, ", "), strings.Join(top, ", ")))
			} else {
				mismatched++
				evidence = append(evidence, fmt.Sprintf("%s: %s: wrong for %s/%s, %s",
					item.Slot, itemLabel(item), class, spec, why))
			}
			continue
		}
		if len(families) > 0 {
			if other, ok := otherFamilyWearers(in.Tables, item); ok {
				inPriority := false
				for family := range other.families {
					if families[family] {
						inPriority = true
					}
				}
				if !inPriority {
					mismatched++
					evidence = append(evidence, fmt.Sprintf("%s: %s: wrong for %s/%s, commonly worn by %s; none of their stat families (%s) is in its priority (%s)",
						item.Slot, itemLabel(item), class, spec, strings.Join(other.wearers, ", "), familyLabel(other.families), strings.Join(top, ", ")))
					continue
				}
				if len(other.families) == 1 {
					// The wearers' one family is among the spec's own top stats (a feral in rogue
					// agility leather): the item cannot be called contrary.
					fine++
					evidence = append(evidence, fmt.Sprintf("%s: %s: fine, worn only by %s, a family in its priority (%s)",
						item.Slot, itemLabel(item), strings.Join(other.wearers, ", "), strings.Join(top, ", ")))
					continue
				}
				// More than one wearer family, one of them in the priority: not evidence against
				// the item, but not proof for it either.
			}
		}
		unproven++
		evidence = append(evidence, fmt.Sprintf("%s: %s: not among the %s items %s players wear and the record carries no item stats",
			item.Slot, itemLabel(item), item.Slot, spec))
	}
	row.N = int64(len(rec.Equipped))
	row.K = int64(fine)
	row.Evidence = sortedEvidence(evidence)
	switch {
	case mismatched > 0:
		row.Verdict = VerdictFail
		row.Reason = fmt.Sprintf("%d of %d equipped items are a clear mismatch", mismatched, len(rec.Equipped))
	case unproven > 0:
		row.Verdict = VerdictInconclusive
		row.Reason = fmt.Sprintf("%d of %d equipped items could not be judged (no item data)", unproven, len(rec.Equipped))
	default:
		row.Verdict = VerdictPass
		row.Reason = fmt.Sprintf("all %d equipped items fit %s/%s", len(rec.Equipped), class, spec)
	}
	return row
}

// itemLabel is how a rule line names an item: its name and (when known) its id.
func itemLabel(item Item) string {
	if item.Entry > 0 {
		return fmt.Sprintf("%s (%d)", item.Name, item.Entry)
	}
	return item.Name
}

// wornBy returns the spec's gear row for the item (matched by id, else by name), or nil.
func wornBy(common []aotc.GearItem, item Item) *aotc.GearItem {
	for i := range common {
		if item.Entry > 0 && common[i].ID == item.Entry {
			return &common[i]
		}
	}
	if item.Name == "" {
		return nil
	}
	for i := range common {
		if strings.EqualFold(common[i].Name, item.Name) {
			return &common[i]
		}
	}
	return nil
}

// priorityNames is the first n stats of a priority, in order (fewer when the spec lists fewer).
func priorityNames(priority []aotc.StatWeight, n int) []string {
	var out []string
	for _, w := range priority {
		if len(out) >= n {
			break
		}
		out = append(out, w.Stat)
	}
	return out
}

// topFamilies is the set of stat families among the spec's top priority stats (the first
// PriorityStats of the priority): an item whose wearers name one of them is no clear mismatch,
// because the spec wants that family too.
func topFamilies(top []string) map[string]bool {
	out := map[string]bool{}
	for _, stat := range top {
		if family := StatFamily(stat); family != FamilyAny {
			out[family] = true
		}
	}
	return out
}

// statsMatch reports whether an item's own stats include at least one of the spec's top priority
// stats (case-insensitive), and when not, the reason.
func statsMatch(stats, top []string) (bool, string) {
	for _, stat := range stats {
		for _, want := range top {
			if strings.EqualFold(strings.TrimSpace(stat), want) {
				return true, ""
			}
		}
	}
	return false, fmt.Sprintf("its stats (%s) match none of the priority (%s)", strings.Join(stats, ", "), strings.Join(top, ", "))
}

// otherFamily is the contrary-wearer reading of an item: the union of the stat families of its
// common wearers, in the item's own slot, with those wearers stated for the evidence line.
type otherFamily struct {
	families map[string]bool
	wearers  []string
}

// familyLabel sorts a family set for an evidence line.
func familyLabel(families map[string]bool) string {
	names := make([]string, 0, len(families))
	for family := range families {
		names = append(names, family)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// otherFamilyWearers reports the specs that commonly wear the item (GearFamilyShare) in its own
// slot, and the union of their stat families: that is the evidence about its primary stats. ok is
// false when the wearers are too few or a wearer spec's family is unknown.
func otherFamilyWearers(t Tables, item Item) (otherFamily, bool) {
	info, ok := t.Item(item.Entry)
	if !ok {
		return otherFamily{}, false
	}
	perSpec := map[string]aotc.ItemWearer{}
	for _, w := range info.Wearers {
		if w.Population != aotc.PopulationBestGeared || !strings.EqualFold(w.Slot, item.Slot) || w.Pct < GearFamilyShare {
			continue
		}
		key := w.Class + "/" + w.Spec
		if prev, seen := perSpec[key]; !seen || w.Pct > prev.Pct {
			perSpec[key] = w
		}
	}
	out := otherFamily{families: map[string]bool{}}
	for key, w := range perSpec {
		family := SpecFamily(t.StatPriorityFor(w.Class, w.Spec))
		if family == FamilyAny {
			return otherFamily{}, false
		}
		out.families[family] = true
		out.wearers = append(out.wearers, fmt.Sprintf("%s %.1f%%", key, w.Pct))
	}
	if len(out.wearers) < GearMismatchSpecs {
		return otherFamily{}, false
	}
	sort.Strings(out.wearers)
	return out, true
}
