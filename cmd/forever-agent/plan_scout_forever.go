// plan_scout_forever.go: plan-scout's Forever mode (K4). The offline draft reads a ForeverCore world export with
// worldspawn.LoadForeverDB on a v16 navmesh; the live pass drives a running `forever serve` slot through serveclient
// (GM lines and the units/path_to/walk_path methods), never the Eluna toolkit. The pass fills the levels the core has
// no row for from the live units read, then the report is rebuilt so every aggro and joiner number uses them.
package main

import (
	"context"
	"errors"
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
	"github.com/pjerra/forever-agent/internal/forever/sightcmd"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/planlint"
	"github.com/pjerra/forever-agent/internal/planscout"
	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/slotdir"
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
	// scoutForeverWalkMin is the least budget a walk is started with: a walk needs its route planned and walked, so a
	// pass with less than this left does not start one (the hop is skipped as "walk budget").
	scoutForeverWalkMin = 2 * time.Second
	// scoutForeverSightPace is the least time between two sight queries of one pass: the module allows five at once
	// per caller and one more every 200 ms, so 250 ms keeps every batch under the refill without spending the burst.
	scoutForeverSightPace = 250 * time.Millisecond
	// scoutForeverSightPass bounds the sight queries of one live pass. A pack larger than the bound leaves its later
	// stands not asked (the report says so); the pass itself is never held by a slow or refusing server.
	scoutForeverSightPass = time.Minute
)

// scoutForeverCeilingDur is how long --max-minutes gives the live pass; the tests shorten it.
var scoutForeverCeilingDur = func(o *planScoutOptions) time.Duration {
	if o.maxMinutes <= 0 {
		return 0
	}
	return time.Duration(o.maxMinutes) * time.Minute
}

// scoutForeverClient is the part of serveclient the Forever live pass uses; the tests inject a fake.
type scoutForeverClient interface {
	Status(context.Context) (serveclient.Status, error)
	GM(context.Context, string) (serveclient.Command, error)
	Units(context.Context, serveclient.UnitsParams) ([]serveclient.Unit, error)
	PathTo(context.Context, float32, float32, float32) (serveclient.Path, error)
	WalkPath(context.Context, float32, float32, float32) (serveclient.Path, error)
	WalkRay(context.Context, *[3]float32, float32, float32, float32) (serveclient.WalkRay, error)
	LOS(context.Context, serveclient.LOSParams) (serveclient.LOS, error)
}

// planScoutOpenForever opens the v16 planner of the Forever draft; the tests replace it.
var planScoutOpenForever = func(dir string, parents map[uint32]uint32) planScoutNav {
	return navmesh.NewForeverPlanner(dir, parents)
}

// scoutForeverOpen opens the serve slot dir the live pass drives; the tests replace it.
var scoutForeverOpen = func(dir string) (scoutForeverClient, error) {
	return serveclient.Open(dir)
}

