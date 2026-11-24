package tactics

// The module's boss tactics table read as a file: the rows of ai_boss_tactic, the table the server module's own bots
// run (BossTacticsMgr). A plan's bosses[].tactics block is the agent's and is never exported (planexport/unexpressed.go),
// so a watched run is the module's rows; the check in this package compares a plan's fight steps with them.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// The module's row kinds this package reads. The table's vocabulary is larger (behind, spread, anchor, linked_death,
// ...); a kind this package does not read is kept and ignored by the comparison, which never judges a step on it.
const (
	KindInterrupt      = "interrupt"
	KindKillFirst      = "kill_first"
	KindAvoidCast      = "avoid_cast"
	KindDispelPriority = "dispel_priority"
)

// Limits of a module tactics file: ai_boss_tactic holds a few hundred rows today; a file past these is a wrong file.
const (
	MaxModuleBytes = 4 << 20
	MaxModuleRows  = 20000
)

// ModuleRow is one row of the module's ai_boss_tactic table. A field the check does not read (tactic_mode, window_s,
// the cycle and spot columns, hp gates) is not typed here and is ignored: the table grows columns and an export must
// not be refused for carrying them. Map, Boss and Kind are the row's identity; the primary key adds Target and Spell.
type ModuleRow struct {
	Map      uint32  `json:"map"`
	Boss     uint32  `json:"boss_entry"`
	Kind     string  `json:"kind"`
	Target   uint32  `json:"target_entry"`
	Spell    uint32  `json:"spell_id"`
	Priority int     `json:"priority"`
	Param    float32 `json:"param"`
	Comment  string  `json:"comment"`
}

// Module is the module's rows, indexed by map and boss entry.
type Module struct {
	byMap map[uint32]map[uint32][]ModuleRow
	rows  int
}

// LoadModule reads a module tactics file: a JSON array with one object per ai_boss_tactic row, keyed by its column
// names (map, boss_entry, kind, target_entry, spell_id, priority, param; comment is for a reader, not the check).
// The rows are checked as a whole: a row without a map, a boss entry or a kind, an interrupt row without a spell, a
// kill_first row without a target, a second row with the same key or a field of the wrong type is an error naming the
// file and the row, never a zero that would quietly judge a fight against nothing.
func LoadModule(path string) (*Module, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > MaxModuleBytes {
		return nil, fmt.Errorf("%s: %d bytes, past the %d-byte bound of a module tactics file", path, st.Size(), MaxModuleBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxModuleBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxModuleBytes {
		return nil, fmt.Errorf("%s: past the %d-byte bound of a module tactics file", path, MaxModuleBytes)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: not a JSON array of ai_boss_tactic rows: %v", path, err)
	}
	m := &Module{byMap: map[uint32]map[uint32][]ModuleRow{}}
	seen := map[ModuleRow]bool{}
	for i, elem := range raw {
		var r ModuleRow
		if err := json.Unmarshal(elem, &r); err != nil {
			return nil, fmt.Errorf("%s: row %d: %v", path, i, err)
		}
		r.Kind = strings.ToLower(strings.TrimSpace(r.Kind))
		if err := r.check(); err != nil {
			return nil, fmt.Errorf("%s: row %d: %v", path, i, err)
		}
		key := r
		key.Priority, key.Param, key.Comment = 0, 0, ""
		if seen[key] {
			return nil, fmt.Errorf("%s: row %d: a second row for map %d boss %d kind %q target %d spell %d", path, i, r.Map, r.Boss, r.Kind, r.Target, r.Spell)
		}
		seen[key] = true
		if len(seen) > MaxModuleRows {
			return nil, fmt.Errorf("%s: more than %d rows", path, MaxModuleRows)
		}
		bosses := m.byMap[r.Map]
		if bosses == nil {
			bosses = map[uint32][]ModuleRow{}
			m.byMap[r.Map] = bosses
		}
		bosses[r.Boss] = append(bosses[r.Boss], r)
	}
	m.rows = len(raw)
	return m, nil
}

// check is one row's own shape: what the check cannot read a row without.
func (r ModuleRow) check() error {
	if r.Map == 0 {
		return fmt.Errorf("no map")
	}
	if r.Boss == 0 {
		return fmt.Errorf("no boss_entry")
	}
	if strings.TrimSpace(r.Kind) == "" {
		return fmt.Errorf("no kind")
	}
	switch r.Kind {
	case KindInterrupt:
		if r.Spell == 0 {
			return fmt.Errorf("an interrupt row without spell_id")
		}
	case KindKillFirst:
		if r.Target == 0 {
			return fmt.Errorf("a kill_first row without target_entry")
		}
	case KindAvoidCast:
		if r.Spell == 0 {
			return fmt.Errorf("an avoid_cast row without spell_id")
		}
		if r.Param <= 0 {
			return fmt.Errorf("an avoid_cast row without param (the yards it leaves)")
		}
	}
	if r.Priority < 0 {
		return fmt.Errorf("a negative priority")
	}
	return nil
}

// Rows are the module's rows for one boss of a map, in file order. A boss the file does not name has none.
func (m *Module) Rows(mapID, boss uint32) []ModuleRow {
	if m == nil {
		return nil
	}
	return m.byMap[mapID][boss]
}

// Len is how many rows the file held.
func (m *Module) Len() int {
	if m == nil {
		return 0
	}
	return m.rows
}
