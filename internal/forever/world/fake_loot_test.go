package world

// The loot and object-use scenarios of the fake world server, and the client side's proof over real sockets.
//
// Every packet the fake sends is laid out from the core's writer, field by field (internal/forever/loot cites the
// reader this mirrors). The scenario checks what the client sends: the loot unit and object GUIDs, the item list ids,
// the vote and the release, so a wrong GUID or slot fails here rather than in a live run.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The wire numbers of this round's opcodes for a 70707 client on a 70707 realm, computed by the same table the
// session uses.
var (
	fakeWireTable = wire.NewOpcodeTable(wire.Build70707, 70707)

	fakeCMSGLootUnit      = mustClientWire(wire.CMSGLootUnit)
	fakeCMSGLootMoney     = mustClientWire(wire.CMSGLootMoney)
	fakeCMSGLootItem      = mustClientWire(wire.CMSGLootItem)
	fakeCMSGLootRelease   = mustClientWire(wire.CMSGLootRelease)
	fakeCMSGLootRoll      = mustClientWire(wire.CMSGLootRoll)
	fakeCMSGGameObjUse    = mustClientWire(wire.CMSGGameObjUse)
	fakeCMSGGameObjReport = mustClientWire(wire.CMSGGameObjReportUse)
	fakeCMSGQueryCreature = mustClientWire(wire.CMSGQueryCreature)
	fakeCMSGSetSelection  = mustClientWire(wire.CMSGSetSelection)
	fakeCMSGAttackSwing   = mustClientWire(wire.CMSGAttackSwing)
	fakeCMSGAttackStop    = mustClientWire(wire.CMSGAttackStop)

	fakeSMSGLootResponse     = mustServerWire(wire.SMSGLootResponse)
	fakeSMSGLootRemoved      = mustServerWire(wire.SMSGLootRemoved)
	fakeSMSGCoinRemoved      = mustServerWire(wire.SMSGCoinRemoved)
	fakeSMSGLootRelease      = mustServerWire(wire.SMSGLootRelease)
	fakeSMSGLootMoneyNotify  = mustServerWire(wire.SMSGLootMoneyNotify)
	fakeSMSGStartLootRoll    = mustServerWire(wire.SMSGStartLootRoll)
	fakeSMSGLootRoll         = mustServerWire(wire.SMSGLootRoll)
	fakeSMSGLootRollWon      = mustServerWire(wire.SMSGLootRollWon)
	fakeSMSGLootAllPassed    = mustServerWire(wire.SMSGLootAllPassed)
	fakeSMSGQueryCreatureRes = mustServerWire(wire.SMSGCreatureQueryResponse)
	fakeSMSGAttackStart      = mustServerWire(wire.SMSGAttackStart)
)

func mustClientWire(core uint32) uint32 {
	w, ok := fakeWireTable.WireForClient(core)
	if !ok {
		panic("no wire number for " + wire.ClientName(core))
	}
	return w
}

func mustServerWire(core uint32) uint32 { return fakeWireTable.TranslateServerCore(core) }

// fakeLootTarget is what the scenario puts in the world and expects the client to act on.
type fakeLootTarget struct {
	guid   wire.GUID
	packed []byte
	entry  int32
	window bool // a game object (a chest) instead of a creature
	goType int8
	state  int8
	dead   bool
}

// fakeExtraObject is another game object of the same entry the scenario puts in the world: object-use must not pick
// between it and the target unless --at names one of them.
type fakeExtraObject struct {
	guid          wire.GUID
	packed        []byte
	entry         int32
	goType, state int8
	x, y, z       float32
}

// fakeItemDef is one LootItemData the fake sends.
type fakeItemDef struct {
	listID uint8
	itemID int32
	count  uint32
	uiType uint8
	trade  bool
}

func fakePacked(g wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, g)
	return b.Bytes()
}

func parseFakePacked(t *testing.T, packed []byte) wire.GUID {
	t.Helper()
	return wire.ReadPackedGUID(wire.NewReader(packed))
}

func fakeItemBytes(it fakeItemDef) []byte {
	b := wire.NewWriter()
	b.WriteBits(0, 2) // Type: Item
	b.WriteBits(uint64(it.uiType), 3)
	b.WriteBit(it.trade)
	b.FlushBits()
	b.WriteU32(it.count)
	b.WriteU8(0) // LootItemType
	b.WriteU8(it.listID)
	b.WriteI32(it.itemID)
	b.WriteBits(0, 7) // no modifiers
	b.FlushBits()
	b.WriteBit(false) // no item bonus
	b.FlushBits()
	return b.Bytes()
}

func fakeLootWindow(owner, lootGUID wire.GUID, method uint8, coins uint32, items []fakeItemDef) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, owner)
	wire.WritePackedGUID(b, lootGUID)
	b.WriteU8(loot.ErrorNoLoot) // the writer's default, sent even on a window
	b.WriteU8(1)                // AcquireReason: LOOT_CORPSE
	b.WriteU8(method)
	b.WriteU8(2) // threshold
	b.WriteU32(coins)
	b.WriteU32(uint32(len(items)))
	b.WriteU32(0) // currencies
	for _, it := range items {
		b.WriteBytes(fakeItemBytes(it))
	}
	b.WriteBit(true)  // Acquired
	b.WriteBit(false) // AELooting
	b.WriteBit(false) // SuppressError
	b.FlushBits()
	return b.Bytes()
}

func fakeLootRefusal(owner wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, owner)
	wire.WritePackedGUID(b, wire.GUID{})
	b.WriteU8(loot.ErrorTooFar)
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU8(2)
	b.WriteU32(0)
	b.WriteU32(0)     // items
	b.WriteU32(0)     // currencies
	b.WriteBit(false) // Acquired
	b.WriteBit(false) // AELooting
	b.WriteBit(false) // SuppressError
	b.FlushBits()
	return b.Bytes()
}

func fakeStartRoll(lootGUID wire.GUID, mapID int32, it fakeItemDef, valid uint8, ms uint32) []byte {
	if it.uiType == 0 {
		it.uiType = loot.SlotRollOngoing
	}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, lootGUID)
	b.WriteI32(mapID)
	b.WriteBytes(fakeItemBytes(it))
	b.WriteU32(ms)
	b.WriteU8(valid)
	for range 5 {
		b.WriteU32(0) // ineligibility reasons
	}
	b.WriteU8(loot.MethodGroupLoot)
	b.WriteI32(0) // encounter
	return b.Bytes()
}

