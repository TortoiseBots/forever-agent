// Package playdata holds the bench's two player-data rules, the ones that score a bot against what
// real WoW Forever players of its class and spec do (data/aotc, from aotc.gg; see that package's
// Credit).
//
// Rule "gear fits the spec" (AOTC-GEAR-FITS-SPEC): an equipped item is FINE when any row of the
// spec's own level 60 gear tables names it (any population, any share, any slot). An item outside
// that data is contrary only when its stat family is known and none of the families the spec's own
// players wear in that slot is its own; the acceptable families are summed by share from the data
// (familyShareFloor, not a union), never taken from a single priority stat, so a hybrid spec
// (protection paladin, retribution paladin) is not read as a pure caster and a shared item one
// hybrid wearer names does not make its family acceptable for every slot. Which specs read as
// hybrids is the data's answer: an enhancement shaman's own three priority rows are stamina,
// intellect and spell power, so it reads as a caster, which is the mail its players wear. An item whose family is unknown
// is INCONCLUSIVE, never a FAIL; stamina, crit and hit name no family, so they never make an item
// contrary. Item level is never a reason. A record with no item data is INCONCLUSIVE, never a pass.
//
// Rule "talents imply spells" (AOTC-TALENTS-IMPLY-SPELLS): for the bot's level group, the talents most
// players of its spec take (share at or above MinTalentShare) that grant an ACTIVE spell join the
// spell judge's should-have-cast list (kbspells.BotCasts.Expected), per combat window the bot's own
// samples prove. A talent the record proves learned is expected and fails when never cast, unless
// the spec's spell lists call the spell situational: that one is reported as held, never failed. A
// talent absent from a partial read-back (the runner learns the spec's marker kit, not the build) is
// unproven, so it never turns a PASS; a record with no talent data or no combat reports what it has
// and judges nothing.
//
// Both rules are opt-in (trials score --player-data) and no trial definition names them: they read
// the run's own record (the setup's bot_items read-back, the ledger's equip and learn read-backs,
// the samples' combat flags, the events' casts) and the tables.
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
	// GearFamilyShare is the share of a spec's characters that makes it a common wearer of an
	// item: the wearers that name an item's family, and whose sums the gear rule weights by that
	// share.
	GearFamilyShare = 5.0
	// GearMismatchSpecs is how many specs must commonly wear an item before its wearers name its
	// stat family (one spec's piece may be a shared item).
	GearMismatchSpecs = 2
	// PriorityStats is how many of a spec's top priority stats its family set reads, and how many
	// the explanation line quotes as the tie-break: the verdict's families come from the gear data.
	PriorityStats = 3
	// MinTalentShare is the pick share at which most players of a spec take a talent (percent).
	MinTalentShare = 50.0
	// TalentMinActiveS is the active time a fight needs before a should-have-cast talent spell is
	// judged; shorter fights say nothing.
	TalentMinActiveS = 30.0
	// TalentCooldownFactor is the guard on long cooldowns: a spell whose cooldown is longer than
	// the combat window divided by this factor is not expected in it (a 3 min spell in a 60 s
	// fight).
	TalentCooldownFactor = 2.0
	// CombatGapS joins two in-combat samples into one combat window when they are at most this
	// many seconds apart: the runner samples at 1 Hz, so a longer gap is a break in the fight,
	// not a missing tick.
	CombatGapS = 3.0
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

// CombatWindow is one stretch [From, To] of the repeat's re-based seconds in which the bot's own
// samples place it in combat. A window starts and ends on the bot's in_combat flag, not on the
// repeat's clock: setup, travel and waiting are not fight time.
type CombatWindow struct {
	From float64
	To   float64
}

// Record is one repeat's view of the bot the rules judge: what it is, what it wore, what talents it
// learned, what it cast, and when it was in combat.
type Record struct {
	Bot     string
	Class   string
	Spec    string
	Role    string
	Level   int
	ActiveS float64 // total seconds inside Combat's windows, 0 when ActiveKnown is false

	Equipped      []Item
	EquippedKnown bool
	Talents       []int64
	TalentsKnown  bool
	// TalentsComplete says the talent list is the bot's full learned set. The runner's
	// `bot_talents` read-back learns the spec's marker kit, not the build, so it is partial:
	// a common talent absent from it may be trained or not.
	TalentsComplete bool
	Casts           map[string]int
	Combat          []CombatWindow
	ActiveKnown     bool
}

// Tables is the player-data view the rules read; *aotc.Data is the shipped one.
type Tables interface {
	SpecKnown(class, spec string) bool
	CommonTalents(class, spec, levels string, minShare float64) []aotc.TalentPick
	StatPriorityFor(class, spec string) []aotc.StatWeight
	GearFor(class, spec, slot string) []aotc.GearItem
	// GearRows is every gear row the spec has, across all slots and populations: the gear rule's
	// own-data membership and per-slot family reading.
	GearRows(class, spec string) []aotc.GearItem
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
// healing, spirit and mana per 5; a physical dps wants agility and attack power; a plate damage
// dealer or tank wants strength and defense. Crit, hit and stamina say nothing about the item's
// family: every priority lists stamina for survivability, so it cannot tell an agility piece from
// a strength one (a feral wants Agility second, an enhancement shaman Intellect).
func StatFamily(stat string) string {
	switch strings.ToLower(strings.TrimSpace(stat)) {
	case "spell power", "intellect", "healing", "spirit", "mana per 5":
		return FamilyCaster
	case "agility", "attack power":
		return FamilyAgility
	case "strength", "defense":
		return FamilyStrength
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
