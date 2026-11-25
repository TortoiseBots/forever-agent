package trialscore

// Translated from the R3 section of tests/test_trials.py: kind-only-where-when-used,
// event triggers, scored windows, thresholds, strict validation, selectors, PR-block reasons,
// pooled card values, survivor-kill cases, window/opportunity.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/data"
	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/report"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
	"github.com/pjerra/forever-agent/internal/trials/spells"
)

// --- R3H1: `kind` is required only where the dict is used ---------------------------

func tcRuleWith(check map[string]any, over map[string]any) map[string]any {
	rule := tcRule(check)
	for k, v := range over {
		rule[k] = v
	}
	return rule
}

func tcVErr(rule map[string]any, trialOver map[string]any) []string {
	over := map[string]any{"rules": []any{rule}}
	for k, v := range trialOver {
		over[k] = v
	}
	return scenario.Validate(tcTrialWith(over))
}

func TestR3H1_LatencyCrossRuleWithoutWhereValidates(t *testing.T) {
	rule := tcRule(map[string]any{"metric": "latency",
		"trigger":  map[string]any{"kind": "cross", "unit": "$player", "below": int64(50)},
		"response": map[string]any{"kind": "heal", "src": "$bot", "dst": "$player"},
		"within_s": int64(4)})
	assertEqual(t, []string{}, tcVErr(rule, nil))
}

func TestR3H1_UptimeRuleWithoutWhereValidates(t *testing.T) {
	rule := tcRule(map[string]any{"metric": "uptime", "unit": "$bot", "aura": []any{int64(588)}})
	assertEqual(t, []string{}, tcVErr(rule, nil))
}

func TestR3H1_WalkMetricRuleWithoutWhereValidates(t *testing.T) {
	rule := tcRule(map[string]any{"metric": "walk_metric", "name": "m"})
	assertEqual(t, []string{}, tcVErr(rule, nil))
}

func TestR3H1_LegacyWhereWithoutKindIsFineOnUnusedMetrics(t *testing.T) {
	rule := tcRule(map[string]any{"metric": "latency",
		"trigger":  map[string]any{"kind": "damage", "src": "$mob", "dst": "$player"},
		"response": map[string]any{"kind": "cast", "src": "$bot"},
		"within_s": int64(4), "where": map[string]any{"trigger_debounce_s": int64(5),
			"response_dst": "$trigger.src"}})
	assertEqual(t, []string{}, tcVErr(rule, nil))
	up := tcRule(map[string]any{"metric": "uptime", "unit": "$bot", "aura": []any{int64(588)},
		"where": map[string]any{"dst_has_aura": []any{int64(588)}}})
	assertEqual(t, []string{}, tcVErr(up, nil))
}

func TestR3H1_KindStillRequiredWhereTheDictIsUsed(t *testing.T) {
	for _, metric := range []string{"count", "first", "rate"} {
		rule := tcRule(map[string]any{"metric": metric, "where": map[string]any{"src": "$bot"}})
		if !containsStr(tcVErr(rule, nil), "needs a kind") {
			t.Fatalf("%s: no 'needs a kind'", metric)
		}
	}
	ratio := tcRule(map[string]any{"metric": "ratio", "num": map[string]any{"kind": "cast"},
		"den": map[string]any{"src": "$bot"}})
	if !containsStr(tcVErr(ratio, nil), "den needs a kind") {
		t.Fatalf("errs = %v", tcVErr(ratio, nil))
	}
	ratio2 := tcRule(map[string]any{"metric": "ratio", "num": map[string]any{"src": "$bot"},
		"den": map[string]any{"kind": "cast"}})
	if !containsStr(tcVErr(ratio2, nil), "num needs a kind") {
		t.Fatalf("errs = %v", tcVErr(ratio2, nil))
	}
	lat := tcRule(map[string]any{"metric": "latency",
		"trigger":  map[string]any{"kind": "cross", "unit": "$player", "below": int64(50)},
		"response": map[string]any{"src": "$bot"}, "within_s": int64(4)})
	if !containsStr(tcVErr(lat, nil), "response needs a kind") {
		t.Fatalf("errs = %v", tcVErr(lat, nil))
	}
}

// --- R3H2: event triggers -----------------------------------------------------------

func TestR3H2_AuraOnTriggerMatchesOnlyAuraOnRows(t *testing.T) {
	rule := tcRule(map[string]any{"metric": "latency",
		"trigger":  map[string]any{"kind": "aura_on", "dst": "$player", "spell_id": []any{int64(118)}},
		"response": map[string]any{"kind": "cast", "src": "$bot", "spell": "Dispel Magic"},
		"within_s": int64(4)})
	assertEqual(t, []string{}, tcVErr(rule, nil))
	rows := []map[string]any{
		tcEv(10.0, "aura_on", tcMob, tcPlayer, int64(118), "mob", "player", nil),
		tcEv(20.0, "cast", tcMob, tcPlayer, int64(118), "mob", "player", nil),
		tcEv(30.0, "aura_off", tcMob, tcPlayer, int64(118), "mob", "player", nil),
		tcEv(40.0, "damage", tcMob, tcPlayer, int64(118), "mob", "player", nil),
		tcEv(11.0, "cast", tcBot, tcPlayer, int64(527), "bot", "player", nil),
	}
	res := rules.Evaluate(rule, rows, nil, nil, tcLatCtx())
	assertEqual(t, []any{int64(1), int64(1)}, []any{res.K, res.N})
	assertEqual(t, "PASS", res.Verdict)
}

func TestR3H2_DamageTriggerWithSrcAndDst(t *testing.T) {
	rule := tcRuleWith(map[string]any{"metric": "latency",
		"trigger":  map[string]any{"kind": "damage", "src": "$mob", "dst": "$player"},
		"response": map[string]any{"kind": "cast", "src": "$bot", "spell": "Taunt"},
		"within_s": int64(4), "trigger_debounce_s": int64(6),
		"response_dst": "$trigger.src"}, map[string]any{"min_n": int64(2), "pass_if": ">= 0.8"})
	assertEqual(t, []string{}, tcVErr(rule, nil))
	rows := []map[string]any{
		tcEv(10.0, "damage", tcMob, tcPlayer, nil, "mob", "player", nil),
		tcEv(10.5, "damage", tcPlayer, tcMob, nil, "player", "mob", nil),    // wrong direction
		tcEv(10.7, "damage", tcMob, tcBot, nil, "mob", "bot", nil),          // wrong victim
		tcEv(11.0, "heal", tcBot, tcPlayer, int64(1), "bot", "player", nil), // wrong kind
		tcEv(12.0, "damage", tcMob2, tcPlayer, nil, "mob", "player", nil),
		tcEv(13.0, "damage", tcMob, tcPlayer, nil, "mob", "player", nil), // debounced
		tcEv(11.5, "cast", tcBot, tcMob, int64(355), "bot", "mob", nil),  // taunt on MOB only
	}
	res := rules.Evaluate(rule, rows, nil, nil, tcLatCtx())
	// Two real triggers (MOB at 10, MOB2 at 12); only MOB's was answered on MOB.
	assertEqual(t, []any{int64(1), int64(2)}, []any{res.K, res.N})
	assertEqual(t, "FAIL", res.Verdict)
}

func TestR3H2_ResponseDstTriggerDstWithEventTrigger(t *testing.T) {
	rule := tcRule(map[string]any{"metric": "latency",
		"trigger":  map[string]any{"kind": "cast_start", "src": "$mob"},
		"response": map[string]any{"kind": "cast", "src": "$bot"},
		"within_s": int64(2), "response_dst": "$trigger.src"})
	rows := []map[string]any{
		tcEv(10.0, "cast_start", tcMob, tcPlayer, int64(118), "mob", "player", nil),
		tcEv(10.5, "cast", tcBot, tcMob2, int64(72), "bot", "mob", nil), // wrong target
		tcEv(11.0, "cast", tcBot, tcMob, int64(72), "bot", "mob", nil),
	}
	res := rules.Evaluate(rule, rows, nil, nil, tcLatCtx())
	assertEqual(t, []any{int64(1), int64(1)}, []any{res.K, res.N})
}

