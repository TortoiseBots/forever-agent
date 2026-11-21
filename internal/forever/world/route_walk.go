package world

// forever route-walk: a dungeon plan walked leg by leg on the server's own navmesh, with every planned point
// confirmed against the server's stored position. It is the live half of the plan check: path-check says where the
// v16 mesh wants the character to go, this says where the server ended up putting it.
//
// Entry is a GM teleport to the plan's entrance (the plan's entrance_tele row), the walk is the client's real
// movement (objects.Mover's start/heartbeat/stop packets, as walk-check uses), and the confirmation is the core's own
// `.gps` answer read through the command channel, each request marked by an echoed self-whisper and each stop
// confirmed by two agreeing reads. GM mode is optional: it changes what the world does around the
// walk (no creature may attack a GM, no fall damage, the entrance's level gate is skipped), not how the movement
// packets themselves are handled.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// Verdicts of one leg. A leg is PASS when every planned point was confirmed within the deviation threshold and no
// walk crossed a fall, damaging liquid or a stall; otherwise it carries the first thing that went wrong.
const (
	RoutePass     = "PASS"
	RouteDeviated = "DEVIATED"
	// RouteUnconfirmed is a stop whose position the server did not confirm: no complete `.gps` block, two reads
	// that disagree, or a stop the walk never reached. It is never PASS.
	RouteUnconfirmed = "UNCONFIRMED"
	RouteStuck       = "STUCK"
	RouteFell        = "FELL"
	RouteLiquid      = "LIQUID"
	RouteNoPath      = "NOPATH"
	// RouteIncomplete is a leg or run the deadline or a cancel ended before its work was done. It is never PASS.
	RouteIncomplete = "INCOMPLETE"
	RouteFail       = "FAIL"
)

// RouteWalkOptions is `forever route-walk`. The thresholds are the command line's flags; zero values take the
// defaults below (the CLI fills them in, so a caller that builds the struct directly gets the same numbers).
type RouteWalkOptions struct {
	// Plan is the loaded plan file; its Map, EntranceTele, Avoid and Legs are used.
	Plan *dungeon.Plan
	// Legs is the selected leg indexes, in plan order. Empty means every leg.
	Legs []int
	// GM turns GM mode on (`.gm on`) after the guard and off at the end. The entry teleport needs it: the core
	// skips a GM's entrance checks (Player::Satisfy, Player.cpp:21380).
	GM bool
	// Enter is "tele" (the plan's entrance_tele row) or "none" (the character is already placed). Empty is "tele".
	Enter string
	// Leave is "tele" (the plan's outside_tele row, after the last leg) or "none". Empty is "tele". Leaving matters
	// with GM mode: GM mode is turned off at the end, and a level-1 character standing in the dungeon's packs then
	// aggros and the server refuses the logout ("result 1, in combat").
	Leave string
	// Settle is how long the session answers the world before the first command; the walk follows the create
	// block's position, so the settle also waits for it.
	Settle time.Duration
	// MaxDeviation (yd) is the 3-D distance between a plan point and the server's confirmed position above which
	// the leg is DEVIATED; a mesh snap farther than this from the plan point is DEVIATED too.
	MaxDeviation float64
	// StuckAfter and StuckStep: the server-confirmed position moving less than StuckStep yards for StuckAfter of
	// walking is a STUCK leg, reported with the position it froze at.
	StuckAfter time.Duration
	StuckStep  float64
	// FallDrop (yd) is how far the confirmed Z may end up below the planned point before the leg is FELL.
	FallDrop float64
	// Sample bounds how long one uninterrupted movement runs before the position is confirmed again, so a stall
	// (a server that ignores the movement packets) is seen within StuckAfter rather than at the end of a leg.
	Sample time.Duration
	// LiquidAbove and LiquidBelow (yd) are the band round a planned point in which a magma/slime floor fails the
	// leg: the mesh's own damaging-liquid polygons, over the path or under it.
	LiquidAbove float64
	LiquidBelow float64
	// GPSQuiet is the quiet window that ends one `.gps` read (default 500 ms). The request itself is marked (see
	// routeGPSBarrier), so no drain runs before it. A test server answers at once; the quiet window is what makes a
	// tokenless core command's reply complete.
	GPSQuiet time.Duration
	// Confirm names the position source: "gps" (default) reads the server's own position with the core `.gps`
	// command; "client" records the position of the last movement packet and treats a server correction as a
	// deviation. The no-correction mode is what a server whose account cannot run `.gps` leaves; the report says
	// which one ran.
	Confirm string
}

// Confirmation sources.
const (
	RouteConfirmGPS    = "gps"
	RouteConfirmClient = "client"
)

// Defaults for the thresholds, as `forever route-walk --help` states them.
const (
	RouteMaxDeviationYd = 2.0
	RouteStuckAfter     = 6 * time.Second
	RouteStuckStepYd    = 0.5
	RouteFallDropYd     = 2.0
	RouteSample         = 3 * time.Second
	RouteLiquidAboveYd  = 2.0
	RouteLiquidBelowYd  = 0.5

	// routeEntryWait bounds the entry teleport: the transfer, the new world and the burst.
	routeEntryWait = 60 * time.Second
	// routeGPSQuiet and routeGPSDeadline bound one `.gps` read: the core answers in a few system lines, so half a
	// second of quiet is well past its send, and the deadline bounds a server that stopped answering.
	routeGPSQuiet    = 500 * time.Millisecond
	routeGPSDeadline = 4 * time.Second
)

// RouteThresholds is the report's copy of the numbers the run used.
type RouteThresholds struct {
	MaxDeviationYd float64 `json:"max_deviation_yd"`
	StuckAfterS    float64 `json:"stuck_after_s"`
	StuckStepYd    float64 `json:"stuck_step_yd"`
	FallDropYd     float64 `json:"fall_drop_yd"`
	SampleS        float64 `json:"sample_s"`
	LiquidAboveYd  float64 `json:"liquid_above_yd"`
	LiquidBelowYd  float64 `json:"liquid_below_yd"`
}

