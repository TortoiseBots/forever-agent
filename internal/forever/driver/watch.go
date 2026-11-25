package driver

// The watch machine: `forever dungeon-watch`. A server module runs the dungeon by itself (its tank bot leads the
// party); the client's job is to watch, not to lead. The watcher brings the party in exactly as a run does
// (PREPARE, then TRAVEL/ENTER), starts the module's run with one typed action, and from then on sends NO order: an
// order would pause the module's run. It polls the module's status and event ring at a stated interval, prints the
// progress (leg, step, pulls, deaths, wipes, pauses with their reasons, the bosses the instance report gives), and
// keeps the safety budgets:
//
//   - the watchdog: no new event for cfg.Watchdog -> pause the run with a reason, report, and end the watch; a run
//     that is already paused is not paused again, the watchdog reports it and the watch ends;
//   - the pause budget: a run whose state is paused and whose pause has not cleared for Limits.WatchPause (5 min)
//     is reported and the watch ends as paused-stuck (the module never resumes a pause by itself, and the watch
//     sends no resume on its own);
//   - the time budget (Limits.MaxMinutes) and the wipe budget (Limits.MaxWipes);
//   - the practice rescue: a run paused on a wipe for Limits.WatchRescue (300 s) gets one `.tb revive party`
//     (PRACTICE) and one `.tb run resume`, at most Limits.MaxPractice times. The rescue runs before the pause
//     budget, so the wipe pause it can clear is not reported as stuck.
//
// The party is bound to a commander of the module's own — a bot its roster names, never this character — and the
// module blocks a bot whose master stands on another map (`stuck=blocked:bot in an instance`). The watch brings that
// commander to the tank's side before PREPARE and again after an entry lands the party, and waits (bounded) for the
// module's own report to show it there.
//
// Every send goes through act and its gate, like every other setup action. The stop path sends `.tb run stop`, waits
// (bounded) for the module's state to reach stopped, and writes the run's summary: the dungeon-run format, extended
// with the module's own counters. A lost link ends the watch at once and nothing is sent after it.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// watchRun is the watcher's own bookkeeping over the module's records.
type watchRun struct {
	record tbcmd.RunState
	have   bool
	// startedAt is the run-start action's reply time: the module's run zero, and the anchor the trace's run_start
	// event carries so a map reader can put the module's at_s on the trace's clock.
	startedAt time.Time

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
	// frames, combatRows and combatLost are the watch's own record of the sample tick: how many frames it wrote, how
	// many combat rows reached the file, and how many the session's ring had already dropped. combatLostThrough is
	// the highest seq whose loss is already in combatLost: a cursor the writer retries reports the same hole again,
	// so only the part past this watermark is new. frameBlind and frameThreatBlind make one line of a failing frame
	// read instead of one per tick; combatBlind, combatErr and combatTruncated do the same for a failing or bounded
	// combat-log write.
	frames            int
	combatSeq         int
	combatRows        int
	combatLost        int
	combatLostThrough int
	combatBlind       bool
	combatErr         string
	combatTruncated   bool
	frameBlind        bool
	frameThreatBlind  bool
	pauseLog          []PauseNote
	pauseSeen         map[int]bool
	// unknownKinds are the event kinds this client cannot name that the ring already carried: one line each, the
	// rest ignored. A kind unknown once is not worth a line per event.
	unknownKinds map[string]bool
	bosses       map[uint32]string
	bossAt       time.Time
	// encounters are the instance's own pull/kill/wipe packets, on the module run's clock.
	encounters []EncounterRecord
	lastLine   string
	lastNote   string
	// stopUnconfirmed says the cleanup phase ended without the module's record reaching stopped: the stop may not
	// have reached it (or it could not stop yet), so the result is not a clean end.
	stopUnconfirmed bool
	// statusBlind and eventsBlind are each read's own blind rule: successive failures and the time of the first of
	// them, so a good status read never hides failing event reads, nor the other way round.
	statusBlind watchBlind
	eventsBlind watchBlind

	// The decisions read: the module's run id (the record's run=, and the header's check), the cursor of the last
	// page read, the next incremental read's time, and what the writes added up to. decisionsOff ends the reads (the
	// module's run changed, or the capture is off); decisionsBlind keeps a failing read to one line until it answers.
	runID            string
	decisionsSeq     int
	decisionsHas     bool
	decisionsAt      time.Time
	decisionsRows    int
	decisionsPages   int
	decisionsReads   int
	decisionsBytes   int64
	decisionsSkipped int
	decisionsNote    string
	decisionsPull    string
	decisionsTrunc   bool
	decisionsOff     bool
	decisionsBlind   bool

	// The report card's evidence: the module's own event times grouped by leg and step, the deaths, the rescues and
	// the party slot each unit id is. Units holds role words, never character names.
	legs       map[int]*LegRecord
	legSeen    map[int]bool
	steps      map[[2]int]*StepRecord
	stepSeen   map[[2]int]bool
	deaths     []DeathRecord
	loosePulls []PullNote // pulls the module sent without a step (or leg)
	rescueLog  []RescueNote
	roles      map[string]string
	elapsed    int
	// deathRecap is the hook's memory: one `.deathrecap` request per death, parsed and attached to the death in
	// the record and the bug hunter's ticket text. Once the server answers that the command does not exist (a core
	// without PR #380) or that a bot has no recording, the watch stops asking for it, never spamming.
	deathRecap *deathRecapState
}

