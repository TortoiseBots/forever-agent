package world

// The death flow against a fake world with a fake clock. The fake is the server at the handler boundary: every
// client packet is decoded here, and every answer the real server sends is built the way its writers build it
// (DEATH-R1-NOTES.md cites each one) and fed through the session's own handlers. The clock only moves when the flow
// waits, so the assertions are exact.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/death"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

// The fake's wire numbers for client build 70707 on realm build 70707, hand-computed from ClassicOpcodes.cpp and
// cross-checked against wire.NewOpcodeTable(70707, 70707).WireForClient in TestDeathWireNumbers.
const (
	fakeDeathCMSGRepop            = 0x3F00CB
	fakeDeathCMSGReclaim          = 0x3F0078
	fakeDeathCMSGResurrectReply   = 0x4400AF
	fakeDeathCMSGQueryCorpse      = 0x44008C
	fakeDeathCMSGSetSelection     = 0x3F00CD
	fakeDeathCMSGTalkGossip       = 0x3F0029
	fakeDeathCMSGGossipSelect     = 0x3F002B
	fakeDeathCMSGSpiritHeal       = 0x3F0047
	fakeDeathCMSGCloseInteraction = 0x3F002A
)

// The fake server's gossip menu ids are synthetic (the same sentinel the fixtures carry): the real ids are the
// server's data and never appear in this repository.
const (
	fakeGossipMenu   int32 = 4242
	fakeGossipOption int32 = 4242
)

// TestDeathWireNumbers pins the fake's hand-written numbers against the production translation, so a table change
// cannot silently move them.
func TestDeathWireNumbers(t *testing.T) {
	tab := wire.NewOpcodeTable(70707, 70707)
	for core, want := range map[uint32]uint32{
		wire.CMSGRepopRequest:                fakeDeathCMSGRepop,
		wire.CMSGReclaimCorpse:               fakeDeathCMSGReclaim,
		wire.CMSGResurrectResponse:           fakeDeathCMSGResurrectReply,
		wire.CMSGQueryCorpseLocation:         fakeDeathCMSGQueryCorpse,
		wire.CMSGSetSelection:                fakeDeathCMSGSetSelection,
		wire.CMSGTalkToGossip:                fakeDeathCMSGTalkGossip,
		wire.CMSGGossipSelectOption:          fakeDeathCMSGGossipSelect,
		wire.CMSGSpiritHealerActivate:        fakeDeathCMSGSpiritHeal,
		wire.CMSGCloseInteraction:            fakeDeathCMSGCloseInteraction,
		wire.CMSGMoveForceRunSpeedChangeAck:  fakeCMSGMoveForceRunSpeedAck,
		wire.CMSGMoveForceSwimSpeedChangeAck: fakeCMSGMoveForceSwimSpeedAck,
		wire.CMSGMoveWaterWalkAck:            fakeCMSGMoveWaterWalkAck,
	} {
		got, ok := tab.WireForClient(core)
		if !ok || got != want {
			t.Fatalf("core 0x%06X: wire 0x%06X (ok %v), want 0x%06X", core, got, ok, want)
		}
	}
	// The server packets the fake sends, each pinned both ways.
	for _, tc := range []struct {
		core uint32
		wire uint32
	}{
		{wire.SMSGUpdateObject, fakeSMSGUpdateObject},
		{wire.SMSGMoveTeleport, fakeSMSGMoveTeleport},
		{wire.SMSGAuraUpdate, fakeSMSGAuraUpdate},
		{wire.SMSGCancelCombat, fakeSMSGCancelCombat},
		{wire.SMSGCorpseLocation, fakeSMSGCorpseLocation},
		{wire.SMSGCorpseReclaimDelay, fakeSMSGCorpseReclaim},
		{wire.SMSGDeathReleaseLoc, fakeSMSGDeathReleaseLoc},
		{wire.SMSGPreRessurect, fakeSMSGPreRessurect},
		{wire.SMSGMoveSetRunSpeed, fakeSMSGMoveSetRunSpeed},
		{wire.SMSGMoveSetWaterWalk, fakeSMSGMoveWaterWalk},
		{wire.SMSGStandStateUpdate, fakeSMSGStandStateUpdate},
	} {
		if got := tab.TranslateServerCore(tc.core); got != tc.wire {
			t.Errorf("%s: wire 0x%06X, the fake sends 0x%06X", wire.ServerName(tc.core), got, tc.wire)
		}
		if back := tab.TranslateServerWire(tc.wire); back != tc.core {
			t.Errorf("wire 0x%06X reads back as 0x%06X (%s), want 0x%06X", tc.wire, back, wire.ServerName(back), tc.core)
		}
	}
}

type deathSent struct {
	core    uint32
	payload []byte
	at      time.Time
}

// deathWorld is the fake server at the handler boundary plus the session under test.
type deathWorld struct {
	t      *testing.T
	now    time.Time
	world  *objects.World
	report *Report
	opts   Options
	self   wire.GUID
	mover  *objects.Mover
	s      *session

	sent []deathSent

	// Scenario knobs.
	graveyard CorpsePoint
	corpseAt  CorpsePoint
	// corpseTransport makes the corpse query answer name a transport: the coordinates are the transport's
	// offsets, as the server's writer pairs them with the GUID (QueryPackets.cpp:393-405).
	corpseTransport wire.GUID
	reclaimDelay    time.Duration
	refuseReclaim   bool
	refuseRelease   bool
	// releaseMapChange makes the release's graveyard another map: the answer is the real transfer (pending,
	// suspend, new world, resume) and SMSG_DEATH_RELEASE_LOC names the new map. The corpse stays on the old one.
	releaseMapChange bool
	graveMap         int32
	// releaseReviveAtTeleport is a graveyard of an instance: the server revives at the teleport instead of leaving
	// a ghost (RepopAtGraveyard's shouldResurrect).
	releaseReviveAtTeleport bool
	healer                  wire.GUID
	healerOptionID          int32
	offer                   *death.ResurrectOffer
	offerAt                 time.Time
	offerSent               bool
	// gossip delay: the menu answer waits for the next clock advance instead of arriving inside the send.
	delayGossip   bool
	pendingGossip []byte
	dieAtWait     int
	waits         int
	reviveHealth  int64
	// runSpeed is the speed the release announces for speedMover (self when empty).
	runSpeed   float32
	speedMover wire.GUID
	// reviveAtWait revives the character inside that wait: a resurrection arriving during a walk. revivedAt is the
	// clock when it fired.
	reviveAtWait int
	revivedAt    time.Time
	// noPreRessurect answers the repop request with the ghost aura only (no SMSG_PRE_RESSURECT), which a real server
	// may skip on a character that is already a ghost.
	noPreRessurect bool
	// noCorpse makes the corpse query answer Valid=false (the server has no corpse for the player).
	noCorpse bool

	lines []string
}

func newDeathWorld(t *testing.T) *deathWorld {
	t.Helper()
	f := &deathWorld{
		t:            t,
		now:          time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC),
		world:        objects.NewWorld(0),
		report:       &Report{Counts: map[string]int{}},
		opts:         Options{DeathCheck: true, NavmeshDir: "", Logf: func(format string, args ...any) {}},
		graveyard:    CorpsePoint{X: 980, Y: 1990, Z: 50, MapID: 0},
		corpseAt:     CorpsePoint{X: 1000, Y: 2000, Z: 50, MapID: 0},
		reclaimDelay: 300 * time.Millisecond,
		reviveHealth: 30,
		runSpeed:     8.75,
	}
	f.opts.Logf = func(format string, args ...any) { f.lines = append(f.lines, fmt.Sprintf(format, args...)) }

	// The character's own create: round 3's committed player fixture, at (1000, 2000, 50).
	payload := loadObjectFixture(t, "create_player")
	u, err := objects.ParseUpdateObject(payload)
	if err != nil {
		t.Fatalf("the self create: %v", err)
	}
	if len(u.Blocks) == 0 {
		t.Fatal("the self create has no block")
	}
	f.self = u.Blocks[0].GUID
	f.world.Apply(u, f.now)

	s := &session{
		opts:        f.opts,
		ctx:         context.Background(),
		world:       f.world,
		report:      f.report,
		self:        f.self,
		selfSeen:    true,
		fightSender: f,
	}
	s.fight = &fightLog{started: f.now, now: f.Now, self: f.self}
	s.mover = &objects.Mover{
		GUID: f.self, Sender: f, X: 1000, Y: 2000, Z: 50, RunSpeed: 7,
		ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(f.now).Milliseconds()) },
	}
	f.mover = s.mover
	f.s = s
	return f
}

// Now is the fake clock.
func (f *deathWorld) Now() time.Time { return f.now }

// WaitUntil advances the fake clock, delivers what is due, and honours the link-death knob.
func (f *deathWorld) WaitUntil(ctx context.Context, until time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.waits++
	if f.dieAtWait > 0 && f.waits >= f.dieAtWait {
		f.s.setLinkDead(sideInstance, errors.New("fake: the server closed the socket"))
		return ErrLinkLost
	}
	if until.After(f.now) {
		f.now = until
	}
	if f.reviveAtWait > 0 && f.waits >= f.reviveAtWait {
		f.reviveAtWait = 0
		f.revivedAt = f.now
		f.revive()
	}
	if f.offer != nil && !f.offerSent && !f.now.Before(f.offerAt) {
		f.offerSent = true
		f.server(wire.SMSGResurrectRequest, buildResurrectOffer(*f.offer))
	}
	if f.pendingGossip != nil {
		payload := f.pendingGossip
		f.pendingGossip = nil
		f.server(wire.SMSGGossipMessage, payload)
	}
	return nil
}

