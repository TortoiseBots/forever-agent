// Package rules is the check DSL engine: Evaluate(rule, rows, samples, marks, ctx).
//
// No eval, a closed set. Each metric returns a number plus the matched events as evidence.
// Ctx selects the `min_*` threshold aliases and carries the trial, the scored window, the
// per-attacker victim table and (for walk_metric rules) the walk report.
package rules

import (
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/spells"
)

// Row is one events/samples/marks row.
type Row = map[string]any

// DefaultCCAuras are the crowd-control auras a mob can carry (marked_has_cc): the vanilla 1.12
// player ranks, every rank, read from the core's spell_template (effect 6 apply aura). Each id
// is the aura spell itself, not the trainer's learn spell of the same name:
//
//	Polymorph        118, 12824, 12825, 12826 (aura 5 + 56, mechanic 17); Cow/Turtle/Pig 28270-28272
//	Sap              6770, 2070, 11297        (aura 12, mechanic 14)
//	Hibernate        2637, 18657, 18658       (aura 12, mechanic 10)
//	Freezing Trap    3355, 14308, 14309       ("Freezing Trap Effect", aura 12, mechanic 13)
//	Shackle Undead   9484, 9485, 10955        (aura 12, mechanic 20)
//	Banish           710, 18647               (aura 12 + 39, mechanic 18)
//	Seduction        6358                     (aura 12, mechanic 1)
//	Gouge            1776, 1777, 8629, 11285, 11286 (aura 12, mechanic 14)
//	Scare Beast      1513, 14326, 14327       (aura 7 fear, mechanic 5)
//	Repentance       20066                    (aura 12, mechanic 14)
//	Sleep            700, 1090, 2937, 10234   (aura 12, mechanic 10)
//	Blind            2094                     (aura 33 + 5, mechanic 2; kept from the old list, verified)
//
// Left out on purpose: Turtle-only ids (Repentance ranks 2-3, Polymorph: Rodent and Homunculus)
// and the unranked NPC/item copies of Banish, Sleep, Gouge and Polymorph, which no player rank
// casts.
var DefaultCCAuras = intSet(118, 12824, 12825, 12826, 28270, 28271, 28272,
	6770, 2070, 11297,
	2637, 18657, 18658,
	3355, 14308, 14309,
	9484, 9485, 10955,
	710, 18647,
	6358,
	1776, 1777, 8629, 11285, 11286,
	1513, 14326, 14327,
	20066,
	700, 1090, 2937, 10234,
	2094)

// SpiritOfRedemptionAuras: a healer under this aura is dead for scoring purposes (the priest
// talent keeps healing for 15 s after death).
var SpiritOfRedemptionAuras = intSet(20711, 20713, 20721, 20723, 27792, 27795, 27827)

func intSet(ids ...int64) map[int64]bool {
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

var extraNames = []string{"$extra1", "$extra2", "$extra3"}

var (
	// passIfRe is the bench's PASS_IF_RE: a comparison against one number.
	passIfRe = regexp.MustCompile(`^(==|!=|<=|>=|<|>)\s*(-?\d+(?:\.\d+)?)$`)
	// passWindowRe is the passing-side bound a first-match window is clipped to.
	passWindowRe = regexp.MustCompile(`^(<=|>=|<|>)\s*(-?\d+(?:\.\d+)?)$`)
)

// Ctx is the evaluation context the score package builds for one repeat.
type Ctx struct {
	Guids             map[string]any
	Thresholds        map[string]any
	ThresholdsAssumed bool
	Trial             map[string]any
	Window            *[2]float64
	VictimsByGUID     map[string][]events.AttackVictim
	Rows              []Row
	SamplesAll        []Row
	Marks             []Row
	WalkReport        map[string]any
	// spellErr is an int() exception `_spell_ids` raised while matching (an infinite
	// where.spell_id: OverflowError, which the bench does not catch). Python's raise aborts
	// the rule, so Evaluate reports it instead of a verdict.
	spellErr error
}

// Result is what one rule says about one repeat.
type Result struct {
	Value    any // float64 for a rate, int64 for a count, nil for no value
	K        int64
	N        int64
	Verdict  string
	Evidence []string
	Guard    string
	Reason   string
	NoMatch  bool
	// Err is the Python exception the bench would have raised on this input (a non-numeric
	// walk mean, ...). The scorer turns it into an error instead of another verdict.
	Err error
}

// Num is the bench's `_num`: a number (bool included, as Python's isinstance sees it), a
// numeric string, or a $critical/$low/$medium threshold alias from ctx.
func Num(value any, ctx *Ctx, def *float64) *float64 {
	if pyjson.IsNum(value) {
		f, _ := pyjson.Num(value)
		return &f
	}
	if s, ok := value.(string); ok {
		v := strings.TrimSpace(s)
		switch v {
		case "$critical", "$low", "$medium":
			if ctx != nil {
				if got := ctx.Thresholds[v[1:]]; got != nil {
					if f, ok := pyjson.Float(got); ok {
						return &f
					}
				}
			}
			return def
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return &f
		}
		return def
	}
	return def
}

func extras(guids map[string]any) []any {
	if list, ok := pyjson.List(guids["extras"]); ok {
		return list
	}
	return nil
}

// AliasMap maps single-guid unit aliases to guids ($extraN follow guids["extras"] order).
func AliasMap(guids map[string]any) map[string]any {
	if guids == nil {
		guids = map[string]any{}
	}
	out := map[string]any{
		"$player": guids["player"], "$bot": guids["bot"],
		"$pet": guids["pet"], "$marked": guids["marked"],
	}
	ext := extras(guids)
	for i, name := range extraNames {
		if i < len(ext) {
			out[name] = ext[i]
		} else {
			out[name] = nil
		}
	}
	return out
}

// UnitIDs resolves a where unit alias to a set of guids ('$mob' is any mob).
func UnitIDs(unit string, ctx *Ctx) map[string]bool {
	out := map[string]bool{}
	var guids map[string]any
	if ctx != nil {
		guids = ctx.Guids
	}
	if guids == nil {
		guids = map[string]any{}
	}
	switch unit {
	case "$player", "$bot", "$pet", "$marked":
		out[lowerOf(guids[strings.TrimPrefix(unit, "$")])] = true
		return out
	case "$extras":
		for _, g := range extras(guids) {
			if pyjson.Truthy(g) {
				out[lowerOf(g)] = true
			}
		}
		return out
	case "$mob":
		out["$mob"] = true
		return out
	}
	for _, name := range extraNames {
		if unit == name {
			out[lowerOf(AliasMap(guids)[unit])] = true
			return out
		}
	}
	if strings.HasPrefix(unit, "mob:") {
		out[strings.ToLower(unit[4:])] = true
		return out
	}
	out[strings.ToLower(unit)] = true
	return out
}

func lowerOf(v any) string {
	// Python: str(value or "").lower() — a missing/empty guid reads "" (never "none").
	if !pyjson.Truthy(v) {
		return ""
	}
	return strings.ToLower(pyjson.Str(v))
}

// floatOrZero is Python's float(value or 0.0) where the bench leaves the conversion
// unguarded: every conversion error propagates.
func floatOrZero(v any) (float64, error) {
	if !pyjson.Truthy(v) {
		return 0, nil
	}
	f, err := pyjson.PyFloat(v)
	if err != nil {
		return 0, err
	}
	return f, nil
}

// guardedFloatOrZero is Python's `float(value or 0.0)` inside try/except
// (TypeError, ValueError): only the classes the bench does not catch propagate.
func guardedFloatOrZero(v any) (float64, error) {
	if !pyjson.Truthy(v) {
		return 0, nil
	}
	f, err := pyjson.PyFloat(v)
	if err != nil {
		if pyjson.CaughtByTypeValue(err) {
			return 0, nil
		}
		return 0, err
	}
	return f, nil
}

// uncaughtTypeValue reports an error Python's except (TypeError, ValueError) would not catch.
func uncaughtTypeValue(err error) bool {
	return err != nil && !pyjson.CaughtByTypeValue(err)
}

// SpellIDs is the set of spell ids a where/trigger filter names (by name or by id), keyed by
// Python's int() decimal form so an id beyond int64 still compares (Python's ints are
// arbitrary precision). An error is an int() exception the bench's `except (TypeError,
// ValueError)` does not catch: an infinite spell_id is OverflowError, so the rule raises.
func SpellIDs(where Row) (map[string]bool, error) {
	out := map[string]bool{}
	if where["spell"] != nil {
		names := []any{where["spell"]}
		if list, ok := pyjson.List(where["spell"]); ok {
			names = list
		}
		for _, name := range names {
			for id := range spells.IDsFor(pyjson.Str(name)) {
				out[pyjson.Str(id)] = true
			}
		}
	}
	if where["spell_id"] != nil {
		ids := []any{where["spell_id"]}
		if list, ok := pyjson.List(where["spell_id"]); ok {
			ids = list
		}
		for _, one := range ids {
			key, err := IntKey(one)
			if err != nil {
				if uncaughtTypeValue(err) {
					return nil, err
				}
				continue
			}
			out[key] = true
		}
	}
	return out, nil
}

// IntKey is Python's int(value) as a comparable decimal key, with the exception class where
// int() raises: OverflowError for an infinity, ValueError for NaN or a bad literal, TypeError
// for a type it refuses. Two values Python considers the same integer (5, 5.0, "5") share a
// key, and an integer beyond int64 keeps its exact digits.
func IntKey(v any) (string, error) {
	n, err := pyjson.PyInt(v)
	if err != nil {
		return "", err
	}
	return pyjson.Str(n), nil
}

// spellIDKey is a row's spell_id as the key of Python's `row.get("spell_id") in spell_ids`
// (a set of ints). Unlike IntKey it never calls int() on the row's value: 5.0 matches 5
// (equal, same hash), but 5.5 and "5" do not, and NaN or an infinity is in no integer set.
func spellIDKey(v any) (string, bool) {
	switch n := v.(type) {
	case int64:
		return strconv.FormatInt(n, 10), true
	case *big.Int:
		return n.String(), true
	case bool:
		if n {
			return "1", true
		}
		return "0", true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
			return "", false
		}
		bi, _ := new(big.Float).SetFloat64(n).Int(nil)
		return bi.String(), true
	}
	return "", false
}

