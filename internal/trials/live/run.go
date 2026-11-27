package live

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
	"github.com/pjerra/forever-agent/internal/trials/trialdefs"
)

// Setup is one repeat's setup phase: read-backs that build setup.json
// ({bot_name, extra_names, checks}). The A2 round ships only the --dry
// scripted setup; A3 ports the real one.
type Setup interface {
	Run(ctx context.Context, r *Repeat) (setup map[string]any, err error)
}

// Situation is one repeat's situation phase: setup → collect → teardown
// around it. The A2 round ships only the --dry scripted situation; A4
// ports tank_damage and the rest.
type Situation interface {
	Run(ctx context.Context, r *Repeat) (outcome map[string]any, err error)
}

// Repeat is one trial repeat: its dirs, trial doc, client, guard, marks,
// collector, ledger and live state the setup/situation scripts share.
type Repeat struct {
	Index   int
	Dir     string
	Trial   map[string]any
	TrialID string
	Client  *Client
	Guard   *Guard
	Marks   *Marks
	Collect *Collector
	Ledger  *Ledger
	Guids   Guids

	// BotName and ExtraNames are the hires the setup provisioned and proved
	// (the situation brings them into the arena); Hires is every character
	// the repeat acquired, keyed by name and filled when the login
	// succeeded, before any later check can fail. The teardown works from
	// Hires, never from the setup document: a hire whose party identity was
	// never proved is still logged out and cleared.
	BotName    string
	ExtraNames []string
	Hires      []hireTarget

	// GrantPath is the item grant file of the items the setup gave; the
	// teardown clears them (see items.go). Empty: in-memory grants only.
	GrantPath string

	// Server is the test server's name (item grants are per server).
	Server string

	// Unidentified counts spawns the situation could not identify (an
	// `.npc add temp` that worked but whose unit was never claimed and had no
	// candidate): teardown cannot kill them and says so.
	Unidentified int

	// ExtraChecks are the situation's own read-backs (mobs_ready); the
	// repeat appends them to setup.json's checks when the situation ends.
	ExtraChecks []map[string]any
}

// Runner owns one trials run: the out dir, the client, and the interfaces
// the round wires (scripted in --dry).
type Runner struct {
	Out    string
	Server string
	Label  string
	Client *Client
	Ledger *Ledger
	Hold   *Hold

	Setup     Setup
	Situation Situation

	// tbRunner overrides the `.tb` transport of the teardown (tests); nil
	// means the serve-backed runner over the repeat's client.
	tbRunner *TBRunner

	Log func(line string)
	Now func() time.Time

	// TeardownTimeout bounds one teardown (0 is two minutes); TeardownWait and
	// TeardownPoll bound the heal read-back (0 is 6 s and 500 ms);
	// LogoutWait bounds the wait for a hire to leave the party roster after
	// `.tb logout` (0 is 20 s), polled every TeardownPoll;
	// TeardownAbort, when set, is the only abort the teardown's GM hands
	// honour (nil: none, so Ctrl-C cannot cancel the kills).
	TeardownTimeout time.Duration
	TeardownWait    time.Duration
	TeardownPoll    time.Duration
	LogoutWait      time.Duration
	TeardownAbort   func() string

	// Abort reports why the run must stop (a lost lease renewal, an
	// interrupt): runRepeat checks it before every repeat and the guard
	// checks it before every GM line.
	Abort func() string

	started  string
	repLog   []map[string]any
	trials   map[string]map[string]any
	trialIDs []string
	repeats  map[string]int
	skipped  map[string]string
	lastRep  *Repeat
	reaped   map[string]bool // summon guids a repeat teardown proved dead or gone
	unproven map[string][]string
}

