package trialscore

// Translated from tests/test_trials.py: the scoring/report shapes (shield counts, rebasing,
// normaliser dedupe, setup guards, latency/uptime/first/rate metrics, majority, compare, PR block).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/report"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
	"github.com/pjerra/forever-agent/internal/trials/spells"
	"github.com/pjerra/forever-agent/internal/trials/wr"
)

const (
	tcPlayer = "0x0000000000000001"
	tcBot    = "0x0000000000000002"
	tcMob    = "0xF13000000000ABCD"
	tcMob2   = "0xF13000000000BEEF"
	tcPet    = "0x0000000000000003"
)

// tcTrial is the def-arms shield trial of the Python file.
func tcTrial() map[string]any {
	return map[string]any{
		"id":    "priest-holy-pws-def-arms",
		"title": "Holy priest: no shield on an Arms warrior tanking in Defensive Stance",
		"group": "healers",
		"class": "priest",
		"spec":  "holy",
		"player": map[string]any{"class": "warrior", "level": int64(35),
			"talents":     map[string]any{"tree": "arms", "spells": []any{int64(12285), int64(12295)}},
			"stance":      int64(71),
			"forced_role": nil,
			"auras":       []any{},
			"items":       []any{},
			"gear":        nil,
		},
		"bot": map[string]any{"class": "priest", "spec": "holy", "role": "healer", "level": int64(35),
			"gear": "blue", "items": []any{map[string]any{"name": "Morning Glory Dew", "count": int64(20)}},
			"mana_pct": nil, "cc_mark": nil},
		"situation": map[string]any{"kind": "tank_damage",
			"mobs":      map[string]any{"role": "melee", "count": int64(1), "level_delta": int64(1), "refill": true},
			"hp_band":   []any{int64(40), int64(90)},
			"floor_pct": int64(30), "victim": "player",
			"pull_after_s": int64(0), "ooc_wait_s": int64(0), "debuff": nil},
		"setup_checks": []any{"player_level", "player_visible", "player_talents", "player_stance",
			"player_forced_role", "bot_present", "bot_level",
			"bot_spec", "grouped", "mobs_ready", "config_read"},
		"duration_s":  int64(180),
		"repeats":     int64(2),
		"min_valid_s": int64(120),
		"opportunity": map[string]any{"unit": "$player", "hp_lt": int64(90), "hp_gt": "$critical",
			"in_combat": true},
		"rules": []any{map[string]any{
			"id":           "PRI-PWS-DEF-ARMS",
			"desc":         "no shield on the player above critical in combat",
			"source":       "ai/playerbot/strategy/priest/PriestActions.h:42; issue #1553",
			"expect_today": "FAIL",
			"why_expect":   "IsTank reads talents/forced role, not stance",
			"check": map[string]any{"metric": "count",
				"where": map[string]any{"kind": "cast", "src": "$bot", "dst": "$player",
					"spell":     "Power Word: Shield",
					"dst_hp_gt": "$critical", "in_combat": true}},
			"pass_if":           "== 0",
			"min_opportunity_s": int64(90),
			"min_n":             int64(0),
		}},
	}
}

// tcTrialWith applies the Python `_trial(**over)`: top-level keys replaced.
func tcTrialWith(over map[string]any) map[string]any {
	trial := tcTrial()
	for k, v := range over {
		trial[k] = v
	}
	return trial
}

// tcShort is `_short()`: the def-arms trial without the opportunity guard.

func tcCtx() *rules.Ctx {
	return &rules.Ctx{
		Guids:             map[string]any{"player": tcPlayer, "bot": tcBot, "pet": nil, "mobs": []any{tcMob}, "marked": nil},
		Thresholds:        map[string]any{"critical": 20.0, "low": 50.0, "medium": 70.0},
		Trial:             tcTrial(),
		ThresholdsAssumed: false,
	}
}

func tcLatCtx() *rules.Ctx {
	ctx := tcCtx()
	ctx.Guids = map[string]any{"player": tcPlayer, "bot": tcBot, "mobs": []any{tcMob, tcMob2}}
	return ctx
}

func tcCast(t, hp float64, src, dst string, spellID int64) map[string]any {
	sguid := map[string]string{"bot": tcBot, "player": tcPlayer}[src]
	if sguid == "" {
		sguid = src
	}
	dguid := map[string]string{"player": tcPlayer, "bot": tcBot}[dst]
	if dguid == "" {
		dguid = dst
	}
	srcUnit := ""
	switch sguid {
	case tcBot:
		srcUnit = "$bot"
	case tcPlayer:
		srcUnit = "$player"
	}
	dstUnit := ""
	switch dguid {
	case tcPlayer:
		dstUnit = "$player"
	case tcBot:
		dstUnit = "$bot"
	}
	var hpVal any
	if hp >= 0 {
		hpVal = hp
	}
	return map[string]any{"t": t, "wall": nil, "kind": "cast", "src": src, "src_guid": sguid,
		"dst": dst, "dst_guid": dguid,
		"src_unit": srcUnit, "dst_unit": dstUnit,
		"spell_id": spellID, "spell": "Power Word: Shield", "amount": nil,
		"dst_hp_pct": hpVal, "src_role": "bot", "dst_role": "player", "origin": "ta"}
}

func tcSample(t float64, guid string, hp float64, maxHP float64, combat bool, auras []int64) map[string]any {
	auraVals := make([]any, len(auras))
	for i, a := range auras {
		auraVals[i] = a
	}
	return map[string]any{"t": t, "wall": nil, "unit": "player", "guid": guid,
		"hp": hp, "max_hp": maxHP, "power": 500, "max_power": 500,
		"auras": auraVals, "casting": nil, "in_combat": combat,
		"target": "", "x": 0.0, "y": 0.0, "z": 0.0}
}

func tcExcluded(a, b float64) map[string]any {
	return map[string]any{"t": a, "wall": nil, "kind": "excluded", "src": "", "src_guid": "",
		"dst": "", "dst_guid": "", "src_unit": "", "dst_unit": "",
		"spell_id": nil, "spell": nil, "amount": nil, "dst_hp_pct": nil,
		"src_role": "other", "dst_role": "other", "origin": "marks",
		"from": a, "to": b, "why": "test"}
}

func tcEv(t float64, kind, srcGUID, dstGUID string, spellID any, srcRole, dstRole string, hp any) map[string]any {
	return map[string]any{"t": t, "wall": nil, "kind": kind, "src": "s", "src_guid": srcGUID,
		"dst": "d", "dst_guid": dstGUID, "src_unit": "", "dst_unit": "",
		"spell_id": spellID, "spell": nil, "amount": int64(1), "dst_hp_pct": hp,
		"src_role": srcRole, "dst_role": dstRole, "origin": "ta"}
}

func tcRule(check map[string]any) map[string]any {
	return map[string]any{"id": "R3", "desc": "d", "source": "s", "expect_today": "?",
		"why_expect": "x", "check": check, "pass_if": ">= 0.5",
		"min_opportunity_s": int64(0), "min_n": int64(0)}
}

func tcObj(v any) map[string]any { return pyjson.ObjectOrEmpty(v) }

func tcList(v any) []any { l, _ := pyjson.List(v); return l }

// tcRows is the trial's rules list as objects.
func tcRows(trial map[string]any) []any { return tcList(trial["rules"]) }

// tcVerdictRow is doc["verdicts"][i] (the trial-level rows ScoreTrial returns).
func tcVerdictRow(t *testing.T, doc map[string]any, i int) map[string]any {
	t.Helper()
	rows := tcList(doc["verdicts"])
	if i >= len(rows) {
		t.Fatalf("no verdict row %d in %v", i, doc["verdicts"])
	}
	return tcObj(rows[i])
}

