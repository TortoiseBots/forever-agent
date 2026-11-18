// Package report writes the report card (card.md + card.json), the baseline compare and the
// PR block.
//
// card.md: REPORT_TITLE + label + server + tb_version, the run line, a data-quality block, one
// verdict table, then FAIL/surprise evidence lines. Compare(run, base) matches rows by
// (trial id, rule id); latency/ratio/uptime k/n are summed over valid repeats, Fisher's p
// applies to those only. PRBlock(before, after, issue) is the exact block for PRs.
package report

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/naming"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/wr"
)

func readJSON(path string) any {
	doc, err := pyjson.UnmarshalFile(path)
	if err != nil {
		return nil
	}
	return doc
}

func obj(doc any) map[string]any { return pyjson.ObjectOrEmpty(doc) }

func surprise(expected, verdict any) string {
	exp, _ := expected.(string)
	ver, _ := verdict.(string)
	if exp == "FAIL" && ver == "PASS" {
		return "expected FAIL, got PASS"
	}
	if exp == "PASS" && ver == "FAIL" {
		return "expected PASS, got FAIL"
	}
	return ""
}

// fmtValue is the card's cell for one value: "no match"/"-" for none, "%.3g" for a float.
func fmtValue(value any, noMatch bool) string {
	if value == nil {
		if noMatch {
			return "no match"
		}
		return "-"
	}
	if f, ok := value.(float64); ok {
		return pyjson.FormatG(3, f)
	}
	return pyjson.Str(value)
}

// pooledMetrics are the k/n metrics whose card value is the POOLED k/n over valid repeats.
var pooledMetrics = map[string]bool{"ratio": true, "latency": true, "uptime": true}

// aggValue is the card value: k/n metrics show the pooled k/n, the others the mean of the
// repeat values. vals are the per-repeat values.
// AggValue is the card value of one rule row: the pooled k/n for a k/n metric, else the mean.
func AggValue(details []any, metric any) (any, []any) {
	metricStr, _ := metric.(string)
	var valid []map[string]any
	var vals []any
	for _, detVal := range details {
		det, ok := pyjson.Object(detVal)
		if !ok {
			continue
		}
		verdict, _ := det["verdict"].(string)
		if verdict != "PASS" && verdict != "FAIL" {
			continue
		}
		valid = append(valid, det)
		if det["value"] != nil {
			vals = append(vals, det["value"])
		}
	}
	if pooledMetrics[metricStr] && metricStr != "" {
		var k, n int64
		for _, det := range valid {
			dk := intField(det["k"])
			dn := intField(det["n"])
			if dn > 0 {
				k += dk
				n += dn
			}
		}
		if n > 0 {
			return float64(k) / float64(n), vals
		}
	}
	var nums []float64
	for _, v := range vals {
		f, ok := pyjson.Float(v)
		if !ok {
			continue
		}
		if math.IsInf(f, 1) {
			continue
		}
		nums = append(nums, f)
	}
	if len(vals) == 0 {
		return nil, []any{}
	}
	if len(nums) == 0 {
		// Every repeat was a no-match (value null): the card shows "no match".
		return nil, vals
	}
	return pyjson.SumFloat(nums) / float64(len(nums)), vals
}

func intField(v any) int64 {
	if n, ok := pyjson.Int64(v); ok {
		return n
	}
	if f, ok := pyjson.Float(v); ok {
		return int64(f)
	}
	return 0
}

// repeatsText is the repeats that RAN, not the --repeats flag (0 means "each trial's own
// count"), counted from the rep* directories the run wrote.
// RepeatsText is the repeats that RAN, counted from the rep* directories the run wrote.
func RepeatsText(run map[string]any, runDir string, runTrials []map[string]any) string {
	args := obj(run["args"])
	type count struct {
		Tid string
		N   int
	}
	var counts []count
	for _, trial := range runTrials {
		tid := pyjson.Str(trial["trial"])
		entries, err := os.ReadDir(filepath.Join(runDir, tid))
		n := 0
		if err == nil {
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), "rep") {
					continue
				}
				if info, err := entry.Info(); err == nil && info.IsDir() {
					n++
				}
			}
		}
		counts = append(counts, count{tid, n})
	}
	seen := map[int]bool{}
	for _, c := range counts {
		seen[c.N] = true
	}
	var ran []int
	for n := range seen {
		ran = append(ran, n)
	}
	sort.Ints(ran)
	if len(counts) == 0 || (len(ran) == 1 && ran[0] == 0) {
		flag := args["repeats"]
		if pyjson.Truthy(flag) {
			return pyjson.Str(flag)
		}
		return "?"
	}
	if len(ran) == 1 {
		return strconv.Itoa(ran[0])
	}
	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		parts = append(parts, fmt.Sprintf("%s %d", c.Tid, c.N))
	}
	return strings.Join(parts, ", ")
}

