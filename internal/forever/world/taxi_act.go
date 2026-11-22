package world

// The flight paths of round R6 as act steps: taxi-nodes (open the window), taxi-status (the flight master's own node)
// and fly (take one flight and watch it land). The flow is the one the recorded flight uses: talk to the flight
// master and select its taxi option, read SMSG_SHOW_TAXI_NODES, then send one CMSG_ACTIVATE_TAXI. In flight the
// mover sends nothing: the server drives the spline, and the character's own UNIT_FLAG_ON_TAXI marks the flight and
// its clearing the landing (FlightPathMovementGenerator.cpp:79-169).

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/taxi"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// actTaxiWait bounds one flight master answer: the window or the node status.
	actTaxiWait = 5 * time.Second
	// actTaxiFlightMax bounds one flight: fifteen minutes, then the step reports the character still on the taxi
	// rather than waiting for a landing that will not come.
	actTaxiFlightMax = 15 * time.Minute
	// actTaxiNodeListMax bounds one printed node list line: the act answer's own line cap is 512 bytes.
	actTaxiNodeListMax = 40
	// actTaxiLandingWait is how long the landing line waits for the core's own teleport after the flag clears:
	// FlightPathMovementGenerator::DoFinalize clears ON_TAXI and then teleport's the player to the node, and the
	// mover only moves when that SMSG_MOVE_TELEPORT is processed.
	actTaxiLandingWait = 2 * time.Second
	// actFlightStepBudget is one fly step at its worst: the flight's own ceiling, the takeoff answer wait and the
	// landing's teleport wait. A run's deadline grows by this per fly step, so its clock never cuts a flight
	// mid-air.
	actFlightStepBudget = actTaxiFlightMax + 2*actTaxiWait + actTaxiLandingWait
)

// taxiTarget checks the current NPC is a flight master within reach.
func (s *session) taxiTarget(a *actState, verb string) (*objects.Object, error) {
	if !a.hasNPC || a.npc == nil {
		return nil, fmt.Errorf("%s: no current NPC (an npc step comes first)", verb)
	}
	now := s.transport().Now()
	nx, ny, nz := a.npc.Position(now)
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), nx, ny, nz); dist > actInteractReach {
		return nil, fmt.Errorf("%s: the NPC is %.1f yd away (walk closer first)", verb, dist)
	}
	if !npc.IsFlightmaster(a.npc.Info.NpcFlags) {
		return nil, fmt.Errorf("%s: entry %d is not a flight master (NpcFlags %#x has no flight-master bit)",
			verb, a.npc.Info.Entry, a.npc.Info.NpcFlags)
	}
	return a.npc, nil
}

// taxiOpen opens the flight master's window: its taxi gossip option when the NPC has a gossip menu (the Classic
// flow), otherwise CMSG_TAXI_QUERY_AVAILABLE_NODES, which the handler answers with the same window
// (TaxiHandler.cpp:72-116).
func (s *session) taxiOpen(ctx context.Context, a *actState, unit *objects.Object) (*taxi.ShowTaxiNodes, error) {
	guid := unit.GUID
	if err := s.transport().Send(wire.CMSGSetSelection, combat.BuildSetSelection(guid)); err != nil {
		return nil, fmt.Errorf("CMSG_SET_SELECTION: %w", err)
	}
	s.taxi = taxiWatch{}
	s.actMenu, s.actQuest = nil, nil
	selectOption := false
	if unit.Info.NpcFlags&npc.NPCFlagGossip != 0 {
		menu, learned, err := s.taxiGossipMenu(ctx, guid)
		if err != nil {
			return nil, err
		}
		if learned {
			s.actLine(a, "taxi learned the flight master's node")
		}
		option, ok := menu.OptionNPC(npc.GossipOptionTaxinode)
		if ok {
			selectOption = true
			if err := s.transport().Send(wire.CMSGGossipSelectOption,
				npc.BuildGossipSelectOption(guid, menu.MenuID, option.ID, "")); err != nil {
				return nil, fmt.Errorf("CMSG_GOSSIP_SELECT_OPTION: %w", err)
			}
		}
	}
	if !selectOption {
		// A flight master without a gossip menu (or without the taxi option in it) answers the query itself; the
		// handler checks the same flag and interaction range.
		learned, err := s.taxiQueryWindow(ctx, guid)
		if err != nil {
			return nil, err
		}
		if learned {
			s.actLine(a, "taxi learned the flight master's node")
		}
	}
	ok, err := s.actUntil(ctx, actTaxiWait, func() bool { return s.taxi.nodes != nil })
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no taxi window within %s", actTaxiWait)
	}
	window := *s.taxi.nodes
	if window.HasWindow && window.WindowUnit != guid {
		return nil, fmt.Errorf("the window came from another flight master")
	}
	return &window, nil
}