func tcRow(t *testing.T, doc map[string]any, i int) map[string]any {
	t.Helper()
	rows := tcList(doc["rules"])
	if i >= len(rows) {
		t.Fatalf("no rule row %d in %v", i, doc["rules"])
	}
	return tcObj(rows[i])
}

func tcWriteRep(t *testing.T, runDir, trialID, rep string, setup map[string]any, samples, marks, events []any) string {
	t.Helper()
	dir := filepath.Join(runDir, trialID, rep)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, "setup.json"), setup)
	writeJSONL(t, filepath.Join(dir, "samples.jsonl"), samples)
	writeJSONL(t, filepath.Join(dir, "marks.jsonl"), marks)
	writeJSONL(t, filepath.Join(dir, "events.jsonl"), events)
	return dir
}

// --- ShieldCount ---------------------------------------------------------------------

func TestShieldCount_IgnoresSelfAndCritical(t *testing.T) {
	trial := tcTrial()
	rule := tcObj(tcRows(trial)[0])
	rows := []map[string]any{
		tcCast(10.0, 100.0, "bot", "bot", 6065),
		tcCast(20.0, 100.0, "bot", "bot", 6065),
		tcCast(30.0, 100.0, "bot", "bot", 6065),
		tcCast(40.0, 15.0, "bot", "player", 6065),
		tcCast(60.0, 60.0, "bot", "player", 6065),
		tcCast(70.0, 60.0, "bot", "player", 6065),
	}
	var samples []map[string]any
	for i := 0; i < 80; i++ {
		samples = append(samples, tcSample(float64(i), tcBot, 900, 1000, true, nil))
	}
	res := rules.Evaluate(rule, rows, samples, nil, tcCtx())
	assertEqual(t, int64(2), res.Value)
	assertEqual(t, "FAIL", res.Verdict)
}

// --- Rebase -------------------------------------------------------------------------

func tcSetupOK() map[string]any {
	return map[string]any{"ok": true, "guids": map[string]any{
		"player": tcPlayer, "bot": tcBot, "pet": nil, "mobs": []any{tcMob}, "marked": nil}}
}

func TestRebase_FightStartRebasesSamplesAndMarks(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "setup.json"), tcSetupOK())
	writeJSONL(t, filepath.Join(dir, "marks.jsonl"), []any{
		map[string]any{"kind": "setup_ok", "wall": "2026-01-01T00:00:00+00:00"},
		map[string]any{"kind": "fight_start", "wall": "2026-01-01T00:00:20+00:00"},
		map[string]any{"kind": "excluded", "from": "2026-01-01T00:00:25+00:00",
			"to": "2026-01-01T00:00:27+00:00", "why": "test"},
	})
	writeJSONL(t, filepath.Join(dir, "samples.jsonl"), []any{
		map[string]any{"t": -999.0, "wall": "2026-01-01T00:00:50+00:00", "guid": tcPlayer,
			"hp": int64(300), "max_hp": int64(1000), "auras": []any{}, "in_combat": true},
	})
	writeJSONL(t, filepath.Join(dir, "raw-ta.jsonl"), []any{
		map[string]any{"seq": int64(7), "time": "2026-01-01T00:00:50+00:00",
			"kind": "spell_go", "source": map[string]any{"name": "b", "guid": tcBot},
			"target":   map[string]any{"name": "p", "guid": tcPlayer},
			"spell_id": int64(6065)},
	})
	rows, err := events.Normalise(dir, tcTrial())
	if err != nil {
		t.Fatal(err)
	}
	var casts []map[string]any
	for _, row := range rows {
		if row["kind"] == "cast" {
			casts = append(casts, row)
		}
	}
	assertEqual(t, 1, len(casts))
	assertAlmostEqual(t, 30.0, num(t, casts[0]["t"]), 7)
	assertEqual(t, 30.0, casts[0]["dst_hp_pct"])
	var excl []map[string]any
	for _, row := range rows {
		if row["kind"] == "excluded" {
			excl = append(excl, row)
		}
	}
	if len(excl) != 1 {
		t.Fatalf("excluded rows = %d", len(excl))
	}
	assertEqual(t, []any{5.0, 7.0}, []any{excl[0]["from"], excl[0]["to"]})
	samples := readJSONL(t, filepath.Join(dir, "samples.jsonl"))
	assertAlmostEqual(t, 30.0, num(t, samples[0]["t"]), 7)
}

func TestRebase_DedupeAndDroppedKinds(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "setup.json"), tcSetupOK())
	writeJSONL(t, filepath.Join(dir, "marks.jsonl"), []any{
		map[string]any{"kind": "setup_ok", "wall": "2026-01-01T00:00:00+00:00"},
	})
	writeJSONL(t, filepath.Join(dir, "samples.jsonl"), []any{
		map[string]any{"t": 10.0, "guid": tcPlayer, "hp": int64(500), "max_hp": int64(1000), "auras": []any{}},
	})
	good := map[string]any{"seq": int64(1), "time": "2026-01-01T00:00:10+00:00",
		"kind": "spell_go", "source": map[string]any{"name": "b", "guid": tcBot},
		"target": map[string]any{"name": "p", "guid": tcPlayer}, "spell_id": int64(6065)}
	sameSeqOther := cloneRow(good)
	sameSeqOther["spell_id"] = int64(17)
	noTime := map[string]any{"kind": "spell_go", "source": map[string]any{"name": "b", "guid": tcBot},
		"target": map[string]any{"name": "p", "guid": tcPlayer}, "spell_id": int64(6065)}
	unknownKind := cloneRow(good)
	unknownKind["seq"] = int64(9)
	unknownKind["kind"] = "emotes"
	writeJSONL(t, filepath.Join(dir, "raw-ta.jsonl"), []any{good, cloneRow(good), sameSeqOther, noTime, unknownKind})
	rows, err := events.Normalise(dir, tcTrial())
	if err != nil {
		t.Fatal(err)
	}
	casts := 0
	for _, row := range rows {
		if row["kind"] == "cast" {
			casts++
		}
	}
	assertEqual(t, 2, casts)
	summary := obj(t, readJSON(t, filepath.Join(dir, "events_summary.json")))
	assertEqual(t, int64(1), tcObj(summary["dropped_kinds"])["emotes"])
	assertEqual(t, int64(1), tcObj(summary["dropped_kinds"])["<missing time>"])
}

func TestRebase_OverlappingExcludedMerged(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "setup.json"), tcSetupOK())
	writeJSONL(t, filepath.Join(dir, "marks.jsonl"), []any{
		map[string]any{"kind": "fight_start", "wall": "2026-01-01T00:00:00+00:00"},
		map[string]any{"kind": "excluded", "from": "2026-01-01T00:00:05+00:00",
			"to": "2026-01-01T00:00:10+00:00", "why": "a"},
		map[string]any{"kind": "excluded", "from": "2026-01-01T00:00:08+00:00",
			"to": "2026-01-01T00:00:12+00:00", "why": "b"},
	})
	writeJSONL(t, filepath.Join(dir, "samples.jsonl"), nil)
	writeJSONL(t, filepath.Join(dir, "raw-ta.jsonl"), nil)
	rows, err := events.Normalise(dir, tcTrial())
	if err != nil {
		t.Fatal(err)
	}
	var excl []map[string]any
	for _, row := range rows {
		if row["kind"] == "excluded" {
			excl = append(excl, row)
		}
	}
	assertEqual(t, 1, len(excl))
	assertEqual(t, []any{5.0, 12.0}, []any{excl[0]["from"], excl[0]["to"]})
}

// --- Normaliser ---------------------------------------------------------------------

