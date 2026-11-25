package realism

// The stat check (REALISM-STATS): does the worn gear follow the spec's stat priority (stat-priority-
// 60.csv)? A healer in strength plate, a rogue in intellect cloth.
//
// It is ONE judgement: the part `fits` is the player-data gear rule itself (playdata.JudgeGear over
// the snapshot's items, its stats included), not a second, stricter rule beside it. It needs every
// worn item's stat reading (`stats_known`): an unread one could be the wrong-family piece, so the part
// is INCONCLUSIVE rather than a PASS over the items that were read. The spec's
// stat-priority reading is reported as evidence (how many items carry one of the spec's priority
// families) and never judged: a second verdict could disagree with the gear rule, and the gear rule
// is the one the bench's other rows read.
//
// It reads the level 60 tables (stat-priority-60.csv): a bot of another level is INCONCLUSIVE.

import (
	"fmt"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/playdata"
)

// statsCheck judges one bot's gear against its spec's stat priority.
func statsCheck(b Bot, t Tables) Check {
	class, spec := botSpec(b)
	if !specKnown(t, class, spec) {
		return checkOf(RuleStats, []Part{{Name: "fits", Verdict: VerdictInconclusive, Reason: specMissingReason(t, class, spec), Decisive: true}}, nil)
	}
	if b.Level != 60 {
		return checkOf(RuleStats, []Part{{Name: "fits", Verdict: VerdictInconclusive, Decisive: true,
			Reason: fmt.Sprintf("stat-priority-60.csv covers level 60 only (bot level %d)", b.Level)}}, nil)
	}
	if !b.ItemsKnown {
		return checkOf(RuleStats, []Part{{Name: "fits", Verdict: VerdictInconclusive, Decisive: true,
			Reason: "the snapshot carries no worn items (items_known false): no equipment read for this bot"}}, nil)
	}
	if len(b.Items) == 0 {
		return checkOf(RuleStats, []Part{{Name: "fits", Verdict: VerdictInconclusive, Decisive: true,
			Reason: "the bot wears nothing (items_known true, no items): there is no outfit to judge"}}, nil)
	}
	// The check is the stat reading: an item whose stats were not read could be the strength plate on
	// a healer, so one unread item leaves the decisive part INCONCLUSIVE rather than a PASS.
	unread := 0
	for _, item := range b.Items {
		if !item.StatsKnown {
			unread++
		}
	}
	if unread > 0 {
		return checkOf(RuleStats, []Part{{Name: "fits", Verdict: VerdictInconclusive, Decisive: true,
			Reason: fmt.Sprintf("%d of %d worn item(s) carry no stat reading (stats_known false): the stat priority cannot be judged",
				unread, len(b.Items))}}, nil)
	}
	items := playdataItems(b.Items)
	row := playdata.JudgeGear(class, spec, items, playdata.Inputs{Tables: t})

	parts := []Part{{
		Name: "fits", Verdict: row.Verdict, Value: ratio(row.K, row.N), Reason: row.Reason,
		Measure:  fmt.Sprintf("%d of %d item(s) fit %s/%s (%s)", row.K, row.N, class, spec, ruleGearFitsSpec),
		Decisive: true,
	}}
	evidence := append([]string(nil), row.Evidence...)
	if line := priorityLine(b, playdata.PriorityFamilies(t, class, spec)); line != "" {
		evidence = append(evidence, line)
	}
	return checkOf(RuleStats, parts, evidence)
}

// priorityLine is the report-only stat-priority reading: the share of the items that carry stats which
// name one of the spec's priority families. It never turns into a verdict (the gear rule is the
// judgement); "" when neither the spec's rows nor the items name a family.
func priorityLine(b Bot, priority map[string]bool) string {
	if len(priority) == 0 {
		return ""
	}
	var judged, matched int
	var bad []string
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
		bad = append(bad, fmt.Sprintf("%s: item %d names %s, none of the spec's priority families",
			item.Slot, item.ItemID, familySetText(families)))
	}
	if judged == 0 {
		return ""
	}
	line := fmt.Sprintf("stat priority (report only): %d of %d stat-reading item(s) carry one of the spec's priority families (%s)",
		matched, judged, familySetText(priority))
	if len(bad) > 0 {
		line += "; " + strings.Join(bad, "; ")
	}
	return line
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
