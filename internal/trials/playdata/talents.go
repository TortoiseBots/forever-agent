package playdata

import (
	"fmt"
	"strings"

	"github.com/pjerra/forever-agent/data/aotc"
	"github.com/pjerra/forever-agent/internal/kbspells"
)

// talentExpectation is one should-have-cast spell the player tables add for a spec's talent build.
type talentExpectation struct {
	Talent     aotc.TalentPick
	Spell      string
	CooldownMS int64
}

// talentRow judges the should-have-cast spells the bot's talent build implies (rule
// AOTC-TALENTS-IMPLY-SPELLS): the talents at or above MinTalentShare that grant an active spell, the
// bot has, and whose cooldown fits one of its combat windows, join the spell judge's expectation.
// It fails only a talent spell the record proves trained and never cast, and never one the spec's
// own spell lists call situational (that one is reported as held, not judged); a record with no
// talent data, with only a partial talent read-back (the runner learns the spec's marker kit, not
// the build), or with no proven combat reports what it has and judges nothing it cannot prove.
func talentRow(rec Record, in Inputs) Row {
	row := Row{ID: RuleTalents, Verdict: VerdictInconclusive}
	class, spec := strings.ToLower(strings.TrimSpace(rec.Class)), SpecName(rec.Spec)
	if in.Tables == nil {
		row.Reason = "no player data tables"
		return row
	}
	if !in.Tables.SpecKnown(class, spec) {
		row.Reason = fmt.Sprintf("no player data for %s/%s (spec-population.csv)", class, spec)
		return row
	}
	group := LevelGroup(rec.Level)
	if group == "" {
		row.Reason = fmt.Sprintf("level %d has no talent points", rec.Level)
		return row
	}
	picks := in.Tables.CommonTalents(class, spec, group, MinTalentShare)
	if len(picks) == 0 {
		row.Reason = fmt.Sprintf("no talent picks at or above %.0f%% for %s/%s at %s", MinTalentShare, class, spec, group)
		return row
	}
	if in.Spells == nil {
		row.Reason = "no Forever spell table in this build"
		return row
	}
	expectations := talentExpectations(picks, class, in)
	if len(expectations) == 0 {
		row.Reason = fmt.Sprintf("none of the %d common talents grants an active spell", len(picks))
		return row
	}
	// Combat, not the repeat's scored time: setup, waiting and travel are no fight, so a long
	// repeat with a short fight must not fail a spell the bot had no chance to cast.
	if !rec.ActiveKnown || len(rec.Combat) == 0 {
		row.Evidence = sortedEvidence(expectationLines(expectations, spec))
		row.Reason = "no combat participation in the record: the bot's samples never flag combat"
		return row
	}
	// Eligibility is per combat window: a spell on cooldown for longer than a window cannot have
	// been cast in it, and a window shorter than TalentMinActiveS says nothing.
	var fitting []talentExpectation
	var skipped []string
	var longest float64
	for _, window := range rec.Combat {
		if length := window.To - window.From; length > longest {
			longest = length
		}
	}
	for _, e := range expectations {
		fits := false
		for _, window := range rec.Combat {
			length := window.To - window.From
			if length < TalentMinActiveS {
				continue
			}
			// A spell on cooldown for longer than the window divided by TalentCooldownFactor is
			// not expected in it: a 3 min spell needs a 6 min fight, a 180 s spell no less.
			if e.CooldownMS <= 0 || float64(e.CooldownMS) <= length*1000/TalentCooldownFactor {
				fits = true
				break
			}
		}
		if fits {
			fitting = append(fitting, e)
			continue
		}
		if e.CooldownMS > 0 {
			skipped = append(skipped, fmt.Sprintf("%s: cooldown %d s, longer than every combat window the rule judges (longest %.0f s)",
				e.Spell, e.CooldownMS/1000, longest))
			continue
		}
		skipped = append(skipped, fmt.Sprintf("%s: no combat window reached the %.0f s the rule judges", e.Spell, TalentMinActiveS))
	}
	if len(fitting) == 0 {
		row.Evidence = sortedEvidence(skipped)
		row.Reason = fmt.Sprintf("none of the %d common talents' spells fits a %.0f s combat window", len(picks), longest)
		return row
	}
	if !rec.TalentsKnown {
		row.Evidence = sortedEvidence(expectationLines(fitting, spec))
		row.Reason = "no talent data in the record: the expectations are reported, nothing is judged"
		return row
	}
	learned := map[int64]bool{}
	for _, id := range rec.Talents {
		learned[id] = true
	}
	var expected, untrained, unproven []string
	var judged []talentExpectation
	for _, e := range fitting {
		if learned[e.Talent.SpellID] {
			judged = append(judged, e)
			expected = append(expected, e.Spell)
			continue
		}
		if rec.TalentsComplete {
			untrained = append(untrained, fmt.Sprintf("%s: not trained, so not expected", e.Spell))
			continue
		}
		// The marker kit's read-back proves what it learned, never what the bot did not: an
		// absent talent may be trained outside the kit.
		unproven = append(unproven, fmt.Sprintf("%s (%s, %.1f%% of %s): not in the recorded talent learning, so its pick is unproven (the read-back is the marker kit, not the build)",
			e.Spell, e.Talent.Talent, e.Talent.PickPct, spec))
	}
	if len(judged) == 0 {
		if len(unproven) > 0 {
			row.Evidence = sortedEvidence(append(unproven, skipped...))
			row.Reason = fmt.Sprintf("talent data is partial: none of the %d talent spells is proven trained", len(fitting))
			return row
		}
		row.Verdict = VerdictPass
		row.Evidence = sortedEvidence(append(untrained, skipped...))
		row.Reason = "no trained talent spell is expected in the recorded combat"
		return row
	}
	if in.Oracle == nil {
		row.Evidence = sortedEvidence(append(append(unproven, untrained...), skipped...))
		row.Reason = "no spell oracle in this build"
		return row
	}
	oracleSpec := OracleSpec(class, spec, rec.Role)
	finding := in.Oracle.Judge(kbspells.BotCasts{
		Name: rec.Bot, Class: class, Role: rec.Role, Spec: oracleSpec, Level: rec.Level,
		Casts: rec.Casts, ActiveS: rec.ActiveS, Expected: expected,
	}, TalentMinActiveS)
	if finding.Level == "na" && len(finding.Missing) == 0 {
		row.Reason = "the spell judge could not read this spec: " + finding.Text
		return row
	}
	missing := map[string]bool{}
	for _, name := range finding.Missing {
		missing[strings.ToLower(name)] = true
	}
	held := map[string]bool{}
	for _, name := range finding.Held {
		held[strings.ToLower(strings.TrimSpace(name))] = true
	}
	var lines []string
	var never, heldCount int
	for _, e := range judged {
		key := strings.ToLower(e.Spell)
		switch {
		case held[key]:
			// The spec's spell lists call the spell situational: the bot may rightly hold it, so
			// the rule reports it and never fails it.
			heldCount++
			lines = append(lines, fmt.Sprintf("%s (%s, %.1f%% of %s): situational in the spell lists, reported, not judged",
				e.Spell, e.Talent.Talent, e.Talent.PickPct, spec))
		case missing[key]:
			never++
			lines = append(lines, fmt.Sprintf("%s (%s, %.1f%% of %s): never cast in %.0f s of combat", e.Spell, e.Talent.Talent, e.Talent.PickPct, spec, rec.ActiveS))
		default:
			lines = append(lines, fmt.Sprintf("%s (%s): cast", e.Spell, e.Talent.Talent))
		}
	}
	lines = append(lines, unproven...)
	lines = append(lines, untrained...)
	lines = append(lines, skipped...)
	row.Evidence = sortedEvidence(lines)
	row.N = int64(len(judged) - heldCount)
	row.K = int64(len(judged) - heldCount - never)
	if never > 0 {
		row.Verdict = VerdictFail
		row.Reason = fmt.Sprintf("%d of %d trained talent spells were never cast", never, len(judged)-heldCount)
		return row
	}
	if row.N == 0 {
		row.Reason = fmt.Sprintf("the %d trained talent spells are situational in the spell lists: reported, not judged", heldCount)
		return row
	}
	if len(unproven) > 0 {
		row.Reason = fmt.Sprintf("the %d trained talent spells were cast; %d common talent spells are unproven with only the marker-kit read-back", len(judged)-heldCount, len(unproven))
		return row
	}
	row.Verdict = VerdictPass
	row.Reason = fmt.Sprintf("all %d trained talent spells were cast", len(judged)-heldCount)
	return row
}

