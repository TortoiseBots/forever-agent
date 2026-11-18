// Package score gives verdicts per rule/repeat/trial, and per-run scoring.
//
// ScoreRep(repDir, trial): setup.json ok false -> every rule INVALID with invalid_reason;
// scored time < min_valid_s -> INVALID "cut short (<n> s, ended: <reason>)"; else each rule
// -> PASS/FAIL/INCONCLUSIVE. ScoreTrial(trialDir, trial): majority of valid non-INCONCLUSIVE
// repeats, tie -> FAIL; zero -> INCONCLUSIVE or INVALID (INVALID wins when any repeat was
// INVALID and none scored). ScoreRun(runDir) scores all trials, writes card.md and card.json
// in the run dir.
package score

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/report"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

// DefaultThresholds are used only when config_read FAILED.
var DefaultThresholds = map[string]float64{"critical": 20, "low": 50, "medium": 70}

// Trial is one trial definition.
type Trial = scenario.Trial

func readJSONL(path string) ([]rules.Row, error) { return events.ReadJSONL(path) }

func readJSON(path string) any {
	doc, err := pyjson.UnmarshalFile(path)
	if err != nil {
		return nil
	}
	return doc
}

func objectOrEmpty(doc any) map[string]any { return pyjson.ObjectOrEmpty(doc) }

func numOrNil(value any) *float64 {
	if value == nil || pyjson.IsBool(value) {
		return nil
	}
	if f, ok := pyjson.Float(value); ok {
		return &f
	}
	return nil
}

// configReadFailed is true only when the run proves config_read FAILED (a check with ok false).
func configReadFailed(setup map[string]any) bool {
	checks, _ := pyjson.List(setup["checks"])
	for _, checkVal := range checks {
		check, ok := pyjson.Object(checkVal)
		if !ok {
			continue
		}
		if pyjson.Str(check["id"]) == "config_read" && !pyjson.Truthy(check["ok"]) {
			return true
		}
	}
	return false
}

// thresholds reads setup.json config first, then RUN.json, then the defaults 20/50/70 only
// when config_read FAILED; otherwise unknown (nil).
func thresholds(runDir string, setup map[string]any) (map[string]any, bool) {
	if setup == nil {
		setup = map[string]any{}
	}
	cfg := map[string]any{}
	if obj, ok := pyjson.Object(setup["config"]); ok {
		cfg = obj
	}
	run := objectOrEmpty(readJSON(filepath.Join(runDir, "RUN.json")))
	failed := configReadFailed(setup)
	out := map[string]any{}
	assumed := false
	for _, name := range []string{"critical", "low", "medium"} {
		value := numOrNil(cfg[name])
		if value == nil {
			value = numOrNil(run[name+"_health"])
		}
		if value == nil && failed {
			v := DefaultThresholds[name]
			value = &v
			assumed = true
		}
		if value == nil {
			out[name] = nil
		} else {
			out[name] = *value
		}
	}
	return out, assumed
}

// mergeSpans merges overlapping/touching (a, b) spans.
func mergeSpans(excluded [][2]float64) [][2]float64 {
	spans := make([][2]float64, 0, len(excluded))
	for _, span := range excluded {
		if span[1] > span[0] {
			spans = append(spans, span)
		}
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i][0] != spans[j][0] {
			return spans[i][0] < spans[j][0]
		}
		return spans[i][1] < spans[j][1]
	})
	var out [][2]float64
	for _, span := range spans {
		if n := len(out); n > 0 && span[0] <= out[n-1][1] {
			if span[1] > out[n-1][1] {
				out[n-1] = [2]float64{out[n-1][0], span[1]}
			}
		} else {
			out = append(out, span)
		}
	}
	return out
}

