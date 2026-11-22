package world

// D5: loot, loot_take, loot_close, open_object, use_object, gameobjects and roll over a handler-boundary fake: the
// fake server answers each client loot packet through the session's own loot handler, so the window, the removals
// and the release travel the same path as live traffic. The failing case first is
// TestServeLootTakeOneSlotKeepsWindow: a take that releases the window (or a loot that releases it at once) fails it.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

var (
	lootCorpseA = wire.GUID{Low: 0xA001, High: uint64(objects.TypeUnit) << 58}
	lootCorpseB = wire.GUID{Low: 0xA002, High: uint64(objects.TypeUnit) << 58}
	lootLiving  = wire.GUID{Low: 0xA003, High: uint64(objects.TypeUnit) << 58}
	lootChest   = wire.GUID{Low: 0xB001, High: uint64(objects.TypeGameObject) << 58}
	lootDoor    = wire.GUID{Low: 0xB002, High: uint64(objects.TypeGameObject) << 58}
	lootFarObj  = wire.GUID{Low: 0xB003, High: uint64(objects.TypeGameObject) << 58}
	lootIDA     = wire.GUID{Low: 0x900A, High: 15 << 58}
	lootIDB     = wire.GUID{Low: 0x900B, High: 15 << 58}
	lootIDChest = wire.GUID{Low: 0x900C, High: 15 << 58}
)

// lootOwnerDef is what the fake server holds for one creature or game object.
type lootOwnerDef struct {
	loot   wire.GUID
	items  []fakeItemDef
	coins  uint32
	door   bool // a use changes the object's state and opens no window
	refuse bool // the open is answered with a refusal
	rolls  []fakeItemDef
}

// lootFakeVote is one CMSG_LOOT_ROLL the fake saw.
type lootFakeVote struct {
	loot wire.GUID
	slot uint8
	vote uint8
}

type lootWorld struct {
	*itemWorld
	owners   map[wire.GUID]*lootOwnerDef
	open     wire.GUID
	valid    uint8
	releases []wire.GUID
	requests [][]uint8
	votes    []lootFakeVote
	reports  int
	// silentItems makes the fake ignore CMSG_LOOT_ITEM, as a stalled server does.
	silentItems bool
	// failCore, when set, makes the fake's write of that opcode fail.
	failCore uint32
}

func newLootWorld(t *testing.T) *lootWorld {
	t.Helper()
	l := &lootWorld{itemWorld: newItemWorld(t), owners: map[wire.GUID]*lootOwnerDef{}, valid: loot.FlagPass | loot.FlagNeed | loot.FlagGreed}
	l.onSend = l.answer
	l.s.fightSender = l
	return l
}

// Send fails the one opcode the test names and otherwise is the item fake's write.
func (l *lootWorld) Send(core uint32, payload []byte) error {
	if l.failCore != 0 && core == l.failCore {
		return errors.New("write: broken pipe")
	}
	return l.itemWorld.Send(core, payload)
}

// place puts a create block into the model, the way the dispatch applies it.
func (l *lootWorld) place(payload []byte) {
	l.t.Helper()
	u, err := objects.ParseUpdateObjectForBuild(payload, 70707)
	if err != nil || len(u.Blocks) == 0 {
		l.t.Fatalf("the fake create: %v", err)
	}
	l.s.applyUpdate(u)
	l.world.Apply(u, l.now)
}

// corpse puts a dead creature at (x, 0, 0) with its loot.
func (l *lootWorld) corpse(guid, lootID wire.GUID, x float32, coins uint32, items ...fakeItemDef) *lootOwnerDef {
	l.place(fakeUnitCreateBlock(fakePacked(guid), 0, x, 0, 0, 40, 299, 0, 100, 5, 0))
	d := &lootOwnerDef{loot: lootID, coins: coins, items: items}
	l.owners[guid] = d
	return d
}

// chest puts a chest (game object type 3) at (x, 0, 0) with its loot.
func (l *lootWorld) chest(guid, lootID wire.GUID, x float32, coins uint32, items ...fakeItemDef) *lootOwnerDef {
	l.place(fakeGameObjectCreate(fakePacked(guid), 0, x, 0, 0, 2842, 1, 3))
	d := &lootOwnerDef{loot: lootID, coins: coins, items: items}
	l.owners[guid] = d
	return d
}

func (l *lootWorld) deliver(core uint32, payload []byte) {
	l.t.Helper()
	if err := l.s.handleLoot(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}); err != nil {
		l.t.Fatalf("%s: %v", wire.ServerName(core), err)
	}
}