// scoutForeverSlotCharacter is the character the serve dir's trusted slot.json names: the only character the pass may
// enable GM mode on, unbind and teleport. The tests replace it.
var scoutForeverSlotCharacter = func(dir string) (string, error) {
	abs, m, err := slotdir.Read(dir)
	if err != nil {
		return "", err
	}
	if err := slotdir.Check("plan-scout", abs, m); err != nil {
		return "", err
	}
	if m.Character == "" {
		return "", fmt.Errorf("%s/slot.json names no character", abs)
	}
	return m.Character, nil
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

// scoutForeverUnitKey names the spawn a live unit can be trusted to stand for: its low 24 bits (the spawn guid) and its
// entry. A summon or pet whose low 24 bits collide with a spawn, or the same guid on another entry, is not that spawn.
type scoutForeverUnitKey struct {
	guid  uint32
	entry uint32
}

// scoutForeverFillLevels replaces the assumed level of every unknown spawn whose own live creature unit the scout read,
// and returns how many spawns it filled. Only units of type creature with the spawn's entry count: the entry fallback is
// gone because every spawn of an entry has its own level (the core rolls level_min..level_max per spawn), so one unit's
// level says nothing about its siblings.
func scoutForeverFillLevels(w *worldspawn.World, units map[int][]world.Unit, unknown map[uint64]bool) int {
	if w == nil || len(unknown) == 0 || len(units) == 0 {
		return 0
	}
	steps := make([]int, 0, len(units))
	for step := range units {
		steps = append(steps, step)
	}
	sort.Ints(steps)
	byUnit := map[scoutForeverUnitKey]int{}
	for _, step := range steps {
		for _, u := range units[step] {
			if u.Type != "creature" || u.Level <= 1 || u.Entry == 0 {
				continue
			}
			g := world.Low24(u.GUID)
			if g == 0 {
				continue
			}
			k := scoutForeverUnitKey{guid: g, entry: u.Entry}
			if _, seen := byUnit[k]; !seen {
				byUnit[k] = int(u.Level)
			}
		}
	}
	filled := 0
	for i := range w.Spawns {
		s := &w.Spawns[i]
		if !unknown[s.GUID] {
			continue
		}
		lv, ok := byUnit[scoutForeverUnitKey{guid: world.Low24(world.GUID(s.GUID)), entry: s.Entry}]
		if ok && lv != s.LevelMax {
			s.LevelMax = lv
			filled++
		}
	}
	return filled
}

// runScoutForeverLive is the Forever live pass: online wait, GM mode, an instance unbind, a `.go xyz` and a units read
// per stop, a `.go xyz` to each hop's own origin and the server's route from there, and a walk of the risky hops within
// the walk budget. Every wait, read and walk runs under the --max-minutes ceiling; a walk gets the budget that is left,
// so the budget is never exceeded. It returns the exit code the turtle pass uses: 0 done, 28 aborted part-way, 18 a
// phase refused.
func runScoutForeverLive(ctx context.Context, o *planScoutOptions, in planscout.Input, rep *planscout.Report,
	w *worldspawn.World, nav planscout.Nav) (planscout.LiveData, int, string) {
	data := planscout.LiveData{
		Paths:      map[int]planscout.ScoutPath{},
		ExtraPaths: map[int][]planscout.ScoutPath{},
		Walks:      map[int]planscout.WalkResult{},
		Units:      map[int][]world.Unit{},
		LOS:        map[planscout.LOSKey]bool{},
		ServerLOS:  map[planscout.LOSKey]bool{},
	}
	// --max-minutes is the pass's overall ceiling: every wait, read and walk below inherits its deadline.
	if d := scoutForeverCeilingDur(o); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.Now().Add(d))
		defer cancel()
	}
	ceiling := func() string {
		switch {
		case ctx.Err() == nil:
			return ""
		case errors.Is(ctx.Err(), context.DeadlineExceeded) && o.maxMinutes > 0:
			return fmt.Sprintf("the --max-minutes ceiling of %d min", o.maxMinutes)
		}
		return "the live pass was cancelled"
	}
	stops := planscout.LiveStops(planscout.LiveSpots(rep, in, w, nil))
	readStops, noteStops := 0, false
	rayNoted := false
	losOK := true // false once the serve answers that it has no walk_ray method
	// sight is the module's own los query beside the ray: a serve whose module build has no los command, that
	// refuses access, or that keeps refusing for rate, is said once and then the pass asks no more (the ray stands
	// alone). A unit the server no longer has is skipped alone, and the query pass has its own time bound.
	sight := scoutForeverSight{ok: true}
	noteStopCount := func() {
		if noteStops || len(stops) == 0 {
			return
		}
		noteStops = true
		data.Notes = append(data.Notes, fmt.Sprintf("live stops: %d of %d read", readStops, len(stops)))
	}
	// partialWalks counts the stands whose walk the server planned no full route for: each is recorded with the
	// distance it reached and the pass goes on, and the one note says how many there were.
	partialWalks, partialNoted := 0, false
	notePartialWalks := func() {
		if partialWalks == 0 || partialNoted {
			return
		}
		partialNoted = true
		data.Notes = append(data.Notes, fmt.Sprintf("live walks: %d stand(s) partial (walk_path had no full route; each recorded with the distance reached and the pass went on)", partialWalks))
	}
	abort := func(reason string) (planscout.LiveData, int, string) {
		noteStopCount()
		notePartialWalks()
		data.Aborted = true
		data.AbortReason = reason
		return data, exitPlanScoutLiveAborted, reason
	}
	slotChar, err := scoutForeverSlotCharacter(o.serveSlot)
	if err != nil {
		return data, exitLivePhaseRefused, "the serve slot: " + err.Error()
	}
	client, err := scoutForeverOpen(o.serveSlot)
	if err != nil {
		return data, exitLivePhaseRefused, "the serve slot: " + err.Error()
	}
	if err := foreverWaitOnline(ctx, client); err != nil {
		if r := ceiling(); r != "" {
			return abort(r)
		}
		return data, exitLivePhaseRefused, err.Error()
	}
	// The slot's manifest and the session's own status must name the same character before anything mutates it: a slot
	// path pointed at the wrong dir would otherwise enable GM mode, unbind instances and teleport someone else.
	st, err := client.Status(ctx)
	if err != nil {
		if r := ceiling(); r != "" {
			return abort(r)
		}
		return data, exitLivePhaseRefused, fmt.Sprintf("status: %v", err)
	}
	if !strings.EqualFold(st.Character, slotChar) {
		return data, exitLivePhaseRefused, fmt.Sprintf(
			"the serve slot is %q's session but its status names %q: refusing to drive another character", slotChar, st.Character)
	}
	planMap := in.Rows.Plan.Map
	for _, line := range []string{".gm on", ".instance unbind all"} {
		if err := foreverGM(ctx, client, line, planMap); err != nil {
			if r := ceiling(); r != "" {
				return abort(r)
			}
			return data, exitLivePhaseRefused, err.Error()
		}
	}
	for _, group := range stops {
		if r := ceiling(); r != "" {
			return abort(r)
		}
		if err := foreverStandAt(ctx, client, group[0].Point, planMap); err != nil {
			if r := ceiling(); r != "" {
				return abort(r)
			}
			return abort(err.Error())
		}
		// A stop's spots chain (each within LiveSpotYd of an earlier one), so a later spot can stand far past the units
		// read at the stop: every spot is read where it stands, and the sight rays start from there.
		at := group[0].Point
		for _, spot := range group {
			if foreverDist2D(spot.Point.X, spot.Point.Y, at.X, at.Y) > scoutLandYd {
				if err := foreverStandAt(ctx, client, spot.Point, planMap); err != nil {
					if r := ceiling(); r != "" {
						return abort(r)
					}
					return abort(err.Error())
				}
				at = spot.Point
			}
			// The session may stand up to scoutLandYd from the spot (a landing within tolerance, or a spot read without
			// moving), so the read is widened by that much and then cut back to the spot's own 40 yd.
			units, err := client.Units(ctx, serveclient.UnitsParams{Within: scoutForeverUnitsYd + scoutLandYd})
			if err != nil {
				if r := ceiling(); r != "" {
					return abort(r)
				}
				return abort(fmt.Sprintf("units at %.1f %.1f %.1f: %v", spot.Point.X, spot.Point.Y, spot.Point.Z, err))
			}
			wu := make([]world.Unit, 0, len(units))
			for _, u := range units {
				if foreverDist2D(u.X, u.Y, spot.Point.X, spot.Point.Y) > scoutForeverUnitsYd {
					continue
				}
				wu = append(wu, foreverWorldUnit(u))
			}
			data.Units[spot.Step] = wu
			if losOK {
				ok, rayErr := foreverWalkRayAt(ctx, client, spot, wu, data.LOS)
				if r := ceiling(); r != "" {
					return abort(r)
				}
				if rayErr != nil && !rayNoted {
					rayNoted = true
					data.Notes = append(data.Notes, fmt.Sprintf("live sight: a walk_ray read from %.1f %.1f %.1f failed (%v); its steps stay unanswered and the pass goes on",
						spot.LOSPoint.X, spot.LOSPoint.Y, spot.LOSPoint.Z, rayErr))
				}
				if !ok {
					losOK = false
					data.Notes = append(data.Notes, "live sight: this serve has no walk_ray method; no sight answer was read")
				}
			}
			// The server's own sight answer for the pull's pack, read from the stand it pulls from: it is recorded
			// beside the ray, never instead of it, so the report can show where the two disagree.
			if err := foreverServerSightAt(ctx, client, spot, units, &data, &sight); err != nil {
				if r := ceiling(); r != "" {
					return abort(r)
				}
				return abort(err.Error())
			}
		}
		readStops++
	}
	noteStopCount()
	if sight.notAsked > 0 {
		why := "the sight query stopped asking"
		if sight.timedOut {
			why = fmt.Sprintf("the sight pass's %s bound ran out", scoutForeverSightPass)
		}
		data.Notes = append(data.Notes, fmt.Sprintf("live sight: %d stand(s) were not asked (%s)", sight.notAsked, why))
	}
	if sight.skipped > 0 {
		data.Notes = append(data.Notes, fmt.Sprintf("live sight: the module refused %d pack unit(s) (unknown-unit, not-in-world or different-maps); those units stay unanswered and the pass went on", sight.skipped))
	}
	if sight.noAccess > 0 {
		data.Notes = append(data.Notes, fmt.Sprintf("live sight: the module answered no-access for %d pack unit(s) (below GM a unit that did not resolve answers this code, so either the creature was gone or the session may read nothing); those units stay unanswered and the pass went on", sight.noAccess))
	}
	if sight.unread > 0 {
		data.Notes = append(data.Notes, fmt.Sprintf("live sight: the module's answer was not understood for %d pack unit(s) (answer not understood); those units stay unanswered and the pass went on", sight.unread))
	}
	budget := o.walkBudgetD
	if budget <= 0 {
		budget = 5 * time.Minute
	}
	walkEnd := time.Now().Add(budget)
	for _, hop := range planscout.LiveHops(rep, in, nav) {
		if !hop.Primary {
			continue
		}
		if r := ceiling(); r != "" {
			return abort(r)
		}
		// The server plans from the session's position, so every hop starts at its own origin: the last stop, the last
		// walk's end and the hop before it are all the wrong place to measure from.
		if err := foreverStandAt(ctx, client, hop.From, planMap); err != nil {
			if r := ceiling(); r != "" {
				return abort(r)
			}
			return abort(err.Error())
		}
		route, err := client.PathTo(ctx, hop.Point.X, hop.Point.Y, hop.Point.Z)
		if err != nil {
			if r := ceiling(); r != "" {
				return abort(r)
			}
			return abort(fmt.Sprintf("path_to %.1f %.1f %.1f from %.1f %.1f %.1f: %v",
				hop.Point.X, hop.Point.Y, hop.Point.Z, hop.From.X, hop.From.Y, hop.From.Z, err))
		}
		data.Paths[hop.Step] = foreverScoutPath(route, hop.Point)
		if !foreverWalkWanted(rep, hop.Step) && data.Paths[hop.Step].Verdict == "ok" {
			continue
		}
		// The walk gets the budget that is left, and only when enough of it is left to walk at all.
		remaining := time.Until(walkEnd)
		if remaining < scoutForeverWalkMin {
			data.Walks[hop.Step] = planscout.WalkResult{Skipped: "walk budget"}
			continue
		}
		walkCtx, cancel := context.WithDeadline(ctx, walkEnd)
		walked, walkErr := client.WalkPath(walkCtx, hop.Point.X, hop.Point.Y, hop.Point.Z)
		cancel()
		if walkErr != nil {
			if r := ceiling(); r != "" {
				return abort(r)
			}
			if errors.Is(walkErr, context.DeadlineExceeded) {
				data.Walks[hop.Step] = planscout.WalkResult{Skipped: "walk budget"}
				continue
			}
			if scoutPartialWalk(walkErr) {
				// The server planned no full route, so the walk never started: that stand is partial, not the end of
				// the pass. Record where the session stands and how far that is from the hop's origin, then go on to
				// the next stand.
				st, serr := client.Status(ctx)
				if serr != nil {
					if r := ceiling(); r != "" {
						return abort(r)
					}
					return abort(fmt.Sprintf("status after the partial route to %.1f %.1f %.1f: %v",
						hop.Point.X, hop.Point.Y, hop.Point.Z, serr))
				}
				partialWalks++
				data.Walks[hop.Step] = planscout.WalkResult{
					Stop:    dungeon.Point{X: st.X, Y: st.Y, Z: st.Z},
					Reached: foreverDist2D(hop.From.X, hop.From.Y, st.X, st.Y),
					Partial: true,
				}
				continue
			}
			// A failed walk is an error row with the reason, never a stop the check measures as a shortfall.
			reason := fmt.Sprintf("the walk to %.1f %.1f %.1f failed: %v",
				hop.Point.X, hop.Point.Y, hop.Point.Z, walkErr)
			data.Walks[hop.Step] = planscout.WalkResult{Skipped: reason}
			return abort(reason)
		}
		data.Paths[hop.Step] = foreverScoutPath(walked, hop.Point)
		st, err := client.Status(ctx)
		if err != nil {
			if r := ceiling(); r != "" {
				return abort(r)
			}
			return abort(fmt.Sprintf("status after the walk to %.1f %.1f %.1f: %v", hop.Point.X, hop.Point.Y, hop.Point.Z, err))
		}
		data.Walks[hop.Step] = planscout.WalkResult{Stop: dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}, Length: float64(walked.Length)}
	}
	notePartialWalks()
	return data, exitOK, ""
}

