package pooled

// Translated from tests/test_addpickup_pooled.py: pooled scoring (`pool: true`) and the
// drink-under-attack rule (paladin add-pickup).
//
// Why (paladin-addpickup WHY.md, 2026-11-12): with 0-5 triggers per repeat and min_n 3 applied
// per repeat, one repeat decided the trial's verdict, and the card that PASSed did so because
// the guard dropped the two repeats with the misses. A pooled rule takes ONE verdict from the
// summed k/n over the valid repeats and applies min_n to the pooled n.
//
// Second half: TNK-PAL-NO-DRINK-UNDER-ATTACK. The bot's Drink sets a 15 s meal duration, so a
// refill mob that opens on the player or the healer during the meal is unanswered until the
// sleep runs out. The rule counts Drink casts by the bot while any of those party members has
// a hostile mob on it (packets first, the mob's sample target as the fallback).

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
	"github.com/pjerra/forever-agent/internal/trials/spells"
)

const (
	apkPlayer = "0x0000000000000001"
	apkBot    = "0x0000000000000002"
	apkMob    = "0xF13000000000ABCD"
	apkHealer = "0x0000000000000003"
)

// apkDrink is the lowest spell id of "Drink" (spells.ids_for("Drink")[0]).
var apkDrink = apkFirstSpellID("Drink")

func apkFirstSpellID(name string) int64 {
	ids := make([]int64, 0)
	for id := range spells.IDsFor(name) {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) == 0 {
		return 0
	}
	return ids[0]
}

// apkLatency is the POOLED latency rule: mob damage on the player answered by a bot cast
// within 5 s, 70 % of the time, pooled over at least 3 triggers.
func apkLatency() map[string]any {
	return map[string]any{
		"id": "POOLED", "desc": "x", "source": "s", "expect_today": "?", "why_expect": "x",
		"check": map[string]any{
			"metric": "latency",
			"trigger": map[string]any{
				"kind": "damage", "src": "$mob", "dst": "$player",
			},
			"response":     map[string]any{"kind": "cast", "src": "$bot"},
			"within_s":     int64(5),
			"response_dst": "$trigger.src",
		},
		"pass_if": ">= 0.7", "min_n": int64(3), "pool": true,
	}
}

func apkMobDamage(t float64) map[string]any {
	return map[string]any{
		"t": t, "kind": "damage", "src": "m", "src_guid": apkMob, "dst": "p",
		"dst_guid": apkPlayer, "src_unit": "mob:" + strings.ToLower(apkMob), "dst_unit": "$player",
		"spell_id": nil, "spell": nil, "amount": int64(1), "dst_hp_pct": 50.0,
		"src_role": "mob", "dst_role": "player", "origin": "ta",
	}
}

// apkBotCast is a bot cast; a nil spellID means the Drink id, as the Python default does.
func apkBotCast(t float64, spell string, spellID *int64, dstGUID string) map[string]any {
	var sid any
	if spellID != nil {
		sid = *spellID
	} else {
		sid = apkDrink
	}
	return map[string]any{
		"t": t, "kind": "cast", "src": "b", "src_guid": apkBot, "dst": "", "dst_guid": dstGUID,
		"src_unit": "$bot", "dst_unit": "", "spell_id": sid,
		"spell": spell, "amount": nil, "dst_hp_pct": nil, "src_role": "bot",
		"dst_role": "other", "origin": "ta",
	}
}

func apkSample(t float64, guid, unit, target string, hp int64) map[string]any {
	return map[string]any{
		"t": t, "guid": guid, "unit": unit, "target": target, "hp": hp, "max_hp": hp,
		"power": int64(100), "max_power": int64(100), "auras": []any{}, "casting": nil,
		"in_combat": true, "x": 0.0, "y": 0.0, "z": 0.0,
	}
}

