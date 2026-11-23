package edge

// Translation of tests/test_edge_trials.py.

import (
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
	"github.com/pjerra/forever-agent/internal/trials/spells"
)

const (
	etCommit       = "0c56cf649"
	etPlayer       = "0x0000000000000001"
	etBot          = "0x0000000000000002"
	etExtra1       = "0x0000000000000003"
	etExtra2       = "0x0000000000000004"
	etMob          = "0xf13000000000abcd"
	etStanceBattle = int64(2457)
	etStanceDef    = int64(71)
	etDrink        = int64(24355)
)

var etRole = map[string]string{
	etPlayer: "player", etBot: "bot", etExtra1: "extra1", etExtra2: "extra2", etMob: "mob",
}

var etEdgeIDs = []string{"dps-rogue-low-energy", "dps-rogue-thistle-tea", "priest-holy-low-mana-drinks",
	"priest-holy-low-mana-fight", "priest-holy-low-mana-innervate", "priest-holy-low-mana-potion",
	"tank-warrior-two-bot-tanks", "tank-warrior-zero-rage-opening"}

var etExpect = map[string]string{
	"TNK-WAR-2T-NO-STEAL": "PASS", "TNK-WAR-2T-OTHER-HELD": "PASS", "TNK-WAR-2T-FOUGHT": "PASS",
	"TNK-WAR-2T-STANCE": "PASS",
	"PRI-LOWMANA-DRINK": "PASS", "PRI-LOWMANA-DRINK-UPTIME": "PASS",
	"PRI-LOWMANA-NO-NUKE": "PASS", "PRI-LOWMANA-HEALS": "?",
	"PRI-LOWMANA-POTION": "PASS", "PRI-LOWMANA-POTION-HEALS": "PASS",
	"DRU-BAL-INNERVATE-HEALER": "PASS", "DRU-BAL-INNERVATE-NOT-PLAYER": "PASS",
	"DRU-BAL-INNERVATE-PRECOND": "?",
	"TNK-WAR-RAGE0-BLOODRAGE":   "?", "TNK-WAR-RAGE0-ENGAGE": "PASS", "TNK-WAR-RAGE0-EMPTY": "PASS",
	"TNK-WAR-RAGE0-STANCE":     "PASS",
	"DPS-ROG-LOWENERGY-SWINGS": "PASS", "DPS-ROG-LOWENERGY-ACTIVE": "PASS", "DPS-ROG-LOWENERGY-STANCE": "PASS",
	"DPS-ROG-TEA": "?", "DPS-ROG-TEA-ACTIVE": "PASS", "DPS-ROG-TEA-STANCE": "PASS",
}

// trial id -> the variants the file carries (name -> what it overrides)
var etVariants = map[string][]string{
	"priest-holy-heal-latency":          {"lvl50", "lvl60"},
	"tank-warrior-taunt-off-healer":     {"lvl50", "lvl60"},
	"dps-mage-threat-cap":               {"lvl50", "lvl60", "frost"},
	"tank-warrior-no-taunt-tank-player": {"bot-offtank"},
	"priest-holy-low-mana-drinks":       {"mana-5", "mana-50"},
}

var etLineRe = regexp.MustCompile(`\.(cpp|h):\d+`)

func etLoad(t *testing.T, tid string) scenario.Trial {
	t.Helper()
	trial, err := scenario.LoadTrial("edge/" + tid + ".json")
	if err != nil {
		t.Fatalf("load %s: %v", tid, err)
	}
	return trial
}

func etSID(t *testing.T, name string) int64 {
	t.Helper()
	ids := spells.IDsFor(name)
	if len(ids) == 0 {
		t.Fatalf("no spell id for %q", name)
	}
	sorted := make([]int64, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[0]
}

func etSample(t float64, guid string, auras []any, target string, combat bool, hp int64, power any, maxPower int64) map[string]any {
	if auras == nil {
		auras = []any{}
	}
	return map[string]any{
		"t": t, "wall": nil, "unit": etRole[guid], "guid": guid, "hp": hp,
		"max_hp": int64(1000), "power": power, "max_power": maxPower, "auras": auras,
		"casting": nil, "in_combat": combat, "target": target,
		"x": 0.0, "y": 0.0, "z": 0.0,
	}
}

func etEvent(t *testing.T, at float64, kind, src, dst, spell string, amount int64) map[string]any {
	spellID := any(nil)
	spellName := any(nil)
	if spell != "" {
		spellID = etSID(t, spell)
		spellName = spell
	}
	role := func(guid string) string {
		if guid == etMob {
			return "mob"
		}
		return etRole[guid]
	}
	return map[string]any{
		"t": at, "wall": nil, "kind": kind, "src": etRole[src], "src_guid": src,
		"dst": etRole[dst], "dst_guid": dst, "src_unit": etRole[src], "dst_unit": etRole[dst],
		"spell_id": spellID, "spell": spellName, "amount": amount, "dst_hp_pct": int64(60),
		"src_role": role(src), "dst_role": role(dst), "origin": "ta",
	}
}

type etSeriesOpts struct {
	PlayerAuras []any
	BotAuras    func(float64) []any
	BotPower    func(float64) any
	// MobTarget defaults to the player; an explicit empty string is a mob with no target.
	MobTarget   *string
	MobTargetAt func(float64) string
	Combat      *bool
	// Extras defaults to {EXTRA1, EXTRA2} when nil; an empty slice means no extras.
	Extras []string
}

// etSeries is the Python `series`: 1 Hz samples for the player, the bot, the extras and one mob.
func etSeries(secs int, o etSeriesOpts) []any {
	out := []any{}
	combat := true
	if o.Combat != nil {
		combat = *o.Combat
	}
	mobTarget := etPlayer
	if o.MobTarget != nil {
		mobTarget = *o.MobTarget
	}
	extras := o.Extras
	if extras == nil {
		extras = []string{etExtra1, etExtra2}
	}
	for i := range secs + 1 {
		t := float64(i)
		power := any(int64(500))
		if o.BotPower != nil {
			power = o.BotPower(t)
		}
		var botAuras []any
		if o.BotAuras != nil {
			botAuras = o.BotAuras(t)
		}
		out = append(out, etSample(t, etPlayer, o.PlayerAuras, etMob, combat, 600, int64(500), 1000))
		out = append(out, etSample(t, etBot, botAuras, etMob, combat, 600, power, 1000))
		for _, guid := range extras {
			out = append(out, etSample(t, guid, nil, "", combat, 600, int64(500), 1000))
		}
		target := mobTarget
		if o.MobTargetAt != nil {
			target = o.MobTargetAt(t)
		}
		out = append(out, etSample(t, etMob, nil, target, combat, 600, int64(500), 1000))
	}
	return out
}

func etConstPower(v int64) func(float64) any { return func(float64) any { return v } }

// etRules scores one synthetic repeat of trial `tid`; returns {rule id: entry}.
func etRules(t *testing.T, tid string, events, samples []any, secs int, extraMarks []any) map[string]map[string]any {
	t.Helper()
	trial := etLoad(t, tid)
	rep := filepath.Join(t.TempDir(), "run", tid, "rep1")
	doc := map[string]any{"ok": true,
		"guids":  map[string]any{"player": etPlayer, "bot": etBot, "extras": []any{etExtra1, etExtra2}, "mobs": []any{etMob}},
		"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)}}
	writeJSON(t, filepath.Join(rep, "setup.json"), doc)
	marks := []any{map[string]any{"kind": "fight_start", "t": int64(0)}}
	marks = append(marks, extraMarks...)
	marks = append(marks, map[string]any{"kind": "fight_end", "t": float64(secs), "ended": "duration"})
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), samples)
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), events)
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), marks)
	out := score.ScoreRep(rep, trial, nil)
	rules := map[string]map[string]any{}
	for _, rowVal := range list(t, out["rules"]) {
		row := obj(t, rowVal)
		rules[pyjson.Str(row["id"])] = row
	}
	return rules
}

