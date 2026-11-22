package worldspawn

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// foreverExport is the fixture export: map 700 written by tools/dungeongen/export-forever.sh with invented rows.
const foreverExport = "testdata/map700-forever"

// foreverHeader is the creatures.tsv header the Forever export writes: the 1.12 column set with group_ai (the core's raw
// creature_formations.groupAI) in place of group_flags.
const foreverHeader = "guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\tdetection_range\tflags_extra\tspeed_walk\tgroup_ai\tpool\n"

// foreverRow is one creatures.tsv row of the Forever export.
func foreverRow(guid uint64, entry uint32, name, levelMin, levelMax string, groupLeader uint64, groupAI uint32) string {
	return join(guid, entry, name, levelMin, levelMax, 0, 16, 0, 0, 0, 0, 0, 0, groupLeader, 0, "", 20, 0, 1, groupAI, 0)
}

// TestForeverGroupAIMapping pins the mapping of the core's GroupAIFlags (CreatureGroups.h:28-31) to the reader's option
// set: either assist direction makes the group wake as one (AggroTogether), and only 0x200 makes its members follow the
// leader (FormationMove). Copying the raw number would turn 0x1 into FormationMove and 0x3 into a moving formation.
func TestForeverGroupAIMapping(t *testing.T) {
	for _, c := range []struct {
		ai   uint32
		want uint32
	}{
		{0, 0},
		{foreverMembersAssistLeader, AggroTogether},
		{foreverLeaderAssistsMember, AggroTogether},
		{foreverMembersAssistLeader | foreverLeaderAssistsMember, AggroTogether},
		{foreverIdleInFormation, FormationMove},
		{foreverIdleInFormation | foreverMembersAssistLeader, FormationMove | AggroTogether},
		{0x100, 0},
	} {
		if got := foreverGroupFlags(c.ai); got != c.want {
			t.Errorf("foreverGroupFlags(%#x) = %#x, want %#x", c.ai, got, c.want)
		}
	}
}

// TestForeverDBFormationSelfRow: the leader's own row (leaderGUID = memberGUID) marks the leader and its groupAI counts
// with the members': the core reads the engaging member's own row, so a self row with 0x1 alone (members assist the
// leader, the common pack) must make the group wake as one. Its members stay one pull; the 0x200 group follows its
// leader instead and is not a pull of its own. The fixture's member row of the 0x1 group is 0 on purpose.
func TestForeverDBFormationSelfRow(t *testing.T) {
	w, notes, err := LoadForeverDB(foreverExport, 700)
	if err != nil {
		t.Fatalf("LoadForeverDB: %v", err)
	}
	byGUID := map[uint64]Spawn{}
	for _, s := range w.Spawns {
		byGUID[s.GUID] = s
	}
	for guid, want := range map[uint64]uint64{100: 100, 101: 100, 300: 300, 301: 300} {
		if got := byGUID[guid].GroupLeader; got != want {
			t.Errorf("spawn %d: GroupLeader %d, want %d (the leader's own row marks it)", guid, got, want)
		}
	}
	if got := byGUID[100].GroupFlags; got != AggroTogether {
		t.Errorf("spawn 100: GroupFlags %#x, want AggroTogether (the leader's own row is 0x1)", got)
	}
	if got := byGUID[101].GroupFlags; got != AggroTogether {
		t.Errorf("spawn 101: GroupFlags %#x, want the group's AggroTogether", got)
	}
	if got := byGUID[300].GroupFlags; got != FormationMove {
		t.Errorf("spawn 300: GroupFlags %#x, want FormationMove from 0x200", got)
	}
	if got := byGUID[301].GroupFlags; got != FormationMove {
		t.Errorf("spawn 301: GroupFlags %#x, want the group's FormationMove", got)
	}
	got := packText(w.Packs(PackOptions{}))
	want := []string{"100:[100 101]", "200:[200]", "300:[300]", "301:[301]"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("packs = %v, want %v (the 0x1 pack is one pull; 0x200 is not aggro together)", got, want)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "groups.tsv: 2 groups, 4 members") {
		t.Errorf("no group note:\n%s", strings.Join(notes, "\n"))
	}
}

