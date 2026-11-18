package variants

// Translated from tests/test_extra_bots.py, scoring half only: the `extra_bots` schema, the
// `$extraN` / `$extras` aliases in the rule engine, and the extras' labels in normalised
// events. The live-runner half of that file (ScriptAgent, setup/teardown/gm_guard/lease) is
// not ported (see the report).

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

const (
	xbtPlayer = "0x0000000000000001"
	xbtHire   = "0x0000000000000002" // the main bot (the Holy priest)
	xbtExtra1 = "0x0000000000000003"
	xbtExtra2 = "0x0000000000000004"

	xbtTwoTank = "healers/priest-holy-pws-def-arms-2tank.json"
)

// xbtTwoTankTrial is Python two_tank_trial(): a deep copy of the shipped 2-tank trial.
func xbtTwoTankTrial(t *testing.T) scenario.Trial {
	t.Helper()
	trial, err := scenario.LoadTrial(xbtTwoTank)
	if err != nil {
		t.Fatalf("load %s: %v", xbtTwoTank, err)
	}
	return trial
}

func xbtLoad(t *testing.T, tid string) scenario.Trial {
	t.Helper()
	trial, err := scenario.LoadTrial("healers/" + tid + ".json")
	if err != nil {
		t.Fatalf("load %s: %v", tid, err)
	}
	return trial
}

// xbtFirstExtra is Python trial["extra_bots"][0].
func xbtFirstExtra(t *testing.T, trial scenario.Trial) map[string]any {
	t.Helper()
	extras := list(t, trial["extra_bots"])
	if len(extras) == 0 {
		t.Fatal("the trial has no extra_bots")
	}
	return obj(t, extras[0])
}

// xbtErrs is SchemaTest.errs: the base trial with its one extra replaced by a copy carrying
// the overrides.
func xbtErrs(t *testing.T, over map[string]any) []string {
	t.Helper()
	trial := xbtTwoTankTrial(t)
	extra := cloneRow(xbtFirstExtra(t, trial))
	for k, v := range over {
		extra[k] = v
	}
	trial["extra_bots"] = []any{extra}
	return scenario.Validate(trial)
}