// TimedSample is a sample with its re-based second.
type TimedSample struct {
	T      float64
	Sample Row
}

// SampleRows groups samples by lower-case guid, each group sorted by t.
func SampleRows(samples []Row) map[string][]TimedSample {
	byGUID := map[string][]TimedSample{}
	for _, sample := range samples {
		guid := strings.ToLower(pyjson.Str(sample["guid"]))
		if sample["guid"] == nil {
			guid = ""
		}
		t, ok := pyjson.Num(sample["t"])
		if !ok {
			t2, ok2 := pyjson.Float(sample["t"])
			if !ok2 {
				continue
			}
			t = t2
		}
		byGUID[guid] = append(byGUID[guid], TimedSample{T: t, Sample: sample})
	}
	for _, rows := range byGUID {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].T < rows[j].T })
	}
	return byGUID
}

// At returns the sample of guid at t: the freshest one at or before t within one second.
func At(byGUID map[string][]TimedSample, guid any, t float64) Row {
	rows := byGUID[lowerOf(guid)]
	var best Row
	for _, row := range rows {
		if row.T <= t && t-row.T <= 1.0 {
			best = row.Sample
		} else if row.T > t {
			break
		}
	}
	return best
}

// PacketVictim is the attacker's victim from its last attack packet, and whether a packet
// decided it. On the seq path the winner is the latest packet with seq below the cast's seq;
// on the time path only packets strictly earlier than the cast are eligible; either way only
// a packet within the last 5 s decides. The newest event wins over the sample.
func PacketVictim(victims map[string][]events.AttackVictim, guid any, t float64, seq any, byGUID map[string][]TimedSample) (string, bool) {
	rows := victims[lowerOf(guid)]
	var seqN int64
	hasSeq := false
	if seq != nil {
		if n, ok := pyjson.Int64(seq); ok {
			seqN, hasSeq = n, true
		} else if f, ok := pyjson.Float(seq); ok {
			seqN, hasSeq = int64(f), true
		}
	}
	useSeq := false
	if hasSeq {
		for _, row := range rows {
			if row.HasSeq {
				useSeq = true
				break
			}
		}
	}
	var bestVictim string
	var bestT float64
	found := false
	var bestRank float64
	for _, row := range rows {
		vt := row.T
		if t-vt > 5.0 {
			continue
		}
		var rank float64
		if useSeq {
			if !row.HasSeq || row.Seq >= seqN {
				continue
			}
			rank = float64(row.Seq)
		} else {
			if !(vt < t) {
				continue
			}
			rank = vt
		}
		if !found || rank >= bestRank {
			bestRank = rank
			bestVictim = row.Victim
			bestT = vt
			found = true
		}
	}
	if !found {
		return "", false
	}
	if byGUID != nil {
		latest := math.Inf(-1)
		hasLatest := false
		for _, row := range byGUID[lowerOf(guid)] {
			st := row.T
			if st <= t && (!hasLatest || st > latest) {
				latest = st
				hasLatest = true
			}
		}
		if hasLatest && latest > bestT {
			// A sample newer than the packet is the fresher observation (even over an
			// attack_stop): the caller uses the sample.
			return "", false
		}
	}
	return bestVictim, true
}

func isMobGUID(guid any, byGUID map[string][]TimedSample, mobGUIDs map[string]bool) bool {
	key := lowerOf(guid)
	if guid == nil || key == "" {
		return false
	}
	if mobGUIDs[key] {
		return true
	}
	for _, row := range byGUID[key] {
		if strings.HasPrefix(pyjson.Str(row.Sample["unit"]), "mob:") {
			return true
		}
	}
	return false
}

// MobLiveAt reports a mob still live around t: its freshest sample at or before t (inside
// window) has hp left, or it has an attack packet inside window.
func MobLiveAt(victims map[string][]events.AttackVictim, byGUID map[string][]TimedSample, guid any, t float64, window float64) bool {
	key := lowerOf(guid)
	var latest *TimedSample
	for i := range byGUID[key] {
		if byGUID[key][i].T <= t {
			latest = &byGUID[key][i]
		} else {
			break
		}
	}
	if latest != nil && t-latest.T <= window {
		hp, ok := pyjson.Float(latest.Sample["hp"])
		if !ok || hp > 0 {
			return true
		}
	}
	for _, row := range victims[key] {
		if row.T <= t && t-row.T <= window {
			return true
		}
	}
	return false
}

// AttackStateWindowS is the liveness window: a sample or packet older than this is no evidence.
const AttackStateWindowS = 5.0

// LatestSample is the freshest sample of guid at or before t inside window.
func LatestSample(byGUID map[string][]TimedSample, guid any, t float64, window float64) Row {
	var best Row
	for _, row := range byGUID[lowerOf(guid)] {
		if row.T <= t && t-row.T <= window {
			best = row.Sample
		} else if row.T > t {
			break
		}
	}
	return best
}

// PartyAttackState is (state True/False/unknown, one line per live mob saying who it was on).
// Packets first, the mob's own sample target as the fallback; only mobs count as attackers.
func PartyAttackState(victims map[string][]events.AttackVictim, byGUID map[string][]TimedSample, partyGUIDs []string, t float64, seq any, guids map[string]any, names map[string]string) (any, []string) {
	want := map[string]bool{}
	for _, g := range partyGUIDs {
		if g != "" {
			want[strings.ToLower(g)] = true
		}
	}
	var detail []string
	if len(want) == 0 {
		return false, detail
	}
	if names == nil {
		names = map[string]string{}
	}
	mobs := map[string]bool{}
	if list, ok := pyjson.List(guids["mobs"]); ok {
		for _, g := range list {
			if pyjson.Truthy(g) {
				mobs[strings.ToLower(pyjson.Str(g))] = true
			}
		}
	}
	candidates := map[string]bool{}
	for key := range byGUID {
		candidates[key] = true
	}
	for key := range victims {
		candidates[key] = true
	}
	for key := range mobs {
		candidates[key] = true
	}
	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	unknown := false
	under := false
	for _, guid := range keys {
		if want[guid] || !isMobGUID(guid, byGUID, mobs) {
			continue
		}
		victim, decided := PacketVictim(victims, guid, t, seq, byGUID)
		seen := ""
		if decided {
			seen = " (packet)"
		}
		if !decided {
			sample := At(byGUID, guid, t)
			if sample == nil {
				sample = LatestSample(byGUID, guid, t, AttackStateWindowS)
			}
			if sample != nil {
				victim = strings.ToLower(pyjson.Str(sample["target"]))
				if sample["target"] == nil || sample["target"] == "" {
					victim = ""
				}
				if st, ok := pyjson.Float(sample["t"]); ok {
					seen = fmt.Sprintf(" (sample t=%.1f)", st)
				} else {
					seen = ""
				}
			} else if MobLiveAt(victims, byGUID, guid, t, AttackStateWindowS) {
				unknown = true
				detail = append(detail, fmt.Sprintf("%s -> unknown (live, no sample or packet)", guid))
				continue
			} else {
				continue
			}
		}
		if victim == "" {
			continue
		}
		on, ok := names[victim]
		if !ok {
			on = victim
		}
		detail = append(detail, fmt.Sprintf("%s -> %s%s", guid, on, seen))
		if want[victim] {
			under = true
		}
	}
	if under {
		return true, detail
	}
	if unknown {
		return nil, detail
	}
	return false, detail
}

// PartyUnderAttack is True / False / unknown: is a hostile mob's victim at t one of partyGUIDs?
func PartyUnderAttack(victims map[string][]events.AttackVictim, byGUID map[string][]TimedSample, partyGUIDs []string, t float64, seq any, guids map[string]any) any {
	state, _ := PartyAttackState(victims, byGUID, partyGUIDs, t, seq, guids, nil)
	return state
}

// UnitNames maps lower-case guid -> `$alias` for the evidence lines.
func UnitNames(guids map[string]any) map[string]string {
	out := map[string]string{}
	for alias, guid := range AliasMap(guids) {
		if pyjson.Truthy(guid) {
			out[strings.ToLower(pyjson.Str(guid))] = alias
		}
	}
	return out
}

func unitHP(byGUID map[string][]TimedSample, guid any, t float64) *float64 {
	sample := At(byGUID, guid, t)
	if sample == nil {
		return nil
	}
	hp, okHP := pyjson.Float(sample["hp"])
	maxHP, okMax := pyjson.Float(sample["max_hp"])
	if !okHP || !okMax || maxHP <= 0 {
		return nil
	}
	v := 100.0 * hp / maxHP
	return &v
}

func inExcluded(excluded [][2]float64, t float64) bool {
	for _, span := range excluded {
		if span[0] <= t && t <= span[1] {
			return true
		}
	}
	return false
}

func auraSet(sample Row) map[int64]bool {
	out := map[int64]bool{}
	auras, ok := pyjson.List(sample["auras"])
	if !ok {
		return out
	}
	for _, a := range auras {
		if n, ok := pyjson.Int64(a); ok {
			out[n] = true
		} else if f, ok := pyjson.Float(a); ok {
			out[int64(f)] = true
		} else {
			return map[int64]bool{}
		}
	}
	return out
}

