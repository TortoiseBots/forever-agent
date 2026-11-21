package tbcmd

// The run commands of a server module that runs dungeon plans: start, stop, pause, resume, status and events. The
// module runs the plan itself; the client only starts it, reads its records and asks it to stop. Each call is built
// from typed arguments (a character name, a plan name, reason words and an event sequence), token-matched, and read
// through the same strict decoder as the rest of this layer (v2.go): a bad percent escape, a repeated key, a count
// that disagrees, an unknown state word or a missing required key is an error, never a zero.
//
// The `run` record is the module's progress (state, step, leg, counters, bosses it has read); the `event` record is
// one thing that happened. The records are documented shapes with optional keys: `leg`/`legname`/`kind` are absent
// past the last step, `why` exactly when the run is paused or stopped, and `ev` when there is no event yet. Keys this
// layer does not type are kept in Extra, never refused.

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

// RunBoss is one plan boss of a run record: the entry and whether the module read it as dead.
type RunBoss struct {
	Entry uint32
	Alive bool
	// State is the word the module sent: alive or dead.
	State string
}

// RunState is one `run` record, parsed whole.
type RunState struct {
	Lead  string
	Plan  string
	State string // running, paused, done, stopped
	Step  int
	Steps int
	// Leg, LegName and Kind describe the step; they are absent once the run is past the last step.
	Leg     int
	HasLeg  bool
	LegName string
	HasLegN bool
	Kind    string
	HasKind bool
	Phase   string
	Note    string
	Pull    int
	Pulls   int
	Bosses  []RunBoss
	Deaths  int
	Wipes   int
	Pauses  int
	Elapsed int
	Why     string
	HasWhy  bool
	Ev      int
	HasEv   bool
	Extra   map[string]string
}

// RunReport is a whole run answer: the records and the end line. Pending reports a stop that waits for the fight to
// end (the end line's pending key); HasPending says whether the module sent it.
type RunReport struct {
	Runs       []RunState
	Verb       string
	Pending    bool
	HasPending bool
	End        map[string]string
	Raw        []string
}

// RunEvent is one `event` record: seq and t are seconds into the run and its position in the ring.
type RunEvent struct {
	Seq     int
	T       int
	Kind    string
	Step    int
	HasStep bool
	Leg     int
	HasLeg  bool
	Unit    uint32
	HasUnit bool
	Why     string
	// Extra holds every key this model does not know, decoded. Unknown keys are not an error.
	Extra map[string]string
}

// RunEventsReport is a whole events answer and the newest sequence it carries.
type RunEventsReport struct {
	Lead    string
	Events  []RunEvent
	Last    int
	HasLast bool
	Verb    string
	End     map[string]string
	Raw     []string
}

// runRecordKeys is the run record's typed set; anything else goes to Extra.
var runRecordKeys = map[string]bool{
	"lead": true, "plan": true, "state": true, "step": true, "steps": true, "leg": true, "legname": true,
	"kind": true, "phase": true, "note": true, "pull": true, "pulls": true, "boss": true, "deaths": true,
	"wipes": true, "pauses": true, "elapsed": true, "why": true, "ev": true,
}

// eventRecordKeys is the event record's typed set.
var eventRecordKeys = map[string]bool{
	"seq": true, "t": true, "kind": true, "step": true, "leg": true, "unit": true, "why": true,
}

// RunStart starts a run of a plan led by a bot: `.tb run start <leader> <plan>`.
func (c *Client) RunStart(ctx context.Context, leader, plan string) (RunReport, error) {
	name, err := runLeader(leader)
	if err != nil {
		return RunReport{}, err
	}
	p, err := argument("plan", plan)
	if err != nil {
		return RunReport{}, err
	}
	rep, err := c.call(ctx, ".tb run start "+name+" "+p, "run-end")
	if err != nil {
		return RunReport{}, err
	}
	out, err := parseRunAnswer(rep, "start", name)
	if err != nil {
		return out, err
	}
	if len(out.Runs) != 1 {
		return out, fmt.Errorf("%w: run start carried %d run record(s), want 1", ErrBadReport, len(out.Runs))
	}
	// The one record is the run the command asked for: the same plan, and running. A zero-record, a several-record
	// or a record naming another plan or state is a report the client must not act on.
	st := out.Runs[0]
	if st.Plan != p {
		return out, fmt.Errorf("%w: run start for plan %q, the command named %q", ErrBadReport, st.Plan, p)
	}
	if st.State != "running" {
		return out, fmt.Errorf("%w: run start state=%q, want running", ErrBadReport, st.State)
	}
	return out, nil
}

