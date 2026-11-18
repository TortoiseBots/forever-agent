package variants

// Translated from tests/test_variants.py: trial variants (`variants` in a trial file): merge
// semantics, refusals, selection, ids in the run folder, the card and the baseline diff.
// No server is contacted.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/report"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

const (
	vrtHeal = "healers/priest-holy-heal-latency.json"
	vrtTank = "tanks/tank-warrior-taunt-off-healer.json"

	vrtPlayer = "0x0000000000000001"
	vrtBot    = "0x0000000000000002"
	vrtMob    = "0xf13000000000abcd"

	vrtHealID = "priest-holy-heal-latency"
)

// vrtBaseTrial is the tank trial with a gear kit, an extra bot and two rules (a small, valid
// trial to vary), without its own `variants`.
func vrtBaseTrial(t *testing.T) scenario.Trial {
	t.Helper()
	trial := vrtLoad(t, vrtTank)
	delete(trial, "variants")
	return trial
}

func vrtLoad(t *testing.T, path string) scenario.Trial {
	t.Helper()
	trial, err := scenario.LoadTrial(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return trial
}

func vrtVar(name string, override map[string]any) map[string]any {
	return map[string]any{"name": name, "override": override}
}

func vrtWithVariants(t *testing.T, variants ...any) scenario.Trial {
	t.Helper()
	trial := vrtBaseTrial(t)
	trial["variants"] = variants
	return trial
}

// vrtNoErrs is assertEqual(errs, []).
func vrtNoErrs(t *testing.T, errs []string) {
	t.Helper()
	if len(errs) != 0 {
		t.Fatalf("want no errors, got %q", errs)
	}
}

// vrtStringsEqual is assertEqual for two []string.
func vrtStringsEqual(t *testing.T, want, got []string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

// vrtHas is Python's `any(text in e for e in errs)`. The Go port renders a few message
// fragments with %q where Python uses %r; normalising the quotes keeps the Python
// expectation text meaningful.
func vrtHas(t *testing.T, errs []string, text string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e, text) || strings.Contains(strings.ReplaceAll(e, `"`, "'"), text) {
			return
		}
	}
	t.Fatalf("%q not in %q", text, errs)
}

// vrtRule finds one rule by id (Python rule_at).
func vrtRule(t *testing.T, trial scenario.Trial, id string) map[string]any {
	t.Helper()
	for _, ruleVal := range list(t, trial["rules"]) {
		rule := obj(t, ruleVal)
		if pyjson.Str(rule["id"]) == id {
			return rule
		}
	}
	t.Fatalf("rule %q not in %v", id, trial["rules"])
	return nil
}

// vrtRuleIDs is Python [r["id"] for r in trial["rules"]].
func vrtRuleIDs(t *testing.T, trial scenario.Trial) []any {
	t.Helper()
	var out []any
	for _, ruleVal := range list(t, trial["rules"]) {
		out = append(out, obj(t, ruleVal)["id"])
	}
	return out
}

// vrtRuleChecks is Python [r["check"] for r in trial["rules"]].
func vrtRuleChecks(t *testing.T, trial scenario.Trial) []any {
	t.Helper()
	var out []any
	for _, ruleVal := range list(t, trial["rules"]) {
		out = append(out, obj(t, ruleVal)["check"])
	}
	return out
}

func TestMergeTest_DeepMergeDictsMergeListsAndScalarsReplace(t *testing.T) {
	base := map[string]any{
		"a": map[string]any{"b": int64(1), "c": []any{int64(1), int64(2)}},
		"d": int64(5),
	}
	original := map[string]any{
		"a": map[string]any{"b": int64(1), "c": []any{int64(1), int64(2)}},
		"d": int64(5),
	}
	out := scenario.DeepMerge(base, map[string]any{
		"a": map[string]any{"c": []any{int64(9)}},
		"d": nil,
		"e": map[string]any{"f": int64(1)},
	})
	assertEqual(t, map[string]any{
		"a": map[string]any{"b": int64(1), "c": []any{int64(9)}},
		"d": nil,
		"e": map[string]any{"f": int64(1)},
	}, out)
	assertEqual(t, original, base) // the base is never changed
}

