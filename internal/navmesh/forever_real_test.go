//go:build cgo

package navmesh

// The real repack mmaps (never committed): set FOREVER_MMAPS_DIR to the folder holding MMMM.mmap and
// MMMM_XX_YY.mmtile files. Without the folder the tests skip. CI's forever-navmesh job fetches a subset built by
// tools/forevermeshsubset and marks it with FOREVER_MMAPS_SUBSET=1 (the every-tile test drops its whole-repack
// floor for a subset folder, docs/CI.md).

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// foreverMmaps is the Forever mmaps folder, or a skip. It must hold map 0 (the human starting area) and
// map 389 (Ragefire Chasm).
func foreverMmaps(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("FOREVER_MMAPS_DIR")
	if dir == "" {
		t.Skip("set FOREVER_MMAPS_DIR to a Forever mmaps folder to run the real-data tests")
	}
	for _, n := range []string{"0000.mmap", "0389.mmap", "0389_31_31.mmtile"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Skipf("%s: %v", dir, err)
		}
	}
	return dir
}

func foreverPlanner(t *testing.T) *Planner {
	t.Helper()
	// Ragefire Chasm is an instance of Eastern Kingdoms: its own tiles first, then map 0's (the server's
	// MMapManager parent rule).
	p := NewForeverPlanner(foreverMmaps(t), map[uint32]uint32{389: 0})
	t.Cleanup(p.Close)
	return p
}

// TestForeverRealMap0HumanStart is (a): a 60-yd route near the human starting area is complete, every
// corner is on the mesh, and the points' Z departs from the straight interpolation between the ends.
func TestForeverRealMap0HumanStart(t *testing.T) {
	p := foreverPlanner(t)
	from := Vec3{-8913, -118, 82.54}
	to := Vec3{-8913, -58, 82.54}
	r, err := p.PathTo(0, from, to)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("map 0 route: %s, %d corners, %d points, %.1f yd, flags %v", r.Kind, r.Corners, len(r.Points), r.Length, r.FlagNames)
	if r.Kind != RouteComplete {
		t.Fatalf("route %+v", r)
	}
	if r.Length < 55 || r.Length > 75 {
		t.Fatalf("%.1f yd for a 60-yd route", r.Length)
	}
	for i, c := range r.Points {
		snapped, ok, err := p.Snap(0, c)
		if err != nil || !ok || dist3D(snapped, c) > 0.2 {
			t.Fatalf("point %d %s is not on the mesh: snap %s ok %v err %v", i, fmtVec(c), fmtVec(snapped), ok, err)
		}
	}
	// A hill between the ends: the floor samples cannot all lie on the end-to-end chord. Project each
	// point on the chord in the x/y plane (the route may run along either axis).
	a, b := r.Points[0], r.Points[len(r.Points)-1]
	dx, dy := float64(b[0]-a[0]), float64(b[1]-a[1])
	l2 := dx*dx + dy*dy
	worst := 0.0
	for _, q := range r.Points {
		f := ((float64(q[0]-a[0]))*dx + (float64(q[1]-a[1]))*dy) / l2
		chord := float64(a[2]) + f*float64(b[2]-a[2])
		worst = max(worst, math.Abs(float64(q[2])-chord))
	}
	t.Logf("map 0 route Z departs from the straight interpolation by up to %.2f yd", worst)
	if worst < 0.5 {
		t.Fatalf("the route Z follows the straight line (worst %.2f yd)", worst)
	}
}

type rfcPlan struct {
	Map      uint32                    `json:"map"`
	Entrance struct{ X, Y, Z float32 } `json:"entrance"`
	Legs     []struct {
		Name string                    `json:"name"`
		To   struct{ X, Y, Z float32 } `json:"to"`
		// PullGroups are the packs of the leg; Approach is where the party stands to pull one (absent: the first
		// position is used), Positions are the mobs themselves.
		PullGroups []struct {
			Approach  []struct{ X, Y, Z float32 } `json:"approach"`
			Positions []struct{ X, Y, Z float32 } `json:"positions"`
		} `json:"pull_groups"`
	} `json:"legs"`
}

