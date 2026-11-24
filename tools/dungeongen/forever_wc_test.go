package main

// The issue's acceptance without game files: the real Wailing Caverns plan regenerated on a synthetic
// Forever (v16) mesh. The fixture is a Delaunay surface through the plan's own points, so every point
// is a vertex at its exact height, plus a flat disk around each boss so the walk has somewhere to stand
// beside it; the surface is split at the 533-yd tile borders and written as linked Forever tiles by
// internal/navmesh/navfixture (never a real .mmap or .mmtile file). A path that crosses a tile border
// needs the border links, so the fixture marks the border edges as Detour external portals.
//
// The real ForeverCore tiles are checked by forever_real_test.go when FOREVER_MMAPS_DIR is set.

import (
	"math"
	"path/filepath"
	"sort"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// TestForeverSyntheticWailingCavernsRegen: every leg of the Wailing Caverns plan regenerates on a
// synthetic Forever mesh and no plan point is off it (the issue's acceptance, runnable without the
// core's data).
func TestForeverSyntheticWailingCavernsRegen(t *testing.T) {
	p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", "wailing_caverns.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := writeWailingCavernsForever(t, p)
	regenWailingCavernsForever(t, p, dir)
}

// wailingCavernsWorld is the world the acceptance runs against: the plan's own pull groups, as
// forever_real_test.go builds it (the fixture carries no world export; the test checks the mesh
// handling, not the live spawn table).
func wailingCavernsWorld(p *dungeon.Plan) *worldspawn.World {
	w := &worldspawn.World{Map: p.Map}
	guid := uint64(1)
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			for i, pos := range g.Positions {
				e := g.Entries[min(i, len(g.Entries)-1)]
				w.Spawns = append(w.Spawns, worldspawn.Spawn{GUID: guid, Entry: e, Name: "pack", LevelMax: 20, Faction: 16, Pos: pos, DetectionYd: 20})
				guid++
			}
		}
	}
	return w
}

// regenWailingCavernsForever is the issue's acceptance on one Forever mesh folder: every leg of the
// plan regenerates and no plan point is off the mesh afterwards. Both the synthetic fixture and the
// real tiles (forever_real_test.go) run it.
func regenWailingCavernsForever(t *testing.T, p *dungeon.Plan, dir string) {
	t.Helper()
	w := wailingCavernsWorld(p)
	pl := navmesh.NewForeverPlanner(dir, nil)
	defer pl.Close()
	pl.SetAvoid(p.Map, dungeon.NavBoxes(p.Avoid))
	offMesh := func() (int, []dungeon.Point) {
		n := 0
		var first []dungeon.Point
		planPointRefs(p, func(pt *dungeon.Point, pull bool, _ func()) {
			maxZ, maxH := meshOKZ, meshOKH
			if pull {
				maxZ, maxH = pullOKZ, pullOKH
			}
			ok, err := onMesh(pl, p.Map, *pt, maxZ, maxH)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				n++
				if len(first) < 5 {
					first = append(first, *pt)
				}
			}
		})
		return n, first
	}
	before, firstBefore := offMesh()
	t.Logf("before: %d plan points off the Forever mesh, first %v", before, firstBefore)
	r := &regen{mapID: p.Map, nav: pl, mmaps: dir, world: w, plan: p}
	for i := range p.Legs {
		rep, err := r.regenLeg(i)
		if err != nil {
			t.Errorf("leg %d (%s): %v", i, p.Legs[i].Name, err)
			continue
		}
		t.Logf("leg %d (%s): %s", i, p.Legs[i].Name, rep)
	}
	after, firstAfter := offMesh()
	t.Logf("after: %d plan points off the Forever mesh, first %v", after, firstAfter)
	if after != 0 {
		t.Errorf("%d plan points are off the Forever mesh after the regeneration, first %v", after, firstAfter)
	}
}

