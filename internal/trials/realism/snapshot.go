// Package realism holds the bench's realism checks: readings of one bot snapshot (what a bot
// actually has) against the aggregate WoW Forever player data in data/aotc (what real players of
// its class and spec have). The snapshot is a file, not a live read: `forever bot-snapshot` writes
// one from what the agent can read today, and `trials realism` judges it.
//
// A snapshot names each bot by a name-free id (bot1, bot2, …; a real character name never goes into
// a file that may be committed), its class and spec, its level, its learned talents (by the talent's
// RANK-1 spell id and the rank learned), and the items it wears (slot, item id, item level, required
// level, quality, stats, enchant, and where the item came from when that is known). The loader is strict: an unknown key,
// an id that is not botN, a slot the server does not have, a level outside 1..60 or a quality
// outside the item ladder is refused with the file and the field, so a malformed file is never
// scored as a pass.
//
// Every check reports a number per bot and per spec with PASS, FAIL or INCONCLUSIVE, and none of
// them fails on missing data: a field the snapshot does not carry leaves the part that needs it
// INCONCLUSIVE with the reason stated (see the check files and docs/manual/testing/realism.md).
// The player data is player CHOICES, not game facts: for a talent or an item fact the server's own
// data wins (data/aotc/README.md).
package realism

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/data/aotc"
	"github.com/pjerra/forever-agent/internal/trials/playdata"
)

// Format is the snapshot file format this package reads and `forever bot-snapshot` writes. A file
// of another format is refused, never read as a best guess.
const Format = "forever-bot-snapshot/1"

// The equipment slot names a snapshot may use: the core's own order (Player.h EQUIPMENT_SLOT_*:
// head 0 through tabard 18). A slot outside this list is refused by the loader.
var slots = []string{
	"head", "neck", "shoulders", "body", "chest", "waist", "legs", "feet", "wrists", "hands",
	"finger1", "finger2", "trinket1", "trinket2", "back", "main_hand", "off_hand", "ranged", "tabard",
}

// tableSlot maps an equipment slot to the name the player tables use, and reports whether the
// tables carry that slot at all: gear-60.csv and enchants-60.csv name Head … Ranged and group both
// rings under "Rings"; they say nothing about shoulders, the shirt, the waist, the trinkets or the
// tabard, so a check that needs a table row answers INCONCLUSIVE for those slots.
func tableSlot(slot string) (string, bool) {
	switch slot {
	case "head":
		return "Head", true
	case "neck":
		return "Neck", true
	case "wrists":
		return "Wrist", true
	case "hands":
		return "Hands", true
	case "feet":
		return "Feet", true
	case "chest":
		return "Chest", true
	case "legs":
		return "Legs", true
	case "back":
		return "Back", true
	case "main_hand":
		return "Main Hand", true
	case "off_hand":
		return "Off Hand", true
	case "ranged":
		return "Ranged", true
	case "finger1", "finger2":
		return "Rings", true
	}
	return "", false
}

// itemSources are the words an item's `source` may carry: where the item comes from, as the taker
// read it. Empty means unknown (the checks that need a source answer INCONCLUSIVE, never a guess
// from the item's name).
var itemSources = []string{"quest", "dungeon", "raid", "pvp", "reputation", "world", "crafted", "vendor"}

// qualityNames is the client's item-quality ladder (ItemQuality, SharedDefines.h:384), the ids a
// snapshot's `quality` may carry.
var qualityNames = []string{"poor", "common", "uncommon", "rare", "epic", "legendary", "artifact", "heirloom"}

// Snapshot is one snapshot file: the bots' own state at the moment it was taken.
type Snapshot struct {
	Format string `json:"format"`
	Taken  string `json:"taken,omitempty"`
	Source string `json:"source,omitempty"`
	Bots   []Bot  `json:"bots"`
}

