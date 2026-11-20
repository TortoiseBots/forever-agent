package bughunt

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Rule is one checker rule: its number, its name and the function over a loaded run.
type Rule struct {
	Number int
	Name   string
	Run    func(*Run) []Hit
}

// FrameRules are the rules that need only the frames and the run's own records: 3, 5, 6, 9, 10, 13 and 14.
func FrameRules() []Rule {
	return []Rule{
		{3, ruleNames[3], Rule3},
		{5, ruleNames[5], Rule5},
		{6, ruleNames[6], Rule6},
		{9, ruleNames[9], Rule9},
		{10, ruleNames[10], Rule10},
		{13, ruleNames[13], Rule13},
		{14, ruleNames[14], Rule14},
	}
}

// sparseOpts marks a hit sparse when the run's frames are a walk's 2-14 s snapshots, as the Python's sparse(run)
// does.
func (r *Run) sparseOpts(opts ...HitOption) []HitOption {
	if r.Kind == "walk" {
		return append(opts, Sparse())
	}
	return opts
}

// isHealer is the checker's healer test. A watch's party says "heal" and a trials repeat says "healer"; both are the
// same slot.
func isHealer(role string) bool { return role == "healer" || role == "heal" }

// lifeOK says whether the module's life word is a member the rules can judge: alive (or resting), or a run that does
// not record it.
func lifeOK(u *Unit) bool {
	switch u.Life {
	case "", "alive", "resting":
		return true
	}
	return false
}

// Rule3: a hostile attacking a non-tank for more than R3_MIN_S while a tank is alive. The victim is the threat
// victim a watch's `.tb threat` names (the model's target where a frame has that instead), and a boss its instance
// report listed is never an add.
func Rule3(r *Run) []Hit {
	type key struct{ mob, bot string }
	type step struct {
		f *Frame
		m *Mob
	}
	type ep struct {
		k   key
		seq []step
	}
	var (
		cur   = map[key][]step{}
		open  []key
		eps   []ep
		hits  []Hit
		lastT float64
		haveT bool
	)
	closeKey := func(k key) {
		seq := cur[k]
		if len(seq) == 0 {
			return
		}
		delete(cur, k)
		for i, o := range open {
			if o == k {
				open = append(open[:i], open[i+1:]...)
				break
			}
		}
		if seq[len(seq)-1].f.T-seq[0].f.T > R3_MIN_S {
			eps = append(eps, ep{k, seq})
		}
	}
	closeAll := func() {
		for len(open) > 0 {
			closeKey(open[0])
		}
	}
	for _, f := range r.Frames {
		if haveT && f.T-lastT > MAX_GAP_S {
			closeAll()
		}
		lastT, haveT = f.T, true
		byGUID := map[string]string{}
		for _, n := range f.Order {
			u := f.Units[n]
			byGUID[u.G] = n
			byGUID[CanonGUID(u.G)] = n
		}
		tanks := 0
		for _, n := range f.Order {
			if r.Role(n) == "tank" && f.Units[n].Alive {
				tanks++
			}
		}
		seen := map[key]bool{}
		if tanks > 0 && f.GC {
			for _, m := range f.Mobs {
				if !m.Combat || m.CC {
					continue
				}
				if _, boss := r.BossEntries[m.Entry]; boss {
					continue
				}
				victim := m.Victim
				if victim == "" {
					victim = m.Target
				}
				tn, ok := byGUID[victim]
				if !ok {
					tn, ok = byGUID[CanonGUID(victim)]
				}
				if !ok || !r.IsSubject(tn) {
					continue
				}
				switch r.Role(tn) {
				case "tank", "player", "?":
					continue
				}
				if !f.Units[tn].Alive {
					continue
				}
				k := key{m.G, tn}
				seen[k] = true
				if _, isOpen := cur[k]; !isOpen {
					open = append(open, k)
				}
				cur[k] = append(cur[k], step{f, m})
			}
		}
		for _, k := range append([]key(nil), open...) {
			if !seen[k] {
				closeKey(k)
			}
		}
	}
	closeAll()
	for _, e := range eps {
		f0, m0 := e.seq[0].f, e.seq[0].m
		var tanks []string
		for _, n := range f0.Order {
			if r.Role(n) == "tank" {
				tanks = append(tanks, n)
			}
		}
		dur := e.seq[len(e.seq)-1].f.T - f0.T
		ev := fmt.Sprintf("add %d (mob hp %.0f%%) hit %s %s for %.0f s; tanks %s; threat %s",
			m0.Entry, m0.HPPct, r.Class(e.k.bot), r.Role(e.k.bot), dur, strings.Join(firstN(tanks, 3), ","), threatText(m0))
		hits = append(hits, r.Hit(3, f0.T, dur, e.k.bot, ev,
			[]string{"3", r.Role(e.k.bot), fmt.Sprint(m0.Entry)}, r.sparseOpts()...))
	}
	return hits
}

