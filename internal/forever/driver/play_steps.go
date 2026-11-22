package driver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// The player run's plan steps (round P4, §3.11): the four kinds a character of its own can do between a leg's pulls and
// its boss — use_object, gossip, loot_object and wait_for. The plan loader validates them; playStep reads them into the
// route's own shapes and refuses, by plan path, what this round cannot run (an item's use, a ritual, an area trigger, a
// chest's expect_item, a gossip by its text line, an object named by its spawn). The machine then runs one step per
// STEPS visit, so a pack in the way and the fight it brings interleave exactly as they do for a walk.
//
// The numbers are the spec's: 10 s for an object's state to change after a use, the plan's own retry_s for a menu that
// lacks its option, the plan's timeout_s for a wait.

// StateSteps is the state between a leg's packs and its boss: one of the leg's steps, in plan order.
const StateSteps = "STEPS"

// playGossipByNote is what a player run says about a gossip step's `by`: the agent talks itself, and bot-commands v1 has
// no order for a bot's talk, so a plan's carrier or tank is noted once instead of dropped silently (D-8's rule).
const playGossipByNote = "the gossip steps are the agent's own talk in player runs v1: a plan's by (a bot's order) is not run"

const (
	// PlayStepStandYd is how near an object or an NPC the character stands when the plan gives no stand point: inside
	// the client's use and gossip range without walking into the thing.
	PlayStepStandYd = 4.0
	// PlayObjectUseWait is how long a use_object waits for the object's state to change (§3.11, 10 s).
	PlayObjectUseWait = 10 * time.Second
)

// Step is one scripted step of a leg as a player run reads it: exactly one of the four kinds is set.
type Step struct {
	Use    *UseStep
	Loot   *LootStep
	Gossip *GossipStep
	Wait   *WaitStep
}

// Kind is the step's plan name, for the record.
func (s Step) Kind() string {
	switch {
	case s.Use != nil:
		return "use_object"
	case s.Loot != nil:
		return "loot_object"
	case s.Gossip != nil:
		return "gossip"
	case s.Wait != nil:
		return "wait_for"
	}
	return "step"
}

// ObjectAt names a game object: its entry, the point nearest it the plan gives (HasNear false when it gives none), the
// search radius around that point and the stand point (HasStand false when the plan gives none).
type ObjectAt struct {
	Entry    uint32
	Near     Vec3
	HasNear  bool
	Range    float32
	Stand    Vec3
	HasStand bool
}

// UseStep is use_object: walk to the stand, use the object, wait for its state to change.
type UseStep struct{ ObjectAt }

// LootStep is loot_object: walk to the stand, open the object's window and take everything in it.
type LootStep struct{ ObjectAt }

// GossipStep is gossip: talk to the NPC and pick one option of its menu. By is the plan's talker ("agent" or a bot's
// role); a player run talks itself and notes the difference once.
type GossipStep struct {
	NPC      uint32
	Near     Vec3
	HasNear  bool
	Option   int
	By       string
	Stand    Vec3
	HasStand bool
	Retry    time.Duration
}

// WaitStep is wait_for: the plan's one condition, up to its timeout.
type WaitStep struct {
	Object   *WaitObject
	Gone     *WaitGone
	Unit     *WaitUnit
	Timeout  time.Duration
	FailText string
}

// WaitObject waits for a game object's state. GoneOK counts the object missing as the wanted state once the run saw it
// (never for "ready": a gone object is not closed).
type WaitObject struct {
	ObjectAt
	State  string
	GoneOK bool
}

// WaitGone waits until no living hostile unit stands within Radius yards of Centre (the agent's own spot when
// HasCentre is false).
type WaitGone struct {
	Radius    float32
	Centre    Vec3
	HasCentre bool
}

// WaitUnit waits for a creature: any unit of Entries within Radius yards of Near (anywhere the client sees it, without
// Near).
type WaitUnit struct {
	Entries []uint32
	Near    Vec3
	HasNear bool
	Radius  float32
	State   string
}

// radius is the object's search radius in yards: the plan's or the loader's default.
func (o ObjectAt) radius() float64 {
	if o.Range > 0 {
		return float64(o.Range)
	}
	return dungeon.DefaultObjectRange
}

// retry is how long a menu that lacks the option is asked again: the plan's retry_s, its default when 0.
func (g *GossipStep) retry() time.Duration {
	if g.Retry > 0 {
		return g.Retry
	}
	return dungeon.DefaultGossipRetryS * time.Second
}

