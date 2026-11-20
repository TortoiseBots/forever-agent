package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
	"github.com/pjerra/forever-agent/internal/planexport"
)

// foreverStagePlan is a two-leg dungeon plan on flat ground: leg one's boss
// stands far from leg two's first point, so leg two stages clean. Every
// point sits inside one Detour grid cell (the fixture cannot share a
// corridor across cells): x and y stay well inside 0..533.
const foreverStagePlan = `{"version":1,"name":"fst","map":70,"entrance":{"x":100,"y":100,"z":0},"level_band":[40,45],
 "legs":[{"name":"one","to":{"x":140,"y":100,"z":0},
          "pull_groups":[{"entries":[100],"positions":[{"x":130,"y":105,"z":0}]}],
          "boss":{"entry":200,"position":{"x":140,"y":100,"z":0}}},
         {"name":"two","via":[{"x":180,"y":100,"z":0}],"to":{"x":220,"y":100,"z":0},
          "pull_groups":[{"entries":[101],"positions":[{"x":210,"y":105,"z":0}]}]}]}`

// foreverStageFixture writes the plan, its Forever migration and one flat
// Forever tile over the walk: leg one's packs stand far from leg two's
// first row, so leg two stages clean. The tile covers the whole walk at
// z=0; every stage point stands on it.
func foreverStageFixture(t *testing.T) (dir, sql, mesh string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fst.json"), []byte(foreverStagePlan), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := dungeon.Load(filepath.Join(dir, "fst.json"))
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
	mesh = foreverStageMesh(t, p.Map)
	return dir, sql, mesh
}

// foreverStageMesh is one flat Forever tile over the stage plan's walk.
func foreverStageMesh(t *testing.T, mapID uint32) string {
	t.Helper()
	return navfixture.WriteForeverDirAt(t, mapID,
		[]navfixture.Poly{navfixture.Square(0, 0, 300, 300, 0)}, nil, 150, 150)
}

