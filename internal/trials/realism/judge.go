package realism

// The realism checks' shared shape: one check per reading, each made of the parts it judges, each
// part with its own verdict so a PASS is never a verdict over missing data.
//
// The checks read one bot snapshot (see snapshot.go) and the aggregate player data (data/aotc):
//
//	REALISM-TALENTS    the bot's talent points against its level group's common picks, and at 60 its
//	                   point split against the spec's common splits.
//	REALISM-GEAR       the worn gear's item levels, quality mix and item sources against what a
//	                   player of that level could have.
//	REALISM-STATS      the worn gear against the spec's stat priority and its players' items, through
//	                   the player-data gear rule (AOTC-GEAR-FITS-SPEC) — the same judgment, not a copy.
//	REALISM-ENCHANTS   at 60, the enchants the spec's slots carry, and nothing on the slots its
//	                   players leave empty.
//
// A part is judged only from data the snapshot carries: an empty field leaves the part
// INCONCLUSIVE with the missing read-out named, and a check with no judged part is INCONCLUSIVE.
// The check's verdict is FAIL when any judged part fails, PASS when at least one part was judged and
// none failed, INCONCLUSIVE otherwise.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/data/aotc"
	"github.com/pjerra/forever-agent/internal/trials/playdata"
)

// The check ids, as they appear in the report.
const (
	RuleTalents  = "REALISM-TALENTS"
	RuleGear     = "REALISM-GEAR"
	RuleStats    = "REALISM-STATS"
	RuleEnchants = "REALISM-ENCHANTS"
)

// The rule ids this package quotes from the player data: the gear rule's own id, so the stat check
// names the judgment it reuses.
const ruleGearFitsSpec = playdata.RuleGear

// Verdicts are the words a Check carries.
const (
	VerdictPass         = "PASS"
	VerdictFail         = "FAIL"
	VerdictInconclusive = "INCONCLUSIVE"
)

// Tables is the player-data view the checks read; *aotc.Data is the shipped one. The gear and
// priority readings go through the player-data package's own judgment, so its interface is
// embedded here rather than restated.
type Tables interface {
	playdata.Tables
	SplitsFor(class, spec string) []aotc.TalentSplit
	EnchantsFor(class, spec, slot string) []aotc.Enchant
}

// Part is one reading a check makes: its own verdict, the number it measured and the reason it
// could not be judged. Decisive marks a part whose reading the check needs: when one is not judged
// the check is INCONCLUSIVE, never a PASS over the parts that were read. Skipped marks a part that
// does not apply to this bot (no slot in the enchant dead zone, say): it is neither judged nor
// counted as missing.
type Part struct {
	Name     string   `json:"name"`
	Verdict  string   `json:"verdict"`
	Value    float64  `json:"value,omitempty"`
	Measure  string   `json:"measure,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Lines    []string `json:"lines,omitempty"`
	Decisive bool     `json:"-"`
	Skipped  bool     `json:"-"`
}

// Check is one check's reading for one bot: the parts it judges, the number it headlines, and the
// evidence a reader can check.
type Check struct {
	ID       string   `json:"id"`
	Verdict  string   `json:"verdict"`
	Value    float64  `json:"value,omitempty"`
	Measure  string   `json:"measure,omitempty"`
	Parts    []Part   `json:"parts,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// Judge reads one bot against the tables: every check, in a stable order.
func Judge(b Bot, t Tables) []Check {
	return []Check{
		talentCheck(b, t),
		gearCheck(b, t),
		statsCheck(b, t),
		enchantsCheck(b, t),
	}
}

// verdictOf sums a check's parts: FAIL when any judged part fails; INCONCLUSIVE when a decisive
// part was not judged (a PASS over a partial reading would hide the missing data); PASS when every
// judged part passes and no decisive part is missing; INCONCLUSIVE otherwise. Skipped parts do not
// apply to this bot and are not counted as missing. The reason names the parts that were not judged.
func verdictOf(parts []Part) (string, string) {
	var failed, judged, decisiveMissing int
	var missing []string
	for _, p := range parts {
		if p.Skipped {
			continue
		}
		switch p.Verdict {
		case VerdictFail:
			failed++
			judged++
		case VerdictPass:
			judged++
		default:
			if p.Decisive {
				decisiveMissing++
			}
			if p.Reason != "" {
				missing = append(missing, fmt.Sprintf("%s: %s", p.Name, p.Reason))
			} else {
				missing = append(missing, p.Name+": not judged")
			}
		}
	}
	sort.Strings(missing)
	switch {
	case failed > 0 && len(missing) > 0:
		return VerdictFail, fmt.Sprintf("%d of %d judged parts fail; not judged: %s", failed, judged, strings.Join(missing, "; "))
	case failed > 0:
		return VerdictFail, fmt.Sprintf("%d of %d judged parts fail", failed, judged)
	case decisiveMissing > 0:
		return VerdictInconclusive, "a decisive part was not judged: " + strings.Join(missing, "; ")
	case judged > 0:
		if len(missing) > 0 {
			return VerdictPass, "judged parts pass; not judged: " + strings.Join(missing, "; ")
		}
		return VerdictPass, fmt.Sprintf("all %d parts pass", judged)
	default:
		if len(missing) == 0 {
			missing = []string{"nothing to judge"}
		}
		return VerdictInconclusive, strings.Join(missing, "; ")
	}
}

// part makes one part, with the measure text its value prints as.
func part(name, verdict string, value float64, measure, reason string) Part {
	return Part{Name: name, Verdict: verdict, Value: value, Measure: measure, Reason: reason}
}

// checkOf assembles a check from its parts and evidence.
func checkOf(id string, parts []Part, evidence []string) Check {
	verdict, reason := verdictOf(parts)
	c := Check{ID: id, Verdict: verdict, Parts: parts, Reason: reason, Evidence: sortedLines(evidence)}
	// The headline value is the first judged numeric part: the check's own number per bot.
	for _, p := range parts {
		if p.Verdict != VerdictInconclusive && p.Measure != "" {
			c.Value, c.Measure = p.Value, p.Measure
			break
		}
	}
	return c
}

// sortedLines keeps evidence stable whatever map order found it.
func sortedLines(lines []string) []string {
	out := append([]string(nil), lines...)
	sort.Strings(out)
	return out
}

// verdictFail returns VerdictFail when the measured value breaks its bound, else VerdictPass.
func verdictAtMost(v, most float64) string {
	if v > most {
		return VerdictFail
	}
	return VerdictPass
}

// verdictAtLeast returns VerdictFail when the measured value is under its floor, else VerdictPass.
func verdictAtLeast(v, least float64) string {
	if v < least {
		return VerdictFail
	}
	return VerdictPass
}
