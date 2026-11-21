package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/planscout"
	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/slotdir"
	"github.com/pjerra/forever-agent/internal/world"
	"github.com/pjerra/forever-agent/internal/worldspawn"
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

// fakePath is one PathTo call: the session stood at From when the server planned to To.
type fakePath struct {
	From, To dungeon.Point
}

// fakeForeverServe answers the live pass from memory: the status follows the last `.go xyz`, units are fixed, and the
// knobs below make the pass fail in the ways its checks must survive.
type fakeForeverServe struct {
	mu     sync.Mutex
	status serveclient.Status
	gm     []string
	units  []serveclient.Unit
	paths  []fakePath
	walks  []dungeon.Point
	// pathStatus answers every hop: "complete" when nil (a partial answer makes the pass walk the hop).
	pathStatus func(from, to dungeon.Point) string
	// walkErr is WalkPath's error; walkHang makes it wait for the context instead of answering.
	walkErr  error
	walkHang bool
	// gmRefusal answers a GM line that starts with the key with this line instead of succeeding.
	gmRefusal map[string]string
	// blockStatus and blockUnits wait for the context in Status and Units (the --max-minutes ceiling).
	blockStatus, blockUnits bool
	// ray answers a walk_ray read: nil answers Known and In for every point. noWalkRay makes the serve answer -32601, the
	// way a serve built before the walk_ray method does.
	ray       func(from [3]float32, x, y, z float32) serveclient.WalkRay
	noWalkRay bool
	rayCalls  int
	// rayErr makes every walk_ray read fail with this error (not a missing method).
	rayErr error
}

func (f *fakeForeverServe) WalkRay(_ context.Context, from *[3]float32, x, y, z float32) (serveclient.WalkRay, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rayErr != nil {
		f.rayCalls++
		return serveclient.WalkRay{}, f.rayErr
	}
	if f.noWalkRay {
		return serveclient.WalkRay{}, &serveclient.Error{Code: rpc.CodeMethodNotFound, Message: "Method not found: walk_ray", Method: "walk_ray"}
	}
	f.rayCalls++
	var at [3]float32
	if from != nil {
		at = *from
	}
	if f.ray != nil {
		return f.ray(at, x, y, z), nil
	}
	return serveclient.WalkRay{Known: true, In: true, Source: "navmesh_raycast"}, nil
}

func newFakeForeverServe(units ...serveclient.Unit) *fakeForeverServe {
	return &fakeForeverServe{
		status: serveclient.Status{State: "online", Map: 700, Character: "Tester"},
		units:  units,
	}
}

func (f *fakeForeverServe) Status(ctx context.Context) (serveclient.Status, error) {
	f.mu.Lock()
	block := f.blockStatus
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return serveclient.Status{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *fakeForeverServe) GM(_ context.Context, text string) (serveclient.Command, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gm = append(f.gm, text)
	for prefix, line := range f.gmRefusal {
		if strings.HasPrefix(text, prefix) {
			return serveclient.Command{Sent: text, Complete: true, Lines: []string{line}}, nil
		}
	}
	if strings.HasPrefix(text, ".go xyz ") {
		fs := strings.Fields(text)
		if len(fs) == 6 {
			x, _ := strconv.ParseFloat(fs[2], 32)
			y, _ := strconv.ParseFloat(fs[3], 32)
			z, _ := strconv.ParseFloat(fs[4], 32)
			m, _ := strconv.ParseUint(fs[5], 10, 32)
			f.status.X, f.status.Y, f.status.Z = float32(x), float32(y), float32(z)
			f.status.Map = uint32(m)
		}
		// The core's `.go xyz` says nothing; the landing is checked from the status.
		return serveclient.Command{Sent: text, Complete: true}, nil
	}
	// The core's success lines: LANG_GM_ON and LANG_COMMAND_INST_UNBIND_UNBOUND.
	ack := "ok"
	switch text {
	case ".gm on":
		ack = "GM mode is ON"
	case ".instance unbind all":
		ack = "Unbound 0 instance(s)."
	}
	return serveclient.Command{Sent: text, Complete: true, Lines: []string{ack}}, nil
}

func (f *fakeForeverServe) Units(ctx context.Context, _ serveclient.UnitsParams) ([]serveclient.Unit, error) {
	f.mu.Lock()
	block := f.blockUnits
	f.mu.Unlock()
	if block {
		// A read the daemon never answers: it fails at the RPC's own timeout when the pass's ceiling does not cut it.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
			return nil, errors.New("the units read timed out")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]serveclient.Unit(nil), f.units...), nil
}

