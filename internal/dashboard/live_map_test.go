package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The live map tests: the dashboard draws a Forever serve's own map_snapshot (the party and the hostiles on the
// Forever mesh) from a fake serve, at a bounded rate, with a clear no-serve state, while the recorded-frames path
// (the plan view and the trace digest) stays as it was.

// liveMapSVG1 is the fake serve's picture: two party members' routes and one hostile creature, at known pixels.
const liveMapSVG1 = `<svg xmlns="http://www.w3.org/2000/svg" width="400" height="300" viewBox="0 0 400 300">` +
	`<title>map 36</title>` +
	`<g class="route" data-member="Atank"><path d="M10 20L30 40"/></g>` +
	`<g class="route" data-member="Bheal"><path d="M50 60L70 80"/></g>` +
	`<g class="creatures"><circle cx="110" cy="120" r="3.2"><title>Gnoll</title></circle></g>` +
	`</svg>`

// liveMapSVG2 is the picture after the run moved: other coordinates, so "the serve came back" is visible.
const liveMapSVG2 = `<svg xmlns="http://www.w3.org/2000/svg" width="400" height="300" viewBox="0 0 400 300">` +
	`<title>map 36</title>` +
	`<g class="route" data-member="Atank"><path d="M210 220L230 240"/></g>` +
	`<g class="route" data-member="Bheal"><path d="M250 260L270 280"/></g>` +
	`<g class="creatures"><circle cx="310" cy="290" r="3.2"><title>Gnoll</title></circle></g>` +
	`</svg>`

// liveMapAnswer is the fake serve's map_snapshot result for one picture.
func liveMapAnswer(svg string) string {
	return fmt.Sprintf(`{"map":36,"floor":0,"floors":["ground"],"seconds":42.5,"members":2,"creatures":1,`+
		`"events":0,"background":true,"notes":["background drawn"],"svg":%s}`, jsonStr(svg))
}

// liveMapClock is a clock the test moves by hand: the cadence and the cards' ages are read from it.
type liveMapClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *liveMapClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *liveMapClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newLiveMapPoller watches one fake Forever slot (discovered from a settings file, as the real dashboard does) and
// returns the poller and the agent's id. The clock is the test's.
func newLiveMapPoller(t *testing.T, f *foreverFakeServe, o Options, clk *liveMapClock) (*Poller, string) {
	t.Helper()
	u, _ := parseURL(t, f)
	dir := t.TempDir()
	writeToken(t, dir, u)
	must(t, os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"rpc":"`+u+`","server":"forever"}`), 0o600))
	o.ConfigDir, o.Agents, o.Now = dir, "auto", clk.now
	return NewPoller(o), u
}

// liveMapCard is the published card of one agent, decoded from /api/state (the page's own view).
func liveMapCard(t *testing.T, p *Poller, id string) Card {
	t.Helper()
	var st struct {
		Agents []Card `json:"agents"`
	}
	must(t, json.Unmarshal(p.State(), &st))
	for _, c := range st.Agents {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no card for %s in %s", id, p.State())
	return Card{}
}

// getLiveMap fetches /api/map/<id> from the real handler.
func getLiveMap(t *testing.T, srv *httptest.Server, id string) (int, http.Header, string) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/map/" + id)
	must(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	must(t, err)
	return resp.StatusCode, resp.Header, string(b)
}

// liveMapTick is one poll with its map ask landed, then the poll that publishes the answer: the page sees a new
// picture on the poll after the serve answered (the clock is the test's, so the second poll asks nothing new).
func liveMapTick(t *testing.T, p *Poller) {
	t.Helper()
	p.Tick(context.Background())
	p.waitLiveMaps()
	p.Tick(context.Background())
}

// TestLiveMapDrawsTheServesPartyAndHostiles pins the panel's picture: the card carries the small facts of the
// serve's map_snapshot, and /api/map/<id> serves the serve's own drawing byte for byte, so the party members and
// the hostiles are where the daemon drew them (the dashboard never reprojects them).
func TestLiveMapDrawsTheServesPartyAndHostiles(t *testing.T) {
	f := newForeverFakeServe(t)
	f.setResult(t, "map_snapshot", liveMapAnswer(liveMapSVG1))
	clk := &liveMapClock{t: t0}
	p, id := newLiveMapPoller(t, f, Options{LiveMapEvery: 5 * time.Second, LiveMapTimeout: 2 * time.Second}, clk)

	liveMapTick(t, p)

	card := liveMapCard(t, p, id)
	if !card.Online {
		t.Fatalf("card offline: %+v", card)
	}
	lm := card.LiveMap
	if lm == nil || !lm.Have || lm.State != "live" {
		t.Fatalf("live_map: %+v", lm)
	}
	if lm.Map != 36 || lm.Members != 2 || lm.Creatures != 1 {
		t.Errorf("facts: map %d, %d members, %d creatures", lm.Map, lm.Members, lm.Creatures)
	}
	if lm.Seconds != 42.5 || !lm.Background || len(lm.Notes) != 1 {
		t.Errorf("run facts: %+v", lm)
	}
	if len(lm.Floors) != 1 || lm.Floors[0] != "ground" {
		t.Errorf("floors: %+v", lm.Floors)
	}

	srv := httptest.NewServer(Handler(p, "", nil))
	t.Cleanup(srv.Close)
	code, hdr, body := getLiveMap(t, srv, id)
	if code != http.StatusOK {
		t.Fatalf("GET /api/map: %d %s", code, body)
	}
	if ct := hdr.Get("Content-Type"); ct != "image/svg+xml; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if body != liveMapSVG1 {
		t.Fatalf("the picture must be the serve's own drawing:\n%s", body)
	}
	// Members and hostiles at the coordinates the daemon drew them at.
	for _, want := range []string{`data-member="Atank"`, `data-member="Bheal"`, `cx="110" cy="120"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the served picture lost %q", want)
		}
	}
}