// Rule5: stuck while the group moves, from the frames. Two findings, each with its own key:
//
//   - stuck follow: on `mvm=follow`, at least R5_STUCK_YD from the party leader, the bot's own path under R5_STILL_YD
//     over more than R5_STUCK_S (a navmesh pocket: the bot is not catching up, it cannot move).
//   - the module's own `stuck=blocked:<reason>` or `stuck=away:<seconds>`: the bot itself reports it cannot act.
//
// A meal (eating or drinking), a `stay`, and the bot's own combat break an episode, as in v6. A stay within
// R5_STAY_BACK_S of the episode's start holds it too: the gap opened on purpose while the lead pulled.
func Rule5(r *Run) []Hit {
	stuck := rule5StuckFollow(r)
	return append(stuck, rule5ModuleStuck(r)...)
}

// rule5StuckFollow is rule 5's own-movement shape.
func rule5StuckFollow(r *Run) []Hit {
	type step struct {
		f    *Frame
		x, y float64
		dl   float64
	}
	var (
		cur      = map[string][]step{}
		open     []string
		lastStay = map[string]float64{}
		hits     []Hit
		lastT    float64
		haveT    bool
	)
	closeBot := func(n string) {
		seq := cur[n]
		if len(seq) == 0 {
			return
		}
		delete(cur, n)
		for i, o := range open {
			if o == n {
				open = append(open[:i], open[i+1:]...)
				break
			}
		}
		dur := seq[len(seq)-1].f.T - seq[0].f.T
		path := 0.0
		far := 0.0
		for i := range seq {
			if i > 0 {
				path += math.Hypot(seq[i].x-seq[i-1].x, seq[i].y-seq[i-1].y)
			}
			if seq[i].dl > far {
				far = seq[i].dl
			}
		}
		if dur <= R5_STUCK_S || path > R5_STILL_YD {
			return
		}
		ev := fmt.Sprintf("stuck follow: still %.0f s on follow, %.0f yd from the leader", dur, far)
		hits = append(hits, r.Hit(5, seq[0].f.T, dur, n, ev, []string{"5", "stuck-follow"},
			append(r.sparseOpts(), Note("stuck follow"))...))
	}
	closeAll := func() {
		for len(open) > 0 {
			closeBot(open[0])
		}
	}
	qualifies := func(n string, u *Unit) bool {
		if !u.Alive || !lifeOK(u) {
			return false
		}
		if u.Combat || u.State == "c" {
			return false
		}
		if u.Eat || u.Drink {
			return false
		}
		if u.Move != "follow" {
			return false
		}
		return u.DL != nil && *u.DL >= R5_STUCK_YD
	}
	for _, f := range r.Frames {
		if haveT && f.T-lastT > MAX_GAP_S {
			closeAll()
		}
		lastT, haveT = f.T, true
		for _, n := range append([]string(nil), open...) {
			u, ok := f.Units[n]
			if !ok || !qualifies(n, u) {
				closeBot(n)
			}
		}
		for _, n := range f.Order {
			u := f.Units[n]
			if !r.IsSubject(n) {
				closeBot(n)
				continue
			}
			if u.Move == "stay" {
				lastStay[n] = f.T
			}
			if !qualifies(n, u) {
				closeBot(n)
				continue
			}
			if _, isOpen := cur[n]; !isOpen {
				if stay, ok := lastStay[n]; ok && f.T-stay < R5_STAY_BACK_S {
					continue
				}
				open = append(open, n)
			}
			cur[n] = append(cur[n], step{f, u.X, u.Y, *u.DL})
		}
	}
	closeAll()
	// One stuck pocket is one finding: a one-frame combat blip can split a long stand in two.
	if len(hits) > 1 {
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].T < hits[j].T })
		var merged []Hit
		for _, h := range hits {
			if n := len(merged); n > 0 {
				p := &merged[n-1]
				if p.Bot == h.Bot && h.T-(p.T+p.Dur) <= R5_STUCK_MERGE_S {
					if h.Dur > p.Dur {
						*p = h
					}
					continue
				}
			}
			merged = append(merged, h)
		}
		hits = merged
	}
	return hits
}

