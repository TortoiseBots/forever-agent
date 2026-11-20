// Package bughunt finds bot misbehaviour in recorded runs: watch frames (F) and a watch's combat log (C),
// Forever trials repeats (T) and a roster record (D). It is the Go home of the checker the Python bug hunt
// used, with the same thresholds, the same episode rule and the same hit fields, so a run scores the same.
//
// A hit names a rule, the run and dataset it came from, the subject (the run's own identity for it: a guid for a
// watch, the character key for a trials repeat), the episode's clock and duration, a stable key the tickets are
// grouped by, the deaths near it and its cost in seconds. Rules only report what the recording shows: an input a
// rule needs but the run does not carry makes it silent, never a guess.
package bughunt

// The thresholds, each with the name the Python checker gave it and the same value, so a difference between the two
// is a diff of this file. The few names the Python leaves as literals (rule 9's window) are named here and say so.
const (
	// MAX_GAP_S breaks an episode: frames further apart than this are not one span of behaviour.
	MAX_GAP_S = 16.0
	// DEATH_COST_S is what a death near a hit adds to its cost, so a hit that killed counts worse.
	DEATH_COST_S = 60.0

	// Rule 3: an add (a non-boss hostile) attacking a non-tank for longer than this.
	R3_MIN_S = 5.0

	// Rule 5: a bot still on follow, this far from the party leader, for this long, is stuck.
	R5_STUCK_YD = 40.0
	R5_STUCK_S  = 30.0
	// R5_STILL_YD is "still": the bot's own path over the episode stays under this.
	R5_STILL_YD = 1.0
	// R5_STUCK_MERGE_S: two stuck episodes of one bot closer than this are one pocket.
	R5_STUCK_MERGE_S = 60.0
	// R5_STAY_BACK_S: a stay within this of the episode's start holds it (the bot was held while the lead pulled).
	R5_STAY_BACK_S = 10.0

	// Rule 6: eating or drinking while the group is in combat.
	R6_GROUP_COMBAT_MIN_S = 3.0  // the group must have been in combat this long first
	R6_NEAR_COMBAT_YD     = 60.0 // and an in-combat hostile must be this near the eater
	R6_LOW_MANA_PCT       = 30.0 // eating at or below this mana % is a need, above it a waste

	// Rule 9 (the z-drop heuristic): the height dropped before a death, and the window it is read over. The window
	// and the frame count are walk_z_drop's defaults, which the old trace rule used too.
	R9_FALL_DROP_YD  = 12.0
	R9_DROP_WINDOW_S = 4.0
	R9_DROP_AFTER_S  = 0.5
	R9_DROP_MIN_ROWS = 2

	// Rule 10: power at or above R10_FULL_PCT for longer than R10_MIN_S in combat while a member is under
	// R10_DAMAGED_PCT health.
	R10_FULL_PCT    = 99.0
	R10_MIN_S       = 30.0
	R10_DAMAGED_PCT = 80.0

	// Rule 13: a healer below R13_MANA_PCT out of combat for longer than R13_MIN_S, no drink within
	// R13_DRINK_PAD_S either side, group not moving (median displacement under R13_GROUP_STILL_YD over
	// R13_MIN_OTHERS members).
	R13_MIN_S          = 20.0
	R13_MANA_PCT       = 30.0
	R13_DRINK_PAD_S    = 15.0
	R13_GROUP_STILL_YD = 5.0
	R13_MIN_OTHERS     = 2

	// Rule 14: a member further than R14_DIST_YD from the centroid of the other group members for longer than
	// R14_MIN_S in combat, with at least R14_MIN_OTHERS others to judge the centroid from.
	R14_MIN_S      = 20.0
	R14_DIST_YD    = 40.0
	R14_MIN_OTHERS = 3
)

// ruleNames are the Python checker's RULE_NAMES rows for the rules this package has.
var ruleNames = map[int]string{
	3:  "tank without aggro: add on non-tank >5 s",
	5:  "stuck >30 s while group moves",
	6:  "eat/drink while group in combat",
	9:  "fall/drown death",
	10: "power waste at 100% in combat >30 s",
	13: "healer under 30% mana out of combat >20 s, no drink 15 s, group not moving",
	14: "member >40 yd from the group centroid >20 s in combat",
}