// TestForeverDBFormationWithoutSelfRow: creature_formations requires a leader's row of its own; the core removes the
// whole formation when it is missing (CreatureGroups.cpp:156-171). The reader does the same and notes it, instead of
// keeping the members in a group the server never forms.
func TestForeverDBFormationWithoutSelfRow(t *testing.T) {
	dir := writeExport(t, map[string]string{
		"creatures.tsv": foreverHeader +
			foreverRow(10, 4100, "Leader", "40", "40", 0, 0) +
			foreverRow(11, 4100, "Member", "40", "40", 10, 1),
		"groups.tsv": "leader_guid\tmember_guid\tdist\tangle\tgroup_ai\n10\t11\t3\t0\t1\n",
	})
	w, notes, err := LoadForeverDB(dir, 700)
	if err != nil {
		t.Fatalf("LoadForeverDB: %v", err)
	}
	for _, s := range w.Spawns {
		if s.GroupLeader != 0 || s.GroupFlags != 0 {
			t.Errorf("spawn %d: GroupLeader %d GroupFlags %#x, want no group (the leader has no self row)", s.GUID, s.GroupLeader, s.GroupFlags)
		}
	}
	if !strings.Contains(strings.Join(notes, "\n"), "formations dropped") {
		t.Errorf("no dropped-formation note:\n%s", strings.Join(notes, "\n"))
	}
}

// TestForeverDBRejectsDuplicateGUID: two rows with one guid (the content_tuning join fanning out, or two exports
// concatenated) would give the world two spawns and a doubled pack; it is an export error, not a quiet duplicate.
func TestForeverDBRejectsDuplicateGUID(t *testing.T) {
	row := foreverRow(7, 4100, "Rat", "40", "40", 0, 0)
	dir := writeExport(t, map[string]string{"creatures.tsv": foreverHeader + row + row})
	_, _, err := LoadForeverDB(dir, 700)
	if !errors.Is(err, ErrDuplicateGUID) {
		t.Fatalf("err = %v, want ErrDuplicateGUID", err)
	}
}

// TestForeverDBRejectsEmptyMap: a header-only creatures.tsv is what a wrong database name or map id writes; the loader
// refuses it instead of returning a valid-looking empty world.
func TestForeverDBRejectsEmptyMap(t *testing.T) {
	dir := writeExport(t, map[string]string{"creatures.tsv": foreverHeader})
	_, _, err := LoadForeverDB(dir, 700)
	if !errors.Is(err, ErrNoSpawns) {
		t.Fatalf("err = %v, want ErrNoSpawns", err)
	}
}

// TestForeverDBRejectsLevelZero: only an empty level cell means "the core has no level" (filled with 60 and noted); a
// literal 0 (or a negative) is a damaged export and is refused.
func TestForeverDBRejectsLevelZero(t *testing.T) {
	for _, level := range []string{"0", "-1"} {
		dir := writeExport(t, map[string]string{
			"creatures.tsv": foreverHeader + foreverRow(7, 4100, "Rat", level, level, 0, 0),
		})
		_, _, err := LoadForeverDB(dir, 700)
		if !errors.Is(err, ErrLevelZero) {
			t.Errorf("level %q: err = %v, want ErrLevelZero", level, err)
		}
	}
}

// TestForeverDBWaypointsViaAddonPath: a spawn with MovementType 2 gets its loop from the movement.tsv rows the export
// writes from creature_addon.PathId -> waypoint_path_node, in node order, with the Delay column as the wait in ms.
func TestForeverDBWaypointsViaAddonPath(t *testing.T) {
	w, _, err := LoadForeverDB(foreverExport, 700)
	if err != nil {
		t.Fatalf("LoadForeverDB: %v", err)
	}
	if len(w.Patrols) != 1 {
		t.Fatalf("%d patrols, want 1: %+v", len(w.Patrols), w.Patrols)
	}
	p := w.Patrols[0]
	if len(p.Path) != 3 || p.Path[0] != (dungeon.Point{X: 0, Y: 20, Z: 0}) || p.Path[2] != (dungeon.Point{X: 10, Y: 30, Z: 0}) {
		t.Fatalf("path %+v", p.Path)
	}
	var waits []string
	for _, wait := range p.WaitsMS {
		waits = append(waits, strconv.Itoa(wait))
	}
	if strings.Join(waits, ",") != "0,2500,0" {
		t.Errorf("waits %v, want the node delays 0,2500,0", p.WaitsMS)
	}
	if !strings.Contains(p.Source, "200") {
		t.Errorf("patrol source %q names no spawn", p.Source)
	}
}