// rule5ModuleStuck is rule 5's own-report shape: the module's stuck word, its own key per kind and blocked reason.
func rule5ModuleStuck(r *Run) []Hit {
	type key struct{ bot, kind, reason string }
	type step struct {
		f    *Frame
		word string
	}
	type ep struct {
		k   key
		seq []step
	}
	var (
		cur   = map[key][]step{}
		open  []key
		eps   []ep
		lastT float64
		haveT bool
	)
	closeKey := func(k key) {
		seq := cur[k]
		if len(seq) == 0 {
			return
		}
		delete(cur, k)
		for i, o := range open {
			if o == k {
				open = append(open[:i], open[i+1:]...)
				break
			}
		}
		eps = append(eps, ep{k, seq})
	}
	closeAll := func() {
		for len(open) > 0 {
			closeKey(open[0])
		}
	}
	for _, f := range r.Frames {
		if haveT && f.T-lastT > MAX_GAP_S {
			closeAll()
		}
		lastT, haveT = f.T, true
		seen := map[key]bool{}
		for _, n := range f.Order {
			u := f.Units[n]
			if !r.IsSubject(n) || !u.Alive || !lifeOK(u) {
				continue
			}
			kind, reason := stuckKind(u.Stuck)
			if kind == "" {
				continue
			}
			k := key{n, kind, reason}
			seen[k] = true
			if _, isOpen := cur[k]; !isOpen {
				open = append(open, k)
			}
			cur[k] = append(cur[k], step{f, u.Stuck})
		}
		for _, k := range append([]key(nil), open...) {
			if !seen[k] {
				closeKey(k)
			}
		}
	}
	closeAll()
	var hits []Hit
	for _, e := range eps {
		dur := e.seq[len(e.seq)-1].f.T - e.seq[0].f.T
		key := []string{"5", e.k.kind}
		if e.k.reason != "" {
			key = append(key, e.k.reason)
		}
		ev := fmt.Sprintf("module reports stuck=%s for %.0f s", e.seq[0].word, dur)
		hits = append(hits, r.Hit(5, e.seq[0].f.T, dur, e.k.bot, ev, key, r.sparseOpts()...))
	}
	return hits
}

// stuckKind splits the module's stuck word into the two kinds rule 5 reports and the blocked reason. `noengine` is
// not one: that bot has no engine, which is a setup failure, not a bot that cannot move.
func stuckKind(word string) (kind, reason string) {
	switch {
	case strings.HasPrefix(word, "blocked:"):
		return "blocked", strings.TrimPrefix(word, "blocked:")
	case word == "blocked":
		return "blocked", ""
	case strings.HasPrefix(word, "away:"):
		return "away", ""
	}
	return "", ""
}

// Rule6: eating or drinking while the group is in combat (group in combat for at least R6_GROUP_COMBAT_MIN_S and an
// in-combat hostile within R6_NEAR_COMBAT_YD of the eater). A watch's frames carry the module's own eating and
// drinking flags; a trials repeat does not, so the rule is silent there, as the Python's is without the flags.
func Rule6(r *Run) []Hit {
	pred := func(f *Frame, n string, u *Unit) (string, bool) {
		if !u.Alive || !f.GC || !f.HasGCSince || f.T-f.GCSince < R6_GROUP_COMBAT_MIN_S {
			return "", false
		}
		meal := ""
		if u.Eat {
			meal = "eat"
		} else if u.Drink {
			meal = "drink"
		}
		if meal == "" {
			return "", false
		}
		near := false
		for _, m := range f.Mobs {
			if m.Combat && !m.NP && dist2(u.X, u.Y, m.X, m.Y) <= R6_NEAR_COMBAT_YD {
				near = true
				break
			}
		}
		if !near {
			return "", false
		}
		return meal, true
	}
	var hits []Hit
	for _, e := range r.Episodes(pred, -1, false) {
		f0 := e.Steps[0].Frame
		u0 := f0.Units[e.Bot]
		pct := -1.0
		if u0.PType == 0 {
			pct = pctOf(u0)
		}
		tag := "no-mana-bar"
		switch {
		case pct < 0:
		case pct < R6_LOW_MANA_PCT:
			tag = "mana-low"
		default:
			tag = "mana-ok"
		}
		dur := e.T1 - e.T0
		if dur < 1 {
			dur = 1
		}
		ev := fmt.Sprintf("%s while group in combat for %.0f s (own cb %s, mana %.0f%%, %d frames)",
			e.Steps[0].Ev, f0.T-f0.GCSince, pyBool(u0.Combat), pct, len(e.Steps))
		hits = append(hits, r.Hit(6, e.T0, dur, e.Bot, ev, []string{"6", r.Role(e.Bot), e.Steps[0].Ev, tag}, r.sparseOpts()...))
	}
	return hits
}

