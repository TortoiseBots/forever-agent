// Package dummybench scores the dummy bench; the format lives in doc.go.
package dummybench

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/spells"
	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// DamageGraceS bounds how long after a cast's go that cast's damage may still
// land (projectile travel and the server's own tick): the scored window ends
// at fight_end, and a damage row up to this many seconds later still counts
// for the last in-window cast. Longer is a later fight, not this one.
const DamageGraceS = 2.0

// gcdFallbackS is 1.12's standard global cooldown, the readiness floor for a
// cast whose own spell metadata is not in the table.
const gcdFallbackS = 1.5

// Expect is one row of the per-class table: the spell, the damage the bench
// expects per cast (floor and half of base points + SP/AP coefficient, per
// the issue) and the reaction ceiling in seconds. Either bound may be
// absent: a missing bound scores UNKNOWN, never FAIL. Numbers marked "to be
// measured" never arrive here: the table keeps them as empty bounds.
type Expect struct {
	// Spell is the spell name as the normaliser writes it (spells.NameFor).
	Spell string `json:"spell"`
	// MinDamage is the least mean damage per cast the bench accepts.
	// Nil: unknown, scores UNKNOWN.
	MinDamage *float64 `json:"min_damage,omitempty"`
	// MaxReactionS is the slowest mean reaction (able-to-act to cast start)
	// the bench accepts. Nil: unknown, scores UNKNOWN.
	MaxReactionS *float64 `json:"max_reaction_s,omitempty"`
}

// TableError is a refused expected table: a bound the bench cannot score (a
// non-finite or negative number, a string, a bool) or a spell named twice.
// It is a type, so a caller can tell a bad table from a missing file.
type TableError struct {
	Path  string // the table's name (file path, or its name in the data dir)
	Row   int    // the expects[] index, -1 when not row-specific
	Field string // the offending field ("min_damage", "spell", ...)
	Value any    // the value read, nil when the field was absent
	Why   string // the refusal in words
}

func (e *TableError) Error() string {
	where := e.Path
	if e.Row >= 0 {
		where += fmt.Sprintf(": expects[%d]", e.Row)
	}
	if e.Field != "" {
		where += ": " + e.Field
	}
	if e.Value != nil {
		return fmt.Sprintf("%s: %s (value %v)", where, e.Why, e.Value)
	}
	return where + ": " + e.Why
}

// LoadExpects reads a table file: {"class", "spec", "expects": [{spell,
// min_damage?, max_reaction_s?}]}. Bounds marked "to be measured" are
// written as JSON null (or left out), never as a guessed number.
func LoadExpects(path string) ([]Expect, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseExpects(path, raw)
}

// ParseExpects decodes one table the loader read (a file, or an embedded
// table named path). A bound must be a finite JSON number >= 0: NaN,
// Infinity, negatives, strings and bools are refused with a *TableError, so
// a hostile table cannot turn `-Infinity min_damage` into an always-PASS or
// a NaN into an always-FAIL. Spell names are trimmed and folded for
// matching; a spell named twice is refused.
func ParseExpects(path string, raw []byte) ([]Expect, error) {
	doc, err := pyjson.Unmarshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	obj, ok := pyjson.Object(doc)
	if !ok {
		return nil, &TableError{Path: path, Row: -1, Why: "table is not an object"}
	}
	list, ok := pyjson.List(obj["expects"])
	if !ok {
		return nil, &TableError{Path: path, Row: -1, Why: "table has no expects list"}
	}
	var out []Expect
	seen := map[string]bool{}
	for i, item := range list {
		row, ok := pyjson.Object(item)
		if !ok {
			return nil, &TableError{Path: path, Row: i, Why: "not an object"}
		}
		raw, isStr := row["spell"].(string)
		name := strings.TrimSpace(raw)
		if !isStr || name == "" {
			return nil, &TableError{Path: path, Row: i, Field: "spell", Why: "names no spell"}
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, &TableError{Path: path, Row: i, Field: "spell", Value: name, Why: "named twice"}
		}
		seen[key] = true
		min, err := tableBound(path, i, row, "min_damage")
		if err != nil {
			return nil, err
		}
		maxRT, err := tableBound(path, i, row, "max_reaction_s")
		if err != nil {
			return nil, err
		}
		out = append(out, Expect{Spell: name, MinDamage: min, MaxReactionS: maxRT})
	}
	return out, nil
}