// findTrial resolves a TRIAL arg: an embedded path, an embedded id, or a
// file on disk (plus @variant).
func findTrial(arg string) (map[string]any, error) {
	if strings.Contains(arg, "@") {
		parts := strings.SplitN(arg, "@", 2)
		base, err := findTrial(parts[0])
		if err != nil {
			return nil, err
		}
		return applyVariant(base, parts[1])
	}
	if strings.HasSuffix(arg, ".json") {
		raw, err := readFile(arg)
		if err == nil {
			doc, derr := pyjson.Unmarshal(raw)
			if derr != nil {
				return nil, derr
			}
			if obj, ok := pyjson.Object(doc); ok {
				return obj, nil
			}
			return nil, errf("trial %s: not an object", arg)
		}
	}
	for _, p := range trialdefs.Paths() {
		if p == arg || strings.TrimSuffix(p, ".json") == arg {
			raw, err := trialdefs.Read(p)
			if err != nil {
				return nil, err
			}
			doc, err := pyjson.Unmarshal(raw)
			if err != nil {
				return nil, err
			}
			if obj, ok := pyjson.Object(doc); ok {
				return obj, nil
			}
			return nil, errf("trial %s: not an object", arg)
		}
	}
	if t := scenario.FindTrialByID(arg); t != nil {
		out := map[string]any{}
		for k, v := range t {
			out[k] = v
		}
		return out, nil
	}
	if t := scenario.FindFixtureByID(arg); t != nil {
		out := map[string]any{}
		for k, v := range t {
			out[k] = v
		}
		return out, nil
	}
	return nil, errf("unknown trial %q", arg)
}

// applyVariant merges a variant over its trial through
// scenario.MergeVariant: variants keyed by "name" with an "override" object,
// validated the way the scorer validates them.
func applyVariant(base map[string]any, variant string) (map[string]any, error) {
	variants, _ := pyjson.List(base["variants"])
	for _, v := range variants {
		obj, ok := pyjson.Object(v)
		if !ok || pyjson.Str(obj["name"]) != variant {
			continue
		}
		merged, errs := scenario.MergeVariant(base, obj)
		if len(errs) > 0 {
			return nil, errf("variant %q invalid: %s", variant, errs[0])
		}
		if merged == nil {
			continue
		}
		out := map[string]any{}
		for k, val := range merged {
			out[k] = val
		}
		out["id"] = pyjson.Str(base["id"]) + scenario.VariantSep + variant
		delete(out, "variants")
		return out, nil
	}
	return nil, errf("unknown variant %q", variant)
}

// trialRepeats is the trial's own repeat count (--repeats 0 means this):
// the `repeats` field, 1 when absent or bad.
func trialRepeats(trial map[string]any) int {
	if n, ok := pyjson.Int64(trial["repeats"]); ok && n >= 1 && n <= 100 {
		return int(n)
	}
	if f, ok := pyjson.Num(trial["repeats"]); ok && f >= 1 && f <= 100 {
		return int(f)
	}
	return 1
}

