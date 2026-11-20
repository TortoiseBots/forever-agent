package bughunt

// The rules that read a run's combat log (C) beside its frames (F): 1, 2, 4, 8, 11 and 15. A watch writes the log
// beside its trace; a trials repeat carries the recorder's raw rows in `raw-ta.jsonl`. A run without the log keeps
// these rules silent: the check is skipped and counted, never answered from a guess. Spell ids are named from the
// live-checked files (`--names`, `--dispel`); an unnamed id is `spell <id>` and matches no name rule.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// EventRules are the rules that need the combat log beside the frames: 1, 2, 4, 8, 11 and 15.
func EventRules() []Rule {
	return []Rule{
		{1, ruleNames[1], Rule1, skipNoCombatLog},
		{2, ruleNames[2], Rule2, skipHealNeedsNames},
		{4, ruleNames[4], Rule4, skipAoENeedsNames},
		{8, ruleNames[8], Rule8, skipAoENeedsNames},
		{11, ruleNames[11], Rule11, skipNoCombatLog},
		{15, ruleNames[15], Rule15, skipDispel},
	}
}

// skipNoCombatLog says a run carries no combat log, so a rule that reads one cannot judge it.
func skipNoCombatLog(r *Run) string {
	if len(r.Combat) == 0 {
		return "no combat log"
	}
	if !r.cover().ok {
		return "combat log has no timed rows"
	}
	return ""
}

// skipHealNeedsNames: a healer's casts are only known through their names (and the heal rows themselves).
func skipHealNeedsNames(r *Run) string {
	if reason := skipNoCombatLog(r); reason != "" {
		return reason
	}
	if len(r.Names) == 0 {
		return "no spell names"
	}
	return ""
}

// skipAoENeedsNames: an AoE or a buff is only known through its name.
func skipAoENeedsNames(r *Run) string {
	if reason := skipNoCombatLog(r); reason != "" {
		return reason
	}
	if len(r.Names) == 0 {
		return "no spell names"
	}
	return ""
}

// skipDispel: a debuff is only known through the live-checked dispel file, and its removal only through classes.
func skipDispel(r *Run) string {
	if len(r.Dispel) == 0 {
		return "no dispel types"
	}
	if !r.knowsClasses() {
		return "no classes"
	}
	return ""
}

// knowsClasses says whether the run names any member's class: rule 15 cannot judge a dispeller with no class.
func (r *Run) knowsClasses() bool {
	for _, f := range r.Frames {
		for _, n := range f.Order {
			if r.Class(n) != "?" {
				return true
			}
		}
	}
	return false
}

// rowsNear says whether one of the indexed rows is a kind in the set with a known time in [t-lo, t+hi], and
// calls match on it when match is not nil.
func (r *Run) rowsNear(idxs []int, t, lo, hi float64, kinds map[string]bool, match func(*CombatRow) bool) bool {
	for _, i := range idxs {
		row := &r.Combat[i]
		if !row.TKnown || row.T < t-lo || row.T > t+hi {
			continue
		}
		if !kinds[row.Kind] {
			continue
		}
		if match == nil || match(row) {
			return true
		}
	}
	return false
}

// castKinds are the rows that say a member cast or swung: a spell start or completion, or a melee swing.
var castKinds = map[string]bool{"spell_start": true, "spell_go": true, "melee": true}

// eating says the module reports the member eating, drinking or resting (or the engine's own state says resting):
// a bot at its meal is rule 6's, never rule 1's or 2's.
func eating(u *Unit) bool {
	return u.Eat || u.Drink || u.Rest || u.State == "r"
}

