package planlint

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// LintObjects is the game-object presence lint: every object row names a
// spawned object, or the run pauses on it.
const LintObjects = "objects"

// LintObjects checks every object row (open, use_on, open_proof, loot,
// wait_object) against the world's game objects: the row's entry — the
// plan's alternate entry for server when the step names one — must have an
// object spawn within the step's search radius of the row's point. A missing
// object is an error: the run would pause on the step with nothing to use,
// loot or wait for. Rows without a plan step behind them (a boss's loot row:
// a creature, not an object) are not object rows and are skipped. A nil
// world skips the check: without the server's spawn list there is nothing
// to judge by. An empty object list does not skip: a plan with an
// object-backed step against a spawn list with no game objects fails every
// such step, so an old or broken creature-only export cannot approve doors
// and levers unchecked; a plan with no object-backed step still passes.
func CheckObjects(rows planexport.Rows, origins []planexport.RowOrigin, p *dungeon.Plan, w *worldspawn.World, server string) []Finding {
	if p == nil || w == nil {
		return nil
	}
	var out []Finding
	for i, s := range rows.Steps {
		switch s.Kind {
		case planexport.KindOpen, planexport.KindOpenProof, planexport.KindUseOn, planexport.KindLoot, planexport.KindWaitObject:
		default:
			continue
		}
		ref := objectRefOf(origins, p, i)
		if ref == nil {
			continue
		}
		entry := ref.Entry
		if alt := ref.AltEntries[server]; alt != 0 {
			entry = alt
		}
		rng := float64(ref.Range)
		if rng <= 0 {
			rng = dungeon.DefaultObjectRange
		}
		if len(w.Objects) == 0 {
			out = append(out, Finding{
				Step: i + 1, Leg: s.Leg, Kind: s.Kind, Lint: LintObjects,
				Point:    pointOf(s),
				Detail:   fmt.Sprintf("no game objects in the spawn list: no game object %d within %.0f yd of %.1f %.1f %.1f", entry, rng, s.X, s.Y, s.Z),
				Severity: SeverityError,
			})
			continue
		}
		if !objectNear(w.Objects, entry, pointOf(s), rng) {
			out = append(out, Finding{
				Step: i + 1, Leg: s.Leg, Kind: s.Kind, Lint: LintObjects,
				Point:    pointOf(s),
				Detail:   fmt.Sprintf("no game object %d within %.0f yd of %.1f %.1f %.1f", entry, rng, s.X, s.Y, s.Z),
				Severity: SeverityError,
			})
		}
		if s.Kind == planexport.KindOpenProof && s.Flags&planexport.FlagProofCreature == 0 && len(s.Entries) > 0 {
			proof := s.Entries[0]
			radius := float64(s.Radius)
			if !objectNear(w.Objects, proof, pointOf(s), radius) {
				out = append(out, Finding{
					Step: i + 1, Leg: s.Leg, Kind: s.Kind, Lint: LintObjects,
					Point:    pointOf(s),
					Detail:   fmt.Sprintf("no game object %d (the use's proof) within %.0f yd of %.1f %.1f %.1f", proof, radius, s.X, s.Y, s.Z),
					Severity: SeverityError,
				})
			}
		}
	}
	return out
}

// objectRefOf is the plan's object ref behind step row i: the use_object,
// loot_object or gameobject wait the exporter wrote the row from. A row with
// no such step behind it (a boss's loot row, a sweep pull) has no ref.
func objectRefOf(origins []planexport.RowOrigin, p *dungeon.Plan, i int) *dungeon.ObjectRef {
	if i < 0 || i >= len(origins) {
		return nil
	}
	o := origins[i]
	if o.Part != "step" && o.Part != "chest" {
		return nil
	}
	if o.Leg < 0 || o.Leg >= len(p.Legs) {
		return nil
	}
	l := p.Legs[o.Leg]
	var st *dungeon.Step
	switch o.List {
	case "", "steps":
		if o.Index >= 0 && o.Index < len(l.Steps) {
			st = &l.Steps[o.Index]
		}
	case "after_boss":
		if o.Index >= 0 && o.Index < len(l.AfterBoss) {
			st = &l.AfterBoss[o.Index]
		}
	}
	if st == nil {
		return nil
	}
	switch {
	case st.UseObject != nil:
		return &st.UseObject.ObjectRef
	case st.LootObject != nil:
		return &st.LootObject.ObjectRef
	case st.WaitFor != nil && st.WaitFor.GameObject != nil:
		return &st.WaitFor.GameObject.ObjectRef
	}
	return nil
}

// objectNear says an object spawn of entry stands within rng yards (3D) of p.
func objectNear(objects []worldspawn.Object, entry uint32, p dungeon.Point, rng float64) bool {
	for _, o := range objects {
		if o.Entry != entry {
			continue
		}
		if dist3(p, o.Pos) <= rng {
			return true
		}
	}
	return false
}
