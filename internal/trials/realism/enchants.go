package realism

// The enchant check (REALISM-ENCHANTS), level 60 only: the enchants the spec's slots carry, and
// nothing on the slots its players leave empty (enchants-60.csv, the all-level-60 population).
//
// Per slot the tables name, the population splits two ways: the share that leaves the slot
// unenchanted (the "No enchant" row) and each enchant's share. A slot is one players enchant when
// at least EnchantPresentShare percent of the spec's all-60 characters carry an enchant in it; one
// they leave empty when at most EnchantEmptyShare percent do. Between the two lines the slot is in
// a dead zone and is not judged: a best-geared minority's habit is no rule for the whole pool, and
// a 50% line splits players down the middle. That gives two parts:
//
//	present  a slot players enchant carries some enchant. Needs the snapshot's enchant ids
//	         (enchant_id set, 0 meaning unenchanted): without them the part is INCONCLUSIVE.
//	empty    a slot players leave empty carries no enchant. Same data need.
//	common   report-only: where one enchant holds at least EnchantCommonShare of the slot, the part
//	         names it. The tables name the enchant's ITEM (its scroll, e.g. item=18330), not the
//	         enchant the server stores on a worn item, so the two ids are unlike and are never
//	         compared: the part is INCONCLUSIVE until a mapping read-out exists.
//
// The checks are per slot and the parts sum them: one slot that must carry an enchant and does not
// fails the check.

import (
	"fmt"

	"github.com/pjerra/forever-agent/data/aotc"
)

const (
	// EnchantPresentShare is the share of a spec's all-60 characters that must carry an enchant in
	// a slot before the slot counts as one players enchant (percent). Starting value to tune, not a
	// fact about players: at 70 no slot in today's file reaches it (the most enchanted slot,
	// marksmanship's ranged, is 61.6%), so the present part reports that it does not apply.
	EnchantPresentShare = 70.0
	// EnchantEmptyShare is the share of a spec's all-60 characters that may carry an enchant in a
	// slot before the slot counts as one players leave empty (percent): at 20 or less enchanting it,
	// players leave it empty and an enchant on it is a fault. Between this line and
	// EnchantPresentShare the slot is not judged. Starting value to tune, not a fact about players.
	EnchantEmptyShare = 20.0
	// EnchantCommonShare is the share of a spec's all-60 characters that must use one enchant
	// before the report names it the slot's common enchant (percent). Report-only: the tables name
	// the enchant's scroll item, not the applied enchant. Starting value to tune.
	EnchantCommonShare = 50.0
)

