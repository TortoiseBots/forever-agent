package realism

// The report: one snapshot judged, per bot and per spec AND level band, and — with a baseline — the
// before/after difference per check. `trials realism --snapshot FILE [--baseline FILE]` prints the
// text form and `--json` prints the machine-readable one, for posting.
//
// The per-spec line sums its bots per check with the three verdict counts and the mean of the
// numbers the bots' judged parts gave, so a spec's row says both "how many passed" and "how far from
// the players' own numbers"; a mean needs MinSpecBotsForMean judged bots, because one bot is not a
// spec. The before/after compares the two readings per spec AND level band, on shares rather than
// counts (the bot sets may differ) and only for bands with MinBotsForChange bots on each side, with only the intersection compared: a spec or band in one
// snapshot only is listed as such, and a verdict that became INCONCLUSIVE is "no longer judged",
// never "better". The headline table is built from the rows, never from whole snapshots or pooled counts. Bot ids are not identities (assigned by name order per snapshot), so no bot is
// paired across the two readings.

import (
	"fmt"
	"io"
	"sort"
	"strings"
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

// MinSpecBotsForMean is the fewest judged bots a per-spec mean is shown over: below it one bot's
// number would read as the spec's. Starting value to tune.
const MinSpecBotsForMean = 5

// MinBotsForChange is the fewest bots each side must hold in a spec and level band before its row may
// read better or worse: below it the row says "not comparable: too few bots", because one bot more or
// less is a swing of whole tens of percent. Starting value to tune.
const MinBotsForChange = 5

// wordTooFew is the row word for a band with fewer than MinBotsForChange bots on a side.
const wordTooFew = "not comparable: too few bots"

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

// SpecSummary is one class, spec and level band's row: its bots and the counts per check. A level
// band is a talent group (10-19 … 60, and 1-9 below level 10): mixing a level 60 bot with a level 40
// one would hide which band moved.
type SpecSummary struct {
	Class  string       `json:"class"`
	Spec   string       `json:"spec"`
	Band   string       `json:"band"`
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
		sum := SpecSummary{Class: group.Class, Spec: group.Spec, Band: group.Band, Bots: len(group.Bots), IDs: append([]string(nil), group.Bots...)}
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
				if v, ok := meanValue(id, c); ok {
					values = append(values, v)
				}
			}
			count.Judged = len(values)
			if len(values) >= MinSpecBotsForMean {
				count.Mean = meanFloats(values)
			}
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
	fmt.Fprintf(tw, "class/spec\tband\tbots")
	for _, id := range checkIDs {
		fmt.Fprintf(tw, "\t%s", id)
	}
	fmt.Fprintln(tw)
	for _, s := range r.Specs {
		fmt.Fprintf(tw, "%s\t%s\t%d", specName(s.Class, s.Spec), s.Band, s.Bots)
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

// countCell is one spec table cell: the three verdict counts and, when enough bots were judged, the
// mean of the judged numbers. Below MinSpecBotsForMean the cell says how many bots it is over.
func countCell(c CheckCount) string {
	cell := fmt.Sprintf("%d/%d/%d", c.Pass, c.Fail, c.Inconclusive)
	switch {
	case c.Judged >= MinSpecBotsForMean:
		cell += fmt.Sprintf(" %s %.2f", meanWord(c.ID), c.Mean)
	case c.Judged > 0:
		cell += fmt.Sprintf(" n=%d", c.Judged)
	}
	return cell
}

// meanValue is the number a check contributes to its spec cell's mean, and whether it has one. It is
// the value of the part the mean's label names (point_share for talents, item_level for gear, fits for
// stats), never another part's number: the gear check's first judged part can be the required-level
// count, which is no share of item levels above the allowance.
func meanValue(id string, c Check) (float64, bool) {
	name := ""
	switch id {
	case RuleTalents:
		name = "point_share"
	case RuleGear:
		name = "item_level"
	case RuleStats:
		name = "fits"
	default:
		if c.Verdict != VerdictInconclusive && c.Measure != "" {
			return c.Value, true
		}
		return 0, false
	}
	for _, p := range c.Parts {
		if p.Name == name && p.Verdict != VerdictInconclusive && p.Measure != "" {
			return p.Value, true
		}
	}
	return 0, false
}

// meanWord names the number a check's mean is: what the cell's number measures. The talents and gear
// means are shares (of the points the common picks account for; of the item levels read), the stats
// mean is the gear rule's fit share, and the enchant mean is a count of bad slots.
func meanWord(id string) string {
	switch id {
	case RuleTalents:
		return "share"
	case RuleGear:
		return "above"
	case RuleStats:
		return "fit"
	default:
		return "bad"
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

// RowTally counts the rows (spec and level bands present in both snapshots) of one check by the word
// changeWord gave them. The headline is built from it, never from pooled bot counts: pooling lets a
// change in how many bots a band holds move the headline while every row is unchanged.
type RowTally struct {
	Better         int `json:"better"`
	Worse          int `json:"worse"`
	Unchanged      int `json:"unchanged"`
	NoLongerJudged int `json:"no_longer_judged"`
	NotComparable  int `json:"not_comparable"`
}

// add counts one row word.
func (t *RowTally) add(word string) {
	switch word {
	case "better":
		t.Better++
	case "worse":
		t.Worse++
	case "no longer judged":
		t.NoLongerJudged++
	case "unchanged":
		t.Unchanged++
	default:
		t.NotComparable++
	}
}

// String is the tally as the report's text: the non-zero counts, "no rows" for none.
func (t RowTally) String() string {
	var out []string
	for _, c := range []struct {
		n    int
		word string
	}{{t.Better, "better"}, {t.Worse, "worse"}, {t.Unchanged, "unchanged"},
		{t.NoLongerJudged, "no longer judged"}, {t.NotComparable, "not comparable"}} {
		if c.n > 0 {
			out = append(out, fmt.Sprintf("%d %s", c.n, c.word))
		}
	}
	if len(out) == 0 {
		return "no rows"
	}
	return strings.Join(out, ", ")
}

// headlineWord is the headline's word for a check, decided from its row tally: worse when a row got
// worse and none better, better when a row got better and none worse, "mixed" when both, then "no
// longer judged", else unchanged; "not comparable" when no row could be compared.
func headlineWord(t RowTally) string {
	switch {
	case t.Better+t.Worse+t.Unchanged+t.NoLongerJudged == 0:
		return "not comparable"
	case t.Better > 0 && t.Worse > 0:
		return "mixed"
	case t.Worse > 0:
		return "worse"
	case t.Better > 0:
		return "better"
	case t.NoLongerJudged > 0:
		return "no longer judged"
	}
	return "unchanged"
}

// CheckDiff is one check's before/after counts. On the headline Rows tallies the per-spec rows the
// word comes from, and Before and After are the compared bands' summed counts, shown for information:
// they decide nothing.
type CheckDiff struct {
	ID     string     `json:"id"`
	Before CheckCount `json:"before"`
	After  CheckCount `json:"after"`
	Change string     `json:"change"`
	Rows   *RowTally  `json:"rows,omitempty"`
}

// SpecDiff is one class, spec and level band's before/after counts per check. Presence is "" when
// the key is in both readings ("compared"), else "only before" or "only after": those keys are
// listed, never compared.
type SpecDiff struct {
	Class    string      `json:"class"`
	Spec     string      `json:"spec"`
	Band     string      `json:"band"`
	Bots     int         `json:"bots"`
	Presence string      `json:"presence,omitempty"`
	Checks   []CheckDiff `json:"checks"`
}

// Diff is the before/after report: the per-check counts and the per-spec-and-band counts. It never
// pairs bots: a botN id is assigned by name order in each snapshot, so before.bot3 and after.bot3
// can be different characters.
//
// Checks is the headline and counts only the spec and level-band keys present in BOTH snapshots (the
// same keys the per-spec rows compare): ComparedBefore and ComparedAfter are the bots those keys hold
// on each side, and OnlyBeforeBots and OnlyAfterBots are the bots in keys of one snapshot only, which
// enter no better or worse verdict.
type Diff struct {
	Format         string      `json:"format"`
	Before         string      `json:"before,omitempty"`
	After          string      `json:"after,omitempty"`
	ComparedBefore int         `json:"compared_before_bots"`
	ComparedAfter  int         `json:"compared_after_bots"`
	OnlyBeforeBots int         `json:"only_before_bots"`
	OnlyAfterBots  int         `json:"only_after_bots"`
	Checks         []CheckDiff `json:"checks"`
	Specs          []SpecDiff  `json:"specs"`
}

// specKey is one class, spec and level band.
type specKey struct{ Class, Spec, Band string }

// Compare diffs two readings: after is the newer snapshot. Only keys in both readings are compared,
// the headline included (on shares over each side's bots in those keys); a key in one reading only
// is listed with its Presence and counted in OnlyBeforeBots or OnlyAfterBots.
func Compare(before, after Reading) Diff {
	d := Diff{Format: DiffFormat, Before: before.Source, After: after.Source}
	beforeSpecs := specMap(before.Specs)
	afterSpecs := specMap(after.Specs)
	keys := map[specKey]bool{}
	for k := range beforeSpecs {
		keys[k] = true
	}
	for k := range afterSpecs {
		keys[k] = true
	}
	ordered := make([]specKey, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		if a.Spec != b.Spec {
			return a.Spec < b.Spec
		}
		return a.Band < b.Band
	})
	headBefore := map[string]CheckCount{}
	headAfter := map[string]CheckCount{}
	tallies := map[string]*RowTally{}
	for _, k := range ordered {
		bs, bOK := beforeSpecs[k]
		as, aOK := afterSpecs[k]
		sd := SpecDiff{Class: k.Class, Spec: k.Spec, Band: k.Band}
		switch {
		case bOK && aOK:
			sd.Bots = as.Bots
			d.ComparedBefore += bs.Bots
			d.ComparedAfter += as.Bots
			for _, id := range checkIDs {
				bc, ac := countOf(bs.Checks, id), countOf(as.Checks, id)
				word := changeWord(bc, ac)
				sd.Checks = append(sd.Checks, CheckDiff{ID: id, Before: bc, After: ac, Change: word})
				if tallies[id] == nil {
					tallies[id] = &RowTally{}
				}
				tallies[id].add(word)
				headBefore[id] = addCounts(headBefore[id], bc)
				headAfter[id] = addCounts(headAfter[id], ac)
			}
		case bOK:
			sd.Bots, sd.Presence = bs.Bots, "only before"
			d.OnlyBeforeBots += bs.Bots
			for _, id := range checkIDs {
				sd.Checks = append(sd.Checks, CheckDiff{ID: id, Before: countOf(bs.Checks, id), Change: sd.Presence})
			}
		default:
			sd.Bots, sd.Presence = as.Bots, "only after"
			d.OnlyAfterBots += as.Bots
			for _, id := range checkIDs {
				sd.Checks = append(sd.Checks, CheckDiff{ID: id, After: countOf(as.Checks, id), Change: sd.Presence})
			}
		}
		d.Specs = append(d.Specs, sd)
	}
	// The headline word comes from the rows' words (one changeWord for every row), so a band that
	// holds more or fewer bots than before cannot move it while its row reads unchanged; bots in a
	// key of one snapshot only never reach it. The summed counts are shown, not judged.
	for _, id := range checkIDs {
		b, a := headBefore[id], headAfter[id]
		b.ID, a.ID = id, id
		tally := RowTally{}
		if tallies[id] != nil {
			tally = *tallies[id]
		}
		d.Checks = append(d.Checks, CheckDiff{ID: id, Before: b, After: a, Change: headlineWord(tally), Rows: &tally})
	}
	return d
}

// addCounts sums two checks' verdict counts (the ID and the mean are not carried: a mean does not add).
func addCounts(x, y CheckCount) CheckCount {
	return CheckCount{ID: x.ID, Pass: x.Pass + y.Pass, Fail: x.Fail + y.Fail, Inconclusive: x.Inconclusive + y.Inconclusive,
		Judged: x.Judged + y.Judged}
}

// total is the bots a count covers.
func (c CheckCount) total() int { return c.Pass + c.Fail + c.Inconclusive }

// specMap indexes per-spec-and-band rows by their key.
func specMap(specs []SpecSummary) map[specKey]SpecSummary {
	out := make(map[specKey]SpecSummary, len(specs))
	for _, s := range specs {
		out[specKey{Class: s.Class, Spec: s.Spec, Band: s.Band}] = s
	}
	return out
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

// changeWord is the ONE verdict function of the before/after report, used for every per-spec row (the
// headline is built from the rows' words): better, worse, unchanged, no longer judged, or not
// comparable. It reads SHARES of each
// side's bots (the two readings' bot sets differ in size). More FAILs is worse, fewer better; with FAILs
// equal, more PASSes is better. A verdict that turned INCONCLUSIVE while nothing started passing is "no
// longer judged": a lost read-out is not an improvement. A side with fewer than MinBotsForChange bots
// gives "not comparable: too few bots".
func changeWord(before, after CheckCount) string {
	beforeN, afterN := before.total(), after.total()
	if beforeN < MinBotsForChange || afterN < MinBotsForChange {
		return wordTooFew
	}
	switch {
	case shareMore(after.Inconclusive, afterN, before.Inconclusive, beforeN) &&
		!shareMore(after.Fail, afterN, before.Fail, beforeN) &&
		!shareMore(after.Pass, afterN, before.Pass, beforeN) &&
		(shareLess(after.Fail, afterN, before.Fail, beforeN) || shareLess(after.Pass, afterN, before.Pass, beforeN)):
		return "no longer judged"
	case shareLess(after.Fail, afterN, before.Fail, beforeN):
		return "better"
	case shareMore(after.Fail, afterN, before.Fail, beforeN):
		return "worse"
	case shareMore(after.Pass, afterN, before.Pass, beforeN):
		return "better"
	case shareLess(after.Pass, afterN, before.Pass, beforeN):
		return "worse"
	case shareMore(after.Inconclusive, afterN, before.Inconclusive, beforeN):
		return "no longer judged"
	}
	return "unchanged"
}

// shareLess reports count1/N1 < count2/N2 without floating point.
func shareLess(count1, n1, count2, n2 int) bool {
	return int64(count1)*int64(n2) < int64(count2)*int64(n1)
}

// shareMore reports count1/N1 > count2/N2 without floating point.
func shareMore(count1, n1, count2, n2 int) bool {
	return int64(count1)*int64(n2) > int64(count2)*int64(n1)
}

// WriteText prints the before/after as a table.
func (d Diff) WriteText(w io.Writer) error {
	fmt.Fprintf(w, "before: %s\nafter: %s\n", orDefault(d.Before, "unnamed"), orDefault(d.After, "unnamed"))
	fmt.Fprintln(w, "\n## Checks")
	fmt.Fprintf(w, "compared: the spec and level bands in both snapshots (%d bot(s) before, %d after); the word is decided by those bands' rows, the counts are for information\n", d.ComparedBefore, d.ComparedAfter)
	if d.OnlyBeforeBots > 0 || d.OnlyAfterBots > 0 {
		fmt.Fprintf(w, "not compared: %d bot(s) only before, %d bot(s) only after (bands in one snapshot only)\n", d.OnlyBeforeBots, d.OnlyAfterBots)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "check\tbefore\tafter\tchange")
	for _, c := range d.Checks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s (rows: %s)\n", c.ID, diffCell(c.Before), diffCell(c.After), c.Change, c.Rows)
	}
	tw.Flush()

	fmt.Fprintln(w, "\n## Per spec and level band")
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "class/spec\tband\tpresence")
	for _, id := range checkIDs {
		fmt.Fprintf(tw, "\t%s", id)
	}
	fmt.Fprintln(tw)
	var unpaired []SpecDiff
	for _, s := range d.Specs {
		fmt.Fprintf(tw, "%s\t%s\t%s", specName(s.Class, s.Spec), s.Band, orDefault(s.Presence, "compared"))
		for _, c := range s.Checks {
			if s.Presence != "" {
				fmt.Fprintf(tw, "\t%s", s.Presence)
				continue
			}
			fmt.Fprintf(tw, "\t%s -> %s %s", countCell(c.Before), countCell(c.After), c.Change)
		}
		fmt.Fprintln(tw)
		if s.Presence != "" {
			unpaired = append(unpaired, s)
		}
	}
	tw.Flush()
	if len(unpaired) > 0 {
		fmt.Fprintln(w, "\nSpecs and level bands in one snapshot only (listed, not compared):")
		for _, s := range unpaired {
			fmt.Fprintf(w, "  %s level band %s: %s\n", specName(s.Class, s.Spec), s.Band, s.Presence)
		}
	}
	return nil
}

// diffCell is one side of the before/after cell.
func diffCell(c CheckCount) string {
	return fmt.Sprintf("%d/%d/%d", c.Pass, c.Fail, c.Inconclusive)
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
