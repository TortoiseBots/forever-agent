package world

// The battleground steps against the handler-boundary fake: a battlemaster answers the hello with its list, the
// join answers the queued status, and the port and leave answer their own statuses.
//
// The failing case is TestBGEmptyListStopsTheRound: an empty list from a battlemaster stops the round with its
// report instead of queueing — a runner that joins without probing sends a join the server never answers.

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/bg"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

const bgTestEntry = 42441

var bgNPCGUID = wire.GUID{Low: 0xE0E0E0E0, High: uint64(objects.TypeUnit) << 58}

// bgWorld is the item-world fake with one battlemaster in the world model and the queue answers.
type bgWorld struct {
	*itemWorld
	// emptyList makes the hello answer an empty list (no support).
	emptyList bool
	// failReason, when not nil, is the reason the join answers as failed.
	failReason *uint32
	// invite makes the join answer need-confirmation instead of queued.
	invite bool
	// noAnswer, when set, makes the fake swallow one client opcode.
	noAnswer map[uint32]bool
}

// newBGWorld plants one battlemaster creature at the character's own position (in reach) and answers the queue
// packets through v.answer.
func newBGWorld(t *testing.T, flags uint32) *bgWorld {
	t.Helper()
	v := &bgWorld{itemWorld: newItemWorld(t), noAnswer: map[uint32]bool{}}
	payload := fakeUnitCreateBlock(fakePackedGUID(bgNPCGUID), 0, 0, 0, 0, 0, bgTestEntry, 100, 100, 5, flags)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the battlemaster create: %v", err)
	}
	v.world.Apply(u, v.now)
	if _, ok := v.world.Object(bgNPCGUID); !ok {
		t.Fatalf("the battlemaster is not in the world model")
	}
	v.onSend = v.answer
	return v
}

// battlemaster returns the battlemaster's model object.
func (v *bgWorld) battlemaster(t *testing.T) *objects.Object {
	t.Helper()
	o, ok := v.world.Object(bgNPCGUID)
	if !ok {
		t.Fatalf("the battlemaster is not in the world model")
	}
	return o
}

// state is an act state with the battlemaster as the current NPC.
func (v *bgWorld) state(t *testing.T) *actState {
	t.Helper()
	return &actState{npc: v.battlemaster(t), npcEntry: bgTestEntry, hasNPC: true}
}

// deliver feeds one server packet through the session's own handler.
func (v *bgWorld) deliver(core uint32, payload []byte) {
	v.t.Helper()
	var err error
	switch core {
	case wire.SMSGBattlefieldList, wire.SMSGBattlefieldStatusNeedConfirmation, wire.SMSGBattlefieldStatusActive,
		wire.SMSGBattlefieldStatusQueued, wire.SMSGBattlefieldStatusNone, wire.SMSGBattlefieldStatusFailed:
		err = v.s.handleBG(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload})
	default:
		v.t.Fatalf("the fake has no bg answer for %s", wire.ServerName(core))
	}
	if err != nil {
		v.t.Fatalf("handleBG: %v", err)
	}
}

// answer is the fake's read side.
func (v *bgWorld) answer(_ *itemWorld, core uint32, payload []byte) {
	v.t.Helper()
	if v.noAnswer[core] {
		return
	}
	switch core {
	case wire.CMSGBattlemasterHello:
		v.deliver(wire.SMSGBattlefieldList, v.listPayload())
	case wire.CMSGBattlemasterJoin:
		if v.failReason != nil {
			v.deliver(wire.SMSGBattlefieldStatusFailed, v.failedPayload(*v.failReason))
			return
		}
		if v.invite {
			v.deliver(wire.SMSGBattlefieldStatusNeedConfirmation, v.needConfirmPayload())
			return
		}
		v.deliver(wire.SMSGBattlefieldStatusQueued, v.queuedPayload())
	case wire.CMSGBattlefieldPort:
		b := wire.NewReader(payload)
		_, _ = readTestTicket(b)
		r := wire.NewReader(payload)
		_ = wire.ReadPackedGUID(r)
		_ = r.ReadU32()
		_ = r.ReadU32()
		_ = r.ReadU64()
		_ = r.ReadBit()
		r.FlushBits()
		accept := r.ReadBit()
		if accept {
			v.deliver(wire.SMSGBattlefieldStatusActive, v.activePayload())
		} else {
			v.deliver(wire.SMSGBattlefieldStatusNone, v.nonePayload())
		}
	case wire.CMSGBattlefieldLeave:
		v.deliver(wire.SMSGBattlefieldStatusNone, v.nonePayload())
	}
}