// RouteWalkReport is one run: the entry, every selected leg with its waypoints, and the run's bounds.
type RouteWalkReport struct {
	Plan     string     `json:"plan"`
	Map      uint32     `json:"map"`
	GM       bool       `json:"gm"`
	Entry    string     `json:"entry"`
	EntryPos [3]float32 `json:"entry_position"`
	// Confirm is the position source the run used: gps (the server's own answer) or client (the last movement
	// packet, with a server correction shown as a deviation).
	Confirm    string          `json:"confirmed"`
	Thresholds RouteThresholds `json:"thresholds"`
	Legs       []RouteLeg      `json:"legs"`
	// Verdict is the worst leg verdict: PASS only when every leg passed.
	Verdict string    `json:"verdict"`
	Started time.Time `json:"started_at"`
	Ended   time.Time `json:"ended_at"`
}

// RouteLeg is one plan leg walked.
type RouteLeg struct {
	Index int        `json:"leg"`
	Name  string     `json:"name"`
	From  [3]float32 `json:"from"`
	Goal  [3]float32 `json:"goal"`
	// Status is the verdict (PASS, DEVIATED, STUCK, FELL, LIQUID, NOPATH, FAIL).
	Status string `json:"status"`
	// Reason is the first failure's own sentence.
	Reason string `json:"reason,omitempty"`
	// Issues lists every point that failed, in walk order, alongside the status.
	Issues []string `json:"issues,omitempty"`
	// PlannedYd is the sum of the planned hops' lengths; WalkedYd the distance between the confirmed positions.
	PlannedYd float64 `json:"planned_yd"`
	WalkedYd  float64 `json:"walked_yd"`
	// Hops is how many route hops (each via point and the leg's To) the leg planned.
	Hops      int             `json:"hops"`
	Waypoints []RouteWaypoint `json:"waypoints"`
	MaxDev    float64         `json:"max_deviation_yd"`
	// MaxSnap is the largest distance between a plan point and the mesh point the walk aimed at (the planner's
	// snap): reported on its own, never folded into the deviation.
	MaxSnap  float64 `json:"max_snap_yd"`
	StartedS float64 `json:"started_s"`
	EndedS   float64 `json:"ended_s"`
}

// RouteWaypoint is one planned point and the server's answer at it.
type RouteWaypoint struct {
	// Point is the planned path point the walk aimed at (a navmesh corner or a sample between two corners).
	Point [3]float32 `json:"planned"`
	// Goal is set on the last point of a hop: the plan's own point, which may differ from the mesh point by the
	// snap distance.
	Goal *[3]float32 `json:"goal,omitempty"`
	// Server is the position the run confirmed after the walk stopped there: the server's own `.gps` answer, or the
	// last movement packet in the no-correction mode; ServerConfirmed says which.
	Server          [3]float32 `json:"server"`
	ServerConfirmed bool       `json:"server_confirmed"`
	// DeviationYd is the 3-D distance between the reference point and Server: the PLAN point on the last point of
	// a hop, the walked path point between them. Dz is Server.Z - the reference's Z.
	DeviationYd float64 `json:"deviation_yd"`
	// SnapYd is the distance between the walked path point and the plan's point on the last point of a hop (the
	// mesh snap); 0 between hops and in a direct hop.
	SnapYd float64 `json:"snap_yd,omitempty"`
	Dz     float64 `json:"dz"`
	// Liquid is "magma" or "slime" when the mesh has a damaging-liquid floor in the band round the point.
	Liquid string `json:"liquid,omitempty"`
	// At is when the server confirmed the point, AtS the seconds since the run started.
	At   time.Time `json:"at"`
	AtS  float64   `json:"at_s"`
	Note string    `json:"note,omitempty"`
}

// routeHop is one point of the leg's route: a via waypoint or the leg's To.
type routeHop struct {
	to navmesh.Vec3
	// direct walks a straight line to the point instead of a navmesh route (the plan's direct waypoint: a gap the
	// mesh does not link).
	direct bool
	// gmCross is the plan's gm_cross: a point a GM account moves the party to. route-walk does not teleport, so the
	// leg stops here with NOPATH and says why.
	gmCross bool
}

// normalise fills the defaults.
func (o RouteWalkOptions) normalise() RouteWalkOptions {
	if o.Settle <= 0 {
		o.Settle = 4 * time.Second
	}
	if o.MaxDeviation <= 0 {
		o.MaxDeviation = RouteMaxDeviationYd
	}
	if o.StuckAfter <= 0 {
		o.StuckAfter = RouteStuckAfter
	}
	if o.StuckStep <= 0 {
		o.StuckStep = RouteStuckStepYd
	}
	if o.FallDrop <= 0 {
		o.FallDrop = RouteFallDropYd
	}
	if o.Sample <= 0 {
		o.Sample = RouteSample
	}
	if o.LiquidAbove <= 0 {
		o.LiquidAbove = RouteLiquidAboveYd
	}
	if o.LiquidBelow <= 0 {
		o.LiquidBelow = RouteLiquidBelowYd
	}
	if o.GPSQuiet <= 0 {
		o.GPSQuiet = routeGPSQuiet
	}
	if o.Confirm == "" {
		o.Confirm = RouteConfirmGPS
	}
	if o.Leave == "" {
		o.Leave = "tele"
	}
	return o
}

// createBlockWait bounds the extra wait for the character's create block after the settle window.
const createBlockWait = 10 * time.Second

