package combat

// The fixture generator: it cuts the committed synthetic fixtures out of the private capture. It is not part of CI
// (it needs FOREVER_CAPTURE_PKT and FOREVER_WRITE_FIXTURES=1). Each captured payload has every packed GUID the
// parse decoded replaced by a synthetic GUID of the same mask and type (collected through wire.Buffer's GUID hook,
// never a per-parser field list), the run's clock values replaced, and is re-parsed afterwards: the rewrite must
// still be consumed to the last byte. The hand-built fixtures (no capture has the case) are labelled in the file.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// pktPacket is one record of a PKT 3.1 packet log (PacketLog.cpp:29-52).
type pktPacket struct {
	ToServer bool
	Core     uint32
	Payload  []byte
}

// readPKT parses a PKT 3.1 file: a 66-byte log header, then records of a 44-byte header (direction, connection,
// arrival ticks, optional-data size, length, 20 bytes optional data, opcode) and Length-4 payload bytes.
func readPKT(t *testing.T, path string) []pktPacket {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if len(data) < 66 || string(data[:3]) != "PKT" {
		t.Fatalf("%s is not a PKT file", path)
	}
	const headerSize = 44
	var out []pktPacket
	for off := 66; off+headerSize <= len(data); {
		direction := binary.LittleEndian.Uint32(data[off:])
		size := binary.LittleEndian.Uint32(data[off+16:])
		core := binary.LittleEndian.Uint32(data[off+40:])
		if size < 4 || off+headerSize+int(size)-4 > len(data) {
			t.Fatalf("%s: record at %d has length %d", path, off, size)
		}
		payload := make([]byte, size-4)
		copy(payload, data[off+headerSize:off+headerSize+int(size)-4])
		out = append(out, pktPacket{ToServer: direction == 0x47534D43, Core: core, Payload: payload})
		off += headerSize + int(size) - 4
	}
	return out
}

// fixturePacket is one committed packet.
type fixturePacket struct {
	Name    string   `json:"name"`
	Opcode  string   `json:"opcode"`
	Source  string   `json:"source"`
	Payload string   `json:"payload"`
	GUIDs   []string `json:"synthetic_guids"`
	Notes   []string `json:"rewritten"`
}

// fixtureFile is the committed document.
type fixtureFile struct {
	Note    string          `json:"note"`
	Packets []fixturePacket `json:"packets"`
}

// sub is one byte substitution.
type sub struct {
	old, new []byte
	what     string
	unique   bool
}

// apply applies substitutions in order and fails on an absent pattern.
func apply(t *testing.T, payload []byte, subs []sub) []byte {
	t.Helper()
	for _, s := range subs {
		if !bytes.Contains(payload, s.old) {
			t.Fatalf("fixture: %s (% X) is not in the payload", s.what, s.old)
		}
		if s.unique && bytes.Count(payload, s.old) != 1 {
			t.Fatalf("fixture: %s (% X) occurs %d times; the rewrite would change more than the field it names",
				s.what, s.old, bytes.Count(payload, s.old))
		}
		payload = bytes.ReplaceAll(payload, s.old, s.new)
	}
	return payload
}

// syntheticGUID returns the synthetic packed form: the same mask (so the length is identical) and the same type
// byte (bit 15, so a parser still reads the object type), every other payload byte deterministic.
func syntheticGUID(orig []byte, n int) []byte {
	out := append([]byte(nil), orig...)
	mask := uint16(orig[0]) | uint16(orig[1])<<8
	payload := 0
	for i := range 16 {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		payload++
		if i == 15 {
			continue
		}
		out[2+payload-1] = byte(0x21 + (n*17+i*7)%0x58)
	}
	return out
}

// u32 is the little-endian encoding of v.
func u32(v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return b[:]
}

