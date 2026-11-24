package tactics

// The check a watched Forever run's report makes on the plan it watched: the plan's bosses[].tactics blocks are the
// agent's own fight steps (this package's file format), while a watch runs on the server module, whose bots fight by
// their own boss tactics rows (the module's ai_boss_tactic table, loaded here by LoadModule). The plan's block is never
// exported to the module (planexport/unexpressed.go), so a plan step the module's rows do not carry is a step no one
// runs, and a step they contradict is worse: the module plays the fight its own way. The comparison reads the three
// axes the two vocabularies share (the interrupts, the kill order and the avoid rows); a plan step no module row can
// answer is listed as not checked, never passed.
//
// A "fight step" is one instruction of a plan boss's tactics block: one interrupt entry, one kill_first entry, one
// never_target entry, one move_out row. The report counts them, so a plan that agrees with the module says so with a
// number, and a plan the module's file cannot judge says not checked.

import (
	"fmt"
	"strings"
)

// BossBlock is one boss of a plan to check: the leg it stands in, the map and boss entry the module's rows are keyed
// by, and the plan's own tactics block (nil or empty when the plan names none).
type BossBlock struct {
	Leg     int
	LegName string
	Map     uint32
	Entry   uint32
	Tactics *Tactics
}

// Conflict is one plan fight step the module's rows contradict. Step is the plan's instruction as written
// ("interrupt \"Ice Bolt\"", "kill_first 7209", the whole kill order for an order conflict); Why is what the module's
// rows say instead. A module row the plan's block does not mention is not a conflict: the plan's block names the
// steps it cares about, and the check judges only those.
type Conflict struct {
	Leg     int
	LegName string
	Boss    uint32
	Step    string
	Why     string
}

// String is the conflict as one report line: the leg and boss, the plan's step, and the module's answer.
func (c Conflict) String() string {
	return fmt.Sprintf("%s boss %d: %s: %s", where(c.Leg, c.LegName), c.Boss, c.Step, c.Why)
}

// Report is one plan's comparison: the bosses whose rows were read, the plan fight steps judged, every conflict, and
// every plan step or boss the module's rows could not answer.
type Report struct {
	Bosses    int
	Steps     int
	Conflicts []Conflict
	Unchecked []string
}

// NotChecked reports a comparison that judged no plan step at all (no tactics blocks, every boss without module rows,
// no spell data, an empty file): such a report is never a pass.
func (r Report) NotChecked() bool { return r.Steps == 0 }

// where is a leg as the report lines name it: `leg 2 "hall"`.
func where(leg int, name string) string {
	if name == "" {
		return fmt.Sprintf("leg %d", leg)
	}
	return fmt.Sprintf("leg %d %q", leg, name)
}

// differ is one block's comparison in progress.
type differ struct {
	rep    *Report
	block  BossBlock
	rows   []ModuleRow
	nameOf func(uint32) string
	idsOf  func(string) []uint32
}

// Compare judges every fight step of the blocks' plans against the module's rows. nameOf and idsOf are the spell
// table's two readings (a name for an id, the ids a name has); nil when the build carries no spell data, which makes
// a step named by name not checked.
func Compare(m *Module, blocks []BossBlock, nameOf func(uint32) string, idsOf func(string) []uint32) Report {
	var rep Report
	for _, b := range blocks {
		if b.Tactics == nil || b.Tactics.Empty() {
			continue
		}
		rows := m.Rows(b.Map, b.Entry)
		if len(rows) == 0 {
			rep.Unchecked = append(rep.Unchecked, fmt.Sprintf("%s boss %d: the module has no tactics rows for this boss: its plan fight steps are not checked", where(b.Leg, b.LegName), b.Entry))
			continue
		}
		rep.Bosses++
		d := differ{rep: &rep, block: b, rows: rows, nameOf: nameOf, idsOf: idsOf}
		d.interrupts()
		d.killOrder()
		d.moveOuts()
		d.dispels()
	}
	return rep
}

// conflict records one contradiction against the block's boss.
func (d *differ) conflict(step, why string) {
	d.rep.Conflicts = append(d.rep.Conflicts, Conflict{Leg: d.block.Leg, LegName: d.block.LegName, Boss: d.block.Entry, Step: step, Why: why})
}

// uncheck records one plan step (or boss) the module's rows cannot answer.
func (d *differ) uncheck(step, why string) {
	d.rep.Unchecked = append(d.rep.Unchecked, fmt.Sprintf("%s boss %d: %s: %s", where(d.block.Leg, d.block.LegName), d.block.Entry, step, why))
}

// spellStep is a plan spell reference as a report step names it: `interrupt 9532`, `move_out "Fire Nova"`.
func spellStep(kind string, ref SpellRef) string {
	if ref.ID != 0 {
		return fmt.Sprintf("%s %d", kind, ref.ID)
	}
	return fmt.Sprintf("%s %q", kind, ref.Name)
}

// spell is a spell id as the report names it: its name and id, or the id alone.
func (d *differ) spell(id uint32) string {
	if d.nameOf != nil {
		if name := d.nameOf(id); name != "" {
			return fmt.Sprintf("%s (%d)", name, id)
		}
	}
	return fmt.Sprint(id)
}

// resolve reads a plan spell reference as the ids the spell table holds: a by-id reference is its own id, a by-name one
// is every id that name has. ok is false when the step cannot be judged (no spell data, or no spell of that name), and
// the step is listed as not checked.
func (d *differ) resolve(step string, ref SpellRef) (ids []uint32, ok bool) {
	if ref.ID != 0 {
		return []uint32{ref.ID}, true
	}
	if d.idsOf == nil {
		d.uncheck(step, "no spell data in this build to resolve the name")
		return nil, false
	}
	ids = d.idsOf(ref.Name)
	if len(ids) == 0 {
		d.uncheck(step, fmt.Sprintf("the spell data has no spell named %q", ref.Name))
		return nil, false
	}
	return ids, true
}

