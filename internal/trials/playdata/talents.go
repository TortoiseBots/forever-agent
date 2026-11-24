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
// bot has, and whose cooldown fits the fight join the spell judge's expectation. It fails only a
// trained talent spell never cast; a record with no talent data reports the expectations and judges
// nothing.
func talentRow(rec Record, in Inputs) Row {
	row := Row{ID: RuleTalents, Verdict: VerdictInconclusive}
	class, spec := strings.ToLower(strings.TrimSpace(rec.Class)), SpecName(rec.Spec)
	if in.Tables == nil {
		row.Reason = "no player data tables"
		return row
	}
	if !in.Tables.SpecKnown(class, spec) {
		row.Reason = fmt.Sprintf("no player data for %s/%s (spec-population.csv)", class, rec.Spec)
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
	// The cooldown guard comes first: a spell on cooldown for longer than the fight could not have
	// been cast in it, so it is no expectation with or without talent data.
	var fitting []talentExpectation
	var skipped []string
	for _, e := range expectations {
		if e.CooldownMS > 0 && float64(e.CooldownMS) > rec.ActiveS*1000*TalentCooldownFactor {
			skipped = append(skipped, fmt.Sprintf("%s: cooldown %d s, longer than the %.0f s fight", e.Spell, e.CooldownMS/1000, rec.ActiveS))
			continue
		}
		fitting = append(fitting, e)
	}
	if len(fitting) == 0 {
		row.Reason = fmt.Sprintf("none of the %d common talents' spells fits a %.0f s fight", len(picks), rec.ActiveS)
		return row
	}
	if !rec.TalentsKnown {
		lines := make([]string, 0, len(fitting))
		for _, e := range fitting {
			lines = append(lines, fmt.Sprintf("%s (%s, %.1f%% of %s)", e.Spell, e.Talent.Talent, e.Talent.PickPct, spec))
		}
		row.Evidence = sortedEvidence(lines)
		row.Reason = "no talent data in the record: the expectations are reported, nothing is judged"
		return row
	}
	learned := map[int64]bool{}
	for _, id := range rec.Talents {
		learned[id] = true
	}
	var expected, untrained []string
	var judged []talentExpectation
	for _, e := range fitting {
		if !learned[e.Talent.SpellID] {
			untrained = append(untrained, fmt.Sprintf("%s: not trained, so not expected", e.Spell))
			continue
		}
		judged = append(judged, e)
		expected = append(expected, e.Spell)
	}
	if len(judged) == 0 {
		row.Verdict = VerdictPass
		row.Evidence = sortedEvidence(append(untrained, skipped...))
		row.Reason = fmt.Sprintf("no trained talent spell is expected in a %.0f s fight", rec.ActiveS)
		return row
	}
	if rec.ActiveS < TalentMinActiveS {
		row.Evidence = sortedEvidence(append(untrained, skipped...))
		row.Reason = fmt.Sprintf("%.0f s active is below the %.0f s the rule judges", rec.ActiveS, TalentMinActiveS)
		return row
	}
	if in.Oracle == nil {
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
	var lines []string
	var never int
	for _, e := range judged {
		if missing[strings.ToLower(e.Spell)] {
			never++
			lines = append(lines, fmt.Sprintf("%s (%s, %.1f%% of %s): never cast in %.0f s", e.Spell, e.Talent.Talent, e.Talent.PickPct, spec, rec.ActiveS))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s (%s): cast", e.Spell, e.Talent.Talent))
	}
	lines = append(lines, untrained...)
	lines = append(lines, skipped...)
	row.Evidence = sortedEvidence(lines)
	row.N = int64(len(judged))
	row.K = int64(len(judged) - never)
	if never > 0 {
		row.Verdict = VerdictFail
		row.Reason = fmt.Sprintf("%d of %d trained talent spells were never cast", never, len(judged))
		return row
	}
	row.Verdict = VerdictPass
	row.Reason = fmt.Sprintf("all %d trained talent spells were cast", len(judged))
	return row
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
