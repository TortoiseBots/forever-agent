// Package runmap turns one Forever watch output folder into what mapview draws: the routes of the party, the
// hostile creatures the watch saw, and the events of the module's run. It is the input adapter the map renderer
// needs, nothing more: the drawing, the floors and the HTML replay stay in mapview.
//
// The folder is the one `forever dungeon-watch --out` writes: the trace, the run summary and (optionally) the
// combat log, plus the dungeon plan that was watched. The frames carry no map id, so the plan names it; the
// module's times count from its own run start, so they are moved onto the trace's clock.
package runmap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/bughunt"
	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/mapview"
	"github.com/pjerra/forever-agent/internal/trace"
)

// Options tunes the adapter. The zero value reads the plan the summary names and labels the routes by slot.
type Options struct {
	// PlanPath is the dungeon plan to draw. Empty: the summary's plan name under dungeons/.
	PlanPath string
	// Names labels the routes with the party's names from the trace's party records instead of its slots.
	Names bool
	// FromS and ToS keep one stretch of the trace, in seconds from its start (0: no limit). The run's zero is then
	// the stretch's start, and the events outside it are reported in the notes.
	FromS, ToS float64
	// MaxFrames bounds the frames the map is built from (0: DefaultMaxFrames).
	MaxFrames int
}

// DefaultMaxFrames is the most frames a map is built from; longer records are thinned evenly.
const DefaultMaxFrames = 20000

func (o Options) maxFrames() int {
	if o.MaxFrames > 0 {
		return o.MaxFrames
	}
	return DefaultMaxFrames
}

// Loaded is a run with what a command needs to name and write it.
type Loaded struct {
	Run     mapview.Run
	Overlay *mapview.Overlay
	// ID is the watch's run id: the trace's own, else its file's stem.
	ID string
}

// Load reads the watch folder dir (or one of its trace files) and returns the run, the plan's overlay (nil when
// no plan was found) and any note about what the record did not carry.
func Load(dir string, o Options) (mapview.Run, *mapview.Overlay, error) {
	l, err := LoadRun(dir, o)
	if err != nil {
		return mapview.Run{}, nil, err
	}
	return l.Run, l.Overlay, nil
}

// LoadRun is Load with the run's id.
func LoadRun(dir string, o Options) (*Loaded, error) {
	rec, err := bughunt.LoadWatchNoCombat(dir, "")
	if err != nil {
		return nil, err
	}
	folder := filepath.Dir(rec.Path)
	stem := filepath.Join(folder, strings.TrimSuffix(filepath.Base(rec.Path), ".jsonl"))
	sum, sumNote := readSummary(stem + ".forever-run.json")
	plan, planNote, err := resolvePlan(o.PlanPath, sum, folder)
	if err != nil {
		return nil, err
	}
	var mapID uint32
	title := rec.Name
	if plan != nil {
		mapID, title = plan.Map, plan.Name
	}
	clock, clockFound := runStartOffset(rec.TraceEvents)
	p := newParty(rec, o.Names)
	// The trace's zero is the run's zero: an empty frame anchors it, so every time the adapter adds (a module
	// at_s moved by the clock offset) is already on the frames' clock.
	base := time.Unix(int64(rec.T0), 0)
	cfg := mapview.DefaultConfig()
	cfg.Map, cfg.Title = int(mapID), title
	b := mapview.NewBuilder(cfg)
	shift := 0.0
	if o.FromS > 0 {
		shift = o.FromS
	}
	b.AddFrame(mapview.Frame{T: base.Add(time.Duration(shift * float64(time.Second))), Map: mapID})
	var kept []*bughunt.Frame
	for _, f := range rec.Frames {
		if f.T < shift || (o.ToS > 0 && f.T > o.ToS) {
			continue
		}
		kept = append(kept, f)
	}
	// A very long watch is thinned to about MaxFrames evenly spaced frames (the first and last always stay), so the
	// map's own copy of the route and the creatures stays bounded.
	step := 1
	if max := o.maxFrames(); len(kept) > max {
		step = (len(kept) + max - 1) / max
	}
	for i, f := range kept {
		if step > 1 && i%step != 0 && i != len(kept)-1 {
			continue
		}
		b.AddFrame(p.frame(f, base, mapID))
	}
	thinned := step > 1
	var bosses []uint32
	var overlay *mapview.Overlay
	if plan != nil {
		bosses = mapview.BossEntries(plan)
		overlay = mapview.OverlayFromPlan(plan)
	}
	run := b.BuildWithBosses(nil, nil, bosses...)
	if rec.T0 <= 0 {
		// The trace does not know when it started: its times are still the run's, but the wall clock is not.
		run.Start = time.Time{}
	}
	appendMarkers(&run, p, rec, sum, clock, shift)
	if thinned {
		run.Notes = append(run.Notes, fmt.Sprintf("a long record: every %d. frame is drawn (%d frames kept)", step, len(kept)))
	}
	run.Notes = append(run.Notes, clockNote(clock, clockFound))
	if planNote != "" {
		run.Notes = append(run.Notes, planNote)
	}
	if sumNote != "" {
		run.Notes = append(run.Notes, sumNote)
	}
	return &Loaded{Run: run, Overlay: overlay, ID: rec.Name}, nil
}

