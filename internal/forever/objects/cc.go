package objects

import (
	"math"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/foreverspells"
)

// ccMechanics are the SpellCategories.Mechanic values that crowd-control a unit (ticket #93 D15): sleep, freeze,
// knockout, polymorph, banish, shackle, turn, sapped (SharedDefines.h:2883-2903: MECHANIC_SLEEP 10, FREEZE 13,
// KNOCKOUT 14, POLYMORPH 17, BANISH 18, SHACKLE 20, TURN 23, SAPPED 30). A stun (12) is not on the list.
var ccMechanics = map[uint8]bool{10: true, 13: true, 14: true, 17: true, 18: true, 20: true, 23: true, 30: true}

// ccNames are the spells counted by name when the table has no mechanic for them, lower case. A name matches
// whole, or followed by a space or a colon ("Polymorph: Sheep", "Freezing Trap Effect").
var ccNames = []string{
	"polymorph", "sap", "shackle undead", "hibernate", "banish", "freezing trap", "turn undead", "wyvern sting",
	"seduction",
}

// neverCC are the spells D15 excludes although the table gives them a crowd-control mechanic: Gouge carries mechanic
// 14 (knockout) in the generated data.
var neverCC = []string{"gouge"}

// nameMatches reports whether name is word or word followed by a space or a colon.
func nameMatches(name, word string) bool {
	if !strings.HasPrefix(name, word) {
		return false
	}
	if len(name) == len(word) {
		return true
	}
	c := name[len(word)]
	return c == ' ' || c == ':'
}

func nameInList(name string, list []string) bool {
	for _, w := range list {
		if nameMatches(name, w) {
			return true
		}
	}
	return false
}

// CrowdControlled reports whether any of auras crowd-controls its unit (D15). The mechanic comes from the spell
// table when the table has a row with one; a row without a mechanic and an id the table lacks fall back to the name
// list (the table's name, else nameOf's). Gouge never counts. t and nameOf may be nil.
func CrowdControlled(auras []Aura, t *foreverspells.Table, nameOf func(int32) string) bool {
	for _, a := range auras {
		var (
			name string
			row  foreverspells.Spell
			ok   bool
		)
		if t != nil && a.SpellID > 0 {
			row, ok = t.Lookup(uint32(a.SpellID))
			name = t.Name(uint32(a.SpellID))
		}
		if name == "" && nameOf != nil {
			name = nameOf(a.SpellID)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if nameInList(name, neverCC) {
			continue
		}
		if ok && row.Mechanic != 0 {
			if ccMechanics[row.Mechanic] {
				return true
			}
			continue
		}
		if nameInList(name, ccNames) {
			return true
		}
	}
	return false
}

// The kinds DynamicObjects lists.
const (
	// KindDynamicObject is a TYPEID_DYNAMICOBJECT (a persistent area aura: Rain of Fire, a consecration, a fire patch).
	KindDynamicObject = "dynamic object"
	// KindAreaTrigger is a TYPEID_AREATRIGGER whose create named a spell (a ground spell of this core: Blizzard,
	// Flamestrike; Spell::EffectCreateAreaTrigger).
	KindAreaTrigger = "area trigger"
)

// DynamicObject is the area a ground spell leaves (Blizzard, Rain of Fire, a consecration, a fire patch under a boss):
// the spell, its caster, the radius and the distance from the asker. This core sends the two spell shapes as two object
// types (see Kind): a dynamic object for a persistent area aura, an area trigger for SPELL_EFFECT_CREATE_AREATRIGGER.
type DynamicObject struct {
	GUID    wire.GUID
	Kind    string
	SpellID int32
	Caster  wire.GUID
	Radius  float32
	X, Y, Z float32
	// Distance is the flat distance from the asker to the centre, -1 while the object has no position.
	Distance float32
}

// DynamicObjects lists the ground-spell areas the model holds, nearest first to (x, y): every dynamic object, and every
// area trigger whose create carried a spell (a portal trigger a create ever shows would carry none and is left out).
// The ones without a position (no packet ever gave them one) come last with distance -1.
func (w *World) DynamicObjects(x, y float32) []DynamicObject {
	w.mu.RLock()
	out := make([]DynamicObject, 0, 8)
	for _, o := range w.objects {
		var kind string
		switch {
		case o.Info.HasDynamicObject || o.Type == TypeDynamicObject:
			kind = KindDynamicObject
		case o.Info.HasAreaTrigger && o.Info.SpellID != 0:
			kind = KindAreaTrigger
		default:
			continue
		}
		d := DynamicObject{GUID: o.GUID, Kind: kind, SpellID: o.Info.SpellID, Caster: o.Info.Caster,
			Radius: o.Info.DynRadius, X: o.X, Y: o.Y, Z: o.Z, Distance: -1}
		if o.Positioned {
			d.Distance = float32(math.Hypot(float64(o.X-x), float64(o.Y-y)))
		}
		out = append(out, d)
	}
	w.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Distance < 0) != (out[j].Distance < 0) {
			return out[j].Distance < 0
		}
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		if out[i].GUID.Low != out[j].GUID.Low {
			return out[i].GUID.Low < out[j].GUID.Low
		}
		return out[i].GUID.High < out[j].GUID.High
	})
	return out
}
