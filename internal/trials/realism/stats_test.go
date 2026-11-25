package realism

import (
	"strings"
	"testing"
)

func TestStatsCheckPassesTheSpecsOwnGear(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{
		{Slot: "head", ItemID: 22267, ItemLevel: 62},
		{Slot: "chest", ItemID: 22409, ItemLevel: 62},
	}
	c := checkOfID(t, Judge(b, Default()), RuleStats)
	if c.Verdict != VerdictPass {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	fits := partOf(t, c, "fits")
	if fits.Verdict != VerdictPass || !strings.Contains(fits.Measure, ruleGearFitsSpec) {
		t.Fatalf("the fits part must be the player-data gear rule's own verdict: %+v", fits)
	}
	if priority := partOf(t, c, "priority"); priority.Verdict != VerdictInconclusive {
		t.Fatalf("an item with no stats leaves the priority part unjudged: %+v", priority)
	}
}

func TestStatsCheckFailsStrengthOnACaster(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{{Slot: "chest", ItemID: 6502, Name: "Haunted Memento", Stats: []string{"Strength"}}}
	c := checkOfID(t, Judge(b, Default()), RuleStats)
	if c.Verdict != VerdictFail {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	priority := partOf(t, c, "priority")
	if priority.Verdict != VerdictFail || priority.Value != 0 {
		t.Fatalf("the priority part must fail: %+v", priority)
	}
	if len(priority.Lines) == 0 {
		t.Fatalf("a failing priority part names the item: %+v", priority)
	}
}

// TestStatsPriorityIsItsOwnReading pins that the two parts are independent: an item the spec's own
// table names passes the gear rule, and its own (wrong) stats still fail the priority part.
func TestStatsPriorityIsItsOwnReading(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{{Slot: "chest", ItemID: 22409, Stats: []string{"Strength"}}}
	c := checkOfID(t, Judge(b, Default()), RuleStats)
	if fits := partOf(t, c, "fits"); fits.Verdict != VerdictPass {
		t.Fatalf("an item in the spec's own table must fit: %+v", fits)
	}
	if priority := partOf(t, c, "priority"); priority.Verdict != VerdictFail {
		t.Fatalf("the priority part must fail on its own: %+v", priority)
	}
}

func TestStatsCheckInconclusiveCases(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Bot)
		want string
	}{
		{"level 50", func(b *Bot) { b.Level = 50 }, "level 60 only"},
		{"no items", func(b *Bot) { b.ItemsKnown, b.Items = false, nil }, "no worn items"},
		{"no spec", func(b *Bot) { b.Spec = "" }, "no spec"},
		{"unproven item", func(b *Bot) { b.Items = []Item{{Slot: "chest", ItemID: 6502}} }, "could not be judged"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bot("bot1", 60)
			b.Items = []Item{{Slot: "chest", ItemID: 22409}}
			tc.edit(&b)
			c := checkOfID(t, Judge(b, Default()), RuleStats)
			if c.Verdict != VerdictInconclusive {
				t.Fatalf("verdict %s, want INCONCLUSIVE (%s)", c.Verdict, c.Reason)
			}
			if !strings.Contains(strings.ToLower(c.Reason), strings.ToLower(tc.want)) {
				t.Fatalf("reason %q does not name %q", c.Reason, tc.want)
			}
		})
	}
}
