package classes

// Translated from the Python bench's test_rules_tanks_dps.py (tortoise-trials).

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
	"github.com/pjerra/forever-agent/internal/trials/spells"
)

var tndTankIDs = []string{"tank-bear-add-pickup", "tank-bear-bash-caster",
	"tank-bear-defensives", "tank-bear-growl-maul", "tank-bear-hold-single",
	"tank-bear-hold-three", "tank-bear-no-growl-tank-player",
	"tank-paladin-add-pickup", "tank-paladin-defensives",
	"tank-paladin-hoj-caster", "tank-paladin-hold-three",
	"tank-paladin-no-taunt-tank-player", "tank-paladin-righteous-fury",
	"tank-paladin-taunt-off-healer", "tank-warrior-add-pickup",
	"tank-warrior-defensives", "tank-warrior-no-taunt-tank-player",
	"tank-warrior-pack-spread", "tank-warrior-shield-bash-caster",
	"tank-warrior-taunt-off-healer"}

var tndDpsIDs = []string{"dps-mage-cc-respect", "dps-mage-counterspell-caster", "dps-mage-threat-cap",
	"dps-rogue-kick-caster"}

// rule id -> expect_today ("?" = no baseline yet; the why_expect says what would decide it)
var tndExpect = map[string]string{
	"TNK-WAR-TAUNT": "PASS", "TNK-WAR-MOBS-ON-TANK": "?", "TNK-WAR-DEF-STANCE": "PASS",
	"TNK-WAR-TAUNT-STANCE":   "PASS",
	"TNK-PAL-RIGHTEOUS-FURY": "PASS", "TNK-PAL-MOBS-ON-TANK": "?", "TNK-PAL-RF-STANCE": "PASS",
	"DPS-MAG-THREAT-NO-AGGRO": "?", "DPS-MAG-THREAT-TANK-HELD": "PASS",
	"DPS-MAG-THREAT-TANK-FOUGHT": "PASS", "DPS-MAG-THREAT-DAMAGE": "PASS", "DPS-MAG-THREAT-STANCE": "PASS",
	"DPS-ROG-KICK": "?", "DPS-ROG-KICK-STANCE": "PASS",
	"DPS-MAG-COUNTERSPELL": "FAIL", "DPS-MAG-CS-STANCE": "PASS",
	"DPS-MAG-CC-RESPECT": "PASS", "DPS-MAG-CC-POLYMORPH": "PASS",
	"TNK-DRU-GROWL": "?", "TNK-DRU-MAUL": "PASS", "TNK-DRU-FORM": "PASS",
	"TNK-DRU-GROWL-STANCE": "PASS",
	"TNK-WAR-PACK-CLAP":    "PASS", "TNK-WAR-PACK-SHARE": "?",
	"TNK-WAR-PACK-STANCE": "PASS",
	"TNK-WAR-SHIELD-WALL": "?", "TNK-WAR-LAST-STAND": "?", "TNK-WAR-DEFENSIVE-STANCE": "PASS",
	"TNK-PAL-HOLY-SHIELD": "PASS", "TNK-PAL-BULWARK": "?", "TNK-PAL-DEFENSIVE-RF": "PASS",
	"TNK-DRU-FRENZIED": "?", "TNK-DRU-DEFENSIVE-FORM": "PASS",
	"TNK-WAR-SHIELD-BASH": "?", "TNK-WAR-BASH-STANCE": "PASS",
	"TNK-PAL-HOJ": "?", "TNK-PAL-HOJ-STANCE": "PASS",
	"TNK-DRU-BASH": "?", "TNK-DRU-BASH-FORM": "PASS",
	"TNK-WAR-NO-TAUNT": "PASS", "TNK-WAR-NO-TAUNT-FOUGHT": "PASS", "TNK-WAR-NO-TAUNT-STANCE": "PASS",
	"TNK-DRU-NO-GROWL": "PASS", "TNK-DRU-NO-GROWL-FOUGHT": "PASS", "TNK-DRU-NO-GROWL-STANCE": "PASS",
	"TNK-PAL-NO-TAUNT": "PASS", "TNK-PAL-NO-TAUNT-FOUGHT": "PASS", "TNK-PAL-NO-TAUNT-STANCE": "PASS",
	"TNK-PAL-TAUNT": "?", "TNK-PAL-TAUNT-STANCE": "PASS",
	"TNK-DRU-HOLD-SINGLE": "?", "TNK-DRU-HOLD-SINGLE-STANCE": "PASS",
	"TNK-DRU-HOLD-THREE": "?", "TNK-DRU-HOLD-THREE-STANCE": "PASS",
	"TNK-PAL-HOLD-THREE": "?", "TNK-PAL-HOLD-THREE-STANCE": "PASS",
	"TNK-WAR-ADD-PICKUP": "PASS", "TNK-WAR-ADD-STANCE": "PASS",
	"TNK-DRU-ADD-PICKUP": "?", "TNK-DRU-ADD-STANCE": "PASS",
	"TNK-PAL-ADD-PICKUP": "?", "TNK-PAL-ADD-STANCE": "PASS",
	"TNK-PAL-NO-DRINK-UNDER-ATTACK": "PASS",
}

const (
	tndPlayer = "0x0000000000000001"
	tndBot    = "0x0000000000000002"
	tndExtra1 = "0x0000000000000003"
	tndMob    = "0xf13000000000abcd"
	tndMarked = "0xf13000000000beef"
)

var tndRole = map[string]string{
	tndPlayer: "player", tndBot: "bot", tndExtra1: "extra1", tndMob: "mob", tndMarked: "mob",
}

const (
	// tndLightningBolt is a cast-time, silence-interruptible spell (spells.IsInterruptible).
	tndLightningBolt = int64(9532)
	// tndPolymorphR3 is the rank a level 40-59 mage casts.
	tndPolymorphR3 = int64(12825)
)

func tndLoad(t *testing.T, tid string) scenario.Trial {
	t.Helper()
	folder := "dps/"
	if strings.HasPrefix(tid, "tank-") {
		folder = "tanks/"
	}
	trial, err := scenario.LoadTrial(folder + tid + ".json")
	if err != nil {
		t.Fatalf("load %s: %v", tid, err)
	}
	return trial
}

