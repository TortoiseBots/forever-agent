// Package bg is the battleground watcher's model: the match record a bg-watch
// writes as <run>.forever-bg.json, and the report card built from it.
//
// The watch is the human in the queue: it queues its character, accepts,
// then plays a passive observer (stays at its start spot and never touches a
// flag), records the score timeline from world states, flag events
// (pick/drop/return/capture from 1545/1546 and the 1581/1582 counters), node
// events (the five AB node states), deaths per side, the match result and
// length, and flags the weak spots the report card names as thresholds. It
// sends nothing to the module during the match except `.tb bg status` polls
// (no orders), like dungeon-watch.
package bg

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Map names the two maps the watch covers (bg.go: MapWSG, MapAB).
type Map uint32

// String names the map.
func (m Map) String() string {
	switch uint32(m) {
	case MapWSG:
		return "wsg"
	case MapAB:
		return "ab"
	}
	return fmt.Sprintf("map %d", uint32(m))
}

// ParseMap names a --map word.
func ParseMap(word string) (Map, error) {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "wsg", "warsong", "489":
		return Map(MapWSG), nil
	case "ab", "arathi", "529":
		return Map(MapAB), nil
	}
	return 0, fmt.Errorf("bg: --map %q is not wsg or ab", word)
}

// Role names the watcher's play mode. The watch is a passive observer: it
// stays at its start spot and never touches a flag.
type Role int

// RoleObserver is the only role.
const RoleObserver Role = iota

// ParseRole names a --role word.
func ParseRole(word string) (Role, error) {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "", "observer", "observe":
		return RoleObserver, nil
	}
	return RoleObserver, fmt.Errorf("bg: --role %q is not observer (a flag carrier is not implemented)", word)
}

// EventKind names a record event.
type EventKind string

const (
	// EvQueue is the queue join: CMSG_BATTLEMASTER_JOIN went out.
	EvQueue EventKind = "bg queue"
	// EvInvite is the invite: NEED_CONFIRMATION arrived.
	EvInvite EventKind = "bg invite"
	// EvJoin is the port accept: the character entered the match.
	EvJoin EventKind = "bg join"
	// EvFlagPick is a flag pickup. For AB the banner word is reused with
	// the node name: the record keeps one flag/node vocabulary.
	EvFlagPick EventKind = "bg flag pick"
	// EvFlagDrop is a flag drop.
	EvFlagDrop EventKind = "bg flag drop"
	// EvFlagReturn is an own-flag return.
	EvFlagReturn EventKind = "bg flag return"
	// EvFlagCapture is a capture.
	EvFlagCapture EventKind = "bg flag capture"
	// EvNodeAssault is an AB node assault (banner click to contested).
	EvNodeAssault EventKind = "bg node assault"
	// EvNodeDefend is an AB own-node defend (click back to occupied).
	EvNodeDefend EventKind = "bg node defend"
	// EvNodeOccupied is an AB node occupied (contested timer ran out).
	EvNodeOccupied EventKind = "bg node occupied"
	// EvFallback is an AB fallback the poll reported.
	EvFallback EventKind = "bg fallback"
	// EvDeath is a death in the match.
	EvDeath EventKind = "bg death"
	// EvLeave is the leave (or port out) at the match end.
	EvLeave EventKind = "bg leave"
	// EvEnd is the match end.
	EvEnd EventKind = "bg end"
)

// Event is one record event: when in the watch, what happened, and the
// typed fields it is about (instance, map, bot, team, node or flag). Bot is
// a role word or side ("alliance"/"horde"), never a character name, and no
// field carries chat text.
type Event struct {
	AtS      float64   `json:"at_s"`
	Kind     EventKind `json:"kind"`
	Instance uint32    `json:"instance,omitempty"`
	Map      string    `json:"map,omitempty"`
	Bot      string    `json:"bot,omitempty"`
	Team     string    `json:"team,omitempty"`
	Node     string    `json:"node,omitempty"`
	Flag     string    `json:"flag,omitempty"`
	Text     string    `json:"text,omitempty"`
}

// ScoreSample is one world-state score sample: the match clock and the
// scoreboard as the client saw it.
type ScoreSample struct {
	AtS float64 `json:"at_s"`
	// WSG: captures per side; AB: resources per side and occupied bases.
	Alliance int `json:"alliance"`
	Horde    int `json:"horde"`
	// Bases holds AB occupied bases (alliance, horde); WSG leaves it zero.
	BasesA int `json:"bases_a,omitempty"`
	BasesH int `json:"bases_h,omitempty"`
}

// PosSample is one movement sample of the watcher: the match clock and the
// client's position, for the idle rule (no 5 yd move in 60 s in progress).
type PosSample struct {
	AtS float64 `json:"at_s"`
	X   float32 `json:"x"`
	Y   float32 `json:"y"`
	Z   float32 `json:"z"`
}

// Side is one side's counters: humans and bots inside and the deaths the
// module counted, from the `.tb bg status` polls the watch sends.
type Side struct {
	Humans int `json:"humans"`
	Bots   int `json:"bots"`
	Deaths int `json:"deaths"`
}

// BotRow is one bot the status poll named: its role, objective, node, alive
// state and last-move age.
type BotRow struct {
	// Role is the module's word (fc, defender, escort, attacker, healer).
	Role string `json:"role"`
	// Objective is the module's word for what the bot does now.
	Objective string `json:"objective,omitempty"`
	// Node is the AB node the bot is assigned to, "" elsewhere.
	Node  string `json:"node,omitempty"`
	Alive bool   `json:"alive"`
	// MoveAgeS is how long ago the bot last moved, in seconds.
	MoveAgeS float64 `json:"move_age_s"`
}