// Rule9 is the z-drop heuristic: a member that dies after falling R9_FALL_DROP_YD or more within R9_DROP_WINDOW_S
// before the death, with no damage source to name the killer. A death on flat ground is not a fall and is no hit.
func Rule9(r *Run) []Hit {
	var hits []Hit
	for _, d := range r.Deaths {
		if !r.IsSubject(d.Bot) {
			continue
		}
		var win []*Frame
		for _, f := range r.framesBetween(d.T-R9_DROP_WINDOW_S, d.T+R9_DROP_AFTER_S) {
			if _, ok := f.Units[d.Bot]; ok {
				win = append(win, f)
			}
		}
		if len(win) < R9_DROP_MIN_ROWS {
			continue
		}
		zs := make([]float64, len(win))
		for i, f := range win {
			zs[i] = f.Units[d.Bot].Z
		}
		hi := zs[0]
		for _, z := range zs[1:] {
			if z > hi {
				hi = z
			}
		}
		drop := hi - math.Min(zs[len(zs)-2], zs[len(zs)-1])
		if drop < R9_FALL_DROP_YD || win[0].Units[d.Bot].Z < zs[len(zs)-1] {
			continue
		}
		ev := fmt.Sprintf("z fell %.0f yd in %.1f s before death (heuristic, no damage-source row)",
			drop, win[len(win)-1].T-win[0].T)
		hits = append(hits, r.Hit(9, d.T, 0, d.Bot, ev, []string{"9", "z-drop"},
			append(r.sparseOpts(), Cost(0), Note("heuristic"))...))
	}
	return hits
}

// Rule10: a healer's mana (or a tank's rage) at R10_FULL_PCT or more for longer than R10_MIN_S in combat while a
// group member is under R10_DAMAGED_PCT health.
func Rule10(r *Run) []Hit {
	pred := func(f *Frame, n string, u *Unit) (string, bool) {
		if !u.Alive || !f.GC {
			return "", false
		}
		var kind string
		switch {
		case isHealer(r.Role(n)) && u.PType == 0:
			kind = "mana"
		case r.Role(n) == "tank" && u.PType == 1:
			kind = "rage"
		default:
			return "", false
		}
		if pctOf(u) < R10_FULL_PCT {
			return "", false
		}
		damaged := false
		for _, o := range f.Order {
			if o == n {
				continue
			}
			if v := f.Units[o]; v.Alive && hpPctOf(v) < R10_DAMAGED_PCT {
				damaged = true
				break
			}
		}
		if !damaged {
			return "", false
		}
		return kind, true
	}
	var hits []Hit
	for _, e := range r.Episodes(pred, R10_MIN_S, true) {
		kind := e.Steps[0].Ev
		dur := e.T1 - e.T0
		ev := fmt.Sprintf("%s at %.0f%%+ for %.0f s in combat with a damaged group (%d frames)",
			kind, R10_FULL_PCT, dur, len(e.Steps))
		hits = append(hits, r.Hit(10, e.T0, dur, e.Bot, ev, []string{"10", r.Role(e.Bot), kind}, r.sparseOpts()...))
	}
	return hits
}

