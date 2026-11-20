package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/planexport"
)

// Stage-check's Forever mode: check where the party stages for the leg on the
// v16 tiles, with the leg's first row read from the run leader's exported
// migration (plan-export --forever output) instead of rebuilt from the plan
// file. The old-server input path (the plan JSON rebuilt through the v6
// cutter) is refused in this mode: rows rebuilt against one tile set prove
// nothing about the other, and the exporter option sets of stageFirstRow
// (plain, cut hops, approach hops, avoid-box routes) are the run leader's,
// not the module's.
//
// Anything the Forever inputs do not carry is skipped with a note, never
// guessed: the module's own first-row rule (the migration's first move row
// of the leg is the row) replaces stageFirstRow's option-set search. When
// the leg has no move row in the migration, the leg's first plan point is
// used with a note saying the rows have none.

// foreverStageNav adapts the v16 planner to the staging check's navmesh
// surface. The staging walk replays the module's own walk query: the
// Forever walker (PathTo) stands in for the 1.12 ServerPath, with the same
// arrival radius and the same up-to-three-legs walk. The offline Plan,
// Nearest and avoid marks are the planner's own answers on its own tiles.
type foreverStageNav struct {
	pl *navmesh.Planner
}

func (f *foreverStageNav) Plan(mapID uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	return f.pl.Plan(mapID, from, to)
}

func (f *foreverStageNav) Raycast(mapID uint32, from, to navmesh.Vec3) (navmesh.Ray, error) {
	return f.pl.Raycast(mapID, from, to)
}

func (f *foreverStageNav) Nearest(mapID uint32, pt navmesh.Vec3) (navmesh.Vec3, bool, error) {
	return f.pl.Nearest(mapID, pt)
}

func (f *foreverStageNav) SetAvoid(mapID uint32, boxes []navmesh.Box) {
	f.pl.SetAvoid(mapID, boxes)
}

// ServerPath replays the staging walk's server query on the v16 tiles: up to
// three legs of the Forever walk, inside the arrival radius counts as
// arrived. A complete walk is the 1.12 normal path; a partial one keeps the
// reach it has as an incomplete path whose end is where the walker stopped;
// a walk with no corridor is NOPATH from the start.
func (f *foreverStageNav) ServerPath(mapID uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	route, err := f.pl.PathTo(mapID, from, to)
	if err != nil {
		return navmesh.ServerPath{}, err
	}
	sp := navmesh.ServerPath{ActualEnd: route.End, StartDist: route.StartDistance, EndDist: route.EndDistance}
	for i := 1; i < len(route.Points); i++ {
		d := route.Points[i-1]
		e := route.Points[i]
		dx, dy, dz := float64(e[0]-d[0]), float64(e[1]-d[1]), float64(e[2]-d[2])
		sp.Length += float32(math.Sqrt(dx*dx + dy*dy + dz*dz))
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

// stageCheckForever stages the leg on the v16 tiles with the migration's
// first move row (or the leg's first plan point when the migration has no
// move row for the leg). The migration fixes the exported rows, so there is
// no --server-steps option-set search here: stageCheckCmd rejects the flag
// in Forever mode.
func stageCheckForever(plan *dungeon.Plan, leg int, mmaps, sqlFile, planFlag, name string, clearedThrough int, asJSON bool, stdout, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "stage-check: "+format+"\n", a...)
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
	want := planFlag
	if want == "" {
		want = name
	}
	var rows *planexport.Rows
	for i := range reps {
		if reps[i].Rows.Plan.Plan == want {
			rows = &reps[i].Rows
			break
		}
	}
	if rows == nil {
		names := make([]string, len(reps))
		for i := range reps {
			names[i] = reps[i].Rows.Plan.Plan
		}
		joined := ""
		for i, n := range names {
			if i > 0 {
				joined += ", "
			}
			joined += n
		}
		if planFlag != "" {
			return fail("%s holds no plan %q (it holds %s)", sqlFile, planFlag, joined)
		}
		return fail("%s holds no plan %q (it holds %s)", sqlFile, name, joined)
	}
	if rows.Plan.Map != plan.Map {
		return fail("%s plan %s is map %d, the plan file is map %d: staging one against the other proves nothing", sqlFile, want, rows.Plan.Map, plan.Map)
	}
	rowLeg, ok := rows.LegNumber(plan, leg)
	if !ok {
		return fail("leg %s is not in the rows (skipped or optional in the export): the migration's first row cannot stage it; stage from the plan file instead", plan.Legs[leg].Name)
	}
	planner := navmesh.NewForeverPlanner(mmaps, nil)
	defer planner.Close()
	// One query tells whether the navmesh can answer at all (no cgo, no
	// v16 tiles for the map, a wrong tile generation): that is exit 2, as
	// plan-check's and trash-check's is, not a refused leg. The leg stages
	// from the migration's first move row (else the plan point below), so
	// probe at the point the staging walk actually starts.
	fr, haveFR, fnote := foreverStageFirstRow(*rows, rowLeg, plan, leg)
	probeAt := plan.LegFirstPoint(leg)
	if haveFR {
		probeAt = fr
	}
	probeV := navmesh.Vec3{probeAt.X, probeAt.Y, probeAt.Z}
	if _, err := planner.PathTo(plan.Map, probeV, probeV); err != nil {
		if reason, unavailable := planCheckUnavailable(err); unavailable {
			return fail("--mmaps %s holds no usable v16 tiles for map %d: %s", mmaps, plan.Map, reason)
		}
		return fail("--mmaps %s: %v", mmaps, err)
	}
	nav := &foreverStageNav{pl: planner}
	if boxes := dungeon.NavBoxes(plan.Avoid); len(boxes) > 0 {
		nav.SetAvoid(plan.Map, boxes)
	}
	opt := dungeon.StageOptions{ClearedThrough: clearedThrough}
	if haveFR {
		opt.FirstRow = &fr
	}
	res, err := dungeon.StageLeg(plan, leg, nav, opt)
	if err != nil {
		return fail("%v", err)
	}
	firstSource := "plan"
	if haveFR {
		firstSource = "rows"
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(buildStageCheckJSON(plan, res, firstSource, fnote))
	} else {
		printStageCheck(stdout, plan, res, firstSource, fnote)
	}
	if res.OK {
		return exitOK
	}
	return exitStageRefused
}

// foreverStageFirstRow is the migration's first move row of the exported leg:
// the module starts the leg at its first row, so the staging check starts
// there too. A leg with no move row in the migration (a leg the export
// starts with a fight) falls back to the leg's first plan point with a note.
func foreverStageFirstRow(rows planexport.Rows, rowLeg int, plan *dungeon.Plan, leg int) (dungeon.Point, bool, string) {
	for _, s := range rows.Steps {
		if s.Leg != rowLeg {
			continue
		}
		if s.Kind != planexport.KindMove && s.Kind != planexport.KindMoveDirect && s.Kind != planexport.KindMoveHopExact {
			continue
		}
		if s.X == 0 && s.Y == 0 && s.Z == 0 {
			continue
		}
		return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}, true, ""
	}
	return dungeon.Point{}, false, "the migration holds no move row for leg " + plan.Legs[leg].Name + ": the leg's first plan point is used"
}