// routeWalk is the session side of `forever route-walk`. A lost link or the run's deadline ends it with its own
// error; a failed leg is a finding, not an error, and the run goes on from where the character actually stands.
func (s *session) routeWalk(ctx context.Context) (err error) {
	o := s.opts.RouteWalk.normalise()
	if o.Plan == nil {
		return errors.New("route-walk: no plan")
	}
	legs := s.opts.RouteWalk.Legs
	if len(legs) == 0 {
		legs = make([]int, 0, len(o.Plan.Legs))
		for i := range o.Plan.Legs {
			legs = append(legs, i)
		}
	}
	for _, li := range legs {
		if li < 0 || li >= len(o.Plan.Legs) {
			return fmt.Errorf("route-walk: leg %d is outside the plan's %d legs", li, len(o.Plan.Legs))
		}
	}
	rep := &RouteWalkReport{
		Plan:    o.Plan.Name,
		Map:     o.Plan.Map,
		GM:      o.GM,
		Confirm: o.Confirm,
		Thresholds: RouteThresholds{
			MaxDeviationYd: o.MaxDeviation,
			StuckAfterS:    o.StuckAfter.Seconds(),
			StuckStepYd:    o.StuckStep,
			FallDropYd:     o.FallDrop,
			SampleS:        o.Sample.Seconds(),
			LiquidAboveYd:  o.LiquidAbove,
			LiquidBelowYd:  o.LiquidBelow,
		},
		Started: time.Now(),
		Verdict: RoutePass,
	}
	s.report.RouteWalk = rep
	runStart := time.Now()
	// Every exit path closes the report: a run that ended on an error (a lost link, the deadline, a cancel) is not
	// PASS, even when the legs already walked had passed.
	defer func() {
		if rep.Ended.IsZero() {
			rep.Ended = time.Now()
		}
		if err != nil {
			// The run did not finish: INCOMPLETE is the verdict whatever the legs found, short of FAIL.
			rep.Verdict = worseVerdict(rep.Verdict, RouteIncomplete)
		}
	}()

	s.initChat()
	s.opts.logf("route-walk: plan %q, map %d, %d leg(s) selected, GM mode %v", o.Plan.Name, o.Plan.Map, len(legs), o.GM)
	if err := s.collect(ctx, o.Settle); err != nil {
		return err
	}
	// The walk starts from the create block's position, which the settle above collects. The settle is a fixed
	// window, and a slow server or a starved machine can deliver the block after it: keep answering the world, up to
	// createBlockWait, until the block arrives instead of failing a run whose block is merely late.
	if s.mover == nil {
		errBlockArrived := errors.New("create block arrived")
		err := s.drain(ctx, "the character's create block", time.Now().Add(createBlockWait), func() error {
			if s.mover != nil {
				return errBlockArrived
			}
			return nil
		})
		if err != nil && !errors.Is(err, errBlockArrived) {
			return err
		}
	}
	if s.mover == nil {
		return errors.New("route-walk: no create block of this character arrived, so there is no position to walk from")
	}
	q := command.NewSession(&cmdLink{s: s})
	if err := s.confirmGM(ctx); err != nil {
		return err
	}
	if o.GM {
		if err := s.routeGM(ctx, q, true); err != nil {
			return err
		}
		// GM mode is the caller's: put it back even when the walk ends with an error.
		defer s.routeGMOff(q)
	}
	// Leave the instance before GM mode goes off (the defer above runs after this one): a level-1 character left
	// among the packs aggroes the moment the GM flag clears, and the server then refuses the logout (result 1).
	if o.Leave != "none" {
		defer s.routeLeave(q)
	}
	entered, pos, err := s.routeEnter(ctx, q)
	rep.Entry = entered
	rep.EntryPos = [3]float32{pos[0], pos[1], pos[2]}
	if err != nil {
		return err
	}
	s.opts.logf("route-walk: %s; the server confirms the character at %.2f %.2f %.2f on map %d",
		entered, pos[0], pos[1], pos[2], s.world.MapID())

	planner, err := s.navPlanner()
	if err != nil {
		return fmt.Errorf("route-walk: navmesh: %w", err)
	}
	defer s.dropPlanner()
	// The plan's avoid boxes keep the route off the ground it says kills (the lava basin and the shelf): the plan
	// check gives its planner the same boxes.
	if len(o.Plan.Avoid) > 0 {
		planner.SetAvoid(o.Plan.Map, dungeon.NavBoxes(o.Plan.Avoid))
	}

	for _, li := range legs {
		leg, at := s.routeWalkLeg(ctx, q, planner, o, runStart, li, pos)
		rep.Legs = append(rep.Legs, leg)
		pos = at
		rep.Verdict = worseVerdict(rep.Verdict, leg.Status)
		s.opts.logf("route-walk: leg %d %-16s %-8s %5.1f of %5.1f yd, %d point(s), max deviation %.2f yd, max snap %.2f yd%s",
			li, leg.Name, leg.Status, leg.WalkedYd, leg.PlannedYd, len(leg.Waypoints), leg.MaxDev, leg.MaxSnap, reasonSuffix(leg.Reason))
		if err := ctx.Err(); err != nil {
			return err
		}
		// Without a confirmed position there is no honest origin for the next leg: the run stops here rather than
		// planning from a point the character may have left.
		if leg.Status == RouteUnconfirmed {
			s.opts.logf("route-walk: the walk ends after leg %d: the server did not confirm where the character stands", li)
			break
		}
	}
	rep.Ended = time.Now()
	s.opts.logf("route-walk: %d leg(s), verdict %s, %.1f yd planned, %.1f yd confirmed, %s",
		len(rep.Legs), rep.Verdict, sumLegYd(rep.Legs, true), sumLegYd(rep.Legs, false), rep.Ended.Sub(rep.Started).Round(time.Millisecond))
	return nil
}

// sumLegYd is the planned (or confirmed) distance over all legs.
func sumLegYd(legs []RouteLeg, planned bool) float64 {
	var out float64
	for _, l := range legs {
		if planned {
			out += l.PlannedYd
		} else {
			out += l.WalkedYd
		}
	}
	return out
}

