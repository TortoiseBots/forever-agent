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
//
// The rule is data-driven: an item any row of the spec's own gear tables names (any population, any
// share, any slot) is fine. An item outside that data is contrary only when its family is known and
// none of the families the spec's own players wear in that slot is its own; no family data is
// INCONCLUSIVE, never a FAIL.
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
	// The priority is the explanation's tie-breaker only: the verdict's family set comes from the
	// items the spec's own players wear, not from the stat order.
	top := priorityNames(in.Tables.StatPriorityFor(class, spec), PriorityStats)
	reader := newFamilyReader(in.Tables)
	ownGear := in.Tables.GearRows(class, spec)

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
		if worn := wornAnywhere(ownGear, item); worn != nil {
			fine++
			evidence = append(evidence, fmt.Sprintf("%s: %s: fine, the items %s %s players wear include it (%s, %.1f%%)",
				item.Slot, itemLabel(item), class, spec, worn.Slot, worn.Pct))
			continue
		}
		families, why, known := reader.itemReading(item)
		if !known {
			unproven++
			evidence = append(evidence, fmt.Sprintf("%s: %s: not among the %s items %s %s players wear, and %s",
				item.Slot, itemLabel(item), item.Slot, class, spec, why))
			continue
		}
		acceptable := reader.acceptable(class, spec, item.Slot)
		if len(acceptable) == 0 {
			unproven++
			evidence = append(evidence, fmt.Sprintf("%s: %s: not among the %s items %s %s players wear, and their items in that slot name no stat family to judge it against",
				item.Slot, itemLabel(item), item.Slot, class, spec))
			continue
		}
		if intersects(families, acceptable) {
			fine++
			evidence = append(evidence, fmt.Sprintf("%s: %s: fine, %s; its families (%s) are among the families %s %s players wear in %s (%s); the priority tie-break lists %s",
				item.Slot, itemLabel(item), why, familyLabel(families), class, spec, item.Slot,
				familyLabel(acceptable), priorityLabel(top)))
			continue
		}
		mismatched++
		evidence = append(evidence, fmt.Sprintf("%s: %s: wrong for %s/%s, %s; its families (%s) are none of the families its players wear in %s (%s); the priority tie-break lists %s",
			item.Slot, itemLabel(item), class, spec, why, familyLabel(families), item.Slot,
			familyLabel(acceptable), priorityLabel(top)))
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

