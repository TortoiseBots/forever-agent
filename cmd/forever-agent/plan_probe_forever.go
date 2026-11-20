package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/mapview"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/planprobe"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// Plan-probe's Forever mode: probe the run leader's exported migration (plan-export --forever output) on the v16 tiles,
// matching every pull, boss and patrol unit against the spawn list by entry plus position (a creature GUID carries no spawn
// id, so plan-check --forever matches the same way). The old-server input paths (the world SQL dump, the v6 tiles, the
// plan JSON rebuild) are refused in this mode: the plan file alone names no spawned unit, and rows rebuilt against one
// tile set prove nothing about the other. Anything the Forever inputs do not carry (linked-pack and patrol tables: the
// spawn list has no groups, links or waypoints) is skipped with a note, never guessed.

// foreverProbePaths adapts the v16 planner to the probe's old-server query surface. ServerPath is the only piece that
// changes generation: the 1.12 core's exact query cannot run on Forever tiles, so the Forever walker (PathTo) stands in.
// The two disagree by construction off the mesh or past a gap; planProbeForeverRouteVerdict names that plainly. Every
// other query (offline plan, floors, nearest, polygons, cut) is the planner's own answer on its own tiles.
type foreverProbePaths struct {
	pl *navmesh.Planner
}

func (f *foreverProbePaths) ServerPath(mapID uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	return planProbeForeverRoute(f.pl, mapID, from, to)
}

func (f *foreverProbePaths) Plan(mapID uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	return f.pl.Plan(mapID, from, to)
}

func (f *foreverProbePaths) Floors(mapID uint32, x, y, lo, hi float32) ([]navmesh.Floor, error) {
	return f.pl.Floors(mapID, x, y, lo, hi)
}

func (f *foreverProbePaths) Nearest(mapID uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	snapped, ok, err := f.pl.Snap(mapID, p)
	if err != nil {
		return navmesh.Vec3{}, false, err
	}
	return snapped, ok, nil
}

func (f *foreverProbePaths) Polygons(mapID uint32, x0, y0, x1, y1 float32) ([]navmesh.Polygon, bool, error) {
	return f.pl.Polygons(mapID, x0, y0, x1, y1)
}

// planProbeForeverRoute runs one hop on the v16 tiles and renders the Forever walker's answer as the probe's server
// query. A complete walk is a normal server path; a partial one keeps the reach it has as an incomplete path whose end
// is where the walker stopped; a walk with no corridor is NOPATH from its start. The distances are the walker's own
// (start/goal distance to the mesh), in the server query's fields.
func planProbeForeverRoute(pl *navmesh.Planner, mapID uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	route, err := pl.PathTo(mapID, from, to)
	if err != nil {
		return navmesh.ServerPath{}, err
	}
	sp := navmesh.ServerPath{ActualEnd: route.End, StartDist: route.StartDistance, EndDist: route.EndDistance}
	for i := 1; i < len(route.Points); i++ {
		sp.Length += float32(foreverDist3(route.Points[i-1], route.Points[i]))
	}
	switch route.Kind {
	case navmesh.RouteComplete:
		if len(route.Points) == 0 {
			sp.Type = navmesh.PathNoPath
			sp.ActualEnd = from
			return sp, nil
		}
		sp.Type = navmesh.PathNormal
		sp.Points = append([]navmesh.Vec3(nil), route.Points...)
		sp.ActualEnd = route.Points[len(route.Points)-1]
	case navmesh.RoutePartial:
		sp.Type = navmesh.PathIncomplete
		sp.Points = append([]navmesh.Vec3(nil), route.Points...)
		if len(route.Points) > 0 {
			sp.ActualEnd = route.Points[len(route.Points)-1]
		} else {
			sp.ActualEnd = route.Start
		}
	default:
		sp.Type = navmesh.PathNoPath
		sp.ActualEnd = from
	}
	return sp, nil
}

