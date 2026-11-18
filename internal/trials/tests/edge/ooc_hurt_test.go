package edge

// Translation of tests/test_ooc_hurt.py.
//
// Not translated (they drive trl.live.situations, the live runner half, which has no Go port —
// there is no `internal/trials/live` package and no Situation type): all ten tests of
// HurtLoopTest — the_hurt_loop_sets_health_on_schedule_and_marks_each_set,
// a_tick_is_skipped_and_logged_while_the_hire_is_in_combat,
// a_tick_is_skipped_while_the_victim_is_in_combat,
// no_hire_sample_waits_and_retries_next_second, a_victim_already_in_band_is_left_alone,
// a_rescue_out_of_combat_fires_the_floor_and_is_logged_loudly,
// the_stance_is_recast_when_it_reads_down, a_stance_that_holds_is_not_recast,
// the_battle_control_recasts_its_own_stance, a_lost_session_ends_the_window_invalid.
// ValidationTest and OocHurtScoringTest only touch scenario/score/rules and are below.

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
	"github.com/pjerra/forever-agent/internal/trials/spells"
)

const (
	oocPlayer   = "0x0000000000000001"
	oocHire     = "0x0000000000000002"
	oocMob      = "0xf13000000000abcd"
	oocDefID    = "priest-holy-pws-def-arms-ooc"
	oocBattleID = "priest-holy-pws-battle-ooc-control"
	oocMaxHP    = int64(1000)
)

func oocTrial(t *testing.T, tid string) scenario.Trial {
	t.Helper()
	trial, err := scenario.LoadTrial("healers/" + tid + ".json")
	if err != nil {
		t.Fatalf("load %s: %v", tid, err)
	}
	return trial
}

func oocSample(at float64, guid string, hp int64, auras []any, combat bool) map[string]any {
	if auras == nil {
		auras = []any{}
	}
	return map[string]any{
		"t": at, "wall": nil, "unit": "x", "guid": guid, "hp": hp,
		"max_hp": oocMaxHP, "power": int64(500), "max_power": int64(500), "auras": auras,
		"casting": nil, "in_combat": combat, "target": "",
		"x": 0.0, "y": 0.0, "z": 0.0,
	}
}

// oocSeries is test_rules_healers.series: the player's HP comes from playerHP, the hire is full.
func oocSeries(secs int, playerHP func(float64) int64, playerAuras []any, combat bool) []any {
	out := []any{}
	for at := range secs + 1 {
		ft := float64(at)
		out = append(out, oocSample(ft, oocPlayer, playerHP(ft), playerAuras, combat))
		out = append(out, oocSample(ft, oocHire, 1000, nil, combat))
	}
	return out
}

func oocEvent(t *testing.T, at float64, kind, src, dst string, spellID int64, hp any) map[string]any {
	t.Helper()
	guid := map[string]string{"player": oocPlayer, "bot": oocHire}
	return map[string]any{
		"t": at, "wall": nil, "kind": kind, "src": src, "src_guid": guid[src],
		"dst": dst, "dst_guid": guid[dst], "src_unit": "$" + src, "dst_unit": "$" + dst,
		"spell_id": spellID, "spell": spells.NameFor(spellID), "amount": nil,
		"dst_hp_pct": hp, "src_role": src, "dst_role": dst, "origin": "ta",
	}
}

// oocScore scores one synthetic repeat of trial `tid`; returns {rule id: entry}.
func oocScore(t *testing.T, tid string, events, samples []any, secs int) map[string]map[string]any {
	t.Helper()
	trial := oocTrial(t, tid)
	rep := filepath.Join(t.TempDir(), "run", tid, "rep1")
	doc := map[string]any{"ok": true,
		"guids":  map[string]any{"player": oocPlayer, "bot": oocHire, "mobs": []any{oocMob}},
		"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)}}
	writeJSON(t, filepath.Join(rep, "setup.json"), doc)
	marks := []any{
		map[string]any{"kind": "fight_start", "t": int64(0)},
		map[string]any{"kind": "fight_end", "t": float64(secs), "ended": "duration"},
	}
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), samples)
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), events)
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), marks)
	out := score.ScoreRep(rep, trial, nil)
	result := map[string]map[string]any{}
	for _, rowVal := range list(t, out["rules"]) {
		row := obj(t, rowVal)
		result[pyjson.Str(row["id"])] = row
	}
	return result
}

