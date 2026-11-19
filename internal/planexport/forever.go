package planexport

// The Forever export writes the plan rows a server module that runs dungeon plans reads: the three plan tables, one unit row
// per pack member, boss, boss.first add and patrol unit, and one row per boss for the boss rank. It differs from the run
// leader's export in what identifies a pack: the unit rows carry the creature entry and the member's position, never a
// database spawn id, because a creature's GUID on those servers does not carry the spawn id.

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// Roles of ai_dungeon_step_unit.role. UnitFirst is a boss.first add: the plan gives it no position of its own, so its row
// stands at the boss's point and the reader searches wider there than for a pack row (role=patrol marks a patrol unit,
// whose row stands at the first path point).
const (
	UnitPack   = "pack"
	UnitBoss   = "boss"
	UnitFirst  = "first"
	UnitPatrol = "patrol"
)

// UnitRow is one ai_dungeon_step_unit row: a member of a pull group, a boss, one of the boss's first adds, or a patrol unit.
type UnitRow struct {
	Plan    string
	Leg     int
	Seq     int
	Idx     int
	Entry   uint32
	X, Y, Z float32
	Role    string
	Comment string
}

// BossRow is one ai_dungeon_boss row (the boss rank a server module shows).
type BossRow struct {
	Map   uint32
	Entry uint32
	Name  string
}

// FieldNote names one plan field and what the export did with it.
type FieldNote struct {
	// Field is the JSON field path ("legs[2].pull_groups[0].patrol").
	Field string
	// Where names the plan element in words ("leg 2 (trogg_cave) pull group 0").
	Where string
	// Why says why the field is dropped or refused.
	Why string
}

// ForeverReport is one plan's Forever rows and the field table of what happened to its plan fields.
type ForeverReport struct {
	Rows     Rows
	Units    []UnitRow
	Bosses   []BossRow
	Carried  []string
	Dropped  []FieldNote
	Refused  []FieldNote
	Warnings []string
}

// ForeverHeader is the comment at the top of a Forever export.
const ForeverHeader = `-- Dungeon plan rows for a server module that runs dungeon plans, exported by forever-agent (plan-export --forever).
-- The plan file is the single source: edit it there and export again; no row here is set by hand.
-- A pack is named by creature entry and position; the rows carry no spawn id.
-- Idempotent: one transaction per plan deletes the plan's own rows from the four plan tables child first, then
-- inserts the plan's rows parent first and upserts its boss rows by map and entry. The boss table is shared by
-- every plan on the map, so the export never deletes from it; a boss row's name is only set from a value the plan
-- carries, never cleared.
--
-- Tables: ai_dungeon_plan, ai_dungeon_leg, ai_dungeon_step, ai_dungeon_step_unit, ai_dungeon_boss.
`

// BuildForever maps a plan to the Forever rows and reports every plan field as carried, dropped with a note, or refused.
// A plan with a refused field is not exported: the error names every refused field.
func BuildForever(p *dungeon.Plan, opt Options) (ForeverReport, error) {
	var rep ForeverReport
	c := newClassifier(&rep)
	classifyPlan(c, p, opt)
	if len(rep.Refused) > 0 {
		return rep, fmt.Errorf("plan %s: %s", p.Name, refusedText(rep.Refused))
	}
	res, err := Build(p, opt)
	if err != nil {
		return rep, err
	}
	rep.Rows = res.Rows
	for _, w := range res.Warnings {
		if strings.Contains(w, "not in the rows:") {
			continue // the field table carries these, in the Forever format's own words
		}
		rep.Warnings = append(rep.Warnings, w)
	}
	if rep.Units, err = buildUnitRows(p, res); err != nil {
		return rep, err
	}
	rep.Bosses = buildBossRows(p, rep.Rows)
	for _, u := range rep.Units {
		if u.Role == UnitFirst {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("boss %d first add %d: the plan gives no position for a first add, so its unit row stands at the boss's point with role=%s; the reader searches wider there", bossOfUnit(p, u), u.Entry, UnitFirst))
			break
		}
	}
	return rep, nil
}

// bossOfUnit finds the boss entry whose leg and row the unit belongs to (for a message only).
func bossOfUnit(p *dungeon.Plan, u UnitRow) uint32 {
	for _, l := range p.Legs {
		if l.Boss != nil && l.Name != "" && u.Role == UnitFirst {
			for _, e := range l.Boss.First {
				if e == u.Entry {
					return l.Boss.Entry
				}
			}
		}
	}
	return 0
}

// classifier collects the field table once per field and place.
type classifier struct {
	rep  *ForeverReport
	seen map[string]bool
}

func newClassifier(rep *ForeverReport) *classifier {
	return &classifier{rep: rep, seen: map[string]bool{}}
}

func (c *classifier) carried(field string) {
	key := "c\x00" + field
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.rep.Carried = append(c.rep.Carried, field)
}

func (c *classifier) dropped(field, where, why string) {
	key := "d\x00" + field + "\x00" + where
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.rep.Dropped = append(c.rep.Dropped, FieldNote{Field: field, Where: where, Why: why})
}