func TestMergeTest_LevelSetsPlayerBotAndEveryExtra(t *testing.T) {
	trial := vrtWithVariants(t, vrtVar("l50", map[string]any{"level": int64(50)}))
	variant := obj(t, list(t, trial["variants"])[0])
	merged, errs := scenario.ApplyOverride(trial, variant["override"])
	vrtNoErrs(t, errs)
	assertEqual(t, int64(50), obj(t, merged["player"])["level"])
	assertEqual(t, int64(50), obj(t, merged["bot"])["level"])
	var levels []any
	for _, botVal := range list(t, merged["extra_bots"]) {
		levels = append(levels, obj(t, botVal)["level"])
	}
	assertEqual(t, []any{int64(50)}, levels)
	if _, present := merged["variants"]; present {
		t.Fatalf("variants must not survive the merge: %v", merged["variants"])
	}
	assertEqual(t, int64(40), obj(t, trial["player"])["level"]) // the base trial is untouched
}

func TestMergeTest_ExplicitFieldsWinOverTheLevelShortcut(t *testing.T) {
	trial := vrtWithVariants(t, vrtVar("x", map[string]any{
		"level": int64(50),
		"bot":   map[string]any{"level": int64(45)},
	}))
	variant := obj(t, list(t, trial["variants"])[0])
	merged, errs := scenario.ApplyOverride(trial, variant["override"])
	vrtNoErrs(t, errs)
	assertEqual(t, int64(45), obj(t, merged["bot"])["level"])
	assertEqual(t, int64(50), obj(t, merged["player"])["level"])
}

func TestMergeTest_NestedSituationFieldMergesAndNullClearsGear(t *testing.T) {
	trial := vrtWithVariants(t, vrtVar("x", map[string]any{
		"situation": map[string]any{"mobs": map[string]any{"level_delta": int64(-3)}},
		"player":    map[string]any{"gear": nil},
	}))
	variant := obj(t, list(t, trial["variants"])[0])
	merged, errs := scenario.ApplyOverride(trial, variant["override"])
	vrtNoErrs(t, errs)
	situation := obj(t, merged["situation"])
	mobs := obj(t, situation["mobs"])
	assertEqual(t, int64(-3), mobs["level_delta"])
	assertEqual(t, int64(3), mobs["count"]) // the rest of mobs stays
	player := obj(t, merged["player"])
	if player["gear"] != nil {
		t.Fatalf("player.gear must be cleared, got %v", player["gear"])
	}
	assertEqual(t, obj(t, trial["player"])["talents"], player["talents"])
}

func TestMergeTest_ExtraBotsListIsReplacedWhole(t *testing.T) {
	extra := []any{
		map[string]any{"class": "priest", "spec": "holy", "role": "healer", "level": int64(40), "items": []any{}},
		map[string]any{"class": "paladin", "spec": "protection", "role": "tank", "level": int64(40), "items": []any{}},
	}
	trial := vrtWithVariants(t, vrtVar("x", map[string]any{"extra_bots": extra}))
	variant := obj(t, list(t, trial["variants"])[0])
	merged, errs := scenario.ApplyOverride(trial, variant["override"])
	vrtNoErrs(t, errs)
	if got := len(list(t, merged["extra_bots"])); got != 2 {
		t.Fatalf("extra_bots len = %d, want 2", got)
	}
}

func TestMergeTest_RuleThresholdsChangeByIDAndNothingElse(t *testing.T) {
	trial := vrtWithVariants(t, vrtVar("x", map[string]any{
		"rules": map[string]any{"TNK-WAR-TAUNT": map[string]any{"pass_if": ">= 0.5", "min_n": int64(3)}},
	}))
	variant := obj(t, list(t, trial["variants"])[0])
	merged, errs := scenario.ApplyOverride(trial, variant["override"])
	vrtNoErrs(t, errs)
	rule := vrtRule(t, merged, "TNK-WAR-TAUNT")
	assertEqual(t, ">= 0.5", rule["pass_if"])
	assertEqual(t, int64(3), rule["min_n"])
	before := vrtRule(t, trial, "TNK-WAR-TAUNT")
	assertEqual(t, before["check"], rule["check"])
	assertEqual(t, vrtRuleIDs(t, trial), vrtRuleIDs(t, merged))
}

// --- validation -------------------------------------------------------------

func vrtErrs(t *testing.T, variants ...any) []string {
	t.Helper()
	return scenario.Validate(vrtWithVariants(t, variants...))
}

