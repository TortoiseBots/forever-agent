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

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/mapview"
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

// fixture writes one watch folder: the trace and its summary, plus the plan and the combat log from testdata.
// The bots' names live in the party records only, as a watch records them.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"fixture_depths.json", "combatlog.jsonl"} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("testdata/%s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTrace(t, filepath.Join(dir, fixtureRun+".jsonl"))
	writeSummary(t, filepath.Join(dir, fixtureRun+".forever-run.json"))
	return dir
}

// writeTrace writes the fixture's trace: one frame a second from the fifth second, each with the party and the
// hostiles the scenario needs.
func writeTrace(t *testing.T, path string) {
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
	bot := func(g string, x, y int) string {
		return fmt.Sprintf(`{"g":"%s","al":true,"cb":false,"st":"n","hp":100,"mhp":100,"x":%d,"y":%d,"z":0}`, g, x, y)
	}
	mob := func(g string, hp, x, y int, extra string) string {
		return fmt.Sprintf(`{"g":"%s","hp":%d,"x":%d,"y":%d,"z":0%s}`, g, hp, x, y, extra)
	}
	for t5 := fixtureStartDelay; t5 <= fixtureSeconds; t5++ {
		x := 10 + 2*(t5-fixtureStartDelay)
		bots := []string{bot(guidTank, x, 0), bot(guidHealer, x, 2), bot(guidDps1, x, 4), bot(guidDps2, x, 6), bot(guidDps3, x, 8)}
		fighting := ""
		if t5 < 14 { // the first boss fights until it dies
			fighting = `,"cb":true`
		}
		list := []string{
			mob(guidBossA, map[bool]int{true: 100, false: 0}[t5 < 14], 20, 0, fighting),
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
// on the module's clock, which starts fixtureStartDelay seconds into the trace.
func writeSummary(t *testing.T, path string) {
	t.Helper()
	const s = `{
  "schema": "forever-run", "version": 1, "run_id": "` + fixtureRun + `", "plan": "fixture_depths",
  "mode": "watch", "clear": true, "bosses": {"900": true, "901": false}, "wipes": 0, "deaths": 1, "practice": 1,
  "duration_s": 25, "stop_reason": "done", "last_leg": "leg_three", "instance": 1, "pulls": [],
  "watch": {
    "state": "done", "step": 3, "steps": 3, "leg": 2, "leg_name": "leg_three", "phase": "done",
    "pull": 1, "pulls": 1, "deaths": 1, "wipes": 0, "pauses": 1, "elapsed_s": 20, "rescues": 1, "events": 9,
    "bosses": {"900": "dead", "901": "alive"},
    "legs": [
      {"leg": 0, "name": "leg_one", "first_s": 0, "last_s": 6, "seconds": 6, "pulls": 1, "steps": 1},
      {"leg": 1, "name": "leg_two", "first_s": 6, "last_s": 12, "seconds": 6, "pulls": 0, "steps": 0},
      {"leg": 2, "name": "leg_three", "first_s": 12, "last_s": 20, "seconds": 8, "pulls": 0, "steps": 0}
    ],
    "step_log": [
      {"leg": 0, "step": 1, "pulls": 1, "pull_events": [{"seq": 2, "at_s": 4}], "deaths": 0}
    ],
    "death_log": [
      {"at_s": 8, "leg": 1, "step": 1, "who": "healer", "why": "Healera"}
    ],
    "pause_log": [
      {"at_s": 2, "leg": 0, "step": 1, "why": "wipe", "cleared": true}
    ],
    "rescue_log": [
      {"at_s": 3, "leg": 0, "step": 1, "why": "wipe"}
    ]
  }
}
`
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
	if m := marker(t, &run, mapview.MarkBossKill); m.T != 14 || !strings.HasPrefix(m.Label, "Boss One") {
		t.Fatalf("boss kill %q at %.0f, want Boss One at 14", m.Label, m.T)
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

func TestWipeStart(t *testing.T) {
	deaths := func(at ...int) []driver.DeathRecord {
		var out []driver.DeathRecord
		for _, t := range at {
			out = append(out, driver.DeathRecord{AtS: t})
		}
		return out
	}
	if at, ok := wipeStart(deaths(40, 41, 42, 43, 44), 5); !ok || at != 40 {
		t.Fatalf("five deaths in five seconds: %v %v, want a wipe at 40", at, ok)
	}
	if _, ok := wipeStart(deaths(10, 11, 12, 13), 5); ok {
		t.Fatal("four deaths are no wipe")
	}
	if _, ok := wipeStart(deaths(10, 12, 15, 18, 25), 5); ok {
		t.Fatal("five deaths over fifteen seconds are no wipe")
	}
	if at, ok := wipeStart(deaths(10, 12), 2); !ok || at != 10 {
		t.Fatalf("a two-member party wiped: %v %v", at, ok)
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

func TestClockOffsetNeedsASummary(t *testing.T) {
	if got := clockOffset(nil); got != 0 {
		t.Fatalf("offset %v without a summary", got)
	}
	if got := clockOffset(&driver.RunSummary{DurationS: 100}); got != 0 {
		t.Fatalf("offset %v without a watch block", got)
	}
	if got := clockOffset(&driver.RunSummary{DurationS: 100, Watch: &driver.RunWatch{ElapsedS: 120}}); got != 0 {
		t.Fatalf("offset %v, want 0 when the module outlasts the trace", got)
	}
	if got := clockNote(0, nil); got != "clock aligned at trace start" {
		t.Fatalf("note %q", got)
	}
	if got := clockOffset(&driver.RunSummary{DurationS: 100, Watch: &driver.RunWatch{ElapsedS: 95.5}}); got != 4.5 {
		t.Fatalf("offset %v, want 4.5", got)
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
