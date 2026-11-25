package driver

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// The driver's own geometry: how far from a group's plan positions a unit may stand and still be a member, how far the
// commander may stand from a group's centre and judge it, and how near a hostile has to come to the next walked segment
// to be a hazard.
const (
	DefaultPackRadius = 25.0
	GroupViewYd       = 60.0
	HazardYd          = 20.0
	PatrolClearYd     = 25.0
	SegmentYd         = 25.0
	ArrivedYd         = 2.0
	PortalArriveYd    = 15.0
	MasterFollowYd    = 20.0
	PriestFollowYd    = 25.0
	VoidwalkerEntry   = 8996
	VoidwalkerNearYd  = 10.0
	// MoveClearMarginYd is the margin a bot's move line keeps outside a pack's radius: a bot ordered toward the
	// commander may not be routed within the group's radius (the plan's, or DefaultPackRadius 25 yd) plus this 10 yd.
	MoveClearMarginYd = 10.0
	// RetreatStepYd bounds one step of the commander's walk back toward a straggler behind an uncleared pack.
	RetreatStepYd = 20.0
)

// PlanRefusal names a plan field this round does not run. The command refuses such a plan before it calls any port
// (exit 2): a plan whose steps, holds, ambushes or optional legs are silently left out would run a different dungeon
// than the one its author wrote.
type PlanRefusal struct {
	Where string // the field's path, e.g. legs[2].pull_groups[3].hold
	Field string
	Why   string
}

func (e *PlanRefusal) Error() string {
	if e.Why != "" {
		return fmt.Sprintf("%s: %s: %s", e.Where, e.Field, e.Why)
	}
	return fmt.Sprintf("%s: %s is not run by this round", e.Where, e.Field)
}

// Portal is the entrance portal on the outer map.
type Portal struct {
	Map uint32
	Pos Vec3
}

// Box is a region the party must never stand in or walk through (the plan's avoid boxes: ground that kills although the
// map data calls it walkable).
type Box struct {
	Min, Max Vec3
}

// Contains reports whether p lies inside the box.
func (b Box) Contains(p Vec3) bool {
	return p[0] >= b.Min[0] && p[0] <= b.Max[0] && p[1] >= b.Min[1] && p[1] <= b.Max[1] && p[2] >= b.Min[2] && p[2] <= b.Max[2]
}

// Route is the part of a dungeon plan the driver runs: the legs, their packs and patrols, and the boss of each leg.
type Route struct {
	Name         string
	Map          uint32
	Entrance     Vec3
	Trigger      uint32
	EntranceTele string
	OutsideTele  string
	Portal       *Portal
	// LevelLo and LevelHi are the plan's level band; 0, 0 when the plan gives none.
	LevelLo, LevelHi int
	Fleers           map[uint32]bool
	Avoid            []Box
	Legs             []Leg
	// Raid is a raid watch's group: the plan's raid block (its size and the role split the plan plans for). Nil in every
	// other mode, which refuses a raid plan.
	Raid *dungeon.RaidSpec
	// TankMinHP is the plan's least tank max health. A player run reads it in FORM and stops a weaker tank
	// (tank-too-weak); the other modes refuse the field.
	TankMinHP int
	// KillFirst is the plan's kill order, best first. A player run puts the skull on the first entry of it that is
	// in the pack (P2b); the other modes refuse the field.
	KillFirst []uint32
	// MaxMinutes is the run's time budget. A player run sets it by plan name (90/150/120, D-14); the other modes
	// take the command's own default.
	MaxMinutes int
	// Notes are the plan fields a player run accepted without acting on them (its crowd-control lists, its bosses'
	// fight tactics, D-8), so the run's record can say so once instead of dropping them silently.
	Notes []string
}

