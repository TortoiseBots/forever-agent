package classes

// Translated from the Python bench's test_rules_healers.py (tortoise-trials).

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/data"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
	"github.com/pjerra/forever-agent/internal/trials/spells"
)

const (
	hlrPlayer = "0x0000000000000001"
	hlrBot    = "0x0000000000000002"
	hlrMob    = "0xf13000000000abcd"
)

var hlrRepeats4 = map[string]bool{
	"priest-holy-pws-def-arms": true, "priest-holy-pws-def-arms-2tank": true,
	"priest-holy-pws-def-arms-ooc": true, "priest-holy-pws-bear-bal": true,
	"priest-holy-pws-bear-bal-2tank": true, "priest-holy-heal-latency": true,
	"paladin-holy-heal-latency": true,
}

// hlrIDs is the Python module's IDS after the appended two-tank / live3 / ooc-hurt block ran
// (module-level code runs before any test).
var hlrIDs = []string{
	"priest-holy-pws-def-arms", "priest-holy-pws-bear-bal", "priest-holy-pws-battle-control",
	"priest-holy-pws-forced-role", "priest-holy-heal-latency", "priest-holy-dispel-magic",
	"priest-holy-fort-ooc", "priest-shadow-form-prepull", "paladin-holy-heal-latency",
	"paladin-blessing-ooc", "druid-resto-heal-latency",
	// the appended block widens the shared tables
	"priest-holy-pws-def-arms-2tank", "priest-holy-pws-bear-bal-2tank",
	"priest-holy-pws-bot-warrior-control",
	"priest-holy-pws-def-arms-ooc", "priest-holy-pws-battle-ooc-control",
}

// hlrExtraFile ships in the pinned snapshot but has no entry in the Python IDS list; the
// Python test_all_healer_trials_validate is stale and fails on that line in the reference.
const hlrExtraFile = "priest-shadow-inform-heal-solo-healer"

// rule id -> expect_today (brief table; solo-tank PWS trials PASS: AiFactory.cpp:597-611 reads
// the stance unless HasOtherTank).
var hlrExpect = map[string]string{
	"PRI-PWS-DEF-ARMS": "PASS", "PRI-PWS-BEAR-BAL": "PASS", "PRI-PWS-BATTLE-CONTROL": "PASS",
	"PRI-PWS-FORCED-ROLE": "PASS", "PRI-HEAL-LATENCY": "PASS", "PRI-DISPEL-MAGIC": "PASS",
	"PRI-FORT-OOC": "PASS", "PRI-SHADOWFORM-PREPULL": "FAIL", "PAL-HOLY-HEAL-LATENCY": "PASS",
	"PAL-BLESSING-OOC": "PASS", "DRU-RESTO-HEAL-LATENCY": "PASS",
	// #1553 is fixed (TortoiseBots 5f57e84f2, PR #1557): the two second-tank trials are
	// regression guards now
	"PRI-PWS-DEF-ARMS-2TANK": "PASS", "PRI-PWS-BEAR-BAL-2TANK": "PASS",
	"PRI-PWS-BOT-WARRIOR-CONTROL": "PASS",
	"PRI-PWS-DEF-ARMS-OOC":        "FAIL",
}

func hlrLoad(t *testing.T, tid string) scenario.Trial {
	t.Helper()
	trial, err := scenario.LoadTrial("healers/" + tid + ".json")
	if err != nil {
		t.Fatalf("load %s: %v", tid, err)
	}
	return trial
}

func hlrRuleOf(t *testing.T, trial map[string]any, rid string) map[string]any {
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

func hlrRulesOf(t *testing.T, trial map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ruleVal := range list(t, trial["rules"]) {
		out = append(out, obj(t, ruleVal))
	}
	return out
}

// hlrSid is sorted(spells.ids_for(name))[rank].
func hlrSid(t *testing.T, name string, rank int) int64 {
	t.Helper()
	ids := spells.IDsFor(name)
	sorted := make([]int64, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if rank >= len(sorted) {
		t.Fatalf("spell %q has %d ranks, asked for %d", name, len(sorted), rank)
	}
	return sorted[rank]
}

func hlrSpellsByID(t *testing.T) map[string]any {
	t.Helper()
	doc, err := pyjson.Unmarshal(data.Read("spells.json"))
	if err != nil {
		t.Fatalf("spells.json: %v", err)
	}
	return obj(t, obj(t, doc)["by_id"])
}

// ---- synthetic repeat -------------------------------------------------------

func hlrSampleRow(ts float64, guid string, hp, maxHP int64, auras []any, combat bool) map[string]any {
	if auras == nil {
		auras = []any{}
	}
	return map[string]any{
		"t": ts, "wall": nil, "unit": "x", "guid": guid, "hp": hp, "max_hp": maxHP,
		"power": int64(500), "max_power": int64(500), "auras": auras, "casting": nil,
		"in_combat": combat, "target": "", "x": 0.0, "y": 0.0, "z": 0.0,
	}
}

func hlrEvent(ts float64, kind, src, dst string, spellID int64, hp any) map[string]any {
	guids := map[string]string{"player": hlrPlayer, "bot": hlrBot}
	return map[string]any{
		"t": ts, "wall": nil, "kind": kind, "src": src, "src_guid": guids[src],
		"dst": dst, "dst_guid": guids[dst], "src_unit": "$" + src, "dst_unit": "$" + dst,
		"spell_id": spellID, "spell": spells.NameFor(spellID), "amount": nil,
		"dst_hp_pct": hp, "src_role": src, "dst_role": dst, "origin": "ta",
	}
}

type hlrSeriesOpts struct {
	PlayerHP    func(float64) int64
	PlayerAuras []any
	BotAuras    []any
	OOC         bool // Python combat=False
	BotPower    any  // int64 or func(float64) any; nil -> 500
	BotDead     func(float64) bool
}

func hlrSeries(secs int, o hlrSeriesOpts) []any {
	var out []any
	playerHP := func(float64) int64 { return 600 }
	if o.PlayerHP != nil {
		playerHP = o.PlayerHP
	}
	var power func(float64) any
	switch val := o.BotPower.(type) {
	case nil:
		power = func(float64) any { return int64(500) }
	case func(float64) any:
		power = val
	default:
		power = func(float64) any { return val }
	}
	for ts := range secs + 1 {
		tf := float64(ts)
		out = append(out, hlrSampleRow(tf, hlrPlayer, playerHP(tf), int64(1000), o.PlayerAuras, !o.OOC))
		row := hlrSampleRow(tf, hlrBot, int64(1000), int64(1000), o.BotAuras, !o.OOC)
		row["power"] = power(tf)
		if o.BotDead != nil && o.BotDead(tf) {
			row["dead"] = true
		}
		out = append(out, row)
	}
	return out
}

type hlrScoreOpts struct {
	Secs  int
	Setup map[string]any
}

// hlrScore scores one synthetic repeat of trial tid; returns {rule id: entry} plus the output.
func hlrScore(t *testing.T, tid string, events, samples []any, o hlrScoreOpts) (map[string]map[string]any, map[string]any) {
	t.Helper()
	if o.Secs == 0 {
		o.Secs = 200
	}
	rep := filepath.Join(t.TempDir(), "run", tid, "rep1")
	doc := map[string]any{
		"ok":     true,
		"guids":  map[string]any{"player": hlrPlayer, "bot": hlrBot, "mobs": []any{hlrMob}},
		"config": map[string]any{"critical": int64(20), "low": int64(50), "medium": int64(70)},
	}
	for key, val := range o.Setup {
		doc[key] = val
	}
	writeJSON(t, filepath.Join(rep, "setup.json"), doc)
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
		map[string]any{"kind": "fight_end", "t": int64(o.Secs), "ended": "duration"},
	})
	out := score.ScoreRep(rep, hlrLoad(t, tid), nil)
	rules := map[string]map[string]any{}
	for _, entryVal := range list(t, out["rules"]) {
		entry := obj(t, entryVal)
		rules[pyjson.Str(entry["id"])] = entry
	}
	return rules, out
}