// Send is the fake server's read side.
func (f *deathWorld) Send(core uint32, payload []byte) error {
	if err := f.s.linkDead(); err != nil {
		return err
	}
	f.sent = append(f.sent, deathSent{core: core, payload: append([]byte(nil), payload...), at: f.now})
	switch core {
	case wire.CMSGRepopRequest:
		if len(payload) != 1 {
			return fmt.Errorf("fake: CMSG_REPOP_REQUEST is %d bytes", len(payload))
		}
		if f.refuseRelease {
			return nil
		}
		if !f.noPreRessurect {
			f.server(wire.SMSGPreRessurect, buildPreRessurect(f.self))
		}
		speedFor := f.self
		if !f.speedMover.IsZero() {
			speedFor = f.speedMover
		}
		f.server(wire.SMSGMoveSetRunSpeed, buildMoveSpeed(speedFor, 1, f.runSpeed))
		f.server(wire.SMSGCorpseReclaimDelay, buildU32Payload(uint32(f.reclaimDelay/time.Millisecond)))
		f.releaseGhost()
		if f.releaseMapChange {
			// RepopAtGraveyard's far path: the transfer (Player.cpp:1501-1545), and SMSG_DEATH_RELEASE_LOC with the
			// graveyard's new map right after TeleportTo returns (Player.cpp:5255-5264). The instance socket's
			// suspend token closes the gate; the client answers it as the session's handler does.
			to := f.graveMap
			if to == 0 {
				to = 1
			}
			f.server(wire.SMSGTransferPending, pendingPayload(to, f.graveyard.X, f.graveyard.Y, f.graveyard.Z))
			f.server(wire.SMSGSuspendToken, tokenPayload(9, 1))
			if !betweenMaps(f.s) {
				f.t.Fatalf("the release's transfer did not close the movement window")
			}
			f.server(wire.SMSGDeathReleaseLoc, buildDeathRelease(to, f.graveyard.X, f.graveyard.Y, f.graveyard.Z))
			f.server(wire.SMSGNewWorld, newWorldPayload(to, f.graveyard.X, f.graveyard.Y, f.graveyard.Z, 0.5, objects.NewWorldReasonNormal, 1))
			f.server(wire.SMSGResumeToken, tokenPayload(9, 1))
			f.mover.SetPosition(f.graveyard.X, f.graveyard.Y, f.graveyard.Z, 0)
			if f.releaseReviveAtTeleport {
				// A dungeon's entrance graveyard: HandleMoveWorldportAck revives after the add to map
				// (MovementHandler.cpp:333-345).
				f.revive()
			}
			return nil
		}
		f.mover.SetPosition(f.graveyard.X, f.graveyard.Y, f.graveyard.Z, 0)
		f.server(wire.SMSGDeathReleaseLoc, buildDeathRelease(int32(f.graveyard.MapID), f.graveyard.X, f.graveyard.Y, f.graveyard.Z))
	case wire.CMSGQueryCorpseLocation:
		if f.noCorpse {
			f.server(wire.SMSGCorpseLocation, buildCorpseLocationInvalid(f.self))
			return nil
		}
		if f.corpseTransport.IsZero() {
			f.server(wire.SMSGCorpseLocation, buildCorpseLocation(f.self, f.corpseAt))
		} else {
			f.server(wire.SMSGCorpseLocation, buildCorpseLocationOnTransport(f.self, f.corpseAt, f.corpseTransport))
		}
	case wire.CMSGReclaimCorpse:
		if f.refuseReclaim {
			return nil
		}
		if f.now.Before(f.releaseAt().Add(f.reclaimDelay)) {
			return nil // the server's silent early refusal (MiscHandler.cpp:486-488)
		}
		f.revive()
	case wire.CMSGResurrectResponse:
		b := wire.NewReader(payload)
		offerer := wire.ReadPackedGUID(b)
		response := b.ReadU32()
		if response == death.ResurrectAccept && offerer == f.offerOfferer() {
			f.revive()
		}
	case wire.CMSGTalkToGossip:
		menu := buildGossipMessage(f.healer, fakeGossipMenu, f.healerOptionID, 4, "Return me to life.")
		if f.delayGossip {
			f.pendingGossip = menu
		} else {
			f.server(wire.SMSGGossipMessage, menu)
		}
	case wire.CMSGGossipSelectOption:
		if f.healer.IsZero() {
			return nil
		}
	case wire.CMSGSpiritHealerActivate:
		f.revive()
	}
	return nil
}

func (f *deathWorld) offerOfferer() wire.GUID {
	if f.offer == nil {
		return wire.EmptyGUID
	}
	return f.offer.Offerer
}

// server feeds one server packet through the session's own handler, exactly as the reader goroutine would.
func (f *deathWorld) server(core uint32, payload []byte) {
	switch core {
	case wire.SMSGAuraUpdate, wire.SMSGAttackStart, wire.SMSGAttackStop, wire.SMSGCancelCombat:
		f.s.handleCombat(worldconn.Packet{Core: core, Payload: payload})
	case wire.SMSGUpdateObject:
		u, err := objects.ParseUpdateObject(payload)
		if err != nil {
			f.t.Fatalf("the fake update: %v", err)
		}
		f.s.applyUpdate(u)
		f.world.Apply(u, f.now)
	case wire.SMSGTransferPending, wire.SMSGSuspendToken, wire.SMSGResumeToken, wire.SMSGNewWorld,
		wire.SMSGTransferAborted, wire.SMSGUpdateLastInstance, wire.SMSGWorldServerInfo, wire.SMSGInstanceInfo:
		if err := f.s.handleTransfer(context.Background(), worldconn.Packet{Core: core, Payload: payload}); err != nil {
			f.t.Fatalf("handleTransfer(%s): %v", wire.ServerName(core), err)
		}
	default:
		if err := f.s.handleDeath(worldconn.Packet{Core: core, Payload: payload}); err != nil {
			f.t.Fatalf("handleDeath(%s): %v", wire.ServerName(core), err)
		}
	}
}

// releaseGhost applies the release's server-side state: health 1, the ghost aura, then the aura packet.
func (f *deathWorld) releaseGhost() {
	f.server(wire.SMSGUpdateObject, buildHealthValues(f.self, 1))
	f.server(wire.SMSGAuraUpdate, buildAuraUpdate(f.self, 2, death.GhostAura))
}

// betweenMaps is the session's transfer window, for the fake's own assertions.
func betweenMaps(s *session) bool { return s.betweenMaps() }

// releaseAt is when the release was handled (the ghost time the server's reclaim rule counts from).
func (f *deathWorld) releaseAt() time.Time {
	for _, s := range f.sent {
		if s.core == wire.CMSGRepopRequest {
			return s.at
		}
	}
	return time.Time{}
}

// revive sends what ResurrectPlayer sends: the map -1 marker, the ghost aura gone, the health restored.
func (f *deathWorld) revive() {
	f.server(wire.SMSGDeathReleaseLoc, buildDeathRelease(-1, 0, 0, 0))
	f.server(wire.SMSGAuraUpdate, buildAuraRemove(f.self, 2))
	f.server(wire.SMSGUpdateObject, buildHealthValues(f.self, f.reviveHealth))
}

// killPlayer sets the character's health to 0, as Unit::setDeathState(JUST_DIED) does.
func (f *deathWorld) killPlayer() {
	f.server(wire.SMSGUpdateObject, buildHealthValues(f.self, 0))
}

// coreSent reports how many packets of one kind the fake saw.
func (f *deathWorld) coreSent(core uint32) int {
	n := 0
	for _, s := range f.sent {
		if s.core == core {
			n++
		}
	}
	return n
}

// sentAt is when the first packet of one kind was sent.
func (f *deathWorld) sentAt(core uint32) (time.Time, bool) {
	for _, s := range f.sent {
		if s.core == core {
			return s.at, true
		}
	}
	return time.Time{}, false
}

// ---- server packet builders (the writers cited in DEATH-R1-NOTES.md) ----

// unitValuesBody is one UnitData field's values body (the same shape round 3's test helper builds: the Classic mask
// header of UpdateFields.cpp:1361-1386, then the value).
func unitValuesBody(retailBit int, write func(*wire.Buffer)) []byte {
	v := wire.NewWriter()
	v.WriteU8(0)    // no UpdateFieldFlag
	v.WriteU8(0)    // the fragment id list did not change
	v.WriteU8(0x03) // CGObject: exists and changed
	v.WriteU32(1 << objects.TypeUnit)
	classic := 0
	{
		// classicUnitBit for a retail bit <= 71 is the bit itself (UpdateFields.cpp:1322-1335).
		c := retailBit
		if c >= 140 {
			c++
		}
		if c >= 132 {
			c += 3
		}
		classic = c
	}
	var blocks [8]uint32
	blocks[classic/32] = 1 << uint(classic%32)
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
	v.WriteBit(false) // the 70291 full/partial list flag
	v.FlushBits()
	write(v)
	return v.Bytes()
}

// buildHealthValues is SMSG_UPDATE_OBJECT with the unit's health changed (retail bit 38).
func buildHealthValues(guid wire.GUID, health int64) []byte {
	body := unitValuesBody(38, func(v *wire.Buffer) { v.WriteU64(uint64(health)) })
	block := wire.NewWriter()
	block.WriteU8(0) // UPDATETYPE_VALUES
	wire.WritePackedGUID(block, guid)
	block.WriteU32(uint32(len(body)))
	block.WriteBytes(body)
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(len(block.Bytes())))
	out.WriteBytes(block.Bytes())
	return out.Bytes()
}

