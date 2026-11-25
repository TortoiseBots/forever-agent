package realism

// The talent check (REALISM-TALENTS): the bot's talent points against the picks and splits real
// players of its spec use at its level (talent-picks.csv and talent-splits-60.csv).
//
// Two parts:
//
//	point_share  the share of the bot's talent points that sit in the talents at least TalentPickShare
//	             percent of its level group's players of the spec took. PASS at or above
//	             TalentPointShareMin.
//	split        level 60 only: the bot's point split (points per tree) against the nearest of the
//	             spec's 20 most common splits, in points moved between trees. PASS at or below
//	             SplitPointsMax, and INCONCLUSIVE when a point sits in a talent the tables do not
//	             name (the split would be a guess).
//
// The check needs the bot's whole build: a partial read-back (a marker kit) would read as missing
// points, so talents_complete false is INCONCLUSIVE, never a FAIL on the points that are there.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/data/aotc"
	"github.com/pjerra/forever-agent/internal/trials/playdata"
)

const (
	// TalentPickShare is the pick share that makes a talent common for its level group, in percent:
	// the same 50% the player-data talent rule reads (playdata.MinTalentShare), so "the common
	// picks" means one thing across the bench.
	TalentPickShare = 50.0
	// TalentPointShareMin is the least share of a bot's points that must sit in those common picks:
	// four points in five. A spec's off-picks (a hybrid's second tree, a PvP talent) are a fifth of
	// a build at most, which is where the number comes from.
	TalentPointShareMin = 0.80
	// SplitPointsMax is how many points a level-60 bot's tree split may differ from the nearest
	// common split in talent-splits-60.csv: five points moved between trees. The file holds each
	// spec's 20 most common splits, which normally covers most of its characters.
	SplitPointsMax = 5.0
	// splitTabs is the number of talent trees a split names (three in 1.12).
	splitTabs = 3
	// TalentPointsMax is the talent points a level 60 character has (one per level from 10).
	TalentPointsMax = 51
)

// TalentPoints is the talent points a character of a level has (1.12: one per level from 10, capped
// at 60's TalentPointsMax): a build with fewer points is not the character's whole build, whatever
// the snapshot's talents_complete says.
func TalentPoints(level int) int {
	n := level - 9
	switch {
	case n < 0:
		return 0
	case n > TalentPointsMax:
		return TalentPointsMax
	}
	return n
}

// talentCheck judges one bot's talents.
func talentCheck(b Bot, t Tables) Check {
	class, spec := botSpec(b)
	if !specKnown(t, class, spec) {
		return checkOf(RuleTalents, []Part{part("point_share", VerdictInconclusive, 0, "",
			specMissingReason(t, class, spec))}, nil)
	}
	if !b.TalentsKnown || len(b.Talents) == 0 {
		return checkOf(RuleTalents, []Part{
			part("point_share", VerdictInconclusive, 0, "",
				"the snapshot carries no talents (talents_known false): the bot module has no talent read-out"),
			part("split", VerdictInconclusive, 0, "", "no talents to split"),
		}, nil)
	}
	if !b.TalentsComplete {
		return checkOf(RuleTalents, []Part{
			part("point_share", VerdictInconclusive, 0, "",
				"the snapshot's talent list is partial (talents_complete false): its missing points would read as off-picks"),
		}, nil)
	}
	group := playdata.LevelGroup(b.Level)
	if group == "" {
		return checkOf(RuleTalents, []Part{part("point_share", VerdictInconclusive, 0, "",
			fmt.Sprintf("level %d has no talent points", b.Level))}, nil)
	}
	common := t.CommonTalents(class, spec, group, TalentPickShare)
	if len(common) == 0 {
		return checkOf(RuleTalents, []Part{part("point_share", VerdictInconclusive, 0, "",
			fmt.Sprintf("no %s/%s talent pick reaches %.0f%% at %s", class, spec, TalentPickShare, group))}, nil)
	}
	commonSet := map[int64]bool{}
	for _, p := range common {
		commonSet[p.SpellID] = true
	}
	var total, inCommon int
	var offLines []string
	for _, talent := range b.Talents {
		total += talent.Rank
		if commonSet[talent.SpellID] {
			inCommon += talent.Rank
			continue
		}
		offLines = append(offLines, fmt.Sprintf("%d point(s) in talent %d, not among the level group's common picks",
			talent.Rank, talent.SpellID))
	}
	if total == 0 {
		return checkOf(RuleTalents, []Part{part("point_share", VerdictInconclusive, 0, "",
			"the snapshot's talents carry no ranks")}, nil)
	}
	// The whole build, not only the flag: a level 40 character has 31 points, so a 15-point list
	// marked complete is a partial read and its missing points would read as off-picks.
	if expected := TalentPoints(b.Level); total < expected {
		reason := fmt.Sprintf("the listed build holds %d point(s) of the %d a level %d character has: the list is partial, whatever talents_complete says",
			total, expected, b.Level)
		return checkOf(RuleTalents, []Part{
			part("point_share", VerdictInconclusive, 0, "", reason),
			part("split", VerdictInconclusive, 0, "", reason),
		}, nil)
	}
	share := float64(inCommon) / float64(total)
	parts := []Part{{
		Name: "point_share", Verdict: verdictAtLeast(share, TalentPointShareMin), Value: share,
		Measure: fmt.Sprintf("%d of %d points in %s's common picks at %s (%.0f%% floor)", inCommon, total, spec, group,
			TalentPointShareMin*100),
		Lines: offLines,
	}}
	evidence := []string{fmt.Sprintf("%d point(s), %d in the %d common picks at %s", total, inCommon, len(common), group)}
	if b.Level == 60 {
		parts = append(parts, splitPart(b, t, class, spec, group))
	}
	return checkOf(RuleTalents, parts, evidence)
}

