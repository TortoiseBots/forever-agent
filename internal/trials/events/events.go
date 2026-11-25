// Package events turns raw turtle-agent rows into normalised events.jsonl, one row per event.
//
// Inputs per repeat dir: raw-ta.jsonl (agent `events` rows: seq, time, kind, source, target,
// spell_id, amount, crit, data; source/target hold name+guid), samples.jsonl, marks.jsonl,
// setup.json (guids: player, bot, pet, mobs, marked). The output carries re-based seconds on
// one time base (the fight_start wall, or setup_ok when no pull).
package events

import (
	"bufio"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/spells"
)

// KindMap maps an agent event kind to the normaliser's kind.
var KindMap = map[string]string{
	"spell_go":     "cast",
	"spell_start":  "cast_start",
	"spell_fail":   "cast_fail",
	"aura_applied": "aura_on",
	"aura_removed": "aura_off",
	"melee":        "damage",
	"spell_damage": "damage",
	"periodic":     "damage",
	"heal":         "heal",
	"death":        "death",
	// The core-gains round's combat-log kinds: an interrupt is the Kick/Counterspell trials' answer, a dispel
	// ends a debuff episode for the bug hunter, and the rest join the stream as their own event kinds.
	"interrupt":      "interrupt",
	"dispel":         "dispel",
	"absorb":         "absorb",
	"miss":           "miss",
	"resist":         "resist",
	"immune":         "immune",
	"instakill":      "instakill",
	"environmental":  "damage",
	"knockback":      "knockback",
	"knockback_land": "knockback_land",
}

// Row is one events/samples/marks row.
type Row = map[string]any

// WallSecs is a wall clock value as epoch seconds: a number stays itself, an ISO string is
// parsed, ok is false when it is neither.
func WallSecs(value any) (float64, bool) {
	if value == nil {
		return 0, false
	}
	if n, ok := pyjson.Num(value); ok {
		return n, true
	}
	text := pyjson.Str(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05-0700",
	} {
		if dt, err := time.Parse(layout, text); err == nil {
			return epochSecs(dt), true
		}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if dt, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return epochSecs(dt), true
		}
	}
	return 0, false
}

// epochSecs is datetime.timestamp(): whole seconds plus the fraction, added separately, so a
// 2026 wall clock keeps its nanoseconds instead of losing them in the 1e9-second magnitude.
func epochSecs(dt time.Time) float64 {
	return float64(dt.Unix()) + float64(dt.Nanosecond())/1e9
}

// pythonIntBase is Python's int(text, base) for a base-10 or base-16 literal: an optional
// sign, an optional 0x prefix (base 16 only), and underscores between digits or directly
// after the prefix.
func pythonIntBase(text string, base int) (*big.Int, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil, false
	}
	sign := 1
	if t[0] == '+' || t[0] == '-' {
		if t[0] == '-' {
			sign = -1
		}
		t = t[1:]
	}
	prefixed := false
	if base == 16 && len(t) >= 2 && t[0] == '0' && (t[1] == 'x' || t[1] == 'X') {
		t = t[2:]
		prefixed = true
	}
	digit := func(c byte) bool {
		switch {
		case c >= '0' && c <= '9':
			return int(c-'0') < base
		case c >= 'a' && c <= 'f':
			return base == 16
		}
		return false
	}
	var digits strings.Builder
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c == '_' {
			if i == 0 && prefixed {
				continue
			}
			if i == 0 || i == len(t)-1 || !digit(t[i-1]) || !digit(t[i+1]) {
				return nil, false
			}
			continue
		}
		if !digit(c) {
			return nil, false
		}
		digits.WriteByte(c)
	}
	if digits.Len() == 0 {
		return nil, false
	}
	n, ok := new(big.Int).SetString(digits.String(), base)
	if !ok {
		return nil, false
	}
	if sign < 0 {
		n.Neg(n)
	}
	return n, true
}