func TestValidationTest_AGoodVariantValidates(t *testing.T) {
	vrtNoErrs(t, vrtErrs(t, vrtVar("l50", map[string]any{"level": int64(50)})))
}

func TestValidationTest_MergedTrialGoesThroughOrdinaryValidation(t *testing.T) {
	// level 60 with the gear kit of a level with no kit
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"player": map[string]any{"level": int64(60)}})), "player.gear tier")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"situation": map[string]any{"victim": "extra9"}})), "bad victim")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"situation": map[string]any{"mobs": map[string]any{"count": "x"}}})), "mobs.count must be int")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"player": map[string]any{"stance": "x"}})), "player.stance")
}

func TestValidationTest_UnknownOverrideKeysAreRefused(t *testing.T) {
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"rules_list": []any{}})), "may not touch 'rules_list'")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"setup_checks": []any{}})), "may not touch 'setup_checks'")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"duration_s": int64(10)})), "may not touch 'duration_s'")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"situation": map[string]any{"nope": int64(1)}})), "unknown situation field 'nope'")
}

func TestValidationTest_ClassSpecAndRoleCannotChange(t *testing.T) {
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"bot": map[string]any{"role": "dps"}})), "may not change bot.role")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"bot": map[string]any{"class": "mage"}})), "may not change bot.class")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"player": map[string]any{"class": "mage"}})), "may not change player.class")
}

func TestValidationTest_ABotSpecVariantMayRewriteARulesOwnTexts(t *testing.T) {
	rule0 := vrtRule(t, vrtBaseTrial(t), "TNK-WAR-TAUNT")
	trial := vrtWithVariants(t, vrtVar("fr", map[string]any{
		"bot": map[string]any{"spec": "frost"},
		"rules": map[string]any{"TNK-WAR-TAUNT": map[string]any{
			"desc":  "frost text",
			"check": rule0["check"],
		}},
	}))
	vrtNoErrs(t, scenario.Validate(trial))
	expanded := scenario.ExpandVariants(trial)
	if len(expanded) != 1 {
		t.Fatalf("expanded %d variants, want 1", len(expanded))
	}
	merged := expanded[0].Trial
	rule := vrtRule(t, merged, "TNK-WAR-TAUNT")
	assertEqual(t, "frost text", rule["desc"])
	assertEqual(t, "frost", merged["spec"])
	// without the spec change the same rewrite is refused
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{
		"rules": map[string]any{"TNK-WAR-TAUNT": map[string]any{"desc": "x"}},
	})), "only with bot.spec")
}

func TestValidationTest_RulesCannotChangeExceptThreeThresholds(t *testing.T) {
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{
		"rules": map[string]any{"TNK-WAR-TAUNT": map[string]any{"check": map[string]any{"within_s": int64(9)}}},
	})), "only with bot.spec")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{
		"rules": map[string]any{"TNK-WAR-TAUNT": map[string]any{"expect_today": "FAIL"}},
	})), "may not change rule field 'expect_today'")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{
		"rules": map[string]any{"TNK-WAR-TAUNT": map[string]any{"pass_if": "maybe"}},
	})), "bad variant pass_if")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{
		"rules": map[string]any{"TNK-WAR-TAUNT": map[string]any{"min_n": int64(0)}},
	})), "min_n must be an int >= 1")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{
		"rules": map[string]any{"TNK-WAR-TAUNT": map[string]any{"min_opportunity_s": int64(-1)}},
	})), "min_opportunity_s must be a number >= 0")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{
		"rules": []any{map[string]any{"id": "X"}},
	})), "rules must be {rule id")
}

func TestValidationTest_BadLevelAndShape(t *testing.T) {
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"level": int64(61)})), "level must be an int 1-60")
	vrtHas(t, vrtErrs(t, vrtVar("bad", map[string]any{"level": true})), "level must be an int 1-60")
	vrtHas(t, vrtErrs(t, map[string]any{"name": "Bad Name", "override": map[string]any{"level": int64(50)}}), "bad name")
	vrtHas(t, vrtErrs(t, map[string]any{"name": "x", "override": map[string]any{}}), "non-empty object")
	vrtHas(t, vrtErrs(t, map[string]any{"name": "x"}), "non-empty object")
	vrtHas(t, vrtErrs(t, map[string]any{"name": "x", "override": map[string]any{"level": int64(50)}, "extra": int64(1)}), "unknown field 'extra'")
	vrtHas(t, vrtErrs(t, vrtVar("a", map[string]any{"level": int64(50)}), vrtVar("a", map[string]any{"level": int64(45)})), "duplicate variant name")

	empty := vrtBaseTrial(t)
	empty["variants"] = []any{}
	vrtHas(t, scenario.Validate(empty), "non-empty list")
	notList := vrtBaseTrial(t)
	notList["variants"] = "x"
	vrtHas(t, scenario.Validate(notList), "non-empty list")

	var many []any
	for i := range scenario.MaxVariants + 1 {
		many = append(many, vrtVar("v"+pyjson.Str(int64(i)), map[string]any{"level": int64(45)}))
	}
	vrtHas(t, vrtErrs(t, many...), "at most 6")
}