func etVerdict(t *testing.T, tid, rid string, events, samples []any, secs int, extraMarks []any) string {
	t.Helper()
	return pyjson.Str(etRules(t, tid, events, samples, secs, extraMarks)[rid]["verdict"])
}

func etTrialFilesWithSuffix(t *testing.T, name string) string {
	t.Helper()
	for _, path := range scenario.TrialFiles() {
		if filepath.Base(path) == name {
			return path
		}
	}
	t.Fatalf("no trial file %s", name)
	return ""
}

func etNamed(t *testing.T, trial scenario.Trial) map[string]scenario.Trial {
	t.Helper()
	out := map[string]scenario.Trial{}
	for _, variant := range scenario.ExpandVariants(trial) {
		out[variant.Name] = variant.Trial
	}
	return out
}

func etDeepCopy(t *testing.T, v any) any {
	t.Helper()
	data, err := pyjson.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	doc, err := pyjson.Unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return doc
}

func etRulesOf(t *testing.T, trial scenario.Trial) []any {
	t.Helper()
	rules, ok := pyjson.List(trial["rules"])
	if !ok {
		t.Fatalf("trial %v has no rules list", trial["id"])
	}
	return rules
}

func etRuleByID(t *testing.T, trial scenario.Trial, rid string) map[string]any {
	t.Helper()
	for _, ruleVal := range etRulesOf(t, trial) {
		rule := obj(t, ruleVal)
		if pyjson.Str(rule["id"]) == rid {
			return rule
		}
	}
	t.Fatalf("trial %v has no rule %s", trial["id"], rid)
	return nil
}

// ---- trial files ------------------------------------------------------------

func TestFilesTest_EveryFileIsKnown(t *testing.T) {
	var names []string
	for _, path := range scenario.TrialFiles() {
		if strings.HasPrefix(path, "edge/") {
			names = append(names, strings.TrimSuffix(filepath.Base(path), ".json"))
		}
	}
	sort.Strings(names)
	want := append([]string(nil), etEdgeIDs...)
	sort.Strings(want)
	assertEqual(t, want, names)
}

func TestFilesTest_EveryTrialValidates(t *testing.T) {
	for _, tid := range etEdgeIDs {
		trial := etLoad(t, tid)
		assertEqual(t, tid, pyjson.Str(trial["id"]))
		errs := scenario.Validate(trial)
		if len(errs) != 0 {
			t.Fatalf("%s: %v", tid, errs)
		}
	}
}

func TestFilesTest_RulesCiteBotCodeAtACommitAndExpectTheTable(t *testing.T) {
	seen := map[string]bool{}
	for _, tid := range etEdgeIDs {
		for _, ruleVal := range etRulesOf(t, etLoad(t, tid)) {
			rule := obj(t, ruleVal)
			rid := pyjson.Str(rule["id"])
			seen[rid] = true
			want, ok := etExpect[rid]
			if !ok {
				t.Fatalf("%s: rule %s not in EXPECT", tid, rid)
			}
			if got := pyjson.Str(rule["expect_today"]); got != want {
				t.Fatalf("%s: expect_today %q, want %q", rid, got, want)
			}
			if !pyjson.Truthy(rule["why_expect"]) {
				t.Fatalf("%s: no why_expect", rid)
			}
			src := pyjson.Str(rule["source"])
			if !strings.HasPrefix(src, "precondition") {
				if !strings.Contains(src, "ai/playerbot/") {
					t.Fatalf("%s: source %q has no ai/playerbot/", rid, src)
				}
				if !strings.Contains(src, "upstream/dev "+etCommit) {
					t.Fatalf("%s: source %q has no commit", rid, src)
				}
				if !etLineRe.MatchString(src) {
					t.Fatalf("%s: source %q is not a file:line cite", rid, src)
				}
			}
		}
	}
	if len(seen) != len(etExpect) {
		t.Fatalf("seen %d rule ids, want %d", len(seen), len(etExpect))
	}
	for rid := range etExpect {
		if !seen[rid] {
			t.Fatalf("rule %s never seen", rid)
		}
	}
}

func TestFilesTest_VariantsTheFilesCarry(t *testing.T) {
	found := map[string]any{}
	for _, path := range scenario.TrialFiles() {
		trial, err := scenario.LoadTrial(path)
		if err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
		if _, ok := trial["variants"]; !ok {
			continue
		}
		variants := []any{}
		for _, varVal := range list(t, trial["variants"]) {
			variants = append(variants, pyjson.Str(obj(t, varVal)["name"]))
		}
		found[pyjson.Str(trial["id"])] = variants
	}
	want := map[string]any{}
	for tid, names := range etVariants {
		items := []any{}
		for _, name := range names {
			items = append(items, name)
		}
		want[tid] = items
	}
	assertEqual(t, want, found)
}