// NormGUID normalises a guid to Python's "0x%016x" form (lower case); unparseable text passes
// through lower-cased. Integers are arbitrary precision, as in Python: a decimal guid at or
// above 2^63 still becomes its hex form.
func NormGUID(value any) string {
	if !pyjson.Truthy(value) {
		return ""
	}
	text := strings.ToLower(strings.TrimSpace(pyjson.Str(value)))
	base := 10
	if strings.HasPrefix(text, "0x") {
		base = 16
	}
	// Python: int(text) for a decimal guid, int(text, 16) for a 0x one. It accepts a sign
	// before the 0x prefix ("-0x5"), not after ("0x-5"), and underscores between digits or
	// right after the prefix ("0x_5"). A parse failure returns the normalised text.
	n, ok := pythonIntBase(text, base)
	if !ok {
		return text
	}
	sign := ""
	if n.Sign() < 0 {
		sign = "-"
	}
	hex := new(big.Int).Abs(n).Text(16)
	// Python's "%016x" width includes the sign: a negative -1 is 0x-000000000000001.
	width := 16
	if sign == "-" {
		width = 15
	}
	if len(hex) < width {
		hex = strings.Repeat("0", width-len(hex)) + hex
	}
	return "0x" + sign + hex
}

// SideGUID is a row's guid for one side: a flat <side>_guid/<side>Guid or the nested object.
func SideGUID(row Row, side string) string {
	for _, key := range []string{side + "_guid", side + "Guid"} {
		if flat := row[key]; pyjson.Truthy(flat) {
			return NormGUID(flat)
		}
	}
	if value := row[side]; value != nil {
		if obj, ok := pyjson.Object(value); ok {
			if pyjson.Truthy(obj["guid"]) {
				return NormGUID(obj["guid"])
			}
		}
	}
	return ""
}

// SideName is a row's display name for one side.
func SideName(row Row, side string) string {
	value := row[side]
	if obj, ok := pyjson.Object(value); ok {
		if pyjson.Truthy(obj["name"]) {
			return pyjson.Str(obj["name"])
		}
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

// ReadJSONL reads a JSON-lines file the way the bench's `_read_jsonl` does: a missing file
// gives no rows and a line that is not JSON is skipped. A line that parses to something other
// than an object is an error, naming the file and line: the Python keeps such a row and then
// fails on `row.get(...)`, so it must never be silently dropped.
func ReadJSONL(path string) ([]Row, error) {
	rows, _, err := ReadJSONLNumbered(path)
	return rows, err
}

// ReadJSONLNumbered is ReadJSONL with each row's 1-based line number. It keeps reading after
// a non-object line so the caller sees every object row the Python would see, and returns the
// first such error.
func ReadJSONLNumbered(path string) ([]Row, []int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil
	}
	defer f.Close()
	var rows []Row
	var lines []int
	var firstErr error
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		doc, err := pyjson.Unmarshal([]byte(line))
		if err != nil {
			continue
		}
		row, ok := pyjson.Object(doc)
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s:%d: expected a JSON object, got %s", path, lineNo, jsonKind(doc))
			}
			continue
		}
		rows = append(rows, row)
		lines = append(lines, lineNo)
	}
	return rows, lines, firstErr
}

// jsonKind names what a non-object JSON line parsed to, for the error above.
func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case string:
		return "a string"
	case int64, *big.Int:
		return "a number"
	case float64:
		return "a number"
	case []any:
		return "a list"
	}
	return "a non-object value"
}

// Span is an excluded span: re-based seconds [A, B] with the reason it was excluded.
type Span struct {
	A   float64
	B   float64
	Why string
}

// ReWall is a wall clock (ISO) or rebased seconds relative to baseWall.
//
// Marks carry ISO walls; samples/events carry rebased seconds (t seconds since fight_start,
// or setup_ok when no pull). score/rules never read the raw marks: they read the normaliser's
// `kind: excluded` rows.
func ReWall(value any, baseWall float64) (float64, bool) {
	if value == nil {
		return 0, false
	}
	if n, ok := pyjson.Num(value); ok {
		return n, true
	}
	secs, ok := WallSecs(value)
	if !ok {
		return 0, false
	}
	return secs - baseWall, true
}