func fakeRollBroadcast(lootGUID, player wire.GUID, it fakeItemDef, number int32, rollType uint8) []byte {
	if it.uiType == 0 {
		it.uiType = loot.SlotRollOngoing
	}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, lootGUID)
	wire.WritePackedGUID(b, player)
	b.WriteBytes(fakeItemBytes(it))
	b.WriteI32(number)
	b.WriteU8(rollType)
	b.WriteI32(0)
	b.WriteBit(false) // Autopassed
	b.WriteBit(false) // OffSpec
	b.FlushBits()
	return b.Bytes()
}

func fakeRollWon(lootGUID wire.GUID, it fakeItemDef, winner wire.GUID, number int32, rollType uint8) []byte {
	it.uiType = loot.SlotLocked
	b := wire.NewWriter()
	wire.WritePackedGUID(b, lootGUID)
	b.WriteBytes(fakeItemBytes(it))
	wire.WritePackedGUID(b, winner)
	b.WriteI32(number)
	b.WriteU8(rollType)
	b.WriteI32(0)
	b.WriteBit(true) // MainSpec
	b.FlushBits()
	return b.Bytes()
}

func fakeAllPassed(lootGUID wire.GUID, it fakeItemDef) []byte {
	it.uiType = loot.SlotAllowLoot
	b := wire.NewWriter()
	wire.WritePackedGUID(b, lootGUID)
	b.WriteBytes(fakeItemBytes(it))
	b.WriteI32(0)
	return b.Bytes()
}

func fakeRemoved(owner, lootGUID wire.GUID, listID uint8) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, owner)
	wire.WritePackedGUID(b, lootGUID)
	b.WriteU8(listID)
	return b.Bytes()
}

func fakeRelease(lootGUID, owner wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, lootGUID)
	wire.WritePackedGUID(b, owner)
	return b.Bytes()
}

func fakeMoneyNotify(money uint64) []byte {
	b := wire.NewWriter()
	b.WriteU64(money)
	b.WriteU64(0)
	b.WriteBit(true) // SoleLooter
	b.FlushBits()
	return b.Bytes()
}

// fakeGameObjectCreate is one TYPEID_GAME_OBJECT create block with GameObjectData::WriteCreate (UpdateFields.cpp
// :7515-7569): entry, display, flags, faction, level, state, type and the assist bit.
func fakeGameObjectCreate(guid []byte, mapID uint16, x, y, z float32, entry int32, state, goType int8) []byte {
	values := wire.NewWriter()
	values.WriteU8(0)      // field flags
	values.WriteU8(3)      // fragCGObject
	values.WriteU8(0xFF)   // end of the fragment list
	values.WriteU8(1)      // CGObject is indirect: its exists byte
	values.WriteI32(entry) // ObjectData::EntryID
	values.WriteU32(0)     // DynamicFlags
	values.WriteFloat32(1) // Scale
	values.WriteI32(100)   // DisplayID
	values.WriteU32(0)     // SpellVisualID
	values.WriteU32(0)     // StateSpellVisualID
	values.WriteU32(0)     // SpawnTrackingStateAnimID
	values.WriteU32(0)     // SpawnTrackingStateAnimKitID
	values.WriteU32(0)     // StateWorldEffectIDs count
	values.WriteU32(0)     // StateWorldEffectsQuestObjectiveID
	wire.WritePackedGUID(values, wire.GUID{})
	wire.WritePackedGUID(values, wire.GUID{})
	values.WriteU32(0) // Flags
	values.WriteU32(0) // FlagsB
	for range 4 {
		values.WriteFloat32(0) // ParentRotation
	}
	values.WriteI32(0) // FactionTemplate
	values.WriteI32(0) // Level (the Classic int32)
	values.WriteU8(uint8(state))
	values.WriteU8(uint8(goType))
	values.WriteU8(100) // PercentHealth
	values.WriteU32(0)  // ArtKit
	values.WriteU32(0)  // doodad sets
	values.WriteU32(0)  // CustomParam
	values.WriteI32(0)  // Level
	values.WriteU32(0)  // AnimGroupInstance
	values.WriteU32(0)  // UiWidgetItemID
	values.WriteU32(0)  // UiWidgetItemQuality
	values.WriteU32(0)  // UiWidgetItemCount
	values.WriteU32(0)  // WorldEffects count
	values.WriteBit(false)
	values.FlushBits()

	block := make([]byte, 0, 96+len(values.Bytes()))
	block = append(block, 1) // UPDATETYPE_CREATE_OBJECT
	block = append(block, guid...)
	block = append(block, objects.TypeGameObject)
	bits := fakeBits{}
	bits.bit(true)  // HasEntityPosition
	bits.bit(false) // NoBirthAnim
	bits.bit(false) // EnablePortals
	bits.bit(false) // PlayHoverAnim
	bits.bit(false) // ThisIsYou
	bits.bit(true)  // MovementUpdate
	for range 15 {
		bits.bit(false)
	}
	block = append(block, bits.flush()...)
	block = appendU32(block, 0) // pause times
	block = append(block, guid...)
	block = appendU64(block, 0x80000000)
	block = appendU32(block, 150000)
	block = append(block, fakeF32(x)...)
	block = append(block, fakeF32(y)...)
	block = append(block, fakeF32(z)...)
	block = append(block, fakeF32(0)...) // orientation
	block = append(block, fakeF32(0)...) // pitch
	block = append(block, fakeF32(0)...) // step-up elevation
	block = appendU32(block, 0)          // removed movement forces
	block = appendU32(block, 0)          // move index
	block = append(block, fakeF32(1)...) // gravity modifier
	b2 := fakeBits{}
	for range 9 {
		b2.bit(false)
	}
	block = append(block, b2.flush()...)
	block = append(block, fakeF32(2.5)...)
	block = append(block, fakeF32(40)...)
	for range 7 {
		block = append(block, fakeF32(2.5)...)
	}
	block = appendU32(block, 0)
	block = append(block, fakeF32(1)...)
	for range 17 {
		block = append(block, fakeF32(0)...)
	}
	b3 := fakeBits{}
	b3.bit(false) // HasSpline
	block = append(block, b3.flush()...)
	block = appendU32(block, uint32(len(values.Bytes())))
	block = append(block, values.Bytes()...)

	out := make([]byte, 0, len(block)+16)
	out = appendU16(out, mapID)
	out = appendU32(out, 1)
	b4 := fakeBits{}
	b4.bit(true)
	b4.bit(false)
	out = append(out, b4.flush()...)
	out = appendU32(out, uint32(len(block)))
	out = append(out, block...)
	return out
}

