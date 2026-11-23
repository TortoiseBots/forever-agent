package world

// combat_near (Forever #111): the counts come from the session event log, the radius is 2D against the world model,
// and the window is mono_ms, so a wall-clock step can move neither the rows nor the radius.

import (
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// combatNearSession is newPortSession with a player GUID for the character, so the "not the agent" and "source is a
// player" rules are tested rather than hidden by the helper's zero-type GUID.
func combatNearSession(t *testing.T) *session {
	t.Helper()
	s := newPortSession(&portRecorder{now: time.Now()})
	s.self = playerGUID(0x1000)
	s.selfSeen = true
	return s
}

// combatHit appends one event-log hit row: both GUIDs are eventGUID text and mono is the row's steady clock.
func combatHit(s *session, kind string, src, dst wire.GUID, party bool, mono int64, name string) {
	s.EventLog().Append(Event{
		Kind:       kind,
		Source:     name,
		SourceGUID: eventGUID(src),
		TargetGUID: eventGUID(dst),
		Party:      party,
		MonoMS:     mono,
	})
}

// combatNearMob is a creature GUID (HighGuid::Creature is 8, ObjectGuid.h:118), the type combat_near takes for a
// target.
func combatNearMob(low uint64) wire.GUID {
	return wire.GUID{High: 8 << 58, Low: low}
}

// TestCombatNearCountsOtherPlayersOnly: rows from the character itself and from a party member are out of the
// default who=others answer; the stranger's row is the one counted.
func TestCombatNearCountsOtherPlayersOnly(t *testing.T) {
	s := combatNearSession(t)
	mob := combatNearMob(1)
	addUnit(t, s, mob, 6, 15, 20, 30, 40, 40, 2, 0)
	now := events.MonoMS()
	self, member, stranger := s.self, playerGUID(0x2000), playerGUID(0x3000)
	combatHit(s, events.KindMelee, self, mob, true, now-3000, "Self")
	combatHit(s, events.KindMelee, member, mob, true, now-2000, "Member")
	combatHit(s, events.KindSpellDamage, stranger, mob, false, now-1000, "Stranger")

	got, err := s.serveCombatNear(serveCombatNearParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || got.Mobs != 1 || got.Unplaced != 0 {
		t.Fatalf("count %d mobs %d unplaced %d, want 1/1/0: %+v", got.Count, got.Mobs, got.Unplaced, got)
	}
	if len(got.Players) != 1 {
		t.Fatalf("players %+v", got.Players)
	}
	p := got.Players[0]
	if wire.GUID(p.GUID) != stranger || p.Name != "Stranger" || p.Hits != 1 || p.FirstMS != now-1000 || p.LastMS != now-1000 {
		t.Fatalf("player row %+v", p)
	}
}

// TestCombatNearPartyMode: who=party is the mirror image: the party member's row counts, the stranger's does not,
// and the character's own row never counts.
func TestCombatNearPartyMode(t *testing.T) {
	s := combatNearSession(t)
	mob := combatNearMob(1)
	addUnit(t, s, mob, 6, 15, 20, 30, 40, 40, 2, 0)
	now := events.MonoMS()
	member, stranger := playerGUID(0x2000), playerGUID(0x3000)
	combatHit(s, events.KindPeriodic, s.self, mob, true, now-3000, "Self")
	combatHit(s, events.KindMelee, member, mob, true, now-2000, "Member")
	combatHit(s, events.KindMelee, stranger, mob, false, now-1000, "Stranger")

	got, err := s.serveCombatNear(serveCombatNearParams{Who: "party"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || len(got.Players) != 1 || wire.GUID(got.Players[0].GUID) != member {
		t.Fatalf("party count %d players %+v", got.Count, got.Players)
	}
}

// TestCombatNearRadiusAndWindow: a mob 40 yd away, a row 70 s old and a mob 35 yd away in 3D but 25 yd in 2D. The
// first two are out, the ledge mob is in (2D), and the players sort by hits then name.
func TestCombatNearRadiusAndWindow(t *testing.T) {
	s := combatNearSession(t)
	near, far, ledge := combatNearMob(1), combatNearMob(2), combatNearMob(3)
	addUnit(t, s, near, 6, 15, 20, 30, 40, 40, 2, 0)    // 5 yd
	addUnit(t, s, far, 6, 50, 20, 30, 40, 40, 2, 0)     // 40 yd
	addUnit(t, s, ledge, 6, 35, 20, 54.5, 40, 40, 2, 0) // 25 yd 2D, 35 yd 3D
	now := events.MonoMS()
	zoe, abe, zed, old := playerGUID(0x3000), playerGUID(0x4000), playerGUID(0x5000), playerGUID(0x6000)
	combatHit(s, events.KindMelee, zoe, near, false, now-5000, "Zoe")
	combatHit(s, events.KindMelee, zoe, far, false, now-4000, "Zoe")
	combatHit(s, events.KindSpellGo, zoe, ledge, false, now-1000, "Zoe")
	combatHit(s, events.KindMelee, abe, near, false, now-3000, "Abe")
	combatHit(s, events.KindMelee, zed, near, false, now-2000, "zed")
	combatHit(s, events.KindMelee, old, near, false, now-70000, "Old")

	got, err := s.serveCombatNear(serveCombatNearParams{Radius: new(float32(30)), WindowS: new(60)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 4 || got.Mobs != 2 {
		t.Fatalf("count %d mobs %d, want 4 hits on 2 mobs (near and the 2D ledge mob): %+v", got.Count, got.Mobs, got)
	}
	names := make([]string, len(got.Players))
	for i, p := range got.Players {
		names[i] = p.Name
	}
	if len(names) != 3 || names[0] != "Zoe" || names[1] != "Abe" || names[2] != "zed" {
		t.Fatalf("player order %v, want [Zoe Abe zed] (hits desc, then name)", names)
	}
	z := got.Players[0]
	if z.Hits != 2 || z.FirstMS != now-5000 || z.LastMS != now-1000 {
		t.Fatalf("Zoe row %+v", z)
	}
}

// TestCombatNearUnplacedTarget: a creature GUID the model does not hold is skipped and counted; a target that is
// not a creature at all (a player) is skipped silently.
func TestCombatNearUnplacedTarget(t *testing.T) {
	s := combatNearSession(t)
	now := events.MonoMS()
	stranger := playerGUID(0x3000)
	combatHit(s, events.KindMelee, stranger, combatNearMob(99), false, now-1000, "Stranger")
	combatHit(s, events.KindMelee, stranger, playerGUID(0x77), false, now-1000, "Stranger")

	got, err := s.serveCombatNear(serveCombatNearParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 0 || got.Mobs != 0 || got.Unplaced != 1 || len(got.Players) != 0 {
		t.Fatalf("answer %+v, want count 0, unplaced 1, no players", got)
	}
}

// TestCombatNearParamBounds: radius 5..100 (default 30), window 1..600 (default 60) and who others|party; the
// bounds themselves are accepted and everything outside them is refused before any work.
func TestCombatNearParamBounds(t *testing.T) {
	s := combatNearSession(t)
	for _, p := range []serveCombatNearParams{
		{Radius: new(float32(0))},
		{Radius: new(float32(101))},
		{WindowS: new(0)},
		{WindowS: new(601)},
		{Who: "friends"},
		{X: new(float32(1))},
	} {
		if _, err := s.serveCombatNear(p); err == nil {
			t.Fatalf("%+v was accepted", p)
		}
	}
	for _, p := range []serveCombatNearParams{
		{Radius: new(float32(5))},
		{Radius: new(float32(100))},
		{WindowS: new(1)},
		{WindowS: new(600)},
		{Who: "party"},
		{X: new(float32(10)), Y: new(float32(20)), Z: new(float32(30))},
	} {
		if _, err := s.serveCombatNear(p); err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
	}
}
