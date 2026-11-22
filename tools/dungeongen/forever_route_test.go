package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

// The Forever polygon-flag bits (internal/navmesh/format.go:200-217): ground 0x01, steep 0x02, water 0x04, magma and slime
// 0x08, road 0x10. The MaNGOS ones are the Nav* constants: water 0x08, magma 0x02, slime 0x04, steep 0x10.
const (
	fvGround = 0x01
	fvSteep  = 0x02
	fvWater  = 0x04
	fvMagma  = 0x08
	fvRoad   = 0x10
)

// foreverRoom is regenRoom on Forever's bits: cells inside the water rectangles take the given flag, every other cell takes
// base.
func foreverRoom(base, wet uint16, water [][4]float32) (polys []navfixture.Poly, flags []uint16) {
	polys, _ = regenRoom(nil)
	for x := float32(10); x < 150; x += 4 {
		for y := float32(10); y < 90; y += 4 {
			f := base
			for _, r := range water {
				if x >= r[0] && x+4 <= r[2] && y >= r[1] && y+4 <= r[3] {
					f = wet
				}
			}
			flags = append(flags, f)
		}
	}
	return polys, flags
}

func foreverPlanner(t *testing.T, mapID uint32, polys []navfixture.Poly, flags []uint16) *navmesh.Planner {
	t.Helper()
	dir := navfixture.WriteForeverDir(t, mapID, polys, flags)
	pl := navmesh.NewForeverPlanner(dir, nil)
	t.Cleanup(pl.Close)
	return pl
}

// TestTerrainReadsEachFormatsBits: the same flag word means different terrain in the two generations. The failing case
// pinned: on a Forever mesh a road (0x10) is plain ground and 0x04 is water, where the MaNGOS constants read 0x10 as steep
// and 0x04 as slime.
func TestTerrainReadsEachFormatsBits(t *testing.T) {
	fv := terrain{navmesh.Forever.Terrain()}
	mg := terrain{navmesh.MaNGOS.Terrain()}
	for _, c := range []struct {
		name        string
		t           terrain
		flags       uint16
		ground, wet bool
		plain       bool
	}{
		{"forever ground", fv, fvGround, true, false, true},
		{"forever road", fv, fvRoad, true, false, true},
		{"forever steep", fv, fvGround | fvSteep, true, false, false},
		{"forever steep alone", fv, fvSteep, false, false, false},
		{"forever water", fv, fvWater, false, true, false},
		{"forever magma or slime", fv, fvMagma, false, false, false},
		{"forever ground marked by an avoid box", fv, fvGround | fvMagma, true, false, false},
		{"mangos ground", mg, navmesh.NavGround, true, false, true},
		{"mangos water", mg, navmesh.NavWater, false, true, false},
		{"mangos ground in water", mg, navmesh.NavGround | navmesh.NavWater, true, true, false},
		{"mangos steep", mg, navmesh.NavGround | navmesh.NavSteep, true, false, false},
		{"mangos slime", mg, navmesh.NavGround | navmesh.NavSlime, true, false, false},
		{"mangos 0x10 is steep, not a road", mg, navmesh.NavSteep, false, false, false},
	} {
		if got := c.t.ground(c.flags); got != c.ground {
			t.Errorf("%s: ground(%#x) = %v, want %v", c.name, c.flags, got, c.ground)
		}
		if got := c.t.water(c.flags); got != c.wet {
			t.Errorf("%s: water(%#x) = %v, want %v", c.name, c.flags, got, c.wet)
		}
		if got := c.t.plain(c.flags); got != c.plain {
			t.Errorf("%s: plain(%#x) = %v, want %v", c.name, c.flags, got, c.plain)
		}
	}
}

// TestRegenPlaneForeverRoadIsGround: a Forever road polygon carries the road bit alone, so a stand on a road is plain ground. The
// MaNGOS constants read that bit as steep and refuse every stand on a road.
func TestRegenPlaneForeverRoadIsGround(t *testing.T) {
	const mapID = 43
	polys, flags := foreverRoom(fvGround, fvRoad, [][4]float32{{50, 30, 90, 70}})
	pl := foreverPlanner(t, mapID, polys, flags)
	r := &regen{mapID: mapID, nav: pl, plan: &dungeon.Plan{Map: mapID}}
	for name, p := range map[string]dungeon.Point{"ground": {X: 20, Y: 50}, "road": {X: 70, Y: 50}} {
		if _, ok, err := r.plane(p); err != nil || !ok {
			t.Errorf("%s at %.0f %.0f is not plain ground (ok %v, err %v)", name, p.X, p.Y, ok, err)
		}
	}
	// the other terrain is still refused
	for name, base := range map[string]uint16{"steep": fvSteep, "magma": fvMagma, "water": fvWater} {
		polys, flags := foreverRoom(base, base, nil)
		r := &regen{mapID: mapID, nav: foreverPlanner(t, mapID, polys, flags), plan: &dungeon.Plan{Map: mapID}}
		if _, ok, err := r.plane(dungeon.Point{X: 70, Y: 50}); err != nil || ok {
			t.Errorf("a %s cell is plain ground (ok %v, err %v)", name, ok, err)
		}
	}
}