// TestForeverDBLevelNote pins the three ForeverCore defaults: no level row is level 60 with one note, a numeric level is
// kept, and no detection range is 20 yd. The not-aggro bit the export keeps is still NoAggro.
func TestForeverDBLevelNote(t *testing.T) {
	w, notes, err := LoadForeverDB(foreverExport, 700)
	if err != nil {
		t.Fatalf("LoadForeverDB: %v", err)
	}
	byGUID := map[uint64]Spawn{}
	for _, s := range w.Spawns {
		byGUID[s.GUID] = s
	}
	if got := byGUID[100].LevelMax; got != 60 {
		t.Errorf("spawn 100 level %d, want 60 (no classic or content tuning row)", got)
	}
	if got := byGUID[200].LevelMax; got != 42 {
		t.Errorf("spawn 200 level %d, want the level row's 42", got)
	}
	if got := byGUID[100].DetectionYd; got != defaultDetectionYd {
		t.Errorf("spawn 100 detection %g, want the %d yd default", got, defaultDetectionYd)
	}
	if byGUID[100].FlagsExtra&NoAggro == 0 {
		t.Errorf("spawn 100 flags_extra %#x lost the not-aggro bit", byGUID[100].FlagsExtra)
	}
	if byGUID[101].FlagsExtra&NoAggro != 0 {
		t.Errorf("spawn 101 flags_extra %#x grew a not-aggro bit", byGUID[101].FlagsExtra)
	}
	count := 0
	for _, n := range notes {
		if strings.Contains(n, "level 60 assumed") {
			count++
			if !strings.Contains(n, "2 of 5 spawns") {
				t.Errorf("level note does not count the two unknown levels: %q", n)
			}
		}
	}
	if count != 1 {
		t.Errorf("%d level notes, want one:\n%s", count, strings.Join(notes, "\n"))
	}
	// A 1.12 export keeps refusing an empty level cell: the Forever reader must not loosen Load.
	dir := writeExport(t, map[string]string{
		"creatures.tsv": newHeader +
			join(1, 10, "Empty Level", 1, "", 0, 16, 0, 0, 0, 0, 0, 0, 0, 0, "", 20, 0, 1, 0, 0),
	})
	if _, _, err := Load(dir, 700); err == nil {
		t.Fatal("Load accepted an empty level_max cell")
	}
}

// TestForeverDBFollowerJoinsPatrol: the group pass runs before the patrols are derived, so a 0x200 (idle in formation)
// follower of a patrolling leader is part of the leader's loop. Applying the formations after the patrols would leave
// the follower out of every patrol although its spawn is a patroller.
func TestForeverDBFollowerJoinsPatrol(t *testing.T) {
	dir := writeExport(t, map[string]string{
		"creatures.tsv": foreverHeader +
			join(300, 4300, "Leader", 40, 40, 0, 16, 0, 0, 0, 0, 0, 2, 300, 1, "", 20, 0, 1, 512, 0) +
			join(301, 4300, "Follower", 40, 40, 0, 16, 0, 3, 0, 0, 0, 0, 300, 0, "", 20, 0, 1, 0, 0),
		"groups.tsv": "leader_guid\tmember_guid\tdist\tangle\tgroup_ai\n300\t300\t0\t0\t512\n300\t301\t3\t0\t512\n",
		"movement.tsv": "guid\tpoint\tx\ty\tz\twaittime\n" +
			"300\t1\t0\t0\t0\t0\n300\t2\t10\t0\t0\t0\n300\t3\t10\t10\t0\t0\n",
	})
	w, _, err := LoadForeverDB(dir, 700)
	if err != nil {
		t.Fatalf("LoadForeverDB: %v", err)
	}
	if len(w.Patrols) != 1 {
		t.Fatalf("%d patrols, want 1: %+v", len(w.Patrols), w.Patrols)
	}
	if got := w.Patrols[0].SpawnGUIDs; len(got) != 2 || got[0] != 300 || got[1] != 301 {
		t.Fatalf("patrol spawns %v, want the leader and its follower [300 301]", got)
	}
}

