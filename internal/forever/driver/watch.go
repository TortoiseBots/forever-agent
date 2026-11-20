package driver

// The watch machine: `forever dungeon-watch`. A server module runs the dungeon by itself (its tank bot leads the
// party); the client's job is to watch, not to lead. The watcher brings the party in exactly as a run does
// (PREPARE, then TRAVEL/ENTER), starts the module's run with one typed action, and from then on sends NO order: an
// order would pause the module's run. It polls the module's status and event ring at a stated interval, prints the
// progress (leg, step, pulls, deaths, wipes, pauses with their reasons, the bosses the instance report gives), and
// keeps the safety budgets:
//
//   - the watchdog: no new event for cfg.Watchdog -> pause the run with a reason, report, and end the watch;
//   - the time budget (Limits.MaxMinutes) and the wipe budget (Limits.MaxWipes);
//   - the practice rescue: a run paused on a wipe for Limits.WatchRescue (300 s) gets one `.tb revive party`
//     (PRACTICE) and one `.tb run resume`, at most Limits.MaxPractice times.
//
// Every send goes through act and its gate, like every other setup action. The stop path sends `.tb run stop`, waits
// (bounded) for the module's state to reach stopped, and writes the run's summary: the dungeon-run format, extended
// with the module's own counters. A lost link ends the watch at once and nothing is sent after it.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// watchRun is the watcher's own bookkeeping over the module's records.
type watchRun struct {
	record tbcmd.RunState
	have   bool

	lastSeq   int
	lastEvent time.Time
	events    int

	state       string
	pausedSince time.Time
	// onWipe marks a pause that follows a wipe: the policy pauses there, and the practice rescue is the watcher's
	// one remedy. wipePending is a wipe seen but whose own pause has not been read yet; wipeSeq is that wipe's event
	// sequence, so the pause must be the very next event. A resume, a running record or any other event or status
	// reason clears both.
	onWipe      bool
	wipePending bool
	wipeSeq     int
	wipeAt      time.Time
	revived     bool
	reviveAt    time.Time
	rescues     int
	pauseLog    []PauseNote
	pauseSeen   map[int]bool
	bosses      map[uint32]string
	bossAt      time.Time
	lastLine    string
	lastNote    string
	// stopUnconfirmed says the cleanup phase ended without the module's record reaching stopped: the stop may not
	// have reached it (or it could not stop yet), so the result is not a clean end.
	stopUnconfirmed bool
	// statusBlind and eventsBlind are each read's own blind rule: successive failures and the time of the first of
	// them, so a good status read never hides failing event reads, nor the other way round.
	statusBlind watchBlind
	eventsBlind watchBlind

	// The report card's evidence: the module's own event times grouped by leg and step, the deaths, the rescues and
	// the party slot each unit id is. Units holds role words, never character names.
	legs      map[int]*LegRecord
	legSeen   map[int]bool
	steps     map[[2]int]*StepRecord
	stepSeen  map[[2]int]bool
	deaths    []DeathRecord
	rescueLog []RescueNote
	units     map[uint32]string
	elapsed   int
}

// Watch runs the watch machine and returns what it saw. The caller owns the ports; r.cfg.Watch and r.cfg.Leader say
// what to watch.
func (r *Run) Watch(ctx context.Context) Result {
	r.start = r.clock.Now()
	r.lastProgress = r.start
	r.refreshSelf()
	w := &watchRun{
		pauseSeen: map[int]bool{}, bosses: map[uint32]string{},
		legs: map[int]*LegRecord{}, legSeen: map[int]bool{},
		steps: map[[2]int]*StepRecord{}, stepSeen: map[[2]int]bool{},
	}
	if r.configBroken() {
		r.stop("config")
		return r.watchResult(w)
	}
	r.legs = nil
	st := StatePrepare
	r.states = append(r.states, st)
	st = r.prepare(ctx)
	w.units = r.watchUnits()
	if r.stopReason == "" && st == StateTravel {
		r.states = append(r.states, StateTravel)
		st = r.travel(ctx)
	}
	if r.stopReason == "" && st == StateEnter {
		r.states = append(r.states, StateEnter)
		st = r.enter(ctx)
	}
	if r.stopReason == "" && st != StateLegWalk {
		r.stop("enter")
	}
	if r.stopReason == "" {
		r.states = append(r.states, "WATCH")
		r.watchStart(ctx, w)
		if r.stopReason == "" {
			r.watchBosses(ctx, w, false)
			r.watchLoop(ctx, w)
		}
	}
	r.watchFinish(ctx, w)
	r.states = append(r.states, StateStop)
	return r.watchResult(w)
}

