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

	// Rule 1: idle in combat longer than R1_IDLE_S, after the group has been in combat R1_GROUP_COMBAT_MIN_S.
	R1_IDLE_S             = 10.0
	R1_GROUP_COMBAT_MIN_S = 3.0
	// R1_CAST_PAD_S is the window either side of a frame in which a cast or a swing by the member says it is not
	// idle: the Python checker's `ts[i] <= f['t'] + 1.0` around a 1 s lookback.
	R1_CAST_PAD_S = 1.0
	// R1_HURT_PCT is the health below which a healer's party has work: a healer with nobody hurt is not idle.
	R1_HURT_PCT = 95.0

	// Rule 2: a healer not healing an ally under R2_LOW_HP_PCT for longer than R2_MIN_S, its mana over
	// R2_MANA_MIN_PCT and the ally within R2_RANGE_YD.
	R2_LOW_HP_PCT   = 40.0
	R2_MIN_S        = 4.0
	R2_MANA_MIN_PCT = 10.0
	R2_RANGE_YD     = 40.0
	// R2_HEAL_PAD_S is how near a heal row (or a heal-spell cast) has to be to a frame to say the healer worked.
	R2_HEAL_PAD_S = 1.5

	// Rule 3: an add (a non-boss hostile) attacking a non-tank for longer than this.
	R3_MIN_S = 5.0

	// Rule 4: standing in a damaging AoE longer than R4_MIN_S; ticks further apart than R4_TICK_GAP_S end it.
	R4_MIN_S      = 3.0
	R4_TICK_GAP_S = 3.5

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

	// Rule 8: the same buff cast more than R8_MAX_CASTS times in R8_WINDOW_S on one target. R8_NEAR_START_S and
	// R8_NEAR_AURA_S are the Python's genuineness windows: a spell_start just before the completion, or an
	// aura_applied just after it, says the cast was real and not a periodic row.
	R8_MAX_CASTS    = 3
	R8_WINDOW_S     = 60.0
	R8_NEAR_START_S = 0.5
	R8_NEAR_AURA_S  = 0.2

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

	// Rule 11: casts started with nothing landing for longer than R11_MIN_S while mana rose. R11_CAST_LAND_S is
	// how long after a start a spell_go of the same spell may still count as that cast landing; R11_OUTPUT_PAD_S
	// is the window either side of a frame in which a damage, heal or swing row breaks a silent stretch.
	R11_MIN_S        = 10.0
	R11_CAST_LAND_S  = 2.5
	R11_OUTPUT_PAD_S = 1.5
	R11_MIN_STARTS   = 2

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

	// Rule 15: a removable debuff up longer than R15_MIN_S with an able dispeller alive, within R15_RANGE_YD and
	// not casting. R15_GRACE_S is how long the debuff had to be removed before the dispellers are judged.
	R15_MIN_S    = 8.0
	R15_RANGE_YD = 30.0
	R15_GRACE_S  = 8.0

	// Rule 16: a class's interrupt spells, their base cooldowns (the untalented 1.12 values: Pummel 10 s, Shield
	// Bash 12 s, Hammer of Justice 60 s, Kick 10 s, Silence 45 s, Earth Shock 6 s, Counterspell 24 s) and their
	// ranges are in interruptRows below. R16_CAST_PAD_S is how far before a completion a frame is near enough to
	// judge the interrupters by (the nearest one wins).
	R16_CAST_PAD_S = 2.0

	// Rule 19: the group must have a member within R19_REACH_YD of the higher-priority mob for it to be "in reach"
	// of the pull.
	R19_REACH_YD = 40.0
)

// interruptRows are a class's interrupt spells: the spell id, how long its cooldown runs from the member's own
// cast row, and its range in yards. A class not in this table is never counted as an interrupter.
var interruptRows = map[string][]interruptRow{
	"warrior": {{spell: 6552, cooldownS: 10, rangeYD: 5}, {spell: 72, cooldownS: 12, rangeYD: 5}},
	"paladin": {{spell: 853, cooldownS: 60, rangeYD: 10}},
	"rogue":   {{spell: 1766, cooldownS: 10, rangeYD: 5}},
	"priest":  {{spell: 15487, cooldownS: 45, rangeYD: 30}},
	"shaman":  {{spell: 8042, cooldownS: 6, rangeYD: 20}},
	"mage":    {{spell: 2139, cooldownS: 24, rangeYD: 30}},
}

// interruptRow is one interrupt spell: its id, its base cooldown and its range.
type interruptRow struct {
	spell     int
	cooldownS float64
	rangeYD   float64
}

// ruleNames are the Python checker's RULE_NAMES rows for the rules this package has.
var ruleNames = map[int]string{
	1:  "idle in combat >10 s",
	2:  "healer not healing <40% ally >4 s",
	3:  "tank without aggro: add on non-tank >5 s",
	4:  "standing in damaging AoE >3 s",
	5:  "stuck >30 s while group moves",
	6:  "eat/drink while group in combat",
	8:  "buff flip-flop (>3 casts / 60 s / target)",
	9:  "fall/drown death",
	10: "power waste at 100% in combat >30 s",
	11: "cast started but nothing lands while mana rises >10 s (cast-cancel signal)",
	13: "healer under 30% mana out of combat >20 s, no drink 15 s, group not moving",
	14: "member >40 yd from the group centroid >20 s in combat",
	15: "missed decurse: removable debuff >8 s with an able dispeller alive, in range, not casting",
	16: "trash tactics: must-interrupt cast completed with an interrupter free",
	17: "trash tactics: crowd control tried on an immune mob",
	18: "trash tactics: listed dispellable debuff left on too long",
	19: "trash tactics: lower-priority mob killed before a higher one",
}