// MergeSpans merges overlapping/touching spans; the first why is kept.
func MergeSpans(spans []Span) []Span {
	ordered := make([]Span, 0, len(spans))
	for _, s := range spans {
		if s.B > s.A {
			ordered = append(ordered, s)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].A != ordered[j].A {
			return ordered[i].A < ordered[j].A
		}
		return ordered[i].B < ordered[j].B
	})
	var out []Span
	for _, s := range ordered {
		if n := len(out); n > 0 && s.A <= out[n-1].B {
			if s.B > out[n-1].B {
				out[n-1] = Span{A: out[n-1].A, B: s.B, Why: out[n-1].Why}
			}
		} else {
			out = append(out, s)
		}
	}
	return out
}

// TimeBase is the fight_start wall (or setup_ok when no pull): ONE base for samples and raws.
func TimeBase(marks, samples []Row) float64 {
	for _, mark := range marks {
		if kind, _ := mark["kind"].(string); kind == "fight_start" {
			if secs, ok := WallSecs(mark["wall"]); ok {
				return secs
			}
			break
		}
	}
	for _, mark := range marks {
		if kind, _ := mark["kind"].(string); kind == "setup_ok" {
			if secs, ok := WallSecs(mark["wall"]); ok {
				return secs
			}
			break
		}
	}
	if len(samples) > 0 {
		if secs, ok := WallSecs(samples[0]["wall"]); ok {
			return secs
		}
	}
	return 0.0
}

// MonoOffset is the median (sample wall - mono_ms): the agent's monotonic clock on the
// runner's steady wall. ok is false for old runs without mono_ms.
func MonoOffset(samples []Row) (float64, bool) {
	var offsets []float64
	for _, smp := range samples {
		wall, ok := WallSecs(smp["wall"])
		if !ok {
			continue
		}
		mono, ok := pyjson.Num(smp["mono_ms"])
		if !ok || smp["mono_ms"] == nil {
			continue
		}
		offsets = append(offsets, wall-mono/1000.0)
	}
	if len(offsets) == 0 {
		return 0, false
	}
	sort.Float64s(offsets)
	return offsets[len(offsets)/2], true
}

// RawTime is a raw row's re-based seconds (the normaliser's `t`): mono_ms first, then ISO wall.
func RawTime(raw Row, baseWall float64, monoOffset float64, hasMono bool) float64 {
	fallback, ok := pyjson.Float(raw["t"])
	if !ok {
		fallback = 0.0
	}
	var t float64
	secs, ok := WallSecs(raw["time"])
	switch {
	case !ok:
		t = fallback
	case baseWall != 0 && absF(baseWall) > 1e-9:
		t = secs - baseWall
	default:
		t = fallback
	}
	if hasMono && raw["mono_ms"] != nil && baseWall != 0 {
		if mono, ok := pyjson.Num(raw["mono_ms"]); ok {
			t = monoOffset + mono/1000.0 - baseWall
		}
	}
	return t
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// AttackVictim is one attack packet: the attacker's victim at T (empty for a stop, meaning
// "not attacking").
type AttackVictim struct {
	Seq    int64
	HasSeq bool
	T      float64
	Victim string
}

// AttackVictims builds per-attacker [(seq, t, victim)] from attack_start (a victim) and
// attack_stop (no victim), times re-based like events, one entry per packet in file order.
// Python guards int(seq) with except (TypeError, ValueError); int(inf)'s OverflowError
// escapes it and is returned here.
func AttackVictims(raws, marks, samples []Row) (map[string][]AttackVictim, error) {
	baseWall := TimeBase(marks, samples)
	mono, hasMono := MonoOffset(samples)
	byGUID := map[string][]AttackVictim{}
	for _, raw := range raws {
		kind, _ := raw["kind"].(string)
		if kind != "attack_start" && kind != "attack_stop" {
			continue
		}
		guid := SideGUID(raw, "source")
		if guid == "" {
			continue
		}
		var seq int64
		hasSeq := false
		if raw["seq"] != nil {
			n, err := pyjson.PyInt(raw["seq"])
			if err != nil && !pyjson.CaughtByTypeValue(err) {
				return nil, fmt.Errorf("attack seq: %w", err)
			}
			if err == nil {
				if i, ok := pyjson.Int64(n); ok {
					seq, hasSeq = i, true
				}
			}
		}
		t := RawTime(raw, baseWall, mono, hasMono)
		victim := ""
		if kind == "attack_start" {
			victim = SideGUID(raw, "target")
		}
		byGUID[guid] = append(byGUID[guid], AttackVictim{
			Seq: seq, HasSeq: hasSeq, T: pyjson.Round(t, 2), Victim: victim,
		})
	}
	for _, rows := range byGUID {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].T < rows[j].T })
	}
	return byGUID, nil
}

