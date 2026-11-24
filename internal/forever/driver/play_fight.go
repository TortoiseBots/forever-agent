package driver

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The player run's fight (round P2b): the pull that marks the pack's kill order and orders the tank, the fight the
// leader runs with his own character, the rest after it, and the boss's pull and its evidence. The numbers are the
// spec's §3.5-§3.9. Orders, not marks, drive the bots (D-4): a mark is set for the people watching the run, and every
// order is a `.bot attack` on the unit the agent has selected.

const (
	// PlayPullYd is how near the way a live pack unit stands before the run pulls it (20 yd): a pack in the way is
	// fought before the walk goes on.
	PlayPullYd = 20.0
	// PlayPullGatherYd is how near the agent every bot must stand before a pull is marked and ordered (10 yd): the
	// tank follows behind, so a pull taken before it is beside the agent can leave it out of the pack's sight.
	PlayPullGatherYd = 10.0
	// PlayPullStepYd is how far a retry pull steps toward the skull after a first pull no one answered (5 yd); the
	// step never ends nearer than PlayPullMinYd to the skull (10 yd), and an agent already inside that ring does not
	// move.
	PlayPullStepYd = 5.0
	PlayPullMinYd  = 10.0
	// PlayRestHP and PlayRestMana are REST's bars: every member's health at 80 per cent, and a mana user's pool at 70
	// (§3.8).
	PlayRestHP   = 80
	PlayRestMana = 70
	// PlayModuleSightYd is the range of the module's own "can the tank see its rti target" check: the 3D distance to the
	// marked unit must be within AiPlayerbot.SightDistance (60 yd; forever/conf/aiplayerbot.conf.dist:896) of the bot.
	// RtiTargetValue::CalculateMark (ai/playerbot/strategy/values/RtiTargetValue.h:89) calls
	// PossibleAttackTargetsValue::IsPossibleTarget with that distance (PossibleAttackTargetsValue.cpp:387,
	// IsWithinDistInMap), and then requires bot->IsWithinLOSInMap(unit) (RtiTargetValue.h:92). A mark that fails either
	// test makes AttackRTITargetAction whisper "I dont see my rti attack target" (AttackAction.cpp:163) and the tank
	// never moves. Both tests are made from the TANK's own position, not the agent's.
	PlayModuleSightYd = 60.0
	// PlaySightStepYd is how finely the close-in samples the navmesh path to the skull (2 yd): the first sample with a
	// clear ray is the stand it walks to. A straight line toward the skull can keep the party on a ledge whose lip
	// hides it (the trogg cave's shelf, proof 2026-11-23: four straight steps to 9.7 yd and still no sight), and the
	// path is where the party can walk.
	PlaySightStepYd = 2.0
	// PlaySightTouchYd is the nearest the close-in stands to the skull (5 yd): a sight stand nearer than a body's
	// reach is the skull's own feet, not a place to pull from.
	PlaySightTouchYd = 5.0
	// PlayTankBesideYd is how near the tank must stand to the sight stand before the sight is read from its own
	// position (5 yd): the module reads the line from the TANK, and a tank a ledge behind or a wall away sees nothing.
	PlayTankBesideYd = 5.0
	// PlayTankWait is how long one close-in stand waits for the tank to come beside it (15 s): a tank that cannot
	// follow is not waited for forever, and the pull is ordered from where the party stands (the module is the judge).
	PlayTankWait = 15 * time.Second
	// The close-in that puts the tank where that check passes is bounded two ways: at most PlayCloseTries steps and at
	// most PlayCloseTime from the first look.
	PlayCloseTries = 4
	PlayCloseTime  = 90 * time.Second
	// The tank's refusal (the module's whisper "I dont see my rti attack target") is the module's own verdict on
	// sight, whatever the agent's navmesh ray said (live 2026-11-24: the ray clear at 21 yd, the core's line of sight
	// not). Each refusal steps the party PlayPullStepYd along the navmesh path toward the skull (never nearer than
	// PlaySightTouchYd), waits for the tank and orders again (the order re-sends the mark); at most PlayRefuseTries
	// steps and PlayRefuseTime from the first refusal, then the pull stops pull-failed as before.
	PlayRefuseTries = 4
	PlayRefuseTime  = 90 * time.Second
)

// pull is the pack in the way: the agent first walks to the plan's stand with the party beside it, then the skull goes
// on the plan's kill order's entry in the pack (else on the unit nearest the agent), the cross on the nearest other
// one, the agent selects the skull and orders the tank onto it, and the run waits for the pack to turn on the party.
// A pull no one answers is tried once more from PlayPullStepYd nearer the skull and then stops the run.
func (p *playRun) pull(ctx context.Context) string {
	if !p.stranger.IsZero() {
		return p.pullStranger(ctx)
	}
	if p.bossRun {
		if leg := p.leg(); leg != nil && leg.Boss != nil {
			return p.pullBoss(ctx, leg.Boss)
		}
		p.bossRun = false
		return StateLegWalk
	}
	g, ok := p.route.GroupByID(p.pack)
	if !ok {
		p.pack = -1
		return StateLegWalk
	}
	p.refreshSelf()
	members := p.liveMembers(g)
	if len(members) == 0 {
		// Everything of the pack that was ever in view is down (or it was killed as an add).
		p.pulled[g.ID] = true
		p.pack = -1
		return StateLegWalk
	}
	p.rec.PullStart(p.now(), PullRecord{Leg: p.legName(), Group: g.ID, Entries: g.Entries, Mobs: len(members)})
	// A pack pull is made from the plan's stand with the party beside the agent: the tank follows behind, so a pull
	// taken wherever the leg walk stopped can leave it out of the pack's sight (proof-3 part A).
	if !p.pullApproach(ctx, g) {
		return ""
	}
	members = p.liveMembers(g)
	if len(members) == 0 {
		p.pulled[g.ID] = true
		p.pack = -1
		return StateLegWalk
	}
	// The module orders the tank onto the mark only when the tank itself sees it; from a stand it cannot see the skull
	// from, the agent closes in before the first order (and the tank follows).
	first, _ := p.skullAndCross(members)
	p.closeToSee(ctx, g.ID, first)
	if p.stopReason != "" {
		return ""
	}
	for attempt := range 2 {
		if attempt > 0 {
			p.rec.Event(p.now(), "pull_retry", map[string]any{"group": g.ID})
		}
		skull, cross := p.skullAndCross(members)
		if p.engageSeen(ctx, members, skull, cross) {
			return StateFight
		}
		if p.stopReason != "" {
			return ""
		}
		if p.refusedOut {
			break
		}
		members = p.liveMembers(g)
		if len(members) == 0 {
			p.pulled[g.ID] = true
			p.pack = -1
			return StateLegWalk
		}
		if attempt == 0 {
			if u, ok := unitByGUID(members, p.skull); ok && !p.pullStep(ctx, u) {
				return ""
			}
		}
	}
	p.stopPullFailed()
	return ""
}

// cannotAttackCode says whether the module's refusal code of an attack order means the unit cannot be attacked or is
// no longer there to be attacked: the codes the pull flow's refusal handling knows (dungeon.classify's dead,
// no-target and unreachable kinds, and the core's own not-attackable). Waiting or retrying does not change them. An
// evade is not one of them: the unit is attackable again in a second or two (evadeCode).
func cannotAttackCode(code string) bool {
	switch strings.ToLower(code) {
	case "not-attackable", "no-target", "dead-target", "dead", "unreachable":
		return true
	}
	return false
}

// evadeCode says whether the module's refusal code says the unit is evading: it is resetting to its path after a
// leash, which lasts a second or two, so the order is sent again (engage, within the pull's own window) or the unit
// is left alone for PlayEvadeWait and pulled again (a stranger: giveBackEvader).
func evadeCode(code string) bool {
	switch strings.ToLower(code) {
	case "evading", "evade":
		return true
	}
	return false
}

// PlayEvadeWait is how long a patrol candidate that answered `evading` is left alone before the hold may pull it
// again, and how long the pull's own window waits between attack orders the module refused as evading.
const PlayEvadeWait = 5 * time.Second