// Bot is one bot in a snapshot: what it is and what it has. A real character name is never a field
// of it; ID is botN, assigned by the taker in the file's own order.
type Bot struct {
	ID    string `json:"id"`
	Class string `json:"class"`
	Spec  string `json:"spec"`
	Level int    `json:"level"`

	// ItemsKnown says the taker read the bot's worn items: false is "not read", which every gear
	// check reports as missing data. An empty list of known items is a bot that wears nothing.
	ItemsKnown bool   `json:"items_known"`
	Items      []Item `json:"items,omitempty"`

	// TalentsKnown says the taker read the bot's learned talents; TalentsComplete says the list is
	// the bot's whole build, not a partial read-back. A check that needs the whole build answers
	// INCONCLUSIVE for a partial list.
	TalentsKnown    bool     `json:"talents_known"`
	TalentsComplete bool     `json:"talents_complete"`
	Talents         []Talent `json:"talents,omitempty"`

	// Notes are the taker's own remarks about fields it could not fill. They are reported, never
	// judged.
	Notes []string `json:"notes,omitempty"`
}

// Item is one worn item. A field the taker could not read is left out: ItemLevel 0, Quality nil,
// EnchantID nil and Source "" all mean "not read", never "zero".
type Item struct {
	Slot          string   `json:"slot"`
	ItemID        int64    `json:"item_id"`
	Name          string   `json:"name,omitempty"`
	ItemLevel     int      `json:"item_level,omitempty"`
	RequiredLevel int      `json:"required_level,omitempty"`
	Quality       *int     `json:"quality,omitempty"`
	Stats         []string `json:"stats,omitempty"`
	// EnchantID is the item's enchant as the server reports it: nil when not read, 0 when read and
	// unenchanted.
	EnchantID *int64 `json:"enchant_id,omitempty"`
	// Source is where the item comes from: one of quest, dungeon, raid, pvp, reputation, world,
	// crafted or vendor; "" when not read.
	Source string `json:"source,omitempty"`
}

// Talent is one learned talent: the talent's RANK-1 spell id (the id the player tables key the
// talent by, not the id of a higher learned rank) and the rank the bot has in it. Tab is the tree's
// page (0-2), nil when not read.
type Talent struct {
	SpellID int64 `json:"spell_id"`
	Rank    int   `json:"rank"`
	Tab     *int  `json:"tab,omitempty"`
}

// botIDRe is a snapshot bot's id: botN, N a positive number without a leading zero.
var botIDRe = regexp.MustCompile(`^bot[1-9][0-9]*$`)

// classRe is a class word: lower-case letters and dashes (beast-mastery is a spec, not a class, but
// the same shape keeps the loader honest about the word it is given).
var classRe = regexp.MustCompile(`^[a-z][a-z-]*$`)

// Marshal writes a snapshot as the file format: indented JSON with a trailing newline, the shape
// Load reads back.
func Marshal(s Snapshot) ([]byte, error) {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// Load reads and validates one snapshot file with the shipped player tables: the structural checks
// of Decode plus the table checks of ValidateTables. The error names the file and the field.
func Load(path string) (Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	s, err := Decode(f)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.ValidateTables(Default()); err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// ValidateTables checks the fields that need the player tables: a talent rank may not exceed the
// talent's own maximum rank, and a build may not hold more points than its level allows. Both are
// malformed snapshots, so the loader refuses them. A spell id the tables do not name is not refused
// here: the talent check reports it INCONCLUSIVE (the module may use another id, and a bad id must
// never read as a realism FAIL).
func (s Snapshot) ValidateTables(t Tables) error {
	for i := range s.Bots {
		b := &s.Bots[i]
		if len(b.Talents) == 0 {
			continue
		}
		maxRank := map[int64]int64{}
		for _, row := range t.CommonTalents(b.Class, b.Spec, "all", 0) {
			maxRank[row.SpellID] = row.MaxRank
		}
		total := 0
		for j, talent := range b.Talents {
			total += talent.Rank
			if max, ok := maxRank[talent.SpellID]; ok && max > 0 && int64(talent.Rank) > max {
				return fmt.Errorf("bots[%d]: talents[%d]: spell_id %d rank %d is above its maximum rank %d",
					i, j, talent.SpellID, talent.Rank, max)
			}
		}
		if allowed := TalentPoints(b.Level); total > allowed {
			return fmt.Errorf("bots[%d]: talents: %d point(s) is more than the %d a level %d character has",
				i, total, allowed, b.Level)
		}
	}
	return nil
}

// Decode reads and validates one snapshot from r.
func Decode(r io.Reader) (Snapshot, error) {
	var s Snapshot
	dec := json.NewDecoder(r)
	// An unknown key is refused: the format is the contract, and a stray `name` key must never
	// reach a file that could be committed.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Snapshot{}, fmt.Errorf("not a snapshot: %w", err)
	}
	// One value and then the end of the file: a second value is trailing data, which a reader must
	// refuse rather than ignore (half a file would otherwise judge as the whole one).
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Snapshot{}, errors.New("trailing data after the snapshot object")
		}
		return Snapshot{}, fmt.Errorf("trailing data after the snapshot object: %w", err)
	}
	if err := s.Validate(); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// Validate checks every field of a snapshot the loader would otherwise hand a check unread.