// foreverWalkRayAt reads the navmesh walkability ray from one spot's sight point to every live unit read at its stop (and
// to its target pack, which is among them when its units stood within reach of the spot's own point) and stores each answer under the spot's
// step as the live_los input. It is not true line of sight: the serve has no collision geometry (serve's walk_ray method).
// A unit the mesh cannot place is left unanswered, never stored as out of sight. A read that fails leaves that unit
// unanswered and the spot goes on; the first such error is returned for the note. ok is false when the serve has no
// walk_ray method at all.
func foreverWalkRayAt(ctx context.Context, c scoutForeverClient, spot planscout.LiveSpot, units []world.Unit,
	out map[planscout.LOSKey]bool) (ok bool, err error) {
	from := [3]float32{spot.LOSPoint.X, spot.LOSPoint.Y, spot.LOSPoint.Z}
	seen := map[uint32]bool{}
	for _, u := range units {
		if u.Dead {
			continue
		}
		low := world.Low24(u.GUID)
		if seen[low] || foreverDist2D(u.X, u.Y, spot.Point.X, spot.Point.Y) > scoutForeverUnitsYd {
			continue
		}
		seen[low] = true
		ans, rerr := c.WalkRay(ctx, &from, u.X, u.Y, u.Z)
		if rerr != nil {
			if code, coded := serveclient.CodeOf(rerr); coded && code == rpc.CodeMethodNotFound {
				return false, nil
			}
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			if err == nil {
				err = rerr
			}
			continue
		}
		if ans.Known {
			out[planscout.LOSKey{Step: spot.Step, Guid: low}] = ans.In
		}
	}
	return true, err
}