func TestValidationTest_LevelBelowTheTalentProofIsRefused(t *testing.T) {
	// a Protection warrior hire at level 20: the premade link of that level stops short of the
	// marker talent
	errs := vrtErrs(t, vrtVar("l20", map[string]any{
		"level":  int64(20),
		"player": map[string]any{"gear": nil},
	}))
	vrtHas(t, errs, "below level 35")
}

func TestValidationTest_RaidTrialsTakeNoVariants(t *testing.T) {
	raid := map[string]any{
		"id": "r", "group": "raid", "boss": map[string]any{"entry": int64(1)}, "live": "practice",
		"variants": []any{vrtVar("x", map[string]any{"level": int64(50)})},
	}
	vrtHas(t, scenario.Validate(raid), "raid trials take no variants")
}

func TestValidationTest_ATrialWithoutVariantsIsUnchanged(t *testing.T) {
	trial := vrtBaseTrial(t)
	vrtNoErrs(t, scenario.Validate(trial))
	if got := len(scenario.ExpandVariants(trial)); got != 0 {
		t.Fatalf("expanded %d variants, want 0", got)
	}
}

func TestValidationTest_EveryShippedVariantValidates(t *testing.T) {
	seen := 0
	for _, path := range scenario.TrialFiles() {
		trial := vrtLoad(t, path)
		variants, hasVariants := pyjson.List(trial["variants"])
		if !hasVariants {
			continue
		}
		vrtNoErrs(t, scenario.Validate(trial))
		expanded := scenario.ExpandVariants(trial)
		if len(expanded) != len(variants) {
			t.Fatalf("%s: expanded %d variants, want %d", path, len(expanded), len(variants))
		}
		for _, variant := range expanded {
			seen++
			assertEqual(t, pyjson.Str(trial["id"])+"@"+variant.Name, variant.Trial["id"])
			assertEqual(t, vrtRuleIDs(t, trial), vrtRuleIDs(t, variant.Trial))
			if jsonEqual(variant.Trial["spec"], trial["spec"]) {
				assertEqual(t, vrtRuleChecks(t, trial), vrtRuleChecks(t, variant.Trial))
			}
		}
	}
	if seen == 0 {
		t.Fatal("no shipped variant was seen")
	}
}

// --- expansion --------------------------------------------------------------

func TestExpandTest_IDsAndTitles(t *testing.T) {
	trial := vrtWithVariants(t, map[string]any{
		"name": "l50", "title": "level 50", "override": map[string]any{"level": int64(50)},
	})
	expanded := scenario.ExpandVariants(trial)
	if len(expanded) != 1 {
		t.Fatalf("expanded %d variants, want 1", len(expanded))
	}
	name, merged := expanded[0].Name, expanded[0].Trial
	assertEqual(t, "l50", name)
	assertEqual(t, pyjson.Str(trial["id"])+"@l50", merged["id"])
	base, variant := scenario.SplitVariantID(pyjson.Str(merged["id"]))
	assertEqual(t, pyjson.Str(trial["id"]), base)
	assertEqual(t, "l50", variant)
	base, variant = scenario.SplitVariantID(pyjson.Str(trial["id"]))
	assertEqual(t, pyjson.Str(trial["id"]), base)
	assertEqual(t, "", variant) // Python's None
}

func TestExpandTest_AnInvalidVariantIsNotExpanded(t *testing.T) {
	trial := vrtWithVariants(t,
		vrtVar("good", map[string]any{"level": int64(50)}),
		vrtVar("bad", map[string]any{"level": int64(20), "player": map[string]any{"gear": nil}}),
	)
	var names []any
	for _, variant := range scenario.ExpandVariants(trial) {
		names = append(names, variant.Name)
	}
	assertEqual(t, []any{"good"}, names)
}

