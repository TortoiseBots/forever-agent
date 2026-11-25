package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"errors"

	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

// fakeScene is a navmesh stand-in: it answers the polygons registered for a tile and counts the calls.
type fakeScene struct {
	tiles     map[[2]int][]navmesh.Polygon
	calls     int
	err       error
	truncated bool
}

func (f *fakeScene) Polygons(_ uint32, x0, y0, x1, y1 float32) ([]navmesh.Polygon, bool, error) {
	f.calls++
	if f.err != nil {
		return nil, false, f.err
	}
	var out []navmesh.Polygon
	for at, ps := range f.tiles {
		tx0, ty0, tx1, ty1 := tileExtent(at[0], at[1])
		if tx1 < x0 || tx0 > x1 || ty1 < y0 || ty0 > y1 {
			continue
		}
		out = append(out, ps...)
	}
	return out, f.truncated, nil
}

func squarePoly(x, y, z, side float32) navmesh.Polygon {
	return navmesh.Polygon{Flags: navmesh.NavGround, Verts: []navmesh.Vec3{
		{x, y, z}, {x + side, y, z}, {x + side, y + side, z}, {x, y + side, z},
	}}
}

// view3DPoller is a poller with the 3D view's hooks set the way the command sets them.
func view3DPoller(t *testing.T) (*Poller, *fakeScene) {
	t.Helper()
	p := NewPoller(Options{TestsDir: t.TempDir(), ConfigDir: t.TempDir()})
	src := &fakeScene{tiles: map[[2]int][]navmesh.Polygon{
		{30, 30}: {squarePoly(600, 600, 10, 40)},
	}}
	p.SetSceneNavmesh(src, "forever-v16")
	return p, src
}

// mapIs says whether an answer's map is the wanted one: a nil map is "unset", and map 0 is a real map.
func mapIs(m *uint32, want uint32) bool { return m != nil && *m == want }

func getJSON(t *testing.T, h http.Handler, url string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Host = "127.0.0.1:8790"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestView3DSceneTilesAndCache(t *testing.T) {
	p, src := view3DPoller(t)
	h := Handler(p, "", nil)
	url := "/api/view3d/scene?map=389&tiles=30:30,31:30"
	code, body := getJSON(t, h, url)
	if code != http.StatusOK {
		t.Fatalf("%d: %s", code, body)
	}
	var sc view3DScene
	if err := json.Unmarshal(body, &sc); err != nil {
		t.Fatal(err)
	}
	if sc.Map != 389 || sc.Format != "forever-v16" || len(sc.Tiles) != 2 || sc.Triangles != 2 || sc.Truncated {
		t.Fatalf("%+v", sc)
	}
	near := sc.Tiles[0]
	if near.Missing || near.Triangles != 2 || len(near.Vertices) != 12 || len(near.Indices) != 6 {
		t.Fatalf("tile %d:%d: %+v", near.X, near.Y, near)
	}
	if near.MinZ != 10 || near.MaxZ != 10 {
		t.Fatalf("heights %g..%g", near.MinZ, near.MaxZ)
	}
	if !sc.Tiles[1].Missing || sc.Tiles[1].Triangles != 0 {
		t.Fatalf("the empty tile is not missing: %+v", sc.Tiles[1])
	}
	first := src.calls
	code, again := getJSON(t, h, "/api/view3d/scene?map=389&tiles=31:30,30:30") // another order, one area
	if code != http.StatusOK || string(again) != string(body) {
		t.Fatalf("the cache answered %d with different bytes:\n%s\n%s", code, again, body)
	}
	if src.calls != first {
		t.Fatalf("the cached answer read the mesh again: %d calls, then %d", first, src.calls)
	}
}

func TestView3DSceneRejectsBounds(t *testing.T) {
	p, _ := view3DPoller(t)
	h := Handler(p, "", nil)
	cases := []string{
		"/api/view3d/scene?map=x&tiles=1:1",
		"/api/view3d/scene?map=10000&tiles=1:1",
		"/api/view3d/scene?map=1",
		"/api/view3d/scene?map=1&tiles=64:0",
		"/api/view3d/scene?map=1&tiles=1",
		"/api/view3d/scene?map=1&tiles=" + sceneTilesParam(View3DMaxTiles+1),
	}
	for _, u := range cases {
		if code, body := getJSON(t, h, u); code != http.StatusBadRequest {
			t.Errorf("%s: %d (want 400): %s", u, code, body)
		}
	}
}

func sceneTilesParam(n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d:%d", i/tileGrid, i%tileGrid)
	}
	return b.String()
}