// taxiGossipMenu asks the flight master for its gossip menu, asking twice when the first hello answered the node's
// discovery instead. The core learns the node while it prepares a menu that carries a taxi option and returns before
// sending the menu (Player.cpp:15036-15043), so a first visit's hello is the "flight point learned" message with no
// menu, and a player's second click is the second hello. learned reports that the first hello was the discovery.
func (s *session) taxiGossipMenu(ctx context.Context, guid wire.GUID) (menu npc.GossipMessage, learned bool, err error) {
	for attempt := range 2 {
		s.actMenu, s.actQuest = nil, nil
		if err := s.transport().Send(wire.CMSGTalkToGossip, npc.BuildTalkToGossip(guid)); err != nil {
			return npc.GossipMessage{}, false, fmt.Errorf("CMSG_TALK_TO_GOSSIP: %w", err)
		}
		found, waitErr := s.actUntil(ctx, actMenuWait, func() bool {
			if m := s.actMenu; m != nil && m.Unit == guid {
				return true
			}
			return s.taxi.status != nil && s.taxi.status.Unit == guid
		})
		if waitErr != nil {
			return npc.GossipMessage{}, false, waitErr
		}
		if m := s.actMenu; m != nil && m.Unit == guid {
			s.actMenu = nil
			return *m, attempt == 1, nil
		}
		if !found {
			return npc.GossipMessage{}, false, fmt.Errorf("no gossip menu within %s", actMenuWait)
		}
		// The answer was the node's discovery, not a menu: the second hello (a player's second click) gets the menu.
		s.taxi.status = nil
	}
	return npc.GossipMessage{}, false, fmt.Errorf("no gossip menu after the node was learned")
}

// taxiQueryWindow asks for the window, asking twice when the first query answered the node's discovery instead: the
// handler learns the node and returns without the menu when it was new (TaxiHandler.cpp:84-88). learned reports the
// discovery. It leaves the window in the watch for the caller's own wait.
func (s *session) taxiQueryWindow(ctx context.Context, guid wire.GUID) (learned bool, err error) {
	for attempt := range 2 {
		if err := s.transport().Send(wire.CMSGTaxiQueryAvailableNodes, taxi.BuildTaxiQueryAvailableNodes(guid)); err != nil {
			return false, fmt.Errorf("CMSG_TAXI_QUERY_AVAILABLE_NODES: %w", err)
		}
		found, waitErr := s.actUntil(ctx, actTaxiWait, func() bool {
			if s.taxi.nodes != nil {
				return true
			}
			return s.taxi.status != nil && s.taxi.status.Unit == guid
		})
		if waitErr != nil {
			return false, waitErr
		}
		if s.taxi.nodes != nil {
			return attempt == 1, nil
		}
		if !found {
			return false, fmt.Errorf("no taxi window within %s", actTaxiWait)
		}
		s.taxi.status = nil
	}
	return false, fmt.Errorf("no taxi window after the node was learned")
}

// nodeListText prints node ids on one line, bounded; the count says how many were left out.
func nodeListText(nodes []uint32) string {
	var b strings.Builder
	for i, node := range nodes {
		if i == actTaxiNodeListMax {
			fmt.Fprintf(&b, " (+%d more)", len(nodes)-i)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.FormatUint(uint64(node), 10))
	}
	if b.Len() == 0 {
		return "none"
	}
	return b.String()
}

// actTaxiNodes opens the current flight master and prints its window: the current node and both masks' node ids.
func (s *session) actTaxiNodes(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.taxiTarget(a, actVerbTaxiNodes)
	if err != nil {
		return s.actFail(a, step, err)
	}
	window, err := s.taxiOpen(ctx, a, unit)
	if err != nil {
		return s.actFail(a, step, err)
	}
	a.taxi, a.hasTaxi = window, true
	if window.HasWindow {
		s.actLine(a, "taxi current node %d", window.CurrentNode)
	}
	s.actLine(a, "taxi known nodes: %s", nodeListText(window.CanLand.Nodes()))
	s.actLine(a, "taxi usable nodes: %s", nodeListText(window.CanUse.Nodes()))
	s.actOK(a, step, fmt.Sprintf("entry %d, %d known, %d usable", unit.Info.Entry, window.CanLand.Count(), window.CanUse.Count()))
	return nil
}

