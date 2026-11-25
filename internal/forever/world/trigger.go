package world

// The area-trigger watch (#228): the client sends CMSG_AREA_TRIGGER itself when its own movement enters a trigger.
// A real client knows every entry of its AreaTrigger data and sends as it steps into one; the server only checks that
// the player really is inside (MiscHandler.cpp:540-561: "too far, ignore" when IsInAreaTrigger says otherwise) and
// then runs the trigger's script, which is how a portal teleports. This agent keeps no table of triggers, so a volume
// is armed by id, map, centre and radius (area_trigger_watch) and the mover's own positions are the crossings: the
// mover calls back after every movement packet, and a volume that was not inside before and is inside now sends the
// packet once. Leaving the volume re-arms it, so a second crossing sends again; nothing else does (one send per
// crossing, and at most one volume state per armed id, which a repeated arm keeps). A volume smaller than the gap
// between two heartbeats is crossed by asking the mover to report the line's closest approach to its centre first
// (triggerSamples), because the server accepts the packet only from a position inside the volume.
//
// The send itself is held for triggerSendHold (enterAreaTrigger): the explicit area_trigger call that follows a walk
// into the volume — a follow loop zoning after its lead, a ghost run taking the corpse's entrance — is the same
// crossing, not a second one. The hold clears when the character leaves the volume, the map changes, a volume of
// that id is armed again, or the hold passes, so a transfer that failed can still be retried.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/rpc"
)

const (
	// triggerDefaultRadius is the radius an armed volume gets when the caller passes none: small enough to stand
	// inside a classic entrance sphere (the entries the 1.60.1.70959 client data still carries are 7 yards across a
	// portal that sits exactly on the plan's point, e.g. Stormwind Vault's 107), and large enough that a walking leg
	// (a 500 ms heartbeat at 7 yd/s, 3.5 yd) usually samples it. A sphere the line merely cuts through, with both
	// sampled ends outside, is still crossed: triggerSamples has the mover report the closest approach first.
	triggerDefaultRadius float32 = 4
	// triggerMaxRadius bounds an armed volume: a trigger sphere is a few yards, never a zone.
	triggerMaxRadius float32 = 100
	// triggerWatchMax bounds the armed volumes one session carries.
	triggerWatchMax = 32
	// triggerSendHold is how long a trigger that just went out from a volume is not sent again while the character
	// still stands in it: one crossing is one send, and the explicit call that follows a walk (a follow loop's
	// zoning, a ghost run's entrance) repeats what the watch already did. It is short enough that a transfer that
	// really failed can be retried by the next attempt.
	triggerSendHold = 10 * time.Second
)

// triggerSend is one area-trigger send the session keeps for triggerSendHold: the armed volume it went out from,
// and when.
type triggerSend struct {
	vol srvTrigger
	at  time.Time
}

// srvTrigger is one armed trigger volume.
type srvTrigger struct {
	ID     int32   `json:"id"`
	Map    uint32  `json:"map"`
	X      float32 `json:"x"`
	Y      float32 `json:"y"`
	Z      float32 `json:"z"`
	Radius float32 `json:"radius"`
}

// serveAreaTriggerWatchParams is area_trigger_watch's parameter object. Without a point the volume is centred on the
// character's own position, which is how a caller that already stands in a trigger arms it.
type serveAreaTriggerWatchParams struct {
	ID     int32    `json:"id"`
	Map    *uint32  `json:"map,omitempty"`
	X      *float32 `json:"x,omitempty"`
	Y      *float32 `json:"y,omitempty"`
	Z      *float32 `json:"z,omitempty"`
	Radius *float32 `json:"radius,omitempty"`
	// Clear removes every armed volume instead of adding one; ID is not needed then.
	Clear bool `json:"clear,omitempty"`
}

// ServeAreaTriggerWatch is area_trigger_watch's answer: the armed volumes after the call, in arming order.
type ServeAreaTriggerWatch struct {
	Triggers []srvTrigger `json:"triggers"`
}

