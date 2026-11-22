package objects

// Tests of the combat half of the world model: auras with expiry and removal, the cast in progress, who attacks
// whom, power updates, death from the values and from an attack stop, and the bounds.

import (
	"fmt"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	testSelf     = wire.GUID{Low: 0x1122, High: 0x0801180000000000}
	testCreature = wire.GUID{Low: 0x3344, High: 0x2001180000000000}
)

// unitWorld returns a model holding one player and one creature, health 60/60 and 42/42.
func unitWorld(t *testing.T) *World {
	t.Helper()
	w := NewWorld(0)
	block := func(guid wire.GUID, health, max int64) *Update {
		t.Helper()
		u := &Update{Blocks: []WorldFragment{{
			GUID: guid, ObjectType: TypeUnit, Values: false,
			ValuesComplete: true,
			Info: Info{
				HasObject: true, HasUnit: true,
				Entry: 1, Health: health, MaxHealth: max, Level: 1, DisplayPower: 0,
			},
		}}}
		u.Blocks[0].Info.f.all()
		return u
	}
	w.Apply(block(testSelf, 60, 60), time.Unix(0, 0))
	w.Apply(block(testCreature, 42, 42), time.Unix(0, 0))
	return w
}

// TestCombatAuraLifetime applies an aura with a duration, checks its expiry moves with the clock, and removes it.
func TestCombatAuraLifetime(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(1000, 0)
	dur := uint32(5000)
	rem := uint32(3000)
	castUnit := testSelf
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testCreature, Auras: []combat.AuraInfo{
		{Slot: 4, Data: &combat.AuraData{SpellID: 772, Flags: combat.AuraHasDuration, Applications: 1, CastLevel: 1,
			CastUnit: &castUnit, Duration: &dur, Remaining: &rem}},
	}}, now)

	cs, ok := w.Combat(testCreature)
	if !ok || len(cs.Auras) != 1 {
		t.Fatalf("the model holds %d auras (%v)", len(cs.Auras), ok)
	}
	a := cs.Auras[0]
	if a.Slot != 4 || a.SpellID != 772 || !a.HasDuration || a.Duration != 5*time.Second || a.Remaining != 3*time.Second {
		t.Fatalf("aura %+v", a)
	}
	if a.Caster != testSelf {
		t.Fatalf("caster %v", a.Caster)
	}
	if a.ExpiresAt.Sub(now) != 3*time.Second {
		t.Fatalf("expiry is %v after the record, want the remaining 3 s", a.ExpiresAt.Sub(now))
	}
	if got := w.AurasOf(testCreature, now.Add(2*time.Second)); len(got) != 1 {
		t.Fatalf("the aura is gone after 2 s: %v", got)
	}
	if got := w.AurasOf(testCreature, now.Add(3*time.Second)); len(got) != 0 {
		t.Fatalf("the aura is still up after its remaining 3 s: %v", got)
	}

	// The removal form deletes the slot.
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testCreature, Auras: []combat.AuraInfo{{Slot: 4}}}, now)
	if cs, _ := w.Combat(testCreature); len(cs.Auras) != 0 {
		t.Fatalf("the removed aura is still in the model: %v", cs.Auras)
	}
}

// TestCombatAuraWithoutDurationNeverExpires checks the bits decide, not the flags: no Duration/Remaining means a
// permanent aura.
func TestCombatAuraWithoutDurationNeverExpires(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(0, 0)
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testSelf, Auras: []combat.AuraInfo{
		{Slot: 0, Data: &combat.AuraData{SpellID: 6673, Flags: combat.AuraPositive | combat.AuraSelfCast}},
	}}, now)
	if got := w.AurasOf(testSelf, now.Add(24*time.Hour)); len(got) != 1 {
		t.Fatalf("a permanent aura expired: %v", got)
	}
}

