package realism

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/data/aotc"
	"github.com/pjerra/forever-agent/internal/trials/playdata"
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

// allSpecs lists the class/spec pairs the player data names.
func allSpecs(t *testing.T, tables *aotc.Data) [][2]string {
	t.Helper()
	seen := map[string]bool{}
	var out [][2]string
	for _, s := range tables.SpecList() {
		key := s.Class + "|" + s.Spec
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, [2]string{s.Class, s.Spec})
	}
	if len(out) < 27 {
		t.Fatalf("the player data names %d specs, want 27", len(out))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// playerLikeBuild builds a whole talent build from the player data itself: the level group's common
// picks at their maximum ranks first, then the group's other picks, and at 60 distributed over the
// trees of the spec's most common split. It is the build a player who follows the data would have,
// and it must PASS the talent check at every level the ticket names.
func playerLikeBuild(t *testing.T, tables *aotc.Data, class, spec string, level int) []Talent {
	t.Helper()
	group := playdata.LevelGroup(level)
	if group == "" {
		t.Fatalf("%s/%s level %d has no talent group", class, spec, level)
	}
	points := TalentPoints(level)
	all := tables.CommonTalents(class, spec, "all", 0)
	maxRank := map[int64]int64{}
	commonID := map[int64]bool{}
	for _, row := range tables.CommonTalents(class, spec, group, TalentPickShare) {
		commonID[row.SpellID] = true
	}
	// The common picks first in each tree, then the rest by pick share: a player-like build takes
	// the common picks before it reaches for the others.
	byTab := map[int][]aotc.TalentPick{}
	for _, row := range all {
		maxRank[row.SpellID] = row.MaxRank
		byTab[row.Tab] = append(byTab[row.Tab], row)
	}
	for tab := range byTab {
		rows := byTab[tab]
		sort.SliceStable(rows, func(i, j int) bool {
			if commonID[rows[i].SpellID] != commonID[rows[j].SpellID] {
				return commonID[rows[i].SpellID]
			}
			return rows[i].PickPct > rows[j].PickPct
		})
	}
	// The points per tree first.
	var targets [splitTabs]int
	if level == 60 {
		splits := tables.SplitsFor(class, spec)
		if len(splits) == 0 {
			t.Fatalf("%s/%s has no level 60 split", class, spec)
		}
		fields := strings.Split(splits[0].Split, "/")
		if len(fields) != splitTabs {
			t.Fatalf("%s/%s split %q is not three numbers", class, spec, splits[0].Split)
		}
		for i, field := range fields {
			n, err := strconv.Atoi(field)
			if err != nil {
				t.Fatalf("%s/%s split %q: %v", class, spec, splits[0].Split, err)
			}
			targets[i] = n
		}
	} else {
		// No split below 60: fill the tree whose common picks hold the most points first, then the
		// rest, so the build spends its points where the common picks are.
		common := tables.CommonTalents(class, spec, group, TalentPickShare)
		var capPerTab [splitTabs]int
		for _, row := range common {
			capPerTab[row.Tab] += int(row.MaxRank)
		}
		order := []int{0, 1, 2}
		sort.SliceStable(order, func(i, j int) bool { return capPerTab[order[i]] > capPerTab[order[j]] })
		left := points
		for _, tab := range order {
			take := min(left, capPerTab[tab])
			targets[tab] = take
			left -= take
		}
		if left > 0 {
			targets[order[0]] += left
		}
	}
	var out []Talent
	for tab := range splitTabs {
		need := targets[tab]
		for _, row := range byTab[tab] {
			if need <= 0 {
				break
			}
			n := int(min(int64(need), maxRank[row.SpellID]))
			if n <= 0 {
				continue
			}
			out = append(out, Talent{SpellID: row.SpellID, Rank: n, Tab: new(tab)})
			need -= n
		}
		if need > 0 {
			t.Fatalf("%s/%s level %d: tree %d cannot hold its %d points from the table", class, spec, level, tab, targets[tab])
		}
	}
	total := 0
	for _, talent := range out {
		total += talent.Rank
	}
	if total != points {
		t.Fatalf("%s/%s level %d: the build holds %d points, want %d", class, spec, level, total, points)
	}
	return out
}

// TestTalentCheckPlayerLikeBuildsPass is the ticket's table test: a build made from the data itself
// passes at every level the ticket names, for EVERY spec. Before the denominator was capped at what
// the common picks account for, 16 of 27 specs failed at level 19, 15 at 29 and 5 at 39 with a
// player-perfect build.
func TestTalentCheckPlayerLikeBuildsPass(t *testing.T) {
	tables := Default().(*aotc.Data)
	for _, level := range []int{19, 29, 39, 49, 60} {
		for _, spec := range allSpecs(t, tables) {
			class, name := spec[0], spec[1]
			t.Run(fmt.Sprintf("%s/%s/%d", class, name, level), func(t *testing.T) {
				b := Bot{ID: "bot1", Class: class, Spec: name, Level: level,
					TalentsKnown: true, TalentsComplete: true}
				b.Talents = playerLikeBuild(t, tables, class, name, level)
				if err := snapshotOf(b).ValidateTables(Default()); err != nil {
					t.Fatalf("the build is not a loadable snapshot: %v", err)
				}
				c := checkOfID(t, Judge(b, Default()), RuleTalents)
				if c.Verdict != VerdictPass {
					t.Fatalf("a player-like build must pass: %s (%s)", c.Verdict, c.Reason)
				}
				if share := partOf(t, c, "point_share"); share.Verdict != VerdictPass {
					t.Fatalf("point share: %+v", share)
				}
			})
		}
	}
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

// TestTalentPointShareThreshold walks the 80% floor on a fake table whose common picks can hold the
// whole build: 20 of 25 points pass exactly at the floor, 15 of 25 fail. Mutation: changing
// TalentPointShareMin moves one of the two.
func TestTalentPointShareThreshold(t *testing.T) {
	tables := pointShareTables()
	pass := Bot{ID: "bot1", Class: "druid", Spec: "balance", Level: 34,
		TalentsKnown: true, TalentsComplete: true, Talents: []Talent{
			{SpellID: 1, Rank: 5}, {SpellID: 2, Rank: 5}, {SpellID: 3, Rank: 5}, {SpellID: 4, Rank: 5},
			{SpellID: 10, Rank: 5},
		}}
	c := checkOfID(t, Judge(pass, tables), RuleTalents)
	if share := partOf(t, c, "point_share"); share.Verdict != VerdictPass || share.Value != 0.8 {
		t.Fatalf("the 80%% boundary must pass: %+v", share)
	}
	fail := pass
	fail.Talents = []Talent{
		{SpellID: 1, Rank: 5}, {SpellID: 2, Rank: 5}, {SpellID: 3, Rank: 5},
		{SpellID: 10, Rank: 5}, {SpellID: 11, Rank: 5},
	}
	c = checkOfID(t, Judge(fail, tables), RuleTalents)
	if share := partOf(t, c, "point_share"); share.Verdict != VerdictFail || share.Value != 0.6 {
		t.Fatalf("0.60 must fail: %+v", share)
	}
}

// TestTalentPickShareBoundary pins the 50% pick line: a talent at exactly 50 is a common pick, one at
// 49.9 is not. The bot takes 5 points in the 50% talent and 16 in uncommon ones, so the 49.9%
// talent (not taken) is capacity only when it is common — which lowers the share below the floor.
func TestTalentPickShareBoundary(t *testing.T) {
	mk := func(pct float64) *fakeTables {
		f := &fakeTables{specs: map[string]bool{"druid|balance": true}}
		f.picks = append(f.picks,
			aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "30-39", Tab: 0, SpellID: 1, MaxRank: 5, PickPct: 90},
			aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "all", Tab: 0, SpellID: 1, MaxRank: 5, PickPct: 90},
			aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "30-39", Tab: 0, SpellID: 20, MaxRank: 5, PickPct: pct},
			aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "all", Tab: 0, SpellID: 20, MaxRank: 5, PickPct: pct})
		for i := int64(10); i <= 14; i++ {
			f.picks = append(f.picks,
				aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "30-39", Tab: 0, SpellID: i, MaxRank: 5, PickPct: 10},
				aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "all", Tab: 0, SpellID: i, MaxRank: 5, PickPct: 10})
		}
		return f
	}
	b := Bot{ID: "bot1", Class: "druid", Spec: "balance", Level: 30,
		TalentsKnown: true, TalentsComplete: true, Talents: []Talent{
			{SpellID: 1, Rank: 5}, {SpellID: 10, Rank: 5}, {SpellID: 11, Rank: 5},
			{SpellID: 12, Rank: 5}, {SpellID: 13, Rank: 1},
		}}
	// 49.9: the talent is not common, the common picks account for 5 points, all of them taken.
	below := checkOfID(t, Judge(b, mk(49.9)), RuleTalents)
	if share := partOf(t, below, "point_share"); share.Verdict != VerdictPass {
		t.Fatalf("a 49.9%% pick is not common: %+v", share)
	}
	// 50.0: it is common, so the common picks account for 10 points and the bot takes 5: 50%.
	at := checkOfID(t, Judge(b, mk(50.0)), RuleTalents)
	if share := partOf(t, at, "point_share"); share.Verdict != VerdictFail {
		t.Fatalf("a 50%% pick is common: %+v", share)
	}
}