// maxStrangerPulls bounds the pulls of one stranger: a unit pulled twice that ends back on the way (an evade) is left
// behind on the third hold, so a mobile unknown cannot keep the leg from its boss.
const maxStrangerPulls = 2

// pullStranger pulls the unit the patrol hold gave up waiting on as its own pull: the plan's sight stand, the skull
// mark, the tank's order and the same two attempts from a step nearer as a pack's pull. The unit was only a patrol
// candidate, never a planned pull, so a pull that cannot be done does not end the run: the tank's refusal that the unit
// cannot be attacked (a friendly quest giver), the sight refused out, or two attempts no one answered give the unit up
// with the reason in the record (giveUpStranger) and the leg walks on to its next pull. A unit gone from the view by
// now is dropped the same way. A stop of the run's own (time, a lost link) stays the run's.
func (p *playRun) pullStranger(ctx context.Context) string {
	guid := p.stranger
	p.refreshSelf()
	u, ok := unitByGUID(p.ports.World.Units(), guid)
	if !ok || u.Dead || u.Friendly {
		p.rec.Event(p.now(), "patrol_gone", map[string]any{"guid": guid.String()})
		p.stranger = wire.GUID{}
		return StateLegWalk
	}
	members := []Unit{u}
	p.rec.PullStart(p.now(), PullRecord{Leg: p.legName(), Group: -1, Entries: []uint32{u.Entry}, Mobs: 1})
	p.closeToSee(ctx, -1, u)
	if p.stopReason != "" {
		return ""
	}
	reason := "no-answer"
	for attempt := range 2 {
		if attempt > 0 {
			p.rec.Event(p.now(), "pull_retry", map[string]any{"entry": u.Entry})
		}
		skull, ok := unitByGUID(p.ports.World.Units(), guid)
		if !ok || skull.Dead || skull.Friendly {
			p.stranger = wire.GUID{}
			return StateLegWalk
		}
		if p.engageSeen(ctx, members, skull, Unit{}) {
			p.stranger = wire.GUID{}
			return StateFight
		}
		if p.stopReason != "" {
			return ""
		}
		if evadeCode(p.orderCode) {
			return p.giveBackEvader(u)
		}
		if cannotAttackCode(p.orderCode) {
			reason = "order-refused:" + p.orderCode
			break
		}
		if p.refusedOut {
			reason = "refused"
			if p.sightBlocked {
				reason = "no-sight"
			}
			break
		}
		if attempt == 0 {
			if skull, ok := unitByGUID(p.ports.World.Units(), guid); ok && !p.pullStep(ctx, skull) {
				return ""
			}
		}
	}
	return p.giveUpStranger(u, reason)
}

// giveUpStranger lets a patrol candidate go: the reason is recorded, the unit is no candidate any more (the hold does
// not start on it again) and the leg walks on. It is bounded by the unit's own two pulls at most.
func (p *playRun) giveUpStranger(u Unit, reason string) string {
	p.rec.Event(p.now(), "patrol_unpullable", map[string]any{
		"guid": u.GUID.String(), "entry": u.Entry, "reason": reason,
		"x": u.Pos[0], "y": u.Pos[1], "z": u.Pos[2],
	})
	p.strangerGaveUp[u.GUID] = true
	p.stranger = wire.GUID{}
	return StateLegWalk
}

// giveBackEvader lets an evading patrol candidate be for now: it is not given up (an evade lasts a second or two,
// the unit is attackable again), only left alone for PlayEvadeWait so the same hold does not restart on it at once.
// The next waypoint inside its circle holds it again; maxStrangerPulls bounds the pulls, and endHold remembers the
// unit once they are spent.
func (p *playRun) giveBackEvader(u Unit) string {
	p.rec.Event(p.now(), "patrol_evade", map[string]any{
		"guid": u.GUID.String(), "entry": u.Entry, "pulls": p.strangerPulls[u.GUID],
		"retry_seconds": PlayEvadeWait.Seconds(),
	})
	p.strangerRetry[u.GUID] = p.now().Add(PlayEvadeWait)
	p.stranger = wire.GUID{}
	return StateLegWalk
}

// stopPullFailed ends a pull no one answered: "pull-no-sight" when the last look said the tank could not see the skull
// (the module's own refusal, closeToSee's bounds spent), else "pull-failed".
func (p *playRun) stopPullFailed() {
	if p.sightBlocked {
		p.stop("pull-no-sight")
		return
	}
	p.stop("pull-failed")
}

// skullSeen is the module's check for the tank (see PlayModuleSightYd): the skull within PlayModuleSightYd of where the
// tank stands, and in line of sight from there. The agent holds no collision geometry, so the line is the navmesh's
// walkability ray (the same stand-in plan-scout uses); known is false when there is no such port or the mesh cannot
// say, and the caller then lets the module judge. The tank's position is its live unit in the view (the roster lags a
// follow order by a poll or two); the roster's row stands in, and the agent's own position when the roster has none on
// the run's map (the tank follows beside the agent).
func (p *playRun) skullSeen(skull Unit) (seen, known bool) {
	from := p.self
	if at, ok := p.tankPos(); ok {
		from = at
	}
	if dist3(from, skull.Pos) > PlayModuleSightYd {
		return false, true
	}
	in, ok := p.sightAt(from, skull.Pos)
	if !ok {
		return true, false
	}
	return in, true
}

// closeToSee puts the tank where the module's check passes before the pull is ordered. While the skull is out of the
// tank's sight, the agent walks to the nearest point of the navmesh path to the skull that has a clear ray (the path
// goes down a shelf the straight line stays on top of; proof 2026-11-23), waits for the tank to come beside that stand
// and looks again from where the tank actually stands. With no path answer it steps toward the skull along
// pullStepPoint's line as before. It is bounded by PlayCloseTries and PlayCloseTime; spent, or with no step left to
// take, the pull is ordered as it stands (the module is the judge) and p.sightBlocked names the refusal if it comes. A
// stop of the run's own is the run's.
func (p *playRun) closeToSee(ctx context.Context, group int, skull Unit) {
	p.sightBlocked = false
	if skull.GUID.IsZero() {
		return
	}
	start := p.now()
	for try := 0; ; try++ {
		p.refreshSelf()
		u, ok := unitByGUID(p.ports.World.Units(), skull.GUID)
		if !ok {
			return
		}
		seen, known := p.skullSeen(u)
		p.rec.Event(p.now(), "pull_sight", map[string]any{
			"group": group, "seen": seen, "known": known, "try": try, "yd": dist3(p.self, u.Pos),
		})
		if !known || seen {
			return
		}
		p.sightBlocked = true
		if try >= PlayCloseTries || p.now().Sub(start) >= PlayCloseTime {
			return
		}
		// The agent's own stand is left alone when it has sight and the tank has not yet come beside it (it trails the
		// follow a step or two behind): the tank has only to catch up, and the next look reads the line from where it
		// then stands. A tank already beside the agent and still blind needs a stand further on instead.
		if in, ok := p.sightAt(p.self, u.Pos); ok && in && !p.tankNear(p.self, PlayTankBesideYd) {
			if !p.waitTankNear(ctx, p.self, PlayTankBesideYd) {
				return
			}
			p.sightBlocked = false
			continue
		}
		stand, ok := p.sightStand(u.Pos)
		if !ok {
			stand = pullStepPoint(p.self, u.Pos)
		}
		if dist3(p.self, stand) <= 0.01 {
			return // nothing nearer to try
		}
		p.pullWalk = true
		p.walkTo(ctx, stand, false, false)
		p.pullWalk = false
		if p.stopReason != "" {
			return
		}
		// The module reads the line from the TANK's own position: a stand that gives the agent sight gives the tank
		// none until it comes beside it. Bounded, and looked at again from where it stands.
		if !p.waitTankNear(ctx, stand, PlayTankBesideYd) {
			return
		}
		p.sightBlocked = false
	}
}

