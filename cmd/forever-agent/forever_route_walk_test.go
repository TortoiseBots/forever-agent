package main

// The route-walk command's own tests: the leg selection grammar, the table, the JSON file and the validation that
// runs before a login. The walk itself is proven against the fake server in internal/forever/world.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// routeWalkTestPlan writes a three-leg plan for the selection and validation tests.
func routeWalkTestPlan(t *testing.T, entranceTele string) string {
	t.Helper()
	plan := `{"version":1,"name":"rw-test","kind":"dungeon","map":389,` +
		`"entrance":{"x":0.8,"y":-8.2,"z":-15.5},"entrance_tele":"` + entranceTele + `",` +
		`"legs":[` +
		`{"name":"tunnel","to":{"x":-15,"y":-50,"z":-21},"pull_groups":[{"entries":[1],"positions":[{"x":-16,"y":-50,"z":-22}]}]},` +
		`{"name":"cave","to":{"x":-80,"y":-34,"z":-20},"pull_groups":[{"entries":[1],"positions":[{"x":-80,"y":-34,"z":-20}]}]},` +
		`{"name":"boss","to":{"x":-140,"y":14,"z":-21},"boss":{"entry":2,"position":{"x":-140,"y":14,"z":-21}}}` +
		`]}`
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadRouteWalkPlan(t *testing.T, path string) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Load(path)
	if err != nil {
		t.Fatalf("the test plan is refused: %v", err)
	}
	return p
}

// The leg selection takes numbers, ranges and names, keeps plan order and refuses anything outside the plan.
func TestRouteWalkLegSelection(t *testing.T) {
	p := loadRouteWalkPlan(t, routeWalkTestPlan(t, "rfc"))
	cases := []struct {
		arg  string
		want []int
	}{
		{"", nil},
		{"1", []int{0}},
		{"2-3", []int{1, 2}},
		{"1,3", []int{0, 2}},
		{"3,1", []int{0, 2}},
		{"boss", []int{2}},
		{"tunnel,cave", []int{0, 1}},
		{"1-2,cave", []int{0, 1}},
	}
	for _, c := range cases {
		got, err := routeWalkLegs(p, c.arg)
		if err != nil {
			t.Errorf("--legs %q: %v", c.arg, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("--legs %q: %v, want %v", c.arg, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("--legs %q: %v, want %v", c.arg, got, c.want)
				break
			}
		}
	}
	for _, bad := range []string{"0", "4", "3-2", "2-4", "nope", "1,,2", "-"} {
		if _, err := routeWalkLegs(p, bad); err == nil {
			t.Errorf("--legs %q was accepted", bad)
		}
	}
}

// The table names every leg with its verdict and thresholds, and the JSON file beside it carries the waypoints.
func TestRouteWalkTableAndJSON(t *testing.T) {
	rw := &world.RouteWalkReport{
		Plan:  "rw-test",
		Map:   389,
		GM:    true,
		Entry: "entered by .tele rfc",
		Thresholds: world.RouteThresholds{
			MaxDeviationYd: 2, StuckAfterS: 6, StuckStepYd: 0.5, FallDropYd: 2, SampleS: 3, LiquidAboveYd: 2, LiquidBelowYd: 0.5,
		},
		Verdict: world.RouteStuck,
		Legs: []world.RouteLeg{
			{
				Index: 0, Name: "tunnel", Status: world.RoutePass, PlannedYd: 41.5, WalkedYd: 41.4, Hops: 1, MaxDev: 0.02,
				Waypoints: []world.RouteWaypoint{{Point: [3]float32{1, 2, 3}, Server: [3]float32{1, 2, 3}, At: time.Unix(1, 0), AtS: 1.5}},
			},
			{
				Index: 1, Name: "cave", Status: world.RouteStuck, Reason: "the server's position did not move for 6s",
				Issues: []string{"STUCK at 1.00 2.00 3.00: the server's position did not move for 6s"}, PlannedYd: 80, WalkedYd: 12, Hops: 1,
			},
		},
	}
	var out bytes.Buffer
	printRouteWalk(&out, rw)
	text := out.String()
	for _, want := range []string{"plan rw-test", "thresholds: deviation 2.00 yd", "tunnel", "cave", "STUCK", "verdict STUCK", "max snap", "first issue"} {
		if !strings.Contains(text, want) {
			t.Errorf("the table does not hold %q:\n%s", want, text)
		}
	}

	path := filepath.Join(t.TempDir(), "route-walk.json")
	if err := writeRouteWalkJSON(path, rw); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back world.RouteWalkReport
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("the JSON does not decode: %v", err)
	}
	if len(back.Legs) != 2 || back.Legs[1].Status != world.RouteStuck || back.Legs[0].Waypoints[0].DeviationYd != 0 {
		t.Fatalf("the JSON lost the report: %+v", back)
	}
	if back.Thresholds != rw.Thresholds {
		t.Fatalf("thresholds in JSON: %+v", back.Thresholds)
	}
}

// The command refuses what it cannot run before a login, and says why.
func TestRouteWalkCmdValidation(t *testing.T) {
	plan := routeWalkTestPlan(t, "rfc")
	noTele := routeWalkTestPlan(t, "")
	dir := t.TempDir()
	base := []string{
		"--rest", "127.0.0.1:1", "--rpc", "127.0.0.1:2", "--ca", filepath.Join(dir, "ca.pem"),
		"--account", "user@example.test", "--character", "Walken",
	}
	run := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		code := foreverRouteWalkCmd(t.Context(), args, &out, &errOut)
		return code, errOut.String()
	}
	cases := []struct {
		name string
		args []string
	}{
		{"no flags", nil},
		{"no plan", append(append([]string{}, base...), "--navmesh", dir)},
		{"no navmesh", append(append([]string{}, base...), "--plan", plan)},
		{"bad enter", append(append([]string{}, base...), "--plan", plan, "--navmesh", dir, "--enter", "walk")},
		{"bad legs", append(append([]string{}, base...), "--plan", plan, "--navmesh", dir, "--legs", "9")},
		{"negative threshold", append(append([]string{}, base...), "--plan", plan, "--navmesh", dir, "--fall-drop", "-1")},
		{"tele without the plan's row", append(append([]string{}, base...), "--plan", noTele, "--navmesh", dir)},
	}
	for _, c := range cases {
		code, errText := run(c.args...)
		if code != exitUsage {
			t.Errorf("%s: exit %d, want %d (%s)", c.name, code, exitUsage, errText)
		}
		if errText == "" {
			t.Errorf("%s: no message on stderr", c.name)
		}
	}
}