func rfcVec(p struct{ X, Y, Z float32 }) Vec3 { return Vec3{p.X, p.Y, p.Z} }

// TestForeverRealMap389Ragefire is (b): the instance's tiles load; the entrance-to-first-boss route is
// complete; and every adjacent pair of the committed plan's waypoints is classified (the table goes in
// docs/forever/NAVMESH-R1-NOTES.md).
func TestForeverRealMap389Ragefire(t *testing.T) {
	p := foreverPlanner(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "dungeons", "ragefire_chasm.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan rfcPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Map != 389 || len(plan.Legs) == 0 {
		t.Fatalf("plan map %d, %d legs", plan.Map, len(plan.Legs))
	}
	entrance := Vec3{plan.Entrance.X, plan.Entrance.Y, plan.Entrance.Z}
	boss := Vec3{-147.55, 38.71, -38.8} // dungeons/ragefire_chasm.json leg oggleflint's boss.position
	r, err := p.PathTo(389, entrance, boss)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("RFC entrance -> Oggleflint: %s, %d corners, %d points, %.1f yd, flags %v", r.Kind, r.Corners, len(r.Points), r.Length, r.FlagNames)
	if r.Kind != RouteComplete {
		t.Fatalf("entrance -> first boss %+v", r)
	}

	type row struct {
		pair    string
		kind    RouteKind
		corners int
		length  float32
	}
	points := []struct {
		name string
		at   Vec3
	}{{"entrance", entrance}}
	for _, leg := range plan.Legs {
		points = append(points, struct {
			name string
			at   Vec3
		}{leg.Name, Vec3{leg.To.X, leg.To.Y, leg.To.Z}})
	}
	var rows []row
	complete := 0
	for i := 1; i < len(points); i++ {
		r, err := p.PathTo(389, points[i-1].at, points[i].at)
		if err != nil {
			t.Fatalf("%s -> %s: %v", points[i-1].name, points[i].name, err)
		}
		if r.Kind == RouteComplete {
			complete++
		}
		rows = append(rows, row{points[i-1].name + " -> " + points[i].name, r.Kind, r.Corners, r.Length})
	}
	t.Logf("RFC plan leg pairs: %d complete of %d", complete, len(rows))
	for _, r := range rows {
		t.Logf("  %-32s %-9s %3d corners %7.1f yd", r.pair, r.kind, r.corners, r.length)
	}
	if complete != len(rows) {
		t.Fatalf("%d of %d plan leg pairs are not complete", len(rows)-complete, len(rows))
	}
}

// TestForeverRealMap389AvoidsMagma is (c): across the trogg cave's lava basin the safe walk filter routes
// round the NAV_MAGMA_SLIME polygons, while PathGenerator's own player filter (which includes them) takes a
// corridor that crosses them.
func TestForeverRealMap389AvoidsMagma(t *testing.T) {
	p := foreverPlanner(t)
	from := Vec3{-228, -66.6, -60.5}
	to := Vec3{-160, -60, -60}
	safe, err := p.PathTo(389, from, to)
	if err != nil {
		t.Fatal(err)
	}
	server, err := p.PathToFilter(389, from, to, Forever.ServerPlayerFilter())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("safe filter:   %s, %d corners, %.1f yd, flags %v", safe.Kind, safe.Corners, safe.Length, safe.FlagNames)
	t.Logf("server filter: %s, %d corners, %.1f yd, flags %v", server.Kind, server.Corners, server.Length, server.FlagNames)
	if safe.Kind != RouteComplete || safe.Flags&Forever.Terrain().Liquid() != 0 {
		t.Fatalf("the safe route is not magma-free: %+v", safe)
	}
	if server.Kind != RouteComplete || server.Flags&Forever.Terrain().Liquid() == 0 {
		t.Fatalf("the server filter did not cross the flagged liquid: %+v", server)
	}
}

