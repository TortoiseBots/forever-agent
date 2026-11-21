package world

// The flight steps against the handler-boundary fake: the fake opens the window through the flight master's gossip
// option (or answers the query), reports the node status, accepts one flight and then sets and clears the
// character's own UNIT_FLAG_ON_TAXI. The step must read the landing from that flag, and the mover must send nothing
// while it is set.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/taxi"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

const (
	flightEntry  = 61641
	taxiMenuID   = 8104
	taxiOption   = 21
	taxiSelfNode = 3
	taxiDestNode = 5
)

var flightGUID = wire.GUID{Low: 0x5150F11, High: uint64(8) << 58}

// taxiWorld is the item-world fake with one flight master in the world model and the flight master's answers.
type taxiWorld struct {
	*itemWorld
	flags    uint32
	talks    int
	selects  int
	queries  int
	statuses int
	flights  int
	refuse   uint8
	lastNode uint32
	// firstVisit makes the first hello (and the first query) answer the node's discovery, as the core does while it
	// prepares a taxi menu and learns the node.
	firstVisit bool
	// The flight's schedule: ON_TAXI comes at flagAt and clears at landAt; the landing teleport follows at
	// teleportAt (FlightPathMovementGenerator::DoFinalize clears the flag, then teleports). The fake delivers each
	// once. fast makes a short flight: the set and the clear arrive in the same tick.
	flagAt, landAt  time.Time
	flagPending     bool
	landPending     bool
	teleportAt      time.Time
	teleportPending bool
	fast            bool
	tookOff         time.Time
	// fare is what the flight master charges for the flight, taken at takeoff; purse is what the character has.
	fare, purse uint64
	// landX, landY, landZ is where the landing teleport puts the character (the destination node's position).
	landX, landY, landZ float32
	// flightDur is how long the flight stays in the air (70 s unless a test asks for a longer one).
	flightDur time.Duration
}

// newTaxiWorld plants one flight master of the given flags at the character's own position and answers its packets.
func newTaxiWorld(t *testing.T, flags uint32) *taxiWorld {
	t.Helper()
	f := newItemWorld(t)
	tw := &taxiWorld{itemWorld: f, flags: flags, fare: 120, purse: 500,
		landX: 100, landY: 200, landZ: 300, flightDur: 70 * time.Second}
	fundSelf(f, tw.purse)
	// The session's own mover, wired as the create block wires it (the taxi guard included).
	f.s.mover = &objects.Mover{GUID: itemSelf, Sender: f, ClientTime: func(time.Time) uint32 { return 0 },
		RunSpeed: 7, Refuse: f.s.taxiMovementBlocked}
	payload := fakeUnitCreateBlock(fakePackedGUID(flightGUID), 0, 0, 0, 0, 0, flightEntry, 100, 100, 5, flags)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the flight master create: %v", err)
	}
	f.world.Apply(u, f.now)
	// The character's create carries UnitData, which this fake's hand-built player block does not; one flags update
	// gives the model the unit it needs for selfFlags.
	tw.server(wire.SMSGUpdateObject, buildFlagsValues(itemSelf, 0))
	f.onSend = tw.answer
	f.onWait = tw.tick
	return tw
}

// flightObject returns the flight master's world model object.
func (tw *taxiWorld) flightObject(t *testing.T) *objects.Object {
	t.Helper()
	o, ok := tw.world.Object(flightGUID)
	if !ok {
		t.Fatal("the flight master is not in the world model")
	}
	return o
}

// state is an act state with the flight master as the current NPC.
func (tw *taxiWorld) state(t *testing.T) *actState {
	t.Helper()
	return &actState{npc: tw.flightObject(t), hasNPC: true, npcEntry: flightEntry}
}

// deliver feeds one flight-master or gossip packet through the session's own handlers.
func (tw *taxiWorld) deliver(core uint32, payload []byte) {
	tw.t.Helper()
	packet := worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}
	var err error
	switch core {
	case wire.SMSGGossipMessage, wire.SMSGGossipComplete:
		err = tw.s.handleNPC(packet)
	default:
		err = tw.s.handleTaxi(packet)
	}
	if err != nil {
		tw.t.Fatalf("%s: %v", wire.ServerName(core), err)
	}
}