func (c *classifier) refused(field, where, why string) {
	key := "r\x00" + field + "\x00" + where
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.rep.Refused = append(c.rep.Refused, FieldNote{Field: field, Where: where, Why: why})
}

// refusedText names every refused field once, for the error and the report.
func refusedText(ns []FieldNote) string {
	var fields []string
	seen := map[string]bool{}
	for _, n := range ns {
		if !seen[n.Field] {
			seen[n.Field] = true
			fields = append(fields, n.Field)
		}
	}
	return "refused fields: " + strings.Join(fields, "; ")
}

// classifyPlan fills the field table: what the rows carry, what is dropped with a note, and what this format cannot carry.
func classifyPlan(c *classifier, p *dungeon.Plan, opt Options) {
	c.carried("version")
	c.carried("name")
	c.carried("map")
	c.carried("legs")
	if p.Kind == "" || p.Kind == dungeon.KindDungeon {
		c.carried("kind")
	} else {
		c.refused("kind", "plan", fmt.Sprintf("kind %q: the rows are a dungeon plan; a raid plan is not carried", p.Kind))
	}
	if p.Status != "" {
		c.carried("status (plan row comment)")
	}
	dropTop := func(field, why string) { c.dropped(field, "plan", why) }
	if p.EntranceTrigger != 0 || p.EntranceTele != "" || p.OutsideTele != "" || p.Portal != nil {
		dropTop("entrance/entrance_trigger/entrance_tele/outside_tele/portal", "no column: the module starts the party inside the instance")
	}
	if len(p.LevelBand) > 0 {
		dropTop("level_band", "no column: the party is the module's own")
	}
	if p.TankMinHP != 0 {
		dropTop("tank_min_hp", "no column: the party's gear is the module's own")
	}
	if len(p.HireComposition) > 0 {
		dropTop("hire_composition", "no column: the party is the module's own")
	}
	if len(p.Fleers) > 0 {
		dropTop("fleers", "no column: the bots' own combat AI handles a fleeing mob")
	}
	if len(p.CCPriority) > 0 {
		c.carried("cc_priority")
	}
	if len(p.CCAlways) > 0 {
		c.carried("cc_always")
	}
	if len(p.KillFirst) > 0 {
		c.carried("kill_first")
	}
	if len(p.NoCC) > 0 {
		dropTop("no_cc", "no column: the module's run makes no crowd-control orders yet")
	}
	if len(p.SummonedKillFirst) > 0 {
		dropTop("summoned_kill_first", "no column: only kill_first is carried")
	}
	if len(p.Notes) > 0 {
		dropTop("notes", "for the reader only")
	}
	if p.PullCeiling != 0 {
		dropTop("pull_ceiling", "no column: the module has its own pull limits")
	}
	if p.Watchdog != nil {
		dropTop("watchdog", "no column: the module has its own stall limits")
	}
	if len(p.Avoid) > 0 {
		if opt.Route != nil {
			c.carried("avoid (exporter route)")
		} else {
			for i, b := range p.Avoid {
				c.dropped(fmt.Sprintf("avoid[%d]", i), fmt.Sprintf("plan, box %q", b.Name),
					"no column: the rows keep the plan's points as written, so nothing in them marks this ground; "+
						avoidInsideText(p, b)+"; pass --mmaps to route the rows around it")
			}
		}
	}
	if p.Team != "" {
		c.dropped("team", "plan "+p.Team, "no plan column: a pack's or boss's team is its row's flag")
	}
	if p.Raid != nil {
		c.refused("raid", "plan", "a raid library is not carried; the rows are a dungeon plan")
	}
	for li, l := range p.Legs {
		at := fmt.Sprintf("legs[%d]", li)
		where := fmt.Sprintf("leg %d (%s)", li, l.Name)
		if !legExported(l, opt) {
			if l.Optional != nil {
				c.dropped(at+".optional", where, fmt.Sprintf("leg left out (it needs item %d); pass --with-optional %s to write it", l.Optional.Item, l.Name))
			}
			if l.SkipExport {
				c.dropped(at+".skip_export", where, fmt.Sprintf("opt-in leg left out; pass --with-legs %s to write it", l.Name))
			}
			if l.Optional == nil && !l.SkipExport {
				c.dropped(at, where, "leg left out by --skip-legs")
			}
			continue
		}
		if l.Optional != nil {
			c.dropped(at+".optional", where, "leg in the rows by --with-optional; the item the leg needs is the operator's to give")
		}
		if l.SkipExport {
			c.dropped(at+".skip_export", where, "opt-in leg in the rows by --with-legs; the opt-in itself is not a row")
		}
		for vi, w := range l.Via {
			vf := fmt.Sprintf("%s.via[%d]", at, vi)
			if w.GMCross {
				c.refused(vf+".gm_cross", where, "a GM move has no row")
			}
			if w.CheckSkip != "" {
				c.dropped(vf+".check_skip", where, "for the plan check only")
			}
			if w.Direct || w.Exact {
				c.carried(vf + ".direct/exact")
			}
		}
		if len(l.Skip) > 0 {
			c.dropped(at+".skip", where+", all its steps",
				fmt.Sprintf("no skip column: entries %s are not marked in any row, so the module does not wait on them only because it finds units by entry and position", entriesText(l.Skip)))
		}
		if len(l.Leave) > 0 {
			c.carried(at + ".leave (leg comment, not read by the module)")
		}
		if len(l.LeftoverEntries) > 0 {
			c.refused(at+".leftover_entries", where, "no loot or sweep rows in this format")
		}
		if l.CompanionsLoot != nil {
			c.dropped(at+".companions_loot", where, "no loot rows in this format; the module's own looting applies")
		}
		if l.NeedsWalk {
			c.dropped(at+".needs_walk", where, "the point is exported as given")
		}
		if l.Rest != nil {
			c.dropped(at+".rest", where, "no column: the module's own rest rules apply")
		}
		if len(l.Notes) > 0 {
			c.dropped(at+".notes", where, "for the reader only")
		}
		for si, st := range l.Steps {
			classifyStep(c, fmt.Sprintf("%s.steps[%d]", at, si), where+fmt.Sprintf(" steps[%d]", si), st)
		}
		for si, st := range l.AfterBoss {
			classifyStep(c, fmt.Sprintf("%s.after_boss[%d]", at, si), where+fmt.Sprintf(" after_boss[%d]", si), st)
		}
		for gi, g := range l.PullGroups {
			gf := fmt.Sprintf("%s.pull_groups[%d]", at, gi)
			gw := where + fmt.Sprintf(" pull group %d", gi)
			c.carried(gf + ".entries/positions")
			if g.Radius != 0 {
				c.carried(gf + ".radius")
			}
			if len(g.Approach) > 0 {
				c.carried(gf + ".approach")
			}
			if g.Ambush {
				c.carried(gf + ".ambush")
			}
			if g.Team != "" {
				c.carried(gf + ".team")
			}
			if g.StandPull {
				c.carried(gf + ".stand_pull/clear_yd")
			}
			if g.MaxBodies != 0 {
				c.carried(gf + ".max_bodies")
			}
			if g.Equiv != "" {
				c.dropped(gf+".equiv", gw, "for the pull-test scenario only")
			}
			if g.NeedsWalk {
				c.dropped(gf+".needs_walk", gw, "the position is exported as given")
			}
			if g.Hold != nil {
				c.refused(gf+".hold", gw, "no hold rows in this format")
			}
			if g.Patrol != nil {
				c.carried(gf + ".patrol (unit rows)")
				c.dropped(gf+".patrol.path/period_s/source", gw, "the unit row carries the first path point; the module finds the patrol by entry")
			}
		}
		if b := l.Boss; b != nil {
			bf := at + ".boss"
			bw := where + fmt.Sprintf(" boss %d", b.Entry)
			c.carried(bf + ".entry/position")
			if len(b.First) > 0 {
				c.carried(bf + ".first (unit rows at the boss's point)")
			}
			if len(b.Approach) > 0 {
				c.carried(bf + ".approach")
			}
			if b.Team != "" {
				c.carried(bf + ".team")
			}
			if b.Radius != 0 {
				c.carried(bf + ".radius")
			}
			if b.WaitS != 0 {
				c.carried(bf + ".wait_s")
			}
			if b.StandPull {
				c.carried(bf + ".stand_pull")
			}
			if b.SkipUnreachable != "" {
				c.carried(bf + ".skip_unreachable")
			}
			if b.Tactics != nil {
				c.dropped(bf+".tactics", bw, "no tactic rows in this format; the module's own boss handling applies")
			}
			if b.CompanionsLoot != nil {
				c.dropped(bf+".companions_loot", bw, "no loot rows in this format")
			}
			if b.NeedsWalk {
				c.dropped(bf+".needs_walk", bw, "the position is exported as given")
			}
			if len(b.PhaseEntries) > 0 {
				c.refused(bf+".phase_entries", bw, "no column for an encounter's other entries")
			}
			if b.LootItem != nil {
				c.refused(bf+".loot_item", bw, "no loot rows in this format")
			}
			if b.SummonOpenStep != 0 {
				c.refused(bf+".summon_open_step", bw, "no use_object steps in this format")
			}
			if b.Hold != nil {
				c.refused(bf+".hold", bw, "no hold rows in this format")
			}
		}
	}
}

