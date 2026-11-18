package pooled

// Translated from tests/test_json_safe.py: a no-match `first` rule writes strict JSON.
//
// Before the fix, a `first` metric with no matching event stored float("inf"),
// which json.dump wrote as the token `Infinity` (invalid JSON). Now the stored
// value is null with no_match true; pass_if still decides on +inf internally.
// Every JSON writer in trl/ uses allow_nan=False, so a NaN/inf fails loudly.
//
// Failing case this guards: a `first` rule "< 30" with no Power Word: Shield
// row wrote value Infinity into verdicts.json and card.json.

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/report"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

const (
	jsfPWS    = "Power Word: Shield"
	jsfPlayer = "0x0000000000000001"
	jsfBot    = "0x0000000000000002"
)

// jsfStrictLoads decodes JSON while rejecting the non-standard Infinity/NaN tokens, like
// json.loads(text, parse_constant=_reject_constant).
func jsfStrictLoads(t *testing.T, text string) any {
	t.Helper()
	for _, token := range []string{"Infinity", "-Infinity", "NaN"} {
		if strings.Contains(text, token) {
			t.Fatalf("non-standard JSON token %q in %q", token, text)
		}
	}
	doc, err := pyjson.Unmarshal([]byte(text))
	if err != nil {
		t.Fatalf("strict decode: %v", err)
	}
	return doc
}

func jsfFirstRule(id, passIf string) map[string]any {
	return map[string]any{
		"id": id, "desc": "first shield", "source": "test", "expect_today": "?",
		"why_expect": "test",
		"check":      map[string]any{"metric": "first", "where": map[string]any{"kind": "cast", "spell": jsfPWS}},
		"pass_if":    passIf, "min_opportunity_s": int64(0), "min_n": int64(0),
	}
}

func jsfTrial(ruleList []any) map[string]any {
	return map[string]any{
		"id": "json-safe-trial", "title": "json safe", "group": "t",
		"class": "priest", "spec": "holy", "player": map[string]any{}, "bot": map[string]any{},
		"situation": map[string]any{}, "setup_checks": []any{}, "duration_s": int64(60),
		"repeats": int64(1), "min_valid_s": int64(0), "opportunity": map[string]any{},
		"rules": ruleList,
	}
}

func jsfCtx() *rules.Ctx {
	return &rules.Ctx{
		Guids:             map[string]any{},
		Thresholds:        map[string]any{},
		ThresholdsAssumed: false,
		Trial:             map[string]any{},
	}
}

// jsfRenew is a cast of another spell: the `first` where (spell = Power Word: Shield)
// matches nothing in this rep.
func jsfRenew(t float64) map[string]any {
	return map[string]any{
		"t": t, "wall": nil, "kind": "cast", "src": "bot", "dst": "player",
		"src_guid": jsfBot, "dst_guid": jsfPlayer, "src_unit": "$bot",
		"dst_unit": "$player", "spell_id": int64(139), "spell": "Renew",
		"amount": nil, "dst_hp_pct": 50.0, "src_role": "bot",
		"dst_role": "player", "origin": "ta",
	}
}

// jsfResultDoc is the Python-shaped JSON mapping of a rules.Result (pyjson cannot encode the
// Go struct): the same keys score.ScoreRep writes into verdicts.json.
func jsfResultDoc(res rules.Result) map[string]any {
	evidence := res.Evidence
	if evidence == nil {
		evidence = []string{}
	}
	return map[string]any{
		"value": res.Value, "k": res.K, "n": res.N, "verdict": res.Verdict,
		"evidence": evidence, "guard": res.Guard, "reason": res.Reason,
		"no_match": res.NoMatch,
	}
}