// wailingCavernsPoints is every point planPointRefs looks at (vias, leg ends, pull positions, boss
// positions), deduplicated on (x, y): the fixture's surface has one height per (x, y), and the one
// plan pair that shares a ground position stands 0.1 yd apart in z (well inside the mesh tolerance).
func wailingCavernsPoints(p *dungeon.Plan) [][3]float64 {
	var pts [][3]float64
	seen := map[[2]float64]bool{}
	add := func(pt dungeon.Point) {
		k := [2]float64{math.Round(float64(pt.X)*100) / 100, math.Round(float64(pt.Y)*100) / 100}
		if seen[k] {
			return
		}
		seen[k] = true
		pts = append(pts, [3]float64{float64(pt.X), float64(pt.Y), float64(pt.Z)})
	}
	for _, l := range p.Legs {
		for _, v := range l.Via {
			add(v.Point)
		}
		add(l.To)
		for _, g := range l.PullGroups {
			for _, pos := range g.Positions {
				add(pos)
			}
		}
		if l.Boss != nil {
			add(l.Boss.Position)
		}
	}
	return pts
}

// writeWailingCavernsForever builds the synthetic map: a Delaunay surface through the plan's points,
// clipped to the tiles it spans, Forever tiles with their border edges linked.
func writeWailingCavernsForever(t *testing.T, p *dungeon.Plan) string {
	t.Helper()
	pts := surfacePoints(wailingCavernsPoints(p), wailingCavernsBosses(p))
	if len(pts) < 3 {
		t.Fatalf("the plan has %d points: no surface", len(pts))
	}
	tris := delaunayXY(pts)
	if len(tris) == 0 {
		t.Fatal("the Delaunay surface is empty")
	}
	minX, minY, maxX, maxY := pts[0][0], pts[0][1], pts[0][0], pts[0][1]
	for _, q := range pts[1:] {
		minX, minY, maxX, maxY = math.Min(minX, q[0]), math.Min(minY, q[1]), math.Max(maxX, q[0]), math.Max(maxY, q[1])
	}
	tx0, tx1 := gridIndex(minY), gridIndex(maxY)
	ty0, ty1 := gridIndex(minX), gridIndex(maxX)
	polys := map[[2]int32][]navfixture.Poly{}
	for _, tr := range tris {
		var tri []wcV3
		for _, i := range tr {
			tri = append(tri, wcV3{pts[i][0], pts[i][1], pts[i][2]})
		}
		for tx := tx0; tx <= tx1; tx++ {
			for ty := ty0; ty <= ty1; ty++ {
				x0, y0, x1, y1 := tileBox(tx, ty)
				polys[[2]int32{tx, ty}] = append(polys[[2]int32{tx, ty}], fanPolys(clipRect(tri, x0, y0, x1, y1))...)
			}
		}
	}
	var tiles []navfixture.ForeverTile
	for tx := tx0; tx <= tx1; tx++ {
		for ty := ty0; ty <= ty1; ty++ {
			ps := polys[[2]int32{tx, ty}]
			if len(ps) == 0 {
				continue
			}
			tiles = append(tiles, navfixture.ForeverTile{TX: tx, TY: ty, Polys: ps, Borders: true})
		}
	}
	sort.Slice(tiles, func(i, j int) bool {
		if tiles[i].TX != tiles[j].TX {
			return tiles[i].TX < tiles[j].TX
		}
		return tiles[i].TY < tiles[j].TY
	})
	maxTiles := int32(4)
	for maxTiles < int32(len(tiles)) {
		maxTiles *= 2
	}
	return navfixture.WriteForeverTilesMax(t, p.Map, tiles, maxTiles)
}

// gridIndex is the Detour tile index of a world coordinate (DetourNavMesh calcTileLoc).
func gridIndex(v float64) int32 { return int32(math.Floor(v / navfixture.Tile)) }

// tileBox is the world box a tile covers: TY from world x, TX from world y.
func tileBox(tx, ty int32) (x0, y0, x1, y1 float64) {
	return float64(ty) * navfixture.Tile, float64(tx) * navfixture.Tile, float64(ty+1) * navfixture.Tile, float64(tx+1) * navfixture.Tile
}