// RunTrial runs trialID repeats times into Out/<id>/, writing
// trial.effective.json first, then per repeat setup → situation → collect →
// teardown, RUN.json, and finally normalise + score. It returns the trial
// verdict doc.
func (r *Runner) RunTrial(ctx context.Context, trialArg string, repeats int) (map[string]any, error) {
	trial, err := findTrial(trialArg)
	if err != nil {
		return nil, err
	}
	id := pyjson.Str(trial["id"])
	if id == "" {
		return nil, errf("trial %q has no id", trialArg)
	}
	if repeats <= 0 {
		repeats = trialRepeats(trial)
	}
	trialDir := TrialDirOf(r.Out, id)
	if err := mkdirAll(trialDir); err != nil {
		return nil, err
	}
	// Trial.effective.json: the complete trial as it runs (merged variant).
	if err := dumpDoc(joinPath(trialDir, scenario.SnapshotName), trial); err != nil {
		return nil, err
	}
	// The hires, items and summons of this trial are cleaned on EVERY way out
	// (an error, SKIPPED, a disconnect, a panic) on a teardown context of its
	// own, not only after the last repeat finished normally.
	tornDown := false
	var unproven []string
	teardownTrial := func() {
		if tornDown {
			return
		}
		tornDown = true
		if rep := r.lastRep; rep != nil && rep.TrialID == id {
			tctx, cancel := r.teardownCtx(ctx)
			defer cancel()
			unproven = r.trialTeardown(tctx, rep, id)
			if len(unproven) > 0 {
				r.noteTeardownUnproven(id, unproven)
			}
		}
	}
	defer teardownTrial()
	var trialErr error
	skipNeeds := ""
	for i := 1; i <= repeats; i++ {
		if err := r.runRepeat(ctx, trial, trialArg, id, i); err != nil {
			// SKIPPED stops the trial after rep 1: every repeat
			// would skip identically (the capability is per
			// server, not per rep), so running on wastes leases.
			if needs, ok := IsSkipped(err); ok {
				r.logf("%s: SKIPPED needs=%s", id, needs)
				skipNeeds = needs
				break
			}
			trialErr = err
			break
		}
		// min_rep_gap_s: the hire's item cooldowns are per character and
		// survive a repeat, so the next repeat waits the trial's gap out.
		if i < repeats {
			if gap := RepGap(trial); gap > 0 {
				r.waitGap(ctx, gap)
			}
		}
	}
	if skipNeeds != "" {
		// The teardown runs on every way out of the trial. Its problems are
		// recorded in RUN.json and handed back with the result: a SKIP whose
		// slot is still dirty must stop a sweep, never read as success.
		teardownTrial()
		if werr := r.writeRUN(); werr != nil {
			r.logf("%s: RUN.json after a skip: %v", id, werr)
		}
		doc := map[string]any{"trial": id, "verdict": "SKIPPED", "needs": skipNeeds}
		if len(unproven) > 0 {
			return doc, &TeardownUnproven{Trial: id, Problems: unproven}
		}
		return doc, nil
	}
	teardownTrial()
	if trialErr != nil {
		// The repeat ended early (an aborted call, a lost session). The
		// teardown still ran: its problems must not vanish with the early
		// return, so RUN.json records them and the error carries them (a
		// sweep stops instead of starting the next trial on a dirty slot).
		r.noteTrial(trial, id, repeats)
		if werr := r.writeRUN(); werr != nil {
			r.logf("%s: RUN.json after an aborted trial: %v", id, werr)
		}
		if len(unproven) > 0 {
			return nil, &TeardownUnproven{Trial: id, Problems: unproven, Cause: trialErr}
		}
		return nil, trialErr
	}
	var doc map[string]any
	if kindOf(trial) == "dummy" {
		doc, err = scoreDummyTrial(trialDir, trialArg, trial)
	} else {
		doc, err = score.ScoreTrialE(trialDir, trial)
	}
	if err != nil {
		return nil, err
	}
	r.noteTrial(trial, id, repeats)
	if err := r.writeRUN(); err != nil {
		return nil, err
	}
	if len(unproven) > 0 {
		// The trial was scored, but the slot is not provably clean: the caller
		// (a sweep) must not start another trial on it.
		return doc, &TeardownUnproven{Trial: id, Problems: unproven}
	}
	return doc, nil
}

// TeardownUnproven is a trial whose teardown could not be proven (items not
// cleared, a hire not logged out, the forced role not cleared): the next
// trial would start on a dirty slot, so a sweep stops here. Cause is the
// error that ended the trial early, when the teardown ran on that way out.
type TeardownUnproven struct {
	Trial    string
	Problems []string
	Cause    error
}

func (e *TeardownUnproven) Error() string {
	msg := "teardown of " + e.Trial + " not proven: " + strings.Join(e.Problems, "; ")
	if e.Cause != nil {
		msg += " (after: " + e.Cause.Error() + ")"
	}
	return msg
}

func (e *TeardownUnproven) Unwrap() error { return e.Cause }

// noteTeardownUnproven records the unproven teardown in RUN.json's
// `teardown_unproven`.
func (r *Runner) noteTeardownUnproven(id string, problems []string) {
	if r.unproven == nil {
		r.unproven = map[string][]string{}
	}
	r.unproven[id] = append([]string(nil), problems...)
}

// waitGap sleeps gap in one-second steps, stopping at once on an abort.
func (r *Runner) waitGap(ctx context.Context, gap time.Duration) {
	end := time.Now().Add(gap)
	for time.Now().Before(end) {
		if r.Abort != nil {
			if why := r.Abort(); why != "" {
				return
			}
		}
		step := time.Second
		if left := time.Until(end); left < step {
			step = left
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(step):
		}
	}
}

