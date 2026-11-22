package world

// Tests for ticket #93 R6: GCD, cooldown lengths, spell history, cast_state + status fields, cooldown/combo events.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
	"github.com/pjerra/forever-agent/internal/foreverspells"
)

const (
	r6SpellsTSV = `# forever-spells v1 source=TEST-SCHEMA
id	name	class	skill_line	rank	level	recovery_ms	category_recovery_ms	category	gcd_ms	gcd_category	mechanic	dispel	attr0
116	Frostbolt	mage	6	1	4	0	0	0	1500	133	0	1	0
1752	Sinister Strike	rogue	38	0	1	0	0	0	1000	133	0	0	0
1784	Stealth	rogue	39	1	1	10000	0	0	1000	133	0	0	33554432
2136	Fire Blast	mage	8	1	6	0	8000	19	1500	133	0	0	0
2948	Scorch	mage	8	1	22	0	8000	19	1500	133	0	0	0
4000	Zero GCD	mage	8	0	1	0	0	0	0	0	0	0	0
`
	r6NamesTSV = `# forever-spell-names v1 source=TEST-SCHEMA
id	name
116	Frostbolt
1752	Sinister Strike
1784	Stealth
2136	Fire Blast
2948	Scorch
4000	Zero GCD
`
)

func r6TestSession(t *testing.T, race, class uint8, clk *fakeClock) (*session, wire.GUID) {
	t.Helper()
	tab, err := foreverspells.Parse(strings.NewReader(r6SpellsTSV), strings.NewReader(r6NamesTSV))
	if err != nil {
		t.Fatalf("r6 test data: %v", err)
	}
	s := newSession(Options{spellTable: tab}, &Report{Counts: map[string]int{}}, context.Background())
	if clk != nil {
		s.nowFn = clk.Now
	}
	s.report.Character.Race = race
	s.report.Character.Class = class
	self := wire.GUID{Low: 0x01, High: 0x0800000000000000}
	spawnPlayerSelf(t, s, self, race, class)
	return s, self
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time        { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// spawnPlayerSelf delivers a minimal ThisIsYou create block for self with the given race and class.
func spawnPlayerSelf(t *testing.T, s *session, self wire.GUID, race, class uint8) {
	t.Helper()
	dispatchSpellPacket(t, s, wire.SMSGUpdateObject, buildSelfPlayerCreate(self, race, class, 100))
}

func buildSelfPlayerCreate(self wire.GUID, race, class uint8, power0 int32) []byte {
	v := wire.NewWriter()
	v.WriteU8(0)    // field flags
	v.WriteU8(3)    // fragCGObject
	v.WriteU8(0xFF) // end
	v.WriteU8(1)    // CGObject exists
	v.WriteI32(0)   // Entry
	v.WriteU32(0)   // DynamicFlags
	v.WriteFloat32(1)
	v.WriteI32(50) // DisplayID
	v.WriteU32(0)  // NpcFlags
	v.WriteU32(0)  // NpcFlags2
	v.WriteU32(0)  // StateSpellVisualID
	v.WriteU32(0)  // StateAnimID
	v.WriteU32(0)  // StateAnimKitID
	v.WriteU32(0)  // StateWorldEffectIDs count
	v.WriteU32(0)  // StateWorldEffectsQuestObjectiveID
	v.WriteU32(0)  // SpellOverrideNameID
	for range 9 {  // Charm..BattlePetCompanionGUID
		fakeZeroGUID(v)
	}
	v.WriteU64(0) // BattlePetDBID
	fakeZeroGUID(v)
	fakeZeroGUID(v)
	v.WriteI32(0) // ChannelData: SpellID
	v.WriteI32(0)
	v.WriteI32(0)
	v.WriteU32(0)
	v.WriteU32(0)
	v.WriteU8(0) // SpellEmpowerStage
	v.WriteU32(0)
	v.WriteU8(race)  // Race
	v.WriteU8(class) // Class
	v.WriteU8(class) // PlayerClassId
	v.WriteU8(2)     // Gender
	v.WriteU8(7)     // CreatureType
	displayPower := uint8(0)
	switch class {
	case 1: // warrior: rage
		displayPower = 1
	case 4: // rogue: energy
		displayPower = 3
	}
	v.WriteU8(displayPower) // DisplayPower
	v.WriteU32(0)           // OverrideDisplayPowerID
	v.WriteU64(100)  // Health
	for i := range 10 {
		if i == 0 {
			v.WriteI32(power0)
			v.WriteI32(power0)
		} else {
			v.WriteI32(0)
			v.WriteI32(0)
		}
	}
	v.WriteU64(100) // MaxHealth
	v.WriteI32(60)  // Level
	v.WriteI32(0)   // EffectiveLevel
	v.WriteI32(0)   // ContentTuningID
	v.WriteI32(0)   // ScalingLevelMin
	v.WriteI32(0)   // ScalingLevelMax
	v.WriteI32(0)   // ScalingLevelDelta
	v.WriteU8(0)    // ScalingFactionGroup
	v.WriteI32(35)  // FactionTemplate
	for range 3 {
		v.WriteI32(0)
		v.WriteI32(0)
		v.WriteI32(0)
		v.WriteI32(0)
		v.WriteU16(0)
		v.WriteU16(0)
		v.WriteU32(0)
		v.WriteU8(0)
		v.WriteU8(0)
		v.WriteBit(false)
		v.WriteBit(false)
		v.FlushBits()
	}
	for range 7 { // Flags..AttackRoundBaseTime[1]
		v.WriteU32(0)
	}
	v.WriteFloat32(0.5)
	v.WriteFloat32(1.5)
	v.WriteFloat32(1)
	v.WriteI32(0)
	v.WriteU8(0)
	v.WriteI32(200)
	v.WriteFloat32(1)
	v.WriteI32(0)
	v.WriteI32(0)
	for range 5 { // StandState..fifth byte
		v.WriteU8(0)
	}
	for range 4 { // PetNumber..PetNextLevelExperience
		v.WriteU32(0)
	}
	v.WriteFloat32(1) // ModCastingSpeed
	v.WriteFloat32(1) // ModCastingSpeedNeg
	for range 4 {     // ModHaste..ModTimeRate
		v.WriteFloat32(0)
	}
	v.WriteI32(0) // CreatedBySpell
	v.WriteI32(0) // EmoteState
	v.WriteU16(0) // ClassicTrainingPointsUsed
	v.WriteU16(0) // ClassicTrainingPointsTotal
	v.WriteI32(0) // BaseMana
	for range 4 { // SheatheState..ShapeshiftForm
		v.WriteU8(0)
	}
	v.WriteFloat32(1) // MaxHealthModifier
	v.WriteFloat32(1) // HoverHeight
	for range 5 {     // MinItemLevelCutoff..WildBattlePetLevel
		v.WriteI32(0)
	}
	v.WriteI32(0) // BattlePetCompanionExperience
	v.WriteU32(0) // BattlePetCompanionNameTimestamp
	for range 7 { // InteractSpellID..TaxiNodesID
		v.WriteI32(0)
	}
	fakeZeroGUID(v)   // GuildGUID
	v.WriteU32(0)     // PassiveSpells
	v.WriteU32(0)     // WorldEffects
	v.WriteU32(0)     // ChannelObjects
	v.WriteI32(0)     // FlightCapabilityID
	v.WriteFloat32(0) // GlideEventSpeedDivisor
	v.WriteI32(0)     // DriveCapabilityID
	v.WriteFloat32(0) // MaxHealthModifierFlatNeg
	v.WriteFloat32(0) // MaxHealthModifierFlatPos
	v.WriteU32(0)     // SilencedSchoolMask
	v.WriteU32(77)    // CurrentAreaID
	v.WriteFloat32(0) // NameplateDistanceMod
	v.WriteFloat32(0) // AutoAttackRangeMod
	fakeZeroGUID(v)   // NameplateAttachToGUID
	v.WriteBit(false) // Field_314
	v.WriteBit(false) // hasAssist
	v.FlushBits()

	// PlayerData::WriteCreate (partyMember = false).
	for range 3 { // DuelArbiter, WowAccount, BnetAccount
		fakeZeroGUID(v)
	}
	v.WriteU64(0) // GuildClubMemberID
	fakeZeroGUID(v) // LootTargetGUID
	v.WriteBytes(make([]byte, 20)) // PlayerFlags..GuildLevel
	v.WriteU32(1)                  // super district
	v.WriteBytes(make([]byte, 13))
	v.WriteBytes(make([]byte, 8))  // Customizations, RandomCustomizations
	v.WriteBytes(make([]byte, 14)) // PartyType..GuildTimeStamp
	v.WriteBytes(make([]byte, 19*27)) // VisibleItems[19]
	v.WriteBytes(make([]byte, 6*4+6*4+1+4+8+4)) // PlayerTitle..ArenaCooldowns
	v.WriteBytes(make([]byte, 32*8))            // ForcedReactions[32]
	v.WriteBytes(make([]byte, 16))              // OfferedAdvJournalQuestID..PetNames
	v.WriteU32(0)
	v.WriteU8(0)
	v.WriteU32(0)
	v.WriteBytes(make([]byte, 8+12)) // CovenantID, SoulbindID, DungeonScore
	fakeZeroGUID(v)                  // LeaverInfo.BnetAccountGUID
	v.WriteBytes(make([]byte, 4+12+4+16+4))
	v.WriteBit(false)
	v.FlushBits()
	fakeZeroGUID(v) // SpectateTarget
	v.WriteI32(0)   // WorldLootObjectInventorySwapSlot
	for range 16 {  // VisibleEquipableSpells[16]
		v.WriteI32(0)
		v.WriteBits(0, 7)
		v.FlushBits()
		v.WriteBit(false)
		v.FlushBits()
	}
	v.WriteU32(0) // VisualItemReplacements
	v.WriteBytes(make([]byte, (19+5)*4)) // PlunderstormItemDisplayID + PersonalTabard
	v.WriteBytes(make([]byte, 3*4+4*4))  // NPCAsPlayerInfo
	fakeZeroGUID(v)
	v.WriteU32(1)
	v.WriteBytes(make([]byte, 13))
	v.WriteBits(8, 6)
	v.WriteBits(0, 9)
	v.WriteBit(false)
	v.WriteBit(false)
	v.WriteBit(false)
	v.FlushBits()
	v.WriteString("Testchar")
	val := v.Bytes()

	b := wire.NewWriter()
	b.WriteU8(1) // UPDATETYPE_CREATE_OBJECT
	wire.WritePackedGUID(b, self)
	b.WriteU8(objects.TypePlayer)
	b.WriteBit(true)  // HasEntityPosition
	b.WriteBit(false) // NoBirthAnim
	b.WriteBit(false) // EnablePortals
	b.WriteBit(false) // PlayHoverAnim
	b.WriteBit(true)  // ThisIsYou
	b.WriteBit(true)  // MovementUpdate
	for range 15 {
		b.WriteBit(false)
	}
	b.FlushBits()
	b.WriteU32(0) // pause times
	wire.WritePackedGUID(b, self)
	b.WriteU64(objects.FlagTouchingGround)
	b.WriteU32(1000)
	b.WriteFloat32(1000)
	b.WriteFloat32(2000)
	b.WriteFloat32(50)
	b.WriteFloat32(0)
	b.WriteFloat32(0)
	b.WriteFloat32(0)
	b.WriteU32(0)
	b.WriteU32(0)
	b.WriteFloat32(1)
	for range 9 {
		b.WriteBit(false)
	}
	b.FlushBits()
	b.WriteFloat32(2.5)
	b.WriteFloat32(7)
	for range 7 {
		b.WriteFloat32(2.5)
	}
	b.WriteU32(0)
	b.WriteFloat32(1)
	for range 17 {
		b.WriteFloat32(0)
	}
	b.WriteBit(false) // HasSpline
	b.FlushBits()
	b.WriteU32(uint32(len(val)))
	b.WriteBytes(val)
	block := b.Bytes()

	out := wire.NewWriter()
	out.WriteU16(0) // mapID
	out.WriteU32(1) // blockCount
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(len(block)))
	out.WriteBytes(block)
	return out.Bytes()
}

func r6SpellEvents(t *testing.T, s *session, kinds ...string) []Event {
	t.Helper()
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	rows, _, _, _ := s.EventLog().Since(0, EventPageMax)
	var out []Event
	for _, e := range rows {
		if want[e.Kind] {
			out = append(out, e)
		}
	}
	return out
}

func r6HistoryPayload(entries []combat.SpellHistoryEntry) []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(entries)))
	for _, e := range entries {
		b.WriteU32(e.SpellID)
		b.WriteU32(e.ItemID)
		b.WriteU32(e.Category)
		b.WriteI32(e.RecoveryTime)
		b.WriteI32(e.CategoryRecoveryTime)
		b.WriteFloat32(e.ModRate)
		b.WriteBit(e.RecoveryStartOffset != nil)
		b.WriteBit(e.CategoryStartOffset != nil)
		b.WriteBit(e.OnHold)
		b.FlushBits()
		if e.RecoveryStartOffset != nil {
			b.WriteU32(*e.RecoveryStartOffset)
		}
		if e.CategoryStartOffset != nil {
			b.WriteU32(*e.CategoryStartOffset)
		}
	}
	return b.Bytes()
}