func (f *fakeForeverServe) PathTo(_ context.Context, x, y, z float32) (serveclient.Path, error) {
	f.mu.Lock()
	from := dungeon.Point{X: f.status.X, Y: f.status.Y, Z: f.status.Z}
	to := dungeon.Point{X: x, Y: y, Z: z}
	f.paths = append(f.paths, fakePath{From: from, To: to})
	st := "complete"
	if f.pathStatus != nil {
		st = f.pathStatus(from, to)
	}
	f.mu.Unlock()
	length := math.Hypot(float64(x-from.X), float64(y-from.Y))
	return serveclient.Path{Map: 700, Status: st, Length: float32(length),
		Points: [][3]float32{{from.X, from.Y, from.Z}, {x, y, z}}}, nil
}

func (f *fakeForeverServe) WalkPath(ctx context.Context, x, y, z float32) (serveclient.Path, error) {
	f.mu.Lock()
	f.walks = append(f.walks, dungeon.Point{X: x, Y: y, Z: z})
	hang, err := f.walkHang, f.walkErr
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return serveclient.Path{}, ctx.Err()
	}
	if err != nil {
		return serveclient.Path{}, err
	}
	return f.PathTo(ctx, x, y, z)
}

// useFakeForeverServe injects the serve client and the slot's character for one test.
func useFakeForeverServe(t *testing.T, f *fakeForeverServe) {
	t.Helper()
	old, oldChar := scoutForeverOpen, scoutForeverSlotCharacter
	scoutForeverOpen = func(string) (scoutForeverClient, error) { return f, nil }
	scoutForeverSlotCharacter = func(string) (string, error) { return "Tester", nil }
	t.Cleanup(func() { scoutForeverOpen, scoutForeverSlotCharacter = old, oldChar })
}

// foreverReport reads the report runCLI wrote for name.
func foreverReport(t *testing.T, dir, name string) planscout.Report {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "out", "scout-"+name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep planscout.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

// foreverSentLines is the fake's GM log.
func foreverSentLines(f *fakeForeverServe) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gm...)
}

