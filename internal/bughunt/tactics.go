package bughunt

// The per-dungeon trash tactics file: what each trash creature casts and what the party should do about it.
// Module issue TortoiseBotsForever#23 wants this generated from the core's creature data; the bug hunter reads a
// hand-written file in the same shape until that generator exists. The format is documented in
// docs/forever/TRASH-TACTICS-NOTES.md; an example with two hand-made entries is
// testdata/tactics-example.json. A file that cannot be read or does not parse is an error naming it (and the
// line), as the other loaders do: a dropped entry would read as a clean run.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
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

// dispelListing is one entry's must_dispel row: the spell's limit and type, and the entry that set it.
type dispelListing struct {
	TacticDispel
	Entry uint32
}

// dispelListings indexes the file's must_dispel rows by spell, entries in ascending order. Built once per rule, so
// the check walks the file's rows and never re-scans every creature for every row of the log.
func (t *Tactics) dispelListings() map[int][]dispelListing {
	out := map[int][]dispelListing{}
	if t == nil {
		return out
	}
	for _, entry := range sortedCreatureEntries(t.Creatures) {
		for _, d := range t.Creatures[entry].MustDispel {
			out[d.Spell] = append(out[d.Spell], dispelListing{TacticDispel: d, Entry: entry})
		}
	}
	return out
}

// dispelByEntry indexes the must_dispel rows by creature entry and spell: the caster's own limit.
func (t *Tactics) dispelByEntry() map[uint32]map[int]TacticDispel {
	out := map[uint32]map[int]TacticDispel{}
	if t == nil {
		return out
	}
	for entry, ce := range t.Creatures {
		if len(ce.MustDispel) == 0 {
			continue
		}
		out[entry] = map[int]TacticDispel{}
		for _, d := range ce.MustDispel {
			out[entry][d.Spell] = d
		}
	}
	return out
}