func TestView3DSceneNeedsANavmesh(t *testing.T) {
	p := NewPoller(Options{TestsDir: t.TempDir(), ConfigDir: t.TempDir()})
	h := Handler(p, "", nil)
	code, body := getJSON(t, h, "/api/view3d/scene?map=1&tiles=1:1")
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "--mmaps-dir") {
		t.Fatalf("%d: %s", code, body)
	}
}

func TestView3DSceneSaysWhenTheMeshCutItsTiles(t *testing.T) {
	p := NewPoller(Options{TestsDir: t.TempDir(), ConfigDir: t.TempDir()})
	p.SetSceneNavmesh(&fakeScene{tiles: map[[2]int][]navmesh.Polygon{{30, 30}: {squarePoly(600, 600, 1, 4)}}, truncated: true}, "x")
	h := Handler(p, "", nil)
	code, body := getJSON(t, h, "/api/view3d/scene?map=1&tiles=30:30")
	if code != http.StatusOK {
		t.Fatalf("%d: %s", code, body)
	}
	var sc view3DScene
	if err := json.Unmarshal(body, &sc); err != nil {
		t.Fatal(err)
	}
	if !sc.Truncated {
		t.Fatalf("the mesh cut its tiles and the answer does not say so: %+v", sc)
	}
}

