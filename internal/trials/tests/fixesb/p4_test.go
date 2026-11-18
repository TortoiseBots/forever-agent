package fixesb

// Translated from tests/test_bench_fix_p4.py: the marked mob is taunted into combat before
// the fight counts, opportunity knows `has_aura`, and zero-hit count rules name the other
// targets instead of printing "0 s".
//
// Not translated (the live runner is not part of the Go scoring port):
//   - EngageMarkedTest.* (needs trl.live situations.Situation / FakeAgent and its calls log)

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

// GUIDs as agent.norm_guid leaves them.
const (
	p4GUIDA  = "0xf130000000000c01"
	p4GUIDB  = "0xf130000000000c02"
	p4GUIDC  = "0xf130000000000c03"
	p4Marked = p4GUIDB
	p4Player = "0x0000000000000001"
	p4BOT    = "0x0000000000000002"
)

// p4LoadRespect reads the embedded dps-mage-cc-respect trial.
func p4LoadRespect(t *testing.T) map[string]any {
	t.Helper()
	trial, err := scenario.LoadTrial("dps/dps-mage-cc-respect.json")
	if err != nil {
		t.Fatalf("load dps-mage-cc-respect: %v", err)
	}
	return trial
}

// p4Rule returns the rule with id from the trial.
func p4Rule(t *testing.T, trial map[string]any, id string) map[string]any {
	t.Helper()
	for _, rv := range list(t, trial["rules"]) {
		r := obj(t, rv)
		if r["id"] == id {
			return r
		}
	}
	t.Fatalf("no rule %s", id)
	return nil
}

// p4Sample is the sampler row the Python helper tt_Sample builds.
func p4Sample(at float64, guid string, auras ...int64) rules.Row {
	list := make([]any, 0, len(auras))
	for _, a := range auras {
		list = append(list, a)
	}
	return rules.Row{"t": at, "guid": guid, "hp": int64(600), "max_hp": int64(1000),
		"auras": list, "in_combat": true}
}

// p4Series is one sample per second 0..secs with the aura carried from t=10 to t=40.
func p4Series(secs int, aura int64) []rules.Row {
	var out []rules.Row
	for at := range secs + 1 {
		var auras []int64
		if aura != 0 && 10 <= at && at <= 40 {
			auras = []int64{aura}
		}
		out = append(out, p4Sample(float64(at), p4Marked, auras...))
	}
	return out
}

// p4RowsAny re-types rows as the []any writeJSONL wants.
func p4RowsAny(rows []rules.Row) []any {
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out
}

func TestHasAuraOpportunityTest_HasAuraOpportunity(t *testing.T) {
	trial := map[string]any{"opportunity": map[string]any{"unit": "$marked",
		"has_aura": []any{int64(12825)}}}
	samples := p4Series(60, 12825)
	ctx := &rules.Ctx{Guids: map[string]any{"marked": p4Marked}, Thresholds: map[string]any{}}
	opp := rules.OpportunitySeconds(trial, samples, nil, ctx)
	// Python: assertAlmostEqual(opp, 30.0, delta=2.0)
	if diff := opp - 30.0; diff > 2.0 || diff < -2.0 {
		t.Fatalf("opportunity = %v, want 30.0 +/- 2.0", opp)
	}
}

func TestHasAuraOpportunityTest_RespectWithZeroHitsPassesAndMissingAuraIsInconclusive(t *testing.T) {
	trial := p4LoadRespect(t)
	rule := p4Rule(t, trial, "DPS-MAG-CC-RESPECT")
	rule["min_opportunity_s"] = int64(10)
	trial["min_valid_s"] = int64(30)
	samples := p4Series(60, 12825)
	res := rules.Evaluate(rule, nil, samples, nil, &rules.Ctx{
		Guids: map[string]any{"marked": p4Marked}, Thresholds: map[string]any{}, Trial: trial})
	if res.Verdict != "PASS" {
		t.Fatalf("verdict %s, want PASS (%+v)", res.Verdict, res)
	}

	// The same rep with no aura: nothing to respect, so no opportunity either.
	bare := p4Series(60, 0)
	rep := filepath.Join(t.TempDir(), "rep1")
	if err := os.MkdirAll(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{
		"ok":     true,
		"guids":  map[string]any{"player": "p", "bot": "b", "marked": p4Marked},
		"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)},
	})
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), p4RowsAny(bare))
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), nil)
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), []any{
		map[string]any{"kind": "fight_start", "t": int64(0)},
		map[string]any{"kind": "fight_end", "t": int64(60), "ended": "duration"},
	})
	out := score.ScoreRep(rep, trial, nil)
	var got map[string]any
	for _, rv := range list(t, out["rules"]) {
		r := obj(t, rv)
		if r["id"] == "DPS-MAG-CC-RESPECT" {
			got = r
		}
	}
	if got == nil {
		t.Fatalf("no DPS-MAG-CC-RESPECT verdict in %v", out["rules"])
	}
	if got["verdict"] != "INCONCLUSIVE" {
		t.Fatalf("verdict %v, want INCONCLUSIVE (%v)", got["verdict"], got)
	}
	if reason := pyjson.Str(got["reason"]); !strings.Contains(reason, "opportunity 0 s < 10 s") {
		t.Fatalf("reason %q does not name the missing opportunity (%v)", reason, got)
	}
}

func TestZeroHitEvidenceTest_ZeroHitCountEvidenceNamesOtherTargets(t *testing.T) {
	trial := p4LoadRespect(t)
	// a trial WITHOUT an opportunity block: no "opportunity 0 s" anywhere
	delete(trial, "opportunity")
	rule := p4Rule(t, trial, "DPS-MAG-CC-POLYMORPH")
	other := "0xf130000000000c99"
	var rows []rules.Row
	for i := range 2 {
		rows = append(rows, rules.Row{
			"t": float64(10 + i), "kind": "cast", "src": "Botty", "dst": "Other Mob",
			"src_guid": p4BOT, "dst_guid": other, "src_unit": "$bot", "dst_unit": "$mob",
			"spell_id": int64(118), "spell": "Polymorph", "amount": int64(0),
			"src_role": "bot", "dst_role": "mob", "origin": "ta",
		})
	}
	var samples []rules.Row
	for at := range 60 {
		samples = append(samples, p4Sample(float64(at), other))
	}
	ctx := &rules.Ctx{
		Guids: map[string]any{"player": p4Player, "bot": p4BOT, "marked": p4Marked,
			"mobs": []any{other}},
		Thresholds: map[string]any{}, Trial: trial}
	res := rules.Evaluate(rule, rows, samples, nil, ctx)
	if res.Verdict != "FAIL" {
		t.Fatalf("verdict %s, want FAIL (%+v)", res.Verdict, res)
	}
	ev := strings.Join(res.Evidence, " ")
	if !strings.Contains(ev, "2 same-spell casts on other targets") {
		t.Fatalf("evidence %q does not count the other-target casts", ev)
	}
	if strings.Contains(ev, "opportunity 0 s") {
		t.Fatalf("evidence %q prints a zero opportunity on a trial without an opportunity block", ev)
	}
}
