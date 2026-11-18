package fixes

// Translated from tests/test_bench_fix_p1.py: P1, the mage threat cap is measured against a
// hired bot tank (a dps-kit player plus a hired Protection warrior tank).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/gear"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
	"github.com/pjerra/forever-agent/internal/trials/spells"
)

const (
	fixP1Player = "0x0000000000000001"
	fixP1Bot    = "0x0000000000000002"
	fixP1Extra1 = "0x0000000000000003"
	fixP1Mob    = "0xf13000000000abcd"
	fixP1Marked = "0xf13000000000beef"
)

func fixP1Role(guid string) string {
	switch guid {
	case fixP1Player:
		return "player"
	case fixP1Bot:
		return "bot"
	case fixP1Extra1:
		return "extra1"
	case fixP1Mob:
		return "mob"
	case fixP1Marked:
		return "mob"
	}
	return ""
}

// fixP1Load is the Python `load()`: a fresh copy of trials/dps/dps-mage-threat-cap.json.
func fixP1Load(t *testing.T) scenario.Trial {
	t.Helper()
	trial, err := scenario.LoadTrial("dps/dps-mage-threat-cap.json")
	if err != nil {
		t.Fatalf("load dps-mage-threat-cap: %v", err)
	}
	if trial == nil {
		t.Fatal("trial dps-mage-threat-cap not found")
	}
	return trial
}

func fixP1Contains(items []any, want string) bool {
	for _, item := range items {
		if s, ok := item.(string); ok && s == want {
			return true
		}
	}
	return false
}

func fixP1AnyRows(rows []rules.Row) []any {
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out
}

func fixP1Sample(t float64, guid string, auras []any, target string) rules.Row {
	if auras == nil {
		auras = []any{}
	}
	return rules.Row{
		"t": float64(t), "wall": nil, "unit": fixP1Role(guid), "guid": guid, "hp": int64(600),
		"max_hp": int64(1000), "power": int64(500), "max_power": int64(500), "auras": auras,
		"casting": nil, "in_combat": true, "target": target,
		"x": 0.0, "y": 0.0, "z": 0.0,
	}
}

func fixP1Event(t float64, kind, src, dst string, spellID any) rules.Row {
	var spellName any
	if id, ok := pyjson.Int64(spellID); ok && id != 0 {
		spellName = spells.NameFor(id)
	}
	srcRole := fixP1Role(src)
	if src == fixP1Mob || src == fixP1Marked {
		srcRole = "mob"
	}
	dstRole := fixP1Role(dst)
	if dst == fixP1Mob || dst == fixP1Marked {
		dstRole = "mob"
	}
	return rules.Row{
		"t": float64(t), "wall": nil, "kind": kind, "src": fixP1Role(src), "src_guid": src,
		"dst": fixP1Role(dst), "dst_guid": dst, "src_unit": fixP1Role(src),
		"dst_unit": fixP1Role(dst), "spell_id": spellID, "spell": spellName,
		"amount": int64(100), "dst_hp_pct": int64(60), "src_role": srcRole,
		"dst_role": dstRole, "origin": "ta",
	}
}

// fixP1Series is the Python `series`: 1 Hz samples for player, bot, extra1, the mob on the
// player, and the marked mob.
func fixP1Series(secs int, playerAuras []any) []rules.Row {
	var out []rules.Row
	for t := range secs + 1 {
		out = append(out,
			fixP1Sample(float64(t), fixP1Player, playerAuras, fixP1Mob),
			fixP1Sample(float64(t), fixP1Bot, nil, fixP1Mob),
			fixP1Sample(float64(t), fixP1Extra1, nil, ""),
			fixP1Sample(float64(t), fixP1Mob, nil, fixP1Player),
			fixP1Sample(float64(t), fixP1Marked, nil, fixP1Player),
		)
	}
	return out
}