func TestFilesTest_LevelVariantsNeverGoUnderTheTalentProofFloor(t *testing.T) {
	// the three levels the sweep asks for are 40 (the file), 50 and 60; level 20 is refused (see DECISIONS.md)
	for _, tid := range []string{"priest-holy-heal-latency", "tank-warrior-taunt-off-healer", "dps-mage-threat-cap"} {
		trial, err := scenario.LoadTrial(etTrialFilesWithSuffix(t, tid+".json"))
		if err != nil {
			t.Fatalf("load %s: %v", tid, err)
		}
		merged := etNamed(t, trial)
		if got := num(t, obj(t, trial["player"])["level"]); got != 40 {
			t.Fatalf("%s: base level %v, want 40", tid, got)
		}
		if got := num(t, obj(t, merged["lvl50"]["player"])["level"]); got != 50 {
			t.Fatalf("%s: lvl50 level %v", tid, got)
		}
		if got := num(t, obj(t, merged["lvl60"]["player"])["level"]); got != 60 {
			t.Fatalf("%s: lvl60 level %v", tid, got)
		}
		for name, m := range merged {
			player := obj(t, m["player"])
			bot := obj(t, m["bot"])
			if num(t, bot["level"]) != num(t, player["level"]) {
				t.Fatalf("%s/%s: bot level %v != player level %v", tid, name, bot["level"], player["level"])
			}
			if extras, ok := pyjson.List(m["extra_bots"]); ok {
				for _, extraVal := range extras {
					extra := obj(t, extraVal)
					if num(t, extra["level"]) != num(t, player["level"]) {
						t.Fatalf("%s/%s: extra level %v != player level %v", tid, name, extra["level"], player["level"])
					}
				}
			}
		}
		// no gear kit above level 50: the level 60 variants run without one
		baseGear := obj(t, trial["player"])["gear"]
		if !jsonEqual(obj(t, merged["lvl50"]["player"])["gear"], baseGear) {
			t.Fatalf("%s: lvl50 gear changed", tid)
		}
		if gear := obj(t, merged["lvl60"]["player"])["gear"]; gear != nil {
			t.Fatalf("%s: lvl60 gear %v, want none", tid, gear)
		}
	}
}

func TestFilesTest_FrostVariantIsAFrostMageWithFrostRules(t *testing.T) {
	trial, err := scenario.LoadTrial(etTrialFilesWithSuffix(t, "dps-mage-threat-cap.json"))
	if err != nil {
		t.Fatal(err)
	}
	merged := etNamed(t, trial)["frost"]
	if merged == nil {
		t.Fatal("no frost variant")
	}
	assertEqual(t, "mage", pyjson.Str(merged["class"]))
	assertEqual(t, "frost", pyjson.Str(merged["spec"]))
	assertEqual(t, "mage", pyjson.Str(obj(t, merged["bot"])["class"]))
	assertEqual(t, "frost", pyjson.Str(obj(t, merged["bot"])["spec"]))
	dmg := etRuleByID(t, merged, "DPS-MAG-THREAT-DAMAGE")
	assertEqual(t, []any{"Frostbolt", "Fire Blast", "Frost Nova"},
		obj(t, obj(t, dmg["check"])["where"])["spell"])
	if !strings.Contains(pyjson.Str(dmg["source"]), "FrostMageStrategy.cpp") {
		t.Fatalf("damage source %q", dmg["source"])
	}
	noaggro := etRuleByID(t, merged, "DPS-MAG-THREAT-NO-AGGRO")
	if !strings.Contains(pyjson.Str(noaggro["desc"]), "frost") {
		t.Fatalf("no-aggro desc %q", noaggro["desc"])
	}
}

func TestFilesTest_BotOfftankVariantAddsThePaladinTank(t *testing.T) {
	trial, err := scenario.LoadTrial("tanks/tank-warrior-no-taunt-tank-player.json")
	if err != nil {
		t.Fatal(err)
	}
	merged := etNamed(t, trial)["bot-offtank"]
	if merged == nil {
		t.Fatal("no bot-offtank variant")
	}
	extras := list(t, merged["extra_bots"])
	want := [][2]string{{"priest", "healer"}, {"paladin", "tank"}}
	if len(extras) != len(want) {
		t.Fatalf("extra_bots %v", extras)
	}
	for i, pair := range want {
		extra := obj(t, extras[i])
		if pyjson.Str(extra["class"]) != pair[0] || pyjson.Str(extra["role"]) != pair[1] {
			t.Fatalf("extra_bots[%d] = %v, want %v", i, extra, pair)
		}
	}
	assertEqual(t, "tank", pyjson.Str(obj(t, merged["player"])["forced_role"]))
}

func TestFilesTest_ManaVariantsDifferOnlyInTheMana(t *testing.T) {
	trial := etLoad(t, "priest-holy-low-mana-drinks")
	merged := etNamed(t, trial)
	if got := num(t, obj(t, trial["bot"])["mana_pct"]); got != 15 {
		t.Fatalf("base mana_pct %v", got)
	}
	if got := num(t, obj(t, merged["mana-5"]["bot"])["mana_pct"]); got != 5 {
		t.Fatalf("mana-5 mana_pct %v", got)
	}
	if got := num(t, obj(t, merged["mana-50"]["bot"])["mana_pct"]); got != 50 {
		t.Fatalf("mana-50 mana_pct %v", got)
	}
	for name, m := range merged {
		if !jsonEqual(m["rules"], trial["rules"]) {
			t.Fatalf("%s: rules changed", name)
		}
		found := false
		for _, checkVal := range list(t, m["setup_checks"]) {
			if pyjson.Str(checkVal) == "bot_mana" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: setup_checks lacks bot_mana", name)
		}
	}
}

func TestFilesTest_GroupsAndRoles(t *testing.T) {
	two := etLoad(t, "tank-warrior-two-bot-tanks")
	assertEqual(t, "tanks-melee", pyjson.Str(two["group"]))
	assertEqual(t, "dps", pyjson.Str(obj(t, two["player"])["forced_role"]))
	extras := list(t, two["extra_bots"])
	if pyjson.Str(obj(t, extras[0])["class"]) != "paladin" || pyjson.Str(obj(t, extras[0])["role"]) != "tank" {
		t.Fatalf("extras[0] = %v", extras[0])
	}
	assertEqual(t, "healer", pyjson.Str(obj(t, extras[1])["role"]))
	for _, tid := range []string{"priest-holy-low-mana-drinks", "priest-holy-low-mana-fight", "priest-holy-low-mana-potion"} {
		trial := etLoad(t, tid)
		if got := num(t, obj(t, trial["bot"])["mana_pct"]); got != 15 {
			t.Fatalf("%s: mana_pct %v", tid, got)
		}
		found := false
		for _, checkVal := range list(t, trial["setup_checks"]) {
			if pyjson.Str(checkVal) == "bot_mana" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: setup_checks lacks bot_mana", tid)
		}
	}
}

