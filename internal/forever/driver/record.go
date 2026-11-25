package driver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	// TraceSourcePlay is the player run's source word: the same summary schema, filled by the run's own leader.
	TraceSourcePlay = "forever_dungeon_play"
	// DefaultCombatMaxBytes is the combat log's size bound: some 370,000 rows of the trials row shape, far more than
	// a dungeon run's fights write. A page that would cross it is not written, so the file never holds a partial page
	// and the summary says the log is truncated.
	DefaultCombatMaxBytes = 64 << 20
)

var (
	// errCombatFull: the log reached its size bound. Combat wrote nothing and the watch stops draining it.
	errCombatFull = errors.New("the combat log reached its size bound")
	// errCombatEmpty: a page carried an empty row. Nothing is written, because a page is placed whole or not at all
	// and the caller's cursor may only advance through a page the file took.
	errCombatEmpty = errors.New("the combat log page carried an empty row")
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
	// Joined marks a pull the run did not start: a fight its party was already in, joined by the leader (fight_join),
	// recorded for the plan group that owns the units (none is written when no group does).
	Joined bool `json:"joined,omitempty"`
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
	// CombatLogWatch is a watch's GM combat-log watch: group or unit when the core started one, off when the
	// operator turned it off, unsupported when the core has no such command (the log is then in range only).
	// Bug-hunt reads it to skip rules that need the whole log. Other modes leave it empty.
	CombatLogWatch string       `json:"combatlog_watch,omitempty"`
	Pulls          []PullRecord `json:"pulls"`
	// Watch is set for a watched run: the module's own progress and counters, plus what the watcher did (the
	// practice rescues and the pauses it saw). A dungeon-run leaves it nil.
	Watch *RunWatch `json:"watch,omitempty"`
	// Play is set for a player run (forever dungeon-play): its time, its budgets, the lines it sent and the evidence
	// the spell judge reads. Other modes leave it nil.
	Play *PlayRecord `json:"play,omitempty"`
	// Decisions is the module's decision rows a run pulled at its end (--decisions-pull): the rows written to
	// <run_id>.decisions.jsonl, or why the pull wrote none. A run that did not ask for the pull leaves it nil.
	Decisions *RunDecisions `json:"decisions,omitempty"`
	// Notes are a player run's own notes, the same lines its trace writes as `note` events: a plan field the run
	// does not act on, and a raid group's split when it differs from the plan's. Other modes leave it empty.
	Notes []string `json:"notes,omitempty"`
}

