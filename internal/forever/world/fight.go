package world

// The fighter: melee and casting on top of round 3's mover. Attack selects the target, faces it, walks to melee
// reach, starts the swing, and keeps facing and distance while the target moves; Cast honours the range window and
// the facing rule and waits for the spell to go or fail. Both read the world model the session keeps fed and write
// through the mover's Sender, so they are exercised against the fake server with a fake clock in the tests.

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

// RangeWindow is the distance window a cast is allowed in, in yards. A zero Min has no lower bound and a zero Max no
// upper one. The window comes from the spell's own data (Spell.dbc), which this client does not carry, so the caller
// states it (COMBAT-R1-NOTES.md, "What is approximated").
type RangeWindow struct {
	Min, Max float32
}

// ErrOutOfRange is a cast the caller's window refuses before the packet is sent.
var ErrOutOfRange = errors.New("world: the target is outside the cast's range window")

// ErrCastFailed is a cast the server refused; the result string carries the reason's name.
var ErrCastFailed = errors.New("world: the cast failed")

// ErrTargetGone is a target that left the model or died while a fight was running.
var ErrTargetGone = errors.New("world: the target is gone")

// ErrSelfDead is the fighter dying: no swing can land and the fight is over.
var ErrSelfDead = errors.New("world: the character died")

// ErrCannotAttack is the server refusing a target: it answers CMSG_ATTACK_SWING with SMSG_ATTACK_STOP at once
// (HandleAttackSwingOpcode's !IsValidAttackTarget path, CombatHandler.cpp:28-45) and no swing follows. The model
// refuses a target too, when the unit's own flags say the server would (IsValidAttackTarget's flag test,
// Object.cpp:2442-2443): never a packet for a unit that cannot be attacked.
var ErrCannotAttack = errors.New("world: the server will not let this character attack that unit")

// ErrNotAUnit is a target that is not a unit object with unit fields: no selection, swing or cast is sent for it.
var ErrNotAUnit = errors.New("world: the target is not a unit")

// ErrTargetEvaded is the server ending the attack while the target is alive: an evade, a leash back to the spawn, or
// any other server-side stop. There is no authoritative death, so the fight is not a success.
var ErrTargetEvaded = errors.New("world: the server stopped the attack and the target is alive")

// ErrStillOutOfRange is the server answering a swing with "not in range" after every forced approach one fight is
// allowed: the client has walked in as far as it may and the swing still does not reach.
var ErrStillOutOfRange = errors.New("world: the server still says the target is out of range after closing in")

// swingErrorRetry is how long Attack waits after a swing before it looks at the world again. The server's swing
// timer is the attacker's weapon speed (1.8-2.0 s for a level-1 character); half a second is the client tick and
// keeps facing and distance current without flooding the server.
const swingErrorRetry = 500 * time.Millisecond

// maxForcedApproaches bounds the swing-error-driven approaches of one fight. A "not in range" answer to a swing the
// client believes is in reach is ground truth for one step, but a target the server never lets the client reach must
// not keep the fight spinning: after this many forced approaches Attack returns ErrStillOutOfRange.
const maxForcedApproaches = 8

// forcedStep is the shortest step a forced approach takes: past the mover's 0.5-yard stop distance, so the walk is
// never the no-op that left the fighter in place repeating the refused swing.
const forcedStep = 0.8

// attackStopWait bounds the cleanup that ends the server's attack state: how long it waits for the server's own
// SMSG_ATTACK_STOP after CMSG_ATTACK_STOP. The server answers at once; two seconds is a slow round trip.
const attackStopWait = 2 * time.Second

// Cast's bounds. maxCastTime clamps the cast time a SMSG_SPELL_START may name: the longest Classic cast is far below
// it, and a start naming days must not park the wait. castWaitBase is the first-answer window (an instant cast still
// arrives a tick or two later), and castWaitMax is the absolute, non-extendable bound of one Cast — no packet moves
// it. A cast clamped to maxCastTime started late in the base window still fits inside castWaitMax.
const (
	maxCastTime  = 30 * time.Second
	castWaitBase = 6 * time.Second
	castWaitMax  = 38 * time.Second
)

// castQuarantineWindow is how long a spell whose Cast gave up is quarantined: any START, GO, CAST_FAILED or
// SPELL_FAILURE for that spell in the window is a late answer to the abandoned cast (the server had already sent it
// before it saw the next request) and is dropped and counted, and Cast does not send the spell again inside it. A
// server's answers to one cast arrive within the base window; three seconds past that is a slow round trip.
const castQuarantineWindow = 3 * time.Second