// Matches reports whether row satisfies every key in where. `unknown`, when non-nil,
// collects the rows whose party_under_attack state could not be decided: they are not
// matches, but they are not clean either.
func Matches(row Row, where Row, byGUID map[string][]TimedSample, ctx *Ctx, trial map[string]any, unknown *[]Row) bool {
	for key := range where {
		if !isWhereKey(key) {
			return false
		}
	}
	if kind, ok := where["kind"]; ok && kind != nil {
		if pyjson.Str(row["kind"]) != pyjson.Str(kind) {
			return false
		}
	}
	for _, side := range []string{"src", "dst"} {
		want := where[side]
		if want == nil {
			continue
		}
		wants := []any{want}
		if list, ok := pyjson.List(want); ok {
			wants = list
		}
		guid := strings.ToLower(pyjson.Str(row[side+"_guid"]))
		if row[side+"_guid"] == nil {
			guid = ""
		}
		hit := false
		for _, w := range wants {
			ids := UnitIDs(pyjson.Str(w), ctx)
			if ids["$mob"] && pyjson.Str(row[side+"_role"]) == "mob" {
				hit = true
			} else if guid != "" && ids[guid] {
				hit = true
			}
		}
		if !hit {
			return false
		}
	}
	ids, err := SpellIDs(where)
	if err != nil {
		// Python's raise aborts the rule; Matches has no error return, so the exception
		// waits on the ctx for Evaluate to report.
		if ctx != nil {
			ctx.spellErr = err
		}
		return false
	}
	if len(ids) > 0 {
		if row["spell_id"] == nil {
			return false
		}
		id, ok := spellIDKey(row["spell_id"])
		if !ok || !ids[id] {
			return false
		}
	}
	if pyjson.Truthy(where["spell_interruptible"]) {
		id, ok := pyjson.Int64(row["spell_id"])
		if !ok || !spells.SpellIsInterruptible(id) {
			return false
		}
	}
	for _, key := range []string{"dst_hp_gt", "dst_hp_lt", "dst_hp_le"} {
		if where[key] == nil {
			continue
		}
		hp, ok := pyjson.Float(row["dst_hp_pct"])
		if row["dst_hp_pct"] == nil || !ok {
			// Missing hp is treated as above critical (+inf): it passes gt gates and
			// fails lt/le gates, never silently "below".
			if key != "dst_hp_gt" {
				return false
			}
			continue
		}
		limit := Num(where[key], ctx, nil)
		if limit == nil {
			return false
		}
		switch key {
		case "dst_hp_gt":
			if !(hp > *limit) {
				return false
			}
		case "dst_hp_lt":
			if !(hp < *limit) {
				return false
			}
		default:
			if !(hp <= *limit) {
				return false
			}
		}
	}
	if where["src_hp_le"] != nil {
		limit := Num(where["src_hp_le"], ctx, nil)
		if limit == nil {
			return false
		}
		hp := unitHP(byGUID, row["src_guid"], rowNum(row, "t"))
		if hp == nil || !(*hp <= *limit) {
			return false
		}
	}
	if where["in_combat"] != nil {
		sample := At(byGUID, row["src_guid"], rowNum(row, "t"))
		want := pyjson.Truthy(where["in_combat"])
		if sample == nil || pyjson.Truthy(sample["in_combat"]) != want {
			return false
		}
	}
	if where["t_from"] != nil {
		from, ok := pyjson.Float(where["t_from"])
		if !ok || rowNum(row, "t") < from {
			return false
		}
	}
	if where["t_to"] != nil {
		to, ok := pyjson.Float(where["t_to"])
		if !ok || rowNum(row, "t") > to {
			return false
		}
	}
	for _, key := range []string{"dst_has_aura", "dst_lacks_aura"} {
		if where[key] == nil {
			continue
		}
		present := key == "dst_has_aura"
		want := []any{where[key]}
		if list, ok := pyjson.List(where[key]); ok {
			want = list
		}
		ids := map[int64]bool{}
		for _, one := range want {
			n, ok := pyjson.Int64(one)
			if !ok {
				if f, fok := pyjson.Float(one); fok {
					n, ok = int64(f), true
				}
			}
			if !ok {
				return false
			}
			ids[n] = true
		}
		sample := At(byGUID, row["dst_guid"], rowNum(row, "t"))
		have := map[int64]bool{}
		if sample != nil {
			have = auraSet(sample)
		}
		intersects := false
		for id := range ids {
			if have[id] {
				intersects = true
			}
		}
		if present && !intersects {
			return false
		}
		if !present && intersects {
			return false
		}
	}
	if where["src_power_lt"] != nil {
		sample := At(byGUID, row["src_guid"], rowNum(row, "t"))
		if sample == nil {
			return false
		}
		power, okP := pyjson.Float(sample["power"])
		maxPower, okM := pyjson.Float(sample["max_power"])
		if !okP || !okM || maxPower == 0 {
			return false
		}
		pct := 100.0 * power / maxPower
		limit := Num(where["src_power_lt"], ctx, nil)
		if limit == nil || !(pct < *limit) {
			return false
		}
	}
	for _, key := range []string{"src_target_of", "dst_target_of"} {
		if where[key] == nil {
			continue
		}
		side := strings.Split(key, "_")[0] + "_guid"
		var victims map[string][]events.AttackVictim
		var guids map[string]any
		if ctx != nil {
			victims = ctx.VictimsByGUID
			guids = ctx.Guids
		}
		victim, decided := PacketVictim(victims, row[side], rowNum(row, "t"), row["seq"], byGUID)
		if !decided {
			sample := At(byGUID, row[side], rowNum(row, "t"))
			if sample == nil {
				return false
			}
			victim = strings.ToLower(pyjson.Str(sample["target"]))
			if sample["target"] == nil || sample["target"] == "" {
				victim = ""
			}
		}
		want := strings.ToLower(pyjson.Str(where[key]))
		alias, ok := AliasMap(guids)[want]
		if !ok {
			alias = want
		}
		aliasStr := ""
		if pyjson.Truthy(alias) {
			aliasStr = strings.ToLower(pyjson.Str(alias))
		}
		if victim != aliasStr {
			return false
		}
	}
	if where["src_within_of_marked"] != nil {
		limit, ok := pyjson.Float(where["src_within_of_marked"])
		if !ok {
			return false
		}
		var guids map[string]any
		if ctx != nil {
			guids = ctx.Guids
		}
		a := At(byGUID, row["src_guid"], rowNum(row, "t"))
		b := At(byGUID, guids["marked"], rowNum(row, "t"))
		if a == nil || b == nil {
			return false
		}
		ax, ok1 := pyjson.Float(a["x"])
		ay, ok2 := pyjson.Float(a["y"])
		az, ok3 := pyjson.Float(a["z"])
		bx, ok4 := pyjson.Float(b["x"])
		by, ok5 := pyjson.Float(b["y"])
		bz, ok6 := pyjson.Float(b["z"])
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
			return false
		}
		dist := math.Sqrt(math.Pow(ax-bx, 2) + math.Pow(ay-by, 2) + math.Pow(az-bz, 2))
		if dist > limit {
			return false
		}
	}
	if pyjson.Truthy(where["marked_has_cc"]) {
		var guids map[string]any
		if ctx != nil {
			guids = ctx.Guids
		}
		sample := At(byGUID, guids["marked"], rowNum(row, "t"))
		if sample == nil {
			return false
		}
		have := auraSet(sample)
		intersects := false
		for id := range DefaultCCAuras {
			if have[id] {
				intersects = true
			}
		}
		if !intersects {
			return false
		}
	}
	if where["party_under_attack"] != nil {
		// A party member has a hostile mob on it at the event's time. Mobs only: a party
		// member's own target is not an attacker. Decided last, after every other key.
		var party []string
		wantParty := []any{where["party_under_attack"]}
		if list, ok := pyjson.List(where["party_under_attack"]); ok {
			wantParty = list
		}
		for _, unit := range wantParty {
			for g := range UnitIDs(pyjson.Str(unit), ctx) {
				if g != "" && g != "$mob" {
					party = append(party, g)
				}
			}
		}
		var victims map[string][]events.AttackVictim
		var guids map[string]any
		if ctx != nil {
			victims = ctx.VictimsByGUID
			guids = ctx.Guids
		}
		state := PartyUnderAttack(victims, byGUID, party, rowNum(row, "t"), row["seq"], guids)
		if state == nil {
			if unknown != nil {
				*unknown = append(*unknown, row)
			}
			return false
		}
		if b, ok := state.(bool); ok && !b {
			return false
		}
	}
	return true
}

func isWhereKey(key string) bool {
	switch key {
	case "kind", "src", "dst", "spell", "spell_id",
		"dst_hp_gt", "dst_hp_lt", "dst_hp_le", "in_combat",
		"t_from", "t_to", "dst_has_aura", "dst_lacks_aura",
		"src_power_lt", "response_dst", "same_dst",
		"trigger_debounce_s", "src_target_of", "dst_target_of",
		"src_within_of_marked", "marked_has_cc",
		"spell_interruptible", "src_hp_le", "not_within_s_after_mark",
		"party_under_attack",
		"num", "den", "trigger", "response", "within_s",
		"aura", "unit", "phase", "name", "alive", "min_n",
		"triggers":
		return true
	}
	return false
}

func rowNum(row Row, key string) float64 {
	if f, ok := pyjson.Float(row[key]); ok {
		return f
	}
	return 0.0
}

// firstT is Python's `row.get("t", 0.0)`: the stored number (int or float), 0.0 when absent.
func firstT(row Row) any {
	if v, ok := row["t"]; ok {
		return v
	}
	return 0.0
}

// EvidenceLine is one human line for one event row.
func EvidenceLine(row Row) string {
	spell := row["spell"]
	if !pyjson.Truthy(spell) {
		spell = row["spell_id"]
	}
	if !pyjson.Truthy(spell) {
		spell = "?"
	}
	hp, ok := pyjson.Float(row["dst_hp_pct"])
	hpS := "hp=?"
	if ok {
		hpS = fmt.Sprintf("hp=%d%%", int64(pyjson.Round(hp, 0)))
	}
	spellID := "None"
	if row["spell_id"] != nil {
		spellID = pyjson.Str(row["spell_id"])
	}
	src := pyjson.Str(row["src"])
	if !pyjson.Truthy(row["src"]) {
		src = "?"
	}
	dst := pyjson.Str(row["dst"])
	if !pyjson.Truthy(row["dst"]) {
		dst = "?"
	}
	return fmt.Sprintf("t=%.1f %s %s (%s) %s -> %s %s",
		rowNum(row, "t"), pyjson.Str(row["kind"]), pyjson.Str(spell), spellID, src, dst, hpS)
}