// scoutForeverSight is the module's own sight query over one live pass: whether the module still answers it, how long
// it may keep asking, and how many stands and units it did not ask about. A refusal that is about the whole pass
// (unsupported, a bad argument that would repeat for every unit, a rate refusal that survived the one retry) stops
// the asking and is said once; a refusal about one unit (it despawned, died or changed map, or a no-access below
// GM, where a unit that did not resolve and a query the caller may not read answer the same code) or an answer for
// one unit that is not understood skips that unit alone.
type scoutForeverSight struct {
	ok       bool
	said     bool
	last     time.Time // the previous query, for the pace
	end      time.Time // the pass's bound, set at its first query
	notAsked int       // stands whose pack was never asked
	skipped  int       // pack units the module refused one by one
	noAccess int       // pack units answered no-access (the creature gone, the caller below GM)
	unread   int       // pack units whose answer was not a record this client understands (each is left unanswered)
	timedOut bool      // the bound, not a refusal, stopped the pass
}

// errScoutSightEnd is the sight pass's own bound: the stand it happens on is left not asked, and the pass goes on with
// the ray alone rather than holding the whole live pass.
var errScoutSightEnd = errors.New("plan-scout: the sight pass's time bound ran out")

// pace waits out the module's refill and checks the pass's bound; it is called before every query, the retry included.
func (s *scoutForeverSight) pace(ctx context.Context) error {
	now := time.Now()
	if s.end.IsZero() {
		s.end = now.Add(scoutForeverSightPass)
	}
	if !s.last.IsZero() {
		if d := scoutForeverSightPace - now.Sub(s.last); d > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
			}
		}
	}
	if time.Now().After(s.end) {
		return errScoutSightEnd
	}
	s.last = time.Now()
	return nil
}