// ---- two bot tanks ----------------------------------------------------------

func TestTwoBotTanksTest_NoSteal(t *testing.T) {
	const tid = "tank-warrior-two-bot-tanks"
	steal := func(events, samples []any) string {
		return etVerdict(t, tid, "TNK-WAR-2T-NO-STEAL", events, samples, 200, nil)
	}
	onPlayer := etSeries(200, etSeriesOpts{MobTarget: new(etPlayer)})
	onPaladin := etSeries(200, etSeriesOpts{MobTarget: new(etExtra1)})
	ev := []any{etEvent(t, 30, "cast", etBot, etMob, "Taunt", 100)}
	if taunt := etSID(t, "Taunt"); taunt == 0 {
		t.Fatal("no Taunt id")
	}
	// taunting a mob on the DPS player is the job
	assertEqual(t, "PASS", steal(ev, onPlayer))
	// the same cast on the mob the paladin holds
	assertEqual(t, "FAIL", steal(ev, onPaladin))
	assertEqual(t, "FAIL", steal([]any{etEvent(t, 30, "cast", etBot, etMob, "Mocking Blow", 100)}, onPaladin))
	assertEqual(t, "FAIL", steal([]any{etEvent(t, 30, "cast", etBot, etMob, "Challenging Shout", 100)}, onPaladin))
	assertEqual(t, "PASS", steal([]any{}, onPaladin))
}

func TestTwoBotTanksTest_TheOpeningRaceAndARefillAreNotCounted(t *testing.T) {
	const tid = "tank-warrior-two-bot-tanks"
	steal := func(events, samples []any, marks []any) string {
		return etVerdict(t, tid, "TNK-WAR-2T-NO-STEAL", events, samples, 200, marks)
	}
	onPaladin := etSeries(200, etSeriesOpts{MobTarget: new(etExtra1)})
	// before t 10
	assertEqual(t, "PASS", steal([]any{etEvent(t, 5, "cast", etBot, etMob, "Taunt", 100)}, onPaladin, nil))
	refill := []any{map[string]any{"kind": "refill", "t": int64(60)}}
	assertEqual(t, "PASS", steal([]any{etEvent(t, 64, "cast", etBot, etMob, "Taunt", 100)}, onPaladin, refill))
	assertEqual(t, "FAIL", steal([]any{etEvent(t, 70, "cast", etBot, etMob, "Taunt", 100)}, onPaladin, refill))
}

func TestTwoBotTanksTest_TheOtherTankMustHaveHeldAMob(t *testing.T) {
	const tid = "tank-warrior-two-bot-tanks"
	held := []any{}
	for i := range 5 {
		held = append(held, etEvent(t, float64(20+i), "damage", etMob, etExtra1, "", 100))
	}
	samples := etSeries(200, etSeriesOpts{})
	assertEqual(t, "PASS", etVerdict(t, tid, "TNK-WAR-2T-OTHER-HELD", held, samples, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "TNK-WAR-2T-OTHER-HELD", held[:4], samples, 200, nil))
	onPlayer := []any{}
	for i := range 9 {
		onPlayer = append(onPlayer, etEvent(t, float64(20+i), "damage", etMob, etPlayer, "", 100))
	}
	assertEqual(t, "FAIL", etVerdict(t, tid, "TNK-WAR-2T-OTHER-HELD", onPlayer, samples, 200, nil))
}

func TestTwoBotTanksTest_FoughtAndStance(t *testing.T) {
	const tid = "tank-warrior-two-bot-tanks"
	samples := etSeries(200, etSeriesOpts{PlayerAuras: []any{etStanceBattle}})
	cast := []any{}
	for i := range 5 {
		cast = append(cast, etEvent(t, float64(10+i), "cast", etBot, etMob, "Sunder Armor", 100))
	}
	assertEqual(t, "PASS", etVerdict(t, tid, "TNK-WAR-2T-FOUGHT", cast, samples, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "TNK-WAR-2T-FOUGHT", cast[:4], samples, 200, nil))
	assertEqual(t, "PASS", etVerdict(t, tid, "TNK-WAR-2T-STANCE", []any{}, samples, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "TNK-WAR-2T-STANCE", []any{}, etSeries(200, etSeriesOpts{}), 200, nil))
}

// ---- low mana drink ---------------------------------------------------------

func TestLowManaDrinkTest_FirstDrinkWithin30S(t *testing.T) {
	const tid = "priest-holy-low-mana-drinks"
	ooc := func(o etSeriesOpts) []any {
		o.Combat = new(false)
		o.Extras = []string{}
		return etSeries(60, o)
	}
	aura := func(at float64) []any {
		if at >= 8 {
			return []any{etDrink}
		}
		return nil
	}
	drinkEv := func(at float64, dst string) []any {
		return []any{etEvent(t, at, "aura_on", etBot, dst, "Drink", 100)}
	}
	assertEqual(t, "PASS", etVerdict(t, tid, "PRI-LOWMANA-DRINK", drinkEv(8, etBot), ooc(etSeriesOpts{BotAuras: aura}), 60, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-DRINK", drinkEv(45, etBot), ooc(etSeriesOpts{}), 60, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-DRINK", []any{}, ooc(etSeriesOpts{}), 60, nil))
	// a Drink on the player does not count
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-DRINK", drinkEv(8, etPlayer), ooc(etSeriesOpts{}), 60, nil))
}

func TestLowManaDrinkTest_Uptime(t *testing.T) {
	const tid = "priest-holy-low-mana-drinks"
	ooc := func(botAuras func(float64) []any) []any {
		return etSeries(60, etSeriesOpts{BotAuras: botAuras, Combat: new(false), Extras: []string{}})
	}
	long := ooc(func(t float64) []any {
		if 8 <= t && t < 38 {
			return []any{etDrink}
		}
		return nil
	})
	short := ooc(func(t float64) []any {
		if 8 <= t && t < 12 {
			return []any{etDrink}
		}
		return nil
	})
	assertEqual(t, "PASS", etVerdict(t, tid, "PRI-LOWMANA-DRINK-UPTIME", []any{}, long, 60, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-DRINK-UPTIME", []any{}, short, 60, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-DRINK-UPTIME", []any{}, ooc(nil), 60, nil))
}

