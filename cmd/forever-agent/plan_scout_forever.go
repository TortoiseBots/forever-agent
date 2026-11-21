// plan_scout_forever.go: plan-scout's Forever mode (K4). The offline draft reads a ForeverCore world export with
// worldspawn.LoadForeverDB on a v16 navmesh; the live pass drives a running `forever serve` slot through serveclient
// (GM lines and the units/path_to/walk_path methods), never the Eluna toolkit. The pass fills the levels the core has
// no row for from the live units read, then the report is rebuilt so every aggro and joiner number uses them.
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/planlint"
	"github.com/pjerra/forever-agent/internal/planscout"
	"github.com/pjerra/forever-agent/internal/world"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

const (
	// scoutForeverOnlineWait is how long the live pass waits for the session to be in the world.
	scoutForeverOnlineWait = 60 * time.Second
	// scoutForeverUnitsYd is the units read at each stop (the spec's `units {within:40}`).
	scoutForeverUnitsYd = 40
	// scoutForeverPoll is the status poll interval of the waits.
	scoutForeverPoll = 500 * time.Millisecond
)

// scoutForeverClient is the part of serveclient the Forever live pass uses; the tests inject a fake.
type scoutForeverClient interface {
	Status(context.Context) (serveclient.Status, error)
	GM(context.Context, string) (serveclient.Command, error)
	Units(context.Context, serveclient.UnitsParams) ([]serveclient.Unit, error)
	PathTo(context.Context, float32, float32, float32) (serveclient.Path, error)
	WalkPath(context.Context, float32, float32, float32) (serveclient.Path, error)
}

// planScoutOpenForever opens the v16 planner of the Forever draft; the tests replace it.
var planScoutOpenForever = func(dir string, parents map[uint32]uint32) planScoutNav {
	return navmesh.NewForeverPlanner(dir, parents)
}

// scoutForeverOpen opens the serve slot dir the live pass drives; the tests replace it.
var scoutForeverOpen = func(dir string) (scoutForeverClient, error) {
	return serveclient.Open(dir)
}

// foreverParents is --parent MAP=PARENT, repeatable: the server's parent-map rule for the v16 tiles.
type foreverParents map[uint32]uint32

func (p foreverParents) String() string {
	if len(p) == 0 {
		return ""
	}
	keys := make([]int, 0, len(p))
	for k := range p {
		keys = append(keys, int(k))
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d=%d", k, p[uint32(k)]))
	}
	return strings.Join(parts, ",")
}

func (p *foreverParents) Set(s string) error {
	eq := strings.IndexByte(s, '=')
	if eq <= 0 || eq == len(s)-1 {
		return fmt.Errorf("--parent takes MAP=PARENT, not %q", s)
	}
	m, err := strconv.ParseUint(s[:eq], 10, 32)
	if err != nil {
		return fmt.Errorf("--parent: %q is not a map id", s[:eq])
	}
	parent, err := strconv.ParseUint(s[eq+1:], 10, 32)
	if err != nil {
		return fmt.Errorf("--parent: %q is not a map id", s[eq+1:])
	}
	if *p == nil {
		*p = foreverParents{}
	}
	(*p)[uint32(m)] = uint32(parent)
	return nil
}

// foreverLiveArgs splits the Forever live pass's flags out of args before the flag package reads them: in Forever mode
// `--live` is a bool and `--slot` is a serve dir, while the turtle pass's --live takes a server name and --slot a number.
func foreverLiveArgs(args []string) (rest []string, slot string, err error) {
	forever := false
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--forever" || a == "-forever" {
			forever = true
		}
	}
	if !forever {
		return args, "", nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--live" || a == "-live":
			rest = append(rest, "--live=forever")
		case strings.HasPrefix(a, "--slot=") || strings.HasPrefix(a, "-slot="):
			if slot != "" {
				return nil, "", fmt.Errorf("--slot names one serve dir")
			}
			slot = a[strings.IndexByte(a, '=')+1:]
		case a == "--slot" || a == "-slot":
			if slot != "" {
				return nil, "", fmt.Errorf("--slot names one serve dir")
			}
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("--slot needs a serve dir")
			}
			i++
			slot = args[i]
		default:
			rest = append(rest, a)
		}
	}
	return rest, slot, nil
}

// foreverUnknownLevels reads the export's creatures.tsv for the spawns whose level_max is empty: LoadForeverDB assumes
// 60 for them, and the live pass replaces that with the unit's level when the server says one.
func foreverUnknownLevels(dir string) (map[uint64]bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, "creatures.tsv"))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("%s: no creatures.tsv header", dir)
	}
	col := map[string]int{}
	for i, name := range strings.Split(lines[0], "\t") {
		col[strings.TrimSpace(name)] = i
	}
	gi, okG := col["guid"]
	li, okL := col["level_max"]
	if !okG || !okL {
		return nil, fmt.Errorf("%s: creatures.tsv has no guid/level_max header", dir)
	}
	out := map[uint64]bool{}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) <= gi {
			continue
		}
		guid, err := strconv.ParseUint(strings.TrimSpace(f[gi]), 10, 64)
		if err != nil {
			continue
		}
		if li < len(f) && strings.TrimSpace(f[li]) == "" {
			out[guid] = true
		}
	}
	return out, nil
}

