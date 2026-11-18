package fixesb

// Translated from tests/test_bench_fix_r2.py (bench-fix-1111): one class per fix.
// The classes that drive the live runner (trl.live: FakeAgent, GmGuard, situations.Situation,
// setup.check_player_visible) are not part of the Go scoring port and are skipped:
//   - SummonClaimAnyDistanceTest (situations.summon_mob + gm_guard)
//   - MarkerBeforeCommandTest (situations.Situation / drain_tick / add_refill_mark)
//   - PrepullUnauraTest, except test_the_trial_strips_shadowform_and_validates: the rest need
//     situations.Situation.run and its prepull lines
//   - PlayerVisibleDirectEvidenceTest (trl.live.setup.check_player_visible, absent from Go)

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

const (
	r2PLAYER = "0x0000000000000001"
	r2BOT    = "0x0000000000000002"
	r2MOBG   = "0xf13000000000abcd"
)

// r2Rows re-types rows as the []any writeJSONL wants.
func r2Rows(rows []rules.Row) []any {
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out
}

// r2LoadThreat reads the embedded dps-mage-threat-cap trial.
func r2LoadThreat(t *testing.T) map[string]any {
	t.Helper()
	trial, err := scenario.LoadTrial("dps/dps-mage-threat-cap.json")
	if err != nil {
		t.Fatalf("load dps-mage-threat-cap: %v", err)
	}
	return trial
}

// r2NoAggro returns the DPS-MAG-THREAT-NO-AGGRO rule.
func r2NoAggro(t *testing.T, trial map[string]any) map[string]any {
	t.Helper()
	for _, rv := range list(t, trial["rules"]) {
		r := obj(t, rv)
		if r["id"] == "DPS-MAG-THREAT-NO-AGGRO" {
			return r
		}
	}
	t.Fatal("no DPS-MAG-THREAT-NO-AGGRO rule")
	return nil
}

// r2Sample is the sampler row of the Python helper.
func r2Sample(at float64, guid string, hp, maxHP float64, combat bool) rules.Row {
	return rules.Row{"t": at, "wall": nil, "unit": "x", "guid": guid, "hp": hp,
		"max_hp": maxHP, "power": int64(500), "max_power": int64(500), "auras": []any{},
		"casting": nil, "in_combat": combat, "target": "", "x": 0.0, "y": 0.0, "z": 0.0}
}

// r2Hit is one mob hit on the bot.
func r2Hit(at float64) rules.Row {
	return rules.Row{"t": at, "wall": nil, "kind": "damage", "src": "m", "src_guid": r2MOBG,
		"dst": "b", "dst_guid": r2BOT, "src_unit": "$mob", "dst_unit": "$bot",
		"spell_id": nil, "spell": nil, "amount": int64(10), "dst_hp_pct": int64(90),
		"src_role": "mob", "dst_role": "bot", "origin": "ta"}
}

// r2ThreatTrial is the small threat-cap trial of ThreatCapWindowsTest.
func r2ThreatTrial() map[string]any {
	return map[string]any{
		"id":          "t",
		"opportunity": map[string]any{"in_combat": true, "unit": "$bot"},
		"rules": []any{map[string]any{
			"id": "NO-AGGRO", "expect_today": "?",
			"check": map[string]any{
				"metric": "count",
				"where": map[string]any{"kind": "damage", "src": "$mob", "dst": "$bot",
					"not_within_s_after_mark": []any{
						map[string]any{"kind": "fight_start", "s": int64(12)},
						map[string]any{"kind": "refill", "s": int64(12)},
					}},
			},
			"pass_if": "== 0", "min_opportunity_s": int64(90),
		}},
	}
}

// r2ThreatMarks is the marks file of ThreatCapWindowsTest.
func r2ThreatMarks() []rules.Row {
	return []rules.Row{
		{"kind": "fight_start", "t": int64(0)},
		{"kind": "refill", "t": int64(60)},
		{"kind": "fight_end", "t": int64(140), "ended": "duration"},
	}
}

// r2ScoreOne writes one rep below a run dir and scores it, indexed by rule id.
func r2ScoreOne(t *testing.T, trial map[string]any, evts, samples, marks []rules.Row) map[string]map[string]any {
	t.Helper()
	rep := filepath.Join(t.TempDir(), "run", "trial", "rep1")
	writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{
		"ok":     true,
		"guids":  map[string]any{"player": r2PLAYER, "bot": r2BOT, "mobs": []any{r2MOBG}},
		"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)},
	})
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), r2Rows(samples))
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), r2Rows(evts))
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), r2Rows(marks))
	out := score.ScoreRep(rep, trial, nil)
	byID := make(map[string]map[string]any)
	for _, rv := range list(t, out["rules"]) {
		r := obj(t, rv)
		byID[pyjson.Str(r["id"])] = r
	}
	return byID
}