// apkRep writes one pre-scored repeat directory (hand-written verdicts.json).
func apkRep(t *testing.T, root, name, verdict string, k, n int64, reason string) string {
	t.Helper()
	rep := filepath.Join(root, name)
	if err := os.MkdirAll(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	var value any
	if n != 0 {
		value = float64(k) / float64(n)
	}
	writeJSON(t, filepath.Join(rep, "verdicts.json"), map[string]any{
		"trial": "t", "rep": name, "ok_setup": true, "scored_s": 180.0,
		"rules": []any{map[string]any{
			"id": "POOLED", "verdict": verdict, "value": value,
			"k": k, "n": n, "evidence": []any{}, "guard": "", "reason": reason,
			"invalid_reason": "",
		}},
	})
	return rep
}

// apkTrial is the Python tests' test_trials._trial() fixture.
func apkTrial() map[string]any {
	return map[string]any{
		"id":    "priest-holy-pws-def-arms",
		"title": "Holy priest: no shield on an Arms warrior tanking in Defensive Stance",
		"group": "healers",
		"class": "priest",
		"spec":  "holy",
		"player": map[string]any{
			"class": "warrior", "level": int64(35),
			"talents": map[string]any{"tree": "arms", "spells": []any{int64(12285), int64(12295)}},
			"stance":  int64(71), "forced_role": nil, "auras": []any{}, "items": []any{},
		},
		"bot": map[string]any{
			"class": "priest", "spec": "holy", "role": "healer", "level": int64(35),
			"gear": "blue", "items": []any{map[string]any{"name": "Morning Glory Dew", "count": int64(20)}},
			"mana_pct": nil, "cc_mark": nil,
		},
		"situation": map[string]any{
			"kind":    "tank_damage",
			"mobs":    map[string]any{"role": "melee", "count": int64(1), "level_delta": int64(1), "refill": true},
			"hp_band": []any{int64(40), int64(90)}, "floor_pct": int64(30), "victim": "player",
			"pull_after_s": int64(0), "ooc_wait_s": int64(0), "debuff": nil,
		},
		"setup_checks": []any{"player_level", "player_visible", "player_talents", "player_stance",
			"player_forced_role", "bot_present", "bot_level",
			"bot_spec", "grouped", "mobs_ready", "config_read"},
		"duration_s":  int64(180),
		"repeats":     int64(2),
		"min_valid_s": int64(120),
		"opportunity": map[string]any{"unit": "$player", "hp_lt": int64(90), "hp_gt": "$critical",
			"in_combat": true},
		"rules": []any{map[string]any{
			"id": "PRI-PWS-DEF-ARMS", "desc": "no shield on the player above critical in combat",
			"source":       "ai/playerbot/strategy/priest/PriestActions.h:42; issue #1553",
			"expect_today": "FAIL",
			"why_expect":   "IsTank reads talents/forced role, not stance",
			"check": map[string]any{
				"metric": "count",
				"where": map[string]any{"kind": "cast", "src": "$bot", "dst": "$player",
					"spell": "Power Word: Shield", "dst_hp_gt": "$critical", "in_combat": true},
			},
			"pass_if": "== 0", "min_opportunity_s": int64(90), "min_n": int64(0),
		}},
	}
}

// apkPooledTrial is _trial(**over) for the pooled tests: the LATENCY rule plus overrides.
func apkPooledTrial(over map[string]any) map[string]any {
	trial := apkTrial()
	trial["id"] = "t"
	rule := apkLatency()
	for k, v := range over {
		rule[k] = v
	}
	trial["rules"] = []any{rule}
	return trial
}

func apkVerdictRow(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	verdicts, _ := pyjson.List(out["verdicts"])
	if len(verdicts) == 0 {
		t.Fatalf("no verdicts in %v", out)
	}
	row, _ := pyjson.Object(verdicts[0])
	return row
}

func apkIntField(t *testing.T, row map[string]any, key string) int64 {
	t.Helper()
	n, ok := pyjson.Int64(row[key])
	if !ok {
		t.Fatalf("%s is not an int: %v", key, row[key])
	}
	return n
}

type apkKNT struct {
	Verdict string
	K, N    int64
}

func apkResult(t *testing.T, row map[string]any) apkKNT {
	t.Helper()
	return apkKNT{Verdict: pyjson.Str(row["verdict"]), K: apkIntField(t, row, "k"), N: apkIntField(t, row, "n")}
}

func TestPooledTrialTest_ZeroTriggerRepeatDoesNotVetoTheFiveTriggerRepeat(t *testing.T) {
	// the failing case before the change: min_n 3 read PER REPEAT, so the rep with 0
	// triggers was INCONCLUSIVE on its own and the 5-trigger rep carried the only vote;
	// pooled, the summed n is the 3-trigger minimum's denominator.
	tmp := t.TempDir()
	apkRep(t, tmp, "rep1", "INCONCLUSIVE", 0, 0, "")
	apkRep(t, tmp, "rep2", "PASS", 5, 5, "")
	out := score.ScoreTrial(tmp, apkPooledTrial(nil))
	row := apkVerdictRow(t, out)
	if got, want := apkResult(t, row), (apkKNT{"PASS", 5, 5}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPooledTrialTest_AnUnknownAttackStateMakesThePooledVerdictInconclusive(t *testing.T) {
	// round 2: the unknown repeat is INCONCLUSIVE with reason "attack state unknown"; it must not
	// be erased from the pool, so the clean PASS repeat cannot decide the trial.
	tmp := t.TempDir()
	apkRep(t, tmp, "rep1", "INCONCLUSIVE", 0, 1, "attack state unknown")
	apkRep(t, tmp, "rep2", "PASS", 3, 3, "")
	out := score.ScoreTrial(tmp, apkPooledTrial(nil))
	row := apkVerdictRow(t, out)
	if got := pyjson.Str(row["verdict"]); got != "INCONCLUSIVE" {
		t.Fatalf("verdict = %q, want INCONCLUSIVE", got)
	}
	reason := pyjson.Str(row["reason"])
	if !strings.Contains(reason, "attack state unknown") {
		t.Fatalf("reason = %q, want it to mention the unknown attack state", reason)
	}
	if !strings.Contains(reason, "rep1") {
		t.Fatalf("reason = %q, want it to name rep1", reason)
	}
}

func TestPooledTrialTest_ANoTriggerRepeatIsStillErasedFromThePool(t *testing.T) {
	// benign INCONCLUSIVE reasons (no opportunity, no trigger) keep the old behaviour: they add
	// nothing, and the PASS repeat still decides the pooled verdict.
	tmp := t.TempDir()
	apkRep(t, tmp, "rep1", "INCONCLUSIVE", 0, 0, "n 0 < min_n 3")
	apkRep(t, tmp, "rep2", "PASS", 3, 3, "")
	out := score.ScoreTrial(tmp, apkPooledTrial(nil))
	row := apkVerdictRow(t, out)
	if got := pyjson.Str(row["verdict"]); got != "PASS" {
		t.Fatalf("verdict = %q, want PASS", got)
	}
}

func TestPooledTrialTest_PooledMinNReadsThePooledTotal(t *testing.T) {
	// each repeat has n=2, under min_n 3 on its own; the pooled n is 4.
	tmp := t.TempDir()
	apkRep(t, tmp, "rep1", "PASS", 1, 2, "")
	apkRep(t, tmp, "rep2", "PASS", 2, 2, "")
	out := score.ScoreTrial(tmp, apkPooledTrial(nil))
	row := apkVerdictRow(t, out)
	if got, want := apkResult(t, row), (apkKNT{"PASS", 3, 4}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPooledTrialTest_OneVerdictFromTheSummedKOverN(t *testing.T) {
	// per-repeat majority says PASS (2 of 3); pooled 2/7 is under 0.7 -> FAIL.
	tmp := t.TempDir()
	apkRep(t, tmp, "rep1", "PASS", 1, 1, "")
	apkRep(t, tmp, "rep2", "PASS", 1, 1, "")
	apkRep(t, tmp, "rep3", "FAIL", 0, 5, "")
	out := score.ScoreTrial(tmp, apkPooledTrial(nil))
	row := apkVerdictRow(t, out)
	if got := pyjson.Str(row["verdict"]); got != "FAIL" {
		t.Fatalf("verdict = %q, want FAIL", got)
	}
}

func TestPooledTrialTest_PooledTotalBelowMinNIsInconclusive(t *testing.T) {
	tmp := t.TempDir()
	apkRep(t, tmp, "rep1", "PASS", 1, 1, "")
	apkRep(t, tmp, "rep2", "FAIL", 0, 1, "")
	out := score.ScoreTrial(tmp, apkPooledTrial(nil))
	row := apkVerdictRow(t, out)
	if got := pyjson.Str(row["verdict"]); got != "INCONCLUSIVE" {
		t.Fatalf("verdict = %q, want INCONCLUSIVE", got)
	}
	if reason := pyjson.Str(row["reason"]); !strings.Contains(reason, "pooled n 2 < min_n 3") {
		t.Fatalf("reason = %q, want pooled n 2 < min_n 3", reason)
	}
}

func TestPooledTrialTest_ScoreRepSkipsThePerRepeatMinNForAPooledRule(t *testing.T) {
	// score_rep must not turn each pooled repeat INCONCLUSIVE before score_trial can pool it.
	run := t.TempDir()
	for _, tc := range []struct {
		pooled   bool
		expected string
	}{{true, "PASS"}, {false, "INCONCLUSIVE"}} {
		trial := apkPooledTrial(nil)
		trial["min_valid_s"] = int64(0)
		rule, _ := pyjson.Object(list(t, trial["rules"])[0])
		rule["pool"] = tc.pooled
		rule["min_n"] = int64(5)
		suffix := "_n"
		if tc.pooled {
			suffix = "_p"
		}
		rep := filepath.Join(run, pyjson.Str(trial["id"])+suffix, "rep1")
		if err := os.MkdirAll(rep, 0o755); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(run, "RUN.json"), map[string]any{
			"critical_health": int64(20), "low_health": int64(50), "medium_health": int64(70),
			"trials": []any{pyjson.Str(trial["id"])},
		})
		writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{
			"ok": true, "guids": map[string]any{"player": apkPlayer, "bot": apkBot,
				"mobs": []any{apkMob}},
		})
		var sampleRows []any
		for i := range 60 {
			sampleRows = append(sampleRows, apkSample(float64(i), apkPlayer, "player", "", 1000))
		}
		writeJSONL(t, filepath.Join(rep, "samples.jsonl"), sampleRows)
		if err := os.WriteFile(filepath.Join(rep, "marks.jsonl"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		taunt := int64(355)
		var eventRows []any
		for _, at := range []float64{10.0, 20.0} {
			eventRows = append(eventRows, apkMobDamage(at))
			eventRows = append(eventRows, apkBotCast(at+1.0, "Taunt", &taunt, apkMob))
		}
		writeJSONL(t, filepath.Join(rep, "events.jsonl"), eventRows)
		out := score.ScoreRep(rep, trial, nil)
		ruleRows, _ := pyjson.List(out["rules"])
		first, _ := pyjson.Object(ruleRows[0])
		if got := pyjson.Str(first["verdict"]); got != tc.expected {
			t.Fatalf("pooled=%v: verdict = %q, want %q", tc.pooled, got, tc.expected)
		}
	}
}

func TestPooledTrialTest_PooledCountOfZeroAndFive(t *testing.T) {
	// a count rule's per-repeat n is 1: pooled value is the summed count over the valid
	// repeats (5/2 = 2.5 here), so `pass_if == 0` passes only when every repeat is 0.
	rule := map[string]any{
		"id": "POOLED", "desc": "x", "source": "s", "expect_today": "?", "why_expect": "x",
		"check": map[string]any{
			"metric": "count",
			"where":  map[string]any{"kind": "cast", "src": "$bot", "spell": "Drink"},
		},
		"pass_if": "== 0", "pool": true,
	}
	trial := apkTrial()
	trial["rules"] = []any{rule}
	tmp := t.TempDir()
	apkRep(t, tmp, "rep1", "PASS", 0, 1, "")
	apkRep(t, tmp, "rep2", "FAIL", 5, 1, "")
	out := score.ScoreTrial(tmp, trial)
	if got := pyjson.Str(apkVerdictRow(t, out)["verdict"]); got != "FAIL" {
		t.Fatalf("verdict = %q, want FAIL", got)
	}
	tmp2 := t.TempDir()
	apkRep(t, tmp2, "rep1", "PASS", 0, 1, "")
	apkRep(t, tmp2, "rep2", "PASS", 0, 1, "")
	out = score.ScoreTrial(tmp2, trial)
	if got := pyjson.Str(apkVerdictRow(t, out)["verdict"]); got != "PASS" {
		t.Fatalf("verdict = %q, want PASS", got)
	}
}

func apkErrs(t *testing.T, rule map[string]any, trial map[string]any) []string {
	t.Helper()
	if trial == nil {
		trial = apkTrial()
	}
	trial["rules"] = []any{rule}
	return scenario.Validate(trial)
}

func apkFilter(errs []string, needle string) []string {
	var out []string
	for _, e := range errs {
		if strings.Contains(e, needle) {
			out = append(out, e)
		}
	}
	return out
}

func TestPooledValidatorTest_PoolTrueIsValidOnACount(t *testing.T) {
	rule := apkLatency()
	rule["check"] = map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"}}
	rule["pass_if"] = "== 0"
	if errs := apkFilter(apkErrs(t, rule, nil), "pool"); len(errs) != 0 {
		t.Fatalf("unexpected pool errors: %v", errs)
	}
}

func TestPooledValidatorTest_PoolMustBeTrueAndOnlyOnAKOverNMetric(t *testing.T) {
	for _, bad := range []any{false, "yes", int64(1)} {
		rule := apkLatency()
		rule["pool"] = bad
		if errs := apkFilter(apkErrs(t, rule, nil), "pool"); len(errs) == 0 {
			t.Fatalf("pool=%v: expected a pool error", bad)
		}
	}
	uptime := map[string]any{
		"id": "POOLED", "desc": "x", "source": "s", "expect_today": "?", "why_expect": "x",
		"check":   map[string]any{"metric": "uptime", "unit": "$player", "aura": []any{int64(71)}},
		"pass_if": ">= 0.5", "pool": true,
	}
	if errs := apkFilter(apkErrs(t, uptime, nil), "pool applies only"); len(errs) == 0 {
		t.Fatalf("expected 'pool applies only' for an uptime rule")
	}
}

func TestPooledValidatorTest_PartyUnderAttackWantsANonEmptyListOfPartyUnits(t *testing.T) {
	errs := func(units any) []string {
		rule := apkLatency()
		rule["check"] = map[string]any{"metric": "count",
			"where": map[string]any{"kind": "cast", "party_under_attack": units}}
		return apkFilter(apkErrs(t, rule, nil), "party_under_attack")
	}
	if got := errs([]any{"$player", "$extras"}); len(got) != 0 {
		t.Fatalf("unexpected errors for a valid party list: %v", got)
	}
	for _, bad := range []any{[]any{}, "$player", []any{"$nope"}, []any{"$mob"}, []any{int64(3)}, []any{""}} {
		if got := errs(bad); len(got) == 0 {
			t.Fatalf("party_under_attack=%v: expected an error", bad)
		}
	}
}

// apkDrinkVictims builds a victims_by_guid table from (t, victim) pairs for the mob.
func apkDrinkVictims(pairs [][2]any) map[string][]events.AttackVictim {
	if pairs == nil {
		return map[string][]events.AttackVictim{}
	}
	out := map[string][]events.AttackVictim{}
	for _, pair := range pairs {
		victim, _ := pair[1].(string)
		out[strings.ToLower(apkMob)] = append(out[strings.ToLower(apkMob)],
			events.AttackVictim{T: pair[0].(float64), Victim: victim})
	}
	return out
}

// apkDrinkCtx is the Python _ctx(): the trial's guids and the victim table.
func apkDrinkCtx(trial map[string]any, victims map[string][]events.AttackVictim) *rules.Ctx {
	if victims == nil {
		victims = map[string][]events.AttackVictim{}
	}
	return &rules.Ctx{
		Guids: map[string]any{
			"player": apkPlayer, "bot": apkBot, "extras": []any{apkHealer},
			"mobs": []any{apkMob},
		},
		Thresholds:        map[string]any{},
		ThresholdsAssumed: false,
		Trial:             trial,
		VictimsByGUID:     victims,
	}
}

// apkDrinkFixture loads the real tank-paladin-add-pickup trial and its drink rule.
func apkDrinkFixture(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	trial, err := scenario.LoadTrial("tanks/tank-paladin-add-pickup.json")
	if err != nil {
		t.Fatalf("load tank-paladin-add-pickup: %v", err)
	}
	rulesList, _ := pyjson.List(trial["rules"])
	for _, rv := range rulesList {
		rule, _ := pyjson.Object(rv)
		if pyjson.Str(rule["id"]) == "TNK-PAL-NO-DRINK-UNDER-ATTACK" {
			return trial, rule
		}
	}
	t.Fatalf("rule TNK-PAL-NO-DRINK-UNDER-ATTACK not found")
	return nil, nil
}

// apkDrinkSamples is the setUp fixture: the mob's sample target at 9 (player), 29 and 45
// (bot), plus a 1 Hz player sample for 0..59.
func apkDrinkSamples() []rules.Row {
	mobUnit := "mob:" + strings.ToLower(apkMob)
	rows := []rules.Row{
		apkSample(9.0, apkMob, mobUnit, apkPlayer, 500),
		apkSample(29.0, apkMob, mobUnit, apkBot, 500),
		apkSample(45.0, apkMob, mobUnit, apkBot, 500),
	}
	for i := range 60 {
		rows = append(rows, apkSample(float64(i), apkPlayer, "player", "", 1000))
	}
	return rows
}

func apkCastRows(ts ...float64) []rules.Row {
	out := make([]rules.Row, 0, len(ts))
	for _, at := range ts {
		out = append(out, apkBotCast(at, "Drink", nil, ""))
	}
	return out
}

func TestDrinkUnderAttackTest_DrinkWhileAMobIsOnThePlayerCounts(t *testing.T) {
	// t=10: the packet at 9 has the player as victim -> a counted Drink cast.
	// t=30 and t=50: the mob is on the bot (the tank), which is not in the party list.
	trial, rule := apkDrinkFixture(t)
	victims := apkDrinkVictims([][2]any{
		{9.0, apkPlayer}, {29.0, apkBot}, {45.0, apkBot},
	})
	res := rules.Evaluate(rule, apkCastRows(10.0, 30.0, 50.0), apkDrinkSamples(), nil, apkDrinkCtx(trial, victims))
	if res.K != 1 || res.N != 1 || res.Verdict != "FAIL" {
		t.Fatalf("got k=%d n=%d verdict=%q, want 1/1 FAIL", res.K, res.N, res.Verdict)
	}
}

func TestDrinkUnderAttackTest_TheSampleTargetIsTheFallbackWhenNoPacketIsFresh(t *testing.T) {
	trial, rule := apkDrinkFixture(t)
	res := rules.Evaluate(rule, apkCastRows(10.0), apkDrinkSamples(), nil, apkDrinkCtx(trial, nil))
	if res.K != 1 || res.Verdict != "FAIL" {
		t.Fatalf("got k=%d verdict=%q, want 1 FAIL", res.K, res.Verdict)
	}
}

func TestDrinkUnderAttackTest_NoDrinkWhileThePartyIsAttackedPasses(t *testing.T) {
	trial, rule := apkDrinkFixture(t)
	victims := apkDrinkVictims([][2]any{{9.0, apkBot}})
	res := rules.Evaluate(rule, apkCastRows(10.0), apkDrinkSamples(), nil, apkDrinkCtx(trial, victims))
	if res.K != 0 || res.Verdict != "PASS" {
		t.Fatalf("got k=%d verdict=%q, want 0 PASS", res.K, res.Verdict)
	}
}

func TestDrinkUnderAttackTest_AHealerAttackPacketCountsWhenTheSampleIsStale(t *testing.T) {
	// t=32: the mob's last sample (29, on the bot) is 3 s old; the packet at 29 has the healer.
	trial, rule := apkDrinkFixture(t)
	victims := apkDrinkVictims([][2]any{{29.0, apkHealer}})
	res := rules.Evaluate(rule, apkCastRows(32.0), apkDrinkSamples(), nil, apkDrinkCtx(trial, victims))
	if res.K != 1 || res.Verdict != "FAIL" {
		t.Fatalf("got k=%d verdict=%q, want 1 FAIL", res.K, res.Verdict)
	}
}

func TestDrinkUnderAttackTest_AStaleSampleWithNoNewerPacketStillDecides(t *testing.T) {
	// bench-1113: t=32, the mob's last sample (29, on the tank) is 3 s old and there is no attack
	// packet. Every target switch emits an attack_start packet, so with none the last read target
	// is still current: the Drink is clean, and the verdict records the state it was decided on.
	trial, rule := apkDrinkFixture(t)
	res := rules.Evaluate(rule, apkCastRows(32.0), apkDrinkSamples(), nil, apkDrinkCtx(trial, nil))
	if res.K != 0 || res.Verdict != "PASS" {
		t.Fatalf("got k=%d verdict=%q, want 0 PASS", res.K, res.Verdict)
	}
	evidence := strings.Join(res.Evidence, " ")
	if !strings.Contains(evidence, "not under attack") {
		t.Fatalf("evidence = %q, want 'not under attack'", evidence)
	}
	if !strings.Contains(evidence, "-> $bot (sample t=29.0)") {
		t.Fatalf("evidence = %q, want '-> $bot (sample t=29.0)'", evidence)
	}
}

func TestDrinkUnderAttackTest_AnAttackStopAfterTheSampleIsNotAttacking(t *testing.T) {
	// Round-1 MEDIUM: the newest event wins. The mob's last sample (29, on the tank) is older
	// than its stop at 30: the stop says it dropped its target, so the Drink is clean.
	trial, rule := apkDrinkFixture(t)
	victims := apkDrinkVictims([][2]any{{30.0, ""}})
	res := rules.Evaluate(rule, apkCastRows(32.0), apkDrinkSamples(), nil, apkDrinkCtx(trial, victims))
	if res.K != 0 || res.Verdict != "PASS" {
		t.Fatalf("got k=%d verdict=%q, want 0 PASS", res.K, res.Verdict)
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "not under attack") {
		t.Fatalf("evidence = %q, want 'not under attack'", res.Evidence)
	}
}

func TestDrinkUnderAttackTest_UnknownAttackStateIsInconclusiveNotAPass(t *testing.T) {
	// A mob that was live (an attack packet inside the window) with no sample at all saying who it
	// is on at the cast is still unknown: a switch on the cast's own t is its echo, not its cause,
	// and missing attack-state evidence is not "not under attack".
	trial, rule := apkDrinkFixture(t)
	var samples []rules.Row
	for i := range 60 {
		samples = append(samples, apkSample(float64(i), apkPlayer, "player", "", 1000))
	}
	victims := apkDrinkVictims([][2]any{{32.0, apkBot}})
	res := rules.Evaluate(rule, apkCastRows(32.0), samples, nil, apkDrinkCtx(trial, victims))
	if res.K != 0 || res.Verdict != "INCONCLUSIVE" {
		t.Fatalf("got k=%d verdict=%q, want 0 INCONCLUSIVE", res.K, res.Verdict)
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "attack state unknown") {
		t.Fatalf("evidence = %q, want 'attack state unknown'", res.Evidence)
	}
	// score.py's pooled guard keys on this exact reason.
	if res.Reason != "attack state unknown" {
		t.Fatalf("reason = %q, want 'attack state unknown'", res.Reason)
	}
}

func TestDrinkUnderAttackTest_ADeadMobIsNotUnknown(t *testing.T) {
	// The mob's freshest sample has hp 0: it was not live at the cast, so nothing was on the
	// party and the Drink is clean (no INCONCLUSIVE from a corpse).
	trial, rule := apkDrinkFixture(t)
	mobUnit := "mob:" + strings.ToLower(apkMob)
	samples := []rules.Row{
		apkSample(29.0, apkMob, mobUnit, apkBot, 0),
		apkSample(29.0, apkPlayer, "player", "", 1000),
	}
	res := rules.Evaluate(rule, apkCastRows(32.0), samples, nil, apkDrinkCtx(trial, nil))
	if res.K != 0 || res.Verdict != "PASS" {
		t.Fatalf("got k=%d verdict=%q, want 0 PASS", res.K, res.Verdict)
	}
}