// buildAuraUpdate is SMSG_AURA_UPDATE with one aura in a slot (AuraUpdate::Write, SpellPackets.cpp:154-164, with
// AuraDataInfo at :94-140; no optional block is present).
func buildAuraUpdate(unit wire.GUID, slot uint16, spellID int32) []byte {
	b := wire.NewWriter()
	b.WriteBit(false) // not UpdateAll
	b.WriteBits(1, 9)
	b.FlushBits()
	wire.WritePackedGUID(b, unit)
	b.WriteU16(slot)
	b.WriteBit(true)
	b.FlushBits()
	wire.WritePackedGUID(b, wire.EmptyGUID) // CastID
	b.WriteI32(spellID)
	b.WriteI32(0) // visual
	b.WriteI32(0) // script visual
	b.WriteU16(0x11)
	b.WriteU32(0)
	b.WriteU16(0)
	b.WriteU8(0)
	b.WriteI32(0)
	b.WriteFloat32(0)
	b.WriteFloat32(0)
	b.WriteFloat32(0)
	for range 5 {
		b.WriteBit(false) // cast unit, cast item, duration, remaining, time mod
	}
	b.WriteBits(0, 6) // points
	b.WriteBits(0, 6) // estimated points
	b.WriteBit(false) // content tuning
	b.FlushBits()
	return b.Bytes()
}

// buildAuraRemove is SMSG_AURA_UPDATE with a slot and no data (SpellAuras.cpp:232-240).
func buildAuraRemove(unit wire.GUID, slot uint16) []byte {
	b := wire.NewWriter()
	b.WriteBit(false)
	b.WriteBits(1, 9)
	b.FlushBits()
	wire.WritePackedGUID(b, unit)
	b.WriteU16(slot)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// buildPreRessurect is SMSG_PRE_RESSURECT (MiscPackets.cpp:292-297).
func buildPreRessurect(g wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, g)
	return b.Bytes()
}

// buildDeathRelease is SMSG_DEATH_RELEASE_LOC (MiscPackets.cpp:284-290).
func buildDeathRelease(mapID int32, x, y, z float32) []byte {
	b := wire.NewWriter()
	b.WriteI32(mapID)
	b.WriteFloat32(x)
	b.WriteFloat32(y)
	b.WriteFloat32(z)
	return b.Bytes()
}

// buildMoveSpeed is SMSG_MOVE_SET_RUN_SPEED (MovementPackets.cpp:697-704).
func buildMoveSpeed(g wire.GUID, sequence uint32, speed float32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, g)
	b.WriteU32(sequence)
	b.WriteFloat32(speed)
	return b.Bytes()
}

// buildCorpseLocation is SMSG_CORPSE_LOCATION (QueryPackets.cpp:393-405).
func buildCorpseLocation(player wire.GUID, at CorpsePoint) []byte {
	b := wire.NewWriter()
	b.WriteBit(true)
	b.FlushBits()
	wire.WritePackedGUID(b, player)
	b.WriteI32(at.MapID)
	b.WriteI32(at.MapID)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteFloat32(at.X)
	b.WriteFloat32(at.Y)
	b.WriteFloat32(at.Z)
	return b.Bytes()
}

// buildCorpseLocationOnTransport is SMSG_CORPSE_LOCATION whose transport is engaged (QueryPackets.cpp:393-405): the
// position is the transport's offset, which is what the server's writer pairs with the GUID.
func buildCorpseLocationOnTransport(player wire.GUID, at CorpsePoint, transport wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteBit(true)
	b.FlushBits()
	wire.WritePackedGUID(b, player)
	b.WriteI32(at.MapID)
	b.WriteI32(at.MapID)
	wire.WritePackedGUID(b, transport)
	b.WriteFloat32(at.X)
	b.WriteFloat32(at.Y)
	b.WriteFloat32(at.Z)
	return b.Bytes()
}

// buildCorpseTransportAnswer is SMSG_CORPSE_TRANSPORT_QUERY (QueryPackets.cpp:413-420): the player, the transport's
// world position and its facing.
func buildCorpseTransportAnswer(player wire.GUID, x, y, z, facing float32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, player)
	b.WriteFloat32(x)
	b.WriteFloat32(y)
	b.WriteFloat32(z)
	b.WriteFloat32(facing)
	return b.Bytes()
}

// buildResurrectOffer is SMSG_RESURRECT_REQUEST (SpellPackets.cpp:997-1011).
func buildResurrectOffer(o death.ResurrectOffer) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, o.Offerer)
	b.WriteU32(0)
	b.WriteU32(0)
	b.WriteI32(o.SpellID)
	b.WriteBits(uint64(len(o.Name)), 11)
	b.WriteBit(o.UseTimer)
	b.WriteBit(o.Sickness)
	b.FlushBits()
	b.WriteString(o.Name)
	return b.Bytes()
}

// buildGossipMessage is SMSG_GOSSIP_MESSAGE (NPCPackets.cpp:109-135 with the option writer at :42-72).
func buildGossipMessage(unit wire.GUID, menu int32, optionID int32, npc uint32, text string) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	b.WriteI32(menu)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteU32(1) // one option
	b.WriteU32(0) // no quest text
	b.WriteI32(optionID)
	b.WriteU32(npc)
	b.WriteU8(0)  // option flags
	b.WriteU64(0) // cost
	b.WriteU32(0) // language
	b.WriteU32(0) // no treasure
	b.WriteI32(0) // flags
	b.WriteI32(0) // order
	b.WriteBits(uint64(len(text)), 12)
	b.WriteBits(0, 12) // confirm
	b.WriteBits(0, 2)  // status
	b.WriteBit(false)  // no spell
	b.WriteBit(false)  // no icon
	b.WriteBits(1, 8)  // empty SizedCString: length 1, no bytes
	b.FlushBits()
	b.WriteString(text)
	b.WriteBit(false) // no random text
	b.WriteBit(false) // no broadcast text
	b.FlushBits()
	return b.Bytes()
}

// buildU32Payload is a one-field server packet.
func buildU32Payload(v uint32) []byte {
	var p [4]byte
	binary.LittleEndian.PutUint32(p[:], v)
	return p[:]
}

// creatureAt puts round 3's committed creature at (x, y, 50) and gives it UNIT_NPC_FLAG_SPIRIT_HEALER through a
// real UnitData values update (retail bit 7), so the model holds a healer the flow can find.
func (f *deathWorld) creatureAt(x, y, z float32) wire.GUID {
	f.t.Helper()
	payload := patchFloats(f.t, loadObjectFixture(f.t, "create_creature"), 1000, x)
	payload = patchFloats(f.t, payload, 2000, y)
	payload = patchFloats(f.t, payload, 50, z)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		f.t.Fatalf("the creature create: %v", err)
	}
	guid := u.Blocks[0].GUID
	f.world.Apply(u, f.now)
	f.server(wire.SMSGUpdateObject, buildNpcFlagsValues(guid, death.NPCFlagSpiritHealer))
	return guid
}

// buildNpcFlagsValues is a UnitData values update for the NPC flags (retail bit 7).
func buildNpcFlagsValues(guid wire.GUID, flags uint32) []byte {
	body := unitValuesBody(7, func(v *wire.Buffer) { v.WriteU32(flags) })
	block := wire.NewWriter()
	block.WriteU8(0)
	wire.WritePackedGUID(block, guid)
	block.WriteU32(uint32(len(body)))
	block.WriteBytes(body)
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(len(block.Bytes())))
	out.WriteBytes(block.Bytes())
	return out.Bytes()
}

// ---- the tests ----

// killAndRelease kills the character and runs the release, asserting the answered state along the way.
func (f *deathWorld) killAndRelease(ctx context.Context) {
	f.t.Helper()
	f.killPlayer()
	f.s.observeSelf(f.now)
	if got := f.s.deathState().phase; got != DeathCorpse {
		f.t.Fatalf("the phase after the death is %s", got)
	}
	if err := f.s.Release(ctx); err != nil {
		f.t.Fatalf("Release: %v\n%s", err, strings.Join(f.lines, "\n"))
	}
	if got := f.s.deathState().phase; got != DeathGhost {
		f.t.Fatalf("the phase after the release is %s", got)
	}
}