// RunStop ends a run for good, with the reason words the module records: `.tb run stop <leader> [reason words]`.
func (c *Client) RunStop(ctx context.Context, leader, reason string) (RunReport, error) {
	name, err := runLeader(leader)
	if err != nil {
		return RunReport{}, err
	}
	line := ".tb run stop " + name
	if words, err := runReasonWords(reason); err != nil {
		return RunReport{}, err
	} else if len(words) > 0 {
		line += " " + strings.Join(words, " ")
	}
	rep, err := c.call(ctx, line, "run-end")
	if err != nil {
		return RunReport{}, err
	}
	return parseRunAnswer(rep, "stop", name)
}

// RunPause pauses a running run: `.tb run pause <leader>`.
func (c *Client) RunPause(ctx context.Context, leader string) (RunReport, error) {
	name, err := runLeader(leader)
	if err != nil {
		return RunReport{}, err
	}
	rep, err := c.call(ctx, ".tb run pause "+name, "run-end")
	if err != nil {
		return RunReport{}, err
	}
	return parseRunAnswer(rep, "pause", name)
}

// RunResume is the policy's checked resume: `.tb run resume <leader>`.
func (c *Client) RunResume(ctx context.Context, leader string) (RunReport, error) {
	name, err := runLeader(leader)
	if err != nil {
		return RunReport{}, err
	}
	rep, err := c.call(ctx, ".tb run resume "+name, "run-end")
	if err != nil {
		return RunReport{}, err
	}
	return parseRunAnswer(rep, "resume", name)
}

// RunStatus reads one run's record, or every run's with "all": `.tb run status <leader|all>`.
func (c *Client) RunStatus(ctx context.Context, leader string) (RunReport, error) {
	target := "all"
	if !strings.EqualFold(strings.TrimSpace(leader), "all") {
		name, err := runLeader(leader)
		if err != nil {
			return RunReport{}, err
		}
		target = name
	}
	rep, err := c.call(ctx, ".tb run status "+target, "run-end")
	if err != nil {
		return RunReport{}, err
	}
	return parseRunAnswer(rep, "status", "")
}

// RunEvents reads the event ring above a sequence: `.tb run events <leader> [since=<seq>]`.
func (c *Client) RunEvents(ctx context.Context, leader string, since int, hasSince bool) (RunEventsReport, error) {
	name, err := runLeader(leader)
	if err != nil {
		return RunEventsReport{}, err
	}
	if hasSince && (since < 0 || since > math.MaxInt32) {
		return RunEventsReport{}, fmt.Errorf("%w: since %d is not an event sequence", ErrBadValue, since)
	}
	line := ".tb run events " + name
	if hasSince {
		line += fmt.Sprintf(" since=%d", since)
	}
	// A ring of 200 events plus its two markers is the command's own bound; the collector caps it at 256 lines.
	rep, err := c.callMax(ctx, line, "run-end", 256)
	if err != nil {
		return RunEventsReport{}, err
	}
	return parseRunEvents(rep, name)
}

// runLeader normalises a character name the module's command takes.
func runLeader(leader string) (string, error) {
	name, err := chat.NormaliseName(leader)
	if err != nil {
		return "", fmt.Errorf("tbcmd: run leader: %w", err)
	}
	return name, nil
}

// runReasonWords checks a stop's reason words: the module takes them as separate words, so each one must be an
// argument word and the whole reason is bounded.
func runReasonWords(reason string) ([]string, error) {
	if reason == "" {
		return nil, nil
	}
	words := strings.Fields(reason)
	if len(words) > 16 {
		return nil, fmt.Errorf("tbcmd: a run stop reason has %d words (at most 16)", len(words))
	}
	total := 0
	for _, w := range words {
		if _, err := argument("reason", w); err != nil {
			return nil, err
		}
		total += len(w) + 1
	}
	if total > 120 {
		return nil, fmt.Errorf("tbcmd: a run stop reason is %d bytes (at most 120)", total)
	}
	return words, nil
}