func TestSchemaTest_Both2tankTrialsValidateAndCarryThePaladin(t *testing.T) {
	wantExtra := map[string]any{
		"class": "paladin", "spec": "protection", "role": "tank", "level": int64(40), "items": []any{},
	}
	for _, tid := range []string{"priest-holy-pws-def-arms-2tank", "priest-holy-pws-bear-bal-2tank"} {
		trial := xbtLoad(t, tid)
		vrtNoErrs(t, scenario.Validate(trial))
		assertEqual(t, []any{wantExtra}, trial["extra_bots"])
		checks := list(t, trial["setup_checks"])
		for _, cid := range scenario.ExtraBotChecks() {
			found := false
			for _, have := range checks {
				if pyjson.Str(have) == cid {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: setup_checks misses %s", tid, cid)
			}
		}
	}
}

func TestSchemaTest_AcceptsUpToThreeAndItems(t *testing.T) {
	trial := xbtTwoTankTrial(t)
	one := xbtFirstExtra(t, trial)
	third := cloneRow(one)
	third["items"] = []any{map[string]any{"name": "Sweet Nectar", "count": int64(5)}}
	trial["extra_bots"] = []any{cloneRow(one), cloneRow(one), third}
	vrtNoErrs(t, scenario.Validate(trial))
}

func TestSchemaTest_RejectsFourExtraBots(t *testing.T) {
	trial := xbtTwoTankTrial(t)
	one := xbtFirstExtra(t, trial)
	trial["extra_bots"] = []any{cloneRow(one), cloneRow(one), cloneRow(one), cloneRow(one)}
	vrtHas(t, scenario.Validate(trial), "at most 3")
}

func TestSchemaTest_RejectsUnknownAndBadFields(t *testing.T) {
	vrtNoErrs(t, xbtErrs(t, map[string]any{"gear": "blue"}))
	vrtHas(t, xbtErrs(t, map[string]any{"gear": int64(3)}), "bad extra_bots[1] gear")
	vrtHas(t, xbtErrs(t, map[string]any{"spec": "nonsense"}), "bad extra_bots[1] spec")
	vrtHas(t, xbtErrs(t, map[string]any{"role": "boss"}), "bad extra_bots[1] role")
	vrtHas(t, xbtErrs(t, map[string]any{"level": int64(61)}), "level must be")
	vrtHas(t, xbtErrs(t, map[string]any{"level": "40"}), "level must be")
	vrtHas(t, xbtErrs(t, map[string]any{"class": "Pal Adin"}), "bad extra_bots[1] class")
	vrtHas(t, xbtErrs(t, map[string]any{"items": []any{map[string]any{"name": "x", "count": int64(99)}}}), "item")
	vrtHas(t, xbtErrs(t, map[string]any{"items": []any{map[string]any{"name": "x", "extra": int64(1)}}}), "item")
}

func TestSchemaTest_RejectsNonListEmptyAndNonObject(t *testing.T) {
	badValues := []any{
		map[string]any{"class": "paladin"},
		[]any{},
		[]any{"paladin"},
		"paladin",
	}
	for _, bad := range badValues {
		trial := xbtTwoTankTrial(t)
		trial["extra_bots"] = bad
		if errs := scenario.Validate(trial); len(errs) == 0 {
			t.Fatalf("extra_bots = %v validated", bad)
		}
	}
}

func TestSchemaTest_MissingFieldIsRejected(t *testing.T) {
	trial := xbtTwoTankTrial(t)
	delete(xbtFirstExtra(t, trial), "spec")
	vrtHas(t, scenario.Validate(trial), "missing spec")
}

func TestSchemaTest_ExtraBotsNeedsItsThreeSetupChecksAndOnlyIt(t *testing.T) {
	trial := xbtTwoTankTrial(t)
	var kept []any
	for _, cid := range list(t, trial["setup_checks"]) {
		if pyjson.Str(cid) != "extra_bots_role" {
			kept = append(kept, cid)
		}
	}
	trial["setup_checks"] = kept
	vrtHas(t, scenario.Validate(trial), "needs setup check 'extra_bots_role'")

	solo := xbtLoad(t, "priest-holy-pws-def-arms")
	checks := append([]any{}, list(t, solo["setup_checks"])...)
	solo["setup_checks"] = append(checks, "extra_bots_present")
	vrtHas(t, scenario.Validate(solo), "needs extra_bots")
}

func TestSchemaTest_SoloTrialsStillValidateUnchanged(t *testing.T) {
	for _, tid := range []string{"priest-holy-pws-def-arms", "priest-holy-pws-bear-bal"} {
		trial := xbtLoad(t, tid)
		vrtNoErrs(t, scenario.Validate(trial))
		if _, present := trial["extra_bots"]; present {
			t.Fatalf("%s carries extra_bots", tid)
		}
	}
}

func TestSchemaTest_AliasesExtra1To3AndExtras(t *testing.T) {
	trial := xbtTwoTankTrial(t)
	rule := obj(t, list(t, trial["rules"])[0])
	where := obj(t, obj(t, rule["check"])["where"])
	where["src"] = []any{"$bot", "$extra1"}
	vrtNoErrs(t, scenario.Validate(trial))
	where["dst"] = "$extras"
	vrtNoErrs(t, scenario.Validate(trial))
	where["dst"] = "$extra4"
	vrtHas(t, scenario.Validate(trial), "bad dst alias '$extra4'")
}

func TestSchemaTest_AliasWithoutABotBehindItIsRejected(t *testing.T) {
	trial := xbtTwoTankTrial(t)
	rule := obj(t, list(t, trial["rules"])[0])
	where := obj(t, obj(t, rule["check"])["where"])
	where["src"] = "$extra2"
	vrtHas(t, scenario.Validate(trial), "'$extra2' has no extra_bots")

	solo := xbtLoad(t, "priest-holy-pws-def-arms")
	soloRule := obj(t, list(t, solo["rules"])[0])
	soloWhere := obj(t, obj(t, soloRule["check"])["where"])
	soloWhere["src"] = "$extras"
	vrtHas(t, scenario.Validate(solo), "'$extras' has no extra_bots")
}

// --- aliases in the rule engine ---------------------------------------------

// xbtScore scores one synthetic repeat with a count rule on `where` and returns the first
// rule's verdict doc (Python AliasScoringTest.score).
func xbtScore(t *testing.T, where map[string]any, eventRows []any, extras ...string) map[string]any {
	t.Helper()
	if len(extras) == 0 {
		extras = []string{xbtExtra1, xbtExtra2}
	}
	trial := xbtTwoTankTrial(t)
	one := xbtFirstExtra(t, trial)
	n := len(extras)
	if n < 2 {
		n = 2
	}
	extraBots := make([]any, 0, n)
	for range n {
		extraBots = append(extraBots, cloneRow(one))
	}
	trial["extra_bots"] = extraBots
	ruleWhere := cloneRow(where)
	ruleWhere["kind"] = "cast"
	trial["rules"] = []any{map[string]any{
		"id": "X", "desc": "d", "source": "s", "expect_today": "PASS",
		"check":   map[string]any{"metric": "count", "where": ruleWhere},
		"pass_if": "== 0",
	}}
	trial["opportunity"] = map[string]any{"unit": "$extra1", "hp_lt": int64(90), "in_combat": true}
	vrtNoErrs(t, scenario.Validate(trial))

	tmp := t.TempDir()
	rep := filepath.Join(tmp, "run", "t", "rep1")
	extrasAny := make([]any, 0, len(extras))
	for _, guid := range extras {
		extrasAny = append(extrasAny, guid)
	}
	writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{
		"ok": true,
		"guids": map[string]any{"player": xbtPlayer, "bot": xbtHire, "extras": extrasAny,
			"mobs": []any{}},
		"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)},
	})
	var samples []any
	guids := append([]string{xbtPlayer, xbtHire}, extras...)
	for ti := range 201 {
		for _, guid := range guids {
			samples = append(samples, map[string]any{
				"t": float64(ti), "wall": nil, "unit": "x", "guid": guid, "hp": int64(600),
				"max_hp": int64(1000), "power": int64(1), "max_power": int64(1), "auras": []any{},
				"casting": nil, "in_combat": true, "target": "", "x": 0.0, "y": 0.0, "z": 0.0,
			})
		}
	}
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), samples)
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), eventRows)
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), []any{
		map[string]any{"kind": "fight_start", "t": int64(0)},
		map[string]any{"kind": "fight_end", "t": int64(200), "ended": "duration"},
	})
	out := score.ScoreRep(rep, trial, nil)
	return obj(t, list(t, out["rules"])[0])
}

