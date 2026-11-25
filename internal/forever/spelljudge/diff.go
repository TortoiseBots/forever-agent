package spelljudge

// The before/after of two spell checks: what changed per class between two runs of the same fight.
// `forever spell-diff A B` judges both recordings with this package's own judge (`forever
// spell-score`'s reading) and prints this comparison, so a change to talents, gear or the module
// shows as the spells a class stopped and started pressing.
//
// A class's row sums its members' casts per spell name; a spell whose total changed is listed with
// the two numbers, and one side's absence is "added" or "removed". A member whose verdict (level or
// text) changed is named too, because a count can stay the same while the oracle's reading of it
// moves (a spec inferred differently, a core spell that fell under the active-time floor).

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// DiffFormat is the comparison's own format word in --json output.
const DiffFormat = "forever-spell-diff/1"

// SpellDiff is one spell's before and after cast counts for a class.
type SpellDiff struct {
	Spell  string `json:"spell"`
	Before int    `json:"before"`
	After  int    `json:"after"`
}

// MemberDiff is one member whose verdict changed.
type MemberDiff struct {
	Bot    string `json:"bot"`
	Class  string `json:"class"`
	Spec   string `json:"spec"`
	Before string `json:"before"`
	After  string `json:"after"`
	Text   string `json:"text,omitempty"`
}

// ClassDiff is one class's changes: its cast totals, every spell whose count moved and the members
// whose verdict moved.
type ClassDiff struct {
	Class       string       `json:"class"`
	CastsBefore int          `json:"casts_before"`
	CastsAfter  int          `json:"casts_after"`
	Spells      []SpellDiff  `json:"spells,omitempty"`
	Members     []MemberDiff `json:"members,omitempty"`
}

// Changed reports whether anything in the class's row moved.
func (c ClassDiff) Changed() bool {
	return len(c.Spells) > 0 || len(c.Members) > 0
}

// DiffReport is one comparison: the two runs' names and every class that changed, class order.
type DiffReport struct {
	Format  string      `json:"format"`
	Before  string      `json:"before,omitempty"`
	After   string      `json:"after,omitempty"`
	Classes []ClassDiff `json:"classes"`
}

// Diff compares two judgments. before and after are each a run's findings (Judge's answer).
func Diff(before, after []Finding, beforeName, afterName string) DiffReport {
	report := DiffReport{Format: DiffFormat, Before: beforeName, After: afterName}
	castsOf := func(findings []Finding) map[string]map[string]int {
		out := map[string]map[string]int{}
		for _, f := range findings {
			class := classOf(f)
			if out[class] == nil {
				out[class] = map[string]int{}
			}
			for spell, n := range f.Spells {
				out[class][spell] += n
			}
		}
		return out
	}
	totalsOf := func(findings []Finding) map[string]int {
		out := map[string]int{}
		for _, f := range findings {
			out[classOf(f)] += f.Casts
		}
		return out
	}
	beforeCasts, afterCasts := castsOf(before), castsOf(after)
	beforeTotals, afterTotals := totalsOf(before), totalsOf(after)
	classes := map[string]bool{}
	for class := range beforeCasts {
		classes[class] = true
	}
	for class := range afterCasts {
		classes[class] = true
	}
	for _, f := range before {
		classes[classOf(f)] = true
	}
	for _, f := range after {
		classes[classOf(f)] = true
	}
	var names []string
	for class := range classes {
		names = append(names, class)
	}
	sort.Strings(names)
	members := memberDiffs(before, after)
	for _, class := range names {
		cd := ClassDiff{Class: class, CastsBefore: beforeTotals[class], CastsAfter: afterTotals[class]}
		spells := map[string]bool{}
		for spell := range beforeCasts[class] {
			spells[spell] = true
		}
		for spell := range afterCasts[class] {
			spells[spell] = true
		}
		for spell := range spells {
			b, a := beforeCasts[class][spell], afterCasts[class][spell]
			if b == a {
				continue
			}
			cd.Spells = append(cd.Spells, SpellDiff{Spell: spell, Before: b, After: a})
		}
		sort.Slice(cd.Spells, func(i, j int) bool { return cd.Spells[i].Spell < cd.Spells[j].Spell })
		for _, md := range members {
			if classOfClass(md.Class) == class {
				cd.Members = append(cd.Members, md)
			}
		}
		report.Classes = append(report.Classes, cd)
	}
	return report
}