// sightAt is the Sight port's answer for one line, known false when it cannot say; with no port it never knows.
func (p *playRun) sightAt(from, to Vec3) (in, known bool) {
	if p.ports.Sight == nil {
		return false, false
	}
	return p.ports.Sight.InSight(from, to)
}

// sightStand is the nearest point of the navmesh path from the agent to the skull with a clear walkability ray to it,
// sampled every PlaySightStepYd and never nearer than PlaySightTouchYd: where the party can walk and the tank can
// then see the skull (the trogg cave's shelf is crossed only by the path down its west end). ok is false when there
// is no path, the mesh cannot answer or no sample clears, and the caller then steps along the straight line.
func (p *playRun) sightStand(skull Vec3) (Vec3, bool) {
	if p.ports.Sight == nil {
		return Vec3{}, false
	}
	p.refreshSelf()
	route, err := p.ports.Walker.Path(p.self, skull)
	if err != nil || len(route.Points) < 2 {
		return Vec3{}, false
	}
	for _, at := range sightSamples(route.Points, PlaySightStepYd) {
		if dist3(at, skull) < PlaySightTouchYd {
			return Vec3{}, false // the rest of the path stands on the skull
		}
		if dist3(at, p.self) < 1 {
			continue // the agent's own stand was looked at already
		}
		in, known := p.ports.Sight.InSight(at, skull)
		if known && in {
			return at, true
		}
	}
	return Vec3{}, false
}

// sightSamples is the path's points every yd or so, the first point included.
func sightSamples(points []Vec3, yd float64) []Vec3 {
	if len(points) == 0 || yd <= 0 {
		return nil
	}
	out := []Vec3{points[0]}
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		n := max(1, int(math.Ceil(dist3(a, b)/yd)))
		for k := 1; k <= n; k++ {
			f := float64(k) / float64(n)
			out = append(out, Vec3{
				a[0] + float32(float64(b[0]-a[0])*f),
				a[1] + float32(float64(b[1]-a[1])*f),
				a[2] + float32(float64(b[2]-a[2])*f),
			})
		}
	}
	return out
}

// tankNear says whether the tank's live position stands within yd of at.
func (p *playRun) tankNear(at Vec3, yd float64) bool {
	tank, ok := p.tankPos()
	return ok && dist3(tank, at) <= yd
}

// waitTankNear waits until the tank's live position stands within yd of at, up to PlayTankWait. It reports whether it
// did: a tank that cannot come beside the stand (a ledge it cannot follow down) is one bounded wait, and the pull is
// then ordered from where the party stands and the module judges.
func (p *playRun) waitTankNear(ctx context.Context, at Vec3, yd float64) bool {
	deadline := p.now().Add(PlayTankWait)
	for {
		if p.tankNear(at, yd) {
			return true
		}
		if !p.now().Before(deadline) {
			return false
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return false
		}
	}
}

// pullApproach walks the agent to the plan's stand for the group (unless it already stands within PlayArriveYd of it),
// records `pull_stand`, and waits for the party at PlayPullGatherYd: the pull is marked and ordered only then.
func (p *playRun) pullApproach(ctx context.Context, g Group) bool {
	stand := g.Stand(p.self)
	if dist3(p.self, stand) > PlayArriveYd {
		// No nested pull and no patrol hold on the way to the stand: the walk is the pull's own approach.
		p.pullWalk = true
		p.walkTo(ctx, stand, false, false)
		p.pullWalk = false
		if p.stopReason != "" {
			return false
		}
	}
	p.rec.Event(p.now(), "pull_stand", map[string]any{
		"group": g.ID, "x": stand[0], "y": stand[1], "z": stand[2], "yd": dist3(stand, g.Centre),
	})
	return p.waitNear(ctx, PlayPullGatherYd, p.lim.FollowAt, p.lim.SummonAt, p.lim.StopAt)
}

// pullStep moves the agent closer after a pull no one answered: the walk goes to PlayPullStepYd along the line to the
// skull, never nearer than PlayPullMinYd to it, records `pull_step`, and gathers the party again before the retry.
func (p *playRun) pullStep(ctx context.Context, skull Unit) bool {
	p.refreshSelf()
	to := pullStepPoint(p.self, skull.Pos)
	if d := dist3(p.self, to); d > 0.01 {
		p.rec.Event(p.now(), "pull_step", map[string]any{"x": to[0], "y": to[1], "z": to[2], "yd": d})
		p.pullWalk = true
		p.walkTo(ctx, to, false, false)
		p.pullWalk = false
		if p.stopReason != "" {
			return false
		}
	}
	return p.waitNear(ctx, PlayPullGatherYd, p.lim.FollowAt, p.lim.SummonAt, p.lim.StopAt)
}

// pullStepPoint is the point a retry pull steps to: PlayPullStepYd from `from` toward the skull, and no nearer than
// PlayPullMinYd to the skull; `from` itself when it already stands inside that ring.
func pullStepPoint(from, skull Vec3) Vec3 {
	d := dist3(from, skull)
	if d <= PlayPullMinYd {
		return from
	}
	step := math.Min(PlayPullStepYd, d-PlayPullMinYd)
	if step <= 0 {
		return from
	}
	f := step / d
	return Vec3{
		from[0] + float32(float64(skull[0]-from[0])*f),
		from[1] + float32(float64(skull[1]-from[1])*f),
		from[2] + float32(float64(skull[2]-from[2])*f),
	}
}

// pullBoss is the boss of the leg: the pull's orders with the boss as the skull and the plan's entries to kill before
// it as its adds. The party is gathered within the pull's distance and rests to its bars before the boss is marked and
// ordered (the boss walk keeps the bots at PlayFollowYd), and a retry reads the pull again: an add that died in the
// wait is no longer its skull.
func (p *playRun) pullBoss(ctx context.Context, b *Boss) string {
	p.refreshSelf()
	u, ok := p.bossUnit(b.Entry)
	if !ok {
		p.stop("bosses-alive")
		return ""
	}
	p.bossGUID = u.GUID
	// The party first: the boss walk pauses only past PlayFollowYd, and a boss pulled with the tank 35 yd behind is
	// the tank's pull to lose.
	if !p.waitNear(ctx, PlayPullGatherYd, p.lim.FollowAt, p.lim.SummonAt, p.lim.StopAt) {
		return ""
	}
	// And the bars: the rest's own cap, no more (a party still short goes on, as rest's timeout does).
	if !p.ready() && !p.restWait(ctx) {
		return ""
	}
	members := p.bossMembers(b, u)
	p.rec.PullStart(p.now(), PullRecord{Leg: p.legName(), Group: -1, Entries: []uint32{b.Entry}, Boss: true, Mobs: len(members)})
	// The plan's entries to kill before the boss are the skull first, in its order; the boss is the skull once they are
	// down, and the cross goes on the next unit of the order.
	skull, cross := p.bossOrder(b, members)
	p.closeToSee(ctx, -1, skull)
	if p.stopReason != "" {
		return ""
	}
	for attempt := range 2 {
		if attempt > 0 {
			p.rec.Event(p.now(), "pull_retry", map[string]any{"entry": b.Entry})
			bu, ok := p.bossUnit(b.Entry)
			if !ok {
				// The boss is off the view for a moment: a missing snapshot is not its death, so the retry walks
				// toward its last known position and looks again for a pull's window before the leg's walk judges it.
				bu, ok = p.reacquireBoss(ctx, b)
			}
			if !ok {
				// Gone for good: dead (its kill is latched) or gone. The leg's walk judges it.
				p.bossRun = false
				return StateLegWalk
			}
			p.bossGUID = bu.GUID
			members = p.bossMembers(b, bu)
			skull, cross = p.bossOrder(b, members)
			if skull.GUID.IsZero() {
				p.bossRun = false
				return StateLegWalk
			}
		}
		if p.engageSeen(ctx, members, skull, cross) {
			return StateFight
		}
		if p.stopReason != "" {
			return ""
		}
		if p.refusedOut {
			break
		}
	}
	p.stopPullFailed()
	return ""
}