// TestDeathFlowReleaseRunReclaim is the whole player flow against the fake: die, release to a same-map graveyard,
// ask for the corpse, run back, wait out the delay, reclaim and come back alive.
func TestDeathFlowReleaseRunReclaim(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.killAndRelease(ctx)
	d := f.s.deathState()
	if !d.graveSeen || d.grave.MapID != 0 {
		t.Fatalf("the graveyard was not recorded: %+v", d.grave)
	}
	if d.reclaimDelay != f.reclaimDelay {
		t.Fatalf("the announced delay is %s, want %s", d.reclaimDelay, f.reclaimDelay)
	}
	// The mover is at the graveyard, the corpse where the character died.
	if got := (CorpsePoint{X: f.s.MoverX(), Y: f.s.MoverY(), Z: f.s.MoverZ()}); got != f.graveyard {
		t.Fatalf("the mover is at %+v, want the graveyard %+v", got, f.graveyard)
	}
	if at, err := f.s.CorpseLocation(ctx); err != nil || at != f.corpseAt {
		t.Fatalf("CorpseLocation = %+v, %v; want the death point %+v", at, err, f.corpseAt)
	}
	if err := f.s.RunToCorpse(ctx); err != nil {
		t.Fatalf("RunToCorpse: %v\n%s", err, strings.Join(f.lines, "\n"))
	}
	if got := objects.Distance(f.s.MoverX(), f.s.MoverY(), f.s.MoverZ(), f.corpseAt.X, f.corpseAt.Y, f.corpseAt.Z); got > objects.StopDistance {
		t.Fatalf("the run stopped %.2f yd short of the corpse", got)
	}
	if err := f.s.ReclaimCorpse(ctx); err != nil {
		t.Fatalf("ReclaimCorpse: %v\n%s", err, strings.Join(f.lines, "\n"))
	}
	if got := d.phase; got != DeathAlive {
		t.Fatalf("the phase after the reclaim is %s", got)
	}
	if d.revivedBy != "corpse" {
		t.Fatalf("the character came back by %q", d.revivedBy)
	}
	// The packets: one release, one corpse query, one reclaim, in that order.
	for _, core := range []uint32{wire.CMSGRepopRequest, wire.CMSGQueryCorpseLocation, wire.CMSGReclaimCorpse} {
		if f.coreSent(core) != 1 {
			t.Fatalf("%s was sent %d time(s): %v", wire.ClientName(core), f.coreSent(core), f.sent)
		}
	}
	// The run used the ghost's run speed, not the living one.
	if f.s.mover.RunSpeed != 8.75 {
		t.Fatalf("the mover ran at %.2f yd/s, want the ghost's 8.75", f.s.mover.RunSpeed)
	}
}

// TestReclaimWaitsForTheAnnouncedDelay: no reclaim packet goes out before the server's delay is over, however often
// the action is asked.
func TestReclaimWaitsForTheAnnouncedDelay(t *testing.T) {
	f := newDeathWorld(t)
	f.reclaimDelay = 30 * time.Second
	ctx := context.Background()
	f.killAndRelease(ctx)
	if err := f.s.RunToCorpse(ctx); err != nil {
		t.Fatalf("RunToCorpse: %v", err)
	}
	if err := f.s.ReclaimCorpse(ctx); err != nil {
		t.Fatalf("ReclaimCorpse: %v\n%s", err, strings.Join(f.lines, "\n"))
	}
	at, ok := f.sentAt(wire.CMSGReclaimCorpse)
	if !ok {
		t.Fatal("no reclaim packet")
	}
	if want := f.releaseAt().Add(30 * time.Second); at.Before(want) {
		t.Fatalf("the reclaim went out at %s, before the delay's end %s", at.Sub(f.releaseAt()), want.Sub(f.releaseAt()))
	}
}

// TestReclaimRefusedIsNamed: a server that answers a reclaim with nothing is a named refusal, not a hang.
func TestReclaimRefusedIsNamed(t *testing.T) {
	f := newDeathWorld(t)
	f.refuseReclaim = true
	ctx := context.Background()
	f.killAndRelease(ctx)
	if err := f.s.RunToCorpse(ctx); err != nil {
		t.Fatalf("RunToCorpse: %v", err)
	}
	err := f.s.ReclaimCorpse(ctx)
	if !errors.Is(err, ErrReclaimRefused) {
		t.Fatalf("ReclaimCorpse = %v, want ErrReclaimRefused", err)
	}
	if f.s.deathState().phase == DeathAlive {
		t.Fatal("the character is alive after a refused reclaim")
	}
}

// TestReleaseFollowsAGraveyardTransfer: a release whose graveyard is on another map follows the transfer (the
// real pending, suspend, new world and resume) and lands as a ghost there. The map change clears the world model but
// not the flow's state: the corpse stays on the map it fell on, the announced reclaim delay and the ghost phase
// survive, and the mover keeps the ghost's run speed.
func TestReleaseFollowsAGraveyardTransfer(t *testing.T) {
	f := newDeathWorld(t)
	f.releaseMapChange = true
	f.graveMap = 1
	f.graveyard = CorpsePoint{X: 980, Y: 1990, Z: 50, MapID: 1}
	ctx := context.Background()
	f.killPlayer()
	f.s.observeSelf(f.now)
	if err := f.s.Release(ctx); err != nil {
		t.Fatalf("Release: %v\n%s", err, strings.Join(f.lines, "\n"))
	}
	d := f.s.deathState()
	if d.phase != DeathGhost {
		t.Fatalf("the phase is %s after the far release", d.phase)
	}
	if !f.s.mapLanded() {
		t.Fatal("the release returned between maps")
	}
	if got := f.s.world.MapID(); got != 1 {
		t.Fatalf("the character is on map %d, want the graveyard's map 1", got)
	}
	if d.grave.MapID != 1 {
		t.Fatalf("the graveyard is recorded on map %d", d.grave.MapID)
	}
	if !d.hasCorpse || d.corpse.MapID != 0 {
		t.Fatalf("the corpse did not survive the map change: %+v", d.corpse)
	}
	if !d.haveReclaim || d.reclaimDelay != f.reclaimDelay {
		t.Fatalf("the reclaim delay did not survive: have %v, %s", d.haveReclaim, d.reclaimDelay)
	}
	if f.s.mover.RunSpeed != 8.75 {
		t.Fatalf("the ghost's run speed is %.2f after the map change, want 8.75", f.s.mover.RunSpeed)
	}
	if f.s.report.MapChanges != 1 {
		t.Fatalf("%d map change(s), want the release's one", f.s.report.MapChanges)
	}
	// The corpse is on the old map: the run names it instead of walking to a point on this map.
	err := f.s.RunToCorpse(ctx)
	if !errors.Is(err, ErrCorpseOnAnotherMap) {
		t.Fatalf("RunToCorpse = %v, want ErrCorpseOnAnotherMap", err)
	}
	if !strings.Contains(err.Error(), "walk to its entrance") {
		t.Fatalf("the error does not name the entrance rule: %v", err)
	}
	if f.coreSent(wire.CMSGReclaimCorpse) != 0 {
		t.Fatalf("the flow sent a reclaim with the corpse on another map: %v", f.sent)
	}
}

// TestReleaseToAnInstanceEntranceRevives: a graveyard of an instance revives the character at the teleport
// (RepopAtGraveyard's shouldResurrect; HandleMoveWorldportAck resurrects after the add to map). The flow reports the
// server's revival and does not go on to a corpse run.
func TestReleaseToAnInstanceEntranceRevives(t *testing.T) {
	f := newDeathWorld(t)
	f.releaseMapChange = true
	f.releaseReviveAtTeleport = true
	f.graveMap = 0
	f.graveyard = CorpsePoint{X: 1629, Y: 1723, Z: 50, MapID: 0}
	ctx := context.Background()
	f.killPlayer()
	f.s.observeSelf(f.now)
	if err := f.s.Release(ctx); err != nil {
		t.Fatalf("Release: %v\n%s", err, strings.Join(f.lines, "\n"))
	}
	if f.s.deathState().phase != DeathAlive {
		t.Fatalf("the character is %s, want alive at the instance entrance", f.s.deathState().phase)
	}
	found := false
	for _, line := range f.lines {
		if strings.Contains(line, "revived the character") && strings.Contains(line, "teleport") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no step names the server's revival at the teleport:\n%s", strings.Join(f.lines, "\n"))
	}
}

// TestGhostKeepsItsStateAcrossAMapChange: a ghost that changes maps keeps the ghost phase, its ghost run speed and
// its water-walking flag across the mover's reset, and the corpse and reclaim state survive the model's clear. The
// mover's map-change reset keeps the status bits (MovementInfo.h:114-115) and the position is the new world's.
func TestGhostKeepsItsStateAcrossAMapChange(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.killAndRelease(ctx)
	d := f.s.deathState()
	corpse, reclaimEnd := d.corpse, d.reclaimEnd
	// A released spirit walks on water: the flag is part of the movement state the map change must keep.
	f.server(wire.SMSGMoveSetWaterWalk, buildMoveSetFlag(f.self, 4))
	if f.s.mover.Flags&objects.FlagWaterwalking == 0 {
		t.Fatalf("the water-walking flag did not apply: %#x", f.s.mover.Flags)
	}
	// A transfer while a ghost (a summon, a server teleport): the same sequence.
	f.server(wire.SMSGTransferPending, pendingPayload(1, 700, 800, 60))
	f.server(wire.SMSGSuspendToken, tokenPayload(11, 1))
	f.server(wire.SMSGNewWorld, newWorldPayload(1, 700, 800, 60, 0.25, objects.NewWorldReasonNormal, 2))
	f.server(wire.SMSGResumeToken, tokenPayload(11, 1))
	if d.phase != DeathGhost {
		t.Fatalf("the phase is %s after the map change", d.phase)
	}
	if f.s.mover.RunSpeed != 8.75 {
		t.Fatalf("the ghost's run speed is %.2f after the map change", f.s.mover.RunSpeed)
	}
	if f.s.mover.Flags&objects.FlagWaterwalking == 0 {
		t.Fatalf("the map change dropped the ghost's water walking: %#x", f.s.mover.Flags)
	}
	if f.s.mover.Flags&objects.FlagTouchingGround == 0 {
		t.Fatalf("the mover does not report the ground after the map change: %#x", f.s.mover.Flags)
	}
	if f.s.mover.Flags&(objects.FlagFalling|objects.FlagForward) != 0 {
		t.Fatalf("the mover carries a moving or falling bit after the map change: %#x", f.s.mover.Flags)
	}
	if d.corpse != corpse || !d.reclaimEnd.Equal(reclaimEnd) {
		t.Fatalf("the map change lost the death state: corpse %+v (was %+v), reclaim end %s (was %s)",
			d.corpse, corpse, d.reclaimEnd, reclaimEnd)
	}
	if got := f.s.world.MapID(); got != 1 {
		t.Fatalf("the character is on map %d, want 1", got)
	}
	if x, y, z, _ := f.s.mover.Position(); x != 700 || y != 800 || z != 60 {
		t.Fatalf("the mover is at %.2f %.2f %.2f, want the new world's position", x, y, z)
	}
}

