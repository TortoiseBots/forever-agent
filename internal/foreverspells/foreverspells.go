// Package foreverspells reads the Forever spell data (Classic 1.60 DB2): one row
// per class ability and crowd-control aura with its cooldown, category, GCD and
// mechanic numbers, plus every SpellName id and name. The data files are
// generated (tools/foreverspellgen, round R2) and embedded in the binary; until
// the lead commits them Default returns an empty table.
package foreverspells

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// Spell is one class ability or crowd-control aura of the Forever spell data (Classic 1.60 DB2).
type Spell struct {
	ID                 uint32
	Name               string
	Class              string // warrior, paladin, hunter, rogue, priest, shaman, mage, warlock, druid; "" none
	SkillLine          uint16
	Rank               uint8  // 1..n within (class, name) by (level, id); 0 for a single-id name
	Level              uint16 // SpellLevels.BaseLevel
	RecoveryMS         uint32 // SpellCooldowns.RecoveryTime
	CategoryRecoveryMS uint32 // SpellCooldowns.CategoryRecoveryTime
	Category           uint16 // SpellCategories.Category
	GCDMS              uint16 // SpellCooldowns.StartRecoveryTime
	GCDCategory        uint16 // SpellCategories.StartRecoveryCategory
	Mechanic           uint8  // SpellCategories.Mechanic
	Dispel             uint8  // SpellCategories.DispelType
	Attr0              uint32 // SpellMisc.Attributes[0]
}

// Passive reports SPELL_ATTR0_PASSIVE (SharedDefines.h:444).
func (s Spell) Passive() bool { return s.Attr0&0x00000040 != 0 }

// CooldownOnEvent reports SPELL_ATTR0_COOLDOWN_ON_EVENT (:463).
func (s Spell) CooldownOnEvent() bool { return s.Attr0&0x02000000 != 0 }

// classSet is the nine class words a spells.tsv row may carry ("" for a crowd-control-only row).
var classSet = map[string]bool{
	"": true, "warrior": true, "paladin": true, "hunter": true, "rogue": true,
	"priest": true, "shaman": true, "mage": true, "warlock": true, "druid": true,
}

const (
	spellsHeader  = "# forever-spells v1 source="
	spellsColumns = "id\tname\tclass\tskill_line\trank\tlevel\trecovery_ms\tcategory_recovery_ms\tcategory\tgcd_ms\tgcd_category\tmechanic\tdispel\tattr0"
	namesHeader   = "# forever-spell-names v1 source="
	namesColumns  = "id\tname"
)

// Table is the parsed spell data: the class/CC rows by id, every name by id,
// and the ids of each name for case-insensitive lookup.
type Table struct {
	byID   map[uint32]Spell
	names  map[uint32]string
	byName map[string][]uint32
	source string // the schema the data came from, from the header
	loaded bool
}

// Parse reads the spells and names files (plain readers; the caller unzips).
func Parse(spells io.Reader, names io.Reader) (*Table, error) {
	t := &Table{byID: map[uint32]Spell{}, names: map[uint32]string{}, byName: map[string][]uint32{}}
	spellSource, err := parseSpells(t, spells)
	if err != nil {
		return nil, err
	}
	nameSource, err := parseNames(t, names)
	if err != nil {
		return nil, err
	}
	if spellSource != nameSource {
		return nil, fmt.Errorf("names.tsv: line 1: source %q differs from spells.tsv source %q", nameSource, spellSource)
	}
	t.source = spellSource
	for key, ids := range t.byName {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		// An id in both files names one spell: keep each id once per lower-case name.
		seen := make(map[uint32]bool, len(ids))
		uniq := ids[:0]
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				uniq = append(uniq, id)
			}
		}
		t.byName[key] = uniq
	}
	t.loaded = true
	return t, nil
}

