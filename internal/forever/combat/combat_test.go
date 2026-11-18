package combat

// The combat layer's tests: every parser against the committed fixtures (consumed to the last byte, with the fields
// that matter pinned), the GUID audit of the fixtures, the writers against a mirror of the server's readers, the
// bounds on every server-declared count, and a fuzz smoke over the fixture seeds.

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// loadFixtures reads testdata/combat.json.
func loadFixtures(t *testing.T) fixtureFile {
	t.Helper()
	data, err := os.ReadFile("testdata/combat.json")
	if err != nil {
		t.Fatalf("reading the fixtures: %v", err)
	}
	var doc fixtureFile
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing the fixtures: %v", err)
	}
	if len(doc.Packets) < 20 {
		t.Fatalf("only %d fixtures", len(doc.Packets))
	}
	return doc
}

// fixtureOpcode maps a fixture's opcode name back to the core number.
func fixtureOpcode(t *testing.T, name string) uint32 {
	t.Helper()
	for _, core := range []uint32{
		wire.SMSGAttackStart, wire.SMSGAttackStop, wire.SMSGAttackSwingError, wire.SMSGAttackSwingLandedLog,
		wire.SMSGAttackerStateUpdate, wire.SMSGSpellStart, wire.SMSGSpellGo, wire.SMSGCastFailed,
		wire.SMSGSpellFailure, wire.SMSGSpellFailedOther, wire.SMSGSpellNonMeleeDamage, wire.SMSGSpellHealLog,
		wire.SMSGSpellPeriodicAuraLog, wire.SMSGSpellEnergizeLog, wire.SMSGSpellDelayed, wire.SMSGAuraUpdate,
		wire.SMSGSpellCooldown, wire.SMSGCooldownEvent, wire.SMSGClearCooldown, wire.SMSGClearCooldowns,
		wire.SMSGPowerUpdate, wire.SMSGLogXPGain, wire.SMSGDeathReleaseLoc, wire.SMSGSendKnownSpells,
	} {
		if wire.ServerName(core) == name {
			return core
		}
	}
	t.Fatalf("fixture opcode %q is not a combat opcode this package parses", name)
	return 0
}