// planProbeForeverVerdict reads the walk behind a Forever-mode server answer the way the module would: a complete
// walk is the CheckMove verdict on its own end; a partial walk is unreachable when the walker made no progress from
// the start and "partial" otherwise; no corridor is NOPATH. Partial has no old-server verdict, so the probe's own
// word for it is reported (the reasons name the walk, never the 1.12 query).
func planProbeForeverVerdict(sp navmesh.ServerPath, from, to navmesh.Vec3) string {
	if sp.Type&navmesh.PathNoPath != 0 {
		return "NOPATH"
	}
	if sp.Type&navmesh.PathIncomplete != 0 {
		if float64(foreverDist3(sp.ActualEnd, from)) <= navmesh.NoProgressYd {
			return "unreachable"
		}
		return "partial"
	}
	return navmesh.ServerMoveVerdict(sp, from, to, 0)
}

func foreverDist3(a, b navmesh.Vec3) float64 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// foreverProbeUnit is one pull, boss or patrol unit of the export with its spawn match, for the report.
type foreverProbeUnit struct {
	Leg     int     `json:"leg"`
	Seq     int     `json:"seq"`
	Idx     int     `json:"idx"`
	Entry   uint32  `json:"entry"`
	Role    string  `json:"role"`
	X       float32 `json:"x"`
	Y       float32 `json:"y"`
	Z       float32 `json:"z"`
	Found   bool    `json:"found"`
	GUID    uint64  `json:"guid,omitempty"`
	DistYd  float32 `json:"dist_yd"`
	Missing bool    `json:"missing"`
}

// foreverProbeReport is the Forever side of a plan-probe report: the walked segments plus the unit match. The Segments
// reuse the probe's own type so --json keeps the tb-plan-probe shape; Units names, per unit row of the export, the spawn
// it matched (entry plus position) or the miss.
type foreverProbeReport struct {
	Format   string              `json:"format"`
	Version  int                 `json:"version"`
	Plan     string              `json:"plan"`
	Map      uint32              `json:"map"`
	Source   string              `json:"source,omitempty"`
	Mmaps    string              `json:"mmaps,omitempty"`
	Spawns   string              `json:"spawns,omitempty"`
	Steps    int                 `json:"steps"`
	Segments []planprobe.Segment `json:"segments"`
	Units    []foreverProbeUnit  `json:"units"`
	Misses   int                 `json:"misses"`
	Notes    []string            `json:"notes"`
	Counts   planprobe.Counts    `json:"counts"`
}

// foreverProbeMatch pairs one row with its spawn: the nearest spawn of the row's entry, like plan-check --forever does
// (entry plus position; the GUID carries no spawn id). r0 is the search radius: a pack member may stand up to the pull
// radius from the plan's anchor, so the member's position is the unit row's own, searched wider than a step point.
func foreverProbeMatch(spawns []worldspawn.Spawn, entry uint32, x, y, z float32, r0 float32) (worldspawn.Spawn, float64, bool) {
	best := -1
	bestD := math.Inf(1)
	for i, s := range spawns {
		if s.Entry != entry {
			continue
		}
		dx, dy, dz := float64(s.Pos.X)-float64(x), float64(s.Pos.Y)-float64(y), float64(s.Pos.Z)-float64(z)
		d := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if d < bestD {
			best, bestD = i, d
		}
	}
	if best < 0 {
		return worldspawn.Spawn{}, 0, false
	}
	if bestD > float64(r0) {
		return worldspawn.Spawn{}, bestD, false
	}
	return spawns[best], bestD, true
}

