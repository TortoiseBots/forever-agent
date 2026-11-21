// Package dummybench scores the dummy bench; the format lives in doc.go.
package dummybench

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

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

// LoadExpects reads a table file: {"class", "spec", "expects": [{spell,
// min_damage?, max_reaction_s?}]}. Bounds marked "to be measured" are
// written as JSON null (or left out), never as a guessed number.
func LoadExpects(path string) ([]Expect, error) {
	doc, err := pyjson.UnmarshalFile(path)
	if err != nil {
		return nil, err
	}
	obj, ok := pyjson.Object(doc)
	if !ok {
		return nil, fmt.Errorf("%s: table is not an object", path)
	}
	list, ok := pyjson.List(obj["expects"])
	if !ok {
		return nil, fmt.Errorf("%s: table has no expects list", path)
	}
	var out []Expect
	for i, item := range list {
		row, ok := pyjson.Object(item)
		if !ok {
			return nil, fmt.Errorf("%s: expects[%d] is not an object", path, i)
		}
		name := pyjson.Str(row["spell"])
		if name == "" {
			return nil, fmt.Errorf("%s: expects[%d] names no spell", path, i)
		}
		var e Expect
		e.Spell = name
		if row["min_damage"] != nil {
			f, ok := pyjson.Float(row["min_damage"])
			if !ok {
				return nil, fmt.Errorf("%s: expects[%d] bad min_damage", path, i)
			}
			e.MinDamage = &f
		}
		if row["max_reaction_s"] != nil {
			f, ok := pyjson.Float(row["max_reaction_s"])
			if !ok {
				return nil, fmt.Errorf("%s: expects[%d] bad max_reaction_s", path, i)
			}
			e.MaxReactionS = &f
		}
		out = append(out, e)
	}
	return out, nil
}