// tick delivers the flight's scheduled flag changes and the landing teleport.
func (tw *taxiWorld) tick(now time.Time) {
	if tw.flagPending && !now.Before(tw.flagAt) {
		tw.flagPending = false
		tw.tookOff = now
		tw.server(wire.SMSGUpdateObject, buildFlagsValues(itemSelf, unitFlagOnTaxi))
		if tw.fast {
			// A flight short enough to start and land inside one wait: both transitions are recorded.
			tw.server(wire.SMSGUpdateObject, buildFlagsValues(itemSelf, 0))
			tw.teleportAt, tw.teleportPending = now.Add(250*time.Millisecond), true
		} else {
			tw.landPending = true
		}
	}
	if tw.landPending && !now.Before(tw.landAt) {
		tw.landPending = false
		tw.server(wire.SMSGUpdateObject, buildFlagsValues(itemSelf, 0))
		// The core's teleport follows the flag update in a later packet; this fake sends it 250 ms later so the
		// landing line has to wait for it.
		tw.teleportAt, tw.teleportPending = now.Add(250*time.Millisecond), true
	}
	if tw.teleportPending && !now.Before(tw.teleportAt) {
		tw.teleportPending = false
		packet := event{side: sideInstance, p: worldconn.Packet{Core: wire.SMSGMoveTeleport,
			Name:    wire.ServerName(wire.SMSGMoveTeleport),
			Payload: moveTeleportPayload(itemSelf, 7, tw.landX, tw.landY, tw.landZ, 1.5)}}
		if err := tw.s.handle(context.Background(), packet); err != nil {
			tw.t.Fatalf("the landing teleport: %v", err)
		}
	}
}

// moveTeleportPayload writes SMSG_MOVE_TELEPORT for the mover (MoveTeleport::Write, MovementPackets.cpp:811-833):
// the packed GUID, the sequence, the position as three floats, the facing, and three clear bits.
func moveTeleportPayload(mover wire.GUID, sequence uint32, x, y, z, o float32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, mover)
	b.WriteU32(sequence)
	b.WriteFloat32(x)
	b.WriteFloat32(y)
	b.WriteFloat32(z)
	b.WriteFloat32(o)
	b.WriteBit(false) // Transport
	b.WriteBit(false) // Vehicle
	b.WriteBit(false) // PreloadWorld
	b.FlushBits()
	return b.Bytes()
}

// answer is the fake server's read side for the flight steps.
func (tw *taxiWorld) answer(f *itemWorld, core uint32, payload []byte) {
	switch core {
	case wire.CMSGTalkToGossip:
		tw.talks++
		if tw.firstVisit && tw.talks == 1 {
			tw.deliver(wire.SMSGNewTaxiPath, newTaxiPathPayload(taxiSelfNode))
			tw.deliver(wire.SMSGTaxiNodeStatus, taxiNodeStatusPayload(flightGUID, taxi.TaxiStatusLearned))
			return
		}
		tw.deliver(wire.SMSGGossipMessage, buildGossipMessage(flightGUID, taxiMenuID, taxiOption,
			npc.GossipOptionTaxinode, "I'd like to fly."))
	case wire.CMSGGossipSelectOption:
		tw.selects++
		tw.deliver(wire.SMSGShowTaxiNodes, showTaxiNodesPayload(flightGUID, taxiSelfNode, taxiLandMask(), taxiUseMask()))
	case wire.CMSGTaxiQueryAvailableNodes:
		tw.queries++
		if g := guidOf(payload); g != flightGUID {
			f.t.Fatalf("CMSG_TAXI_QUERY_AVAILABLE_NODES names %s", g)
		}
		if tw.firstVisit && tw.queries == 1 {
			tw.deliver(wire.SMSGNewTaxiPath, newTaxiPathPayload(taxiSelfNode))
			tw.deliver(wire.SMSGTaxiNodeStatus, taxiNodeStatusPayload(flightGUID, taxi.TaxiStatusLearned))
			return
		}
		tw.deliver(wire.SMSGShowTaxiNodes, showTaxiNodesPayload(flightGUID, taxiSelfNode, taxiLandMask(), taxiUseMask()))
	case wire.CMSGTaxiNodeStatusQuery:
		tw.statuses++
		if g := guidOf(payload); g != flightGUID {
			f.t.Fatalf("CMSG_TAXI_NODE_STATUS_QUERY names %s", g)
		}
		tw.deliver(wire.SMSGTaxiNodeStatus, taxiNodeStatusPayload(flightGUID, taxi.TaxiStatusLearned))
	case wire.CMSGActivateTaxi:
		tw.flights++
		b := wire.NewReader(payload)
		if g := wire.ReadPackedGUID(b); g != flightGUID {
			f.t.Fatalf("CMSG_ACTIVATE_TAXI names %s", g)
		}
		tw.lastNode = b.ReadU32()
		ground, flying := b.ReadU32(), b.ReadU32()
		if b.Remaining() != 0 || b.Err() != nil {
			f.t.Fatalf("the flight request has %d byte(s) left: %v", b.Remaining(), b.Err())
		}
		if ground != 0 || flying != 0 {
			f.t.Fatalf("the flight request names mounts %d/%d, want 0/0", ground, flying)
		}
		if tw.refuse != taxi.TaxiReplyOK {
			tw.deliver(wire.SMSGActivateTaxiReply, activateTaxiReplyPayload(tw.refuse))
			return
		}
		tw.deliver(wire.SMSGActivateTaxiReply, activateTaxiReplyPayload(taxi.TaxiReplyOK))
		// The first leg's fare is charged at takeoff (Player::ActivateTaxiPathTo -> ModifyMoney), so the purse falls
		// before the flag arrives.
		if tw.fare <= tw.purse {
			tw.purse -= tw.fare
			coin := tw.purse
			tw.server(wire.SMSGUpdateObject, activeUpdate(nil, &coin))
		}
		tw.flagAt = f.now.Add(time.Second)
		tw.landAt = f.now.Add(tw.flightDur)
		tw.flagPending = true
	}
}