// enchantsCheck judges one bot's enchants.
func enchantsCheck(b Bot, t Tables) Check {
	class, spec := botSpec(b)
	if !specKnown(t, class, spec) {
		return checkOf(RuleEnchants, []Part{part("present", VerdictInconclusive, 0, "", specMissingReason(t, class, spec))}, nil)
	}
	if b.Level != 60 {
		return checkOf(RuleEnchants, []Part{part("present", VerdictInconclusive, 0, "",
			fmt.Sprintf("enchants-60.csv covers level 60 only (bot level %d)", b.Level))}, nil)
	}
	if !b.ItemsKnown {
		return checkOf(RuleEnchants, []Part{part("present", VerdictInconclusive, 0, "",
			"the snapshot carries no worn items (items_known false): no equipment read for this bot")}, nil)
	}
	if len(b.Items) == 0 {
		return checkOf(RuleEnchants, []Part{part("present", VerdictInconclusive, 0, "",
			"the bot wears nothing (items_known true, no items): there is no slot to judge")}, nil)
	}
	var present, empty, common Part
	present.Name, empty.Name, common.Name = "present", "empty", "common"
	var presentJudged, emptyJudged, presentBad, emptyBad int
	var presentUnread, emptyUnread int
	var presentLines, emptyLines, commonLines []string
	var presentSlots, emptySlots, commonSlots int
	for _, item := range b.Items {
		slot, ok := tableSlot(item.Slot)
		if !ok {
			continue
		}
		rows := t.EnchantsFor(class, spec, slot)
		if len(rows) == 0 {
			continue
		}
		noEnchant, hasBare, top, hasTop := enchantSplit(rows)
		if !hasBare {
			// No "No enchant" row: the bare share is not in the file, so the slot's side of the
			// dead zone is unknown and nothing is judged for it.
			continue
		}
		enchanted := 100 - noEnchant
		switch {
		case enchanted <= EnchantEmptyShare:
			emptySlots++
			if item.EnchantID == nil {
				emptyUnread++
				continue
			}
			emptyJudged++
			if *item.EnchantID != 0 {
				emptyBad++
				emptyLines = append(emptyLines, fmt.Sprintf("%s: only %.1f%% of %s's characters enchant it, the bot's item %d carries enchant %d",
					item.Slot, enchanted, spec, item.ItemID, *item.EnchantID))
			}
		case enchanted < EnchantPresentShare:
			// Dead zone: between the two named lines the population is split, so neither an
			// enchant nor a bare slot is a realism fault.
		default:
			presentSlots++
			if item.EnchantID == nil {
				presentUnread++
				continue
			}
			presentJudged++
			if *item.EnchantID == 0 {
				presentBad++
				presentLines = append(presentLines, fmt.Sprintf("%s: %.1f%% of %s's characters enchant it, the bot's item %d carries none",
					item.Slot, enchanted, spec, item.ItemID))
			}
			if hasTop && top.Pct >= EnchantCommonShare {
				commonSlots++
				commonLines = append(commonLines, fmt.Sprintf("%s: item %d's slot's common enchant is %s (%.1f%% of the spec's characters)",
					item.Slot, item.ItemID, top.Name, top.Pct))
			}
		}
	}
	var parts []Part
	switch {
	case presentSlots == 0:
		present = Part{Name: "present", Verdict: VerdictInconclusive, Skipped: true,
			Reason: "no worn slot is one the spec's all-60 players enchant at the present line"}
	case presentJudged == 0:
		present = Part{Name: "present", Verdict: VerdictInconclusive, Decisive: true,
			Reason: "the snapshot's items carry no enchant id (the equipment read-out names no enchant)"}
	case presentBad > 0:
		present = Part{Name: "present", Verdict: VerdictFail, Value: float64(presentBad),
			Measure: fmt.Sprintf("%d of %d enchanted slot(s) carry no enchant", presentBad, presentJudged),
			Lines:   presentLines}
	case presentUnread > 0:
		// One unread slot could be the bare one: the part judges what it saw and says the rest is
		// missing, never a PASS over the slots it could not read.
		present = Part{Name: "present", Verdict: VerdictInconclusive, Decisive: true,
			Reason: fmt.Sprintf("%d of %d slot(s) players enchant carry no enchant id", presentUnread, presentSlots)}
	default:
		present = Part{Name: "present", Verdict: VerdictPass, Value: 0, Decisive: true,
			Measure: fmt.Sprintf("all %d slot(s) players enchant carry one", presentJudged),
			Lines:   presentLines}
	}
	parts = append(parts, present)

	switch {
	case emptySlots == 0:
		empty = Part{Name: "empty", Verdict: VerdictInconclusive, Skipped: true,
			Reason: "no worn slot is one the spec's all-60 players leave empty at the empty line"}
	case emptyJudged == 0:
		empty = Part{Name: "empty", Verdict: VerdictInconclusive, Decisive: true,
			Reason: "the snapshot's items carry no enchant id (the equipment read-out names no enchant)"}
	case emptyBad > 0:
		empty = Part{Name: "empty", Verdict: VerdictFail, Value: float64(emptyBad), Decisive: true,
			Measure: fmt.Sprintf("%d of %d slot(s) players leave empty are enchanted", emptyBad, emptyJudged),
			Lines:   emptyLines}
	case emptyUnread > 0:
		empty = Part{Name: "empty", Verdict: VerdictInconclusive, Decisive: true,
			Reason: fmt.Sprintf("%d of %d slot(s) players leave empty carry no enchant id", emptyUnread, emptySlots)}
	default:
		empty = Part{Name: "empty", Verdict: VerdictPass, Value: 0, Decisive: true,
			Measure: fmt.Sprintf("none of %d slot(s) players leave empty is enchanted", emptyJudged),
			Lines:   emptyLines}
	}
	parts = append(parts, empty)

	if commonSlots > 0 {
		// Report-only: the table's id is the enchant's scroll item, not the enchant the server
		// stores, so comparing a snapshot's enchant id with it would be comparing unlike ids.
		common = Part{Name: "common", Verdict: VerdictInconclusive,
			Reason: fmt.Sprintf("%d slot(s) have a common enchant; the tables name its scroll item, not the applied enchant: a mapping read-out is needed to compare them", commonSlots),
			Lines:  commonLines}
		parts = append(parts, common)
	}
	return checkOf(RuleEnchants, parts, nil)
}

// enchantSplit reads one slot's enchant rows: the "No enchant" share and whether the row is there,
// and the most used named enchant with its share.
func enchantSplit(rows []aotc.Enchant) (noEnchant float64, hasBare bool, top aotc.Enchant, hasTop bool) {
	for _, row := range rows {
		if row.Name == "No enchant" {
			noEnchant, hasBare = row.Pct, true
			continue
		}
		if !hasTop || row.Pct > top.Pct {
			top, hasTop = row, true
		}
	}
	return noEnchant, hasBare, top, hasTop
}