// exportScript reads tools/dungeongen/export-forever.sh.
func exportScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "tools", "dungeongen", "export-forever.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestExportScriptSelectsOnly: the Forever export is a read-only, SELECT-only script. The read-only session is part of
// the mariadb command line that query() runs (the header comment may say anything), every statement in a query heredoc
// starts with SELECT, and no write verb appears anywhere.
func TestExportScriptSelectsOnly(t *testing.T) {
	script := exportScript(t)
	var mariadb string
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "exec mariadb") {
			mariadb = line
			break
		}
	}
	if mariadb == "" {
		t.Fatal("no mariadb command line: query() is gone")
	}
	if !strings.Contains(mariadb, "--init-command=") || !strings.Contains(mariadb, "SET SESSION TRANSACTION READ ONLY") {
		t.Fatalf("the mariadb command line does not open a read-only session: %q", mariadb)
	}
	if write := regexp.MustCompile(`\b(?i:insert|update|delete|drop)\b`).FindString(script); write != "" {
		t.Fatalf("the script contains the write verb %q", write)
	}
	lines := strings.Split(script, "\n")
	queries := 0
	for i := 0; i < len(lines); i++ {
		if !strings.HasSuffix(strings.TrimSpace(lines[i]), "query <<SQL") {
			continue
		}
		var stmt []string
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "SQL"; i++ {
			stmt = append(stmt, strings.TrimSpace(lines[i]))
		}
		text := strings.Join(stmt, " ")
		for _, part := range strings.Split(text, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !strings.HasPrefix(strings.ToUpper(part), "SELECT") {
				t.Errorf("statement does not start with SELECT: %q", part)
			}
		}
		queries++
	}
	if queries != 8 {
		t.Errorf("%d queries, want the six table blocks, the orphan count and the --all map list (portals.tsv is a header)", queries)
	}
	for _, file := range []string{"creatures.tsv", "groups.tsv", "movement.tsv", "movement_template.tsv", "gameobjects.tsv", "entrances.tsv", "portals.tsv"} {
		if !strings.Contains(script, `> "$tmp/`+file+`"`) {
			t.Errorf("the script does not stage %s", file)
		}
		if !strings.Contains(script, `"$tmp/`+file+`"`) {
			t.Errorf("the script does not publish %s", file)
		}
	}
}

// TestExportScriptOneTuningRowPerID: hotfixes.content_tuning is keyed (ID, VerifiedBuild), so a plain join on ID
// duplicates a spawn for every build row. The export joins through the ID's newest VerifiedBuild.
func TestExportScriptOneTuningRowPerID(t *testing.T) {
	script := exportScript(t)
	if !strings.Contains(script, "SELECT ID, MAX(VerifiedBuild) AS VerifiedBuild FROM $hotdb.content_tuning GROUP BY ID") {
		t.Error("content_tuning is not joined through one row per ID (MAX(VerifiedBuild) GROUP BY ID)")
	}
	if regexp.MustCompile(`content_tuning\s+\w+\s+ON\s+\w+\.ID\s*=\s*d\.ContentTuningID`).MatchString(script) {
		t.Error("content_tuning is still joined directly on its primary key's first column")
	}
}