func TestSpellStateGCDStartAndCastFailed(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	s, self := r6TestSession(t, 1, 8, clk) // human mage
	target := wire.GUID{Low: 9, High: 0x2000000000000000}
	castID := combat.CastGUID(0, 116, 1)

	dispatchSpellPacket(t, s, wire.SMSGSpellStart,
		buildSpellStart(self, self, castID, 116, 1500, combat.SpellTarget{Unit: target}))
	cs := s.serveCastState()
	if !cs.GCD.Active || cs.GCD.TotalMS != 1500 || cs.GCD.RemainingMS != 1500 || !cs.GCDKnown {
		t.Fatalf("after SPELL_START Frostbolt: gcd=%+v known=%v", cs.GCD, cs.GCDKnown)
	}
	st := s.serveStatus(&serveState{})
	if st.GCDMS != 1500 {
		t.Fatalf("status.gcd_ms = %d, want 1500", st.GCDMS)
	}

	clk.Advance(200 * time.Millisecond)
	dispatchSpellPacket(t, s, wire.SMSGCastFailed, buildCastFailed(castID, 116, 118)) // out of range
	cs = s.serveCastState()
	if cs.GCD.Active || cs.GCD.RemainingMS != 0 {
		t.Fatalf("CAST_FAILED did not turn GCD off: %+v", cs.GCD)
	}
	if cs.Failures != 1 || cs.LastFailure == nil || cs.LastFailure.ReasonName != "out of range" || cs.LastFailure.SpellID != 116 {
		t.Fatalf("last_failure = %+v, failures = %d", cs.LastFailure, cs.Failures)
	}

	// Zero-GCD spell must never be clamped up to 750 ms.
	castZero := combat.CastGUID(0, 4000, 2)
	dispatchSpellPacket(t, s, wire.SMSGSpellStart,
		buildSpellStart(self, self, castZero, 4000, 0, combat.SpellTarget{Unit: target}))
	cs = s.serveCastState()
	if cs.GCD.Active || cs.GCD.TotalMS != 0 || !cs.GCDKnown {
		t.Fatalf("zero GCD spell started GCD: %+v known=%v", cs.GCD, cs.GCDKnown)
	}

	// Unknown spell sets gcd_known:false and starts no GCD.
	castUnk := combat.CastGUID(0, 99999, 3)
	dispatchSpellPacket(t, s, wire.SMSGSpellStart,
		buildSpellStart(self, self, castUnk, 99999, 1000, combat.SpellTarget{Unit: target}))
	cs = s.serveCastState()
	if cs.GCD.Active || cs.GCDKnown {
		t.Fatalf("unknown spell: gcd=%+v known=%v, want inactive and false", cs.GCD, cs.GCDKnown)
	}
	dispatchSpellPacket(t, s, wire.SMSGSpellGo,
		buildSpellGo(self, self, castUnk, 99999, 0, combat.SpellTarget{Unit: target}))

	// SPELL_START then SPELL_GO: last_cast is set, GCD stays active, and a later failure of another spell keeps GCD.
	cast2 := combat.CastGUID(0, 116, 4)
	dispatchSpellPacket(t, s, wire.SMSGSpellStart,
		buildSpellStart(self, self, cast2, 116, 1500, combat.SpellTarget{Unit: target}))
	clk.Advance(1000 * time.Millisecond)
	dispatchSpellPacket(t, s, wire.SMSGSpellGo,
		buildSpellGo(self, self, cast2, 116, 0, combat.SpellTarget{Unit: target}))
	dispatchSpellPacket(t, s, wire.SMSGCastFailed, buildCastFailed(combat.CastGUID(0, 2136, 5), 2136, 91)) // not ready
	cs = s.serveCastState()
	if !cs.GCD.Active || cs.GCD.RemainingMS != 500 {
		t.Fatalf("GCD after GO + unrelated fail = %+v, want active 500ms", cs.GCD)
	}
	if cs.LastCast == nil || cs.LastCast.SpellID != 116 || cs.LastCast.Name != "Frostbolt" || cs.LastCast.AgoMS != 0 {
		t.Fatalf("last_cast = %+v", cs.LastCast)
	}
}