// maxStaleCastIDs and maxQuarantinedSpells bound the cast ids and spells a timed-out generation leaves behind for
// exact drops; the quarantine window alone would do, but a packet that names the id is attributable whatever the
// window. Both are fixed arrays: the reader writes them and the fighter reads them, and a map would crash on a
// concurrent access where a small array only tears a value (the file's existing, documented trade-off).
const (
	maxStaleCastIDs      = 16
	maxQuarantinedSpells = 4
)

// unitFlagNonAttackable, unitFlagNotAttackable and unitFlagNonAttackable2 are the flags IsValidAttackTarget refuses
// on (UnitDefines.h:168, :174, :183; the check at Object.cpp:2429-2443).
const (
	unitFlagNonAttackable  = 0x00000002
	unitFlagNotAttackable  = 0x00000080
	unitFlagNonAttackable2 = 0x00010000
)

// FightEvent is one line of the fight log.
type FightEvent struct {
	At     time.Duration
	Kind   string
	Detail string
	Damage int32
}

// FightRow is one cast the fight saw.
type FightRow struct {
	SpellID int32
	Started bool
	Result  string
	At      time.Duration
}

// AuraRow is one aura gained or lost on the fighter or its target.
type AuraRow struct {
	Unit    wire.GUID
	Slot    uint16
	SpellID int32
	Gained  bool
	Stacks  uint8
	At      time.Duration
}

// maxFightRows caps the fight log. The log is the fight's own events, but a hostile server could name the same
// fighter in thousands of packets; past the cap only the counters grow.
const maxFightRows = 4096

// fightLog is what the session records of one fight; the reader goroutine writes it, the fighter reads it.
type fightLog struct {
	started time.Time
	// now is the clock the event times are measured on: the link's, so a fight run on a fake clock prints the
	// durations it actually saw. A nil clock (a log built outside a session) falls back to the wall clock.
	now    func() time.Time
	self   wire.GUID
	target wire.GUID

	events []FightEvent
	casts  []FightRow
	auras  []AuraRow

	swings, misses, castCount, auraCount int
	damageDealt, damageTaken             int32
	killed                               bool
	xp                                   int32
	parseErrors                          int

	// lastSwingErr is the last swing error the server sent; seq increments on every one, so the fighter notices a
	// repeated error too.
	lastSwingErr combat.SwingErr
	swingErrSeq  uint64
	// attackStops counts the SMSG_ATTACK_STOP packets that named the fighter as the attacker: one that the fighter
	// did not ask for is the server ending the attack (an invalid target, or the victim's death).
	attackStops int
	// contact counts the swing rounds between the fighter and its target, so a stop after contact is read as the
	// fight ending (the victim died) and not as a refused target.
	contact int
	// attackedByOthers counts the rounds another unit fought with the fighter during the fight, and
	// damageFromOthers is what those rounds took from it. Such a round is not contact with the target (a mob that
	// pulled the character must not turn the server's refusal of the target into a finished fight).
	attackedByOthers int
	damageFromOthers int32

	// Cast bookkeeping for Cast's wait: seq increments when the pending cast ends (go or failure), and result is
	// its outcome. castClientID is the id this client sent (CMSG_CAST_SPELL's CastID); castID is the server's id
	// from the matching SMSG_SPELL_START (Spell::SendSpellStart writes m_castId, Spell.cpp:4799), which is what the
	// go and the later failures name. castStarted says a matching start was seen; castEnds is that start's
	// clamped deadline on the client's clock.
	castSeq      uint64
	castResult   string
	castEnds     time.Time
	castOpen     bool
	castStarted  bool
	castSpell    int32
	castClientID wire.GUID
	castID       wire.GUID

	// Cast generations: castGen increments for every Cast, so a packet can be told apart from one of a generation
	// that gave up. quarantine holds the spells whose timed-out generation may still answer (until when), and
	// staleIDs holds the cast ids that generation named; both drop and count those late packets.
	castGen    uint64
	quarantine [maxQuarantinedSpells]quarantineEntry
	staleIDs   [maxStaleCastIDs]staleCastID
	staleDrops int
}

// quarantineEntry is one spell whose abandoned generation may still answer, and until when.
type quarantineEntry struct {
	spell int32
	until time.Time
}

// staleCastID is one cast id a generation that gave up named, and until when an answer naming it is that
// generation's (the same window; a server's cast id may be reused after it).
type staleCastID struct {
	id    wire.GUID
	gen   uint64
	until time.Time
}