func (s Snapshot) Validate() error {
	if s.Format != Format {
		return fmt.Errorf("format: %q is not %q", s.Format, Format)
	}
	if s.Taken != "" {
		if _, err := time.Parse(time.RFC3339, s.Taken); err != nil {
			return fmt.Errorf("taken: %q is not an RFC 3339 time", s.Taken)
		}
	}
	if len(s.Bots) == 0 {
		return errors.New("bots: a snapshot needs at least one bot")
	}
	seen := map[string]bool{}
	for i := range s.Bots {
		b := &s.Bots[i]
		if err := b.validate(); err != nil {
			return fmt.Errorf("bots[%d]: %w", i, err)
		}
		if seen[b.ID] {
			return fmt.Errorf("bots[%d]: id %q repeats", i, b.ID)
		}
		seen[b.ID] = true
	}
	return nil
}

// validate checks one bot's own fields.
func (b *Bot) validate() error {
	if !botIDRe.MatchString(b.ID) {
		return fmt.Errorf("id: %q is not botN (a name-free id; character names never go into a snapshot)", b.ID)
	}
	if !classRe.MatchString(b.Class) {
		return fmt.Errorf("%s: class %q is not a lower-case class word", b.ID, b.Class)
	}
	if b.Spec != "" && !classRe.MatchString(b.Spec) {
		return fmt.Errorf("%s: spec %q is not a lower-case spec word (leave it empty when not read)", b.ID, b.Spec)
	}
	if b.Level < 1 || b.Level > 60 {
		return fmt.Errorf("%s: level %d is outside 1..60", b.ID, b.Level)
	}
	if len(b.Items) > 0 && !b.ItemsKnown {
		return fmt.Errorf("%s: items without items_known", b.ID)
	}
	slotsSeen := map[string]bool{}
	for i := range b.Items {
		it := &b.Items[i]
		if !slices.Contains(slots, it.Slot) {
			return fmt.Errorf("%s: items[%d]: slot %q is not one of %s", b.ID, i, it.Slot, strings.Join(slots, " "))
		}
		if slotsSeen[it.Slot] {
			return fmt.Errorf("%s: items[%d]: slot %q repeats", b.ID, i, it.Slot)
		}
		slotsSeen[it.Slot] = true
		if err := it.validate(); err != nil {
			return fmt.Errorf("%s: items[%d]: %w", b.ID, i, err)
		}
	}
	if len(b.Talents) > 0 && !b.TalentsKnown {
		return fmt.Errorf("%s: talents without talents_known", b.ID)
	}
	if b.TalentsComplete && !b.TalentsKnown {
		return fmt.Errorf("%s: talents_complete without talents_known", b.ID)
	}
	spells := map[int64]bool{}
	for i := range b.Talents {
		t := &b.Talents[i]
		if t.SpellID <= 0 {
			return fmt.Errorf("%s: talents[%d]: spell_id %d is not a positive id", b.ID, i, t.SpellID)
		}
		if spells[t.SpellID] {
			return fmt.Errorf("%s: talents[%d]: spell_id %d repeats", b.ID, i, t.SpellID)
		}
		spells[t.SpellID] = true
		if t.Rank < 1 || t.Rank > 5 {
			return fmt.Errorf("%s: talents[%d]: rank %d is outside 1..5", b.ID, i, t.Rank)
		}
		if t.Tab != nil && (*t.Tab < 0 || *t.Tab > 2) {
			return fmt.Errorf("%s: talents[%d]: tab %d is outside 0..2", b.ID, i, *t.Tab)
		}
	}
	for i, note := range b.Notes {
		if strings.TrimSpace(note) == "" {
			return fmt.Errorf("%s: notes[%d] is empty", b.ID, i)
		}
	}
	return nil
}