func tndRuleOf(t *testing.T, trial map[string]any, rid string) map[string]any {
	t.Helper()
	for _, ruleVal := range list(t, trial["rules"]) {
		rule := obj(t, ruleVal)
		if pyjson.Str(rule["id"]) == rid {
			return rule
		}
	}
	t.Fatalf("rule %s not in trial %v", rid, trial["id"])
	return nil
}

func tndRulesOf(t *testing.T, trial map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ruleVal := range list(t, trial["rules"]) {
		out = append(out, obj(t, ruleVal))
	}
	return out
}

func tndSid(t *testing.T, name string) int64 {
	t.Helper()
	ids := spells.IDsFor(name)
	sorted := make([]int64, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	if len(sorted) == 0 {
		t.Fatalf("spell %q has no ids", name)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[0]
}

// ---- synthetic repeat -------------------------------------------------------

func tndSampleRow(ts float64, guid string, auras []any, target string, combat bool, hp int64) map[string]any {
	if auras == nil {
		auras = []any{}
	}
	return map[string]any{
		"t": ts, "wall": nil, "unit": tndRole[guid], "guid": guid, "hp": hp,
		"max_hp": int64(1000), "power": int64(500), "max_power": int64(500), "auras": auras,
		"casting": nil, "in_combat": combat, "target": target,
		"x": 0.0, "y": 0.0, "z": 0.0,
	}
}

func tndEvent(ts float64, kind, src, dst string, spellID any) map[string]any {
	roleOf := func(guid string) string {
		if guid == tndMob || guid == tndMarked {
			return "mob"
		}
		return tndRole[guid]
	}
	var spell any
	if spellID != nil {
		spell = spells.NameFor(spellID.(int64))
	}
	return map[string]any{
		"t": ts, "wall": nil, "kind": kind, "src": tndRole[src], "src_guid": src,
		"dst": tndRole[dst], "dst_guid": dst, "src_unit": tndRole[src], "dst_unit": tndRole[dst],
		"spell_id": spellID, "spell": spell, "amount": int64(100), "dst_hp_pct": int64(60),
		"src_role": roleOf(src), "dst_role": roleOf(dst), "origin": "ta",
	}
}

// tndSeries builds 1 Hz samples: player, bot, extra1, the mob on the player, and the marked mob.
func tndSeries(secs int, player, bot []any, marked func(float64) []any) []any {
	var out []any
	for ts := range secs + 1 {
		tf := float64(ts)
		out = append(out, tndSampleRow(tf, tndPlayer, player, tndMob, true, 600))
		out = append(out, tndSampleRow(tf, tndBot, bot, tndMob, true, 600))
		out = append(out, tndSampleRow(tf, tndExtra1, nil, "", true, 600))
		out = append(out, tndSampleRow(tf, tndMob, nil, tndPlayer, true, 600))
		var markedAuras []any
		if marked != nil {
			markedAuras = marked(tf)
		}
		out = append(out, tndSampleRow(tf, tndMarked, markedAuras, tndPlayer, true, 600))
	}
	return out
}

// tndScore scores one synthetic repeat of trial tid; returns {rule id: entry}.
func tndScore(t *testing.T, tid string, events, samples []any, secs int) map[string]map[string]any {
	t.Helper()
	if secs == 0 {
		secs = 200
	}
	rep := filepath.Join(t.TempDir(), "run", tid, "rep1")
	writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{
		"ok": true,
		"guids": map[string]any{"player": tndPlayer, "bot": tndBot,
			"extras": []any{tndExtra1}, "mobs": []any{tndMob, tndMarked}, "marked": tndMarked},
		"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)},
	})
	if events == nil {
		events = []any{}
	}
	if samples == nil {
		samples = []any{}
	}
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), samples)
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), events)
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), []any{
		map[string]any{"kind": "fight_start", "t": int64(0)},
		map[string]any{"kind": "fight_end", "t": int64(secs), "ended": "duration"},
	})
	out := score.ScoreRep(rep, tndLoad(t, tid), nil)
	rules := map[string]map[string]any{}
	for _, entryVal := range list(t, out["rules"]) {
		entry := obj(t, entryVal)
		rules[pyjson.Str(entry["id"])] = entry
	}
	return rules
}

func tndVerdict(t *testing.T, tid, rid string, events, samples []any) string {
	t.Helper()
	rules := tndScore(t, tid, events, samples, 0)
	entry, ok := rules[rid]
	if !ok {
		t.Fatalf("rule %s missing from %s", rid, tid)
	}
	return pyjson.Str(entry["verdict"])
}

func tndHasInt(values any, want int64) bool {
	for _, val := range hlrValues(values) {
		if got, ok := pyjson.Int64(val); ok && got == want {
			return true
		}
	}
	return false
}

// ---- trial files ------------------------------------------------------------

func TestFilesTest_EveryFileIsKnown(t *testing.T) {
	for _, group := range []struct {
		prefix string
		ids    []string
	}{{"tanks/", tndTankIDs}, {"dps/", tndDpsIDs}} {
		var names []string
		for _, path := range scenario.TrialFiles() {
			if strings.HasPrefix(path, group.prefix) {
				names = append(names, strings.TrimSuffix(strings.TrimPrefix(path, group.prefix), ".json"))
			}
		}
		sort.Strings(names)
		want := append([]string{}, group.ids...)
		sort.Strings(want)
		assertEqual(t, want, names)
	}
}

func TestFilesTest_EveryTrialValidates(t *testing.T) {
	for _, tid := range append(append([]string{}, tndTankIDs...), tndDpsIDs...) {
		trial := tndLoad(t, tid)
		assertEqual(t, tid, trial["id"])
		if errs := scenario.Validate(trial); len(errs) != 0 {
			t.Fatalf("%s: %v", tid, errs)
		}
	}
}

func TestFilesTest_Groups(t *testing.T) {
	for _, tid := range tndTankIDs {
		trial := tndLoad(t, tid)
		assertEqual(t, "tanks-melee", trial["group"])
		assertEqual(t, "tank", obj(t, trial["bot"])["role"])
	}
	for _, tid := range tndDpsIDs {
		trial := tndLoad(t, tid)
		want := "tanks-melee"
		if class := pyjson.Str(trial["class"]); class == "mage" || class == "hunter" || class == "warlock" {
			want = "ranged"
		}
		assertEqual(t, want, trial["group"])
		assertEqual(t, "dps", obj(t, trial["bot"])["role"])
	}
}