// foreverTilePolygons parses one Forever .mmtile without Detour: the 20-byte file header, the 100-byte
// dtMeshHeader (polyCount at +24, vertCount at +28), the vertices (12 bytes each, Detour y, z, x) and one 32-byte
// dtPoly each (verts[6] at +4, flags at +28, vertCount at +30).
func foreverTilePolygons(data []byte) (flags []uint16, corners [][][3]float32, err error) {
	if len(data) < TileHeaderLen+100 {
		return nil, nil, os.ErrInvalid
	}
	head := data[TileHeaderLen:]
	polyCount := int(int32(binary.LittleEndian.Uint32(head[24:])))
	vertCount := int(int32(binary.LittleEndian.Uint32(head[28:])))
	if polyCount < 0 || vertCount < 0 || len(head) < 100+12*vertCount+32*polyCount {
		return nil, nil, os.ErrInvalid
	}
	verts := head[100 : 100+12*vertCount]
	polys := head[100+12*vertCount : 100+12*vertCount+32*polyCount]
	for i := 0; i < polyCount; i++ {
		p := polys[32*i:]
		n := int(p[30])
		if n < 0 || n > 6 {
			return nil, nil, os.ErrInvalid
		}
		flags = append(flags, binary.LittleEndian.Uint16(p[28:]))
		poly := make([][3]float32, 0, n)
		for k := 0; k < n; k++ {
			v := int(binary.LittleEndian.Uint16(p[4+2*k:]))
			if v >= vertCount {
				return nil, nil, os.ErrInvalid
			}
			// Detour (y, z, x) to WoW (x, y, z).
			poly = append(poly, Vec3{
				math.Float32frombits(binary.LittleEndian.Uint32(verts[12*v+8:])),
				math.Float32frombits(binary.LittleEndian.Uint32(verts[12*v:])),
				math.Float32frombits(binary.LittleEndian.Uint32(verts[12*v+4:])),
			})
		}
		corners = append(corners, poly)
	}
	return flags, corners, nil
}

