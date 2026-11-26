package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/pjerra/forever-agent/internal/forever/sightcmd"
	"github.com/pjerra/forever-agent/internal/navmesh"
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
	// walkErrAt, when set, answers WalkPath at one point with its error instead of walking (the serve's own
	// "no full route" for a partial one); a nil answer walks the point as usual.
	walkErrAt func(x, y, z float32) error
	// gmRefusal answers a GM line that starts with the key with this line instead of succeeding.
	gmRefusal map[string]string
	// gmErr makes a GM line that starts with the key return this error (the serve's own "not confirmed" answer).
	gmErr map[string]error
	// landAfter delays a `.go xyz` landing: the status shows the point only after this many status reads (0 means at
	// once, as the serve's answer arrives with the command).
	landAfter int
	landing   *serveclient.Status
	landReads int
	// blockStatus and blockUnits wait for the context in Status and Units (the --max-minutes ceiling).
	blockStatus, blockUnits bool
	// ray answers a walk_ray read: nil answers Known and In for every point. noWalkRay makes the serve answer -32601, the
	// way a serve built before the walk_ray method does.
	ray       func(from [3]float32, x, y, z float32) serveclient.WalkRay
	noWalkRay bool
	rayCalls  int
	// rayErr makes every walk_ray read fail with this error (not a missing method).
	rayErr error
	// unitsAt, when set, answers a units read from the session's own position (the way the serve reads around it).
	unitsAt func(x, y float32) []serveclient.Unit
	// los answers the module's own sight query (#259): nil answers every query with a clear line at 12.5 yd. losErr
	// answers it with this error (the daemon's "not supported by this module build"), losRefusal with this reason,
	// losRefusals with one reason for the named guid, and loss logs every query as "a|b" with its time.
	los         func(a, b string) serveclient.LOS
	losErr      error
	losRefusal  string
	losRefusals map[string]string
	// losErrs answers the query for the named guid (canonical form) with this error.
	losErrs  map[string]error
	losCalls int
	loss     []string
	lossAt   []time.Time
	// rays logs every walk_ray read: the sight point and the target.
	rays []fakeRay
}

// fakeLanding is where a `.go xyz` line puts the character, or nil for a line that names no point.
func fakeLanding(text string) *serveclient.Status {
	fs := strings.Fields(text)
	if len(fs) != 6 || !strings.HasPrefix(text, ".go xyz ") {
		return nil
	}
	x, _ := strconv.ParseFloat(fs[2], 32)
	y, _ := strconv.ParseFloat(fs[3], 32)
	z, _ := strconv.ParseFloat(fs[4], 32)
	m, _ := strconv.ParseUint(fs[5], 10, 32)
	return &serveclient.Status{X: float32(x), Y: float32(y), Z: float32(z), Map: uint32(m)}
}

// fakeRay is one walk_ray read.
type fakeRay struct{ From, To [3]float32 }

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
	f.rays = append(f.rays, fakeRay{From: at, To: [3]float32{x, y, z}})
	if f.ray != nil {
		return f.ray(at, x, y, z), nil
	}
	return serveclient.WalkRay{Known: true, In: true, Source: "navmesh_raycast"}, nil
}

// LOS answers the module's own sight query the way the module does: a spot comes back with two decimals and a guid
// with the unit's name (or its 32 hex digits when the fake has no such unit).
func (f *fakeForeverServe) LOS(_ context.Context, p serveclient.LOSParams) (serveclient.LOS, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.losCalls++
	f.loss = append(f.loss, p.A+"|"+p.B)
	f.lossAt = append(f.lossAt, time.Now())
	answer := serveclient.LOS{A: f.echoSide(p.A), B: f.echoSide(p.B)}
	if f.losErr != nil {
		return serveclient.LOS{}, f.losErr
	}
	if f.losRefusal != "" {
		answer.Reason = f.losRefusal
		return answer, nil
	}
	if err, ok := f.losErrs[fakeCanonGuid(p.B)]; ok {
		return serveclient.LOS{}, err
	}
	if reason, ok := f.losRefusals[fakeCanonGuid(p.B)]; ok {
		answer.Reason = reason
		return answer, nil
	}
	if f.los != nil {
		verdict := f.los(p.A, p.B)
		verdict.A, verdict.B = answer.A, answer.B
		return verdict, nil
	}
	dist := 12.5
	answer.Clear, answer.DistYD, answer.SameMap, answer.InWorld = true, &dist, true, true
	return answer, nil
}

// echoSide renders one sent side as the module prints it: a spot with two decimals (the line's coordinate form), a
// resolved guid as the unit's name, and anything else as the word itself.
func (f *fakeForeverServe) echoSide(side string) string {
	if strings.Contains(side, ",") {
		parts := strings.Split(side, ",")
		for i, part := range parts {
			v, err := strconv.ParseFloat(part, 32)
			if err != nil {
				return side
			}
			parts[i] = strconv.FormatFloat(v, 'f', 2, 32)
		}
		return strings.Join(parts, ",")
	}
	canon := fakeCanonGuid(side)
	for _, u := range f.units {
		if fakeCanonGuid(string(u.GUID)) == canon && u.Name != "" {
			return u.Name
		}
	}
	return side
}

// fakeCanonGuid is a guid as 32 lower-case hex digits, the way a comparison between the sent and answered forms needs.
func fakeCanonGuid(s string) string {
	h := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"))
	if len(h) < 32 && len(h) > 0 {
		h = strings.Repeat("0", 32-len(h)) + h
	}
	return h
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
	if f.landing != nil {
		f.landReads++
		if f.landReads > f.landAfter {
			f.status = *f.landing
			f.landing = nil
		}
	}
	return f.status, nil
}