// rewriteCaptured rewrites one captured payload. The GUID hook reports every packed GUID with the offset of its
// packed form, so exactly those bytes are rewritten — never a look-alike inside a damage value or a count. coreParse
// runs the parser on the hooked buffer and returns the extra substitutions its fields need.
func rewriteCaptured(t *testing.T, name string, core uint32, payload []byte, coreParse func(*wire.Buffer) ([]sub, error), source string, notes []string) fixturePacket {
	t.Helper()
	type hit struct {
		guid wire.GUID
		off  int
	}
	var hits []hit
	b := wire.NewReader(payload)
	b.SetGUIDAtHook(func(g wire.GUID, off int) { hits = append(hits, hit{g, off}) })
	extra, err := coreParse(b)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	payload = append([]byte(nil), payload...)
	synthetic := map[string][]byte{}
	var synthetics []string
	for _, h := range hits {
		if h.off < 0 || h.off+2 > len(payload) {
			t.Fatalf("%s: GUID offset %d does not fit", name, h.off)
		}
		mask := binary.LittleEndian.Uint16(payload[h.off:])
		n := 2 + bits.OnesCount16(mask)
		if mask == 0 {
			continue // the empty GUID stays empty
		}
		if h.off+n > len(payload) {
			t.Fatalf("%s: packed GUID at %d needs %d bytes", name, h.off, n)
		}
		orig := payload[h.off : h.off+n]
		key := hex.EncodeToString(orig)
		syn, ok := synthetic[key]
		if !ok {
			syn = syntheticGUID(orig, len(synthetic))
			synthetic[key] = syn
			synthetics = append(synthetics, hex.EncodeToString(syn))
		}
		copy(payload[h.off:h.off+n], syn)
	}
	payload = apply(t, payload, extra)
	if _, err := parseFixture(payload, core); err != nil {
		t.Fatalf("%s after the rewrite: %v", name, err)
	}
	notes = append(notes, "every packed GUID the parse decoded was rewritten in place (the list is original -> synthetic)")
	return fixturePacket{
		Name: name, Opcode: wire.ServerName(core), Source: source,
		Payload: hex.EncodeToString(payload), GUIDs: synthetics, Notes: notes,
	}
}