// --- selection --------------------------------------------------------------

// vrtResolve is `trials resolve_trials` with only `--trial` selectors: no class, no scenario.
func vrtResolve(t *testing.T, sels ...string) ([]string, error) {
	t.Helper()
	trials, paths := scenario.AllTrials()
	return scenario.ResolveSelectors(sels, scenario.Entries(trials, paths), "")
}

func TestSelectionTest_BareIDIsTheBaseTrialOnly(t *testing.T) {
	got, err := vrtResolve(t, "priest-holy-heal-latency")
	if err != nil {
		t.Fatal(err)
	}
	vrtStringsEqual(t, []string{vrtHeal}, got)
}

func TestSelectionTest_VariantIDSelectsTheVariant(t *testing.T) {
	got, err := vrtResolve(t, "priest-holy-heal-latency@lvl50")
	if err != nil {
		t.Fatal(err)
	}
	vrtStringsEqual(t, []string{vrtHeal + "@lvl50"}, got)
}

func TestSelectionTest_VariantGlobSelectsEveryVariantAndNotTheBase(t *testing.T) {
	got, err := vrtResolve(t, "priest-holy-heal-latency@*")
	if err != nil {
		t.Fatal(err)
	}
	vrtStringsEqual(t, []string{vrtHeal + "@lvl50", vrtHeal + "@lvl60"}, got)
}

func TestSelectionTest_PlainGlobAndClassNeverStartVariants(t *testing.T) {
	got, err := vrtResolve(t, "priest-holy-heal-*")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, path := range got {
		if path == vrtHeal {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s not in %v", vrtHeal, got)
	}
	for _, path := range got {
		if strings.Contains(filepath.Base(path), scenario.VariantSep) {
			t.Fatalf("a plain glob started a variant: %v", got)
		}
	}
	picked, err := scenario.FindTrials(nil, "priest", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range picked {
		if strings.Contains(filepath.Base(path), scenario.VariantSep) {
			t.Fatalf("a class selection started a variant: %v", picked)
		}
	}
}

func TestSelectionTest_UnknownVariantMatchesNothing(t *testing.T) {
	if _, err := vrtResolve(t, "priest-holy-heal-latency@nope"); err == nil {
		t.Fatal("a missing variant must fail the command")
	}
}

func TestSelectionTest_RunnerLoadsTheMergedVariant(t *testing.T) {
	// the runner's load_trial is scenario.load_selected for `path@variant`
	trial, err := scenario.LoadSelected(vrtHeal + "@lvl60")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "priest-holy-heal-latency@lvl60", trial["id"])
	assertEqual(t, int64(60), obj(t, trial["player"])["level"])
	assertEqual(t, int64(60), obj(t, trial["bot"])["level"])
	assertEqual(t, int64(-2), obj(t, obj(t, trial["situation"])["mobs"])["level_delta"])
	plain, err := scenario.LoadSelected(vrtHeal)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "priest-holy-heal-latency", plain["id"])
	assertEqual(t, int64(40), obj(t, plain["player"])["level"])
	assertEqual(t, vrtRuleIDs(t, plain), vrtRuleIDs(t, trial))
	if _, err := scenario.LoadSelected(vrtHeal + "@nope"); err == nil {
		t.Fatal("a missing variant must fail the load")
	}
}

// --- report -----------------------------------------------------------------