// avoidInsideText names the plan elements a box holds, for the report line of a dropped avoid box. The rows keep the plan's
// own points when there is no --mmaps route, so the elements whose point is inside the box are what the reader would have
// to widen: a hop may cross a box without either end in it, which no plan point can show.
func avoidInsideText(p *dungeon.Plan, b dungeon.AvoidBox) string {
	var at []string
	leg := func(li int, l dungeon.Leg, what string, pt dungeon.Point) {
		if b.Contains(pt) {
			at = append(at, fmt.Sprintf("leg %d (%s) %s", li, l.Name, what))
		}
	}
	if b.Contains(p.Entrance) {
		at = append(at, "entrance")
	}
	for li, l := range p.Legs {
		leg(li, l, "to", l.To)
		for vi, w := range l.Via {
			leg(li, l, fmt.Sprintf("via[%d]", vi), w.Point)
		}
		for gi, g := range l.PullGroups {
			for pi, pt := range g.Positions {
				leg(li, l, fmt.Sprintf("pull group %d position %d", gi, pi), pt)
			}
			for ai, pt := range g.Approach {
				leg(li, l, fmt.Sprintf("pull group %d approach %d", gi, ai), pt)
			}
			if g.Patrol != nil {
				for pi, pt := range g.Patrol.Path {
					leg(li, l, fmt.Sprintf("pull group %d patrol point %d", gi, pi), pt)
				}
			}
		}
		if l.Boss != nil {
			leg(li, l, "boss position", l.Boss.Position)
			for ai, pt := range l.Boss.Approach {
				leg(li, l, fmt.Sprintf("boss approach %d", ai), pt)
			}
		}
	}
	if len(at) == 0 {
		return "no plan point falls in it (a hop may still cross it)"
	}
	return "holds " + strings.Join(at, ", ")
}