func hlrVerdict(t *testing.T, tid, rid string, events, samples []any, o hlrScoreOpts) map[string]any {
	t.Helper()
	rules, _ := hlrScore(t, tid, events, samples, o)
	entry, ok := rules[rid]
	if !ok {
		t.Fatalf("rule %s missing from %s", rid, tid)
	}
	return entry
}

func hlrV(t *testing.T, tid, rid string, events, samples []any) map[string]any {
	return hlrVerdict(t, tid, rid, events, samples, hlrScoreOpts{})
}

func hlrValues(values any) []any {
	out, _ := pyjson.List(values)
	return out
}

func hlrHas(values any, want string) bool {
	for _, val := range hlrValues(values) {
		if pyjson.Str(val) == want {
			return true
		}
	}
	return false
}

func hlrKN(t *testing.T, entry map[string]any) (int64, int64) {
	t.Helper()
	k, ok := pyjson.Int64(entry["k"])
	if !ok {
		t.Fatalf("entry k is not an int: %v", entry["k"])
	}
	n, ok := pyjson.Int64(entry["n"])
	if !ok {
		t.Fatalf("entry n is not an int: %v", entry["n"])
	}
	return k, n
}

func hlrCheck(t *testing.T, entry map[string]any, wantVerdict string, wantValue any) {
	t.Helper()
	assertEqual(t, []any{wantVerdict, wantValue}, []any{pyjson.Str(entry["verdict"]), entry["value"]})
}

// ---- trial files ------------------------------------------------------------

func TestTrialFilesTest_AllHealerTrialsValidate(t *testing.T) {
	var files []string
	for _, path := range scenario.TrialFiles() {
		if strings.HasPrefix(path, "healers/") {
			files = append(files, path)
		}
	}
	sort.Strings(files)
	var names []string
	for _, path := range files {
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(path, "healers/"), ".json"))
	}
	want := append(append([]string{}, hlrIDs...), hlrExtraFile)
	// Python sorts the ids by "id + '.json'", which is the same order as the sorted paths.
	sort.Slice(want, func(i, j int) bool { return want[i]+".json" < want[j]+".json" })
	assertEqual(t, want, names)
	for _, path := range files {
		trial, err := scenario.LoadTrial(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if errs := scenario.Validate(trial); len(errs) != 0 {
			t.Fatalf("%s: %v", path, errs)
		}
		base := strings.TrimSuffix(filepath.Base(path), ".json")
		assertEqual(t, base, trial["id"])
	}
}

func TestTrialFilesTest_RuleIdsUnique(t *testing.T) {
	seen := map[string]string{}
	for _, tid := range hlrIDs {
		for _, rule := range hlrRulesOf(t, hlrLoad(t, tid)) {
			id := pyjson.Str(rule["id"])
			if other, ok := seen[id]; ok {
				t.Fatalf("%s also in %s", id, other)
			}
			seen[id] = tid
		}
	}
}

func TestTrialFilesTest_ExpectTodayValues(t *testing.T) {
	for _, tid := range hlrIDs {
		for _, rule := range hlrRulesOf(t, hlrLoad(t, tid)) {
			id := pyjson.Str(rule["id"])
			want := "PASS"
			if expected, ok := hlrExpect[id]; ok {
				want = expected
			}
			assertEqual(t, want, rule["expect_today"])
			if !pyjson.Truthy(rule["why_expect"]) {
				t.Fatalf("%s: why_expect is empty", id)
			}
		}
	}
}

