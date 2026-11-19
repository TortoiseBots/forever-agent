package world

// forever fight-check: the target choice, the spell plan and the report of round 4's command. The fight itself is
// Attack/Cast in fight.go; this file decides what to fight, which of the character's spells to use, and what the
// run prints.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// targetSearchRange is the brief's 40 yards.
	targetSearchRange = 40
	// targetBandBelow and targetBandAbove size the default level band relative to the character's own level: three
	// levels below to two above, clamped to levelFloor-levelCeil. A level-1 character therefore looks for levels
	// 1-3, the round-4 brief's window; a level-52 one for 49-54.
	targetBandBelow = 3
	targetBandAbove = 2
	// levelFloor and levelCeil are the client's level range (1.60.1).
	levelFloor = 1
	levelCeil  = 60
	// creatureTypeCritter is the creature type a critter has (SharedDefines CREATURE_TYPE_CRITTER = 8); the client
	// does not attack one by accident.
	creatureTypeCritter = 8
	// ragePowerType is Powers's POWER_RAGE (SharedDefines.h:292-300); the warrior abilities below cost rage.
	ragePowerType = 1
)

// hostileSpells are the single-target abilities a plan may cast, in preference order, with the range window each is
// cast in. The ids are game constants; the windows are conservatively inside each spell's own (Charge's is 8-25
// yards, the melee abilities' is melee reach). The client has no Spell.dbc, so these are stated, not derived
// (COMBAT-R1-NOTES.md, "What is approximated").
var hostileSpells = []struct {
	id     int32
	window RangeWindow
	needs  string
}{
	{78, RangeWindow{0, 5}, "Heroic Strike (next swing, rage)"},
	{772, RangeWindow{0, 5}, "Rend (bleed, rage)"},
	{845, RangeWindow{0, 5}, "Cleave (rage)"},
	{1464, RangeWindow{0, 5}, "Slam (rage)"},
	{1715, RangeWindow{0, 5}, "Hamstring (rage)"},
	{100, RangeWindow{8, 25}, "Charge (8-25 yd, out of combat)"},
}

// selfSpells are the self casts a plan may use, in preference order, with the range window each is cast in. A self
// spell has no target block (the server resolves the caster, Spell.cpp:104-110).
var selfSpells = []struct {
	id     int32
	window RangeWindow
	needs  string
}{
	{6673, RangeWindow{0, 0}, "Battle Shout (rage, self)"},
	{2457, RangeWindow{0, 0}, "Battle Shout rank 2 (rage, self)"},
	{71, RangeWindow{0, 0}, "Defensive Stance (self)"},
}

// spellPlan is what the fight-check will cast, chosen from the spells the server said the character knows.
type spellPlan struct {
	Hostile       int32
	HostileWindow RangeWindow
	HostileNote   string
	Self          int32
	SelfNote      string
}

// planSpells picks the first known spell of each list. Nothing is cast when the list is empty.
func planSpells(known []int32) spellPlan {
	has := func(id int32) bool {
		for _, k := range known {
			if k == id {
				return true
			}
		}
		return false
	}
	var plan spellPlan
	for _, s := range hostileSpells {
		if has(s.id) {
			plan.Hostile, plan.HostileWindow, plan.HostileNote = s.id, s.window, s.needs
			break
		}
	}
	for _, s := range selfSpells {
		if has(s.id) {
			plan.Self, plan.SelfNote = s.id, s.needs
			break
		}
	}
	return plan
}

// targetWalkRange is how far the fallback walk may look for a target when none is within the fight's 40 yards.
const targetWalkRange = 200