// TestExportScriptNormalSpawnsAndMissingTemplates: only normal-difficulty rows are exported (every creature query
// carries the spawnDifficulties filter), a spawn whose template row is missing is kept with defaults and counted in the
// summary, and the phase behaviour is written down.
func TestExportScriptNormalSpawnsAndMissingTemplates(t *testing.T) {
	script := exportScript(t)
	if !strings.Contains(script, `normal="FIND_IN_SET('0', REPLACE(c.spawnDifficulties, ' ', '')) > 0"`) {
		t.Error("the normal-difficulty filter is not defined")
	}
	if !strings.Contains(script, "SELECT DISTINCT c.map FROM creature c WHERE FIND_IN_SET('0', REPLACE(c.spawnDifficulties, ' ', '')) > 0") {
		t.Error("the --all map list does not use the normal-difficulty filter")
	}
	if n := strings.Count(script, "$normal"); n != 5 {
		t.Errorf("the normal-difficulty filter is used in %d creature queries, want 5 (the four blocks and the orphan count)", n)
	}
	if !strings.Contains(script, "FIND_IN_SET('0', REPLACE(g.spawnDifficulties, ' ', '')) > 0") {
		t.Error("the gameobject query does not filter spawnDifficulties")
	}
	if !strings.Contains(script, "LEFT JOIN creature_template t ON t.entry = c.id") {
		t.Error("creature_template is still an inner join: a spawn without a template row would vanish silently")
	}
	if !strings.Contains(script, `echo "spawns with no creature_template row: $orphans`) {
		t.Error("the summary does not count spawns without a creature_template row")
	}
	if !strings.Contains(script, "Phases are not filtered") {
		t.Error("the script does not say how phases are handled")
	}
}

// scriptEnv is the script's environment with the DUNGEONGEN_* variables replaced by extra, and a stub docker first on
// PATH so nothing reaches a real container.
func scriptEnv(t *testing.T, bin string, extra map[string]string) []string {
	t.Helper()
	env := []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "DUNGEONGEN_") || strings.HasPrefix(kv, "PATH=") {
			continue // PATH is set above: a second PATH entry would win over the stub's
		}
		env = append(env, kv)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// writeStubDocker writes a docker stub that records each call in called and exits with code.
func writeStubDocker(t *testing.T, dir string, code int) (bin, called string) {
	t.Helper()
	bin = filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	called = filepath.Join(dir, "docker-called")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + called + "\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, called
}

// TestExportScriptRejectsUnsafeEnv: DUNGEONGEN_* values reach a shell command line and a SQL string inside the
// container; a value outside the name-safe set must be refused before docker runs. A ';' would start a second command.
func TestExportScriptRejectsUnsafeEnv(t *testing.T) {
	dir := t.TempDir()
	bin, called := writeStubDocker(t, dir, 0)
	out := filepath.Join(dir, "out")
	cmd := exec.Command("bash", filepath.Join("..", "..", "tools", "dungeongen", "export-forever.sh"), "700", out)
	cmd.Env = scriptEnv(t, bin, map[string]string{"DUNGEONGEN_DB": "forever_world;touch " + filepath.Join(dir, "pwned")})
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("the script accepted a database name with ';'")
	}
	if !strings.Contains(stderr.String(), "DUNGEONGEN_DB") {
		t.Errorf("the error does not name DUNGEONGEN_DB: %q", stderr.String())
	}
	if _, err := os.Stat(called); err == nil {
		t.Error("docker was invoked with the unsafe value")
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("the injected command ran")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("the script created the output directory before validating its environment")
	}
}