// actTaxiStatus asks the current flight master about its own node.
func (s *session) actTaxiStatus(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.taxiTarget(a, actVerbTaxiStatus)
	if err != nil {
		return s.actFail(a, step, err)
	}
	s.taxi.status = nil
	if err := s.transport().Send(wire.CMSGTaxiNodeStatusQuery, taxi.BuildTaxiNodeStatusQuery(unit.GUID)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_TAXI_NODE_STATUS_QUERY: %w", err))
	}
	ok, err := s.actUntil(ctx, actTaxiWait, func() bool {
		return s.taxi.status != nil && s.taxi.status.Unit == unit.GUID
	})
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("no node status within %s (the server answers only a flight master it can see)", actTaxiWait))
	}
	s.actOK(a, step, fmt.Sprintf("entry %d, node %s", unit.Info.Entry, taxi.TaxiStatusName(s.taxi.status.Status)))
	return nil
}

// actFly takes one flight to node= and watches it land. The node must be one the window offers (a real client picks
// from that list), the window must be this flight master's own, and the character must have money for a fare and not
// be on a flight already: every one of those is refused before a packet is sent. The flight is read from the
// character's own ON_TAXI transitions, never from a poll of the flag, so a short flight that sets and clears it
// inside one wait is not missed and a flight across maps is followed through the new map.
func (s *session) actFly(ctx context.Context, a *actState, step ActStep) error {
	if err := step.requireKeys("node"); err != nil {
		return s.actFail(a, step, err)
	}
	nodeRaw := step.Args["node"]
	node64, err := strconv.ParseUint(nodeRaw, 10, 32)
	if err != nil || node64 == 0 {
		return s.actFail(a, step, fmt.Errorf("node=%q is not a taxi node id", nodeRaw))
	}
	unit, err := s.taxiTarget(a, actVerbFly)
	if err != nil {
		return s.actFail(a, step, err)
	}
	run, err := s.flightStart(ctx, a, unit, uint32(node64))
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !run.landed {
		if err := s.flightAwaitLanding(ctx, run); err != nil {
			return s.actFail(a, step, err)
		}
	}
	detail, _, _, _ := s.flightLanded(ctx, run)
	s.actOK(a, step, detail)
	return nil
}

// flightRun is one accepted flight from takeoff on: what the landing line needs and the counters the landing is read
// from.
type flightRun struct {
	node    uint32
	before  items.Inventory
	takeoff time.Time
	// teleports is the self-teleport count at the request: the landing's own teleport moves it.
	teleports int
	// start is where the character stood at the request.
	startX, startY, startZ float32
	// landingClear is the clear counter at the request: a flight is over when it moves past it.
	landingClear uint64
	// sent is true once CMSG_ACTIVATE_TAXI went out.
	sent bool
	// landed is true when the flight was already over by the time the takeoff wait returned (a short flight).
	landed bool
}

