package tbcmd

// The core GM read-back of pending respawns and instance spawn rules: `.list respawns [range]`
// (cs_list.cpp:650-700) and `.debug instancespawn [groupID|explain]` (cs_debug.cpp:1431-1565).
//
// `.list respawns` prints, per spawn-object type (SpawnData.h:34-41 Creature, Gameobject, AreaTrigger; the titles
// are EnumUtils::ToTitle: "Creature", "Gameobject", "AreaTrigger", enuminfo_SpawnData.cpp), a header line, a
// column header, then one row per pending respawn:
//
//	'Listing %s respawns within %uyd'                               (LANG_LIST_RESPAWNS_RANGE 5077)
//	'Listing %s respawns for %s (zone %u)'                          (LANG_LIST_RESPAWNS_ZONE 5078)
//	'SpawnID | Entry | GridXY| Zone | Respawn time'                  (LANG_LIST_RESPAWNS_LISTHEADER 5079)
//	'<spawnId> | <entry> | [<gridX>,<gridY>] | <zone> (<id>) | <time>[ (inactive)]'
//	                                                                  (cs_list.cpp:690; UI64FMTD | %u | [%02u,%02u] | %s (%u) | %s%s)
//
// The 2017 seed texts for these rows are 'Listing %s respawns within %uyd', 'Listing %s respawns for %s (zone
// %u)', 'SpawnID | Entry | GridXY| Zone | Respawn time' and 'overdue' (the 2017_99_99 world updates name them;
// the handler call sites above fix the shape: header, column header, then UI64FMTD rows). A pending respawn whose
// timer has passed prints the overdue word (currently 'overdue'); otherwise the ShortText duration ('1h2m3s',
// '45s'; Util.cpp:116-165 secsToTimeString). A row whose spawn group is not active carries ' (inactive)'.
//
// `.debug instancespawn` prints, per spawn group the instance script manages, either the compact line
// " - '<name>' (<id>) is [not ]active" or, with `explain`, the group's rule tree (`'-- ...'`, `'| |-- ...'`,
// `'| |=> ...'` lines). The typed read-back keeps the compact facts (active or not) and the raw rule text for
// bug reports; it does not re-decide the rules.
//
// Both are core commands (no token). A `.list respawns` that prints no type section at all is an empty reply,
// not an empty world: the handler always prints one section per type. Proof that "a pack stays dead" is read as
// the absence of its spawn ids from every section.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// RespawnRow is one pending respawn: the spawn id the world will recreate, what it is, where its grid sits, and
// how long until it comes back.
type RespawnRow struct {
	// Kind is the spawn-object title of the section the row came from: Creature, Gameobject or AreaTrigger.
	Kind string
	// SpawnID is the spawn id (UI64FMTD in the core's row).
	SpawnID uint64
	// Entry is the creature or game-object entry.
	Entry uint32
	// GridX and GridY are the map grid the respawn sits on.
	GridX, GridY uint8
	// Zone is the zone name and id the core printed.
	Zone   string
	ZoneID uint32
	// TimeLeft is the printed wait ('45s', '1h2m3s', the overdue word); Overdue says the timer has passed.
	TimeLeft string
	Overdue  bool
	// Inactive says the row carried ' (inactive)': the spawn group is not active.
	Inactive bool
	// Raw is the row's visible text.
	Raw string
}

// RespawnSection is one type's section of a `.list respawns` answer.
type RespawnSection struct {
	Kind string
	// Range is set for the `within <n>yd` form; Zone and ZoneID for the zone form.
	Range    uint32
	HasRange bool
	Zone     string
	ZoneID   uint32
	Rows     []RespawnRow
	// headed says the column header arrived before the rows: a section that lists rows without it is
	// another command's tail, and a section that prints only its type header is an empty section (the
	// common "no pending respawns of this type" shape).
	headed    bool
	RawHeader string
}

// RespawnReport is a whole `.list respawns` answer.
type RespawnReport struct {
	Sections []RespawnSection
	Raw      []string
}

// respawnTitles are the ToTitle words of SpawnObjectType (enuminfo_SpawnData.cpp: Creature, Gameobject,
// AreaTrigger): the only section kinds the core prints.
var respawnTitles = map[string]bool{"Creature": true, "Gameobject": true, "AreaTrigger": true}

