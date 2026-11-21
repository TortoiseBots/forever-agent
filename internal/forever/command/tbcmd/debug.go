package tbcmd

// The leash-and-evade bug-report read-backs: `.debug boundary [fill] [duration]`, `.npc evade [why] [force]`,
// `.debug combat` and `.debug threat`, each parsed with the raw server text kept for the report.
//
// `.debug boundary` prints nothing on success: it summons the boundary markers in the world and returns true with
// no chat line (CreatureAI::VisualizeBoundary, CreatureAI.cpp:352-430; cs_debug.cpp:1318 prints only when the
// result is an error id). Its failures are LANG ids (Language.h): 11011 (no interior point), 11012 (unbounded),
// 11013 (fail-safe flood). The 2016 seed texts are 'VisualizeBoundary warning: No interior point of the
// creature's boundary could be found - check if you have mutually exclusive boundaries!',
// 'VisualizeBoundary error: Creature movement is unbounded' and 'VisualizeBoundary warning: Reached fail-safe
// flood boundary - check is your boundary is unbounded!'. A bug report needs that distinction, not the words:
// the typed answer says whether the creature is bounded and, when not, which of the three failures the server
// named.
//
// `.npc evade` also prints nothing on success (cs_npc.cpp HandleNpcEvadeCommand: it enters evade mode and returns
// true with no line). Its refusals are LANG_SELECT_CREATURE 2 ('Select a creature') and
// LANG_CREATURE_NOT_AI_ENABLED 11015; `.debug threat` and `.debug combat` print the server's own lists (see the
// matchers below which quote each line's exact words).

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// BoundaryResult is a `.debug boundary` answer: whether the creature's movement is bounded, and the server's own
// failure when it is not. The core prints nothing on success (cs_debug.cpp HandleDebugBoundaryCommand: the markers
// summoned in the world ARE the answer); an empty reply is therefore only meaningful when the runner saw a truly
// quiet window. Reference copies of the failure strings (LANG 11011-11013,
// 2016_03_22_09_world_2016_01_13_00_world.sql:8-10):
// "VisualizeBoundary warning: No interior point of the creature's boundary could be found - check if you have
// mutually exclusive boundaries!", "VisualizeBoundary error: Creature movement is unbounded",
// "VisualizeBoundary warning: Reached fail-safe flood boundary - check is your boundary is unbounded!".
type BoundaryResult struct {
	// Bounded says the command ran without a failure line: the markers are in the world for the duration.
	// Complete says the run lasted to its deadline with no line (command.Options.Silent, so a late failure line is still caught): the markers are the answer. An empty
	// reply that did NOT end quietly (deadline, line cap) is UNKNOWN, never bounded: a late or dropped
	// failure line must not read as success.
	Bounded  bool
	Complete bool
	// Failure is the failure kind when not bounded: no-interior, unbounded or failsafe.
	Failure string
	Raw     []string
}

// parseBoundary reads a tokenless `.debug boundary` reply: empty (or only blank lines) with a quiet end is
// success — the markers are summoned in the world. One failure line is the failure; anything else is another
// command's tail. The nil check comes first: rep.Raw is never read before it.
func parseBoundary(rep *command.Reply) (BoundaryResult, error) {
	if rep == nil {
		return BoundaryResult{}, fmt.Errorf("%w: the boundary reply is missing", ErrBadReport)
	}
	out := BoundaryResult{Raw: append([]string(nil), rep.Raw...)}
	var lines []string
	for _, l := range rep.RawText {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) == 0 {
		if !rep.Complete || (rep.End != "deadline" && rep.End != "quiet") {
			return BoundaryResult{}, fmt.Errorf("%w: the boundary reply ended %q with no line (UNKNOWN, not bounded)", ErrBadReport, rep.End)
		}
		out.Bounded = true
		out.Complete = true
		return out, nil
	}
	if len(lines) > 1 {
		return BoundaryResult{}, fmt.Errorf("%w: boundary answered %d lines", ErrBadReport, len(lines))
	}
	text := lines[0]
	switch {
	case strings.Contains(text, "No interior point"):
		out.Failure = "no-interior"
	case strings.Contains(text, "unbounded") && strings.Contains(text, "error"):
		out.Failure = "unbounded"
	case strings.Contains(text, "fail-safe flood"):
		out.Failure = "failsafe"
	default:
		return BoundaryResult{}, fmt.Errorf("%w: %q is not a boundary answer", ErrBadReport, text)
	}
	return out, nil
}