// quietAcross says a member did nothing the kinds name anywhere across an episode, not only beside its frames
// (frames can be up to MAX_GAP_S apart), and that the log provably covers the whole span. An episode over a stretch
// the log does not cover is counted and dropped.
func (r *Run) quietAcross(rule int, ep Episode, lo, hi float64, kinds map[string]bool, match func(*CombatRow) bool) bool {
	if !r.covers(ep.T0-lo, ep.T1+hi) {
		r.noteUnknown(rule, whyLogCover)
		return false
	}
	for _, i := range r.rowsFromMember(ep.Bot) {
		row := &r.Combat[i]
		if !row.TKnown || row.T < ep.T0-lo || row.T > ep.T1+hi || !kinds[row.Kind] {
			continue
		}
		if match == nil || match(row) {
			return false
		}
	}
	return true
}

// Rule1: idle in combat R1_IDLE_S or more: alive, the group in combat R1_GROUP_COMBAT_MIN_S, not moving and not
// casting, no cast or swing in the log within R1_CAST_PAD_S of the frame, and (for a healer) somebody hurt. A
// non-idle action word from the module breaks it too.
func Rule1(r *Run) []Hit {
	pred := func(f *Frame, n string, u *Unit) (string, bool) {
		if !u.Alive || !lifeOK(u) || !f.GC || !f.HasGCSince || f.T-f.GCSince < R1_GROUP_COMBAT_MIN_S {
			return "", false
		}
		if u.Moving || u.Casting || eating(u) {
			return "", false
		}
		if isHealer(r.Role(n)) {
			hurt := false
			for _, a := range f.Order {
				v := f.Units[a]
				if a != n && v.Alive && hpPctOf(v) < R1_HURT_PCT {
					hurt = true
					break
				}
			}
			if !hurt {
				return "", false
			}
		}
		act := strings.ToLower(u.Act)
		if eatRE.MatchString(act) {
			return "", false
		}
		if act != "" {
			idle := false
			for _, a := range idleActs {
				if strings.HasPrefix(act, a) {
					idle = true
					break
				}
			}
			if !idle {
				return "", false
			}
		}
		if found, known := r.logSays(1, r.rowsFromMember(n), f.T, R1_CAST_PAD_S, R1_CAST_PAD_S, castKinds, nil); found || !known {
			return "", false
		}
		if act == "" {
			act = "-"
		}
		return act, true
	}
	var hits []Hit
	for _, ep := range r.Episodes(pred, R1_IDLE_S, true) {
		if !r.quietAcross(1, ep, R1_CAST_PAD_S, R1_CAST_PAD_S, castKinds, nil) {
			continue
		}
		act := commonestEvidence(ep)
		mobs := 0
		if len(ep.Steps) > 0 {
			for _, m := range ep.Steps[0].Frame.Mobs {
				if m.Combat {
					mobs++
				}
			}
		}
		ev := fmt.Sprintf("idle %.0f s in combat, act %s, %d frames, %d mobs in combat",
			ep.T1-ep.T0, act, len(ep.Steps), mobs)
		hits = append(hits, r.Hit(1, ep.T0, ep.T1-ep.T0, ep.Bot, ev, []string{"1", r.Role(ep.Bot)}))
	}
	return hits
}

// commonestEvidence is the most common evidence text of an episode: ties keep the first seen.
func commonestEvidence(ep Episode) string {
	counts := map[string]int{}
	order := []string{}
	for _, s := range ep.Steps {
		if _, ok := counts[s.Ev]; !ok {
			order = append(order, s.Ev)
		}
		counts[s.Ev]++
	}
	best, bestN := "-", 0
	for _, e := range order {
		if counts[e] > bestN {
			best, bestN = e, counts[e]
		}
	}
	return best
}

// healKinds are the rows a healer's work shows in: a heal, or a heal spell's start or completion.
var healKinds = map[string]bool{"heal": true, "spell_start": true, "spell_go": true}