// fakeGameObjectState is SMSG_UPDATE_OBJECT with a values update setting GameObjectData::State (classic bit 17).
func fakeGameObjectState(guid wire.GUID, mapID uint16, state int8) []byte {
	body := wire.NewWriter()
	body.WriteU8(0)    // field flags
	body.WriteU8(0)    // the fragment id list did not change
	body.WriteU8(0x03) // CGObject: exists and changed
	body.WriteU32(1 << objects.TypeGameObject)
	body.WriteBits(1<<17, 28)
	body.FlushBits()
	body.WriteU8(uint8(state))
	body.WriteBit(false) // has assist action
	body.FlushBits()

	block := wire.NewWriter()
	block.WriteU8(0) // UPDATETYPE_VALUES
	wire.WritePackedGUID(block, guid)
	block.WriteU32(uint32(len(body.Bytes())))
	block.WriteBytes(body.Bytes())

	out := wire.NewWriter()
	out.WriteU16(mapID)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(len(block.Bytes())))
	out.WriteBytes(block.Bytes())
	return out.Bytes()
}

// fakeCreatureNotFound answers a creature query with a not-found result for every entry asked (QueryPackets.cpp
// :64-151: a count, then per entry its u32 id and a result byte; anything but zero is "not found").
func fakeCreatureNotFound(payload []byte) []byte {
	b := wire.NewReader(payload)
	count := int(b.ReadBits(6))
	b.FlushBits()
	entries := make([]uint32, 0, count)
	for range count {
		entries = append(entries, b.ReadU32())
	}
	out := wire.NewWriter()
	out.WriteBits(uint64(len(entries)), 6)
	out.FlushBits()
	for _, e := range entries {
		out.WriteU32(e)
		out.WriteU8(1)
	}
	return out.Bytes()
}