// registerTriggerWatch wires area_trigger_watch into the method table.
func registerTriggerWatch(srv *rpc.Server, st *serveState) {
	srv.Register("area_trigger_watch", serveCallTyped(st, func(s *session, _ context.Context, p serveAreaTriggerWatchParams) (ServeAreaTriggerWatch, error) {
		return s.serveAreaTriggerWatch(p)
	}))
}

// serveAreaTriggerWatch arms (or clears) the crossing watch.
func (s *session) serveAreaTriggerWatch(p serveAreaTriggerWatchParams) (ServeAreaTriggerWatch, error) {
	if p.Clear {
		s.triggers, s.triggersIn, s.trigSends = nil, nil, nil
		return ServeAreaTriggerWatch{Triggers: []srvTrigger{}}, nil
	}
	if p.ID <= 0 {
		return ServeAreaTriggerWatch{}, errors.New("area_trigger_watch: needs a trigger id")
	}
	if len(s.triggers) >= triggerWatchMax && !s.triggerArmed(p.ID) {
		return ServeAreaTriggerWatch{}, fmt.Errorf("area_trigger_watch: %d volumes are already armed (the bound)", triggerWatchMax)
	}
	v := srvTrigger{ID: p.ID, Radius: triggerDefaultRadius}
	if p.Map != nil {
		v.Map = *p.Map
	} else if s.world != nil {
		v.Map = s.world.MapID()
	}
	if p.X != nil || p.Y != nil || p.Z != nil {
		if p.X == nil || p.Y == nil || p.Z == nil {
			return ServeAreaTriggerWatch{}, errors.New("area_trigger_watch: give x, y and z together, or none (the character's own position)")
		}
		v.X, v.Y, v.Z = *p.X, *p.Y, *p.Z
	} else {
		if s.mover == nil {
			return ServeAreaTriggerWatch{}, errors.New("area_trigger_watch: no position: the mover never arrived, so give x, y and z")
		}
		v.X, v.Y, v.Z, _ = s.mover.Position()
	}
	if p.Radius != nil {
		v.Radius = *p.Radius
	}
	if v.Radius <= 0 || v.Radius > triggerMaxRadius {
		return ServeAreaTriggerWatch{}, fmt.Errorf("area_trigger_watch: radius must be over 0 and at most %g yards, not %g", triggerMaxRadius, v.Radius)
	}
	// Arming the same id again moves its volume instead of adding a second state. The entered state is kept: an
	// identical retry (a lost RPC reply) while the character stands inside must not send the trigger a second time —
	// for a portal script that is another teleport request, and the server accepts one from the position it still
	// holds (MiscHandler.cpp:556-561). A changed volume the character is already counted inside of is not a new entry
	// either; one that leaves the character outside clears the state, so stepping back in (or arming it back onto the
	// character) sends once.
	s.armTrigger(v)
	if s.triggersIn == nil {
		s.triggersIn = map[int32]bool{}
	}
	out := ServeAreaTriggerWatch{Triggers: append([]srvTrigger(nil), s.triggers...)}
	// A volume armed under the character's feet is a crossing at once: the caller stood in the trigger and asked for
	// it, which is what the client would have done on stepping in. An already entered volume stays entered here.
	if s.mover != nil {
		x, y, z, _ := s.mover.Position()
		if err := s.triggerCrossing([3]float32{x, y, z}); err != nil {
			return out, err
		}
	}
	return out, nil
}