// Rule2: a healer with mana over R2_MANA_MIN_PCT not healing an alive ally under R2_LOW_HP_PCT within
// R2_RANGE_YD for longer than R2_MIN_S, while not casting and with no heal row or heal-spell cast within
// R2_HEAL_PAD_S.
func Rule2(r *Run) []Hit {
	pred := func(f *Frame, n string, u *Unit) (string, bool) {
		if !isHealer(r.Role(n)) || !u.Alive || !lifeOK(u) || !f.GC || u.PType != 0 || eating(u) {
			return "", false
		}
		if pctOf(u) <= R2_MANA_MIN_PCT {
			return "", false
		}
		who, pct, ok := lowestAlly(f, n, R2_LOW_HP_PCT, R2_RANGE_YD)
		if !ok {
			return "", false
		}
		if u.Casting {
			return "", false
		}
		if found, known := r.logSays(2, r.rowsFromMember(n), f.T, R2_HEAL_PAD_S, R2_HEAL_PAD_S, healKinds, r.healRow); found || !known {
			return "", false
		}
		act := u.Act
		if act == "" {
			act = "-"
		}
		return fmt.Sprintf("%s %.0f%% act=%s", who, pct, act), true
	}
	var hits []Hit
	for _, ep := range r.Episodes(pred, R2_MIN_S, true) {
		if !r.quietAcross(2, ep, R2_HEAL_PAD_S, R2_HEAL_PAD_S, healKinds, r.healRow) {
			continue
		}
		who := commonestWho(ep)
		first, last := ep.Steps[0].Ev, ep.Steps[len(ep.Steps)-1].Ev
		mana := 0.0
		if len(ep.Steps) > 0 {
			u := ep.Steps[0].Frame.Units[ep.Bot]
			mana = pctOf(u)
		}
		ev := fmt.Sprintf("healer idle on %s: %s ... %s (%d frames, mana %.0f%%)", who, first, last, len(ep.Steps), mana)
		hits = append(hits, r.Hit(2, ep.T0, ep.T1-ep.T0, ep.Bot, ev, []string{"2", r.Class(ep.Bot)}))
	}
	return hits
}

// healRow says a heal-kind row is a heal, or the start or completion of a heal spell.
func (r *Run) healRow(row *CombatRow) bool {
	return row.Kind == "heal" || healSpellRE.MatchString(r.SpellName(row.SpellID))
}

// lowestAlly is the alive ally under pct within rng yards of the unit, lowest health first.
func lowestAlly(f *Frame, self string, pct, rng float64) (string, float64, bool) {
	u := f.Units[self]
	who, low, ok := "", 0.0, false
	for _, a := range f.Order {
		v := f.Units[a]
		if a == self || !v.Alive || v.MaxHP == 0 {
			continue
		}
		p := hpPctOf(v)
		if p >= pct || dist3(u, v) > rng {
			continue
		}
		if !ok || p < low {
			who, low, ok = a, p, true
		}
	}
	return who, low, ok
}

// commonestWho is the first word of the most common evidence in the episode: the ally the healer left hurt.
func commonestWho(ep Episode) string {
	counts := map[string]int{}
	order := []string{}
	for _, s := range ep.Steps {
		who := strings.Fields(s.Ev)
		if len(who) == 0 {
			continue
		}
		if _, ok := counts[who[0]]; !ok {
			order = append(order, who[0])
		}
		counts[who[0]]++
	}
	best, bestN := "-", 0
	for _, w := range order {
		if counts[w] > bestN {
			best, bestN = w, counts[w]
		}
	}
	return best
}