// planProbeForever runs the Forever mode: reads the exported migration and the spawn list, walks every hop on the v16
// tiles with the Forever walker, matches every pull/boss/patrol unit by entry plus position, and prints the report.
func planProbeForever(mmaps, sqlFile, planFlag, spawnsFile, dir string, fromStep int, asJSON bool, out string, stdout, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "plan-probe: "+format+"\n", a...)
		return exitUsage
	}
	data, err := os.ReadFile(sqlFile)
	if err != nil {
		return fail("%v", err)
	}
	reps, err := planexport.ParseForever(string(data))
	if err != nil {
		return fail("%s: %v", sqlFile, err)
	}
	var reps2 []planexport.ForeverReport
	for _, r := range reps {
		if planFlag == "" || r.Rows.Plan.Plan == planFlag {
			reps2 = append(reps2, r)
		}
	}
	switch {
	case len(reps2) == 0 && planFlag != "":
		return fail("%s holds no plan %q (it holds %s)", sqlFile, planFlag, foreverProbePlans(reps))
	case len(reps2) == 0:
		return fail("%s holds no plan rows", sqlFile)
	case len(reps2) > 1:
		return fail("%s holds %d plans (%s): pass --plan NAME", sqlFile, len(reps2), foreverProbePlans(reps2))
	}
	rep := reps2[0]
	rows := rep.Rows
	w, notes, err := worldspawn.LoadForeverSpawns(spawnsFile, rows.Plan.Map)
	if err != nil {
		return fail("%v", err)
	}
	pl := navmesh.NewForeverPlanner(mmaps, nil)
	defer pl.Close()
	paths := &foreverProbePaths{pl: pl}
	fp := &foreverProbe{pl: pl, paths: paths}
	segments, _, perNotes, err := fp.walk(rows, fromStep)
	if err != nil {
		return probeUnavailable(stderr, err)
	}
	notes = append(notes, perNotes...)
	units, misses := foreverProbeUnits(rows.Steps, rep.Units, w.Spawns)
	frep := &foreverProbeReport{
		Format: planprobe.Format, Version: planprobe.Version, Plan: rows.Plan.Plan, Map: rows.Plan.Map,
		Source: "forever-sql " + sqlFile, Mmaps: mmaps, Spawns: spawnsFile, Steps: len(rows.Steps),
		Segments: append([]planprobe.Segment{}, segments...), Units: units, Misses: misses, Notes: notes,
	}
	if frep.Segments == nil {
		frep.Segments = []planprobe.Segment{}
	}
	for _, s := range frep.Segments {
		switch s.Verdict {
		case planprobe.VerdictBad:
			frep.Counts.Bad++
		case planprobe.VerdictWarn:
			frep.Counts.Warn++
		default:
			frep.Counts.OK++
		}
	}
	// Carry the walked segments in the probe's own text shape: the same
	// verdict lines plan-probe prints, with the spawn list named up front.
	prep := &planprobe.Report{Format: planprobe.Format, Version: planprobe.Version, Plan: rows.Plan.Plan,
		Map: rows.Plan.Map, Source: frep.Source, Mmaps: mmaps, Steps: len(rows.Steps), Notes: notes, Counts: frep.Counts}
	prep.Segments = frep.Segments
	if out != "" {
		if !planName.MatchString(frep.Plan) {
			return fail("the plan name %q cannot name a report file; probe without --out", frep.Plan)
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			return fail("%v", err)
		}
		var jb bytes.Buffer
		enc := json.NewEncoder(&jb)
		enc.SetIndent("", "  ")
		if err := enc.Encode(frep); err != nil {
			return fail("%v", err)
		}
		var text bytes.Buffer
		foreverProbeText(&text, frep, prep)
		jsonFile := filepath.Join(out, "probe-"+frep.Plan+".json")
		textFile := filepath.Join(out, "probe-"+frep.Plan+".txt")
		if err := os.WriteFile(jsonFile, jb.Bytes(), 0o644); err != nil {
			return fail("%v", err)
		}
		if err := os.WriteFile(textFile, text.Bytes(), 0o644); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "wrote %s and %s\n", jsonFile, textFile)
	} else if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(frep); err != nil {
			return fail("%v", err)
		}
	} else {
		foreverProbeText(stdout, frep, prep)
	}
	if frep.Counts.Bad > 0 || misses > 0 {
		return exitPlanProbeBad
	}
	return exitOK
}

func foreverProbePlans(reps []planexport.ForeverReport) string {
	names := make([]string, len(reps))
	for i, r := range reps {
		names[i] = r.Rows.Plan.Plan
	}
	return strings.Join(names, ", ")
}