// taxiLandMask is the known-node mask: nodes 1, 3 and 5.
func taxiLandMask() taxi.TaxiMask {
	m := make(taxi.TaxiMask, 8)
	m[0] = 0x15
	return m
}

// taxiUseMask is the usable-node mask: nodes 1 and 5 (the current node 3 is not usable).
func taxiUseMask() taxi.TaxiMask {
	m := make(taxi.TaxiMask, 8)
	m[0] = 0x11
	return m
}

// showTaxiNodesPayload writes SMSG_SHOW_TAXI_NODES with the window.
func showTaxiNodesPayload(unit wire.GUID, current int32, land, use taxi.TaxiMask) []byte {
	b := wire.NewWriter()
	b.WriteBit(true)
	b.FlushBits()
	b.WriteU32(uint32(len(land) / 8))
	b.WriteU32(uint32(len(use) / 8))
	wire.WritePackedGUID(b, unit)
	b.WriteI32(current)
	b.WriteBytes(land)
	b.WriteBytes(use)
	return b.Bytes()
}

// taxiNodeStatusPayload writes SMSG_TAXI_NODE_STATUS.
func taxiNodeStatusPayload(unit wire.GUID, status uint8) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	b.WriteBits(uint64(status), 2)
	b.FlushBits()
	return b.Bytes()
}

// newTaxiPathPayload writes SMSG_NEW_TAXI_PATH: the node the server just learned.
func newTaxiPathPayload(node int32) []byte {
	b := wire.NewWriter()
	b.WriteI32(node)
	return b.Bytes()
}

// activateTaxiReplyPayload writes SMSG_ACTIVATE_TAXI_REPLY.
func activateTaxiReplyPayload(reply uint8) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(reply), 4)
	b.FlushBits()
	return b.Bytes()
}

// buildFlagsValues is one UnitData values update of UNIT_FIELD_FLAGS (retail bit 48) for guid.
func buildFlagsValues(guid wire.GUID, flags uint32) []byte {
	body := unitValuesBody(48, func(v *wire.Buffer) { v.WriteU32(flags) })
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

// TestTaxiNodesOpensThroughTheGossipOption: the window opens from the "fly" option, and the step prints the current
// node and both masks.
func TestTaxiNodesOpensThroughTheGossipOption(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
	a := tw.state(t)
	if err := tw.s.actTaxiNodes(context.Background(), a, ActStep{Verb: actVerbTaxiNodes}); err != nil {
		t.Fatalf("the taxi-nodes step: %v", err)
	}
	if tw.talks != 1 || tw.selects != 1 || tw.queries != 0 {
		t.Fatalf("the step talked %d, selected %d, queried %d", tw.talks, tw.selects, tw.queries)
	}
	if !a.hasTaxi || a.taxi == nil || !a.taxi.HasWindow || a.taxi.CurrentNode != taxiSelfNode {
		t.Fatalf("the step kept %+v", a.taxi)
	}
	want := []string{
		fmt.Sprintf("taxi current node %d", taxiSelfNode),
		"taxi known nodes: 1, 3, 5",
		"taxi usable nodes: 1, 5",
	}
	for _, line := range want {
		if !contains(joinLines(a.lines), line) {
			t.Fatalf("the step did not print %q: %q", line, a.lines)
		}
	}
	if got := lastActLine(a); !contains(got, fmt.Sprintf("entry %d, 3 known, 2 usable", flightEntry)) {
		t.Fatalf("the step line is %q", got)
	}
}

// TestTaxiNodesAsksAgainWhenTheFirstHelloLearnedTheNode: the core learns a new node while preparing the menu and
// sends no menu (Player.cpp:15036-15043), so the step asks a second time, which is a player's second click.
func TestTaxiNodesAsksAgainWhenTheFirstHelloLearnedTheNode(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
	tw.firstVisit = true
	a := tw.state(t)
	if err := tw.s.actTaxiNodes(context.Background(), a, ActStep{Verb: actVerbTaxiNodes}); err != nil {
		t.Fatalf("the taxi-nodes step: %v", err)
	}
	if tw.talks != 2 || tw.selects != 1 {
		t.Fatalf("the step talked %d, selected %d", tw.talks, tw.selects)
	}
	if !contains(joinLines(a.lines), "taxi learned the flight master's node") {
		t.Fatalf("the step did not report the learned node: %q", a.lines)
	}
	if !a.hasTaxi || a.taxi.CurrentNode != taxiSelfNode {
		t.Fatalf("the step kept %+v", a.taxi)
	}
}

// TestTaxiQueryAsksAgainWhenTheFirstQueryLearnedTheNode: the query handler returns before the menu when it learns the
// node too (TaxiHandler.cpp:84-88).
func TestTaxiQueryAsksAgainWhenTheFirstQueryLearnedTheNode(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagFlightmaster)
	tw.firstVisit = true
	a := tw.state(t)
	if err := tw.s.actTaxiNodes(context.Background(), a, ActStep{Verb: actVerbTaxiNodes}); err != nil {
		t.Fatalf("the taxi-nodes step: %v", err)
	}
	if tw.queries != 2 || tw.talks != 0 {
		t.Fatalf("the step queried %d, talked %d", tw.queries, tw.talks)
	}
	if !contains(joinLines(a.lines), "taxi learned the flight master's node") {
		t.Fatalf("the step did not report the learned node: %q", a.lines)
	}
	if !a.hasTaxi {
		t.Fatal("the step did not keep the window")
	}
}

