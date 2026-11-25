// Package scenario loads and validates trial files. The schema is the contract with the
// trial briefs: an unknown key at any level is an error, and every enum is closed.
package scenario

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/spells"
	"github.com/pjerra/forever-agent/internal/trials/talents"
	"github.com/pjerra/forever-agent/internal/trials/trialdefs"
)

// Trial is one trial definition (a decoded trial JSON object).
type Trial = map[string]any

// VariantSep splits a `<trial>@<variant>` id.
const VariantSep = "@"

// SnapshotName is the effective trial a run froze (written by the runner).
const SnapshotName = "trial.effective.json"

// MinHireLevel: the talent proof needs the spec's marker talent, which the premade build of
// level 35 already has and the builds of levels 20-30 stop short of.
const MinHireLevel = 35

// MaxVariants is the most variants one trial may carry.
const MaxVariants = 6

var (
	idRe     = regexp.MustCompile(`^[a-z0-9-]+$`)
	passIfRe = regexp.MustCompile(`^(==|!=|<=|>=|<|>)\s*-?\d+(\.\d+)?$`)
	gearRe   = regexp.MustCompile(`^[a-z]+$`)
)

var groups = map[string]bool{"healers": true, "tanks-melee": true, "ranged": true, "raid": true}

var setupChecks = map[string]bool{
	"player_level": true, "player_talents": true, "player_stance": true, "player_forced_role": true,
	"player_auras": true, "bot_present": true, "bot_level": true, "bot_spec": true, "bot_role": true, "bot_talents": true,
	"bot_items": true, "bot_mana": true, "grouped": true, "mobs_ready": true, "mob_caster_casts": true,
	"cc_mark_set": true, "config_read": true, "pet_present": true, "player_gear": true, "player_gear_strip": true, "player_gear_carryover": true,
	"walk_report_ok": true, "boss_fights_present": true, "practice_marked": true,
	"extra_bots_present": true, "extra_bots_role": true, "extra_bots_in_group": true,
	// live3: player_visible = the bots saw the player (checked in the fight, required by every
	// non-raid trial); player_no_shield = no off-hand item on the player (Arms/Battle trials);
	// extra_bots_no_shield = the same for the extra_bots hires
	"player_visible": true, "player_no_shield": true, "extra_bots_no_shield": true,
}

// requiredSetupChecks must be listed by every non-raid trial.
var requiredSetupChecks = []string{"player_visible"}

var extraOnlyChecks = []string{"extra_bots_no_shield"}

// extraBotChecks are the checks an `extra_bots` trial must list (and only such a trial may list).
var extraBotChecks = []string{"extra_bots_present", "extra_bots_role", "extra_bots_in_group"}

// MaxExtraBots is the most hires beside `bot`.
const MaxExtraBots = 3

var metrics = map[string]bool{"count": true, "ratio": true, "latency": true, "uptime": true, "first": true, "rate": true, "walk_metric": true, "dummy": true}

var whereKeys = map[string]bool{
	"kind": true, "src": true, "dst": true, "spell": true, "spell_id": true, "dst_hp_gt": true, "dst_hp_lt": true,
	"dst_hp_le": true, "in_combat": true, "t_from": true, "t_to": true, "dst_has_aura": true,
	"dst_lacks_aura": true, "src_power_lt": true, "dst_power_lt": true, "response_dst": true, "same_dst": true,
	"trigger_debounce_s": true, "src_target_of": true, "dst_target_of": true,
	"src_within_of_marked": true, "marked_has_cc": true, "spell_interruptible": true,
	"src_hp_le": true, "not_within_s_after_mark": true, "party_under_attack": true,
}

// Closed enums (no prefix escape anywhere).
var triggerKinds = map[string]bool{"cross": true, "mark": true, "damage": true, "aura_on": true, "aura_off": true, "cast": true, "cast_start": true}

var extraAliases = []string{"$extra1", "$extra2", "$extra3"}

var crossUnits = map[string]bool{"$player": true, "$bot": true, "$pet": true, "$marked": true, "$extra1": true, "$extra2": true, "$extra3": true}

var whereMetrics = map[string]bool{"count": true, "first": true, "rate": true}

var responseDstModes = map[string]bool{"$trigger.src": true, "$trigger.dst": true}

var kindEnum = map[string]bool{"cast": true, "cast_start": true, "cast_fail": true, "aura_on": true, "aura_off": true,
	"damage": true, "heal": true, "death": true,
	// The core-gains round's event kinds: the interrupt log (a Kick/Counterspell answer), a dispel, an absorb, a
	// miss, a proc resist, an immune and an instant kill.
	"interrupt": true, "dispel": true, "absorb": true, "miss": true, "resist": true, "immune": true, "instakill": true}

var unitAliases = map[string]bool{"$player": true, "$bot": true, "$pet": true, "$marked": true, "$mob": true,
	"$extra1": true, "$extra2": true, "$extra3": true, "$extras": true}

var thresholdAliases = map[string]bool{"$critical": true, "$low": true, "$medium": true}

// Closed top-level fields (extra keys rejected at every level).
var topFields = stringSet("id", "title", "group", "class", "spec", "player", "bot",
	"situation", "setup_checks", "duration_s", "repeats",
	"min_valid_s", "opportunity", "rules", "boss", "live", "spec_ids",
	"extra_bots", "variants")

var playerFields = stringSet("class", "level", "talents", "stance", "forced_role",
	"auras", "items", "mana_pct", "cc_mark", "gear", "role", "spec")

var botFields = stringSet("class", "spec", "role", "level", "gear", "items", "mana_pct", "cc_mark")

var extraBotFields = stringSet("class", "spec", "role", "level", "items", "gear")

var extraItemFields = stringSet("name", "count")

var sitFields = stringSet("kind", "mobs", "hp_band", "floor_pct", "victim", "pull_after_s",
	"ooc_wait_s", "debuff", "refill", "roles", "max_rescues_per_min",
	"hurt_every_s", "adaptive", "top_up_to_pct", "damage_interval_s", "reset_hire_cooldowns",
	"energy_drain", "prepull_unaura", "min_rep_gap_s",
	"dummy_entry", "rotation", "expects_file")

var talentsFields = stringSet("tree", "spells")

var opportunityFields = stringSet("unit", "hp_lt", "hp_gt", "power_lt", "power_gt", "in_combat", "has_aura")

var mobsFields = stringSet("role", "count", "level_delta", "refill", "refill_every_s")

var ruleFields = stringSet("id", "desc", "source", "expect_today", "why_expect",
	"check", "pass_if", "min_opportunity_s", "min_n", "min_reps", "pool")