// foreverProbe is the per-plan walker: the v16 planner and the probe-shaped query surface over it.
type foreverProbe struct {
	pl    *navmesh.Planner
	paths *foreverProbePaths
}

// foreverProbeRows are the positioned rows of the export in walk order.
func foreverProbeRows(rows planexport.Rows) []planexport.StepRow {
	steps := append([]planexport.StepRow(nil), rows.Steps...)
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].Leg != steps[j].Leg {
			return steps[i].Leg < steps[j].Leg
		}
		return steps[i].Seq < steps[j].Seq
	})
	var out []planexport.StepRow
	for _, s := range steps {
		if s.X != 0 || s.Y != 0 || s.Z != 0 {
			out = append(out, s)
		}
	}
	return out
}

// walk replays every hop of the export on the v16 tiles: the Forever walker's answer rendered through the probe's own
// segment judgement (probe internals are not exported, so the judgement here mirrors measure: floor gap, NOPATH,
// unreachable, off-mesh, detour, water, fall, snap, floor change). The floor-cut polygons come from the planner's own
// tiles, over the rows' bounding box.
func (f *foreverProbe) walk(rows planexport.Rows, fromStep int) ([]planprobe.Segment, []float32, []string, error) {
	steps := foreverProbeRows(rows)
	var notes []string
	notes = append(notes, "the spawn list carries no groups, links or waypoints: linked-pack and patrol lints are skipped (each spawn is judged alone)")
	cuts, err := f.cuts(rows)
	if err != nil {
		return nil, nil, nil, err
	}
	t := f.pl.Format().Terrain()
	waterBit := t.Water
	lavaBits := t.Magma | t.Slime
	var segs []planprobe.Segment
	for k := 1; k < len(steps); k++ {
		a, b := steps[k-1], steps[k]
		num := k + 1
		if fromStep > 0 && num < fromStep {
			continue
		}
		from := navmesh.Vec3{a.X, a.Y, a.Z}
		to := navmesh.Vec3{b.X, b.Y, b.Z}
		sp, err := f.paths.ServerPath(rows.Plan.Map, from, to)
		if err != nil {
			return nil, nil, nil, err
		}
		verdict := planProbeForeverVerdict(sp, from, to)
		seg := planprobe.Segment{
			From: planprobe.End{Step: k, Leg: a.Leg, Seq: a.Seq, Kind: a.Kind, Comment: a.Comment, X: a.X, Y: a.Y, Z: a.Z},
			To:   planprobe.End{Step: num, Leg: b.Leg, Seq: b.Seq, Kind: b.Kind, Comment: b.Comment, X: b.X, Y: b.Y, Z: b.Z},
		}
		seg.Straight = r2f(float32(foreverDist3(from, to)))
		endShort := float32(foreverDist3(sp.ActualEnd, to))
		seg.Server = planprobe.Server{Type: sp.Type, Verdict: verdict, Length: r2f(sp.Length), EndShort: r2f(endShort),
			StartPoly: r2f(sp.StartDist), EndPoly: r2f(sp.EndDist)}
		offline, err := f.paths.Plan(rows.Plan.Map, from, to)
		if err != nil {
			return nil, nil, nil, err
		}
		seg.Offline = planprobe.Offline{Status: string(offline.Status), Length: r2f(offline.Length)}
		seg.Ratio = 1
		if seg.Straight >= 1 {
			seg.Ratio = r2f(sp.Length / seg.Straight)
		}
		floors, err := f.paths.Floors(rows.Plan.Map, to[0], to[1], to[2]-40, to[2]+40)
		if err != nil {
			return nil, nil, nil, err
		}
		seg.Point = foreverProbePoint(floors, to)
		snap, ok, err := f.paths.Nearest(rows.Plan.Map, to)
		if err != nil {
			return nil, nil, nil, err
		}
		seg.Point.Snap = -1
		if ok {
			seg.Point.Snap = r2f(float32(foreverDist3(snap, to)))
		}
		if err := foreverProbeSurface(f.paths, rows.Plan.Map, offline, &seg, waterBit, lavaBits); err != nil {
			return nil, nil, nil, err
		}
		seg.FloorFrom = mapview.FloorIndex(cuts, a.Z)
		seg.FloorTo = mapview.FloorIndex(cuts, b.Z)
		if float64(seg.Straight) > dungeon.RunLeaderSeeYd {
			seg.Hop = true
		}
		var bad, warn []string
		pullLike := b.Kind == planexport.KindPull || b.Kind == planexport.KindBoss
		if !pullLike && seg.Point.FloorDZ > 5 {
			bad = append(bad, fmt.Sprintf("no floor within 5 yd (row z %.2f, floor %.2f)", b.Z, foreverProbeNearestZ(floors, to[2])))
		}
		switch {
		case sp.Type&navmesh.PathNoPath != 0:
			bad = append(bad, "server NOPATH")
		case verdict == "unreachable":
			bad = append(bad, "server unreachable")
		case verdict == "partial":
			bad = append(bad, fmt.Sprintf("server partial (the Forever walk stops %.1f yd short of the row)", endShort))
		case verdict == "off-mesh" && !(pullLike && float64(endShort) <= float64(foreverProbeRadius(b))+5):
			bad = append(bad, fmt.Sprintf("server off-mesh (end %.1f yd from the row)", endShort))
		}
		if seg.Ratio > 1.6 {
			warn = append(warn, fmt.Sprintf("path %.2fx the straight line", seg.Ratio))
		}
		if seg.WaterYd > 0 {
			warn = append(warn, fmt.Sprintf("water %.1f yd", seg.WaterYd))
		}
		if seg.LavaYd > 0 {
			warn = append(warn, fmt.Sprintf("lava %.1f yd", seg.LavaYd))
		}
		if seg.Steep {
			warn = append(warn, "steep")
		}
		switch {
		case seg.MaxDrop >= 14:
			warn = append(warn, fmt.Sprintf("fall %.1f yd (approximate 1.12 fall damage)", seg.MaxDrop))
		case seg.MaxDrop >= 6:
			warn = append(warn, fmt.Sprintf("drop %.1f yd", seg.MaxDrop))
		}
		if seg.Point.Snap > 2 {
			warn = append(warn, fmt.Sprintf("%.1f yd off the nearest walkable point", seg.Point.Snap))
		}
		if seg.FloorFrom != seg.FloorTo {
			warn = append(warn, fmt.Sprintf("floor change %d->%d", seg.FloorFrom, seg.FloorTo))
		}
		if seg.Point.FloorsHere >= 2 {
			warn = append(warn, fmt.Sprintf("multi-floor x,y: the row's z picks floor z=%.2f", b.Z))
		}
		if (offline.Status == navmesh.Complete) != (sp.Type&(navmesh.PathNoPath|navmesh.PathIncomplete) == 0) {
			warn = append(warn, fmt.Sprintf("offline %s, server %s", offline.Status, verdict))
		}
		if seg.Hop {
			warn = append(warn, fmt.Sprintf("%.0f yd hop: the module walks it in hops of its own", seg.Straight))
		}
		seg.Reasons = append(append([]string{}, bad...), warn...)
		switch {
		case len(bad) > 0:
			seg.Verdict = planprobe.VerdictBad
		case len(warn) > 0:
			seg.Verdict = planprobe.VerdictWarn
		default:
			seg.Verdict = planprobe.VerdictOK
		}
		segs = append(segs, seg)
	}
	return segs, cuts, notes, nil
}

