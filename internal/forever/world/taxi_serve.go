package world

// The `taxi` method (flight paths for a serve caller): open a flight master's window, or take one flight to a node.
// It is the old daemon's method name, with the Forever session's shape: ForeverCore routes the flight itself, so the
// request names the destination alone (CMSG_ACTIVATE_TAXI, TaxiPackets.cpp:67-73; the handler's route is
// TaxiHandler.cpp:207-208), and there is no express opcode for a hop list. A `nodes` list in the old shape (source
// first) is accepted: its first node must be the flight master's own and its last is the destination; the hops in
// between are the server's to choose.
//
// A flight is the server's: the call returns when the flight is in the air (the character's own UNIT_FLAG_ON_TAXI is
// set), so the session stays free for `status` (its on_taxi field), `chat` and the rest while the flight goes on;
// the flight's takeoff and landing are `taxi_flight` events. wait_landing holds the call to the landing instead (the
// flight's own fifteen-minute ceiling), which keeps the session busy until then.

import (
	"context"
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/taxi"
)

// serveTaxiNodesMax bounds the old shape's `nodes` list, as the old method bounded its route.
const serveTaxiNodesMax = 64

// serveTaxiWaitDefaultMS is the default wait_ms: the window (a first visit asks twice) and the takeoff.
const serveTaxiWaitDefaultMS = 30000

// serveTaxiParams is taxi's call: the flight master, and the destination for a flight (none asks for the window).
type serveTaxiParams struct {
	GUID GUID `json:"guid"`
	// Node is the destination taxi node. Nodes is the old shape: source first, destination last.
	Node  uint32   `json:"node,omitempty"`
	Nodes []uint32 `json:"nodes,omitempty"`
	// WaitLanding holds the call until the flight lands.
	WaitLanding bool `json:"wait_landing,omitempty"`
	WaitMS      int  `json:"wait_ms,omitempty"`
}

// ServeTaxi is taxi's answer. Status is "window" (no node asked), "flying", "landed", "refused" (the server answered the request
// with a refusal) or "no_takeoff" (the request went out and the server neither refused nor started a flight within
// the wait; a late flight may still start, and status.on_taxi says so). Error says why. OnTaxi is the character's own flag as the call ends.
type ServeTaxi struct {
	Since       uint64   `json:"since"`
	OK          bool     `json:"ok"`
	Status      string   `json:"status"`
	Unit        GUID     `json:"unit"`
	CurrentNode int32    `json:"current_node,omitempty"`
	Known       []uint32 `json:"known"`
	Usable      []uint32 `json:"usable"`
	Node        uint32   `json:"node,omitempty"`
	Reply       string   `json:"reply,omitempty"`
	Error       string   `json:"error,omitempty"`
	OnTaxi      bool     `json:"on_taxi"`
	// X, Y, Z are where a flight that landed in this call put the character; Detail is the landing line.
	X      float32  `json:"x,omitempty"`
	Y      float32  `json:"y,omitempty"`
	Z      float32  `json:"z,omitempty"`
	Detail string   `json:"detail,omitempty"`
	Event  *Event   `json:"event"`
	Lines  []string `json:"lines"`
}

// destination reads the node the call flies to: node, or the last of the old shape's list, and zero for none.
func (p serveTaxiParams) destination() (uint32, error) {
	if len(p.Nodes) > serveTaxiNodesMax {
		return 0, fmt.Errorf("taxi: too many nodes (max %d)", serveTaxiNodesMax)
	}
	if len(p.Nodes) == 1 {
		return 0, errors.New("taxi: nodes needs a source and a destination node (or use node)")
	}
	node := p.Node
	if n := len(p.Nodes); n >= 2 {
		last := p.Nodes[n-1]
		if node != 0 && node != last {
			return 0, fmt.Errorf("taxi: node %d is not the last of nodes (%d)", node, last)
		}
		node = last
	}
	if p.WaitLanding && node == 0 {
		return 0, errors.New("taxi: wait_landing needs a destination node")
	}
	return node, nil
}