// ThresholdNames are the threshold aliases a rule may read.
var ThresholdNames = []string{"critical", "low", "medium"}

// AliasNames are the threshold aliases ($critical/$low/$medium, as names) a rule reads.
// It looks at where/num/den/trigger/response and, when the rule has a min_opportunity_s, at
// the trial's opportunity block.
func AliasNames(rule Row, trial map[string]any) []string {
	check := pyjson.ObjectOrEmpty(rule["check"])
	blobs := []Row{
		pyjson.ObjectOrEmpty(check["where"]),
		pyjson.ObjectOrEmpty(check["num"]),
		pyjson.ObjectOrEmpty(check["den"]),
		pyjson.ObjectOrEmpty(check["trigger"]),
		pyjson.ObjectOrEmpty(check["response"]),
	}
	if pyjson.Truthy(rule["min_opportunity_s"]) && trial != nil {
		blobs = append(blobs, pyjson.ObjectOrEmpty(trial["opportunity"]))
	}
	var found []string
	seen := map[string]bool{}
	for _, blob := range blobs {
		for _, val := range blob {
			vals := []any{val}
			if list, ok := pyjson.List(val); ok {
				vals = list
			}
			for _, v := range vals {
				if s, ok := v.(string); ok {
					trimmed := strings.TrimSpace(s)
					if trimmed == "$critical" || trimmed == "$low" || trimmed == "$medium" {
						name := trimmed[1:]
						if !seen[name] {
							seen[name] = true
							found = append(found, name)
						}
					}
				}
			}
		}
	}
	return found
}

// UsesThresholdAlias reports whether a rule reads a threshold alias (only those rules get the
// 'thresholds assumed' guard).
func UsesThresholdAlias(rule Row, trial map[string]any) bool {
	return len(AliasNames(rule, trial)) > 0
}

// UnknownThresholds are the alias names a rule reads whose threshold is unknown.
func UnknownThresholds(rule Row, ctx *Ctx) []string {
	var thresholds map[string]any
	var trial map[string]any
	if ctx != nil {
		thresholds = ctx.Thresholds
		trial = ctx.Trial
	}
	var out []string
	for _, name := range AliasNames(rule, trial) {
		if _, ok := pyjson.Float(thresholds[name]); !ok {
			out = append(out, name)
		}
	}
	return out
}

// Pass evaluates a pass_if expression against a value.
func Pass(passIf any, value any) bool {
	m := passIfRe.FindStringSubmatch(strings.TrimSpace(pyjson.Str(passIf)))
	if m == nil || value == nil {
		return false
	}
	f, ok := pyjson.Float(value)
	if !ok {
		return false
	}
	if f == math.Inf(1) {
		switch m[1] {
		case "!=", ">", ">=":
			return true
		}
		return false
	}
	if f == math.Inf(-1) {
		switch m[1] {
		case "!=", "<", "<=":
			return true
		}
		return false
	}
	num, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return false
	}
	switch m[1] {
	case "==":
		return f == num
	case "!=":
		return f != num
	case "<=":
		return f <= num
	case ">=":
		return f >= num
	case "<":
		return f < num
	case ">":
		return f > num
	}
	return false
}

// ExcludedFrom are the normaliser's kind: excluded rows, the ONE source of spans, merged.
func ExcludedFrom(rows []Row) [][2]float64 {
	var spans [][2]float64
	for _, row := range rows {
		if pyjson.Str(row["kind"]) != "excluded" {
			continue
		}
		a, okA := pyjson.Float(row["from"])
		b, okB := pyjson.Float(row["to"])
		if !okA || !okB {
			continue
		}
		if b > a {
			spans = append(spans, [2]float64{a, b})
		}
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i][0] != spans[j][0] {
			return spans[i][0] < spans[j][0]
		}
		return spans[i][1] < spans[j][1]
	})
	var out [][2]float64
	for _, span := range spans {
		if n := len(out); n > 0 && span[0] <= out[n-1][1] {
			if span[1] > out[n-1][1] {
				out[n-1] = [2]float64{out[n-1][0], span[1]}
			}
		} else {
			out = append(out, span)
		}
	}
	return out
}

func triggers(check Row, samples []Row, ctx *Ctx) []Row {
	trig := pyjson.ObjectOrEmpty(check["trigger"])
	switch pyjson.Str(trig["kind"]) {
	case "cross":
		unit := pyjson.Str(trig["unit"])
		below := Num(trig["below"], ctx, nil)
		if below == nil {
			// A bad threshold alias never crashes evaluation: no triggers.
			return nil
		}
		var guids map[string]any
		if ctx != nil {
			guids = ctx.Guids
		}
		alias, ok := AliasMap(guids)[unit]
		if !ok {
			alias = unit
		}
		aliasStr := ""
		if pyjson.Truthy(alias) {
			aliasStr = pyjson.Str(alias)
		}
		var rows []Row
		for _, sample := range samples {
			if strings.EqualFold(pyjson.Str(sample["guid"]), aliasStr) {
				rows = append(rows, sample)
			}
		}
		sort.SliceStable(rows, func(i, j int) bool { return rowNum(rows[i], "t") < rowNum(rows[j], "t") })
		var out []Row
		var prev *float64
		for _, sample := range rows {
			var pct *float64
			hp, okHP := pyjson.Float(sample["hp"])
			maxHP, okMax := pyjson.Float(sample["max_hp"])
			if okHP && okMax {
				if maxHP > 0 {
					v := 100.0 * hp / maxHP
					pct = &v
				}
			}
			if pct == nil {
				prev = nil
				continue
			}
			t, okT := pyjson.Float(sample["t"])
			if !okT {
				prev = pct
				continue
			}
			if prev != nil && *pct < *below && *below <= *prev {
				if skipTrigger(check, t, ctx) {
					prev = pct
					continue
				}
				out = append(out, Row{
					"t": t, "guid": alias, "unit": unit,
					"src_guid": strings.ToLower(aliasStr),
					"dst_guid": strings.ToLower(aliasStr),
				})
			}
			prev = pct
		}
		return out
	case "mark":
		want, ok := trig["mark"].(string)
		if !ok || want == "" {
			return nil
		}
		var marks []Row
		var window *[2]float64
		if ctx != nil {
			marks = ctx.Marks
			window = ctx.Window
		}
		var out []Row
		for _, t := range MarkTimes(marks, want, window) {
			out = append(out, Row{"t": t, "unit": "mark"})
		}
		return out
	}
	// Event trigger: its kind FILTERS the rows (never dropped), together with every
	// where-style field.
	where := Row{}
	for k, v := range trig {
		if k != "unit" && k != "below" {
			where[k] = v
		}
	}
	var samplesAll []Row
	var trial map[string]any
	if ctx != nil {
		samplesAll = ctx.SamplesAll
		trial = ctx.Trial
	}
	byGUID := SampleRows(samplesAll)
	if len(samplesAll) == 0 {
		byGUID = SampleRows(samples)
	}
	var rows []Row
	if ctx != nil {
		rows = ctx.Rows
	}
	var out []Row
	for _, r := range rows {
		if Matches(r, where, byGUID, ctx, trial, nil) {
			out = append(out, r)
		}
	}
	return out
}

// skipTrigger reports a latency trigger at t carrying no chance: the answering bot is out of
// mana or dead (`check.skip_trigger_while`).
func skipTrigger(check Row, t float64, ctx *Ctx) bool {
	spec, ok := pyjson.Object(check["skip_trigger_while"])
	if !ok {
		return false
	}
	var guids map[string]any
	var samplesAll []Row
	if ctx != nil {
		guids = ctx.Guids
		samplesAll = ctx.SamplesAll
	}
	unit := pyjson.Str(spec["unit"])
	alias, ok := AliasMap(guids)[unit]
	if !ok {
		alias = unit
	}
	aliasStr := ""
	if pyjson.Truthy(alias) {
		aliasStr = pyjson.Str(alias)
	}
	byGUID := SampleRows(samplesAll)
	sample := At(byGUID, aliasStr, t)
	if sample == nil {
		return false
	}
	if pyjson.Truthy(spec["skip_when_dead"]) {
		if pyjson.Truthy(sample["dead"]) {
			return true
		}
		have := auraSet(sample)
		for id := range SpiritOfRedemptionAuras {
			if have[id] {
				return true
			}
		}
	}
	if spec["power_below_pct"] != nil {
		limit, ok := pyjson.Float(spec["power_below_pct"])
		if !ok {
			return false
		}
		power, okP := pyjson.Float(sample["power"])
		maxPower, okM := pyjson.Float(sample["max_power"])
		if !okP || !okM || maxPower == 0 {
			return false
		}
		pct := 100.0 * power / maxPower
		if pyjson.Truthy(sample["max_power"]) && pct < limit {
			return true
		}
	}
	return false
}

// MarkTimes are the times of runner marks of `kind` on the events' time base, inside window.
func MarkTimes(marks []Row, kind string, window *[2]float64) []float64 {
	var base float64
	hasBase := false
	for _, want := range []string{"fight_start", "setup_ok"} {
		for _, mark := range marks {
			if pyjson.Str(mark["kind"]) == want {
				if secs, ok := events.WallSecs(mark["wall"]); ok {
					base, hasBase = secs, true
				}
				break
			}
		}
		if hasBase {
			break
		}
	}
	var out []float64
	for _, mark := range marks {
		if pyjson.Str(mark["kind"]) != kind {
			continue
		}
		var t float64
		if wall, ok := events.WallSecs(mark["wall"]); ok && hasBase {
			t = wall - base
		} else {
			f, ok := pyjson.Float(mark["t"])
			if !ok {
				continue
			}
			t = f
		}
		if window != nil {
			if !(window[0] <= t && t <= window[1]) {
				continue
			}
		}
		out = append(out, t)
	}
	sort.Float64s(out)
	return out
}