// serveLoot runs one loot or object-use scenario. The target and the traffic depend on lootCase; every case ends
// with the client's logout, except the link cases, which drop the socket at the named wait.
func (f *fakeServer) serveLoot(c *fakeConn, realm *fakeConn) {
	self := parseFakePacked(f.t, f.clientGUID)
	target := parseFakePacked(f.t, f.lootTarget.packed)
	want := target
	if f.lootWant != (wire.GUID{}) {
		want = f.lootWant
	}
	mapID := f.mapID
	c.writeFrame(fakeSMSGUpdateObject, fakeCreateBlock(f.clientGUID, mapID, 0, 0, 0, 40))
	if f.lootTarget.window {
		c.writeFrame(fakeSMSGUpdateObject, fakeGameObjectCreate(f.lootTarget.packed, mapID, 2, 0, 0,
			f.lootTarget.entry, f.lootTarget.state, f.lootTarget.goType))
	} else {
		health, maxHealth := int64(100), int64(100)
		if f.lootTarget.dead {
			health = 0
		}
		c.writeFrame(fakeSMSGUpdateObject, fakeUnitCreateBlock(f.lootTarget.packed, mapID, 2, 0, 0, 40,
			f.lootTarget.entry, health, maxHealth, 5, 0))
	}
	for _, extra := range f.lootExtras {
		c.writeFrame(fakeSMSGUpdateObject, fakeGameObjectCreate(extra.packed, mapID, extra.x, extra.y, extra.z,
			extra.entry, extra.state, extra.goType))
	}

	windowOpen := false
	windowLoot := wire.GUID{}
	windowOwner := wire.GUID{}
	items := f.lootItems
	rolls := f.lootRolls
	for {
		w, payload, err := c.readFrame()
		if err != nil {
			select {
			case <-f.logoutDone:
				return
			default:
			}
			if f.deliberate() || f.leftOnRead(err) {
				return
			}
			f.failf("loot: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, w)
		f.mu.Unlock()
		switch w {
		case fakeCMSGTimeSyncResponse, fakeCMSGMoveInitActiveMover, fakeCMSGSetSelection, fakeCMSGAttackStop,
			fakeCMSGMoveHeartbeat, fakeCMSGMoveSetFacing:
		case fakeCMSGPing:
			c.writeFrame(fakeSMSGPong, payload[:4])
		case fakeCMSGQueryCreature:
			c.writeFrame(fakeSMSGQueryCreatureRes, fakeCreatureNotFound(payload))
		case fakeCMSGAttackSwing:
			c.writeFrame(fakeSMSGAttackStart, append(fakePacked(self), fakePacked(target)...))
			c.writeFrame(fakeSMSGUpdateObject, buildHealthValues(target, 0))
		case fakeCMSGGameObjUse, fakeCMSGLootUnit:
			b := wire.NewReader(payload)
			got := wire.ReadPackedGUID(b)
			if b.Err() != nil || b.Remaining() != 0 {
				f.failf("loot: the open packet: %v (%d bytes left)", b.Err(), b.Remaining())
				return
			}
			if got != want {
				f.failf("loot: opened %v, want %v", got, want)
				return
			}
			switch f.lootCase {
			case "link-window":
				f.dropLink(realm, c)
				return
			case "link-use":
				if w == fakeCMSGGameObjUse {
					f.dropLink(realm, c)
					return
				}
			}
			f.mu.Lock()
			if w == fakeCMSGGameObjUse {
				f.lootUsed = true
			}
			f.mu.Unlock()
			windowOpen = true
			windowLoot = f.lootLootGUID
			windowOwner = want
			if f.lootCase == "use-refused" {
				// SendLootError: the same packet with Acquired false and the reason (Player.cpp:10048-10057).
				c.writeFrame(fakeSMSGLootResponse, fakeLootRefusal(want))
				windowOpen = false
				continue
			}
			if f.lootTarget.window && f.lootCase == "object-door" {
				// A door's use is the state change: no window. Values update first, then nothing else.
				c.writeFrame(fakeSMSGUpdateObject, fakeGameObjectState(want, mapID, 0))
				windowOpen = false
				continue
			}
			if f.lootCase == "window-other-owner" {
				// The server opened a window for someone else's object (a chest the target shares nothing with):
				// the client must not read it as the target's, and must still give it back.
				windowOwner = f.lootOtherOwner
			}
			c.writeFrame(fakeSMSGLootResponse, fakeLootWindow(windowOwner, windowLoot, loot.MethodGroupLoot,
				f.lootCoins, items))
			rollMs := f.lootRollMs
			if rollMs == 0 {
				rollMs = 300
			}
			for _, roll := range rolls {
				c.writeFrame(fakeSMSGStartLootRoll, fakeStartRoll(windowLoot, int32(mapID), roll, f.lootValid, rollMs))
				if f.lootCase == "roll-none" || f.lootCase == "roll-blocked" {
					// Nobody votes (or the client may not): the server's timer ends the roll at once here.
					c.writeFrame(fakeSMSGLootAllPassed, fakeAllPassed(windowLoot, roll))
				}
			}
			if f.lootCase == "loot-bad" {
				// A truncated loot packet: the parse error must end the run, and the window must still be released.
				c.writeFrame(fakeSMSGLootRemoved, []byte{0x00})
			}
		case fakeCMSGGameObjReport:
			f.mu.Lock()
			f.lootReported = true
			f.mu.Unlock()
		case fakeCMSGLootMoney:
			f.mu.Lock()
			f.lootMoney = true
			f.mu.Unlock()
			switch f.lootCase {
			case "link-money":
				f.dropLink(realm, c)
				return
			}
			c.writeFrame(fakeSMSGLootMoneyNotify, fakeMoneyNotify(uint64(f.lootCoins)))
			c.writeFrame(fakeSMSGCoinRemoved, fakePacked(windowLoot))
		case fakeCMSGLootItem:
			b := wire.NewReader(payload)
			count := b.ReadU32()
			batch := make([]uint8, 0, count)
			for range count {
				got := wire.ReadPackedGUID(b)
				listID := b.ReadU8()
				if got != windowLoot {
					f.failf("loot: item request names loot %v, want %v", got, windowLoot)
					return
				}
				batch = append(batch, listID)
			}
			b.ReadBit()
			b.FlushBits()
			if b.Err() != nil || b.Remaining() != 0 {
				f.failf("loot: CMSG_LOOT_ITEM: %v (%d bytes left)", b.Err(), b.Remaining())
				return
			}
			f.mu.Lock()
			f.lootItemsTaken = append(f.lootItemsTaken, batch...)
			f.mu.Unlock()
			switch f.lootCase {
			case "link-take":
				f.dropLink(realm, c)
				return
			}
			for _, id := range batch {
				c.writeFrame(fakeSMSGLootRemoved, fakeRemoved(want, windowLoot, id))
			}
		case fakeCMSGLootRoll:
			b := wire.NewReader(payload)
			got := wire.ReadPackedGUID(b)
			listID := b.ReadU8()
			vote := b.ReadU8()
			if b.Err() != nil || b.Remaining() != 0 {
				f.failf("loot: CMSG_LOOT_ROLL: %v (%d bytes left)", b.Err(), b.Remaining())
				return
			}
			if got != windowLoot {
				f.failf("loot: vote names loot %v, want %v", got, windowLoot)
				return
			}
			f.mu.Lock()
			f.lootVotes = append(f.lootVotes, vote)
			f.mu.Unlock()
			var def fakeItemDef
			found := false
			for _, it := range rolls {
				if it.listID == listID {
					def, found = it, true
				}
			}
			if !found {
				f.failf("loot: vote on slot %d, which never rolled", listID)
				return
			}
			c.writeFrame(fakeSMSGLootRoll, fakeRollBroadcast(windowLoot, self, def, 0, vote))
			switch f.lootCase {
			case "link-roll":
				f.dropLink(realm, c)
				return
			case "roll-long":
				// The roll never ends: the client must give up on its own deadline and still release the window.
				continue
			}
			if vote == loot.RollPass {
				c.writeFrame(fakeSMSGLootAllPassed, fakeAllPassed(windowLoot, def))
			} else {
				c.writeFrame(fakeSMSGLootRollWon, fakeRollWon(windowLoot, def, self, 77, vote))
			}
		case fakeCMSGLootRelease:
			b := wire.NewReader(payload)
			got := wire.ReadPackedGUID(b)
			if b.Err() != nil || b.Remaining() != 0 {
				f.failf("loot: CMSG_LOOT_RELEASE: %v (%d bytes left)", b.Err(), b.Remaining())
				return
			}
			// A release names the window's own owner: the target, or the object the server named in the window.
			releaseOwner := want
			if windowOpen {
				releaseOwner = windowOwner
			}
			if got != releaseOwner {
				f.failf("loot: release names %v, want the owner %v", got, releaseOwner)
				return
			}
			f.mu.Lock()
			f.lootReleased = true
			f.mu.Unlock()
			switch f.lootCase {
			case "link-release":
				f.dropLink(realm, c)
				return
			}
			if windowOpen {
				c.writeFrame(fakeSMSGLootRelease, fakeRelease(windowLoot, self))
				windowOpen = false
			}
		case fakeCMSGLogoutRequest:
			c.writeFrame(fakeSMSGLogoutResponse, []byte{0, 0, 0, 0, 0})
			time.Sleep(20 * time.Millisecond)
			realm.writeFrame(fakeSMSGLogoutComplete, []byte{0})
			f.logoutOnce.Do(func() { close(f.logoutDone) })
			time.Sleep(20 * time.Millisecond)
			c.nc.Close()
			return
		default:
			f.failf("loot: unexpected client wire 0x%06X", w)
			return
		}
	}
}

// ---- the tests ----

const (
	fakeLootMap         uint16 = 0
	fakeLootCorpseEntry int32  = 299
	fakeLootChestEntry  int32  = 2842
	fakeLootDoorEntry   int32  = 17153
)

// newLootScenario wires one fake run: the character create, one target and the loot case.
func newLootScenario(t *testing.T, lootCase string, target fakeLootTarget, items, rolls []fakeItemDef) (*fakeServer, wire.GUID, wire.GUID) {
	t.Helper()
	f := newFakeServer(t)
	f.loot = true
	f.lootCase = lootCase
	f.mapID = fakeLootMap
	f.lootTarget = target
	f.lootItems = items
	f.lootRolls = rolls
	f.lootCoins = 311
	f.lootValid = loot.FlagPass | loot.FlagNeed | loot.FlagGreed
	f.lootLootGUID = wire.GUID{Low: 0x900, High: 15 << 58}
	return f, parseFakePacked(t, target.packed), f.lootLootGUID
}

// enterLootScenario runs the scenario's client with the scenario's options and returns the report and the run's own
// error, without judging either: the tests that expect the run to fail keep the error.
func enterLootScenario(t *testing.T, f *fakeServer, opts Options) (*Report, []string, error) {
	t.Helper()
	f.wg.Add(1)
	go f.serveRealm()
	var steps []string
	opts.Address = f.realmListener.Addr().String()
	opts.RealmID = 70
	opts.RealmAddress = 0x46010046
	opts.RealmJoinTicket = []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`)
	opts.ClientSecret = f.keyData[:32]
	opts.JoinSecret = f.keyData[32:]
	opts.ClientBuild = 70707
	opts.RealmBuild = 70707
	opts.Character = "XXXXXX"
	opts.BuildAuthKey = f.buildKey[:]
	if opts.Settle == 0 {
		opts.Settle = 150 * time.Millisecond
	}
	if opts.Deadline == 0 {
		opts.Deadline = 20 * time.Second
	}
	opts.Logf = func(format string, args ...any) {
		steps = append(steps, fmt.Sprintf(format, args...))
	}
	report, err := Enter(t.Context(), opts)
	return report, steps, err
}

// runLootScenario enters the world with the scenario's options and returns the report.
func runLootScenario(t *testing.T, f *fakeServer, opts Options) (*Report, []string) {
	t.Helper()
	defer f.close()
	report, steps, err := enterLootScenario(t, f, opts)
	if err != nil {
		t.Fatalf("the loot run failed: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	return report, steps
}

func TestLootCheckAgainstFakeServer(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x301, High: 8 << 58}, entry: fakeLootCorpseEntry, dead: true}
	target.packed = fakePacked(target.guid)
	items := []fakeItemDef{
		{listID: 0, itemID: 4583, count: 1, uiType: loot.SlotAllowLoot},
		{listID: 1, itemID: 12208, count: 2, uiType: loot.SlotAllowLoot, trade: true},
	}
	f, _, _ := newLootScenario(t, "creature", target, items, nil)
	report, steps := runLootScenario(t, f, Options{
		LootCheck: true,
		LootMoney: true,
	})

	r := report.Loot
	if !r.Opened || r.Kind != "unit" || r.Entry != fakeLootCorpseEntry {
		t.Fatalf("the window: %+v\nsteps:\n%s", r, joinLines(steps))
	}
	if len(r.Items) != 2 || r.Items[0].ItemID != 4583 || r.Items[1].ItemID != 12208 || !r.Items[1].Trade {
		t.Fatalf("the items: %+v", r.Items)
	}
	if r.Coins != 311 || r.MoneyTaken != 311 {
		t.Fatalf("money: coins %d taken %d", r.Coins, r.MoneyTaken)
	}
	if len(r.Removed) != 2 || !r.Released {
		t.Fatalf("removed %v released %v", r.Removed, r.Released)
	}
	f.mu.Lock()
	taken := append([]uint8{}, f.lootItemsTaken...)
	f.mu.Unlock()
	if len(taken) != 2 || taken[0] != 0 || taken[1] != 1 {
		t.Fatalf("the item requests: %v", taken)
	}
	if !f.lootMoney || !f.lootReleased {
		t.Fatalf("money packet %v, release %v", f.lootMoney, f.lootReleased)
	}
}

func TestLootCheckRollNeed(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x302, High: 8 << 58}, entry: fakeLootCorpseEntry, dead: true}
	target.packed = fakePacked(target.guid)
	items := []fakeItemDef{{listID: 3, itemID: 11754, count: 1, uiType: loot.SlotRollOngoing}}
	f, self, _ := newLootScenario(t, "roll-need", target, items, items)
	report, steps := runLootScenario(t, f, Options{LootCheck: true, Roll: "need", LootMoney: true})

	r := report.Loot
	if len(r.Rolls) != 1 {
		t.Fatalf("the rolls: %+v\nsteps:\n%s", r.Rolls, joinLines(steps))
	}
	row := r.Rolls[0]
	if row.LootListID != 3 || row.OurVote != loot.RollNeed || row.Vote != loot.RollNeed ||
		row.Winner != "you" || row.Number != 77 || !row.Ended {
		t.Fatalf("the roll row: %+v", row)
	}
	if row.ValidRolls != loot.FlagPass|loot.FlagNeed|loot.FlagGreed || row.TimeLimit != 300 {
		t.Fatalf("the roll start: %+v", row)
	}
	f.mu.Lock()
	votes := append([]uint8{}, f.lootVotes...)
	f.mu.Unlock()
	if len(votes) != 1 || votes[0] != loot.RollNeed {
		t.Fatalf("the votes the server saw: %v", votes)
	}
	if !f.lootReleased {
		t.Fatal("the window was not released")
	}
	_ = self
}

func TestLootCheckVotesNothingWhenNotAsked(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x303, High: 8 << 58}, entry: fakeLootCorpseEntry, dead: true}
	target.packed = fakePacked(target.guid)
	items := []fakeItemDef{{listID: 0, itemID: 11754, count: 1, uiType: loot.SlotRollOngoing}}
	f, _, _ := newLootScenario(t, "roll-none", target, items, items)
	report, steps := runLootScenario(t, f, Options{LootCheck: true, LootMoney: true})

	f.mu.Lock()
	votes := append([]uint8{}, f.lootVotes...)
	f.mu.Unlock()
	if len(votes) != 0 {
		t.Fatalf("the client voted %v without being asked\nsteps:\n%s", votes, joinLines(steps))
	}
	row := report.Loot.Rolls[0]
	if row.OurVote != 0xFF || !row.AllPassed || row.Winner != "nobody" {
		t.Fatalf("the roll row: %+v", row)
	}
}

func TestLootCheckNeverVotesNeedUninvited(t *testing.T) {
	// The roll allows pass and greed only (ROLL_FLAG_TYPE_NEED clear, TryToStart's NEED_BEFORE_GREED rule and
	// ITEM_FLAG2_CAN_ONLY_ROLL_GREED, Loot.cpp:500-510): a need vote would be refused, so the client must not send it.
	target := fakeLootTarget{guid: wire.GUID{Low: 0x304, High: 8 << 58}, entry: fakeLootCorpseEntry, dead: true}
	target.packed = fakePacked(target.guid)
	items := []fakeItemDef{{listID: 0, itemID: 11754, count: 1, uiType: loot.SlotRollOngoing}}
	f, _, _ := newLootScenario(t, "roll-blocked", target, items, items)
	f.lootValid = loot.FlagPass | loot.FlagGreed
	report, steps := runLootScenario(t, f, Options{LootCheck: true, Roll: "need", LootMoney: true})

	f.mu.Lock()
	votes := append([]uint8{}, f.lootVotes...)
	f.mu.Unlock()
	if len(votes) != 0 {
		t.Fatalf("the client voted %v on a roll that does not allow it\nsteps:\n%s", votes, joinLines(steps))
	}
	if row := report.Loot.Rolls[0]; row.OurVote != 0xFF || row.ValidRolls != loot.FlagPass|loot.FlagGreed {
		t.Fatalf("the roll row: %+v", row)
	}
}

func TestLootCheckChestAgainstFakeServer(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x305, High: 8 << 58}, entry: fakeLootChestEntry,
		window: true, goType: goTypeChest, state: 1}
	target.packed = fakePacked(target.guid)
	items := []fakeItemDef{{listID: 0, itemID: 117, count: 1, uiType: loot.SlotAllowLoot}}
	f, _, _ := newLootScenario(t, "chest", target, items, nil)
	report, steps := runLootScenario(t, f, Options{LootCheck: true, LootMoney: true})

	r := report.Loot
	if !r.Opened || r.Kind != "game object" || r.Entry != fakeLootChestEntry {
		t.Fatalf("the window: %+v\nsteps:\n%s", r, joinLines(steps))
	}
	if !r.Released || len(r.Removed) != 1 {
		t.Fatalf("released %v, removed %v", r.Released, r.Removed)
	}
	f.mu.Lock()
	used, reported := f.lootUsed, f.lootReported
	f.mu.Unlock()
	if !used || !reported {
		t.Fatalf("the object was used %v, reported %v", used, reported)
	}
}

func TestObjectUseDoor(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x306, High: 8 << 58}, entry: fakeLootDoorEntry,
		window: true, goType: 0, state: 1}
	target.packed = fakePacked(target.guid)
	f, _, _ := newLootScenario(t, "object-door", target, nil, nil)
	report, steps := runLootScenario(t, f, Options{ObjectUse: true, Entry: fakeLootDoorEntry})

	r := report.ObjectUse
	if !r.Found || r.Entry != fakeLootDoorEntry {
		t.Fatalf("the object-use report: %+v\nsteps:\n%s", r, joinLines(steps))
	}
	if r.Ambiguous || len(r.Candidates) != 0 {
		t.Fatalf("one object in range was treated as ambiguous: %+v", r)
	}
	if !r.StateKnown || r.StateBefore != 1 || r.StateAfter != 0 || !r.StateChanged {
		t.Fatalf("the states: %+v", r)
	}
	if r.LootOpened {
		t.Fatalf("a door opened a loot window: %+v", r)
	}
}

func TestObjectUseChestReleasesTheWindow(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x307, High: 8 << 58}, entry: fakeLootChestEntry,
		window: true, goType: goTypeChest, state: 1}
	target.packed = fakePacked(target.guid)
	items := []fakeItemDef{{listID: 0, itemID: 117, count: 1, uiType: loot.SlotAllowLoot}}
	f, _, _ := newLootScenario(t, "object-chest", target, items, nil)
	report, steps := runLootScenario(t, f, Options{ObjectUse: true, Entry: fakeLootChestEntry})

	r := report.ObjectUse
	if !r.Found || !r.LootOpened || r.LootItems != 1 || !r.Released {
		t.Fatalf("the object-use report: %+v\nsteps:\n%s", r, joinLines(steps))
	}
	if len(report.Loot.Removed) != 0 {
		t.Fatalf("object-use took items: %v", report.Loot.Removed)
	}
}

// TestLootLinkDeath waits on each step in turn with the socket dropped underneath it: the run must end with a lost
// link, not a hang or a timeout dressed up as a quiet server.
func TestLootLinkDeath(t *testing.T) {
	cases := []struct {
		name   string
		loot   string
		window bool
		opts   Options
	}{
		{"window", "link-window", false, Options{LootCheck: true}},
		{"use", "link-use", true, Options{LootCheck: true}},
		{"money", "link-money", false, Options{LootCheck: true, LootMoney: true}},
		{"take", "link-take", false, Options{LootCheck: true, LootMoney: false}},
		{"release", "link-release", false, Options{LootCheck: true}},
		{"roll", "link-roll", false, Options{LootCheck: true, Roll: "greed"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := fakeLootTarget{guid: wire.GUID{Low: 0x310, High: 8 << 58}, entry: fakeLootCorpseEntry, dead: true}
			if c.window {
				target = fakeLootTarget{guid: wire.GUID{Low: 0x311, High: 8 << 58}, entry: fakeLootChestEntry,
					window: true, goType: goTypeChest, state: 1}
			}
			target.packed = fakePacked(target.guid)
			items := []fakeItemDef{{listID: 0, itemID: 4583, count: 1, uiType: loot.SlotAllowLoot}}
			rolls := []fakeItemDef(nil)
			if c.name == "roll" {
				items = []fakeItemDef{{listID: 0, itemID: 11754, count: 1, uiType: loot.SlotRollOngoing}}
				rolls = items
			}
			f, _, _ := newLootScenario(t, c.loot, target, items, rolls)
			f.deliberateClose = true
			defer f.close()
			f.wg.Add(1)
			go f.serveRealm()
			opts := c.opts
			opts.Address = f.realmListener.Addr().String()
			opts.RealmID = 70
			opts.RealmAddress = 0x46010046
			opts.RealmJoinTicket = []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`)
			opts.ClientSecret = f.keyData[:32]
			opts.JoinSecret = f.keyData[32:]
			opts.ClientBuild = 70707
			opts.RealmBuild = 70707
			opts.Character = "XXXXXX"
			opts.BuildAuthKey = f.buildKey[:]
			opts.Settle = 150 * time.Millisecond
			opts.Deadline = 10 * time.Second
			started := time.Now()
			_, err := Enter(t.Context(), opts)
			if !errors.Is(err, ErrLinkLost) {
				t.Fatalf("the run ended with %v, want a lost link", err)
			}
			if elapsed := time.Since(started); elapsed > 8*time.Second {
				t.Fatalf("the run took %s to notice the loss", elapsed.Round(time.Millisecond))
			}
		})
	}
}

