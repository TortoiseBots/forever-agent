package realism

// The report: one snapshot judged, per bot and per spec, and — with a baseline — the before/after
// difference per check. `trials realism --snapshot FILE [--baseline FILE]` prints the text form and
// `--json` prints the machine-readable one, for posting.
//
// The per-spec line sums its bots per check with the three verdict counts and the mean of the
// numbers the bots' judged parts gave, so a spec's row says both "how many passed" and "how far from
// the players' own numbers". The before/after compares the two readings' counts and means per check
// (a spec that went from FAIL to PASS is `better`), and names every bot whose verdict changed.

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/pjerra/forever-agent/data/aotc"
)

// The formats the report writes.
const (
	// ReadingFormat is the JSON report's own format word.
	ReadingFormat = "forever-realism/1"
	// DiffFormat is the before/after report's own format word.
	DiffFormat = "forever-realism-diff/1"
)

// checkIDs is the report's column order.
var checkIDs = []string{RuleTalents, RuleGear, RuleStats, RuleEnchants}

// BotReading is one bot's checks.
type BotReading struct {
	ID     string   `json:"id"`
	Class  string   `json:"class"`
	Spec   string   `json:"spec"`
	Level  int      `json:"level"`
	Checks []Check  `json:"checks"`
	Notes  []string `json:"notes,omitempty"`
}

// check returns one check's reading for the bot.
func (b BotReading) check(id string) Check {
	for _, c := range b.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{ID: id, Verdict: VerdictInconclusive, Reason: "not read"}
}

// CheckCount is one check's verdict counts over a set of bots.
type CheckCount struct {
	ID           string  `json:"id"`
	Pass         int     `json:"pass"`
	Fail         int     `json:"fail"`
	Inconclusive int     `json:"inconclusive"`
	Judged       int     `json:"judged"`
	Mean         float64 `json:"mean,omitempty"`
}

// SpecSummary is one class and spec's row: its bots and the counts per check.
type SpecSummary struct {
	Class  string       `json:"class"`
	Spec   string       `json:"spec"`
	Bots   int          `json:"bots"`
	IDs    []string     `json:"bot_ids"`
	Checks []CheckCount `json:"checks"`
}

// Reading is a whole snapshot's verdicts: every bot, the per-spec rows and the per-check counts.
type Reading struct {
	Format string        `json:"format"`
	Source string        `json:"source,omitempty"`
	Taken  string        `json:"taken,omitempty"`
	Bots   []BotReading  `json:"bots"`
	Specs  []SpecSummary `json:"specs"`
	Checks []CheckCount  `json:"checks"`
	Credit string        `json:"credit"`
}

// Fails is the number of FAIL verdicts in the reading (a bot counts once per failed check).
func (r Reading) Fails() int {
	n := 0
	for _, c := range r.Checks {
		n += c.Fail
	}
	return n
}

// Judged is the number of bots the reading holds.
func (r Reading) Judged() int { return len(r.Bots) }

// JudgedChecks is the number of bot checks that reached a verdict (PASS or FAIL): the reading's own
// coverage. A reading with none assessed nothing, and a caller must not read that as a pass.
func (r Reading) JudgedChecks() int {
	n := 0
	for _, b := range r.Bots {
		for _, c := range b.Checks {
			if c.Verdict != VerdictInconclusive {
				n++
			}
		}
	}
	return n
}

// InconclusiveChecks is the number of bot checks that could not be judged.
func (r Reading) InconclusiveChecks() int {
	n := 0
	for _, b := range r.Bots {
		for _, c := range b.Checks {
			if c.Verdict == VerdictInconclusive {
				n++
			}
		}
	}
	return n
}