// DebugBoundary visualises the selected creature's leash: `.debug boundary [fill] [duration]`. duration is
// seconds (default 180, the core's 3 min); fill fills the interior, not just the edge. A bounded creature answers
// with no line (the markers are the answer); a failure line is the typed Failure.
func (c *Client) DebugBoundary(ctx context.Context, fill bool, duration uint32, hasDuration bool) (BoundaryResult, error) {
	line := ".debug boundary"
	if fill {
		line += " fill"
	}
	if hasDuration {
		line += fmt.Sprintf(" %d", duration)
	}
	o := c.Base
	o.Token = ""
	o.EndKind = ""
	o.Silent = true
	rep, err := c.run.Run(ctx, line, o)
	if err != nil {
		return BoundaryResult{}, err
	}
	return parseBoundary(rep)
}

// EvadeResult is an `.npc evade` answer: it prints nothing on success (the evade itself is the answer).
type EvadeResult struct {
	// Evaded says the command entered evade mode (no refusal line arrived).
	Evaded bool
	Raw    []string
}

// parseEvade reads a tokenless `.npc evade` reply: a quiet end with no line is success (the core prints
// nothing: cs_npc.cpp HandleNpcEvadeCommand returns true with no line). The only lines this command prints are
// its two refusals (LANG_SELECT_CREATURE 2 'Select a creature', LANG_CREATURE_NOT_AI_ENABLED 11015), and both
// come back as command errors through the collector, not as collected lines — so any collected line is another
// command's tail. The nil check comes first: rep.Raw is never read before it.
func parseEvade(rep *command.Reply) (EvadeResult, error) {
	if rep == nil {
		return EvadeResult{}, fmt.Errorf("%w: the evade reply is missing", ErrBadReport)
	}
	out := EvadeResult{Raw: append([]string(nil), rep.Raw...)}
	if len(rep.RawText) == 0 {
		if !rep.Complete || (rep.End != "deadline" && rep.End != "quiet") {
			return EvadeResult{}, fmt.Errorf("%w: the evade reply ended %q with no line", ErrBadReport, rep.End)
		}
		out.Evaded = true
		return out, nil
	}
	for _, l := range rep.RawText {
		if strings.TrimSpace(l) != "" {
			return EvadeResult{}, fmt.Errorf("%w: evade answered %q", ErrBadReport, strings.TrimSpace(l))
		}
	}
	out.Evaded = true
	return out, nil
}

// NpcEvade sends `.npc evade [why] [force]`: the selected creature leaves combat and resets. why is the evade
// reason word, force clears UNIT_STATE_EVADE first (cs_npc.cpp HandleNpcEvadeCommand).
func (c *Client) NpcEvade(ctx context.Context, why string, force bool) (EvadeResult, error) {
	line := ".npc evade"
	if strings.TrimSpace(why) != "" {
		w := strings.TrimSpace(why)
		if strings.ContainsAny(w, " \t\"'") {
			return EvadeResult{}, fmt.Errorf("tbcmd: evade reason %q is not one word", why)
		}
		line += " " + w
	}
	if force {
		line += " force"
	}
	o := c.Base
	o.Token = ""
	o.EndKind = ""
	o.Silent = true
	rep, err := c.run.Run(ctx, line, o)
	if err != nil {
		return EvadeResult{}, err
	}
	return parseEvade(rep)
}