func TestObjectUseRefusedWindowStillReportsState(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x309, High: 8 << 58}, entry: fakeLootChestEntry,
		window: true, goType: goTypeChest, state: 1}
	target.packed = fakePacked(target.guid)
	f, _, _ := newLootScenario(t, "use-refused", target, nil, nil)
	report, steps := runLootScenario(t, f, Options{ObjectUse: true, Entry: fakeLootChestEntry})

	r := report.ObjectUse
	if !r.Found || r.LootOpened || !r.StateKnown || r.StateBefore != 1 || r.StateAfter != 1 {
		t.Fatalf("the refused use: %+v\nsteps:\n%s", r, joinLines(steps))
	}
}

// TestLootKillFirst: --entry names a living creature; the run kills it and loots the corpse it leaves.
func TestLootKillFirst(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x308, High: 8 << 58}, entry: fakeLootCorpseEntry}
	target.packed = fakePacked(target.guid)
	items := []fakeItemDef{{listID: 0, itemID: 4583, count: 1, uiType: loot.SlotAllowLoot}}
	f, _, _ := newLootScenario(t, "kill", target, items, nil)
	report, steps := runLootScenario(t, f, Options{
		LootCheck: true,
		Entry:     fakeLootCorpseEntry,
		LootMoney: true,
		FightFor:  5 * time.Second,
	})

	if !report.Loot.Opened {
		t.Fatalf("no window after the kill: %+v\nsteps:\n%s", report.Loot, joinLines(steps))
	}
	if !containsWire(f.instWire, fakeCMSGAttackSwing) {
		t.Fatalf("no attack was sent: % X", f.instWire)
	}
}