// parseSpells reads the spells.tsv rows into the table, keeping the stored rank as-is; it returns the source.
func parseSpells(t *Table, r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	line := 0
	var source string
	var lastID uint32 // rows are sorted by id
	first := true
	addName := func(id uint32, name string) {
		key := strings.ToLower(name)
		t.byName[key] = append(t.byName[key], id)
	}
	next := func() bool { ok := sc.Scan(); line++; return ok }
	if !next() {
		if err := sc.Err(); err != nil {
			return "", fmt.Errorf("spells.tsv: line 1: %v", err)
		}
		return "", fmt.Errorf("spells.tsv: line 1: want header %q", spellsHeader+"<schema>")
	}
	if !strings.HasPrefix(sc.Text(), spellsHeader) || len(sc.Text()) <= len(spellsHeader) {
		return "", fmt.Errorf("spells.tsv: line 1: unknown header %q", sc.Text())
	}
	source = sc.Text()[len(spellsHeader):]
	if !next() {
		return "", fmt.Errorf("spells.tsv: line 2: want %q", spellsColumns)
	}
	if sc.Text() != spellsColumns {
		return "", fmt.Errorf("spells.tsv: line 2: unknown columns %q; want %q", sc.Text(), spellsColumns)
	}
	for next() {
		text := sc.Text()
		fs := strings.Split(text, "\t")
		if len(fs) != 14 {
			return "", fmt.Errorf("spells.tsv: line %d: %d fields; want 14", line, len(fs))
		}
		id, err := parseU32(fs[0], 32)
		if err != nil {
			return "", fmt.Errorf("spells.tsv: line %d: bad id %q", line, fs[0])
		}
		name := fs[1]
		class := fs[2]
		if err := checkName(name); err != nil {
			return "", fmt.Errorf("spells.tsv: line %d: bad name: %v", line, err)
		}
		if !classSet[class] {
			return "", fmt.Errorf("spells.tsv: line %d: bad class %q", line, class)
		}
		nums := fs[3:]
		parsed := make([]uint64, len(nums))
		widths := []int{16, 8, 16, 32, 32, 16, 16, 16, 8, 8, 32}
		for i, s := range nums {
			v, err := parseU32(s, widths[i])
			if err != nil {
				return "", fmt.Errorf("spells.tsv: line %d: bad number %q", line, s)
			}
			parsed[i] = uint64(v)
		}
		if _, dup := t.byID[id]; dup {
			return "", fmt.Errorf("spells.tsv: line %d: id %d twice", line, id)
		}
		if !first && id <= lastID {
			return "", fmt.Errorf("spells.tsv: line %d: id %d out of order", line, id)
		}
		first = false
		lastID = id
		t.byID[id] = Spell{
			ID: id, Name: name, Class: class,
			SkillLine: uint16(parsed[0]), Rank: uint8(parsed[1]), Level: uint16(parsed[2]),
			RecoveryMS: uint32(parsed[3]), CategoryRecoveryMS: uint32(parsed[4]),
			Category: uint16(parsed[5]), GCDMS: uint16(parsed[6]), GCDCategory: uint16(parsed[7]),
			Mechanic: uint8(parsed[8]), Dispel: uint8(parsed[9]), Attr0: uint32(parsed[10]),
		}
		addName(id, name)
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("spells.tsv: line %d: %v", line, err)
	}
	return source, nil
}

// parseNames reads the names.tsv rows into the table; it returns the source.
func parseNames(t *Table, r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	line := 0
	var source string
	var lastID uint32
	first := true
	next := func() bool { ok := sc.Scan(); line++; return ok }
	if !next() {
		if err := sc.Err(); err != nil {
			return "", fmt.Errorf("names.tsv: line 1: %v", err)
		}
		return "", fmt.Errorf("names.tsv: line 1: want header %q", namesHeader+"<schema>")
	}
	if !strings.HasPrefix(sc.Text(), namesHeader) || len(sc.Text()) <= len(namesHeader) {
		return "", fmt.Errorf("names.tsv: line 1: unknown header %q", sc.Text())
	}
	source = sc.Text()[len(namesHeader):]
	if !next() {
		return "", fmt.Errorf("names.tsv: line 2: want %q", namesColumns)
	}
	if sc.Text() != namesColumns {
		return "", fmt.Errorf("names.tsv: line 2: unknown columns %q; want %q", sc.Text(), namesColumns)
	}
	for next() {
		text := sc.Text()
		fs := strings.Split(text, "\t")
		if len(fs) != 2 {
			return "", fmt.Errorf("names.tsv: line %d: %d fields; want 2", line, len(fs))
		}
		id, err := parseU32(fs[0], 32)
		if err != nil {
			return "", fmt.Errorf("names.tsv: line %d: bad id %q", line, fs[0])
		}
		if err := checkName(fs[1]); err != nil {
			return "", fmt.Errorf("names.tsv: line %d: bad name: %v", line, err)
		}
		if _, dup := t.names[id]; dup {
			return "", fmt.Errorf("names.tsv: line %d: id %d twice", line, id)
		}
		if !first && id <= lastID {
			return "", fmt.Errorf("names.tsv: line %d: id %d out of order", line, id)
		}
		first = false
		lastID = id
		t.names[id] = fs[1]
		t.byName[strings.ToLower(fs[1])] = append(t.byName[strings.ToLower(fs[1])], id)
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("names.tsv: line %d: %v", line, err)
	}
	return source, nil
}