// Reference copies of the core's format strings (cs_list.cpp:663-690 with LANG_LIST_RESPAWNS_RANGE 5077,
// LANG_LIST_RESPAWNS_ZONE 5078 and LANG_LIST_RESPAWNS_LISTHEADER 5079):
// 'Listing %s respawns within %uyd', 'Listing %s respawns for %s (zone %u)',
// 'SpawnID | Entry | GridXY| Zone | Respawn time'.
var (
	respawnRangeRE  = regexp.MustCompile(`^Listing (\S+) respawns within (\d+)yd$`)
	respawnZoneRE   = regexp.MustCompile(`^Listing (\S+) respawns for (.+) \(zone (\d+)\)$`)
	respawnHeaderRE = regexp.MustCompile(`^SpawnID \| Entry \| GridXY\| Zone \| Respawn time$`)
	// The row (cs_list.cpp:690): UI64FMTD " | " %u " | " [%02u,%02u] " | " %s (%u) " | " %s [" (inactive)"].
	respawnRowRE = regexp.MustCompile(`^(\d+) \| (\d+) \| \[(\d\d),(\d\d)\] \| (.+) \((\d+)\) \| (.*?)((?: \(inactive\))?)$`)
)

// overdueWord is LANG_LIST_RESPAWNS_OVERDUE 5080: the 2014 seed text is 'overdue'.
const overdueWord = "overdue"

// ListRespawns reads the pending respawns: `.list respawns [range]`. range bounds the search in yards; 0 lists
// the player's whole zone.
func (c *Client) ListRespawns(ctx context.Context, rangeYd uint32, hasRange bool) (RespawnReport, error) {
	line := ".list respawns"
	if hasRange {
		line = fmt.Sprintf(".list respawns %d", rangeYd)
	}
	rep, err := c.run.Run(ctx, line, command.Options{})
	if err != nil {
		return RespawnReport{}, err
	}
	return parseRespawns(rep)
}

// parseRespawns reads a tokenless `.list respawns` reply. Every line must be a section header, the column header
// or a row: the handler prints nothing else, so anything else is another command's tail.
func parseRespawns(rep *command.Reply) (RespawnReport, error) {
	if rep == nil || len(rep.RawText) == 0 {
		return RespawnReport{}, fmt.Errorf("%w: the respawn reply is empty", ErrBadReport)
	}
	out := RespawnReport{Raw: append([]string(nil), rep.Raw...)}
	var cur *RespawnSection
	for _, raw := range rep.RawText {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if m := respawnRangeRE.FindStringSubmatch(line); m != nil {
			if !respawnTitles[m[1]] {
				return RespawnReport{}, fmt.Errorf("%w: respawn section %q is not a spawn-object type", ErrBadValue, m[1])
			}
			n, err := strconv.ParseUint(m[2], 10, 32)
			if err != nil {
				return RespawnReport{}, fmt.Errorf("%w: respawn range %q is not a number", ErrBadValue, m[2])
			}
			out.Sections = append(out.Sections, RespawnSection{Kind: m[1], Range: uint32(n), HasRange: true, RawHeader: line})
			cur = &out.Sections[len(out.Sections)-1]
			continue
		}
		if m := respawnZoneRE.FindStringSubmatch(line); m != nil {
			if !respawnTitles[m[1]] {
				return RespawnReport{}, fmt.Errorf("%w: respawn section %q is not a spawn-object type", ErrBadValue, m[1])
			}
			id, err := strconv.ParseUint(m[3], 10, 32)
			if err != nil {
				return RespawnReport{}, fmt.Errorf("%w: respawn zone %q is not a number", ErrBadValue, m[3])
			}
			out.Sections = append(out.Sections, RespawnSection{Kind: m[1], Zone: m[2], ZoneID: uint32(id), RawHeader: line})
			cur = &out.Sections[len(out.Sections)-1]
			continue
		}
		if cur == nil {
			return RespawnReport{}, fmt.Errorf("%w: %q is before any respawn section", ErrBadReport, line)
		}
		if !cur.headed {
			if !respawnHeaderRE.MatchString(line) {
				return RespawnReport{}, fmt.Errorf("%w: %q is not the respawn column header", ErrBadReport, line)
			}
			cur.headed = true
			continue
		}
		row, err := parseRespawnRow(cur.Kind, line)
		if err != nil {
			return RespawnReport{}, err
		}
		cur.Rows = append(cur.Rows, row)
	}
	if len(out.Sections) == 0 {
		return RespawnReport{}, fmt.Errorf("%w: no respawn section in the reply", ErrBadReport)
	}
	// The handler prints the column header under every section header (cs_list.cpp:667): a section without
	// it is a cut reply, even when it carries no rows.
	for _, s := range out.Sections {
		if !s.headed {
			return RespawnReport{}, fmt.Errorf("%w: respawn section %q has no column header", ErrBadReport, s.Kind)
		}
	}
	return out, nil
}