// scoredSeconds is the fight_start..fight_end window minus the merged excluded spans; it
// drives the cut-short check and the rate denominator.
// ScoredSeconds is the fight_start..fight_end window minus the merged excluded spans (or the
// rows/samples span without a window).
func ScoredSeconds(rows, samples []rules.Row, excluded [][2]float64, window *[2]float64) float64 {
	if window != nil {
		lo, hi := window[0], window[1]
		cut := 0.0
		for _, span := range mergeSpans(excluded) {
			cut += maxf(0.0, minf(hi, span[1])-maxf(lo, span[0]))
		}
		return maxf(0.0, (hi-lo)-cut)
	}
	var lo, hi float64
	have := false
	for _, r := range rows {
		if pyjson.Str(r["kind"]) == "excluded" {
			continue
		}
		t, ok := pyjson.Float(r["t"])
		if !ok {
			continue
		}
		if !have {
			lo, hi, have = t, t, true
			continue
		}
		lo = minf(lo, t)
		hi = maxf(hi, t)
	}
	for _, s := range samples {
		t, ok := pyjson.Float(s["t"])
		if !ok {
			continue
		}
		if !have {
			lo, hi, have = t, t, true
			continue
		}
		lo = minf(lo, t)
		hi = maxf(hi, t)
	}
	if !have {
		return 0.0
	}
	cut := 0.0
	for _, span := range mergeSpans(excluded) {
		cut += maxf(0.0, minf(hi, span[1])-maxf(lo, span[0]))
	}
	return maxf(0.0, (hi-lo)-cut)
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// minNValue is Python's `rule.get("min_n") or rule.get("check", {}).get("min_n", 0) or 0`
// before its int(): the raw value, so a caller converts exactly where Python does.
func minNValue(rule map[string]any) any {
	if pyjson.Truthy(rule["min_n"]) {
		return rule["min_n"]
	}
	if check, ok := pyjson.Object(rule["check"]); ok && pyjson.Truthy(check["min_n"]) {
		return check["min_n"]
	}
	return int64(0)
}

// intLessThan is Python's `n < converted` for an int() result that may be a *big.Int.
func intLessThan(n int64, converted any) bool {
	switch v := converted.(type) {
	case int64:
		return n < v
	case *big.Int:
		return big.NewInt(n).Cmp(v) < 0
	}
	return false
}

// intGreaterThan is Python's `converted > n` for an int() result that may be a *big.Int.
func intGreaterThan(converted any, n int64) bool {
	switch v := converted.(type) {
	case int64:
		return v > n
	case *big.Int:
		return v.Cmp(big.NewInt(n)) > 0
	}
	return false
}

// floatOrZero is Python's float(value or 0) where the conversion is unguarded.
func floatOrZero(v any) (float64, error) {
	if !pyjson.Truthy(v) {
		return 0, nil
	}
	return pyjson.PyFloat(v)
}

// markTime is a mark's time on the normaliser's base: its ISO wall minus the base wall, else
// its plain numeric `t`.
func markTime(mark rules.Row, baseWall float64, hasBase bool) *float64 {
	if mark == nil {
		return nil
	}
	if wall, ok := events.WallSecs(mark["wall"]); ok && hasBase {
		v := wall - baseWall
		return &v
	}
	if f, ok := pyjson.Float(mark["t"]); ok {
		return &f
	}
	return nil
}

// fightWindow is (lo, hi, fightEndMissing) of the scored window.
func fightWindow(marks, samples []rules.Row) (float64, float64, bool, bool) {
	var fs, so, fe rules.Row
	for _, mark := range marks {
		switch pyjson.Str(mark["kind"]) {
		case "fight_start":
			if fs == nil {
				fs = mark
			}
		case "setup_ok":
			if so == nil {
				so = mark
			}
		case "fight_end":
			fe = mark
		}
	}
	var base float64
	hasBase := false
	if fs != nil {
		base, hasBase = events.WallSecs(fs["wall"])
	}
	if !hasBase && so != nil {
		base, hasBase = events.WallSecs(so["wall"])
	}
	type timed struct {
		T      float64
		Sample rules.Row
	}
	var times []timed
	for _, sample := range samples {
		t, ok := pyjson.Float(sample["t"])
		if !ok {
			continue
		}
		times = append(times, timed{T: t, Sample: sample})
	}
	var lo *float64
	if fs != nil {
		lo = markTime(fs, base, hasBase)
	}
	if lo == nil {
		if fs != nil || so != nil {
			v := 0.0
			lo = &v
		} else if len(times) > 0 {
			v := times[0].T
			for _, item := range times[1:] {
				v = minf(v, item.T)
			}
			lo = &v
		}
	}
	var hi *float64
	if fe != nil {
		hi = markTime(fe, base, hasBase)
	}
	missing := hi == nil
	if missing {
		var fight, later []float64
		for _, item := range times {
			if lo == nil || item.T >= *lo {
				later = append(later, item.T)
				if pyjson.Truthy(item.Sample["in_combat"]) {
					fight = append(fight, item.T)
				}
			}
		}
		pickMax := func(vals []float64) *float64 {
			if len(vals) == 0 {
				return nil
			}
			v := vals[0]
			for _, x := range vals[1:] {
				v = maxf(v, x)
			}
			return &v
		}
		hi = pickMax(fight)
		if hi == nil {
			hi = pickMax(later)
		}
	}
	if lo == nil || hi == nil {
		return 0, 0, missing, false
	}
	return *lo, maxf(*lo, *hi), missing, true
}

// excludedSpans are the normaliser's kind: excluded rows, merged (the ONE source).
func excludedSpans(rows []rules.Row, marks []rules.Row) [][2]float64 {
	var spans [][2]float64
	for _, row := range rows {
		if pyjson.Str(row["kind"]) != "excluded" {
			continue
		}
		a, okA := pyjson.Float(row["from"])
		b, okB := pyjson.Float(row["to"])
		if !okA || !okB {
			continue
		}
		if b > a {
			spans = append(spans, [2]float64{a, b})
		}
	}
	return mergeSpans(spans)
}

func buildCtx(runDir string, trial Trial, setup map[string]any, window *[2]float64) *rules.Ctx {
	thr, assumed := thresholds(runDir, setup)
	ctx := &rules.Ctx{
		Guids:             pyjson.ObjectOrEmpty(setup["guids"]),
		Thresholds:        thr,
		ThresholdsAssumed: assumed,
		Trial:             trial,
		Window:            window,
	}
	return ctx
}

// loadRepRows reads a repeat's setup, samples, marks, events and victim table.
func loadRepRows(repDir string, trial Trial) (setup map[string]any, samples, marks, rows []rules.Row, victims map[string][]events.AttackVictim, missing bool, err error) {
	setup = objectOrEmpty(readJSON(filepath.Join(repDir, "setup.json")))
	if _, statErr := os.Stat(filepath.Join(repDir, "setup.json")); statErr != nil {
		return nil, nil, nil, nil, nil, true, nil
	}
	samples, err = readJSONL(filepath.Join(repDir, "samples.jsonl"))
	if err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	marks, err = readJSONL(filepath.Join(repDir, "marks.jsonl"))
	if err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	rows, eventsErr := readJSONL(filepath.Join(repDir, "events.jsonl"))
	rawPath := filepath.Join(repDir, "raw-ta.jsonl")
	hasSeqRow := false
	for _, r := range rows {
		if r["seq"] != nil {
			hasSeqRow = true
			break
		}
	}
	// Python's condition ignores a non-object line: `(not rows or no seq row) and raw exists`.
	// When it normalises, the bad events.jsonl is overwritten and the line never matters; when
	// it does not, the Python keeps the line and fails on `.get` later.
	if (len(rows) == 0 || !hasSeqRow) && fileExists(rawPath) {
		rows, err = events.Normalise(repDir, trial)
		if err != nil {
			return nil, nil, nil, nil, nil, false, err
		}
		samples, err = readJSONL(filepath.Join(repDir, "samples.jsonl"))
		if err != nil {
			return nil, nil, nil, nil, nil, false, err
		}
		marks, err = readJSONL(filepath.Join(repDir, "marks.jsonl"))
		if err != nil {
			return nil, nil, nil, nil, nil, false, err
		}
	} else if eventsErr != nil {
		return nil, nil, nil, nil, nil, false, eventsErr
	}
	// The victim table needs the raw agent packets (attack_start/stop), which the normaliser
	// drops; build it here once per rep.
	raw, err := readJSONL(rawPath)
	if err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	victims = map[string][]events.AttackVictim{}
	if len(raw) > 0 {
		victims, err = events.AttackVictims(raw, marks, samples)
		if err != nil {
			return nil, nil, nil, nil, nil, false, err
		}
	}
	return setup, samples, marks, rows, victims, false, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// CutShortReason is 'cut short (<n> s, ended: <reason>)'; a floor end says how many rescues
// it took.
func CutShortReason(marks []rules.Row, scored float64) string {
	ended := ""
	for _, mark := range marks {
		if pyjson.Str(mark["kind"]) == "fight_end" && pyjson.Truthy(mark["ended"]) {
			ended = pyjson.Str(mark["ended"])
		}
	}
	if ended == "floor" {
		rescues := 0
		for _, mark := range marks {
			if pyjson.Str(mark["kind"]) == "gm_heal" {
				rescues++
			}
		}
		return fmt.Sprintf("cut short (%.0f s, ended: floor after %d rescues)", scored, rescues)
	}
	if ended == "" {
		ended = "unknown"
	}
	return fmt.Sprintf("cut short (%.0f s, ended: %s)", scored, ended)
}

// ScoreRep scores one repeat directory, writing verdicts.json. It panics on an input the
// Python bench raises on: ScoreRepE is the same work with the error returned.
func ScoreRep(repDir string, trial Trial, priorEvents []rules.PriorEvent) map[string]any {
	doc, err := ScoreRepE(repDir, trial, priorEvents)
	if err != nil {
		// The bench would fail loudly; a scored run must not silently pass.
		panic(fmt.Sprintf("trials score: %v", err))
	}
	return doc
}

// ScoreRepE scores one repeat directory, writing verdicts.json, and returns the error an input
// the Python bench raises on would have raised.
func ScoreRepE(repDir string, trial Trial, priorEvents []rules.PriorEvent) (map[string]any, error) {
	setup, samples, marks, rows, victims, missing, err := loadRepRows(repDir, trial)
	if err != nil {
		return nil, err
	}
	repName := filepath.Base(strings.TrimRight(repDir, "/"))
	if missing {
		// A rep dir without setup.json is INVALID with a reason (not dropped).
		reason := "rep without setup.json"
		out := map[string]any{"trial": trial["id"], "rep": repName,
			"ok_setup": false, "invalid_reason": reason, "rules": []any{}}
		out["rules"] = invalidRules(trial, reason)
		if err := dumpVerdicts(repDir, out); err != nil {
			return nil, err
		}
		return out, nil
	}
	// run dir is two levels up from rep dir (<run>/<trial>/repN)
	abs, _ := filepath.Abs(repDir)
	runDir := filepath.Dir(filepath.Dir(abs))
	lo, hi, endMissing, hasWindow := fightWindow(marks, samples)
	var window *[2]float64
	if hasWindow {
		window = &[2]float64{lo, hi}
	}
	ctx := buildCtx(runDir, trial, setup, window)
	ctx.VictimsByGUID = victims
	baseWall := events.TimeBase(marks, samples)
	excluded := excludedSpans(rows, marks)
	out := map[string]any{"trial": trial["id"], "rep": repName,
		"ok_setup": pyjson.Truthy(setup["ok"]), "rules": []any{},
		"thresholds":         ctx.Thresholds,
		"thresholds_assumed": ctx.ThresholdsAssumed,
		"fight_end_missing":  endMissing}
	if !pyjson.Truthy(setup["ok"]) {
		reason := pyjson.Str(setup["invalid_reason"])
		if !pyjson.Truthy(setup["invalid_reason"]) {
			reason = "setup not proven"
		}
		out["rules"] = invalidRules(trial, reason)
		out["invalid_reason"] = reason
		if err := dumpVerdicts(repDir, out); err != nil {
			return nil, err
		}
		return out, nil
	}
	scored := ScoredSeconds(rows, samples, excluded, window)
	out["scored_s"] = pyjson.Round(scored, 1)
	// Python's round(sum(...), 1): an empty sum is the int 0, a non-empty one a float.
	roundSum := func(total float64) any {
		if len(excluded) == 0 {
			return int64(0)
		}
		return pyjson.Round(total, 1)
	}
	if window != nil {
		out["window"] = []any{pyjson.Round(window[0], 2), pyjson.Round(window[1], 2)}
		var parts []float64
		for _, span := range excluded {
			parts = append(parts, maxf(0.0, minf(window[1], span[1])-maxf(window[0], span[0])))
		}
		out["excluded_s"] = roundSum(pyjson.SumFloat(parts))
	} else {
		var parts []float64
		for _, span := range excluded {
			parts = append(parts, span[1]-span[0])
		}
		out["excluded_s"] = roundSum(pyjson.SumFloat(parts))
	}
	winSamples := samples
	if window != nil {
		winSamples = rules.WindowSamples(samples, window)
	}
	// Python: min_valid = float(trial.get("min_valid_s") or 0), unguarded.
	minValid, err := floatOrZero(trial["min_valid_s"])
	if err != nil {
		return nil, fmt.Errorf("min_valid_s: %w", err)
	}
	if scored < minValid {
		reason := CutShortReason(marks, scored)
		out["rules"] = invalidRules(trial, reason)
		out["invalid_reason"] = reason
		if err := dumpVerdicts(repDir, out); err != nil {
			return nil, err
		}
		return out, nil
	}
	var ruleEntries []any
	trialRules, _ := pyjson.List(trial["rules"])
	for _, ruleVal := range trialRules {
		rule, ok := pyjson.Object(ruleVal)
		if !ok {
			continue
		}
		// a rule that carries the responder's cooldown across reps gets the earlier responses
		// as excluded spans on this rep's base
		ruleRows := rows
		spans := rules.PriorResponseSpans(rule, priorEvents, baseWall, true, ctx)
		if len(spans) > 0 {
			ruleRows = append([]rules.Row(nil), rows...)
			for _, span := range spans {
				ruleRows = append(ruleRows, rules.Row{
					"kind": "excluded", "from": pyjson.Round(span[0], 3), "to": pyjson.Round(span[1], 3),
					"why": "prior rep responder cooldown", "t": pyjson.Round(span[0], 3),
				})
			}
		}
		res := rules.Evaluate(rule, ruleRows, samples, marks, ctx)
		if res.Err != nil {
			return nil, fmt.Errorf("rule %s in %s: %w", pyjson.Str(rule["id"]), repDir, res.Err)
		}
		// Python: min_opp = rule.get("min_opportunity_s") or 0, converted by
		// float(min_opp) at the comparison, which is unguarded.
		minOpp := rule["min_opportunity_s"]
		// Python: rule.get("min_n") or rule.get("check", {}).get("min_n", 0) or 0 — a
		// rule-level 0 is falsy, so check.min_n still applies.
		minN := minNValue(rule)
		guardHold := true
		guardWhy := res.Verdict
		if pyjson.Truthy(minOpp) && (res.Verdict == "PASS" || res.Verdict == "FAIL") {
			minOppF, err := floatOrZero(minOpp)
			if err != nil {
				return nil, fmt.Errorf("rule %s in %s: min_opportunity_s: %w", pyjson.Str(rule["id"]), repDir, err)
			}
			// the rule's own not_within_s_after_mark windows carry no chance either
			opp := rules.OpportunitySeconds(trial, rules.AfterMarkSamples(winSamples, rule, marks), excluded, ctx)
			if opp < minOppF {
				guardHold = false
				guardWhy = fmt.Sprintf("opportunity %.0f s < %.0f s", opp, minOppF)
			}
		}
		nHave := res.N
		// A pooled rule's trigger minimum is applied to the pooled total in ScoreTrial, not
		// per repeat. Python calls int(min_n) here with no guard, so a value it cannot
		// convert is an error on this path.
		if pyjson.Truthy(minN) && !(pyjson.IsBool(rule["pool"]) && pyjson.Truthy(rule["pool"])) &&
			(res.Verdict == "PASS" || res.Verdict == "FAIL") {
			converted, err := pyjson.PyInt(minN)
			if err != nil {
				return nil, fmt.Errorf("rule %s in %s: min_n: %w", pyjson.Str(rule["id"]), repDir, err)
			}
			if intLessThan(nHave, converted) {
				guardHold = false
				guardWhy = fmt.Sprintf("n %d < min_n %s", nHave, pyjson.Str(converted))
			}
		}
		evidence := res.Evidence
		if evidence == nil {
			evidence = []string{}
		}
		entry := map[string]any{"id": rule["id"], "verdict": res.Verdict,
			"value": res.Value, "k": res.K, "n": res.N,
			"evidence": evidence,
			"guard":    res.Guard,
			"no_match": res.NoMatch}
		if res.Verdict == "INCONCLUSIVE" {
			// The real reason an INCONCLUSIVE is printed in the PR block.
			reason := res.Reason
			if reason == "" && len(evidence) > 0 {
				reason = evidence[0]
			}
			entry["reason"] = reason
		}
		if !guardHold {
			entry["verdict"] = "INCONCLUSIVE"
			entry["reason"] = guardWhy
			guard := res.Guard
			if guard != "" {
				guard += "; "
			}
			entry["guard"] = guard + guardWhy
		}
		ruleEntries = append(ruleEntries, entry)
	}
	out["rules"] = ruleEntries
	if err := dumpVerdicts(repDir, out); err != nil {
		return nil, err
	}
	return out, nil
}

func invalidRules(trial Trial, reason string) []any {
	out := []any{}
	trialRules, _ := pyjson.List(trial["rules"])
	for _, ruleVal := range trialRules {
		rule, _ := pyjson.Object(ruleVal)
		var id any
		if rule != nil {
			id = rule["id"]
		}
		out = append(out, map[string]any{"id": id, "verdict": "INVALID",
			"invalid_reason": reason, "value": nil,
			"evidence": []any{}, "guard": ""})
	}
	return out
}

// dumpVerdicts writes verdicts.json; a value Python's allow_nan=False would refuse is an
// error, the same class as the bench's ValueError.
func dumpVerdicts(repDir string, doc map[string]any) error {
	if err := pyjson.Dump(filepath.Join(repDir, "verdicts.json"), doc); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Join(repDir, "verdicts.json"), err)
	}
	return nil
}

