package main

// TestScenarioForeverMove* (round 2 of #98): the four movement scenarios in
// scenarios/ run through `scenario run --slot NAME=DIR` against two fake serve
// slots. The fake is the K3 sample's (scenario_forever_sample_test.go) with the
// movement methods added: `status` carries a real map and position, `path_to`
// and `walk_to` move the character, `area_trigger` jumps it to the trigger's
// map (moving a grouped peer into an instance) and writes the serve's
// `map_change` session event, and `units` answers one patrol creature whose x
// moves 5 yd per second of wall clock. The sample's own methods are replayed to
// its handler unchanged, so both files can share one fake.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// moveStartX,Y,Z is the human start the Forever navmesh tests use (map 0).
	moveStartX, moveStartY, moveStartZ = -8913.0, -118.0, 3.4
	// moveTargetX is 20 yd east of the start: the walk scenario's target.
	moveTargetX = -8893.0
	// patrolBaseX is the patrol creature's x at the fake's start; patrolStepX
	// yd per second, out and back over patrolSpan yd.
	patrolBaseX = 100.0
	patrolStepX = 5.0
	patrolSpan  = 100.0
	// The trigger ids the fake's table answers; the scenarios carry them.
	triggerMapChange = 4711
	triggerRagefire  = 4712
)

// patrolGUID is the patrol creature's GUID: 0x plus 32 hex digits, as the
// serve prints every GUID.
const patrolGUID = "0x00000000000000000000000000000047"

// moveTrigger is one row of the fake's area-trigger table: where the trigger
// sends the character, and whether the server takes the whole group with it
// (an instance entrance) or only the character (a map change).
type moveTrigger struct {
	mapID uint32
	pos   [3]float32
	group bool
}

// fakeMoveTriggers is the table the scenarios name ids from. Ragefire's
// coordinates are the entrance's, the map change's are a boat's deck: only
// their presence matters, the fake answers whatever it is asked.
var fakeMoveTriggers = map[int32]moveTrigger{
	triggerMapChange: {mapID: 1, pos: [3]float32{-600, -4200, 40}},
	triggerRagefire:  {mapID: 389, pos: [3]float32{3.5, -14.5, -20}, group: true},
}

// fakeForeverMove is the sample fake plus movement: map and position, the
// trigger table, the patrol clock and the peer a grouped instance entry takes
// along.
type fakeForeverMove struct {
	*fakeForeverAgent
	peer     *fakeForeverMove
	mu       sync.Mutex
	mapID    uint32
	pos      [3]float32
	patrolAt time.Time
	reads    []float64 // the x every units read answered, oldest first
}

// movePair builds A and B on the same map, linked to each other and to the
// sample's party pair so invite and accept_invite still work.
func movePair(t *testing.T) (*fakeForeverMove, *fakeForeverMove) {
	t.Helper()
	pair := &fakeForeverAgents{}
	at := time.Now()
	a := &fakeForeverMove{
		fakeForeverAgent: &fakeForeverAgent{t: t, tok: "tok-" + strings.Repeat("a", 40), char: "Atank", pair: pair},
		mapID:            0,
		pos:              [3]float32{moveStartX, moveStartY, moveStartZ},
		patrolAt:         at,
	}
	b := &fakeForeverMove{
		fakeForeverAgent: &fakeForeverAgent{t: t, tok: "tok-" + strings.Repeat("b", 40), char: "Bhealer", pair: pair},
		mapID:            0,
		pos:              [3]float32{moveStartX + 2, moveStartY + 2, moveStartZ},
		patrolAt:         at,
	}
	a.peer, b.peer = b, a
	pair.a, pair.b = a.fakeForeverAgent, b.fakeForeverAgent
	a.fakeForeverAgent.other, b.fakeForeverAgent.other = b.fakeForeverAgent, a.fakeForeverAgent
	return a, b
}

// handler answers the movement methods and replays every other method to the
// sample's handler, body intact, so the party, chat and event_log behaviour
// is exactly the sample's.
func (f *fakeForeverMove) handler() http.Handler {
	inner := f.fakeForeverAgent.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.tok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "status", "path_to", "walk_to", "area_trigger", "units":
			f.moveCall(w, req.Method, req.Params)
		default:
			r2 := r.Clone(r.Context())
			r2.Body = io.NopCloser(bytes.NewReader(body))
			inner.ServeHTTP(w, r2)
		}
	})
}

