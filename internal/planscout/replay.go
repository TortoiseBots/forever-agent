package planscout

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/planexport"
)

// UnmarshalJSON reads a fix file and keeps, per fix, which coordinate fields its JSON leaves out: json's zero value
// cannot tell a recorded 0 from a missing one, and a replay must never read a missing coordinate as 0.
func (ff *FixFile) UnmarshalJSON(b []byte) error {
	type plain FixFile
	var raw struct {
		*plain
		Fixes []json.RawMessage `json:"fixes"`
	}
	raw.plain = (*plain)(ff)
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	ff.Fixes = make([]Fix, len(raw.Fixes))
	ff.unrecorded = make([][]string, len(raw.Fixes))
	for i, rb := range raw.Fixes {
		if err := json.Unmarshal(rb, &ff.Fixes[i]); err != nil {
			return fmt.Errorf("fix %d: %w", i+1, err)
		}
		ff.unrecorded[i] = unrecordedCoords(rb)
	}
	return nil
}

// unrecordedCoords names the coordinate fields a fix's JSON does not record, as "point.y", "stand.z" or "vias[0].x"; a
// null is not a coordinate either.
func unrecordedCoords(b []byte) []string {
	var f struct {
		Point map[string]json.RawMessage   `json:"point"`
		Stand map[string]json.RawMessage   `json:"stand"`
		Vias  []map[string]json.RawMessage `json:"vias"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		// A coordinate object that is not one is refused by the typed decode of the fix itself.
		return nil
	}
	var out []string
	add := func(name string, m map[string]json.RawMessage) {
		for _, k := range [...]string{"x", "y", "z"} {
			raw, ok := m[k]
			if !ok || strings.TrimSpace(string(raw)) == "null" {
				out = append(out, name+"."+k)
			}
		}
	}
	if f.Point != nil {
		add("point", f.Point)
	}
	if f.Stand != nil {
		add("stand", f.Stand)
	}
	for i, v := range f.Vias {
		add(fmt.Sprintf("vias[%d]", i), v)
	}
	return out
}

// VerifiedFixes is a recorded fix list made safe to replay. An applicable fix must be of a kind and with a payload
// ApplyFixes applies, must name the plan element its step's row names in this export (the target is then taken from the
// export, not from the file), and every destination must be a point the file records whole and the map's mesh holds. A fix
// that fails any is dropped with the reason, never applied to a guessed target or to a missing coordinate read as zero and
// never handed on to fail the whole batch; advice fixes (apply false) are returned as recorded.
func (ff FixFile) VerifiedFixes(in Input, nav Nav) ([]Fix, []DroppedFix) {
	out := make([]Fix, 0, len(ff.Fixes))
	var dropped []DroppedFix
	for i, f := range ff.Fixes {
		if !f.Apply {
			out = append(out, f)
			continue
		}
		if reason := ff.replayRefusal(i, f, in, nav); reason != "" {
			dropped = append(dropped, DroppedFix{ID: f.ID, Step: f.Step, Kind: f.Kind, Reason: reason})
			continue
		}
		ref := in.Origins[f.Step-1]
		f.Ref = &ref
		out = append(out, f)
	}
	return out, dropped
}

// replayRefusal says why a recorded fix cannot be replayed against this export, or "".
func (ff FixFile) replayRefusal(i int, f Fix, in Input, nav Nav) string {
	if f.Step < 1 || f.Step > len(in.Rows.Steps) {
		return fmt.Sprintf("step %d is outside this plan's %d exported rows", f.Step, len(in.Rows.Steps))
	}
	if len(in.Origins) < len(in.Rows.Steps) {
		return fmt.Sprintf("step %d's row names no plan element in this export", f.Step)
	}
	origin := in.Origins[f.Step-1]
	if f.Ref == nil {
		return fmt.Sprintf("the fix names no plan element; step %d's row is %s", f.Step, originName(origin))
	}
	if *f.Ref != origin {
		return fmt.Sprintf("the fix names %s; step %d's row is %s in this export", originName(*f.Ref), f.Step, originName(origin))
	}
	if reason := fixRefusal(f); reason != "" {
		return reason
	}
	switch f.Kind {
	case FixMoveStand:
		if miss := ff.missing(i, "stand"); len(miss) > 0 {
			return "the fix records no stand " + strings.Join(miss, ", ") + ": a missing coordinate is never read as zero"
		}
		return meshRefusal(nav, in.Rows.Plan.Map, dungeon.Point{X: f.Stand.X, Y: f.Stand.Y, Z: f.Stand.Z}, "stand")
	case FixSnapPoint, FixInsertVia, FixDryDetour:
		if f.Point != nil {
			if miss := ff.missing(i, "point"); len(miss) > 0 {
				return "the fix records no point " + strings.Join(miss, ", ") + ": a missing coordinate is never read as zero"
			}
			return meshRefusal(nav, in.Rows.Plan.Map, *f.Point, "point")
		}
		for k, p := range f.Vias {
			field := fmt.Sprintf("vias[%d]", k)
			if miss := ff.missing(i, field); len(miss) > 0 {
				return "the fix records no " + field + " " + strings.Join(miss, ", ") + ": a missing coordinate is never read as zero"
			}
			if reason := meshRefusal(nav, in.Rows.Plan.Map, p, field); reason != "" {
				return reason
			}
		}
	}
	return ""
}

// missing is the fields of one recorded coordinate that fix i does not carry, without the field's own name: "y", "z".
func (ff FixFile) missing(i int, field string) []string {
	if i >= len(ff.unrecorded) {
		return nil
	}
	var out []string
	for _, m := range ff.unrecorded[i] {
		if rest, ok := strings.CutPrefix(m, field+"."); ok {
			out = append(out, rest)
		}
	}
	return out
}

// meshRefusal says the point is not on the map's mesh by the plan check's own walk rule — the nearest walkable floor
// within dungeon.WalkFloorDH sideways and dungeon.WalkFloorDZ up or down, whatever window the lookup searched — or "".
func meshRefusal(nav Nav, mapID uint32, p dungeon.Point, name string) string {
	q, ok, err := nav.Nearest(mapID, navmesh.Vec3{p.X, p.Y, p.Z})
	if err != nil {
		return fmt.Sprintf("the %s (%.1f %.1f %.1f) could not be checked on the mesh: %v", name, p.X, p.Y, p.Z, err)
	}
	if !ok {
		return fmt.Sprintf("the %s (%.1f %.1f %.1f) is off the mesh: the map has no floor there", name, p.X, p.Y, p.Z)
	}
	at := dungeon.Point{X: q[0], Y: q[1], Z: q[2]}
	if dungeon.OnFloor(p, at) {
		return ""
	}
	return fmt.Sprintf("the %s (%.1f %.1f %.1f) is off the mesh: the nearest floor is %.1f yd sideways and %.1f yd up or down (a walk point takes %.1f and %.1f)",
		name, p.X, p.Y, p.Z, math.Hypot(float64(at.X-p.X), float64(at.Y-p.Y)), math.Abs(float64(at.Z-p.Z)),
		dungeon.WalkFloorDH, dungeon.WalkFloorDZ)
}

// originName names a row origin as the fix file's reader knows it.
func originName(o planexport.RowOrigin) string {
	var b strings.Builder
	fmt.Fprintf(&b, "leg %d %s", o.Leg, o.Part)
	if o.Part == "step" {
		list := o.List
		if list == "" {
			list = "steps"
		}
		fmt.Fprintf(&b, " %s %d", list, o.Index)
		return b.String()
	}
	if o.Group >= 0 {
		fmt.Fprintf(&b, " group %d", o.Group)
	}
	if o.Index != 0 || o.Part == "via" || o.Part == "approach" {
		fmt.Fprintf(&b, " index %d", o.Index)
	}
	return b.String()
}
