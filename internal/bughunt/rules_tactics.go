package bughunt

// Trash tactics checked in the combat log: module issue TortoiseBotsForever#23. Rules 16-19 read a per-dungeon
// tactics file (tactics.go; documented in docs/forever/TRASH-TACTICS-NOTES.md) against a watched run and check
// the four things its generator will derive from the core's creature data: must-interrupt spells that completed
// (16), crowd control tried on an immune mob (17), listed dispellable debuffs left on (18) and kill order (19).
// Every hit cites the combat rows. A recording whose log lacks the combat kinds a rule reads is UNKNOWN (a
// skipped window or a skipped check), never a finding: rules 16 and 18 need spell_go, interrupt and dispel rows;
// rule 17 needs spell_go, miss, resist or immune rows; rule 19 needs death rows or combat frames to date a kill.
// A run with no tactics file skips all four rules.

import (
	"fmt"
	"math"
	"sort"
)

// Rules 16-19: the trash-tactics checks over a tactics file. A nil tactics file is "no tactics file".
func TacticsRules(t *Tactics) []Rule {
	return []Rule{
		{16, ruleNames[16], func(r *Run) []Hit { return Rule16(r, t) }, skipTacticsLog(t)},
		{17, ruleNames[17], func(r *Run) []Hit { return Rule17(r, t) }, skipTacticsLog(t)},
		{18, ruleNames[18], func(r *Run) []Hit { return Rule18(r, t) }, skipTacticsLog(t)},
		{19, ruleNames[19], func(r *Run) []Hit { return Rule19(r, t) }, skipTacticsNoLog(t)},
	}
}

// skipTacticsLog: no tactics file, or no combat log with timed rows. The time check is the core-gains "no timed
// rows" one, not the blind len(Combat) one: an unknown time at any rule is never t=0.
func skipTacticsLog(t *Tactics) func(*Run) string {
	return func(r *Run) string {
		if t == nil || len(t.Creatures) == 0 {
			return "no tactics file"
		}
		return skipNoCombatLog(r)
	}
}

// skipTacticsNoLog: rule 19 dates kills from death rows or combat frames. An empty log is no input; a log of
// untimed rows still answers the frame check, which carries its own UNKNOWN paths below.
func skipTacticsNoLog(t *Tactics) func(*Run) string {
	return func(r *Run) string {
		if t == nil || len(t.Creatures) == 0 {
			return "no tactics file"
		}
		if len(r.Combat) == 0 {
			return "no combat log"
		}
		return ""
	}
}

// The combat kinds the tactics rules read.
var (
	interruptKinds  = map[string]bool{"interrupt": true}
	dispelKinds     = map[string]bool{"dispel": true}
	spellGoKinds    = map[string]bool{"spell_go": true}
	auraOnKinds     = map[string]bool{"spell_go": true, "aura_applied": true}
	ccAttemptKinds  = map[string]bool{"spell_go": true, "miss": true, "resist": true, "immune": true}
	ccAttemptReason = "log has no spell_go, miss, resist or immune rows"
)

// The UNKNOWN reasons the tactics rules count beyond the cover package's two.
const (
	whyNoPosition = "a mob's position is unknown"
	whyNoFrame    = "no frame near the cast"
)

// interrupterClasses are the classes whose table row carries an interrupt (warrior Pummel/Shield Bash, paladin
// Hammer of Justice, rogue Kick, priest Silence, shaman Earth Shock, mage Counterspell). A hunter, warlock or
// druid, or an unknown class, is never counted as an interrupter: unknown stays unknown.
var interrupterClasses = map[string]bool{
	"warrior": true, "paladin": true, "rogue": true,
	"priest": true, "shaman": true, "mage": true,
}

// tacticsEntries maps every canonical guid the frames ever showed a creature entry for to that entry. A guid the
// frames never named (and whose own bits name none) is not a creature cast.
func (r *Run) tacticsEntries() map[string]uint32 {
	out := map[string]uint32{}
	for _, f := range r.Frames {
		for _, m := range f.Mobs {
			g := CanonGUID(m.G)
			if g == "" {
				continue
			}
			if m.Entry != 0 {
				out[g] = m.Entry
			} else if _, ok := out[g]; !ok {
				if e := entryOf(g); e != 0 {
					out[g] = e
				}
			}
		}
	}
	return out
}