// TestScoutForeverAllowlist is the live pass's line guard: the phase lines, a `.go xyz` on the plan's map and the bot
// scout forms pass in their canonical form; anything that changes the world, moves elsewhere or reads its numbers in
// a form the core's parser could take differently is refused.
func TestScoutForeverAllowlist(t *testing.T) {
	for _, line := range []string{
		".gm on", ".gm visible off", ".instance unbind all", ".instance unbind 700",
		".go xyz 1 2 3 700", ".go xyz -1.5 2.25 -3 700", ".go xyz 1 2 3 701",
		".bot scout cfg #1", ".bot scout where #12", ".bot scout walk #1 1 2 3",
		".bot scout path #1 1 2 3", ".bot scout path #1 1 2 3 from 4 5 6",
	} {
		if !scoutAllowedLine(line, 700, 701) {
			t.Errorf("%q refused, want allowed", line)
		}
	}
	for _, line := range []string{
		".npc delete", ".npc add 4100", ".gm off", ".die",
		".go xyz 1 2 3 699", ".go xyz 1 2 3 0", ".instance unbind 701",
		// map 0 is Eastern Kingdoms only when the plan is there; 701 is the portal's map, never an unbind target.
		".go xyz 1 2 3", ".go xyz 1 2 3 700 9", ".go xyz x 2 3 700",
		".go xyz 1 2 3 0700", ".go xyz 1 2 3 700.0", ".go xyz 1e3 2 3 700",
		".go xyz 0x1p4 2 3 700", ".go xyz +1 2 3 700", ".go xyz 1_0 2 3 700",
		".go xyz inf 2 3 700", ".go xyz nan 2 3 700", ".go xyz .5 2 3 700",
		".go xyz 1 2 3 700 ", " .go xyz 1 2 3 700", ".go xyz  1 2 3 700",
		".go xyz 1\t2 3 700", ".bot scout path #1 1 2 3 to 4 5 6", ".bot scout path #0 1 2 3",
	} {
		if scoutAllowedLine(line, 700, 0) {
			t.Errorf("%q allowed, want refused", line)
		}
	}
	if !scoutAllowedLine(".go xyz 1 2 3 0", 0, 0) {
		t.Errorf(".go xyz on map 0 refused for a plan on map 0, want allowed")
	}
	// The canonical re-render: the line the server reads is the one the allow-list judged.
	for in, want := range map[string]string{
		".go xyz 1.50 2.0 -3.250 700":         ".go xyz 1.5 2 -3.25 700",
		".bot scout walk #1 40.00 0 -0.50":    ".bot scout walk #1 40 0 -0.5",
		".bot scout path #2 4 5 6 from 1 2 3": ".bot scout path #2 4 5 6 from 1 2 3",
	} {
		got, ok := scoutCanonicalGM(in, 700, 0)
		if !ok || got != want {
			t.Errorf("scoutCanonicalGM(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
}

// TestScoutForeverPathsStartAtHopOrigins: the server plans from the session's position, so every hop must be measured
// from its own origin row, never from the last stop or the last walk's end.
func TestScoutForeverPathsStartAtHopOrigins(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_o", true)
	fake := newFakeForeverServe()
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_o", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code != exitOK && code != exitPlanScoutFail {
		t.Fatalf("plan-scout: code %d err %q", code, errOut)
	}
	rep := foreverReport(t, dir, "scout_o")
	// Every primary hop is one path query from the positioned row before it to the row's own point.
	var want []fakePath
	var prev *dungeon.Point
	steps := append([]planscout.StepResult(nil), rep.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Step < steps[j].Step })
	for _, st := range steps {
		if st.X == 0 && st.Y == 0 && st.Z == 0 {
			continue
		}
		p := dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}
		if prev != nil {
			want = append(want, fakePath{From: *prev, To: p})
		}
		prev = &p
	}
	if len(want) < 2 {
		t.Fatalf("the fixture yields %d hops, want at least 2", len(want))
	}
	fake.mu.Lock()
	got := append([]fakePath(nil), fake.paths...)
	fake.mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("%d path queries, want %d (one per primary hop): %+v", len(got), len(want), got)
	}
	for i := range want {
		if d := mathDist3(got[i].From, want[i].From); d > 0.01 {
			t.Errorf("path %d asks from %.2f %.2f %.2f, want its hop origin %.2f %.2f %.2f (the last stop is not the origin)",
				i, got[i].From.X, got[i].From.Y, got[i].From.Z, want[i].From.X, want[i].From.Y, want[i].From.Z)
		}
		if d := mathDist3(got[i].To, want[i].To); d > 0.01 {
			t.Errorf("path %d goes to %.2f %.2f %.2f, want %.2f %.2f %.2f", i,
				got[i].To.X, got[i].To.Y, got[i].To.Z, want[i].To.X, want[i].To.Y, want[i].To.Z)
		}
	}
	for _, sent := range foreverSentLines(fake) {
		canon, ok := scoutCanonicalGM(sent, 700, 0)
		if !ok || canon != sent {
			t.Errorf("the pass sent %q, which is not its canonical allow-listed form", sent)
		}
	}
}