// xbtCast is AliasScoringTest.cast.
func xbtCast(src, dst string, at float64) map[string]any {
	return map[string]any{
		"t": at, "wall": nil, "kind": "cast", "src": "x", "src_guid": src, "dst": "y",
		"dst_guid": dst, "src_unit": "", "dst_unit": "", "spell_id": int64(6065),
		"spell": "Power Word: Shield", "amount": nil, "dst_hp_pct": int64(60),
		"src_role": "other", "dst_role": "player", "origin": "ta",
	}
}

func TestAliasScoringTest_Extra1MatchesOnlyTheFirstExtra(t *testing.T) {
	eventRows := []any{
		xbtCast(xbtExtra1, xbtPlayer, 50),
		xbtCast(xbtExtra2, xbtPlayer, 60),
		xbtCast(xbtHire, xbtPlayer, 70),
	}
	assertEqual(t, int64(1), xbtScore(t, map[string]any{"src": "$extra1"}, eventRows)["value"])
	assertEqual(t, int64(1), xbtScore(t, map[string]any{"src": "$extra2"}, eventRows)["value"])
}

func TestAliasScoringTest_ExtrasMatchesEveryExtraAndNotTheMainBot(t *testing.T) {
	eventRows := []any{
		xbtCast(xbtExtra1, xbtPlayer, 50),
		xbtCast(xbtExtra2, xbtPlayer, 60),
		xbtCast(xbtHire, xbtPlayer, 70),
	}
	assertEqual(t, int64(2), xbtScore(t, map[string]any{"src": "$extras"}, eventRows)["value"])
	assertEqual(t, int64(1), xbtScore(t, map[string]any{"src": "$bot"}, eventRows)["value"])
}

func TestAliasScoringTest_MissingExtraMatchesNothing(t *testing.T) {
	eventRows := []any{xbtCast(xbtExtra1, xbtPlayer, 50)}
	got := xbtScore(t, map[string]any{"src": "$extra2"}, eventRows, xbtExtra1)
	assertEqual(t, int64(0), got["value"])
}

// --- events labelling -------------------------------------------------------

func TestNormaliseTest_EventsLabelTheExtras(t *testing.T) {
	tmp := t.TempDir()
	writeJSON(t, filepath.Join(tmp, "setup.json"), map[string]any{
		"ok": true,
		"guids": map[string]any{
			"player": xbtPlayer, "bot": xbtHire, "pet": nil,
			"extras": []any{xbtExtra1, xbtExtra2}, "mobs": []any{}, "marked": nil,
		},
	})
	writeJSONL(t, filepath.Join(tmp, "marks.jsonl"), []any{
		map[string]any{"kind": "fight_start", "wall": "2026-01-01T00:00:00+00:00"},
	})
	if err := os.WriteFile(filepath.Join(tmp, "samples.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var raw []any
	for _, pair := range [][2]any{{int64(1), xbtExtra1}, {int64(2), xbtExtra2}, {int64(3), xbtHire}} {
		raw = append(raw, map[string]any{
			"seq": pair[0], "time": fmt.Sprintf("2026-01-01T00:00:0%s+00:00", pyjson.Str(pair[0])),
			"kind":     "spell_go",
			"source":   map[string]any{"name": "b", "guid": pair[1]},
			"target":   map[string]any{"name": "p", "guid": xbtPlayer},
			"spell_id": int64(6065),
		})
	}
	writeJSONL(t, filepath.Join(tmp, "raw-ta.jsonl"), raw)

	trial := xbtLoad(t, "priest-holy-pws-def-arms-2tank")
	rows, err := events.Normalise(tmp, trial)
	if err != nil {
		t.Fatal(err)
	}
	var srcUnits, srcRoles []any
	for _, row := range rows {
		if pyjson.Str(row["kind"]) != "cast" {
			continue
		}
		srcUnits = append(srcUnits, row["src_unit"])
		srcRoles = append(srcRoles, row["src_role"])
	}
	assertEqual(t, []any{"$extra1", "$extra2", "$bot"}, srcUnits)
	assertEqual(t, []any{"extra", "extra", "bot"}, srcRoles)
}