func TestR3H2_InterruptShapeValidatesAndScores(t *testing.T) {
	rule := tcRuleWith(map[string]any{"metric": "latency",
		"trigger": map[string]any{"kind": "cast_start", "src": "$mob",
			"spell_interruptible": true},
		"response": map[string]any{"kind": "cast", "src": "$bot",
			"spell": []any{"Shield Bash", "Pummel"}},
		"within_s": 1.5, "response_dst": "$trigger.src"}, map[string]any{"min_n": int64(2)})
	assertEqual(t, []string{}, tcVErr(rule, nil))
	rows := []map[string]any{
		tcEv(10.0, "cast_start", tcMob, tcPlayer, int64(118), "mob", "player", nil), // interruptible
		tcEv(10.6, "cast", tcBot, tcMob, int64(72), "bot", "mob", nil),              // Shield Bash
		tcEv(20.0, "cast_start", tcMob, tcPlayer, int64(118), "mob", "player", nil), // unanswered
		tcEv(30.0, "cast_start", tcMob, tcPlayer, int64(17), "mob", "player", nil),  // instant
		tcEv(31.0, "cast", tcBot, tcMob, int64(72), "bot", "mob", nil),
		tcEv(40.0, "cast", tcMob, tcPlayer, int64(118), "mob", "player", nil), // a go, not a start
	}
	res := rules.Evaluate(rule, rows, nil, nil, tcLatCtx())
	assertEqual(t, []any{int64(1), int64(2)}, []any{res.K, res.N})
	assertEqual(t, "PASS", res.Verdict)
}

func TestR3H2_TriggerValidation(t *testing.T) {
	base := map[string]any{"metric": "latency", "response": map[string]any{"kind": "cast", "src": "$bot"},
		"within_s": int64(2)}
	for _, kind := range []string{"cross", "damage", "aura_on", "aura_off", "cast", "cast_start"} {
		if !scenario.IsTriggerKind(kind) {
			t.Fatalf("trigger kind %q is not in the enum", kind)
		}
	}
	badKind := cloneRow(base)
	badKind["trigger"] = map[string]any{"kind": "damge", "src": "$mob"}
	if !containsStr(tcVErr(tcRule(badKind), nil), "bad trigger kind") {
		t.Fatal("bad trigger kind not reported")
	}
	badField := cloneRow(base)
	badField["trigger"] = map[string]any{"kind": "damage", "bogus": int64(1)}
	if !containsStr(tcVErr(tcRule(badField), nil), "unknown trigger field") {
		t.Fatal("unknown trigger field not reported")
	}
	anyMob := cloneRow(base)
	anyMob["trigger"] = map[string]any{"kind": "damage", "src": "$any_mob", "dst": "$player"}
	if !containsStr(tcVErr(tcRule(anyMob), nil), "bad src alias") {
		t.Fatal("bad src alias not reported")
	}
	noKind := cloneRow(base)
	noKind["trigger"] = map[string]any{"src": "$mob"}
	if !containsStr(tcVErr(tcRule(noKind), nil), "bad trigger kind") {
		t.Fatal("bad trigger kind not reported for a missing kind")
	}
	for _, key := range []string{"spell_interruptible", "src_hp_le", "dst_hp_lt", "in_combat"} {
		if !scenario.IsTriggerField(key) {
			t.Fatalf("%q is not a trigger field", key)
		}
	}
}

// --- R3H3: the scored window --------------------------------------------------------

// tcFightOpts mirrors the Python _fight_rep keyword arguments.
type tcFightOpts struct {
	SetupS        int
	StartS        int
	EndS          int
	EndMark       bool
	EndMarkSet    bool
	Shields       []int
	LastSample    int
	ExtraSetup    map[string]any
	Pet           bool
	LastSampleSet bool
}

func tcWall(sec int) string { return fmt.Sprintf("2026-01-01T00:00:%02d.000+00:00", sec) }

func tcFightRep(t *testing.T, root string, opts tcFightOpts) string {
	t.Helper()
	endMark := true
	if opts.EndMarkSet {
		endMark = opts.EndMark
	}
	shields := opts.Shields
	if shields == nil {
		shields = []int{10, 27}
	}
	last := opts.LastSample
	if opts.LastSample == 0 {
		last = 44
	}
	trialID := "priest-holy-pws-def-arms"
	rep := filepath.Join(root, trialID, "rep1")
	if err := os.MkdirAll(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	setup := map[string]any{"ok": true, "guids": map[string]any{"player": tcPlayer, "bot": tcBot,
		"pet": tcPet, "mobs": []any{tcMob}},
		"checks": []any{map[string]any{"id": "config_read", "ok": true}},
		"config": map[string]any{"ok": true, "critical": int64(20), "low": int64(50), "medium": int64(70)}}
	for k, v := range opts.ExtraSetup {
		setup[k] = v
	}
	writeJSON(t, filepath.Join(rep, "setup.json"), setup)
	writeJSON(t, filepath.Join(root, "RUN.json"), map[string]any{"trials": []any{trialID}})
	marks := []any{}
	addMark := func(kind string, sec int) {
		row := map[string]any{"kind": kind, "wall": tcWall(sec), "t": float64(sec)}
		if kind == "fight_end" {
			row["ended"] = "disconnected"
		}
		marks = append(marks, row)
	}
	addMark("setup_ok", opts.SetupS)
	addMark("fight_start", opts.StartS)
	if endMark {
		addMark("fight_end", opts.EndS)
	}
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), marks)
	var samples []any
	for sec := 0; sec <= last; sec++ {
		guids := []string{tcPlayer, tcBot}
		if opts.Pet && sec < 2 {
			guids = append(guids, tcPet)
		}
		for _, g := range guids {
			samples = append(samples, map[string]any{"t": float64(sec), "wall": tcWall(sec), "guid": g,
				"hp": int64(600), "max_hp": int64(1000), "auras": []any{}, "in_combat": true})
		}
	}
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), samples)
	var raws []any
	for i, sec := range shields {
		raws = append(raws, map[string]any{"seq": int64(i + 1), "time": tcWall(sec), "kind": "spell_go",
			"source": map[string]any{"name": "b", "guid": tcBot},
			"target": map[string]any{"name": "p", "guid": tcPlayer}, "spell_id": int64(17)})
	}
	writeJSONL(t, filepath.Join(rep, "raw-ta.jsonl"), raws)
	return rep
}

func tcShortWith(over map[string]any) map[string]any {
	trial := tcTrial()
	for k, v := range over {
		trial[k] = v
	}
	tcObj(tcRows(trial)[0])["min_opportunity_s"] = int64(0)
	return trial
}

func TestR3H3_ShortFightIsCutShortNotScoredOnWholeCapture(t *testing.T) {
	rep := tcFightRep(t, t.TempDir(), tcFightOpts{StartS: 25, EndS: 30, LastSample: 44})
	out := score.ScoreRep(rep, tcTrialWith(map[string]any{"min_valid_s": int64(30)}), nil)
	assertAlmostEqual(t, 5.0, num(t, out["scored_s"]), 7)
	for _, rowVal := range tcList(out["rules"]) {
		assertEqual(t, "INVALID", tcObj(rowVal)["verdict"])
	}
	reason := pyjson.Str(out["invalid_reason"])
	if !strings.Contains(reason, "cut short") || !strings.Contains(reason, "disconnected") {
		t.Fatalf("invalid_reason = %q", reason)
	}
}

func TestR3H3_25To30sFightWithMinValid30IsInvalid(t *testing.T) {
	rep := tcFightRep(t, t.TempDir(), tcFightOpts{SetupS: 0, StartS: 5, EndS: 32, LastSample: 60})
	out := score.ScoreRep(rep, tcTrialWith(map[string]any{"min_valid_s": int64(30)}), nil)
	assertAlmostEqual(t, 27.0, num(t, out["scored_s"]), 7)
	if !strings.Contains(pyjson.Str(out["invalid_reason"]), "cut short") {
		t.Fatalf("invalid_reason = %v", out["invalid_reason"])
	}
}