// Leg is one stretch of the dungeon: walk the via points and To, clear the groups in order, then kill the boss.
type Leg struct {
	Index int
	Name  string
	To    Vec3
	Via   []Vec3
	// ViaExact and ViaDirect are a player run's reading of the via points (nil in the other modes, which refuse a
	// plan carrying either): an exact point is stood on within PlayExactYd before the walk goes on, and a direct one
	// is a straight-line hop the map data has no link for.
	ViaExact  []bool
	ViaDirect []bool
	Start     Vec3 // where a wiped party comes back to: the previous leg's end, or the entrance
	Groups    []Group
	Boss      *Boss
	// Steps are the leg's scripted steps in plan order (P4): they run once the leg's packs are down and before its
	// boss, as the module's own stepper orders them.
	Steps []Step
	// Skip lists entries the leg never pulls or waits for.
	Skip map[uint32]bool
}

// Group is one pack of the plan.
type Group struct {
	ID        int
	Leg       int
	Index     int
	Entries   []uint32
	Positions []Vec3
	Radius    float32
	// MemberRadius is the plan's member_radius: the radius the pack match uses where the plan sets one, because the
	// exported row's Radius must cover the whole group from its mean centre while the match works from the positions.
	MemberRadius float32
	Approach     []Vec3
	Patrol       *Patrol
	Centre       Vec3
}

// RadiusYd is the group's radius in yards, the plan's or the driver's default.
func (g Group) RadiusYd() float32 {
	if g.Radius > 0 {
		return g.Radius
	}
	return DefaultPackRadius
}

// MatchRadiusYd is the radius a member may stand from one of the group's positions: the plan's member_radius when it
// has one, else RadiusYd.
func (g Group) MatchRadiusYd() float32 {
	if g.MemberRadius > 0 {
		return g.MemberRadius
	}
	return g.RadiusYd()
}

// Patrol is a group of mobs that walks a fixed path.
type Patrol struct {
	Entries []uint32
	Path    []Vec3
	PeriodS float64
}

// Boss is the leg's boss; First lists the entries to kill before the boss itself.
type Boss struct {
	Leg      int
	Entry    uint32
	Pos      Vec3
	First    []uint32
	Radius   float32
	Approach []Vec3
	// WaitS is the plan's wait for a patrolling boss; a watch route keeps it (the module runs the wait), the run
	// route refuses it.
	WaitS float64
}

// LoadRoute reads a dungeon plan file and builds the driver's view of it for a run this client leads: the plan's
// in-fight run fields are refused, because this driver does not run them.
func LoadRoute(path string) (*Route, error) { return loadRoute(path, modeRun) }

// LoadWatchRoute reads a dungeon plan file for dungeon-watch: the module's own run executes the fight, so the plan's
// in-fight run fields are accepted unchanged (the loader has validated them).
func LoadWatchRoute(path string) (*Route, error) { return loadRoute(path, modeWatch) }

// LoadRaidWatchRoute reads a raid plan file for forever raid-watch: a watch route whose plan must be a raid, with its
// raid block kept as the group's size (Route.Raid).
func LoadRaidWatchRoute(path string) (*Route, error) { return loadRoute(path, modeRaidWatch) }

// LoadPlayRoute reads a dungeon plan file for dungeon-play: the player run leads the fights itself and refuses what it
// cannot run (NewPlayRoute).
func LoadPlayRoute(path string) (*Route, error) { return loadRoute(path, modePlay) }

// LoadPlayRaidRoute reads a raid plan file for a player-led raid run: the same player run, with the plan's raid block
// as the group it leads (NewPlayRaidRoute).
func LoadPlayRaidRoute(path string) (*Route, error) { return loadRoute(path, modePlayRaid) }

func loadRoute(path string, mode routeMode) (*Route, error) {
	p, err := dungeon.Load(path)
	if err != nil {
		return nil, err
	}
	return newRoute(p, mode)
}

