package planscout

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/routeedit"
)

// DroppedFix is an applicable fix that ApplyFixes undid: the edited plan failed dungeon.Parse, the fix added a plan-check
// issue, or it wrote the same plan point as a fix applied before it.
type DroppedFix struct {
	ID     int    `json:"id"`
	Step   int    `json:"step"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// ApplyFixes applies the applicable fixes to a copy of the plan (routeedit.Clone) one at a time, each on the plan the fixes
// before it left: a fix after which dungeon.Parse fails, or that adds a plan-check issue against the base plan, is undone and
// dropped with its reason (the remap spec's A3 rule); a fix that adds none stays. Point moves run first and insertions after
// them (back to front), so an insertion never shifts the element a point move names; an insertion into a stand-pull row's
// approach list lands before the list's stand, so it never becomes the stand a move_stand just repaired. A stand move runs
// before the other point moves, and a later point move that resolves to the same plan slot — a snap of that stand's own
// approach point, for one — is dropped with the conflict named, never left to overwrite the accepted stand. If every
// applicable fix is dropped, an error is returned. The base plan is never changed.
func ApplyFixes(p *dungeon.Plan, fixes []Fix, nav dungeon.Navmesh) (*dungeon.Plan, routeedit.CheckResult, []DroppedFix, error) {
	good, err := routeedit.Clone(p)
	if err != nil {
		return nil, routeedit.CheckResult{}, nil, err
	}
	// The base plan's own issues are what a fix's issues are told from: the check runs once for the whole list.
	base, err := dungeon.CheckPlan(p, nav, 0)
	if err != nil {
		return nil, routeedit.CheckResult{Skipped: err.Error()}, nil, fmt.Errorf("the plan check of the plan could not run: %s", err)
	}
	var todo []Fix
	for _, f := range fixes {
		if f.Apply {
			todo = append(todo, f)
		}
	}
	// Point moves first (highest step first), then insertions: an insertion shifts the indexes of the list it writes to, so
	// applying it before a point move would move the inserted waypoint in place of the one the fix names. Insertions of one
	// list run back to front (the highest index first) for the same reason. A stand move runs before every other point
	// move, so a snap of the point it repairs is the fix the slot conflict refuses, not the stand.
	sort.SliceStable(todo, func(i, j int) bool {
		ai, ains := insertion(todo[i])
		bi, bins := insertion(todo[j])
		switch {
		case ains != bins:
			return !ains
		case ains:
			return originAfter(ai, bi)
		}
		as, bs := todo[i].Kind == FixMoveStand, todo[j].Kind == FixMoveStand
		if as != bs {
			return as
		}
		return todo[i].Step > todo[j].Step
	})
	var dropped []DroppedFix
	written := make(map[pointSlot]Fix, len(todo))
	applied := 0
	name := good.Name
	if name == "" {
		name = "plan"
	}
	for _, f := range todo {
		cand, err := routeedit.Clone(good)
		if err != nil {
			return nil, routeedit.CheckResult{}, dropped, err
		}
		slot, moved, err := applyFix(cand, f)
		if err != nil {
			return nil, routeedit.CheckResult{}, dropped, fmt.Errorf("fix %d (step %d, %s): %w", f.ID, f.Step, f.Kind, err)
		}
		raw, err := dungeon.Marshal(cand)
		if err != nil {
			return nil, routeedit.CheckResult{}, dropped, err
		}
		if _, err := dungeon.Parse(name+".json", raw); err != nil {
			dropped = append(dropped, DroppedFix{ID: f.ID, Step: f.Step, Kind: f.Kind, Reason: err.Error()})
			continue
		}
		res := routeedit.EvaluateAgainst(base, cand, nav)
		if !res.Checked {
			return nil, res, dropped, fmt.Errorf("the plan check of the edited plan could not run: %s", res.Skipped)
		}
		if res.NewIssues > 0 {
			dropped = append(dropped, DroppedFix{ID: f.ID, Step: f.Step, Kind: f.Kind, Reason: planCheckDrop(res)})
			continue
		}
		if moved {
			if prev, ok := written[slot]; ok {
				dropped = append(dropped, DroppedFix{ID: f.ID, Step: f.Step, Kind: f.Kind,
					Reason: fmt.Sprintf("it writes the same plan point as fix %d (step %d, %s), which was applied first: the plan keeps that fix", prev.ID, prev.Step, prev.Kind)})
				continue
			}
		}
		good = cand
		if moved {
			written[slot] = f
		}
		applied++
	}
	if len(todo) > 0 && applied == 0 {
		return nil, routeedit.CheckResult{}, dropped, fmt.Errorf("every applicable fix (%d) was dropped (the plan loader, the plan check or a shared plan point refused each one)", len(todo))
	}
	// Every accepted fix passed this check, so the copy adds no issue; the check is run once more for its counts and issues.
	res := routeedit.EvaluateAgainst(base, good, nav)
	if !res.Checked {
		return nil, res, dropped, fmt.Errorf("the plan check of the edited plan could not run: %s", res.Skipped)
	}
	if res.NewIssues > 0 {
		return nil, res, dropped, fmt.Errorf("the fixes add %d plan-check issue(s): %s", res.NewIssues, res.Refusal())
	}
	return good, res, dropped, nil
}

// planCheckDrop is a dropped fix's reason when the plan check refused it: the issues the fix adds, by name and point, so
// dropped.json says what the fix broke.
func planCheckDrop(res routeedit.CheckResult) string {
	var texts []string
	for _, is := range res.Issues {
		if is.New {
			texts = append(texts, is.Text)
		}
	}
	return fmt.Sprintf("the fix adds %d plan-check issue(s): %s", res.NewIssues, strings.Join(texts, "; "))
}

// insertion says the fix adds vias to a plan element instead of moving a point, and the element whose list it inserts into.
func insertion(f Fix) (planexport.RowOrigin, bool) {
	if f.Ref == nil || f.Point != nil || len(f.Vias) == 0 {
		return planexport.RowOrigin{}, false
	}
	switch f.Kind {
	case FixInsertVia, FixDryDetour:
		return *f.Ref, true
	}
	return planexport.RowOrigin{}, false
}

// originAfter says a names a later element than b, in the same list: its insertion runs first, so it does not shift the
// index b's insertion names.
func originAfter(a, b planexport.RowOrigin) bool {
	if a.Leg != b.Leg {
		return a.Leg > b.Leg
	}
	if a.Part != b.Part {
		return a.Part > b.Part
	}
	if a.Group != b.Group {
		return a.Group > b.Group
	}
	return a.Index > b.Index
}

// fixRefusal says why a fix's kind and payload cannot be applied, whatever plan it names: ApplyFixes's own rule, so a
// replay refuses such a fix with its reason instead of handing it on and failing the whole batch.
func fixRefusal(f Fix) string {
	switch f.Kind {
	case FixSnapPoint:
		if f.Point == nil {
			return "a snap_point fix records no point (vias fit an insert_via or dry_detour fix)"
		}
	case FixMoveStand:
		if f.Stand == nil {
			return "the fix records no stand"
		}
	case FixInsertVia, FixDryDetour:
		if f.Point == nil && len(f.Vias) == 0 {
			return "the fix records no point and no vias"
		}
	default:
		return fmt.Sprintf("fix kind %q is not applied", f.Kind)
	}
	return ""
}

// applyFix applies f to p and says whether it moved a point, and which plan slot it wrote: the slot is how a later fix
// that names the same point is told from one that names another.
func applyFix(p *dungeon.Plan, f Fix) (pointSlot, bool, error) {
	if f.Ref == nil {
		return pointSlot{}, false, fmt.Errorf("no plan element to edit")
	}
	if reason := fixRefusal(f); reason != "" {
		return pointSlot{}, false, errors.New(reason)
	}
	ref := *f.Ref
	if ref.Leg < 0 || ref.Leg >= len(p.Legs) {
		return pointSlot{}, false, fmt.Errorf("leg %d is outside the plan", ref.Leg)
	}
	switch f.Kind {
	case FixSnapPoint:
		slot, err := movePoint(p, ref, *f.Point)
		return slot, true, err
	case FixMoveStand:
		slot, err := moveStand(p, ref, dungeon.Point{X: f.Stand.X, Y: f.Stand.Y, Z: f.Stand.Z})
		return slot, true, err
	case FixInsertVia, FixDryDetour:
		if f.Point != nil {
			slot, err := movePoint(p, ref, *f.Point)
			return slot, true, err
		}
		return pointSlot{}, false, insertVias(p, ref, f.Vias)
	}
	return pointSlot{}, false, fmt.Errorf("fix kind %q is not applied", f.Kind)
}

// element is where a fix writes: the via list it inserts into, or the point it moves.
func insertVias(p *dungeon.Plan, ref planexport.RowOrigin, vias []dungeon.Point) error {
	leg := &p.Legs[ref.Leg]
	switch ref.Part {
	case "to":
		for _, v := range vias {
			leg.Via = append(leg.Via, dungeon.Waypoint{Point: v})
		}
		return nil
	case "via":
		if ref.Index < 0 || ref.Index > len(leg.Via) {
			return fmt.Errorf("via index %d is outside the leg", ref.Index)
		}
		var out []dungeon.Waypoint
		out = append(out, leg.Via[:ref.Index]...)
		for _, v := range vias {
			out = append(out, dungeon.Waypoint{Point: v})
		}
		out = append(out, leg.Via[ref.Index:]...)
		leg.Via = out
		return nil
	case "approach":
		if ref.Group < 0 {
			if leg.Boss == nil {
				return fmt.Errorf("the leg has no boss")
			}
			return addApproach(&leg.Boss.Approach, ref.Index, vias, leg.Boss.StandPull)
		}
		if ref.Group >= len(leg.PullGroups) {
			return fmt.Errorf("pull group %d is outside the leg", ref.Group)
		}
		g := &leg.PullGroups[ref.Group]
		return addApproach(&g.Approach, ref.Index, vias, g.StandPull)
	case "pull", "hold":
		if ref.Group < 0 || ref.Group >= len(leg.PullGroups) {
			return fmt.Errorf("pull group %d is outside the leg", ref.Group)
		}
		g := &leg.PullGroups[ref.Group]
		return addApproach(&g.Approach, len(g.Approach), vias, g.StandPull)
	case "boss":
		if leg.Boss == nil {
			return fmt.Errorf("the leg has no boss")
		}
		return addApproach(&leg.Boss.Approach, len(leg.Boss.Approach), vias, leg.Boss.StandPull)
	}
	return fmt.Errorf("a %q row has no plan element to insert vias into", ref.Part)
}

// addApproach inserts vias into an approach list at index (len(ap) appends). A stand-pull list ends in the party's stand,
// so the insertion stays before that last point: an insertion after it would become the stand.
func addApproach(ap *[]dungeon.Point, index int, vias []dungeon.Point, standPull bool) error {
	if index < 0 || index > len(*ap) {
		return fmt.Errorf("approach index %d is outside the group", index)
	}
	if standPull && index > standSlot(len(*ap)) {
		index = standSlot(len(*ap))
	}
	*ap = insertAt(*ap, index, vias)
	return nil
}

// standSlot is the last slot before an approach list's stand: where an insertion into a stand-pull list goes.
func standSlot(n int) int {
	if n > 0 {
		return n - 1
	}
	return 0
}

// insertAt returns the points with the vias inserted before the point at index.
func insertAt(points []dungeon.Point, index int, vias []dungeon.Point) []dungeon.Point {
	out := make([]dungeon.Point, 0, len(points)+len(vias))
	out = append(out, points[:index]...)
	out = append(out, vias...)
	out = append(out, points[index:]...)
	return out
}

// pointSlot names the one plan slot a point move writes. A snap of a pull row's final approach point and a move_stand on
// that row both write the group's stand, so both resolve to one slot and the later fix is refused, not left to overwrite
// the first.
type pointSlot struct {
	Leg   int
	Part  string
	Group int
	Index int
	List  string
}

// movePoint moves the plan element a row names to pt and returns the slot it wrote.
func movePoint(p *dungeon.Plan, ref planexport.RowOrigin, pt dungeon.Point) (pointSlot, error) {
	slot, set, err := resolveMove(p, ref, false)
	if err != nil {
		return pointSlot{}, err
	}
	set(pt)
	return slot, nil
}

// moveStand moves the stand a row names to pt and returns the slot it wrote.
func moveStand(p *dungeon.Plan, ref planexport.RowOrigin, pt dungeon.Point) (pointSlot, error) {
	slot, set, err := resolveMove(p, ref, true)
	if err != nil {
		return pointSlot{}, err
	}
	set(pt)
	return slot, nil
}

// setPoint moves the plan element a row names to pt.
func setPoint(p *dungeon.Plan, ref planexport.RowOrigin, pt dungeon.Point) error {
	_, err := movePoint(p, ref, pt)
	return err
}

// resolveMove resolves the plan slot a point or stand move writes, and a setter for it. The slot is where two fixes that
// share a point meet: the last approach point of a list is its stand, so a snap of it and a stand move of its row resolve
// to the same slot.
func resolveMove(p *dungeon.Plan, ref planexport.RowOrigin, standMove bool) (pointSlot, func(dungeon.Point), error) {
	leg := &p.Legs[ref.Leg]
	if standMove {
		if ref.Part == "boss" || (ref.Part == "pull" && ref.Group < 0) {
			// A boss row (an older fix file names it part "pull" with group -1).
			if leg.Boss == nil {
				return pointSlot{}, nil, fmt.Errorf("the leg has no boss")
			}
			if leg.Boss.Hold != nil {
				spot := &leg.Boss.Hold.Spot
				return pointSlot{Leg: ref.Leg, Part: "hold", Group: -1}, func(pt dungeon.Point) { *spot = pt }, nil
			}
			return pointSlot{Leg: ref.Leg, Part: "stand", Group: -1}, func(pt dungeon.Point) {
				if n := len(leg.Boss.Approach); n > 0 {
					leg.Boss.Approach[n-1] = pt
				} else {
					leg.Boss.Approach = []dungeon.Point{pt}
				}
			}, nil
		}
		if ref.Part == "pull" {
			if ref.Group >= len(leg.PullGroups) {
				return pointSlot{}, nil, fmt.Errorf("pull group %d is outside the leg", ref.Group)
			}
			g := &leg.PullGroups[ref.Group]
			return pointSlot{Leg: ref.Leg, Part: "stand", Group: ref.Group}, func(pt dungeon.Point) {
				if n := len(g.Approach); n > 0 {
					g.Approach[n-1] = pt
				} else {
					g.Approach = []dungeon.Point{pt}
				}
			}, nil
		}
	}
	switch ref.Part {
	case "via":
		if ref.Index < 0 || ref.Index >= len(leg.Via) {
			return pointSlot{}, nil, fmt.Errorf("via index %d is outside the leg", ref.Index)
		}
		w := &leg.Via[ref.Index]
		if w.Direct || w.Exact || w.CheckSkip != "" {
			return pointSlot{}, nil, fmt.Errorf("via %d is locked (direct, exact or check_skip): the navmesh cannot judge it", ref.Index)
		}
		return pointSlot{Leg: ref.Leg, Part: "via", Index: ref.Index}, func(pt dungeon.Point) { w.Point = pt }, nil
	case "to":
		return pointSlot{Leg: ref.Leg, Part: "to"}, func(pt dungeon.Point) { leg.To = pt }, nil
	case "approach":
		if ref.Group >= 0 && ref.Group < len(leg.PullGroups) {
			ap := &leg.PullGroups[ref.Group].Approach
			if ref.Index < 0 || ref.Index >= len(*ap) {
				return pointSlot{}, nil, fmt.Errorf("approach index %d is outside the group", ref.Index)
			}
			return approachSlot(ref.Leg, ref.Group, ref.Index, len(*ap)), func(pt dungeon.Point) { (*ap)[ref.Index] = pt }, nil
		}
		if leg.Boss != nil && ref.Index >= 0 && ref.Index < len(leg.Boss.Approach) {
			ap := &leg.Boss.Approach
			return approachSlot(ref.Leg, -1, ref.Index, len(*ap)), func(pt dungeon.Point) { (*ap)[ref.Index] = pt }, nil
		}
		return pointSlot{}, nil, fmt.Errorf("no approach point %d to move", ref.Index)
	case "pull":
		if ref.Group < 0 || ref.Group >= len(leg.PullGroups) {
			return pointSlot{}, nil, fmt.Errorf("pull group %d is outside the leg", ref.Group)
		}
		g := &leg.PullGroups[ref.Group]
		if len(g.Approach) > 0 {
			return pointSlot{Leg: ref.Leg, Part: "stand", Group: ref.Group}, func(pt dungeon.Point) {
				g.Approach[len(g.Approach)-1] = pt
			}, nil
		}
		if len(g.Positions) == 0 {
			return pointSlot{}, nil, fmt.Errorf("pull group %d has no position", ref.Group)
		}
		c := centre(g.Positions)
		return pointSlot{Leg: ref.Leg, Part: "positions", Group: ref.Group}, func(pt dungeon.Point) {
			dx, dy, dz := pt.X-c.X, pt.Y-c.Y, pt.Z-c.Z
			for i := range g.Positions {
				g.Positions[i].X += dx
				g.Positions[i].Y += dy
				g.Positions[i].Z += dz
			}
		}, nil
	case "hold":
		if ref.Group < 0 || ref.Group >= len(leg.PullGroups) || leg.PullGroups[ref.Group].Hold == nil {
			return pointSlot{}, nil, fmt.Errorf("no hold to move")
		}
		spot := &leg.PullGroups[ref.Group].Hold.Spot
		return pointSlot{Leg: ref.Leg, Part: "hold", Group: ref.Group}, func(pt dungeon.Point) { *spot = pt }, nil
	case "boss":
		if leg.Boss == nil {
			return pointSlot{}, nil, fmt.Errorf("the leg has no boss")
		}
		if leg.Boss.Hold != nil {
			// The row is the boss's hold: its coordinates are the hold's spot (the hold walks the party there).
			spot := &leg.Boss.Hold.Spot
			return pointSlot{Leg: ref.Leg, Part: "hold", Group: -1}, func(pt dungeon.Point) { *spot = pt }, nil
		}
		if leg.Boss.StandPull && len(leg.Boss.Approach) > 0 {
			return pointSlot{Leg: ref.Leg, Part: "stand", Group: -1}, func(pt dungeon.Point) {
				leg.Boss.Approach[len(leg.Boss.Approach)-1] = pt
			}, nil
		}
		return pointSlot{Leg: ref.Leg, Part: "boss"}, func(pt dungeon.Point) { leg.Boss.Position = pt }, nil
	case "step":
		// A step's stand (the row a step walks the party to): the step the origin names, in the list it names.
		steps, list := leg.Steps, "steps"
		switch ref.List {
		case "", "steps":
		case "after_boss":
			steps, list = leg.AfterBoss, "after_boss"
		default:
			return pointSlot{}, nil, fmt.Errorf("a step row names list %q, not steps or after_boss", ref.List)
		}
		if ref.Index < 0 || ref.Index >= len(steps) {
			return pointSlot{}, nil, fmt.Errorf("%s index %d is outside the leg", list, ref.Index)
		}
		st := &steps[ref.Index]
		slot := pointSlot{Leg: ref.Leg, Part: "step", Index: ref.Index, List: list}
		switch {
		case st.UseObject != nil && st.UseObject.Stand != nil:
			return slot, func(pt dungeon.Point) { *st.UseObject.Stand = pt }, nil
		case st.LootObject != nil && st.LootObject.Stand != nil:
			return slot, func(pt dungeon.Point) { *st.LootObject.Stand = pt }, nil
		case st.AreaTrigger != nil:
			return slot, func(pt dungeon.Point) { st.AreaTrigger.Stand = pt }, nil
		}
		return pointSlot{}, nil, fmt.Errorf("%s %d has no stand point to move", list, ref.Index)
	}
	return pointSlot{}, nil, fmt.Errorf("a %q row has no plan element to move", ref.Part)
}

// approachSlot is an approach point's slot: the list's last point is its stand, which a stand move writes too, so the two
// resolve to one slot.
func approachSlot(leg, group, index, n int) pointSlot {
	if index == n-1 {
		return pointSlot{Leg: leg, Part: "stand", Group: group}
	}
	return pointSlot{Leg: leg, Part: "approach", Group: group, Index: index}
}

// setStand moves the stand a row names to pt. A pull row names its group's stand, the last approach point (one is created
// when the group has none); a boss row names the party's stand, the last approach point before the boss (also created when
// the leg has none) — never the boss's own position, which is where the boss stands, not where the party does. A boss
// row whose boss has a hold is the hold, though: its coordinates are the hold's spot, which the hold walks the party to,
// so its stand move is a move of that spot. Every other row names the plan element it sits on (the leg's to point, a
// via, an approach point or a step's stand), so its stand move is a move of that element.
func setStand(p *dungeon.Plan, ref planexport.RowOrigin, pt dungeon.Point) error {
	_, err := moveStand(p, ref, pt)
	return err
}

func centre(ps []dungeon.Point) dungeon.Point {
	var c dungeon.Point
	for _, p := range ps {
		c.X += p.X
		c.Y += p.Y
		c.Z += p.Z
	}
	n := float32(len(ps))
	return dungeon.Point{X: c.X / n, Y: c.Y / n, Z: c.Z / n}
}