func TestNoMatchFirstRuleTest_ValueNullNoMatchTrueAndVerdictKept(t *testing.T) {
	for _, tc := range []struct {
		passIf  string
		verdict string
	}{{"< 30", "FAIL"}, {"> 30", "PASS"}} {
		t.Run(tc.passIf, func(t *testing.T) {
			res := rules.Evaluate(jsfFirstRule("F", tc.passIf), nil, nil, nil, jsfCtx())
			if res.Value != nil {
				t.Fatalf("value = %v, want nil", res.Value)
			}
			if !res.NoMatch {
				t.Fatalf("no_match = false, want true")
			}
			if res.Verdict != tc.verdict {
				t.Fatalf("verdict = %q, want %q", res.Verdict, tc.verdict)
			}
			if len(res.Evidence) != 1 || res.Evidence[0] != "no match" {
				t.Fatalf("evidence = %v, want [\"no match\"]", res.Evidence)
			}
		})
	}
}

func TestNoMatchFirstRuleTest_MatchStillHasNumericValue(t *testing.T) {
	shield := map[string]any{
		"t": 12.0, "kind": "cast", "src": "bot", "dst": "player",
		"src_guid": jsfBot, "dst_guid": jsfPlayer, "src_unit": "$bot",
		"dst_unit": "$player", "spell_id": int64(6065), "spell": jsfPWS,
		"dst_hp_pct": 80.0, "origin": "ta",
	}
	res := rules.Evaluate(jsfFirstRule("F", "< 30"), []rules.Row{shield}, nil, nil, jsfCtx())
	value, ok := pyjson.Float(res.Value)
	if !ok || value != 12.0 {
		t.Fatalf("value = %v, want 12.0", res.Value)
	}
	if res.NoMatch {
		t.Fatalf("no_match = true, want absent/false")
	}
	if res.Verdict != "PASS" {
		t.Fatalf("verdict = %q, want PASS", res.Verdict)
	}
}

func TestNoMatchFirstRuleTest_ResultDumpsStrictly(t *testing.T) {
	// Python dumps the result dict; the Go Result is a struct, so dump the same JSON mapping
	// ScoreRep writes.
	res := rules.Evaluate(jsfFirstRule("F", "< 30"), nil, nil, nil, jsfCtx())
	text, err := pyjson.Marshal(jsfResultDoc(res))
	if err != nil {
		t.Fatalf("dump result: %v", err)
	}
	doc, _ := pyjson.Object(jsfStrictLoads(t, string(text)))
	if doc["value"] != nil {
		t.Fatalf("value = %v, want nil", doc["value"])
	}
}

// jsfRep writes a rep with one non-matching cast, like _rep().
func jsfRep(t *testing.T, runDir, trialID string) string {
	t.Helper()
	rep := filepath.Join(runDir, trialID, "rep1")
	if err := os.MkdirAll(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{
		"ok": true, "checks": []any{}, "guids": map[string]any{},
	})
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), []any{jsfRenew(5.0)})
	return rep
}

