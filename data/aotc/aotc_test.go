package aotc

import (
	"strings"
	"testing"
	"testing/fstest"
)

// classes are the nine classes every class-keyed file carries.
var classes = []string{"druid", "hunter", "mage", "paladin", "priest", "rogue", "shaman", "warlock", "warrior"}

// TestDefaultParsesEveryFile checks the embedded tables: Load must accept all 15 files and fill each
// table, so a file that broke at refresh is a test failure here rather than an empty rule later.
func TestDefaultParsesEveryFile(t *testing.T) {
	d := Default()
	counts := map[string]int{
		"talent-picks.csv":            len(d.TalentPicks),
		"talent-tree-points.csv":      len(d.TreePoints),
		"talent-splits-60.csv":        len(d.Splits),
		"stat-priority-60.csv":        len(d.StatPriority),
		"gear-60.csv":                 len(d.Gear),
		"enchants-60.csv":             len(d.Enchants),
		"spec-population.csv":         len(d.Specs),
		"census-classes.csv":          len(d.Classes),
		"census-races.csv":            len(d.Races),
		"census-class-gender.csv":     len(d.ClassGender),
		"census-race-class-pairs.csv": len(d.RaceClassPairs),
		"census-levels.csv":           len(d.Levels),
		"behaviour-stats.csv":         len(d.Behaviour),
		"new-forever-bosses.csv":      len(d.Bosses),
		"legacy-achievements.csv":     len(d.Achievements),
	}
	want := map[string]int{
		"talent-picks.csv":            9807,
		"talent-tree-points.csv":      567,
		"talent-splits-60.csv":        540,
		"stat-priority-60.csv":        162,
		"gear-60.csv":                 18601,
		"enchants-60.csv":             4648,
		"spec-population.csv":         27,
		"census-classes.csv":          9,
		"census-races.csv":            10,
		"census-class-gender.csv":     9,
		"census-race-class-pairs.csv": 20,
		"census-levels.csv":           51,
		"behaviour-stats.csv":         22,
		"new-forever-bosses.csv":      7,
		"legacy-achievements.csv":     65,
	}
	for file, n := range counts {
		if n != want[file] {
			t.Errorf("%s: %d rows, want %d", file, n, want[file])
		}
	}
	if len(d.Specs) != 27 {
		t.Fatalf("specs: %d, want 27", len(d.Specs))
	}
}

// TestEveryClassIsCovered is the per-class table: every class of the census appears in the class-keyed
// files, its specs sum to 100 percent, and each spec has the rows the rules read.
func TestEveryClassIsCovered(t *testing.T) {
	d := Default()
	census := map[string]ClassCensus{}
	for _, c := range d.Classes {
		census[c.Class] = c
	}
	for _, class := range classes {
		t.Run(class, func(t *testing.T) {
			if _, ok := census[class]; !ok {
				t.Fatalf("census-classes.csv has no row for %s", class)
			}
			share, n := 0.0, 0
			for _, s := range d.Specs {
				if s.Class != class {
					continue
				}
				n++
				share += s.PctOfClass60
			}
			if n == 0 {
				t.Fatalf("spec-population.csv has no spec for %s", class)
			}
			// The file rounds each share to one decimal: a class sums to 100 within its rounding.
			if share < 98.5 || share > 101.5 {
				t.Errorf("%s: spec shares sum to %.1f, want 100", class, share)
			}
			for _, s := range d.Specs {
				if s.Class != class {
					continue
				}
				t.Run(s.Spec, func(t *testing.T) {
					if len(d.CommonTalents(class, s.Spec, LevelMax, 0)) == 0 {
						t.Error("no talent picks at 60")
					}
					if len(d.StatPriorityFor(class, s.Spec)) == 0 {
						t.Error("no stat priority")
					}
					if len(d.GearFor(class, s.Spec, "")) == 0 {
						t.Error("no best geared items")
					}
					if len(d.EnchantsFor(class, s.Spec, "")) == 0 {
						t.Error("no enchants")
					}
				})
			}
		})
	}
}

