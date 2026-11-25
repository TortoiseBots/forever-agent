package realism

import (
	"bytes"
	"strings"
	"testing"
)

// bot builds a snapshot bot for a test.
func bot(id string, level int) Bot {
	return Bot{ID: id, Class: "druid", Spec: "balance", Level: level,
		ItemsKnown: true, TalentsKnown: true, TalentsComplete: true}
}

// snapshotOf wraps bots in a valid snapshot.
func snapshotOf(bots ...Bot) Snapshot {
	return Snapshot{Format: Format, Taken: "2026-11-25T10:00:00Z", Source: "test", Bots: bots}
}

// jsonOf marshals a snapshot through the loader's own decoder, so a test reads what a file would.
func roundTrip(t *testing.T, s Snapshot) Snapshot {
	t.Helper()
	raw, err := Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("the loader refused a snapshot this package wrote: %v\n%s", err, raw)
	}
	return out
}

func TestSnapshotRoundTrip(t *testing.T) {
	in := snapshotOf(bot("bot1", 60))
	in.Bots[0].Items = []Item{{Slot: "head", ItemID: 22267, Name: "Spellweaver's Turban", ItemLevel: 62,
		RequiredLevel: 58, Quality: new(3), StatsKnown: true, Stats: []string{"Intellect"}, EnchantID: new(int64(0)), Source: "dungeon"}}
	in.Bots[0].Talents = []Talent{{SpellID: 16814, Rank: 5, Tab: new(0)}}
	out := roundTrip(t, in)
	if len(out.Bots) != 1 || out.Bots[0].Items[0].ItemLevel != 62 || *out.Bots[0].Items[0].Quality != 3 {
		t.Fatalf("round trip lost a field: %+v", out.Bots[0])
	}
	if out.Bots[0].Talents[0].SpellID != 16814 {
		t.Fatalf("talents: %+v", out.Bots[0].Talents)
	}
}

// TestSnapshotLoaderRefuses covers every malformed case the strict loader must refuse: each mutation
// is one field a check would otherwise read as data (or as zero).
func TestSnapshotLoaderRefuses(t *testing.T) {
	valid := func() Snapshot {
		s := snapshotOf(bot("bot1", 60))
		s.Bots[0].Items = []Item{{Slot: "head", ItemID: 22267, ItemLevel: 62, Quality: new(3), StatsKnown: true, EnchantID: new(int64(0)), Source: "dungeon"}}
		s.Bots[0].Talents = []Talent{{SpellID: 16814, Rank: 5, Tab: new(0)}}
		return s
	}
	cases := []struct {
		name string
		edit func(*Snapshot)
		want string
	}{
		{"format", func(s *Snapshot) { s.Format = "forever-bot-snapshot/2" }, "format"},
		{"no bots", func(s *Snapshot) { s.Bots = nil }, "at least one bot"},
		{"bad id", func(s *Snapshot) { s.Bots[0].ID = "char1" }, "botN"},
		{"leading zero", func(s *Snapshot) { s.Bots[0].ID = "bot01" }, "botN"},
		{"repeated id", func(s *Snapshot) {
			second := s.Bots[0]
			s.Bots = append(s.Bots, second)
		}, "repeats"},
		{"upper class", func(s *Snapshot) { s.Bots[0].Class = "Druid" }, "class"},
		{"empty class", func(s *Snapshot) { s.Bots[0].Class = "" }, "class"},
		{"bad spec", func(s *Snapshot) { s.Bots[0].Spec = "Balance Druid" }, "spec"},
		{"level zero", func(s *Snapshot) { s.Bots[0].Level = 0 }, "level"},
		{"level 61", func(s *Snapshot) { s.Bots[0].Level = 61 }, "level"},
		{"items not known", func(s *Snapshot) { s.Bots[0].ItemsKnown = false }, "items without items_known"},
		{"bad slot", func(s *Snapshot) { s.Bots[0].Items[0].Slot = "shoulder" }, "slot"},
		{"repeated slot", func(s *Snapshot) {
			second := s.Bots[0].Items[0]
			s.Bots[0].Items = append(s.Bots[0].Items, second)
		}, "repeats"},
		{"no item id", func(s *Snapshot) { s.Bots[0].Items[0].ItemID = 0 }, "item_id"},
		{"item level", func(s *Snapshot) { s.Bots[0].Items[0].ItemLevel = 301 }, "item_level"},
		{"required level", func(s *Snapshot) { s.Bots[0].Items[0].RequiredLevel = 61 }, "required_level"},
		{"quality", func(s *Snapshot) { s.Bots[0].Items[0].Quality = new(8) }, "quality"},
		{"enchant", func(s *Snapshot) { s.Bots[0].Items[0].EnchantID = new(int64(-1)) }, "enchant_id"},
		{"source", func(s *Snapshot) { s.Bots[0].Items[0].Source = "heroic" }, "source"},
		{"empty stat", func(s *Snapshot) { s.Bots[0].Items[0].Stats = []string{" "} }, "stats"},
		{"stats not known", func(s *Snapshot) {
			s.Bots[0].Items[0].StatsKnown = false
			s.Bots[0].Items[0].Stats = []string{"Intellect"}
		}, "stats without stats_known"},
		{"talents not known", func(s *Snapshot) { s.Bots[0].TalentsKnown = false }, "talents without talents_known"},
		{"complete not known", func(s *Snapshot) {
			s.Bots[0].TalentsKnown = false
			s.Bots[0].TalentsComplete = true
			s.Bots[0].Talents = nil
		}, "talents_complete"},
		{"spell id", func(s *Snapshot) { s.Bots[0].Talents[0].SpellID = 0 }, "spell_id"},
		{"repeated spell", func(s *Snapshot) { s.Bots[0].Talents = append(s.Bots[0].Talents, s.Bots[0].Talents[0]) }, "repeats"},
		{"rank", func(s *Snapshot) { s.Bots[0].Talents[0].Rank = 0 }, "rank"},
		{"rank six", func(s *Snapshot) { s.Bots[0].Talents[0].Rank = 6 }, "rank"},
		{"tab", func(s *Snapshot) { s.Bots[0].Talents[0].Tab = new(3) }, "tab"},
		{"empty note", func(s *Snapshot) { s.Bots[0].Notes = []string{"  "} }, "notes"},
		{"taken", func(s *Snapshot) { s.Taken = "yesterday" }, "taken"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.edit(&s)
			raw, err := Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(bytes.NewReader(raw)); err == nil {
				t.Fatalf("the loader accepted %s:\n%s", tc.name, raw)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the loader's error for %s does not name %q: %v", tc.name, tc.want, err)
			}
		})
	}
}

