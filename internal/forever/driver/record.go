package driver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/trace"
)

// The run's output: the bt-trace JSONL the run-analysis tools read (events by name: pull_start, pull_end, rest,
// death, wipe), and one small JSON beside it with the run's per-pull numbers. The summary is rewritten after every pull,
// so a run that loses its link still leaves a readable file.
const (
	SummarySchema  = "forever-run"
	SummaryVersion = 1
	TraceSource    = "forever_dungeon_run"
	// TraceSourceWatch is the watch's source word: the same summary schema, filled from the module's own counters.
	TraceSourceWatch = "forever_dungeon_watch"
)

// PullRecord is one pull's numbers.
type PullRecord struct {
	Leg              string         `json:"leg"`
	Group            int            `json:"group"`
	Entries          []uint32       `json:"entries"`
	Boss             bool           `json:"boss"`
	Start            float64        `json:"start"`
	Seconds          float64        `json:"seconds"`
	Mobs             int            `json:"mobs"`
	Adds             int            `json:"adds"`
	Lost             []uint32       `json:"lost,omitempty"`
	Deaths           []string       `json:"deaths"`
	MinHPPct         map[string]int `json:"min_hp_pct,omitempty"`
	TankNotTargetedS float64        `json:"tank_not_targeted_s"`
	HealsByMob       int            `json:"heals_by_mob"`
	RestS            float64        `json:"rest_s"`
	PracticeRevives  int            `json:"practice_revives"`
	Retries          int            `json:"retries"`
}

// RunSummary is the run's own JSON: the plan's numbers and the run's counters, no identifiers of people or servers.
type RunSummary struct {
	Schema     string          `json:"schema"`
	Version    int             `json:"version"`
	RunID      string          `json:"run_id"`
	Plan       string          `json:"plan"`
	Mode       string          `json:"mode"`
	Clear      bool            `json:"clear"`
	Bosses     map[string]bool `json:"bosses"`
	Wipes      int             `json:"wipes"`
	Deaths     int             `json:"deaths"`
	Practice   int             `json:"practice"`
	DurationS  float64         `json:"duration_s"`
	StopReason string          `json:"stop_reason"`
	LastLeg    string          `json:"last_leg"`
	Instance   uint32          `json:"instance"`
	Pulls      []PullRecord    `json:"pulls"`
	// Watch is set for a watched run: the module's own progress and counters, plus what the watcher did (the
	// practice rescues and the pauses it saw). A dungeon-run leaves it nil.
	Watch *RunWatch `json:"watch,omitempty"`
}

// RunWatch is a watched run's record: the module's progress at the end (or at the last read), its counters, the
// bosses it read through the instance report, and the watcher's own actions.
type RunWatch struct {
	State    string  `json:"state"`
	Step     int     `json:"step"`
	Steps    int     `json:"steps"`
	Leg      int     `json:"leg"`
	LegName  string  `json:"leg_name"`
	Kind     string  `json:"kind,omitempty"`
	Phase    string  `json:"phase"`
	Pull     int     `json:"pull"`
	Pulls    int     `json:"pulls"`
	Deaths   int     `json:"deaths"`
	Wipes    int     `json:"wipes"`
	Pauses   int     `json:"pauses"`
	ElapsedS float64 `json:"elapsed_s"`
	Why      string  `json:"why,omitempty"`
	// Bosses maps a boss entry to alive or dead, as the module's instance report gave it.
	Bosses map[string]string `json:"bosses,omitempty"`
	// Rescues is how many practice rescues the watcher sent, Events how many run events it read, and PauseLog the
	// pauses it saw with their reasons and times.
	Rescues  int         `json:"rescues"`
	Events   int         `json:"events"`
	PauseLog []PauseNote `json:"pause_log,omitempty"`
	// StopUnconfirmed says the cleanup phase ended without the module's record reaching stopped: the stop may not
	// have reached it, so the run's end is not confirmed by the module.
	StopUnconfirmed bool `json:"stop_unconfirmed,omitempty"`
	// Legs, StepLog, DeathLog and RescueLog are the per-leg evidence the report card is built from: the module's own
	// event times, grouped by leg and step. A watch writes them as it reads; a saved record lets the card be built
	// offline. They are empty for a record written before this round.
	Legs      []LegRecord   `json:"legs,omitempty"`
	StepLog   []StepRecord  `json:"step_log,omitempty"`
	DeathLog  []DeathRecord `json:"death_log,omitempty"`
	RescueLog []RescueNote  `json:"rescue_log,omitempty"`
}

