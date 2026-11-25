// Package aotc reads data/aotc, the aggregate WoW Forever player statistics the bench scores bots
// against (aotc.gg, read 2026-11-24). Every file is an aggregate: no player names, no guilds.
//
// The numbers are player CHOICES, not game facts: for a spell, talent or item fact the server's own
// data wins when the two disagree (see README.md in this directory). The tables are embedded, so a
// binary that quotes them needs no path, and Load refuses a malformed file with the file and line,
// never a silently short or empty table.
//
// Credit: the data is from aotc.gg (https://www.aotc.gg/forever); every output that quotes it carries
// Credit.
package aotc

import (
	"embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Source and Credit name where the tables come from. Credit is the line every output that quotes
// the tables carries.
const (
	Source    = "aotc.gg"
	SourceURL = "https://www.aotc.gg/forever"
	ReadOn    = "2026-11-24"

	Credit = "aggregate WoW Forever player statistics from aotc.gg (" + SourceURL + ", read " + ReadOn +
		"); player choices, not game facts: the server's own data wins"
)

// The level groups talent-picks.csv and talent-tree-points.csv carry, and the two gear populations.
const (
	LevelMax = "60"

	PopulationBestGeared = "best_geared"
	PopulationAll        = "all_60"
)

var (
	levelGroups = []string{"10-19", "20-29", "30-39", "40-49", "50-59", "60", "all"}
	populations = []string{PopulationBestGeared, PopulationAll}
)

// TalentPick is one row of talent-picks.csv: the share of a spec's characters at a level group that
// took a talent.
type TalentPick struct {
	Class, Spec, Levels string
	Tab                 int // the tree's page, 0-2
	Tree                string
	Row, Col            int
	Talent              string
	SpellID             int64
	MaxRank             int64 // 0 when the file gives none (a talent with no ranks)
	PickPct             float64
	Chars               int64
	EligibleChars       int64
	UsualRank           int64
	// The Forever traits the site maps the talent to; 0 when it maps none.
	OurTraitNode int64
	OurTalentID  int64
	OurMaxRank   int64
}

// TreePoint is one row of talent-tree-points.csv: the average points a spec at a level group puts in
// one tree.
type TreePoint struct {
	Class, Spec, Levels string
	Tab                 int
	Tree                string
	AvgPoints           float64
	Chars               int64
}

// TalentSplit is one row of talent-splits-60.csv: one of a spec's 20 most common point splits at 60.
type TalentSplit struct {
	Class, Spec string
	Split       string // "12/8/31"
	Pct         float64
	Chars       int64
}

// StatWeight is one row of stat-priority-60.csv: a stat's place in a spec's priority at 60. Rows
// with the same Order and EqualToPrevious are about equal.
type StatWeight struct {
	Class, Spec     string
	Order           int
	Stat            string
	Weight          float64
	EqualToPrevious bool
}

// GearItem is one row of gear-60.csv: an item a spec wears in a slot at 60, or (a Slot beginning
// with "Set: ") an item set's use.
type GearItem struct {
	Class, Spec, Population string
	Slot, Name              string
	ID                      int64 // wowhead id; 0 for "No off hand"/a set row
	Pct                     float64
	Chars, OfChars          int64
}

// SetSlot reports whether the row is an item-set row, not a slot's item.
func (g GearItem) SetSlot() bool { return strings.HasPrefix(g.Slot, "Set: ") }

// Enchant is one row of enchants-60.csv: an enchant a spec uses in a slot at 60.
type Enchant struct {
	Class, Spec, Population string
	Slot, Name              string
	ID                      int64
	Pct                     float64
	Chars, OfChars          int64
}

// SpecShare is one row of spec-population.csv: a spec's share of its class's level 60 characters.
type SpecShare struct {
	Class, Spec  string
	PctOfClass60 float64
	ClassChars60 int64
}

// ClassCensus is one row of census-classes.csv.
type ClassCensus struct {
	Class                  string
	Chars, Alliance, Horde int64
	Chars60                int64
	PctOfClassAt60         float64
}

// RaceCensus is one row of census-races.csv.
type RaceCensus struct {
	Race, Faction string
	PctOfFaction  float64
	FemalePct     float64
}

// ClassGender is one row of census-class-gender.csv.
type ClassGender struct {
	Class     string
	FemalePct float64
}

// RaceClassPair is one row of census-race-class-pairs.csv. Chars is the count the file gives,
// already scaled ("464k" is 464000).
type RaceClassPair struct {
	List     string
	Rank     int
	Pair     string
	PctOfAll float64
	Chars    int64
}

// LevelCensus is one row of census-levels.csv.
type LevelCensus struct {
	Level int
	Chars int64
}

// BehaviourStat is one row of behaviour-stats.csv. HasCount is false for the rows the file gives
// no count for (a share only).
type BehaviourStat struct {
	What, Name string
	Pct        float64
	Count      int64
	HasCount   bool
}

// BossKill is one row of new-forever-bosses.csv.
type BossKill struct {
	Boss, Instance string
	Pct            float64
}

// Achievement is one row of legacy-achievements.csv.
type Achievement struct {
	Group, Achievement, Criteria string
}

// ItemWearer is one spec that wears an item in a slot, with the share of its characters.
type ItemWearer struct {
	Class, Spec, Slot, Population string
	Pct                           float64
}

// ItemInfo is what the gear tables say about one item id: its name, the slots it is listed in and
// the specs that wear it.
type ItemInfo struct {
	ID      int64
	Name    string
	Slots   []string
	Wearers []ItemWearer
}

// Data is every table of data/aotc, plus the per-spec and per-item indexes the accessors read.
type Data struct {
	TalentPicks    []TalentPick
	TreePoints     []TreePoint
	Splits         []TalentSplit
	StatPriority   []StatWeight
	Gear           []GearItem
	Enchants       []Enchant
	Specs          []SpecShare
	Classes        []ClassCensus
	Races          []RaceCensus
	ClassGender    []ClassGender
	RaceClassPairs []RaceClassPair
	Levels         []LevelCensus
	Behaviour      []BehaviourStat
	Bosses         []BossKill
	Achievements   []Achievement

	items map[int64]*ItemInfo
}

type specKey struct{ Class, Spec string }

//go:embed *.csv
var embedded embed.FS

// Default returns the tables embedded in this build. The embedded files are checked by the package's
// tests; a build whose files are malformed panics here rather than score a bot against a half table.
func Default() *Data {
	d, err := Load(embedded)
	if err != nil {
		panic("aotc: " + err.Error())
	}
	return d
}

// Load reads every table from fsys, whose root holds the CSV files with the names Load lists. It
// returns the first malformed file as `file.csv: line N: what`, and cross-checks that every class and
// spec the files name has its talent, stat and gear rows.
func Load(fsys fs.FS) (*Data, error) {
	d := &Data{items: map[int64]*ItemInfo{}}
	if err := parse(fsys, "talent-picks.csv",
		[]string{"class", "spec", "levels", "tab", "tree", "row", "col", "talent", "spell_id", "max_rank", "pick_pct",
			"chars", "eligible_chars", "usual_rank", "our_trait_node", "our_talent_id", "our_max_rank"},
		func(line int, f []string) error {
			var t TalentPick
			t.Class, t.Spec = f[0], f[1]
			t.Levels = f[2]
			var err error
			if t.Tab, err = intOf(f[3], "tab"); err != nil {
				return err
			}
			t.Tree = f[4]
			if t.Row, err = intOf(f[5], "row"); err != nil {
				return err
			}
			if t.Col, err = intOf(f[6], "col"); err != nil {
				return err
			}
			t.Talent = f[7]
			if t.SpellID, err = int64Of(f[8], "spell_id"); err != nil {
				return err
			}
			if t.MaxRank, err = optInt64Of(f[9], "max_rank"); err != nil {
				return err
			}
			if t.PickPct, err = pctOf(f[10], "pick_pct"); err != nil {
				return err
			}
			if t.Chars, err = int64Of(f[11], "chars"); err != nil {
				return err
			}
			if t.EligibleChars, err = int64Of(f[12], "eligible_chars"); err != nil {
				return err
			}
			if t.UsualRank, err = int64Of(f[13], "usual_rank"); err != nil {
				return err
			}
			if t.OurTraitNode, err = optInt64Of(f[14], "our_trait_node"); err != nil {
				return err
			}
			if t.OurTalentID, err = optInt64Of(f[15], "our_talent_id"); err != nil {
				return err
			}
			if t.OurMaxRank, err = optInt64Of(f[16], "our_max_rank"); err != nil {
				return err
			}
			if !slices.Contains(levelGroups, t.Levels) {
				return fmt.Errorf("levels %q is not one of %v", t.Levels, levelGroups)
			}
			t.Talent = strings.TrimSpace(t.Talent)
			if t.Talent == "" {
				return errors.New("empty talent")
			}
			d.TalentPicks = append(d.TalentPicks, t)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "talent-tree-points.csv",
		[]string{"class", "spec", "levels", "tab", "tree", "avg_points", "chars"},
		func(line int, f []string) error {
			var p TreePoint
			p.Class, p.Spec, p.Levels = f[0], f[1], f[2]
			var err error
			if p.Tab, err = intOf(f[3], "tab"); err != nil {
				return err
			}
			p.Tree = f[4]
			if p.AvgPoints, err = numOf(f[5], "avg_points"); err != nil {
				return err
			}
			if p.Chars, err = int64Of(f[6], "chars"); err != nil {
				return err
			}
			if !slices.Contains(levelGroups, p.Levels) {
				return fmt.Errorf("levels %q is not one of %v", p.Levels, levelGroups)
			}
			d.TreePoints = append(d.TreePoints, p)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "talent-splits-60.csv",
		[]string{"class", "spec", "split", "pct", "chars"},
		func(line int, f []string) error {
			var s TalentSplit
			s.Class, s.Spec, s.Split = f[0], f[1], f[2]
			var err error
			if s.Pct, err = pctOf(f[3], "pct"); err != nil {
				return err
			}
			if s.Chars, err = int64Of(f[4], "chars"); err != nil {
				return err
			}
			if !splitShape(s.Split) {
				return fmt.Errorf("split %q is not N/N/N", s.Split)
			}
			d.Splits = append(d.Splits, s)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "stat-priority-60.csv",
		[]string{"class", "spec", "order", "stat", "weight", "about_equal_to_previous"},
		func(line int, f []string) error {
			var w StatWeight
			w.Class, w.Spec = f[0], f[1]
			var err error
			if w.Order, err = intOf(f[2], "order"); err != nil {
				return err
			}
			w.Stat = strings.TrimSpace(f[3])
			if w.Stat == "" {
				return errors.New("empty stat")
			}
			if w.Weight, err = numOf(f[4], "weight"); err != nil {
				return err
			}
			switch f[5] {
			case "":
			case "yes":
				w.EqualToPrevious = true
			default:
				return fmt.Errorf("about_equal_to_previous %q is neither empty nor yes", f[5])
			}
			d.StatPriority = append(d.StatPriority, w)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "gear-60.csv",
		[]string{"class", "spec", "population", "slot", "name", "wowhead_id", "pct", "chars", "of_chars"},
		func(line int, f []string) error {
			g, err := gearRow(f)
			if err != nil {
				return err
			}
			d.Gear = append(d.Gear, g)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "enchants-60.csv",
		[]string{"class", "spec", "population", "slot", "name", "wowhead_id", "pct", "chars", "of_chars"},
		func(line int, f []string) error {
			g, err := gearRow(f)
			if err != nil {
				return err
			}
			d.Enchants = append(d.Enchants, Enchant(g))
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "spec-population.csv",
		[]string{"class", "spec", "pct_of_class_60", "class_chars_60"},
		func(line int, f []string) error {
			var s SpecShare
			s.Class, s.Spec = f[0], f[1]
			var err error
			if s.PctOfClass60, err = pctOf(f[2], "pct_of_class_60"); err != nil {
				return err
			}
			if s.ClassChars60, err = int64Of(f[3], "class_chars_60"); err != nil {
				return err
			}
			d.Specs = append(d.Specs, s)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "census-classes.csv",
		[]string{"class", "chars", "alliance", "horde", "chars_60", "pct_of_class_at_60"},
		func(line int, f []string) error {
			c := ClassCensus{Class: f[0]}
			var err error
			if c.Chars, err = int64Of(f[1], "chars"); err != nil {
				return err
			}
			if c.Alliance, err = int64Of(f[2], "alliance"); err != nil {
				return err
			}
			if c.Horde, err = int64Of(f[3], "horde"); err != nil {
				return err
			}
			if c.Chars60, err = int64Of(f[4], "chars_60"); err != nil {
				return err
			}
			if c.PctOfClassAt60, err = pctOf(f[5], "pct_of_class_at_60"); err != nil {
				return err
			}
			d.Classes = append(d.Classes, c)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "census-races.csv",
		[]string{"race", "faction", "pct_of_faction", "female_pct"},
		func(line int, f []string) error {
			r := RaceCensus{Race: f[0], Faction: f[1]}
			if r.Faction != "alliance" && r.Faction != "horde" {
				return fmt.Errorf("faction %q is neither alliance nor horde", r.Faction)
			}
			var err error
			if r.PctOfFaction, err = pctOf(f[2], "pct_of_faction"); err != nil {
				return err
			}
			if r.FemalePct, err = pctOf(f[3], "female_pct"); err != nil {
				return err
			}
			d.Races = append(d.Races, r)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "census-class-gender.csv",
		[]string{"class", "female_pct"},
		func(line int, f []string) error {
			g := ClassGender{Class: f[0]}
			pct, err := pctOf(f[1], "female_pct")
			if err != nil {
				return err
			}
			g.FemalePct = pct
			d.ClassGender = append(d.ClassGender, g)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "census-race-class-pairs.csv",
		[]string{"list", "rank", "pair", "pct_of_all", "chars"},
		func(line int, f []string) error {
			p := RaceClassPair{List: f[0], Pair: f[2]}
			if p.List != "most" && p.List != "least" {
				return fmt.Errorf("list %q is neither most nor least", p.List)
			}
			var err error
			if p.Rank, err = intOf(f[1], "rank"); err != nil {
				return err
			}
			if p.PctOfAll, err = pctOf(f[3], "pct_of_all"); err != nil {
				return err
			}
			if p.Chars, err = scaledInt64Of(f[4], "chars"); err != nil {
				return err
			}
			d.RaceClassPairs = append(d.RaceClassPairs, p)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "census-levels.csv",
		[]string{"level", "chars"},
		func(line int, f []string) error {
			var l LevelCensus
			var err error
			if l.Level, err = intOf(f[0], "level"); err != nil {
				return err
			}
			if l.Level < 1 || l.Level > 60 {
				return fmt.Errorf("level %d is outside 1..60", l.Level)
			}
			if l.Chars, err = int64Of(f[1], "chars"); err != nil {
				return err
			}
			d.Levels = append(d.Levels, l)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "behaviour-stats.csv",
		[]string{"what", "name", "pct", "count"},
		func(line int, f []string) error {
			b := BehaviourStat{What: f[0], Name: f[1]}
			if b.What == "" || b.Name == "" {
				return errors.New("empty what or name")
			}
			var err error
			if b.Pct, err = pctOf(f[2], "pct"); err != nil {
				return err
			}
			if f[3] != "" {
				if b.Count, err = int64Of(f[3], "count"); err != nil {
					return err
				}
				b.HasCount = true
			}
			d.Behaviour = append(d.Behaviour, b)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "new-forever-bosses.csv",
		[]string{"boss", "instance", "pct_of_characters_killed"},
		func(line int, f []string) error {
			k := BossKill{Boss: f[0], Instance: f[1]}
			pct, err := pctOf(f[2], "pct_of_characters_killed")
			if err != nil {
				return err
			}
			k.Pct = pct
			d.Bosses = append(d.Bosses, k)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := parse(fsys, "legacy-achievements.csv",
		[]string{"group", "achievement", "criteria"},
		func(line int, f []string) error {
			a := Achievement{Group: f[0], Achievement: f[1], Criteria: f[2]}
			if a.Group == "" || a.Achievement == "" {
				return errors.New("empty group or achievement")
			}
			d.Achievements = append(d.Achievements, a)
			return nil
		}); err != nil {
		return nil, err
	}
	if err := d.check(); err != nil {
		return nil, err
	}
	d.index()
	return d, nil
}

// parse reads one CSV file: the header must be exactly want, every row must have that many fields and
// each hands them to row with its line number. A CSV syntax error keeps the file and line too.
func parse(fsys fs.FS, name string, want []string, row func(line int, fields []string) error) error {
	f, err := fsys.Open(name)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return fmt.Errorf("%s: line 1: %v", name, err)
	}
	if !slices.Equal(header, want) {
		return fmt.Errorf("%s: line 1: columns %v; want %v", name, header, want)
	}
	for line := 2; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: line %d: %v", name, line, err)
		}
		if len(rec) != len(want) {
			return fmt.Errorf("%s: line %d: %d fields; want %d", name, line, len(rec), len(want))
		}
		if err := row(line, rec); err != nil {
			return fmt.Errorf("%s: line %d: %v", name, line, err)
		}
	}
	return nil
}

// gearRow parses one row of a gear-shaped file (gear-60.csv and enchants-60.csv share the columns).
func gearRow(f []string) (GearItem, error) {
	var g GearItem
	g.Class, g.Spec, g.Population = f[0], f[1], f[2]
	g.Slot, g.Name = strings.TrimSpace(f[3]), strings.TrimSpace(f[4])
	if !slices.Contains(populations, g.Population) {
		return g, fmt.Errorf("population %q is not one of %v", g.Population, populations)
	}
	if g.Slot == "" {
		return g, errors.New("empty slot")
	}
	if g.Name == "" {
		return g, errors.New("empty name")
	}
	var err error
	if g.ID, err = wowheadID(f[5]); err != nil {
		return g, err
	}
	if g.Pct, err = pctOf(f[6], "pct"); err != nil {
		return g, err
	}
	if g.Chars, err = int64Of(f[7], "chars"); err != nil {
		return g, err
	}
	if g.OfChars, err = int64Of(f[8], "of_chars"); err != nil {
		return g, err
	}
	return g, nil
}

// check cross-checks the files: every class and spec any table names has a spec-population row, and
// each of those specs has a stat priority and level 60 gear rows. A spec the site lists but a table
// misses would otherwise read as "no data" deep inside a rule.
func (d *Data) check() error {
	known := map[specKey]bool{}
	for _, s := range d.Specs {
		known[specKey{s.Class, s.Spec}] = true
	}
	if len(known) == 0 {
		return errors.New("spec-population.csv: no specs")
	}
	named := map[specKey]string{}
	add := func(class, spec, file string) {
		k := specKey{class, spec}
		if _, ok := named[k]; !ok {
			named[k] = file
		}
	}
	for _, t := range d.TalentPicks {
		add(t.Class, t.Spec, "talent-picks.csv")
	}
	for _, p := range d.TreePoints {
		add(p.Class, p.Spec, "talent-tree-points.csv")
	}
	for _, s := range d.Splits {
		add(s.Class, s.Spec, "talent-splits-60.csv")
	}
	for _, w := range d.StatPriority {
		add(w.Class, w.Spec, "stat-priority-60.csv")
	}
	for _, g := range d.Gear {
		add(g.Class, g.Spec, "gear-60.csv")
	}
	for _, e := range d.Enchants {
		add(e.Class, e.Spec, "enchants-60.csv")
	}
	var missing []string
	for k, file := range named {
		if !known[k] {
			missing = append(missing, fmt.Sprintf("%s/%s (in %s)", k.Class, k.Spec, file))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("specs not in spec-population.csv: %s", strings.Join(missing, ", "))
	}
	priorities := map[specKey]bool{}
	for _, w := range d.StatPriority {
		priorities[specKey{w.Class, w.Spec}] = true
	}
	geared := map[specKey]bool{}
	for _, g := range d.Gear {
		if g.Population == PopulationBestGeared && !g.SetSlot() {
			geared[specKey{g.Class, g.Spec}] = true
		}
	}
	var noPriority, noGear []string
	for k := range known {
		if !priorities[k] {
			noPriority = append(noPriority, k.Class+"/"+k.Spec)
		}
		if !geared[k] {
			noGear = append(noGear, k.Class+"/"+k.Spec)
		}
	}
	sort.Strings(noPriority)
	sort.Strings(noGear)
	if len(noPriority) > 0 {
		return fmt.Errorf("specs without a stat priority: %s", strings.Join(noPriority, ", "))
	}
	if len(noGear) > 0 {
		return fmt.Errorf("specs without level 60 gear rows: %s", strings.Join(noGear, ", "))
	}
	return nil
}

// index builds the per-item view of the gear table: name, slots and the specs that wear the id.
func (d *Data) index() {
	for _, g := range d.Gear {
		if g.ID <= 0 || g.SetSlot() {
			continue
		}
		info, ok := d.items[g.ID]
		if !ok {
			info = &ItemInfo{ID: g.ID, Name: g.Name}
			d.items[g.ID] = info
		}
		if !slices.Contains(info.Slots, g.Slot) {
			info.Slots = append(info.Slots, g.Slot)
		}
		info.Wearers = append(info.Wearers, ItemWearer{
			Class: g.Class, Spec: g.Spec, Slot: g.Slot, Population: g.Population, Pct: g.Pct,
		})
	}
}

// SpecList returns every spec the player population file names, sorted by class then spec.
func (d *Data) SpecList() []SpecShare {
	out := append([]SpecShare(nil), d.Specs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return out[i].Spec < out[j].Spec
	})
	return out
}

// SpecKnown reports whether the tables name this class and spec.
func (d *Data) SpecKnown(class, spec string) bool {
	for _, s := range d.Specs {
		if s.Class == class && s.Spec == spec {
			return true
		}
	}
	return false
}

// CommonTalents returns the spec's talent picks at a level group with at least minShare percent of
// the spec's characters, highest share first. A level group with no rows is empty, never a guess.
func (d *Data) CommonTalents(class, spec, levels string, minShare float64) []TalentPick {
	var out []TalentPick
	for _, t := range d.TalentPicks {
		if t.Class == class && t.Spec == spec && t.Levels == levels && t.PickPct >= minShare {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PickPct != out[j].PickPct {
			return out[i].PickPct > out[j].PickPct
		}
		if out[i].Tree != out[j].Tree {
			return out[i].Tree < out[j].Tree
		}
		if out[i].Row != out[j].Row {
			return out[i].Row < out[j].Row
		}
		return out[i].Col < out[j].Col
	})
	return out
}

// SplitsFor returns the spec's most common level 60 point splits, most common first. A spec the file
// does not name is empty, never a guess.
func (d *Data) SplitsFor(class, spec string) []TalentSplit {
	var out []TalentSplit
	for _, s := range d.Splits {
		if s.Class == class && s.Spec == spec {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pct != out[j].Pct {
			return out[i].Pct > out[j].Pct
		}
		return out[i].Split < out[j].Split
	})
	return out
}

// StatPriorityFor returns the spec's stat priority at 60, highest first (equal orders keep the file's
// order).
func (d *Data) StatPriorityFor(class, spec string) []StatWeight {
	var out []StatWeight
	for _, w := range d.StatPriority {
		if w.Class == class && w.Spec == spec {
			out = append(out, w)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// GearRows returns every level 60 gear row the spec has, in every slot and population (item-set
// rows included): the gear rule's own-data membership and per-slot family reading.
func (d *Data) GearRows(class, spec string) []GearItem {
	var out []GearItem
	for _, g := range d.Gear {
		if g.Class == class && g.Spec == spec {
			out = append(out, g)
		}
	}
	return out
}

// GearFor returns the items the spec wears at 60 in one slot (empty for every slot), best geared
// population only, item-set rows excluded, most worn first.
func (d *Data) GearFor(class, spec, slot string) []GearItem {
	var out []GearItem
	for _, g := range d.Gear {
		if g.Class != class || g.Spec != spec || g.SetSlot() || g.Population != PopulationBestGeared {
			continue
		}
		if slot != "" && !strings.EqualFold(g.Slot, slot) {
			continue
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pct != out[j].Pct {
			return out[i].Pct > out[j].Pct
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// EnchantsFor returns the enchants the spec uses at 60 in one slot (empty for every slot), the
// all-level-60 population (every character, not only the best geared: a raider's habit is not the
// pool's), most used first.
func (d *Data) EnchantsFor(class, spec, slot string) []Enchant {
	var out []Enchant
	for _, e := range d.Enchants {
		if e.Class != class || e.Spec != spec || e.Population != PopulationAll {
			continue
		}
		if slot != "" && !strings.EqualFold(e.Slot, slot) {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pct != out[j].Pct {
			return out[i].Pct > out[j].Pct
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Item returns what the gear table knows about one item id.
func (d *Data) Item(id int64) (ItemInfo, bool) {
	info, ok := d.items[id]
	if !ok {
		return ItemInfo{}, false
	}
	return *info, true
}

// number parsing

func intOf(s, field string) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%s %q is not an integer", field, s)
	}
	return v, nil
}

func int64Of(s, field string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not an integer", field, s)
	}
	return v, nil
}

// optInt64Of reads a count the file leaves empty sometimes (a talent with no ranks, a trait the site
// does not map): empty is 0.
func optInt64Of(s, field string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return int64Of(s, field)
}

func numOf(s, field string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a number", field, s)
	}
	return v, nil
}

func pctOf(s, field string) (float64, error) {
	v, err := numOf(s, field)
	if err != nil {
		return 0, err
	}
	if v < 0 || v > 100 {
		return 0, fmt.Errorf("%s %v is outside 0..100", field, v)
	}
	return v, nil
}

// scaledInt64Of reads a count the file scales with a k suffix ("464k" is 464000).
func scaledInt64Of(s, field string) (int64, error) {
	v := strings.TrimSpace(s)
	scale := int64(1)
	if strings.HasSuffix(v, "k") {
		scale = 1000
		v = strings.TrimSuffix(v, "k")
	}
	n, err := int64Of(v, field)
	if err != nil {
		return 0, err
	}
	return n * scale, nil
}

// wowheadID reads the wowhead_id column ("item=22267"), empty for a row with no item.
func wowheadID(s string) (int64, error) {
	v := strings.TrimSpace(s)
	if v == "" {
		return 0, nil
	}
	if !strings.HasPrefix(v, "item=") {
		return 0, fmt.Errorf("wowhead_id %q is not item=<id>", v)
	}
	return int64Of(strings.TrimPrefix(v, "item="), "wowhead_id")
}

// splitShape reports whether a point split is three numbers joined by "/".
func splitShape(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}