// TestWriteFixtures regenerates testdata/combat.json from the capture. It is a one-off: the fixtures are committed
// and the tests never need the capture.
func TestWriteFixtures(t *testing.T) {
	path := os.Getenv("FOREVER_CAPTURE_PKT")
	if path == "" || os.Getenv("FOREVER_WRITE_FIXTURES") == "" {
		t.Skip("set FOREVER_CAPTURE_PKT and FOREVER_WRITE_FIXTURES=1 to regenerate the fixtures")
	}
	packets := readPKT(t, path)
	out := fixtureFile{
		Note: "Synthetic fixtures for the combat round. The captured packets were cut from the private capture and " +
			"every packed GUID, position and clock value rewritten with the same lengths and bit layout (the " +
			"generator is fixture_gen_test.go). The packets whose source says hand-built come from the server's " +
			"writer because no capture contains the case. The payloads carry no opcode; the opcodes are the " +
			"core's numbers, before the Classic wire translation.",
	}

	// find returns the first server packet of core whose payload satisfies ok.
	find := func(core uint32, ok func(p []byte) bool) pktPacket {
		t.Helper()
		for _, p := range packets {
			if p.ToServer || p.Core != core {
				continue
			}
			if ok == nil || ok(p.Payload) {
				return p
			}
		}
		t.Fatalf("no captured packet of opcode 0x%06X matches", core)
		return pktPacket{}
	}

	// add rewrites one captured packet from the private capture.
	add := func(name string, pkt pktPacket, coreParse func(*wire.Buffer) ([]sub, error), notes []string) {
		t.Helper()
		out.Packets = append(out.Packets, rewriteCaptured(t, name, pkt.Core, pkt.Payload, coreParse, "capture: 3-combat.pkt", notes))
	}

	// swipe parses a cast on a buffer and returns no extra substitutions.
	castSubs := func(b *wire.Buffer) ([]sub, error) {
		if _, err := parseSpellCast(b); err != nil {
			return nil, err
		}
		return nil, nil
	}

	add("attack_start", find(wire.SMSGAttackStart, nil), func(b *wire.Buffer) ([]sub, error) {
		if _, err := parseAttackStart(b); err != nil {
			return nil, err
		}
		return nil, nil
	}, nil)
	add("attack_stop", find(wire.SMSGAttackStop, func(p []byte) bool {
		v, err := parseAttackStop(wire.NewReader(p))
		return err == nil && v.NowDead
	}), func(b *wire.Buffer) ([]sub, error) {
		if _, err := parseAttackStop(b); err != nil {
			return nil, err
		}
		return nil, nil
	}, []string{"a stop whose NowDead bit is set (the victim died)"})

	roundSubs := func(b *wire.Buffer) ([]sub, error) {
		if _, err := parseRoundPacket(b); err != nil {
			return nil, err
		}
		return nil, nil
	}
	add("attacker_state_update", find(wire.SMSGAttackerStateUpdate, func(p []byte) bool {
		b := wire.NewReader(p)
		v, err := parseRoundPacket(b)
		return err == nil && b.Remaining() == 0 && v.VictimState == 1 && v.RageGained != 0
	}), roundSubs, []string{"a landed hit with a rage gain; the round's ContentTuning keeps its captured values (flags 3 = no level/item scaling, ids 0)"})
	add("attacker_state_update_dodge", find(wire.SMSGAttackerStateUpdate, func(p []byte) bool {
		b := wire.NewReader(p)
		v, err := parseRoundPacket(b)
		return err == nil && b.Remaining() == 0 && v.VictimState == 2
	}), roundSubs, []string{"victim state dodge (2)"})

	auraSubs := func(b *wire.Buffer) ([]sub, error) {
		v, err := parseAuraUpdate(b)
		if err != nil {
			return nil, err
		}
		var subs []sub
		for _, a := range v.Auras {
			if a.Data == nil {
				continue
			}
			if a.Data.Duration != nil {
				subs = append(subs, sub{old: u32(*a.Data.Duration), new: u32(960000), what: "aura duration", unique: true})
			}
			if a.Data.Remaining != nil {
				subs = append(subs, sub{old: u32(*a.Data.Remaining), new: u32(960000), what: "aura remaining", unique: true})
			}
		}
		return subs, nil
	}
	add("aura_update", find(wire.SMSGAuraUpdate, func(p []byte) bool {
		b := wire.NewReader(p)
		v, err := parseAuraUpdate(b)
		return err == nil && b.Remaining() == 0 && len(v.Auras) == 1 && v.Auras[0].Data != nil && v.Auras[0].Data.Duration != nil
	}), auraSubs, []string{"one aura with a duration and a caster; duration and remaining rewritten to 960000 ms"})
	add("aura_update_multi", find(wire.SMSGAuraUpdate, func(p []byte) bool {
		b := wire.NewReader(p)
		v, err := parseAuraUpdate(b)
		return err == nil && b.Remaining() == 0 && len(v.Auras) == 3 && v.Auras[2].Data != nil && v.Auras[2].Data.Duration != nil
	}), auraSubs, []string{"three auras on one unit, the last with a duration; durations and remainings rewritten to 960000 ms"})
	add("aura_removed", find(wire.SMSGAuraUpdate, func(p []byte) bool {
		b := wire.NewReader(p)
		v, err := parseAuraUpdate(b)
		return err == nil && b.Remaining() == 0 && len(v.Auras) == 1 && v.Auras[0].Data == nil
	}), func(b *wire.Buffer) ([]sub, error) {
		if _, err := parseAuraUpdate(b); err != nil {
			return nil, err
		}
		return nil, nil
	}, []string{"the removal form: a slot and no aura data"})

	add("spell_start", find(wire.SMSGSpellStart, func(p []byte) bool {
		b := wire.NewReader(p)
		v, err := parseSpellCast(b)
		return err == nil && b.Remaining() == 0 && v.CastTime > 0 && len(v.Remaining) > 0
	}), castSubs, []string{"a cast with a cast time and a remaining-power list"})
	add("spell_go", find(wire.SMSGSpellGo, func(p []byte) bool {
		b := wire.NewReader(p)
		v, err := parseSpellCast(b)
		if err != nil || b.Remaining() != 1 || len(v.HitTargets) == 0 {
			return false
		}
		// the trailing byte is the log-data bit byte; consume it so the check means "exact"
		if b.ReadBit() {
			return false
		}
		return b.Remaining() == 0
	}), func(b *wire.Buffer) ([]sub, error) {
		v, err := parseSpellCast(b)
		if err != nil {
			return nil, err
		}
		var subs []sub
		// CastTime in a GO is getMSTime() (Spell.cpp:4985), the server's uptime: a run value.
		if v.CastTime != 0 {
			subs = append(subs, sub{old: u32(v.CastTime), new: u32(960000), what: "server uptime", unique: true})
		}
		return subs, nil
	}, []string{"the GO's CastTime is the server uptime (getMSTime, Spell.cpp:4985) and was rewritten to 960000"})
	add("spell_failure", find(wire.SMSGSpellFailure, nil), func(b *wire.Buffer) ([]sub, error) {
		if _, err := parseSpellFailure(b); err != nil {
			return nil, err
		}
		return nil, nil
	}, nil)
	add("spell_failed_other", find(wire.SMSGSpellFailedOther, nil), func(b *wire.Buffer) ([]sub, error) {
		if _, err := parseSpellFailedOther(b); err != nil {
			return nil, err
		}
		return nil, nil
	}, nil)
	add("spell_non_melee_damage_log", find(wire.SMSGSpellNonMeleeDamage, nil), func(b *wire.Buffer) ([]sub, error) {
		if _, err := parseNonMelee(b); err != nil {
			return nil, err
		}
		return nil, nil
	}, nil)
	add("spell_periodic_aura_log", find(wire.SMSGSpellPeriodicAuraLog, nil), func(b *wire.Buffer) ([]sub, error) {
		if _, err := parsePeriodicAura(b); err != nil {
			return nil, err
		}
		return nil, nil
	}, nil)
	add("spell_energize_log", find(wire.SMSGSpellEnergizeLog, nil), func(b *wire.Buffer) ([]sub, error) {
		if _, err := parseEnergize(b); err != nil {
			return nil, err
		}
		return nil, nil
	}, nil)
	add("spell_delayed", find(wire.SMSGSpellDelayed, nil), func(b *wire.Buffer) ([]sub, error) {
		v, err := parseSpellDelayed(b)
		if err != nil {
			return nil, err
		}
		if v.Delay == 0 {
			return nil, nil
		}
		return []sub{{old: u32(v.Delay), new: u32(960000), what: "cast delay", unique: true}}, nil
	}, []string{"the delay (a run's latency) rewritten to 960000 ms"})
	add("power_update", find(wire.SMSGPowerUpdate, func(p []byte) bool {
		b := wire.NewReader(p)
		v, err := parsePowerUpdate(b)
		// The capture's server also logs a flood of 17-byte packets under this repack's number: a real power update
		// names a power type this client knows (0-3) and a value that fits a bar, which that flood does not.
		return err == nil && b.Remaining() == 0 && len(v.Powers) > 0 && v.Powers[0].Type <= 3 && v.Powers[0].Value < 100000
	}), func(b *wire.Buffer) ([]sub, error) {
		if _, err := parsePowerUpdate(b); err != nil {
			return nil, err
		}
		return nil, nil
	}, nil)

	// Hand-built fixtures: no capture has the case. They are written the way the server's writer writes them (the
	// file:line is in the notes of each).
	hand := func(name string, core uint32, payload []byte, notes ...string) {
		t.Helper()
		if _, err := parseFixture(payload, core); err != nil {
			t.Fatalf("%s (hand-built): %v", name, err)
		}
		// The GUID list of a hand-built fixture comes from the same parse the audit runs, so the sweep covers it too.
		var synthetics []string
		seen := map[string]bool{}
		b := wire.NewReader(payload)
		b.SetGUIDAtHook(func(g wire.GUID, off int) {
			mask := binary.LittleEndian.Uint16(payload[off:])
			n := 2 + bits.OnesCount16(mask)
			key := hex.EncodeToString(payload[off : off+n])
			if mask != 0 && !seen[key] {
				seen[key] = true
				synthetics = append(synthetics, key)
			}
		})
		if _, err := parseFixtureOn(b, core); err != nil {
			t.Fatalf("%s (hand-built, audit parse): %v", name, err)
		}
		out.Packets = append(out.Packets, fixturePacket{
			Name: name, Opcode: wire.ServerName(core), Source: "hand-built from the server's writer",
			Payload: hex.EncodeToString(payload), GUIDs: synthetics, Notes: notes,
		})
	}
	hand("attack_swing_landed_log", wire.SMSGAttackSwingLandedLog, buildLandedLog(),
		"Unit.cpp:5963-5968: uint32 0, then the full log packet of the same swing (log-data bit true)")
	hand("spell_heal_log", wire.SMSGSpellHealLog, buildHealLog(),
		"CombatLogPackets.cpp:180-222")
	hand("spell_cooldown", wire.SMSGSpellCooldown, buildCooldown(),
		"SpellPackets.cpp:671-689")
	hand("cooldown_event", wire.SMSGCooldownEvent, buildCooldownEvent(),
		"SpellPackets.cpp:620-627")
	hand("clear_cooldown", wire.SMSGClearCooldown, buildClearCooldown(),
		"SpellPackets.cpp:641-649")
	hand("clear_cooldowns", wire.SMSGClearCooldowns, buildClearCooldowns(),
		"SpellPackets.cpp:629-639: the count and the append(T*,count) list, four bytes each")
	hand("log_xp_gain", wire.SMSGLogXPGain, buildXPGain(),
		"CharacterPackets.cpp:881-890, reason 0 = kill")
	hand("death_release_loc", wire.SMSGDeathReleaseLoc, buildDeathRelease(),
		"MiscPackets.cpp:284-289")
	hand("send_known_spells", wire.SMSGSendKnownSpells, buildKnownSpells(),
		"SpellPackets.cpp:490-510")
	hand("spell_go_miss", wire.SMSGSpellGo, buildSpellGoMiss(),
		"SpellPackets.cpp:415-464 with a miss list: a reflect entry carries its extra status byte")

	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join("testdata", "combat.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d fixtures", len(out.Packets))
}