// pointShareTables is a fake table with five tab-0 common picks of five ranks each (25 points the
// common picks account for at 30-39) and a set of uncommon tab-0 picks to fill the build.
func pointShareTables() *fakeTables {
	f := &fakeTables{specs: map[string]bool{"druid|balance": true}}
	for i := int64(1); i <= 5; i++ {
		f.picks = append(f.picks,
			aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "30-39", Tab: 0, SpellID: i, MaxRank: 5, PickPct: 90},
			aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "all", Tab: 0, SpellID: i, MaxRank: 5, PickPct: 90})
	}
	for i := int64(10); i <= 14; i++ {
		f.picks = append(f.picks,
			aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "30-39", Tab: 0, SpellID: i, MaxRank: 5, PickPct: 10},
			aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "all", Tab: 0, SpellID: i, MaxRank: 5, PickPct: 10})
	}
	return f
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
	b.Talents = []Talent{{SpellID: 16814, Rank: 5, Tab: new(0)}, {SpellID: 16845, Rank: 3, Tab: new(0)},
		{SpellID: 16821, Rank: 2, Tab: new(0)}, {SpellID: 16819, Rank: 2, Tab: new(0)},
		{SpellID: 1223082, Rank: 2, Tab: new(0)}, {SpellID: 5570, Rank: 1, Tab: new(0)}}
	c := checkOfID(t, Judge(b, Default()), RuleTalents)
	if c.Verdict != VerdictInconclusive || !strings.Contains(c.Reason, "partial") {
		t.Fatalf("a 15-point build at 60 must be INCONCLUSIVE: %s (%s)", c.Verdict, c.Reason)
	}
}