// TestCombatUpdateAllReplacesTheSet: an UpdateAll list is the unit's whole set, so slots it does not name are gone.
func TestCombatUpdateAllReplacesTheSet(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(0, 0)
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testSelf, Auras: []combat.AuraInfo{
		{Slot: 0, Data: &combat.AuraData{SpellID: 100}},
		{Slot: 1, Data: &combat.AuraData{SpellID: 101}},
	}}, now)
	if cs, _ := w.Combat(testSelf); len(cs.Auras) != 2 {
		t.Fatalf("%d auras after the merge", len(cs.Auras))
	}
	w.ApplyAuraUpdate(&combat.AuraUpdate{UpdateAll: true, Unit: testSelf, Auras: []combat.AuraInfo{
		{Slot: 1, Data: &combat.AuraData{SpellID: 102}},
	}}, now)
	cs, _ := w.Combat(testSelf)
	if len(cs.Auras) != 1 || cs.Auras[0].Slot != 1 || cs.Auras[0].SpellID != 102 {
		t.Fatalf("after UpdateAll: %+v", cs.Auras)
	}
}

// TestCombatAuraBound caps the slots: a 301st new slot is dropped, an existing slot still updates.
func TestCombatAuraBound(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(0, 0)
	for i := range maxAurasPerUnit + 5 {
		w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testSelf, Auras: []combat.AuraInfo{
			{Slot: uint16(i), Data: &combat.AuraData{SpellID: int32(1000 + i)}},
		}}, now)
	}
	cs, _ := w.Combat(testSelf)
	if len(cs.Auras) != maxAurasPerUnit {
		t.Fatalf("%d auras, want the cap %d", len(cs.Auras), maxAurasPerUnit)
	}
	// Slot 0 already exists: a new spell still updates it.
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testSelf, Auras: []combat.AuraInfo{
		{Slot: 0, Data: &combat.AuraData{SpellID: 4242}},
	}}, now)
	cs, _ = w.Combat(testSelf)
	if cs.Auras[0].SpellID != 4242 {
		t.Fatalf("the existing slot did not update: %+v", cs.Auras[0])
	}
}

// TestCombatCastLifecycle records a cast, ends it with the go, and ends another with a failure.
func TestCombatCastLifecycle(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(0, 0)
	castID := combat.CastGUID(0, 133, 1)
	w.ApplySpellCast(&combat.SpellCast{CasterUnit: testSelf, SpellID: 133, CastID: castID, CastTime: 1500,
		Target: combat.SpellTarget{Unit: testCreature}}, false, now)
	cs, _ := w.Combat(testSelf)
	if cs.Cast == nil || cs.Cast.SpellID != 133 || cs.Cast.CastID != castID || cs.Cast.CastTime != 1500*time.Millisecond {
		t.Fatalf("cast %+v", cs.Cast)
	}
	if want := now.Add(1500 * time.Millisecond); !cs.Cast.Ends().Equal(want) {
		t.Fatalf("the cast ends at %v, want %v", cs.Cast.Ends(), want)
	}
	if cs.Cast.Target != testCreature {
		t.Fatalf("target %v", cs.Cast.Target)
	}
	w.ApplySpellCast(&combat.SpellCast{CasterUnit: testSelf, SpellID: 133, CastID: castID}, true, now.Add(time.Second))
	if cs, _ := w.Combat(testSelf); cs.Cast != nil {
		t.Fatalf("the go did not end the cast: %+v", cs.Cast)
	}

	// A failure ends a cast too, matched by cast id.
	w.ApplySpellCast(&combat.SpellCast{CasterUnit: testSelf, SpellID: 133, CastID: castID}, false, now)
	w.ApplySpellFailure(combat.SpellFailure{Caster: testSelf, CastID: castID, SpellID: 133, Reason: 118}, now)
	if cs, _ := w.Combat(testSelf); cs.Cast != nil {
		t.Fatalf("the failure did not end the cast: %+v", cs.Cast)
	}
}