// parseFixture parses a payload by its core opcode; every parser requires exact consumption.
func parseFixture(payload []byte, core uint32) (any, error) {
	return parseFixtureOn(wire.NewReader(payload), core)
}

// parseFixtureOn parses on a caller-provided reader, which is the seam the fixture audits use: a hook set on that
// reader sees every GUID the parse decodes, including the ones inside a framed sub-buffer.
func parseFixtureOn(b *wire.Buffer, core uint32) (any, error) {
	v, err := parseFixtureCore(b, core)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

// parseFixtureCore dispatches to the buffer-taking parser of an opcode.
func parseFixtureCore(b *wire.Buffer, core uint32) (any, error) {
	switch core {
	case wire.SMSGAttackStart:
		return parseAttackStart(b)
	case wire.SMSGAttackStop:
		return parseAttackStop(b)
	case wire.SMSGAttackSwingError:
		return parseSwingError(b)
	case wire.SMSGAttackerStateUpdate:
		return parseAttackerStateUpdate(b)
	case wire.SMSGAttackSwingLandedLog:
		return parseAttackSwingLanded(b)
	case wire.SMSGSpellStart:
		return parseSpellCast(b) // the START has no trailing log bit
	case wire.SMSGSpellGo:
		return parseSpellGoCast(b)
	case wire.SMSGCastFailed:
		return parseCastFailed(b)
	case wire.SMSGSpellFailure:
		return parseSpellFailure(b)
	case wire.SMSGSpellFailedOther:
		return parseSpellFailedOther(b)
	case wire.SMSGSpellNonMeleeDamage:
		return parseNonMelee(b)
	case wire.SMSGSpellHealLog:
		return parseHeal(b)
	case wire.SMSGSpellPeriodicAuraLog:
		return parsePeriodicAura(b)
	case wire.SMSGSpellEnergizeLog:
		return parseEnergize(b)
	case wire.SMSGSpellDelayed:
		return parseSpellDelayed(b)
	case wire.SMSGAuraUpdate:
		return parseAuraUpdate(b)
	case wire.SMSGSpellCooldown:
		return parseCooldown(b)
	case wire.SMSGCooldownEvent:
		return parseCooldownEvent(b)
	case wire.SMSGClearCooldown:
		return parseClearCooldown(b)
	case wire.SMSGClearCooldowns:
		return parseClearCooldowns(b)
	case wire.SMSGPowerUpdate:
		return parsePowerUpdate(b)
	case wire.SMSGLogXPGain:
		return parseXPGain(b)
	case wire.SMSGDeathReleaseLoc:
		return parseDeathRelease(b)
	case wire.SMSGSendKnownSpells:
		return parseKnownSpells(b)
	}
	return nil, errShort
}

// ---- hand-built packet builders ----

// bb is a fixture builder that packs bits the way the server's ByteBuffer does.
type bb struct {
	bytes []byte
	cur   byte
	used  int
}

func (b *bb) u8(v uint8) { b.flushBits(); b.bytes = append(b.bytes, v) }
func (b *bb) u16(v uint16) {
	b.u8(byte(v))
	b.u8(byte(v >> 8))
}
func (b *bb) u32(v uint32) {
	b.u16(uint16(v))
	b.u16(uint16(v >> 16))
}
func (b *bb) u64(v uint64) {
	b.u32(uint32(v))
	b.u32(uint32(v >> 32))
}
func (b *bb) i32(v int32)   { b.u32(uint32(v)) }
func (b *bb) f32(v float32) { b.u32(math.Float32bits(v)) }
func (b *bb) bit(v bool) {
	b.cur <<= 1
	if v {
		b.cur |= 1
	}
	b.used++
	if b.used == 8 {
		b.bytes = append(b.bytes, b.cur)
		b.cur, b.used = 0, 0
	}
}
func (b *bb) bits(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		b.bit(v&(1<<uint(i)) != 0)
	}
}
func (b *bb) flushBits() {
	if b.used != 0 {
		b.cur <<= 8 - b.used
		b.bytes = append(b.bytes, b.cur)
		b.cur, b.used = 0, 0
	}
}
func (b *bb) guid(g wire.GUID) {
	w := wire.NewWriter()
	wire.WritePackedGUID(w, g)
	b.flushBits()
	b.bytes = append(b.bytes, w.Bytes()...)
}
func (b *bb) raw(p []byte) { b.flushBits(); b.bytes = append(b.bytes, p...) }
func (b *bb) done() []byte { b.flushBits(); return b.bytes }

