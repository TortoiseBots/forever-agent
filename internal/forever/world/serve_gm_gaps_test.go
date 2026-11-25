package world

// The daemon gaps of #99 over the fake serve: teleport (with the fall a navmesh puts under it), gm_evade and
// gm_respawn. The GM hands keep the fail-closed boundary: no flag, no registration, no unit; every refusal sends
// nothing, proved from the fake's own record of selections and lines.

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// gapLines is what the fake saw: the GM lines, minus the guard's own `.gm` probe, and the selections.
func gapLines(f *fakeServer) (lines []string, selections []wire.GUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.serveGMLines {
		if l != ".gm" {
			lines = append(lines, l)
		}
	}
	return lines, append([]wire.GUID(nil), f.serveSelections...)
}

// rawCount counts the packets of one client opcode the fake read on the instance socket.
func rawCount(f *fakeServer, core uint32) int {
	table := wire.NewOpcodeTable(70707, 70707)
	raw, ok := table.WireForClient(core)
	if !ok {
		return -1
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, w := range f.instWire {
		if w == raw {
			n++
		}
	}
	return n
}

func TestServeTeleportMovesTheCharacter(t *testing.T) {
	f := newFakeServer(t)
	f.serveGMScript = goScript(&[3]float32{1100, 2100, 55})
	defer f.close()
	a := startGM(t, f)
	var out ServeTeleport
	json.Unmarshal(a.mustCall(t, "teleport", map[string]any{"x": 1100, "y": 2100, "z": 55}), &out)
	if out.Sent != ".go xyz 1100.00 2100.00 55.00" || out.Map != 0 || out.X != 1100 || out.Y != 2100 || out.Z != 55 {
		t.Fatalf("teleport answered %+v", out)
	}
	if out.Fell || out.Seq < 1 {
		t.Fatalf("a teleport with no navmesh fell or was not counted: %+v", out)
	}
	// Without a navmesh the height is only as stated, and the answer says so instead of claiming ground.
	if out.Ground != "unknown" || !strings.Contains(out.GroundWhy, "no navmesh") || !out.ZUnknown {
		t.Fatalf("the unchecked ground was not reported: %+v", out)
	}
	// The session says the same through status, not only the one answer.
	var st struct {
		ZUnknown bool `json:"z_unknown"`
	}
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	if !st.ZUnknown {
		t.Fatal("status.z_unknown is false after a teleport whose ground was not checked")
	}
	lines, _ := gapLines(f)
	if len(lines) != 1 || lines[0] != out.Sent {
		t.Fatalf("the server saw %v", lines)
	}
	if rawCount(f, wire.CMSGMoveFallLand) != 0 {
		t.Fatal("a teleport without a navmesh sent a fall-land")
	}
	// require_ground turns the unknown ground into an error that still says the character was moved.
	a.mustFail(t, "teleport", map[string]any{"x": 1100, "y": 2100, "z": 55, "require_ground": true}, -32003, "was moved to the point, but the ground under it is unknown")
	// The map form names the map the line carries, and is confirmed on it.
	json.Unmarshal(a.mustCall(t, "teleport", map[string]any{"x": 1100, "y": 2100, "z": 55, "map": 0}), &out)
	if out.Sent != ".go xyz 1100.00 2100.00 55.00 0" {
		t.Fatalf("the map form sent %q", out.Sent)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeWalkPathWaitsOutTheFallItWasAskedIn: teleport answers at the fall's start, so a walk_path asked right after
// meets the mover in the air (objects.ErrAirborne). The handler waits for the landing and then walks; this drives the
// daemon's own walk_path, not the wait, so removing the wait from serveWalkPath fails it.
func TestServeWalkPathWaitsOutTheFallItWasAskedIn(t *testing.T) {
	dir := navfixture.WriteForeverDirAt(t, 0, []navfixture.Poly{navfixture.Square(1000, 2000, 1008, 2008, 50)}, nil, 1000, 2000)
	f := newFakeServer(t)
	f.serveMode = true
	tele := &[3]float32{1002, 2002, 70}
	f.serveGMScript = goScript(tele)
	defer f.close()
	a := startServeDaemonWith(t, f, &stepLog{}, dir)
	waitServeUnits(t, a)
	var out ServeTeleport
	json.Unmarshal(a.mustCall(t, "teleport", map[string]any{"x": 1002.0, "y": 2002.0, "z": 70.0}), &out)
	if !out.Fell || !out.Falling {
		t.Fatalf("teleport answered %+v, want a fall in flight", out)
	}
	var walked ServePath
	json.Unmarshal(a.mustCall(t, "walk_path", map[string]any{"x": 1004.0, "y": 2004.0, "z": 50.0}), &walked)
	if walked.Status != "complete" {
		t.Fatalf("walk_path during the fall answered %+v; want it to wait for the landing and walk", walked)
	}
	if n := rawCount(f, wire.CMSGMoveFallLand); n != 1 {
		t.Fatalf("%d fall-land packet(s), want one", n)
	}
	a.stop(t)
	f.wait(t)
}

func TestServeTeleportRefusesBadArguments(t *testing.T) {
	f := newFakeServer(t)
	f.serveGMScript = goScript(&[3]float32{1100, 2100, 55})
	defer f.close()
	a := startGM(t, f)
	a.mustFail(t, "teleport", map[string]any{}, -32003, "needs x, y and z")
	a.mustFail(t, "teleport", map[string]any{"x": 1, "y": 2}, -32003, "needs x, y and z")
	a.mustFail(t, "teleport", map[string]any{"x": 1e9, "y": 2, "z": 3}, -32003, "not a coordinate")
	a.mustFail(t, "teleport", map[string]any{"x": 1, "y": 2, "z": 3, "map": -1}, -32003, "not a map id")
	a.mustFail(t, "teleport", map[string]any{"x": 1, "y": 2, "z": 3, "map": 4294967296}, -32003, "not a map id")
	if lines, _ := gapLines(f); len(lines) != 0 {
		t.Fatalf("a refused teleport sent %v", lines)
	}
	a.stop(t)
	f.wait(t)
}

func TestServeTeleportThatNeverArrivesFails(t *testing.T) {
	old := teleportConfirmWait
	teleportConfirmWait = 600 * time.Millisecond
	defer func() { teleportConfirmWait = old }()
	f := newFakeServer(t)
	f.serveGMScript = goScript(nil)
	defer f.close()
	a := startGM(t, f)
	a.mustFail(t, "teleport", map[string]any{"x": 1100, "y": 2100, "z": 55}, -32003, "teleport is not confirmed")
	a.stop(t)
	f.wait(t)
}

// TestServeTeleportAboveTheGroundFallsAndStatusShowsIt: with a navmesh whose floor is at 50, a teleport 200 yd above
// it answers at the fall's start ("falling", with the floor and the computed flight), status reports the fall
// while it is in the air, and the fall then lands by itself on the session's own waits: one fall-land packet and the
// floor as the position. Answering after the landing (the old behaviour) set and cleared the falling state inside one
// call, so a status poll never saw it (issue #228, row 5).
func TestServeTeleportAboveTheGroundFallsAndStatusShowsIt(t *testing.T) {
	dir := navfixture.WriteForeverDirAt(t, 0, []navfixture.Poly{navfixture.Square(1000, 2000, 1008, 2008, 50)}, nil, 1000, 2000)
	f := newFakeServer(t)
	f.serveMode = true
	// A 200 yd drop flies for about 5 s, so a slow answer on a loaded runner still lands well after the call returns.
	tele := &[3]float32{1002, 2002, 250}
	f.serveGMScript = goScript(tele)
	defer f.close()
	a := startServeDaemonWith(t, f, &stepLog{}, dir)
	waitServeUnits(t, a)
	var out ServeTeleport
	json.Unmarshal(a.mustCall(t, "teleport", map[string]any{"x": 1002, "y": 2002, "z": 250}), &out)
	// The answer is the fall's start, whatever the call's own latency: waiting for the landing would answer "landed"
	// with the character on the floor.
	if !out.Fell || !out.Falling || out.Ground != "falling" {
		t.Fatalf("teleport answered %+v, want a fall in flight", out)
	}
	if math.Abs(float64(out.FallLandZ)-50) > 0.05 || math.Abs(float64(out.DropYd)-200) > 0.05 || out.FallMS <= 0 {
		t.Fatalf("the fall's own numbers: %+v", out)
	}
	var st ServeStatus
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	if !st.Falling || st.FallMS <= 0 || math.Abs(float64(st.FallLandZ)-50) > 0.05 {
		t.Fatalf("status during the fall %+v", st)
	}
	// The fall lands on the session's own waits: falling goes false, z is the floor, one fall-land went out.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		json.Unmarshal(a.mustCall(t, "status", nil), &st)
		if !st.Falling {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.Falling {
		t.Fatal("status still says falling after the flight")
	}
	if math.Abs(float64(st.Z)-50) > 0.05 {
		t.Fatalf("z after the landing %v, want the floor 50", st.Z)
	}
	if n := rawCount(f, wire.CMSGMoveFallLand); n != 1 {
		t.Fatalf("%d fall-land packet(s), want one", n)
	}
	if n := rawCount(f, wire.CMSGMoveHeartbeat); n < 4 {
		t.Fatalf("%d heartbeat(s) in a 5 s fall, want the first and the 500 ms beats", n)
	}
	// A teleport onto the floor stands, and require_ground accepts it.
	*tele = [3]float32{1004, 2004, 50}
	// The struct is reset between answers: a later answer omits the optional fields it did not set (omitempty), and
	// Unmarshal would otherwise keep the earlier answer's values.
	out = ServeTeleport{}
	json.Unmarshal(a.mustCall(t, "teleport", map[string]any{"x": 1004, "y": 2004, "z": 50, "require_ground": true}), &out)
	if out.Fell || out.Falling || out.Ground != "standing" || out.ZUnknown {
		t.Fatalf("a teleport onto the floor answered %+v", out)
	}
	var zs struct {
		ZUnknown bool `json:"z_unknown"`
	}
	json.Unmarshal(a.mustCall(t, "status", nil), &zs)
	if zs.ZUnknown {
		t.Fatal("status.z_unknown stayed set after a checked teleport")
	}
	// The raw gm path is still done at the landing, so the call after a `.go xyz` never meets a mover in the air.
	*tele = [3]float32{1003, 2003, 90}
	before := rawCount(f, wire.CMSGMoveFallLand)
	a.mustCall(t, "gm", map[string]any{"text": ".go xyz 1003 2003 90"})
	if rawCount(f, wire.CMSGMoveFallLand) != before+1 {
		t.Fatal("the raw gm teleport answered before the fall landed")
	}
	a.mustCall(t, "walk_to", map[string]any{"x": 1004, "y": 2003, "z": 50})
	a.mustCall(t, "walk_to", map[string]any{"x": 1004, "y": 2002, "z": 50})
	a.stop(t)
	f.wait(t)
}

func gmHands(t *testing.T, tune func(*fakeServer)) (*fakeServer, *serveAgent, ServeUnit, GUID) {
	t.Helper()
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSelectionEcho = true
	if tune != nil {
		tune(f)
	}
	a := startServeDaemonTuned(t, f, &stepLog{}, func(cfg *ServeConfig) { cfg.AllowGMUnits = true })
	units := waitServeUnits(t, a)
	var st struct {
		GUID GUID `json:"guid"`
	}
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	return f, a, units[0], st.GUID
}

func TestServeGMEvadeAndRespawnAreOffByDefault(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSelectionEcho = true
	f.serveCreatureDead = true
	defer f.close()
	a := startServeDaemon(t, f, &stepLog{})
	units := waitServeUnits(t, a)
	a.mustFail(t, "gm_evade", map[string]any{"guid": units[0].GUID}, rpc.CodeForbidden, "--allow-gm-units")
	a.mustFail(t, "gm_respawn", map[string]any{"guid": units[0].GUID}, rpc.CodeForbidden, "--allow-gm-units")
	if lines, sel := gapLines(f); len(lines) != 0 || len(sel) != 0 {
		t.Fatalf("a refused hand sent lines %v selections %v", lines, sel)
	}
	a.stop(t)
	f.wait(t)
}

func TestServeGMEvadeSelectsThenEvades(t *testing.T) {
	f, a, creature, self := gmHands(t, nil)
	defer f.close()
	// The flag alone opens nothing: the registration does, and a player never passes.
	a.mustFail(t, "gm_evade", map[string]any{"guid": creature.GUID}, rpc.CodeForbidden, "not registered")
	a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{creature.GUID, self}})
	a.mustFail(t, "gm_evade", map[string]any{"guid": self}, -32003, "not a creature")
	a.mustFail(t, "gm_evade", map[string]any{}, -32003, "guid or a name")
	if lines, sel := gapLines(f); len(lines) != 0 || len(sel) != 0 {
		t.Fatalf("refusals sent lines %v selections %v", lines, sel)
	}
	var out ServeCommand
	json.Unmarshal(a.mustCall(t, "gm_evade", map[string]any{"guid": creature.GUID}), &out)
	if out.Sent != ".npc evade" {
		t.Fatalf("gm_evade sent %q", out.Sent)
	}
	lines, sel := gapLines(f)
	if len(lines) != 1 || lines[0] != ".npc evade" || len(sel) != 1 || sel[0] != wire.GUID(creature.GUID) {
		t.Fatalf("the server saw lines %v selections %v", lines, sel)
	}
	f.mu.Lock()
	order := strings.Join(f.serveOrder, ",")
	f.mu.Unlock()
	if strings.LastIndex(order, "select") < 0 || strings.LastIndex(order, "select") > strings.LastIndex(order, "say") {
		t.Fatalf("the line did not follow the confirmed selection: %s", order)
	}
	a.stop(t)
	f.wait(t)
}

// gm_evade reports what the server did, not only that the line went out (#99: it used to answer ok whatever happened).
func TestServeGMEvadeReportsTheEffect(t *testing.T) {
	old := gmEvadeSettle
	gmEvadeSettle = 400 * time.Millisecond
	defer func() { gmEvadeSettle = old }()
	for _, tc := range []struct {
		name    string
		tune    func(*fakeServer)
		want    string // the effect, or "" for an error
		wantErr string
	}{
		{"left combat", func(f *fakeServer) { f.serveCreatureInCombat, f.serveEvadeClears = true, true }, "left_combat", ""},
		{"idle creature", nil, "none", ""},
		{"still in combat", func(f *fakeServer) { f.serveCreatureInCombat = true }, "", "still in combat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, a, creature, _ := gmHands(t, tc.tune)
			defer f.close()
			a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{creature.GUID}})
			if f.serveCreatureInCombat {
				// The combat flag arrives right behind the create block: wait until the client shows it.
				deadline := time.Now().Add(5 * time.Second)
				for {
					var u ServeUnit
					json.Unmarshal(a.mustCall(t, "unit", map[string]any{"guid": creature.GUID}), &u)
					if u.InCombat {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("the creature never showed in combat")
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			if tc.wantErr != "" {
				a.mustFail(t, "gm_evade", map[string]any{"guid": creature.GUID}, -32003, tc.wantErr)
			} else {
				var out ServeGMEvade
				json.Unmarshal(a.mustCall(t, "gm_evade", map[string]any{"guid": creature.GUID}), &out)
				if out.Effect != tc.want || out.Sent != ".npc evade" || out.InCombatBefore != (tc.want == "left_combat") || out.InCombatAfter {
					t.Fatalf("gm_evade answered %+v, want effect %q", out, tc.want)
				}
			}
			a.stop(t)
			f.wait(t)
		})
	}
}

func TestServeGMRespawnOnlyForADeadCreature(t *testing.T) {
	// Alive: refused before anything is sent (the bare form would respawn every dead creature around).
	f, a, creature, _ := gmHands(t, nil)
	defer f.close()
	a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{creature.GUID}})
	a.mustFail(t, "gm_respawn", map[string]any{"guid": creature.GUID}, -32003, "is alive")
	if lines, sel := gapLines(f); len(lines) != 0 || len(sel) != 0 {
		t.Fatalf("an alive respawn sent lines %v selections %v", lines, sel)
	}
	a.stop(t)
	f.wait(t)

	// Dead: select with the echo confirmed, then `.respawn`.
	f2, a2, dead, _ := gmHands(t, func(f *fakeServer) { f.serveCreatureDead = true })
	defer f2.close()
	a2.mustCall(t, "trial_units", map[string]any{"guids": []GUID{dead.GUID}})
	deadline := time.Now().Add(5 * time.Second)
	for {
		var u ServeUnit
		json.Unmarshal(a2.mustCall(t, "unit", map[string]any{"guid": dead.GUID}), &u)
		if u.Dead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the creature never showed dead")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var out ServeCommand
	json.Unmarshal(a2.mustCall(t, "gm_respawn", map[string]any{"guid": dead.GUID}), &out)
	if out.Sent != ".respawn" {
		t.Fatalf("gm_respawn sent %q", out.Sent)
	}
	lines, sel := gapLines(f2)
	if len(lines) != 1 || lines[0] != ".respawn" || len(sel) != 1 || sel[0] != wire.GUID(dead.GUID) {
		t.Fatalf("the server saw lines %v selections %v", lines, sel)
	}
	a2.stop(t)
	f2.wait(t)
}
