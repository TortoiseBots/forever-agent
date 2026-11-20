package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/planprobe"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// foreverProbePlan is a small dungeon plan on flat ground: two legs, three
// pulls, a patrol and a boss, every point within 200 yd of the others. The
// fixture tile below covers the whole walk, so every hop is complete on the
// mesh and only the unit match is judged.
const foreverProbePlan = `{"version":1,"name":"probeever","map":70,"entrance":{"x":10,"y":10,"z":0},"level_band":[40,45],
 "legs":[
  {"name":"one","via":[{"x":20,"y":12,"z":0}],"to":{"x":30,"y":12,"z":0},
   "pull_groups":[{"entries":[100],"positions":[{"x":32,"y":16,"z":0}]},{"entries":[101,102],"positions":[{"x":44,"y":14,"z":0},{"x":46,"y":18,"z":0}]}]},
  {"name":"two","via":[{"x":60,"y":20,"z":0}],"to":{"x":80,"y":20,"z":0},
   "pull_groups":[{"entries":[103],"positions":[{"x":82,"y":24,"z":0}],"patrol":{"entries":[104],"path":[{"x":70,"y":30,"z":0},{"x":90,"y":30,"z":0}],"period_s":20}}],
   "boss":{"entry":200,"position":{"x":110,"y":20,"z":0},"first":[201]}}]}`

// foreverProbeMesh is one flat Forever tile over the plan's own cell: every
// hop is complete on the mesh, so only the unit match is judged. The tile
// covers the whole walk in one Detour grid cell (tiles never share a
// corridor across cells in the fixture: cross-cell walks come back partial
// for want of a portal, not for want of mesh).
func foreverProbeMesh(t *testing.T, mapID uint32) string {
	t.Helper()
	return navfixture.WriteForeverDirAt(t, mapID,
		[]navfixture.Poly{navfixture.Square(-100, -100, 200, 200, 0)}, nil, 50, 50)
}