// Record is the <run>.forever-bg.json the watch writes: the score timeline
// from world states, flag events, node events, deaths per side, the match
// result and length. RunID names the files; no field carries a character
// name, account, host or path.
type Record struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	RunID   string `json:"run_id"`
	Map     string `json:"map"`
	Role    string `json:"role"`
	// Team is the watcher's side as the module's status polls named it
	// ("alliance"/"horde"); "" when no poll carried it, which switches the
	// untouched-flag and undefended-node rules off.
	Team string `json:"team,omitempty"`
	// Clear says the match ran to its end (a result arrived); StopReason
	// names every other end (queue timeout, link lost, budget).
	Clear      bool          `json:"clear"`
	StopReason string        `json:"stop_reason,omitempty"`
	Winner     string        `json:"winner,omitempty"`
	DurationS  float64       `json:"duration_s"`
	Scores     []ScoreSample `json:"scores,omitempty"`
	Flags      []Event       `json:"flags,omitempty"`
	Nodes      []Event       `json:"nodes,omitempty"`
	Deaths     []Event       `json:"deaths,omitempty"`
	Events     []Event       `json:"events,omitempty"`
	Positions  []PosSample   `json:"positions,omitempty"`
	Alliance   Side          `json:"alliance"`
	Horde      Side          `json:"horde"`
	Bots       []BotRow      `json:"bots,omitempty"`
	// MovedYD reports the watcher's own farthest move from its start spot.
	MovedYD float64 `json:"moved_yd,omitempty"`
	// Captures and Returns count WSG captures/returns; Assaults and Occupied
	// count AB assaults/occupations; Fallbacks counts bg-fallback polls.
	Captures  int `json:"captures,omitempty"`
	Returns   int `json:"returns,omitempty"`
	Assaults  int `json:"assaults,omitempty"`
	Occupied  int `json:"occupied,omitempty"`
	Fallbacks int `json:"fallbacks,omitempty"`
	// Deserter says a bot carried aura 26013 at any poll; Premature says the
	// match ended by the premature-finish timer (winner empty, duration
	// under the winner's threshold with players below minimum).
	Deserter  bool `json:"deserter,omitempty"`
	Premature bool `json:"premature,omitempty"`
	// EscortShare is the fraction of in-progress match time with >= 2
	// teammates within 20 yd of the FC (-1 when no FC was seen);
	// EscortSelected is the sampled seconds behind it.
	EscortShare    float64 `json:"escort_share,omitempty"`
	EscortSelected float64 `json:"escort_selected_s,omitempty"`
	// TouchedDropped counts dropped own flags the team did not touch within
	// 10 s; LostWithDefenders counts AB node losses with 0 defenders near.
	TouchedDropped int `json:"touched_dropped,omitempty"`
	LostNoDefend   int `json:"lost_no_defend,omitempty"`
}

// Schema and Version name the record and card JSON.
const (
	Schema      = "forever-bg"
	Version     = 1
	CardSchema  = "forever-bg-card"
	CardVersion = 1
)

// NewRecord opens a record for a run.
func NewRecord(runID string, m Map) *Record {
	return &Record{Schema: Schema, Version: Version, RunID: runID, Map: m.String(), Role: "observer"}
}

// Add appends one event to the typed list its kind belongs to, and to the
// full event list. Flag/node/death kinds go to both; the rest only to Events.
func (r *Record) Add(e Event) {
	r.Events = append(r.Events, e)
	switch e.Kind {
	case EvFlagPick, EvFlagDrop, EvFlagReturn, EvFlagCapture:
		r.Flags = append(r.Flags, e)
	case EvNodeAssault, EvNodeDefend, EvNodeOccupied:
		r.Nodes = append(r.Nodes, e)
	case EvDeath:
		r.Deaths = append(r.Deaths, e)
	}
	if e.Kind == EvFlagCapture {
		r.Captures++
	}
	if e.Kind == EvFlagReturn {
		r.Returns++
	}
	if e.Kind == EvNodeAssault {
		r.Assaults++
	}
	if e.Kind == EvNodeOccupied {
		r.Occupied++
	}
	if e.Kind == EvFallback {
		r.Fallbacks++
	}
}

// Thresholds are the card's rules, every one a flag with the default below;
// the JSON records the values the card was built with. They are the spec's
// report-card list: no capture/assault in 10 min; a bot idle (no 5 yd move)
// 60 s in progress; FC escort share under 50%; a dropped own flag not
// returned and untouched within 10 s; an AB node lost with 0 defenders near;
// deserter on any bot; the premature-finish end.
type Thresholds struct {
	// QuietMin is the minutes without a capture/assault that flag a quiet match.
	QuietMin float64 `json:"quiet_min"`
	// IdleS is the seconds without a 5 yd move that flag an idle bot.
	IdleS float64 `json:"idle_s"`
	// IdleYD is the move distance that resets the idle clock.
	IdleYD float64 `json:"idle_yd"`
	// EscortShare is the minimum FC escort share (0-1).
	EscortShare float64 `json:"escort_share"`
	// TouchS is the seconds a dropped own flag may lie untouched.
	TouchS float64 `json:"touch_s"`
}

// DefaultThresholds is the card's table of defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{QuietMin: 10, IdleS: 60, IdleYD: 5, EscortShare: 0.5, TouchS: 10}
}

// Card is one BG watch's report card: the verdict, the match numbers and the
// flags with their evidence.
type Card struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	RunID   string `json:"run_id"`
	Map     string `json:"map"`
	// Verdict is one word (CLEARED, QUIET, STALLED, IDLE, UNESCORTED,
	// UNTOUCHED, UNDEFENDED, DESERTER, PREMATURE, STOPPED, FAILED);
	// VerdictText is the one-line sentence.
	Verdict     string `json:"verdict"`
	VerdictText string `json:"verdict_text"`
	// Notes say which rules could not run (for example the ones that need
	// the module's team) so a clean card is not read as proof they passed.
	Notes      []string   `json:"notes,omitempty"`
	Clear      bool       `json:"clear"`
	Winner     string     `json:"winner,omitempty"`
	StopReason string     `json:"stop_reason,omitempty"`
	DurationS  float64    `json:"duration_s"`
	Thresholds Thresholds `json:"thresholds"`
	Flags      []Flag     `json:"flags"`
}

// Flag is one weak spot. ID is stable across runs; Key names the match part
// it is about; Evidence holds the record lines that show it.
type Flag struct {
	ID       string   `json:"id"`
	Key      string   `json:"key"`
	Text     string   `json:"text"`
	Evidence []string `json:"evidence"`
}

