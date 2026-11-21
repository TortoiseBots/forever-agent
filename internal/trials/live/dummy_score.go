package live

// dummy_score.go: the runner's seam into dummybench. A dummy repeat's trial
// verdicts come from the trial's own per-spell rules ({metric: dummy,
// spell, stat}), scored from the repeat's tallies — not from rules.Evaluate
// (a dummy spell is a table lookup and a mean, not an event filter). The
// per-repeat verdicts.json keeps the score package's row shape so
// report.WriteCard and the compare table read dummy trials unchanged; the
// per-trial assembly mirrors ScoreTrialE (majority of valid repeats, tie
// FAIL, INVALID/UNKNOWN never become PASS).

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/dummybench"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/rules"
)

// dummyTableDir is the directory of per-class expected tables, beside the
// embedded trial files: <table dir>/<class>-<spec>.json. Tests override it
// with a temp dir.
var dummyTableDir = ""

// dummyExpects loads the trial's expected table: situation.expects_file,
// or <table dir>/<class>-<spec>.json. A missing file is an error, never an
// empty PASS: the bench refuses to score a rep it cannot check.
func dummyExpects(trial map[string]any) ([]dummybench.Expect, error) {
	sit, _ := trial["situation"].(map[string]any)
	if name, _ := sit["expects_file"].(string); name != "" {
		return dummybench.LoadExpects(name)
	}
	class, _ := trial["class"].(string)
	spec, _ := trial["spec"].(string)
	if class == "" || spec == "" {
		return nil, fmt.Errorf("dummy trial needs class/spec or situation.expects_file")
	}
	dir := dummyTableDir
	if dir == "" {
		dir = filepath.Join("internal", "trials", "dummybench", "testdata")
	}
	name := strings.ToLower(class) + "-" + strings.ToLower(spec) + ".json"
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		return nil, fmt.Errorf("dummy trial %s/%s: no expected table %s: %w", class, spec, name, err)
	}
	return dummybench.LoadExpects(filepath.Join(dir, name))
}

// scoreDummyRep scores one dummy repeat dir with dummybench: it writes
// verdicts.json (the per-spell tally rows for the card) and returns the
// tallies for the trial assembly.
func scoreDummyRep(repDir string, trial map[string]any) (dummybench.RepTally, map[string]any, error) {
	expects, err := dummyExpects(trial)
	if err != nil {
		return dummybench.RepTally{}, nil, err
	}
	tally, out, err := dummybench.TallyRep(repDir, expects)
	if err != nil {
		return dummybench.RepTally{}, nil, err
	}
	outRules := make([]any, 0, len(out))
	for _, v := range out {
		outRules = append(outRules, dummyRepRow(v))
	}
	doc := map[string]any{"trial": pyjson.Str(trial["id"]), "rep": repBase(repDir),
		"ok_setup": true, "rules": outRules}
	if tally.Invalid != "" {
		doc["invalid_reason"] = tally.Invalid
	}
	if err := pyjson.Dump(filepath.Join(repDir, "verdicts.json"), doc); err != nil {
		return dummybench.RepTally{}, nil, err
	}
	return tally, doc, nil
}

// dummyRepRow is one per-spell row of a dummy verdicts.json.
func dummyRepRow(v dummybench.Verdict) map[string]any {
	ev := make([]any, 0, len(v.Evidence))
	for _, e := range v.Evidence {
		ev = append(ev, e)
	}
	row := map[string]any{"id": dummyRuleID(v.Spell), "spell": v.Spell,
		"verdict": v.Verdict, "value": dummyValue(v),
		"k": v.Casts, "n": v.Casts,
		"casts": v.Casts, "damage": v.Damage,
		"mean_damage":   dummyRound1(v.MeanDamage),
		"mean_reaction": dummyReact(v), "max_reaction": dummyMaxReact(v),
		"evidence": ev, "guard": ""}
	if v.NoNumberFor != "" {
		row["no_number_for"] = v.NoNumberFor
	}
	if v.Verdict == "INVALID" || v.Verdict == "UNKNOWN" {
		row["reason"] = dummyFirstEv(v)
	}
	return row
}

// dummyRuleID is the verdict row id for a spell row.
func dummyRuleID(spell string) string {
	return "DUMMY-" + strings.ToUpper(strings.ReplaceAll(spell, " ", "_"))
}

func dummyValue(v dummybench.Verdict) any {
	if v.Verdict == "PASS" || v.Verdict == "FAIL" {
		return dummybench.Round1(v.MeanDamage)
	}
	return nil
}