// TestLootDeadlineReleasesTheWindow: the run's own deadline ends it while it waits for a roll that never ends. The
// window the server gave it must still be given back, on the cleanup path, instead of staying open on the server
// until the loot's own timeout.
func TestLootDeadlineReleasesTheWindow(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x312, High: 8 << 58}, entry: fakeLootCorpseEntry, dead: true}
	target.packed = fakePacked(target.guid)
	items := []fakeItemDef{{listID: 0, itemID: 11754, count: 1, uiType: loot.SlotRollOngoing}}
	f, _, _ := newLootScenario(t, "roll-long", target, items, items)
	f.lootRollMs = 60000
	f.clientMayLeave = true // the run's deadline ends it; the client closes its sockets right after the cleanup
	defer f.close()
	report, steps, err := enterLootScenario(t, f, Options{
		LootCheck: true,
		Roll:      "need",
		Deadline:  5 * time.Second,
	})
	f.wait(t)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the run ended with %v, want the deadline\nsteps:\n%s", err, joinLines(steps))
	}
	if report == nil || !report.Loot.Opened {
		t.Fatalf("no window was opened before the deadline: %+v", report)
	}
	f.mu.Lock()
	released := f.lootReleased
	f.mu.Unlock()
	if !released {
		t.Fatalf("the window was not released when the deadline ended the run\nsteps:\n%s", joinLines(steps))
	}
	if !report.Loot.ReleaseAttempted {
		t.Fatalf("the report does not say the cleanup release was attempted: %+v", report.Loot)
	}
}