func hlrExpandUser(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func TestTrialFilesTest_SourcesPointAtRealLines(t *testing.T) {
	botSrc := hlrExpandUser(os.Getenv("TRIALS_BOT_SRC"))
	if botSrc == "" {
		botSrc = "/nonexistent"
	}
	if info, err := os.Stat(botSrc); err != nil || !info.IsDir() {
		t.Skip("bot source tree not present")
	}
	pat := regexp.MustCompile(`((?:ai|commands)/[\w/.\-]+\.(?:h|cpp)):([0-9,\-]+)`)
	checked := 0
	for _, tid := range hlrIDs {
		for _, rule := range hlrRulesOf(t, hlrLoad(t, tid)) {
			id := pyjson.Str(rule["id"])
			source := pyjson.Str(rule["source"])
			if strings.HasPrefix(source, "precondition") {
				continue
			}
			found := pat.FindAllStringSubmatch(source, -1)
			if len(found) == 0 {
				t.Fatalf("%s: no code citation in %q", id, source)
			}
			for _, match := range found {
				full := filepath.Join(botSrc, match[1])
				if _, err := os.Stat(full); err != nil {
					t.Fatalf("%s: %s: %v", id, match[1], err)
				}
				content, err := os.ReadFile(full)
				if err != nil {
					t.Fatalf("%s: %s: %v", id, match[1], err)
				}
				count := strings.Count(string(content), "\n")
				if len(content) > 0 && !strings.HasSuffix(string(content), "\n") {
					count++
				}
				for _, part := range strings.Split(match[2], ",") {
					pieces := strings.Split(part, "-")
					last, err := strconv.Atoi(pieces[len(pieces)-1])
					if err != nil {
						t.Fatalf("%s: bad line range %q", id, part)
					}
					if last > count {
						t.Fatalf("%s: %s:%s beyond %d lines", id, match[1], part, count)
					}
					checked++
				}
			}
		}
	}
	if checked <= 20 {
		t.Fatalf("checked %d citations, want > 20", checked)
	}
}

func TestTrialFilesTest_HealerTrialsCarryTheCommonDecisions(t *testing.T) {
	for _, tid := range hlrIDs {
		trial := hlrLoad(t, tid)
		player := obj(t, trial["player"])
		bot := obj(t, trial["bot"])
		assertEqual(t, int64(40), player["level"])
		assertEqual(t, int64(40), bot["level"])
		situation := obj(t, trial["situation"])
		kind := pyjson.Str(situation["kind"])
		hurt := kind == "ooc_hurt" // window = ooc_wait_s 150, one stance, no mobs
		ooc := kind == "ooc_buffs" || kind == "ooc_hurt"
		wantDuration := []any{int64(180), int64(120)}
		if kind == "ooc_buffs" {
			wantDuration = []any{int64(60), int64(45)}
		} else if hurt {
			wantDuration = []any{int64(150), int64(120)}
		}
		assertEqual(t, wantDuration, []any{trial["duration_s"], trial["min_valid_s"]})
		// the four #1553 trials run 4 repeats so one flaky rep leaves more than one data point
		wantRepeats := int64(2)
		if hlrRepeats4[tid] {
			wantRepeats = 4
		}
		assertEqual(t, wantRepeats, trial["repeats"])
		if !hlrHas(trial["setup_checks"], "player_talents") {
			t.Fatalf("%s: player_talents setup check missing", tid)
		}
		if got, want := hlrHas(trial["setup_checks"], "player_stance"), hurt || !ooc; got != want {
			t.Fatalf("%s: player_stance check present=%v want %v", tid, got, want)
		}
		if got, want := hlrHas(trial["setup_checks"], "mobs_ready"), !ooc; got != want {
			t.Fatalf("%s: mobs_ready check present=%v want %v", tid, got, want)
		}
		if pyjson.Str(bot["role"]) == "healer" {
			assertEqual(t, []any{map[string]any{"name": "Sweet Nectar", "count": int64(20)}}, bot["items"])
			if !hlrHas(trial["setup_checks"], "bot_items") {
				t.Fatalf("%s: bot_items setup check missing", tid)
			}
		}
		if !ooc {
			// lighter fights (DECISIONS.md): paladin-heal-latency at -4, the bear-bal pair at -4
			// and shadow-form-prepull at -1 after the 11-11 bench round; everything else at +1
			deltas := map[string]int64{
				"paladin-holy-heal-latency": -4, "priest-holy-pws-bear-bal": -4,
				"priest-holy-pws-bear-bal-2tank": -4, "priest-shadow-form-prepull": -1,
			}
			want := int64(1)
			if delta, ok := deltas[tid]; ok {
				want = delta
			}
			assertEqual(t, want, obj(t, situation["mobs"])["level_delta"])
		}
	}
}

// ---- #1553 setup contract ---------------------------------------------------

func hlrReadDBC(t *testing.T, name string) [][]int64 {
	t.Helper()
	dbcDir := hlrExpandUser(os.Getenv("TRIALS_DBC_DIR"))
	if dbcDir == "" {
		dbcDir = "/nonexistent"
	}
	raw, err := os.ReadFile(filepath.Join(dbcDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if len(raw) < 20 {
		t.Fatalf("%s: short header", name)
	}
	rec := int(binary.LittleEndian.Uint32(raw[4:8]))
	fields := int(binary.LittleEndian.Uint32(raw[8:12]))
	size := int(binary.LittleEndian.Uint32(raw[12:16]))
	out := make([][]int64, 0, rec)
	for i := range rec {
		start := 20 + i*size
		row := make([]int64, fields)
		for f := range fields {
			off := start + f*4
			if off+4 > len(raw) {
				t.Fatalf("%s: record %d field %d out of range", name, i, f)
			}
			row[f] = int64(int32(binary.LittleEndian.Uint32(raw[off : off+4])))
		}
		out = append(out, row)
	}
	return out
}

// hlrPointsPerTab is TB's GetPlayerSpecTabs: ranks held per talent tab page (HasSpell of a
// rank spell).
func hlrPointsPerTab(t *testing.T, spellIDs map[int64]bool, classID int64) map[int64]int64 {
	t.Helper()
	tabs := map[int64]int64{}
	for _, row := range hlrReadDBC(t, "TalentTab.dbc") {
		if row[12]&(1<<(classID-1)) == 0 {
			continue
		}
		if row[0] == 41 {
			tabs[row[0]] = 1
		} else {
			tabs[row[0]] = row[13]
		}
	}
	pts := map[int64]int64{0: 0, 1: 0, 2: 0}
	for _, tal := range hlrReadDBC(t, "Talent.dbc") {
		tab, ok := tabs[tal[1]]
		if !ok {
			continue
		}
		var rank int64
		for r := range 5 {
			spellID := tal[4+r]
			if spellID != 0 && spellIDs[spellID] && int64(r+1) > rank {
				rank = int64(r + 1)
			}
		}
		pts[tab] += rank
	}
	return pts
}

// hlrSpecTab is AiFactory::GetPlayerSpecTab: first tab wins ties.
func hlrSpecTab(pts map[int64]int64) int64 {
	tab, best := int64(-1), int64(0)
	for i := range int64(3) {
		if tab == -1 || best < pts[i] {
			tab, best = i, pts[i]
		}
	}
	return tab
}

func hlrTalentIDs(t *testing.T, trial map[string]any) []int64 {
	t.Helper()
	var out []int64
	for _, idVal := range list(t, obj(t, obj(t, trial["player"])["talents"])["spells"]) {
		id, ok := pyjson.Int64(idVal)
		if !ok {
			t.Fatalf("talent id is not an int: %v", idVal)
		}
		out = append(out, id)
	}
	return out
}

func hlrTalentSet(t *testing.T, trial map[string]any) map[int64]bool {
	t.Helper()
	out := map[int64]bool{}
	for _, id := range hlrTalentIDs(t, trial) {
		out[id] = true
	}
	return out
}

func TestSetupContractTest_ArmsTalentIDsComeFromTheSpellTable(t *testing.T) {
	armsTrials := []string{"priest-holy-pws-def-arms", "priest-holy-pws-battle-control",
		"priest-holy-pws-forced-role"}
	all := append(append([]string{}, armsTrials...), hlrIDs...)
	byID := hlrSpellsByID(t)
	for _, tid := range all {
		trial := hlrLoad(t, tid)
		if pyjson.Str(obj(t, trial["player"])["class"]) != "warrior" {
			continue
		}
		ids := hlrTalentIDs(t, trial)
		talents := obj(t, obj(t, trial["player"])["talents"])
		assertEqual(t, "arms", talents["tree"])
		var names []any
		for _, id := range ids {
			names = append(names, spells.NameFor(id))
		}
		sort.Slice(names, func(i, j int) bool {
			return pyjson.Str(names[i]) < pyjson.Str(names[j])
		})
		assertEqual(t, []any{"Deflection", "Improved Rend"}, names)
		for _, id := range ids {
			info := obj(t, byID[strconv.FormatInt(id, 10)])
			assertEqual(t, "Rank 1", info["rank"])
		}
	}
}

func TestSetupContractTest_ArmsTrialsHaveArmsPointsAndNoProtectionPoints(t *testing.T) {
	dbcDir := hlrExpandUser(os.Getenv("TRIALS_DBC_DIR"))
	if dbcDir == "" {
		dbcDir = "/nonexistent"
	}
	if _, err := os.Stat(filepath.Join(dbcDir, "Talent.dbc")); err != nil {
		t.Skip("Talent.dbc not present")
	}
	for _, tid := range hlrIDs {
		trial := hlrLoad(t, tid)
		if pyjson.Str(obj(t, trial["player"])["class"]) != "warrior" {
			continue
		}
		pts := hlrPointsPerTab(t, hlrTalentSet(t, trial), 1)
		if pts[2] != 0 {
			t.Fatalf("%s has Protection points %v", tid, pts)
		}
		if pts[0] <= 0 {
			t.Fatalf("%s has no Arms points: %v", tid, pts)
		}
		if tab := hlrSpecTab(pts); tab != 0 {
			t.Fatalf("%s would read tab %d (%v)", tid, tab, pts)
		}
	}
}

func TestSetupContractTest_BalanceDruidHasBalancePointsOnly(t *testing.T) {
	dbcDir := hlrExpandUser(os.Getenv("TRIALS_DBC_DIR"))
	if dbcDir == "" {
		dbcDir = "/nonexistent"
	}
	if _, err := os.Stat(filepath.Join(dbcDir, "Talent.dbc")); err != nil {
		t.Skip("Talent.dbc not present")
	}
	trial := hlrLoad(t, "priest-holy-pws-bear-bal")
	var names []any
	for _, id := range hlrTalentIDs(t, trial) {
		names = append(names, strings.ReplaceAll(spells.NameFor(id), "''", "'"))
	}
	sort.Slice(names, func(i, j int) bool { return pyjson.Str(names[i]) < pyjson.Str(names[j]) })
	assertEqual(t, []any{"Improved Wrath", "Nature's Grasp"}, names)
	pts := hlrPointsPerTab(t, hlrTalentSet(t, trial), 11)
	assertEqual(t, []any{int64(2), int64(0), int64(0)}, []any{pts[0], pts[1], pts[2]})
}

func TestSetupContractTest_StanceAuraIsProvenAndHeld(t *testing.T) {
	want := map[string][2]any{
		"priest-holy-pws-def-arms":       {int64(71), "Defensive Stance"},
		"priest-holy-pws-bear-bal":       {int64(9634), "Dire Bear Form"},
		"priest-holy-pws-battle-control": {int64(2457), "Battle Stance"},
		"priest-holy-pws-forced-role":    {int64(71), "Defensive Stance"},
	}
	for tid, pair := range want {
		aura := pair[0].(int64)
		trial := hlrLoad(t, tid)
		assertEqual(t, aura, obj(t, trial["player"])["stance"])
		assertEqual(t, pair[1], spells.NameFor(aura))
		if !hlrHas(trial["setup_checks"], "player_stance") {
			t.Fatalf("%s: player_stance setup check missing", tid)
		}
		var held []map[string]any
		for _, rule := range hlrRulesOf(t, trial) {
			if pyjson.Str(obj(t, rule["check"])["metric"]) == "uptime" {
				held = append(held, rule)
			}
		}
		if len(held) != 1 {
			t.Fatalf("%s: %d uptime rules, want 1", tid, len(held))
		}
		check := obj(t, held[0]["check"])
		assertEqual(t, []any{aura}, check["aura"])
		assertEqual(t, "$player", check["unit"])
	}
}

func TestSetupContractTest_PlayerIsNotForcedUnlessTheTrialSaysSo(t *testing.T) {
	for _, tid := range append([]string{"priest-holy-pws-def-arms", "priest-holy-pws-battle-control",
		"priest-holy-pws-forced-role"}, "priest-holy-pws-bear-bal") {
		var want any
		if strings.HasSuffix(tid, "forced-role") {
			want = "tank"
		}
		assertEqual(t, want, obj(t, hlrLoad(t, tid)["player"])["forced_role"])
		if !hlrHas(hlrLoad(t, tid)["setup_checks"], "player_forced_role") {
			t.Fatalf("%s: player_forced_role setup check missing", tid)
		}
	}
}

func TestSetupContractTest_BotIsAHolyPriestHealer(t *testing.T) {
	for _, tid := range hlrIDs[:7] {
		trial := hlrLoad(t, tid)
		bot := obj(t, trial["bot"])
		assertEqual(t, []any{"priest", "holy", "healer"},
			[]any{bot["class"], bot["spec"], bot["role"]})
		for _, check := range []string{"bot_present", "bot_level", "bot_role", "bot_spec"} {
			if !hlrHas(trial["setup_checks"], check) {
				t.Fatalf("%s: %s setup check missing", tid, check)
			}
		}
	}
}

func TestSetupContractTest_TalentlessPlayerIsNotAValidSetup(t *testing.T) {
	// AiFactory::GetPlayerSpecTab: a level>=10 warrior with 0 talent points reads tab 2 = tank
	for _, talents := range []map[string]any{
		{"tree": "arms", "spells": []any{}},
		{"tree": "none", "spells": []any{}},
	} {
		trial := hlrLoad(t, "priest-holy-pws-def-arms")
		obj(t, trial["player"])["talents"] = talents
		errs := scenario.Validate(trial)
		found := false
		for _, err := range errs {
			if strings.Contains(err, "talents.spells") {
				found = true
			}
		}
		if !found {
			t.Fatalf("talents.spells error missing: %v", errs)
		}
	}
}

func TestSetupFailureIsInvalidTest_UnprovenTalentsOrStanceAreInvalidNotScored(t *testing.T) {
	for _, pair := range [][2]string{
		{"player_talents", "talents not learned: 16462"},
		{"player_stance", "stance not held: aura 71 missing"},
	} {
		check, why := pair[0], pair[1]
		rules, out := hlrScore(t, "priest-holy-pws-def-arms", []any{}, hlrSeries(200, hlrSeriesOpts{}), hlrScoreOpts{
			Setup: map[string]any{
				"ok": false, "invalid_reason": why,
				"checks": []any{map[string]any{"id": check, "ok": false, "value": nil, "evidence": why}},
			},
		})
		for id, entry := range rules {
			if pyjson.Str(entry["verdict"]) != "INVALID" {
				t.Fatalf("%s: verdict %v, want INVALID", id, entry["verdict"])
			}
		}
		assertEqual(t, why, out["invalid_reason"])
	}
}

// ---- #1553 rules ------------------------------------------------------------

const hlrPWS = int64(6065)

var hlrStances = [][3]any{
	{"priest-holy-pws-def-arms", "PRI-PWS-DEF-ARMS-STANCE", int64(71)},
	{"priest-holy-pws-bear-bal", "PRI-PWS-BEAR-BAL-FORM", int64(9634)},
	{"priest-holy-pws-battle-control", "PRI-PWS-BATTLE-CONTROL-STANCE", int64(2457)},
	{"priest-holy-pws-forced-role", "PRI-PWS-FORCED-ROLE-STANCE", int64(71)},
}

func hlrTankHP(ts float64) int64 {
	if ts < 150 {
		return 600
	}
	return 1000
}

func TestShieldRulesTest_DefArmsShieldFailsOn1553Shape(t *testing.T) {
	// the named failing case: 2 shields bot->player at 60 % in combat, 3 bot->bot, 120 s of
	// opportunity
	events := []any{
		hlrEvent(10, "cast", "bot", "bot", hlrPWS, int64(100)),
		hlrEvent(40, "cast", "bot", "bot", hlrPWS, int64(100)),
		hlrEvent(70, "cast", "bot", "bot", hlrPWS, int64(100)),
		hlrEvent(60, "cast", "bot", "player", hlrPWS, int64(60)),
		hlrEvent(90, "cast", "bot", "player", hlrPWS, int64(60)),
	}
	samples := hlrSeries(200, hlrSeriesOpts{
		PlayerHP: func(ts float64) int64 {
			if ts < 120 {
				return 600
			}
			return 1000
		},
		PlayerAuras: []any{int64(71)},
	})
	entry := hlrV(t, "priest-holy-pws-def-arms", "PRI-PWS-DEF-ARMS", events, samples)
	assertEqual(t, "FAIL", entry["verdict"])
	assertEqual(t, int64(2), entry["value"])
}

func TestShieldRulesTest_DefArmsPassWhenOnlyTheCriticalShieldLands(t *testing.T) {
	events := []any{
		hlrEvent(10, "cast", "bot", "bot", hlrPWS, int64(100)),
		hlrEvent(80, "cast", "bot", "player", hlrPWS, int64(15)),
	}
	samples := hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrTankHP, PlayerAuras: []any{int64(71)}})
	entry := hlrV(t, "priest-holy-pws-def-arms", "PRI-PWS-DEF-ARMS", events, samples)
	hlrCheck(t, entry, "PASS", int64(0))
}

func TestShieldRulesTest_DefArmsWithoutOpportunityIsInconclusiveNeverPass(t *testing.T) {
	samples := hlrSeries(200, hlrSeriesOpts{
		PlayerHP:    func(float64) int64 { return 1000 },
		PlayerAuras: []any{int64(71)},
	})
	entry := hlrV(t, "priest-holy-pws-def-arms", "PRI-PWS-DEF-ARMS", nil, samples)
	assertEqual(t, "INCONCLUSIVE", entry["verdict"])
}

func TestShieldRulesTest_BearBalFailAndPass(t *testing.T) {
	samples := hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrTankHP, PlayerAuras: []any{int64(9634)}})
	bad := hlrV(t, "priest-holy-pws-bear-bal", "PRI-PWS-BEAR-BAL",
		[]any{hlrEvent(50, "cast", "bot", "player", hlrPWS, int64(70))}, samples)
	good := hlrV(t, "priest-holy-pws-bear-bal", "PRI-PWS-BEAR-BAL",
		[]any{hlrEvent(10, "cast", "bot", "bot", hlrPWS, int64(100))}, samples)
	hlrCheck(t, bad, "FAIL", int64(1))
	hlrCheck(t, good, "PASS", int64(0))
}