func TestThreatCapTrialShapeTest_TheRuleLeavesOutTheOpeningAndEveryRefill(t *testing.T) {
	trial := r2LoadThreat(t)
	rule := r2NoAggro(t, trial)
	where := obj(t, obj(t, rule["check"])["where"])
	assertEqual(t, []any{
		map[string]any{"kind": "fight_start", "s": int64(12)},
		map[string]any{"kind": "refill", "s": int64(12)},
	}, where["not_within_s_after_mark"])
}

func TestThreatCapTrialShapeTest_TheOpportunityIsTheMageAndThreeRepsAreRequired(t *testing.T) {
	trial := r2LoadThreat(t)
	assertEqual(t, map[string]any{"in_combat": true, "unit": "$bot"}, trial["opportunity"])
	assertEqual(t, int64(3), r2NoAggro(t, trial)["min_reps"])
}

func TestThreatCapTrialShapeTest_TheFrostVariantKeepsTheSameWindows(t *testing.T) {
	trial := r2LoadThreat(t)
	var frost map[string]any
	for _, vv := range list(t, trial["variants"]) {
		v := obj(t, vv)
		if v["name"] == "frost" {
			frost = v
		}
	}
	if frost == nil {
		t.Fatal("no frost variant")
	}
	overrideRules := obj(t, obj(t, frost["override"])["rules"])
	noAggroOverride := obj(t, overrideRules["DPS-MAG-THREAT-NO-AGGRO"])
	if _, has := noAggroOverride["check"]; has {
		t.Fatalf("frost override carries a check: %v", noAggroOverride)
	}
	if _, has := r2NoAggro(t, trial)["min_reps"]; !has {
		t.Fatal("the base rule has no min_reps")
	}
}

func TestThreatCapWindowsTest_AHitInsideTheOpeningTwelveSecondsIsNotCounted(t *testing.T) {
	// bench-fix-1111 rep1: 3 hits at t=0.5, 1.2 and 2.1 from the opening mob, which had no refill mark
	var samples []rules.Row
	for at := range 141 {
		samples = append(samples, r2Sample(float64(at), r2BOT, 1000, 1000, true))
	}
	got := r2ScoreOne(t, r2ThreatTrial(), []rules.Row{r2Hit(5.0)}, samples, r2ThreatMarks())
	row := got["NO-AGGRO"]
	if row["verdict"] != "PASS" {
		t.Fatalf("verdict %v, want PASS (%v)", row["verdict"], row)
	}
	assertEqual(t, int64(0), row["k"])
}

func TestThreatCapWindowsTest_AHitAfterTheRefillWindowStillFails(t *testing.T) {
	var samples []rules.Row
	for at := range 141 {
		samples = append(samples, r2Sample(float64(at), r2BOT, 1000, 1000, true))
	}
	got := r2ScoreOne(t, r2ThreatTrial(), []rules.Row{r2Hit(100.0)}, samples, r2ThreatMarks())
	row := got["NO-AGGRO"]
	if row["verdict"] != "FAIL" {
		t.Fatalf("verdict %v, want FAIL (%v)", row["verdict"], row)
	}
	assertEqual(t, int64(1), row["k"])
}

func TestThreatCapWindowsTest_TheGuardCountsTheOpportunityWithoutTheDroppedWindows(t *testing.T) {
	// 100 s in combat minus the opening 12 s = 88 s < 90: the rule cannot PASS on the rest
	var samples []rules.Row
	for at := range 101 {
		samples = append(samples, r2Sample(float64(at), r2BOT, 1000, 1000, true))
	}
	got := r2ScoreOne(t, r2ThreatTrial(), nil, samples, r2ThreatMarks())
	row := got["NO-AGGRO"]
	if row["verdict"] != "INCONCLUSIVE" {
		t.Fatalf("verdict %v, want INCONCLUSIVE (%v)", row["verdict"], row)
	}
	if reason := pyjson.Str(row["reason"]); !strings.Contains(reason, "opportunity") {
		t.Fatalf("reason %q does not name the opportunity", reason)
	}
}