// reacquireBoss looks for the leg's boss again after a missing snapshot: the party steps toward the boss's plan
// position, at most a retry's step each poll, and re-reads the view within a pull's own window. A unit that never comes
// back is no pull's: ok false sends the run to the leg's walk, which has its own wait and then the stop.
func (p *playRun) reacquireBoss(ctx context.Context, b *Boss) (Unit, bool) {
	deadline := p.now().Add(p.lim.Pull)
	for {
		if u, ok := p.bossUnit(b.Entry); ok {
			return u, true
		}
		if p.timeUp() {
			p.stop("time")
			return Unit{}, false
		}
		if !p.now().Before(deadline) {
			return Unit{}, false
		}
		p.refreshSelf()
		if to := pullStepPoint(p.self, b.Pos); dist3(p.self, to) > 0 {
			p.walkTo(ctx, to, false, false)
			if p.stopReason != "" {
				return Unit{}, false
			}
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return Unit{}, false
		}
	}
}

// bossMembers is the boss pull's set: the boss and the plan's First entries that are living in the view.
func (p *playRun) bossMembers(b *Boss, boss Unit) []Unit {
	members := []Unit{boss}
	for _, x := range p.ports.World.Units() {
		if x.Dead || x.Friendly || x.GUID == boss.GUID || !slices.Contains(b.First, x.Entry) {
			continue
		}
		members = append(members, x)
	}
	return members
}

// bossOrder is the boss fight's kill order over the living units of the pull: the leg's First entries in the plan's
// order, then the boss. It returns the first as the skull and the second as the cross.
func (p *playRun) bossOrder(b *Boss, units []Unit) (Unit, Unit) {
	var order []Unit
	for _, e := range b.First {
		for _, u := range units {
			if u.Entry == e && !u.Dead && !u.Friendly && !slices.ContainsFunc(order, func(o Unit) bool { return o.GUID == u.GUID }) {
				order = append(order, u)
			}
		}
	}
	for _, u := range units {
		if u.Entry == b.Entry && !u.Dead && !u.Friendly {
			order = append(order, u)
			break
		}
	}
	for _, u := range units {
		if !u.Dead && !u.Friendly && !slices.ContainsFunc(order, func(o Unit) bool { return o.GUID == u.GUID }) && slices.Contains(p.pullSet, u.GUID) {
			order = append(order, u)
		}
	}
	switch len(order) {
	case 0:
		return Unit{}, Unit{}
	case 1:
		return order[0], Unit{}
	}
	return order[0], order[1]
}

// engage is one pull: the two marks, the agent's selection of the skull and `.bot attack <Tank>`, then the wait for the
// pack to turn on the party (the skull on the tank, or any of the pack on a member). It reports false when the wait ran
// out, true as soon as the pack is fighting; a stop of its own is the run's.
func (p *playRun) engage(ctx context.Context, members []Unit, skull, cross Unit) bool {
	if !p.mark(ctx, skull.GUID, party.MarkSkull) || !p.mark(ctx, cross.GUID, party.MarkCross) {
		return false
	}
	p.skull = skull.GUID
	p.pullSet = p.pullSet[:0]
	for _, u := range members {
		p.pullSet = append(p.pullSet, u.GUID)
	}
	p.refused = false
	p.orderCode = ""
	before := p.sightRefusals()
	if !p.order(ctx, p.cfg.Bots[0], skull.GUID) {
		return false
	}
	if !p.stranger.IsZero() && cannotAttackCode(p.orderCode) {
		// The held unit may not be attacked: waiting out the pull window would not change the module's answer.
		return false
	}
	// An evade is not that answer: the unit is attackable again in a second or two, so the order goes again a
	// PlayEvadeWait later while the pull's window lasts (bounded orders, not a stop and not a give-up).
	orderAgain := time.Time{}
	if evadeCode(p.orderCode) {
		orderAgain = p.now().Add(PlayEvadeWait)
	}
	deadline := p.now().Add(p.lim.Pull)
	// seenDead is the pull's kills this order has observed: a unit seen dead stays dead for the pull even when its
	// corpse despawns before the next snapshot.
	seenDead := map[wire.GUID]bool{}
	for {
		units := p.ports.World.Units()
		for _, g := range p.pullSet {
			if u, ok := unitByGUID(units, g); ok && u.Dead {
				seenDead[g] = true
			}
		}
		// A pull whose set is seen dead before it ever turned on the party is answered: a one-shot add and a mob killed
		// by a dot never aggro, and waiting out the window would only fail a won pull. A unit merely missing from the
		// view was never seen dead, so it does not clear the pull (the review of 2026-11-24): the wait goes on and the
		// boss is pulled for real.
		if p.pullCleared(units, seenDead) {
			return true
		}
		if p.aggro() {
			return true
		}
		if !orderAgain.IsZero() && !p.now().Before(orderAgain) {
			p.rec.Event(p.now(), "order_again", map[string]any{"target": skull.GUID.String(), "code": p.orderCode})
			if !p.order(ctx, p.cfg.Bots[0], skull.GUID) {
				return false
			}
			orderAgain = time.Time{}
			if evadeCode(p.orderCode) {
				orderAgain = p.now().Add(PlayEvadeWait)
			}
		}
		// The module's whisper is its verdict: the tank found no rti target. Waiting out the window would not change
		// it (the tank stands still), so the caller reacts now.
		if p.sightRefusals() > before {
			p.refused = true
			tankYd := -1.0
			if at, ok := p.tankPos(); ok {
				tankYd = dist3(at, skull.Pos)
			}
			p.rec.Event(p.now(), "pull_refused", map[string]any{
				"target": skull.GUID.String(), "yd": dist3(p.self, skull.Pos), "tank_yd": tankYd,
			})
			return false
		}
		if p.timeUp() {
			p.stop("time")
			return false
		}
		if !p.now().Before(deadline) {
			return false
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return false
		}
	}
}

// sightRefusals is the tank's refusal count so far, 0 with no Refusals port.
func (p *playRun) sightRefusals() int {
	if p.ports.Refusals == nil || len(p.cfg.Bots) == 0 {
		return 0
	}
	return p.ports.Refusals.SightRefusals(p.cfg.Bots[0])
}

// engageSeen is engage plus the reaction to the tank's refusal. A refused order means the module did not find the
// skull from where the tank stands, whatever the agent's own check said: the party steps toward the skull along the
// navmesh path (refusalStand), the agent waits (bounded) for the tank to come beside the new stand, and engage marks
// and orders again. Bounded by PlayRefuseTries steps and PlayRefuseTime from the first refusal; spent, or with no step
// left to take, refusedOut is set and the caller stops the pull. A pull that is not refused (no answer at all) is
// engage's as before.
func (p *playRun) engageSeen(ctx context.Context, members []Unit, skull, cross Unit) bool {
	p.refusedOut = false
	var first time.Time
	for try := 0; ; try++ {
		if p.engage(ctx, members, skull, cross) {
			return true
		}
		if p.stopReason != "" || !p.refused {
			return false
		}
		if first.IsZero() {
			first = p.now()
		}
		p.refreshSelf()
		u, ok := unitByGUID(p.ports.World.Units(), skull.GUID)
		if !ok || u.Dead {
			return false
		}
		if try >= PlayRefuseTries || p.now().Sub(first) >= PlayRefuseTime {
			p.refusedOut = true
			return false
		}
		stand := p.refusalStand(u.Pos)
		d := dist3(p.self, stand)
		if d <= 0.01 {
			p.refusedOut = true // nothing nearer to try
			return false
		}
		p.rec.Event(p.now(), "pull_step", map[string]any{"x": stand[0], "y": stand[1], "z": stand[2], "yd": d, "refused": true})
		p.pullWalk = true
		p.walkTo(ctx, stand, false, false)
		p.pullWalk = false
		if p.stopReason != "" {
			return false
		}
		// The module reads the line from the TANK: the next order waits (bounded) for it to come beside the stand,
		// and goes out regardless (the module is the judge).
		p.waitTankNear(ctx, stand, PlayTankBesideYd)
		if p.stopReason != "" {
			return false
		}
		skull = u
	}
}