// Normalise writes events.jsonl in repDir and returns the rows list.
//
// One time base: samples AND raws are re-based from their ISO `wall` onto the fight_start
// wall (or setup_ok when no pull). Events and the excluded rows carry re-based seconds;
// score/rules read ONLY this output (plus the untouched samples), never the runner's raw
// marks.
func Normalise(repDir string, trial map[string]any) ([]Row, error) {
	rawPath := filepath.Join(repDir, "raw-ta.jsonl")
	raw, rawLines, err := ReadJSONLNumbered(rawPath)
	if err != nil {
		return nil, err
	}
	samples, err := ReadJSONL(filepath.Join(repDir, "samples.jsonl"))
	if err != nil {
		return nil, err
	}
	marks, err := ReadJSONL(filepath.Join(repDir, "marks.jsonl"))
	if err != nil {
		return nil, err
	}
	setup := map[string]any{}
	if doc, err := pyjson.UnmarshalFile(filepath.Join(repDir, "setup.json")); err == nil {
		setup = pyjson.ObjectOrEmpty(doc)
	}

	guids := pyjson.ObjectOrEmpty(setup["guids"])
	player := NormGUID(guids["player"])
	bot := NormGUID(guids["bot"])
	pet := NormGUID(guids["pet"])
	mobs := map[string]bool{}
	if list, ok := pyjson.List(guids["mobs"]); ok {
		for _, g := range list {
			if pyjson.Truthy(g) {
				mobs[NormGUID(g)] = true
			}
		}
	}
	marked := NormGUID(guids["marked"])
	extras := []string{}
	if list, ok := pyjson.List(guids["extras"]); ok {
		for _, g := range list {
			extras = append(extras, NormGUID(g))
		}
	}
	if len(extras) > 3 {
		extras = extras[:3]
	}

	// t = wall - fight_start wall (or setup_ok when no pull): ONE base for samples and raws.
	baseWall := TimeBase(marks, samples)

	// Excluded spans in re-based seconds (ONE source for score/rules: the kind: excluded rows below).
	var excluded []Span
	for _, mark := range marks {
		kind, _ := mark["kind"].(string)
		switch kind {
		case "excluded":
			ra, okA := ReWall(mark["from"], baseWall)
			rb, okB := ReWall(mark["to"], baseWall)
			why := ""
			if mark["why"] != nil {
				why = pyjson.Str(mark["why"])
			}
			if okA && okB && rb > ra {
				excluded = append(excluded, Span{A: ra, B: rb, Why: why})
			}
		case "gm_heal":
			if w, ok := WallSecs(mark["wall"]); ok {
				excluded = append(excluded, Span{A: w - baseWall, B: w - baseWall + 3.0, Why: "floor"})
			}
		}
	}
	excluded = MergeSpans(excluded)

	monoOffset, hasMono := MonoOffset(samples)

	roleOf := func(guid string) string {
		switch {
		case guid != "" && guid == player:
			return "player"
		case guid != "" && guid == bot:
			return "bot"
		case guid != "" && guid == pet:
			return "pet"
		case guid != "" && inList(extras, guid):
			return "extra"
		case guid != "" && (mobs[guid] || guid == marked):
			return "mob"
		}
		return "other"
	}
	unitOf := func(guid string) string {
		switch {
		case guid != "" && guid == player:
			return "$player"
		case guid != "" && guid == bot:
			return "$bot"
		case guid != "" && guid == pet:
			return "$pet"
		case guid != "" && inList(extras, guid):
			return fmt.Sprintf("$extra%d", indexOf(extras, guid)+1)
		case guid != "" && guid == marked:
			return "$marked"
		case guid != "" && mobs[guid]:
			return "mob:" + guid
		}
		return guid
	}

	// Samples: re-base ISO walls onto the fight_start wall, same base as raws.
	rebased := make([]Row, 0, len(samples))
	for _, sample := range samples {
		next := make(Row, len(sample))
		for k, v := range sample {
			next[k] = v
		}
		if wall := next["wall"]; wall != nil {
			if secs, ok := WallSecs(wall); ok && baseWall != 0 {
				next["t"] = pyjson.Round(secs-baseWall, 2)
			}
		}
		rebased = append(rebased, next)
	}
	var buf strings.Builder
	for _, sample := range rebased {
		line, err := pyjson.MarshalLine(sample)
		if err != nil {
			return nil, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(repDir, "samples.jsonl"), []byte(buf.String()), 0o644); err != nil {
		return nil, err
	}
	samples = rebased

	// Samples by guid, sorted by t.
	type timedSample struct {
		T      float64
		Sample Row
	}
	byGUID := map[string][]timedSample{}
	for _, sample := range samples {
		guid := NormGUID(sample["guid"])
		t, ok := pyjson.Num(sample["t"])
		if !ok {
			continue
		}
		byGUID[guid] = append(byGUID[guid], timedSample{T: t, Sample: sample})
	}
	for _, rows := range byGUID {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].T < rows[j].T })
	}

	hpAt := func(guid string, t float64) any {
		rows := byGUID[guid]
		var best *timedSample
		for i := range rows {
			st := rows[i].T
			if st <= t && t-st <= 1.0 {
				best = &rows[i]
			} else if st > t {
				break
			}
		}
		if best == nil {
			return nil
		}
		hp, okHP := pyjson.Float(best.Sample["hp"])
		maxHP, okMax := pyjson.Float(best.Sample["max_hp"])
		if !okHP || !okMax || maxHP <= 0 {
			return nil
		}
		return 100.0 * hp / maxHP
	}

	// Dedupe on (seq, content): null seq rows dedupe on content alone.
	seen := map[string]bool{}
	var out []Row
	dropped := map[string]int64{}
	for i, row := range raw {
		seqVal := row["seq"]
		switch seqVal.(type) {
		case []any, map[string]any:
			// Python builds the key tuple (seq, content) and a list/dict seq is unhashable.
			return nil, fmt.Errorf("%s:%d: unhashable seq %s", rawPath, rawLines[i], jsonKind(seqVal))
		}
		content, err := pyjson.MarshalLine(row)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", rawPath, rawLines[i], err)
		}
		// The content already holds the seq, so equal content means the same (seq, content)
		// tuple Python would dedupe on.
		key := string(content)
		if seen[key] {
			continue
		}
		seen[key] = true
		kind, mapped := KindMap[pyjson.Str(row["kind"])]
		if !mapped {
			dropped[pyjson.Str(row["kind"])]++
			continue
		}
		if row["time"] == nil && row["t"] == nil {
			dropped["<missing time>"]++
			continue
		}
		t := RawTime(row, baseWall, monoOffset, hasMono)
		srcGUID := SideGUID(row, "source")
		dstGUID := SideGUID(row, "target")
		var seq any
		if row["seq"] != nil {
			n, err := pyjson.PyInt(row["seq"])
			if err != nil {
				if !pyjson.CaughtByTypeValue(err) {
					return nil, fmt.Errorf("%s:%d: seq: %w", rawPath, rawLines[i], err)
				}
			} else {
				seq = n
			}
		}
		var spellID any
		if row["spell_id"] != nil {
			n, err := pyjson.PyInt(row["spell_id"])
			if err != nil {
				if !pyjson.CaughtByTypeValue(err) {
					return nil, fmt.Errorf("%s:%d: spell_id: %w", rawPath, rawLines[i], err)
				}
			} else {
				spellID = n
			}
		}
		var spellName any
		if id, ok := pyjson.Int64(spellID); ok {
			spellName = spells.NameFor(id)
		} else if bigID, ok := spellID.(*big.Int); ok {
			// Python's name_for(str(id)) for an id outside the table is str(id).
			spellName = bigID.String()
		}
		ev := Row{
			"t":          pyjson.Round(t, 2),
			"wall":       row["time"],
			"kind":       kind,
			"seq":        seq,
			"src":        SideName(row, "source"),
			"src_guid":   srcGUID,
			"dst":        SideName(row, "target"),
			"dst_guid":   dstGUID,
			"src_unit":   unitOf(srcGUID),
			"dst_unit":   unitOf(dstGUID),
			"spell_id":   spellID,
			"spell":      spellName,
			"amount":     row["amount"],
			"dst_hp_pct": hpAt(dstGUID, t),
			"src_role":   roleOf(srcGUID),
			"dst_role":   roleOf(dstGUID),
			"origin":     "ta",
		}
		// The core-gains fields are written only when the row carries them, so older recordings keep their shape.
		// pyjson.Str(nil) is "None", so a row without the key must be checked first.
		if row["reason"] != nil {
			if reason := pyjson.Str(row["reason"]); reason != "" {
				ev["reason"] = reason
			}
		}
		if row["interrupted_spell_id"] != nil {
			ev["interrupted_spell_id"] = row["interrupted_spell_id"]
		}
		if row["absorbed_spell_id"] != nil {
			ev["absorbed_spell_id"] = row["absorbed_spell_id"]
		}
		if row["dispelled"] != nil {
			ev["dispelled"] = row["dispelled"]
		}
		out = append(out, ev)
	}

	// Excluded rows from marks (gm_heal, excluded) become kind: excluded rows.
	for _, span := range excluded {
		out = append(out, Row{
			"t":          pyjson.Round(span.A, 2),
			"wall":       nil,
			"kind":       "excluded",
			"src":        "",
			"src_guid":   "",
			"dst":        "",
			"dst_guid":   "",
			"src_unit":   "",
			"dst_unit":   "",
			"spell_id":   nil,
			"spell":      nil,
			"amount":     nil,
			"dst_hp_pct": nil,
			"src_role":   "other",
			"dst_role":   "other",
			"origin":     "marks",
			"from":       pyjson.Round(span.A, 2),
			"to":         pyjson.Round(span.B, 2),
			"why":        span.Why,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return rowT(out[i]) < rowT(out[j])
	})
	buf.Reset()
	for _, row := range out {
		line, err := pyjson.MarshalLine(row)
		if err != nil {
			return nil, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(repDir, "events.jsonl"), []byte(buf.String()), 0o644); err != nil {
		return nil, err
	}
	// Kept in the output, not thrown away: agent-ring drops and excluded spans.
	droppedKinds := map[string]any{}
	for k, v := range dropped {
		droppedKinds[k] = v
	}
	var excludedDocs []any
	for _, span := range excluded {
		excludedDocs = append(excludedDocs, map[string]any{
			"from": pyjson.Round(span.A, 2), "to": pyjson.Round(span.B, 2), "why": span.Why,
		})
	}
	summary := map[string]any{
		"dropped_kinds":  droppedKinds,
		"excluded_spans": excludedDocs,
	}
	if excludedDocs == nil {
		summary["excluded_spans"] = []any{}
	}
	if err := pyjson.Dump(filepath.Join(repDir, "events_summary.json"), summary); err != nil {
		return nil, err
	}
	return out, nil
}

// RowT is a row's re-based time, 0 when it has none.
func RowT(row Row) float64 { return rowT(row) }

// FightWindow is the scored fight: (lo, hi, fightEndMissing, ok) on the events' own time base.
// lo is the fight_start mark (the pull, the fight's first hostile action), else 0 when only
// setup_ok exists, else the first sample; hi is the fight_end mark, else the last sample taken in
// combat, else the last sample. A recording with neither marks nor samples has no window, so a
// caller must report the missing evidence instead of judging an empty span.
func FightWindow(marks, samples []Row) (float64, float64, bool, bool) {
	var fs, so, fe Row
	for _, mark := range marks {
		switch pyjson.Str(mark["kind"]) {
		case "fight_start":
			if fs == nil {
				fs = mark
			}
		case "setup_ok":
			if so == nil {
				so = mark
			}
		case "fight_end":
			fe = mark
		}
	}
	var base float64
	hasBase := false
	if fs != nil {
		base, hasBase = WallSecs(fs["wall"])
	}
	if !hasBase && so != nil {
		base, hasBase = WallSecs(so["wall"])
	}
	type timed struct {
		T      float64
		Sample Row
	}
	var times []timed
	for _, sample := range samples {
		t, ok := pyjson.Float(sample["t"])
		if !ok {
			continue
		}
		times = append(times, timed{T: t, Sample: sample})
	}
	var lo *float64
	if fs != nil {
		lo = markTime(fs, base, hasBase)
	}
	if lo == nil {
		if fs != nil || so != nil {
			v := 0.0
			lo = &v
		} else if len(times) > 0 {
			v := times[0].T
			for _, item := range times[1:] {
				if item.T < v {
					v = item.T
				}
			}
			lo = &v
		}
	}
	var hi *float64
	if fe != nil {
		hi = markTime(fe, base, hasBase)
	}
	missing := hi == nil
	if missing {
		var fight, later []float64
		for _, item := range times {
			if lo == nil || item.T >= *lo {
				later = append(later, item.T)
				if pyjson.Truthy(item.Sample["in_combat"]) {
					fight = append(fight, item.T)
				}
			}
		}
		pickMax := func(vals []float64) *float64 {
			if len(vals) == 0 {
				return nil
			}
			v := vals[0]
			for _, x := range vals[1:] {
				if x > v {
					v = x
				}
			}
			return &v
		}
		hi = pickMax(fight)
		if hi == nil {
			hi = pickMax(later)
		}
	}
	if lo == nil || hi == nil {
		return 0, 0, missing, false
	}
	if *hi < *lo {
		return *lo, *lo, missing, true
	}
	return *lo, *hi, missing, true
}

// markTime is a mark's time on the normaliser's base: its ISO wall minus the base wall, else its
// plain numeric t.
func markTime(mark Row, baseWall float64, hasBase bool) *float64 {
	if mark == nil {
		return nil
	}
	if wall, ok := WallSecs(mark["wall"]); ok && hasBase {
		v := wall - baseWall
		return &v
	}
	if f, ok := pyjson.Float(mark["t"]); ok {
		return &f
	}
	return nil
}

func rowT(row Row) float64 {
	if n, ok := pyjson.Num(row["t"]); ok {
		return n
	}
	return 0.0
}

func inList(list []string, want string) bool { return indexOf(list, want) >= 0 }

func indexOf(list []string, want string) int {
	for i, item := range list {
		if item == want {
			return i
		}
	}
	return -1
}