// Build makes the card from a record.
func Build(rec *Record, th Thresholds) Card {
	th = normalize(th)
	c := Card{
		Schema: CardSchema, Version: CardVersion, RunID: rec.RunID, Map: rec.Map,
		Clear: rec.Clear, Winner: rec.Winner, StopReason: rec.StopReason,
		DurationS: rec.DurationS, Thresholds: th, Flags: []Flag{},
	}
	c.Flags = buildFlags(rec, th)
	c.Notes = cardNotes(rec)
	c.Verdict, c.VerdictText = verdict(c, rec)
	return c
}

// cardNotes says which rules could not run, so a card with no flag is not
// read as a clean bill: the untouched own-flag and undefended-node rules
// need the team the module's status poll names, and stay off without it.
func cardNotes(rec *Record) []string {
	if rec.Team != "" {
		return nil
	}
	return []string{"team unknown: the untouched own-flag and undefended node rules need the module's team and stayed off"}
}

// normalize fills a zero threshold from the defaults.
func normalize(th Thresholds) Thresholds {
	d := DefaultThresholds()
	if th.QuietMin <= 0 {
		th.QuietMin = d.QuietMin
	}
	if th.IdleS <= 0 {
		th.IdleS = d.IdleS
	}
	if th.IdleYD <= 0 {
		th.IdleYD = d.IdleYD
	}
	if th.EscortShare <= 0 {
		th.EscortShare = d.EscortShare
	}
	if th.TouchS <= 0 {
		th.TouchS = d.TouchS
	}
	return th
}

// verdict is the one-line verdict: the first failing rule in spec order, or
// the match result. A record that never reached a result is STOPPED or
// FAILED by its stop reason.
func verdict(c Card, rec *Record) (string, string) {
	if !rec.Clear {
		reason := rec.StopReason
		if reason == "" {
			reason = "no stop reason"
		}
		if reason == "link-lost" || reason == "blind" {
			return "FAILED", "FAILED (" + reason + ")"
		}
		return "STOPPED", "STOPPED (" + reason + ")"
	}
	if len(c.Flags) == 0 {
		if rec.Winner != "" {
			return "CLEARED", "CLEARED (" + rec.Winner + " won in " + dur(rec.DurationS) + ")"
		}
		return "CLEARED", "CLEARED (match ended in " + dur(rec.DurationS) + ")"
	}
	f := c.Flags[0]
	return strings.ToUpper(f.ID), f.Text
}

// dur prints seconds as m:ss.
func dur(s float64) string {
	m := int(s) / 60
	return fmt.Sprintf("%d:%02d", m, int(s)%60)
}

// buildFlags applies every rule to the record in spec order. A flag's
// evidence is the record's own event lines, so a reader can check the rule
// without opening the log.
func buildFlags(rec *Record, th Thresholds) []Flag {
	var flags []Flag
	// No capture (WSG) or assault (AB) in QuietMin minutes.
	if rec.Clear {
		quiet := th.QuietMin * 60
		if rec.Map == "wsg" && rec.Captures == 0 && rec.DurationS >= quiet {
			flags = append(flags, Flag{
				ID: "quiet", Key: "match",
				Text: fmt.Sprintf("no capture in %s of match", dur(rec.DurationS)),
				Evidence: []string{
					fmt.Sprintf("captures=0 duration_s=%.0f quiet_min=%.0f", rec.DurationS, th.QuietMin),
				},
			})
		}
		if rec.Map == "ab" && rec.Assaults == 0 && rec.DurationS >= quiet {
			flags = append(flags, Flag{
				ID: "quiet", Key: "match",
				Text: fmt.Sprintf("no assault in %s of match", dur(rec.DurationS)),
				Evidence: []string{
					fmt.Sprintf("assaults=0 duration_s=%.0f quiet_min=%.0f", rec.DurationS, th.QuietMin),
				},
			})
		}
	}
	// A bot idle (no IdleYD move) IdleS seconds in progress.
	for _, b := range rec.Bots {
		if b.MoveAgeS >= th.IdleS {
			flags = append(flags, Flag{
				ID: "idle", Key: b.Role,
				Text: fmt.Sprintf("a %s bot idle %.0fs (limit %.0fs)", b.Role, b.MoveAgeS, th.IdleS),
				Evidence: []string{
					fmt.Sprintf("bot role=%s move_age_s=%.0f idle_s=%.0f", b.Role, b.MoveAgeS, th.IdleS),
				},
			})
		}
	}
	// FC escort share under the limit, when an FC was seen at all (the
	// share accumulates only on polls with an FC, so 0 with an FC seen is
	// never escorted; -1 with no FC seen is not a finding).
	if rec.EscortSelected > 0 && rec.EscortShare < th.EscortShare {
		flags = append(flags, Flag{
			ID: "unescorted", Key: "fc",
			Text: fmt.Sprintf("FC escort share %.0f%% under %.0f%%", 100*rec.EscortShare, 100*th.EscortShare),
			Evidence: []string{
				fmt.Sprintf("escort_share=%.2f limit=%.2f", rec.EscortShare, th.EscortShare),
			},
		})
	}
	// A dropped own flag not returned and untouched within TouchS seconds.
	if rec.TouchedDropped > 0 {
		flags = append(flags, Flag{
			ID: "untouched", Key: "flag",
			Text: fmt.Sprintf("%d dropped own flag(s) untouched for %.0fs", rec.TouchedDropped, th.TouchS),
			Evidence: []string{
				fmt.Sprintf("touched_dropped=%d touch_s=%.0f", rec.TouchedDropped, th.TouchS),
			},
		})
	}
	// An AB node lost with 0 defenders near.
	if rec.LostNoDefend > 0 {
		flags = append(flags, Flag{
			ID: "undefended", Key: "node",
			Text:     fmt.Sprintf("%d node(s) lost with 0 defenders near", rec.LostNoDefend),
			Evidence: []string{fmt.Sprintf("lost_no_defend=%d", rec.LostNoDefend)},
		})
	}
	// Deserter on any bot.
	if rec.Deserter {
		flags = append(flags, Flag{
			ID: "deserter", Key: "bots",
			Text:     "a bot carried the Deserter aura",
			Evidence: []string{"deserter=true"},
		})
	}
	// The premature-finish end.
	if rec.Premature {
		flags = append(flags, Flag{
			ID: "premature", Key: "match",
			Text:     "the match ended by the premature-finish timer",
			Evidence: []string{"premature=true"},
		})
	}
	return flags
}