// fightCheck is round 4's command: settle, look at the world, choose a target, fight it with the spells the
// character really knows, report and let Enter log out.
func (s *session) fightCheck(ctx context.Context) error {
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 8 * time.Second
	}
	// The settle is the first thing to read the world, and it ends at once if the character dies during it: nothing
	// optional (a ping, a name query) is sent for a corpse, and the run exits with the dead code.
	if err := s.settle(ctx, settle); err != nil {
		return err
	}
	x, y, z := s.report.WalkClientPos[0], s.report.WalkClientPos[1], s.report.WalkClientPos[2]
	if err := s.linkDead(); err != nil {
		return err
	}
	if s.selfDead() {
		s.opts.logf("fight-check: the character is dead; a corpse run is not part of this round")
		return ErrSelfDead
	}
	s.sendPing()
	s.queryNames(ctx)
	if err := s.linkDead(); err != nil {
		return err
	}
	s.summarise(x, y, z)
	// A player at low health first walks out of reach of what is on it and rests; anything that attacks during the
	// rest is fought back.
	if s.mover != nil {
		s.retreat(ctx)
		for range 3 {
			attacker := s.rest(ctx, 0.5, restLimit)
			if s.selfDead() {
				// Nothing is sent for a corpse but the logout: no more rests, no target, no fight.
				return ErrSelfDead
			}
			if attacker.IsZero() {
				break
			}
			s.opts.logf("fight-check: %s attacked the character while it rested; fighting back", s.fight.name(attacker))
			err := s.Attack(ctx, attacker)
			if errors.Is(err, ErrSelfDead) {
				return err // dead in the defensive fight: stop at once
			}
			if err != nil && !errors.Is(err, ErrCannotAttack) && !errors.Is(err, context.Canceled) {
				s.opts.logf("fight-check: the defensive fight ended: %v", err)
			}
		}
		// The retreat and the defensive fight moved the character: the candidates are chosen from where it is now.
		x, y, z = s.MoverX(), s.MoverY(), s.MoverZ()
	}

	// The level band the geometric filter accepts: explicit flags first, else the character's own level (stated on
	// fightLevelWindow). It is printed because a run that finds nothing must say which levels it looked for.
	levelMin, levelMax := fightLevelWindow(s.opts.LevelMin, s.opts.LevelMax, s.selfLevel())
	s.opts.logf("fight-check: target band level %d-%d within %d yd (the walk looks to %d yd)", levelMin, levelMax, targetSearchRange, targetWalkRange)

	// A debug step before the fight (--provoke): the refusals whose real packets a normal fight does not produce.
	if s.opts.Provoke && s.mover != nil {
		s.provokeRefusals(ctx, attackableCandidates(s.world.Units(x, y, z), x, y, z, s.self, s.opts.Entry, targetWalkRange, levelMin, levelMax))
	}

	// The candidates: every living unit the geometric filter accepts, nearest first, out to the fallback range.
	// The server has the last word: attacking a friendly unit is answered with an immediate SMSG_ATTACK_STOP, and
	// then the next candidate is tried. That is how the client learns what the faction table would have said.
	candidates := attackableCandidates(s.world.Units(x, y, z), x, y, z, s.self, s.opts.Entry, targetWalkRange, levelMin, levelMax)
	if len(candidates) == 0 {
		s.opts.logf("fight-check: no living creature of level %d-%d within %d yd (the walk looked to %d yd)",
			levelMin, levelMax, targetSearchRange, targetWalkRange)
		if s.opts.Entry != 0 {
			s.opts.logf("fight-check: --entry %d did not match a creature in range", s.opts.Entry)
		}
		return nil
	}
	plan := planSpells(s.known)
	s.opts.logf("fight-check: the character knows %d spell(s)%s", len(s.known), spellListText(s.known))
	switch {
	case plan.Hostile != 0 && plan.Self != 0:
		s.opts.logf("fight-check: plan: %s, then %s", plan.HostileNote, plan.SelfNote)
	case plan.Hostile != 0:
		s.opts.logf("fight-check: plan: %s", plan.HostileNote)
	case plan.Self != 0:
		s.opts.logf("fight-check: plan: %s", plan.SelfNote)
	default:
		s.opts.logf("fight-check: plan: melee only (no usable spell in the known list)")
	}

	link := s.transport()
	for attempt, candidate := range candidates {
		if attempt >= maxTargetAttempts {
			s.opts.logf("fight-check: %d candidates refused; giving up", maxTargetAttempts)
			break
		}
		// A refused candidate leaves its events behind: the next one starts a fresh fight log.
		s.fight = &fightLog{started: link.Now(), now: link.Now, self: s.self}
		target := candidate
		// The target's position now, not the coordinates of the last update it carried: a unit on a spline is not
		// where its create block put it.
		now := link.Now()
		tx, ty, tz := target.Position(now)
		dist := objects.Distance(tx, ty, tz, s.MoverX(), s.MoverY(), s.MoverZ())
		s.opts.logf("fight-check: target %s entry %d level %d health %d/%d at %.1f yd",
			objects.TypeName(target.Type), target.Info.Entry, target.Info.Level, target.Info.Health,
			target.Info.MaxHealth, dist)
		if name := s.world.Name(target); name != "" {
			s.opts.logf("fight-check: it is %s", name)
		}
		// Out of the 40 yards the brief names: walk toward it first, straight on foot.
		if dist > targetSearchRange && s.mover != nil {
			tx, ty, tz := target.Position(link.Now())
			px, py, pz := approachPoint(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz, meleeReach(s.selfObject(), target), 1)
			s.opts.logf("fight-check: walking to it (%.1f yd)", dist)
			if err := s.mover.MoveTo(ctx, px, py, pz); err != nil {
				s.opts.logf("fight-check: the approach walk stopped: %v", err)
			}
		}
		// Charge has a minimum range: walk to a chargeable distance first, exactly as a player would.
		spentPlan := plan
		if spentPlan.Hostile != 0 && spentPlan.HostileWindow.Min > 0 && s.mover != nil {
			tx, ty, tz := target.Position(link.Now())
			d := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz)
			if d > spentPlan.HostileWindow.Max {
				mid := spentPlan.HostileWindow.Max * 0.6
				px, py, pz := approachPoint(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz, mid, 1)
				s.opts.logf("fight-check: walking to %.1f yd for the spell (it needs %.0f-%.0f yd)",
					mid, spentPlan.HostileWindow.Min, spentPlan.HostileWindow.Max)
				if err := s.mover.MoveTo(ctx, px, py, pz); err != nil {
					s.opts.logf("fight-check: the approach walk stopped: %v", err)
				}
			}
			if result, err := s.Cast(ctx, spentPlan.Hostile, target.GUID, spentPlan.HostileWindow); err != nil {
				s.opts.logf("fight-check: the ranged spell did not happen: %v (%s)", err, result)
			} else {
				s.opts.logf("fight-check: spell %d: %s", spentPlan.Hostile, result)
				spentPlan.Hostile = 0 // cast once; the plan is not a rotation
			}
		}
		start := link.Now()
		err := s.Attack(ctx, target.GUID, s.castBetweenSwings(&spentPlan, target.GUID))
		fought := link.Now().Sub(start)
		if errors.Is(err, ErrCannotAttack) {
			s.opts.logf("fight-check: the server will not let this character attack entry %d; trying the next one",
				target.Info.Entry)
			continue
		}
		if errors.Is(err, ErrTargetEvaded) {
			s.opts.logf("fight-check: the target got away: the server stopped the attack with it alive")
			s.report.Fight.Evaded = true
		}
		s.fillFightReport()
		s.printFightReport(fought)
		if errors.Is(err, ErrSelfDead) {
			// Nothing more is sent for a corpse: no retreat, no rest, no next candidate — only the logout.
			return err
		}
		// Do not log out at a sliver of health: a player walks out of the fight and rests first (and the run must
		// not leave the character one hit from death).
		if s.mover != nil {
			s.retreat(ctx)
			s.rest(ctx, 0.5, restLimit)
		}
		if s.selfDead() {
			return ErrSelfDead
		}
		if err != nil && s.fight.killed {
			return nil // the kill makes a deadline error irrelevant
		}
		if errors.Is(err, ErrTargetEvaded) {
			// Its own outcome and its own exit code: the target lived and the server ended the fight.
			return fmt.Errorf("world: the target got away: %w", err)
		}
		return err
	}
	s.opts.logf("fight-check: no target could be attacked")
	return nil
}

