package planexport

import (
	"math"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

func loadRFC(t *testing.T) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Load("../../dungeons/ragefire_chasm.json")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func buildRFC(t *testing.T) ForeverReport {
	t.Helper()
	rep, err := BuildForever(loadRFC(t), Options{Source: "forever-agent dungeons/ragefire_chasm.json"})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// The golden file is the RFC export as the tool writes it. Its first 30 rows were read line by line against the plan.
func TestForeverRagefireGolden(t *testing.T) {
	rep := buildRFC(t)
	got := WriteForeverSQL([]ForeverReport{rep})
	want, err := os.ReadFile("testdata/forever/ragefire_chasm.sql")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		_, a := firstLineDiff(string(want), string(got))
		t.Fatalf("the export differs from the golden file at line %d:\n  golden: %q\n  export: %q", a.line, a.want, a.got)
	}
	if n := len(rep.Rows.Legs); n != 9 {
		t.Fatalf("%d legs, want 9", n)
	}
	if n := len(rep.Rows.Steps); n != 74 {
		t.Fatalf("%d steps, want 74", n)
	}
	if n := len(rep.Units); n != 103 {
		t.Fatalf("%d unit rows, want 103 (93 pack + 4 boss + 6 patrol)", n)
	}
	if n := len(rep.Bosses); n != 4 {
		t.Fatalf("%d boss rows, want 4", n)
	}
	if len(rep.Refused) != 0 {
		t.Fatalf("the first dungeon must export, refused %v", rep.Refused)
	}
}

type diffLine struct {
	line      int
	want, got string
}

func firstLineDiff(want, got string) (int, diffLine) {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		w, g := "", ""
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return i + 1, diffLine{line: i + 1, want: w, got: g}
		}
	}
	return 0, diffLine{}
}

func TestForeverDeterministic(t *testing.T) {
	a := WriteForeverSQL([]ForeverReport{buildRFC(t)})
	for i := 0; i < 3; i++ {
		if b := WriteForeverSQL([]ForeverReport{buildRFC(t)}); a != b {
			t.Fatalf("export %d differs from the first", i)
		}
	}
}

func TestForeverUnitRowsFromThePlan(t *testing.T) {
	p := loadRFC(t)
	rep := buildRFC(t)
	var want []UnitRow
	perRole := map[string]int{}
	for li, l := range p.Legs {
		rowLeg, ok := rep.Rows.LegNumber(p, li)
		if !ok {
			continue
		}
		var steps []StepRow
		for _, s := range rep.Rows.Steps {
			if s.Leg == rowLeg {
				steps = append(steps, s)
			}
		}
		pi := 0
		for _, s := range steps {
			switch s.Kind {
			case KindPull:
				if pi >= len(l.PullGroups) {
					t.Fatalf("leg %d: more pull rows than plan groups", li)
				}
				g := l.PullGroups[pi]
				pi++
				n := max(len(g.Entries), len(g.Positions))
				for k := 0; k < n; k++ {
					e := g.Entries[min(k, len(g.Entries)-1)]
					at := g.Positions[min(k, len(g.Positions)-1)]
					want = append(want, UnitRow{Plan: rep.Rows.Plan.Plan, Leg: rowLeg, Seq: s.Seq, Idx: k, Entry: e, X: at.X, Y: at.Y, Z: at.Z, Role: UnitPack})
					perRole[UnitPack]++
				}
				if g.Patrol != nil {
					for j, e := range g.Patrol.Entries {
						at := g.Patrol.Path[0]
						want = append(want, UnitRow{Plan: rep.Rows.Plan.Plan, Leg: rowLeg, Seq: s.Seq, Idx: n + j, Entry: e, X: at.X, Y: at.Y, Z: at.Z, Role: UnitPatrol})
						perRole[UnitPatrol]++
					}
				}
			case KindBoss:
				b := l.Boss
				want = append(want, UnitRow{Plan: rep.Rows.Plan.Plan, Leg: rowLeg, Seq: s.Seq, Idx: 0, Entry: b.Entry, X: b.Position.X, Y: b.Position.Y, Z: b.Position.Z, Role: UnitBoss})
				perRole[UnitBoss]++
				for j, e := range b.First {
					want = append(want, UnitRow{Plan: rep.Rows.Plan.Plan, Leg: rowLeg, Seq: s.Seq, Idx: 1 + j, Entry: e, X: b.Position.X, Y: b.Position.Y, Z: b.Position.Z, Role: UnitFirst})
					perRole[UnitFirst]++
				}
			}
		}
	}
	if len(rep.Units) != len(want) {
		t.Fatalf("%d unit rows, the plan gives %d", len(rep.Units), len(want))
	}
	for i := range want {
		if rep.Units[i] != want[i] {
			t.Fatalf("unit row %d: %+v, the plan gives %+v", i, rep.Units[i], want[i])
		}
	}
	if perRole[UnitPack] != 93 || perRole[UnitBoss] != 4 || perRole[UnitPatrol] != 6 || perRole[UnitFirst] != 0 {
		t.Fatalf("roles %v", perRole)
	}
}