// refusalStand is where the party steps after a refusal. The module reads the line from the TANK, so the step is
// measured from where the tank stands: PlayPullStepYd along the navmesh path from the tank to the skull (the way the
// party can walk, round a lip or a pillar), never nearer than PlaySightTouchYd to the skull; the straight line when
// the mesh gives no path, and the agent's own position when the tank's is unknown. An agent already on the skull's
// feet steps onto the mob itself: what a blind tank that trails needs then is the party closer, not a stand further
// on (a stand measured from the agent there was the agent's own position, and one refusal ended the pull).
func (p *playRun) refusalStand(skull Vec3) Vec3 {
	p.refreshSelf()
	if dist3(p.self, skull) <= PlaySightTouchYd+0.01 {
		return skull
	}
	from := p.self
	if at, ok := p.tankPos(); ok {
		from = at
	}
	d := dist3(from, skull)
	if d-PlaySightTouchYd <= 0.01 {
		return from
	}
	step := math.Min(PlayPullStepYd, d-PlaySightTouchYd)
	if route, err := p.ports.Walker.Path(from, skull); err == nil && len(route.Points) >= 2 {
		return alongPath(route.Points, step, skull, PlaySightTouchYd)
	}
	f := step / d
	return Vec3{
		from[0] + float32(float64(skull[0]-from[0])*f),
		from[1] + float32(float64(skull[1]-from[1])*f),
		from[2] + float32(float64(skull[2]-from[2])*f),
	}
}

// alongPath is the point yd along the polyline from its first point, cut short where the path would first come
// within floor of skull: the last point of the path when it is shorter.
func alongPath(points []Vec3, yd float64, skull Vec3, floor float64) Vec3 {
	at := points[0]
	left := yd
	for i := 1; i < len(points) && left > 0; i++ {
		a, b := points[i-1], points[i]
		seg := dist3(a, b)
		if seg <= 0 {
			continue
		}
		n := max(1, int(math.Ceil(seg/0.5)))
		part := seg / float64(n)
		for k := 1; k <= n; k++ {
			next := lerpVec(a, b, float64(k)/float64(n))
			if dist3(next, skull) < floor {
				return at
			}
			if part >= left {
				return lerpVec(at, next, left/part)
			}
			left -= part
			at = next
		}
	}
	return at
}

// lerpVec is the point f of the way from a to b.
func lerpVec(a, b Vec3, f float64) Vec3 {
	return Vec3{
		a[0] + float32(float64(b[0]-a[0])*f),
		a[1] + float32(float64(b[1]-a[1])*f),
		a[2] + float32(float64(b[2]-a[2])*f),
	}
}

// fight runs one fight: the damage dealers are ordered onto the skull once the pack is on the tank, the agent swings
// himself 1.5 s later, a dead skull moves to the cross and the cross to the next living unit (with both damage dealers
// re-ordered), the tank is ordered back onto a skull that took the pack off it, and the fight ends when no living unit
// has targeted a party member for the quiet window. The fight's cap stops the run.
func (p *playRun) fight(ctx context.Context) string {
	p.refreshSelf()
	p.inFight = true
	defer func() { p.inFight = false }()
	start := p.now()
	deadline := start.Add(p.lim.Fight)
	agentAt := start.Add(p.lim.AgentHit)
	swinging := false
	var calm, broke, idle time.Time
	adds := 0
	// A fight resumed after the character's death has lost its selection, and the module's attack takes the requester's
	// selected unit: the skull is selected again before either order.
	if !p.skull.IsZero() {
		if err := p.ports.Player.Select(p.skull); err != nil {
			p.playerErr(err, "select")
			return ""
		}
	}
	if !p.order(ctx, p.cfg.Bots[2], p.skull) || !p.order(ctx, p.cfg.Bots[3], p.skull) {
		return ""
	}
	for {
		if p.stopReason != "" || p.lost {
			return ""
		}
		now := p.now()
		if swinging {
			if err := p.ports.Player.FightTick(ctx, p.skull); err != nil {
				if errors.Is(err, ErrLinkLost) {
					p.linkLost()
				} else {
					p.stop("fight")
				}
				return ""
			}
		}
		units := p.ports.World.Units()
		p.sample(units)
		p.noteDeaths()
		p.latchBoss()
		if p.wiped() {
			return p.endFight(ctx, adds, units)
		}
		if p.ports.Player.Phase() != PhaseAlive {
			p.deadFrom = StateFight
			return StateDeadWait
		}
		live, extra := p.engaged(units)
		adds = max(adds, extra)
		switch {
		case live > 0:
			calm, idle = time.Time{}, time.Time{}
		case !p.pullAlive(units):
			idle = time.Time{}
			if calm.IsZero() {
				calm = now
			}
			if now.Sub(calm) >= p.lim.FightQuiet {
				return p.endFight(ctx, adds, units)
			}
		default:
			// Nothing of the pull is on the party and one of its units still stands: a member that never aggroed (a
			// pillar between it and the party) or one that evaded would hold the fight open to its cap. A unit gets the
			// pull's own window (counted from the last order that named it) to turn on the party: a boss that waits for
			// the tank to walk to it is not idle. Past it the unit is dropped from the pull, so the fight can end; it is
			// alive, so its pack is not counted fought and the run pulls the pack again.
			calm = time.Time{}
			if idle.IsZero() {
				idle = now
			}
			if now.Sub(idle) >= max(p.lim.FightQuiet, p.lim.Pull) {
				p.dropIdle(units)
				idle = time.Time{}
			}
		}
		if !now.Before(deadline) {
			p.stop("fight-timeout")
			return ""
		}
		if p.skullGone(units) {
			if next, cross := p.nextSkull(units); !next.GUID.IsZero() {
				p.skull = next.GUID
				if !p.mark(ctx, next.GUID, party.MarkSkull) || !p.mark(ctx, cross.GUID, party.MarkCross) {
					return ""
				}
				// The tank goes with them: a pack member that never came at the party (behind a pillar) is the one
				// still up, and no TankBreak order will come for a skull nothing targets.
				if !p.order(ctx, p.cfg.Bots[0], p.skull) || !p.order(ctx, p.cfg.Bots[2], p.skull) || !p.order(ctx, p.cfg.Bots[3], p.skull) {
					return ""
				}
				if swinging {
					if err := p.ports.Player.Attack(p.skull); err != nil {
						p.stop("attack")
						return ""
					}
				}
				broke, idle = time.Time{}, time.Time{}
			}
		}
		if u, ok := unitByGUID(units, p.skull); ok && !u.Dead && !u.Target.IsZero() && p.partyMember(u.Target) && u.Target != p.tankGUID() {
			if broke.IsZero() {
				broke = now
			}
			if now.Sub(broke) >= p.lim.TankBreak {
				if !p.order(ctx, p.cfg.Bots[0], p.skull) {
					return ""
				}
				broke = time.Time{}
			}
		} else {
			broke = time.Time{}
		}
		if !swinging && !now.Before(agentAt) {
			if err := p.ports.Player.Attack(p.skull); err != nil {
				if errors.Is(err, ErrLinkLost) {
					p.linkLost()
				} else {
					p.stop("attack")
				}
				return ""
			}
			swinging = true
		}
		if !p.sleep(ctx, p.lim.FightPoll) {
			return ""
		}
	}
}