// configBroken refuses a watch it cannot run before anything is sent.
func (r *Run) configBroken() bool {
	if r.cfg.Leader == "" || r.route == nil || r.route.Name == "" {
		return true
	}
	if r.cfg.Enter != "tele" && r.cfg.Enter != "portal" && r.cfg.Enter != "none" {
		return true
	}
	return false
}

// watchStart starts the module's run: `.tb run start <leader> <plan>`, a fight-starting action (the run pulls from
// then on), retried while its gate is shut and ended with the module's own refusal code when it refuses.
func (r *Run) watchStart(ctx context.Context, w *watchRun) {
	deadline := r.now().Add(r.lim.Enter)
	for r.stopReason == "" {
		rep, why := r.act(ctx, Action{Kind: ActionRunStart, Who: r.cfg.Leader, Plan: r.route.Name})
		switch why {
		case "":
			w.lastEvent = r.now()
			if rep.Run != nil {
				r.applyRun(w, *rep.Run)
				if rep.Run.HasLeg {
					// The module's run starts in its first leg: that leg's time starts at the run's zero.
					w.legSeen[rep.Run.Leg] = true
					w.leg(rep.Run.Leg).FirstS = 0
				}
				if rep.Run.HasEv {
					w.lastSeq = rep.Run.Ev
				}
			}
			r.progress()
			r.logf("watch: started plan=%s state=%s step=%d/%d", w.record.Plan, w.record.State, w.record.Step, w.record.Steps)
			return
		case "link", "stop":
			return
		case "error":
			code := rep.Code
			if code == "" {
				code = "refused"
			}
			r.stop("start:" + code)
			return
		}
		if !actionHold(why) && why != "budget" && why != "wipes" {
			r.stop("start:" + reasonCode(why))
			return
		}
		if !r.now().Before(deadline) {
			r.stop("start:" + reasonCode(why))
			return
		}
		if !r.sleep(ctx, r.cfg.Poll) {
			return
		}
	}
}

// watchLoop is the watch itself: status, events, the budgets, the practice rescue and the end.
func (r *Run) watchLoop(ctx context.Context, w *watchRun) {
	for r.stopReason == "" {
		if r.lim.MaxMinutes > 0 && r.sinceStart() >= time.Duration(r.lim.MaxMinutes)*time.Minute {
			r.stop("budget")
			return
		}
		if !r.watchPollStatus(ctx, w) {
			return
		}
		if !r.watchPollEvents(ctx, w) {
			return
		}
		if r.stopReason != "" {
			return
		}
		switch w.state {
		case "done":
			r.logf("watch: run done plan=%s elapsed=%ds pulls=%d deaths=%d wipes=%d pauses=%d",
				w.record.Plan, w.record.Elapsed, w.record.Pull, w.record.Deaths, w.record.Wipes, w.record.Pauses)
			r.stop("done")
			return
		case "stopped":
			r.logf("watch: run stopped by the module why=%s", w.record.Why)
			r.stop("stopped")
			return
		}
		if r.lim.MaxWipes >= 0 && w.record.Wipes > r.lim.MaxWipes {
			r.logf("watch: wipe budget %d/%d", w.record.Wipes, r.lim.MaxWipes)
			r.stop("wipes")
			return
		}
		if w.state == "running" && r.watchdog(ctx, w) {
			r.stop("watchdog")
			return
		}
		if r.watchRescue(ctx, w) {
			// The rescue ended the watch (a refusal, a lost link, a spent budget).
			if r.stopReason != "" {
				return
			}
		}
		if !r.sleep(ctx, r.cfg.Poll) {
			return
		}
	}
}

