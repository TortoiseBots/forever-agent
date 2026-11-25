package dashboard

import (
	"context"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// The live map: a Forever serve slot's own picture of the run (map_snapshot: the party members and the hostile
// creatures on the Forever mesh, drawn by the daemon from its recorder over the navmesh). The poller asks for it at
// a bounded rate on a goroutine of its own, so a slow or gone serve never delays a poll or a page request; the card
// carries the small facts and the SVG is served on its own route (/api/map/<id>), which keeps /api/state small.
//
// The recorded frames are still what the plan view and the layers draw (the run's trace, replays included): the
// live map is the one picture the dashboard cannot build on its own, because only the daemon sees the mesh.

const (
	// liveMapEveryDefault is how often one slot's map is asked for.
	liveMapEveryDefault = 5 * time.Second
	// liveMapEveryMin is the floor: an ask costs the daemon a fresh frame and a scene build, so no setting may go
	// under it (the tests move the poller's clock instead of lowering it).
	liveMapEveryMin = 2 * time.Second
	// liveMapTimeoutDefault bounds one call: a serve that hangs reports "no serve" instead of holding the slot.
	liveMapTimeoutDefault = 10 * time.Second
)

// LiveMapInfo is what a card says about its slot's live map: the facts the page shows next to the picture (the
// picture itself is /api/map/<id>). State is "live" (Have is set), "waiting" (the serve answered that it has
// nothing to draw yet: its character is not in the world) or "no-serve" (the call failed; Reason says why).
type LiveMapInfo struct {
	Have       bool      `json:"have"`
	State      string    `json:"state"`
	Map        uint32    `json:"map"` // never omitted: Eastern Kingdoms is map 0
	Floor      int       `json:"floor"`
	Floors     []string  `json:"floors,omitempty"`
	Members    int       `json:"members,omitempty"`
	Creatures  int       `json:"creatures,omitempty"`
	Seconds    float64   `json:"seconds,omitempty"`
	Background bool      `json:"background,omitempty"`
	Notes      []string  `json:"notes,omitempty"`
	At         time.Time `json:"at,omitempty"`     // when this picture was taken (the page's version of it)
	AgeMS      int64     `json:"age_ms,omitempty"` // how old the picture is at the poll that built the card
	Reason     string    `json:"reason,omitempty"` // why there is no picture: the serve's own words, or its failure
}

// liveMapEntry is one slot's last answer and the bookkeeping that keeps the asks bounded.
type liveMapEntry struct {
	info    LiveMapInfo
	svg     []byte
	busy    bool      // an ask is in flight: a second one never stacks behind it
	lastTry time.Time // when the last ask started (the cadence is measured from here, not from the answer)
	gen     uint64    // bumped when the serve goes away: an ask that started before it must not land
}

// liveMapEvery is the cadence in force: the option, never under the floor.
func (p *Poller) liveMapEvery() time.Duration {
	d := p.opt.LiveMapEvery
	if d <= 0 {
		return liveMapEveryDefault
	}
	if d < liveMapEveryMin {
		return liveMapEveryMin
	}
	return d
}

// liveMapTimeout bounds one ask.
func (p *Poller) liveMapTimeout() time.Duration {
	if p.opt.LiveMapTimeout <= 0 {
		return liveMapTimeoutDefault
	}
	return p.opt.LiveMapTimeout
}

// refreshLiveMap starts one map_snapshot ask for a Forever slot when the last ask is older than the cadence and no
// ask is in flight. The ask runs on its own goroutine, bounded by ctx and liveMapTimeout, so the poll and every page
// request go on: the card shows the last answer until a new one lands.
func (p *Poller) refreshLiveMap(ctx context.Context, a Agent, cl *serveclient.Client, now time.Time) {
	p.mu.Lock()
	if p.liveMaps == nil {
		p.liveMaps = map[string]*liveMapEntry{}
	}
	e := p.liveMaps[a.ID]
	if e == nil {
		e = &liveMapEntry{}
		p.liveMaps[a.ID] = e
	}
	if e.busy || (!e.lastTry.IsZero() && now.Sub(e.lastTry) < p.liveMapEvery()) {
		p.mu.Unlock()
		return
	}
	e.busy, e.lastTry = true, now
	gen := e.gen
	p.liveAsked.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.liveAsked.Done()
		fctx, cancel := context.WithTimeout(ctx, p.liveMapTimeout())
		defer cancel()
		m, err := cl.MapSnapshot(fctx, serveclient.MapParams{})
		p.finishLiveMap(a.ID, cl.Token(), gen, m, err)
	}()
}

