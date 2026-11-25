package realism

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/data/aotc"
)

// enchantedItem is a worn item with its enchant read (0 means read and unenchanted).
func enchantedItem(slot string, id int64, enchantID int64) Item {
	return Item{Slot: slot, ItemID: id, ItemLevel: 60, RequiredLevel: 58, EnchantID: new(enchantID)}
}

// enchantTables is a fake table for one spec and one slot, with the slot's bare share written exactly
// where a dead-zone test needs it.
func enchantTables(slot string, bare float64) *fakeTables {
	return &fakeTables{
		specs: map[string]bool{"druid|balance": true},
		enchants: []aotc.Enchant{
			{Class: "druid", Spec: "balance", Population: aotc.PopulationAll, Slot: slot, Name: "No enchant", Pct: bare, Chars: int64(100 - bare)},
			{Class: "druid", Spec: "balance", Population: aotc.PopulationAll, Slot: slot, Name: "Spell Power +5", ID: 18000, Pct: 99 - bare, Chars: int64(99 - bare)},
		},
	}
}

func TestEnchantCheckPassesTheCommonSlots(t *testing.T) {
	b := bot("bot1", 60)
	// All-60 balance: head is 98.6% bare (players leave it empty), main hand is 56.2% bare — inside
	// the dead zone, so neither an enchant nor a bare slot there is judged.
	b.Items = []Item{enchantedItem("head", 22267, 0), enchantedItem("main_hand", 13964, 273629)}
	c := checkOfID(t, Judge(b, Default()), RuleEnchants)
	if c.Verdict != VerdictPass {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	if empty := partOf(t, c, "empty"); empty.Verdict != VerdictPass {
		t.Fatalf("the bare head must pass the empty part: %+v", empty)
	}
	if present := partOf(t, c, "present"); !present.Skipped {
		t.Fatalf("no all-60 slot reaches the 70%% present line, so the part does not apply: %+v", present)
	}
}

func TestEnchantCheckFailsAForcedEnchant(t *testing.T) {
	b := bot("bot1", 60)
	// Head: 98.6% of the spec's characters leave it empty, so an enchant on it is a fault.
	b.Items = []Item{enchantedItem("head", 22267, 7)}
	c := checkOfID(t, Judge(b, Default()), RuleEnchants)
	if c.Verdict != VerdictFail {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	if empty := partOf(t, c, "empty"); empty.Verdict != VerdictFail {
		t.Fatalf("the enchanted head must fail the empty part: %+v", empty)
	}
}

// TestEnchantFuryHandsIsNotAFail is the review's example: in all-60 the fury warrior hands slot is
// 65.9% bare (34.1% enchanted), inside the dead zone. An enchanted glove must not fail either line.
func TestEnchantFuryHandsIsNotAFail(t *testing.T) {
	b := Bot{ID: "bot1", Class: "warrior", Spec: "fury", Level: 60, ItemsKnown: true}
	b.Items = []Item{enchantedItem("hands", 226774, 12345), enchantedItem("head", 22267, 0)}
	c := checkOfID(t, Judge(b, Default()), RuleEnchants)
	if c.Verdict != VerdictPass {
		t.Fatalf("an enchanted fury glove is in the dead zone: %s (%s)", c.Verdict, c.Reason)
	}
	for _, p := range c.Parts {
		if p.Verdict == VerdictFail {
			t.Fatalf("no part may fail: %+v", c.Parts)
		}
	}
}

// TestEnchantDeadZoneBoundaries walks both lines: at 70% enchanted the present part applies, just
// below it does not; at 20% enchanted the empty part applies, just above it does not. Mutation:
// moving either constant turns one case.
func TestEnchantDeadZoneBoundaries(t *testing.T) {
	// Enchanted share = 100 - bare.
	cases := []struct {
		name      string
		bare      float64
		enchantID int64
		want      string // verdict of the part the slot falls in, or "" for the dead zone
	}{
		{"present at the line", 30.0, 0, VerdictFail},
		{"dead below the present line", 30.1, 0, ""},
		{"empty at the line", 80.0, 7, VerdictFail},
		{"dead above the empty line", 79.9, 7, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bot("bot1", 60)
			b.Items = []Item{enchantedItem("head", 22267, tc.enchantID)}
			c := checkOfID(t, Judge(b, enchantTables("Head", tc.bare)), RuleEnchants)
			var got string
			for _, p := range c.Parts {
				if p.Name == "present" || p.Name == "empty" {
					if p.Skipped {
						continue
					}
					got = p.Verdict
				}
			}
			if got != tc.want {
				t.Fatalf("bare %.1f: verdict %q, want %q (%s)", tc.bare, got, tc.want, c.Reason)
			}
		})
	}
}

// TestEnchantCommonIsReportOnly pins the known limit: the tables name the enchant's scroll item, so
// the part names the common enchant but never compares its id with the applied enchant.
func TestEnchantCommonIsReportOnly(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{enchantedItem("head", 22267, 5)}
	c := checkOfID(t, Judge(b, enchantTables("Head", 10)), RuleEnchants) // 90% enchanted: present
	common := partOf(t, c, "common")
	if common.Verdict != VerdictInconclusive || !strings.Contains(common.Reason, "unlike") &&
		!strings.Contains(common.Reason, "mapping") {
		t.Fatalf("the common part must be report-only with the mapping reason: %+v", common)
	}
	if len(common.Lines) == 0 || !strings.Contains(common.Lines[0], "Spell Power +5") {
		t.Fatalf("the common part must name the common enchant: %+v", common)
	}
}

// TestEnchantCommonShareBoundary pins where the report starts naming a common enchant.
func TestEnchantCommonShareBoundary(t *testing.T) {
	mk := func(topPct float64) *fakeTables {
		f := enchantTables("Head", 10)
		f.enchants[1].Pct = topPct
		return f
	}
	b := bot("bot1", 60)
	b.Items = []Item{enchantedItem("head", 22267, 5)}
	hasCommon := func(c Check) bool {
		for _, p := range c.Parts {
			if p.Name == "common" {
				return true
			}
		}
		return false
	}
	if at := checkOfID(t, Judge(b, mk(EnchantCommonShare)), RuleEnchants); !hasCommon(at) {
		t.Fatalf("at %.0f%% the common part must be reported: %+v", EnchantCommonShare, at.Parts)
	}
	if below := checkOfID(t, Judge(b, mk(EnchantCommonShare-0.1)), RuleEnchants); hasCommon(below) {
		t.Fatalf("below %.0f%% no common part must be reported: %+v", EnchantCommonShare, below.Parts)
	}
}

func TestEnchantCheckInconclusiveCases(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Bot)
		want string
	}{
		{"no enchant ids", func(b *Bot) {
			b.Items = []Item{{Slot: "head", ItemID: 22267, ItemLevel: 60, RequiredLevel: 58}}
		}, "no enchant id"},
		{"level 50", func(b *Bot) { b.Level = 50 }, "level 60 only"},
		{"no items", func(b *Bot) { b.ItemsKnown, b.Items = false, nil }, "no worn items"},
		{"naked bot", func(b *Bot) { b.Items = nil }, "wears nothing"},
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

// TestEnchantPartsFailClosed pins the coverage: one unread enchant on a slot players leave empty
// leaves the part INCONCLUSIVE, and a read violation on another empty slot still fails.
func TestEnchantPartsFailClosed(t *testing.T) {
	mixed := bot("bot1", 60)
	mixed.Items = []Item{enchantedItem("head", 22267, 0), {Slot: "wrists", ItemID: 226775, ItemLevel: 60, RequiredLevel: 58}}
	c := checkOfID(t, Judge(mixed, Default()), RuleEnchants)
	if c.Verdict != VerdictInconclusive {
		t.Fatalf("an unread empty slot leaves the empty part unjudged: %s (%s)", c.Verdict, c.Reason)
	}
	if empty := partOf(t, c, "empty"); empty.Verdict != VerdictInconclusive {
		t.Fatalf("empty part: %+v", empty)
	}
	violation := bot("bot1", 60)
	violation.Items = []Item{enchantedItem("head", 22267, 7), {Slot: "wrists", ItemID: 226775, ItemLevel: 60, RequiredLevel: 58}}
	c = checkOfID(t, Judge(violation, Default()), RuleEnchants)
	if c.Verdict != VerdictFail {
		t.Fatalf("a read enchanted empty slot must fail even with an unread one: %s (%s)", c.Verdict, c.Reason)
	}
}

// TestEnchantSlotsTheTablesDoNotName pins that a slot with no enchant row is skipped, not judged:
// shoulders have no rows in enchants-60.csv.
func TestEnchantSlotsTheTablesDoNotName(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{{Slot: "shoulders", ItemID: 15067, ItemLevel: 60, RequiredLevel: 58, EnchantID: new(int64(7))}}
	c := checkOfID(t, Judge(b, Default()), RuleEnchants)
	if c.Verdict != VerdictInconclusive {
		t.Fatalf("a slot with no table row must not be judged: %s (%s)", c.Verdict, c.Reason)
	}
}
