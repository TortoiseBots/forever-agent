package scenario

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/pjerra/forever-agent/internal/trials/gear"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

func numLenient(v any) bool {
	_, ok := pyjson.Num(v)
	return ok
}

func floatOf(v any) float64 {
	f, _ := pyjson.Num(v)
	return f
}

// Validate returns a list of error strings (empty = valid).
func Validate(trialVal any) []string {
	var errs []string
	trial, ok := pyjson.Object(trialVal)
	if !ok {
		return []string{"trial is not an object"}
	}
	tidVal := trial["id"]
	if tidVal == nil {
		tidVal = "?"
	}
	tid := pyjson.Str(tidVal)
	if s, ok := tidVal.(string); !ok || !idRe.MatchString(s) {
		errAdd(&errs, fmt.Sprintf("%s: bad id", tid))
	}
	for _, key := range sortedKeys(trial) {
		if !topFields[key] {
			errAdd(&errs, fmt.Sprintf("%s: unknown top-level field %s", tid, pythonRepr(key)))
		}
	}
	group := pyjson.Str(trial["group"])
	if !groups[group] || trial["group"] == nil {
		errAdd(&errs, fmt.Sprintf("%s: bad group %s", tid, pythonRepr(trial["group"])))
		return errs
	}
	if group == "raid" {
		if _, present := trial["variants"]; present {
			errAdd(&errs, fmt.Sprintf("%s: raid trials take no variants", tid))
		}
		for _, key := range []string{"boss", "live"} {
			if _, present := trial[key]; !present {
				errAdd(&errs, fmt.Sprintf("%s: raid trial missing %s", tid, key))
			}
		}
		if boss, ok := pyjson.Object(trial["boss"]); !ok || boss["entry"] == nil {
			errAdd(&errs, fmt.Sprintf("%s: raid trial boss needs an entry", tid))
		}
		live := pyjson.Str(trial["live"])
		if live != "practice" && live != "walk" {
			errAdd(&errs, fmt.Sprintf("%s: raid trial live must be practice|walk", tid))
		}
		_ = pyjson.Str(trial["spec"])
	} else {
		for _, key := range []string{"class", "spec", "player", "bot", "situation", "rules"} {
			if _, present := trial[key]; !present {
				errAdd(&errs, fmt.Sprintf("%s: missing %s", tid, key))
			}
		}
		player, _ := objectOrFalsyEmpty(trial["player"], playerFields, "player", tid, &errs)
		bot, _ := objectOrFalsyEmpty(trial["bot"], botFields, "bot", tid, &errs)
		if specVal, ok := trial["spec"].(string); !ok || !specIDs[specVal] {
			errAdd(&errs, fmt.Sprintf("%s: bad bot spec %s", tid, pythonRepr(trial["spec"])))
		}
		if !idRe.MatchString(pyjson.Str(trial["class"])) {
			errAdd(&errs, fmt.Sprintf("%s: bad bot class", tid))
		}
		talentsVal := player["talents"]
		talents := map[string]any{}
		if pyjson.Truthy(talentsVal) {
			if obj, isObj := pyjson.Object(talentsVal); isObj {
				talents = obj
				checkDict(talents, talentsFields, "talents", tid, "", &errs)
			} else {
				errAdd(&errs, fmt.Sprintf("%s: player talents is not an object", tid))
			}
		}
		tree := pyjson.Str(talents["tree"])
		tspells, spellsOK := pyjson.List(talents["spells"])
		lowLevel := false
		if lvl, ok := pyjson.Float(player["level"]); ok {
			if player["level"] != nil {
				lowLevel = int64(lvl) < 10
			}
		}
		if !spellsOK || (len(tspells) == 0 && !(tree == "none" && lowLevel)) {
			errAdd(&errs, fmt.Sprintf("%s: talents.spells must be a non-empty list (tree none + level < 10 only)", tid))
		} else {
			for _, sp := range tspells {
				if !isIntStrict(sp) {
					errAdd(&errs, fmt.Sprintf("%s: talents.spells must be a list of spell ids", tid))
					break
				}
			}
		}
		if stance := player["stance"]; stance != nil && !isIntLike(stance) {
			errAdd(&errs, fmt.Sprintf("%s: player.stance must be an aura id or null", tid))
		}
		if role := player["forced_role"]; role != nil {
			if s, ok := role.(string); !ok || (s != "tank" && s != "healer" && s != "dps") {
				errAdd(&errs, fmt.Sprintf("%s: bad forced_role %s", tid, pythonRepr(role)))
			}
		}
		if gearVal := player["gear"]; gearVal != nil {
			if s, ok := gearVal.(string); !ok || s != "tier" {
				errAdd(&errs, fmt.Sprintf("%s: bad player.gear %s (null or \"tier\")", tid, pythonRepr(gearVal)))
			} else {
				items, _, why := gear.PickKit(player)
				if len(items) == 0 {
					errAdd(&errs, fmt.Sprintf("%s: player.gear tier: %s", tid, why))
				}
			}
		}
		sit, sitValid := objectOrFalsyEmpty(trial["situation"], sitFields, "situation", tid, &errs)
		if sitValid {
			kind := pyjson.Str(sit["kind"])
			switch kind {
			case "tank_damage", "member_damage", "pack", "cc_target",
				"ooc_buffs", "ooc_hurt", "mana_pressure", "caster_mob", "debuff", "dummy":
			default:
				errAdd(&errs, fmt.Sprintf("%s: bad situation.kind %s", tid, pythonRepr(sit["kind"])))
			}
			if kind == "dummy" {
				if _, ok := pyjson.Object(sit["mobs"]); ok {
					errAdd(&errs, fmt.Sprintf("%s: dummy takes no mobs block (one dummy_entry target)", tid))
				}
				dummyEntry, hasEntry := sit["dummy_entry"]
				if !hasEntry || !isIntStrict(dummyEntry) || floatOf(dummyEntry) < 1 {
					errAdd(&errs, fmt.Sprintf("%s: dummy_entry must be a creature entry id", tid))
				}
				rotation, ok := pyjson.List(sit["rotation"])
				if !ok || len(rotation) == 0 {
					errAdd(&errs, fmt.Sprintf("%s: rotation must be a non-empty list of spell names", tid))
				} else {
					for _, item := range rotation {
						if s, ok := item.(string); !ok || s == "" {
							errAdd(&errs, fmt.Sprintf("%s: rotation must be a non-empty list of spell names", tid))
							break
						}
					}
				}
			} else {
				mobsVal := sit["mobs"]
				mobs := pyjson.ObjectOrEmpty(mobsVal)
				if _, ok := pyjson.Object(mobsVal); ok {
					checkDict(mobsVal, mobsFields, "mobs", tid, "", &errs)
				}
				role := pyjson.Str(mobs["role"])
				if role != "melee" && role != "caster" && role != "humanoid" {
					errAdd(&errs, fmt.Sprintf("%s: bad mobs.role %s", tid, pythonRepr(mobs["role"])))
				}
				for _, numkey := range []string{"count", "level_delta"} {
					if !isIntLike(mobs[numkey]) {
						errAdd(&errs, fmt.Sprintf("%s: mobs.%s must be int", tid, numkey))
					}
				}
				if refill := mobs["refill_every_s"]; refill != nil {
					if !isIntLike(refill) || floatOf(refill) < 5 {
						errAdd(&errs, fmt.Sprintf("%s: refill_every_s must be an int >= 5", tid))
					}
				}
			}
			band, bandOK := pyjson.List(sit["hp_band"])
			if kind != "dummy" {
				bandGood := bandOK && len(band) == 2
				if bandGood {
					for _, x := range band {
						if !numLenient(x) {
							bandGood = false
						}
					}
				}
				if !bandGood {
					errAdd(&errs, fmt.Sprintf("%s: hp_band must be [low, high]", tid))
				}
				if !numLenient(sit["floor_pct"]) {
					errAdd(&errs, fmt.Sprintf("%s: floor_pct must be a number", tid))
				}
			}
			if rescues := sit["max_rescues_per_min"]; rescues != nil {
				if !isIntStrict(rescues) || floatOf(rescues) < 1 {
					errAdd(&errs, fmt.Sprintf("%s: max_rescues_per_min must be an int >= 1", tid))
				}
			}
			if adaptive := sit["adaptive"]; adaptive != nil && !pyjson.IsBool(adaptive) {
				errAdd(&errs, fmt.Sprintf("%s: adaptive must be true or false", tid))
			}
			if hurt := sit["hurt_every_s"]; hurt != nil {
				f, ok := pyjson.Num(hurt)
				if pyjson.IsBool(hurt) || !ok || f < 8 || f > 120 {
					errAdd(&errs, fmt.Sprintf("%s: hurt_every_s must be a number from 8 to 120", tid))
				}
			}
			if gap := sit["damage_interval_s"]; gap != nil {
				if kind != "tank_damage" {
					errAdd(&errs, fmt.Sprintf("%s: damage_interval_s needs kind tank_damage (it paces the governor top-up)", tid))
				} else {
					f, ok := pyjson.Num(gap)
					if pyjson.IsBool(gap) || !ok || f < 4 || f > 60 {
						errAdd(&errs, fmt.Sprintf("%s: damage_interval_s must be a number from 4 to 60", tid))
					}
				}
			}
			if topUp := sit["top_up_to_pct"]; topUp != nil {
				f, ok := pyjson.Num(topUp)
				inBand := len(band) == 2 && bandGoodBlock(band) && ok &&
					floatOf(band[0]) < f && f < floatOf(band[1])
				if kind == "ooc_buffs" || kind == "ooc_hurt" {
					errAdd(&errs, fmt.Sprintf("%s: top_up_to_pct needs a governor top-up (not for ooc kinds)", tid))
				} else if pyjson.IsBool(topUp) || !ok || !inBand {
					errAdd(&errs, fmt.Sprintf("%s: top_up_to_pct must be a number with hp_band[0] < x < hp_band[1]", tid))
				}
			}
			if reset := sit["reset_hire_cooldowns"]; reset != nil {
				if !(pyjson.IsBool(reset) && pyjson.Truthy(reset)) {
					errAdd(&errs, fmt.Sprintf("%s: reset_hire_cooldowns must be true (or absent)", tid))
				}
			}
			if drain := sit["energy_drain"]; drain != nil {
				botClass := pyjson.Str(bot["class"])
				if kind != "tank_damage" || botClass != "rogue" {
					errAdd(&errs, fmt.Sprintf("%s: energy_drain needs kind tank_damage and bot.class rogue", tid))
				} else if _, ok := pyjson.Object(drain); !ok {
					errAdd(&errs, fmt.Sprintf("%s: energy_drain must be {first_at_s, every_s, to}", tid))
				} else {
					drainObj, _ := pyjson.Object(drain)
					first := drainObj["first_at_s"]
					every := drainObj["every_s"]
					to := drainObj["to"]
					if !isIntStrict(first) || floatOf(first) < 5 || floatOf(first) > 170 {
						errAdd(&errs, fmt.Sprintf("%s: energy_drain first_at_s must be an int 5-170", tid))
					}
					if !isIntStrict(every) || floatOf(every) < 20 {
						errAdd(&errs, fmt.Sprintf("%s: energy_drain every_s must be an int >= 20", tid))
					}
					if !isIntStrict(to) || floatOf(to) < 0 || floatOf(to) > 19 {
						errAdd(&errs, fmt.Sprintf("%s: energy_drain to must be an int 0-19", tid))
					}
				}
			}
			if stripVal := sit["prepull_unaura"]; stripVal != nil {
				strip, ok := pyjson.List(stripVal)
				good := ok && len(strip) > 0
				if good {
					for _, x := range strip {
						f, numOK := pyjson.Num(x)
						if pyjson.IsBool(x) || !numOK || f < 1 || f > 999999 {
							good = false
						}
					}
				}
				if !good {
					errAdd(&errs, fmt.Sprintf("%s: prepull_unaura must be a non-empty list of aura/spell ids", tid))
				}
			}
			if repGap := sit["min_rep_gap_s"]; repGap != nil {
				f, ok := pyjson.Num(repGap)
				if pyjson.IsBool(repGap) || !ok || f <= 0 {
					errAdd(&errs, fmt.Sprintf("%s: min_rep_gap_s must be a number of seconds > 0", tid))
				}
			}
			if kind == "ooc_hurt" {
				// no mobs: the victim is hurt by a GM line, out of combat, inside hp_band
				if len(band) == 2 && bandGoodBlock(band) {
					lo, hi := floatOf(band[0]), floatOf(band[1])
					if !(1 <= lo && lo <= hi && hi <= 99) {
						errAdd(&errs, fmt.Sprintf("%s: ooc_hurt hp_band must be [low, high] with 1 <= low <= high <= 99", tid))
					}
				}
				waitVal := sit["ooc_wait_s"]
				f, ok := pyjson.Num(waitVal)
				if pyjson.IsBool(waitVal) || !ok || f <= 0 {
					errAdd(&errs, fmt.Sprintf("%s: ooc_hurt needs ooc_wait_s > 0 (the window length)", tid))
				}
				if n, ok := pyjson.Num(pyjson.ObjectOrEmpty(sit["mobs"])["count"]); !ok || n != 0 {
					errAdd(&errs, fmt.Sprintf("%s: ooc_hurt takes mobs.count 0", tid))
				}
			}
			if kind != "dummy" {
				victim := pyjson.Str(sit["victim"])
				extraList, _ := pyjson.List(trial["extra_bots"])
				nExtras := len(extraList)
				if victim == "extra1" || victim == "extra2" || victim == "extra3" {
					n, _ := strconv.Atoi(string(victim[len(victim)-1]))
					if n > nExtras {
						errAdd(&errs, fmt.Sprintf("%s: victim %s has no extra_bots bot behind it", tid, pythonRepr(victim)))
					}
				} else if victim != "player" && victim != "bot" {
					errAdd(&errs, fmt.Sprintf("%s: bad victim %s", tid, pythonRepr(sit["victim"])))
				}
				floorVal := sit["floor_pct"]
				if f, ok := pyjson.Num(floorVal); ok && !pyjson.IsBool(floorVal) && len(band) == 2 && bandGoodBlock(band) {
					// a floor at or above the band's low edge would rescue the victim the whole time
					if !(0 < f && f < floatOf(band[0])) {
						errAdd(&errs, fmt.Sprintf("%s: floor_pct %s must be above 0 and below the hp_band low %s",
							tid, pyjson.Str(floorVal), pyjson.Str(band[0])))
					}
				}
			}
		}
	}
	if list, ok := pyjson.List(trial["setup_checks"]); ok {
		for _, cid := range list {
			if !setupChecks[pyjson.Str(cid)] {
				errAdd(&errs, fmt.Sprintf("%s: unknown setup check %s", tid, pythonRepr(cid)))
			}
		}
	}
	if group != "raid" {
		haveChecks := map[string]bool{}
		if list, ok := pyjson.List(trial["setup_checks"]); ok {
			for _, cid := range list {
				haveChecks[pyjson.Str(cid)] = true
			}
		}
		for _, cid := range requiredSetupChecks {
			if !haveChecks[cid] {
				errAdd(&errs, fmt.Sprintf("%s: setup_checks must list the required check %s", tid, pythonRepr(cid)))
			}
		}
	}
	extraVal, hasExtra := trial["extra_bots"]
	extraList, _ := pyjson.List(extraVal)
	nExtra := len(extraList)
	if hasExtra {
		checkExtraBots(extraVal, tid, &errs)
		have := map[string]bool{}
		if list, ok := pyjson.List(trial["setup_checks"]); ok {
			for _, cid := range list {
				have[pyjson.Str(cid)] = true
			}
		}
		for _, cid := range extraBotChecks {
			if !have[cid] {
				errAdd(&errs, fmt.Sprintf("%s: extra_bots needs setup check %s", tid, pythonRepr(cid)))
			}
		}
	} else {
		have := map[string]bool{}
		if list, ok := pyjson.List(trial["setup_checks"]); ok {
			for _, cid := range list {
				have[pyjson.Str(cid)] = true
			}
		}
		for _, cid := range append(append([]string{}, extraBotChecks...), extraOnlyChecks...) {
			if have[cid] {
				errAdd(&errs, fmt.Sprintf("%s: setup check %s needs extra_bots", tid, pythonRepr(cid)))
			}
		}
	}
	uses := map[string]bool{}
	extraAliasUses(trial["rules"], uses)
	extraAliasUses(trial["opportunity"], uses)
	aliasNames := make([]string, 0, len(uses))
	for alias := range uses {
		aliasNames = append(aliasNames, alias)
	}
	sort.Strings(aliasNames)
	for _, alias := range aliasNames {
		var ok bool
		switch {
		case alias == "$extras":
			ok = nExtra > 0
		default:
			for i, name := range extraAliases {
				if alias == name {
					ok = i < nExtra
				}
			}
		}
		if !ok {
			errAdd(&errs, fmt.Sprintf("%s: alias %s has no extra_bots bot behind it", tid, pythonRepr(alias)))
		}
	}
	for _, numkey := range []string{"duration_s", "repeats", "min_valid_s"} {
		if !numLenient(trial[numkey]) {
			errAdd(&errs, fmt.Sprintf("%s: %s must be a number", tid, numkey))
		}
	}
	oppVal := trial["opportunity"]
	if oppVal != nil {
		opp, ok := pyjson.Object(oppVal)
		if !ok {
			errAdd(&errs, fmt.Sprintf("%s: opportunity is not an object", tid))
		} else {
			checkDict(oppVal, opportunityFields, "opportunity", tid, "", &errs)
			if unit := opp["unit"]; unit != nil {
				if s, ok := unit.(string); !ok || !crossUnits[s] {
					errAdd(&errs, fmt.Sprintf("%s: bad opportunity unit %s", tid, pythonRepr(unit)))
				}
			}
			for _, key := range []string{"hp_lt", "hp_gt", "power_lt", "power_gt"} {
				if opp[key] != nil && !numOrThreshold(opp[key]) {
					errAdd(&errs, fmt.Sprintf("%s: bad opportunity %s %s", tid, key, pythonRepr(opp[key])))
				}
			}
			if opp["in_combat"] != nil && !pyjson.IsBool(opp["in_combat"]) {
				errAdd(&errs, fmt.Sprintf("%s: bad opportunity in_combat %s", tid, pythonRepr(opp["in_combat"])))
			}
			if ha := opp["has_aura"]; ha != nil {
				list, okList := pyjson.List(ha)
				good := okList && len(list) > 0
				if good {
					for _, x := range list {
						if !isIntStrict(x) {
							good = false
						}
					}
				}
				if !good {
					errAdd(&errs, fmt.Sprintf("%s: bad opportunity has_aura %s (a non-empty list of ints)", tid, pythonRepr(ha)))
				}
			}
		}
	}
	rulesVal := trial["rules"]
	rules, okRules := pyjson.List(rulesVal)
	if !okRules || len(rules) == 0 {
		errAdd(&errs, fmt.Sprintf("%s: rules must be a non-empty list", tid))
		return errs
	}
	for _, rule := range rules {
		checkRule(rule, tid, &errs)
	}
	if _, present := trial["variants"]; present && group != "raid" {
		errs = append(errs, CheckVariants(trial, tid)...)
	}
	return errs
}

