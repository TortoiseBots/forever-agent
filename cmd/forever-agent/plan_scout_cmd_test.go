package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/planscout"
)

// scoutNet is a flat, complete navmesh for the plan-scout command's tests.
type scoutNet struct{}

func (scoutNet) Floors(uint32, float32, float32, float32, float32) ([]navmesh.Floor, error) {
	return nil, nil
}
func (scoutNet) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) { return p, true, nil }
func (scoutNet) Raycast(uint32, navmesh.Vec3, navmesh.Vec3) (navmesh.Ray, error) {
	return navmesh.Ray{Known: true, Clear: true}, nil
}
func (scoutNet) ServerPath(_ uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	return navmesh.ServerPath{Type: navmesh.PathNormal, Points: []navmesh.Vec3{from, to}, ActualEnd: to,
		Length: float32(math.Hypot(float64(to[0]-from[0]), float64(to[1]-from[1])))}, nil
}
func (scoutNet) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to}}, nil
}
func (scoutNet) Close() {}

// useScoutNet injects the fake navmesh for one test.
func useScoutNet(t *testing.T) {
	t.Helper()
	old := planScoutOpen
	planScoutOpen = func(string) planScoutNav { return scoutNet{} }
	t.Cleanup(func() { planScoutOpen = old })
}

