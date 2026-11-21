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
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/dummybench"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// dummyTableDir overrides the bench's expected-table data root: the
// directory the default table name resolves in and the tree an
// expects_file may not escape. Empty (the shipped default) resolves against
// the trial file itself: the table is the trial's sibling on disk, so a
// binary run from anywhere finds it. Tests set it to a temp dir.
var dummyTableDir = ""

// dummyExpects loads the trial's expected table: situation.expects_file, or
// <root>/dummy_<class>_<spec>.json. The file must resolve inside the root
// (no absolute path, no ".." escape), and a missing file is an error, never
// an empty PASS: the bench refuses to score a rep it cannot check. The
// trial's declared rotation is checked against the table here: the bot's
// module still picks the order, but every spell the trial says the fight
// casts must be a table row, so a typo fails at load instead of turning
// every repeat INVALID.
func dummyExpects(trial map[string]any, trialArg string) ([]dummybench.Expect, error) {
	sit, _ := trial["situation"].(map[string]any)
	name, _ := sit["expects_file"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		class, _ := trial["class"].(string)
		spec, _ := trial["spec"].(string)
		if class == "" || spec == "" {
			return nil, fmt.Errorf("dummy trial needs class/spec or situation.expects_file")
		}
		name = "dummy_" + strings.ToLower(class) + "_" + strings.ToLower(spec) + ".json"
	}
	rel, err := dummyTableName(name)
	if err != nil {
		return nil, err
	}
	var path string
	switch {
	case dummyTableDir != "":
		path = filepath.Join(dummyTableDir, rel)
	case strings.HasSuffix(trialArg, ".json"):
		// The table is the trial file's sibling: no working-directory
		// assumption, wherever the binary runs.
		path = filepath.Join(filepath.Dir(trialArg), rel)
	default:
		return nil, fmt.Errorf("dummy trial %q: set situation.expects_file or pass the trial file from disk", trialArg)
	}
	expects, err := dummybench.LoadExpects(path)
	if err != nil {
		return nil, err
	}
	if why := dummyRotationCovered(trial, expects); why != "" {
		return nil, fmt.Errorf("dummy trial %s: %s", pyjson.Str(trial["id"]), why)
	}
	return expects, nil
}

// dummyRotationCovered checks the trial's declared rotation against the
// table: every spell it names must have a row (case-folded). "" when every
// spell is covered.
func dummyRotationCovered(trial map[string]any, expects []dummybench.Expect) string {
	sit, _ := trial["situation"].(map[string]any)
	list, _ := pyjson.List(sit["rotation"])
	if len(list) == 0 {
		return ""
	}
	known := map[string]bool{}
	for i := range expects {
		known[strings.ToLower(expects[i].Spell)] = true
	}
	for _, item := range list {
		name := strings.TrimSpace(pyjson.Str(item))
		if name == "" {
			continue
		}
		if !known[strings.ToLower(name)] {
			return fmt.Sprintf("rotation names %q, which the expected table does not: every repeat would be INVALID", name)
		}
	}
	return ""
}

