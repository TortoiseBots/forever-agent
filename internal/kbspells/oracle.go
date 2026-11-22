// Package kbspells holds the spells oracle: what each Turtle WoW knowledge-base spec casts every fight (core), what it casts
// in some fights (situational), what it must never cast, and the talents that tell the specs of a class apart. The data is
// generated from the KB rotations (internal/kbspells/oracle) and judged by a pure function of a set of casts, so a trace or a
// fight can be scored after the fact. Credit: Turtle WoW knowledge base (stevemcqueenz),
// https://stevemcqueenz.github.io/turtle-knowledge-base-/#/, plus the sources each KB YAML lists.
package kbspells

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/pjerra/forever-agent/internal/spells"
)

// SpecOracle is one spec of a class: the spell lists the oracle judges.
type SpecOracle struct {
	Spec        string   `json:"spec"`
	Role        string   `json:"role"`
	Rotation    string   `json:"rotation"`
	Core        []string `json:"core"`
	Situational []string `json:"situational"`
	Never       []string `json:"never"`
	Signature   []string `json:"signature"`
	Taunt       []string `json:"taunt"`
}

// Limits are the fight debugger's numbers (TortoiseBots commander-fight-debugger.md §2), so both count the same way.
type Limits struct {
	SunderOKPer10s   float64 `json:"sunder_ok_per_10s"`
	SunderWarnPer10s float64 `json:"sunder_warn_per_10s"`
	MinFightMS       int64   `json:"min_fight_ms"`
}

// Common is the data every class shares: the debugger's taunts, the consumables and item effects a bot uses, the generic
// buffs, forms and utility of each class, and the limits.
type Common struct {
	Taunts  []string            `json:"taunts"`
	Ignore  []string            `json:"ignore"`
	Generic map[string][]string `json:"generic"`
	Limits  Limits              `json:"limits"`
}

// Data is the spell data an oracle is validated against: the names it lists and the character level at which the
// first rank of a spell is learnt. The Turtle table is the default; a Forever build plugs the Forever spell table in,
// so the same judge validates Forever names.
type Data interface {
	HasName(string) bool
	FirstRankLevel(string) (int, bool)
}

// turtleData is the Turtle spell table (internal/spells), the data every Turtle oracle is validated against.
type turtleData struct{}

func (turtleData) HasName(name string) bool { return spells.HasName(name) }

func (turtleData) FirstRankLevel(name string) (int, bool) { return firstRankLevel(name) }

// Oracle is the judge's data: one spec list per class and the common lists.
type Oracle struct {
	Classes map[string][]SpecOracle
	Common  Common
	data    Data // nil is the Turtle table
}

// Load reads an oracle from a file system: `_common.json` and one `<class>.json` per class at the root of fsys. The
// names and levels are validated against the Turtle spell data.
func Load(fsys fs.FS) (*Oracle, error) { return load(fsys, ".", "_common.json", turtleData{}) }

// LoadWith reads an oracle the way Load does and validates it against d instead of the Turtle table (a Forever build
// passes its Forever spell table). A nil d is the Turtle table.
func LoadWith(fsys fs.FS, d Data) (*Oracle, error) {
	if d == nil {
		d = turtleData{}
	}
	return load(fsys, ".", "_common.json", d)
}

//go:embed oracle/*.json
var embedded embed.FS

// Default is the oracle the agent ships: the generated per-class files of internal/kbspells/oracle (written by tools/kbrotgen
// from the KB rotations) and the hand-written common data.
func Default() *Oracle {
	defOnce.Do(func() {
		o, err := load(embedded, "oracle", "oracle/_common.json", turtleData{})
		if err != nil {
			panic("kbspells: embedded oracle: " + err.Error())
		}
		def = o
	})
	return def
}

var (
	defOnce sync.Once
	def     *Oracle
)

type classFile struct {
	Class string       `json:"class"`
	Specs []SpecOracle `json:"specs"`
}