func TestFilesTest_RulesCiteBotCodeAndExpectTheTable(t *testing.T) {
	seen := map[string]bool{}
	for _, tid := range append(append([]string{}, tndTankIDs...), tndDpsIDs...) {
		for _, rule := range tndRulesOf(t, tndLoad(t, tid)) {
			id := pyjson.Str(rule["id"])
			seen[id] = true
			expected, ok := tndExpect[id]
			if !ok {
				t.Fatalf("%s: rule %s not in the table", tid, id)
			}
			assertEqual(t, expected, rule["expect_today"])
			if !pyjson.Truthy(rule["why_expect"]) {
				t.Fatalf("%s: why_expect is empty", id)
			}
			source := pyjson.Str(rule["source"])
			if !strings.HasPrefix(source, "precondition") && !strings.Contains(source, "ai/playerbot/") {
				t.Fatalf("%s: source %q does not cite ai/playerbot/", id, source)
			}
		}
	}
	assertEqual(t, len(tndExpect), len(seen))
	for id := range tndExpect {
		if !seen[id] {
			t.Fatalf("rule %s in the table but in no trial", id)
		}
	}
}

func TestFilesTest_PlayerRoleIsForced(t *testing.T) {
	// most tank trials set the player to a non-tank (Battle Stance, forced dps) so every mob
	// opens on a non-tank; the no-taunt controls and the interrupt trials need a player tank
	// (Defensive Stance, forced tank). The forced role decides first (PlayerbotAI::IsTank), so
	// a stance read cannot flip it
	tankPlayer := map[string]bool{
		"tank-warrior-no-taunt-tank-player": true, "tank-bear-no-growl-tank-player": true,
		"tank-paladin-no-taunt-tank-player": true, "tank-warrior-shield-bash-caster": true,
		"tank-paladin-hoj-caster": true, "tank-bear-bash-caster": true,
	}
	for _, tid := range tndTankIDs {
		want := "dps"
		if tankPlayer[tid] {
			want = "tank"
		}
		assertEqual(t, want, obj(t, tndLoad(t, tid)["player"])["forced_role"])
	}
	for _, tid := range tndDpsIDs {
		// dps-mage-threat-cap now runs a dps-kit player under a hired bot tank (P1)
		want := "tank"
		if tid == "dps-mage-threat-cap" {
			want = "dps"
		}
		assertEqual(t, want, obj(t, tndLoad(t, tid)["player"])["forced_role"])
	}
	for _, tid := range append(append([]string{}, tndTankIDs...), tndDpsIDs...) {
		if !hlrHas(tndLoad(t, tid)["setup_checks"], "player_forced_role") {
			t.Fatalf("%s: player_forced_role setup check missing", tid)
		}
	}
}

func TestFilesTest_EveryTrialBringsAHealer(t *testing.T) {
	// a GM-levelled player with no healer hits the rescue cap within a minute (the
	// paladin-holy-heal-latency runs): every tank and DPS trial hires a holy priest as extra1
	all := append(append([]string{}, tndTankIDs...), tndDpsIDs...)
	for _, tid := range all {
		trial := tndLoad(t, tid)
		var pairs []any
		for _, botVal := range list(t, trial["extra_bots"]) {
			bot := obj(t, botVal)
			pairs = append(pairs, []any{bot["class"], bot["role"]})
		}
		if tid == "dps-mage-threat-cap" {
			assertEqual(t, []any{[]any{"warrior", "tank"}, []any{"priest", "healer"}}, pairs)
			continue
		}
		assertEqual(t, []any{[]any{"priest", "healer"}}, pairs)
		for _, botVal := range list(t, trial["extra_bots"]) {
			bot := obj(t, botVal)
			assertEqual(t, obj(t, trial["bot"])["level"], bot["level"])
		}
		// the two remaining mage trials fight mobs that are on the mage/the tank: 30;
		// everything else 12 (DECISIONS.md, 2026-11-09; P1 moved threat-cap to 12)
		want := int64(12)
		if tid == "dps-mage-counterspell-caster" || tid == "dps-mage-cc-respect" {
			want = 30
		}
		assertEqual(t, want, obj(t, trial["situation"])["max_rescues_per_min"])
	}
}

func TestFilesTest_CasterTrialsProveTheCaster(t *testing.T) {
	for _, tid := range []string{"dps-rogue-kick-caster", "dps-mage-counterspell-caster",
		"tank-warrior-shield-bash-caster", "tank-paladin-hoj-caster",
		"tank-bear-bash-caster"} {
		trial := tndLoad(t, tid)
		situation := obj(t, trial["situation"])
		assertEqual(t, "caster", obj(t, situation["mobs"])["role"])
		if !hlrHas(trial["setup_checks"], "mob_caster_casts") {
			t.Fatalf("%s: mob_caster_casts setup check missing", tid)
		}
	}
}

func TestFilesTest_CCRespectReadsEveryPolymorphRank(t *testing.T) {
	tid, rid := "dps-mage-cc-respect", "DPS-MAG-CC-RESPECT"
	rule := tndRuleOf(t, tndLoad(t, tid), rid)
	where := obj(t, obj(t, rule["check"])["where"])
	for _, rank := range []int64{118, 12824, 12825, 12826} {
		if !tndHasInt(where["dst_has_aura"], rank) {
			t.Fatalf("%s: aura %d missing", tid, rank)
		}
		assertEqual(t, "Polymorph", spells.NameFor(rank))
	}
	if !hlrHas(tndLoad(t, tid)["setup_checks"], "cc_mark_set") {
		t.Fatalf("%s: cc_mark_set setup check missing", tid)
	}
	// the moon mark plus two unmarked mobs: a kill never leaves the sheep as the only target
	assertEqual(t, int64(3), obj(t, obj(t, tndLoad(t, tid)["situation"])["mobs"])["count"])
}