// elapsed is the log's time since the fight started, on the clock the log measures with.
func (f *fightLog) elapsed() time.Duration {
	if f.now != nil {
		return f.now().Sub(f.started)
	}
	return time.Since(f.started)
}

// add appends one event under the row cap.
func (f *fightLog) add(kind, detail string, damage int32) {
	if len(f.events) >= maxFightRows {
		return
	}
	f.events = append(f.events, FightEvent{At: f.elapsed(), Kind: kind, Detail: detail, Damage: damage})
}

// setTarget starts the target-specific bookkeeping.
func (f *fightLog) setTarget(guid wire.GUID) {
	f.target = guid
}

// isFighter reports whether guid is the fighter or its target: the two units whose auras the log keeps.
func (f *fightLog) isFighter(guid wire.GUID) bool {
	return guid == f.self || (guid == f.target && !f.target.IsZero())
}

// swing records one attack round. Only a round between the fighter and its target is contact with the target: a mob
// that hits the character while the fight runs proves nothing about the target, so it gets its own count and its own
// line instead.
func (f *fightLog) swing(r combat.AttackRound) {
	selfAttacker := r.Attacker == f.self
	if !selfAttacker && r.Victim != f.self {
		return
	}
	other := r.Attacker
	if selfAttacker {
		other = r.Victim
	}
	if other != f.target || other == f.self {
		f.attackedByOthers++
		if !selfAttacker {
			f.damageFromOthers += r.Damage
		}
		f.add("swing", "attacked by another unit", r.Damage)
		return
	}
	f.contact++
	switch {
	case r.HitMiss() || r.VictimState == 2 || r.VictimState == 3 || r.VictimState == 6 || r.VictimState == 7 || r.VictimState == 8:
		f.misses++
		f.add("swing", fmt.Sprintf("miss against %s (victim state %s)", f.name(other), combat.VictimStateName(r.VictimState)), 0)
	case r.Damage <= 0 && r.OverDamage < 0:
		f.add("swing", fmt.Sprintf("no damage to %s", f.name(other)), 0)
	default:
		f.swings++
		if selfAttacker {
			f.damageDealt += r.Damage
			f.add("swing", fmt.Sprintf("hit %s for %d", f.name(other), r.Damage), r.Damage)
		} else {
			f.damageTaken += r.Damage
			f.add("swing", fmt.Sprintf("took %d from %s", r.Damage, f.name(other)), r.Damage)
		}
	}
}

// quarantineCast closes the generation a Cast gave up on: the spell is quarantined for castQuarantineWindow and the
// cast ids that generation named are remembered, so its late START/GO/FAILURE cannot complete a later Cast.
func (f *fightLog) quarantineCast(spellID int32, now time.Time) {
	if spellID == 0 {
		return
	}
	gen := f.castGen
	until := now.Add(castQuarantineWindow)
	f.quarantine[gen%maxQuarantinedSpells] = quarantineEntry{spell: spellID, until: until}
	for i, id := range []wire.GUID{f.castClientID, f.castID} {
		if id.IsZero() {
			continue
		}
		f.staleIDs[(gen+uint64(i))%maxStaleCastIDs] = staleCastID{id: id, gen: gen, until: until}
	}
}

// quarantinedUntil reports whether a spell may not be sent again yet; Cast waits the window out.
func (f *fightLog) quarantinedUntil(spellID int32, now time.Time) (time.Time, bool) {
	for _, q := range f.quarantine {
		if q.spell == spellID && now.Before(q.until) {
			return q.until, true
		}
	}
	return time.Time{}, false
}

// staleCast reports whether a cast packet belongs to a generation that gave up instead of the pending one: its cast
// id is one of those, or its spell is quarantined (Cast does not send a quarantined spell, so no pending Cast can
// own it). Such a packet is dropped and counted.
func (f *fightLog) staleCast(spellID int32, castID wire.GUID, now time.Time) bool {
	if !castID.IsZero() {
		for _, s := range f.staleIDs {
			if s.id == castID && now.Before(s.until) {
				f.staleDrops++
				return true
			}
		}
	}
	if _, ok := f.quarantinedUntil(spellID, now); ok {
		f.staleDrops++
		return true
	}
	return false
}