// parseRespawnRow reads one row: '<spawnId> | <entry> | [<gridX>,<gridY>] | <zone> (<id>) | <time>[ (inactive)]'
// (cs_list.cpp:690; the zone name itself may hold parentheses, so the id is the LAST pair).
func parseRespawnRow(kind, line string) (RespawnRow, error) {
	m := respawnRowRE.FindStringSubmatch(line)
	if m == nil {
		return RespawnRow{}, fmt.Errorf("%w: %q is not a respawn row", ErrBadReport, line)
	}
	spawn, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return RespawnRow{}, fmt.Errorf("%w: respawn id %q is not a number", ErrBadValue, m[1])
	}
	entry, err := strconv.ParseUint(m[2], 10, 32)
	if err != nil {
		return RespawnRow{}, fmt.Errorf("%w: respawn entry %q is not a number", ErrBadValue, m[2])
	}
	gx, err := strconv.ParseUint(m[3], 10, 8)
	if err != nil {
		return RespawnRow{}, fmt.Errorf("%w: respawn grid %q is not a number", ErrBadValue, m[3])
	}
	gy, err := strconv.ParseUint(m[4], 10, 8)
	if err != nil {
		return RespawnRow{}, fmt.Errorf("%w: respawn grid %q is not a number", ErrBadValue, m[4])
	}
	zoneID, err := strconv.ParseUint(m[6], 10, 32)
	if err != nil {
		return RespawnRow{}, fmt.Errorf("%w: respawn zone %q is not a number", ErrBadValue, m[6])
	}
	row := RespawnRow{
		Kind: kind, SpawnID: spawn, Entry: uint32(entry),
		GridX: uint8(gx), GridY: uint8(gy),
		Zone: m[5], ZoneID: uint32(zoneID),
		TimeLeft: m[7], Overdue: m[7] == overdueWord,
		Inactive: m[8] != "", Raw: line,
	}
	return row, nil
}

// InstanceSpawnGroup is one spawn group `.debug instancespawn` names: whether the instance script currently
// holds it active, and the rule lines that decided it (the explain text, kept raw for bug reports).
type InstanceSpawnGroup struct {
	// Name and ID are the group's name and spawn-group id.
	Name string
	ID   uint32
	// Active is what the compact line said ("is active" vs "is not active").
	Active bool
	// Compact is the compact line's visible text.
	Compact string
	// Rules are the explain lines under a group header (" |-- ...", " | |-- ...", " | |=> ..."), in order.
	Rules []string
	// Raw holds the header plus its rules, visible text.
	Raw []string
}

// InstanceSpawnReport is a whole `.debug instancespawn` answer.
type InstanceSpawnReport struct {
	// Map names the map of the 'Spawn groups managed by %s (%u):' line; MapID its id.
	Map   string
	MapID uint32
	// Groups are the groups in the order the core printed them.
	Groups []InstanceSpawnGroup
	Raw    []string
}

// Reference copies of the core's format strings (cs_debug.cpp:1431-1565): 'Spawn groups managed by %s (%u):',
// " - '%s' (%u) is %sactive" and, with explain, " |-- '%s' (%u)" plus the rule lines (" | |-- '%s' ...",
// " | |=> '%s' ...").
var (
	spawnManagedRE = regexp.MustCompile(`^Spawn groups managed by (.+) \((\d+)\):$`)
	// The lines are matched trimmed (the loop trims first), so the core's leading spaces are gone: " - '...'"
	// matches as "^- '", " |-- '...'" as "^\\|-- '", and the rule lines as "^\\| \\|-- " / "^\\| \\|=> ".
	spawnActiveRE = regexp.MustCompile(`^- '(.+)' \((\d+)\) is (not )?active$`)
	spawnGroupRE  = regexp.MustCompile(`^\|-- '(.+)' \((\d+)\)$`)
)