func TestSpellStateCooldownAndCategory(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	s, self := r6TestSession(t, 1, 8, clk)
	target := wire.GUID{Low: 9, High: 0x2000000000000000}
	dispatchSpellPacket(t, s, wire.SMSGSendKnownSpells, r5KnownPayload([]int32{2136, 2948}, nil))

	castID := combat.CastGUID(0, 2136, 1)
	dispatchSpellPacket(t, s, wire.SMSGSpellGo,
		buildSpellGo(self, self, castID, 2136, 0, combat.SpellTarget{Unit: target}))
	cs := s.serveCastState()
	if len(cs.Cooldowns) != 2 {
		t.Fatalf("cooldowns = %+v, want both Fire Blast 2136 and Scorch 2948 (shared category 19)", cs.Cooldowns)
	}
	for _, cd := range cs.Cooldowns {
		if cd.Category != 19 || cd.RemainingMS != 8000 {
			t.Fatalf("cd %+v, want category 19 remaining 8000", cd)
		}
	}
	evs := r6SpellEvents(t, s, events.KindCooldownStart)
	if len(evs) != 1 || evs[0].SpellID != 2136 || evs[0].Data["source"] != "cast" || evs[0].Data["category"] != uint16(19) {
		t.Fatalf("cooldown_start events = %+v", evs)
	}

	clk.Advance(8001 * time.Millisecond)
	cs = s.serveCastState()
	if len(cs.Cooldowns) != 0 {
		t.Fatalf("cooldowns after 8s = %+v, want empty", cs.Cooldowns)
	}
	ends := r6SpellEvents(t, s, events.KindCooldownEnd)
	if len(ends) != 1 || ends[0].SpellID != 2136 {
		t.Fatalf("cooldown_end events = %+v, want [2136]", ends)
	}
}

