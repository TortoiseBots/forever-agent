package world

// One area-trigger send per crossing, also through the daemon's own callers: a walk that ends in the portal's armed
// volume sends the trigger from the watch, and the caller's next explicit area_trigger call (a follow loop that
// zones after its lead, a ghost run that takes the corpse's entrance) must not send it a second time while the
// character still stands in that volume.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestWalkThenExplicitSendSendsThePortalTriggerOnce: the walk to the portal fires the watch's packet, and the
// explicit call right after it (the caller's next tick, still on the outer map) answers success without a second
// packet.
func TestWalkThenExplicitSendSendsThePortalTriggerOnce(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	if _, err := s.serveWalkTo(context.Background(), 1005, 2000, 50); err != nil {
		t.Fatalf("the walk answered %v", err)
	}
	if n := areaTriggerSends(link); n != 1 {
		t.Fatalf("the walk over the portal sent %d trigger(s), want one", n)
	}
	if err := s.EnterAreaTrigger(78); err != nil {
		t.Fatalf("the explicit send answered %v", err)
	}
	if n := areaTriggerSends(link); n != 1 {
		t.Fatalf("the explicit send after the walk sent %d trigger(s) in all, want one", n)
	}
	if s.report.AreaTriggers != 1 {
		t.Fatalf("the report counts %d area trigger(s), want one", s.report.AreaTriggers)
	}
}

// TestWalkThenTheDungeonPortSendSendsThePortalTriggerOnce: the same in the dungeon driver's shape — its world port
// sends the entrance trigger after a walk that already crossed it.
func TestWalkThenTheDungeonPortSendSendsThePortalTriggerOnce(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	if _, err := s.serveWalkTo(context.Background(), 1005, 2000, 50); err != nil {
		t.Fatalf("the walk answered %v", err)
	}
	if n := areaTriggerSends(link); n != 1 {
		t.Fatalf("the walk over the portal sent %d trigger(s), want one", n)
	}
	if err := (dungeonWorld{s: s}).EnterAreaTrigger(78); err != nil {
		t.Fatalf("the port's send answered %v", err)
	}
	if n := areaTriggerSends(link); n != 1 {
		t.Fatalf("the port's send after the walk sent %d trigger(s) in all, want one", n)
	}
}

// TestServeWalkThenExplicitSendSendsThePortalTriggerOnce: the shape over the daemon's RPC. walk_to ends inside the
// plan's portal volume and the watch sends the trigger; the area_trigger call that follows answers already_sent
// and sends nothing.
func TestServeWalkThenExplicitSendSendsThePortalTriggerOnce(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	dir := planTriggerDir(t, "auto")
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.PlansDir = dir })
	serveWaitPosition(t, a, steps)
	a.mustCall(t, "walk_to", map[string]any{"x": 1005.0, "y": 2000.0, "z": 50.0})
	deadline := time.Now().Add(5 * time.Second)
	for serveAreaCount(f) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the walk over the plan's portal sent no area trigger\n%s", steps.joined())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var ans struct {
		AlreadySent bool `json:"already_sent"`
	}
	json.Unmarshal(a.mustCall(t, "area_trigger", map[string]any{"id": 78}), &ans)
	if !ans.AlreadySent {
		t.Fatalf("the explicit send after the walk answered already_sent=false\n%s", steps.joined())
	}
	if n := serveAreaCount(f); n != 1 {
		t.Fatalf("the daemon sent %d area trigger(s) in all, want one\n%s", n, steps.joined())
	}
	a.stop(t)
	f.wait(t)
}

// TestExplicitSendForAnUnarmedTriggerIsUnchanged: a trigger the watch never armed has no volume to be held by, so
// every explicit call sends, as before.
func TestExplicitSendForAnUnarmedTriggerIsUnchanged(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	for i := range 2 {
		sent, err := s.enterAreaTrigger(78)
		if err != nil {
			t.Fatalf("send %d answered %v", i+1, err)
		}
		if !sent {
			t.Fatalf("send %d of an un-armed trigger was held", i+1)
		}
	}
	if n := areaTriggerSends(link); n != 2 {
		t.Fatalf("two explicit sends of an un-armed trigger sent %d packet(s), want two", n)
	}
}

