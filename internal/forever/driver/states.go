package driver

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// The states' work. Each returns the next state's name, or "" when the run has stopped (stopReason is set).

// prepare checks the GM lines and the party roster, then waits for the party to be rested and alive.
func (r *Run) prepare(ctx context.Context) string {
	// The GM lines are typed actions like every other setup line; they change a mode and move nobody, so the gate is
	// the send function's stop and link check alone.
	if _, why := r.act(ctx, Action{Kind: ActionGMOn}); why != "" {
		if r.stopReason == "" && !r.lost {
			r.stop("prepare:gm")
		}
		return ""
	}
	if _, why := r.act(ctx, Action{Kind: ActionGMVisible}); why != "" {
		if r.stopReason == "" && !r.lost {
			r.stop("prepare:gm-visible")
		}
		return ""
	}
	if r.lost {
		return ""
	}
	party, err := r.cmd.Party(ctx)
	if errors.Is(err, ErrLinkLost) {
		r.linkLost()
		return ""
	}
	if err != nil {
		r.stop("prepare:party")
		return ""
	}
	var why string
	if r.cfg.Watch {
		// A watch is not one of our parties: the tank is the bot the module's run names, the commander is one of
		// the module's own bots outside it, and the roster is where the five names come from. PREPARE then checks it
		// exactly as a run does (levels, roles, online, commanded).
		why = r.adoptRoster(party)
	} else {
		why = rosterWhy(r.cfg, party)
	}
	if why != "" {
		r.stop("prepare:" + why)
		return ""
	}
	deadline := r.now().Add(r.lim.Prepare)
	last, logged := "rest", ""
	for {
		if !r.refresh(ctx) {
			if r.stopReason != "" {
				return ""
			}
		} else if ok, why := r.ready(); ok {
			r.progress()
			return r.afterPrepare()
		} else {
			last = reasonCode(why)
			// One line per changed reason: a party that is not ready says why (a member with the engine off is
			// noengine:<member>, never a silent wait and never "blind").
			if why != logged {
				logged = why
				r.logf("prepare: not ready: %s", why)
			}
		}
		if !r.now().Before(deadline) {
			r.logf("prepare: not ready after %.0fs: %s", r.lim.Prepare.Seconds(), last)
			r.stop("prepare:" + last)
			return ""
		}
		if !r.sleep(ctx, 2*time.Second) {
			return ""
		}
	}
}

// afterPrepare picks the state that brings the party in.
func (r *Run) afterPrepare() string {
	switch r.cfg.Enter {
	case "none":
		return StateLegWalk
	case "portal":
		if r.route.Portal == nil {
			return StateLegWalk
		}
		return StateTravel
	default:
		if r.route.Portal == nil {
			return StateEnter
		}
		return StateTravel
	}
}

// rosterWhy checks the roster the module reports: five members, the tank leading, the commander leading them, levels
// above the entry rule, and the roles the party is made of.
func rosterWhy(cfg Config, p tbcmd.PartyReport) string {
	if len(p.Members) != 5 {
		return "roster"
	}
	if !strings.EqualFold(p.Leader, cfg.Tank()) {
		return "leader"
	}
	if !strings.EqualFold(p.Commander, cfg.Commander) {
		return "commander"
	}
	names := map[string]bool{}
	roles := map[string]int{}
	for _, m := range p.Members {
		names[m.Name] = true
		if !m.Online {
			return "offline"
		}
		if m.HasLevel && m.Level < 10 {
			return "level"
		}
		if m.HasRole {
			roles[m.Role]++
		}
	}
	for _, name := range cfg.Party {
		if !names[name] {
			return "names"
		}
	}
	if roles["tank"] < 1 || roles["healer"] < 1 || roles["dps"] < 3 {
		return "roles"
	}
	return ""
}

// adoptRoster fills a watch's party from the module's roster: the tank first (so every role lookup has the tank
// where the driver's own run keeps it), then the one healer, then the three damage dealers. The party's commander is
// the module's own bot, outside the party; this character is only an observer and is refused if the roster names it
// as the commander. It refuses what a run's PREPARE refuses — five members, exactly one member leader flag naming
// the leader the summary names, the module's run leader being neither the party's tank nor its leader, everyone
// online and above the entry level, the roles — and two things only a watch needs: every member a host bot, and
// every member already commanded, because the module's start refuses a party it would have to bind itself.
func (r *Run) adoptRoster(p tbcmd.PartyReport) string {
	if len(p.Members) != 5 {
		return "roster"
	}
	if r.cfg.Leader == "" {
		return "leader"
	}
	if r.cfg.Commander != "" && strings.EqualFold(p.Commander, r.cfg.Commander) {
		// The observer never commands the party: a roster that names it is the wrong setup, caught before the entry.
		return "observer-commander"
	}
	var tank, heal, flagged string
	var flags int
	var dps []string
	for _, m := range p.Members {
		if !m.Online {
			return "offline"
		}
		if m.HasLevel && m.Level < 10 {
			return "level"
		}
		if !m.Bot {
			return "not-bot"
		}
		if !m.Commanded {
			return "not-commanded"
		}
		if m.Leader {
			flagged, flags = m.Name, flags+1
		}
		switch {
		case m.HasRole && m.Role == "tank":
			if tank != "" {
				return "roles"
			}
			tank = m.Name
		case m.HasRole && m.Role == "healer":
			if heal != "" {
				return "roles"
			}
			heal = m.Name
		case m.HasRole && m.Role == "dps":
			dps = append(dps, m.Name)
		default:
			return "roles"
		}
	}
	if tank == "" || heal == "" || len(dps) != 3 {
		return "roles"
	}
	// The summary and the member lines must agree on who leads: exactly one member carries the flag, and it is the
	// member the summary names. A malformed or skewed report that disagrees would let a --leader the party does not
	// follow through, and the entry would run before the module's start refused it.
	if flags != 1 || !strings.EqualFold(flagged, p.Leader) {
		return "leader"
	}
	// The module's run starts on the bot the watch names, and the start refuses a leader that does not lead its
	// party: the name must be the party's tank or the member that holds the party's leader flag (a healer or a dps
	// may hold it). Any other name would only be refused later by the module, after the entry and its teleports.
	if !strings.EqualFold(r.cfg.Leader, tank) && !strings.EqualFold(r.cfg.Leader, p.Leader) {
		return "leader"
	}
	r.cfg.Party = append([]string{tank, heal}, dps...)
	return ""
}

// travel takes the commander and the party to the portal on the outer map and has the party follow.
//
// The two teleports are moving actions: each waits for its own gate (a fresh complete report, nobody in combat or
// dead) and is retried on every poll while the gate is shut. The party's follow then goes through the movement gate;
// it needs a report taken after the teleport lines, which the poll supplies.
func (r *Run) travel(ctx context.Context) string {
	p := r.route.Portal
	if p == nil {
		return StateEnter
	}
	cmdTele := r.route.OutsideTele
	cmdDone, partyDone, followSent := false, false, false
	deadline := r.now().Add(r.lim.Travel)
	for {
		if !cmdDone {
			a := Action{Kind: ActionTeleOutside, Dest: cmdTele}
			if cmdTele == "" {
				a = Action{Kind: ActionTeleCommander, Who: r.cfg.Commander, Map: p.Map, Pos: p.Pos}
			}
			done, retry := r.actStep(ctx, a, "travel")
			if done {
				cmdDone = true
			} else if !retry {
				return ""
			}
		}
		if cmdDone && !partyDone {
			done, retry := r.actStep(ctx, Action{Kind: ActionTeleParty, Map: p.Map, Pos: p.Pos}, "travel")
			if done {
				partyDone = true
			} else if !retry {
				return ""
			}
		}
		if !r.refresh(ctx) {
			if r.stopReason != "" {
				return ""
			}
		} else if r.allAtPortal(p) {
			r.progress()
			return StateEnter
		} else if cmdDone && partyDone && !followSent {
			if r.cfg.Watch {
				// The party's master is the module's own bot commander, never this character: a follow order
				// naming the observer would order a party it does not command. The party's teleport above already
				// moved it to the portal.
				followSent = true
			} else {
				_, why := r.send(ctx, "party", "follow", r.cfg.Commander, nil)
				followSent = why == "" || r.lost
				if r.stopReason != "" || r.lost {
					return ""
				}
			}
		}
		if !r.now().Before(deadline) {
			r.stop("travel")
			return ""
		}
		if !r.sleep(ctx, 2*time.Second) {
			return ""
		}
	}
}