func TestSpellStateHistoryLoginOffsetAndOnHold(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	s, _ := r6TestSession(t, 4, 4, clk)
	dispatchSpellPacket(t, s, wire.SMSGSendSpellHistory, r6HistoryPayload([]combat.SpellHistoryEntry{
		{
			SpellID:              2136,
			Category:             19,
			CategoryRecoveryTime: 8000,
			CategoryStartOffset:  new(uint32(3000)),
			ModRate:              1,
		},
		{
			SpellID:      1784,
			RecoveryTime: 10000,
			ModRate:      1,
			OnHold:       true,
		},
	}))
	cs := s.serveCastState()
	if len(cs.Cooldowns) != 2 {
		t.Fatalf("cooldowns after spell history = %+v, want 2", cs.Cooldowns)
	}
	if cs.Cooldowns[0].SpellID != 1784 || cs.Cooldowns[0].RemainingMS != 0 {
		t.Fatalf("on-hold entry = %+v", cs.Cooldowns[0])
	}
	if cs.Cooldowns[1].SpellID != 2136 || cs.Cooldowns[1].RemainingMS != 5000 {
		t.Fatalf("offset entry = %+v, want 2136 with 5000ms", cs.Cooldowns[1])
	}
	starts := r6SpellEvents(t, s, events.KindCooldownStart)
	if len(starts) != 2 || starts[0].Data["source"] != "login" || starts[1].Data["source"] != "login" {
		t.Fatalf("login cooldown_start events = %+v", starts)
	}
}