// cuts are the map's floor cuts over the rows' bounding box, from the planner's own tiles.
func (f *foreverProbe) cuts(rows planexport.Rows) ([]float32, error) {
	var x0, y0, x1, y1 float32
	first := true
	for _, s := range rows.Steps {
		if s.X == 0 && s.Y == 0 && s.Z == 0 {
			continue
		}
		if first {
			x0, y0, x1, y1, first = s.X, s.Y, s.X, s.Y, false
			continue
		}
		if s.X < x0 {
			x0 = s.X
		}
		if s.X > x1 {
			x1 = s.X
		}
		if s.Y < y0 {
			y0 = s.Y
		}
		if s.Y > y1 {
			y1 = s.Y
		}
	}
	if first {
		return nil, nil
	}
	polys, _, err := f.paths.Polygons(rows.Plan.Map, x0, y0, x1, y1)
	if err != nil {
		return nil, err
	}
	return mapview.SplitHeights(polys, mapview.DefaultFloorOptions()), nil
}

func foreverProbePoint(floors []navmesh.Floor, to navmesh.Vec3) planprobe.Point {
	p := planprobe.Point{Floors: make([]planprobe.Floor, 0, len(floors)), FloorDZ: -1, Snap: -1}
	var here int
	var last float32
	seen := false
	sorted := append([]navmesh.Floor(nil), floors...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Z < sorted[j].Z })
	for _, fl := range sorted {
		p.Floors = append(p.Floors, planprobe.Floor{Z: r2f(fl.Z), Flags: fl.Flags})
		if !seen || math.Abs(float64(fl.Z-last)) >= 6 {
			here++
			seen = true
		}
		last = fl.Z
	}
	p.FloorsHere = here
	if len(floors) > 0 {
		p.FloorDZ = r2f(float32(math.Abs(float64(foreverProbeNearestZ(floors, to[2])) - float64(to[2]))))
	}
	return p
}