// provokeRefusals is the --provoke debug step (forever fight-check --provoke --capture-dir DIR). It is test tooling,
// never part of the fight's plan, and it sends only packets a player's client sends:
//
//  1. a self cast of the plan's self spell at whatever rage the character has: at 0 rage the server refuses it with
//     "no power" (SMSG_CAST_FAILED, Spell::CheckCast's SPELL_FAILED_NO_POWER), the cast-failed form;
//  2. the same cast again at once, for the "not ready"/global-cooldown refusal when the first one went off
//     (Player::CanRequestSpellCast / Spell::CheckCast);
//  3. a real attack on the nearest candidate from outside melee reach, kept on for a few server swing ticks: each
//     tick answers "not in range" (Unit::DoMeleeAttackIfReady, Unit.cpp:2251-2257, SMSG_ATTACK_SWING_ERROR), and the
//     client then stops the attack the ordinary way.
func (s *session) provokeRefusals(ctx context.Context, candidates []*objects.Object) {
	plan := planSpells(s.known)
	if plan.Self != 0 {
		for i := 0; i < 2; i++ {
			result, err := s.Cast(ctx, plan.Self, wire.EmptyGUID, RangeWindow{})
			if err != nil {
				s.opts.logf("fight-check: provoke: the self cast did not happen: %v", err)
				break
			}
			s.opts.logf("fight-check: provoke: spell %d at %s rage: %s", plan.Self,
				combat.PowerText(ragePowerType, s.powerOfType(ragePowerType), s.powerMaxOfType(ragePowerType)), result)
			if result != "success" {
				break // the second try is only for the cooldown answer after a success
			}
		}
	}
	if len(candidates) == 0 {
		s.opts.logf("fight-check: provoke: no candidate in reach of the walk, so no out-of-range swing")
		return
	}
	target := candidates[0]
	tx, ty, tz := target.Position(s.transport().Now())
	reach := meleeReach(s.selfObject(), target)
	dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz)
	if dist <= reach+1 {
		// Walk straight away from it: the swing must start outside the server's reach.
		dx, dy := s.MoverX()-tx, s.MoverY()-ty
		if d := float32(math.Hypot(float64(dx), float64(dy))); d > 0.1 {
			dx, dy = dx/d, dy/d
		} else {
			dx, dy = 1, 0
		}
		if err := s.mover.MoveTo(ctx, s.MoverX()+dx*15, s.MoverY()+dy*15, s.MoverZ()); err != nil {
			s.opts.logf("fight-check: provoke: the walk away stopped: %v", err)
		}
	}
	link := s.transport()
	if err := link.Send(wire.CMSGSetSelection, combat.BuildSetSelection(target.GUID)); err != nil {
		s.opts.logf("fight-check: provoke: CMSG_SET_SELECTION: %v", err)
		return
	}
	if err := link.Send(wire.CMSGAttackSwing, combat.BuildAttackSwing(target.GUID)); err != nil {
		s.opts.logf("fight-check: provoke: CMSG_ATTACK_SWING: %v", err)
		return
	}
	before := s.fight.swingErrSeq
	for i := 0; i < 4; i++ {
		if err := link.WaitUntil(ctx, link.Now().Add(700*time.Millisecond)); err != nil {
			return
		}
	}
	s.opts.logf("fight-check: provoke: %d swing error(s) from %0.1f yd away", int(s.fight.swingErrSeq-before), dist)
	if err := link.Send(wire.CMSGAttackStop, combat.BuildAttackStop()); err != nil {
		s.opts.logf("fight-check: provoke: CMSG_ATTACK_STOP: %v", err)
		return
	}
	_ = link.WaitUntil(ctx, link.Now().Add(attackStopWait))
}