// TestCommonTalentsFiltersAndSorts pins the talent accessor: the share floor is a floor (a pick just
// below it stays out) and the rows come highest share first.
func TestCommonTalentsFiltersAndSorts(t *testing.T) {
	d := Default()
	picks := d.CommonTalents("priest", "holy", LevelMax, 50)
	if len(picks) == 0 {
		t.Fatal("no priest/holy picks at 50 percent")
	}
	prev := 100.1
	for _, pick := range picks {
		if pick.Class != "priest" || pick.Spec != "holy" || pick.Levels != LevelMax {
			t.Fatalf("pick outside the query: %+v", pick)
		}
		if pick.PickPct < 50 {
			t.Fatalf("%s: share %.1f below the floor", pick.Talent, pick.PickPct)
		}
		if pick.PickPct > prev {
			t.Fatalf("%s: %.1f after %.1f, not highest first", pick.Talent, pick.PickPct, prev)
		}
		prev = pick.PickPct
	}
	all := d.CommonTalents("priest", "holy", LevelMax, 0)
	if len(all) <= len(picks) {
		t.Fatalf("share floor did not filter: %d of %d", len(picks), len(all))
	}
	if got := d.CommonTalents("priest", "holy", "20-29", 50); len(got) == 0 {
		t.Fatal("no priest/holy picks for the 20-29 group")
	}
}

// TestGearAndEnchantAccessors pins the slot accessors: best geared only, no item-set rows, the slot
// matched case-insensitively, most worn first.
func TestGearAndEnchantAccessors(t *testing.T) {
	d := Default()
	head := d.GearFor("druid", "balance", "head")
	if len(head) == 0 {
		t.Fatal("no druid/balance head items")
	}
	prev := 100.1
	for _, g := range head {
		if g.Slot != "Head" {
			t.Fatalf("item in slot %q", g.Slot)
		}
		if g.Population != PopulationBestGeared {
			t.Fatalf("population %q", g.Population)
		}
		if g.SetSlot() {
			t.Fatalf("set row in a slot list: %+v", g)
		}
		if g.Pct > prev {
			t.Fatalf("%s: %.1f after %.1f", g.Name, g.Pct, prev)
		}
		prev = g.Pct
	}
	enchants := d.EnchantsFor("druid", "balance", "Head")
	if len(enchants) == 0 {
		t.Fatal("no head enchants")
	}
	if enchants[0].Population != PopulationBestGeared {
		t.Fatalf("enchant population %q", enchants[0].Population)
	}
	if len(d.GearFor("druid", "balance", "NoSuchSlot")) != 0 {
		t.Fatal("an unknown slot returned items")
	}
}

// TestGearRows pins the gear rule's own-data accessor: every row of the spec, in both populations
// and every slot (item-set rows included), exactly as the file has them. The rule's own-data
// membership must never lose the all_60 population.
func TestGearRows(t *testing.T) {
	d := Default()
	rows := d.GearRows("shaman", "enhancement")
	var want int
	for _, g := range d.Gear {
		if g.Class == "shaman" && g.Spec == "enhancement" {
			want++
		}
	}
	if len(rows) != want {
		t.Fatalf("%d rows, want %d", len(rows), want)
	}
	pops := map[string]bool{}
	both := map[int64]map[string]bool{}
	for _, g := range rows {
		if g.Class != "shaman" || g.Spec != "enhancement" {
			t.Fatalf("row of %s/%s in the result", g.Class, g.Spec)
		}
		pops[g.Population] = true
		if both[g.ID] == nil {
			both[g.ID] = map[string]bool{}
		}
		both[g.ID][g.Population] = true
	}
	if !pops[PopulationBestGeared] || !pops[PopulationAll] {
		t.Fatalf("populations %v", pops)
	}
	if !both[19159][PopulationBestGeared] || !both[19159][PopulationAll] {
		t.Fatalf("item 19159 populations %v", both[19159])
	}
}