func foreverProbeNearestZ(floors []navmesh.Floor, z float32) float32 {
	best := floors[0].Z
	bestD := math.Abs(float64(floors[0].Z - z))
	for _, fl := range floors[1:] {
		if d := math.Abs(float64(fl.Z - z)); d < bestD {
			best, bestD = fl.Z, d
		}
	}
	return best
}

// foreverProbeSurface samples the offline path the way the probe does: water and lava yards, climbs, drops and steep
// flags, over corners and ground samples. The bit arguments are the tile set's own water and lava flags: the old
// 1.12 bits name other surfaces on v16 tiles.
func foreverProbeSurface(nav *foreverProbePaths, mapID uint32, offline navmesh.Path, seg *planprobe.Segment, water, lava uint16) error {
	var samples []navmesh.Vec3
	for i, pt := range offline.Points {
		samples = append(samples, pt)
		if i < len(offline.Ground) {
			samples = append(samples, offline.Ground[i]...)
		}
	}
	var prev navmesh.Vec3
	for i, s := range samples {
		floors, err := nav.Floors(mapID, s[0], s[1], s[2]-3, s[2]+3)
		if err != nil {
			return err
		}
		var flags uint16
		if len(floors) > 0 {
			near := foreverProbeNearestZ(floors, s[2])
			for _, fl := range floors {
				if fl.Z == near {
					flags = fl.Flags
					break
				}
			}
		}
		if i > 0 {
			d := float32(foreverDist3(prev, s))
			if flags&water != 0 {
				seg.WaterYd += d
			}
			if flags&lava != 0 {
				seg.LavaYd += d
			}
			dz := s[2] - prev[2]
			if dz > seg.MaxClimb {
				seg.MaxClimb = dz
			}
			if -dz > seg.MaxDrop {
				seg.MaxDrop = -dz
			}
			dx := float64(s[0] - prev[0])
			dy := float64(s[1] - prev[1])
			if h := math.Sqrt(dx*dx + dy*dy); math.Abs(float64(dz)) >= 2 && h > 0 && math.Abs(float64(dz))/h > 1 {
				seg.Steep = true
			}
		}
		prev = s
	}
	seg.WaterYd = r2f(seg.WaterYd)
	seg.LavaYd = r2f(seg.LavaYd)
	seg.MaxClimb = r2f(seg.MaxClimb)
	seg.MaxDrop = r2f(seg.MaxDrop)
	return nil
}

