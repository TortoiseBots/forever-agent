package realism

import (
	"strings"
	"testing"
)

func TestStatsCheckPassesTheSpecsOwnGear(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{
		{Slot: "head", ItemID: 22267, ItemLevel: 62, RequiredLevel: 58},
		{Slot: "chest", ItemID: 22409, ItemLevel: 62, RequiredLevel: 58},
	}
	c := checkOfID(t, Judge(b, Default()), RuleStats)
	if c.Verdict != VerdictPass {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	fits := partOf(t, c, "fits")
	if fits.Verdict != VerdictPass || !strings.Contains(fits.Measure, ruleGearFitsSpec) {
		t.Fatalf("the fits part must be the player-data gear rule's own verdict: %+v", fits)
	}
	for _, p := range c.Parts {
		if p.Name == "priority" {
			t.Fatalf("the stat priority is report-only, never a second verdict: %+v", c.Parts)
		}
	}
}

func TestStatsCheckFailsStrengthOnACaster(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{{Slot: "chest", ItemID: 6502, Name: "Haunted Memento", Stats: []string{"Strength"}}}
	c := checkOfID(t, Judge(b, Default()), RuleStats)
	if c.Verdict != VerdictFail {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	if fits := partOf(t, c, "fits"); fits.Verdict != VerdictFail {
		t.Fatalf("the gear rule must fail a strength item on a caster: %+v", fits)
	}
	if !strings.Contains(strings.Join(c.Evidence, "\n"), "stat priority (report only)") {
		t.Fatalf("the priority reading must still be reported: %+v", c.Evidence)
	}
}

// TestStatsPriorityIsReportOnly pins the single judgement: an item the spec's own table names passes
// the gear rule, and its own (wrong) stats are reported without a second verdict that could disagree.
func TestStatsPriorityIsReportOnly(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{{Slot: "chest", ItemID: 22409, Stats: []string{"Strength"}}}
	c := checkOfID(t, Judge(b, Default()), RuleStats)
	if fits := partOf(t, c, "fits"); fits.Verdict != VerdictPass {
		t.Fatalf("an item in the spec's own table must fit: %+v", fits)
	}
	if c.Verdict != VerdictPass {
		t.Fatalf("one judgement, so the check passes with the gear rule: %s (%s)", c.Verdict, c.Reason)
	}
	evidence := strings.Join(c.Evidence, "\n")
	if !strings.Contains(evidence, "stat priority (report only)") || !strings.Contains(evidence, "strength") {
		t.Fatalf("the report must name the strength item without judging it: %s", evidence)
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
		{"naked bot", func(b *Bot) { b.Items = nil }, "wears nothing"},
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