// tableBound reads one optional numeric bound. Absent or JSON null is nil
// (unknown, scores UNKNOWN).
func tableBound(path string, i int, row map[string]any, field string) (*float64, error) {
	v := row[field]
	if v == nil {
		return nil, nil
	}
	_, isFloat := v.(float64)
	if !pyjson.IsInt(v) && !isFloat {
		return nil, &TableError{Path: path, Row: i, Field: field, Value: v, Why: "not a number"}
	}
	f, ok := pyjson.Num(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, &TableError{Path: path, Row: i, Field: field, Value: v, Why: "not a finite number"}
	}
	if f < 0 {
		return nil, &TableError{Path: path, Row: i, Field: field, Value: v, Why: "negative"}
	}
	return &f, nil
}

// Cast is one scored cast: its spell, the damage it dealt and its reaction.
// T is the cast's start (the spell_start when one preceded the go, else the
// go itself); Go is the spell_go. Reaction is T minus the moment the bot
// could act again, never from the previous cast alone; HasReact is false
// when that moment cannot be bounded (a repeated cast of a spell whose
// cooldown the spell table does not know).
type Cast struct {
	Spell    string
	Damage   int64
	Reaction float64
	HasReact bool
	T        float64
	Go       float64

	meta castMeta
}

// castMeta is the readiness data of one spell, from the spell table: the
// global cooldown, the cast time, the spell's own cooldown and its shared
// category cooldown. known is false for a spell id the meta table lacks.
type castMeta struct {
	gcdS     float64
	castS    float64
	cdS      float64
	category uint32
	catCDS   float64
	known    bool
}

// metaOf reads the readiness data of one cast's spell id.
func metaOf(id int64) castMeta {
	if id <= 0 {
		return castMeta{}
	}
	m, ok := spells.Lookup(uint32(id))
	if !ok {
		return castMeta{}
	}
	return castMeta{gcdS: float64(m.GCDMS) / 1000, castS: float64(m.CastMS) / 1000,
		cdS: float64(m.CooldownMS) / 1000, category: m.Category,
		catCDS: float64(m.CategoryCDMS) / 1000, known: true}
}

// SpellTally is one spell's bench row: casts, damage, reaction.
type SpellTally struct {
	Spell    string
	Casts    int64
	Damage   int64
	MeanDmg  float64
	MeanRT   float64
	MaxRT    float64
	HasReact bool
}

// Verdict is one scored spell row.
type Verdict struct {
	Spell       string
	Verdict     string // PASS, FAIL, UNKNOWN or INVALID
	Casts       int64
	Damage      int64
	MeanDamage  float64
	MeanReact   float64
	MaxReact    float64
	HasReact    bool
	NoNumberFor string // the bound the table did not know, "" when known
	Evidence    []string
}

// window reads the fight_start..fight_end window from marks, in the events'
// own time base (the normaliser re-bases on the fight_start wall, so lo is
// 0 and hi is the end wall minus the start wall). A marks file without
// walls (scripted rows) falls back to its own `t` values. ok is false with
// no window, so a repeat that never fought scores nothing.
func window(marks []events.Row) (lo, hi float64, ok bool) {
	var fs, fe map[string]any
	for _, m := range marks {
		switch pyjson.Str(m["kind"]) {
		case "fight_start":
			if fs == nil {
				fs = m
			}
		case "fight_end":
			fe = m
		}
	}
	if fs == nil || fe == nil {
		return 0, 0, false
	}
	fsWall, fsOK := events.WallSecs(fs["wall"])
	feWall, feOK := events.WallSecs(fe["wall"])
	if fsOK && feOK {
		if feWall <= fsWall {
			return 0, 0, false
		}
		return 0, feWall - fsWall, true
	}
	lo, ok1 := rowT(fs)
	h, ok2 := rowT(fe)
	if !ok1 || !ok2 || h <= lo {
		return 0, 0, false
	}
	return lo, h, true
}