// interrupts compares the plan's interrupt steps with the module's interrupt rows (kind interrupt).
func (d *differ) interrupts() {
	var modIDs []uint32
	seen := map[uint32]bool{}
	for _, r := range d.rows {
		if r.Kind == KindInterrupt && !seen[r.Spell] {
			seen[r.Spell] = true
			modIDs = append(modIDs, r.Spell)
		}
	}
	for _, ref := range d.block.Tactics.Interrupt {
		step := spellStep("interrupt", ref)
		ids, ok := d.resolve(step, ref)
		if !ok {
			continue
		}
		d.rep.Steps++
		hit := false
		for _, id := range ids {
			hit = hit || seen[id]
		}
		if !hit {
			d.conflict(step, d.interruptWhy(modIDs))
		}
	}
}

// interruptWhy is what the module's interrupt rows say when the plan names a spell they do not.
func (d *differ) interruptWhy(modIDs []uint32) string {
	if len(modIDs) == 0 {
		return "the module's rows have no interrupt row for it"
	}
	names := make([]string, len(modIDs))
	for i, id := range modIDs {
		names[i] = d.spell(id)
	}
	return fmt.Sprintf("the module's rows interrupt %s only", strings.Join(names, ", "))
}

// killOrder compares the plan's kill_first and never_target steps with the module's kill_first rows: the entries, the
// order (by the module's priorities, lower first) and the contradiction of a plan that never attacks a unit the module
// kills first.
func (d *differ) killOrder() {
	t := d.block.Tactics
	priority := map[uint32]int{}
	for _, r := range d.rows {
		if r.Kind != KindKillFirst {
			continue
		}
		if _, ok := priority[r.Target]; !ok {
			priority[r.Target] = r.Priority
		}
	}
	for _, e := range t.KillFirst {
		d.rep.Steps++
		if _, ok := priority[e]; !ok {
			d.conflict(fmt.Sprintf("kill_first %d", e), "the module has no kill_first row for it")
		}
	}
	for _, e := range t.NeverTarget {
		step := fmt.Sprintf("never_target %d", e)
		if _, ok := priority[e]; ok {
			d.rep.Steps++
			d.conflict(step, "the module kills it first")
			continue
		}
		d.uncheck(step, "the module's rows have no never_target kind, so nothing says whether its bots attack it")
	}
	// The plan's own order against the module's priorities: the first pair they disagree on, once.
	for i := range t.KillFirst {
		for j := i + 1; j < len(t.KillFirst); j++ {
			a, b := t.KillFirst[i], t.KillFirst[j]
			pa, oka := priority[a]
			pb, okb := priority[b]
			if !oka || !okb || pa == pb || pa < pb {
				continue
			}
			d.conflict("kill_first "+entryList(t.KillFirst), fmt.Sprintf("the module's priorities kill %d (%d) before %d (%d)", b, pb, a, pa))
			return
		}
	}
}

// moveOuts compares the plan's move_out steps with the module's avoid rows (kind avoid_cast): a spell the module has no
// row for is a step no one runs, and a smaller radius is a plan that leaves less room than the module will. A name
// resolves to every id the spell table has, and one spell's rows may leave different radii (a target entry or a health
// band): the widest of them is the plan's requirement. A ground effect has no module row, so it is listed as not
// checked.
func (d *differ) moveOuts() {
	// A spell may have several avoid_cast rows (a target entry or a health band splits them): the plan must leave the
	// widest radius any of them leaves.
	avoid := map[uint32]float32{}
	for _, r := range d.rows {
		if r.Kind != KindAvoidCast {
			continue
		}
		if yards, ok := avoid[r.Spell]; !ok || r.Param > yards {
			avoid[r.Spell] = r.Param
		}
	}
	for _, mv := range d.block.Tactics.MoveOut {
		if mv.Ground != nil {
			d.uncheck(spellStep("move_out", *mv.Ground)+" (ground)", "the module's rows have no ground-effect row the check reads")
			continue
		}
		if mv.Cast == nil {
			d.uncheck("move_out", "the rule names neither a cast nor a ground effect")
			continue
		}
		step := spellStep("move_out", *mv.Cast)
		ids, ok := d.resolve(step, *mv.Cast)
		if !ok {
			continue
		}
		d.rep.Steps++
		// A name may resolve to several ids (every rank): the row of any one of them counts, and the widest it leaves
		// is the radius the plan must reach.
		var yards float32
		found := false
		for _, id := range ids {
			if r, ok := avoid[id]; ok {
				found = true
				if r > yards {
					yards = r
				}
			}
		}
		if !found {
			d.conflict(step, "the module's rows have no avoid_cast row for it")
			continue
		}
		if mv.YardsOf() < yards {
			d.conflict(step, fmt.Sprintf("the plan leaves %g yd; the module's avoid_cast row leaves %g yd", mv.YardsOf(), yards))
		}
	}
}

// dispels lists the plan's dispel and no_dispel steps as not checked: the module's table carries a dispel rank, not a
// dispel instruction, so no row of it can say whether the plan's step is right.
func (d *differ) dispels() {
	t := d.block.Tactics
	for _, ref := range t.Dispel {
		d.uncheck(spellStep("dispel", ref), "the module's rows have no dispel row the check reads")
	}
	for _, ref := range t.NoDispel {
		d.uncheck(spellStep("no_dispel", ref), "the module's rows have no dispel row the check reads")
	}
}

// entryList is a plan kill order as a report step names it: `8002, 8001`.
func entryList(entries []uint32) string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = fmt.Sprint(e)
	}
	return strings.Join(out, ", ")
}
