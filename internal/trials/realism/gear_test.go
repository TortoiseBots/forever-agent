package realism

import (
	"strings"
	"testing"
)

// item is one worn item for a test: its slot, id and item level, with a required level low enough to
// wear at any band (the required-level tests set it themselves).
func item(slot string, id int64, level int) Item {
	return Item{Slot: slot, ItemID: id, ItemLevel: level, RequiredLevel: 1, Source: "dungeon"}
}

func TestGearCheckPassesAPlausibleSixty(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{
		item("head", 22267, 62), item("neck", 22403, 60), item("chest", 22409, 62),
		item("legs", 252486, 65), item("feet", 226774, 61), item("hands", 226777, 61),
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
	for _, name := range []string{"required_level", "source"} {
		if p := partOf(t, c, name); p.Verdict != VerdictPass {
			t.Fatalf("%s part: %+v", name, p)
		}
	}
	joined := strings.Join(c.Evidence, "\n")
	if !strings.Contains(joined, "average item level") || !strings.Contains(joined, "quality mix") {
		t.Fatalf("the evidence must report the average item level and the quality mix:\n%s", joined)
	}
}

func TestGearCheckFailsAnOverLevelledOutfit(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{
		item("head", 22267, 62), item("chest", 22409, 95), item("legs", 252486, 65), item("feet", 226774, 61),
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

// TestGearRequiredLevelFails pins the clearest realism signal: an item whose required level is above
// the bot's is one no player of that level could wear, so one is a FAIL, not a share.
func TestGearRequiredLevelFails(t *testing.T) {
	b := bot("bot1", 30)
	b.Items = []Item{item("head", 22267, 30), item("chest", 22409, 31)}
	b.Items[1].RequiredLevel = 40
	c := checkOfID(t, Judge(b, Default()), RuleGear)
	if c.Verdict != VerdictFail {
		t.Fatalf("an item needing level 40 on a level 30 bot must fail: %s (%s)", c.Verdict, c.Reason)
	}
	part := partOf(t, c, "required_level")
	if part.Verdict != VerdictFail || part.Value != 1 || len(part.Lines) != 1 {
		t.Fatalf("required level part: %+v", part)
	}
}

// TestGearRequiredLevelUnreadIsInconclusive pins that the check never passes without the decisive
// required-level reading: an unread required level could be the impossible item.
func TestGearRequiredLevelUnreadIsInconclusive(t *testing.T) {
	b := bot("bot1", 60)
	b.Items = []Item{{Slot: "head", ItemID: 22267, ItemLevel: 62, Source: "dungeon"}}
	c := checkOfID(t, Judge(b, Default()), RuleGear)
	if c.Verdict != VerdictInconclusive || !strings.Contains(c.Reason, "required level") {
		t.Fatalf("an unread required level must leave the check INCONCLUSIVE: %s (%s)", c.Verdict, c.Reason)
	}
}

// TestGearAllowanceBands walks each band's boundary with the data-derived allowance (5 above the
// level through 59, 30 at 60): one item level inside passes, one outside fails. Mutation: changing a
// band's allowance moves its two cases.
func TestGearAllowanceBands(t *testing.T) {
	cases := []struct {
		level int
		ilvl  int
		pass  bool
	}{
		{19, 24, true}, {19, 25, false},
		{29, 34, true}, {29, 35, false},
		{39, 44, true}, {39, 45, false},
		{49, 54, true}, {49, 55, false},
		{59, 64, true}, {59, 65, false},
		{60, 90, true}, {60, 91, false},
	}
	for _, tc := range cases {
		b := bot("bot1", tc.level)
		b.Items = []Item{item("chest", 100, tc.ilvl)}
		level := partOf(t, checkOfID(t, Judge(b, Default()), RuleGear), "item_level")
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
			b.Items = append(b.Items, Item{Slot: slot, ItemID: int64(1000 + i), ItemLevel: level, RequiredLevel: 1, Source: "dungeon"})
		}
		return b
	}
	if level := partOf(t, checkOfID(t, Judge(build(1), Default()), RuleGear), "item_level"); level.Verdict != VerdictPass {
		t.Fatalf("1 of 10 above must pass: %+v", level)
	}
	if level := partOf(t, checkOfID(t, Judge(build(2), Default()), RuleGear), "item_level"); level.Verdict != VerdictFail {
		t.Fatalf("2 of 10 above must fail: %+v", level)
	}
	// A partial item set says how many worn items the item-level part judged.
	partial := bot("bot1", 60)
	partial.Items = []Item{item("head", 1, 60), {Slot: "chest", ItemID: 2, RequiredLevel: 1, Source: "dungeon"}}
	measure := partOf(t, checkOfID(t, Judge(partial, Default()), RuleGear), "item_level").Measure
	if !strings.Contains(measure, "1 of 2 worn item(s) judged") {
		t.Fatalf("the item level measure must say how many slots were judged: %q", measure)
	}
}

func TestGearCheckInconclusiveCases(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Bot)
		want string
	}{
		{"no items read", func(b *Bot) { b.ItemsKnown, b.Items = false, nil }, "no worn items"},
		{"naked bot", func(b *Bot) { b.Items = nil }, "wears nothing"},
		{"no item levels", func(b *Bot) { b.Items = []Item{{Slot: "head", ItemID: 1, RequiredLevel: 1, Source: "dungeon"}} }, "no worn item carries an item level"},
		{"no sources", func(b *Bot) { b.Items = []Item{{Slot: "head", ItemID: 1, ItemLevel: 60, RequiredLevel: 60}} }, "no worn item carries a source"},
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
// outranks both. The check itself is INCONCLUSIVE on a partial source reading, never a PASS.
func TestGearSourcePartFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		items   []Item
		verdict string
	}{
		{"one of two unread", []Item{
			{Slot: "head", ItemID: 1, ItemLevel: 60, RequiredLevel: 1, Source: "dungeon"},
			{Slot: "chest", ItemID: 2, ItemLevel: 60, RequiredLevel: 1},
		}, VerdictInconclusive},
		{"a raid item beside an unread one", []Item{
			{Slot: "head", ItemID: 1, ItemLevel: 60, RequiredLevel: 1, Source: "raid"},
			{Slot: "chest", ItemID: 2, ItemLevel: 60, RequiredLevel: 1},
		}, VerdictFail},
		{"every item read", []Item{
			{Slot: "head", ItemID: 1, ItemLevel: 60, RequiredLevel: 1, Source: "quest"},
			{Slot: "chest", ItemID: 2, ItemLevel: 60, RequiredLevel: 1, Source: "crafted"},
		}, VerdictPass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bot("bot1", 60)
			b.Items = tc.items
			c := checkOfID(t, Judge(b, Default()), RuleGear)
			source := partOf(t, c, "source")
			if source.Verdict != tc.verdict {
				t.Fatalf("source part %s, want %s (%+v)", source.Verdict, tc.verdict, source)
			}
			if tc.verdict == VerdictInconclusive && c.Verdict != VerdictInconclusive {
				t.Fatalf("a partial source reading must leave the check INCONCLUSIVE, not %s (%s)", c.Verdict, c.Reason)
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
