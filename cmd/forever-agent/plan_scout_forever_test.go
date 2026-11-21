package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
	"github.com/pjerra/forever-agent/internal/planscout"
)

// foreverExport is the worldspawn fixture export (map 700, invented rows): spawn 101 has no level in the core (60
// assumed offline) and stands at (3, 0).
func foreverExport() string {
	return filepath.Join("..", "..", "internal", "worldspawn", "testdata", "map700-forever")
}

// foreverFlat is a flat v16 tile set for map 700 around x 0..40.
func foreverFlat(t *testing.T) string {
	t.Helper()
	return navfixture.WriteForeverDirAt(t, 700,
		[]navfixture.Poly{navfixture.Square(-300, -300, 300, 300, 0)}, nil, 20, 0)
}

// foreverScoutPlanFile writes a plan on map 700: one leg to (40, 0, 0), and with stand the leg pulls a stand-pull group
// from an approach point at (10, 0, 0).
func foreverScoutPlanFile(t *testing.T, dir, name string, stand bool) string {
	t.Helper()
	group := dungeon.PullGroup{
		Entries:   []uint32{9999}, // no export spawn: nothing is owned by this pull
		Positions: []dungeon.Point{{X: 20}},
		Radius:    5,
	}
	if stand {
		group.Approach = []dungeon.Point{{X: 10}}
		group.StandPull = true
	}
	leg := dungeon.Leg{Name: "one", To: dungeon.Point{X: 40}, PullGroups: []dungeon.PullGroup{group}}
	b, err := dungeon.Marshal(&dungeon.Plan{Version: 1, Name: name, Map: 700, Legs: []dungeon.Leg{leg}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeForeverServe answers the live pass from memory: status follows the last `.go xyz`, units are fixed.
type fakeForeverServe struct {
	mu     sync.Mutex
	status serveclient.Status
	gm     []string
	units  []serveclient.Unit
}

func newFakeForeverServe(units ...serveclient.Unit) *fakeForeverServe {
	return &fakeForeverServe{
		status: serveclient.Status{State: "online", Map: 700},
		units:  units,
	}
}

func (f *fakeForeverServe) Status(context.Context) (serveclient.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *fakeForeverServe) GM(_ context.Context, text string) (serveclient.Command, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gm = append(f.gm, text)
	if strings.HasPrefix(text, ".go xyz ") {
		fs := strings.Fields(text)
		if len(fs) == 6 {
			x, _ := strconv.ParseFloat(fs[2], 32)
			y, _ := strconv.ParseFloat(fs[3], 32)
			z, _ := strconv.ParseFloat(fs[4], 32)
			f.status.X, f.status.Y, f.status.Z = float32(x), float32(y), float32(z)
		}
	}
	return serveclient.Command{Sent: text, Complete: true, Lines: []string{"ok"}}, nil
}

func (f *fakeForeverServe) Units(context.Context, serveclient.UnitsParams) ([]serveclient.Unit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]serveclient.Unit(nil), f.units...), nil
}

func (f *fakeForeverServe) PathTo(_ context.Context, x, y, z float32) (serveclient.Path, error) {
	return serveclient.Path{Map: 700, Status: "complete", Length: 10, Points: [][3]float32{{0, 0, 0}, {x, y, z}}}, nil
}

func (f *fakeForeverServe) WalkPath(ctx context.Context, x, y, z float32) (serveclient.Path, error) {
	return f.PathTo(ctx, x, y, z)
}

// useFakeForeverServe injects the serve client for one test.
func useFakeForeverServe(t *testing.T, f *fakeForeverServe) {
	t.Helper()
	old := scoutForeverOpen
	scoutForeverOpen = func(string) (scoutForeverClient, error) { return f, nil }
	t.Cleanup(func() { scoutForeverOpen = old })
}

// TestScoutForeverAllowlist is the live pass's line guard: the two phase lines and a `.go xyz` on the plan's map pass,
// anything that changes the world or moves elsewhere is refused.
func TestScoutForeverAllowlist(t *testing.T) {
	for _, line := range []string{".gm on", ".instance unbind all", ".go xyz 1 2 3 700"} {
		if !scoutAllowedLine(line, 700, 0) {
			t.Errorf("%q refused, want allowed", line)
		}
	}
	for _, line := range []string{
		".npc delete", ".npc add 4100", ".go xyz 1 2 3 701", ".instance unbind 701", ".gm off", ".die",
	} {
		if scoutAllowedLine(line, 700, 0) {
			t.Errorf("%q allowed, want refused", line)
		}
	}
}

// TestScoutForeverLevelsFromLiveUnits is the round's failing case: spawn 101 has no level offline (LoadForeverDB assumes
// 60), and the live `units` read says 30. The live aggro reach then comes from level 30 (5 yd at party level 60), not
// from the assumed 60 (20 yd). A pass that ignores units keeps 20.
func TestScoutForeverLevelsFromLiveUnits(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_live", true)
	fake := newFakeForeverServe(serveclient.Unit{
		GUID: "0x00000000000000000000000000000065", Type: "creature", Entry: 4100, Name: "ForeverRat",
		X: 10, Y: 0, Z: 0, Level: 30, Health: 100, MaxHealth: 100,
	})
	useFakeForeverServe(t, fake)
	slot := t.TempDir()

	code, _, errOut := runCLI("plan-scout", "scout_live", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", outDir, "--live", "--slot", slot)
	if code == exitLivePhaseRefused {
		t.Fatalf("live pass refused: %q", errOut)
	}
	b, err := os.ReadFile(filepath.Join(outDir, "scout-scout_live.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep planscout.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	reach, found := -1.0, false
	for _, st := range rep.Steps {
		for _, c := range st.Checks {
			if c.Name == planscout.CheckLiveAggro {
				reach, found = c.Data["reach"], true
			}
		}
	}
	if !found {
		t.Fatalf("no live_aggro check in the report: %+v", rep.Steps)
	}
	if reach != 5 {
		t.Fatalf("live reach %.1f, want 5 (level 30 from units); 20 means the pass kept the assumed 60", reach)
	}
	fake.mu.Lock()
	gm := strings.Join(fake.gm, "; ")
	fake.mu.Unlock()
	for _, want := range []string{".gm on", ".instance unbind all", ".go xyz"} {
		if !strings.Contains(gm, want) {
			t.Errorf("the live pass never sent %q: %s", want, gm)
		}
	}
}

// TestScoutForeverDraftPassesPlanCheck: a draft scouted from a Forever export and v16 tiles passes plan-check --forever.
func TestScoutForeverDraftPassesPlanCheck(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_f", false)
	code, outText, errOut := runCLI("plan-scout", "scout_f", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"))
	if code != exitOK {
		t.Fatalf("plan-scout --forever: code %d out %q err %q", code, outText, errOut)
	}
	spawnFile := filepath.Join(dir, "spawns.tsv")
	if err := os.WriteFile(spawnFile, []byte("guid\tentry\tx\ty\tz\tmovetype\n900\t4100\t2000\t2000\t0\t0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, outText, errOut := runCLI("plan-check", "--dir", dir, "--mmaps", mmaps,
		"--spawns", spawnFile, "--forever", "scout_f"); code != exitOK {
		t.Fatalf("plan-check --forever: code %d out %q err %q", code, outText, errOut)
	}
}

// foreverParents is the parent-map flag's value: the parser refuses text it cannot read.
func TestForeverParents(t *testing.T) {
	var p foreverParents
	for _, s := range []string{"389=0", "36=0"} {
		if err := p.Set(s); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
	}
	if p[389] != 0 || p[36] != 0 || len(p) != 2 {
		t.Fatalf("parents %v", p)
	}
	for _, s := range []string{"389", "389=x", "=0"} {
		if err := p.Set(s); err == nil {
			t.Errorf("%q accepted, want refused", s)
		}
	}
}
