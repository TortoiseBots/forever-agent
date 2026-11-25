package driver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// The party gate of a player run (forever-agent #256): the leader does not leave a fight's stand, and does not walk a
// leg, while a living member of the party is left behind. A member beyond PlayRegroupYd is waited for, told to follow
// once, and fetched with a bounded walk back; a party still behind after those attempts is left to the walk's own
// straggler ladder (the follow, the summon, the stop), whose stop names who was behind and how far. A member down is the
// run's own regroup (WIPE) before any travel, so the healer's raise (or the corpse-run whispers) brings it back and the
// death is counted.
//
// The numbers are the lead's decisions on issue #256: PlayRegroupYd is well past PlayFollowYd, so a bot a corner behind
// on a normal walk is not a left-behind party and a normal run waits exactly as it did.

const (
	// PlayRegroupYd is how far a living member may stand from the leader before the party counts as left behind
	// (60 yd): a bot further than PlayFollowYd (35 yd) is waited for by the walk, and one further than this is not
	// following at all.
	PlayRegroupYd = 60.0
	// PlayRegroupWait is how long the leader stands and waits for a left-behind member before it re-sends the follow
	// order (20 s).
	PlayRegroupWait = 20 * time.Second
	// PlayRegroupFollowWait is how long it waits after the one follow order before it walks back, and after each walk
	// back before it counts the attempt (20 s).
	PlayRegroupFollowWait = 20 * time.Second
	// PlayRegroupBackYd is how far one walk back toward the party goes (15 yd): the leader closes that ground itself
	// instead of waiting out a bot that cannot path to it.
	PlayRegroupBackYd = 15.0
	// PlayRegroupTries bounds the walk backs of one party gate (2).
	PlayRegroupTries = 2
	// PlayRegroupBackTime bounds the whole walk back of one party gate (60 s).
	PlayRegroupBackTime = 60 * time.Second
)

// holdParty is the gate a walk passes before it starts and between its segments: it counts the party's deaths, holds
// the run while a living member is left behind (wait, one follow order, a bounded walk back), and hands a member's
// death to the run's regroup. It reports whether the walk may go on; a stop of the run's or the regroup's own state is
// set when it may not (p.stopReason, or p.stall for the death).
func (p *playRun) holdParty(ctx context.Context, why string) bool {
	if p.stopReason != "" {
		return false
	}
	p.refreshSelf()
	p.noteDeaths()
	if down := p.downBots(); len(down) > 0 {
		// #256 (ii): a member down is brought back before the walk goes on, through the run's own revive path (the
		// healer's raise out of combat, then the corpse-run whispers), and the death is in the count above.
		p.rec.Event(p.now(), "regroup_down", map[string]any{"why": why, "down": p.slots(down)})
		p.stall, p.resume = StateWipe, StateLegWalk
		return false
	}
	far := p.regroupFar()
	if len(far) == 0 {
		p.regroupSince = time.Time{}
		return true
	}
	if !p.regroupSince.IsZero() {
		// The same episode: this gate's attempts were spent already, and the walk's own straggler ladder (the follow at
		// FollowAt, the summon at SummonAt and the named stop at StopAt) carries it from here.
		return true
	}
	p.regroupSince = p.now()
	p.rec.Event(p.now(), "regroup", map[string]any{"why": why, "bots": p.slots(far), "yd": p.regroupYds(far)})
	if p.regroupWait(ctx, PlayRegroupWait) {
		p.regroupSince = time.Time{}
		p.rec.Event(p.now(), "regroup_join", map[string]any{"why": why})
		return true
	}
	if p.stall != "" {
		return false // a member fell during the wait: the regroup is next, not a follow order
	}
	// One follow order for the ones still behind, and one more bounded wait.
	far = p.regroupFar()
	for _, name := range far {
		if _, err := p.follow(ctx, name); p.fatal(err) {
			return false
		}
	}
	p.rec.Event(p.now(), "regroup_follow", map[string]any{"bots": p.slots(far)})
	if p.regroupWait(ctx, PlayRegroupFollowWait) {
		p.regroupSince = time.Time{}
		p.rec.Event(p.now(), "regroup_join", map[string]any{"why": why})
		return true
	}
	if p.stall != "" {
		return false
	}
	until := p.now().Add(PlayRegroupBackTime)
	for try := 0; try < PlayRegroupTries && p.now().Before(until); try++ {
		far = p.regroupFar()
		if len(far) == 0 {
			p.regroupSince = time.Time{}
			p.rec.Event(p.now(), "regroup_join", map[string]any{"why": why})
			return true
		}
		to, ok := p.backTarget(far)
		if !ok {
			// The party stands in the plan's avoid ground (the run's own rule says that ground kills): the leader does
			// not walk in after it, and the stop says where they are.
			p.rec.Event(p.now(), "regroup_refused", map[string]any{"bots": p.slots(far), "yd": p.regroupYds(far)})
			p.stopStraggler(far)
			return false
		}
		p.rec.Event(p.now(), "regroup_back", map[string]any{"x": to[0], "y": to[1], "z": to[2], "yd": dist3(p.self, to)})
		if !p.walkBack(ctx, to) {
			return false
		}
		if p.regroupWait(ctx, PlayRegroupFollowWait) {
			p.regroupSince = time.Time{}
			p.rec.Event(p.now(), "regroup_join", map[string]any{"why": why})
			return true
		}
		if p.stall != "" {
			return false
		}
	}
	if far = p.regroupFar(); len(far) == 0 {
		p.regroupSince = time.Time{}
		return true
	}
	// The attempts are spent and the party is still behind: the walk's ladder is the last arm, and its stop names who
	// was behind and how far (stopStraggler).
	p.rec.Event(p.now(), "regroup_giveup", map[string]any{"why": why, "bots": p.slots(far), "yd": p.regroupYds(far)})
	return true
}