func TestR3H3_PreFightShieldIsNotCounted(t *testing.T) {
	rep := tcFightRep(t, t.TempDir(), tcFightOpts{StartS: 25, EndS: 30, LastSample: 44})
	out := score.ScoreRep(rep, tcShortWith(map[string]any{"min_valid_s": int64(3)}), nil)
	rule := tcRow(t, out, 0)
	assertEqual(t, "FAIL", rule["verdict"])
	assertEqual(t, int64(1), rule["value"])
	// With only the pre-fight shield nothing is counted.
	rep2 := tcFightRep(t, t.TempDir(), tcFightOpts{StartS: 25, EndS: 30, LastSample: 44, Shields: []int{10}})
	out2 := score.ScoreRep(rep2, tcShortWith(map[string]any{"min_valid_s": int64(3)}), nil)
	assertEqual(t, int64(0), tcRow(t, out2, 0)["value"])
	assertEqual(t, "PASS", tcRow(t, out2, 0)["verdict"])
}

func TestR3H3_PostFightEventsAreNotCounted(t *testing.T) {
	rep := tcFightRep(t, t.TempDir(), tcFightOpts{StartS: 25, EndS: 30, LastSample: 44, Shields: []int{40}})
	out := score.ScoreRep(rep, tcShortWith(map[string]any{"min_valid_s": int64(3)}), nil)
	assertEqual(t, int64(0), tcRow(t, out, 0)["value"])
}

func TestR3H3_WindowArgumentsToScoredSecondsAndSpan(t *testing.T) {
	rows := []map[string]any{tcExcluded(10.0, 20.0), tcExcluded(15.0, 25.0)}
	var samples []map[string]any
	for i := -20; i <= 100; i++ {
		samples = append(samples, tcSample(float64(i), tcPlayer, 500, 1000, true, nil))
	}
	excluded := [][2]float64{{10.0, 25.0}}
	window := &[2]float64{0.0, 60.0}
	assertAlmostEqual(t, 45.0, score.ScoredSeconds(rows, samples, excluded, window), 7)
	assertAlmostEqual(t, 45.0, rules.ScoredSpan(rows, samples, excluded, window), 7)
	// An excluded span half outside the window only cuts what is inside.
	window10 := &[2]float64{0.0, 10.0}
	assertAlmostEqual(t, 5.0, score.ScoredSeconds(nil, samples, [][2]float64{{-5.0, 5.0}}, window10), 7)
}

func TestR3H3_RateCountsOnlyWindowRows(t *testing.T) {
	rate := tcRuleWith(map[string]any{"metric": "rate",
		"where": map[string]any{"kind": "cast", "spell": "Power Word: Shield"}},
		map[string]any{"pass_if": ">= 1"})
	rows := []map[string]any{tcCast(-10.0, 80.0, "bot", "player", 6065),
		tcCast(10.0, 80.0, "bot", "player", 6065),
		tcCast(20.0, 80.0, "bot", "player", 6065),
		tcCast(90.0, 80.0, "bot", "player", 6065)}
	var samples []map[string]any
	for i := -20; i <= 100; i++ {
		samples = append(samples, tcSample(float64(i), tcPlayer, 500, 1000, true, nil))
	}
	ctx := tcCtx()
	ctx.Window = &[2]float64{0.0, 60.0}
	res := rules.Evaluate(rate, rows, samples, nil, ctx)
	assertEqual(t, int64(2), res.K)
	assertAlmostEqual(t, 2.0, num(t, res.Value), 7)
	// The denominator is the window (60 s), not the span of the few samples in it.
	sparse := []map[string]any{tcSample(10.0, tcPlayer, 500, 1000, true, nil),
		tcSample(20.0, tcPlayer, 500, 1000, true, nil)}
	res = rules.Evaluate(rate, rows, sparse, nil, ctx)
	assertAlmostEqual(t, 2.0, num(t, res.Value), 7)
}

func TestR3H3_AliveUptimeDenominatorOnlyWindowSamples(t *testing.T) {
	rule := tcRuleWith(map[string]any{"metric": "uptime", "unit": "$pet", "alive": true},
		map[string]any{"pass_if": ">= 0.5"})
	var samples []map[string]any
	for i := -20; i <= 20; i++ {
		samples = append(samples, tcSample(float64(i), tcPlayer, 500, 1000, true, nil))
		if i >= 0 {
			samples = append(samples, tcSample(float64(i), tcPet, 500, 1000, true, nil))
		}
	}
	ctx := &rules.Ctx{Guids: map[string]any{"player": tcPlayer, "pet": tcPet}, Trial: tcTrial(),
		Window: &[2]float64{0.0, 20.0}}
	res := rules.Evaluate(rule, nil, samples, nil, ctx)
	assertEqual(t, []any{int64(21), int64(21)}, []any{res.K, res.N})
	assertEqual(t, "PASS", res.Verdict)
}

func TestR3H3_InCombatUptimeAndTriggersUseWindow(t *testing.T) {
	rule := tcRuleWith(map[string]any{"metric": "uptime", "unit": "$player", "aura": []any{int64(71)},
		"phase": "in_combat"}, map[string]any{"pass_if": ">= 0.9"})
	var samples []map[string]any
	for i := -20; i <= 20; i++ {
		var auras []int64
		if i >= 0 {
			auras = []int64{71}
		}
		samples = append(samples, tcSample(float64(i), tcPlayer, 500, 1000, true, auras))
	}
	ctx := &rules.Ctx{Guids: map[string]any{"player": tcPlayer}, Trial: tcTrial(),
		Window: &[2]float64{0.0, 20.0}}
	res := rules.Evaluate(rule, nil, samples, nil, ctx)
	assertEqual(t, []any{int64(21), int64(21)}, []any{res.K, res.N})
	cross := tcRule(map[string]any{"metric": "latency",
		"trigger":  map[string]any{"kind": "cross", "unit": "$player", "below": int64(50)},
		"response": map[string]any{"kind": "heal", "src": "$bot"}, "within_s": int64(4)})
	var low []map[string]any
	for i := -20; i <= 20; i++ {
		hp := 600.0
		if i >= -10 {
			hp = 400.0
		}
		low = append(low, tcSample(float64(i), tcPlayer, hp, 1000, true, nil))
	}
	ctx2 := &rules.Ctx{Guids: map[string]any{"player": tcPlayer, "bot": tcBot}, Trial: tcTrial(),
		Window: &[2]float64{0.0, 20.0}}
	res = rules.Evaluate(cross, nil, low, nil, ctx2)
	assertEqual(t, "INCONCLUSIVE", res.Verdict) // the only crossing was pre-fight
}

func TestR3H3_FightEndMissingUsesLastInFightSampleAndFlags(t *testing.T) {
	rep := tcFightRep(t, t.TempDir(), tcFightOpts{SetupS: 0, StartS: 5, LastSample: 40, EndMark: false, EndMarkSet: true})
	// samples after t=30 (wall 35) are out of combat
	rows := readJSONL(t, filepath.Join(rep, "samples.jsonl"))
	for _, row := range rows {
		if num(t, row["t"]) > 30 {
			row["in_combat"] = false
		}
	}
	var outRows []any
	for _, row := range rows {
		outRows = append(outRows, row)
	}
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), outRows)
	out := score.ScoreRep(rep, tcTrialWith(map[string]any{"min_valid_s": int64(3)}), nil)
	assertAlmostEqual(t, 25.0, num(t, out["scored_s"]), 7) // t 5 .. 30
	if !pyjson.Truthy(out["fight_end_missing"]) {
		t.Fatal("fight_end_missing not set")
	}
	run := filepath.Dir(filepath.Dir(rep))
	score.ScoreRun(run)
	data, err := os.ReadFile(filepath.Join(run, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fight_end missing") {
		t.Fatalf("card.md lacks the flag:\n%s", data)
	}
}

// --- R3M2: thresholds ----------------------------------------------------------------

