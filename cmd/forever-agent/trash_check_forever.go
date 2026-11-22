package main

import (
	"encoding/json"
	"fmt"
	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/patrols"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/trashcheck"
	"github.com/pjerra/forever-agent/internal/worldspawn"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Trash-check's Forever mode: report what one walk of the run leader's exported migration (plan-export --forever output)
// wakes, matching every pull, boss and patrol unit against the spawn list by entry plus position (a creature GUID
// carries no spawn id, so plan-check --forever matches the same way). The old-server inputs (the world SQL dump with
// its groups, links and waypoint tables, the v6 tiles) are refused in this mode: the spawn list carries none of those
// tables, and rows rebuilt against one tile set prove nothing about the other.
//
// The spawns are --spawns: a world export (export-forever.sh: one map's folder, or a root of map<N> folders) or the thin
// spawn list. A world export carries the formation groups, factions and waypoint loops, so linked packs and patrol windows
// are judged; the thin list carries none of that and the notes say so, never guessing:
//   - linked packs: ForeverCore has no creature_linking tables, so a pack is its creature_formations group (none in the list);
//   - patrols: the list has no waypoint loops, so no patrol window is simulated unless a tb-patrols recording (--patrols)
//     gives one. The plan's own patrol paths do not stand in: they are waypoints without waits or periods, and a guessed
//     period would invent windows.
//   - the family assist hop runs over the faction template (the list carries no flags_extra).
// The assist ring defaults to the port's 20 yd (foreverAssistYd); the spread and aggro numbers already match.

// foreverAssistYd is the family assist ring Forever mode defaults to: the port's kAggroFamilyAssistMinYd
// (ai/playerbot/PullAggroEstimatePolicy.h:90), the least the run leader's pull estimate takes a call for help to carry. The
// core's own CreatureFamilyAssistanceRadius is 10 yd (:86), but the cross-pack wakes measured on the Zul'Gurub terrace were
// spawn pairs 11.0-19.5 yd apart (:87-89), so the estimate never goes under 20. The Turtle run leader's 10 yd is the
// --assist-yd default of the old mode only. The spread (3 yd party, 10 yd raid: kSpreadSettledDistance) and the aggro
// ring's reach are the model's own and match (aggroest.TestConstantsMirrorTheRunLeader).
const foreverAssistYd = 20.0

func foreverAssistSource(given bool) string {
	if given {
		return "--assist-yd"
	}
	return "the port's pull estimate never goes under 20 yd (kAggroFamilyAssistMinYd); --assist-yd sets it"
}

// foreverTrashWorldNotes says what the spawn input did and did not carry. A world export carries groups (the
// creature_formations rows), factions and flags_extra's not-aggro bit; its waypoint loops make patrols, with the waits the
// core has. The thin list carries none of that: every spawn walks alone and no patrol is simulated unless a tb-patrols
// recording gives one. ForeverCore has no creature_linking tables, so a pack is its formation group in both.
func foreverTrashWorldNotes(fw *foreverSpawns) []string {
	if fw.Full {
		return []string{"the spawns are a world export: packs are its creature_formations groups (ForeverCore has no creature_linking), patrols are its waypoint loops, the family assist hop matches the faction template and skips a spawn the export flags not-aggro"}
	}
	return []string{
		"the spawn list carries no groups or links: every spawn walks alone (no linked packs; pass a world export folder for them)",
		"the spawn list carries no waypoint loops: no patrol is simulated from it (pass a world export folder or a tb-patrols --patrols recording for patrol windows)",
		"the family assist hop matches on faction alone (the list carries no flags): a spawn the flags would silence still assists",
	}
}

// trashCheckForever runs the Forever mode: the exported migration's rows against the spawn list, routes on the v16
// tiles when --mmaps is given, straight lines otherwise.
func trashCheckForever(dir, sqlFile, planFlag, spawnsFile, mmaps string, level int, spread, rate, assist float64, haveLevel, haveSpread, haveAssist, asJSON bool, out, dashOut, failOn string, patrolFiles []string, stdout, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "trash-check: "+format+"\n", a...)
		return exitUsage
	}
	switch failOn {
	case "", "danger", "patrol", "any":
	default:
		return fail("--fail-on %q: danger, patrol or any", failOn)
	}
	data, err := os.ReadFile(sqlFile)
	if err != nil {
		return fail("%v", err)
	}
	reps, err := planexport.ParseForever(string(data))
	if err != nil {
		return fail("%s: %v", sqlFile, err)
	}
	var sel []planexport.ForeverReport
	for _, r := range reps {
		if planFlag == "" || r.Rows.Plan.Plan == planFlag {
			sel = append(sel, r)
		}
	}
	switch {
	case len(sel) == 0 && planFlag != "":
		return fail("%s holds no plan %q (it holds %s)", sqlFile, planFlag, foreverTrashPlans(reps))
	case len(sel) == 0:
		return fail("%s holds no plan rows", sqlFile)
	case len(sel) > 1:
		return fail("%s holds %d plans: pick one with --plan (%s)", sqlFile, len(sel), foreverTrashPlans(sel))
	}
	rep := sel[0]
	rows := rep.Rows
	if !planName.MatchString(rows.Plan.Plan) {
		return fail("the plan name %q is not a plan name (a-z, 0-9, _ and -)", rows.Plan.Plan)
	}
	fw, err := loadForeverWorld(spawnsFile, rows.Plan.Map)
	if err != nil {
		return fail("%v", err)
	}
	w, notes := fw.World, fw.Notes
	meta, metaNote := trashForeverMeta(dir, rows)
	if metaNote != "" {
		notes = append(notes, metaNote)
	}
	lvl := level
	if !haveLevel {
		if meta == nil || len(meta.LevelBand) != 2 {
			return fail("--level is required: the plan file %s is not there (or gives no level_band)", filepath.Join(dir, rows.Plan.Plan+".json"))
		}
		lvl = meta.LevelBand[0]
	}
	if lvl < 1 || lvl > 60 {
		return fail("--level %d: 1-60", lvl)
	}
	sp := spread
	if !haveSpread {
		if meta != nil && meta.IsRaid() {
			sp = 10
		} else {
			sp = 3
		}
	}
	if sp < 0 {
		return fail("--spread %g: not negative", sp)
	}
	if rate <= 0 {
		return fail("--aggro-rate %g: a positive scale", rate)
	}
	if !haveAssist {
		assist = foreverAssistYd
	}
	notes = append(notes, foreverTrashWorldNotes(fw)...)
	notes = append(notes, fmt.Sprintf("the family assist hop reaches %g yd (%s)", assist, foreverAssistSource(haveAssist)),
		"the plan's own patrol paths are not simulated: waypoints without waits or periods invent no windows")
	var recorded []patrols.Patrol
	for _, path := range patrolFiles {
		f, err := patrols.Read(path)
		if err != nil {
			return fail("%v", err)
		}
		if f.Map != rows.Plan.Map {
			return fail("%s is a recording of map %d, the plan is map %d", path, f.Map, rows.Plan.Map)
		}
		recorded = append(recorded, f.Patrols...)
	}
	opt := trashcheck.Options{
		Level: lvl, Spread: float32(sp), AggroRate: float32(rate), AssistYd: float32(assist),
		Recorded: recorded,
	}
	if mmaps != "" {
		planner := navmesh.NewForeverPlanner(mmaps, nil)
		defer planner.Close()
		// One query tells whether the navmesh can answer at all (no cgo, no files for the map): that is exit 1, as
		// plan-check's is, not a silent straight-line route.
		if p, ok := firstPoint(rows); ok {
			v := navmesh.Vec3{p.X, p.Y, p.Z}
			route, err := planner.PathTo(rows.Plan.Map, v, v)
			if err != nil {
				if reason, unavailable := planCheckUnavailable(err); unavailable {
					return fail("%s", reason)
				}
				return fail("%v", err)
			}
			_ = route
		}
		opt.Nav = &foreverTrashNav{pl: planner}
	}
	check := trashcheck.Check(rows, w, opt)
	check.Source = "forever-sql " + sqlFile
	check.SpawnsDir = fw.Path
	check.Notes = append(check.Notes, notes...)
	if unplanned := foreverTrashUnplanned(rep.Units, w, check); len(unplanned) > 0 {
		check.Notes = append(check.Notes, unplanned...)
	}
	if out != "" {
		if err := writeTrashCheck(out, check); err != nil {
			return fail("%v", err)
		}
	}
	if dashOut != "" {
		if err := writeDashPatrols(dashOut, check); err != nil {
			return fail("%v", err)
		}
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(check); err != nil {
			return fail("%v", err)
		}
	} else {
		fmt.Fprint(stdout, check.Text())
	}
	if failOn != "" && check.Finds(failOn) {
		return exitTrashCheckFound
	}
	return exitOK
}

