package bughunt

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/trace"
)

// foreverSummary is the `watch` part of `<run_id>.forever-run.json`, the file a Forever watch writes: the module's
// progress and its instance report's bosses (entry -> alive or dead), which rule 3 does not count as adds.
type foreverSummary struct {
	RunID    string          `json:"run_id"`
	Instance uint32          `json:"instance"`
	Bosses   map[string]bool `json:"bosses"`
	Watch    *struct {
		State  string            `json:"state"`
		Frames int               `json:"frames"`
		Bosses map[string]string `json:"bosses"`
	} `json:"watch"`
}

// LoadWatch loads a watch recording: a folder (or a trace file) with the run's trace (`<run_id>.jsonl`), its
// summary (`<run_id>.forever-run.json`) and its combat log (`<run_id>.combatlog.jsonl`). A folder that holds several
// runs takes the last run id, which is the newest one. The summary and the combat log are optional; a run without
// them still loads (its rules that need the combat log stay silent).
func LoadWatch(path, ds string) (*Run, error) {
	tracePath, err := watchTracePath(path)
	if err != nil {
		return nil, err
	}
	tf, err := trace.Read(tracePath)
	if err != nil {
		return nil, err
	}
	r := &Run{
		Kind:        "watch",
		Name:        tf.Header.Run,
		DS:          ds,
		Path:        tracePath,
		T0:          float64(tf.Header.StartUnix),
		Roles:       map[string]string{},
		Classes:     map[string]string{},
		Subjects:    map[string]bool{},
		BossEntries: map[uint32]string{},
		CastTimes:   map[string][]float64{},
	}
	if r.Name == "" {
		name := filepath.Base(tracePath)
		r.Name = strings.TrimSuffix(name, ".jsonl")
	}
	// A recording with no party rows has no known members: the roster fills them (UseRoster), and until then no
	// member is a subject (a frame's guid alone says nothing about what it is).
	for _, m := range tf.Party {
		g := CanonGUID(m.G)
		if g == "" {
			continue
		}
		if m.Role != "" {
			r.Roles[g] = m.Role
		}
		if m.Class != "" {
			r.Classes[g] = m.Class
		}
		r.Subjects[g] = true
	}
	for i := range tf.Frames {
		r.Frames = append(r.Frames, watchFrame(&tf.Frames[i], tf.Mobs))
	}
	simple := filepath.Base(tracePath)
	simple = strings.TrimSuffix(simple, ".jsonl")
	dir := filepath.Dir(tracePath)
	if sum := readForeverSummary(filepath.Join(dir, simple+".forever-run.json")); sum != nil {
		r.bossesFrom(sum)
	}
	rows, err := readCombatLog(filepath.Join(dir, simple+".combatlog.jsonl"))
	if err != nil {
		return nil, err
	}
	r.Combat = stampCombatRows(rows, r.T0)
	r.deathsFromFrames()
	r.finalise()
	return r, nil
}

// watchTracePath resolves a watch folder (or a trace file) to its run's trace.
func watchTracePath(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return path, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".jsonl") || strings.HasSuffix(n, ".combatlog.jsonl") {
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("%s: no watch trace (<run>.jsonl)", path)
	}
	sort.Strings(names)
	return filepath.Join(path, names[len(names)-1]), nil
}

// watchFrame is one trace frame in the bug hunter's model.
func watchFrame(f *trace.Frame, registry map[string]trace.MobInfo) *Frame {
	out := &Frame{T: float64(f.T) / 1000, Units: map[string]*Unit{}}
	for _, b := range f.Bots {
		g := CanonGUID(b.G)
		if g == "" {
			continue
		}
		u := &Unit{
			G:         g,
			Alive:     b.Alive,
			Combat:    b.Combat,
			State:     b.State,
			HP:        float64(b.HP),
			MaxHP:     float64(b.MaxHP),
			Power:     float64(b.Power),
			MaxPower:  float64(b.MaxPow),
			PType:     int(b.PType),
			X:         float64(b.X),
			Y:         float64(b.Y),
			Z:         float64(b.Z),
			Moving:    b.Moving,
			HasMoving: true,
			Target:    b.Target,
			Casting:   b.Cast != 0,
			Spell:     b.Cast,
			Act:       b.Action,
			Res:       b.Res,
			Lead:      b.Lead,
			Auras:     append([]uint32(nil), b.Auras...),
			Eat:       b.Eat,
			Drink:     b.Drink,
			Rest:      b.Rest,
			HasMeal:   true,
			Life:      b.Life,
			Move:      b.Move,
			Stuck:     b.Stuck,
		}
		if b.DL != nil {
			d := float64(*b.DL)
			u.DL = &d
		}
		out.addUnit(g, u)
	}
	for _, m := range f.Mobs {
		g := CanonGUID(m.G)
		if g == "" {
			continue
		}
		mob := &Mob{
			G:      g,
			HPPct:  float64(m.HPPct),
			X:      float64(m.X),
			Y:      float64(m.Y),
			Z:      float64(m.Z),
			Combat: m.Combat,
			Target: m.Target,
			Victim: m.Victim,
			CC:     m.CC,
			Flee:   m.Flee,
			NP:     m.NP,
		}
		info, ok := registry[m.G]
		if !ok {
			info, ok = registry[g]
		}
		if ok {
			mob.Entry, mob.Name, mob.Boss = info.Entry, info.Name, info.Boss
		} else {
			mob.Entry = entryOf(g)
		}
		for _, t := range m.Top {
			mob.Top = append(mob.Top, TopEntry{G: CanonGUID(t.G), Threat: t.Threat})
		}
		out.Mobs = append(out.Mobs, mob)
	}
	out.GC = false
	for _, u := range out.Units {
		if u.Combat {
			out.GC = true
			break
		}
	}
	if !out.GC {
		for _, m := range out.Mobs {
			if m.Combat {
				out.GC = true
				break
			}
		}
	}
	return out
}

