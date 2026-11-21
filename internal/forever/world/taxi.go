package world

// The flight paths of round R6: the answers a flight master gives, the watcher that reads the character's own
// UNIT_FLAG_ON_TAXI, and the world summary's count of other units on a taxi flight (W2: bots fly rather than
// teleport). A flight is driven by the server: the client sends one CMSG_ACTIVATE_TAXI and nothing while the flag is
// set (FlightPathMovementGenerator.cpp:79-169), and the flag's clearing is the landing.

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/taxi"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// unitFlagOnTaxi is UNIT_FLAG_ON_TAXI (UnitDefines.h:187): the server's word that the unit is on a taxi flight and
// the client sends no movement.
const unitFlagOnTaxi uint32 = 0x00100000

// taxiWatch holds the flight master's answers since the last step cleared them. The session goroutine is the only
// reader and writer, like the other watches.
type taxiWatch struct {
	// nodes is the last SMSG_SHOW_TAXI_NODES.
	nodes *taxi.ShowTaxiNodes
	// status is the last SMSG_TAXI_NODE_STATUS.
	status *taxi.TaxiNodeStatus
	// reply is the last SMSG_ACTIVATE_TAXI_REPLY.
	reply *uint8
}

// ErrOnTaxi is a movement refused because the character is on a taxi flight: the server drives the spline, so a
// client movement packet is exactly the anticheat case the round names. Every mover checks it (objects.Mover.Refuse).
var ErrOnTaxi = errors.New("world: the character is on a taxi flight (UNIT_FLAG_ON_TAXI): no movement until it lands")

// taxiFlightState records the character's own ON_TAXI from the world model packet by packet, so a flight that sets
// and clears the flag inside one wait is still seen, and an accepted flight's landing clears it.
type taxiFlightState struct {
	// known is true once the model has held the character's flags.
	known bool
	// on is the flag's last known state.
	on bool
	// set and clear count the transitions (monotonic): a step records the counters before it asks for a flight and
	// waits for them to move, which a 50 ms poll of the flag itself would miss on a short flight.
	set, clear uint64
	// pending is true from an accepted ACTIVATE_TAXI until the flag's clearing: a flight the caller's own clock cut
	// short is still in the air, and no movement may go out while it is.
	pending bool
}

// noteSelfTaxi records the character's own flags after one update of the world model. The dispatch calls it right
// after the model has taken an update, so every packet's transition is seen, not just the state at a poll.
func (s *session) noteSelfTaxi() {
	f, ok := s.selfFlags()
	if !ok {
		return
	}
	on := f&unitFlagOnTaxi != 0
	st := &s.taxiFlight
	switch {
	case !st.known:
		st.known, st.on = true, on
		if on {
			st.set++
		}
	case on && !st.on:
		st.on = true
		st.set++
	case !on && st.on:
		st.on = false
		st.clear++
		st.pending = false
	}
}

// selfOnTaxi is the character's own ON_TAXI as the model holds it now.
func (s *session) selfOnTaxi() bool {
	f, ok := s.selfFlags()
	return ok && f&unitFlagOnTaxi != 0
}

// taxiMovementBlocked is the mover's guard: while the character's own flags carry ON_TAXI, or an accepted flight's
// landing has not been seen, no movement packet may go out. The session sets it on the mover it builds.
func (s *session) taxiMovementBlocked() error {
	if s.selfOnTaxi() {
		return ErrOnTaxi
	}
	if s.taxiFlight.pending {
		return fmt.Errorf("%w (the last flight's landing has not been seen)", ErrOnTaxi)
	}
	return nil
}

// isTaxiPacket reports the opcodes handleTaxi owns.
func isTaxiPacket(core uint32) bool {
	switch core {
	case wire.SMSGShowTaxiNodes, wire.SMSGTaxiNodeStatus, wire.SMSGActivateTaxiReply:
		return true
	}
	return false
}

// handleTaxi applies one flight-master answer. A packet this round's parser cannot consume exactly is counted and
// merges nothing.
func (s *session) handleTaxi(p worldconn.Packet) error {
	s.capturePacket(p)
	switch p.Core {
	case wire.SMSGShowTaxiNodes:
		n, err := taxi.ParseShowTaxiNodes(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.taxi.nodes = &n
		if n.HasWindow {
			s.opts.logf("world: taxi window: current node %d, %d known, %d usable",
				n.CurrentNode, n.CanLand.Count(), n.CanUse.Count())
		}
	case wire.SMSGTaxiNodeStatus:
		st, err := taxi.ParseTaxiNodeStatus(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.taxi.status = &st
		s.opts.logf("world: the flight master's node is %s", taxi.TaxiStatusName(st.Status))
	case wire.SMSGActivateTaxiReply:
		reply, err := taxi.ParseActivateTaxiReply(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.taxi.reply = &reply
		reason := taxi.TaxiReplyName(reply)
		s.opts.logf("world: taxi reply: %s", reason)
		s.event(Event{
			Kind: events.KindTaxiReply,
			Text: "taxi reply: " + reason,
			Data: map[string]any{"result": reply, "reason": reason},
		})
	}
	return nil
}

// selfFlags is the character's own UnitData flags from the world model, and whether the model holds them yet.
func (s *session) selfFlags() (uint32, bool) {
	self := s.selfOrMover()
	if self.IsZero() || s.world == nil {
		return 0, false
	}
	o, ok := s.world.Object(self)
	if !ok || !o.Info.HasUnit {
		return 0, false
	}
	return o.Info.Flags, true
}

// onTaxiUnits counts the other units whose flags carry ON_TAXI. It is the world summary's "a bot flies rather than
// teleports" check (W2): the character itself is not counted.
func (s *session) onTaxiUnits() int {
	if s.world == nil {
		return 0
	}
	self := s.selfOrMover()
	n := 0
	for _, u := range s.world.Units(s.MoverX(), s.MoverY(), s.MoverZ()) {
		if u.GUID == self {
			continue
		}
		if u.Info.HasUnit && u.Info.Flags&unitFlagOnTaxi != 0 {
			n++
		}
	}
	return n
}