// watchPollStatus reads the module's run record and applies it. false means the watch has ended (a lost link or a
// refusal that ended it).
func (r *Run) watchPollStatus(ctx context.Context, w *watchRun) bool {
	rep, err := r.cmd.RunStatus(ctx, r.cfg.Leader)
	switch {
	case errors.Is(err, ErrLinkLost):
		r.linkLost()
		return false
	case err != nil:
		r.watchFail(&w.statusBlind)
		r.logf("watch: status: %v", err)
		return r.stopReason == ""
	}
	r.watchOK(&w.statusBlind)
	if len(rep.Runs) != 1 {
		r.stop("status:records")
		return false
	}
	r.applyRun(w, rep.Runs[0])
	r.watchPrint(w)
	return true
}

// watchPollEvents reads the event ring above the last sequence and applies every new event.
func (r *Run) watchPollEvents(ctx context.Context, w *watchRun) bool {
	rep, err := r.cmd.RunEvents(ctx, r.cfg.Leader, w.lastSeq, w.lastSeq > 0)
	switch {
	case errors.Is(err, ErrLinkLost):
		r.linkLost()
		return false
	case err != nil:
		r.watchFail(&w.eventsBlind)
		r.logf("watch: events: %v", err)
		return r.stopReason == ""
	}
	r.watchOK(&w.eventsBlind)
	for _, ev := range rep.Events {
		r.watchEvent(w, ev)
	}
	if rep.HasLast && rep.Last > w.lastSeq {
		w.lastSeq = rep.Last
	}
	if w.events%20 == 0 && w.events > 0 {
		// One instance read per twenty events (and one per minute, below): the boss state is a party report away.
		r.watchBosses(ctx, w, false)
	}
	return true
}

// applyRun copies the module's record into the watcher's bookkeeping and notes the state transitions.
func (r *Run) applyRun(w *watchRun, st tbcmd.RunState) {
	prev := w.state
	w.record = st
	w.have = true
	w.state = st.State
	w.elapsed = st.Elapsed
	if st.HasLeg && st.LegName != "" {
		w.nameLeg(st.Leg, st.LegName)
	}
	r.wipes = st.Wipes
	r.deadTotal = st.Deaths
	if prev != st.State {
		switch st.State {
		case "paused":
			if w.pausedSince.IsZero() {
				w.pausedSince = r.now()
			}
			if !wipePauseReason(st.Why) {
				// The module paused for a reason that is not the wipe: that pause is not the wipe's, whatever event
				// came before it.
				w.onWipe = false
				w.wipePending = false
			}
		case "running":
			w.pausedSince = time.Time{}
			w.onWipe = false
			w.wipePending = false
			w.wipeAt = time.Time{}
			w.revived = false
		default:
			// A done or stopped run has no pause left to rescue.
			w.onWipe = false
			w.wipePending = false
		}
	}
}

// watchEvent applies one run event: the pause log, the wipe/pause pairing the rescue needs, progress, and the
// per-leg evidence the report card reads.
func (r *Run) watchEvent(w *watchRun, ev tbcmd.RunEvent) {
	w.lastSeq = ev.Seq
	w.lastEvent = r.now()
	w.events++
	r.progress()
	w.noteEvent(ev)
	switch ev.Kind {
	case "wipe":
		w.wipePending = true
		w.wipeSeq = ev.Seq
		w.wipeAt = r.now()
	case "pause":
		// The rescue's pause is the wipe's own pause and nothing else: the event right after the wipe (no other
		// event between them, by sequence) whose reason names the wipe or the dead party it left. A pause later in
		// the run, for a player's order or any other reason, is not it, and neither is one whose pause event carries
		// no reason while the module's record names another.
		why := ev.Why
		if why == "" {
			why = w.record.Why
		}
		w.onWipe = w.wipePending && ev.Seq == w.wipeSeq+1 && wipePauseReason(why)
		w.wipePending = false
		if !w.pauseSeen[ev.Seq] {
			w.pauseSeen[ev.Seq] = true
			w.pauseLog = append(w.pauseLog, PauseNote{
				AtS: float64(ev.T), Leg: ev.Leg, Step: ev.Step, Why: ev.Why,
			})
		}
	case "resume":
		w.onWipe = false
		w.wipePending = false
		w.wipeAt = time.Time{}
		w.pausedSince = time.Time{}
		w.revived = false
		if n := len(w.pauseLog); n > 0 {
			w.pauseLog[n-1].Cleared = true
		}
	default:
		// Any other event between a wipe and a pause breaks the pair: the pause that follows is not the wipe's.
		w.wipePending = false
	}
	line := eventText(ev)
	if line != w.lastNote {
		r.logf("watch: %s", line)
		w.lastNote = line
	}
}