// PauseNote is one pause event: its time into the watched run, where it happened, the module's reason and whether a
// resume followed it.
type PauseNote struct {
	AtS     float64 `json:"at_s"`
	Leg     int     `json:"leg,omitempty"`
	Step    int     `json:"step,omitempty"`
	Why     string  `json:"why"`
	Cleared bool    `json:"cleared"`
}

// LegRecord is one leg of the module's run: its event span, its pulls, and its deaths, wipes and pauses. Seconds runs to the next leg's first event, or to the run's last elapsed second for the last leg.
type LegRecord struct {
	Leg     int    `json:"leg"`
	Name    string `json:"name,omitempty"`
	FirstS  int    `json:"first_s"`
	LastS   int    `json:"last_s"`
	Seconds int    `json:"seconds"`
	Pulls   int    `json:"pulls"`
	Steps   int    `json:"steps"`
	Deaths  int    `json:"deaths"`
	Wipes   int    `json:"wipes"`
	Pauses  int    `json:"pauses"`
	// PlanS is the plan's recorded time for the leg in seconds, 0 when the plan records none.
	PlanS int `json:"plan_s,omitempty"`
}

// StepRecord is one step that had a pull or a death: its pull events (a second one at the same step is a re-pull)
// and its deaths.
type StepRecord struct {
	Leg        int        `json:"leg"`
	Step       int        `json:"step"`
	Pulls      int        `json:"pulls"`
	PullEvents []PullNote `json:"pull_events,omitempty"`
	Deaths     int        `json:"deaths"`
}

// PullNote is one pull event at a step: its place in the module's event ring and its time into the run.
type PullNote struct {
	Seq int `json:"seq"`
	AtS int `json:"at_s"`
}

// DeathRecord is one death event: when, where, and the party slot the module named (a role word, never a
// character name). Unit is the module's own unit id when it sent one.
type DeathRecord struct {
	AtS  int    `json:"at_s"`
	Leg  int    `json:"leg"`
	Step int    `json:"step"`
	Unit uint32 `json:"unit,omitempty"`
	Who  string `json:"who,omitempty"`
}

// RescueNote is one practice rescue the watcher sent: when, where, and the pause it answered.
type RescueNote struct {
	AtS  int    `json:"at_s"`
	Leg  int    `json:"leg"`
	Step int    `json:"step"`
	Why  string `json:"why,omitempty"`
}

// Recorder writes the run's trace and summary. A nil Recorder writes nothing, so a run without an output folder still
// plays. It is used from the driver's one goroutine only.
type Recorder struct {
	runID   string
	trace   *trace.Writer
	traceAt string
	sumAt   string
	start   time.Time
	sum     RunSummary
	pull    *PullRecord
}

// NewRecorder opens a dungeon-run's trace in dir. runID names the files; plan and instance go into the header and
// summary.
func NewRecorder(dir, runID, plan string, planMap, instance uint32, start time.Time, party []trace.Member) (*Recorder, error) {
	return newRecorder(dir, runID, plan, planMap, instance, start, party, "commander", TraceSource)
}

// NewWatchRecorder opens a watched run's trace: the same files and schema, with the watch's own mode and source.
func NewWatchRecorder(dir, runID, plan string, planMap, instance uint32, start time.Time, party []trace.Member) (*Recorder, error) {
	return newRecorder(dir, runID, plan, planMap, instance, start, party, "watch", TraceSourceWatch)
}

