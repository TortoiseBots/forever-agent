package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/db2"
	"github.com/pjerra/forever-agent/internal/foreverspells"
)

// testSource is the schema string the test files carry, the only one the committed data may name.
const testSource = "WOWSTATIC_1_60_1_70399"

// classInputs is the join fixture: three mage ranks of Frostbolt on skill line 6 and one rogue
// ability on line 38, with the levels that order the ranks.
func classInputs() Inputs {
	return Inputs{
		Names: map[uint32]string{
			116:  "Frostbolt",
			205:  "Frostbolt",
			837:  "Frostbolt",
			1752: "Sinister Strike",
		},
		Skills: []SkillLineAbility{
			{ID: 10, SkillLine: 6, Spell: 116},
			{ID: 11, SkillLine: 6, Spell: 205},
			{ID: 12, SkillLine: 6, Spell: 837},
			{ID: 13, SkillLine: 38, Spell: 1752},
		},
		Levels: []SpellLevel{
			{Spell: 116, BaseLevel: 4},
			{Spell: 205, BaseLevel: 8},
			{Spell: 837, BaseLevel: 14},
			{Spell: 1752, BaseLevel: 1},
		},
	}
}

func TestBuildClassAndRank(t *testing.T) {
	rows, _, stats, err := Build(classInputs())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("%d rows; want 4", len(rows))
	}
	want := map[uint32]struct {
		class string
		rank  uint8
	}{
		116:  {"mage", 1},
		205:  {"mage", 2},
		837:  {"mage", 3},
		1752: {"rogue", 0},
	}
	for _, s := range rows {
		w := want[s.ID]
		if s.Class != w.class || s.Rank != w.rank {
			t.Errorf("spell %d: class %q rank %d; want %q rank %d", s.ID, s.Class, s.Rank, w.class, w.rank)
		}
	}
	if stats.Classes["mage"] != 3 || stats.Classes["rogue"] != 1 {
		t.Errorf("stats.Classes = %v; want 3 mage and 1 rogue", stats.Classes)
	}
	// The rank order is (level, id), not the ids: with the levels reversed 837 comes first.
	in := classInputs()
	in.Levels = []SpellLevel{
		{Spell: 116, BaseLevel: 14},
		{Spell: 205, BaseLevel: 8},
		{Spell: 837, BaseLevel: 4},
	}
	rows, _, _, err = Build(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rows {
		if s.ID == 837 && s.Rank != 1 {
			t.Errorf("spell 837 at level 4: rank %d; want 1", s.Rank)
		}
		if s.ID == 116 && s.Rank != 3 {
			t.Errorf("spell 116 at level 14: rank %d; want 3", s.Rank)
		}
	}
}

func TestBuildTwoClasses(t *testing.T) {
	in := Inputs{
		Names: map[uint32]string{100: "Shared"},
		Skills: []SkillLineAbility{
			{ID: 5, SkillLine: 6, Spell: 100},
			{ID: 6, SkillLine: 38, Spell: 100},
		},
	}
	rows, _, stats, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Class != "" {
		t.Fatalf("rows = %+v; want one row with class \"\"", rows)
	}
	if stats.TwoClass != 1 {
		t.Errorf("stats.TwoClass = %d; want 1", stats.TwoClass)
	}
}

func TestBuildCCOnly(t *testing.T) {
	in := Inputs{
		Names: map[uint32]string{118: "Polymorph", 5782: "Fear"},
		Categories: []SpellCategory{
			{Spell: 118, Mechanic: 17},
			{Spell: 5782, Mechanic: 5},
		},
	}
	rows, _, stats, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != 118 || rows[0].Class != "" {
		t.Fatalf("rows = %+v; want only Polymorph with class \"\"", rows)
	}
	if rows[0].Mechanic != 17 {
		t.Errorf("Polymorph mechanic = %d; want 17", rows[0].Mechanic)
	}
	if stats.CConly != 1 {
		t.Errorf("stats.CConly = %d; want 1", stats.CConly)
	}
}

func TestBuildDifficulty(t *testing.T) {
	in := Inputs{
		Names:  map[uint32]string{116: "Frostbolt"},
		Skills: []SkillLineAbility{{ID: 1, SkillLine: 6, Spell: 116}},
		Levels: []SpellLevel{
			{Spell: 116, DifficultyID: 1, BaseLevel: 99},
			{Spell: 116, BaseLevel: 4},
		},
	}
	rows, _, _, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Level != 4 {
		t.Fatalf("rows = %+v; want one row at level 4", rows)
	}
	in.Levels = append(in.Levels, SpellLevel{Spell: 116, BaseLevel: 8})
	if _, _, _, err := Build(in); err == nil {
		t.Error("two DifficultyID 0 rows for one spell were accepted")
	}
}

func TestRoundTrip(t *testing.T) {
	rows, names, _, err := Build(classInputs())
	if err != nil {
		t.Fatal(err)
	}
	var spells, nameFile bytes.Buffer
	if err := WriteSpellsTSV(&spells, testSource, rows); err != nil {
		t.Fatal(err)
	}
	if err := WriteNamesTSV(&nameFile, testSource, names); err != nil {
		t.Fatal(err)
	}
	tab, err := foreverspells.Parse(bytes.NewReader(spells.Bytes()), bytes.NewReader(nameFile.Bytes()))
	if err != nil {
		t.Fatalf("Parse of the generator's files: %v\n%s", err, spells.String())
	}
	for _, want := range rows {
		got, ok := tab.Lookup(want.ID)
		if !ok || got != want {
			t.Errorf("Lookup(%d) = %+v, %v; want %+v", want.ID, got, ok, want)
		}
	}
	if got := tab.IDsByName("frostbolt"); len(got) != 3 {
		t.Errorf("IDsByName(frostbolt) = %v; want three ids", got)
	}
	if tab.Source() != testSource {
		t.Errorf("Source = %q; want %q", tab.Source(), testSource)
	}
}