// TestRegenWetLineForeverWater: the straight line across a Forever pool (flag 0x04) is wet, and across a lava pool (0x08, water on the
// MaNGOS bits) it is not: the lava is the planner's to keep out of, not water.
func TestRegenWetLineForeverWater(t *testing.T) {
	const mapID = 43
	across := [2]dungeon.Point{{X: 20, Y: 50}, {X: 140, Y: 50}}
	for _, c := range []struct {
		name string
		wet  uint16
		want bool
	}{{"water", fvWater, true}, {"lava", fvMagma, false}} {
		polys, flags := foreverRoom(fvGround, c.wet, [][4]float32{{50, 30, 90, 70}})
		r := &regen{mapID: mapID, nav: foreverPlanner(t, mapID, polys, flags), plan: &dungeon.Plan{Map: mapID}}
		if got := regenWetLine(r, across[0], across[1]); got != c.want {
			t.Errorf("a line across a %s pool: wet %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRegenSnapForeverRoad: snap puts an off-mesh point on the nearest plain ground, and a road is ground.
func TestRegenSnapForeverRoad(t *testing.T) {
	const mapID = 43
	polys, flags := foreverRoom(fvRoad, fvRoad, nil)
	r := &regen{mapID: mapID, nav: foreverPlanner(t, mapID, polys, flags), plan: &dungeon.Plan{Map: mapID}}
	// 12 yd above the room's floor and outside Nearest's 6 yd: only the floor scan finds the road.
	got, ok, err := r.snap(navmesh.Vec3{50, 50, 12})
	if err != nil || !ok {
		t.Fatalf("snap: ok %v, err %v", ok, err)
	}
	if got.Z != 0 {
		t.Errorf("snapped to z %.1f, want the road at 0", got.Z)
	}
}

// TestSnapPointForeverRoad: the snap table's sweep moves a point off the mesh to the nearest ground polygon, a road polygon
// included (the sweep read only the ground bit, so a Forever road never counted).
func TestSnapPointForeverRoad(t *testing.T) {
	const mapID = 43
	roadCell := navfixture.Square(10, 10, 14, 14, 0)
	pl := foreverPlanner(t, mapID, []navfixture.Poly{roadCell}, []uint16{fvRoad})
	// 3 yd east of the cell: off the mesh (onMesh allows 1 yd), within the 5 yd snap reach.
	pt := dungeon.Point{X: 17, Y: 12, Z: 0}
	if ok, err := onMesh(pl, mapID, pt, meshOKZ, meshOKH); err != nil || ok {
		t.Fatalf("the test point is on the mesh (ok %v, err %v): it does not reproduce the defect", ok, err)
	}
	to, found, err := snapPoint(pl, mapID, pt)
	if err != nil || !found {
		t.Fatalf("snapPoint: found %v, err %v", found, err)
	}
	if to.X < 10 || to.X > 14 || to.Y < 10 || to.Y > 14 {
		t.Errorf("snapped to %v, outside the road cell", to)
	}
}

// TestSnapPointForeverIgnoresWater: a Forever water cell is no place to stand: the sweep gives up (needs_walk) beside one.
func TestSnapPointForeverIgnoresWater(t *testing.T) {
	const mapID = 43
	pl := foreverPlanner(t, mapID, []navfixture.Poly{navfixture.Square(10, 10, 14, 14, 0)}, []uint16{fvWater})
	if _, found, err := snapPoint(pl, mapID, dungeon.Point{X: 17, Y: 12, Z: 0}); err != nil || found {
		t.Errorf("snapped onto a water cell (found %v, err %v)", found, err)
	}
}

// TestMmapsForFormat: a Forever navmesh is one folder holding MMMM.mmap; the Turtle one is a folder per map under the root.
func TestMmapsForFormat(t *testing.T) {
	root := t.TempDir()
	if got := mmapsFor(root, 43, navmesh.Forever); got != "" {
		t.Errorf("an empty root has no map 43: %q", got)
	}
	if err := os.WriteFile(filepath.Join(root, "0043.mmap"), navfixture.MmapParamsForever(), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mmapsFor(root, 43, navmesh.Forever); got != root {
		t.Errorf("forever folder = %q, want %q", got, root)
	}
	if got := mmapsFor(root, 43, navmesh.MaNGOS); got != "" {
		t.Errorf("the MaNGOS lookup found %q in a Forever folder", got)
	}
	if got := mmapsFor(root, 44, navmesh.Forever); got != "" {
		t.Errorf("map 44 has no file: %q", got)
	}
	turtle := filepath.Join(root, "mmaps-turtle043")
	if err := os.Mkdir(turtle, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(turtle, "043.mmap"), navfixture.MmapParams(), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mmapsFor(root, 43, navmesh.MaNGOS); got != turtle {
		t.Errorf("turtle folder = %q, want %q", got, turtle)
	}
}

func TestSnapsFileNames(t *testing.T) {
	if got := snapsFileName(false); got != "snaps.json" {
		t.Errorf("turtle table %q", got)
	}
	if got := snapsFileName(true); got != "snaps-forever.json" {
		t.Errorf("forever table %q: the Forever mesh must not overwrite the Turtle one", got)
	}
}

func TestParentFlag(t *testing.T) {
	var p parentFlag
	for _, s := range []string{"389=0", "43=1"} {
		if err := p.Set(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if v, ok := p.m()[389]; !ok || v != 0 || p.m()[43] != 1 || len(p.m()) != 2 {
		t.Errorf("parents %v", p.m())
	}
	for _, bad := range []string{"", "389", "=0", "389=", "a=1", "1=b"} {
		if err := (&parentFlag{}).Set(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if !strings.Contains(p.String(), "43=1") {
		t.Errorf("string %q", p.String())
	}
}

// writeRoutePlan saves a plan's JSON under a new plans folder and returns the folder.
func writeRoutePlan(t *testing.T, name, plan string) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := dungeon.Parse(name+".json", []byte(plan)); err != nil {
		t.Fatalf("the fixture plan is invalid: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// foreverExportHeader is the creatures.tsv header export-forever.sh writes (export-forever.sh:76).
const foreverExportHeader = "guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\tdetection_range\tflags_extra\tspeed_walk\tgroup_ai\tpool\n"

// foreverExportRow is one creatures.tsv row of that export; level is "" for a spawn the core has no level for.
func foreverExportRow(guid uint64, entry uint32, name, level string, x, y, z float32) string {
	return fmt.Sprintf("%d\t%d\t%s\t\t%s\t0\t16\t0\t%g\t%g\t%g\t0\t0\t0\t0\t\t20\t0\t1\t0\t0\n", guid, entry, name, level, x, y, z)
}

func writeForeverExport(t *testing.T, rows ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "creatures.tsv"), []byte(foreverExportHeader+strings.Join(rows, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// aggroProbePlan walks from x 10 to 20 and pulls a pack at x 40: a spawn near the walk is in a later pack's aggro.
const aggroProbePlan = `{"version":1,"name":"probe","map":43,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"a","to":{"x":20,"y":0,"z":0},
  "pull_groups":[{"entries":[1],"positions":[{"x":10,"y":8,"z":0}],"radius":5},
                 {"entries":[2],"positions":[{"x":40,"y":0,"z":0}],"radius":5,"approach":[{"x":40,"y":0,"z":0}]}]}]}`

func runAggro(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb strings.Builder
	rc := routeAggroCmd(args, &out, &errb)
	return rc, out.String(), errb.String()
}

// TestRouteAggroCmdForeverReadsTheExport: a Forever export leaves the level empty for a spawn the core has no level for; the
// Forever reader gives it level 60 and the check sees the pack, where the Turtle reader (level 0, below the critter level)
// skips it and the check passes a route that walks into it.
func TestRouteAggroCmdForeverReadsTheExport(t *testing.T) {
	plans := writeRoutePlan(t, "probe", aggroProbePlan)
	spawns := writeForeverExport(t,
		foreverExportRow(1, 1, "Early", "20", 10, 8, 0),
		foreverExportRow(2, 2, "Later", "", 25, -2, 0))
	rc, out, errs := runAggro(t, "--forever", "--spawns", spawns, "--dir", plans, "probe")
	if rc != aggroExitFindings || !strings.Contains(out, "Later") || !strings.Contains(out, "guid 2") {
		t.Errorf("--forever: rc %d, out %q, err %q: want the Later pack found", rc, out, errs)
	}
	if !strings.Contains(out, "level 60 assumed") {
		t.Errorf("--forever: no note about the assumed level in %q", out)
	}
	rc, out, errs = runAggro(t, "--spawns", spawns, "--dir", plans, "probe")
	if rc != aggroExitOK {
		t.Errorf("Turtle reader on the same files: rc %d, out %q, err %q: the defect this mode fixes is gone from the test", rc, out, errs)
	}
}

func TestRouteAggroCmdForeverClean(t *testing.T) {
	plans := writeRoutePlan(t, "probe", aggroProbePlan)
	spawns := writeForeverExport(t, foreverExportRow(2, 2, "Far", "20", 25, 300, 0))
	if rc, out, errs := runAggro(t, "--forever", "--spawns", spawns, "--dir", plans, "probe"); rc != aggroExitOK {
		t.Errorf("rc %d, out %q, err %q: want a clean route", rc, out, errs)
	}
}

func TestRouteAggroCmdForeverRefusals(t *testing.T) {
	plans := writeRoutePlan(t, "probe", aggroProbePlan)
	spawns := writeForeverExport(t, foreverExportRow(2, 2, "Far", "20", 25, 300, 0))
	file := filepath.Join(t.TempDir(), "spawns.tsv")
	if err := os.WriteFile(file, []byte("2\t2\t25\t300\t0\t0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no export":     {[]string{"--forever", "--dir", plans, "probe"}, "--spawns"},
		"a spawn list":  {[]string{"--forever", "--spawns", file, "--dir", plans, "probe"}, "is a file"},
		"missing":       {[]string{"--forever", "--spawns", filepath.Join(spawns, "nope"), "--dir", plans, "probe"}, "world export"},
		"no plan":       {[]string{"--forever", "--spawns", spawns, "--dir", plans}, "name one plan"},
		"an unknown id": {[]string{"--forever", "--spawns", spawns, "--dir", plans, "nope"}, "nope"},
	} {
		rc, _, errs := runAggro(t, c.args...)
		if rc != aggroExitUsage || !strings.Contains(errs, c.want) {
			t.Errorf("%s: rc %d, stderr %q, want rc 2 naming %q", name, rc, errs, c.want)
		}
	}
}

func TestCreaturesFromWorldCarriesTheSpawn(t *testing.T) {
	spawns := writeForeverExport(t, foreverExportRow(7, 3, "Rat", "", 1, 2, 3))
	w, _, err := loadRouteWorld(true, spawns, "", 43)
	if err != nil {
		t.Fatal(err)
	}
	cs := creaturesFromWorld(w)
	if len(cs) != 1 {
		t.Fatalf("creatures %v", cs)
	}
	c := cs[0]
	if c.GUID != 7 || c.Entry != 3 || c.Name != "Rat" || c.LevelMax != 60 || c.Faction != 16 ||
		c.Pos != (dungeon.Point{X: 1, Y: 2, Z: 3}) || c.DetectionRange != 20 {
		t.Errorf("creature %+v", c)
	}
}

// The plans of the regen fixtures, in foreverRoom's room: a pull group at x 100 reached from x 20, and a boss with no
// approach of its own.
const (
	regenFixturePlan = `{"version":1,"name":"room","map":43,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"a","to":{"x":20,"y":50,"z":0},
  "pull_groups":[{"entries":[1],"positions":[{"x":100,"y":50,"z":0}],"radius":5,"approach":[{"x":80,"y":50,"z":0}]}]}]}`
	regenBossPlan = `{"version":1,"name":"bossroom","map":43,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"b","to":{"x":20,"y":50,"z":0},
  "boss":{"entry":2,"position":{"x":100,"y":50,"z":0}}}]}`
)

func runRegen(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var rc int
	out := captureStdStreams(t, func() { rc = routeRegenCmd(args) })
	return rc, out
}

// captureStdStreams runs f with os.Stdout and os.Stderr redirected to a file and returns what it wrote.
func captureStdStreams(t *testing.T, f func()) string {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "streams")
	if err != nil {
		t.Fatal(err)
	}
	so, se := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = tmp, tmp
	defer func() { os.Stdout, os.Stderr = so, se }()
	f()
	os.Stdout, os.Stderr = so, se
	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRouteRegenCmdForeverRoom: route-regen --forever regenerates a leg on a Forever v16 mesh against a Forever export, and the
// route it writes stays on the mesh: the first stand on the road polygons (flag 0x10) is accepted as ground.
func TestRouteRegenCmdForeverRoom(t *testing.T) {
	polys, flags := foreverRoom(fvGround, fvRoad, [][4]float32{{60, 30, 120, 70}}) // a road strip across the middle
	mmaps := navfixture.WriteForeverDir(t, 43, polys, flags)
	plans := writeRoutePlan(t, "room", regenFixturePlan)
	spawns := writeForeverExport(t, foreverExportRow(1, 1, "Guard", "", 100, 50, 0))
	rc, out := runRegen(t, "--forever", "--mmaps", mmaps, "--spawns", spawns, "--dir", plans, "--leg", "0", "--dry-run", "room")
	if rc != regenExitOK {
		t.Fatalf("rc %d:\n%s", rc, out)
	}
	if !strings.Contains(out, "dry run") || !strings.Contains(out, "level 60 assumed") || strings.Contains(out, "fails") || strings.Contains(out, "no route") {
		t.Errorf("output:\n%s", out)
	}
}

// TestRouteRegenCmdFormatMismatch: the generation is named, never guessed: a v16 folder read as 1.12 (and the other way) fails at
// the navmesh with the mismatch, before any work.
func TestRouteRegenCmdFormatMismatch(t *testing.T) {
	polys, flags := regenRoom(nil)
	forever := navfixture.WriteForeverDir(t, 43, polys, nil)
	turtle := navfixture.WriteDirFlags(t, 43, polys, flags)
	plans := writeRoutePlan(t, "room", regenFixturePlan)
	spawns := writeForeverExport(t, foreverExportRow(1, 1, "Guard", "60", 100, 50, 0))
	for name, args := range map[string][]string{
		"v16 read as 1.12": {"--mmaps", forever, "--spawns", spawns, "--dir", plans, "--leg", "0", "--dry-run", "room"},
		"1.12 read as v16": {"--forever", "--mmaps", turtle, "--spawns", spawns, "--dir", plans, "--leg", "0", "--dry-run", "room"},
	} {
		rc, out := runRegen(t, args...)
		if rc != regenExitUsage || !strings.Contains(out, "navmesh") {
			t.Errorf("%s: rc %d, output %q: want the navmesh refusal", name, rc, out)
		}
	}
}

func TestRouteRegenCmdForeverFlagRules(t *testing.T) {
	plans := writeRoutePlan(t, "room", regenFixturePlan)
	spawns := writeForeverExport(t, foreverExportRow(1, 1, "Guard", "60", 100, 50, 0))
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"forever needs an export": {[]string{"--forever", "--mmaps", t.TempDir(), "--dir", plans, "--leg", "0", "room"}, "--spawns"},
		"parent is forever's":     {[]string{"--parent", "389=0", "--mmaps", t.TempDir(), "--spawns", spawns, "--dir", plans, "--leg", "0", "room"}, "--forever"},
		"a bad parent":            {[]string{"--forever", "--parent", "x", "--mmaps", t.TempDir(), "--spawns", spawns, "--dir", plans, "--leg", "0", "room"}, "MAP=PARENT"},
	} {
		rc, out := runRegen(t, c.args...)
		if rc != regenExitUsage || !strings.Contains(out, c.want) {
			t.Errorf("%s: rc %d, output %q, want rc 2 naming %q", name, rc, out, c.want)
		}
	}
}

// TestRouteRegenCmdForeverBossWithoutApproach: a boss with no approach gets a stand estimated near it. The leg took the empty
// approach for a stand at 0 0 0 and failed the boss there ("boss 0.0 0.0 0.0 fails ... no route to the boss's stand").
func TestRouteRegenCmdForeverBossWithoutApproach(t *testing.T) {
	polys, flags := foreverRoom(fvGround, fvGround, nil)
	mmaps := navfixture.WriteForeverDir(t, 43, polys, flags)
	plans := writeRoutePlan(t, "bossroom", regenBossPlan)
	spawns := writeForeverExport(t, foreverExportRow(2, 2, "Boss", "63", 100, 50, 0))
	rc, out := runRegen(t, "--forever", "--mmaps", mmaps, "--spawns", spawns, "--dir", plans, "--leg", "0", "--dry-run", "bossroom")
	if rc != regenExitOK {
		t.Fatalf("rc %d:\n%s", rc, out)
	}
	if strings.Contains(out, "0.0 0.0 0.0") || strings.Contains(out, "no route to the boss") {
		t.Errorf("the boss was placed at the origin:\n%s", out)
	}
}