// wipeReasonWords are the words the module's pause reason carries when it pauses on a wipe: the wipe itself, as the
// run record writes it (`why=wipe`), or the party it left down (`why=dead`). The reason is read as words, so any
// other pause — a player's order (`why=outside-order`), a refused revive (`why=revive`), a stuck leader — is not the
// wipe's, whatever event came before it.
var wipeReasonWords = []string{"wipe", "wiped", "dead"}

// wipePauseReason says whether a pause reason names the wipe or the dead party the module paused on.
func wipePauseReason(why string) bool {
	words := strings.FieldsFunc(strings.ToLower(why), func(c rune) bool {
		return !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9')
	})
	for _, word := range words {
		for _, want := range wipeReasonWords {
			if word == want {
				return true
			}
		}
	}
	return false
}

// noteEvent folds one event into the leg's and step's record: its time span, its pulls, deaths and wipes. The card
// is built from this, so it sees the module's own event times, not the watcher's poll times.
func (w *watchRun) noteEvent(ev tbcmd.RunEvent) {
	if !ev.HasLeg {
		return
	}
	leg := w.leg(ev.Leg)
	if !w.legSeen[ev.Leg] {
		w.legSeen[ev.Leg] = true
		leg.FirstS = ev.T
	}
	if ev.T > leg.LastS {
		leg.LastS = ev.T
	}
	if ev.HasStep {
		key := [2]int{ev.Leg, ev.Step}
		if !w.stepSeen[key] {
			w.stepSeen[key] = true
			leg.Steps++
		}
	}
	switch ev.Kind {
	case "pull":
		leg.Pulls++
		if ev.HasStep {
			s := w.step(ev.Leg, ev.Step)
			s.Pulls++
			s.PullEvents = append(s.PullEvents, PullNote{Seq: ev.Seq, AtS: ev.T})
		}
	case "death":
		leg.Deaths++
		d := DeathRecord{AtS: ev.T, Leg: ev.Leg, Step: ev.Step}
		if ev.HasUnit {
			d.Unit = ev.Unit
			d.Who = w.units[ev.Unit]
		}
		w.deaths = append(w.deaths, d)
		if ev.HasStep {
			w.step(ev.Leg, ev.Step).Deaths++
		}
	case "wipe":
		leg.Wipes++
	case "pause":
		leg.Pauses++
	}
}

// leg returns the leg's record, making it on first sight.
func (w *watchRun) leg(n int) *LegRecord {
	if l := w.legs[n]; l != nil {
		return l
	}
	l := &LegRecord{Leg: n}
	w.legs[n] = l
	return l
}

// nameLeg fills a leg's name from a status record when the record is in that leg.
func (w *watchRun) nameLeg(n int, name string) {
	if l := w.legs[n]; l != nil {
		if l.Name == "" {
			l.Name = name
		}
		return
	}
	w.leg(n).Name = name
}

// step returns the step's record, making it on first sight.
func (w *watchRun) step(leg, step int) *StepRecord {
	key := [2]int{leg, step}
	if s := w.steps[key]; s != nil {
		return s
	}
	s := &StepRecord{Leg: leg, Step: step}
	w.steps[key] = s
	return s
}

// watchUnits maps a party member's GUID to the role word the card uses. A card names roles, never characters.
func (r *Run) watchUnits() map[uint32]string {
	out := map[uint32]string{}
	for i, name := range r.cfg.Party {
		g, ok := r.world.PlayerGUID(name)
		if !ok || g.Low == 0 || g.Low > math.MaxUint32 {
			continue
		}
		out[uint32(g.Low)] = partyRole(i)
	}
	return out
}

// partyRole is the party slot's word: the leader is the tank, the second the healer, the rest the damage dealers.
func partyRole(i int) string {
	switch i {
	case 0:
		return "tank"
	case 1:
		return "healer"
	}
	return fmt.Sprintf("dps%d", i-1)
}