// TestScoutForeverLevelsFromLiveUnits is the round's failing case: spawn 101 has no level offline (LoadForeverDB assumes
// 60), and the live `units` read says 30. Only a creature unit with the spawn's own entry counts: a player whose guid
// collides and a creature of another entry are not the spawn. The live aggro reach then comes from level 30 (5 yd at
// party level 60), not from the assumed 60 (20 yd).
func TestScoutForeverLevelsFromLiveUnits(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_live", true)
	fake := newFakeForeverServe(
		serveclient.Unit{GUID: "0x00000000000000000000000000000065", Type: "player", Entry: 4100, Name: "Someone",
			X: 10, Y: 0, Z: 0, Level: 60, Health: 100, MaxHealth: 100},
		serveclient.Unit{GUID: "0x00000000000000000000000000000065", Type: "creature", Entry: 4300, Name: "OtherRat",
			X: 10, Y: 0, Z: 0, Level: 60, Health: 100, MaxHealth: 100},
		serveclient.Unit{GUID: "0x00000000000000000000000000000065", Type: "creature", Entry: 4100, Name: "ForeverRat",
			X: 10, Y: 0, Z: 0, Level: 30, Health: 100, MaxHealth: 100},
	)
	useFakeForeverServe(t, fake)
	slot := t.TempDir()

	code, _, errOut := runCLI("plan-scout", "scout_live", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", outDir, "--live", "--slot", slot)
	if code == exitLivePhaseRefused {
		t.Fatalf("live pass refused: %q", errOut)
	}
	rep := foreverReport(t, dir, "scout_live")
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
		t.Fatalf("live reach %.1f, want 5 (level 30 from the spawn's own creature unit); 20 means the pass kept the assumed 60",
			reach)
	}
	stops := ""
	for _, n := range rep.Notes {
		if strings.HasPrefix(n, "live stops: ") {
			stops = n
		}
	}
	if !regexp.MustCompile(`^live stops: [0-9]+ of [0-9]+ read$`).MatchString(stops) {
		t.Fatalf("the report has no stop count note, only %q (%v)", stops, rep.Notes)
	}
	gm := strings.Join(foreverSentLines(fake), "; ")
	for _, want := range []string{".gm on", ".instance unbind all", ".go xyz"} {
		if !strings.Contains(gm, want) {
			t.Errorf("the live pass never sent %q: %s", want, gm)
		}
	}
}

// TestScoutForeverLevelFillTrustsOnlyTheSpawnsOwnCreature: the entry fallback is gone (every spawn of an entry rolls its
// own level), and a player or another entry on the same guid does not fill the spawn.
func TestScoutForeverLevelFillTrustsOnlyTheSpawnsOwnCreature(t *testing.T) {
	w := &worldspawn.World{Spawns: []worldspawn.Spawn{
		{GUID: 100, Entry: 4100, LevelMax: 60},
		{GUID: 101, Entry: 4100, LevelMax: 60},
	}}
	units := map[int][]world.Unit{1: {
		{GUID: 0x65, Type: "player", Entry: 4100, Level: 60},
		{GUID: 0x65, Type: "creature", Entry: 4300, Level: 60},
		{GUID: 0x65, Type: "creature", Entry: 4100, Level: 30},
	}}
	unknown := map[uint64]bool{100: true, 101: true}
	if filled := scoutForeverFillLevels(w, units, unknown); filled != 1 {
		t.Fatalf("filled %d spawns, want 1", filled)
	}
	if w.Spawns[1].LevelMax != 30 {
		t.Errorf("spawn 101 level %d, want 30 (its own creature unit)", w.Spawns[1].LevelMax)
	}
	if w.Spawns[0].LevelMax != 60 {
		t.Errorf("spawn 100 level %d, want the assumed 60 (no entry fallback)", w.Spawns[0].LevelMax)
	}
}

// TestScoutForeverWalkBudgetIsNotExceeded: a walk that never returns is cut at the budget's end, and no later walk is
// started, so the pass spends the budget once instead of once per hop.
func TestScoutForeverWalkBudgetIsNotExceeded(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_b", true)
	fake := newFakeForeverServe()
	fake.pathStatus = func(dungeon.Point, dungeon.Point) string { return "partial" }
	fake.walkHang = true
	useFakeForeverServe(t, fake)
	start := time.Now()
	code, _, errOut := runCLI("plan-scout", "scout_b", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir(),
		"--walk-budget", "2200ms")
	elapsed := time.Since(start)
	if code != exitOK && code != exitPlanScoutFail {
		t.Fatalf("plan-scout: code %d err %q", code, errOut)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("the pass took %s with a 2.2 s walk budget", elapsed)
	}
	fake.mu.Lock()
	walks := len(fake.walks)
	fake.mu.Unlock()
	if walks != 1 {
		t.Fatalf("%d walks started, want 1 (the budget is spent once, not once per hop)", walks)
	}
	rep := foreverReport(t, dir, "scout_b")
	skipped := 0
	for _, st := range rep.Steps {
		for _, c := range st.Checks {
			if c.Name == planscout.CheckLiveWalk && c.Verdict == planscout.VerdictSkip && c.Reason == "walk budget" {
				skipped++
			}
		}
	}
	if skipped < 2 {
		t.Fatalf("%d live_walk checks skipped as walk budget, want every risky hop after the first (%+v)", skipped, rep.Steps)
	}
}