func TestFilesTest_SpellIDsTheRulesRead(t *testing.T) {
	assertEqual(t, "Righteous Fury", spells.NameFor(25780))
	assertEqual(t, "Defensive Stance", spells.NameFor(71))
	assertEqual(t, "Battle Stance", spells.NameFor(2457))
	if !spells.IDsFor("Taunt")[355] {
		t.Fatal("Taunt has no id 355")
	}
	if !spells.SpellIsInterruptible(tndLightningBolt) {
		t.Fatal("Lightning Bolt is not interruptible")
	}
	for _, name := range []string{"Growl", "Maul", "Swipe", "Challenging Shout", "Shield Wall",
		"Last Stand", "Shield Block", "Shield Bash", "Hand of Reckoning", "Righteous Defense",
		"Holy Shield", "Bulwark of the Righteous", "Hammer of Justice",
		"Thunder Clap", "Bash", "Consecration", "Frenzied Regeneration",
		"Bear Form", "Dire Bear Form"} {
		if len(spells.IDsFor(name)) == 0 {
			t.Fatalf("spell %q has no ids", name)
		}
	}
}

// ---- tank rules -------------------------------------------------------------

const tndTauntTID = "tank-warrior-taunt-off-healer"

func TestTankRulesTest_TauntLatency(t *testing.T) {
	times := []float64{10, 30, 70, 100, 130}
	var hits []any
	for _, ts := range times {
		dst := tndPlayer
		if ts >= 50 {
			dst = tndExtra1
		}
		hits = append(hits, tndEvent(ts, "damage", tndMob, dst, nil))
	}
	var taunts []any
	for _, ts := range times {
		taunts = append(taunts, tndEvent(ts+2, "cast", tndBot, tndMob, int64(355)))
	}
	samples := tndSeries(200, nil, nil, nil)
	assertEqual(t, "PASS", tndVerdict(t, tndTauntTID, "TNK-WAR-TAUNT", append(append([]any{}, hits...), taunts...), samples))
	assertEqual(t, "FAIL", tndVerdict(t, tndTauntTID, "TNK-WAR-TAUNT", hits, samples))
	// Mocking Blow and Challenging Shout answer too (the dev rows: mocking blow taunt, AoE taunt)
	var other []any
	for i, ts := range times {
		spell := tndSid(t, "Challenging Shout")
		if i%2 == 1 {
			spell = tndSid(t, "Mocking Blow")
		}
		other = append(other, tndEvent(ts+2, "cast", tndBot, tndMob, spell))
	}
	assertEqual(t, "PASS", tndVerdict(t, tndTauntTID, "TNK-WAR-TAUNT", append(append([]any{}, hits...), other...), samples))
	// a taunt after the 5 s window does not answer
	var late []any
	for _, ts := range times {
		late = append(late, tndEvent(ts+6, "cast", tndBot, tndMob, int64(355)))
	}
	assertEqual(t, "FAIL", tndVerdict(t, tndTauntTID, "TNK-WAR-TAUNT", append(append([]any{}, hits...), late...), samples))
	// a taunt on another unit (the marked mob, not the one that hit) does not answer
	var elsewhere []any
	for _, ts := range times {
		elsewhere = append(elsewhere, tndEvent(ts+2, "cast", tndBot, tndMarked, int64(355)))
	}
	assertEqual(t, "FAIL", tndVerdict(t, tndTauntTID, "TNK-WAR-TAUNT", append(append([]any{}, hits...), elsewhere...), samples))
	// the debounce folds a mob's swings inside 12 s into one trigger: n stays 5
	swings := append(append([]any{}, hits...),
		tndEvent(11, "damage", tndMob, tndPlayer, nil),
		tndEvent(12, "damage", tndMob, tndPlayer, nil))
	got := tndScore(t, tndTauntTID, append(swings, taunts...), samples, 0)["TNK-WAR-TAUNT"]
	assertEqual(t, []any{"PASS", int64(5)}, []any{got["verdict"], got["n"]})
	// fewer than min_n 5 triggers is INCONCLUSIVE
	assertEqual(t, "INCONCLUSIVE", tndVerdict(t, tndTauntTID, "TNK-WAR-TAUNT",
		append(append([]any{}, hits[:4]...), taunts[:4]...), samples))
}

func TestTankRulesTest_MobsOnTankRatio(t *testing.T) {
	samples := tndSeries(200, nil, nil, nil)
	var good, bad []any
	for i := range 20 {
		good = append(good, tndEvent(float64(10+i), "damage", tndMob, tndBot, nil))
	}
	for i := range 5 {
		good = append(good, tndEvent(float64(40+i), "damage", tndMob, tndPlayer, nil))
	}
	for i := range 10 {
		bad = append(bad, tndEvent(float64(10+i), "damage", tndMob, tndBot, nil))
	}
	for i := range 15 {
		bad = append(bad, tndEvent(float64(40+i), "damage", tndMob, tndExtra1, nil))
	}
	assertEqual(t, "PASS", tndVerdict(t, tndTauntTID, "TNK-WAR-MOBS-ON-TANK", good, samples))
	assertEqual(t, "FAIL", tndVerdict(t, tndTauntTID, "TNK-WAR-MOBS-ON-TANK", bad, samples))
}

func TestTankRulesTest_Stances(t *testing.T) {
	up := tndSeries(200, []any{int64(2457)}, []any{int64(71)}, nil)
	down := tndSeries(200, []any{int64(71)}, []any{int64(2457)}, nil)
	assertEqual(t, "PASS", tndVerdict(t, tndTauntTID, "TNK-WAR-DEF-STANCE", nil, up))
	assertEqual(t, "FAIL", tndVerdict(t, tndTauntTID, "TNK-WAR-DEF-STANCE", nil, down))
	assertEqual(t, "PASS", tndVerdict(t, tndTauntTID, "TNK-WAR-TAUNT-STANCE", nil, up))
	assertEqual(t, "FAIL", tndVerdict(t, tndTauntTID, "TNK-WAR-TAUNT-STANCE", nil, down))
}