func (f *fakeForeverServe) GM(_ context.Context, text string) (serveclient.Command, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gm = append(f.gm, text)
	for prefix, err := range f.gmErr {
		if strings.HasPrefix(text, prefix) {
			if landing := fakeLanding(text); landing != nil && f.landAfter > 0 {
				f.landing, f.landReads = landing, 0
			}
			return serveclient.Command{Sent: text, Complete: true}, err
		}
	}
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
			if f.landAfter > 0 {
				f.landing = &serveclient.Status{X: float32(x), Y: float32(y), Z: float32(z), Map: uint32(m)}
				f.landReads = 0
			} else {
				f.status.X, f.status.Y, f.status.Z = float32(x), float32(y), float32(z)
				f.status.Map = uint32(m)
			}
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
	if f.unitsAt != nil {
		return f.unitsAt(f.status.X, f.status.Y), nil
	}
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
	hang, err, at := f.walkHang, f.walkErr, f.walkErrAt
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return serveclient.Path{}, ctx.Err()
	}
	if at != nil {
		if e := at(x, y, z); e != nil {
			return serveclient.Path{}, e
		}
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

// TestScoutForeverLevelFillUsesUnitSpawnKey: a Forever serve unit carries a 128-bit guid (parsed as 0 or an
// instance counter) and its matched export spawn id in SpawnID, which can exceed 24 bits. Level fill keys by
// UnitSpawnKey and the full uint32 spawn id, never the low 24 bits of GUID.
func TestScoutForeverLevelFillUsesUnitSpawnKey(t *testing.T) {
	w := &worldspawn.World{Spawns: []worldspawn.Spawn{
		{GUID: 47, Entry: 4100, LevelMax: 60},
		{GUID: 20038045, Entry: 4100, LevelMax: 60},
	}}
	// A 128-bit Forever guid whose low byte is instance counter 0x2f (47), matched to spawn 20038045 (> 24 bits).
	u := foreverWorldUnit(serveclient.Unit{
		GUID: "0x20011830A00B0DC0000000000000002F", Type: "creature", Entry: 4100, Level: 32,
	}, 20038045)
	// Also check a 64-bit unit whose low 24 bits match spawn 47 while SpawnID names 20038045.
	u2 := world.Unit{GUID: 0x2F, SpawnID: 20038045, Type: "creature", Entry: 4100, Level: 32}
	for _, tc := range []struct {
		name string
		unit world.Unit
	}{
		{"128-bit guid from serve", u},
		{"low 24 bits collide with another spawn", u2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w.Spawns[0].LevelMax, w.Spawns[1].LevelMax = 60, 60
			units := map[int][]world.Unit{1: {tc.unit}}
			unknown := map[uint64]bool{47: true, 20038045: true}
			if filled := scoutForeverFillLevels(w, units, unknown); filled != 1 {
				t.Fatalf("filled %d spawns, want 1", filled)
			}
			if w.Spawns[1].LevelMax != 32 {
				t.Errorf("spawn 20038045 level %d, want 32", w.Spawns[1].LevelMax)
			}
			if w.Spawns[0].LevelMax != 60 {
				t.Errorf("spawn 47 level %d, want the assumed 60 (must not match the instance counter)", w.Spawns[0].LevelMax)
			}
		})
	}
}

// unconfirmedServe is a fake serve whose first drop `.go xyz` lines are the core's silent drop: the serve answers
// each of them with its own "not confirmed" once its budget is spent and the character stays where it was, and the
// line after them lands.
type unconfirmedServe struct {
	*fakeForeverServe
	mu    sync.Mutex
	drops int
	sends int
}

func (f *unconfirmedServe) GM(ctx context.Context, text string) (serveclient.Command, error) {
	if strings.HasPrefix(text, ".go xyz ") {
		f.mu.Lock()
		f.sends++
		drop := f.sends <= f.drops
		f.mu.Unlock()
		if drop {
			f.fakeForeverServe.mu.Lock()
			f.gm = append(f.gm, text)
			f.fakeForeverServe.mu.Unlock()
			return serveclient.Command{Sent: text, Complete: true},
				errors.New(`serveclient: gm: rpc error -32003: "` + text + `": the teleport is not confirmed: the character did not arrive within 8s (at 0 0 on map 700)`)
		}
	}
	return f.fakeForeverServe.GM(ctx, text)
}

// TestScoutStandResendsALineTheCoreDropped is the live K4 stop: the core drops a `.go xyz` that arrives while the
// previous teleport is still settling, and says nothing about it, so the serve answers "not confirmed". The stand
// sends the line once more after that settle and the pass goes on; before, it took the unconfirmed answer for a slow
// landing, polled the status it already stood at and aborted on the next stand.
func TestScoutStandResendsALineTheCoreDropped(t *testing.T) {
	old := scoutTeleportSettle
	scoutTeleportSettle = time.Millisecond
	defer func() { scoutTeleportSettle = old }()
	f := &fakeForeverServe{status: serveclient.Status{Character: "Testchar", State: "online", Map: 700}}
	s := &unconfirmedServe{fakeForeverServe: f, drops: 1}
	if err := foreverStandAt(context.Background(), s, dungeon.Point{X: 40}, 700); err != nil {
		t.Fatalf("the stand gave up on the line the core dropped: %v", err)
	}
	if s.sends != 2 {
		t.Fatalf("%d `.go xyz` lines, want 2 (the first one was dropped in silence)", s.sends)
	}
	if f.status.X != 40 {
		t.Fatalf("the character stands at %.1f, want 40: the second line did not land", f.status.X)
	}
}