var repNumberRe = regexp.MustCompile(`rep(\d+)`)

func repNumber(path string) int {
	m := repNumberRe.FindStringSubmatch(path)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// repTags is '; slot N; difficulty step S' for a rep whose setup.json carries them.
func repTags(setup map[string]any) string {
	tags := ""
	if slot := setup["slot"]; slot != nil && pyjson.Str(slot) != "" {
		tags += fmt.Sprintf("; slot %s", pyjson.Str(slot))
	}
	if dif, ok := pyjson.Object(setup["difficulty"]); ok && dif["step"] != nil {
		tags += fmt.Sprintf("; difficulty step %s", pyjson.Str(dif["step"]))
	}
	return tags
}

// ladderLine is one line per trial when any rep ran easier than written (step > 0), else "".
func ladderLine(trialID string, steps [][2]any) string {
	anyEasier := false
	for _, step := range steps {
		dif, _ := pyjson.Object(step[1])
		if f, _ := pyjson.Float(dif["step"]); f > 0 {
			anyEasier = true
		}
	}
	if !anyEasier {
		return ""
	}
	var parts []string
	for _, step := range steps {
		rep, _ := step[0].(string)
		dif, _ := pyjson.Object(step[1])
		text := fmt.Sprintf("%s step %s", rep, pyjson.Str(dif["step"]))
		if f, _ := pyjson.Float(dif["step"]); f > 0 {
			refill := "off"
			if pyjson.Truthy(dif["refill"]) {
				refill = "on"
			}
			text += fmt.Sprintf(" (level_delta %s, count %s, refill %s)",
				pyjson.Str(dif["level_delta"]), pyjson.Str(dif["count"]), refill)
		}
		parts = append(parts, text)
	}
	return fmt.Sprintf("- %s: ran easier than written after rescue-cap reps: %s", trialID, strings.Join(parts, "; "))
}

// WriteCard writes card.md and card.json for a run and returns the card rows.
func WriteCard(runDir string, runTrials []map[string]any) []any {
	run := obj(readJSON(filepath.Join(runDir, "RUN.json")))
	label := pyjson.Str(run["label"])
	if !pyjson.Truthy(run["label"]) {
		label = filepath.Base(strings.TrimRight(runDir, "/"))
	}
	server := pyjson.Str(run["server"])
	if !pyjson.Truthy(run["server"]) {
		server = "?"
	}
	tb := pyjson.Str(run["tb_version"])
	if !pyjson.Truthy(run["tb_version"]) {
		tb = "?"
	}
	var slotParts []string
	if slots, ok := pyjson.List(run["slots"]); ok {
		for _, s := range slots {
			slotParts = append(slotParts, pyjson.Str(s))
		}
	}
	slots := "?"
	if len(slotParts) > 0 {
		slots = strings.Join(slotParts, ", ")
	}
	started := pyjson.Str(run["started"])
	if !pyjson.Truthy(run["started"]) {
		started = "?"
	}
	lines := []string{
		fmt.Sprintf("# %s — %s — %s — %s", naming.ReportTitle, label, server, tb), "",
		fmt.Sprintf("Run: started %s, slots %s, repeats %s.", started, slots, RepeatsText(run, runDir, runTrials)),
		"",
	}
	lines = append(lines, "## Data quality")
	for _, trial := range runTrials {
		tdir := filepath.Join(runDir, pyjson.Str(trial["trial"]))
		var ladderSteps [][2]any
		setupPaths := repSetupFiles(tdir)
		for _, det := range setupPaths {
			repdir := filepath.Dir(det)
			setup := obj(readJSON(det))
			rep := filepath.Base(repdir)
			var failed []string
			if checks, ok := pyjson.List(setup["checks"]); ok {
				for _, checkVal := range checks {
					check, ok := pyjson.Object(checkVal)
					if ok && !pyjson.Truthy(check["ok"]) {
						failed = append(failed, pyjson.Str(check["id"]))
					}
				}
			}
			verdicts := obj(readJSON(filepath.Join(repdir, "verdicts.json")))
			ev := "events missing"
			if fileExists(filepath.Join(repdir, "events.jsonl")) {
				ev = "events ok"
			}
			summary := obj(readJSON(filepath.Join(repdir, "events_summary.json")))
			dropParts := []string{}
			if dropped, ok := pyjson.Object(summary["dropped_kinds"]); ok {
				keys := make([]string, 0, len(dropped))
				for k := range dropped {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					dropParts = append(dropParts, fmt.Sprintf("%s=%d", k, intField(dropped[k])))
				}
			}
			dropS := strings.Join(dropParts, ",")
			if dropS == "" {
				dropS = "none"
			}
			var exclParts []float64
			if spans, ok := pyjson.List(summary["excluded_spans"]); ok {
				for _, spanVal := range spans {
					span, ok := pyjson.Object(spanVal)
					if !ok {
						continue
					}
					to, _ := pyjson.Float(span["to"])
					from, _ := pyjson.Float(span["from"])
					exclParts = append(exclParts, to-from)
				}
			}
			exclSum := pyjson.SumFloat(exclParts)
			reason := pyjson.Str(verdicts["invalid_reason"])
			if !pyjson.Truthy(verdicts["invalid_reason"]) {
				reason = pyjson.Str(setup["invalid_reason"])
			}
			cutS := "cut-short: none"
			if strings.Contains(reason, "cut short") {
				cutS = fmt.Sprintf("cut-short: %s", reason)
			}
			var thrS string
			if _, present := verdicts["thresholds_assumed"]; present {
				thrS = thrText(verdicts)
			} else if thresholdsAssumed(run) {
				thrS = "assumed 20/50/70"
			} else {
				thrS = "live"
			}
			scoredS := "?"
			if v, present := verdicts["scored_s"]; present {
				scoredS = pyjson.Str(v)
			}
			setupText := "INVALID (" + pyjson.Str(setup["invalid_reason"]) + ")"
			if pyjson.Truthy(setup["ok"]) {
				setupText = "ok"
			} else if !pyjson.Truthy(setup["invalid_reason"]) {
				setupText = "INVALID (?)"
			}
			fightEnd := ""
			if pyjson.Truthy(verdicts["fight_end_missing"]) {
				fightEnd = "; fight_end missing"
			}
			lines = append(lines, fmt.Sprintf(
				"- %s %s: setup %s; failed=[%s]; scored=%s s; excluded=%.0f s; %s; %s; events_dropped=[%s]; thresholds=%s%s",
				pyjson.Str(trial["trial"]), rep, setupText, strings.Join(failed, ","),
				scoredS, exclSum, cutS, ev, dropS, thrS, fightEnd)+repTags(setup))
			if dif, ok := pyjson.Object(setup["difficulty"]); ok {
				ladderSteps = append(ladderSteps, [2]any{rep, dif})
			}
		}
		ladder := ladderLine(pyjson.Str(trial["trial"]), ladderSteps)
		if ladder != "" {
			lines = append(lines, ladder)
		}
	}
	if left, ok := pyjson.List(run["unverified_summons"]); ok && len(left) > 0 {
		parts := make([]string, 0, len(left))
		for _, g := range left {
			parts = append(parts, pyjson.Str(g))
		}
		lines = append(lines, "", fmt.Sprintf(
			"**Summons not proved dead or gone at the run end** (%d): %s. Run `trials cleanup <run-dir>`.", len(left), strings.Join(parts, ", ")))
	}
	lines = append(lines, "",
		"| trial | rule | expected | verdict | metric k/n (summed over repeats) | value | pass_if | surprise |",
		"|---|---|---|---|---|---|---|---|")
	var cardRows []any
	for _, trial := range runTrials {
		verdicts, _ := pyjson.List(trial["verdicts"])
		for _, rowVal := range verdicts {
			row, ok := pyjson.Object(rowVal)
			if !ok {
				continue
			}
			sur := surprise(row["expected"], row["verdict"])
			details, _ := pyjson.List(row["detail"])
			agg, vals := AggValue(details, row["metric"])
			nm := 0
			for _, detVal := range details {
				if det, ok := pyjson.Object(detVal); ok && pyjson.Truthy(det["no_match"]) {
					nm++
				}
			}
			valS := fmtValue(agg, nm > 0)
			if len(vals) > 1 {
				parts := make([]string, 0, len(vals))
				for _, v := range vals {
					parts = append(parts, fmtValue(v, false))
				}
				valS += " (" + strings.Join(parts, ", ") + ")"
			}
			if nm > 0 && agg != nil {
				valS += fmt.Sprintf(" [no match x%d]", nm)
			}
			expected := pyjson.Str(row["expected"])
			if !pyjson.Truthy(row["expected"]) {
				expected = "?"
			}
			passIf := pyjson.Str(row["pass_if"])
			lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s | %d/%d | %s | %s | %s |",
				pyjson.Str(trial["trial"]), pyjson.Str(row["id"]), expected,
				pyjson.Str(row["verdict"]), intField(row["k"]), intField(row["n"]),
				valS, passIf, sur))
			cardRows = append(cardRows, map[string]any{
				"trial": trial["trial"], "rule": row["id"],
				"expected": row["expected"], "verdict": row["verdict"],
				"k": row["k"], "n": row["n"],
				"value": agg, "values": vals, "no_match": nm > 0,
				"metric":  row["metric"],
				"pass_if": row["pass_if"], "surprise": sur,
				"source": row["source"], "detail": row["detail"],
				"reason": orEmpty(row["reason"]),
			})
		}
	}
	lines = append(lines, "")
	for _, trial := range runTrials {
		verdicts, _ := pyjson.List(trial["verdicts"])
		for _, rowVal := range verdicts {
			row, ok := pyjson.Object(rowVal)
			if !ok {
				continue
			}
			if pyjson.Str(row["verdict"]) != "FAIL" && surprise(row["expected"], row["verdict"]) == "" {
				continue
			}
			source := pyjson.Str(row["source"])
			if !pyjson.Truthy(row["source"]) {
				source = "no source"
			}
			lines = append(lines, fmt.Sprintf("**%s** (%s)", pyjson.Str(row["id"]), source))
			details, _ := pyjson.List(row["detail"])
			if len(details) > 3 {
				details = details[:3]
			}
			for _, detVal := range details {
				det, ok := pyjson.Object(detVal)
				if !ok {
					continue
				}
				evidence, _ := pyjson.List(det["evidence"])
				if len(evidence) > 3 {
					evidence = evidence[:3]
				}
				for _, ev := range evidence {
					lines = append(lines, fmt.Sprintf("- %s %s: %s",
						pyjson.Str(trial["trial"]), pyjson.Str(det["rep"]), pyjson.Str(ev)))
				}
			}
			lines = append(lines, "")
		}
	}
	text := strings.TrimRight(strings.Join(lines, "\n"), " \t\n\r\f\v") + "\n"
	if err := os.WriteFile(filepath.Join(runDir, "card.md"), []byte(text), 0o644); err != nil {
		panic(fmt.Sprintf("trials report: card.md: %v", err))
	}
	doc := map[string]any{
		"title": fmt.Sprintf("%s — %s — %s — %s", naming.ReportTitle, label, server, tb),
		"run":   run,
		"rows":  cardRows,
	}
	if cardRows == nil {
		doc["rows"] = []any{}
	}
	if err := pyjson.Dump(filepath.Join(runDir, "card.json"), doc); err != nil {
		panic(fmt.Sprintf("trials report: card.json: %v", err))
	}
	return cardRows
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// repSetupFiles lists a trial dir's rep*/setup.json files, sorted by rep number (the bench's
// _rep_number key).
func repSetupFiles(trialDir string) []string {
	entries, err := os.ReadDir(trialDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "rep") {
			continue
		}
		candidate := filepath.Join(trialDir, entry.Name(), "setup.json")
		if fileExists(candidate) {
			out = append(out, candidate)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return repNumber(out[i]) < repNumber(out[j]) })
	return out
}