// watchdog pauses a running run whose event ring has not grown for cfg.Watchdog and reports.
func (r *Run) watchdog(ctx context.Context, w *watchRun) bool {
	if w.lastEvent.IsZero() {
		w.lastEvent = r.now()
		return false
	}
	idle := r.now().Sub(w.lastEvent)
	if idle < r.cfg.Watchdog {
		return false
	}
	r.logf("watch: watchdog: no new event for %.0fs (limit %.0fs); pausing the run", idle.Seconds(), r.cfg.Watchdog.Seconds())
	rep, why := r.act(ctx, Action{Kind: ActionRunPause, Who: r.cfg.Leader, Reason: "watchdog no new event"})
	if why == "error" {
		r.logf("watch: the pause was refused code=%s", rep.Code)
	}
	if why == "" {
		// The watch ends here, so its own pause is what the report card must see: the module's event ring is not
		// read again. The module's reason words are the pause's own note.
		reason := "watchdog no new event"
		w.pauseLog = append(w.pauseLog, PauseNote{
			AtS: float64(w.record.Elapsed), Leg: w.record.Leg, Step: w.record.Step, Why: reason,
		})
		w.leg(w.record.Leg).Pauses++
	}
	return true
}

// watchRescue is the practice rescue: a run paused on a wipe for Limits.WatchRescue gets `.tb revive party`
// (PRACTICE) and then `.tb run resume`, at most Limits.MaxPractice times. It returns true when it acted this poll.
func (r *Run) watchRescue(ctx context.Context, w *watchRun) bool {
	if w.state != "paused" || !w.onWipe || w.pausedSince.IsZero() {
		return false
	}
	if r.now().Sub(w.pausedSince) < r.lim.WatchRescue {
		return false
	}
	if !r.practiceOn() {
		if !w.revived {
			r.logf("watch: paused on a wipe for %.0fs; the practice switch is off",
				r.now().Sub(w.pausedSince).Seconds())
			w.revived = true // one line, then the time budget decides
		}
		return false
	}
	if !w.revived {
		if w.rescues >= r.lim.MaxPractice {
			r.stop("practice-budget")
			return true
		}
		rep, why := r.act(ctx, Action{Kind: ActionRevive, Who: "party", Practice: true})
		switch why {
		case "":
			w.rescues++
			w.revived = true
			w.reviveAt = r.now()
			why := ""
			if n := len(w.pauseLog); n > 0 {
				why = w.pauseLog[n-1].Why
			}
			w.rescueLog = append(w.rescueLog, RescueNote{
				AtS: w.record.Elapsed, Leg: w.record.Leg, Step: w.record.Step, Why: why,
			})
			r.logf("watch: practice rescue %d/%d: paused on a wipe for %.0fs; revived the party",
				w.rescues, r.lim.MaxPractice, r.now().Sub(w.pausedSince).Seconds())
		case "link", "stop":
			return true
		case "error":
			if rep.Code == "not-dead" {
				// The module raised the party itself; the pause is what is left to clear.
				w.revived = true
				w.reviveAt = r.now()
				r.logf("watch: paused on a wipe but nobody is dead; resuming")
				break
			}
			r.stop("revive:" + rep.Code)
			return true
		}
		if !w.revived {
			return true // a shut gate: the next poll retries the revive
		}
	}
	// The resume is its own fight-starting action: a fresh complete report and everyone ready, as the gate says.
	rep, why := r.act(ctx, Action{Kind: ActionRunResume, Who: r.cfg.Leader})
	switch why {
	case "":
		r.logf("watch: resumed after the rescue")
		w.revived = false
		w.onWipe = false
		w.pausedSince = time.Time{}
		if rep.Run != nil {
			r.applyRun(w, *rep.Run)
		}
	case "link", "stop":
		return true
	case "error":
		r.stop("resume:" + rep.Code)
		return true
	}
	if r.now().Sub(w.reviveAt) > 2*r.lim.WatchRescue {
		r.stop("resume:timeout")
	}
	return true
}