func (l *lootWorld) answer(_ *itemWorld, core uint32, payload []byte) {
	switch core {
	case wire.CMSGLootUnit, wire.CMSGGameObjUse:
		g := parseFakePacked(l.t, payload)
		o := l.owners[g]
		if o == nil {
			return
		}
		switch {
		case o.door:
			u, err := objects.ParseUpdateObjectForBuild(fakeGameObjectState(g, 0, 0), 70707)
			if err != nil {
				l.t.Fatalf("the door state: %v", err)
			}
			l.s.applyUpdate(u)
			l.world.Apply(u, l.now)
		case o.refuse:
			l.deliver(wire.SMSGLootResponse, fakeLootRefusal(g))
		default:
			l.open = g
			l.deliver(wire.SMSGLootResponse, fakeLootWindow(g, o.loot, loot.MethodGroupLoot, o.coins, o.items))
			for _, r := range o.rolls {
				l.deliver(wire.SMSGStartLootRoll, fakeStartRoll(o.loot, 0, r, l.valid, 60000))
			}
		}
	case wire.CMSGGameObjReportUse:
		l.reports++
	case wire.CMSGLootItem:
		b := wire.NewReader(payload)
		n := b.ReadU32()
		var ids []uint8
		for range n {
			wire.ReadPackedGUID(b)
			ids = append(ids, b.ReadU8())
		}
		l.requests = append(l.requests, ids)
		if l.silentItems {
			return
		}
		o := l.owners[l.open]
		for _, id := range ids {
			l.deliver(wire.SMSGLootRemoved, fakeRemoved(l.open, o.loot, id))
			o.items = slices.DeleteFunc(o.items, func(it fakeItemDef) bool { return it.listID == id })
		}
	case wire.CMSGLootMoney:
		o := l.owners[l.open]
		l.deliver(wire.SMSGLootMoneyNotify, fakeMoneyNotify(uint64(o.coins)))
		l.deliver(wire.SMSGCoinRemoved, fakePacked(o.loot))
		o.coins = 0
	case wire.CMSGLootRelease:
		g := parseFakePacked(l.t, payload)
		l.releases = append(l.releases, g)
		if l.open == g {
			l.deliver(wire.SMSGLootRelease, fakeRelease(g, itemSelf))
			l.open = wire.GUID{}
		}
	case wire.CMSGLootRoll:
		b := wire.NewReader(payload)
		g := wire.ReadPackedGUID(b)
		l.votes = append(l.votes, lootFakeVote{loot: g, slot: b.ReadU8(), vote: b.ReadU8()})
	case wire.CMSGLogoutRequest:
		l.s.report.LogoutComplete = true
	}
}

func (l *lootWorld) count(core uint32) int {
	n := 0
	for _, s := range l.sent {
		if s.core == core {
			n++
		}
	}
	return n
}

// index is the position of the nth packet of a core in the sent order, -1 when there are fewer.
func (l *lootWorld) index(core uint32, nth int) int {
	for i, s := range l.sent {
		if s.core == core {
			if nth == 0 {
				return i
			}
			nth--
		}
	}
	return -1
}

func twoItems() []fakeItemDef {
	return []fakeItemDef{
		{listID: 0, itemID: 4583, count: 1, uiType: loot.SlotAllowLoot},
		{listID: 1, itemID: 12208, count: 2, uiType: loot.SlotAllowLoot},
	}
}

func slotsOf(w *ServeLootWindow) []uint8 {
	var out []uint8
	for _, it := range w.Items {
		out = append(out, it.Slot)
	}
	return out
}