// NewRoute builds the driver's view of a loaded plan for a run this client leads. Every field the driver cannot run is
// a PlanRefusal; the returned route is nil when there is one (a partial route would look runnable). A field is either
// read by the driver or, when the plan sets it to a non-default value, refused by name: a plan whose crowd control,
// kill order, holds or skips were silently left out would run a different dungeon than the one its author wrote. This
// includes the plan's in-fight run fields (kill_first, cc_priority, cc_always, no_cc, summoned_kill_first,
// tank_min_hp and a boss's tactics): they belong to whoever runs the fight, and this driver does not. NewWatchRoute is
// the one exception, for a run the server module's own bot executes. Five more groups are not run inputs and so are
// not refused: the plan's name, status and notes (descriptions), its level band and hire composition (party
// preparation happens before this driver), and entrance_tele, skip_export, a via's check_skip and a patrol's
// period_s/source/period_estimate (they belong to the remote entry mode, to plan-check or to plan-export; the driver
// enters through Portal or EnterAreaTrigger and gates a patrol by its live units, not by its period).
func NewRoute(p *dungeon.Plan) (*Route, error) { return newRoute(p, modeRun) }

// NewWatchRoute is NewRoute for dungeon-watch: the run is the server module's own bot's, so the plan's in-fight run
// fields are part of its inputs and are accepted unchanged, and so are the module's own movement and waiting: a
// straight-line or stand-on-the-point hop and a boss wait for a patrolling boss (Wailing Caverns). The loader has
// validated them (ranges, the kill_first/cc_priority rule, the summoned subset); this client neither rewrites nor acts
// on them.
func NewWatchRoute(p *dungeon.Plan) (*Route, error) { return newRoute(p, modeWatch) }

// NewPlayRoute is NewRoute for the player run: the party is the owner's four bots and this character, and the run
// walks the plan's legs and pulls its packs itself (P2b). It accepts the in-fight fields the run uses (a kill order,
// the tank preflight) and loads the crowd-control lists and the bosses' fight tactics without acting on them, noting
// them in Route.Notes (D-8: no player order for either exists in v1). P4 reads a leg's four step kinds (use_object,
// gossip, loot_object and wait_for); the step kinds a player run cannot do are refused by their plan path. It still
// refuses what it cannot run: a hold, an ambush, a stand pull, a leg or pack the map data cannot walk, an optional leg,
// an after-boss step, a raid plan and a one-team plan. The time budget comes from the plan's name (playMinutes).
func NewPlayRoute(p *dungeon.Plan) (*Route, error) { return newRoute(p, modePlay) }

// NewRaidWatchRoute is NewWatchRoute for a raid plan: the module's own run leads the raid, so the plan's in-fight fields
// are its inputs, and the plan's raid block becomes the group's size and role split (Route.Raid). A plan without kind
// raid and its raid block is refused: raid-watch runs a raid, never a five-bot party.
func NewRaidWatchRoute(p *dungeon.Plan) (*Route, error) { return newRoute(p, modeRaidWatch) }

// NewPlayRaidRoute is NewPlayRoute for a raid plan: the player leads the raid group himself, so the plan's raid block
// becomes the group's size and role split (Route.Raid), and every rule of the player run holds unchanged — the same
// accepted steps, the same refusals, the same time budget. A plan without kind raid and its raid block is refused:
// a player-led raid runs a raid, never a five-bot party (NewPlayRoute keeps refusing a raid).
func NewPlayRaidRoute(p *dungeon.Plan) (*Route, error) { return newRoute(p, modePlayRaid) }

// routeMode says who runs the fight: this client (modeRun), the server module's own bot (modeWatch), the player
// who leads the party himself (modePlay), the server module's own bot leading a raid (modeRaidWatch) or the player
// who leads the raid group himself (modePlayRaid). It decides which plan fields are inputs and which are refused.
type routeMode int

const (
	modeRun routeMode = iota
	modeWatch
	modePlay
	modeRaidWatch
	modePlayRaid
)

// playerRun says the mode is the player leading the group himself: a five-bot party or a raid. Every rule of the
// player run holds for both, so the checks read this instead of modePlay alone.
func (m routeMode) playerRun() bool { return m == modePlay || m == modePlayRaid }