// TestLootParseErrorReleasesTheWindow: a loot packet this client cannot read ends the run with the parse error; the
// window is still released, and the parse error stays the run's error.
func TestLootParseErrorReleasesTheWindow(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x313, High: 8 << 58}, entry: fakeLootCorpseEntry, dead: true}
	target.packed = fakePacked(target.guid)
	items := []fakeItemDef{{listID: 0, itemID: 4583, count: 1, uiType: loot.SlotAllowLoot}}
	f, _, _ := newLootScenario(t, "loot-bad", target, items, nil)
	f.clientMayLeave = true
	defer f.close()
	report, steps, err := enterLootScenario(t, f, Options{LootCheck: true})
	f.wait(t)
	if err == nil || !strings.Contains(err.Error(), "SMSG_LOOT_REMOVED") {
		t.Fatalf("the run ended with %v, want the SMSG_LOOT_REMOVED parse error\nsteps:\n%s", err, joinLines(steps))
	}
	if report == nil || !report.Loot.Opened {
		t.Fatalf("no window was opened: %+v", report)
	}
	f.mu.Lock()
	released := f.lootReleased
	f.mu.Unlock()
	if !released {
		t.Fatalf("the window was not released after the parse error\nsteps:\n%s", joinLines(steps))
	}
	if !report.Loot.ReleaseAttempted {
		t.Fatalf("the report does not say the cleanup release was attempted: %+v", report.Loot)
	}
}

// TestObjectUseRefusesAmbiguousTargets: two game objects of the entry stand in range. Using the nearest would be a
// guess, so the command refuses and names each one by where it stands and how far it is — and sends no use.
func TestObjectUseRefusesAmbiguousTargets(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x314, High: 8 << 58}, entry: fakeLootDoorEntry,
		window: true, goType: 0, state: 1}
	target.packed = fakePacked(target.guid)
	extra := fakeExtraObject{guid: wire.GUID{Low: 0x315, High: 8 << 58}, entry: fakeLootDoorEntry,
		goType: 0, state: 1, x: 10, y: 0, z: 0}
	extra.packed = fakePacked(extra.guid)
	f, _, _ := newLootScenario(t, "object-ambiguous", target, nil, nil)
	f.lootExtras = []fakeExtraObject{extra}
	f.clientMayLeave = true // the refusal ends the run before its logout
	defer f.close()
	report, steps, err := enterLootScenario(t, f, Options{ObjectUse: true, Entry: fakeLootDoorEntry})
	f.wait(t)
	if !errors.Is(err, ErrAmbiguousObject) {
		t.Fatalf("the run ended with %v, want the ambiguous refusal (a script must tell it from a use)\nsteps:\n%s",
			err, joinLines(steps))
	}

	r := report.ObjectUse
	if r.Found || !r.Ambiguous {
		t.Fatalf("the object-use report: %+v\nsteps:\n%s", r, joinLines(steps))
	}
	if len(r.Candidates) != 2 {
		t.Fatalf("the candidates: %+v", r.Candidates)
	}
	if r.Candidates[0].Distance != 2 || r.Candidates[1].Distance != 10 {
		t.Fatalf("the candidates' distances: %+v", r.Candidates)
	}
	if r.Candidates[1].X != 10 || r.Candidates[1].Y != 0 || r.Candidates[1].Z != 0 {
		t.Fatalf("the second candidate's position: %+v", r.Candidates[1])
	}
	f.mu.Lock()
	used := f.lootUsed
	f.mu.Unlock()
	if used {
		t.Fatalf("the client used one of the ambiguous objects\nsteps:\n%s", joinLines(steps))
	}
}