// TestScoutForeverWalkFailureIsAnErrorRow: a walk the daemon cannot answer is reported as the walk's error, never as a
// measured shortfall, and the pass stops part-way.
func TestScoutForeverWalkFailureIsAnErrorRow(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_w", true)
	fake := newFakeForeverServe()
	fake.pathStatus = func(dungeon.Point, dungeon.Point) string { return "partial" }
	fake.walkErr = errors.New("the daemon has no navmesh folder")
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_w", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code != exitPlanScoutLiveAborted {
		t.Fatalf("plan-scout: code %d, want %d (aborted); err %q", code, exitPlanScoutLiveAborted, errOut)
	}
	if !strings.Contains(errOut, "the walk to") || !strings.Contains(errOut, "no navmesh folder") {
		t.Fatalf("the abort does not carry the walk's error: %q", errOut)
	}
	rep := foreverReport(t, dir, "scout_w")
	found := false
	for _, st := range rep.Steps {
		for _, c := range st.Checks {
			if c.Name != planscout.CheckLiveWalk {
				continue
			}
			found = true
			if c.Verdict != planscout.VerdictSkip || !strings.Contains(c.Reason, "walk to") {
				t.Errorf("live_walk %s %q, want a skip carrying the walk's error", c.Verdict, c.Reason)
			}
			if strings.Contains(c.Reason, "yd short") {
				t.Errorf("live_walk reports a measured shortfall for a failed walk: %q", c.Reason)
			}
		}
	}
	if !found {
		t.Fatal("no live_walk check in the report")
	}
}

// TestScoutForeverGMRefusalStopsThePhase: a refused `.gm on` is a refused phase (18) and nothing else is sent; a refused
// `.go xyz` stops the pass with the server's own words.
func TestScoutForeverGMRefusalStopsThePhase(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_r", true)
	fake := newFakeForeverServe()
	fake.gmRefusal = map[string]string{".gm on": "You do not have permission to use this command."}
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_r", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code != exitLivePhaseRefused {
		t.Fatalf("refused `.gm on`: code %d, want %d; err %q", code, exitLivePhaseRefused, errOut)
	}
	if !strings.Contains(errOut, "permission") {
		t.Errorf("the refusal does not carry the server's line: %q", errOut)
	}
	for _, sent := range foreverSentLines(fake) {
		if sent != ".gm on" {
			t.Errorf("the pass sent %q after the refusal", sent)
		}
	}

	dir2 := t.TempDir()
	foreverScoutPlanFile(t, dir2, "scout_r2", true)
	fake2 := newFakeForeverServe()
	fake2.gmRefusal = map[string]string{".go xyz": "Invalid target coordinates (1 2 700)"}
	useFakeForeverServe(t, fake2)
	code, _, errOut = runCLI("plan-scout", "scout_r2", "--dir", dir2, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir2, "out"), "--live", "--slot", t.TempDir())
	if code != exitPlanScoutLiveAborted {
		t.Fatalf("refused `.go xyz`: code %d, want %d; err %q", code, exitPlanScoutLiveAborted, errOut)
	}
	if !strings.Contains(errOut, "Invalid target coordinates") {
		t.Errorf("the abort does not carry the server's refusal: %q", errOut)
	}
}

