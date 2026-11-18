package pooled

// Translated from tests/test_counterspell_pooled.py: pooled scoring for
// DPS-MAG-COUNTERSPELL (t1113 scoring flip, not a regression).
//
// The t1113 card (bench 8bc775f, `dps-mage-counterspell-caster`) read FAIL 6/10 only
// because two repeats fell under `min_n` 5 by one trigger and the tie among the two
// valid repeats scored FAIL. The t1112-PASS was the same rate (12/21 = 0.571). Pooled,
// both images FAIL at the same rate (10/18 = 0.556), so the rule never held. This
// test pins the pooled verdict at the t1113 numbers (2/5, 2/4, 4/5, 2/4 -> FAIL at
// 10/18) and checks the flip on a real synthetic repeat: pooled, a 4-trigger repeat
// is scored 4/4 and PASS; without `pool` the same repeat is INCONCLUSIVE (`n 4 <
// min_n 5`), which is what the pre-pool JSON still does.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
	"github.com/pjerra/forever-agent/internal/trials/spells"
)

const (
	cspPlayer        = "0x0000000000000001"
	cspBot           = "0x0000000000000002"
	cspExtra1        = "0x0000000000000003"
	cspMob           = "0xf13000000000abcd"
	cspMarked        = "0xf13000000000beef"
	cspLightningBolt = int64(9532) // a cast-time, silence-interruptible spell
	cspTrialID       = "dps-mage-counterspell-caster"
	cspRuleID        = "DPS-MAG-COUNTERSPELL"
)

// cspT1113 is the t1113 per-rep k/n from COUNTERSPELL-FAIL.md (card 20261113-141949-t1113).
var cspT1113 = [][2]int64{{2, 5}, {2, 4}, {4, 5}, {2, 4}}

// cspRole is test_rules_tanks_dps.ROLE.
var cspRole = map[string]string{
	cspPlayer: "player", cspBot: "bot", cspExtra1: "extra1", cspMob: "mob", cspMarked: "mob",
}

func cspSample(t float64, guid string, auras []any, target string, combat bool, hp int64) map[string]any {
	if auras == nil {
		auras = []any{}
	}
	return map[string]any{
		"t": t, "wall": nil, "unit": cspRole[guid], "guid": guid, "hp": hp,
		"max_hp": int64(1000), "power": int64(500), "max_power": int64(500), "auras": auras,
		"casting": nil, "in_combat": combat, "target": target,
		"x": 0.0, "y": 0.0, "z": 0.0,
	}
}

func cspEvent(t float64, kind, src, dst string, spellID int64) map[string]any {
	var spell any
	if spellID != 0 {
		spell = spells.NameFor(spellID)
	}
	srcRole := "mob"
	if src != cspMob && src != cspMarked {
		srcRole = cspRole[src]
	}
	dstRole := "mob"
	if dst != cspMob && dst != cspMarked {
		dstRole = cspRole[dst]
	}
	return map[string]any{
		"t": t, "wall": nil, "kind": kind, "src": cspRole[src], "src_guid": src,
		"dst": cspRole[dst], "dst_guid": dst, "src_unit": cspRole[src], "dst_unit": cspRole[dst],
		"spell_id": spellID, "spell": spell,
		"amount": int64(100), "dst_hp_pct": int64(60), "src_role": srcRole,
		"dst_role": dstRole, "origin": "ta",
	}
}

// cspSeries is 1 Hz samples: player, bot, extra1, the mob on the player, and the marked mob.
func cspSeries(secs int, player []any) []any {
	var out []any
	for t := range secs + 1 {
		at := float64(t)
		target := cspMob
		out = append(out,
			cspSample(at, cspPlayer, player, target, true, 600),
			cspSample(at, cspBot, nil, target, true, 600),
			cspSample(at, cspExtra1, nil, "", true, 600),
			cspSample(at, cspMob, nil, cspPlayer, true, 600),
			cspSample(at, cspMarked, nil, cspPlayer, true, 600),
		)
	}
	return out
}

// cspLoadRule loads the real trial and its counterspell rule.
func cspLoadRule(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	trial, err := scenario.LoadTrial("dps/" + cspTrialID + ".json")
	if err != nil {
		t.Fatalf("load %s: %v", cspTrialID, err)
	}
	ruleRows, _ := pyjson.List(trial["rules"])
	for _, rv := range ruleRows {
		rule, _ := pyjson.Object(rv)
		if pyjson.Str(rule["id"]) == cspRuleID {
			return trial, rule
		}
	}
	t.Fatalf("rule %s not found", cspRuleID)
	return nil, nil
}

// cspWriteReps writes pre-scored repeats with the given k/n pairs.
func cspWriteReps(t *testing.T, root string, pairs [][2]int64) string {
	t.Helper()
	for i, kn := range pairs {
		k, n := kn[0], kn[1]
		rep := filepath.Join(root, "rep"+cspItoa(int64(i+1)))
		if err := os.MkdirAll(rep, 0o755); err != nil {
			t.Fatal(err)
		}
		verdict := "FAIL"
		if float64(k)/float64(n) >= 0.6 {
			verdict = "PASS"
		}
		writeJSON(t, filepath.Join(rep, "verdicts.json"), map[string]any{
			"trial": cspTrialID, "rep": "rep" + cspItoa(int64(i+1)), "ok_setup": true,
			"scored_s": 180.0,
			"rules": []any{map[string]any{
				"id": cspRuleID, "verdict": verdict, "value": float64(k) / float64(n),
				"k": k, "n": n, "evidence": []any{}, "guard": "", "reason": "",
				"invalid_reason": "",
			}},
		})
	}
	return root
}

