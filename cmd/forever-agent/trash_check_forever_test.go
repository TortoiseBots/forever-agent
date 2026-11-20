package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/trashcheck"
)

// foreverTrashPlan is a small dungeon plan on flat ground: a pack on the
// route the walk uses, and a second pack far from it. The fixture mesh is
// flat, so the route is the server path on it and only the spawn list is
// judged.
const foreverTrashPlan = `{"version":1,"name":"ftc","map":70,"entrance":{"x":1,"y":2,"z":3},"level_band":[40,45],
 "legs":[{"name":"a","via":[{"x":20,"y":0,"z":1},{"x":40,"y":0,"z":1}],"to":{"x":60,"y":0,"z":1},
          "pull_groups":[{"entries":[100],"positions":[{"x":45,"y":2,"z":1}]},{"entries":[200],"positions":[{"x":500,"y":500,"z":1}]}]}]}`

// foreverTrashFixture writes the plan, its Forever migration, a spawn list
// with one spawn per pull position, and one flat Forever tile over the walk.
func foreverTrashFixture(t *testing.T) (dir, sql, spawns, mesh string) {
	t.Helper()
	dir = t.TempDir()
	plans := filepath.Join(dir, "dungeons")
	if err := os.MkdirAll(plans, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plans, "ftc.json"), []byte(foreverTrashPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := dungeon.Load(filepath.Join(plans, "ftc.json"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := planexport.BuildForever(p, planexport.Options{Source: "test"})
	if err != nil {
		t.Fatalf("BuildForever: %v", err)
	}
	sql = filepath.Join(dir, "plan.sql")
	if err := os.WriteFile(sql, []byte(planexport.WriteForeverSQL([]planexport.ForeverReport{rep})), 0o644); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	sb.WriteString("guid\tentry\tx\ty\tz\tmovetype\tkind\n")
	for i, u := range rep.Units {
		sb.WriteString(itoa(uint64(30030000+i)) + "\t" + itoa(uint64(u.Entry)) + "\t" +
			ftoa(u.X) + "\t" + ftoa(u.Y) + "\t" + ftoa(u.Z) + "\t0\tcreature\n")
	}
	spawns = filepath.Join(dir, "spawns.tsv")
	if err := os.WriteFile(spawns, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	mesh = foreverProbeMesh(t, p.Map)
	return dir, sql, spawns, mesh
}

func runForeverTrashCheck(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(t.Context(), append([]string{"trash-check"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// TestTrashCheckForeverDanger: the walk passes the pack the plan pulls, so
// the report names a danger; --fail-on danger exits 21.
func TestTrashCheckForeverDanger(t *testing.T) {
	dir, sql, spawns, mesh := foreverTrashFixture(t)
	code, stdout, stderr := runForeverTrashCheck(t, "--forever", "--sql", sql, "--spawns", spawns, "--mmaps", mesh,
		"--dir", filepath.Join(dir, "dungeons"))
	if code != exitOK {
		t.Fatalf("exit %d, want 0 (%s%s)", code, stdout, stderr)
	}
	for _, want := range []string{"DANGER: body pull", "no groups or links", "no waypoint loops"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("text lacks %q:\n%s", want, stdout)
		}
	}
	if code, _, _ := runForeverTrashCheck(t, "--forever", "--sql", sql, "--spawns", spawns, "--mmaps", mesh,
		"--dir", filepath.Join(dir, "dungeons"), "--fail-on", "danger"); code != exitTrashCheckFound {
		t.Errorf("--fail-on danger: exit %d, want %d", code, exitTrashCheckFound)
	}
	var rep trashcheck.Report
	code, stdout, _ = runForeverTrashCheck(t, "--forever", "--sql", sql, "--spawns", spawns, "--mmaps", mesh,
		"--dir", filepath.Join(dir, "dungeons"), "--json")
	if code != exitOK {
		t.Fatalf("--json: exit %d", code)
	}
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("--json: %v\n%s", err, stdout)
	}
	if rep.Plan != "ftc" || rep.Map != 70 {
		t.Errorf("report %q map %d, want ftc map 70", rep.Plan, rep.Map)
	}
	if rep.Counts.Danger == 0 {
		t.Errorf("no danger in counts %+v", rep.Counts)
	}
	if rep.Source != "forever-sql "+sql || rep.SpawnsDir != spawns {
		t.Errorf("source %q spawns %q", rep.Source, rep.SpawnsDir)
	}
}

// TestTrashCheckForeverNeedsInputs: Forever mode needs the exported
// migration and the spawn list, and takes no plan-JSON path.
func TestTrashCheckForeverNeedsInputs(t *testing.T) {
	dir, sql, spawns, mesh := foreverTrashFixture(t)
	dungeons := filepath.Join(dir, "dungeons")
	if code, _, stderr := runForeverTrashCheck(t, "--forever", "--spawns", spawns, "--mmaps", mesh, "ftc"); code != exitUsage {
		t.Errorf("no --sql: exit %d, want %d", code, exitUsage)
	} else if !strings.Contains(stderr, "--sql") {
		t.Errorf("no hint at --sql: %q", stderr)
	}
	if code, _, stderr := runForeverTrashCheck(t, "--forever", "--sql", sql, "--mmaps", mesh, "ftc"); code != exitUsage {
		t.Errorf("no --spawns: exit %d, want %d", code, exitUsage)
	} else if !strings.Contains(stderr, "--spawns") {
		t.Errorf("no hint at --spawns: %q", stderr)
	}
	_ = dungeons
	_ = sql
}

// TestTrashCheckForeverOldModeUnchanged: without --forever a missing world
// export still names the export script, and the old report still works.
func TestTrashCheckForeverOldModeUnchanged(t *testing.T) {
	dir, _ := trashFixture(t)
	code, _, stderr := runTrashCheck(t, "--dir", filepath.Join(dir, "dungeons"), "--spawns", filepath.Join(dir, "nope"), "tc")
	if code != exitUsage || !strings.Contains(stderr, "tools/dungeongen/export.sh") {
		t.Errorf("missing export: exit %d, %q", code, stderr)
	}
}