func TestNamesGzipDeterministic(t *testing.T) {
	names := map[uint32]string{116: "Frostbolt", 133: "Fireball", 1752: "Sinister Strike"}
	a, err := gzipNames(testSource, names)
	if err != nil {
		t.Fatal(err)
	}
	b, err := gzipNames(testSource, names)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("two gzip writes of one names file differ")
	}
	zr, err := gzip.NewReader(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	if !zr.ModTime.IsZero() || zr.Name != "" {
		t.Errorf("the header carries ModTime %v and name %q; want neither", zr.ModTime, zr.Name)
	}
	text, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# forever-spell-names v1 source=" + testSource + "\n", "id\tname\n", "1752\tSinister Strike\n"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("the names file lacks %q:\n%s", want, text)
		}
	}
}

// writeDB2 writes the smallest WDC5 file with fieldCount plain 32-bit columns and one record. The
// decoders are tested for their layout refusal here, so every value is zero.
func writeDB2(t *testing.T, path string, layout uint32, fieldCount int) {
	t.Helper()
	const headerLen = 204
	recordSize := 4 * fieldCount
	dataStart := headerLen + 40 + 4*fieldCount
	var b bytes.Buffer
	le := binary.LittleEndian
	u16 := func(v uint16) { _ = binary.Write(&b, le, v) }
	u32 := func(v uint32) { _ = binary.Write(&b, le, v) }
	b.WriteString("WDC5")
	u32(5)
	schema := make([]byte, 128)
	copy(schema, testSource)
	b.Write(schema)
	u32(1)                  // record count
	u32(uint32(fieldCount)) // field count
	u32(uint32(recordSize))
	u32(0)      // string table size
	u32(0x1234) // table hash
	u32(layout)
	u32(0) // min id
	u32(0) // max id
	u32(1) // locale
	u16(0x4)
	u16(0)                  // id index: the ids are in the section ID list
	u32(uint32(fieldCount)) // total fields
	u32(0)                  // packed data offset
	u32(0)                  // parent lookups
	u32(0)                  // column meta size: every column plain
	u32(0)                  // common data size
	u32(0)                  // pallet data size
	u32(1)                  // section count
	_ = binary.Write(&b, le, uint64(0))
	u32(uint32(dataStart)) // the section's file offset
	u32(1)                 // the section's record count
	u32(0)                 // string table size
	u32(0)                 // offset records end
	u32(4)                 // id list size
	u32(0)                 // relationship data
	u32(0)                 // offset map id count
	u32(0)                 // copy table count
	for i := range fieldCount {
		u16(0) // 32 bits wide
		u16(uint16(4 * i))
	}
	if b.Len() != dataStart {
		t.Fatalf("the header blocks end at %d, the data starts at %d", b.Len(), dataStart)
	}
	b.Write(make([]byte, recordSize))
	u32(1) // the record's id
	if b.Len() != dataStart+recordSize+4 {
		t.Fatalf("wrote %d bytes; want %d", b.Len(), dataStart+recordSize+4)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDecodersCheckLayout(t *testing.T) {
	cases := []struct {
		table  string
		layout uint32
		fields int
		decode func(*db2.File) error
	}{
		{"SpellName", spellNameLayout, spellNameFields, func(f *db2.File) error {
			_, _, err := decodeNames(f)
			return err
		}},
		{"SkillLineAbility", skillLineAbilityLayout, skillLineAbilityFields, func(f *db2.File) error {
			_, _, err := decodeSkills(f)
			return err
		}},
		{"SpellLevels", spellLevelsLayout, spellLevelsFields, func(f *db2.File) error {
			_, _, err := decodeLevels(f)
			return err
		}},
		{"SpellCooldowns", spellCooldownsLayout, spellCooldownsFields, func(f *db2.File) error {
			_, _, err := decodeCooldowns(f)
			return err
		}},
		{"SpellCategories", spellCategoriesLayout, spellCategoriesFields, func(f *db2.File) error {
			_, _, err := decodeCategories(f)
			return err
		}},
		{"SpellMisc", spellMiscLayout, spellMiscFields, func(f *db2.File) error {
			_, _, err := decodeMisc(f)
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.table, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), c.table+".db2")
			writeDB2(t, path, c.layout^0xffffffff, c.fields)
			f, err := db2.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			err = c.decode(f)
			if err == nil || !strings.Contains(err.Error(), c.table) {
				t.Errorf("decode = %v; want a layout error naming %s", err, c.table)
			}
		})
	}
}

func TestPublishFailureKeepsOldPair(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "spells.tsv"), []byte("old-spells"), 0o644)
	os.WriteFile(filepath.Join(dir, "names.tsv.gz"), []byte("old-names"), 0o644)
	// A directory squats on the second temp name, so its write fails after the first succeeded.
	os.Mkdir(filepath.Join(dir, "names.tsv.gz.tmp"), 0o755)
	err := publish(dir, map[string][]byte{"spells.tsv": []byte("new"), "names.tsv.gz": []byte("new")})
	if err == nil {
		t.Fatal("expected an error")
	}
	for f, want := range map[string]string{"spells.tsv": "old-spells", "names.tsv.gz": "old-names"} {
		got, _ := os.ReadFile(filepath.Join(dir, f))
		if string(got) != want {
			t.Errorf("%s = %q, want %q", f, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "spells.tsv.tmp")); err == nil {
		t.Error("temp file left behind")
	}
}