// r2TeaTrial is the small thistle-tea trial of TeaCrossRepCooldownTest.
func r2TeaTrial(across bool) map[string]any {
	check := map[string]any{
		"metric":                    "latency",
		"trigger":                   map[string]any{"kind": "mark", "mark": "energy_drain"},
		"response":                  map[string]any{"kind": "cast", "src": "$bot", "spell": "Restore Energy"},
		"within_s":                  int64(5),
		"debounce_responder_s":      int64(300),
		"debounce_responder_spells": []any{"Restore Energy"},
	}
	if across {
		check["debounce_responder_across_reps"] = true
	}
	return map[string]any{
		"id": "tea", "group": "tanks-melee", "class": "rogue", "spec": "combat",
		"player": map[string]any{"class": "warrior", "level": int64(60),
			"talents": map[string]any{"tree": "arms", "spells": []any{int64(16462), int64(12286)}}},
		"bot": map[string]any{"class": "rogue", "spec": "combat", "role": "dps", "level": int64(60)},
		"situation": map[string]any{"kind": "tank_damage",
			"mobs":    map[string]any{"role": "melee", "count": int64(1), "level_delta": int64(0)},
			"hp_band": []any{int64(40), int64(90)}, "floor_pct": int64(12)},
		"opportunity": map[string]any{"unit": "$player", "in_combat": true},
		"rules": []any{map[string]any{"id": "DPS-ROG-TEA", "expect_today": "?",
			"check": check, "pass_if": ">= 0.5", "min_n": int64(1)}},
	}
}

// r2Wall is datetime.fromtimestamp(second, utc).isoformat(timespec="milliseconds").
func r2Wall(base, offset float64) string {
	total := base + offset
	sec := math.Floor(total)
	ms := math.Round((total - sec) * 1000.0)
	if ms >= 1000.0 {
		sec++
		ms -= 1000.0
	}
	return time.Unix(int64(sec), int64(ms)*int64(time.Millisecond)).UTC().
		Format("2006-01-02T15:04:05.000+00:00")
}

// r2WriteRep writes one rep with wall-stamped marks and the tea casts.
func r2WriteRep(t *testing.T, trialDir, name string, baseSeconds, duration float64, drains, teas []float64) {
	t.Helper()
	rep := filepath.Join(trialDir, name)
	writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{
		"ok":     true,
		"guids":  map[string]any{"player": r2PLAYER, "bot": r2BOT, "mobs": []any{r2MOBG}},
		"config": map[string]any{},
	})
	marks := []rules.Row{{"kind": "fight_start", "wall": r2Wall(baseSeconds, 0)}}
	for _, at := range drains {
		marks = append(marks, rules.Row{"kind": "energy_drain", "wall": r2Wall(baseSeconds, at),
			"to": int64(0)})
	}
	marks = append(marks, rules.Row{"kind": "fight_end", "wall": r2Wall(baseSeconds, duration),
		"ended": "duration"})
	events := make([]rules.Row, 0, len(teas))
	for _, at := range teas {
		events = append(events, rules.Row{
			"t": at, "wall": nil, "kind": "cast", "src": "b", "src_guid": r2BOT,
			"dst": "b", "dst_guid": r2BOT, "src_unit": "$bot", "dst_unit": "$bot",
			"spell_id": int64(9512), "spell": "Restore Energy", "amount": int64(0),
			"dst_hp_pct": int64(100), "src_role": "bot", "dst_role": "bot", "origin": "ta",
		})
	}
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), r2Rows(marks))
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), r2Rows(events))
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), nil)
}

// r2TeaScore scores rep1 (tea at t=95) and rep2 over the same 300 s cooldown.
func r2TeaScore(t *testing.T, across bool) map[string]any {
	t.Helper()
	tdir := filepath.Join(t.TempDir(), "run", "tea")
	r2WriteRep(t, tdir, "rep1", 1_800_000_000.0, 120, []float64{20}, []float64{20.1, 95.0})
	r2WriteRep(t, tdir, "rep2", 1_800_000_100.0, 400, []float64{50, 100, 350}, []float64{350.1})
	out := score.ScoreTrial(tdir, r2TeaTrial(across))
	return obj(t, list(t, out["verdicts"])[0])
}

// r2TeaDetail returns the detail row of one rep.
func r2TeaDetail(t *testing.T, got map[string]any, rep string) map[string]any {
	t.Helper()
	for _, dv := range list(t, got["detail"]) {
		d := obj(t, dv)
		if d["rep"] == rep {
			return d
		}
	}
	t.Fatalf("no detail row for %s in %v", rep, got["detail"])
	return nil
}

func TestTeaCrossRepCooldownTest_ADrainInsideThePriorRepsTeaCooldownIsNoChance(t *testing.T) {
	// bench-fix-1111: rep2's drains 241 s and 282 s after the rep1 tea were scored as misses
	got := r2TeaScore(t, true)
	assertEqual(t, "PASS", got["verdict"])
	assertEqual(t, int64(2), got["k"])
	assertEqual(t, int64(2), got["n"])
	rep2 := r2TeaDetail(t, got, "rep2")
	assertEqual(t, int64(1), rep2["k"])
	assertEqual(t, int64(1), rep2["n"])
}

func TestTeaCrossRepCooldownTest_WithoutTheOptionTheSameRepsScoreTheMisses(t *testing.T) {
	got := r2TeaScore(t, false)
	assertEqual(t, "FAIL", got["verdict"])
	assertEqual(t, int64(2), got["k"])
	assertEqual(t, int64(4), got["n"])
	rep2 := r2TeaDetail(t, got, "rep2")
	assertEqual(t, int64(1), rep2["k"])
	assertEqual(t, int64(3), rep2["n"])
}