// hasTimedKinds says whether the run's log carries any timed row of the kinds: the kinds the rule reads exist in
// this recording, so their absence anywhere else is evidence, not an older recording.
func (r *Run) hasTimedKinds(kinds map[string]bool) bool {
	for i := range r.Combat {
		row := &r.Combat[i]
		if row.TKnown && kinds[row.Kind] {
			return true
		}
	}
	return false
}

// interruptAnswers indexes the timed interrupt rows by victim and stopped spell: an interrupt row is the exact
// answer to the cast it stopped, as rule 15's dispel index answers a debuff episode.
func (r *Run) interruptAnswers() map[string]map[int][]float64 {
	var out map[string]map[int][]float64
	for i := range r.Combat {
		row := &r.Combat[i]
		if row.Kind != "interrupt" || !row.TKnown || row.InterruptedSpellID <= 0 {
			continue
		}
		victim := CanonGUID(row.Target.GUID)
		if victim == "" {
			continue
		}
		if out == nil {
			out = map[string]map[int][]float64{}
		}
		bySpell := out[victim]
		if bySpell == nil {
			bySpell = map[int][]float64{}
			out[victim] = bySpell
		}
		bySpell[row.InterruptedSpellID] = append(bySpell[row.InterruptedSpellID], row.T)
	}
	return out
}

// answered says whether an interrupt row for the victim and spell falls in [t-1, t+3]: the kick that stopped
// this cast, not an earlier cast's.
func answered(answers map[string]map[int][]float64, victim string, spell int, t float64) bool {
	for _, at := range answers[victim][spell] {
		if at >= t-1 && at <= t+3 {
			return true
		}
	}
	return false
}

// interrupters is rule 15's judge recast for a cast's completion: the most able interrupters seen in one frame
// within 2 s of the completion, and the nearest of them. Able is alive, an interrupter class, within
// R15_RANGE_YD of the caster and not casting. A class the run does not know is never able.
func (r *Run) interrupters(caster *Unit, at float64) (who string, able int, near float64, ok bool) {
	best, nearestName, nearest, found := 0, "", math.Inf(1), false
	for _, f := range r.framesBetween(at-2, at) {
		able, near, name := 0, math.Inf(1), ""
		for _, n := range f.Order {
			if !r.IsSubject(n) {
				continue
			}
			u := f.Units[n]
			if !u.Alive || u.Casting || u.Spell != 0 {
				continue
			}
			if !interrupterClasses[r.Class(n)] {
				continue
			}
			if d := dist3(u, caster); d <= R15_RANGE_YD {
				able++
				if d < near {
					near, name = d, n
				}
			}
		}
		if able > 0 && (!found || able > best) {
			best, nearestName, nearest, found = able, name, near, true
		}
	}
	if !found {
		return "", 0, 0, false
	}
	return nearestName, best, nearest, true
}

// mobPosAt reads a hostile's position from the nearest frame at or before at. A hostile the frames never show,
// or one flagged NP, has no position: the check is UNKNOWN from there.
func (r *Run) mobPosAt(guid string, at float64) (Unit, bool) {
	g := CanonGUID(guid)
	for i := len(r.Frames) - 1; i >= 0; i-- {
		f := r.Frames[i]
		if f.T > at {
			continue
		}
		for _, m := range f.Mobs {
			if CanonGUID(m.G) == g {
				if m.NP {
					return Unit{}, false
				}
				return Unit{X: m.X, Y: m.Y, Z: m.Z, Alive: true}, true
			}
		}
	}
	return Unit{}, false
}

// mobName is the tactics name for an entry, or `entry N` when the file does not name it.
func mobName(t *Tactics, entry uint32) string {
	if ce, ok := t.Creature(entry); ok && ce.Name != "" {
		return ce.Name
	}
	return fmt.Sprintf("entry %d", entry)
}

