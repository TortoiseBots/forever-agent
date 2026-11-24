// Package playdata holds the bench's two player-data rules, the ones that score a bot against what
// real WoW Forever players of its class and spec do (data/aotc, from aotc.gg; see that package's
// Credit).
//
// Rule "gear fits the spec" (AOTC-GEAR-FITS-SPEC): an equipped item is FINE when it is among the
// items the spec's level 60 players wear in that slot, or when its primary stats match the spec's
// stat priority. Only a clear mismatch fails: an item whose stats match none of the priority, or one
// that only players of a contrary stat family wear (a strength/tank item on a caster, a spell item on
// a rogue). Item level is never a reason. A record with no item data is INCONCLUSIVE, never a pass.
//
// Rule "talents imply spells" (AOTC-TALENTS-IMPLY-SPELLS): for the bot's level group, the talents most
// players of its spec take (share at or above MinTalentShare) that grant an ACTIVE spell join the
// spell judge's should-have-cast list (kbspells.BotCasts.Expected). A talent the bot does not have is
// never expected, and a record with no talent data reports the expectations without failing.
//
// Both rules are opt-in (trials score --player-data) and no trial definition names them: they read
// the run's own record (the ledger's equip and learn read-backs, the events' casts) and the tables.
package playdata

import (
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/data/aotc"
	"github.com/pjerra/forever-agent/internal/foreverspells"
	"github.com/pjerra/forever-agent/internal/kbspells"
	"github.com/pjerra/forever-agent/internal/trials/gear"
)

// The two rule ids, as they appear on the report card.
const (
	RuleGear    = "AOTC-GEAR-FITS-SPEC"
	RuleTalents = "AOTC-TALENTS-IMPLY-SPELLS"
)

// The rules' thresholds, named so a report can quote them.
const (
	// GearLevel is the only level the player gear data covers: a bot of another level is not
	// judged on it.
	GearLevel = 60
	// GearFamilyShare is the share of a spec's characters that makes the spec a common wearer of
	// an item, for the stat-family reading.
	GearFamilyShare = 5.0
	// GearMismatchSpecs is how many specs of another stat family must commonly wear an item before
	// its wearers prove a mismatch for this bot's family (one spec's item may be a shared piece).
	GearMismatchSpecs = 2
	// PriorityStats is how many of the spec's top priority stats an item's own stats must match.
	PriorityStats = 3
	// MinTalentShare is the pick share at which most players of a spec take a talent (percent).
	MinTalentShare = 50.0
	// TalentMinActiveS is the active time a fight needs before a should-have-cast talent spell is
	// judged; shorter fights say nothing.
	TalentMinActiveS = 30.0
	// TalentCooldownFactor is the guard on long cooldowns: a spell whose cooldown is longer than
	// the fight divided by this factor is not expected in it (a 3 min spell in a 60 s fight).
	TalentCooldownFactor = 2.0
)

// Item is one equipped item as the record carries it. Entry and Name may be 0/"" when a record
// carries only ids; Slot is resolved from the tables then. Stats are the item's primary stats and
// are empty when the record (and this repository) carries none.
type Item struct {
	Entry int64
	Name  string
	Slot  string
	Stats []string
}

// Record is one repeat's view of the bot the rules judge: what it is, what it wore, what talents it
// learned, what it cast, and how long it was active.
type Record struct {
	Bot     string
	Class   string
	Spec    string
	Role    string
	Level   int
	ActiveS float64

	Equipped      []Item
	EquippedKnown bool
	Talents       []int64
	TalentsKnown  bool
	Casts         map[string]int
}

// Tables is the player-data view the rules read; *aotc.Data is the shipped one.
type Tables interface {
	SpecKnown(class, spec string) bool
	CommonTalents(class, spec, levels string, minShare float64) []aotc.TalentPick
	StatPriorityFor(class, spec string) []aotc.StatWeight
	GearFor(class, spec, slot string) []aotc.GearItem
	Item(id int64) (aotc.ItemInfo, bool)
}

// Inputs are the tables one judgment reads. A nil Spells or Oracle only disables the talent rule's
// reading: it reports INCONCLUSIVE with what is missing, never a pass.
type Inputs struct {
	Tables Tables
	Spells *foreverspells.Table
	Oracle *kbspells.Oracle
}

// DefaultInputs is the tables the build embeds. A build without the Forever spell data still judges
// gear; the talent rule reports that it cannot read the talents' spells.
func DefaultInputs() Inputs {
	in := Inputs{Tables: aotc.Default(), Spells: foreverspells.Default()}
	if oracle, err := kbspells.ForeverDefault(); err == nil {
		in.Oracle = oracle
	}
	return in
}

// Row is one player-data rule's verdict for one repeat, in the report card's row shape.
type Row struct {
	Rep      string
	ID       string
	Verdict  string
	K, N     int64
	Evidence []string
	Reason   string
}