// Judge reads every bot of a snapshot against the tables.
func JudgeReading(s Snapshot, t Tables) Reading {
	r := Reading{Format: ReadingFormat, Source: s.Source, Taken: s.Taken, Credit: aotc.Credit}
	bots := map[string]BotReading{}
	for _, bot := range s.Bots {
		br := BotReading{ID: bot.ID, Class: bot.Class, Spec: bot.Spec, Level: bot.Level,
			Checks: Judge(bot, t), Notes: append([]string(nil), bot.Notes...)}
		r.Bots = append(r.Bots, br)
		bots[bot.ID] = br
	}
	for _, group := range SpecBots(s) {
		sum := SpecSummary{Class: group.Class, Spec: group.Spec, Bots: len(group.Bots), IDs: append([]string(nil), group.Bots...)}
		for _, id := range checkIDs {
			count := CheckCount{ID: id}
			var values []float64
			for _, botID := range group.Bots {
				c := bots[botID].check(id)
				switch c.Verdict {
				case VerdictPass:
					count.Pass++
				case VerdictFail:
					count.Fail++
				default:
					count.Inconclusive++
				}
				if c.Verdict != VerdictInconclusive && c.Measure != "" {
					values = append(values, c.Value)
				}
			}
			count.Judged = len(values)
			count.Mean = meanFloats(values)
			sum.Checks = append(sum.Checks, count)
		}
		r.Specs = append(r.Specs, sum)
	}
	for _, id := range checkIDs {
		count := CheckCount{ID: id}
		for _, b := range r.Bots {
			c := b.check(id)
			switch c.Verdict {
			case VerdictPass:
				count.Pass++
			case VerdictFail:
				count.Fail++
			default:
				count.Inconclusive++
			}
		}
		r.Checks = append(r.Checks, count)
	}
	return r
}

// WriteText prints the reading as the report's text form.
func (r Reading) WriteText(w io.Writer) error {
	fmt.Fprintf(w, "snapshot: %s\n", orDefault(r.Source, "unnamed"))
	if r.Taken != "" {
		fmt.Fprintf(w, "taken: %s\n", r.Taken)
	}
	fmt.Fprintf(w, "bots: %d\n", len(r.Bots))
	fmt.Fprintf(w, "coverage: %d check(s) judged, %d INCONCLUSIVE (of %d)\n",
		r.JudgedChecks(), r.InconclusiveChecks(), len(r.Bots)*len(checkIDs))
	fmt.Fprintf(w, "credit: %s\n", r.Credit)

	fmt.Fprintln(w, "\n## Checks")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "check\tpass\tfail\tinconclusive")
	for _, c := range r.Checks {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\n", c.ID, c.Pass, c.Fail, c.Inconclusive)
	}
	tw.Flush()

	fmt.Fprintln(w, "\n## Per spec")
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "class/spec\tbots")
	for _, id := range checkIDs {
		fmt.Fprintf(tw, "\t%s", id)
	}
	fmt.Fprintln(tw)
	for _, s := range r.Specs {
		fmt.Fprintf(tw, "%s\t%d", specName(s.Class, s.Spec), s.Bots)
		for _, c := range s.Checks {
			fmt.Fprintf(tw, "\t%s", countCell(c))
		}
		fmt.Fprintln(tw)
	}
	tw.Flush()

	fmt.Fprintln(w, "\n## Per bot")
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "bot\tclass/spec\tlevel")
	for _, id := range checkIDs {
		fmt.Fprintf(tw, "\t%s", id)
	}
	fmt.Fprintln(tw)
	for _, b := range r.Bots {
		fmt.Fprintf(tw, "%s\t%s\t%d", b.ID, specName(b.Class, b.Spec), b.Level)
		for _, id := range checkIDs {
			fmt.Fprintf(tw, "\t%s", b.check(id).Verdict)
		}
		fmt.Fprintln(tw)
	}
	tw.Flush()

	for _, b := range r.Bots {
		fmt.Fprintf(w, "\n%s: %s", b.ID, specName(b.Class, b.Spec))
		if b.Level > 0 {
			fmt.Fprintf(w, " level %d", b.Level)
		}
		fmt.Fprintln(w)
		for _, id := range checkIDs {
			c := b.check(id)
			fmt.Fprintf(w, "  %-18s %s", c.ID, c.Verdict)
			if c.Measure != "" {
				fmt.Fprintf(w, ": %s", c.Measure)
			} else if c.Reason != "" {
				fmt.Fprintf(w, ": %s", c.Reason)
			}
			fmt.Fprintln(w)
			for _, p := range c.Parts {
				if p.Verdict == VerdictFail {
					for _, line := range p.Lines {
						fmt.Fprintf(w, "    %s\n", line)
					}
				}
			}
			if c.Verdict == VerdictFail && c.Reason != "" {
				fmt.Fprintf(w, "    reason: %s\n", c.Reason)
			}
		}
		for _, note := range b.Notes {
			fmt.Fprintf(w, "  note: %s\n", note)
		}
	}
	return nil
}