// runRepeat runs one repeat: setup, marks start, collect, situation,
// teardown, setup.json, normalise + score.
func (r *Runner) runRepeat(ctx context.Context, trial map[string]any, trialArg, id string, index int) error {
	if r.Abort != nil {
		if why := r.Abort(); why != "" {
			return &Disconnected{err: fmt.Errorf("%s", why)}
		}
	}
	repDir := joinPath(joinPath(r.Out, id), "rep"+itoa(index))
	if err := mkdirAll(repDir); err != nil {
		return err
	}
	r.logf("%s rep%d: setup", id, index)
	marks := NewMarks(joinPath(repDir, "marks.jsonl"))
	marks.Start("")
	// The guard's "self" class is the session's own character: one status
	// read, so a GM line for "self" can never name another player.
	playerGUID := ""
	if st, serr := r.Client.StatusOf(ctx); serr == nil {
		playerGUID = normGUIDOf(st.GUID)
	} else if IsDisconnected(serr) {
		return errDisconnected(serr)
	}
	rep := &Repeat{Index: index, Dir: repDir, Trial: trial, TrialID: id,
		Client: r.Client, Marks: marks, Ledger: r.Ledger}
	collect := NewCollector(r.Client, repDir, marks, &rep.Guids, r.Ledger)
	guard := NewGuard(r.Client, GuardOptions{Ledger: r.Ledger, Marks: marks,
		PlayerGUID: playerGUID, DryRun: r.dry(), Abort: r.Abort,
		Summons: func() []string { return r.Ledger.SummonGUIDs() }})
	rep.Guard, rep.Collect = guard, collect
	rep.Server = r.Server
	r.lastRep = rep
	// Whatever way this repeat ends, its summons are torn down (on a context
	// of their own) and the situation lets go of its arena AFTER that.
	situationStarted, tornDown := false, false
	defer func() {
		if situationStarted && !tornDown {
			tctx, cancel := r.teardownCtx(ctx)
			rows := r.repeatTeardown(tctx, rep)
			cancel()
			marks.Add(map[string]any{"kind": "teardown", "rows": rows})
		}
		if fin, ok := r.Situation.(interface{ Finish() }); ok {
			fin.Finish()
		}
	}()
	// The serve confirms the GM channel once per session, and that probe's
	// help text can leak into the first command's reply: spend it here on a
	// line the setup sends anyway, before any module command is read.
	if !r.dry() && playerGUID != "" {
		_ = guard.Send(ctx, "gm_visible_on", TargetSelf, playerGUID)
		// The probe's help block is about 100 lines and trickles in after
		// the call: give it the quiet window to drain, or the first module
		// reply reads "### USAGE" as a refusal.
		sleepCtx(ctx, 2*time.Second)
	}

	var setupDoc map[string]any = map[string]any{"ok": true, "checks": []any{}}
	setupFailed := false
	if r.Setup != nil {
		doc, err := r.Setup.Run(ctx, rep)
		if err != nil {
			// A missing server capability (TB-1 not live) SKIPs the
			// whole trial: every repeat ends SKIPPED with
			// needs=<check> in RUN.json, never a FAIL. The doc
			// carries the needs and the run stops before collect.
			if needs, ok := IsSkipped(err); ok {
				why := err.Error()
				if !strings.HasPrefix(why, "SKIPPED needs=") {
					why = "SKIPPED needs=" + needs + ": " + why
				}
				setupDoc = map[string]any{"ok": false, "checks": []any{},
					"invalid_reason": why,
					"bot_name":       nil, "extra_names": []any{},
					"guids": map[string]any{"player": nil, "bot": nil, "pet": nil, "mobs": []any{}, "marked": nil}}
				setupDoc["skipped"] = map[string]any{"needs": needs}
				if werr := r.writeSetup(repDir, setupDoc); werr != nil {
					return werr
				}
				r.noteRep(id, index, false, "skipped")
				marks.Add(map[string]any{"kind": "teardown", "rows": []any{}})
				r.noteSkipped(trial, id, trialRepeats(trial), needs)
				if werr := r.writeRUN(); werr != nil {
					return werr
				}
				return &skippedTrial{needs: needs, why: err.Error()}
			}
			return err
		}
		if doc != nil {
			setupDoc = doc
		}
		setupFailed = !boolOf(setupDoc["ok"])
	}
	rep.Guids = GuidsOf(setupDoc)
	collect.SetGuids(&rep.Guids)
	guard.SetHire(rep.Guids.Bot)
	rep.BotName, _ = setupDoc["bot_name"].(string)
	rep.ExtraNames = nil
	if list, ok := setupDoc["extra_names"].([]any); ok {
		for _, e := range list {
			if name, ok := e.(string); ok && name != "" {
				rep.ExtraNames = append(rep.ExtraNames, name)
			}
		}
	}
	// rep.Hires (the teardown list) is filled by the provisioner's callbacks
	// as each character logs in and its guid is read, never from this
	// filtered document: a hire that failed its party identity check is
	// still released.
	if err := r.writeSetup(repDir, setupDoc); err != nil {
		return err
	}

	if setupFailed {
		// A failed setup check means the repeat cannot score: there is no
		// fight to run (the Python returns before the situation).
		marks.Add(map[string]any{"kind": "setup_failed", "reason": setupDoc["invalid_reason"]})
		setupDoc["outcome"] = map[string]any{"ended": "setup_failed", "ok": false}
		if err := r.writeSetup(repDir, setupDoc); err != nil {
			return err
		}
		r.noteRep(id, index, false, "setup_failed")
		r.logf("%s rep%d: setup failed: %s", id, index, strOf(setupDoc["invalid_reason"]))
		return nil
	}
	marks.Add(map[string]any{"kind": "setup_ok", "guids": setupDoc["guids"]})
	// The core's GM combat-log watch on the trial's subject (the player): the serve then collects the fight's rows
	// from any distance, which is what the hurt-and-heal situations need when the healer stands off. A core without
	// the command refuses it: one log line, and the repeat goes on with the in-range log.
	if playerGUID != "" && !r.dry() {
		if err := guard.CombatLogWatch(ctx, playerGUID, "unit"); err != nil && !IsDisconnected(err) {
			r.logf("%s rep%d: combat-log watch: %v (the log is in range only)", id, index, err)
		}
	}
	collect.Start(ctx)
	outcome := map[string]any{"ended": "dry"}
	if r.Situation != nil {
		situationStarted = true
		got, err := r.Situation.Run(ctx, rep)
		if err != nil {
			collect.Stop()
			if IsDisconnected(err) {
				r.noteRep(id, index, false, "disconnected")
				if werr := r.writeSetup(repDir, setupDoc); werr != nil {
					return werr
				}
				return err
			}
			return err
		}
		if got != nil {
			outcome = got
		}
		if len(rep.ExtraChecks) > 0 {
			if list, ok := setupDoc["checks"].([]any); ok {
				for _, c := range rep.ExtraChecks {
					list = append(list, c)
				}
				setupDoc["checks"] = list
			}
		}
	}
	collect.Stop()
	if lost := collect.Lost(); lost != "" {
		setupDoc["ok"] = false
		setupDoc["invalid_reason"] = lost
		if err := r.writeSetup(repDir, setupDoc); err != nil {
			return err
		}
		r.noteRep(id, index, false, "disconnected")
		marks.Add(map[string]any{"kind": "teardown", "rows": []any{}})
		return &Disconnected{err: fmt.Errorf("%s", lost)}
	}
	// The situation added the mobs it summoned: setup.json's guid table is
	// the scorer's role map, so it is rewritten from the repeat's own table.
	setupDoc["guids"] = guidsDoc(rep.Guids)
	tornDown = true
	tctx, tcancel := r.teardownCtx(ctx)
	teardown := r.repeatTeardown(tctx, rep)
	tcancel()
	marks.Add(map[string]any{"kind": "teardown", "rows": teardown})
	failed := failedChecks(setupDoc)
	reason := outcomeInvalid(outcome)
	setupDoc["ok"] = len(failed) == 0 && reason == ""
	// No arena / no mobs / an unported option = no scored fight happened: the
	// failed mobs_ready row (and anything the missing fight left unproved) is
	// its consequence, so the reason says WHY there was no fight.
	if noFightEnd(strOf(outcome["ended"])) && reason != "" {
		setupDoc["invalid_reason"] = reason
	} else if len(failed) > 0 {
		setupDoc["invalid_reason"] = checkEvidence(failed[0])
	} else if reason != "" {
		setupDoc["invalid_reason"] = reason
	}
	setupDoc["outcome"] = outcome
	if err := r.writeSetup(repDir, setupDoc); err != nil {
		return err
	}
	if _, err := events.Normalise(repDir, trial); err != nil {
		return err
	}
	if kindOf(trial) == "dummy" {
		if _, _, err := scoreDummyRep(repDir, trial, trialArg); err != nil {
			return err
		}
	} else if _, err := score.ScoreRepE(repDir, trial, nil); err != nil {
		return err
	}
	r.noteRep(id, index, boolOf(setupDoc["ok"]), strOf(outcome["ended"]))
	r.logf("%s rep%d: %s", id, index, strOf(outcome["ended"]))
	return nil
}

