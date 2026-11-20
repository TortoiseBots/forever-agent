package reportcard

// Compare is the offline diff of two cards (or two run records, built into cards first): what got better or worse
// per leg and per flag. It is deliberately numeric and keyed, so a later run's card can be read against an earlier
// one without opening either log.

import (
	"fmt"
	"sort"
	"strings"
)

// CompareRow is one difference. Kind is "verdict", "leg" or "flag"; Better and Worse are false when the row only
// changed shape (a flag's text changed, or a leg appeared with no earlier number to compare).
type CompareRow struct {
	Kind   string
	Key    string
	Text   string
	Better bool
	Worse  bool
}

// verdictRank orders the verdicts: CLEARED is best, FAILED worst.
func verdictRank(word string) int {
	switch word {
	case "CLEARED":
		return 0
	case "STOPPED":
		return 1
	}
	return 2
}

// Compare matches legs by number and flags by (ID, Key).
func Compare(a, b Card) []CompareRow {
	var rows []CompareRow
	if a.VerdictText != b.VerdictText {
		row := CompareRow{Kind: "verdict", Key: "verdict",
			Text: fmt.Sprintf("verdict %s -> %s", a.VerdictText, b.VerdictText)}
		ra, rb := verdictRank(a.Verdict), verdictRank(b.Verdict)
		row.Better, row.Worse = rb < ra, rb > ra
		rows = append(rows, row)
	}

	aLegs := map[int]Leg{}
	for _, l := range a.Legs {
		aLegs[l.Leg] = l
	}
	bLegs := map[int]Leg{}
	for _, l := range b.Legs {
		bLegs[l.Leg] = l
	}
	nums := map[int]bool{}
	for n := range aLegs {
		nums[n] = true
	}
	for n := range bLegs {
		nums[n] = true
	}
	sorted := make([]int, 0, len(nums))
	for n := range nums {
		sorted = append(sorted, n)
	}
	sort.Ints(sorted)
	for _, n := range sorted {
		la, oka := aLegs[n]
		lb, okb := bLegs[n]
		if !oka || !okb {
			row := CompareRow{Kind: "leg", Key: fmt.Sprintf("leg:%d", n)}
			switch {
			case !oka:
				row.Text = fmt.Sprintf("leg %d %q: added", n, lb.Name)
				row.Worse = true
			default:
				row.Text = fmt.Sprintf("leg %d %q: gone", n, la.Name)
				row.Better = true
			}
			rows = append(rows, row)
			continue
		}
		for _, m := range []struct {
			name string
			a, b int
		}{
			{"seconds", la.Seconds, lb.Seconds},
			{"pulls", la.Pulls, lb.Pulls},
			{"re-pulls", la.Repulls, lb.Repulls},
			{"deaths", la.Deaths, lb.Deaths},
			{"wipes", la.Wipes, lb.Wipes},
		} {
			if m.a == m.b {
				continue
			}
			row := CompareRow{Kind: "leg", Key: fmt.Sprintf("leg:%d/%s", n, m.name),
				Text: fmt.Sprintf("leg %d %q %s: %d -> %d (%+d)", n, lb.Name, m.name, m.a, m.b, m.b-m.a)}
			row.Better, row.Worse = m.b < m.a, m.b > m.a
			rows = append(rows, row)
		}
	}

	flags := func(c Card) map[string]Flag {
		out := map[string]Flag{}
		for _, f := range c.Flags {
			out[f.ID+"\x00"+f.Key] = f
		}
		return out
	}
	fa, fb := flags(a), flags(b)
	keys := map[string]bool{}
	for k := range fa {
		keys[k] = true
	}
	for k := range fb {
		keys[k] = true
	}
	sortedKeys := make([]string, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Slice(sortedKeys, func(i, j int) bool {
		ia, ja := splitFlagKey(sortedKeys[i])
		ib, jb := splitFlagKey(sortedKeys[j])
		if ia != ib {
			return ia < ib
		}
		return ja < jb
	})
	for _, k := range sortedKeys {
		old, hadOld := fa[k]
		now, hasNow := fb[k]
		id, key := splitFlagKey(k)
		ref := id
		if key != "" {
			ref += " " + key
		}
		switch {
		case !hadOld && hasNow:
			rows = append(rows, CompareRow{Kind: "flag", Key: k, Worse: true,
				Text: fmt.Sprintf("+ %s: %s", ref, now.Text)})
		case hadOld && !hasNow:
			rows = append(rows, CompareRow{Kind: "flag", Key: k, Better: true,
				Text: fmt.Sprintf("- %s: %s", ref, old.Text)})
		case old.Text != now.Text:
			rows = append(rows, CompareRow{Kind: "flag", Key: k,
				Text: fmt.Sprintf("~ %s: %s -> %s", ref, old.Text, now.Text)})
		}
	}
	return rows
}

// splitFlagKey splits the flag map's composite key.
func splitFlagKey(k string) (id, key string) {
	if i := strings.IndexByte(k, 0); i >= 0 {
		return k[:i], k[i+1:]
	}
	return k, ""
}

// RenderCompare prints the rows with a short header and the counts of better and worse.
func RenderCompare(a, b Card, rows []CompareRow) string {
	var out strings.Builder
	fmt.Fprintf(&out, "compare: run %s -> run %s\n", orNone(a.RunID), orNone(b.RunID))
	if a.VerdictText == b.VerdictText {
		fmt.Fprintf(&out, "verdict: %s (same)\n", a.VerdictText)
	}
	better, worse := 0, 0
	for _, r := range rows {
		if r.Better {
			better++
		}
		if r.Worse {
			worse++
		}
		fmt.Fprintf(&out, "  %s\n", r.Text)
	}
	fmt.Fprintf(&out, "rows: %d better, %d worse, %d changed\n", better, worse, len(rows)-better-worse)
	return out.String()
}