// priorEvents is [(absolute wall, row)] of one scored repeat, for the next repeat's cross-rep
// debounce.
func priorEvents(repDir string) ([]rules.PriorEvent, error) {
	marks, err := readJSONL(filepath.Join(repDir, "marks.jsonl"))
	if err != nil {
		return nil, err
	}
	base := events.TimeBase(marks, nil)
	if base == 0 {
		return nil, nil
	}
	rows, err := readJSONL(filepath.Join(repDir, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	var out []rules.PriorEvent
	for _, row := range rows {
		t, ok := pyjson.Float(row["t"])
		if !ok {
			if row["t"] == nil {
				t = 0
			} else {
				continue
			}
		}
		out = append(out, rules.PriorEvent{Wall: base + t, Row: row})
	}
	return out, nil
}

var repNumberRe = regexp.MustCompile(`rep(\d+)$`)

// RepNumber is the numeric index of a `repN` dir: rep10 sorts after rep9, never after rep1.
func RepNumber(path string) int {
	m := repNumberRe.FindStringSubmatch(filepath.Base(filepath.Clean(path)))
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// RepDirs lists a trial's `rep*` dirs in run order.
func RepDirs(trialDir string) []string {
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
	sort.SliceStable(out, func(i, j int) bool { return RepNumber(out[i]) < RepNumber(out[j]) })
	return out
}

// ScoreTrial scores every repeat of one trial and writes trial.json. It panics on an input
// the Python bench raises on: ScoreTrialE is the same work with the error returned.
func ScoreTrial(trialDir string, trial Trial) map[string]any {
	out, err := ScoreTrialE(trialDir, trial)
	if err != nil {
		panic(fmt.Sprintf("trials score: %v", err))
	}
	return out
}

// ScoreTrialE scores every repeat of one trial, writes trial.json and returns the error an
// input the Python bench raises on would have raised.
func ScoreTrialE(trialDir string, trial Trial) (map[string]any, error) {
	return scoreTrialWith(trialDir, trial, ScoreRepE)
}

// repScorer is the per-repeat scorer ScoreTrialE threads prior events through. It is a
// parameter (not a package variable) so the rep-order test can watch the calls.
type repScorer func(repDir string, trial Trial, prior []rules.PriorEvent) (map[string]any, error)

func scoreTrialWith(trialDir string, trial Trial, scoreRep repScorer) (map[string]any, error) {
	reps := RepDirs(trialDir)
	carries := false
	trialRules, _ := pyjson.List(trial["rules"])
	for _, ruleVal := range trialRules {
		if rule, ok := pyjson.Object(ruleVal); ok {
			if check, ok := pyjson.Object(rule["check"]); ok && pyjson.Truthy(check["debounce_responder_across_reps"]) {
				carries = true
			}
		}
	}
	var prior []rules.PriorEvent
	var scored []map[string]any
	for _, rep := range reps {
		info, err := os.Stat(rep)
		if err != nil || !info.IsDir() {
			continue
		}
		if fileExists(filepath.Join(rep, "setup.json")) {
			doc, err := scoreRep(rep, trial, prior)
			if err != nil {
				return nil, err
			}
			scored = append(scored, doc)
		} else if fileExists(filepath.Join(rep, "verdicts.json")) {
			// Pre-scored (hand-written test reps): read as-is.
			scored = append(scored, objectOrEmpty(readJSON(filepath.Join(rep, "verdicts.json"))))
		} else {
			doc, err := scoreRep(rep, trial, prior)
			if err != nil {
				return nil, err
			}
			scored = append(scored, doc)
		}
		if carries {
			events, err := priorEvents(rep)
			if err != nil {
				return nil, err
			}
			prior = append(prior, events...)
		}
	}
	votes := map[string][]struct {
		Rep string
		Row map[string]any
	}{}
	for _, rep := range scored {
		repName := pyjson.Str(rep["rep"])
		repRules, _ := pyjson.List(rep["rules"])
		for _, rowVal := range repRules {
			row, ok := pyjson.Object(rowVal)
			if !ok {
				continue
			}
			rid := pyjson.Str(row["id"])
			votes[rid] = append(votes[rid], struct {
				Rep string
				Row map[string]any
			}{repName, row})
		}
	}
	var outRules []any
	for _, ruleVal := range trialRules {
		rule, ok := pyjson.Object(ruleVal)
		if !ok {
			continue
		}
		rid := pyjson.Str(rule["id"])
		ruleVotes := votes[rid]
		type vote struct {
			Rep string
			Row map[string]any
		}
		var valid []vote
		var attackUnknown []vote
		for _, v := range ruleVotes {
			verdict := pyjson.Str(v.Row["verdict"])
			if verdict == "PASS" || verdict == "FAIL" {
				valid = append(valid, vote{v.Rep, v.Row})
			}
			if verdict == "INCONCLUSIVE" && pyjson.Str(v.Row["reason"]) == "attack state unknown" {
				attackUnknown = append(attackUnknown, vote{v.Rep, v.Row})
			}
		}
		var verdict string
		var kSum, nSum int64
		reason := ""
		if len(valid) == 0 {
			// Zero valid repeats: INCONCLUSIVE unless a repeat was INVALID and none scored.
			anyInvalid := false
			anyScored := false
			for _, v := range ruleVotes {
				switch pyjson.Str(v.Row["verdict"]) {
				case "INVALID":
					anyInvalid = true
				case "PASS", "FAIL", "INCONCLUSIVE":
					anyScored = true
				}
			}
			if anyInvalid && !anyScored {
				verdict = "INVALID"
			} else {
				verdict = "INCONCLUSIVE"
			}
		} else {
			nPass := 0
			for _, v := range valid {
				if pyjson.Str(v.Row["verdict"]) == "PASS" {
					nPass++
				}
			}
			nFail := len(valid) - nPass
			if nPass > nFail {
				verdict = "PASS"
			} else {
				verdict = "FAIL"
			}
			for _, v := range valid {
				// Python: int(row.get("k", 0) or 0), so 5.0 counts as 5.
				n, err := intOrZero(v.Row["k"])
				if err != nil {
					return nil, fmt.Errorf("rule %s: k: %w", rid, err)
				}
				kSum += n
				n, err = intOrZero(v.Row["n"])
				if err != nil {
					return nil, fmt.Errorf("rule %s: n: %w", rid, err)
				}
				nSum += n
			}
			if pyjson.IsBool(rule["pool"]) && pyjson.Truthy(rule["pool"]) {
				// POOLED: one verdict from the summed k/n over every valid repeat.
				var value *float64
				if nSum != 0 {
					v := float64(kSum) / float64(nSum)
					value = &v
				}
				// Python: min_n = (... or 0); try int(min_n) except
				// (TypeError, ValueError) -> 0. int(inf) escapes as OverflowError; an
				// integer beyond int64 is still a Python int and compares.
				var minN any = int64(0)
				if converted, err := pyjson.PyInt(minNValue(rule)); err != nil {
					if !pyjson.CaughtByTypeValue(err) {
						return nil, fmt.Errorf("rule %s: min_n: %w", rid, err)
					}
				} else {
					minN = converted
				}
				if len(attackUnknown) > 0 {
					// An unreadable attack state in any repeat is missing evidence.
					verdict = "INCONCLUSIVE"
					reps := make([]string, 0, len(attackUnknown))
					for _, v := range attackUnknown {
						reps = append(reps, v.Rep)
					}
					reason = fmt.Sprintf("attack state unknown (%s)", strings.Join(reps, ", "))
				} else if value == nil {
					verdict = "INCONCLUSIVE"
					reason = "pooled n 0"
				} else if pyjson.Truthy(minN) && intLessThan(nSum, minN) {
					verdict = "INCONCLUSIVE"
					reason = fmt.Sprintf("pooled n %d < min_n %s", nSum, pyjson.Str(minN))
				} else if rules.Pass(rule["pass_if"], *value) {
					verdict = "PASS"
				} else {
					verdict = "FAIL"
				}
			}
			// Python: min_reps = rule.get("min_reps") or 0; try int() except
			// (TypeError, ValueError) -> 0. int(inf) escapes as OverflowError; an
			// integer beyond int64 is still a plain Python int and compares.
			minRepsVal := rule["min_reps"]
			if !pyjson.Truthy(minRepsVal) {
				minRepsVal = int64(0)
			}
			minReps, err := pyjson.PyInt(minRepsVal)
			if err != nil {
				if !pyjson.CaughtByTypeValue(err) {
					return nil, fmt.Errorf("rule %s: min_reps: %w", rid, err)
				}
				minReps = int64(0)
			}
			if intGreaterThan(minReps, 1) && intLessThan(int64(len(valid)), minReps) {
				verdict = "INCONCLUSIVE"
				reason = fmt.Sprintf("valid reps %d < min_reps %s", len(valid), pyjson.Str(minReps))
			}
		}
		detail := []any{}
		for _, v := range ruleVotes {
			row := v.Row
			evidence := row["evidence"]
			if evidence == nil {
				evidence = []any{}
			}
			detail = append(detail, map[string]any{
				"rep": v.Rep, "verdict": row["verdict"], "value": row["value"],
				"k": defaultField(row, "k"), "n": defaultField(row, "n"),
				"evidence": evidence, "guard": orEmpty(row["guard"]),
				"reason":         orEmpty(row["reason"]),
				"no_match":       pyjson.Truthy(row["no_match"]),
				"invalid_reason": orEmpty(row["invalid_reason"]),
			})
		}
		var kOut, nOut int64
		if len(valid) > 0 {
			kOut, nOut = kSum, nSum
		} else {
			for _, v := range valid {
				if pyjson.Str(v.Row["verdict"]) == verdict {
					kOut++
				}
			}
			nOut = int64(len(valid))
		}
		var metric, checkAny any
		if check, ok := pyjson.Object(rule["check"]); ok {
			metric = check["metric"]
		} else {
			checkAny = rule["check"]
			_ = checkAny
		}
		reasonOut := ""
		if len(valid) > 0 && reason != "" {
			reasonOut = reason
		}
		outRules = append(outRules, map[string]any{
			"id": rid, "verdict": verdict, "k": kOut, "n": nOut, "detail": detail,
			"expected": rule["expect_today"], "metric": metric,
			"pass_if": rule["pass_if"], "source": rule["source"],
			"desc":   rule["desc"],
			"reason": reasonOut,
		})
	}
	out := map[string]any{"trial": trial["id"], "verdicts": outRules}
	if outRules == nil {
		out["verdicts"] = []any{}
	}
	if err := pyjson.Dump(filepath.Join(trialDir, "trial.json"), out); err != nil {
		return nil, fmt.Errorf("write %s: %w", filepath.Join(trialDir, "trial.json"), err)
	}
	return out, nil
}

// defaultField is Python's `row.get(key, 0)`: the raw value when the key is present (an int
// stays an int, a float stays a float), 0 only when it is absent.
func defaultField(row map[string]any, key string) any {
	if v, ok := row[key]; ok {
		return v
	}
	return int64(0)
}

// intOrZero is Python's int(value or 0): a falsy value is 0, a float is truncated, a numeric
// string is accepted, and anything Python's int() rejects is an error.
func intOrZero(value any) (int64, error) {
	if !pyjson.Truthy(value) {
		return 0, nil
	}
	converted, err := pyjson.PyInt(value)
	if err != nil {
		return 0, err
	}
	n, ok := pyjson.Int64(converted)
	if !ok {
		return 0, fmt.Errorf("integer %s does not fit int64", pyjson.Str(converted))
	}
	return n, nil
}

// ScoreRun scores all trials of a run, writes card.md and card.json, and returns the
// per-trial verdict docs. It panics on an input the Python bench raises on: ScoreRunE is the
// same work with the error returned.
func ScoreRun(runDir string) []map[string]any {
	runTrials, err := ScoreRunE(runDir)
	if err != nil {
		panic(fmt.Sprintf("trials score: %v", err))
	}
	return runTrials
}

// ScoreRunE scores all trials of a run and returns the error an input the Python bench raises
// on would have raised (an unreadable snapshot, a non-object events line, ...).
func ScoreRunE(runDir string) ([]map[string]any, error) {
	var runTrials []map[string]any
	runDoc := objectOrEmpty(readJSON(filepath.Join(runDir, "RUN.json")))
	trials, _ := pyjson.List(runDoc["trials"])
	for _, tidVal := range trials {
		tid := pyjson.Str(tidVal)
		tdir := filepath.Join(runDir, tid)
		// The run froze the effective trial (merged for a `<trial>@<variant>`) before it ran:
		// score that, never today's definition.
		snap := filepath.Join(tdir, scenario.SnapshotName)
		var trial Trial
		if info, err := os.Stat(snap); err == nil && !info.IsDir() {
			doc, err := pyjson.UnmarshalFile(snap)
			if err != nil {
				return nil, fmt.Errorf("%s: unreadable trial snapshot %s: %w", runDir, snap, err)
			}
			obj, ok := pyjson.Object(doc)
			if !ok || pyjson.Str(obj["id"]) != tid {
				return nil, fmt.Errorf("%s: trial snapshot %s does not hold trial %q", runDir, snap, tid)
			}
			trial = obj
		} else if pyjson.Truthy(runDoc["trial_snapshots"]) {
			if info, err := os.Stat(tdir); err == nil && info.IsDir() {
				return nil, fmt.Errorf("%s: no %s for trial %q although the run recorded snapshots; refusing to score it against today's trial files",
					runDir, scenario.SnapshotName, tid)
			}
			continue
		} else {
			fmt.Fprintf(os.Stderr, "score: %s has no trial snapshots (a run from before they existed): scoring trial %q against the CURRENT trial files\n", runDir, tid)
			trial = scenario.FindTrialByID(tid)
		}
		if trial == nil {
			trial = scenario.FindFixtureByID(tid)
		}
		if trial == nil {
			continue
		}
		if info, err := os.Stat(tdir); err != nil || !info.IsDir() {
			continue
		}
		doc, err := ScoreTrialE(tdir, trial)
		if err != nil {
			return nil, err
		}
		runTrials = append(runTrials, doc)
	}
	if _, err := report.WriteCard(runDir, runTrials); err != nil {
		return nil, err
	}
	return runTrials, nil
}

// orEmpty is Python's `value or ""`.
func orEmpty(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}