// TestTaxiNodesWithoutGossipAsksTheQuery: a flight master with no gossip menu answers CMSG_TAXI_QUERY_AVAILABLE_NODES.
func TestTaxiNodesWithoutGossipAsksTheQuery(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagFlightmaster)
	a := tw.state(t)
	if err := tw.s.actTaxiNodes(context.Background(), a, ActStep{Verb: actVerbTaxiNodes}); err != nil {
		t.Fatalf("the taxi-nodes step: %v", err)
	}
	if tw.queries != 1 || tw.talks != 0 || tw.selects != 0 {
		t.Fatalf("the step queried %d, talked %d, selected %d", tw.queries, tw.talks, tw.selects)
	}
	if !a.hasTaxi {
		t.Fatal("the step did not keep the window")
	}
}

// TestTaxiStatusNamesTheNode: the status query's two bits are named.
func TestTaxiStatusNamesTheNode(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagFlightmaster)
	a := tw.state(t)
	if err := tw.s.actTaxiStatus(context.Background(), a, ActStep{Verb: actVerbTaxiStatus}); err != nil {
		t.Fatalf("the taxi-status step: %v", err)
	}
	if tw.statuses != 1 {
		t.Fatalf("the step sent %d status queries", tw.statuses)
	}
	if got := lastActLine(a); !contains(got, "node learned") {
		t.Fatalf("the step line is %q", got)
	}
}

// TestFlyWaitsForTheLandingAndSendsNoMovement: one flight is accepted, ON_TAXI comes and goes, and the step sends
// nothing between the request and the landing.
func TestFlyWaitsForTheLandingAndSendsNoMovement(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
	a := tw.state(t)
	if err := tw.s.actTaxiNodes(context.Background(), a, ActStep{Verb: actVerbTaxiNodes}); err != nil {
		t.Fatalf("the taxi-nodes step: %v", err)
	}
	before := len(tw.sent)
	if err := tw.s.actFly(context.Background(), a, ActStep{Verb: actVerbFly, Args: map[string]string{"node": "5"}}); err != nil {
		t.Fatalf("the fly step: %v", err)
	}
	if tw.flights != 1 || tw.lastNode != taxiDestNode {
		t.Fatalf("the step sent %d flight(s), node %d", tw.flights, tw.lastNode)
	}
	// The flight itself sends the request and, at the landing, the acknowledgement of the server's own teleport (the
	// server waits for it). No movement packet is among the client's at all.
	if len(tw.sent) != before+2 || tw.sent[before].core != wire.CMSGActivateTaxi || tw.sent[before+1].core != wire.CMSGMoveTeleportAck {
		t.Fatalf("the step sent %v", cores(tw.sent[before:]))
	}
	for _, s := range tw.sent {
		if isMovementOpcode(s.core) {
			t.Fatalf("the run sent %s during the flight", wire.ClientName(s.core))
		}
	}
	// The landing line carries the position the core's teleport put the character at, not the takeoff point, and the
	// money the fare took.
	landed := fmt.Sprintf("landed at %.1f %.1f %.1f", tw.landX, tw.landY, tw.landZ)
	if got := lastActLine(a); !contains(got, landed) || !contains(got, "after 1m9s") || !contains(got, "node 5") ||
		!contains(got, "374 yd from takeoff") || !contains(got, "500 -> 380 copper (est., 120 spent)") {
		t.Fatalf("the step line is %q", got)
	}
	if flags, ok := tw.s.selfFlags(); !ok || flags&unitFlagOnTaxi != 0 {
		t.Fatalf("the character is still on the taxi (%#x)", flags)
	}
}