// wailingCavernsBosses is every boss position the fixture flattens a disk around.
func wailingCavernsBosses(p *dungeon.Plan) [][3]float64 {
	var out [][3]float64
	for _, l := range p.Legs {
		if l.Boss != nil {
			out = append(out, [3]float64{float64(l.Boss.Position.X), float64(l.Boss.Position.Y), float64(l.Boss.Position.Z)})
		}
	}
	return out
}

// surfacePoints is the fixture's vertex set: the plan's own points at their exact heights, plus a flat
// disk (rings at 5 and 10 yd) around every boss. A boss is the one anchor the walk must stand beside,
// and a straight interpolation between the boss's floor and the ledge or wall faces around it left no
// spot that passes the wall (4 yd) and ledge (4.5 yd) rules; the disk keeps the ground flat there,
// and the plan's own points stay exact vertices wherever they are.
func surfacePoints(pts, bosses [][3]float64) [][3]float64 {
	out := append(make([][3]float64, 0, len(pts)+len(bosses)*25), pts...)
	for _, q := range bosses {
		for _, r := range []float64{5, 10} {
			for k := range 12 {
				a := 2 * math.Pi * float64(k) / 12
				out = append(out, [3]float64{q[0] + r*math.Cos(a), q[1] + r*math.Sin(a), q[2]})
			}
		}
	}
	return out
}

// delaunayXY is the Delaunay triangulation of the points' (x, y), Bowyer-Watson: it returns index
// triples into pts. Either diagonal of a cocircular quad is valid, so ties need no rule.
func delaunayXY(pts [][3]float64) [][3]int {
	n := len(pts)
	if n < 3 {
		return nil
	}
	minX, minY, maxX, maxY := pts[0][0], pts[0][1], pts[0][0], pts[0][1]
	for _, q := range pts[1:] {
		minX, minY, maxX, maxY = math.Min(minX, q[0]), math.Min(minY, q[1]), math.Max(maxX, q[0]), math.Max(maxY, q[1])
	}
	m := math.Max(maxX-minX, maxY-minY) * 20
	cx, cy := (minX+maxX)/2, (minY+maxY)/2
	xy := make([][2]float64, 0, n+3)
	for _, q := range pts {
		xy = append(xy, [2]float64{q[0], q[1]})
	}
	// The super triangle is lopsided on purpose: an axis-aligned one puts its edges on lines that a
	// point can share, and a zero-area triangle then tests as containing nothing.
	xy = append(xy,
		[2]float64{cx - m*1.37, cy - m*1.11},
		[2]float64{cx + m*1.23, cy - m*0.97},
		[2]float64{cx - m*0.83, cy + m*1.51})
	tris := [][3]int{{n, n + 1, n + 2}}
	for i := range n {
		var keep [][3]int
		count := map[[2]int]int{}
		for _, tr := range tris {
			if !inCircumcircle(xy[tr[0]], xy[tr[1]], xy[tr[2]], xy[i]) {
				keep = append(keep, tr)
				continue
			}
			for k := range 3 {
				a, b := tr[k], tr[(k+1)%3]
				if a > b {
					a, b = b, a
				}
				count[[2]int{a, b}]++
			}
		}
		edges := make([][2]int, 0, len(count))
		for e, c := range count {
			if c == 1 {
				edges = append(edges, e)
			}
		}
		sort.Slice(edges, func(a, b int) bool {
			if edges[a][0] != edges[b][0] {
				return edges[a][0] < edges[b][0]
			}
			return edges[a][1] < edges[b][1]
		})
		for _, e := range edges {
			keep = append(keep, [3]int{e[0], e[1], i})
		}
		tris = keep
	}
	var out [][3]int
	for _, tr := range tris {
		if tr[0] >= n || tr[1] >= n || tr[2] >= n {
			continue
		}
		if math.Abs(triArea2(xy[tr[0]], xy[tr[1]], xy[tr[2]])) < 1e-3 {
			continue
		}
		out = append(out, tr)
	}
	return out
}

