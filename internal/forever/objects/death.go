package objects

// The death state of every unit in the world model. Round 4 derived death from health alone; a released spirit is a
// different state: Player::BuildPlayerRepop sets health 1 on the ghost (Player.cpp:4714-4715), and the aura 8326
// (SPELL_AURA_GHOST, applied at :4688 and removed at :4767) is what marks it. The model therefore carries both:
//
//   - Dead: health 0 with a real maximum, or the ghost aura;
//   - Ghost: the ghost aura 8326 is up on a player;
//   - ResurrectedAt: the last time the unit went from ghost to alive (its ghost aura went away and its health was
//     above 0).
//
// What the client cannot see is PLAYER_FLAGS_GHOST (it lives in the player block this client leaves to its declared
// size, see OBJECTS-R1-NOTES.md), so the aura is the unit-level signal. Party members additionally carry
// StatusDead/StatusGhost in the party model, which is round 5's state.

import (
	"time"

	"github.com/pjerra/forever-agent/internal/forever/death"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// DeathState is a snapshot of one unit's life state.
type DeathState struct {
	Dead   bool
	Ghost  bool
	Health int64
	Max    int64
	// ResurrectedAt is when a ghost came back (zero when it never did).
	ResurrectedAt time.Time
}

// isPlayerType reports the object types that can be a player (and therefore a ghost).
func isPlayerType(t uint8) bool { return t == TypePlayer || t == TypeActivePlayer }

// hasGhostAuraLocked reports whether the unit's aura slots carry SPELL_AURA_GHOST.
func hasGhostAuraLocked(o *Object) bool {
	for _, a := range o.Auras {
		if a.SpellID == death.GhostAura {
			return true
		}
	}
	return false
}

// applyGhostLocked recomputes the ghost flag after an aura change, and records the resurrection when a ghost came
// back: the aura went away and the unit's health is above 0. A unit that never had the aura is not "resurrected" by
// an unrelated aura update.
func applyGhostLocked(o *Object, now time.Time) bool {
	was := o.Ghost
	o.Ghost = o.Info.HasUnit && isPlayerType(o.Type) && hasGhostAuraLocked(o)
	back := was && !o.Ghost && o.Info.Health > 0
	if back {
		o.ResurrectedAt = now
	}
	applyDeathLocked(o)
	return back
}

// DeathOf returns a snapshot of one unit's death state.
func (w *World) DeathOf(guid wire.GUID) (DeathState, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	o := w.objects[guid]
	if o == nil || !o.Info.HasUnit {
		return DeathState{}, false
	}
	return DeathState{
		Dead:          o.Dead,
		Ghost:         o.Ghost,
		Health:        o.Info.Health,
		Max:           o.Info.MaxHealth,
		ResurrectedAt: o.ResurrectedAt,
	}, true
}

// GhostAt reports whether a unit is a released ghost.
func (w *World) GhostAt(guid wire.GUID) bool {
	d, ok := w.DeathOf(guid)
	return ok && d.Ghost
}

// Resurrections is the number of units this model saw come back from the ghost state (the report prints it).
func (w *World) Resurrections() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.resurrections
}

// CorpseGUID returns the GUID of a corpse object whose owner is owner, when the model holds one. The reclaim packet
// carries it, though the server's handler ignores the value (MiscHandler.cpp:472, ReclaimCorpse& /*packet*/).
func (w *World) CorpseGUID(owner wire.GUID) (wire.GUID, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, o := range w.objects {
		if o.Type == TypeCorpse && o.Info.HasCorpse && o.Info.Owner == owner {
			return o.GUID, true
		}
	}
	return wire.EmptyGUID, false
}