// TestCombatRelationsAndCombatFlag follows the attack start/stop pair and the unit flag.
func TestCombatRelationsAndCombatFlag(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(0, 0)
	w.ApplyAttackStart(combat.AttackStart{Attacker: testSelf, Victim: testCreature}, now)
	self, _ := w.Combat(testSelf)
	creature, _ := w.Combat(testCreature)
	if self.Attacking != testCreature || !self.InCombat {
		t.Fatalf("self %+v", self)
	}
	if creature.AttackedBy != testSelf || !creature.InCombat {
		t.Fatalf("creature %+v", creature)
	}
	w.ApplyAttackStop(combat.AttackStop{Attacker: testSelf, Victim: testCreature}, now)
	self, _ = w.Combat(testSelf)
	creature, _ = w.Combat(testCreature)
	if !self.Attacking.IsZero() || self.InCombat || !creature.AttackedBy.IsZero() {
		t.Fatalf("after the stop: self %+v creature %+v", self, creature)
	}

	// The unit's own flags set the in-combat state when a values update carries them.
	w.Apply(&Update{Blocks: []WorldFragment{{
		GUID: testSelf, Values: true, ValuesComplete: true,
		Info: func() Info {
			var i Info
			i.f.Flags = true
			i.Flags = 0x00080000
			return i
		}(),
	}}}, now)
	if self, _ := w.Combat(testSelf); !self.InCombat {
		t.Fatal("UNIT_FLAG_IN_COMBAT in a values update did not set the state")
	}
	w.ApplyCancelCombat(testSelf, now)
	if self, _ := w.Combat(testSelf); self.InCombat {
		t.Fatal("SMSG_CANCEL_COMBAT did not clear the state")
	}
}

// TestCombatDeathFromValuesAndStops checks both ways a unit is known dead, and the respawn.
func TestCombatDeathFromValuesAndStops(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(0, 0)
	values := func(health int64) *Update {
		i := Info{}
		i.f.Health = true
		i.Health = health
		return &Update{Blocks: []WorldFragment{{GUID: testCreature, Values: true, ValuesComplete: true, Info: i}}}
	}
	w.Apply(values(0), now)
	if !w.DeadAt(testCreature) {
		t.Fatal("health 0 did not mark the creature dead")
	}
	w.Apply(values(42), now)
	if w.DeadAt(testCreature) {
		t.Fatal("a respawn (health back) left the creature dead")
	}
	// An attack stop with NowDead marks the attacker dead.
	w.ApplyAttackStop(combat.AttackStop{Attacker: testSelf, Victim: testCreature, NowDead: true}, now)
	if !w.DeadAt(testSelf) {
		t.Fatal("an attack stop with NowDead did not mark the attacker dead")
	}
	// A false NowDead says nothing.
	w.ApplyAttackStop(combat.AttackStop{Attacker: testCreature, Victim: testSelf, NowDead: false}, now)
	if w.DeadAt(testCreature) {
		t.Fatal("a false NowDead marked the attacker dead")
	}
}

// captureCreate parses one committed create block and returns the model update it came in.
func captureCreate(t *testing.T, name string) (*Update, WorldFragment) {
	t.Helper()
	fx := loadFixtures(t)
	u, err := ParseUpdateObject(fixturePayload(t, fx[name]))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(u.Blocks) != 1 {
		t.Fatalf("%s has %d blocks", name, len(u.Blocks))
	}
	return u, u.Blocks[0]
}

