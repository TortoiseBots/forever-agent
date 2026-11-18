package parityfix

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

func writeJSON(t *testing.T, path string, doc any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := pyjson.Dump(path, doc); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeJSONL(t *testing.T, path string, rows []any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for _, row := range rows {
		if err := pyjson.DumpLine(&b, row); err != nil {
			t.Fatalf("encode row for %s: %v", path, err)
		}
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeText(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func repDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "setup.json"), map[string]any{
		"ok": true, "guids": map[string]any{"player": "0x1", "bot": "0x2"},
	})
	writeJSONL(t, filepath.Join(dir, "marks.jsonl"), []any{
		map[string]any{"kind": "fight_start", "wall": "2026-01-01T00:00:00"},
		map[string]any{"kind": "fight_end", "wall": "2026-01-01T00:00:10", "ended": "boss dead"},
	})
	return dir
}

func castRow(t float64) map[string]any {
	return map[string]any{
		"t": t, "kind": "cast", "spell_id": int64(17),
		"src_guid": "0x2", "dst_guid": "0x1", "src": "Bot", "dst": "Player",
		"src_unit": "$bot", "dst_unit": "$player", "src_role": "bot", "dst_role": "player",
	}
}

func testRule(metric string, check map[string]any, passIf string) map[string]any {
	full := map[string]any{"metric": metric}
	for k, v := range check {
		full[k] = v
	}
	return map[string]any{"id": "R", "check": full, "pass_if": passIf,
		"source": "parity", "desc": "R"}
}

func testTrial(rule map[string]any) map[string]any {
	return map[string]any{"id": "t1", "min_valid_s": int64(0), "rules": []any{rule}}
}

func ruleRow(t *testing.T, doc map[string]any, i int) map[string]any {
	t.Helper()
	list, _ := pyjson.List(doc["rules"])
	if i >= len(list) {
		t.Fatalf("no rule %d in %v", i, doc)
	}
	return pyjson.ObjectOrEmpty(list[i])
}

// B1: Python compares rows by object identity (`cand is not trig`), so two equal rows are two
// rows. Python oracle: rules.evaluate(...) -> INCONCLUSIVE, value None, k 0, n 0.
func TestB1TwinRowsAreTwoRows(t *testing.T) {
	check := map[string]any{
		"metric":   "latency",
		"trigger":  map[string]any{"kind": "cast", "spell_id": int64(17)},
		"response": map[string]any{"kind": "cast", "spell_id": int64(17)},
		"within_s": int64(2), "debounce_responder_s": int64(10),
	}
	rule := testRule("latency", check, ">= 1")
	row := map[string]any{"t": 5.0, "kind": "cast", "spell_id": int64(17),
		"src_guid": "0x2", "dst_guid": "0x1", "src": "Bot", "dst": "Player",
		"src_unit": "$bot", "dst_unit": "$player"}
	twin := map[string]any{}
	for k, v := range row {
		twin[k] = v
	}
	res := rules.Evaluate(rule, []rules.Row{row, twin}, nil, nil, &rules.Ctx{})
	if res.Verdict != "INCONCLUSIVE" || res.Value != nil || res.K != 0 || res.N != 0 {
		t.Fatalf("twin rows: %s value=%v k=%d n=%d, want INCONCLUSIVE nil 0 0",
			res.Verdict, res.Value, res.K, res.N)
	}
	if len(res.Evidence) == 0 || res.Evidence[0] != "no triggers" {
		t.Fatalf("evidence = %v, want [no triggers]", res.Evidence)
	}
}

// B2: Python's int(found["n"]) accepts 5.0; float("mean") of None raises TypeError.
func TestB2WalkMetricCountAndMean(t *testing.T) {
	rule := map[string]any{"id": "W", "check": map[string]any{
		"metric": "walk_metric", "name": "walk_to_target"}, "pass_if": "<= 0.5"}
	ctx := &rules.Ctx{WalkReport: map[string]any{
		"walk_to_target": map[string]any{"k": int64(2), "n": 5.0},
	}}
	res := rules.Evaluate(rule, nil, nil, nil, ctx)
	if res.Err != nil {
		t.Fatalf("float n: %v", res.Err)
	}
	// Python: PASS, value 0.4, n 5 (int(5.0) == 5).
	if res.Verdict != "PASS" || res.N != 5 {
		t.Fatalf("float n: %s n=%d, want PASS n=5", res.Verdict, res.N)
	}
	if value, ok := res.Value.(float64); !ok || value != 0.4 {
		t.Fatalf("float n: value = %#v, want 0.4", res.Value)
	}

	ctx.WalkReport["walk_to_target"] = map[string]any{"n": 5.0, "mean": nil}
	res = rules.Evaluate(rule, nil, nil, nil, ctx)
	if res.Err == nil {
		t.Fatalf("mean null: no error, got %s value=%v", res.Verdict, res.Value)
	}
}

// B2 (reviewer FIX FIRST): min_alive_s 0 is falsy in Python, so the 10 s floor still applies.
func TestB2MinAliveZeroMeansTenSeconds(t *testing.T) {
	rule := testRule("rate", map[string]any{"where": map[string]any{"kind": "cast"},
		"per": "mob_alive", "min_alive_s": int64(0)}, ">= 0")
	samples := []rules.Row{
		{"t": 1.0, "unit": "mob:0xabc", "hp": int64(10)},
		{"t": 2.0, "unit": "mob:0xabc", "hp": int64(10)},
		{"t": 3.0, "unit": "mob:0xabc", "hp": int64(10)},
	}
	res := rules.Evaluate(rule, []rules.Row{castRow(1)}, samples, nil, &rules.Ctx{})
	if res.Verdict != "INCONCLUSIVE" || res.Reason != "mob alive too short" {
		t.Fatalf("min_alive_s 0: %s %q, want INCONCLUSIVE mob alive too short", res.Verdict, res.Reason)
	}
	if len(res.Evidence) == 0 || !strings.Contains(res.Evidence[0], "needs 10 s") {
		t.Fatalf("evidence = %v, want the 10 s floor", res.Evidence)
	}
}

// B3: Python formats arbitrary-precision guids as 0x%016x (printed by events._norm_guid).
func TestB3GuidFormatting(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"17379390989916241921", "0xf13000067e950001"},
		{"9223372036854775808", "0x8000000000000000"},
		{"0x8000000000000000", "0x8000000000000000"},
		{"-1", "0x-000000000000001"},
		{int64(123), "0x000000000000007b"},
		{"not-a-guid", "not-a-guid"},
		{"  0XABC  ", "0x0000000000000abc"},
		{int64(0), ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := events.NormGUID(c.in); got != c.want {
			t.Errorf("NormGUID(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// B5: a non-object JSONL line is a Python AttributeError, not a row to drop.
func TestB5NonObjectJSONLLineIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeText(t, path, `{"t": 1}`, `null`)
	if _, err := events.ReadJSONL(path); err == nil {
		t.Fatal("a null line must be an error")
	} else if !strings.Contains(err.Error(), path+":2") {
		t.Fatalf("error = %v, want the file and line 2", err)
	}

	dir := repDir(t)
	writeText(t, filepath.Join(dir, "events.jsonl"), `null`)
	if _, err := score.ScoreRepE(dir, testTrial(testRule("count",
		map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")), nil); err == nil {
		t.Fatal("scoring a null events line must error")
	} else if !strings.Contains(err.Error(), "events.jsonl:1") {
		t.Fatalf("error = %v, want events.jsonl:1", err)
	}
}

// B6: a list seq is unhashable in Python's dedupe key (TypeError).
func TestB6ListSeqIsAnError(t *testing.T) {
	dir := repDir(t)
	writeJSONL(t, filepath.Join(dir, "raw-ta.jsonl"), []any{
		map[string]any{"seq": []any{int64(1)}, "time": "2026-01-01T00:00:05",
			"kind": "spell_go", "spell_id": int64(17),
			"source": map[string]any{"name": "Bot", "guid": "0x2"},
			"target": map[string]any{"name": "Player", "guid": "0x1"}},
	})
	_, err := score.ScoreRepE(dir, testTrial(testRule("count",
		map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")), nil)
	if err == nil {
		t.Fatal("a list seq must be an error")
	}
	if !strings.Contains(err.Error(), "raw-ta.jsonl:1") || !strings.Contains(err.Error(), "unhashable seq") {
		t.Fatalf("error = %v, want raw-ta.jsonl:1 unhashable seq", err)
	}
}

// Reviewer FIX FIRST: a rule-level min_n 0 must not mask check.min_n. Python:
// `rule.get("min_n") or check.get("min_n", 0)`.
func TestRuleMinNZeroDoesNotMaskCheckMinN(t *testing.T) {
	rule := testRule("count", map[string]any{"where": map[string]any{"kind": "cast"},
		"min_n": int64(2)}, ">= 1")
	rule["min_n"] = int64(0)
	dir := repDir(t)
	writeJSONL(t, filepath.Join(dir, "events.jsonl"), []any{castRow(1)})
	doc, err := score.ScoreRepE(dir, testTrial(rule), nil)
	if err != nil {
		t.Fatal(err)
	}
	row := ruleRow(t, doc, 0)
	if row["verdict"] != "INCONCLUSIVE" {
		t.Fatalf("verdict = %v, want INCONCLUSIVE (n 1 < min_n 2)", row["verdict"])
	}
	if reason := pyjson.Str(row["reason"]); reason != "n 1 < min_n 2" {
		t.Fatalf("reason = %q, want n 1 < min_n 2", reason)
	}
}

// Reviewer FIX FIRST: Python's `str(value or "").lower()` of a missing guid is "", not "none":
// a sample with no guid is keyed "" and is the sample a row with no src_guid reads.
// Python oracle: rules.evaluate(rule, [row], [sample], [], ctx) -> PASS, k 1.
func TestMissingGuidSampleIsKeyedEmpty(t *testing.T) {
	rule := testRule("count", map[string]any{"where": map[string]any{"kind": "cast", "src_hp_le": int64(50)}}, ">= 1")
	row := castRow(1)
	row["src_guid"] = nil
	samples := []rules.Row{{"t": 0.5, "hp": int64(10), "max_hp": int64(100)}}
	res := rules.Evaluate(rule, []rules.Row{row}, samples, nil, &rules.Ctx{Guids: map[string]any{}})
	if res.Verdict != "PASS" || res.K != 1 {
		t.Fatalf("missing guid sample: %s k=%d, want PASS 1", res.Verdict, res.K)
	}
}