// ---- low mana fight ---------------------------------------------------------

func TestLowManaFightTest_NoNukeWhileManaIsLow(t *testing.T) {
	const tid = "priest-holy-low-mana-fight"
	smite := []any{etEvent(t, 30, "cast", etBot, etMob, "Smite", 100)}
	low := etSeries(200, etSeriesOpts{BotPower: etConstPower(300)})  // 30 percent of the pool
	high := etSeries(200, etSeriesOpts{BotPower: etConstPower(900)}) // 90 percent
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-NO-NUKE", smite, low, 200, nil))
	assertEqual(t, "PASS", etVerdict(t, tid, "PRI-LOWMANA-NO-NUKE", smite, high, 200, nil))
	assertEqual(t, "PASS", etVerdict(t, tid, "PRI-LOWMANA-NO-NUKE", []any{}, low, 200, nil))
	wand := []any{etEvent(t, 30, "cast", etBot, etMob, "Shoot", 100)}
	// the wand costs no mana
	assertEqual(t, "PASS", etVerdict(t, tid, "PRI-LOWMANA-NO-NUKE", wand, low, 200, nil))
	for _, spell := range []string{"Holy Fire", "Mind Blast", "Shadow Word: Pain"} {
		ev := []any{etEvent(t, 30, "cast", etBot, etMob, spell, 100)}
		if got := etVerdict(t, tid, "PRI-LOWMANA-NO-NUKE", ev, low, 200, nil); got != "FAIL" {
			t.Fatalf("%s: %s", spell, got)
		}
	}
}