// watchBosses reads the bosses from the instance report (at most once a minute unless forced) and prints a change.
// The end of the watch forces the read: the last state the module reports is what the summary records, and a run
// that ends inside the cooldown would otherwise keep a boss's earlier state (or none at all).
func (r *Run) watchBosses(ctx context.Context, w *watchRun, force bool) {
	now := r.now()
	if !force && !w.bossAt.IsZero() && now.Sub(w.bossAt) < time.Minute {
		return
	}
	rep, why := r.act(ctx, Action{Kind: ActionInstanceInfo, Who: r.cfg.Leader})
	if why != "" || rep.Info == nil {
		return
	}
	w.bossAt = now
	changed := false
	for _, b := range rep.Info.Bosses {
		if w.bosses[b.Entry] != b.State {
			w.bosses[b.Entry] = b.State
			changed = true
		}
	}
	if changed || w.lastLine == "" {
		r.logf("watch: bosses %s", bossText(w.bosses))
	}
}

// watchPrint prints the run's progress when the record changes.
func (r *Run) watchPrint(w *watchRun) {
	st := w.record
	line := fmt.Sprintf("state=%s step=%d/%d pulls=%d/%d deaths=%d wipes=%d pauses=%d elapsed=%ds",
		st.State, st.Step, st.Steps, st.Pull, st.Pulls, st.Deaths, st.Wipes, st.Pauses, st.Elapsed)
	if st.HasLeg {
		line += fmt.Sprintf(" leg=%d", st.Leg)
	}
	if st.LegName != "" {
		line += " " + st.LegName
	}
	if st.HasKind {
		line += " " + st.Kind
	}
	line += " phase=" + st.Phase
	if st.HasWhy && st.Why != "" {
		line += " why=" + st.Why
	}
	// The elapsed second changes every poll; everything else is what makes a line worth printing.
	key := line
	if i := strings.Index(key, " elapsed="); i >= 0 {
		if j := strings.Index(key[i:], "s "); j >= 0 {
			key = key[:i] + key[i+j+1:]
		}
	}
	if key == w.lastLine {
		return
	}
	w.lastLine = key
	r.logf("watch: %s", line)
}

// watchBlind is one read's blind rule state: consecutive failures and when the first of the run came.
type watchBlind struct {
	failures int
	since    time.Time
}

// watchFail and watchOK are the read failures' own blind rule, kept per read: three failures in a row and
// Limits.Blind without a good read of the same kind end the watch. A status read that succeeds proves nothing about
// the event ring, so each read carries its own counters.
func (r *Run) watchFail(b *watchBlind) {
	b.failures++
	if b.failures == 1 {
		b.since = r.now()
	}
	if b.failures >= 3 && !b.since.IsZero() && r.now().Sub(b.since) >= r.lim.Blind {
		r.stop("blind")
	}
}

func (r *Run) watchOK(b *watchBlind) {
	b.failures = 0
	b.since = time.Time{}
}

// watchFinishBudget is the cleanup phase's whole bound on the run's clock when the watch ended on its own: WatchStop,
// 0 or negative read as the default (600 s).
func (r *Run) watchFinishBudget() time.Duration {
	if r.lim.WatchStop > 0 {
		return r.lim.WatchStop
	}
	return DefaultLimits().WatchStop
}

// InterruptedStopBudget is the cleanup phase's whole bound when the caller's context ended the watch (the operator's
// interrupt or the caller's own deadline): one stop attempt and its confirmation polls, all within this. The command
// must not stay blocked for the whole WatchStop after an interrupt, so the cancel path has this fixed short budget of
// its own; the normal end keeps WatchStop.
const InterruptedStopBudget = 60 * time.Second