func TestNoMatchWritesStrictJsonTest_VerdictsAndTrialJsonAreStrict(t *testing.T) {
	tmp := t.TempDir()
	trial := jsfTrial([]any{jsfFirstRule("F-LT", "< 30"), jsfFirstRule("F-GT", "> 30")})
	rep := jsfRep(t, tmp, pyjson.Str(trial["id"]))
	out := score.ScoreRep(rep, trial, nil)
	byID := map[string]map[string]any{}
	ruleRows, _ := pyjson.List(out["rules"])
	for _, rv := range ruleRows {
		row, _ := pyjson.Object(rv)
		byID[pyjson.Str(row["id"])] = row
	}
	if got := pyjson.Str(byID["F-LT"]["verdict"]); got != "FAIL" {
		t.Fatalf("F-LT verdict = %q, want FAIL", got)
	}
	if got := pyjson.Str(byID["F-GT"]["verdict"]); got != "PASS" {
		t.Fatalf("F-GT verdict = %q, want PASS", got)
	}

	raw, err := os.ReadFile(filepath.Join(rep, "verdicts.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := pyjson.Object(jsfStrictLoads(t, string(raw)))
	rulesByID := map[string]map[string]any{}
	docRules, _ := pyjson.List(doc["rules"])
	for _, rv := range docRules {
		row, _ := pyjson.Object(rv)
		rulesByID[pyjson.Str(row["id"])] = row
	}
	for _, tc := range []struct{ id, verdict string }{{"F-LT", "FAIL"}, {"F-GT", "PASS"}} {
		row := rulesByID[tc.id]
		if row["value"] != nil {
			t.Fatalf("%s value = %v, want nil", tc.id, row["value"])
		}
		if !pyjson.Truthy(row["no_match"]) {
			t.Fatalf("%s no_match = false, want true", tc.id)
		}
		if got := pyjson.Str(row["verdict"]); got != tc.verdict {
			t.Fatalf("%s verdict = %q, want %q", tc.id, got, tc.verdict)
		}
	}

	trialDir := filepath.Join(tmp, pyjson.Str(trial["id"]))
	score.ScoreTrial(trialDir, trial)
	raw, err = os.ReadFile(filepath.Join(trialDir, "trial.json"))
	if err != nil {
		t.Fatal(err)
	}
	tdoc, _ := pyjson.Object(jsfStrictLoads(t, string(raw)))
	verdicts, _ := pyjson.List(tdoc["verdicts"])
	for _, rv := range verdicts {
		row, _ := pyjson.Object(rv)
		detail, _ := pyjson.List(row["detail"])
		first, _ := pyjson.Object(detail[0])
		if first["value"] != nil {
			t.Fatalf("%s detail value = %v, want nil", row["id"], first["value"])
		}
		if !pyjson.Truthy(first["no_match"]) {
			t.Fatalf("%s detail no_match = false, want true", row["id"])
		}
		want := "PASS"
		if pyjson.Str(row["id"]) == "F-LT" {
			want = "FAIL"
		}
		if got := pyjson.Str(row["verdict"]); got != want {
			t.Fatalf("%s verdict = %q, want %q", row["id"], got, want)
		}
	}
}

func TestNoMatchWritesStrictJsonTest_CardIsStrictAndSaysNoMatch(t *testing.T) {
	tmp := t.TempDir()
	trial := jsfTrial([]any{jsfFirstRule("F-LT", "< 30")})
	jsfRep(t, tmp, pyjson.Str(trial["id"]))
	trialDir := filepath.Join(tmp, pyjson.Str(trial["id"]))
	runTrials := score.ScoreTrial(trialDir, trial)
	if _, err := report.WriteCard(tmp, []map[string]any{runTrials}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(tmp, "card.json"))
	if err != nil {
		t.Fatal(err)
	}
	card, _ := pyjson.Object(jsfStrictLoads(t, string(raw)))
	rows, _ := pyjson.List(card["rows"])
	row, _ := pyjson.Object(rows[0])
	if row["value"] != nil {
		t.Fatalf("card value = %v, want nil", row["value"])
	}
	if !pyjson.Truthy(row["no_match"]) {
		t.Fatalf("card no_match = false, want true")
	}

	mdBytes, err := os.ReadFile(filepath.Join(tmp, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	md := string(mdBytes)
	if !strings.Contains(md, "no match") {
		t.Fatalf("card.md has no 'no match': %s", md)
	}
	if strings.Contains(md, "Infinity") {
		t.Fatalf("card.md contains Infinity: %s", md)
	}
	parts := strings.SplitN(md, "| trial |", 2)
	table := parts[len(parts)-1]
	if strings.Contains(table, "inf") {
		t.Fatalf("card.md table contains 'inf': %s", table)
	}
}

func TestNoMatchWritesStrictJsonTest_AggValueAllNoMatchIsNoneNotInf(t *testing.T) {
	details := []any{map[string]any{"verdict": "FAIL", "value": nil, "no_match": true}}
	agg, vals := report.AggValue(details, "first")
	if agg != nil {
		t.Fatalf("agg = %v, want nil", agg)
	}
	if len(vals) != 0 {
		t.Fatalf("vals = %v, want []", vals)
	}
}

func TestAllowNanGuardTest_NanAndInfFailLoudlyOnWrite(t *testing.T) {
	tmp := t.TempDir()
	for _, bad := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		path := filepath.Join(tmp, "x.json")
		if err := pyjson.Dump(path, map[string]any{"value": bad}); err == nil {
			t.Fatalf("value %v: expected a write error", bad)
		}
	}
}
