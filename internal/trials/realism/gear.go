package realism

// The gear check (REALISM-GEAR), the main one: the worn gear against what a player of the bot's
// level could have.
//
// Reported for every bot with known items: the average item level of the worn gear, the quality mix
// and the item levels' spread. Judged (all three parts decisive: no PASS over a partial reading):
//
//	required_level  any item whose required level is above the bot's level: FAIL on one. This is the
//	                clearest "no player of this level could wear it" signal, and it is a game fact,
//	                not a player choice.
//	item_level      the share of slots whose item level is above the bot's level plus the band's own
//	                allowance (GearAllowance), PASS at or below GearAboveShareMax.
//	source          any raid, PvP or reputation item: FAIL on one. The check reads the snapshot's item
//	                `source`; without it the part is INCONCLUSIVE, never a guess from the item's name.
//
// A bot that wears nothing, or whose items carry no item level, reports the numbers it has and leaves
// the parts it cannot judge INCONCLUSIVE.

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

// GearAllowance is the item level a bot may wear above its own level, by level band. These are
// STARTING VALUES to tune, not facts about players: each was taken as the 95th percentile of (item
// level minus required level) over the game's own item table, counted at the band's levels. That item
// table, the query and its version are NOT in this repository (data/aotc has no item level or
// required level), so the values cannot be reproduced from it; the manual says so. Through level 59 an
// item sits about five item levels above the level its required level names; at 60 the raid tier
// reaches item levels 88-92 on required-level-60 items, so the high percentile is 30. A real batch may
// move them. Raid gear on a pool bot is the `source` part's business, not this one's.
var GearAllowance = []gearBand{
	{MaxLevel: 19, Allowance: 5},
	{MaxLevel: 29, Allowance: 5},
	{MaxLevel: 39, Allowance: 5},
	{MaxLevel: 49, Allowance: 5},
	{MaxLevel: 59, Allowance: 5},
	{MaxLevel: 60, Allowance: 30},
}

// GearAboveShareMax is the most of the measured slots that may sit above the allowance: one in ten.
// A starting value to tune, not a fact about players: a single piece of measurement noise is no
// realism fault, while a bot dressed for a later band shows several slots over.
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
	if !b.ItemsKnown {
		reason := "the snapshot carries no worn items (items_known false): no equipment read for this bot"
		return checkOf(RuleGear, []Part{
			part("required_level", VerdictInconclusive, 0, "", reason),
			part("item_level", VerdictInconclusive, 0, "", reason),
			part("source", VerdictInconclusive, 0, "", reason),
		}, nil)
	}
	if len(b.Items) == 0 {
		reason := "the bot wears nothing (items_known true, no items): there is no gear to judge"
		return checkOf(RuleGear, []Part{
			part("required_level", VerdictInconclusive, 0, "", reason),
			part("item_level", VerdictInconclusive, 0, "", reason),
			part("source", VerdictInconclusive, 0, "", reason),
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
	switch {
	case len(levels) == 0:
		parts = append(parts, Part{Name: "item_level", Verdict: VerdictInconclusive, Decisive: true,
			Reason: "no worn item carries an item level (the item read-out is missing)"})
	case len(levels) < len(b.Items):
		// An unread item could be the one far above the allowance: the part judges what it saw and
		// says how many slots it could not, never a PASS over a partial set.
		parts = append(parts, Part{Name: "item_level", Verdict: VerdictInconclusive, Decisive: true,
			Reason: fmt.Sprintf("%d of %d worn item(s) carry no item level, so an over-allowance one cannot be ruled out",
				len(b.Items)-len(levels), len(b.Items))})
	default:
		share := float64(above) / float64(len(levels))
		parts = append(parts, Part{
			Name: "item_level", Verdict: verdictAtMost(share, GearAboveShareMax), Value: share,
			Measure: fmt.Sprintf("%d of %d item level(s) above level %d plus %.0f (%.0f%% allowed)",
				above, len(levels), b.Level, allowance, GearAboveShareMax*100),
			Lines: aboveLines, Decisive: true,
		})
	}

	// required_level: a game fact the snapshot's item read carries. An item above the bot's level is
	// one no player of that level could wear, so one is a FAIL of realism, not a share.
	requiredJudged, requiredOver := 0, 0
	var requiredLines []string
	for _, item := range b.Items {
		if item.RequiredLevel <= 0 {
			continue
		}
		requiredJudged++
		if item.RequiredLevel > b.Level {
			requiredOver++
			requiredLines = append(requiredLines, fmt.Sprintf("%s: item %d needs level %d, the bot is level %d",
				item.Slot, item.ItemID, item.RequiredLevel, b.Level))
		}
	}
	switch {
	case requiredOver > 0:
		parts = append(parts, Part{Name: "required_level", Verdict: VerdictFail, Value: float64(requiredOver),
			Measure: fmt.Sprintf("%d of %d item(s) need a higher level than the bot has", requiredOver, requiredJudged),
			Lines:   requiredLines, Decisive: true})
	case requiredJudged == 0:
		parts = append(parts, Part{Name: "required_level", Verdict: VerdictInconclusive, Decisive: true,
			Reason: "no worn item carries a required level (the item read-out names none)"})
	case requiredJudged < len(b.Items):
		parts = append(parts, Part{Name: "required_level", Verdict: VerdictInconclusive, Decisive: true,
			Reason: fmt.Sprintf("%d of %d worn item(s) carry no required level, so a too-high one cannot be ruled out",
				len(b.Items)-requiredJudged, len(b.Items))})
	default:
		parts = append(parts, Part{Name: "required_level", Verdict: VerdictPass, Value: 0, Decisive: true,
			Measure: fmt.Sprintf("all %d item(s) are wearable at level %d", requiredJudged, b.Level)})
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
			Name: "source", Verdict: VerdictFail, Value: float64(forbidden), Decisive: true,
			Measure: fmt.Sprintf("%d of %d sourced item(s) come from a raid, PvP or reputation source", forbidden, judged),
			Lines:   sourceLines,
		})
	case unknown == len(b.Items):
		parts = append(parts, Part{Name: "source", Verdict: VerdictInconclusive, Decisive: true,
			Reason: "no worn item carries a source (where an item comes from is not read by the snapshot command)"})
	case unknown > 0:
		parts = append(parts, Part{Name: "source", Verdict: VerdictInconclusive, Decisive: true,
			Reason: fmt.Sprintf("%d of %d worn item(s) carry no source, so a raid, PvP or reputation item cannot be ruled out",
				unknown, len(b.Items))})
	default:
		parts = append(parts, Part{
			Name: "source", Verdict: VerdictPass, Value: 0, Decisive: true,
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