// moveCall answers one movement method the way internal/forever/world's serve
// does: the field names are ServeStatus's, ServePath's, ServeUnit's and
// serveWalkParams'/serveAreaTriggerParams' json tags.
func (f *fakeForeverMove) moveCall(w http.ResponseWriter, method string, params json.RawMessage) {
	enc := func(v any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": v})
	}
	var walk struct {
		X float32 `json:"x"`
		Y float32 `json:"y"`
		Z float32 `json:"z"`
	}
	_ = json.Unmarshal(params, &walk)
	switch method {
	case "status":
		mapID, pos := f.at()
		enc(map[string]any{"character": f.char, "state": "online", "map": mapID,
			"x": pos[0], "y": pos[1], "z": pos[2], "guid": "0x" + strings.Repeat("0", 16) + "00000000000000A1",
			"last_seq": f.lastSeq()})
	case "path_to":
		mapID, from := f.at()
		to := [3]float32{walk.X, walk.Y, walk.Z}
		dx, dy, dz := float64(to[0]-from[0]), float64(to[1]-from[1]), float64(to[2]-from[2])
		enc(map[string]any{"map": mapID, "from": from, "to": to, "status": "complete",
			"points": [][3]float32{from, to}, "corners": 1,
			"length": float32(math.Sqrt(dx*dx + dy*dy + dz*dz))})
	case "walk_to":
		f.mu.Lock()
		f.pos = [3]float32{walk.X, walk.Y, walk.Z}
		f.mu.Unlock()
		enc(map[string]any{})
	case "area_trigger":
		var p struct {
			ID int32 `json:"id"`
		}
		_ = json.Unmarshal(params, &p)
		dst, ok := fakeMoveTriggers[p.ID]
		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1,
				"error": map[string]any{"code": -32602, "message": "no such area trigger"}})
			return
		}
		f.enter(dst)
		enc(map[string]any{})
	case "units":
		var p struct {
			Within float32 `json:"within"`
		}
		_ = json.Unmarshal(params, &p)
		x := f.patrolX()
		f.mu.Lock()
		f.reads = append(f.reads, x)
		f.mu.Unlock()
		enc([]map[string]any{{"guid": patrolGUID, "type": "npc", "entry": 4726, "name": "Ragefire Trogg",
			"x": x, "y": moveStartY, "z": moveStartZ, "distance": p.Within, "distance_2d": p.Within,
			"health": 120, "max_health": 120, "level": 4, "moving": true}})
	}
}

// at reads the character's map and position.
func (f *fakeForeverMove) at() (uint32, [3]float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mapID, f.pos
}

// lastSeq reads the event log's seq under its own lock.
func (f *fakeForeverMove) lastSeq() uint64 {
	f.fakeForeverAgent.mu.Lock()
	defer f.fakeForeverAgent.mu.Unlock()
	return f.seq
}

// enter moves this session through the trigger; an instance entrance takes a
// grouped peer along, as the server moves the instance group with its leader.
func (f *fakeForeverMove) enter(dst moveTrigger) {
	from := f.moveTo(dst.mapID, dst.pos)
	f.mapChangeEvent(from, dst.mapID)
	if !dst.group || f.peer == nil || !f.grouped() {
		return
	}
	peer := f.peer
	peerFrom := peer.moveTo(dst.mapID, dst.pos)
	peer.mapChangeEvent(peerFrom, dst.mapID)
}

// moveTo sets the map and position and answers the map it left.
func (f *fakeForeverMove) moveTo(mapID uint32, pos [3]float32) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	from := f.mapID
	f.mapID, f.pos = mapID, pos
	return from
}

// grouped reads the sample fake's party flag under its lock.
func (f *fakeForeverMove) grouped() bool {
	f.fakeForeverAgent.mu.Lock()
	defer f.fakeForeverAgent.mu.Unlock()
	return f.inGroup
}

