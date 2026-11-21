package bughunt

// Trash tactics checked in the combat log: module issue TortoiseBotsForever#23. Rules 16-19 read a per-dungeon
// tactics file (tactics.go; documented in docs/forever/TRASH-TACTICS-NOTES.md) against a watched run and check
// the four things its generator will derive from the core's creature data: must-interrupt spells that completed
// (16), crowd control tried on an immune mob (17), listed dispellable debuffs left on (18) and kill order (19).
// Every hit cites the combat rows. A recording whose log lacks the combat kinds a rule reads is UNKNOWN (a
// skipped window or a skipped check), never a finding: rule 16 needs spell_go and interrupt rows; rule 17 needs
// spell_go, miss, resist or immune rows; rule 18 needs spell_go or aura_applied rows, and aura_removed rows to
// tell an expired debuff from one still up; rule 19 needs death rows. A run with no tactics file skips all four
// rules.

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

// skipTacticsNoLog: rule 19 needs a timed death row to date a kill; the rule itself reports UNKNOWN without a
// death kind. An empty log is no input; a log of untimed rows still answers the frame checks.
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
	spellGoKinds    = map[string]bool{"spell_go": true}
	auraOnKinds     = map[string]bool{"spell_go": true, "aura_applied": true}
	auraOffKinds    = map[string]bool{"aura_removed": true}
	deathKinds      = map[string]bool{"death": true}
	ccAttemptKinds  = map[string]bool{"spell_go": true, "miss": true, "resist": true, "immune": true}
	ccAttemptReason = "log has no spell_go, miss, resist or immune rows"
)

// The UNKNOWN reasons the tactics rules count beyond the cover package's two.
const (
	whyNoPosition = "a mob's position is unknown"
	whyNoFrame    = "no frame near the cast"
	whyNoCombat   = "the caster's mob was not in combat"
)

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

// castStarts indexes timed spell_start rows by source and spell: the casts a member or mob began.
func (r *Run) castStarts() map[string]map[int][]float64 {
	var out map[string]map[int][]float64
	for i := range r.Combat {
		row := &r.Combat[i]
		if row.Kind != "spell_start" || !row.TKnown {
			continue
		}
		src := CanonGUID(row.Source.GUID)
		if src == "" {
			continue
		}
		if out == nil {
			out = map[string]map[int][]float64{}
		}
		bySpell := out[src]
		if bySpell == nil {
			bySpell = map[int][]float64{}
			out[src] = bySpell
		}
		bySpell[row.SpellID] = append(bySpell[row.SpellID], row.T)
	}
	return out
}

// answered says whether an interrupt row for the victim and spell falls in [t-1, t+3] and answered this cast. A
// cast of the same spell started after this completion and before the interrupt was the one it stopped: the next
// cast's kick never masks a completed one.
func answered(answers map[string]map[int][]float64, starts map[string]map[int][]float64, victim string, spell int, t float64) bool {
	for _, at := range answers[victim][spell] {
		if at < t-1 || at > t+3 {
			continue
		}
		later := false
		for _, start := range starts[victim][spell] {
			if start > t && start <= at {
				later = true
				break
			}
		}
		if !later {
			return true
		}
	}
	return false
}

// incapReasons are the cast-failure reasons that say a member could not act: a stun, a silence, a pacify, a fear
// (fleeing) or another mechanic. A member with one of these rows in the window is not free; the recording has no
// other channel for a party member's own CC, so a member with no action failure there is read as not controlled.
var incapReasons = map[string]bool{
	"stunned": true, "silenced": true, "pacified": true, "fleeing": true, "prevented by mechanic": true,
}

// incapWait is how far before the cast a member's own action failure makes it not free.
const incapWait = 2.0