// blockingScene holds its first answer until the test opens the gate, so a second request for the same tiles
// arrives while the first build runs. It must wait for that one build, not read the mesh again.
type blockingScene struct {
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

func (b *blockingScene) Polygons(_ uint32, _, _, _, _ float32) ([]navmesh.Polygon, bool, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	b.once.Do(func() {
		close(b.entered)
		<-b.gate
	})
	return []navmesh.Polygon{squarePoly(600, 600, 1, 40)}, false, nil
}

func TestView3DSceneSharesOneBuild(t *testing.T) {
	src := &blockingScene{entered: make(chan struct{}), gate: make(chan struct{})}
	svc := newView3DSceneService(src, "x")
	type res struct {
		body []byte
		err  string
	}
	first := make(chan res, 1)
	go func() { b, e := svc.scene(1, [][2]int{{31, 31}}); first <- res{b, e} }()
	<-src.entered
	second := make(chan res, 1)
	go func() { b, e := svc.scene(1, [][2]int{{31, 31}}); second <- res{b, e} }()
	time.Sleep(20 * time.Millisecond) // let the second request join the build in flight
	close(src.gate)
	a, b := <-first, <-second
	if a.err != "" || b.err != "" || len(a.body) == 0 || string(a.body) != string(b.body) {
		t.Fatalf("two answers: %q %q", a.err, b.err)
	}
	src.mu.Lock()
	calls := src.calls
	src.mu.Unlock()
	if calls != 1 {
		t.Fatalf("%d mesh reads for one tile set, want 1 (the two requests share the build)", calls)
	}
	// And the cache answers a third request without reading the mesh at all.
	if body, errText := svc.scene(1, [][2]int{{31, 31}}); errText != "" || string(body) != string(a.body) {
		t.Fatalf("after the build: %q", errText)
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if src.calls != 1 {
		t.Fatalf("the cache read the mesh again: %d calls", src.calls)
	}
}

// The scene on a real navmesh (a corridor made by navfixture): the polygons of the tile come back as triangles,
// and a tile without a file is reported as missing.
func TestView3DSceneOnARealNavmesh(t *testing.T) {
	dir := navfixture.WriteDir(t, 209, navfixture.LCorridor(0))
	planner := navmesh.NewPlanner(dir)
	defer planner.Close()
	if _, _, err := planner.Polygons(209, 10, 10, 12, 12); errors.Is(err, navmesh.ErrNoEngine) {
		t.Skip("built without the navmesh engine")
	}
	p := NewPoller(Options{TestsDir: t.TempDir(), ConfigDir: t.TempDir()})
	p.SetSceneNavmesh(planner, "mangos-v6")
	h := Handler(p, "", nil)
	code, body := getJSON(t, h, "/api/view3d/scene?map=209&tiles=31:31,31:32")
	if code != http.StatusOK {
		t.Fatalf("%d: %s", code, body)
	}
	var sc view3DScene
	if err := json.Unmarshal(body, &sc); err != nil {
		t.Fatal(err)
	}
	if sc.Triangles == 0 || sc.Tiles[0].Missing || len(sc.Tiles[0].Indices) != sc.Tiles[0].Triangles*3 {
		t.Fatalf("the corridor tile: %+v", sc.Tiles[0])
	}
	if !sc.Tiles[1].Missing {
		t.Fatalf("the neighbour without a tile file is not missing: %+v", sc.Tiles[1])
	}
	tile := sc.Tiles[0]
	if len(tile.Vertices)%3 != 0 {
		t.Fatalf("%d vertex floats: %+v", len(tile.Vertices), tile)
	}
	for _, i := range tile.Indices {
		if i < 0 || i >= len(tile.Vertices)/3 {
			t.Fatalf("index %d outside %d vertices", i, len(tile.Vertices)/3)
		}
	}
}

// writeRecord writes one record folder with a small trace: a party of one, one hostile, a chat line and an end.
func writeRecord(t *testing.T, root, name string) string {
	t.Helper()
	return writeRecordAt(t, root, name, 1700000000, 389)
}

// writeRecordOnMap is writeRecord with another map id: map 0 is a real map.
func writeRecordOnMap(t *testing.T, root, name string, mapID uint32) string {
	t.Helper()
	return writeRecordAt(t, root, name, 1700000000, mapID)
}

// writeRecordAt writes the same trace with a chosen start time and map id.
func writeRecordAt(t *testing.T, root, name string, startUnix int64, mapID uint32) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		fmt.Sprintf(`{"v":1,"k":"header","run":%q,"src":"agent","t":0,"schema":"bt-trace","scope":"party","start_unix":%d,"map":%d,"caps":["facing"]}`, name, startUnix, mapID),
		`{"v":1,"k":"party","run":"` + name + `","src":"agent","t":0,"g":"0x01","name":"Tank","lead":true,"class":"warrior","role":"tank"}`,
		`{"v":1,"k":"party","run":"` + name + `","src":"agent","t":0,"g":"0x02","name":"Healer","class":"priest"}`,
		`{"v":1,"k":"mobs","run":"` + name + `","src":"agent","t":0,"g":"0xAA","name":"Scorpid","entry":1,"level":3}`,
		`{"v":1,"k":"frame","run":"` + name + `","src":"agent","t":0,"via":"session","bots":[{"g":"0x01","al":true,"st":"n","hp":100,"mhp":100,"pw":10,"mpw":100,"x":-18,"y":-46,"z":-21,"tg":"0xAA","lead":true},{"g":"0x02","al":true,"st":"n","hp":80,"mhp":100,"x":-17,"y":-45,"z":-21}],"mobs":[{"g":"0xAA","hp":50,"x":-16,"y":-44,"z":-21,"cb":true,"tg":"0x01"}]}`,
		`{"v":1,"k":"frame","run":"` + name + `","src":"agent","t":1000,"via":"session","bots":[{"g":"0x01","al":true,"st":"n","hp":90,"mhp":100,"x":-10,"y":-40,"z":-20,"tg":"0xAA","lead":true}],"mobs":[{"g":"0xAA","hp":10,"x":-9,"y":-39,"z":-20,"cb":true}]}`,
		`{"v":1,"k":"event","run":"` + name + `","src":"agent","t":500,"ev":"chat","text":"pull now"}`,
		`{"v":1,"k":"event","run":"` + name + `","src":"agent","t":600,"ev":"pull","text":"pulling the scorpids"}`,
		`{"v":1,"k":"end","run":"` + name + `","src":"agent","t":1000,"seconds":12.5}`,
	}
	path := filepath.Join(dir, name+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestView3DRecordsAndReplay(t *testing.T) {
	root := t.TempDir()
	writeRecord(t, root, "run-a")
	writeRecord(t, root, "run-b")
	if err := os.MkdirAll(filepath.Join(root, "not-a-record"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(Options{TestsDir: t.TempDir(), ConfigDir: t.TempDir(), RecordsDir: root})
	h := Handler(p, "", nil)

	code, body := getJSON(t, h, "/api/view3d/records")
	if code != http.StatusOK {
		t.Fatalf("%d: %s", code, body)
	}
	var list struct {
		Records []view3DRecord `json:"records"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Records) != 2 {
		t.Fatalf("records: %+v", list.Records)
	}
	names := map[string]view3DRecord{}
	for _, r := range list.Records {
		names[r.Name] = r
	}
	if r := names["run-a"]; r.Map != 389 || r.Run != "run-a" || r.Start == "" || r.Bytes == 0 {
		t.Fatalf("run-a: %+v", r)
	}

	code, body = getJSON(t, h, "/api/view3d/replay/run-a")
	if code != http.StatusOK {
		t.Fatalf("%d: %s", code, body)
	}
	var rep view3DAnswer
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Mode != "replay" || !mapIs(rep.Map, 389) || rep.Duration != 12.5 || len(rep.Frames) != 2 {
		t.Fatalf("%+v", rep)
	}
	u := rep.Frames[0].Units
	if len(u) != 3 || u[0].Kind != "agent" || u[0].Name != "Tank" || u[0].HP != 100 || u[0].MaxPower != 100 || u[0].Target != "0xAA" {
		t.Fatalf("agent: %+v", u)
	}
	if u[1].Kind != "party" || u[1].Name != "Healer" {
		t.Fatalf("party: %+v", u[1])
	}
	if u[2].Kind != "hostile" || u[2].Name != "Scorpid" || u[2].MaxHP != 100 {
		t.Fatalf("hostile: %+v", u[2])
	}
	if rep.Frames[1].T != 1 || len(rep.Chat) != 1 || rep.Chat[0].Text != "pull now" || len(rep.Events) != 1 {
		t.Fatalf("last frame or lines: %+v chat %+v events %+v", rep.Frames[1], rep.Chat, rep.Events)
	}
}

func TestView3DReplayStaysInsideTheRecordsFolder(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "records")
	outside := filepath.Join(parent, "outside")
	writeRecord(t, outside, "secret")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("no symlinks: %v", err)
	}
	// A real folder whose trace is a symlink out: neither the picker nor the replay may follow it.
	if err := os.MkdirAll(filepath.Join(root, "run-link"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret", "secret.jsonl"), filepath.Join(root, "run-link", "run-link.jsonl")); err != nil {
		t.Skipf("no symlinks: %v", err)
	}
	p := NewPoller(Options{TestsDir: t.TempDir(), ConfigDir: t.TempDir(), RecordsDir: root})
	h := Handler(p, "", nil)
	for _, u := range []string{
		"/api/view3d/replay/../outside/secret",
		"/api/view3d/replay/..%2Foutside%2Fsecret",
		"/api/view3d/replay/link/secret",
		"/api/view3d/replay/.hidden",
		"/api/view3d/replay/",
	} {
		if code, body := getJSON(t, h, u); code == http.StatusOK {
			t.Errorf("%s was served: %s", u, body)
		}
	}
	// A record name that is a directory of the folder but holds no trace is refused.
	if code, _ := getJSON(t, h, "/api/view3d/replay/nothere"); code != http.StatusNotFound {
		t.Errorf("a missing record answered %d", code)
	}
	if code, body := getJSON(t, h, "/api/view3d/replay/run-link"); code == http.StatusOK || strings.Contains(string(body), "Tank") {
		t.Fatalf("a trace symlink out of the folder was read: %d %s", code, body)
	}
	if code, body := getJSON(t, h, "/api/view3d/records"); code != http.StatusOK || strings.Contains(string(body), "run-link") {
		t.Fatalf("the picker listed the symlinked record: %d %s", code, body)
	}
}

func TestView3DLiveFrame(t *testing.T) {
	p := NewPoller(Options{TestsDir: t.TempDir(), ConfigDir: t.TempDir()})
	const id = "127.0.0.1:8774"
	f := newLiveFrameCache(time.Now(), 389, []serveclient.Unit{
		{GUID: "0x01", Type: "player", Name: "Tank", X: -18, Y: -46, Z: -21, O: 1.5, Health: 5000, MaxHealth: 5000, Power: 10, MaxPower: 100},
		{GUID: "0x02", Type: "player", Name: "Healer", X: -17, Y: -45, Z: -21, Health: 4000, MaxHealth: 4000, Target: "0xAA"},
		{GUID: "0xAA", Type: "creature", Name: "Scorpid", X: -16, Y: -44, Z: -21, Health: 800, MaxHealth: 1000, Casting: true, SpellID: 42},
	}, serveclient.Party{Members: []serveclient.PartyMember{{Name: "Tank", GUID: "0x01"}, {Name: "Healer", GUID: "0x02"}}}, true,
		serveclient.CommanderResult{Card: &serveclient.CommanderCard{Members: []serveclient.CommanderMember{
			{Bot: "Tank", GUID: "0x01", Class: "warrior", Lead: true},
			{Bot: "Healer", GUID: "0x02", Class: "priest"},
		}}}, true)
	p.setLiveFrame(id, f)
	p.mu.Lock()
	p.foreverFeed[id] = &foreverFeed{list: []Event{{Kind: "chat", Text: "hello"}, {Kind: "death", Text: "a bot died"}}}
	p.mu.Unlock()
	h := Handler(p, "", nil)

	code, body := getJSON(t, h, "/api/view3d/live?id="+id)
	if code != http.StatusOK {
		t.Fatalf("%d: %s", code, body)
	}
	var ans view3DAnswer
	if err := json.Unmarshal(body, &ans); err != nil {
		t.Fatal(err)
	}
	if ans.State != "live" || !mapIs(ans.Map, 389) || len(ans.Units) != 3 {
		t.Fatalf("%+v", ans)
	}
	if ans.Units[0].Kind != "agent" || ans.Units[0].Class != "warrior" || ans.Units[0].MaxHP != 5000 {
		t.Fatalf("agent: %+v", ans.Units[0])
	}
	if ans.Units[1].Kind != "party" || ans.Units[1].Target != "0xAA" {
		t.Fatalf("party: %+v", ans.Units[1])
	}
	if ans.Units[2].Kind != "hostile" || ans.Units[2].Cast != "spell 42" {
		t.Fatalf("hostile: %+v", ans.Units[2])
	}
	if len(ans.Chat) != 1 || ans.Chat[0].Text != "hello" || len(ans.Events) != 1 {
		t.Fatalf("lines: %+v", ans)
	}

	// A serve that stopped answering is no serve, never a stale scene.
	p.setLiveFrame(id, &liveFrameCache{at: time.Now().Add(-view3DLiveStale - time.Second), mapID: 389})
	if code, body := getJSON(t, h, "/api/view3d/live?id="+id); code != http.StatusServiceUnavailable || !strings.Contains(string(body), "no-serve") {
		t.Fatalf("stale: %d %s", code, body)
	}
	if code, body := getJSON(t, h, "/api/view3d/live"); code != http.StatusServiceUnavailable || !strings.Contains(string(body), "no-serve") {
		t.Fatalf("stale without id: %d %s", code, body)
	}
	if code, _ := getJSON(t, h, "/api/view3d/live?id=/x"); code != http.StatusNotFound {
		t.Fatalf("a bad id answered %d", code)
	}
}

func TestView3DNothingLive(t *testing.T) {
	p := NewPoller(Options{TestsDir: t.TempDir(), ConfigDir: t.TempDir()})
	h := Handler(p, "", nil)
	code, body := getJSON(t, h, "/api/view3d/live")
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "no-serve") {
		t.Fatalf("%d: %s", code, body)
	}
}

func TestView3DMapZero(t *testing.T) {
	p, _ := view3DPoller(t)
	h := Handler(p, "", nil)

	// A scene on map 0: the answer names map 0, which is not "unset".
	code, body := getJSON(t, h, "/api/view3d/scene?map=0&tiles=30:30")
	if code != http.StatusOK || !strings.Contains(string(body), `"map":0`) {
		t.Fatalf("scene on map 0: %d %s", code, body)
	}

	// A live frame on map 0 carries it too.
	const id = "127.0.0.1:8774"
	p.setLiveFrame(id, newLiveFrameCache(time.Now(), 0, nil, serveclient.Party{}, false, serveclient.CommanderResult{}, false))
	code, body = getJSON(t, h, "/api/view3d/live?id="+id)
	if code != http.StatusOK || !strings.Contains(string(body), `"map":0`) {
		t.Fatalf("live on map 0: %d %s", code, body)
	}

	// No serve is no map, never map 0: the page keeps the scene it already has.
	p.dropLiveFrame(id)
	if _, body := getJSON(t, h, "/api/view3d/live?id="+id); strings.Contains(string(body), `"map"`) {
		t.Fatalf("a no-serve answer carries a map: %s", body)
	}

	// A replay whose header names map 0.
	root := t.TempDir()
	writeRecordOnMap(t, root, "run-zero", 0)
	p2 := NewPoller(Options{TestsDir: t.TempDir(), ConfigDir: t.TempDir(), RecordsDir: root})
	h2 := Handler(p2, "", nil)
	code, body = getJSON(t, h2, "/api/view3d/replay/run-zero")
	if code != http.StatusOK || !strings.Contains(string(body), `"map":0`) {
		t.Fatalf("replay on map 0: %d %s", code, body)
	}
	var rep view3DAnswer
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatal(err)
	}
	if !mapIs(rep.Map, 0) {
		t.Fatalf("replay map: %v", rep.Map)
	}
}

func TestView3DReplayReadsAreLimitedAndCached(t *testing.T) {
	s := newView3DReplayService()
	var (
		mu    sync.Mutex
		at    int
		peak  int
		loads int
	)
	load := func(key string) func() ([]byte, string) {
		return func() ([]byte, string) {
			mu.Lock()
			at++
			if at > peak {
				peak = at
			}
			loads++
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			at--
			mu.Unlock()
			return []byte(`{"key":"` + key + `"}`), ""
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		key := fmt.Sprintf("k%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b, errText := s.read(key, load(key)); b == nil || errText != "" {
				t.Errorf("%s: %q %q", key, b, errText)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	gotPeak, gotLoads := peak, loads
	mu.Unlock()
	if gotPeak != view3DReplayReads {
		t.Fatalf("%d replay reads ran at once, want %d", gotPeak, view3DReplayReads)
	}
	if gotLoads != 6 {
		t.Fatalf("%d loads for 6 keys", gotLoads)
	}

	// One key runs through load once however many ask at once; the rest read the cached answer.
	wg.Add(8)
	for i := 0; i < 8; i++ {
		go func() {
			defer wg.Done()
			if _, errText := s.read("shared", load("shared")); errText != "" {
				t.Errorf("shared: %q", errText)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	after := loads
	mu.Unlock()
	if after != gotLoads+1 {
		t.Fatalf("the shared key was loaded %d times, want once", after-gotLoads)
	}
}

func TestView3DRecordsKeepTheNewestBeforeTheCut(t *testing.T) {
	root := t.TempDir()
	// The newest records sort last by folder name: a cut before the sort would drop them.
	for i := 0; i <= view3DRecordsMax; i++ {
		writeRecordAt(t, root, fmt.Sprintf("rec-%03d", i), 1700000000+int64(i), 389)
	}
	p := NewPoller(Options{TestsDir: t.TempDir(), ConfigDir: t.TempDir(), RecordsDir: root})
	h := Handler(p, "", nil)
	code, body := getJSON(t, h, "/api/view3d/records")
	if code != http.StatusOK {
		t.Fatalf("%d: %s", code, body)
	}
	var list struct {
		Records []view3DRecord `json:"records"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Records) != view3DRecordsMax {
		t.Fatalf("%d records, want %d", len(list.Records), view3DRecordsMax)
	}
	if got, want := list.Records[0].Name, fmt.Sprintf("rec-%03d", view3DRecordsMax); got != want {
		t.Fatalf("newest record is %q, want %q", got, want)
	}
	if got, want := list.Records[len(list.Records)-1].Name, "rec-001"; got != want {
		t.Fatalf("oldest listed record is %q, want %q", got, want)
	}
}

func TestTileGridMath(t *testing.T) {
	x0, y0, x1, y1 := tileExtent(30, 31)
	if r, c := tileOf((x0+x1)/2, (y0+y1)/2); r != 30 || c != 31 {
		t.Fatalf("the middle of tile 30:31 is %d:%d", r, c)
	}
	if r, c := tileOf(x0+1, y0+1); r != 30 || c != 31 {
		t.Fatalf("the near corner is %d:%d", r, c)
	}
	if r, c := tileOf(x1-1, y1-1); r != 30 || c != 31 {
		t.Fatalf("the far corner is %d:%d", r, c)
	}
}