// Rule16: a listed must-interrupt spell that completed while an interrupter stood free. A spell_go row from a
// listed entry for a listed spell is the completion; an interrupt row for the same mob and spell in [t-1, t+3]
// answers it. Only a cast with no answer, a covered window, a known caster position and a free interrupter is a
// finding. The hit cites the spell_go row; the key is (16, entry, spell name).
//
// UNKNOWN: no spell_go kind in the recording (no completed casts to judge), no interrupt kind (an older
// recording: an unanswered cast may have been kicked), a cast whose source has no creature entry, a window the
// log does not cover, or no frame near the cast to place the interrupters.
func Rule16(r *Run, t *Tactics) []Hit {
	if !r.hasTimedKinds(spellGoKinds) {
		r.noteUnknown(16, "log has no spell_go rows")
		return nil
	}
	if !r.hasTimedKinds(interruptKinds) {
		r.noteUnknown(16, "log has no interrupt rows")
		return nil
	}
	entries := r.tacticsEntries()
	answers := r.interruptAnswers()
	var hits []Hit
	for i := range r.Combat {
		row := &r.Combat[i]
		if row.Kind != "spell_go" || !row.TKnown {
			continue
		}
		src := CanonGUID(row.Source.GUID)
		entry := entries[src]
		if entry == 0 {
			if _, isMember := r.ByGUID[src]; !isMember && src != "" {
				r.noteUnknown(16, "a cast's source has no creature entry")
			}
			continue
		}
		ce, ok := t.Creature(entry)
		if !ok || !ce.interrupts(row.SpellID) {
			continue
		}
		if answered(answers, src, row.SpellID, row.T) {
			continue
		}
		if !r.covers(row.T-1, row.T+3) {
			r.noteUnknown(16, whyLogCover)
			continue
		}
		caster, ok := r.mobPosAt(row.Source.GUID, row.T)
		if !ok {
			r.noteUnknown(16, whyNoPosition)
			continue
		}
		who, able, near, ok := r.interrupters(&caster, row.T)
		if !ok {
			r.noteUnknown(16, whyNoFrame)
			continue
		}
		if able == 0 {
			continue
		}
		spell := r.SpellName(row.SpellID)
		ev := fmt.Sprintf("%s completed %s at t=%.1f s with no interrupt row in [%.1f, %.1f]; %d interrupter(s) free, nearest %.0f yd (%s; combat row spell_go@%.1f)",
			mobName(t, entry), spell, row.T, row.T-1, row.T+3, able, near, who, row.T)
		hits = append(hits, r.Hit(16, row.T, 0, who, ev, []string{"16", fmt.Sprint(entry), spell}))
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].T != hits[j].T {
			return hits[i].T < hits[j].T
		}
		return hits[i].Bot < hits[j].Bot
	})
	return hits
}

// Rule17: crowd control tried on a mob the tactics list as immune to it. The CC kind comes from the tactics
// cc_spells; an unlisted CC spell is unknown, never a finding. The try is a spell_go, miss, resist or immune row
// for the spell whose target is the immune mob, cast by a party member. The hit cites the row; the key is
// (17, entry, CC kind).
//
// UNKNOWN: no spell_go, miss, resist or immune kind in the recording. A row whose target has no creature entry,
// or whose caster is not a member, is outside the check, never a finding.
func Rule17(r *Run, t *Tactics) []Hit {
	if !r.hasTimedKinds(ccAttemptKinds) {
		r.noteUnknown(17, ccAttemptReason)
		return nil
	}
	entries := r.tacticsEntries()
	var hits []Hit
	for i := range r.Combat {
		row := &r.Combat[i]
		if !ccAttemptKinds[row.Kind] || !row.TKnown {
			continue
		}
		kinds, ok := t.CCSpells[row.SpellID]
		if !ok {
			continue
		}
		src := CanonGUID(row.Source.GUID)
		member, isMember := r.ByGUID[src]
		if !isMember {
			continue
		}
		entry := entries[CanonGUID(row.Target.GUID)]
		if entry == 0 {
			continue
		}
		ce, ok := t.Creature(entry)
		if !ok {
			continue
		}
		kind := ""
		for _, k := range kinds {
			if ce.immuneTo(k) {
				kind = k
				break
			}
		}
		if kind == "" {
			continue
		}
		ev := fmt.Sprintf("%s cast %s (%s) on %s (immune to %s; combat row %s@%.1f)",
			member, r.SpellName(row.SpellID), kind, mobName(t, entry), kind, row.Kind, row.T)
		hits = append(hits, r.Hit(17, row.T, 0, member, ev, []string{"17", fmt.Sprint(entry), kind}))
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].T != hits[j].T {
			return hits[i].T < hits[j].T
		}
		return hits[i].Bot < hits[j].Bot
	})
	return hits
}