func TestLowManaFightTest_StillHeals(t *testing.T) {
	const tid = "priest-holy-low-mana-fight"
	heals := []any{}
	for i := range 3 {
		heals = append(heals, etEvent(t, float64(20+10*i), "cast", etBot, etPlayer, "Renew", 100))
	}
	samples := etSeries(200, etSeriesOpts{})
	assertEqual(t, "PASS", etVerdict(t, tid, "PRI-LOWMANA-HEALS", heals, samples, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-HEALS", heals[:2], samples, 200, nil))
	// a heal on the paladin or the mob is not a heal on the player
	other := []any{}
	for i := range 5 {
		other = append(other, etEvent(t, float64(20+10*i), "cast", etBot, etExtra1, "Renew", 100))
	}
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-HEALS", other, samples, 200, nil))
}

// ---- low mana potion --------------------------------------------------------

func TestLowManaPotionTest_PotionWithin30S(t *testing.T) {
	const tid = "priest-holy-low-mana-potion"
	samples := etSeries(200, etSeriesOpts{})
	potion := func(at float64, spell string) []any {
		return []any{etEvent(t, at, "cast", etBot, etBot, spell, 100)}
	}
	assertEqual(t, "PASS", etVerdict(t, tid, "PRI-LOWMANA-POTION", potion(12, "Restore Mana"), samples, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-POTION", potion(55, "Restore Mana"), samples, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-POTION", []any{}, samples, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-POTION", potion(12, "Renew"), samples, 200, nil))
}

func TestLowManaPotionTest_APotionFrom2sBeforeTheWindowCounts(t *testing.T) {
	// bench-fix-1112: a ready bot answers during the pull (rep1's potion 0.24 s before
	// fight_start was not counted). A potion at t -2 is inside the window now.
	const tid = "priest-holy-low-mana-potion"
	samples := etSeries(200, etSeriesOpts{})
	got := etRules(t, tid, []any{etEvent(t, -0.24, "cast", etBot, etBot, "Restore Mana", 100)}, samples, 200, nil)
	row := got["PRI-LOWMANA-POTION"]
	if verdict := pyjson.Str(row["verdict"]); verdict != "PASS" {
		t.Fatalf("verdict %s", verdict)
	}
	assertAlmostEqual(t, -0.24, num(t, row["value"]), 7)
	far := etRules(t, tid, []any{etEvent(t, -2.5, "cast", etBot, etBot, "Restore Mana", 100)}, samples, 200, nil)
	assertEqual(t, "FAIL", pyjson.Str(far["PRI-LOWMANA-POTION"]["verdict"]))
}

func TestLowManaPotionTest_TheTrialCarriesTheItemCooldownAcrossReps(t *testing.T) {
	const tid = "priest-holy-low-mana-potion"
	rule := etRuleByID(t, etLoad(t, tid), "PRI-LOWMANA-POTION")
	check := obj(t, rule["check"])
	assertEqual(t, int64(-2), obj(t, check["where"])["t_from"])
	if !pyjson.Truthy(check["debounce_responder_across_reps"]) {
		t.Fatal("debounce_responder_across_reps not set")
	}
	assertEqual(t, int64(120), check["debounce_responder_s"])
	assertEqual(t, []any{"Restore Mana"}, check["debounce_responder_spells"])
	if !strings.Contains(pyjson.Str(rule["why_expect"]), "120 s") {
		t.Fatalf("why_expect %q", rule["why_expect"])
	}
	if errs := scenario.Validate(etLoad(t, tid)); len(errs) != 0 {
		t.Fatalf("validate: %v", errs)
	}
}

func TestLowManaPotionTest_BadCooldownShapesOnAFirstRuleAreRejected(t *testing.T) {
	const tid = "priest-holy-low-mana-potion"
	trial := obj(t, etDeepCopy(t, etLoad(t, tid)))
	rules := list(t, trial["rules"])
	first := obj(t, rules[0])
	orig := etDeepCopy(t, first["check"])
	obj(t, first["check"])["debounce_responder_spells"] = []any{"Renew"}
	if !etHasErrContaining(scenario.Validate(trial), "debounce_responder_spells") {
		t.Fatalf("no debounce_responder_spells error: %v", scenario.Validate(trial))
	}
	first["check"] = etDeepCopy(t, orig)
	obj(t, first["check"])["debounce_responder_across_reps"] = false
	if !etHasErrContaining(scenario.Validate(trial), "debounce_responder_across_reps") {
		t.Fatalf("no debounce_responder_across_reps error: %v", scenario.Validate(trial))
	}
}

func etHasErrContaining(errs []string, needle string) bool {
	for _, e := range errs {
		if strings.Contains(e, needle) {
			return true
		}
	}
	return false
}

func TestLowManaPotionTest_HealsAfterThePotion(t *testing.T) {
	const tid = "priest-holy-low-mana-potion"
	heals := []any{}
	for i := range 3 {
		heals = append(heals, etEvent(t, float64(20+10*i), "cast", etBot, etPlayer, "Flash Heal", 100))
	}
	assertEqual(t, "PASS", etVerdict(t, tid, "PRI-LOWMANA-POTION-HEALS", heals, etSeries(200, etSeriesOpts{}), 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "PRI-LOWMANA-POTION-HEALS", heals[:1], etSeries(200, etSeriesOpts{}), 200, nil))
}

// ---- innervate on the low-mana healer ---------------------------------------

// TestEdgeInnervateTrialRules pins the staging (the low-mana priest is `bot`, the balance druid an extra), the three
// rule ids and every rule's target filter and pass line. Round 1 covers the behaviour of today only, so the file
// carries no variants.
func TestEdgeInnervateTrialRules(t *testing.T) {
	const tid = "priest-holy-low-mana-innervate"
	trial := etLoad(t, tid)
	if errs := scenario.Validate(trial); len(errs) != 0 {
		t.Fatalf("%s: %v", tid, errs)
	}
	bot := obj(t, trial["bot"])
	assertEqual(t, "priest", pyjson.Str(bot["class"]))
	assertEqual(t, "holy", pyjson.Str(bot["spec"]))
	assertEqual(t, "healer", pyjson.Str(bot["role"]))
	if got := num(t, bot["mana_pct"]); got != 15 {
		t.Fatalf("%s: bot mana_pct %v, want 15 (under the 25 percent Innervate threshold)", tid, got)
	}
	extras := list(t, trial["extra_bots"])
	if len(extras) != 1 {
		t.Fatalf("%s: %d extra_bots, want 1", tid, len(extras))
	}
	druid := obj(t, extras[0])
	assertEqual(t, "druid", pyjson.Str(druid["class"]))
	assertEqual(t, "balance", pyjson.Str(druid["spec"]))
	assertEqual(t, "dps", pyjson.Str(druid["role"]))
	if _, ok := trial["variants"]; ok {
		t.Fatalf("%s: round 1 carries no variants", tid)
	}

	var ids []any
	for _, ruleVal := range etRulesOf(t, trial) {
		ids = append(ids, pyjson.Str(obj(t, ruleVal)["id"]))
	}
	assertEqual(t, []any{"DRU-BAL-INNERVATE-HEALER", "DRU-BAL-INNERVATE-NOT-PLAYER", "DRU-BAL-INNERVATE-PRECOND"}, ids)

	for _, want := range []struct{ id, metric, src, dst, passIf string }{
		{"DRU-BAL-INNERVATE-HEALER", "first", "$extra1", "$bot", "<= 20"},
		{"DRU-BAL-INNERVATE-NOT-PLAYER", "count", "$extra1", "$player", "== 0"},
	} {
		rule := etRuleByID(t, trial, want.id)
		check := obj(t, rule["check"])
		where := obj(t, check["where"])
		assertEqual(t, want.metric, pyjson.Str(check["metric"]))
		assertEqual(t, "cast", pyjson.Str(where["kind"]))
		assertEqual(t, want.src, pyjson.Str(where["src"]))
		assertEqual(t, want.dst, pyjson.Str(where["dst"]))
		assertEqual(t, "Innervate", pyjson.Str(where["spell"]))
		assertEqual(t, want.passIf, pyjson.Str(rule["pass_if"]))
	}

	// the precondition counts the priest's own casts: a fight where it never acts has nothing to score
	precond := obj(t, etRuleByID(t, trial, "DRU-BAL-INNERVATE-PRECOND")["check"])
	assertEqual(t, "count", pyjson.Str(precond["metric"]))
	assertEqual(t, "cast", pyjson.Str(obj(t, precond["where"])["kind"]))
	assertEqual(t, "$bot", pyjson.Str(obj(t, precond["where"])["src"]))
	assertEqual(t, ">= 3", pyjson.Str(etRuleByID(t, trial, "DRU-BAL-INNERVATE-PRECOND")["pass_if"]))
}

// ---- potion cooldown across reps --------------------------------------------

// etWall is Python's datetime.fromtimestamp(base + offset, timezone.utc).isoformat(timespec="milliseconds").
func etWall(base, offset float64) string {
	ts := base + offset
	whole := int64(math.Floor(ts))
	ms := int64(math.Round((ts - float64(whole)) * 1000))
	return time.Unix(whole, ms*int64(time.Millisecond)).UTC().Format("2006-01-02T15:04:05.000+00:00")
}

func etWriteRep(t *testing.T, trialDir, name string, base float64, potionAt *float64) {
	t.Helper()
	rep := filepath.Join(trialDir, name)
	setup := map[string]any{"ok": true,
		"guids": map[string]any{"player": etPlayer, "bot": etBot, "mobs": []any{etMob}}, "config": map[string]any{}}
	writeJSON(t, filepath.Join(rep, "setup.json"), setup)
	marks := []any{
		map[string]any{"kind": "fight_start", "wall": etWall(base, 0)},
		map[string]any{"kind": "fight_end", "wall": etWall(base, 200), "ended": "duration"},
	}
	events := []any{}
	if potionAt != nil {
		events = append(events, etEvent(t, *potionAt, "cast", etBot, etBot, "Restore Mana", 100))
	}
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), marks)
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), events)
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), []any{})
}

func etScoreCrossRep(t *testing.T, potions []*float64, spacing float64) map[string]any {
	t.Helper()
	const tid = "priest-holy-low-mana-potion"
	tdir := filepath.Join(t.TempDir(), "run", tid)
	base := 1800000000.0
	for i, at := range potions {
		// reps start `spacing` apart (122 s is the nightly spacing: rep2 opens inside rep1's cooldown)
		etWriteRep(t, tdir, "rep"+strconv.Itoa(i+1), base+float64(i)*spacing, at)
	}
	out := score.ScoreTrial(tdir, etLoad(t, tid))
	verdicts := list(t, out["verdicts"])
	if len(verdicts) == 0 {
		t.Fatal("no verdicts")
	}
	return obj(t, verdicts[0])
}

