package realism

import (
	"strings"
	"testing"
)

// enchantedItem is a worn item with its enchant read (0 means read and unenchanted).
func enchantedItem(slot string, id int64, enchantID int64) Item {
	return Item{Slot: slot, ItemID: id, ItemLevel: 60, EnchantID: new(enchantID)}
}

func TestEnchantCheckPassesTheCommonSlots(t *testing.T) {
	b := bot("bot1", 60)
	// Head: 96.1% of the spec's characters leave it unenchanted. Main hand: 39.8% leave it bare and
	// 51.4% use one enchant, so the slot is one players enchant.
	b.Items = []Item{enchantedItem("head", 22267, 0), enchantedItem("main_hand", 13964, 273629)}
	c := checkOfID(t, Judge(b, Default()), RuleEnchants)
	if c.Verdict != VerdictPass {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	if present := partOf(t, c, "present"); present.Verdict != VerdictPass {
		t.Fatalf("the main hand must pass the present part: %+v", present)
	}
	if empty := partOf(t, c, "empty"); empty.Verdict != VerdictPass {
		t.Fatalf("the head must pass the empty part: %+v", empty)
	}
	if common := partOf(t, c, "common"); common.Verdict != VerdictPass {
		t.Fatalf("the common enchant id must match the table's row: %+v", common)
	}
}

func TestEnchantCheckFailsAMissingAndAForcedEnchant(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{enchantedItem("head", 22267, 7), enchantedItem("main_hand", 13964, 0)}
	c := checkOfID(t, Judge(b, Default()), RuleEnchants)
	if c.Verdict != VerdictFail {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	if present := partOf(t, c, "present"); present.Verdict != VerdictFail {
		t.Fatalf("the bare main hand must fail the present part: %+v", present)
	}
	if empty := partOf(t, c, "empty"); empty.Verdict != VerdictFail {
		t.Fatalf("the enchanted head must fail the empty part: %+v", empty)
	}
	if common := partOf(t, c, "common"); common.Verdict != VerdictInconclusive {
		t.Fatalf("a slot with no enchant leaves the common part unjudged: %+v", common)
	}
}

// TestEnchantCommonIsInconclusiveWhenTheIdCannotBeMatched pins the known limit: the tables name the
// enchant's item, so an id that is not the table's row is unjudged (never a FAIL).
func TestEnchantCommonIsInconclusiveWhenTheIdCannotBeMatched(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{enchantedItem("head", 22267, 0), enchantedItem("main_hand", 13964, 5)}
	c := checkOfID(t, Judge(b, Default()), RuleEnchants)
	if c.Verdict != VerdictPass {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	common := partOf(t, c, "common")
	if common.Verdict != VerdictInconclusive || !strings.Contains(common.Reason, "mapping read-out") {
		t.Fatalf("the common part must name the missing mapping: %+v", common)
	}
}

func TestEnchantCheckInconclusiveCases(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Bot)
		want string
	}{
		{"no enchant ids", func(b *Bot) {
			b.Items = []Item{{Slot: "head", ItemID: 22267, ItemLevel: 60}, {Slot: "main_hand", ItemID: 13964, ItemLevel: 60}}
		}, "no enchant id"},
		{"level 50", func(b *Bot) { b.Level = 50 }, "level 60 only"},
		{"no items", func(b *Bot) { b.ItemsKnown, b.Items = false, nil }, "no worn items"},
		{"no spec", func(b *Bot) { b.Spec = "" }, "no spec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bot("bot1", 60)
			b.Items = []Item{enchantedItem("head", 22267, 0)}
			tc.edit(&b)
			c := checkOfID(t, Judge(b, Default()), RuleEnchants)
			if c.Verdict != VerdictInconclusive {
				t.Fatalf("verdict %s, want INCONCLUSIVE (%s)", c.Verdict, c.Reason)
			}
			if !strings.Contains(strings.ToLower(c.Reason), strings.ToLower(tc.want)) {
				t.Fatalf("reason %q does not name %q", c.Reason, tc.want)
			}
		})
	}
}

// TestEnchantPartsFailClosed pins the coverage: one unread enchant beside a read one leaves the part
// INCONCLUSIVE, and a clear violation on a read slot still fails.
func TestEnchantPartsFailClosed(t *testing.T) {
	mixed := bot("bot1", 60)
	mixed.Items = []Item{enchantedItem("head", 22267, 0), {Slot: "main_hand", ItemID: 13964, ItemLevel: 60}}
	c := checkOfID(t, Judge(mixed, Default()), RuleEnchants)
	if c.Verdict != VerdictPass || !strings.Contains(c.Reason, "present") {
		t.Fatalf("the read head passes the empty part, so the check passes with the present part named: %s (%s)", c.Verdict, c.Reason)
	}
	if present := partOf(t, c, "present"); present.Verdict != VerdictInconclusive {
		t.Fatalf("present part: %+v", present)
	}
	violation := bot("bot1", 60)
	violation.Items = []Item{enchantedItem("main_hand", 13964, 0), {Slot: "head", ItemID: 22267, ItemLevel: 60}}
	c = checkOfID(t, Judge(violation, Default()), RuleEnchants)
	if c.Verdict != VerdictFail {
		t.Fatalf("a read bare slot players enchant must fail even with an unread head: %s (%s)", c.Verdict, c.Reason)
	}
}

// TestEnchantSlotsTheTablesDoNotName pins that a slot with no enchant row is skipped, not judged:
// shoulders have no rows in enchants-60.csv.
func TestEnchantSlotsTheTablesDoNotName(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{{Slot: "shoulders", ItemID: 15067, ItemLevel: 60, EnchantID: new(int64(7))}}
	c := checkOfID(t, Judge(b, Default()), RuleEnchants)
	if c.Verdict != VerdictInconclusive {
		t.Fatalf("a slot with no table row must not be judged: %s (%s)", c.Verdict, c.Reason)
	}
}