// Rule18: a listed dispellable debuff left on for more than its within_s while an able dispeller stood free. The
// episode starts at the first spell_go from a non-member, or the first aura_applied row, for the victim and spell
// and ends at the first dispel row for the same pair (rule 15's index, per episode); past the log's last timed
// row it is still up. The listing's within_s replaces rule 15's minimum; the grace, range and judge are rule
// 15's own. The hit cites the applying row; the key is (18, spell name, type).
//
// UNKNOWN: no spell_go or aura_applied kind in the recording, no dispel kind (an older recording), or a window
// the log does not cover. A victim the frames never show as a member is outside the check.
func Rule18(r *Run, t *Tactics) []Hit {
	if !r.hasTimedKinds(auraOnKinds) {
		r.noteUnknown(18, "log has no spell_go or aura_applied rows")
		return nil
	}
	if !r.hasTimedKinds(dispelKinds) {
		r.noteUnknown(18, "log has no dispel rows")
		return nil
	}
	type key struct {
		victim string
		spell  int
	}
	starts := map[key][]float64{}
	var order []key
	for i := range r.Combat {
		row := &r.Combat[i]
		if !row.TKnown || !auraOnKinds[row.Kind] {
			continue
		}
		if _, _, _, ok := t.dispelFor(row.SpellID); !ok {
			continue
		}
		if row.Kind == "spell_go" {
			if _, isMember := r.ByGUID[CanonGUID(row.Source.GUID)]; isMember {
				continue
			}
		}
		victim := CanonGUID(row.Target.GUID)
		if victim == "" {
			continue
		}
		k := key{victim: victim, spell: row.SpellID}
		if _, ok := starts[k]; !ok {
			order = append(order, k)
		}
		starts[k] = append(starts[k], row.T)
	}
	dispels := r.dispelRemovals()
	logEnd := math.Inf(-1)
	for i := range r.Combat {
		if row := &r.Combat[i]; row.TKnown && row.T > logEnd {
			logEnd = row.T
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].victim != order[j].victim {
			return order[i].victim < order[j].victim
		}
		return order[i].spell < order[j].spell
	})
	var hits []Hit
	for _, k := range order {
		within, typ, _, ok := t.dispelFor(k.spell)
		if !ok {
			continue
		}
		ts := starts[k]
		sort.Float64s(ts)
		member := r.Member(k.victim)
		if _, ok := r.GUIDOf[member]; !ok && r.ByGUID[k.victim] == "" {
			// The victim is no member the frames show: nothing to judge a dispeller from.
			if member == k.victim {
				continue
			}
		}
		prevEnd := math.Inf(-1)
		for _, t0 := range ts {
			if t0 < prevEnd {
				continue
			}
			t1, removed := firstRemoval(dispels[k.victim][uint32(k.spell)], t0, math.Inf(1))
			still := ""
			if !removed {
				t1 = logEnd
				still = " (still up at the end)"
			}
			prevEnd = t1
			if t1-t0 <= within {
				continue
			}
			if !r.covers(t0, t1) {
				r.noteUnknown(18, whyLogCover)
				continue
			}
			able, near, ok := r.dispellers(member, t0, t1, typ)
			if !ok || able == 0 {
				continue
			}
			name := r.SpellName(k.spell)
			ev := fmt.Sprintf("%s (%s) on %s for %.1f s (limit %.0f s); %d able dispeller(s) alive, nearest %.0f yd and not casting%s (applied combat row at t=%.1f)",
				name, typ, member, t1-t0, within, able, near, still, t0)
			hits = append(hits, r.Hit(18, t0, t1-t0, member, ev, []string{"18", name, typ}))
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].T != hits[j].T {
			return hits[i].T < hits[j].T
		}
		return hits[i].Bot < hits[j].Bot
	})
	return hits
}