func etDetailRep(t *testing.T, got map[string]any, rep string) map[string]any {
	t.Helper()
	for _, detailVal := range list(t, got["detail"]) {
		row := obj(t, detailVal)
		if pyjson.Str(row["rep"]) == rep {
			return row
		}
	}
	t.Fatalf("no detail row for %s", rep)
	return nil
}

func TestPotionCrossRepCooldownTest_APotionInsideAPartiallyCoveringCooldownIsAMiss(t *testing.T) {
	// bench-fix-1112 round 4: at the nightly 122 s spacing rep 2's cooldown is -112..8, so the
	// t=5 potion stamped inside it is impossible (or a proc row) and is dropped; rep 2 had 8..30
	// left to drink, so the empty hit list is a miss, not no-opportunity.
	got := etScoreCrossRep(t, []*float64{new(10.0), new(5.0)}, 122.0)
	if v := pyjson.Str(got["verdict"]); v != "FAIL" {
		t.Fatalf("verdict %s", v)
	}
	if k := num(t, got["k"]); k != 1 {
		t.Fatalf("k %v", k)
	}
	if n := num(t, got["n"]); n != 2 {
		t.Fatalf("n %v", n)
	}
	rep2 := etDetailRep(t, got, "rep2")
	assertEqual(t, "FAIL", pyjson.Str(rep2["verdict"]))
	assertEqual(t, "", pyjson.Str(rep2["reason"]))
}

func TestPotionCrossRepCooldownTest_APotionInsideAFullyCoveredWindowIsNoChance(t *testing.T) {
	// 80 s spacing leaves rep 2's cooldown at -70..50, which covers its whole -2..30 window:
	// the potion at t=5 sits in it, so the rep is no-opportunity, never a miss.
	got := etScoreCrossRep(t, []*float64{new(10.0), new(5.0)}, 80.0)
	if v := pyjson.Str(got["verdict"]); v != "PASS" {
		t.Fatalf("verdict %s", v)
	}
	if k := num(t, got["k"]); k != 1 {
		t.Fatalf("k %v", k)
	}
	if n := num(t, got["n"]); n != 1 {
		t.Fatalf("n %v", n)
	}
	rep2 := etDetailRep(t, got, "rep2")
	assertEqual(t, "INCONCLUSIVE", pyjson.Str(rep2["verdict"]))
	assertEqual(t, "no opportunity (item on cooldown from an earlier rep)", pyjson.Str(rep2["reason"]))
}

func TestPotionCrossRepCooldownTest_APotionAfterTheCooldownCounts(t *testing.T) {
	got := etScoreCrossRep(t, []*float64{new(10.0), nil, new(5.0)}, 122.0)
	rep3 := etDetailRep(t, got, "rep3")
	assertEqual(t, "PASS", pyjson.Str(rep3["verdict"]))
	assertAlmostEqual(t, 5.0, num(t, rep3["value"]), 7)
}

func TestPotionCrossRepCooldownTest_NoPotionAtAllIsStillAMiss(t *testing.T) {
	got := etScoreCrossRep(t, []*float64{nil}, 122.0)
	rep1 := etDetailRep(t, got, "rep1")
	assertEqual(t, "FAIL", pyjson.Str(rep1["verdict"]))
}

func TestPotionCrossRepCooldownTest_NoPotionInAFullyCoveredWindowIsNoChance(t *testing.T) {
	// codex HIGH (round 1, tightened in round 3): rep 2 opens inside rep 1's 120 s item cooldown
	// with NO potion cast. Only a cooldown that covers the WHOLE response window (here -2..30 on
	// rep2's base) is no opportunity; 80 s spacing leaves the cooldown at -70..50.
	got := etScoreCrossRep(t, []*float64{new(10.0), nil}, 80.0)
	rep2 := etDetailRep(t, got, "rep2")
	assertEqual(t, "INCONCLUSIVE", pyjson.Str(rep2["verdict"]))
	assertEqual(t, "no opportunity (item on cooldown from an earlier rep)", pyjson.Str(rep2["reason"]))
}

func TestPotionCrossRepCooldownTest_NoPotionInAPartiallyCoveredWindowIsAMiss(t *testing.T) {
	// round 3: at the nightly 122 s spacing the cooldown is -112..8, so rep2 still had 8..30 to
	// drink: the no-match scoring applies ("<= 30" FAILs), it is not no-opportunity.
	got := etScoreCrossRep(t, []*float64{new(10.0), nil}, 122.0)
	rep2 := etDetailRep(t, got, "rep2")
	assertEqual(t, "FAIL", pyjson.Str(rep2["verdict"]))
	assertEqual(t, "", pyjson.Str(rep2["reason"]))
}

// ---- zero rage opening ------------------------------------------------------

func TestZeroRageTest_BloodrageWithin10S(t *testing.T) {
	const tid = "tank-warrior-zero-rage-opening"
	samples := etSeries(200, etSeriesOpts{})
	cast := func(at float64) []any {
		return []any{etEvent(t, at, "cast", etBot, etBot, "Bloodrage", 100)}
	}
	assertEqual(t, "PASS", etVerdict(t, tid, "TNK-WAR-RAGE0-BLOODRAGE", cast(4), samples, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "TNK-WAR-RAGE0-BLOODRAGE", cast(25), samples, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "TNK-WAR-RAGE0-BLOODRAGE", []any{}, samples, 200, nil))
}

func TestZeroRageTest_EngagesWithin8S(t *testing.T) {
	const tid = "tank-warrior-zero-rage-opening"
	samples := etSeries(200, etSeriesOpts{})
	hit := func(at float64) []any {
		return []any{etEvent(t, at, "damage", etBot, etMob, "", 100)}
	}
	assertEqual(t, "PASS", etVerdict(t, tid, "TNK-WAR-RAGE0-ENGAGE", hit(3), samples, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "TNK-WAR-RAGE0-ENGAGE", hit(15), samples, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "TNK-WAR-RAGE0-ENGAGE", []any{}, samples, 200, nil))
}