func TestTankRulesTest_RighteousFury(t *testing.T) {
	tid := "tank-paladin-righteous-fury"
	for _, aura := range []int64{20450, 25780, 25781} {
		assertEqual(t, "PASS", tndVerdict(t, tid, "TNK-PAL-RIGHTEOUS-FURY", nil,
			tndSeries(200, nil, []any{aura}, nil)))
	}
	assertEqual(t, "FAIL", tndVerdict(t, tid, "TNK-PAL-RIGHTEOUS-FURY", nil, tndSeries(200, nil, nil, nil)))
	var good, bad []any
	for i := range 20 {
		good = append(good, tndEvent(float64(10+i), "damage", tndMob, tndBot, nil))
	}
	for i := range 2 {
		good = append(good, tndEvent(float64(40+i), "damage", tndMob, tndExtra1, nil))
	}
	for i := range 25 {
		bad = append(bad, tndEvent(float64(10+i), "damage", tndMob, tndPlayer, nil))
	}
	assertEqual(t, "PASS", tndVerdict(t, tid, "TNK-PAL-MOBS-ON-TANK", good, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, tid, "TNK-PAL-MOBS-ON-TANK", bad, tndSeries(200, nil, nil, nil)))
}

// tndLatency is a generic latency pass/fail pair: 5 triggers, answered in-window vs never
// answered.
func tndLatency(t *testing.T, tid, rid string, spellID int64, gap, within float64) ([]any, []any) {
	t.Helper()
	var times []float64
	for i := range 5 {
		times = append(times, 10+gap*float64(i))
	}
	var hits, answers []any
	for _, ts := range times {
		dst := tndPlayer
		if ts >= 50 {
			dst = tndExtra1
		}
		hits = append(hits, tndEvent(ts, "damage", tndMob, dst, nil))
		answers = append(answers, tndEvent(ts+within-1, "cast", tndBot, tndMob, spellID))
	}
	samples := tndSeries(200, nil, nil, nil)
	assertEqual(t, "PASS", tndVerdict(t, tid, rid, append(append([]any{}, hits...), answers...), samples))
	assertEqual(t, "FAIL", tndVerdict(t, tid, rid, hits, samples))
	return hits, samples
}