// Synthetic identifiers for the hand-built fixtures: a player, a creature and a cast, spelled out the way the
// packed form writes them.
func synthPlayer() wire.GUID   { return wire.GUID{Low: 0x1122, High: 0x0801180000000000} }
func synthCreature() wire.GUID { return wire.GUID{Low: 0x3344, High: 0x2001180000000000} }
func synthCast() wire.GUID     { return CastGUID(0, 4242, 17) }

// buildRound lays out one attack round exactly as CombatLogPackets.cpp:371-425 does.
func buildRound(flags uint32, victimState uint8) *bb {
	b := &bb{}
	b.u32(flags)
	b.guid(synthPlayer())
	b.guid(synthCreature())
	b.i32(7)  // Damage
	b.i32(7)  // OriginalDamage
	b.i32(-1) // OverDamage
	b.u8(1)   // sub damage present
	b.i32(1)  // SchoolMask
	b.f32(7)  // FDamage
	b.i32(7)  // Damage
	if flags&(hitInfoFullAbsorb|hitInfoPartialAbsorb) != 0 {
		b.i32(0)
	}
	if flags&(hitInfoFullResist|hitInfoPartialResist) != 0 {
		b.i32(0)
	}
	b.u8(victimState)
	b.u32(0) // AttackerState
	b.u32(0) // MeleeSpellID
	if flags&hitInfoBlock != 0 {
		b.i32(0)
	}
	if flags&hitInfoRageGain != 0 {
		b.i32(3)
	}
	if flags&hitInfoUnknown1 != 0 {
		b.u32(0)
		for range 11 {
			b.f32(0)
		}
		b.u32(0)
	}
	if flags&(hitInfoBlock|hitInfoUnknown12) != 0 {
		b.f32(0)
	}
	// ContentTuningParams: flags 3 (no level/item scaling), all ids zero, ratio 1.
	b.f32(0)
	b.f32(0)
	b.u16(0)
	b.i32(0)
	b.i32(0)
	b.i32(0)
	b.u8(0)
	b.u8(0)
	b.u8(0)
	b.u32(3)
	b.i32(0)
	b.i32(0)
	b.i32(0)
	b.f32(1)
	b.bits(0, 4)
	return b
}