// cspWriteCastingRep writes one synthetic repeat: `triggers` mob casts, `answers` Counterspells.
func cspWriteCastingRep(t *testing.T, root string, trial map[string]any, answers, triggers int) map[string]any {
	t.Helper()
	rep := filepath.Join(root, "rep")
	if err := os.MkdirAll(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{
		"ok": true,
		"guids": map[string]any{"player": cspPlayer, "bot": cspBot,
			"extras": []any{cspExtra1}, "mobs": []any{cspMob, cspMarked}, "marked": cspMarked},
		"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)},
	})
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), cspSeries(200, []any{int64(71)}))
	var events []any
	for i := range triggers {
		events = append(events, cspEvent(10+40*float64(i), "cast_start", cspMob, cspPlayer, cspLightningBolt))
	}
	for i := range answers {
		events = append(events, cspEvent(11.5+40*float64(i), "cast", cspBot, cspMob, 2139))
	}
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), events)
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), []any{
		map[string]any{"kind": "fight_start", "t": int64(0)},
		map[string]any{"kind": "fight_end", "t": int64(200), "ended": "duration"},
	})
	out := score.ScoreRep(rep, trial, nil)
	ruleRows, _ := pyjson.List(out["rules"])
	for _, rv := range ruleRows {
		row, _ := pyjson.Object(rv)
		if pyjson.Str(row["id"]) == cspRuleID {
			return row
		}
	}
	t.Fatalf("rule %s missing from score_rep output", cspRuleID)
	return nil
}

func TestCounterspellPooledTest_RuleIsPooledAndMinNReadsThePooledTotal(t *testing.T) {
	_, rule := cspLoadRule(t)
	if !(pyjson.IsBool(rule["pool"]) && pyjson.Truthy(rule["pool"])) {
		t.Fatalf("pool = %v, want true", rule["pool"])
	}
	var sumN int64
	for _, kn := range cspT1113 {
		sumN += kn[1]
	}
	minN, _ := pyjson.Int64(rule["min_n"])
	if sumN < minN {
		t.Fatalf("sum n %d < min_n %d", sumN, minN)
	}
}

func TestCounterspellPooledTest_T1113ScoresAPooledFailAt10Of18(t *testing.T) {
	trial, _ := cspLoadRule(t)
	tmp := t.TempDir()
	out := score.ScoreTrial(cspWriteReps(t, tmp, cspT1113), trial)
	verdicts, _ := pyjson.List(out["verdicts"])
	row, _ := pyjson.Object(verdicts[0])
	k, _ := pyjson.Int64(row["k"])
	n, _ := pyjson.Int64(row["n"])
	if got, want := pyjson.Str(row["verdict"]), "FAIL"; got != want {
		t.Fatalf("verdict = %q, want %q", got, want)
	}
	if k != 10 || n != 18 {
		t.Fatalf("k/n = %d/%d, want 10/18", k, n)
	}
}

func TestCounterspellPooledTest_AFourTriggerRepeatIsScoredOnlyWhenPooled(t *testing.T) {
	// t1113 rep2/rep4: 4 triggers, under min_n 5. Pooled, the per-repeat guard does not
	// run and the repeat contributes 4/4; without `pool` it is INCONCLUSIVE (the old JSON).
	trial, _ := cspLoadRule(t)
	tmp := t.TempDir()
	row := cspWriteCastingRep(t, filepath.Join(tmp, "pooled"), trial, 4, 4)
	k, _ := pyjson.Int64(row["k"])
	n, _ := pyjson.Int64(row["n"])
	if got := pyjson.Str(row["verdict"]); got != "PASS" {
		t.Fatalf("pooled verdict = %q, want PASS", got)
	}
	if k != 4 || n != 4 {
		t.Fatalf("k/n = %d/%d, want 4/4", k, n)
	}
	legacy := cspClone(t, trial)
	legacyRules, _ := pyjson.List(legacy["rules"])
	for _, rv := range legacyRules {
		rule, _ := pyjson.Object(rv)
		if pyjson.Str(rule["id"]) == cspRuleID {
			delete(rule, "pool")
		}
	}
	row = cspWriteCastingRep(t, filepath.Join(tmp, "legacy"), legacy, 4, 4)
	if got := pyjson.Str(row["verdict"]); got != "INCONCLUSIVE" {
		t.Fatalf("legacy verdict = %q, want INCONCLUSIVE", got)
	}
	if reason := pyjson.Str(row["reason"]); !strings.Contains(reason, "n 4 < min_n 5") {
		t.Fatalf("legacy reason = %q, want 'n 4 < min_n 5'", reason)
	}
}

// cspClone deep-copies a decoded trial the way copy.deepcopy does for JSON values.
func cspClone(t *testing.T, doc any) map[string]any {
	t.Helper()
	data, err := pyjson.Marshal(doc)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	decoded, err := pyjson.Unmarshal(data)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	out, _ := pyjson.Object(decoded)
	return out
}

func cspItoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
