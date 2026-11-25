package realism

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// goodSixty is a bot that passes every check it can be judged on: the common balance build at 60,
// its spec's own gear at plausible item levels, required levels read, enchant ids read.
func goodSixty() Bot {
	b := bot("bot1", 60)
	b.Spec = "balance"
	b.Talents = balance60()
	b.Items = []Item{
		{Slot: "head", ItemID: 22267, ItemLevel: 62, RequiredLevel: 58, StatsKnown: true, Source: "dungeon", EnchantID: new(int64(0))},
		{Slot: "chest", ItemID: 22409, ItemLevel: 62, RequiredLevel: 58, StatsKnown: true, Source: "dungeon", EnchantID: new(int64(0))},
		{Slot: "main_hand", ItemID: 13964, ItemLevel: 58, RequiredLevel: 55, StatsKnown: true, Source: "dungeon", EnchantID: new(int64(273629))},
	}
	return b
}

func TestReadingCountsPerSpecAndCheck(t *testing.T) {
	s := Snapshot{Format: Format, Source: "before", Bots: []Bot{goodSixty()}}
	second := goodSixty()
	second.ID = "bot2"
	second.Level = 45
	second.Talents = balance60()[:14]
	second.Talents[12].Rank = 3 // 36 points: exactly what level 45 allows
	second.ItemsKnown, second.Items = false, nil
	s.Bots = append(s.Bots, second)
	r := JudgeReading(s, Default())
	if r.Format != ReadingFormat || r.Judged() != 2 {
		t.Fatalf("reading: %+v", r)
	}
	if len(r.Specs) != 2 {
		t.Fatalf("a level 60 and a level 45 bot are two bands: %+v", r.Specs)
	}
	for _, spec := range r.Specs {
		if spec.Spec != "balance" || spec.Bots != 1 {
			t.Fatalf("spec row: %+v", spec)
		}
	}
	if r.Specs[0].Band != "40-49" || r.Specs[1].Band != "60" {
		t.Fatalf("bands must sort: %+v", r.Specs)
	}
	byID := map[string]CheckCount{}
	for _, c := range r.Checks {
		byID[c.ID] = c
	}
	if c := byID[RuleTalents]; c.Pass != 2 || c.Fail != 0 {
		t.Fatalf("both builds are common ones: %+v", c)
	}
	if c := byID[RuleGear]; c.Pass != 1 || c.Inconclusive != 1 {
		t.Fatalf("gear (the second bot's items were not read): %+v", c)
	}
	// The level 40 bot's stat and enchant checks are INCONCLUSIVE (level 60 data only).
	if c := byID[RuleStats]; c.Pass != 1 || c.Inconclusive != 1 {
		t.Fatalf("stats: %+v", c)
	}
	if r.Fails() != 0 {
		t.Fatalf("a passing reading reports %d fails", r.Fails())
	}
}

// TestSpecMeanNeedsEnoughBots pins the minimum: one or two judged bots do not make a spec mean.
func TestSpecMeanNeedsEnoughBots(t *testing.T) {
	s := Snapshot{Format: Format}
	for i := 1; i <= MinSpecBotsForMean-1; i++ {
		b := goodSixty()
		b.ID = "bot" + string(rune('0'+i))
		s.Bots = append(s.Bots, b)
	}
	r := JudgeReading(s, Default())
	for _, spec := range r.Specs {
		for _, c := range spec.Checks {
			if c.Judged > 0 && c.Mean != 0 {
				t.Fatalf("%d judged bots must not carry a mean: %+v", c.Judged, c)
			}
		}
	}
	if !strings.Contains(countCell(r.Specs[0].Checks[0]), "n=") {
		t.Fatalf("a cell below the minimum must say how many bots it is over: %q", countCell(r.Specs[0].Checks[0]))
	}
}

