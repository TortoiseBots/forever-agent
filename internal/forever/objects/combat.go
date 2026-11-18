package objects

// The combat half of the world model: per-unit auras with an expiry, the current cast, who attacks whom, the
// in-combat flag, health and power, and death. Everything is fed by the parsers of internal/forever/combat under
// the round-3 trust rule: a packet the parser did not consume exactly is counted and merges nothing.

import (
	"sort"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// maxAurasPerUnit is the server's own cap on an update's slots (MAX_AURAS, SpellAuraDefines.h:24-25). A new slot
// past it is dropped; an existing one still updates.
const maxAurasPerUnit = 300

// maxCooldownsPerUnit bounds the cooldowns the model keeps per unit; the server's own list is small and a new spell
// past the cap is dropped.
const maxCooldownsPerUnit = 128

// Aura is one aura of a unit as SMSG_AURA_UPDATE describes it.
type Aura struct {
	Slot         uint16
	SpellID      int32
	Flags        uint16
	Applications uint8
	CastLevel    uint16
	Caster       wire.GUID
	// HasDuration is true when the packet carried Duration/Remaining (the bits, not AFLAG_DURATION, decide).
	HasDuration bool
	Duration    time.Duration
	Remaining   time.Duration
	// Applied is when the record arrived; ExpiresAt is Applied + Remaining while HasDuration.
	Applied   time.Time
	ExpiresAt time.Time
}

// Active reports whether the aura is still up at now. An aura without duration never expires by itself.
func (a Aura) Active(now time.Time) bool {
	if !a.HasDuration {
		return true
	}
	return now.Before(a.ExpiresAt)
}

// CastState is the cast a unit is in the middle of (SMSG_SPELL_START), cleared by its SMSG_SPELL_GO or a failure.
type CastState struct {
	SpellID  int32
	CastID   wire.GUID
	Target   wire.GUID
	Started  time.Time
	CastTime time.Duration
}

// Ends is when the cast should finish on the client's clock (Started + CastTime).
func (c *CastState) Ends() time.Time { return c.Started.Add(c.CastTime) }

// CooldownState is one spell cooldown the server started (SMSG_SPELL_COOLDOWN's ForcedCooldown is a duration, and
// the store turns it into an expiry from the packet's arrival).
type CooldownState struct {
	SpellID int32
	// EndsAt is when the cooldown runs out; a zero value means "no known end": either an event-driven cooldown, or
	// one whose ForcedCooldown was 0. For the server 0 means "the spell's own cooldown" (SpellHistory.cpp:544-547
	// sends the real duration; Player.cpp:12595-12601 sends 0 for the weapon-switch GCD), which this client cannot
	// resolve without Spell.dbc, so the cooldown is kept as active with an unknown duration until a clear packet.
	EndsAt time.Time
}

// Active reports whether the cooldown still runs at now.
func (c CooldownState) Active(now time.Time) bool { return c.EndsAt.IsZero() || now.Before(c.EndsAt) }

// CombatState is a snapshot of one unit's combat state.
type CombatState struct {
	Attacking  wire.GUID
	AttackedBy wire.GUID
	InCombat   bool
	Dead       bool
	Health     int64
	MaxHealth  int64
	Power      int32
	MaxPower   int32
	PowerType  uint8
	Auras      []Aura
	Cast       *CastState
	Cooldowns  []CooldownState
}

// unitLocked returns the object a combat packet names, or nil. It never creates an object: combat traffic for a
// GUID the model does not hold is counted with the movement orphans and dropped.
func (w *World) unitLocked(guid wire.GUID) *Object {
	o := w.objects[guid]
	if o == nil {
		w.orphans++
		return nil
	}
	return o
}

// auraLocked returns the object's aura map, creating it under the per-unit bound.
func (o *Object) auraLocked() map[uint16]*Aura {
	if o.Auras == nil {
		o.Auras = make(map[uint16]*Aura)
	}
	return o.Auras
}

// ApplyAuraUpdate applies SMSG_AURA_UPDATE: a slot with data is an aura gained or refreshed, a slot without data is
// an aura lost (SpellAuras.cpp:232-240).
func (w *World) ApplyAuraUpdate(u *combat.AuraUpdate, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.unitLocked(u.Unit)
	if o == nil {
		return
	}
	w.touchLocked(o, now)
	if u.UpdateAll {
		// A full aura update names every aura the unit has: the client drops what is not in the list
		// (AuraUpdate::UpdateAll, SpellPackets.cpp:154-164).
		o.Auras = nil
	}
	for _, info := range u.Auras {
		auras := o.auraLocked()
		if info.Data == nil {
			delete(auras, info.Slot)
			continue
		}
		if _, exists := auras[info.Slot]; !exists && len(auras) >= maxAurasPerUnit {
			continue // the cap: a new slot is dropped, an existing one still updates
		}
		a := &Aura{
			Slot:         info.Slot,
			SpellID:      info.Data.SpellID,
			Flags:        info.Data.Flags,
			Applications: info.Data.Applications,
			CastLevel:    info.Data.CastLevel,
			Applied:      now,
		}
		if info.Data.CastUnit != nil {
			a.Caster = *info.Data.CastUnit
		}
		if info.Data.Duration != nil && info.Data.Remaining != nil {
			a.HasDuration = true
			a.Duration = time.Duration(*info.Data.Duration) * time.Millisecond
			a.Remaining = time.Duration(*info.Data.Remaining) * time.Millisecond
			a.ExpiresAt = now.Add(a.Remaining)
		}
		auras[info.Slot] = a
	}
}

// ApplyPowerUpdate applies SMSG_POWER_UPDATE to the unit's power bar. The packet names a power TYPE
// (Unit::SetPower builds the packet with `power`, Unit.cpp:10515-10520), while the value arrays are indexed by the
// class's slot, so the type is resolved through PowerSlot before anything is written.
func (w *World) ApplyPowerUpdate(p *combat.PowerUpdate, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.unitLocked(p.GUID)
	if o == nil {
		return
	}
	w.touchLocked(o, now)
	for _, v := range p.Powers {
		slot, ok := o.Info.PowerSlot(v.Type)
		if !ok || slot >= len(o.Info.Power) {
			continue // a type neither the class nor the creature layout places: never a slot guess
		}
		o.Info.Power[slot] = v.Value
	}
}

// ApplyAttackStart applies SMSG_ATTACK_START: the attacker is now attacking the victim, both are in combat.
func (w *World) ApplyAttackStart(a combat.AttackStart, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if o := w.unitLocked(a.Attacker); o != nil {
		o.Attacking = a.Victim
		o.InCombat = true
		w.touchLocked(o, now)
	}
	if o := w.unitLocked(a.Victim); o != nil {
		o.AttackedBy = a.Attacker
		o.InCombat = true
		w.touchLocked(o, now)
	}
}

// ApplyAttackStop applies SMSG_ATTACK_STOP. NowDead says the attacker is not alive (Unit.cpp:2661-2673), so it
// marks the attacker dead; a false bit says nothing about life.
func (w *World) ApplyAttackStop(a combat.AttackStop, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if o := w.unitLocked(a.Attacker); o != nil {
		o.Attacking = wire.EmptyGUID
		o.InCombat = false
		if a.NowDead {
			o.Dead = true
		}
		w.touchLocked(o, now)
	}
	if o := w.unitLocked(a.Victim); o != nil {
		if o.AttackedBy == a.Attacker {
			o.AttackedBy = wire.EmptyGUID
		}
		w.touchLocked(o, now)
	}
}

// ApplyAttackRound applies one swing's log to the attacker's in-combat state. The damage itself is not applied to
// health: the values update carries the health the server decided (COMBAT-R1-NOTES.md, "What is approximated").
func (w *World) ApplyAttackRound(r combat.AttackRound, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if o := w.unitLocked(r.Attacker); o != nil {
		o.InCombat = true
		w.touchLocked(o, now)
	}
	if o := w.unitLocked(r.Victim); o != nil {
		o.InCombat = true
		w.touchLocked(o, now)
	}
}

// ApplySpellCast applies SMSG_SPELL_START (go false) or SMSG_SPELL_GO (go true) for a cast whose caster the model
// holds: the start records the cast, the go ends it.
func (w *World) ApplySpellCast(c *combat.SpellCast, goPacket bool, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.unitLocked(c.CasterUnit)
	if o == nil {
		return
	}
	if goPacket {
		if o.Cast != nil && (o.Cast.CastID == c.CastID || o.Cast.SpellID == c.SpellID) {
			o.Cast = nil
		}
		w.touchLocked(o, now)
		return
	}
	o.Cast = &CastState{
		SpellID:  c.SpellID,
		CastID:   c.CastID,
		Target:   c.Target.Unit,
		Started:  now,
		CastTime: time.Duration(c.CastTime) * time.Millisecond,
	}
	w.touchLocked(o, now)
}

// ApplySpellFailure ends a cast the model recorded (a failure or an interruption).
func (w *World) ApplySpellFailure(f combat.SpellFailure, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.unitLocked(f.Caster)
	if o == nil {
		return
	}
	if o.Cast != nil && (o.Cast.CastID == f.CastID || o.Cast.SpellID == f.SpellID) {
		o.Cast = nil
	}
	w.touchLocked(o, now)
}

// ApplyCooldowns applies SMSG_SPELL_COOLDOWN: each entry starts (or replaces) that spell's cooldown for the caster.
func (w *World) ApplyCooldowns(c combat.Cooldowns, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.unitLocked(c.Caster)
	if o == nil {
		return
	}
	if o.Cooldowns == nil {
		o.Cooldowns = make(map[int32]time.Time)
	}
	for _, cd := range c.List {
		if _, exists := o.Cooldowns[int32(cd.SpellID)]; !exists && len(o.Cooldowns) >= maxCooldownsPerUnit {
			continue
		}
		// 0 is not "no cooldown": it is the spell's own cooldown, whose length this client does not carry. Store it
		// as active with no known end (a zero EndsAt) instead of an end that has already passed.
		end := time.Time{}
		if cd.ForcedCooldown > 0 {
			end = now.Add(time.Duration(cd.ForcedCooldown) * time.Millisecond)
		}
		o.Cooldowns[int32(cd.SpellID)] = end
	}
	w.touchLocked(o, now)
}

// ClearCooldown applies SMSG_CLEAR_COOLDOWN / SMSG_COOLDOWN_EVENT: the spell's cooldown is over.
func (w *World) ClearCooldown(guid wire.GUID, spellID int32, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.unitLocked(guid)
	if o == nil {
		return
	}
	delete(o.Cooldowns, spellID)
	w.touchLocked(o, now)
}

// ClearCooldowns applies SMSG_CLEAR_COOLDOWNS: every listed spell's cooldown is over.
func (w *World) ClearCooldowns(guid wire.GUID, spells []int32, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.unitLocked(guid)
	if o == nil {
		return
	}
	for _, id := range spells {
		delete(o.Cooldowns, id)
	}
	w.touchLocked(o, now)
}

// ApplyCancelCombat clears a unit's combat state (SMSG_CANCEL_COMBAT is empty, CombatPackets.h:135-141).
func (w *World) ApplyCancelCombat(guid wire.GUID, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.unitLocked(guid)
	if o == nil {
		return
	}
	o.InCombat = false
	o.Attacking = wire.EmptyGUID
	o.AttackedBy = wire.EmptyGUID
	w.touchLocked(o, now)
}

// Combat returns a snapshot of one unit's combat state. Auras are copied, nearest expiry last, so the caller can
// read them without the lock.
func (w *World) Combat(guid wire.GUID) (CombatState, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	o := w.objects[guid]
	if o == nil || !o.Info.HasUnit {
		return CombatState{}, false
	}
	power, maxPower := o.Info.PowerValue()
	cs := CombatState{
		Attacking:  o.Attacking,
		AttackedBy: o.AttackedBy,
		InCombat:   o.InCombat,
		Dead:       o.Dead,
		Health:     o.Info.Health,
		MaxHealth:  o.Info.MaxHealth,
		Power:      power,
		MaxPower:   maxPower,
		PowerType:  o.Info.DisplayPower,
		Auras:      make([]Aura, 0, len(o.Auras)),
	}
	for _, a := range o.Auras {
		cs.Auras = append(cs.Auras, *a)
	}
	sort.Slice(cs.Auras, func(i, j int) bool {
		if cs.Auras[i].Slot != cs.Auras[j].Slot {
			return cs.Auras[i].Slot < cs.Auras[j].Slot
		}
		return cs.Auras[i].SpellID < cs.Auras[j].SpellID
	})
	if o.Cast != nil {
		c := *o.Cast
		cs.Cast = &c
	}
	cs.Cooldowns = make([]CooldownState, 0, len(o.Cooldowns))
	for id, end := range o.Cooldowns {
		cs.Cooldowns = append(cs.Cooldowns, CooldownState{SpellID: id, EndsAt: end})
	}
	sort.Slice(cs.Cooldowns, func(i, j int) bool { return cs.Cooldowns[i].SpellID < cs.Cooldowns[j].SpellID })
	return cs, true
}

// AurasOf returns the auras of a unit that are still up at now, in slot order.
func (w *World) AurasOf(guid wire.GUID, now time.Time) []Aura {
	cs, ok := w.Combat(guid)
	if !ok {
		return nil
	}
	out := cs.Auras[:0]
	for _, a := range cs.Auras {
		if a.Active(now) {
			out = append(out, a)
		}
	}
	return out
}

// DeadAt reports whether a unit is known dead: health zero in its values, or a swing that said so.
func (w *World) DeadAt(guid wire.GUID) bool {
	cs, ok := w.Combat(guid)
	return ok && cs.Dead
}

// applyDeathLocked derives the dead flag from the values a merge just stored: a unit whose health is zero is dead
// (Unit::setDeathState sets health 0, Unit.cpp:9584-9587), and a unit whose health came back is alive again (a
// creature respawn, Creature.cpp:2411-2415).
func applyDeathLocked(o *Object) {
	if !o.Info.HasUnit {
		return
	}
	if o.Info.Health <= 0 && o.Info.MaxHealth > 0 {
		o.Dead = true
		return
	}
	if o.Info.Health > 0 {
		o.Dead = false
	}
}

// InCombatFromFlags reports whether a unit's decoded unit flags carry UNIT_FLAG_IN_COMBAT (UnitDefines.h:186).
func InCombatFromFlags(flags uint32) bool { return flags&0x00080000 != 0 }