// allAtPortal says whether the whole party stands on the outer map within the portal's reach.
func (r *Run) allAtPortal(p *Portal) bool {
	if len(r.rep.Bots) != 5 {
		return false
	}
	for _, b := range r.rep.Bots {
		if b.Map != p.Map {
			return false
		}
		if dist3(Vec3{float32(b.X), float32(b.Y), float32(b.Z)}, p.Pos) > PortalArriveYd {
			return false
		}
	}
	return true
}

// enter brings the party in first and the commander after it: by teleport to the tank (the default), or by walking
// through the portal's area trigger. The commander never takes its own trigger.
func (r *Run) enter(ctx context.Context) string {
	if r.cfg.Enter == "none" {
		return StateLegWalk
	}
	tank := r.cfg.Tank()
	enterSent, teleSent := false, false
	if r.cfg.Enter == "portal" {
		if r.route.Portal != nil && r.mapID != r.route.Map {
			r.refreshSelf()
			if dist3(r.pos, r.route.Portal.Pos) > ArrivedYd {
				if res := r.walkTo(ctx, r.route.Portal.Pos, -1, false); res == walkStop {
					return ""
				}
			}
			if err := r.world.EnterAreaTrigger(int32(r.route.Trigger)); err != nil {
				r.stop("enter:trigger")
				return ""
			}
			r.progress()
		}
	}
	deadline := r.now().Add(r.lim.Enter)
	block := ""
	for {
		if r.cfg.Enter != "portal" && !enterSent {
			// The entry is a moving action: a fresh report, nobody in combat or dead.
			done, retry := r.actStep(ctx, Action{Kind: ActionEnter, Who: "party"}, "enter")
			if done {
				enterSent = true
			} else if !retry {
				return ""
			}
		}
		if !r.refresh(ctx) {
			if r.stopReason != "" {
				return ""
			}
		} else {
			// The commander's own map and position come from the world model, not from the party's report.
			r.refreshSelf()
			for _, b := range r.rep.Bots {
				if block == "" && blocked(b) != "" {
					block = blocked(b)
				}
			}
			if r.enteredNow() {
				r.progress()
				if r.cfg.Watch {
					// The party is in; the observer is a GM who comes in on its own terms. A shut gate or a refusal
					// leaves it outside and the watch goes on: it reads the module, not the map.
					r.observerIn(ctx, tank)
					return StateLegWalk
				}
				return StateLegWalk
			}
			if !r.cfg.Watch && r.cfg.Enter != "portal" && enterSent && !teleSent {
				if tb := r.bot(tank); tb != nil && tb.Map == r.route.Map {
					// The commander comes in at the tank's side; the module's teleport is the first choice and the
					// core's appear the fallback. Both are moving actions and pass their own gate.
					_, why := r.act(ctx, Action{Kind: ActionTeleTo, Who: r.cfg.Commander, Other: tank})
					switch {
					case why == "":
						teleSent = true
					case actionHold(why):
						// The gate is shut: try again on the next poll.
					case why == "link" || why == "stop":
						return ""
					default:
						_, why2 := r.act(ctx, Action{Kind: ActionAppear, Who: tank})
						switch {
						case why2 == "":
							teleSent = true
						case why2 == "link" || why2 == "stop":
							return ""
						case !actionHold(why2):
							r.stop("enter:appear")
							return ""
						}
					}
				}
			}
		}
		if !r.now().Before(deadline) {
			if block == "" {
				block = "timeout"
			}
			r.stop("enter:" + block)
			return ""
		}
		if !r.sleep(ctx, 2*time.Second) {
			return ""
		}
	}
}

// enteredNow says whether the party is in: in a run, the commander and every bot on the instance map in one instance;
// in a watch, the party alone, because the observer may still be outside. The party's map is compared with the
// plan's, never with the observer's.
func (r *Run) enteredNow() bool {
	if !r.cfg.Watch && (r.mapID != r.route.Map || r.between) {
		return false
	}
	return r.partyInInstance()
}

// partyInInstance says whether every party member stands on the plan's map in one instance, from a whole report fresh
// enough to act on: a report whose read failed, whose counts disagree or whose five lines are not the party is not
// evidence of anything. Instance 0 is the open world, never the instance a plan names: five bots on map 0 are not in.
func (r *Run) partyInInstance() bool {
	if !r.reportComplete() || !r.repFresh() {
		return false
	}
	inst := r.rep.Bots[0].Instance
	if inst == 0 {
		return false
	}
	for _, b := range r.rep.Bots {
		if b.Map != r.route.Map || b.Instance != inst {
			return false
		}
	}
	return true
}

// observerIn brings the outside observer to the tank's side once a watched party is in. The watch never waits for
// it: the observer is a GM and reads the module from wherever it stands, so a shut gate or a refusal is logged and
// the watch goes on; only a lost link or a pending stop is not. The module's teleport is the first choice, the
// core's appear the fallback.
func (r *Run) observerIn(ctx context.Context, tank string) {
	_, why := r.act(ctx, Action{Kind: ActionTeleTo, Who: r.cfg.Commander, Other: tank})
	switch why {
	case "":
		r.logf("watch: the observer is at the tank's side")
		return
	case "link", "stop":
		return
	}
	_, why = r.act(ctx, Action{Kind: ActionAppear, Who: tank})
	switch why {
	case "":
		r.logf("watch: the observer appeared at the tank's side")
	case "link", "stop":
	default:
		r.logf("watch: the observer stays outside (%s); the watch goes on", why)
	}
}

// walkResult is how one walk ended.
type walkResult int

const (
	walkOK walkResult = iota
	walkPullNow
	walkStop
)

// legWalk walks the leg's via points and To in short segments, polling and looking for hazards between them.
func (r *Run) legWalk(ctx context.Context) string {
	leg := r.leg()
	if leg == nil {
		return StateDone
	}
	points := append(append([]Vec3(nil), leg.Via...), leg.To)
	for _, wp := range points {
		r.refreshSelf()
		if dist3(r.pos, wp) <= ArrivedYd {
			continue
		}
		switch r.walkTo(ctx, wp, -1, true) {
		case walkPullNow:
			return StatePick
		case walkStop:
			return ""
		}
	}
	for {
		if !r.refresh(ctx) {
			if r.stopReason != "" {
				return ""
			}
		} else {
			if len(r.deadBots()) > 0 {
				return StateDeadWait
			}
			if ok, _ := r.ready(); ok {
				r.progress()
				return StatePick
			}
			if next := r.stragglers(ctx); next != "" || r.stopReason != "" {
				return next
			}
		}
		if !r.sleep(ctx, 2*time.Second) {
			return ""
		}
	}
}