func runStageCheck(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(t.Context(), append([]string{"stage-check"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// TestStageCheckForeverStages: leg two's first row (the migration's first
// move of leg 1) stands clear of leg one's packs, so the leg stages with
// the rows as the first-point source.
func TestStageCheckForeverStages(t *testing.T) {
	dir, sql, mesh := foreverStageFixture(t)
	code, stdout, stderr := runStageCheck(t, "--forever", "--dir", dir, "--mmaps", mesh, "--sql", sql, "fst", "two")
	if code != exitOK {
		t.Fatalf("exit %d, want 0 (%s%s)", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "fst leg 1 two: stage at") || !strings.Contains(stdout, "first point from rows") {
		t.Errorf("no staged line with the rows source:\n%s", stdout)
	}
	code, stdout, _ = runStageCheck(t, "--forever", "--dir", dir, "--mmaps", mesh, "--sql", sql, "--json", "fst", "two")
	if code != exitOK {
		t.Fatalf("--json: exit %d", code)
	}
	var rep stageCheckJSONOut
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("--json: %v\n%s", err, stdout)
	}
	if rep.FirstSource != "rows" || !rep.Staged && rep.Refusal != "" {
		t.Errorf("first source %q staged, refusal %q", rep.FirstSource, rep.Refusal)
	}
}

// TestStageCheckForeverNeedsSQL: Forever mode without the migration is a
// usage error that names it.
func TestStageCheckForeverNeedsSQL(t *testing.T) {
	dir, _, mesh := foreverStageFixture(t)
	if code, _, stderr := runStageCheck(t, "--forever", "--dir", dir, "--mmaps", mesh, "fst", "two"); code != exitUsage {
		t.Errorf("no --sql: exit %d, want %d", code, exitUsage)
	} else if !strings.Contains(stderr, "--sql") {
		t.Errorf("no hint at --sql: %q", stderr)
	}
}

// TestStageCheckOldModeUnchanged: without --forever the usage line still
// demands the plan and the leg.
func TestStageCheckOldModeUnchanged(t *testing.T) {
	if code, _, stderr := runStageCheck(t); code != exitUsage {
		t.Errorf("no args: exit %d, want %d", code, exitUsage)
	} else if !strings.Contains(stderr, "usage: forever-agent stage-check") {
		t.Errorf("no usage line: %q", stderr)
	}
}

// TestStageCheckLegacyRejectsSQL: --sql without --forever is a usage error
// naming --forever. The old-server run stages from the plan file on the v6
// tiles, so a migration would be silently ignored.
func TestStageCheckLegacyRejectsSQL(t *testing.T) {
	dir, sql, mesh := foreverStageFixture(t)
	if code, _, stderr := runStageCheck(t, "--dir", dir, "--mmaps", mesh, "--sql", sql, "fst", "two"); code != exitUsage {
		t.Errorf("--sql without --forever: exit %d, want %d", code, exitUsage)
	} else if !strings.Contains(stderr, "--forever") {
		t.Errorf("--sql without --forever names no mode: %q", stderr)
	}
}

// TestStageCheckLegacyRejectsPlan: --plan without --forever is a usage
// error naming --forever. It names a plan inside the --sql file, so without
// the migration it would be silently ignored.
func TestStageCheckLegacyRejectsPlan(t *testing.T) {
	dir, _, mesh := foreverStageFixture(t)
	if code, _, stderr := runStageCheck(t, "--dir", dir, "--mmaps", mesh, "--plan", "fst", "fst", "two"); code != exitUsage {
		t.Errorf("--plan without --forever: exit %d, want %d", code, exitUsage)
	} else if !strings.Contains(stderr, "--forever") {
		t.Errorf("--plan without --forever names no mode: %q", stderr)
	}
}

// TestStageCheckForeverRejectsServerSteps: --server-steps with --forever is
// a usage error naming the flag, in text and in --json. The migration fixes
// the exported rows, so no exporter option set is picked: accepting the
// flag would pretend it affected the result.
func TestStageCheckForeverRejectsServerSteps(t *testing.T) {
	dir, sql, mesh := foreverStageFixture(t)
	for _, extra := range [][]string{{}, {"--json"}} {
		args := append([]string{"--forever", "--dir", dir, "--mmaps", mesh, "--sql", sql, "--server-steps", "40"}, extra...)
		args = append(args, "fst", "two")
		if code, _, stderr := runStageCheck(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", extra, code, exitUsage)
		} else if !strings.Contains(stderr, "--server-steps") {
			t.Errorf("%v: no hint at --server-steps: %q", extra, stderr)
		}
	}
}

// TestStageCheckForeverBogusMmaps: a missing --mmaps folder is a usage
// error naming it, not a leg refusal. Without v16 tiles the staging walk
// cannot run: plan-check, trash-check and plan-probe report the missing
// navmesh up front instead of judging the plan against nothing.
func TestStageCheckForeverBogusMmaps(t *testing.T) {
	dir, sql, _ := foreverStageFixture(t)
	for _, extra := range [][]string{{}, {"--json"}} {
		args := append([]string{"--forever", "--dir", dir, "--mmaps", filepath.Join(t.TempDir(), "no-tiles"), "--sql", sql}, extra...)
		args = append(args, "fst", "two")
		if code, _, stderr := runStageCheck(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", extra, code, exitUsage)
		} else if !strings.Contains(stderr, "--mmaps") {
			t.Errorf("%v: no hint at --mmaps: %q", extra, stderr)
		}
	}
}

// TestStageCheckForeverWrongGenerationMmaps: a v6 tile set (wrong
// generation, not missing files) is a usage error too: rows rebuilt
// against one tile set prove nothing about the other.
func TestStageCheckForeverWrongGenerationMmaps(t *testing.T) {
	dir, sql, _ := foreverStageFixture(t)
	v6 := t.TempDir()
	if err := os.WriteFile(filepath.Join(v6, "70.mmap"), []byte("v6"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runStageCheck(t, "--forever", "--dir", dir, "--mmaps", v6, "--sql", sql, "fst", "two"); code != exitUsage {
		t.Errorf("exit %d, want %d", code, exitUsage)
	} else if !strings.Contains(stderr, "--mmaps") {
		t.Errorf("no hint at --mmaps: %q", stderr)
	}
}