func TestTeaCrossRepCooldownTest_TheTrialCarriesTheOptionAndSaysWhy(t *testing.T) {
	trial, err := scenario.LoadTrial("edge/dps-rogue-thistle-tea.json")
	if err != nil {
		t.Fatalf("load dps-rogue-thistle-tea: %v", err)
	}
	var rule map[string]any
	for _, rv := range list(t, trial["rules"]) {
		r := obj(t, rv)
		if r["id"] == "DPS-ROG-TEA" {
			rule = r
		}
	}
	if rule == nil {
		t.Fatal("no DPS-ROG-TEA rule")
	}
	if !pyjson.Truthy(obj(t, rule["check"])["debounce_responder_across_reps"]) {
		t.Fatal("the trial rule does not carry debounce_responder_across_reps")
	}
	if why := pyjson.Str(rule["why_expect"]); !strings.Contains(why, "item cooldown") {
		t.Fatalf("why_expect %q does not name the item cooldown", why)
	}
}

func TestTeaCrossRepCooldownTest_TheOptionIsValidated(t *testing.T) {
	bad := r2TeaTrial(true)
	obj(t, list(t, bad["rules"])[0])["check"].(map[string]any)["debounce_responder_across_reps"] = false
	errs := scenario.Validate(bad)
	if !strings.Contains(strings.Join(errs, "\n"), "debounce_responder_across_reps") {
		t.Fatalf("validate %v does not reject across_reps=false", errs)
	}
	missing := r2TeaTrial(true)
	delete(obj(t, list(t, missing["rules"])[0])["check"].(map[string]any), "debounce_responder_s")
	errs = scenario.Validate(missing)
	if !strings.Contains(strings.Join(errs, "\n"), "debounce_responder_across_reps") {
		t.Fatalf("validate %v does not reject a missing debounce_responder_s", errs)
	}
}

func TestTwoTankRegressionGuardTest_TheStaleFailExpectationIsGone(t *testing.T) {
	trials := [][2]string{
		{"priest-holy-pws-def-arms-2tank", "PRI-PWS-DEF-ARMS-2TANK"},
		{"priest-holy-pws-bear-bal-2tank", "PRI-PWS-BEAR-BAL-2TANK"},
	}
	for _, pair := range trials {
		tid, rid := pair[0], pair[1]
		trial, err := scenario.LoadTrial("healers/" + tid + ".json")
		if err != nil {
			t.Fatalf("%s: load: %v", tid, err)
		}
		var rule map[string]any
		for _, rv := range list(t, trial["rules"]) {
			r := obj(t, rv)
			if r["id"] == rid {
				rule = r
			}
		}
		if rule == nil {
			t.Fatalf("%s: no rule %s", tid, rid)
		}
		assertEqual(t, "PASS", rule["expect_today"])
		assertEqual(t, int64(3), rule["min_reps"])
		if desc := pyjson.Str(rule["desc"]); !strings.Contains(desc, "Regression guard") {
			t.Fatalf("%s: desc %q is not a regression guard", tid, desc)
		}
		if why := pyjson.Str(rule["why_expect"]); !strings.Contains(why, "#1557") {
			t.Fatalf("%s: why_expect %q does not cite #1557", tid, why)
		}
		if errs := scenario.Validate(trial); len(errs) != 0 {
			t.Fatalf("%s: validate: %v", tid, errs)
		}
	}
}

func TestPrepullUnauraTest_TheTrialStripsShadowformAndValidates(t *testing.T) {
	trial, err := scenario.LoadTrial("healers/priest-shadow-form-prepull.json")
	if err != nil {
		t.Fatalf("load priest-shadow-form-prepull: %v", err)
	}
	sit := obj(t, trial["situation"])
	assertEqual(t, []any{int64(15473)}, sit["prepull_unaura"])
	if errs := scenario.Validate(trial); len(errs) != 0 {
		t.Fatalf("validate: %v", errs)
	}
	for _, bad := range []any{[]any{}, "15473", []any{int64(0)}, []any{true}} {
		trial, err := scenario.LoadTrial("healers/priest-shadow-form-prepull.json")
		if err != nil {
			t.Fatalf("load priest-shadow-form-prepull: %v", err)
		}
		obj(t, trial["situation"])["prepull_unaura"] = bad
		errs := scenario.Validate(trial)
		found := false
		for _, e := range errs {
			if strings.Contains(e, "prepull_unaura") {
				found = true
			}
		}
		if !found {
			t.Fatalf("validate %v does not reject prepull_unaura=%v", errs, bad)
		}
	}
}