// buildCastLogData is SpellCastLogData (CombatLogPacketsCommon.cpp:150-172) with the fields the landed log carries.
func buildCastLogData(b *bb) {
	b.u64(60) // Health
	b.i32(10) // AttackPower
	b.i32(0)  // SpellPower
	b.i32(0)  // Armor
	b.i32(0)  // Versatility
	b.i32(0)  // Avoidance
	b.bit(false)
	b.bits(1, 9) // one power entry
	b.flushBits()
	b.u8(1) // rage
	b.i32(35)
	b.i32(0)
}

func buildLandedLog() []byte {
	round := buildRound(hitInfoAffectsVictim|hitInfoRageGain, 1).done()
	out := &bb{}
	out.u32(0) // the header the landed log carries (Unit.cpp:5967)
	out.bit(true)
	out.flushBits()
	buildCastLogData(out)
	out.u32(uint32(len(round)))
	out.raw(round)
	return out.done()
}

func buildHealLog() []byte {
	b := &bb{}
	b.guid(synthCreature())
	b.guid(synthPlayer())
	b.i32(4242)
	b.i32(60)    // Health after the heal
	b.i32(25)    // OriginalHeal
	b.i32(0)     // OverHeal
	b.i32(0)     // Absorbed
	b.u32(0)     // supporters
	b.bit(false) // Crit
	b.bit(false) // CritRollMade present
	b.bit(false) // CritRollNeeded present
	b.bit(false) // log data present (the basic packet)
	b.bit(false) // ContentTuning present
	return b.done()
}