// WatcherConfig is the watch machine's knobs: the map, the role, the output
// folder and the card thresholds. The queue waits for an invite inside the
// same budget the match runs in (MaxMinutes), and the status polls go out
// every Poll seconds.
type WatcherConfig struct {
	Map    Map
	Role   Role
	TeamOf int // --team-of N: follow the team-of hint (reserved; refused unless 0)
	Out    string
	RunID  string
	MaxMin int
	PollS  int
	Thresholds
}

// DefaultWatcherConfig is the watch's table of defaults.
func DefaultWatcherConfig() WatcherConfig {
	return WatcherConfig{Map: Map(MapWSG), Role: RoleObserver, MaxMin: 45, PollS: 5, Thresholds: DefaultThresholds()}
}

// Watcher is the bg-watch machine over the session's ports: queue, accept,
// observe, record, leave, card. Ports is the seam the live session and the
// dry fake both implement.
type Watcher struct {
	cfg   WatcherConfig
	ports Ports
	rec   *Record
	logf  func(format string, args ...any)
	start time.Time
	now   func() time.Time
	// team is the watcher's side as the last status poll named it.
	team string
	// flags/nodes hold the last derived states; caps the capture counters.
	flags map[int32]int32
	nodes map[int]ABNodeState
	caps  map[int32]int32
	// owned is each AB node's last occupying side ("alliance"/"horde"),
	// used to tell a node lost to the enemy from one never seen ours.
	owned [5]string
	// droppedAt maps a dropped flag to when it fell; touched marks the drop
	// already counted, cleared when the flag leaves the ground.
	droppedAt map[string]float64
	touched   map[string]bool
	// botRows keeps the module's bot rows, each with its longest still span
	// over the match. movedYD and the escort counters are the samples
	// finish writes whatever the stop.
	botRows    []BotRow
	movedYD    float64
	escortSeen float64
	escortGood float64
	// noResult is set when the end came from a complete-state initialize
	// rather than the result packet: the winner is unknown, so finish does
	// not infer one from the last score sample.
	noResult bool
}

// Ports is what the watcher needs from the session: send one client packet,
// poll `.tb bg status`, read the world states the session decoded, and sleep
// between polls. The dry fake implements the same seam.
type Ports interface {
	// Send writes one client packet by core opcode.
	Send(core uint32, payload []byte) error
	// Status polls `.tb bg status`: the per-side humans/bots and per-bot
	// rows the record keeps, plus deaths and deserter sightings.
	Status() (StatusReport, error)
	// WorldStates returns the world-state map the session decoded so far.
	WorldStates() map[int32]int32
	// SelfPos returns the watcher's own position.
	SelfPos() (x, y, z float32)
	// Died says the watcher itself died since the last poll.
	Died() bool
	// PollStatus sends CMSG_REQUEST_BATTLEFIELD_STATUS (an empty packet:
	// the server answers the queued/invited/active status).
	PollStatus() error
	// Refused is the last join refusal the session decoded (a FAILED
	// status: deserter, out of bracket), "" when there is none.
	Refused() string
	// Active says the server confirmed the queue entry
	// (SMSG_BATTLEFIELD_STATUS_ACTIVE): the join event and the match
	// observation wait for it.
	Active() bool
	// Sleep waits between polls, answering server packets meanwhile.
	Sleep(d time.Duration) error
}

// ErrStatusRefused says the `.tb bg status` poll was answered without the
// module's BG block: the server has no module, or it is off. It is a
// configuration answer, not a broken link.
var ErrStatusRefused = errors.New("bg: the status poll was refused")

// StatusReport is one `.tb bg status` poll answer: the watcher's own team,
// per-side humans/bots/deaths and the per-bot rows. Names never leave the
// session: a bot row carries a role word and a node, never a name.
type StatusReport struct {
	// Team is the watcher's side ("alliance" or "horde") as the module
	// reports it; "" when the answer does not carry it.
	Team     string
	Alliance Side
	Horde    Side
	Bots     []BotRow
	// FCEscorts is how many of the watcher's team the module counts within
	// 20 yards of the flag carrier, the carrier itself excluded.
	FCEscorts int
	Deserter  bool
	FCX, FCY  float32
	HasFC     bool
	Fallback  bool
	Map       string
	Status    string
	ScoreA    int
	ScoreH    int
}

// Result is what the watch did: the record, the card and the end state.
type Result struct {
	Record *Record
	Card   Card
	Stop   string
	Clear  bool
}

// NewWatcher builds the watch machine over ports.
func NewWatcher(cfg WatcherConfig, ports Ports, logf func(format string, args ...any)) *Watcher {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Watcher{cfg: cfg, ports: ports, logf: logf, now: time.Now}
}