// walkTo plans one walk and cuts it into segments. Between segments the driver polls the party and looks for a
// hostile of an uncleared group near the way; the next group found on the way becomes the next pull, any other group
// holds the walk for a while and then stops it (its patrols do not move out of the way in time). A failed poll stops
// the walker at once and no segment is walked until a good report is back; every stop path ends the walker too.
func (r *Run) walkTo(ctx context.Context, to Vec3, target int, checkHazard bool) walkResult {
	r.walking = true
	defer func() { r.walking = false }()
	for range 3 {
		if r.stopReason != "" || r.lost {
			r.stopWalk()
			return walkStop
		}
		r.refreshSelf()
		route, err := r.walk.Path(r.pos, to)
		if err != nil {
			r.stop("walk")
			r.stopWalk()
			return walkStop
		}
		if route.Kind != navmesh.RouteComplete {
			r.stop("walk:" + string(route.Kind))
			r.stopWalk()
			return walkStop
		}
		changed := false
		for _, seg := range SplitRoute(route.Points, SegmentYd) {
			if err := r.walk.Walk(ctx, seg); err != nil {
				if errors.Is(err, ErrMapChange) {
					r.refreshSelf()
					changed = true
					break
				}
				if errors.Is(err, ErrLinkLost) {
					r.linkLost()
					r.stopWalk()
					return walkStop
				}
				if ctx.Err() != nil {
					r.interrupted(ctx)
				} else {
					r.stop("walk")
				}
				r.stopWalk()
				return walkStop
			}
			r.progress()
			// A failed poll is "the party is unknown": no further segment until a good report answers.
			if !r.waitGoodReport(ctx) {
				r.stopWalk()
				return walkStop
			}
			units := r.view()
			r.recordFrame(units)
			// The hazard goes before the straggler rule: a pack the way runs into is pulled (or blocks the walk)
			// before any movement order can point a bot at this ground.
			res, gid := r.hazard(ctx, units, target, seg[len(seg)-1], to)
			switch res {
			case hazardPull:
				r.forced = gid
				return walkPullNow
			case hazardBlocked:
				r.stopWalk()
				return walkStop
			}
			if next := r.stragglers(ctx); next != "" || r.stopReason != "" || r.lost {
				r.stopWalk()
				return walkStop
			}
		}
		if !changed {
			return walkOK
		}
	}
	r.stop("walk:mapchange")
	return walkStop
}

// waitGoodReport holds the walk until a good state report answers. The first failed poll ends the walker, so no
// segment is walked while the party is unknown; a stop that comes from the poll (blind, budget, link) ends the walk.
func (r *Run) waitGoodReport(ctx context.Context) bool {
	for {
		if r.refresh(ctx) {
			return true
		}
		if r.stopReason != "" || r.lost {
			return false
		}
		r.stopWalk()
		if !r.sleep(ctx, 2*time.Second) {
			return false
		}
	}
}

// stopWalk ends the walker; every stop path of a walk calls it.
func (r *Run) stopWalk() {
	if r.walk != nil {
		_ = r.walk.Stop()
	}
}

// hazard says what an uncleared group's unit near the way means for the walk.
type hazard int

const (
	hazardNone hazard = iota
	hazardPull
	hazardBlocked
)

// hazard looks for a live hostile of an uncleared plan group within HazardYd of the point or the destination. Such a
// group becomes the next pull (pulling the plan's next pack while it stands 18 yd away would bring it in anyway, and a
// static pack never moves out of the way). Only a hostile that belongs to no plan group holds the walk for HazardHold
// and, if it stays, stops it.
func (r *Run) hazard(ctx context.Context, units []Unit, target int, at, to Vec3) (hazard, int) {
	claim := r.claim(units)
	nextID := r.nextUnclearedAfter(target)
	other := false
	for ui := range units {
		u := units[ui]
		if u.Dead || u.Friendly {
			continue
		}
		gid, ok := claim.GroupOf(ui)
		if !ok || r.cleared[gid] {
			continue // a unit of no plan group is not a pack hazard
		}
		if dist3(u.Pos, at) > HazardYd && dist3(u.Pos, to) > HazardYd {
			continue
		}
		if _, bad := r.route.InAvoid(u.Pos); bad {
			continue // never pulled: an avoid box is ground, not a mob
		}
		if gid == target || gid == nextID {
			return hazardPull, gid
		}
		other = true
	}
	if !other {
		return hazardNone, -1
	}
	deadline := r.now().Add(r.lim.HazardHold)
	for {
		if !r.sleep(ctx, 5*time.Second) {
			return hazardBlocked, -1
		}
		if !r.refresh(ctx) && r.stopReason != "" {
			return hazardBlocked, -1
		}
		units = r.view()
		claim = r.claim(units)
		still := false
		for ui := range units {
			u := units[ui]
			if u.Dead || u.Friendly {
				continue
			}
			gid, ok := claim.GroupOf(ui)
			if !ok || r.cleared[gid] || gid == nextID || gid == target {
				continue
			}
			if dist3(u.Pos, at) <= HazardYd || dist3(u.Pos, to) <= HazardYd {
				still = true
				break
			}
		}
		if !still {
			return hazardNone, -1
		}
		if !r.now().Before(deadline) {
			r.stop("path-blocked")
			return hazardBlocked, -1
		}
	}
}

// nextUnclearedAfter is the first group after target in plan order that is not cleared (the first group when target
// is -1). The hazard rule treats that group's pack and the target's own as "the next group".
func (r *Run) nextUnclearedAfter(target int) int {
	leg := r.leg()
	if leg == nil {
		return -1
	}
	for i := range leg.Groups {
		g := &leg.Groups[i]
		if g.ID == target || r.cleared[g.ID] {
			continue
		}
		if target >= 0 {
			t, ok := r.groupByID(target)
			if ok && g.ID < t.ID {
				continue
			}
			if ok && g.ID > t.ID {
				return g.ID
			}
			continue
		}
		return g.ID
	}
	return -1
}

// pick chooses the leg's next target: a patrol group in view first, then the groups in plan order, then the boss.
func (r *Run) pick(ctx context.Context) string {
	leg := r.leg()
	if leg == nil {
		return StateDone
	}
	units := r.view()
	claim := r.claim(units)
	r.noteDeaths(claim, units)
	if r.busy() {
		// Something is still fighting that the driver did not pull (an add out of the threat list): wait it out.
		deadline := r.now().Add(r.lim.HazardHold)
		for r.busy() && r.now().Before(deadline) {
			if !r.sleep(ctx, 2*time.Second) {
				return ""
			}
			if !r.refresh(ctx) && r.stopReason != "" {
				return ""
			}
		}
		if r.busy() {
			r.stop("combat")
			return ""
		}
	}
	var cand *Group
	if r.forced >= 0 {
		if g, ok := r.groupByID(r.forced); ok && !r.cleared[g.ID] {
			cand = &g
		}
		r.forced = -1
	}
	for i := range leg.Groups {
		g := &leg.Groups[i]
		if cand != nil {
			break
		}
		if r.cleared[g.ID] {
			continue
		}
		if g.Patrol != nil && r.patrolInView(*g, units, claim) {
			cand = g
			break
		}
	}
	if cand == nil {
		for i := range leg.Groups {
			g := &leg.Groups[i]
			if r.cleared[g.ID] {
				continue
			}
			if r.groupClear(*g, units, claim) {
				r.cleared[g.ID] = true
				continue
			}
			cand = g
			break
		}
	}
	if cand == nil {
		if leg.Boss != nil && !r.bossDead[leg.Boss.Entry] {
			r.boss = true
			r.group = -1
			return StatePosition
		}
		if r.nextLeg() {
			return StateLegWalk
		}
		return StateDone
	}
	r.boss = false
	r.group = cand.ID
	return StatePosition
}