// A boss.first add stands at the boss's point with its own role, and a patrol unit stands at the first path point, so the
// reader knows to search wider than for a pack member.
func TestForeverFirstAddAndPatrolRoles(t *testing.T) {
	p := &dungeon.Plan{
		Version: 1, Name: "roles", Status: "live-checked", Map: 1,
		Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name: "a", To: dungeon.Point{X: 1},
			PullGroups: []dungeon.PullGroup{{
				Entries: []uint32{2}, Positions: []dungeon.Point{{X: 2}},
				Patrol: &dungeon.Patrol{Entries: []uint32{3}, Path: []dungeon.Point{{X: 3}, {X: 4}}},
			}},
			Boss: &dungeon.Boss{Entry: 4, Position: dungeon.Point{X: 5}, First: []uint32{6, 7}},
		}},
	}
	rep, err := BuildForever(p, Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	byEntry := map[uint32]UnitRow{}
	for _, u := range rep.Units {
		byEntry[u.Entry] = u
	}
	for _, c := range []struct {
		entry uint32
		role  string
		idx   int
		x     float32
	}{
		{2, UnitPack, 0, 2},
		{3, UnitPatrol, 1, 3}, // the first path point, not the second
		{4, UnitBoss, 0, 5},
		{6, UnitFirst, 1, 5}, // at the boss's point
		{7, UnitFirst, 2, 5},
	} {
		u, ok := byEntry[c.entry]
		if !ok {
			t.Fatalf("entry %d has no unit row: %+v", c.entry, rep.Units)
		}
		if u.Role != c.role || u.Idx != c.idx || u.X != c.x {
			t.Errorf("entry %d: role %q idx %d x %v, want %q %d %v", c.entry, u.Role, u.Idx, u.X, c.role, c.idx, c.x)
		}
	}
	found := false
	for _, w := range rep.Warnings {
		if strings.Contains(w, "role=first") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no first-add warning names the role: %v", rep.Warnings)
	}
}