// flightStart runs every guard of a flight and sends the one CMSG_ACTIVATE_TAXI, then waits for the takeoff: the
// server's refusal, or the character's own ON_TAXI being set. The window in a (a.taxi) is the one the caller read
// from this flight master. On a nil error the flight is in the air (or already over, landed) and, until its landing
// is seen, no movement may go out: taxiFlight.pending says so to the mover. An error before the request went out
// returns a nil run; an error after it (a refusal, a silent server, a cut wait) returns the run too, so a caller can
// tell "nothing was sent" from "the request is out and its outcome is the server's".
func (s *session) flightStart(ctx context.Context, a *actState, unit *objects.Object, node uint32) (*flightRun, error) {
	if !a.hasTaxi || a.taxi == nil {
		return nil, fmt.Errorf("fly: no taxi window (a taxi-nodes step comes first)")
	}
	if !a.taxi.HasWindow {
		return nil, fmt.Errorf("fly: the window carries no window block (its flight master and current node are unknown)")
	}
	if a.taxi.WindowUnit != unit.GUID {
		return nil, fmt.Errorf("fly: the taxi window is another flight master's (run taxi-nodes on entry %d)", unit.Info.Entry)
	}
	if !a.taxi.CanLand.Known(node) {
		return nil, fmt.Errorf("fly: node %d is not in the window's known mask", node)
	}
	if !a.taxi.CanUse.Known(node) {
		return nil, fmt.Errorf("fly: node %d is not in the window's usable mask", node)
	}
	if a.taxi.CurrentNode != 0 && uint32(a.taxi.CurrentNode) == node {
		return nil, fmt.Errorf("fly: node %d is the current node", node)
	}
	flags, known := s.selfFlags()
	if !known {
		return nil, fmt.Errorf("fly: the character's own flags are not in the world model yet")
	}
	if flags&unitFlagOnTaxi != 0 {
		return nil, fmt.Errorf("fly: the character is already on a taxi flight")
	}
	if s.taxiFlight.pending {
		return nil, fmt.Errorf("fly: the last flight's landing was not seen; no movement or flight until it is")
	}
	// The fare is the server's data (the window carries no price), so the only client-side test is a character with no
	// money at all; the server's refusal answer is the rest.
	run := &flightRun{node: node, before: s.inventoryNow(),
		startX: s.MoverX(), startY: s.MoverY(), startZ: s.MoverZ(),
		teleports: s.selfTeleports, landingClear: s.taxiFlight.clear}
	if run.before.Known && run.before.Coinage == 0 {
		return nil, fmt.Errorf("fly: the character has no copper for a fare")
	}
	takeoffSet := s.taxiFlight.set
	s.taxi.reply = nil
	// The server routes the flight itself: the request names the destination alone, and a path of several hops is the
	// server's TaxiPathGraph answer (TaxiHandler.cpp:207-208, TaxiPathGraph::GetCompleteNodeRoute). ForeverCore has no express opcode for a hop list.
	if err := s.transport().Send(wire.CMSGActivateTaxi, taxi.BuildActivateTaxi(unit.GUID, node)); err != nil {
		return nil, fmt.Errorf("CMSG_ACTIVATE_TAXI: %w", err)
	}
	run.sent = true
	// Takeoff: the refusal, or the flag's own setting read from the transition counters. A reply of 0 alone is not
	// enough, because the flag is what says the character is flying (and its clearing is the landing).
	ok, err := s.actUntilAcrossMaps(ctx, actTaxiWait, func() bool {
		if r := s.taxi.reply; r != nil && *r != taxi.TaxiReplyOK {
			return true
		}
		return s.taxiFlight.set > takeoffSet
	})
	if err != nil {
		return run, err
	}
	if r := s.taxi.reply; r != nil && *r != taxi.TaxiReplyOK {
		return run, fmt.Errorf("the flight master refused node %d: %s", node, taxi.TaxiReplyName(*r))
	}
	if !ok {
		return run, fmt.Errorf("node %d: no ON_TAXI and no refusal within %s; a silent server starts no flight (no path, client control removed, or an instant-taxi teleport)",
			node, actTaxiWait)
	}
	run.takeoff = s.transport().Now()
	if s.taxiFlight.clear > run.landingClear {
		// A short flight can be over before the takeoff wait returned: its landing is the clearing, already seen.
		run.landed = true
		return run, nil
	}
	// From here the flight is the server's: until its clearing no movement packet may go out, even if the caller's
	// own deadline ends a wait (the mover's guard reads pending).
	s.taxiFlight.pending = true
	return run, nil
}

// flightAwaitLanding waits for the flag's clearing: the flight's own ceiling, then the step reports the character
// still on the taxi.
func (s *session) flightAwaitLanding(ctx context.Context, run *flightRun) error {
	ok, err := s.actUntilAcrossMaps(ctx, actTaxiFlightMax, func() bool { return s.taxiFlight.clear > run.landingClear })
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("node %d: still on the taxi after %s (its landing was not seen; no movement until it is)", run.node, actTaxiFlightMax)
	}
	return nil
}

// flightLanded waits for the core's landing teleport and describes the landing: the core clears ON_TAXI and then
// teleports the player to the destination node (FlightPathMovementGenerator::DoFinalize), and the mover moves only
// when that SMSG_MOVE_TELEPORT is processed. Reading the position at the flag's clearing would print the takeoff
// point. The text also carries the money the fare took from the inventory the inventory tracker read back; x, y, z
// and the flight's length are returned for the method's answer.
func (s *session) flightLanded(ctx context.Context, run *flightRun) (detail string, x, y, z float32) {
	// The teleport is optional (a path whose last node is a teleport flag lands in place), so the wait is bounded and
	// its result is not an error.
	_, _ = s.actUntil(ctx, actTaxiLandingWait, func() bool { return s.selfTeleports > run.teleports })
	x, y, z = s.MoverX(), s.MoverY(), s.MoverZ()
	money := "coinage unknown"
	if after := s.inventoryNow(); run.before.Known && after.Known {
		money = coinageLine(run.before.Coinage, after.Coinage)
	}
	detail = fmt.Sprintf("landed at %.1f %.1f %.1f after %s, node %d, %.0f yd from takeoff, %s",
		x, y, z, s.transport().Now().Sub(run.takeoff).Round(time.Second), run.node,
		objects.Distance(run.startX, run.startY, run.startZ, x, y, z), money)
	return detail, x, y, z
}