func bandGoodBlock(band []any) bool {
	for _, x := range band {
		if !numLenient(x) || pyjson.IsBool(x) {
			return false
		}
	}
	return true
}

// objectOrFalsyEmpty is Python's `doc = trial.get(key) or {}` followed by _check_dict: a falsy
// value becomes an empty object, a truthy object is validated, and a truthy non-object is an
// error the caller drops to {}. valid is false only in that last case.
func objectOrFalsyEmpty(val any, allowed map[string]bool, label, tid string, errs *[]string) (map[string]any, bool) {
	if !pyjson.Truthy(val) {
		return map[string]any{}, true
	}
	obj, ok := pyjson.Object(val)
	if !ok {
		checkDict(val, allowed, label, tid, "", errs)
		return map[string]any{}, false
	}
	if !checkDict(val, allowed, label, tid, "", errs) {
		return map[string]any{}, false
	}
	return obj, true
}

// FileValidation is one trial file's validate result: the load error, the schema errors and
// the two checks the Python `trials validate` adds on top of Validate (id == file stem, no
// duplicate rule id across files).
type FileValidation struct {
	Path   string
	Errors []string
}

// ValidateFiles validates trial files the way the Python `trials.py validate` does: each file
// through Validate, plus id-versus-file-stem and duplicate rule ids across the whole set. The
// error is an input Python's cmd_validate raises on before printing that file's line (a rule
// that is not an object, an unhashable rule id); the results before it were already printed.
func ValidateFiles(paths []string) ([]FileValidation, error) {
	seen := map[any]string{}
	out := make([]FileValidation, 0, len(paths))
	for _, path := range paths {
		result, err := validateFile(path, seen)
		if err != nil {
			return out, err
		}
		out = append(out, result)
	}
	return out, nil
}

