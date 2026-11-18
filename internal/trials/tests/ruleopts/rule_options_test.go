package ruleopts

// Translated from the Python bench's tests/test_rule_options.py:
// check.debounce_responder_s (the responder's own cooldown) and where.not_within_s_after_mark.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/spells"
)

const (
	roPlayer = "0x0000000000000001"
	roBot    = "0x0000000000000002"
	roMob    = "0xF13000000000ABCD"
)

// roTrial is tests/test_trials.py:32 _trial(): the baseline healer trial the rule tests hang on.
func roTrial() map[string]any {
	return map[string]any{
		"id":    "priest-holy-pws-def-arms",
		"title": "Holy priest: no shield on an Arms warrior tanking in Defensive Stance",
		"group": "healers",
		"class": "priest",
		"spec":  "holy",
		"player": map[string]any{
			"class": "warrior", "level": int64(35),
			"talents":     map[string]any{"tree": "arms", "spells": []any{int64(12285), int64(12295)}},
			"stance":      int64(71),
			"forced_role": nil,
			"auras":       []any{},
			"items":       []any{},
		},
		"bot": map[string]any{
			"class": "priest", "spec": "holy", "role": "healer", "level": int64(35),
			"gear":     "blue",
			"items":    []any{map[string]any{"name": "Morning Glory Dew", "count": int64(20)}},
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
		"opportunity": map[string]any{"unit": "$player", "hp_lt": int64(90), "hp_gt": "$critical", "in_combat": true},
		"rules":       []any{roDefaultRule()},
	}
}

func roDefaultRule() map[string]any {
	return map[string]any{
		"id":           "PRI-PWS-DEF-ARMS",
		"desc":         "no shield on the player above critical in combat",
		"source":       "ai/playerbot/strategy/priest/PriestActions.h:42; issue #1553",
		"expect_today": "FAIL",
		"why_expect":   "IsTank reads talents/forced role, not stance",
		"check": map[string]any{
			"metric": "count",
			"where": map[string]any{"kind": "cast", "src": "$bot", "dst": "$player",
				"spell": "Power Word: Shield", "dst_hp_gt": "$critical", "in_combat": true},
		},
		"pass_if":           "== 0",
		"min_opportunity_s": int64(90),
		"min_n":             int64(0),
	}
}

func roCtx(trial map[string]any) *rules.Ctx {
	return &rules.Ctx{
		Guids:      map[string]any{"player": roPlayer, "bot": roBot, "pet": nil, "mobs": []any{roMob}, "marked": nil},
		Thresholds: map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)},
		Trial:      trial,
	}
}

// roMobDamage is mob_damage(t).
func roMobDamage(t float64) map[string]any {
	return map[string]any{"t": t, "kind": "damage", "src": "m", "src_guid": roMob, "dst": "p", "dst_guid": roPlayer,
		"src_unit": "x", "dst_unit": "$player", "spell_id": nil, "spell": nil, "amount": int64(1),
		"dst_hp_pct": 40.0, "src_role": "mob", "dst_role": "player", "origin": "ta"}
}

// roBotHeal is bot_heal(t).
func roBotHeal(t float64) map[string]any {
	return map[string]any{"t": t, "kind": "heal", "src": "b", "src_guid": roBot, "dst": "p", "dst_guid": roPlayer,
		"src_unit": "$bot", "dst_unit": "$player", "spell_id": int64(1), "spell": "x", "amount": int64(1),
		"dst_hp_pct": 40.0, "src_role": "bot", "dst_role": "player", "origin": "ta"}
}

// roSample is tests/test_trials.py:96 _sample().
func roSample(t float64, guid string, hp any, combat bool, auras []any) map[string]any {
	if auras == nil {
		auras = []any{}
	}
	return map[string]any{"t": t, "wall": nil, "unit": "player", "guid": guid,
		"hp": hp, "max_hp": int64(1000), "power": int64(500), "max_power": int64(500),
		"auras": auras, "casting": nil, "in_combat": combat,
		"target": "", "x": 0.0, "y": 0.0, "z": 0.0}
}