// retreat walks 25 yards away from the unit that is attacking the character, else from the nearest unit within 20
// yards, when the character is below half health: a player at a sliver of health does not stand in a fight.
func (s *session) retreat(ctx context.Context) {
	cs, ok := s.world.Combat(s.self)
	if !ok || cs.MaxHealth <= 0 || float32(cs.Health) >= 0.5*float32(cs.MaxHealth) {
		return
	}
	units := s.world.Units(s.MoverX(), s.MoverY(), s.MoverZ())
	var threat *objects.Object
	for _, u := range units {
		if ucs, ok := s.world.Combat(u.GUID); ok && ucs.Attacking == s.self {
			threat = u
			break
		}
	}
	if threat == nil {
		for _, u := range units {
			if u.GUID == s.self || u.Dead || u.Type != objects.TypeUnit || u.Info.NpcFlags != 0 {
				continue
			}
			if objects.Distance(u.X, u.Y, u.Z, s.MoverX(), s.MoverY(), s.MoverZ()) <= 20 {
				threat = u
				break
			}
		}
	}
	if threat == nil {
		return
	}
	tx, ty, _ := threat.Position(s.transport().Now())
	dx, dy := s.MoverX()-tx, s.MoverY()-ty
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
	if dist < 0.1 {
		dx, dy, dist = 1, 0, 1
	}
	ex, ey := s.MoverX()+dx/dist*25, s.MoverY()+dy/dist*25
	s.opts.logf("fight-check: at %d/%d health, walking %.0f yd away from the unit on it", cs.Health, cs.MaxHealth, 25)
	if err := s.mover.MoveTo(ctx, ex, ey, s.MoverZ()); err != nil {
		s.opts.logf("fight-check: the retreat stopped: %v", err)
	}
}