// Doc renders the row for trial.json and the report card (the same keys as a DSL rule's row, minus
// the ones only the DSL has).
func (r Row) Doc() map[string]any {
	evidence := r.Evidence
	if evidence == nil {
		evidence = []string{}
	}
	return map[string]any{
		"rep": r.Rep, "id": r.ID, "verdict": r.Verdict, "k": r.K, "n": r.N,
		"evidence": evidence, "reason": r.Reason, "source": "aotc.gg",
	}
}

// Verdicts are the words a Row carries.
const (
	VerdictPass         = "PASS"
	VerdictFail         = "FAIL"
	VerdictInconclusive = "INCONCLUSIVE"
)

// Rows judges one repeat: the gear rule and the talent rule, in that order.
func Rows(rec Record, in Inputs) []Row {
	return []Row{gearRow(rec, in), talentRow(rec, in)}
}

// LevelGroup returns the talent level group a character level belongs to (talent-picks.csv's own
// groups), "" below level 10 where there are no points.
func LevelGroup(level int) string {
	switch {
	case level >= 60:
		return aotc.LevelMax
	case level >= 50:
		return "50-59"
	case level >= 40:
		return "40-49"
	case level >= 30:
		return "30-39"
	case level >= 20:
		return "20-29"
	case level >= 10:
		return "10-19"
	}
	return ""
}

// specAliases maps the bench's spec words to the player tables' names.
var specAliases = map[string]string{
	"bm": "beast-mastery", "beastmastery": "beast-mastery", "beast-mastery": "beast-mastery",
	"mm": "marksmanship", "marksmanship": "marksmanship",
	"assa": "assassination", "assassination": "assassination",
	"ele": "elemental", "elemental": "elemental",
	"enh": "enhancement", "enhancement": "enhancement",
	"boomkin": "balance", "balance": "balance",
	"cat": "feral", "bear": "feral", "feral": "feral",
	"resto": "restoration", "restoration": "restoration",
	"disc": "discipline", "discipline": "discipline",
}

// SpecName maps a bench spec word to the player tables' spec name.
func SpecName(spec string) string {
	key := strings.ToLower(strings.TrimSpace(spec))
	if name, ok := specAliases[key]; ok {
		return name
	}
	return key
}

// The stat families the gear rule's wearer reading uses.
const (
	FamilyCaster   = "caster"
	FamilyAgility  = "agility"
	FamilyStrength = "strength"
	FamilyAny      = "any"
)

// StatFamily is the stat family of one priority stat: a caster wants spell power, intellect,
// healing, spirit and mana per 5; a physical dps wants agility and attack power; a tank or a plate
// damage dealer wants stamina and defense. Crit and hit say nothing about the item's family.
func StatFamily(stat string) string {
	switch strings.ToLower(strings.TrimSpace(stat)) {
	case "spell power", "intellect", "healing", "spirit", "mana per 5":
		return FamilyCaster
	case "agility", "attack power":
		return FamilyAgility
	case "stamina", "defense":
		return FamilyStrength
	}
	return FamilyAny
}

// SpecFamily is the family of a spec's stat priority: the first priority stat that has one.
func SpecFamily(priority []aotc.StatWeight) string {
	for _, w := range priority {
		if family := StatFamily(w.Stat); family != FamilyAny {
			return family
		}
	}
	return FamilyAny
}

// kitSlot resolves an item id that the player tables do not list through the bench's gear kits (the
// kits the live runner equips). "" when no kit names it.
func kitSlot(entry int64) (name, slot string) {
	for _, roles := range gear.LoadKits() {
		for _, levels := range roles {
			for _, items := range levels {
				for _, item := range items {
					if item.Entry == entry {
						return item.Name, item.Slot
					}
				}
			}
		}
	}
	return "", ""
}

// resolve fills an item's name and slot from the tables (then the bench's kits) when the record
// carried only an id. ok is false when neither names the slot.
func resolve(item Item, t Tables) (Item, bool) {
	if item.Name == "" && item.Entry > 0 {
		if info, found := t.Item(item.Entry); found {
			item.Name = info.Name
		} else if name, _ := kitSlot(item.Entry); name != "" {
			item.Name = name
		}
	}
	if item.Slot == "" && item.Entry > 0 {
		if info, found := t.Item(item.Entry); found && len(info.Slots) == 1 {
			item.Slot = info.Slots[0]
		} else if _, slot := kitSlot(item.Entry); slot != "" {
			item.Slot = slot
		}
	}
	if item.Name == "" && item.Entry > 0 {
		item.Name = "item " + strconv.FormatInt(item.Entry, 10)
	}
	return item, item.Slot != ""
}

// sortedEvidence keeps a rule's lines stable whatever map order found them.
func sortedEvidence(lines []string) []string {
	out := append([]string(nil), lines...)
	sort.Strings(out)
	return out
}