// Run plays the watch: queue, invite accept, observe to the match end, leave,
// card. The queue join, the port accept and the leave are typed client
// packets; everything after the accept is reads except the status polls.
func (w *Watcher) Run(deadline time.Time) Result {
	w.start = w.now()
	w.rec = NewRecord(w.cfg.RunID, w.cfg.Map)
	res := Result{Record: w.rec}
	// stop is the one exit: the stop reason in the result and the record,
	// and the card built from them. Every early end goes through it, so no
	// file carries an empty verdict.
	stop := func(reason string) Result {
		res.Stop = reason
		res.Clear = false
		w.rec.StopReason = reason
		w.rec.Clear = false
		w.finish()
		res.Card = Build(w.rec, w.cfg.Thresholds)
		w.log("bg-watch: %s", reason)
		return res
	}
	if w.cfg.TeamOf != 0 {
		return stop("refused: --team-of is reserved")
	}
	list, ok := ListIDForMap(uint32(w.cfg.Map))
	if !ok {
		return stop("refused: unknown map")
	}
	// Queue: CMSG_BATTLEMASTER_JOIN through the typed queue id.
	if err := w.ports.Send(coreBattlemasterJoin(), BuildBattlemasterJoin([]uint64{PackQueueType(list)}, 0, [2]int32{})); err != nil {
		return stop("link-lost")
	}
	w.rec.Add(Event{AtS: w.at(), Kind: EvQueue, Map: w.rec.Map, Text: "queued"})
	w.log("bg-watch: queued for %s", w.rec.Map)
	// Wait for the invite (NEED_CONFIRMATION), polling the status.
	confirm, why := w.waitInvite(deadline)
	if why != "" {
		return stop(why)
	}
	w.rec.Add(Event{AtS: w.at(), Kind: EvInvite, Map: w.rec.Map, Instance: confirm.Header.InstanceID, Text: "invited"})
	w.log("bg-watch: invited to instance %d", confirm.Header.InstanceID)
	// Accept after a random 2-8 s on the world thread, capped below the
	// invite's remaining accept window: the popup is clicked at once when it
	// is about to vanish, never after it.
	if why := w.refuseExpiredInvite(confirm); why != "" {
		return stop(why)
	}
	if err := w.sleepOrDone(deadline, w.acceptDelayFor(w.inviteWindow(confirm))); err != nil {
		return stop("time-budget")
	}
	tick := confirm.Header.Ticket
	if err := w.ports.Send(coreBattlefieldPort(), BuildBattlefieldPort(tick, true)); err != nil {
		return stop("link-lost")
	}
	// The join is recorded only once the server confirms the entry
	// (SMSG_BATTLEFIELD_STATUS_ACTIVE): sending the accept is not entering.
	if why := w.waitActive(deadline); why != "" {
		return stop(why)
	}
	w.rec.Add(Event{AtS: w.at(), Kind: EvJoin, Map: w.rec.Map, Instance: confirm.Header.InstanceID, Text: "entered"})
	w.log("bg-watch: accepted, entering instance %d", confirm.Header.InstanceID)
	end := w.observe(deadline, confirm.Header.InstanceID)
	res.Stop = end
	res.Clear = end == "match-end"
	w.rec.Clear = res.Clear
	if res.Clear {
		w.rec.StopReason = ""
		// The match ended and the character is in WAIT_LEAVE: leave the
		// instance so the next run starts clean.
		if err := w.ports.Send(coreBattlefieldLeave(), BuildBattlefieldLeave()); err != nil {
			w.log("bg-watch: the leave was not sent (%v)", err)
		}
		w.rec.Add(Event{AtS: w.at(), Kind: EvLeave, Instance: confirm.Header.InstanceID, Map: w.rec.Map, Text: "left"})
	} else {
		w.rec.StopReason = end
	}
	w.finish()
	res.Card = Build(w.rec, w.cfg.Thresholds)
	return res
}

// waitInvite waits for NEED_CONFIRMATION: it polls the battlefield status and
// sleeps a poll interval until the invite arrives, the deadline passes, the
// server refuses the join or the link fails. An invite whose accept window
// has already closed is an expiry, not a longer wait. The second result is
// the stop reason, "" on success.
func (w *Watcher) waitInvite(deadline time.Time) (ConfirmStatus, string) {
	poll := w.poll()
	for {
		if inv, ok := w.invitePort(); ok {
			if why := w.refuseExpiredInvite(inv); why != "" {
				return ConfirmStatus{}, why
			}
			return inv, ""
		}
		if why := w.ports.Refused(); why != "" {
			return ConfirmStatus{}, "queue-refused: " + why
		}
		if w.now().After(deadline) {
			return ConfirmStatus{}, "queue-timeout"
		}
		if err := w.ports.PollStatus(); err != nil {
			return ConfirmStatus{}, "link-lost"
		}
		if err := w.ports.Sleep(poll); err != nil {
			return ConfirmStatus{}, "link-lost"
		}
	}
}

// inviteAcceptWindow is the server's accept window when the ports name none
// (INVITE_ACCEPT_WAIT_TIME, Battleground.h:136).
const inviteAcceptWindow = 90 * time.Second

// acceptMargin is the safety margin kept clear of the invite's end: the
// accept must leave the server time to read it.
const acceptMargin = time.Second

// inviteWindow is the accept time left on an invite: the session's Expires
// when it filled one, else the packet's Timeout, else the core's default.
func (w *Watcher) inviteWindow(c ConfirmStatus) time.Duration {
	switch {
	case !c.Expires.IsZero():
		return c.Expires.Sub(w.now())
	case c.Timeout > 0:
		return time.Duration(c.Timeout) * time.Millisecond
	}
	return inviteAcceptWindow
}

// refuseExpiredInvite names the stop when the invite's window has closed:
// the accept is not sent, because the server has already removed the entry.
func (w *Watcher) refuseExpiredInvite(c ConfirmStatus) string {
	if w.inviteWindow(c) <= 0 {
		return "invite-expired"
	}
	return ""
}

// acceptDelayFor caps the human accept delay below the remaining window: a
// late invite is accepted at once rather than after its window closes.
func (w *Watcher) acceptDelayFor(window time.Duration) time.Duration {
	d := w.acceptDelay()
	if limit := window - acceptMargin; d > limit {
		if limit < 0 {
			limit = 0
		}
		d = limit
	}
	return d
}

// waitActive waits for SMSG_BATTLEFIELD_STATUS_ACTIVE after the port accept:
// the queue entry is confirmed by the server, not by sending the accept. A
// refused join stops with its reason; an entry that is not confirmed within
// a few polls (the invite lapsed first) stops as invite-expired.
func (w *Watcher) waitActive(deadline time.Time) string {
	poll := w.poll()
	limit := w.now().Add(3 * poll)
	for {
		if w.ports.Active() {
			return ""
		}
		if why := w.ports.Refused(); why != "" {
			return "queue-refused: " + why
		}
		if w.now().After(deadline) {
			return "time-budget"
		}
		if w.now().After(limit) {
			return "invite-expired"
		}
		if err := w.ports.PollStatus(); err != nil {
			return "link-lost"
		}
		if err := w.ports.Sleep(poll); err != nil {
			return "link-lost"
		}
	}
}