// restLimit is how long the character rests for its health before a fight.
const restLimit = 90 * time.Second

// rest waits until the character's health is at least frac of its maximum, answering the server while it stands.
// It returns the GUID of a unit that attacked the character during the rest (empty when the rest finished), so the
// caller can fight back instead of standing in a fight it ignores.
func (s *session) rest(ctx context.Context, frac float32, limit time.Duration) wire.GUID {
	cs, ok := s.world.Combat(s.self)
	if !ok || cs.MaxHealth <= 0 {
		return wire.EmptyGUID
	}
	if float32(cs.Health) >= frac*float32(cs.MaxHealth) {
		return wire.EmptyGUID
	}
	link := s.transport()
	start := link.Now()
	last := cs.Health
	deadline := start.Add(limit)
	s.opts.logf("fight-check: resting: %d/%d health", cs.Health, cs.MaxHealth)
	for {
		if link.Now().After(deadline) {
			s.opts.logf("fight-check: still at %d/%d health after %s", last, cs.MaxHealth, limit)
			return wire.EmptyGUID
		}
		if err := link.WaitUntil(ctx, link.Now().Add(time.Second)); err != nil {
			return wire.EmptyGUID
		}
		cs, ok = s.world.Combat(s.self)
		if !ok {
			return wire.EmptyGUID
		}
		if cs.Dead || (cs.Health <= 0 && cs.MaxHealth > 0) {
			// A corpse does not rest: the run is over, and the only packet left is the logout.
			s.opts.logf("fight-check: the character died while resting")
			return wire.EmptyGUID
		}
		if !cs.AttackedBy.IsZero() || cs.Health < last {
			attacker := cs.AttackedBy
			if !attacker.IsZero() {
				// ApplyAttackStart records whatever GUID the server named; attacking one the model does not hold (or
				// a corpse, or a non-unit) would send CMSG_SET_SELECTION for nothing. Keep resting instead.
				if _, err := s.liveUnit(attacker); err != nil {
					s.opts.logf("fight-check: something the model cannot see (%v) hit the character while it rested", err)
					attacker = wire.EmptyGUID
				}
			}
			if !attacker.IsZero() {
				return attacker
			}
		}
		if cs.Health != last {
			s.opts.logf("fight-check: resting: %d/%d health", cs.Health, cs.MaxHealth)
		}
		last = cs.Health
		if float32(cs.Health) >= frac*float32(cs.MaxHealth) {
			return wire.EmptyGUID
		}
	}
}

// selfObject returns the fighter as the model holds it.
func (s *session) selfObject() *objects.Object {
	o, ok := s.world.Object(s.self)
	if !ok {
		return &objects.Object{}
	}
	return o
}

// selfDead reports the fighter's death the way Attack reads it: the death bit, or zero health with a real maximum.
func (s *session) selfDead() bool {
	cs, ok := s.world.Combat(s.self)
	if !ok {
		return false
	}
	return cs.Dead || (cs.Health <= 0 && cs.MaxHealth > 0)
}

// maxTargetAttempts bounds how many refused candidates the command walks through.
const maxTargetAttempts = 5

// fightLevelWindow is the level band a fight target must be in. An explicit bound (--level-min/--level-max) wins;
// the other end, and both when neither is given, come from the character's own level: three levels below it to two
// above, clamped to 1-60 (a level-30 character looks for 27-32, a level-1 one for 1-3). A single explicit bound
// past the automatic one on the other side (--level-min 40 on a level-1 character) widens the automatic end to it
// instead of leaving an empty band the run would only report as "nothing found"; two contradictory explicit bounds
// are refused before this is called.
func fightLevelWindow(min, max, selfLevel int32) (int32, int32) {
	autoMin, autoMax := min == 0, max == 0
	if selfLevel < levelFloor {
		selfLevel = levelFloor
	}
	if selfLevel > levelCeil {
		selfLevel = levelCeil
	}
	if autoMin {
		min = selfLevel - targetBandBelow
		if min < levelFloor {
			min = levelFloor
		}
	}
	if autoMax {
		max = selfLevel + targetBandAbove
		if max > levelCeil {
			max = levelCeil
		}
	}
	if min > max {
		if autoMin {
			min = max
		} else {
			max = min
		}
	}
	return min, max
}