// roExcluded is tests/test_trials.py:103 _excluded().
func roExcluded(a, b float64) map[string]any {
	return map[string]any{"t": a, "wall": nil, "kind": "excluded", "src": "", "src_guid": "",
		"dst": "", "dst_guid": "", "src_unit": "", "dst_unit": "",
		"spell_id": nil, "spell": nil, "amount": nil, "dst_hp_pct": nil,
		"src_role": "other", "dst_role": "other", "origin": "marks",
		"from": a, "to": b, "why": "test"}
}

// roLatencyRule is latency_rule(**check_over).
func roLatencyRule(over map[string]any) map[string]any {
	check := map[string]any{
		"metric": "latency", "trigger": map[string]any{"kind": "damage"},
		"response": map[string]any{"kind": "heal", "src": "$bot", "dst": "$player"}, "within_s": int64(3),
	}
	for k, v := range over {
		check[k] = v
	}
	return map[string]any{"id": "LAT", "desc": "x", "source": "s", "expect_today": "?", "why_expect": "x",
		"check": check, "pass_if": ">= 0.8", "min_opportunity_s": int64(0), "min_n": int64(0)}
}

func roEvaluate(rule map[string]any, rows, samples, marks []rules.Row, ctx *rules.Ctx) rules.Result {
	return rules.Evaluate(rule, rows, samples, marks, ctx)
}

// DebounceResponderTest: triggers at 10, 16 and 45; the bot answers only the first (at 11): its
// ability is on a 30 s cooldown.
func roDebounceRows() []rules.Row {
	return []rules.Row{roMobDamage(10.0), roMobDamage(16.0), roMobDamage(45.0), roBotHeal(11.0)}
}

func roRunLatency(t *testing.T, rows []rules.Row, over map[string]any) rules.Result {
	t.Helper()
	trial := roTrial()
	trial["rules"] = []any{roLatencyRule(over)}
	return roEvaluate(roLatencyRule(over), rows, nil, nil, roCtx(trial))
}

func TestDebounceResponderTest_OffByDefaultEveryTriggerCounts(t *testing.T) {
	res := roRunLatency(t, roDebounceRows(), nil)
	if res.K != 1 || res.N != 3 {
		t.Fatalf("k, n = %d, %d; want 1, 3", res.K, res.N)
	}
}

func TestDebounceResponderTest_ATriggerInsideTheRespondersCooldownIsSkippedNotMissed(t *testing.T) {
	// the failing case before the change: the option did not exist, 16 s counted as a miss
	res := roRunLatency(t, roDebounceRows(), map[string]any{"debounce_responder_s": int64(30)})
	if res.K != 1 || res.N != 2 {
		t.Fatalf("k, n = %d, %d; want 1, 2 (10 answered, 16 skipped, 45 missed)", res.K, res.N)
	}
}

func TestDebounceResponderTest_TheCooldownEndsAfterNSeconds(t *testing.T) {
	res := roRunLatency(t, roDebounceRows(), map[string]any{"debounce_responder_s": int64(5)})
	if res.K != 1 || res.N != 3 { // 16 is 5 s after the answer at 11: counted
		t.Fatalf("k, n = %d, %d; want 1, 3", res.K, res.N)
	}
	res = roRunLatency(t, roDebounceRows(), map[string]any{"debounce_responder_s": 5.5})
	if res.K != 1 || res.N != 2 {
		t.Fatalf("k, n = %d, %d; want 1, 2", res.K, res.N)
	}
}

func TestDebounceResponderTest_OnlyTheRespondersOwnResponsesStartACooldown(t *testing.T) {
	other := cloneRow(roBotHeal(11.0))
	other["src_guid"] = "0xF13000000000FFFF"
	other["src_unit"] = ""
	other["src_role"] = "other"
	rows := []rules.Row{roMobDamage(10.0), roMobDamage(16.0), roMobDamage(45.0), other}
	res := roRunLatency(t, rows, map[string]any{"debounce_responder_s": int64(30)})
	if res.K != 0 || res.N != 3 {
		t.Fatalf("k, n = %d, %d; want 0, 3", res.K, res.N)
	}
}