func TestSpellStateCooldownEvent(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	s, self := r6TestSession(t, 4, 4, clk) // rogue
	dispatchSpellPacket(t, s, wire.SMSGSpellGo,
		buildSpellGo(self, self, combat.CastGUID(0, 1784, 1), 1784, 0, combat.SpellTarget{Unit: self}))
	if starts := r6SpellEvents(t, s, events.KindCooldownStart); len(starts) != 0 {
		t.Fatalf("CooldownOnEvent spell_go wrote cooldown_start prematurely: %+v", starts)
	}

	b := wire.NewWriter()
	b.WriteI32(1784)
	b.WriteBit(false)
	b.FlushBits()
	dispatchSpellPacket(t, s, wire.SMSGCooldownEvent, b.Bytes())

	cs := s.serveCastState()
	if len(cs.Cooldowns) != 1 || cs.Cooldowns[0].SpellID != 1784 || cs.Cooldowns[0].RemainingMS != 10000 {
		t.Fatalf("after COOLDOWN_EVENT = %+v, want 1784 at 10000ms", cs.Cooldowns)
	}
	starts := r6SpellEvents(t, s, events.KindCooldownStart)
	if len(starts) != 1 || starts[0].Data["source"] != "event" {
		t.Fatalf("cooldown_start = %+v", starts)
	}

	// SMSG_CLEAR_COOLDOWN clears Stealth early and emits cooldown_clear.
	cb := wire.NewWriter()
	cb.WriteU32(1784)
	cb.WriteBit(false)
	cb.WriteBit(false)
	cb.FlushBits()
	dispatchSpellPacket(t, s, wire.SMSGClearCooldown, cb.Bytes())
	if cs := s.serveCastState(); len(cs.Cooldowns) != 0 {
		t.Fatalf("after SMSG_CLEAR_COOLDOWN = %+v, want empty", cs.Cooldowns)
	}
	clears := r6SpellEvents(t, s, events.KindCooldownClear)
	if len(clears) != 1 || clears[0].SpellID != 1784 {
		t.Fatalf("cooldown_clear = %+v", clears)
	}

	// SMSG_COOLDOWN_EVENT on an unknown spell clears its on-hold entry.
	sb := wire.NewWriter()
	wire.WritePackedGUID(sb, self)
	sb.WriteU8(0)
	sb.WriteU32(1)
	sb.WriteU32(8888)
	sb.WriteU32(0)
	sb.WriteFloat32(1)
	dispatchSpellPacket(t, s, wire.SMSGSpellCooldown, sb.Bytes())
	eb := wire.NewWriter()
	eb.WriteI32(8888)
	eb.WriteBit(false)
	eb.FlushBits()
	dispatchSpellPacket(t, s, wire.SMSGCooldownEvent, eb.Bytes())
	if cs := s.serveCastState(); len(cs.Cooldowns) != 0 {
		t.Fatalf("after unknown COOLDOWN_EVENT = %+v, want empty", cs.Cooldowns)
	}
	clears = r6SpellEvents(t, s, events.KindCooldownClear)
	if len(clears) != 2 || clears[1].SpellID != 8888 {
		t.Fatalf("cooldown_clear after unknown COOLDOWN_EVENT = %+v", clears)
	}
}

