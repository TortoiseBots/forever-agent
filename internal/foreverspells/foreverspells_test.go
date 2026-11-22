package foreverspells

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func loadTestdata(t *testing.T) *Table {
	t.Helper()
	tab, err := Load(os.DirFS("testdata"))
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func TestParseTestdata(t *testing.T) {
	tab := loadTestdata(t)
	if !tab.Loaded() {
		t.Error("a loaded table reports not loaded")
	}
	if got := tab.Source(); got != "WOWSTATIC_1_60_1_70399" {
		t.Errorf("Source = %q", got)
	}
	s, ok := tab.Lookup(205)
	if !ok || s.Name != "Frostbolt" || s.Class != "mage" || s.Rank != 2 || s.Level != 8 || s.SkillLine != 6 {
		t.Errorf("Lookup(205) = %+v, %v", s, ok)
	}
	if got := tab.IDsByName("frostbolt"); !reflect.DeepEqual(got, []uint32{116, 205, 837}) {
		t.Errorf("IDsByName(frostbolt) = %v", got)
	}
	if got := tab.ClassOf(1752); got != "rogue" {
		t.Errorf("ClassOf(1752) = %q", got)
	}
	if lvl, ok := tab.FirstRankLevel("Frostbolt"); !ok || lvl != 4 {
		t.Errorf("FirstRankLevel(Frostbolt) = %d, %v", lvl, ok)
	}
	if got := tab.Name(133); got != "Fireball" {
		t.Errorf("Name(133) = %q; 133 is only in names.tsv", got)
	}
	if _, ok := tab.Lookup(133); ok {
		t.Error("an id only in names.tsv parses as a spell row")
	}
	if !tab.HasName("Taunt") || tab.HasName("No Such Spell") {
		t.Error("HasName is wrong")
	}
	if s, ok := tab.Lookup(118); !ok || s.Mechanic != 17 {
		t.Errorf("Lookup(118) = %+v, %v; Polymorph is mechanic 17", s, ok)
	}
}

// TestRankKeptAsStored parses rows whose stored ranks run against id order (837 is stored rank 1,
// 116 stored rank 2): parse must keep the stored rank, never compute it from id order.
func TestRankKeptAsStored(t *testing.T) {
	const head = "# forever-spells v1 source=WOWSTATIC_1_60_1_70399\n" +
		"id\tname\tclass\tskill_line\trank\tlevel\trecovery_ms\tcategory_recovery_ms\tcategory\tgcd_ms\tgcd_category\tmechanic\tdispel\tattr0\n"
	spells := head +
		"116\tFrostbolt\tmage\t6\t2\t4\t0\t0\t0\t1500\t133\t0\t1\t0\n" +
		"837\tFrostbolt\tmage\t6\t1\t14\t0\t0\t0\t1500\t133\t0\t1\t0\n"
	names := "# forever-spell-names v1 source=WOWSTATIC_1_60_1_70399\nid\tname\n116\tFrostbolt\n837\tFrostbolt\n"
	tab, err := Parse(strings.NewReader(spells), strings.NewReader(names))
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := tab.Lookup(837); !ok || s.Rank != 1 {
		t.Errorf("Lookup(837) = %+v, %v; the stored rank 1 must be kept, not id order", s, ok)
	}
	if s, ok := tab.Lookup(116); !ok || s.Rank != 2 {
		t.Errorf("Lookup(116) = %+v, %v; the stored rank 2 must be kept, not id order", s, ok)
	}
}

func TestDefaultEmpty(t *testing.T) {
	var tab *Table
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Default panics: %v", r)
			}
		}()
		tab = Default()
	}()
	// The test reads the embed FS instead of hard-coding false: true after L1 commits the data.
	_, err := embeddedData.Open("data/spells.tsv")
	if want := err == nil; tab.Loaded() != want {
		t.Errorf("Loaded = %v, the embedded FS holds data/spells.tsv: %v", tab.Loaded(), want)
	}
}

func TestParseErrors(t *testing.T) {
	const (
		spellsHead = "# forever-spells v1 source=WOWSTATIC_1_60_1_70399\n" +
			"id\tname\tclass\tskill_line\trank\tlevel\trecovery_ms\tcategory_recovery_ms\tcategory\tgcd_ms\tgcd_category\tmechanic\tdispel\tattr0\n"
		// namesRow is a valid names body line: cases replace it so every bad row stays line 3.
		namesHead = "# forever-spell-names v1 source=WOWSTATIC_1_60_1_70399\nid\tname\n"
		namesRow  = "116\tFrostbolt\n"
		goodRow   = "116\tFrostbolt\tmage\t6\t1\t4\t0\t0\t0\t1500\t133\t0\t1\t0\n"
	)
	cases := []struct {
		name   string
		spells string
		names  string
		want   string // a fragment of the error
	}{
		{"spells field count", spellsHead + "116\tFrostbolt\tmage\n", namesHead + namesRow, "spells.tsv: line 3"},
		{"number out of range", spellsHead + strings.Replace(goodRow, "\t1\t4\t", "\t256\t4\t", 1), namesHead + namesRow, "spells.tsv: line 3"},
		{"id twice", spellsHead + goodRow + goodRow, namesHead + namesRow, "spells.tsv: line 4"},
		{"bad class", spellsHead + strings.Replace(goodRow, "\tmage\t", "\tdeathknight\t", 1), namesHead + namesRow, "spells.tsv: line 3"},
		{"empty name", spellsHead + strings.Replace(goodRow, "\tFrostbolt\t", "\t\t", 1), namesHead + namesRow, "spells.tsv: line 3"},
		{"bad spells header", "# bogus\n", namesHead + namesRow, "spells.tsv: line 1"},
		{"bad spells columns", spellsHead[:strings.Index(spellsHead, "\n")+1] + "id\tname\n" + goodRow, namesHead + namesRow, "spells.tsv: line 2"},
		{"rows out of order", spellsHead + strings.Replace(goodRow, "116\t", "205\t", 1) + goodRow, namesHead + namesRow, "spells.tsv: line 4"},
		{"names field count", spellsHead + goodRow, namesHead + "117\n", "names.tsv: line 3"},
		{"names id twice", spellsHead + goodRow, namesHead + namesRow + "116\tFrostbolt\n", "names.tsv: line 4"},
		{"names out of order", spellsHead + goodRow, namesHead + "205\tFrostbolt\n116\tFrostbolt\n", "names.tsv: line 4"},
		{"names number out of range", spellsHead + goodRow, namesHead + "4294967296\tBig\n", "names.tsv: line 3"},
		{"sources differ", spellsHead + goodRow, "# forever-spell-names v1 source=WOWSTATIC_0_0_0_0\nid\tname\n116\tFrostbolt\n", "names.tsv: line 1"},
		{"bad names header", spellsHead + goodRow, "# bogus\n", "names.tsv: line 1"},
	}
	for _, c := range cases {
		if _, err := Parse(strings.NewReader(c.spells), strings.NewReader(c.names)); err == nil {
			t.Errorf("%s: accepted", c.name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.want)
		}
	}
}