// splitPart judges a level-60 bot's point split against the spec's common splits.
func splitPart(b Bot, t Tables, class, spec, group string) Part {
	var points [splitTabs]int
	var unplaced int
	for _, talent := range b.Talents {
		tab, ok := talentTab(t, class, spec, group, talent.SpellID)
		if !ok {
			unplaced += talent.Rank
			continue
		}
		points[tab] += talent.Rank
	}
	if unplaced > 0 {
		return part("split", VerdictInconclusive, 0, "",
			fmt.Sprintf("%d point(s) sit in talents the tables do not name, so the split would be a guess", unplaced))
	}
	splits := t.SplitsFor(class, spec)
	if len(splits) == 0 {
		return part("split", VerdictInconclusive, 0, "",
			fmt.Sprintf("talent-splits-60.csv names no split for %s/%s", class, spec))
	}
	nearest, distance := "", 0
	var nearestPct float64
	for i, split := range splits {
		d, ok := splitDistance(points, split.Split)
		if !ok {
			continue
		}
		if i == 0 || d < distance {
			nearest, distance, nearestPct = split.Split, d, split.Pct
		}
	}
	if nearest == "" {
		return part("split", VerdictInconclusive, 0, "",
			fmt.Sprintf("none of %s/%s's %d splits is three numbers", class, spec, len(splits)))
	}
	measure := fmt.Sprintf("%s is %d point(s) from the nearest common split %s (%.1f%% of the spec's characters)",
		splitText(points), distance, nearest, nearestPct)
	return Part{
		Name: "split", Verdict: verdictAtMost(float64(distance), SplitPointsMax), Value: float64(distance),
		Measure: measure,
	}
}

// talentTab finds the tree a talent sits in: the bot's own level group's row first, then the level
// 60 and all-levels rows (a talent's tree does not change with level).
func talentTab(t Tables, class, spec, group string, spellID int64) (int, bool) {
	for _, levels := range []string{group, aotc.LevelMax, "all"} {
		for _, row := range t.CommonTalents(class, spec, levels, 0) {
			if row.SpellID == spellID && row.Tab >= 0 && row.Tab < splitTabs {
				return row.Tab, true
			}
		}
	}
	return 0, false
}

// splitDistance is how many points a bot's split is from a common one: half the sum of the tree
// differences, which is the number of points moved between trees.
func splitDistance(bot [splitTabs]int, split string) (int, bool) {
	fields := strings.Split(split, "/")
	if len(fields) != splitTabs {
		return 0, false
	}
	diff := 0
	for i, field := range fields {
		n, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil {
			return 0, false
		}
		diff += absInt(bot[i] - n)
	}
	return diff / 2, true
}

// splitText is a bot's split as the tables write it ("38/0/13").
func splitText(points [splitTabs]int) string {
	return fmt.Sprintf("%d/%d/%d", points[0], points[1], points[2])
}

// absInt is |n|.
func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// botSpec is a bot's class and the player tables' name for its spec.
func botSpec(b Bot) (string, string) {
	return strings.ToLower(strings.TrimSpace(b.Class)), playdata.SpecName(b.Spec)
}

// specKnown reports whether the tables name the bot's class and spec.
func specKnown(t Tables, class, spec string) bool {
	return specsTable(t, class, spec)
}

// specMissingReason is what a check says when it cannot name the bot's class and spec.
func specMissingReason(t Tables, class, spec string) string {
	if spec == "" {
		return fmt.Sprintf("no spec in the snapshot for %s (the bot module has no spec read-out)", class)
	}
	return fmt.Sprintf("no player data for %s/%s (spec-population.csv)", class, spec)
}

// specsTable reports whether the tables know the spec; a nil table knows nothing.
func specsTable(t Tables, class, spec string) bool {
	if t == nil || spec == "" {
		return false
	}
	return t.SpecKnown(class, spec)
}