func triArea2(a, b, c [2]float64) float64 {
	return (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
}

// inCircumcircle says whether p is inside the circumcircle of the triangle a, b, c, whose winding
// is not assumed.
func inCircumcircle(a, b, c, p [2]float64) bool {
	ax, ay := a[0]-p[0], a[1]-p[1]
	bx, by := b[0]-p[0], b[1]-p[1]
	cx, cy := c[0]-p[0], c[1]-p[1]
	det := (ax*ax+ay*ay)*(bx*cy-cx*by) - (bx*bx+by*by)*(ax*cy-cx*ay) + (cx*cx+cy*cy)*(ax*by-bx*ay)
	if triArea2(a, b, c) > 0 {
		return det > 0
	}
	return det < 0
}

// wcV3 is a fixture vertex: world (x, y, z).
type wcV3 struct{ x, y, z float64 }

// clipRect cuts the polygon to the box, carrying each vertex's z along the edges it splits (the
// vertices all lie on one plane, so the piece stays planar).
func clipRect(poly []wcV3, x0, y0, x1, y1 float64) []wcV3 {
	poly = clipAxis(poly, func(v wcV3) bool { return v.x >= x0 }, func(a, b wcV3) wcV3 {
		f := (x0 - a.x) / (b.x - a.x)
		return wcV3{x0, a.y + f*(b.y-a.y), a.z + f*(b.z-a.z)}
	})
	poly = clipAxis(poly, func(v wcV3) bool { return v.x <= x1 }, func(a, b wcV3) wcV3 {
		f := (x1 - a.x) / (b.x - a.x)
		return wcV3{x1, a.y + f*(b.y-a.y), a.z + f*(b.z-a.z)}
	})
	poly = clipAxis(poly, func(v wcV3) bool { return v.y >= y0 }, func(a, b wcV3) wcV3 {
		f := (y0 - a.y) / (b.y - a.y)
		return wcV3{a.x + f*(b.x-a.x), y0, a.z + f*(b.z-a.z)}
	})
	return clipAxis(poly, func(v wcV3) bool { return v.y <= y1 }, func(a, b wcV3) wcV3 {
		f := (y1 - a.y) / (b.y - a.y)
		return wcV3{a.x + f*(b.x-a.x), y1, a.z + f*(b.z-a.z)}
	})
}

// clipAxis is one Sutherland-Hodgman step: keep the inside vertices, insert the crossing point where
// the polygon leaves or enters.
func clipAxis(poly []wcV3, inside func(wcV3) bool, cut func(a, b wcV3) wcV3) []wcV3 {
	var out []wcV3
	for i, cur := range poly {
		prev := poly[(i+len(poly)-1)%len(poly)]
		ci, pi := inside(cur), inside(prev)
		switch {
		case ci && pi:
			out = append(out, cur)
		case ci && !pi:
			out = append(out, cut(prev, cur), cur)
		case !ci && pi:
			out = append(out, cut(prev, cur))
		}
	}
	return out
}

// fanPolys turns a convex clipped polygon into triangles (a fan), dropping the degenerate ones.
func fanPolys(poly []wcV3) []navfixture.Poly {
	if len(poly) < 3 {
		return nil
	}
	out := make([]navfixture.Poly, 0, len(poly)-2)
	a := [2]float64{poly[0].x, poly[0].y}
	for i := 1; i+1 < len(poly); i++ {
		b := [2]float64{poly[i].x, poly[i].y}
		c := [2]float64{poly[i+1].x, poly[i+1].y}
		if math.Abs(triArea2(a, b, c)) < 1e-3 {
			continue
		}
		out = append(out, navfixture.Poly{toV3(poly[0]), toV3(poly[i]), toV3(poly[i+1])})
	}
	return out
}

func toV3(v wcV3) [3]float32 { return [3]float32{float32(v.x), float32(v.y), float32(v.z)} }