// validate checks one item's own fields.
func (it *Item) validate() error {
	if it.ItemID <= 0 {
		return fmt.Errorf("item_id %d is not a positive item id", it.ItemID)
	}
	if it.ItemLevel < 0 || it.ItemLevel > 300 {
		return fmt.Errorf("item_id %d: item_level %d is outside 0..300", it.ItemID, it.ItemLevel)
	}
	if it.RequiredLevel < 0 || it.RequiredLevel > 60 {
		return fmt.Errorf("item_id %d: required_level %d is outside 0..60", it.ItemID, it.RequiredLevel)
	}
	if it.Quality != nil && (*it.Quality < 0 || *it.Quality >= len(qualityNames)) {
		return fmt.Errorf("item_id %d: quality %d is outside the item ladder (0-%d)", it.ItemID, *it.Quality, len(qualityNames)-1)
	}
	if it.EnchantID != nil && *it.EnchantID < 0 {
		return fmt.Errorf("item_id %d: enchant_id %d is negative", it.ItemID, *it.EnchantID)
	}
	if it.Source != "" && !slices.Contains(itemSources, it.Source) {
		return fmt.Errorf("item_id %d: source %q is not one of %s", it.ItemID, it.Source, strings.Join(itemSources, " "))
	}
	for i, stat := range it.Stats {
		if strings.TrimSpace(stat) == "" {
			return fmt.Errorf("item_id %d: stats[%d] is empty", it.ItemID, i)
		}
	}
	return nil
}

// QualityName names a quality id; "" for an id outside the ladder.
func QualityName(q int) string {
	if q < 0 || q >= len(qualityNames) {
		return ""
	}
	return qualityNames[q]
}

// ClassName names a class id from the world's UNIT_FIELD_BYTES_0 (1 warrior … 11 druid, the 1.12
// ladder); "" for an id the ladder does not name.
func ClassName(id uint8) string {
	switch id {
	case 1:
		return "warrior"
	case 2:
		return "paladin"
	case 3:
		return "hunter"
	case 4:
		return "rogue"
	case 5:
		return "priest"
	case 7:
		return "shaman"
	case 8:
		return "mage"
	case 9:
		return "warlock"
	case 11:
		return "druid"
	}
	return ""
}

// SpecBots groups a snapshot's bots by class, spec and level band, the order the report prints:
// class then spec then band, and a bot with no spec under its class's empty spec last. Bands are the
// talent groups (10-19 … 60) plus 1-9 below level 10, so a level 60 bot is never summed with a level
// 45 one.
func SpecBots(s Snapshot) []SpecGroup {
	groups := map[string]*SpecGroup{}
	var order []string
	for _, b := range s.Bots {
		group := SpecGroup{Class: b.Class, Spec: b.Spec, Band: BandOf(b.Level)}
		key := group.Class + "\x00" + group.Spec + "\x00" + group.Band
		g := groups[key]
		if g == nil {
			g = &group
			groups[key] = g
			order = append(order, key)
		}
		g.Bots = append(g.Bots, b.ID)
	}
	out := make([]SpecGroup, 0, len(order))
	for _, key := range order {
		out = append(out, *groups[key])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		if out[i].Spec != out[j].Spec {
			return out[i].Spec < out[j].Spec
		}
		return out[i].Band < out[j].Band
	})
	return out
}

// SpecGroup is one class, spec and level band's bots, in the snapshot's order.
type SpecGroup struct {
	Class string
	Spec  string
	Band  string
	Bots  []string
}

// BandOf names a level's band: the talent groups the player data uses, and 1-9 below level 10 (no
// talent points, but still a band of its own).
func BandOf(level int) string {
	if band := playdata.LevelGroup(level); band != "" {
		return band
	}
	return "1-9"
}

// SlotName is an equipment slot word as the snapshot format writes it (lower case, trimmed), and
// whether the format carries it: the one list the taker and the loader share.
func SlotName(word string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(word))
	for _, known := range slots {
		if name == known {
			return name, true
		}
	}
	return "", false
}

// Default returns the tables every build embeds (data/aotc), the player data the checks read.
func Default() Tables { return aotc.Default() }