func TestDebounceResponderTest_TheCooldownIsOnTheAbilityNotTheTarget(t *testing.T) {
	// the answer at 11 went to another unit; a response to a different dst still starts the cooldown
	elsewhere := cloneRow(roBotHeal(11.0))
	elsewhere["dst_guid"] = "0x0000000000000009"
	elsewhere["dst_unit"] = ""
	rows := []rules.Row{roMobDamage(10.0), roMobDamage(16.0), roMobDamage(45.0), elsewhere}
	res := roRunLatency(t, rows, map[string]any{
		"response":             map[string]any{"kind": "heal", "src": "$bot"},
		"debounce_responder_s": int64(30),
	})
	if res.N != 2 {
		t.Fatalf("n = %d; want 2", res.N)
	}
}

func TestDebounceResponderTest_ACooldownSkippedTriggerDoesNotStartItsMobsDebounceWindow(t *testing.T) {
	// the reviewer's repro (counterspell): mob B casts at 10 and is answered at 11; mob A casts at 20
	// (inside the 30 s cooldown: skipped) and again at 45 (the bot is free: a miss that must count).
	// Before the fix the skipped t=20 trigger started mob A's 30 s debounce window and hid the t=45 miss.
	mobA, mobB := roMob, "0xF13000000000BEEF"
	castStart := func(t float64, guid string) map[string]any {
		row := roMobDamage(t)
		row["src_guid"] = guid
		return row
	}
	rows := []rules.Row{castStart(10.0, mobB), roBotHeal(11.0), castStart(20.0, mobA), castStart(45.0, mobA)}
	res := roRunLatency(t, rows, map[string]any{
		"debounce_responder_s": int64(30), "trigger_debounce_s": int64(30)})
	if res.K != 1 || res.N != 2 {
		t.Fatalf("k, n = %d, %d; want 1, 2", res.K, res.N)
	}
}

func roMinID(ids map[int64]bool) int64 {
	first := true
	var best int64
	for id := range ids {
		if first || id < best {
			best, first = id, false
		}
	}
	return best
}

func TestDebounceResponderTest_OnlyTheListedResponseSpellsStartTheCooldown(t *testing.T) {
	taunt := roMinID(spells.IDsFor("Taunt"))
	mocking := roMinID(spells.IDsFor("Mocking Blow"))
	resp := map[string]any{"kind": "heal", "src": "$bot", "spell": []any{"Taunt", "Mocking Blow"}}
	mb := cloneRow(roBotHeal(11.0))
	mb["spell_id"] = mocking
	mb["spell"] = "Mocking Blow"
	rows := []rules.Row{roMobDamage(10.0), mb, roMobDamage(16.0)}
	// every response spell starts the cooldown (the default): 16 is skipped
	res := roRunLatency(t, rows, map[string]any{"response": resp, "debounce_responder_s": int64(10)})
	if res.N != 1 {
		t.Fatalf("n = %d; want 1", res.N)
	}
	// only Taunt does: the Mocking Blow at 11 does not, so 16 counts (a miss)
	res = roRunLatency(t, rows, map[string]any{"response": resp, "debounce_responder_s": int64(10),
		"debounce_responder_spells": []any{"Taunt"}})
	if res.K != 1 || res.N != 2 {
		t.Fatalf("k, n = %d, %d; want 1, 2", res.K, res.N)
	}
	// a Taunt at 11 does start it
	tauntRow := cloneRow(roBotHeal(11.0))
	tauntRow["spell_id"] = taunt
	tauntRow["spell"] = "Taunt"
	rows = []rules.Row{roMobDamage(10.0), tauntRow, roMobDamage(16.0)}
	res = roRunLatency(t, rows, map[string]any{"response": resp, "debounce_responder_s": int64(10),
		"debounce_responder_spells": []any{"Taunt"}})
	if res.N != 1 {
		t.Fatalf("n = %d; want 1", res.N)
	}
}

func TestDebounceResponderTest_SpellsValidator(t *testing.T) {
	errs := func(spellList any, cd any, setCD bool) []string {
		trial := roTrial()
		rule := roLatencyRule(map[string]any{
			"response":                  map[string]any{"kind": "heal", "src": "$bot", "spell": []any{"Taunt", "Mocking Blow"}},
			"debounce_responder_spells": spellList,
		})
		if setCD {
			rule["check"].(map[string]any)["debounce_responder_s"] = cd
		}
		trial["rules"] = []any{rule}
		var out []string
		for _, e := range scenario.Validate(trial) {
			if contains(e, "debounce_responder_spells") {
				out = append(out, e)
			}
		}
		return out
	}
	if got := errs([]any{"Taunt"}, int64(10), true); len(got) != 0 {
		t.Fatalf("valid case: %v", got)
	}
	for _, bad := range []any{[]any{}, "Taunt", []any{""}, []any{int64(3)}, []any{"Kick"}} {
		if len(errs(bad, int64(10), true)) == 0 {
			t.Fatalf("want an error for %v", bad)
		}
	}
	// needs debounce_responder_s
	if len(errs([]any{"Taunt"}, nil, false)) == 0 {
		t.Fatal("want an error without debounce_responder_s")
	}
}