// scoutForeverFillLevels replaces the assumed level of every unknown spawn whose entry a live unit names, and returns
// how many spawns it filled.
func scoutForeverFillLevels(w *worldspawn.World, units map[int][]world.Unit, unknown map[uint64]bool) int {
	if w == nil || len(unknown) == 0 || len(units) == 0 {
		return 0
	}
	steps := make([]int, 0, len(units))
	for step := range units {
		steps = append(steps, step)
	}
	sort.Ints(steps)
	// A unit's low 24 bits name the spawn it was created from; the entry is the fallback when the export and the live
	// world disagree on the spawn id.
	byGUID, byEntry := map[uint64]int{}, map[uint32]int{}
	for _, step := range steps {
		for _, u := range units[step] {
			if u.Level <= 1 {
				continue
			}
			if g := uint64(world.Low24(u.GUID)); g != 0 {
				if _, seen := byGUID[g]; !seen {
					byGUID[g] = int(u.Level)
				}
			}
			if _, seen := byEntry[u.Entry]; !seen {
				byEntry[u.Entry] = int(u.Level)
			}
		}
	}
	filled := 0
	for i := range w.Spawns {
		s := &w.Spawns[i]
		if !unknown[s.GUID] {
			continue
		}
		lv, ok := byGUID[s.GUID]
		if !ok {
			lv, ok = byEntry[s.Entry]
		}
		if ok && lv != s.LevelMax {
			s.LevelMax = lv
			filled++
		}
	}
	return filled
}

// runScoutForeverLive is the Forever live pass: online wait, GM mode, an instance unbind, a `.go xyz` and a units read
// per stop, the server's route for every hop and a walk of the risky ones within the budget. It returns the exit code the
// turtle pass uses: 0 done, 28 aborted part-way, 18 a phase refused.
func runScoutForeverLive(ctx context.Context, o *planScoutOptions, in planscout.Input, rep *planscout.Report,
	w *worldspawn.World, nav planscout.Nav) (planscout.LiveData, int, string) {
	data := planscout.LiveData{
		Paths:      map[int]planscout.ScoutPath{},
		ExtraPaths: map[int][]planscout.ScoutPath{},
		Walks:      map[int]planscout.WalkResult{},
		Units:      map[int][]world.Unit{},
		LOS:        map[planscout.LOSKey]bool{},
	}
	abort := func(reason string) (planscout.LiveData, int, string) {
		data.Aborted = true
		data.AbortReason = reason
		return data, exitPlanScoutLiveAborted, reason
	}
	client, err := scoutForeverOpen(o.serveSlot)
	if err != nil {
		return data, exitLivePhaseRefused, "the serve slot: " + err.Error()
	}
	if err := foreverWaitOnline(ctx, client); err != nil {
		return data, exitLivePhaseRefused, err.Error()
	}
	planMap := in.Rows.Plan.Map
	for _, line := range []string{".gm on", ".instance unbind all"} {
		if !scoutAllowedLine(line, planMap, 0) {
			return data, exitLivePhaseRefused, fmt.Sprintf("%q is not on the scout's allow-list", line)
		}
		if _, err := client.GM(ctx, line); err != nil {
			return data, exitLivePhaseRefused, fmt.Sprintf("%s: %v", line, err)
		}
	}
	for _, group := range planscout.LiveStops(planscout.LiveSpots(rep, in, w, nil)) {
		if err := foreverStandAt(ctx, client, group[0].Point, planMap); err != nil {
			return abort(err.Error())
		}
		units, err := client.Units(ctx, serveclient.UnitsParams{Within: scoutForeverUnitsYd})
		if err != nil {
			return abort(fmt.Sprintf("units at %.1f %.1f %.1f: %v", group[0].Point.X, group[0].Point.Y, group[0].Point.Z, err))
		}
		wu := make([]world.Unit, 0, len(units))
		for _, u := range units {
			wu = append(wu, foreverWorldUnit(u))
		}
		for _, spot := range group {
			data.Units[spot.Step] = wu
		}
	}
	budget := o.walkBudgetD
	if budget <= 0 {
		budget = 5 * time.Minute
	}
	deadline := time.Now().Add(budget)
	for _, hop := range planscout.LiveHops(rep, in, nav) {
		if !hop.Primary {
			continue
		}
		route, err := client.PathTo(ctx, hop.Point.X, hop.Point.Y, hop.Point.Z)
		if err != nil {
			return abort(fmt.Sprintf("path_to %.1f %.1f %.1f: %v", hop.Point.X, hop.Point.Y, hop.Point.Z, err))
		}
		data.Paths[hop.Step] = foreverScoutPath(route, hop.Point)
		if !foreverWalkWanted(rep, hop.Step) {
			continue
		}
		if time.Now().After(deadline) {
			data.Walks[hop.Step] = planscout.WalkResult{Skipped: "walk budget"}
			continue
		}
		walked, walkErr := client.WalkPath(ctx, hop.Point.X, hop.Point.Y, hop.Point.Z)
		if walkErr == nil {
			data.Paths[hop.Step] = foreverScoutPath(walked, hop.Point)
		}
		st, err := client.Status(ctx)
		if err != nil {
			return abort(fmt.Sprintf("status after the walk to %.1f %.1f %.1f: %v", hop.Point.X, hop.Point.Y, hop.Point.Z, err))
		}
		// A refused walk (no full route) stopped where the session stands: the check measures the shortfall.
		data.Walks[hop.Step] = planscout.WalkResult{Stop: dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}, Length: float64(walked.Length)}
	}
	return data, exitOK, ""
}