// parseRunAnswer reads a run-begin/run-end reply with `run` records. verb is the command word the end line must
// repeat; target, when set, is the leader every record must agree with.
func parseRunAnswer(rep *command.Reply, verb, target string) (RunReport, error) {
	out := RunReport{}
	r, err := parseReport(rep, "run-begin", "run-end", "run", "lead")
	if err != nil {
		return out, err
	}
	end := r.End
	out.Raw = append([]string(nil), rep.Raw...)
	if out.Verb, err = end.requireText("verb"); err != nil {
		return out, err
	}
	if out.Verb != verb {
		return out, fmt.Errorf("%w: run-end verb=%q, the command was %q", ErrBadReport, out.Verb, verb)
	}
	if out.Pending, out.HasPending, err = end.optionalBool("pending"); err != nil {
		return out, err
	}
	out.End = decodedEnd(end)
	for _, rec := range r.Records {
		st, err := parseRunState(rec)
		if err != nil {
			return out, err
		}
		if target != "" && !strings.EqualFold(st.Lead, target) {
			return out, fmt.Errorf("%w: run record for %q, the command named %q", ErrBadReport, st.Lead, target)
		}
		out.Runs = append(out.Runs, st)
	}
	return out, nil
}

// parseRunState reads one run record.
func parseRunState(rec record) (RunState, error) {
	var out RunState
	var err error
	if out.Lead, err = rec.requireText("lead"); err != nil {
		return out, err
	}
	if out.Plan, err = rec.requireText("plan"); err != nil {
		return out, err
	}
	if out.State, err = requiredWord(rec, "state", "running", "paused", "done", "stopped"); err != nil {
		return out, err
	}
	if out.Step, err = rec.requireInt("step", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Steps, err = rec.requireInt("steps", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Leg, out.HasLeg, err = rec.optionalInt("leg", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.LegName, out.HasLegN, err = rec.text("legname"); err != nil {
		return out, err
	}
	if out.Kind, out.HasKind, err = rec.text("kind"); err != nil {
		return out, err
	}
	if out.HasKind && !stepKindWord(out.Kind) {
		return out, fmt.Errorf("%w: kind=%q is not a plan step kind", ErrBadValue, out.Kind)
	}
	if out.Phase, err = requiredWord(rec, "phase", phaseWords()...); err != nil {
		return out, err
	}
	if out.Note, err = rec.requireText("note"); err != nil {
		return out, err
	}
	if out.Pull, err = rec.requireInt("pull", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Pulls, err = rec.requireInt("pulls", 0, math.MaxInt32); err != nil {
		return out, err
	}
	raw, ok := rec.rawValue("boss")
	if !ok {
		return out, rec.missing("boss")
	}
	bosses, err := parseBossList("boss", raw)
	if err != nil {
		return out, err
	}
	for _, b := range bosses {
		out.Bosses = append(out.Bosses, RunBoss(b))
	}
	if out.Deaths, err = rec.requireInt("deaths", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Wipes, err = rec.requireInt("wipes", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Pauses, err = rec.requireInt("pauses", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Elapsed, err = rec.requireInt("elapsed", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Why, out.HasWhy, err = rec.text("why"); err != nil {
		return out, err
	}
	if out.Ev, out.HasEv, err = rec.optionalInt("ev", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Extra, err = rec.extra(runRecordKeys); err != nil {
		return out, err
	}
	return out, nil
}

// parseRunEvents reads a run-begin/run-end reply with `event` records.
func parseRunEvents(rep *command.Reply, leader string) (RunEventsReport, error) {
	out := RunEventsReport{}
	r, err := parseReport(rep, "run-begin", "run-end", "event", "seq")
	if err != nil {
		return out, err
	}
	end := r.End
	out.Raw = append([]string(nil), rep.Raw...)
	if out.Verb, err = end.requireText("verb"); err != nil {
		return out, err
	}
	if out.Verb != "events" {
		return out, fmt.Errorf("%w: run-end verb=%q, the command was %q", ErrBadReport, out.Verb, "events")
	}
	if out.Lead, err = end.requireText("lead"); err != nil {
		return out, err
	}
	if !strings.EqualFold(out.Lead, leader) {
		return out, fmt.Errorf("%w: events for %q, the command named %q", ErrBadReport, out.Lead, leader)
	}
	if out.Last, out.HasLast, err = end.optionalInt("last", 1, math.MaxInt32); err != nil {
		return out, err
	}
	out.End = decodedEnd(end)
	for _, rec := range r.Records {
		ev, err := parseRunEvent(rec)
		if err != nil {
			return out, err
		}
		out.Events = append(out.Events, ev)
	}
	for i := 1; i < len(out.Events); i++ {
		if out.Events[i].Seq <= out.Events[i-1].Seq {
			return out, fmt.Errorf("%w: events are not in sequence (%d after %d)", ErrBadReport,
				out.Events[i].Seq, out.Events[i-1].Seq)
		}
	}
	if out.HasLast && len(out.Events) > 0 && out.Events[len(out.Events)-1].Seq > out.Last {
		return out, fmt.Errorf("%w: last=%d, the newest event is %d", ErrBadReport, out.Last, out.Events[len(out.Events)-1].Seq)
	}
	return out, nil
}

// parseRunEvent reads one event record.
func parseRunEvent(rec record) (RunEvent, error) {
	var out RunEvent
	var err error
	if out.Seq, err = rec.requireInt("seq", 1, math.MaxInt32); err != nil {
		return out, err
	}
	if out.T, err = rec.requireInt("t", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Kind, err = requiredWord(rec, "kind", eventKinds()...); err != nil {
		return out, err
	}
	if out.Step, out.HasStep, err = rec.optionalInt("step", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Leg, out.HasLeg, err = rec.optionalInt("leg", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Unit, out.HasUnit, err = rec.optionalUint32("unit"); err != nil {
		return out, err
	}
	if out.Why, err = rec.requireText("why"); err != nil {
		return out, err
	}
	if out.Extra, err = rec.extra(eventRecordKeys); err != nil {
		return out, err
	}
	return out, nil
}

// optionalUint32 reads an optional positive 32-bit number (a creature entry).
func (r record) optionalUint32(key string) (n uint32, present bool, err error) {
	s, ok, err := r.text(key)
	if err != nil || !ok {
		return 0, false, err
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil || v == 0 {
		return 0, true, fmt.Errorf("%w: %s=%q is not an entry", ErrBadValue, key, s)
	}
	return uint32(v), true, nil
}

// optionalMapID reads an optional map id. 0 is a real map (Eastern Kingdoms), so only an absent key means no map.
func (r record) optionalMapID(key string) (n uint32, present bool, err error) {
	s, ok, err := r.text(key)
	if err != nil || !ok {
		return 0, false, err
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, true, fmt.Errorf("%w: %s=%q is not a map id", ErrBadValue, key, s)
	}
	return uint32(v), true, nil
}

// phaseWords are the policy's phase words, in a stable order for the error text.
func phaseWords() []string {
	return []string{"begin", "gather", "walk", "pull", "task", "rest", "wait", "hold"}
}

// eventKinds are the module's event kinds, in a stable order.
func eventKinds() []string {
	return []string{"start", "step", "pull", "fight-end", "rest", "death", "wipe", "revive", "pause", "resume", "done", "stop"}
}

// stepKindWord says whether a run record's kind is a plan step kind: a lower-case word, optionally with one
// underscore (move, move_direct, pull, boss, and the module's other step words). The vocabulary is the plan's data,
// so an unknown step word is kept, not refused; only the shape is checked.
func stepKindWord(s string) bool {
	if s == "" || len(s) > 24 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c == '_' || (c >= '0' && c <= '9'):
		default:
			return false
		}
	}
	return true
}

// decodedEnd keeps the end line's keys, decoded, for the caller's own use (the tick counters of status all, the
// stayed count of a stop).
func decodedEnd(rec record) map[string]string {
	out := make(map[string]string, len(rec.keys))
	for _, k := range rec.keys {
		if s, err := decodePart(rec.values[k]); err == nil {
			out[k] = s
		}
	}
	return out
}