// TestScoutStandStopsWhenTheLineIsTwiceUnconfirmed: the retry is bounded, and the last answer is the stand's error.
func TestScoutStandStopsWhenTheLineIsTwiceUnconfirmed(t *testing.T) {
	old := scoutTeleportSettle
	scoutTeleportSettle = time.Millisecond
	defer func() { scoutTeleportSettle = old }()
	s := &unconfirmedServe{
		fakeForeverServe: &fakeForeverServe{status: serveclient.Status{Character: "Testchar", State: "online", Map: 700}},
		drops:            scoutTeleportAttempts,
	}
	err := foreverStandAt(context.Background(), s, dungeon.Point{X: 40}, 700)
	if err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("two dropped lines gave %v, want the serve's own words", err)
	}
	if s.sends != scoutTeleportAttempts {
		t.Fatalf("%d `.go xyz` lines, want %d", s.sends, scoutTeleportAttempts)
	}
}

// TestScoutStandDoesNotRetryAnotherGMError: a refusal that is not the core's silent drop is the stand's answer at
// once, with the server's own words.
func TestScoutStandDoesNotRetryAnotherGMError(t *testing.T) {
	s := &unconfirmedServe{fakeForeverServe: &fakeForeverServe{
		status: serveclient.Status{Character: "Testchar", State: "online", Map: 700},
		gmErr:  map[string]error{".go xyz ": errors.New(`serveclient: gm: rpc error -32003: "no permission"`)},
	}}
	if err := foreverStandAt(context.Background(), s, dungeon.Point{X: 40}, 700); err == nil || !strings.Contains(err.Error(), "no permission") {
		t.Fatalf("a refused line gave %v, want the server's own words", err)
	}
	if s.sends != 1 {
		t.Fatalf("%d `.go xyz` lines, want 1 (a refusal is not retried)", s.sends)
	}
	if len(foreverSentLines(s.fakeForeverServe)) != 1 {
		t.Fatalf("GM lines %q, want one", foreverSentLines(s.fakeForeverServe))
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

// TestScoutForeverNoFullRouteGoesOnToTheNextStand: a walk_path that answers no full route is not a reason to stop the
// pass, for either status the serve gives it (a partial route, and a route it finds none of). The stand's walk is
// recorded with the distance the session reached, the next stand is still planned and walked, and the report's note
// counts the stands without a full route.
func TestScoutForeverNoFullRouteGoesOnToTheNextStand(t *testing.T) {
	for _, status := range []string{"partial", "none"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			mmaps := foreverFlat(t)
			foreverScoutPlanFile(t, dir, "scout_pw", true)
			fake := newFakeForeverServe()
			fake.pathStatus = func(dungeon.Point, dungeon.Point) string { return status }
			fake.walkErrAt = func(x, y, z float32) error {
				if x != 20 {
					return nil
				}
				// The session moved halfway to the stand before the server answered that it has no full route.
				fake.mu.Lock()
				fake.status.X, fake.status.Y, fake.status.Z = 15, 0, 0
				fake.mu.Unlock()
				return fmt.Errorf("serveclient: walk_path: rpc error -32000: walk_path: no full route to 20.0 0.0 0.0 (%s)", status)
			}
			useFakeForeverServe(t, fake)
			code, _, errOut := runCLI("plan-scout", "scout_pw", "--dir", dir, "--mmaps", mmaps,
				"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
			if code == exitPlanScoutLiveAborted {
				t.Fatalf("the pass stopped on a no-full-route walk: %q", errOut)
			}
			fake.mu.Lock()
			paths := len(fake.paths)
			fake.mu.Unlock()
			if paths < 2 {
				t.Fatalf("%d path queries; want the next stand scouted after the no-full-route walk", paths)
			}
			rep := foreverReport(t, dir, "scout_pw")
			recorded := 0
			for _, st := range rep.Steps {
				for _, c := range st.Checks {
					if c.Name != planscout.CheckLiveWalk || c.Verdict != planscout.VerdictSkip {
						continue
					}
					if reached, ok := c.Data["reached_yd"]; ok {
						recorded++
						if reached != 5 {
							t.Errorf("live_walk reached_yd = %v; want 5 (from x 10 to x 15)", reached)
						}
						if !strings.Contains(c.Reason, "no full route") {
							t.Errorf("live_walk reason %q; want the server's no-full-route answer", c.Reason)
						}
					}
				}
			}
			if recorded != 1 {
				t.Fatalf("%d no-full-route live_walk checks; want one", recorded)
			}
			note := ""
			for _, n := range rep.Notes {
				if strings.HasPrefix(n, "live walks: ") {
					note = n
				}
			}
			if !regexp.MustCompile(`^live walks: 1 stand\(s\) without a full route`).MatchString(note) {
				t.Fatalf("the report has no no-full-route count, only %q (%v)", note, rep.Notes)
			}
		})
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

// TestScoutForeverReadsEverySpotWhereItStands: two pull rows 60 yd apart share one stop (100 yd), but the units are read
// within 40 yd of the session, so the second row's pack is only seen when the scout stands there. The live finding: one
// stop read, no sight answer for the later rows.
func TestScoutForeverReadsEverySpotWhereItStands(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	leg := dungeon.Leg{Name: "one", To: dungeon.Point{X: 100}, PullGroups: []dungeon.PullGroup{
		{Entries: []uint32{9999}, Positions: []dungeon.Point{{X: 20}}, Radius: 5},
		{Entries: []uint32{9999}, Positions: []dungeon.Point{{X: 80}}, Radius: 5},
	}}
	b, err := dungeon.Marshal(&dungeon.Plan{Version: 1, Name: "scout_two", Map: 700, Legs: []dungeon.Leg{leg}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scout_two.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	unit := func(guid string, x float32) serveclient.Unit {
		return serveclient.Unit{GUID: serveclient.GUID(guid), Type: "creature", Entry: 4100, Name: "Rat",
			X: x, Y: 0, Z: 0, Level: 30, Health: 100, MaxHealth: 100}
	}
	fake := newFakeForeverServe()
	fake.unitsAt = func(x, _ float32) []serveclient.Unit {
		// The read is wider than 40 yd (it allows for a landing offset), so the far pack is also returned to the near
		// spot (65 yd away) and must be cut back to the spot's own 40 yd.
		_ = x
		return []serveclient.Unit{unit("0x00000000000000000000000000000065", 25), unit("0x00000000000000000000000000000066", 85)}
	}
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_two", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code == exitLivePhaseRefused || code == exitPlanScoutLiveAborted {
		t.Fatalf("live pass: code %d err %q", code, errOut)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var near, far bool
	for _, r := range fake.rays {
		near = near || r.To[0] < 30
		far = far || (r.From[0] > 70 && r.To[0] > 70)
	}
	nearRays, farRays := 0, 0
	for _, r := range fake.rays {
		if r.To[0] < 30 {
			nearRays++
		}
		if r.To[0] > 70 {
			farRays++
		}
	}
	// The near pack is rayed from its own pull row only: the read returns it to every spot, and a spot more than 40 yd
	// from it (the stands at 80 and 100) must cut it back.
	if nearRays != 1 || farRays < 1 {
		t.Fatalf("each pack must be rayed once (near %d, far %d): %+v", nearRays, farRays, fake.rays)
	}
	if !near || !far {
		t.Fatalf("a pull row got no sight ray to its own pack (near %v, far %v): %+v", near, far, fake.rays)
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

// foreverScoutPackPlanFile writes a plan on map 700 whose pull group is the export's own pack (entry 4100, spawns 100
// and 101 at x 0 and 3): the live pass has a target pack to ask the server about.
func foreverScoutPackPlanFile(t *testing.T, dir, name string) {
	t.Helper()
	group := dungeon.PullGroup{
		Entries:   []uint32{4100},
		Positions: []dungeon.Point{{X: 3}},
		Radius:    5,
		Approach:  []dungeon.Point{{X: -7}},
		StandPull: true,
	}
	leg := dungeon.Leg{Name: "one", To: dungeon.Point{X: 40}, PullGroups: []dungeon.PullGroup{group}}
	b, err := dungeon.Marshal(&dungeon.Plan{Version: 1, Name: name, Map: 700, Legs: []dungeon.Leg{leg}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// foreverPackUnits are the export's pack: spawns 100 and 101 of entry 4100, at x 0 and 3.
func foreverPackUnits() []serveclient.Unit {
	return []serveclient.Unit{
		{GUID: "0x00000000000000000000000000000064", Type: "creature", Entry: 4100, Name: "ForeverRat",
			X: 0, Y: 0, Z: 0, Level: 30, Health: 100, MaxHealth: 100},
		{GUID: "0x00000000000000000000000000000065", Type: "creature", Entry: 4100, Name: "ForeverRat",
			X: 3, Y: 0, Z: 0, Level: 30, Health: 100, MaxHealth: 100},
	}
}

// foreverLOSCheck is the pull row's live_los check in a report.
func foreverLOSCheck(t *testing.T, rep planscout.Report, step int) planscout.Check {
	t.Helper()
	for _, st := range rep.Steps {
		if st.Step != step {
			continue
		}
		for _, c := range st.Checks {
			if c.Name == planscout.CheckLiveLOS {
				return c
			}
		}
	}
	t.Fatalf("step %d has no %s check", step, planscout.CheckLiveLOS)
	return planscout.Check{}
}

// TestScoutForeverRecordsServerSightBesideTheRay: for the pull's stand the pass asks the module's los for every pack
// unit the read saw, records the server's answer beside the navmesh ray under the same key, and the pull row's check
// reports both plus the disagreement, with one note for the pass.
func TestScoutForeverRecordsServerSightBesideTheRay(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPackPlanFile(t, dir, "scout_pack")
	fake := newFakeForeverServe(foreverPackUnits()...)
	// The mesh cannot say the pack is visible; the server sees spawn 101 only.
	fake.ray = func(_ [3]float32, _, _, _ float32) serveclient.WalkRay {
		return serveclient.WalkRay{Known: true, In: false, Hit: 3, Source: "navmesh_raycast"}
	}
	fake.los = func(_, b string) serveclient.LOS {
		dist := 12.5
		return serveclient.LOS{Clear: strings.HasSuffix(b, "65"), DistYD: &dist, SameMap: true, InWorld: true}
	}
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_pack", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code == exitLivePhaseRefused || code == exitPlanScoutLiveAborted {
		t.Fatalf("live pass: code %d err %q", code, errOut)
	}
	fake.mu.Lock()
	calls, queries := fake.losCalls, append([]string(nil), fake.loss...)
	at := append([]time.Time(nil), fake.lossAt...)
	fake.mu.Unlock()
	if calls != 2 {
		t.Fatalf("%d los query(ies), want one per pack unit (%q)", calls, queries)
	}
	// The pass paces its queries: the module's bucket refills one every 200 ms.
	if len(at) == 2 && at[1].Sub(at[0]) < 200*time.Millisecond {
		t.Fatalf("the two queries were %s apart, want at least 200ms", at[1].Sub(at[0]))
	}
	for _, q := range queries {
		if !strings.HasSuffix(q, "|0x00000000000000000000000000000064") && !strings.HasSuffix(q, "|0x00000000000000000000000000000065") {
			t.Fatalf("a los query names %q, not a pack guid", q)
		}
		if !strings.HasPrefix(q, "-7,0,0|") {
			t.Fatalf("a los query asks from %q, not the pull's stand", q)
		}
	}
	rep := foreverReport(t, dir, "scout_pack")
	// The pull row is the step whose check carries the two pack units.
	var step int
	for _, st := range rep.Steps {
		for _, c := range st.Checks {
			if c.Name == planscout.CheckLiveLOS {
				step = st.Step
			}
		}
	}
	if step == 0 {
		t.Fatal("the report has no live_los check")
	}
	c := foreverLOSCheck(t, rep, step)
	if c.Data["server_answered"] != 2 || c.Data["server_in_sight"] != 1 || c.Data["disagree"] != 1 {
		t.Fatalf("check data %v, want the server's two answers, one in sight, one disagreement", c.Data)
	}
	if c.Data["answered"] != 2 || c.Data["in_sight"] != 0 {
		t.Fatalf("check data %v, want the ray's two answers, none in sight", c.Data)
	}
	if !strings.Contains(c.Reason, "the server's sight") || !strings.Contains(c.Reason, "disagrees on 1") {
		t.Fatalf("check reason %q, want the server's verdict and the disagreement", c.Reason)
	}
	named := false
	for _, n := range rep.Notes {
		if strings.Contains(n, "disagree on 1 of 2 sight answers") {
			named = true
		}
		if strings.Contains(n, "does not answer los") {
			t.Fatalf("the report says the module has no los command: %q", n)
		}
	}
	if !named {
		t.Fatalf("the report has no disagreement summary: %q", rep.Notes)
	}
}

// TestScoutForeverServerSightFallsBack: a module build without the los command is said once and the pass asks no more;
// a rate refusal is retried once for the same query and, if it comes back again, said once and the pass asks no more.
func TestScoutForeverServerSightFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeForeverServe)
		want  string
		calls int
	}{
		{"an older module", func(f *fakeForeverServe) {
			f.losErr = &serveclient.Error{Code: rpc.CodeGame,
				Message: "los: not supported by this module build (the server does not answer .tb los)", Method: "los"}
		}, "does not answer los", 1},
		{"a rate refusal", func(f *fakeForeverServe) { f.losRefusal = "rate" }, "refused the los query for rate twice", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mmaps := foreverFlat(t)
			foreverScoutPackPlanFile(t, dir, "scout_fallback")
			fake := newFakeForeverServe(foreverPackUnits()...)
			fake.ray = func(_ [3]float32, _, _, _ float32) serveclient.WalkRay {
				return serveclient.WalkRay{Known: true, In: true, Source: "navmesh_raycast"}
			}
			tc.setup(fake)
			useFakeForeverServe(t, fake)
			code, _, errOut := runCLI("plan-scout", "scout_fallback", "--dir", dir, "--mmaps", mmaps,
				"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
			if code == exitLivePhaseRefused || code == exitPlanScoutLiveAborted {
				t.Fatalf("live pass: code %d err %q", code, errOut)
			}
			fake.mu.Lock()
			calls := fake.losCalls
			fake.mu.Unlock()
			if calls != tc.calls {
				t.Fatalf("%d los queries, want %d: one try, at most one retry, and then the pass must not ask again", calls, tc.calls)
			}
			rep := foreverReport(t, dir, "scout_fallback")
			said, checked := 0, false
			for _, n := range rep.Notes {
				if strings.Contains(n, tc.want) {
					said++
				}
			}
			if said != 1 {
				t.Fatalf("%d note(s) say %q: %q", said, tc.want, rep.Notes)
			}
			for _, st := range rep.Steps {
				for _, c := range st.Checks {
					if c.Name != planscout.CheckLiveLOS {
						continue
					}
					checked = true
					if c.Data["server_answered"] != 0 || c.Data["answered"] == 0 {
						t.Fatalf("check data %v, want the ray's answers alone", c.Data)
					}
				}
			}
			if !checked {
				t.Fatal("the report has no live_los check")
			}
		})
	}
}

// TestScoutForeverServerSightSkipsOneRefusedUnit: a unit that despawned, died or changed map between the read and the
// query answers unknown-unit, not-in-world or different-maps. Below GM the module says no-access for the same gone
// creature (a ground point against a creature guid needs no access of its own), so that is a per-unit skip too. The
// one unit is left unanswered, with its own note, and the pass asks the rest; it does not turn server sight off.
func TestScoutForeverServerSightSkipsOneRefusedUnit(t *testing.T) {
	for _, reason := range []string{"unknown-unit", "not-in-world", "different-maps", "no-access"} {
		t.Run(reason, func(t *testing.T) {
			dir := t.TempDir()
			mmaps := foreverFlat(t)
			foreverScoutPackPlanFile(t, dir, "scout_skip")
			fake := newFakeForeverServe(foreverPackUnits()...)
			fake.ray = func(_ [3]float32, _, _, _ float32) serveclient.WalkRay {
				return serveclient.WalkRay{Known: true, In: false, Hit: 3, Source: "navmesh_raycast"}
			}
			fake.losRefusals = map[string]string{fakeCanonGuid("0x00000000000000000000000000000064"): reason}
			useFakeForeverServe(t, fake)
			code, _, errOut := runCLI("plan-scout", "scout_skip", "--dir", dir, "--mmaps", mmaps,
				"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
			if code == exitLivePhaseRefused || code == exitPlanScoutLiveAborted {
				t.Fatalf("live pass: code %d err %q", code, errOut)
			}
			fake.mu.Lock()
			calls := fake.losCalls
			fake.mu.Unlock()
			if calls != 2 {
				t.Fatalf("%d los queries, want both pack units asked", calls)
			}
			rep := foreverReport(t, dir, "scout_skip")
			noted := false
			for _, n := range rep.Notes {
				if strings.Contains(n, "does not answer los") || strings.Contains(n, "refused the los query") {
					t.Fatalf("one refused unit stopped the pass: %q", n)
				}
				want := "the module refused 1 pack unit(s)"
				if reason == "no-access" {
					want = "answered no-access for 1 pack unit(s)"
				}
				if strings.Contains(n, want) {
					noted = true
				}
			}
			if !noted {
				t.Fatalf("the report does not record the %s unit: %q", reason, rep.Notes)
			}
			var step int
			for _, st := range rep.Steps {
				for _, c := range st.Checks {
					if c.Name == planscout.CheckLiveLOS {
						step = st.Step
					}
				}
			}
			if step == 0 {
				t.Fatal("the report has no live_los check")
			}
			c := foreverLOSCheck(t, rep, step)
			if c.Data["server_answered"] != 1 {
				t.Fatalf("check data %v, want the one unit the server answered", c.Data)
			}
		})
	}
}

// TestScoutForeverServerSightBadArgumentEndsThePass: the scout sends canonical sides, so bad-argument means the module
// and this client read the line differently and every unit would answer the same. That one refusal ends the sight
// pass (said once) instead of being counted per unit.
func TestScoutForeverServerSightBadArgumentEndsThePass(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPackPlanFile(t, dir, "scout_badarg")
	fake := newFakeForeverServe(foreverPackUnits()...)
	fake.ray = func(_ [3]float32, _, _, _ float32) serveclient.WalkRay {
		return serveclient.WalkRay{Known: true, In: false, Hit: 3, Source: "navmesh_raycast"}
	}
	fake.losRefusals = map[string]string{fakeCanonGuid("0x00000000000000000000000000000064"): "bad-argument"}
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_badarg", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code == exitLivePhaseRefused || code == exitPlanScoutLiveAborted {
		t.Fatalf("live pass: code %d err %q", code, errOut)
	}
	fake.mu.Lock()
	calls := fake.losCalls
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("%d los queries, want the pass to stop after the bad-argument answer", calls)
	}
	rep := foreverReport(t, dir, "scout_badarg")
	said := 0
	for _, n := range rep.Notes {
		if strings.Contains(n, "refused the los query for bad-argument") {
			said++
		}
	}
	if said != 1 {
		t.Fatalf("%d note(s) say the module refused the query for bad-argument: %q", said, rep.Notes)
	}
}

// TestScoutForeverServerSightGoesOnPastAnUnreadAnswer: one unit's answer that is not a record this client understands
// (a side the module did not echo as expected, a malformed line) is recorded for that unit as "answer not understood";
// the rest of the pack is still asked and the pass does not stop.
func TestScoutForeverServerSightGoesOnPastAnUnreadAnswer(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPackPlanFile(t, dir, "scout_unread")
	fake := newFakeForeverServe(foreverPackUnits()...)
	fake.ray = func(_ [3]float32, _, _, _ float32) serveclient.WalkRay {
		return serveclient.WalkRay{Known: true, In: false, Hit: 3, Source: "navmesh_raycast"}
	}
	fake.losErrs = map[string]error{fakeCanonGuid("0x00000000000000000000000000000064"): &serveclient.Error{Code: rpc.CodeGame,
		Message: "los: " + sightcmd.NotARecord + ": tbcmd: the answer names b=\"Dark Iron Dwarf\"", Method: "los"}}
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_unread", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code == exitLivePhaseRefused || code == exitPlanScoutLiveAborted {
		t.Fatalf("live pass: code %d err %q", code, errOut)
	}
	fake.mu.Lock()
	calls := fake.losCalls
	fake.mu.Unlock()
	if calls != 2 {
		t.Fatalf("%d los queries, want both pack units asked", calls)
	}
	rep := foreverReport(t, dir, "scout_unread")
	understood := 0
	for _, n := range rep.Notes {
		if strings.Contains(n, "does not answer los") || strings.Contains(n, "los query for") {
			t.Fatalf("one unread answer stopped the pass: %q", n)
		}
		if strings.Contains(n, "answer not understood") && strings.Contains(n, "1 pack unit") {
			understood++
		}
	}
	if understood != 1 {
		t.Fatalf("%d note(s) record the unread answer, want 1: %q", understood, rep.Notes)
	}
	var step int
	for _, st := range rep.Steps {
		for _, c := range st.Checks {
			if c.Name == planscout.CheckLiveLOS {
				step = st.Step
			}
		}
	}
	if step == 0 {
		t.Fatal("the report has no live_los check")
	}
	if c := foreverLOSCheck(t, rep, step); c.Data["server_answered"] != 1 {
		t.Fatalf("check data %v, want the one unit the server answered", c.Data)
	}
}

// TestScoutForeverSightPaceAndBound: the sight pass spaces its queries by the named pace, and its named bound ends the
// asking instead of holding the live pass.
func TestScoutForeverSightPaceAndBound(t *testing.T) {
	ctx := context.Background()
	expired := scoutForeverSight{ok: true, end: time.Now().Add(-time.Second)}
	if err := expired.pace(ctx); !errors.Is(err, errScoutSightEnd) {
		t.Fatalf("a pass past its bound: %v, want the bound", err)
	}
	var sight scoutForeverSight
	start := time.Now()
	if err := sight.pace(ctx); err != nil {
		t.Fatalf("first query: %v", err)
	}
	if err := sight.pace(ctx); err != nil {
		t.Fatalf("second query: %v", err)
	}
	if d := time.Since(start); d < scoutForeverSightPace {
		t.Fatalf("two queries were %s apart, want at least %s", d, scoutForeverSightPace)
	}
	if sight.end.Sub(start) < scoutForeverSightPass-time.Second {
		t.Fatalf("the pass bound is %s from the first query, want %s", sight.end.Sub(start), scoutForeverSightPass)
	}
}

// TestScoutForeverSightNotAsked: once the pass has stopped asking, a stand with a pack is counted as not asked and no
// query goes out for it.
func TestScoutForeverSightNotAsked(t *testing.T) {
	fake := newFakeForeverServe(foreverPackUnits()...)
	sight := scoutForeverSight{ok: false}
	data := planscout.LiveData{ServerLOS: map[planscout.LOSKey]bool{}}
	spot := planscout.LiveSpot{Step: 1, TargetGuids: []planscout.LiveTarget{{Guid: 100}, {Guid: 101}}}
	if err := foreverServerSightAt(context.Background(), fake, spot, nil, nil, &data, &sight); err != nil {
		t.Fatalf("sight: %v", err)
	}
	if sight.notAsked != 1 {
		t.Fatalf("%d stand(s) not asked, want 1", sight.notAsked)
	}
	if fake.losCalls != 0 {
		t.Fatalf("%d los queries after the pass stopped asking", fake.losCalls)
	}
}

// TestForeverServerSightAt128BitGUID: a Forever serve prints 128-bit guids whose low part is an instance counter, not
// a spawn id. The live read is named by the export spawn of its entry that stands nearest it, the ray is stored under
// that spawn id, and the los query carries the guid string exactly as the serve printed it.
func TestForeverServerSightAt128BitGUID(t *testing.T) {
	guid := serveclient.GUID("0x20011830A00B0DC0000000000000002F")
	units := []serveclient.Unit{{
		GUID: guid, Type: "creature", Entry: 11318, Name: "CaveTrog", X: -154.04, Y: 27.97, Z: -38.57,
		Level: 16, Health: 100, MaxHealth: 100,
	}}
	spawns := []worldspawn.Spawn{{GUID: 20038045, Entry: 11318, Pos: dungeon.Point{X: -154, Y: 28, Z: -38.6}}}
	match := foreverMatchSpawns(units, spawns, nil)
	if match[0] != 20038045 {
		t.Fatalf("match = %v; want unit 0 named by spawn 20038045", match)
	}
	spot := planscout.LiveSpot{
		Step: 1, Point: dungeon.Point{X: -150, Y: 30, Z: -38},
		LOSPoint: dungeon.Point{X: -150, Y: 30, Z: -38}, TargetGuids: []planscout.LiveTarget{{Guid: 20038045, Entry: 11318}},
	}
	data := planscout.LiveData{LOS: map[planscout.LOSKey]bool{}, ServerLOS: map[planscout.LOSKey]bool{}}
	fake := newFakeForeverServe(units...)
	wu := []world.Unit{foreverWorldUnit(units[0], match[0])}
	if wu[0].SpawnID != 20038045 {
		t.Fatalf("the world unit = %+v; want the spawn id carried", wu[0])
	}
	ok, err := foreverWalkRayAt(context.Background(), fake, spot, wu, data.LOS)
	if !ok || err != nil {
		t.Fatalf("walk ray: ok=%v err=%v", ok, err)
	}
	if _, have := data.LOS[planscout.LOSKey{Step: 1, Guid: 20038045}]; !have {
		t.Fatalf("LOS = %v; want the ray stored under spawn 20038045", data.LOS)
	}
	sight := scoutForeverSight{ok: true}
	if err := foreverServerSightAt(context.Background(), fake, spot, units, match, &data, &sight); err != nil {
		t.Fatalf("sight: %v", err)
	}
	if _, have := data.ServerLOS[planscout.LOSKey{Step: 1, Guid: 20038045}]; !have {
		t.Fatalf("ServerLOS = %v; want the server's answer under spawn 20038045", data.ServerLOS)
	}
	if len(fake.loss) != 1 || !strings.HasSuffix(fake.loss[0], "|"+string(guid)) {
		t.Fatalf("los queries = %v; want the live guid string unchanged", fake.loss)
	}
	if sight.unmatched != 0 || sight.skipped != 0 || sight.unread != 0 {
		t.Fatalf("sight = %+v; want the one target answered", sight)
	}
}

// TestForeverMatchSpawns: nearest first, one live unit per spawn, entry and distance rules, a wander radius and a
// patrol's path, and players never matched.
func TestForeverMatchSpawns(t *testing.T) {
	spawns := []worldspawn.Spawn{
		{GUID: 10, Entry: 100, Pos: dungeon.Point{}},
		{GUID: 11, Entry: 100, Pos: dungeon.Point{X: 2}},
		{GUID: 12, Entry: 100, Pos: dungeon.Point{X: 40}},              // entry 100, far from every unit
		{GUID: 13, Entry: 200, Pos: dungeon.Point{X: 100}, Wander: 10}, // 11 yd away, matches on its wander
		{GUID: 14, Entry: 300, Pos: dungeon.Point{X: 200}},             // a patrol away from its spawn point
		{GUID: 15, Entry: 400, Pos: dungeon.Point{X: 300}},             // 11 yd away, no wander to cover it
		{GUID: 1 << 40, Entry: 500, Pos: dungeon.Point{X: 400}},        // no uint32 spawn id
	}
	patrols := []worldspawn.Patrol{{SpawnGUIDs: []uint64{14},
		Path: []dungeon.Point{{X: 220, Y: 0}, {X: 260, Y: 0}}}}
	units := []serveclient.Unit{
		{GUID: "0x1", Type: "creature", Entry: 100, X: 0.5}, // nearer 10 than 11
		{GUID: "0x2", Type: "creature", Entry: 100, X: 2.5}, // 11
		{GUID: "0x3", Type: "creature", Entry: 100, X: 3},   // 11
		{GUID: "0x4", Type: "creature", Entry: 200, X: 111}, // 13 on its wander
		{GUID: "0x5", Type: "creature", Entry: 300, X: 260}, // 14 on its path
		{GUID: "0x6", Type: "creature", Entry: 400, X: 311}, // 11 yd, out of reach
		{GUID: "0x7", Type: "player", Entry: 500, X: 400},   // a player is never a spawn
		{GUID: "0x8", Type: "creature", Entry: 500, X: 400}, // a spawn id past uint32
		{GUID: "0x9", Type: "creature", X: 0},               // entry 0
		{GUID: "0xA", Type: "creature", Entry: 100, X: 0, Dead: true},
	}
	match := foreverMatchSpawns(units, spawns, patrols)
	want := map[int]uint32{0: 10, 1: 11, 3: 13, 4: 14}
	if len(match) != len(want) {
		t.Fatalf("match = %v; want %v", match, want)
	}
	for ui, id := range want {
		if match[ui] != id {
			t.Fatalf("unit %d matched spawn %d; want %d (match %v)", ui, match[ui], id, match)
		}
	}
	// Nearest first: unit 2 (x 3) may take 11 only if unit 1 does not; unit 1 stands nearer, so both keep one.
	if _, taken := match[2]; taken {
		t.Fatalf("two units took one spawn: %v", match)
	}
}

// TestForeverMatchSpawnsNearestFirst: when two units stand near one spawn and a nearer spawn exists, the nearer pair
// wins and the other unit takes the farther spawn.
func TestForeverMatchSpawnsNearestFirst(t *testing.T) {
	spawns := []worldspawn.Spawn{
		{GUID: 20, Entry: 100, Pos: dungeon.Point{X: 1}},
		{GUID: 21, Entry: 100, Pos: dungeon.Point{X: 12}},
	}
	units := []serveclient.Unit{
		{GUID: "0x1", Type: "creature", Entry: 100, X: 2},
		{GUID: "0x2", Type: "creature", Entry: 100, X: 3},
	}
	match := foreverMatchSpawns(units, spawns, nil)
	if match[0] != 20 || match[1] != 21 {
		t.Fatalf("match = %v; want the nearer unit on spawn 20 and the other on 21", match)
	}
}

// TestForeverServerSightNotesUnmatchedTargets: a pass whose target pack has no live unit near its spawn says so
// instead of reporting 0 of N in silence, and sends no query.
func TestForeverServerSightNotesUnmatchedTargets(t *testing.T) {
	dir := t.TempDir()
	mmaps := foreverFlat(t)
	foreverScoutPackPlanFile(t, dir, "scout_nomatch")
	// The read holds the pack's entry, but nowhere near either spawn: nothing matches.
	units := []serveclient.Unit{
		{GUID: "0x20011830A00B0DC0000000000000AA", Type: "creature", Entry: 4100, Name: "ForeverRat",
			X: -100, Y: 0, Z: 0, Level: 30, Health: 100, MaxHealth: 100},
	}
	fake := newFakeForeverServe(units...)
	useFakeForeverServe(t, fake)
	code, _, errOut := runCLI("plan-scout", "scout_nomatch", "--dir", dir, "--mmaps", mmaps,
		"--spawns", foreverExport(), "--forever", "--out", filepath.Join(dir, "out"), "--live", "--slot", t.TempDir())
	if code == exitLivePhaseRefused || code == exitPlanScoutLiveAborted {
		t.Fatalf("live pass: code %d err %q", code, errOut)
	}
	if fake.losCalls != 0 {
		t.Fatalf("%d los queries; want none for an unmatched target pack", fake.losCalls)
	}
	rep := foreverReport(t, dir, "scout_nomatch")
	found := false
	for _, n := range rep.Notes {
		if strings.Contains(n, "target pack unit(s) had no live unit of their entry near their spawn") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the report does not note the unmatched targets: %q", rep.Notes)
	}
}

type fakeForeverNoServerPathNav struct{ scoutNet }

func (fakeForeverNoServerPathNav) ServerPath(uint32, navmesh.Vec3, navmesh.Vec3) (navmesh.ServerPath, error) {
	return navmesh.ServerPath{}, errors.New("navmesh: ServerPath replays the 1.12 core's own query; use PathTo/PathToFilter for a Forever mmap set")
}

func TestPlanScoutApplyFixesForeverNoServerPath(t *testing.T) {
	dir := t.TempDir()
	foreverScoutPlanFile(t, dir, "scout_apply_forever", false)
	old := planScoutOpenForever
	planScoutOpenForever = func(string, map[uint32]uint32) planScoutNav { return fakeForeverNoServerPathNav{} }
	t.Cleanup(func() { planScoutOpenForever = old })

	out := filepath.Join(dir, "out")
	applyDir := filepath.Join(dir, "patched")
	code, _, errOut := runCLI("plan-scout", "scout_apply_forever", "--dir", dir, "--mmaps", dir,
		"--spawns", foreverExport(), "--forever", "--out", out, "--apply-fixes", applyDir)
	if code != exitOK {
		t.Fatalf("plan-scout --forever --apply-fixes exited %d: %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(applyDir, "scout_apply_forever.json")); err != nil {
		t.Fatalf("missing patched plan copy: %v", err)
	}
}
