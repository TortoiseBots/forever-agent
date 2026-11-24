package tactics

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moduleFile writes a module tactics file and returns its path.
func moduleFile(t *testing.T, js string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ai_boss_tactic.json")
	if err := os.WriteFile(path, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// testModule is the fixture the comparison tests read: map 999, boss 9001 interrupts Fire Bolt 7001 and Ice Bolt 7002
// and kills 8001 first; the avoid row on boss 9002 leaves 13 yd from Shadow Nova 7003. Entries, spells and the map are
// invented: the names are the check's words, not a server's.
const testModule = `[
 {"map":999,"boss_entry":9001,"kind":"interrupt","spell_id":7001,"comment":"Fire Bolt"},
 {"map":999,"boss_entry":9001,"kind":"interrupt","spell_id":7002,"comment":"Ice Bolt"},
 {"map":999,"boss_entry":9001,"kind":"kill_first","target_entry":8001,"priority":1},
 {"map":999,"boss_entry":9001,"kind":"behind","target_entry":0,"spell_id":7100,"param":10},
 {"map":999,"boss_entry":9002,"kind":"avoid_cast","spell_id":7003,"param":13}
]`

func loadTestModule(t *testing.T) *Module {
	t.Helper()
	m, err := LoadModule(moduleFile(t, testModule))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// testNames is the spell table the comparison tests pass: the three invented spells and nothing else.
func testNames() (nameOf func(uint32) string, idsOf func(string) []uint32) {
	names := map[uint32]string{7001: "Fire Bolt", 7002: "Ice Bolt", 7003: "Shadow Nova", 7004: "Dust Cloud"}
	byName := map[string][]uint32{}
	for id, name := range names {
		byName[strings.ToLower(name)] = append(byName[strings.ToLower(name)], id)
	}
	nameOf = func(id uint32) string { return names[id] }
	idsOf = func(name string) []uint32 { return byName[strings.ToLower(name)] }
	return nameOf, idsOf
}

func TestLoadModule(t *testing.T) {
	m := loadTestModule(t)
	if m.Len() != 5 {
		t.Fatalf("Len %d, want 5", m.Len())
	}
	if got := m.Rows(999, 9001); len(got) != 4 || got[0].Kind != KindInterrupt || got[1].Spell != 7002 || got[2].Priority != 1 || got[3].Param != 10 {
		t.Fatalf("rows of boss 9001: %+v", got)
	}
	if got := m.Rows(999, 1); len(got) != 0 {
		t.Fatalf("a boss the file does not name has rows: %+v", got)
	}
	if got := m.Rows(1, 9001); len(got) != 0 {
		t.Fatalf("a map the file does not name has rows: %+v", got)
	}
	// A row field the check does not read is not refused: the table grows columns.
	if _, err := LoadModule(moduleFile(t, `[{"map":999,"boss_entry":9001,"kind":"spread","param":12,"tactic_mode":"all","cycle_s":90}]`)); err != nil {
		t.Fatalf("a row with a column the check does not read: %v", err)
	}
}

func TestLoadModuleHostile(t *testing.T) {
	cases := []struct{ name, js, want string }{
		{"not json", `{"map":999}`, "not a JSON array"},
		{"no map", `[{"boss_entry":9001,"kind":"interrupt","spell_id":1}]`, "row 0: no map"},
		{"no boss", `[{"map":999,"kind":"interrupt","spell_id":1}]`, "row 0: no boss_entry"},
		{"no kind", `[{"map":999,"boss_entry":9001,"kind":"  "}]`, "row 0: no kind"},
		{"interrupt without spell", `[{"map":999,"boss_entry":9001,"kind":"interrupt"}]`, "row 0: an interrupt row without spell_id"},
		{"kill_first without target", `[{"map":999,"boss_entry":9001,"kind":"kill_first","priority":1}]`, "row 0: a kill_first row without target_entry"},
		{"avoid without spell", `[{"map":999,"boss_entry":9001,"kind":"avoid_cast","param":10}]`, "row 0: an avoid_cast row without spell_id"},
		{"avoid without param", `[{"map":999,"boss_entry":9001,"kind":"avoid_cast","spell_id":1}]`, "row 0: an avoid_cast row without param"},
		{"negative priority", `[{"map":999,"boss_entry":9001,"kind":"interrupt","spell_id":1,"priority":-1}]`, "row 0: a negative priority"},
		{"map a string", `[{"map":"999","boss_entry":9001,"kind":"interrupt","spell_id":1}]`, "row 0"},
		{"spell negative", `[{"map":999,"boss_entry":9001,"kind":"interrupt","spell_id":-4}]`, "row 0"},
		{"second row", `[{"map":999,"boss_entry":9001,"kind":"interrupt","spell_id":1},{"map":999,"boss_entry":9001,"kind":"interrupt","spell_id":1,"comment":"again"}]`, "row 1: a second row"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadModule(moduleFile(t, c.js))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
			if err != nil && !strings.Contains(err.Error(), "ai_boss_tactic.json") {
				t.Fatalf("err %v does not name the file", err)
			}
		})
	}
	if _, err := LoadModule(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("a missing file loaded")
	}
}

func TestLoadModuleCaps(t *testing.T) {
	var b strings.Builder
	b.WriteString("[")
	for i := range MaxModuleRows + 1 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"map":999,"boss_entry":%d,"kind":"interrupt","spell_id":1}`, i+1)
	}
	b.WriteString("]")
	if _, err := LoadModule(moduleFile(t, b.String())); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("rows past the cap: %v", err)
	}
	big := "[" + strings.Repeat(" ", MaxModuleBytes) + "]"
	if _, err := LoadModule(moduleFile(t, big)); err == nil || !strings.Contains(err.Error(), "past the") {
		t.Fatalf("bytes past the cap: %v", err)
	}
}

func TestCompareAgreeingPlan(t *testing.T) {
	nameOf, idsOf := testNames()
	rep := Compare(loadTestModule(t), []BossBlock{{
		Leg: 2, LegName: "hall", Map: 999, Entry: 9001,
		Tactics: &Tactics{
			Interrupt: []SpellRef{{ID: 7001}, {Name: "Ice Bolt"}},
			KillFirst: []uint32{8001},
		},
	}}, nameOf, idsOf)
	if len(rep.Conflicts) != 0 || len(rep.Unchecked) != 0 || rep.Steps != 3 || rep.Bosses != 1 {
		t.Fatalf("an agreeing plan: %+v", rep)
	}
}

func TestCompareInterruptNotInModule(t *testing.T) {
	nameOf, idsOf := testNames()
	rep := Compare(loadTestModule(t), []BossBlock{{
		Leg: 1, LegName: "revelosh", Map: 999, Entry: 9001,
		Tactics: &Tactics{Interrupt: []SpellRef{{ID: 7001}, {Name: "Dust Cloud"}}},
	}}, nameOf, idsOf)
	if len(rep.Conflicts) != 1 || rep.Steps != 2 {
		t.Fatalf("one extra interrupt: %+v", rep)
	}
	line := rep.Conflicts[0].String()
	for _, want := range []string{`leg 1 "revelosh" boss 9001`, `interrupt "Dust Cloud"`, "the module's rows interrupt Fire Bolt (7001), Ice Bolt (7002) only"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q has no %q", line, want)
		}
	}
}

// TestComparePartialBlock: a plan block that speaks of one axis only contradicts nothing on the others: the module's
// kill_first row is not a plan step the plan got wrong.
func TestComparePartialBlock(t *testing.T) {
	nameOf, idsOf := testNames()
	rep := Compare(loadTestModule(t), []BossBlock{{
		Leg: 0, LegName: "gate", Map: 999, Entry: 9001,
		Tactics: &Tactics{Interrupt: []SpellRef{{ID: 7001}}},
	}}, nameOf, idsOf)
	if len(rep.Conflicts) != 0 || rep.Steps != 1 || len(rep.Unchecked) != 0 {
		t.Fatalf("a one-axis block: %+v", rep)
	}
}

func TestCompareKillOrder(t *testing.T) {
	nameOf, idsOf := testNames()
	m := &Module{byMap: map[uint32]map[uint32][]ModuleRow{999: {9001: {
		{Map: 999, Boss: 9001, Kind: KindKillFirst, Target: 8001, Priority: 1},
		{Map: 999, Boss: 9001, Kind: KindKillFirst, Target: 8002, Priority: 2},
	}}}, rows: 2}
	rep := Compare(m, []BossBlock{{
		Leg: 3, LegName: "dwarves", Map: 999, Entry: 9001,
		Tactics: &Tactics{KillFirst: []uint32{8002, 8001}},
	}}, nameOf, idsOf)
	if len(rep.Conflicts) != 1 || rep.Steps != 2 {
		t.Fatalf("reversed kill order: %+v", rep)
	}
	line := rep.Conflicts[0].String()
	for _, want := range []string{"kill_first 8002, 8001", "the module's priorities kill 8001 (1) before 8002 (2)"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q has no %q", line, want)
		}
	}
}

func TestCompareNeverTarget(t *testing.T) {
	nameOf, idsOf := testNames()
	rep := Compare(loadTestModule(t), []BossBlock{{
		Leg: 0, LegName: "sentinel", Map: 999, Entry: 9001,
		Tactics: &Tactics{NeverTarget: []uint32{8001}},
	}}, nameOf, idsOf)
	if len(rep.Conflicts) != 1 || rep.Steps != 1 {
		t.Fatalf("never_target against a kill_first row: %+v", rep)
	}
	line := rep.Conflicts[0].String()
	for _, want := range []string{"never_target 8001", "the module kills it first"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q has no %q", line, want)
		}
	}
}

func TestCompareMoveOut(t *testing.T) {
	nameOf, idsOf := testNames()
	// The radius the plan leaves against the radius the module's row leaves.
	rep := Compare(loadTestModule(t), []BossBlock{{
		Leg: 4, LegName: "bat", Map: 999, Entry: 9002,
		Tactics: &Tactics{MoveOut: []MoveOut{{Cast: &SpellRef{ID: 7003}, Yards: 8}}},
	}}, nameOf, idsOf)
	if len(rep.Conflicts) != 1 || rep.Steps != 1 {
		t.Fatalf("a plan radius under the module's: %+v", rep)
	}
	if got := rep.Conflicts[0].String(); !strings.Contains(got, "the plan leaves 8 yd; the module's avoid_cast row leaves 13 yd") {
		t.Fatalf("radius line: %s", got)
	}
	// A plan step the module has no avoid row for: the step is not run by the module.
	rep = Compare(loadTestModule(t), []BossBlock{{
		Leg: 5, LegName: "void", Map: 999, Entry: 9002,
		Tactics: &Tactics{MoveOut: []MoveOut{{Cast: &SpellRef{Name: "Dust Cloud"}, Yards: 6}}},
	}}, nameOf, idsOf)
	if len(rep.Conflicts) != 1 || rep.Steps != 1 {
		t.Fatalf("a plan step with no module row: %+v", rep)
	}
	if got := rep.Conflicts[0].String(); !strings.Contains(got, `move_out "Dust Cloud": the module's rows have no avoid_cast row for it`) {
		t.Fatalf("missing avoid row line: %s", got)
	}
	// A ground effect has no module row at all: not checked, never passed.
	rep = Compare(loadTestModule(t), []BossBlock{{
		Leg: 4, LegName: "bat", Map: 999, Entry: 9002,
		Tactics: &Tactics{MoveOut: []MoveOut{{Ground: &SpellRef{ID: 7003}}}},
	}}, nameOf, idsOf)
	if len(rep.Unchecked) != 1 || len(rep.Conflicts) != 0 || !strings.Contains(rep.Unchecked[0], "ground-effect") || !rep.NotChecked() {
		t.Fatalf("a ground rule: %+v", rep)
	}
}