// beginCast registers the cast Cast is about to send. Until its matching start arrives there is no deadline to
// extend; the pending state exists so an unrelated packet cannot close the wait. Every Cast is its own generation.
func (f *fightLog) beginCast(spellID int32, clientID wire.GUID) {
	f.castGen++
	f.castOpen = true
	f.castStarted = false
	f.castSpell = spellID
	f.castClientID = clientID
	f.castID = wire.EmptyGUID
	f.castEnds = time.Time{}
}

// castReset clears the pending cast on every exit of Cast, so a start or failure that arrives after the wait is over
// changes nothing.
func (f *fightLog) castReset() {
	f.castOpen = false
	f.castStarted = false
	f.castSpell = 0
	f.castClientID = wire.EmptyGUID
	f.castID = wire.EmptyGUID
	f.castEnds = time.Time{}
}

// castStart records SMSG_SPELL_START for the cast Cast is waiting for. Only a start with that spell id moves the
// deadline, and only the first one: the server's cast id differs from the client's (Spell.cpp:4799 generates it), so
// the first matching start adopts it, and a later start with another id is a different cast and is ignored. The cast
// time is clamped by maxCastTime.
func (f *fightLog) castStart(c *combat.SpellCast, now time.Time) {
	if !f.castOpen || c.CasterUnit != f.self || c.SpellID != f.castSpell {
		return
	}
	if f.castStarted {
		if f.castID != c.CastID {
			return
		}
		return // the same start again: idempotent, the deadline was set by the first
	}
	f.castStarted = true
	f.castID = c.CastID
	d := time.Duration(c.CastTime) * time.Millisecond
	if d > maxCastTime {
		d = maxCastTime
	}
	f.castEnds = now.Add(d)
	if len(f.casts) < maxFightRows {
		f.casts = append(f.casts, FightRow{SpellID: c.SpellID, Started: true, Result: "started", At: f.elapsed()})
	}
	f.add("cast", fmt.Sprintf("spell %d started (%d ms)", c.SpellID, c.CastTime), 0)
}

// castGo records SMSG_SPELL_GO for the pending cast: the cast succeeded. A go may arrive without a start (an instant
// cast); it then adopts the server's cast id. A go with another cast id after a start is another cast of the same
// spell and is ignored.
func (f *fightLog) castGo(c *combat.SpellCast) {
	if !f.castOpen || c.CasterUnit != f.self || c.SpellID != f.castSpell {
		return
	}
	if f.castStarted && c.CastID != f.castID {
		return
	}
	f.castID = c.CastID
	f.castOpen = false
	f.castStarted = false
	f.castSeq++
	f.castResult = "success"
	f.castCount++
	if len(f.casts) < maxFightRows {
		f.casts = append(f.casts, FightRow{SpellID: c.SpellID, Result: "success", At: f.elapsed()})
	}
	f.add("cast", fmt.Sprintf("spell %d went off (%d hit, %d miss)", c.SpellID, len(c.HitTargets), len(c.MissTargets)), 0)
}

// castFail records a failure of the pending cast. preCast says the packet is SMSG_CAST_FAILED: it is sent straight
// to the casting player before the spell starts. Its cast id is either the client's or the server's own: only the
// "cannot request a cast at all" path echoes the request's id (SpellHandler.cpp:255), while a cast that fails in
// Spell::prepare carries m_castId (Spell.cpp:4709), which Create<HighGuid::Cast> builds with
// SPELL_CAST_SOURCE_NORMAL (Spell.cpp:562, Spell.h:168) — the live 2026-11-18 capture's no-power refusal for Heroic
// Strike arrived that way with no start before it. So a CAST_FAILED matches by spell id (the packet is addressed to
// this client and names the spell it asked for). SMSG_SPELL_FAILURE names the caster and the server's cast id; when
// a start was seen, its id must match that start's. A failure of another spell changes nothing, however large its
// reason.
func (f *fightLog) castFail(spellID int32, castID wire.GUID, reason int32, preCast bool) {
	if !f.castOpen || spellID != f.castSpell || reason == 0 {
		return
	}
	if !preCast && f.castStarted && castID != f.castID {
		return
	}
	f.castOpen = false
	f.castStarted = false
	f.castSeq++
	f.castResult = combat.SpellFailureName(reason)
	if len(f.casts) < maxFightRows {
		f.casts = append(f.casts, FightRow{SpellID: spellID, Result: f.castResult, At: f.elapsed()})
	}
	f.add("cast", fmt.Sprintf("spell %d failed: %s", spellID, f.castResult), 0)
}