func TestDebounceResponderTest_Validator(t *testing.T) {
	errs := func(value any, metric string) []string {
		trial := roTrial()
		rule := roLatencyRule(map[string]any{"debounce_responder_s": value})
		rule["check"].(map[string]any)["metric"] = metric
		trial["rules"] = []any{rule}
		var out []string
		for _, e := range scenario.Validate(trial) {
			if contains(e, "debounce_responder_s") {
				out = append(out, e)
			}
		}
		return out
	}
	if got := errs(int64(30), "latency"); len(got) != 0 {
		t.Fatalf("valid: %v", got)
	}
	if got := errs(2.5, "latency"); len(got) != 0 {
		t.Fatalf("valid: %v", got)
	}
	// Adaptation: Python's `None.__class__` (the NoneType class object) has no JSON equivalent;
	// an opaque Go value stands in for it as the non-numeric bad case.
	for _, bad := range []any{int64(0), int64(-1), "x", true, struct{}{}} {
		if len(errs(bad, "latency")) == 0 {
			t.Fatalf("want an error for %v", bad)
		}
	}
	// latency only
	if len(errs(int64(30), "count")) == 0 {
		t.Fatal("want an error for a count rule")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// NotWithinAfterMarkTest.
func roCountRule(spec any, hasSpec bool) map[string]any {
	where := map[string]any{"kind": "heal", "src": "$bot"}
	if hasSpec {
		where["not_within_s_after_mark"] = spec
	}
	return map[string]any{"id": "C", "desc": "x", "source": "s", "expect_today": "?", "why_expect": "x",
		"check":   map[string]any{"metric": "count", "where": where},
		"pass_if": ">= 0", "min_opportunity_s": int64(0), "min_n": int64(0)}
}

func roCount(spec any, hasSpec bool, marks []rules.Row) any {
	trial := roTrial()
	rule := roCountRule(spec, hasSpec)
	trial["rules"] = []any{rule}
	ctx := &rules.Ctx{Guids: map[string]any{"player": roPlayer, "bot": roBot}, Thresholds: map[string]any{}, Trial: trial}
	rows := []rules.Row{roBotHeal(5.0), roBotHeal(12.0), roBotHeal(30.0)}
	return rules.Evaluate(rule, rows, nil, marks, ctx).Value
}

func TestNotWithinAfterMarkTest_EventsWithinSAfterEachRefillMarkAreDropped(t *testing.T) {
	marks := []rules.Row{{"kind": "refill", "t": 10.0}}
	if got := roCount(nil, false, marks); got != int64(3) {
		t.Fatalf("no spec: %v; want 3", got)
	}
	// the failing case before the change: the key was unknown and changed nothing
	if got := roCount(map[string]any{"kind": "refill", "s": int64(5)}, true, marks); got != int64(2) {
		t.Fatalf("s=5: %v; want 2 (12 s dropped)", got)
	}
	if got := roCount(map[string]any{"kind": "refill", "s": int64(1)}, true, marks); got != int64(3) {
		t.Fatalf("s=1: %v; want 3 (12 s is outside)", got)
	}
	if got := roCount(map[string]any{"kind": "other_kind", "s": int64(5)}, true, marks); got != int64(3) {
		t.Fatalf("other kind: %v; want 3", got)
	}
}

func TestNotWithinAfterMarkTest_EveryMarkOfTheKindCounts(t *testing.T) {
	marks := []rules.Row{{"kind": "refill", "t": 4.0}, {"kind": "refill", "t": 29.0}}
	if got := roCount(map[string]any{"kind": "refill", "s": int64(2)}, true, marks); got != int64(1) {
		t.Fatalf("got %v; want 1 (5 and 30 dropped)", got)
	}
}

func TestNotWithinAfterMarkTest_MarksArePutOnTheEventsTimeBaseThroughTheirWallClock(t *testing.T) {
	// marks.jsonl times start at setup_ok; events and samples count from fight_start
	marks := []rules.Row{
		{"kind": "setup_ok", "t": 0.0, "wall": "2026-11-08T10:00:00.000+00:00"},
		{"kind": "fight_start", "t": 10.0, "wall": "2026-11-08T10:00:10.000+00:00"},
		{"kind": "refill", "t": 22.0, "wall": "2026-11-08T10:00:22.000+00:00"}, // = 12 s after fight_start
	}
	if got := roCount(map[string]any{"kind": "refill", "s": int64(5)}, true, marks); got != int64(2) {
		t.Fatalf("got %v; want 2 (the heal at 12 is dropped)", got)
	}
	spans := rules.MarkSpans(marks, "refill", int64(5))
	if len(spans) != 1 || spans[0][0] != 12.0 || spans[0][1] != 17.0 {
		t.Fatalf("mark_spans = %v; want [[12 17]]", spans)
	}
}

func TestNotWithinAfterMarkTest_SamplesInTheSpanAreDroppedToo(t *testing.T) {
	trial := roTrial()
	rule := map[string]any{"id": "U", "desc": "x", "source": "s", "expect_today": "?", "why_expect": "x",
		"check": map[string]any{"metric": "uptime", "unit": "$player", "aura": []any{int64(71)}, "phase": "in_combat",
			"where": map[string]any{"not_within_s_after_mark": map[string]any{"kind": "refill", "s": int64(9)}}},
		"pass_if": ">= 0.5", "min_opportunity_s": int64(0), "min_n": int64(0)}
	var samples []rules.Row
	for i := range 20 {
		var auras []any
		if i < 15 {
			auras = []any{int64(71)}
		}
		samples = append(samples, roSample(float64(i), roPlayer, int64(500), true, auras))
	}
	ctx := &rules.Ctx{Guids: map[string]any{"player": roPlayer}, Trial: trial}
	marks := []rules.Row{{"kind": "refill", "t": 10.0}}
	assertAlmostEqual(t, 1.0, num(t, rules.Evaluate(rule, nil, samples, marks, ctx).Value), 7)
	assertAlmostEqual(t, 0.75, num(t, rules.Evaluate(rule, nil, samples, nil, ctx).Value), 7)
}

func TestNotWithinAfterMarkTest_ExcludedSpanRowsAreKept(t *testing.T) {
	trial := roTrial()
	trial["rules"] = []any{roCountRule(map[string]any{"kind": "refill", "s": int64(20)}, true)}
	ctx := &rules.Ctx{Guids: map[string]any{"player": roPlayer, "bot": roBot}, Thresholds: map[string]any{}, Trial: trial}
	rows := []rules.Row{roBotHeal(12.0), roExcluded(11.0, 14.0)}
	res := rules.Evaluate(trial["rules"].([]any)[0].(map[string]any), rows, nil,
		[]rules.Row{{"kind": "refill", "t": 10.0}}, ctx)
	if res.Value != int64(0) {
		t.Fatalf("value = %v; want 0", res.Value)
	}
	// the excluded row survived the filter, so it still excludes (a hit inside it would not score)
	rows2 := []rules.Row{roBotHeal(12.0), roExcluded(11.0, 14.0)}
	trial["rules"] = []any{roCountRule(nil, false)}
	if got := rules.Evaluate(trial["rules"].([]any)[0].(map[string]any), rows2, nil, nil, ctx).Value; got != int64(0) {
		t.Fatalf("value = %v; want 0", got)
	}
}

func TestNotWithinAfterMarkTest_Validator(t *testing.T) {
	errs := func(spec any, label string) []string {
		trial := roTrial()
		rule := roCountRule(nil, false)
		if label == "where" {
			rule["check"].(map[string]any)["where"].(map[string]any)["not_within_s_after_mark"] = spec
		} else {
			rule["check"] = map[string]any{"metric": "ratio",
				"num": map[string]any{"kind": "heal", "not_within_s_after_mark": spec},
				"den": map[string]any{"kind": "heal"}}
		}
		trial["rules"] = []any{rule}
		var out []string
		for _, e := range scenario.Validate(trial) {
			if contains(e, "not_within_s_after_mark") {
				out = append(out, e)
			}
		}
		return out
	}
	if got := errs(map[string]any{"kind": "refill", "s": int64(5)}, "where"); len(got) != 0 {
		t.Fatalf("valid: %v", got)
	}
	if got := errs(map[string]any{"kind": "refill", "s": 2.5}, "where"); len(got) != 0 {
		t.Fatalf("valid: %v", got)
	}
	for _, bad := range []any{
		map[string]any{"kind": "refill"},
		map[string]any{"s": int64(5)},
		map[string]any{"kind": "", "s": int64(5)},
		map[string]any{"kind": "refill", "s": int64(0)},
		map[string]any{"kind": "refill", "s": int64(-1)},
		map[string]any{"kind": "refill", "s": true},
		map[string]any{"kind": int64(3), "s": int64(5)},
		map[string]any{"kind": "refill", "s": int64(5), "extra": int64(1)},
		"refill",
		int64(5),
	} {
		if len(errs(bad, "where")) == 0 {
			t.Fatalf("want an error for %v", bad)
		}
	}
	// only valid in check.where
	if len(errs(map[string]any{"kind": "refill", "s": int64(5)}, "num")) == 0 {
		t.Fatal("want an error in check.num")
	}
}

func TestNotWithinAfterMarkTest_ALatencyRuleMayCarryItInCheckWhere(t *testing.T) {
	trial := roTrial()
	rule := roLatencyRule(map[string]any{"where": map[string]any{
		"not_within_s_after_mark": map[string]any{"kind": "refill", "s": int64(5)}}})
	trial["rules"] = []any{rule}
	for _, e := range scenario.Validate(trial) {
		if contains(e, "not_within") || contains(e, "needs a kind") {
			t.Fatalf("unexpected error: %s", e)
		}
	}
}

// RatePerMobAliveTest: DPS-MAG-THREAT-DAMAGE counted nukes per repeat: a mob that dies after
// 2-3 nukes made it unpassable.
func roRateRule(per any, hasPer bool, minAlive any, hasMinAlive bool, passIf string) map[string]any {
	check := map[string]any{"metric": "rate", "where": map[string]any{
		"kind": "cast", "src": "$bot", "dst": "$mob", "spell": []any{"Fireball"}}}
	if hasPer {
		check["per"] = per
	}
	if hasMinAlive {
		check["min_alive_s"] = minAlive
	}
	return map[string]any{"id": "R", "desc": "x", "source": "s", "expect_today": "?", "why_expect": "x",
		"check": check, "pass_if": passIf, "min_opportunity_s": int64(0), "min_n": int64(0)}
}

func roNuke(t float64) map[string]any {
	return map[string]any{"t": t, "kind": "cast", "src": "b", "src_guid": roBot, "dst": "m", "dst_guid": roMob,
		"src_unit": "$bot", "dst_unit": "$mob", "spell_id": int64(133), "spell": "Fireball", "amount": nil,
		"dst_hp_pct": 90.0, "src_role": "bot", "dst_role": "mob", "origin": "ta"}
}

func roRateSamples(aliveS int, totalS int) []rules.Row {
	var out []rules.Row
	for i := range totalS {
		out = append(out, roSample(float64(i), roPlayer, int64(800), true, nil))
		hp := int64(0)
		if i < aliveS {
			hp = 500
		}
		out = append(out, map[string]any{"t": float64(i), "unit": "mob:" + lower(roMob), "guid": lower(roMob),
			"hp": hp, "max_hp": int64(2000), "in_combat": i < aliveS, "auras": []any{}})
	}
	return out
}

func lower(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + 32
		}
	}
	return string(out)
}