func tcThrRep(t *testing.T, hp float64, config map[string]any, configRead bool, rulesList []any) (string, map[string]any) {
	t.Helper()
	cfg := config
	if cfg == nil {
		cfg = map[string]any{"ok": false, "critical": nil, "low": nil, "medium": nil}
	}
	rep := tcFightRep(t, t.TempDir(), tcFightOpts{StartS: 0, EndS: 40, Shields: []int{10},
		ExtraSetup: map[string]any{"config": cfg,
			"checks": []any{map[string]any{"id": "config_read", "ok": configRead}}}})
	var samples []any
	for sec := 0; sec < 45; sec++ {
		samples = append(samples, map[string]any{"t": float64(sec), "wall": tcWall(sec), "guid": tcPlayer,
			"hp": hp, "max_hp": int64(1000), "auras": []any{}, "in_combat": true})
		samples = append(samples, map[string]any{"t": float64(sec), "wall": tcWall(sec), "guid": tcBot,
			"hp": int64(900), "max_hp": int64(1000), "auras": []any{}, "in_combat": true})
	}
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), samples)
	run := filepath.Dir(filepath.Dir(rep))
	writeJSON(t, filepath.Join(run, "RUN.json"), map[string]any{"trials": []any{"priest-holy-pws-def-arms"}})
	trial := tcShortWith(map[string]any{"min_valid_s": int64(10)})
	delete(trial, "opportunity")
	if rulesList != nil {
		trial["rules"] = rulesList
	}
	return rep, trial
}

func TestR3M2_SetupConfigIsReadFirst(t *testing.T) {
	cfg := map[string]any{"ok": true, "critical": int64(25), "low": int64(45), "medium": int64(65)}
	rep, trial := tcThrRep(t, 270, cfg, true, nil)
	out := score.ScoreRep(rep, trial, nil)
	assertEqual(t, int64(1), tcRow(t, out, 0)["value"])
	rep, trial = tcThrRep(t, 220, cfg, true, nil)
	out = score.ScoreRep(rep, trial, nil)
	assertEqual(t, int64(0), tcRow(t, out, 0)["value"])
	assertEqual(t, "", tcRow(t, out, 0)["guard"])
}

func TestR3M2_SetupConfigBeatsRunJSONAndRunJSONBeatsDefaults(t *testing.T) {
	cfg := map[string]any{"ok": true, "critical": int64(25), "low": int64(45), "medium": int64(65)}
	rep, trial := tcThrRep(t, 270, cfg, true, nil)
	run := filepath.Dir(filepath.Dir(rep))
	writeJSON(t, filepath.Join(run, "RUN.json"), map[string]any{"critical_health": int64(30),
		"low_health": int64(50), "medium_health": int64(70),
		"trials": []any{"priest-holy-pws-def-arms"}})
	assertEqual(t, int64(1), tcRow(t, score.ScoreRep(rep, trial, nil), 0)["value"]) // 25 not 30
	rep, trial = tcThrRep(t, 270, nil, true, nil)
	run = filepath.Dir(filepath.Dir(rep))
	writeJSON(t, filepath.Join(run, "RUN.json"), map[string]any{"critical_health": int64(30),
		"low_health": int64(50), "medium_health": int64(70),
		"trials": []any{"priest-holy-pws-def-arms"}})
	assertEqual(t, int64(0), tcRow(t, score.ScoreRep(rep, trial, nil), 0)["value"]) // 30 from RUN.json
}

func TestR3M2_DefaultsOnlyWhenConfigReadFailed(t *testing.T) {
	rep, trial := tcThrRep(t, 220, nil, false, nil)
	out := score.ScoreRep(rep, trial, nil)
	// 22 % > default critical 20 -> counted, with the assumed guard.
	row := tcRow(t, out, 0)
	assertEqual(t, int64(1), row["value"])
	if !strings.Contains(pyjson.Str(row["guard"]), "thresholds assumed") {
		t.Fatalf("guard = %v", row["guard"])
	}
	assertEqual(t, true, out["thresholds_assumed"])
}

func TestR3M2_DefaultsNotUsedWhenConfigReadOKButValuesMissing(t *testing.T) {
	rep, trial := tcThrRep(t, 220, nil, true, nil)
	out := score.ScoreRep(rep, trial, nil)
	row := tcRow(t, out, 0)
	assertEqual(t, "INCONCLUSIVE", row["verdict"])
	guard := pyjson.Str(row["guard"])
	if !strings.Contains(guard, "threshold unknown") || strings.Contains(guard, "thresholds assumed") {
		t.Fatalf("guard = %v", row["guard"])
	}
	assertEqual(t, false, out["thresholds_assumed"])
}

func TestR3M2_AssumedGuardOnlyOnRulesUsingAnAlias(t *testing.T) {
	plain := tcRuleWith(map[string]any{"metric": "count",
		"where": map[string]any{"kind": "cast", "spell": "Power Word: Shield"}},
		map[string]any{"id": "PLAIN", "pass_if": ">= 0", "source": "s"})
	alias := tcObj(tcRows(tcTrial())[0])
	rep, trial := tcThrRep(t, 220, nil, false, []any{plain, alias})
	out := score.ScoreRep(rep, trial, nil)
	byID := map[string]map[string]any{}
	for _, rowVal := range tcList(out["rules"]) {
		row := tcObj(rowVal)
		byID[pyjson.Str(row["id"])] = row
	}
	assertEqual(t, "", byID["PLAIN"]["guard"])
	if !strings.Contains(pyjson.Str(byID["PRI-PWS-DEF-ARMS"]["guard"]), "thresholds assumed") {
		t.Fatalf("guard = %v", byID["PRI-PWS-DEF-ARMS"]["guard"])
	}
}

func TestR3M2_NoneThresholdNeverReachesAComparison(t *testing.T) {
	rule := tcObj(tcRows(tcTrial())[0])
	ctx := tcCtx()
	ctx.Thresholds = map[string]any{"critical": nil, "low": nil, "medium": nil}
	rows := []map[string]any{tcCast(60.0, 60.0, "bot", "player", 6065)}
	res := rules.Evaluate(rule, rows, []map[string]any{tcSample(60.0, tcBot, 900, 1000, true, nil)}, nil, ctx)
	assertEqual(t, "INCONCLUSIVE", res.Verdict)
	if !strings.Contains(res.Guard, "threshold unknown") {
		t.Fatalf("guard = %v", res.Guard)
	}
	// ... also for every other alias place.
	lat := tcRule(map[string]any{"metric": "latency",
		"trigger":  map[string]any{"kind": "cross", "unit": "$player", "below": "$low"},
		"response": map[string]any{"kind": "heal"}, "within_s": int64(4)})
	res = rules.Evaluate(lat, nil, []map[string]any{tcSample(1.0, tcPlayer, 100, 1000, true, nil)}, nil, ctx)
	assertEqual(t, "INCONCLUSIVE", res.Verdict)
	if !strings.Contains(res.Guard, "threshold unknown") {
		t.Fatalf("guard = %v", res.Guard)
	}
	hp := tcRuleWith(map[string]any{"metric": "count",
		"where": map[string]any{"kind": "cast", "src_hp_le": "$low"}}, map[string]any{"pass_if": "== 0"})
	res = rules.Evaluate(hp, rows, []map[string]any{tcSample(60.0, tcBot, 900, 1000, true, nil)}, nil, ctx)
	assertEqual(t, "INCONCLUSIVE", res.Verdict)
}

func TestR3M2_BadThresholdAliasIsAValidationError(t *testing.T) {
	rule := tcRuleWith(map[string]any{"metric": "count",
		"where": map[string]any{"kind": "cast", "src_hp_le": "$crit"}}, map[string]any{"pass_if": "== 0"})
	if !containsStr(tcVErr(rule, nil), "bad src_hp_le") {
		t.Fatalf("errs = %v", tcVErr(rule, nil))
	}
}

// --- R3M1: strict validation --------------------------------------------------------