func newRecorder(dir, runID, plan string, planMap, instance uint32, start time.Time, party []trace.Member,
	mode, source string) (*Recorder, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	w, err := trace.Open(trace.Options{Dir: dir, RunID: runID, Src: trace.Producer})
	if err != nil {
		return nil, err
	}
	r := &Recorder{
		runID:   runID,
		trace:   w,
		traceAt: w.Path(),
		sumAt:   filepath.Join(dir, runID+".forever-run.json"),
		start:   start,
		sum: RunSummary{
			Schema:  SummarySchema,
			Version: SummaryVersion,
			RunID:   runID,
			Plan:    plan,
			Mode:    mode,
			Bosses:  map[string]bool{},
			Pulls:   []PullRecord{},
		},
	}
	w.Header(trace.Header{
		Source:    source,
		Mode:      mode,
		Plan:      plan,
		Start:     start,
		Map:       planMap,
		Instance:  instance,
		Caps:      []string{},
		Party:     party,
		Requester: "",
	})
	return r, nil
}

// ms is the time since the run started, in the trace's unit.
func (r *Recorder) ms(t time.Time) int64 { return int64(t.Sub(r.start) / time.Millisecond) }

// Event writes one event.
func (r *Recorder) Event(t time.Time, ev string, d map[string]any) {
	if r == nil || r.trace == nil {
		return
	}
	r.trace.Event(r.ms(t), ev, d)
}

// Frame writes one frame of the party and the units in view.
func (r *Recorder) Frame(t time.Time, rep *tbcmd.StateReport, units []Unit, botGUID map[string]string) {
	if r == nil || r.trace == nil {
		return
	}
	f := trace.Frame{Via: "session"}
	if rep != nil {
		for _, b := range rep.Bots {
			fb := trace.Bot{
				G:      botGUID[b.Name],
				Alive:  !isDead(b),
				Combat: b.InCombat,
				State:  stateCode(b),
				HP:     uint32(max(b.HP, 0)),
				MaxHP:  uint32(max(b.HPMax, 0)),
				Power:  uint32(max(b.Power, 0)),
				MaxPow: uint32(max(b.PowerMax, 0)),
				PType:  powerType(b.PowerType),
				X:      float32(b.X),
				Y:      float32(b.Y),
				Z:      float32(b.Z),
				Moving: b.Move == "follow",
				Target: b.TargetGUID,
			}
			if fb.G == "" {
				fb.G = b.Name
			}
			f.Bots = append(f.Bots, fb)
		}
	}
	for _, u := range units {
		if u.Friendly {
			continue
		}
		pct := 0
		if u.MaxHP > 0 {
			pct = u.HP * 100 / u.MaxHP
		}
		f.Mobs = append(f.Mobs, trace.Mob{
			G:      u.GUID.String(),
			HPPct:  pct,
			X:      u.Pos[0],
			Y:      u.Pos[1],
			Z:      u.Pos[2],
			Combat: !u.Dead && !u.Target.IsZero(),
			Target: u.Target.String(),
			Cast:   u.Cast,
			Entry:  u.Entry,
		})
	}
	r.trace.Frame(f)
}

// stateCode is the frame's one-letter engine state: c combat, n non-combat, d dead, r resting.
func stateCode(b tbcmd.BotState) string {
	switch {
	case isDead(b):
		return "d"
	case b.Resting:
		return "r"
	case b.InCombat:
		return "c"
	default:
		return "n"
	}
}

// powerType maps the report's power word to the trace's number: 0 mana, 1 rage, 2 focus, 3 energy.
func powerType(w string) uint8 {
	switch w {
	case "rage":
		return 1
	case "focus":
		return 2
	case "energy":
		return 3
	default:
		return 0
	}
}