// position walks to the group's stand and waits for readiness and for the patrol to pass.
func (r *Run) position(ctx context.Context) string {
	leg := r.leg()
	if leg == nil {
		return StateDone
	}
	var g Group
	var stand Vec3
	if r.boss {
		b := leg.Boss
		if b == nil {
			return StatePick
		}
		stand = bossStand(*b, leg.To)
	} else {
		var ok bool
		g, ok = r.groupByID(r.group)
		if !ok {
			r.stop("state:position")
			return ""
		}
		stand = g.Stand(leg.To)
	}
	r.refreshSelf()
	if dist3(r.pos, stand) > ArrivedYd {
		target := -1
		if !r.boss {
			target = g.ID
		}
		switch r.walkTo(ctx, stand, target, true) {
		case walkPullNow:
			return StatePick
		case walkStop:
			return ""
		}
	}
	patrolSince := time.Time{}
	for {
		if !r.sleep(ctx, 2*time.Second) {
			return ""
		}
		units := r.view()
		ok := r.refresh(ctx)
		if r.stopReason != "" {
			return ""
		}
		if !ok {
			continue
		}
		if len(r.deadBots()) > 0 {
			return StateDeadWait
		}
		if next := r.stragglers(ctx); next != "" || r.stopReason != "" {
			return next
		}
		ready, _ := r.ready()
		claim := r.claim(units)
		r.noteDeaths(claim, units)
		if !r.boss && r.groupClear(g, units, claim) {
			r.cleared[g.ID] = true
			return StatePick
		}
		if !ready {
			continue
		}
		if r.boss {
			return StateBoss
		}
		if g.Patrol != nil && r.patrolNear(g, units, claim, stand) != nil {
			if patrolSince.IsZero() {
				patrolSince = r.now()
			}
			if r.now().Sub(patrolSince) >= r.lim.Patrol {
				if pg, ok := r.patrolGroup(g, units, claim); ok {
					r.group = pg.ID
					r.progress()
					return StatePosition
				}
				r.stop("patrol")
				return ""
			}
			continue
		}
		patrolSince = time.Time{}
		if len(r.liveMembers(g.ID, units, claim)) == 0 {
			if d, ok := r.dwell[g.ID]; ok && r.now().Sub(d) >= 5*time.Second {
				r.cleared[g.ID] = true
				return StatePick
			} else if !ok {
				r.dwell[g.ID] = r.now()
			}
		} else {
			delete(r.dwell, g.ID)
		}
		return StatePull
	}
}

// bossStand is where the commander stands to fight the boss: its approach point, else a look point from the leg.
func bossStand(b Boss, from Vec3) Vec3 {
	if len(b.Approach) > 0 {
		return b.Approach[len(b.Approach)-1]
	}
	d := dist3(from, b.Pos)
	if d <= 1 {
		return b.Pos
	}
	standoff := mathMin(8, d-0.5)
	fx, fy, fz := float64(from[0]), float64(from[1]), float64(from[2])
	x, y, z := float64(b.Pos[0]), float64(b.Pos[1]), float64(b.Pos[2])
	return Vec3{float32(x + (fx-x)*standoff/d), float32(y + (fy-y)*standoff/d), float32(z + (fz-z)*standoff/d)}
}

func mathMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// pull orders one pull of the group's member nearest the tank. Every attempt comes through pullAllowed in the same step
// as its send: the report that permits it is taken after the last order and after any wait, and the target is picked
// from the view that gate returned.
func (r *Run) pull(ctx context.Context) string {
	g, ok := r.groupByID(r.group)
	if !ok {
		return StatePick
	}
	attempts := 0
	code := ""
	for {
		if r.stopReason != "" || r.lost {
			return ""
		}
		permit, why := r.pullAllowed(ctx)
		if why != "" {
			if r.stopReason != "" || r.lost {
				return ""
			}
			if r.repErr == nil && len(r.deadBots()) > 0 {
				return StateDeadWait
			}
			if next := r.stragglers(ctx); next != "" || r.stopReason != "" {
				return next
			}
			if !r.sleep(ctx, 2*time.Second) {
				return ""
			}
			continue
		}
		units := permit.units
		claim := r.claim(units)
		r.noteDeaths(claim, units)
		if r.groupClear(g, units, claim) {
			r.cleared[g.ID] = true
			return StatePick
		}
		target, ok := r.pullTarget(g, units, claim)
		if !ok {
			return StatePick
		}
		sent, c := r.sendPull(ctx, permit, target)
		if sent {
			r.startFight(g.ID, target, false, 0, units)
			return StateFight
		}
		if r.stopReason != "" {
			return ""
		}
		code = c
		attempts++
		r.retries[g.ID]++
		if attempts >= 2 || r.retries[g.ID] >= r.lim.MaxRetries {
			r.stop("pull-refused:" + nonEmpty(code, "refused"))
			return ""
		}
	}
}

// pullTarget is the group's live member nearest the tank.
func (r *Run) pullTarget(g Group, units []Unit, claim packClaim) (Unit, bool) {
	tp, ok := r.tankPos()
	if !ok {
		return Unit{}, false
	}
	var best Unit
	bestD := 1e18
	found := false
	for _, ui := range claim.Members(g.ID) {
		u := units[ui]
		if u.Dead || u.Friendly {
			continue
		}
		if d := dist3(u.Pos, tp); !found || d < bestD {
			best, bestD, found = u, d, true
		}
	}
	return best, found
}

// sendPull sends one pull, but only with a permit from pullAllowed taken in this same step, and waits for it to be
// carried out: the tank in combat or the mob on a threat list.
func (r *Run) sendPull(ctx context.Context, p pullPermit, u Unit) (bool, string) {
	key := guidText(u.GUID)
	rep, why := r.send(ctx, r.cfg.Tank(), "pull", key, &p)
	if why != "" {
		return false, why
	}
	if rep.Refused > 0 || rep.Queued == 0 {
		code := "refused"
		for _, res := range rep.Results {
			if res.HasErr {
				code = res.Error
			}
		}
		return false, code
	}
	deadline := r.now().Add(r.lim.Pull)
	for r.now().Before(deadline) {
		if !r.sleep(ctx, 2*time.Second) {
			return false, ""
		}
		if !r.refresh(ctx) && r.stopReason != "" {
			return false, ""
		}
		if tb := r.bot(r.cfg.Tank()); tb != nil && tb.InCombat {
			return true, ""
		}
		if t, ok := r.threat(ctx); ok {
			for _, m := range t.Mobs {
				if g, ok := parseGUID(m.Mob); ok && g == u.GUID {
					return true, ""
				}
			}
		}
	}
	return false, "no-combat"
}

// sendAttack sends the party's attack on one mob, only with a permit from pullAllowed taken in this same step.
func (r *Run) sendAttack(ctx context.Context, p pullPermit, g wire.GUID) (bool, string) {
	rep, why := r.send(ctx, "party", "attack", guidText(g), &p)
	if why != "" {
		return false, why
	}
	if rep.Refused > 0 || rep.Queued == 0 {
		code := "refused"
		for _, res := range rep.Results {
			if res.HasErr {
				code = res.Error
			}
		}
		return false, code
	}
	return true, ""
}