func dummyRound1(f float64) float64 { return dummybench.Round1(f) }

func dummyReact(v dummybench.Verdict) any {
	if !v.HasReact {
		return nil
	}
	return dummybench.Round2(v.MeanReact)
}

func dummyMaxReact(v dummybench.Verdict) any {
	if !v.HasReact {
		return nil
	}
	return dummybench.Round2(v.MaxReact)
}

func dummyFirstEv(v dummybench.Verdict) string {
	if len(v.Evidence) > 0 {
		return v.Evidence[0]
	}
	return v.Verdict
}

// dummyRepTally is one scored repeat: its dir base and its tallies.
type dummyRepTally struct {
	rep   string
	tally dummybench.RepTally
}

// scoreDummyTrial assembles the trial verdicts of one dummy trial dir from
// its repeats' tallies: one row per trial rule ({metric: dummy, spell,
// stat}), majority of valid repeats, tie FAIL. INVALID or SKIPPED never
// become PASS; UNKNOWN never becomes FAIL.
func scoreDummyTrial(trialDir string, trial map[string]any) (map[string]any, error) {
	expects, err := dummyExpects(trial)
	if err != nil {
		return nil, err
	}
	var reps []dummyRepTally
	for _, repDir := range repDirs(trialDir) {
		tally, _, err := dummybench.TallyRep(repDir, expects)
		if err != nil {
			return nil, err
		}
		reps = append(reps, dummyRepTally{rep: repBase(repDir), tally: tally})
	}
	rulesList, _ := pyjson.List(trial["rules"])
	var outRules []any
	for _, ruleVal := range rulesList {
		rule, ok := pyjson.Object(ruleVal)
		if !ok {
			continue
		}
		check, _ := pyjson.Object(rule["check"])
		spell := pyjson.Str(check["spell"])
		stat := pyjson.Str(check["stat"])
		rid := pyjson.Str(rule["id"])
		var detail []any
		var valid int64
		var pass int64
		var kSum, nSum int64
		var sum float64
		for _, rep := range reps {
			got, verdict, value, k, n, evidence := dummyRuleScore(rep.tally, spell, stat, rule)
			if verdict == "PASS" || verdict == "FAIL" {
				valid++
				kSum += k
				nSum += n
				sum += value
				if verdict == "PASS" {
					pass++
				}
			}
			detail = append(detail, map[string]any{
				"rep": rep.rep, "verdict": got, "value": valueOrNil(got, value),
				"k": k, "n": n, "evidence": evidence,
				"guard": "", "reason": reasonOf(got, evidence),
				"no_match": false, "invalid_reason": invalidOf(got, evidence),
			})
		}
		verdict := "INCONCLUSIVE"
		var value any
		switch {
		case valid == 0:
			verdict, value = dummyNoValid(reps, spell, stat)
		case pass > valid-pass:
			verdict = "PASS"
			value = sum / float64(valid)
		default:
			verdict = "FAIL"
			value = sum / float64(valid)
		}
		outRules = append(outRules, map[string]any{
			"id": rid, "verdict": verdict, "k": kSum, "n": nSum, "detail": detail,
			"expected": rule["expect_today"], "metric": "dummy",
			"pass_if": rule["pass_if"], "source": rule["source"],
			"desc":   rule["desc"],
			"reason": orEmpty(rule["reason"]),
			"value":  value,
		})
	}
	out := map[string]any{"trial": pyjson.Str(trial["id"]), "verdicts": outRules}
	if outRules == nil {
		out["verdicts"] = []any{}
	}
	if err := pyjson.Dump(filepath.Join(trialDir, "trial.json"), out); err != nil {
		return nil, fmt.Errorf("write %s: %w", filepath.Join(trialDir, "trial.json"), err)
	}
	return out, nil
}