// Watch runs the watch machine and returns what it saw. The caller owns the ports; r.cfg.Watch and r.cfg.Leader say
// what to watch.
func (r *Run) Watch(ctx context.Context) Result {
	r.start = r.clock.Now()
	r.lastProgress = r.start
	r.refreshSelf()
	w := &watchRun{
		pauseSeen: map[int]bool{}, bosses: map[uint32]string{}, unknownKinds: map[string]bool{},
		legs: map[int]*LegRecord{}, legSeen: map[int]bool{},
		steps: map[[2]int]*StepRecord{}, stepSeen: map[[2]int]bool{},
	}
	if r.configBroken() {
		r.stop("config")
		return r.watchResult(w)
	}
	// The commander comes to the party before PREPARE. The module names it in its roster (never this client's own
	// character), and with the party already placed (--enter none) on a map the commander is not on, the module
	// reports the bots `stuck=blocked:bot in an instance` and PREPARE would end on stuck:<tank>. The roster is read
	// here for that one move; PREPARE reads it again for its own rules and retries the move with the GM lines on.
	// With an entry the party lands without the commander; ENTER brings it in again after the party lands. A roster
	// that names no commander ends the watch here (commander:unnamed): the watching character is never moved in its
	// place, and no PREPARE line goes out for a watch that cannot place the party's own. A failed roster read is not
	// the end: PREPARE reads again and names its own failure.
	r.legs = nil
	st := StatePrepare
	if r.cfg.Attach {
		// A watch that attaches to a run started elsewhere (--attach) runs no entry: the party is already in and
		// another client commands it, so no GM line, no move and no PREPARE, TRAVEL or ENTER step goes out. The
		// roster is still read, with PREPARE's own rules, for the party's names, its roles and the commander; the
		// run's own plan is checked when the attach finds it.
		if r.adoptWatchParty(ctx) {
			st = StateLegWalk
		}
	} else {
		// A roster read that fails is not the end here: PREPARE reads it again and names its own failure. A raid
		// watch still makes the module's group the plan's raid, with its layout, before anything else goes out.
		r.readCommander(ctx)
		if r.cfg.Raid && r.stopReason == "" && !r.lost {
			r.raidSetup(ctx)
		}
		if r.stopReason == "" && !r.lost {
			r.commanderToTank(ctx, commanderMoveBudget)
		}
		if r.stopReason == "" && !r.lost {
			r.states = append(r.states, st)
			st = r.prepare(ctx)
		}
	}
	w.roles = r.watchRoles()
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
	// The same-map check is the party's own: every member on the plan's map in one instance. The observer may stand
	// anywhere; with --enter none this is the one check that the party was placed as the watch expects. The observer's
	// own teleport polls the state after ENTER verified the party, so a failed or stale read there is re-read, not a
	// placement. A watch attached to a run started elsewhere runs no entry and checks the run instead: the attach
	// refuses a run of another plan, whose map this check would report as a party out of place.
	if r.stopReason == "" && !r.cfg.Attach && !r.partyPlaced(ctx) {
		if r.stopReason == "" {
			r.logf("watch: the party is not on the plan's map %d in one instance", r.route.Map)
			r.stop("enter:party-map")
		}
	}
	if r.stopReason == "" {
		// The party is in: the core's GM combat-log watch now sees it whole, from any distance (B of SPEC #109).
		r.startCombatlogWatch(ctx)
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
	// The run is over: the summary says what the decisions reads wrote, and the warning a failed read left. The reads
	// happened during the run and before the stop, because the module erases a run's capture when it ends.
	r.watchDecisionsSummary(w)
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

// partyPlaced is the placement check before a watch starts the module's run: every member on the plan's map in one
// instance, from a whole report. A read that failed or went stale (the observer's own teleport polls the state after
// the party's entry) is polled again, a few times; only a whole fresh report that says the party is elsewhere is a
// refusal without another read.
func (r *Run) partyPlaced(ctx context.Context) bool {
	const attempts = 3
	for range attempts {
		if r.partyInInstance() {
			return true
		}
		if r.reportComplete() && r.repFresh() {
			return false
		}
		if r.stopReason != "" || r.lost || !r.sleep(ctx, r.cfg.Poll) {
			return false
		}
		if !r.refresh(ctx) && (r.stopReason != "" || r.lost) {
			return false
		}
	}
	return r.partyInInstance()
}

// startRecord is the one run record a start answer must carry: exactly one, for the leader and the plan the command
// named, and running. Zero or several records, or a record naming another leader, plan or state, is a malformed
// report (ErrBadReport) the watch must not read as a started run.
func startRecord(rep ActionReport, leader, plan string) (tbcmd.RunState, error) {
	if rep.Count != 1 || rep.Run == nil {
		return tbcmd.RunState{}, fmt.Errorf("%w: run start carried %d run record(s), want 1", tbcmd.ErrBadReport, rep.Count)
	}
	st := *rep.Run
	if !strings.EqualFold(st.Lead, leader) || st.Plan != plan || st.State != "running" {
		return tbcmd.RunState{}, fmt.Errorf("%w: run start answered lead=%q plan=%q state=%q, the command was lead=%q plan=%q",
			tbcmd.ErrBadReport, st.Lead, st.Plan, st.State, leader, plan)
	}
	return st, nil
}

// defaultAttachWait bounds a watch's wait for a run started elsewhere (--attach) when the flag does not say.
const defaultAttachWait = 300 * time.Second

// watchStart starts the module's run: `.tb run start <leader> <plan>`, a fight-starting action (the run pulls from
// then on), retried while its gate is shut and ended with the module's own refusal code when it refuses. With
// --attach it sends no start at all: it waits for the run another client started and attaches to it.
func (r *Run) watchStart(ctx context.Context, w *watchRun) {
	if r.cfg.Attach {
		r.watchAttach(ctx, w)
		return
	}
	deadline := r.now().Add(r.lim.Enter)
	for r.stopReason == "" {
		rep, why := r.act(ctx, Action{Kind: ActionRunStart, Who: r.cfg.Leader, Plan: r.route.Name})
		switch why {
		case "":
			st, err := startRecord(rep, r.cfg.Leader, r.route.Name)
			if err != nil {
				// A start answer without its one run record is not a started run: the watch must not go on as if
				// the module had accepted the line.
				r.logf("watch: the start answer is refused: %v", err)
				r.stop("start:bad-report")
				return
			}
			// The reply time is the module's run zero on the trace's clock: the map adapter aligns every at_s with
			// it. The event is written even when the run ends at once, so an old-style duration guess is never used.
			w.startedAt = r.now()
			w.lastEvent = w.startedAt
			if r.rec != nil {
				r.rec.Event(w.startedAt, "run_start", map[string]any{"plan": r.route.Name})
			}
			r.applyRun(w, st)
			if st.HasLeg {
				// The module's run starts in its first leg: that leg's time starts at the run's zero.
				w.legSeen[st.Leg] = true
				w.leg(st.Leg).FirstS = 0
			}
			if st.HasEv {
				w.lastSeq = st.Ev
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
			if code == "foreign-bot" {
				// The run's leader is not this module's to start: another client started it, and --attach is how a
				// watch follows that run.
				r.logf("watch: start refused foreign-bot; a run started elsewhere can be watched with --attach")
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

// watchAttach waits for the run another client started (--attach) and attaches to it: no `.tb run start` is sent.
// The leader's one run in state running is the run; a paused one is attached too, with its state applied. A run whose
// plan is not this watch's plan is refused with both plans named (start:attach-plan), before anything is applied. The
// wait is bounded by --attach-wait (300 s by default) and ends as `start:attach-timeout`.
func (r *Run) watchAttach(ctx context.Context, w *watchRun) {
	wait := r.cfg.AttachWait
	if wait <= 0 {
		wait = defaultAttachWait
	}
	deadline := r.now().Add(wait)
	logged := false
	for r.stopReason == "" {
		rep, err := r.cmd.RunStatus(ctx, r.cfg.Leader)
		switch {
		case errors.Is(err, ErrLinkLost):
			r.linkLost()
			return
		case err != nil:
			// No run yet (or a read that failed): the wait goes on. One line says why, so a silent server is visible
			// without a line per poll.
			if !logged {
				logged = true
				r.logf("watch: attach: %v", err)
			}
		case len(rep.Runs) == 1 && (rep.Runs[0].State == "running" || rep.Runs[0].State == "paused"):
			st := rep.Runs[0]
			if st.Plan != r.route.Name {
				// A watch follows a run of its own plan: the report card, the legs and the map are the plan's. A run
				// of another plan is refused before anything is applied, and the refusal names both plans.
				r.logf("watch: attach refused: the leader's run is plan %q, not --plan %q; a watch attaches only to a run of its own plan", st.Plan, r.route.Name)
				r.stop("start:attach-plan")
				return
			}
			// The attach time is the watch's run zero on the trace's clock, as a start's reply time is.
			w.startedAt = r.now()
			w.lastEvent = w.startedAt
			if r.rec != nil {
				r.rec.Event(w.startedAt, "run_start", map[string]any{"plan": st.Plan, "attached": true})
			}
			r.applyRun(w, st)
			// The leg and the event cursor the run already reached are the watch's own from now on, as a start's
			// record gives them: without them the leg's time and the early events would be lost.
			if st.HasLeg {
				w.legSeen[st.Leg] = true
				w.leg(st.Leg).FirstS = 0
			}
			if st.HasEv {
				w.lastSeq = st.Ev
			}
			r.progress()
			r.logf("watch: attached state=%s", st.State)
			return
		}
		if !r.now().Before(deadline) {
			r.stop("start:attach-timeout")
			return
		}
		if !r.sleep(ctx, r.cfg.Poll) {
			return
		}
	}
}

// watchLoop is the watch itself: status, events, the budgets, the practice rescue and the end.
func (r *Run) watchLoop(ctx context.Context, w *watchRun) {
	var nextFrame time.Time
	for r.stopReason == "" {
		if r.cfg.FrameEvery > 0 {
			now := r.now()
			if nextFrame.IsZero() || !now.Before(nextFrame) {
				r.watchFrame(ctx, w)
				if r.stopReason != "" || r.lost {
					return
				}
				if nextFrame.IsZero() {
					nextFrame = now
				}
				nextFrame = nextFrame.Add(r.cfg.FrameEvery)
				if !nextFrame.After(now) {
					// A poll or a frame that took longer than the interval: the next one is one interval away, so the
					// frames stay spaced instead of a burst catching up.
					nextFrame = now.Add(r.cfg.FrameEvery)
				}
			}
		}
		r.applyEncounters(r.now(), w)
		r.drainCombat(w)
		if r.lim.MaxMinutes > 0 && r.sinceStart() >= time.Duration(r.lim.MaxMinutes)*time.Minute {
			r.stop("budget")
			return
		}
		if !r.watchPollStatus(ctx, w) {
			return
		}
		if !r.now().Before(w.decisionsAt) {
			r.watchPullDecisions(ctx, w)
			w.decisionsAt = r.now().Add(decisionsReadEvery)
		}
		if !r.watchPollEvents(ctx, w) {
			return
		}
		if r.stopReason != "" {
			return
		}
		switch w.state {
		case "done":
			r.deathRecapFlush(ctx, w)
			r.logf("watch: run done plan=%s elapsed=%ds pulls=%d deaths=%d wipes=%d pauses=%d",
				w.record.Plan, w.record.Elapsed, w.record.Pull, w.record.Deaths, w.record.Wipes, w.record.Pauses)
			r.stop("done")
			return
		case "stopped":
			r.deathRecapFlush(ctx, w)
			r.logf("watch: run stopped by the module why=%s", w.record.Why)
			r.stop("stopped")
			return
		}
		if r.lim.MaxWipes >= 0 && w.record.Wipes > r.lim.MaxWipes {
			r.logf("watch: wipe budget %d/%d", w.record.Wipes, r.lim.MaxWipes)
			r.stop("wipes")
			return
		}
		// The practice rescue is the wipe pause's one remedy and runs before the pause budget: a pause it can
		// clear is not a stuck one.
		if r.watchRescue(ctx, w) {
			// The rescue ended the watch (a refusal, a lost link, a spent budget).
			if r.stopReason != "" {
				return
			}
		}
		if w.state == "paused" && r.pauseStuck(w) {
			r.stop(StopPausedStuck)
			return
		}
		// The watchdog covers a paused run too: a pause sends no event, so its ring is as silent as a hung pull's.
		if (w.state == "running" || w.state == "paused") && r.watchdog(ctx, w) {
			if w.state == "paused" {
				r.stop(StopPausedStuck)
			} else {
				r.stop("watchdog")
			}
			return
		}
		// The recordings come after the event handling and the rescue decisions, one request per tick.
		r.deathRecapPump(ctx, w, false)
		if r.stopReason != "" {
			return
		}
		if !r.sleep(ctx, r.cfg.Poll) {
			return
		}
	}
}

// combatPageMax is the most combat-log rows one drain asks for (the session's own page bound).
const combatPageMax = 1000

// watchFrame is the watch's sample tick: `.tb state party <leader>` and `.tb threat party <leader>` (both reads,
// never orders) written as one trace frame. A frame the module answers with a failed read is one line and no frame;
// the next tick retries, and a later good read says it came back. A frame read never ends the watch: the module's
// run is still watched by the status and event polls.
func (r *Run) watchFrame(ctx context.Context, w *watchRun) {
	rep, err := r.cmd.State(ctx)
	if errors.Is(err, ErrLinkLost) {
		r.linkLost()
		return
	}
	if err != nil {
		if !w.frameBlind {
			w.frameBlind = true
			r.logf("watch: frame: the state read failed (%v); the next tick retries", err)
		}
		return
	}
	if w.frameBlind {
		w.frameBlind = false
		r.logf("watch: frame: the state read answers again")
	}
	// The client's pushed threat table answers first; `.tb threat` is now the fallback, at most once per
	// threatFallbackEvery while the packets flow.
	thr, tok := r.threat(ctx)
	if r.stopReason != "" || r.lost {
		return
	}
	var trep *tbcmd.ThreatReport
	if !tok {
		if !w.frameThreatBlind {
			w.frameThreatBlind = true
			r.logf("watch: frame: the threat read failed; the frame carries the party only")
		}
	} else {
		if w.frameThreatBlind {
			w.frameThreatBlind = false
			r.logf("watch: frame: the threat read answers again")
		}
		trep = &thr
	}
	bots := r.frameBots(ctx, rep)
	if r.stopReason != "" || r.lost {
		return
	}
	r.rec.WatchFrame(r.now(), bots, trep, r.frameGUID, r.world.Units())
	w.frames++
}

// frameBots is the frame's party: the module's lines in the order it sent them, each with its member's guid (see
// memberGUIDOf), the party slot's role and the auras the model holds for it. A member whose guid is not known yet
// has the commander's binding card read for it (cardReadEvery at most), so a frame never waits on a name.
func (r *Run) frameBots(ctx context.Context, rep tbcmd.StateReport) []FrameBot {
	out := make([]FrameBot, 0, len(rep.Bots))
	for i := range rep.Bots {
		if _, ok := r.memberGUIDOf(rep.Bots[i].Name); !ok {
			r.readCardGUIDs(ctx)
			break
		}
	}
	for i := range rep.Bots {
		b := rep.Bots[i]
		fb := FrameBot{State: b}
		if slot := r.partySlot(b.Name); slot >= 0 {
			fb.Role = r.traceRoleOf(b.Name, slot)
		}
		if g, ok := r.memberGUIDOf(b.Name); ok {
			fb.GUID = g.String()
			fb.Auras = r.world.Auras(g)
		}
		out = append(out, fb)
	}
	return out
}

// cardReadEvery is the least time between two binding-card reads for a member whose guid is not known yet.
const cardReadEvery = 30 * time.Second

// memberGUIDOf is a watched member's guid: the one already learned for its name, else what this client's model
// answers (the party roster, a player in view), which is then kept. A watch's observer is neither in the party nor,
// as a rule, in view of it, so the model alone leaves every member unknown; the commander's binding card
// (readCardGUIDs) is the source that needs neither.
func (r *Run) memberGUIDOf(name string) (wire.GUID, bool) {
	key := strings.ToLower(name)
	if g, ok := r.memberGUID[key]; ok {
		return g, true
	}
	if g, ok := r.world.PlayerGUID(name); ok && !g.IsZero() {
		r.learnMemberGUID(key, g)
		return g, true
	}
	return wire.EmptyGUID, false
}

// learnMemberGUID keeps a member's guid; the first one learned stays.
func (r *Run) learnMemberGUID(key string, g wire.GUID) {
	if r.memberGUID == nil {
		r.memberGUID = map[string]wire.GUID{}
	}
	if _, ok := r.memberGUID[key]; !ok {
		r.memberGUID[key] = g
	}
}

// readCardGUIDs reads the commander's binding card and learns each bound bot's guid from it: the module writes the
// bot's database counter (guid=, decimal), which on this realm is the low half of the player's guid. A bot two
// cards name with two counters is ambiguous and learned from neither. It reads at most once per cardReadEvery; a
// failed read is one line until a read answers again.
func (r *Run) readCardGUIDs(ctx context.Context) {
	if r.lost || r.stopReason != "" {
		return
	}
	now := r.now()
	if !r.cardReadAt.IsZero() && now.Sub(r.cardReadAt) < cardReadEvery {
		return
	}
	r.cardReadAt = now
	rep, err := r.cmd.CommanderStatus(ctx, r.commander)
	if errors.Is(err, ErrLinkLost) {
		r.linkLost()
		return
	}
	if err != nil {
		if !r.cardBlind {
			r.cardBlind = true
			r.logf("watch: frame: the commander's card read failed (%v); members without a guid stay unnamed", err)
		}
		return
	}
	r.cardBlind = false
	found := map[string]uint64{}
	bad := map[string]bool{}
	for _, c := range rep.Cards {
		for _, m := range c.Members {
			n, err := strconv.ParseUint(strings.TrimSpace(m.GUID), 10, 64)
			if err != nil || n == 0 {
				continue
			}
			key := strings.ToLower(m.Bot)
			if have, ok := found[key]; ok && have != n {
				bad[key] = true
			}
			found[key] = n
		}
	}
	for key, n := range found {
		if bad[key] {
			continue
		}
		if g, ok := r.world.PlayerGUIDOfCounter(n); ok && !g.IsZero() {
			r.learnMemberGUID(key, g)
		}
	}
}

// partySlot is a bot's party position, -1 for a name outside the party.
func (r *Run) partySlot(name string) int {
	for i, n := range r.cfg.Party {
		if strings.EqualFold(n, name) {
			return i
		}
	}
	return -1
}

// frameGUID is a unit name as the trace names it: its guid (the combat row's own text) when the watch has learned it
// (memberGUIDOf: the binding card or this client's model). A pushed threat list names a unit by GUID when the model holds no name for it, and that text is
// already the trace's own. Nothing when neither is true (a frame never carries a character name).
func (r *Run) frameGUID(name string) string {
	if name == "" {
		return ""
	}
	if len(name) >= 2 && name[0] == '0' && (name[1] == 'x' || name[1] == 'X') {
		return name
	}
	if g, ok := r.memberGUIDOf(name); ok {
		return g.String()
	}
	return ""
}

// drainCombat writes the session's new combat-log rows into the run's <run_id>.combatlog.jsonl: pages of at most
// combatPageMax until the log is caught up. A hole the ring reports is one line per growth of the loss, with the
// oldest seq the log still holds, never a silent hole, and every hole adds to the total: a later hole counts whatever
// its size, and a cursor the writer retries does not count the same hole twice. The cursor advances only past a page
// the file has: a failed write is one line, the page stays where it is and the next tick retries it, so a full disk or
// a revoked permission never turns into rows the run's record does not know it lost. A page the file's size bound
// refuses ends the drain for good and the summary carries combat_truncated.
func (r *Run) drainCombat(w *watchRun) {
	if r.combat == nil || r.rec == nil {
		return
	}
	for {
		if r.rec.CombatTruncated() {
			if !w.combatTruncated {
				w.combatTruncated = true
				r.logf("watch: the combat log reached its %d MiB bound at %d row(s); no later row is written",
					r.rec.CombatMaxBytes()>>20, w.combatRows)
			}
			return
		}
		rows, next, oldest, dropped := r.combat.CombatRows(w.combatSeq, combatPageMax)
		// dropped is the hole above the cursor the ring reports now: rows w.combatSeq+1 through oldest-1 are gone.
		// A retried cursor reports the same hole again, so only the seqs past the watermark are a new loss, and a
		// later hole counts whatever its size.
		if lost := w.combatSeq + dropped; lost > w.combatLostThrough {
			gain := lost - max(w.combatLostThrough, w.combatSeq)
			w.combatLost += gain
			w.combatLostThrough = lost
			r.logf("watch: the combat log dropped %d row(s) above seq %d (oldest held %d)",
				gain, w.combatSeq, oldest)
		}
		if len(rows) > 0 {
			written, err := r.rec.Combat(rows)
			if err != nil {
				if errors.Is(err, errCombatFull) {
					w.combatTruncated = true
					r.logf("watch: the combat log reached its %d MiB bound at %d row(s); no later row is written",
						r.rec.CombatMaxBytes()>>20, w.combatRows)
					return
				}
				w.combatErr = combatErrText(err)
				if !w.combatBlind {
					w.combatBlind = true
					r.logf("watch: combat log: the write failed (%v); the page stays and the next tick retries it", err)
				}
				return
			}
			if w.combatBlind {
				w.combatBlind = false
				w.combatErr = ""
				r.logf("watch: combat log: the writes answer again")
			}
			w.combatRows += written
			r.rec.CombatEvents(rows)
		}
		if next <= w.combatSeq {
			return
		}
		w.combatSeq = next
		if len(rows) < combatPageMax {
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
		r.logf("watch: status: %v", err)
		r.watchFail(&w.statusBlind, "status", err)
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
		r.logf("watch: events: %v", err)
		r.watchFail(&w.eventsBlind, "events", err)
		return r.stopReason == ""
	}
	r.watchOK(&w.eventsBlind)
	for _, ev := range rep.Events {
		before := len(w.deaths)
		r.watchEvent(w, ev)
		if ev.Kind == "death" && len(w.deaths) > before && r.cfg.Watch {
			if _, ok := w.roles[partyNameKey(ev.Why)]; ok {
				r.deathRecapQueue(w, ev.Why, before, ev.T)
			}
		}
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
	// The module's run id is the record's run= key (a newer module prints it): the decisions read compares every
	// header against it, so a newer run of the same leader is never written under this run's file.
	if id := st.Extra["run"]; id != "" && w.runID == "" {
		w.runID = id
	}
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
	// A pause the status read shows starts the pause clock even without a state change: a resume event clears it,
	// and the module's next paused record is the pause that matters then (a status read newer than the event).
	if st.State == "paused" && w.pausedSince.IsZero() {
		w.pausedSince = r.now()
	}
}

// watchEvent applies one run event: the pause log, the wipe/pause pairing the rescue needs, progress, and the
// per-leg evidence the report card reads.
func (r *Run) watchEvent(w *watchRun, ev tbcmd.RunEvent) {
	w.lastSeq = ev.Seq
	w.lastEvent = r.now()
	w.events++
	r.progress()
	if !ev.Known {
		// A kind this client cannot name is kept (the server's vocabulary can grow) and ignored: one line per kind,
		// never a fatal read and never a touch of the wipe/pause pair.
		if !w.unknownKinds[ev.Kind] {
			w.unknownKinds[ev.Kind] = true
			r.logf("watch: unknown event kind=%s (kept, ignored)", ev.Kind)
		}
		return
	}
	w.noteEvent(ev)
	switch ev.Kind {
	case "self-recover":
		// The module brought the party back by itself: there is no rescue to make, and the pair a wipe left is no
		// longer the pause's. The trace records when.
		w.wipePending = false
		w.onWipe = false
		w.wipeAt = time.Time{}
		if r.rec != nil {
			at := r.now()
			if !w.startedAt.IsZero() {
				at = w.startedAt.Add(time.Duration(ev.T) * time.Second)
			}
			r.rec.Event(at, "self_recover", map[string]any{"at_s": ev.T})
		}
	case "wipe":
		w.wipePending = true
		w.wipeSeq = ev.Seq
		w.wipeAt = r.now()
		if r.rec != nil {
			// The module's own wipe event, at the module's own second: the map draws it where it happened instead
			// of inferring one from the deaths. A record without it (from before this event) still infers.
			at := r.now()
			if !w.startedAt.IsZero() {
				at = w.startedAt.Add(time.Duration(ev.T) * time.Second)
			}
			r.rec.Event(at, "wipe", map[string]any{"at_s": ev.T})
		}
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
	case "death":
		// Every other event between a wipe and a pause breaks that pair, a death included.
		w.wipePending = false
		if _, ok := w.roles[partyNameKey(ev.Why)]; !ok {
			r.logf("watch: death event names why=%q, which is no current party member: the death is reported unattributed",
				ev.Why)
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
	if ev.Kind == "pull" && (!ev.HasLeg || !ev.HasStep) {
		// No step to file it under, but it is still a pull: the record keeps it for the run map.
		w.loosePulls = append(w.loosePulls, PullNote{Seq: ev.Seq, AtS: ev.T})
	}
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
		d := DeathRecord{AtS: ev.T, Leg: ev.Leg, Step: ev.Step, Why: ev.Why}
		if ev.HasUnit {
			d.Unit = ev.Unit
		}
		// The module names the dead member in the event's why=; unit= is the fight's unit, the same for every
		// death of one pull, so it is never the member. The member's word is its party slot, not its name. A name
		// that is not a current party member (a typo, an NPC, a stale roster) is bad telemetry: the death keeps
		// the name it carried and takes no role, so no member's count and no pack flag folds it in.
		d.Who = w.roles[partyNameKey(ev.Why)]
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

// watchRoles maps a character name to the party slot word a card uses, so a death event's why= (the dead member's
// name) becomes a role and never a name on the card.
func (r *Run) watchRoles() map[string]string {
	out := make(map[string]string, len(r.cfg.Party))
	for _, name := range r.cfg.Party {
		if name == "" {
			continue
		}
		out[partyNameKey(name)] = r.roleName(name)
	}
	return out
}

// partyNameKey is how a character name is compared with the event's why=: module names are case-insensitive, and
// the event value may carry the surrounding blanks of the record it was written from.
func partyNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// StopPausedStuck is the watch's stop reason when the module's pause never cleared within Limits.WatchPause (or,
// for a watchdog that covers the pause, within the watchdog's own budget): the run is not hung mid-pull, it is
// waiting on a pause nothing will lift, and the watch reports it instead of waiting out the time budget.
const StopPausedStuck = "paused-stuck"

// watchPauseBudget is Limits.WatchPause with the default for a zero or negative value.
func (r *Run) watchPauseBudget() time.Duration {
	if r.lim.WatchPause > 0 {
		return r.lim.WatchPause
	}
	return DefaultLimits().WatchPause
}

// rescueWindow says whether the practice rescue still owns a paused run's time: a pause on a wipe is the rescue's
// until its threshold (Limits.WatchRescue), and after the revive its resume has watchRescue's own window (2×it) to
// land. Both budgets stand aside while it does: a pause the rescue can still clear is not stuck, and a watchdog
// shorter than the rescue would otherwise end a run that is about to be cleared, or cut off its gated resume.
func (r *Run) rescueWindow(w *watchRun) bool {
	rescue := r.lim.WatchRescue
	if rescue <= 0 || w.state != "paused" {
		return false
	}
	// The rescue in flight: the revive has been answered and the resume waits for a report newer than that send.
	// watchRescue ends a rescue that never completes after this window.
	if w.revived && !w.reviveAt.IsZero() && r.now().Sub(w.reviveAt) <= 2*rescue {
		return true
	}
	// The rescue not yet sent: the wipe's pause gets its threshold before anything reads it as stuck.
	return w.onWipe && !w.pausedSince.IsZero() && r.now().Sub(w.pausedSince) < rescue
}

// pauseStuck reports a run that has been paused past the pause budget, and says where it stands and why it is
// paused. The module never resumes a pause by itself, so a pause nothing clears is a run that will sit there: the
// watch ends with StopPausedStuck rather than waiting for the time budget.
func (r *Run) pauseStuck(w *watchRun) bool {
	if w.pausedSince.IsZero() {
		return false
	}
	age, budget := r.now().Sub(w.pausedSince), r.watchPauseBudget()
	if age < budget {
		return false
	}
	// A pause the rescue still owns is not stuck: neither the wait for its revive nor an in-flight resume may be
	// cut short, or the watch would end between the two halves of a rescue that was about to work.
	if r.rescueWindow(w) {
		return false
	}
	r.logf("watch: pause not cleared for %.0fs (limit %.0fs) at leg %d/step %d: %s", age.Seconds(),
		budget.Seconds(), w.record.Leg, w.record.Step, w.record.Why)
	return true
}

// bossWait is the wait the plan gives the boss of this leg (wait_s: the module waits for a patrolling boss and sends
// no event meanwhile), which the watchdog's idle limit allows on top of its own.
func (r *Run) bossWait(leg int) time.Duration {
	if r.route == nil || leg < 0 || leg >= len(r.route.Legs) || r.route.Legs[leg].Boss == nil {
		return 0
	}
	return time.Duration(r.route.Legs[leg].Boss.WaitS * float64(time.Second))
}

// watchdog pauses a running run whose event ring has not grown for cfg.Watchdog and reports. A paused run it
// reaches is reported, not paused again: there is nothing to send, and the caller ends the watch as paused-stuck.
// A pause the practice rescue still owns is not reached at all: a wipe's pause is silent by design, and the
// watchdog's idle clock runs on events the rescue itself must be allowed to answer.
func (r *Run) watchdog(ctx context.Context, w *watchRun) bool {
	if w.lastEvent.IsZero() {
		w.lastEvent = r.now()
		return false
	}
	idle := r.now().Sub(w.lastEvent)
	if idle < r.cfg.Watchdog+r.bossWait(w.record.Leg) {
		return false
	}
	if w.state == "paused" {
		if r.rescueWindow(w) {
			return false
		}
		age := idle
		if !w.pausedSince.IsZero() {
			age = r.now().Sub(w.pausedSince)
		}
		r.logf("watch: watchdog: paused for %.0fs with no new event (limit %.0fs) at leg %d/step %d: %s",
			age.Seconds(), r.cfg.Watchdog.Seconds(), w.record.Leg, w.record.Step, w.record.Why)
		return true
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
		// The resume is the watcher's own action and its event is not in the ring this read: the run starts its
		// event clock again here, or a watchdog shorter than the pause just cleared would fire on the stale idle.
		w.lastEvent = r.now()
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
	// The encounter packets are the exact boss state of a named fight, so while one is running the module's boss
	// report is not needed. Outside one, the report is the fallback: an instance whose script sends nothing (the
	// classic Ragefire Chasm bosses) gets the minute reads, and so does a boss the script never names as soon as a
	// named fight ends — a five-minute hold from the last packet would hide that kill. A forced read at the end is
	// never skipped.
	if !force && r.world.EncounterOpen() {
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

// watchBlind is one read's blind rule state: consecutive failures, when the first of the run came, and the last
// error, so a blind end can always name what blinded it.
type watchBlind struct {
	failures int
	since    time.Time
	last     error
}

// watchFail and watchOK are the read failures' own blind rule, kept per read: three failures in a row and
// Limits.Blind without a good read of the same kind end the watch. A status read that succeeds proves nothing about
// the event ring, so each read carries its own counters. The blind end logs its own reason line with the last read's
// error: an end with no reason never happens.
func (r *Run) watchFail(b *watchBlind, kind string, err error) {
	b.failures++
	b.last = err
	if b.failures == 1 {
		b.since = r.now()
	}
	if b.failures >= 3 && !b.since.IsZero() && r.now().Sub(b.since) >= r.lim.Blind {
		r.logf("watch: blind: %d %s reads failed over %.0fs; the last was: %v", b.failures, kind,
			r.now().Sub(b.since).Seconds(), b.last)
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
// (Result.StopUnconfirmed); a lost link ends it at once as before. The final instance read after the wait is bounded
// by FinishCallTimeout on its own, detached context: the confirmation may have spent the whole budget, and the read
// still has to reach the server for the summary to record the state the run ended with.
func (r *Run) watchFinish(ctx context.Context, w *watchRun) {
	if r.lost {
		r.watchSummary(w)
		return
	}
	if r.cfg.Attach && !w.have {
		// The watch never attached: the leader has no run for it (start:attach-timeout) or has one of another plan
		// (start:attach-plan), which is not this watch's to stop or to read further. Nothing was sent and nothing
		// is waited for; a run found in the confirmation reads below would only report our own stop unconfirmed.
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
	// The core's watch ends with the run's, whatever ended it: an ordinary end, a budget, or an error exit.
	{
		cctx, ccancel := context.WithTimeout(fctx, FinishCallTimeout)
		r.stopCombatlogWatch(cctx)
		ccancel()
	}
	if !r.lost {
		// The module erases a run's capture when the run ends, so the rows that arrived since the last incremental
		// read are read here, before the stop (or the wait for a self-ended run) goes any further.
		r.watchPullDecisions(ctx, w)
	}
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
		// The final read keeps its own bound: the confirmation loop above may have spent the whole cleanup budget, and
		// a child of the expired fctx would fail before the request reached the server, leaving the summary with the
		// previous boss state. Like the stop, it must run after the caller's context ended.
		octx, ocancel := context.WithTimeout(context.WithoutCancel(ctx), FinishCallTimeout)
		r.watchBosses(octx, w, true)
		ocancel()
	}
	r.watchSummary(w)
}

// watchSummary writes the watched run's counters into the recorder's summary.
func (r *Run) watchSummary(w *watchRun) {
	// The last rows of the fight: the stop and the final reads take time, and rows written between the last loop
	// tick and now belong to the run. The session's log is local, so this works after a lost link too.
	r.drainCombat(w)
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
		Frames: w.frames, CombatRows: w.combatRows, CombatDropped: w.combatLost,
		CombatBytes: r.rec.CombatBytes(), CombatTruncated: w.combatTruncated, CombatError: w.combatErr,
		PauseLog: w.pauseLog, Legs: w.legRecords(), StepLog: w.stepRecords(), LoosePulls: w.loosePulls, DeathLog: w.deaths,
		RescueLog: w.rescueLog, StopUnconfirmed: w.stopUnconfirmed, Encounters: w.encounters,
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
	// Every end of the watch says why in its own log, whatever ended it: the budgets, a module refusal, a lost
	// link, a blind read (which names its last error), or the run reaching done or stopped.
	reason := r.stopReason
	if reason == "" {
		reason = "end"
	}
	r.logf("watch: end reason=%s", reason)
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
	// The combat-log watch the run asked for (or could not start): group, unit, off, unsupported. It is set after
	// the recorder's summary replaces the result's, so both a recorded watch and a dry one carry it.
	res.Summary.CombatLogWatch = r.combatlogMode
	// A raid watch also leaves its summary.json in the tb-raid-walk shape beside the run's own file, on every end:
	// a short roster or a refused convert is as much a result raid-autopilot has to read as a finished run.
	r.writeRaidSummary(w, &res)
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