// startFight sets up the fight's bookkeeping and the trace's pull_start.
func (r *Run) startFight(gid int, target Unit, boss bool, entry uint32, units []Unit) {
	f := &fight{
		group: gid, boss: boss, entry: entry, mobs: map[string]bool{}, mobGroups: map[string]int{},
		unitEntry: map[string]uint32{}, killed: map[string]bool{}, lost: map[string]time.Time{},
		groups: map[int]bool{}, started: r.now(), runner: map[string]bool{},
		heals: map[string]bool{}, firstDst: map[string]float64{}, minHP: map[string]int{}, deaths: map[string]bool{},
	}
	f.mobs[guidText(target.GUID)] = true
	f.unitEntry[guidText(target.GUID)] = target.Entry
	if gid >= 0 {
		f.groups[gid] = true
		f.mobGroups[guidText(target.GUID)] = gid
		claim := r.claim(units)
		for _, ui := range claim.Members(gid) {
			u := units[ui]
			if !u.Dead && !u.Friendly {
				k := guidText(u.GUID)
				f.mobs[k] = true
				f.mobGroups[k] = gid
				f.unitEntry[k] = u.Entry
			}
		}
	}
	r.fight = f
	r.crowded = len(f.mobs) >= 7 || len(f.groups) >= 2
	pull := PullRecord{Boss: boss, Mobs: len(f.mobs)}
	if gid >= 0 {
		pull.Retries = r.retries[gid]
	}
	if gid >= 0 {
		if g, ok := r.groupByID(gid); ok {
			pull.Leg = r.lastLegName()
			pull.Group = g.Index
			pull.Entries = append([]uint32(nil), g.Entries...)
		}
	} else {
		pull.Leg = r.lastLegName()
		pull.Group = -1
		pull.Entries = []uint32{entry}
	}
	if r.rec != nil {
		r.rec.PullStart(r.now(), pull)
		r.pullOpen = true
	}
}

// fightState watches the fight, orders the runner's attack, adds every mob that joins the threat list and ends when
// the threat list is empty and every living bot is out of combat in two polls.
func (r *Run) fightState(ctx context.Context) string {
	f := r.fight
	if f == nil {
		return StatePick
	}
	prev := r.now()
	var lastUnits []Unit
	for {
		if !r.sleep(ctx, 2*time.Second) {
			return ""
		}
		now := r.now()
		dt := now.Sub(prev).Seconds()
		prev = now
		lastUnits = r.view()
		ok := r.refresh(ctx)
		if r.stopReason != "" {
			return ""
		}
		r.recordFrame(lastUnits)
		if !ok {
			continue // the party is unknown: hold, do not decide the fight is over
		}
		for _, b := range r.rep.Bots {
			if isDead(b) && !f.deaths[b.Name] {
				f.deaths[b.Name] = true
				f.pull.Deaths = append(f.pull.Deaths, r.roleName(b.Name))
				r.deadTotal++
				r.event("death", map[string]any{"bot": r.roleName(b.Name), "leg": r.lastLegName(), "group": f.group})
			}
			if pct, seen := f.minHP[b.Name]; !seen || b.HPPct < pct {
				f.minHP[b.Name] = b.HPPct
			}
		}
		tankGUID, _ := r.world.PlayerGUID(r.cfg.Tank())
		for _, u := range lastUnits {
			if u.Dead {
				continue
			}
			if f.mobs[guidText(u.GUID)] && !tankGUID.IsZero() && u.Target != tankGUID {
				f.pull.TankNotTargetedS += dt
			}
			if u.Cast == 11986 {
				k := guidText(u.GUID)
				if !f.heals[k] {
					f.heals[k] = true
					f.pull.HealsByMob++
				}
			}
		}
		r.crowded = len(f.mobs) >= 7 || len(f.groups) >= 2
		if t, tOK := r.threat(ctx); tOK {
			for _, m := range t.Mobs {
				g, ok := parseGUID(m.Mob)
				if !ok {
					continue
				}
				k := guidText(g)
				if f.mobs[k] {
					continue
				}
				f.mobs[k] = true
				f.unitEntry[k] = uint32(m.Entry)
				f.adds++
				f.pull.Adds++
				r.event("add", map[string]any{"guid": k, "entry": m.Entry, "leg": r.lastLegName()})
				claim := r.claim(lastUnits)
				for ui := range lastUnits {
					if lastUnits[ui].GUID == g {
						if gid, ok := claim.GroupOf(ui); ok {
							f.groups[gid] = true
							f.mobGroups[k] = gid
						}
					}
				}
			}
			onList := map[string]bool{}
			for _, m := range t.Mobs {
				if g, ok := parseGUID(m.Mob); ok {
					onList[guidText(g)] = true
				}
			}
			for k := range f.mobs {
				if onList[k] {
					delete(f.lost, k)
					continue
				}
				if f.killed[k] {
					continue
				}
				u, ok := parseGUID(k)
				if !ok {
					continue
				}
				wu, inView := unitByGUID(lastUnits, u)
				switch {
				case inView && wu.Dead:
					// seen dead: the only way a mob counts as killed
					f.killed[k] = true
					delete(f.lost, k)
				case inView:
					// alive and in sight but off the list: it may come back, so it is neither dead nor lost
				default:
					if _, seen := f.lost[k]; !seen {
						f.lost[k] = now
						f.lostEnts = append(f.lostEnts, f.unitEntry[k])
						if gid, ok := f.mobGroups[k]; ok {
							r.lostGroups[gid] = true
						}
						r.event("lost", map[string]any{
							"guid": k, "entry": f.unitEntry[k], "leg": r.lastLegName(), "group": f.group,
						})
					}
				}
			}
			for k, since := range f.lost {
				if now.Sub(since) < r.lim.Lost {
					continue
				}
				delete(f.lost, k)
				r.event("lost-mob", map[string]any{
					"guid": k, "entry": f.unitEntry[k], "leg": r.lastLegName(), "group": f.group,
					"seconds": now.Sub(since).Seconds(),
				})
				// Nothing may clear the group; the run ends here rather than pulling a pack it cannot judge.
				r.stop("lost-mob")
			}
			if !r.crowded {
				r.runners(ctx, lastUnits, t, tankGUID, f)
			}
			// The fight is over only when nothing is on a threat list, no living bot is in combat and no mob is still
			// missing: a mob that left the list without being seen dead holds the fight.
			if t.Count == 0 && !r.busy() && len(f.lost) == 0 && r.stopReason == "" {
				f.endOK++
				if f.endOK >= 2 {
					break
				}
			} else {
				f.endOK = 0
			}
		}
		if r.wipeNow() {
			break
		}
		elapsed := now.Sub(f.started)
		long := r.lim.LongTrash
		if f.boss {
			long = r.lim.LongBoss
		}
		if elapsed >= long && !f.longSent {
			f.longSent = true
			r.event("long-fight", map[string]any{"seconds": elapsed.Seconds(), "leg": r.lastLegName(), "boss": f.boss})
		}
		if elapsed >= r.lim.FightMax {
			r.stop("fight-timeout")
			return ""
		}
	}
	r.progress()
	r.restStart = r.now()
	r.updatePull()
	// A group whose member died in the fight is clear even when its corpse lies out of the group's radius (a fleeing
	// mob dies away from its spawn): the death was seen in this fight.
	for k, gid := range f.mobGroups {
		if f.killed[k] {
			r.deaths[gid] = true
		}
	}
	if f.boss {
		if leg := r.leg(); leg != nil && leg.Boss != nil && r.unitDeadIn(lastUnits, leg.Boss.Entry) {
			r.noteBossDead(leg.Boss.Entry)
		}
	}
	if r.wipeNow() {
		return StateWipe
	}
	if len(r.deadBots()) > 0 {
		return StateDeadWait
	}
	return StateRest
}