func TestNormaliser_DstHpFromLastSample(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "setup.json"), tcSetupOK())
	writeJSONL(t, filepath.Join(dir, "marks.jsonl"), []any{
		map[string]any{"kind": "setup_ok", "wall": "2026-01-01T00:00:00+00:00"},
	})
	writeJSONL(t, filepath.Join(dir, "samples.jsonl"), []any{
		map[string]any{"t": 8.8, "guid": tcPlayer, "hp": int64(500), "max_hp": int64(1000), "auras": []any{}},
		map[string]any{"t": 9.6, "guid": tcPlayer, "hp": int64(800), "max_hp": int64(1000), "auras": []any{}},
	})
	raw := map[string]any{"seq": int64(1), "time": "2026-01-01T00:00:10+00:00",
		"kind": "spell_go", "source": map[string]any{"name": "b", "guid": tcBot},
		"target": map[string]any{"name": "p", "guid": tcPlayer}, "spell_id": int64(6065)}
	writeJSONL(t, filepath.Join(dir, "raw-ta.jsonl"), []any{raw, cloneRow(raw)})
	rows, err := events.Normalise(dir, tcTrial())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 1, len(rows))
	assertEqual(t, "cast", rows[0]["kind"])
	assertEqual(t, 80.0, rows[0]["dst_hp_pct"])
	writeJSONL(t, filepath.Join(dir, "samples.jsonl"), []any{
		map[string]any{"t": 5.0, "guid": tcPlayer, "hp": int64(500), "max_hp": int64(1000), "auras": []any{}},
	})
	rows, err = events.Normalise(dir, tcTrial())
	if err != nil {
		t.Fatal(err)
	}
	if rows[0]["dst_hp_pct"] != nil {
		t.Fatalf("dst_hp_pct = %v, want nil", rows[0]["dst_hp_pct"])
	}
}

// --- SetupInvalid / CutShort / Opportunity ------------------------------------------

func TestSetupInvalid_NotScored(t *testing.T) {
	dir := t.TempDir()
	rep := tcWriteRep(t, dir, "t", "rep1",
		map[string]any{"ok": false, "invalid_reason": "stance not held", "guids": map[string]any{}},
		nil, nil, nil)
	out := score.ScoreRep(rep, tcTrial(), nil)
	for _, rowVal := range tcList(out["rules"]) {
		assertEqual(t, "INVALID", tcObj(rowVal)["verdict"])
	}
	if !strings.Contains(pyjson.Str(out["invalid_reason"]), "stance") {
		t.Fatalf("invalid_reason = %v", out["invalid_reason"])
	}
}

func TestCutShort_Invalid(t *testing.T) {
	dir := t.TempDir()
	var samples []any
	for i := 0; i <= 100; i++ {
		samples = append(samples, tcSample(float64(i), tcPlayer, 500, 1000, true, nil))
	}
	marks := []any{map[string]any{"kind": "fight_end", "ended": "player died"}}
	rep := tcWriteRep(t, dir, "t", "rep1",
		map[string]any{"ok": true, "guids": map[string]any{"player": tcPlayer, "bot": tcBot}},
		samples, marks, nil)
	out := score.ScoreRep(rep, tcTrial(), nil)
	for _, rowVal := range tcList(out["rules"]) {
		assertEqual(t, "INVALID", tcObj(rowVal)["verdict"])
	}
	reason := pyjson.Str(out["invalid_reason"])
	if !strings.Contains(reason, "cut short") || !strings.Contains(reason, "player died") {
		t.Fatalf("invalid_reason = %q", reason)
	}
}

func TestOpportunity_InconclusiveBelowOpportunity(t *testing.T) {
	dir := t.TempDir()
	var samples []any
	for i := 0; i < 200; i++ {
		hp := 800.0
		if i >= 60 {
			hp = 950.0
		}
		samples = append(samples, tcSample(float64(i), tcPlayer, hp, 1000, true, nil))
	}
	rep := tcWriteRep(t, dir, "t", "rep1",
		map[string]any{"ok": true, "guids": map[string]any{"player": tcPlayer, "bot": tcBot}},
		samples, nil, []any{tcCast(200.0, 80.0, "bot", "player", 6065)})
	out := score.ScoreRep(rep, tcTrial(), nil)
	assertEqual(t, "INCONCLUSIVE", tcRow(t, out, 0)["verdict"])
}

// --- Latency ------------------------------------------------------------------------

func tcLatTrial() map[string]any {
	trial := tcTrial()
	trial["rules"] = []any{map[string]any{
		"id": "LAT", "desc": "x", "source": "s", "expect_today": "?", "why_expect": "x",
		"check": map[string]any{"metric": "latency",
			"trigger":  map[string]any{"kind": "cross", "unit": "$player", "below": int64(50)},
			"response": map[string]any{"kind": "heal", "src": "$bot", "dst": "$player"},
			"within_s": int64(4)},
		"pass_if": ">= 0.8", "min_opportunity_s": int64(0), "min_n": int64(0)}}
	return trial
}

func tcHeal(t float64) map[string]any {
	return map[string]any{"t": t, "kind": "heal", "src": "b", "src_guid": tcBot,
		"dst": "p", "dst_guid": tcPlayer, "src_unit": "$bot",
		"dst_unit": "$player", "spell_id": int64(1), "spell": "x",
		"amount": int64(1), "dst_hp_pct": 40.0, "src_role": "bot",
		"dst_role": "player", "origin": "ta"}
}

func TestLatency_WindowAndExcluded(t *testing.T) {
	trial := tcLatTrial()
	rule := tcObj(tcRows(trial)[0])
	var samples []map[string]any
	for i := 0; i < 30; i++ {
		hp := 600.0
		if i >= 10 {
			hp = 400.0
		}
		samples = append(samples, tcSample(float64(i), tcPlayer, hp, 1000, true, nil))
	}
	rows := []map[string]any{tcHeal(13.0)}
	ctx := &rules.Ctx{Guids: map[string]any{"player": tcPlayer, "bot": tcBot},
		Thresholds: map[string]any{}, Trial: trial}
	res := rules.Evaluate(rule, rows, samples, nil, ctx)
	assertEqual(t, 1.0, res.Value)
	assertEqual(t, "PASS", res.Verdict)
	assertEqual(t, int64(1), res.K)
	assertEqual(t, int64(1), res.N)
	// A trigger inside an excluded span is skipped -> no triggers.
	exclRows := append(append([]map[string]any{}, rows...), tcExcluded(9.0, 12.0))
	res = rules.Evaluate(rule, exclRows, samples, nil, ctx)
	assertEqual(t, "INCONCLUSIVE", res.Verdict)
}

func TestLatency_WindowCrossingExcludedSkipped(t *testing.T) {
	trial := tcLatTrial()
	rule := tcObj(tcRows(trial)[0])
	var samples []map[string]any
	for i := 0; i < 30; i++ {
		hp := 600.0
		if i >= 10 {
			hp = 400.0
		}
		samples = append(samples, tcSample(float64(i), tcPlayer, hp, 1000, true, nil))
	}
	rows := []map[string]any{tcHeal(13.0), tcExcluded(11.0, 12.0)}
	ctx := &rules.Ctx{Guids: map[string]any{"player": tcPlayer, "bot": tcBot},
		Thresholds: map[string]any{}, Trial: trial}
	res := rules.Evaluate(rule, rows, samples, nil, ctx)
	assertEqual(t, "INCONCLUSIVE", res.Verdict)
}