// sortedCreatureEntries are the file's creature entries in ascending order, so every walk of the file is stable.
func sortedCreatureEntries(creatures map[uint32]*TacticEntry) []uint32 {
	out := make([]uint32, 0, len(creatures))
	for entry := range creatures {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// dispelFor returns the strictest listing of the spell on any entry: the smallest within_s and its type and
// entry name. One application is judged once, so one listing answers for all of them.
func (t *Tactics) dispelFor(spell int) (within float64, typ, entryName string, ok bool) {
	list := t.dispelListings()[spell]
	for _, d := range list {
		if !ok || d.WithinS < within {
			within, typ, ok = d.WithinS, d.Type, true
			if ce, found := t.Creature(d.Entry); found {
				entryName = ce.Name
			}
		}
	}
	return within, typ, entryName, ok
}

// The loader's bounds: a hand-written tactics file is a few KiB, and a hostile or mistyped one must fail fast
// instead of tying the hunter up. Each error names the cap.
const (
	maxTacticsFileBytes  = 1 << 20 // 1 MiB
	maxTacticsCreatures  = 512
	maxTacticsCCSpells   = 512
	maxTacticsInterrupts = 64
	maxTacticsDispels    = 64
	maxTacticsKinds      = 8
)

// tacticsKeys are the fields each object level accepts, so a typo (`must_interupt`, `cc_imune`) is an error naming
// the file and the line, not a silently disabled check. `comment` is the example's own note; a dispel row accepts
// `type` and its `dispel` alias.
var tacticsKeys = map[string]map[string]bool{
	"": {"dungeon": true, "cc_spells": true, "creatures": true, "comment": true},
	"creature": {"name": true, "must_interrupt": true, "cc_immune": true, "must_dispel": true,
		"priority": true},
	"dispel": {"spell": true, "type": true, "dispel": true, "within_s": true},
}

// tacticsStrict checks every object level for unknown and duplicate fields before anything is decoded from it.
func tacticsStrict(raw json.RawMessage, b []byte, path, where string) error {
	keys, err := objectKeys(raw)
	if err != nil {
		return fmt.Errorf("%s:%d: %v", path, lineOfKey(b, where), err)
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if !tacticsKeys[where][key] {
			return fmt.Errorf("%s:%d: unknown field %q in %s", path, lineOfKey(b, key), key, whereName(where))
		}
		if seen[key] && !(where == "dispel" && (key == "type" || key == "dispel")) {
			return fmt.Errorf("%s:%d: duplicate field %q in %s", path, lineOfKey(b, key), key, whereName(where))
		}
		seen[key] = true
	}
	if where == "dispel" && seen["type"] && seen["dispel"] {
		return fmt.Errorf("%s:%d: a dispel row names both type and dispel", path, lineOfKey(b, "dispel"))
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("%s: not a JSON object", path)
	}
	switch where {
	case "":
		if rawCreatures, ok := obj["creatures"]; ok {
			creatures, err := tacticsMap(rawCreatures)
			if err != nil {
				return fmt.Errorf("%s:%d: creatures is not an object", path, lineOfKey(b, "creatures"))
			}
			for _, key := range sortedKeys(creatures) {
				if err := tacticsStrict(creatures[key], b, path, "creature"); err != nil {
					return err
				}
			}
		}
	case "creature":
		if rawDispel, ok := obj["must_dispel"]; ok {
			var rows []json.RawMessage
			if err := json.Unmarshal(rawDispel, &rows); err == nil {
				for _, row := range rows {
					if err := tacticsStrict(row, b, path, "dispel"); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// whereName is an object level in words.
func whereName(where string) string {
	switch where {
	case "creature":
		return "a creature row"
	case "dispel":
		return "a must_dispel row"
	}
	return "the tactics file"
}

// objectKeys lists an object's keys in file order, duplicates included (json.Unmarshal into a map would drop them).
func objectKeys(raw json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("not a JSON object")
	}
	var keys []string
	for dec.More() {
		ktok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := ktok.(string)
		if !ok {
			return nil, fmt.Errorf("not a JSON object")
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// tacticsMap decodes an object-valued field into its raw members.
func tacticsMap(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("not an object")
	}
	return obj, nil
}

// tacticsEntryID reads a creature entry: a JSON number or a decimal/0x-prefixed string, 1..MaxUint32. Entries are
// uint32 in full, and one resolved entry has one key: a decimal and a hex spelling of the same value collide and
// are an error.
func tacticsEntryID(raw json.RawMessage) (uint32, error) {
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		if n != math.Trunc(n) || n <= 0 || n > math.MaxUint32 {
			return 0, fmt.Errorf("entry %v is not a creature entry", n)
		}
		return uint32(n), nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("not a creature entry")
	}
	return entryKey(s)
}

// entryKey reads an entry key: decimal, or 0x-prefixed hex, as a full uint32 above zero.
func entryKey(key string) (uint32, error) {
	k := strings.TrimSpace(key)
	if k == "" {
		return 0, fmt.Errorf("empty entry")
	}
	base, digits := 10, k
	if strings.HasPrefix(k, "0x") || strings.HasPrefix(k, "0X") {
		base, digits = 16, k[2:]
	}
	entry, err := strconv.ParseUint(digits, base, 32)
	if err != nil || entry == 0 {
		return 0, fmt.Errorf("not a creature entry: %.20q", key)
	}
	return uint32(entry), nil
}

// LoadTactics reads a per-dungeon tactics file: a JSON object with a `dungeon` name, an optional `comment`, a
// `cc_spells` object of spell id to CC kind(s), and a `creatures` object of creature entry to its row. Ids and
// entries are decimal or 0x-prefixed hex, as the lexicon reads them; entries are full uint32 values, and the same
// entry (or spell) spelled two ways is a duplicate, not an overwrite. A creature row carries `name`,
// `must_interrupt` spell ids, `cc_immune` kinds, `must_dispel` rows (`spell`, `type` or its `dispel` alias,
// `within_s`) and `priority` (1 killed first, 0 unordered). Unknown or duplicate fields, a file over the size cap,
// too many creatures, cc spells or list rows, an empty creatures object, an unknown CC kind, a must_dispel of type
// never, a non-positive spell, entry or within_s, or a negative priority is an error naming the file and the line.
func LoadTactics(path string) (*Tactics, error) {
	if st, err := os.Stat(path); err == nil && st.Size() > maxTacticsFileBytes {
		return nil, fmt.Errorf("%s: %d bytes; the tactics file cap is %d", path, st.Size(), maxTacticsFileBytes)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) > maxTacticsFileBytes {
		return nil, fmt.Errorf("%s: %d bytes; the tactics file cap is %d", path, len(b), maxTacticsFileBytes)
	}
	obj, err := jsonObject(b, path)
	if err != nil {
		return nil, err
	}
	if err := tacticsStrict(b, b, path, ""); err != nil {
		return nil, err
	}
	out := &Tactics{Creatures: map[uint32]*TacticEntry{}, CCSpells: map[int][]string{}}
	if raw, ok := obj["dungeon"]; ok {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return nil, fmt.Errorf("%s:%d: dungeon is not a name", path, lineOfKey(b, "dungeon"))
		}
		out.Dungeon = name
	}
	if raw, ok := obj["cc_spells"]; ok {
		spells, err := rawObject(raw, path, b, "cc_spells")
		if err != nil {
			return nil, err
		}
		if len(spells) > maxTacticsCCSpells {
			return nil, fmt.Errorf("%s:%d: %d cc spells; the cap is %d", path, lineOfKey(b, "cc_spells"),
				len(spells), maxTacticsCCSpells)
		}
		spelled := map[int]string{}
		for _, key := range sortedKeys(spells) {
			id, err := spellID(key)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", path, lineOfKey(b, key), err)
			}
			if id <= 0 {
				return nil, fmt.Errorf("%s:%d: spell %d: not a spell id", path, lineOfKey(b, key), id)
			}
			if first, dup := spelled[id]; dup {
				return nil, fmt.Errorf("%s:%d: spell %d is listed twice (%q and %q)", path, lineOfKey(b, key), id, first, key)
			}
			spelled[id] = key
			kinds, err := tacticsKindList(spells[key])
			if err != nil {
				return nil, fmt.Errorf("%s:%d: cc spell %d: %v", path, lineOfKey(b, key), id, err)
			}
			if len(kinds) > maxTacticsKinds {
				return nil, fmt.Errorf("%s:%d: cc spell %d: %d kinds; the cap is %d", path, lineOfKey(b, key),
					id, len(kinds), maxTacticsKinds)
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
	if len(creatures) > maxTacticsCreatures {
		return nil, fmt.Errorf("%s:%d: %d creatures; the cap is %d", path, lineOfKey(b, "creatures"),
			len(creatures), maxTacticsCreatures)
	}
	entryKeys := map[uint32]string{}
	for _, key := range sortedKeys(creatures) {
		entry, err := entryKey(key)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", path, lineOfKey(b, key), err)
		}
		if first, dup := entryKeys[entry]; dup {
			return nil, fmt.Errorf("%s:%d: entry %d is listed twice (%q and %q)", path, lineOfKey(b, key), entry, first, key)
		}
		entryKeys[entry] = key
		ce, err := tacticsCreature(creatures[key])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: entry %d: %v", path, lineOfKey(b, key), entry, err)
		}
		ce.Entry = entry
		out.Creatures[entry] = ce
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
		if len(ids) > maxTacticsInterrupts {
			return nil, fmt.Errorf("must_interrupt: %d spells; the cap is %d", len(ids), maxTacticsInterrupts)
		}
		seen := map[int]bool{}
		for _, id := range ids {
			if seen[id] {
				return nil, fmt.Errorf("must_interrupt: spell %d is listed twice", id)
			}
			seen[id] = true
		}
		ce.MustInterrupt = ids
	}
	if raw, ok := obj["cc_immune"]; ok {
		kinds, err := tacticsKindList(raw)
		if err != nil {
			return nil, fmt.Errorf("cc_immune: %v", err)
		}
		if len(kinds) > maxTacticsKinds {
			return nil, fmt.Errorf("cc_immune: %d kinds; the cap is %d", len(kinds), maxTacticsKinds)
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
		if len(rows) > maxTacticsDispels {
			return nil, fmt.Errorf("must_dispel: %d rows; the cap is %d", len(rows), maxTacticsDispels)
		}
		seen := map[int]bool{}
		for i, row := range rows {
			d, err := tacticsDispel(row)
			if err != nil {
				return nil, fmt.Errorf("must_dispel[%d]: %v", i, err)
			}
			if seen[d.Spell] {
				return nil, fmt.Errorf("must_dispel[%d]: spell %d is listed twice", i, d.Spell)
			}
			seen[d.Spell] = true
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
	if len(many) > maxTacticsKinds {
		return nil, fmt.Errorf("%d kinds; the cap is %d", len(many), maxTacticsKinds)
	}
	return tacticsKinds(many)
}

// tacticsKinds lower-cases the kinds and rejects the unknown ones.
func tacticsKinds(kinds []string) ([]string, error) {
	out := make([]string, 0, len(kinds))
	seen := map[string]bool{}
	for _, k := range kinds {
		w := strings.ToLower(strings.TrimSpace(k))
		if !tacticsCCKinds[w] {
			return nil, fmt.Errorf("unknown CC kind %.20q", k)
		}
		if seen[w] {
			continue
		}
		seen[w] = true
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