// TestFlyRefusedByTheServerFails: a non-zero reply is the refusal and names its reason.
func TestFlyRefusedByTheServerFails(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
	tw.refuse = taxi.TaxiReplyNotVisited
	a := tw.state(t)
	if err := tw.s.actTaxiNodes(context.Background(), a, ActStep{Verb: actVerbTaxiNodes}); err != nil {
		t.Fatalf("the taxi-nodes step: %v", err)
	}
	err := tw.s.actFly(context.Background(), a, ActStep{Verb: actVerbFly, Args: map[string]string{"node": "5"}})
	if err == nil || !contains(err.Error(), "not_visited") {
		t.Fatalf("the fly step answered %v", err)
	}
	if got := lastActLine(a); !contains(got, "fail") || !contains(got, "not_visited") {
		t.Fatalf("the step line is %q", got)
	}
}

// TestFlyWithoutTheWindowSendsNothing: a flight names a node the window offered, so no window means no packet.
func TestFlyWithoutTheWindowSendsNothing(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagFlightmaster)
	a := tw.state(t)
	err := tw.s.actFly(context.Background(), a, ActStep{Verb: actVerbFly, Args: map[string]string{"node": "5"}})
	if err == nil || !contains(err.Error(), "no taxi window") {
		t.Fatalf("the fly step answered %v", err)
	}
	if len(tw.sent) != 0 {
		t.Fatalf("the fly step sent %v", cores(tw.sent))
	}
}

// TestFlyNodeNotInTheUsableMaskRefused: the node must be one the window marks usable, before any packet.
func TestFlyNodeNotInTheUsableMaskRefused(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
	a := tw.state(t)
	if err := tw.s.actTaxiNodes(context.Background(), a, ActStep{Verb: actVerbTaxiNodes}); err != nil {
		t.Fatalf("the taxi-nodes step: %v", err)
	}
	before := len(tw.sent)
	err := tw.s.actFly(context.Background(), a, ActStep{Verb: actVerbFly, Args: map[string]string{"node": "3"}})
	if err == nil || !contains(err.Error(), "not in the window's usable mask") {
		t.Fatalf("the fly step answered %v", err)
	}
	if len(tw.sent) != before {
		t.Fatalf("the fly step sent %v", cores(tw.sent[before:]))
	}
}

// TestTaxiNonFlightmasterRefused: without the flight-master bit the handlers answer nothing, so the step sends
// nothing.
func TestTaxiNonFlightmasterRefused(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip)
	a := tw.state(t)
	err := tw.s.actTaxiNodes(context.Background(), a, ActStep{Verb: actVerbTaxiNodes})
	if err == nil || !contains(err.Error(), "not a flight master") {
		t.Fatalf("the step answered %v", err)
	}
	if len(tw.sent) != 0 {
		t.Fatalf("the step sent %v", cores(tw.sent))
	}
}

// TestOtherUnitsOnTaxiCounted: the world summary counts the other units whose flags carry ON_TAXI, and not the
// character itself.
func TestOtherUnitsOnTaxiCounted(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagFlightmaster)
	other := wire.GUID{Low: 0xC0FFEE01, High: uint64(objects.TypeUnit) << 58}
	payload := fakeUnitCreateBlock(fakePackedGUID(other), 0, 5, 0, 0, 0, 12345, 100, 100, 5, 0)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the other unit's create: %v", err)
	}
	tw.world.Apply(u, tw.now)
	if got := tw.s.objectSummary().OnTaxi; got != 0 {
		t.Fatalf("the summary counts %d unit(s) before any flag", got)
	}
	tw.server(wire.SMSGUpdateObject, buildFlagsValues(other, unitFlagOnTaxi))
	if got := tw.s.objectSummary().OnTaxi; got != 1 {
		t.Fatalf("the summary counts %d unit(s), want 1", got)
	}
	tw.server(wire.SMSGUpdateObject, buildFlagsValues(itemSelf, unitFlagOnTaxi))
	if got := tw.s.objectSummary().OnTaxi; got != 1 {
		t.Fatalf("the summary counted the character itself (%d)", got)
	}
	tw.server(wire.SMSGUpdateObject, buildFlagsValues(other, 0))
	if got := tw.s.objectSummary().OnTaxi; got != 0 {
		t.Fatalf("the summary still counts %d unit(s) after the landing", got)
	}
}