// TestScoutForeverMaxMinutesAborts: --max-minutes is the pass's overall ceiling, so a read that never answers ends the
// pass at the ceiling with the aborted (28) report, not after each call's own timeout.
func TestScoutForeverMaxMinutesAborts(t *testing.T) {
	old := scoutForeverCeilingDur
	scoutForeverCeilingDur = func(*planScoutOptions) time.Duration { return 150 * time.Millisecond }
	t.Cleanup(func() { scoutForeverCeilingDur = old })
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_m", true)
	fake := newFakeForeverServe()
	fake.blockUnits = true
	useFakeForeverServe(t, fake)
	start := time.Now()
	code, _, errOut := runCLI("plan-scout", "scout_m", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code != exitPlanScoutLiveAborted {
		t.Fatalf("plan-scout: code %d, want %d; err %q", code, exitPlanScoutLiveAborted, errOut)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the ceiling took %s, want about 150 ms", elapsed)
	}
	if !strings.Contains(errOut, "--max-minutes") {
		t.Errorf("the abort does not name the ceiling: %q", errOut)
	}
	rep := foreverReport(t, dir, "scout_m")
	notes := strings.Join(rep.Notes, "\n")
	if !strings.Contains(notes, "--max-minutes") {
		t.Errorf("the report does not record the ceiling's abort: %q", notes)
	}
}

// TestScoutForeverUnitsAreCopiedPerStep: two spots read at one stop each keep their own units, so nothing that edits
// one step's read can change another's.
func TestScoutForeverUnitsAreCopiedPerStep(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	planPath := foreverScoutPlanFile(t, dir, "scout_u", true)
	fake := newFakeForeverServe(serveclient.Unit{
		GUID: "0x00000000000000000000000000000065", Type: "creature", Entry: 4100, Name: "ForeverRat",
		X: 10, Y: 0, Z: 0, Level: 30, Health: 100, MaxHealth: 100,
	})
	useFakeForeverServe(t, fake)
	p, err := dungeon.Load(planPath)
	if err != nil {
		t.Fatal(err)
	}
	planner := planScoutOpenForever(mmaps, nil)
	defer planner.Close()
	res, err := planexport.Build(p, planexport.Options{Source: "plan-scout"})
	if err != nil {
		t.Fatal(err)
	}
	in := planscout.Input{Plan: p, Rows: res.Rows, Origins: res.Origins}
	w, _, err := worldspawn.LoadForeverDB(foreverExport(), 700)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := planscout.Offline(in, planner, w, planscout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	data, code, reason := runScoutForeverLive(context.Background(),
		&planScoutOptions{serveSlot: t.TempDir(), maxMinutes: 1, walkBudgetD: time.Minute}, in, rep, w, planner)
	if code != exitOK {
		t.Fatalf("live pass: code %d %q", code, reason)
	}
	steps := make([]int, 0, len(data.Units))
	for step := range data.Units {
		steps = append(steps, step)
	}
	if len(steps) < 2 {
		t.Fatalf("%d steps hold units, want at least 2 spots (stand and pull)", len(steps))
	}
	sort.Ints(steps)
	a, b := steps[0], steps[1]
	if len(data.Units[a]) == 0 || len(data.Units[b]) == 0 {
		t.Fatalf("step %d or %d has no units", a, b)
	}
	data.Units[a][0].Level = 1
	if data.Units[b][0].Level == 1 {
		t.Fatalf("steps %d and %d share one units slice", a, b)
	}
}

// TestScoutForeverSlotIdentityRefused: a slot path whose session is another character is refused before any GM line
// is sent, so a mistaken --slot can never enable GM mode, unbind or teleport someone else.
func TestScoutForeverSlotIdentityRefused(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_id", true)
	fake := newFakeForeverServe()
	fake.status.Character = "SomeoneElse"
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_id", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code != exitLivePhaseRefused {
		t.Fatalf("another character's slot: code %d, want %d; err %q", code, exitLivePhaseRefused, errOut)
	}
	if !strings.Contains(errOut, "refusing to drive another character") || !strings.Contains(errOut, "SomeoneElse") {
		t.Errorf("the refusal does not name both characters: %q", errOut)
	}
	if sent := foreverSentLines(fake); len(sent) != 0 {
		t.Errorf("the pass sent %q to another character's session", sent)
	}
}

// TestScoutForeverSlotManifest: the pass reads the character from the slot.json serve wrote, and refuses a manifest
// that names none rather than guessing.
func TestScoutForeverSlotManifest(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := slotdir.Manifest{
		Server: "forever", Slot: "1", Holder: "forever", Character: "Tester", Port: 7311,
		Dir: dir, Binary: filepath.Join(dir, "forever-agent"), Settings: filepath.Join(dir, "settings.json"),
		PIDFile: filepath.Join(dir, "agent.pid"), TokenFile: filepath.Join(dir, "rpc-7311.token"),
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := scoutForeverSlotCharacter(dir)
	if err != nil || got != "Tester" {
		t.Fatalf("slot character %q, %v; want Tester", got, err)
	}
	m.Character = ""
	b, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := scoutForeverSlotCharacter(dir); err == nil {
		t.Fatal("a slot.json without a character was accepted, want refused")
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

// TestScoutForeverReadsSightFromTheServe is the live finding "live_los: no q los answer": the live pass asks the serve's
// walk_ray method from each spot's own sight point to every live unit it read, and stores the answers the checks read. A unit
// the mesh cannot place stays unanswered.
func TestScoutForeverReadsSightFromTheServe(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_los", true)
	fake := newFakeForeverServe(
		serveclient.Unit{GUID: "0x00000000000000000000000000000065", Type: "creature", Entry: 4100, Name: "ForeverRat",
			X: 10, Y: 0, Z: 0, Level: 30, Health: 100, MaxHealth: 100},
		serveclient.Unit{GUID: "0x00000000000000000000000000000066", Type: "creature", Entry: 4100, Name: "Dead",
			X: 11, Y: 0, Z: 0, Level: 30, Dead: true},
	)
	fake.ray = func(_ [3]float32, _, _, _ float32) serveclient.WalkRay {
		return serveclient.WalkRay{Known: true, In: false, Hit: 3, Source: "navmesh_raycast"}
	}
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_los", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code == exitLivePhaseRefused || code == exitPlanScoutLiveAborted {
		t.Fatalf("live pass: code %d err %q", code, errOut)
	}
	fake.mu.Lock()
	calls := fake.rayCalls
	fake.mu.Unlock()
	if calls == 0 {
		t.Fatal("the live pass never asked the serve for sight")
	}
	rep := foreverReport(t, dir, "scout_los")
	for _, n := range rep.Notes {
		if strings.Contains(n, "no walk_ray method") {
			t.Fatalf("the report says the serve has no walk_ray method: %q", n)
		}
	}
}

// TestScoutForeverWithoutALosMethodStillFinishes: a serve without the method answers -32601; the pass notes it once and
// carries on with its other reads instead of aborting.
func TestScoutForeverWithoutALosMethodStillFinishes(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_nolos", true)
	fake := newFakeForeverServe(
		serveclient.Unit{GUID: "0x00000000000000000000000000000065", Type: "creature", Entry: 4100, Name: "ForeverRat",
			X: 10, Y: 0, Z: 0, Level: 30, Health: 100, MaxHealth: 100},
	)
	fake.noWalkRay = true
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_nolos", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code == exitLivePhaseRefused || code == exitPlanScoutLiveAborted {
		t.Fatalf("live pass: code %d err %q", code, errOut)
	}
	rep := foreverReport(t, dir, "scout_nolos")
	n := 0
	for _, note := range rep.Notes {
		if strings.Contains(note, "no walk_ray method") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d notes about the missing walk_ray method, want 1: %v", n, rep.Notes)
	}
}

// TestScoutForeverOneRayErrorDoesNotAbortThePass: a walk_ray read that fails (a build without cgo answers an error on
// every read) leaves its steps unanswered, is noted once, and the pass finishes its other reads.
func TestScoutForeverOneRayErrorDoesNotAbortThePass(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPlanFile(t, dir, "scout_rayerr", true)
	fake := newFakeForeverServe(
		serveclient.Unit{GUID: "0x00000000000000000000000000000065", Type: "creature", Entry: 4100, Name: "ForeverRat",
			X: 10, Y: 0, Z: 0, Level: 30, Health: 100, MaxHealth: 100},
	)
	fake.rayErr = errors.New("navmesh: the engine is not built")
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_rayerr", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code == exitLivePhaseRefused || code == exitPlanScoutLiveAborted {
		t.Fatalf("a failed walk_ray read aborted the pass: code %d err %q", code, errOut)
	}
	rep := foreverReport(t, dir, "scout_rayerr")
	n := 0
	for _, note := range rep.Notes {
		if strings.Contains(note, "walk_ray read") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d notes about the failed read, want 1: %v", n, rep.Notes)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.paths) == 0 {
		t.Fatal("the pass never reached its path reads after the failed ray")
	}
}