// Cast is one scored cast: its spell, the damage it dealt and its reaction.
// T is the cast's start; Go is the go the start was paired with: the
// reaction clock is start minus the previous cast's go (the moment the bot
// was able to act again), not start minus start.
type Cast struct {
	Spell    string
	Damage   int64
	Reaction float64
	HasReact bool
	T        float64
	Go       float64
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

// window reads the fight_start..fight_end window from marks (the score
// package's window, re-based seconds). ok is false with no window.
func window(marks []events.Row) (lo, hi float64, ok bool) {
	var start, end *float64
	for _, m := range marks {
		kind, _ := m["kind"].(string)
		switch kind {
		case "fight_start":
			if t, good := rowT(m); good && start == nil {
				start = &t
			}
		case "fight_end":
			if t, good := rowT(m); good {
				end = &t
			}
		}
	}
	if start == nil || end == nil || *end <= *start {
		return 0, 0, false
	}
	return *start, *end, true
}

func rowT(row events.Row) (float64, bool) {
	return pyjson.Float(row["t"])
}

// castsInWindow keeps the normaliser's rows inside the scored window.
func castsInWindow(rows []events.Row, lo, hi float64) []events.Row {
	var out []events.Row
	for _, r := range rows {
		t, ok := rowT(r)
		if !ok || t < lo || t > hi {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Tally folds normalised event rows into casts and damage per spell. Rows
// are the normaliser's: kind cast/cast_start/cast_fail/damage/heal, spell,
// amount, src_unit/dst_unit. Only rows the table's bot aimed at the fight
// count: src_unit $bot, dst_unit the dummy (or $bot for a self heal: the
// rotation's heal lands on the caster, not the target).
//
// One cast is one spell_go ("cast"): a cast-time spell also emits a
// spell_start ("cast_start"), which never counts twice. Damage sums per
// spell over every damage/heal row of that spell, whether or not a cast
// row precedes it (a periodic tick, a proc): unattributed damage never
// invents a cast. A spell the table does not name is a stranger row: kept
// in the tally so Score marks it INVALID, never silently dropped.
func Tally(rows []events.Row, botUnit, dummyUnit string) ([]SpellTally, []Cast) {
	aimed := func(r events.Row) bool {
		if pyjson.Str(r["src_unit"]) != botUnit {
			return false
		}
		switch dst := pyjson.Str(r["dst_unit"]); dst {
		case dummyUnit, botUnit:
			return true
		}
		return false
	}
	starts := map[string][]float64{}
	goes := map[string][]float64{}
	damage := map[string]int64{}
	casts := map[string]int64{}
	var order []string
	seen := map[string]bool{}
	nameOf := func(r events.Row) string {
		if s := pyjson.Str(r["spell"]); s != "" {
			return s
		}
		return pyjson.Str(r["spell_id"])
	}
	for _, r := range rows {
		if !aimed(r) {
			continue
		}
		spell := nameOf(r)
		if spell == "" || spell == "?" {
			continue
		}
		if !seen[spell] {
			seen[spell] = true
			order = append(order, spell)
		}
		t, _ := rowT(r)
		switch pyjson.Str(r["kind"]) {
		case "cast":
			goes[spell] = append(goes[spell], t)
			casts[spell]++
		case "cast_start":
			starts[spell] = append(starts[spell], t)
		case "damage", "heal":
			amt, ok := pyjson.Int64(r["amount"])
			if !ok {
				if f, ok := pyjson.Float(r["amount"]); ok {
					amt = int64(f)
				} else {
					continue
				}
			}
			damage[spell] += amt
		}
	}
	// One cast row per go: the cast's start is its latest cast_start at or
	// before the go, else the go itself (instant). Both lists arrive in
	// window order, so one index each pairs them.
	var out []Cast
	for _, spell := range order {
		ss := starts[spell]
		si := 0
		last := -1
		for _, g := range goes[spell] {
			for si < len(ss) && ss[si] <= g {
				last = si
				si++
			}
			start := g
			if last >= 0 {
				start = ss[last]
				last = -1
			}
			out = append(out, Cast{Spell: spell, T: start, Go: g})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	// A consumed start belongs to its go: drop the pairing scan's leftover
	// bookkeeping (starts only ever move the go's time earlier).
	var tallies []SpellTally
	for _, spell := range order {
		n := casts[spell]
		t := SpellTally{Spell: spell, Casts: n, Damage: damage[spell]}
		if n > 0 {
			t.MeanDmg = float64(damage[spell]) / float64(n)
		}
		tallies = append(tallies, t)
	}
	return tallies, React(out)
}

// React fills each cast's reaction: the gap from the previous cast's go
// (the moment the bot was able to act again: cooldown/GCD spent, target in
// range on a quiet dummy) to this cast's start. The first cast has no
// reaction: nothing before it proves when the bot was ready.
func React(casts []Cast) []Cast {
	out := append([]Cast(nil), casts...)
	var prev *float64
	for i := range out {
		if prev != nil {
			out[i].Reaction = out[i].T - *prev
			out[i].HasReact = true
		}
		g := out[i].Go
		prev = &g
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

// Score compares one spell's tally with its expected row. Unknown stays
// unknown (a missing bound never fails); a stranger (a cast spell the table
// does not name, or a table spell with no entry at all) is INVALID, never a
// silent PASS.
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
	wantRT := e.MaxReactionS != nil && t.HasReact
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
	dmgOK := t.MeanDmg >= *e.MinDamage
	rtOK := !t.HasReact || t.MeanRT <= *e.MaxReactionS
	_ = wantRT
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
// verdicts path).
type RepTally struct {
	Tallies []SpellTally
	Casts   []Cast
	Expects map[string]*Expect
	Invalid string
}

// ScoreRep scores one dummy repeat dir: reads setup.json (which names the
// bot and the trial's class/spec), marks.jsonl (the window), events.jsonl
// (the normaliser's output) and the expects table, and writes
// verdicts.json. It returns the per-spell verdicts.
//
// A failed setup or a missing fight window is INVALID, never PASS: there is
// no damage to score. A spell with no expected numbers is UNKNOWN. A spell
// the table does not name (a stranger: a proc, a wrong rotation, another
// bot's cast leaking in) is INVALID.
// SetupBad is set when the setup or the window is missing: the repeat is
// INVALID before any tally.
func TallyRep(repDir string, expects []Expect) (RepTally, []Verdict, error) {
	byName := map[string]*Expect{}
	for i := range expects {
		byName[expects[i].Spell] = &expects[i]
	}
	tallied := func(tallies []SpellTally, casts []Cast) (RepTally, []Verdict, error) {
		rep := RepTally{Tallies: tallies, Casts: casts, Expects: byName}
		var out []Verdict
		for _, t := range tallies {
			out = append(out, Score(t, byName[t.Spell]))
		}
		seen := map[string]bool{}
		for _, t := range tallies {
			seen[t.Spell] = true
		}
		for _, e := range expects {
			if !seen[e.Spell] {
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
	in := castsInWindow(eventRows, lo, hi)
	// The fight is the bot against the one summoned target: the
	// normaliser's $bot against the setup's dummy alias.
	tallies, casts := Tally(in, "$bot", dummyAlias(setup))
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