// playStep reads one plan step into the route's shape for a player run. What this round cannot run is refused by its
// plan path, never dropped: a plan the route accepts is one the machine means to finish.
func playStep(where string, s dungeon.Step) (Step, []error) {
	var errs []error
	refuse := func(field, why string) { errs = append(errs, &PlanRefusal{Where: where, Field: field, Why: why}) }
	set := 0
	for _, on := range []bool{s.UseObject != nil, s.LootObject != nil, s.Gossip != nil, s.WaitFor != nil,
		s.UseItem != nil, s.Ritual != nil, s.AreaTrigger != nil} {
		if on {
			set++
		}
	}
	if set > 1 {
		return Step{}, []error{&PlanRefusal{Where: where, Field: "step", Why: "a step names one kind, not several"}}
	}
	switch {
	case s.UseObject != nil:
		u := s.UseObject
		if u.Entry == 0 {
			refuse("use_object.entry", "a use_object names its object by entry")
		}
		if u.Spawn != 0 {
			refuse("use_object.spawn", "an object named by its spawn is the remote run's readback, not a player run's")
		}
		if u.ViaItem {
			refuse("use_object.via_item", "an item cast on the object is not run by this round")
		}
		if u.RequiresItem != nil {
			refuse("use_object.requires_item", "the character's bags are not read by this round")
		}
		if u.Proof != nil {
			refuse("use_object.proof", "a use whose proof is another object or creature is not read by this round: the state readback would fail it")
		}
		return Step{Use: &UseStep{ObjectAt: objectAt(u.ObjectRef, u.Stand)}}, errs
	case s.LootObject != nil:
		l := s.LootObject
		if l.Entry == 0 {
			refuse("loot_object.entry", "a loot_object names its object by entry")
		}
		if l.Spawn != 0 {
			refuse("loot_object.spawn", "an object named by its spawn is the remote run's readback, not a player run's")
		}
		if l.ExpectItem != nil {
			refuse("loot_object.expect_item", "the character's bags are not read by this round")
		}
		if l.Batch != "" {
			refuse("loot_object.batch", "a batch of chests is not run by this round")
		}
		return Step{Loot: &LootStep{ObjectAt: objectAt(l.ObjectRef, l.Stand)}}, errs
	case s.Gossip != nil:
		g := s.Gossip
		if g.NPC == 0 {
			refuse("gossip.npc", "a gossip names its NPC by entry")
		}
		if g.Option == nil {
			refuse("gossip.text", "a gossip by its text line needs the menu, which a player run does not read: name the option")
		}
		st := &GossipStep{NPC: g.NPC, By: g.Talker(), Retry: time.Duration(g.RetryS) * time.Second}
		if g.Option != nil {
			st.Option = int(*g.Option)
		}
		st.Near, st.HasNear = vec(g.Near), true
		if g.Stand != nil {
			st.Stand, st.HasStand = vec(*g.Stand), true
		}
		return Step{Gossip: st}, errs
	case s.WaitFor != nil:
		w := s.WaitFor
		st := &WaitStep{Timeout: time.Duration(w.TimeoutS) * time.Second, FailText: w.FailText}
		if st.Timeout <= 0 {
			st.Timeout = dungeon.DefaultTimeoutS * time.Second
		}
		switch {
		case w.GameObject != nil:
			o := w.GameObject
			if o.Entry == 0 {
				refuse("wait_for.gameobject.entry", "a game object wait names its object by entry")
			}
			if o.Spawn != 0 {
				refuse("wait_for.gameobject.spawn", "an object named by its spawn is the remote run's readback")
			}
			if !slices.Contains(dungeon.ObjectStates, o.State) {
				refuse("wait_for.gameobject.state", fmt.Sprintf("%q is none of %v", o.State, dungeon.ObjectStates))
			}
			st.Object = &WaitObject{ObjectAt: objectAt(o.ObjectRef, nil), State: o.State, GoneOK: o.GoneOK}
		case w.HostilesGone != nil:
			h := w.HostilesGone
			if h.Radius <= 0 {
				refuse("wait_for.hostiles_gone.radius", "a hostiles_gone wait names the radius it reads")
			}
			st.Gone = &WaitGone{Radius: h.Radius}
			if h.Center != nil {
				st.Gone.Centre, st.Gone.HasCentre = vec(*h.Center), true
			}
		case w.Unit != nil:
			u := w.Unit
			if len(u.Entries) == 0 {
				refuse("wait_for.unit.entries", "a unit wait names the entries it reads")
			}
			if !slices.Contains(dungeon.UnitStates, u.State) {
				refuse("wait_for.unit.state", fmt.Sprintf("%q is none of %v", u.State, dungeon.UnitStates))
			}
			st.Unit = &WaitUnit{Entries: append([]uint32(nil), u.Entries...), Radius: u.Radius, State: u.State}
			if u.Near != nil {
				st.Unit.Near, st.Unit.HasNear = vec(*u.Near), true
			}
		default:
			refuse("wait_for", "a wait names one condition: a game object, hostiles gone or a unit")
		}
		return Step{Wait: st}, errs
	case s.UseItem != nil:
		refuse("use_item", "using an item is not run by this round")
	case s.Ritual != nil:
		refuse("ritual", "a ritual is not run by this round")
	case s.AreaTrigger != nil:
		refuse("area_trigger", "an area trigger is not run by this round")
	default:
		refuse("step", "a step names none of use_object, loot_object, gossip, wait_for")
	}
	return Step{}, errs
}