// countCell is one spec table cell: the three verdict counts and the mean of the judged numbers.
func countCell(c CheckCount) string {
	cell := fmt.Sprintf("%d/%d/%d", c.Pass, c.Fail, c.Inconclusive)
	if c.Judged > 0 {
		cell += fmt.Sprintf(" %s %.2f", meanWord(c.ID), c.Mean)
	}
	return cell
}

// meanWord names the number a check's mean is: what the cell's number measures.
func meanWord(id string) string {
	switch id {
	case RuleTalents:
		return "pts"
	case RuleGear:
		return "above"
	case RuleStats:
		return "fit"
	default:
		return "cov"
	}
}

// specName is a class and spec for a table cell.
func specName(class, spec string) string {
	if spec == "" {
		return class + "/?"
	}
	return class + "/" + spec
}

// orDefault is s, or word when s is empty.
func orDefault(s, word string) string {
	if s == "" {
		return word
	}
	return s
}

// CheckDiff is one check's before/after counts.
type CheckDiff struct {
	ID     string     `json:"id"`
	Before CheckCount `json:"before"`
	After  CheckCount `json:"after"`
	Change string     `json:"change"`
}

// SpecDiff is one class and spec's before/after counts per check.
type SpecDiff struct {
	Class  string      `json:"class"`
	Spec   string      `json:"spec"`
	Bots   int         `json:"bots"`
	Checks []CheckDiff `json:"checks"`
}

// BotDiff is one bot's before/after verdict per check.
type BotDiff struct {
	ID     string          `json:"id"`
	Class  string          `json:"class"`
	Spec   string          `json:"spec"`
	Level  int             `json:"level"`
	Checks []VerdictChange `json:"checks"`
}