// attackableCandidates is every unit the geometric filter accepts, nearest first (the filter is stated on
// chooseTarget).
func attackableCandidates(units []*objects.Object, x, y, z float32, self wire.GUID, entry int32, radius float32, levelMin, levelMax int32) []*objects.Object {
	var out []*objects.Object
	for _, u := range units {
		if u.GUID == self || u.Dead || (u.Info.Health <= 0 && u.Info.MaxHealth > 0) {
			continue
		}
		if u.Type != objects.TypeUnit || u.Info.Entry == 0 {
			continue
		}
		if objects.Distance(u.X, u.Y, u.Z, x, y, z) > radius {
			continue
		}
		if entry != 0 {
			if u.Info.Entry == entry {
				out = append(out, u)
			}
			continue
		}
		if u.Info.NpcFlags != 0 || u.Info.NpcFlags2 != 0 || u.Info.CreatureType == creatureTypeCritter {
			continue
		}
		if unattackable(u) {
			continue
		}
		if u.Info.Level < levelMin || u.Info.Level > levelMax {
			continue
		}
		out = append(out, u)
	}
	return out
}

// castBetweenSwings is the hook Attack calls after each tick: it casts the plan's spells once the character has the
// rage for them. Power is read from the model (the server's SMSG_POWER_UPDATE and values updates); the cost is not
// known from packets, so a "no power" refusal simply means "try again later".
func (s *session) castBetweenSwings(plan *spellPlan, target wire.GUID) FightHook {
	hostileTries, selfTries := 0, 0
	link := s.transport()
	nextHostile := link.Now()
	nextSelf := link.Now()
	maxTries := 10
	retry := 2 * time.Second
	return func(ctx context.Context, s *session) error {
		now := link.Now()
		// The character's rage bar, by power type (rage = 1): tenths of rage as the server stores them.
		power := s.powerOfType(ragePowerType)
		if plan.Hostile != 0 && hostileTries < maxTries && now.After(nextHostile) && power >= 1 {
			result, err := s.Cast(ctx, plan.Hostile, target, plan.HostileWindow)
			hostileTries++
			nextHostile = now.Add(retry)
			if err != nil {
				s.opts.logf("fight-check: spell %d: %v", plan.Hostile, err)
			} else {
				s.opts.logf("fight-check: spell %d at %s rage: %s", plan.Hostile, combat.PowerText(ragePowerType, power, s.powerMaxOfType(ragePowerType)), result)
				if result != "no power" && result != "not ready" {
					plan.Hostile = 0
				}
			}
		}
		if plan.Self != 0 && selfTries < maxTries && now.After(nextSelf) && power >= 1 {
			result, err := s.Cast(ctx, plan.Self, wire.EmptyGUID, RangeWindow{})
			selfTries++
			nextSelf = now.Add(retry)
			if err != nil {
				s.opts.logf("fight-check: spell %d: %v", plan.Self, err)
			} else {
				s.opts.logf("fight-check: spell %d at %s rage: %s", plan.Self, combat.PowerText(ragePowerType, power, s.powerMaxOfType(ragePowerType)), result)
				if result != "no power" && result != "not ready" {
					plan.Self = 0
				}
			}
		}
		return nil
	}
}

// powerOfType reads one power bar of the fighter from the model, by power TYPE: the model resolves the type to the
// slot the server's class layout uses (objects.Info.PowerOf), so rage on a warrior is Power[0] and not Power[1].
// selfLevel is the character's own level from its create block. 0 when the model does not hold it.
func (s *session) selfLevel() int32 {
	if o, ok := s.world.Object(s.self); ok {
		return o.Info.Level
	}
	return 0
}