// Rule19: a lower-priority mob killed first while a higher one was in reach. Both mobs shared one frame (the
// pull); the kill is a death row for the entry, else the last frame that shows it alive while group combat runs.
// Priority 0 is unordered, an unlisted entry is outside the order, and two entries with the same priority are one
// wave. In reach is the last shared frame with both mobs at most R19_REACH_YD apart. The hit cites the death row
// (or the two frames); the key is (19, dungeon, low entry, high entry).
//
// UNKNOWN: a shared pull whose kill has no dated death row or frame, a mob whose position is unknown, or a higher
// mob whose fate past the kill cannot be told.
func Rule19(r *Run, t *Tactics) []Hit {
	type ordered struct {
		entry uint32
		prio  int
	}
	var ord []ordered
	for e, ce := range t.Creatures {
		if ce.Priority > 0 {
			ord = append(ord, ordered{entry: e, prio: ce.Priority})
		}
	}
	if len(ord) < 2 {
		return nil
	}
	entries := r.tacticsEntries()
	kills, killRow := r.killTimes(entries)
	var hits []Hit
	for _, low := range ord {
		tk, dated := kills[low.entry]
		if !dated {
			if r.sharedPull(low.entry) {
				r.noteUnknown(19, "a kill has no dated death row or frame")
			}
			continue
		}
		for _, high := range ord {
			if high.entry == low.entry || high.prio >= low.prio {
				continue
			}
			co, ok := r.lastSharedFrame(low.entry, high.entry, tk)
			if !ok {
				continue
			}
			if co.lowNP || co.highNP {
				r.noteUnknown(19, whyNoPosition)
				continue
			}
			if math.Hypot(co.lowX-co.highX, co.lowY-co.highY) > R19_REACH_YD {
				continue
			}
			if alive, known := r.aliveAt(high.entry, tk, kills); !known {
				r.noteUnknown(19, "a higher mob's fate past the kill is unknown")
				continue
			} else if !alive {
				continue
			}
			bot := r.nearestMember(co.frame, co.lowX, co.lowY)
			cite := fmt.Sprintf("combat row death@%.1f", tk)
			if ri, ok := killRow[low.entry]; ok {
				cite = fmt.Sprintf("combat row %s@%.1f", r.Combat[ri].Kind, r.Combat[ri].T)
			}
			ev := fmt.Sprintf("%s killed at t=%.1f s while %s (priority %d before %d) within %.0f yd (last together t=%.1f s; %s)",
				mobName(t, low.entry), tk, mobName(t, high.entry), high.prio, low.prio, R19_REACH_YD, co.t, cite)
			dungeon := t.Dungeon
			if dungeon == "" {
				dungeon = "tactics"
			}
			hits = append(hits, r.Hit(19, tk, 0, bot, ev,
				[]string{"19", dungeon, fmt.Sprint(low.entry), fmt.Sprint(high.entry)}))
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].T != hits[j].T {
			return hits[i].T < hits[j].T
		}
		return hits[i].Bot < hits[j].Bot
	})
	return hits
}

// R19_REACH_YD is how near two mobs in one frame are "in reach" of the same pull.
const R19_REACH_YD = 40.0