// watchFinish ends the module's run when it is still going, waits (bounded) for the stopped state, reads the bosses
// once more and writes the summary. It is the one part of the watch that must run after the caller's context ended:
// the operator's interrupt (or the caller's own deadline) cancels the watch loop, but the module still has to be told
// to stop. The stop and the wait therefore run on a cleanup context taken from context.WithoutCancel, bounded twice —
// on the run's clock and by the same span of real time, because a port that blocks without ever touching the clock
// would never reach the first bound — and every call is additionally bounded by FinishCallTimeout (10 s). The bound
// is WatchStop (600 s by default) when the watch ended on its own, and InterruptedStopBudget (60 s) when the caller's
// context ended it. When the run has not reached stopped by either bound the result says the stop was not confirmed
// (Result.StopUnconfirmed); a lost link ends it at once as before.
func (r *Run) watchFinish(ctx context.Context, w *watchRun) {
	if r.lost {
		r.watchSummary(w)
		return
	}
	budget := r.watchFinishBudget()
	if ctx.Err() != nil {
		// The caller ended the watch (an interrupt or its deadline): stop the module, but do not hold the command
		// for the whole WatchStop waiting for the confirmation.
		budget = InterruptedStopBudget
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	wallDeadline := time.Now().Add(budget)
	outOfTime := func() bool { return fctx.Err() != nil || !time.Now().Before(wallDeadline) }
	if w.have && (w.state == "running" || w.state == "paused" || w.state == "") {
		reason := r.stopReason
		if reason == "" {
			reason = "watch end"
		}
		cctx, ccancel := context.WithTimeout(fctx, FinishCallTimeout)
		rep, why := r.act(cctx, Action{Kind: ActionRunStop, Who: r.cfg.Leader, Reason: reason})
		ccancel()
		switch why {
		case "":
			if rep.Run != nil {
				r.applyRun(w, *rep.Run)
			}
		case "link":
			r.watchSummary(w)
			return
		case "error":
			if rep.Code != "not-running" {
				r.logf("watch: stop refused code=%s", rep.Code)
			}
			// A refused stop is still the end of the watch: report and record.
		}
	}
	deadline := r.now().Add(budget)
	for !r.lost && r.now().Before(deadline) && !outOfTime() && w.state != "stopped" && w.state != "done" {
		cctx, ccancel := context.WithTimeout(fctx, FinishCallTimeout)
		rep, err := r.cmd.RunStatus(cctx, r.cfg.Leader)
		ccancel()
		if errors.Is(err, ErrLinkLost) {
			r.linkLost()
			break
		}
		if err != nil {
			var ce *tbcmd.CommandError
			if errors.As(err, &ce) && (ce.Code == "no-run" || ce.Code == "not-running") {
				// The module has no run of that leader any more: nothing will reach stopped.
				break
			}
			if !r.sleepDetached(fctx, r.cfg.Poll) {
				break
			}
			continue
		}
		if len(rep.Runs) == 1 {
			r.applyRun(w, rep.Runs[0])
		}
		if w.state == "stopped" || w.state == "done" {
			break
		}
		if !r.sleepDetached(fctx, r.cfg.Poll) {
			break
		}
	}
	if w.have && w.state != "stopped" && w.state != "done" && !r.lost {
		w.stopUnconfirmed = true
		r.logf("watch: stop not confirmed: the run is still %s, not stopped within %.0fs",
			w.state, budget.Seconds())
	}
	if !r.lost {
		cctx, ccancel := context.WithTimeout(fctx, FinishCallTimeout)
		r.watchBosses(cctx, w, true)
		ccancel()
	}
	r.watchSummary(w)
}

// watchSummary writes the watched run's counters into the recorder's summary.
func (r *Run) watchSummary(w *watchRun) {
	st := w.record
	bosses := make(map[string]string, len(w.bosses))
	for entry, state := range w.bosses {
		bosses[fmt.Sprintf("%d", entry)] = state
	}
	dead := map[string]bool{}
	for entry, state := range bosses {
		if state == "dead" {
			dead[entry] = true
		}
	}
	rw := RunWatch{
		State: w.state, Step: st.Step, Steps: st.Steps, Leg: st.Leg, LegName: st.LegName, Kind: st.Kind,
		Phase: st.Phase, Pull: st.Pull, Pulls: st.Pulls, Deaths: st.Deaths, Wipes: st.Wipes, Pauses: st.Pauses,
		ElapsedS: float64(st.Elapsed), Why: st.Why, Bosses: bosses, Rescues: w.rescues, Events: w.events,
		PauseLog: w.pauseLog, Legs: w.legRecords(), StepLog: w.stepRecords(), DeathLog: w.deaths,
		RescueLog: w.rescueLog, StopUnconfirmed: w.stopUnconfirmed,
	}
	r.watch = &rw
	if r.rec == nil {
		return
	}
	r.rec.sum.Bosses = dead
	r.rec.sum.Wipes = st.Wipes
	r.rec.sum.Deaths = st.Deaths
	// The instance the party is in comes from the state report the run's gates took, as a leading run's does. Without
	// it the written summary always says instance 0, because watchResult replaces res.Summary with the recorder's.
	r.rec.sum.Instance = r.instance
	r.rec.sum.Practice = w.rescues
	r.rec.sum.Clear = w.state == "done"
	r.rec.SetWatch(rw, r.now())
}

// watchResult is the watch's result: the summary, the reason and the exit code.
func (r *Run) watchResult(w *watchRun) Result {
	res := r.result()
	if w != nil && w.state == "done" {
		res.Clear = true
	}
	if r.watch != nil {
		res.Summary.Watch = r.watch
		res.Summary.Mode = "watch"
		res.Summary.Instance = r.instance
		res.Summary.Plan = r.watchStatePlan(w)
		res.Summary.Wipes = r.watch.Wipes
		res.Summary.Deaths = r.watch.Deaths
		res.Summary.Practice = r.watch.Rescues
		res.Summary.Clear = r.watch.State == "done"
		res.StopUnconfirmed = r.watch.StopUnconfirmed
	}
	r.event("stop", map[string]any{"reason": r.stopReason, "watch": true})
	if r.rec != nil {
		r.rec.Finish(r.now(), r.stopReason, w.record.LegName)
		res.Summary = r.rec.sum
		res.SummaryFile = r.rec.SummaryPath()
		res.Trace = r.rec.TracePath()
		res.Clear = r.rec.sum.Clear
	}
	return res
}

// legRecords is the per-leg record in leg order, with each leg's span closed: a leg runs to the next leg's first
// event, and the last to the run's last elapsed second (or its own last event, whichever is later).
func (w *watchRun) legRecords() []LegRecord {
	if len(w.legs) == 0 {
		return nil
	}
	nums := make([]int, 0, len(w.legs))
	for n := range w.legs {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := make([]LegRecord, 0, len(nums))
	for i, n := range nums {
		l := *w.legs[n]
		if i+1 < len(nums) {
			l.Seconds = w.legs[nums[i+1]].FirstS - l.FirstS
		} else {
			end := w.elapsed
			if end < l.LastS {
				end = l.LastS
			}
			l.Seconds = end - l.FirstS
		}
		if l.Seconds < 0 {
			l.Seconds = 0
		}
		out = append(out, l)
	}
	return out
}

// stepRecords is the per-step record in (leg, step) order.
func (w *watchRun) stepRecords() []StepRecord {
	if len(w.steps) == 0 {
		return nil
	}
	keys := make([][2]int, 0, len(w.steps))
	for k := range w.steps {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	out := make([]StepRecord, 0, len(keys))
	for _, k := range keys {
		s := *w.steps[k]
		s.PullEvents = append([]PullNote(nil), s.PullEvents...)
		out = append(out, s)
	}
	return out
}

// watchStatePlan is the plan name the watched run reports.
func (r *Run) watchStatePlan(w *watchRun) string {
	if w == nil {
		return ""
	}
	return w.record.Plan
}

// logf prints one watch line through the caller's writer.
func (r *Run) logf(format string, args ...any) {
	if r.cfg.Logf != nil {
		r.cfg.Logf(format, args...)
	}
}

// eventText is one event in the watch's words, without the keys it does not carry.
func eventText(ev tbcmd.RunEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "event kind=%s seq=%d t=%ds", ev.Kind, ev.Seq, ev.T)
	if ev.HasStep {
		fmt.Fprintf(&b, " step=%d", ev.Step)
	}
	if ev.HasLeg {
		fmt.Fprintf(&b, " leg=%d", ev.Leg)
	}
	if ev.HasUnit {
		fmt.Fprintf(&b, " unit=%d", ev.Unit)
	}
	if ev.Why != "" {
		b.WriteString(" why=")
		b.WriteString(ev.Why)
	}
	return b.String()
}

// bossText is the boss list, sorted by entry.
func bossText(bosses map[uint32]string) string {
	entries := make([]int, 0, len(bosses))
	for e := range bosses {
		entries = append(entries, int(e))
	}
	sort.Ints(entries)
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%d:%s", e, bosses[uint32(e)])
	}
	if b.Len() == 0 {
		return "none"
	}
	return b.String()
}