// mapChangeEvent writes the session event the serve writes: kind session,
// state map_change (the text carries it so a scenario's `expect` can name it).
func (f *fakeForeverMove) mapChangeEvent(from, to uint32) {
	f.fakeForeverAgent.mu.Lock()
	f.add("session", "", "map_change", map[string]any{"state": "map_change", "from": from, "to": to})
	f.fakeForeverAgent.mu.Unlock()
}

// patrolX is the patrol creature's x: patrolStepX yd per second of wall
// clock, out and back over patrolSpan yd, one position held per second so a
// 1 s check_rpc poll sees every one.
func (f *fakeForeverMove) patrolX() float64 {
	secs := int(time.Since(f.patrolAt) / time.Second)
	d := math.Mod(patrolStepX*float64(secs), 2*patrolSpan)
	if d > patrolSpan {
		d = 2*patrolSpan - d
	}
	return patrolBaseX + d
}

// patrolReads copies the x each units read answered.
func (f *fakeForeverMove) patrolReads() []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]float64(nil), f.reads...)
}

// runMoveScenario runs one shipped scenario file against the pair and fails
// unless every check passed.
func runMoveScenario(t *testing.T, a, b *fakeForeverMove, file string) {
	t.Helper()
	srvA := httptest.NewServer(a.handler())
	defer srvA.Close()
	srvB := httptest.NewServer(b.handler())
	defer srvB.Close()
	dirA := scenarioServeSlot(t, srvA.Listener.Addr().(*net.TCPAddr).Port, a.tok)
	dirB := scenarioServeSlot(t, srvB.Listener.Addr().(*net.TCPAddr).Port, b.tok)
	sample, err := filepath.Abs(filepath.Join("..", "..", "scenarios", file))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := scenarioCmd(context.Background(), []string{"run", "--out", out,
		"--slot", "A=" + dirA, "--slot", "B=" + dirB, sample}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("scenario run %s: exit %d stderr %q stdout %q", file, code, stderr.String(), stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(out, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Exit   int `json:"exit"`
		Passed int `json:"passed"`
		Failed int `json:"failed"`
		Errors int `json:"errors"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Exit != 0 || res.Failed != 0 || res.Errors != 0 || res.Passed == 0 {
		t.Fatalf("%s: result %+v (%s)", file, res, raw)
	}
}

// TestScenarioForeverWalkPath: the walk scenario's path_to answer is a
// complete path of at least two points and walk_to lands on the target.
func TestScenarioForeverWalkPath(t *testing.T) {
	a, b := movePair(t)
	runMoveScenario(t, a, b, "forever-walk-path.json")
	if _, pos := a.at(); pos[0] != moveTargetX {
		t.Fatalf("A ended at x %v, want %v (walk_to did not move the character)", pos[0], moveTargetX)
	}
}

// TestScenarioForeverMapChange: the trigger carries A to another map and
// status shows it.
func TestScenarioForeverMapChange(t *testing.T) {
	a, b := movePair(t)
	runMoveScenario(t, a, b, "forever-map-change.json")
	if mapID, _ := a.at(); mapID != 1 {
		t.Fatalf("A is on map %d, want 1 (area_trigger left the map unchanged)", mapID)
	}
}

// TestScenarioForeverInstanceEntry: A and B group, A takes the Ragefire
// trigger, and both read map 389 with a map_change event in their logs.
func TestScenarioForeverInstanceEntry(t *testing.T) {
	a, b := movePair(t)
	runMoveScenario(t, a, b, "forever-instance-entry.json")
	if mapID, _ := a.at(); mapID != 389 {
		t.Fatalf("A is on map %d, want 389", mapID)
	}
	if mapID, _ := b.at(); mapID != 389 {
		t.Fatalf("B is on map %d, want 389 (the instance entry did not take the group)", mapID)
	}
}

// TestScenarioForeverPatrolRead: two units reads 3 s apart pin two different
// positions of the patrol creature.
func TestScenarioForeverPatrolRead(t *testing.T) {
	a, b := movePair(t)
	runMoveScenario(t, a, b, "forever-patrol-read.json")
	reads := a.patrolReads()
	differ := false
	for _, x := range reads[1:] {
		if x != reads[0] {
			differ = true
			break
		}
	}
	if len(reads) < 2 || !differ {
		t.Fatalf("patrol reads %v: want two different positions (the creature stood still)", reads)
	}
}