// ask runs one query under the pace, and asks the same query once more when the module answers a rate refusal (the
// pace has waited its refill by then). Any other answer is returned as it is.
func (s *scoutForeverSight) ask(ctx context.Context, c scoutForeverClient, stand, guid string) (serveclient.LOS, error) {
	for attempt := 0; ; attempt++ {
		if err := s.pace(ctx); err != nil {
			return serveclient.LOS{}, err
		}
		ans, err := c.LOS(ctx, serveclient.LOSParams{A: stand, B: guid})
		if err != nil {
			return serveclient.LOS{}, err
		}
		if ans.Reason != sightcmd.Rate || attempt > 0 {
			return ans, nil
		}
	}
}

// foreverLOSUnsupported reports whether the query failed because the module build has no such command, rather than
// because the answer or the link was bad.
func foreverLOSUnsupported(err error) bool {
	if code, ok := serveclient.CodeOf(err); ok && code == rpc.CodeMethodNotFound {
		return true
	}
	return strings.Contains(err.Error(), "not supported by this module build")
}

// foreverLOSUnread reports whether the query failed because the module's answer for this one unit was not a record this
// client understands (the daemon's "not a los record"): that unit stays unanswered and the pass goes on.
func foreverLOSUnread(err error) bool {
	code, ok := serveclient.CodeOf(err)
	return ok && code == rpc.CodeGame && strings.Contains(err.Error(), sightcmd.NotARecord)
}