// isMovementOpcode reports the client's own movement packets. The teleport acknowledgement is not one of them: it
// answers the server's own SMSG_MOVE_TELEPORT and carries the position the server chose.
func isMovementOpcode(core uint32) bool {
	switch core {
	case wire.CMSGMoveStartForward, wire.CMSGMoveStartBackward, wire.CMSGMoveStop,
		wire.CMSGMoveStartStrafeLeft, wire.CMSGMoveStopStrafe, wire.CMSGMoveJump,
		wire.CMSGMoveStartTurnLeft, wire.CMSGMoveStopTurn, wire.CMSGMoveSetRunMode,
		wire.CMSGMoveSetWalkMode, wire.CMSGMoveFallLand, wire.CMSGMoveStartSwim,
		wire.CMSGMoveStopSwim, wire.CMSGMoveSetFacing, wire.CMSGMoveHeartbeat,
		wire.CMSGMoveSetFacingHeartbeat:
		return true
	}
	return false
}

// mapChangeSender is the fake with a map change in the middle: the first wait that reaches at answers
// objects.ErrMapChange, the error the real session's wait gives while the character is between maps.
type mapChangeSender struct {
	*itemWorld
	at    time.Time
	fired bool
}

func (m *mapChangeSender) WaitUntil(ctx context.Context, until time.Time) error {
	if err := m.itemWorld.WaitUntil(ctx, until); err != nil {
		return err
	}
	if !m.fired && !m.itemWorld.now.Before(m.at) {
		m.fired = true
		return objects.ErrMapChange
	}
	return nil
}

// openWindow runs taxi-nodes on the fake's flight master and returns the act state.
func (tw *taxiWorld) openWindow(t *testing.T) *actState {
	t.Helper()
	a := tw.state(t)
	if err := tw.s.actTaxiNodes(context.Background(), a, ActStep{Verb: actVerbTaxiNodes}); err != nil {
		t.Fatalf("the taxi-nodes step: %v", err)
	}
	return a
}

// TestFlyShortFlightSeenInsideOneWait: a flight whose ON_TAXI setting and clearing both arrive inside one wait is
// still read as flown and landed (the step reads the transitions, not a 50 ms poll of the flag).
func TestFlyShortFlightSeenInsideOneWait(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
	tw.fast = true
	a := tw.openWindow(t)
	if err := tw.s.actFly(context.Background(), a, ActStep{Verb: actVerbFly, Args: map[string]string{"node": "5"}}); err != nil {
		t.Fatalf("the fly step: %v", err)
	}
	if tw.flights != 1 {
		t.Fatalf("the step sent %d flight(s)", tw.flights)
	}
	if got := lastActLine(a); !contains(got, "landed at") || !contains(got, "node 5") {
		t.Fatalf("the step line is %q", got)
	}
	if tw.s.taxiFlight.pending {
		t.Fatal("the flight is still pending after its landing")
	}
}

// TestFlyFollowsAMapChange: a flight across maps is a map change to the session's waits; the step must keep waiting
// through it and land, not fail with the map-change error.
func TestFlyFollowsAMapChange(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
	a := tw.openWindow(t)
	mcs := &mapChangeSender{itemWorld: tw.itemWorld, at: tw.now.Add(30 * time.Second)}
	tw.s.fightSender = mcs
	if err := tw.s.actFly(context.Background(), a, ActStep{Verb: actVerbFly, Args: map[string]string{"node": "5"}}); err != nil {
		t.Fatalf("the fly step: %v", err)
	}
	if !mcs.fired {
		t.Fatal("the fake never answered a map change, so the step was not tested across one")
	}
	if got := lastActLine(a); !contains(got, "landed at") || !contains(got, "node 5") {
		t.Fatalf("the step line is %q", got)
	}
}

