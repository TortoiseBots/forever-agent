package world

// The `taxi` method and its neighbours against the handler-boundary taxi fake: node discovery (SMSG_NEW_TAXI_PATH),
// the window a call answers, one flight started by the destination alone (ForeverCore routes the hops itself, so no
// express packet exists), the flight state the session reports until the landing, and mount and dismount around a
// flight. The fake's flight schedule is the one the act tests use.

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/taxi"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const flyer = npc.NPCFlagGossip | npc.NPCFlagFlightmaster

// mountSelfTaxi sets the character's own MountDisplayID through the update path (retail bit 61).
func (tw *taxiWorld) mountSelfTaxi(display int32) {
	tw.t.Helper()
	tw.server(wire.SMSGUpdateObject, buildUnitValuesUpdate(itemSelf, unitValuesBody(61, func(v *wire.Buffer) { v.WriteI32(display) })))
}

// sentCores counts the client packets of one opcode.
func (tw *taxiWorld) sentCount(core uint32) int {
	n := 0
	for _, s := range tw.sent {
		if s.core == core {
			n++
		}
	}
	return n
}

func eventsOfKind(tw *taxiWorld, kind string) []Event {
	var out []Event
	rows, _, _, _ := tw.s.EventLog().Since(0, EventPageMax)
	for _, r := range rows {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// TestNewTaxiPathRecordsTheDiscoveredNode: SMSG_NEW_TAXI_PATH is the node discovery: the node is kept once, and each
// packet is a taxi_node event.
func TestNewTaxiPathRecordsTheDiscoveredNode(t *testing.T) {
	tw := newTaxiWorld(t, flyer)
	tw.deliver(wire.SMSGNewTaxiPath, newTaxiPathPayload(7))
	tw.deliver(wire.SMSGNewTaxiPath, newTaxiPathPayload(7))
	tw.deliver(wire.SMSGNewTaxiPath, newTaxiPathPayload(300))
	if got := tw.s.taxiFlight.learned; !slices.Equal(got, []uint32{7, 300}) {
		t.Fatalf("learned nodes %v, want [7 300]", got)
	}
	ev := eventsOfKind(tw, events.KindTaxiNode)
	if len(ev) != 3 || ev[2].Data["node"] != uint32(300) {
		t.Fatalf("taxi_node events %+v, want three, the last for node 300", ev)
	}
	// A body this layout cannot read is counted and merges nothing.
	tw.deliver(wire.SMSGNewTaxiPath, []byte{1, 0})
	if len(tw.s.taxiFlight.learned) != 2 || tw.s.report.ParseErrors == 0 {
		t.Fatalf("a short packet merged or went uncounted: %v, %d parse errors", tw.s.taxiFlight.learned, tw.s.report.ParseErrors)
	}
}

// TestTaxiMethodAnswersTheWindow: no node asks for the window alone: the current node and both masks, and no flight
// request.
func TestTaxiMethodAnswersTheWindow(t *testing.T) {
	tw := newTaxiWorld(t, flyer)
	res, err := tw.s.serveTaxi(context.Background(), serveTaxiParams{GUID: GUID(flightGUID)})
	if err != nil {
		t.Fatalf("taxi: %v", err)
	}
	if !res.OK || res.Status != "window" || res.CurrentNode != taxiSelfNode ||
		!slices.Equal(res.Known, []uint32{1, 3, 5}) || !slices.Equal(res.Usable, []uint32{1, 5}) || res.OnTaxi {
		t.Fatalf("the window answer is %+v", res)
	}
	if tw.sentCount(wire.CMSGActivateTaxi) != 0 {
		t.Fatal("a window call requested a flight")
	}
}

// TestTaxiMethodFlightStaysFreeUntilTheLanding: the call returns at takeoff; the session reports on_taxi until the
// flag clears, no movement goes out, and the takeoff and landing are events.
func TestTaxiMethodFlightStaysFreeUntilTheLanding(t *testing.T) {
	tw := newTaxiWorld(t, flyer)
	res, err := tw.s.serveTaxi(context.Background(), serveTaxiParams{GUID: GUID(flightGUID), Node: taxiDestNode})
	if err != nil {
		t.Fatalf("taxi: %v", err)
	}
	if !res.OK || res.Status != "flying" || !res.OnTaxi || res.Reply != "ok" || res.Node != taxiDestNode {
		t.Fatalf("the takeoff answer is %+v", res)
	}
	if res.Event == nil || res.Event.Kind != events.KindTaxiFlight || res.Event.Data["state"] != "takeoff" {
		t.Fatalf("the answering event is %+v", res.Event)
	}
	if tw.sentCount(wire.CMSGActivateTaxi) != 1 || tw.lastNode != taxiDestNode {
		t.Fatalf("flights sent %d, node %d", tw.sentCount(wire.CMSGActivateTaxi), tw.lastNode)
	}
	if st := tw.s.serveStatus(&serveState{}); !st.OnTaxi {
		t.Fatal("status.on_taxi is false in the air")
	}
	if err := tw.s.taxiMovementBlocked(); err == nil {
		t.Fatal("the movement guard is open in the air")
	}
	// The session is free: let the flight run to its landing through the ordinary waits.
	ok, err := tw.s.actUntilAcrossMaps(context.Background(), actTaxiFlightMax, func() bool { return !tw.s.selfOnTaxi() })
	if err != nil || !ok {
		t.Fatalf("the flight never landed: %v %v", ok, err)
	}
	if st := tw.s.serveStatus(&serveState{}); st.OnTaxi {
		t.Fatal("status.on_taxi stays true after the landing")
	}
	if err := tw.s.taxiMovementBlocked(); err != nil {
		t.Fatalf("the guard stays shut after the landing: %v", err)
	}
	var states []string
	for _, e := range eventsOfKind(tw, events.KindTaxiFlight) {
		states = append(states, e.Data["state"].(string))
	}
	if !slices.Equal(states, []string{"takeoff", "landed"}) {
		t.Fatalf("taxi_flight events %v", states)
	}
	for _, s := range tw.sent {
		if isMovementOpcode(s.core) {
			t.Fatalf("a movement packet %s went out during the flight", wire.ClientName(s.core))
		}
	}
}

// TestTaxiMethodWaitLandingHoldsToTheLanding: wait_landing returns at the landing with its position.
func TestTaxiMethodWaitLandingHoldsToTheLanding(t *testing.T) {
	tw := newTaxiWorld(t, flyer)
	res, err := tw.s.serveTaxi(context.Background(), serveTaxiParams{GUID: GUID(flightGUID), Node: taxiDestNode, WaitLanding: true})
	if err != nil {
		t.Fatalf("taxi: %v", err)
	}
	if !res.OK || res.Status != "landed" || res.OnTaxi || res.X != tw.landX || res.Y != tw.landY || res.Z != tw.landZ {
		t.Fatalf("the landing answer is %+v", res)
	}
	if !contains(res.Detail, "landed at") {
		t.Fatalf("the landing detail is %q", res.Detail)
	}
}

// TestTaxiMethodMultiHopIsOneActivate: the old shape's node list (source first) is one CMSG_ACTIVATE_TAXI naming the
// destination: ForeverCore routes the hops itself and has no express packet.
func TestTaxiMethodMultiHopIsOneActivate(t *testing.T) {
	tw := newTaxiWorld(t, flyer)
	res, err := tw.s.serveTaxi(context.Background(), serveTaxiParams{GUID: GUID(flightGUID), Nodes: []uint32{taxiSelfNode, 1, taxiDestNode}})
	if err != nil || res.Status != "flying" {
		t.Fatalf("taxi: %+v, %v", res, err)
	}
	if tw.sentCount(wire.CMSGActivateTaxi) != 1 || tw.lastNode != taxiDestNode {
		t.Fatalf("flights sent %d, node %d, want one to %d", tw.sentCount(wire.CMSGActivateTaxi), tw.lastNode, taxiDestNode)
	}
	for _, name := range tw.sentNames() {
		if contains(name, "EXPRESS") {
			t.Fatalf("an express packet went out: %s", name)
		}
	}
}

// sentNames is the client names of the packets sent.
func (tw *taxiWorld) sentNames() []string {
	var out []string
	for _, s := range tw.sent {
		out = append(out, wire.ClientName(s.core))
	}
	return out
}

// TestTaxiMethodRefusesBadCalls: calls the method must refuse before it sends a flight request.
func TestTaxiMethodRefusesBadCalls(t *testing.T) {
	cases := map[string]struct {
		p    serveTaxiParams
		want string
	}{
		"one node":        {serveTaxiParams{GUID: GUID(flightGUID), Nodes: []uint32{5}}, "source and a destination"},
		"disagreeing":     {serveTaxiParams{GUID: GUID(flightGUID), Node: 1, Nodes: []uint32{3, 5}}, "not the last"},
		"too many":        {serveTaxiParams{GUID: GUID(flightGUID), Nodes: make([]uint32, 65)}, "too many nodes"},
		"landing alone":   {serveTaxiParams{GUID: GUID(flightGUID), WaitLanding: true}, "needs a destination"},
		"wrong source":    {serveTaxiParams{GUID: GUID(flightGUID), Nodes: []uint32{1, 5}}, "source node 1"},
		"unusable node":   {serveTaxiParams{GUID: GUID(flightGUID), Node: 3}, "not in the window's usable mask"},
		"no guid":         {serveTaxiParams{Node: 5}, "guid is required"},
		"unknown node":    {serveTaxiParams{GUID: GUID(flightGUID), Node: 9}, "not in the window's known mask"},
		"negative wait":   {serveTaxiParams{GUID: GUID(flightGUID), Node: 5, WaitMS: -1}, "wait_ms"},
		"wait over limit": {serveTaxiParams{GUID: GUID(flightGUID), Node: 5, WaitMS: 61000}, "wait_ms"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tw := newTaxiWorld(t, flyer)
			_, err := tw.s.serveTaxi(context.Background(), c.p)
			if err == nil || !contains(err.Error(), c.want) {
				t.Fatalf("taxi answered %v, want %q", err, c.want)
			}
			if tw.sentCount(wire.CMSGActivateTaxi) != 0 {
				t.Fatal("a refused call requested a flight")
			}
		})
	}
}

// TestTaxiMethodRefusedByTheServerIsAResult: once the request is out, the server's refusal is an answer (ok false,
// the reason, the taxi_reply event), not an RPC error, and the session is not left "in the air".
func TestTaxiMethodRefusedByTheServerIsAResult(t *testing.T) {
	tw := newTaxiWorld(t, flyer)
	tw.refuse = taxi.TaxiReplyNotEnoughMoney
	res, err := tw.s.serveTaxi(context.Background(), serveTaxiParams{GUID: GUID(flightGUID), Node: taxiDestNode})
	if err != nil {
		t.Fatalf("taxi: %v", err)
	}
	if res.OK || res.Status != "refused" || res.Reply != "not_enough_money" || res.OnTaxi {
		t.Fatalf("the refusal answer is %+v", res)
	}
	if res.Event == nil || res.Event.Kind != events.KindTaxiReply || res.Event.Data["reason"] != "not_enough_money" {
		t.Fatalf("the answering event is %+v", res.Event)
	}
	if err := tw.s.taxiMovementBlocked(); err != nil {
		t.Fatalf("a refused flight left the guard shut: %v", err)
	}
}

// TestTaxiMethodSilentServerIsNoTakeoff: a request the server neither refuses nor starts is its own status, with the
// session not held in the air.
func TestTaxiMethodSilentServerIsNoTakeoff(t *testing.T) {
	tw := newTaxiWorld(t, flyer)
	tw.silent = true
	res, err := tw.s.serveTaxi(context.Background(), serveTaxiParams{GUID: GUID(flightGUID), Node: taxiDestNode})
	if err != nil {
		t.Fatalf("taxi: %v", err)
	}
	if res.OK || res.Status != "no_takeoff" || res.Reply != "" || res.OnTaxi || !contains(res.Error, "no ON_TAXI") {
		t.Fatalf("the silent answer is %+v", res)
	}
	if err := tw.s.taxiMovementBlocked(); err != nil {
		t.Fatalf("a flight that never started left the guard shut: %v", err)
	}
}

// TestTaxiMethodFirstVisitDiscoversTheNode: a first visit's answer is the node's discovery (SMSG_NEW_TAXI_PATH and a
// learned status); the method asks again and answers the window, with the node recorded.
func TestTaxiMethodFirstVisitDiscoversTheNode(t *testing.T) {
	tw := newTaxiWorld(t, flyer)
	tw.firstVisit = true
	res, err := tw.s.serveTaxi(context.Background(), serveTaxiParams{GUID: GUID(flightGUID)})
	if err != nil || !res.OK || res.Status != "window" {
		t.Fatalf("taxi: %+v, %v", res, err)
	}
	if !slices.Equal(tw.s.taxiFlight.learned, []uint32{taxiSelfNode}) {
		t.Fatalf("learned nodes %v, want the flight master's node", tw.s.taxiFlight.learned)
	}
	if tw.talks != 2 {
		t.Fatalf("hellos %d, want two (the first one answered the discovery)", tw.talks)
	}
}

// TestTaxiMethodRefusesANonFlightMaster: a unit without the flight-master bit is refused before any packet.
func TestTaxiMethodRefusesANonFlightMaster(t *testing.T) {
	tw := newTaxiWorld(t, npc.NPCFlagGossip)
	_, err := tw.s.serveTaxi(context.Background(), serveTaxiParams{GUID: GUID(flightGUID)})
	if err == nil || !contains(err.Error(), "not a flight master") || len(tw.sent) != 0 {
		t.Fatalf("taxi answered %v after %v", err, cores(tw.sent))
	}
}

// TestTaxiMethodWhileMountedSendsNoDismount: the core removes the mount aura when a flight starts, so a mounted
// character asks for the flight and no CMSG_CANCEL_MOUNT_AURA goes out.
func TestTaxiMethodWhileMountedSendsNoDismount(t *testing.T) {
	tw := newTaxiWorld(t, flyer)
	tw.mountSelfTaxi(14338)
	if tw.s.mountDisplay() != 14338 {
		t.Fatal("the fake did not mount the character")
	}
	res, err := tw.s.serveTaxi(context.Background(), serveTaxiParams{GUID: GUID(flightGUID), Node: taxiDestNode})
	if err != nil || res.Status != "flying" {
		t.Fatalf("taxi: %+v, %v", res, err)
	}
	if tw.sentCount(wire.CMSGCancelMountAura) != 0 {
		t.Fatal("a dismount went out before the flight")
	}
}

// TestMountAndDismountRefusedInTheAir: in flight the display is the flight's own; neither verb sends anything, and
// they work again after the landing.
func TestMountAndDismountRefusedInTheAir(t *testing.T) {
	tw := newTaxiWorld(t, flyer)
	if res, err := tw.s.serveTaxi(context.Background(), serveTaxiParams{GUID: GUID(flightGUID), Node: taxiDestNode}); err != nil || res.Status != "flying" {
		t.Fatalf("taxi: %+v, %v", res, err)
	}
	tw.mountSelfTaxi(25678) // the flight's own mount display
	before := len(tw.sent)
	if _, err := tw.s.serveDismount(context.Background()); err == nil || !contains(err.Error(), "taxi flight") {
		t.Fatalf("dismount answered %v", err)
	}
	tw.s.known = append(tw.s.known, 133)
	if _, err := tw.s.serveMount(context.Background(), serveMountParams{Spell: 133}); err == nil || !contains(err.Error(), "taxi flight") {
		t.Fatalf("mount answered %v", err)
	}
	if len(tw.sent) != before {
		t.Fatalf("a refused verb sent %v", cores(tw.sent[before:]))
	}
	if ok, err := tw.s.actUntilAcrossMaps(context.Background(), actTaxiFlightMax, func() bool { return !tw.s.selfOnTaxi() }); err != nil || !ok {
		t.Fatalf("the flight never landed: %v %v", ok, err)
	}
	// On the ground the display is the character's own again: a mounted character may dismount.
	tw.mountSelfTaxi(0)
	if out, err := tw.s.serveDismount(context.Background()); err != nil || out.Mounted {
		t.Fatalf("dismount after the landing: %+v, %v", out, err)
	}
}

// TestTaxiIsRegisteredAndAnswersOverRPC: the method is in the ported table, not the not-ported one, and its JSON
// answer carries the flight fields.
func TestTaxiIsRegisteredAndAnswersOverRPC(t *testing.T) {
	ported, notPortedNames := ServeRPCMethods()
	if !slices.Contains(ported, "taxi") || slices.Contains(notPortedNames, "taxi") {
		t.Fatalf("taxi is not a ported method: %v / %v", ported, notPortedNames)
	}
	tw := newTaxiWorld(t, flyer)
	res, err := tw.s.serveTaxi(context.Background(), serveTaxiParams{GUID: GUID(flightGUID), Node: taxiDestNode})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"since", "ok", "status", "unit", "known", "usable", "on_taxi", "event", "lines"} {
		if _, ok := m[k]; !ok {
			t.Errorf("the answer has no %q: %s", k, raw)
		}
	}
	var p serveTaxiParams
	if err := json.Unmarshal([]byte(`{"guid":"`+GUID(flightGUID).String()+`","nodes":[3,1,5],"wait_landing":true}`), &p); err != nil ||
		!slices.Equal(p.Nodes, []uint32{3, 1, 5}) || !p.WaitLanding {
		t.Fatalf("the params decode as %+v, %v", p, err)
	}
}
