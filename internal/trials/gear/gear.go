// Package gear reads the gear kits (data/gear_kits.json) for the trial option
// `player.gear: "tier"`, and the proficiency spells each kit item needs.
package gear

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/pjerra/forever-agent/internal/trials/data"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// MaxLevelGap: a kit built for level 40 serves a level 40-49 trial when there is no kit of its own.
const MaxLevelGap = 9

// Kits is the whole gear table: class -> role -> level -> items.
type Kits = map[string]map[string]map[string][]Kit

type Kit struct {
	Armor         float64
	Entry         int64
	ItemClass     *int64
	Name          string
	RequiredLevel float64
	Slot          string
	Subclass      *int64
}

var (
	once sync.Once
	kits Kits
)

func load() {
	once.Do(func() {
		kits = map[string]map[string]map[string][]Kit{}
		doc, err := pyjson.Unmarshal(data.Read("gear_kits.json"))
		if err != nil {
			return
		}
		root, ok := pyjson.Object(doc)
		if !ok {
			return
		}
		for klass, rolesVal := range root {
			roles, ok := pyjson.Object(rolesVal)
			if !ok {
				continue
			}
			byRole := map[string]map[string][]Kit{}
			for role, levelsVal := range roles {
				levels, ok := pyjson.Object(levelsVal)
				if !ok {
					continue
				}
				byLevel := map[string][]Kit{}
				for level, itemsVal := range levels {
					items, ok := pyjson.List(itemsVal)
					if !ok {
						continue
					}
					byLevel[level] = parseItems(items)
				}
				byRole[role] = byLevel
			}
			kits[klass] = byRole
		}
	})
}

func parseItems(items []any) []Kit {
	out := make([]Kit, 0, len(items))
	for _, itemVal := range items {
		item, ok := pyjson.Object(itemVal)
		if !ok {
			continue
		}
		var kit Kit
		kit.Armor = pyjson.NumOrZero(item["armor"])
		kit.Entry, _ = pyjson.Int64(item["entry"])
		if i, ok := pyjson.Int64(item["item_class"]); ok && i != 0 {
			kit.ItemClass = &i
		}
		if s, ok := item["name"].(string); ok {
			kit.Name = s
		}
		kit.RequiredLevel = pyjson.NumOrZero(item["required_level"])
		if s, ok := item["slot"].(string); ok {
			kit.Slot = s
		}
		if i, ok := pyjson.Int64(item["subclass"]); ok {
			kit.Subclass = &i
		}
		out = append(out, kit)
	}
	return out
}

// LoadKits returns the embedded gear kits (class -> role -> level -> items).
func LoadKits() Kits {
	load()
	return kits
}

// RoleOf is "tank" for a forced tank role, else "dps".
func RoleOf(player map[string]any) string {
	if role, ok := player["forced_role"].(string); ok && role == "tank" {
		return "tank"
	}
	return "dps"
}

// PickKit returns the kit of the player's class and role for its level, or the reason there is
// none: the highest kit level not above the player's level, at most MaxLevelGap below it.
func PickKit(player map[string]any, kitSets ...Kits) (items []Kit, kitLevel int64, why string) {
	load()
	table := kits
	if len(kitSets) > 0 {
		table = kitSets[0]
	}
	klass := ""
	if v, ok := player["class"]; ok && v != nil {
		klass = strings.ToLower(pyjson.Str(v))
	}
	role := RoleOf(player)
	levelVal, ok := player["level"]
	if !ok || levelVal == nil {
		return nil, 0, "player.level missing: no gear kit"
	}
	level64, ok := toInt(levelVal)
	if !ok {
		return nil, 0, "player.level missing: no gear kit"
	}
	byLevel := table[klass][role]
	var levels []int64
	for key := range byLevel {
		if n, err := strconv.ParseInt(key, 10, 64); err == nil && n <= level64 {
			levels = append(levels, n)
		}
	}
	if len(levels) == 0 {
		return nil, 0, fmt.Sprintf("no %s %s gear kit for level %d (data/gear_kits.json; build_data.py makes it)",
			orQ(klass), role, level64)
	}
	sort.Slice(levels, func(i, j int) bool { return levels[i] < levels[j] })
	kitLevel = levels[len(levels)-1]
	if level64-kitLevel > MaxLevelGap {
		return nil, 0, fmt.Sprintf("no %s %s gear kit within %d levels of %d (nearest is level %d)",
			klass, role, MaxLevelGap, level64, kitLevel)
	}
	items = byLevel[strconv.FormatInt(kitLevel, 10)]
	if len(items) == 0 {
		return nil, 0, fmt.Sprintf("the %s %s kit for level %d is empty", klass, role, kitLevel)
	}
	return items, kitLevel, ""
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// Proficiency maps (item class, subclass) to the proficiency spell a kit item needs. Cloth
// (class 4, subclass 1) is known by every class and is not listed.
var Proficiency = map[[2]int64]int64{
	{4, 2}:  9077,  // leather
	{4, 3}:  8737,  // mail
	{4, 4}:  750,   // plate
	{4, 6}:  9116,  // shield
	{2, 0}:  196,   // one-handed axe
	{2, 1}:  197,   // two-handed axe
	{2, 2}:  264,   // bow
	{2, 3}:  266,   // gun
	{2, 4}:  198,   // one-handed mace
	{2, 5}:  199,   // two-handed mace
	{2, 6}:  200,   // polearm
	{2, 7}:  201,   // one-handed sword
	{2, 8}:  202,   // two-handed sword
	{2, 10}: 227,   // staff
	{2, 13}: 15590, // fist weapon
	{2, 15}: 1180,  // dagger
	{2, 16}: 2567,  // thrown
	{2, 18}: 5011,  // crossbow
	{2, 19}: 5009,  // wand
}

// MissingClass reports the kit entries without item class data: such a kit fails closed, and
// item class 0 is a consumable, never a kit item.
func MissingClass(items []Kit) []Kit {
	var out []Kit
	for _, item := range items {
		if item.ItemClass == nil || *item.ItemClass == 0 || item.Subclass == nil {
			out = append(out, item)
		}
	}
	return out
}

// ProficiencySpells returns the sorted ids of the armor and weapon proficiencies the kit needs.
func ProficiencySpells(items []Kit) []int64 {
	seen := map[int64]bool{}
	for _, item := range items {
		if len(MissingClass([]Kit{item})) > 0 {
			continue
		}
		if spell, ok := Proficiency[[2]int64{*item.ItemClass, *item.Subclass}]; ok {
			seen[spell] = true
		}
	}
	out := make([]int64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// KitEntries returns the item entries of the player's kit.
func KitEntries(player map[string]any) []int64 {
	items, _, _ := PickKit(player)
	out := make([]int64, 0, len(items))
	for _, item := range items {
		out = append(out, item.Entry)
	}
	return out
}

// AllEntries is every entry of every kit (the strip set).
func AllEntries() []int64 {
	load()
	seen := map[int64]bool{}
	for _, roles := range kits {
		for _, levels := range roles {
			for _, items := range levels {
				for _, item := range items {
					seen[item.Entry] = true
				}
			}
		}
	}
	out := make([]int64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// toInt is Python's int(v) for a number or a numeric string.
func toInt(v any) (int64, bool) {
	if i, ok := pyjson.Int64(v); ok {
		return i, true
	}
	if f, ok := pyjson.Float(v); ok {
		return int64(f), true
	}
	return 0, false
}