// TestTheHeldSendExpires: after triggerSendHold a still-standing character may send the trigger again, so a
// transfer that really failed is retried.
func TestTheHeldSendExpires(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	if _, err := s.serveWalkTo(context.Background(), 1005, 2000, 50); err != nil {
		t.Fatalf("the walk answered %v", err)
	}
	if sent, err := s.enterAreaTrigger(78); err != nil || sent {
		t.Fatalf("the send right after the walk answered sent=%v, %v", sent, err)
	}
	rec := s.trigSends[78]
	if rec.vol.ID != 78 || rec.vol.Radius != triggerDefaultRadius {
		t.Fatalf("the held send is %+v, want the walk's volume 78", rec)
	}
	rec.at = rec.at.Add(-triggerSendHold - time.Second)
	s.trigSends[78] = rec
	if sent, err := s.enterAreaTrigger(78); err != nil || !sent {
		t.Fatalf("the send after %s answered sent=%v, %v", triggerSendHold, sent, err)
	}
	if n := areaTriggerSends(link); n != 2 {
		t.Fatalf("the send after the hold sent %d packet(s) in all, want two", n)
	}
}

// TestTheHeldSendClearsWhenTheCharacterLeavesTheVolume: a character that walked out of the portal and back may send
// the trigger again: that is a step in, not the crossing the walk already reported.
func TestTheHeldSendClearsWhenTheCharacterLeavesTheVolume(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	if _, err := s.serveWalkTo(context.Background(), 1005, 2000, 50); err != nil {
		t.Fatalf("the walk answered %v", err)
	}
	if _, err := s.serveWalkTo(context.Background(), 1030, 2000, 50); err != nil {
		t.Fatalf("the walk out answered %v", err)
	}
	if _, ok := s.trigSends[78]; ok {
		t.Fatal("the send record outlived the character leaving the volume")
	}
	if sent, err := s.enterAreaTrigger(78); err != nil || !sent {
		t.Fatalf("the send after leaving answered sent=%v, %v", sent, err)
	}
	if n := areaTriggerSends(link); n != 2 {
		t.Fatalf("the walk and the send after leaving made %d packet(s), want two", n)
	}
}

// TestTheHeldSendClearsOnAMapChange: a record of the outer map holds nothing on the instance's map.
func TestTheHeldSendClearsOnAMapChange(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	if _, err := s.serveWalkTo(context.Background(), 1005, 2000, 50); err != nil {
		t.Fatalf("the walk answered %v", err)
	}
	if sent, err := s.enterAreaTrigger(78); err != nil || sent {
		t.Fatalf("the send right after the walk answered sent=%v, %v", sent, err)
	}
	s.world.ResetForMap(36)
	if sent, err := s.enterAreaTrigger(78); err != nil || !sent {
		t.Fatalf("the send on map 36 answered sent=%v, %v", sent, err)
	}
	if n := areaTriggerSends(link); n != 2 {
		t.Fatalf("the walk and the send on the new map made %d packet(s), want two", n)
	}
}

// transferNewWorld feeds one whole map change through the session: the transfer pending, its suspend token and the
// new world.
func transferNewWorld(t *testing.T, s *session, mapID int32, counter int32, x, y, z float32) {
	t.Helper()
	if err := feed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(mapID, x, y, z)); err != nil {
		t.Fatal(err)
	}
	if err := feed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(uint32(counter), 1)); err != nil {
		t.Fatal(err)
	}
	if err := feed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(mapID, x, y, z, 0, objects.NewWorldReasonNormal, counter)); err != nil {
		t.Fatal(err)
	}
}

// TestTriggerHoldClearsOnAMapChange: the hold is a crossing on one map. A character that zones in and straight back
// out stands at the volume again within triggerSendHold, and that is a new crossing: the send goes out instead of
// being swallowed as a repeat of the crossing before the zoning.
//
// Mutation this test catches: a hold that only compares the record's map with the session's (the round trip puts the
// same map back, and the send is held again).
func TestTriggerHoldClearsOnAMapChange(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	x, y, z := float32(1000), float32(2000), float32(50)
	r := float32(4)
	if _, err := s.serveAreaTriggerWatch(serveAreaTriggerWatchParams{ID: 500, X: &x, Y: &y, Z: &z, Radius: &r}); err != nil {
		t.Fatalf("the arm answered %v", err)
	}
	if n := areaTriggerSends(link); n != 1 {
		t.Fatalf("arming under the character sent %d trigger(s), want one", n)
	}
	if err := s.EnterAreaTrigger(500); err != nil {
		t.Fatalf("the explicit send while standing in the volume answered %v", err)
	}
	if n := areaTriggerSends(link); n != 1 {
		t.Fatalf("the explicit send in the volume sent %d trigger(s) in all, want the arm's one", n)
	}
	// Zoning in and straight back out: the map change ends the record, so the volume's send is a new crossing.
	transferNewWorld(t, s, 36, 3, 1000, 2000, 50)
	transferNewWorld(t, s, 0, 4, 1000, 2000, 50)
	if len(s.trigSends) != 0 {
		t.Fatalf("the round trip kept %d send record(s): %+v", len(s.trigSends), s.trigSends)
	}
	if err := s.EnterAreaTrigger(500); err != nil {
		t.Fatalf("the send after the round trip answered %v", err)
	}
	if n := areaTriggerSends(link); n != 2 {
		t.Fatalf("the send after the round trip sent %d trigger(s) in all, want two", n)
	}
}