// DebugThreatRow is one line of `.debug threat`: the threatened unit, its threat value and the flags the core
// printed. Reference copies of the core's formats (cs_debug.cpp:877-968): '%s (%s) is not alive.%s',
// '%s (%s) does not threaten any units.', 'List of units threatened by %s (%s)',
// "   %u.   %s   (%s, SpawnID <spawnId>)  - threat %f", 'End of threatened-by-me list.',
// 'Threat list of %s (%s, SpawnID <spawnId>):',
// '%s (%s, SpawnID <spawnId>) is not engaged, but still has a threat list? Well, here it is:',
// "   %u.   %s   (%s)  - threat %f%s%s" (with [TAUNT]/[DETAUNT] and [SUPPRESSED]/[OFFLINE]),
// 'End of threat list.', '%s (%s, SpawnID <spawnId>) is not currently engaged.',
// '%s (%s, SpawnID <spawnId>) seems to be engaged, but does not have a threat list??',
// '%s (%s) is currently engaged. (This unit cannot have a threat list.)',
// '%s (%s) is not currently engaged. (This unit cannot have a threat list.)'.
type DebugThreatRow struct {
	// Index is the list number (1-based), 0 for the unnumbered status lines.
	Index int
	// Name and GUID name the row's unit.
	Name string
	GUID string
	// SpawnID is set when the row carries one.
	SpawnID  uint64
	HasSpawn bool
	// Threat is the threat value when the row carries one.
	Threat    float64
	HasThreat bool
	// Flags are the bracket words the core appended ([TAUNT], [DETAUNT], [SUPPRESSED], [OFFLINE], [FIXATE]).
	Flags []string
	// Raw is the row's visible text.
	Raw string
}

// DebugThreatReport is a whole `.debug threat` answer: the subject, its aliveness and engagement, and both lists.
type DebugThreatReport struct {
	// Subject names the unit the command read.
	Subject string
	// Alive and Engaged are the subject's state as the answer says it. HasThreatList says the core printed a
	// list header (even the "not engaged, but still has a list" one); CannotHave says the subject cannot have
	// one at all ("This unit cannot have a threat list").
	Alive, Engaged bool
	HasThreatList  bool
	CannotHave     bool
	ThreatenedByMe []DebugThreatRow
	ThreatList     []DebugThreatRow
	// Status lines that are not rows: the "does not threaten", "is not alive", "not currently engaged" texts.
	Notes []string
	Raw   []string
}

var (
	threatNotAliveRE  = regexp.MustCompile(`^(.+) \(([^)]+)\) is not alive\.(.*)$`)
	threatNoneRE      = regexp.MustCompile(`^(.+) \(([^)]+)\) does not threaten any units\.$`)
	threatByMeHeadRE  = regexp.MustCompile(`^List of units threatened by (.+) \(([^)]+)\)$`)
	threatByMeRowRE   = regexp.MustCompile(`^(\d+)\.\s+(.+)\s+\(([^,]+), SpawnID (\d+)\)\s+ - threat ([0-9.]+)$`)
	threatListHeadRE  = regexp.MustCompile(`^Threat list of (.+) \(([^,]+), SpawnID (\d+)\):$`)
	threatListOddRE   = regexp.MustCompile(`^(.+) \(([^,]+), SpawnID (\d+)\) is not engaged, but still has a threat list\? Well, here it is:$`)
	threatRowRE       = regexp.MustCompile(`^(\d+)\.\s+(.+)\s+\(([^)]+)\)\s+ - threat ([0-9.]+)((?: \[[A-Z]+\])*)$`)
	threatNoListRE    = regexp.MustCompile(`^(.+) \(([^,]+), SpawnID (\d+)\) is not currently engaged\.$`)
	threatEmptyListRE = regexp.MustCompile(`^(.+) \(([^,]+), SpawnID (\d+)\) seems to be engaged, but does not have a threat list\?\?$`)
	threatCannotYesRE = regexp.MustCompile(`^(.+) \(([^)]+)\) is currently engaged\. \(This unit cannot have a threat list\.\)$`)
	threatCannotNoRE  = regexp.MustCompile(`^(.+) \(([^)]+)\) is not currently engaged\. \(This unit cannot have a threat list\.\)$`)
	threatEndRE       = regexp.MustCompile(`^End of (threatened-by-me|threat) list\.$`)
	threatFlagsRE     = regexp.MustCompile(`\[([A-Z]+)\]`)
)