// ticketPayload writes the fixed test ticket (the character's own GUID, slot 3, battlegrounds kind).
func (v *bgWorld) ticketPayload(b *wire.Buffer) {
	wire.WritePackedGUID(b, itemSelf)
	b.WriteU32(3)
	b.WriteU32(uint32(bg.RideBattlegrounds))
	b.WriteU64(0x11223344)
	b.WriteBit(false)
	b.FlushBits()
}

// headerPayload writes the fixed test header around one queue id.
func (v *bgWorld) headerPayload(b *wire.Buffer, queues []uint64) {
	v.ticketPayload(b)
	b.WriteU32(uint32(len(queues)))
	b.WriteU8(10)
	b.WriteU8(60)
	b.WriteU8(2)
	b.WriteU32(0)
	for _, q := range queues {
		b.WriteU64(q)
	}
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
}

// listPayload writes the fake's SMSG_BATTLEFIELD_LIST: a supporting list, or an empty one for the probe.
func (v *bgWorld) listPayload() []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, bgNPCGUID)
	if v.emptyList {
		b.WriteI32(0)
		b.WriteU8(0)
		b.WriteU8(0)
		b.WriteU32(0)
		b.WriteBit(false)
		b.WriteBit(false)
		b.FlushBits()
		return b.Bytes()
	}
	b.WriteI32(2)
	b.WriteU8(10)
	b.WriteU8(60)
	b.WriteU32(0)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// queuedPayload writes one queued status around the test queue id.