// invitePort reads the invite from ports that serve one (the dry fake); the
// live session serves invites through its packet dispatch instead.
func (w *Watcher) invitePort() (ConfirmStatus, bool) {
	if f, ok := w.ports.(InvitePort); ok {
		return f.Invite(w.until())
	}
	return ConfirmStatus{}, false
}

// until bounds the whole watch.
func (w *Watcher) until() time.Time {
	return w.start.Add(time.Duration(w.cfg.MaxMin+5) * time.Minute)
}

// poll is the status poll interval.
func (w *Watcher) poll() time.Duration {
	poll := time.Duration(w.cfg.PollS) * time.Second
	if poll <= 0 {
		poll = 5 * time.Second
	}
	return poll
}

// observe is the match itself: polls, world-state scores, flag/node events,
// the watcher's deaths, the escort sample, and the end. It sends nothing to
// the module except `.tb bg status` polls (no orders).
func (w *Watcher) observe(deadline time.Time, instance uint32) string {
	poll := w.poll()
	var startX, startY float32
	first := true
	for {
		if w.now().After(deadline) {
			return "time-budget"
		}
		st, err := w.ports.Status()
		if err != nil {
			if errors.Is(err, ErrStatusRefused) {
				return "status-refused"
			}
			return "link-lost"
		}
		inProgress := strings.EqualFold(st.Status, "in-progress")
		w.applyStatus(st, instance, inProgress)
		x, y, z := w.ports.SelfPos()
		if first {
			startX, startY = x, y
			first = false
		}
		if dd := dist2(x, y, startX, startY); dd > w.movedYD {
			w.movedYD = dd
		}
		w.rec.Positions = append(w.rec.Positions, PosSample{AtS: w.at(), X: x, Y: y, Z: z})
		// The escort share comes from the module's own count: the passive
		// observer's visibility would miss a far escort and count an enemy
		// standing on the carrier.
		if inProgress && st.HasFC {
			w.escortSeen += poll.Seconds()
			if st.FCEscorts >= 2 {
				w.escortGood += poll.Seconds()
			}
		}
		w.applyWorld(instance)
		if end, done := w.checkEnd(instance); done {
			return end
		}
		if err := w.ports.Sleep(poll); err != nil {
			return "link-lost"
		}
	}
}

// applyStatus folds one `.tb bg status` poll into the record. The idle rule
// reads every in-progress poll: a role's longest still span over the match
// is the record's row, not the last poll's.
func (w *Watcher) applyStatus(st StatusReport, instance uint32, inProgress bool) {
	if st.Team != "" {
		w.team = strings.ToLower(st.Team)
		w.rec.Team = w.team
	}
	w.rec.Alliance = st.Alliance
	w.rec.Horde = st.Horde
	if st.Deserter {
		w.rec.Deserter = true
	}
	if st.Fallback {
		w.rec.Add(Event{AtS: w.at(), Kind: EvFallback, Instance: instance, Map: w.rec.Map, Text: "fallback"})
	}
	if !inProgress {
		return
	}
	if len(w.botRows) != len(st.Bots) {
		rows := make([]BotRow, len(st.Bots))
		copy(rows, w.botRows)
		w.botRows = rows
	}
	for i, row := range st.Bots {
		// The rows come in the module's order, so a row's place is its
		// identity: a role may repeat, and the card wants the longest still
		// span the bot showed while the match was in progress.
		if prev := w.botRows[i]; prev.Role == row.Role && prev.MoveAgeS > row.MoveAgeS {
			row.MoveAgeS = prev.MoveAgeS
		}
		w.botRows[i] = row
	}
	w.rec.Bots = append(w.rec.Bots[:0], w.botRows...)
}

// applyWorld folds the world-state rows into flag/node events and records
// the watcher's own death.
func (w *Watcher) applyWorld(instance uint32) {
	states := w.ports.WorldStates()
	w.recordScores(states)
	if w.rec.Map == "wsg" {
		w.recordWSGFlags(states, instance)
	} else {
		w.recordABNodes(states, instance)
	}
	if w.ports.Died() {
		w.rec.Add(Event{AtS: w.at(), Kind: EvDeath, Instance: instance, Map: w.rec.Map, Bot: "watcher", Team: "self", Text: "the watcher died"})
	}
}

// recordScores appends one score sample from the world states.
func (w *Watcher) recordScores(states map[int32]int32) {
	var s ScoreSample
	s.AtS = w.at()
	if w.rec.Map == "wsg" {
		s.Alliance = int(states[WSCapAlliance])
		s.Horde = int(states[WSCapHorde])
	} else {
		s.Alliance = int(states[ABResourcesAlliance])
		s.Horde = int(states[ABResourcesHorde])
		s.BasesA = int(states[ABOccupiedAllies])
		s.BasesH = int(states[ABOccupiedHorde])
	}
	if n := len(w.rec.Scores); n == 0 || w.rec.Scores[n-1] != s {
		w.rec.Scores = append(w.rec.Scores, s)
	}
}