// TestLiveMapSaysNoServeAndComesBack pins the no-serve state: a failing map_snapshot and a gone serve both say why
// (never a stale picture, never a hung poll), and the next good answer draws again.
func TestLiveMapSaysNoServeAndComesBack(t *testing.T) {
	f := newForeverFakeServe(t)
	f.setResult(t, "map_snapshot", liveMapAnswer(liveMapSVG1))
	clk := &liveMapClock{t: t0}
	p, id := newLiveMapPoller(t, f, Options{LiveMapEvery: 5 * time.Second, LiveMapTimeout: 2 * time.Second}, clk)
	srv := httptest.NewServer(Handler(p, "", nil))
	t.Cleanup(srv.Close)

	liveMapTick(t, p)
	if lm := liveMapCard(t, p, id).LiveMap; lm == nil || !lm.Have {
		t.Fatalf("first picture: %+v", lm)
	}

	// The map call fails: the serve is there (the card is online) but draws nothing.
	f.fail(t, "map_snapshot")
	clk.add(6 * time.Second)
	liveMapTick(t, p)
	card := liveMapCard(t, p, id)
	if !card.Online {
		t.Fatalf("the card went offline for a failed map call: %+v", card)
	}
	if lm := card.LiveMap; lm == nil || lm.Have || lm.State != "no-serve" || !strings.Contains(lm.Reason, "test down") {
		t.Fatalf("no-serve state: %+v", lm)
	}
	code, _, body := getLiveMap(t, srv, id)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "no live map") || !strings.Contains(body, "test down") {
		t.Fatalf("the route must refuse with the reason: %d %s", code, body)
	}

	// The serve answers again with a fresh drawing.
	f.heal(t, "map_snapshot")
	f.setResult(t, "map_snapshot", liveMapAnswer(liveMapSVG2))
	clk.add(6 * time.Second)
	liveMapTick(t, p)
	if lm := liveMapCard(t, p, id).LiveMap; lm == nil || !lm.Have || lm.State != "live" {
		t.Fatalf("after the serve came back: %+v", lm)
	}
	code, _, body = getLiveMap(t, srv, id)
	if code != http.StatusOK || body != liveMapSVG2 {
		t.Fatalf("the new picture: %d\n%s", code, body)
	}

	// The whole serve goes away: the card is offline and its live map says why, with no picture served.
	f.fail(t, "status")
	clk.add(6 * time.Second)
	liveMapTick(t, p)
	card = liveMapCard(t, p, id)
	if card.Online {
		t.Fatalf("the card of a gone serve is online: %+v", card)
	}
	if lm := card.LiveMap; lm == nil || lm.Have || lm.State != "no-serve" || lm.Reason == "" {
		t.Fatalf("gone serve state: %+v", lm)
	}
	if code, _, _ := getLiveMap(t, srv, id); code != http.StatusServiceUnavailable {
		t.Fatalf("a gone serve still serves a picture: %d", code)
	}
}