func roEvalRate(t *testing.T, rule map[string]any, rows, samples []rules.Row) rules.Result {
	t.Helper()
	trial := roTrial()
	trial["rules"] = []any{rule}
	ctx := &rules.Ctx{Guids: map[string]any{"player": roPlayer, "bot": roBot, "mobs": []any{roMob}},
		Thresholds: map[string]any{}, Trial: trial}
	return rules.Evaluate(rule, rows, samples, nil, ctx)
}

func TestRatePerMobAliveTest_AShortLivedMobDoesNotSinkAnActiveMage(t *testing.T) {
	// 6 nukes while the mob lives for 20 s of a 180 s repeat: 18 a minute alive, 2 a minute scored
	var rows []rules.Row
	for i := range 6 {
		rows = append(rows, roNuke(2.0+3.3*float64(i)))
	}
	res := roEvalRate(t, roRateRule("mob_alive", true, int64(15), true, ">= 8"), rows, roRateSamples(20, 180))
	if res.Verdict != "PASS" {
		t.Fatalf("verdict = %q; want PASS", res.Verdict)
	}
	assertAlmostEqual(t, 18.0, num(t, res.Value), 0)
	// the failing case before the change: the per-repeat rate over the scored span reads 2 a minute
	res = roEvalRate(t, roRateRule(nil, false, nil, false, ">= 8"), rows, roRateSamples(20, 180))
	if num(t, res.Value) >= 8 {
		t.Fatalf("value = %v; want < 8", res.Value)
	}
}