// aura records a gained or lost aura on the fighter or its target.
func (f *fightLog) aura(unit wire.GUID, info combat.AuraInfo) {
	if !f.isFighter(unit) {
		return
	}
	f.auraCount++
	row := AuraRow{Unit: unit, Slot: info.Slot, At: f.elapsed()}
	if info.Data != nil {
		row.Gained = true
		row.SpellID = info.Data.SpellID
		row.Stacks = info.Data.Applications
		f.add("aura", fmt.Sprintf("gained spell %d on %s (slot %d, %d stack(s))", row.SpellID, f.name(unit), info.Slot, row.Stacks), 0)
	} else {
		f.add("aura", fmt.Sprintf("lost the aura in slot %d on %s", info.Slot, f.name(unit)), 0)
	}
	if len(f.auras) < maxFightRows {
		f.auras = append(f.auras, row)
	}
}

// name is the fight log's word for a unit: its role, never its identifier.
func (f *fightLog) name(g wire.GUID) string {
	switch {
	case g == f.self:
		return "you"
	case g == f.target && !g.IsZero():
		return "the target"
	case g.IsZero():
		return "nobody"
	}
	return "another unit"
}

// meleeReach is the server's swing range: max(caster combat reach + target combat reach + 4/3, 5) (Unit.cpp:749-753).
func meleeReach(self, target *objects.Object) float32 {
	reach := self.Info.CombatReach + target.Info.CombatReach + 4.0/3.0
	if reach < 5 {
		reach = 5 // NOMINAL_MELEE_RANGE, UnitDefines.h:31
	}
	return reach
}

// approachPoint is a point at frac × reach from the target on the line from (sx,sy,sz): where a walk to melee
// stops. It returns the fighter's own point when it is already closer than that.
func approachPoint(sx, sy, sz, tx, ty, tz, reach, frac float32) (x, y, z float32) {
	dx, dy, dz := sx-tx, sy-ty, sz-tz
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
	if dist == 0 {
		return tx, ty, tz
	}
	want := reach * frac
	if want > dist {
		return sx, sy, sz
	}
	k := want / dist
	return tx + dx*k, ty + dy*k, tz + dz*k
}

// transport is what a fight writes through: the session in production, a fake in the tests (the mover's Sender).
func (s *session) transport() objects.Sender {
	if s.fightSender != nil {
		return s.fightSender
	}
	return s
}

// FightHook is called once per Attack tick, after facing and swinging. The fight-check uses it to cast between
// swings; a nil hook (or none) is a pure melee fight.
type FightHook func(ctx context.Context, s *session) error

// liveUnit resolves a GUID to a live unit object in the model. The order is the one Attack uses: it must exist,
// have unit fields, and not be dead, and its own unit flags must not say "not attackable" (IsValidAttackTarget's
// flag test, Object.cpp:2429-2443). The model cannot tell friend from foe: it has faction TEMPLATES but no faction or
// reputation table, so a friendly unit is only discovered from the server's SMSG_ATTACK_STOP, exactly as a real
// client's server round trip would.
func (s *session) liveUnit(guid wire.GUID) (*objects.Object, error) {
	o, ok := s.world.Object(guid)
	if !ok {
		return nil, ErrTargetGone
	}
	switch o.Type {
	case objects.TypeUnit, objects.TypePlayer, objects.TypeActivePlayer:
	default:
		return nil, fmt.Errorf("%w: it is a %s", ErrNotAUnit, objects.TypeName(o.Type))
	}
	if !o.Info.HasUnit {
		return nil, fmt.Errorf("%w: no unit fields", ErrNotAUnit)
	}
	if o.Dead || (o.Info.Health <= 0 && o.Info.MaxHealth > 0) {
		return nil, ErrTargetGone
	}
	if unattackable(o) {
		return nil, ErrCannotAttack
	}
	return o, nil
}

// unattackable reports the unit flags IsValidAttackTarget refuses on (Object.cpp:2429-2443).
func unattackable(o *objects.Object) bool {
	return o.Info.Flags&(unitFlagNonAttackable|unitFlagNotAttackable|unitFlagNonAttackable2) != 0
}

