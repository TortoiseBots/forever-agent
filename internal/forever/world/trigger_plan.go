package world

// The trigger volumes the daemon's own plan files name, per map (issue #251, row A6 of the live re-check): a plan's
// entrance trigger at its portal point on the outer map, and every area_trigger step's zone on the plan's map. A walk
// arms the volumes its route crosses for the walk's own duration (armWalkTriggers), so stepping into an instance portal
// fires its trigger the way a real client does — the client keeps the AreaTrigger data and sends as it steps in — with
// no area_trigger_watch call first. The explicit method stays: a volume the caller armed by hand is left alone, and the
// walk removes exactly what it added.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/objects"
)

// planTriggerFile is the part of a plan file these volumes come from: the plan's map, its entrance trigger and portal,
// and its legs' area_trigger steps. Nothing is validated here: a plan this build refuses still names its trigger, and a
// file that cannot be read names no volume.
type planTriggerFile struct {
	Map             uint32 `json:"map"`
	EntranceTrigger int32  `json:"entrance_trigger"`
	Portal          *struct {
		Map uint32  `json:"map"`
		X   float32 `json:"x"`
		Y   float32 `json:"y"`
		Z   float32 `json:"z"`
	} `json:"portal"`
	Legs []struct {
		Steps []struct {
			AreaTrigger *struct {
				ID     int32 `json:"id"`
				Center struct {
					X float32 `json:"x"`
					Y float32 `json:"y"`
					Z float32 `json:"z"`
				} `json:"center"`
				Radius float32 `json:"radius"`
			} `json:"area_trigger"`
		} `json:"steps"`
	} `json:"legs"`
}

// planTriggerVolumes is the daemon's trigger-volume index: built once, from the plan folder serve was started with.
func (st *serveState) planTriggerVolumes() map[uint32][]srvTrigger {
	st.planTriggersOnce.Do(func() {
		st.planTriggers = loadPlanTriggerVolumes(st.plansFolder())
	})
	return st.planTriggers
}

// loadPlanTriggerVolumes indexes every trigger volume the plan files of dir name, by map, in plan-name order. A plan's
// entrance trigger is a volume of the portal's own radius on the outer map (the trigger sphere a classic entrance sits
// in is 7 yd across the plan's point, so triggerDefaultRadius covers it); an area_trigger step is its own centre and
// radius on the plan's map. An id named twice in one map is one volume: the first plan that names it wins, so a
// duplicated trigger cannot be armed in two places.
func loadPlanTriggerVolumes(dir string) map[uint32][]srvTrigger {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	out := map[uint32][]srvTrigger{}
	seen := map[uint32]map[int32]bool{}
	add := func(v srvTrigger) bool {
		if v.ID <= 0 {
			return false
		}
		if seen[v.Map] == nil {
			seen[v.Map] = map[int32]bool{}
		}
		if seen[v.Map][v.ID] {
			return false
		}
		seen[v.Map][v.ID] = true
		out[v.Map] = append(out[v.Map], v)
		return true
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var p planTriggerFile
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		if p.Portal != nil && p.EntranceTrigger > 0 {
			add(srvTrigger{ID: p.EntranceTrigger, Map: p.Portal.Map, X: p.Portal.X, Y: p.Portal.Y, Z: p.Portal.Z,
				Radius: triggerDefaultRadius})
		}
		for _, leg := range p.Legs {
			for _, step := range leg.Steps {
				t := step.AreaTrigger
				if t == nil || t.ID <= 0 || t.Radius <= 0 || t.Radius > triggerMaxRadius {
					continue
				}
				add(srvTrigger{ID: t.ID, Map: p.Map, X: t.Center.X, Y: t.Center.Y, Z: t.Center.Z, Radius: t.Radius})
			}
		}
	}
	return out
}

// planTriggerVolumesForMap is the current map's share of the plan trigger volumes: none outside a serving daemon (a
// one-shot run keeps no plan folder) and none for a map the plans do not name.
func (s *session) planTriggerVolumesForMap() []srvTrigger {
	if s.serveRT == nil || s.serveRT.st == nil {
		return nil
	}
	return s.serveRT.st.planTriggerVolumes()[s.world.MapID()]
}

// armWalkTriggers arms every plan trigger volume the walk's route crosses on the current map, and returns the disarm
// that removes exactly the volumes it added. The route starts at the mover's own position and then follows points. An
// id the caller already armed stays the caller's volume (and stays armed): arming it again would move it, and one state
// per id is what keeps a retried arm from sending the trigger twice. A walk that arms nothing gets a disarm that does
// nothing.
func (s *session) armWalkTriggers(points [][3]float32) func() {
	if s.mover == nil {
		return func() {}
	}
	vols := s.planTriggerVolumesForMap()
	if len(vols) == 0 {
		return func() {}
	}
	x, y, z, _ := s.mover.Position()
	route := make([][3]float32, 0, len(points)+1)
	route = append(route, [3]float32{x, y, z})
	route = append(route, points...)
	var added []int32
	for _, v := range vols {
		if s.triggerArmed(v.ID) || !routeCrossesVolume(route, v) {
			continue
		}
		if len(s.triggers) >= triggerWatchMax {
			s.opts.logf("world: the walk crosses trigger %d, but the watch already holds %d volumes: it is not armed",
				v.ID, triggerWatchMax)
			continue
		}
		s.armTrigger(v)
		added = append(added, v.ID)
	}
	if len(added) == 0 {
		return func() {}
	}
	s.opts.logf("world: the walk arms trigger volume(s) %v for its own duration (route of %d point(s))", added, len(route))
	return func() {
		for _, id := range added {
			s.disarmTrigger(id)
		}
	}
}

// routeCrossesVolume reports whether any segment of the route comes within the volume's radius of its centre: a point
// the mover passes that close is a crossing the watch's own movement packets report (triggerSamples inserts the closest
// approach of a segment whose ends are outside, and an end inside needs no insertion).
func routeCrossesVolume(route [][3]float32, v srvTrigger) bool {
	if len(route) == 0 {
		return false
	}
	c := [3]float32{v.X, v.Y, v.Z}
	if len(route) == 1 {
		return objects.Distance(route[0][0], route[0][1], route[0][2], v.X, v.Y, v.Z) <= v.Radius
	}
	for i := 1; i < len(route); i++ {
		at, _ := segmentClosest(route[i-1], route[i], c)
		if objects.Distance(at[0], at[1], at[2], v.X, v.Y, v.Z) <= v.Radius {
			return true
		}
	}
	return false
}