// dummyRuleScore scores one (repeat, rule): the rule's spell and stat
// against the repeat's tallies. A stranger spell is INVALID; a missing
// number is UNKNOWN; else the stat (mean damage, casts, mean reaction)
// goes through rules.Pass.
func dummyRuleScore(tally dummybench.RepTally, spell, stat string, rule map[string]any) (string, string, float64, int64, int64, []any) {
	if tally.Invalid != "" && len(tally.Tallies) == 0 {
		ev := []any{tally.Invalid}
		return "INVALID", "INVALID", 0, 0, 0, ev
	}
	for _, t := range tally.Tallies {
		if t.Spell != spell {
			continue
		}
		e, ok := tally.Expects[spell]
		if !ok || e == nil {
			ev := []any{fmt.Sprintf("%s: not in the expected table (stranger)", spell)}
			return "INVALID", "INVALID", 0, t.Casts, t.Casts, ev
		}
		var value float64
		switch stat {
		case "casts":
			value = float64(t.Casts)
		case "reaction":
			if !t.HasReact {
				ev := []any{fmt.Sprintf("%s: no reaction measured in the scored window", spell)}
				return "UNKNOWN", "UNKNOWN", 0, t.Casts, t.Casts, ev
			}
			value = dummybench.Round2(t.MeanRT)
		default: // damage
			value = dummybench.Round1(t.MeanDmg)
		}
		if missing := dummyMissing(e, stat, t); missing != "" {
			ev := []any{fmt.Sprintf("%s: casts=%d damage=%d mean=%.1f, no expected %s",
				spell, t.Casts, t.Damage, t.MeanDmg, missing)}
			return "UNKNOWN", "UNKNOWN", 0, t.Casts, t.Casts, ev
		}
		verdict := "FAIL"
		if rules.Pass(rule["pass_if"], value) {
			verdict = "PASS"
		}
		var ev []any
		switch stat {
		case "casts":
			ev = []any{fmt.Sprintf("%s: casts=%d", spell, t.Casts)}
		case "reaction":
			ev = []any{fmt.Sprintf("%s: mean reaction %.2fs max %.2fs", spell, t.MeanRT, t.MaxRT)}
		default:
			ev = []any{fmt.Sprintf("%s: casts=%d damage=%d mean=%.1f", spell, t.Casts, t.Damage, t.MeanDmg)}
		}
		return verdict, verdict, value, t.Casts, t.Casts, ev
	}
	if e, ok := tally.Expects[spell]; ok && e != nil {
		ev := []any{fmt.Sprintf("%s: no casts in the scored window", spell)}
		return "UNKNOWN", "UNKNOWN", 0, 0, 0, ev
	}
	ev := []any{fmt.Sprintf("%s: not in the expected table (stranger)", spell)}
	return "INVALID", "INVALID", 0, 0, 0, ev
}

// dummyMissing names the expected bound the tally needs but the table does
// not know: min_damage for damage/casts, max_reaction_s for reaction.
func dummyMissing(e *dummybench.Expect, stat string, t dummybench.SpellTally) string {
	_ = t
	switch stat {
	case "reaction":
		if e.MaxReactionS == nil {
			return "max_reaction_s"
		}
	default:
		if e.MinDamage == nil {
			return "min_damage"
		}
	}
	return ""
}

// dummyNoValid is the trial verdict with no valid repeat: INVALID when a
// repeat was INVALID and none scored, else INCONCLUSIVE (mirrors the score
// package's zero-valid rule).
func dummyNoValid(reps []dummyRepTally, spell, stat string) (string, any) {
	_ = stat
	anyInvalid := false
	for _, rep := range reps {
		for _, t := range rep.tally.Tallies {
			if t.Spell != spell {
				continue
			}
		}
		if rep.tally.Invalid != "" {
			anyInvalid = true
		}
	}
	if anyInvalid {
		return "INVALID", nil
	}
	return "INCONCLUSIVE", nil
}

func valueOrNil(verdict string, value float64) any {
	if verdict == "PASS" || verdict == "FAIL" {
		return value
	}
	return nil
}

func reasonOf(verdict string, evidence []any) string {
	if verdict == "INCONCLUSIVE" || verdict == "INVALID" || verdict == "UNKNOWN" {
		if len(evidence) > 0 {
			return pyjson.Str(evidence[0])
		}
		return verdict
	}
	return ""
}

func invalidOf(verdict string, evidence []any) string {
	if verdict == "INVALID" && len(evidence) > 0 {
		return pyjson.Str(evidence[0])
	}
	return ""
}

func orEmpty(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// repDirs lists a trial dir's rep* dirs in run order.
func repDirs(trialDir string) []string {
	entries, err := os.ReadDir(trialDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "rep") {
			continue
		}
		out = append(out, filepath.Join(trialDir, entry.Name()))
	}
	sort.Strings(out)
	return out
}

func repBase(path string) string {
	base := filepath.Base(strings.TrimRight(path, "/"))
	if base == "" {
		return path
	}
	return base
}