// recordWSGFlags derives the flag events from 1545/1546: 0 base, 1 carried,
// -1 ground (battleground_warsong_gulch.cpp:661-662). A transition to 1 is a
// pick, to -1 a drop, from ground back to base a return. A carried flag
// coming home is a capture, and the 1581/1582 counters are its only source
// (CaptureFlag also writes the flag state to 0 and a carrier logout resets
// it the same way, :1046-1092, :815-821), so the 1 -> 0 step emits nothing.
func (w *Watcher) recordWSGFlags(states map[int32]int32, instance uint32) {
	w.checkCapCounter(states, WSCapHorde, "silverwing", "horde", instance)
	w.checkCapCounter(states, WSCapAlliance, "warsong", "alliance", instance)
	type side struct {
		key    int32
		flag   string
		teamOf string
	}
	for _, sd := range []side{{WSFlagAlliance, "silverwing", "alliance"}, {WSFlagHorde, "warsong", "horde"}} {
		v, ok := states[sd.key]
		if !ok {
			continue
		}
		prev := w.prevFlag(sd.key)
		if prev == v {
			continue
		}
		w.setPrevFlag(sd.key, v)
		// The first sight of a flag already home is the baseline, not a
		// return: only transitions between known states count.
		if prev == -99 && v == 0 {
			continue
		}
		switch v {
		case 1:
			w.rec.Add(Event{AtS: w.at(), Kind: EvFlagPick, Instance: instance, Map: w.rec.Map, Flag: sd.flag, Team: sd.teamOf, Text: "picked"})
			delete(w.droppedAt, sd.flag)
			delete(w.touched, sd.flag)
		case -1:
			w.rec.Add(Event{AtS: w.at(), Kind: EvFlagDrop, Instance: instance, Map: w.rec.Map, Flag: sd.flag, Team: sd.teamOf, Text: "dropped"})
			if w.droppedAt == nil {
				w.droppedAt = map[string]float64{}
			}
			w.droppedAt[sd.flag] = w.at()
		case 0:
			if prev == -1 {
				w.rec.Add(Event{AtS: w.at(), Kind: EvFlagReturn, Instance: instance, Map: w.rec.Map, Flag: sd.flag, Team: sd.teamOf, Text: "returned"})
			}
			delete(w.droppedAt, sd.flag)
			delete(w.touched, sd.flag)
		}
	}
	// A dropped own flag untouched for TouchS seconds counts once per drop.
	if w.team == "" {
		return
	}
	for flag, at := range w.droppedAt {
		if !ownFlag(flag, w.team) || w.touched[flag] || w.at()-at < w.cfg.Thresholds.TouchS {
			continue
		}
		if w.touched == nil {
			w.touched = map[string]bool{}
		}
		w.rec.TouchedDropped++
		w.touched[flag] = true
	}
}

// ownFlag reports whether flag is the watcher's team's own flag: the
// Alliance flag is Silverwing's, the Horde flag Warsong's.
func ownFlag(flag, team string) bool {
	switch team {
	case "alliance":
		return flag == "silverwing"
	case "horde":
		return flag == "warsong"
	}
	return false
}

// checkCapCounter emits a capture when a side's capture counter rises.
// The counters are the authority on captures (Battleground::UpdateTeamScore
// writes 1581/1582 on every capture,
// battleground_warsong_gulch.cpp:340-346): an Alliance capture takes the
// Warsong flag home, a Horde capture the Silverwing flag.
func (w *Watcher) checkCapCounter(states map[int32]int32, key int32, flag, team string, instance uint32) {
	v, ok := states[key]
	if !ok {
		return
	}
	if w.caps == nil {
		w.caps = map[int32]int32{}
	}
	prev, known := w.caps[key]
	w.caps[key] = v
	if !known || v <= prev {
		return
	}
	for range int(v - prev) {
		w.rec.Add(Event{AtS: w.at(), Kind: EvFlagCapture, Instance: instance, Map: w.rec.Map, Flag: flag, Team: team, Text: "captured"})
	}
}

// recordABNodes derives node events from the five node states (ABNode), from
// the core's own transitions (HandleBannerClick,
// battleground_arathi_basin.cpp:980-1047, and the contested timer,
// :800-818): neutral -> contested is an assault, contested -> the same
// side's occupied state is the timer's occupation, occupied -> the enemy's
// contested state is an assault, contested by the enemy -> the original
// owner's occupied state is a defend, and contested A -> contested H is a
// re-assault. A node the watcher's team held that the enemy then occupies
// with no bot of the team at it counts as lost with no defender. The first
// sight of a node is the baseline: a match loaded mid-flight already shows
// contested or occupied nodes the watcher never saw change, and no event is
// derived from them.
func (w *Watcher) recordABNodes(states map[int32]int32, instance uint32) {
	for node := range 5 {
		got := ABNode(states, node)
		prev := w.prevNode(node)
		name := ABNodeNames[node]
		if prev == abUnseen {
			w.setPrevNode(node, got)
			if isOccupied(got) {
				w.owned[node] = sideName(got)
			}
			continue
		}
		if prev == got {
			continue
		}
		w.setPrevNode(node, got)
		switch {
		case isContested(got) && !isContested(prev):
			w.rec.Add(Event{AtS: w.at(), Kind: EvNodeAssault, Instance: instance, Map: w.rec.Map, Node: name, Text: "assaulted (" + got.String() + ")"})
		case isContested(got) && isContested(prev) && !sameSide(got, prev):
			w.rec.Add(Event{AtS: w.at(), Kind: EvNodeAssault, Instance: instance, Map: w.rec.Map, Node: name, Text: "re-assaulted (" + got.String() + ")"})
		case isOccupied(got) && isContested(prev):
			if sameSide(got, prev) {
				w.rec.Add(Event{AtS: w.at(), Kind: EvNodeOccupied, Instance: instance, Map: w.rec.Map, Node: name, Text: "occupied (" + got.String() + ")"})
			} else {
				w.rec.Add(Event{AtS: w.at(), Kind: EvNodeDefend, Instance: instance, Map: w.rec.Map, Node: name, Text: "defended (" + got.String() + ")"})
			}
		case isOccupied(got):
			w.rec.Add(Event{AtS: w.at(), Kind: EvNodeOccupied, Instance: instance, Map: w.rec.Map, Node: name, Text: "occupied (" + got.String() + ")"})
		}
		// The loss is the enemy's occupation, not its assault: a node
		// assaulted and defended back was never lost.
		if side := sideName(got); isOccupied(got) && w.team != "" && side != w.team && w.owned[node] == w.team && w.defendersAt(name) == 0 {
			w.rec.LostNoDefend++
		}
		if isOccupied(got) {
			w.owned[node] = sideName(got)
		}
	}
}

// sideName names the side an occupied or contested state belongs to.
func sideName(s ABNodeState) string {
	switch s {
	case ABAllianceHeld, ABAllianceContested:
		return "alliance"
	case ABHordeHeld, ABHordeContested:
		return "horde"
	}
	return ""
}