// armTrigger stores v: a volume with the same id is replaced in place (its entered state is kept, so a retried arm is
// not a second crossing), a new one is appended and starts outside every volume. A volume that is not the one a send
// was recorded from (a new id, or the same id at a changed point or reach) drops that record: the send belonged to a
// volume that is no longer armed, and arming under the character is a crossing of its own. An identical retry keeps
// the record, so the explicit call after it is still held.
func (s *session) armTrigger(v srvTrigger) {
	for i := range s.triggers {
		if s.triggers[i].ID == v.ID {
			s.triggers[i] = v
			if rec, ok := s.trigSends[v.ID]; ok && rec.vol != v {
				delete(s.trigSends, v.ID)
			}
			return
		}
	}
	s.triggers = append(s.triggers, v)
	delete(s.triggersIn, v.ID)
	delete(s.trigSends, v.ID)
}

// disarmTrigger removes one armed volume and its entered state: the walk that armed it ended.
func (s *session) disarmTrigger(id int32) {
	for i := range s.triggers {
		if s.triggers[i].ID == id {
			s.triggers = append(s.triggers[:i], s.triggers[i+1:]...)
			break
		}
	}
	delete(s.triggersIn, id)
}

// triggerArmed reports whether id already has a volume (which arming again replaces).
func (s *session) triggerArmed(id int32) bool {
	for i := range s.triggers {
		if s.triggers[i].ID == id {
			return true
		}
	}
	return false
}

// armedVolume returns the armed volume of id, if there is one.
func (s *session) armedVolume(id int32) (srvTrigger, bool) {
	for i := range s.triggers {
		if s.triggers[i].ID == id {
			return s.triggers[i], true
		}
	}
	return srvTrigger{}, false
}

// triggerSendHeld reports whether id's trigger already went out from the volume the character still stands in, on
// the same map, less than triggerSendHold ago: a second send is the same crossing heard twice. A record that no
// longer holds (the character left the volume, the map changed, the hold passed) is dropped here.
func (s *session) triggerSendHeld(id int32) bool {
	rec, ok := s.trigSends[id]
	if !ok {
		return false
	}
	if s.mover == nil {
		delete(s.trigSends, id)
		return false
	}
	x, y, z, _ := s.mover.Position()
	if !s.triggerSendLive(rec, [3]float32{x, y, z}) {
		delete(s.trigSends, id)
		return false
	}
	return true
}

// triggerSendLive reports whether rec still holds at pos: less than triggerSendHold ago, on the record's map, and
// the character inside the record's volume.
func (s *session) triggerSendLive(rec triggerSend, pos [3]float32) bool {
	if time.Since(rec.at) >= triggerSendHold {
		return false
	}
	var mapID uint32
	if s.world != nil {
		mapID = s.world.MapID()
	}
	if rec.vol.Map != mapID {
		return false
	}
	return objects.Distance(pos[0], pos[1], pos[2], rec.vol.X, rec.vol.Y, rec.vol.Z) <= rec.vol.Radius
}

// noteTriggerSend records a send of id when it went out from an armed volume the character stands in: that volume
// is what a repeat would be sent from. A send from no armed volume (a trigger the watch never armed) clears the
// record, so such a trigger behaves exactly as it did before the hold.
func (s *session) noteTriggerSend(id int32) {
	v, ok := s.armedVolume(id)
	if !ok || s.mover == nil {
		delete(s.trigSends, id)
		return
	}
	x, y, z, _ := s.mover.Position()
	rec := triggerSend{vol: v, at: time.Now()}
	if !s.triggerSendLive(rec, [3]float32{x, y, z}) {
		delete(s.trigSends, id)
		return
	}
	if s.trigSends == nil {
		s.trigSends = map[int32]triggerSend{}
	}
	s.trigSends[id] = rec
}

// pruneTriggerSends drops the send records that no longer hold at pos: the character left the volume, or the map
// changed, or the hold passed. The mover calls it after every movement packet, before any crossing is judged.
func (s *session) pruneTriggerSends(pos [3]float32) {
	for id, rec := range s.trigSends {
		if !s.triggerSendLive(rec, pos) {
			delete(s.trigSends, id)
		}
	}
}