// writePlanFile writes a plan the scout can check: a via, a to, a pull group with an approach and a boss-free route.
func writePlanFile(t *testing.T, dir, name string, to float32) string {
	t.Helper()
	p := &dungeon.Plan{Version: 1, Name: name, Map: 1, Entrance: dungeon.Point{}, Legs: []dungeon.Leg{{
		Name: "one",
		To:   dungeon.Point{X: to, Y: 0, Z: 0},
		PullGroups: []dungeon.PullGroup{{
			Entries: []uint32{100}, Positions: []dungeon.Point{{X: to + 20, Y: 0, Z: 0}},
			Approach: []dungeon.Point{{X: to + 10, Y: 0, Z: 0}},
		}},
	}}}
	b, err := dungeon.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeMigration writes a migration with one plan of the given steps.
func writeMigration(t *testing.T, dir string, steps ...planexport.StepRow) string {
	t.Helper()
	for i := range steps {
		steps[i].Plan, steps[i].Leg, steps[i].Seq = "scout_m", 0, i
	}
	rows := planexport.Rows{Plan: planexport.PlanRow{Plan: "scout_m", Map: 1},
		Legs: []planexport.LegRow{{Plan: "scout_m", Leg: 0, Name: "one"}}, Steps: steps}
	path := filepath.Join(dir, "migration.sql")
	if err := os.WriteFile(path, []byte(planexport.WriteSQL([]planexport.Rows{rows})), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlanScoutUsageAndExitCodes(t *testing.T) {
	useScoutNet(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out")

	for _, args := range [][]string{
		{"plan-scout"},
		{"plan-scout", "Bad Name"},
		{"plan-scout", "missing_plan", "--dir", dir},
	} {
		if code, _, _ := runCLI(args...); code != exitUsage {
			t.Fatalf("%v: code %d, want %d", args, code, exitUsage)
		}
	}
	if code, _, errOut := runCLI("plan-scout", "x", "--live", "live-server"); code != exitLivePhaseRefused || !strings.Contains(errOut, "not a test server") {
		t.Fatalf("--live on a live server: code %d, %q", code, errOut)
	}
	if code, _, _ := runCLI("plan-scout", "x", "--slot", "3"); code != exitSlotRefused {
		t.Fatalf("--slot 3: code %d", code)
	}

	// A passing plan: 0.
	writePlanFile(t, dir, "scout_pass", 40)
	if code, _, errOut := runCLI("plan-scout", "scout_pass", "--dir", dir, "--out", out, "--mmaps", dir); code != exitOK {
		t.Fatalf("passing plan: code %d, %q", code, errOut)
	}
	// A 150 yd hop in a migration: 27.
	sql := writeMigration(t, dir, planexport.StepRow{Kind: planexport.KindMove, X: 1}, planexport.StepRow{Kind: planexport.KindMove, X: 150})
	if code, outText, _ := runCLI("plan-scout", "scout_m", "--sql", sql, "--dir", dir, "--out", out, "--mmaps", dir); code != exitPlanScoutFail || !strings.Contains(outText, "hop_len") {
		t.Fatalf("150 yd hop: code %d out %q", code, outText)
	}
	// A 121 yd hop is a warning: 0 by default, 27 with --fail-on warn.
	sql = writeMigration(t, dir, planexport.StepRow{Kind: planexport.KindMove, X: 1}, planexport.StepRow{Kind: planexport.KindMove, X: 122})
	if code, _, _ := runCLI("plan-scout", "scout_m", "--sql", sql, "--dir", dir, "--out", out, "--mmaps", dir); code != exitOK {
		t.Fatalf("121 yd hop: code %d", code)
	}
	if code, _, _ := runCLI("plan-scout", "scout_m", "--sql", sql, "--dir", dir, "--out", out, "--mmaps", dir, "--fail-on", "warn"); code != exitPlanScoutFail {
		t.Fatalf("121 yd hop --fail-on warn: code %d", code)
	}
}

func TestPlanScoutNoNavmesh(t *testing.T) {
	useScoutNet(t)
	planScoutOpen = func(string) planScoutNav { return navmesh.NewPlanner("") }
	dir := t.TempDir()
	writePlanFile(t, dir, "scout_pass", 40)
	code, _, errOut := runCLI("plan-scout", "scout_pass", "--dir", dir, "--out", filepath.Join(dir, "out"), "--mmaps", dir)
	if code != exitPlanScoutNoNavmesh || !strings.Contains(errOut, "navmesh") {
		t.Fatalf("code %d, %q", code, errOut)
	}
}

func TestPlanScoutFromUntil(t *testing.T) {
	useScoutNet(t)
	dir := t.TempDir()
	// Two legs: the second walks 10 yd.
	p := &dungeon.Plan{Version: 1, Name: "scout_two", Map: 1, Entrance: dungeon.Point{}, Legs: []dungeon.Leg{
		{Name: "one", To: dungeon.Point{X: 40}, PullGroups: []dungeon.PullGroup{{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 60}}}}},
		{Name: "two", To: dungeon.Point{X: 50}, PullGroups: []dungeon.PullGroup{{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 70}}}}},
	}}
	b, err := dungeon.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scout_two.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if code, _, errOut := runCLI("plan-scout", "scout_two", "--dir", dir, "--out", out, "--mmaps", dir, "--from", "one", "--until", "one"); code != exitOK {
		t.Fatalf("code %d, %q", code, errOut)
	}
	b, err = os.ReadFile(filepath.Join(out, "scout-scout_two.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Steps []struct {
			Leg int `json:"leg"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Steps) == 0 {
		t.Fatal("no steps")
	}
	for _, s := range rep.Steps {
		if s.Leg != 0 {
			t.Fatalf("leg %d in a leg-0 report", s.Leg)
		}
	}
}

func TestPlanScoutApplyWritesCopyOnly(t *testing.T) {
	useScoutNet(t)
	dir := t.TempDir()
	src := writePlanFile(t, dir, "scout_pass", 40)
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	applyTo := filepath.Join(dir, "patched")
	if code, _, errOut := runCLI("plan-scout", "scout_pass", "--dir", dir, "--out", out, "--mmaps", dir, "--apply-fixes", applyTo); code != exitOK {
		t.Fatalf("code %d, %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(applyTo, "scout_pass.json")); err != nil {
		t.Fatalf("no copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(applyTo, "scout_pass.diff")); err != nil {
		t.Fatalf("no diff: %v", err)
	}
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the plan file was written in place")
	}
	// Without --apply-fixes no patched copy is written anywhere.
	if _, err := os.Stat(filepath.Join(out, "scout_pass.json")); err == nil {
		t.Fatal("a patched copy was written without --apply-fixes")
	}
	// A DIR under a dungeons/ folder is refused.
	bad := filepath.Join(dir, "dungeons", "patched")
	if code, _, _ := runCLI("plan-scout", "scout_pass", "--dir", dir, "--out", out, "--mmaps", dir, "--apply-fixes", bad); code != exitUsage {
		t.Fatalf("a dungeons/ DIR: code %d", code)
	}
}

// TestPlanScoutFixInReplaysARecordedList: --fix-in applies a recorded fix file's list instead of the pass's own proposals, so a
// walk's fixes can be re-run; a file made for another plan or another export, and a live pass, are refused.
func TestPlanScoutFixInReplaysARecordedList(t *testing.T) {
	useScoutNet(t)
	dir := t.TempDir()
	writePlanFile(t, dir, "scout_fix", 40)
	out := filepath.Join(dir, "out")
	rec := filepath.Join(dir, "fix.json")
	// The empty fix file carries the export's sha256: fill it with one stand fix.
	if code, _, errOut := runCLI("plan-scout", "scout_fix", "--dir", dir, "--out", out, "--mmaps", dir, "--fix-out", rec); code != exitOK {
		t.Fatalf("--fix-out: code %d, %q", code, errOut)
	}
	var ff planscout.FixFile
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &ff); err != nil {
		t.Fatal(err)
	}
	ff.Fixes = []planscout.Fix{{
		ID: 1, Step: 2, Kind: planscout.FixMoveStand, Apply: true,
		Ref:   &planexport.RowOrigin{Leg: 0, Part: "pull", Group: 0},
		Stand: &planscout.FixStand{X: 45},
	}}
	nb, err := json.Marshal(ff)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, nb, 0o644); err != nil {
		t.Fatal(err)
	}
	applyTo := filepath.Join(dir, "patched")
	code, _, errOut := runCLI("plan-scout", "scout_fix", "--dir", dir, "--out", out, "--mmaps", dir, "--fix-in", rec, "--apply-fixes", applyTo)
	if code != exitOK {
		t.Fatalf("--fix-in --apply-fixes: code %d, %q", code, errOut)
	}
	patched, err := dungeon.Load(filepath.Join(applyTo, "scout_fix.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := patched.Legs[0].PullGroups[0].Approach[0].X; got != 45 {
		t.Fatalf("the stand from the recorded list = %v; want 45", got)
	}
	// A file for another plan is refused.
	other := ff
	other.Plan = "scout_other"
	nb, _ = json.Marshal(other)
	if err := os.WriteFile(filepath.Join(dir, "other.json"), nb, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCLI("plan-scout", "scout_fix", "--dir", dir, "--out", out, "--mmaps", dir, "--fix-in", filepath.Join(dir, "other.json")); code != exitUsage || !strings.Contains(errOut, "scout_other") {
		t.Fatalf("a file for another plan: code %d, %q", code, errOut)
	}
	// A file made for other rows is refused.
	stale := ff
	stale.RowsSHA256 = "0000"
	nb, _ = json.Marshal(stale)
	if err := os.WriteFile(filepath.Join(dir, "stale.json"), nb, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCLI("plan-scout", "scout_fix", "--dir", dir, "--out", out, "--mmaps", dir, "--fix-in", filepath.Join(dir, "stale.json")); code != exitUsage || !strings.Contains(errOut, "other rows") {
		t.Fatalf("a file for other rows: code %d, %q", code, errOut)
	}
	// A replay is not a live pass.
	if code, _, errOut := runCLI("plan-scout", "scout_fix", "--dir", dir, "--out", out, "--mmaps", dir, "--fix-in", rec, "--live", "forever"); code != exitUsage || !strings.Contains(errOut, "cannot be combined with --live") {
		t.Fatalf("--fix-in with --live: code %d, %q", code, errOut)
	}
}

// holeScoutNet is scoutNet with a square off the mesh, so an edit can add a plan-check issue.
type holeScoutNet struct{ scoutNet }

func (holeScoutNet) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	if p[0] >= 400 && p[0] <= 500 {
		return navmesh.Vec3{}, false, nil
	}
	return p, true, nil
}

// TestPlanScoutApplyDropsAFixThatAddsAPlanCheckIssue: a recorded fix that adds a plan-check issue is dropped into
// dropped.json with the issue it makes, and the other fix of the same list still lands.
func TestPlanScoutApplyDropsAFixThatAddsAPlanCheckIssue(t *testing.T) {
	old := planScoutOpen
	planScoutOpen = func(string) planScoutNav { return holeScoutNet{} }
	t.Cleanup(func() { planScoutOpen = old })
	dir := t.TempDir()
	writePlanFile(t, dir, "scout_hole", 40)
	out := filepath.Join(dir, "out")
	rec := filepath.Join(dir, "fix.json")
	if code, _, errOut := runCLI("plan-scout", "scout_hole", "--dir", dir, "--out", out, "--mmaps", dir, "--fix-out", rec); code != exitOK {
		t.Fatalf("--fix-out: code %d, %q", code, errOut)
	}
	var ff planscout.FixFile
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &ff); err != nil {
		t.Fatal(err)
	}
	ff.Fixes = []planscout.Fix{
		{ID: 1, Step: 1, Kind: planscout.FixMoveStand, Apply: true,
			Ref: &planexport.RowOrigin{Leg: 0, Part: "to", Group: -1}, Stand: &planscout.FixStand{X: 450}},
		{ID: 2, Step: 2, Kind: planscout.FixMoveStand, Apply: true,
			Ref: &planexport.RowOrigin{Leg: 0, Part: "pull", Group: 0}, Stand: &planscout.FixStand{X: 45}},
	}
	nb, err := json.Marshal(ff)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, nb, 0o644); err != nil {
		t.Fatal(err)
	}
	applyTo := filepath.Join(dir, "patched")
	if code, _, errOut := runCLI("plan-scout", "scout_hole", "--dir", dir, "--out", out, "--mmaps", dir, "--fix-in", rec, "--apply-fixes", applyTo); code != exitOK {
		t.Fatalf("--fix-in --apply-fixes: code %d, %q", code, errOut)
	}
	var dropped []planscout.DroppedFix
	b, err = os.ReadFile(filepath.Join(applyTo, "dropped.json"))
	if err != nil {
		t.Fatalf("no dropped.json for the refused fix: %v", err)
	}
	if err := json.Unmarshal(b, &dropped); err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0].ID != 1 || !strings.Contains(dropped[0].Reason, "off the mesh") {
		t.Fatalf("dropped = %+v; want fix 1 and the issue it makes (off the mesh)", dropped)
	}
	patched, err := dungeon.Load(filepath.Join(applyTo, "scout_hole.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := patched.Legs[0].To.X; got != 40 {
		t.Errorf("the dropped fix moved the leg's to point: X = %v; want 40", got)
	}
	if got := patched.Legs[0].PullGroups[0].Approach[0].X; got != 45 {
		t.Errorf("the accepted fix's stand X = %v; want 45", got)
	}
}
