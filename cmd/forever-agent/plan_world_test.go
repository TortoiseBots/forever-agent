package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trashcheck"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// The creatures.tsv header export-forever.sh writes (tools/dungeongen/export-forever.sh, the printf before the creature
// query): the 21 columns of the 1.12 export with group_ai in place of group_flags.
const fwCreatureHeader = "guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\tdetection_range\tflags_extra\tspeed_walk\tgroup_ai\tpool\n"

// fwCreature is one hand-made creatures.tsv row: a level 40 spawn of faction 14, standing still, no group, detection empty
// (the Forever export leaves it empty, the reader defaults it to 20 yd).
func fwCreature(guid, entry int, x, y, z float32, leader, groupAI int) string {
	return fmt.Sprintf("%d\t%d\tmob%d\t40\t40\t0\t14\t0\t%g\t%g\t%g\t0\t0\t%d\t0\t\t\t0\t1\t%d\t0\n", guid, entry, entry, x, y, z, leader, groupAI)
}

// fwWriteMap writes one map's export folder: creatures.tsv from rows and, when groups is not empty, groups.tsv.
func fwWriteMap(t *testing.T, dir string, rows []string, groups string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "creatures.tsv"), []byte(fwCreatureHeader+strings.Join(rows, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	if groups != "" {
		if err := os.WriteFile(filepath.Join(dir, "groups.tsv"), []byte("leader_guid\tmember_guid\tdist\tangle\tgroup_ai\n"+groups), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fwRoot is a world root with two maps (the --all layout): map70 holds three spawns and a group, map71 holds one.
func fwRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	fwWriteMap(t, filepath.Join(root, "map70"), []string{
		fwCreature(1, 100, 10, 10, 0, 1, 1),
		fwCreature(2, 100, 12, 10, 0, 1, 1),
		fwCreature(3, 300, 50, 50, 0, 0, 0),
	}, "1\t1\t0\t0\t1\n1\t2\t3\t90\t1\n")
	fwWriteMap(t, filepath.Join(root, "map71"), []string{fwCreature(9, 900, 1, 2, 3, 0, 0)}, "")
	return root
}

// TestLoadForeverWorldPicksTheMapOfARoot: a world root serves every map: the plan's map picks its folder, the world is
// the full export (groups read, levels read), and each map sees only its own spawns.
func TestLoadForeverWorldPicksTheMapOfARoot(t *testing.T) {
	root := fwRoot(t)
	w70, err := loadForeverWorld(root, 70)
	if err != nil {
		t.Fatal(err)
	}
	if !w70.Full || len(w70.World.Spawns) != 3 || w70.Path != filepath.Join(root, "map70") {
		t.Fatalf("map 70: full=%v spawns=%d path=%s", w70.Full, len(w70.World.Spawns), w70.Path)
	}
	for _, s := range w70.World.Spawns {
		if s.GUID == 2 && (s.GroupLeader != 1 || s.GroupFlags&worldspawn.AggroTogether == 0) {
			t.Errorf("spawn 2 group leader %d flags %#x, want leader 1 aggro together", s.GroupLeader, s.GroupFlags)
		}
		if s.LevelMax != 40 {
			t.Errorf("spawn %d level %d, want the export's 40", s.GUID, s.LevelMax)
		}
	}
	// a stray creatures.tsv in the root (another map's single export) never wins over the plan's map folder
	fwWriteMap(t, root, []string{fwCreature(77, 777, 0, 0, 0, 0, 0)}, "")
	if again, err := loadForeverWorld(root, 70); err != nil || len(again.World.Spawns) != 3 {
		t.Fatalf("root with a stray creatures.tsv: %v %+v", err, again)
	}
	w71, err := loadForeverWorld(root, 71)
	if err != nil {
		t.Fatal(err)
	}
	if len(w71.World.Spawns) != 1 || w71.World.Spawns[0].Entry != 900 {
		t.Fatalf("map 71 spawns %+v, want the one entry-900 spawn", w71.World.Spawns)
	}
}

// TestLoadForeverWorldShapes: a single map's folder and the thin TSV list still load; the single folder says it carries
// no map id, the thin list says it carries no groups.
func TestLoadForeverWorldShapes(t *testing.T) {
	root := fwRoot(t)
	one, err := loadForeverWorld(filepath.Join(root, "map70"), 70)
	if err != nil {
		t.Fatal(err)
	}
	if !one.Full || len(one.World.Spawns) != 3 || !strings.Contains(strings.Join(one.Notes, ";"), "carry no map id") {
		t.Errorf("single folder: full=%v spawns=%d notes=%v", one.Full, len(one.World.Spawns), one.Notes)
	}
	tsv := filepath.Join(t.TempDir(), "spawns.tsv")
	if err := os.WriteFile(tsv, []byte("guid\tentry\tx\ty\tz\tmovetype\n1\t100\t1\t2\t3\t0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	thin, err := loadForeverWorld(tsv, 70)
	if err != nil {
		t.Fatal(err)
	}
	if thin.Full || len(thin.World.Spawns) != 1 {
		t.Errorf("thin list: full=%v spawns=%d", thin.Full, len(thin.World.Spawns))
	}
}

// TestLoadForeverWorldMissingMap: a root with no folder for the plan's map names the map, the root and both places it
// looked, and says which script writes it; a missing path is the file system's own error.
func TestLoadForeverWorldMissingMap(t *testing.T) {
	root := fwRoot(t)
	_, err := loadForeverWorld(root, 72)
	if !errors.Is(err, errNoForeverWorld) {
		t.Fatalf("error %v, want errNoForeverWorld", err)
	}
	for _, want := range []string{"map 72", root, filepath.Join(root, "map72", "creatures.tsv"), "export-forever.sh"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if _, err := loadForeverWorld(filepath.Join(root, "nope"), 70); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing path: %v, want not-exist", err)
	}
	// a folder that holds a map folder without creatures.tsv is a missing map too
	if err := os.MkdirAll(filepath.Join(root, "map73"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := loadForeverWorld(root, 73); !errors.Is(err, errNoForeverWorld) {
		t.Errorf("empty map folder: %v, want errNoForeverWorld", err)
	}
}

// TestLoadForeverWorldBadRow: a bad row of the export is an error that names the folder and the row, never a silent
// skip: a level that is not positive (the typed ErrLevelZero), a non-numeric position, a header-only map.
func TestLoadForeverWorldBadRow(t *testing.T) {
	root := t.TempDir()
	bad := fwCreature(1, 100, 1, 2, 3, 0, 0)
	fwWriteMap(t, filepath.Join(root, "map70"), []string{strings.Replace(bad, "\t40\t40\t", "\t0\t0\t", 1)}, "")
	if _, err := loadForeverWorld(root, 70); !errors.Is(err, worldspawn.ErrLevelZero) {
		t.Errorf("level 0: %v, want ErrLevelZero", err)
	}
	fwWriteMap(t, filepath.Join(root, "map71"), []string{strings.Replace(bad, "\t1\t2\t3\t", "\tx\t2\t3\t", 1)}, "")
	if _, err := loadForeverWorld(root, 71); err == nil || !strings.Contains(err.Error(), filepath.Join(root, "map71")) {
		t.Errorf("bad position: %v, want an error naming map71's folder", err)
	}
	fwWriteMap(t, filepath.Join(root, "map72"), nil, "")
	if _, err := loadForeverWorld(root, 72); !errors.Is(err, worldspawn.ErrNoSpawns) {
		t.Errorf("header only: %v, want ErrNoSpawns", err)
	}
}

// TestWorldForLintsForeverReadsAnExport: plan-check's lints read the full export from a world root or a map folder; a
// root without the map skips the lints with the reason instead of failing.
func TestWorldForLintsForeverReadsAnExport(t *testing.T) {
	root := fwRoot(t)
	w, note := worldForLintsForever(root, 70)
	if w == nil || len(w.Spawns) != 3 {
		t.Fatalf("root: world %v note %q", w, note)
	}
	if w, note := worldForLintsForever(root, 72); w != nil || !strings.Contains(note, "lints skipped") || !strings.Contains(note, "map 72") {
		t.Errorf("missing map: world %v note %q", w, note)
	}
}

// fwTrashRoot writes a world root for the foreverTrashPlan fixture (map 70): the plan's own pull (entry 100) and the
// far one (entry 200) as in the thin fixture, plus an unplanned linked pack (entry 300) beside the route: its leader 3 yd
// off the walk, its follower 27 yd from the leader and 30 yd from the walk. Only the group (group_ai 1: members assist the
// leader) brings the follower along.
func fwTrashRoot(t *testing.T, groupAI int) string {
	t.Helper()
	root := t.TempDir()
	groups := ""
	if groupAI != 0 {
		groups = fmt.Sprintf("31\t31\t0\t0\t%d\n31\t32\t3\t90\t%d\n", groupAI, groupAI)
	}
	fwWriteMap(t, filepath.Join(root, "map70"), []string{
		fwCreature(10, 100, 45, 2, 1, 0, 0),
		fwCreature(11, 200, 500, 500, 1, 0, 0),
		fwCreature(31, 300, 30, 3, 1, 31, groupAI),
		fwCreature(32, 300, 30, 30, 1, 31, groupAI),
	}, groups)
	return root
}

func fwTrashJSON(t *testing.T, spawns string, extra ...string) trashcheck.Report {
	t.Helper()
	dir, sql, _, mesh := foreverTrashFixture(t)
	args := append([]string{"--forever", "--sql", sql, "--spawns", spawns, "--mmaps", mesh, "--dir", filepath.Join(dir, "dungeons"), "--json"}, extra...)
	var out, errb bytes.Buffer
	if code := run(t.Context(), append([]string{"trash-check"}, args...), &out, &errb); code != exitOK {
		t.Fatalf("exit %d: %s%s", code, out.String(), errb.String())
	}
	var rep trashcheck.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return rep
}

func fwPackOf(rep trashcheck.Report, guid uint64) *trashcheck.PackRow {
	for i := range rep.Packs {
		for _, m := range rep.Packs[i].Members {
			if m == guid {
				return &rep.Packs[i]
			}
		}
	}
	return nil
}

// TestTrashCheckForeverFullExportLinkedPack is the issue's acceptance in miniature: the walk passes an unplanned linked
// pack, the full export's formation makes the pack two spawns that wake as one (the thin list judged each spawn alone),
// and --fail-on danger fails the plan. Without the group row (group_ai 0) the leader alone wakes.
func TestTrashCheckForeverFullExportLinkedPack(t *testing.T) {
	rep := fwTrashJSON(t, fwTrashRoot(t, 1))
	pack := fwPackOf(rep, 31)
	if pack == nil || len(pack.Members) != 2 || fwPackOf(rep, 32) != pack {
		t.Fatalf("pack of 31: %+v, want one pack of 31 and 32", pack)
	}
	if !pack.Unplanned {
		t.Errorf("the linked pack is planned: %+v", pack)
	}
	var danger bool
	for _, s := range rep.Segments {
		for _, p := range s.Packs {
			danger = danger || p.Pack == pack.ID && p.Class == "danger" && p.Brings == 2
		}
	}
	if !danger {
		t.Errorf("no segment wakes the linked pack as a danger bringing 2 spawns: %+v", rep.Segments)
	}
	dir, sql, _, mesh := foreverTrashFixture(t)
	var out, errb bytes.Buffer
	code := run(t.Context(), []string{"trash-check", "--forever", "--sql", sql, "--spawns", fwTrashRoot(t, 1), "--mmaps", mesh,
		"--dir", filepath.Join(dir, "dungeons"), "--fail-on", "danger"}, &out, &errb)
	if code != exitTrashCheckFound {
		t.Errorf("--fail-on danger: exit %d, want %d\n%s%s", code, exitTrashCheckFound, out.String(), errb.String())
	}
	if strings.Contains(out.String(), "no groups or links") {
		t.Errorf("the full export's report still says the list carries no groups:\n%s", out.String())
	}
	plain := fwTrashJSON(t, fwTrashRoot(t, 0))
	if p := fwPackOf(plain, 31); p == nil || len(p.Members) != 1 {
		t.Errorf("without a formation row the leader is alone: %+v", p)
	}
}

// TestTrashCheckForeverAssistDefaultsToTheRing: Forever mode takes the port's family assist ring (never under 20 yd,
// kAggroFamilyAssistMinYd) as its default, and --assist-yd still sets it.
func TestTrashCheckForeverAssistDefaultsToTheRing(t *testing.T) {
	root := fwTrashRoot(t, 0)
	if got := fwTrashJSON(t, root).AssistYd; got != 20 || foreverAssistYd != 20 {
		t.Errorf("default assist %g (const %g), want 20: the port's kAggroFamilyAssistMinYd", got, foreverAssistYd)
	}
	if got := fwTrashJSON(t, root, "--assist-yd", "7").AssistYd; got != 7 {
		t.Errorf("--assist-yd 7: assist %g, want 7", got)
	}
}

// TestPlanProbeForeverReadsAnExport: plan-probe --forever matches the rows' units against a world root's map folder, and
// a root without the map is a usage error that names the map.
func TestPlanProbeForeverReadsAnExport(t *testing.T) {
	dir, sql, _, mesh := foreverTrashFixture(t)
	root := fwTrashRoot(t, 0)
	var out, errb bytes.Buffer
	code := run(t.Context(), []string{"plan-probe", "--forever", "--sql", sql, "--spawns", root, "--mmaps", mesh, "--dir", filepath.Join(dir, "dungeons")}, &out, &errb)
	if code != exitOK && code != exitPlanProbeBad {
		t.Fatalf("exit %d: %s%s", code, out.String(), errb.String())
	}
	out.Reset()
	errb.Reset()
	empty := t.TempDir()
	if code := run(t.Context(), []string{"plan-probe", "--forever", "--sql", sql, "--spawns", empty, "--mmaps", mesh, "--dir", filepath.Join(dir, "dungeons")}, &out, &errb); code != exitUsage ||
		!strings.Contains(errb.String(), "map 70") {
		t.Errorf("missing map: exit %d %q, want usage naming map 70", code, errb.String())
	}
}
