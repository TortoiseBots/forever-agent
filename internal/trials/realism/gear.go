package realism

// The gear check (REALISM-GEAR), the main one: the worn gear against what a player of the bot's
// level could have.
//
// Reported for every bot with known items: the average item level of the worn gear, the quality mix
// and the item levels' spread. Judged:
//
//	item_level  the share of slots whose item level is above the bot's level plus the band's own
//	            allowance (GearAllowance), PASS at or below GearAboveShareMax. The allowance is the
//	            best gear that level's own content drops: a bot dressed for a later band shows
//	            several pieces, one noisy reading does not.
//	source      any raid, PvP or reputation item: FAIL on one. The check reads the snapshot's item
//	            `source`; without it the part is INCONCLUSIVE, never a guess from the item's name.
//
// A bot that wears nothing known, or whose items carry no item level, reports the numbers it has and
// leaves the parts it cannot judge INCONCLUSIVE.

import (
	"fmt"
	"strings"
)

// gearBand is one level band's item-level allowance: an item at most Allowance above the bot's own
// level is what that level's content can give.
type gearBand struct {
	MaxLevel  int
	Allowance float64
}

// GearAllowance is the item level a bot may wear above its own level, by level band. The numbers
// follow the item level of the best gear each band's own content drops: a level 19 character's quest
// and dungeon gear sits within half a dozen item levels of its own; level 60's 30 covers the epic
// high end of the era (the last tier bands at item level 88-92), which is what a player of that
// level "could have" — raid gear on a pool bot is the `source` part's business, not this one's.
var GearAllowance = []gearBand{
	{MaxLevel: 19, Allowance: 6},
	{MaxLevel: 29, Allowance: 8},
	{MaxLevel: 39, Allowance: 10},
	{MaxLevel: 49, Allowance: 12},
	{MaxLevel: 59, Allowance: 15},
	{MaxLevel: 60, Allowance: 30},
}

// GearAboveShareMax is the most of the measured slots that may sit above the allowance: one in ten.
// A single piece of measurement noise is no realism fault; a bot dressed for a later band shows
// several slots over.
const GearAboveShareMax = 0.10

// allowanceFor is the band a bot level falls in.
func allowanceFor(level int) float64 {
	for _, band := range GearAllowance {
		if level <= band.MaxLevel {
			return band.Allowance
		}
	}
	return GearAllowance[len(GearAllowance)-1].Allowance
}

// gearCheck judges one bot's worn gear.
func gearCheck(b Bot, _ Tables) Check {
	if !b.ItemsKnown || len(b.Items) == 0 {
		return checkOf(RuleGear, []Part{
			part("item_level", VerdictInconclusive, 0, "",
				"the snapshot carries no worn items (items_known false): no equipment read for this bot"),
			part("source", VerdictInconclusive, 0, "", "no worn items to read a source from"),
		}, nil)
	}
	var levels []int
	qualities := map[int]int{}
	var knownQuality int
	var above int
	allowance := allowanceFor(b.Level)
	var aboveLines []string
	for _, item := range b.Items {
		if item.ItemLevel > 0 {
			levels = append(levels, item.ItemLevel)
			if float64(item.ItemLevel) > float64(b.Level)+allowance {
				above++
				aboveLines = append(aboveLines, fmt.Sprintf("%s: item %d at item level %d, above %d (level %d plus %.0f)",
					item.Slot, item.ItemID, item.ItemLevel, b.Level+int(allowance), b.Level, allowance))
			}
		}
		if item.Quality != nil {
			qualities[*item.Quality]++
			knownQuality++
		}
	}
	evidence := []string{}
	if len(levels) > 0 {
		avg := meanInts(levels)
		evidence = append(evidence, fmt.Sprintf("average item level %.1f over %d worn item(s)", avg, len(levels)))
		equipped := 0
		for _, item := range b.Items {
			if item.ItemLevel > 0 {
				equipped++
			}
		}
		if equipped < len(b.Items) {
			evidence = append(evidence, fmt.Sprintf("%d of %d worn item(s) carry no item level", len(b.Items)-equipped, len(b.Items)))
		}
	}
	if knownQuality > 0 {
		var mix []string
		for q := range qualityNames {
			if qualities[q] > 0 {
				mix = append(mix, fmt.Sprintf("%d %s", qualities[q], QualityName(q)))
			}
		}
		evidence = append(evidence, "quality mix: "+strings.Join(mix, ", "))
	}

	var parts []Part
	if len(levels) == 0 {
		parts = append(parts, part("item_level", VerdictInconclusive, 0, "",
			"no worn item carries an item level (the item read-out is missing)"))
	} else {
		share := float64(above) / float64(len(levels))
		parts = append(parts, Part{
			Name: "item_level", Verdict: verdictAtMost(share, GearAboveShareMax), Value: share,
			Measure: fmt.Sprintf("%d of %d item level(s) above level %d plus %.0f (%.0f%% allowed)",
				above, len(levels), b.Level, allowance, GearAboveShareMax*100),
			Lines: aboveLines,
		})
	}

	// Every worn item must carry a source for the part to pass: one permitted piece beside an
	// unknown one would otherwise hide a raid, PvP or reputation item it cannot see.
	judged, forbidden, unknown := 0, 0, 0
	var sourceLines []string
	for _, item := range b.Items {
		if item.Source == "" {
			unknown++
			continue
		}
		judged++
		switch item.Source {
		case "raid", "pvp", "reputation":
			forbidden++
			sourceLines = append(sourceLines, fmt.Sprintf("%s: item %d from %s", item.Slot, item.ItemID, item.Source))
		}
	}
	switch {
	case forbidden > 0:
		parts = append(parts, Part{
			Name: "source", Verdict: VerdictFail, Value: float64(forbidden),
			Measure: fmt.Sprintf("%d of %d sourced item(s) come from a raid, PvP or reputation source", forbidden, judged),
			Lines:   sourceLines,
		})
	case unknown == len(b.Items):
		parts = append(parts, part("source", VerdictInconclusive, 0, "",
			"no worn item carries a source (where an item comes from is not read by the snapshot command)"))
	case unknown > 0:
		parts = append(parts, part("source", VerdictInconclusive, 0, "",
			fmt.Sprintf("%d of %d worn item(s) carry no source, so a raid, PvP or reputation item cannot be ruled out",
				unknown, len(b.Items))))
	default:
		parts = append(parts, Part{
			Name: "source", Verdict: VerdictPass, Value: 0,
			Measure: fmt.Sprintf("none of %d sourced item(s) is raid, PvP or reputation", judged),
		})
	}
	return checkOf(RuleGear, parts, evidence)
}

// meanInts is the mean of a list of ints, 0 for an empty list.
func meanInts(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0
	for _, v := range values {
		sum += v
	}
	return float64(sum) / float64(len(values))
}

// meanFloats is the mean of a list of floats, 0 for an empty list.
func meanFloats(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}