// TestServeLootTakeOneSlotKeepsWindow is the failing case: loot opens a window and keeps it, loot_take takes one
// slot without releasing, and loot_close is what sends the release.
func TestServeLootTakeOneSlotKeepsWindow(t *testing.T) {
	l := newLootWorld(t)
	l.corpse(lootCorpseA, lootIDA, 2, 311, twoItems()...)
	ctx := context.Background()

	res, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(lootCorpseA)})
	if err != nil || !res.OK {
		t.Fatalf("loot: %+v %v", res, err)
	}
	if res.GUID != GUID(lootCorpseA) || res.LootGUID != GUID(lootIDA) || res.Type != "unit" || res.Money != 311 {
		t.Fatalf("window %+v", res.ServeLootWindow)
	}
	if !slices.Equal(slotsOf(&res.ServeLootWindow), []uint8{0, 1}) || res.Items[1].ItemEntry != 12208 || res.Items[1].Count != 2 || res.Items[0].SlotType != "allow loot" {
		t.Fatalf("items %+v", res.Items)
	}
	if res.Event == nil || res.Event.Kind != events.KindLootOpen || res.Event.Seq <= res.Since {
		t.Fatalf("event %+v since %d", res.Event, res.Since)
	}
	if l.count(wire.CMSGLootRelease) != 0 || l.s.methodLoot == nil {
		t.Fatal("the window was released when loot returned")
	}

	slot := 0
	take, err := l.s.serveLootTake(ctx, serveLootTakeParams{Slot: &slot})
	if err != nil || !take.OK {
		t.Fatalf("loot_take: %+v %v", take, err)
	}
	if !slices.Equal(take.Taken, []int{0}) || !slices.Equal(take.Left, []int{1}) || take.Window == nil || !slices.Equal(slotsOf(take.Window), []uint8{1}) {
		t.Fatalf("take %+v window %+v", take, take.Window)
	}
	if take.Event == nil || take.Event.Kind != events.KindLootTaken {
		t.Fatalf("take event %+v", take.Event)
	}
	if l.count(wire.CMSGLootRelease) != 0 {
		t.Fatal("loot_take released the window")
	}
	if got := l.requests; len(got) != 1 || !slices.Equal(got[0], []uint8{0}) {
		t.Fatalf("CMSG_LOOT_ITEM asked %v", got)
	}

	// loot with no guid answers the held window, with no packet.
	sent := len(l.sent)
	held, err := l.s.serveLoot(ctx, serveLootParams{})
	if err != nil || !slices.Equal(slotsOf(&held.ServeLootWindow), []uint8{1}) || len(l.sent) != sent {
		t.Fatalf("held window %+v %v (%d packets sent)", held, err, len(l.sent)-sent)
	}

	cl, err := l.s.serveLootClose(ctx, serveLootCloseParams{})
	if err != nil || !cl.OK || !cl.Closed || cl.Event == nil || cl.Event.Kind != events.KindLootClosed {
		t.Fatalf("loot_close %+v %v", cl, err)
	}
	if len(l.releases) != 1 || l.releases[0] != lootCorpseA || l.s.methodLoot != nil {
		t.Fatalf("releases %v held %v", l.releases, l.s.methodLoot)
	}
	if _, err := l.s.serveLootTake(ctx, serveLootTakeParams{All: true}); err == nil || !strings.Contains(err.Error(), "no loot window") {
		t.Fatalf("a take after the close answered %v", err)
	}
	if _, err := l.s.serveLoot(ctx, serveLootParams{}); err == nil || !strings.Contains(err.Error(), "no loot window") {
		t.Fatalf("loot with nothing held answered %v", err)
	}
	// Closing again is a no-op.
	sent = len(l.sent)
	if cl, err = l.s.serveLootClose(ctx, serveLootCloseParams{}); err != nil || !cl.OK || cl.Closed || len(l.sent) != sent {
		t.Fatalf("second close %+v %v", cl, err)
	}
}

func TestServeLootTakeAllReleasesByItself(t *testing.T) {
	l := newLootWorld(t)
	l.corpse(lootCorpseA, lootIDA, 2, 311, twoItems()...)
	ctx := context.Background()
	if res, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(lootCorpseA)}); err != nil || !res.OK {
		t.Fatalf("loot: %+v %v", res, err)
	}
	take, err := l.s.serveLootTake(ctx, serveLootTakeParams{All: true})
	if err != nil || !take.OK {
		t.Fatalf("loot_take all: %+v %v", take, err)
	}
	if !slices.Equal(take.Taken, []int{0, 1}) || take.Money != 311 || take.Window != nil || len(take.Left) != 0 {
		t.Fatalf("take %+v", take)
	}
	if len(l.releases) != 1 || l.s.methodLoot != nil {
		t.Fatalf("the emptied window was not released: %v held %v", l.releases, l.s.methodLoot)
	}
	if got := l.requests; len(got) != 1 || !slices.Equal(got[0], []uint8{0, 1}) {
		t.Fatalf("CMSG_LOOT_ITEM asked %v", got)
	}
}

func TestServeLootTakeMoneyOnlyAndSlotsItCannotTake(t *testing.T) {
	l := newLootWorld(t)
	items := append(twoItems(), fakeItemDef{listID: 2, itemID: 777, count: 1, uiType: loot.SlotRollOngoing})
	l.corpse(lootCorpseA, lootIDA, 2, 311, items...)
	ctx := context.Background()
	if res, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(lootCorpseA)}); err != nil || !res.OK || res.Items[2].SlotType != "roll ongoing" {
		t.Fatalf("loot: %+v %v", res, err)
	}
	take, err := l.s.serveLootTake(ctx, serveLootTakeParams{Money: true})
	if err != nil || !take.OK || take.Money != 311 || len(take.Taken) != 0 || take.Window == nil || len(take.Window.Items) != 3 || take.Window.Money != 0 {
		t.Fatalf("money take %+v %v", take, err)
	}
	if len(l.requests) != 0 {
		t.Fatalf("a money take asked for items: %v", l.requests)
	}
	// A slot still on a roll is skipped and reported, with no packet for it.
	slot := 2
	take, err = l.s.serveLootTake(ctx, serveLootTakeParams{Slot: &slot})
	if err != nil || take.OK || !slices.Equal(take.Skipped, []int{2}) || take.Event == nil || take.Event.Kind != events.KindLootError || take.Event.Text == "" {
		t.Fatalf("a roll slot: %+v %v", take, err)
	}
	if len(l.requests) != 0 {
		t.Fatalf("the roll slot was requested: %v", l.requests)
	}
	// all takes what can be taken, reports the rest and keeps the window (the roll item is still in it).
	take, err = l.s.serveLootTake(ctx, serveLootTakeParams{All: true})
	if err != nil || !take.OK || !slices.Equal(take.Taken, []int{0, 1}) || !slices.Equal(take.Skipped, []int{2}) || !slices.Equal(take.Left, []int{2}) || take.Window == nil {
		t.Fatalf("all with a roll slot: %+v %v", take, err)
	}
	if len(l.releases) != 0 {
		t.Fatalf("a window with a slot left was released: %v", l.releases)
	}
}