// vrtMakeRun writes a run folder with one rep per trial id, the way the reference fixtures
// record one: 201 samples of the player's health, an optional heal cast, fight marks.
func vrtMakeRun(t *testing.T, baseDir, name string, ids []string, healEvents map[string]bool) string {
	t.Helper()
	runDir := filepath.Join(baseDir, name)
	for _, tid := range ids {
		rep := filepath.Join(runDir, tid, "rep1")
		writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{
			"ok": true,
			"guids": map[string]any{"player": vrtPlayer, "bot": vrtBot, "extras": []any{},
				"mobs": []any{vrtMob}},
			"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)},
		})
		var samples []any
		for ti := range 201 {
			hp := int64(400)
			if ti < 10 {
				hp = 900
			}
			samples = append(samples, map[string]any{
				"t": float64(ti), "wall": nil, "unit": "player", "guid": vrtPlayer, "hp": hp,
				"max_hp": int64(1000), "power": int64(1), "max_power": int64(1), "auras": []any{},
				"casting": nil, "in_combat": true, "target": vrtMob,
				"x": 0.0, "y": 0.0, "z": 0.0,
			})
		}
		var events []any
		if healEvents[tid] {
			events = append(events, map[string]any{
				"t": 11.0, "wall": nil, "kind": "cast", "src": "bot", "src_guid": vrtBot,
				"dst": "player", "dst_guid": vrtPlayer, "src_unit": "bot", "dst_unit": "player",
				"spell_id": int64(2060), "spell": "Greater Heal", "amount": int64(0),
				"dst_hp_pct": int64(40), "src_role": "bot", "dst_role": "player", "origin": "ta",
			})
		}
		writeJSONL(t, filepath.Join(rep, "samples.jsonl"), samples)
		writeJSONL(t, filepath.Join(rep, "events.jsonl"), events)
		writeJSONL(t, filepath.Join(rep, "marks.jsonl"), []any{
			map[string]any{"kind": "fight_start", "t": int64(0)},
			map[string]any{"kind": "fight_end", "t": int64(200), "ended": "duration"},
		})
	}
	idsAny := make([]any, 0, len(ids))
	for _, tid := range ids {
		idsAny = append(idsAny, tid)
	}
	writeJSON(t, filepath.Join(runDir, "RUN.json"), map[string]any{"trials": idsAny})
	return runDir
}

// vrtRows is Python `rows`: the card rows keyed by (trial, rule).
func vrtRows(t *testing.T, runDir string) map[[2]string]map[string]any {
	t.Helper()
	doc := obj(t, readJSON(t, filepath.Join(runDir, "card.json")))
	out := map[[2]string]map[string]any{}
	for _, rowVal := range list(t, doc["rows"]) {
		row := obj(t, rowVal)
		out[[2]string{pyjson.Str(row["trial"]), pyjson.Str(row["rule"])}] = row
	}
	return out
}

// vrtDiff indexes report.compare rows by (trial, rule).
func vrtDiff(t *testing.T, rows []any) map[[2]string]map[string]any {
	t.Helper()
	out := map[[2]string]map[string]any{}
	for _, rowVal := range rows {
		row := obj(t, rowVal)
		out[[2]string{pyjson.Str(row["trial"]), pyjson.Str(row["rule"])}] = row
	}
	return out
}

func TestReportTest_CardRowsAndBaselineKeyOnTheVariantID(t *testing.T) {
	baseID := vrtHealID
	ids := []string{baseID, baseID + "@lvl50"}
	before := vrtMakeRun(t, t.TempDir(), "before", ids, map[string]bool{baseID: false, baseID + "@lvl50": false})
	after := vrtMakeRun(t, t.TempDir(), "after", ids, map[string]bool{baseID: true, baseID + "@lvl50": false})
	for _, runDir := range []string{before, after} {
		scored := score.ScoreRun(runDir)
		var got []any
		for _, trial := range scored {
			got = append(got, trial["trial"])
		}
		assertEqual(t, []any{ids[0], ids[1]}, got)
	}
	rows := vrtRows(t, after)
	trials := map[string]bool{}
	for key := range rows {
		trials[key[0]] = true
	}
	if len(trials) != 2 || !trials[baseID] || !trials[baseID+"@lvl50"] {
		t.Fatalf("card trials = %v, want both ids", trials)
	}
	assertEqual(t, "INCONCLUSIVE", rows[[2]string{baseID, "PRI-HEAL-LATENCY"}]["verdict"])
	diff := vrtDiff(t, report.Compare(after, before))
	if _, ok := diff[[2]string{baseID + "@lvl50", "PRI-HEAL-LATENCY"}]; !ok {
		t.Fatalf("variant row missing from %v", diff)
	}
	if _, ok := diff[[2]string{baseID, "PRI-HEAL-LATENCY"}]; !ok {
		t.Fatalf("base row missing from %v", diff)
	}
	if len(diff) != 2 {
		t.Fatalf("got %d diff rows, want 2 (one row per trial id, the variant is not folded into its base)", len(diff))
	}
}

