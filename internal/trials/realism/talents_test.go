package realism

import (
	"strings"
	"testing"
)

// balance60 are the talents at least half of aotc.gg's level 60 balance druids took, at their usual
// ranks: tab 0 sums 38 points, tab 2 sums 13 — the spec's most common split, 38/0/13.
func balance60() []Talent {
	return []Talent{
		{SpellID: 16814, Rank: 5, Tab: new(0)}, // Improved Wrath
		{SpellID: 1223081, Rank: 2, Tab: new(0)},
		{SpellID: 16845, Rank: 3, Tab: new(0)},
		{SpellID: 16821, Rank: 2, Tab: new(0)},
		{SpellID: 1223082, Rank: 2, Tab: new(0)},
		{SpellID: 16819, Rank: 2, Tab: new(0)},
		{SpellID: 1223083, Rank: 1, Tab: new(0)},
		{SpellID: 5570, Rank: 1, Tab: new(0)},
		{SpellID: 16909, Rank: 5, Tab: new(0)},
		{SpellID: 16850, Rank: 5, Tab: new(0)},
		{SpellID: 16880, Rank: 1, Tab: new(0)},
		{SpellID: 408248, Rank: 3, Tab: new(0)},
		{SpellID: 16896, Rank: 5, Tab: new(0)},
		{SpellID: 24858, Rank: 1, Tab: new(0)},
		{SpellID: 17063, Rank: 5, Tab: new(2)},
		{SpellID: 17069, Rank: 5, Tab: new(2)},
		{SpellID: 17106, Rank: 3, Tab: new(2)},
	}
}

// checkOfID returns one check from a judged bot.
func checkOfID(t *testing.T, checks []Check, id string) Check {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no %s check in %+v", id, checks)
	return Check{}
}

// partOf returns one part of a check.
func partOf(t *testing.T, c Check, name string) Part {
	t.Helper()
	for _, p := range c.Parts {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no part %q in %+v", name, c.Parts)
	return Part{}
}

func TestTalentCheckPassesTheCommonBuild(t *testing.T) {
	b := bot("bot1", 60)
	b.Talents = balance60()
	c := checkOfID(t, Judge(b, Default()), RuleTalents)
	if c.Verdict != VerdictPass {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	share := partOf(t, c, "point_share")
	if share.Value != 1 || share.Verdict != VerdictPass {
		t.Fatalf("point share: %+v", share)
	}
	split := partOf(t, c, "split")
	if split.Verdict != VerdictPass || split.Value != 0 {
		t.Fatalf("split: %+v", split)
	}
	if !strings.Contains(split.Measure, "38/0/13") {
		t.Fatalf("the split measure must name the bot's split: %q", split.Measure)
	}
}

func TestTalentCheckFailsAForeignBuild(t *testing.T) {
	b := bot("bot1", 60)
	// 41 of 51 points in the common picks (80%), but 10 points in the feral tree: the split is 10
	// points from the nearest common one.
	b.Talents = append(balance60()[:14], []Talent{
		{SpellID: 17063, Rank: 3, Tab: new(2)},
		{SpellID: 16934, Rank: 5, Tab: new(1)},
		{SpellID: 17056, Rank: 5, Tab: new(1)},
	}...)
	c := checkOfID(t, Judge(b, Default()), RuleTalents)
	if c.Verdict != VerdictFail {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	if share := partOf(t, c, "point_share"); share.Verdict != VerdictPass {
		t.Fatalf("the point share must still pass: %+v", share)
	}
	split := partOf(t, c, "split")
	if split.Verdict != VerdictFail {
		t.Fatalf("the split must fail: %+v", split)
	}
	if split.Value <= SplitPointsMax {
		t.Fatalf("split distance %v must be above %v", split.Value, SplitPointsMax)
	}
}

// TestTalentPointShareThreshold walks the boundary: 20 of 25 points pass exactly at the 80% floor,
// one point fewer fails. Mutation: changing TalentPointShareMin moves one of the two.
func TestTalentPointShareThreshold(t *testing.T) {
	common := []Talent{
		{SpellID: 16814, Rank: 5},
		{SpellID: 16845, Rank: 5},
		{SpellID: 16821, Rank: 5},
		{SpellID: 16819, Rank: 5},
	}
	pass := bot("bot1", 30)
	pass.Talents = append(append([]Talent(nil), common...), Talent{SpellID: 16918, Rank: 5})
	c := checkOfID(t, Judge(pass, Default()), RuleTalents)
	if share := partOf(t, c, "point_share"); share.Verdict != VerdictPass || share.Value != 0.8 {
		t.Fatalf("the 80%% boundary must pass: %+v", share)
	}
	fail := bot("bot1", 30)
	fail.Talents = append(append([]Talent(nil), common[1:]...), Talent{SpellID: 16918, Rank: 5},
		Talent{SpellID: 17002, Rank: 5})
	c = checkOfID(t, Judge(fail, Default()), RuleTalents)
	if share := partOf(t, c, "point_share"); share.Verdict != VerdictFail || share.Value != 0.6 {
		t.Fatalf("0.60 must fail: %+v", share)
	}
}

func TestTalentCheckInconclusiveCases(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Bot)
		want string
	}{
		{"no talents read", func(b *Bot) { b.TalentsKnown, b.TalentsComplete, b.Talents = false, false, nil }, "no talents"},
		{"partial list", func(b *Bot) { b.TalentsComplete = false }, "partial"},
		{"no spec", func(b *Bot) { b.Spec = "" }, "no spec"},
		{"unknown spec", func(b *Bot) { b.Spec = "recovery" }, "no player data"},
		{"level below ten", func(b *Bot) { b.Level = 5 }, "no talent points"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bot("bot1", 60)
			b.Talents = balance60()
			tc.edit(&b)
			c := checkOfID(t, Judge(b, Default()), RuleTalents)
			if c.Verdict != VerdictInconclusive {
				t.Fatalf("verdict %s, want INCONCLUSIVE (%s)", c.Verdict, c.Reason)
			}
			if tc.want != "" && !strings.Contains(strings.ToLower(c.Reason), strings.ToLower(tc.want)) {
				t.Fatalf("reason %q does not name %q", c.Reason, tc.want)
			}
		})
	}
}