func tcDmg(t float64, guid, name string) map[string]any {
	return map[string]any{"t": t, "kind": "damage", "src": name, "src_guid": guid,
		"dst": "p", "dst_guid": tcPlayer, "src_unit": "x",
		"dst_unit": "$player", "spell_id": nil, "spell": nil,
		"amount": int64(1), "dst_hp_pct": 40.0, "src_role": "mob",
		"dst_role": "player", "origin": "ta"}
}

func TestLatency_DebouncePerSrc(t *testing.T) {
	trial := tcTrial()
	m2 := tcMob2
	trigRows := []map[string]any{tcDmg(10.0, tcMob, "m"), tcDmg(12.0, m2, "m2"), tcDmg(13.0, tcMob, "m")}
	trial["rules"] = []any{map[string]any{
		"id": "LAT", "desc": "x", "source": "s", "expect_today": "?", "why_expect": "x",
		"check": map[string]any{"metric": "latency",
			"trigger":  map[string]any{"kind": "damage"},
			"response": map[string]any{"kind": "heal", "src": "$bot", "dst": "$player"},
			"within_s": int64(10), "trigger_debounce_s": int64(5)},
		"pass_if": ">= 0.8", "min_opportunity_s": int64(0), "min_n": int64(0)}}
	rule := tcObj(tcRows(trial)[0])
	ctx := &rules.Ctx{Guids: map[string]any{"player": tcPlayer, "bot": tcBot, "mobs": []any{tcMob, m2}},
		Thresholds: map[string]any{}, Trial: trial, Rows: trigRows}
	res := rules.Evaluate(rule, trigRows, nil, nil, ctx)
	// 3 triggers, one debounced (same src 3 s later) -> counted 2.
	assertEqual(t, int64(2), res.N)
	assertEqual(t, "FAIL", res.Verdict)
	// ... and the same test at where level agrees.
	tcObj(rule["check"])["trigger_debounce_s"] = nil
	tcObj(rule["check"])["where"] = map[string]any{"trigger_debounce_s": int64(5)}
	res = rules.Evaluate(rule, trigRows, nil, nil, ctx)
	assertEqual(t, int64(2), res.N)
}

func TestLatency_ResponseDstGUID(t *testing.T) {
	trial := tcTrial()
	trial["rules"] = []any{map[string]any{
		"id": "LAT", "desc": "x", "source": "s", "expect_today": "?", "why_expect": "x",
		"check": map[string]any{"metric": "latency",
			"trigger":  map[string]any{"kind": "damage", "dst": "$player"},
			"response": map[string]any{"kind": "heal", "src": "$bot"},
			"within_s": int64(4), "response_dst": "$trigger.dst"},
		"pass_if": ">= 0.8", "min_opportunity_s": int64(0), "min_n": int64(0)}}
	trig := map[string]any{"t": 10.0, "kind": "damage", "src": "Wrong Name", "src_guid": tcMob,
		"dst": "Also Wrong", "dst_guid": tcPlayer, "src_unit": "mob:x",
		"dst_unit": "$player", "spell_id": nil, "spell": nil,
		"amount": int64(1), "dst_hp_pct": 40.0, "src_role": "mob",
		"dst_role": "player", "origin": "ta"}
	resp := map[string]any{"t": 11.0, "kind": "heal", "src": "b", "src_guid": tcBot,
		"dst": "Renamed Player", "dst_guid": tcPlayer, "src_unit": "$bot",
		"dst_unit": "$player", "spell_id": int64(1), "spell": "x",
		"amount": int64(1), "dst_hp_pct": 40.0, "src_role": "bot",
		"dst_role": "player", "origin": "ta"}
	rule := tcObj(tcRows(trial)[0])
	ctx := &rules.Ctx{Guids: map[string]any{"player": tcPlayer, "bot": tcBot},
		Thresholds: map[string]any{}, Trial: trial, Rows: []map[string]any{trig, resp}}
	res := rules.Evaluate(rule, []map[string]any{trig, resp}, nil, nil, ctx)
	assertEqual(t, 1.0, res.Value)
}

// --- Uptime / first / rate ----------------------------------------------------------

func TestUptimeFirstRate_UptimeFromSamples(t *testing.T) {
	trial := tcTrial()
	rule := map[string]any{"id": "U", "desc": "x", "source": "s", "expect_today": "?",
		"why_expect": "x",
		"check": map[string]any{"metric": "uptime", "unit": "$player", "aura": []any{int64(71)},
			"phase": "in_combat"},
		"pass_if": ">= 0.5", "min_opportunity_s": int64(0), "min_n": int64(0)}
	var samples []map[string]any
	for i := 0; i < 20; i++ {
		var auras []int64
		if i < 15 {
			auras = []int64{71}
		}
		samples = append(samples, tcSample(float64(i), tcPlayer, 500, 1000, true, auras))
	}
	ctx := &rules.Ctx{Guids: map[string]any{"player": tcPlayer}, Trial: trial}
	res := rules.Evaluate(rule, nil, samples, nil, ctx)
	assertAlmostEqual(t, 0.75, num(t, res.Value), 7)
	assertEqual(t, "PASS", res.Verdict)
}

func TestUptimeFirstRate_FirstAndRate(t *testing.T) {
	first := map[string]any{"id": "F", "desc": "x", "source": "s", "expect_today": "?",
		"why_expect": "x",
		"check": map[string]any{"metric": "first",
			"where": map[string]any{"kind": "cast", "spell": "Power Word: Shield"}},
		"pass_if": "< 30", "min_opportunity_s": int64(0), "min_n": int64(0)}
	ctx := tcCtx()
	res := rules.Evaluate(first, []map[string]any{tcCast(12.0, 80.0, "bot", "player", 6065)}, nil, nil, ctx)
	assertEqual(t, 12.0, res.Value)
	assertEqual(t, "PASS", res.Verdict)
	rate := cloneRow(first)
	rate["id"] = "R"
	rate["check"] = map[string]any{"metric": "rate",
		"where": map[string]any{"kind": "cast", "spell": "Power Word: Shield"}}
	rate["pass_if"] = ">= 1"
	rows := []map[string]any{tcCast(10.0, 80.0, "bot", "player", 6065), tcCast(70.0, 80.0, "bot", "player", 6065)}
	samples := []map[string]any{tcSample(10.0, tcPlayer, 500, 1000, true, nil),
		tcSample(70.0, tcPlayer, 500, 1000, true, nil)}
	res = rules.Evaluate(rate, rows, samples, nil, tcCtx())
	assertAlmostEqual(t, 2.0, num(t, res.Value), 7)
	assertEqual(t, "PASS", res.Verdict)
}

func TestUptimeFirstRate_FirstNoMatchIsNull(t *testing.T) {
	rule := map[string]any{"id": "F", "desc": "x", "source": "s", "expect_today": "?",
		"why_expect": "x",
		"check": map[string]any{"metric": "first",
			"where": map[string]any{"kind": "cast", "spell": "Power Word: Shield"}},
		"pass_if": "< 30", "min_opportunity_s": int64(0), "min_n": int64(0)}
	ctx := tcCtx()
	res := rules.Evaluate(rule, nil, nil, nil, ctx)
	if res.Value != nil {
		t.Fatalf("value = %v, want nil", res.Value)
	}
	assertEqual(t, true, res.NoMatch)
	assertEqual(t, "FAIL", res.Verdict)
	rule["pass_if"] = "> 30"
	res = rules.Evaluate(rule, nil, nil, nil, ctx)
	assertEqual(t, "PASS", res.Verdict)
}