func oocRuleOf(t *testing.T, trial scenario.Trial, rid string) map[string]any {
	t.Helper()
	for _, ruleVal := range list(t, trial["rules"]) {
		rule := obj(t, ruleVal)
		if pyjson.Str(rule["id"]) == rid {
			return rule
		}
	}
	t.Fatalf("trial %v has no rule %s", trial["id"], rid)
	return nil
}

// ---- validation -------------------------------------------------------------

func TestValidationTest_BothNewTrialsValidate(t *testing.T) {
	for _, tid := range []string{oocDefID, oocBattleID} {
		trial := oocTrial(t, tid)
		if errs := scenario.Validate(trial); len(errs) != 0 {
			t.Fatalf("%s: %v", tid, errs)
		}
		sit := obj(t, trial["situation"])
		assertEqual(t, "ooc_hurt", pyjson.Str(sit["kind"]))
		// the cadence is read from each file, not pinned: a retune must not break this
		every := num(t, sit["hurt_every_s"])
		if every < 8 || every > 120 { // the scenario range
			t.Fatalf("%s: hurt_every_s %v", tid, every)
		}
		assertEqual(t, int64(0), obj(t, sit["mobs"])["count"])
	}
}

func oocErrors(t *testing.T, over map[string]any) []string {
	t.Helper()
	trial := obj(t, etDeepCopy(t, oocTrial(t, oocDefID)))
	sit := obj(t, trial["situation"])
	for key, value := range over {
		sit[key] = value
	}
	return scenario.Validate(trial)
}

func TestValidationTest_ABadHurtEverySIsRejected(t *testing.T) {
	// None.__class__ (the class object, not None) is an empty object here: present and not a number.
	for _, bad := range []any{int64(7), int64(121), int64(0), int64(-20), "20", true, map[string]any{}} {
		errs := oocErrors(t, map[string]any{"hurt_every_s": bad})
		if !etHasErrContaining(errs, "hurt_every_s") {
			t.Fatalf("%v: %v", bad, errs)
		}
	}
}

func TestValidationTest_TheRangeEdgesAreAccepted(t *testing.T) {
	for _, ok := range []any{int64(8), int64(120), 20.5} {
		if errs := oocErrors(t, map[string]any{"hurt_every_s": ok}); len(errs) != 0 {
			t.Fatalf("%v: %v", ok, errs)
		}
	}
}

func TestValidationTest_HpBandIsRequiredAndChecked(t *testing.T) {
	trial := obj(t, etDeepCopy(t, oocTrial(t, oocDefID)))
	delete(obj(t, trial["situation"]), "hp_band")
	if !etHasErrContaining(scenario.Validate(trial), "hp_band") {
		t.Fatal("missing hp_band accepted")
	}
	if errs := oocErrors(t, map[string]any{"hp_band": []any{int64(0), int64(40)}}); !etHasErrContaining(errs, "hp_band") {
		t.Fatalf("[0, 40]: %v", errs)
	}
	if errs := oocErrors(t, map[string]any{"hp_band": []any{int64(40), int64(100)}}); !etHasErrContaining(errs, "hp_band") {
		t.Fatalf("[40, 100]: %v", errs)
	}
}

func TestValidationTest_AnOocHurtWithMobsOrNoWindowIsRejected(t *testing.T) {
	mobs := map[string]any{"role": "melee", "count": int64(1), "level_delta": int64(0)}
	if errs := oocErrors(t, map[string]any{"mobs": mobs}); !etHasErrContaining(errs, "mobs.count 0") {
		t.Fatalf("mobs: %v", errs)
	}
	if errs := oocErrors(t, map[string]any{"ooc_wait_s": int64(0)}); !etHasErrContaining(errs, "ooc_wait_s") {
		t.Fatalf("ooc_wait_s: %v", errs)
	}
}

func TestValidationTest_TheKindIsStillChecked(t *testing.T) {
	if errs := oocErrors(t, map[string]any{"kind": "ooc_hurtx"}); !etHasErrContaining(errs, "situation.kind") {
		t.Fatalf("%v", errs)
	}
}