// thrText is the thresholds the rep was scored with, following score's config logic.
func thrText(verdicts map[string]any) string {
	thr := obj(verdicts["thresholds"])
	vals := []any{thr["critical"], thr["low"], thr["medium"]}
	if pyjson.Truthy(verdicts["thresholds_assumed"]) {
		parts := make([]string, 0, len(vals))
		for _, v := range vals {
			parts = append(parts, fmtThr(v))
		}
		return "assumed " + strings.Join(parts, "/")
	}
	for _, v := range vals {
		if v == nil {
			return "unknown"
		}
	}
	return "live"
}

func fmtThr(v any) string {
	f, ok := pyjson.Num(v)
	if !ok {
		return "?"
	}
	return pyjson.FormatG(6, f)
}

func thresholdsAssumed(run map[string]any) bool {
	return run["critical_health"] == nil
}

// ruleKN is k/n summed over valid repeats: answered/triggers, num/den, up/samples.
func ruleKN(trialDir, ruleID string) (int64, int64, []any) {
	var k, n int64
	var vals []any
	entries, err := os.ReadDir(trialDir)
	if err != nil {
		return 0, 0, nil
	}
	var paths []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "rep") {
			continue
		}
		candidate := filepath.Join(trialDir, entry.Name(), "verdicts.json")
		if fileExists(candidate) {
			paths = append(paths, candidate)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		doc := obj(readJSON(path))
		rules, _ := pyjson.List(doc["rules"])
		for _, rowVal := range rules {
			row, ok := pyjson.Object(rowVal)
			if !ok || pyjson.Str(row["id"]) != ruleID {
				continue
			}
			verdict := pyjson.Str(row["verdict"])
			if verdict == "PASS" || verdict == "FAIL" {
				k += intField(row["k"])
				n += intField(row["n"])
				vals = append(vals, row["value"])
			}
		}
	}
	return k, n, vals
}