// serveTaxi opens the flight master's window and, for a destination, takes the flight.
func (s *session) serveTaxi(parent context.Context, p serveTaxiParams) (ServeTaxi, error) {
	node, err := p.destination()
	if err != nil {
		return ServeTaxi{}, err
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, serveTaxiWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeTaxi{}, err
	}
	defer cancel()
	if s.mover == nil {
		return ServeTaxi{}, ErrNotReady
	}
	unit, err := s.methodNPC(p.GUID)
	if err != nil {
		return ServeTaxi{}, err
	}
	if !npc.IsFlightmaster(unit.Info.NpcFlags) {
		return ServeTaxi{}, fmt.Errorf("entry %d is not a flight master (NpcFlags %#x has no flight-master bit)", unit.Info.Entry, unit.Info.NpcFlags)
	}
	if node != 0 {
		// Refused before the window's own packets: a character already in the air asks nothing of a flight master.
		if err := s.taxiMovementBlocked(); err != nil {
			return ServeTaxi{}, err
		}
	}
	res := ServeTaxi{Since: s.EventLog().Last(), Unit: GUID(unit.GUID), Known: []uint32{}, Usable: []uint32{}, Lines: []string{}}
	a := s.methodState()
	lines, refusal, err := s.methodStep(parent, ctx, a, actVerbTaxiNodes, nil)
	res.Lines = lines
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	if refusal != "" {
		res.Status, res.Error = "refused", refusal
		return res, nil
	}
	window := a.taxi
	res.OK, res.Status = true, "window"
	res.CurrentNode = window.CurrentNode
	res.Known, res.Usable = window.CanLand.Nodes(), window.CanUse.Nodes()
	res.OnTaxi = s.selfOnTaxi()
	if node == 0 {
		return res, nil
	}
	if len(p.Nodes) >= 2 && window.HasWindow && int32(p.Nodes[0]) != window.CurrentNode {
		res.OK = false
		return res, fmt.Errorf("taxi: the source node %d is not this flight master's node %d", p.Nodes[0], window.CurrentNode)
	}
	res.Node = node
	run, err := s.flightStart(ctx, a, unit, node)
	if run == nil {
		// Nothing went out: the caller's mistake, raised as the frame's precondition error.
		res.OK = false
		return res, err
	}
	if err != nil {
		if s.methodStopped(parent, err) {
			return res, sentTimeout(err, res.Since)
		}
		// A reply that is not 0 is the server's refusal; no reply and no flag is a silent server (no path, client
		// control removed): the request is out and a late flight may still start, so it is its own status.
		res.OK, res.Status, res.Error = false, "no_takeoff", err.Error()
		if r := s.taxi.reply; r != nil {
			res.Status, res.Reply = "refused", taxi.TaxiReplyName(*r)
		}
		res.Event = s.firstEventSince(res.Since, events.KindTaxiReply)
		res.OnTaxi = s.selfOnTaxi()
		return res, nil
	}
	res.Reply = taxi.TaxiReplyName(taxi.TaxiReplyOK)
	res.Status = "flying"
	res.Event = s.firstEventSince(res.Since, events.KindTaxiFlight)
	if p.WaitLanding {
		// The flight's own clock, not the call's wait_ms: a flight cannot be cut mid-air, and the movement guard
		// stays on until its landing is seen either way.
		landCtx, cancelLand := context.WithTimeout(parent, actTaxiFlightMax+actTaxiLandingWait+actTaxiWait)
		defer cancelLand()
		if !run.landed {
			if err := s.flightAwaitLanding(landCtx, run); err != nil {
				res.OnTaxi = s.selfOnTaxi()
				if s.methodStopped(parent, err) {
					return res, err
				}
				res.OK, res.Error = false, err.Error()
				return res, nil
			}
		}
		res.Detail, res.X, res.Y, res.Z = s.flightLanded(landCtx, run)
		res.Status = "landed"
	} else if run.landed {
		res.Status = "landed"
		res.Detail, res.X, res.Y, res.Z = s.flightLanded(ctx, run)
	}
	res.OnTaxi = s.selfOnTaxi()
	return res, nil
}
