package bughunt

// The per-dungeon trash tactics file: what each trash creature casts and what the party should do about it.
// Module issue TortoiseBotsForever#23 wants this generated from the core's creature data; the bug hunter reads a
// hand-written file in the same shape until that generator exists. The format is documented in
// docs/forever/TRASH-TACTICS-NOTES.md; an example with two hand-made entries is
// testdata/tactics-example.json. A file that cannot be read or does not parse is an error naming it (and the
// line), as the other loaders do: a dropped entry would read as a clean run.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// tacticsCCKinds are the crowd-control kinds a tactics file may name, on a CC spell (cc_spells) and on a
// creature's immunities (cc_immune). A kind outside this set is a hostile or mistyped file, never a silent miss.
var tacticsCCKinds = map[string]bool{
	"sap":       true,
	"polymorph": true,
	"fear":      true,
	"root":      true,
	"stun":      true,
	"shackle":   true,
	"sleep":     true,
}

// Tactics is one dungeon's trash tactics: creature entries keyed by creature entry, and the CC spells keyed by
// spell id. Priority 1 is killed first; a higher number dies later; 0 means unordered (rule 19 skips it).
type Tactics struct {
	Dungeon   string
	Creatures map[uint32]*TacticEntry
	CCSpells  map[int][]string
}

// TacticEntry is one creature entry's tactics.
type TacticEntry struct {
	Entry         uint32
	Name          string
	MustInterrupt []int
	CCImmune      map[string]bool
	MustDispel    []TacticDispel
	Priority      int
}

// TacticDispel is one debuff a creature applies that the party must remove: the spell, its dispel type (the
// lexicon's words) and how long it may stay, in seconds.
type TacticDispel struct {
	Spell   int
	Type    string
	WithinS float64
}

// Creature returns the entry's tactics, false when the file does not list it. An unlisted entry is unknown,
// never a finding.
func (t *Tactics) Creature(entry uint32) (*TacticEntry, bool) {
	if t == nil {
		return nil, false
	}
	ce, ok := t.Creatures[entry]
	return ce, ok
}

// interrupts says whether the spell is listed as a must-interrupt on the entry.
func (ce *TacticEntry) interrupts(spell int) bool {
	for _, id := range ce.MustInterrupt {
		if id == spell {
			return true
		}
	}
	return false
}

// immuneTo says whether the entry is listed as immune to the CC kind.
func (ce *TacticEntry) immuneTo(kind string) bool {
	return ce.CCImmune[strings.ToLower(kind)]
}

// dispelFor returns the strictest listing of the spell on any entry: the smallest within_s and its type and
// entry name. One application is judged once, so one listing answers for all of them.
func (t *Tactics) dispelFor(spell int) (within float64, typ, entryName string, ok bool) {
	if t == nil {
		return 0, "", "", false
	}
	var entries []*TacticEntry
	for _, ce := range t.Creatures {
		for _, d := range ce.MustDispel {
			if d.Spell == spell {
				entries = append(entries, ce)
				break
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Entry < entries[j].Entry })
	for _, ce := range entries {
		for _, d := range ce.MustDispel {
			if d.Spell != spell {
				continue
			}
			if !ok || d.WithinS < within {
				within, typ, entryName, ok = d.WithinS, d.Type, ce.Name, true
			}
		}
	}
	return within, typ, entryName, ok
}

// LoadTactics reads a per-dungeon tactics file: a JSON object with a `dungeon` name, a `cc_spells` object of
// spell id to CC kind(s), and a `creatures` object of creature entry to its row. Ids and entries are decimal or
// 0x-prefixed hex, as the lexicon reads them; a creature row carries `name`, `must_interrupt` spell ids,
// `cc_immune` kinds, `must_dispel` rows (`spell`, `type`, `within_s`) and `priority` (1 killed first, 0
// unordered). An empty creatures object, an unknown CC kind, a must_dispel of type never, a non-positive spell,
// entry or within_s, or a negative priority is an error naming the file and the line.
func LoadTactics(path string) (*Tactics, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	obj, err := jsonObject(b, path)
	if err != nil {
		return nil, err
	}
	out := &Tactics{Creatures: map[uint32]*TacticEntry{}, CCSpells: map[int][]string{}}
	if raw, ok := obj["dungeon"]; ok {
		var name string
		if err := json.Unmarshal(raw, &name); err == nil {
			out.Dungeon = name
		}
	}
	if raw, ok := obj["cc_spells"]; ok {
		spells, err := rawObject(raw, path, b, "cc_spells")
		if err != nil {
			return nil, err
		}
		for _, key := range sortedKeys(spells) {
			id, err := spellID(key)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", path, lineOfKey(b, key), err)
			}
			if id <= 0 {
				return nil, fmt.Errorf("%s:%d: spell %d: not a spell id", path, lineOfKey(b, key), id)
			}
			kinds, err := tacticsKindList(spells[key])
			if err != nil {
				return nil, fmt.Errorf("%s:%d: cc spell %d: %v", path, lineOfKey(b, key), id, err)
			}
			out.CCSpells[id] = kinds
		}
	}
	rawCreatures, ok := obj["creatures"]
	if !ok {
		return nil, fmt.Errorf("%s: no creatures", path)
	}
	creatures, err := rawObject(rawCreatures, path, b, "creatures")
	if err != nil {
		return nil, err
	}
	if len(creatures) == 0 {
		return nil, fmt.Errorf("%s: no creatures", path)
	}
	for _, key := range sortedKeys(creatures) {
		entry, err := spellID(key)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", path, lineOfKey(b, key), err)
		}
		if entry <= 0 {
			return nil, fmt.Errorf("%s:%d: entry %d: not a creature entry", path, lineOfKey(b, key), entry)
		}
		ce, err := tacticsCreature(creatures[key])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: entry %d: %v", path, lineOfKey(b, key), entry, err)
		}
		ce.Entry = uint32(entry)
		out.Creatures[uint32(entry)] = ce
	}
	return out, nil
}