// foreverProbeExport writes the small plan's Forever migration plus a spawn
// list holding one spawn per unit position: every unit matches by entry
// plus position, so the report is clean.
func foreverProbeExport(t *testing.T) (sql, spawns, mesh string) {
	t.Helper()
	dir := t.TempDir()
	planPath := filepath.Join(dir, "probeever.json")
	if err := os.WriteFile(planPath, []byte(foreverProbePlan), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := dungeon.Load(planPath)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := planexport.BuildForever(p, planexport.Options{Source: "test"})
	if err != nil {
		t.Fatalf("BuildForever: %v", err)
	}
	if len(rep.Units) == 0 {
		t.Fatal("no unit rows")
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
	return sql, spawns, mesh
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

func ftoa(v float32) string { return strconv.FormatFloat(float64(v), 'f', 3, 32) }

// TestPlanProbeForeverClean: every unit of the export matches a spawn of its
// entry at its position, and every hop is complete on the flat tile: exit 0,
// the text names the unit count with no missing.
func TestPlanProbeForeverClean(t *testing.T) {
	sql, spawns, mesh := foreverProbeExport(t)
	var out, errOut bytes.Buffer
	code := planProbeCmd([]string{"--forever", "--sql", sql, "--spawns", spawns, "--mmaps", mesh}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d, want 0 (stderr %q, stdout %q)", code, errOut.String(), out.String())
	}
	if !strings.Contains(out.String(), "0 missing") {
		t.Errorf("report does not say no unit is missing:\n%s", out.String())
	}
}

// TestPlanProbeForeverMissingUnit: without one unit's spawn the probe names
// the missing unit's entry and exits 20, like a bad segment.
func TestPlanProbeForeverMissingUnit(t *testing.T) {
	sql, spawns, mesh := foreverProbeExport(t)
	data, err := os.ReadFile(spawns)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("spawn list has %d lines", len(lines))
	}
	missing := lines[1]
	if err := os.WriteFile(spawns, []byte(strings.Join(append(lines[:1], lines[2:]...), "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var entry string
	if f := strings.Split(missing, "\t"); len(f) > 1 {
		entry = f[1]
	}
	var out, errOut bytes.Buffer
	code := planProbeCmd([]string{"--forever", "--sql", sql, "--spawns", spawns, "--mmaps", mesh}, &out, &errOut)
	if code != exitPlanProbeBad {
		t.Fatalf("exit %d, want %d (stderr %q, stdout %q)", code, exitPlanProbeBad, errOut.String(), out.String())
	}
	if !strings.Contains(out.String(), "MISSING") || !strings.Contains(out.String(), entry) {
		t.Errorf("report names no missing unit of entry %s:\n%s", entry, out.String())
	}
}

// TestPlanProbeForeverJSON keeps the Forever report's shape: the probe's
// tb-plan-probe format with the spawn file named and the per-unit matches.
func TestPlanProbeForeverJSON(t *testing.T) {
	sql, spawns, mesh := foreverProbeExport(t)
	var out, errOut bytes.Buffer
	code := planProbeCmd([]string{"--forever", "--sql", sql, "--spawns", spawns, "--mmaps", mesh, "--json"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d, want 0 (%s)", code, errOut.String())
	}
	var rep foreverProbeReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("--json: %v\n%s", err, out.String())
	}
	if rep.Format != "tb-plan-probe" || rep.Version != 1 {
		t.Errorf("format %q v%d, want tb-plan-probe v1", rep.Format, rep.Version)
	}
	if rep.Spawns != spawns || rep.Misses != 0 || len(rep.Units) == 0 {
		t.Errorf("spawns %q misses %d units %d", rep.Spawns, rep.Misses, len(rep.Units))
	}
	for _, u := range rep.Units {
		if !u.Found || u.Missing || u.GUID == 0 {
			t.Errorf("unit %+v is not a match", u)
		}
	}
	if len(rep.Segments) == 0 {
		t.Errorf("no segments in the --json report")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("--json raw: %v", err)
	}
	if _, ok := raw["segments"]; !ok {
		t.Errorf("--json has no \"segments\" key")
	}
	var asProbe planprobe.Report
	if err := json.Unmarshal(out.Bytes(), &asProbe); err != nil {
		t.Fatalf("--json does not decode as tb-plan-probe: %v", err)
	}
	if len(asProbe.Segments) != len(rep.Segments) ||
		asProbe.Counts.OK != rep.Counts.OK || asProbe.Counts.Warn != rep.Counts.Warn || asProbe.Counts.Bad != rep.Counts.Bad {
		t.Errorf("segments/counts diverge: probe %+v vs forever %+v/%d segs", asProbe.Counts, rep.Counts, len(rep.Segments))
	}
}

// TestPlanProbeForeverFirstAddRadius: a first add standing 25 yd from the
// boss's point still matches: its row stands at the boss's point, so it is
// searched with the boss radius plus the pull-off slack, not the 20 yd pack
// radius. The boss keeps the same wider search.
func TestPlanProbeForeverFirstAddRadius(t *testing.T) {
	mkSpawn := func(x float32) worldspawn.Spawn {
		return worldspawn.Spawn{Entry: 201, Pos: dungeon.Point{X: x, Y: 20}}
	}
	for _, role := range []string{planexport.UnitFirst, planexport.UnitBoss} {
		units := []planexport.UnitRow{{Plan: "probeever", Leg: 1, Seq: 0, Idx: 1, Entry: 201, X: 110, Y: 20, Z: 0, Role: role}}
		got, misses := foreverProbeUnits(nil, units, []worldspawn.Spawn{mkSpawn(135)})
		if misses != 0 || len(got) != 1 || !got[0].Found {
			t.Errorf("role %s 25 yd off: misses=%d found=%v (want a match)", role, misses, got)
		}
		far, farMisses := foreverProbeUnits(nil, units, []worldspawn.Spawn{mkSpawn(200)})
		if farMisses != 1 || len(far) != 1 || far[0].Found {
			t.Errorf("role %s 90 yd off: misses=%d (want a miss)", role, farMisses)
		}
	}
}

// TestPlanProbeForeverPackRadius: a pack spawn 25 yd from the unit row's
// anchor still matches when the step's pull radius is 30: the row is
// searched with the step's own radius plus the 5 yd slack, not the 20 yd
// default. A spawn past radius+slack still misses.
func TestPlanProbeForeverPackRadius(t *testing.T) {
	steps := []planexport.StepRow{{Plan: "probeever", Leg: 0, Seq: 0, Kind: planexport.KindPull, X: 32, Y: 16, Z: 0, Radius: 30}}
	units := []planexport.UnitRow{{Plan: "probeever", Leg: 0, Seq: 0, Idx: 0, Entry: 100, X: 32, Y: 16, Z: 0, Role: planexport.UnitPack}}
	mkSpawn := func(x float32) worldspawn.Spawn {
		return worldspawn.Spawn{Entry: 100, Pos: dungeon.Point{X: x, Y: 16}}
	}
	got, misses := foreverProbeUnits(steps, units, []worldspawn.Spawn{mkSpawn(57)})
	if misses != 0 || len(got) != 1 || !got[0].Found {
		t.Errorf("25 yd off with radius 30: misses=%d found=%v (want a match)", misses, got)
	}
	far, farMisses := foreverProbeUnits(steps, units, []worldspawn.Spawn{mkSpawn(100)})
	if farMisses != 1 || len(far) != 1 || far[0].Found {
		t.Errorf("68 yd off with radius 30: misses=%d (want a miss)", farMisses)
	}
}

// TestPlanProbeForeverNeedsSQLAndSpawns: the plan-JSON path and a missing
// spawn list are usage errors that say what Forever mode reads.
func TestPlanProbeForeverNeedsSQLAndSpawns(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := planProbeCmd([]string{"--forever", "--mmaps", "mm", "probeever"}, &out, &errOut); code != exitUsage {
		t.Errorf("no --sql: exit %d, want %d", code, exitUsage)
	}
	sql, _, mesh := foreverProbeExport(t)
	out.Reset()
	errOut.Reset()
	if code := planProbeCmd([]string{"--forever", "--sql", sql, "--mmaps", mesh}, &out, &errOut); code != exitUsage {
		t.Errorf("no --spawns: exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "--spawns") {
		t.Errorf("no hint at --spawns: %q", errOut.String())
	}
}

// TestPlanProbeForeverOldModeUnchanged: --spawns without --forever is a usage
// error, and the old mode probes a migration without it.
func TestPlanProbeForeverOldModeUnchanged(t *testing.T) {
	mig := writeProbeMigration(t, planexport.Rows{
		Plan: planexport.PlanRow{Plan: "probe_fixture", Map: 249, Source: "test"},
		Legs: []planexport.LegRow{{Plan: "probe_fixture", Leg: 0, Name: "one"}},
		Steps: []planexport.StepRow{
			{Plan: "probe_fixture", Leg: 0, Seq: 0, Kind: planexport.KindMove, X: 10, Comment: "to"},
			{Plan: "probe_fixture", Leg: 0, Seq: 1, Kind: planexport.KindMove, X: 20, Comment: "path"},
		},
	})
	useFakeProbeNav(t, cmdFakeNav{}, nil)
	var out, errOut bytes.Buffer
	if code := planProbeCmd([]string{"--sql", mig, "--spawns", "x", "--mmaps", "mm"}, &out, &errOut); code != exitUsage {
		t.Errorf("--spawns without --forever: exit %d, want %d", code, exitUsage)
	}
	out.Reset()
	errOut.Reset()
	if code := planProbeCmd([]string{"--mmaps", "mm", "--sql", mig}, &out, &errOut); code != exitOK {
		t.Fatalf("old mode: exit %d, want 0 (%s)", code, errOut.String())
	}
	_ = math.Pi
	_ = navmesh.Planner{}
}