func tcLatRule(resp map[string]any) map[string]any {
	response := map[string]any{"kind": "cast", "src": "$bot", "spell": "Dispel Magic"}
	for k, v := range resp {
		response[k] = v
	}
	return tcRule(map[string]any{"metric": "latency",
		"trigger":  map[string]any{"kind": "cross", "unit": "$player", "below": int64(50)},
		"response": response, "within_s": int64(4)})
}

func TestR3M1_GoodLatencyRuleIsClean(t *testing.T) {
	assertEqual(t, []string{}, tcVErr(tcLatRule(nil), nil))
}

func TestR3M1_ResponseValues(t *testing.T) {
	cases := []struct {
		resp   map[string]any
		needle string
	}{
		{map[string]any{"spell": "Dispel Magik"}, "unknown spell"},
		{map[string]any{"kind": "casts"}, "bad kind"},
		{map[string]any{"src": "$bots"}, "bad src alias"},
		{map[string]any{"dst": "$trigger.dstt"}, "bad dst alias"},
		{map[string]any{"spell_id": []any{int64(99999999)}}, "unknown spell_id"},
		{map[string]any{"spell_id": "abc"}, "unknown spell_id"},
	}
	for _, c := range cases {
		if !containsStr(tcVErr(tcLatRule(c.resp), nil), c.needle) {
			t.Fatalf("%s not reported", c.needle)
		}
	}
	rule := tcLatRule(nil)
	tcObj(rule["check"])["response"].(map[string]any)["bogus"] = int64(1)
	if !containsStr(tcVErr(rule, nil), "unknown response field") {
		t.Fatal("unknown response field not reported")
	}
}

func TestR3M1_ResponseKnownSpellIDValidates(t *testing.T) {
	assertEqual(t, []string{}, tcVErr(tcLatRule(map[string]any{"spell_id": []any{int64(527)}}), nil))
}

func TestR3M1_NumDenValues(t *testing.T) {
	ratio := func(num, den map[string]any) map[string]any {
		numDoc := map[string]any{"kind": "cast", "src": "$bot"}
		for k, v := range num {
			numDoc[k] = v
		}
		denDoc := map[string]any{"kind": "cast"}
		for k, v := range den {
			denDoc[k] = v
		}
		return tcRule(map[string]any{"metric": "ratio", "num": numDoc, "den": denDoc})
	}
	assertEqual(t, []string{}, tcVErr(ratio(nil, nil), nil))
	cases := []struct {
		num, den map[string]any
		needle   string
	}{
		{map[string]any{"kind": "casts"}, nil, "bad kind"},
		{map[string]any{"src": "$bots"}, nil, "bad src alias"},
		{map[string]any{"dst_hp_lt": "$crit"}, nil, "bad dst_hp_lt"},
		{nil, map[string]any{"spell": "Nope Nope"}, "unknown spell"},
		{nil, map[string]any{"bogus": int64(1)}, "unknown den key"},
		{map[string]any{"src_hp_le": "$crit"}, nil, "bad src_hp_le"},
	}
	for _, c := range cases {
		if !containsStr(tcVErr(ratio(c.num, c.den), nil), c.needle) {
			t.Fatalf("%s not reported", c.needle)
		}
	}
}

func TestR3M1_WhereFallbackValues(t *testing.T) {
	count := func(check map[string]any) map[string]any {
		doc := map[string]any{"metric": "count"}
		for k, v := range check {
			doc[k] = v
		}
		return tcRule(doc)
	}
	where := map[string]any{"kind": "aura_on", "src": "$bot", "spell": "Innervate"}
	fallback := map[string]any{"kind": "cast", "src": "$bot", "spell": "Innervate"}
	assertEqual(t, []string{}, tcVErr(count(map[string]any{"where": where, "where_fallback": fallback}), nil))

	// A fallback without its where has nothing to stand in for.
	errs := tcVErr(count(map[string]any{"where_fallback": fallback}), nil)
	if !containsStr(errs, "where_fallback without a where") {
		t.Fatalf("an orphan fallback is not reported: %v", errs)
	}
	for _, c := range []struct {
		fallback map[string]any
		needle   string
	}{
		{map[string]any{"kind": "casts"}, "bad kind"},
		{map[string]any{"src": "$bots"}, "bad src alias"},
		{map[string]any{"bogus": int64(1)}, "unknown where_fallback key"},
		{map[string]any{"dst_power_lt": "abc"}, "bad dst_power_lt"},
	} {
		if !containsStr(tcVErr(count(map[string]any{"where": where, "where_fallback": c.fallback}), nil), c.needle) {
			t.Fatalf("%s not reported", c.needle)
		}
	}
	// Only a count check carries the evidence note that says which form it read.
	rate := tcRule(map[string]any{"metric": "rate", "where": where, "where_fallback": fallback})
	if !containsStr(tcVErr(rate, nil), "only read by a count check") {
		t.Fatal("a rate rule with a fallback is not reported")
	}
}

func TestR3M1_TriggerUnitAlias(t *testing.T) {
	rule := tcLatRule(nil)
	tcObj(tcObj(rule["check"])["trigger"])["unit"] = "$players"
	if !containsStr(tcVErr(rule, nil), "bad trigger unit") {
		t.Fatal("bad trigger unit not reported")
	}
	tcObj(tcObj(rule["check"])["trigger"])["unit"] = "$mob" // a cross reads a sample guid
	if !containsStr(tcVErr(rule, nil), "bad trigger unit") {
		t.Fatal("$mob trigger unit not reported")
	}
}

func TestR3M1_WhereValueChecks(t *testing.T) {
	count := func(where map[string]any) map[string]any {
		doc := map[string]any{"kind": "cast"}
		for k, v := range where {
			doc[k] = v
		}
		return tcRuleWith(map[string]any{"metric": "count", "where": doc}, map[string]any{"pass_if": "== 0"})
	}
	assertEqual(t, []string{}, tcVErr(count(map[string]any{"src_hp_le": int64(40),
		"src_power_lt": "$low", "t_from": int64(-5), "t_to": int64(60),
		"spell_id": []any{int64(17)}}), nil))
	for _, c := range []struct {
		where  map[string]any
		needle string
	}{
		{map[string]any{"src_hp_le": "$crit"}, "bad src_hp_le"},
		{map[string]any{"src_hp_le": "abc"}, "bad src_hp_le"},
		{map[string]any{"src_power_lt": "x"}, "bad src_power_lt"},
		{map[string]any{"src_power_lt": "$crit"}, "bad src_power_lt"},
		{map[string]any{"spell_id": "abc"}, "unknown spell_id"},
		{map[string]any{"spell_id": []any{int64(99999999)}}, "unknown spell_id"},
		{map[string]any{"t_from": "x"}, "bad t_from"},
		{map[string]any{"t_to": "x"}, "bad t_to"},
		{map[string]any{"dst_has_aura": []any{"x"}}, "bad dst_has_aura"},
	} {
		if !containsStr(tcVErr(count(c.where), nil), c.needle) {
			t.Fatalf("%s not reported", c.needle)
		}
	}
}

func TestR3M1_OpportunityFields(t *testing.T) {
	good := map[string]any{"unit": "$player", "hp_lt": int64(90), "hp_gt": "$critical", "in_combat": true}
	assertEqual(t, []string{}, scenario.Validate(tcTrialWith(map[string]any{"opportunity": good})))
	check := func(opp any, needle string) {
		t.Helper()
		if !containsStr(scenario.Validate(tcTrialWith(map[string]any{"opportunity": opp})), needle) {
			t.Fatalf("%s not reported", needle)
		}
	}
	extra := cloneRow(good)
	extra["hp_ltt"] = int64(90)
	check(extra, "unknown opportunity field")
	unit := cloneRow(good)
	unit["unit"] = "$players"
	check(unit, "bad opportunity unit")
	gt := cloneRow(good)
	gt["hp_gt"] = "$crit"
	check(gt, "bad opportunity hp_gt")
	lt := cloneRow(good)
	lt["hp_lt"] = "x"
	check(lt, "bad opportunity hp_lt")
	check([]any{"x"}, "opportunity is not an object")
}

