// Package classrules holds the bench's class rules: readings of one repeat's own recording (events
// and samples) that judge whether a party played its roles, not whether a bot learned a spell.
//
// Rule "healer assignment" (HEAL-ASSIGNMENT): in one fight of at least MinFightSeconds (30 s) with
// at least two proven healers and a named tank, the main healer (the one with the most healing)
// must put at least MainHealerTankShare (60%) of its healing on the party's tanks, and the other
// healers must keep their healing off the tanks (at most OtherHealerTankShare, 40%) and spread it
// over the rest of the party (at least MinRestTargets, 2, distinct non-tank allies). Only heals
// inside the fight window (fight_start, the pull, to fight_end) and outside the recording's
// excluded spans count. A healer is judged only with MinHealerHeals (10) placed heal events of its
// own; heals on pets or units the setup did not name are left out of the shares and reported as an
// "other" count, so one pet heal cannot silence the rule. The shares are gross healing: the heal
// log's amount is the original heal and the recording carries no overheal. The rule reports
// INCONCLUSIVE — never FAIL — when the recording cannot carry the verdict: no events file, no fight
// window, a fight under MinFightSeconds, no heal rows, fewer than two judged healers (a one-healer
// party has no split to check), no tank role in the party, a tank or healer the setup proves no guid
// for, no single main healer (a tie at the top), or a placed heal row without a readable amount.
//
// The rows are bench readings, added by `trials score --class-rules` under trial.json's
// `class_rules`; they are off by default so a default score stays exactly the bench's, and they never
// change the exit code. They read the run's own record: the trial's party roles (the player's role
// or forced role, the bot's role, the extras' roles), the setup's guids, the recording's marks and
// samples (the fight window and the excluded spans) and the normalised events' heal rows.
package classrules

import (
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// The rule ids, as they appear on the report card.
const (
	RuleHealAssignment = "HEAL-ASSIGNMENT"
)

// The rule's thresholds, named so a report and a test can quote them, each with the reason for its
// value.
const (
	// MainHealerTankShare is the share of the main healer's healing that must land on the party's
	// tanks. It is a clear majority: the main healer's job is the tank, and 60% leaves it room to
	// top up the rest of the party without losing the tank's cover.
	MainHealerTankShare = 0.60
	// OtherHealerTankShare is the most of any other healer's healing that may land on the tanks.
	// It is the main healer's 60% mirrored (the pair sums to 100), so the two numbers describe one
	// split: the main healer on the tank, the others on the party.
	OtherHealerTankShare = 0.40
	// MinHealerHeals is the placed heal events a healer needs before the rule reads its shares: a
	// handful of heals has no distribution to judge. Ten heals is about one every 3 s in a fight
	// of MinFightSeconds, the shortest fight the rule judges.
	MinHealerHeals = 10
	// MinRestTargets is the distinct non-tank party members the other healers must have healed
	// between them. One ally could be a single off-target; two distinct allies is a spread.
	MinRestTargets = 2
	// MinFightSeconds is the fight length the rule needs before it reads shares: at the run's
	// one-second sampling a shorter fight holds too few heals to reach MinHealerHeals, and the
	// shares would be one or two casts wide, so the rule reports INCONCLUSIVE instead.
	MinFightSeconds = 30.0
)

// Verdicts are the words a Row carries.
const (
	VerdictPass         = "PASS"
	VerdictFail         = "FAIL"
	VerdictInconclusive = "INCONCLUSIVE"
)

// Roles a party member can carry in a trial definition.
const (
	RoleTank   = "tank"
	RoleHealer = "healer"
	RoleDPS    = "dps"
)

// Member is one party member as the trial definition and the setup name it: the unit alias the
// events and the rules use, the hire's name when the setup proved one, its guid, and its role.
type Member struct {
	Unit  string // "$player", "$bot", "$extra1", ...
	Name  string
	GUID  string // lower-case, "" when the setup proved none
	Class string
	Spec  string
	Role  string // RoleTank, RoleHealer, RoleDPS, "" when the trial names none
}

// Heal is one normalised heal row: when, who, on whom and how much.
type Heal struct {
	At          float64
	Src         string  // the caster's unit alias, "" when it is not a party member
	Dst         string  // the target's unit alias, "" when it is not a party member
	Amount      float64 // 0 when AmountKnown is false
	AmountKnown bool
}

// Span is one excluded stretch of the repeat's timeline, in the events' seconds: GM heals and
// the like (the normaliser's kind: excluded rows). Rows inside it never count.
type Span struct {
	A, B float64
	Why  string
}

// Record is one repeat's view of the party and its healing.
type Record struct {
	Members     []Member
	Heals       []Heal
	EventsKnown bool // events.jsonl was read; false means the file is missing or unreadable
	// Window is the fight the rule reads, [fight_start, fight_end] in the events' seconds (the
	// pull is the fight's first hostile action). nil when marks.jsonl and samples.jsonl carry no
	// usable fight, so the rule reports the missing window instead of judging the whole repeat.
	Window   *[2]float64
	Excluded []Span // the recording's excluded spans; a heal inside one never counts
}

// Row is one class rule's verdict for one repeat, in the report card's row shape (the same keys a
// player-data row carries).
type Row struct {
	Rep      string
	ID       string
	Verdict  string
	K, N     int64
	Evidence []string
	Reason   string
}

// Doc renders the row for trial.json and the report card.
func (r Row) Doc() map[string]any {
	evidence := r.Evidence
	if evidence == nil {
		evidence = []string{}
	}
	return map[string]any{
		"rep": r.Rep, "id": r.ID, "verdict": r.Verdict, "k": r.K, "n": r.N,
		"evidence": evidence, "reason": r.Reason, "source": "trials record",
	}
}

// Rows judges one repeat: every class rule, in a stable order.
func Rows(rec Record) []Row {
	return []Row{healAssignmentRow(rec)}
}

// memberByGUID indexes the party by guid (lower case).
func memberByGUID(members []Member) map[string]int {
	out := make(map[string]int, len(members))
	for i, m := range members {
		if m.GUID != "" {
			out[strings.ToLower(m.GUID)] = i
		}
	}
	return out
}

// sortedEvidence keeps a rule's lines stable whatever map order found them.
func sortedEvidence(lines []string) []string {
	out := append([]string(nil), lines...)
	sort.Strings(out)
	return out
}

// orNone is a unit's label for an evidence line: its alias and name when the record carries one.
func orNone(m Member) string {
	label := m.Unit
	if m.Name != "" {
		label += " (" + m.Name + ")"
	}
	if m.Role != "" {
		label += " " + m.Role
	}
	return label
}

// roundPct is a percent for an evidence line, whole numbers.
func roundPct(v float64) int64 { return int64(pyjson.Round(v*100, 0)) }