// Attack is the fighter: resolve the target, select it, face it, walk into melee reach, start the swing, keep facing
// and distance while it moves, and stop when it is dead or ctx ends. Success is the target's authoritative death; a
// fight the target wins, or one the server ends while the target lives, is an error. Whatever the exit, an attack
// this function started is ended with CMSG_ATTACK_STOP unless the server already ended it.
func (s *session) Attack(ctx context.Context, guid wire.GUID, hooks ...FightHook) error {
	if s.mover == nil {
		return errors.New("world: no mover (the character's create block never arrived)")
	}
	if guid.IsZero() || guid == s.self {
		return errors.New("world: an attack needs another unit")
	}
	// Resolve and validate before anything is sent: no selection for a GUID the model does not hold, for a
	// non-unit, for a corpse, or for a unit the server's own flags call unattackable.
	if _, err := s.liveUnit(guid); err != nil {
		return err
	}
	link := s.transport()
	if err := link.Send(wire.CMSGSetSelection, combat.BuildSetSelection(guid)); err != nil {
		return fmt.Errorf("world: CMSG_SET_SELECTION: %w", err)
	}
	s.fight.setTarget(guid)
	attacking := false
	forcedApproaches := 0
	var lastErrSeq uint64
	stopsBefore := s.fight.attackStops
	contactBefore := s.fight.contact
	deadline := link.Now().Add(s.opts.FightFor)
	if s.opts.FightFor <= 0 {
		deadline = link.Now().Add(60 * time.Second)
	}
	// One cleanup for every exit: the server's attack state is ended here, or the server ended it itself (the stop
	// it sent is counted). The wait is bounded by attackStopWait and a failure is recorded, never returned over the
	// exit's own reason.
	defer func() {
		if s.betweenMaps() {
			return // the server is moving the character; an attack stop off the old map means nothing
		}
		if !attacking || s.fight.attackStops > stopsBefore {
			return // the server already ended the attack (its stop is counted)
		}
		if err := link.Send(wire.CMSGAttackStop, combat.BuildAttackStop()); err != nil {
			s.fight.add("swing", "CMSG_ATTACK_STOP failed: "+err.Error(), 0)
			return
		}
		before := s.fight.attackStops
		wctx, cancel := context.WithTimeout(context.Background(), attackStopWait)
		defer cancel()
		if err := link.WaitUntil(wctx, link.Now().Add(attackStopWait)); err != nil {
			s.fight.add("swing", "no answer to CMSG_ATTACK_STOP", 0)
			return
		}
		if s.fight.attackStops == before {
			s.fight.add("swing", "the server did not confirm the attack stop", 0)
		}
	}()
	for {
		now := link.Now()
		if s.serveRT != nil && s.serveRT.stopFight.Swap(false) {
			return ErrAttackStopped
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.betweenMaps() {
			// The character is between maps: no swing, cast or movement belongs to the map it just left.
			return ErrMapChange
		}
		if now.After(deadline) {
			return fmt.Errorf("world: %s of fighting and the target is not dead", s.opts.FightFor)
		}
		target, ok := s.world.Object(guid)
		if !ok {
			return ErrTargetGone
		}
		self, ok := s.world.Object(s.self)
		if !ok {
			return errors.New("world: the model lost the fighter")
		}
		if self.Dead || (self.Info.Health <= 0 && self.Info.MaxHealth > 0) {
			s.fight.add("death", "the character died", 0)
			return ErrSelfDead
		}
		if target.Dead || (target.Info.Health <= 0 && target.Info.MaxHealth > 0) {
			s.fight.killed = true
			s.fight.add("death", "the target died", 0)
			return nil
		}
		if attacking && s.fight.attackStops > stopsBefore {
			// The server ended the attack on its own: before any contact round it refuses the target
			// (HandleAttackSwingOpcode's invalid-target path); after contact it is a stop with the target still
			// standing — an evade, a leash or a hostile stop. The victim's death can trail the stop by a tick, so
			// give the values one grace period; then only an authoritative death is a success.
			if s.fight.contact > contactBefore {
				_ = link.WaitUntil(ctx, link.Now().Add(300*time.Millisecond))
				if t, ok := s.world.Object(guid); ok && (t.Dead || (t.Info.Health <= 0 && t.Info.MaxHealth > 0)) {
					s.fight.killed = true
					s.fight.add("death", "the target died", 0)
					return nil
				}
				health := int64(0)
				if t, ok := s.world.Object(guid); ok {
					health = t.Info.Health
				}
				return fmt.Errorf("%w: target alive at %d health", ErrTargetEvaded, health)
			}
			return ErrCannotAttack
		}
		tx, ty, tz := target.Position(now)
		reach := meleeReach(self, target)
		dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz)
		// A swing error the server sent since the last look is ground truth: close in even when the model's own
		// distance looks fine (the model's reach is the server's formula, but positions lag a spline).
		forced := false
		if s.fight.swingErrSeq != lastErrSeq {
			lastErrSeq = s.fight.swingErrSeq
			if s.fight.lastSwingErr == combat.SwingNotInRange {
				forced = true
			}
		}
		if dist > reach-0.3 || forced {
			// One continuous chase: no start/stop pair per position sample, one leg that is re-aimed at most once
			// per heartbeat. The destination is the target's position now; the chase ends when the distance the
			// fight cares about is reached (inside the server's reach, or closer than the client believes when the
			// server answered "not in range").
			stopAt := reach - 0.3
			if forced {
				// Bounded: a server that keeps saying "not in range" whatever the client does must not keep the
				// fight spinning.
				forcedApproaches++
				if forcedApproaches > maxForcedApproaches {
					return fmt.Errorf("%w: %d forced approaches", ErrStillOutOfRange, maxForcedApproaches)
				}
				// The server's answer is ground truth even when the client's own distance says the swing reaches:
				// walk past the client's reach, at least forcedStep closer than where the fighter stands, but never
				// onto the target itself (a point that close would mean no walk at all).
				stopAt = min(reach*0.85, dist-forcedStep)
				if stopAt < 0.2 {
					stopAt = 0.2
				}
			}
			bx, by, bz := s.MoverX(), s.MoverY(), s.MoverZ()
			goal := objects.ChaseGoal{
				Dest: func(now time.Time) (float32, float32, float32) {
					return target.Position(now)
				},
				Arrived: func(now time.Time) bool {
					tx, ty, tz := target.Position(now)
					return objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz) <= stopAt
				},
			}
			if err := s.mover.ChaseTo(ctx, goal); err != nil {
				if errors.Is(err, objects.ErrTeleported) {
					if werr := link.WaitUntil(ctx, link.Now().Add(swingErrorRetry)); werr != nil {
						return werr
					}
					continue
				}
				return err
			}
			// The walk may have taken a second and the target may have moved: the distance that matters is the one
			// after it, both for the range check and for the line the fight log prints.
			tx, ty, tz = target.Position(link.Now())
			dist = objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz)
			if forced && s.MoverX() == bx && s.MoverY() == by && s.MoverZ() == bz {
				// The forced point was inside the mover's stop distance, so the chase did not move or face. A fresh
				// facing is the one thing left to change before the next swing.
				if err := s.mover.Face(objects.OrientationTo(s.MoverX(), s.MoverY(), tx, ty)); err != nil {
					return fmt.Errorf("world: facing the target: %w", err)
				}
			}
		}
		// Face the target: CMSG_MOVE_SET_FACING is the packet the server's HasInArc check reads the orientation of.
		want := objects.OrientationTo(s.MoverX(), s.MoverY(), tx, ty)
		if absf(normalizeAngle(s.MoverO()-want)) > 0.2 {
			if err := s.mover.Face(want); err != nil {
				return fmt.Errorf("world: facing the target: %w", err)
			}
		}
		if !attacking && dist > reach-0.3 {
			// The walk stopped short of the server's reach: close in again on the next tick instead of asking the
			// server for a swing it would answer with "not in range".
			if err := link.WaitUntil(ctx, link.Now().Add(swingErrorRetry)); err != nil {
				return err
			}
			continue
		}
		if !attacking {
			// The target is re-validated after the approach and immediately before the swing: a unit that died, lost
			// its unit fields or became unattackable in the meantime gets no swing. The fighter's own death is the
			// same rule — the walk may have been the last thing it did.
			if self, ok := s.world.Object(s.self); ok && (self.Dead || (self.Info.Health <= 0 && self.Info.MaxHealth > 0)) {
				s.fight.add("death", "the character died", 0)
				return ErrSelfDead
			}
			if _, err := s.liveUnit(guid); err != nil {
				return err
			}
			if err := link.Send(wire.CMSGAttackSwing, combat.BuildAttackSwing(guid)); err != nil {
				return fmt.Errorf("world: CMSG_ATTACK_SWING: %w", err)
			}
			attacking = true
			s.fight.add("swing", fmt.Sprintf("attack started on the target (reach %.2f yd, at %.2f yd)", reach, dist), 0)
		}
		for _, hook := range hooks {
			if err := hook(ctx, s); err != nil {
				return err
			}
		}
		if err := link.WaitUntil(ctx, link.Now().Add(swingErrorRetry)); err != nil {
			return err
		}
	}
}

