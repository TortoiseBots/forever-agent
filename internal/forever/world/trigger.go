package world

// The area-trigger watch (#228): the client sends CMSG_AREA_TRIGGER itself when its own movement enters a trigger.
// A real client knows every entry of its AreaTrigger data and sends as it steps into one; the server only checks that
// the player really is inside (MiscHandler.cpp:540-561: "too far, ignore" when IsInAreaTrigger says otherwise) and
// then runs the trigger's script, which is how a portal teleports. This agent keeps no table of triggers, so a volume
// is armed by id, map, centre and radius (area_trigger_watch) and the mover's own positions are the crossings: the
// mover calls back after every movement packet, and a volume that was not inside before and is inside now sends the
// packet once. Leaving the volume re-arms it, so a second crossing sends again; nothing else does (one send per
// crossing, and at most one volume state per armed id).

import (
	"context"
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/rpc"
)

const (
	// triggerDefaultRadius is the radius an armed volume gets when the caller passes none: small enough to stand
	// inside a classic entrance sphere (the entries the 1.60.1.70959 client data still carries are 7 yards across a
	// portal that sits exactly on the plan's point, e.g. Stormwind Vault's 107), and large enough that a walking leg
	// (a 500 ms heartbeat at 7 yd/s, 3.5 yd) samples it at least once.
	triggerDefaultRadius float32 = 4
	// triggerMaxRadius bounds an armed volume: a trigger sphere is a few yards, never a zone.
	triggerMaxRadius float32 = 100
	// triggerWatchMax bounds the armed volumes one session carries.
	triggerWatchMax = 32
)

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
		s.triggers, s.triggersIn = nil, nil
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
	// Arming the same id again moves its volume instead of adding a second state.
	replaced := false
	for i := range s.triggers {
		if s.triggers[i].ID == v.ID {
			s.triggers[i], replaced = v, true
			break
		}
	}
	if !replaced {
		s.triggers = append(s.triggers, v)
	}
	if s.triggersIn == nil {
		s.triggersIn = map[int32]bool{}
	}
	delete(s.triggersIn, v.ID) // a moved volume is entered again from wherever the mover is
	out := ServeAreaTriggerWatch{Triggers: append([]srvTrigger(nil), s.triggers...)}
	// A volume armed under the character's feet is a crossing at once: the caller stood in the trigger and asked for
	// it, which is what the client would have done on stepping in.
	if s.mover != nil {
		x, y, z, _ := s.mover.Position()
		if err := s.triggerCrossing([3]float32{x, y, z}); err != nil {
			return out, err
		}
	}
	return out, nil
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

// triggerCrossing sends CMSG_AREA_TRIGGER for every armed volume the mover has just entered, once per crossing: a
// volume the mover is outside of clears its state, so stepping out and back in sends again. The mover calls it after
// every movement packet, so the packet goes out with the server holding the same position.
func (s *session) triggerCrossing(pos [3]float32) error {
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