// TestTriggerHoldDropsOnARearmAndOnAClear: an identical re-arm keeps the send record (the same crossing is not
// repeated), a re-arm at another point drops it (the volume it went out from is no longer armed), and clearing the
// watch drops every record.
//
// Mutations these tests catch: a re-arm that keeps the record of a moved volume (a send from the old volume holds the
// new one), and a clear that empties the volumes but keeps the sends.
func TestTriggerHoldDropsOnARearmAndOnAClear(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	x, y, z := float32(1000), float32(2000), float32(50)
	r := float32(4)
	if _, err := s.serveAreaTriggerWatch(serveAreaTriggerWatchParams{ID: 500, X: &x, Y: &y, Z: &z, Radius: &r}); err != nil {
		t.Fatalf("the arm answered %v", err)
	}
	if _, ok := s.trigSends[500]; !ok {
		t.Fatalf("arming under the character recorded no send")
	}
	// An identical re-arm keeps the record, so the explicit send stays held.
	if _, err := s.serveAreaTriggerWatch(serveAreaTriggerWatchParams{ID: 500, X: &x, Y: &y, Z: &z, Radius: &r}); err != nil {
		t.Fatalf("the identical re-arm answered %v", err)
	}
	if err := s.EnterAreaTrigger(500); err != nil {
		t.Fatalf("the explicit send answered %v", err)
	}
	if n := areaTriggerSends(link); n != 1 {
		t.Fatalf("the identical re-arm's explicit send sent %d trigger(s) in all, want the arm's one", n)
	}
	// A moved volume drops the old record: the send belonged to a volume that is no longer armed. (The character is
	// still inside it, so the moved arm is not a new crossing and records nothing.)
	x2 := float32(1000.5)
	if _, err := s.serveAreaTriggerWatch(serveAreaTriggerWatchParams{ID: 500, X: &x2, Y: &y, Z: &z, Radius: &r}); err != nil {
		t.Fatalf("the moved re-arm answered %v", err)
	}
	if rec, ok := s.trigSends[500]; ok {
		t.Fatalf("a moved volume kept the send record %+v", rec)
	}
	// Another id armed under the character is a crossing of its own, with its record.
	x3 := float32(1000)
	if _, err := s.serveAreaTriggerWatch(serveAreaTriggerWatchParams{ID: 501, X: &x3, Y: &y, Z: &z, Radius: &r}); err != nil {
		t.Fatalf("the second arm answered %v", err)
	}
	if _, ok := s.trigSends[501]; !ok {
		t.Fatalf("arming a new volume under the character recorded no send")
	}
	// The clear drops every record, so a trigger goes out again.
	if _, err := s.serveAreaTriggerWatch(serveAreaTriggerWatchParams{Clear: true}); err != nil {
		t.Fatalf("the clear answered %v", err)
	}
	if len(s.trigSends) != 0 {
		t.Fatalf("the clear kept %d send record(s): %+v", len(s.trigSends), s.trigSends)
	}
}

// TestCancelledWalkToDisarmsItsVolumes: a walk_to whose context is cancelled still removes the volumes it armed: the
// disarm is a defer, not a line after the walk.
//
// Mutation this test catches: a disarm only on the walk's success path (a cancelled walk leaves the portal armed, and
// the next crossing sends the trigger from a walk that never reached it).
func TestCancelledWalkToDisarmsItsVolumes(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.serveWalkTo(ctx, 1010, 2000, 50); err == nil {
		t.Fatalf("a cancelled walk answered success")
	}
	if s.triggerArmed(78) {
		t.Fatalf("the cancelled walk left its volume armed: %+v", s.triggers)
	}
	if n := areaTriggerSends(link); n != 0 {
		t.Fatalf("the cancelled walk sent %d trigger(s), want none", n)
	}
}

// TestDungeonLegWalkArmsNothing: the dungeon driver's own walking port arms no trigger volume: automatic arming is
// the daemon's walk_to and walk_path alone (a leg's walk through the same session must not arm a volume the plan's
// step keeps, nor fire its zone from the walk in).
//
// Mutation this test catches: the port arming the plan's volumes for a leg walk.
func TestDungeonLegWalkArmsNothing(t *testing.T) {
	s, _, _ := planTriggerSession(t, "auto")
	if err := (dungeonWalker{s: s}).Walk(context.Background(), [][3]float32{{1010, 2000, 50}}); err != nil {
		t.Fatalf("the leg walk answered %v", err)
	}
	if len(s.triggers) != 0 || len(s.trigSends) != 0 {
		t.Fatalf("a dungeon leg armed %+v and recorded %+v", s.triggers, s.trigSends)
	}
}