func TestUptimeFirstRate_PetAliveMissingSamplesNotAlive(t *testing.T) {
	pet := tcPet
	rule := map[string]any{"id": "U", "desc": "x", "source": "s", "expect_today": "?",
		"why_expect": "x",
		"check":      map[string]any{"metric": "uptime", "unit": "$pet", "alive": true},
		"pass_if":    ">= 0.5", "min_opportunity_s": int64(0), "min_n": int64(0)}
	var samples []map[string]any
	for i := 0; i < 20; i++ {
		samples = append(samples, tcSample(float64(i), tcPlayer, 500, 1000, true, nil))
		if i < 2 {
			samples = append(samples, tcSample(float64(i), pet, 500, 1000, true, nil))
		}
	}
	ctx := &rules.Ctx{Guids: map[string]any{"player": tcPlayer, "pet": pet}, Trial: tcTrial()}
	res := rules.Evaluate(rule, nil, samples, nil, ctx)
	assertAlmostEqual(t, 0.1, num(t, res.Value), 7)
	assertEqual(t, "FAIL", res.Verdict)
}

func TestUptimeFirstRate_HpWindowLastWithin1s(t *testing.T) {
	trial := tcTrial()
	rule := tcObj(tcRows(trial)[0])
	row := tcCast(10.0, -1, "bot", "player", 6065) // hp nil
	samples := []map[string]any{tcSample(9.0, tcPlayer, 900, 1000, true, nil),
		tcSample(9.6, tcPlayer, 300, 1000, true, nil),
		tcSample(11.0, tcPlayer, 100, 1000, true, nil)}
	_ = rules.Evaluate(rule, []map[string]any{row}, samples, nil, tcCtx())
	// Direct check through the normaliser: last within 1 s (9.6) wins.
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "setup.json"), tcSetupOK())
	writeJSONL(t, filepath.Join(dir, "marks.jsonl"), []any{
		map[string]any{"kind": "setup_ok", "wall": "2026-01-01T00:00:00+00:00"},
	})
	writeJSONL(t, filepath.Join(dir, "samples.jsonl"), []any{
		map[string]any{"t": 8.5, "guid": tcPlayer, "hp": int64(900), "max_hp": int64(1000), "auras": []any{}},
		map[string]any{"t": 9.6, "guid": tcPlayer, "hp": int64(300), "max_hp": int64(1000), "auras": []any{}},
		map[string]any{"t": 11.0, "guid": tcPlayer, "hp": int64(100), "max_hp": int64(1000), "auras": []any{}},
	})
	writeJSONL(t, filepath.Join(dir, "raw-ta.jsonl"), []any{
		map[string]any{"seq": int64(1), "time": "2026-01-01T00:00:10+00:00",
			"kind": "spell_go", "source": map[string]any{"name": "b", "guid": tcBot},
			"target": map[string]any{"name": "p", "guid": tcPlayer}, "spell_id": int64(6065)},
	})
	rows, err := events.Normalise(dir, tcTrial())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, 30.0, rows[0]["dst_hp_pct"])
	// 1.5 s stale -> null.
	writeJSONL(t, filepath.Join(dir, "samples.jsonl"), []any{
		map[string]any{"t": 8.5, "guid": tcPlayer, "hp": int64(900), "max_hp": int64(1000), "auras": []any{}},
	})
	rows, err = events.Normalise(dir, tcTrial())
	if err != nil {
		t.Fatal(err)
	}
	if rows[0]["dst_hp_pct"] != nil {
		t.Fatalf("dst_hp_pct = %v, want nil", rows[0]["dst_hp_pct"])
	}
}

func TestUptimeFirstRate_MissingHpNeverPassesGate(t *testing.T) {
	trial := tcTrial()
	rule := tcObj(tcRows(trial)[0])
	row := tcCast(10.0, -1, "bot", "player", 6065)
	samples := []map[string]any{tcSample(10.0, tcBot, 900, 1000, true, nil)}
	res := rules.Evaluate(rule, []map[string]any{row}, samples, nil, tcCtx())
	assertEqual(t, int64(1), res.Value)
	assertEqual(t, "FAIL", res.Verdict)
}

// --- Majority -----------------------------------------------------------------------

func tcRep(t *testing.T, root, name, verdict string, value, k, n int64) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, "verdicts.json"), map[string]any{
		"trial": tcTrial()["id"], "rep": name, "ok_setup": true, "scored_s": 180.0,
		"rules": []any{map[string]any{"id": "PRI-PWS-DEF-ARMS", "verdict": verdict,
			"value": value, "k": k, "n": n, "evidence": []any{},
			"guard": "", "invalid_reason": ""}},
	})
}

func TestMajority_TieFails(t *testing.T) {
	dir := t.TempDir()
	trial := tcTrial()
	trial["repeats"] = int64(2)
	tcRep(t, dir, "rep1", "PASS", 0, 1, 1)
	tcRep(t, dir, "rep2", "FAIL", 0, 0, 1)
	out := score.ScoreTrial(dir, trial)
	row := tcVerdictRow(t, out, 0)
	assertEqual(t, "FAIL", row["verdict"])
	assertEqual(t, []any{int64(1), int64(2)}, []any{row["k"], row["n"]})

	dir2 := t.TempDir()
	for i, v := range []string{"PASS", "PASS", "FAIL"} {
		var k int64
		if v == "PASS" {
			k = 1
		}
		tcRep(t, dir2, "rep"+itoa(i+1), v, 0, k, 1)
	}
	trial3 := tcTrial()
	trial3["repeats"] = int64(3)
	out = score.ScoreTrial(dir2, trial3)
	row = tcVerdictRow(t, out, 0)
	assertEqual(t, "PASS", row["verdict"])
	assertEqual(t, []any{int64(2), int64(3)}, []any{row["k"], row["n"]})

	dir3 := t.TempDir()
	tcRep(t, dir3, "rep1", "INVALID", 0, 0, 0)
	tcRep(t, dir3, "rep2", "PASS", 0, 3, 4)
	out = score.ScoreTrial(dir3, trial)
	row = tcVerdictRow(t, out, 0)
	assertEqual(t, "PASS", row["verdict"])
	assertEqual(t, int64(4), row["n"])
}

func itoa(i int) string {
	return pyjson.Str(int64(i))
}

// --- Validate -----------------------------------------------------------------------

func TestValidate_Rejects(t *testing.T) {
	base := tcTrial()
	badMetric := pyjson.ObjectOrEmpty(deepCopyJSON(base))
	tcObj(tcObj(tcObj(tcRows(badMetric)[0])["check"]))["metric"] = "frobnicate"
	errs := scenario.Validate(badMetric)
	if !containsStr(errs, "unknown metric") {
		t.Fatalf("errs = %v", errs)
	}
	badPass := pyjson.ObjectOrEmpty(deepCopyJSON(base))
	tcObj(tcRows(badPass)[0])["pass_if"] = "=< 3"
	if errs := scenario.Validate(badPass); !containsStr(errs, "bad pass_if") {
		t.Fatalf("errs = %v", errs)
	}
	badWhere := pyjson.ObjectOrEmpty(deepCopyJSON(base))
	tcObj(tcObj(tcRows(badWhere)[0])["check"])["where"].(map[string]any)["frobnicate"] = int64(1)
	if errs := scenario.Validate(badWhere); !containsStr(errs, "unknown where key") {
		t.Fatalf("errs = %v", errs)
	}
	badSpell := pyjson.ObjectOrEmpty(deepCopyJSON(base))
	tcObj(tcObj(tcRows(badSpell)[0])["check"])["where"].(map[string]any)["spell"] = "Not A Spell At All"
	if errs := scenario.Validate(badSpell); !containsStr(errs, "unknown spell") {
		t.Fatalf("errs = %v", errs)
	}
}