// VerdictChange is one check's verdict before and after for one bot.
type VerdictChange struct {
	ID     string `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
	Change string `json:"change"`
}

// Diff is the before/after report: the per-check counts, the per-spec counts and every bot whose
// verdict changed.
type Diff struct {
	Format string      `json:"format"`
	Before string      `json:"before,omitempty"`
	After  string      `json:"after,omitempty"`
	Checks []CheckDiff `json:"checks"`
	Specs  []SpecDiff  `json:"specs"`
	Bots   []BotDiff   `json:"bots"`
}

// Compare diffs two readings: after is the newer snapshot.
func Compare(before, after Reading) Diff {
	d := Diff{Format: DiffFormat, Before: before.Source, After: after.Source}
	afterBots := map[string]BotReading{}
	for _, b := range after.Bots {
		afterBots[b.ID] = b
	}
	for _, id := range checkIDs {
		b, a := countOf(before.Checks, id), countOf(after.Checks, id)
		d.Checks = append(d.Checks, CheckDiff{ID: id, Before: b, After: a, Change: changeWord(b, a)})
	}
	afterSpecs := map[string]SpecSummary{}
	for _, s := range after.Specs {
		afterSpecs[s.Class+"\x00"+s.Spec] = s
	}
	for _, s := range before.Specs {
		sd := SpecDiff{Class: s.Class, Spec: s.Spec, Bots: s.Bots}
		a, ok := afterSpecs[s.Class+"\x00"+s.Spec]
		if ok {
			sd.Bots = a.Bots
		}
		for _, id := range checkIDs {
			b := countOf(s.Checks, id)
			var ac CheckCount
			if ok {
				ac = countOf(a.Checks, id)
			}
			sd.Checks = append(sd.Checks, CheckDiff{ID: id, Before: b, After: ac, Change: changeWord(b, ac)})
		}
		d.Specs = append(d.Specs, sd)
	}
	for _, b := range before.Bots {
		a, ok := afterBots[b.ID]
		if !ok {
			continue
		}
		bd := BotDiff{ID: b.ID, Class: b.Class, Spec: b.Spec, Level: b.Level}
		for _, id := range checkIDs {
			bc, ac := b.check(id), a.check(id)
			change := "same"
			if bc.Verdict != ac.Verdict {
				change = verdictChange(bc.Verdict, ac.Verdict)
			}
			bd.Checks = append(bd.Checks, VerdictChange{ID: id, Before: bc.Verdict, After: ac.Verdict, Change: change})
		}
		d.Bots = append(d.Bots, bd)
	}
	return d
}

// countOf returns one check's count, an empty count for a check the reading does not carry.
func countOf(counts []CheckCount, id string) CheckCount {
	for _, c := range counts {
		if c.ID == id {
			return c
		}
	}
	return CheckCount{ID: id}
}

// changeWord says whether the counts improved: fewer FAILs is better, more is worse; with the FAIL
// counts equal, more PASSes is better.
func changeWord(before, after CheckCount) string {
	switch {
	case after.Fail < before.Fail:
		return "better"
	case after.Fail > before.Fail:
		return "worse"
	case after.Pass > before.Pass:
		return "better"
	case after.Pass < before.Pass:
		return "worse"
	}
	return "same"
}

// verdictChange says whether one bot's verdict moved for the better.
func verdictChange(before, after string) string {
	switch {
	case before == VerdictFail && after == VerdictPass:
		return "better"
	case before == VerdictInconclusive && after == VerdictPass:
		return "better"
	case before == VerdictFail && after == VerdictInconclusive:
		return "better"
	case after == VerdictFail:
		return "worse"
	case before == VerdictPass && after == VerdictInconclusive:
		return "worse"
	}
	return "other"
}

// WriteText prints the before/after as a table.
func (d Diff) WriteText(w io.Writer) error {
	fmt.Fprintf(w, "before: %s\nafter: %s\n", orDefault(d.Before, "unnamed"), orDefault(d.After, "unnamed"))
	fmt.Fprintln(w, "\n## Checks")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "check\tbefore\tafter\tchange")
	for _, c := range d.Checks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s (%s)\n", c.ID, diffCell(c.Before), diffCell(c.After), c.Change, impact(c.After, c.Before))
	}
	tw.Flush()

	fmt.Fprintln(w, "\n## Per spec")
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "class/spec")
	for _, id := range checkIDs {
		fmt.Fprintf(tw, "\t%s", id)
	}
	fmt.Fprintln(tw)
	for _, s := range d.Specs {
		fmt.Fprintf(tw, "%s", specName(s.Class, s.Spec))
		for _, c := range s.Checks {
			fmt.Fprintf(tw, "\t%s -> %s %s", countCell(c.Before), countCell(c.After), c.Change)
		}
		fmt.Fprintln(tw)
	}
	tw.Flush()

	var changed []BotDiff
	for _, b := range d.Bots {
		for _, c := range b.Checks {
			if c.Change != "same" {
				changed = append(changed, b)
				break
			}
		}
	}
	if len(changed) > 0 {
		fmt.Fprintln(w, "\n## Bots that changed")
		for _, b := range changed {
			fmt.Fprintf(w, "%s (%s, level %d)\n", b.ID, specName(b.Class, b.Spec), b.Level)
			for _, c := range b.Checks {
				if c.Change == "same" {
					continue
				}
				fmt.Fprintf(w, "  %-18s %s -> %s (%s)\n", c.ID, c.Before, c.After, c.Change)
			}
		}
	}
	return nil
}

// diffCell is one side of the before/after cell.
func diffCell(c CheckCount) string {
	return fmt.Sprintf("%d/%d/%d", c.Pass, c.Fail, c.Inconclusive)
}

// impact is the count change a spec's cell shows ("+1 pass, -1 fail").
func impact(after, before CheckCount) string {
	pass, fail := after.Pass-before.Pass, after.Fail-before.Fail
	switch {
	case pass == 0 && fail == 0:
		return "="
	}
	out := ""
	if pass != 0 {
		out += fmt.Sprintf("%+d pass", pass)
	}
	if fail != 0 {
		if out != "" {
			out += ", "
		}
		out += fmt.Sprintf("%+d fail", fail)
	}
	return out
}

// SortedBotIDs lists a reading's bot ids, for a deterministic listing.
func (r Reading) SortedBotIDs() []string {
	out := make([]string, 0, len(r.Bots))
	for _, b := range r.Bots {
		out = append(out, b.ID)
	}
	sort.Strings(out)
	return out
}