func TestZeroRageTest_TheStartReallyIsEmpty(t *testing.T) {
	const tid = "tank-warrior-zero-rage-opening"
	hit := []any{etEvent(t, 3, "damage", etBot, etMob, "", 100)}
	assertEqual(t, "PASS", etVerdict(t, tid, "TNK-WAR-RAGE0-EMPTY", hit,
		etSeries(200, etSeriesOpts{BotPower: etConstPower(100)}), 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "TNK-WAR-RAGE0-EMPTY", hit,
		etSeries(200, etSeriesOpts{BotPower: etConstPower(800)}), 200, nil))
	late := []any{etEvent(t, 30, "damage", etBot, etMob, "", 100)}
	assertEqual(t, "FAIL", etVerdict(t, tid, "TNK-WAR-RAGE0-EMPTY", late,
		etSeries(200, etSeriesOpts{BotPower: etConstPower(100)}), 200, nil))
}

func TestZeroRageTest_Stance(t *testing.T) {
	const tid = "tank-warrior-zero-rage-opening"
	assertEqual(t, "PASS", etVerdict(t, tid, "TNK-WAR-RAGE0-STANCE", []any{},
		etSeries(200, etSeriesOpts{PlayerAuras: []any{etStanceBattle}}), 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "TNK-WAR-RAGE0-STANCE", []any{},
		etSeries(200, etSeriesOpts{}), 200, nil))
}

func TestZeroRageTest_PullWaitsSoTheRageDecays(t *testing.T) {
	trial := etLoad(t, "tank-warrior-zero-rage-opening")
	assertEqual(t, int64(20), obj(t, trial["situation"])["pull_after_s"])
}

// ---- rogue energy -----------------------------------------------------------

func etSwings(t *testing.T, n int, start, step float64) []any {
	t.Helper()
	out := []any{}
	for i := range n {
		out = append(out, etEvent(t, start+step*float64(i), "damage", etBot, etMob, "", 100))
	}
	return out
}

func TestRogueEnergyTest_SwingsWhileEmpty(t *testing.T) {
	const tid = "dps-rogue-low-energy"
	empty := etSeries(200, etSeriesOpts{BotPower: etConstPower(100)}) // 10 percent of the bar
	full := etSeries(200, etSeriesOpts{BotPower: etConstPower(900)})
	assertEqual(t, "PASS", etVerdict(t, tid, "DPS-ROG-LOWENERGY-SWINGS", etSwings(t, 8, 10, 1.5), empty, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "DPS-ROG-LOWENERGY-SWINGS", etSwings(t, 7, 10, 1.5), empty, 200, nil))
	assertEqual(t, "FAIL", etVerdict(t, tid, "DPS-ROG-LOWENERGY-SWINGS", etSwings(t, 40, 10, 1.5), full, 200, nil))
}

func TestRogueEnergyTest_ActiveRate(t *testing.T) {
	for _, pair := range [][2]string{
		{"dps-rogue-low-energy", "DPS-ROG-LOWENERGY-ACTIVE"},
		{"dps-rogue-thistle-tea", "DPS-ROG-TEA-ACTIVE"},
	} {
		tid, rid := pair[0], pair[1]
		samples := etSeries(200, etSeriesOpts{})
		if got := etVerdict(t, tid, rid, etSwings(t, 100, 10, 1.8), samples, 200, nil); got != "PASS" {
			t.Fatalf("%s: %s", tid, got)
		}
		if got := etVerdict(t, tid, rid, etSwings(t, 20, 10, 8), samples, 200, nil); got != "FAIL" {
			t.Fatalf("%s: %s", tid, got)
		}
	}
}

func TestRogueEnergyTest_Stances(t *testing.T) {
	for _, pair := range [][2]string{
		{"dps-rogue-low-energy", "DPS-ROG-LOWENERGY-STANCE"},
		{"dps-rogue-thistle-tea", "DPS-ROG-TEA-STANCE"},
	} {
		tid, rid := pair[0], pair[1]
		if got := etVerdict(t, tid, rid, []any{}, etSeries(200, etSeriesOpts{PlayerAuras: []any{etStanceDef}}), 200, nil); got != "PASS" {
			t.Fatalf("%s: %s", tid, got)
		}
		if got := etVerdict(t, tid, rid, []any{}, etSeries(200, etSeriesOpts{}), 200, nil); got != "FAIL" {
			t.Fatalf("%s: %s", tid, got)
		}
	}
}

func TestRogueEnergyTest_ThistleTeaAnswersTheDrain(t *testing.T) {
	const tid = "dps-rogue-thistle-tea"
	samples := etSeries(200, etSeriesOpts{})
	drains := []any{map[string]any{"kind": "energy_drain", "t": 20.0}, map[string]any{"kind": "energy_drain", "t": 60.0}}
	tea := []any{etEvent(t, 22, "cast", etBot, etBot, "Restore Energy", 100)}
	assertEqual(t, "PASS", etVerdict(t, tid, "DPS-ROG-TEA", tea, samples, 200, drains))
	// answered once: the 300 s responder cooldown skips the second drain (answered 1/1, not 1/2)
	if n := num(t, etRules(t, tid, tea, samples, 200, drains)["DPS-ROG-TEA"]["n"]); n != 1 {
		t.Fatalf("n %v", n)
	}
	// no answer to two drains: 0/2 FAIL
	assertEqual(t, "FAIL", etVerdict(t, tid, "DPS-ROG-TEA", []any{}, samples, 200, drains))
	// no drain, no chance: INCONCLUSIVE, not a verdict on the bot
	assertEqual(t, "INCONCLUSIVE", etVerdict(t, tid, "DPS-ROG-TEA", []any{}, samples, 200, nil))
}

func TestRogueEnergyTest_TheTeaTrialGivesTheItemAndRunsAt60WithoutAKit(t *testing.T) {
	trial := etLoad(t, "dps-rogue-thistle-tea")
	assertEqual(t, []any{map[string]any{"name": "Thistle Tea", "count": int64(3)}}, obj(t, trial["bot"])["items"])
	setupChecks := []any{}
	for _, checkVal := range list(t, trial["setup_checks"]) {
		setupChecks = append(setupChecks, pyjson.Str(checkVal))
	}
	foundItems := false
	for _, check := range setupChecks {
		if check == "bot_items" {
			foundItems = true
		}
		if check == "player_gear" {
			t.Fatal("setup_checks lists player_gear")
		}
	}
	if !foundItems {
		t.Fatal("setup_checks lacks bot_items")
	}
	if gear := obj(t, trial["player"])["gear"]; gear != nil {
		t.Fatalf("player gear %v, want none", gear)
	}
	if lvl := num(t, obj(t, trial["player"])["level"]); lvl != 60 {
		t.Fatalf("level %v", lvl)
	}
}
