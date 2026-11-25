package realism

// The enchant check (REALISM-ENCHANTS), level 60 only: the enchants the spec's slots carry, and
// nothing on the slots its players leave empty (enchants-60.csv, best geared population).
//
// Per slot the tables name, the population splits two ways: the share that leaves the slot
// unenchanted (the "No enchant" row) and each enchant's share. A slot whose "No enchant" share is at
// least EnchantEmptyShare is one players leave empty; a slot below it is one they enchant. That
// gives two parts:
//
//	present  a slot players enchant carries some enchant. Needs the snapshot's enchant ids
//	         (enchant_id set, 0 meaning unenchanted): without them the part is INCONCLUSIVE.
//	empty    a slot players leave empty carries no enchant. Same data need.
//	common   where one enchant holds at least EnchantCommonShare of the slot, that enchant is the one
//	         carried. The tables name the enchant's ITEM (its scroll, e.g. item=18330), not the
//	         enchant the server stores on a worn item, so a snapshot's enchant id can be compared
//	         only when the taker reads the scroll id: today it is INCONCLUSIVE with that reason, and
//	         a matching read-out would turn it into a verdict.
//
// The checks are per slot and the parts sum them: one slot that must carry an enchant and does not
// fails the check.

import (
	"fmt"

	"github.com/pjerra/forever-agent/data/aotc"
)

const (
	// EnchantEmptyShare is the share of a spec's best-geared characters that leaves a slot
	// unenchanted before the slot counts as one players leave empty (percent). Half the population
	// is the line: a slot most of them enchant is one the bot should have enchanted, one most leave
	// bare is one an enchant is a fault on.
	EnchantEmptyShare = 50.0
	// EnchantCommonShare is the share of the spec's best-geared characters that must use one
	// enchant before it is the slot's common enchant (percent): the same half of the population.
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
	if !b.ItemsKnown || len(b.Items) == 0 {
		return checkOf(RuleEnchants, []Part{part("present", VerdictInconclusive, 0, "",
			"the snapshot carries no worn items (items_known false): no equipment read for this bot")}, nil)
	}
	var present, empty, common Part
	present.Name, empty.Name, common.Name = "present", "empty", "common"
	var presentJudged, emptyJudged, presentBad, emptyBad int
	var presentUnread, emptyUnread int
	var commonMatched, commonMissing int
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
		noEnchant, top, hasTop := enchantSplit(rows)
		if noEnchant >= EnchantEmptyShare {
			emptySlots++
			if item.EnchantID == nil {
				emptyUnread++
				continue
			}
			emptyJudged++
			if *item.EnchantID != 0 {
				emptyBad++
				emptyLines = append(emptyLines, fmt.Sprintf("%s: %d of %s's characters leave it unenchanted (%.1f%%), the bot's item %d carries enchant %d",
					item.Slot, int(noEnchant), spec, noEnchant, item.ItemID, *item.EnchantID))
			}
			continue
		}
		presentSlots++
		if item.EnchantID == nil {
			presentUnread++
			continue
		}
		presentJudged++
		if *item.EnchantID == 0 {
			presentBad++
			presentLines = append(presentLines, fmt.Sprintf("%s: only %.1f%% of %s's characters leave it unenchanted, the bot's item %d carries none",
				item.Slot, noEnchant, spec, item.ItemID))
		}
		if hasTop && top.Pct >= EnchantCommonShare {
			commonSlots++
			switch {
			case *item.EnchantID == 0:
				// The slot's missing enchant is the `present` part's verdict; this part only says
				// which enchant the slot's players use.
			case *item.EnchantID == top.ID:
				commonMatched++
				commonLines = append(commonLines, fmt.Sprintf("%s: item %d carries the common enchant %s (%.1f%%)",
					item.Slot, item.ItemID, top.Name, top.Pct))
			default:
				commonMissing++
			}
		}
	}
	var parts []Part
	switch {
	case presentSlots == 0:
		present = part("present", VerdictInconclusive, 0, "",
			"no worn slot's players mostly enchant it, so no slot has an enchant to expect")
	case presentJudged == 0:
		present = part("present", VerdictInconclusive, 0, "",
			"the snapshot's items carry no enchant id (the equipment read-out names no enchant)")
	case presentBad > 0:
		present = Part{Name: "present", Verdict: VerdictFail, Value: float64(presentBad),
			Measure: fmt.Sprintf("%d of %d enchanted slot(s) carry no enchant", presentBad, presentJudged),
			Lines:   presentLines}
	case presentUnread > 0:
		// One unread slot could be the bare one: the part judges what it saw and says the rest is
		// missing, never a PASS over the slots it could not read.
		present = part("present", VerdictInconclusive, 0, "",
			fmt.Sprintf("%d of %d slot(s) players enchant carry no enchant id", presentUnread, presentSlots))
	default:
		present = Part{Name: "present", Verdict: VerdictPass, Value: 0,
			Measure: fmt.Sprintf("all %d slot(s) players enchant carry one", presentJudged),
			Lines:   presentLines}
	}
	parts = append(parts, present)

	switch {
	case emptySlots == 0:
		empty = part("empty", VerdictInconclusive, 0, "",
			"no worn slot is one the spec's players leave unenchanted")
	case emptyJudged == 0:
		empty = part("empty", VerdictInconclusive, 0, "",
			"the snapshot's items carry no enchant id (the equipment read-out names no enchant)")
	case emptyBad > 0:
		empty = Part{Name: "empty", Verdict: VerdictFail, Value: float64(emptyBad),
			Measure: fmt.Sprintf("%d of %d slot(s) players leave empty are enchanted", emptyBad, emptyJudged),
			Lines:   emptyLines}
	case emptyUnread > 0:
		empty = part("empty", VerdictInconclusive, 0, "",
			fmt.Sprintf("%d of %d slot(s) players leave empty carry no enchant id", emptyUnread, emptySlots))
	default:
		empty = Part{Name: "empty", Verdict: VerdictPass, Value: 0,
			Measure: fmt.Sprintf("none of %d slot(s) players leave empty is enchanted", emptyJudged),
			Lines:   emptyLines}
	}
	parts = append(parts, empty)

	switch {
	case commonSlots == 0:
		// No slot has one enchant over half of the spec's characters: the brief's "common enchant"
		// has nothing to name at this level, and the two parts above carry the reading.
	case commonMissing > 0:
		common = part("common", VerdictInconclusive, 0, "",
			fmt.Sprintf("%d slot(s) carry an enchant other than the table's common one, and the table names the enchant's item, not the applied enchant: a mapping read-out is needed to compare them", commonMissing))
	case commonMatched > 0:
		common = Part{Name: "common", Verdict: VerdictPass, Value: 0,
			Measure: fmt.Sprintf("%d of %d slot(s) with a common enchant carry it", commonMatched, commonSlots),
			Lines:   commonLines}
	default:
		common = part("common", VerdictInconclusive, 0, "",
			"the slot(s) with a common enchant carry no enchant: the present part judges that")
	}
	if commonSlots > 0 {
		parts = append(parts, common)
	}
	return checkOf(RuleEnchants, parts, nil)
}

// enchantSplit reads one slot's enchant rows: the "No enchant" share (0 when no row says it), and the
// most used named enchant with its share.
func enchantSplit(rows []aotc.Enchant) (noEnchant float64, top aotc.Enchant, hasTop bool) {
	for _, row := range rows {
		if row.Name == "No enchant" {
			noEnchant = row.Pct
			continue
		}
		if !hasTop || row.Pct > top.Pct {
			top, hasTop = row, true
		}
	}
	return noEnchant, top, hasTop
}