// classOfClass is a class word as the report groups it, "?" for a member the run has none for.
func classOfClass(class string) string {
	if class == "" {
		return "?"
	}
	return class
}

// classOf is a finding's class, "?" for a member the run has none for.
func classOf(f Finding) string {
	if f.Class == "" {
		return "?"
	}
	return f.Class
}

// memberDiffs pairs the two runs' members by name, position by position, over both runs at once: a
// member whose verdict (level or text) moved is reported with the class it is judged in. Pairing is
// global, so a member that changes class between the runs is still the same member, and a name that
// appears more than once on one side pairs in the run's own order (the first with the first), never
// collapsed into one.
func memberDiffs(before, after []Finding) []MemberDiff {
	byName := func(findings []Finding) map[string][]Finding {
		out := map[string][]Finding{}
		for _, f := range findings {
			out[f.Bot] = append(out[f.Bot], f)
		}
		return out
	}
	beforeBy, afterBy := byName(before), byName(after)
	names := map[string]bool{}
	for name := range beforeBy {
		names[name] = true
	}
	for name := range afterBy {
		names[name] = true
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	var out []MemberDiff
	for _, name := range sorted {
		b, a := beforeBy[name], afterBy[name]
		n := max(len(b), len(a))
		for i := range n {
			var bf, af *Finding
			if i < len(b) {
				bf = &b[i]
			}
			if i < len(a) {
				af = &a[i]
			}
			if bf != nil && af != nil && bf.Level == af.Level && bf.Text == af.Text {
				continue
			}
			md := MemberDiff{Bot: name}
			switch {
			case af != nil:
				md.Class, md.Spec, md.After, md.Text = af.Class, af.Spec, af.Level, af.Text
			case bf != nil:
				md.Class, md.Spec, md.Text = bf.Class, bf.Spec, bf.Text
			}
			if bf != nil {
				md.Before = bf.Level
			} else {
				md.Before = "absent"
			}
			if af == nil {
				md.After = "absent"
			}
			out = append(out, md)
		}
	}
	return out
}

// WriteText prints the comparison: one block per class that changed, "no change" when none did.
func (r DiffReport) WriteText(w io.Writer) error {
	fmt.Fprintf(w, "before: %s\nafter: %s\n", orNone(r.Before), orNone(r.After))
	changed := 0
	for _, c := range r.Classes {
		if !c.Changed() && c.CastsBefore == c.CastsAfter {
			continue
		}
		changed++
		fmt.Fprintf(w, "\n%s: %d cast(s) -> %d cast(s)\n", c.Class, c.CastsBefore, c.CastsAfter)
		for _, s := range c.Spells {
			switch {
			case s.Before == 0:
				fmt.Fprintf(w, "  %s: added (%d)\n", s.Spell, s.After)
			case s.After == 0:
				fmt.Fprintf(w, "  %s: removed (%d)\n", s.Spell, s.Before)
			default:
				fmt.Fprintf(w, "  %s: %d -> %d (%+d)\n", s.Spell, s.Before, s.After, s.After-s.Before)
			}
		}
		for _, m := range c.Members {
			fmt.Fprintf(w, "  %s (%s): %s -> %s\n", m.Bot, m.Spec, m.Before, m.After)
			if m.Text != "" {
				fmt.Fprintf(w, "    %s\n", m.Text)
			}
		}
	}
	if changed == 0 {
		fmt.Fprintln(w, "\nno change")
	}
	return nil
}

// orNone is s, or "-" when it is empty.
func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