// fixP1Rules is the Python RepMixin.rules: score one synthetic repeat and return
// {rule id: entry}.
func fixP1Rules(t *testing.T, tid string, evts, samples []rules.Row, secs int) map[string]map[string]any {
	t.Helper()
	rep := filepath.Join(t.TempDir(), "run", tid, "rep1")
	if err := os.MkdirAll(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{
		"ok": true,
		"guids": map[string]any{"player": fixP1Player, "bot": fixP1Bot,
			"extras": []any{fixP1Extra1}, "mobs": []any{fixP1Mob, fixP1Marked},
			"marked": fixP1Marked},
		"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)},
	})
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), fixP1AnyRows(samples))
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), fixP1AnyRows(evts))
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), []any{
		map[string]any{"kind": "fight_start", "t": int64(0)},
		map[string]any{"kind": "fight_end", "t": int64(secs), "ended": "duration"},
	})
	out := score.ScoreRep(rep, fixP1Load(t), nil)
	got := map[string]map[string]any{}
	rows, _ := pyjson.List(out["rules"])
	for _, rowVal := range rows {
		row, ok := pyjson.Object(rowVal)
		if !ok {
			continue
		}
		got[pyjson.Str(row["id"])] = row
	}
	return got
}

func TestExtraBotGearFieldTest_ExtraBotGearFieldValidates(t *testing.T) {
	trial := fixP1Load(t)
	extras, _ := pyjson.List(trial["extra_bots"])
	first := cloneRow(obj(t, extras[0]))
	first["gear"] = "blue"
	trial["extra_bots"] = []any{first}
	if errs := scenario.Validate(trial); len(errs) != 0 {
		t.Fatalf("validate: want no errors, got %v", errs)
	}
}

func TestExtraBotGearFieldTest_ExtraBotGearRejectsNonWord(t *testing.T) {
	trial := fixP1Load(t)
	extras, _ := pyjson.List(trial["extra_bots"])
	first := cloneRow(obj(t, extras[0]))
	first["gear"] = int64(3)
	trial["extra_bots"] = []any{first}
	errs := scenario.Validate(trial)
	if len(errs) == 0 {
		t.Fatal("validate: want a gear error, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "gear") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no error mentions gear: %v", errs)
	}
}

// TestRunExtrasAutogearTest_* are skipped: they drive setup.run_extras/GmGuard, the live runner
// (FakeAgent, ledger, mocked setup helpers, hire slots).

func TestThreatCapBotTankTest_ThreatCapUsesABotTank(t *testing.T) {
	trial := fixP1Load(t)
	merged := map[string]scenario.Trial{"base": trial}
	for _, variant := range scenario.ExpandVariants(trial) {
		merged[variant.Name] = variant.Trial
	}
	for name, m := range merged {
		extras, ok := pyjson.List(m["extra_bots"])
		if !ok || len(extras) == 0 {
			t.Fatalf("%s: extra_bots[0] missing", name)
		}
		extra := obj(t, extras[0])
		assertEqual(t, "warrior", extra["class"])
		assertEqual(t, "protection", extra["spec"])
		assertEqual(t, "tank", extra["role"])
		assertEqual(t, "blue", extra["gear"])
		player := obj(t, m["player"])
		assertEqual(t, "dps", player["forced_role"])
		assertEqual(t, int64(2457), player["stance"])
		checks, _ := pyjson.List(m["setup_checks"])
		if !fixP1Contains(checks, "player_no_shield") {
			t.Fatalf("%s: setup_checks lacks player_no_shield: %v", name, checks)
		}
		var ids []string
		ruleList, _ := pyjson.List(m["rules"])
		for _, ruleVal := range ruleList {
			rule, ok := pyjson.Object(ruleVal)
			if !ok {
				continue
			}
			ids = append(ids, pyjson.Str(rule["id"]))
		}
		for _, want := range []string{"DPS-MAG-THREAT-TANK-HELD", "DPS-MAG-THREAT-TANK-FOUGHT"} {
			found := false
			for _, id := range ids {
				if id == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: rules lack %s: %v", name, want, ids)
			}
		}
		withID := cloneRow(m)
		withID["id"] = trial["id"]
		if errs := scenario.Validate(withID); len(errs) != 0 {
			t.Fatalf("%s: validate: want no errors, got %v", name, errs)
		}
		for _, ruleVal := range ruleList {
			rule, ok := pyjson.Object(ruleVal)
			if !ok {
				continue
			}
			for _, key := range []string{"desc", "why_expect"} {
				text := pyjson.Str(rule[key])
				if strings.Contains(text, "player tank") {
					t.Fatalf("%s/%s: %s still says player tank", name, pyjson.Str(rule["id"]), key)
				}
			}
		}
	}
	lvl60 := merged["lvl60"]
	player := obj(t, lvl60["player"])
	if player["gear"] != nil {
		t.Fatalf("lvl60 player.gear: want None, got %v", player["gear"])
	}
	assertEqual(t, int64(-2), obj(t, obj(t, lvl60["situation"])["mobs"])["level_delta"])
	assertEqual(t, int64(4), trial["repeats"])
}