// TestSnapshotLoaderRefusesTrailingData pins that one file is one snapshot: a second JSON value after
// the object is refused rather than ignored.
func TestSnapshotLoaderRefusesTrailingData(t *testing.T) {
	doc := `{"format":"forever-bot-snapshot/1","bots":[{"id":"bot1","class":"druid","spec":"","level":60}]}`
	for _, trailing := range []string{"{}", "[]", `{"format":"forever-bot-snapshot/1","bots":[]}`, "garbage"} {
		if _, err := Decode(strings.NewReader(doc + trailing)); err == nil {
			t.Fatalf("the loader accepted trailing data %q", trailing)
		} else if !strings.Contains(err.Error(), "trailing data") {
			t.Fatalf("want a trailing-data refusal for %q, got %v", trailing, err)
		}
	}
	if _, err := Decode(strings.NewReader(doc + "\n")); err != nil {
		t.Fatalf("a trailing newline is not trailing data: %v", err)
	}
}

// TestSnapshotLoaderRefusesANameKey pins the name-free contract: a stray `name` at any level is an
// unknown key, so a real character name can never reach a committable file.
func TestSnapshotLoaderRefusesANameKey(t *testing.T) {
	docs := []string{
		`{"format":"forever-bot-snapshot/1","bots":[{"id":"bot1","class":"druid","spec":"balance","level":60,"name":"Someone"}]}`,
		`{"format":"forever-bot-snapshot/1","bots":[{"id":"bot1","class":"druid","spec":"balance","level":60,"items":[{"slot":"head","item_id":1,"character":"Someone"}]}]}`,
		`{"format":"forever-bot-snapshot/1","bots":[{"id":"bot1","class":"druid","spec":"balance","level":60,"talents":[{"spell_id":1,"rank":1,"owner":"Someone"}]}]}`,
	}
	for _, doc := range docs {
		if _, err := Decode(strings.NewReader(doc)); err == nil {
			t.Fatalf("the loader accepted a name key:\n%s", doc)
		} else if !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("want an unknown-field refusal, got %v", err)
		}
	}
}