func rowT(row events.Row) (float64, bool) {
	return pyjson.Float(row["t"])
}

// amountOf reads a damage row's amount.
func amountOf(row events.Row) (float64, bool) {
	if n, ok := pyjson.Int64(row["amount"]); ok {
		return float64(n), true
	}
	return pyjson.Float(row["amount"])
}

// spellName is the row's spell name, the spell id as text when it has no
// name; "?" is the normaliser's unknown and is skipped. A row with neither
// has no spell and is skipped (never a spell called "None").
func spellName(row events.Row) string {
	if s := pyjson.Str(row["spell"]); s != "" && s != "?" {
		return s
	}
	if id, ok := pyjson.Int64(row["spell_id"]); ok {
		return strconv.FormatInt(id, 10)
	}
	return ""
}

// spellRows is one spell's raw rows inside the window scan.
type spellRows struct {
	starts []float64
	fails  []float64
	goes   []goRow
	damage []damageRow
}

type goRow struct {
	t  float64
	id int64
}

type damageRow struct {
	t      float64
	amount float64
}

// Tally folds normalised event rows into casts and damage per spell inside
// the fight window [lo, hi]. Rows are the normaliser's: kind
// cast/cast_start/cast_fail/damage, spell, spell_id, amount,
// src_unit/dst_unit. Only the bot's own damage at the dummy counts:
// src_unit is the bot and dst_unit the dummy; heals, self-target rows and a
// pet's rows are not this bench's damage and are dropped.
//
// One cast is one spell_go ("cast") whose go lands in [lo, hi]; a
// cast-time spell's spell_start ("cast_start") moves that go's time
// earlier, and a spell_fail ("cast_fail") after a start drops it (a
// cancelled cast is never paired with the next go). A cast inside the
// window counts with all its damage rows: a damage row belongs to the
// latest in-window go at or before it, and is dropped beyond the
// cast's damage grace (DamageGraceS), so the last cast's projectile still
// counts and a later DoT tick does not. Damage with no in-window cast never
// invents a cast, so a proc or a tick is not a stranger.
//
// The returned tallies hold only spells with an in-window cast: a spell
// with damage but no cast is not a rotation spell.
func Tally(rows []events.Row, botUnit, dummyUnit string, lo, hi float64) ([]SpellTally, []Cast) {
	bySpell := map[string]*spellRows{}
	var order []string
	for _, r := range rows {
		if pyjson.Str(r["src_unit"]) != botUnit || pyjson.Str(r["dst_unit"]) != dummyUnit {
			continue
		}
		spell := spellName(r)
		if spell == "" || spell == "?" {
			continue
		}
		t, ok := rowT(r)
		if !ok {
			continue
		}
		s, seen := bySpell[spell]
		if !seen {
			s = &spellRows{}
			bySpell[spell] = s
			order = append(order, spell)
		}
		switch pyjson.Str(r["kind"]) {
		case "cast_start":
			s.starts = append(s.starts, t)
		case "cast_fail":
			s.fails = append(s.fails, t)
		case "cast":
			id, _ := pyjson.Int64(r["spell_id"])
			s.goes = append(s.goes, goRow{t: t, id: id})
		case "damage":
			if amt, ok := amountOf(r); ok {
				s.damage = append(s.damage, damageRow{t: t, amount: amt})
			}
		}
	}
	var tallies []SpellTally
	var casts []Cast
	for _, spell := range order {
		s := bySpell[spell]
		sort.Float64s(s.starts)
		sort.Float64s(s.fails)
		sort.SliceStable(s.goes, func(i, j int) bool { return s.goes[i].t < s.goes[j].t })
		// Pair each go with its own start: the latest start at or before
		// the go, dropped by a cast_fail between the two. A start belongs
		// to one go.
		si, fi := 0, 0
		var spellCasts []Cast
		for _, g := range s.goes {
			var start *float64
			for si < len(s.starts) && s.starts[si] <= g.t {
				v := s.starts[si]
				start = &v
				si++
			}
			for fi < len(s.fails) && s.fails[fi] <= g.t {
				if start != nil && s.fails[fi] >= *start {
					start = nil
				}
				fi++
			}
			if g.t < lo || g.t > hi {
				continue
			}
			c := Cast{Spell: spell, T: g.t, Go: g.t, meta: metaOf(g.id)}
			if start != nil {
				c.T = *start
			}
			spellCasts = append(spellCasts, c)
		}
		if len(spellCasts) == 0 {
			continue
		}
		// Damage: the latest in-window go at or before the row, within the
		// grace of that cast.
		attr := make([]float64, len(spellCasts))
		for _, d := range s.damage {
			if d.t < lo || d.t > hi+DamageGraceS {
				continue
			}
			idx := -1
			for i := range spellCasts {
				if spellCasts[i].Go <= d.t {
					idx = i
				} else {
					break
				}
			}
			if idx < 0 || d.t > spellCasts[idx].Go+DamageGraceS {
				continue
			}
			attr[idx] += d.amount
		}
		var total int64
		for i := range spellCasts {
			spellCasts[i].Damage = int64(math.Round(attr[i]))
			total += spellCasts[i].Damage
		}
		n := int64(len(spellCasts))
		tallies = append(tallies, SpellTally{Spell: spell, Casts: n, Damage: total,
			MeanDmg: float64(total) / float64(n)})
		casts = append(casts, spellCasts...)
	}
	return tallies, React(casts, lo)
}

