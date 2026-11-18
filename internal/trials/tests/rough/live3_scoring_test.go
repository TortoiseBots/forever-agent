package rough

// Translated from tests/test_live3.py: the methods that exercise the scoring modules only
// (scenario.load_trial/validate, score.score_rep, events.normalise). The live-runner classes
// (GmVisibleSetupTest, PlayerVisibleCheckTest's check_player_visible cases, FloorTest's governor
// cases, OffhandTest, LoginPacingTest, StanceSelectionTest) are not ported here.

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

const (
	// the synthetic-repeat ids of test_rules_healers (trh.PLAYER/BOT/MOB) ...
	l3Player = "0x0000000000000001"
	l3Bot    = "0x0000000000000002"
	l3Mob    = "0xf13000000000abcd"
	// ... and tl1.EXTRA, the extra_bots hire
	l3Extra = "0x000000000000064f"

	l3PWS = int64(6065)

	l3ControlTid = "priest-holy-pws-bot-warrior-control"
)

func l3Trial(t *testing.T, tid string) map[string]any {
	t.Helper()
	trial, err := scenario.LoadTrial("healers/" + tid + ".json")
	if err != nil {
		t.Fatalf("load %s: %v", tid, err)
	}
	return trial
}

func l3Rule(t *testing.T, trial map[string]any, rid string) map[string]any {
	t.Helper()
	for _, rv := range list(t, trial["rules"]) {
		rule := obj(t, rv)
		if pyjson.Str(rule["id"]) == rid {
			return rule
		}
	}
	t.Fatalf("trial has no rule %s", rid)
	return nil
}

// l3Sample is trh.sample: one synthetic samples.jsonl row.
func l3Sample(tv int, guid string, hp, maxHP int64, auras []any, combat bool) rules.Row {
	return rules.Row{
		"t": float64(tv), "wall": nil, "unit": "x", "guid": guid, "hp": hp, "max_hp": maxHP,
		"power": int64(500), "max_power": int64(500), "auras": auras, "casting": nil,
		"in_combat": combat, "target": "", "x": 0.0, "y": 0.0, "z": 0.0,
	}
}

// l3ExtraEvent is the bot's Power Word: Shield cast at the extra hire.
func l3ExtraEvent(tv float64, hp int64) rules.Row {
	return rules.Row{
		"t": tv, "wall": nil, "kind": "cast", "src": "bot", "src_guid": l3Bot, "dst": "extra",
		"dst_guid": l3Extra, "src_unit": "$bot", "dst_unit": "$extra1", "spell_id": l3PWS,
		"spell": "Power Word: Shield", "amount": nil, "dst_hp_pct": hp, "src_role": "bot",
		"dst_role": "extra", "origin": "ta",
	}
}

func l3ExtraSeries(secs int, hpPct int64, aura []any) []rules.Row {
	var out []rules.Row
	for tv := range secs + 1 {
		out = append(out, l3Sample(tv, l3Player, 800, 1000, []any{}, true))
		out = append(out, l3Sample(tv, l3Bot, 1000, 1000, []any{}, true))
		out = append(out, l3Sample(tv, l3Extra, hpPct*10, 1000, aura, true))
	}
	return out
}

// l3ExtraSetup is EXTRA_SETUP: the guids of the extra-bots control (it replaces the default set).
func l3ExtraSetup() map[string]any {
	return map[string]any{"guids": map[string]any{
		"player": l3Player, "bot": l3Bot, "extras": []any{l3Extra}, "mobs": []any{l3Mob},
	}}
}

func l3Rows(rows []rules.Row) []any {
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out
}

// l3Verdict is trh.RepMixin.verdict: score one synthetic repeat through score.score_rep and
// return the entry of one rule.
func l3Verdict(t *testing.T, tid, rid string, evs, samples []rules.Row, secs int, setup map[string]any) map[string]any {
	t.Helper()
	rep := filepath.Join(t.TempDir(), "run", tid, "rep1")
	if err := os.MkdirAll(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"ok":     true,
		"guids":  map[string]any{"player": l3Player, "bot": l3Bot, "mobs": []any{l3Mob}},
		"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)},
	}
	for k, v := range setup {
		doc[k] = v
	}
	writeJSON(t, filepath.Join(rep, "setup.json"), doc)
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), l3Rows(samples))
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), l3Rows(evs))
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), []any{
		map[string]any{"kind": "fight_start", "t": int64(0)},
		map[string]any{"kind": "fight_end", "t": int64(secs), "ended": "duration"},
	})
	out := score.ScoreRep(rep, l3Trial(t, tid), nil)
	for _, rv := range list(t, out["rules"]) {
		entry := obj(t, rv)
		if pyjson.Str(entry["id"]) == rid {
			return entry
		}
	}
	t.Fatalf("score output has no rule %s", rid)
	return nil
}

func l3HasString(items []any, want string) bool {
	for _, item := range items {
		if pyjson.Str(item) == want {
			return true
		}
	}
	return false
}