func TestValidate_StrictSchema(t *testing.T) {
	base := tcTrial()
	for _, tc := range []struct {
		doc    map[string]any
		needle string
	}{
		{tcTrialWith(map[string]any{"bogus": int64(1)}), "unknown top-level field"},
		{tcTrialWith(map[string]any{"player": map[string]any{"bogus": int64(1)}}), "unknown player field"},
		{tcTrialWith(map[string]any{"bot": map[string]any{"bogus": int64(1)}}), "unknown bot field"},
		{tcTrialWith(map[string]any{"situation": map[string]any{"bogus": int64(1)}}), "unknown situation field"},
	} {
		if errs := scenario.Validate(tc.doc); !containsStr(errs, tc.needle) {
			t.Fatalf("%s: errs = %v", tc.needle, errs)
		}
	}
	ruleNoKind := pyjson.ObjectOrEmpty(deepCopyJSON(base))
	delete(tcObj(tcObj(tcRows(ruleNoKind)[0])["check"])["where"].(map[string]any), "kind")
	if errs := scenario.Validate(ruleNoKind); !containsStr(errs, "needs a kind") {
		t.Fatalf("errs = %v", errs)
	}
	badKind := pyjson.ObjectOrEmpty(deepCopyJSON(base))
	tcObj(tcObj(tcRows(badKind)[0])["check"])["where"].(map[string]any)["kind"] = "frobnicate"
	if errs := scenario.Validate(badKind); !containsStr(errs, "bad kind") {
		t.Fatalf("errs = %v", errs)
	}
	prefixEscape := pyjson.ObjectOrEmpty(deepCopyJSON(base))
	tcObj(tcObj(tcRows(prefixEscape)[0])["check"])["where"].(map[string]any)["num_cast"] = int64(1)
	if errs := scenario.Validate(prefixEscape); !containsStr(errs, "unknown where key") {
		t.Fatalf("errs = %v", errs)
	}
	badAlias := pyjson.ObjectOrEmpty(deepCopyJSON(base))
	tcObj(tcObj(tcRows(badAlias)[0])["check"])["where"].(map[string]any)["src"] = "$frobnicate"
	if errs := scenario.Validate(badAlias); !containsStr(errs, "bad src alias") {
		t.Fatalf("errs = %v", errs)
	}
	badThr := pyjson.ObjectOrEmpty(deepCopyJSON(base))
	tcObj(tcObj(tcRows(badThr)[0])["check"])["where"].(map[string]any)["dst_hp_gt"] = "$frobnicate"
	if errs := scenario.Validate(badThr); !containsStr(errs, "bad dst_hp_gt") {
		t.Fatalf("errs = %v", errs)
	}
	badTrig := pyjson.ObjectOrEmpty(deepCopyJSON(base))
	tcObj(tcObj(tcRows(badTrig)[0])["check"])["trigger"] = map[string]any{"kind": "frobnicate",
		"unit": "$player", "below": int64(50)}
	if errs := scenario.Validate(badTrig); !containsStr(errs, "bad trigger kind") {
		t.Fatalf("errs = %v", errs)
	}
	badBelow := pyjson.ObjectOrEmpty(deepCopyJSON(base))
	tcObj(tcObj(tcRows(badBelow)[0])["check"])["trigger"] = map[string]any{"kind": "cross",
		"unit": "$player", "below": "$frobnicate"}
	if errs := scenario.Validate(badBelow); !containsStr(errs, "bad trigger below") {
		t.Fatalf("errs = %v", errs)
	}
	// A bad threshold alias never crashes evaluation.
	lat := map[string]any{"id": "L", "desc": "x", "source": "s", "expect_today": "?",
		"why_expect": "x",
		"check": map[string]any{"metric": "latency",
			"trigger":  map[string]any{"kind": "cross", "unit": "$player", "below": "$frobnicate"},
			"response": map[string]any{"kind": "heal"}, "within_s": int64(4)},
		"pass_if": ">= 0.5", "min_opportunity_s": int64(0), "min_n": int64(0)}
	res := rules.Evaluate(lat, nil, []map[string]any{tcSample(1.0, tcPlayer, 100, 1000, true, nil)}, nil, tcCtx())
	assertEqual(t, "INCONCLUSIVE", res.Verdict)
}

func containsStr(errs []string, needle string) bool {
	for _, e := range errs {
		if strings.Contains(e, needle) {
			return true
		}
	}
	return false
}

// deepCopyJSON clones a decoded document (the Python copy.deepcopy in these tests).
func deepCopyJSON(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[k] = deepCopyJSON(item)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = deepCopyJSON(item)
		}
		return out
	}
	return v
}

// --- Compare ------------------------------------------------------------------------

func tcCards(t *testing.T, run, base, metric string, runV, baseV float64, rk, rn, bk, bn int64) {
	t.Helper()
	for _, side := range []struct {
		dir string
		k   int64
		n   int64
		v   float64
	}{{run, rk, rn, runV}, {base, bk, bn, baseV}} {
		rep := filepath.Join(side.dir, "t", "rep1")
		if err := os.MkdirAll(rep, 0o755); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(side.dir, "card.json"), map[string]any{
			"run": map[string]any{"label": "x", "tb_version": "v", "critical_health": int64(20),
				"low_health": int64(50), "medium_health": int64(70)},
			"rows": []any{map[string]any{"trial": "t", "rule": "R", "expected": "?",
				"verdict": "PASS", "k": side.k, "n": side.n, "metric": metric,
				"value": side.v, "values": []any{side.v},
				"pass_if": ">= 0.8", "surprise": "", "source": "s", "detail": []any{}}},
		})
		writeJSON(t, filepath.Join(rep, "verdicts.json"), map[string]any{
			"rules": []any{map[string]any{"id": "R", "verdict": "PASS",
				"value": int64(1), "k": side.k, "n": side.n}},
		})
	}
}

func TestCompare_UsesWalkReportFisher(t *testing.T) {
	run, base := t.TempDir(), t.TempDir()
	tcCards(t, run, base, "ratio", 0.2, 0.9, 2, 10, 9, 10)
	rows := report.Compare(run, base)
	assertEqual(t, 1, len(rows))
	row := obj(t, rows[0])
	assertAlmostEqual(t, wr.FisherTwoSided(2, 8, 9, 1, 1e-7), num(t, row["p"]), 7)
}

func TestCompare_Rows(t *testing.T) {
	run, base := t.TempDir(), t.TempDir()
	tcCards(t, run, base, "ratio", 0.2, 0.9, 2, 10, 9, 10)
	rows := report.Compare(run, base)
	row := obj(t, rows[0])
	assertEqual(t, "2/10", row["k_n_after"])
	assertEqual(t, "9/10", row["k_n_before"])
	assertEqual(t, []any{0.2}, row["values_after"])
	assertEqual(t, []any{0.9}, row["values_before"])
	assertAlmostEqual(t, wr.FisherTwoSided(2, 8, 9, 1, 1e-7), num(t, row["p"]), 7)
	note := pyjson.Str(row["note"])
	if note != "differs (p<0.05)" && note != "suggestive (p<0.20)" && note != "no evidence" {
		t.Fatalf("note = %q", note)
	}
}

func TestCompare_CountNoTest(t *testing.T) {
	run, base := t.TempDir(), t.TempDir()
	tcCards(t, run, base, "count", 3, 1, 0, 0, 0, 0)
	rows := report.Compare(run, base)
	row := obj(t, rows[0])
	if row["p"] != nil {
		t.Fatalf("p = %v, want nil", row["p"])
	}
	if !strings.Contains(pyjson.Str(row["note"]), "diff") {
		t.Fatalf("note = %v", row["note"])
	}
}

// --- PR block -----------------------------------------------------------------------