// ---- scoring: the three rules on a synthetic repeat -------------------------

// oocWindow is the Python `window`: the player sits at 35 % for inBandS s after each hurt (and is
// full otherwise), all out of combat.
func oocWindow(t *testing.T, secs int, shieldAt, healAt []float64, auras []any, hurtEvery, inBandS float64) ([]any, []any) {
	t.Helper()
	hp := func(at float64) int64 {
		if math.Mod(at, hurtEvery) < inBandS {
			return 350
		}
		return 1000
	}
	samples := oocSeries(secs, hp, auras, false)
	events := []any{}
	for _, at := range shieldAt {
		events = append(events, oocEvent(t, at, "cast", "bot", "player", etSID(t, "Power Word: Shield"), int64(35)))
	}
	for _, at := range healAt {
		events = append(events, oocEvent(t, at, "cast", "bot", "player", etSID(t, "Heal"), int64(35)))
	}
	return events, samples
}

func oocRows(t *testing.T, vals []any) []rules.Row {
	t.Helper()
	out := make([]rules.Row, 0, len(vals))
	for _, val := range vals {
		out = append(out, obj(t, val))
	}
	return out
}

func TestOocHurtScoringTest_AShieldBetweenPullsAboveCriticalFailsTheRuleAndTheControlsPass(t *testing.T) {
	events, samples := oocWindow(t, 150, []float64{2.0, 22.0}, []float64{4.0, 24.0}, []any{int64(71)}, 20, 12)
	got := oocScore(t, oocDefID, events, samples, 150)
	assertEqual(t, "FAIL", pyjson.Str(got["PRI-PWS-DEF-ARMS-OOC"]["verdict"]))
	assertEqual(t, int64(2), got["PRI-PWS-DEF-ARMS-OOC"]["value"])
	assertEqual(t, "PASS", pyjson.Str(got["PRI-PWS-DEF-ARMS-OOC-HEALS"]["verdict"]))
	assertEqual(t, "PASS", pyjson.Str(got["PRI-PWS-DEF-ARMS-OOC-STANCE"]["verdict"]))
}

func TestOocHurtScoringTest_NoShieldButHealsPassesAfterTheFix(t *testing.T) {
	events, samples := oocWindow(t, 150, nil, []float64{4.0, 24.0}, []any{int64(71)}, 20, 12)
	got := oocScore(t, oocDefID, events, samples, 150)
	assertEqual(t, "PASS", pyjson.Str(got["PRI-PWS-DEF-ARMS-OOC"]["verdict"]))
}

func TestOocHurtScoringTest_APriestThatNeverHealsFailsTheHealsControl(t *testing.T) {
	events, samples := oocWindow(t, 150, nil, nil, []any{int64(71)}, 20, 12)
	got := oocScore(t, oocDefID, events, samples, 150)
	assertEqual(t, "FAIL", pyjson.Str(got["PRI-PWS-DEF-ARMS-OOC-HEALS"]["verdict"]))
}

func TestOocHurtScoringTest_TooLittleInBandTimeIsInconclusiveNotAPass(t *testing.T) {
	// 8 hurts x ~2 s = 16 s < the rule's min_opportunity_s (30)
	events, samples := oocWindow(t, 150, nil, nil, []any{int64(71)}, 20, 2)
	got := oocScore(t, oocDefID, events, samples, 150)
	row := got["PRI-PWS-DEF-ARMS-OOC"]
	assertEqual(t, "INCONCLUSIVE", pyjson.Str(row["verdict"]))
	if reason := pyjson.Str(row["reason"]); !strings.Contains(reason, "opportunity") {
		t.Fatalf("reason %q", reason)
	}
}