// PullStart starts a pull's record; it also writes the trace's pull_start event.
func (r *Recorder) PullStart(t time.Time, p PullRecord) {
	if r == nil {
		return
	}
	p.Start = t.Sub(r.start).Seconds()
	r.pull = &p
	r.Event(t, "pull_start", map[string]any{
		"leg": p.Leg, "group": p.Group, "entries": p.Entries, "boss": p.Boss, "mobs": p.Mobs,
	})
}

// UpdatePull changes the open pull's record (the fight's counts are known only when it ends).
func (r *Recorder) UpdatePull(fn func(*PullRecord)) {
	if r == nil || r.pull == nil {
		return
	}
	fn(r.pull)
}

// CurrentPull is the pull being fought, nil between pulls.
func (r *Recorder) CurrentPull() *PullRecord {
	if r == nil {
		return nil
	}
	return r.pull
}

// PullEnd closes the pull's record, writes the trace's pull_end and rewrites the summary.
func (r *Recorder) PullEnd(t time.Time, rest time.Duration) {
	if r == nil || r.pull == nil {
		return
	}
	p := r.pull
	p.Seconds = t.Sub(r.start).Seconds() - p.Start
	p.RestS = rest.Seconds()
	r.Event(t, "pull_end", map[string]any{
		"seconds": p.Seconds, "mobs": p.Mobs, "adds": p.Adds, "deaths": p.Deaths,
	})
	r.sum.Pulls = append(r.sum.Pulls, *p)
	r.pull = nil
	r.Summary(t)
}

// Summary rewrites the summary file atomically.
func (r *Recorder) Summary(t time.Time) {
	if r == nil || r.sumAt == "" {
		return
	}
	r.sum.DurationS = t.Sub(r.start).Seconds()
	r.sum.Pulls = append([]PullRecord(nil), r.sum.Pulls...)
	b, err := json.MarshalIndent(r.sum, "", "  ")
	if err != nil {
		return
	}
	tmp := r.sumAt + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, r.sumAt)
}

// Finish closes the run: the end record, the summary and the trace file.
func (r *Recorder) Finish(t time.Time, reason, lastLeg string) {
	if r == nil {
		return
	}
	r.sum.StopReason = reason
	r.sum.LastLeg = lastLeg
	r.Summary(t)
	if r.trace != nil {
		r.trace.End(trace.End{
			Reason:  reason,
			Seconds: t.Sub(r.start).Seconds(),
			Ended:   t,
			Counters: map[string]any{
				"wipes": r.sum.Wipes, "deaths": r.sum.Deaths, "practice": r.sum.Practice,
				"pulls": len(r.sum.Pulls), "clear": r.sum.Clear,
			},
		})
		_ = r.trace.Close()
	}
}

// SetWatch stores a watched run's counters and rewrites the summary. It is called while the watch is on and once at
// its end, so a watch that loses its link still leaves the last record it read.
func (r *Recorder) SetWatch(w RunWatch, t time.Time) {
	if r == nil {
		return
	}
	cp := w
	if w.Bosses != nil {
		cp.Bosses = make(map[string]string, len(w.Bosses))
		for k, v := range w.Bosses {
			cp.Bosses[k] = v
		}
	}
	cp.PauseLog = append([]PauseNote(nil), w.PauseLog...)
	cp.Legs = append([]LegRecord(nil), w.Legs...)
	cp.StepLog = append([]StepRecord(nil), w.StepLog...)
	for i := range cp.StepLog {
		cp.StepLog[i].PullEvents = append([]PullNote(nil), w.StepLog[i].PullEvents...)
	}
	cp.DeathLog = append([]DeathRecord(nil), w.DeathLog...)
	cp.RescueLog = append([]RescueNote(nil), w.RescueLog...)
	r.sum.Watch = &cp
	r.Summary(t)
}

// SummaryPath is where the run's JSON is written.
func (r *Recorder) SummaryPath() string {
	if r == nil {
		return ""
	}
	return r.sumAt
}

// TracePath is where the run's trace is written.
func (r *Recorder) TracePath() string {
	if r == nil {
		return ""
	}
	return r.traceAt
}