func TestSpecBotsGroupsAndOrders(t *testing.T) {
	s := snapshotOf(
		Bot{ID: "bot1", Class: "warrior", Spec: "protection", Level: 60},
		Bot{ID: "bot2", Class: "druid", Spec: "balance", Level: 60},
		Bot{ID: "bot3", Class: "druid", Spec: "", Level: 60},
		Bot{ID: "bot4", Class: "druid", Spec: "balance", Level: 40},
		Bot{ID: "bot5", Class: "druid", Spec: "balance", Level: 45},
		Bot{ID: "bot6", Class: "druid", Spec: "balance", Level: 5},
	)
	groups := SpecBots(s)
	if len(groups) != 5 {
		t.Fatalf("groups: %+v", groups)
	}
	if groups[0].Class != "druid" || groups[0].Spec != "" || groups[0].Band != "60" || len(groups[0].Bots) != 1 {
		t.Fatalf("the empty spec must sort first under its class: %+v", groups[0])
	}
	if groups[1].Spec != "balance" || groups[1].Band != "1-9" || groups[1].Bots[0] != "bot6" {
		t.Fatalf("below level 10 is its own band: %+v", groups[1])
	}
	if groups[2].Spec != "balance" || groups[2].Band != "40-49" || len(groups[2].Bots) != 2 ||
		groups[2].Bots[0] != "bot4" || groups[2].Bots[1] != "bot5" {
		t.Fatalf("balance must group one band in snapshot order: %+v", groups[2])
	}
	if groups[3].Spec != "balance" || groups[3].Band != "60" || groups[3].Bots[0] != "bot2" {
		t.Fatalf("the level 60 bots are a separate band: %+v", groups[3])
	}
	if groups[4].Class != "warrior" {
		t.Fatalf("classes must sort: %+v", groups[4])
	}
}

// TestSnapshotTablesRefusesAMalformedBuild pins the loader's table checks: a rank above the talent's
// maximum, and a build with more points than the level allows, are malformed snapshots.
func TestSnapshotTablesRefusesAMalformedBuild(t *testing.T) {
	valid := func() Snapshot {
		s := snapshotOf(bot("bot1", 60))
		s.Bots[0].Talents = balance60()
		return s
	}
	cases := []struct {
		name string
		edit func(*Snapshot)
		want string
	}{
		{"rank above the talent's maximum", func(s *Snapshot) {
			s.Bots[0].Talents[2].Rank = 4 // Moonglow's maximum is 3
		}, "above its maximum rank"},
		{"more points than the level allows", func(s *Snapshot) {
			s.Bots[0].Level = 30
			s.Bots[0].Talents = balance60()[:4] // 12 points, level 30 allows 21: not this; use 25
			s.Bots[0].Talents = append(s.Bots[0].Talents,
				Talent{SpellID: 16909, Rank: 5}, Talent{SpellID: 16850, Rank: 5},
				Talent{SpellID: 16896, Rank: 5}, Talent{SpellID: 17063, Rank: 3})
		}, "more than the 21"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.edit(&s)
			if err := s.ValidateTables(Default()); err == nil {
				t.Fatalf("the tables accepted %s", tc.name)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the error does not name %q: %v", tc.want, err)
			}
		})
	}
	if err := valid().ValidateTables(Default()); err != nil {
		t.Fatalf("a valid build was refused: %v", err)
	}
}

func TestSlotTableNames(t *testing.T) {
	cases := map[string]string{
		"head": "Head", "neck": "Neck", "wrists": "Wrist", "hands": "Hands", "feet": "Feet",
		"chest": "Chest", "legs": "Legs", "back": "Back", "main_hand": "Main Hand",
		"off_hand": "Off Hand", "ranged": "Ranged", "finger1": "Rings", "finger2": "Rings",
	}
	for slot, want := range cases {
		if got, ok := tableSlot(slot); !ok || got != want {
			t.Fatalf("tableSlot(%q) = %q, %v; want %q", slot, got, ok, want)
		}
	}
	for _, slot := range []string{"shoulders", "body", "waist", "trinket1", "tabard"} {
		if _, ok := tableSlot(slot); ok {
			t.Fatalf("tableSlot(%q) must report no table row", slot)
		}
	}
}

func TestClassName(t *testing.T) {
	want := map[uint8]string{1: "warrior", 2: "paladin", 3: "hunter", 4: "rogue", 5: "priest", 7: "shaman", 8: "mage", 9: "warlock", 11: "druid"}
	for id, name := range want {
		if got := ClassName(id); got != name {
			t.Fatalf("ClassName(%d) = %q, want %q", id, got, name)
		}
	}
	for _, id := range []uint8{0, 6, 10, 12} {
		if got := ClassName(id); got != "" {
			t.Fatalf("ClassName(%d) = %q, want no class", id, got)
		}
	}
}