// worseVerdict keeps the more serious of two verdicts; PASS is the least serious and the order is the one the
// table's reader expects (a liquid or a fall outranks a deviation).
func worseVerdict(a, b string) string {
	rank := func(v string) int {
		switch v {
		case RoutePass:
			return 0
		case RouteDeviated:
			return 1
		case RouteUnconfirmed:
			return 2
		case RouteNoPath:
			return 3
		case RouteStuck:
			return 4
		case RouteFell:
			return 5
		case RouteLiquid:
			return 6
		case RouteIncomplete:
			return 7
		case RouteFail:
			return 8
		}
		return 8
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// routeGM runs `.gm on`/`.gm off` and waits for the core's notification. The notification is the only answer (the
// command sends SMSG_PRINT_NOTIFICATION, which the session feeds to the command channel), so no line is a noted fact,
// not a failure: the guard already proved the permission.
func (s *session) routeGM(ctx context.Context, q *command.Session, on bool) error {
	line := ".gm off"
	if on {
		line = ".gm on"
	}
	rep, err := s.runCommandLine(ctx, q, line, command.Options{Quiet: 700 * time.Millisecond, Deadline: 5 * time.Second})
	s.noteGuard(rep, err)
	switch {
	case err == nil:
		s.opts.logf("route-walk: %s answered: %s", line, firstReplyLine(rep))
		return nil
	case errors.Is(err, command.ErrNoReply):
		s.opts.logf("route-walk: %s got no chat answer (the permission was confirmed; continuing)", line)
		return nil
	default:
		return fmt.Errorf("route-walk: %s: %w", line, err)
	}
}

// routeGMOff is the deferred undo: best effort, and nothing is sent after a dead link or a guard stop.
func (s *session) routeGMOff(q *command.Session) {
	if s.gmStop != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.routeGM(ctx, q, false); err != nil {
		s.opts.logf("route-walk: .gm off: %v", err)
	}
}

func firstReplyLine(rep *command.Reply) string {
	if rep == nil || len(rep.Lines) == 0 {
		return "(no line)"
	}
	return rep.Lines[0].Raw
}

// routeLeave teleports the character to the plan's outside_tele when the walk ends inside. It is best effort and
// uses its own deadline: the run may be ending on the session's.
func (s *session) routeLeave(q *command.Session) {
	plan := s.opts.RouteWalk.Plan
	if plan.OutsideTele == "" || s.world.MapID() != plan.Map {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	line := ".tele " + plan.OutsideTele
	rep, err := s.runCommandLine(ctx, q, line, command.Options{Quiet: 700 * time.Millisecond, Deadline: 8 * time.Second})
	s.noteGuard(rep, err)
	if err != nil && !errors.Is(err, command.ErrNoReply) {
		s.opts.logf("route-walk: %s: %v", line, err)
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for s.world.MapID() == plan.Map && time.Now().Before(deadline) {
		if err := s.collect(ctx, 250*time.Millisecond); err != nil {
			return
		}
	}
	if s.world.MapID() == plan.Map {
		s.opts.logf("route-walk: %s did not leave map %d within 30s", line, plan.Map)
		return
	}
	s.opts.logf("route-walk: %s; the character is on map %d again", line, s.world.MapID())
}

// routeEnter makes sure the character stands in the plan's instance: a GM teleport to the plan's entrance when the
// client is on another map, then a position the server confirms. The teleport names the plan's entrance_tele row (the
// server's own areatrigger destination); the plan's entrance point is the arrival check, not the teleport target.
func (s *session) routeEnter(ctx context.Context, q *command.Session) (string, navmesh.Vec3, error) {
	enter := s.opts.RouteWalk.Enter
	if enter == "" {
		enter = "tele"
	}
	if s.world.MapID() == s.opts.RouteWalk.Plan.Map {
		pos, mapID, err := s.routeGPS(ctx, q)
		if err != nil {
			return "", navmesh.Vec3{}, err
		}
		if mapID != 0 && mapID != s.opts.RouteWalk.Plan.Map {
			return "", navmesh.Vec3{}, fmt.Errorf("route-walk: the server says the character is on map %d, the plan is map %d", mapID, s.opts.RouteWalk.Plan.Map)
		}
		return "already on the instance map", pos, nil
	}
	if enter != "tele" {
		return "", navmesh.Vec3{}, fmt.Errorf("route-walk: the character is on map %d, the plan is map %d, and --enter none does not move it", s.world.MapID(), s.opts.RouteWalk.Plan.Map)
	}
	tele := s.opts.RouteWalk.Plan.EntranceTele
	if tele == "" {
		return "", navmesh.Vec3{}, fmt.Errorf("route-walk: the character is on map %d and the plan names no entrance_tele to reach map %d", s.world.MapID(), s.opts.RouteWalk.Plan.Map)
	}
	line := ".tele " + tele
	rep, err := s.runCommandLine(ctx, q, line, command.Options{Quiet: 700 * time.Millisecond, Deadline: 8 * time.Second})
	s.noteGuard(rep, err)
	if err != nil && !errors.Is(err, command.ErrNoReply) {
		return "", navmesh.Vec3{}, fmt.Errorf("route-walk: %s: %w", line, err)
	}
	s.opts.logf("route-walk: %s sent; waiting up to %s for the instance", line, routeEntryWait)
	deadline := time.Now().Add(routeEntryWait)
	var lastErr error
	for {
		if s.world.MapID() == s.opts.RouteWalk.Plan.Map && s.mover != nil && !s.mover.BetweenMaps() {
			pos, mapID, err := s.routeGPS(ctx, q)
			if err == nil && (mapID == 0 || mapID == s.opts.RouteWalk.Plan.Map) {
				return fmt.Sprintf("entered by %s", line), pos, nil
			}
			// A hidden or refused `.gps` will not get better by waiting: report it now, not at the entry deadline.
			if errors.Is(err, command.ErrUnknownCommand) || errors.Is(err, command.ErrNoPermission) {
				return "", navmesh.Vec3{}, err
			}
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return "", navmesh.Vec3{}, fmt.Errorf("route-walk: the character did not reach map %d within %s (%v)", s.opts.RouteWalk.Plan.Map, routeEntryWait, lastErr)
			}
			return "", navmesh.Vec3{}, fmt.Errorf("route-walk: the character did not reach map %d within %s", s.opts.RouteWalk.Plan.Map, routeEntryWait)
		}
		if err := s.collect(ctx, 250*time.Millisecond); err != nil {
			return "", navmesh.Vec3{}, err
		}
	}
}

// errRouteUnconfirmed marks a stop whose position the server did not confirm: no marker echo, no complete block,
// more than one block in one read, a reply that ended at its own bound, or two reads that disagree. The leg is
// UNCONFIRMED and the run is not PASS; the tool never records the client's belief as the server's answer.
var errRouteUnconfirmed = errors.New("route-walk: the server did not confirm the position")

// Freshness bound of one `.gps` confirmation: two post-request reads that agree within half a yard are the same
// position, whether it is the stop itself or a real offset (a correction, a server that stores a different spot).
const routeGPSAgreeYd = 0.5

// routeGPS selects this character and reads `.gps` without an expected stop: the entry check and a map-change
// correction. See routeGPSAt for the leg's confirmation.
func (s *session) routeGPS(ctx context.Context, q *command.Session) (navmesh.Vec3, uint32, error) {
	return s.routeGPSAt(ctx, q, nil)
}

// routeGPSAt reads `.gps` and, when want names the stop the character just walked to, ties the answer to the
// request: a confirmation is two post-request reads that agree. A stop is never confirmed from one read, however
// close its position is: a block the server wrote for an earlier command can still arrive after the new request, and
// it must not become this stop's answer.
func (s *session) routeGPSAt(ctx context.Context, q *command.Session, want *navmesh.Vec3) (navmesh.Vec3, uint32, error) {
	if s.opts.RouteWalk != nil && s.opts.RouteWalk.Confirm == RouteConfirmClient {
		// The server's position is not readable here: the last movement packet the client sent is the position the
		// server was told, and a server that rejects it answers with a correction (a teleport or a create) the
		// mover follows. The report names this source; a silent refusal (a root, a stun) is invisible to it.
		x, y, z, _ := s.mover.Position()
		return navmesh.Vec3{x, y, z}, s.world.MapID(), nil
	}
	pos, mapID, err := s.routeGPSRead(ctx, q)
	if err != nil || want == nil {
		return pos, mapID, err
	}
	// A real offset (a correction, a server that stores a different spot) reproduces on the second read; a block
	// written for an earlier request does not, and the pair is UNCONFIRMED rather than a confirmation.
	pos2, mapID2, err2 := s.routeGPSRead(ctx, q)
	if err2 != nil {
		return navmesh.Vec3{}, 0, fmt.Errorf("%w: the second read failed: %v", errRouteUnconfirmed, err2)
	}
	if mapID != mapID2 {
		return navmesh.Vec3{}, 0, fmt.Errorf("%w: two reads name map %d and map %d", errRouteUnconfirmed, mapID, mapID2)
	}
	if d := dist3v(pos, pos2); d > routeGPSAgreeYd {
		return navmesh.Vec3{}, 0, fmt.Errorf("%w: two reads disagree (%.2f yd apart, %.2f and %.2f yd from the stop)", errRouteUnconfirmed, d, dist3v(pos, *want), dist3v(pos2, *want))
	}
	return pos2, mapID2, nil
}

// routeGPSRead sends one marker, waits for its echo, then sends one `.gps` and reads one complete block. The echo
// makes the answer this request's own: the core handles this session's chat messages in order and writes the
// whisper's echo to the same stream, so every reply the server wrote before the marker is written before the echo,
// and the wait consumes all of it. Only a block whose map line precedes its coordinates line and which carries the
// grid line after them is an answer at all; two blocks, a missing part or a reply the collector ended at its
// deadline or line cap is not a confirmation.
func (s *session) routeGPSRead(ctx context.Context, q *command.Session) (navmesh.Vec3, uint32, error) {
	self := s.selfOrMover()
	if self.IsZero() {
		return navmesh.Vec3{}, 0, errors.New("route-walk: the character's GUID is not known yet")
	}
	// The core reports the selected unit: select this character (once per map; a map change can clear it).
	if s.routeSelectGUID != self || s.routeSelectMap != s.world.MapID() {
		if err := s.transport().Send(wire.CMSGSetSelection, combat.BuildSetSelection(self)); err != nil {
			return navmesh.Vec3{}, 0, fmt.Errorf("route-walk: CMSG_SET_SELECTION: %w", err)
		}
		s.routeSelectGUID = self
		s.routeSelectMap = s.world.MapID()
	}
	quiet := routeGPSQuiet
	if s.opts.RouteWalk != nil && s.opts.RouteWalk.GPSQuiet > 0 {
		quiet = s.opts.RouteWalk.GPSQuiet
	}
	// The marker's echo is the barrier: when it is seen, no reply to an earlier request is still on the way, so the
	// `.gps` sent after it is the first request whose answer the stream can carry.
	if err := s.routeGPSBarrier(ctx); err != nil {
		return navmesh.Vec3{}, 0, err
	}
	rep, err := s.runCommandLine(ctx, q, ".gps", command.Options{Quiet: quiet, Deadline: routeGPSDeadline})
	s.noteGuard(rep, err)
	switch {
	case err == nil:
	case errors.Is(err, command.ErrNoReply):
		// A reply without a line cannot hold a block; the parse below refuses it.
	case errors.Is(err, command.ErrUnknownCommand), errors.Is(err, command.ErrNoPermission):
		return navmesh.Vec3{}, 0, fmt.Errorf("route-walk: this account cannot read the server's position (.gps is hidden: %w); pass --confirm client to record the last movement packet instead", err)
	default:
		if ctx.Err() != nil {
			return navmesh.Vec3{}, 0, ctx.Err()
		}
		return navmesh.Vec3{}, 0, fmt.Errorf("route-walk: .gps: %w", err)
	}
	if rep == nil {
		return navmesh.Vec3{}, 0, fmt.Errorf("%w: .gps got no answer", errRouteUnconfirmed)
	}
	if rep.End == "deadline" || rep.End == "maxlines" {
		return navmesh.Vec3{}, 0, fmt.Errorf("%w: the reply ended at the %s bound", errRouteUnconfirmed, rep.End)
	}
	pos, mapID, ok, why := parseRouteGPSBlock(rep.Raw)
	if !ok {
		return navmesh.Vec3{}, 0, fmt.Errorf("%w: %s (the server answered %d line(s))", errRouteUnconfirmed, why, len(rep.Raw))
	}
	return pos, mapID, nil
}

// routeGPSBarrier sends one self-whisper carrying a fresh marker and pumps until the server echoes it. The echo is
// what ties a reply to its request: the core handles this session's chat messages one at a time and writes every
// answer to the session's own stream, so a request sent before the marker is answered before the echo. When the echo
// arrives, nothing older is left in the stream, and the reply that follows the next request can only be that
// request's. A marked line from any other sender is not the echo.
func (s *session) routeGPSBarrier(ctx context.Context) error {
	if s.gmStop != nil {
		return s.gmStop
	}
	name := ""
	if s.report != nil {
		name = strings.TrimSpace(s.report.Character.Name)
	}
	if name == "" {
		return fmt.Errorf("%w: the character's own name is not known yet, so no request can be marked", errRouteUnconfirmed)
	}
	tok, err := command.NewToken()
	if err != nil {
		return err
	}
	lang, err := s.chatLanguage()
	if err != nil {
		return fmt.Errorf("%w: %v", errRouteUnconfirmed, err)
	}
	marker := "route-gps " + tok
	seen := false
	feed := func(line chat.Line) {
		if seen || line.Type != chat.TypeWhisper || !s.isSelf(line) {
			return
		}
		if strings.Contains(line.Text, tok) {
			seen = true
		}
	}
	s.cmdFeed = feed
	defer func() { s.cmdFeed = nil }()
	if err := s.Send(wire.CMSGChatMessageWhisper, chat.BuildWhisper(lang, name, marker)); err != nil {
		return fmt.Errorf("route-walk: the .gps marker: %w", err)
	}
	deadline := time.Now().Add(routeGPSDeadline)
	for !seen {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("%w: the marker whisper got no echo, so no answer can be tied to the next request", errRouteUnconfirmed)
		}
		if remaining > cmdPoll {
			remaining = cmdPoll
		}
		if err := s.collect(ctx, remaining); err != nil {
			return err
		}
	}
	return nil
}

// routeGPSRe matches the core's `.gps` coordinates line
// ("X: -158.440994 Y: 131.600998 Z: -74.255203 Orientation: ...", cs_misc.cpp:275-289).
var routeGPSRe = regexp.MustCompile(`(?i)\bx:\s*(-?[0-9]+(?:\.[0-9]+)?)\s+y:\s*(-?[0-9]+(?:\.[0-9]+)?)\s+z:\s*(-?[0-9]+(?:\.[0-9]+)?)`)

// routeGPSMapRe matches the map id of the `.gps` map line.
var routeGPSMapRe = regexp.MustCompile(`(?i)\bmap:\s*([0-9]+)\b`)

// parseRouteGPS reads the coordinates line of a `.gps` answer.
func parseRouteGPS(s string) (navmesh.Vec3, bool) {
	m := routeGPSRe.FindStringSubmatch(s)
	if m == nil {
		return navmesh.Vec3{}, false
	}
	x, e1 := strconv.ParseFloat(m[1], 32)
	y, e2 := strconv.ParseFloat(m[2], 32)
	z, e3 := strconv.ParseFloat(m[3], 32)
	if e1 != nil || e2 != nil || e3 != nil {
		return navmesh.Vec3{}, false
	}
	return navmesh.Vec3{float32(x), float32(y), float32(z)}, true
}

// isRouteGPSMapLine reports whether a line is the block's map line. The grid line's own "(Map: 1 VMap: 1)" is the
// tile's map and is not a second block.
func isRouteGPSMapLine(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "map:")
}

// parseRouteGPSBlock reads one `.gps` block out of a reply: exactly one map line, then one coordinates line, then
// the grid line. Two blocks, a line order that no single block has, or a missing grid line leaves the stop
// unconfirmed rather than guessing. The returned reason is the answer's own defect.
func parseRouteGPSBlock(raw []string) (navmesh.Vec3, uint32, bool, string) {
	mapAt, coordAt, gridAt := -1, -1, -1
	maps, coords := 0, 0
	var pos navmesh.Vec3
	var mapID uint32
	for i, ln := range raw {
		if isRouteGPSMapLine(ln) {
			if m, ok := parseRouteGPSMap(ln); ok {
				maps++
				if mapAt < 0 {
					mapAt, mapID = i, m
				}
			}
		}
		if p, ok := parseRouteGPS(ln); ok {
			coords++
			if coordAt < 0 {
				coordAt, pos = i, p
			}
		}
		if gridAt < 0 && strings.Contains(strings.ToLower(ln), "grid[") {
			gridAt = i
		}
	}
	switch {
	case coords == 0:
		return navmesh.Vec3{}, 0, false, "the answer has no coordinates line"
	case coords > 1:
		return navmesh.Vec3{}, 0, false, fmt.Sprintf("the answer has %d coordinates lines", coords)
	case maps > 1:
		return navmesh.Vec3{}, 0, false, fmt.Sprintf("the answer has %d map lines", maps)
	case mapAt < 0:
		return navmesh.Vec3{}, 0, false, "the answer has no map line"
	case mapAt > coordAt:
		return navmesh.Vec3{}, 0, false, "the coordinates line came before the map line"
	case gridAt < 0:
		return navmesh.Vec3{}, 0, false, "the answer has no grid line"
	case gridAt < coordAt:
		return navmesh.Vec3{}, 0, false, "the grid line came before the coordinates line"
	}
	return pos, mapID, true, ""
}

// parseRouteGPSMap reads the map id of a `.gps` answer; 0 says the line does not name one.
func parseRouteGPSMap(s string) (uint32, bool) {
	m := routeGPSMapRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}

// routeWalkLeg walks one leg: plan each hop (each via point in order, then the leg's To), walk the route's points,
// and confirm each one. The second return is the server's last confirmed position, where the next leg plans from.
func (s *session) routeWalkLeg(ctx context.Context, q *command.Session, planner *navmesh.Planner, o RouteWalkOptions, runStart time.Time, index int, from navmesh.Vec3) (RouteLeg, navmesh.Vec3) {
	leg := o.Plan.Legs[index]
	started := time.Now()
	rep := RouteLeg{
		Index:    index,
		Name:     leg.Name,
		From:     [3]float32{from[0], from[1], from[2]},
		Goal:     [3]float32{leg.To.X, leg.To.Y, leg.To.Z},
		Status:   RoutePass,
		StartedS: started.Sub(runStart).Seconds(),
	}
	cur := from
	maxDev := 0.0
	maxSnap := 0.0
	plannedYd := 0.0
	lastProgress := cur
	lastProgressAt := started

	hops := make([]routeHop, 0, len(leg.Via)+1)
	for _, w := range leg.Via {
		hops = append(hops, routeHop{to: navmesh.Vec3{w.X, w.Y, w.Z}, direct: w.Direct || w.GMCross, gmCross: w.GMCross})
	}
	hops = append(hops, routeHop{to: navmesh.Vec3{leg.To.X, leg.To.Y, leg.To.Z}})
	rep.Hops = len(hops)

	fail := func(status, reason string, at navmesh.Vec3) {
		if rep.Status == RoutePass {
			rep.Status = status
			rep.Reason = reason
		}
		rep.Issues = append(rep.Issues, fmt.Sprintf("%s at %.2f %.2f %.2f: %s", status, at[0], at[1], at[2], reason))
	}
	// incomplete marks the leg and the run as ended early: a deadline or a cancel is not a finding about the route,
	// but the leg did not finish its work and must never carry PASS.
	incomplete := func(reason string) {
		rep.Status = RouteIncomplete
		rep.Reason = reason
		rep.Issues = append(rep.Issues, reason)
	}
	if err := ctx.Err(); err != nil {
		incomplete(fmt.Sprintf("the run ended before this leg could start: %v", err))
		return finishRouteLeg(rep, 0, 0, 0, runStart), cur
	}

walk:
	for hi, hop := range hops {
		if hop.gmCross {
			fail(RouteNoPath, "the plan crosses this gap with a GM move (gm_cross), which route-walk does not do", cur)
			break
		}
		var points []navmesh.Vec3
		// hopGoal is the PLAN's point (the check's reference); meshGoal is the mesh point the planner ends on
		// (route.End), recorded as the snap distance and never used for the deviation.
		hopGoal := hop.to
		meshGoal := hop.to
		if hop.direct {
			// The plan's direct waypoint is a straight line over a gap the mesh does not link; the goal is the point
			// itself, so the straight walk is the plan's own movement.
			points = []navmesh.Vec3{hop.to}
			plannedYd += dist3v(cur, hop.to)
			s.opts.logf("route-walk: leg %d hop %d: direct straight line %.2f yd to %.2f %.2f %.2f",
				index, hi, dist3v(cur, hop.to), hop.to[0], hop.to[1], hop.to[2])
		} else {
			route, err := planner.PathTo(o.Plan.Map, cur, hop.to)
			if err != nil {
				fail(RouteNoPath, fmt.Sprintf("navmesh: %v", err), cur)
				break
			}
			if route.Kind != navmesh.RouteComplete {
				reason := route.Reason
				if reason == "" {
					reason = string(route.Kind)
				}
				fail(RouteNoPath, fmt.Sprintf("no complete route to %.2f %.2f %.2f: %s", hop.to[0], hop.to[1], hop.to[2], reason), cur)
				break
			}
			points = route.Points
			meshGoal = route.End
			plannedYd += float64(route.Length)
			s.opts.logf("route-walk: leg %d hop %d: %s route, %d point(s), %.1f yd to %.2f %.2f %.2f%s",
				index, hi, route.Kind, len(points), route.Length, route.End[0], route.End[1], route.End[2], reasonSuffix(route.Reason))
		}
		if len(points) == 0 {
			points = []navmesh.Vec3{hopGoal}
		}
		// The dangerous liquids are read off the mesh before the character walks into them.
		for _, p := range points {
			if name, wet := s.routeLiquidAt(planner, o.Plan.Map, p, o); wet {
				fail(RouteLiquid, fmt.Sprintf("the planned path crosses %s", name), p)
				break walk
			}
		}

		last := len(points) - 1
		prev := cur
		for pi := range points {
			target := points[pi]
			chunks := 1
			if step := float64(s.mover.RunSpeed) * o.Sample.Seconds(); step > 0 {
				if n := int(math.Ceil(dist3v(prev, target) / step)); n > 1 {
					chunks = n
				}
			}
			for ci := 1; ci <= chunks; ci++ {
				t := float32(ci) / float32(chunks)
				at := navmesh.Vec3{
					prev[0] + (target[0]-prev[0])*t,
					prev[1] + (target[1]-prev[1])*t,
					prev[2] + (target[2]-prev[2])*t,
				}
				if err := s.mover.MoveTo(ctx, at[0], at[1], at[2]); err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						incomplete(fmt.Sprintf("the run ended while walking to the stop: %v", err))
						return finishRouteLeg(rep, plannedYd, maxDev, maxSnap, runStart), cur
					}
					switch {
					case errors.Is(err, objects.ErrMapChange):
						fail(RouteFail, "the character left the map in the middle of the leg", at)
					case errors.Is(err, objects.ErrTeleported):
						srv, _, gerr := s.routeGPS(ctx, q)
						if gerr != nil {
							if errors.Is(gerr, errRouteUnconfirmed) {
								fail(RouteUnconfirmed, gerr.Error(), at)
							} else {
								rep.Status = RouteFail
								rep.Reason = gerr.Error()
								rep.Issues = append(rep.Issues, gerr.Error())
							}
							return finishRouteLeg(rep, plannedYd, maxDev, maxSnap, runStart), cur
						}
						fail(RouteDeviated, fmt.Sprintf("the server moved the character to %.2f %.2f %.2f in the middle of the leg", srv[0], srv[1], srv[2]), srv)
						cur = srv
					default:
						rep.Status = RouteFail
						rep.Reason = err.Error()
						rep.Issues = append(rep.Issues, err.Error())
						return finishRouteLeg(rep, plannedYd, maxDev, maxSnap, runStart), cur
					}
					break walk
				}
				if err := ctx.Err(); err != nil {
					incomplete(fmt.Sprintf("the run ended before the stop was confirmed: %v", err))
					return finishRouteLeg(rep, plannedYd, maxDev, maxSnap, runStart), cur
				}
				now := time.Now()
				srv, _, err := s.routeGPSAt(ctx, q, &at)
				if err != nil {
					switch {
					case ctx.Err() != nil:
						incomplete(fmt.Sprintf("the run ended before the stop was confirmed: %v", ctx.Err()))
						return finishRouteLeg(rep, plannedYd, maxDev, maxSnap, runStart), cur
					case errors.Is(err, errRouteUnconfirmed):
						fail(RouteUnconfirmed, err.Error(), at)
						break walk
					default:
						rep.Status = RouteFail
						rep.Reason = err.Error()
						rep.Issues = append(rep.Issues, err.Error())
						return finishRouteLeg(rep, plannedYd, maxDev, maxSnap, runStart), cur
					}
				}
				cur = srv
				// The last point of a hop is the plan's own waypoint: the confirmation is measured against it,
				// not against the mesh point the planner snapped the goal to. Between two hops there is no plan
				// point, and the walked path point is the reference.
				hopEnd := pi == last && ci == chunks
				ref := at
				if hopEnd {
					ref = hopGoal
				}
				wp := RouteWaypoint{
					Point:           [3]float32{at[0], at[1], at[2]},
					Server:          [3]float32{srv[0], srv[1], srv[2]},
					ServerConfirmed: o.Confirm == RouteConfirmGPS,
					DeviationYd:     dist3v(ref, srv),
					Dz:              float64(srv[2] - ref[2]),
					At:              now,
					AtS:             now.Sub(runStart).Seconds(),
				}
				if hopEnd {
					g := [3]float32{hopGoal[0], hopGoal[1], hopGoal[2]}
					wp.Goal = &g
					wp.SnapYd = dist3v(meshGoal, hopGoal)
					if wp.SnapYd > 0.01 {
						wp.Note = fmt.Sprintf("the mesh point is %.2f yd from the plan's point", wp.SnapYd)
					}
					if wp.SnapYd > maxSnap {
						maxSnap = wp.SnapYd
					}
				}
				if name, wet := s.routeLiquidAt(planner, o.Plan.Map, srv, o); wet {
					wp.Liquid = name
				}
				rep.Waypoints = append(rep.Waypoints, wp)
				if wp.DeviationYd > maxDev {
					maxDev = wp.DeviationYd
				}
				if wp.Liquid != "" {
					fail(RouteLiquid, fmt.Sprintf("the character stands in %s at the confirmed position", wp.Liquid), srv)
					break walk
				}
				if wp.SnapYd > o.MaxDeviation {
					fail(RouteDeviated, fmt.Sprintf("the mesh point is %.2f yd from the plan's point, beyond the %.2f-yd tolerance", wp.SnapYd, o.MaxDeviation), srv)
				}
				if wp.DeviationYd > o.MaxDeviation {
					fail(RouteDeviated, fmt.Sprintf("the server confirms %.2f yd from the planned point", wp.DeviationYd), srv)
				}
				if wp.Dz <= -o.FallDrop {
					fail(RouteFell, fmt.Sprintf("the server confirms the character %.2f yd below the planned point", -wp.Dz), srv)
					break walk
				}
				if o.Confirm == RouteConfirmGPS {
					// The stall test needs the server's own position; the client's belief advances by construction.
					if dist3v(srv, lastProgress) >= o.StuckStep {
						lastProgress = srv
						lastProgressAt = now
					} else if now.Sub(lastProgressAt) >= o.StuckAfter {
						fail(RouteStuck, fmt.Sprintf("the server's position did not move for %s", o.StuckAfter), srv)
						break walk
					}
				}
			}
			prev = target
		}
	}
	if rep.Status == RoutePass && maxDev > o.MaxDeviation {
		rep.Status = RouteDeviated
		rep.Reason = fmt.Sprintf("max deviation %.2f yd", maxDev)
	}
	return finishRouteLeg(rep, plannedYd, maxDev, maxSnap, runStart), cur
}