// TestMovementRefusedWhileOnTaxi: with UNIT_FLAG_ON_TAXI set, every movement verb sends no packet and answers the
// reason: `goto`, `npc`'s walk, `follow`, serve's `face`, and the mover's own MoveTo (what walk_to and walk_path
// call).
func TestMovementRefusedWhileOnTaxi(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
	tw.server(wire.SMSGUpdateObject, buildFlagsValues(itemSelf, unitFlagOnTaxi))
	a := tw.state(t)
	if got := tw.s.taxiMovementBlocked(); got == nil || !contains(got.Error(), "taxi flight") {
		t.Fatalf("the guard answered %v", got)
	}
	before := len(tw.sent)
	err := tw.s.actGoto(context.Background(), a, ActStep{Verb: actVerbGoto,
		Args: map[string]string{"x": "10", "y": "0", "z": "0", "straight": "1"}})
	if err == nil || !contains(err.Error(), "taxi flight") {
		t.Fatalf("goto answered %v", err)
	}
	if got := lastActLine(a); !contains(got, "fail") || !contains(got, "taxi flight") {
		t.Fatalf("the step line is %q", got)
	}
	// `npc` would walk to a unit out of reach (the near flight master is within it, so a far one of its own entry is
	// planted for this test).
	far := wire.GUID{Low: 0x5150F12, High: uint64(8) << 58}
	payload := fakeUnitCreateBlock(fakePackedGUID(far), 0, 40, 0, 0, 0, flightEntry+1, 100, 100, 5, tw.flags)
	u, err2 := objects.ParseUpdateObject(payload)
	if err2 != nil || len(u.Blocks) == 0 {
		t.Fatalf("the far flight master's create: %v", err2)
	}
	tw.world.Apply(u, tw.now)
	err = tw.s.actNPC(context.Background(), a, ActStep{Verb: actVerbNPC,
		Args: map[string]string{"entry": fmt.Sprint(flightEntry + 1), "straight": "1"}})
	if err == nil || !contains(err.Error(), "taxi flight") {
		t.Fatalf("npc answered %v", err)
	}
	err = tw.s.actFollow(context.Background(), a, ActStep{Verb: actVerbFollow,
		Args: map[string]string{"lead": "Someone", "dur": "1s"}})
	if err == nil || !contains(err.Error(), "taxi flight") {
		t.Fatalf("follow answered %v", err)
	}
	if err := tw.s.serveFace(tw.flightObject(t)); err == nil || !contains(err.Error(), "taxi flight") {
		t.Fatalf("serve face answered %v", err)
	}
	if err := tw.s.mover.MoveTo(context.Background(), 10, 0, 0); err == nil || !contains(err.Error(), "taxi flight") {
		t.Fatalf("the mover answered %v", err)
	}
	if err := tw.s.mover.WalkPath(context.Background(), [][3]float32{{10, 0, 0}}); err == nil || !contains(err.Error(), "taxi flight") {
		t.Fatalf("the mover's walk path answered %v", err)
	}
	if err := tw.s.mover.Stop(); err == nil || !contains(err.Error(), "taxi flight") {
		t.Fatalf("the mover's stop answered %v", err)
	}
	if len(tw.sent) != before {
		t.Fatalf("a refused verb sent %v", cores(tw.sent[before:]))
	}
	for _, s := range tw.sent {
		if isMovementOpcode(s.core) {
			t.Fatalf("the run sent %s while on the taxi", wire.ClientName(s.core))
		}
	}
}

// TestFlyCutByTheDeadlineBlocksMovement: a flight whose wait the run's context cancels leaves the character in the
// air; a later goto sends no movement and says why, and the landing is what releases the guard.
func TestFlyCutByTheDeadlineBlocksMovement(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
	a := tw.openWindow(t)
	ctx, cancel := context.WithCancel(context.Background())
	tw.onWait = func(now time.Time) {
		tw.tick(now)
		if !tw.tookOff.IsZero() {
			cancel()
		}
	}
	err := tw.s.actFly(ctx, a, ActStep{Verb: actVerbFly, Args: map[string]string{"node": "5"}})
	if err == nil {
		t.Fatal("a cancelled flight succeeded")
	}
	if !tw.s.taxiFlight.pending {
		t.Fatal("the cut flight is not pending")
	}
	// The landing is not in yet (the flight's schedule still holds it), so movement is refused.
	before := len(tw.sent)
	err = tw.s.actGoto(context.Background(), a, ActStep{Verb: actVerbGoto,
		Args: map[string]string{"x": "10", "y": "0", "z": "0", "straight": "1"}})
	if err == nil || !contains(err.Error(), "taxi flight") {
		t.Fatalf("goto answered %v", err)
	}
	if len(tw.sent) != before {
		t.Fatalf("goto sent %v", cores(tw.sent[before:]))
	}
	// The landing (flag clear, then the teleport) releases it: the schedule still holds both, and the fake's clock
	// moves on.
	tw.onWait = tw.tick
	for i := 0; tw.s.taxiFlight.pending && i < 120; i++ {
		tw.now = tw.now.Add(time.Second)
		tw.tick(tw.now)
	}
	if tw.s.taxiFlight.pending {
		t.Fatal("the landing never released the flight")
	}
	if err := tw.s.taxiMovementBlocked(); err != nil {
		t.Fatalf("the guard still refuses after the landing: %v", err)
	}
}