// runners orders one attack on a fleeing mob that has run away from the tank, at most once per mob per fight. The order
// goes through the same gate as a pull (a fresh report, readiness, the rate window, no stop), in the same step as the
// send; a shut gate leaves the mob for the next poll.
func (r *Run) runners(ctx context.Context, units []Unit, t tbcmd.ThreatReport, tankGUID wire.GUID, f *fight) {
	tp, ok := r.tankPos()
	if !ok {
		return
	}
	for _, m := range t.Mobs {
		if !r.route.Fleers[uint32(m.Entry)] || m.HPPct > 20 {
			continue
		}
		g, ok := parseGUID(m.Mob)
		if !ok {
			continue
		}
		u, ok := unitByGUID(units, g)
		if !ok {
			continue
		}
		d := dist3(u.Pos, tp)
		k := guidText(g)
		first, seen := f.firstDst[k]
		if !seen {
			f.firstDst[k] = d
			continue
		}
		if d-first < 8 || f.runner[k] {
			continue
		}
		permit, why := r.pullAllowed(ctx)
		if why != "" {
			if r.stopReason != "" || r.lost {
				return
			}
			continue
		}
		if _, inView := unitByGUID(permit.units, g); !inView {
			continue
		}
		f.runner[k] = true
		if sent, _ := r.sendAttack(ctx, permit, g); sent {
			r.event("runner", map[string]any{"guid": k, "entry": m.Entry, "leg": r.lastLegName()})
		}
		if r.stopReason != "" || r.lost {
			return
		}
	}
}

// unitDeadIn reports a dead unit of the entry in view.
func (r *Run) unitDeadIn(units []Unit, entry uint32) bool {
	for _, u := range units {
		if u.Entry == entry && u.Dead {
			return true
		}
	}
	return false
}

// rest polls until the party is ready again, then goes back to the leg's work.
func (r *Run) rest(ctx context.Context) string {
	next, settled := r.settleParty(ctx, r.lim.Rest, 5*time.Second, "rest")
	if !settled {
		return next
	}
	r.closePull()
	r.progress()
	if r.boss {
		leg := r.leg()
		if leg == nil {
			return StateDone
		}
		if leg.Boss != nil && !r.bossDead[leg.Boss.Entry] {
			return StateBoss
		}
		if r.nextLeg() {
			return StateLegWalk
		}
		return StateDone
	}
	return StatePick
}

// settleParty waits for the party's readiness, polling every interval. A bot out of range gets the straggler rule
// here too; the state it asks for is returned as next.
func (r *Run) settleParty(ctx context.Context, limit, interval time.Duration, reason string) (next string, settled bool) {
	deadline := r.now().Add(limit)
	for {
		if !r.sleep(ctx, interval) {
			return "", false
		}
		ok := r.refresh(ctx)
		if r.stopReason != "" {
			return "", false
		}
		if ok {
			if next := r.stragglers(ctx); next != "" || r.stopReason != "" {
				return next, false
			}
			if ready, _ := r.ready(); ready {
				return "", true
			}
		}
		if !r.now().Before(deadline) {
			r.stop(reason)
			return "", false
		}
	}
}

// deadWait waits for the party's dead bots to be raised by the priest, then practices a revive.
//
// Each revive is a typed action under the run's practice budget, one bot at a time: the action's gate takes its own
// fresh report, so a bot the priest raised between two polls is not revived again, and no revive goes out on the
// report that still showed it dead.
func (r *Run) deadWait(ctx context.Context) string {
	deadline := r.now().Add(r.lim.DeadWait)
	practiced := false
	revived := map[string]bool{}
	for {
		if !r.refresh(ctx) {
			if r.stopReason != "" {
				return ""
			}
		} else {
			r.recordFrame(r.view())
			dead := r.deadBots()
			if len(dead) == 0 {
				r.progress()
				return StateRest
			}
			if practiced {
				for _, name := range dead {
					if revived[name] {
						continue
					}
					// One revive per poll; a shut gate leaves the name for the next poll.
					done, retry := r.actStep(ctx, Action{Kind: ActionRevive, Who: name, Practice: true}, "dead-wait")
					if done {
						revived[name] = true
					}
					if !done && !retry {
						return ""
					}
					break
				}
			}
		}
		if !practiced && !r.now().Before(deadline) {
			if !r.practiceOn() {
				r.stop("dead-wait")
				return ""
			}
			practiced = true
			deadline = r.now().Add(30 * time.Second)
			continue
		}
		if practiced && !r.now().Before(deadline) {
			r.stop("dead-wait")
			return ""
		}
		if !r.sleep(ctx, 2*time.Second) {
			return ""
		}
	}
}

// wipe walks the commander back to the leg's start, waits for the mobs to reset, and only as a last resort revives
// the party and brings it back.
//
// The recovery is a typed action sequence: one party revive, then each bot teleported to the commander. Each of them
// is gated in the same step as its send, so the report the next action stands on is always complete and newer than
// the line before it — nothing moves on the report that showed the wipe.
func (r *Run) wipe(ctx context.Context) string {
	leg := r.leg()
	if leg == nil {
		return StateDone
	}
	r.wipes++
	gid := r.group
	if r.boss {
		gid = -1
	}
	if gid >= 0 {
		r.wipeGroups[gid]++
	}
	r.event("wipe", map[string]any{"leg": r.lastLegName(), "group": gid, "wipe": r.wipes})
	// MaxWipes is the wipe budget: 0 allows no wipe at all (the first one ends the run), a negative value is no
	// budget. The watch reads it the same way.
	if r.lim.MaxWipes >= 0 && r.wipes >= r.lim.MaxWipes {
		r.stop("wipe-budget")
		return ""
	}
	if gid >= 0 && r.wipeGroups[gid] >= 2 {
		r.stop("wipe-repeat")
		return ""
	}
	if dist3(r.pos, leg.Start) > ArrivedYd {
		if res := r.walkTo(ctx, leg.Start, -1, false); res == walkStop {
			return ""
		}
	}
	deadline := r.now().Add(r.lim.Wipe)
	quiet := time.Time{}
	for {
		if !r.refresh(ctx) {
			if r.stopReason != "" {
				return ""
			}
		} else if r.rep.End["whole"] == "yes" {
			r.progress()
			return StateRest
		} else if t, ok := r.threat(ctx); ok {
			if t.Count == 0 && !r.busy() {
				if quiet.IsZero() {
					quiet = r.now()
				} else if r.now().Sub(quiet) >= 10*time.Second {
					r.progress()
					return StateRest
				}
			} else {
				quiet = time.Time{}
			}
		}
		if !r.now().Before(deadline) {
			break
		}
		if !r.sleep(ctx, 5*time.Second) {
			return ""
		}
	}
	if !r.practiceOn() {
		r.stop("wipe")
		return ""
	}
	deadline = r.now().Add(r.lim.Wipe)
	revived := false
	next := 0
	for next < len(r.cfg.Party) {
		a := Action{Kind: ActionRevive, Who: "party", Recovery: true}
		if revived {
			a = Action{Kind: ActionTeleTo, Who: r.cfg.Party[next], Other: r.cfg.Commander, Recovery: true, Practice: true}
		}
		done, retry := r.actStep(ctx, a, "wipe")
		if done {
			if revived {
				next++
			} else {
				revived = true
			}
			continue
		}
		if !retry {
			return ""
		}
		if !r.now().Before(deadline) {
			r.stop("wipe:recover")
			return ""
		}
		if !r.sleep(ctx, 2*time.Second) {
			return ""
		}
	}
	return StateRest
}