// TestDeathDuringTheTransferWindowIsNotApplied: a map-content packet read between the suspend token and the resume
// token — a health 0 update or the ghost aura's removal — is held and dropped, never applied to the fresh model. A
// death cannot happen between the maps anyway: the server removed the character from its map before it wrote the
// token (Player.cpp:1526-1527), so nothing on it can damage the character; the test pins that the client cannot be
// corrupted by a stale copy either.
func TestDeathDuringTheTransferWindowIsNotApplied(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.killAndRelease(ctx)
	d := f.s.deathState()
	f.server(wire.SMSGTransferPending, pendingPayload(1, 700, 800, 60))
	f.server(wire.SMSGSuspendToken, tokenPayload(12, 1))
	// The old map's last words: a death update and the aura's removal, read while the gate is closed. They go
	// through the session's dispatch, which is where the instance-stream gate lives.
	if err := feed(t, f.s, sideInstance, wire.SMSGUpdateObject, buildHealthValues(f.self, 0)); err != nil {
		t.Fatalf("the stale health update: %v", err)
	}
	if err := feed(t, f.s, sideInstance, wire.SMSGAuraUpdate, buildAuraRemove(f.self, 2)); err != nil {
		t.Fatalf("the stale aura update: %v", err)
	}
	if d.phase != DeathGhost {
		t.Fatalf("a held packet moved the phase to %s", d.phase)
	}
	f.server(wire.SMSGNewWorld, newWorldPayload(1, 700, 800, 60, 0, objects.NewWorldReasonNormal, 3))
	f.server(wire.SMSGResumeToken, tokenPayload(12, 1))
	if f.s.report.StaleMapPackets != 2 {
		t.Fatalf("%d stale map packet(s) dropped, want the two held ones", f.s.report.StaleMapPackets)
	}
	if d.phase != DeathGhost {
		t.Fatalf("the dropped packets moved the phase to %s", d.phase)
	}
	if _, ok := f.s.world.Combat(f.self); ok {
		t.Fatal("the old map's unit came back with the new map's model")
	}
}

// TestOfferAccepted: a resurrection offer arrives, AcceptResurrect answers 0 and the character comes back.
func TestOfferAccepted(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.killAndRelease(ctx)
	offerer := wire.GUID{Low: 0x7777, High: 2 << 58}
	f.healer = offerer
	f.offer = &death.ResurrectOffer{Offerer: offerer, SpellID: 2008}
	f.offerAt = f.now
	// The offer arrives while the client waits for it, as it would on the socket.
	if err := f.s.waitDeath(ctx, "an offer", time.Second, ErrNoResurrectOffer, func() bool {
		return f.s.deathState().liveOffer(f.now) != nil
	}); err != nil {
		t.Fatalf("waiting for the offer: %v", err)
	}
	if err := f.s.AcceptResurrect(ctx); err != nil {
		t.Fatalf("AcceptResurrect: %v\n%s", err, strings.Join(f.lines, "\n"))
	}
	if f.s.deathState().phase != DeathAlive {
		t.Fatalf("the phase is %s after the accepted offer", f.s.deathState().phase)
	}
	if f.s.deathState().revivedBy != "offer" {
		t.Fatalf("the character came back by %q", f.s.deathState().revivedBy)
	}
	if f.coreSent(wire.CMSGResurrectResponse) != 1 {
		t.Fatalf("the answer was sent %d times", f.coreSent(wire.CMSGResurrectResponse))
	}
}

// TestOfferDeclined: the answer is 1, the offer is gone and the ghost stays a ghost.
func TestOfferDeclined(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.killAndRelease(ctx)
	offerer := wire.GUID{Low: 0x8888, High: 2 << 58}
	f.offer = &death.ResurrectOffer{Offerer: offerer, SpellID: 2008}
	f.offerAt = f.now
	if err := f.s.waitDeath(ctx, "an offer", time.Second, ErrNoResurrectOffer, func() bool {
		return f.s.deathState().liveOffer(f.now) != nil
	}); err != nil {
		t.Fatalf("waiting for the offer: %v", err)
	}
	if err := f.s.DeclineResurrect(ctx); err != nil {
		t.Fatalf("DeclineResurrect: %v", err)
	}
	if f.s.deathState().phase != DeathGhost {
		t.Fatalf("the phase is %s after the declined offer", f.s.deathState().phase)
	}
	if f.s.deathState().offerAnswered != "declined" {
		t.Fatalf("the offer's answer is %q", f.s.deathState().offerAnswered)
	}
	if err := f.s.AcceptResurrect(ctx); !errors.Is(err, ErrNoResurrectOffer) {
		t.Fatalf("a second answer: %v, want ErrNoResurrectOffer", err)
	}
	// The declined response is a 1.
	for _, s := range f.sent {
		if s.core == wire.CMSGResurrectResponse {
			b := wire.NewReader(s.payload)
			wire.ReadPackedGUID(b)
			if got := b.ReadU32(); got != death.ResurrectDecline {
				t.Fatalf("the answer's response is %d, want the decline", got)
			}
		}
	}
}

// TestSpiritHealerFlow: the minimum gossip interaction from the capture, then the activation, then the resurrection.
func TestSpiritHealerFlow(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.killAndRelease(ctx)
	f.healer = f.creatureAt(f.graveyard.X+2, f.graveyard.Y, f.graveyard.Z)
	f.healerOptionID = fakeGossipOption
	if err := f.s.SpiritHealer(ctx); err != nil {
		t.Fatalf("SpiritHealer: %v\n%s", err, strings.Join(f.lines, "\n"))
	}
	if f.s.deathState().phase != DeathAlive {
		t.Fatalf("the phase is %s after the spirit healer", f.s.deathState().phase)
	}
	if f.s.deathState().revivedBy != "spirit healer" {
		t.Fatalf("the character came back by %q", f.s.deathState().revivedBy)
	}
	for _, core := range []uint32{wire.CMSGSetSelection, wire.CMSGTalkToGossip, wire.CMSGGossipSelectOption,
		wire.CMSGSpiritHealerActivate, wire.CMSGCloseInteraction} {
		if f.coreSent(core) != 1 {
			t.Fatalf("%s was sent %d time(s)", wire.ClientName(core), f.coreSent(core))
		}
	}
}

// TestSpiritHealerWithoutOneIsNamed: a model with no spirit healer never guesses one.
func TestSpiritHealerWithoutOneIsNamed(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.killAndRelease(ctx)
	if err := f.s.SpiritHealer(ctx); !errors.Is(err, ErrNoSpiritHealer) {
		t.Fatalf("SpiritHealer = %v, want ErrNoSpiritHealer", err)
	}
}

// TestLinkDeathEndsEveryDeathWait: every wait of the flow ends at once with ErrLinkLost, and nothing is sent after.
func TestLinkDeathEndsEveryDeathWait(t *testing.T) {
	t.Run("release", func(t *testing.T) {
		f := newDeathWorld(t)
		f.refuseRelease = true
		ctx := context.Background()
		f.killPlayer()
		f.s.observeSelf(f.now)
		f.dieAtWait = f.waits + 1
		if err := f.s.Release(ctx); !errors.Is(err, ErrLinkLost) {
			t.Fatalf("Release = %v, want ErrLinkLost", err)
		}
	})
	t.Run("resurrect offer", func(t *testing.T) {
		f := newDeathWorld(t)
		ctx := context.Background()
		f.killAndRelease(ctx)
		f.dieAtWait = f.waits + 1
		err := f.s.waitDeath(ctx, "an offer", time.Minute, ErrNoResurrectOffer, func() bool { return false })
		if !errors.Is(err, ErrLinkLost) {
			t.Fatalf("the offer wait = %v, want ErrLinkLost", err)
		}
	})
	t.Run("reclaim delay", func(t *testing.T) {
		f := newDeathWorld(t)
		f.reclaimDelay = maxReclaimDelay - time.Minute
		ctx := context.Background()
		f.killAndRelease(ctx)
		if err := f.s.RunToCorpse(ctx); err != nil {
			t.Fatalf("RunToCorpse: %v", err)
		}
		f.dieAtWait = f.waits + 1
		if err := f.s.ReclaimCorpse(ctx); !errors.Is(err, ErrLinkLost) {
			t.Fatalf("ReclaimCorpse = %v, want ErrLinkLost", err)
		}
		if f.coreSent(wire.CMSGReclaimCorpse) != 0 {
			t.Fatal("a reclaim was sent on a dead link")
		}
	})
	t.Run("the run", func(t *testing.T) {
		f := newDeathWorld(t)
		ctx := context.Background()
		f.killAndRelease(ctx)
		f.corpseAt = CorpsePoint{X: 1000, Y: 2500, Z: 50, MapID: 0}
		if _, err := f.s.CorpseLocation(ctx); err != nil {
			t.Fatalf("CorpseLocation: %v", err)
		}
		f.dieAtWait = f.waits + 1
		if err := f.s.RunToCorpse(ctx); !errors.Is(err, ErrLinkLost) {
			t.Fatalf("RunToCorpse = %v, want ErrLinkLost", err)
		}
	})
	t.Run("spirit healer", func(t *testing.T) {
		f := newDeathWorld(t)
		ctx := context.Background()
		f.killAndRelease(ctx)
		f.healer = f.creatureAt(f.graveyard.X+2, f.graveyard.Y, f.graveyard.Z)
		f.healerOptionID = 1
		f.delayGossip = true
		// The menu wait is the first one this flow makes: the link dies on it.
		f.dieAtWait = f.waits + 1
		if err := f.s.SpiritHealer(ctx); !errors.Is(err, ErrLinkLost) {
			t.Fatalf("SpiritHealer = %v, want ErrLinkLost", err)
		}
	})
}