func TestServeLootSecondCorpseReleasesTheFirst(t *testing.T) {
	l := newLootWorld(t)
	l.corpse(lootCorpseA, lootIDA, 2, 0, twoItems()...)
	l.corpse(lootCorpseB, lootIDB, 3, 0, fakeItemDef{listID: 0, itemID: 55, count: 1})
	ctx := context.Background()
	if res, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(lootCorpseA)}); err != nil || !res.OK {
		t.Fatalf("first loot: %+v %v", res, err)
	}
	res, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(lootCorpseB)})
	if err != nil || !res.OK || res.GUID != GUID(lootCorpseB) || len(res.Items) != 1 {
		t.Fatalf("second loot: %+v %v", res, err)
	}
	if len(l.releases) != 1 || l.releases[0] != lootCorpseA {
		t.Fatalf("releases %v", l.releases)
	}
	rel, second := l.index(wire.CMSGLootRelease, 0), l.index(wire.CMSGLootUnit, 1)
	if rel < 0 || second < 0 || rel > second {
		t.Fatalf("the first window was released after the second open (release at %d, open at %d)", rel, second)
	}
	if l.s.methodLoot == nil || l.s.methodLoot.Owner != lootCorpseB {
		t.Fatalf("held %+v", l.s.methodLoot)
	}
}

func TestServeLootPreconditionsSendNothing(t *testing.T) {
	l := newLootWorld(t)
	l.corpse(lootCorpseA, lootIDA, 2, 0, twoItems()...)
	l.place(fakeUnitCreateBlock(fakePacked(lootLiving), 0, 2, 0, 0, 40, 299, 100, 100, 5, 0))
	ctx := context.Background()
	zero := 0
	for name, call := range map[string]func() error{
		"alive": func() error { _, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(lootLiving)}); return err },
		"unknown": func() error {
			_, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(wire.GUID{Low: 5, High: 8 << 58})})
			return err
		},
		"name": func() error { _, err := l.s.serveLoot(ctx, serveLootParams{Name: "nobody"}); return err },
		"wait": func() error {
			_, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(lootCorpseA), WaitMS: -5})
			return err
		},
		"take none": func() error { _, err := l.s.serveLootTake(ctx, serveLootTakeParams{All: true}); return err },
		"open spell": func() error {
			_, err := l.s.serveOpenObject(ctx, serveOpenObjectParams{GUID: GUID(lootChest), SpellID: 6477})
			return err
		},
		"open not an object": func() error {
			_, err := l.s.serveOpenObject(ctx, serveOpenObjectParams{GUID: GUID(lootCorpseA)})
			return err
		},
		"use none": func() error { _, err := l.s.serveUseObject(ctx, serveUseObjectParams{}); return err },
		"roll id": func() error {
			_, err := l.s.serveRoll(serveRollParams{RollID: 3, Choice: "need"})
			return err
		},
		"roll no choice": func() error { _, err := l.s.serveRoll(serveRollParams{}); return err },
		"roll half a name": func() error {
			_, err := l.s.serveRoll(serveRollParams{Looted: GUID(lootIDA), Choice: "need"})
			return err
		},
		"roll slot only": func() error { _, err := l.s.serveRoll(serveRollParams{ItemSlot: &zero, Choice: "need"}); return err },
	} {
		if err := call(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// With a window held, the take's own parameter checks run before any packet too.
	if res, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(lootCorpseA)}); err != nil || !res.OK {
		t.Fatalf("loot: %+v %v", res, err)
	}
	sent := len(l.sent)
	bad := 9
	for name, p := range map[string]serveLootTakeParams{
		"none":        {},
		"all and one": {All: true, Slot: &zero},
		"absent slot": {Slot: &bad},
		"wait":        {All: true, WaitMS: 60001},
	} {
		if _, err := l.s.serveLootTake(ctx, p); err == nil {
			t.Errorf("loot_take %s: no error", name)
		}
	}
	if len(l.sent) != sent {
		t.Fatalf("refused calls sent %d packets", len(l.sent)-sent)
	}
	if l.count(wire.CMSGLootUnit) != 1 {
		t.Fatalf("%d open packets: a refused call opened something", l.count(wire.CMSGLootUnit))
	}
}