// expectationLines names the should-have-cast spells of a talent build, for a row that reports the
// expectations without judging them.
func expectationLines(expectations []talentExpectation, spec string) []string {
	lines := make([]string, 0, len(expectations))
	for _, e := range expectations {
		lines = append(lines, fmt.Sprintf("%s (%s, %.1f%% of %s)", e.Spell, e.Talent.Talent, e.Talent.PickPct, spec))
	}
	return lines
}

// talentExpectations is the common picks that grant an active spell of the bot's own class: the
// talent's spell exists in the Forever spell table, is not passive, and names this class (or none).
func talentExpectations(picks []aotc.TalentPick, class string, in Inputs) []talentExpectation {
	var out []talentExpectation
	for _, pick := range picks {
		if pick.SpellID <= 0 {
			continue
		}
		spell, ok := in.Spells.Lookup(uint32(pick.SpellID))
		if !ok || spell.Passive() || strings.TrimSpace(spell.Name) == "" {
			continue
		}
		if spell.Class != "" && !strings.EqualFold(spell.Class, class) {
			continue
		}
		cooldown := int64(spell.RecoveryMS)
		if int64(spell.CategoryRecoveryMS) > cooldown {
			cooldown = int64(spell.CategoryRecoveryMS)
		}
		out = append(out, talentExpectation{Talent: pick, Spell: spell.Name, CooldownMS: cooldown})
	}
	return out
}

// OracleSpec maps a bench spec to the spell oracle's spec word for a role (the oracle splits a druid's
// feral into feral_bear and feral_cat, and a warrior tank's dps build into fury_tank). "" when the
// oracle cannot name it.
func OracleSpec(class, spec, role string) string {
	base := strings.ReplaceAll(SpecName(spec), "-", "_")
	switch {
	case class == "druid" && base == "feral":
		if strings.EqualFold(strings.TrimSpace(role), "tank") {
			return "feral_bear"
		}
		return "feral_cat"
	case class == "warrior" && base == "fury" && strings.EqualFold(strings.TrimSpace(role), "tank"):
		return "fury_tank"
	}
	return base
}
