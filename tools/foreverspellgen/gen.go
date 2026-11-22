package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/db2"
	"github.com/pjerra/forever-agent/internal/foreverspells"
)

// The 1.60 build 70399 layouts (DB2Metadata.h): total fields and layout hash per table, with the
// ID and the parent outside the record where the meta says so.
const (
	spellNameLayout, spellNameFields               = 0x782EE721, 1
	skillLineAbilityLayout, skillLineAbilityFields = 0x224F7EA0, 18
	spellLevelsLayout, spellLevelsFields           = 0x7EB86FDC, 5
	spellCooldownsLayout, spellCooldownsFields     = 0xDC945B8C, 5
	spellCategoriesLayout, spellCategoriesFields   = 0x679EF94C, 9
	spellMiscLayout, spellMiscFields               = 0x434B3607, 16
)

// The file field indices the decoders read (DB2Metadata.h order, the ID and the parent not
// counted where the meta keeps them outside the record).
const (
	spellNameField = 0 // Name

	skillLineField  = 3 // SkillLine
	skillSpellField = 4 // Spell

	levelsDiffField = 0 // DifficultyID
	levelsBaseField = 3 // BaseLevel

	cooldownsDiffField  = 0 // DifficultyID
	cooldownsCatField   = 1 // CategoryRecoveryTime
	cooldownsRecField   = 2 // RecoveryTime
	cooldownsStartField = 3 // StartRecoveryTime

	categoriesDiffField     = 0 // DifficultyID
	categoriesCatField      = 1 // Category
	categoriesDispelField   = 4 // DispelType
	categoriesMechanicField = 5 // Mechanic
	categoriesStartCatField = 7 // StartRecoveryCategory

	miscAttrsField = 0 // Attributes
	miscDiffField  = 1 // DifficultyID
)

// classLines is D5: the 27 Classic class skill lines, measured in SkillLineAbility.db2.
var classLines = map[uint16]string{
	26: "warrior", 256: "warrior", 257: "warrior",
	594: "paladin", 267: "paladin", 184: "paladin",
	50: "hunter", 163: "hunter", 51: "hunter",
	253: "rogue", 38: "rogue", 39: "rogue",
	613: "priest", 56: "priest", 78: "priest",
	375: "shaman", 373: "shaman", 374: "shaman",
	237: "mage", 8: "mage", 6: "mage",
	355: "warlock", 354: "warlock", 593: "warlock",
	574: "druid", 134: "druid", 573: "druid",
}

// classes is the order the stats line prints the nine class words in.
var classes = []string{"warrior", "paladin", "hunter", "rogue", "priest", "shaman", "mage", "warlock", "druid"}

// ccMechanics is D15: the crowd-control mechanics (SharedDefines.h:2874-2903). Gouge (mechanic
// 5, fear) and the other non-CC mechanics are not here.
var ccMechanics = map[int64]bool{10: true, 13: true, 14: true, 17: true, 18: true, 20: true, 23: true, 30: true}

// Inputs is every decoded table the join reads: plain structs, so the tests need no DB2 bytes.
type Inputs struct {
	Names      map[uint32]string // SpellName: every id and name
	Skills     []SkillLineAbility
	Levels     []SpellLevel
	Cooldowns  []SpellCooldown
	Categories []SpellCategory
	Misc       []SpellMisc
	Notes      []string // the decoders' notes, printed with the stats
}

// SkillLineAbility is one SkillLineAbility row: which spell a skill line teaches.
type SkillLineAbility struct {
	ID        uint32
	SkillLine uint16
	Spell     uint32
}

// SpellLevel is one SpellLevels row.
type SpellLevel struct {
	Spell        uint32
	DifficultyID int64
	BaseLevel    int64
}

// SpellCooldown is one SpellCooldowns row.
type SpellCooldown struct {
	Spell              uint32
	DifficultyID       int64
	CategoryRecoveryMS int64
	RecoveryMS         int64
	StartRecoveryMS    int64
}

// SpellCategory is one SpellCategories row.
type SpellCategory struct {
	Spell                 uint32
	DifficultyID          int64
	Category              int64
	Dispel                int64
	Mechanic              int64
	StartRecoveryCategory int64
}