// interrupters is rule 16's judge: the last frame at or before the completion (within R16_CAST_PAD_S), its members
// that could interrupt right now, and the nearest of them. Able is alive, a class with an interrupt, that interrupt
// off cooldown from the member's own cast rows, not casting, no stun/silence/incapacitation row in the window, and
// within the interrupt spell's range of the caster. When no frame is near, or a member's class is unknown (and no
// one is able), the recording cannot answer: ok is false and why names it.
func (r *Run) interrupters(caster *Unit, at float64) (who string, able int, near float64, ok bool, why string) {
	frames := r.framesBetween(at-R16_CAST_PAD_S, at)
	if len(frames) == 0 {
		return "", 0, 0, false, whyNoFrame
	}
	f := frames[len(frames)-1] // the frame nearest the completion: the state that decides the interrupt
	unknownClass := false
	near = math.Inf(1)
	for _, n := range f.Order {
		if !r.IsSubject(n) {
			continue
		}
		rows, known := interruptRows[r.Class(n)]
		if !known {
			if c := r.Class(n); c == "" || c == "?" {
				unknownClass = true
			}
			continue
		}
		u := f.Units[n]
		if !u.Alive || u.Casting || u.Spell != 0 {
			continue
		}
		if r.incapacitated(n, at) {
			continue
		}
		d := dist3(u, caster)
		for _, row := range rows {
			if d > row.rangeYD || !r.interruptReady(n, row.spell, row.cooldownS, at) {
				continue
			}
			able++
			if d < near {
				near, who = d, n
			}
			break
		}
	}
	if able == 0 && unknownClass {
		return "", 0, 0, false, "a member's class is unknown"
	}
	return who, able, near, true, ""
}

// interruptReady says the member's interrupt was off cooldown at at: its own cast rows for the spell within the
// cooldown window would say it was not.
func (r *Run) interruptReady(n string, spell int, cooldownS, at float64) bool {
	for _, i := range r.rowsFromMember(n) {
		row := &r.Combat[i]
		if !row.TKnown || row.SpellID != spell || row.T > at || row.T < at-cooldownS {
			continue
		}
		if row.Kind == "spell_start" || row.Kind == "spell_go" {
			return false
		}
	}
	return true
}

// incapacitated says the member's own rows name a stun, silence or incapacitation within incapWait before at.
func (r *Run) incapacitated(n string, at float64) bool {
	for _, i := range r.rowsFromMember(n) {
		row := &r.Combat[i]
		if !row.TKnown || row.Kind != "spell_fail" || row.T > at || row.T < at-incapWait {
			continue
		}
		if incapReasons[row.Reason] {
			return true
		}
	}
	return false
}

// mobAt reads a hostile's row from the nearest frame at or before at. A hostile the frames never show has no
// state: the check is UNKNOWN from there.
func (r *Run) mobAt(guid string, at float64) (*Mob, bool) {
	g := CanonGUID(guid)
	for i := len(r.Frames) - 1; i >= 0; i-- {
		f := r.Frames[i]
		if f.T > at {
			continue
		}
		for _, m := range f.Mobs {
			if CanonGUID(m.G) == g {
				return m, true
			}
		}
	}
	return nil, false
}

// mobName is the tactics name for an entry, or `entry N` when the file does not name it.
func mobName(t *Tactics, entry uint32) string {
	if ce, ok := t.Creature(entry); ok && ce.Name != "" {
		return ce.Name
	}
	return fmt.Sprintf("entry %d", entry)
}