// MarkSpans are [(a, b)] on the events' time base: from each mark of `kind` to seconds later.
func MarkSpans(marks []Row, kind string, seconds any) [][2]float64 {
	secs, ok := pyjson.Float(seconds)
	if !ok {
		return nil
	}
	var out [][2]float64
	for _, t := range MarkTimes(marks, kind, nil) {
		out = append(out, [2]float64{t, t + secs})
	}
	return out
}

// AfterMarkSpans are the spans dropped by where.not_within_s_after_mark, merged.
func AfterMarkSpans(rule Row, marks []Row) [][2]float64 {
	check := pyjson.ObjectOrEmpty(rule["check"])
	where := pyjson.ObjectOrEmpty(check["where"])
	spec := where["not_within_s_after_mark"]
	var specs []any
	switch val := spec.(type) {
	case map[string]any:
		specs = []any{val}
	case []any:
		specs = val
	}
	var spans [][2]float64
	for _, one := range specs {
		obj, ok := pyjson.Object(one)
		if !ok {
			continue
		}
		spans = append(spans, MarkSpans(marks, pyjson.Str(obj["kind"]), obj["s"])...)
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i][0] != spans[j][0] {
			return spans[i][0] < spans[j][0]
		}
		return spans[i][1] < spans[j][1]
	})
	var out [][2]float64
	for _, span := range spans {
		if n := len(out); n > 0 && span[0] <= out[n-1][1] {
			out[n-1] = [2]float64{out[n-1][0], math.Max(out[n-1][1], span[1])}
		} else {
			out = append(out, span)
		}
	}
	return out
}

// PriorEvent is one row of an earlier repeat with its absolute wall time.
type PriorEvent struct {
	Wall float64
	Row  Row
}

// PriorResponseSpans are the spans of a responder cooldown that started in an EARLIER repeat,
// on this rep's base. Only a rule with check.debounce_responder_across_reps reads them.
func PriorResponseSpans(rule Row, prior []PriorEvent, baseWall float64, hasBase bool, ctx *Ctx) [][2]float64 {
	check := pyjson.ObjectOrEmpty(rule["check"])
	if !pyjson.Truthy(check["debounce_responder_across_reps"]) || !hasBase {
		return nil
	}
	cooldown, ok := pyjson.Float(check["debounce_responder_s"])
	if !ok || cooldown <= 0 {
		return nil
	}
	where := Row{}
	for k, v := range pyjson.ObjectOrEmpty(check["response"]) {
		if k != "dst" {
			where[k] = v
		}
	}
	if pyjson.Str(check["metric"]) == "first" && len(where) == 0 {
		// a `first` rule has no `response` block: the answered ability is the `where` filter itself.
		checkWhere := pyjson.ObjectOrEmpty(check["where"])
		for _, k := range []string{"kind", "src", "dst", "spell", "spell_id"} {
			if v, ok := checkWhere[k]; ok {
				where[k] = v
			}
		}
	}
	if pyjson.Truthy(check["debounce_responder_spells"]) {
		where["spell"] = check["debounce_responder_spells"]
	}
	var trial map[string]any
	if ctx != nil {
		trial = ctx.Trial
	}
	var spans [][2]float64
	empty := map[string][]TimedSample{}
	for _, entry := range prior {
		if entry.Row == nil {
			continue
		}
		if !Matches(entry.Row, where, empty, ctx, trial, nil) {
			continue
		}
		a := entry.Wall - baseWall
		spans = append(spans, [2]float64{a, a + cooldown})
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i][0] != spans[j][0] {
			return spans[i][0] < spans[j][0]
		}
		return spans[i][1] < spans[j][1]
	})
	return spans
}

// AfterMarkSamples is `samples` without the spans AfterMarkSpans drops.
func AfterMarkSamples(samples []Row, rule Row, marks []Row) []Row {
	spans := AfterMarkSpans(rule, marks)
	if len(spans) == 0 {
		out := make([]Row, len(samples))
		copy(out, samples)
		return out
	}
	var out []Row
	for _, s := range samples {
		if !inSpans(spans, rowNum(s, "t")) {
			out = append(out, s)
		}
	}
	return out
}

func inSpans(spans [][2]float64, t float64) bool {
	for _, span := range spans {
		if span[0] <= t && t <= span[1] {
			return true
		}
	}
	return false
}

// Evaluate scores one rule against one repeat's rows, samples and marks.
func Evaluate(rule Row, rows []Row, samples []Row, marks []Row, ctx *Ctx) Result {
	if ctx == nil {
		ctx = &Ctx{}
	}
	local := *ctx
	ctx = &local
	ctx.spellErr = nil
	res := evaluate(rule, rows, samples, marks, ctx)
	if ctx.spellErr != nil {
		// The match that raised produced no verdict: Python's exception wins even over an
		// error a later conversion on this input would have raised.
		res.Err = ctx.spellErr
	}
	return res
}