// rateishKinds are the metrics whose value is a k/n rate: Fisher's exact test applies.
var rateishKinds = map[string]bool{"ratio": true, "latency": true, "latency-share": true, "uptime": true, "uptime-share": true}

// Compare matches rows by (trial id, rule id) and returns one entry per row.
func Compare(run, base string) []any {
	runCard := obj(readJSON(filepath.Join(run, "card.json")))
	baseCard := obj(readJSON(filepath.Join(base, "card.json")))
	type key struct{ Trial, Rule string }
	runRows := map[key]map[string]any{}
	baseRows := map[key]map[string]any{}
	if rows, ok := pyjson.List(runCard["rows"]); ok {
		for _, rowVal := range rows {
			r, ok := pyjson.Object(rowVal)
			if !ok {
				continue
			}
			runRows[key{pyjson.Str(r["trial"]), pyjson.Str(r["rule"])}] = r
		}
	}
	if rows, ok := pyjson.List(baseCard["rows"]); ok {
		for _, rowVal := range rows {
			b, ok := pyjson.Object(rowVal)
			if !ok {
				continue
			}
			baseRows[key{pyjson.Str(b["trial"]), pyjson.Str(b["rule"])}] = b
		}
	}
	keys := map[key]bool{}
	for k := range runRows {
		keys[k] = true
	}
	for k := range baseRows {
		keys[k] = true
	}
	sortedKeys := make([]key, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.SliceStable(sortedKeys, func(i, j int) bool {
		if sortedKeys[i].Trial != sortedKeys[j].Trial {
			return sortedKeys[i].Trial < sortedKeys[j].Trial
		}
		return sortedKeys[i].Rule < sortedKeys[j].Rule
	})
	var out []any
	for _, key := range sortedKeys {
		r, rOK := runRows[key]
		b, bOK := baseRows[key]
		if !rOK || !bOK {
			var before, after any
			if bOK {
				before = b["verdict"]
			}
			if rOK {
				after = r["verdict"]
			}
			out = append(out, map[string]any{"trial": key.Trial, "rule": key.Rule,
				"before": before, "after": after, "note": "not run"})
			continue
		}
		kind := ""
		if s, ok := r["metric"].(string); ok && s != "" {
			kind = s
		} else if s, ok := b["metric"].(string); ok && s != "" {
			kind = s
		}
		entry := map[string]any{"trial": key.Trial, "rule": key.Rule, "before": b["verdict"],
			"after": r["verdict"], "value_before": b["value"],
			"value_after":     r["value"],
			"no_match_before": pyjson.Truthy(b["no_match"]),
			"no_match_after":  pyjson.Truthy(r["no_match"]),
			"values_before":   listOrEmpty(b["values"]),
			"values_after":    listOrEmpty(r["values"])}
		rk, rn, _ := ruleKN(filepath.Join(run, key.Trial), key.Rule)
		bk, bn, _ := ruleKN(filepath.Join(base, key.Trial), key.Rule)
		entry["k_n_before"] = fmt.Sprintf("%d/%d", bk, bn)
		entry["k_n_after"] = fmt.Sprintf("%d/%d", rk, rn)
		entry["k_before"], entry["n_before"] = bk, bn
		entry["k_after"], entry["n_after"] = rk, rn
		thinAfter := pyjson.Str(r["verdict"]) == "INCONCLUSIVE" && strings.Contains(pyjson.Str(r["reason"]), "min_reps")
		thinBefore := pyjson.Str(b["verdict"]) == "INCONCLUSIVE" && strings.Contains(pyjson.Str(b["reason"]), "min_reps")
		if thinAfter || thinBefore {
			entry["p"] = nil
			entry["note"] = "too few valid reps"
		} else if rateishKinds[kind] {
			if rn != 0 || bn != 0 {
				p := wr.FisherTwoSided(rk, rn-rk, bk, bn-bk, 1e-7)
				entry["p"] = p
				entry["note"] = wr.NoteFor(p)
			} else {
				entry["p"] = 1.0
				entry["note"] = wr.NoteFor(1.0)
			}
		} else {
			entry["p"] = nil
			vb, vok := pyjson.Float(b["value"])
			va, vok2 := pyjson.Float(r["value"])
			if vok && vok2 {
				entry["note"] = "diff " + signedG(3, va-vb)
			} else if !sameJSON(b["value"], r["value"]) {
				entry["note"] = "values differ"
			} else {
				entry["note"] = "same"
			}
		}
		out = append(out, entry)
	}
	return out
}

func listOrEmpty(v any) []any {
	if list, ok := pyjson.List(v); ok {
		return list
	}
	return []any{}
}

func sameJSON(a, b any) bool {
	ja, errA := pyjson.MarshalLine(a)
	jb, errB := pyjson.MarshalLine(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// signedG is Python's "%+.3g" % v.
func signedG(prec int, v float64) string {
	s := pyjson.FormatG(prec, v)
	if !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "+") && s != "nan" {
		s = "+" + s
	}
	return s
}

// RenderCompare is the compare table as Markdown.
func RenderCompare(rows []any) string {
	lines := []string{"| trial | rule | before | after | value before → after | p | note |",
		"|---|---|---|---|---|---|---|"}
	for _, rowVal := range rows {
		r, ok := pyjson.Object(rowVal)
		if !ok {
			continue
		}
		if pyjson.Str(r["note"]) == "not run" {
			before := pyjson.Str(r["before"])
			if !pyjson.Truthy(r["before"]) {
				before = "-"
			}
			after := pyjson.Str(r["after"])
			if !pyjson.Truthy(r["after"]) {
				after = "-"
			}
			lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s | - | - | not run |",
				pyjson.Str(r["trial"]), pyjson.Str(r["rule"]), before, after))
			continue
		}
		lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s | %s → %s | %s | %s |",
			pyjson.Str(r["trial"]), pyjson.Str(r["rule"]), pyjson.Str(r["before"]), pyjson.Str(r["after"]),
			fmtValue(r["value_before"], false), fmtValue(r["value_after"], false),
			fmtP(r["p"]), pyjson.Str(r["note"])))
	}
	return strings.Join(lines, "\n") + "\n"
}

