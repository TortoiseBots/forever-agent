package realism

import (
	"strings"
	"testing"
)

// item is one worn item for a test: its slot, id and item level.
func item(slot string, id int64, level int) Item {
	return Item{Slot: slot, ItemID: id, ItemLevel: level, Source: "dungeon"}
}

func TestGearCheckPassesAPlausibleSixty(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{
		item("head", 22267, 62), item("neck", 22403, 60), item("chest", 22409, 62),
		item("legs", 252486, 67), item("feet", 226774, 61), item("hands", 226777, 61),
		item("wrists", 226775, 60), item("back", 272411, 59), item("main_hand", 13964, 58),
		item("off_hand", 22253, 60), item("ranged", 272430, 60), item("finger1", 12543, 54),
	}
	b.Items[0].Quality = new(3)
	b.Items[2].Quality = new(4)
	c := checkOfID(t, Judge(b, Default()), RuleGear)
	if c.Verdict != VerdictPass {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	level := partOf(t, c, "item_level")
	if level.Value != 0 || level.Verdict != VerdictPass {
		t.Fatalf("item level part: %+v", level)
	}
	source := partOf(t, c, "source")
	if source.Verdict != VerdictPass {
		t.Fatalf("source part: %+v", source)
	}
	joined := strings.Join(c.Evidence, "\n")
	if !strings.Contains(joined, "average item level") || !strings.Contains(joined, "quality mix") {
		t.Fatalf("the evidence must report the average item level and the quality mix:\n%s", joined)
	}
}

func TestGearCheckFailsAnOverLevelledOutfit(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{
		item("head", 22267, 62), item("chest", 22409, 95), item("legs", 252486, 67), item("feet", 226774, 61),
	}
	b.Items[1].Source = "raid"
	c := checkOfID(t, Judge(b, Default()), RuleGear)
	if c.Verdict != VerdictFail {
		t.Fatalf("verdict %s (%s)", c.Verdict, c.Reason)
	}
	if level := partOf(t, c, "item_level"); level.Verdict != VerdictFail || level.Value != 0.25 {
		t.Fatalf("item level part: %+v", level)
	}
	if source := partOf(t, c, "source"); source.Verdict != VerdictFail {
		t.Fatalf("a raid item must fail the source part: %+v", source)
	}
}

// TestGearAllowanceBands walks each band's boundary: one item level inside the allowance passes, one
// outside fails. Mutation: changing a band's allowance moves its two cases.
func TestGearAllowanceBands(t *testing.T) {
	cases := []struct {
		level int
		ilvl  int
		pass  bool
	}{
		{19, 25, true}, {19, 26, false},
		{29, 37, true}, {29, 38, false},
		{39, 49, true}, {39, 50, false},
		{49, 61, true}, {49, 62, false},
		{59, 74, true}, {59, 75, false},
		{60, 90, true}, {60, 91, false},
	}
	for _, tc := range cases {
		b := bot("bot1", tc.level)
		b.Items = []Item{item("chest", 100, tc.ilvl)}
		c := checkOfID(t, Judge(b, Default()), RuleGear)
		level := partOf(t, c, "item_level")
		if tc.pass && level.Verdict != VerdictPass {
			t.Fatalf("level %d, item level %d must pass: %+v", tc.level, tc.ilvl, level)
		}
		if !tc.pass && level.Verdict != VerdictFail {
			t.Fatalf("level %d, item level %d must fail: %+v", tc.level, tc.ilvl, level)
		}
	}
}

// TestGearAboveShareThreshold walks the 10% bar: one of ten slots above passes, two fail.
func TestGearAboveShareThreshold(t *testing.T) {
	slots := []string{"head", "neck", "chest", "legs", "feet", "hands", "wrists", "back", "main_hand", "ranged"}
	build := func(above int) Bot {
		b := bot("bot1", 60)
		for i, slot := range slots {
			level := 60
			if i < above {
				level = 100
			}
			b.Items = append(b.Items, Item{Slot: slot, ItemID: int64(1000 + i), ItemLevel: level, Source: "dungeon"})
		}
		return b
	}
	if level := partOf(t, checkOfID(t, Judge(build(1), Default()), RuleGear), "item_level"); level.Verdict != VerdictPass {
		t.Fatalf("1 of 10 above must pass: %+v", level)
	}
	if level := partOf(t, checkOfID(t, Judge(build(2), Default()), RuleGear), "item_level"); level.Verdict != VerdictFail {
		t.Fatalf("2 of 10 above must fail: %+v", level)
	}
}

func TestGearCheckInconclusiveCases(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Bot)
		want string
	}{
		{"no items read", func(b *Bot) { b.ItemsKnown, b.Items = false, nil }, "no worn items"},
		{"no item levels", func(b *Bot) { b.Items = []Item{{Slot: "head", ItemID: 1, Source: "dungeon"}} }, "no worn item carries an item level"},
		{"no sources", func(b *Bot) { b.Items = []Item{{Slot: "head", ItemID: 1, ItemLevel: 60}} }, "no worn item carries a source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bot("bot1", 60)
			b.Items = []Item{item("head", 22267, 62)}
			tc.edit(&b)
			c := checkOfID(t, Judge(b, Default()), RuleGear)
			if !strings.Contains(c.Reason, tc.want) {
				t.Fatalf("reason %q does not name %q", c.Reason, tc.want)
			}
		})
	}
}

// TestGearSourcePartFailsClosed pins the source part's coverage: one permitted source beside an
// unknown one is INCONCLUSIVE (a raid item could be the one unread), and a known forbidden source
// outranks both.
func TestGearSourcePartFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		items   []Item
		verdict string
	}{
		{"one of two unread", []Item{
			{Slot: "head", ItemID: 1, ItemLevel: 60, Source: "dungeon"},
			{Slot: "chest", ItemID: 2, ItemLevel: 60},
		}, VerdictInconclusive},
		{"a raid item beside an unread one", []Item{
			{Slot: "head", ItemID: 1, ItemLevel: 60, Source: "raid"},
			{Slot: "chest", ItemID: 2, ItemLevel: 60},
		}, VerdictFail},
		{"every item read", []Item{
			{Slot: "head", ItemID: 1, ItemLevel: 60, Source: "quest"},
			{Slot: "chest", ItemID: 2, ItemLevel: 60, Source: "crafted"},
		}, VerdictPass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bot("bot1", 60)
			b.Items = tc.items
			source := partOf(t, checkOfID(t, Judge(b, Default()), RuleGear), "source")
			if source.Verdict != tc.verdict {
				t.Fatalf("source part %s, want %s (%+v)", source.Verdict, tc.verdict, source)
			}
		})
	}
}

// TestGearCheckIsNeverAFailOnMissingData pins the contract: a snapshot with nothing but a class and a
// level fails nothing.
func TestGearCheckIsNeverAFailOnMissingData(t *testing.T) {
	b := bot("bot1", 60)
	b.ItemsKnown = false
	b.TalentsKnown, b.TalentsComplete = false, false
	for _, c := range Judge(b, Default()) {
		if c.Verdict == VerdictFail {
			t.Fatalf("%s failed on an empty snapshot: %s", c.ID, c.Reason)
		}
	}
}