// React fills each cast's reaction: the cast's start minus the moment the
// bot could act again. That moment is the latest of:
//   - the fight's engage order (lo): the bot is in range, has the target and
//     is ordered to attack, so it can act from there;
//   - the previous cast's lockout end: its start plus the longer of its
//     global cooldown and its cast time (an instant spell's GCD is thus not
//     counted as reaction, and a 3.5 s cast's GCD is spent inside it);
//   - the cast's own cooldown end: the last start of the same spell plus
//     its cooldown, and the last start of a spell in its category plus the
//     category cooldown.
//
// The previous cast's spell data is used, so the value never counts another
// spell's cooldown as this one's latency. A repeated cast whose own spell id
// is not in the spell table cannot have its cooldown bounded: it keeps no
// reaction (HasReact false; the bench scores its reaction UNKNOWN rather
// than PASS), while a first cast still measures from the engage order. A
// negative gap (a start before the bound, e.g. a client's own queued cast)
// is clamped to 0.
func React(casts []Cast, lo float64) []Cast {
	out := append([]Cast(nil), casts...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	var prevStart *float64
	var prev castMeta
	lastSpell := map[string]float64{}
	lastCat := map[uint32]float64{}
	for i := range out {
		ready := lo
		if prevStart != nil {
			lock := gcdFallbackS
			if prev.known {
				lock = math.Max(prev.gcdS, prev.castS)
			}
			if r := *prevStart + lock; r > ready {
				ready = r
			}
		}
		m := out[i].meta
		_, repeat := lastSpell[out[i].Spell]
		if m.known {
			if repeat {
				if r := lastSpell[out[i].Spell] + m.cdS; r > ready {
					ready = r
				}
			}
			if m.category != 0 && m.catCDS > 0 {
				if s, ok := lastCat[m.category]; ok {
					if r := s + m.catCDS; r > ready {
						ready = r
					}
				}
			}
			lastCat[m.category] = out[i].T
		}
		lastSpell[out[i].Spell] = out[i].T
		if !m.known {
			// The spell's own cooldown is unknown, so the gap to any of
			// its starts may be cooldown, not reaction: no reaction is
			// claimed (the bench scores its reaction UNKNOWN, never PASS).
			out[i].HasReact = false
		} else {
			out[i].Reaction = out[i].T - ready
			if out[i].Reaction < 0 {
				out[i].Reaction = 0
			}
			out[i].HasReact = true
		}
		start := out[i].T
		prevStart, prev = &start, m
	}
	return out
}

// FoldReactions rolls per-cast reactions back into the tallies (mean, max).
func FoldReactions(tallies []SpellTally, casts []Cast) []SpellTally {
	bySpell := map[string]*SpellTally{}
	for i := range tallies {
		bySpell[tallies[i].Spell] = &tallies[i]
	}
	sum := map[string]float64{}
	max := map[string]float64{}
	n := map[string]int64{}
	for _, c := range casts {
		if !c.HasReact {
			continue
		}
		sum[c.Spell] += c.Reaction
		if c.Reaction > max[c.Spell] || n[c.Spell] == 0 {
			max[c.Spell] = c.Reaction
		}
		n[c.Spell]++
	}
	for spell, s := range bySpell {
		if n[spell] == 0 {
			continue
		}
		s.HasReact = true
		s.MeanRT = sum[spell] / float64(n[spell])
		s.MaxRT = max[spell]
	}
	return tallies
}

// Score compares one spell's tally with its expected row. The table
// decides: a mean damage below MinDamage is FAIL, a mean reaction above
// MaxReactionS is FAIL, both met is PASS. Unknown stays unknown: a missing
// bound, or a known reaction bound with no measured reaction, is UNKNOWN,
// never FAIL and never PASS. A cast spell the table does not name (a
// stranger: a proc, a wrong rotation, another bot's cast) is INVALID, never
// a silent PASS.
func Score(t SpellTally, e *Expect) Verdict {
	v := Verdict{Spell: t.Spell, Casts: t.Casts, Damage: t.Damage,
		MeanDamage: t.MeanDmg, MeanReact: t.MeanRT, MaxReact: t.MaxRT, HasReact: t.HasReact}
	if e == nil {
		v.Verdict = "INVALID"
		v.Evidence = []string{fmt.Sprintf("%s: not in the expected table (stranger)", t.Spell)}
		return v
	}
	if t.Casts == 0 {
		v.Verdict = "UNKNOWN"
		v.NoNumberFor = "casts"
		v.Evidence = []string{fmt.Sprintf("%s: no casts in the scored window", t.Spell)}
		return v
	}
	var missing []string
	if e.MinDamage == nil {
		missing = append(missing, "min_damage")
	}
	if e.MaxReactionS == nil {
		missing = append(missing, "max_reaction_s")
	}
	if len(missing) > 0 {
		v.Verdict = "UNKNOWN"
		v.NoNumberFor = strings.Join(missing, ",")
		v.Evidence = []string{fmt.Sprintf("%s: casts=%d damage=%d mean=%.1f, no expected %s",
			t.Spell, t.Casts, t.Damage, t.MeanDmg, v.NoNumberFor)}
		if t.HasReact {
			v.Evidence = append(v.Evidence, fmt.Sprintf("%s: mean reaction %.2fs max %.2fs",
				t.Spell, t.MeanRT, t.MaxRT))
		}
		return v
	}
	if !t.HasReact {
		v.Verdict = "UNKNOWN"
		v.NoNumberFor = "reaction"
		v.Evidence = []string{fmt.Sprintf("%s: no reaction measured in the scored window (max %.2fs known)",
			t.Spell, *e.MaxReactionS)}
		return v
	}
	dmgOK := t.MeanDmg >= *e.MinDamage
	rtOK := t.MeanRT <= *e.MaxReactionS
	if dmgOK && rtOK {
		v.Verdict = "PASS"
	} else {
		v.Verdict = "FAIL"
	}
	v.Evidence = []string{fmt.Sprintf("%s: casts=%d damage=%d mean=%.1f (min %.1f), mean reaction %s (max %s)",
		t.Spell, t.Casts, t.Damage, t.MeanDmg, *e.MinDamage,
		reactS(t), reactMaxS(t, *e.MaxReactionS))}
	return v
}

func reactS(t SpellTally) string {
	if !t.HasReact {
		return "n/a"
	}
	return fmt.Sprintf("%.2fs", t.MeanRT)
}

func reactMaxS(t SpellTally, max float64) string {
	if !t.HasReact {
		return "n/a"
	}
	return fmt.Sprintf("%.2fs", max)
}

// RepTally is one dummy repeat's scored content: per-spell tallies plus
// the expects table keyed by spell. Every caller shares it: ScoreRep (the
// verdicts.json path) and the score package's dummy rule (the trial
// verdicts path). Invalid names why a repeat as a whole is INVALID: a
// stranger or a guard reason voids every spell of the repeat, so no spell
// in it scores PASS or FAIL.
type RepTally struct {
	Tallies []SpellTally
	Casts   []Cast
	Expects map[string]*Expect
	Invalid string
}

// Expect is the table row naming spell (trimmed, case-folded), nil when
// the table does not name it.
func (r RepTally) Expect(spell string) *Expect {
	return r.Expects[strings.ToLower(strings.TrimSpace(spell))]
}

// ScoreRep scores one dummy repeat dir: reads setup.json (which names the
// bot and the trial's class/spec), marks.jsonl (the window), events.jsonl
// (the normaliser's output) and the expects table, and writes
// verdicts.json. It returns the per-spell verdicts.
//
// A failed setup or a missing fight window is INVALID, never PASS: there is
// no damage to score. A stranger makes the whole repeat INVALID: every
// verdict row of that repeat is INVALID, so a card never shows a PASS
// beside the stranger that voids it. A table spell with no casts is
// UNKNOWN; a spell with damage but no cast is not a rotation spell and is
// left out.
func TallyRep(repDir string, expects []Expect) (RepTally, []Verdict, error) {
	byName := map[string]*Expect{}
	for i := range expects {
		byName[strings.ToLower(expects[i].Spell)] = &expects[i]
	}
	tallied := func(tallies []SpellTally, casts []Cast) (RepTally, []Verdict, error) {
		rep := RepTally{Tallies: tallies, Casts: casts, Expects: byName}
		var out []Verdict
		for _, t := range tallies {
			out = append(out, Score(t, byName[strings.ToLower(t.Spell)]))
		}
		seen := map[string]bool{}
		for _, t := range tallies {
			seen[strings.ToLower(t.Spell)] = true
		}
		for _, e := range expects {
			if !seen[strings.ToLower(e.Spell)] {
				out = append(out, Verdict{Spell: e.Spell, Verdict: "UNKNOWN",
					NoNumberFor: "casts",
					Evidence:    []string{fmt.Sprintf("%s: no casts in the scored window", e.Spell)}})
			}
		}
		for _, v := range out {
			if v.Verdict == "INVALID" {
				rep.Invalid = v.Evidence[0]
				break
			}
		}
		if rep.Invalid != "" {
			// The stranger (or whatever voided the repeat) voids every
			// row: no spell in an INVALID repeat scores PASS or FAIL.
			for i := range out {
				out[i].Verdict = "INVALID"
				out[i].NoNumberFor = ""
				out[i].Evidence = []string{rep.Invalid}
			}
		}
		return rep, out, nil
	}
	setupRaw, err := os.ReadFile(filepath.Join(repDir, "setup.json"))
	if err != nil {
		return RepTally{}, nil, err
	}
	setupDoc, err := pyjson.Unmarshal(setupRaw)
	if err != nil {
		return RepTally{}, nil, err
	}
	setup := pyjson.ObjectOrEmpty(setupDoc)
	if !pyjson.Truthy(setup["ok"]) {
		why := pyjson.Str(setup["invalid_reason"])
		if why == "" {
			why = "setup not proven"
		}
		out := []Verdict{{Spell: "setup", Verdict: "INVALID", Evidence: []string{why}}}
		return RepTally{Expects: byName, Invalid: why}, out, nil
	}
	marksRows, err := events.ReadJSONL(filepath.Join(repDir, "marks.jsonl"))
	if err != nil {
		return RepTally{}, nil, err
	}
	lo, hi, ok := window(marksRows)
	if !ok {
		out := []Verdict{{Spell: "fight", Verdict: "INVALID",
			Evidence: []string{"no fight_start..fight_end window: no scored fight happened"}}}
		return RepTally{Expects: byName, Invalid: "no scored fight"}, out, nil
	}
	eventRows, err := events.ReadJSONL(filepath.Join(repDir, "events.jsonl"))
	if err != nil {
		return RepTally{}, nil, err
	}
	// The fight is the bot against the one summoned target: the
	// normaliser's $bot against the setup's dummy alias.
	tallies, casts := Tally(eventRows, "$bot", dummyAlias(setup), lo, hi)
	tallies = FoldReactions(tallies, casts)
	return tallied(tallies, casts)
}

func ScoreRep(repDir string, expects []Expect) ([]Verdict, error) {
	tally, out, err := TallyRep(repDir, expects)
	if err != nil {
		return nil, err
	}
	if err := dumpVerdicts(repDir, out, tally.Invalid); err != nil {
		return nil, err
	}
	return out, nil
}

// dummyAlias is the normaliser's unit word for the fight's target: the one
// mob the repeat summoned ($mob when unnamed, "mob:<guid>" otherwise). The
// dummy bench summons exactly one target, so the first mob guid is the
// dummy; an empty mob table is INVALID at the caller, never guessed here.
func dummyAlias(setup map[string]any) string {
	guids := pyjson.ObjectOrEmpty(setup["guids"])
	mobs, _ := pyjson.List(guids["mobs"])
	if len(mobs) == 0 {
		return "$mob"
	}
	first := events.NormGUID(mobs[0])
	if first == "" {
		return "$mob"
	}
	marked := events.NormGUID(guids["marked"])
	if marked != "" && marked == first {
		return "$marked"
	}
	// The normaliser names an unmarked mob "mob:<guid>".
	return "mob:" + first
}

// dumpVerdicts writes verdicts.json in the score package's row shape, so
// ScoreTrial and the card read dummy trials unchanged. An INVALID row
// carries the reason; an UNKNOWN row carries it too (a stranger never
// becomes a PASS downstream: verdicts stay INVALID/UNKNOWN, never PASS).
func dumpVerdicts(repDir string, verdicts []Verdict, invalid string) error {
	rules := make([]any, 0, len(verdicts))
	for _, v := range verdicts {
		ev := make([]any, 0, len(v.Evidence))
		for _, e := range v.Evidence {
			ev = append(ev, e)
		}
		row := map[string]any{"id": "DUMMY-" + upperSpell(v.Spell), "spell": v.Spell,
			"verdict": v.Verdict, "casts": v.Casts, "damage": v.Damage,
			"mean_damage":     Round1(v.MeanDamage),
			"mean_reaction_s": reactOrNil(v), "max_reaction_s": maxReactOrNil(v),
			"value": valueOf(v), "k": v.Casts, "n": v.Casts,
			"evidence": ev, "guard": ""}
		if v.NoNumberFor != "" {
			row["no_number_for"] = v.NoNumberFor
		}
		if v.Verdict == "INVALID" || v.Verdict == "UNKNOWN" {
			row["reason"] = firstEv(v)
		}
		rules = append(rules, row)
	}
	doc := map[string]any{"trial": "dummy", "rules": rules}
	if invalid != "" {
		doc["invalid_reason"] = invalid
	}
	return pyjson.Dump(filepath.Join(repDir, "verdicts.json"), doc)
}

func firstEv(v Verdict) string {
	if len(v.Evidence) > 0 {
		return v.Evidence[0]
	}
	return v.Verdict
}

func valueOf(v Verdict) any {
	if v.Verdict == "PASS" || v.Verdict == "FAIL" {
		return Round1(v.MeanDamage)
	}
	return nil
}

func reactOrNil(v Verdict) any {
	if !v.HasReact {
		return nil
	}
	return Round2(v.MeanReact)
}

func maxReactOrNil(v Verdict) any {
	if !v.HasReact {
		return nil
	}
	return Round2(v.MaxReact)
}

// Round1 rounds to one decimal (damage); Round2 to two (seconds).
func Round1(f float64) float64 { return math.Round(f*10) / 10 }

// Round2 rounds to two decimals (seconds).
func Round2(f float64) float64 { return math.Round(f*100) / 100 }

func upperSpell(spell string) string {
	return strings.ToUpper(strings.ReplaceAll(spell, " ", "_"))
}