func (s *session) powerOfType(powerType int) int32 {
	o, ok := s.world.Object(s.self)
	if !ok || powerType < 0 || powerType > 255 {
		return 0
	}
	return o.Info.PowerOf(uint8(powerType))
}

// powerMaxOfType is the maximum of one power bar, resolved the same way (the DisplayPower slot holds it for a unit
// whose bar this is).
func (s *session) powerMaxOfType(powerType int) int32 {
	o, ok := s.world.Object(s.self)
	if !ok || powerType < 0 || powerType > 255 {
		return 0
	}
	slot, ok := o.Info.PowerSlot(uint8(powerType))
	if !ok {
		return 0
	}
	return o.Info.MaxPower[slot]
}

// fillFightReport copies the fight log and the target's state into the report.
func (s *session) fillFightReport() {
	f := s.fight
	r := &s.report.Fight
	r.Target = f.target
	r.TargetName = ""
	if o, ok := s.world.Object(f.target); ok {
		r.TargetEntry = o.Info.Entry
		r.TargetLevel = o.Info.Level
		r.TargetHealth = o.Info.Health
		r.TargetMax = o.Info.MaxHealth
		r.TargetName = s.world.Name(o)
	}
	// Engaged means a round landed (or a cast went off), not that a swing was sent: a fight whose only answer was a
	// refusal is not an engagement.
	r.Engaged = f.contact > 0 || f.castCount > 0
	r.Killed = f.killed
	r.Swings = f.swings
	r.Misses = f.misses
	r.Casts = f.castCount
	r.Auras = f.auraCount
	r.DamageDealt = f.damageDealt
	r.DamageTaken = f.damageTaken
	r.XPGain = f.xp
	r.StaleCasts = f.staleDrops
	r.LastSwingErr = f.lastSwingErr.String()
	r.Events = append([]FightEvent(nil), f.events...)
	r.CastRows = append([]FightRow(nil), f.casts...)
	r.AuraRows = append([]AuraRow(nil), f.auras...)
	if cs, ok := s.world.Combat(s.self); ok {
		r.SelfHealth = cs.Health
		r.SelfMax = cs.MaxHealth
		r.SelfPower = cs.Power
		r.SelfMaxPower = cs.MaxPower
		r.SelfPowerType = cs.PowerType
	}
}

// printFightReport prints the fight log: times, hits, misses, casts, auras and the final state. Units are named by
// role and spell ids are game constants, so the lines carry no identifiers.
func (s *session) printFightReport(fought time.Duration) {
	r := s.report.Fight
	for _, e := range r.Events {
		s.opts.logf("fight %6.1fs %-6s %s", e.At.Seconds(), e.Kind, e.Detail)
	}
	s.opts.logf("fight-check: %s after %s: %d swing(s), %d miss(es), %d cast(s), %d aura event(s)",
		verdict(r.Killed, r.Evaded), fought.Round(time.Millisecond), r.Swings, r.Misses, r.Casts, r.Auras)
	s.opts.logf("fight-check: damage dealt %d, damage taken %d, experience %d", r.DamageDealt, r.DamageTaken, r.XPGain)
	s.opts.logf("fight-check: the target is at %d/%d health", r.TargetHealth, r.TargetMax)
	s.opts.logf("fight-check: you are at %d/%d health, %s %s",
		r.SelfHealth, r.SelfMax, combat.PowerText(int8(r.SelfPowerType), r.SelfPower, r.SelfMaxPower),
		combat.PowerName(int8(r.SelfPowerType)))
	if r.StaleCasts > 0 {
		s.opts.logf("fight-check: %d late cast packet(s) dropped (a cast that gave up was answered after the next one)",
			r.StaleCasts)
	}
	if s.report.ParseErrors > 0 {
		s.opts.logf("fight-check: %d combat packet(s) were not decoded", s.report.ParseErrors)
	}
}

func verdict(killed, evaded bool) string {
	switch {
	case killed:
		return "the target died"
	case evaded:
		return "the target got away"
	}
	return "the fight ended with the target alive"
}

// spellListText is "(3: 6603 6673 100)" or "" with no spells.
func spellListText(spells []int32) string {
	if len(spells) == 0 {
		return ""
	}
	out := fmt.Sprintf(" (%d:", len(spells))
	for _, id := range spells {
		out += fmt.Sprintf(" %d", id)
	}
	return out + ")"
}