func TestRatePerMobAliveTest_ABlockedMageMissesItWithALongLivedMob(t *testing.T) {
	// 3 nukes in 180 s alive: 1 a minute
	rows := []rules.Row{roNuke(10.0), roNuke(60.0), roNuke(120.0)}
	res := roEvalRate(t, roRateRule("mob_alive", true, int64(15), true, ">= 8"), rows, roRateSamples(180, 180))
	if res.Verdict != "FAIL" {
		t.Fatalf("verdict = %q; want FAIL", res.Verdict)
	}
	assertAlmostEqual(t, 1.0, num(t, res.Value), 1)
}

func TestRatePerMobAliveTest_AMobAliveTooBrieflyIsInconclusive(t *testing.T) {
	res := roEvalRate(t, roRateRule("mob_alive", true, int64(15), true, ">= 8"),
		[]rules.Row{roNuke(1.0)}, roRateSamples(8, 180))
	if res.Verdict != "INCONCLUSIVE" {
		t.Fatalf("verdict = %q; want INCONCLUSIVE", res.Verdict)
	}
}

func TestRatePerMobAliveTest_TheUnionOfMobsCountsEachSecondOnce(t *testing.T) {
	samples := roRateSamples(20, 180)
	for i := 10; i < 30; i++ {
		samples = append(samples, map[string]any{"t": float64(i), "unit": "mob:0xf130000000000bbb", "guid": "x",
			"hp": int64(400), "max_hp": int64(900), "in_combat": true, "auras": []any{}})
	}
	if got := rules.MobAliveSeconds(samples, nil); got != 30.0 {
		t.Fatalf("mob_alive_seconds = %v; want 30", got)
	}
}