// wornAnywhere returns the spec's gear row for the item anywhere in its tables (any population, any
// share and any slot; matched by id, else by name), or nil.
func wornAnywhere(rows []aotc.GearItem, item Item) *aotc.GearItem {
	var best *aotc.GearItem
	for i := range rows {
		g := &rows[i]
		if g.SetSlot() {
			continue
		}
		match := item.Entry > 0 && g.ID == item.Entry
		if !match && item.Name != "" {
			match = strings.EqualFold(g.Name, item.Name)
		}
		if match && (best == nil || g.Pct > best.Pct) {
			best = g
		}
	}
	return best
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

// priorityFamilies is the set of families among a priority's top PriorityStats stats: every one of
// them, not only the first stat with a family (a hybrid spec wants more than one family).
func priorityFamilies(priority []aotc.StatWeight) map[string]bool {
	families := map[string]bool{}
	for _, stat := range priorityNames(priority, PriorityStats) {
		if family := StatFamily(stat); family != FamilyAny {
			families[family] = true
		}
	}
	return families
}

// priorityLabel joins a priority's top stats for the explanation text.
func priorityLabel(top []string) string {
	if len(top) == 0 {
		return "no stats"
	}
	return "(" + strings.Join(top, ", ") + ")"
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

// intersects reports whether two family sets share a family.
func intersects(a, b map[string]bool) bool {
	for family := range a {
		if b[family] {
			return true
		}
	}
	return false
}

// familyReader reads the gear rule's family data from the tables once per repeat: a spec's own
// families (the families of the top PriorityStats stats of each spec that wears an item), the
// families its players wear in one slot, and an item's readable families.
type familyReader struct {
	t     Tables
	specs map[string]map[string]bool // class/spec -> families of its priority's top stats
	slots map[string]map[string]bool // class/spec/slot -> families its players wear there
}

func newFamilyReader(t Tables) *familyReader {
	return &familyReader{t: t, specs: map[string]map[string]bool{}, slots: map[string]map[string]bool{}}
}

// specFamilies is the set of families among a spec's top PriorityStats stats. No table carries item
// stats, so the specs that commonly wear an item are its family evidence; the set (not only the
// first stat, which made a hybrid read as a pure caster) keeps every family its players want.
func (r *familyReader) specFamilies(class, spec string) map[string]bool {
	k := class + "|" + spec
	if families, ok := r.specs[k]; ok {
		return families
	}
	families := priorityFamilies(r.t.StatPriorityFor(class, spec))
	r.specs[k] = families
	return families
}

// acceptable is the set of families the spec's own players wear in one slot: the union of the
// families of every item its gear tables list there at or above GearFamilyShare (any population, so
// the reading covers the whole population and the best-geared one). It is cached per slot.
func (r *familyReader) acceptable(class, spec, slot string) map[string]bool {
	k := class + "|" + spec + "|" + strings.ToLower(slot)
	if families, ok := r.slots[k]; ok {
		return families
	}
	families := map[string]bool{}
	for _, g := range r.t.GearRows(class, spec) {
		if g.SetSlot() || g.ID <= 0 || !strings.EqualFold(g.Slot, slot) || g.Pct < GearFamilyShare {
			continue
		}
		items, _ := r.wearers(Item{Entry: g.ID, Name: g.Name, Slot: g.Slot})
		for family := range items {
			families[family] = true
		}
	}
	r.slots[k] = families
	return families
}

// itemReading names an item's stat families and where the reading came from. Item stats in the
// record win; otherwise the specs that commonly wear the item at or above GearFamilyShare in its
// slot do, and at least GearMismatchSpecs of them must (one spec's piece may be shared). known is
// false when neither source names a family, which the rule reports as INCONCLUSIVE, never a FAIL.
func (r *familyReader) itemReading(item Item) (families map[string]bool, why string, known bool) {
	if len(item.Stats) > 0 {
		families = map[string]bool{}
		for _, stat := range item.Stats {
			if family := StatFamily(stat); family != FamilyAny {
				families[family] = true
			}
		}
		if len(families) == 0 {
			return nil, fmt.Sprintf("its stats (%s) name no stat family", strings.Join(item.Stats, ", ")), false
		}
		return families, fmt.Sprintf("its stats (%s)", strings.Join(item.Stats, ", ")), true
	}
	families, wearers := r.wearers(item)
	if len(wearers) < GearMismatchSpecs {
		return nil, "the record carries no item stats and no common wearers name its stat family", false
	}
	if len(families) == 0 {
		return nil, fmt.Sprintf("commonly worn by %s, none of whom names a stat family", strings.Join(wearers, ", ")), false
	}
	return families, fmt.Sprintf("commonly worn by %s", strings.Join(wearers, ", ")), true
}

// wearers is the union of the families of the specs that wear the item at or above
// GearFamilyShare in its slot, with each such spec's best share stated for the evidence line.
func (r *familyReader) wearers(item Item) (map[string]bool, []string) {
	families := map[string]bool{}
	info, ok := r.t.Item(item.Entry)
	if !ok {
		return families, nil
	}
	perSpec := map[string]float64{}
	for _, w := range info.Wearers {
		if !strings.EqualFold(w.Slot, item.Slot) || w.Pct < GearFamilyShare {
			continue
		}
		k := w.Class + "/" + w.Spec
		if w.Pct > perSpec[k] {
			perSpec[k] = w.Pct
		}
	}
	wearers := make([]string, 0, len(perSpec))
	for k, pct := range perSpec {
		wearers = append(wearers, fmt.Sprintf("%s %.1f%%", k, pct))
		parts := strings.SplitN(k, "/", 2)
		if len(parts) != 2 {
			continue
		}
		for family := range r.specFamilies(parts[0], parts[1]) {
			families[family] = true
		}
	}
	sort.Strings(wearers)
	return families, wearers
}