func buildCooldown() []byte {
	b := &bb{}
	b.guid(synthPlayer())
	b.u8(0) // SPELL_COOLDOWN_FLAG_NONE
	b.u32(1)
	b.u32(4242) // SpellID
	b.u32(6000) // ForcedCooldown
	b.f32(1)    // ModRate
	return b.done()
}

func buildCooldownEvent() []byte {
	b := &bb{}
	b.i32(4242)
	b.bit(false)
	return b.done()
}

func buildClearCooldown() []byte {
	b := &bb{}
	b.u32(4242)
	b.bit(false)
	b.bit(false)
	return b.done()
}

func buildClearCooldowns() []byte {
	b := &bb{}
	b.u32(2)
	b.i32(4242)
	b.i32(4243)
	b.bit(false)
	return b.done()
}

func buildXPGain() []byte {
	b := &bb{}
	b.guid(synthCreature())
	b.i32(45) // Original
	b.u8(0)   // kill
	b.i32(45) // Amount
	b.f32(0)  // GroupBonus
	return b.done()
}

func buildDeathRelease() []byte {
	b := &bb{}
	b.i32(0)
	b.f32(1000)
	b.f32(2000)
	b.f32(50)
	return b.done()
}

func buildKnownSpells() []byte {
	// SendKnownSpells::Write (SpellPackets.cpp:48-63): the InitialLogin bit flushed into a byte, the two counts, then
	// the two lists of uint32 ids.
	b := &bb{}
	b.bit(true)
	b.flushBits()
	b.u32(3) // known
	b.u32(1) // favourites
	for _, id := range []int32{6603, 6673, 100} {
		b.u32(uint32(id))
	}
	b.u32(6603)
	return b.done()
}

// buildSpellGoMiss is a GO whose miss list carries one plain miss and one reflect (which appends its status byte).
func buildSpellGoMiss() []byte {
	b := &bb{}
	b.guid(synthPlayer())
	b.guid(synthPlayer())
	b.guid(synthCast())
	b.guid(wire.EmptyGUID)
	b.i32(4242)
	b.i32(0)
	b.i32(0)
	b.u32(0x40000) // CAST_FLAG_FROM_CLIENT
	b.u32(0)
	b.u32(0)
	b.u32(0)
	// SpellTargetData: one unit target, nothing optional, no name.
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
	b.u32(0) // TravelTime
	b.f32(0) // Pitch
	b.i32(0) // AmmoDisplayID
	b.u8(0)  // DestLocSpellCastIndex
	b.i32(0) // Immunities
	b.i32(0)
	b.i32(0) // Predict
	b.u32(0)
	b.guid(wire.EmptyGUID)
	b.bits(0, 16) // hit targets
	b.bits(2, 16) // miss targets
	b.bits(0, 16) // hit status
	b.bits(2, 16) // miss status
	b.bits(0, 9)  // remaining power
	b.bit(false)  // runes
	b.bits(0, 16) // target points
	b.flushBits()
	// miss targets: a creature and the player
	b.guid(synthCreature())
	b.guid(synthCreature())
	// miss statuses: block (5), then reflect (11) with its status byte 3
	b.u8(5)
	b.u8(11)
	b.u8(3)
	// the GO's log-data bit
	b.bit(false)
	return b.done()
}

