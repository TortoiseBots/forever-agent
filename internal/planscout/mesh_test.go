//go:build cgo

package planscout

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/planlint"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// zgMmaps finds the map 309 navmesh files: $TURTLE_AGENT_MMAPS, the VM's per-map folder, or the server host's folder.
func zgMmaps(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle309"), filepath.Join(home, "tortoise-wow-server", "data", "mmaps")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "309.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(d, "309*.mmtile")); len(tiles) == 0 {
			continue
		}
		p := navmesh.NewPlanner(d)
		t.Cleanup(p.Close)
		return p
	}
	t.Skip("no map 309 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles")
	return nil
}

// passBudget is how much work the offline pass may do. It counts the test process's CPU time, not the wall clock: the
// whole module's suite runs every package at once, so on an oversubscribed runner the pass waits for a CPU it does not
// use, and the wall clock reads the runner's load rather than the pass's work (in such a run every CPU-bound package
// read 7-20x slower, internal/navmesh at 1.169 s against 0.109 s). A pass that really slows down spends the CPU time.
const passBudget = 30 * time.Second

// cpuTime is the test process's user and system time so far.
func cpuTime(t *testing.T) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatalf("Getrusage: %v", err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// TestZGWalk31OfflineCatchesF1F3F4F5: the walk-31 rows and the real ZG navmesh catch the four failures the walk hit (f step
// 23's gather ledge, f step 16's floor, the Arlokk pull's joiners, the water of rows 7-9) well inside the runtime target.
func TestZGWalk31OfflineCatchesF1F3F4F5(t *testing.T) {
	planner := zgMmaps(t)
	b, err := os.ReadFile(filepath.Join("testdata", "zg-20261103050000.sql"))
	if err != nil {
		t.Fatal(err)
	}
	all, _, err := planexport.ParseMigration(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("%d plans", len(all))
	}
	w, _, err := worldspawn.Load(filepath.Join("..", "..", "tools", "dungeongen", "testdata", "map309"), 309)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := dungeon.Load(filepath.Join("..", "..", "dungeons", "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	start, cpu := time.Now(), cpuTime(t)
	rep, err := Offline(Input{Rows: all[0], Plan: plan}, planner, w, Options{PartyLevel: 60})
	if err != nil {
		t.Fatal(err)
	}
	wall, work := time.Since(start), cpuTime(t)-cpu
	t.Logf("the offline pass took %s wall, %s cpu", wall, work)
	if work > passBudget {
		t.Fatalf("the offline pass spent %s cpu (%s wall) (target %s)", work, wall, passBudget)
	}
	failed := func(step int, name string) bool {
		for _, s := range rep.Steps {
			if s.Step != step {
				continue
			}
			for _, c := range s.Checks {
				if c.Name == name && c.Verdict == VerdictFail {
					return true
				}
			}
		}
		return false
	}
	// F1: the stand above its anchor (the finding lands on the stand row 22).
	if !failed(22, planlint.LintClimb) {
		t.Errorf("step 22: no climb_anchor failure")
	}
	// F4: the point with no floor (f step 16).
	if !failed(16, planlint.LintOffFloor) {
		t.Errorf("step 16: no off-floor failure")
	}
	// F5: the Arlokk pull's joiners.
	if !failed(366, planlint.LintJoiners) {
		t.Errorf("step 366: no joiners failure")
	}
	// F3/river: leg 1's rows 7-9 (steps 71-73 of the export) are the Mar'li water.
	watered := false
	for step := 71; step <= 73 && !watered; step++ {
		for _, s := range rep.Steps {
			if s.Step != step {
				continue
			}
			for _, c := range s.Checks {
				if c.Name == planlint.LintWater && (c.Verdict == VerdictFail || c.Verdict == VerdictWarn) {
					watered = true
					t.Logf("step %d: %s", step, c.Reason)
				}
			}
		}
	}
	if !watered {
		t.Errorf("no water finding on leg 1 rows 7-9 (steps 71-73)")
	}
	fmt.Printf("scout: %d steps, %d fail, %d warn in %.1f s\n", len(rep.Steps), rep.Counts.Fail, rep.Counts.Warn, rep.Runtime)
}