// foreverServerSightAt records the server's own sight answer between a pull row's stand and every unit of its target
// pack that the live read saw. Each answer is stored beside the navmesh ray under the same LOSKey, so the report shows
// both. The queries are paced and bounded; a whole-pass refusal (no command, no access, a repeated rate) is said once and
// the pass asks no more, while a unit the server no longer has, or whose answer is not understood, is skipped alone. An error is
// returned only for the pass's own context ending.
func foreverServerSightAt(ctx context.Context, c scoutForeverClient, spot planscout.LiveSpot, units []serveclient.Unit,
	data *planscout.LiveData, sight *scoutForeverSight) error {
	if len(spot.TargetGuids) == 0 {
		return nil
	}
	if !sight.ok {
		sight.notAsked++
		return nil
	}
	byLow := make(map[uint32]string, len(units))
	for _, u := range units {
		if u.Dead {
			continue
		}
		guid, err := world.ParseGUID(string(u.GUID))
		if err != nil {
			continue
		}
		low := world.Low24(guid)
		if _, seen := byLow[low]; !seen {
			byLow[low] = string(u.GUID)
		}
	}
	stand := fmt.Sprintf("%s,%s,%s", foreverNum(spot.LOSPoint.X), foreverNum(spot.LOSPoint.Y), foreverNum(spot.LOSPoint.Z))
	give := func(note string) {
		sight.ok = false
		if !sight.said {
			sight.said = true
			data.Notes = append(data.Notes, note)
		}
	}
	for _, t := range spot.TargetGuids {
		guid, ok := byLow[t.Guid]
		if !ok {
			continue // a pack unit the live read never saw: there is no guid to name it by
		}
		ans, err := sight.ask(ctx, c, stand, guid)
		if err != nil {
			if errors.Is(err, errScoutSightEnd) {
				sight.timedOut = true
				sight.notAsked++
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if foreverLOSUnread(err) {
				// One unit's answer that is not understood is recorded for that unit and the rest of the pack is asked.
				sight.unread++
				continue
			}
			if foreverLOSUnsupported(err) {
				give(fmt.Sprintf("live sight: the server does not answer los (%v); the sight answers are the navmesh ray's alone", err))
			} else {
				give(fmt.Sprintf("live sight: the los query for %s failed (%v); the sight answers are the navmesh ray's alone", guid, err))
			}
			return nil
		}
		switch ans.Reason {
		case "":
			data.ServerLOS[planscout.LOSKey{Step: spot.Step, Guid: t.Guid}] = ans.Clear
		case sightcmd.UnknownUnit, sightcmd.NotInWorld, sightcmd.DifferentMaps:
			// The unit despawned, died or changed map between the read and the query: leave it unanswered and ask the
			// rest of the pack.
			sight.skipped++
		case sightcmd.NoAccess:
			// Below GM the module answers no-access where it would answer unknown-unit, and this query, a ground point
			// against a creature guid, has no unit of the caller's own to be allowed on: the code cannot say which, so
			// the unit stays unanswered and the note carries the count instead of the pass claiming a cause.
			sight.noAccess++
		case sightcmd.Rate:
			give("live sight: the module refused the los query for rate twice; the sight answers are the navmesh ray's alone")
			return nil
		default:
			give(fmt.Sprintf("live sight: the module refused the los query for %s; the sight answers are the navmesh ray's alone", ans.Reason))
			return nil
		}
	}
	return nil
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

// scoutGMErrorTexts are the parts of the core's refusals of the scout's GM lines that stay the same in every sentence:
// LANG_INVALID_TARGET_COORD ("Invalid target coordinates ...", row 263) and trinity rows 6-10 and the syntax block.
// A line carrying one of these is a refusal even when the reply as a whole ended normally.
var scoutGMErrorTexts = []string{
	"invalid target coord",
	"does not exist",
	"incorrect syntax",
	"syntax: ",
	"is ambiguous",
	"possible subcommands",
	"commands available to you",
	"no permission",
	"not have permission",
	"not allowed",
	"must be in gm mode",
	"not a gm",
	"unknown command",
	"not found",
	"cannot",
	"can't",
	"failed",
}

// scoutGMWantText is the success line the core sends for line, or "" when the core says nothing (`.go xyz` only
// teleports; its landing is checked from the status instead).
func scoutGMWantText(line string) string {
	switch line {
	case ".gm on":
		// LANG_GM_ON: "GM mode is ON" (the same line the live pass waits for).
		return "gm mode is on"
	case ".instance unbind all":
		// LANG_COMMAND_INST_UNBIND_UNBOUND: "Unbound N instance(s)."
		return "unbound"
	}
	return ""
}

// scoutGMRefusal returns the core's refusal text in a GM answer, or "" when the answer says the line worked. A reply
// that never carries the success line is a refusal too: the daemon's echo of the command is not the server's ack.
func scoutGMRefusal(line string, cmd serveclient.Command) string {
	want := scoutGMWantText(line)
	said := want == ""
	for _, l := range cmd.Lines {
		low := strings.ToLower(l)
		if !said && strings.Contains(low, want) {
			said = true
			continue
		}
		for _, bad := range scoutGMErrorTexts {
			if strings.Contains(low, bad) {
				return strings.TrimSpace(l)
			}
		}
	}
	if said {
		return ""
	}
	if len(cmd.Lines) == 0 {
		return fmt.Sprintf("no %q acknowledgement (reply end %q)", want, cmd.End)
	}
	return fmt.Sprintf("no %q acknowledgement (lines %q, reply end %q)", want, cmd.Lines, cmd.End)
}

// foreverGM sends one GM line in its canonical allow-listed form and checks the core's answer: a line it refuses stops
// the caller with the server's own words.
func foreverGM(ctx context.Context, c scoutForeverClient, line string, planMap uint32) error {
	canon, ok := scoutCanonicalGM(line, planMap, 0)
	if !ok {
		return fmt.Errorf("%q is not on the scout's allow-list in its canonical form", line)
	}
	cmd, err := c.GM(ctx, canon)
	if err != nil {
		return fmt.Errorf("%s: %v", canon, err)
	}
	if ref := scoutGMRefusal(canon, cmd); ref != "" {
		return fmt.Errorf("%s: the server refused: %s", canon, ref)
	}
	return nil
}

// scoutTeleportUnconfirmed reports the serve's own "not confirmed" answer to a `.go xyz`: the line went out and the
// server sent no refusal, but its own move of the character never came within the serve's budget. The core drops a
// `.go xyz` that arrives while a previous teleport is still settling (Player::TeleportTo returns false, in silence,
// while GetTeleportState() != TeleportState::NotTeleporting), so a stand that gets this answer sends its line once
// more after that settle instead of stopping the pass there.
func scoutTeleportUnconfirmed(err error) bool {
	return err != nil && strings.Contains(err.Error(), "the teleport is not confirmed")
}

// scoutPartialWalk reports the serve's own no-full-route answer for a partial route: the walk never starts, and the
// pass records that stand as partial and goes on to the next one. The answer is the serve's documented text, read
// here because the error crosses the RPC as the method's own message.
func scoutPartialWalk(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "walk_path: no full route") && strings.Contains(msg, "(partial)")
}

// scoutTeleportAttempts is how many times a stand sends its `.go xyz`: the first line can be the one the core dropped
// in silence while the previous teleport was settling, and the send after that settle lands.
const scoutTeleportAttempts = 2

// scoutTeleportSettle is how long a stand waits before sending its line again; a variable so the tests shorten it.
var scoutTeleportSettle = 500 * time.Millisecond

// foreverStandAt teleports the GM to a point with `.go xyz` and waits for the status to show it on that map: the
// arrival, never the packet, is the confirmation. A line the serve did not confirm is sent once more after the settle
// window, and the error of the last attempt is the stand's.
func foreverStandAt(ctx context.Context, c scoutForeverClient, p dungeon.Point, planMap uint32) error {
	line := fmt.Sprintf(".go xyz %s %s %s %d", foreverNum(p.X), foreverNum(p.Y), foreverNum(p.Z), planMap)
	for attempt := 1; ; attempt++ {
		err := foreverStandOnce(ctx, c, line, p, planMap)
		if err == nil || !scoutTeleportUnconfirmed(err) || attempt == scoutTeleportAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(scoutTeleportSettle):
		}
	}
}