func newRoute(p *dungeon.Plan, mode routeMode) (*Route, error) {
	var refusals []error
	refuse := func(where, field, why string) {
		refusals = append(refusals, &PlanRefusal{Where: where, Field: field, Why: why})
	}
	if p.Team != "" {
		refuse("team", "team", "a plan limited to one team is not run by this round")
	}
	if p.Kind == dungeon.KindRaid && mode != modeRaidWatch && mode != modePlayRaid {
		refuse("kind", "kind", "this round runs a five-bot dungeon party, not a raid")
	}
	if mode == modeRaidWatch && (p.Kind != dungeon.KindRaid || p.Raid == nil) {
		refuse("kind", "kind", "a raid watch runs a raid plan: kind raid with its raid block")
	}
	if mode == modePlayRaid && (p.Kind != dungeon.KindRaid || p.Raid == nil) {
		refuse("kind", "kind", "a player-led raid runs a raid plan: kind raid with its raid block")
	}
	// The in-fight run fields are the watched module's own inputs; a run this client leads does not run them, so it
	// refuses them rather than silently dropping them. A player run is the party's leader and fights itself: it reads
	// the kill order and the tank preflight, and loads the crowd-control lists and the bosses' tactics without acting
	// on them (D-8), so they are neither refused nor silently dropped.
	noCC := len(p.CCPriority) > 0 || len(p.CCAlways) > 0 || len(p.NoCC) > 0
	if mode == modeRun {
		if len(p.KillFirst) > 0 {
			refuse("kill_first", "kill_first", "a kill order inside a fight is not run by this round")
		}
		if len(p.CCPriority) > 0 {
			refuse("cc_priority", "cc_priority", "crowd control is not run by this round")
		}
		if len(p.CCAlways) > 0 {
			refuse("cc_always", "cc_always", "crowd control is not run by this round")
		}
		if len(p.NoCC) > 0 {
			refuse("no_cc", "no_cc", "crowd control is not run by this round")
		}
		if len(p.SummonedKillFirst) > 0 {
			refuse("summoned_kill_first", "summoned_kill_first", "a kill order inside a fight is not run by this round")
		}
		if p.TankMinHP != 0 {
			refuse("tank_min_hp", "tank_min_hp", "the tank health preflight is not run by this round")
		}
	}
	if mode.playerRun() && len(p.SummonedKillFirst) > 0 {
		refuse("summoned_kill_first", "summoned_kill_first", "a summoned kill target is not run by this round")
	}
	if p.PullCeiling != 0 {
		refuse("pull_ceiling", "pull_ceiling", "the pull-size ceiling is a plan-check rule, not a run rule")
	}
	if p.Watchdog != nil {
		refuse("watchdog", "watchdog", "the plan's stall levels are not used by this round")
	}
	r := &Route{
		Name:         p.Name,
		Map:          p.Map,
		Entrance:     vec(p.Entrance),
		Trigger:      p.EntranceTrigger,
		EntranceTele: p.EntranceTele,
		OutsideTele:  p.OutsideTele,
		Fleers:       map[uint32]bool{},
	}
	if lo, hi := p.Band(); lo > 0 {
		r.LevelLo, r.LevelHi = lo, hi
	}
	if (mode == modeRaidWatch || mode == modePlayRaid) && p.Raid != nil {
		raid := *p.Raid
		r.Raid = &raid
	}
	for _, f := range p.Fleers {
		r.Fleers[f] = true
	}
	if p.Portal != nil {
		r.Portal = &Portal{Map: p.Portal.Map, Pos: vec(p.Portal.Point)}
	}
	for _, b := range p.Avoid {
		r.Avoid = append(r.Avoid, Box{Min: vec(b.Min), Max: vec(b.Max)})
	}
	if mode.playerRun() {
		if noCC {
			r.Notes = append(r.Notes, playNoCCNote)
		}
		r.KillFirst = append([]uint32(nil), p.KillFirst...)
		r.TankMinHP = int(p.TankMinHP)
		r.MaxMinutes = playMinutes(p.Name)
	}
	hasTactics := false
	// gossipBy says a plan's gossip step names a bot to talk: a player run talks itself and notes it once.
	gossipBy := false
	nextID := 0
	for i, pl := range p.Legs {
		where := fmt.Sprintf("legs[%d]", i)
		l := Leg{Index: i, Name: pl.Name, To: vec(pl.To), Skip: map[uint32]bool{}}
		if i == 0 {
			l.Start = r.Entrance
		} else {
			l.Start = vec(p.Legs[i-1].To)
		}
		if len(pl.Steps) > 0 {
			if !mode.playerRun() {
				refuse(where, "steps", "a step is not run by this round")
			} else {
				for j, s := range pl.Steps {
					st, errs := playStep(fmt.Sprintf("%s.steps[%d]", where, j), s)
					refusals = append(refusals, errs...)
					if len(errs) > 0 {
						continue
					}
					l.Steps = append(l.Steps, st)
					if g := st.Gossip; g != nil && g.By != "" && g.By != dungeon.GossipByAgent {
						gossipBy = true
					}
				}
			}
		}
		if len(pl.AfterBoss) > 0 {
			refuse(where, "after_boss", "a step after the boss is not run by this round")
		}
		if pl.Optional != nil {
			refuse(where, "optional", "an optional leg is not run by this round")
		}
		if len(pl.Leave) > 0 {
			refuse(where, "leave", "spawns to leave alone are not run by this round")
		}
		if pl.NeedsWalk {
			refuse(where, "needs_walk", "the leg's end is not on the walkable map data")
		}
		if len(pl.LeftoverEntries) > 0 {
			refuse(where, "leftover_entries", "a sweep for leftovers is not run by this round")
		}
		if pl.Rest != nil {
			refuse(where, "rest", "a leg's own rest gates are not run by this round")
		}
		if pl.CompanionsLoot != nil {
			refuse(where, "companions_loot", "the party's looting is not run by this round")
		}
		for j, w := range pl.Via {
			// A straight-line hop and a stand-on-the-point hop are steps of the module's own (remote) run: the watched
			// bot walks them and the watch only follows the leg, so a watch takes them. A player run walks them too
			// (the walk is its own, and Wailing Caverns' jump needs both) and keeps their flags. A run this client
			// leads without a player keeps refusing them, and a GM hop is refused for every mode: only a GM account
			// travels by GM.
			if w.GMCross || mode == modeRun && w.Direct {
				refuse(fmt.Sprintf("%s.via[%d]", where, j), "via.direct", "a straight-line or GM hop is not run by this round")
			}
			if mode == modeRun && w.Exact {
				refuse(fmt.Sprintf("%s.via[%d]", where, j), "via.exact", "a stand-on-the-point hop is not run by this round")
			}
			l.Via = append(l.Via, vec(w.Point))
			if mode.playerRun() {
				// Wailing Caverns' Serpentis leg: stand on the foot of the jump, then hop it. A player run walks
				// both itself (the walk is this round's), so it keeps the flags instead of refusing the plan.
				l.ViaExact = append(l.ViaExact, w.Exact)
				l.ViaDirect = append(l.ViaDirect, w.Direct)
			}
		}
		for _, e := range pl.Skip {
			l.Skip[e] = true
		}
		for j, pg := range pl.PullGroups {
			gwhere := fmt.Sprintf("%s.pull_groups[%d]", where, j)
			if pg.Hold != nil {
				refuse(gwhere, "hold", "a scripted hold is not run by this round")
			}
			if pg.Ambush {
				refuse(gwhere, "ambush", "a stealthed pack is not run by this round")
			}
			if pg.NeedsWalk {
				refuse(gwhere, "needs_walk", "the pack's positions are not on the walkable map data")
			}
			if pg.StandPull {
				refuse(gwhere, "stand_pull", "a timed stand pull is not run by this round")
			}
			if pg.ClearYd != 0 {
				refuse(gwhere, "clear_yd", "a timed stand pull is not run by this round")
			}
			if pg.MaxBodies != 0 {
				refuse(gwhere, "max_bodies", "a pull-size ceiling is not run by this round")
			}
			if pg.Team != "" {
				refuse(gwhere, "team", "a pack limited to one team is not run by this round")
			}
			if pg.Equiv != "" {
				refuse(gwhere, "equiv", "equivalent packs are a pull-test rule, not a run rule")
			}
			// A skipped entry is never pulled or waited for: it is not part of the pack's membership, and a group
			// left with no entries is not a group.
			entries := skipEntries(pg.Entries, l.Skip)
			if len(entries) == 0 {
				continue
			}
			g := Group{ID: nextID, Leg: i, Index: j, Entries: entries, Radius: pg.Radius, MemberRadius: pg.MemberRadius}
			nextID++
			for _, pos := range pg.Positions {
				g.Positions = append(g.Positions, vec(pos))
			}
			for _, pos := range pg.Approach {
				g.Approach = append(g.Approach, vec(pos))
			}
			if pg.Patrol != nil {
				pt := &Patrol{Entries: append([]uint32(nil), pg.Patrol.Entries...), PeriodS: float64(pg.Patrol.PeriodS)}
				for _, pos := range pg.Patrol.Path {
					pt.Path = append(pt.Path, vec(pos))
				}
				g.Patrol = pt
			}
			if len(g.Positions) > 0 {
				g.Centre = vec(dungeon.PackCentre(pg))
			}
			l.Groups = append(l.Groups, g)
		}
		if pl.Boss != nil {
			bw := where + ".boss"
			if pl.Boss.NeedsWalk {
				refuse(bw, "needs_walk", "the boss's position is not on the walkable map data")
			}
			if pl.Boss.Hold != nil {
				refuse(bw, "hold", "a scripted hold is not run by this round")
			}
			if pl.Boss.LootItem != nil {
				refuse(bw, "loot_item", "looting the boss is not run by this round")
			}
			if mode == modeRun && pl.Boss.Tactics != nil {
				refuse(bw, "tactics", "the party's fight tactics are not run by this round")
			}
			if mode.playerRun() && pl.Boss.Tactics != nil {
				hasTactics = true // noted once, after the legs: no player order for a tactic exists in v1
			}
			// The wait for a patrolling boss is the module's own: the watch follows the leg and sees the boss come.
			// A player run waits for it on the walk to the boss (P2b), so it keeps the plan's value.
			if mode == modeRun && pl.Boss.WaitS != 0 {
				refuse(bw, "wait_s", "a boss wait for a patrolling boss is not run by this round")
			}
			if len(pl.Boss.PhaseEntries) > 0 {
				refuse(bw, "phase_entries", "an encounter's phase entries are not run by this round")
			}
			if pl.Boss.StandPull {
				refuse(bw, "stand_pull", "a timed stand pull is not run by this round")
			}
			if pl.Boss.SummonOpenStep != 0 {
				refuse(bw, "summon_open_step", "a boss-summoning step is not run by this round")
			}
			if pl.Boss.SkipUnreachable != "" {
				refuse(bw, "skip_unreachable", "a boss out of the party's reach is not run by this round")
			}
			if pl.Boss.Team != "" {
				refuse(bw, "team", "a boss limited to one team is not run by this round")
			}
			if pl.Boss.CompanionsLoot != nil {
				refuse(bw, "companions_loot", "the party's looting is not run by this round")
			}
			l.Boss = &Boss{Leg: i, Entry: pl.Boss.Entry, Pos: vec(pl.Boss.Position), First: skipEntries(pl.Boss.First, l.Skip), Radius: pl.Boss.Radius}
			if mode != modeRun {
				l.Boss.WaitS = float64(pl.Boss.WaitS)
			}
			for _, p := range pl.Boss.Approach {
				l.Boss.Approach = append(l.Boss.Approach, vec(p))
			}
		}
		r.Legs = append(r.Legs, l)
	}
	if hasTactics {
		r.Notes = append(r.Notes, playTacticsNote)
	}
	if gossipBy {
		r.Notes = append(r.Notes, playGossipByNote)
	}
	if err := errors.Join(refusals...); err != nil {
		return nil, err
	}
	return r, nil
}