// PlayRecord is a player run's own counters (#88). Minutes is the run's length on its clock, MaxMinutes and MaxWipes
// the budgets it ran under (MaxWipes -1 is no budget). LinesByPrefix counts every line the run's send gate admitted
// by its first words (`.bot add`, `whisper release`), and GMLines the lines that began with a `.` and were not a
// `.bot` line: the gate admits none, so a non-zero count is a broken gate, and the milestone fails on it.
type PlayRecord struct {
	Minutes       float64        `json:"minutes"`
	MaxMinutes    int            `json:"max_minutes"`
	MaxWipes      int            `json:"max_wipes"`
	GMLines       int            `json:"gm_lines"`
	LinesByPrefix map[string]int `json:"lines_by_prefix"`
	// Frames is how many trace frames the run wrote and CombatRows how many combat-log rows reached the run's
	// combat-log file; CombatDropped is how many rows the session's ring lost before the run read them, and
	// CombatTruncated says the file's size bound stopped the log.
	Frames          int  `json:"frames"`
	CombatRows      int  `json:"combat_rows"`
	CombatDropped   int  `json:"combat_dropped,omitempty"`
	CombatTruncated bool `json:"combat_truncated,omitempty"`
	// CombatError is the last write error of a page the run could not put in the file (the rows were lost to the
	// record); empty when the file has every row the run read.
	CombatError string `json:"combat_error,omitempty"`
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
	Rescues int `json:"rescues"`
	Events  int `json:"events"`
	// Frames is how many trace frames the watch wrote, CombatRows how many combat-log rows reached the run's
	// combat-log file and CombatDropped how many rows the session's ring had dropped before the watch read them, in
	// total over the run's holes.
	// CombatBytes is what the file holds, CombatTruncated says the size bound stopped it, and CombatError the last
	// write error (a page that could not be written stays in the cursor and is retried, never skipped).
	Frames          int         `json:"frames,omitempty"`
	CombatRows      int         `json:"combat_rows,omitempty"`
	CombatDropped   int         `json:"combat_dropped,omitempty"`
	CombatBytes     int64       `json:"combat_bytes,omitempty"`
	CombatTruncated bool        `json:"combat_truncated,omitempty"`
	CombatError     string      `json:"combat_error,omitempty"`
	PauseLog        []PauseNote `json:"pause_log,omitempty"`
	// StopUnconfirmed says the cleanup phase ended without the module's record reaching stopped: the stop may not
	// have reached it, so the run's end is not confirmed by the module.
	StopUnconfirmed bool `json:"stop_unconfirmed,omitempty"`
	// Legs, StepLog, DeathLog and RescueLog are the per-leg evidence the report card is built from: the module's own
	// event times, grouped by leg and step. A watch writes them as it reads; a saved record lets the card be built
	// offline. They are empty for a record written before this round.
	Legs     []LegRecord   `json:"legs,omitempty"`
	StepLog  []StepRecord  `json:"step_log,omitempty"`
	DeathLog []DeathRecord `json:"death_log,omitempty"`
	// LoosePulls are the pull events that named no step (or no leg), so the step log cannot hold them.
	LoosePulls []PullNote   `json:"loose_pulls,omitempty"`
	RescueLog  []RescueNote `json:"rescue_log,omitempty"`
	// Encounters are the instance's own pull, kill and wipe packets: the exact moments, where the boss report
	// could only say alive or dead. An instance whose script sends nothing leaves it empty and the boss map is
	// the evidence.
	Encounters []EncounterRecord `json:"encounters,omitempty"`
}