func TestPrBlock_Format(t *testing.T) {
	before, after := t.TempDir(), t.TempDir()
	for _, d := range []string{before, after} {
		if err := os.MkdirAll(filepath.Join(d, "t", "rep1"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(d, "card.json"), map[string]any{
			"run": map[string]any{"label": "before", "tb_version": "v1"},
			"rows": []any{map[string]any{"trial": "t", "rule": "R", "expected": "?",
				"verdict": "FAIL", "k": int64(0), "n": int64(1),
				"value": int64(3), "pass_if": "== 0",
				"surprise": "", "source": "s", "detail": []any{}}},
		})
		writeJSON(t, filepath.Join(d, "t", "rep1", "setup.json"), map[string]any{"checks": []any{}})
	}
	text := report.PRBlock(before, after, "1553")
	if !strings.HasPrefix(text, "### Tortoise Trials: before/after, issue #1553") {
		t.Fatalf("text = %q", text)
	}
	for _, want := range []string{"Before: before v1",
		"| trial | rule | before | after | value before → after |",
		"|---|---|---|---|---|"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}

func TestPrBlock_BeforeAfterEvidence(t *testing.T) {
	before, after := t.TempDir(), t.TempDir()
	for _, side := range []struct{ d, ev string }{{before, "BEFORE-EV"}, {after, "AFTER-EV"}} {
		if err := os.MkdirAll(filepath.Join(side.d, "t", "rep1"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(side.d, "card.json"), map[string]any{
			"run": map[string]any{"label": "x", "tb_version": "v1"},
			"rows": []any{map[string]any{"trial": "t", "rule": "R", "expected": "?",
				"verdict": "FAIL", "k": int64(0), "n": int64(1),
				"value": int64(3), "pass_if": "== 0", "metric": "count",
				"surprise": "", "source": "s",
				"detail": []any{map[string]any{"rep": "rep1", "verdict": "FAIL",
					"value": int64(3), "evidence": []any{side.ev}}}}},
		})
		writeJSON(t, filepath.Join(side.d, "t", "rep1", "setup.json"), map[string]any{"checks": []any{}})
	}
	text := report.PRBlock(before, after, "1553")
	if !(strings.Index(text, "BEFORE-EV") < strings.Index(text, "AFTER-EV")) {
		t.Fatalf("evidence order wrong:\n%s", text)
	}
	if !strings.Contains(text, "- before: BEFORE-EV") || !strings.Contains(text, "- after: AFTER-EV") {
		t.Fatalf("evidence lines missing:\n%s", text)
	}
	// INCONCLUSIVE/INVALID print their reason.
	card := obj(t, readJSON(t, filepath.Join(after, "card.json")))
	row := obj(t, tcList(card["rows"])[0])
	row["verdict"] = "INCONCLUSIVE"
	row["detail"] = []any{map[string]any{"rep": "rep1", "verdict": "INCONCLUSIVE",
		"value": nil, "evidence": []any{}, "invalid_reason": "walk n == 0"}}
	writeJSON(t, filepath.Join(after, "card.json"), card)
	text = report.PRBlock(before, after, "1553")
	if !strings.Contains(text, "INCONCLUSIVE") || !strings.Contains(text, "walk n == 0") {
		t.Fatalf("reason missing:\n%s", text)
	}
}

// --- Interruptible ------------------------------------------------------------------

func TestInterruptible_CastTimePreventionFlags(t *testing.T) {
	rule := map[string]any{"id": "I", "desc": "x", "source": "s", "expect_today": "?",
		"why_expect": "x",
		"check": map[string]any{"metric": "count",
			"where": map[string]any{"kind": "cast", "spell_interruptible": true}},
		"pass_if": ">= 1", "min_opportunity_s": int64(0), "min_n": int64(0)}
	poly := tcCast(10.0, 80.0, "bot", "player", 118)
	pws := tcCast(11.0, 80.0, "bot", "player", 6065)
	res := rules.Evaluate(rule, []map[string]any{poly, pws}, nil, nil, tcCtx())
	assertEqual(t, int64(1), res.Value)
	assertEqual(t, "PASS", res.Verdict)
}

func TestInterruptible_SharedHelperNeedsAllThreeConditions(t *testing.T) {
	ok := func(castMS, prevention, flags any) bool {
		return spells.IsInterruptible(castMS, prevention, flags)
	}
	if !ok(float64(1500), int64(1), int64(0x02)) {
		t.Fatal("1500 ms silence damage flag must be interruptible")
	}
	if ok(float64(0), int64(1), int64(0x02)) {
		t.Fatal("instant must not be interruptible")
	}
	if ok(float64(-1000000), int64(1), int64(0x02)) {
		t.Fatal("DBC index 18 must not be interruptible")
	}
	if ok(float64(1500), int64(2), int64(0x02)) {
		t.Fatal("prevention type 2 must not be interruptible")
	}
	if ok(float64(1500), int64(0), int64(0x02)) {
		t.Fatal("prevention type 0 must not be interruptible")
	}
	if ok(float64(1500), int64(1), int64(0x01)) {
		t.Fatal("only the damage flag must not be interruptible")
	}
	if ok(float64(1500), int64(1), int64(0)) {
		t.Fatal("no flags must not be interruptible")
	}
	if ok(nil, nil, nil) {
		t.Fatal("nil must not be interruptible")
	}
	if ok(float64(0), int64(1), int64(0x0F)) {
		t.Fatal("channeled (cast_ms 0) must not be interruptible")
	}
}

// --- Scored window ------------------------------------------------------------------

func TestScoredWindow_MinusMergedExcluded(t *testing.T) {
	var samples []map[string]any
	for i := 0; i <= 100; i++ {
		samples = append(samples, tcSample(float64(i), tcPlayer, 500, 1000, true, nil))
	}
	rows := []map[string]any{tcCast(0.0, 80.0, "bot", "player", 6065),
		tcCast(100.0, 80.0, "bot", "player", 6065),
		tcExcluded(10.0, 20.0), tcExcluded(15.0, 25.0)}
	excluded := [][2]float64{{10.0, 25.0}}
	assertAlmostEqual(t, 85.0, rules.ScoredSpan(rows, samples, excluded, nil), 7)
	rawExcluded := [][2]float64{{10.0, 20.0}, {15.0, 25.0}}
	assertAlmostEqual(t, 85.0, score.ScoredSeconds(rows, samples, rawExcluded, nil), 7)
}

func TestScoredWindow_OpportunityIgnoresExcluded(t *testing.T) {
	trial := tcTrial()
	var samples []map[string]any
	for i := 0; i < 100; i++ {
		samples = append(samples, tcSample(float64(i), tcPlayer, 500, 1000, true, nil))
	}
	ctx := tcCtx()
	full := rules.OpportunitySeconds(trial, samples, nil, ctx)
	cut := rules.OpportunitySeconds(trial, samples, [][2]float64{{10.0, 20.0}}, ctx)
	assertAlmostEqual(t, 10.0, full-cut, 0)
}

func TestScoredWindow_MinNGate(t *testing.T) {
	rule := map[string]any{"id": "U", "desc": "x", "source": "s", "expect_today": "?",
		"why_expect": "x",
		"check": map[string]any{"metric": "uptime", "unit": "$player", "aura": []any{int64(71)},
			"phase": "in_combat"},
		"pass_if": ">= 0.5", "min_opportunity_s": int64(0), "min_n": int64(99)}
	var samples []map[string]any
	for i := 0; i < 20; i++ {
		samples = append(samples, tcSample(float64(i), tcPlayer, 500, 1000, true, []int64{71}))
	}
	res := rules.Evaluate(rule, nil, samples, nil, tcCtx())
	assertEqual(t, "PASS", res.Verdict)
	if !(res.N < 99) {
		t.Fatalf("n = %d", res.N)
	}
}

func TestScoredWindow_WalkNZeroInconclusive(t *testing.T) {
	rule := map[string]any{"id": "W", "desc": "x", "source": "s", "expect_today": "?",
		"why_expect": "x",
		"check":      map[string]any{"metric": "walk_metric", "name": "m"},
		"pass_if":    ">= 0.5", "min_opportunity_s": int64(0), "min_n": int64(0)}
	ctx := tcCtx()
	ctx.WalkReport = map[string]any{"metrics": map[string]any{"m": map[string]any{"k": int64(0), "n": int64(0)}}}
	res := rules.Evaluate(rule, nil, nil, nil, ctx)
	assertEqual(t, "INCONCLUSIVE", res.Verdict)
	assertEqual(t, int64(0), res.N)
}

func TestScoredWindow_ZeroValidRepeats(t *testing.T) {
	dir := t.TempDir()
	rep := filepath.Join(dir, "t", "rep1")
	if err := os.MkdirAll(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(rep, "verdicts.json"), map[string]any{"trial": "t", "rep": "rep1",
		"ok_setup": true,
		"rules": []any{map[string]any{"id": "R", "verdict": "INCONCLUSIVE",
			"value": nil, "evidence": []any{"no triggers"},
			"guard": "", "invalid_reason": ""}}})
	trial := map[string]any{"id": "t", "rules": []any{map[string]any{"id": "R", "expect_today": "?",
		"pass_if": ">= 0.5", "source": "s", "desc": "x",
		"check": map[string]any{"metric": "latency"}}}}
	out := score.ScoreTrial(filepath.Join(dir, "t"), trial)
	assertEqual(t, "INCONCLUSIVE", tcVerdictRow(t, out, 0)["verdict"])
}

func TestScoredWindow_SurpriseColumn(t *testing.T) {
	run := t.TempDir()
	if err := os.MkdirAll(filepath.Join(run, "t", "rep1"), 0o755); err != nil {
		t.Fatal(err)
	}
	trial := map[string]any{"id": "t", "verdicts": []any{map[string]any{"id": "R", "verdict": "PASS",
		"k": int64(1), "n": int64(1), "metric": "count",
		"expected": "FAIL", "pass_if": ">= 1",
		"source": "s", "desc": "x", "detail": []any{}}}}
	rows, err := report.WriteCard(run, []map[string]any{trial})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "expected FAIL, got PASS", obj(t, rows[0])["surprise"])
}

func TestScoredWindow_RatioDenZero(t *testing.T) {
	rule := map[string]any{"id": "R", "desc": "x", "source": "s", "expect_today": "?",
		"why_expect": "x",
		"check": map[string]any{"metric": "ratio", "num": map[string]any{"kind": "cast"},
			"den": map[string]any{"kind": "heal"}},
		"pass_if": ">= 0.5", "min_opportunity_s": int64(0), "min_n": int64(0)}
	res := rules.Evaluate(rule, []map[string]any{tcCast(1.0, 80.0, "bot", "player", 6065)}, nil, nil, tcCtx())
	assertEqual(t, "INCONCLUSIVE", res.Verdict)
	assertEqual(t, int64(0), res.N)
}

func TestScoredWindow_UptimeNoSamples(t *testing.T) {
	rule := map[string]any{"id": "U", "desc": "x", "source": "s", "expect_today": "?",
		"why_expect": "x",
		"check": map[string]any{"metric": "uptime", "unit": "$player", "aura": []any{int64(71)},
			"phase": "in_combat"},
		"pass_if": ">= 0.5", "min_opportunity_s": int64(0), "min_n": int64(0)}
	res := rules.Evaluate(rule, nil, nil, nil, tcCtx())
	assertEqual(t, "INCONCLUSIVE", res.Verdict)
}

func TestScoredWindow_CountReportsMinNGuard(t *testing.T) {
	run := t.TempDir()
	rule := map[string]any{"id": "R", "desc": "x", "source": "s", "expect_today": "?",
		"why_expect": "x", "min_n": int64(5),
		"check": map[string]any{"metric": "ratio", "num": map[string]any{"kind": "cast"},
			"den": map[string]any{"kind": "cast"}},
		"pass_if": ">= 0.5", "min_opportunity_s": int64(0)}
	trial := tcTrial()
	trial["rules"] = []any{rule}
	var samples []any
	for i := 0; i < 200; i++ {
		samples = append(samples, tcSample(float64(i), tcPlayer, 500, 1000, true, nil))
	}
	rep := tcWriteRep(t, run, pyjson.Str(trial["id"]), "rep1",
		map[string]any{"ok": true, "guids": map[string]any{"player": tcPlayer, "bot": tcBot}},
		samples, nil, []any{tcCast(50.0, 80.0, "bot", "player", 6065)})
	writeJSON(t, filepath.Join(run, "RUN.json"), map[string]any{"critical_health": int64(20),
		"low_health": int64(50), "medium_health": int64(70), "trials": []any{trial["id"]}})
	out := score.ScoreRep(rep, trial, nil)
	row := tcRow(t, out, 0)
	assertEqual(t, "INCONCLUSIVE", row["verdict"])
	if !strings.Contains(pyjson.Str(row["guard"]), "min_n") {
		t.Fatalf("row = %v", row)
	}
}

// --- Card quality -------------------------------------------------------------------

func TestCardQuality_CutShortReasonAndDropped(t *testing.T) {
	run := t.TempDir()
	repdir := filepath.Join(run, "t", "rep1")
	if err := os.MkdirAll(repdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(repdir, "setup.json"), map[string]any{"ok": true, "checks": []any{}})
	writeJSON(t, filepath.Join(repdir, "verdicts.json"), map[string]any{"trial": "t", "rep": "rep1",
		"ok_setup": true, "scored_s": 5.0, "excluded_s": 3.0,
		"invalid_reason": "cut short (5 s, ended: wiped)", "rules": []any{}})
	writeJSON(t, filepath.Join(repdir, "events_summary.json"), map[string]any{
		"dropped_kinds":  map[string]any{"session": int64(2)},
		"excluded_spans": []any{map[string]any{"from": int64(1), "to": int64(4), "why": "floor"}}})
	writeJSONL(t, filepath.Join(repdir, "events.jsonl"), nil)
	writeJSON(t, filepath.Join(run, "RUN.json"), map[string]any{"label": "x", "tb_version": "v"})
	if _, err := report.WriteCard(run, []map[string]any{{"trial": "t", "verdicts": []any{}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(run, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"events_dropped=[session=2]", "excluded=3 s",
		"cut-short: cut short (5 s, ended: wiped)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}

// --- CLI selectors (the scenario part; argparse cases are not ported) ---------------

func TestCliSelectors_ExactOrGlob(t *testing.T) {
	trials := []map[string]any{{"id": "priest-holy-x"}, {"id": "priest-holy-y"}}
	entries := scenario.Entries(trials, []string{"px", "py"})
	hits, err := scenario.ResolveSelectors([]string{"priest-holy-x"}, entries, "")
	if err != nil || len(hits) != 1 || hits[0] != "px" {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	if _, err := scenario.ResolveSelectors([]string{"priest-holy"}, entries, ""); err == nil {
		t.Fatal("bare prefix must match nothing")
	}
	hits, err = scenario.ResolveSelectors([]string{"priest-*"}, entries, "")
	assertEqual(t, []string{"px", "py"}, hits)
	if err != nil {
		t.Fatal(err)
	}
	hits, err = scenario.ResolveSelectors([]string{"priest-holy-x*"}, entries, "")
	assertEqual(t, []string{"px"}, hits)
	if err != nil {
		t.Fatal(err)
	}
}