// TestCompareBossWithoutRows: a boss the module's file does not name is not checked, and the report says so: an empty
// row set is never a pass.
func TestCompareBossWithoutRows(t *testing.T) {
	nameOf, idsOf := testNames()
	rep := Compare(loadTestModule(t), []BossBlock{{
		Leg: 0, LegName: "no_rows", Map: 999, Entry: 9003,
		Tactics: &Tactics{Interrupt: []SpellRef{{ID: 7001}}},
	}}, nameOf, idsOf)
	if len(rep.Conflicts) != 0 || rep.Steps != 0 || rep.Bosses != 0 || len(rep.Unchecked) != 1 {
		t.Fatalf("a boss without module rows: %+v", rep)
	}
	if !strings.Contains(rep.Unchecked[0], `leg 0 "no_rows" boss 9003`) || !strings.Contains(rep.Unchecked[0], "no tactics rows") {
		t.Fatalf("unchecked line %q", rep.Unchecked[0])
	}
	if !rep.NotChecked() {
		t.Fatal("a report with nothing checked reads as checked")
	}
}

// TestCompareWithoutSpellData: a plan step named by name and no spell table to resolve it is not checked, never a pass.
func TestCompareWithoutSpellData(t *testing.T) {
	rep := Compare(loadTestModule(t), []BossBlock{{
		Leg: 0, LegName: "gate", Map: 999, Entry: 9001,
		Tactics: &Tactics{Interrupt: []SpellRef{{Name: "Fire Bolt"}}},
	}}, nil, nil)
	if len(rep.Conflicts) != 0 || rep.Steps != 0 || len(rep.Unchecked) != 1 {
		t.Fatalf("a name without spell data: %+v", rep)
	}
	if !strings.Contains(rep.Unchecked[0], "no spell data") {
		t.Fatalf("unchecked line %q", rep.Unchecked[0])
	}
}