// triggerCrossing sends CMSG_AREA_TRIGGER for every armed volume the mover has just entered, once per crossing: a
// volume the mover is outside of clears its state, so stepping out and back in sends again. The mover calls it after
// every movement packet, so the packet goes out with the server holding the same position. The same call drops the
// send records that no longer hold at pos (the character left a volume, the map changed, the hold passed).
func (s *session) triggerCrossing(pos [3]float32) error {
	s.pruneTriggerSends(pos)
	if len(s.triggers) == 0 {
		return nil
	}
	if s.triggersIn == nil {
		s.triggersIn = map[int32]bool{}
	}
	var mapID uint32
	if s.world != nil {
		mapID = s.world.MapID()
	}
	for _, v := range s.triggers {
		if v.Map != mapID {
			s.triggersIn[v.ID] = false
			continue
		}
		inside := objects.Distance(pos[0], pos[1], pos[2], v.X, v.Y, v.Z) <= v.Radius
		if !inside {
			s.triggersIn[v.ID] = false
			continue
		}
		if s.triggersIn[v.ID] {
			continue
		}
		s.triggersIn[v.ID] = true
		if err := s.EnterAreaTrigger(v.ID); err != nil {
			// The state stays entered: a send that failed at the link is not a new crossing, and the walk reports it.
			return err
		}
	}
	return nil
}

// triggerSamples is the mover's Samples hook: the points on the segment from to that a walk must report before it
// reaches to, so a volume the path cuts through is entered from a position the server holds. A heartbeat's positions
// can straddle a small volume with both ends outside it; the endpoint test alone would then never see the crossing,
// and a trigger sent at the far end is refused ("too far, ignore", MiscHandler.cpp:556-561) because the player's own
// position is what it compares. Each point is the segment's closest approach to the volume's centre, which is strictly
// inside a sphere it crosses, in travel order.
func (s *session) triggerSamples(from, to [3]float32) [][3]float32 {
	if len(s.triggers) == 0 {
		return nil
	}
	seg := objects.Distance(from[0], from[1], from[2], to[0], to[1], to[2])
	if seg == 0 {
		return nil
	}
	var mapID uint32
	if s.world != nil {
		mapID = s.world.MapID()
	}
	type crossing struct {
		at [3]float32
		f  float32
	}
	var crossed []crossing
	for _, v := range s.triggers {
		if v.Map != mapID {
			continue
		}
		// Only a pass-through needs a sample of its own: an endpoint inside the volume is reported anyway.
		if objects.Distance(from[0], from[1], from[2], v.X, v.Y, v.Z) <= v.Radius ||
			objects.Distance(to[0], to[1], to[2], v.X, v.Y, v.Z) <= v.Radius {
			continue
		}
		at, f := segmentClosest(from, to, [3]float32{v.X, v.Y, v.Z})
		if objects.Distance(at[0], at[1], at[2], v.X, v.Y, v.Z) >= v.Radius {
			continue // the straight line passes outside the sphere
		}
		crossed = append(crossed, crossing{at: at, f: f})
	}
	if len(crossed) == 0 {
		return nil
	}
	slices.SortStableFunc(crossed, func(a, b crossing) int {
		switch {
		case a.f < b.f:
			return -1
		case a.f > b.f:
			return 1
		}
		return 0
	})
	out := make([][3]float32, len(crossed))
	for i, c := range crossed {
		out[i] = c.at
	}
	return out
}

// segmentClosest returns the point on the segment from a to b closest to p, and its fraction of the way along it.
func segmentClosest(a, b, p [3]float32) ([3]float32, float32) {
	d := [3]float32{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	dd := d[0]*d[0] + d[1]*d[1] + d[2]*d[2]
	if dd == 0 {
		return a, 0
	}
	t := ((p[0]-a[0])*d[0] + (p[1]-a[1])*d[1] + (p[2]-a[2])*d[2]) / dd
	t = min(max(t, 0), 1)
	return [3]float32{a[0] + d[0]*t, a[1] + d[1]*t, a[2] + d[2]*t}, t
}