func (v *bgWorld) queuedPayload() []byte {
	b := wire.NewWriter()
	b.WriteU32(30)
	v.headerPayload(b, []uint64{0x1F10000000000002})
	b.WriteU32(12)
	b.WriteI32(0)
	b.WriteBit(false)
	b.WriteBit(true)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// needConfirmPayload writes one need-confirmation status.
func (v *bgWorld) needConfirmPayload() []byte {
	b := wire.NewWriter()
	b.WriteU32(60)
	b.WriteU32(489)
	v.headerPayload(b, []uint64{0x1F10000000000002})
	b.WriteU8(0)
	return b.Bytes()
}

// activePayload writes one active status.
func (v *bgWorld) activePayload() []byte {
	b := wire.NewWriter()
	v.headerPayload(b, []uint64{0x1F10000000000002})
	b.WriteU32(489)
	b.WriteU32(0)
	b.WriteU32(300)
	b.WriteU8(0xFE)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// nonePayload writes one none status.
func (v *bgWorld) nonePayload() []byte {
	b := wire.NewWriter()
	v.ticketPayload(b)
	return b.Bytes()
}

// failedPayload writes one failed status with the reason.
func (v *bgWorld) failedPayload(reason uint32) []byte {
	b := wire.NewWriter()
	v.ticketPayload(b)
	b.WriteU64(0x1F10000000000002)
	b.WriteU32(reason)
	wire.WritePackedGUID(b, itemSelf)
	return b.Bytes()
}

// readTestTicket reads the ticket the step sent, for the port-order test.
func readTestTicket(b *wire.Buffer) (bg.Ticket, bool) {
	_ = b
	return bg.Ticket{}, true
}

// clientSends counts the packets of one client opcode.
func (v *bgWorld) clientSends(core uint32) int {
	n := 0
	for _, s := range v.sent {
		if s.core == core {
			n++
		}
	}
	return n
}

// TestBGEmptyListStopsTheRound is the round's failing case: an empty list from a battlemaster stops the round with
// its report instead of queueing. A runner that joins without probing sends a join the server never answers.
func TestBGEmptyListStopsTheRound(t *testing.T) {
	v := newBGWorld(t, npc.NPCFlagBattlemaster)
	v.emptyList = true
	a := v.state(t)
	if err := v.s.actBGQueue(v.s.ctx, a, mustItemStep(t, "bg-queue")); err != nil {
		t.Fatalf("bg-queue on an empty list: %v", err)
	}
	if n := v.clientSends(wire.CMSGBattlemasterJoin); n != 0 {
		t.Fatalf("%d join packet(s) went out with no support", n)
	}
	if !hasText(a.lines, "no support") {
		t.Fatalf("the probe line misses the no-support report:\n%s", joinLines(a.lines))
	}
}

// TestBGQueueNeedsABattlemaster: a creature without the flag is refused before any packet.
func TestBGQueueNeedsABattlemaster(t *testing.T) {
	v := newBGWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.s.actBGQueue(v.s.ctx, a, mustItemStep(t, "bg-queue")); err == nil {
		t.Fatal("bg-queue passed against a non-battlemaster")
	} else if !strings.Contains(err.Error(), "not a battlemaster") {
		t.Fatalf("bg-queue failed with %q, want the flag refusal", err)
	}
	if n := v.clientSends(wire.CMSGBattlemasterHello); n != 0 {
		t.Fatalf("%d hello packet(s) went out against a non-battlemaster", n)
	}
}

// TestBGQueueJoinsAndNamesTheQueue: the hello names the master, the join names the list's packed queue id, and the
// queued status ends the step.
func TestBGQueueJoinsAndNamesTheQueue(t *testing.T) {
	v := newBGWorld(t, npc.NPCFlagBattlemaster)
	a := v.state(t)
	if err := v.s.actBGQueue(v.s.ctx, a, mustItemStep(t, "bg-queue")); err != nil {
		t.Fatalf("bg-queue: %v", err)
	}
	if n := v.clientSends(wire.CMSGBattlemasterHello); n != 1 {
		t.Fatalf("%d hello packet(s), want 1", n)
	}
	if n := v.clientSends(wire.CMSGBattlemasterJoin); n != 1 {
		t.Fatalf("%d join packet(s), want 1", n)
	}
	join := v.sent[len(v.sent)-1].payload
	b := wire.NewReader(join)
	if got := b.ReadU32(); got != 1 {
		t.Fatalf("the join names %d queue(s), want 1", got)
	}
	_ = b.ReadU8()
	_ = b.ReadI32()
	_ = b.ReadI32()
	if got := b.ReadU64(); got != 0x1F10000000000002 {
		t.Fatalf("the join names queue %#x, want 0x1f10000000000002", got)
	}
	if !hasText(a.lines, "queued for list 2") {
		t.Fatalf("the queue line misses the list:\n%s", joinLines(a.lines))
	}
}

// TestBGQueueSilentListFails: a battlemaster that answers nothing fails the step with the silence.
func TestBGQueueSilentListFails(t *testing.T) {
	v := newBGWorld(t, npc.NPCFlagBattlemaster)
	v.noAnswer[wire.CMSGBattlemasterHello] = true
	a := v.state(t)
	if err := v.s.actBGQueue(v.s.ctx, a, mustItemStep(t, "bg-queue")); err == nil {
		t.Fatal("bg-queue passed with no list")
	} else if !strings.Contains(err.Error(), "sent no list") {
		t.Fatalf("bg-queue failed with %q, want the silence", err)
	}
}

// TestBGQueueRefusalNamesTheReason: a failed status fails the step with the server's reason.
func TestBGQueueRefusalNamesTheReason(t *testing.T) {
	v := newBGWorld(t, npc.NPCFlagBattlemaster)
	reason := uint32(10)
	v.failReason = &reason
	a := v.state(t)
	if err := v.s.actBGQueue(v.s.ctx, a, mustItemStep(t, "bg-queue")); err == nil {
		t.Fatal("bg-queue passed on a refusal")
	} else if !strings.Contains(err.Error(), "range index") {
		t.Fatalf("bg-queue failed with %q, want the server's reason", err)
	}
}

// TestBGQueueInviteReadsReady: when the server invites at once, the step reports the invite's map.
func TestBGQueueInviteReadsReady(t *testing.T) {
	v := newBGWorld(t, npc.NPCFlagBattlemaster)
	v.invite = true
	a := v.state(t)
	if err := v.s.actBGQueue(v.s.ctx, a, mustItemStep(t, "bg-queue")); err != nil {
		t.Fatalf("bg-queue: %v", err)
	}
	if !hasText(a.lines, "invite is ready on map 489") {
		t.Fatalf("the queue line misses the invite:\n%s", joinLines(a.lines))
	}
}

// TestBGAcceptPortsOnTheInvite: the port carries the accept bit, and the active status ends the step.
func TestBGAcceptPortsOnTheInvite(t *testing.T) {
	v := newBGWorld(t, npc.NPCFlagBattlemaster)
	v.invite = true
	a := v.state(t)
	if err := v.s.actBGQueue(v.s.ctx, a, mustItemStep(t, "bg-queue")); err != nil {
		t.Fatalf("bg-queue: %v", err)
	}
	if err := v.s.actBGAccept(v.s.ctx, a, mustItemStep(t, "bg-accept")); err != nil {
		t.Fatalf("bg-accept: %v", err)
	}
	if n := v.clientSends(wire.CMSGBattlefieldPort); n != 1 {
		t.Fatalf("%d port packet(s), want 1", n)
	}
	if !hasText(a.lines, "inside the battleground on map 489") {
		t.Fatalf("the accept line misses the map:\n%s", joinLines(a.lines))
	}
}

// TestBGAcceptWithoutInviteRefuses: with no need-confirmation the step refuses before any packet.
func TestBGAcceptWithoutInviteRefuses(t *testing.T) {
	v := newBGWorld(t, npc.NPCFlagBattlemaster)
	a := v.state(t)
	if err := v.s.actBGQueue(v.s.ctx, a, mustItemStep(t, "bg-queue")); err != nil {
		t.Fatalf("bg-queue: %v", err)
	}
	if err := v.s.actBGAccept(v.s.ctx, a, mustItemStep(t, "bg-accept")); err == nil {
		t.Fatal("bg-accept passed with no invite")
	}
}

// TestBGLeaveDeclinesTheInvite: declining ports with a clear accept bit and the none status ends the step.
func TestBGLeaveDeclinesTheInvite(t *testing.T) {
	v := newBGWorld(t, npc.NPCFlagBattlemaster)
	v.invite = true
	a := v.state(t)
	if err := v.s.actBGQueue(v.s.ctx, a, mustItemStep(t, "bg-queue")); err != nil {
		t.Fatalf("bg-queue: %v", err)
	}
	if err := v.s.actBGLeave(v.s.ctx, a, mustItemStep(t, "bg-leave")); err != nil {
		t.Fatalf("bg-leave: %v", err)
	}
	if n := v.clientSends(wire.CMSGBattlefieldPort); n != 1 {
		t.Fatalf("%d port packet(s), want 1 (the decline)", n)
	}
	if !hasText(a.lines, "invite declined") {
		t.Fatalf("the leave line misses the decline:\n%s", joinLines(a.lines))
	}
}

// TestBGLeaveSendsTheLeave: queued with no invite, the step sends the explicit leave.
func TestBGLeaveSendsTheLeave(t *testing.T) {
	v := newBGWorld(t, npc.NPCFlagBattlemaster)
	a := v.state(t)
	if err := v.s.actBGQueue(v.s.ctx, a, mustItemStep(t, "bg-queue")); err != nil {
		t.Fatalf("bg-queue: %v", err)
	}
	if err := v.s.actBGLeave(v.s.ctx, a, mustItemStep(t, "bg-leave")); err != nil {
		t.Fatalf("bg-leave: %v", err)
	}
	if n := v.clientSends(wire.CMSGBattlefieldLeave); n != 1 {
		t.Fatalf("%d leave packet(s), want 1", n)
	}
	if !hasText(a.lines, "out of the battleground queue") {
		t.Fatalf("the leave line misses the report:\n%s", joinLines(a.lines))
	}
}
