package world

// #216: plan_check runs the CLI's own plan check (dungeon.CheckPlanWith on a Forever planner) over the serve's
// plans folder and --navmesh, and types the answer exactly as the old daemon did: the check, or the reason it could
// not run. Mutation: a swapped planner format, a plan path that escapes the plans folder, or a check that ignores
// its leg options fails here.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

// planCheckPlan is a two-pack plan on map 0 around (1000, 1000): the first pack on the mesh, the second one off it
// when the test asks for a failing plan.
const planCheckPlanOK = `{
  "version": 1,
  "name": "check_fixture",
  "status": "draft (test layout)",
  "map": 0,
  "entrance": {"x": 1000, "y": 1000, "z": 50},
  "level_band": [1, 5],
  "notes": ["A two-pack test layout on the navfixture square: the points sit on the mesh."],
  "legs": [
    {
      "name": "first",
      "to": {"x": 1000, "y": 1000, "z": 50},
      "pull_groups": [
        {"entries": [6, 6], "positions": [{"x": 1000, "y": 1000, "z": 50}], "radius": 12}
      ]
    }
  ]
}`

const planCheckPlanOffMesh = `{
  "version": 1,
  "name": "check_fixture",
  "status": "draft (test layout)",
  "map": 0,
  "entrance": {"x": 1000, "y": 1000, "z": 50},
  "level_band": [1, 5],
  "notes": ["The second pack stands 900 yd off the navfixture square, so the check must fail it."],
  "legs": [
    {
      "name": "first",
      "to": {"x": 1000, "y": 1000, "z": 50},
      "pull_groups": [
        {"entries": [6, 6], "positions": [{"x": 1000, "y": 1000, "z": 50}], "radius": 12},
        {"entries": [6, 6], "positions": [{"x": 1900, "y": 1900, "z": 50}], "radius": 12}
      ]
    }
  ]
}`

// planCheckFixture writes a plans folder holding the given plan and returns it.
func planCheckFixture(t *testing.T, plan string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "check_fixture.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// planCheckMesh is the v16 navmesh a plan check walks: a 40x40 square at z 50 around (1000, 1000).
func planCheckMesh(t *testing.T) string {
	t.Helper()
	return navfixture.WriteForeverDirAt(t, 0, []navfixture.Poly{navfixture.Square(980, 980, 1020, 1020, 50)}, nil, 1000, 1000)
}

func TestServePlanCheckOK(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) {
		cfg.NavmeshDir = planCheckMesh(t)
		cfg.PlansDir = planCheckFixture(t, planCheckPlanOK)
	})
	waitServeState(t, a, "online", steps)

	var out ServePlanCheck
	json.Unmarshal(a.mustCall(t, "plan_check", map[string]any{"name": "check_fixture"}), &out)
	if !out.Checked || out.Skipped != "" {
		t.Fatalf("plan_check was not checked: %+v", out)
	}
	if out.Plan != "check_fixture" || out.Map != 0 || !out.OK() || len(out.Issues) != 0 {
		t.Fatalf("plan_check %+v", out)
	}
	if out.Issues == nil || out.Warnings == nil || out.Exempt == nil || out.RunLeader == nil {
		t.Fatalf("a list is null: %+v", out)
	}
	// The leg options ride through: the plan has one leg, so leg 0 and its name both check, and a leg past the end
	// is refused by name.
	json.Unmarshal(a.mustCall(t, "plan_check", map[string]any{"name": "check_fixture", "from_leg": 0}), &out)
	if !out.Checked {
		t.Fatalf("from_leg 0: %+v", out)
	}
	json.Unmarshal(a.mustCall(t, "plan_check", map[string]any{"name": "check_fixture", "from_leg": "first"}), &out)
	if !out.Checked {
		t.Fatalf("from_leg by name: %+v", out)
	}
	a.mustFail(t, "plan_check", map[string]any{"name": "check_fixture", "from_leg": 1}, -32602, "the plan has 1 legs")
	a.mustFail(t, "plan_check", map[string]any{"name": "check_fixture", "from_pull_group": 0}, -32602, "from_pull_group needs from_leg")
	a.mustFail(t, "plan_check", map[string]any{"name": "../etc/passwd"}, -32602, "name must be a plan file name")
	a.stop(t)
	f.wait(t)
}

func TestServePlanCheckFindsTheOffMeshPoint(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) {
		cfg.NavmeshDir = planCheckMesh(t)
		cfg.PlansDir = planCheckFixture(t, planCheckPlanOffMesh)
	})
	waitServeState(t, a, "online", steps)

	var out ServePlanCheck
	json.Unmarshal(a.mustCall(t, "plan_check", map[string]any{"name": "check_fixture"}), &out)
	if !out.Checked {
		t.Fatalf("plan_check was not checked: %+v", out)
	}
	if out.OK() || len(out.Issues) == 0 {
		t.Fatalf("the off-mesh pack was not failed: %+v", out)
	}
	if out.Issues[0].Reason == "" {
		t.Fatalf("the issue carries no reason: %+v", out.Issues[0])
	}
	a.stop(t)
	f.wait(t)
}

// TestServePlanCheckWithoutNavmesh: the plan is left unchecked with the reason, as the old daemon did.
func TestServePlanCheckWithoutNavmesh(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) {
		cfg.PlansDir = planCheckFixture(t, planCheckPlanOK)
	})
	waitServeState(t, a, "online", steps)

	var out ServePlanCheck
	json.Unmarshal(a.mustCall(t, "plan_check", map[string]any{"name": "check_fixture"}), &out)
	if out.Checked || out.Skipped == "" {
		t.Fatalf("plan_check without a navmesh: %+v", out)
	}
	if out.Skipped != "no navmesh: the serve was started without --navmesh" {
		t.Fatalf("skipped %q", out.Skipped)
	}
	a.mustFail(t, "plan_check", map[string]any{"name": "no_such_plan"}, -32602, "dungeon plan")
	a.stop(t)
	f.wait(t)
}