// bossState pulls the boss's first entries in order, then the boss.
func (r *Run) bossState(ctx context.Context) string {
	leg := r.leg()
	if leg == nil {
		return StateDone
	}
	b := leg.Boss
	if b == nil {
		return StateRest
	}
	units := r.view()
	if r.unitDeadIn(units, b.Entry) {
		r.noteBossDead(b.Entry)
	}
	if r.bossDead[b.Entry] {
		return StateRest
	}
	entry := b.Entry
	for _, e := range b.First {
		if !r.unitDeadIn(units, e) {
			entry = e
			break
		}
	}
	walked := false
	deadline := r.now().Add(r.lim.Boss)
	attempts := 0
	code := ""
	for {
		if r.stopReason != "" || r.lost {
			return ""
		}
		permit, why := r.pullAllowed(ctx)
		if why != "" {
			if r.stopReason != "" || r.lost {
				return ""
			}
			if r.repErr == nil && len(r.deadBots()) > 0 {
				return StateDeadWait
			}
			// Keep walking for the boss while the gate is shut for a reason that may clear (readiness, rate, a
			// report that failed) but end at the boss deadline.
			if !walked {
				stand := bossStand(*b, leg.To)
				if dist3(r.pos, stand) > ArrivedYd {
					if res := r.walkTo(ctx, stand, -1, false); res == walkStop {
						return ""
					}
				}
				walked = true
				deadline = r.now().Add(r.lim.Boss)
			}
			if !r.now().Before(deadline) {
				r.stop("boss-missing")
				return ""
			}
			if !r.sleep(ctx, 2*time.Second) {
				return ""
			}
			continue
		}
		units = permit.units
		if r.unitDeadIn(units, b.Entry) {
			r.noteBossDead(b.Entry)
			return StateRest
		}
		if target, ok := r.bossTarget(units, entry, *b); ok {
			sent, c := r.sendPull(ctx, permit, target)
			if sent {
				r.startFight(-1, target, true, entry, units)
				return StateFight
			}
			if r.stopReason != "" {
				return ""
			}
			code = c
			attempts++
			if attempts >= 2 {
				r.stop("pull-refused:" + nonEmpty(code, "refused"))
				return ""
			}
			continue
		}
		if !walked {
			stand := bossStand(*b, leg.To)
			if dist3(r.pos, stand) > ArrivedYd {
				if res := r.walkTo(ctx, stand, -1, false); res == walkStop {
					return ""
				}
			}
			walked = true
			deadline = r.now().Add(r.lim.Boss)
		}
		if !r.now().Before(deadline) {
			r.stop("boss-missing")
			return ""
		}
		if !r.sleep(ctx, 2*time.Second) {
			return ""
		}
	}
}

// bossTarget is a live unit of the entry: near the boss's spot, or anywhere in view.
func (r *Run) bossTarget(units []Unit, entry uint32, b Boss) (Unit, bool) {
	radius := float64(b.Radius)
	if radius <= 0 {
		radius = 30
	}
	var best Unit
	bestD := 1e18
	found := false
	for _, u := range units {
		if u.Entry != entry || u.Dead || u.Friendly {
			continue
		}
		d := dist3(u.Pos, b.Pos)
		if d <= radius || d <= GroupViewYd {
			if d < bestD {
				best, bestD, found = u, d, true
			}
		}
	}
	return best, found
}

// done rests the party one last time and ends the run clear; with --reset the clear is marked only after the leave step.
func (r *Run) done(ctx context.Context) string {
	next, settled := r.settleParty(ctx, r.lim.Rest, 5*time.Second, "rest")
	if !settled {
		return next
	}
	// The run has reached its end: from here the instance reset is allowed, and only from here.
	r.ended = true
	if r.cfg.Reset {
		return StateLeave
	}
	r.markClear()
	r.stop("clear")
	return ""
}

// markClear marks the run clear in the summary, if there is one.
func (r *Run) markClear() {
	if r.rec != nil {
		r.rec.sum.Clear = true
	}
}

// leave leaves the instance, brings the commander out and resets the instance.
//
// The leave and the commander's teleport are moving actions. The reset is the one action with its own rule: the run
// must have reached its end and a fresh report must show the whole party outside, because the module refuses a reset
// from inside. Until then the driver does not ask.
func (r *Run) leave(ctx context.Context) string {
	tank := r.cfg.Tank()
	deadline := r.now().Add(r.lim.Leave)
	left, out := false, r.route.OutsideTele == ""
	for {
		if !left {
			done, retry := r.actStep(ctx, Action{Kind: ActionInstanceLeave, Who: tank}, "leave")
			if done {
				left = true
			} else if !retry {
				return ""
			}
		}
		if left && !out {
			done, retry := r.actStep(ctx, Action{Kind: ActionTeleOutside, Dest: r.route.OutsideTele}, "leave")
			if done {
				out = true
			} else if !retry {
				return ""
			}
		}
		if left && out {
			done, retry := r.actStep(ctx, Action{Kind: ActionInstanceReset, Who: tank}, "leave")
			if done {
				r.progress()
				r.markClear()
				r.stop("clear")
				return ""
			}
			if !retry {
				return ""
			}
		}
		if !r.now().Before(deadline) {
			r.stop("leave")
			return ""
		}
		if !r.sleep(ctx, 2*time.Second) {
			return ""
		}
	}
}

// allOutside says whether every bot is off the instance map.
func (r *Run) allOutside() bool {
	if len(r.rep.Bots) != 5 {
		return false
	}
	for _, b := range r.rep.Bots {
		if b.Map == r.route.Map {
			return false
		}
	}
	return true
}

// stragglers applies the straggler rule: a come at 20 s, a follow at 50 s, a stop at 90 s; a bot on another map or
// blocked waits 60 s and then stops the run. It returns a state to switch to, or "".
func (r *Run) stragglers(ctx context.Context) string {
	if r.rep == nil || r.repErr != nil {
		return ""
	}
	now := r.now()
	for _, b := range r.rep.Bots {
		if isDead(b) {
			continue
		}
		if blocked(b) != "" || b.DistMaster.Kind == "othermap" || b.Map != r.mapID {
			if r.outSince[b.Name].IsZero() {
				r.outSince[b.Name] = now
			}
			if now.Sub(r.outSince[b.Name]) >= r.lim.OutsideWait {
				r.stop("bot-outside:" + nonEmpty(blocked(b), "othermap"))
				return ""
			}
			continue
		}
		delete(r.outSince, b.Name)
		far := b.DistMaster.Has && b.DistMaster.Kind != "self" && b.DistMaster.Yards > 30
		if !far {
			delete(r.farSince, b.Name)
			delete(r.farStage, b.Name)
			continue
		}
		if r.farSince[b.Name].IsZero() {
			r.farSince[b.Name] = now
			r.event("straggler", map[string]any{"bot": r.roleName(b.Name), "yards": b.DistMaster.Yards})
		}
		d := now.Sub(r.farSince[b.Name])
		stage := r.farStage[b.Name]
		verb, arg := "", ""
		switch {
		case d >= r.lim.Straggler[2]:
			r.stop("straggler:" + r.roleName(b.Name))
			return ""
		case d >= r.lim.Straggler[1] && stage < 2:
			verb, arg = "follow", r.cfg.Commander
		case d >= r.lim.Straggler[0] && stage < 1:
			verb = "come"
		}
		if verb == "" {
			continue
		}
		_, why := r.send(ctx, b.Name, verb, arg, nil)
		if r.stopReason != "" || r.lost {
			return ""
		}
		if why == "" {
			if verb == "come" {
				r.farStage[b.Name] = 1
			} else {
				r.farStage[b.Name] = 2
			}
			delete(r.blockedSince, b.Name)
			continue
		}
		// The movement gate refused: nothing was sent. A straggler behind an uncleared pack is the case the walk back
		// answer; a stale report is just the next poll's job.
		if why == "pack" {
			if r.stragglerBehind(ctx, b.Name, Vec3{float32(b.X), float32(b.Y), float32(b.Z)}) {
				return ""
			}
		}
	}
	return ""
}