// DebugInstanceSpawn reads the instance's spawn rules: `.debug instancespawn [groupID|explain]`. groupID limits
// the answer to one group; explain asks for the rule tree instead of the compact lines.
func (c *Client) DebugInstanceSpawn(ctx context.Context, groupID uint32, hasGroup, explain bool) (InstanceSpawnReport, error) {
	line := ".debug instancespawn"
	switch {
	case hasGroup:
		line = fmt.Sprintf(".debug instancespawn %d", groupID)
	case explain:
		line = ".debug instancespawn explain"
	}
	rep, err := c.run.Run(ctx, line, command.Options{})
	if err != nil {
		return InstanceSpawnReport{}, err
	}
	return parseInstanceSpawn(rep)
}

// parseInstanceSpawn reads a tokenless `.debug instancespawn` reply: the managed-by line, then one compact line
// or one explain group each. Unexplained rule lines (' | |-- ...', ' | |=> ...') belong to the open group; a rule
// line before any group header is another command's tail.
func parseInstanceSpawn(rep *command.Reply) (InstanceSpawnReport, error) {
	if rep == nil || len(rep.RawText) == 0 {
		return InstanceSpawnReport{}, fmt.Errorf("%w: the instance-spawn reply is empty", ErrBadReport)
	}
	out := InstanceSpawnReport{Raw: append([]string(nil), rep.Raw...)}
	var cur *InstanceSpawnGroup
	managed := false
	for _, raw := range rep.RawText {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if m := spawnManagedRE.FindStringSubmatch(line); m != nil {
			if managed {
				return InstanceSpawnReport{}, fmt.Errorf("%w: two managed-by lines in one reply", ErrBadReport)
			}
			id, err := strconv.ParseUint(m[2], 10, 32)
			if err != nil {
				return InstanceSpawnReport{}, fmt.Errorf("%w: map %q is not a number", ErrBadValue, m[2])
			}
			out.Map, out.MapID = m[1], uint32(id)
			managed = true
			continue
		}
		if !managed {
			return InstanceSpawnReport{}, fmt.Errorf("%w: %q is before the managed-by line", ErrBadReport, line)
		}
		if m := spawnActiveRE.FindStringSubmatch(line); m != nil {
			if cur != nil {
				return InstanceSpawnReport{}, fmt.Errorf("%w: a compact line inside an explain group", ErrBadReport)
			}
			id, err := strconv.ParseUint(m[2], 10, 32)
			if err != nil {
				return InstanceSpawnReport{}, fmt.Errorf("%w: spawn group %q is not a number", ErrBadValue, m[2])
			}
			out.Groups = append(out.Groups, InstanceSpawnGroup{Name: m[1], ID: uint32(id), Active: m[3] == "", Compact: line, Raw: []string{line}})
			continue
		}
		if m := spawnGroupRE.FindStringSubmatch(line); m != nil {
			if cur != nil {
				return InstanceSpawnReport{}, fmt.Errorf("%w: a group header inside an explain group", ErrBadReport)
			}
			id, err := strconv.ParseUint(m[2], 10, 32)
			if err != nil {
				return InstanceSpawnReport{}, fmt.Errorf("%w: spawn group %q is not a number", ErrBadValue, m[2])
			}
			out.Groups = append(out.Groups, InstanceSpawnGroup{Name: m[1], ID: uint32(id), Raw: []string{line}})
			cur = &out.Groups[len(out.Groups)-1]
			continue
		}
		if cur == nil {
			return InstanceSpawnReport{}, fmt.Errorf("%w: %q is not a spawn-group line", ErrBadReport, line)
		}
		if !strings.HasPrefix(line, "| |-- ") && !strings.HasPrefix(line, "| |=> ") {
			return InstanceSpawnReport{}, fmt.Errorf("%w: %q is not an explain rule line", ErrBadReport, line)
		}
		cur.Rules = append(cur.Rules, line)
		cur.Raw = append(cur.Raw, line)
		cur.Active = explainGroupActive(cur.Rules)
	}
	if !managed {
		return InstanceSpawnReport{}, fmt.Errorf("%w: no managed-by line in the reply", ErrBadReport)
	}
	return out, nil
}

// explainGroupActive reads the group's last verdict line (" | |=> '<name>' is [not ]active ...", cs_debug.cpp:
// ' | |=> '%s' is %sactive'). Lines before the verdict leave the zero value; callers that need the verdict check
// the rules' tail.
func explainGroupActive(rules []string) bool {
	for i := len(rules) - 1; i >= 0; i-- {
		if strings.Contains(rules[i], "|=>") {
			return !strings.Contains(rules[i], "not active")
		}
	}
	return false
}