func TestThreatCapBotTankTest_TankHeldRatio(t *testing.T) {
	trial := fixP1Load(t)
	// The Python loop expands the variants but adds nothing to the assertions.
	scenario.ExpandVariants(trial)
	var held map[string]any
	ruleList, _ := pyjson.List(trial["rules"])
	for _, ruleVal := range ruleList {
		rule, ok := pyjson.Object(ruleVal)
		if !ok {
			continue
		}
		if pyjson.Str(rule["id"]) == "DPS-MAG-THREAT-TANK-HELD" {
			held = rule
		}
	}
	if held == nil {
		t.Fatal("trial needs the TANK-HELD rule first")
	}
	check := obj(t, held["check"])
	assertEqual(t, map[string]any{"kind": "damage", "src": "$mob", "dst": "$extra1"}, check["num"])
	assertEqual(t, map[string]any{"kind": "damage", "src": "$mob"}, check["den"])
	assertEqual(t, ">= 0.6", held["pass_if"])
	assertEqual(t, int64(20), check["min_n"])
}

func TestThreatCapBotTankTest_NoLevel60Kit(t *testing.T) {
	items, _, why := gear.PickKit(map[string]any{
		"class": "warrior", "level": int64(60), "forced_role": "dps"})
	if len(items) != 0 {
		t.Fatalf("pick_kit: want no kit for warrior dps 60, got %d items", len(items))
	}
	if !strings.Contains(why, "within 9 levels") {
		t.Fatalf("pick_kit reason: want 'within 9 levels', got %q", why)
	}
}

// TestThreatCapBotTankTest_KitsAreVanillaEntries is skipped: it asserts
// build_data.GEAR_MAX_ENTRY == 20000, and the Python build table has no Go counterpart.

func TestThreatCapHeldScoringTest_TankHeldPassAndNoAggroFail(t *testing.T) {
	samples := fixP1Series(200, []any{int64(2457)})
	var evts []rules.Row
	// 30 mob hits on extra1, 5 on the player, 2 on the mage
	for i := range 30 {
		evts = append(evts, fixP1Event(10+float64(i), "damage", fixP1Mob, fixP1Extra1, nil))
	}
	for i := range 5 {
		evts = append(evts, fixP1Event(50+float64(i), "damage", fixP1Mob, fixP1Player, nil))
	}
	for i := range 2 {
		evts = append(evts, fixP1Event(70+float64(i), "damage", fixP1Mob, fixP1Bot, nil))
	}
	got := fixP1Rules(t, "dps-mage-threat-cap", evts, samples, 200)
	held, ok := got["DPS-MAG-THREAT-TANK-HELD"]
	if !ok {
		t.Fatalf("rules lack DPS-MAG-THREAT-TANK-HELD")
	}
	assertEqual(t, "PASS", held["verdict"])
	noAggro, ok := got["DPS-MAG-THREAT-NO-AGGRO"]
	if !ok {
		t.Fatalf("rules lack DPS-MAG-THREAT-NO-AGGRO")
	}
	assertEqual(t, "FAIL", noAggro["verdict"])
}

func TestThreatCapHeldScoringTest_TankHeldInconclusiveBelowMinN(t *testing.T) {
	samples := fixP1Series(200, []any{int64(2457)})
	var evts []rules.Row
	for i := range 12 {
		evts = append(evts, fixP1Event(10+float64(i), "damage", fixP1Mob, fixP1Extra1, nil))
	}
	got := fixP1Rules(t, "dps-mage-threat-cap", evts, samples, 200)
	held, ok := got["DPS-MAG-THREAT-TANK-HELD"]
	if !ok {
		t.Fatalf("rules lack DPS-MAG-THREAT-TANK-HELD")
	}
	assertEqual(t, "INCONCLUSIVE", held["verdict"])
}