// SpellMisc is one SpellMisc row.
type SpellMisc struct {
	Spell        uint32
	DifficultyID int64
	Attr0        uint32
}

// Stats is what the generator prints besides the files.
type Stats struct {
	Classes  map[string]int // rows per class word
	TwoClass int            // spells on the class lines of two classes, written with class ""
	CConly   int            // rows that exist only for a crowd-control mechanic
	Notes    []string       // what the decoders left out
}

// Build joins the decoded tables into the spells.tsv rows and the names file (D5-D7, D15). A
// second DifficultyID 0 row for one spell is an error: the join would otherwise pick one
// arbitrarily, and the numbers would depend on the file's record order.
func Build(in Inputs) (rows []foreverspells.Spell, names map[uint32]string, stats Stats, err error) {
	levels, err := diffZero(in.Levels, func(r SpellLevel) (uint32, int64) { return r.Spell, r.DifficultyID }, "SpellLevels")
	if err != nil {
		return nil, nil, Stats{}, err
	}
	cooldowns, err := diffZero(in.Cooldowns, func(r SpellCooldown) (uint32, int64) { return r.Spell, r.DifficultyID }, "SpellCooldowns")
	if err != nil {
		return nil, nil, Stats{}, err
	}
	categories, err := diffZero(in.Categories, func(r SpellCategory) (uint32, int64) { return r.Spell, r.DifficultyID }, "SpellCategories")
	if err != nil {
		return nil, nil, Stats{}, err
	}
	misc, err := diffZero(in.Misc, func(r SpellMisc) (uint32, int64) { return r.Spell, r.DifficultyID }, "SpellMisc")
	if err != nil {
		return nil, nil, Stats{}, err
	}

	// D5 and D7: a spell's class comes from every class line it is on, and its skill line from
	// its first SkillLineAbility row by that row's own id.
	type classRow struct {
		row     SkillLineAbility
		classes map[string]bool
	}
	bySpell := map[uint32]*classRow{}
	for _, s := range in.Skills {
		class, ok := classLines[s.SkillLine]
		if !ok {
			continue
		}
		if _, named := in.Names[s.Spell]; !named {
			continue
		}
		c := bySpell[s.Spell]
		if c == nil {
			c = &classRow{row: s, classes: map[string]bool{}}
			bySpell[s.Spell] = c
		} else if s.ID < c.row.ID {
			c.row = s
		}
		c.classes[class] = true
	}

	stats = Stats{Classes: map[string]int{}, Notes: in.Notes}
	rows = make([]foreverspells.Spell, 0, len(bySpell))
	added := make(map[uint32]bool, len(bySpell))
	for id, c := range bySpell {
		name := in.Names[id]
		class := ""
		if len(c.classes) == 1 {
			for only := range c.classes {
				class = only
			}
		} else {
			stats.TwoClass++
		}
		s, err := spellRow(id, name, class, c.row.SkillLine, levels[id], cooldowns[id], categories[id], misc[id])
		if err != nil {
			return nil, nil, Stats{}, err
		}
		rows = append(rows, s)
		added[id] = true
	}

	// D7's second part: a crowd-control mechanic with no class line is a row with class "".
	for id, cat := range categories {
		if added[id] || !ccMechanics[cat.Mechanic] {
			continue
		}
		name, named := in.Names[id]
		if !named {
			continue
		}
		s, err := spellRow(id, name, "", 0, levels[id], cooldowns[id], cat, misc[id])
		if err != nil {
			return nil, nil, Stats{}, err
		}
		rows = append(rows, s)
		stats.CConly++
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	rank(rows)
	for _, s := range rows {
		if s.Class != "" {
			stats.Classes[s.Class]++
		}
	}
	return rows, in.Names, stats, nil
}

// diffZero keeps the DifficultyID 0 rows of one table by spell; a second one for a spell is an
// error.
func diffZero[T any](rows []T, key func(T) (uint32, int64), table string) (map[uint32]T, error) {
	out := make(map[uint32]T, len(rows))
	for _, r := range rows {
		id, diff := key(r)
		if diff != 0 {
			continue
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("%s: spell %d has two DifficultyID 0 rows", table, id)
		}
		out[id] = r
	}
	return out, nil
}

// spellRow fills one row's numbers from the spell's DifficultyID 0 rows; a missing row leaves
// zeros (D7). A number outside the Spell field's type is an error naming the spell.
func spellRow(id uint32, name, class string, skillLine uint16, lvl SpellLevel, cd SpellCooldown, cat SpellCategory, misc SpellMisc) (foreverspells.Spell, error) {
	s := foreverspells.Spell{ID: id, Name: name, Class: class, SkillLine: skillLine, Attr0: misc.Attr0}
	nums := []struct {
		field string
		v     int64
		bits  uint
	}{
		{"level", lvl.BaseLevel, 16},
		{"recovery", cd.RecoveryMS, 32},
		{"category recovery", cd.CategoryRecoveryMS, 32},
		{"category", cat.Category, 16},
		{"gcd", cd.StartRecoveryMS, 16},
		{"gcd category", cat.StartRecoveryCategory, 16},
		{"mechanic", cat.Mechanic, 8},
		{"dispel", cat.Dispel, 8},
	}
	for _, n := range nums {
		if n.v < 0 || n.v > int64(uint64(1)<<n.bits-1) {
			return s, fmt.Errorf("spell %d (%s): %s %d does not fit %d bits", id, name, n.field, n.v, n.bits)
		}
	}
	s.Level = uint16(lvl.BaseLevel)
	s.RecoveryMS = uint32(cd.RecoveryMS)
	s.CategoryRecoveryMS = uint32(cd.CategoryRecoveryMS)
	s.Category = uint16(cat.Category)
	s.GCDMS = uint16(cd.StartRecoveryMS)
	s.GCDCategory = uint16(cat.StartRecoveryCategory)
	s.Mechanic = uint8(cat.Mechanic)
	s.Dispel = uint8(cat.Dispel)
	return s, nil
}

// rank numbers the class rows of one (class, lower-case name) by (level, id) 1..n (D6); a name
// with one id keeps rank 0.
func rank(rows []foreverspells.Spell) {
	groups := map[string][]int{}
	for i := range rows {
		if rows[i].Class == "" {
			continue
		}
		key := rows[i].Class + "\x00" + strings.ToLower(rows[i].Name)
		groups[key] = append(groups[key], i)
	}
	for _, idx := range groups {
		if len(idx) < 2 {
			continue
		}
		sort.Slice(idx, func(a, b int) bool {
			x, y := rows[idx[a]], rows[idx[b]]
			if x.Level != y.Level {
				return x.Level < y.Level
			}
			return x.ID < y.ID
		})
		for k, i := range idx {
			rows[i].Rank = uint8(k + 1)
		}
	}
}

// decodeNames reads SpellName.db2: every row's id and name. A row with an empty name is skipped
// and counted: the names file cannot hold an empty name.
func decodeNames(f *db2.File) (map[uint32]string, []string, error) {
	if err := f.CheckLayout("SpellName", spellNameLayout, spellNameFields); err != nil {
		return nil, nil, err
	}
	rows, notes, err := f.Rows()
	if err != nil {
		return nil, nil, err
	}
	names := make(map[uint32]string, len(rows))
	empty := 0
	for _, r := range rows {
		name, err := f.String(r, spellNameField)
		if err != nil {
			return nil, nil, fmt.Errorf("SpellName %d: %v", r.ID, err)
		}
		if name == "" {
			empty++
			continue
		}
		names[r.ID] = name
	}
	if empty > 0 {
		notes = append(notes, fmt.Sprintf("%d SpellName rows skipped: empty name", empty))
	}
	return names, notes, nil
}

// decodeSkills reads SkillLineAbility.db2: which skill line teaches which spell. The row id is
// the table's inline id, which the join orders by.
func decodeSkills(f *db2.File) ([]SkillLineAbility, []string, error) {
	if err := f.CheckLayout("SkillLineAbility", skillLineAbilityLayout, skillLineAbilityFields); err != nil {
		return nil, nil, err
	}
	rows, notes, err := f.Rows()
	if err != nil {
		return nil, nil, err
	}
	out := make([]SkillLineAbility, 0, len(rows))
	for _, r := range rows {
		line, err := f.Uint(r, skillLineField)
		if err != nil {
			return nil, nil, fmt.Errorf("SkillLineAbility %d: %v", r.ID, err)
		}
		spell, err := f.Int(r, skillSpellField)
		if err != nil {
			return nil, nil, fmt.Errorf("SkillLineAbility %d: %v", r.ID, err)
		}
		if line > math.MaxUint16 {
			return nil, nil, fmt.Errorf("SkillLineAbility %d: skill line %d does not fit 16 bits", r.ID, line)
		}
		if spell <= 0 || spell > math.MaxUint32 {
			return nil, nil, fmt.Errorf("SkillLineAbility %d: spell %d is not a spell id", r.ID, spell)
		}
		out = append(out, SkillLineAbility{ID: r.ID, SkillLine: uint16(line), Spell: uint32(spell)})
	}
	return out, notes, nil
}

// decodeLevels reads SpellLevels.db2's rows that name their spell.
func decodeLevels(f *db2.File) ([]SpellLevel, []string, error) {
	if err := f.CheckLayout("SpellLevels", spellLevelsLayout, spellLevelsFields); err != nil {
		return nil, nil, err
	}
	out := []SpellLevel{}
	notes, err := parented(f, "SpellLevels", func(r db2.Row) error {
		diff, err := f.Int(r, levelsDiffField)
		if err != nil {
			return fmt.Errorf("SpellLevels %d: %v", r.ID, err)
		}
		base, err := f.Int(r, levelsBaseField)
		if err != nil {
			return fmt.Errorf("SpellLevels %d: %v", r.ID, err)
		}
		out = append(out, SpellLevel{Spell: r.Parent, DifficultyID: diff, BaseLevel: base})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, notes, nil
}

// decodeCooldowns reads SpellCooldowns.db2's rows that name their spell.
func decodeCooldowns(f *db2.File) ([]SpellCooldown, []string, error) {
	if err := f.CheckLayout("SpellCooldowns", spellCooldownsLayout, spellCooldownsFields); err != nil {
		return nil, nil, err
	}
	out := []SpellCooldown{}
	notes, err := parented(f, "SpellCooldowns", func(r db2.Row) error {
		diff, err := f.Int(r, cooldownsDiffField)
		if err != nil {
			return fmt.Errorf("SpellCooldowns %d: %v", r.ID, err)
		}
		cat, err := f.Int(r, cooldownsCatField)
		if err != nil {
			return fmt.Errorf("SpellCooldowns %d: %v", r.ID, err)
		}
		rec, err := f.Int(r, cooldownsRecField)
		if err != nil {
			return fmt.Errorf("SpellCooldowns %d: %v", r.ID, err)
		}
		start, err := f.Int(r, cooldownsStartField)
		if err != nil {
			return fmt.Errorf("SpellCooldowns %d: %v", r.ID, err)
		}
		out = append(out, SpellCooldown{Spell: r.Parent, DifficultyID: diff, CategoryRecoveryMS: cat, RecoveryMS: rec, StartRecoveryMS: start})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, notes, nil
}

// decodeCategories reads SpellCategories.db2's rows that name their spell.
func decodeCategories(f *db2.File) ([]SpellCategory, []string, error) {
	if err := f.CheckLayout("SpellCategories", spellCategoriesLayout, spellCategoriesFields); err != nil {
		return nil, nil, err
	}
	out := []SpellCategory{}
	notes, err := parented(f, "SpellCategories", func(r db2.Row) error {
		diff, err := f.Int(r, categoriesDiffField)
		if err != nil {
			return fmt.Errorf("SpellCategories %d: %v", r.ID, err)
		}
		cat, err := f.Int(r, categoriesCatField)
		if err != nil {
			return fmt.Errorf("SpellCategories %d: %v", r.ID, err)
		}
		dispel, err := f.Int(r, categoriesDispelField)
		if err != nil {
			return fmt.Errorf("SpellCategories %d: %v", r.ID, err)
		}
		mechanic, err := f.Int(r, categoriesMechanicField)
		if err != nil {
			return fmt.Errorf("SpellCategories %d: %v", r.ID, err)
		}
		startCat, err := f.Int(r, categoriesStartCatField)
		if err != nil {
			return fmt.Errorf("SpellCategories %d: %v", r.ID, err)
		}
		out = append(out, SpellCategory{Spell: r.Parent, DifficultyID: diff, Category: cat, Dispel: dispel, Mechanic: mechanic, StartRecoveryCategory: startCat})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, notes, nil
}

// decodeMisc reads SpellMisc.db2's rows that name their spell, keeping Attributes[0].
func decodeMisc(f *db2.File) ([]SpellMisc, []string, error) {
	if err := f.CheckLayout("SpellMisc", spellMiscLayout, spellMiscFields); err != nil {
		return nil, nil, err
	}
	out := []SpellMisc{}
	notes, err := parented(f, "SpellMisc", func(r db2.Row) error {
		attrs, err := f.Uints(r, miscAttrsField)
		if err != nil {
			return fmt.Errorf("SpellMisc %d: %v", r.ID, err)
		}
		if len(attrs) == 0 {
			return fmt.Errorf("SpellMisc %d: no attributes", r.ID)
		}
		diff, err := f.Int(r, miscDiffField)
		if err != nil {
			return fmt.Errorf("SpellMisc %d: %v", r.ID, err)
		}
		out = append(out, SpellMisc{Spell: r.Parent, DifficultyID: diff, Attr0: attrs[0]})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, notes, nil
}

// parented runs read over the rows that name a parent spell and counts the rows that do not: a
// row of a parent-keyed table without a parent joins to no spell.
func parented(f *db2.File, table string, read func(db2.Row) error) ([]string, error) {
	rows, notes, err := f.Rows()
	if err != nil {
		return nil, err
	}
	skipped := 0
	for _, r := range rows {
		if !r.HasParent {
			skipped++
			continue
		}
		if err := read(r); err != nil {
			return nil, err
		}
	}
	if skipped > 0 {
		notes = append(notes, fmt.Sprintf("%d %s rows skipped: no parent spell", skipped, table))
	}
	return notes, nil
}

// WriteSpellsTSV writes spells.tsv in the 5.3 format: the header, the column line, then the rows
// by id, so a diff of two runs shows only real changes.
func WriteSpellsTSV(w io.Writer, source string, rows []foreverspells.Spell) error {
	sorted := append([]foreverspells.Spell(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "# forever-spells v1 source=%s\n", source)
	bw.WriteString("id\tname\tclass\tskill_line\trank\tlevel\trecovery_ms\tcategory_recovery_ms\tcategory\tgcd_ms\tgcd_category\tmechanic\tdispel\tattr0\n")
	for _, s := range sorted {
		fmt.Fprintf(bw, "%d\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
			s.ID, s.Name, s.Class, s.SkillLine, s.Rank, s.Level,
			s.RecoveryMS, s.CategoryRecoveryMS, s.Category, s.GCDMS, s.GCDCategory,
			s.Mechanic, s.Dispel, s.Attr0)
	}
	return bw.Flush()
}

// WriteNamesTSV writes names.tsv in the 5.3 format: the header, the column line, then the ids
// and names by id.
func WriteNamesTSV(w io.Writer, source string, names map[uint32]string) error {
	ids := make([]uint32, 0, len(names))
	for id := range names {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "# forever-spell-names v1 source=%s\n", source)
	bw.WriteString("id\tname\n")
	for _, id := range ids {
		fmt.Fprintf(bw, "%d\t%s\n", id, names[id])
	}
	return bw.Flush()
}

// gzipNames is the gzipped names file. The header carries no timestamp and no name, so a rerun
// on the same data is byte-identical.
func gzipNames(source string, names map[uint32]string) ([]byte, error) {
	var b bytes.Buffer
	zw, err := gzip.NewWriterLevel(&b, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if err := WriteNamesTSV(zw, source, names); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