func fmtP(p any) string {
	if p == nil {
		return "-"
	}
	f, ok := pyjson.Float(p)
	if !ok {
		return "-"
	}
	return pyjson.FormatF(4, f)
}

// reasonOf is why a repeat is INCONCLUSIVE / INVALID: the recorded reason, else the guard
// text, else the first evidence line.
func reasonOf(det map[string]any) string {
	for _, key := range []string{"invalid_reason", "reason", "guard"} {
		if v := det[key]; pyjson.Truthy(v) {
			return pyjson.Str(v)
		}
	}
	if evidence, ok := pyjson.List(det["evidence"]); ok && len(evidence) > 0 {
		return pyjson.Str(evidence[0])
	}
	return ""
}

// PRBlock is the exact block for issue-fix PRs. issue is empty for none.
func PRBlock(before, after, issue string) string {
	b := obj(readJSON(filepath.Join(before, "card.json")))
	a := obj(readJSON(filepath.Join(after, "card.json")))
	brow, arow := obj(b["run"]), obj(a["run"])
	title := "### " + naming.ToolName + ": before/after"
	if issue != "" {
		title += ", issue #" + issue
	}
	opt := func(m map[string]any, key string) string {
		if pyjson.Truthy(m[key]) {
			return pyjson.Str(m[key])
		}
		return "?"
	}
	lines := []string{title,
		fmt.Sprintf("Before: %s %s (%s) · After: %s %s (%s)",
			opt(brow, "label"), opt(brow, "tb_version"), filepath.Base(strings.TrimRight(before, "/")),
			opt(arow, "label"), opt(arow, "tb_version"), filepath.Base(strings.TrimRight(after, "/"))),
		"",
		"| trial | rule | before | after | value before → after |",
		"|---|---|---|---|---|"}
	for _, rowVal := range Compare(after, before) {
		row, ok := pyjson.Object(rowVal)
		if !ok {
			continue
		}
		beforeV := pyjson.Str(row["before"])
		if !pyjson.Truthy(row["before"]) {
			beforeV = "-"
		}
		afterV := pyjson.Str(row["after"])
		if !pyjson.Truthy(row["after"]) {
			afterV = "-"
		}
		lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s | %s → %s |",
			pyjson.Str(row["trial"]), pyjson.Str(row["rule"]), beforeV, afterV,
			fmtValue(row["value_before"], pyjson.Truthy(row["no_match_before"])),
			fmtValue(row["value_after"], pyjson.Truthy(row["no_match_after"]))))
	}
	lines = append(lines, "<details><summary>Evidence</summary>", "")
	arows := map[string]map[string]any{}
	brows := map[string]map[string]any{}
	if rows, ok := pyjson.List(a["rows"]); ok {
		for _, rowVal := range rows {
			row, ok := pyjson.Object(rowVal)
			if ok {
				arows[pyjson.Str(row["trial"])+"\x00"+pyjson.Str(row["rule"])] = row
			}
		}
	}
	if rows, ok := pyjson.List(b["rows"]); ok {
		for _, rowVal := range rows {
			row, ok := pyjson.Object(rowVal)
			if ok {
				brows[pyjson.Str(row["trial"])+"\x00"+pyjson.Str(row["rule"])] = row
			}
		}
	}
	keys := map[string]bool{}
	for k := range arows {
		keys[k] = true
	}
	for k := range brows {
		keys[k] = true
	}
	sortedKeys := make([]string, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)
	for _, k := range sortedKeys {
		// "before:" shows the BEFORE run's evidence, "after:" the AFTER run's.
		bdet, _ := pyjson.List(brows[k]["detail"])
		adet, _ := pyjson.List(arows[k]["detail"])
		source := "no source"
		if pyjson.Truthy(arows[k]["source"]) {
			source = pyjson.Str(arows[k]["source"])
		} else if pyjson.Truthy(brows[k]["source"]) {
			source = pyjson.Str(brows[k]["source"])
		}
		_, rule := splitKey(k)
		lines = append(lines, fmt.Sprintf("**%s** (%s)", rule, source))
		appendEvidence := func(tag string, details []any) {
			if len(details) > 2 {
				details = details[:2]
			}
			for _, detVal := range details {
				det, ok := pyjson.Object(detVal)
				if !ok {
					continue
				}
				evidence, _ := pyjson.List(det["evidence"])
				if len(evidence) > 3 {
					evidence = evidence[:3]
				}
				for _, ev := range evidence {
					lines = append(lines, fmt.Sprintf("- %s: %s", tag, pyjson.Str(ev)))
				}
			}
		}
		appendEvidence("before", bdet)
		appendEvidence("after", adet)
		// Every INCONCLUSIVE and every INVALID prints its real reason.
		for _, side := range []struct {
			Tag     string
			Row     map[string]any
			Details []any
		}{{"before", brows[k], bdet}, {"after", arows[k], adet}} {
			for _, detVal := range side.Details {
				det, ok := pyjson.Object(detVal)
				if !ok {
					continue
				}
				verdict := pyjson.Str(det["verdict"])
				if verdict != "INCONCLUSIVE" && verdict != "INVALID" {
					continue
				}
				if reason := reasonOf(det); reason != "" {
					rep := pyjson.Str(det["rep"])
					if rep == "" {
						rep = "?"
					}
					lines = append(lines, fmt.Sprintf("- %s %s: %s (%s)", side.Tag, verdict, reason, rep))
				}
			}
			trialReason := pyjson.Str(side.Row["reason"])
			if pyjson.Str(side.Row["verdict"]) == "INCONCLUSIVE" && strings.Contains(trialReason, "min_reps") {
				lines = append(lines, fmt.Sprintf("- %s INCONCLUSIVE (trial): %s", side.Tag, trialReason))
			}
		}
	}
	var setupBits []string
	setupPaths := globTwoLevel(filepath.Join(after, "*", "rep*", "setup.json"))
	for _, path := range setupPaths {
		doc := obj(readJSON(path))
		checks, _ := pyjson.List(doc["checks"])
		for _, checkVal := range checks {
			check, ok := pyjson.Object(checkVal)
			if !ok {
				continue
			}
			id := pyjson.Str(check["id"])
			if pyjson.Truthy(check["ok"]) && (id == "player_stance" || id == "player_talents" || id == "bot_spec") {
				setupBits = append(setupBits, fmt.Sprintf("%s=%s", id, pyjson.Str(check["value"])))
				break
			}
		}
	}
	if len(setupBits) > 6 {
		setupBits = setupBits[:6]
	}
	proof := "n/a"
	if len(setupBits) > 0 {
		proof = strings.Join(setupBits, " · ")
	}
	lines = append(lines, fmt.Sprintf("Setup proof (after): %s", proof), "</details>")
	return strings.Join(lines, "\n") + "\n"
}