// TestItemIndex pins the per-item view the gear rule reads: the name, the slots and the specs.
func TestItemIndex(t *testing.T) {
	d := Default()
	info, ok := d.Item(22267)
	if !ok {
		t.Fatal("item 22267 (Spellweaver's Turban) is not indexed")
	}
	if info.Name != "Spellweaver's Turban" {
		t.Fatalf("name %q", info.Name)
	}
	if len(info.Slots) != 1 || info.Slots[0] != "Head" {
		t.Fatalf("slots %v", info.Slots)
	}
	found := false
	for _, w := range info.Wearers {
		if w.Class == "druid" && w.Spec == "balance" && w.Population == PopulationBestGeared {
			found = true
		}
	}
	if !found {
		t.Fatalf("druid/balance does not wear 22267: %v", info.Wearers)
	}
	if _, ok := d.Item(0); ok {
		t.Fatal("item 0 is indexed (a no-item sentinel is not an item)")
	}
}

// TestCreditNamesTheSource keeps the attribution the README asks for: an output that quotes the data
// carries where it came from.
func TestCreditNamesTheSource(t *testing.T) {
	if !strings.Contains(Credit, "aotc.gg") {
		t.Fatalf("Credit does not name aotc.gg: %q", Credit)
	}
}

// embeddedCopy is a mutable copy of the embedded files for the malformed-input tests.
func embeddedCopy(t *testing.T) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	entries, err := embedded.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		b, err := embedded.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = &fstest.MapFile{Data: b}
	}
	return out
}

// TestLoadRejectsMalformed is the loud-error table: a bad header, a bad number, a short row, an
// uncovered spec and a missing file each name the file (and the line where there is one).
func TestLoadRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		file string
		edit func(data []byte) []byte
		want []string
	}{
		{
			name: "header",
			file: "stat-priority-60.csv",
			edit: func(b []byte) []byte {
				return []byte(strings.Replace(string(b), "class,spec,order", "class,spec,rank", 1))
			},
			want: []string{"stat-priority-60.csv: line 1"},
		},
		{
			name: "number",
			file: "stat-priority-60.csv",
			edit: func(b []byte) []byte { return append(b, []byte("druid,balance,x,Spell Power,1,\n")...) },
			want: []string{"stat-priority-60.csv: line 164", "order"},
		},
		{
			name: "short row",
			file: "census-races.csv",
			edit: func(b []byte) []byte { return append(b, []byte("Gnome,alliance,1\n")...) },
			want: []string{"census-races.csv: line 12", "fields"},
		},
		{
			name: "percent out of range",
			file: "new-forever-bosses.csv",
			edit: func(b []byte) []byte { return append(b, []byte("Nobody,Nowhere,140\n")...) },
			want: []string{"new-forever-bosses.csv: line 9", "outside 0..100"},
		},
		{
			name: "uncovered spec",
			file: "talent-splits-60.csv",
			edit: func(b []byte) []byte { return append(b, []byte("gnome,tinker,10/0/0,1,1\n")...) },
			want: []string{"gnome/tinker", "spec-population.csv"},
		},
		{
			name: "bad wowhead id",
			file: "gear-60.csv",
			edit: func(b []byte) []byte {
				return append(b, []byte("druid,balance,best_geared,Head,Thing,22267,1,1,1\n")...)
			},
			want: []string{"gear-60.csv: line 18603", "item=<id>"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := embeddedCopy(t)
			files[tc.file] = &fstest.MapFile{Data: tc.edit(files[tc.file].Data)}
			_, err := Load(files)
			if err == nil {
				t.Fatalf("%s: Load accepted a malformed file", tc.file)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("%s: error %q does not name %q", tc.file, err, want)
				}
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		files := embeddedCopy(t)
		delete(files, "census-levels.csv")
		if _, err := Load(files); err == nil || !strings.Contains(err.Error(), "census-levels.csv") {
			t.Fatalf("missing file: %v", err)
		}
	})
}

// TestLoadTakesAnyFS keeps Load useful outside the binary: a plain fs.FS with the files at its root
// gives the same tables (the command's --dir path, a test's fixtures).
func TestLoadTakesAnyFS(t *testing.T) {
	d, err := Load(embedded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(embedded); err != nil {
		t.Fatal(err)
	}
	if len(d.SpecList()) != 27 {
		t.Fatalf("spec list: %d", len(d.SpecList()))
	}
	if !d.SpecKnown("priest", "shadow") {
		t.Fatal("priest/shadow is not known")
	}
	if d.SpecKnown("priest", "tinker") {
		t.Fatal("an invented spec is known")
	}
}
