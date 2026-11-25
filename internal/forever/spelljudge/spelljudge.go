// Package spelljudge judges a Forever (Classic 1.60) class fight from the watch's combat log: it counts each party
// member's casts exactly (one spell_go row per cast), judges them against the shared kbspells oracle with the Forever
// spell table as the data source, and fails the one data-true fault a rotation opinion cannot explain: a cast of
// another class's class ability.
package spelljudge

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/bughunt"
	"github.com/pjerra/forever-agent/internal/foreverspells"
	"github.com/pjerra/forever-agent/internal/kbspells"
)

// activeGapCapS is the longest frame gap counted as active time: a gap over it is not evidence of a fight (D13).
const activeGapCapS = 3.0

// Finding is one member's judgment: the oracle's finding (class, spec, missing core, off-list) plus the Forever
// readings the oracle cannot see.
type Finding struct {
	kbspells.Finding
	WrongClass []string `json:"wrong_class,omitempty"` // "Sinister Strike (rogue)"
	UnknownIDs []uint32 `json:"unknown_ids,omitempty"` // casts named by neither the row nor the spell table
	Casts      int      `json:"casts"`
	// Spells is the member's own cast count per spell name (the same counting Spells reads): what a
	// before/after of two runs compares, spell by spell.
	Spells map[string]int `json:"spells,omitempty"`
}

// Options are the judge's readings: how much active time a member needs before its core list is judged, and whether
// an unknown cast is a failure (a spell the server's own name and the table both lack is usually a custom or an
// item, so it only fails on request).
type Options struct {
	MinActiveS  float64
	FailUnknown bool
}

// Judge judges every party member of the run, in the party's own order. A member the run has no class for is not
// judged (na): its wrong-class reading has nothing to compare against.
func Judge(r *bughunt.Run, o *kbspells.Oracle, t *foreverspells.Table, opt Options) []Finding {
	var out []Finding
	for _, m := range r.PartyOrder {
		out = append(out, judgeMember(r, o, t, m, opt))
	}
	return out
}

// Failed reports whether any finding failed: a cast of another class's ability, or an unknown cast under
// FailUnknown.
func Failed(fs []Finding) bool {
	for _, f := range fs {
		if f.Level == "fail" {
			return true
		}
	}
	return false
}

// judgeMember counts one member's casts and judges them.
func judgeMember(r *bughunt.Run, o *kbspells.Oracle, t *foreverspells.Table, m string, opt Options) Finding {
	class := r.Class(m)
	casts := map[string]int{}
	unknown := map[uint32]bool{}
	wrong := map[string]bool{}
	n := 0
	for i := range r.Combat {
		row := &r.Combat[i]
		if row.Kind != "spell_go" || r.Member(row.Source.GUID) != m {
			continue
		}
		n++
		id := uint32(row.SpellID)
		name := strings.TrimSpace(row.SpellName)
		if name == "" && t != nil {
			name = t.Name(id)
		}
		if name == "" {
			unknown[id] = true
			continue
		}
		casts[name]++
		if t == nil || class == "?" {
			continue
		}
		if c := t.ClassOf(id); c != "" && !strings.EqualFold(c, class) {
			wrong[fmt.Sprintf("%s (%s)", name, c)] = true
		}
	}
	f := Finding{Casts: n}
	if len(casts) > 0 {
		f.Spells = casts
	}
	for id := range unknown {
		f.UnknownIDs = append(f.UnknownIDs, id)
	}
	sort.Slice(f.UnknownIDs, func(i, j int) bool { return f.UnknownIDs[i] < f.UnknownIDs[j] })
	if class == "?" {
		f.Finding = kbspells.Finding{Bot: m, Class: "?", Role: r.Role(m), Spec: "unknown", Level: "na", Text: "no class in the trace"}
		return f
	}
	f.Finding = o.Judge(kbspells.BotCasts{
		Name: m, Class: class, Role: r.Role(m), Level: r.Levels[m], Casts: casts, ActiveS: activeS(r, m),
	}, opt.MinActiveS)
	for w := range wrong {
		f.WrongClass = append(f.WrongClass, w)
	}
	sort.Strings(f.WrongClass)
	if len(f.WrongClass) > 0 {
		f.Level = "fail"
		f.Text += "; wrong class: " + strings.Join(f.WrongClass, ", ")
	}
	if opt.FailUnknown && len(f.UnknownIDs) > 0 {
		f.Level = "fail"
		var ids []string
		for _, id := range f.UnknownIDs {
			ids = append(ids, strconv.FormatUint(uint64(id), 10))
		}
		f.Text += "; unknown spell ids: " + strings.Join(ids, ", ")
	}
	return f
}

// activeS is a member's active time (D13): the sum of the gaps between consecutive frames in which it is alive and
// in combat in both, each gap capped at activeGapCapS.
func activeS(r *bughunt.Run, m string) float64 {
	var s float64
	in := func(f *bughunt.Frame) bool {
		u, ok := f.Units[m]
		return ok && u.Alive && u.Combat
	}
	for i := 1; i < len(r.Frames); i++ {
		prev, cur := r.Frames[i-1], r.Frames[i]
		if !in(prev) || !in(cur) {
			continue
		}
		gap := cur.T - prev.T
		if gap > activeGapCapS {
			gap = activeGapCapS
		}
		if gap > 0 {
			s += gap
		}
	}
	return s
}