// classifyStep refuses every step kind the Forever rows do not carry.
func classifyStep(c *classifier, field, where string, st dungeon.Step) {
	switch {
	case st.UseObject != nil:
		c.refused(field+".use_object", where, "no object rows in this format")
	case st.WaitFor != nil:
		c.refused(field+".wait_for", where, "no wait rows in this format")
	case st.LootObject != nil:
		c.refused(field+".loot_object", where, "no loot rows in this format")
	case st.UseItem != nil:
		c.refused(field+".use_item", where, "no item rows in this format")
	case st.Ritual != nil:
		c.refused(field+".ritual", where, "no ritual rows in this format")
	case st.Gossip != nil:
		c.refused(field+".gossip", where, "no gossip rows in this format")
	case st.AreaTrigger != nil:
		c.refused(field+".area_trigger", where, "no area-trigger rows in this format")
	}
}

// buildUnitRows writes one unit row per pull-group member, boss, boss.first add and patrol unit, keyed to the step row the
// element belongs to.
func buildUnitRows(p *dungeon.Plan, res Result) ([]UnitRow, error) {
	if len(res.Origins) != len(res.Rows.Steps) {
		return nil, fmt.Errorf("plan %s: %d step origins for %d step rows", p.Name, len(res.Origins), len(res.Rows.Steps))
	}
	var out []UnitRow
	add := func(plan string, s StepRow, idx int, e uint32, at dungeon.Point, role string) {
		out = append(out, UnitRow{Plan: plan, Leg: s.Leg, Seq: s.Seq, Idx: idx, Entry: e, X: at.X, Y: at.Y, Z: at.Z, Role: role})
	}
	for i, o := range res.Origins {
		s := res.Rows.Steps[i]
		if o.Leg < 0 || o.Leg >= len(p.Legs) {
			continue
		}
		l := p.Legs[o.Leg]
		switch o.Part {
		case "pull":
			if o.Group < 0 || o.Group >= len(l.PullGroups) {
				continue
			}
			g := l.PullGroups[o.Group]
			n := len(g.Entries)
			if len(g.Positions) > n {
				n = len(g.Positions)
			}
			for k := 0; k < n; k++ {
				add(s.Plan, s, k, at(g.Entries, k), g.Positions[min(k, len(g.Positions)-1)], UnitPack)
			}
			if g.Patrol != nil && len(g.Patrol.Path) > 0 {
				for j, e := range g.Patrol.Entries {
					add(s.Plan, s, n+j, e, g.Patrol.Path[0], UnitPatrol)
				}
			}
		case "boss":
			b := l.Boss
			if b == nil {
				continue
			}
			add(s.Plan, s, 0, b.Entry, b.Position, UnitBoss)
			for j, e := range b.First {
				// A first add has no position of its own in the plan: its row stands at the boss's point, where the module's
				// search finds the nearest spawn of that entry.
				add(s.Plan, s, 1+j, e, b.Position, UnitFirst)
			}
		}
	}
	return out, nil
}

// at is the entry at i, or the last entry when the plan lists fewer entries than positions.
func at(es []uint32, i int) uint32 {
	if len(es) == 0 {
		return 0
	}
	if i >= len(es) {
		i = len(es) - 1
	}
	return es[i]
}

// buildBossRows writes one ai_dungeon_boss row per boss of the exported legs, in plan order.
func buildBossRows(p *dungeon.Plan, rows Rows) []BossRow {
	var out []BossRow
	seen := map[uint32]bool{}
	for li, l := range p.Legs {
		if l.Boss == nil || seen[l.Boss.Entry] {
			continue
		}
		if _, ok := rows.LegNumber(p, li); !ok {
			continue // the leg is not in the rows
		}
		seen[l.Boss.Entry] = true
		out = append(out, BossRow{Map: p.Map, Entry: l.Boss.Entry})
	}
	return out
}

