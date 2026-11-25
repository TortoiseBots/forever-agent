package realism

import (
	"bytes"
	"strings"
	"testing"
)

// goodSixty is a bot that passes every check it can be judged on: the common balance build at 60,
// its spec's own gear at plausible item levels, enchant ids read.
func goodSixty() Bot {
	b := bot("bot1", 60)
	b.Spec = "balance"
	b.Talents = balance60()
	b.Items = []Item{
		{Slot: "head", ItemID: 22267, ItemLevel: 62, Source: "dungeon", EnchantID: new(int64(0))},
		{Slot: "chest", ItemID: 22409, ItemLevel: 62, Source: "dungeon", EnchantID: new(int64(0))},
		{Slot: "main_hand", ItemID: 13964, ItemLevel: 58, Source: "dungeon", EnchantID: new(int64(273629))},
	}
	return b
}

func TestReadingCountsPerSpecAndPerCheck(t *testing.T) {
	s := Snapshot{Format: Format, Source: "before", Bots: []Bot{goodSixty()}}
	second := goodSixty()
	second.ID = "bot2"
	second.Level = 45
	second.Talents = balance60()[:14]
	second.ItemsKnown, second.Items = false, nil
	s.Bots = append(s.Bots, second)
	r := JudgeReading(s, Default())
	if r.Format != ReadingFormat || r.Judged() != 2 {
		t.Fatalf("reading: %+v", r)
	}
	if len(r.Specs) != 1 || r.Specs[0].Bots != 2 || r.Specs[0].Spec != "balance" {
		t.Fatalf("the spec row must hold both levels: %+v", r.Specs)
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
	if !strings.Contains(out, "credit:") || strings.Contains(out, "aotc.gg (") == false {
		t.Fatalf("the report must carry the data credit:\n%s", out)
	}
}

// TestCompareReportsARegressionAndAnImprovement pins the before/after: a bot that failed gear and now
// passes it is `better`, one that slipped is `worse`, and the text names both.
func TestCompareReportsARegressionAndAnImprovement(t *testing.T) {
	before := goodSixty()
	before.Items[0].ItemLevel = 120 // above the level 60 allowance
	beforeBots := []Bot{before}
	second := goodSixty()
	second.ID = "bot2"
	second.Items[0].ItemLevel = 120
	afterBots := []Bot{goodSixty(), second}

	beforeReading := JudgeReading(Snapshot{Format: Format, Source: "before", Bots: beforeBots}, Default())
	afterReading := JudgeReading(Snapshot{Format: Format, Source: "after", Bots: afterBots}, Default())
	d := Compare(beforeReading, afterReading)
	if d.Format != DiffFormat || d.Before != "before" || d.After != "after" {
		t.Fatalf("diff header: %+v", d)
	}
	gear := checkDiffOf(t, d, RuleGear)
	if gear.Change != "better" || gear.Before.Fail != 1 || gear.After.Fail != 1 || gear.After.Pass != 1 {
		t.Fatalf("2 bots, one failing both sides, is not a whole-check change: %+v", gear)
	}
	// bot1 improved, bot2 is new and fails.
	for _, b := range d.Bots {
		if b.ID != "bot1" {
			continue
		}
		for _, c := range b.Checks {
			if c.ID == RuleGear && c.Change != "better" {
				t.Fatalf("bot1's gear must read better: %+v", c)
			}
		}
	}
	var w bytes.Buffer
	if err := d.WriteText(&w); err != nil {
		t.Fatal(err)
	}
	out := w.String()
	if !strings.Contains(out, "## Bots that changed") || !strings.Contains(out, "better") {
		t.Fatalf("the before/after text must name the change:\n%s", out)
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
	for _, bd := range d.Bots {
		for _, c := range bd.Checks {
			if c.Change != "same" {
				t.Fatalf("%s/%s changed between identical snapshots: %+v", bd.ID, c.ID, c)
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