// TestObserveSelfTracksTheDeathAndTheGhost: the model's health and aura drive the phase without any death packet.
func TestObserveSelfTracksTheDeathAndTheGhost(t *testing.T) {
	f := newDeathWorld(t)
	f.killPlayer()
	f.s.observeSelf(f.now)
	d := f.s.deathState()
	if d.phase != DeathCorpse || !d.hasCorpse {
		t.Fatalf("after the health zero: %+v", d)
	}
	if d.corpse != (CorpsePoint{X: 1000, Y: 2000, Z: 50, MapID: 0}) {
		t.Fatalf("the corpse is %+v", d.corpse)
	}
	f.releaseGhost()
	f.s.observeSelf(f.now)
	if d.phase != DeathGhost {
		t.Fatalf("after the ghost aura: %s", d.phase)
	}
	f.revive()
	f.s.observeSelf(f.now)
	if d.phase != DeathAlive {
		t.Fatalf("after the resurrection: %s", d.phase)
	}
	if f.world.Resurrections() != 1 {
		t.Fatalf("%d resurrection(s) in the model", f.world.Resurrections())
	}
}

// ---- fix round 2: state and movement ----

// movementCore reports whether a client opcode is a movement packet.
func movementCore(core uint32) bool {
	switch core {
	case wire.CMSGMoveStartForward, wire.CMSGMoveStop, wire.CMSGMoveHeartbeat, wire.CMSGMoveSetFacing,
		wire.CMSGMoveSetFacingHeartbeat:
		return true
	}
	return false
}

// movementSent counts the movement packets the fake saw.
func (f *deathWorld) movementSent() int {
	n := 0
	for _, s := range f.sent {
		if movementCore(s.core) {
			n++
		}
	}
	return n
}

// lastMovement is the last movement packet the fake saw, or 0 when there was none.
func (f *deathWorld) lastMovement() (core uint32, index int) {
	for i := len(f.sent) - 1; i >= 0; i-- {
		if movementCore(f.sent[i].core) {
			return f.sent[i].core, i
		}
	}
	return 0, -1
}

// TestAnotherUnitsSpeedDoesNotCompleteTheRelease: only the character's own speed packet is the ghost's setup. A
// speed packet for another mover leaves the mover's speed and the release's ghost-speed state alone.
func TestAnotherUnitsSpeedDoesNotCompleteTheRelease(t *testing.T) {
	f := newDeathWorld(t)
	f.speedMover = wire.GUID{Low: 0x5151, High: 3 << 58}
	f.killAndRelease(context.Background())
	d := f.s.deathState()
	if d.ghostSpeed {
		t.Fatal("another unit's speed packet completed the ghost setup")
	}
	if f.s.mover.RunSpeed != 7 {
		t.Fatalf("the mover's speed changed to %.2f on another unit's packet", f.s.mover.RunSpeed)
	}
}

// TestUnusableSpeedsAreIgnored: NaN, an infinity, a zero, a negative and an absurd speed are not speeds. The mover
// keeps its own and the ghost setup is not completed by a packet the client refused.
func TestUnusableSpeedsAreIgnored(t *testing.T) {
	for _, speed := range []float32{float32(math.NaN()), float32(math.Inf(1)), 0, -1, maxMoveSpeed + 1} {
		t.Run(fmt.Sprintf("%v", speed), func(t *testing.T) {
			f := newDeathWorld(t)
			f.runSpeed = speed
			f.killAndRelease(context.Background())
			d := f.s.deathState()
			if d.ghostSpeed {
				t.Fatalf("the ghost setup accepted the speed %v", speed)
			}
			if f.s.mover.RunSpeed != 7 {
				t.Fatalf("the mover took the speed %v (now %.2f)", speed, f.s.mover.RunSpeed)
			}
			if d.ignoredNumbers != 1 {
				t.Fatalf("%d server numbers were counted ignored, want 1", d.ignoredNumbers)
			}
		})
	}
}

// TestReclaimDelayBeyondTheCapIsRefused: a delay over the cap is not waited out. The reclaim says so at once and
// sends nothing (0xFFFFFFFF ms would be 49 days).
func TestReclaimDelayBeyondTheCapIsRefused(t *testing.T) {
	f := newDeathWorld(t)
	f.reclaimDelay = time.Duration(0xFFFFFFFF) * time.Millisecond
	ctx := context.Background()
	f.killAndRelease(ctx)
	d := f.s.deathState()
	if !d.reclaimBogus || d.haveReclaim {
		t.Fatalf("the absurd delay: bogus %v, haveReclaim %v", d.reclaimBogus, d.haveReclaim)
	}
	if err := f.s.ReclaimCorpse(ctx); !errors.Is(err, ErrReclaimTooEarly) {
		t.Fatalf("ReclaimCorpse = %v, want ErrReclaimTooEarly", err)
	}
	if f.coreSent(wire.CMSGReclaimCorpse) != 0 {
		t.Fatal("a reclaim was sent for a delay the client refuses")
	}
}

// TestReviveDuringTheCorpseRunEndsTheRun: a resurrection that lands while the ghost walks back stops the walk at
// once (the stop is the last movement packet, nothing follows it), sends no reclaim and ends the run as a success.
// It runs a straight line and a navmesh path.
func TestReviveDuringTheCorpseRunEndsTheRun(t *testing.T) {
	run := func(t *testing.T, useNavmesh bool) {
		f := newDeathWorld(t)
		ctx := context.Background()
		f.killAndRelease(ctx)
		if _, err := f.s.CorpseLocation(ctx); err != nil {
			t.Fatalf("CorpseLocation: %v", err)
		}
		if useNavmesh {
			polys := []navfixture.Poly{navfixture.Square(976, 1986, 1008, 2008, 50)}
			f.s.opts.NavmeshDir = navfixture.WriteForeverDirAt(t, 0, polys, nil, 980, 1990)
		}
		f.reviveAtWait = f.waits + 1
		if err := f.s.RunToCorpse(ctx); err != nil {
			t.Fatalf("RunToCorpse: %v\n%s", err, strings.Join(f.lines, "\n"))
		}
		d := f.s.deathState()
		if d.phase != DeathAlive || d.revivedBy != "server" {
			t.Fatalf("the run ended in %s by %q, want alive by the server", d.phase, d.revivedBy)
		}
		last, at := f.lastMovement()
		if last != wire.CMSGMoveStop {
			t.Fatalf("the last movement packet is %s, not the stop", wire.ClientName(last))
		}
		for _, s := range f.sent[at+1:] {
			if movementCore(s.core) {
				t.Fatalf("%s was sent after the stop", wire.ClientName(s.core))
			}
		}
		// Nothing but that stop went out at or after the revival: the walk did not carry on to the corpse.
		if f.revivedAt.IsZero() {
			t.Fatal("the revival did not fire")
		}
		after := 0
		for _, s := range f.sent {
			if movementCore(s.core) && !s.at.Before(f.revivedAt) {
				after++
			}
		}
		if after != 1 {
			t.Fatalf("%d movement packet(s) at or after the revival, want the stop only", after)
		}
		if left := objects.Distance(f.s.MoverX(), f.s.MoverY(), f.s.MoverZ(), f.corpseAt.X, f.corpseAt.Y, f.corpseAt.Z); left <= objects.StopDistance {
			t.Fatalf("the walk went on to the corpse (%.1f yd away)", left)
		}
		// No reclaim follows: the character is alive.
		if err := f.s.ReclaimCorpse(ctx); !errors.Is(err, ErrNotGhost) {
			t.Fatalf("ReclaimCorpse after the revive = %v, want ErrNotGhost", err)
		}
		if f.coreSent(wire.CMSGReclaimCorpse) != 0 {
			t.Fatal("a reclaim was sent after the character came back")
		}
	}
	t.Run("straight line", func(t *testing.T) { run(t, false) })
	t.Run("navmesh path", func(t *testing.T) { run(t, true) })
}

// TestOfferWhileAliveIsIgnored: an offer that arrives while the character is alive is not stored, and an accept
// answers nothing without a death phase.
func TestOfferWhileAliveIsIgnored(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.server(wire.SMSGResurrectRequest, buildResurrectOffer(death.ResurrectOffer{
		Offerer: wire.GUID{Low: 0x7777, High: 2 << 58}, SpellID: 2008,
	}))
	d := f.s.deathState()
	if d.offer != nil {
		t.Fatal("an offer was stored while the character is alive")
	}
	if err := f.s.AcceptResurrect(ctx); !errors.Is(err, ErrNotDead) {
		t.Fatalf("AcceptResurrect while alive = %v, want ErrNotDead", err)
	}
	if f.coreSent(wire.CMSGResurrectResponse) != 0 {
		t.Fatal("a resurrect response was sent while the character is alive")
	}
}