// defendersAt counts the alive bots the last status poll assigned to node.
func (w *Watcher) defendersAt(node string) int {
	n := 0
	for _, row := range w.rec.Bots {
		if row.Node == node && row.Alive {
			n++
		}
	}
	return n
}

// isContested reports a contested AB state.
func isContested(s ABNodeState) bool {
	return s == ABAllianceContested || s == ABHordeContested
}

// isOccupied reports an occupied AB state.
func isOccupied(s ABNodeState) bool {
	return s == ABAllianceHeld || s == ABHordeHeld
}

// sameSide reports two AB states of the same faction.
func sameSide(a, b ABNodeState) bool {
	return (a == ABAllianceHeld || a == ABAllianceContested) && (b == ABAllianceHeld || b == ABAllianceContested) ||
		(a == ABHordeHeld || a == ABHordeContested) && (b == ABHordeHeld || b == ABHordeContested)
}

// prevFlag/prevNode hold the last derived states. The watcher is
// single-goroutine, so plain maps do.
func (w *Watcher) prevFlag(key int32) int32 {
	if w.flags == nil {
		return -99
	}
	v, ok := w.flags[key]
	if !ok {
		return -99
	}
	return v
}

func (w *Watcher) setPrevFlag(key int32, v int32) {
	if w.flags == nil {
		w.flags = map[int32]int32{}
	}
	w.flags[key] = v
}

// abUnseen marks a node the watcher has not observed yet: the first
// snapshot is the baseline, not a transition.
const abUnseen ABNodeState = ABNeutral - 1

func (w *Watcher) prevNode(n int) ABNodeState {
	if w.nodes == nil {
		return abUnseen
	}
	v, ok := w.nodes[n]
	if !ok {
		return abUnseen
	}
	return v
}

func (w *Watcher) setPrevNode(n int, s ABNodeState) {
	if w.nodes == nil {
		w.nodes = map[int]ABNodeState{}
	}
	w.nodes[n] = s
}

// checkEnd reports the match end: the session's match-end marker (PVP match
// complete / WAIT_LEAVE) arrives through the EndPort. The duration is the
// server's in every case; a winner-empty end with the premature marker is
// the premature finish.
func (w *Watcher) checkEnd(instance uint32) (string, bool) {
	if f, ok := w.ports.(EndPort); ok {
		if end, done := f.Ended(); done {
			w.rec.Winner = end.Winner
			w.rec.DurationS = end.DurationS
			if end.Winner == "" && end.Premature {
				w.rec.Premature = true
			}
			// A complete-state initialize end carries no result packet:
			// its length is known, its winner is not, and the scores of a
			// match already over must not name one.
			w.noResult = end.NoResult
			w.rec.Add(Event{AtS: w.at(), Kind: EvEnd, Instance: instance, Map: w.rec.Map, Text: end.Text})
			return "match-end", true
		}
	}
	return "", false
}

// finish closes the record for every stop: the position and escort samples,
// the duration when the server gave none, and the winner from the last
// scores when the end named none (a normal score end without a winner bus
// message). A premature or drawn end keeps no winner.
func (w *Watcher) finish() {
	w.rec.MovedYD = w.movedYD
	if w.escortSeen > 0 {
		w.rec.EscortShare = w.escortGood / w.escortSeen
		w.rec.EscortSelected = w.escortSeen
	} else {
		w.rec.EscortShare = -1
	}
	if w.rec.DurationS == 0 {
		w.rec.DurationS = w.at()
	}
	if w.rec.Winner != "" || !w.rec.Clear || w.rec.Premature || w.noResult {
		return
	}
	if n := len(w.rec.Scores); n > 0 {
		s := w.rec.Scores[n-1]
		switch {
		case s.Alliance > s.Horde:
			w.rec.Winner = "alliance"
		case s.Horde > s.Alliance:
			w.rec.Winner = "horde"
		}
	}
}

// at is the seconds since the watch started.
func (w *Watcher) at() float64 {
	if w.start.IsZero() {
		return 0
	}
	return w.now().Sub(w.start).Seconds()
}

// sleepOrDone sleeps d unless the deadline passes first.
func (w *Watcher) sleepOrDone(deadline time.Time, d time.Duration) error {
	end := w.now().Add(d)
	if end.After(deadline) {
		return errors.New("bg: deadline passed")
	}
	return w.ports.Sleep(d)
}

// acceptDelay is the invite accept delay: a random 2-8 s on the world
// thread, like a person who just saw the popup.
func (w *Watcher) acceptDelay() time.Duration {
	return 2*time.Second + time.Duration(rand.IntN(6001))*time.Millisecond
}

// dist2 is the 2D distance in yards.
func dist2(x0, y0, x1, y1 float32) float64 {
	dx, dy := float64(x0-x1), float64(y0-y1)
	return math.Sqrt(dx*dx + dy*dy)
}

// log prints one watch line.
func (w *Watcher) log(format string, args ...any) {
	if w.logf != nil {
		w.logf(format, args...)
	}
}

// InvitePort is served by ports that hand the watcher its decoded invite
// (the dry fake); the live session decodes NEED_CONFIRMATION in its packet
// dispatch and hands it through the same seam.
type InvitePort interface {
	Invite(until time.Time) (ConfirmStatus, bool)
}

// EndPort is served by ports that hand the watcher the match end (the dry
// fake); the live session decodes PVP_MATCH_COMPLETE / WAIT_LEAVE the same
// way.
type EndPort interface {
	Ended() (WatchEnd, bool)
}

// WatchEnd is the decoded match end.
type WatchEnd struct {
	Winner    string
	DurationS float64
	Premature bool
	// NoResult says the length came from a complete-state initialize
	// because the result packet never arrived: the winner is unknown and
	// finish must not infer one from the last score sample.
	NoResult bool
	Text     string
}

// core opcodes the watcher sends (internal/forever/wire).
func coreBattlemasterJoin() uint32 { return wire.CMSGBattlemasterJoin }

func coreBattlefieldPort() uint32 { return wire.CMSGBattlefieldPort }

func coreBattlefieldLeave() uint32 { return wire.CMSGBattlefieldLeave }