func TestOocHurtScoringTest_OpportunityCountsOnlyOutOfCombatInBandSeconds(t *testing.T) {
	trial := oocTrial(t, oocDefID)
	minS := num(t, oocRuleOf(t, trial, "PRI-PWS-DEF-ARMS-OOC")["min_opportunity_s"])
	// 35 %: inside the critical-to-low band, out of combat
	_, samples := oocWindow(t, 150, nil, nil, []any{int64(71)}, 20, 12)
	// "$low" and "$critical" resolve through ctx.Thresholds, the way score.py's _ctx builds it
	ctx := &rules.Ctx{
		Guids:      map[string]any{"player": oocPlayer, "bot": oocHire},
		Thresholds: map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)},
	}
	got := rules.OpportunitySeconds(trial, oocRows(t, samples), nil, ctx)
	if got < minS {
		t.Fatalf("opportunity %v < %v", got, minS)
	}
	fighting := []any{}
	for _, sampleVal := range samples {
		row := cloneRow(obj(t, sampleVal))
		row["in_combat"] = true
		fighting = append(fighting, row)
	}
	if v := rules.OpportunitySeconds(trial, oocRows(t, fighting), nil, ctx); v != 0.0 {
		t.Fatalf("fighting opportunity %v, want 0", v)
	}
	// 60 %: above the low band
	high := oocSeries(150, func(float64) int64 { return 600 }, []any{int64(71)}, false)
	if v := rules.OpportunitySeconds(trial, oocRows(t, high), nil, ctx); v != 0.0 {
		t.Fatalf("high-hp opportunity %v, want 0", v)
	}
}

func TestOocHurtScoringTest_AShieldInCombatDoesNotCountForTheOutOfCombatRule(t *testing.T) {
	events, samples := oocWindow(t, 150, nil, nil, []any{int64(71)}, 20, 12)
	events = append(events, oocEvent(t, 30.0, "cast", "bot", "player", etSID(t, "Power Word: Shield"), int64(35)))
	for i, sampleVal := range samples {
		row := obj(t, sampleVal)
		if pyjson.Str(row["guid"]) == oocHire && num(t, row["t"]) >= 29 && num(t, row["t"]) <= 31 {
			patched := cloneRow(row)
			patched["in_combat"] = true
			samples[i] = patched
		}
	}
	got := oocScore(t, oocDefID, events, samples, 150)
	assertEqual(t, int64(0), got["PRI-PWS-DEF-ARMS-OOC"]["value"])
}

func TestOocHurtScoringTest_AStanceThatDropsFailsTheStancePrecondition(t *testing.T) {
	events, samples := oocWindow(t, 150, nil, nil, nil, 20, 12)
	got := oocScore(t, oocDefID, events, samples, 150)
	assertEqual(t, "FAIL", pyjson.Str(got["PRI-PWS-DEF-ARMS-OOC-STANCE"]["verdict"]))
}

func TestOocHurtScoringTest_BattleControlNeedsOneOutOfCombatShield(t *testing.T) {
	events, samples := oocWindow(t, 150, []float64{2.0}, nil, []any{etStanceBattle}, 20, 12)
	got := oocScore(t, oocBattleID, events, samples, 150)
	assertEqual(t, "PASS", pyjson.Str(got["PRI-PWS-BATTLE-OOC-CONTROL"]["verdict"]))
	assertEqual(t, "PASS", pyjson.Str(got["PRI-PWS-BATTLE-OOC-CONTROL-STANCE"]["verdict"]))
	events, samples = oocWindow(t, 150, nil, nil, []any{etStanceBattle}, 20, 12)
	got = oocScore(t, oocBattleID, events, samples, 150)
	assertEqual(t, "FAIL", pyjson.Str(got["PRI-PWS-BATTLE-OOC-CONTROL"]["verdict"]))
}

func TestOocHurtScoringTest_ExpectationsAreTheBeforeAfterPair(t *testing.T) {
	assertEqual(t, "FAIL", pyjson.Str(oocRuleOf(t, oocTrial(t, oocDefID), "PRI-PWS-DEF-ARMS-OOC")["expect_today"]))
	assertEqual(t, "PASS", pyjson.Str(oocRuleOf(t, oocTrial(t, oocDefID), "PRI-PWS-DEF-ARMS-OOC-HEALS")["expect_today"]))
	assertEqual(t, "PASS", pyjson.Str(oocRuleOf(t, oocTrial(t, oocBattleID), "PRI-PWS-BATTLE-OOC-CONTROL")["expect_today"]))
}
