package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/planlint"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// The facts' Deadmines object table: the spawns plan-check --forever reads.
// Positions are the plan's own near points (the same spawns).
const deadminesForeverSpawns = `guid	entry	x	y	z	movetype	kind
30030533	13965	-191.414	-457.446	54.4391	0	object
30026183	17153	-262.714	-482.361	49.4353	0	object
30026185	16400	-290.294	-536.960	49.4353	0	object
30026184	17154	-242.965	-578.561	51.1366	0	object
30026182	16399	-168.514	-579.861	19.3159	0	object
30026203	17155	-106.409	-617.284	13.8495	0	object
30026205	16398	-107.562	-659.674	7.21211	0	object
30030534	16397	-100.502	-668.771	7.41049	0	object
`

func foreverWorld(t *testing.T, data string, mapID uint32) *worldspawn.World {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spawns.tsv")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	w, _, err := worldspawn.LoadForeverSpawns(path, mapID)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func buildPlan(t *testing.T, name string) (*dungeon.Plan, planexport.Result) {
	t.Helper()
	p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(p, planexport.Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return p, res
}

// Deadmines on the forever spawns: every object row (the two clicked Heavy
// Doors, Sneed's and Gilnid's waits resolved to 16400/16399, the gunpowder,
// the cannon, the Iron Clad Door) names a spawned object.
func TestPlanCheckForeverDeadminesObjects(t *testing.T) {
	p, res := buildPlan(t, "deadmines")
	w := foreverWorld(t, deadminesForeverSpawns, 36)
	if fs := planlint.CheckObjects(res.Rows, res.Origins, p, w, foreverServer); len(fs) != 0 {
		t.Fatalf("findings %v, want none", fs)
	}
}

// Without Sneed's and Gilnid's doors the waits fail on the forever entries
// (16400/16399), not on the base 17153: the checker reads the alt_entries.
func TestPlanCheckForeverDeadminesMissingDoors(t *testing.T) {
	p, res := buildPlan(t, "deadmines")
	var kept []string
	var rows []string
	for _, line := range strings.Split(deadminesForeverSpawns, "\n") {
		if strings.HasPrefix(line, "30026185\t") || strings.HasPrefix(line, "30026182\t") {
			continue
		}
		kept = append(kept, line)
		_ = rows
	}
	w := foreverWorld(t, strings.Join(kept, "\n"), 36)
	fs := planlint.CheckObjects(res.Rows, res.Origins, p, w, foreverServer)
	if len(fs) != 2 {
		t.Fatalf("findings %v, want the two missing doors", fs)
	}
	for _, f := range fs {
		if f.Lint != planlint.LintObjects || f.Severity != planlint.SeverityError {
			t.Fatalf("finding %+v, want an objects error", f)
		}
		if !strings.Contains(f.Detail, "16400") && !strings.Contains(f.Detail, "16399") {
			t.Fatalf("finding %+v names no forever entry", f)
		}
	}
}

// Deadmines against a creature-only spawn file through lintRows, the
// command's own lint path (planLints -> lintResult -> lintRows): every
// object-backed step fails with a SeverityError naming the forever entry,
// so plan-check --forever refuses the plan (lintErrors > 0 is exit 11);
// Wailing Caverns (no object step) gets no objects finding from the same
// file. A full planCheckCmd run is impossible here: CheckPlanWith's server
// walk replays the 1.12 query (ServerPath), which a Forever tile set
// refuses, so the command exits 2 before any lint runs (pre-existing, not
// this round). The mesh is a flat Forever fixture; the stand-in creature
// row sits far from both plans' walks, so only the object list is judged.
func TestPlanCheckForeverEmptyObjectsFailsDeadminesPassesWailingCaverns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spawns.tsv")
	// Deadmines's own creatures would sit here; one distant spawn stands in:
	// creature presence is not what this test judges, only the object list
	// being empty.
	if err := os.WriteFile(path, []byte("guid\tentry\tx\ty\tz\tmovetype\n27368\t3653\t-3000\t-3000\t0\t1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// One flat Forever tile per plan map, named for a point the plan walks
	// (DM Sneed's door, WC the entrance), so the placement lints answer
	// on-mesh and only the object list is judged.
	mesh := func(mapID uint32, x, y float32) *navmesh.Planner {
		t.Helper()
		p := navmesh.NewForeverPlanner(navfixture.WriteForeverDirAt(t, mapID,
			[]navfixture.Poly{navfixture.Square(x-300, y-300, x+300, y+300, 0)}, nil, x, y), nil)
		t.Cleanup(p.Close)
		return p
	}
	check := func(plan string, mapID uint32, atX, atY float32) []planlint.Finding {
		t.Helper()
		p, res := buildPlan(t, plan)
		fs, _, err := lintRows(res.Rows, res.Origins, mesh(mapID, atX, atY), "", path, p, true)
		if err != nil {
			t.Fatal(err)
		}
		var objects []planlint.Finding
		for _, f := range fs {
			if f.Lint == planlint.LintObjects {
				objects = append(objects, f)
			}
		}
		return objects
	}
	dmFS := check("deadmines", 36, -262, -482)
	if len(dmFS) == 0 {
		t.Fatalf("deadmines: no objects findings, want an error per object row")
	}
	found := false
	for _, f := range dmFS {
		if f.Severity != planlint.SeverityError {
			t.Fatalf("deadmines finding %+v, want an objects error", f)
		}
		if strings.Contains(f.Detail, "16400") || strings.Contains(f.Detail, "16399") {
			found = true
		}
	}
	if !found {
		t.Fatalf("deadmines findings name no forever door entry: %v", dmFS)
	}
	if fs := check("wailing_caverns", 43, -3, 229); len(fs) != 0 {
		t.Fatalf("wailing_caverns: findings %v, want none", fs)
	}
}

// --forever reads --spawns as a TSV file: a folder or a missing file skips
// the spawn-based lints with a reason instead of failing.
func TestWorldForLintsForeverNeedsAFile(t *testing.T) {
	if _, note := worldForLintsForever(t.TempDir(), 36); !strings.Contains(note, "reads --spawns as a TSV file") {
		t.Fatalf("note %q", note)
	}
	if _, note := worldForLintsForever(filepath.Join(t.TempDir(), "missing.tsv"), 36); !strings.Contains(note, "lints skipped") {
		t.Fatalf("note %q", note)
	}
}