// resumed is the state the run goes back to after a regroup's rest: the one a death interrupted (TRAVEL outside the
// instance), else the leg walk the run resumes at (#256).
func (p *playRun) resumed() string {
	if p.resume != "" {
		st := p.resume
		p.resume = ""
		return st
	}
	return StateLegWalk
}

// regroupFar is the living members of the party left further than PlayRegroupYd behind the leader on its map: a member
// down is not one (the regroup is the healer's job), and neither is one on another map (no walk back reaches it).
func (p *playRun) regroupFar() []string {
	roster := p.roster()
	var out []string
	for _, name := range p.cfg.Bots {
		m, ok := findMember(roster, name)
		if !ok || !m.Online || m.Dead || m.Ghost || m.Map != p.selfMap {
			continue
		}
		if dist3(m.Pos, p.self) > PlayRegroupYd {
			out = append(out, name)
		}
	}
	return out
}

// regroupYds is the distance of each of names from the leader, in whole yards, in the same order.
func (p *playRun) regroupYds(names []string) []int {
	roster := p.roster()
	out := make([]int, 0, len(names))
	for _, name := range names {
		if m, ok := findMember(roster, name); ok {
			out = append(out, int(math.Round(dist3(m.Pos, p.self))))
		}
	}
	return out
}

// regroupWait waits up to d for the party to come back, reporting whether it did. A member that falls during the wait
// is not a party that came back: the run's regroup is next (p.stall, p.resume) and the wait reports false, so the gate
// does not send its follow or walk on past the corpse. A stop of the run's ends it too.
func (p *playRun) regroupWait(ctx context.Context, d time.Duration) bool {
	until := p.now().Add(d)
	for {
		p.noteDeaths()
		if down := p.downBots(); len(down) > 0 {
			p.rec.Event(p.now(), "regroup_down", map[string]any{"why": "wait", "down": p.slots(down)})
			p.stall, p.resume = StateWipe, StateLegWalk
			return false
		}
		if len(p.regroupFar()) == 0 {
			return true
		}
		if p.timeUp() {
			p.stop("time")
			return false
		}
		if !p.now().Before(until) {
			return false
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return false
		}
	}
}