// TestTalentCheckPartialBuildIsInconclusive pins the fail-closed reading of a "complete" build: a
// level 60 list with 15 of the 51 points a level 60 character has is a partial read whatever the
// snapshot's talents_complete says, so neither the share nor the split is judged.
func TestTalentCheckPartialBuildIsInconclusive(t *testing.T) {
	b := bot("bot1", 60)
	b.Talents = []Talent{{SpellID: 16814, Rank: 5, Tab: new(0)}, {SpellID: 16845, Rank: 5, Tab: new(0)},
		{SpellID: 16821, Rank: 5, Tab: new(0)}}
	c := checkOfID(t, Judge(b, Default()), RuleTalents)
	if c.Verdict != VerdictInconclusive || !strings.Contains(c.Reason, "partial") {
		t.Fatalf("a 15-point build at 60 must be INCONCLUSIVE: %s (%s)", c.Verdict, c.Reason)
	}
}

// TestTalentCheckUnplacedPointsLeaveOnlyTheSplitUnjudged pins the aggregation: the point share is
// judged and passes, and the split names the points it cannot place instead of guessing a tree.
func TestTalentCheckUnplacedPointsLeaveOnlyTheSplitUnjudged(t *testing.T) {
	b := bot("bot1", 60)
	b.Talents = append(balance60(), Talent{SpellID: 999999, Rank: 1})
	c := checkOfID(t, Judge(b, Default()), RuleTalents)
	if c.Verdict != VerdictPass {
		t.Fatalf("the point share is judged, so the check passes with the split named: %s (%s)", c.Verdict, c.Reason)
	}
	if share := partOf(t, c, "point_share"); share.Verdict != VerdictPass {
		t.Fatalf("point share: %+v", share)
	}
	if split := partOf(t, c, "split"); split.Verdict != VerdictInconclusive || !strings.Contains(split.Reason, "tables do not name") {
		t.Fatalf("split: %+v", split)
	}
}

// TestTalentCheckLevel60TwiceIsNotASplit pins that the split part exists only at 60: a level 49 build
// with the same talents is judged on its point share only (and 50-59's own common picks).
func TestTalentCheckNoSplitBelowSixty(t *testing.T) {
	b := bot("bot1", 49)
	b.Talents = append(append([]Talent(nil), balance60()[:14]...), Talent{SpellID: 17063, Rank: 2, Tab: new(2)})
	c := checkOfID(t, Judge(b, Default()), RuleTalents)
	for _, p := range c.Parts {
		if p.Name == "split" {
			t.Fatalf("a level 49 bot must have no split part: %+v", c.Parts)
		}
	}
}
