package runmap

// The adapter over a synthetic watch record: a three-leg run of five bots, two bosses (one killed), a trash
// pull, a mob that resets, a mob whose position is unknown, two guids that share their low 64 bits, a death, a
// pause, a rescue and a start delay of five seconds.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/bughunt"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/mapview"
	"github.com/pjerra/forever-agent/internal/trace"
)

// The fixture's run id and the guids it uses: five bots, two plan bosses, a trash pack, two twins and a runner.
const (
	fixtureRun = "r-20261120T120000-ab12"
	guidTank   = "0x00000000000000000000000000000001"
	guidHealer = "0x00000000000000000000000000000002"
	guidDps1   = "0x00000000000000000000000000000003"
	guidDps2   = "0x00000000000000000000000000000004"
	guidDps3   = "0x00000000000000000000000000000005"
	guidBossA  = "0x0000000000000000000000000000000b"
	guidBossB  = "0x0000000000000000000000000000000c"
	guidTrash  = "0x00000000000000000000000000000064"
	guidHidden = "0x00000000000000000000000000000065"
	guidRunner = "0x00000000000000000000000000000067"
	guidTwinA  = "0x0000000000000000000000000000006e"
	guidTwinB  = "0x0000000000000001000000000000006e"
)

// The fixture's clock: the trace runs 25 s, the module's run starts 5 s into it.
const (
	fixtureStartDelay = 5
	fixtureSeconds    = 25
)

// fixtureSpec changes what a fixture record carries; the zero value is the standard record: the trace's run_start
// event, the summary's one death, and a driver duration that outlasts the module's run (a cleanup tail the clock
// offset must not count).
type fixtureSpec struct {
	noRunStart bool  // an old trace, from before the watch wrote the anchor
	wipes      []int // trace milliseconds of the watch's wipe events
	durationS  float64
	elapsedS   float64
	deathLog   string // the summary's death_log JSON
	// frameBots lists, by party index and in this order, the bots the frames carry; nil is the whole party.
	frameBots []int
	loosePuls string // the summary's loose_pulls JSON (pulls that named no step)
}

// fixture writes one watch folder: the trace and its summary, plus the plan and the combat log from testdata.
// The bots' names live in the party records only, as a watch records them.
func fixture(t *testing.T) string { return fixtureWith(t, fixtureSpec{}) }

func fixtureWith(t *testing.T, sp fixtureSpec) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"fixture_depths.json"} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("testdata/%s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTrace(t, filepath.Join(dir, fixtureRun+".jsonl"), sp)
	writeSummary(t, filepath.Join(dir, fixtureRun+".forever-run.json"), sp)
	return dir
}