func (r *Runner) writeSetup(repDir string, doc map[string]any) error {
	doc["slot"] = r.slotID()
	return dumpDoc(joinPath(repDir, "setup.json"), doc)
}

func (r *Runner) slotID() string {
	if r.Hold != nil {
		return r.Hold.Slot
	}
	return ""
}

// noteSkipped records a SKIPPED trial: RUN.json names it with its needs so
// a card reader never mistakes it for a FAIL.
func (r *Runner) noteSkipped(trial map[string]any, id string, n int, needs string) {
	r.noteTrial(trial, id, n)
	if r.skipped == nil {
		r.skipped = map[string]string{}
	}
	r.skipped[id] = needs
}

func (r *Runner) noteRep(trial string, rep int, valid bool, ended string) {
	r.repLog = append(r.repLog, map[string]any{"trial": trial, "rep": rep,
		"slot": r.slotID(), "valid": valid, "ended": ended})
}

// noteTrial records one finished trial for the run card: RUN.json lists
// every trial the run completed, not just the last one.
func (r *Runner) noteTrial(trial map[string]any, id string, repeats int) {
	if r.trials == nil {
		r.trials = map[string]map[string]any{}
	}
	if r.repeats == nil {
		r.repeats = map[string]int{}
	}
	if _, ok := r.trials[id]; !ok {
		r.trialIDs = append(r.trialIDs, id)
	}
	r.trials[id] = trial
	r.repeats[id] = repeats
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(format, args...))
	}
}