// TestFixtureCombatPackets parses every fixture and pins the fields the round relies on. A parser that is consumed
// exactly but decodes the wrong field fails here.
func TestFixtureCombatPackets(t *testing.T) {
	doc := loadFixtures(t)
	for _, p := range doc.Packets {
		t.Run(p.Name, func(t *testing.T) {
			payload, err := hex.DecodeString(p.Payload)
			if err != nil {
				t.Fatalf("payload: %v", err)
			}
			core := fixtureOpcode(t, p.Opcode)
			v, err := parseFixture(payload, core)
			if err != nil {
				t.Fatalf("%v", err)
			}
			switch p.Name {
			case "attack_start":
				a := v.(AttackStart)
				if a.Attacker.IsZero() || a.Victim.IsZero() || a.Attacker.Type() != 2 || a.Victim.Type() != 8 {
					t.Fatalf("attacker %v victim %v", a.Attacker, a.Victim)
				}
			case "attack_stop":
				a := v.(AttackStop)
				if !a.NowDead || a.Attacker.IsZero() || a.Victim.IsZero() {
					t.Fatalf("%+v", a)
				}
			case "attacker_state_update":
				r := v.(AttackRound)
				if r.Damage != 10 || r.OriginalDamage != 10 || r.OverDamage != -1 || r.VictimState != 1 {
					t.Fatalf("round %+v", r)
				}
				if r.Flags&hitInfoRageGain == 0 || r.RageGained != 43 {
					t.Fatalf("the rage gain: flags %#x, gained %d", r.Flags, r.RageGained)
				}
				if !r.HasSub || r.SchoolMask != 1 || r.FloatDamage != 10 || r.SubDamage != 10 {
					t.Fatalf("the sub-damage block: %+v", r)
				}
				if r.Tuning.Flags != 3 || r.Tuning.Type != 0 || r.HasHitInfo {
					t.Fatalf("content tuning %+v", r.Tuning)
				}
				if r.LogData != nil {
					t.Fatal("the basic attacker state update carries log data")
				}
			case "attacker_state_update_dodge":
				r := v.(AttackRound)
				if r.VictimState != 2 || r.Damage != 0 || r.OriginalDamage != 11 {
					t.Fatalf("round %+v", r)
				}
				if VictimStateName(r.VictimState) != "dodge" {
					t.Fatal("the victim state is not named dodge")
				}
			case "aura_update":
				a := v.(AuraUpdate)
				if len(a.Auras) != 1 || a.Auras[0].Slot != 2 || a.Auras[0].Data == nil {
					t.Fatalf("%+v", a)
				}
				d := a.Auras[0].Data
				if d.SpellID != 1459 || d.Duration == nil || *d.Duration != 960000 || d.Remaining == nil || *d.Remaining != 960000 {
					t.Fatalf("aura data %+v", d)
				}
				if d.Flags&AuraHasDuration == 0 {
					t.Fatalf("flags %#x have no duration bit", d.Flags)
				}
				if d.CastUnit == nil || d.CastUnit.IsZero() {
					t.Fatal("the aura's caster is absent")
				}
			case "aura_update_multi":
				a := v.(AuraUpdate)
				if !a.UpdateAll || len(a.Auras) != 3 {
					t.Fatalf("%+v", a)
				}
				for i, want := range []uint16{0, 1, 2} {
					if a.Auras[i].Slot != want || a.Auras[i].Data == nil {
						t.Fatalf("aura %d: %+v", i, a.Auras[i])
					}
				}
				if d := a.Auras[2].Data; d.SpellID != 1459 || d.Duration == nil || *d.Duration != 960000 {
					t.Fatalf("the third aura %+v", d)
				}
			case "aura_removed":
				a := v.(AuraUpdate)
				if len(a.Auras) != 1 || a.Auras[0].Data != nil {
					t.Fatalf("%+v", a)
				}
			case "spell_start":
				c := v.(SpellCast)
				if c.SpellID != 133 || c.CastTime != 1500 || c.Target.Unit.IsZero() || len(c.Remaining) != 1 {
					t.Fatalf("cast %+v", c)
				}
				if c.Remaining[0].Type != 0 || c.Remaining[0].Cost != 306 {
					t.Fatalf("remaining power %+v", c.Remaining)
				}
				if !c.CastFlagHasPowerLeft() {
					t.Fatal("the power-left flag is not read")
				}
				if c.Target.Flags&TargetFlagUnit == 0 {
					t.Fatalf("target flags %#x", c.Target.Flags)
				}
			case "spell_go":
				c := v.(SpellCast)
				if c.SpellID != 836 || c.CastTime != 960000 {
					t.Fatalf("cast %+v", c)
				}
				if len(c.HitTargets) != 1 || len(c.MissStatus) != 0 {
					t.Fatalf("hit %v miss status %v", c.HitTargets, c.MissStatus)
				}
				if c.LogData != nil {
					t.Fatal("the basic spell go carries log data")
				}
			case "spell_failure", "spell_failed_other":
				f := v.(SpellFailure)
				if f.SpellID != 133 || f.Reason != 13 || f.CastID.IsZero() {
					t.Fatalf("%+v", f)
				}
				if SpellFailureName(f.Reason) != "bad targets" {
					t.Fatalf("reason %d is named %q", f.Reason, SpellFailureName(f.Reason))
				}
			case "spell_non_melee_damage_log":
				d := v.(SpellDamage)
				if d.SpellID != 133 || d.Damage != 18 || d.OriginalDamage != 18 || d.SchoolMask != 4 || d.Overkill != -1 {
					t.Fatalf("%+v", d)
				}
				if d.LogData != nil || d.Tuning != nil || len(d.Viewers) != 0 || len(d.Supporters) != 0 {
					t.Fatalf("the basic damage log carries extras: %+v", d)
				}
			case "spell_periodic_aura_log":
				p := v.(PeriodicAuraLog)
				if p.SpellID != 772 || len(p.Effects) != 1 {
					t.Fatalf("%+v", p)
				}
				e := p.Effects[0]
				if e.Effect != 3 || e.Amount != 5 || e.OriginalDamage != 5 || e.SchoolMaskOrPower != 1 {
					t.Fatalf("effect %+v", e)
				}
			case "spell_energize_log":
				e := v.(EnergizeLog)
				if e.SpellID != 100 || e.Type != 1 || e.Amount != 90 || e.OverEnergize != 0 {
					t.Fatalf("%+v", e)
				}
				if PowerName(e.Type) != "rage" {
					t.Fatalf("type %d is named %q", e.Type, PowerName(e.Type))
				}
			case "spell_delayed":
				d := v.(SpellDelayed)
				if d.Delay != 960000 || d.Caster.IsZero() {
					t.Fatalf("%+v", d)
				}
			case "power_update":
				p := v.(PowerUpdate)
				if p.GUID.IsZero() || len(p.Powers) != 1 || p.Powers[0].Type != 1 || p.Powers[0].Value != 357 {
					t.Fatalf("%+v", p)
				}
			case "attack_swing_error":
				e := v.(SwingError)
				if e.Reason != SwingNotInRange || e.Reason.String() != "not in range" {
					t.Fatalf("%+v", e)
				}
			case "attack_swing_landed_log":
				r := v.(AttackRound)
				if r.Damage != 7 || r.VictimState != 1 || r.RageGained != 3 {
					t.Fatalf("round %+v", r)
				}
				if r.LogData == nil || r.LogData.Health != 60 || len(r.LogData.Power) != 1 {
					t.Fatalf("log data %+v", r.LogData)
				}
				if r.LogData.Power[0].Type != 1 || r.LogData.Power[0].Amount != 35 {
					t.Fatalf("log power %+v", r.LogData.Power)
				}
			case "cast_failed":
				// The live capture's Heroic Strike refusal: no power, the server's cast id, no caster.
				f := v.(SpellFailure)
				if f.Reason != 108 || f.SpellID != 78 || f.Arg1 != -1 || f.Arg2 != -1 || !f.Caster.IsZero() {
					t.Fatalf("%+v", f)
				}
				if SpellFailureName(f.Reason) != "no power" {
					t.Fatalf("reason %d is named %q", f.Reason, SpellFailureName(f.Reason))
				}
				if f.CastID.Type() != 47 || f.CastID.IsZero() {
					t.Fatalf("the cast id %v is not a cast GUID", f.CastID)
				}
			case "cast_failed_not_ready":
				f := v.(SpellFailure)
				if f.Reason != 91 || f.SpellID != 2457 || f.Arg1 != 0 || f.Arg2 != -1 {
					t.Fatalf("%+v", f)
				}
				if SpellFailureName(f.Reason) != "not ready" {
					t.Fatalf("reason %d is named %q", f.Reason, SpellFailureName(f.Reason))
				}
			case "spell_heal_log":
				h := v.(SpellHeal)
				if h.SpellID != 4242 || h.Health != 60 || h.OriginalHeal != 25 || h.OverHeal != 0 || h.Crit {
					t.Fatalf("%+v", h)
				}
			case "spell_cooldown":
				c := v.(Cooldowns)
				if len(c.List) != 1 || c.List[0].SpellID != 4242 || c.List[0].ForcedCooldown != 6000 || c.List[0].ModRate != 1 {
					t.Fatalf("%+v", c)
				}
			case "cooldown_event":
				e := v.(CooldownEvent)
				if e.SpellID != 4242 || e.Pet {
					t.Fatalf("%+v", e)
				}
			case "clear_cooldown":
				c := v.(ClearCooldown)
				if c.SpellID != 4242 || c.Hold || c.Pet {
					t.Fatalf("%+v", c)
				}
			case "clear_cooldowns":
				c := v.(ClearCooldowns)
				if len(c.Spells) != 2 || c.Spells[0] != 4242 || c.Spells[1] != 4243 || c.Pet {
					t.Fatalf("%+v", c)
				}
			case "log_xp_gain":
				x := v.(XPGain)
				if x.Amount != 45 || x.Original != 45 || x.Reason != 0 || x.Victim.IsZero() {
					t.Fatalf("%+v", x)
				}
				if XPGainReasonName(x.Reason) != "kill" {
					t.Fatalf("reason %d", x.Reason)
				}
			case "death_release_loc":
				d := v.(DeathRelease)
				if d.MapID != 0 || d.X != 1000 || d.Y != 2000 || d.Z != 50 {
					t.Fatalf("%+v", d)
				}
			case "send_known_spells":
				k := v.(KnownSpells)
				if !k.InitialLogin || len(k.Known) != 3 || k.Known[0] != 6603 || k.Known[1] != 6673 || k.Known[2] != 100 {
					t.Fatalf("%+v", k)
				}
				if len(k.Favorite) != 1 || k.Favorite[0] != 6603 {
					t.Fatalf("favourites %v", k.Favorite)
				}
			case "spell_go_miss":
				c := v.(SpellCast)
				if len(c.MissTargets) != 2 || len(c.MissStatus) != 2 {
					t.Fatalf("miss %v status %v", c.MissTargets, c.MissStatus)
				}
				if c.MissStatus[0].Reason != 5 || MissName(5) != "block" {
					t.Fatalf("first miss status %+v", c.MissStatus[0])
				}
				if c.MissStatus[1].Reason != missReflect || c.MissStatus[1].Reflect != 3 {
					t.Fatalf("the reflect entry %+v", c.MissStatus[1])
				}
				if c.Target.Unit.IsZero() || c.Target.Flags&TargetFlagUnit == 0 {
					t.Fatalf("target %+v", c.Target)
				}
			default:
				t.Fatalf("fixture %q has no assertions: a parser is not pinned", p.Name)
			}
		})
	}
}