// TestCombatPowerUpdate is the power-slot test against the captured player create and a power update of a type the
// array does not index directly. The fixture is a mage (class 8): its create carries DisplayPower 0 (mana), Power
// [244 0 ...] and MaxPower [333 5 ...], and the server's slot order for the class is mana at slot 0 and combo points
// at slot 1 (ChrClassesXPowerTypes sorted by PowerType, DB2Stores.cpp:1171-1184 with :3580-3585). So a combo-point
// update (type 4) must land in Power[1], and the mana bar (244/333) must not move.
func TestCombatPowerUpdate(t *testing.T) {
	u, block := captureCreate(t, "create_player")
	w := NewWorld(0)
	w.Apply(u, time.Unix(0, 0))
	cs, ok := w.Combat(block.GUID)
	if !ok || cs.Power != 244 || cs.MaxPower != 333 || cs.PowerType != 0 {
		t.Fatalf("the capture's mana bar: %+v (%v)", cs, ok)
	}
	now := time.Unix(0, 0)
	w.ApplyPowerUpdate(&combat.PowerUpdate{GUID: block.GUID, Powers: []combat.PowerValue{{Type: powerCombo, Value: 3}}}, now)
	w.mu.Lock()
	if got := w.objects[block.GUID].Info.Power[1]; got != 3 {
		t.Errorf("combo points are in slot 1: got Power[1] = %d", got)
	}
	if got := w.objects[block.GUID].Info.Power[powerCombo]; got != 0 {
		t.Errorf("the power type was written into the slot array: Power[4] = %d", got)
	}
	w.mu.Unlock()
	cs, _ = w.Combat(block.GUID)
	if cs.Power != 244 || cs.MaxPower != 333 {
		t.Fatalf("a combo-point update moved the mana bar: %d/%d", cs.Power, cs.MaxPower)
	}
	w.ApplyPowerUpdate(&combat.PowerUpdate{GUID: block.GUID, Powers: []combat.PowerValue{{Type: powerMana, Value: 200}}}, now)
	cs, _ = w.Combat(block.GUID)
	if cs.Power != 200 || cs.MaxPower != 333 || cs.PowerType != 0 {
		t.Fatalf("a mana update: %d/%d type %d", cs.Power, cs.MaxPower, cs.PowerType)
	}
}

// TestCombatPowerWarriorRage is the live warrior's own create (its numbers from the 2026-11-18 run: class 1,
// DisplayPower 1 = rage, MaxPower [1000 5 ...]) and its MID-fight power update. Rage lives in slot 0 (1000 tenths is
// the 100-point bar); slot 1 is the 5 combo points. The bar must read 13.8/100.0, not 138/5.
func TestCombatPowerWarriorRage(t *testing.T) {
	w := NewWorld(0)
	info := Info{Type: TypePlayer, HasObject: true, HasUnit: true, Class: 1, DisplayPower: powerRage,
		Level: 2, Health: 92, MaxHealth: 92}
	info.Power[0], info.MaxPower[0], info.MaxPower[1] = 138, 1000, 5
	info.f.Class, info.f.DisplayPower, info.f.Power, info.f.MaxPower, info.f.Health = true, true, true, true, true
	info.f.MaxHealth, info.f.Level = true, true
	w.Apply(&Update{Blocks: []WorldFragment{{GUID: testSelf, ObjectType: TypePlayer, Info: info, ValuesComplete: true}}}, time.Unix(0, 0))

	cs, _ := w.Combat(testSelf)
	if cs.Power != 138 || cs.MaxPower != 1000 || cs.PowerType != powerRage {
		t.Fatalf("the rage bar: %d/%d type %d, want 138/1000 type 1", cs.Power, cs.MaxPower, cs.PowerType)
	}
	if got := combat.PowerText(int8(cs.PowerType), cs.Power, cs.MaxPower); got != "13.8/100.0" {
		t.Fatalf("the printed bar is %q, want \"13.8/100.0\"", got)
	}
	w.ApplyPowerUpdate(&combat.PowerUpdate{GUID: testSelf, Powers: []combat.PowerValue{{Type: powerRage, Value: 200}}}, time.Unix(0, 0))
	cs, _ = w.Combat(testSelf)
	if cs.Power != 200 || cs.MaxPower != 1000 {
		t.Fatalf("a rage update wrote %d/%d, want 200/1000", cs.Power, cs.MaxPower)
	}
	w.mu.Lock()
	if got := w.objects[testSelf].Info.Power[1]; got != 0 {
		t.Errorf("the rage type landed in the combo-point slot: Power[1] = %d", got)
	}
	w.mu.Unlock()
}