// TestTalentCheckUncoveredTreeIsInconclusive pins the self-review case: below 60 there is no split
// part, so points in a tree the level group's common picks do not cover are not silently forgiven
// (a couple of common points would otherwise read as a 100% share).
func TestTalentCheckUncoveredTreeIsInconclusive(t *testing.T) {
	f := &fakeTables{specs: map[string]bool{"druid|balance": true}}
	f.picks = append(f.picks,
		aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "30-39", Tab: 0, SpellID: 1, MaxRank: 5, PickPct: 90},
		aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "all", Tab: 0, SpellID: 1, MaxRank: 5, PickPct: 90})
	for i := int64(10); i <= 14; i++ {
		f.picks = append(f.picks,
			aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "30-39", Tab: 1, SpellID: i, MaxRank: 5, PickPct: 10},
			aotc.TalentPick{Class: "druid", Spec: "balance", Levels: "all", Tab: 1, SpellID: i, MaxRank: 5, PickPct: 10})
	}
	b := Bot{ID: "bot1", Class: "druid", Spec: "balance", Level: 30,
		TalentsKnown: true, TalentsComplete: true, Talents: []Talent{
			{SpellID: 1, Rank: 5},
			{SpellID: 10, Rank: 5}, {SpellID: 11, Rank: 5}, {SpellID: 12, Rank: 5}, {SpellID: 13, Rank: 5}, {SpellID: 14, Rank: 1},
		}}
	c := checkOfID(t, Judge(b, f), RuleTalents)
	if c.Verdict != VerdictInconclusive || !strings.Contains(c.Reason, "do not cover") {
		t.Fatalf("a build off the common picks' trees must be INCONCLUSIVE: %s (%s)", c.Verdict, c.Reason)
	}
}

// TestTalentCheckUnknownIDIsInconclusive pins the rank-1 id contract: an id the tables do not name
// is INCONCLUSIVE with that reason, never a FAIL on an id the module may spell differently.
func TestTalentCheckUnknownIDIsInconclusive(t *testing.T) {
	b := bot("bot1", 60)
	b.Talents = append(balance60(), Talent{SpellID: 999999, Rank: 1})
	c := checkOfID(t, Judge(b, Default()), RuleTalents)
	if c.Verdict != VerdictInconclusive {
		t.Fatalf("an unknown talent id must be INCONCLUSIVE: %s (%s)", c.Verdict, c.Reason)
	}
	if !strings.Contains(c.Reason, "rank-1") || !strings.Contains(c.Reason, "999999") {
		t.Fatalf("the reason must name the id and the rank-1 rule: %q", c.Reason)
	}
}

// TestTalentCheckNoSplitBelowSixty pins that the split part exists only at 60: a level 49 build
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