// pythonStem is os.path.splitext(name)[0]: leading dots of the last component are part of
// the root, so ".json" keeps its whole name as the stem where filepath.Ext would say "".
func pythonStem(name string) string {
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return name
	}
	for i := range dot {
		if name[i] != '.' {
			return name[:dot]
		}
	}
	return name
}

func validateFile(path string, seen map[any]string) (FileValidation, error) {
	result := FileValidation{Path: path}
	trial, err := LoadTrial(path)
	if err != nil {
		// Python prints the exception's str(); for an OS error that is
		// "[Errno N] message: 'path'".
		result.Errors = append(result.Errors, fmt.Sprintf("cannot load: %s", pythonErrorText(err)))
		return result, nil
	}
	result.Errors = append(result.Errors, Validate(trial)...)
	stem := pythonStem(filepath.Base(path))
	if id, ok := trial["id"].(string); ok && id != stem {
		result.Errors = append(result.Errors,
			fmt.Sprintf("%s: trial id %s != file stem %s", pyjson.Str(trial["id"]), pythonRepr(id), pythonRepr(stem)))
	}
	rules, _ := pyjson.List(trial["rules"])
	for _, ruleVal := range rules {
		rule, ok := pyjson.Object(ruleVal)
		if !ok {
			return FileValidation{}, fmt.Errorf("%s: rule is not an object", pyjson.Str(trial["id"]))
		}
		rid := rule["id"]
		key, err := ruleIDKey(rid)
		if err != nil {
			return FileValidation{}, fmt.Errorf("%s: unhashable rule id %s", pyjson.Str(trial["id"]), pythonRepr(rid))
		}
		if owner, dup := seen[key]; dup {
			result.Errors = append(result.Errors,
				fmt.Sprintf("%s: duplicate rule id %s (also in %s)", pyjson.Str(trial["id"]), pyjson.Str(rid), owner))
		} else {
			seen[key] = pyjson.Str(trial["id"])
		}
	}
	return result, nil
}