// readForeverSummary reads a watch's summary; nil when it is missing or not JSON.
func readForeverSummary(path string) *foreverSummary {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s foreverSummary
	if err := json.Unmarshal(b, &s); err != nil {
		return nil
	}
	return &s
}

// bossesFrom records the bosses the instance report listed. The watch's own state map (entry -> alive/dead) is the
// full list once it has been read; the summary's dead-set is the fallback for an older file.
func (r *Run) bossesFrom(s *foreverSummary) {
	if s.Watch != nil {
		for k, v := range s.Watch.Bosses {
			if entry, err := strconv.ParseUint(k, 10, 32); err == nil {
				r.BossEntries[uint32(entry)] = v
			}
		}
	}
	for k, dead := range s.Bosses {
		entry, err := strconv.ParseUint(k, 10, 32)
		if err != nil {
			continue
		}
		if _, ok := r.BossEntries[uint32(entry)]; !ok {
			if dead {
				r.BossEntries[uint32(entry)] = "dead"
			} else {
				r.BossEntries[uint32(entry)] = "alive"
			}
		}
	}
}

// deathsFromFrames dates each member's death at the first frame that shows it not alive after one that showed it
// alive. A member first seen dead has no dated death and is not one.
func (r *Run) deathsFromFrames() {
	alive := map[string]bool{}
	for _, f := range r.Frames {
		for _, g := range f.Order {
			u := f.Units[g]
			if u.Alive {
				alive[g] = true
				continue
			}
			if alive[g] {
				r.Deaths = append(r.Deaths, Death{T: f.T, Bot: g})
				delete(alive, g)
			}
		}
	}
}

// readCombatLog reads a combat log: a watch's `<run_id>.combatlog.jsonl`, or a trials repeat's `raw-ta.jsonl`, which
// carries the same row. A missing file is no rows (the rules that need the log are skipped and counted). The log is
// a required input of the rules that read it, so a line that is not a valid row is an error naming the file and the
// line, as the other loaders do: a dropped line would read as a member doing nothing.
func readCombatLog(path string) ([]CombatRow, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []CombatRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row CombatRow
		if !strings.HasPrefix(line, "{") {
			return nil, fmt.Errorf("%s:%d: not a combat row: %.40q", path, n, line)
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("%s:%d: not a combat row: %v", path, n, err)
		}
		out = append(out, row)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s:%d: %v", path, n+1, err)
	}
	return out, nil
}

// stampCombatRows puts every row on the run's clock and canonicalises both sides' guids, so a row joins a frame
// directly. A row whose time cannot be read, or a run that does not know its start, leaves TKnown false: the row
// stays in the log but no rule places it in time.
func stampCombatRows(rows []CombatRow, t0 float64) []CombatRow {
	for i := range rows {
		row := &rows[i]
		row.Source.GUID = CanonGUID(row.Source.GUID)
		row.Target.GUID = CanonGUID(row.Target.GUID)
		if t0 <= 0 || row.Time == "" {
			continue
		}
		if sec, ok := parseCombatTime(row.Time); ok {
			row.T = sec - t0
			row.TKnown = true
		}
	}
	return rows
}

// combatTimeLayout is the session's row time: the wall clock with milliseconds and its offset.
const combatTimeLayout = "2006-01-02T15:04:05.000-07:00"

// parseCombatTime reads a combat row's `time` as epoch seconds.
func parseCombatTime(s string) (float64, bool) {
	for _, layout := range []string{combatTimeLayout, time.RFC3339Nano, "2006-01-02T15:04:05-07:00"} {
		if dt, err := time.Parse(layout, s); err == nil {
			return float64(dt.Unix()) + float64(dt.Nanosecond())/1e9, true
		}
	}
	return 0, false
}
