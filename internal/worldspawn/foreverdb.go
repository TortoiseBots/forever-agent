package worldspawn

import (
	"fmt"
	"strings"
)

// The core's GroupAIFlags (CreatureGroups.h:28-31). They are NOT worldspawn's option bits: 0x1 says the members assist
// the leader, 0x2 that the leader assists a member, and 0x200 that a member follows the leader while idle. The reader's
// FormationMove means that last follow and its AggroTogether means the group wakes as one, which either assist direction
// causes.
const (
	foreverMembersAssistLeader = 0x00000001
	foreverLeaderAssistsMember = 0x00000002
	foreverIdleInFormation     = 0x00000200
)

// foreverGroupFlags maps one creature_formations.groupAI value to the reader's own option set. Copying the raw number
// would read the common 0x1 pack (members assist the leader when it pulls) as FormationMove and split it into single
// pulls, and a 0x3 pack (assist both ways) as FormationMove|AggroTogether, i.e. as a moving formation.
func foreverGroupFlags(groupAI uint32) uint32 {
	var flags uint32
	if groupAI&(foreverMembersAssistLeader|foreverLeaderAssistsMember) != 0 {
		flags |= AggroTogether
	}
	if groupAI&foreverIdleInFormation != 0 {
		flags |= FormationMove
	}
	return flags
}

// LoadForeverDB reads one map's export written by tools/dungeongen/export-forever.sh from a ForeverCore world database
// (server revision 25fc7ca) and returns the world Load models. It is Load with the three differences the ForeverCore
// tables make:
//
//   - The level is not in creature_template: the export writes the range the core applies (Creature::ApplyLevelScaling,
//     Creature.cpp:3244-3265: a creature_classic_level_spawn row for the guid, else creature_classic_level for the entry,
//     else the difficulty row's content tuning). An empty level_max cell means the core has no level for the spawn: that
//     spawn gets level 60 and one note counts them. A numeric level that is not positive is an export error
//     (ErrLevelZero), and a map with no spawns at all is too (ErrNoSpawns: a wrong database name or map id writes the
//     header only).
//   - creature_template has no detection_range column (Creature::GetAttackDistance is level based, Creature.cpp:2257), so
//     the export's cell is empty and the spawn gets the model's 20 yd default.
//   - creature_formations stores the leader on a row of its own with leaderGUID = memberGUID (CreatureGroups.cpp:137-147):
//     that row marks the spawn as the group's leader, and its groupAI counts with the members' (the core reads the
//     engaging member's own row, so the leader's row's 0x1 is what makes the members assist it). The group's option set
//     is foreverGroupFlags of the OR of every row's groupAI. A formation whose leader has no row of its own is dropped,
//     as the core drops it (CreatureGroups.cpp:156-171).
//
// Unchanged from Load: the formation offsets and the creation of Patrols from the waypoint rows (the Forever export writes
// the creature_movement and creature_movement_template column sets from creature_addon/creature_template_addon PathId and
// waypoint_path_node, so the reader needs no second dialect). The group pass runs before the patrols are derived, so a
// follower whose leader moves is a patroller too. ForeverCore has no creature_linking tables, so the export writes no
// links.tsv or link_templates.tsv; Load's two "an export before the raid columns" notes are replaced by one that names
// the Forever reason.
func LoadForeverDB(dir string, mapID uint32) (*World, []string, error) {
	w, notes, err := load(dir, mapID, true)
	if err != nil {
		return nil, nil, err
	}
	if len(w.Spawns) == 0 {
		return nil, nil, fmt.Errorf("%w: %s, map %d (a wrong database name or map id writes the header only)", ErrNoSpawns, dir, mapID)
	}
	unknown := 0
	for i := range w.Spawns {
		s := &w.Spawns[i]
		if s.LevelMax < 1 {
			s.LevelMax = 60
			unknown++
		}
		if s.DetectionYd < 1 {
			s.DetectionYd = defaultDetectionYd
		}
	}
	notes = foreverNotes(notes)
	if unknown > 0 {
		notes = append(notes, fmt.Sprintf("creatures.tsv: %d of %d spawns have no level in the core (creature_classic_level_spawn, creature_classic_level and content tuning all lack them): level 60 assumed",
			unknown, len(w.Spawns)))
	}
	return w, notes, nil
}

// foreverNotes replaces Load's link notes with the Forever reason: the server has no creature_linking tables.
func foreverNotes(notes []string) []string {
	out := make([]string, 0, len(notes))
	missing := 0
	for _, n := range notes {
		if strings.Contains(n, "no creature_linking rows") || strings.Contains(n, "no creature_linking_template rows") {
			missing++
			continue
		}
		out = append(out, n)
	}
	if missing > 0 {
		out = append(out, "links.tsv, link_templates.tsv: ForeverCore has no creature_linking or creature_linking_template tables: no link edges, so a pack is its creature_formations group alone")
	}
	return out
}

// applyForeverGroups fills GroupLeader and GroupFlags from the Forever groups.tsv (the creature_formations rows, whose
// group_ai column is the core's raw GroupAIFlags). The core keeps groupAI per member row and reads it when a member
// engages (CreatureGroups.cpp:225-247): the leader's own row's 0x1 is what makes the members assist the leader, so a
// group's option set is foreverGroupFlags of the OR of every row, the self row included. A row whose memberGUID is the
// leaderGUID is the leader's own row (CreatureGroups.cpp:143); the core drops a formation whose leader has no such row
// (CreatureGroups.cpp:156-171), and so does this pass when the leader is one of the export's spawns. The pass replaces
// what creatures.tsv's group columns said, so the file is authoritative.
func applyForeverGroups(w *World, t *tsvFile, notes *[]string) error {
	if t == nil {
		*notes = append(*notes, "groups.tsv: no creature_formations rows (no groups)")
		return nil
	}
	if err := t.require("leader_guid", "member_guid", "group_ai"); err != nil {
		return err
	}
	leaderOf := map[uint64]uint64{}
	members := map[uint64]int{}
	flags := map[uint64]uint32{}
	selfRow := map[uint64]bool{}
	for _, row := range t.rows {
		r := rowReader{t: t, row: row}
		leader, member := uint64(r.num("leader_guid")), uint64(r.num("member_guid"))
		ai := uint32(r.num("group_ai"))
		if r.err != nil {
			return r.err
		}
		leaderOf[member] = leader
		members[leader]++
		flags[leader] |= foreverGroupFlags(ai)
		if member == leader {
			selfRow[leader] = true
		}
	}
	present := make(map[uint64]bool, len(w.Spawns))
	for _, s := range w.Spawns {
		present[s.GUID] = true
	}
	dropped := map[uint64]bool{}
	for leader := range flags {
		if present[leader] && !selfRow[leader] {
			dropped[leader] = true
		}
	}
	for i := range w.Spawns {
		s := &w.Spawns[i]
		leader, ok := leaderOf[s.GUID]
		if !ok || dropped[leader] {
			s.GroupLeader, s.GroupFlags = 0, 0
			continue
		}
		s.GroupLeader, s.GroupFlags = leader, flags[leader]
	}
	groups, kept := 0, 0
	for leader := range flags {
		if !dropped[leader] {
			groups++
			kept += members[leader]
		}
	}
	note := fmt.Sprintf("groups.tsv: %d groups, %d members", groups, kept)
	if len(dropped) > 0 {
		note += fmt.Sprintf("; %d formations dropped (their leader has no row of its own, as the core drops them)", len(dropped))
	}
	*notes = append(*notes, note)
	return nil
}