func TestRatePerMobAliveTest_Validator(t *testing.T) {
	errs := func(over map[string]any) []string {
		trial := roTrial()
		rule := roRateRule("mob_alive", true, int64(15), true, ">= 8")
		for k, v := range over {
			rule["check"].(map[string]any)[k] = v
		}
		trial["rules"] = []any{rule}
		var out []string
		for _, e := range scenario.Validate(trial) {
			if contains(e, "per") || contains(e, "min_alive_s") {
				out = append(out, e)
			}
		}
		return out
	}
	if got := errs(nil); len(got) != 0 {
		t.Fatalf("valid: %v", got)
	}
	if len(errs(map[string]any{"per": "alive"})) == 0 {
		t.Fatal("want an error for per=alive")
	}
	if len(errs(map[string]any{"min_alive_s": int64(0)})) == 0 {
		t.Fatal("want an error for min_alive_s=0")
	}
	if len(errs(map[string]any{"min_alive_s": "x"})) == 0 {
		t.Fatal("want an error for min_alive_s=x")
	}
	trial := roTrial()
	rule := roRateRule("mob_alive", true, int64(15), true, ">= 8")
	rule["check"].(map[string]any)["metric"] = "count"
	trial["rules"] = []any{rule}
	found := false
	for _, e := range scenario.Validate(trial) {
		if contains(e, "bad per") {
			found = true
		}
	}
	if !found { // rate rules only
		t.Fatal("want a bad per error on a count rule")
	}
}