// TestFixtureGUIDsAreSynthetic walks every fixture through the offset hook: every packed GUID the parse decodes must
// be one the fixture's list names (the generator wrote it), so no captured identifier can hide in a committed
// payload. The negative subtest puts a real-looking GUID back into one fixture and requires the sweep to see it.
func TestFixtureGUIDsAreSynthetic(t *testing.T) {
	doc := loadFixtures(t)
	seen := 0
	for _, p := range doc.Packets {
		payload, err := hex.DecodeString(p.Payload)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		allowed := map[string]bool{}
		for _, g := range p.GUIDs {
			allowed[g] = true
		}
		core := fixtureOpcode(t, p.Opcode)
		b := wire.NewReader(payload)
		b.SetGUIDAtHook(func(g wire.GUID, off int) {
			mask := binary.LittleEndian.Uint16(payload[off:])
			n := 2 + popcount(mask)
			if mask == 0 {
				return
			}
			bin := payload[off : off+n]
			seen++
			if !allowed[hex.EncodeToString(bin)] {
				t.Errorf("%s: GUID at offset %d (% X) is not in the fixture's synthetic list", p.Name, off, bin)
			}
			if g.Type() == 0 && !g.IsZero() {
				t.Errorf("%s: GUID at offset %d has no type", p.Name, off)
			}
		})
		if _, err := parseFixtureOn(b, core); err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
	}
	if seen < 20 {
		t.Fatalf("the sweep saw only %d GUIDs", seen)
	}

	// Negative: a captured-shaped player GUID must be caught.
	payload, err := hex.DecodeString(doc.Packets[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	real := []byte{0x03, 0xE0, 0x65, 0x0D, 0x18, 0x01, 0x08} // mask 0xE003 + five bytes
	copy(payload[0:len(real)], real)
	allowed := map[string]bool{}
	for _, g := range doc.Packets[0].GUIDs {
		allowed[g] = true
	}
	caught := false
	b := wire.NewReader(payload)
	b.SetGUIDAtHook(func(g wire.GUID, off int) {
		mask := binary.LittleEndian.Uint16(payload[off:])
		n := 2 + popcount(mask)
		if !allowed[hex.EncodeToString(payload[off:off+n])] {
			caught = true
		}
	})
	_, _ = parseFixtureOn(b, fixtureOpcode(t, doc.Packets[0].Opcode))
	if !caught {
		t.Fatal("the sweep did not catch a captured GUID put back into the first fixture")
	}
}

func popcount(v uint16) int {
	n := 0
	for v != 0 {
		n += int(v & 1)
		v >>= 1
	}
	return n
}

// TestBoundsRefuseHostileCounts forges every server-declared count past its bound and requires an error instead of
// an allocation. The payloads are cut down to the count itself: the parser must reject before reading the list.
func TestBoundsRefuseHostileCounts(t *testing.T) {
	t.Run("cast target counts", func(t *testing.T) {
		// Each of the four 16-bit counts and the target-point count is forged past 256 in turn.
		for name, payload := range map[string][]byte{
			"hit targets":   forgedGo(300, 0, 0, 0, 0),
			"miss targets":  forgedGo(0, 300, 0, 0, 0),
			"hit statuses":  forgedGo(0, 0, 300, 0, 0),
			"miss statuses": forgedGo(0, 0, 0, 300, 0),
			"target points": forgedGo(0, 0, 0, 0, 300),
		} {
			if _, err := ParseSpellGo(payload); err == nil {
				t.Fatalf("%s: a count of 300 was accepted", name)
			}
		}
	})
	t.Run("aura count", func(t *testing.T) {
		// 1 (UpdateAll) + 9 bits count = 511, then the unit GUID.
		payload := []byte{0xFF, 0xFF, 0x03, 0x80, 0x11}
		if _, err := ParseAuraUpdate(payload); err == nil {
			t.Fatal("a 511-aura update was accepted")
		}
	})
	t.Run("cooldown count", func(t *testing.T) {
		payload := append([]byte{0x00, 0x00}, u32(1<<20)...)
		if _, err := ParseSpellCooldown(payload); err == nil {
			t.Fatal("a million cooldowns were accepted")
		}
	})
	t.Run("cleared cooldown count", func(t *testing.T) {
		if _, err := ParseClearCooldowns(u32(1 << 20)); err == nil {
			t.Fatal("a million cleared cooldowns were accepted")
		}
	})
	t.Run("power count", func(t *testing.T) {
		payload := append([]byte{0x00, 0x00}, u32(1<<16)...)
		if _, err := ParsePowerUpdate(payload); err == nil {
			t.Fatal("a 65536-power update was accepted")
		}
	})
	t.Run("known spell count", func(t *testing.T) {
		if _, err := ParseKnownSpells(u32(1 << 20)); err == nil {
			t.Fatal("a million known spells were accepted")
		}
	})
	t.Run("attack round size", func(t *testing.T) {
		payload := append([]byte{0x00}, u32(1<<20)...)
		if _, err := ParseAttackerStateUpdate(payload); err == nil {
			t.Fatal("a round bigger than the packet was accepted")
		}
	})
	t.Run("periodic effect count", func(t *testing.T) {
		payload := append([]byte{0x00, 0x00, 0x00, 0x00}, u32(1<<20)...)
		if _, err := ParseSpellPeriodicAuraLog(payload); err == nil {
			t.Fatal("a million periodic effects were accepted")
		}
	})
	t.Run("message length", func(t *testing.T) {
		// Every parser refuses a packet that ends before its layout: a one-byte truncation of a valid fixture.
		doc := loadFixtures(t)
		for _, p := range doc.Packets {
			payload, err := hex.DecodeString(p.Payload)
			if err != nil || len(payload) < 2 {
				continue
			}
			if _, err := parseFixture(payload[:len(payload)-1], fixtureOpcode(t, p.Opcode)); err == nil {
				t.Fatalf("%s: a packet truncated by one byte parsed", p.Name)
			}
		}
	})
}

// forgedGo builds a SpellCastData with the five target-related counts the caller names, and no lists behind them.
// The parser must refuse an over-limit count before it ever reads a list.
func forgedGo(hit, miss, hitStatus, missStatus, points uint32) []byte {
	b := &bb{}
	b.guid(synthPlayer())
	b.guid(synthPlayer())
	b.guid(synthCast())
	b.guid(wire.EmptyGUID)
	b.i32(4242)
	b.i32(0)
	b.i32(0)
	b.u32(0) // CastFlags
	b.u32(0) // CastFlagsEx
	b.u32(0) // CastFlagsEx2
	b.u32(0) // CastTime
	b.u32(TargetFlagUnit)
	b.guid(synthCreature())
	b.guid(wire.EmptyGUID)
	b.guid(wire.EmptyGUID)
	b.bit(false)
	b.bit(false)
	b.bit(false)
	b.bit(false)
	b.bit(false)
	b.bits(0, 7)
	b.flushBits()
	b.u32(0)
	b.f32(0)
	b.i32(0)
	b.u8(0)
	b.i32(0)
	b.i32(0)
	b.i32(0)
	b.u32(0)
	b.guid(wire.EmptyGUID)
	b.bits(hit, 16)
	b.bits(miss, 16)
	b.bits(hitStatus, 16)
	b.bits(missStatus, 16)
	b.bits(0, 9) // remaining power
	b.bit(false)
	b.bits(points, 16)
	b.flushBits()
	b.bit(false) // the GO's log-data bit
	return b.done()
}

// ---- writers against the server's readers ----

// reader mirrors Server/Packets/SpellPackets.cpp:243-288 (the client → server reader) independently of the writer,
// so a wrong field order or width shows up here. It reads only what this client sends and requires the packet to
// end exactly.
func readServerCastRequest(t *testing.T, p []byte) CastRequest {
	t.Helper()
	b := wire.NewReader(p)
	var r CastRequest
	r.CastID = wire.ReadPackedGUID(b)
	r.SendCastFlags = b.ReadU8()
	for range 3 {
		if v := b.ReadI32(); v != 0 {
			t.Fatalf("Misc is not zero: %d", v)
		}
	}
	r.SpellID = b.ReadI32()
	r.Visual = SpellVisual{SpellXSpellVisualID: b.ReadI32(), ScriptVisualID: b.ReadI32()}
	r.Target.Flags = b.ReadU32()
	r.Target.Unit = wire.ReadPackedGUID(b)
	r.Target.Item = wire.ReadPackedGUID(b)
	r.Target.Housing = wire.ReadPackedGUID(b)
	if b.ReadBit() {
		t.Fatal("HousingIsResident set")
	}
	hasSrc, hasDst, hasOrient, hasMap := b.ReadBit(), b.ReadBit(), b.ReadBit(), b.ReadBit()
	nameLen := int(b.ReadBits(7))
	b.FlushBits()
	if hasSrc || hasDst || hasOrient || hasMap {
		t.Fatalf("optional target parts set: %v %v %v %v", hasSrc, hasDst, hasOrient, hasMap)
	}
	r.Target.Name = b.ReadString(nameLen)
	// MissileTrajectory: the writer sends two floats.
	if pitch := b.ReadFloat32(); pitch != 0 {
		t.Fatalf("pitch %v", pitch)
	}
	if speed := b.ReadFloat32(); speed != 0 {
		t.Fatalf("speed %v", speed)
	}
	if npc := wire.ReadPackedGUID(b); !npc.IsZero() {
		t.Fatalf("crafting NPC %v", npc)
	}
	for i, what := range []string{"extra currency", "crafting reagents", "removed reagents"} {
		if n := b.ReadU32(); n != 0 {
			t.Fatalf("%s count %d", what, n)
		}
		_ = i
	}
	if flags := b.ReadU8(); flags != 0 {
		t.Fatalf("crafting cast flags %d", flags)
	}
	if b.ReadBit() {
		t.Fatal("ReceiveTime present")
	}
	if b.ReadBit() {
		t.Fatal("MoveUpdate present")
	}
	if n := b.ReadBits(2); n != 0 {
		t.Fatalf("weight count %d", n)
	}
	if b.ReadBit() {
		t.Fatal("CraftingOrderID present")
	}
	b.FlushBits()
	if b.Remaining() != 0 {
		t.Fatalf("%d trailing bytes after the cast request", b.Remaining())
	}
	return r
}

// TestWritersMatchTheServerReaders writes each client packet and reads it back the way the server reads it.
func TestWritersMatchTheServerReaders(t *testing.T) {
	unit := wire.GUID{Low: 0x3344, High: 0x2001180000000000}

	t.Run("set selection", func(t *testing.T) {
		p := BuildSetSelection(unit)
		b := wire.NewReader(p)
		if got := wire.ReadPackedGUID(b); got != unit {
			t.Fatalf("selection %v, want %v", got, unit)
		}
		if b.Remaining() != 0 {
			t.Fatalf("%d trailing bytes", b.Remaining())
		}
		if len(p) != 2+popcountPacked(unit) {
			t.Fatalf("length %d for a %d-byte packed GUID", len(p), 2+popcountPacked(unit))
		}
	})
	t.Run("attack swing", func(t *testing.T) {
		b := wire.NewReader(BuildAttackSwing(unit))
		if got := wire.ReadPackedGUID(b); got != unit {
			t.Fatalf("victim %v", got)
		}
		if b.Remaining() != 0 {
			t.Fatalf("%d trailing bytes", b.Remaining())
		}
	})
	t.Run("attack stop", func(t *testing.T) {
		if p := BuildAttackStop(); len(p) != 0 {
			t.Fatalf("attack stop carries %d bytes", len(p))
		}
	})
	t.Run("cancel cast", func(t *testing.T) {
		b := wire.NewReader(BuildCancelCast(synthCast(), 4242))
		if got := wire.ReadPackedGUID(b); got != synthCast() {
			t.Fatalf("cast id %v", got)
		}
		if got := b.ReadU32(); got != 4242 {
			t.Fatalf("spell %d", got)
		}
		if b.Remaining() != 0 {
			t.Fatalf("%d trailing bytes", b.Remaining())
		}
	})
	t.Run("cast at a unit", func(t *testing.T) {
		req := CastRequest{CastID: CastGUID(0, 133, 7), SpellID: 133, Target: UnitTarget(unit)}
		got := readServerCastRequest(t, BuildCastSpell(req))
		if got.CastID != req.CastID || got.SpellID != 133 {
			t.Fatalf("cast id %v spell %d", got.CastID, got.SpellID)
		}
		if got.Target.Flags != TargetFlagUnit || got.Target.Unit != unit {
			t.Fatalf("target %+v", got.Target)
		}
		if got.Target.Name != "" {
			t.Fatalf("name %q", got.Target.Name)
		}
	})
	t.Run("self cast", func(t *testing.T) {
		req := CastRequest{CastID: CastGUID(0, 6673, 8), SpellID: 6673, Target: SelfTarget()}
		got := readServerCastRequest(t, BuildCastSpell(req))
		if got.SpellID != 6673 || got.Target.Flags != 0 || !got.Target.Unit.IsZero() {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the cast's tail is the reader's bit group", func(t *testing.T) {
		// The four optional bits and the two-bit weight count are one byte, flushed, and nothing follows it.
		p := BuildCastSpell(CastRequest{CastID: synthCast(), SpellID: 4242, Target: SelfTarget()})
		if n := len(p); n == 0 {
			t.Fatal("empty packet")
		}
		if last := p[len(p)-1]; last != 0 {
			t.Fatalf("the trailing bit byte is %#x, want the four absent optionals and a zero weight count", last)
		}
	})
}

func popcountPacked(g wire.GUID) int {
	w := wire.NewWriter()
	wire.WritePackedGUID(w, g)
	return len(w.Bytes()) - 2
}

// TestSpellFailureName covers the naming of the codes the fight can hit and the numeric fallback.
func TestSpellFailureName(t *testing.T) {
	for code, want := range map[int32]string{
		118: "out of range",
		91:  "not ready",
		75:  "moving",
		71:  "line of sight",
		157: "unit not in front",
		13:  "bad targets",
		130: "targets dead",
		126: "spell in progress",
		108: "no power",
	} {
		if got := SpellFailureName(code); got != want {
			t.Errorf("SpellFailureName(%d) = %q, want %q", code, got, want)
		}
	}
	if got := SpellFailureName(9999); got != "spell failure 9999" {
		t.Errorf("the fallback = %q", got)
	}
}

// TestSwingErrString pins the four swing error names.
func TestSwingErrString(t *testing.T) {
	for e, want := range map[SwingErr]string{
		SwingNotInRange: "not in range",
		SwingBadFacing:  "bad facing",
		SwingCantAttack: "cannot attack",
		SwingDeadTarget: "dead target",
	} {
		if got := e.String(); got != want {
			t.Errorf("%d = %q, want %q", e, got, want)
		}
	}
	if got := SwingErr(9).String(); !strings.Contains(got, "9") {
		t.Errorf("an unknown reason = %q", got)
	}
}

// FuzzCombatPackets feeds every fixture through its parser under mutation: no panic, no allocation from a forged
// count (the bounds refuse first).
func FuzzCombatPackets(f *testing.F) {
	dir := "testdata/combat.json"
	data, err := os.ReadFile(dir)
	if err != nil {
		f.Fatalf("reading %s: %v", dir, err)
	}
	var doc fixtureFile
	if err := json.Unmarshal(data, &doc); err != nil {
		f.Fatalf("parsing %s: %v", dir, err)
	}
	for _, p := range doc.Packets {
		payload, err := hex.DecodeString(p.Payload)
		if err != nil {
			f.Fatalf("%s: %v", p.Name, err)
		}
		core := uint32(0)
		for _, c := range []uint32{
			wire.SMSGAttackStart, wire.SMSGAttackStop, wire.SMSGAttackSwingError, wire.SMSGAttackSwingLandedLog,
			wire.SMSGAttackerStateUpdate, wire.SMSGSpellStart, wire.SMSGSpellGo, wire.SMSGCastFailed,
			wire.SMSGSpellFailure, wire.SMSGSpellFailedOther, wire.SMSGSpellNonMeleeDamage, wire.SMSGSpellHealLog,
			wire.SMSGSpellPeriodicAuraLog, wire.SMSGSpellEnergizeLog, wire.SMSGSpellDelayed, wire.SMSGAuraUpdate,
			wire.SMSGSpellCooldown, wire.SMSGCooldownEvent, wire.SMSGClearCooldown, wire.SMSGClearCooldowns,
			wire.SMSGPowerUpdate, wire.SMSGLogXPGain, wire.SMSGDeathReleaseLoc, wire.SMSGSendKnownSpells,
		} {
			if wire.ServerName(c) == p.Opcode {
				core = c
			}
		}
		if core == 0 {
			f.Fatalf("%s: unknown opcode %q", p.Name, p.Opcode)
		}
		f.Add(core, payload)
	}
	f.Fuzz(func(t *testing.T, core uint32, payload []byte) {
		switch core {
		case wire.SMSGAttackStart, wire.SMSGAttackStop, wire.SMSGAttackSwingError, wire.SMSGAttackSwingLandedLog,
			wire.SMSGAttackerStateUpdate, wire.SMSGSpellStart, wire.SMSGSpellGo, wire.SMSGCastFailed,
			wire.SMSGSpellFailure, wire.SMSGSpellFailedOther, wire.SMSGSpellNonMeleeDamage, wire.SMSGSpellHealLog,
			wire.SMSGSpellPeriodicAuraLog, wire.SMSGSpellEnergizeLog, wire.SMSGSpellDelayed, wire.SMSGAuraUpdate,
			wire.SMSGSpellCooldown, wire.SMSGCooldownEvent, wire.SMSGClearCooldown, wire.SMSGClearCooldowns,
			wire.SMSGPowerUpdate, wire.SMSGLogXPGain, wire.SMSGDeathReleaseLoc, wire.SMSGSendKnownSpells:
			_, _ = parseFixture(payload, core)
		default:
			t.Skip()
		}
	})
}

// TestCastLogPowerCountRefused: a cast log whose power count is past the number of power types is an error, not a
// half-read struct. The old parser returned what it had read and no error.
func TestCastLogPowerCountRefused(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU64(0) // Health
	for range 5 {
		b.WriteI32(0) // AttackPower, SpellPower, Armor, Versatility, Avoidance
	}
	b.WriteBit(false)           // Hide
	b.WriteBits(maxPowers+4, 9) // the power count
	b.FlushBits()
	if _, err := parseCastLogData(wire.NewReader(b.Bytes())); err == nil {
		t.Fatal("a cast log with 20 power entries was accepted")
	}
}