// vec converts a plan point.
func vec(p dungeon.Point) Vec3 { return Vec3{p.X, p.Y, p.Z} }

// skipEntries drops the entries a leg's Skip names: the leg never pulls them or waits for them.
func skipEntries(entries []uint32, skip map[uint32]bool) []uint32 {
	if len(entries) == 0 || len(skip) == 0 {
		return append([]uint32(nil), entries...)
	}
	out := make([]uint32, 0, len(entries))
	for _, e := range entries {
		if !skip[e] {
			out = append(out, e)
		}
	}
	return out
}

// point converts a driver position back to a plan point.
func point(v Vec3) dungeon.Point { return dungeon.Point{X: v[0], Y: v[1], Z: v[2]} }

// LookPoint is where the commander stands to look at a pack: the pack's last approach point when the plan gives one,
// otherwise the driver's default standoff from the pack's centre along the line it came by.
func (g Group) LookPoint(from Vec3) (Vec3, bool) {
	if len(g.Approach) > 0 {
		return g.Approach[len(g.Approach)-1], true
	}
	if len(g.Positions) == 0 {
		return from, false
	}
	p, ok := dungeon.DefaultLookPoint(point(from), point(g.Centre))
	return vec(p), ok
}

// Stand is the point the commander walks to before it pulls the group.
func (g Group) Stand(from Vec3) Vec3 {
	if p, ok := g.LookPoint(from); ok {
		return p
	}
	return from
}