func TestR3M1_TalentsFields(t *testing.T) {
	trial := tcTrial()
	tcObj(tcObj(tcObj(trial["player"])["talents"]))["treee"] = int64(1)
	if !containsStr(scenario.Validate(trial), "unknown talents field") {
		t.Fatal("unknown talents field not reported")
	}
	trial = tcTrial()
	tcObj(trial["player"])["talents"] = []any{"12285"}
	if !containsStr(scenario.Validate(trial), "talents is not an object") {
		t.Fatal("talents shape not reported")
	}
	trial = tcTrial()
	tcObj(tcObj(tcObj(trial["player"])["talents"]))["spells"] = []any{int64(12285), "x"}
	if !containsStr(scenario.Validate(trial), "talents.spells") {
		t.Fatal("talents.spells not reported")
	}
}

func TestR3M1_CheckLevelAndResponseUnknownFields(t *testing.T) {
	rule := tcRuleWith(map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"},
		"bogus": int64(1)}, map[string]any{"pass_if": "== 0"})
	if !containsStr(tcVErr(rule, nil), "unknown check field") {
		t.Fatal("unknown check field not reported")
	}
	rule = tcLatRule(nil)
	tcObj(rule["check"])["response"].(map[string]any)["bogus"] = int64(1)
	if !containsStr(tcVErr(rule, nil), "unknown response field") {
		t.Fatal("unknown response field not reported")
	}
	rule = tcLatRule(nil)
	rule["bogus"] = int64(1)
	if !containsStr(tcVErr(rule, nil), "unknown rule field") {
		t.Fatal("unknown rule field not reported")
	}
}

// --- R3M3: selectors ----------------------------------------------------------------

var tcSelTrials = []map[string]any{{"id": "priest-holy-x"}, {"id": "priest-holy-y"}, {"id": "mage-frost-z"}}
var tcSelPaths = []string{"px", "py", "pz"}

func tcFind(t *testing.T, sel string) ([]string, error) {
	t.Helper()
	return scenario.ResolveSelectors([]string{sel}, scenario.Entries(tcSelTrials, tcSelPaths), "")
}

func TestR3M3_GlobMayMatchManyAndBareIDExactlyOne(t *testing.T) {
	for _, c := range []struct {
		sel  string
		want []string
	}{
		{"priest-*", []string{"px", "py"}},
		{"*-z", []string{"pz"}},
		{"priest-holy-?", []string{"px", "py"}},
		{"priest-holy-[xy]", []string{"px", "py"}},
		{"mage-frost-z", []string{"pz"}},
	} {
		got, err := tcFind(t, c.sel)
		if err != nil {
			t.Fatalf("%s: %v", c.sel, err)
		}
		assertEqual(t, c.want, got)
	}
	got, err := scenario.ResolveSelectors([]string{"priest-*", "mage-frost-z"},
		scenario.Entries(tcSelTrials, tcSelPaths), "")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, []string{"px", "py", "pz"}, got)
}

func TestR3M3_BadSelectorIsAnError(t *testing.T) {
	for _, sel := range []string{"priest-holy", "nope", "nope-*", "priest-holy-xx"} {
		if _, err := tcFind(t, sel); err == nil {
			t.Fatalf("%s must not match", sel)
		}
	}
	if _, err := scenario.ResolveSelectors([]string{"mage-frost-z", "nope"},
		scenario.Entries(tcSelTrials, tcSelPaths), ""); err == nil {
		t.Fatal("one bad selector must fail the whole selection")
	}
}

func TestR3M3_DuplicateBareIDIsAmbiguous(t *testing.T) {
	entries := scenario.Entries([]map[string]any{{"id": "dup"}, {"id": "dup"}}, []string{"a", "b"})
	if _, err := scenario.ResolveSelectors([]string{"dup"}, entries, ""); err == nil {
		t.Fatal("duplicate bare id must be ambiguous")
	}
}

// --- R3M4: PR-block reasons ---------------------------------------------------------

func tcReasonRun(t *testing.T, detail []any, verdict string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "t", "rep1"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, "card.json"), map[string]any{
		"run": map[string]any{"label": "x", "tb_version": "v1"},
		"rows": []any{map[string]any{"trial": "t", "rule": "R", "expected": "?",
			"verdict": verdict, "k": int64(0), "n": int64(1), "value": nil,
			"pass_if": ">= 0.5", "metric": "latency", "surprise": "",
			"source": "s", "detail": detail}}})
	writeJSON(t, filepath.Join(dir, "t", "rep1", "setup.json"), map[string]any{"checks": []any{}})
	return dir
}

