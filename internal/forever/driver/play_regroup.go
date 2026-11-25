package driver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// The party gate of a player run (forever-agent #256): the leader does not leave a fight's stand, does not walk a leg,
// and does not gather a pull, while a living member of the party is left behind. A member beyond PlayRegroupYd is waited
// for, told to follow once, and fetched with a bounded walk back on the walk's own rules (the navmesh, the pack
// clearances, the fight); a party still behind after those attempts is left to the walk's own straggler ladder (the
// follow, the summon, the stop), whose stop names who was behind and how far. A member down is the run's own regroup
// (WIPE) before any travel, so the healer's raise (or the corpse-run whispers) brings it back and the death is counted.
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
	// PlayRegroupLimit is how many regroups one pull may cost before the run stops naming it (2): a death that keeps
	// taking the party out of the same pull is ground the plan does not mark (the live run's environmental damage at a
	// pull stand), and re-entering that pull is not a run that can finish.
	PlayRegroupLimit = 2
	// PlayRegroupClearYd is how much nearer a walk back's point may come to an uncleared pack than the leader already
	// stands (0.5 yd): the plan's own pull stand sits beside its pack, so a step away from it is clear and only a step
	// toward the pack is refused.
	PlayRegroupClearYd = 0.5
	// PlayRegroupDownPolls is how many consecutive polls (at different instants) must read a member missing from the
	// roster, or dead, before the gate counts it down (2): a roster row that drops out for one poll is a flicker, not a
	// death, and must not cost a regroup.
	PlayRegroupDownPolls = 2
)

// holdParty is the gate a walk passes before it starts, between its segments, and at a pull's gather: it counts the
// party's deaths, holds the run while a living member is left behind (wait, one follow order, a bounded walk back), and
// hands a member's death to the run's regroup. It reports whether the walk may go on; a stop of the run's or the
// regroup's own state is set when it may not (p.stopReason, or p.stall for the death or for the fight the party is in).
func (p *playRun) holdParty(ctx context.Context, why string) bool {
	if p.stopReason != "" {
		return false
	}
	p.refreshSelf()
	if !p.leaderAlive() {
		return false
	}
	p.noteDeaths()
	if down := p.partyDown(ctx); len(down) > 0 {
		// #256 (ii): a member down is brought back before the walk goes on, through the run's own revive path (the
		// healer's raise out of combat, then the corpse-run whispers), and the death is in the count above.
		p.stallRegroup(why, down)
		return false
	}
	return p.holdGate(ctx, why)
}