var checkFields = stringSet("metric", "where", "num", "den", "trigger", "response",
	"within_s", "response_dst", "same_dst", "trigger_debounce_s",
	"skip_trigger_while",
	"debounce_responder_s", "debounce_responder_spells", "debounce_responder_across_reps",
	"per", "min_alive_s", "name", "aura", "unit", "phase", "alive", "triggers", "min_n",
	"spell", "stat")

var triggerFields = stringSet("kind", "src", "dst", "spell", "spell_id", "dst_hp_gt", "dst_hp_lt",
	"dst_hp_le", "in_combat", "t_from", "t_to", "dst_has_aura", "dst_lacks_aura",
	"src_power_lt", "dst_power_lt", "src_target_of", "dst_target_of", "src_within_of_marked", "marked_has_cc",
	"spell_interruptible", "src_hp_le", "not_within_s_after_mark", "party_under_attack",
	"unit", "below", "mark")

var responseFields = stringSet("kind", "src", "dst", "spell", "spell_id", "in_combat")

var specIDs = stringSet("arms", "fury", "protection", "holy", "retribution", "bm", "mm",
	"survival", "assa", "combat", "subtlety", "discipline", "shadow",
	"hybrid", "ele", "enh", "restoration", "arcane", "fire", "frost",
	"affliction", "demonology", "destruction", "balance", "boomkin",
	"cat", "bear")

var variantFields = stringSet("name", "title", "override")
var overrideFields = stringSet("level", "player", "bot", "extra_bots", "situation", "rules")
var overrideFrozen = map[string][]string{
	"player": {"class"},
	"bot":    {"class", "role"},
}
var ruleOverrideFields = stringSet("pass_if", "min_n", "min_opportunity_s", "min_reps")
var ruleTextFields = stringSet("desc", "source", "why_expect", "check")

// IsTriggerKind reports whether kind is one of the closed trigger kinds.
func IsTriggerKind(kind string) bool { return triggerKinds[kind] }

// IsTriggerField reports whether key is one of the closed trigger fields.
func IsTriggerField(key string) bool { return triggerFields[key] }

// ExtraBotChecks lists the setup checks an `extra_bots` trial must carry.
func ExtraBotChecks() []string { return append([]string{}, extraBotChecks...) }

func stringSet(items ...string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}

// TrialFiles lists the embedded trial files (trials/<group>/*.json, no _fixtures), sorted.
func TrialFiles() []string {
	var out []string
	for _, path := range trialdefs.Paths() {
		if strings.HasPrefix(path, "_fixtures/") {
			continue
		}
		out = append(out, path)
	}
	return out
}

// FixtureFiles lists the embedded trials/_fixtures/*.json files, sorted.
func FixtureFiles() []string {
	var out []string
	for _, path := range trialdefs.Paths() {
		if strings.HasPrefix(path, "_fixtures/") {
			out = append(out, path)
		}
	}
	return out
}

// LoadTrial reads and decodes one trial file from disk or, when the path is one of the
// embedded names, from the embedded set.
func LoadTrial(path string) (Trial, error) {
	var data []byte
	if b, err := trialdefs.Read(path); err == nil {
		data = b
	} else {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		data = b
	}
	doc, err := pyjson.Unmarshal(data)
	if err != nil {
		return nil, err
	}
	trial, ok := pyjson.Object(doc)
	if !ok {
		return nil, fmt.Errorf("%s: trial is not a JSON object", path)
	}
	return trial, nil
}

func errAdd(errs *[]string, msg string) { *errs = append(*errs, msg) }

func isIntLike(v any) bool {
	if pyjson.IsBool(v) {
		return true
	}
	return pyjson.IsInt(v)
}

func isIntStrict(v any) bool {
	if pyjson.IsBool(v) {
		return false
	}
	return pyjson.IsInt(v)
}

func checkDict(doc any, allowed map[string]bool, label, trialID, rid string, errs *[]string) bool {
	obj, ok := pyjson.Object(doc)
	if !ok {
		errAdd(errs, fmt.Sprintf("%s/%s: %s is not an object", trialID, rid, label))
		return false
	}
	valid := true
	for _, key := range sortedKeys(obj) {
		if !allowed[key] {
			errAdd(errs, fmt.Sprintf("%s/%s: unknown %s field %s", trialID, rid, label, pythonRepr(key)))
			valid = false
		}
	}
	return valid
}

func sortedKeys(obj map[string]any) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func isNumVal(val any) bool {
	if pyjson.IsBool(val) {
		return false
	}
	if _, ok := pyjson.Num(val); ok {
		return true
	}
	if s, ok := val.(string); ok {
		if _, err := parseFloatLoose(s); err == nil {
			return true
		}
	}
	return false
}