func splitKey(k string) (string, string) {
	idx := strings.Index(k, "\x00")
	if idx < 0 {
		return k, ""
	}
	return k[:idx], k[idx+1:]
}

// globTwoLevel expands "<dir>/*/rep*/setup.json" (one trial level, then the reps), sorted.
func globTwoLevel(pattern string) []string {
	trials, err := os.ReadDir(filepath.Dir(strings.Split(pattern, "*")[0]))
	if err != nil {
		return nil
	}
	var out []string
	for _, trialEntry := range trials {
		if !trialEntry.IsDir() {
			continue
		}
		trialDir := filepath.Join(filepath.Dir(strings.Split(pattern, "*")[0]), trialEntry.Name())
		reps, err := os.ReadDir(trialDir)
		if err != nil {
			continue
		}
		for _, repEntry := range reps {
			if !repEntry.IsDir() || !strings.HasPrefix(repEntry.Name(), "rep") {
				continue
			}
			candidate := filepath.Join(trialDir, repEntry.Name(), "setup.json")
			if fileExists(candidate) {
				out = append(out, candidate)
			}
		}
	}
	sort.Strings(out)
	return out
}

// CardMD returns a run's card.md.
func CardMD(runDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "card.md"))
	return string(data), err
}

// CardJSON returns a run's card.json.
func CardJSON(runDir string) map[string]any {
	return obj(readJSON(filepath.Join(runDir, "card.json")))
}

// orEmpty is Python's `value or ""`.
func orEmpty(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}
