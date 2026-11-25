package realism

// The stat check (REALISM-STATS): does the worn gear follow the spec's stat priority (stat-priority-
// 60.csv)? A healer in strength plate, a rogue in intellect cloth.
//
// It reuses the player-data gear rule rather than restating it: the part `fits` is the same
// AOTC-GEAR-FITS-SPEC judgment (playdata.JudgeGear over the snapshot's items, its stats included), so
// the two readings of one outfit can never disagree. The part `priority` is the sharper reading the
// gear rule leaves to its tie-break: every worn item whose own stats name a family must name one of
// the families among the spec's top priority stats. Stamina, crit and hit name no family, so an item
// that carries only those is not judged.
//
// Both read the level 60 tables (stat-priority-60.csv): a bot of another level is INCONCLUSIVE, and a
// snapshot whose items carry no stats leaves `priority` INCONCLUSIVE (the item read-out names no
// stats).

import (
	"fmt"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/playdata"
)

// StatPriorityMatchMin is the share of the stat-reading items that must carry one of the spec's own
// priority families: all of them. One strength piece on a healer is a real fault, not noise, and the
// items that carry no family are not counted.
const StatPriorityMatchMin = 1.0

// statsCheck judges one bot's gear against its spec's stat priority.
func statsCheck(b Bot, t Tables) Check {
	class, spec := botSpec(b)
	if !specKnown(t, class, spec) {
		return checkOf(RuleStats, []Part{part("fits", VerdictInconclusive, 0, "", specMissingReason(t, class, spec))}, nil)
	}
	if b.Level != 60 {
		return checkOf(RuleStats, []Part{part("fits", VerdictInconclusive, 0, "",
			fmt.Sprintf("stat-priority-60.csv covers level 60 only (bot level %d)", b.Level))}, nil)
	}
	if !b.ItemsKnown || len(b.Items) == 0 {
		return checkOf(RuleStats, []Part{part("fits", VerdictInconclusive, 0, "",
			"the snapshot carries no worn items (items_known false): no equipment read for this bot")}, nil)
	}
	items := playdataItems(b.Items)
	row := playdata.JudgeGear(class, spec, items, playdata.Inputs{Tables: t})

	parts := []Part{{
		Name: "fits", Verdict: row.Verdict, Value: ratio(row.K, row.N), Reason: row.Reason,
		Measure: fmt.Sprintf("%d of %d item(s) fit %s/%s (%s)", row.K, row.N, class, spec, ruleGearFitsSpec),
	}}
	priority := playdata.PriorityFamilies(t, class, spec)
	parts = append(parts, priorityPart(b, priority))
	return checkOf(RuleStats, parts, row.Evidence)
}

// priorityPart judges every item's own stat families against the spec's priority families.
func priorityPart(b Bot, priority map[string]bool) Part {
	if len(priority) == 0 {
		return part("priority", VerdictInconclusive, 0, "",
			"the spec's priority rows name no stat family (stamina, crit and hit name none)")
	}
	var judged, matched int
	var lines []string
	for _, item := range b.Items {
		families := map[string]bool{}
		for _, stat := range item.Stats {
			if family := playdata.StatFamily(stat); family != playdata.FamilyAny {
				families[family] = true
			}
		}
		if len(families) == 0 {
			continue
		}
		judged++
		if playdata.FamiliesIntersect(families, priority) {
			matched++
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: item %d names %s, none of the spec's priority families",
			item.Slot, item.ItemID, familySetText(families)))
	}
	if judged == 0 {
		return part("priority", VerdictInconclusive, 0, "",
			"no worn item carries stats (the item read-out names none), so no stat family can be read")
	}
	share := float64(matched) / float64(judged)
	return Part{
		Name: "priority", Verdict: verdictAtLeast(share, StatPriorityMatchMin), Value: share,
		Measure: fmt.Sprintf("%d of %d item(s) carry one of the spec's priority families (%s)",
			matched, judged, familySetText(priority)),
		Lines: lines,
	}
}

// playdataItems turns a snapshot's items into the player-data rule's item shape: the table's slot
// name when the tables carry the slot, else the snapshot's own (which matches no table row, so the
// rule reports it unproven rather than guessing another slot).
func playdataItems(items []Item) []playdata.Item {
	out := make([]playdata.Item, 0, len(items))
	for _, item := range items {
		slot := item.Slot
		if table, ok := tableSlot(item.Slot); ok {
			slot = table
		}
		out = append(out, playdata.Item{Entry: item.ItemID, Name: item.Name, Slot: slot, Stats: item.Stats})
	}
	return out
}

// familySetText lists a family set for an evidence line.
func familySetText(families map[string]bool) string {
	var out []string
	for _, family := range []string{playdata.FamilyCaster, playdata.FamilyAgility, playdata.FamilyStrength} {
		if families[family] {
			out = append(out, family)
		}
	}
	return strings.Join(out, ", ")
}

// ratio is k/n as a share, 0 for no items.
func ratio(k, n int64) float64 {
	if n == 0 {
		return 0
	}
	return float64(k) / float64(n)
}