// TestObjectUseAtPicksTheObject: --at names the object, and the use must go to that one and not to the nearer.
func TestObjectUseAtPicksTheObject(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x316, High: 8 << 58}, entry: fakeLootDoorEntry,
		window: true, goType: 0, state: 1}
	target.packed = fakePacked(target.guid)
	// Both stand inside the interaction range, so the run walks nowhere; the nearer one is not the one --at names.
	extra := fakeExtraObject{guid: wire.GUID{Low: 0x317, High: 8 << 58}, entry: fakeLootDoorEntry,
		goType: 0, state: 1, x: 3.5, y: 0, z: 0}
	extra.packed = fakePacked(extra.guid)
	f, _, _ := newLootScenario(t, "object-door", target, nil, nil)
	f.lootExtras = []fakeExtraObject{extra}
	// The object at the --at point is the one to use; the fake fails the run if the use names anything else.
	f.lootWant = extra.guid
	report, steps := runLootScenario(t, f, Options{
		ObjectUse: true,
		Entry:     fakeLootDoorEntry,
		AtSet:     true,
		AtX:       3.5, AtY: 0, AtZ: 0,
	})

	r := report.ObjectUse
	if !r.Found || r.Ambiguous || r.Distance != 3.5 {
		t.Fatalf("the object-use report: %+v\nsteps:\n%s", r, joinLines(steps))
	}
	if !r.StateKnown || r.StateBefore != 1 || r.StateAfter != 0 || !r.StateChanged {
		t.Fatalf("the states: %+v", r)
	}
	f.mu.Lock()
	used := f.lootUsed
	f.mu.Unlock()
	if !used {
		t.Fatalf("the object at the --at point was not used\nsteps:\n%s", joinLines(steps))
	}
}

// TestLootWindowForAnotherOwnerIsReleased: the server answers the open with a window for an object the client never
// opened. The run must not read it as the target's — and must still give it back, because a window left open on the
// server waits for the loot's own timeout.
func TestLootWindowForAnotherOwnerIsReleased(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x318, High: 8 << 58}, entry: fakeLootCorpseEntry, dead: true}
	target.packed = fakePacked(target.guid)
	items := []fakeItemDef{{listID: 0, itemID: 4583, count: 1, uiType: loot.SlotAllowLoot}}
	f, _, _ := newLootScenario(t, "window-other-owner", target, items, nil)
	f.lootOtherOwner = wire.GUID{Low: 0x319, High: 8 << 58}
	f.clientMayLeave = true // the refusal ends the run before its logout
	defer f.close()
	report, steps, err := enterLootScenario(t, f, Options{LootCheck: true})
	f.wait(t)
	if err == nil || !strings.Contains(err.Error(), "belongs to another object") {
		t.Fatalf("the run ended with %v, want the window's owner mismatch\nsteps:\n%s", err, joinLines(steps))
	}
	f.mu.Lock()
	released := f.lootReleased
	f.mu.Unlock()
	if !released {
		t.Fatalf("the window for another owner was not released\nsteps:\n%s", joinLines(steps))
	}
	if !report.Loot.ReleaseAttempted {
		t.Fatalf("the report does not say the cleanup release was attempted: %+v", report.Loot)
	}
}

// TestLootCheckRefusesAmbiguousChests: two chests of the entry stand in range and --at names none. Looting the
// nearest would be a guess at whose chest it is, so the run refuses (ErrAmbiguousObject, nothing opened) and lists
// both by position and distance only.
func TestLootCheckRefusesAmbiguousChests(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x31A, High: 8 << 58}, entry: fakeLootChestEntry,
		window: true, goType: goTypeChest, state: 1}
	target.packed = fakePacked(target.guid)
	extra := fakeExtraObject{guid: wire.GUID{Low: 0x31B, High: 8 << 58}, entry: fakeLootChestEntry,
		goType: goTypeChest, state: 1, x: 10, y: 0, z: 0}
	extra.packed = fakePacked(extra.guid)
	f, _, _ := newLootScenario(t, "chest-ambiguous", target, nil, nil)
	f.lootExtras = []fakeExtraObject{extra}
	f.clientMayLeave = true // the refusal ends the run before its logout
	defer f.close()
	report, steps, err := enterLootScenario(t, f, Options{LootCheck: true})
	f.wait(t)
	if !errors.Is(err, ErrAmbiguousObject) {
		t.Fatalf("the run ended with %v, want the ambiguous refusal\nsteps:\n%s", err, joinLines(steps))
	}
	r := report.Loot
	if !r.Ambiguous || r.Opened || len(r.Candidates) != 2 {
		t.Fatalf("the loot report: %+v\nsteps:\n%s", r, joinLines(steps))
	}
	if r.Candidates[0].X != 2 || r.Candidates[1].X != 10 || r.Candidates[1].Distance != 10 {
		t.Fatalf("the candidates: %+v", r.Candidates)
	}
	f.mu.Lock()
	used := f.lootUsed
	f.mu.Unlock()
	if used {
		t.Fatalf("the client used one of the ambiguous chests\nsteps:\n%s", joinLines(steps))
	}
}

// TestLootCheckAtPicksTheChest: --at names the chest to loot, and the open must go to that one and not to the nearer.
func TestLootCheckAtPicksTheChest(t *testing.T) {
	target := fakeLootTarget{guid: wire.GUID{Low: 0x31C, High: 8 << 58}, entry: fakeLootChestEntry,
		window: true, goType: goTypeChest, state: 1}
	target.packed = fakePacked(target.guid)
	// Both chests stand inside the interaction range, so the run walks nowhere; the nearer one is not the one --at
	// names.
	extra := fakeExtraObject{guid: wire.GUID{Low: 0x31D, High: 8 << 58}, entry: fakeLootChestEntry,
		goType: goTypeChest, state: 1, x: 3.5, y: 0, z: 0}
	extra.packed = fakePacked(extra.guid)
	items := []fakeItemDef{{listID: 0, itemID: 117, count: 1, uiType: loot.SlotAllowLoot}}
	f, _, _ := newLootScenario(t, "chest", target, items, nil)
	f.lootExtras = []fakeExtraObject{extra}
	// The chest at the --at point is the one to open; the fake fails the run if the use names anything else.
	f.lootWant = extra.guid
	report, steps := runLootScenario(t, f, Options{
		LootCheck: true,
		AtSet:     true,
		AtX:       3.5, AtY: 0, AtZ: 0,
	})

	r := report.Loot
	if !r.Opened || r.Ambiguous || r.Distance != 3.5 || !r.Released {
		t.Fatalf("the loot report: %+v\nsteps:\n%s", r, joinLines(steps))
	}
	f.mu.Lock()
	used := f.lootUsed
	f.mu.Unlock()
	if !used {
		t.Fatalf("the chest at the --at point was not opened\nsteps:\n%s", joinLines(steps))
	}
}
