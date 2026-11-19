package objects

// The ghost and death state of the world model: the aura 8326 is what marks a released spirit, because
// Player::BuildPlayerRepop gives a ghost 1 health (Player.cpp:4714-4715) and only the aura says it is still dead
// (Player.cpp:4688, :4767). The state is fed by the aura updates and the values updates the model already applies.

import (
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/death"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func deathTestPlayer(health int64) Info {
	info := Info{Type: TypePlayer, HasObject: true, HasUnit: true, Health: health, MaxHealth: 60}
	info.f.all()
	return info
}

func deathTestUnit(health int64) Info {
	info := Info{Type: TypeUnit, HasObject: true, HasUnit: true, Health: health, MaxHealth: 42}
	info.f.all()
	return info
}

// deathHealth is the values form of a health change: only the health field is present.
func deathHealth(health int64) Info {
	info := Info{HasUnit: true, Health: health}
	info.f.Health = true
	return info
}

// TestGhostStateFollowsTheAura: a player that dies is dead, the release's ghost aura keeps it dead with health 1,
// and the aura's removal brings it back and counts one resurrection.
func TestGhostStateFollowsTheAura(t *testing.T) {
	w := NewWorld(0)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	self := wire.GUID{Low: 0x1001, High: 2 << 58}
	w.Apply(&Update{Blocks: []WorldFragment{{
		GUID: self, ObjectType: TypePlayer, Info: deathTestPlayer(0), ValuesComplete: true,
	}}}, now)

	d, ok := w.DeathOf(self)
	if !ok || !d.Dead || d.Ghost {
		t.Fatalf("the corpse state: %+v (ok %v)", d, ok)
	}
	// Release: the server sets health 1 and applies 8326.
	w.Apply(&Update{Blocks: []WorldFragment{{
		GUID: self, Values: true, Info: deathHealth(1), ValuesComplete: true,
	}}}, now)
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: self, Auras: []combat.AuraInfo{{
		Slot: 2, Data: &combat.AuraData{SpellID: death.GhostAura, Flags: 0x11},
	}}}, now)
	d, _ = w.DeathOf(self)
	if !d.Ghost || !d.Dead || d.Health != 1 {
		t.Fatalf("the ghost state: %+v", d)
	}
	if w.Resurrections() != 0 {
		t.Fatalf("%d resurrection(s) on release", w.Resurrections())
	}
	// Resurrection: the aura goes and the health is restored.
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: self, Auras: []combat.AuraInfo{{Slot: 2}}}, now)
	w.Apply(&Update{Blocks: []WorldFragment{{
		GUID: self, Values: true, Info: deathHealth(30), ValuesComplete: true,
	}}}, now)
	d, _ = w.DeathOf(self)
	if d.Ghost || d.Dead || d.Health != 30 {
		t.Fatalf("the resurrected state: %+v", d)
	}
	if !d.ResurrectedAt.Equal(now) {
		t.Fatalf("the resurrection time is %v", d.ResurrectedAt)
	}
	if w.Resurrections() != 1 {
		t.Fatalf("%d resurrection(s), want one", w.Resurrections())
	}
	// A second aura update without the ghost aura changes nothing.
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: self, Auras: []combat.AuraInfo{{Slot: 0}}}, now)
	if w.Resurrections() != 1 {
		t.Fatalf("%d resurrection(s) after an unrelated aura update", w.Resurrections())
	}
}

// TestGhostAuraOnACreatureIsNotAGhost: only a player can be a ghost; the aura on a creature is just an aura.
func TestGhostAuraOnACreatureIsNotAGhost(t *testing.T) {
	w := NewWorld(0)
	now := time.Unix(0, 0)
	guid := wire.GUID{Low: 0x2001, High: 8 << 58}
	w.Apply(&Update{Blocks: []WorldFragment{{
		GUID: guid, ObjectType: TypeUnit, Info: deathTestUnit(0), ValuesComplete: true,
	}}}, now)
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: guid, Auras: []combat.AuraInfo{{
		Slot: 1, Data: &combat.AuraData{SpellID: death.GhostAura},
	}}}, now)
	d, _ := w.DeathOf(guid)
	if d.Ghost {
		t.Fatal("a creature with the ghost aura is a ghost")
	}
	if !d.Dead {
		t.Fatal("a creature at 0 health is alive")
	}
	if w.Resurrections() != 0 {
		t.Fatalf("%d resurrection(s)", w.Resurrections())
	}
}

// TestCreatureRespawnIsNotAResurrection: a creature's death and respawn are health values only; the resurrection
// counter belongs to ghosts.
func TestCreatureRespawnIsNotAResurrection(t *testing.T) {
	w := NewWorld(0)
	now := time.Unix(0, 0)
	guid := wire.GUID{Low: 0x3001, High: 8 << 58}
	w.Apply(&Update{Blocks: []WorldFragment{{
		GUID: guid, ObjectType: TypeUnit, Info: deathTestUnit(0), ValuesComplete: true,
	}}}, now)
	if !w.DeadAt(guid) {
		t.Fatal("the creature is not dead at 0 health")
	}
	w.Apply(&Update{Blocks: []WorldFragment{{
		GUID: guid, Values: true, Info: deathHealth(42), ValuesComplete: true,
	}}}, now)
	if w.DeadAt(guid) {
		t.Fatal("the creature is still dead after its respawn")
	}
	if w.Resurrections() != 0 {
		t.Fatalf("%d resurrection(s) for a respawn", w.Resurrections())
	}
}

// TestGhostAuraLostInAnUpdateAllIsAResurrection: a full aura update drops every aura it does not name
// (AuraUpdate::UpdateAll), so a ghost whose update-all list lacks 8326 has been resurrected.
func TestGhostAuraLostInAnUpdateAllIsAResurrection(t *testing.T) {
	w := NewWorld(0)
	now := time.Unix(0, 0)
	guid := wire.GUID{Low: 0x4001, High: 2 << 58}
	w.Apply(&Update{Blocks: []WorldFragment{{
		GUID: guid, ObjectType: TypePlayer, Info: deathTestPlayer(1), ValuesComplete: true,
	}}}, now)
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: guid, Auras: []combat.AuraInfo{{
		Slot: 2, Data: &combat.AuraData{SpellID: death.GhostAura},
	}}}, now)
	if !w.GhostAt(guid) {
		t.Fatal("the ghost aura did not mark the ghost")
	}
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: guid, UpdateAll: true}, now)
	if w.GhostAt(guid) {
		t.Fatal("the ghost survived an empty update-all")
	}
	if w.Resurrections() != 1 {
		t.Fatalf("%d resurrection(s), want one", w.Resurrections())
	}
}

// TestCombatSnapshotCarriesTheGhost: the fight's snapshot can see the ghost flag (it is how a fighter knows the
// character died rather than faded).
func TestCombatSnapshotCarriesTheGhost(t *testing.T) {
	w := NewWorld(0)
	now := time.Unix(0, 0)
	guid := wire.GUID{Low: 0x5001, High: 2 << 58}
	w.Apply(&Update{Blocks: []WorldFragment{{
		GUID: guid, ObjectType: TypePlayer, Info: deathTestPlayer(1), ValuesComplete: true,
	}}}, now)
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: guid, Auras: []combat.AuraInfo{{
		Slot: 2, Data: &combat.AuraData{SpellID: death.GhostAura},
	}}}, now)
	cs, ok := w.Combat(guid)
	if !ok || !cs.Ghost || !cs.Dead {
		t.Fatalf("the combat snapshot: %+v (ok %v)", cs, ok)
	}
}