func TestShieldRulesTest_ForcedRoleTrialPassesWithNoShields(t *testing.T) {
	samples := hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrTankHP, PlayerAuras: []any{int64(71)}})
	same := []any{
		hlrEvent(10, "cast", "bot", "bot", hlrPWS, int64(100)),
		hlrEvent(40, "cast", "bot", "bot", hlrPWS, int64(100)),
	}
	good := hlrV(t, "priest-holy-pws-forced-role", "PRI-PWS-FORCED-ROLE", same, samples)
	badEvents := append(append([]any{}, same...), hlrEvent(60, "cast", "bot", "player", hlrPWS, int64(60)))
	bad := hlrV(t, "priest-holy-pws-forced-role", "PRI-PWS-FORCED-ROLE", badEvents, samples)
	hlrCheck(t, good, "PASS", int64(0))
	hlrCheck(t, bad, "FAIL", int64(1))
}

func TestShieldRulesTest_ForcedRoleAllowsTheCriticalShield(t *testing.T) {
	samples := hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrTankHP, PlayerAuras: []any{int64(71)}})
	entry := hlrV(t, "priest-holy-pws-forced-role", "PRI-PWS-FORCED-ROLE",
		[]any{hlrEvent(80, "cast", "bot", "player", hlrPWS, int64(15))}, samples)
	hlrCheck(t, entry, "PASS", int64(0))
}