// TestFlyWithoutMoneySendsNothing: the fare is the server's data, but a character with no copper at all is refused
// before CMSG_ACTIVATE_TAXI.
func TestFlyWithoutMoneySendsNothing(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
	a := tw.openWindow(t)
	fundSelf(tw.itemWorld, 0)
	before := len(tw.sent)
	err := tw.s.actFly(context.Background(), a, ActStep{Verb: actVerbFly, Args: map[string]string{"node": "5"}})
	if err == nil || !contains(err.Error(), "no copper") {
		t.Fatalf("the fly step answered %v", err)
	}
	if len(tw.sent) != before {
		t.Fatalf("the fly step sent %v", cores(tw.sent[before:]))
	}
}

// TestFlyWindowChecksRefuseBeforeSending: the window must be this flight master's, the node must be in both masks,
// and a window without its window block cannot be used.
func TestFlyWindowChecksRefuseBeforeSending(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(tw *taxiWorld, a *actState)
		want   string
	}{
		{"another master", func(tw *taxiWorld, a *actState) {
			a.taxi.WindowUnit = wire.GUID{Low: 0xBEEF, High: uint64(8) << 58}
		}, "another flight master's"},
		{"not in the known mask", func(tw *taxiWorld, a *actState) {
			a.taxi.CanLand = make(taxi.TaxiMask, 8)
			a.taxi.CanUse[0] = 0x11
		}, "known mask"},
		{"no window block", func(tw *taxiWorld, a *actState) { a.taxi.HasWindow = false }, "no window block"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
			a := tw.openWindow(t)
			c.mutate(tw, a)
			before := len(tw.sent)
			err := tw.s.actFly(context.Background(), a, ActStep{Verb: actVerbFly, Args: map[string]string{"node": "5"}})
			if err == nil || !contains(err.Error(), c.want) {
				t.Fatalf("the fly step answered %v", err)
			}
			if len(tw.sent) != before {
				t.Fatalf("the fly step sent %v", cores(tw.sent[before:]))
			}
		})
	}
}

// TestServeActCoversAFlight: a serve act script with a flight gets the flight's own budget, and a flight longer
// than both the method's old 60 s default and its old ten-minute cap runs to its landing. A caller's 60 s would cut
// the flight mid-air, so it is refused before anything is sent.
func TestServeActCoversAFlight(t *testing.T) {
	steps := []string{fmt.Sprintf("npc entry=%d", flightEntry), "taxi-nodes", "fly node=5"}
	tw := newTaxiWorld(t, npc.NPCFlagGossip|npc.NPCFlagFlightmaster)
	tw.flightDur = 11 * time.Minute
	before := len(tw.sent)
	_, err := tw.s.serveAct(context.Background(), serveActParams{Steps: steps, TimeoutMS: 60000})
	if err == nil || !contains(err.Error(), "flight") {
		t.Fatalf("a 60 s budget for a flying script answered %v", err)
	}
	if len(tw.sent) != before {
		t.Fatalf("the refused script sent %v", cores(tw.sent[before:]))
	}
	res, err := tw.s.serveAct(context.Background(), serveActParams{Steps: steps})
	if err != nil {
		t.Fatalf("the serve act: %v", err)
	}
	if !res.OK {
		t.Fatalf("the flying script failed: %v", res.Lines)
	}
	if !contains(strings.Join(res.Lines, "\n"), "landed at") {
		t.Fatalf("the script did not land: %v", res.Lines)
	}
}

// TestServeActFlightBudget: the budget a script with a fly step earns covers the flight's own ceiling, and the
// baseline `forever act` gives it covers a login on top.
func TestServeActFlightBudget(t *testing.T) {
	steps := []ActStep{{Verb: actVerbFly, Args: map[string]string{"node": "5"}}}
	if got := serveActFlightBudget(steps); got < actTaxiFlightMax {
		t.Fatalf("serve's flight budget is %s, below the flight ceiling %s", got, actTaxiFlightMax)
	}
	if got := ActBudget(steps, 3*time.Second); got < actTaxiFlightMax+actRunBaseline {
		t.Fatalf("forever act's deadline is %s, below the flight ceiling plus the baseline", got)
	}
	if got := ActBudget(nil, 3*time.Second); got != actRunBaseline+3*time.Second {
		t.Fatalf("a script without a flight gets %s, want the baseline and the settle", got)
	}
}