// backTarget is the point one walk back toward the party goes to: PlayRegroupBackYd along the line to where the party
// stands (their mean), and never into the plan's avoid ground. ok is false when that step would end there or on the way
// (the party itself stands in ground the run's rules say kills).
func (p *playRun) backTarget(far []string) (Vec3, bool) {
	roster := p.roster()
	var cx, cy, cz float64
	n := 0
	for _, name := range far {
		if m, ok := findMember(roster, name); ok {
			cx, cy, cz = cx+float64(m.Pos[0]), cy+float64(m.Pos[1]), cz+float64(m.Pos[2])
			n++
		}
	}
	if n == 0 {
		return p.self, false
	}
	centre := Vec3{float32(cx / float64(n)), float32(cy / float64(n)), float32(cz / float64(n))}
	d := dist3(p.self, centre)
	if d <= 0.01 {
		return p.self, false
	}
	step := math.Min(PlayRegroupBackYd, d)
	f := step / d
	to := Vec3{
		p.self[0] + float32(float64(centre[0]-p.self[0])*f),
		p.self[1] + float32(float64(centre[1]-p.self[1])*f),
		p.self[2] + float32(float64(centre[2]-p.self[2])*f),
	}
	mid := Vec3{
		p.self[0] + float32(float64(centre[0]-p.self[0])*f/2),
		p.self[1] + float32(float64(centre[1]-p.self[1])*f/2),
		p.self[2] + float32(float64(centre[2]-p.self[2])*f/2),
	}
	for _, at := range []Vec3{mid, to} {
		if _, bad := p.route.InAvoid(at); bad {
			return p.self, false
		}
	}
	return to, true
}

// walkBack walks the leader one bounded step back toward its party: a plain walk on ground the party stands on, with no
// party gate (this is the gate's own step) and no pack in the way (a pack met there is fought by the leg after the
// party is together). A map change, a lost link or a walk the walker refuses is the run's.
func (p *playRun) walkBack(ctx context.Context, to Vec3) bool {
	from := [3]float32{p.self[0], p.self[1], p.self[2]}
	end := [3]float32{to[0], to[1], to[2]}
	if err := p.ports.Walker.Walk(ctx, [][3]float32{from, end}); err != nil {
		switch {
		case errors.Is(err, ErrMapChange):
			p.stop("walk:mapchange")
		case errors.Is(err, ErrLinkLost):
			p.linkLost()
		default:
			p.stop("walk")
		}
		return false
	}
	p.refreshSelf()
	if _, bad := p.route.InAvoid(p.self); bad {
		p.stop("avoid")
		return false
	}
	return p.stopReason == ""
}

// stopStraggler ends the run naming who was behind and how far (forever-agent #256): `straggler:heal@97yd` for one,
// `straggler:heal@97yd,dps2@103yd` for several, nearest first.
func (p *playRun) stopStraggler(names []string) {
	if len(names) == 0 || p.stopReason != "" {
		return
	}
	roster := p.roster()
	type behind struct {
		slot string
		yd   int
	}
	rows := make([]behind, 0, len(names))
	for _, name := range names {
		m, ok := findMember(roster, name)
		if !ok {
			continue
		}
		rows = append(rows, behind{slot: p.slot(name), yd: int(math.Round(dist3(m.Pos, p.self)))})
	}
	if len(rows) == 0 {
		p.stop("straggler")
		return
	}
	slices.SortStableFunc(rows, func(a, b behind) int {
		if a.yd != b.yd {
			return a.yd - b.yd
		}
		return strings.Compare(a.slot, b.slot)
	})
	parts := make([]string, 0, len(rows))
	slots := make([]string, 0, len(rows))
	yds := make([]int, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s@%dyd", r.slot, r.yd))
		slots = append(slots, r.slot)
		yds = append(yds, r.yd)
	}
	p.rec.Event(p.now(), "straggler_stop", map[string]any{"bots": slots, "yd": yds})
	p.stop("straggler:" + strings.Join(parts, ","))
}
