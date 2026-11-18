// Package spells maps spell names to ids (all ranks) and back, from data/spells.json.
package spells

import (
	"strconv"
	"strings"
	"sync"

	"github.com/pjerra/forever-agent/internal/trials/data"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// InterruptFlagDamage is the core's SPELL_INTERRUPT_FLAG_DAMAGE bit
// (SpellDefines.h), the one SpellEffects.cpp tests for a kick.
const InterruptFlagDamage = 0x02

// PreventionSilence is PreventionType SILENCE in the core's spell data.
const PreventionSilence = 1

type spellInfo struct {
	name           string
	castMS         any
	preventionType any
	interruptFlags any
}

type table struct {
	byName map[string][]any
	byID   map[string]spellInfo
}

var (
	once sync.Once
	tab  table
)

func load() *table {
	once.Do(func() {
		tab = table{byName: map[string][]any{}, byID: map[string]spellInfo{}}
		doc, err := pyjson.Unmarshal(data.Read("spells.json"))
		if err != nil {
			return
		}
		root, ok := pyjson.Object(doc)
		if !ok {
			return
		}
		if byName, ok := pyjson.Object(root["by_name"]); ok {
			for name, ids := range byName {
				if list, ok := pyjson.List(ids); ok {
					tab.byName[name] = list
				}
			}
		}
		if byID, ok := pyjson.Object(root["by_id"]); ok {
			for id, info := range byID {
				switch row := info.(type) {
				case map[string]any:
					name := ""
					if s, ok := row["name"].(string); ok {
						name = s
					}
					tab.byID[id] = spellInfo{
						name:           name,
						castMS:         row["cast_ms"],
						preventionType: row["prevention_type"],
						interruptFlags: row["interrupt_flags"],
					}
				case string:
					tab.byID[id] = spellInfo{name: row}
				}
			}
		}
	})
	return &tab
}

// IDsFor returns every spell id of a name (all ranks); an unknown name gives an empty set.
func IDsFor(name string) map[int64]bool {
	t := load()
	ids := t.byName[name]
	if len(ids) == 0 {
		ids = t.byName[strings.ToLower(name)]
	}
	out := make(map[int64]bool, len(ids))
	for _, i := range ids {
		if n, ok := pyjson.Int64(i); ok {
			out[n] = true
		}
	}
	return out
}

// HasID reports whether the id is a known spell id.
func HasID(spellID int64) bool {
	t := load()
	_, ok := t.byID[strconv.FormatInt(spellID, 10)]
	return ok
}

// NameFor returns the spell name for an id; an unknown id gives str(id).
func NameFor(spellID int64) string {
	t := load()
	key := strconv.FormatInt(spellID, 10)
	if info, ok := t.byID[key]; ok {
		if info.name != "" {
			return info.name
		}
		return key
	}
	return key
}

// IsInterruptible is THE definition of an interruptible spell (rules and build_data share
// it): cast time > 0, PreventionType SILENCE and the damage interrupt flag.
func IsInterruptible(castMS, preventionType, interruptFlags any) bool {
	n := func(v any) float64 {
		if f, ok := pyjson.Float(v); ok {
			return f
		}
		return 0.0
	}
	return n(castMS) > 0 && int64(n(preventionType)) == PreventionSilence &&
		(int64(n(interruptFlags))&InterruptFlagDamage) != 0
}

// SpellIsInterruptible is IsInterruptible for a spell id (unknown id -> false).
func SpellIsInterruptible(spellID int64) bool {
	t := load()
	info, ok := t.byID[strconv.FormatInt(spellID, 10)]
	if !ok {
		return false
	}
	return IsInterruptible(info.castMS, info.preventionType, info.interruptFlags)
}