func TestReadingTextReportsEveryBotAndSpec(t *testing.T) {
	s := Snapshot{Format: Format, Source: "run-a", Bots: []Bot{goodSixty()}}
	var w bytes.Buffer
	if err := JudgeReading(s, Default()).WriteText(&w); err != nil {
		t.Fatal(err)
	}
	out := w.String()
	for _, want := range []string{"## Checks", "## Per spec", "## Per bot", RuleTalents, RuleGear, RuleStats, RuleEnchants,
		"druid/balance", "bot1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the report does not name %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "credit:") || !strings.Contains(out, "aotc.gg (") {
		t.Fatalf("the report must carry the data credit:\n%s", out)
	}
}

// botsOf is n level 60 bots (ids prefix01...), of which `failing` use a raid-sourced item (a GEAR FAIL).
func botsOf(prefix string, n, failing int) []Bot {
	var bots []Bot
	for i := range n {
		b := goodSixty()
		b.ID = fmt.Sprintf("%s%02d", prefix, i+1)
		if i < failing {
			b.Items[0].Source = "raid"
		}
		bots = append(bots, b)
	}
	return bots
}

// reading is a judged snapshot of the bots.
func readingOf(source string, bots []Bot) Reading {
	return JudgeReading(Snapshot{Format: Format, Source: source, Bots: bots}, Default())
}

// TestCompareComparesSpecsAndBands pins the unit of comparison: a spec that failed on the older
// snapshot and passes on the newer one reads better, and no bot id is paired across the two.
func TestCompareComparesSpecsAndBands(t *testing.T) {
	d := Compare(readingOf("before", botsOf("a", MinBotsForChange, MinBotsForChange)),
		readingOf("after", botsOf("b", MinBotsForChange+1, 0)))
	if d.Format != DiffFormat || d.Before != "before" || d.After != "after" {
		t.Fatalf("diff header: %+v", d)
	}
	gear := checkDiffOf(t, d, RuleGear)
	if gear.Change != "better" || gear.Before.Fail != MinBotsForChange || gear.After.Fail != 0 || gear.After.Pass != MinBotsForChange+1 {
		t.Fatalf("fewer failing bots is better: %+v", gear)
	}
	if len(d.Specs) != 1 || d.Specs[0].Band != "60" || d.Specs[0].Presence != "" {
		t.Fatalf("both readings share one spec and band: %+v", d.Specs)
	}
	for _, c := range d.Specs[0].Checks {
		if c.ID == RuleGear && c.Change != "better" {
			t.Fatalf("the same spec and band must read better: %+v", c)
		}
	}
	var w bytes.Buffer
	if err := d.WriteText(&w); err != nil {
		t.Fatal(err)
	}
	out := w.String()
	if !strings.Contains(out, "## Per spec and level band") || !strings.Contains(out, "better") {
		t.Fatalf("the before/after text must name the change:\n%s", out)
	}
	if strings.Contains(out, "## Bots that changed") {
		t.Fatalf("bots are not identities and must not be paired:\n%s", out)
	}
}

// TestCompareReportsARegression pins the other direction on shares: three failing bots among five
// is worse than none among five.
func TestCompareReportsARegression(t *testing.T) {
	d := Compare(readingOf("before", botsOf("a", 5, 0)), readingOf("after", botsOf("b", 5, 3)))
	if gear := checkDiffOf(t, d, RuleGear); gear.Change != "worse" {
		t.Fatalf("three of five bots failing is worse than none: %+v", gear)
	}
}

// TestCompareLostReadoutIsNotBetter pins the review's case: FAIL on INCONCLUSIVE is "no longer
// judged", never "better".
func TestCompareLostReadoutIsNotBetter(t *testing.T) {
	after := botsOf("z", 5, 0) // different ids, so no id pairing could help
	for i := range after {
		after[i].ItemsKnown, after[i].Items = false, nil
	}
	d := Compare(readingOf("before", botsOf("a", 5, 5)), readingOf("after", after))
	if change := rowChange(t, d, RuleGear); change != "no longer judged" {
		t.Fatalf("a FAIL that became INCONCLUSIVE is a lost read-out, not %q", change)
	}
	if head := checkDiffOf(t, d, RuleGear).Change; head != "no longer judged" {
		t.Fatalf("the headline follows its row, not %q", head)
	}
}

// TestComparePassToInconclusiveIsNoLongerJudged pins the other lost read-out: PASS turning
// INCONCLUSIVE is "no longer judged" in the row and the headline, never "worse" or "unchanged".
func TestComparePassToInconclusiveIsNoLongerJudged(t *testing.T) {
	after := botsOf("z", 5, 0)
	for i := range after {
		after[i].ItemsKnown, after[i].Items = false, nil
	}
	d := Compare(readingOf("before", botsOf("a", 5, 0)), readingOf("after", after))
	if change := rowChange(t, d, RuleGear); change != "no longer judged" {
		t.Fatalf("PASS to INCONCLUSIVE is a lost read-out, not %q", change)
	}
	if head := checkDiffOf(t, d, RuleGear).Change; head != "no longer judged" {
		t.Fatalf("the headline follows its row, not %q", head)
	}
}

// rowChange is the gear row's word of the only shared spec and band.
func rowChange(t *testing.T, d Diff, id string) string {
	t.Helper()
	for _, c := range specDiffOf(t, d, "druid", "balance", "60").Checks {
		if c.ID == id {
			return c.Change
		}
	}
	t.Fatalf("no %s row", id)
	return ""
}

// TestCompareMixShiftDoesNotMoveTheHeadline pins the headline against a change in how many bots a
// band holds: band A has 5 bots all failing GEAR before and 50 all failing after, band B 5 passing
// bots on both sides. Both rows read unchanged, so the headline does too (pooled counts went from
// 5 of 10 failing to 50 of 55 and read "worse").
func TestCompareMixShiftDoesNotMoveTheHeadline(t *testing.T) {
	other := func(prefix string, n int) []Bot {
		bots := botsOf(prefix, n, 0)
		for i := range bots {
			bots[i].Spec = "restoration"
		}
		return bots
	}
	before := append(botsOf("a", 5, 5), other("b", 5)...)
	after := append(botsOf("c", 50, 50), other("d", 5)...)
	d := Compare(readingOf("before", before), readingOf("after", after))
	head := checkDiffOf(t, d, RuleGear)
	if head.Change != "unchanged" || head.Rows == nil || head.Rows.Unchanged != 2 || head.Rows.Worse != 0 {
		t.Fatalf("both rows are unchanged, so is the headline: %+v rows %+v", head, head.Rows)
	}
	for _, s := range d.Specs {
		for _, c := range s.Checks {
			if c.ID == RuleGear && c.Change != "unchanged" {
				t.Fatalf("%s/%s: %q", s.Class, s.Spec, c.Change)
			}
		}
	}
	var w bytes.Buffer
	if err := d.WriteText(&w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "rows: 2 unchanged") {
		t.Fatalf("the headline text must show its rows:\n%s", w.String())
	}
}

// TestCompareMixedRowsReadMixed pins the headline when one band got better and another worse.
func TestCompareMixedRowsReadMixed(t *testing.T) {
	other := func(prefix string, n, failing int) []Bot {
		bots := botsOf(prefix, n, failing)
		for i := range bots {
			bots[i].Spec = "restoration"
		}
		return bots
	}
	before := append(botsOf("a", 5, 5), other("b", 5, 0)...)
	after := append(botsOf("c", 5, 0), other("d", 5, 5)...)
	head := checkDiffOf(t, Compare(readingOf("before", before), readingOf("after", after)), RuleGear)
	if head.Change != "mixed" || head.Rows.Better != 1 || head.Rows.Worse != 1 {
		t.Fatalf("one better and one worse row: %+v rows %+v", head, head.Rows)
	}
}

// TestCompareTooFewBotsIsNotComparable pins the minimum: a band with fewer than MinBotsForChange bots
// on a side never reads better or worse, and the headline over only such rows is not comparable.
func TestCompareTooFewBotsIsNotComparable(t *testing.T) {
	few := MinBotsForChange - 1
	d := Compare(readingOf("before", botsOf("a", few, few)), readingOf("after", botsOf("b", few, 0)))
	if change := rowChange(t, d, RuleGear); change != wordTooFew {
		t.Fatalf("%d bots a side is too few to call better: %q", few, change)
	}
	if head := checkDiffOf(t, d, RuleGear); head.Change != "not comparable" || head.Rows.NotComparable != 1 {
		t.Fatalf("the headline has no row it can compare: %+v rows %+v", head, head.Rows)
	}
	// One side under the minimum is enough; both at the minimum compare.
	d = Compare(readingOf("before", botsOf("a", MinBotsForChange, MinBotsForChange)), readingOf("after", botsOf("b", few, 0)))
	if change := rowChange(t, d, RuleGear); change != wordTooFew {
		t.Fatalf("one thin side is too few: %q", change)
	}
	d = Compare(readingOf("before", botsOf("a", MinBotsForChange, MinBotsForChange)), readingOf("after", botsOf("b", MinBotsForChange, 0)))
	if change := rowChange(t, d, RuleGear); change != "better" {
		t.Fatalf("%d bots a side compare: %q", MinBotsForChange, change)
	}
}

// TestCompareOnlyBeforeAndOnlyAfter pins the intersection rule: a spec or band in one snapshot only
// is listed with its presence, never compared against zero.
func TestCompareOnlyBeforeAndOnlyAfter(t *testing.T) {
	before := goodSixty()
	after := goodSixty()
	after.Level = 45
	after.Talents = balance60()[:14]
	after.Talents[12].Rank = 3
	d := Compare(
		JudgeReading(Snapshot{Format: Format, Source: "before", Bots: []Bot{before}}, Default()),
		JudgeReading(Snapshot{Format: Format, Source: "after", Bots: []Bot{after}}, Default()),
	)
	if len(d.Specs) != 2 {
		t.Fatalf("one band each side: %+v", d.Specs)
	}
	if d.Specs[0].Band != "40-49" || d.Specs[0].Presence != "only after" {
		t.Fatalf("the newer band must be listed as only after: %+v", d.Specs[0])
	}
	if d.Specs[1].Band != "60" || d.Specs[1].Presence != "only before" {
		t.Fatalf("the older band must be listed as only before: %+v", d.Specs[1])
	}
	for _, c := range d.Specs[0].Checks {
		if c.Change != "only after" {
			t.Fatalf("an only-after key is not compared: %+v", c)
		}
	}
	// No key is in both snapshots, so the headline has nothing to compare: never better or worse.
	for _, c := range d.Checks {
		if c.Change != "not comparable" {
			t.Fatalf("a headline with no shared key must read not comparable: %+v", c)
		}
	}
	if d.ComparedBefore != 0 || d.ComparedAfter != 0 || d.OnlyBeforeBots != 1 || d.OnlyAfterBots != 1 {
		t.Fatalf("bots per side: %+v", d)
	}
	var w bytes.Buffer
	if err := d.WriteText(&w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "only before") || !strings.Contains(w.String(), "only after") {
		t.Fatalf("the text must list both presences:\n%s", w.String())
	}
}

// tenSixtyBots is ten level 60 bots of which `failing` use a raid-sourced item (a GEAR FAIL).
func tenSixtyBots(failing int) []Bot { return botsOf("bot", 10, failing) }

// TestCompareHeadlineIgnoresBandsInOneSnapshotOnly pins the headline: the same ten level 60 bots
// (five GEAR FAILs) plus ten new INCONCLUSIVE level 45 bots is no change. Counting whole snapshots
// the fail share dropped from 0.5 to 0.25 and the headline read "better". The new bots are listed as
// only after and enter no verdict.
func TestCompareHeadlineIgnoresBandsInOneSnapshotOnly(t *testing.T) {
	before := tenSixtyBots(5)
	after := tenSixtyBots(5)
	for i := range 10 {
		b := goodSixty()
		b.ID = fmt.Sprintf("new%02d", i+1)
		b.Level = 45
		b.TalentsKnown, b.TalentsComplete, b.Talents = false, false, nil
		b.ItemsKnown, b.Items = false, nil
		after = append(after, b)
	}
	d := Compare(
		JudgeReading(Snapshot{Format: Format, Source: "before", Bots: before}, Default()),
		JudgeReading(Snapshot{Format: Format, Source: "after", Bots: after}, Default()),
	)
	for _, c := range d.Checks {
		if c.Change != "unchanged" {
			t.Fatalf("%s: ten new bots in a band of their own changed nothing, got %q (%+v -> %+v)", c.ID, c.Change, c.Before, c.After)
		}
	}
	if gear := checkDiffOf(t, d, RuleGear); gear.Before.Fail != 5 || gear.After.Fail != 5 || gear.After.total() != 10 {
		t.Fatalf("the headline counts the shared band only: %+v", gear)
	}
	if d.OnlyAfterBots != 10 || d.OnlyBeforeBots != 0 || d.ComparedBefore != 10 || d.ComparedAfter != 10 {
		t.Fatalf("bots per side: %+v", d)
	}
	var w bytes.Buffer
	if err := d.WriteText(&w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "10 bot(s) only after") {
		t.Fatalf("the text must list the 10 bots as only after:\n%s", w.String())
	}
}

// TestCompareHeadlineAndRowsShareOneVerdict pins that the headline and the rows read the same
// verdict when they cover the same keys: one shared band, half the bots failing before, none after.
func TestCompareHeadlineAndRowsShareOneVerdict(t *testing.T) {
	d := Compare(
		JudgeReading(Snapshot{Format: Format, Source: "before", Bots: tenSixtyBots(5)}, Default()),
		JudgeReading(Snapshot{Format: Format, Source: "after", Bots: tenSixtyBots(0)}, Default()),
	)
	head := checkDiffOf(t, d, RuleGear)
	row := specDiffOf(t, d, "druid", "balance", "60")
	for _, c := range row.Checks {
		if c.ID == RuleGear && (c.Change != head.Change || head.Change != "better") {
			t.Fatalf("headline %q and row %q must agree and read better", head.Change, c.Change)
		}
	}
}

// TestChangeWordLostReadoutAndShares pins the verdict words on counts alone.
func TestChangeWordLostReadoutAndShares(t *testing.T) {
	cases := []struct {
		name          string
		before, after CheckCount
		want          string
	}{
		{"fail to inconclusive", CheckCount{Fail: 5, Pass: 5}, CheckCount{Pass: 5, Inconclusive: 5}, "no longer judged"},
		{"pass to inconclusive", CheckCount{Pass: 10}, CheckCount{Pass: 5, Inconclusive: 5}, "no longer judged"},
		{"fewer fails", CheckCount{Fail: 5, Pass: 5}, CheckCount{Fail: 2, Pass: 8}, "better"},
		{"more fails", CheckCount{Fail: 2, Pass: 8}, CheckCount{Fail: 5, Pass: 5}, "worse"},
		{"same share, more bots", CheckCount{Fail: 5, Pass: 5}, CheckCount{Fail: 25, Pass: 25}, "unchanged"},
		{"an empty side", CheckCount{}, CheckCount{Fail: 5}, wordTooFew},
		{"one bot under the minimum", CheckCount{Fail: 5}, CheckCount{Pass: MinBotsForChange - 1}, wordTooFew},
	}
	for _, tc := range cases {
		if got := changeWord(tc.before, tc.after); got != tc.want {
			t.Fatalf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestCompareSameReadingIsNoChange pins that two identical snapshots diff to `unchanged` everywhere.
func TestCompareSameReadingIsNoChange(t *testing.T) {
	a := readingOf("a", botsOf("a", MinBotsForChange, 0))
	b := readingOf("b", botsOf("b", MinBotsForChange, 0))
	d := Compare(a, b)
	for _, c := range d.Checks {
		if c.Change != "unchanged" {
			t.Fatalf("%s changed between identical snapshots: %+v", c.ID, c)
		}
	}
	for _, sd := range d.Specs {
		for _, c := range sd.Checks {
			if c.Change != "unchanged" {
				t.Fatalf("%s/%s changed between identical snapshots: %+v", sd.Spec, c.ID, c)
			}
		}
	}
	var w bytes.Buffer
	if err := d.WriteText(&w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "## Checks") {
		t.Fatalf("the no-change report still prints the table:\n%s", w.String())
	}
}

// checkDiffOf returns one check's diff.
func checkDiffOf(t *testing.T, d Diff, id string) CheckDiff {
	t.Helper()
	for _, c := range d.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no %s in %+v", id, d.Checks)
	return CheckDiff{}
}

// specDiffOf returns one spec and band's diff.
func specDiffOf(t *testing.T, d Diff, class, spec, band string) SpecDiff {
	t.Helper()
	for _, s := range d.Specs {
		if s.Class == class && s.Spec == spec && s.Band == band {
			return s
		}
	}
	t.Fatalf("no %s/%s band %s in %+v", class, spec, band, d.Specs)
	return SpecDiff{}
}

// TestHeadlineWordDecisionTable pins the headline's word for every tally shape: better only without a
// worse row and without a row that is no longer judged, rows with too few bots outside the headline.
func TestHeadlineWordDecisionTable(t *testing.T) {
	cases := []struct {
		name  string
		tally RowTally
		want  string
	}{
		{"all unchanged", RowTally{Unchanged: 4}, "unchanged"},
		{"one better, rest unchanged", RowTally{Better: 1, Unchanged: 3}, "better"},
		{"one worse, rest unchanged", RowTally{Worse: 1, Unchanged: 3}, "worse"},
		{"one better, one worse", RowTally{Better: 1, Worse: 1}, "mixed"},
		{"one better, one no longer judged", RowTally{Better: 1, NoLongerJudged: 1}, "mixed"},
		{"one better, nine no longer judged", RowTally{Better: 1, NoLongerJudged: 9}, "mixed"},
		{"worse and no longer judged stays worse", RowTally{Worse: 1, NoLongerJudged: 2}, "worse"},
		{"only no longer judged", RowTally{NoLongerJudged: 3}, "no longer judged"},
		{"no longer judged beside unchanged", RowTally{NoLongerJudged: 1, Unchanged: 2}, "no longer judged"},
		{"all not comparable", RowTally{NotComparable: 3}, "not comparable"},
		{"no shared rows", RowTally{}, "not comparable"},
		{"one better among too few bots rows", RowTally{Better: 1, NotComparable: 4}, "better"},
	}
	for _, tc := range cases {
		if got := headlineWord(tc.tally); got != tc.want {
			t.Fatalf("%s: %+v reads %q, want %q", tc.name, tc.tally, got, tc.want)
		}
	}
}