// readSummary reads the watch's `<run_id>.forever-run.json`; nil and a note say why there is none.
func readSummary(path string) (*driver.RunSummary, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "no run summary beside the trace, so the module's events are missing"
	}
	var s driver.RunSummary
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, "the run summary is not readable: " + err.Error()
	}
	if s.Schema != "" && s.Schema != driver.SummarySchema {
		return nil, fmt.Sprintf("the file beside the trace is not a run summary (schema %q)", s.Schema)
	}
	return &s, ""
}

// resolvePlan loads the plan to draw: the one asked for, else the summary's plan name under dungeons/. No plan is
// not an error (a run can still be drawn where it was), and the note says what was looked for.
func resolvePlan(path string, s *driver.RunSummary, dir string) (*dungeon.Plan, string, error) {
	if path != "" {
		p, err := dungeon.Load(path)
		if err != nil {
			return nil, "", fmt.Errorf("the plan %q: %w", filepath.Base(path), err)
		}
		return p, "", nil
	}
	name := ""
	if s != nil {
		name = strings.TrimSpace(s.Plan)
	}
	if name == "" {
		return nil, "no plan named in the summary: no plan overlay and no map id", nil
	}
	if name == "." || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || filepath.Base(name) != name {
		return nil, fmt.Sprintf("the summary's plan %q is not a plan name (no separators or ..): no plan overlay and no map id", name), nil
	}
	for _, cand := range planCandidates(name, dir) {
		if _, err := os.Stat(cand); err != nil {
			continue
		}
		p, err := dungeon.Load(cand)
		if err != nil {
			return nil, "", fmt.Errorf("the plan %q: %w", filepath.Base(cand), err)
		}
		return p, "", nil
	}
	return nil, fmt.Sprintf("no plan file for %q: no plan overlay and no map id", name), nil
}

// planCandidates are where the summary's plan may live, in the order they are tried.
func planCandidates(name, dir string) []string {
	file := name + ".json"
	return []string{
		filepath.Join("dungeons", file),      // the working folder, as the commands read it
		filepath.Join(dir, "dungeons", file), // the watch folder's own copy
		filepath.Join(dir, file),             // beside the run's files
	}
}

// runStartOffset is how far into the trace the module's run started: the watch writes a run_start event at the
// run-start action's reply time, and the module's at_s counts from that moment. The event is the only anchor: the
// driver's own duration spans the whole watch, so subtracting the module's elapsed time would also count the time
// after the module stopped (its cleanup and the summary write) and shift every event late.
//
// A record written before the event existed has no anchor; its module times are then taken to start at the trace's
// zero and the note says so, rather than guessing from durations that are not commensurate.
func runStartOffset(evs []trace.Event) (float64, bool) {
	for _, ev := range evs {
		if ev.Ev == "run_start" && ev.T >= 0 {
			return float64(ev.T) / 1000, true
		}
	}
	return 0, false
}

// clockNote says how the module's times were placed on the trace's clock.
func clockNote(offset float64, found bool) string {
	if !found {
		return "clock aligned at trace start: the record carries no run_start event"
	}
	if offset <= 0 {
		return "clock aligned at trace start"
	}
	return fmt.Sprintf("the module's run started %.0f s into the trace; its times are moved onto it", offset)
}

// party is the run's party: its order, each member's label and slot, and the guid that walks first.
type party struct {
	order  []string
	label  map[string]string // guid text -> the route's label
	slot   map[string]string // guid text -> tank, healer or dpsN
	bySlot map[string]string // tank, healer or dpsN -> the route's label
	leader string
	size   int // the members the run record names (0: it names none)
}