// ValidateOutput renders the Python `trials validate` stdout and exit status for paths.
// The error is an input cmd_validate raises on; its results up to that file were printed.
func ValidateOutput(paths []string) (string, int, error) {
	results, err := ValidateFiles(paths)
	var b strings.Builder
	failed := 0
	for _, result := range results {
		if len(result.Errors) == 0 {
			fmt.Fprintf(&b, "%s: ok\n", result.Path)
			continue
		}
		failed++
		for _, problem := range result.Errors {
			fmt.Fprintf(&b, "%s: %s\n", result.Path, problem)
		}
	}
	if failed > 0 || err != nil {
		return b.String(), 1, err
	}
	return b.String(), 0, nil
}

// ruleIDKey is the dict key Python's `seen_rule_ids[rid]` uses: strings keep their text,
// ints/floats/bools compare by numeric value (True == 1), None is its own key, and a list or
// object is unhashable and raises TypeError.
func ruleIDKey(v any) (any, error) {
	switch val := v.(type) {
	case nil, string:
		return val, nil
	case bool:
		if val {
			return int64(1), nil
		}
		return int64(0), nil
	case int64:
		return val, nil
	case float64:
		if !math.IsInf(val, 0) && !math.IsNaN(val) && val == math.Trunc(val) {
			if n, _ := new(big.Float).SetFloat64(val).Int(nil); n.IsInt64() {
				return n.Int64(), nil
			}
		}
		return val, nil
	case *big.Int:
		// Tagged: a big rule id 12 is not the string "12" in Python's dict.
		return bigRuleID(val.String()), nil
	}
	return nil, errors.New("unhashable")
}

// bigRuleID is a rule id beyond int64: distinct from a string of the same digits.
type bigRuleID string

// pythonErrorText is the Python `str(exc)` for the load errors LoadTrial returns: an OS error
// keeps Python's "[Errno N] message: 'path'" form.
func pythonErrorText(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		var errno syscall.Errno
		if errors.As(pathErr.Err, &errno) {
			msg := errno.Error()
			if msg != "" {
				msg = strings.ToUpper(msg[:1]) + msg[1:]
			}
			return fmt.Sprintf("[Errno %d] %s: %s", int(errno), msg, pythonRepr(pathErr.Path))
		}
	}
	return err.Error()
}
