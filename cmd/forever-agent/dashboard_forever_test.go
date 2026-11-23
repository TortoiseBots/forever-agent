package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dashboard"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
	"github.com/pjerra/forever-agent/internal/routeedit"
)

// foreverDashPlan is a Ragefire Chasm plan (map 389) on the square mesh the fixture writes: every point is inside the square, so
// the plan checks clean and only an edit that leaves the square makes a new issue.
const foreverDashPlan = `{"version":1,"name":"rfc","map":389,"entrance":{"x":12,"y":12,"z":0},
 "legs":[{"name":"corridor","to":{"x":40,"y":12,"z":0},"via":[{"x":20,"y":12,"z":0},{"x":30,"y":12,"z":0}],
  "pull_groups":[{"entries":[7001],"positions":[{"x":48,"y":30,"z":0}]}],"boss":{"entry":6001,"position":{"x":48,"y":46,"z":0}}}]}`

// foreverDashMesh writes the Ragefire Chasm mmaps folder: its header and one tile of its own (a square covering +-100 yd at height 0,
// named for the grid of (32, 13)), and its parent map's (Eastern Kingdoms, 0) header and tile, an island at x, y 600..900 the instance
// has no tile of its own for.
func foreverDashMesh(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	merge := func(from string) {
		ents, err := os.ReadDir(from)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			b, err := os.ReadFile(filepath.Join(from, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	merge(navfixture.WriteForeverDirAt(t, 389, []navfixture.Poly{navfixture.Square(-100, -100, 100, 100, 0)}, nil, 32, 13))
	merge(navfixture.WriteForeverTiles(t, 0, []navfixture.ForeverTile{{TX: 1, TY: 1, Polys: []navfixture.Poly{navfixture.Square(600, 600, 900, 900, 0)}, AtX: 700, AtY: 700}}))
	return dir
}

// foreverDashRig is the dashboard's plan view and editor over one Forever mesh folder, served over HTTP as the page reads them.
type foreverDashRig struct {
	t       *testing.T
	src     string
	edits   string
	tokens  string
	base    string // sha256 of the plan file
	srv     *httptest.Server
	planner *navmesh.Planner
}

func newForeverDashRig(t *testing.T) *foreverDashRig {
	t.Helper()
	root := t.TempDir()
	r := &foreverDashRig{t: t, src: filepath.Join(root, "plans"), edits: filepath.Join(root, "edits"), tokens: filepath.Join(root, "tokens")}
	if err := os.MkdirAll(r.src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.src, "rfc.json"), []byte(foreverDashPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.tokens, []byte("owner "+strings.Repeat("v", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mesh := foreverDashMesh(t)
	planner, err := openDashNav(mesh, navmesh.Forever, map[uint32]uint32{389: 0})
	if err != nil {
		t.Fatalf("openDashNav: %v", err)
	}
	t.Cleanup(planner.Close)
	r.planner = planner
	tk, err := routeedit.LoadTokens(r.tokens)
	if err != nil {
		t.Fatal(err)
	}
	store, err := routeedit.NewStore(r.edits)
	if err != nil {
		t.Fatal(err)
	}
	p := dashboard.NewPoller(dashboard.Options{ConfigDir: t.TempDir(), PlansDirs: []string{r.src}, Agents: "auto"})
	ed, err := dashboard.NewEditor(p, dashboard.EditConfig{Source: r.src, Store: store, Tokens: tk, Nav: dashNav(planner, navmesh.Forever), Mmaps: mesh})
	if err != nil {
		t.Fatal(err)
	}
	r.srv = httptest.NewServer(dashboard.HandlerWithEditor(p, "", nil, nil, ed))
	t.Cleanup(r.srv.Close)
	sum := sha256.Sum256([]byte(foreverDashPlan))
	r.base = hex.EncodeToString(sum[:])
	return r
}

func (r *foreverDashRig) do(method, path string, body []byte, hdr map[string]string) (int, string) {
	r.t.Helper()
	req, err := http.NewRequest(method, r.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// post edits the plan: one move of leg 0's via point index to (x, y).
func (r *foreverDashRig) post(index int, x, y float64) (int, string) {
	r.t.Helper()
	b, err := json.Marshal(map[string]any{"base_sha256": r.base, "note": "test",
		"ops": []any{map[string]any{"leg": 0, "kind": "move", "index": index, "x": x, "y": y}}})
	if err != nil {
		r.t.Fatal(err)
	}
	return r.do(http.MethodPost, "/api/edit/rfc", b, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + strings.Repeat("v", 40),
	})
}

// TestDashboardForeverEditAcceptsOnMesh: a Ragefire leg edited on the Forever mesh is checked and saves a version, the plan view reports the
// mesh's generation, and the planner plans across the instance tile that exists only under its parent map. A Turtle planner here (the format
// flag ignored) would find no Forever tile, and a planner without the parent pairs would fail the parent-only walk, so both mutations fail
// this test.
func TestDashboardForeverEditAcceptsOnMesh(t *testing.T) {
	r := newForeverDashRig(t)
	code, body := r.post(1, 32, 13)
	if code != http.StatusOK {
		t.Fatalf("an on-mesh move: %d %s", code, body)
	}
	var rep dashboard.EditReply
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Status != "checked" || rep.Version != 1 || !rep.Check.OK {
		t.Fatalf("%+v", rep)
	}
	if _, err := os.Stat(filepath.Join(r.edits, "rfc", "v0001.json")); err != nil {
		t.Errorf("no saved version: %v", err)
	}
	code, body = r.do(http.MethodGet, "/api/plan/rfc/route", nil, nil)
	if code != http.StatusOK || !strings.Contains(body, `"format":"forever"`) {
		t.Errorf("the plan view route: %d %.300s", code, body)
	}
	// The island is the parent map's tile alone: the instance's own folder has nothing at 600..900.
	path, err := r.planner.Plan(389, navmesh.Vec3{700, 700, 0}, navmesh.Vec3{760, 700, 0})
	if err != nil || path.Status != navmesh.Complete {
		t.Fatalf("the walk on the parent map's island: %v %+v", err, path)
	}
}

// TestDashboardForeverEditRejectsOffMesh: a point 50 yd outside the square has no floor, and the editor refuses the op with its own
// off-mesh answer; no version is written.
func TestDashboardForeverEditRejectsOffMesh(t *testing.T) {
	r := newForeverDashRig(t)
	code, body := r.post(1, 150, 13)
	if code != http.StatusBadRequest || !strings.Contains(body, "off the mesh") {
		t.Fatalf("a point 50 yd outside the square: %d %.300s", code, body)
	}
	if ents, err := os.ReadDir(filepath.Join(r.edits, "rfc")); err == nil && len(ents) > 0 {
		t.Errorf("a refused edit wrote %d files", len(ents))
	}
}

// doneCtx is a context that is already over: a command that gets past its flag checks stops at the listen instead of serving the
// test's page forever, so a refused-start test can never hang.
func doneCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// TestDashboardFormatMismatchFailsAtStart: the format is never guessed. Each generation's folder is checked for a tile its own parser
// accepts, so the wrong --mmaps-format stops the dashboard at start instead of leaving every plan point off the mesh.
func TestDashboardFormatMismatchFailsAtStart(t *testing.T) {
	turtleDir := navfixture.WriteDir(t, 209, navfixture.LCorridor(0))
	foreverDir := foreverDashMesh(t)
	if p, err := openDashNav(turtleDir, navmesh.MaNGOS, nil); err != nil {
		t.Errorf("a Turtle folder read as Turtle: %v", err)
	} else {
		p.Close()
	}
	if p, err := openDashNav(foreverDir, navmesh.Forever, nil); err != nil {
		t.Errorf("a Forever folder read as Forever: %v", err)
	} else {
		p.Close()
	}
	if _, err := openDashNav(turtleDir, navmesh.Forever, nil); err == nil || !strings.Contains(err.Error(), "has no Forever tiles") {
		t.Errorf("a Turtle folder read as Forever: %v", err)
	}
	if _, err := openDashNav(foreverDir, navmesh.MaNGOS, nil); err == nil || !strings.Contains(err.Error(), "has no Turtle tiles") {
		t.Errorf("a Forever folder read as Turtle: %v", err)
	}
	for _, args := range [][]string{
		{"--mmaps-format", "forever", "--mmaps-dir", turtleDir},
		{"--mmaps-format", "turtle", "--mmaps-dir", foreverDir},
	} {
		var out, errb bytes.Buffer
		full := append([]string{"--config-dir", t.TempDir(), "--listen", "127.0.0.1:0"}, args...)
		if code := dashboardCmd(doneCtx(), full, &out, &errb); code != exitUsage {
			t.Errorf("%v: exit %d: %s", args, code, errb.String())
		}
		if !strings.Contains(errb.String(), "has no ") || !strings.Contains(errb.String(), "tiles") {
			t.Errorf("%v: %s", args, errb.String())
		}
	}
	var out, errb bytes.Buffer
	if code := dashboardCmd(doneCtx(), []string{"--mmaps-format", "forever-core", "--config-dir", t.TempDir(), "--listen", "127.0.0.1:0"}, &out, &errb); code != exitUsage || !strings.Contains(errb.String(), "--mmaps-format") {
		t.Errorf("a made-up format: exit %d: %s", code, errb.String())
	}
}

// TestDashboardParentNeedsForever: the instance parent rule is a Forever server's, so --parent is refused with the Turtle format.
func TestDashboardParentNeedsForever(t *testing.T) {
	var out, errb bytes.Buffer
	args := []string{"--parent", "389=0", "--mmaps-dir", navfixture.WriteDir(t, 209, navfixture.LCorridor(0)), "--config-dir", t.TempDir(), "--listen", "127.0.0.1:0"}
	if code := dashboardCmd(doneCtx(), args, &out, &errb); code != exitUsage || !strings.Contains(errb.String(), "--parent needs --mmaps-format forever") {
		t.Errorf("exit %d: %s", code, errb.String())
	}
	if got, err := parseParentPairs("389=0"); err != nil || got[389] != 0 {
		t.Errorf("parseParentPairs: %v %v", got, err)
	}
}