// Rule16: a listed must-interrupt spell that completed while a free interrupter stood there. A spell_go row from a
// listed entry for a listed spell is the completion; an interrupt row for the same mob and spell in [t-1, t+3]
// answers it, unless a later cast of that spell started before the interrupt (then the kick answered that one).
// Free is a member of a class that has an interrupt, its interrupt spell off cooldown from the member's own cast
// rows, alive, not casting, no stun/silence/incapacitation row of its own in the 2 s before, and within that
// spell's range of the caster. The mob must be in combat with the group and not crowd-controlled or fleeing. The
// hit cites the spell_go row; the key is (16, entry, spell name); one cast (one spell_go per target) is one hit.
//
// UNKNOWN: no spell_go kind in the recording (no completed casts to judge), no interrupt kind (an older
// recording: an unanswered cast may have been kicked), a cast whose source has no creature entry, a window the
// log does not cover, no frame near the cast to place the interrupters, a member whose class is unknown while no
// one is able, or a caster mob the frames do not show in combat.
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
	starts := r.castStarts()
	type castKey struct {
		src   string
		spell int
		at    float64
	}
	seen := map[castKey]bool{}
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
		if answered(answers, starts, src, row.SpellID, row.T) {
			continue
		}
		if !r.covers(row.T-1, row.T+3) {
			r.noteUnknown(16, whyLogCover)
			continue
		}
		mob, ok := r.mobAt(row.Source.GUID, row.T)
		if !ok || mob.NP {
			r.noteUnknown(16, whyNoPosition)
			continue
		}
		if !mob.Combat {
			r.noteUnknown(16, whyNoCombat)
			continue
		}
		if mob.CC || mob.Flee {
			continue // a crowd-controlled or fleeing mob is not casting at the party
		}
		who, able, near, ok, why := r.interrupters(&Unit{X: mob.X, Y: mob.Y, Z: mob.Z}, row.T)
		if !ok {
			r.noteUnknown(16, why)
			continue
		}
		if able == 0 {
			continue
		}
		key := castKey{src: src, spell: row.SpellID, at: row.T}
		if seen[key] {
			continue // one cast's spell_go rows (one per target) are one event
		}
		seen[key] = true
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
// cc_spells; an unlisted CC spell is unknown, never a finding. A cast is the member's rows for one spell within 1 s
// of its first row: its spell_go, miss, resist and immune rows are one event, so a spell_go and its immune row are
// one hit, never two. A cast that hit more than one target is an AoE cast: one immune mob in the pack does not make
// the cast a wrong try, so it is never blamed. A single-target cast is blamed when the log holds its immune row, or
// when the mob is immune to every kind the spell carries (nothing could land). The hit cites the immune row when
// there is one; the key is (17, entry, CC kind).
//
// UNKNOWN: no spell_go, miss, resist or immune kind in the recording. A row whose target has no creature entry,
// or whose caster is not a member, is outside the check, never a finding.
func Rule17(r *Run, t *Tactics) []Hit {
	if !r.hasTimedKinds(ccAttemptKinds) {
		r.noteUnknown(17, ccAttemptReason)
		return nil
	}
	entries := r.tacticsEntries()
	type attempt struct {
		src, member, target string
		entry               uint32
		spell               int
		kind                string
		t                   float64
	}
	var attempts []attempt
	for i := range r.Combat {
		row := &r.Combat[i]
		if !ccAttemptKinds[row.Kind] || !row.TKnown {
			continue
		}
		if _, ok := t.CCSpells[row.SpellID]; !ok {
			continue
		}
		src := CanonGUID(row.Source.GUID)
		member, isMember := r.ByGUID[src]
		if !isMember {
			continue
		}
		target := CanonGUID(row.Target.GUID)
		entry := entries[target]
		if entry == 0 {
			continue
		}
		if _, ok := t.Creature(entry); !ok {
			continue
		}
		attempts = append(attempts, attempt{src: src, member: member, target: target, entry: entry,
			spell: row.SpellID, kind: row.Kind, t: row.T})
	}
	sort.SliceStable(attempts, func(i, j int) bool {
		if attempts[i].src != attempts[j].src {
			return attempts[i].src < attempts[j].src
		}
		if attempts[i].spell != attempts[j].spell {
			return attempts[i].spell < attempts[j].spell
		}
		if attempts[i].t != attempts[j].t {
			return attempts[i].t < attempts[j].t
		}
		return attempts[i].kind < attempts[j].kind
	})
	var hits []Hit
	for start := 0; start < len(attempts); {
		end := start + 1
		for end < len(attempts) && attempts[end].src == attempts[start].src &&
			attempts[end].spell == attempts[start].spell &&
			attempts[end].t-attempts[start].t <= 1.0 {
			end++
		}
		cast := attempts[start:end]
		start = end
		targets := map[string]bool{}
		for _, a := range cast {
			targets[a.target] = true
		}
		if len(targets) > 1 {
			continue // an AoE cast: the immune mob's share is not a wrong try
		}
		first := cast[0]
		ce, ok := t.Creature(first.entry)
		if !ok {
			continue
		}
		kinds := t.CCSpells[first.spell]
		var immune []string
		for _, k := range kinds {
			if ce.immuneTo(k) {
				immune = append(immune, k)
			}
		}
		if len(immune) == 0 {
			continue
		}
		cite, immuneRow := cast[0], false
		for _, a := range cast {
			if a.kind == "immune" {
				cite, immuneRow = a, true
				break
			}
		}
		if !immuneRow && len(immune) != len(kinds) {
			continue // another effect of the spell could have landed
		}
		kind := immune[0]
		ev := fmt.Sprintf("%s cast %s (%s) on %s (immune to %s; combat row %s@%.1f)",
			first.member, r.SpellName(first.spell), kind, mobName(t, first.entry), kind, cite.kind, cite.t)
		hits = append(hits, r.Hit(17, cite.t, 0, first.member, ev, []string{"17", fmt.Sprint(first.entry), kind}))
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
// episode starts at a spell_go from a non-member or an aura_applied row for the victim and spell. Its limit is the
// applying entry's own must_dispel row for a spell_go (an unlisted caster entry is not judged); an aura row with no
// listed caster takes the loosest listing, so an unlisted source can never tighten another entry's limit. The
// episode ends at the first dispel row, aura removal, natural expiry (the aura row's remaining_ms) or the victim's
// death row; an episode with none of those is still up at the log's end. The grace, range and judge are rule 15's.
// The hit cites the applying row; the key is (18, spell name, type).
//
// UNKNOWN: no spell_go or aura_applied kind in the recording, an applying cast whose source has no creature entry,
// an episode with no removal, expiry or death while the recording has no aura_removed rows (its expiry cannot be
// told from an older log), or a window the log does not cover. A victim the frames never show as a member is
// outside the check.
func Rule18(r *Run, t *Tactics) []Hit {
	if !r.hasTimedKinds(auraOnKinds) {
		r.noteUnknown(18, "log has no spell_go or aura_applied rows")
		return nil
	}
	listings := t.dispelListings()
	byEntry := t.dispelByEntry()
	type application struct {
		t      float64
		within float64
		typ    string
	}
	type key struct {
		victim string
		spell  int
	}
	starts := map[key][]application{}
	var order []key
	entries := r.tacticsEntries()
	for i := range r.Combat {
		row := &r.Combat[i]
		if !row.TKnown || !auraOnKinds[row.Kind] {
			continue
		}
		src := CanonGUID(row.Source.GUID)
		var listing TacticDispel
		var ok bool
		if row.Kind == "spell_go" {
			if _, isMember := r.ByGUID[src]; isMember {
				continue
			}
			entry := entries[src]
			if entry == 0 {
				if src != "" {
					r.noteUnknown(18, "an applying cast's source has no creature entry")
				}
				continue
			}
			listing, ok = byEntry[entry][row.SpellID]
			if !ok {
				continue // an unlisted entry is not judged, even when the spell is listed elsewhere
			}
		} else {
			if _, isMember := r.ByGUID[src]; isMember {
				continue
			}
			if entry := entries[src]; entry != 0 {
				d, found := byEntry[entry][row.SpellID]
				if !found {
					continue // that caster entry does not list the spell: not judged by another entry's row
				}
				listing, ok = d, true
			}
			if !ok {
				if d, found := loosestListing(listings[row.SpellID]); found {
					listing, ok = d, true
				}
			}
			if !ok {
				continue
			}
		}
		victim := CanonGUID(row.Target.GUID)
		if victim == "" {
			continue
		}
		k := key{victim: victim, spell: row.SpellID}
		if _, seen := starts[k]; !seen {
			order = append(order, k)
		}
		starts[k] = append(starts[k], application{t: row.T, within: listing.WithinS, typ: listing.Type})
	}
	ends := r.debuffEnds()
	deaths := r.deathTimes()
	logEnd := math.Inf(-1)
	for i := range r.Combat {
		if row := &r.Combat[i]; row.TKnown && row.T > logEnd {
			logEnd = row.T
		}
	}
	removalsKnown := r.hasTimedKinds(auraOffKinds)
	sort.Slice(order, func(i, j int) bool {
		if order[i].victim != order[j].victim {
			return order[i].victim < order[j].victim
		}
		return order[i].spell < order[j].spell
	})
	var hits []Hit
	for _, k := range order {
		member := r.Member(k.victim)
		if r.ByGUID[k.victim] == "" {
			continue // the victim is no member the frames show: outside the check
		}
		apps := starts[k]
		sort.SliceStable(apps, func(i, j int) bool { return apps[i].t < apps[j].t })
		prevEnd := math.Inf(-1)
		for _, app := range apps {
			if app.t < prevEnd {
				continue
			}
			t1, removed := firstRemoval(ends[k.victim][uint32(k.spell)], app.t, math.Inf(1))
			if d, ok := firstRemoval(deaths[k.victim], app.t, math.Inf(1)); ok && (!removed || d < t1) {
				t1, removed = d, true
			}
			still := ""
			if !removed {
				if !removalsKnown {
					r.noteUnknown(18, "log has no aura_removed rows, so an unpulled debuff's expiry is unknown")
					prevEnd = math.Inf(1)
					continue
				}
				t1 = logEnd
				still = " (still up at the end)"
			}
			prevEnd = t1
			if t1-app.t <= app.within {
				continue
			}
			if !r.covers(app.t, t1) {
				r.noteUnknown(18, whyLogCover)
				continue
			}
			able, near, ok := r.dispellers(member, app.t, t1, app.typ)
			if !ok || able == 0 {
				continue
			}
			name := r.SpellName(k.spell)
			ev := fmt.Sprintf("%s (%s) on %s for %.1f s (limit %.0f s); %d able dispeller(s) alive, nearest %.0f yd and not casting%s (applied combat row at t=%.1f)",
				name, app.typ, member, t1-app.t, app.within, able, near, still, app.t)
			hits = append(hits, r.Hit(18, app.t, t1-app.t, member, ev, []string{"18", name, app.typ}))
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

// debuffEnds indexes every moment a debuff ends by victim and spell: a dispel row's removed aura, an aura_removed
// row, and an aura_applied row's natural expiry (its remaining_ms). Sorted and bounded like dispelRemovals.
func (r *Run) debuffEnds() map[string]map[uint32][]float64 {
	out := r.dispelRemovals()
	add := func(victim string, spell uint32, at float64) {
		if victim == "" || spell == 0 {
			return
		}
		if out == nil {
			out = map[string]map[uint32][]float64{}
		}
		if out[victim] == nil {
			out[victim] = map[uint32][]float64{}
		}
		times := out[victim][spell]
		i := sort.SearchFloat64s(times, at)
		times = append(times, 0)
		copy(times[i+1:], times[i:])
		times[i] = at
		if len(times) > maxDispelTimes {
			times = times[len(times)-maxDispelTimes:]
		}
		out[victim][spell] = times
	}
	for i := range r.Combat {
		row := &r.Combat[i]
		if !row.TKnown || row.SpellID <= 0 {
			continue
		}
		victim := CanonGUID(row.Target.GUID)
		switch row.Kind {
		case "aura_removed":
			add(victim, uint32(row.SpellID), row.T)
		case "aura_applied":
			if row.RemainingMS > 0 {
				add(victim, uint32(row.SpellID), row.T+float64(row.RemainingMS)/1000)
			}
		}
	}
	return out
}

// deathTimes indexes the timed death rows by the guid that died.
func (r *Run) deathTimes() map[string][]float64 {
	var out map[string][]float64
	for i := range r.Combat {
		row := &r.Combat[i]
		if row.Kind != "death" || !row.TKnown {
			continue
		}
		victim := CanonGUID(row.Target.GUID)
		if victim == "" {
			continue
		}
		if out == nil {
			out = map[string][]float64{}
		}
		out[victim] = append(out[victim], row.T)
	}
	return out
}

// loosestListing is the longest within_s any entry gives the spell: what an aura row with no listed caster is
// judged by, so an unlisted source can never make a finding stricter (or sooner) than the file's own words.
func loosestListing(list []dispelListing) (TacticDispel, bool) {
	var best TacticDispel
	found := false
	for _, d := range list {
		if !found || d.WithinS > best.WithinS {
			best, found = d.TacticDispel, true
		}
	}
	return best, found
}

// Rule19: a lower-priority mob killed first while a higher-priority one stood in reach of the group. The kill is
// the mob's own death row (a recording with no death rows is UNKNOWN: a frame gap is not a death). Only mobs in
// combat with the group count: both are seen in a frame with the group in combat, at or before the kill, and the
// higher one had an alive member within R19_REACH_YD of it there. Priority 0 is unordered, an unlisted entry is
// outside the order, and two entries with the same priority are one wave. Every kill and every living mob is
// judged by its own guid, so three same-entry mobs are three mobs. The hit cites the death row and that frame; the
// key is (19, dungeon, low guid, high guid).
//
// UNKNOWN: no death kind in the recording, a killed mob the frames never show in combat with the group, a higher
// mob the frames never show in combat with the group, a position the frames do not know, or a higher mob whose
// fate past the kill cannot be told.
func Rule19(r *Run, t *Tactics) []Hit {
	if !r.hasTimedKinds(deathKinds) {
		r.noteUnknown(19, "log has no death rows")
		return nil
	}
	entries := r.tacticsEntries()
	priorityOf := func(entry uint32) (int, bool) {
		ce, ok := t.Creature(entry)
		if !ok || ce.Priority <= 0 {
			return 0, false
		}
		return ce.Priority, true
	}
	priority := func(guid string) (int, bool) { return priorityOf(entries[guid]) }
	var guids []string
	for g := range entries {
		if _, ordered := priority(g); ordered {
			guids = append(guids, g)
		}
	}
	sort.Strings(guids)
	if len(guids) == 0 {
		return nil
	}
	deaths, deathRow := r.deathTimesByGUID()
	idx := r.indexFrames()
	var hits []Hit
	for _, low := range guids {
		tk, killed := deaths[low]
		if !killed {
			continue // every guid is a death row's target: a living guid has none
		}
		lowPrio, _ := priority(low)
		if !r.inCombatWithGroup(low, tk, idx) {
			r.noteUnknown(19, "a killed mob was not in combat with the group")
			continue
		}
		for _, high := range guids {
			if high == low {
				continue
			}
			highPrio, ordered := priority(high)
			if !ordered || highPrio >= lowPrio {
				continue
			}
			if hd, died := deaths[high]; died {
				if hd <= tk {
					continue // it was already dead: not a mob the group passed over
				}
			} else if !r.seenAfter(high, tk, idx) {
				r.noteUnknown(19, "a higher mob's fate past the kill is unknown")
				continue
			}
			fi, lowMob, highMob, ok := r.pullFrame(low, high, tk, idx)
			if !ok {
				r.noteUnknown(19, "the group's combat with both mobs is unknown")
				continue
			}
			if lowMob.NP || highMob.NP {
				r.noteUnknown(19, whyNoPosition)
				continue
			}
			bot, d := nearestMember(r.Frames[fi], highMob.X, highMob.Y)
			if bot == "" {
				r.noteUnknown(19, "no member in the frame to place the pull")
				continue
			}
			if d > R19_REACH_YD {
				continue
			}
			cite := fmt.Sprintf("combat row death@%.1f", tk)
			if ri, ok := deathRow[low]; ok {
				cite = fmt.Sprintf("combat row %s@%.1f", r.Combat[ri].Kind, r.Combat[ri].T)
			}
			ev := fmt.Sprintf("%s killed at t=%.1f s while %s (priority %d before %d) was in combat %.0f yd from %s (frame t=%.1f s; %s)",
				mobName(t, entries[low]), tk, mobName(t, entries[high]), highPrio, lowPrio, d, bot, r.Frames[fi].T, cite)
			dungeon := t.Dungeon
			if dungeon == "" {
				dungeon = "tactics"
			}
			hits = append(hits, r.Hit(19, tk, 0, bot, ev,
				[]string{"19", dungeon, low, high}))
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

// deathTimesByGUID indexes the first timed death row of every guid, and the row's index.
func (r *Run) deathTimesByGUID() (map[string]float64, map[string]int) {
	at, row := map[string]float64{}, map[string]int{}
	for i := range r.Combat {
		rowAt := &r.Combat[i]
		if rowAt.Kind != "death" || !rowAt.TKnown {
			continue
		}
		g := CanonGUID(rowAt.Target.GUID)
		if g == "" {
			continue
		}
		if _, done := at[g]; !done {
			at[g], row[g] = rowAt.T, i
		}
	}
	return at, row
}

// frameIndex is the frames' mobs by canonical guid, and each guid's frame indexes: the kill-order check's lookups
// are list walks, never a scan of every frame for every pair.
type frameIndex struct {
	mobs []map[string]*Mob
	at   map[string][]int
}

// indexFrames builds the frame index once per rule.
func (r *Run) indexFrames() frameIndex {
	idx := frameIndex{mobs: make([]map[string]*Mob, len(r.Frames)), at: map[string][]int{}}
	for fi, f := range r.Frames {
		m := make(map[string]*Mob, len(f.Mobs))
		for _, mob := range f.Mobs {
			if g := CanonGUID(mob.G); g != "" {
				m[g] = mob
				idx.at[g] = append(idx.at[g], fi)
			}
		}
		idx.mobs[fi] = m
	}
	return idx
}

// inCombatWithGroup says the guid was seen in a frame at or before at with the group in combat and the mob in
// combat: a mob that never was is not part of the pull.
func (r *Run) inCombatWithGroup(guid string, at float64, idx frameIndex) bool {
	frames := idx.at[guid]
	for i := len(frames) - 1; i >= 0; i-- {
		fi := frames[i]
		f := r.Frames[fi]
		if f.T > at {
			continue
		}
		if m := idx.mobs[fi][guid]; m != nil && m.Combat && f.GC {
			return true
		}
	}
	return false
}

// seenAfter says the guid appears in a frame after at.
func (r *Run) seenAfter(guid string, at float64, idx frameIndex) bool {
	for _, fi := range idx.at[guid] {
		if r.Frames[fi].T > at {
			return true
		}
	}
	return false
}

// pullFrame finds the last frame at or before tk that shows both guids in combat with the group, and returns it
// with both mobs: the pull the order is judged from.
func (r *Run) pullFrame(low, high string, tk float64, idx frameIndex) (int, *Mob, *Mob, bool) {
	frames := idx.at[low]
	for i := len(frames) - 1; i >= 0; i-- {
		fi := frames[i]
		f := r.Frames[fi]
		if f.T > tk || !f.GC {
			continue
		}
		lo, hi := idx.mobs[fi][low], idx.mobs[fi][high]
		if lo != nil && hi != nil && lo.Combat && hi.Combat {
			return fi, lo, hi, true
		}
	}
	return 0, nil, nil, false
}

// nearestMember is the nearest alive subject to (x, y) in the frame, and its distance; empty when the frame holds
// no alive subject.
func nearestMember(f *Frame, x, y float64) (string, float64) {
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
	return best, near
}