// foreverQuote writes a SQL string literal for the module's tables. Backslash, quote and the control characters are escaped;
// no character becomes a space, so a comment or a name reads back as it was.
func foreverQuote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`''`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case 0:
			b.WriteString(`\0`)
		case 26:
			b.WriteString(`\Z`)
		case 8:
			b.WriteString(`\b`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// commentSafe keeps a name or comment on the comment line it is written on.
func commentSafe(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}

// foreverNum is a float column with three decimals, never a locale's separator and never exponent form. Three decimals are a
// millimetre at map scale, which is below any distance the module measures, and they make the file readable. A value that
// rounds to zero is written without a sign, so a plan never holds "-0.000".
func foreverNum(v float32) string {
	s := strconv.FormatFloat(float64(v), 'f', 3, 32)
	if s == "-0.000" {
		return "0.000"
	}
	return s
}

func foreverEntries(es []uint32) string { return entriesText(es) }

// WriteForeverSQL renders the migration for the plans, in the order given. The same plans give the same bytes.
func WriteForeverSQL(plans []ForeverReport) string {
	var b strings.Builder
	b.WriteString(ForeverHeader)
	for _, rep := range plans {
		r := rep.Rows
		pulls := 0
		for _, s := range r.Steps {
			if s.Kind == KindPull {
				pulls++
			}
		}
		fmt.Fprintf(&b, "\n-- %s: map %d, %d legs, %d steps, %d pull groups, %d unit rows\n", commentSafe(r.Plan.Plan), r.Plan.Map, len(r.Legs), len(r.Steps), pulls, len(rep.Units))
		b.WriteString("START TRANSACTION;\n")
		fmt.Fprintf(&b, "DELETE FROM `ai_dungeon_step_unit` WHERE `plan` = %s;\n", foreverQuote(r.Plan.Plan))
		fmt.Fprintf(&b, "DELETE FROM `ai_dungeon_step` WHERE `plan` = %s;\n", foreverQuote(r.Plan.Plan))
		fmt.Fprintf(&b, "DELETE FROM `ai_dungeon_leg` WHERE `plan` = %s;\n", foreverQuote(r.Plan.Plan))
		fmt.Fprintf(&b, "DELETE FROM `ai_dungeon_plan` WHERE `plan` = %s;\n", foreverQuote(r.Plan.Plan))
		fmt.Fprintf(&b, "INSERT INTO `ai_dungeon_plan` (`plan`, `map`, `cc_priority`, `cc_always`, `kill_first`, `source`, `comment`) VALUES\n(%s, %d, %s, %s, %s, %s, %s);\n",
			foreverQuote(r.Plan.Plan), r.Plan.Map, foreverQuote(foreverEntries(r.Plan.CCPriority)), foreverQuote(foreverEntries(r.Plan.CCAlways)),
			foreverQuote(foreverEntries(r.Plan.KillFirst)), foreverQuote(r.Plan.Source), foreverQuote(r.Plan.Comment))
		b.WriteString("INSERT INTO `ai_dungeon_leg` (`plan`, `leg`, `name`, `comment`) VALUES\n")
		for i, l := range r.Legs {
			sep := ",\n"
			if i == len(r.Legs)-1 {
				sep = ";\n"
			}
			fmt.Fprintf(&b, "(%s, %d, %s, %s)%s", foreverQuote(l.Plan), l.Leg, foreverQuote(l.Name), foreverQuote(l.Comment), sep)
		}
		b.WriteString("INSERT INTO `ai_dungeon_step` (`plan`, `leg`, `seq`, `kind`, `entry`, `x`, `y`, `z`, `radius`, `item`, `param`, `flags`, `timeout_s`, `entries`, `comment`) VALUES\n")
		for i, s := range r.Steps {
			sep := ",\n"
			if i == len(r.Steps)-1 {
				sep = ";\n"
			}
			fmt.Fprintf(&b, "(%s, %d, %d, %s, %d, %s, %s, %s, %s, %d, %d, %d, %d, %s, %s)%s", foreverQuote(s.Plan), s.Leg, s.Seq, foreverQuote(s.Kind), s.Entry,
				foreverNum(s.X), foreverNum(s.Y), foreverNum(s.Z), foreverNum(s.Radius), s.Item, s.Param, s.Flags, s.TimeoutS,
				foreverQuote(foreverEntries(s.Entries)), foreverQuote(s.Comment), sep)
		}
		if len(rep.Units) > 0 {
			b.WriteString("INSERT INTO `ai_dungeon_step_unit` (`plan`, `leg`, `seq`, `idx`, `entry`, `x`, `y`, `z`, `role`, `comment`) VALUES\n")
			for i, u := range rep.Units {
				sep := ",\n"
				if i == len(rep.Units)-1 {
					sep = ";\n"
				}
				fmt.Fprintf(&b, "(%s, %d, %d, %d, %d, %s, %s, %s, %s, %s)%s", foreverQuote(u.Plan), u.Leg, u.Seq, u.Idx, u.Entry,
					foreverNum(u.X), foreverNum(u.Y), foreverNum(u.Z), foreverQuote(u.Role), foreverQuote(u.Comment), sep)
			}
		}
		if len(rep.Bosses) > 0 {
			// The boss table is keyed by map and entry and shared by every plan on the map, so its rows are upserted and
			// never deleted. The plan carries no display name: an existing row keeps its name unless the plan supplies one.
			b.WriteString("INSERT INTO `ai_dungeon_boss` (`map`, `entry`, `name`) VALUES\n")
			for i, bo := range rep.Bosses {
				sep := ",\n"
				if i == len(rep.Bosses)-1 {
					sep = "\n"
				}
				fmt.Fprintf(&b, "(%d, %d, %s)%s", bo.Map, bo.Entry, foreverQuote(bo.Name), sep)
			}
			b.WriteString("ON DUPLICATE KEY UPDATE `name` = IF(VALUES(`name`) <> '', VALUES(`name`), `name`);\n")
		}
		b.WriteString("COMMIT;\n")
	}
	return b.String()
}

// ParseForever reads a migration WriteForeverSQL wrote back into its rows, units and boss rows, one ForeverReport per plan
// in the order of the file. It is the round-trip check of the exporter.
func ParseForever(sql string) ([]ForeverReport, error) {
	var out []ForeverReport
	byPlan := map[string]int{}
	table := ""
	line := 0
	sc := bufio.NewScanner(strings.NewReader(sql))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	fail := func(format string, a ...any) ([]ForeverReport, error) {
		return nil, fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, a...))
	}
	for sc.Scan() {
		line++
		t := strings.TrimSpace(sc.Text())
		switch {
		case t == "" || strings.HasPrefix(t, "--"):
			continue
		case strings.HasPrefix(t, "DELETE FROM"), strings.HasPrefix(t, "SET"),
			t == "START TRANSACTION;", t == "BEGIN;", t == "COMMIT;":
			table = ""
			continue
		case strings.HasPrefix(t, "INSERT INTO `"), strings.HasPrefix(t, "REPLACE INTO `"):
			name := ""
			if i := strings.Index(t, "`"); i >= 0 {
				if j := strings.Index(t[i+1:], "`"); j >= 0 {
					name = t[i+1 : i+1+j]
				}
			}
			switch name {
			case "ai_dungeon_plan", "ai_dungeon_leg", "ai_dungeon_step", "ai_dungeon_step_unit", "ai_dungeon_boss":
				table = name
			default:
				return fail("unexpected table %q", name)
			}
			if strings.HasSuffix(t, ";") {
				table = ""
			}
			continue
		case strings.HasPrefix(t, "ON DUPLICATE KEY UPDATE"):
			// The boss rows are an upsert: the clause closes the statement and changes no row the parser reads back.
			if table != "ai_dungeon_boss" {
				return fail("an upsert after a %q row", table)
			}
			table = ""
			continue
		case strings.HasPrefix(t, "("):
			if table == "" {
				return fail("a row before any INSERT")
			}
			f, err := splitForeverTuple(t)
			if err != nil {
				return fail("%v", err)
			}
			switch table {
			case "ai_dungeon_plan":
				if len(f) != 7 {
					return fail("%d columns in ai_dungeon_plan", len(f))
				}
				m, err := strconv.ParseUint(f[1], 10, 32)
				if err != nil {
					return fail("map: %v", err)
				}
				var lists [3][]uint32
				for i := range lists {
					if lists[i], err = parseEntries(f[2+i]); err != nil {
						return fail("%v", err)
					}
				}
				if _, ok := byPlan[f[0]]; ok {
					return fail("plan %q twice", f[0])
				}
				byPlan[f[0]] = len(out)
				out = append(out, ForeverReport{Rows: Rows{Plan: PlanRow{Plan: f[0], Map: uint32(m), CCPriority: lists[0], CCAlways: lists[1], KillFirst: lists[2], Source: f[5], Comment: f[6]}}})
			case "ai_dungeon_leg":
				if len(f) != 4 {
					return fail("%d columns in ai_dungeon_leg", len(f))
				}
				r, err := reportFor(out, byPlan, f[0], line)
				if err != nil {
					return fail("%v", err)
				}
				n, err := strconv.Atoi(f[1])
				if err != nil {
					return fail("leg: %v", err)
				}
				r.Rows.Legs = append(r.Rows.Legs, LegRow{Plan: f[0], Leg: n, Name: f[2], Comment: f[3]})
			case "ai_dungeon_step":
				if len(f) != 15 {
					return fail("%d columns in ai_dungeon_step", len(f))
				}
				r, err := reportFor(out, byPlan, f[0], line)
				if err != nil {
					return fail("%v", err)
				}
				s := StepRow{Plan: f[0], Kind: f[3], Comment: f[14]}
				iv := func(i int) (int, error) { return strconv.Atoi(f[i]) }
				uiv := func(i int) (uint32, error) {
					v, err := strconv.ParseUint(f[i], 10, 32)
					return uint32(v), err
				}
				fv := func(i int) (float32, error) {
					v, err := strconv.ParseFloat(f[i], 32)
					return float32(v), err
				}
				var err2 error
				if s.Leg, err2 = iv(1); err2 != nil {
					return fail("column leg: %v", err2)
				}
				if s.Seq, err2 = iv(2); err2 != nil {
					return fail("column seq: %v", err2)
				}
				if s.Entry, err2 = uiv(4); err2 != nil {
					return fail("column entry: %v", err2)
				}
				if s.X, err2 = fv(5); err2 != nil {
					return fail("column x: %v", err2)
				}
				if s.Y, err2 = fv(6); err2 != nil {
					return fail("column y: %v", err2)
				}
				if s.Z, err2 = fv(7); err2 != nil {
					return fail("column z: %v", err2)
				}
				if s.Radius, err2 = fv(8); err2 != nil {
					return fail("column radius: %v", err2)
				}
				if s.Item, err2 = uiv(9); err2 != nil {
					return fail("column item: %v", err2)
				}
				if s.Param, err2 = uiv(10); err2 != nil {
					return fail("column param: %v", err2)
				}
				if s.Flags, err2 = uiv(11); err2 != nil {
					return fail("column flags: %v", err2)
				}
				if s.TimeoutS, err2 = iv(12); err2 != nil {
					return fail("column timeout_s: %v", err2)
				}
				if s.Entries, err2 = parseEntries(f[13]); err2 != nil {
					return fail("column entries: %v", err2)
				}
				r.Rows.Steps = append(r.Rows.Steps, s)
			case "ai_dungeon_step_unit":
				if len(f) != 10 {
					return fail("%d columns in ai_dungeon_step_unit", len(f))
				}
				r, err := reportFor(out, byPlan, f[0], line)
				if err != nil {
					return fail("%v", err)
				}
				var u UnitRow
				var e error
				u.Plan = f[0]
				if u.Leg, e = strconv.Atoi(f[1]); e != nil {
					return fail("unit leg: %v", e)
				}
				if u.Seq, e = strconv.Atoi(f[2]); e != nil {
					return fail("unit seq: %v", e)
				}
				if u.Idx, e = strconv.Atoi(f[3]); e != nil {
					return fail("unit idx: %v", e)
				}
				ev, e := strconv.ParseUint(f[4], 10, 32)
				if e != nil {
					return fail("unit entry: %v", e)
				}
				u.Entry = uint32(ev)
				if u.X, e = parseFloat32(f[5]); e != nil {
					return fail("unit x: %v", e)
				}
				if u.Y, e = parseFloat32(f[6]); e != nil {
					return fail("unit y: %v", e)
				}
				if u.Z, e = parseFloat32(f[7]); e != nil {
					return fail("unit z: %v", e)
				}
				u.Role, u.Comment = f[8], f[9]
				r.Units = append(r.Units, u)
			case "ai_dungeon_boss":
				if len(f) != 3 {
					return fail("%d columns in ai_dungeon_boss", len(f))
				}
				m, err := strconv.ParseUint(f[0], 10, 32)
				if err != nil {
					return fail("boss map: %v", err)
				}
				en, err := strconv.ParseUint(f[1], 10, 32)
				if err != nil {
					return fail("boss entry: %v", err)
				}
				if i := lastReportWithMap(out, uint32(m)); i >= 0 {
					out[i].Bosses = append(out[i].Bosses, BossRow{Map: uint32(m), Entry: uint32(en), Name: f[2]})
				} else {
					return fail("boss row for map %d before its plan", m)
				}
			}
			if strings.HasSuffix(t, ";") {
				table = ""
			}
			continue
		default:
			return fail("unexpected %q", clip(t, 40))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func reportFor(out []ForeverReport, byPlan map[string]int, plan string, line int) (*ForeverReport, error) {
	i, ok := byPlan[plan]
	if !ok {
		return nil, fmt.Errorf("line %d: rows of plan %q before its ai_dungeon_plan row", line, plan)
	}
	return &out[i], nil
}

func lastReportWithMap(out []ForeverReport, m uint32) int {
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Rows.Plan.Map == m {
			return i
		}
	}
	return -1
}

func parseFloat32(s string) (float32, error) {
	v, err := strconv.ParseFloat(s, 32)
	return float32(v), err
}

// splitForeverTuple splits "(a, 'b', 3)," into its values, decoding the string escapes foreverQuote writes.
func splitForeverTuple(t string) ([]string, error) {
	t = strings.TrimSpace(t)
	t = strings.TrimSuffix(t, ";")
	t = strings.TrimSuffix(strings.TrimSpace(t), ",")
	if !strings.HasPrefix(t, "(") || !strings.HasSuffix(t, ")") {
		return nil, fmt.Errorf("not a tuple: %q", clip(t, 40))
	}
	t = t[1 : len(t)-1]
	var out []string
	for i := 0; i < len(t); {
		for i < len(t) && (t[i] == ' ' || t[i] == '\t') {
			i++
		}
		var v strings.Builder
		if i < len(t) && t[i] == '\'' {
			i++
			for {
				if i >= len(t) {
					return nil, fmt.Errorf("unterminated string")
				}
				c := t[i]
				switch {
				case c == '\\' && i+1 < len(t):
					i++
					switch t[i] {
					case 'n':
						v.WriteByte('\n')
					case 'r':
						v.WriteByte('\r')
					case 't':
						v.WriteByte('\t')
					case '0':
						v.WriteByte(0)
					case 'Z':
						v.WriteByte(26)
					case 'b':
						v.WriteByte(8)
					default:
						v.WriteByte(t[i])
					}
					i++
				case c == '\'' && i+1 < len(t) && t[i+1] == '\'':
					v.WriteByte('\'')
					i += 2
				case c == '\'':
					i++
					goto done
				default:
					v.WriteByte(c)
					i++
				}
			}
		} else {
			for i < len(t) && t[i] != ',' {
				v.WriteByte(t[i])
				i++
			}
		}
	done:
		out = append(out, strings.TrimSpace(v.String()))
		for i < len(t) && (t[i] == ' ' || t[i] == '\t') {
			i++
		}
		if i < len(t) {
			if t[i] != ',' {
				return nil, fmt.Errorf("expected a comma at %d", i)
			}
			i++
		}
	}
	return out, nil
}

// EqualForever compares the reports that were built with the reports read back, and names the first difference.
func EqualForever(want, got []ForeverReport) error {
	if len(want) != len(got) {
		return fmt.Errorf("%d plans, read back %d", len(want), len(got))
	}
	for i := range want {
		w, g := want[i], got[i]
		if err := equalForeverRows(w.Rows, g.Rows); err != nil {
			return err
		}
		if len(w.Units) != len(g.Units) {
			return fmt.Errorf("plan %s: %d unit rows, read back %d", w.Rows.Plan.Plan, len(w.Units), len(g.Units))
		}
		for j := range w.Units {
			if w.Units[j] != g.Units[j] {
				return fmt.Errorf("plan %s unit row %d: %+v, read back %+v", w.Rows.Plan.Plan, j, w.Units[j], g.Units[j])
			}
		}
		if len(w.Bosses) != len(g.Bosses) {
			return fmt.Errorf("plan %s: %d boss rows, read back %d", w.Rows.Plan.Plan, len(w.Bosses), len(g.Bosses))
		}
		for j := range w.Bosses {
			if w.Bosses[j] != g.Bosses[j] {
				return fmt.Errorf("plan %s boss row %d: %+v, read back %+v", w.Rows.Plan.Plan, j, w.Bosses[j], g.Bosses[j])
			}
		}
	}
	return nil
}

// equalForeverRows compares a built plan with its read-back rows. The float columns are written with three decimals, so a
// coordinate may differ from the plan's own float by less than half a thousandth of a yard.
func equalForeverRows(want, got Rows) error {
	wp, gp := want.Plan, got.Plan
	if wp.Plan != gp.Plan || wp.Map != gp.Map || wp.Source != gp.Source || wp.Comment != gp.Comment ||
		entriesText(wp.CCPriority) != entriesText(gp.CCPriority) || entriesText(wp.CCAlways) != entriesText(gp.CCAlways) ||
		entriesText(wp.KillFirst) != entriesText(gp.KillFirst) {
		return fmt.Errorf("plan row %+v, read back %+v", wp, gp)
	}
	if len(want.Legs) != len(got.Legs) {
		return fmt.Errorf("plan %s: %d legs, read back %d", wp.Plan, len(want.Legs), len(got.Legs))
	}
	for i := range want.Legs {
		if want.Legs[i] != got.Legs[i] {
			return fmt.Errorf("plan %s leg %d: %+v, read back %+v", wp.Plan, i, want.Legs[i], got.Legs[i])
		}
	}
	if len(want.Steps) != len(got.Steps) {
		return fmt.Errorf("plan %s: %d steps, read back %d", wp.Plan, len(want.Steps), len(got.Steps))
	}
	for i := range want.Steps {
		a, b := want.Steps[i], got.Steps[i]
		if a.Plan != b.Plan || a.Leg != b.Leg || a.Seq != b.Seq || a.Kind != b.Kind || a.Entry != b.Entry ||
			a.Item != b.Item || a.Param != b.Param || a.Flags != b.Flags || a.TimeoutS != b.TimeoutS ||
			entriesText(a.Entries) != entriesText(b.Entries) || a.Comment != b.Comment ||
			!near3(a.X, b.X) || !near3(a.Y, b.Y) || !near3(a.Z, b.Z) || !near3(a.Radius, b.Radius) {
			return fmt.Errorf("plan %s step %d: %+v, read back %+v", wp.Plan, i, a, b)
		}
	}
	return nil
}

// near3 says two values agree to the three decimals the Forever rows carry.
func near3(a, b float32) bool {
	d := float64(a) - float64(b)
	return d <= 0.0005 && d >= -0.0005
}