// Each field the Forever format cannot carry is refused by its JSON path.
func TestForeverRefusals(t *testing.T) {
	base := func() *dungeon.Plan {
		return &dungeon.Plan{
			Version: 1, Name: "cave", Status: "live-checked", Map: 70,
			Entrance: dungeon.Point{X: 0, Y: 0, Z: 0},
			Legs: []dungeon.Leg{{
				Name: "a", To: dungeon.Point{X: 1, Y: 0, Z: 0},
				Boss: &dungeon.Boss{Entry: 1, Position: dungeon.Point{X: 5, Y: 0, Z: 0}},
			}},
		}
	}
	cases := []struct {
		name  string
		plan  func(*dungeon.Plan)
		field string
	}{
		{"gossip step", func(p *dungeon.Plan) {
			p.Legs[0].Steps = []dungeon.Step{{Gossip: &dungeon.Gossip{NPC: 1}}}
		}, "legs[0].steps[0].gossip"},
		{"use_object step", func(p *dungeon.Plan) {
			p.Legs[0].Steps = []dungeon.Step{{UseObject: &dungeon.UseObject{}}}
		}, "legs[0].steps[0].use_object"},
		{"pull hold", func(p *dungeon.Plan) {
			p.Legs[0].PullGroups = []dungeon.PullGroup{{Entries: []uint32{2}, Positions: []dungeon.Point{{X: 2}}, Hold: &dungeon.Hold{Data: 1}}}
		}, "legs[0].pull_groups[0].hold"},
		{"boss loot", func(p *dungeon.Plan) {
			p.Legs[0].Boss.LootItem = &dungeon.ItemRef{Entry: 2}
		}, "legs[0].boss.loot_item"},
		{"phase entries", func(p *dungeon.Plan) {
			p.Legs[0].Boss.PhaseEntries = []uint32{3}
		}, "legs[0].boss.phase_entries"},
		{"leftover entries", func(p *dungeon.Plan) {
			p.Legs[0].LeftoverEntries = []uint32{4}
		}, "legs[0].leftover_entries"},
		{"gm cross", func(p *dungeon.Plan) {
			p.Legs[0].Via = []dungeon.Waypoint{{Point: dungeon.Point{X: 2}, GMCross: true}}
		}, "legs[0].via[0].gm_cross"},
		{"raid kind", func(p *dungeon.Plan) {
			p.Kind = dungeon.KindRaid
		}, "kind"},
		{"raid block", func(p *dungeon.Plan) {
			p.Raid = &dungeon.RaidSpec{Size: 10}
		}, "raid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base()
			tc.plan(p)
			rep, err := BuildForever(p, Options{})
			if err == nil {
				t.Fatalf("exported, want a refusal of %s", tc.field)
			}
			found := false
			for _, r := range rep.Refused {
				if r.Field == tc.field {
					found = true
				}
			}
			if !found || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("refused %v, error %v; want %s", rep.Refused, err, tc.field)
			}
		})
	}
}

// Every string is a SQL literal that reads back byte for byte: a quote, a backslash, a newline and non-ASCII included.
func TestForeverEscaping(t *testing.T) {
	text := "q'uo\\te\nline two, café"
	rep := ForeverReport{
		Rows: Rows{
			Plan: PlanRow{Plan: text, Map: 1, Source: "src " + text, Comment: "comment " + text},
			Legs: []LegRow{{Plan: text, Leg: 0, Name: text, Comment: text}},
			Steps: []StepRow{{
				Plan: text, Leg: 0, Seq: 0, Kind: KindMove, X: -1.5, Y: 2.25, Z: -3, Comment: text,
			}},
		},
		Units:  []UnitRow{{Plan: text, Leg: 0, Seq: 0, Idx: 0, Entry: 7, X: 1, Y: 2.5, Z: 3, Role: UnitPack, Comment: text}},
		Bosses: []BossRow{{Map: 1, Entry: 7, Name: text}},
	}
	sql := WriteForeverSQL([]ForeverReport{rep})
	for _, want := range []string{`\n`, `\\`, `''`, "café"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("the SQL lacks %q:\n%s", want, sql)
		}
	}
	back, err := ParseForever(sql)
	if err != nil {
		t.Fatal(err)
	}
	if err := EqualForever([]ForeverReport{rep}, back); err != nil {
		t.Fatal(err)
	}
	if got := back[0].Rows.Plan.Plan; got != text {
		t.Fatalf("plan name read back %q, want %q", got, text)
	}
	if got := back[0].Units[0].Comment; got != text {
		t.Fatalf("unit comment read back %q, want %q", got, text)
	}
}

// The float columns are fixed-point, three decimals, with no locale separator and no exponent.
func TestForeverNumericFormat(t *testing.T) {
	sql := WriteForeverSQL([]ForeverReport{buildRFC(t)})
	if !strings.Contains(sql, "-15.990, -50.170, -21.780, 11.000,") {
		t.Fatal("the pull row is not written with three decimals")
	}
	for _, bad := range []string{"-50.169998", "e+0", "e-0", "NaN", "Inf"} {
		if strings.Contains(sql, bad) {
			t.Fatalf("the SQL holds %q", bad)
		}
	}
	if regexp.MustCompile(`\d,\d{3}\.\d{3}`).MatchString(sql) {
		t.Fatal("a float column has a thousands separator")
	}
}