func evaluate(rule Row, rows []Row, samples []Row, marks []Row, ctx *Ctx) Result {
	trial := ctx.Trial
	if trial == nil {
		trial = map[string]any{}
	}
	// The scored window is fight_start..fight_end: only rows and samples inside it count
	// (excluded rows stay: they carry the spans). Hp / aura / combat lookups still read ALL
	// samples so the window's first events have their data.
	window := ctx.Window
	allSamples := samples
	if window != nil {
		rows = WindowRows(rows, RuleWindow(window, rule))
		samples = WindowSamples(samples, window)
	}
	check := pyjson.ObjectOrEmpty(rule["check"])
	metric := pyjson.Str(check["metric"])
	where := pyjson.ObjectOrEmpty(check["where"])
	// where.not_within_s_after_mark: drop the events and samples within s seconds after each
	// mark of that kind. Excluded-span rows stay. Hp/aura lookups still read all samples.
	spans := AfterMarkSpans(rule, marks)
	if len(spans) > 0 {
		var keptRows []Row
		for _, r := range rows {
			if pyjson.Str(r["kind"]) == "excluded" || !inSpans(spans, rowNum(r, "t")) {
				keptRows = append(keptRows, r)
			}
		}
		rows = keptRows
		var keptSamples []Row
		for _, s := range samples {
			if !inSpans(spans, rowNum(s, "t")) {
				keptSamples = append(keptSamples, s)
			}
		}
		samples = keptSamples
	}
	ctx.Rows = rows
	ctx.SamplesAll = allSamples
	ctx.Marks = marks
	byGUID := SampleRows(allSamples)
	excluded := ExcludedFrom(rows)
	guardNote := ""
	if UsesThresholdAlias(rule, trial) && ctx.ThresholdsAssumed {
		guardNote = "thresholds assumed"
	}
	if names := UnknownThresholds(rule, ctx); len(names) > 0 {
		// A None threshold never reaches a comparison.
		return Result{Value: nil, K: 0, N: 0, Verdict: "INCONCLUSIVE",
			Evidence: []string{"threshold unknown"}, Reason: "threshold unknown",
			Guard: "threshold unknown"}
	}

	scored := func(evts []Row) []Row {
		var out []Row
		for _, r := range evts {
			if !inExcluded(excluded, rowNum(r, "t")) {
				out = append(out, r)
			}
		}
		return out
	}
	filter := func(pred func(Row) bool) []Row {
		var out []Row
		for _, r := range rows {
			if pred(r) {
				out = append(out, r)
			}
		}
		return out
	}

	switch metric {
	case "walk_metric":
		name := pyjson.Str(check["name"])
		report := ctx.WalkReport
		if report == nil {
			report = map[string]any{}
		}
		found := report[name]
		if found == nil {
			if metrics, ok := pyjson.Object(report["metrics"]); ok {
				found = metrics[name]
			}
		}
		var n int64
		obj, isObj := pyjson.Object(found)
		if isObj {
			// Python: int(found.get("n", 0)) inside try/except (TypeError, ValueError):
			// a bad value is 0, but int(inf)'s OverflowError escapes the guard.
			if raw, ok := obj["n"]; ok {
				v, err := pyjson.PyInt(raw)
				if uncaughtTypeValue(err) {
					return Result{Err: fmt.Errorf("walk n: %w", err)}
				}
				if err == nil {
					if i, fits := pyjson.Int64(v); fits {
						n = i
					}
				}
			}
		}
		if found == nil || n == 0 {
			ev := "walk n == 0"
			if found == nil {
				ev = "metric not in report"
			}
			return Result{Value: nil, N: 0, Verdict: "INCONCLUSIVE", Evidence: []string{ev}, Guard: guardNote}
		}
		var value float64
		if raw, hasMean := obj["mean"]; hasMean {
			// Python checks `"mean" in found` and calls float(); None raises TypeError.
			mean, ok := pyjson.Float(raw)
			if !ok {
				return Result{Verdict: "INCONCLUSIVE", Guard: guardNote,
					Err: fmt.Errorf("walk metric %q: mean %s is not a number", name, pyjson.Str(raw))}
			}
			value = mean
		} else {
			var k float64
			if raw, hasK := obj["k"]; hasK {
				converted, ok := pyjson.Float(raw)
				if !ok {
					return Result{Verdict: "INCONCLUSIVE", Guard: guardNote,
						Err: fmt.Errorf("walk metric %q: k %s is not a number", name, pyjson.Str(raw))}
				}
				k = converted
			}
			value = k / float64(n)
		}
		ok := Pass(rule["pass_if"], value)
		verdict := "FAIL"
		if ok {
			verdict = "PASS"
		}
		return Result{Value: value, N: n, Verdict: verdict,
			Evidence: []string{fmt.Sprintf("%s = %s", name, pyjson.FormatFloat(value))}, Guard: guardNote}

	case "count":
		var unknown []Row
		hits := scored(filter(func(r Row) bool {
			return Matches(r, where, byGUID, ctx, trial, &unknown)
		}))
		unknown = scored(unknown)
		// Record the attack state at every cast the rule watches: one line per live mob and
		// who it was on at that cast, so the verdict carries the evidence it was decided from.
		var attackEv []string
		if where["party_under_attack"] != nil {
			var party []string
			wantParty := []any{where["party_under_attack"]}
			if list, ok := pyjson.List(where["party_under_attack"]); ok {
				wantParty = list
			}
			for _, unit := range wantParty {
				for g := range UnitIDs(pyjson.Str(unit), ctx) {
					if g != "" && g != "$mob" {
						party = append(party, g)
					}
				}
			}
			base := Row{}
			for k, v := range where {
				if k != "party_under_attack" {
					base[k] = v
				}
			}
			names := UnitNames(ctx.Guids)
			baseHits := scored(filter(func(r Row) bool {
				return Matches(r, base, byGUID, ctx, trial, nil)
			}))
			if len(baseHits) > 5 {
				baseHits = baseHits[:5]
			}
			for _, r := range baseHits {
				state, detail := PartyAttackState(ctx.VictimsByGUID, byGUID, party, rowNum(r, "t"), r["seq"], ctx.Guids, names)
				said := "not under attack"
				if state == nil {
					said = "unknown"
				} else if b, ok := state.(bool); ok && b {
					said = "UNDER ATTACK"
				}
				detailS := "no live mob on record"
				if len(detail) > 0 {
					detailS = strings.Join(detail, ", ")
				}
				attackEv = append(attackEv, fmt.Sprintf("%s: %s (%s)", EvidenceLine(r), said, detailS))
			}
		}
		if len(unknown) > 0 {
			// The attack state at this event could not be read: missing evidence is not
			// "not under attack", so the repeat is not a pass.
			ev := []string{fmt.Sprintf("attack state unknown at %d event(s)", len(unknown))}
			limit := len(unknown)
			if limit > 5 {
				limit = 5
			}
			for _, r := range unknown[:limit] {
				ev = append(ev, EvidenceLine(r))
			}
			ev = append(ev, attackEv...)
			return Result{Value: nil, K: int64(len(hits)), N: 1, Verdict: "INCONCLUSIVE",
				Evidence: ev, Reason: "attack state unknown", Guard: guardNote}
		}
		var ev []string
		if len(hits) == 0 {
			nowhere := Row{}
			for k, v := range where {
				if k != "dst" {
					nowhere[k] = v
				}
			}
			same := scored(filter(func(r Row) bool {
				return Matches(r, nowhere, byGUID, ctx, trial, nil)
			}))
			opp := OpportunitySeconds(trial, samples, excluded, ctx)
			oppS := "opportunity n/a (no opportunity block)"
			if trial != nil && pyjson.Truthy(trial["opportunity"]) {
				oppS = fmt.Sprintf("opportunity %.0f s", opp)
			}
			ev = append(ev, fmt.Sprintf("%s, %d same-spell casts on other targets", oppS, len(same)))
			limit := len(same)
			if limit > 3 {
				limit = 3
			}
			for _, r := range same[:limit] {
				ev = append(ev, EvidenceLine(r))
			}
		} else {
			limit := len(hits)
			if limit > 5 {
				limit = 5
			}
			for _, r := range hits[:limit] {
				ev = append(ev, EvidenceLine(r))
			}
		}
		ev = append(ev, attackEv...)
		verdict := "FAIL"
		if Pass(rule["pass_if"], int64(len(hits))) {
			verdict = "PASS"
		}
		return Result{Value: int64(len(hits)), K: int64(len(hits)), N: 1, Verdict: verdict,
			Evidence: ev, Guard: guardNote}

	case "ratio":
		num := scored(filter(func(r Row) bool {
			return Matches(r, pyjson.ObjectOrEmpty(check["num"]), byGUID, ctx, trial, nil)
		}))
		den := scored(filter(func(r Row) bool {
			return Matches(r, pyjson.ObjectOrEmpty(check["den"]), byGUID, ctx, trial, nil)
		}))
		n := int64(len(den))
		k := int64(len(num))
		if len(den) == 0 {
			return Result{Value: nil, K: 0, N: 0, Verdict: "INCONCLUSIVE",
				Evidence: []string{"den 0"}, Guard: guardNote}
		}
		value := float64(k) / float64(n)
		verdict := "FAIL"
		if Pass(rule["pass_if"], value) {
			verdict = "PASS"
		}
		ev := []string{fmt.Sprintf("%d/%d", k, n)}
		limit := len(num)
		if limit > 5 {
			limit = 5
		}
		for _, r := range num[:limit] {
			ev = append(ev, EvidenceLine(r))
		}
		return Result{Value: value, K: k, N: n, Verdict: verdict, Evidence: ev, Guard: guardNote}

	case "latency":
		trigs := triggers(check, samples, ctx)
		respWhere := Row{}
		for k, v := range pyjson.ObjectOrEmpty(check["response"]) {
			respWhere[k] = v
		}
		dstMode := pyjson.Str(where["response_dst"])
		if where["response_dst"] == nil {
			dstMode = pyjson.Str(check["response_dst"])
		}
		var debounce *float64
		if check["trigger_debounce_s"] != nil {
			debounce = Num(check["trigger_debounce_s"], ctx, nil)
		} else if where["trigger_debounce_s"] != nil {
			debounce = Num(where["trigger_debounce_s"], ctx, nil)
		}
		// Python: within = float(check.get("within_s") or 0.0) is unguarded; resp_cd is
		// inside try/except (TypeError, ValueError).
		within, err := floatOrZero(check["within_s"])
		if err != nil {
			return Result{Err: fmt.Errorf("within_s: %w", err)}
		}
		respCD, err := guardedFloatOrZero(check["debounce_responder_s"])
		if err != nil {
			return Result{Err: fmt.Errorf("debounce_responder_s: %w", err)}
		}
		cdWhere := Row{}
		for k, v := range respWhere {
			if k != "dst" {
				cdWhere[k] = v
			}
		}
		if pyjson.Truthy(check["debounce_responder_spells"]) {
			cdWhere["spell"] = check["debounce_responder_spells"]
		}
		inCooldown := func(trig Row) bool {
			if respCD <= 0 {
				return false
			}
			t0 := rowNum(trig, "t")
			for _, cand := range rows {
				if cand == nil || sameRow(cand, trig) {
					continue
				}
				tc := rowNum(cand, "t")
				if 0.0 <= t0-tc && t0-tc < respCD && !inExcluded(excluded, tc) &&
					Matches(cand, cdWhere, byGUID, ctx, trial, nil) {
					return true
				}
			}
			return false
		}
		if debounce != nil || respCD > 0 {
			var kept []Row
			lastBySrc := map[string]float64{}
			sortedTrigs := append([]Row(nil), trigs...)
			sort.SliceStable(sortedTrigs, func(i, j int) bool { return rowNum(sortedTrigs[i], "t") < rowNum(sortedTrigs[j], "t") })
			for _, trig := range sortedTrigs {
				if inCooldown(trig) {
					// skipped for the responder's cooldown: it must not start its mob's
					// debounce window either
					continue
				}
				src := pyjson.Str(trig["src_guid"])
				if trig["src_guid"] == nil || src == "" {
					src = pyjson.Str(trig["src"])
					if trig["src"] == nil || src == "" {
						src = pyjson.Str(trig["guid"])
						if trig["guid"] == nil || src == "" {
							src = pyjson.Str(trig["unit"])
						}
					}
				}
				last, seen := lastBySrc[src]
				if debounce == nil || !seen || rowNum(trig, "t")-last >= *debounce {
					kept = append(kept, trig)
					lastBySrc[src] = rowNum(trig, "t")
				}
			}
			trigs = kept
		}
		var answered, counted int64
		var ev []string
		sortedTrigs := append([]Row(nil), trigs...)
		sort.SliceStable(sortedTrigs, func(i, j int) bool { return rowNum(sortedTrigs[i], "t") < rowNum(sortedTrigs[j], "t") })
		sortedRows := append([]Row(nil), rows...)
		sort.SliceStable(sortedRows, func(i, j int) bool { return rowNum(sortedRows[i], "t") < rowNum(sortedRows[j], "t") })
		for _, trig := range sortedTrigs {
			t0 := rowNum(trig, "t")
			// A trigger inside an excluded span, or whose response window crosses one,
			// carries no opportunity: skip it entirely.
			skip := inExcluded(excluded, t0)
			if !skip {
				for _, span := range excluded {
					if span[0] <= t0+within && t0 <= span[1] {
						skip = true
						break
					}
				}
			}
			if skip {
				continue
			}
			rw := Row{}
			for k, v := range respWhere {
				rw[k] = v
			}
			switch dstMode {
			case "$trigger.src":
				rw["dst"] = triggerSrc(trig)
			case "$trigger.dst":
				rw["dst"] = triggerDst(trig)
			}
			if pyjson.Truthy(check["same_dst"]) {
				rw["dst"] = triggerDstAny(trig)
			}
			counted++
			var hit Row
			for _, cand := range sortedRows {
				tc := rowNum(cand, "t")
				if !(t0 <= tc && tc <= t0+within) {
					continue
				}
				if inExcluded(excluded, tc) {
					continue
				}
				if Matches(cand, rw, byGUID, ctx, trial, nil) {
					hit = cand
					break
				}
			}
			if hit != nil {
				answered++
				if len(ev) < 5 {
					ev = append(ev, fmt.Sprintf("t=%.1f answered t=%.1f", t0, rowNum(hit, "t")))
				}
			}
		}
		if counted == 0 {
			return Result{Value: nil, K: 0, N: 0, Verdict: "INCONCLUSIVE",
				Evidence: []string{"no triggers"}, Guard: guardNote}
		}
		value := float64(answered) / float64(counted)
		verdict := "FAIL"
		if Pass(rule["pass_if"], value) {
			verdict = "PASS"
		}
		return Result{Value: value, K: answered, N: counted, Verdict: verdict,
			Evidence: append([]string{fmt.Sprintf("%d/%d", answered, counted)}, ev...), Guard: guardNote}

	case "uptime":
		unit := pyjson.Str(check["unit"])
		guids := ctx.Guids
		alias, ok := AliasMap(guids)[unit]
		if !ok {
			alias = unit
		}
		aliasStr := ""
		if pyjson.Truthy(alias) {
			aliasStr = strings.ToLower(pyjson.Str(alias))
		}
		phase := "in_combat"
		if v, ok := check["phase"]; ok {
			phase = pyjson.Str(v)
		}
		aura := map[int64]bool{}
		if check["aura"] != nil {
			vals := []any{check["aura"]}
			if list, ok := pyjson.List(check["aura"]); ok {
				vals = list
			}
			for _, a := range vals {
				if n, ok := pyjson.Int64(a); ok {
					aura[n] = true
				} else if f, ok := pyjson.Float(a); ok {
					aura[int64(f)] = true
				} else {
					aura = map[int64]bool{}
					break
				}
			}
		}
		if pyjson.Truthy(check["alive"]) {
			// Pet "alive" uptime: missing samples (pet gone) count as NOT alive, and the
			// denominator is ALL fight samples.
			times := map[float64]bool{}
			for _, s := range samples {
				t, ok := pyjson.Float(s["t"])
				if !ok {
					continue
				}
				if inExcluded(excluded, t) {
					continue
				}
				times[pyjson.Round(t, 2)] = true
			}
			if len(times) == 0 {
				return Result{Value: nil, K: 0, N: 0, Verdict: "INCONCLUSIVE",
					Evidence: []string{"no samples"}, Guard: guardNote}
			}
			byT := map[float64][]Row{}
			for _, s := range samples {
				t, ok := pyjson.Float(s["t"])
				if !ok {
					continue
				}
				key := pyjson.Round(t, 2)
				byT[key] = append(byT[key], s)
			}
			keys := make([]float64, 0, len(times))
			for t := range times {
				keys = append(keys, t)
			}
			sort.Float64s(keys)
			var up int64
			for _, t := range keys {
				var cands []Row
				for _, c := range byT[t] {
					if strings.ToLower(pyjson.Str(c["guid"])) == aliasStr {
						cands = append(cands, c)
					}
				}
				if len(cands) == 0 {
					continue // missing sample: NOT alive, still in the denominator
				}
				hp, ok := pyjson.Float(cands[0]["hp"])
				if !ok {
					hp = 0
				}
				if hp > 0 {
					up++
				}
			}
			n := int64(len(times))
			value := 0.0
			if n != 0 {
				value = float64(up) / float64(n)
			}
			verdict := "FAIL"
			if Pass(rule["pass_if"], value) {
				verdict = "PASS"
			}
			return Result{Value: value, K: up, N: n, Verdict: verdict,
				Evidence: []string{fmt.Sprintf("%d/%d samples", up, n)}, Guard: guardNote}
		}
		var rowsS []Row
		for _, s := range samples {
			if strings.ToLower(pyjson.Str(s["guid"])) != aliasStr {
				continue
			}
			if phase == "in_combat" || phase == "out_of_combat" {
				if pyjson.Truthy(s["in_combat"]) != (phase == "in_combat") {
					continue
				}
			}
			rowsS = append(rowsS, s)
		}
		sort.SliceStable(rowsS, func(i, j int) bool { return rowNum(rowsS[i], "t") < rowNum(rowsS[j], "t") })
		var kept []Row
		for _, s := range rowsS {
			if !inExcluded(excluded, rowNum(s, "t")) {
				kept = append(kept, s)
			}
		}
		rowsS = kept
		if len(rowsS) == 0 {
			return Result{Value: nil, K: 0, N: 0, Verdict: "INCONCLUSIVE",
				Evidence: []string{"no samples"}, Guard: guardNote}
		}
		var up int64
		for _, sample := range rowsS {
			have := auraSet(sample)
			hit := false
			for id := range aura {
				if have[id] {
					hit = true
				}
			}
			if hit {
				up++
			}
		}
		value := float64(up) / float64(len(rowsS))
		verdict := "FAIL"
		if Pass(rule["pass_if"], value) {
			verdict = "PASS"
		}
		return Result{Value: value, K: up, N: int64(len(rowsS)), Verdict: verdict,
			Evidence: []string{fmt.Sprintf("%d/%d samples", up, len(rowsS))}, Guard: guardNote}

	case "first":
		var cool [][2]float64
		if pyjson.Truthy(check["debounce_responder_across_reps"]) {
			for _, r := range rows {
				if pyjson.Str(r["kind"]) != "excluded" || pyjson.Str(r["why"]) != "prior rep responder cooldown" {
					continue
				}
				a, errA := pyjson.PyFloat(r["from"])
				b, errB := pyjson.PyFloat(r["to"])
				if uncaughtTypeValue(errA) || uncaughtTypeValue(errB) {
					return Result{Err: fmt.Errorf("excluded span from/to: %v / %v", errA, errB)}
				}
				if errA == nil && errB == nil && b > a {
					cool = append(cool, [2]float64{a, b})
				}
			}
		}
		var matches []Row
		for _, r := range rows {
			if Matches(r, where, byGUID, ctx, trial, nil) {
				matches = append(matches, r)
			}
		}
		if len(cool) > 0 && pyjson.Truthy(check["debounce_responder_across_reps"]) {
			cooldown, err := guardedFloatOrZero(check["debounce_responder_s"])
			if err != nil {
				return Result{Err: fmt.Errorf("debounce_responder_s: %w", err)}
			}
			if cooldown > 0 {
				var outside []Row
				for _, r := range scored(matches) {
					t := rowNum(r, "t")
					inside := false
					for _, span := range cool {
						if span[0] <= t && t <= span[1] {
							inside = true
							break
						}
					}
					if !inside {
						outside = append(outside, r)
					}
				}
				sort.SliceStable(outside, func(i, j int) bool { return rowNum(outside[i], "t") < rowNum(outside[j], "t") })
				if len(outside) > 0 {
					firstTime := rowNum(outside[0], "t")
					prevLast := math.Inf(-1)
					hasPrev := false
					for _, span := range cool {
						val := span[1] - cooldown
						if !hasPrev || val > prevLast {
							prevLast = val
							hasPrev = true
						}
					}
					if hasPrev && firstTime-prevLast <= cooldown+1.0 {
						// the whole rep rode the carried-over cooldown
						first := outside[0]
						return Result{Value: firstT(first), NoMatch: false, K: 0, N: 0,
							Verdict: "INCONCLUSIVE", Evidence: []string{EvidenceLine(first)},
							Reason: "item cooldown from rep N-1", Guard: guardNote}
					}
				}
			}
		}
		if len(cool) > 0 {
			// A cast stamped inside an earlier rep's item cooldown is impossible (or a proc
			// row): drop it from the hits.
			var kept []Row
			for _, r := range matches {
				t := rowNum(r, "t")
				inside := false
				for _, span := range cool {
					if span[0] <= t && t <= span[1] {
						inside = true
						break
					}
				}
				if !inside {
					kept = append(kept, r)
				}
			}
			matches = kept
		}
		hits := scored(matches)
		sort.SliceStable(hits, func(i, j int) bool { return rowNum(hits[i], "t") < rowNum(hits[j], "t") })
		if len(hits) == 0 {
			if len(cool) > 0 && windowCoveredByCooldown(rule, window, cool) {
				return Result{Value: nil, NoMatch: true, K: 0, N: 0, Verdict: "INCONCLUSIVE",
					Evidence: []string{"response window fully covered by an earlier rep's responder cooldown"},
					Reason:   "no opportunity (item on cooldown from an earlier rep)",
					Guard:    guardNote}
			}
			// No match: pass_if still decides, as +inf.
			verdict := "FAIL"
			if Pass(rule["pass_if"], math.Inf(1)) {
				verdict = "PASS"
			}
			return Result{Value: nil, NoMatch: true, K: 0, N: 1, Verdict: verdict,
				Evidence: []string{"no match"}, Guard: guardNote}
		}
		// Python stores the row's own t (an int stays an int in the verdicts JSON); only a
		// missing t falls back to 0.0.
		value := firstT(hits[0])
		verdict := "FAIL"
		if Pass(rule["pass_if"], value) {
			verdict = "PASS"
		}
		return Result{Value: value, K: 1, N: 1, Verdict: verdict,
			Evidence: []string{EvidenceLine(hits[0])}, Guard: guardNote}

	case "rate":
		hits := scored(filter(func(r Row) bool {
			return Matches(r, where, byGUID, ctx, trial, nil)
		}))
		if pyjson.Str(check["per"]) == "mob_alive" {
			alive := MobAliveSeconds(samples, excluded)
			// Python: float(check.get("min_alive_s") or 10.0) — 0 is falsy, so it means 10 s.
			floorS := 10.0
			if raw := check["min_alive_s"]; pyjson.Truthy(raw) {
				f, ok := pyjson.Float(raw)
				if !ok {
					return Result{Verdict: "INCONCLUSIVE", Guard: guardNote,
						Err: fmt.Errorf("rate min_alive_s %s is not a number", pyjson.Str(raw))}
				}
				floorS = f
			}
			if alive < floorS {
				return Result{Value: nil, K: int64(len(hits)), N: 0, Verdict: "INCONCLUSIVE",
					Evidence: []string{fmt.Sprintf("a mob was alive only %.0f s (needs %.0f s)", alive, floorS)},
					Reason:   "mob alive too short", Guard: guardNote}
			}
			value := float64(len(hits)) / alive * 60.0
			verdict := "FAIL"
			if Pass(rule["pass_if"], value) {
				verdict = "PASS"
			}
			return Result{Value: value, K: int64(len(hits)), N: 1, Verdict: verdict,
				Evidence: []string{fmt.Sprintf("%d in %.0f s with a mob alive", len(hits), alive)}, Guard: guardNote}
		}
		span := ScoredSpan(rows, samples, excluded, window)
		value := 0.0
		if span > 0 {
			value = float64(len(hits)) / span * 60.0
		}
		verdict := "FAIL"
		if Pass(rule["pass_if"], value) {
			verdict = "PASS"
		}
		return Result{Value: value, K: int64(len(hits)), N: 1, Verdict: verdict,
			Evidence: []string{fmt.Sprintf("%d in %.0f s scored", len(hits), span)}, Guard: guardNote}
	}

	metricS := pyjson.Str(check["metric"])
	return Result{Value: nil, Verdict: "INCONCLUSIVE",
		Evidence: []string{fmt.Sprintf("unknown metric %q", metricS)}, Guard: guardNote}
}