// TestWriteLiveFixtures replaces the hand-built fixtures a live fight can now produce with payloads from the
// fight-check debug capture (forever fight-check --provoke --capture-dir DIR). It edits the committed file in place,
// leaving the fixtures no player action produced (the heal log, the death release, the cooldown forms and the
// reflected miss) as they were. Run it with FOREVER_LIVE_CAPTURE=<parent or run directory> and
// FOREVER_WRITE_FIXTURES=1: the capture is read through the one reader (internal/forever/capturedir), as round 5's
// chat and party fixtures are.
func TestWriteLiveFixtures(t *testing.T) {
	path := os.Getenv("FOREVER_LIVE_CAPTURE")
	if path == "" || os.Getenv("FOREVER_WRITE_FIXTURES") == "" {
		t.Skip("set FOREVER_LIVE_CAPTURE and FOREVER_WRITE_FIXTURES=1 to fold a live capture into the fixtures")
	}
	recs, err := capturedir.ReadDir(path)
	if err != nil {
		t.Fatalf("the live capture: %v", err)
	}
	// find returns the first captured packet of core whose parse satisfies ok.
	find := func(core uint32, ok func([]byte) bool) []byte {
		t.Helper()
		for _, r := range recs {
			if r.Opcode != core {
				continue
			}
			if ok == nil || ok(r.Payload) {
				return r.Payload
			}
		}
		t.Fatalf("the live capture has no %s matching", wire.ServerName(core))
		return nil
	}
	failureOf := func(reason int32) func([]byte) bool {
		return func(p []byte) bool {
			v, err := ParseCastFailed(p)
			return err == nil && v.Reason == reason
		}
	}
	failSubs := func(b *wire.Buffer) ([]sub, error) {
		if _, err := parseCastFailed(b); err != nil {
			return nil, err
		}
		return nil, nil
	}
	swing := find(wire.SMSGAttackSwingError, func(p []byte) bool {
		v, err := ParseSwingError(p)
		return err == nil && v.Reason == SwingNotInRange
	})
	noPower := find(wire.SMSGCastFailed, failureOf(108)) // SPELL_FAILED_NO_POWER
	notReady := find(wire.SMSGCastFailed, failureOf(91)) // SPELL_FAILED_NOT_READY
	source := "live capture: forever fight-check --provoke, 2026-11-18"
	live := map[string]fixturePacket{
		"attack_swing_error": rewriteCaptured(t, "attack_swing_error", wire.SMSGAttackSwingError, swing,
			func(b *wire.Buffer) ([]sub, error) {
				if _, err := parseSwingError(b); err != nil {
					return nil, err
				}
				return nil, nil
			}, source, []string{"the server's own not-in-range answer, from a swing sent at 20.4 yd"}),
		"cast_failed": rewriteCaptured(t, "cast_failed", wire.SMSGCastFailed, noPower, failSubs, source,
			[]string{"a real Heroic Strike refusal: Spell::prepare answered SPELL_FAILED_NO_POWER with the server's cast id and no start before it"}),
		"cast_failed_not_ready": rewriteCaptured(t, "cast_failed_not_ready", wire.SMSGCastFailed, notReady, failSubs, source,
			[]string{"the refusal of a second Battle Shout inside the global cooldown: SPELL_FAILED_NOT_READY"}),
	}

	doc, err := os.ReadFile("testdata/combat.json")
	if err != nil {
		t.Fatalf("the fixtures: %v", err)
	}
	var file fixtureFile
	if err := json.Unmarshal(doc, &file); err != nil {
		t.Fatalf("the fixtures: %v", err)
	}
	seen := map[string]bool{}
	for i, p := range file.Packets {
		if live[p.Name].Name == "" {
			continue
		}
		file.Packets[i] = live[p.Name]
		seen[p.Name] = true
	}
	for _, name := range []string{"attack_swing_error", "cast_failed", "cast_failed_not_ready"} {
		if !seen[name] {
			file.Packets = append(file.Packets, live[name])
		}
	}
	file.Note = "Synthetic fixtures for the combat round. The captured packets were cut from the private capture and " +
		"every packed GUID, position and clock value rewritten with the same lengths and bit layout (the generator is " +
		"fixture_gen_test.go). Packets whose source says \"live capture\" came from the fight-check debug capture " +
		"(--provoke) and were rewritten the same way; the rest whose source says hand-built come from the server's " +
		"writer because no player action produced the case. The payloads carry no opcode; the opcodes are the core's " +
		"numbers, before the Classic wire translation."
	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile("testdata/combat.json", append(out, '\n'), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