// endFight closes a fight: the agent stops swinging, the instance's encounters are read, the pull's record is written
// and every bot is told to follow again. A boss fight whose boss the evidence does not count dead stops the run.
func (p *playRun) endFight(ctx context.Context, adds int, units []Unit) string {
	_ = p.ports.Player.StopAttack()
	p.drainEncounters()
	// The boss's kill is read before a wipe or a bot's corpse can take the run off the fight: the view is the last
	// one that saw it.
	p.latchBoss()
	p.rec.UpdatePull(func(r *PullRecord) { r.Adds = adds })
	p.rec.PullEnd(p.now(), 0)
	p.noteDeaths()
	boss := p.bossRun
	down := p.pullDown(units)
	p.bossRun = false
	p.skull, p.pullSet, p.pullGone = wire.GUID{}, p.pullSet[:0], p.pullGone[:0]
	// Nobody walks on while a member is down (§3.10): the whole party is the budget's wipe, the character's own death
	// takes the resurrect wait, and a bot's corpse alone is a regroup that costs no wipe.
	switch {
	case p.wiped():
		p.pack = -1
		return StateWipe
	case p.ports.Player.Phase() != PhaseAlive:
		p.deadFrom = ""
		p.pack = -1
		return StateDeadWait
	case len(p.downBots()) > 0:
		p.pack = -1
		return StateWipe
	}
	for _, name := range p.cfg.Bots {
		if _, err := p.follow(ctx, name); p.fatal(err) {
			return ""
		}
	}
	if boss {
		entry := uint32(0)
		if leg := p.leg(); leg != nil && leg.Boss != nil {
			entry = leg.Boss.Entry
		}
		if !p.bossDone(entry) {
			p.stop("bosses-alive")
			return ""
		}
		return StateLoot
	}
	if down && p.pack >= 0 {
		p.pulled[p.pack] = true
	}
	p.pack = -1
	return StateLoot
}

// rest sits the party down after a fight: the run goes on once every member's health and every mana user's pool are
// back over the spec's bars, or the rest's cap passes (the bots eat and drink on their own; the run does not wait for
// ever, and a party that is still short goes on regardless).
func (p *playRun) rest(ctx context.Context) string {
	started := p.now()
	deadline := started.Add(p.lim.Rest)
	p.rec.Event(started, "rest", map[string]any{"leg": p.legName()})
	if err := p.ports.Player.Rest(ctx, p.lim.Rest); err != nil {
		if errors.Is(err, ErrLinkLost) {
			p.linkLost()
		} else {
			p.stop("rest")
		}
		return ""
	}
	for {
		if p.ready() {
			p.rec.Event(p.now(), "rest_end", map[string]any{"seconds": p.now().Sub(started).Seconds()})
			return StateLegWalk
		}
		if p.timeUp() {
			p.stop("time")
			return ""
		}
		if !p.now().Before(deadline) {
			p.rec.Event(p.now(), "rest_timeout", map[string]any{"seconds": p.now().Sub(started).Seconds()})
			return StateLegWalk
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return ""
		}
	}
}

// restWait waits the rest's cap for the party's bars before a boss pull, the agent sitting as REST does: the run does
// not open a boss fight on a half-empty party. A timeout does not open that gate (the review of 2026-11-24): the party
// is ordered to rest again and given a second window of the same length; if it is still short, the boss is pulled only
// when the gate's floors hold (the healer at BossHealMana of its pool and the tank at BossTankHP of its health), and the
// run stops `boss-not-ready` otherwise. Every event names the members still under their bars and their values, so the
// real cause is visible. It reports false only when the run itself ended or stopped at the gate.
func (p *playRun) restWait(ctx context.Context) bool {
	short := []map[string]any(nil)
	for window := 0; window < 2; window++ {
		started := p.now()
		ev := "boss_rest"
		if window > 0 {
			ev = "boss_rest_again"
		}
		p.rec.Event(started, ev, map[string]any{"leg": p.legName()})
		if err := p.ports.Player.Rest(ctx, p.lim.Rest); err != nil {
			if errors.Is(err, ErrLinkLost) {
				p.linkLost()
			} else {
				p.stop("rest")
			}
			return false
		}
		deadline := started.Add(p.lim.Rest)
		for {
			if p.ready() {
				p.rec.Event(p.now(), "boss_rest_end", map[string]any{"seconds": p.now().Sub(started).Seconds(), "window": window + 1})
				return true
			}
			if p.timeUp() {
				p.stop("time")
				return false
			}
			if !p.now().Before(deadline) {
				break
			}
			if !p.sleep(ctx, p.lim.Poll) {
				return false
			}
		}
		short = p.shortBars()
		p.rec.Event(p.now(), "boss_rest_timeout", map[string]any{"seconds": p.now().Sub(started).Seconds(), "window": window + 1, "short": short})
	}
	// Both windows spent: the gate's floors, or the run does not pull.
	gate := map[string]any{
		"healer_mana": p.gatePct(p.cfg.Bots[1], true),
		"tank_hp":     p.gatePct(p.cfg.Bots[0], false),
		"short":       short,
	}
	if deficient := p.bossGateShort(); len(deficient) > 0 {
		gate["gate"] = deficient
		p.rec.Event(p.now(), "boss_not_ready", gate)
		p.stop("boss-not-ready")
		return false
	}
	p.rec.Event(p.now(), "boss_pull_min_bars", gate)
	return true
}

// shortBars names the party's members under the rest's bars, with their values: the pre-boss gate's own account of a
// party that did not come back to its bars.
func (p *playRun) shortBars() []map[string]any {
	out := []map[string]any{}
	for _, m := range p.roster() {
		if m.Dead || m.Ghost {
			continue // a corpse's bars say nothing about whether the party may go on
		}
		if m.MaxHP > 0 && m.HP*100 < m.MaxHP*PlayRestHP {
			out = append(out, p.barRow(m, "hp", PlayRestHP, m.HP, m.MaxHP))
		}
		if m.PowerType == 0 && m.MaxPower > 0 && m.Power*100 < m.MaxPower*PlayRestMana {
			out = append(out, p.barRow(m, "mana", PlayRestMana, m.Power, m.MaxPower))
		}
	}
	return out
}

// bossGateShort names the members under the gate's floors: the tank below BossTankHP of its health and the healer below
// BossHealMana of its pool. A dead, ghost or unknown member is skipped, as ready skips it: a corpse is no short bar.
func (p *playRun) bossGateShort() []map[string]any {
	out := []map[string]any{}
	if m, ok := p.member(p.cfg.Bots[0]); ok && !m.Dead && !m.Ghost {
		if m.MaxHP > 0 && m.HP*100 < m.MaxHP*p.lim.BossTankHP {
			out = append(out, p.barRow(m, "hp", p.lim.BossTankHP, m.HP, m.MaxHP))
		}
	}
	if m, ok := p.member(p.cfg.Bots[1]); ok && !m.Dead && !m.Ghost {
		if m.PowerType == 0 && m.MaxPower > 0 && m.Power*100 < m.MaxPower*p.lim.BossHealMana {
			out = append(out, p.barRow(m, "mana", p.lim.BossHealMana, m.Power, m.MaxPower))
		}
	}
	return out
}

// barRow is one member's short bar for the gate's events: who (its slot, never its name), the bar, its value and
// maximum, the bar the party needs, and the value per cent.
func (p *playRun) barRow(m PartyMember, bar string, need, value, max int) map[string]any {
	row := map[string]any{"who": p.slot(m.Name), "bar": bar, "value": value, "max": max, "need": need}
	if max > 0 {
		row["pct"] = value * 100 / max
	}
	return row
}

// gatePct is one gate reading in per cent: a bot's mana (mana) or health. A bot with no name, one the roster does not
// hold, a dead one or one without such a bar answers -1.
func (p *playRun) gatePct(name string, mana bool) int {
	m, ok := p.member(name)
	if name == "" || !ok || m.Dead || m.Ghost {
		return -1
	}
	if mana {
		if m.PowerType != 0 || m.MaxPower <= 0 {
			return -1
		}
		return m.Power * 100 / m.MaxPower
	}
	if m.MaxHP <= 0 {
		return -1
	}
	return m.HP * 100 / m.MaxHP
}