// TestCombatPowerCreatureSlots: a creature's slots are Creature::GetPowerIndex's (StatSystem.cpp:1207-1231): its
// display power at 0 whatever its type, combo points at 2. A race creature with DisplayPower 1 keeps its bar at
// Power[0].
func TestCombatPowerCreatureSlots(t *testing.T) {
	w := NewWorld(0)
	info := Info{Type: TypeUnit, HasObject: true, HasUnit: true, Class: 1, DisplayPower: powerRage,
		Health: 42, MaxHealth: 42}
	info.Power[0], info.MaxPower[0] = 50, 1000
	info.Power[2], info.MaxPower[2] = 3, 5
	info.f.Class, info.f.DisplayPower, info.f.Power, info.f.MaxPower, info.f.Health = true, true, true, true, true
	info.f.MaxHealth = true
	w.Apply(&Update{Blocks: []WorldFragment{{GUID: testCreature, ObjectType: TypeUnit, Info: info, ValuesComplete: true}}}, time.Unix(0, 0))

	cs, _ := w.Combat(testCreature)
	if cs.Power != 50 || cs.MaxPower != 1000 {
		t.Fatalf("the creature's bar: %d/%d, want the display power's 50/1000", cs.Power, cs.MaxPower)
	}
	w.ApplyPowerUpdate(&combat.PowerUpdate{GUID: testCreature, Powers: []combat.PowerValue{{Type: powerCombo, Value: 4}}}, time.Unix(0, 0))
	w.mu.Lock()
	if got := w.objects[testCreature].Info.Power[2]; got != 4 {
		t.Errorf("a creature's combo points are slot 2: got %d", got)
	}
	w.mu.Unlock()
}

// TestPowerSlotTable pins the class table against the two create blocks the server sent: the mage's and the
// warrior's slot orders. A drift in the table (a class row added or reordered by a core pin move) fails here.
func TestPowerSlotTable(t *testing.T) {
	for _, tc := range []struct {
		class uint8
		want  []uint8
	}{
		{1, []uint8{powerRage, powerCombo}},
		{8, []uint8{powerMana, powerCombo}},
	} {
		got := powerTypesByClass[tc.class]
		if len(got) != len(tc.want) {
			t.Fatalf("class %d: %v, want %v", tc.class, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("class %d: %v, want %v", tc.class, got, tc.want)
			}
		}
	}
	// A class with no rows places nothing, so no power type can be mistaken for a slot.
	info := Info{Type: TypePlayer, Class: 0}
	if _, ok := info.PowerSlot(powerMana); ok {
		t.Fatal("an unknown class placed a power type")
	}
}

// TestCombatOrphansAreCounted: combat packets for a GUID the model does not hold merge nothing and are counted.
func TestCombatOrphansAreCounted(t *testing.T) {
	w := unitWorld(t)
	before := w.Counts().Orphans
	now := time.Unix(0, 0)
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: wire.GUID{Low: 9, High: 0x0801180000000000}, Auras: []combat.AuraInfo{{Slot: 0}}}, now)
	w.ApplyAttackStart(combat.AttackStart{Attacker: wire.GUID{Low: 9, High: 0x0801180000000000}, Victim: testSelf}, now)
	w.ApplyPowerUpdate(&combat.PowerUpdate{GUID: wire.GUID{Low: 9, High: 0x0801180000000000}}, now)
	after := w.Counts().Orphans
	if after <= before {
		t.Fatalf("orphan count did not grow: %d -> %d", before, after)
	}
}

// TestCombatSnapshotIsACopy: mutating the model after a snapshot does not change the snapshot.
func TestCombatSnapshotIsACopy(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(0, 0)
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testSelf, Auras: []combat.AuraInfo{{Slot: 0, Data: &combat.AuraData{SpellID: 6673}}}}, now)
	cs, _ := w.Combat(testSelf)
	w.ApplyAuraUpdate(&combat.AuraUpdate{Unit: testSelf, Auras: []combat.AuraInfo{{Slot: 0}}}, now)
	if len(cs.Auras) != 1 {
		t.Fatalf("the snapshot changed with the model: %v", cs.Auras)
	}
}