func TestServeLootRefusalIsAResult(t *testing.T) {
	l := newLootWorld(t)
	l.corpse(lootCorpseA, lootIDA, 2, 0).refuse = true
	res, err := l.s.serveLoot(context.Background(), serveLootParams{GUID: GUID(lootCorpseA)})
	if err != nil {
		t.Fatalf("a refused window was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindLootError || res.Event.Text == "" || len(res.Items) != 0 {
		t.Fatalf("answer %+v", res)
	}
	if l.s.methodLoot != nil {
		t.Fatal("a refusal left a window held")
	}
}

func TestServeServerReleaseClearsTheHeldWindow(t *testing.T) {
	l := newLootWorld(t)
	l.corpse(lootCorpseA, lootIDA, 2, 0, twoItems()...)
	ctx := context.Background()
	if res, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(lootCorpseA)}); err != nil || !res.OK {
		t.Fatalf("loot: %+v %v", res, err)
	}
	l.deliver(wire.SMSGLootRelease, fakeRelease(lootCorpseA, itemSelf))
	if l.s.methodLoot != nil {
		t.Fatal("the server's release left the window held")
	}
	if _, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(lootCorpseA)}); err != nil {
		t.Fatal(err)
	}
	l.deliver(wire.SMSGLootReleaseAll, nil)
	if l.s.methodLoot != nil {
		t.Fatal("release-all left the window held")
	}
}

func TestServeOpenObjectTakeOnAChest(t *testing.T) {
	l := newLootWorld(t)
	l.chest(lootChest, lootIDChest, 2, 80, twoItems()...)
	ctx := context.Background()
	res, err := l.s.serveOpenObject(ctx, serveOpenObjectParams{GUID: GUID(lootChest), Take: true})
	if err != nil || !res.OK {
		t.Fatalf("open_object: %+v %v", res, err)
	}
	if res.Window != nil || len(res.Taken) != 2 || res.Taken[0].ItemEntry != 4583 || res.Taken[1].Count != 2 || res.Money != 80 || len(res.Left) != 0 {
		t.Fatalf("answer %+v", res)
	}
	if l.count(wire.CMSGGameObjUse) != 1 || l.count(wire.CMSGGameObjReportUse) != 1 || l.count(wire.CMSGLootUnit) != 0 {
		t.Fatal("a chest was not opened with the game object use")
	}
	if len(l.releases) != 1 || l.releases[0] != lootChest || l.s.methodLoot != nil {
		t.Fatalf("releases %v held %v", l.releases, l.s.methodLoot)
	}
	if res.Event == nil || res.Event.Kind != events.KindLootOpen {
		t.Fatalf("event %+v", res.Event)
	}

	// Without take the window stays held, and a game object window is typed as one.
	l.chest(lootFarObj, lootIDB, 3, 0, fakeItemDef{listID: 0, itemID: 9, count: 1})
	res, err = l.s.serveOpenObject(ctx, serveOpenObjectParams{GUID: GUID(lootFarObj)})
	if err != nil || !res.OK || res.Window == nil || res.Window.Type != "game_object" || len(res.Taken) != 0 || l.s.methodLoot == nil {
		t.Fatalf("open without take: %+v %v", res, err)
	}
}

func TestServeOpenObjectDoorHasNoWindow(t *testing.T) {
	l := newLootWorld(t)
	l.place(fakeGameObjectCreate(fakePacked(lootDoor), 0, 2, 0, 0, 17153, 1, 0))
	l.owners[lootDoor] = &lootOwnerDef{door: true}
	res, err := l.s.serveOpenObject(context.Background(), serveOpenObjectParams{GUID: GUID(lootDoor)})
	if err != nil || !res.OK || res.Window != nil || len(res.Taken) != 0 || res.Event == nil || res.Event.Kind != events.KindGameObject {
		t.Fatalf("door: %+v %v", res, err)
	}
	if l.s.methodLoot != nil {
		t.Fatal("a door left a window held")
	}
}