// finishLiveMap keeps one answer: the picture and its facts, or the clear reason there is none. A failed call drops
// the old picture: a stale drawing pinned under a live run misleads more than its absence does.
func (p *Poller) finishLiveMap(id, token string, gen uint64, m serveclient.MapSnapshot, err error) {
	info := LiveMapInfo{}
	var svg []byte
	switch {
	case err == nil && strings.TrimSpace(m.SVG) != "":
		info.Have, info.State = true, "live"
		info.Map, info.Floor = m.Map, m.Floor
		info.Floors = append([]string(nil), m.Floors...)
		info.Members, info.Creatures = m.Members, m.Creatures
		info.Seconds = round1(m.Seconds)
		info.Background, info.Notes = m.Background, append([]string(nil), m.Notes...)
		info.At = p.opt.Now()
		svg = []byte(m.SVG)
	case err == nil:
		info.State, info.Reason = "waiting", "the serve drew an empty map"
	default:
		msg := scrubText(token, err.Error())
		info.Reason = msg
		if code, ok := serveclient.CodeOf(err); ok && code == rpc.CodeGame &&
			!strings.Contains(msg, "not ported on this server") {
			// The daemon's own "nothing to draw yet": its character is not in the world. Not a gone serve. A
			// daemon without map_snapshot ported is a gone picture instead: its -32003 names the method.
			info.State = "waiting"
		} else {
			info.State = "no-serve"
		}
	}
	p.mu.Lock()
	if p.liveMaps == nil {
		p.liveMaps = map[string]*liveMapEntry{}
	}
	e := p.liveMaps[id]
	if e == nil {
		e = &liveMapEntry{}
		p.liveMaps[id] = e
	}
	if e.gen != gen {
		// The serve went away, or its slot was reset, while this ask ran: the answer is older than the state.
		e.busy = false
		p.mu.Unlock()
		return
	}
	e.info, e.svg, e.busy = info, svg, false
	p.mu.Unlock()
}

// markLiveMapGone says the serve answers nothing at all (its status call failed): the card shows a clear no-serve
// state and the route stops serving the last picture.
func (p *Poller) markLiveMapGone(id, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.liveMaps == nil {
		p.liveMaps = map[string]*liveMapEntry{}
	}
	e := p.liveMaps[id]
	if e == nil {
		e = &liveMapEntry{}
		p.liveMaps[id] = e
	}
	e.gen++ // an ask in flight started before this: its answer must not overwrite the gone state
	e.info = LiveMapInfo{State: "no-serve", Reason: reason}
	e.svg = nil
}

// liveMapInfo is the card's copy of the slot's live map (nil-free: a card always carries the current state).
func (p *Poller) liveMapInfo(id string, now time.Time) *LiveMapInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e := p.liveMaps[id]
	if e == nil || e.info.State == "" {
		return &LiveMapInfo{State: "waiting", Reason: "the dashboard is asking the serve for its first map"}
	}
	info := e.info
	if info.Have && !info.At.IsZero() {
		if age := now.Sub(info.At); age > 0 {
			info.AgeMS = age.Milliseconds()
		}
	}
	return &info
}

// LiveMap is the slot's last picture and its facts: the SVG the /api/map route serves, or an empty one with the
// reason there is none.
func (p *Poller) LiveMap(id string) (LiveMapInfo, []byte) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if e := p.liveMaps[id]; e != nil {
		return e.info, e.svg
	}
	return LiveMapInfo{State: "waiting", Reason: "the dashboard has not asked the serve for its map yet"}, nil
}

// waitLiveMaps waits for the map asks in flight to land (the tests use it; nothing in the page waits on a picture).
func (p *Poller) waitLiveMaps() { p.liveAsked.Wait() }

// liveMapWhy is the route's one-line reason there is no picture, from the state the card carries.
func liveMapWhy(info LiveMapInfo) string {
	if info.Reason != "" {
		return info.Reason
	}
	if info.State == "waiting" {
		return "the serve has nothing to draw yet"
	}
	return "no live map yet"
}

// scrubText takes a token out of a short piece of text the card shows (Scrub works on JSON only).
func scrubText(token, text string) string {
	if token == "" || !strings.Contains(text, token) {
		return text
	}
	return strings.ReplaceAll(text, token, "<redacted>")
}