func load(fsys fs.FS, dir, commonPath string, d Data) (*Oracle, error) {
	if d == nil {
		d = turtleData{}
	}
	o := &Oracle{Classes: map[string][]SpecOracle{}, data: d}
	b, err := fs.ReadFile(fsys, commonPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", commonPath, err)
	}
	if err := json.Unmarshal(b, &o.Common); err != nil {
		return nil, fmt.Errorf("%s: %w", commonPath, err)
	}
	if err := validateCommon(o.Common, d); err != nil {
		return nil, fmt.Errorf("%s: %w", commonPath, err)
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || name == path.Base(commonPath) {
			continue
		}
		p := path.Join(dir, name)
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var cf classFile
		if err := json.Unmarshal(b, &cf); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		class := strings.ToLower(strings.TrimSpace(cf.Class))
		if class == "" {
			return nil, fmt.Errorf("%s: no class", p)
		}
		if _, dup := o.Classes[class]; dup {
			return nil, fmt.Errorf("%s: class %s is in two files", p, class)
		}
		slices.SortFunc(cf.Specs, func(a, b SpecOracle) int { return strings.Compare(a.Spec, b.Spec) })
		if err := validateClass(class, cf.Specs, d); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		o.Classes[class] = cf.Specs
	}
	if len(o.Classes) == 0 {
		return nil, fmt.Errorf("%s: no class files", dir)
	}
	return o, nil
}

func validateCommon(c Common, d Data) error {
	if len(c.Taunts) == 0 {
		return fmt.Errorf("no taunts")
	}
	for _, n := range c.Taunts {
		if !d.HasName(n) {
			return fmt.Errorf("taunt %q is not in the spell data", n)
		}
	}
	for class, names := range c.Generic {
		if strings.TrimSpace(class) == "" {
			return fmt.Errorf("generic: a class with no name")
		}
		if err := namesInData(d, "generic "+class, names); err != nil {
			return err
		}
	}
	l := c.Limits
	if l.MinFightMS <= 0 || l.SunderWarnPer10s <= 0 || l.SunderOKPer10s <= l.SunderWarnPer10s {
		return fmt.Errorf("limits %+v: want 0 < warn < ok and a positive min_fight_ms", l)
	}
	return nil
}

func validateClass(class string, specs []SpecOracle, d Data) error {
	if len(specs) == 0 {
		return fmt.Errorf("class %s has no spec", class)
	}
	seen := map[string]bool{}
	sigOf := map[string]string{} // lower-case signature spell -> the spec that claims it
	for _, s := range specs {
		name := strings.TrimSpace(s.Spec)
		if name == "" || seen[name] {
			return fmt.Errorf("spec %q is empty or doubled", s.Spec)
		}
		seen[name] = true
		switch normRole(s.Role) {
		case "tank", "heal", "dps":
		default:
			return fmt.Errorf("spec %s: role %q: use tank, heal or dps", name, s.Role)
		}
		if strings.TrimSpace(s.Rotation) == "" {
			return fmt.Errorf("spec %s: no rotation", name)
		}
		if len(s.Core) == 0 || len(s.Core) > 5 {
			return fmt.Errorf("spec %s: %d core spells, want 1-5", name, len(s.Core))
		}
		if len(s.Signature) > 3 {
			return fmt.Errorf("spec %s: %d signature spells, want at most 3", name, len(s.Signature))
		}
		for _, l := range []struct {
			what  string
			names []string
		}{{"core", s.Core}, {"situational", s.Situational}, {"never", s.Never}, {"signature", s.Signature}, {"taunt", s.Taunt}} {
			if err := namesInData(d, "spec "+name+" "+l.what, l.names); err != nil {
				return err
			}
		}
		if normRole(s.Role) != "tank" && len(s.Taunt) > 0 {
			return fmt.Errorf("spec %s: taunts on a %s spec; only a tank spec has them", name, s.Role)
		}
		for _, n := range s.Signature {
			k := strings.ToLower(n)
			if prev, ok := sigOf[k]; ok && prev != name {
				return fmt.Errorf("%s is a signature of both %s and %s", n, prev, name)
			}
			sigOf[k] = name
		}
	}
	return nil
}

func namesInData(d Data, what string, names []string) error {
	for _, n := range names {
		if strings.TrimSpace(n) == "" {
			return fmt.Errorf("%s: an empty name", what)
		}
		if !d.HasName(n) {
			return fmt.Errorf("%s: %q is not in the spell data", what, n)
		}
	}
	return nil
}

// spec finds one spec of a class.
func (o *Oracle) spec(class, name string) (SpecOracle, bool) {
	for _, s := range o.Classes[normClass(class)] {
		if s.Spec == name {
			return s, true
		}
	}
	return SpecOracle{}, false
}

func normClass(class string) string { return strings.ToLower(strings.TrimSpace(class)) }

// normRole folds the wire's role names (tank, heal, healer, dps) to tank, heal or dps; an empty role is a dps bot.
func normRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "tank":
		return "tank"
	case "heal", "healer":
		return "heal"
	default:
		return "dps"
	}
}
