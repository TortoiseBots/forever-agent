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