// parseDebugThreat reads a tokenless `.debug threat` reply. The matchers quote the core's words
// (cs_debug.cpp:877-968); every line must be one of them. Raw is always kept for the bug report.
func parseDebugThreat(rep *command.Reply) (DebugThreatReport, error) {
	if rep == nil || len(rep.RawText) == 0 {
		return DebugThreatReport{}, fmt.Errorf("%w: the threat reply is empty", ErrBadReport)
	}
	out := DebugThreatReport{Alive: true, Raw: append([]string(nil), rep.Raw...)}
	section := ""
	for _, raw := range rep.RawText {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case threatEndRE.MatchString(line):
			section = ""
			continue
		case threatByMeHeadRE.MatchString(line):
			m := threatByMeHeadRE.FindStringSubmatch(line)
			out.Subject = m[1]
			section = "byme"
			continue
		case threatListHeadRE.MatchString(line):
			m := threatListHeadRE.FindStringSubmatch(line)
			out.Subject = m[1]
			out.Engaged, out.HasThreatList = true, true
			section = "list"
			continue
		case threatListOddRE.MatchString(line):
			m := threatListOddRE.FindStringSubmatch(line)
			out.Subject = m[1]
			out.Engaged, out.HasThreatList = false, true
			out.Notes = append(out.Notes, line)
			section = "list"
			continue
		}
		if m := threatNotAliveRE.FindStringSubmatch(line); m != nil {
			out.Subject, out.Alive = m[1], false
			if strings.Contains(m[3], "engaged") {
				out.Engaged = true
			}
			out.Notes = append(out.Notes, line)
			continue
		}
		if m := threatNoneRE.FindStringSubmatch(line); m != nil {
			out.Subject = m[1]
			out.Notes = append(out.Notes, line)
			continue
		}
		if m := threatNoListRE.FindStringSubmatch(line); m != nil {
			out.Subject = m[1]
			out.Engaged = false
			out.Notes = append(out.Notes, line)
			continue
		}
		if m := threatEmptyListRE.FindStringSubmatch(line); m != nil {
			out.Subject = m[1]
			out.Engaged = true
			out.Notes = append(out.Notes, line)
			continue
		}
		if m := threatCannotYesRE.FindStringSubmatch(line); m != nil {
			out.Subject = m[1]
			out.Engaged, out.CannotHave = true, true
			continue
		}
		if m := threatCannotNoRE.FindStringSubmatch(line); m != nil {
			out.Subject = m[1]
			out.Engaged, out.CannotHave = false, true
			continue
		}
		if m := threatByMeRowRE.FindStringSubmatch(line); m != nil {
			if section != "byme" {
				return DebugThreatReport{}, fmt.Errorf("%w: threatened-by-me row outside its list", ErrBadReport)
			}
			row, err := debugThreatRow(m[1], m[2], m[3], m[4], m[5], "")
			if err != nil {
				return DebugThreatReport{}, err
			}
			row.Raw = line
			out.ThreatenedByMe = append(out.ThreatenedByMe, row)
			continue
		}
		if m := threatRowRE.FindStringSubmatch(line); m != nil {
			if section != "list" {
				return DebugThreatReport{}, fmt.Errorf("%w: threat row outside the threat list", ErrBadReport)
			}
			row, err := debugThreatRow(m[1], m[2], m[3], "", m[4], m[5])
			if err != nil {
				return DebugThreatReport{}, err
			}
			row.Raw = line
			out.ThreatList = append(out.ThreatList, row)
			continue
		}
		return DebugThreatReport{}, fmt.Errorf("%w: %q is not a debug-threat line", ErrBadReport, line)
	}
	if out.Subject == "" && len(out.Notes) == 0 && len(out.ThreatList) == 0 && len(out.ThreatenedByMe) == 0 {
		return DebugThreatReport{}, fmt.Errorf("%w: no threat subject in the reply", ErrBadReport)
	}
	return out, nil
}

func debugThreatRow(index, name, guid, spawn, threat, flags string) (DebugThreatRow, error) {
	i, err := strconv.Atoi(index)
	if err != nil || i <= 0 {
		return DebugThreatRow{}, fmt.Errorf("%w: threat index %q is not a number", ErrBadValue, index)
	}
	t, err := strconv.ParseFloat(threat, 64)
	if err != nil {
		return DebugThreatRow{}, fmt.Errorf("%w: threat %q is not a number", ErrBadValue, threat)
	}
	row := DebugThreatRow{Index: i, Name: name, GUID: guid, Threat: t, HasThreat: true}
	if spawn != "" {
		s, err := strconv.ParseUint(spawn, 10, 64)
		if err != nil {
			return DebugThreatRow{}, fmt.Errorf("%w: spawn id %q is not a number", ErrBadValue, spawn)
		}
		row.SpawnID, row.HasSpawn = s, true
	}
	for _, f := range threatFlagsRE.FindAllStringSubmatch(flags, -1) {
		row.Flags = append(row.Flags, f[1])
	}
	return row, nil
}