// stragglerBehind handles a straggler whose movement order the pack gate refused. Nothing is sent: the commander
// walks back toward the bot, one RetreatStepYd (20 yd) step per poll, only on ground outside every live pack's radius
// and only when no walk is in progress, so the bot's next line may clear the pack. After StragglerBack (30 s) the
// run ends as straggler behind a pack. It reports whether the run has stopped.
func (r *Run) stragglerBehind(ctx context.Context, name string, bot Vec3) bool {
	now := r.now()
	if r.blockedSince[name].IsZero() {
		r.blockedSince[name] = now
		r.event("straggler_behind_pack", map[string]any{"bot": r.roleName(name)})
	}
	if now.Sub(r.blockedSince[name]) >= r.lim.StragglerBack {
		r.stop("straggler-behind-pack:" + r.roleName(name))
		return true
	}
	if !r.walking {
		r.retreatToward(ctx, bot)
	}
	return r.stopReason != ""
}

// claim matches the view's units to the route's groups.
func (r *Run) claim(units []Unit) packClaim { return MatchPacks(units, r.groups, len(r.groups)) }

// groupByID finds a group by its route-wide id.
func (r *Run) groupByID(id int) (Group, bool) {
	if id < 0 || id >= len(r.groups) || r.groups[id].ID != id {
		return Group{}, false
	}
	return r.groups[id], true
}

// noteDeaths records the groups a death was seen in, so a pack whose members lie dead is clear without standing at
// it for the dwell time.
func (r *Run) noteDeaths(claim packClaim, units []Unit) {
	for gid := range r.groups {
		for _, ui := range claim.Members(gid) {
			u := units[ui]
			if u.Dead && !u.Friendly {
				r.deaths[gid] = true
			}
		}
	}
}

// liveMembers are the group's members still alive.
func (r *Run) liveMembers(gid int, units []Unit, claim packClaim) []Unit {
	var out []Unit
	for _, ui := range claim.Members(gid) {
		if u := units[ui]; !u.Dead && !u.Friendly {
			out = append(out, u)
		}
	}
	return out
}

// groupClear is the group's verdict: no live member, and either a death was seen or the commander has stood at the
// stand for 5 s without seeing one. A group out of view is never clear, and neither is a group a mob of which left a
// fight unfound: only a seen death clears it.
func (r *Run) groupClear(g Group, units []Unit, claim packClaim) bool {
	if r.lostGroups[g.ID] && !r.deaths[g.ID] {
		return false
	}
	if dist3(r.pos, g.Centre) > GroupViewYd && len(r.liveMembers(g.ID, units, claim)) == 0 {
		return r.deaths[g.ID]
	}
	if len(r.liveMembers(g.ID, units, claim)) > 0 {
		return false
	}
	if r.deaths[g.ID] {
		return true
	}
	if d, ok := r.dwell[g.ID]; ok && r.now().Sub(d) >= 5*time.Second {
		return true
	}
	return false
}

// isPatrolMember says whether a unit is the group's patrol: one of its entries, not one of the group's own members
// (the plan lists the same entry in both), and standing on the patrol's path.
func (r *Run) isPatrolMember(g Group, ui int, units []Unit, claim packClaim) bool {
	if g.Patrol == nil {
		return false
	}
	u := units[ui]
	if u.Dead || u.Friendly || !entryIn(g.Patrol.Entries, u.Entry) {
		return false
	}
	// A unit any plan group claims is a pack member, not the patrol: the plan lists the same entry in both, and a
	// patrolling spawn that stands inside a pack's radius cannot be told from the pack without spawn data.
	if _, ok := claim.GroupOf(ui); ok {
		return false
	}
	return onPath(g.Patrol.Path, u.Pos, 10)
}

// onPath says whether p lies within near yards of the patrol's polyline.
func onPath(path []Vec3, p Vec3, near float64) bool {
	if len(path) == 0 {
		return false
	}
	if len(path) == 1 {
		return dist3(path[0], p) <= near
	}
	for i := 1; i < len(path); i++ {
		if distToSegment(p, path[i-1], path[i]) <= near {
			return true
		}
	}
	return false
}

// distToSegment is the distance from p to the segment a-b in the horizontal plane; a patrol walks the ground.
func distToSegment(p, a, b Vec3) float64 {
	ax, ay := float64(a[0]), float64(a[1])
	bx, by := float64(b[0]), float64(b[1])
	px, py := float64(p[0]), float64(p[1])
	dx, dy := bx-ax, by-ay
	l := dx*dx + dy*dy
	if l == 0 {
		return dist2(p, a)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / l
	t = max(0, min(1, t))
	return dist2(Vec3{float32(px), float32(py), 0}, Vec3{float32(ax + t*dx), float32(ay + t*dy), 0})
}

// patrolInView says whether one of the group's patrol members is in view.
func (r *Run) patrolInView(g Group, units []Unit, claim packClaim) bool {
	for ui := range units {
		if r.isPatrolMember(g, ui, units, claim) && dist3(units[ui].Pos, r.pos) <= GroupViewYd {
			return true
		}
	}
	return false
}

// patrolNear is a live patrol member within PatrolClearYd of the stand or the pack.
func (r *Run) patrolNear(g Group, units []Unit, claim packClaim, stand Vec3) *Unit {
	for ui := range units {
		if !r.isPatrolMember(g, ui, units, claim) {
			continue
		}
		if dist3(units[ui].Pos, stand) <= PatrolClearYd || dist3(units[ui].Pos, g.Centre) <= PatrolClearYd {
			return &units[ui]
		}
	}
	return nil
}

// patrolGroup finds the plan group a patrol belongs to, so the driver can pull the patrol as its own fight.
func (r *Run) patrolGroup(g Group, units []Unit, claim packClaim) (Group, bool) {
	for ui := range units {
		if !r.isPatrolMember(g, ui, units, claim) {
			continue
		}
		u := units[ui]
		for _, other := range r.groups {
			if other.ID == g.ID {
				continue
			}
			if entryIn(other.Entries, u.Entry) && other.NearestPosition(u.Pos) <= float64(other.RadiusYd()) {
				return other, true
			}
		}
	}
	return Group{}, false
}

// practiceOn is the practice-revive switch; absent means on.
func (r *Run) practiceOn() bool { return r.cfg.Practice == nil || *r.cfg.Practice }

// nonEmpty is s, or fallback when s is empty.
func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
