package runmap

// Death labels from the combat log: a death marker's label gains the last
// damage row on that member before the death. The combat log is optional; a
// missing file leaves every label as it is today.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/bughunt"
)

// damageKinds are the combat-log kinds that wound a member: a death label
// names the last of these on the member before the death. Heals never do, and
// neither does damage on another member.
var damageKinds = map[string]bool{"melee": true, "spell_damage": true, "periodic": true}

// damageWindowS is how far back a damage row may lie to be the death's blow:
// rows older than that belong to an earlier fight, not this death.
const damageWindowS = 10.0

// maxCombatRows bounds the damage rows kept from one combat log (about 40 MB):
// a huge log is read to the cap and the rest is counted as skipped.
var maxCombatRows = 1 << 20

// combatDamage is one damage row on one member, on the trace's clock.
type combatDamage struct {
	target uint64 // GUIDKey of the target's guid: the full 128-bit text, never the low word alone
	t      float64
	melee  bool
	spell  int
	amount int
}

// readCombatDamage reads the optional combat log beside the trace: rows on
// the trace's clock (the row's wall time minus the trace's start), keyed by
// the target's full guid. A missing file is no rows and no error. A line that
// is not a usable damage row is skipped and counted; damage kinds are the
// only rows kept (a heal is ignored, not skipped). Other read errors fail.
func readCombatDamage(path string, t0 float64) ([]combatDamage, int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()
	var out []combatDamage
	skipped := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row bughunt.CombatRow
		if !strings.HasPrefix(line, "{") {
			skipped++
			continue
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			skipped++
			continue
		}
		if !damageKinds[row.Kind] {
			continue
		}
		t, ok := combatTime(row.Time, t0)
		if !ok {
			skipped++
			continue
		}
		if len(out) >= maxCombatRows {
			skipped++
			continue
		}
		out = append(out, combatDamage{target: GUIDKey(row.Target.GUID), t: t,
			melee: row.Kind == "melee", spell: row.SpellID, amount: row.Amount})
	}
	if err := sc.Err(); err != nil {
		return out, skipped, fmt.Errorf("%s:%d: %v", path, n+1, err)
	}
	return out, skipped, nil
}

// combatTime puts a combat row's wall time on the trace's clock: the row's
// epoch seconds minus the trace's start. A row with no readable time, or a
// trace that does not know its start, cannot be placed in time.
func combatTime(wall string, t0 float64) (float64, bool) {
	if t0 <= 0 || wall == "" {
		return 0, false
	}
	for _, layout := range []string{"2006-01-02T15:04:05.000-07:00", time.RFC3339Nano, "2006-01-02T15:04:05-07:00"} {
		if dt, err := time.Parse(layout, wall); err == nil {
			return float64(dt.Unix()) + float64(dt.Nanosecond())/1e9 - t0, true
		}
	}
	return 0, false
}

// deathLabel adds the last damage row's blow to a death marker's label: the
// member's last damage row within the window before the death. A spell row
// stays `spell <id>` (the log carries no names and none are guessed); a melee
// row names no spell. No row on the member in time leaves the label as it is.
func deathLabel(base string, member uint64, at float64, rows []combatDamage) string {
	last := -1
	for i, r := range rows {
		if r.target != member || r.t > at || at-r.t > damageWindowS {
			continue
		}
		if last < 0 || r.t >= rows[last].t {
			last = i
		}
	}
	if last < 0 {
		return base
	}
	r := rows[last]
	if r.melee {
		return fmt.Sprintf("%s died: melee %d", base, r.amount)
	}
	return fmt.Sprintf("%s died: spell %d %d", base, r.spell, r.amount)
}

// guidOfSlot is the guid text of the member in a slot: "" when the slot names
// none. Labels are unique (a slot is never shared), so the scan finds one.
func (p *party) guidOfSlot(slot string) string {
	want := p.bySlot[slot]
	if want == "" {
		return ""
	}
	for g, l := range p.label {
		if l == want {
			return g
		}
	}
	return ""
}