// TestCombatStateOfANonUnit: game objects have no combat state.
func TestCombatStateOfANonUnit(t *testing.T) {
	w := NewWorld(0)
	goGUID := wire.GUID{Low: 1, High: 0x2801180000000000}
	w.Apply(&Update{Blocks: []WorldFragment{{GUID: goGUID, ObjectType: TypeGameObject, ValuesComplete: true, Info: Info{HasObject: true, HasGameObject: true}}}}, time.Unix(0, 0))
	if _, ok := w.Combat(goGUID); ok {
		t.Fatal("a game object has a combat state")
	}
	_ = fmt.Sprint(goGUID)
}

// TestCooldownZeroDurationIsActive: ForcedCooldown 0 means "the spell's own cooldown" to the server
// (SpellHistory.cpp:544-547; Player.cpp:12595-12601 sends 0 for the weapon-switch GCD), not "no cooldown". The model
// does not carry Spell.dbc, so the cooldown is kept as active with an unknown end. The old store wrote `now`, which
// read as already over.
func TestCooldownZeroDurationIsActive(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(1000, 0)
	w.ApplyCooldowns(combat.Cooldowns{Caster: testSelf, List: []combat.Cooldown{{SpellID: 6119, ForcedCooldown: 0}}}, now)
	cs, ok := w.Combat(testSelf)
	if !ok || len(cs.Cooldowns) != 1 {
		t.Fatalf("the cooldown store: %+v", cs.Cooldowns)
	}
	if !cs.Cooldowns[0].Active(now.Add(time.Hour)) {
		t.Fatal("a zero forced cooldown was stored as already over")
	}
	// A duration, by contrast, ends.
	w.ApplyCooldowns(combat.Cooldowns{Caster: testSelf, List: []combat.Cooldown{{SpellID: 6119, ForcedCooldown: 2000}}}, now)
	cs, _ = w.Combat(testSelf)
	if !cs.Cooldowns[0].Active(now.Add(time.Second)) || cs.Cooldowns[0].Active(now.Add(3*time.Second)) {
		t.Fatalf("a 2 s cooldown: %+v", cs.Cooldowns[0])
	}
}

func TestStartCooldownCategoryAndExpire(t *testing.T) {
	w := unitWorld(t)
	now := time.Unix(1000, 0)
	w.StartCooldown(testSelf, 2136, now.Add(8*time.Second), 19)
	w.StartCooldown(testSelf, 1784, time.Time{}, 0) // on-hold: zero end never expires
	cs, ok := w.Combat(testSelf)
	if !ok {
		t.Fatal("missing combat state")
	}
	if end, ok := cs.CategoryCooldowns[19]; !ok || !end.Equal(now.Add(8*time.Second)) {
		t.Fatalf("category 19 end = %v (%v), want +8s", end, ok)
	}
	if got := w.ExpireCooldowns(testSelf, now.Add(7*time.Second)); len(got) != 0 {
		t.Fatalf("expired too early: %v", got)
	}
	got := w.ExpireCooldowns(testSelf, now.Add(8*time.Second))
	if len(got) != 1 || got[0] != 2136 {
		t.Fatalf("expired = %v, want [2136]", got)
	}
	cs, _ = w.Combat(testSelf)
	if len(cs.CategoryCooldowns) != 0 {
		t.Fatalf("category cooldown did not expire: %+v", cs.CategoryCooldowns)
	}
	if len(cs.Cooldowns) != 1 || cs.Cooldowns[0].SpellID != 1784 {
		t.Fatalf("on-hold cooldown lost or 2136 kept: %+v", cs.Cooldowns)
	}
}