// boss is the leg's boss: the run walks to it (the plan's approach points first), waits for a patrolling boss that the
// plan says waits, and pulls it. A boss the view never shows is not dead, so the run stops (bosses-alive).
func (p *playRun) boss(ctx context.Context) string {
	leg := p.leg()
	if leg == nil || leg.Boss == nil {
		return StateLegWalk
	}
	b := leg.Boss
	p.refreshSelf()
	if p.bossDone(b.Entry) {
		return StateLegWalk
	}
	for _, wp := range append(append([]Vec3(nil), b.Approach...), b.Pos) {
		p.walkTo(ctx, wp, false, false)
		if p.stopReason != "" {
			return ""
		}
		if p.pack >= 0 || !p.stranger.IsZero() {
			return StatePull // a pack, or a hostile the plan does not know, in the way to the boss is fought first
		}
	}
	// A plan that says the boss waits 0 s means "it is there": the spawn's update can still trail the walk, so the
	// look is given a pull's window rather than a single check.
	wait := time.Duration(b.WaitS) * time.Second
	if wait <= 0 {
		wait = p.lim.Pull
	}
	deadline := p.now().Add(wait)
	for {
		if u, ok := p.bossUnit(b.Entry); ok {
			// The pull begins here: the encounter starts the boss fight opens are this boss's (drainEncounters).
			p.bossRun = true
			p.bossGUID = u.GUID
			p.bossSince = p.now()
			clear(p.bossEnc)
			return StatePull
		}
		if !p.now().Before(deadline) {
			p.stop("bosses-alive")
			return ""
		}
		if p.timeUp() {
			p.stop("time")
			return ""
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return ""
		}
	}
}

// --- the fight's small helpers ---

// liveMembers is the pack's living units in view: MatchPacks' claim, plus the group's live patrol members within
// PlayPullYd of the agent. A patrol walks out of the pack's claim (and its spawn leaves the pack's radius), so a group
// met only by its patrol would otherwise be marked pulled without a fight.
func (p *playRun) liveMembers(g Group) []Unit {
	units := p.ports.World.Units()
	claim := p.claim(units)
	var out []Unit
	seen := map[wire.GUID]bool{}
	add := func(u Unit) {
		if u.Dead || u.Friendly || seen[u.GUID] {
			return
		}
		seen[u.GUID] = true
		out = append(out, u)
	}
	for _, ui := range claim.Members(g.ID) {
		add(units[ui])
	}
	if g.Patrol != nil {
		for _, u := range units {
			if !slices.Contains(g.Patrol.Entries, u.Entry) {
				continue
			}
			if dist3(u.Pos, p.self) <= PlayPullYd {
				add(u)
			}
		}
	}
	return out
}

// claim matches the view's units to the route's groups.
func (p *playRun) claim(units []Unit) packClaim {
	groups := p.route.AllGroups()
	return MatchPacks(units, groups, len(groups))
}

// skullAndCross is the pull's marks: the skull on the first live unit of the plan's kill order that is in the pack,
// else on the unit nearest the agent; the cross on the nearest other unit. A pack of one unit has no cross.
func (p *playRun) skullAndCross(members []Unit) (Unit, Unit) {
	near := append([]Unit(nil), members...)
	self := p.self
	slices.SortStableFunc(near, func(a, b Unit) int {
		da, db := dist3(a.Pos, self), dist3(b.Pos, self)
		switch {
		case da < db:
			return -1
		case da > db:
			return 1
		}
		return 0
	})
	skull := near[0]
	for _, e := range p.route.KillFirst {
		if i := slices.IndexFunc(near, func(u Unit) bool { return u.Entry == e }); i >= 0 {
			skull = near[i]
			break
		}
	}
	var cross Unit
	for _, u := range near {
		if u.GUID != skull.GUID {
			cross = u
			break
		}
	}
	return skull, cross
}

// nextSkull is the fight's next target once the skull is down: the nearest living unit of the pull (which is the old
// cross), and as the new cross the next nearest after it.
func (p *playRun) nextSkull(units []Unit) (Unit, Unit) {
	if p.bossRun {
		if leg := p.leg(); leg != nil && leg.Boss != nil {
			var pool []Unit
			for _, u := range units {
				if !u.Dead && !u.Friendly && (slices.Contains(p.pullSet, u.GUID) || u.Entry == leg.Boss.Entry) {
					pool = append(pool, u)
				}
			}
			return p.bossOrder(leg.Boss, pool)
		}
	}
	live := make([]Unit, 0, len(p.pullSet))
	for _, u := range units {
		if u.Dead || u.Friendly || !slices.Contains(p.pullSet, u.GUID) {
			continue
		}
		live = append(live, u)
	}
	if len(live) == 0 {
		return Unit{}, Unit{}
	}
	self := p.self
	slices.SortStableFunc(live, func(a, b Unit) int {
		da, db := dist3(a.Pos, self), dist3(b.Pos, self)
		switch {
		case da < db:
			return -1
		case da > db:
			return 1
		}
		return 0
	})
	if len(live) > 1 && live[0].GUID == p.skull {
		live = live[1:]
	}
	var cross Unit
	if len(live) > 1 {
		cross = live[1]
	}
	return live[0], cross
}

// skullGone says whether the fight's skull is down or left the view.
func (p *playRun) skullGone(units []Unit) bool {
	u, ok := unitByGUID(units, p.skull)
	return !ok || u.Dead
}

// engaged counts the fight's living units that target a party member, and how many of them the pull did not bring (the
// adds).
func (p *playRun) engaged(units []Unit) (int, int) {
	live, extra := 0, 0
	for _, u := range units {
		if u.Dead || u.Friendly || !p.partyMember(u.Target) {
			continue
		}
		live++
		if !slices.Contains(p.pullSet, u.GUID) {
			extra++
		}
	}
	return live, extra
}

// aggro says whether the pulled pack has turned on the party.
func (p *playRun) aggro() bool {
	for _, u := range p.ports.World.Units() {
		if u.Dead || u.Friendly || !slices.Contains(p.pullSet, u.GUID) {
			continue
		}
		if p.partyMember(u.Target) {
			return true
		}
	}
	return false
}

// partyMember says whether a guid is this character's or one of the party's.
func (p *playRun) partyMember(g wire.GUID) bool {
	if g.IsZero() {
		return false
	}
	for _, m := range p.roster() {
		if m.GUID == g {
			return true
		}
	}
	return false
}

// tankGUID is the tank's guid.
func (p *playRun) tankGUID() wire.GUID {
	if m, ok := p.member(p.cfg.Bots[0]); ok {
		return m.GUID
	}
	return wire.GUID{}
}

// tankPos is the tank's live position: its unit in the view the packet loop keeps (the roster lags a follow order by a
// poll or two, and the close-in judges the line from where the tank really stands), else the roster's row on the run's
// map. ok is false when neither knows it.
func (p *playRun) tankPos() (Vec3, bool) {
	if len(p.cfg.Bots) == 0 {
		return Vec3{}, false
	}
	if g := p.tankGUID(); !g.IsZero() {
		for _, u := range p.ports.World.Units() {
			if u.GUID == g && !u.Dead {
				return u.Pos, true
			}
		}
	}
	if m, ok := p.member(p.cfg.Bots[0]); ok && m.Map == p.selfMap {
		return m.Pos, true
	}
	return Vec3{}, false
}

// ready says whether the party is rested: every member's health at PlayRestHP per cent, and every mana user's pool at
// PlayRestMana (§3.8).
func (p *playRun) ready() bool {
	for _, m := range p.roster() {
		if m.Dead || m.Ghost {
			continue // a corpse's bars say nothing about whether the party may go on
		}
		if m.MaxHP > 0 && m.HP*100 < m.MaxHP*PlayRestHP {
			return false
		}
		if m.PowerType == 0 && m.MaxPower > 0 && m.Power*100 < m.MaxPower*PlayRestMana {
			return false
		}
	}
	return true
}

// bossUnit is the leg boss's living unit in view.
func (p *playRun) bossUnit(entry uint32) (Unit, bool) {
	for _, u := range p.ports.World.Units() {
		if u.Entry == entry && !u.Dead && !u.Friendly {
			return u, true
		}
	}
	return Unit{}, false
}