func TestSpellStateComboPoints(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	s, self := r6TestSession(t, 2, 4, clk) // orc rogue (power 0 = energy, power 1 = combo points)
	mob := wire.GUID{Low: 0x42, High: 0x2000000000000000}

	// Values update setting Power[1] = 3 (retail bit 141) and ComboTarget = mob (retail bit 230).
	pkt := buildMultiUnitValuesUpdate(self, map[int]func(*wire.Buffer){
		141: func(v *wire.Buffer) { v.WriteI32(3) },
		230: func(v *wire.Buffer) { wire.WritePackedGUID(v, mob) },
	})
	dispatchSpellPacket(t, s, wire.SMSGUpdateObject, pkt)

	cs := s.serveCastState()
	if cs.ComboPoints != 3 || wire.GUID(cs.ComboTarget) != mob {
		t.Fatalf("cast_state combo = %d on %s, want 3 on %s", cs.ComboPoints, cs.ComboTarget, mob)
	}
	st := s.serveStatus(&serveState{})
	if st.ComboPoints != 3 || wire.GUID(st.ComboTarget) != mob || st.Power != 100 {
		t.Fatalf("status combo = %d on %s, power = %d (must stay 100!)", st.ComboPoints, st.ComboTarget, st.Power)
	}
	evs := r6SpellEvents(t, s, events.KindComboPoints)
	if len(evs) != 1 || evs[0].Data["points"] != uint8(3) || evs[0].Data["was"] != uint8(0) {
		t.Fatalf("combo_points event = %+v", evs)
	}

	// Spend combo points (Power[1] = 0), ComboTarget stays stale in object, omitted in cast_state/status JSON.
	pkt2 := buildMultiUnitValuesUpdate(self, map[int]func(*wire.Buffer){
		141: func(v *wire.Buffer) { v.WriteI32(0) },
	})
	dispatchSpellPacket(t, s, wire.SMSGUpdateObject, pkt2)
	cs = s.serveCastState()
	if cs.ComboPoints != 0 || !wire.GUID(cs.ComboTarget).IsZero() {
		t.Fatalf("after spend combo = %d on %s, want 0 and empty", cs.ComboPoints, cs.ComboTarget)
	}
	raw, _ := json.Marshal(cs)
	if strings.Contains(string(raw), "combo_target") {
		t.Fatalf("combo_target present at 0 points: %s", raw)
	}
}