// foreverWaitOnline waits for the session to be in the world.
func foreverWaitOnline(ctx context.Context, c scoutForeverClient) error {
	deadline := time.Now().Add(scoutForeverOnlineWait)
	for {
		st, err := c.Status(ctx)
		if err != nil {
			return fmt.Errorf("status: %v", err)
		}
		if st.State == "online" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the session is not in the world after %s (state %q)", scoutForeverOnlineWait, st.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(scoutForeverPoll):
		}
	}
}

// foreverStandAt teleports the GM to a stop with `.go xyz` and waits for the status to show it.
func foreverStandAt(ctx context.Context, c scoutForeverClient, p dungeon.Point, planMap uint32) error {
	line := fmt.Sprintf(".go xyz %s %s %s %d", foreverNum(p.X), foreverNum(p.Y), foreverNum(p.Z), planMap)
	if !scoutAllowedLine(line, planMap, 0) {
		return fmt.Errorf("%q is not on the scout's allow-list", line)
	}
	if _, err := c.GM(ctx, line); err != nil {
		return fmt.Errorf("%s: %v", line, err)
	}
	deadline := time.Now().Add(scoutTeleportWait)
	for {
		st, err := c.Status(ctx)
		if err != nil {
			return fmt.Errorf("status after %s: %v", line, err)
		}
		if foreverDist2D(st.X, st.Y, p.X, p.Y) <= scoutLandYd {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not land within %s (the status is at %.1f %.1f)", line, scoutTeleportWait, st.X, st.Y)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(scoutForeverPoll):
		}
	}
}

// foreverNum formats a coordinate the way the GM command parsers take it.
func foreverNum(v float32) string { return strconv.FormatFloat(float64(v), 'f', 2, 32) }

// foreverDist2D is the ground distance between two points.
func foreverDist2D(ax, ay, bx, by float32) float64 {
	return math.Hypot(float64(ax-bx), float64(ay-by))
}

// foreverWorldUnit is a serve unit as the scout's live checks take it.
func foreverWorldUnit(u serveclient.Unit) world.Unit {
	guid, _ := world.ParseGUID(string(u.GUID))
	health := u.Health
	if health < 0 {
		health = 0
	}
	maxHealth := u.MaxHealth
	if maxHealth < 0 {
		maxHealth = 0
	}
	return world.Unit{
		GUID: guid, Type: u.Type, Entry: uint32(u.Entry), Name: u.Name,
		X: u.X, Y: u.Y, Z: u.Z, O: u.O, Distance: u.Distance, Distance2D: u.Distance2D,
		Health: uint32(health), MaxHealth: uint32(maxHealth), Level: uint32(max(u.Level, 0)),
		Class: u.Class, InCombat: u.InCombat, Dead: u.Dead, Ghost: u.Ghost, Moving: u.Moving,
	}
}

// foreverScoutPath is one serve route as the live path check takes it.
func foreverScoutPath(p serveclient.Path, to dungeon.Point) planscout.ScoutPath {
	sp := planscout.ScoutPath{Verdict: p.Status, Length: float64(p.Length), End: to}
	end := to
	if n := len(p.Points); n > 0 {
		end = dungeon.Point{X: p.Points[n-1][0], Y: p.Points[n-1][1], Z: p.Points[n-1][2]}
	}
	sp.End, sp.EndOffset = end, mathDist3(end, to)
	sp.SnappedZ = float64(end.Z)
	switch p.Status {
	case string(navmesh.RouteComplete):
		sp.Verdict, sp.PathType = "ok", navmesh.PathNormal
	case string(navmesh.RoutePartial):
		sp.PathType = navmesh.PathIncomplete
	default:
		sp.PathType = navmesh.PathNoPath
	}
	return sp
}

// mathDist3 is the 3D distance between two points.
func mathDist3(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// foreverWalkWanted says the walk pass takes this step: one of its walk checks failed or warned offline (the turtle
// pass's list).
func foreverWalkWanted(rep *planscout.Report, step int) bool {
	for _, st := range rep.Steps {
		if st.Step != step {
			continue
		}
		for _, c := range st.Checks {
			switch c.Name {
			case planlint.LintClimb, planlint.LintWater, planlint.LintArrival, "drop", planscout.CheckLivePath:
			default:
				continue
			}
			if c.Verdict == planscout.VerdictFail || c.Verdict == planscout.VerdictWarn {
				return true
			}
		}
	}
	return false
}