// writeTrace writes the fixture's trace: one frame a second from the fifth second, each with the party and the
// hostiles the scenario needs. Boss One first stands at z=0 and dies on z=20, so its kill floor differs from its
// first-seen one.
func writeTrace(t *testing.T, path string, sp fixtureSpec) {
	t.Helper()
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	env := func(k string, t int) string {
		return fmt.Sprintf(`{"v":1,"k":"%s","run":"%s","src":"agent","t":%d`, k, fixtureRun, t)
	}
	line(`%s,"schema":"bt-trace","scope":"party","start_unix":1700000000,"interval_ms":1000,"map":36,`+
		`"instance":1,"caps":[],"source":"forever_dungeon_watch","mode":"watch","plan":"fixture_depths",`+
		`"start":"2023-11-14T22:13:20Z"}`, env("header", 0))
	party := []struct{ g, name, class, role string }{
		{guidTank, "Tanka", "warrior", "tank"},
		{guidHealer, "Healera", "priest", "heal"},
		{guidDps1, "Dpsa", "mage", "dps"},
		{guidDps2, "Dpsb", "rogue", "dps"},
		{guidDps3, "Dpsc", "hunter", "dps"},
	}
	for _, m := range party {
		line(`%s,"g":"%s","name":"%s","class":"%s","role":"%s"}`, env("party", 5000), m.g, m.name, m.class, m.role)
	}
	mobs := []struct {
		g     string
		entry int
		name  string
		boss  bool
	}{
		{guidBossA, 900, "Boss One", true},
		{guidBossB, 901, "Boss Two", true},
		{guidTrash, 100, "Trash A", false},
		{guidTwinA, 102, "Twin A", false},
		{guidTwinB, 102, "Twin B", false},
		{guidHidden, 101, "Hidden Trash", false},
		{guidRunner, 103, "Runner", false},
	}
	for _, m := range mobs {
		line(`%s,"g":"%s","entry":%d,"name":"%s","boss":%v}`, env("mobs", 5000), m.g, m.entry, m.name, m.boss)
	}
	if !sp.noRunStart {
		line(`%s,"ev":"run_start","plan":"fixture_depths"}`, env("event", fixtureStartDelay*1000))
	}
	for _, ms := range sp.wipes {
		line(`%s,"ev":"wipe","at_s":%d}`, env("event", ms), ms/1000)
	}
	bot := func(g string, x, y int) string {
		return fmt.Sprintf(`{"g":"%s","al":true,"cb":false,"st":"n","hp":100,"mhp":100,"x":%d,"y":%d,"z":0}`, g, x, y)
	}
	mob := func(g string, hp, x, y int, extra string) string {
		return fmt.Sprintf(`{"g":"%s","hp":%d,"x":%d,"y":%d,"z":0%s}`, g, hp, x, y, extra)
	}
	for t5 := fixtureStartDelay; t5 <= fixtureSeconds; t5++ {
		x := 10 + 2*(t5-fixtureStartDelay)
		all := []string{bot(guidTank, x, 0), bot(guidHealer, x, 2), bot(guidDps1, x, 4), bot(guidDps2, x, 6), bot(guidDps3, x, 8)}
		bots := all
		if sp.frameBots != nil {
			bots = []string{}
			for _, i := range sp.frameBots {
				bots = append(bots, all[i])
			}
		}
		fighting := ""
		if t5 < 14 { // the first boss fights until it dies
			fighting = `,"cb":true`
		}
		// From the tenth second the first boss stands on the upper floor and stays there to die: its kill
		// marker must be the death's floor, not the one it was first seen on.
		bx, bz := 20, 0
		if t5 >= 10 {
			bx, bz = 24, 20
		}
		list := []string{
			fmt.Sprintf(`{"g":"%s","hp":%d,"x":%d,"y":0,"z":%d%s}`,
				guidBossA, map[bool]int{true: 100, false: 0}[t5 < 14], bx, bz, fighting),
			mob(guidBossB, 100, 40, 0, ""),
			mob(guidTrash, map[bool]int{true: 100, false: 0}[t5 < 9], 12, 3, ""),
			mob(guidTwinA, 100, 30, 1, ""),
			mob(guidTwinB, 100, 30, 5, ""),
			mob(guidHidden, 100, 0, 0, `,"np":true`),
			mob(guidRunner, 100, 25, 4, map[bool]string{true: `,"cb":true`, false: ""}[t5 == 11 || t5 == 12]),
		}
		line(`%s,"via":"session","bots":[%s],"mobs":[%s]}`, env("frame", t5*1000), strings.Join(bots, ","), strings.Join(list, ","))
	}
	line(`%s,"reason":"done","seconds":%d,"ended":"2023-11-14T22:13:45Z","counters":{}}`, env("end", 0), fixtureSeconds)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeSummary writes the fixture's run summary: three legs, one pull, one death, one pause and one rescue, all
// on the module's clock, which starts fixtureStartDelay seconds into the trace. The driver's duration outlasts the
// module's run by a cleanup tail; only the trace's run_start event says where the module's clock begins.
func writeSummary(t *testing.T, path string, sp fixtureSpec) {
	t.Helper()
	duration, elapsed := 30.0, 20.0
	if sp.durationS != 0 {
		duration = sp.durationS
	}
	if sp.elapsedS != 0 {
		elapsed = sp.elapsedS
	}
	deathLog := sp.deathLog
	if deathLog == "" {
		deathLog = `[
      {"at_s": 8, "leg": 1, "step": 1, "who": "healer", "why": "Healera"}
    ]`
	}
	loose := sp.loosePuls
	if loose == "" {
		loose = "[]"
	}
	s := fmt.Sprintf(`{
  "schema": "forever-run", "version": 1, "run_id": "%s", "plan": "fixture_depths",
  "mode": "watch", "clear": true, "bosses": {"900": true, "901": false}, "wipes": 0, "deaths": 1, "practice": 1,
  "duration_s": %g, "stop_reason": "done", "last_leg": "leg_three", "instance": 1, "pulls": [],
  "watch": {
    "state": "done", "step": 3, "steps": 3, "leg": 2, "leg_name": "leg_three", "phase": "done",
    "pull": 1, "pulls": 1, "deaths": 1, "wipes": 0, "pauses": 1, "elapsed_s": %g, "rescues": 1, "events": 9,
    "bosses": {"900": "dead", "901": "alive"},
    "legs": [
      {"leg": 0, "name": "leg_one", "first_s": 0, "last_s": 6, "seconds": 6, "pulls": 1, "steps": 1},
      {"leg": 1, "name": "leg_two", "first_s": 6, "last_s": 12, "seconds": 6, "pulls": 0, "steps": 0},
      {"leg": 2, "name": "leg_three", "first_s": 12, "last_s": 20, "seconds": 8, "pulls": 0, "steps": 0}
    ],
    "step_log": [
      {"leg": 0, "step": 1, "pulls": 1, "pull_events": [{"seq": 2, "at_s": 4}], "deaths": 0}
    ],
    "death_log": %s,
    "loose_pulls": %s,
    "pause_log": [
      {"at_s": 2, "leg": 0, "step": 1, "why": "wipe", "cleared": true}
    ],
    "rescue_log": [
      {"at_s": 3, "leg": 0, "step": 1, "why": "wipe"}
    ]
  }
}
`, fixtureRun, duration, elapsed, deathLog, loose)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// marker is the first marker of a kind, with its place and label.
func marker(t *testing.T, run *mapview.Run, kind string) mapview.Marker {
	t.Helper()
	for _, m := range run.Markers {
		if m.Kind == kind {
			return m
		}
	}
	var kinds []string
	for _, m := range run.Markers {
		kinds = append(kinds, m.Kind)
	}
	t.Fatalf("no %s marker among %v", kind, kinds)
	return mapview.Marker{}
}

func TestLoadRoutesBySlot(t *testing.T) {
	run, overlay, err := Load(fixture(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Map != 36 {
		t.Fatalf("map %d, want the plan's 36", run.Map)
	}
	var names []string
	for _, tr := range run.Tracks {
		names = append(names, tr.Name)
	}
	if got, want := strings.Join(names, ","), "tank,healer,dps1,dps2,dps3"; got != want {
		t.Fatalf("tracks %q, want %q", got, want)
	}
	if !run.Tracks[0].Leader {
		t.Fatal("the tank is not the leader's route")
	}
	if overlay == nil || overlay.Name != "fixture_depths" || len(overlay.Legs) != 3 {
		t.Fatalf("overlay %+v", overlay)
	}
	if run.Seconds != fixtureSeconds {
		t.Fatalf("run is %.0f s, want the trace's %d", run.Seconds, fixtureSeconds)
	}
}

func TestLoadCreatures(t *testing.T) {
	run, _, err := Load(fixture(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The hidden trash is no place on the map; everything else is one creature.
	if len(run.Creatures) != 6 {
		t.Fatalf("%d creatures, want 6: %+v", len(run.Creatures), run.Creatures)
	}
	byEntry := map[uint32][]mapview.Creature{}
	for _, c := range run.Creatures {
		byEntry[c.Entry] = append(byEntry[c.Entry], c)
	}
	if n := len(byEntry[102]); n != 2 {
		t.Fatalf("%d creatures of entry 102, want 2 (guids sharing the low 64 bits)", n)
	}
	for entry, wantBoss := range map[uint32]bool{900: true, 901: true, 100: false, 102: false, 103: false} {
		for _, c := range byEntry[entry] {
			if c.Boss != wantBoss {
				t.Fatalf("entry %d boss=%v, want %v", entry, c.Boss, wantBoss)
			}
		}
	}
	if _, ok := byEntry[101]; ok {
		t.Fatal("the mob whose position is unknown was drawn")
	}
	for _, c := range run.Creatures {
		switch c.Entry {
		case 900:
			if c.Fate != mapview.FateKilled || c.FateT != 14 {
				t.Fatalf("boss one %s at %.0f, want killed at 14", c.Fate, c.FateT)
			}
			// The creature itself stays where it was first seen; only its kill marker moves to the death place.
			if c.X != 20 || c.Z != 0 {
				t.Fatalf("boss one at (%.0f,%.0f), want its first sight (20,0)", c.X, c.Z)
			}
		case 100:
			if c.Fate != mapview.FateKilled || c.FateT != 9 {
				t.Fatalf("trash %s at %.0f, want killed at 9", c.Fate, c.FateT)
			}
		case 103:
			if c.Fate != mapview.FateEvaded || c.FateT != 16 {
				t.Fatalf("runner %s at %.0f, want evaded at 16", c.Fate, c.FateT)
			}
		}
	}
}

func TestLoadMarkers(t *testing.T) {
	run, _, err := Load(fixture(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, m := range run.Markers {
		kinds[m.Kind]++
	}
	for kind, want := range map[string]int{mapview.MarkLeg: 3, mapview.MarkPull: 1, mapview.MarkDeath: 1,
		mapview.MarkPause: 1, mapview.MarkRescue: 1, mapview.MarkBossKill: 1, mapview.MarkWipe: 0} {
		if kinds[kind] != want {
			t.Fatalf("%d %s markers, want %d (%+v)", kinds[kind], kind, want, run.Markers)
		}
	}
	// The module's at_s counts from its run start, five seconds into the trace.
	if m := marker(t, &run, mapview.MarkPull); m.T != 4+fixtureStartDelay {
		t.Fatalf("pull at %.0f s, want %d: the start delay was not applied", m.T, 4+fixtureStartDelay)
	}
	if m := marker(t, &run, mapview.MarkPause); m.T != 2+fixtureStartDelay {
		t.Fatalf("pause at %.0f s, want %d", m.T, 2+fixtureStartDelay)
	}
	if m := marker(t, &run, mapview.MarkRescue); m.T != 3+fixtureStartDelay {
		t.Fatalf("rescue at %.0f s, want %d", m.T, 3+fixtureStartDelay)
	}
	if m := marker(t, &run, mapview.MarkBossKill); m.T != 14 || !strings.HasPrefix(m.Label, "Boss One") ||
		m.X != 24 || m.Z != 20 {
		t.Fatalf("boss kill %q at %.0f (%.0f,%.0f), want Boss One at 14 where it died (24,20)",
			m.Label, m.T, m.X, m.Z)
	}
	death := marker(t, &run, mapview.MarkDeath)
	if death.T != 8+fixtureStartDelay || death.Label != "healer" {
		t.Fatalf("death %+v, want the healer at %d", death, 8+fixtureStartDelay)
	}
	// Placed where the healer stood at that moment: x = 10 + 2 s after the start.
	if want := float32(10 + 2*(8+fixtureStartDelay-fixtureStartDelay)); death.X != want || death.Y != 2 {
		t.Fatalf("death at (%.0f,%.0f), want (%.0f,2)", death.X, death.Y, want)
	}
	if !strings.Contains(strings.Join(run.Notes, "\n"), fmt.Sprintf("started %d s into the trace", fixtureStartDelay)) {
		t.Fatalf("notes do not name the start delay: %v", run.Notes)
	}
}

func TestLoadNames(t *testing.T) {
	run, _, err := Load(fixture(t), Options{Names: true})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tr := range run.Tracks {
		names = append(names, tr.Name)
	}
	if got, want := strings.Join(names, ","), "Tanka,Healera,Dpsa,Dpsb,Dpsc"; got != want {
		t.Fatalf("tracks %q, want %q", got, want)
	}
}

func TestHTMLHoldsNoNameWithoutNames(t *testing.T) {
	run, overlay, err := Load(fixture(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	scene, err := mapview.NewScene(run, mapview.SceneOptions{Overlay: overlay})
	if err != nil {
		t.Fatal(err)
	}
	html, err := scene.HTML()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Tanka", "Healera", "Dpsa", "Dpsb", "Dpsc"} {
		if bytes.Contains(html, []byte(name)) {
			t.Fatalf("the replay holds the character name %q", name)
		}
	}
	for _, want := range []string{`"name":"tank"`, `"name":"healer"`, `"name":"dps1"`, "module paused the run", "practice rescue"} {
		if !bytes.Contains(html, []byte(want)) {
			t.Fatalf("the replay holds no %q", want)
		}
	}
}

func TestWipeStarts(t *testing.T) {
	party := func(at ...int) []driver.DeathRecord {
		who := []string{"tank", "healer", "dps1", "dps2", "dps3"}
		out := make([]driver.DeathRecord, len(at))
		for i, s := range at {
			out[i] = driver.DeathRecord{AtS: s, Who: who[i%len(who)]}
		}
		return out
	}
	if got := wipeStarts(party(40, 41, 42, 43, 44), 5); len(got) != 1 || got[0] != 40 {
		t.Fatalf("five members in five seconds: %v, want a wipe at 40", got)
	}
	if got := wipeStarts(party(10, 11, 12, 13), 5); got != nil {
		t.Fatalf("four members are no wipe: %v", got)
	}
	if got := wipeStarts(party(10, 12, 16, 20, 26), 5); got != nil {
		t.Fatalf("five members over sixteen seconds are no wipe: %v", got)
	}
	if got := wipeStarts(party(10, 12), 2); len(got) != 1 || got[0] != 10 {
		t.Fatalf("a two-member party wiped: %v", got)
	}
	// One member dying five times is five deaths, not five members.
	dup := []driver.DeathRecord{}
	for s := 40; s < 45; s++ {
		dup = append(dup, driver.DeathRecord{AtS: s, Who: "tank"})
	}
	if got := wipeStarts(dup, 5); got != nil {
		t.Fatalf("repeated deaths of one member fabricated a wipe: %v", got)
	}
	// A death the module could not attribute counts for no member.
	unattr := []driver.DeathRecord{}
	for s := 40; s < 45; s++ {
		unattr = append(unattr, driver.DeathRecord{AtS: s})
	}
	if got := wipeStarts(unattr, 5); got != nil {
		t.Fatalf("unattributed deaths fabricated a wipe: %v", got)
	}
	// A member's duplicate deaths inside a burst count once and the burst still needs every slot.
	mixed := []driver.DeathRecord{
		{AtS: 40, Who: "tank"}, {AtS: 40, Who: "tank"}, {AtS: 41, Who: "tank"},
		{AtS: 42, Who: "healer"}, {AtS: 43, Who: "dps1"}, {AtS: 44, Who: "dps2"}, {AtS: 45, Who: "dps3"},
	}
	if got := wipeStarts(mixed, 5); len(got) != 1 || got[0] != 40 {
		t.Fatalf("one burst with duplicates: %v, want a wipe at 40", got)
	}
	// Two wipes separated by more than the window are two markers.
	two := append(party(1, 2, 3, 4, 5), party(17, 18, 19, 20, 21)...)
	if got := wipeStarts(two, 5); len(got) != 2 || got[0] != 1 || got[1] != 17 {
		t.Fatalf("two wipes: %v, want 1 and 17", got)
	}
	// A second wipe that begins inside the first's window (the first completed at 5, the next burst starts at 14).
	close := append(party(1, 2, 3, 4, 5), party(14, 15, 16, 17, 18)...)
	if got := wipeStarts(close, 5); len(got) != 2 || got[0] != 1 || got[1] != 14 {
		t.Fatalf("two close wipes: %v, want 1 and 14", got)
	}
}

func TestLoadRefusesABadPlan(t *testing.T) {
	if _, _, err := Load(fixture(t), Options{PlanPath: filepath.Join("testdata", "missing.json")}); err == nil {
		t.Fatal("a plan that cannot be read was accepted")
	}
}

func TestLoadRefusesAnEmptyFolder(t *testing.T) {
	if _, _, err := Load(t.TempDir(), Options{}); err == nil {
		t.Fatal("a folder with no trace was accepted")
	}
}

func TestSVGDrawsPauseAndRescue(t *testing.T) {
	run, overlay, err := Load(fixture(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	scene, err := mapview.NewScene(run, mapview.SceneOptions{Overlay: overlay})
	if err != nil {
		t.Fatal(err)
	}
	svg := string(scene.SVG(0))
	for _, want := range []string{`class="pause"`, `class="rescue"`, `class="pull"`, `class="boss-kill"`,
		"module paused the run: 1", "practice rescue: 1"} {
		if !strings.Contains(svg, want) {
			t.Fatalf("the image holds no %q", want)
		}
	}
}

func TestRunStartOffset(t *testing.T) {
	if off, found := runStartOffset(nil); off != 0 || found {
		t.Fatalf("offset %v found %v without events", off, found)
	}
	evs := []trace.Event{
		{Ev: "pull_start", Envelope: trace.Envelope{T: 900}},
		{Ev: "run_start", Envelope: trace.Envelope{T: 7500}},
		{Ev: "wipe", Envelope: trace.Envelope{T: 9000}},
	}
	if off, found := runStartOffset(evs); !found || off != 7.5 {
		t.Fatalf("offset %v found %v, want 7.5 from the run_start event", off, found)
	}
	if get := clockNote(7.5, true); !strings.Contains(get, "started 8 s into the trace") &&
		!strings.Contains(get, "started 7 s into the trace") {
		t.Fatalf("note %q does not name the start", get)
	}
	if get := clockNote(0, false); !strings.Contains(get, "aligned at trace start") ||
		!strings.Contains(get, "run_start") {
		t.Fatalf("note %q does not say the anchor is missing", get)
	}
	if get := clockNote(0, true); get != "clock aligned at trace start" {
		t.Fatalf("note %q", get)
	}
}

// TestLoadClockFollowsTheRunStartEvent: the driver's duration outlasts the module's run by ten seconds of cleanup
// (30 s against the module's 20 s), a tail the old duration difference could not tell from the run's start delay.
// The trace's run_start event says five.
func TestLoadClockFollowsTheRunStartEvent(t *testing.T) {
	run, _, err := Load(fixture(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m := marker(t, &run, mapview.MarkPull); m.T != 4+fixtureStartDelay {
		t.Fatalf("pull at %.0f s, want %d: the cleanup tail was counted as a start delay", m.T, 4+fixtureStartDelay)
	}
	if !strings.Contains(strings.Join(run.Notes, "\n"), "started 5 s into the trace") {
		t.Fatalf("notes do not name the start delay: %v", run.Notes)
	}
}

// TestLoadClockWithoutTheAnchor: an old trace has no run_start event. The module's times are then aligned at the
// trace's zero and the note says so, rather than subtracting a duration that spans the whole watch.
func TestLoadClockWithoutTheAnchor(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{noRunStart: true}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m := marker(t, &run, mapview.MarkPull); m.T != 4 {
		t.Fatalf("pull at %.0f s, want 4: an unanchored record was moved by the duration guess", m.T)
	}
	if !strings.Contains(strings.Join(run.Notes, "\n"), "no run_start event") {
		t.Fatalf("notes do not say the anchor is missing: %v", run.Notes)
	}
}

// wipeTimes are the wipe markers' times, in the order the markers were sorted.
func wipeTimes(run *mapview.Run) []float64 {
	var out []float64
	for _, m := range run.Markers {
		if m.Kind == mapview.MarkWipe {
			out = append(out, m.T)
		}
	}
	return out
}

// partyDeaths is a death log of five distinct members at the given module seconds.
func partyDeaths(at ...int) string {
	var b strings.Builder
	b.WriteString("[")
	for i, s := range at {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"at_s":%d,"who":%q}`, s, []string{"tank", "healer", "dps1", "dps2", "dps3"}[i%5])
	}
	b.WriteString("]")
	return b.String()
}

// TestLoadWipeEvents: the watch writes one trace event per wipe the module reports; each is a marker on the trace's
// own clock, unshifted, and a run with two wipes has two.
func TestLoadWipeEvents(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{wipes: []int{10000, 20000}}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := wipeTimes(&run); len(got) != 2 || got[0] != 10 || got[1] != 20 {
		t.Fatalf("wipes %v, want 10 and 20 on the trace's clock", got)
	}
}

// TestLoadWipeEventBeatsTheDeaths: the trace's wipe event is the record; the death log is not used again to infer
// a second wipe at a shifted time.
func TestLoadWipeEventBeatsTheDeaths(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{wipes: []int{12000}, deathLog: partyDeaths(1, 2, 3, 4, 5)}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := wipeTimes(&run); len(got) != 1 || got[0] != 12 {
		t.Fatalf("wipes %v, want the trace's one at 12 and no inferred one at 6", got)
	}
}

// TestLoadWipeInferredFromDeaths: an old record has no wipe event; five distinct members down within the window
// are one wipe, moved onto the trace's clock by the run_start anchor.
func TestLoadWipeInferredFromDeaths(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{deathLog: partyDeaths(1, 2, 3, 4, 5)}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := wipeTimes(&run); len(got) != 1 || got[0] != 1+fixtureStartDelay {
		t.Fatalf("wipes %v, want one at %d", got, 1+fixtureStartDelay)
	}
}

// TestLoadWipeNeedsDistinctMembers: repeated deaths of one member and unattributed deaths never make a wipe.
func TestLoadWipeNeedsDistinctMembers(t *testing.T) {
	dup := `[{"at_s":1,"who":"tank"},{"at_s":2,"who":"tank"},{"at_s":3,"who":"tank"},` +
		`{"at_s":4,"who":"tank"},{"at_s":5,"who":"tank"}]`
	run, _, err := Load(fixtureWith(t, fixtureSpec{deathLog: dup}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := wipeTimes(&run); len(got) != 0 {
		t.Fatalf("wipes %v from one member's five deaths, want none", got)
	}
	unattr := `[{"at_s":1},{"at_s":2},{"at_s":3},{"at_s":4},{"at_s":5}]`
	run, _, err = Load(fixtureWith(t, fixtureSpec{deathLog: unattr}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := wipeTimes(&run); len(got) != 0 {
		t.Fatalf("wipes %v from unattributed deaths, want none", got)
	}
}

// TestLoadWipeInferredTwice: two separated bursts in an old record are two wipes.
func TestLoadWipeInferredTwice(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{
		deathLog: partyDeaths(1, 2, 3, 4, 5, 17, 18, 19, 20, 21),
	}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{1 + fixtureStartDelay, 17 + fixtureStartDelay}
	if got := wipeTimes(&run); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("wipes %v, want %v", got, want)
	}
}

func TestGUIDKey(t *testing.T) {
	if GUIDKey(guidTwinA) != GUIDKey("0X0000000000000000000000000000006E") {
		t.Fatal("the same guid in another case is another key")
	}
	if GUIDKey("0x00000000000000011111111122222222") == GUIDKey("0x00000000000000021111111122222222") {
		t.Fatal("two guids that share their low word share a key")
	}
}

// notesText is the run's notes in one string.
func notesText(run *mapview.Run) string { return strings.Join(run.Notes, "\n") }

// TestLoadLeaderMissingUsesANearbyMember: no frame carries the leader, so its markers cannot stand at the leader's
// place; they take the nearest known position (the first member with a route) and never the origin.
func TestLoadLeaderMissingUsesANearbyMember(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{frameBots: []int{1, 2, 3, 4}}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	pull := marker(t, &run, mapview.MarkPull)
	if pull.X != 18 || pull.Y != 2 {
		t.Fatalf("pull at (%.0f,%.0f), want the healer's (18,2), not the origin", pull.X, pull.Y)
	}
	for _, m := range run.Markers {
		if m.X == 0 && m.Y == 0 && m.Kind != mapview.MarkBossKill {
			t.Fatalf("marker %+v landed at the origin", m)
		}
	}
}

// TestLoadNoRouteDropsMarkersWithANote: frames with no bot at all give no place for any event: those markers are
// left out and the notes say how many.
func TestLoadNoRouteDropsMarkersWithANote(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{frameBots: []int{}}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range run.Markers {
		switch m.Kind {
		case mapview.MarkLeg, mapview.MarkPull, mapview.MarkDeath, mapview.MarkPause, mapview.MarkRescue:
			t.Fatalf("marker %+v drawn with no route to place it", m)
		}
	}
	if n := notesText(&run); !strings.Contains(n, "no known position") {
		t.Fatalf("notes %q do not say the markers have no position", n)
	}
}

// TestLoadReportsMarkersOutsideTheRun: an event after the trace ended (a truncated trace) is not drawn, and the
// notes say so with its kind.
func TestLoadReportsMarkersOutsideTheRun(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{deathLog: `[{"at_s":8,"who":"healer"},{"at_s":100,"who":"tank"}]`}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range run.Markers {
		if m.Kind == mapview.MarkDeath {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d death markers, want the one inside the run", n)
	}
	if notes := notesText(&run); !strings.Contains(notes, "outside the run") || !strings.Contains(notes, "1 death") {
		t.Fatalf("notes %q do not report the death outside the run", notes)
	}
}

// TestLoadPullWithoutAStep: a pull the module sent with no step is in the summary's loose pulls and is still a
// marker, numbered in time order with the others.
func TestLoadPullWithoutAStep(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{loosePuls: `[{"seq":9,"at_s":6}]`}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range run.Markers {
		if m.Kind == mapview.MarkPull {
			got = append(got, fmt.Sprintf("%s@%.0f", m.Label, m.T))
		}
	}
	if want := "pull 1@9,pull 2@11"; strings.Join(got, ",") != want {
		t.Fatalf("pulls %v, want %s", got, want)
	}
}

// TestLoadWipeNeedsTheRecordsParty: the party record names five members; only two ever appear in frames. Two
// deaths are not a wipe of a five-member party.
func TestLoadWipeNeedsTheRecordsParty(t *testing.T) {
	log := `[{"at_s":1,"who":"tank"},{"at_s":2,"who":"healer"}]`
	run, _, err := Load(fixtureWith(t, fixtureSpec{frameBots: []int{0, 1}, deathLog: log}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := wipeTimes(&run); len(got) != 0 {
		t.Fatalf("wipes %v from two deaths in a party of five", got)
	}
}

// TestLoadDpsSlotsFollowThePartyIndex: the frames list the third damage dealer last, but the party record puts
// the first one third; a death of dps1 is placed where the party's first damage dealer stood.
func TestLoadDpsSlotsFollowThePartyIndex(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{
		frameBots: []int{0, 1, 3, 4, 2},
		deathLog:  `[{"at_s":8,"who":"dps1"}]`,
	}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if d := marker(t, &run, mapview.MarkDeath); d.Y != 4 {
		t.Fatalf("dps1 died at y=%.0f, want the first damage dealer's 4", d.Y)
	}
}

// TestLoadWipeInferredBesideARecordedWipe: a recorded wipe covers the burst it belongs to; a later burst that no
// event covers is still a wipe.
func TestLoadWipeInferredBesideARecordedWipe(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{
		wipes:    []int{12000},
		deathLog: partyDeaths(1, 2, 3, 4, 5, 14, 15, 16, 17, 18),
	}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{12, 14 + fixtureStartDelay}
	if got := wipeTimes(&run); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("wipes %v, want %v: the recorded one and the uncovered burst", got, want)
	}
}

// TestLoadWindow: --from-s/--to-s keep one stretch; the run's zero is the stretch's start and the events outside
// it are reported.
func TestLoadWindow(t *testing.T) {
	run, _, err := Load(fixture(t), Options{FromS: 8, ToS: 18})
	if err != nil {
		t.Fatal(err)
	}
	if run.Seconds != 10 {
		t.Fatalf("window is %.0f s, want 10", run.Seconds)
	}
	if m := marker(t, &run, mapview.MarkPull); m.T != 4+fixtureStartDelay-8 {
		t.Fatalf("pull at %.0f s, want %d (moved to the window's zero)", m.T, 4+fixtureStartDelay-8)
	}
	if !strings.Contains(notesText(&run), "outside the run") {
		t.Fatalf("notes %q do not report the leg events before the window", notesText(&run))
	}
}

// rewriteTrace changes the fixture's trace text.
func rewriteTrace(t *testing.T, dir string, edit func(string) string) {
	t.Helper()
	path := filepath.Join(dir, fixtureRun+".jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(edit(string(b))), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPartySlotsAreUnique: a member with no role never takes the slot another member has by its role.
func TestPartySlotsAreUnique(t *testing.T) {
	dir := fixture(t)
	rewriteTrace(t, dir, func(s string) string {
		s = strings.Replace(s, `"name":"Tanka","class":"warrior","role":"tank"`, `"name":"Tanka","class":"warrior"`, 1)
		return strings.Replace(s, `"name":"Healera","class":"priest","role":"heal"`, `"name":"Healera","class":"priest","role":"tank"`, 1)
	})
	run, _, err := Load(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tr := range run.Tracks {
		if seen[tr.Name] {
			t.Fatalf("two routes are labelled %q", tr.Name)
		}
		seen[tr.Name] = true
	}
	if len(run.Tracks) != 5 {
		t.Fatalf("%d routes, want 5", len(run.Tracks))
	}
	// The healer record names the tank role explicitly: it keeps "tank"; the roleless first member does not.
	rec, err := bughunt.LoadWatch(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	p := newParty(rec, false)
	if p.slot[bughunt.CanonGUID(guidHealer)] != "tank" || p.slot[bughunt.CanonGUID(guidTank)] == "tank" {
		t.Fatalf("slots %v", p.slot)
	}
}

// TestPartyNamesStayDistinct: two members with one name keep two routes under --names.
func TestPartyNamesStayDistinct(t *testing.T) {
	dir := fixture(t)
	rewriteTrace(t, dir, func(s string) string { return strings.ReplaceAll(s, `"name":"Healera"`, `"name":"Tanka"`) })
	run, _, err := Load(dir, Options{Names: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tr := range run.Tracks {
		if seen[tr.Name] {
			t.Fatalf("two routes are labelled %q", tr.Name)
		}
		seen[tr.Name] = true
	}
}

// TestPlanNameIsNotAPath: the summary's plan value never chooses a file outside the plan folders.
func TestPlanNameIsNotAPath(t *testing.T) {
	dir := fixture(t)
	b, err := os.ReadFile(filepath.Join(dir, "fixture_depths.json"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(dir), "fixture_depths.json")
	if err := os.WriteFile(outside, b, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)
	if err := os.Remove(filepath.Join(dir, "fixture_depths.json")); err != nil {
		t.Fatal(err)
	}
	sum := filepath.Join(dir, fixtureRun+".forever-run.json")
	sb, _ := os.ReadFile(sum)
	if err := os.WriteFile(sum, []byte(strings.Replace(string(sb), `"plan": "fixture_depths"`, `"plan": "../fixture_depths"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	run, ov, err := Load(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ov != nil || !strings.Contains(strings.Join(run.Notes, "\n"), "not a plan name") {
		t.Fatalf("the plan outside the folder was read (overlay %v): %v", ov != nil, run.Notes)
	}
}

// TestMemberAtIgnoresOldSamples: a member's position older than a few seconds is not where it is.
func TestMemberAtIgnoresOldSamples(t *testing.T) {
	run := &mapview.Run{Tracks: []mapview.Track{{Name: "tank", Samples: []mapview.Sample{
		{T: 1, Pos: mapview.Pos{X: 1}}, {T: 2, Pos: mapview.Pos{X: 2}},
	}}}}
	if pos, ok := memberAt(run, "tank", 4); !ok || pos.X != 2 {
		t.Fatalf("a fresh sample was refused: %v %v", pos, ok)
	}
	if _, ok := memberAt(run, "tank", 60); ok {
		t.Fatal("a position a minute old was used")
	}
	if _, ok := memberAt(run, "tank", -60); ok {
		t.Fatal("a route's first position was used for a time long before it")
	}
}

// TestDeathOfAMemberWithoutARouteFallsBackWithANote: the leader's position stands in, and the notes say so.
func TestDeathOfAMemberWithoutARouteFallsBackWithANote(t *testing.T) {
	run, _, err := Load(fixtureWith(t, fixtureSpec{frameBots: []int{0}}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m := marker(t, &run, mapview.MarkDeath); m.Y != 0 {
		t.Fatalf("the healer's death is at y=%.0f, want the leader's 0", m.Y)
	}
	if !strings.Contains(strings.Join(run.Notes, "\n"), "stand at the leader") {
		t.Fatalf("the fallback is not in the notes: %v", run.Notes)
	}
}

// TestLoadThinsLongRecords: a record past MaxFrames is drawn from evenly spaced frames, and the notes say so.
func TestLoadThinsLongRecords(t *testing.T) {
	run, _, err := Load(fixture(t), Options{MaxFrames: 6})
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range run.Tracks {
		if len(tr.Samples) > 8 {
			t.Fatalf("%s keeps %d samples, want about 6", tr.Name, len(tr.Samples))
		}
	}
	if !strings.Contains(strings.Join(run.Notes, "\n"), "frame is drawn") {
		t.Fatalf("the thinning is not in the notes: %v", run.Notes)
	}
	full, _, _ := Load(fixture(t), Options{})
	if len(full.Tracks[0].Samples) <= 8 || strings.Contains(strings.Join(full.Notes, "\n"), "frame is drawn") {
		t.Fatal("a short record was thinned")
	}
}

// foreverPlayer is a ForeverCore player guid as the world model prints it: 0x, then the high and low halves, 16
// digits each (a player is HighGuid type 2).
func foreverPlayer(low uint64) string { return wire.GUID{High: 2 << 58, Low: low}.String() }

// TestForeverWatchRecordRoundTripsIntoRunMap: a watch record written by the driver's own frame writer, the way a
// watch on ForeverCore writes it (each member's guid as the world model prints it), draws five routes and its
// markers, and the death label attaches to the dying member by the full guid. A frame with an empty guid, which is
// what a watch wrote while nothing named the players, draws no member at all.
func TestForeverWatchRecordRoundTripsIntoRunMap(t *testing.T) {
	guids := []string{foreverPlayer(0x11), foreverPlayer(0x12), foreverPlayer(0x13), foreverPlayer(0x14), foreverPlayer(0x15)}
	write := func(guids []string) string {
		dir := fixture(t)
		// The fixture's summary and plan stay; its trace is replaced by the recorder's own.
		if err := os.Remove(filepath.Join(dir, fixtureRun+".jsonl")); err != nil {
			t.Fatal(err)
		}
		start := time.Unix(1700000000, 0)
		rec, err := driver.NewWatchRecorder(dir, fixtureRun, "fixture_depths", 36, 1, start, nil)
		if err != nil {
			t.Fatal(err)
		}
		rec.Event(start.Add(fixtureStartDelay*time.Second), "run_start", map[string]any{"plan": "fixture_depths"})
		for sec := fixtureStartDelay; sec <= fixtureSeconds; sec++ {
			var bots []driver.FrameBot
			for i, g := range guids {
				role := []string{"tank", "heal", "dps", "dps", "dps"}[i]
				bots = append(bots, driver.FrameBot{
					State: tbcmd.BotState{Name: fmt.Sprintf("Bot%d", i), HP: 100, HPMax: 100, State: "non-combat",
						X: float64(10 + 2*(sec-fixtureStartDelay)), Y: float64(2 * i), Z: 0},
					GUID: g, Role: role,
				})
			}
			rec.WatchFrame(start.Add(time.Duration(sec)*time.Second), bots, nil, func(string) string { return "" })
		}
		rec.Finish(start.Add(fixtureSeconds*time.Second), "done", "leg_three")
		rec.CloseCombat()
		// Finish rewrote the summary with the driver's own; put the fixture's back, which carries the death.
		writeSummary(t, filepath.Join(dir, fixtureRun+".forever-run.json"), fixtureSpec{})
		return dir
	}

	dir := write(guids)
	writeCombatLog(t, dir, combatRowLine(1, "spell_damage", 12, guidBossA, guids[1], 999, 55))
	run, _, err := Load(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Tracks) != 5 {
		t.Fatalf("%d route(s), want 5 (one per member)", len(run.Tracks))
	}
	if got := marker(t, &run, mapview.MarkDeath).Label; got != "healer died: spell 999 55" {
		t.Fatalf("death label %q: the combat row did not attach to the healer by guid", got)
	}
	marker(t, &run, mapview.MarkLeg)

	// The same record with every guid empty has no member to route.
	empty := write([]string{"", "", "", "", ""})
	if run, _, err := Load(empty, Options{}); err == nil && len(run.Tracks) == 5 {
		t.Fatalf("a record with empty guids drew %d routes", len(run.Tracks))
	}
}