// TestCompareDispelNotChecked: the module's table has no dispel row the check reads, so a plan's dispel step is listed
// as not checked beside the rows it could judge.
func TestCompareDispelNotChecked(t *testing.T) {
	nameOf, idsOf := testNames()
	rep := Compare(loadTestModule(t), []BossBlock{{
		Leg: 0, LegName: "gate", Map: 999, Entry: 9001,
		Tactics: &Tactics{Interrupt: []SpellRef{{ID: 7001}}, Dispel: []SpellRef{{ID: 7002}}},
	}}, nameOf, idsOf)
	if len(rep.Conflicts) != 0 || rep.Steps != 1 || len(rep.Unchecked) != 1 {
		t.Fatalf("a dispel step: %+v", rep)
	}
	if !strings.Contains(rep.Unchecked[0], "dispel 7002") || !strings.Contains(rep.Unchecked[0], "no dispel row") {
		t.Fatalf("unchecked line %q", rep.Unchecked[0])
	}
	if rep.NotChecked() {
		t.Fatal("one judged step beside one unchecked step reads as wholly unchecked")
	}
}

// TestCompareEmptyPlanBlock: a boss with an empty tactics block is no fight step at all: it has nothing to contradict.
func TestCompareEmptyPlanBlock(t *testing.T) {
	nameOf, idsOf := testNames()
	rep := Compare(loadTestModule(t), []BossBlock{{
		Leg: 0, LegName: "empty", Map: 999, Entry: 9001, Tactics: &Tactics{},
	}}, nameOf, idsOf)
	if rep.Steps != 0 || len(rep.Conflicts) != 0 || len(rep.Unchecked) != 0 || rep.Bosses != 0 {
		t.Fatalf("an empty block: %+v", rep)
	}
}