func TestServeUseObjectOutcomes(t *testing.T) {
	l := newLootWorld(t)
	l.place(fakeGameObjectCreate(fakePacked(lootDoor), 0, 2, 0, 0, 17153, 1, 0))
	l.owners[lootDoor] = &lootOwnerDef{door: true}
	ctx := context.Background()

	res, err := l.s.serveUseObject(ctx, serveUseObjectParams{GUID: GUID(lootDoor)})
	if err != nil || !res.OK || res.Outcome != "state_changed" || res.Event == nil || res.Window != nil {
		t.Fatalf("door: %+v %v", res, err)
	}
	if res.Object.GUID != GUID(lootDoor) || res.Object.TypeName != "door" || res.Object.State != 0 {
		t.Fatalf("object %+v", res.Object)
	}
	use, rep := l.index(wire.CMSGGameObjUse, 0), l.index(wire.CMSGGameObjReportUse, 0)
	if use < 0 || rep != use+1 {
		t.Fatalf("the use and its report went out at %d and %d", use, rep)
	}

	// A chest opens a window, which the call keeps.
	l.chest(lootChest, lootIDChest, 2, 5, twoItems()...)
	res, err = l.s.serveUseObject(ctx, serveUseObjectParams{GUID: GUID(lootChest)})
	if err != nil || !res.OK || res.Outcome != events.KindLootOpen || res.Window == nil || len(res.Window.Items) != 2 || l.s.methodLoot == nil {
		t.Fatalf("chest: %+v %v", res, err)
	}
	if res.Event == nil || res.Event.Kind != events.KindLootOpen {
		t.Fatalf("chest event %+v", res.Event)
	}

	// A refusal is an outcome with its event; the held window of the chest is released before the next use.
	l.chest(lootFarObj, lootIDB, 3, 0).refuse = true
	res, err = l.s.serveUseObject(ctx, serveUseObjectParams{GUID: GUID(lootFarObj)})
	if err != nil || res.OK || res.Outcome != events.KindLootError || res.Event == nil || res.Event.Kind != events.KindLootError {
		t.Fatalf("refused: %+v %v", res, err)
	}
	if len(l.releases) != 1 || l.releases[0] != lootChest {
		t.Fatalf("the held chest window was not released first: %v", l.releases)
	}
}

func TestServeUseObjectNoAnswerHintsTheDistance(t *testing.T) {
	l := newLootWorld(t)
	l.place(fakeGameObjectCreate(fakePacked(lootFarObj), 0, 30, 0, 0, 2842, 1, 3)) // not in the fake's table: silence
	res, err := l.s.serveUseObject(context.Background(), serveUseObjectParams{GUID: GUID(lootFarObj), WaitMS: 30})
	if err != nil || res.OK || res.Outcome != "none" || res.Event != nil {
		t.Fatalf("silence: %+v %v", res, err)
	}
	if !strings.Contains(res.Hint, "30.0 yd away") {
		t.Fatalf("hint %q", res.Hint)
	}
}