// TestOfferExpiry: an offer lives on the session clock. With the server's timer it expires after the ten-second
// countdown; without one after this client's own five minutes.
func TestOfferExpiry(t *testing.T) {
	newOffer := func(t *testing.T, useTimer bool) (*deathWorld, *ResurrectOffer) {
		t.Helper()
		f := newDeathWorld(t)
		f.killAndRelease(context.Background())
		offerer := wire.GUID{Low: 0x7777, High: 2 << 58}
		f.server(wire.SMSGResurrectRequest, buildResurrectOffer(death.ResurrectOffer{
			Offerer: offerer, SpellID: 2008, UseTimer: useTimer,
		}))
		d := f.s.deathState()
		o := d.liveOffer(f.now)
		if o == nil {
			t.Fatal("the offer is not live right after it arrived")
		}
		return f, o
	}
	t.Run("the server's timer", func(t *testing.T) {
		f, o := newOffer(t, true)
		if got := o.Expires.Sub(o.Received); got != offerTimerLife {
			t.Fatalf("the offer's life is %s, want the server's %s", got, offerTimerLife)
		}
		f.now = f.now.Add(offerTimerLife + time.Second)
		if f.s.deathState().liveOffer(f.now) != nil {
			t.Fatal("the offer outlived the server's countdown")
		}
	})
	t.Run("the client's own", func(t *testing.T) {
		f, o := newOffer(t, false)
		if got := o.Expires.Sub(o.Received); got != offerLifetime {
			t.Fatalf("the offer's life is %s, want this client's %s", got, offerLifetime)
		}
		f.now = f.now.Add(offerLifetime - time.Second)
		if f.s.deathState().liveOffer(f.now) == nil {
			t.Fatal("the offer expired before this client's own lifetime")
		}
	})
}

// TestCorpsePositionNotUsableIsNotBelieved: a NaN corpse answer is no corpse. The character logged in as a ghost has
// no death point either, so the reclaim cannot be sent at all.
func TestCorpsePositionNotUsableIsNotBelieved(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	// A login that is already a ghost: the aura and health 1, no death seen, so no corpse point.
	f.releaseGhost()
	f.s.observeSelf(f.now)
	d := f.s.deathState()
	if d.phase != DeathGhost || d.hasCorpse {
		t.Fatalf("the login ghost: phase %s, corpse known %v", d.phase, d.hasCorpse)
	}
	f.corpseAt = CorpsePoint{X: float32(math.NaN()), Y: 2000, Z: 50, MapID: 0}
	if _, err := f.s.CorpseLocation(ctx); !errors.Is(err, ErrCorpseUnknown) {
		t.Fatalf("CorpseLocation = %v, want ErrCorpseUnknown", err)
	}
	if d.hasCorpse || d.corpseValid {
		t.Fatal("the NaN corpse position was believed")
	}
	if err := f.s.ReclaimCorpse(ctx); !errors.Is(err, ErrCorpseUnknown) {
		t.Fatalf("ReclaimCorpse = %v, want ErrCorpseUnknown", err)
	}
	if f.coreSent(wire.CMSGReclaimCorpse) != 0 {
		t.Fatal("a reclaim was sent with no usable corpse position")
	}
	if d.ignoredNumbers != 1 {
		t.Fatalf("%d server numbers were counted ignored, want 1", d.ignoredNumbers)
	}
}

// TestGraveyardPositionNotUsableIsNotBelieved: a NaN graveyard is not a graveyard: the release waits for a usable
// answer and fails rather than recording a point the run could not use.
func TestGraveyardPositionNotUsableIsNotBelieved(t *testing.T) {
	f := newDeathWorld(t)
	f.graveyard = CorpsePoint{X: float32(math.NaN()), Y: 1990, Z: 50, MapID: 0}
	ctx := context.Background()
	f.killPlayer()
	f.s.observeSelf(f.now)
	if err := f.s.Release(ctx); !errors.Is(err, ErrReleaseFailed) {
		t.Fatalf("Release = %v, want ErrReleaseFailed", err)
	}
	d := f.s.deathState()
	if d.graveSeen {
		t.Fatal("the NaN graveyard was recorded")
	}
	if d.ignoredNumbers != 1 {
		t.Fatalf("%d server numbers were counted ignored, want 1", d.ignoredNumbers)
	}
}

// TestCorpseOnATransportIsNotWalkable: SMSG_CORPSE_LOCATION with a transport GUID carries the transport's offsets,
// not a world point. The flow records the corpse as on a transport; the location query, the run and the reclaim
// refuse it by name before any movement or reclaim, and the transport-query answer does not turn it into a point.
func TestCorpseOnATransportIsNotWalkable(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.killAndRelease(ctx)
	// The answer's coordinates are the offsets on the transport.
	f.corpseAt = CorpsePoint{X: 12, Y: -3, Z: 4, MapID: 0}
	f.corpseTransport = wire.GUID{Low: 0x5151, High: 5 << 58}
	d := f.s.deathState()
	// The release had recorded the death point; the transport answer replaces it with the explicit state.
	if _, err := f.s.CorpseLocation(ctx); !errors.Is(err, ErrCorpseOnTransport) {
		t.Fatalf("CorpseLocation = %v, want ErrCorpseOnTransport", err)
	}
	if !d.corpseOnTransport || d.corpseTransport != f.corpseTransport {
		t.Fatalf("the transport corpse was not recorded: on %v, transport %v", d.corpseOnTransport, d.corpseTransport)
	}
	if d.hasCorpse || d.corpseValid {
		t.Fatal("the transport offsets were recorded as a walkable corpse")
	}
	if r := f.s.deathReport(); r.HasCorpse || !r.CorpseOnTransport {
		t.Fatalf("the report: HasCorpse %v, CorpseOnTransport %v", r.HasCorpse, r.CorpseOnTransport)
	}
	// The transport-query answer names the transport's own world position; it is recorded, not walked to.
	f.server(wire.SMSGCorpseTransportQuery, buildCorpseTransportAnswer(f.self, 500, 600, 20, 1.5))
	if !d.transportAnswer {
		t.Fatal("the transport answer was not recorded")
	}
	x, y, z, _ := f.s.mover.Position()
	before := len(f.sent)
	if err := f.s.RunToCorpse(ctx); !errors.Is(err, ErrCorpseOnTransport) {
		t.Fatalf("RunToCorpse = %v, want ErrCorpseOnTransport", err)
	}
	if err := f.s.ReclaimCorpse(ctx); !errors.Is(err, ErrCorpseOnTransport) {
		t.Fatalf("ReclaimCorpse = %v, want ErrCorpseOnTransport", err)
	}
	if len(f.sent) != before {
		t.Fatalf("the refused actions sent %d packet(s), want none", len(f.sent)-before)
	}
	if nx, ny, nz, _ := f.s.mover.Position(); nx != x || ny != y || nz != z {
		t.Fatalf("the mover moved from %.1f %.1f %.1f to %.1f %.1f %.1f", x, y, z, nx, ny, nz)
	}
	if f.coreSent(wire.CMSGReclaimCorpse) != 0 {
		t.Fatal("a reclaim was sent for a corpse on a transport")
	}
}

// TestASecondDeathRecordsItsOwnCorpse: a revival forgets the corpse, the reclaim deadline and the offer, so a
// second death records the point it actually died at.
func TestASecondDeathRecordsItsOwnCorpse(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.killAndRelease(ctx)
	if err := f.s.RunToCorpse(ctx); err != nil {
		t.Fatalf("RunToCorpse: %v", err)
	}
	if err := f.s.ReclaimCorpse(ctx); err != nil {
		t.Fatalf("ReclaimCorpse: %v", err)
	}
	d := f.s.deathState()
	if d.phase != DeathAlive {
		t.Fatalf("the phase after the reclaim is %s", d.phase)
	}
	if d.hasCorpse || d.haveReclaim || d.offer != nil {
		t.Fatalf("the death survived the revival: corpse %v, deadline %v, offer %v", d.hasCorpse, d.haveReclaim, d.offer)
	}
	f.s.mover.SetPosition(1010, 2020, 50, 0)
	f.killPlayer()
	f.s.observeSelf(f.now)
	if got := d.corpse; got != (CorpsePoint{X: 1010, Y: 2020, Z: 50, MapID: 0}) {
		t.Fatalf("the second death's corpse is %+v, want the new point", got)
	}
}

// TestLeaveCombatWithoutANavmeshDoesNotMove: a straight line at the current Z is a walk at an invented height, so
// without a navmesh the character waits the fight out where it stands.
func TestLeaveCombatWithoutANavmeshDoesNotMove(t *testing.T) {
	f := newDeathWorld(t)
	f.s.mover.SetPosition(1000, 2000, 50, 0)
	attacker := wire.GUID{Low: 0x6161, High: 3 << 58}
	f.server(wire.SMSGAttackStart, buildAttackStart(attacker, f.self))
	if cs, ok := f.s.world.Combat(f.self); !ok || !cs.InCombat {
		t.Fatalf("the fake's combat state: %+v", cs)
	}
	f.s.leaveCombat(context.Background())
	if n := f.movementSent(); n != 0 {
		t.Fatalf("%d movement packet(s) were sent without a navmesh", n)
	}
	if !strings.Contains(strings.Join(f.lines, "\n"), "no navmesh") {
		t.Fatalf("the flow does not say why it stayed:\n%s", strings.Join(f.lines, "\n"))
	}
	if !strings.Contains(strings.Join(f.lines, "\n"), "still in combat after the wait") {
		t.Fatalf("the flow did not report the combat that did not end:\n%s", strings.Join(f.lines, "\n"))
	}
}