// finishRouteLeg fills a leg's totals.
func finishRouteLeg(rep RouteLeg, plannedYd, maxDev, maxSnap float64, runStart time.Time) RouteLeg {
	rep.PlannedYd = plannedYd
	rep.WalkedYd = routeWalkedYd(rep.Waypoints)
	rep.MaxDev = maxDev
	rep.MaxSnap = maxSnap
	rep.EndedS = time.Since(runStart).Seconds()
	return rep
}

// routeWalkedYd sums the confirmed positions' own steps.
func routeWalkedYd(wps []RouteWaypoint) float64 {
	var total float64
	for i := 1; i < len(wps); i++ {
		total += dist3f3(wps[i-1].Server, wps[i].Server)
	}
	return total
}

// routeLiquidAt reads the mesh's damaging-liquid polygons in the band round p. A floor at the point, over it or just
// under it that carries magma or slime fails: the mesh marks a lava bed and a lava surface alike, and a point below
// the surface is in it.
func (s *session) routeLiquidAt(planner *navmesh.Planner, mapID uint32, p navmesh.Vec3, o RouteWalkOptions) (string, bool) {
	t := planner.Format().Terrain()
	floors, err := planner.Floors(mapID, p[0], p[1], p[2]-float32(o.LiquidBelow), p[2]+float32(o.LiquidAbove))
	if err != nil {
		// A missing tile is a planning fact the leg already reports; no floor is no liquid.
		return "", false
	}
	for _, f := range floors {
		// Forever's mesh gives magma and slime one flag (0x08), so the mesh cannot tell them apart: name both.
		if f.Flags&t.Liquid() != 0 {
			return "magma/slime", true
		}
	}
	return "", false
}

// dist3v is the 3-D distance between two navmesh points.
func dist3v(a, b navmesh.Vec3) float64 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// dist3f3 is dist3v over the report's float32 triples.
func dist3f3(a, b [3]float32) float64 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