// Rule13: a healer under R13_MANA_PCT out of combat for longer than R13_MIN_S, with no drink or food within
// R13_DRINK_PAD_S either side, while the group stands still. The rule needs the module's meal report to read "no
// drink" as a fact; a trials repeat does not carry one, so the run leaves that input unknown and the check is
// skipped there, as the Python's is.
func Rule13(r *Run) []Hit {
	if !r.ReportsMeals() {
		return nil
	}
	pred := func(f *Frame, n string, u *Unit) (string, bool) {
		if !isHealer(r.Role(n)) || !u.Alive || u.PType != 0 {
			return "", false
		}
		if f.GC || u.State == "c" || u.Combat {
			return "", false
		}
		if !u.HasMeal || u.Eat || u.Drink {
			return "", false
		}
		pct := pctOf(u)
		if pct >= R13_MANA_PCT {
			return "", false
		}
		return fmt.Sprintf("mana %.0f%%", pct), true
	}
	var hits []Hit
	for _, e := range r.Episodes(pred, R13_MIN_S, true) {
		drank, unknown := false, false
		for _, f := range r.framesBetween(e.T0-R13_DRINK_PAD_S, e.T1+R13_DRINK_PAD_S) {
			u, ok := f.Units[e.Bot]
			if !ok {
				continue
			}
			if !u.HasMeal {
				unknown = true // a meal in the pad cannot be ruled out
				break
			}
			if u.Eat || u.Drink {
				drank = true
				break
			}
		}
		if drank || unknown {
			continue
		}
		f0, f1 := e.Steps[0].Frame, e.Steps[len(e.Steps)-1].Frame
		var moves []float64
		// The Python walks the last frame's units and looks both endpoints up, so a member that joined mid-episode
		// still counts when it is alive at both ends.
		for _, o := range f1.Order {
			if o == e.Bot || !r.IsSubject(o) {
				continue
			}
			u0, ok0 := f0.Units[o]
			u1, ok1 := f1.Units[o]
			if ok0 && ok1 && u0.Alive && u1.Alive {
				moves = append(moves, math.Hypot(u1.X-u0.X, u1.Y-u0.Y))
			}
		}
		if len(moves) < R13_MIN_OTHERS {
			continue
		}
		sort.Float64s(moves)
		med := moves[len(moves)/2]
		if med >= R13_GROUP_STILL_YD {
			continue
		}
		dur := e.T1 - e.T0
		ev := fmt.Sprintf("healer %.0f%% -> %.0f%% mana out of combat for %.0f s, no drink/food within %.0f s, group moved %.1f yd (%d frames)",
			pctOf(e.Steps[0].Frame.Units[e.Bot]), pctOf(f1.Units[e.Bot]), dur, R13_DRINK_PAD_S, med, len(e.Steps))
		hits = append(hits, r.Hit(13, e.T0, dur, e.Bot, ev,
			[]string{"13", r.Class(e.Bot), "no-drink"}, r.sparseOpts()...))
	}
	return hits
}

// Rule14: a member more than R14_DIST_YD from the centroid of the other alive group members for longer than
// R14_MIN_S while the group is in combat, with at least R14_MIN_OTHERS others to judge the centroid from.
func Rule14(r *Run) []Hit {
	pred := func(f *Frame, n string, u *Unit) (string, bool) {
		if !u.Alive || !f.GC {
			return "", false
		}
		var (
			xs, ys []float64
		)
		for _, o := range f.Order {
			if o == n || !r.IsSubject(o) {
				continue
			}
			if v := f.Units[o]; v.Alive {
				xs = append(xs, v.X)
				ys = append(ys, v.Y)
			}
		}
		if len(xs) < R14_MIN_OTHERS {
			return "", false
		}
		var sx, sy float64
		for i := range xs {
			sx += xs[i]
			sy += ys[i]
		}
		cx, cy := sx/float64(len(xs)), sy/float64(len(ys))
		d := math.Hypot(u.X-cx, u.Y-cy)
		if d <= R14_DIST_YD {
			return "", false
		}
		return fmt.Sprintf("%.0f yd from %d others", d, len(xs)), true
	}
	var hits []Hit
	for _, e := range r.Episodes(pred, R14_MIN_S, true) {
		farMin, farMax := math.Inf(1), math.Inf(-1)
		for _, s := range e.Steps {
			var v float64
			if _, err := fmt.Sscanf(s.Ev, "%f", &v); err != nil {
				continue
			}
			farMin = math.Min(farMin, v)
			farMax = math.Max(farMax, v)
		}
		dur := e.T1 - e.T0
		ev := fmt.Sprintf("stray %.0f s, %.0f-%.0f yd from the group centroid in combat (%d frames)",
			dur, farMin, farMax, len(e.Steps))
		hits = append(hits, r.Hit(14, e.T0, dur, e.Bot, ev,
			[]string{"14", r.Role(e.Bot), r.Class(e.Bot)}, r.sparseOpts()...))
	}
	return hits
}

// firstN is the first n of a list (the tanks in rule 3's evidence).
func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

// threatText is the mob's threat top as rule 3's evidence carries it: the top entries, capped at the Python's 70
// characters.
func threatText(m *Mob) string {
	if len(m.Top) == 0 {
		if m.Target != "" {
			return m.Target
		}
		return "None"
	}
	parts := make([]string, 0, len(m.Top))
	for _, t := range m.Top {
		parts = append(parts, fmt.Sprintf("%s:%d", t.G, t.Threat))
	}
	s := strings.Join(parts, ",")
	if len(s) > 70 {
		s = s[:70]
	}
	return s
}