// dummyTableName cleans an expects_file into a path relative to the data
// root, refusing an absolute path, a volume name or any ".." escape.
func dummyTableName(name string) (string, error) {
	bad := fmt.Errorf("expects_file %q must stay inside the expected-table directory", name)
	if name == "" || strings.Contains(name, `\`) || strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return "", bad
	}
	rel := path.Clean(name)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", bad
	}
	return filepath.FromSlash(rel), nil
}

// scoreDummyRep scores one dummy repeat dir with dummybench: it writes
// verdicts.json (the per-spell tally rows for the card) and returns the
// tallies for the trial assembly.
func scoreDummyRep(repDir string, trial map[string]any, trialArg string) (dummybench.RepTally, map[string]any, error) {
	expects, err := dummyExpects(trial, trialArg)
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
func scoreDummyTrial(trialDir, trialArg string, trial map[string]any) (map[string]any, error) {
	expects, err := dummyExpects(trial, trialArg)
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
		firstReason := ""
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
			reason := reasonOf(got, evidence)
			if firstReason == "" {
				firstReason = reason
			}
			detail = append(detail, map[string]any{
				"rep": rep.rep, "verdict": got, "value": valueOrNil(got, value),
				"k": k, "n": n, "evidence": evidence,
				"guard": "", "reason": reason,
				"no_match": false, "invalid_reason": invalidOf(got, evidence),
			})
		}
		verdict := "INCONCLUSIVE"
		var value any
		switch {
		case valid == 0:
			verdict, value = dummyNoValid(reps)
		case pass > valid-pass:
			verdict = "PASS"
			value = sum / float64(valid)
		default:
			verdict = "FAIL"
			value = sum / float64(valid)
		}
		// A card row says why it is not PASS/FAIL: the repeat's reason
		// (the stranger, the missing bound), else the rule's own note.
		reason := orEmpty(rule["reason"])
		if verdict != "PASS" && verdict != "FAIL" && firstReason != "" {
			reason = firstReason
		}
		outRules = append(outRules, map[string]any{
			"id": rid, "verdict": verdict, "k": kSum, "n": nSum, "detail": detail,
			"expected": rule["expect_today"], "metric": "dummy",
			"pass_if": rule["pass_if"], "source": rule["source"],
			"desc":   rule["desc"],
			"reason": reason,
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
// against the repeat's tallies, with the EXPECTED TABLE deciding. A mean
// damage below the table's min_damage is FAIL, a mean reaction above its
// max_reaction_s is FAIL, both met is PASS; the rule's own pass_if is
// carried for the card and never overrides the table. An INVALID repeat is
// INVALID for every rule (no spell in it scores PASS or FAIL). A missing
// bound, a known reaction bound with no measured reaction, a stat the table
// has no column for, and a spell with no table row and no casts are
// UNKNOWN; a cast spell the table does not name is INVALID.
func dummyRuleScore(tally dummybench.RepTally, spell, stat string, rule map[string]any) (string, string, float64, int64, int64, []any) {
	_ = rule // pass_if no longer decides: the table's bounds do
	if tally.Invalid != "" {
		ev := []any{tally.Invalid}
		return "INVALID", "INVALID", 0, 0, 0, ev
	}
	e := tally.Expect(spell)
	for _, t := range tally.Tallies {
		if !strings.EqualFold(t.Spell, spell) {
			continue
		}
		if e == nil {
			ev := []any{fmt.Sprintf("%s: not in the expected table (stranger)", spell)}
			return "INVALID", "INVALID", 0, t.Casts, t.Casts, ev
		}
		var value float64
		switch stat {
		case "reaction":
			if e.MaxReactionS == nil {
				ev := []any{fmt.Sprintf("%s: casts=%d, no expected max_reaction_s", spell, t.Casts)}
				return "UNKNOWN", "UNKNOWN", 0, t.Casts, t.Casts, ev
			}
			if !t.HasReact {
				ev := []any{fmt.Sprintf("%s: no reaction measured in the scored window (max %.2fs known)",
					spell, *e.MaxReactionS)}
				return "UNKNOWN", "UNKNOWN", 0, t.Casts, t.Casts, ev
			}
			value = dummybench.Round2(t.MeanRT)
			verdict := "FAIL"
			if t.MeanRT <= *e.MaxReactionS {
				verdict = "PASS"
			}
			ev := []any{fmt.Sprintf("%s: mean reaction %.2fs max %.2fs (expected <= %.2fs)",
				spell, t.MeanRT, t.MaxRT, *e.MaxReactionS)}
			return verdict, verdict, value, t.Casts, t.Casts, ev
		case "damage", "":
			if e.MinDamage == nil {
				ev := []any{fmt.Sprintf("%s: casts=%d damage=%d mean=%.1f, no expected min_damage",
					spell, t.Casts, t.Damage, t.MeanDmg)}
				return "UNKNOWN", "UNKNOWN", 0, t.Casts, t.Casts, ev
			}
			value = dummybench.Round1(t.MeanDmg)
			verdict := "FAIL"
			if t.MeanDmg >= *e.MinDamage {
				verdict = "PASS"
			}
			ev := []any{fmt.Sprintf("%s: casts=%d damage=%d mean=%.1f (expected >= %.1f)",
				spell, t.Casts, t.Damage, t.MeanDmg, *e.MinDamage)}
			return verdict, verdict, value, t.Casts, t.Casts, ev
		default:
			ev := []any{fmt.Sprintf("%s: stat %q has no column in the expected table", spell, stat)}
			return "UNKNOWN", "UNKNOWN", 0, t.Casts, t.Casts, ev
		}
	}
	if e != nil {
		ev := []any{fmt.Sprintf("%s: no casts in the scored window", spell)}
		return "UNKNOWN", "UNKNOWN", 0, 0, 0, ev
	}
	ev := []any{fmt.Sprintf("%s: no table row and no casts (ignored)", spell)}
	return "UNKNOWN", "UNKNOWN", 0, 0, 0, ev
}

// dummyNoValid is the trial verdict with no valid repeat: INVALID when a
// repeat was INVALID and none scored, else INCONCLUSIVE (mirrors the score
// package's zero-valid rule).
func dummyNoValid(reps []dummyRepTally) (string, any) {
	for _, rep := range reps {
		if rep.tally.Invalid != "" {
			return "INVALID", nil
		}
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