func TestStatusFields(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1700000000, 0)}
	s, self := r6TestSession(t, 4, 4, clk) // night elf -> alliance
	st := s.serveStatus(&serveState{})
	if st.Team != "alliance" || st.BaseTime.IsZero() {
		t.Fatalf("status alliance/base_time = %+v", st)
	}

	s.mover.Flags |= objects.FlagFalling
	if !s.serveStatus(&serveState{}).Falling {
		t.Fatal("falling false with FlagFalling")
	}
	s.mover.Flags &^= objects.FlagFalling

	// Simulated knockback landing sets z_unknown; a later server position clears it.
	s.landing = &selfLanding{at: clk.Now(), simX: 1000, simY: 2000, simZ: 50}
	s.finishSelfLanding(false, 0, 0, 0)
	if !s.serveStatus(&serveState{}).ZUnknown {
		t.Fatal("z_unknown false after simulated knockback landing")
	}
	s.statedSelfPosition(1000, 2000, 50, clk.Now())
	if s.serveStatus(&serveState{}).ZUnknown {
		t.Fatal("z_unknown still true after server position")
	}

	// Acked self teleport increments teleport_seq; far worldport increments; seamless does not.
	s.fightSender = &noopSender{}
	tp := wire.NewWriter()
	wire.WritePackedGUID(tp, self)
	tp.WriteU32(1)
	tp.WriteFloat32(1010)
	tp.WriteFloat32(2010)
	tp.WriteFloat32(55)
	tp.WriteFloat32(0)
	tp.WriteBit(false)
	tp.WriteBit(false)
	tp.WriteBit(false)
	tp.FlushBits()
	dispatchSpellPacket(t, s, wire.SMSGMoveTeleport, tp.Bytes())
	if got := s.serveStatus(&serveState{}).TeleportSeq; got != 1 {
		t.Fatalf("teleport_seq after SMSG_MOVE_TELEPORT = %d, want 1", got)
	}

	// Completed far worldport increments teleport_seq; seamless does not.
	s.mapChg.phase = transferSuspended
	if err := s.applyNewWorld(context.Background(), objects.NewWorldPacket{
		MapID: 1, X: 100, Y: 200, Z: 10, O: 0, Reason: objects.NewWorldReasonNormal, Counter: 1,
	}); err != nil {
		t.Fatalf("far worldport: %v", err)
	}
	if got := s.serveStatus(&serveState{}).TeleportSeq; got != 2 {
		t.Fatalf("teleport_seq after far worldport = %d, want 2", got)
	}
	s.mapChg.phase = transferSuspended
	if err := s.applyNewWorld(context.Background(), objects.NewWorldPacket{
		MapID: 1, X: 100, Y: 200, Z: 10, O: 0, Reason: objects.NewWorldReasonSeamless, Counter: 2,
	}); err != nil {
		t.Fatalf("seamless worldport: %v", err)
	}
	if got := s.serveStatus(&serveState{}).TeleportSeq; got != 2 {
		t.Fatalf("teleport_seq after seamless worldport = %d, want 2", got)
	}
}