// foreverProbeRadius is the pull or boss radius of a row, with the module's defaults for 0 (15 yd pull, 30 yd boss).
func foreverProbeRadius(s planexport.StepRow) float32 {
	if s.Radius > 0 {
		return s.Radius
	}
	if s.Kind == planexport.KindBoss {
		return planexport.DefaultBossRad
	}
	return planexport.DefaultPullYd
}

func r2f(v float32) float32 { return float32(math.Round(float64(v)*100) / 100) }

// foreverProbeUnits matches every pull, boss and patrol unit row of the export against the spawn list by entry plus
// position, keyed to its step row by leg and seq. Pack and patrol rows stand away from any step point (the plan's
// anchor is elsewhere), so they are searched with the step's own pull radius plus the same slack the targeting model
// allows when a pull stands off its pack. Boss and first-add rows stand at the boss's point and are searched with
// the boss radius plus the same slack. A successful match names the spawn's GUID and distance; a miss is reported
// with the radius it was searched in.
func foreverProbeUnits(steps []planexport.StepRow, units []planexport.UnitRow, spawns []worldspawn.Spawn) ([]foreverProbeUnit, int) {
	out := make([]foreverProbeUnit, 0, len(units))
	misses := 0
	radiusByStep := make(map[[2]int]float32, len(steps))
	for _, s := range steps {
		radiusByStep[[2]int{s.Leg, s.Seq}] = s.Radius
	}
	for _, u := range units {
		if u.Role != planexport.UnitPack && u.Role != planexport.UnitBoss && u.Role != planexport.UnitFirst && u.Role != planexport.UnitPatrol {
			continue
		}
		pu := foreverProbeUnit{Leg: u.Leg, Seq: u.Seq, Idx: u.Idx, Entry: u.Entry, Role: u.Role, X: u.X, Y: u.Y, Z: u.Z}
		r0 := float32(15 + 5)
		if u.Role == planexport.UnitBoss || u.Role == planexport.UnitFirst {
			r0 = planexport.DefaultBossRad + 5
		} else if rad := radiusByStep[[2]int{u.Leg, u.Seq}]; rad > 0 {
			r0 = rad + 5
		}
		if s, d, ok := foreverProbeMatch(spawns, u.Entry, u.X, u.Y, u.Z, r0); ok {
			pu.Found = true
			pu.GUID = s.GUID
			pu.DistYd = r2f(float32(d))
		} else {
			pu.Missing = true
			misses++
		}
		out = append(out, pu)
	}
	return out, misses
}

// foreverProbeText renders the Forever report in the probe's own text shape: one line per non-ok segment, one per
// missing unit, then the counts.
func foreverProbeText(w io.Writer, frep *foreverProbeReport, prep *planprobe.Report) {
	for _, s := range prep.Segments {
		if s.Verdict == planprobe.VerdictOK {
			continue
		}
		comment := s.To.Kind
		if s.To.Comment != "" {
			comment = s.To.Kind + " '" + s.To.Comment + "'"
		}
		reasons := strings.Join(s.Reasons, "; ")
		fmt.Fprintf(w, "step %d->%d (%s) %s: %s\n", s.From.Step, s.To.Step, comment, strings.ToUpper(s.Verdict), reasons)
	}
	for _, u := range frep.Units {
		if !u.Missing {
			continue
		}
		fmt.Fprintf(w, "unit leg %d seq %d idx %d (%s entry %d at %.1f %.1f %.1f) MISSING: no spawn of entry %d within the search radius\n",
			u.Leg, u.Seq, u.Idx, u.Role, u.Entry, u.X, u.Y, u.Z, u.Entry)
	}
	fmt.Fprintf(w, "%s: %d steps, %d segments: %d ok, %d warn, %d bad; %d units, %d missing\n", frep.Plan, frep.Steps,
		len(prep.Segments), frep.Counts.OK, frep.Counts.Warn, frep.Counts.Bad, len(frep.Units), frep.Misses)
	if frep.Spawns != "" {
		fmt.Fprintf(w, "note: spawns %s\n", frep.Spawns)
	}
	for _, n := range frep.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
}