// holdGate is the gate's hold on a living party left behind, after the deaths are read: wait, one follow order, a
// bounded walk back, and the ladder's own arm when the attempts are spent.
func (p *playRun) holdGate(ctx context.Context, why string) bool {
	p.refreshSelf()
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
	p.rec.Event(p.now(), "regroup_hold", map[string]any{"why": why, "bots": p.slots(far), "yd": p.regroupYds(far)})
	if p.regroupWait(ctx, PlayRegroupWait) {
		p.regroupSince = time.Time{}
		p.rec.Event(p.now(), "regroup_join", map[string]any{"why": why})
		return true
	}
	if p.stall != "" || p.stopReason != "" {
		// A member fell during the wait (the regroup is next), or the party is fighting (FIGHT is), or the run stopped.
		return false
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
	if p.stall != "" || p.stopReason != "" {
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
		switch p.walkBack(ctx, to, until) {
		case walkBackStopped:
			return false
		case walkBackFight:
			// The party is in a fight: the leader joins it (FIGHT) instead of standing beside it (#256).
			p.stallFight()
			return false
		case walkBackBlocked:
			// The way back runs through an uncleared pack or past a patrol's radius: the step is not taken, and the
			// walk's own ladder is the last arm.
			p.rec.Event(p.now(), "regroup_blocked", map[string]any{"bots": p.slots(far), "yd": p.regroupYds(far)})
			until = time.Time{}
			continue
		case walkBackTimeout:
			// The gate's walk-back time is spent: the walk's own ladder is the last arm, and its stop names who was
			// behind.
			p.rec.Event(p.now(), "regroup_timeout", map[string]any{"bots": p.slots(far), "yd": p.regroupYds(far)})
			until = time.Time{}
			continue
		}
		if p.regroupWait(ctx, PlayRegroupFollowWait) {
			p.regroupSince = time.Time{}
			p.rec.Event(p.now(), "regroup_join", map[string]any{"why": why})
			return true
		}
		if p.stall != "" || p.stopReason != "" {
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

// stallRegroup hands a walk's or a wait's member-down to the run's own regroup: the state goes to WIPE, the resume is
// what the death interrupted (TRAVEL outside the instance, else the leg walk that re-enters the pull or the boss it was
// gathering for), and a pull that keeps costing regroups stops the run naming it (#256).
func (p *playRun) stallRegroup(why string, down []string) {
	key := p.regroupKey()
	p.rec.Event(p.now(), "regroup_down", map[string]any{"why": why, "down": p.slots(down), "key": key})
	p.downPolls = nil // the next down member is read afresh
	p.resume = StateLegWalk
	if p.selfMap != p.route.Map {
		p.resume = StateTravel
	}
	p.stall = StateWipe
	if !p.regroupAllowed(key) {
		p.stall = ""
	}
}

// partyDown is the party gate's own reading of the members that are down: a bot missing from the roster, or dead or a
// ghost, on PlayRegroupDownPolls consecutive polls. A member read down once is read again after a poll's wait, so a
// roster row that drops out for one poll is no death and costs no regroup, while a real fall is found one poll later at
// most. The recovery's own waits read downBots directly.
func (p *playRun) partyDown(ctx context.Context) []string {
	var down []string
	for range PlayRegroupDownPolls {
		var pending bool
		down, pending = p.readDown()
		if !pending {
			return down
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return down
		}
	}
	return down
}

// readDown is one poll of the gate's reading: the members confirmed down (read so on PlayRegroupDownPolls consecutive
// polls) and whether some member is down but not yet confirmed. Two reads at one instant are one poll, and a member read
// up again starts its count afresh.
func (p *playRun) readDown() (confirmed []string, pending bool) {
	down := p.downBots()
	now := p.now()
	fresh := now.After(p.downPollAt)
	next := make(map[string]int, len(down))
	for _, name := range down {
		key := strings.ToLower(name)
		n := p.downPolls[key]
		if fresh || n == 0 {
			n++
		}
		next[key] = n
		if n >= PlayRegroupDownPolls {
			confirmed = append(confirmed, name)
		} else {
			pending = true
		}
	}
	p.downPolls = next
	if fresh || len(down) == 0 {
		p.downPollAt = now
	}
	return confirmed, pending
}

// abortPullOrders takes a pull's orders back when a regroup ends the pull before a fight: every bot is told to follow,
// so a tank still holding its attack order comes back instead of pulling the pack into the recovery, and the skull and
// cross marks are cleared, so the party is not left marked for a fight nobody is fighting. It acts only while a pull's
// record is open; a regroup that starts outside a pull has no orders to take back. Best effort: a refused order is the
// module's answer, and a lost link ends the run through the same fatal path as any other order.
func (p *playRun) abortPullOrders(ctx context.Context) {
	if p.rec.CurrentPull() == nil {
		return
	}
	for _, name := range p.members() {
		if _, err := p.follow(ctx, name); errors.Is(err, ErrLinkLost) {
			p.linkLost()
			return
		}
	}
	if p.ports.Group == nil {
		return
	}
	for _, m := range []party.Mark{party.MarkSkull, party.MarkCross} {
		if err := p.ports.Group.Mark(wire.EmptyGUID, m); err != nil {
			if errors.Is(err, ErrLinkLost) {
				p.linkLost()
				return
			}
			continue
		}
		p.rec.Event(p.now(), "raid_mark_clear", map[string]any{"mark": party.MarkName(m)})
	}
}

// stallFight hands the run to the fight its party is already in: the chief's FIGHT state, with the pull in hand or, when
// the fight is not the run's own, armed from the view (joinFight), and the leg walk the regroup goes back to after it. It
// reports whether the run goes to FIGHT: no fight is entered with an empty unit set (a combat flag with no hostile on
// the party), and nothing is stalled then.
func (p *playRun) stallFight() bool {
	if p.selfMap != p.route.Map {
		return false // outside the instance the plan's fight rules do not hold; the gate's own waits carry it
	}
	if !p.joinFight() {
		return false
	}
	p.stall, p.resume = StateFight, StateLegWalk
	return true
}

// joinFight arms the fight a regroup walks into: a hostile already on the party is the pull's set (and the one on the
// tank, else the nearest, its skull), so a fight the leader was not the one to start is ordered and watched like its
// own. A pull already in hand whose units are on the party keeps its own set, skull and pack. It reports false when
// there is nothing to fight (no hostile on the party and no pull in hand): the run does not enter FIGHT then.
//
// The pack of a joined fight is the plan group that owns the skull's unit, or none: p.pack is set to it (-1 when no
// group owns the units), so the fight's end marks only the group that was fought, never the pull that was in hand when
// the party turned out to be fighting another pack. The fight writes its own pull record (`joined`) for that group,
// after the record of a pull in hand that it replaces is closed (`pull_end` with `aborted: joined`); a fight no group
// owns writes none.
func (p *playRun) joinFight() bool {
	units := p.ports.World.Units()
	// A pull in hand whose own units are on the party is the fight's already: its set and skull stay. Only a pull
	// whose record is open is the fight's record: a set left from a pull that a regroup aborted has no record to
	// close, so it falls through and the joined fight writes its own (`joined`) below.
	if p.rec.CurrentPull() != nil && len(p.pullSet) > 0 && !p.skull.IsZero() {
		for _, u := range units {
			if !u.Dead && !u.Friendly && slices.Contains(p.pullSet, u.GUID) && p.partyMember(u.Target) {
				return true
			}
		}
	}
	set := make([]wire.GUID, 0, len(units))
	best, bestD := Unit{}, math.Inf(1)
	tank := p.tankGUID()
	var onTank Unit
	for _, u := range units {
		if u.Dead || u.Friendly || !p.partyMember(u.Target) {
			continue
		}
		set = append(set, u.GUID)
		if !tank.IsZero() && u.Target == tank {
			onTank = u
		}
		if d := dist3(u.Pos, p.self); d < bestD {
			best, bestD = u, d
		}
	}
	if len(set) == 0 {
		return false
	}
	p.pullSet = set
	p.skull = best.GUID
	if !onTank.GUID.IsZero() {
		p.skull = onTank.GUID
	}
	p.pullGone = p.pullGone[:0]
	owner := -1
	claim := p.claim(units)
	for ui := range units {
		if units[ui].GUID == p.skull {
			if g, ok := claim.GroupOf(ui); ok {
				owner = g
			}
			break
		}
	}
	if p.bossRun {
		if leg := p.leg(); leg == nil || leg.Boss == nil || !p.setHasEntry(units, leg.Boss.Entry) {
			p.bossRun = false // the fight is not the boss's: its end must not read the boss as the pull
		}
	}
	p.rec.PullAbort(p.now(), "joined")
	p.pack = owner
	p.rec.Event(p.now(), "fight_join", map[string]any{"skull": p.skull.String(), "units": len(set), "group": owner})
	if g, ok := p.route.GroupByID(owner); ok && owner >= 0 {
		p.rec.PullStart(p.now(), PullRecord{Leg: p.legName(), Group: owner, Entries: g.Entries, Mobs: len(set), Joined: true})
	}
	return true
}

// setHasEntry says whether a unit of the pull's set has the entry.
func (p *playRun) setHasEntry(units []Unit, entry uint32) bool {
	for _, u := range units {
		if u.Entry == entry && slices.Contains(p.pullSet, u.GUID) {
			return true
		}
	}
	return false
}

// regroupAllowed counts a regroup against the place it happened at (key, see regroupKey) and stops the run once that
// place has had its PlayRegroupLimit already: `regroup-limit:group-<id>`, `regroup-limit:boss-<entry>`,
// `regroup-limit:stranger-<entry>`, `regroup-limit:walk-<leg>-<via>`, `regroup-limit:step-<leg>-<step>` or
// `regroup-limit:travel` names the place the run will not re-enter.
func (p *playRun) regroupAllowed(key string) bool {
	if p.regroups == nil {
		p.regroups = map[string]int{}
	}
	p.regroups[key]++
	if p.regroups[key] <= PlayRegroupLimit {
		return true
	}
	p.stop("regroup-limit:" + key)
	return false
}

// regroupKey names where a regroup happened: the pull in hand, else the place the walk is at, so a death that repeats at
// one place is capped wherever it is.
func (p *playRun) regroupKey() string {
	if key := p.pullKey(); key != "" {
		return key
	}
	if p.selfMap != p.route.Map {
		return "travel"
	}
	leg := p.leg()
	if leg == nil {
		return "walk-end"
	}
	if points := len(leg.Via) + 1; p.walkAt > points {
		return "step-" + strconv.Itoa(p.legIdx) + "-" + strconv.Itoa(p.stepIdx)
	}
	return "walk-" + strconv.Itoa(p.legIdx) + "-" + strconv.Itoa(p.walkAt)
}

// pullKey names the pull in hand: `group-<id>`, `boss-<entry>`, `stranger-<entry>`, or "" between pulls.
func (p *playRun) pullKey() string {
	switch {
	case !p.stranger.IsZero():
		if u, ok := unitByGUID(p.ports.World.Units(), p.stranger); ok {
			return "stranger-" + strconv.FormatUint(uint64(u.Entry), 10)
		}
		return "stranger"
	case p.pack >= 0:
		return "group-" + strconv.Itoa(p.pack)
	case p.bossRun:
		if leg := p.leg(); leg != nil && leg.Boss != nil {
			return "boss-" + strconv.FormatUint(uint64(leg.Boss.Entry), 10)
		}
	}
	return ""
}

// leaderAlive says whether the character still stands: the gate's waits stop the run when the leader falls, because no
// wait or walk can put it back (the character's own death is DEAD_WAIT's, entered from a fight it can see).
func (p *playRun) leaderAlive() bool {
	if p.ports.Player.Phase() == PhaseAlive {
		return true
	}
	p.stop("not-alive")
	return false
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
	for _, name := range p.members() {
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
// does not send its follow or walk on past the corpse. A party found fighting is the fight's: the leader joins it
// (FIGHT) instead of standing out of it (#256). A stop of the run's ends the wait too.
func (p *playRun) regroupWait(ctx context.Context, d time.Duration) bool {
	until := p.now().Add(d)
	for {
		if !p.leaderAlive() {
			return false
		}
		p.noteDeaths()
		if down := p.partyDown(ctx); len(down) > 0 {
			p.stallRegroup("wait", down)
			return false
		}
		if p.selfMap == p.route.Map && p.inCombat() {
			if p.stallFight() {
				return false
			}
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

// walkBackEnd is how a walk back ended.
type walkBackEnd int

const (
	// walkBackArrived is the step's end reached.
	walkBackArrived walkBackEnd = iota
	// walkBackTimeout is the gate's or the run's own time spent on the step.
	walkBackTimeout
	// walkBackBlocked is a way back through an uncleared pack or past a patrol-radius hostile, or no complete route to
	// the party: the step is not taken.
	walkBackBlocked
	// walkBackFight is a party already fighting: the run's FIGHT is next.
	walkBackFight
	// walkBackStopped is a stop of the run's own.
	walkBackStopped
)

// walkBack walks the leader one bounded step back toward its party on the walk's own rules: the navmesh route cut into
// segments of PlaySegmentYd, out of the plan's avoid ground, clear of every uncleared pack's radius and every
// patrol-radius hostile the plan does not name, under the earlier of the gate's deadline (until) and the run's own
// budget — a blocked walker is cancelled there, never waited out. It skips the party gate (this is the gate's own step)
// and a party found fighting hands the run to FIGHT at once (#256).
func (p *playRun) walkBack(ctx context.Context, to Vec3, until time.Time) walkBackEnd {
	if p.timeUp() {
		p.stop("time")
		return walkBackStopped
	}
	p.refreshSelf()
	if dist3(p.self, to) <= PlayArriveYd {
		return walkBackArrived
	}
	route, err := p.ports.Walker.Path(p.self, to)
	if err != nil {
		switch {
		case errors.Is(err, ErrMapChange):
			p.stop("walk:mapchange")
		case errors.Is(err, ErrLinkLost):
			p.linkLost()
		default:
			// No route to the party is no reason to end the run: the step is not taken and the walk's own ladder
			// (follow, summon, the named straggler stop) carries it.
			return walkBackBlocked
		}
		return walkBackStopped
	}
	if route.Kind != navmesh.RouteComplete || len(route.Points) < 2 {
		return walkBackBlocked
	}
	// The gate's clock is the run's own (virtual under the fake) and a context deadline is wall time: the remaining
	// duration, not the deadline instant, is what a blocked walker is given (agentWalkPath's rule).
	left := until.Sub(p.now())
	if end := p.runEnd(); !end.IsZero() {
		if l := end.Sub(p.now()); l < left {
			left = l
		}
	}
	if left <= 0 {
		return walkBackTimeout
	}
	wctx, cancel := context.WithTimeout(ctx, left)
	defer cancel()
	for _, seg := range SplitRoute(route.Points, PlaySegmentYd) {
		if p.timeUp() {
			p.stop("time")
			return walkBackStopped
		}
		if !p.now().Before(until) {
			return walkBackTimeout
		}
		if !p.walkClear(seg) {
			return walkBackBlocked
		}
		if p.selfMap == p.route.Map && p.inCombat() {
			return walkBackFight
		}
		if err := p.ports.Walker.Walk(wctx, seg); err != nil {
			switch {
			case errors.Is(err, ErrMapChange):
				p.stop("walk:mapchange")
			case errors.Is(err, ErrLinkLost):
				p.linkLost()
			case errors.Is(wctx.Err(), context.DeadlineExceeded):
				return walkBackTimeout
			case wctx.Err() != nil:
				p.stop("interrupt")
			default:
				p.stop("walk")
			}
			return walkBackStopped
		}
		p.refreshSelf()
		if _, bad := p.route.InAvoid(p.self); bad {
			p.stop("avoid")
			return walkBackStopped
		}
		if p.selfMap == p.route.Map && p.inCombat() {
			return walkBackFight
		}
	}
	return walkBackArrived
}

// walkClear says whether a walk back's segment stays out of the ground the walk's own rules refuse: the plan's avoid
// boxes, every uncleared plan pack's radius (a point nearer than the leader already stands, PlayRegroupClearYd of
// slack) and every patrol-radius hostile the plan does not name. The pull's own stand sits beside its pack, so a step
// away is clear and only a step toward a pack is refused (#256).
func (p *playRun) walkClear(seg [][3]float32) bool {
	pts := make([]Vec3, len(seg))
	for i := range seg {
		pts[i] = Vec3(seg[i])
		if _, bad := p.route.InAvoid(pts[i]); bad {
			return false
		}
	}
	units := p.ports.World.Units()
	groups := p.route.AllGroups()
	claim := MatchPacks(units, groups, len(groups))
	for i := range groups {
		g := &groups[i]
		if p.pulled[g.ID] {
			continue
		}
		at := append([]Vec3(nil), g.Positions...)
		for _, ui := range claim.Members(g.ID) {
			if u := units[ui]; !u.Dead && !u.Friendly {
				at = append(at, u.Pos)
			}
		}
		if g.Patrol != nil {
			for _, u := range units {
				if !u.Dead && !u.Friendly && slices.Contains(g.Patrol.Entries, u.Entry) {
					at = append(at, u.Pos)
				}
			}
		}
		if p.nearerTo(at, pts) {
			return false
		}
	}
	for ui, u := range units {
		if u.Dead || u.Friendly || p.partyOwned(u) || p.strangerGaveUp[u.GUID] {
			continue
		}
		if g, ok := claim.GroupOf(ui); ok && !p.pulled[g] {
			continue // its pack's radius is the check above
		}
		if p.planNames(u.Entry) || p.skipListed(u.Entry) || u.Flags&UnitNotAttackable != 0 || p.notHostile(u) {
			continue
		}
		if distPolyline(u.Pos, pts) > PlayPatrolYd {
			continue
		}
		if p.nearerTo([]Vec3{u.Pos}, pts) {
			return false
		}
	}
	return true
}

// nearerTo says whether a point of at comes closer to a point of seg than the leader already stands to at, by more
// than PlayRegroupClearYd: the step toward ground the run keeps out of.
func (p *playRun) nearerTo(at, seg []Vec3) bool {
	if len(at) == 0 || len(seg) == 0 {
		return false
	}
	now := math.Inf(1)
	for _, a := range at {
		if d := dist3(a, p.self); d < now {
			now = d
		}
	}
	for _, pt := range seg {
		for _, a := range at {
			if dist3(a, pt) < now-PlayRegroupClearYd {
				return true
			}
		}
	}
	return false
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