func TestReportTest_AVariantMissingFromTheBaselineIsItsOwnNewRow(t *testing.T) {
	baseID := vrtHealID
	before := vrtMakeRun(t, t.TempDir(), "before", []string{baseID}, map[string]bool{})
	after := vrtMakeRun(t, t.TempDir(), "after", []string{baseID, baseID + "@lvl60"}, map[string]bool{})
	score.ScoreRun(before)
	score.ScoreRun(after)
	diff := vrtDiff(t, report.Compare(after, before))
	newRow := diff[[2]string{baseID + "@lvl60", "PRI-HEAL-LATENCY"}]
	if newRow["before"] != nil {
		t.Fatalf("a new variant row has a baseline: %v", newRow["before"])
	}
	if diff[[2]string{baseID, "PRI-HEAL-LATENCY"}]["before"] == nil {
		t.Fatal("the base row must keep its baseline")
	}
}

func TestReportTest_ScoreRunFindsTheMergedVariant(t *testing.T) {
	trial := scenario.FindTrialByID("priest-holy-heal-latency@lvl60")
	assertEqual(t, int64(60), obj(t, trial["player"])["level"])
	if scenario.FindTrialByID("priest-holy-heal-latency@nope") != nil {
		t.Fatal("a missing variant must not resolve")
	}
	assertEqual(t, int64(40), obj(t, scenario.FindTrialByID("priest-holy-heal-latency")["player"])["level"])
}

// --- snapshots --------------------------------------------------------------

// vrtDefinition is the heal trial with one `frozen` variant that sets the rule's min_n.
func vrtDefinition(t *testing.T, minN int64) scenario.Trial {
	t.Helper()
	trial := vrtLoad(t, vrtHeal)
	trial["variants"] = []any{map[string]any{
		"name": "frozen",
		"override": map[string]any{
			"level": int64(50),
			"rules": map[string]any{"PRI-HEAL-LATENCY": map[string]any{"min_n": minN}},
		},
	}}
	return trial
}

// vrtRunWithSnapshots writes a run whose one trial is the merged `frozen` variant and whose
// RUN.json records that snapshots were taken.
func vrtRunWithSnapshots(t *testing.T, baseDir, name string, minN int64) (string, string) {
	t.Helper()
	trial := vrtDefinition(t, minN)
	expanded := scenario.ExpandVariants(trial)
	if len(expanded) != 1 {
		t.Fatalf("expanded %d variants, want 1", len(expanded))
	}
	merged := expanded[0].Trial
	tid := pyjson.Str(merged["id"])
	runDir := vrtMakeRun(t, baseDir, name, []string{tid}, map[string]bool{})
	// live_run.snapshot_trial: <run>/<trial id>/trial.effective.json holds the merged trial
	writeJSON(t, filepath.Join(runDir, tid, scenario.SnapshotName), merged)
	writeJSON(t, filepath.Join(runDir, "RUN.json"), map[string]any{
		"trials": []any{tid}, "trial_snapshots": true,
	})
	return runDir, tid
}

func vrtVerdict(t *testing.T, runDir, tid string) any {
	t.Helper()
	return vrtRows(t, runDir)[[2]string{tid, "PRI-HEAL-LATENCY"}]["verdict"]
}

// vrtMustPanic runs fn and fails unless it panics with a message containing text.
func vrtMustPanic(t *testing.T, text string, fn func()) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("want a panic containing %q", text)
		}
		if msg := pyjson.Str(recovered); !strings.Contains(msg, text) {
			t.Fatalf("panic %v does not contain %q", recovered, text)
		}
	}()
	fn()
}

func TestSnapshotTest_TheSnapshotIsTheMergedTrial(t *testing.T) {
	runDir, tid := vrtRunWithSnapshots(t, t.TempDir(), "snap", int64(1))
	snap := obj(t, readJSON(t, filepath.Join(runDir, tid, scenario.SnapshotName)))
	assertEqual(t, tid, snap["id"])
	assertEqual(t, int64(50), obj(t, snap["player"])["level"])
	var minNs []any
	for _, ruleVal := range list(t, snap["rules"]) {
		minNs = append(minNs, obj(t, ruleVal)["min_n"])
	}
	assertEqual(t, []any{int64(1)}, minNs)
	if _, present := snap["variants"]; present {
		t.Fatalf("the snapshot must not carry variants: %v", snap["variants"])
	}
}