type noopSender struct{}

func (noopSender) Send(uint32, []byte) error                   { return nil }
func (noopSender) WaitUntil(context.Context, time.Time) error  { return nil }
func (noopSender) Now() time.Time                              { return time.Now() }

func testClassicUnitBit(bit int) int {
	c := bit + 9
	switch {
	case bit == 230:
		c = 141
	case bit <= 71:
		c = bit
	case bit <= 127:
		c = bit + 6
	}
	if c >= 140 {
		c++
	}
	if c >= 132 {
		c += 3
	}
	return c
}

func buildMultiUnitValuesUpdate(guid wire.GUID, fields map[int]func(*wire.Buffer)) []byte {
	v := wire.NewWriter()
	v.WriteU8(0)    // no UpdateFieldFlag
	v.WriteU8(0)    // fragment list unchanged
	v.WriteU8(0x03) // CGObject: exists + changed
	v.WriteU32(1 << objects.TypeUnit)
	var blocks [8]uint32
	for retailBit := range fields {
		c := testClassicUnitBit(retailBit)
		blocks[c/32] |= 1 << uint(c%32)
	}
	var blockMask byte
	for n, block := range blocks {
		if block != 0 {
			blockMask |= 1 << uint(n)
		}
	}
	v.WriteBits(uint64(blockMask), 8)
	for n, block := range blocks {
		if blockMask&(1<<uint(n)) != 0 {
			v.WriteBits(uint64(block), 32)
		}
	}
	v.WriteBit(false)
	v.FlushBits()
	// Wire write order in UnitData::WriteUpdate: bit 230 (ComboTarget in gate128) comes before 140..179 (Power).
	if fn, ok := fields[230]; ok {
		fn(v)
	}
	for n := range 10 {
		if fn, ok := fields[140+n]; ok {
			fn(v)
		}
	}
	body := v.Bytes()
	blk := wire.NewWriter()
	blk.WriteU8(0) // UPDATETYPE_VALUES
	wire.WritePackedGUID(blk, guid)
	blk.WriteU32(uint32(len(body)))
	blk.WriteBytes(body)
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(len(blk.Bytes())))
	out.WriteBytes(blk.Bytes())
	return out.Bytes()
}

var _ = worldconn.Packet{}