// foreverStandOnce sends one `.go xyz` and waits for the status to show the landing on the plan's map.
func foreverStandOnce(ctx context.Context, c scoutForeverClient, line string, p dungeon.Point, planMap uint32) error {
	canon, ok := scoutCanonicalGM(line, planMap, 0)
	if !ok {
		return fmt.Errorf("%q is not on the scout's allow-list in its canonical form", line)
	}
	cmd, gmErr := c.GM(ctx, canon)
	if gmErr != nil {
		return fmt.Errorf("%s: %v", canon, gmErr)
	}
	if ref := scoutGMRefusal(canon, cmd); ref != "" {
		return fmt.Errorf("%s: the server refused: %s", canon, ref)
	}
	deadline := time.Now().Add(scoutTeleportWait)
	for {
		st, err := c.Status(ctx)
		if err != nil {
			return fmt.Errorf("status after %s: %v", line, err)
		}
		if st.Map == planMap && foreverDist2D(st.X, st.Y, p.X, p.Y) <= scoutLandYd {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not land on map %d within %s (the status is at %.1f %.1f on map %d)",
				line, planMap, scoutTeleportWait, st.X, st.Y, st.Map)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(scoutForeverPoll):
		}
	}
}

// foreverNum formats a coordinate in the canonical form the allow-list sends: decimals only, no exponent, and the
// shortest text that round-trips as the float32 the plan holds.
func foreverNum(v float32) string {
	return strconv.FormatFloat(float64(v), 'f', -1, 32)
}

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