// objectAt reads an object reference and its stand point into the route's shape.
func objectAt(ref dungeon.ObjectRef, stand *dungeon.Point) ObjectAt {
	o := ObjectAt{Entry: ref.Entry, Range: ref.Range}
	if ref.Near != nil {
		o.Near, o.HasNear = vec(*ref.Near), true
	}
	if stand != nil {
		o.Stand, o.HasStand = vec(*stand), true
	}
	return o
}

// steps runs the leg's next step: one visit per step, so a pack in the way (StatePull) or a stop interleaves it the way
// it does for a walk, and a step a pack interrupted is begun again when the fight is over.
func (p *playRun) steps(ctx context.Context) string {
	leg := p.leg()
	if leg == nil || p.stepIdx >= len(leg.Steps) {
		return StateLegWalk
	}
	st := leg.Steps[p.stepIdx]
	kind := st.Kind()
	started := p.now()
	p.rec.Event(started, "step", map[string]any{"leg": leg.Name, "index": p.stepIdx, "kind": kind})
	var next string
	switch {
	case st.Use != nil:
		next = p.stepUse(ctx, st.Use)
	case st.Loot != nil:
		next = p.stepLoot(ctx, st.Loot)
	case st.Gossip != nil:
		next = p.stepGossip(ctx, st.Gossip)
	case st.Wait != nil:
		next = p.stepWait(ctx, st.Wait)
	}
	if next == StateLegWalk {
		p.rec.Event(p.now(), "step_done", map[string]any{"leg": leg.Name, "index": p.stepIdx, "kind": kind,
			"seconds": p.now().Sub(started).Seconds()})
		p.stepIdx++
	}
	return next
}

// stepUse is use_object: the character walks to the stand, uses the object and the step is done only once the object's
// state changed (§3.11, 10 s); a use that changes nothing stops the run instead of claiming the step.
func (p *playRun) stepUse(ctx context.Context, st *UseStep) string {
	stand, why := p.stepStand(st.ObjectAt)
	if why != "" {
		return p.stepFail("use_object", why)
	}
	p.walkTo(ctx, stand, false, false)
	if next, ok := p.stepArrived(); !ok {
		return next
	}
	p.refreshSelf()
	obj, seen := p.findObject(st.ObjectAt)
	if !seen {
		return p.stepFail("use_object", fmt.Sprintf("no object %d stands within %.0f yd of its point", st.Entry, st.radius()))
	}
	before := obj.State
	if err := p.ports.Player.UseObject(ctx, st.Entry, stand); err != nil {
		p.playerErr(err, "step:use_object")
		return ""
	}
	p.rec.Event(p.now(), "step_use", map[string]any{"entry": st.Entry, "state": before})
	deadline := p.now().Add(PlayObjectUseWait)
	for {
		if obj, ok := p.findObject(st.ObjectAt); ok && obj.State != before {
			return StateLegWalk
		}
		if p.timeUp() {
			p.stop("time")
			return ""
		}
		if !p.now().Before(deadline) {
			return p.stepFail("use_object", fmt.Sprintf("the object %d changed no state within %v", st.Entry, PlayObjectUseWait))
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return ""
		}
	}
}

