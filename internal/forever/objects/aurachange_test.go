package objects

// The aura update's own report: what an update changed, so the session's combat log can write one row per change
// (a refresh is no row). The changes are the model's own answer; nothing here reads the combat log.

import (
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func auraData(spell int32, caster *wire.GUID) *combat.AuraData {
	return &combat.AuraData{SpellID: spell, Applications: 1, CastLevel: 1, CastUnit: caster}
}

func TestAuraUpdateReportsChanges(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(1000, 0)
	caster := testSelf

	changes := w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testCreature, Auras: []combat.AuraInfo{
		{Slot: 1, Data: auraData(17, &caster)},
		{Slot: 2, Data: auraData(139, nil)},
	}}, now)
	if len(changes) != 2 {
		t.Fatalf("%d changes for two new auras: %+v", len(changes), changes)
	}
	for _, c := range changes {
		if c.Kind != AuraAdded || c.Unit != testCreature {
			t.Fatalf("aura change %+v, want added on the creature", c)
		}
	}
	if changes[0].SpellID != 17 || changes[0].Caster != testSelf || changes[1].SpellID != 139 {
		t.Fatalf("changes %+v", changes)
	}

	// A refresh of a known slot is a change of kind refreshed: the log writes no row for it.
	changes = w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testCreature, Auras: []combat.AuraInfo{
		{Slot: 1, Data: auraData(17, &caster)},
	}}, now.Add(time.Second))
	if len(changes) != 1 || changes[0].Kind != AuraRefreshed || changes[0].SpellID != 17 {
		t.Fatalf("refresh changes %+v", changes)
	}

	// A cleared slot is removed, and the row still names the aura that was there.
	changes = w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testCreature, Auras: []combat.AuraInfo{{Slot: 2}}}, now)
	if len(changes) != 1 || changes[0].Kind != AuraRemoved || changes[0].SpellID != 139 {
		t.Fatalf("removal changes %+v", changes)
	}

	// A cleared slot the unit never had changes nothing: an absent aura is not a removal.
	changes = w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testCreature, Auras: []combat.AuraInfo{{Slot: 9}}}, now)
	if len(changes) != 0 {
		t.Fatalf("a removal of an absent slot reported %+v", changes)
	}
}

// TestAuraUpdateAllReportsItsClearances: a full update names every aura the unit has, so the slots it leaves out are
// removals of their own.
func TestAuraUpdateAllReportsItsClearances(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(1000, 0)
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testCreature, Auras: []combat.AuraInfo{
		{Slot: 1, Data: auraData(17, nil)},
		{Slot: 2, Data: auraData(139, nil)},
	}}, now)
	changes := w.ApplyAuraUpdate(&combat.AuraUpdate{UpdateAll: true, Unit: testCreature, Auras: []combat.AuraInfo{
		{Slot: 2, Data: auraData(139, nil)},
	}}, now.Add(time.Second))
	var removed, other int
	for _, c := range changes {
		switch c.Kind {
		case AuraRemoved:
			removed++
			if c.SpellID != 17 {
				t.Fatalf("the full update removed %d, want 17", c.SpellID)
			}
		default:
			other++
		}
	}
	if removed != 1 || other != 1 {
		t.Fatalf("changes %+v, want one removal and the listed slot", changes)
	}
}

// TestAuraUpdateUnknownUnitChangesNothing: a unit the model does not hold has no slots to report.
func TestAuraUpdateUnknownUnitChangesNothing(t *testing.T) {
	w := unitWorld(t)
	changes := w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: wire.GUID{Low: 0x9999}, Auras: []combat.AuraInfo{
		{Slot: 1, Data: auraData(17, nil)},
	}}, time.Unix(1000, 0))
	if len(changes) != 0 {
		t.Fatalf("an unknown unit reported %+v", changes)
	}
}