func TestShieldRulesTest_BattleControlNeedsLowBand(t *testing.T) {
	samples := hlrSeries(200, hlrSeriesOpts{
		PlayerHP: func(ts float64) int64 {
			if ts < 120 {
				return 450
			}
			return 800
		},
		PlayerAuras: []any{int64(2457)},
	})
	high := hlrV(t, "priest-holy-pws-battle-control", "PRI-PWS-BATTLE-CONTROL",
		[]any{hlrEvent(150, "cast", "bot", "player", hlrPWS, int64(80))}, samples)
	low := hlrV(t, "priest-holy-pws-battle-control", "PRI-PWS-BATTLE-CONTROL",
		[]any{hlrEvent(30, "cast", "bot", "player", hlrPWS, int64(45))}, samples)
	hlrCheck(t, high, "FAIL", int64(0))
	hlrCheck(t, low, "PASS", int64(1))
}

func TestShieldRulesTest_BattleControlWithoutLowBandTimeIsInconclusive(t *testing.T) {
	samples := hlrSeries(200, hlrSeriesOpts{
		PlayerHP:    func(float64) int64 { return 800 },
		PlayerAuras: []any{int64(2457)},
	})
	entry := hlrV(t, "priest-holy-pws-battle-control", "PRI-PWS-BATTLE-CONTROL", nil, samples)
	assertEqual(t, "INCONCLUSIVE", entry["verdict"])
}

func TestShieldRulesTest_StanceHeldRules(t *testing.T) {
	for _, row := range hlrStances {
		tid := row[0].(string)
		rid := row[1].(string)
		aura := row[2].(int64)
		hp := hlrTankHP
		if strings.Contains(tid, "battle") {
			hp = func(float64) int64 { return 450 }
		}
		held := hlrV(t, tid, rid, nil, hlrSeries(200, hlrSeriesOpts{
			PlayerHP: hp, PlayerAuras: []any{aura},
		}))
		assertEqual(t, "PASS", held["verdict"])
		lost := hlrSeries(200, hlrSeriesOpts{PlayerHP: hp})
		assertEqual(t, "FAIL", hlrV(t, tid, rid, nil, lost)["verdict"])
	}
}

// ---- latency rules ----------------------------------------------------------

var hlrHeals = []struct {
	tid  string
	rid  string
	heal int64
}{
	{"priest-holy-heal-latency", "PRI-HEAL-LATENCY", 2061},
	{"paladin-holy-heal-latency", "PAL-HOLY-HEAL-LATENCY", 635},
	{"druid-resto-heal-latency", "DRU-RESTO-HEAL-LATENCY", 8936},
}

var hlrDrops = []int{20, 45, 70, 95, 120, 145}

func hlrDroppingHP(ts float64) int64 {
	for _, d := range hlrDrops {
		if ts >= float64(d) && ts < float64(d+8) {
			return 400
		}
	}
	return 800
}

func hlrRunHeal(t *testing.T, tid string, heal int64, rid string, delay int) map[string]any {
	t.Helper()
	var events []any
	for _, d := range hlrDrops {
		events = append(events, hlrEvent(float64(d+delay), "cast", "bot", "player", heal, int64(40)))
	}
	return hlrV(t, tid, rid, events, hlrSeries(200, hlrSeriesOpts{
		PlayerHP: hlrDroppingHP, PlayerAuras: []any{int64(71)},
	}))
}

func TestHealLatencyTest_HealLatencyPassAndFail(t *testing.T) {
	for _, heal := range hlrHeals {
		fast := hlrRunHeal(t, heal.tid, heal.heal, heal.rid, 2)
		slow := hlrRunHeal(t, heal.tid, heal.heal, heal.rid, 7)
		k, n := hlrKN(t, fast)
		assertEqual(t, []any{"PASS", int64(6), int64(6)},
			[]any{fast["verdict"], k, n})
		k, n = hlrKN(t, slow)
		assertEqual(t, []any{"FAIL", int64(0), int64(6)},
			[]any{slow["verdict"], k, n})
	}
}

func TestHealLatencyTest_HealOnSomeoneElseDoesNotAnswer(t *testing.T) {
	var events []any
	for _, d := range hlrDrops {
		events = append(events, hlrEvent(float64(d+2), "cast", "bot", "bot", 2061, int64(90)))
	}
	entry := hlrV(t, "priest-holy-heal-latency", "PRI-HEAL-LATENCY", events,
		hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrDroppingHP, PlayerAuras: []any{int64(71)}}))
	assertEqual(t, "FAIL", entry["verdict"])
}

func TestHealLatencyTest_TooFewCrossingsIsInconclusive(t *testing.T) {
	samples := hlrSeries(200, hlrSeriesOpts{
		PlayerHP: func(ts float64) int64 {
			if ts >= 20 && ts < 28 {
				return 400
			}
			return 800
		},
		PlayerAuras: []any{int64(71)},
	})
	entry := hlrV(t, "druid-resto-heal-latency", "DRU-RESTO-HEAL-LATENCY",
		[]any{hlrEvent(22, "cast", "bot", "player", 8936, int64(40))}, samples)
	assertEqual(t, "INCONCLUSIVE", entry["verdict"])
}

func TestHealLatencyTest_DruidSwiftnessAndPriestShieldCountAsAnswers(t *testing.T) {
	swift := hlrSid(t, "Nature''s Swiftness", 0)
	var events []any
	for _, d := range hlrDrops {
		events = append(events, hlrEvent(float64(d+1), "cast", "bot", "player", swift, int64(40)))
	}
	entry := hlrV(t, "druid-resto-heal-latency", "DRU-RESTO-HEAL-LATENCY", events,
		hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrDroppingHP, PlayerAuras: []any{int64(71)}}))
	assertEqual(t, "PASS", entry["verdict"])
	events = nil
	for _, d := range hlrDrops {
		events = append(events, hlrEvent(float64(d+1), "cast", "bot", "player", hlrPWS, int64(40)))
	}
	entry = hlrV(t, "priest-holy-heal-latency", "PRI-HEAL-LATENCY", events,
		hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrDroppingHP, PlayerAuras: []any{int64(71)}}))
	assertEqual(t, "PASS", entry["verdict"])
}

func TestHealLatencyTest_TriggersWhileTheHealerIsOutOfManaAreSkipped(t *testing.T) {
	// bench-fix-1112: 22 of 24 paladin misses and 14 of 19 priest misses sat under 10 %
	// mana. Answered crossings still count; the OOM ones are no opportunity.
	oom := func(ts float64) any {
		if ts >= 55 && ts <= 115 {
			return int64(30)
		}
		return int64(500) // 10 % of the 500 pool is 50
	}
	var events []any
	for _, d := range hlrDrops {
		events = append(events, hlrEvent(float64(d+2), "cast", "bot", "player", 2061, int64(40)))
	}
	entry := hlrV(t, "priest-holy-heal-latency", "PRI-HEAL-LATENCY", events,
		hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrDroppingHP, PlayerAuras: []any{int64(71)}, BotPower: oom}))
	k, n := hlrKN(t, entry)
	assertEqual(t, []any{"INCONCLUSIVE", int64(4), int64(4)}, []any{entry["verdict"], k, n})
}

