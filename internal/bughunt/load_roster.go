package bughunt

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Roster is what a roster record says about the party: the role and class per member, keyed by the trace's guid
// text. `forever dash-bots --watch --record` writes one record per good read: `{"at": ..., "bots": [...]}`.
type Roster struct {
	Roles   map[string]string
	Classes map[string]string
}

// classNames are the core's class ids as this repo spells them (the roster spells a class either way).
var classNames = map[string]string{
	"1": "warrior", "2": "paladin", "3": "rogue", "4": "priest", "5": "hunter",
	"6": "warlock", "7": "shaman", "8": "mage", "9": "druid", "11": "druid",
}

// LoadRoster reads a roster record: the `--record` file (one `{"at", "bots", ...}` object per line, the last
// record winning), one such object, or an array of bot records. A member is identified by its guid (canonicalised
// to the trace's text) or, without one, by its name.
func LoadRoster(path string) (Roster, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Roster{}, err
	}
	ro := Roster{Roles: map[string]string{}, Classes: map[string]string{}}
	var records []map[string]any
	var single map[string]any
	if err := json.Unmarshal(b, &single); err == nil && single != nil {
		records = []map[string]any{single}
	} else {
		var bots []any
		if err := json.Unmarshal(b, &bots); err == nil {
			records = []map[string]any{{"bots": bots}}
		} else {
			records = readJSONL(path)
		}
	}
	if len(records) == 0 {
		return Roster{}, fmt.Errorf("%s: no roster record", path)
	}
	for _, rec := range records {
		bots, ok := rec["bots"].([]any)
		if !ok {
			continue
		}
		for _, x := range bots {
			bot, ok := x.(map[string]any)
			if !ok {
				continue
			}
			key := guidKey(bot["guid"])
			if key == "" {
				key = textValue(bot["name"])
			}
			if key == "" {
				continue
			}
			if role := strings.ToLower(strings.TrimSpace(textValue(bot["role"]))); role != "" {
				ro.Roles[key] = role
			}
			if class := strings.ToLower(strings.TrimSpace(textValue(bot["class"]))); class != "" {
				if name, ok := classNames[class]; ok {
					class = name
				}
				ro.Classes[key] = class
			}
		}
	}
	return ro, nil
}

// UseRoster is the roster fallback (decision 4): a member whose role or class the frames do not carry takes the
// roster's, and a run with no members at all (a trace with no party rows) takes the roster's members as its
// subjects.
func (r *Run) UseRoster(ro Roster) {
	for g, role := range ro.Roles {
		if r.Roles[g] == "" {
			r.Roles[g] = role
		}
	}
	for g, class := range ro.Classes {
		if r.Classes[g] == "" {
			r.Classes[g] = class
		}
	}
	if len(r.Subjects) == 0 {
		if r.Subjects == nil {
			r.Subjects = map[string]bool{}
		}
		for g := range ro.Roles {
			r.Subjects[g] = true
		}
		for g := range ro.Classes {
			r.Subjects[g] = true
		}
	}
}

// guidKey is a roster guid as the trace's guid text: a number is decimal and becomes hex, a string is hex with or
// without its 0x and is padded.
func guidKey(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		if x < 0 {
			return ""
		}
		return fmt.Sprintf("0x%032X", uint64(x))
	case string:
		return CanonGUID(x)
	}
	return ""
}

// textValue is a JSON string or number as text (a roster spells a guid or a class either way).
func textValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return trimFloat(x)
	case json.Number:
		return x.String()
	case bool:
		return ""
	}
	return ""
}

// trimFloat writes a whole number without its decimal point (a guid or a class id arrives as a JSON number).
func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%v", f)
}