// Rule4: standing in a damaging AoE more than R4_MIN_S: the same spell ticking on the same member, ticks further
// apart than R4_TICK_GAP_S ending the episode. The spell's name is what says it leaves an area effect.
func Rule4(r *Run) []Hit {
	type group struct {
		dst, spell, src string
	}
	ticks := map[group][]float64{}
	var order []group
	for i := range r.Combat {
		row := &r.Combat[i]
		if !row.TKnown || (row.Kind != "periodic" && row.Kind != "spell_damage") {
			continue
		}
		dst := r.Member(row.Target.GUID)
		if dst == "" || !r.IsSubject(dst) {
			continue
		}
		spell := r.SpellName(row.SpellID)
		if !aoeRE.MatchString(spell) {
			continue
		}
		src := row.Source.Name
		if src == "" {
			src = r.Member(row.Source.GUID)
		}
		g := group{dst: dst, spell: spell, src: src}
		if _, ok := ticks[g]; !ok {
			order = append(order, g)
		}
		ticks[g] = append(ticks[g], row.T)
	}
	var hits []Hit
	sort.Slice(order, func(i, j int) bool {
		if order[i].dst != order[j].dst {
			return order[i].dst < order[j].dst
		}
		if order[i].spell != order[j].spell {
			return order[i].spell < order[j].spell
		}
		return order[i].src < order[j].src
	})
	for _, g := range order {
		ts := ticks[g]
		sort.Float64s(ts)
		start, prev, count := ts[0], ts[0], 1
		for _, t := range append(append([]float64(nil), ts[1:]...), math.Inf(1)) {
			if t-prev > R4_TICK_GAP_S {
				if prev-start > R4_MIN_S {
					src := g.src
					if src == "" {
						src = "?"
					}
					ev := fmt.Sprintf("%s from %s ticked %d times over %.1f s on %s", g.spell, src, count, prev-start, g.dst)
					if r.covers(start, prev) {
						hits = append(hits, r.Hit(4, start, prev-start, g.dst, ev, []string{"4", g.spell}))
					} else {
						r.noteUnknown(4, whyLogCover)
					}
				}
				start, count = t, 0
			}
			prev = t
			count++
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

// Rule8: the same buff cast more than R8_MAX_CASTS times on the same target within R8_WINDOW_S. A completion is
// genuine when a start of the same spell came just before it or the aura landed just after it.
func Rule8(r *Run) []Hit {
	type pair struct {
		guid string
		sid  int
	}
	type group struct {
		src, spell, dst string
		sid             int
	}
	starts := map[pair][]float64{}
	aurasOn := map[pair][]float64{}
	aurasOff := map[pair][]float64{}
	for i := range r.Combat {
		row := &r.Combat[i]
		if !row.TKnown {
			continue
		}
		src, dst := r.Member(row.Source.GUID), r.Member(row.Target.GUID)
		switch row.Kind {
		case "spell_start":
			starts[pair{src, row.SpellID}] = append(starts[pair{src, row.SpellID}], row.T)
		case "aura_applied":
			aurasOn[pair{dst, row.SpellID}] = append(aurasOn[pair{dst, row.SpellID}], row.T)
		case "aura_removed":
			aurasOff[pair{dst, row.SpellID}] = append(aurasOff[pair{dst, row.SpellID}], row.T)
		}
	}
	casts := map[group][]float64{}
	var order []group
	for i := range r.Combat {
		row := &r.Combat[i]
		if !row.TKnown || row.Kind != "spell_go" {
			continue
		}
		src, dst := r.Member(row.Source.GUID), r.Member(row.Target.GUID)
		if src == "" || !r.IsSubject(src) {
			continue
		}
		spell := r.SpellName(row.SpellID)
		if !buffRE.MatchString(spell) {
			continue
		}
		genuine := nearTime(starts[pair{src, row.SpellID}], row.T, R8_NEAR_START_S, 0) ||
			nearTime(aurasOn[pair{dst, row.SpellID}], row.T, 0.1, R8_NEAR_AURA_S)
		if !genuine {
			continue
		}
		g := group{src: src, spell: spell, dst: dst, sid: row.SpellID}
		if _, ok := casts[g]; !ok {
			order = append(order, g)
		}
		casts[g] = append(casts[g], row.T)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].src != order[j].src {
			return order[i].src < order[j].src
		}
		if order[i].spell != order[j].spell {
			return order[i].spell < order[j].spell
		}
		if order[i].dst != order[j].dst {
			return order[i].dst < order[j].dst
		}
		return order[i].sid < order[j].sid
	})
	var hits []Hit
	for _, g := range order {
		ts := casts[g]
		sort.Float64s(ts)
		i, reported := 0, math.Inf(-1)
		for j := range ts {
			for ts[j]-ts[i] > R8_WINDOW_S {
				i++
			}
			if j-i+1 > R8_MAX_CASTS && ts[i] > reported+R8_WINDOW_S {
				life := -1.0
				if l := auraLives(aurasOn[pair{g.dst, g.sid}], aurasOff[pair{g.dst, g.sid}]); len(l) > 0 {
					life = l[len(l)/2]
				}
				kind := "long-aura"
				if life >= 0 && life < 10 {
					kind = "short-aura"
				}
				ev := fmt.Sprintf("%s cast %d x in %.0f s on %s (median aura life %.1f s, %s)", g.spell, j-i+1, ts[j]-ts[i], g.dst, life, kind)
				if r.covers(ts[i], ts[j]) {
					// The key is the spell alone: the aura-life word is derived from measured data and can flip between
					// nights, so it lives in the evidence and the note, never in the ticket's identity.
					hits = append(hits, r.Hit(8, ts[i], ts[j]-ts[i], g.src, ev, []string{"8", g.spell},
						Note(fmt.Sprintf("aura_life=%.1f", life))))
				} else {
					r.noteUnknown(8, whyLogCover)
				}
				reported = ts[j]
			}
		}
	}
	return hits
}

// nearTime says whether one of the times is in [t-lo, t+hi].
func nearTime(times []float64, t, lo, hi float64) bool {
	for _, x := range times {
		if x >= t-lo && x <= t+hi {
			return true
		}
	}
	return false
}

// auraLives is each aura's life: the first removal after its application, from the sorted rows. Auras still up at
// the end of the log have no life.
func auraLives(on, off []float64) []float64 {
	onSorted := append([]float64(nil), on...)
	offSorted := append([]float64(nil), off...)
	sort.Float64s(onSorted)
	sort.Float64s(offSorted)
	var lives []float64
	for _, t := range onSorted {
		k := sort.SearchFloat64s(offSorted, t)
		for k < len(offSorted) && offSorted[k] <= t {
			k++
		}
		if k < len(offSorted) {
			lives = append(lives, offSorted[k]-t)
		}
	}
	sort.Float64s(lives)
	return lives
}

// outputKinds are the rows that say a member's damage or heal landed.
var outputKinds = map[string]bool{"melee": true, "spell_damage": true, "periodic": true, "heal": true}

// Rule11: casts started with nothing landing for more than R11_MIN_S while the member's mana rose: the
// cast-cancel signal. The member is a mana user with a caster role, in combat; every frame of the episode has no
// damage, heal or swing row from it, and the episode holds at least R11_MIN_STARTS started casts whose spell
// never completed.
func Rule11(r *Run) []Hit {
	pred := func(f *Frame, n string, u *Unit) (string, bool) {
		if !u.Alive || !lifeOK(u) || !f.GC || u.PType != 0 {
			return "", false
		}
		switch r.Role(n) {
		case "healer", "heal", "dps":
		default:
			return "", false
		}
		if found, known := r.logSays(11, r.rowsFromMember(n), f.T, R11_OUTPUT_PAD_S, R11_OUTPUT_PAD_S, outputKinds, nil); found || !known {
			return "", false
		}
		return "silent", true
	}
	var hits []Hit
	for _, ep := range r.Episodes(pred, R11_MIN_S, true) {
		first := ep.Steps[0].Frame.Units[ep.Bot]
		last := ep.Steps[len(ep.Steps)-1].Frame.Units[ep.Bot]
		if last.Power <= first.Power {
			continue // no regen: the mana did not rise, so the cancel signal is absent
		}
		if !r.quietAcross(11, ep, R11_OUTPUT_PAD_S, R11_OUTPUT_PAD_S, outputKinds, nil) {
			continue
		}
		if !r.covers(ep.T0-1, ep.T1+1+R11_CAST_LAND_S) {
			r.noteUnknown(11, whyLogCover) // a landing past the log's end cannot be told from a cancel
			continue
		}
		starts, unlanded := 0, 0
		reasons := map[string]int{}
		for _, i := range r.rowsFromMember(ep.Bot) {
			row := &r.Combat[i]
			if !row.TKnown || row.T < ep.T0-1 || row.T > ep.T1+1 {
				continue
			}
			switch row.Kind {
			case "spell_start":
				starts++
				if !r.lands(ep.Bot, row.SpellID, row.T) {
					unlanded++
				}
			case "spell_fail":
				reason := row.Reason
				if reason == "" {
					reason = "unknown"
				}
				reasons[reason]++
			}
		}
		if starts < R11_MIN_STARTS || unlanded < R11_MIN_STARTS || 2*unlanded < len(ep.Steps) {
			continue
		}
		extra := ""
		if len(reasons) > 0 {
			extra = " (fail reasons " + reasonText(reasons) + ")"
		}
		ev := fmt.Sprintf("started %d casts in %d frames with no heal/damage landed for %.0f s while mana rose %.0f->%.0f%s",
			unlanded, len(ep.Steps), ep.T1-ep.T0, first.Power, last.Power, extra)
		hits = append(hits, r.Hit(11, ep.T0, ep.T1-ep.T0, ep.Bot, ev,
			[]string{"11", r.Role(ep.Bot), r.Class(ep.Bot), "cast-cancel-signal"}))
	}
	return hits
}

// lands says whether a cast's spell completed: a spell_go of the same spell within R11_CAST_LAND_S of its start.
func (r *Run) lands(src string, spell int, t float64) bool {
	for _, i := range r.rowsFromMember(src) {
		row := &r.Combat[i]
		if row.Kind != "spell_go" || row.SpellID != spell || !row.TKnown {
			continue
		}
		if row.T >= t && row.T <= t+R11_CAST_LAND_S {
			return true
		}
	}
	return false
}

// reasonText is a reason count as `interrupted x2, no-mana x1`, sorted.
func reasonText(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s x%d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}

// auraInterval is one stay of one aura on one member.
type auraInterval struct {
	Bot     string
	SpellID uint32
	T0, T1  float64
	Still   bool // still up at the recording's end
	// Lost says the aura's end was never seen: the member left the commander's view or the frames broke. Its life
	// after the last sighting is unknown, never a removal.
	Lost bool
}

// auraIntervals reads each member's auras from the frames: an aura is up from the first frame that lists it to the
// first frame that does not (a gap of MAX_GAP_S closes it too, as it breaks any episode). The order is by start.
func (r *Run) auraIntervals() []auraInterval {
	type open struct{ start, last float64 }
	var (
		cur      = map[string]map[uint32]*open{}
		closed   []auraInterval
		lastT    float64
		haveLast bool
	)
	closeOne := func(n string, id uint32, end float64, still, lost bool) {
		o := cur[n][id]
		if o == nil {
			return
		}
		delete(cur[n], id)
		closed = append(closed, auraInterval{Bot: n, SpellID: id, T0: o.start, T1: end, Still: still, Lost: lost})
	}
	closeAll := func(end func(o *open) float64, still, lost bool) {
		for n, ids := range cur {
			for id, o := range ids {
				closeOne(n, id, end(o), still, lost)
			}
		}
	}
	for _, f := range r.Frames {
		if haveLast && f.T-lastT > MAX_GAP_S {
			closeAll(func(o *open) float64 { return o.last }, false, true)
		}
		lastT, haveLast = f.T, true
		for n, ids := range cur {
			u, ok := f.Units[n]
			for id := range ids {
				if !ok {
					// Out of the commander's view is not a debuff ending: the episode is unknown from here.
					closeOne(n, id, cur[n][id].last, false, true)
				} else if !hasAura(u, id) {
					closeOne(n, id, f.T, false, false)
				}
			}
		}
		for _, n := range f.Order {
			u := f.Units[n]
			for _, id := range u.Auras {
				if cur[n] == nil {
					cur[n] = map[uint32]*open{}
				}
				if cur[n][id] == nil {
					cur[n][id] = &open{start: f.T, last: f.T}
				} else {
					cur[n][id].last = f.T
				}
			}
		}
	}
	end := 0.0
	if len(r.Frames) > 0 {
		end = r.Frames[len(r.Frames)-1].T
	}
	closeAll(func(*open) float64 { return end }, true, false)
	sort.SliceStable(closed, func(i, j int) bool {
		if closed[i].T0 != closed[j].T0 {
			return closed[i].T0 < closed[j].T0
		}
		if closed[i].Bot != closed[j].Bot {
			return closed[i].Bot < closed[j].Bot
		}
		return closed[i].SpellID < closed[j].SpellID
	})
	return closed
}

// hasAura says whether the unit's model lists the spell.
func hasAura(u *Unit, id uint32) bool {
	for _, a := range u.Auras {
		if a == id {
			return true
		}
	}
	return false
}

// canDispel says whether a class can remove the type in this core's scripts.
func canDispel(class, typ string) bool {
	for _, c := range dispelClasses[typ] {
		if c == class {
			return true
		}
	}
	return false
}

// Rule15: a removable debuff (a dispel file type) on a member for longer than R15_MIN_S while an able dispeller
// was alive, within R15_RANGE_YD and not casting after the grace. The debuff's name is the live-checked name, or
// `spell <id>`.
func Rule15(r *Run) []Hit {
	var hits []Hit
	for _, iv := range r.auraIntervals() {
		if !r.IsSubject(iv.Bot) {
			continue
		}
		typ := r.DispelType(int(iv.SpellID))
		if typ == "" || typ == "never" {
			continue
		}
		if iv.Lost {
			r.noteUnknown(15, whyOutOfView)
			continue
		}
		if iv.T1-iv.T0 <= R15_MIN_S {
			continue
		}
		able, nearest, ok := r.dispellers(iv.Bot, iv.T0, iv.T1, typ)
		if !ok || able == 0 {
			continue
		}
		name := r.SpellName(int(iv.SpellID))
		suffix := ""
		if iv.Still {
			suffix = " (still up at the end)"
		}
		ev := fmt.Sprintf("%s (%s) on %s %s for %.1f s; %d able dispeller(s) alive, nearest %.0f yd and not casting%s",
			name, typ, r.Class(iv.Bot), r.Role(iv.Bot), iv.T1-iv.T0, able, nearest, suffix)
		hits = append(hits, r.Hit(15, iv.T0, iv.T1-iv.T0, iv.Bot, ev, []string{"15", typ, name}))
	}
	return hits
}

// dispellers is rule 15's judge: the most able dispellers seen in one frame after the grace (any frame when the
// debuff ended inside the grace), and the nearest of them.
func (r *Run) dispellers(victim string, t0, t1 float64, typ string) (int, float64, bool) {
	frames := r.framesBetween(t0+R15_GRACE_S, t1)
	if len(frames) == 0 {
		frames = r.framesBetween(t0, t1)
	}
	best, nearest, found := 0, math.Inf(1), false
	for _, f := range frames {
		v, ok := f.Units[victim]
		if !ok || !v.Alive {
			continue
		}
		able, near := 0, math.Inf(1)
		for _, a := range f.Order {
			u := f.Units[a]
			if a == victim || !canDispel(r.Class(a), typ) || !u.Alive {
				continue
			}
			if u.Casting || u.Spell != 0 {
				continue
			}
			if d := dist3(u, v); d <= R15_RANGE_YD {
				able++
				if d < near {
					near = d
				}
			}
		}
		if able > 0 && (!found || able > best) {
			best, nearest, found = able, near, true
		}
	}
	if !found {
		return 0, 0, false
	}
	return best, nearest, true
}
