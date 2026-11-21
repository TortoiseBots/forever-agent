package tbcmd

// Hand-written samples of the core's respawn and instance-spawn answers. The reference copies of the format
// strings sit in respawns.go beside the matchers: 'Listing %s respawns within %uyd',
// 'Listing %s respawns for %s (zone %u)', 'SpawnID | Entry | GridXY| Zone | Respawn time (Full)'
// (cs_list.cpp:663-667, LANG_LIST_RESPAWNS 5077-5079; trinity_string 5079 in
// 2020_08_22_00_world_2017_07_31_00_world.sql:200) with rows '<spawnId> | <entry> | [<gridX>,<gridY>] | <zone> (<id>) | <time>[
// (inactive)]' (cs_list.cpp:690: UI64FMTD | %u | [%02u,%02u] | %s (%u) | %s%s), and 'Spawn groups managed by
// %s (%u):' with " - '<name>' (<id>) is [not ]active" or " |-- '<name>' (<id>)" plus the rule lines
// (cs_debug.cpp:1431-1565).

import (
	"context"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

func TestListRespawnsZone(t *testing.T) {
	// A two-type answer: the Creature section carries two rows (one overdue, one with a ShortText wait and an
	// inactive group); the Gameobject section is listed and empty (a pack that stays dead leaves no rows).
	rep := bossReply(
		"Listing Creature respawns for Elwynn Forest (zone 12)",
		"SpawnID | Entry | GridXY| Zone | Respawn time (Full)",
		"137 | 476 | [32,33] | Elwynn Forest (12) | overdue",
		"138 | 477 | [32,33] | Elwynn Forest (12) | 1h2m3s (inactive)",
		"Listing Gameobject respawns for Elwynn Forest (zone 12)",
		"SpawnID | Entry | GridXY| Zone | Respawn time (Full)",
		"Listing AreaTrigger respawns for Elwynn Forest (zone 12)",
		"SpawnID | Entry | GridXY| Zone | Respawn time (Full)",
	)
	got, err := parseRespawns(rep)
	if err != nil {
		t.Fatalf("parseRespawns: %v", err)
	}
	if len(got.Sections) != 3 {
		t.Fatalf("sections %+v", got.Sections)
	}
	cr := got.Sections[0]
	if cr.Kind != "Creature" || cr.Zone != "Elwynn Forest" || cr.ZoneID != 12 || cr.HasRange {
		t.Fatalf("section %+v", cr)
	}
	if len(cr.Rows) != 2 {
		t.Fatalf("rows %+v", cr.Rows)
	}
	if cr.Rows[0].SpawnID != 137 || cr.Rows[0].Entry != 476 || cr.Rows[0].GridX != 32 || cr.Rows[0].GridY != 33 ||
		cr.Rows[0].ZoneID != 12 || !cr.Rows[0].Overdue || cr.Rows[0].Inactive || cr.Rows[0].TimeLeft != "overdue" {
		t.Fatalf("row %+v", cr.Rows[0])
	}
	if cr.Rows[1].TimeLeft != "1h2m3s" || cr.Rows[1].Overdue || !cr.Rows[1].Inactive {
		t.Fatalf("row %+v", cr.Rows[1])
	}
	if len(got.Sections[1].Rows) != 0 {
		t.Fatalf("gameobject section %+v", got.Sections[1])
	}
}

func TestListRespawnsRange(t *testing.T) {
	var sent string
	rep := bossReply(
		"Listing Creature respawns within 100yd",
		"SpawnID | Entry | GridXY| Zone | Respawn time (Full)",
		"Listing Gameobject respawns within 100yd",
		"SpawnID | Entry | GridXY| Zone | Respawn time (Full)",
		"99 | 17305 | [40,41] | Deadmines (Gate) (36) | 45s",
	)
	// The gate row's zone name holds a parenthesis: the id is the last pair, the name keeps its own.
	row, err := parseRespawnRow("Gameobject", "99 | 17305 | [40,41] | Deadmines (Gate) (36) | 45s")
	if err != nil {
		t.Fatalf("parseRespawnRow: %v", err)
	}
	if row.Zone != "Deadmines (Gate)" || row.ZoneID != 36 || row.Overdue || row.TimeLeft != "45s" {
		t.Fatalf("row %+v", row)
	}
	c := NewClient(bossRunner(rep, &sent))
	got, err := c.ListRespawns(context.Background(), 100, true)
	if err != nil {
		t.Fatalf("ListRespawns: %v", err)
	}
	if sent != ".list respawns 100" {
		t.Fatalf("line %q", sent)
	}
	if !got.Sections[0].HasRange || got.Sections[0].Range != 100 {
		t.Fatalf("section %+v", got.Sections[0])
	}
	var bare string
	c = NewClient(bossRunner(rep, &bare))
	if _, err := c.ListRespawns(context.Background(), 0, false); err != nil {
		t.Fatalf("ListRespawns: %v", err)
	}
	if bare != ".list respawns" {
		t.Fatalf("line %q", bare)
	}
}

func TestListRespawnsRefusals(t *testing.T) {
	for _, lines := range [][]string{
		{},
		{"SpawnID | Entry | GridXY| Zone | Respawn time (Full)"},
		{"SpawnID | Entry | GridXY| Zone | Respawn time"},
		{"Listing Creature respawns for Elwynn Forest (zone 12)"},
		{"Listing Beasts respawns for Elwynn Forest (zone 12)"},
		{"Listing Creature respawns within manyyd"},
		{"Listing Creature respawns for Elwynn Forest (zone 12)",
			"137 | 476 | [32,33] | Elwynn Forest (12) | overdue"},
		{"Listing Creature respawns for Elwynn Forest (zone 12)",
			"SpawnID | Entry | GridXY| Zone | Respawn time (Full)",
			"137 | 476 | 32,33 | Elwynn Forest (12) | overdue"},
		{"Listing Creature respawns for Elwynn Forest (zone 12)",
			"SpawnID | Entry | GridXY| Zone | Respawn time (Full)",
			"137 | 476 | [32,33] | Elwynn Forest (12)"},
	} {
		if _, err := parseRespawns(bossReply(lines...)); err == nil {
			t.Fatalf("%q parsed", lines)
		}
		if _, err := parseBossState(bossReply(lines...), false); err == nil && len(lines) > 0 {
			t.Fatalf("%q parsed as a boss state", lines)
		}
	}
}

func TestDebugInstanceSpawnCompact(t *testing.T) {
	rep := bossReply(
		"Spawn groups managed by Zul'Gurub (309):",
		" - 'Jeklik trash' (12) is active",
		" - 'Hakkar' (13) is not active",
	)
	got, err := parseInstanceSpawn(rep, false)
	if err != nil {
		t.Fatalf("parseInstanceSpawn: %v", err)
	}
	if got.Map != "Zul'Gurub" || got.MapID != 309 {
		t.Fatalf("map %+v", got)
	}
	if len(got.Groups) != 2 || got.Groups[0].Name != "Jeklik trash" || got.Groups[0].ID != 12 || !got.Groups[0].Active {
		t.Fatalf("groups %+v", got.Groups)
	}
	if got.Groups[1].Active {
		t.Fatalf("groups %+v", got.Groups)
	}
	var sent string
	c := NewClient(bossRunner(rep, &sent))
	if _, err := c.DebugInstanceSpawn(context.Background(), 0, false, false); err != nil {
		t.Fatalf("DebugInstanceSpawn: %v", err)
	}
	if sent != ".debug instancespawn" {
		t.Fatalf("line %q", sent)
	}
	// A group id's answer carries no managed-by line (cs_debug.cpp: `if (!groupID) PSendSysMessage(...)`):
	// the parser must take the compact line on its own. The fake runner answers by line, the way the
	// production runner does (command.go: a quiet window with no line is AllowEmpty; a collected group
	// line is the reply).
	groupRep := bossReply(" - 'Hakkar' (13) is not active")
	c = NewClient(bossRunner(groupRep, &sent))
	gotGroup, err := c.DebugInstanceSpawn(context.Background(), 13, true, false)
	if err != nil {
		t.Fatalf("DebugInstanceSpawn 13: %v", err)
	}
	if sent != ".debug instancespawn 13" {
		t.Fatalf("line %q", sent)
	}
	if len(gotGroup.Groups) != 1 || gotGroup.Groups[0].ID != 13 || gotGroup.Groups[0].Active {
		t.Fatalf("group %+v", gotGroup.Groups)
	}
	_ = command.Options{}
}

// TestDebugInstanceSpawnGroupID: the core's real single-group shape (no managed-by line) parses on the
// group-id path and is refused without it.
func TestDebugInstanceSpawnGroupID(t *testing.T) {
	got, err := parseInstanceSpawn(bossReply(" - 'Hakkar' (13) is not active"), true)
	if err != nil {
		t.Fatalf("group-id reply: %v", err)
	}
	if len(got.Groups) != 1 || got.Groups[0].Name != "Hakkar" || got.Groups[0].ID != 13 || got.Groups[0].Active {
		t.Fatalf("groups %+v", got.Groups)
	}
	if _, err := parseInstanceSpawn(bossReply(" - 'Hakkar' (13) is not active"), false); err == nil {
		t.Fatal("managed-by-less reply parsed without the group flag")
	}
	// The core's error lines are command errors, never collected lines: a reply that reaches the parser
	// must still be a group line.
	for _, line := range []string{
		"There is no spawn group with ID 12.",
		"Zul'Gurub has no instance script.",
		"Zul'Gurub's instance script does not manage any spawn groups.",
		"Zul'Gurub's instance script does not manage group 'Hakkar'.",
	} {
		if _, err := parseInstanceSpawn(bossReply(line), true); err == nil {
			t.Fatalf("%q parsed", line)
		}
	}
}

func TestDebugInstanceSpawnExplain(t *testing.T) {
	rep := bossReply(
		"Spawn groups managed by Zul'Gurub (309):",
		" |-- 'Hakkar' (13)",
		" | |-- 'Hakkar' is blocked from spawning because boss state 4 is NOT_STARTED.",
		" | |=> 'Hakkar' is not active due to a blocking rule being matched",
	)
	got, err := parseInstanceSpawn(rep, false)
	if err != nil {
		t.Fatalf("parseInstanceSpawn: %v", err)
	}
	if len(got.Groups) != 1 || len(got.Groups[0].Rules) != 2 || got.Groups[0].Active {
		t.Fatalf("groups %+v", got.Groups)
	}
	var sent string
	c := NewClient(bossRunner(rep, &sent))
	if _, err := c.DebugInstanceSpawn(context.Background(), 0, false, true); err != nil {
		t.Fatalf("DebugInstanceSpawn: %v", err)
	}
	if sent != ".debug instancespawn explain" {
		t.Fatalf("line %q", sent)
	}
	for _, lines := range [][]string{
		{" - 'Hakkar' (13) is active"},
		{"Spawn groups managed by Zul'Gurub (309):",
			" | |-- 'Hakkar' is blocked from spawning because boss state 4 is NOT_STARTED."},
		{"Spawn groups managed by Zul'Gurub (309):",
			"Spawn groups managed by Zul'Gurub (309):"},
	} {
		if _, err := parseInstanceSpawn(bossReply(lines...), false); err == nil {
			t.Fatalf("%q parsed", lines)
		}
	}
}