// DebugThreat reads the selected unit's threat: `.debug threat`.
func (c *Client) DebugThreat(ctx context.Context) (DebugThreatReport, error) {
	rep, err := c.run.Run(ctx, ".debug threat", command.Options{})
	if err != nil {
		return DebugThreatReport{}, err
	}
	return parseDebugThreat(rep)
}

// DebugCombatRef is one `.debug combat` row: the unit on the other side of one combat reference.
// Reference copies of the formats (cs_debug.cpp HandleDebugCombatListCommand): 'Combat refs: (Combat state: %d |
// Manager state: %d)', '[PvP] %s (SpawnID %u)', '[PvE] %s (SpawnID %u)'.
type DebugCombatRef struct {
	// PvP says which list the row came from.
	PvP  bool
	Name string
	// SpawnID is 0 for a player (the core prints SpawnID 0 for non-creatures).
	SpawnID uint32
	Raw     string
}

// DebugCombatReport is a whole `.debug combat` answer.
type DebugCombatReport struct {
	// SubjectInCombat and ManagerCombat are the two %d of the header: the unit's combat state and its
	// combat manager's state.
	SubjectInCombat bool
	ManagerCombat   bool
	Refs            []DebugCombatRef
	Raw             []string
}

var (
	combatHeadRE = regexp.MustCompile(`^Combat refs: \(Combat state: ([01]) \| Manager state: ([01])\)$`)
	combatRowRE  = regexp.MustCompile(`^\[(PvP|PvE)\] (.+) \(SpawnID (\d+)\)$`)
)

// parseDebugCombat reads a tokenless `.debug combat` reply: the header, then zero or more [PvP]/[PvE] rows.
func parseDebugCombat(rep *command.Reply) (DebugCombatReport, error) {
	if rep == nil || len(rep.RawText) == 0 {
		return DebugCombatReport{}, fmt.Errorf("%w: the combat reply is empty", ErrBadReport)
	}
	out := DebugCombatReport{Raw: append([]string(nil), rep.Raw...)}
	headed := false
	for _, raw := range rep.RawText {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if m := combatHeadRE.FindStringSubmatch(line); m != nil {
			if headed {
				return DebugCombatReport{}, fmt.Errorf("%w: two combat headers in one reply", ErrBadReport)
			}
			headed = true
			out.SubjectInCombat = m[1] == "1"
			out.ManagerCombat = m[2] == "1"
			continue
		}
		if !headed {
			return DebugCombatReport{}, fmt.Errorf("%w: %q is before the combat header", ErrBadReport, line)
		}
		m := combatRowRE.FindStringSubmatch(line)
		if m == nil {
			return DebugCombatReport{}, fmt.Errorf("%w: %q is not a combat row", ErrBadReport, line)
		}
		n, err := strconv.ParseUint(m[3], 10, 32)
		if err != nil {
			return DebugCombatReport{}, fmt.Errorf("%w: spawn id %q is not a number", ErrBadValue, m[3])
		}
		out.Refs = append(out.Refs, DebugCombatRef{PvP: m[1] == "PvP", Name: m[2], SpawnID: uint32(n), Raw: line})
	}
	if !headed {
		return DebugCombatReport{}, fmt.Errorf("%w: no combat header in the reply", ErrBadReport)
	}
	return out, nil
}

// DebugCombat reads the selected unit's combat references: `.debug combat`.
func (c *Client) DebugCombat(ctx context.Context) (DebugCombatReport, error) {
	rep, err := c.run.Run(ctx, ".debug combat", command.Options{})
	if err != nil {
		return DebugCombatReport{}, err
	}
	return parseDebugCombat(rep)
}