func TestServeGameObjectsRows(t *testing.T) {
	l := newLootWorld(t)
	l.place(fakeGameObjectCreate(fakePacked(lootFarObj), 0, 30, 0, 0, 2842, 1, 3))
	l.place(fakeGameObjectCreate(fakePacked(lootDoor), 0, 5, 0, 0, 17153, 0, 0))
	l.place(fakeGameObjectCreate(fakePacked(lootChest), 0, 2, 0, 0, 2842, 1, 3))
	l.corpse(lootCorpseA, lootIDA, 1, 0) // a unit is not a game object

	rows, err := l.s.serveGameObjects(serveGameObjectsParams{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if rows[0].GUID != GUID(lootChest) || rows[1].GUID != GUID(lootDoor) || rows[2].GUID != GUID(lootFarObj) {
		t.Fatalf("not sorted by distance: %+v", rows)
	}
	c := rows[0]
	if c.Entry != 2842 || c.Type != 3 || c.TypeName != "chest" || c.State != 1 || c.X != 2 || c.Distance != 2 || c.Distance2D != 2 {
		t.Fatalf("chest row %+v", c)
	}
	if rows[1].TypeName != "door" || rows[1].State != 0 {
		t.Fatalf("door row %+v", rows[1])
	}
	chestType := 3
	for name, tc := range map[string]struct {
		p    serveGameObjectsParams
		want int
	}{
		"within":  {serveGameObjectsParams{Within: 10}, 2},
		"radius":  {serveGameObjectsParams{Radius: 10}, 2},
		"entry":   {serveGameObjectsParams{Entry: 17153}, 1},
		"type":    {serveGameObjectsParams{Type: &chestType}, 2},
		"limit":   {serveGameObjectsParams{Limit: 1}, 1},
		"no name": {serveGameObjectsParams{Name: "zzz"}, 0},
	} {
		got, err := l.s.serveGameObjects(tc.p)
		if err != nil || len(got) != tc.want {
			t.Errorf("%s: %d rows %v, want %d", name, len(got), err, tc.want)
		}
	}
	// A huge limit sizes nothing: it answers every row without allocating for the limit.
	if got, err := l.s.serveGameObjects(serveGameObjectsParams{Limit: 1 << 62}); err != nil || len(got) != 3 {
		t.Errorf("a huge limit: %d rows %v", len(got), err)
	}
	if _, err := l.s.serveGameObjects(serveGameObjectsParams{Limit: -1}); err == nil {
		t.Error("a negative limit passed")
	}
}

// lootRolls puts two rolls into the session as the server's SMSG_START_LOOT_ROLL does.
func (l *lootWorld) startRolls(lootID wire.GUID, valid uint8, slots ...uint8) {
	for _, slot := range slots {
		l.deliver(wire.SMSGStartLootRoll, fakeStartRoll(lootID, 0, fakeItemDef{listID: slot, itemID: 700 + int32(slot), count: 1}, valid, 60000))
	}
}

func TestServeRoll(t *testing.T) {
	l := newLootWorld(t)
	l.startRolls(lootIDA, loot.FlagPass|loot.FlagGreed, 0, 1)

	// Two open rolls and no name: refused, listing both, nothing sent.
	if _, err := l.s.serveRoll(serveRollParams{Choice: "greed"}); err == nil || !strings.Contains(err.Error(), "item_slot=0") || !strings.Contains(err.Error(), "item_slot=1") {
		t.Fatalf("an ambiguous roll answered %v", err)
	}
	// A vote the roll does not allow is refused before the packet.
	one := 1
	if _, err := l.s.serveRoll(serveRollParams{Looted: GUID(lootIDA), ItemSlot: &one, Choice: "need"}); err == nil || !strings.Contains(err.Error(), "does not allow need") {
		t.Fatalf("a disallowed vote answered %v", err)
	}
	if _, err := l.s.serveRoll(serveRollParams{Looted: GUID(lootIDB), ItemSlot: &one, Choice: "greed"}); err == nil || !strings.Contains(err.Error(), "no roll") {
		t.Fatalf("an unknown roll answered %v", err)
	}
	if len(l.votes) != 0 {
		t.Fatalf("refused votes were sent: %v", l.votes)
	}

	res, err := l.s.serveRoll(serveRollParams{Looted: GUID(lootIDA), ItemSlot: &one, Choice: "greed"})
	if err != nil || res.RollID != 0 || res.Looted != GUID(lootIDA) || res.ItemSlot != 1 || res.ItemEntry != 701 || res.Choice != "greed" {
		t.Fatalf("roll: %+v %v", res, err)
	}
	if len(l.votes) != 1 || l.votes[0] != (lootFakeVote{loot: lootIDA, slot: 1, vote: loot.RollGreed}) {
		t.Fatalf("votes %v", l.votes)
	}
	// The same roll again is refused; the other one is now the only open roll, so no name is needed.
	if _, err := l.s.serveRoll(serveRollParams{Looted: GUID(lootIDA), ItemSlot: &one, Choice: "pass"}); err == nil || !strings.Contains(err.Error(), "already voted") {
		t.Fatalf("a second vote answered %v", err)
	}
	res, err = l.s.serveRoll(serveRollParams{Choice: "PASS"})
	if err != nil || res.ItemSlot != 0 || res.Choice != "pass" || len(l.votes) != 2 || l.votes[1].vote != loot.RollPass {
		t.Fatalf("the only open roll: %+v %v votes %v", res, err, l.votes)
	}
	if _, err := l.s.serveRoll(serveRollParams{Choice: "pass"}); err == nil || !strings.Contains(err.Error(), "no roll is open") {
		t.Fatalf("no open roll answered %v", err)
	}
}

// TestServeLootKeepsRollsOfOtherLoot: opening another corpse starts a fresh window, which forgets the rolls the
// loot watch holds; a roll still waiting for this client's vote must survive it.
func TestServeLootKeepsRollsOfOtherLoot(t *testing.T) {
	l := newLootWorld(t)
	l.startRolls(lootIDB, loot.FlagPass|loot.FlagGreed, 4)
	l.corpse(lootCorpseA, lootIDA, 2, 0, twoItems()...)
	if res, err := l.s.serveLoot(context.Background(), serveLootParams{GUID: GUID(lootCorpseA)}); err != nil || !res.OK {
		t.Fatalf("loot: %+v %v", res, err)
	}
	res, err := l.s.serveRoll(serveRollParams{Choice: "greed"})
	if err != nil || res.Looted != GUID(lootIDB) || res.ItemSlot != 4 {
		t.Fatalf("the earlier roll was forgotten: %+v %v", res, err)
	}
}

// TestServeTeardownReleasesAHeldWindow: a window a method left open is given back before the logout goes out, and
// a dry run only plans it.
func TestServeTeardownReleasesAHeldWindow(t *testing.T) {
	l := newLootWorld(t)
	l.corpse(lootCorpseA, lootIDA, 2, 0, twoItems()...)
	ctx := context.Background()
	if res, err := l.s.serveLoot(ctx, serveLootParams{GUID: GUID(lootCorpseA)}); err != nil || !res.OK {
		t.Fatalf("loot: %+v %v", res, err)
	}
	st := &serveState{}
	dry, err := l.s.serveTeardown(ctx, st, serveTeardownParams{GM: "keep", DryRun: true})
	if err != nil || len(l.releases) != 0 || l.s.methodLoot == nil {
		t.Fatalf("dry run: %+v %v releases %v", dry, err, l.releases)
	}
	if !slices.ContainsFunc(dry.Steps, func(s ServeTeardownStep) bool { return s.Step == "loot" && s.Status == "plan" }) {
		t.Fatalf("the dry run does not plan the release: %+v", dry.Steps)
	}

	out, err := l.s.serveTeardown(ctx, st, serveTeardownParams{GM: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(out.Steps, func(s ServeTeardownStep) bool { return s.Step == "loot" && s.Status == "ok" }) {
		t.Fatalf("no loot step: %+v", out.Steps)
	}
	rel, logout := l.index(wire.CMSGLootRelease, 0), l.index(wire.CMSGLogoutRequest, 0)
	if rel < 0 || logout < 0 || rel > logout {
		t.Fatalf("the release went out at %d, the logout request at %d", rel, logout)
	}
	if l.s.methodLoot != nil {
		t.Fatal("the window is still held after the teardown")
	}

	// No window held: no loot step at all.
	out, err = l.s.serveTeardown(ctx, st, serveTeardownParams{GM: "keep", DryRun: true})
	if err != nil || slices.ContainsFunc(out.Steps, func(s ServeTeardownStep) bool { return s.Step == "loot" }) {
		t.Fatalf("a loot step with nothing held: %+v %v", out.Steps, err)
	}
}

// TestServeLogoutReleasesAHeldWindow: the shutdown path (a signal) logs out without a teardown call, and still gives
// the held window back first.
func TestServeLogoutReleasesAHeldWindow(t *testing.T) {
	l := newLootWorld(t)
	l.corpse(lootCorpseA, lootIDA, 2, 0, twoItems()...)
	if res, err := l.s.serveLoot(context.Background(), serveLootParams{GUID: GUID(lootCorpseA)}); err != nil || !res.OK {
		t.Fatalf("loot: %+v %v", res, err)
	}
	if err := l.s.serveLogout(&serveState{}); err != nil {
		t.Fatal(err)
	}
	rel, logout := l.index(wire.CMSGLootRelease, 0), l.index(wire.CMSGLogoutRequest, 0)
	if rel < 0 || logout < 0 || rel > logout || l.s.methodLoot != nil {
		t.Fatalf("the release went out at %d, the logout request at %d, held %v", rel, logout, l.s.methodLoot)
	}
}

// TestServeLootTakeStopsWithItsContext: a server that never answers the take must not hold the call past its own
// wait_ms or past the caller's cancel (the loot waits' own budget is 4 s).
func TestServeLootTakeStopsWithItsContext(t *testing.T) {
	l := newLootWorld(t)
	l.corpse(lootCorpseA, lootIDA, 2, 0, twoItems()...)
	l.silentItems = true
	if res, err := l.s.serveLoot(context.Background(), serveLootParams{GUID: GUID(lootCorpseA)}); err != nil || !res.OK {
		t.Fatalf("loot: %+v %v", res, err)
	}
	start := time.Now()
	_, err := l.s.serveLootTake(context.Background(), serveLootTakeParams{All: true, WaitMS: 100})
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a take on a silent server answered %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("wait_ms 100 held the call for %v", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start = time.Now()
	if _, err = l.s.serveLootTake(ctx, serveLootTakeParams{All: true, WaitMS: 60000}); err == nil {
		t.Fatal("a cancelled take answered no error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the cancel was seen after %v", d)
	}
}

// TestServeUseObjectReportsAHalfSentUse: the use went out and its report did not: the call says the outcome is
// unknown instead of answering a success the server may never have credited.
func TestServeUseObjectReportsAHalfSentUse(t *testing.T) {
	l := newLootWorld(t)
	l.chest(lootChest, lootIDChest, 2, 0, twoItems()...)
	l.failCore = wire.CMSGGameObjReportUse
	res, err := l.s.serveUseObject(context.Background(), serveUseObjectParams{GUID: GUID(lootChest)})
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") || res.OK {
		t.Fatalf("a half-sent use answered %+v %v", res, err)
	}
	if l.count(wire.CMSGGameObjUse) != 1 {
		t.Fatal("the use itself was not sent")
	}
}
