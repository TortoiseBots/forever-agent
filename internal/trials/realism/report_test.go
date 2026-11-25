package realism

import (
	"bytes"
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

// TestCompareComparesSpecsAndBands pins the unit of comparison: a spec that failed on the older
// snapshot and passes on the newer one reads better, and no bot id is paired across the two.
func TestCompareComparesSpecsAndBands(t *testing.T) {
	before := goodSixty()
	before.Items[0].ItemLevel = 120 // above the level 60 allowance
	before.Items[0].Source = "raid"
	beforeBots := []Bot{before}
	second := goodSixty()
	second.ID = "bot9"
	afterBots := []Bot{goodSixty(), second}

	beforeReading := JudgeReading(Snapshot{Format: Format, Source: "before", Bots: beforeBots}, Default())
	afterReading := JudgeReading(Snapshot{Format: Format, Source: "after", Bots: afterBots}, Default())
	d := Compare(beforeReading, afterReading)
	if d.Format != DiffFormat || d.Before != "before" || d.After != "after" {
		t.Fatalf("diff header: %+v", d)
	}
	gear := checkDiffOf(t, d, RuleGear)
	if gear.Change != "better" || gear.Before.Fail != 1 || gear.After.Fail != 0 || gear.After.Pass != 2 {
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

// TestCompareReportsARegression pins the other direction on shares: one failing bot among two is
// worse than none among two.
func TestCompareReportsARegression(t *testing.T) {
	before := []Bot{goodSixty(), goodSixty()}
	before[1].ID = "bot2"
	after := []Bot{goodSixty(), goodSixty()}
	after[1].ID = "bot9"
	after[1].Items[0].Source = "raid"
	d := Compare(
		JudgeReading(Snapshot{Format: Format, Source: "before", Bots: before}, Default()),
		JudgeReading(Snapshot{Format: Format, Source: "after", Bots: after}, Default()),
	)
	gear := checkDiffOf(t, d, RuleGear)
	if gear.Change != "worse" {
		t.Fatalf("half the bots failing is worse than none: %+v", gear)
	}
}

// TestCompareLostReadoutIsNotBetter pins the review's case: FAIL on INCONCLUSIVE is "no longer
// judged", never "better".
func TestCompareLostReadoutIsNotBetter(t *testing.T) {
	before := goodSixty()
	before.Items[0].Source = "raid" // gear source FAIL
	after := goodSixty()
	after.ID = "bot7" // a different bot, so no id pairing could help
	after.ItemsKnown, after.Items = false, nil

	d := Compare(
		JudgeReading(Snapshot{Format: Format, Source: "before", Bots: []Bot{before}}, Default()),
		JudgeReading(Snapshot{Format: Format, Source: "after", Bots: []Bot{after}}, Default()),
	)
	gear := specDiffOf(t, d, "druid", "balance", "60").Checks
	var change string
	for _, c := range gear {
		if c.ID == RuleGear {
			change = c.Change
		}
	}
	if change != "no longer judged" {
		t.Fatalf("a FAIL that became INCONCLUSIVE is a lost read-out, not %q", change)
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
	var w bytes.Buffer
	if err := d.WriteText(&w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "only before") || !strings.Contains(w.String(), "only after") {
		t.Fatalf("the text must list both presences:\n%s", w.String())
	}
}

// TestCompareSameReadingIsNoChange pins that two identical snapshots diff to `same` everywhere.
func TestCompareSameReadingIsNoChange(t *testing.T) {
	a := JudgeReading(Snapshot{Format: Format, Source: "a", Bots: []Bot{goodSixty()}}, Default())
	b := JudgeReading(Snapshot{Format: Format, Source: "b", Bots: []Bot{goodSixty()}}, Default())
	d := Compare(a, b)
	for _, c := range d.Checks {
		if c.Change != "same" {
			t.Fatalf("%s changed between identical snapshots: %+v", c.ID, c)
		}
	}
	for _, sd := range d.Specs {
		for _, c := range sd.Checks {
			if c.Change != "same" {
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