func TestHealLatencyTest_TriggersWhileTheHealerIsDeadAreSkipped(t *testing.T) {
	// bench-fix-1112: the priest lay dead at 124.3 s in rep3 (@lvl50). A dead healer and a
	// Spirit of Redemption healer (aura 27827) carry no chance either.
	var late []any
	for _, d := range hlrDrops[:4] {
		late = append(late, hlrEvent(float64(d+2), "cast", "bot", "player", 2061, int64(40)))
	}
	dead := func(ts float64) bool { return ts >= 100 }
	entry := hlrV(t, "priest-holy-heal-latency", "PRI-HEAL-LATENCY", late,
		hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrDroppingHP, PlayerAuras: []any{int64(71)}, BotDead: dead}))
	k, n := hlrKN(t, entry)
	assertEqual(t, []any{"INCONCLUSIVE", int64(4), int64(4)}, []any{entry["verdict"], k, n})
	sor := hlrSeries(200, hlrSeriesOpts{
		PlayerHP: hlrDroppingHP, PlayerAuras: []any{int64(71)}, BotAuras: []any{int64(27827)},
		BotDead: func(float64) bool { return true },
	})
	entry = hlrV(t, "priest-holy-heal-latency", "PRI-HEAL-LATENCY", late, sor)
	_, n = hlrKN(t, entry)
	assertEqual(t, []any{"INCONCLUSIVE", int64(0)}, []any{entry["verdict"], n})
}

func TestHealLatencyTest_TheHealTrialsSkipOOMDeadTriggersAndSpaceTheDamage(t *testing.T) {
	for _, tid := range []string{"priest-holy-heal-latency", "paladin-holy-heal-latency"} {
		trial := hlrLoad(t, tid)
		rid := "PRI-HEAL-LATENCY"
		if tid == "paladin-holy-heal-latency" {
			rid = "PAL-HOLY-HEAL-LATENCY"
		}
		rule := hlrRuleOf(t, trial, rid)
		assertEqual(t, map[string]any{
			"unit": "$bot", "power_below_pct": int64(10), "skip_when_dead": true,
		}, obj(t, rule["check"])["skip_trigger_while"])
		assertEqual(t, int64(15), obj(t, trial["situation"])["damage_interval_s"])
	}
}

func TestHealLatencyTest_BadSkipAndIntervalShapesAreRejected(t *testing.T) {
	trial := hlrLoad(t, "priest-holy-heal-latency")
	rule := hlrRuleOf(t, trial, "PRI-HEAL-LATENCY")
	check := obj(t, rule["check"])
	orig := check["skip_trigger_while"]
	for _, bad := range []map[string]any{
		{"unit": "$bot", "power_below_pct": int64(0), "skip_when_dead": true},
		{"unit": "$bot", "power_below_pct": int64(100), "skip_when_dead": true},
		{"unit": "$mob", "power_below_pct": int64(10), "skip_when_dead": true},
		{"unit": "$bot", "power_below_pct": int64(10), "skip_when_dead": false},
		{"unit": "$bot", "power_below_pct": int64(10), "skip_when_dead": true, "extra": int64(1)},
	} {
		check["skip_trigger_while"] = bad
		found := false
		for _, err := range scenario.Validate(trial) {
			if strings.Contains(err, "skip_trigger_while") {
				found = true
			}
		}
		if !found {
			t.Fatalf("skip_trigger_while error missing for %v", bad)
		}
	}
	check["skip_trigger_while"] = orig
	situation := obj(t, trial["situation"])
	for _, bad := range []any{int64(3), int64(61), "15"} {
		situation["damage_interval_s"] = bad
		found := false
		for _, err := range scenario.Validate(trial) {
			if strings.Contains(err, "damage_interval_s") {
				found = true
			}
		}
		if !found {
			t.Fatalf("damage_interval_s error missing for %v", bad)
		}
	}
	situation["damage_interval_s"] = int64(15)
	assertEqual(t, []any{}, hlrErrs(scenario.Validate(trial)))
}

func hlrErrs(errs []string) []any {
	out := []any{}
	for _, err := range errs {
		out = append(out, err)
	}
	return out
}

// ---- dispel, OOC and shadowform ---------------------------------------------

const hlrFF = int64(770)

var hlrApplied = []int{10, 50, 90, 130}

func TestDispelTest_DispelMagicPassAndFail(t *testing.T) {
	dispel := hlrSid(t, "Dispel Magic", 0)
	var trig []any
	for _, ts := range hlrApplied {
		trig = append(trig, hlrEvent(float64(ts), "aura_on", "player", "player", hlrFF, int64(80)))
	}
	samples := hlrSeries(200, hlrSeriesOpts{
		PlayerHP:    func(float64) int64 { return 800 },
		PlayerAuras: []any{int64(71), hlrFF},
	})
	good := slices.Clone(trig)
	for _, ts := range hlrApplied {
		good = append(good, hlrEvent(float64(ts+3), "cast", "bot", "player", dispel, int64(80)))
	}
	late := slices.Clone(trig)
	for _, ts := range hlrApplied {
		late = append(late, hlrEvent(float64(ts+9), "cast", "bot", "player", dispel, int64(80)))
	}
	assertEqual(t, "PASS", hlrV(t, "priest-holy-dispel-magic", "PRI-DISPEL-MAGIC", good, samples)["verdict"])
	bad := hlrV(t, "priest-holy-dispel-magic", "PRI-DISPEL-MAGIC", late, samples)
	k, n := hlrKN(t, bad)
	assertEqual(t, []any{"FAIL", int64(0), int64(4)}, []any{bad["verdict"], k, n})
}

func TestDispelTest_DispelTrialProvesTheDebuffLands(t *testing.T) {
	trial := hlrLoad(t, "priest-holy-dispel-magic")
	if !hlrHas(trial["setup_checks"], "player_stance") {
		t.Fatal("player_stance setup check missing")
	}
	// the debuff is the TESTED aura, not a setup aura: a 770 applied in setup (or left from an
	// earlier rep) is already up at the pull, and the post-pull `.aura` only refreshes it, so
	// the rule has no fresh `aura_on` to score. The runner strips it and proves it absent
	// before the pull, then applies it and proves the event.
	assertEqual(t, []any{}, obj(t, trial["player"])["auras"])
	if hlrHas(trial["setup_checks"], "player_auras") {
		t.Fatal("player_auras must not be a setup check")
	}
	assertEqual(t, map[string]any{"spell_id": hlrFF}, obj(t, trial["situation"])["debuff"])
	assertEqual(t, int64(1), obj(t, hlrSpellsByID(t)["770"])["dispel"])
}

func TestDispelTest_FewerThanFourApplicationsIsInconclusive(t *testing.T) {
	var trig []any
	for _, ts := range hlrApplied[:2] {
		trig = append(trig, hlrEvent(float64(ts), "aura_on", "player", "player", hlrFF, int64(80)))
	}
	entry := hlrV(t, "priest-holy-dispel-magic", "PRI-DISPEL-MAGIC", trig,
		hlrSeries(200, hlrSeriesOpts{
			PlayerHP:    func(float64) int64 { return 800 },
			PlayerAuras: []any{int64(71), hlrFF},
		}))
	assertEqual(t, "INCONCLUSIVE", entry["verdict"])
}

func hlrOOC(t *testing.T, tid, rid string, events []any) map[string]any {
	t.Helper()
	return hlrVerdict(t, tid, rid, events,
		hlrSeries(60, hlrSeriesOpts{PlayerHP: func(float64) int64 { return 1000 }, OOC: true}),
		hlrScoreOpts{Secs: 60})
}