// sameRow reports whether a and b are the same row OBJECT (Python's `cand is trig`).
// Equal content is not identity: the normaliser dedupes identical rows, but a trigger and a
// different event can still serialise the same after crit/data are stripped, and Python treats
// those as two rows. A map's header pointer is the row's identity in Go.
func sameRow(a, b Row) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

func triggerSrc(trig Row) any {
	if pyjson.Truthy(trig["src_guid"]) {
		return trig["src_guid"]
	}
	if pyjson.Truthy(trig["src"]) {
		return trig["src"]
	}
	if pyjson.Truthy(trig["guid"]) {
		return trig["guid"]
	}
	return trig["unit"]
}

func triggerDst(trig Row) any {
	if pyjson.Truthy(trig["dst_guid"]) {
		return trig["dst_guid"]
	}
	if pyjson.Truthy(trig["dst"]) {
		return trig["dst"]
	}
	return trig["guid"]
}

func triggerDstAny(trig Row) any {
	if pyjson.Truthy(trig["dst_guid"]) {
		return trig["dst_guid"]
	}
	if pyjson.Truthy(trig["dst"]) {
		return trig["dst"]
	}
	if pyjson.Truthy(trig["guid"]) {
		return trig["guid"]
	}
	return trig["unit"]
}

// WindowRows keeps the rows inside the window; excluded rows stay.
func WindowRows(rows []Row, window *[2]float64) []Row {
	if window == nil {
		return rows
	}
	var out []Row
	for _, r := range rows {
		if pyjson.Str(r["kind"]) == "excluded" {
			out = append(out, r)
			continue
		}
		t, ok := pyjson.Float(r["t"])
		if !ok {
			continue
		}
		if window[0] <= t && t <= window[1] {
			out = append(out, r)
		}
	}
	return out
}