// parseU32 parses an unsigned decimal in a type of bits width.
func parseU32(s string, bits int) (uint32, error) {
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	v, err := strconv.ParseUint(s, 10, bits)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// checkName refuses an empty name or one with a tab or a newline.
func checkName(name string) error {
	if name == "" {
		return fmt.Errorf("empty name")
	}
	if strings.ContainsAny(name, "\t\n\r") {
		return fmt.Errorf("bad name %q", name)
	}
	return nil
}

// openSpells reads the spells table and the names table at the root of fsys: names.tsv.gz first,
// then plain names.tsv (the testdata form). It returns the two files' bytes.
func openSpells(fsys fs.FS) (spells, names []byte, err error) {
	var sf fs.File
	sf, err = fsys.Open("spells.tsv")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = sf.Close() }()
	spells, err = io.ReadAll(sf)
	if err != nil {
		return nil, nil, fmt.Errorf("spells.tsv: %v", err)
	}
	readPlain := func() ([]byte, error) {
		var nf fs.File
		nf, err = fsys.Open("names.tsv")
		if err != nil {
			return nil, err
		}
		defer func() { _ = nf.Close() }()
		return io.ReadAll(nf)
	}
	var nf fs.File
	nf, err = fsys.Open("names.tsv.gz")
	if err != nil {
		names, err = readPlain()
		return spells, names, err
	}
	defer func() { _ = nf.Close() }()
	var gz *gzip.Reader
	gz, err = gzip.NewReader(nf)
	if err != nil {
		return nil, nil, fmt.Errorf("names.tsv.gz: %v", err)
	}
	names, err = io.ReadAll(gz)
	_ = gz.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("names.tsv.gz: %v", err)
	}
	return spells, names, nil
}

// Load reads spells.tsv and names.tsv.gz (or plain names.tsv, for testdata) at the root of fsys.
func Load(fsys fs.FS) (*Table, error) {
	spells, names, err := openSpells(fsys)
	if err != nil {
		return nil, err
	}
	return Parse(bytes.NewReader(spells), bytes.NewReader(names))
}

// Loaded reports whether the table holds data.
func (t *Table) Loaded() bool { return t != nil && t.loaded }

// Source is the schema the data came from, from the header.
func (t *Table) Source() string {
	if t == nil {
		return ""
	}
	return t.source
}

// Lookup returns the spell row for id.
func (t *Table) Lookup(id uint32) (Spell, bool) {
	s, ok := t.byID[id]
	return s, ok
}

// Name returns the spell's name: the names file first, then the spells row; "" when neither has it.
func (t *Table) Name(id uint32) string {
	if n, ok := t.names[id]; ok {
		return n
	}
	if s, ok := t.byID[id]; ok {
		return s.Name
	}
	return ""
}

// IDsByName returns the ids of a name, case-insensitive, trimmed, ascending, from both files.
func (t *Table) IDsByName(name string) []uint32 {
	ids := t.byName[strings.ToLower(strings.TrimSpace(name))]
	out := append([]uint32(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// HasName reports whether any row carries the name.
func (t *Table) HasName(name string) bool { return len(t.IDsByName(name)) > 0 }

// ClassOf returns the spell's class word, "" when the id is not a class row.
func (t *Table) ClassOf(id uint32) string {
	s, ok := t.byID[id]
	if !ok {
		return ""
	}
	return s.Class
}

// FirstRankLevel returns the lowest level above 0 among the class rows of a name.
func (t *Table) FirstRankLevel(name string) (int, bool) {
	best := 0
	found := false
	for _, id := range t.IDsByName(name) {
		s, ok := t.byID[id]
		if !ok || s.Class == "" || s.Level == 0 {
			continue
		}
		if !found || int(s.Level) < best {
			best, found = int(s.Level), true
		}
	}
	return best, found
}