func TestOocTest_FortitudeOocPassAndFail(t *testing.T) {
	fort := hlrSid(t, "Power Word: Fortitude", 0)
	assertEqual(t, "PASS", hlrOOC(t, "priest-holy-fort-ooc", "PRI-FORT-OOC",
		[]any{hlrEvent(12, "aura_on", "bot", "player", fort, int64(100))})["verdict"])
	prayer := hlrSid(t, "Prayer of Fortitude", 0)
	assertEqual(t, "PASS", hlrOOC(t, "priest-holy-fort-ooc", "PRI-FORT-OOC",
		[]any{hlrEvent(29, "aura_on", "bot", "player", prayer, int64(100))})["verdict"])
	late := hlrOOC(t, "priest-holy-fort-ooc", "PRI-FORT-OOC",
		[]any{hlrEvent(45, "aura_on", "bot", "player", fort, int64(100))})
	none := hlrOOC(t, "priest-holy-fort-ooc", "PRI-FORT-OOC", nil)
	assertEqual(t, "FAIL", late["verdict"])
	assertEqual(t, "FAIL", none["verdict"])
	if none["value"] != nil {
		t.Fatalf("value = %v, want nil", none["value"])
	}
}

func TestOocTest_FortitudeOnThePriestItselfDoesNotCount(t *testing.T) {
	fort := hlrSid(t, "Power Word: Fortitude", 0)
	entry := hlrOOC(t, "priest-holy-fort-ooc", "PRI-FORT-OOC",
		[]any{hlrEvent(5, "aura_on", "bot", "bot", fort, int64(100))})
	assertEqual(t, "FAIL", entry["verdict"])
}

func TestOocTest_BlessingOocPassAndFail(t *testing.T) {
	might := hlrSid(t, "Blessing of Might", 0)
	greater := hlrSid(t, "Greater Blessing of Kings", 0)
	assertEqual(t, "PASS", hlrOOC(t, "paladin-blessing-ooc", "PAL-BLESSING-OOC",
		[]any{hlrEvent(15, "aura_on", "bot", "player", might, int64(100))})["verdict"])
	assertEqual(t, "PASS", hlrOOC(t, "paladin-blessing-ooc", "PAL-BLESSING-OOC",
		[]any{hlrEvent(28, "aura_on", "bot", "player", greater, int64(100))})["verdict"])
	assertEqual(t, "FAIL", hlrOOC(t, "paladin-blessing-ooc", "PAL-BLESSING-OOC",
		[]any{hlrEvent(40, "aura_on", "bot", "player", might, int64(100))})["verdict"])
	assertEqual(t, "FAIL", hlrOOC(t, "paladin-blessing-ooc", "PAL-BLESSING-OOC", nil)["verdict"])
	// a blessing on the paladin itself is not a blessing on the player
	assertEqual(t, "FAIL", hlrOOC(t, "paladin-blessing-ooc", "PAL-BLESSING-OOC",
		[]any{hlrEvent(10, "aura_on", "bot", "bot", might, int64(100))})["verdict"])
}

const hlrShadowform = int64(15473)

func TestShadowformTest_ShadowformPrepullPassAndFail(t *testing.T) {
	samples := hlrSeries(200, hlrSeriesOpts{
		PlayerHP: func(float64) int64 { return 800 }, PlayerAuras: []any{int64(71)},
		BotAuras: []any{hlrShadowform},
	})
	// a cast long before the pull (t -5) is outside the window: the form was up at the pull
	before := []any{hlrEvent(-5, "cast", "bot", "bot", hlrShadowform, int64(100))}
	good := hlrV(t, "priest-shadow-form-prepull", "PRI-SHADOWFORM-PREPULL", before, samples)
	late := hlrV(t, "priest-shadow-form-prepull", "PRI-SHADOWFORM-PREPULL",
		[]any{hlrEvent(1.2, "cast", "bot", "bot", hlrShadowform, int64(100))}, samples)
	hlrCheck(t, good, "PASS", int64(0))
	hlrCheck(t, late, "FAIL", int64(1))
}

func TestShadowformTest_ACastAtThePullCountsFrom3sBefore(t *testing.T) {
	// bench-fix-1112: casts 0.3-0.4 s before fight_start coin-flipped the per-rep value. The
	// window opens at t -3, so a cast at the pull counts.
	samples := hlrSeries(200, hlrSeriesOpts{
		PlayerHP: func(float64) int64 { return 800 }, PlayerAuras: []any{int64(71)},
		BotAuras: []any{hlrShadowform},
	})
	pull := []any{hlrEvent(-0.43, "cast", "bot", "bot", hlrShadowform, int64(100))}
	entry := hlrV(t, "priest-shadow-form-prepull", "PRI-SHADOWFORM-PREPULL", pull, samples)
	hlrCheck(t, entry, "FAIL", int64(1))
	rule := hlrRuleOf(t, hlrLoad(t, "priest-shadow-form-prepull"), "PRI-SHADOWFORM-PREPULL")
	where := obj(t, obj(t, rule["check"])["where"])
	assertEqual(t, []any{int64(-3), int64(8)}, []any{where["t_from"], where["t_to"]})
}

func TestShadowformTest_ARecastLateInTheFightIsNotThePull(t *testing.T) {
	samples := hlrSeries(200, hlrSeriesOpts{
		PlayerHP: func(float64) int64 { return 800 }, PlayerAuras: []any{int64(71)},
		BotAuras: []any{hlrShadowform},
	})
	entry := hlrV(t, "priest-shadow-form-prepull", "PRI-SHADOWFORM-PREPULL",
		[]any{hlrEvent(90, "cast", "bot", "bot", hlrShadowform, int64(100))}, samples)
	assertEqual(t, "PASS", entry["verdict"])
}

func TestShadowformTest_CompanionUptimeKeepsThePrepullPassHonest(t *testing.T) {
	opts := hlrSeriesOpts{PlayerHP: func(float64) int64 { return 800 }, PlayerAuras: []any{int64(71)}}
	opts.BotAuras = []any{hlrShadowform}
	up := hlrSeries(200, opts)
	opts.BotAuras = nil
	down := hlrSeries(200, opts)
	assertEqual(t, "PASS", hlrV(t, "priest-shadow-form-prepull", "PRI-SHADOWFORM-UP", nil, up)["verdict"])
	assertEqual(t, "FAIL", hlrV(t, "priest-shadow-form-prepull", "PRI-SHADOWFORM-UP", nil, down)["verdict"])
}

// ---- #1553 second-tank trials (extra_bots) -----------------------------------

var hlrTwoTank = []string{"priest-holy-pws-def-arms-2tank", "priest-holy-pws-bear-bal-2tank"}

const hlrExtraTank = "0x0000000000000003"

var hlrTwoTankSetup = map[string]any{
	"guids": map[string]any{"player": hlrPlayer, "bot": hlrBot,
		"extras": []any{hlrExtraTank}, "mobs": []any{hlrMob}},
}

func hlrTwo(t *testing.T, tid, rid string, events, samples []any) map[string]any {
	t.Helper()
	return hlrVerdict(t, tid, rid, events, samples, hlrScoreOpts{Setup: hlrTwoTankSetup})
}