func parseFloatLoose(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func numOrThreshold(val any) bool {
	if s, ok := val.(string); ok && thresholdAliases[s] {
		return true
	}
	return isNumVal(val)
}

func checkFilter(doc any, label, trialID, rid string, errs *[]string, needKind bool, checkKeys bool, allowed map[string]bool) {
	obj, ok := pyjson.Object(doc)
	where := fmt.Sprintf("%s/%s", trialID, rid)
	if !ok {
		errAdd(errs, fmt.Sprintf("%s: %s is not an object", where, label))
		return
	}
	if checkKeys {
		if allowed == nil {
			allowed = whereKeys
		}
		for _, key := range sortedKeys(obj) {
			if !allowed[key] {
				errAdd(errs, fmt.Sprintf("%s: unknown %s key %s", where, label, pythonRepr(key)))
			}
		}
	}
	if spec, present := obj["not_within_s_after_mark"]; present {
		specs := []any{spec}
		if list, ok := pyjson.List(spec); ok {
			specs = list
		}
		badEntry := len(specs) == 0
		for _, one := range specs {
			entry, ok := pyjson.Object(one)
			if !ok || len(entry) != 2 {
				badEntry = true
				continue
			}
			kindVal, hasKind := entry["kind"]
			sVal, hasS := entry["s"]
			kindStr, kindOK := kindVal.(string)
			sNum, sOK := pyjson.Num(sVal)
			if !hasKind || !hasS || !kindOK || kindStr == "" || pyjson.IsBool(sVal) || !sOK || sNum <= 0 {
				badEntry = true
			}
		}
		if label != "where" {
			errAdd(errs, fmt.Sprintf("%s: not_within_s_after_mark is only valid in check.where (it filters the whole check)", where))
		} else if badEntry {
			errAdd(errs, fmt.Sprintf("%s: bad not_within_s_after_mark %s (want {\"kind\": <mark kind>, \"s\": seconds > 0}, or a list of them)",
				where, pythonRepr(spec)))
		}
	}
	if needKind && obj["kind"] == nil {
		// A filter without a `kind` matches every event kind: reject it.
		errAdd(errs, fmt.Sprintf("%s: %s needs a kind", where, label))
	}
	if kind := obj["kind"]; kind != nil {
		if !kindEnum[pyjson.Str(kind)] || !isStringValue(kind) {
			errAdd(errs, fmt.Sprintf("%s: bad kind %s", where, pythonRepr(kind)))
		}
	}
	for _, side := range []string{"src", "dst"} {
		val, present := obj[side]
		if !present || val == nil {
			continue
		}
		vals := []any{val}
		if list, ok := pyjson.List(val); ok {
			vals = list
		}
		for _, v := range vals {
			s, ok := v.(string)
			if !ok {
				errAdd(errs, fmt.Sprintf("%s: bad %s alias %s", where, side, pythonRepr(v)))
			} else if strings.HasPrefix(s, "$") && !unitAliases[s] && s != "$trigger.src" && s != "$trigger.dst" {
				errAdd(errs, fmt.Sprintf("%s: bad %s alias %s", where, side, pythonRepr(v)))
			} else if strings.HasPrefix(s, "mob:") && len(s) == 4 {
				errAdd(errs, fmt.Sprintf("%s: bad %s alias %s", where, side, pythonRepr(v)))
			}
		}
	}
	if mode := obj["response_dst"]; mode != nil {
		if s, ok := mode.(string); !ok || !responseDstModes[s] {
			errAdd(errs, fmt.Sprintf("%s: bad response_dst %s", where, pythonRepr(mode)))
		}
	}
	if units := obj["party_under_attack"]; units != nil {
		list, ok := pyjson.List(units)
		if !ok || len(list) == 0 {
			errAdd(errs, fmt.Sprintf("%s: bad party_under_attack %s (a non-empty list of unit aliases)", where, pythonRepr(units)))
		} else {
			for _, unitVal := range list {
				unit, ok := unitVal.(string)
				if !ok || unit == "" {
					errAdd(errs, fmt.Sprintf("%s: bad party_under_attack unit %s", where, pythonRepr(unitVal)))
				} else if unit == "$mob" {
					errAdd(errs, fmt.Sprintf("%s: party_under_attack wants party units, not $mob", where))
				} else if strings.HasPrefix(unit, "$") && !unitAliases[unit] {
					errAdd(errs, fmt.Sprintf("%s: bad party_under_attack unit %s", where, pythonRepr(unitVal)))
				} else if strings.HasPrefix(unit, "mob:") && len(unit) == 4 {
					errAdd(errs, fmt.Sprintf("%s: bad party_under_attack unit %s", where, pythonRepr(unitVal)))
				}
			}
		}
	}
	if spell := obj["spell"]; spell != nil {
		names := []any{spell}
		if list, ok := pyjson.List(spell); ok {
			names = list
		}
		for _, nm := range names {
			s, ok := nm.(string)
			if !ok || len(spells.IDsFor(s)) == 0 {
				errAdd(errs, fmt.Sprintf("%s: unknown spell %s", where, pythonRepr(nm)))
			}
		}
	}
	if sid := obj["spell_id"]; sid != nil {
		ids := []any{sid}
		if list, ok := pyjson.List(sid); ok {
			ids = list
		}
		for _, one := range ids {
			n, ok := pyjson.Int64(one)
			if !ok || !spells.HasID(n) {
				errAdd(errs, fmt.Sprintf("%s: unknown spell_id %s", where, pythonRepr(one)))
			}
		}
	}
	for _, key := range []string{"dst_hp_gt", "dst_hp_lt", "dst_hp_le", "src_hp_le", "src_power_lt", "dst_power_lt"} {
		if val := obj[key]; val != nil && !numOrThreshold(val) {
			errAdd(errs, fmt.Sprintf("%s: bad %s %s", where, key, pythonRepr(val)))
		}
	}
	for _, key := range []string{"t_from", "t_to", "src_within_of_marked", "trigger_debounce_s"} {
		if val := obj[key]; val != nil && !isNumVal(val) {
			errAdd(errs, fmt.Sprintf("%s: bad %s %s", where, key, pythonRepr(val)))
		}
	}
	for _, key := range []string{"dst_has_aura", "dst_lacks_aura"} {
		val := obj[key]
		if val == nil {
			continue
		}
		vals := []any{val}
		if list, ok := pyjson.List(val); ok {
			vals = list
		}
		for _, one := range vals {
			if !isIntStrict(one) {
				errAdd(errs, fmt.Sprintf("%s: bad %s %s", where, key, pythonRepr(one)))
			}
		}
	}
}

// isStringValue reports whether v is a JSON string (Python's isinstance(v, str)).
func isStringValue(v any) bool {
	_, ok := v.(string)
	return ok
}

func checkWhere(where any, trialID, rid string, errs *[]string, needKind bool) {
	if _, ok := pyjson.Object(where); !ok {
		errAdd(errs, fmt.Sprintf("%s/%s: where is not an object", trialID, rid))
		return
	}
	checkFilter(where, "where", trialID, rid, errs, needKind, true, nil)
}

func checkNumDen(doc any, label, trialID, rid string, errs *[]string, needKind bool) {
	checkFilter(doc, label, trialID, rid, errs, needKind, true, nil)
}

func checkRule(ruleVal any, trialID string, errs *[]string) {
	rule, ok := pyjson.Object(ruleVal)
	if !ok {
		errAdd(errs, fmt.Sprintf("%s: rule is not an object", trialID))
		return
	}
	ridVal := rule["id"]
	rid, ridOK := ridVal.(string)
	if !ridOK || rid == "" {
		errAdd(errs, fmt.Sprintf("%s: rule without an id", trialID))
		return
	}
	checkDict(rule, ruleFields, "rule", trialID, rid, errs)
	for _, key := range []string{"desc", "source", "expect_today", "check", "pass_if"} {
		if _, present := rule[key]; !present {
			errAdd(errs, fmt.Sprintf("%s/%s: missing %s", trialID, rid, key))
		}
	}
	if exp, ok := rule["expect_today"].(string); !ok || (exp != "PASS" && exp != "FAIL" && exp != "?") {
		errAdd(errs, fmt.Sprintf("%s/%s: bad expect_today %s", trialID, rid, pythonRepr(rule["expect_today"])))
	}
	if !pyjson.Truthy(rule["source"]) {
		errAdd(errs, fmt.Sprintf("%s/%s: empty source", trialID, rid))
	}
	checkVal := rule["check"]
	check, ok := pyjson.Object(checkVal)
	if !ok {
		errAdd(errs, fmt.Sprintf("%s/%s: check is not an object", trialID, rid))
		return
	}
	checkDict(check, checkFields, "check", trialID, rid, errs)
	metric, _ := check["metric"].(string)
	if !metrics[metric] || check["metric"] == nil {
		errAdd(errs, fmt.Sprintf("%s/%s: unknown metric %s", trialID, rid, pythonRepr(check["metric"])))
		return
	}
	// `kind` is required only where the dict is actually USED by the metric.
	whereKey := pyjson.ObjectOrEmpty(check)
	where := whereKey["where"]
	if where != nil || whereMetrics[metric] {
		doc := where
		if doc == nil {
			doc = map[string]any{}
		}
		checkWhere(doc, trialID, rid, errs, whereMetrics[metric])
	}
	for _, label := range []string{"num", "den"} {
		val := check[label]
		if val != nil || metric == "ratio" {
			doc := val
			if doc == nil {
				doc = map[string]any{}
			}
			checkNumDen(doc, label, trialID, rid, errs, metric == "ratio")
		}
	}
	if trig := check["trigger"]; trig != nil {
		checkTrigger(trig, trialID, rid, errs)
	}
	resp := check["response"]
	if resp != nil {
		if checkDict(resp, responseFields, "response", trialID, rid, errs) {
			checkFilter(resp, "response", trialID, rid, errs, metric == "latency", false, nil)
		}
	}
	if debounce := check["trigger_debounce_s"]; debounce != nil && !isNumVal(debounce) {
		errAdd(errs, fmt.Sprintf("%s/%s: bad trigger_debounce_s %s", trialID, rid, pythonRepr(debounce)))
	}
	if skip := check["skip_trigger_while"]; skip != nil {
		// bench-fix-1112: heal-latency skips triggers while the healer is out of mana or dead.
		trigObj, trigIsObj := pyjson.Object(check["trigger"])
		if metric != "latency" || !trigIsObj || pyjson.Str(trigObj["kind"]) != "cross" {
			errAdd(errs, fmt.Sprintf("%s/%s: skip_trigger_while only applies to a latency rule with a cross trigger", trialID, rid))
		} else {
			skipObj, skipIsObj := pyjson.Object(skip)
			bad := !skipIsObj
			if skipIsObj {
				for k := range skipObj {
					if k != "unit" && k != "power_below_pct" && k != "skip_when_dead" {
						bad = true
					}
				}
				if unit, _ := skipObj["unit"].(string); !crossUnits[unit] {
					bad = true
				}
			}
			if bad {
				errAdd(errs, fmt.Sprintf("%s/%s: bad skip_trigger_while %s (want {unit, power_below_pct, skip_when_dead})",
					trialID, rid, pythonRepr(skip)))
			} else {
				if limit := skipObj["power_below_pct"]; limit != nil {
					f, ok := pyjson.Num(limit)
					if pyjson.IsBool(limit) || !ok || !(0 < f && f < 100) {
						errAdd(errs, fmt.Sprintf("%s/%s: bad skip_trigger_while power_below_pct %s (a percent 0-100)",
							trialID, rid, pythonRepr(limit)))
					}
				}
				if v, present := skipObj["skip_when_dead"]; present && !(pyjson.IsBool(v) && pyjson.Truthy(v)) {
					errAdd(errs, fmt.Sprintf("%s/%s: skip_trigger_while skip_when_dead must be true (or absent)", trialID, rid))
				}
			}
		}
	}
	cd := check["debounce_responder_s"]
	if cd != nil {
		// Latency rules skip triggers inside the responder's own cooldown; a `first` rule
		// with an across-reps cooldown reads the same spans as no opportunity instead.
		if metric != "latency" && metric != "first" {
			errAdd(errs, fmt.Sprintf("%s/%s: debounce_responder_s only applies to a latency or first rule", trialID, rid))
		}
		if metric == "first" && check["trigger"] != nil {
			errAdd(errs, fmt.Sprintf("%s/%s: debounce_responder_s on a first rule takes no trigger", trialID, rid))
		}
		f, _ := pyjson.Num(cd)
		if pyjson.IsBool(cd) || !pyjson.IsNum(cd) || f <= 0 {
			errAdd(errs, fmt.Sprintf("%s/%s: bad debounce_responder_s %s (a number of seconds > 0)", trialID, rid, pythonRepr(cd)))
		}
	}
	per := check["per"]
	if per != nil {
		if s, ok := per.(string); metric != "rate" || !ok || s != "mob_alive" {
			errAdd(errs, fmt.Sprintf("%s/%s: bad per %s (only \"mob_alive\", only on a rate rule)", trialID, rid, pythonRepr(per)))
		}
	}
	minAlive := check["min_alive_s"]
	if minAlive != nil {
		if per == nil {
			errAdd(errs, fmt.Sprintf("%s/%s: min_alive_s needs per: mob_alive", trialID, rid))
		}
		f, _ := pyjson.Num(minAlive)
		if pyjson.IsBool(minAlive) || !pyjson.IsNum(minAlive) || f <= 0 {
			errAdd(errs, fmt.Sprintf("%s/%s: bad min_alive_s %s (seconds > 0)", trialID, rid, pythonRepr(minAlive)))
		}
	}
	if across := check["debounce_responder_across_reps"]; across != nil {
		if !(pyjson.IsBool(across) && pyjson.Truthy(across)) {
			errAdd(errs, fmt.Sprintf("%s/%s: debounce_responder_across_reps must be true (or absent)", trialID, rid))
		}
		if cd == nil {
			errAdd(errs, fmt.Sprintf("%s/%s: debounce_responder_across_reps needs debounce_responder_s", trialID, rid))
		}
	}
	if cds := check["debounce_responder_spells"]; cds != nil {
		if cd == nil {
			errAdd(errs, fmt.Sprintf("%s/%s: debounce_responder_spells needs debounce_responder_s", trialID, rid))
		}
		var respSpells any
		if respObj, ok := pyjson.Object(resp); ok {
			respSpells = respObj["spell"]
		}
		if respSpells == nil && metric == "first" {
			// a `first` rule has no `response` block: the answered ability is the `where` filter itself
			if whereObj, ok := pyjson.Object(where); ok {
				respSpells = whereObj["spell"]
			}
		}
		list, okList := pyjson.List(cds)
		good := okList && len(list) > 0
		if good {
			for _, x := range list {
				if s, ok := x.(string); !ok || s == "" {
					good = false
				}
			}
		}
		if !good {
			errAdd(errs, fmt.Sprintf("%s/%s: bad debounce_responder_spells %s (a non-empty list of spell names)",
				trialID, rid, pythonRepr(cds)))
		} else {
			for _, x := range list {
				name := pyjson.Str(x)
				if !containsAny(respSpells, name) {
					errAdd(errs, fmt.Sprintf("%s/%s: debounce_responder_spells %s is not one of the answered spells", trialID, rid, pythonRepr(name)))
				}
			}
		}
	}
	if within := check["within_s"]; within != nil && !isNumVal(within) {
		errAdd(errs, fmt.Sprintf("%s/%s: bad within_s %s", trialID, rid, pythonRepr(within)))
	}
	if metric == "dummy" {
		spell, _ := check["spell"].(string)
		if spell == "" {
			errAdd(errs, fmt.Sprintf("%s/%s: dummy check needs a spell name", trialID, rid))
		}
		stat, _ := check["stat"].(string)
		if stat != "damage" && stat != "casts" && stat != "reaction" {
			errAdd(errs, fmt.Sprintf("%s/%s: dummy check stat must be damage, casts or reaction %s", trialID, rid, pythonRepr(check["stat"])))
		}
		for _, key := range []string{"where", "num", "den", "trigger", "response", "per",
			"min_alive_s", "name", "aura", "unit", "phase", "alive", "triggers", "min_n",
			"within_s", "response_dst", "same_dst", "trigger_debounce_s", "skip_trigger_while",
			"debounce_responder_s", "debounce_responder_spells", "debounce_responder_across_reps"} {
			if check[key] != nil {
				errAdd(errs, fmt.Sprintf("%s/%s: dummy check takes only metric, spell and stat", trialID, rid))
				break
			}
		}
		return
	}
	if rd := check["response_dst"]; rd != nil {
		if s, ok := rd.(string); !ok || !responseDstModes[s] {
			errAdd(errs, fmt.Sprintf("%s/%s: bad response_dst %s", trialID, rid, pythonRepr(rd)))
		}
	}
	passIfVal, okPassIf := rule["pass_if"].(string)
	if !okPassIf || !passIfRe.MatchString(strings.TrimSpace(passIfVal)) {
		errAdd(errs, fmt.Sprintf("%s/%s: bad pass_if %s", trialID, rid, pythonRepr(rule["pass_if"])))
	}
	if _, present := rule["min_reps"]; present {
		mr := rule["min_reps"]
		if !isIntStrict(mr) {
			errAdd(errs, fmt.Sprintf("%s/%s: bad min_reps %s (an int >= 1)", trialID, rid, pythonRepr(mr)))
		} else if n, _ := pyjson.Int64(mr); n < 1 {
			errAdd(errs, fmt.Sprintf("%s/%s: bad min_reps %s (an int >= 1)", trialID, rid, pythonRepr(mr)))
		}
	}
	if _, present := rule["pool"]; present {
		pool := rule["pool"]
		if !(pyjson.IsBool(pool) && pyjson.Truthy(pool)) {
			errAdd(errs, fmt.Sprintf("%s/%s: bad pool %s (true, or absent)", trialID, rid, pythonRepr(pool)))
		}
		if metric != "count" && metric != "ratio" && metric != "latency" {
			errAdd(errs, fmt.Sprintf("%s/%s: pool applies only to a count, ratio or latency rule", trialID, rid))
		}
	}
}

// containsAny is Python's `name in resp_spells` for a string or a list of strings.
func containsAny(haystack any, needle string) bool {
	switch val := haystack.(type) {
	case string:
		return val == needle
	case []any:
		for _, item := range val {
			if s, ok := item.(string); ok && s == needle {
				return true
			}
		}
	}
	return false
}

func checkTrigger(trig any, trialID, rid string, errs *[]string) {
	if !checkDict(trig, triggerFields, "trigger", trialID, rid, errs) {
		return
	}
	obj := pyjson.ObjectOrEmpty(trig)
	kind := pyjson.Str(obj["kind"])
	switch {
	case !triggerKinds[kind] || obj["kind"] == nil:
		errAdd(errs, fmt.Sprintf("%s/%s: bad trigger kind %s", trialID, rid, pythonRepr(obj["kind"])))
		return
	case kind == "cross":
		if unit, _ := obj["unit"].(string); !crossUnits[unit] {
			errAdd(errs, fmt.Sprintf("%s/%s: bad trigger unit %s", trialID, rid, pythonRepr(obj["unit"])))
		}
		if !numOrThreshold(obj["below"]) {
			errAdd(errs, fmt.Sprintf("%s/%s: bad trigger below %s", trialID, rid, pythonRepr(obj["below"])))
		}
		return
	case kind == "mark":
		if len(obj) != 2 || obj["kind"] == nil || obj["mark"] == nil {
			errAdd(errs, fmt.Sprintf("%s/%s: a mark trigger takes only kind and mark", trialID, rid))
		} else if mark, ok := obj["mark"].(string); !ok || mark == "" {
			errAdd(errs, fmt.Sprintf("%s/%s: bad trigger mark %s", trialID, rid, pythonRepr(obj["mark"])))
		}
		return
	}
	// Event trigger: the kind FILTERS the rows; the where-style fields apply.
	for _, key := range []string{"unit", "below"} {
		if _, present := obj[key]; present {
			errAdd(errs, fmt.Sprintf("%s/%s: trigger %s is only for cross triggers", trialID, rid, key))
		}
	}
	checkFilter(trig, "trigger", trialID, rid, errs, true, false, nil)
}

func checkExtraBots(extraVal any, tid string, errs *[]string) {
	extra, ok := pyjson.List(extraVal)
	if !ok {
		errAdd(errs, fmt.Sprintf("%s: extra_bots must be a list", tid))
		return
	}
	if len(extra) == 0 {
		errAdd(errs, fmt.Sprintf("%s: extra_bots must not be empty (leave it out)", tid))
	}
	if len(extra) > MaxExtraBots {
		errAdd(errs, fmt.Sprintf("%s: extra_bots holds %d bots, at most %d", tid, len(extra), MaxExtraBots))
	}
	for i, botVal := range extra {
		label := fmt.Sprintf("extra_bots[%d]", i+1)
		bot, ok := pyjson.Object(botVal)
		if !ok {
			errAdd(errs, fmt.Sprintf("%s: %s is not an object", tid, label))
			continue
		}
		for _, key := range sortedKeys(bot) {
			if !extraBotFields[key] {
				errAdd(errs, fmt.Sprintf("%s: unknown %s field %s", tid, label, pythonRepr(key)))
			}
		}
		for _, key := range []string{"class", "spec", "role", "level"} {
			if _, present := bot[key]; !present {
				errAdd(errs, fmt.Sprintf("%s: %s missing %s", tid, label, key))
			}
		}
		if class, present := bot["class"]; present && !idRe.MatchString(pyjson.Str(class)) {
			errAdd(errs, fmt.Sprintf("%s: bad %s class", tid, label))
		}
		if spec, present := bot["spec"]; present {
			if s, ok := spec.(string); !ok || !specIDs[s] {
				errAdd(errs, fmt.Sprintf("%s: bad %s spec %s", tid, label, pythonRepr(spec)))
			}
		}
		if role, present := bot["role"]; present {
			if s, ok := role.(string); !ok || (s != "tank" && s != "healer" && s != "dps") {
				errAdd(errs, fmt.Sprintf("%s: bad %s role %s", tid, label, pythonRepr(role)))
			}
		}
		if lvlVal, present := bot["level"]; present {
			if !isIntStrict(lvlVal) {
				errAdd(errs, fmt.Sprintf("%s: %s level must be an int 1-60", tid, label))
			} else if lvl, _ := pyjson.Int64(lvlVal); lvl < 1 || lvl > 60 {
				errAdd(errs, fmt.Sprintf("%s: %s level must be an int 1-60", tid, label))
			}
		}
		if gearVal, present := bot["gear"]; present && gearVal != nil {
			if s, ok := gearVal.(string); !ok || !gearRe.MatchString(s) {
				errAdd(errs, fmt.Sprintf("%s: bad %s gear %s", tid, label, pythonRepr(gearVal)))
			}
		}
		itemsVal := bot["items"]
		if itemsVal != nil {
			items, ok := pyjson.List(itemsVal)
			if !ok {
				errAdd(errs, fmt.Sprintf("%s: %s items is not a list", tid, label))
				continue
			}
			for _, itemVal := range items {
				item, ok := pyjson.Object(itemVal)
				if !ok {
					errAdd(errs, fmt.Sprintf("%s: %s item needs a name", tid, label))
					continue
				}
				name, okName := item["name"].(string)
				if !okName || name == "" {
					errAdd(errs, fmt.Sprintf("%s: %s item needs a name", tid, label))
					continue
				}
				for _, key := range sortedKeys(item) {
					if !extraItemFields[key] {
						errAdd(errs, fmt.Sprintf("%s: unknown %s item field %s", tid, label, pythonRepr(key)))
					}
				}
				count := any(int64(1))
				if c, present := item["count"]; present {
					count = c
				}
				if !isIntStrict(count) {
					errAdd(errs, fmt.Sprintf("%s: %s item count must be an int 1-20", tid, label))
				} else if n, _ := pyjson.Int64(count); n < 1 || n > 20 {
					errAdd(errs, fmt.Sprintf("%s: %s item count must be an int 1-20", tid, label))
				}
			}
		}
	}
}

func extraAliasUses(node any, found map[string]bool) {
	switch val := node.(type) {
	case map[string]any:
		for _, v := range val {
			extraAliasUses(v, found)
		}
	case []any:
		for _, v := range val {
			extraAliasUses(v, found)
		}
	case string:
		if strings.HasPrefix(val, "$extra") {
			found[val] = true
		}
	}
}

// FindTrialByID returns the trial (or merged variant) with this id from the embedded trial
// files, else nil.
func FindTrialByID(tid string) Trial {
	baseID, name := SplitVariantID(tid)
	for _, path := range TrialFiles() {
		cand, err := LoadTrial(path)
		if err != nil {
			continue
		}
		if id, _ := cand["id"].(string); id != baseID {
			continue
		}
		if name == "" {
			return cand
		}
		for _, variant := range ExpandVariants(cand) {
			if variant.Name == name {
				return variant.Trial
			}
		}
		return nil
	}
	return nil
}

// FindFixtureByID looks a trial up in the embedded _fixtures set (the pre-snapshot fallback).
func FindFixtureByID(tid string) Trial {
	for _, path := range FixtureFiles() {
		cand, err := LoadTrial(path)
		if err != nil {
			continue
		}
		if id, _ := cand["id"].(string); id == tid {
			return cand
		}
	}
	return nil
}

// SplitVariantID splits 'trial@variant' into ('trial', 'variant'); a plain id gives ("", "").
func SplitVariantID(tid string) (string, string) {
	if idx := strings.Index(tid, VariantSep); idx >= 0 {
		return tid[:idx], tid[idx+len(VariantSep):]
	}
	return tid, ""
}

// NamedTrial is one expanded variant.
type NamedTrial struct {
	Name  string
	Trial Trial
}

// DeepMerge is a copy of base with override laid over it: objects merge key by key, anything
// else replaces.
func DeepMerge(base, override any) any {
	baseObj, baseOK := pyjson.Object(base)
	overObj, overOK := pyjson.Object(override)
	if baseOK && overOK {
		out := deepCopy(baseObj).(map[string]any)
		for key, val := range overObj {
			if existing, ok := baseObj[key]; ok {
				out[key] = DeepMerge(existing, val)
			} else {
				out[key] = deepCopy(val)
			}
		}
		return out
	}
	return deepCopy(override)
}

func deepCopy(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[k] = deepCopy(item)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = deepCopy(item)
		}
		return out
	}
	return v
}