// tacticsCreature reads one creature row.
func tacticsCreature(raw json.RawMessage) (*TacticEntry, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("not a creature row")
	}
	ce := &TacticEntry{CCImmune: map[string]bool{}}
	if raw, ok := obj["name"]; ok {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return nil, fmt.Errorf("name: not a name")
		}
		ce.Name = name
	}
	if raw, ok := obj["must_interrupt"]; ok {
		ids, err := tacticsSpellIDs(raw)
		if err != nil {
			return nil, fmt.Errorf("must_interrupt: %v", err)
		}
		ce.MustInterrupt = ids
	}
	if raw, ok := obj["cc_immune"]; ok {
		kinds, err := tacticsKindList(raw)
		if err != nil {
			return nil, fmt.Errorf("cc_immune: %v", err)
		}
		for _, k := range kinds {
			ce.CCImmune[k] = true
		}
	}
	if raw, ok := obj["must_dispel"]; ok {
		var rows []json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("must_dispel: not a list")
		}
		for i, row := range rows {
			d, err := tacticsDispel(row)
			if err != nil {
				return nil, fmt.Errorf("must_dispel[%d]: %v", i, err)
			}
			ce.MustDispel = append(ce.MustDispel, d)
		}
	}
	if raw, ok := obj["priority"]; ok {
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, fmt.Errorf("priority: not a number")
		}
		if n < 0 || n != float64(int(n)) {
			return nil, fmt.Errorf("priority %v: not a non-negative whole number", n)
		}
		ce.Priority = int(n)
	}
	return ce, nil
}

// tacticsDispel reads one must_dispel row: the spell, its dispel type and how long it may stay.
func tacticsDispel(raw json.RawMessage) (TacticDispel, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return TacticDispel{}, fmt.Errorf("not a dispel row")
	}
	var d TacticDispel
	if raw, ok := obj["spell"]; ok {
		id, err := tacticsSpellID(raw)
		if err != nil {
			return TacticDispel{}, fmt.Errorf("spell: %v", err)
		}
		d.Spell = id
	} else {
		return TacticDispel{}, fmt.Errorf("spell: missing")
	}
	if d.Spell <= 0 {
		return TacticDispel{}, fmt.Errorf("spell %d: not a spell id", d.Spell)
	}
	typeRaw, ok := obj["type"]
	if !ok {
		typeRaw, ok = obj["dispel"]
	}
	if !ok {
		return TacticDispel{}, fmt.Errorf("spell %d: no dispel type", d.Spell)
	}
	var typ string
	if err := json.Unmarshal(typeRaw, &typ); err != nil {
		return TacticDispel{}, fmt.Errorf("spell %d: no dispel type", d.Spell)
	}
	w, err := dispelWord(typ)
	if err != nil {
		return TacticDispel{}, err
	}
	if w == "never" {
		return TacticDispel{}, fmt.Errorf("spell %d: a must-dispel of type never stays", d.Spell)
	}
	d.Type = w
	if raw, ok := obj["within_s"]; ok {
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return TacticDispel{}, fmt.Errorf("spell %d: within_s is not a number", d.Spell)
		}
		d.WithinS = n
	} else {
		return TacticDispel{}, fmt.Errorf("spell %d: no within_s", d.Spell)
	}
	if d.WithinS <= 0 {
		return TacticDispel{}, fmt.Errorf("spell %d: within_s %v is not positive", d.Spell, d.WithinS)
	}
	return d, nil
}

// tacticsKindList reads a CC kind or list of kinds: each must be a known kind, in any letter case.
func tacticsKindList(raw json.RawMessage) ([]string, error) {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		kinds, err := tacticsKinds([]string{one})
		if err != nil {
			return nil, err
		}
		return kinds, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("not a CC kind or list of kinds")
	}
	return tacticsKinds(many)
}

// tacticsKinds lower-cases the kinds and rejects the unknown ones.
func tacticsKinds(kinds []string) ([]string, error) {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		w := strings.ToLower(strings.TrimSpace(k))
		if !tacticsCCKinds[w] {
			return nil, fmt.Errorf("unknown CC kind %.20q", k)
		}
		out = append(out, w)
	}
	return out, nil
}

// tacticsSpellIDs reads a list of spell ids: JSON numbers or decimal/0x-prefixed strings, each positive.
func tacticsSpellIDs(raw json.RawMessage) ([]int, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("not a list of spell ids")
	}
	var out []int
	for i, row := range rows {
		id, err := tacticsSpellID(row)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %v", i, err)
		}
		if id <= 0 {
			return nil, fmt.Errorf("[%d]: spell %d is not a spell id", i, id)
		}
		out = append(out, id)
	}
	return out, nil
}

// tacticsSpellID reads one spell id: a JSON number or a decimal/0x-prefixed string, as the lexicon reads keys.
func tacticsSpellID(raw json.RawMessage) (int, error) {
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		if n != float64(int(n)) {
			return 0, fmt.Errorf("not a spell id: %v", n)
		}
		return int(n), nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("not a spell id")
	}
	return spellID(s)
}