func TestTwoTankTrialFilesTest_TwoTankTrialsAreTheSoloTrialsPlusAProtectionPaladin(t *testing.T) {
	pairs := [][3]any{
		{"priest-holy-pws-def-arms", hlrTwoTank[0], int64(71)},
		{"priest-holy-pws-bear-bal", hlrTwoTank[1], int64(9634)},
	}
	for _, pair := range pairs {
		soloID, tid, aura := pair[0].(string), pair[1].(string), pair[2].(int64)
		solo, two := hlrLoad(t, soloID), hlrLoad(t, tid)
		assertEqual(t, []any{map[string]any{
			"class": "paladin", "spec": "protection", "role": "tank", "level": int64(40),
			"items": []any{},
		}}, two["extra_bots"])
		for _, key := range []string{"class", "spec", "player", "bot", "situation", "duration_s",
			"repeats", "min_valid_s", "opportunity", "group"} {
			assertEqual(t, solo[key], two[key])
		}
		var twoChecks, soloChecks []any
		for _, checkVal := range list(t, two["setup_checks"]) {
			check := pyjson.Str(checkVal)
			if !strings.HasPrefix(check, "extra_bots_") {
				twoChecks = append(twoChecks, check)
			}
		}
		soloChecks = list(t, solo["setup_checks"])
		assertEqual(t, soloChecks, twoChecks)
		extra := map[string]bool{}
		for _, checkVal := range list(t, two["setup_checks"]) {
			check := pyjson.Str(checkVal)
			if strings.HasPrefix(check, "extra_bots_") {
				extra[check] = true
			}
		}
		assertEqual(t, map[string]bool{
			"extra_bots_present": true, "extra_bots_role": true, "extra_bots_in_group": true,
		}, extra)
		assertEqual(t, aura, obj(t, two["player"])["stance"])
		assertEqual(t, obj(t, two["bot"])["level"], list(t, two["extra_bots"])[0].(map[string]any)["level"])
	}
}

func TestTwoTankTrialFilesTest_TwoTankRulesCiteTheSecondTankLinesAndTheIssue(t *testing.T) {
	for i, tid := range hlrTwoTank {
		rid := []string{"PRI-PWS-DEF-ARMS-2TANK", "PRI-PWS-BEAR-BAL-2TANK"}[i]
		rule := hlrRuleOf(t, hlrLoad(t, tid), rid)
		// the expectation is stale on 4180cde: #1553 was fixed by TortoiseBots PR #1557
		assertEqual(t, "PASS", rule["expect_today"])
		assertEqual(t, int64(3), rule["min_reps"])
		if !strings.Contains(pyjson.Str(rule["desc"]), "Regression guard") {
			t.Fatalf("%s: desc lacks Regression guard", rid)
		}
		for _, text := range []any{rule["source"], rule["why_expect"]} {
			if !strings.Contains(pyjson.Str(text)+pyjson.Str(rule["source"]), "1553") {
				t.Fatalf("%s: no 1553 in %q", rid, text)
			}
		}
		for _, bit := range []string{"520-576", "597-611"} {
			if !strings.Contains(pyjson.Str(rule["source"]), bit) {
				t.Fatalf("%s: source lacks %s", rid, bit)
			}
		}
		why := pyjson.Str(rule["why_expect"])
		for _, bit := range []string{"AiFactory.cpp:520-576", "597-611", "#1553", "#1557"} {
			if !strings.Contains(why, bit) {
				t.Fatalf("%s: why_expect lacks %s", rid, bit)
			}
		}
		assertEqual(t, "== 0", rule["pass_if"])
		solo := hlrRuleOf(t, hlrLoad(t, strings.Replace(tid, "-2tank", "", 1)),
			strings.Replace(rid, "-2TANK", "", 1))
		assertEqual(t, solo["check"], rule["check"])
	}
}

func TestTwoTankRulesTest_DefArms2tankFailsWhenTheWarriorIsShieldedAboveCritical(t *testing.T) {
	events := []any{
		hlrEvent(10, "cast", "bot", "bot", hlrPWS, int64(100)),
		hlrEvent(60, "cast", "bot", "player", hlrPWS, int64(60)),
		hlrEvent(90, "cast", "bot", "player", hlrPWS, int64(60)),
	}
	samples := hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrTankHP, PlayerAuras: []any{int64(71)}})
	entry := hlrTwo(t, "priest-holy-pws-def-arms-2tank", "PRI-PWS-DEF-ARMS-2TANK", events, samples)
	hlrCheck(t, entry, "FAIL", int64(2))
}

func TestTwoTankRulesTest_DefArms2tankPassesWithoutShieldsAboveCritical(t *testing.T) {
	events := []any{
		hlrEvent(10, "cast", "bot", "bot", hlrPWS, int64(100)),
		hlrEvent(80, "cast", "bot", "player", hlrPWS, int64(15)),
	}
	samples := hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrTankHP, PlayerAuras: []any{int64(71)}})
	entry := hlrTwo(t, "priest-holy-pws-def-arms-2tank", "PRI-PWS-DEF-ARMS-2TANK", events, samples)
	hlrCheck(t, entry, "PASS", int64(0))
}

func TestTwoTankRulesTest_BearBal2tankFailsAndPasses(t *testing.T) {
	samples := hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrTankHP, PlayerAuras: []any{int64(9634)}})
	bad := hlrTwo(t, "priest-holy-pws-bear-bal-2tank", "PRI-PWS-BEAR-BAL-2TANK",
		[]any{hlrEvent(50, "cast", "bot", "player", hlrPWS, int64(70))}, samples)
	good := hlrTwo(t, "priest-holy-pws-bear-bal-2tank", "PRI-PWS-BEAR-BAL-2TANK",
		[]any{hlrEvent(10, "cast", "bot", "bot", hlrPWS, int64(100))}, samples)
	hlrCheck(t, bad, "FAIL", int64(1))
	hlrCheck(t, good, "PASS", int64(0))
}

func TestTwoTankRulesTest_2tankWithoutOpportunityIsInconclusiveNeverPass(t *testing.T) {
	samples := hlrSeries(200, hlrSeriesOpts{
		PlayerHP:    func(float64) int64 { return 1000 },
		PlayerAuras: []any{int64(71)},
	})
	entry := hlrTwo(t, "priest-holy-pws-def-arms-2tank", "PRI-PWS-DEF-ARMS-2TANK", nil, samples)
	assertEqual(t, "INCONCLUSIVE", entry["verdict"])
}

func TestTwoTankRulesTest_2tankStanceRulesPassHeldAndFailLost(t *testing.T) {
	for _, row := range [][3]any{
		{"priest-holy-pws-def-arms-2tank", "PRI-PWS-DEF-ARMS-2TANK-STANCE", int64(71)},
		{"priest-holy-pws-bear-bal-2tank", "PRI-PWS-BEAR-BAL-2TANK-FORM", int64(9634)},
	} {
		tid, rid, aura := row[0].(string), row[1].(string), row[2].(int64)
		held := hlrTwo(t, tid, rid, nil, hlrSeries(200, hlrSeriesOpts{
			PlayerHP: hlrTankHP, PlayerAuras: []any{aura},
		}))
		lost := hlrTwo(t, tid, rid, nil, hlrSeries(200, hlrSeriesOpts{PlayerHP: hlrTankHP}))
		assertEqual(t, []any{"PASS", "FAIL"}, []any{held["verdict"], lost["verdict"]})
	}
}

func TestTwoTankRulesTest_FailedExtraBotSetupIsInvalidNotScored(t *testing.T) {
	for _, pair := range [][2]string{
		{"extra_bots_present", "extra1: hire refused: cannot afford"},
		{"extra_bots_role", "extra1 Bulwark: no 'role set to' reply"},
		{"extra_bots_in_group", "in_group=True; not in the party: Bulwark"},
	} {
		check, why := pair[0], pair[1]
		for _, tid := range hlrTwoTank {
			setup := map[string]any{
				"ok": false, "invalid_reason": why,
				"checks": []any{map[string]any{"id": check, "ok": false, "value": nil, "evidence": why}},
			}
			for key, val := range hlrTwoTankSetup {
				setup[key] = val
			}
			rules, out := hlrScore(t, tid, nil, hlrSeries(200, hlrSeriesOpts{}), hlrScoreOpts{Setup: setup})
			for id, entry := range rules {
				if pyjson.Str(entry["verdict"]) != "INVALID" {
					t.Fatalf("%s/%s/%s: verdict %v, want INVALID", tid, check, id, entry["verdict"])
				}
			}
			assertEqual(t, why, out["invalid_reason"])
		}
	}
}
