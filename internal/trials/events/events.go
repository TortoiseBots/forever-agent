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
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// NormGUID normalises a guid to Python's "0x%016x" form (lower case); unparseable text passes
// through lower-cased.
func NormGUID(value any) string {
	if !pyjson.Truthy(value) {
		return ""
	}
	text := strings.ToLower(strings.TrimSpace(pyjson.Str(value)))
	if !strings.HasPrefix(text, "0x") {
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return text
		}
		return fmt.Sprintf("0x%016x", n)
	}
	n, err := strconv.ParseInt(text, 0, 64)
	if err != nil {
		return text
	}
	return fmt.Sprintf("0x%016x", n)
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

// ReadJSONL reads a JSON-lines file; a missing file gives no rows and a malformed line is
// skipped, like the bench. Non-object lines are skipped too: every reader wants objects.
func ReadJSONL(path string) []Row {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var rows []Row
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		doc, err := pyjson.Unmarshal([]byte(line))
		if err != nil {
			continue
		}
		if row, ok := pyjson.Object(doc); ok {
			rows = append(rows, row)
		}
	}
	return rows
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
func AttackVictims(raws, marks, samples []Row) map[string][]AttackVictim {
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
			if n, ok := pyjson.Int64(raw["seq"]); ok {
				seq, hasSeq = n, true
			} else if f, ok := pyjson.Float(raw["seq"]); ok {
				seq, hasSeq = int64(f), true
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
	return byGUID
}

// Normalise writes events.jsonl in repDir and returns the rows list.
//
// One time base: samples AND raws are re-based from their ISO `wall` onto the fight_start
// wall (or setup_ok when no pull). Events and the excluded rows carry re-based seconds;
// score/rules read ONLY this output (plus the untouched samples), never the runner's raw
// marks.
func Normalise(repDir string, trial map[string]any) ([]Row, error) {
	raw := ReadJSONL(filepath.Join(repDir, "raw-ta.jsonl"))
	samples := ReadJSONL(filepath.Join(repDir, "samples.jsonl"))
	marks := ReadJSONL(filepath.Join(repDir, "marks.jsonl"))
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
	for _, row := range raw {
		seqVal := row["seq"]
		content, err := pyjson.MarshalLine(row)
		if err != nil {
			return nil, err
		}
		key := "null\x00" + string(content)
		if seqVal != nil {
			key = "seq:" + pyjson.Str(seqVal) + "\x00" + string(content)
		}
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
			if n, ok := pyjson.Int64(row["seq"]); ok {
				seq = n
			} else if f, ok := pyjson.Float(row["seq"]); ok {
				seq = int64(f)
			}
		}
		var spellID any
		if row["spell_id"] != nil {
			if n, ok := pyjson.Int64(row["spell_id"]); ok {
				spellID = n
			} else if f, ok := pyjson.Float(row["spell_id"]); ok {
				spellID = int64(f)
			}
		}
		var spellName any
		if id, ok := spellID.(int64); ok {
			spellName = spells.NameFor(id)
		}
		out = append(out, Row{
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
		})
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