func TestForeverRoundTrip(t *testing.T) {
	rep := buildRFC(t)
	sql := WriteForeverSQL([]ForeverReport{rep})
	back, err := ParseForever(sql)
	if err != nil {
		t.Fatal(err)
	}
	if err := EqualForever([]ForeverReport{rep}, back); err != nil {
		t.Fatal(err)
	}
}

// A unit position that needs more than the three decimals the columns carry (Kresh's X -3.64081, written -3.641) still
// reads back: the check allows the column's precision, like the step rows. A real mismatch (a moved unit, a wrong
func TestForeverUnitRoundTripPrecision(t *testing.T) {
	mk := func(x float32, entry uint32) ForeverReport {
		return ForeverReport{
			Rows:  Rows{Plan: PlanRow{Plan: "t", Map: 43}, Legs: []LegRow{{Plan: "t", Leg: 0, Name: "kresh"}}},
			Units: []UnitRow{{Plan: "t", Leg: 3, Seq: 1, Idx: 0, Entry: entry, X: x, Y: 229.268, Z: -106.189, Role: UnitBoss}},
		}
	}
	want := []ForeverReport{mk(-3.64081, 3653)}
	sql := WriteForeverSQL(want)
	if !strings.Contains(sql, "-3.641") {
		t.Fatalf("the column does not hold -3.641:\n%s", sql)
	}
	back, err := ParseForever(sql)
	if err != nil {
		t.Fatal(err)
	}
	if err := EqualForever(want, back); err != nil {
		t.Fatalf("a value that round-trips within the column's precision fails: %v", err)
	}
	moved := []ForeverReport{mk(-3.65, 3653)}
	movedBack, err := ParseForever(WriteForeverSQL(moved))
	if err != nil {
		t.Fatal(err)
	}
	movedBack[0].Units[0].X = -3.64
	if err := EqualForever(moved, movedBack); err == nil {
		t.Fatal("a unit moved by a hundredth of a yard reads back equal")
	}
	wrongEntry := []ForeverReport{mk(-3.64081, 3654)}
	wrongBack, err := ParseForever(WriteForeverSQL([]ForeverReport{mk(-3.64081, 3653)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := EqualForever(wrongEntry, wrongBack); err == nil {
		t.Fatal("a unit with the wrong entry reads back equal")
	}
}

// No row carries a spawn id: a pack is its entries and the members' positions.
func TestForeverNoSpawnIDs(t *testing.T) {
	sql := strings.ToLower(WriteForeverSQL([]ForeverReport{buildRFC(t)}))
	if strings.Contains(sql, "guid") {
		t.Fatal("the SQL mentions a guid")
	}
	if strings.Contains(sql, "spawn_id") || strings.Contains(sql, "`spawn`") {
		t.Fatal("the SQL has a spawn id column")
	}
	// The unit table's columns are the ten the module reads.
	if !strings.Contains(sql, "(`plan`, `leg`, `seq`, `idx`, `entry`, `x`, `y`, `z`, `role`, `comment`)") {
		t.Fatal("the unit table's column list changed")
	}
}

// The float columns never take exponent form and never write a negative zero: a value that rounds to zero is 0.000.
func TestForeverNumericFormatEdgeCases(t *testing.T) {
	for _, c := range []struct {
		in   float32
		want string
	}{
		{-0.004, "-0.004"},
		{-1e-7, "0.000"},
		{1e-7, "0.000"},
		{-0.0004, "0.000"},
		{math.Float32frombits(0x80000000), "0.000"}, // -0.0
	} {
		if got := foreverNum(c.in); got != c.want {
			t.Errorf("%v: %q, want %q", c.in, got, c.want)
		}
	}
	for _, v := range []float32{1e20, -1e20, 123456789} {
		if s := foreverNum(v); strings.ContainsAny(s, "eE") || s == "" || s[0] == '-' && strings.Trim(s[1:], "0.") == "" {
			t.Errorf("%v: %q is exponent form or a negative zero", v, s)
		}
	}
}

// foreverState is the rows of one plan in the export, by key, for the shrink test.
type foreverState struct {
	legs   map[int]bool
	steps  map[[2]int]bool
	units  map[[3]int]bool
	bosses map[uint32]bool
}

func stateOf(rep ForeverReport) foreverState {
	s := foreverState{legs: map[int]bool{}, steps: map[[2]int]bool{}, units: map[[3]int]bool{}, bosses: map[uint32]bool{}}
	for _, l := range rep.Rows.Legs {
		s.legs[l.Leg] = true
	}
	for _, st := range rep.Rows.Steps {
		s.steps[[2]int{st.Leg, st.Seq}] = true
	}
	for _, u := range rep.Units {
		s.units[[3]int{u.Leg, u.Seq, u.Idx}] = true
	}
	for _, b := range rep.Bosses {
		s.bosses[b.Entry] = true
	}
	return s
}

func (s *foreverState) clear(table string) {
	switch table {
	case "ai_dungeon_leg":
		s.legs = map[int]bool{}
	case "ai_dungeon_step":
		s.steps = map[[2]int]bool{}
	case "ai_dungeon_step_unit":
		s.units = map[[3]int]bool{}
	}
}

func (s foreverState) add(o foreverState) {
	for k := range o.legs {
		s.legs[k] = true
	}
	for k := range o.steps {
		s.steps[k] = true
	}
	for k := range o.units {
		s.units[k] = true
	}
	for k := range o.bosses {
		s.bosses[k] = true
	}
}

func (s foreverState) equal(o foreverState) bool {
	for k := range s.legs {
		if !o.legs[k] {
			return false
		}
	}
	for k := range s.steps {
		if !o.steps[k] {
			return false
		}
	}
	for k := range s.units {
		if !o.units[k] {
			return false
		}
	}
	for k := range s.bosses {
		if !o.bosses[k] {
			return false
		}
	}
	return len(s.legs) == len(o.legs) && len(s.steps) == len(o.steps) && len(s.units) == len(o.units) && len(s.bosses) == len(o.bosses)
}

// deletedTables reads the DELETE statements of a migration and checks their scope: every delete names the plan, and none
// touches the shared boss table. The order they are returned in is the file's order.
func deletedTables(t *testing.T, sql, plan string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(sql, "\n") {
		if !strings.HasPrefix(line, "DELETE FROM `") {
			continue
		}
		i := strings.Index(line, "`")
		j := strings.Index(line[i+1:], "`")
		table := line[i+1 : i+1+j]
		if table == "ai_dungeon_boss" {
			t.Fatalf("the export deletes from the shared boss table: %s", line)
		}
		if !strings.Contains(line, "WHERE `plan` = '"+plan+"';") {
			t.Fatalf("%s does not delete the plan's rows", line)
		}
		out = append(out, table)
	}
	return out
}

// A plan that shrinks takes its old plan rows with it: the migration deletes the plan's rows child first inside one
// transaction, and a smaller export of the same plan, applied after the larger one, leaves none of the larger one's plan
// rows. Boss rows are shared with every plan on the map, so they are upserted and survive.
func TestForeverShrinkingPlanRemovesOldRows(t *testing.T) {
	large := ForeverReport{
		Rows: Rows{
			Plan: PlanRow{Plan: "shrink", Map: 389, Source: "s", Comment: "c"},
			Legs: []LegRow{{Plan: "shrink", Leg: 0, Name: "a"}, {Plan: "shrink", Leg: 1, Name: "b"}, {Plan: "shrink", Leg: 2, Name: "c"}},
			Steps: []StepRow{
				{Plan: "shrink", Leg: 0, Seq: 0, Kind: KindMove},
				{Plan: "shrink", Leg: 0, Seq: 1, Kind: KindPull},
				{Plan: "shrink", Leg: 1, Seq: 0, Kind: KindMove},
				{Plan: "shrink", Leg: 1, Seq: 1, Kind: KindPull},
				{Plan: "shrink", Leg: 2, Seq: 0, Kind: KindBoss},
			},
		},
		Units: []UnitRow{
			{Plan: "shrink", Leg: 0, Seq: 1, Idx: 0, Entry: 10, Role: UnitPack},
			{Plan: "shrink", Leg: 0, Seq: 1, Idx: 1, Entry: 10, Role: UnitPack},
			{Plan: "shrink", Leg: 1, Seq: 1, Idx: 0, Entry: 11, Role: UnitPack},
			{Plan: "shrink", Leg: 2, Seq: 0, Idx: 0, Entry: 12, Role: UnitBoss},
		},
		Bosses: []BossRow{{Map: 389, Entry: 12}, {Map: 389, Entry: 13}},
	}
	small := ForeverReport{
		Rows: Rows{
			Plan: PlanRow{Plan: "shrink", Map: 389, Source: "s", Comment: "c"},
			Legs: []LegRow{{Plan: "shrink", Leg: 0, Name: "a"}},
			Steps: []StepRow{
				{Plan: "shrink", Leg: 0, Seq: 0, Kind: KindMove},
				{Plan: "shrink", Leg: 0, Seq: 1, Kind: KindPull},
			},
		},
		Units:  []UnitRow{{Plan: "shrink", Leg: 0, Seq: 1, Idx: 0, Entry: 10, Role: UnitPack}},
		Bosses: []BossRow{{Map: 389, Entry: 12}},
	}
	bigSQL := WriteForeverSQL([]ForeverReport{large})
	smallSQL := WriteForeverSQL([]ForeverReport{small})
	bigBack, err := ParseForever(bigSQL)
	if err != nil {
		t.Fatal(err)
	}
	smallBack, err := ParseForever(smallSQL)
	if err != nil {
		t.Fatal(err)
	}
	got := stateOf(bigBack[0])
	if len(got.legs) != 3 || len(got.steps) != 5 || len(got.units) != 4 || len(got.bosses) != 2 {
		t.Fatalf("the large export reads back %d legs, %d steps, %d units, %d bosses", len(got.legs), len(got.steps), len(got.units), len(got.bosses))
	}
	wantDel := []string{"ai_dungeon_step_unit", "ai_dungeon_step", "ai_dungeon_leg", "ai_dungeon_plan"}
	del := deletedTables(t, smallSQL, "shrink")
	if strings.Join(del, ",") != strings.Join(wantDel, ",") {
		t.Fatalf("the deletes are %v, want child first %v", del, wantDel)
	}
	if n := strings.Count(smallSQL, "START TRANSACTION;"); n != 1 {
		t.Fatalf("%d START TRANSACTION for one plan", n)
	}
	if n := strings.Count(smallSQL, "COMMIT;"); n != 1 {
		t.Fatalf("%d COMMIT for one plan", n)
	}
	// Apply the smaller migration: only the plan tables its own DELETEs name are cleared, then its rows are inserted.
	for _, table := range del {
		got.clear(table)
	}
	got.add(stateOf(smallBack[0]))
	want := stateOf(smallBack[0])
	want.bosses[13] = true // the larger export's second boss row stays: boss rows are not the plan's
	if !got.equal(want) {
		t.Fatalf("after the smaller export the rows are %+v, want %+v", got, want)
	}
	if got.legs[1] || got.steps[[2]int{1, 0}] || got.units[[3]int{1, 1, 0}] {
		t.Fatal("a plan row of the larger export survived the smaller one")
	}
	if !got.bosses[12] || !got.bosses[13] {
		t.Fatal("the smaller export removed a boss row")
	}
}

// The clause every boss statement ends with: keep an existing row's name unless the plan supplies a non-empty one.
const bossUpsertClause = "ON DUPLICATE KEY UPDATE `name` = IF(VALUES(`name`) <> '', VALUES(`name`), `name`);"

// bossNames applies a migration to a boss table keyed by map and entry the way the upsert the export writes does: a new
// key inserts its row, an existing row's name changes only when the migration carries a non-empty name. It also checks
// that the migration is that upsert and that nothing in it deletes or replaces shared boss rows.
func bossNames(t *testing.T, names map[[2]uint32]string, sql string) {
	t.Helper()
	if strings.Contains(sql, "DELETE FROM `ai_dungeon_boss`") {
		t.Fatalf("a migration deletes shared boss rows:\n%s", sql)
	}
	if strings.Contains(sql, "REPLACE INTO `ai_dungeon_boss`") {
		t.Fatalf("a migration replaces shared boss rows:\n%s", sql)
	}
	if n := strings.Count(sql, bossUpsertClause); n > 1 {
		t.Fatalf("%d guarded boss upserts in one migration", n)
	}
	reps, err := ParseForever(sql)
	if err != nil {
		t.Fatal(err)
	}
	for _, rep := range reps {
		for _, bo := range rep.Bosses {
			k := [2]uint32{bo.Map, bo.Entry}
			if _, ok := names[k]; !ok || bo.Name != "" {
				names[k] = bo.Name
			}
		}
	}
}

// The boss table is shared by every plan on a map: applying two plans' exports in either order keeps both plans' boss
// rows, because the export upserts by map and entry and never deletes from that table.
func TestForeverTwoPlansOnOneMapKeepBothBossRows(t *testing.T) {
	plan := func(name string, entries ...uint32) ForeverReport {
		rep := ForeverReport{Rows: Rows{Plan: PlanRow{Plan: name, Map: 389}}}
		for _, e := range entries {
			rep.Bosses = append(rep.Bosses, BossRow{Map: 389, Entry: e})
		}
		return rep
	}
	a, b := plan("north", 11, 12), plan("south", 13, 14)
	sa, sb := WriteForeverSQL([]ForeverReport{a}), WriteForeverSQL([]ForeverReport{b})
	for _, s := range []struct{ plan, sql string }{{"north", sa}, {"south", sb}} {
		if del := deletedTables(t, s.sql, s.plan); len(del) != 4 {
			t.Fatalf("%s deletes %v, want the four plan tables", s.plan, del)
		}
	}
	for name, order := range map[string][]string{"north then south": {sa, sb}, "south then north": {sb, sa}} {
		names := map[[2]uint32]string{}
		for _, sql := range order {
			bossNames(t, names, sql)
		}
		for _, e := range []uint32{11, 12, 13, 14} {
			if _, ok := names[[2]uint32{389, e}]; !ok {
				t.Fatalf("%s: boss %d is gone", name, e)
			}
		}
	}
}

// A plan carries no boss display name: an upsert with an empty name leaves an existing row's name alone, and a name the
// plan does carry is written.
func TestForeverBossUpsertKeepsAnExistingName(t *testing.T) {
	rep := ForeverReport{
		Rows:   Rows{Plan: PlanRow{Plan: "named", Map: 389}},
		Bosses: []BossRow{{Map: 389, Entry: 11}},
	}
	names := map[[2]uint32]string{{389, 11}: "Oggleflint"}
	bossNames(t, names, WriteForeverSQL([]ForeverReport{rep}))
	if got := names[[2]uint32{389, 11}]; got != "Oggleflint" {
		t.Fatalf("the empty name overwrote the display name: %q", got)
	}
	rep.Bosses[0].Name = "Taragaman the Hungerer"
	bossNames(t, names, WriteForeverSQL([]ForeverReport{rep}))
	if got := names[[2]uint32{389, 11}]; got != "Taragaman the Hungerer" {
		t.Fatalf("the plan's name was not written: %q", got)
	}
}

// The reader understands the upsert the export writes: the clause closes a multi-row boss statement without ending the
// parse, and a name reads back unchanged.
func TestForeverParserReadsTheBossUpsert(t *testing.T) {
	rep := ForeverReport{
		Rows:   Rows{Plan: PlanRow{Plan: "upsert", Map: 389}},
		Bosses: []BossRow{{Map: 389, Entry: 11, Name: "Boss One"}, {Map: 389, Entry: 12}},
	}
	back, err := ParseForever(WriteForeverSQL([]ForeverReport{rep}))
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || len(back[0].Bosses) != 2 {
		t.Fatalf("read back %d plans", len(back))
	}
	if back[0].Bosses[0] != (BossRow{Map: 389, Entry: 11, Name: "Boss One"}) || back[0].Bosses[1].Entry != 12 {
		t.Fatalf("boss rows read back %+v", back[0].Bosses)
	}
}