func (r *Runner) dry() bool { return r.Setup != nil && isDrySetup(r.Setup) }

// writeRUN rewrites RUN.json with every trial finished so far, so the run
// record names the whole card even when the next trial has not run yet.
func (r *Runner) writeRUN() error {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	started := r.started
	if started == "" {
		started = now.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		r.started = started
	}
	trials := make([]any, 0, len(r.trialIDs))
	for _, id := range r.trialIDs {
		trials = append(trials, id)
	}
	repeats := map[string]any{}
	for id, n := range r.repeats {
		repeats[id] = n
	}
	doc := map[string]any{
		"name": "forever-trials", "label": r.Label, "server": r.Server,
		"trials": trials, "repeats_run": repeats,
		"reps": repLogAny(r.repLog), "started": started,
		"ended":           now.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		"trial_snapshots": true,
	}
	if len(r.unproven) > 0 {
		up := map[string]any{}
		for id, problems := range r.unproven {
			list := make([]any, 0, len(problems))
			for _, p := range problems {
				list = append(list, p)
			}
			up[id] = list
		}
		doc["teardown_unproven"] = up
	}
	if len(r.skipped) > 0 {
		sk := map[string]any{}
		for id, needs := range r.skipped {
			sk[id] = "needs=" + needs
		}
		doc["skipped"] = sk
	}
	return dumpDoc(joinPath(r.Out, "RUN.json"), doc)
}

// guidsDoc renders a Guids table the way setup.json carries it.
func guidsDoc(g Guids) map[string]any {
	nilStr := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	extras := make([]any, 0, len(g.Extras))
	for _, e := range g.Extras {
		extras = append(extras, e)
	}
	mobs := make([]any, 0, len(g.Mobs))
	for _, m := range g.Mobs {
		mobs = append(mobs, m)
	}
	return map[string]any{
		"player": nilStr(g.Player), "bot": nilStr(g.Bot), "pet": nilStr(g.Pet),
		"extras": extras, "mobs": mobs, "marked": nilStr(g.Marked),
	}
}

// repLogAny converts the rep log to []any: pyjson.Marshal rejects typed slices.
func repLogAny(rows []map[string]any) []any {
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	return out
}

func outcomeInvalid(outcome map[string]any) string {
	s, _ := outcome["invalid_reason"].(string)
	return s
}

func failedChecks(setup map[string]any) []map[string]any {
	var out []map[string]any
	list, _ := setup["checks"].([]any)
	for _, c := range list {
		m, _ := c.(map[string]any)
		if m == nil || pyjson.Str(m["id"]) == "config_read" {
			continue
		}
		if !boolOf(m["ok"]) {
			out = append(out, m)
		}
	}
	return out
}

func checkEvidence(m map[string]any) string {
	if s, _ := m["evidence"].(string); s != "" {
		return s
	}
	return pyjson.Str(m["id"])
}

func strOf(v any) string { return pyjson.Str(v) }

// TrialIDs lists the embedded trial ids matching args (ids or paths).
func TrialIDs(args []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, arg := range args {
		trial, err := findTrial(arg)
		if err != nil {
			return nil, err
		}
		id := pyjson.Str(trial["id"])
		if id == "" {
			return nil, errf("trial %q has no id", arg)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}