// TestLeaveCombatWithANavmeshPlansAnExit: with a navmesh the walk out is a route on the mesh; with one that has no
// route from the character the flow does not move.
func TestLeaveCombatWithANavmeshPlansAnExit(t *testing.T) {
	run := func(t *testing.T, polys []navfixture.Poly, atX, atY float32) *deathWorld {
		t.Helper()
		f := newDeathWorld(t)
		f.s.opts.NavmeshDir = navfixture.WriteForeverDirAt(t, 0, polys, nil, atX, atY)
		f.s.mover.SetPosition(1000, 2000, 50, 0)
		f.server(wire.SMSGAttackStart, buildAttackStart(wire.GUID{Low: 0x6161, High: 3 << 58}, f.self))
		f.s.leaveCombat(context.Background())
		return f
	}
	t.Run("a route out", func(t *testing.T) {
		f := run(t, []navfixture.Poly{navfixture.Square(976, 1986, 1040, 2040, 50)}, 1000, 2000)
		if n := f.movementSent(); n == 0 {
			t.Fatalf("no walk was planned with a navmesh:\n%s", strings.Join(f.lines, "\n"))
		}
		if last, _ := f.lastMovement(); last != wire.CMSGMoveStop {
			t.Fatalf("the walk out did not stop: last movement %s", wire.ClientName(last))
		}
	})
	t.Run("no route", func(t *testing.T) {
		f := run(t, []navfixture.Poly{navfixture.Square(2000, 3000, 2030, 3030, 50)}, 1000, 2000)
		if n := f.movementSent(); n != 0 {
			t.Fatalf("%d movement packet(s) were sent with no route", n)
		}
		if !strings.Contains(strings.Join(f.lines, "\n"), "no walkable way out of combat") {
			t.Fatalf("the flow does not say why it stayed:\n%s", strings.Join(f.lines, "\n"))
		}
	})
}

// TestReleaseIsAnsweredByTheGhostStateItself: the release is accepted when the ghost state is visible on the
// character even without SMSG_PRE_RESSURECT (health 1 and aura 8326, which observeSelf turns into the ghost phase).
func TestReleaseIsAnsweredByTheGhostStateItself(t *testing.T) {
	f := newDeathWorld(t)
	f.noPreRessurect = true
	ctx := context.Background()
	f.killPlayer()
	f.s.observeSelf(f.now)
	if got := f.s.deathState().phase; got != DeathCorpse {
		t.Fatalf("the phase after the death is %s", got)
	}
	if err := f.s.Release(ctx); err != nil {
		t.Fatalf("Release without SMSG_PRE_RESSURECT: %v\n%s", err, strings.Join(f.lines, "\n"))
	}
	d := f.s.deathState()
	if d.phase != DeathGhost {
		t.Fatalf("the phase after the release is %s", d.phase)
	}
	if !d.graveSeen {
		t.Fatal("the graveyard was not recorded")
	}
}

// TestLoginGhostHasNoCorpseUntilTheServerAnswers: a character that logs in already a ghost stands at its graveyard,
// not on its corpse. The corpse stays unknown, and with no usable answer the command can only report it.
func TestLoginGhostHasNoCorpseUntilTheServerAnswers(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.releaseGhost()
	f.s.observeSelf(f.now)
	d := f.s.deathState()
	if d.phase != DeathGhost || d.hasCorpse {
		t.Fatalf("the login ghost: phase %s, corpse known %v", d.phase, d.hasCorpse)
	}
	if err := f.s.Release(ctx); err != nil {
		t.Fatalf("Release on an unreleased ghost: %v", err)
	}
	// The server has no corpse for it (the no-corpse answer): nothing can be run back to.
	f.noCorpse = true
	if _, err := f.s.CorpseLocation(ctx); !errors.Is(err, ErrCorpseUnknown) {
		t.Fatalf("CorpseLocation = %v, want ErrCorpseUnknown", err)
	}
	if err := f.s.RunToCorpse(ctx); !errors.Is(err, ErrCorpseUnknown) {
		t.Fatalf("RunToCorpse = %v, want ErrCorpseUnknown", err)
	}
	if err := f.s.ReclaimCorpse(ctx); !errors.Is(err, ErrCorpseUnknown) {
		t.Fatalf("ReclaimCorpse = %v, want ErrCorpseUnknown", err)
	}
	if f.coreSent(wire.CMSGReclaimCorpse) != 0 {
		t.Fatal("a reclaim was sent with no known corpse")
	}
}

// buildCorpseLocationInvalid is SMSG_CORPSE_LOCATION with Valid=false (QueryPackets.cpp:393-405): the server has no
// corpse for the player, and every other field is still written.
func buildCorpseLocationInvalid(player wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteBit(false)
	b.FlushBits()
	wire.WritePackedGUID(b, player)
	b.WriteI32(0)
	b.WriteI32(0)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteFloat32(0)
	b.WriteFloat32(0)
	b.WriteFloat32(0)
	return b.Bytes()
}

// TestAcceptResurrectFromOnlyAnswersThatUnit: --accept-resurrect-from is the stricter form of --accept-resurrect.
// The default answers any offer; the name rule answers only the unit it names (by the model's name, or by the name
// a creature caster's offer packet carries) and refuses anything else without sending a response.
func TestAcceptResurrectFromOnlyAnswersThatUnit(t *testing.T) {
	offerer := wire.GUID{Low: 0x7777, High: 2 << 58}
	setup := func(t *testing.T, from string) (*deathWorld, context.Context) {
		t.Helper()
		f := newDeathWorld(t)
		f.s.opts.AcceptResurrectFrom = from
		ctx := context.Background()
		f.killAndRelease(ctx)
		f.offer = &death.ResurrectOffer{Offerer: offerer, SpellID: 2008}
		f.offerAt = f.now
		if err := f.s.waitDeath(ctx, "an offer", time.Second, ErrNoResurrectOffer, func() bool {
			return f.s.deathState().liveOffer(f.now) != nil
		}); err != nil {
			t.Fatalf("waiting for the offer: %v", err)
		}
		return f, ctx
	}
	t.Run("the named unit", func(t *testing.T) {
		f, ctx := setup(t, "Helper")
		f.s.world.SetName(offerer, "Helper")
		if err := f.s.AcceptResurrect(ctx); err != nil {
			t.Fatalf("AcceptResurrect: %v", err)
		}
		if got := f.s.deathState().phase; got != DeathAlive {
			t.Fatalf("the phase after the accepted offer is %s", got)
		}
		if f.coreSent(wire.CMSGResurrectResponse) != 1 {
			t.Fatal("the answer was not sent once")
		}
	})
	t.Run("another unit", func(t *testing.T) {
		f, ctx := setup(t, "Helper")
		f.s.world.SetName(offerer, "Imposter")
		if err := f.s.AcceptResurrect(ctx); !errors.Is(err, ErrResurrectOfferer) {
			t.Fatalf("AcceptResurrect = %v, want ErrResurrectOfferer", err)
		}
		if f.coreSent(wire.CMSGResurrectResponse) != 0 {
			t.Fatal("an answer was sent to the wrong unit")
		}
		if got := f.s.deathState().phase; got != DeathResurrecting {
			t.Fatalf("the offer was consumed: phase %s", got)
		}
	})
	t.Run("an unnamed unit", func(t *testing.T) {
		f, ctx := setup(t, "Helper")
		if err := f.s.AcceptResurrect(ctx); !errors.Is(err, ErrResurrectOfferer) {
			t.Fatalf("AcceptResurrect = %v, want ErrResurrectOfferer", err)
		}
	})
	t.Run("a creature that names itself", func(t *testing.T) {
		f, ctx := setup(t, "Helper")
		// The offer is replaced by a creature's, which names itself in the packet and needs no name answer.
		f.s.world.SetName(offerer, "Imposter")
		healer := wire.GUID{Low: 0x8888, High: 3 << 58}
		f.offer = &death.ResurrectOffer{Offerer: healer, SpellID: 2008, Name: "Helper", Sickness: true}
		f.offerSent = false
		f.offerAt = f.now
		f.server(wire.SMSGResurrectRequest, buildResurrectOffer(*f.offer))
		if err := f.s.AcceptResurrect(ctx); err != nil {
			t.Fatalf("AcceptResurrect: %v", err)
		}
	})
}

// TestCorpseRunOfARevivedCharacterSucceeds: a resurrection that arrives before the run starts leaves nothing to run
// to, so the run is a success too (the command then ends successfully and sends no reclaim).
func TestCorpseRunOfARevivedCharacterSucceeds(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	f.killAndRelease(ctx)
	if _, err := f.s.CorpseLocation(ctx); err != nil {
		t.Fatalf("CorpseLocation: %v", err)
	}
	f.revive()
	f.s.observeSelf(f.now)
	if err := f.s.RunToCorpse(ctx); err != nil {
		t.Fatalf("RunToCorpse after the revival: %v", err)
	}
	if got := f.s.deathState().phase; got != DeathAlive {
		t.Fatalf("the phase is %s", got)
	}
	if n := f.movementSent(); n != 0 {
		t.Fatalf("%d movement packet(s) were sent to a corpse that is gone", n)
	}
	if f.coreSent(wire.CMSGReclaimCorpse) != 0 {
		t.Fatal("a reclaim was sent after the revival")
	}
}