// TestLiveMapIsBoundedAndNeverBlocksThePoll pins the cadence and the decoupling: a slow map_snapshot does not delay
// a poll, asks never stack, nothing is asked inside the cadence, and a hung call ends as "no serve" at its bound.
func TestLiveMapIsBoundedAndNeverBlocksThePoll(t *testing.T) {
	f := newForeverFakeServe(t)
	f.setResult(t, "map_snapshot", liveMapAnswer(liveMapSVG1))
	f.delay(t, "map_snapshot", 3*time.Second)
	clk := &liveMapClock{t: t0}
	p, id := newLiveMapPoller(t, f, Options{LiveMapEvery: 10 * time.Second, LiveMapTimeout: 1500 * time.Millisecond}, clk)

	start := time.Now()
	p.Tick(context.Background())
	if d := time.Since(start); d > 600*time.Millisecond {
		t.Fatalf("Tick waited %s on a slow map_snapshot", d)
	}
	if card := liveMapCard(t, p, id); !card.Online || card.LiveMap == nil {
		t.Fatalf("a slow map must not cost the card: %+v", card)
	}
	// A second poll, and a later one while the ask is in flight: nothing stacks behind it.
	p.Tick(context.Background())
	clk.add(1 * time.Second)
	p.Tick(context.Background())
	if n := f.count("map_snapshot"); n != 1 {
		t.Fatalf("asks stacked: %d", n)
	}
	// The bound ends the call: the state is a clear no serve, and the poller goes on.
	p.waitLiveMaps()
	p.Tick(context.Background()) // the same clock: it publishes, and asks nothing new
	if lm := liveMapCard(t, p, id).LiveMap; lm == nil || lm.Have || lm.State != "no-serve" ||
		!strings.Contains(lm.Reason, "deadline exceeded") {
		t.Fatalf("a hung call: %+v", lm)
	}
	// Nothing is asked again inside the cadence, and the next window asks once more and draws.
	clk.add(1 * time.Second)
	liveMapTick(t, p)
	if n := f.count("map_snapshot"); n != 1 {
		t.Fatalf("asked inside the cadence: %d", n)
	}
	f.delay(t, "map_snapshot", 0)
	clk.add(10 * time.Second)
	liveMapTick(t, p)
	if n := f.count("map_snapshot"); n != 2 {
		t.Fatalf("the next window asked %d times", n-1)
	}
	if lm := liveMapCard(t, p, id).LiveMap; lm == nil || !lm.Have {
		t.Fatalf("the window after: %+v", lm)
	}
}

// TestLiveMapPanelWording is the Positions panel's picture under node: the endpoint and the version, the words, the
// no-serve line, and no live map for a turtle card or the plan view.
func TestLiveMapPanelWording(t *testing.T) { runCheck(t, "livemap_check.js", "controls.js") }

// TestLiveMapKeepsTheRecordedFramesPath pins what the live map does not change: a turtle agent is polled with its
// four methods only (never map_snapshot), its card carries no live map, and the plan view and the trace digest the
// page draws for replays still answer.
func TestLiveMapKeepsTheRecordedFramesPath(t *testing.T) {
	f := newForeverFakeServe(t)
	f.setResult(t, "map_snapshot", liveMapAnswer(liveMapSVG1))
	turtle := newFakeAgent(t, dungeonJSON("leg 0 (one): pull group 2 of 3", time.Second))
	u, _ := parseURL(t, f)
	uu, _ := url.Parse(turtle.srv.URL)
	tu := uu.Host

	dir := t.TempDir()
	plans := filepath.Join(dir, "plans")
	must(t, os.MkdirAll(plans, 0o755))
	tiny, err := os.ReadFile("testdata/tiny.json")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(plans, "tiny.json"), tiny, 0o644))
	writeToken(t, dir, u)
	writeToken(t, dir, tu)
	must(t, os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"rpc":"`+u+`","server":"forever"}`), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "settings-1.json"), []byte(`{"rpc":"`+tu+`"}`), 0o600))

	clk := &liveMapClock{t: t0}
	p := NewPoller(Options{ConfigDir: dir, Agents: "auto", PlansDirs: []string{plans}, Now: clk.now})
	p.Tick(context.Background())
	p.waitLiveMaps()
	// One turtle poll, and no map ask on it.
	turtle.mu.Lock()
	methods := append([]string(nil), turtle.methods...)
	turtle.mu.Unlock()
	if len(methods) != 4 {
		t.Errorf("a turtle poll is four calls, got %v", methods)
	}
	for _, m := range methods {
		if m == "map_snapshot" {
			t.Errorf("the turtle agent was asked for a map_snapshot")
		}
	}
	p.Tick(context.Background()) // the poll that publishes the landed picture

	srv := httptest.NewServer(Handler(p, "", nil))
	t.Cleanup(srv.Close)

	// The Forever card draws, the turtle card has no live map at all.
	if lm := liveMapCard(t, p, u).LiveMap; lm == nil || !lm.Have {
		t.Fatalf("forever card: %+v", lm)
	}
	var st struct {
		Agents []map[string]json.RawMessage `json:"agents"`
	}
	must(t, json.Unmarshal(p.State(), &st))
	if len(st.Agents) != 2 {
		t.Fatalf("two cards, got %d", len(st.Agents))
	}
	for _, a := range st.Agents {
		var id string
		must(t, json.Unmarshal(a["id"], &id))
		_, has := a["live_map"]
		if id == tu && has {
			t.Errorf("a turtle card carries a live map")
		}
		if id == u && !has {
			t.Errorf("the forever card lost its live map")
		}
	}

	// The recorded-frames path is untouched: the plan drawing answers, and the trace digest says what it always said.
	resp, err := http.Get(srv.URL + "/api/plan/tiny")
	must(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"svg"`) {
		t.Fatalf("the plan drawing: %d %s", resp.StatusCode, b)
	}
	resp2, err := http.Get(srv.URL + "/api/live/" + u)
	must(t, err)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("the trace digest of a run with no trace: %d", resp2.StatusCode)
	}
}
