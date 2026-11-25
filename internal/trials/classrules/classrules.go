// Package classrules holds the bench's class rules: readings of one repeat's own recording (events
// and samples) that judge whether a party played its roles, not whether a bot learned a spell.
//
// Rule "healer assignment" (HEAL-ASSIGNMENT): in a fight with at least two proven healers and a
// named tank, the main healer (the one with the most healing) must put at least MainHealerTankShare
// (60%) of its healing on the party's tanks, and the other healers must keep their healing off the
// tanks (at most OtherHealerTankShare, 40%) and spread it over the rest of the party (at least
// MinRestTargets, 2, distinct non-tank allies). A healer is judged only with MinHealerHeals (10)
// heal events of its own. The rule reports INCONCLUSIVE — never FAIL — when the recording cannot
// carry the verdict: no events file, no heal rows, fewer than two judged healers (a one-healer party
// has no split to check), no tank role in the party, a tank or healer the setup proves no guid for,
// no single main healer (a tie at the top), a heal row without a readable amount, or a heal whose
// target is not a party member the setup named (a pet, or a guid that never reached the setup): a
// share computed over unreadable targets is not evidence.
//
// The rows are bench readings, added by `trials score --class-rules` under trial.json's
// `class_rules`; they are off by default so a default score stays exactly the bench's, and they never
// change the exit code. They read the run's own record: the trial's party roles (the player's role
// or forced role, the bot's role, the extras' roles), the setup's guids, the normalised events' heal
// rows and the samples' bars.
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

// The rule's thresholds, named so a report and a test can quote them.
const (
	// MainHealerTankShare is the share of the main healer's healing that must land on the party's
	// tanks: a tank healer who sends most of its healing elsewhere is not covering the tank.
	MainHealerTankShare = 0.60
	// OtherHealerTankShare is the most of any other healer's healing that may land on the tanks:
	// above it the second healer is doing the main healer's job instead of the rest of the party.
	OtherHealerTankShare = 0.40
	// MinHealerHeals is the heal events a healer needs before the rule reads its shares: a healer
	// with a handful of heals has no distribution to judge.
	MinHealerHeals = 10
	// MinRestTargets is the distinct non-tank party members the other healers must have healed
	// between them: below it their healing is not split across the rest of the party.
	MinRestTargets = 2
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

// Record is one repeat's view of the party and its healing.
type Record struct {
	Members     []Member
	Heals       []Heal
	EventsKnown bool // events.jsonl was read; false means the file is missing or unreadable
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