// killTimes dates each listed entry's kill: the first timed death row for a guid with the entry, else the frame
// after the last frame that shows it alive while group combat runs. An entry seen alive to the end, or one that
// vanishes across a frame gap, has no dated kill. killRow is the death row behind the time, when a row dated it.
func (r *Run) killTimes(entries map[string]uint32) (kills map[uint32]float64, killRow map[uint32]int) {
	kills, killRow = map[uint32]float64{}, map[uint32]int{}
	for i := range r.Combat {
		row := &r.Combat[i]
		if row.Kind != "death" || !row.TKnown {
			continue
		}
		e := entries[CanonGUID(row.Target.GUID)]
		if e == 0 {
			continue
		}
		if _, done := kills[e]; !done {
			kills[e], killRow[e] = row.T, i
		}
	}
	present := func(f *Frame, e uint32) bool {
		for _, m := range f.Mobs {
			if m.Entry == e || entries[CanonGUID(m.G)] == e {
				return true
			}
		}
		return false
	}
	listed := map[uint32]bool{}
	for _, f := range r.Frames {
		for _, m := range f.Mobs {
			if m.Entry != 0 {
				listed[m.Entry] = true
			}
			if e := entries[CanonGUID(m.G)]; e != 0 {
				listed[e] = true
			}
		}
	}
	for e := range listed {
		if _, done := kills[e]; done {
			continue
		}
		last := -1
		for fi, f := range r.Frames {
			if present(f, e) {
				last = fi
			}
		}
		if last < 0 || last == len(r.Frames)-1 {
			continue
		}
		next := r.Frames[last+1]
		if next.T-r.Frames[last].T > MAX_GAP_S {
			continue
		}
		kills[e] = next.T
	}
	return kills, killRow
}

// sharedPull says whether the entry ever shared a frame with another listed entry: there is a pull to order, so
// an undated kill is UNKNOWN rather than nothing.
func (r *Run) sharedPull(entry uint32) bool {
	for _, f := range r.Frames {
		seen := map[uint32]bool{}
		for _, m := range f.Mobs {
			if m.Entry != 0 {
				seen[m.Entry] = true
			}
		}
		if seen[entry] && len(seen) > 1 {
			return true
		}
	}
	return false
}

// sharedFrame is the last frame at or before tk showing both entries, with their positions.
type sharedFrame struct {
	frame         *Frame
	t             float64
	lowX, lowY    float64
	highX, highY  float64
	lowNP, highNP bool
}

// lastSharedFrame finds the last frame at or before tk showing both entries.
func (r *Run) lastSharedFrame(low, high uint32, tk float64) (sharedFrame, bool) {
	var out sharedFrame
	found := false
	for _, f := range r.Frames {
		if f.T > tk {
			break
		}
		var lw, hi *Mob
		for _, m := range f.Mobs {
			if m.Entry == low {
				lw = m
			}
			if m.Entry == high {
				hi = m
			}
		}
		if lw == nil || hi == nil {
			continue
		}
		out = sharedFrame{frame: f, t: f.T, lowX: lw.X, lowY: lw.Y, highX: hi.X, highY: hi.Y,
			lowNP: lw.NP, highNP: hi.NP}
		found = true
	}
	return out, found
}

// aliveAt says whether the entry was still alive past tk: seen in a later frame, or killed later. A mob never
// seen after tk whose kill is undated may have died before it: unknown.
func (r *Run) aliveAt(entry uint32, tk float64, kills map[uint32]float64) (alive, known bool) {
	if kill, dated := kills[entry]; dated {
		return kill > tk, true
	}
	for _, f := range r.Frames {
		if f.T <= tk {
			continue
		}
		for _, m := range f.Mobs {
			if m.Entry == entry {
				return true, true
			}
		}
	}
	return false, false
}

// nearestMember is the nearest alive member to (x, y) in the frame, else the first subject, else empty.
func (r *Run) nearestMember(f *Frame, x, y float64) string {
	best, near := "", math.Inf(1)
	for _, n := range f.Order {
		u := f.Units[n]
		if !u.Alive {
			continue
		}
		if d := math.Hypot(u.X-x, u.Y-y); d < near {
			near, best = d, n
		}
	}
	if best != "" {
		return best
	}
	for n := range r.Subjects {
		return n
	}
	return ""
}
