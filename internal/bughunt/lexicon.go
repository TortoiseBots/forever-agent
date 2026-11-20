package bughunt

// The spell names and dispel types the combat rules read come from live-checked data files, never from this
// package: `--names` and `--dispel` are required inputs for the rules that read a name, and a file that cannot be
// read or does not parse is an error naming it (and the line). An id the names file does not name is `spell <id>`
// and never matches a buff, AoE or heal name; an id the dispel file does not list is not a known debuff.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// dispelTypes are the dispel types the core's spell_template column and the boss tactics name.
var dispelTypes = map[string]bool{"magic": true, "curse": true, "disease": true, "poison": true}

// Reading a name rule's text: the Python checker's own patterns, minus its non-capturing groups (Go's RE2 has none).

// healSpellRE is the Python's HEAL_SPELL_RE: a spell that heals, shields, dispels or resurrects.
var healSpellRE = regexp.MustCompile(`(?i)heal|renew|regrowth|rejuvenation|holy light|flash of light|prayer of healing|healing touch|lifebloom|` +
	`swiftmend|holy shock|lay on hands|power word: shield|resurrect|redemption|ancestral spirit|` +
	`cleanse|dispel|purify|abolish|remove curse|rebirth|innervate|bandage`)

// buffRE is the Python's BUFF_RE: a buff whose repeated casts are a flip-flop.
var buffRE = regexp.MustCompile(`(?i)^(greater )?blessing of|power word: fortitude|prayer of (fortitude|spirit|shadow)|arcane (intellect|brilliance)|` +
	`mark of the wild|gift of the wild|thorns|battle shout|commanding shout|demon (skin|armor)|(frost|ice|mage|fire) armor|` +
	`(fire|lightning|water|earth) shield|inner fire|divine spirit|shadow protection|trueshot aura|aspect of|` +
	`(devotion|retribution|concentration|sanctity|fire resistance|frost resistance|shadow resistance) aura|righteous fury|` +
	`seal of|windfury totem|stoneskin|grace of air|strength of earth|mana spring|spirit of the ancients|` +
	`dampen magic|amplify magic|sense demons|presence of mind|slice and dice`)

// aoeRE is the Python's AOE_RE: a spell that leaves a damaging area effect.
var aoeRE = regexp.MustCompile(`(?i)rain of fire|flamestrike|blizzard|fire nova|flame breath|hellfire|consecration|volley|` +
	`swirl|cloud|earthquake|fire ?field|brimstone|lava|geyser|poison ?pool|spore|inferno|flame wave|` +
	`immolation|shadowflame`)

// idleActs are the Python's IDLE_ACTS: an action whose prefix says the bot is waiting, not working.
var idleActs = []string{"follow", "stay", "add gathering", "check values", "formation", "reset objective",
	"wait for attack", "select new", "nothing"}

// eatRE is the Python's EAT_RE.
var eatRE = regexp.MustCompile(`(?i)^(food|drink|eat)\b`)

// dispelClasses is the Python's R15_CLASSES: who can remove each dispel type in this core's scripts.
var dispelClasses = map[string][]string{
	"magic":   {"priest", "paladin"},
	"curse":   {"druid", "mage"},
	"disease": {"priest", "paladin", "shaman"},
	"poison":  {"druid", "paladin", "shaman"},
}

// LoadNames reads the live-checked spell names: a JSON object of id -> name, the `by_id` object of the trials
// spells.json, or either with objects as values (`{"name": ...}`). An id whose text is not a number, or a value
// that is not a name, is an error naming the file and the line.
func LoadNames(path string) (map[int]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	obj, err := jsonObject(b, path)
	if err != nil {
		return nil, err
	}
	byID := obj
	if raw, ok := obj["by_id"]; ok {
		nested, err := rawObject(raw, path, b, "by_id")
		if err != nil {
			return nil, err
		}
		byID = nested
	}
	names := make(map[int]string, len(byID))
	for key, raw := range byID {
		id, err := spellID(key)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", path, lineOfKey(b, key), err)
		}
		name, err := rawName(raw)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: spell %d: %v", path, lineOfKey(b, key), id, err)
		}
		names[id] = name
	}
	return names, nil
}