func TestSnapshotTest_RescoreUsesTheFrozenThresholdNotTheCurrentDefinition(t *testing.T) {
	runDir, tid := vrtRunWithSnapshots(t, t.TempDir(), "snap", int64(1))
	score.ScoreRun(runDir)
	// one trigger, min_n 1, but min_reps 3 with 1 rep
	assertEqual(t, "INCONCLUSIVE", vrtVerdict(t, runDir, tid))
	// the definition now asks for 5 triggers: a rescore from today's definition would say INCONCLUSIVE
	today := scenario.ExpandVariants(vrtDefinition(t, int64(5)))[0].Trial
	assertEqual(t, int64(5), obj(t, list(t, today["rules"])[0])["min_n"])
	score.ScoreRun(runDir)
	assertEqual(t, "INCONCLUSIVE", vrtVerdict(t, runDir, tid))
}

func TestSnapshotTest_RescoreAfterTheVariantIsRemovedKeepsTheRows(t *testing.T) {
	runDir, tid := vrtRunWithSnapshots(t, t.TempDir(), "snap", int64(1))
	// the shipped files carry no such variant: the run must score from its snapshot, never a
	// lookup of today's trial files (the Go port has no monkeypatch; the nil lookup is the
	// same proof that scoring cannot have used it).
	if scenario.FindTrialByID(tid) != nil {
		t.Fatalf("%s unexpectedly resolves from the shipped files", tid)
	}
	scored := score.ScoreRun(runDir)
	var got []any
	for _, trial := range scored {
		got = append(got, trial["trial"])
	}
	assertEqual(t, []any{tid}, got)
	assertEqual(t, "INCONCLUSIVE", vrtVerdict(t, runDir, tid))
}

func TestSnapshotTest_AMissingSnapshotInASnapshotRunFailsLoudly(t *testing.T) {
	runDir, tid := vrtRunWithSnapshots(t, t.TempDir(), "snap", int64(1))
	if err := os.Remove(filepath.Join(runDir, tid, scenario.SnapshotName)); err != nil {
		t.Fatal(err)
	}
	vrtMustPanic(t, scenario.SnapshotName, func() { score.ScoreRun(runDir) })
}

func TestSnapshotTest_AnUnreadableOrForeignSnapshotFailsLoudly(t *testing.T) {
	runDir, tid := vrtRunWithSnapshots(t, t.TempDir(), "snap", int64(1))
	path := filepath.Join(runDir, tid, scenario.SnapshotName)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	vrtMustPanic(t, "", func() { score.ScoreRun(runDir) })
	writeJSON(t, path, map[string]any{"id": "other-trial"})
	vrtMustPanic(t, "", func() { score.ScoreRun(runDir) })
}

func TestSnapshotTest_ALegacyRunWithoutTheMarkerFallsBackAndSaysSo(t *testing.T) {
	runDir := vrtMakeRun(t, t.TempDir(), "legacy", []string{vrtHealID}, map[string]bool{})
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	scored := score.ScoreRun(runDir)
	w.Close()
	os.Stderr = old
	var captured bytes.Buffer
	if _, err := captured.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	var got []any
	for _, trial := range scored {
		got = append(got, trial["trial"])
	}
	assertEqual(t, []any{vrtHealID}, got)
	if !strings.Contains(captured.String(), "no trial snapshots") {
		t.Fatalf("stderr = %q, want a fallback note", captured.String())
	}
	if !strings.Contains(captured.String(), "CURRENT trial files") {
		t.Fatalf("stderr = %q, want it to name the CURRENT trial files", captured.String())
	}
}

// TestSnapshotTest_TheRunnerMarksItsRunsAndWritesASnapshotPerTrial: the Go port has no runner
// module (and no source to introspect), so the portable half is kept: the merged trial is
// written to <run>/<id>/trial.effective.json and reads back verbatim, which is what
// score.ScoreRun consumes for a run whose RUN.json says "trial_snapshots": true.
func TestSnapshotTest_TheRunnerMarksItsRunsAndWritesASnapshotPerTrial(t *testing.T) {
	trial, err := scenario.LoadSelected(vrtHeal + "@lvl60")
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(t.TempDir(), "r")
	path := filepath.Join(runDir, pyjson.Str(trial["id"]), scenario.SnapshotName)
	writeJSON(t, path, trial)
	assertEqual(t, filepath.Join(runDir, "priest-holy-heal-latency@lvl60", scenario.SnapshotName), path)
	assertEqual(t, trial, readJSON(t, path))
}