// drainEncounters reads the instance's encounter events: an end with success and a boss's kill credit both kill the
// leg's boss, so the clear is judged from the fight and not only from the last view (§3.9). A start names the
// encounter the boss fight opened (bossEnc, from the pull's own time on), and a kill's id is compared with those: a
// kill of another encounter (another fight of the instance, an earlier leg's) is not this leg's boss.
func (p *playRun) drainEncounters() {
	for _, ev := range p.ports.World.Encounters() {
		if ev.Kind == "start" {
			if ev.EncounterID != 0 {
				p.encStarted[ev.EncounterID] = true
				if p.bossRun && !ev.At.Before(p.bossSince) {
					p.bossEnc[ev.EncounterID] = true
				}
			}
			continue
		}
		if (ev.Kind != "end" || !ev.Success) && ev.Kind != "boss_kill" {
			continue
		}
		leg := p.leg()
		if leg == nil || leg.Boss == nil {
			continue
		}
		if p.bossKilled[leg.Boss.Entry] {
			continue
		}
		if !p.killIsThisBoss(ev) {
			p.rec.Event(p.now(), "boss_kill_other", map[string]any{"encounter": ev.EncounterID, "kind": ev.Kind})
			continue
		}
		p.bossKilled[leg.Boss.Entry] = true
		p.rec.Event(p.now(), "boss_kill", map[string]any{"entry": leg.Boss.Entry, "encounter": ev.EncounterID, "kind": ev.Kind})
	}
}

// killIsThisBoss says whether an encounter end or kill credit is the current leg's boss fight's: the id it carries is
// one this boss's pull started (bossEnc). An event with no id, or a run that saw no encounter start at all (the
// credit is its only evidence), is this boss's: there is nothing to compare it with.
func (p *playRun) killIsThisBoss(ev EncounterEvent) bool {
	if ev.EncounterID == 0 {
		return true
	}
	if len(p.bossEnc) > 0 {
		return p.bossEnc[ev.EncounterID]
	}
	return len(p.encStarted) == 0
}

// bossDone says whether a plan boss is dead: an encounter's end, a kill credit, or its unit's dead flag. A unit seen
// dead latches the kill: a corpse that despawns (or leaves the view) before the run's verdict, and a bot's death
// during the kill, cannot turn a killed boss back into a live one.
func (p *playRun) bossDone(entry uint32) bool {
	if entry == 0 {
		return false
	}
	if p.bossKilled[entry] {
		return true
	}
	if !bossDead(p.ports.World.Units(), entry) {
		return false
	}
	p.bossKilled[entry] = true
	p.rec.Event(p.now(), "boss_seen_dead", map[string]any{"entry": entry})
	return true
}

// latchBoss latches the running boss fight's kill the moment its unit is seen dead: the fight's own evidence is read
// before a wipe, a despawn or the next view can lose it.
func (p *playRun) latchBoss() {
	if !p.bossRun {
		return
	}
	leg := p.leg()
	if leg == nil || leg.Boss == nil {
		return
	}
	p.bossDone(leg.Boss.Entry)
}

// mark sets one raid target icon; a group command the server does not confirm stops the run (D-7).
func (p *playRun) mark(ctx context.Context, target wire.GUID, m party.Mark) bool {
	if target.IsZero() {
		return true
	}
	if err := p.ports.Group.Mark(target, m); err != nil {
		if errors.Is(err, ErrLinkLost) {
			p.linkLost()
		} else {
			p.stop("mark")
		}
		return false
	}
	p.rec.Event(p.now(), "raid_mark", map[string]any{"mark": party.MarkName(m), "target": target.String()})
	return true
}

// order selects a unit and orders a bot onto it. The module's attack takes the requester's selection, so the selection
// goes first, always. A refused order is logged and the run goes on: the fight's own evidence says whether it worked.
func (p *playRun) order(ctx context.Context, bot string, target wire.GUID) bool {
	if target.IsZero() {
		return true
	}
	p.orderCode = ""
	if err := p.ports.Player.Select(target); err != nil {
		if errors.Is(err, ErrLinkLost) {
			p.linkLost()
		} else {
			p.stop("select")
		}
		return false
	}
	rep, err := p.attack(ctx, bot)
	if err == nil {
		return true
	}
	if errors.Is(err, botcmd.ErrRefused) {
		p.orderCode = rep.Code
		p.rec.Event(p.now(), "order_refused", map[string]any{"bot": p.slot(bot), "code": rep.Code})
		return true
	}
	return !p.fatal(err)
}

// attack sends `.bot attack` and asks the module.
func (p *playRun) attack(ctx context.Context, bot string) (botcmd.Reply, error) {
	if err := p.sendBot(botcmd.VerbAttack, bot); err != nil {
		return botcmd.Reply{}, err
	}
	return p.ports.Owner.Attack(ctx, bot)
}

// bossKey is a boss' key in the run's record.
func bossKey(entry uint32) string { return strconv.FormatUint(uint64(entry), 10) }

// pullAlive says whether a unit of the pull is still alive in the view: a unit that stopped targeting the party (an
// evade, a reset) is not a dead one, so it keeps the fight open and the skull moves to it.
func (p *playRun) pullAlive(units []Unit) bool {
	for _, u := range units {
		if !u.Dead && !u.Friendly && slices.Contains(p.pullSet, u.GUID) {
			return true
		}
	}
	return false
}

// dropIdle drops the pull's living units that are not on the party from the fight's set, and remembers them: they keep
// the fight open (an evade, a member that never aggroed), and after the quiet window they are the fight's leftovers,
// not its mobs. The unit stays alive and unconfirmed, so its pack is not counted fought and the run pulls it again.
func (p *playRun) dropIdle(units []Unit) {
	keep := p.pullSet[:0]
	for _, g := range p.pullSet {
		u, ok := unitByGUID(units, g)
		if !ok || u.Dead || u.Friendly || p.partyMember(u.Target) {
			keep = append(keep, g)
			continue
		}
		p.pullGone = append(p.pullGone, g)
		p.rec.Event(p.now(), "pull_drop", map[string]any{"guid": g.String(), "entry": u.Entry})
	}
	p.pullSet = keep
}

// pullCleared says whether every unit of the pull is confirmed dead: seen dead in a snapshot since this pull's order
// (seen), or, for the leg's boss, latched dead by the kill evidence the run already reads. A pull whose set was killed
// before it turned on the party was answered by that, and a retry would order a corpse. A unit missing from the view
// that was never seen dead is UNKNOWN, not dead: a delayed or incomplete snapshot, a range transition or a unit that
// vanishes for a moment does not clear a pull, and the pull goes on toward it.
func (p *playRun) pullCleared(units []Unit, seen map[wire.GUID]bool) bool {
	for _, g := range p.pullSet {
		if seen[g] {
			continue
		}
		if u, ok := unitByGUID(units, g); ok {
			if !u.Dead {
				return false
			}
			continue
		}
		if !p.bossLatched(g) {
			return false
		}
	}
	return true
}

// bossLatched says whether the guid is the boss this pull sees and the run's kill evidence has latched it dead (an
// encounter's end, a kill credit, its corpse seen): the one absence a boss pull may be cleared by.
func (p *playRun) bossLatched(g wire.GUID) bool {
	if !p.bossRun || g.IsZero() || g != p.bossGUID {
		return false
	}
	leg := p.leg()
	if leg == nil || leg.Boss == nil {
		return false
	}
	return p.bossDone(leg.Boss.Entry)
}

// pullDown says whether every unit of the pull is confirmed dead in the view. A unit that left the view is not
// confirmed: its pack is not marked fought, so the run pulls it again when it shows. A unit the fight dropped as out
// of combat is not confirmed either: it is still alive.
func (p *playRun) pullDown(units []Unit) bool {
	for _, g := range p.pullSet {
		u, ok := unitByGUID(units, g)
		if !ok || !u.Dead {
			return false
		}
	}
	for _, g := range p.pullGone {
		if u, ok := unitByGUID(units, g); !ok || !u.Dead {
			return false
		}
	}
	return true
}