// LoadDispel reads the live-checked dispel types: a JSON object of id -> type (`magic`, `curse`, `disease`,
// `poison`, or `never` for a debuff a fight must keep), or the same with objects as values (`{"dispel": ...}` or
// `{"type": ...}`), or the core's numeric spelling of the spell_template column (0 never, 1 magic, 2 curse,
// 3 disease, 4 poison). An id without a usable type is an error naming the file and the line.
func LoadDispel(path string) (map[int]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	obj, err := jsonObject(b, path)
	if err != nil {
		return nil, err
	}
	out := make(map[int]string, len(obj))
	for key, raw := range obj {
		id, err := spellID(key)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", path, lineOfKey(b, key), err)
		}
		typ, err := rawDispel(raw)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: spell %d: %v", path, lineOfKey(b, key), id, err)
		}
		out[id] = typ
	}
	return out, nil
}

// jsonObject decodes a JSON object; the error names the file and the line for a syntax or type error.
func jsonObject(b []byte, path string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, jsonInputError(b, path, err)
	}
	if obj == nil {
		return nil, fmt.Errorf("%s: not a JSON object", path)
	}
	return obj, nil
}

// rawObject decodes one object-valued field.
func rawObject(raw json.RawMessage, path string, b []byte, key string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("%s:%d: %s is not an object", path, lineOfKey(b, key), key)
	}
	if obj == nil {
		return nil, fmt.Errorf("%s:%d: %s is not an object", path, lineOfKey(b, key), key)
	}
	return obj, nil
}

// rawName reads a names value: the name text, or an object with a `name`.
func rawName(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("empty name")
		}
		return s, nil
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return "", fmt.Errorf("not a name")
	}
	if v, ok := row["name"].(string); ok && strings.TrimSpace(v) != "" {
		return v, nil
	}
	return "", fmt.Errorf("not a name")
}

// rawDispel reads a dispel value: a type text, the core's number, or an object with a `dispel`/`type` of either.
func rawDispel(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return dispelWord(s)
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return dispelNumber(n)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err == nil {
		for _, key := range []string{"dispel", "type"} {
			switch v := row[key].(type) {
			case string:
				return dispelWord(v)
			case float64:
				return dispelNumber(v)
			}
		}
	}
	return "", fmt.Errorf("no dispel type")
}

// dispelWord normalises a type text; `never` is a debuff that must stay (the Python's must-stay set).
func dispelWord(s string) (string, error) {
	w := strings.ToLower(strings.TrimSpace(s))
	if w == "never" || w == "none" {
		return "never", nil
	}
	if dispelTypes[w] {
		return w, nil
	}
	return "", fmt.Errorf("unknown dispel type %.20q", s)
}

// dispelNumber reads the core's spell_template.dispel column: 1 magic, 2 curse, 3 disease, 4 poison, 0 never.
func dispelNumber(n float64) (string, error) {
	switch int(n) {
	case 0:
		return "never", nil
	case 1:
		return "magic", nil
	case 2:
		return "curse", nil
	case 3:
		return "disease", nil
	case 4:
		return "poison", nil
	}
	return "", fmt.Errorf("unknown dispel number %v", n)
}

// spellID parses an id key: decimal, or 0x-prefixed hex.
func spellID(key string) (int, error) {
	k := strings.TrimSpace(key)
	if k == "" {
		return 0, fmt.Errorf("empty spell id")
	}
	base, digits := 10, k
	if strings.HasPrefix(k, "0x") || strings.HasPrefix(k, "0X") {
		base, digits = 16, k[2:]
	}
	id, err := strconv.ParseUint(digits, base, 32)
	if err != nil {
		return 0, fmt.Errorf("not a spell id: %.20q", key)
	}
	return int(id), nil
}

// jsonInputError is a decode failure as a message naming the file and, for a syntax or type error, the line.
func jsonInputError(b []byte, path string, err error) error {
	switch e := err.(type) {
	case *json.SyntaxError:
		return fmt.Errorf("%s:%d: %v", path, lineOfOffset(b, e.Offset), err)
	case *json.UnmarshalTypeError:
		return fmt.Errorf("%s:%d: %v", path, lineOfOffset(b, e.Offset), err)
	}
	return fmt.Errorf("%s: %v", path, err)
}

// lineOfOffset is the 1-based line of a byte offset.
func lineOfOffset(b []byte, off int64) int {
	if off < 0 {
		return 1
	}
	if off > int64(len(b)) {
		off = int64(len(b))
	}
	return 1 + bytes.Count(b[:off], []byte("\n"))
}

// lineOfKey is the 1-based line where an object key first appears; 1 when the text does not hold it.
func lineOfKey(b []byte, key string) int {
	i := bytes.Index(b, []byte(`"`+key+`"`))
	if i < 0 {
		return 1
	}
	return lineOfOffset(b, int64(i))
}