func TestR3M4_GuardReasonForEveryInconclusive(t *testing.T) {
	before := tcReasonRun(t, []any{
		map[string]any{"rep": "rep1", "verdict": "INCONCLUSIVE", "value": 0.4,
			"evidence": []any{"3/5"}, "guard": "n 3 < min_n 5"},
		map[string]any{"rep": "rep2", "verdict": "INCONCLUSIVE", "value": nil,
			"evidence": []any{"no triggers"}, "guard": ""},
	}, "INCONCLUSIVE")
	after := tcReasonRun(t, []any{
		map[string]any{"rep": "rep1", "verdict": "INCONCLUSIVE", "value": nil,
			"evidence": []any{"threshold unknown"}, "guard": "threshold unknown"},
		map[string]any{"rep": "rep2", "verdict": "INVALID", "value": nil, "evidence": []any{},
			"invalid_reason": "cut short (5 s, ended: wiped)"},
	}, "INCONCLUSIVE")
	text := report.PRBlock(before, after, "1")
	for _, want := range []string{
		"before INCONCLUSIVE: n 3 < min_n 5 (rep1)",
		"before INCONCLUSIVE: no triggers (rep2)",
		"after INCONCLUSIVE: threshold unknown (rep1)",
		"after INVALID: cut short (5 s, ended: wiped) (rep2)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}

func TestR3M4_ReasonFieldWinsOverAssumedGuard(t *testing.T) {
	dir := tcReasonRun(t, []any{map[string]any{"rep": "rep1", "verdict": "INCONCLUSIVE",
		"value": nil, "evidence": []any{}, "guard": "thresholds assumed", "reason": "den 0"}}, "INCONCLUSIVE")
	text := report.PRBlock(dir, dir, "")
	if !strings.Contains(text, "after INCONCLUSIVE: den 0 (rep1)") {
		t.Fatalf("reason missing:\n%s", text)
	}
	if strings.Contains(text, "INCONCLUSIVE: thresholds assumed") {
		t.Fatalf("assumed guard won:\n%s", text)
	}
}

func TestR3M4_EndToEndMinNReasonReachesPRBlock(t *testing.T) {
	var runs []string
	for i := 0; i < 2; i++ {
		rule := map[string]any{"id": "R", "desc": "x", "source": "s", "expect_today": "?",
			"why_expect": "x", "min_n": int64(5), "min_opportunity_s": int64(0),
			"check": map[string]any{"metric": "ratio", "num": map[string]any{"kind": "cast"},
				"den": map[string]any{"kind": "cast"}},
			"pass_if": ">= 0.5"}
		rep, trial := tcThrRep(t, 500, map[string]any{"ok": true, "critical": int64(20),
			"low": int64(50), "medium": int64(70)}, true, []any{rule})
		run := filepath.Dir(filepath.Dir(rep))
		res := score.ScoreTrial(filepath.Dir(rep), trial)
		if _, err := report.WriteCard(run, []map[string]any{res}); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
	}
	text := report.PRBlock(runs[0], runs[1], "7")
	if !strings.Contains(text, "INCONCLUSIVE: n 1 < min_n 5 (rep1)") {
		t.Fatalf("reason missing:\n%s", text)
	}
}

// --- R3 low: pooled card values -----------------------------------------------------

func tcCard(t *testing.T, metric string, details []any, verdict string) (string, []any) {
	t.Helper()
	run := t.TempDir()
	if err := os.MkdirAll(filepath.Join(run, "t", "rep1"), 0o755); err != nil {
		t.Fatal(err)
	}
	var kSum, nSum int64
	for _, detVal := range details {
		det := tcObj(detVal)
		if dk, ok := pyjson.Int64(det["k"]); ok {
			kSum += dk
		}
		if dn, ok := pyjson.Int64(det["n"]); ok {
			nSum += dn
		}
	}
	trial := map[string]any{"trial": "t", "verdicts": []any{map[string]any{
		"id": "R", "verdict": verdict, "k": kSum, "n": nSum, "metric": metric, "expected": "?",
		"pass_if": ">= 0.3", "source": "s", "desc": "x", "detail": details}}}
	rows, err := report.WriteCard(run, []map[string]any{trial})
	if err != nil {
		t.Fatal(err)
	}
	return run, rows
}

func TestR3Low_CardHeaderSaysSummedOverRepeats(t *testing.T) {
	run, _ := tcCard(t, "ratio", nil, "PASS")
	data, err := os.ReadFile(filepath.Join(run, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "metric k/n (summed over repeats)") {
		t.Fatalf("header missing:\n%s", data)
	}
}

func TestR3Low_CardValueIsPooledKOverNNotMeanOfRatios(t *testing.T) {
	details := []any{
		map[string]any{"rep": "rep1", "verdict": "PASS", "value": 0.5, "k": int64(1), "n": int64(2)},
		map[string]any{"rep": "rep2", "verdict": "PASS", "value": 0.3, "k": int64(3), "n": int64(10)},
	}
	run, rows := tcCard(t, "ratio", details, "PASS")
	assertAlmostEqual(t, 4/12.0, num(t, obj(t, rows[0])["value"]), 7)
	data, err := os.ReadFile(filepath.Join(run, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "0.333 (0.5, 0.3)") {
		t.Fatalf("value cell missing:\n%s", data)
	}
	for _, metric := range []string{"latency", "uptime"} {
		_, rows := tcCard(t, metric, details, "PASS")
		assertAlmostEqual(t, 4/12.0, num(t, obj(t, rows[0])["value"]), 7)
	}
	counts := []any{
		map[string]any{"rep": "rep1", "verdict": "PASS", "value": int64(3), "k": int64(3), "n": int64(1)},
		map[string]any{"rep": "rep2", "verdict": "PASS", "value": int64(1), "k": int64(1), "n": int64(1)},
	}
	_, rows = tcCard(t, "count", counts, "PASS")
	assertAlmostEqual(t, 2.0, num(t, obj(t, rows[0])["value"]), 7)
}

func TestR3Low_PooledValueSkipsInvalidAndEmptyRepeats(t *testing.T) {
	details := []any{
		map[string]any{"rep": "rep1", "verdict": "INVALID", "value": nil, "k": int64(0), "n": int64(0)},
		map[string]any{"rep": "rep2", "verdict": "PASS", "value": 0.5, "k": int64(2), "n": int64(4)},
		map[string]any{"rep": "rep3", "verdict": "INCONCLUSIVE", "value": 0.0, "k": int64(0), "n": int64(3)},
	}
	_, rows := tcCard(t, "ratio", details, "PASS")
	assertAlmostEqual(t, 0.5, num(t, obj(t, rows[0])["value"]), 7)
}

func TestR3Low_AssumedTextFollowsScoreConfigLogic(t *testing.T) {
	cfg := map[string]any{"ok": true, "critical": int64(25), "low": int64(45), "medium": int64(65)}
	rep, trial := tcThrRep(t, 270, cfg, true, nil)
	run := filepath.Dir(filepath.Dir(rep))
	res := score.ScoreTrial(filepath.Dir(rep), trial)
	if _, err := report.WriteCard(run, []map[string]any{res}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(run, "card.md"))
	if !strings.Contains(string(data), "thresholds=live") {
		t.Fatalf("want thresholds=live:\n%s", data)
	}
	rep, trial = tcThrRep(t, 270, nil, false, nil)
	run = filepath.Dir(filepath.Dir(rep))
	res = score.ScoreTrial(filepath.Dir(rep), trial)
	if _, err := report.WriteCard(run, []map[string]any{res}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(run, "card.md"))
	if !strings.Contains(string(data), "thresholds=assumed 20/50/70") {
		t.Fatalf("want assumed thresholds:\n%s", data)
	}
	rep, trial = tcThrRep(t, 270, nil, true, nil)
	run = filepath.Dir(filepath.Dir(rep))
	res = score.ScoreTrial(filepath.Dir(rep), trial)
	if _, err := report.WriteCard(run, []map[string]any{res}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(run, "card.md"))
	if !strings.Contains(string(data), "thresholds=unknown") || strings.Contains(string(data), "assumed 20/50/70") {
		t.Fatalf("want thresholds=unknown:\n%s", data)
	}
}

// --- R3 survivor-kill cases ---------------------------------------------------------

func tcNormaliseHP(t *testing.T, samples [][2]float64) any {
	t.Helper()
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "setup.json"), tcSetupOK())
	writeJSONL(t, filepath.Join(dir, "marks.jsonl"), []any{
		map[string]any{"kind": "setup_ok", "wall": "2026-01-01T00:00:00+00:00"},
	})
	var rows []any
	for _, s := range samples {
		rows = append(rows, map[string]any{"t": s[0], "guid": tcPlayer, "hp": s[1],
			"max_hp": int64(1000), "auras": []any{}})
	}
	writeJSONL(t, filepath.Join(dir, "samples.jsonl"), rows)
	writeJSONL(t, filepath.Join(dir, "raw-ta.jsonl"), []any{
		map[string]any{"seq": int64(1), "time": "2026-01-01T00:00:10+00:00",
			"kind": "spell_go", "source": map[string]any{"name": "b", "guid": tcBot},
			"target": map[string]any{"name": "p", "guid": tcPlayer}, "spell_id": int64(6065)},
	})
	normalised, err := events.Normalise(dir, tcTrial())
	if err != nil {
		t.Fatal(err)
	}
	return normalised[0]["dst_hp_pct"]
}

func TestR3Survivor_HPTakesTheLastSampleNotTheFirstInsideTheWindow(t *testing.T) {
	assertEqual(t, 30.0, tcNormaliseHP(t, [][2]float64{{9.3, 900}, {9.6, 300}}))
	// ... and a later sample (10.4 is after t) is never read.
	assertEqual(t, 30.0, tcNormaliseHP(t, [][2]float64{{9.3, 900}, {9.6, 300}, {10.4, 100}}))
}

func TestR3Survivor_RulesAtFutureSampleAndFirstNotLast(t *testing.T) {
	byGUID := rules.SampleRows([]map[string]any{
		tcSample(9.3, tcPlayer, 900, 1000, true, nil),
		tcSample(9.6, tcPlayer, 300, 1000, true, nil),
		tcSample(11.0, tcPlayer, 100, 1000, true, nil)})
	assertEqual(t, int64(300), rules.At(byGUID, tcPlayer, 10.0)["hp"])
	assertEqual(t, 9.6, rules.At(byGUID, tcPlayer, 10.0)["t"])
	onlyFuture := rules.SampleRows([]map[string]any{tcSample(10.5, tcPlayer, 100, 1000, true, nil)})
	if rules.At(onlyFuture, tcPlayer, 10.0) != nil {
		t.Fatal("a future sample must not be read")
	}
	stale := rules.SampleRows([]map[string]any{tcSample(8.5, tcPlayer, 900, 1000, true, nil)})
	if rules.At(stale, tcPlayer, 10.0) != nil {
		t.Fatal("a 1.5 s old sample must not be read")
	}
	edge := rules.SampleRows([]map[string]any{tcSample(9.0, tcPlayer, 900, 1000, true, nil)})
	assertEqual(t, int64(900), rules.At(edge, tcPlayer, 10.0)["hp"])
	// Through a where key: src_hp_le reads the last sample, not the first.
	rule := tcRuleWith(map[string]any{"metric": "count",
		"where": map[string]any{"kind": "cast", "src_hp_le": int64(50)}}, map[string]any{"pass_if": "== 0"})
	row := tcCast(10.0, 80.0, "bot", "player", 6065)
	samples := []map[string]any{tcSample(9.3, tcBot, 900, 1000, true, nil),
		tcSample(9.6, tcBot, 300, 1000, true, nil)}
	res := rules.Evaluate(rule, []map[string]any{row}, samples, nil, tcCtx())
	assertEqual(t, int64(1), res.Value)
}

func TestR3Survivor_InterruptibleEachConditionAlone(t *testing.T) {
	// The Python test swaps the spell table for four synthetic rows and goes through
	// rules.evaluate. Go reads the embedded table, so each condition is pinned with a real
	// id that carries it, and the rule's value is asserted exactly as Python does (1/0).
	table := interruptibleSpellTable(t)
	pick := func(name string, pred func(castMS, prevention, flags float64) bool) int64 {
		id, ok := pickSpellID(table, pred)
		if !ok {
			t.Fatalf("the embedded spell table has no %s spell", name)
		}
		return id
	}
	cases := []struct {
		name string
		id   int64
		want int64
	}{
		{"ok", pick("interruptible", func(cast, prev, flags float64) bool {
			return cast > 0 && prev == spells.PreventionSilence && int64(flags)&spells.InterruptFlagDamage != 0
		}), 1},
		{"instant", pick("instant", func(cast, prev, flags float64) bool {
			return cast == 0 && prev == spells.PreventionSilence && int64(flags)&spells.InterruptFlagDamage != 0
		}), 0},
		{"pushback only", pick("pushback-only", func(cast, prev, flags float64) bool {
			return cast > 0 && prev == 2 && int64(flags)&spells.InterruptFlagDamage != 0
		}), 0},
		{"no dmg flag", pick("no-damage-flag", func(cast, prev, flags float64) bool {
			return cast > 0 && prev == spells.PreventionSilence && int64(flags)&spells.InterruptFlagDamage == 0
		}), 0},
		{"unknown id", 99999999, 0},
	}
	rule := tcRuleWith(map[string]any{"metric": "count",
		"where": map[string]any{"kind": "cast", "spell_interruptible": true}}, map[string]any{"pass_if": ">= 0"})
	for _, c := range cases {
		row := tcCast(10.0, 80.0, "bot", "player", c.id)
		res := rules.Evaluate(rule, []map[string]any{row}, nil, nil, tcCtx())
		assertEqual(t, c.want, res.Value)
	}
}

func interruptibleSpellTable(t *testing.T) map[string]any {
	t.Helper()
	doc, err := pyjson.Unmarshal(data.Read("spells.json"))
	if err != nil {
		t.Fatalf("spells.json: %v", err)
	}
	table, ok := pyjson.Object(pyjson.ObjectOrEmpty(doc)["by_id"])
	if !ok {
		t.Fatal("spells.json has no by_id object")
	}
	return table
}

// pickSpellID returns the first id (sorted, so the choice is stable) whose table row matches.
func pickSpellID(table map[string]any, pred func(castMS, prevention, flags float64) bool) (int64, bool) {
	ids := make([]string, 0, len(table))
	for id := range table {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		row, ok := pyjson.Object(table[id])
		if !ok {
			continue
		}
		cast, okCast := pyjson.Float(row["cast_ms"])
		prev, okPrev := pyjson.Float(row["prevention_type"])
		flags, okFlags := pyjson.Float(row["interrupt_flags"])
		if !okCast || !okPrev || !okFlags {
			continue
		}
		if !pred(cast, prev, flags) {
			continue
		}
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			continue
		}
		return n, true
	}
	return 0, false
}

func TestR3Survivor_RepeatWithoutSetupJSONIsInvalid(t *testing.T) {
	run := t.TempDir()
	tdir := filepath.Join(run, "t")
	if err := os.MkdirAll(filepath.Join(tdir, "rep1"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := score.ScoreTrial(tdir, tcTrial())
	row := tcVerdictRow(t, out, 0)
	assertEqual(t, "INVALID", row["verdict"])
	detail := tcObj(tcList(row["detail"])[0])
	if !strings.Contains(pyjson.Str(detail["invalid_reason"]), "setup.json") {
		t.Fatalf("invalid_reason = %v", detail["invalid_reason"])
	}
	assertEqual(t, int64(0), row["n"])
}

func TestR3Survivor_AliveUptimeWithNoSamplesIsInconclusiveNotFail(t *testing.T) {
	rule := tcRuleWith(map[string]any{"metric": "uptime", "unit": "$pet", "alive": true},
		map[string]any{"pass_if": ">= 0.5"})
	res := rules.Evaluate(rule, nil, nil, nil, &rules.Ctx{
		Guids: map[string]any{"pet": tcPet}, Trial: tcTrial()})
	assertEqual(t, "INCONCLUSIVE", res.Verdict)
	assertEqual(t, int64(0), res.N)
	// Samples only OUTSIDE the window are no samples either.
	res = rules.Evaluate(rule, nil, []map[string]any{tcSample(-5.0, tcPlayer, 500, 1000, true, nil)}, nil,
		&rules.Ctx{Guids: map[string]any{"pet": tcPet}, Trial: tcTrial(), Window: &[2]float64{0.0, 10.0}})
	assertEqual(t, "INCONCLUSIVE", res.Verdict)
}

func TestR3Survivor_CastTimeIndex18IsNeverInterruptibleInTheData(t *testing.T) {
	doc, err := pyjson.Unmarshal(data.Read("spells.json"))
	if err != nil {
		t.Fatal(err)
	}
	byID := pyjson.ObjectOrEmpty(pyjson.Get(doc, "by_id"))
	if len(byID) == 0 {
		t.Fatal("no by_id table")
	}
	for sid, info := range byID {
		obj := pyjson.ObjectOrEmpty(info)
		idx, ok := pyjson.Num(obj["casting_time_index"])
		if !ok || idx != 18 {
			continue
		}
		if spells.IsInterruptible(obj["cast_ms"], obj["prevention_type"], obj["interrupt_flags"]) {
			t.Fatalf("spell %s (cast index 18) must not be interruptible", sid)
		}
	}
}

// --- R3 window/opportunity ----------------------------------------------------------

func TestR3Window_OpportunityCountsOnlyTheWindow(t *testing.T) {
	rep := tcFightRep(t, t.TempDir(), tcFightOpts{SetupS: 0, StartS: 40, EndS: 45,
		Shields: []int{42}, LastSample: 60})
	trial := tcTrialWith(map[string]any{"min_valid_s": int64(3)})
	trial["opportunity"] = map[string]any{"unit": "$player", "hp_lt": int64(90), "in_combat": true}
	tcObj(tcRows(trial)[0])["min_opportunity_s"] = int64(20)
	out := score.ScoreRep(rep, trial, nil)
	rule := tcRow(t, out, 0)
	assertEqual(t, "INCONCLUSIVE", rule["verdict"])
	guard := pyjson.Str(rule["guard"])
	if !strings.Contains(guard, "opportunity") {
		t.Fatalf("guard = %q", guard)
	}
	got := strings.Split(strings.Split(guard, "opportunity ")[1], " s")[0]
	if f, ok := pyjson.Float(got); !ok || f > 5.0 {
		t.Fatalf("opportunity read %q, want <= 5 s", got)
	}
}

func TestR3Window_PRBlockReasonPrefersRealReasonToAssumedGuard(t *testing.T) {
	var runs []string
	for i := 0; i < 2; i++ {
		lat := tcRuleWith(map[string]any{"metric": "latency",
			"trigger":  map[string]any{"kind": "cross", "unit": "$player", "below": "$low"},
			"response": map[string]any{"kind": "heal", "src": "$bot"}, "within_s": int64(4)},
			map[string]any{"min_n": int64(0)})
		rep, trial := tcThrRep(t, 900, nil, false, []any{lat})
		run := filepath.Dir(filepath.Dir(rep))
		res := score.ScoreTrial(filepath.Dir(rep), trial)
		if _, err := report.WriteCard(run, []map[string]any{res}); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
	}
	text := report.PRBlock(runs[0], runs[1], "")
	if !strings.Contains(text, "INCONCLUSIVE: no triggers (rep1)") {
		t.Fatalf("reason missing:\n%s", text)
	}
}