// RuleWindow is the row window for one rule: the fight window, widened below fight_start when
// the rule's own `where` reads before it.
func RuleWindow(window *[2]float64, rule Row) *[2]float64 {
	if window == nil {
		return nil
	}
	check := pyjson.ObjectOrEmpty(rule["check"])
	where := pyjson.ObjectOrEmpty(check["where"])
	var pre *float64
	if where["t_from"] != nil {
		if f, ok := pyjson.Float(where["t_from"]); ok {
			pre = &f
		}
	}
	if pre == nil || *pre >= 0 {
		return window
	}
	// a trigger row (cross/mark) keeps the fight window: only the `where` filter of a
	// count/first/rate rule moves rows, never the trigger times.
	if check["trigger"] != nil {
		return window
	}
	return &[2]float64{math.Min(window[0], *pre), window[1]}
}

// firstResponseWindow is [lo, hi] a first-match response must land in to pass, clipped to the
// passing side of pass_if.
func firstResponseWindow(rule Row, window *[2]float64) *[2]float64 {
	if window == nil {
		return nil
	}
	rw := RuleWindow(window, rule)
	m := passWindowRe.FindStringSubmatch(strings.TrimSpace(pyjson.Str(rule["pass_if"])))
	if m == nil {
		return nil
	}
	num, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return nil
	}
	switch m[1] {
	case "<=", "<":
		return &[2]float64{rw[0], math.Min(rw[1], num)}
	}
	return &[2]float64{math.Max(rw[0], num), rw[1]}
}

// windowCoveredByCooldown is true only when the merged cooldown spans cover the WHOLE
// response window.
func windowCoveredByCooldown(rule Row, window *[2]float64, cool [][2]float64) bool {
	span := firstResponseWindow(rule, window)
	if span == nil || len(cool) == 0 {
		return false
	}
	lo, hi := span[0], span[1]
	if hi < lo {
		return false
	}
	cur := lo
	sorted := append([][2]float64(nil), cool...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i][0] != sorted[j][0] {
			return sorted[i][0] < sorted[j][0]
		}
		return sorted[i][1] < sorted[j][1]
	})
	for _, span := range sorted {
		if span[1] < cur {
			continue
		}
		if span[0] > cur {
			return false
		}
		cur = math.Max(cur, span[1])
		if cur >= hi {
			return true
		}
	}
	return false
}

// WindowSamples keeps the samples inside the window.
func WindowSamples(samples []Row, window *[2]float64) []Row {
	if window == nil {
		return samples
	}
	var out []Row
	for _, s := range samples {
		t, ok := pyjson.Float(s["t"])
		if !ok {
			continue
		}
		if window[0] <= t && t <= window[1] {
			out = append(out, s)
		}
	}
	return out
}

// MobAliveSeconds is the seconds (1 Hz samples, each second once) in which at least one
// `mob:` unit had hp > 0.
func MobAliveSeconds(samples []Row, excluded [][2]float64) float64 {
	seconds := map[int64]bool{}
	for _, sample := range samples {
		if !strings.HasPrefix(pyjson.Str(sample["unit"]), "mob:") {
			continue
		}
		t, okT := pyjson.Float(sample["t"])
		hp, okHP := pyjson.Float(sample["hp"])
		if sample["hp"] == nil {
			okHP = true
			hp = 0
		}
		if !okT || !okHP {
			continue
		}
		if hp > 0 && !inExcluded(excluded, t) {
			seconds[int64(pyjson.Round(t, 0))] = true
		}
	}
	return float64(len(seconds))
}

// ScoredSpan is the scored seconds of a repeat: the window minus the excluded spans, or the
// rows/samples span when there is no window.
func ScoredSpan(rows, samples []Row, excluded [][2]float64, window *[2]float64) float64 {
	if window != nil {
		lo, hi := window[0], window[1]
		cut := 0.0
		for _, span := range excluded {
			cut += math.Max(0.0, math.Min(hi, span[1])-math.Max(lo, span[0]))
		}
		return math.Max(0.0, (hi-lo)-cut)
	}
	var ts []float64
	for _, r := range rows {
		if pyjson.Str(r["kind"]) == "excluded" {
			continue
		}
		ts = append(ts, rowNum(r, "t"))
	}
	for _, s := range samples {
		if t, ok := pyjson.Float(s["t"]); ok {
			ts = append(ts, t)
		}
	}
	if len(ts) == 0 {
		return 0.0
	}
	lo, hi := ts[0], ts[0]
	for _, t := range ts[1:] {
		lo = math.Min(lo, t)
		hi = math.Max(hi, t)
	}
	cut := 0.0
	for _, span := range excluded {
		cut += math.Max(0.0, math.Min(hi, span[1])-math.Max(lo, span[0]))
	}
	return math.Max(0.0, (hi-lo)-cut)
}

// OpportunitySeconds is the time the bot had a reason to act, from the trial's opportunity
// block, minus the excluded overlap (never double-counted).
func OpportunitySeconds(trial map[string]any, samples []Row, excluded [][2]float64, ctx *Ctx) float64 {
	opp := pyjson.ObjectOrEmpty(trial["opportunity"])
	if len(opp) == 0 {
		return 0.0
	}
	var guids map[string]any
	if ctx != nil {
		guids = ctx.Guids
	}
	unit := pyjson.Str(opp["unit"])
	alias, ok := AliasMap(guids)[unit]
	if !ok {
		alias = unit
	}
	aliasStr := ""
	if pyjson.Truthy(alias) {
		aliasStr = strings.ToLower(pyjson.Str(alias))
	}
	sorted := append([]Row(nil), samples...)
	sort.SliceStable(sorted, func(i, j int) bool { return rowNum(sorted[i], "t") < rowNum(sorted[j], "t") })
	total := 0.0
	var prev *float64
	for _, sample := range sorted {
		t, ok := pyjson.Float(sample["t"])
		if !ok {
			continue
		}
		if strings.ToLower(pyjson.Str(sample["guid"])) != aliasStr {
			continue
		}
		if prev != nil {
			var pct *float64
			hp, okHP := pyjson.Float(sample["hp"])
			maxHP, okMax := pyjson.Float(sample["max_hp"])
			if okHP && okMax && maxHP > 0 {
				v := 100.0 * hp / maxHP
				pct = &v
			}
			ok2 := true
			if opp["hp_lt"] != nil {
				negInf := math.Inf(-1)
				limit := Num(opp["hp_lt"], ctx, &negInf)
				if !(pct != nil && limit != nil && *pct < *limit) {
					ok2 = false
				}
			}
			if opp["hp_gt"] != nil {
				zero := 0.0
				limit := Num(opp["hp_gt"], ctx, &zero)
				if !(pct != nil && limit != nil && *pct > *limit) {
					ok2 = false
				}
			}
			if opp["in_combat"] != nil {
				if pyjson.Truthy(sample["in_combat"]) != pyjson.Truthy(opp["in_combat"]) {
					ok2 = false
				}
			}
			if opp["has_aura"] != nil {
				want := map[int64]bool{}
				if list, ok := pyjson.List(opp["has_aura"]); ok {
					for _, a := range list {
						if n, ok := pyjson.Int64(a); ok {
							want[n] = true
						} else if f, ok := pyjson.Float(a); ok {
							want[int64(f)] = true
						} else {
							want = map[int64]bool{}
							break
						}
					}
				}
				have := auraSet(sample)
				intersects := false
				for id := range want {
					if have[id] {
						intersects = true
					}
				}
				if !intersects {
					ok2 = false
				}
			}
			if ok2 {
				// The interval (prev, t] contributes its length minus any merged excluded
				// overlap.
				seg := t - *prev
				for _, span := range excluded {
					seg -= math.Max(0.0, math.Min(t, span[1])-math.Max(*prev, span[0]))
				}
				total += math.Max(0.0, seg)
			}
		}
		v := t
		prev = &v
	}
	return total
}