func l3AnyContaining(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func TestBotWarriorControlTest_TheTrialIsTheBattleControlPlusAForcedDpsArmsWarrior(t *testing.T) {
	trial := l3Trial(t, l3ControlTid)
	if errs := scenario.Validate(trial); len(errs) != 0 {
		t.Fatalf("the trial does not validate: %v", errs)
	}
	assertEqual(t, []any{map[string]any{
		"class": "warrior", "spec": "arms", "role": "dps", "level": int64(40), "items": []any{},
	}}, trial["extra_bots"])
	player := obj(t, trial["player"])
	assertEqual(t, int64(2457), player["stance"])
	assertEqual(t, "arms", obj(t, player["talents"])["tree"])
	sit := obj(t, trial["situation"])
	assertEqual(t, "extra1", sit["victim"])
	assertEqual(t, []any{int64(21), int64(50)}, sit["hp_band"])
	assertEqual(t, map[string]any{
		"unit": "$extra1", "hp_lt": "$low", "hp_gt": "$critical", "in_combat": true,
	}, trial["opportunity"])
	rule := l3Rule(t, trial, "PRI-PWS-BOT-WARRIOR-CONTROL")
	assertEqual(t, "PASS", rule["expect_today"])
	assertEqual(t, ">= 1", rule["pass_if"])
	assertEqual(t, "$extra1", obj(t, obj(t, rule["check"])["where"])["dst"])
	checks := list(t, trial["setup_checks"])
	for _, cid := range []string{"player_visible", "player_no_shield", "extra_bots_no_shield"} {
		if !l3HasString(checks, cid) {
			t.Fatalf("setup_checks does not list %s: %v", cid, checks)
		}
	}
}

func TestBotWarriorControlTest_OneShieldOnTheBotInTheBandPassesAndNoneFails(t *testing.T) {
	samples := l3ExtraSeries(200, 35, []any{int64(2457)})
	rid := "PRI-PWS-BOT-WARRIOR-CONTROL"
	good := l3Verdict(t, l3ControlTid, rid, []rules.Row{l3ExtraEvent(30, 35)}, samples, 200, l3ExtraSetup())
	bad := l3Verdict(t, l3ControlTid, rid, nil, samples, 200, l3ExtraSetup())
	above := l3Verdict(t, l3ControlTid, rid, []rules.Row{l3ExtraEvent(30, 80)}, samples, 200, l3ExtraSetup())
	assertEqual(t, "PASS", good["verdict"])
	assertEqual(t, int64(1), good["value"])
	assertEqual(t, "FAIL", bad["verdict"])
	// a shield above the low band is not the control's case
	assertEqual(t, "FAIL", above["verdict"])
}

func TestBotWarriorControlTest_TheBattleStancePreconditionReadsTheBotSOwnAura(t *testing.T) {
	rid := "PRI-PWS-BOT-WARRIOR-CONTROL-STANCE"
	held := l3Verdict(t, l3ControlTid, rid, nil, l3ExtraSeries(200, 35, []any{int64(2457)}), 200, l3ExtraSetup())
	lost := l3Verdict(t, l3ControlTid, rid, nil, l3ExtraSeries(200, 35, []any{int64(71)}), 200, l3ExtraSetup())
	assertEqual(t, "PASS", held["verdict"])
	assertEqual(t, "FAIL", lost["verdict"])
}

func TestBotWarriorControlTest_VictimExtra1NeedsAnExtraBotAndIsTheOnlyNewVictim(t *testing.T) {
	trial := l3Trial(t, l3ControlTid)
	obj(t, trial["situation"])["victim"] = "extra2"
	if errs := scenario.Validate(trial); !l3AnyContaining(errs, "victim") {
		t.Fatalf("victim extra2 was accepted: %v", errs)
	}
	trial2 := l3Trial(t, "priest-holy-pws-def-arms")
	obj(t, trial2["situation"])["victim"] = "extra1"
	if errs := scenario.Validate(trial2); !l3AnyContaining(errs, "victim") {
		t.Fatalf("victim extra1 without extra_bots was accepted: %v", errs)
	}
}

func TestPlayerVisibleCheckTest_EveryNonRaidTrialListsTheCheckAndTheValidatorRequiresIt(t *testing.T) {
	for _, path := range scenario.TrialFiles() {
		if !strings.HasPrefix(path, "healers/") {
			continue
		}
		trial, err := scenario.LoadTrial(path)
		if err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
		if !l3HasString(list(t, trial["setup_checks"]), "player_visible") {
			t.Fatalf("%s: setup_checks does not list player_visible", path)
		}
		if errs := scenario.Validate(trial); len(errs) != 0 {
			t.Fatalf("%s: %v", path, errs)
		}
	}
	trial := l3Trial(t, "priest-holy-pws-def-arms")
	var kept []any
	for _, cid := range list(t, trial["setup_checks"]) {
		if pyjson.Str(cid) != "player_visible" {
			kept = append(kept, cid)
		}
	}
	trial["setup_checks"] = kept
	if errs := scenario.Validate(trial); !l3AnyContaining(errs, "player_visible") {
		t.Fatalf("a missing player_visible check was accepted: %v", errs)
	}
	kept = append(kept, "player_visible", "player_invisible")
	trial["setup_checks"] = kept
	if errs := scenario.Validate(trial); !l3AnyContaining(errs, "unknown setup check") {
		t.Fatalf("an unknown setup check was accepted: %v", errs)
	}
}

// The floor of every healer trial: 12 for the PWS shield trials (and the other live3 trials),
// 15 for the priest heal-latency trial, 30 otherwise.
//
// Adaptation: the Python test's expression predates priest-shadow-inform-heal-solo-healer
// (added in 4e95f4f after the expectation was written); the shipped file has floor_pct 12, so
// the reference test is red on it. It is listed among the 12-floor trials here.
func TestFloorTest_TheFourShieldTrialsSetFloor12AndTheRestKeepTheDefault(t *testing.T) {
	shield := map[string]bool{
		"priest-holy-pws-bear-bal": true, "priest-holy-pws-bear-bal-2tank": true,
		"priest-holy-pws-def-arms": true, "priest-holy-pws-def-arms-2tank": true,
	}
	also12 := map[string]bool{
		"priest-holy-pws-bot-warrior-control": true, "priest-holy-pws-def-arms-ooc": true,
		"priest-holy-pws-battle-ooc-control": true, "priest-shadow-form-prepull": true,
		"paladin-holy-heal-latency": true, "priest-shadow-inform-heal-solo-healer": true,
	}
	for _, path := range scenario.TrialFiles() {
		if !strings.HasPrefix(path, "healers/") {
			continue
		}
		trial, err := scenario.LoadTrial(path)
		if err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
		tid := pyjson.Str(trial["id"])
		want := int64(30)
		switch {
		case shield[tid] || also12[tid]:
			want = 12
		case tid == "priest-holy-heal-latency":
			want = 15
		}
		assertEqual(t, want, obj(t, trial["situation"])["floor_pct"])
		if errs := scenario.Validate(trial); len(errs) != 0 {
			t.Fatalf("%s: %v", tid, errs)
		}
	}
}

func TestFloorTest_ValidatorRejectsAFloorAtOrAboveTheBandLow(t *testing.T) {
	trial := l3Trial(t, "priest-holy-pws-def-arms")
	for _, bad := range []int64{40, 55, 0, -3} {
		obj(t, trial["situation"])["floor_pct"] = bad
		if errs := scenario.Validate(trial); !l3AnyContaining(errs, "floor_pct") {
			t.Fatalf("floor_pct %d was accepted: %v", bad, errs)
		}
	}
}

// l3ISO is the fixture's ISO wall with milliseconds.
func l3ISO(base time.Time, secs float64) string {
	millis := int64(math.Round(secs * 1000))
	return base.Add(time.Duration(millis) * time.Millisecond).Format("2006-01-02T15:04:05.000-07:00")
}

func TestClockStepTest_NormalisePutsEventsOnTheMonotonicAxis(t *testing.T) {
	base := time.Date(2026, 11, 8, 5, 0, 0, 0, time.UTC)
	tmp := t.TempDir()
	var samples []any
	for k := range 20 {
		samples = append(samples, map[string]any{
			"t": int64(0), "wall": l3ISO(base, float64(k)), "unit": "player", "guid": l3Player,
			"hp": int64(1000 - 10*k), "max_hp": int64(1000), "auras": []any{}, "in_combat": true,
			"mono_ms": int64(5000 + 1000*k),
		})
	}
	// the host clock stepped back 2.93 s before event 12: the agent's `time` steps, mono_ms does not
	var raw []any
	for k := range 18 {
		back := 0.0
		if k >= 12 {
			back = 2.93
		}
		raw = append(raw, map[string]any{
			"kind": "melee", "seq": int64(k), "mono_ms": int64(5000 + 1000*k + 500),
			"time":   l3ISO(base, float64(k)+0.5-back),
			"source": "mob", "source_guid": "0xf130000000000001", "target": "p", "target_guid": l3Player,
			"amount": int64(5),
		})
	}
	marks := []any{map[string]any{"kind": "fight_start", "wall": l3ISO(base, 0.0), "t": 0.0}}
	writeJSONL(t, filepath.Join(tmp, "samples.jsonl"), samples)
	writeJSONL(t, filepath.Join(tmp, "raw-ta.jsonl"), raw)
	writeJSONL(t, filepath.Join(tmp, "marks.jsonl"), marks)
	writeJSON(t, filepath.Join(tmp, "setup.json"), map[string]any{
		"guids": map[string]any{"player": l3Player, "bot": l3Bot, "mobs": []any{"0xf130000000000001"}},
	})
	rows, err := events.Normalise(tmp, map[string]any{})
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	var out []rules.Row
	for _, e := range rows {
		if pyjson.Str(e["kind"]) == "damage" {
			out = append(out, e)
		}
	}
	if len(out) != 18 {
		t.Fatalf("want 18 damage rows, got %d", len(out))
	}
	for k := range 18 {
		assertAlmostEqual(t, float64(k)+0.5, num(t, out[k]["t"]), 7)
	}
	// the sample of its own second
	assertAlmostEqual(t, 100.0*(1000-10*15)/1000.0, num(t, out[15]["dst_hp_pct"]), 7)
}