// ApplyOverride merges one variant override onto the base trial; the merged trial keeps the
// BASE id (the caller names it).
func ApplyOverride(base Trial, override any) (Trial, []string) {
	var errs []string
	tid := "?"
	if s, ok := base["id"].(string); ok {
		tid = s
	}
	over, ok := pyjson.Object(override)
	if !ok || len(over) == 0 {
		return nil, []string{fmt.Sprintf("%s: variant override must be a non-empty object", tid)}
	}
	allowedKeys := make([]string, 0, len(overrideFields))
	for k := range overrideFields {
		allowedKeys = append(allowedKeys, k)
	}
	sort.Strings(allowedKeys)
	for _, key := range sortedKeys(over) {
		if !overrideFields[key] {
			errs = append(errs, fmt.Sprintf("%s: variant override may not touch %s (allowed: %s)",
				tid, pythonRepr(key), strings.Join(allowedKeys, ", ")))
		}
	}
	for _, key := range []string{"player", "bot"} {
		frozen := overrideFrozen[key]
		part, present := over[key]
		if obj, ok := pyjson.Object(part); ok {
			hit := []string{}
			for _, fld := range frozen {
				if _, has := obj[fld]; has {
					hit = append(hit, fld)
				}
			}
			sort.Strings(hit)
			for _, fld := range hit {
				errs = append(errs, fmt.Sprintf("%s: variant override may not change %s.%s", tid, key, fld))
			}
		} else if present {
			errs = append(errs, fmt.Sprintf("%s: variant override %s must be an object", tid, key))
		}
	}
	baseBot, _ := pyjson.Object(base["bot"])
	overrideBot, _ := pyjson.Object(over["bot"])
	var baseSpec, wantSpec any
	if baseBot != nil {
		baseSpec = baseBot["spec"]
	}
	if overrideBot != nil {
		wantSpec = overrideBot["spec"]
	}
	if rulesVal, present := over["rules"]; present {
		have := map[string]bool{}
		if ruleList, ok := pyjson.List(base["rules"]); ok {
			for _, ruleVal := range ruleList {
				if rule, ok := pyjson.Object(ruleVal); ok {
					have[pyjson.Str(rule["id"])] = true
				}
			}
		}
		tweaks, ok := pyjson.Object(rulesVal)
		if !ok || len(tweaks) == 0 {
			errs = append(errs, fmt.Sprintf("%s: variant override rules must be {rule id: {thresholds}}", tid))
		} else {
			rids := make([]string, 0, len(tweaks))
			for rid := range tweaks {
				rids = append(rids, rid)
			}
			sort.Strings(rids)
			for _, rid := range rids {
				tweak := tweaks[rid]
				if !have[rid] {
					errs = append(errs, fmt.Sprintf("%s: variant override names unknown rule %s", tid, pythonRepr(rid)))
					continue
				}
				tweakObj, ok := pyjson.Object(tweak)
				if !ok || len(tweakObj) == 0 {
					errs = append(errs, fmt.Sprintf("%s/%s: variant rule override must be a non-empty object", tid, rid))
					continue
				}
				for _, fld := range sortedKeys(tweakObj) {
					if ruleTextFields[fld] {
						if wantSpec == nil || wantSpec == baseSpec {
							errs = append(errs, fmt.Sprintf("%s/%s: variant may rewrite rule field %s only with bot.spec", tid, rid, pythonRepr(fld)))
						}
						continue
					}
					if !ruleOverrideFields[fld] {
						allowed := make([]string, 0, len(ruleOverrideFields))
						for k := range ruleOverrideFields {
							allowed = append(allowed, k)
						}
						sort.Strings(allowed)
						errs = append(errs, fmt.Sprintf("%s/%s: variant may not change rule field %s (only %s)",
							tid, rid, pythonRepr(fld), strings.Join(allowed, ", ")))
					}
				}
				if v, present := tweakObj["pass_if"]; present {
					if s, ok := v.(string); !ok || !passIfRe.MatchString(strings.TrimSpace(s)) {
						errs = append(errs, fmt.Sprintf("%s/%s: bad variant pass_if %s", tid, rid, pythonRepr(v)))
					}
				}
				if v, present := tweakObj["min_n"]; present {
					if n, ok := pyjson.Int64(v); pyjson.IsBool(v) || !ok || n < 1 {
						errs = append(errs, fmt.Sprintf("%s/%s: variant min_n must be an int >= 1", tid, rid))
					}
				}
				if v, present := tweakObj["min_reps"]; present {
					if n, ok := pyjson.Int64(v); pyjson.IsBool(v) || !ok || n < 1 {
						errs = append(errs, fmt.Sprintf("%s/%s: variant min_reps must be an int >= 1", tid, rid))
					}
				}
				if v, present := tweakObj["min_opportunity_s"]; present {
					f, ok := pyjson.Num(v)
					if pyjson.IsBool(v) || !ok || f < 0 {
						errs = append(errs, fmt.Sprintf("%s/%s: variant min_opportunity_s must be a number >= 0", tid, rid))
					}
				}
			}
		}
	}
	lvlVal, hasLevel := over["level"]
	if hasLevel {
		if n, ok := pyjson.Int64(lvlVal); pyjson.IsBool(lvlVal) || !ok || n < 1 || n > 60 {
			errs = append(errs, fmt.Sprintf("%s: variant level must be an int 1-60", tid))
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	merged := deepCopy(base).(map[string]any)
	delete(merged, "variants")
	if hasLevel {
		n, _ := pyjson.Int64(lvlVal)
		player := pyjson.ObjectOrEmpty(merged["player"])
		if _, ok := merged["player"]; !ok {
			merged["player"] = player
		}
		player["level"] = n
		bot := pyjson.ObjectOrEmpty(merged["bot"])
		if _, ok := merged["bot"]; !ok {
			merged["bot"] = bot
		}
		bot["level"] = n
		if extraList, ok := pyjson.List(merged["extra_bots"]); ok {
			for _, botVal := range extraList {
				if extraBot, ok := pyjson.Object(botVal); ok {
					extraBot["level"] = n
				}
			}
		}
	}
	for _, key := range []string{"player", "bot", "situation"} {
		if val, present := over[key]; present {
			existing := merged[key]
			if existing == nil {
				existing = map[string]any{}
			}
			merged[key] = DeepMerge(existing, val)
		}
	}
	if val, present := over["extra_bots"]; present {
		merged["extra_bots"] = deepCopy(val)
	}
	if rulesVal, present := over["rules"]; present {
		tweaks, _ := pyjson.Object(rulesVal)
		if ruleList, ok := pyjson.List(merged["rules"]); ok {
			for _, ruleVal := range ruleList {
				rule, ok := pyjson.Object(ruleVal)
				if !ok {
					continue
				}
				if tweak, ok := tweaks[pyjson.Str(rule["id"])]; ok {
					if tweakObj, ok := pyjson.Object(tweak); ok {
						for k, v := range tweakObj {
							rule[k] = deepCopy(v)
						}
					}
				}
			}
		}
	}
	return merged, nil
}

// Hires lists the main hire and every extra hire as (label, class, spec, level).
func Hires(trial Trial) []struct {
	Label string
	Class string
	Spec  string
	Level any
} {
	var out []struct {
		Label string
		Class string
		Spec  string
		Level any
	}
	bot := pyjson.ObjectOrEmpty(trial["bot"])
	out = append(out, struct {
		Label string
		Class string
		Spec  string
		Level any
	}{"bot", pyjson.Str(trial["class"]), pyjson.Str(trial["spec"]), bot["level"]})
	if extras, ok := pyjson.List(trial["extra_bots"]); ok {
		for i, exVal := range extras {
			ex, ok := pyjson.Object(exVal)
			if !ok {
				continue
			}
			out = append(out, struct {
				Label string
				Class string
				Spec  string
				Level any
			}{fmt.Sprintf("extra_bots[%d]", i+1), pyjson.Str(ex["class"]), pyjson.Str(ex["spec"]), ex["level"]})
		}
	}
	return out
}

// MergeVariant returns the merged trial of one variant entry, or the errors it has: the shape
// of the entry, the override, the ordinary validation of the merged trial, the hire level
// floor, and the rules list left as it was.
func MergeVariant(trial Trial, varVal any) (Trial, []string) {
	tid := "?"
	if s, ok := trial["id"].(string); ok {
		tid = s
	}
	var nameVal any
	if obj, ok := pyjson.Object(varVal); ok {
		nameVal = obj["name"]
	}
	name := pyjson.Str(nameVal)
	label := fmt.Sprintf("%s variants[%s]", tid, name)
	obj, ok := pyjson.Object(varVal)
	if !ok {
		return nil, []string{fmt.Sprintf("%s: not an object", label)}
	}
	var errs []string
	for _, key := range sortedKeys(obj) {
		if !variantFields[key] {
			errs = append(errs, fmt.Sprintf("%s: unknown field %s", label, pythonRepr(key)))
		}
	}
	nameStr, okName := nameVal.(string)
	if !okName || !idRe.MatchString(nameStr) {
		return nil, append(errs, fmt.Sprintf("%s: bad name %s (lower-case letters, digits and -)", label, pythonRepr(nameVal)))
	}
	if title, present := obj["title"]; present {
		if _, ok := title.(string); !ok {
			errs = append(errs, fmt.Sprintf("%s: title must be a string", label))
		}
	}
	base := Trial{}
	for k, v := range trial {
		if k != "variants" {
			base[k] = v
		}
	}
	merged, oerrs := ApplyOverride(base, obj["override"])
	if len(oerrs) > 0 {
		for _, e := range oerrs {
			rest := e
			if idx := strings.Index(e, ": "); idx >= 0 {
				rest = e[idx+2:]
			}
			errs = append(errs, fmt.Sprintf("%s: %s", label, rest))
		}
		return nil, errs
	}
	for _, e := range Validate(merged) {
		errs = append(errs, fmt.Sprintf("%s: merged trial: %s", label, e))
	}
	for _, hire := range Hires(merged) {
		if _, markerOK := talents.MarkerFor(hire.Class, hire.Spec); markerOK {
			if lvl, ok := pyjson.Int64(hire.Level); ok && !pyjson.IsBool(hire.Level) && lvl < MinHireLevel {
				errs = append(errs, fmt.Sprintf("%s: %s is level %d: below level %d no hire can carry the talent marker its build is proven by (the premade link of that level stops short of it), so every repeat would be INVALID",
					label, hire.Label, lvl, MinHireLevel))
			}
		}
	}
	if !sameRuleIDs(merged, trial) {
		errs = append(errs, fmt.Sprintf("%s: the variant changed the rules list", label))
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return merged, nil
}

func sameRuleIDs(a, b Trial) bool {
	ids := func(t Trial) []string {
		var out []string
		if list, ok := pyjson.List(t["rules"]); ok {
			for _, ruleVal := range list {
				rule, ok := pyjson.Object(ruleVal)
				if !ok {
					out = append(out, "")
					continue
				}
				out = append(out, pyjson.Str(rule["id"]))
			}
		}
		return out
	}
	left, right := ids(a), ids(b)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// CheckVariants returns the errors for the `variants` list: its shape, then every variant's
// merged trial (ordinary validation).
func CheckVariants(trial Trial, tid string) []string {
	var errs []string
	if pyjson.Str(trial["group"]) == "raid" {
		return []string{fmt.Sprintf("%s: raid trials take no variants", tid)}
	}
	variants, ok := pyjson.List(trial["variants"])
	if !ok || len(variants) == 0 {
		return []string{fmt.Sprintf("%s: variants must be a non-empty list (leave it out)", tid)}
	}
	if len(variants) > MaxVariants {
		errs = append(errs, fmt.Sprintf("%s: %d variants, at most %d", tid, len(variants), MaxVariants))
	}
	seen := map[string]bool{}
	for _, varVal := range variants {
		var name any
		if obj, ok := pyjson.Object(varVal); ok {
			name = obj["name"]
		}
		key := pythonRepr(name)
		if s, ok := name.(string); ok {
			if seen[s] {
				errs = append(errs, fmt.Sprintf("%s: duplicate variant name %s", tid, pythonRepr(s)))
			}
			seen[s] = true
		}
		_ = key
		_, verrs := MergeVariant(trial, varVal)
		errs = append(errs, verrs...)
	}
	return errs
}

// ExpandVariants lists the VALID variants of a trial, ids `<trial>@<variant>`. A trial without
// variants (or with a bad list) gives none; Validate reports the problems.
func ExpandVariants(trial Trial) []NamedTrial {
	var out []NamedTrial
	if trial == nil || pyjson.Str(trial["group"]) == "raid" {
		return out
	}
	variants, ok := pyjson.List(trial["variants"])
	if !ok {
		return out
	}
	for _, varVal := range variants {
		merged, errs := MergeVariant(trial, varVal)
		if len(errs) > 0 || merged == nil {
			continue
		}
		obj, _ := pyjson.Object(varVal)
		name, _ := obj["name"].(string)
		merged["id"] = fmt.Sprintf("%s%s%s", pyjson.Str(trial["id"]), VariantSep, name)
		bot := pyjson.ObjectOrEmpty(merged["bot"])
		botSpec, botHasSpec := bot["spec"].(string)
		baseSpec, baseHasSpec := trial["spec"].(string)
		if botHasSpec && (!baseHasSpec || botSpec != baseSpec) {
			merged["spec"] = botSpec
		}
		if title := pyjson.Str(obj["title"]); pyjson.Truthy(obj["title"]) {
			if botSpec != pyjson.Str(trial["spec"]) {
				merged["title"] = obj["title"]
			} else {
				baseTitle := pyjson.Str(trial["title"])
				if !pyjson.Truthy(trial["title"]) {
					baseTitle = pyjson.Str(trial["id"])
				}
				merged["title"] = fmt.Sprintf("%s [%s]", baseTitle, title)
			}
		}
		out = append(out, NamedTrial{Name: name, Trial: merged})
	}
	return out
}

// LoadSelected loads `path` (the base trial) or `path@variant` (the merged variant).
func LoadSelected(spec string) (Trial, error) {
	if _, err := os.Stat(spec); err != nil && strings.Contains(spec, VariantSep) {
		idx := strings.LastIndex(spec, VariantSep)
		path, name := spec[:idx], spec[idx+len(VariantSep):]
		trial, err := LoadTrial(path)
		if err != nil {
			return nil, err
		}
		for _, variant := range ExpandVariants(trial) {
			if variant.Name == name {
				return variant.Trial, nil
			}
		}
		return nil, fmt.Errorf("%s: no valid variant %s", path, pythonRepr(name))
	}
	return LoadTrial(spec)
}

// pythonReprString is Python's repr() of a string: single quotes unless the text holds a
// single quote and no double quote, with backslash escapes for quotes and control characters.
func pythonReprString(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			switch {
			case r < 0x20 || r == 0x7f:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r <= 0xff && !unicode.IsPrint(r):
				fmt.Fprintf(&b, `\x%02x`, r)
			case r <= 0xffff && !unicode.IsPrint(r):
				fmt.Fprintf(&b, `\u%04x`, r)
			case !unicode.IsPrint(r):
				fmt.Fprintf(&b, `\U%08x`, r)
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// pythonRepr renders a JSON value the way Python's %r does for the scalars validation prints.
func pythonRepr(v any) string {
	switch val := v.(type) {
	case nil:
		return "None"
	case string:
		return pythonReprString(val)
	case bool:
		if val {
			return "True"
		}
		return "False"
	case int64:
		return pyjson.Str(val)
	case float64:
		return pyjson.FormatFloat(val)
	case []any:
		parts := make([]string, 0, len(val))
		for _, item := range val {
			parts = append(parts, pythonRepr(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := sortedKeys(val)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, "'"+k+"': "+pythonRepr(val[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}