// Cast sends CMSG_CAST_SPELL for one spell and waits for its go or its failure. target may be empty for a self
// cast. It returns the outcome's name ("success" or the failure reason) and an error for a broken session or a
// timeout; a refusal the server gives by name is the outcome, not an error. The wait is bounded by castWaitMax
// whatever the start names, and the pending state is cleared on every exit.
func (s *session) Cast(ctx context.Context, spellID int32, target wire.GUID, window RangeWindow) (string, error) {
	if s.mover == nil {
		return "", errors.New("world: no mover (the character's create block never arrived)")
	}
	link := s.transport()
	// A spell whose previous Cast gave up is not sent inside its quarantine window: its late answers would be
	// indistinguishable from this cast's. Waiting out the window is what makes the drop rule safe.
	if until, ok := s.fight.quarantinedUntil(spellID, link.Now()); ok {
		if err := link.WaitUntil(ctx, until); err != nil {
			return "", err
		}
	}
	targetBlock := combat.SelfTarget()
	if !target.IsZero() {
		t, err := s.liveUnit(target)
		if err != nil {
			return "", err
		}
		now := link.Now()
		tx, ty, tz := t.Position(now)
		dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz)
		if window.Min > 0 && dist < window.Min {
			return "too close", fmt.Errorf("%w: %.2f yd < %.2f yd", ErrOutOfRange, dist, window.Min)
		}
		if window.Max > 0 && dist > window.Max {
			return "out of range", fmt.Errorf("%w: %.2f yd > %.2f yd", ErrOutOfRange, dist, window.Max)
		}
		want := objects.OrientationTo(s.MoverX(), s.MoverY(), tx, ty)
		if absf(normalizeAngle(s.MoverO()-want)) > 0.2 {
			if err := s.mover.Face(want); err != nil {
				return "", fmt.Errorf("world: facing the target: %w", err)
			}
		}
		targetBlock = combat.UnitTarget(target)
	}
	s.castSerial++
	castID := combat.CastGUID(s.world.MapID(), spellID, s.castSerial)
	before := s.fight.castSeq
	req := combat.CastRequest{CastID: castID, SpellID: spellID, Target: targetBlock}
	s.fight.beginCast(spellID, castID)
	defer s.fight.castReset()
	s.fight.add("cast", fmt.Sprintf("CMSG_CAST_SPELL for spell %d at %s", spellID, s.fight.name(target)), 0)
	if err := link.Send(wire.CMSGCastSpell, combat.BuildCastSpell(req)); err != nil {
		return "", fmt.Errorf("world: CMSG_CAST_SPELL: %w", err)
	}
	// The wait: a go or a failure ends it; the first matching start extends it by its clamped cast time. The base is
	// generous because an instant cast still waits for its go and a lagging server answers after one or two ticks.
	deadline := link.Now().Add(castWaitBase)
	hardDeadline := link.Now().Add(castWaitMax)
	for {
		if s.fight.castSeq > before {
			return s.fight.castResult, nil
		}
		if ends := s.fight.castEnds; ends.After(deadline) {
			deadline = ends.Add(2 * time.Second)
			if deadline.After(hardDeadline) {
				deadline = hardDeadline
			}
		}
		if link.Now().After(deadline) {
			// This generation is abandoned: quarantine the spell so its late START/GO/FAILURE cannot complete the
			// next Cast of the same spell, and the next Cast waits the window out.
			s.fight.quarantineCast(spellID, link.Now())
			return "", fmt.Errorf("world: no answer to CMSG_CAST_SPELL for spell %d", spellID)
		}
		if err := link.WaitUntil(ctx, link.Now().Add(100*time.Millisecond)); err != nil {
			return "", err
		}
	}
}

// CancelCast cancels the cast in progress (CMSG_CANCEL_CAST).
func (s *session) CancelCast(spellID int32, castID wire.GUID) error {
	return s.transport().Send(wire.CMSGCancelCast, combat.BuildCancelCast(castID, spellID))
}

// MoverX and the rest are the mover's belief, which is where the client is; the model's copy of the fighter lags
// behind it during a walk.
func (s *session) MoverX() float32 { return s.mover.X }
func (s *session) MoverY() float32 { return s.mover.Y }
func (s *session) MoverZ() float32 { return s.mover.Z }
func (s *session) MoverO() float32 { return s.mover.O }

// normalizeAngle folds an angle into (-pi, pi].
func normalizeAngle(a float32) float32 {
	const twoPi = float32(2 * math.Pi)
	for a > math.Pi {
		a -= twoPi
	}
	for a <= -math.Pi {
		a += twoPi
	}
	return a
}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