func foreverTrashPlans(reps []planexport.ForeverReport) string {
	names := make([]string, len(reps))
	for i, r := range reps {
		names[i] = r.Rows.Plan.Plan
	}
	return strings.Join(names, ", ")
}

// trashForeverMeta reads the plan file beside the migration for the level band and the raid size (the sweep mode).
// A mismatch on the map refuses it: the rows and the plan would be different plans.
func trashForeverMeta(dir string, rows planexport.Rows) (*dungeon.Plan, string) {
	p, err := dungeon.Load(filepath.Join(dir, rows.Plan.Plan+".json"))
	if err != nil {
		return nil, ""
	}
	if p.Map != rows.Plan.Map {
		return nil, fmt.Sprintf("the plan file %s is map %d, the rows are map %d: level and raid size need --level/--spread",
			rows.Plan.Plan+".json", p.Map, rows.Plan.Map)
	}
	return p, ""
}

// foreverTrashNav adapts the v16 planner to the trash model's old-server route query. ServerPath is the only piece
// that changes generation: the Forever walker (PathTo) stands in for the 1.12 core's exact query, and its corners
// are the route the model samples. A partial walk keeps the reach it has; a walk with no corridor falls back the way
// the model does without a navmesh (a straight line, counted).
func foreverTrashNavRoute(pl *navmesh.Planner, mapID uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	route, err := pl.PathTo(mapID, from, to)
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

// foreverTrashNav is the trash model's Nav over the v16 tiles.
type foreverTrashNav struct {
	pl *navmesh.Planner
}

func (f *foreverTrashNav) ServerPath(mapID uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	return foreverTrashNavRoute(f.pl, mapID, from, to)
}

// foreverTrashUnplanned names the export's units no spawn matches: the trash model assigns every pack the first
// pull, boss or hold row that clears it by entry and distance (targetEntries/packClearedBy), so a unit whose entry
// stands nowhere near the plan is unplanned there too — but only when the spawn list truly lacks it. A missing unit
// here means the plan pulls a pack the list does not have, and the report's packs would silently miss a danger.
func foreverTrashUnplanned(units []planexport.UnitRow, w *worldspawn.World, check *trashcheck.Report) []string {
	byEntry := map[uint32][]worldspawn.Spawn{}
	for _, s := range w.Spawns {
		byEntry[s.Entry] = append(byEntry[s.Entry], s)
	}
	seen := map[string]bool{}
	var out []string
	for _, u := range units {
		if u.Role != planexport.UnitPack && u.Role != planexport.UnitBoss && u.Role != planexport.UnitFirst {
			continue
		}
		key := string(u.Role) + "\x00" + strconv.Itoa(int(u.Entry))
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(byEntry[u.Entry]) > 0 {
			continue
		}
		out = append(out, fmt.Sprintf("unit %s entry %d (leg %d seq %d): no spawn of that entry in the list, so the trash model cannot clear it (unplanned there too)",
			u.Role, u.Entry, u.Leg, u.Seq))
	}
	sort.Strings(out)
	_ = check
	return out
}
