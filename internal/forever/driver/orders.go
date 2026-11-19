package driver

// The typed orders. Every order the driver sends leaves through send, and send is the one place in the driver that
// calls the commander's Order port: it classifies the verb and applies the gate that class needs. A verb it does not
// know is refused, never sent, so a verb the module grows cannot go out ungated (TestEveryOrderVerbIsClassified).

import (
	"context"
	"errors"
	"math"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// orderClass says what one typed order would do to the bots and so which gate it needs at the send.
type orderClass int

const (
	// orderPassive never moves a bot toward danger: stay, hold, free, endpull. Always allowed (a lost link aside);
	// these are the stop's own orders, so a pending stop is not a reason to refuse them.
	orderPassive orderClass = iota
	// orderFight starts a fight: pull, attack, assist. Needs the whole pull gate's permit, taken in the same step.
	orderFight
	// orderMove can walk a bot across hostile ground: come, follow, and every movement verb the module gains. Needs
	// the movement gate (moveAllowed): no stop pending, a complete report newer than the last order, the commander's
	// position known, and a line that is clear of every plan pack not marked dead.
	orderMove
)

// orderClasses is the classification of every verb the typed layer can send today.
var orderClasses = map[string]orderClass{
	"pull":    orderFight,
	"attack":  orderFight,
	"assist":  orderFight,
	"come":    orderMove,
	"follow":  orderMove,
	"stay":    orderPassive,
	"hold":    orderPassive,
	"free":    orderPassive,
	"endpull": orderPassive,
}

// classifyOrder returns a verb's class; ok false is a verb the driver refuses to send.
func classifyOrder(verb string) (orderClass, bool) {
	c, ok := orderClasses[verb]
	return c, ok
}

// send is the one send function of the typed orders. It classifies the verb and gates it:
//
//   - a fight verb (pull, attack, assist) needs the permit from pullAllowed taken in this same step (p), and the
//     permit is re-checked here, so nothing can start a fight on a stale report;
//   - a movement verb (come, follow, any future one) needs moveAllowed;
//   - a passive verb (stay, hold, free, endpull) goes out at once, which is what the stop path needs.
//
// It returns the module's answer and why, the gate's reason when the order was not sent; a lost link ends the run
// here. Only a send that went out is recorded in the run's orders.
func (r *Run) send(ctx context.Context, who, verb, arg string, p *pullPermit) (tbcmd.OrderReport, string) {
	class, ok := classifyOrder(verb)
	if !ok {
		r.event("order_refused", map[string]any{"verb": verb, "why": "verb"})
		return tbcmd.OrderReport{}, "verb"
	}
	if r.lost {
		return tbcmd.OrderReport{}, "link"
	}
	switch class {
	case orderFight:
		if p == nil || !r.permitValid(*p) {
			return tbcmd.OrderReport{}, "stale"
		}
	case orderMove:
		if why := r.moveAllowed(who); why != "" {
			r.event("order_refused", map[string]any{"verb": verb, "bot": r.roleName(who), "why": why})
			return tbcmd.OrderReport{}, why
		}
	}
	out, why := r.sendLine(ctx, who, verb, arg, func() (sent, error) {
		rep, err := r.cmd.Order(ctx, who, verb, arg)
		return sent{order: rep}, err
	})
	if why != "" {
		return out.order, why
	}
	if class == orderFight {
		r.pullTimes = append(r.pullTimes, r.now())
	}
	return out.order, ""
}

// sent is one typed line's answer from the port: an order's report or a setup action's, with the port's error when
// the line was refused or failed.
type sent struct {
	order  tbcmd.OrderReport
	action ActionReport
	err    error
}

// sendLine is the one function every non-read-only line leaves the run through, the orders and the setup actions
// alike: it records the order in the run's log, sends through the port, counts the send for the freshness gates and
// ends the run on a lost link. The caller's own gate has already run and its class needed nothing else; a refusal
// comes back as the port's error, and the line is recorded failed.
func (r *Run) sendLine(ctx context.Context, who, verb, arg string, call func() (sent, error)) (sent, string) {
	if r.lost {
		return sent{}, "link"
	}
	out, err := call()
	out.err = err
	o := Order{At: r.sinceStart(), Who: who, Verb: verb, Arg: arg}
	if err != nil {
		o.Failed = true
	}
	r.orders = append(r.orders, o)
	r.noteSend(err)
	if errors.Is(err, ErrLinkLost) {
		r.linkLost()
		return out, "link"
	}
	if err != nil {
		return out, "error"
	}
	return out, ""
}

// moveAllowed is the gate before a movement order that can cross hostile ground. The order may go out only when:
// no stop is pending and the link is up; the commander's own position is known and no transfer is in flight; the
// last report is complete and newer than the last order (so the bot's position and the bags the line crosses are
// current); the bot is alive and on the commander's map; and the line the bot will walk — the walker's planned path
// when it returns a complete one, else the straight line — stays clear of every plan pack not marked dead by its
// radius plus MoveClearMarginYd. It returns the reason it is shut, "" when the order may go.
func (r *Run) moveAllowed(name string) string {
	if r.stopReason != "" || r.lost {
		return "stop"
	}
	if !r.selfSeen || r.between {
		return "self"
	}
	if !r.reportComplete() {
		return "report"
	}
	if !r.repAt.After(r.lastOrderAt) {
		return "stale"
	}
	names := []string{name}
	if name == "party" {
		names = r.cfg.Party // an order to the party is ordered for every member
	}
	for _, n := range names {
		b := r.bot(n)
		if b == nil {
			return "report"
		}
		if isDead(*b) || b.Map != r.mapID {
			return "map"
		}
		if b.Map != r.route.Map {
			continue // the plan's packs are on the instance map; nothing on this map can be crossed
		}
		from := Vec3{float32(b.X), float32(b.Y), float32(b.Z)}
		if !r.clearOfPacks(r.movePath(from, r.pos), MoveClearMarginYd) {
			return "pack"
		}
	}
	return ""
}

// movePath is the ground the bot would cover from its reported position to the commander: the walker's route when
// the planner returns a complete one, else the straight line between them.
func (r *Run) movePath(from, to Vec3) []Vec3 {
	if r.walk != nil {
		if route, err := r.walk.Path(from, to); err == nil && route.Kind == navmesh.RouteComplete && len(route.Points) >= 2 {
			return route.Points
		}
	}
	if dist3(from, to) == 0 {
		return []Vec3{from}
	}
	return []Vec3{from, to}
}

// clearOfPacks says whether a polyline stays clear of every plan pack not marked dead. A pack is the group's plan
// positions and the live members of it the commander has in view; it blocks the line when the line comes within the
// group's radius (the plan's, or DefaultPackRadius 25 yd) plus margin. A group the run marked cleared, or saw a death
// in, does not block.
func (r *Run) clearOfPacks(pts []Vec3, margin float64) bool {
	units := r.view()
	claim := r.claim(units)
	for gid := range r.groups {
		if r.cleared[gid] || r.deaths[gid] {
			continue
		}
		g := r.groups[gid]
		clear := float64(g.RadiusYd()) + margin
		for _, p := range g.Positions {
			if distPolyline(p, pts) <= clear {
				return false
			}
		}
		for _, ui := range claim.Members(gid) {
			u := units[ui]
			if u.Dead || u.Friendly {
				continue
			}
			if distPolyline(u.Pos, pts) <= clear {
				return false
			}
		}
	}
	return true
}

// distPolyline is the horizontal distance from p to the nearest point of a polyline; one point is that point.
func distPolyline(p Vec3, pts []Vec3) float64 {
	switch len(pts) {
	case 0:
		return math.Inf(1)
	case 1:
		return dist2(p, pts[0])
	}
	best := dist2(p, pts[0])
	for i := 1; i < len(pts); i++ {
		if d := distToSegment(p, pts[i-1], pts[i]); d < best {
			best = d
		}
	}
	return best
}

// retreatToward walks the commander one bounded step toward a straggler, on ground clear of every pack's radius
// (the margin is only for the bot's ordered line: the commander may stand near a pack, never inside its radius): the
// step is at most RetreatStepYd (20 yd), and it is taken only when the walker plans a complete route whose every
// point is outside every live pack's radius. It returns the yards walked, 0 when there is no such step.
func (r *Run) retreatToward(ctx context.Context, bot Vec3) float64 {
	if r.walk == nil {
		return 0
	}
	base := dist2(r.pos, bot)
	if base < 1 {
		return 0
	}
	step := min(RetreatStepYd, base)
	to := Vec3{
		float32(float64(r.pos[0]) + (float64(bot[0])-float64(r.pos[0]))/base*step),
		float32(float64(r.pos[1]) + (float64(bot[1])-float64(r.pos[1]))/base*step),
		r.pos[2],
	}
	route, err := r.walk.Path(r.pos, to)
	if err != nil || route.Kind != navmesh.RouteComplete || len(route.Points) < 2 {
		return 0
	}
	if !r.clearOfPacks(route.Points, 0) {
		return 0
	}
	for _, seg := range SplitRoute(route.Points, SegmentYd) {
		if err := r.walk.Walk(ctx, seg); err != nil {
			switch {
			case errors.Is(err, ErrMapChange):
				r.refreshSelf()
			case errors.Is(err, ErrLinkLost):
				r.linkLost()
			case ctx.Err() != nil:
				r.interrupted(ctx)
			default:
				r.stop("walk")
			}
			r.stopWalk()
			return 0
		}
	}
	r.refreshSelf()
	r.progress()
	return dist2(r.pos, to)
}