// TestForeverRealFlagCounts is item 1's scan: every polygon of every real tile, counted by its flag word, per map.
// It is the table in NAVMESH-R1-NOTES.md; it also pins that the tiles only carry bits the format defines.
func TestForeverRealFlagCounts(t *testing.T) {
	dir := foreverMmaps(t)
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	per := map[uint32]map[uint16]int{}
	known := Forever.Terrain()
	knownBits := known.Ground | known.Steep | known.Water | known.Magma | known.Slime | known.Road
	for _, e := range ents {
		name := e.Name()
		mapID, _, _, ok := Forever.TileParse(name)
		if !ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		flags, _, err := foreverTilePolygons(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if per[mapID] == nil {
			per[mapID] = map[uint16]int{}
		}
		for _, f := range flags {
			per[mapID][f]++
			if f&^knownBits != 0 {
				t.Fatalf("%s: flag 0x%04x carries bits the format does not define (known 0x%04x)", name, f, knownBits)
			}
		}
	}
	ids := slices.Sorted(maps.Keys(per))
	for _, m := range ids {
		var parts []string
		for _, f := range slices.Sorted(maps.Keys(per[m])) {
			parts = append(parts, fmt.Sprintf("0x%02x:%d", f, per[m][f]))
		}
		t.Logf("map %04d: %s", m, strings.Join(parts, " "))
	}
	// The two outdoor maps this round looked at, so the notes' table can be read from the test log directly.
	for _, m := range []uint32{0, 1} {
		if per[m][known.Road] == 0 {
			t.Logf("map %04d: no NAV_ROAD (0x%02x) polygons in this data set", m, known.Road)
		}
	}
}

// TestForeverRealRoadRoute is item 1's real-data route: a stretch of road whose start box holds nothing but road
// polygons must be unreachable for the old ground|water filter and complete with the fixed one. The repack this round
// used (0.1.37.4-client-70888) has no NAV_ROAD polygon at all, so the test skips with the scan's number; a folder
// generated with road tagging runs it for real.
func TestForeverRealRoadRoute(t *testing.T) {
	dir := foreverMmaps(t)
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	known := Forever.Terrain()
	type road struct {
		mapID uint32
		poly  [][3]float32
	}
	var roads []road
	scanned := 0
	for _, e := range ents {
		name := e.Name()
		mapID, _, _, ok := Forever.TileParse(name)
		if !ok || (mapID != 0 && mapID != 1) {
			continue
		}
		scanned++
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		flags, polys, err := foreverTilePolygons(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for i, f := range flags {
			if f&known.Road != 0 {
				roads = append(roads, road{mapID, polys[i]})
			}
		}
	}
	if len(roads) == 0 {
		t.Skipf("no NAV_ROAD polygon in maps 0/1 of this set (%d tiles scanned): the route needs a folder generated with road tagging", scanned)
	}
	p := foreverPlanner(t)
	old := Filter{Include: known.Ground | known.Water, Exclude: known.Magma | known.Slime | known.Steep}
	for i, r := range roads {
		if i >= 64 {
			break
		}
		// The road-only start: every polygon in the server's 3 x 5 x 3 yd box must be road, else the old filter's
		// findNearestPoly would still find ground and the comparison would prove nothing.
		var cx, cy, cz float32
		for _, v := range r.poly {
			cx += v[0] / float32(len(r.poly))
			cy += v[1] / float32(len(r.poly))
			cz += v[2] / float32(len(r.poly))
		}
		box, _, err := p.Polygons(r.mapID, cx-3, cy-3, cx+3, cy+3)
		if err != nil || len(box) == 0 {
			continue
		}
		onlyRoad := true
		for _, b := range box {
			if b.Flags&known.Road == 0 || b.Flags&^known.Road != 0 {
				onlyRoad = false
				break
			}
		}
		if !onlyRoad {
			continue
		}
		// A second point on the same road polygon, 8 to 30 yd from the first along its longest edge.
		from, to := Vec3{cx, cy, cz}, Vec3{cx, cy, cz}
		best := float32(0)
		for k := 0; k < len(r.poly); k++ {
			a, b := r.poly[k], r.poly[(k+1)%len(r.poly)]
			if d := dist2D(a, b); d > best {
				best, from, to = d, a, b
			}
		}
		if best < 8 {
			continue
		}
		from[0] += 0.2
		from[1] += 0.2
		to[0] -= 0.2
		to[1] -= 0.2
		oldRoute, err := p.PathToFilter(r.mapID, from, to, old)
		if err != nil {
			t.Fatal(err)
		}
		if oldRoute.Kind != RouteNone || !strings.Contains(oldRoute.Reason, "start") {
			continue // this road has ground beside it: the next one
		}
		fixed, err := p.PathTo(r.mapID, from, to)
		if err != nil {
			t.Fatal(err)
		}
		if fixed.Kind != RouteComplete || fixed.Flags&known.Road == 0 {
			t.Fatalf("road route on map %d %s -> %s: %+v", r.mapID, fmtVec(from), fmtVec(to), fixed)
		}
		t.Logf("map %d road: %s -> %s, %.1f yd, %d corners, old filter %q, fixed %s (flags %v)",
			r.mapID, fmtVec(from), fmtVec(to), fixed.Length, fixed.Corners, oldRoute.Reason, fixed.Kind, fixed.FlagNames)
		return
	}
	t.Skipf("no road stretch of maps 0/1 has a road-only 3-yrd box (%d road polygons): nothing to compare", len(roads))
}

// TestForeverRealMap389ApproachPoints is item 8's extension of the plan check: every pull group's approach point (its
// `approach` entry, or its first mob position when the plan gives none) from the leg's start and from the previous
// group's point. The table goes in NAVMESH-R1-NOTES.md; the test fails on a plan row the mesh cannot answer at all
// (a query error), not on an honest partial route.
func TestForeverRealMap389ApproachPoints(t *testing.T) {
	p := foreverPlanner(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "dungeons", "ragefire_chasm.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan rfcPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	type row struct {
		from, to string
		kind     RouteKind
		corners  int
		length   float32
		straight float32
	}
	var rows []row
	counts := map[RouteKind]int{}
	start := rfcVec(plan.Entrance)
	for li, leg := range plan.Legs {
		if li > 0 {
			start = rfcVec(plan.Legs[li-1].To)
		}
		prev, prevName := start, "start("+leg.Name+")"
		for gi, group := range leg.PullGroups {
			var at Vec3
			switch {
			case len(group.Approach) > 0:
				at = rfcVec(group.Approach[0])
			case len(group.Positions) > 0:
				at = rfcVec(group.Positions[0])
			default:
				continue
			}
			name := fmt.Sprintf("%s.g%d", leg.Name, gi+1)
			r, err := p.PathTo(389, prev, at)
			if err != nil {
				t.Fatalf("%s -> %s: %v", prevName, name, err)
			}
			rows = append(rows, row{prevName, name, r.Kind, r.Corners, r.Length, dist3D(prev, at)})
			counts[r.Kind]++
			if r.Kind == RouteNone {
				t.Logf("  none %s -> %s (%s): %s", prevName, name, fmtVec(at), r.Reason)
			}
			prev, prevName = at, name
		}
	}
	t.Logf("RFC approach rows: %d complete, %d partial, %d none of %d",
		counts[RouteComplete], counts[RoutePartial], counts[RouteNone], len(rows))
	for _, r := range rows {
		t.Logf("  %-18s -> %-18s %-8s %3d corners %7.1f yd (straight %.1f)", r.from, r.to, r.kind, r.corners, r.length, r.straight)
	}
	if counts[RouteComplete] == 0 {
		t.Fatalf("no approach row is complete")
	}
}

// TestForeverRealEveryTileLoads loads every tile of every map through the planner (readTileFile, validateTile,
// dtNavMesh::addTile and the LRU's removeTile): the round-1 checks must refuse corrupt files and still accept every
// healthy one. It also runs the new params validation over all 74 real .mmap files.
func TestForeverRealEveryTileLoads(t *testing.T) {
	dir := foreverMmaps(t)
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	byMap := map[uint32][][2]int{}
	for _, e := range ents {
		if m, r, c, ok := Forever.TileParse(e.Name()); ok {
			byMap[m] = append(byMap[m], [2]int{r, c})
		}
	}
	const tile = 533.3333333
	tiles, maps := 0, 0
	for mapID, grids := range byMap {
		p := NewForeverPlanner(dir, nil)
		for _, rc := range grids {
			x := (32 - float32(rc[0]) - 0.5) * tile
			y := (32 - float32(rc[1]) - 0.5) * tile
			if _, _, err := p.Polygons(mapID, x-1, y-1, x+1, y+1); err != nil {
				p.Close()
				t.Fatalf("map %d tile %d/%d: %v", mapID, rc[0], rc[1], err)
			}
			tiles++
		}
		p.Close()
		maps++
	}
	t.Logf("loaded %d tiles of %d maps, none refused", tiles, maps)
	// The whole-repack floor is the laptop run's check that the folder is the full repack. CI fetches a
	// tools/forevermeshsubset folder (Ragefire Chasm and a block of map 0), which the forever-navmesh job marks with
	// FOREVER_MMAPS_SUBSET; there every tile present must still load, but the count is the subset's.
	if os.Getenv("FOREVER_MMAPS_SUBSET") == "" && (maps < 70 || tiles < 3000) {
		t.Fatalf("%d tiles of %d maps, want the whole repack", tiles, maps)
	}
}