// EncounterRecord is one encounter packet on the watched run's clock: "start" (the pull), "end" (a kill when
// Success, a wipe or reset otherwise) or "boss_kill" (the credit that follows a kill).
type EncounterRecord struct {
	AtS         float64 `json:"at_s"`
	Kind        string  `json:"kind"`
	EncounterID uint32  `json:"encounter"`
	Difficulty  int32   `json:"difficulty,omitempty"`
	GroupSize   int32   `json:"group_size,omitempty"`
	DurationS   float64 `json:"duration_s,omitempty"`
	Success     bool    `json:"success,omitempty"`
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
// character name). Unit is the module's own unit id when it sent one. Why is the event's why= as it arrived: the
// dead member's name when it named one of the party, and the name that matched nobody when it did not, in which
// case Who is empty and the death is unattributed. Recap is the parsed `.deathrecap` timeline the watch asked for
// once when the death arrived: the core's own header, frame and cast lines, as text (or "no recording" / "not
// recorded" when the server said so); empty when the watch never asked (a dry run, a record from before this
// round, a death of no party member).
type DeathRecord struct {
	AtS   int      `json:"at_s"`
	Leg   int      `json:"leg"`
	Step  int      `json:"step"`
	Unit  uint32   `json:"unit,omitempty"`
	Who   string   `json:"who,omitempty"`
	Why   string   `json:"why,omitempty"`
	Recap []string `json:"recap,omitempty"`
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
	dir     string
	trace   *trace.Writer
	traceAt string
	sumAt   string
	// combatAt is the run's combat-log file and combat the open handle: written by a watch that drains the
	// session's log, opened on the first row so a run without one leaves no empty file. combatSize is the bytes the
	// file holds, combatMax its bound and combatTrunc that the bound stopped a page.
	combatAt    string
	combat      *os.File
	combatSize  int64
	combatMax   int64
	combatTrunc bool
	start       time.Time
	sum         RunSummary
	pull        *PullRecord
}

// NewRecorder opens a dungeon-run's trace in dir. runID names the files; plan and instance go into the header and
// summary.
func NewRecorder(dir, runID, plan string, planMap, instance uint32, start time.Time, party []trace.Member) (*Recorder, error) {
	return newRecorder(dir, runID, plan, planMap, instance, start, party, "commander", TraceSource)
}

// NewPlayRecorder opens a player run's trace: the same files and schema, with the player run's own mode and source.
func NewPlayRecorder(dir, runID, plan string, planMap, instance uint32, start time.Time, party []trace.Member) (*Recorder, error) {
	return newRecorder(dir, runID, plan, planMap, instance, start, party, "play", TraceSourcePlay)
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
		runID:     runID,
		dir:       dir,
		trace:     w,
		traceAt:   w.Path(),
		sumAt:     filepath.Join(dir, runID+".forever-run.json"),
		combatAt:  filepath.Join(dir, runID+".combatlog.jsonl"),
		combatMax: DefaultCombatMaxBytes,
		start:     start,
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
	f := trace.Frame{Via: "session", Envelope: trace.Envelope{T: r.ms(t)}}
	if rep != nil {
		for _, b := range rep.Bots {
			f.Bots = append(f.Bots, botFrame(b, botGUID[b.Name], "", nil))
		}
	}
	members := frameMembers(f.Bots)
	for _, u := range units {
		if u.Friendly || (!u.Owner.IsZero() && members[strings.ToUpper(u.Owner.String())]) {
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
	f.Pets = framePets(members, units)
	r.trace.Frame(f)
}

// PlayFrameBot is one bot of a player run's frame: the roster's reading of it, the slot's role and name in the trace
// (never the character's name), the class and level `.bot who` gave, and whether the run is in a fight.
type PlayFrameBot struct {
	Member PartyMember
	Name   string
	Role   string
	Class  string
	Level  int
	Combat bool
}

// PlaySelf is the player run's own character as a frame writes it: its guid (the party's leader), where it stands, its
// map and whether it is alive.
type PlaySelf struct {
	GUID  string
	Pos   Vec3
	Map   uint32
	Alive bool
}

// PlayFrame writes one frame of a player run from the party roster, the run's own character and the hostile units in
// view. The first frame a bot is in also writes its party record (guid, role, class and level), which the spell judge
// joins the combat log on. A unit owned by a party member (the agent included) is the frame's pet, not a mob. It
// reports whether the writer kept the frame (it samples). A frame never carries a character name.
func (r *Recorder) PlayFrame(t time.Time, self PlaySelf, bots []PlayFrameBot, units []Unit) bool {
	if r == nil || r.trace == nil {
		return false
	}
	ms := r.ms(t)
	f := trace.Frame{Via: "session", Envelope: trace.Envelope{T: ms}}
	f.Self = &trace.Self{X: self.Pos[0], Y: self.Pos[1], Z: self.Pos[2], Map: self.Map, Alive: self.Alive}
	for _, b := range bots {
		m := b.Member
		g := m.GUID.String()
		if m.GUID.IsZero() {
			g = ""
		}
		alive := !m.Dead && !m.Ghost
		st := "n"
		switch {
		case !alive:
			st = "d"
		case b.Combat:
			st = "c"
		}
		f.Bots = append(f.Bots, trace.Bot{
			G: g, Alive: alive, Combat: alive && b.Combat, State: st,
			HP: uint32(max(m.HP, 0)), MaxHP: uint32(max(m.MaxHP, 0)),
			Power: uint32(max(m.Power, 0)), MaxPow: uint32(max(m.MaxPower, 0)), PType: uint8(max(m.PowerType, 0)),
			X: m.Pos[0], Y: m.Pos[1], Z: m.Pos[2], Role: b.Role,
		})
		if g != "" {
			r.trace.Party(ms, trace.Member{G: g, Name: b.Name, Role: b.Role, Class: b.Class, Level: b.Level})
		}
	}
	members := frameMembers(f.Bots)
	if g := strings.ToUpper(strings.TrimSpace(self.GUID)); g != "" {
		members[g] = true
	}
	for _, u := range units {
		if u.Friendly || (!u.Owner.IsZero() && members[strings.ToUpper(u.Owner.String())]) {
			continue
		}
		pct := 0
		if u.MaxHP > 0 {
			pct = u.HP * 100 / u.MaxHP
		}
		f.Mobs = append(f.Mobs, trace.Mob{
			G: u.GUID.String(), HPPct: pct, X: u.Pos[0], Y: u.Pos[1], Z: u.Pos[2],
			Combat: !u.Dead && !u.Target.IsZero(), Target: u.Target.String(), Cast: u.Cast, Entry: u.Entry,
		})
	}
	f.Pets = framePets(members, units)
	return r.trace.Frame(f)
}

// FrameBot is one party member of a frame taken from the module's own state report: its line, the guid this
// client's model gives its name (the name itself stays out of the trace), the party slot's role and the auras the
// model holds for it.
type FrameBot struct {
	State tbcmd.BotState
	GUID  string
	Role  string
	Auras []uint32
}

// WatchFrame writes one frame of a watched run from the module's own reports: the party from `.tb state`, the
// mobs from `.tb threat` and the party members' pets from the world model's units. Both are reads; a frame never
// carries an order. The party's first frame also writes the party records (guid, name, role, class) the rest of
// the run joins on: a watch's roster comes from the module, so the header could not carry it.
func (r *Recorder) WatchFrame(t time.Time, bots []FrameBot, thr *tbcmd.ThreatReport, guidOf func(string) string, units []Unit) {
	if r == nil || r.trace == nil {
		return
	}
	ms := r.ms(t)
	f := trace.Frame{Via: "session", Envelope: trace.Envelope{T: ms}}
	for _, b := range bots {
		fb := botFrame(b.State, b.GUID, b.Role, b.Auras)
		f.Bots = append(f.Bots, fb)
		if fb.G != "" {
			r.trace.Party(ms, trace.Member{G: fb.G, Name: b.Role, Role: b.Role, Class: b.State.Class})
		}
	}
	if thr != nil {
		for _, m := range thr.Mobs {
			mob := trace.Mob{
				G:      traceGUID(m.Mob),
				Entry:  uint32(max(m.Entry, 0)),
				Name:   m.Name,
				HPPct:  m.HPPct,
				Combat: m.Victim != "" || m.Target != "",
				Victim: guidOf(m.Victim),
				Flee:   m.Flee,
			}
			if m.HasPos {
				mob.X, mob.Y, mob.Z = float32(m.X), float32(m.Y), float32(m.Z)
			} else {
				mob.NP = true
			}
			// The registry record, the first time this mob is in a frame: the entry and the module's name, which the
			// frame itself does not carry.
			r.trace.Mob(ms, trace.MobInfo{G: mob.G, Entry: mob.Entry, Name: m.Name})
			for _, e := range m.Top {
				mob.Top = append(mob.Top, trace.TopEntry{G: guidOf(e.Name), Threat: e.Threat})
			}
			f.Mobs = append(f.Mobs, mob)
		}
	}
	f.Pets = framePets(frameMembers(f.Bots), units)
	r.trace.Frame(f)
}

func frameMembers(bots []trace.Bot) map[string]bool {
	members := make(map[string]bool, len(bots))
	for _, b := range bots {
		if b.G != "" {
			members[strings.ToUpper(b.G)] = true
		}
	}
	return members
}

// framePets selects the world units whose owner is a member of the frame (using P2's owner rule, carried on
// Unit.Owner) and shapes them into trace.Pet rows.
func framePets(members map[string]bool, units []Unit) []trace.Pet {
	if len(members) == 0 || len(units) == 0 {
		return nil
	}
	var pets []trace.Pet
	for _, u := range units {
		if u.Owner.IsZero() {
			continue
		}
		ow := u.Owner.String()
		if !members[strings.ToUpper(ow)] {
			continue
		}
		pct := 0
		if u.MaxHP > 0 {
			pct = u.HP * 100 / u.MaxHP
		}
		tg := ""
		if !u.Target.IsZero() {
			tg = u.Target.String()
		}
		pets = append(pets, trace.Pet{
			G:      u.GUID.String(),
			Owner:  ow,
			HPPct:  pct,
			Combat: u.Combat,
			Target: tg,
			X:      u.Pos[0],
			Y:      u.Pos[1],
			Z:      u.Pos[2],
		})
	}
	return pets
}

// botFrame is one party member's frame entry from the module's state line: the engine state, health, power,
// position and target, and the module's own report of role, move order, stuck state, meal, rest, life and distance.
// G is the guid text its caller resolved, or empty when the model does not know the member yet: a frame never
// carries a character name, and an unresolved member is a row without an identity (its role still names its slot)
// rather than one that changes identity once the guid arrives.
func botFrame(b tbcmd.BotState, guid, role string, auras []uint32) trace.Bot {
	fb := trace.Bot{
		G:      guid,
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
		Target: traceGUID(b.TargetGUID),
		Role:   role,
		Move:   b.Move,
		Stuck:  b.Stuck.Raw,
		Eat:    b.Eating,
		Drink:  b.Drinking,
		Rest:   b.Resting,
		Life:   b.Extra["life"],
		Auras:  auras,
	}
	if d := b.DistLeader; d.Has {
		switch d.Kind {
		case "othermap":
			// A member on another map has no distance to the leader, so the frame has no dl key.
		case "self":
			var yd float32
			fb.DL = &yd
		default:
			yd := float32(d.Yards)
			fb.DL = &yd
		}
	}
	return fb
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
	// A pull_start never overwrites an open pull: that one is closed first, so every pull_start has one pull_end.
	r.PullAbort(t, "superseded")
	r.pull = &p
	data := map[string]any{
		"leg": p.Leg, "group": p.Group, "entries": p.Entries, "boss": p.Boss, "mobs": p.Mobs,
	}
	if p.Joined {
		data["joined"] = true
	}
	r.Event(t, "pull_start", data)
}

// PullAbort closes the open pull's record with a pull_end that says why (`aborted`), when no fight came of it: a
// regroup that took the run out of the pull before the party was in combat, a fight another pack's fight replaced, a
// stop. The pull is no pull of the summary (its rows are the pulls that were fought), and the call is a no-op between
// pulls.
func (r *Recorder) PullAbort(t time.Time, why string) {
	if r == nil || r.pull == nil {
		return
	}
	p := r.pull
	r.Event(t, "pull_end", map[string]any{
		"seconds": t.Sub(r.start).Seconds() - p.Start, "mobs": p.Mobs, "adds": p.Adds, "deaths": p.Deaths, "aborted": why,
	})
	r.pull = nil
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
	r.CloseCombat()
}

// Combat appends the combat log's rows to the run's <run_id>.combatlog.jsonl, one JSON row per line, as the session
// answered them (the trials runner's raw row shape). It returns how many rows were written and an error when they
// were not. A page is all or nothing: a failed or short write is cut back to the file's last complete row, so the
// caller can retry the same page without advancing its cursor past rows that are not on disk. A page that would
// cross SetCombatMaxBytes' bound is not written and answers errCombatFull. The file is opened on the first row; a
// run whose log never answered leaves no file.
func (r *Recorder) Combat(rows []json.RawMessage) (int, error) {
	if r == nil || r.combatAt == "" || len(rows) == 0 {
		return 0, nil
	}
	if r.combatTrunc {
		return 0, errCombatFull
	}
	buf := make([]byte, 0, 128*len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			return 0, errCombatEmpty
		}
		buf = append(buf, row...)
		buf = append(buf, '\n')
	}
	if r.combat == nil {
		f, err := os.OpenFile(r.combatAt, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return 0, err
		}
		st, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return 0, err
		}
		r.combat, r.combatSize = f, st.Size()
	}
	if r.combatMax > 0 && r.combatSize+int64(len(buf)) > r.combatMax {
		r.combatTrunc = true
		if r.combatSize == 0 {
			// Nothing was ever written: leave no file behind, as a run whose log never answered leaves none.
			_ = r.combat.Close()
			_ = os.Remove(r.combatAt)
			r.combat = nil
		}
		return 0, errCombatFull
	}
	n, err := r.combat.Write(buf)
	if err == nil && n != len(buf) {
		err = io.ErrShortWrite
	}
	if err != nil {
		// Cut the partial rows back off: the file ends at the last complete row, and the caller retries the page.
		if terr := r.combat.Truncate(r.combatSize); terr != nil {
			return 0, fmt.Errorf("%w (and the truncate back to %d bytes failed: %v)", err, r.combatSize, terr)
		}
		return 0, err
	}
	r.combatSize += int64(n)
	return len(rows), nil
}

// SetCombatMaxBytes bounds the combat-log file. A page that would cross the bound is not written (all or nothing, so
// the watch's cursor never advances past rows that are not on disk) and the run's summary carries
// combat_truncated. A value of zero or less keeps the default.
func (r *Recorder) SetCombatMaxBytes(n int64) {
	if r == nil || n <= 0 {
		return
	}
	r.combatMax = n
}

// CombatMaxBytes is the combat-log file's bound.
func (r *Recorder) CombatMaxBytes() int64 {
	if r == nil {
		return 0
	}
	return r.combatMax
}

// CombatBytes is how many bytes of combat rows the file holds.
func (r *Recorder) CombatBytes() int64 {
	if r == nil {
		return 0
	}
	return r.combatSize
}

// CombatTruncated says the size bound stopped the log: the file ends where it stood and no later page is written.
func (r *Recorder) CombatTruncated() bool {
	return r != nil && r.combatTrunc
}

// combatErrText is a write error without the file's path: the summary is the record others read, and a path is the
// operator's.
func combatErrText(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	return err.Error()
}

// CombatEvents writes the knockback rows of a drained combat page to the trace: a knockback event at the takeoff and
// a knockback_land event at the landing, on the trace's own clock. The combat log keeps every row; these events make
// the knockback timeline readable beside the frames, where the landing's position can be compared with the mobs.
func (r *Recorder) CombatEvents(rows []json.RawMessage) {
	if r == nil || r.trace == nil {
		return
	}
	for _, raw := range rows {
		if !bytes.Contains(raw, []byte(`"kind":"knockback`)) {
			continue
		}
		var row struct {
			Kind   string `json:"kind"`
			Time   string `json:"time"`
			Source struct {
				GUID string `json:"guid"`
			} `json:"source"`
			Pos *struct {
				X float32 `json:"x"`
				Y float32 `json:"y"`
				Z float32 `json:"z"`
			} `json:"pos"`
			Self      bool    `json:"self"`
			HorzSpeed float32 `json:"horz_speed"`
			VertSpeed float32 `json:"vert_speed"`
			AirMS     int64   `json:"air_ms"`
			Drop      float32 `json:"drop"`
			Simulated bool    `json:"simulated"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			continue
		}
		if row.Kind != "knockback" && row.Kind != "knockback_land" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, row.Time)
		if err != nil {
			continue
		}
		d := map[string]any{"guid": row.Source.GUID, "self": row.Self}
		if row.Pos != nil {
			d["pos"] = map[string]any{"x": row.Pos.X, "y": row.Pos.Y, "z": row.Pos.Z}
		}
		if row.Simulated {
			d["simulated"] = true
		}
		if row.Kind == "knockback" {
			d["speed_xy"] = row.HorzSpeed
			d["speed_z"] = row.VertSpeed
		} else {
			d["air_ms"] = row.AirMS
			d["drop"] = row.Drop
		}
		r.Event(t, row.Kind, d)
	}
}

// CloseCombat closes the combat-log file once the watch has drained its last page.
func (r *Recorder) CloseCombat() {
	if r == nil || r.combat == nil {
		return
	}
	_ = r.combat.Close()
	r.combat = nil
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
	cp.LoosePulls = append([]PullNote(nil), w.LoosePulls...)
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