// TestExportScriptKeepsPreviousExportOnFailure: files are staged in a sibling directory and moved into place only after
// every query finished, so a failed query leaves the previous export untouched instead of a mixed generation.
func TestExportScriptKeepsPreviousExportOnFailure(t *testing.T) {
	dir := t.TempDir()
	bin, _ := writeStubDocker(t, dir, 1) // every query fails
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"creatures.tsv", "groups.tsv"} {
		if err := os.WriteFile(filepath.Join(out, f), []byte("sentinel\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", filepath.Join("..", "..", "tools", "dungeongen", "export-forever.sh"), "700", out)
	cmd.Env = scriptEnv(t, bin, nil)
	if err := cmd.Run(); err == nil {
		t.Fatal("the script succeeded although every query failed")
	}
	for _, f := range []string{"creatures.tsv", "groups.tsv"} {
		b, err := os.ReadFile(filepath.Join(out, f))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "sentinel\n" {
			t.Errorf("%s = %q, want the previous export", f, b)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "out.tmp.") {
			t.Errorf("staging directory %s left behind", e.Name())
		}
	}
}

// writeAllStubDocker writes a docker stub for --all: it reads the SQL on stdin, answers the map-list query with maps
// (one per line), the orphan count with 0, every other query with no rows, and fails any query that names failMap.
func writeAllStubDocker(t *testing.T, dir, maps, failMap string) string {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nsql=$(cat)\n" +
		"case \"$sql\" in\n" +
		"  *'DISTINCT c.map'*) printf '" + maps + "';;\n"
	if failMap != "" {
		body += "  *'c.map = " + failMap + " '*) exit 1;;\n"
	}
	body += "  *'COUNT(*)'*) echo 0;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestExportScriptAllWritesEveryMap: --all lists the maps with a normal spawn and writes <root>/map<N> for each, the
// layout the plan tools read.
func TestExportScriptAllWritesEveryMap(t *testing.T) {
	dir := t.TempDir()
	bin := writeAllStubDocker(t, dir, `36\n389\n700\n`, "")
	root := filepath.Join(dir, "world")
	cmd := exec.Command("bash", filepath.Join("..", "..", "tools", "dungeongen", "export-forever.sh"), "--all", root)
	cmd.Env = scriptEnv(t, bin, nil)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--all: %v\n%s", err, out)
	}
	for _, m := range []string{"36", "389", "700"} {
		for _, f := range []string{"creatures.tsv", "groups.tsv", "movement.tsv", "gameobjects.tsv"} {
			if _, err := os.Stat(filepath.Join(root, "map"+m, f)); err != nil {
				t.Errorf("map %s: %v", m, err)
			}
		}
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 3 {
		t.Errorf("%d entries in the root, want 3 map folders", len(entries))
	}
}

// TestExportScriptAllStopsAtAFailingMap: a map whose queries fail stops the run with an error that names it; the maps
// before it stay exported and the later ones are not written.
func TestExportScriptAllStopsAtAFailingMap(t *testing.T) {
	dir := t.TempDir()
	bin := writeAllStubDocker(t, dir, `36\n389\n700\n`, "389")
	root := filepath.Join(dir, "world")
	cmd := exec.Command("bash", filepath.Join("..", "..", "tools", "dungeongen", "export-forever.sh"), "--all", root)
	cmd.Env = scriptEnv(t, bin, nil)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("--all succeeded although map 389 failed")
	}
	if !strings.Contains(string(out), "map 389") {
		t.Errorf("the error does not name map 389: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "map36", "creatures.tsv")); err != nil {
		t.Errorf("map 36 before the failure is not exported: %v", err)
	}
	for _, m := range []string{"map389", "map700"} {
		if _, err := os.Stat(filepath.Join(root, m)); err == nil {
			t.Errorf("%s exists after the run stopped at 389", m)
		}
	}
}

// TestExportScriptAllRejectsBadArgs: --all takes exactly a root folder, and a bare map argument still has to be a number.
func TestExportScriptAllRejectsBadArgs(t *testing.T) {
	dir := t.TempDir()
	bin := writeAllStubDocker(t, dir, "", "")
	script := filepath.Join("..", "..", "tools", "dungeongen", "export-forever.sh")
	for _, args := range [][]string{{"--all"}, {"--all", "a", "b"}, {"x", filepath.Join(dir, "o")}} {
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Env = scriptEnv(t, bin, nil)
		if err := cmd.Run(); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
	// --all with no map listed: nothing to export is an error, not an empty success.
	cmd := exec.Command("bash", script, "--all", filepath.Join(dir, "root"))
	cmd.Env = scriptEnv(t, bin, nil)
	if err := cmd.Run(); err == nil {
		t.Error("--all with no maps succeeded")
	}
}
