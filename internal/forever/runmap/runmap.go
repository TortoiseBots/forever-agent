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
)

// Options tunes the adapter. The zero value reads the plan the summary names and labels the routes by slot.
type Options struct {
	// PlanPath is the dungeon plan to draw. Empty: the summary's plan name under dungeons/.
	PlanPath string
	// Names labels the routes with the party's names from the trace's party records instead of its slots.
	Names bool
}

// Load reads the watch folder dir (or one of its trace files) and returns the run, the plan's overlay (nil when
// no plan was found) and any note about what the record did not carry.
func Load(dir string, o Options) (mapview.Run, *mapview.Overlay, error) {
	rec, err := bughunt.LoadWatch(dir, "")
	if err != nil {
		return mapview.Run{}, nil, err
	}
	folder := filepath.Dir(rec.Path)
	stem := filepath.Join(folder, strings.TrimSuffix(filepath.Base(rec.Path), ".jsonl"))
	sum, sumNote := readSummary(stem + ".forever-run.json")
	plan, planNote, err := resolvePlan(o.PlanPath, sum, folder)
	if err != nil {
		return mapview.Run{}, nil, err
	}
	var mapID uint32
	title := rec.Name
	if plan != nil {
		mapID, title = plan.Map, plan.Name
	}
	clock := clockOffset(sum)
	p := newParty(rec, o.Names)
	// The trace's zero is the run's zero: an empty frame anchors it, so every time the adapter adds (a module
	// at_s moved by the clock offset) is already on the frames' clock.
	base := time.Unix(int64(rec.T0), 0)
	cfg := mapview.DefaultConfig()
	cfg.Map, cfg.Title = int(mapID), title
	b := mapview.NewBuilder(cfg)
	b.AddFrame(mapview.Frame{T: base, Map: mapID})
	for _, f := range rec.Frames {
		b.AddFrame(p.frame(f, base, mapID))
	}
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
	appendMarkers(&run, p, sum, clock)
	run.Notes = append(run.Notes, clockNote(clock, sum))
	if planNote != "" {
		run.Notes = append(run.Notes, planNote)
	}
	if sumNote != "" {
		run.Notes = append(run.Notes, sumNote)
	}
	return run, overlay, nil
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

// clockOffset is how far into the trace the module's run started: the module's at_s counts from its own start,
// not from the trace's zero, and a watch enters the dungeon before it starts the run.
func clockOffset(s *driver.RunSummary) float64 {
	if s == nil || s.Watch == nil {
		return 0
	}
	if d := s.DurationS - s.Watch.ElapsedS; d > 0 {
		return d
	}
	return 0
}

// clockNote says how the module's times were placed on the trace's clock.
func clockNote(offset float64, s *driver.RunSummary) string {
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
}

// newParty names the members in the order the frames list them. The module's role word places a member in its
// slot; a member without one takes the slot its place in the party gives (the leader is the tank, the second the
// healer). With names set, the route is labelled with the party record's name.
func newParty(rec *bughunt.Run, names bool) *party {
	p := &party{label: map[string]string{}, slot: map[string]string{}, bySlot: map[string]string{}}
	for _, f := range rec.Frames {
		for _, g := range f.Order {
			if _, ok := p.slot[g]; !ok {
				p.order = append(p.order, g)
				p.slot[g] = ""
			}
		}
	}
	dps := 0
	for i, g := range p.order {
		switch strings.ToLower(strings.TrimSpace(rec.Roles[g])) {
		case "tank":
			p.slot[g] = "tank"
		case "heal", "healer":
			p.slot[g] = "healer"
		case "dps", "damage", "dd":
			dps++
			p.slot[g] = fmt.Sprintf("dps%d", dps)
		default:
			switch i {
			case 0:
				p.slot[g] = "tank"
			case 1:
				p.slot[g] = "healer"
			default:
				dps++
				p.slot[g] = fmt.Sprintf("dps%d", dps)
			}
		}
		p.label[g] = p.slot[g]
		p.bySlot[p.slot[g]] = p.label[g]
		if names {
			if n := rec.Names[g]; n != "" {
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
