package bughunt

import "fmt"

// Rule7: a pet whose owner is a member of the frame, in combat with its target set to a mob, while its owner is alive
// and not in combat (or the owner's target is empty) for longer than rule7MinS seconds of consecutive frames. A pet
// with an unknown owner is never judged. One hit per owner episode, keyed (7, owner class).
func Rule7(r *Run) []Hit {
	pred := func(f *Frame, n string, u *Unit) (string, bool) {
		if !u.Alive || !lifeOK(u) || len(f.Pets) == 0 {
			return "", false
		}
		if u.Combat && u.Target != "" {
			return "", false
		}
		for _, p := range f.Pets {
			if p.Owner != n || !p.Combat || p.Target == "" {
				continue
			}
			isMob := false
			for _, m := range f.Mobs {
				if m.G == p.Target {
					isMob = true
					break
				}
			}
			if !isMob {
				continue
			}
			return fmt.Sprintf("pet %s fights %s", p.G, p.Target), true
		}
		return "", false
	}
	var hits []Hit
	for _, e := range r.Episodes(pred, rule7MinS, true) {
		dur := e.T1 - e.T0
		f0 := e.Steps[0].Frame
		u0 := f0.Units[e.Bot]
		ev := fmt.Sprintf("%s while owner %s is not fighting (own cb %s) for %.0f s (%d frames)",
			e.Steps[0].Ev, e.Bot, pyBool(u0.Combat), dur, len(e.Steps))
		hits = append(hits, r.Hit(7, e.T0, dur, e.Bot, ev, []string{"7", r.Class(e.Bot)}, r.sparseOpts()...))
	}
	return hits
}