func TestTankRulesTest_BearGrowlMaulForm(t *testing.T) {
	tid := "tank-bear-growl-maul"
	tndLatency(t, tid, "TNK-DRU-GROWL", tndSid(t, "Growl"), 20, 2)
	// Maul is a rate per minute of scored time (>= 4): 12 casts in 200 s pass, 2 fail
	var many []any
	for i := range 20 {
		many = append(many, tndEvent(float64(5+9*i), "cast", tndBot, tndMob, tndSid(t, "Maul")))
	}
	few := many[:2]
	assertEqual(t, "PASS", tndVerdict(t, tid, "TNK-DRU-MAUL", many, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, tid, "TNK-DRU-MAUL", few, tndSeries(200, nil, nil, nil)))
	// Bear or Dire Bear Form uptime >= 0.9
	assertEqual(t, "PASS", tndVerdict(t, tid, "TNK-DRU-FORM", nil, tndSeries(200, nil, []any{int64(5487)}, nil)))
	assertEqual(t, "PASS", tndVerdict(t, tid, "TNK-DRU-FORM", nil, tndSeries(200, nil, []any{int64(9634)}, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, tid, "TNK-DRU-FORM", nil, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "PASS", tndVerdict(t, tid, "TNK-DRU-GROWL-STANCE", nil, tndSeries(200, []any{int64(2457)}, nil, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, tid, "TNK-DRU-GROWL-STANCE", nil, tndSeries(200, []any{int64(71)}, nil, nil)))
}

func TestTankRulesTest_WarriorPackSpread(t *testing.T) {
	tid := "tank-warrior-pack-spread"
	// Thunder Clap count >= 1: one clap passes, none fails
	clap := []any{tndEvent(30, "cast", tndBot, tndMob, tndSid(t, "Thunder Clap"))}
	assertEqual(t, "PASS", tndVerdict(t, tid, "TNK-WAR-PACK-CLAP", clap, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, tid, "TNK-WAR-PACK-CLAP", nil, tndSeries(200, nil, nil, nil)))
	// share <= 0.4 over 25 hits
	var good, bad []any
	for i := range 18 {
		good = append(good, tndEvent(float64(10+i), "damage", tndMob, tndBot, nil))
	}
	for i := range 7 {
		good = append(good, tndEvent(float64(40+i), "damage", tndMob, tndPlayer, nil))
	}
	for i := range 10 {
		bad = append(bad, tndEvent(float64(10+i), "damage", tndMob, tndBot, nil))
	}
	for i := range 15 {
		bad = append(bad, tndEvent(float64(40+i), "damage", tndMob, tndPlayer, nil))
	}
	assertEqual(t, "PASS", tndVerdict(t, tid, "TNK-WAR-PACK-SHARE", good, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, tid, "TNK-WAR-PACK-SHARE", bad, tndSeries(200, nil, nil, nil)))
}

func TestTankRulesTest_Defensives(t *testing.T) {
	// Shield Wall / Last Stand / Frenzied Regen fire only at low bot hp (src_hp_le), and only
	// count when the bot spent time that low (opportunity + min_opportunity_s): full-hp samples
	// give no opportunity (INCONCLUSIVE), thin samples score the cast
	fat := tndSeries(200, nil, nil, nil)
	thinBot := func(hp int64) []any {
		var out []any
		for ts := range 201 {
			out = append(out, tndSampleRow(float64(ts), tndBot, nil, "", true, hp))
		}
		for _, row := range tndSeries(200, nil, nil, nil) {
			if pyjson.Str(obj(t, row)["guid"]) != tndBot {
				out = append(out, row)
			}
		}
		return out
	}
	thin := thinBot(400)
	wall := []any{tndEvent(50, "cast", tndBot, tndMob, tndSid(t, "Shield Wall"))}
	last := []any{tndEvent(50, "cast", tndBot, tndMob, tndSid(t, "Last Stand"))}
	frenzy := []any{tndEvent(50, "cast", tndBot, tndMob, tndSid(t, "Frenzied Regeneration"))}
	assertEqual(t, "INCONCLUSIVE", tndVerdict(t, "tank-warrior-defensives", "TNK-WAR-SHIELD-WALL", wall, fat))
	assertEqual(t, "PASS", tndVerdict(t, "tank-warrior-defensives", "TNK-WAR-SHIELD-WALL", wall, thin))
	thinCrit := thinBot(150)
	assertEqual(t, "PASS", tndVerdict(t, "tank-warrior-defensives", "TNK-WAR-LAST-STAND", last, thinCrit))
	assertEqual(t, "INCONCLUSIVE", tndVerdict(t, "tank-bear-defensives", "TNK-DRU-FRENZIED", frenzy, fat))
	assertEqual(t, "PASS", tndVerdict(t, "tank-bear-defensives", "TNK-DRU-FRENZIED", frenzy, thin))
	assertEqual(t, "PASS", tndVerdict(t, "tank-warrior-defensives", "TNK-WAR-DEFENSIVE-STANCE", nil,
		tndSeries(200, nil, []any{int64(71)}, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, "tank-warrior-defensives", "TNK-WAR-DEFENSIVE-STANCE", nil,
		tndSeries(200, nil, []any{int64(2457)}, nil)))
	// Holy Shield rate >= 2/min: 8 casts in 200 s pass, 1 fails; Bulwark count >= 1
	var holy []any
	for i := range 8 {
		holy = append(holy, tndEvent(float64(5+20*i), "cast", tndBot, tndMob, tndSid(t, "Holy Shield")))
	}
	assertEqual(t, "PASS", tndVerdict(t, "tank-paladin-defensives", "TNK-PAL-HOLY-SHIELD", holy, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, "tank-paladin-defensives", "TNK-PAL-HOLY-SHIELD", holy[:1], tndSeries(200, nil, nil, nil)))
	bulwark := []any{tndEvent(60, "cast", tndBot, tndMob, tndSid(t, "Bulwark of the Righteous"))}
	assertEqual(t, "PASS", tndVerdict(t, "tank-paladin-defensives", "TNK-PAL-BULWARK", bulwark, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, "tank-paladin-defensives", "TNK-PAL-BULWARK", nil, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "PASS", tndVerdict(t, "tank-paladin-defensives", "TNK-PAL-DEFENSIVE-RF", nil,
		tndSeries(200, nil, []any{int64(20450)}, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, "tank-paladin-defensives", "TNK-PAL-DEFENSIVE-RF", nil, tndSeries(200, nil, nil, nil)))
}

func TestTankRulesTest_TankInterrupts(t *testing.T) {
	// Shield Bash (12 s responder cooldown): 5 casts at gap 20 all answer
	tid, rid := "tank-warrior-shield-bash-caster", "TNK-WAR-SHIELD-BASH"
	var casts, answers, instants []any
	for i := range 5 {
		casts = append(casts, tndEvent(float64(10+20*i), "cast_start", tndMob, tndPlayer, tndLightningBolt))
		answers = append(answers, tndEvent(11.5+20*float64(i), "cast", tndBot, tndMob, tndSid(t, "Shield Bash")))
		instants = append(instants, tndEvent(float64(10+20*i), "cast_start", tndMob, tndPlayer, int64(355)))
	}
	samples := tndSeries(200, []any{int64(71)}, nil, nil)
	assertEqual(t, "PASS", tndVerdict(t, tid, rid, append(append([]any{}, casts...), answers...), samples))
	assertEqual(t, "FAIL", tndVerdict(t, tid, rid, casts, samples))
	assertEqual(t, "INCONCLUSIVE", tndVerdict(t, tid, rid, instants, samples))
	// Hammer of Justice and Bash (60 s responder cooldown): 3 casts at gap 70 all answer
	for _, pair := range [][2]string{
		{"tank-paladin-hoj-caster", "TNK-PAL-HOJ"},
		{"tank-bear-bash-caster", "TNK-DRU-BASH"},
	} {
		spell := map[string]string{"TNK-PAL-HOJ": "Hammer of Justice", "TNK-DRU-BASH": "Bash"}[pair[1]]
		var casts, answers []any
		for i := range 3 {
			casts = append(casts, tndEvent(float64(10+70*i), "cast_start", tndMob, tndPlayer, tndLightningBolt))
			answers = append(answers, tndEvent(11.5+70*float64(i), "cast", tndBot, tndMob, tndSid(t, spell)))
		}
		assertEqual(t, "PASS", tndVerdict(t, pair[0], pair[1], append(append([]any{}, casts...), answers...), samples))
		assertEqual(t, "FAIL", tndVerdict(t, pair[0], pair[1], casts, samples))
		assertEqual(t, "INCONCLUSIVE", tndVerdict(t, pair[0], pair[1], instants[:3], samples))
	}
}

func TestTankRulesTest_NoTauntControls(t *testing.T) {
	// a taunt on a mob the player tank holds fails, even when the bot fought; a taunt on a mob
	// the player does not hold passes
	for _, row := range [][4]string{
		{"tank-warrior-no-taunt-tank-player", "TNK-WAR-NO-TAUNT", "Taunt", "TNK-WAR-NO-TAUNT-FOUGHT"},
		{"tank-bear-no-growl-tank-player", "TNK-DRU-NO-GROWL", "Growl", "TNK-DRU-NO-GROWL-FOUGHT"},
		{"tank-paladin-no-taunt-tank-player", "TNK-PAL-NO-TAUNT", "Hand of Reckoning", "TNK-PAL-NO-TAUNT-FOUGHT"},
	} {
		tid, rid, spell, fought := row[0], row[1], row[2], row[3]
		held := []any{tndEvent(12, "cast", tndBot, tndMob, tndSid(t, spell))}
		assertEqual(t, "FAIL", tndVerdict(t, tid, rid, held, tndSeries(200, []any{int64(71)}, nil, nil)))
		var swings []any
		for i := range 5 {
			swings = append(swings, tndEvent(float64(10+i), "cast", tndBot, tndMob, int64(772)))
		}
		assertEqual(t, "PASS", tndVerdict(t, tid, fought, swings, tndSeries(200, []any{int64(71)}, nil, nil)))
		assertEqual(t, "FAIL", tndVerdict(t, tid, fought, swings[:4], tndSeries(200, []any{int64(71)}, nil, nil)))
		idle := []any{tndEvent(12, "cast", tndBot, tndMob, tndSid(t, spell))}
		var onBot []any
		for _, row := range tndSeries(200, []any{int64(71)}, nil, nil) {
			sample := cloneRow(obj(t, row))
			if pyjson.Str(sample["guid"]) == tndMob {
				sample["target"] = tndBot
			}
			onBot = append(onBot, sample)
		}
		assertEqual(t, "PASS", tndVerdict(t, tid, rid, idle, onBot))
	}
	// the warrior control counts all three taunts, not only Taunt
	for _, spell := range []string{"Mocking Blow", "Challenging Shout"} {
		held := []any{tndEvent(12, "cast", tndBot, tndMob, tndSid(t, spell))}
		assertEqual(t, "FAIL", tndVerdict(t, "tank-warrior-no-taunt-tank-player", "TNK-WAR-NO-TAUNT",
			held, tndSeries(200, []any{int64(71)}, nil, nil)))
	}
}

func TestTankRulesTest_PaladinTauntAndHolds(t *testing.T) {
	tid := "tank-paladin-taunt-off-healer"
	tndLatency(t, tid, "TNK-PAL-TAUNT", tndSid(t, "Hand of Reckoning"), 20, 2)
	var good, bad []any
	for i := range 20 {
		good = append(good, tndEvent(float64(10+i), "damage", tndMob, tndBot, nil))
	}
	for i := range 5 {
		good = append(good, tndEvent(float64(40+i), "damage", tndMob, tndPlayer, nil))
	}
	for i := range 10 {
		bad = append(bad, tndEvent(float64(10+i), "damage", tndMob, tndBot, nil))
	}
	for i := range 15 {
		bad = append(bad, tndEvent(float64(40+i), "damage", tndMob, tndPlayer, nil))
	}
	assertEqual(t, "PASS", tndVerdict(t, "tank-bear-hold-single", "TNK-DRU-HOLD-SINGLE", good, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, "tank-bear-hold-single", "TNK-DRU-HOLD-SINGLE", bad, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "PASS", tndVerdict(t, "tank-bear-hold-three", "TNK-DRU-HOLD-THREE", good, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, "tank-bear-hold-three", "TNK-DRU-HOLD-THREE", bad, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "PASS", tndVerdict(t, "tank-paladin-hold-three", "TNK-PAL-HOLD-THREE", good, tndSeries(200, nil, nil, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, "tank-paladin-hold-three", "TNK-PAL-HOLD-THREE", bad, tndSeries(200, nil, nil, nil)))
}

func TestTankRulesTest_AddPickup(t *testing.T) {
	// the add-only gate (t_from 55) drops the first mob's hits: early triggers are
	// INCONCLUSIVE, answered late triggers pass, unanswered late ones fail
	for _, row := range [][3]string{
		{"tank-warrior-add-pickup", "TNK-WAR-ADD-PICKUP", "Taunt"},
		{"tank-bear-add-pickup", "TNK-DRU-ADD-PICKUP", "Growl"},
		{"tank-paladin-add-pickup", "TNK-PAL-ADD-PICKUP", "Hand of Reckoning"},
	} {
		tid, rid, spell := row[0], row[1], row[2]
		samples := tndSeries(200, nil, nil, nil)
		early := []any{
			tndEvent(10, "damage", tndMob, tndPlayer, nil),
			tndEvent(11, "cast", tndBot, tndMob, tndSid(t, spell)),
		}
		assertEqual(t, "INCONCLUSIVE", tndVerdict(t, tid, rid, early, samples))
		times := []float64{60, 80, 100}
		var hits, answers []any
		for _, ts := range times {
			dst := tndPlayer
			if ts >= 90 {
				dst = tndExtra1
			}
			hits = append(hits, tndEvent(ts, "damage", tndMob, dst, nil))
			answers = append(answers, tndEvent(ts+2, "cast", tndBot, tndMob, tndSid(t, spell)))
		}
		got := tndScore(t, tid, append(append([]any{}, hits...), answers...), samples, 0)[rid]
		assertEqual(t, []any{"PASS", int64(3)}, []any{got["verdict"], got["n"]})
		assertEqual(t, "FAIL", tndVerdict(t, tid, rid, hits, samples))
	}
}

// ---- DPS rules --------------------------------------------------------------

func TestDpsRulesTest_ThreatCap(t *testing.T) {
	tid := "dps-mage-threat-cap"
	samples := tndSeries(200, []any{int64(71)}, nil, nil)
	var nukes []any
	for i := range 15 {
		nukes = append(nukes, tndEvent(float64(10+5*i), "cast", tndBot, tndMob, int64(133)))
	}
	assertEqual(t, "PASS", tndVerdict(t, tid, "DPS-MAG-THREAT-NO-AGGRO", nukes, samples))
	pulled := append(append([]any{}, nukes...), tndEvent(60, "damage", tndMob, tndBot, nil))
	assertEqual(t, "FAIL", tndVerdict(t, tid, "DPS-MAG-THREAT-NO-AGGRO", pulled, samples))
	// DPS-MAG-THREAT-DAMAGE is a rate per minute of mob-alive time (>= 8), see DECISIONS.md
	mobAlive := func(alive int) []any {
		out := append([]any{}, samples...)
		mobGUID := strings.ToLower(tndMob)
		for ts := range 201 {
			hp := int64(500)
			if ts >= alive {
				hp = 0
			}
			out = append(out, map[string]any{
				"t": float64(ts), "unit": "mob:" + mobGUID, "guid": mobGUID,
				"hp": hp, "max_hp": int64(2000), "in_combat": ts < alive, "auras": []any{},
			})
		}
		return out
	}
	var quick []any
	for i := range 12 {
		quick = append(quick, tndEvent(2+3.5*float64(i), "cast", tndBot, tndMob, int64(133)))
	}
	assertEqual(t, "PASS", tndVerdict(t, tid, "DPS-MAG-THREAT-DAMAGE", quick, mobAlive(50)))
	// the same 12 nukes against a mob that lives the whole repeat: 3.6 a minute, the back-off held
	assertEqual(t, "FAIL", tndVerdict(t, tid, "DPS-MAG-THREAT-DAMAGE", quick, mobAlive(200)))
	// a mob that lived under 45 s (the step-3 reps of sweep-1b: about 20 s) says nothing
	assertEqual(t, "INCONCLUSIVE", tndVerdict(t, tid, "DPS-MAG-THREAT-DAMAGE", quick[:6], mobAlive(22)))
	// wand shots are not nukes: an only-wanding mage fails the precondition
	var wand []any
	for i := range 40 {
		wand = append(wand, tndEvent(float64(10+2*i), "cast", tndBot, tndMob, tndSid(t, "Shoot")))
	}
	assertEqual(t, "FAIL", tndVerdict(t, tid, "DPS-MAG-THREAT-DAMAGE", wand, mobAlive(200)))
	// with the mage out of combat there is no opportunity: the count rule is not scored (the
	// rule's opportunity is the MAGE's own combat time: a bot tank leaves the player out, bench
	// fix round 2)
	var idle []any
	for _, row := range samples {
		if pyjson.Str(obj(t, row)["guid"]) != tndBot {
			idle = append(idle, row)
		}
	}
	for ts := range 201 {
		idle = append(idle, tndSampleRow(float64(ts), tndBot, nil, "", false, 600))
	}
	assertEqual(t, "INCONCLUSIVE", tndVerdict(t, tid, "DPS-MAG-THREAT-NO-AGGRO", nukes, idle))
}

func tndInterrupt(t *testing.T, tid, rid string, spellID int64, gap float64, pooled bool) ([]any, []any) {
	t.Helper()
	samples := tndSeries(200, []any{int64(71)}, nil, nil)
	var casts, answers, instants []any
	for i := range 5 {
		casts = append(casts, tndEvent(10+gap*float64(i), "cast_start", tndMob, tndPlayer, tndLightningBolt))
		answers = append(answers, tndEvent(11.5+gap*float64(i), "cast", tndBot, tndMob, spellID))
		instants = append(instants, tndEvent(10+gap*float64(i), "cast_start", tndMob, tndPlayer, int64(355)))
	}
	assertEqual(t, "PASS", tndVerdict(t, tid, rid, append(append([]any{}, casts...), answers...), samples))
	assertEqual(t, "FAIL", tndVerdict(t, tid, rid, casts, samples))
	// an instant (not interruptible) cast is no trigger: no triggers at all -> INCONCLUSIVE
	assertEqual(t, "INCONCLUSIVE", tndVerdict(t, tid, rid, instants, samples))
	// fewer than min_n 5 triggers is INCONCLUSIVE, not a pass; a pooled rule skips the
	// per-repeat guard (min_n reads the pooled n), so 4 answered of 4 passes the repeat
	want := "INCONCLUSIVE"
	if pooled {
		want = "PASS"
	}
	assertEqual(t, want, tndVerdict(t, tid, rid, append(append([]any{}, casts[:4]...), answers[:4]...), samples))
	return casts, samples
}

func TestDpsRulesTest_Kick(t *testing.T) {
	casts, samples := tndInterrupt(t, "dps-rogue-kick-caster", "DPS-ROG-KICK", tndSid(t, "Kick"), 20, false)
	// Kidney Shot does not answer: the events cannot show that it stopped the cast
	var kidney []any
	for i := range 5 {
		kidney = append(kidney, tndEvent(11.5+20*float64(i), "cast", tndBot, tndMob, tndSid(t, "Kidney Shot")))
	}
	assertEqual(t, "FAIL", tndVerdict(t, "dps-rogue-kick-caster", "DPS-ROG-KICK",
		append(append([]any{}, casts...), kidney...), samples))
}

func TestDpsRulesTest_Counterspell(t *testing.T) {
	tndInterrupt(t, "dps-mage-counterspell-caster", "DPS-MAG-COUNTERSPELL", 2139, 40, true)
}

func TestDpsRulesTest_InterruptStancePreconditions(t *testing.T) {
	for _, pair := range [][2]string{
		{"dps-rogue-kick-caster", "DPS-ROG-KICK-STANCE"},
		{"dps-mage-counterspell-caster", "DPS-MAG-CS-STANCE"},
	} {
		assertEqual(t, "PASS", tndVerdict(t, pair[0], pair[1], nil, tndSeries(200, []any{int64(71)}, nil, nil)))
		assertEqual(t, "FAIL", tndVerdict(t, pair[0], pair[1], nil, tndSeries(200, []any{int64(2457)}, nil, nil)))
	}
	// threat-cap runs Battle Stance on a dps-kit player under a hired bot tank (P1)
	assertEqual(t, "PASS", tndVerdict(t, "dps-mage-threat-cap", "DPS-MAG-THREAT-STANCE", nil,
		tndSeries(200, []any{int64(2457)}, nil, nil)))
	assertEqual(t, "FAIL", tndVerdict(t, "dps-mage-threat-cap", "DPS-MAG-THREAT-STANCE", nil,
		tndSeries(200, []any{int64(71)}, nil, nil)))
}

func TestDpsRulesTest_CCRespect(t *testing.T) {
	tid := "dps-mage-cc-respect"
	sheeped := func(ts float64) []any {
		if ts >= 20 && ts <= 70 {
			return []any{tndPolymorphR3}
		}
		return []any{}
	}
	samples := tndSeries(200, []any{int64(71)}, nil, sheeped)
	poly := []any{tndEvent(18, "cast", tndBot, tndMarked, tndPolymorphR3)}
	// damage on the marked mob before the sheep, or after it ends, is not a break
	before := []any{
		tndEvent(10, "damage", tndBot, tndMarked, int64(116)),
		tndEvent(90, "damage", tndBot, tndMarked, int64(116)),
	}
	assertEqual(t, "PASS", tndVerdict(t, tid, "DPS-MAG-CC-RESPECT", append(append([]any{}, poly...), before...), samples))
	// a Blizzard tick on the sheep
	broke := append(append([]any{}, poly...), tndEvent(40, "damage", tndBot, tndMarked, int64(10)))
	assertEqual(t, "FAIL", tndVerdict(t, tid, "DPS-MAG-CC-RESPECT", broke, samples))
	assertEqual(t, "PASS", tndVerdict(t, tid, "DPS-MAG-CC-POLYMORPH", poly, samples))
	assertEqual(t, "FAIL", tndVerdict(t, tid, "DPS-MAG-CC-POLYMORPH", nil, samples))
}