// newParty names the members in the party records' order (then any the frames add). The module's role word places a member in its
// slot; a member without one takes the slot its place in the party gives (the leader is the tank, the second the
// healer). With names set, the route is labelled with the party record's name.
func newParty(rec *bughunt.Run, names bool) *party {
	p := &party{label: map[string]string{}, slot: map[string]string{}, bySlot: map[string]string{}}
	// The party index comes from the run record's party records; a member only the frames know follows them.
	for _, g := range rec.PartyOrder {
		if _, ok := p.slot[g]; !ok {
			p.order = append(p.order, g)
			p.slot[g] = ""
		}
	}
	p.size = len(p.order)
	for _, f := range rec.Frames {
		for _, g := range f.Order {
			if _, ok := p.slot[g]; !ok {
				p.order = append(p.order, g)
				p.slot[g] = ""
			}
		}
	}
	// An explicit role is kept; a member with no role (or a second one for a taken slot) never takes a slot another
	// member has explicitly. Slots are unique, so a route's label is too.
	dps := 0
	nextDps := func() string { dps++; return fmt.Sprintf("dps%d", dps) }
	taken := map[string]bool{}
	claim := func(g, slot string) {
		if slot == "tank" || slot == "healer" {
			if taken[slot] {
				slot = nextDps()
			}
		}
		taken[slot] = true
		p.slot[g] = slot
	}
	for _, g := range p.order {
		switch strings.ToLower(strings.TrimSpace(rec.Roles[g])) {
		case "tank":
			claim(g, "tank")
		case "heal", "healer":
			claim(g, "healer")
		case "dps", "damage", "dd":
			claim(g, nextDps())
		}
	}
	for i, g := range p.order {
		if p.slot[g] != "" {
			continue
		}
		switch {
		case i == 0 && !taken["tank"]:
			claim(g, "tank")
		case i == 1 && !taken["healer"]:
			claim(g, "healer")
		default:
			claim(g, nextDps())
		}
	}
	used := map[string]bool{}
	for _, g := range p.order {
		p.label[g] = p.slot[g]
		p.bySlot[p.slot[g]] = p.label[g]
		if names {
			if n := rec.Names[g]; n != "" {
				if used[n] {
					n = n + " (" + p.slot[g] + ")" // two members with one name keep two routes
				}
				used[n] = true
				p.label[g] = n
				p.bySlot[p.slot[g]] = n
			}
		}
	}
	if len(p.order) > 0 {
		p.leader = p.order[0]
	}
	return p
}

// name is a member's route label.
func (p *party) name(guid string) string {
	if l, ok := p.label[guid]; ok {
		return l
	}
	return guid
}

// frame converts one watched frame: the party as routes, the hostiles as creatures.
func (p *party) frame(f *bughunt.Frame, base time.Time, mapID uint32) mapview.Frame {
	out := mapview.Frame{T: base.Add(time.Duration(f.T * float64(time.Second))), Map: mapID}
	for _, g := range f.Order {
		u := f.Units[g]
		out.Members = append(out.Members, mapview.MemberFrame{
			Name: p.name(g), GUID: GUIDKey(g), Self: g == p.leader,
			Pos: mapview.Pos{X: float32(u.X), Y: float32(u.Y), Z: float32(u.Z)},
		})
	}
	for _, m := range f.Mobs {
		if m.NP {
			// The module does not know where it is: the frame's zeros are not a place on the map.
			continue
		}
		out.Hostiles = append(out.Hostiles, mapview.UnitFrame{
			GUID: GUIDKey(m.G), Entry: m.Entry, Name: m.Name, InCombat: m.Combat,
			Dead: m.HPPct <= 0, Pos: mapview.Pos{X: float32(m.X), Y: float32(m.Y), Z: float32(m.Z)},
		})
	}
	return out
}

// GUIDKey is the uint64 mapview keys a creature by: FNV-1a 64 of the guid's upper-case hex text. A ForeverCore
// guid is 128-bit text, and its low half alone does not tell two units apart.
func GUIDKey(guid string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for _, c := range guidText(guid) {
		h ^= uint64(c)
		h *= prime64
	}
	return h
}

// guidText is a guid in one text: no 0x, upper case, 32 digits when it is hex (CanonGUID pads it), else as given.
func guidText(guid string) string {
	return strings.TrimPrefix(strings.TrimPrefix(bughunt.CanonGUID(guid), "0x"), "0X")
}