// stepLoot is loot_object: the character walks to the stand and opens the object's window by its guid, taking what is
// in it (the window's rolls answered greed, D-9; the port owns the taking).
func (p *playRun) stepLoot(ctx context.Context, st *LootStep) string {
	stand, why := p.stepStand(st.ObjectAt)
	if why != "" {
		return p.stepFail("loot_object", why)
	}
	p.walkTo(ctx, stand, false, false)
	if next, ok := p.stepArrived(); !ok {
		return next
	}
	obj, seen := p.findObject(st.ObjectAt)
	if !seen {
		return p.stepFail("loot_object", fmt.Sprintf("no object %d stands within %.0f yd of its point", st.Entry, st.radius()))
	}
	switch err := p.ports.Player.Loot(ctx, obj.GUID); {
	case err == nil:
		p.rec.Event(p.now(), "step_loot", map[string]any{"entry": st.Entry})
		return StateLegWalk
	case errors.Is(err, ErrCannotLoot):
		return p.stepFail("loot_object", fmt.Sprintf("the character may not loot the object %d", st.Entry))
	default:
		p.playerErr(err, "step:loot_object")
		return ""
	}
}

// stepGossip is gossip: the character talks to the NPC itself (a plan's by is noted once at load, playGossipByNote) and
// picks the plan's option. A menu without the option is asked again for retry_s, then the step fails; a dead NPC fails
// it at once (§3.11; dead_is_done is plan-export's, not the agent's driver's).
func (p *playRun) stepGossip(ctx context.Context, st *GossipStep) string {
	p.refreshSelf()
	npc, seen := p.creatureNear(st.NPC, st.Near, st.HasNear)
	if !seen {
		return p.stepFail("gossip", fmt.Sprintf("no creature %d is in view", st.NPC))
	}
	if npc.Dead {
		return p.stepFail("gossip", fmt.Sprintf("the NPC %d is dead", st.NPC))
	}
	stand := st.Stand
	if !st.HasStand {
		stand = standOff(p.self, npc.Pos)
	}
	p.walkTo(ctx, stand, false, false)
	if next, ok := p.stepArrived(); !ok {
		return next
	}
	deadline := p.now().Add(st.retry())
	for {
		switch err := p.ports.Player.Gossip(ctx, st.NPC, st.Option); {
		case err == nil:
			p.rec.Event(p.now(), "gossip", map[string]any{"npc": st.NPC, "option": st.Option})
			return StateLegWalk
		case errors.Is(err, ErrNoGossipOption):
			if p.timeUp() {
				p.stop("time")
				return ""
			}
			if !p.now().Before(deadline) {
				return p.stepFail("gossip", fmt.Sprintf("the menu of %d lacked option %d for %v", st.NPC, st.Option, st.retry()))
			}
			if !p.sleep(ctx, p.lim.Poll) {
				return ""
			}
		default:
			p.playerErr(err, "step:gossip")
			return ""
		}
	}
}

// stepWait is wait_for: the plan's condition polled to its timeout, then the run stops with the step's reason and the
// plan's fail_text.
func (p *playRun) stepWait(ctx context.Context, st *WaitStep) string {
	seen := false
	deadline := p.now().Add(st.Timeout)
	for {
		if p.waitHolds(st, &seen) {
			p.rec.Event(p.now(), "wait_done", map[string]any{"leg": p.legName(), "index": p.stepIdx})
			return StateLegWalk
		}
		if p.timeUp() {
			p.stop("time")
			return ""
		}
		if !p.now().Before(deadline) {
			why := fmt.Sprintf("the wait did not come within %v", st.Timeout)
			if st.FailText != "" {
				why += ": " + st.FailText
			}
			return p.stepFail("wait_for", why)
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return ""
		}
	}
}

// waitHolds reads the plan's wait once: a game object's state (with gone_ok once the run saw it), no living hostile
// within a radius, or a creature's state.
func (p *playRun) waitHolds(st *WaitStep, seen *bool) bool {
	p.refreshSelf()
	switch {
	case st.Object != nil:
		obj, ok := p.findObject(st.Object.ObjectAt)
		if !ok {
			return st.Object.GoneOK && *seen && st.Object.State != "ready"
		}
		*seen = true
		return obj.State == st.Object.State
	case st.Gone != nil:
		centre := p.self
		if st.Gone.HasCentre {
			centre = st.Gone.Centre
		}
		for _, u := range p.ports.World.Units() {
			if u.Dead || u.Friendly {
				continue
			}
			if dist3(u.Pos, centre) <= float64(st.Gone.Radius) {
				return false
			}
		}
		return true
	case st.Unit != nil:
		return unitWaitHolds(p.ports.World.Units(), *st.Unit)
	}
	return true
}