// NearestPosition is the distance from p to the nearest of the group's positions.
func (g Group) NearestPosition(p Vec3) float64 {
	best := math.Inf(1)
	for _, pos := range g.Positions {
		best = math.Min(best, dist3(p, pos))
	}
	return best
}

// InAvoid reports the first avoid box that holds p.
func (r *Route) InAvoid(p Vec3) (Box, bool) {
	for _, b := range r.Avoid {
		if b.Contains(p) {
			return b, true
		}
	}
	return Box{}, false
}

// LegNamed finds a leg by its name, case-insensitively.
func (r *Route) LegNamed(name string) (int, bool) {
	for i, l := range r.Legs {
		if strings.EqualFold(l.Name, name) {
			return i, true
		}
	}
	return 0, false
}

// LegSelection resolves --from-leg and --legs into the indices to run, in plan order. An empty legs list runs from
// fromLeg to the end.
func (r *Route) LegSelection(fromLeg int, legs []string) ([]int, error) {
	if fromLeg < 0 || fromLeg >= len(r.Legs) {
		return nil, fmt.Errorf("from leg %d is outside the plan's %d legs", fromLeg, len(r.Legs))
	}
	if len(legs) == 0 {
		out := make([]int, 0, len(r.Legs)-fromLeg)
		for i := fromLeg; i < len(r.Legs); i++ {
			out = append(out, i)
		}
		return out, nil
	}
	seen := map[int]bool{}
	var out []int
	for _, name := range legs {
		i, ok := r.LegNamed(strings.TrimSpace(name))
		if !ok {
			return nil, fmt.Errorf("no leg named %q", name)
		}
		if i < fromLeg {
			return nil, fmt.Errorf("leg %q comes before --from-leg %d", name, fromLeg)
		}
		if !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	slices.Sort(out)
	return out, nil
}

// SplitRoute cuts a walk polyline into pieces of at most maxYd yards, so the driver can poll between them.
func SplitRoute(points []Vec3, maxYd float64) [][][3]float32 {
	if len(points) < 2 {
		if len(points) == 1 {
			return [][][3]float32{{points[0]}}
		}
		return nil
	}
	var out [][][3]float32
	cur := [][3]float32{points[0]}
	var d float64
	for i := 1; i < len(points); i++ {
		step := dist3(points[i-1], points[i])
		if d+step > maxYd && len(cur) > 1 {
			out = append(out, cur)
			cur = [][3]float32{points[i-1]}
			d = 0
		}
		cur = append(cur, points[i])
		d += step
	}
	if len(cur) > 1 {
		out = append(out, cur)
	}
	return out
}