// unitWaitHolds says whether a unit wait holds in the character's view: the four states are §3.11's (and the module's):
// a `dead` wait also wants none of the entries alive, and `gone` none of them at all.
func unitWaitHolds(units []Unit, w WaitUnit) bool {
	radius := float64(w.Radius)
	if radius <= 0 {
		radius = dungeon.DefaultUnitRadius
	}
	var alive, hostile, dead bool
	for _, u := range units {
		if !slices.Contains(w.Entries, u.Entry) {
			continue
		}
		if w.HasNear && dist3(u.Pos, w.Near) > radius {
			continue
		}
		if u.Dead {
			dead = true
			continue
		}
		alive = true
		if !u.Friendly {
			hostile = true
		}
	}
	switch w.State {
	case dungeon.UnitSeen:
		return alive
	case dungeon.UnitHostile:
		return hostile
	case dungeon.UnitDead:
		return dead && !alive
	case dungeon.UnitGone:
		return !alive && !dead
	}
	return false
}

// stepStand is where the character stands for an object step: the plan's stand, else PlayStepStandYd short of the object
// on the line to where it stands now, else short of the plan's near point. why is set when neither the plan nor the view
// names a point to walk to.
func (p *playRun) stepStand(o ObjectAt) (Vec3, string) {
	if o.HasStand {
		return o.Stand, ""
	}
	p.refreshSelf()
	if obj, ok := p.findObject(o); ok {
		return standOff(p.self, obj.Pos), ""
	}
	if o.HasNear {
		return standOff(p.self, o.Near), ""
	}
	return Vec3{}, "the object is in no view and the plan names no point to walk to"
}

// stepArrived is how a step's walk ended: ok with no next state when the character stands on the point, no next state
// when the run stopped, and StatePull when a pack in the way comes first (the step begins again after the fight).
func (p *playRun) stepArrived() (string, bool) {
	switch {
	case p.stopReason != "":
		return "", false
	case p.pack >= 0:
		return StatePull, false
	}
	return "", true
}

// findObject is the step's object in the character's view: the nearest object of its entry to the plan's point (to the
// character when the plan gives none), inside the plan's range of that point. A step's object never stands further.
func (p *playRun) findObject(o ObjectAt) (PlayObject, bool) {
	best, bestD := PlayObject{}, math.Inf(1)
	for _, obj := range p.ports.Player.Objects() {
		if obj.Entry != o.Entry {
			continue
		}
		at := p.self
		if o.HasNear {
			at = o.Near
			if d := dist3(obj.Pos, o.Near); d > o.radius() {
				continue
			}
		}
		if d := dist3(obj.Pos, at); d < bestD {
			best, bestD = obj, d
		}
	}
	return best, bestD < math.Inf(1)
}

// creatureNear is the nearest unit of that entry in the character's view (to the plan's point when it gives one): the
// NPC a gossip step talks to. A corpse is returned too, so the step fails on it instead of looking again.
func (p *playRun) creatureNear(entry uint32, near Vec3, hasNear bool) (Unit, bool) {
	best, bestD := Unit{}, math.Inf(1)
	for _, u := range p.ports.World.Units() {
		if u.Entry != entry {
			continue
		}
		at := p.self
		if hasNear {
			at = near
		}
		if d := dist3(u.Pos, at); d < bestD {
			best, bestD = u, d
		}
	}
	return best, bestD < math.Inf(1)
}

// standOff is the point PlayStepStandYd from an object or an NPC on the line to where the character stands: the plan's
// own stand when it gives one (stepStand asks first), else a foot at the client's use range instead of the centre.
func standOff(from, at Vec3) Vec3 {
	d := float32(dist3(from, at))
	if d <= PlayStepStandYd {
		return from
	}
	t := PlayStepStandYd / d
	return Vec3{at[0] + (from[0]-at[0])*t, at[1] + (from[1]-at[1])*t, at[2] + (from[2]-at[2])*t}
}

// stepFail ends the run for a step it could not do, recording what a reader needs: the leg, the step, its kind and the
// reason.
func (p *playRun) stepFail(kind, why string) string {
	p.rec.Event(p.now(), "step_failed", map[string]any{"leg": p.legName(), "index": p.stepIdx, "kind": kind, "text": why})
	p.stop("step:" + kind)
	return ""
}
