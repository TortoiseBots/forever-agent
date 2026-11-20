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

	Log func(line string)
	Now func() time.Time

	// Abort reports why the run must stop (a lost lease renewal, an
	// interrupt): runRepeat checks it before every repeat and the guard
	// checks it before every GM line.
	Abort func() string

	started  string
	repLog   []map[string]any
	trials   map[string]map[string]any
	trialIDs []string
	repeats  map[string]int
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
	trialDir := joinPath(r.Out, id)
	if err := mkdirAll(trialDir); err != nil {
		return nil, err
	}
	// Trial.effective.json: the complete trial as it runs (merged variant).
	if err := dumpDoc(joinPath(trialDir, scenario.SnapshotName), trial); err != nil {
		return nil, err
	}
	for i := 1; i <= repeats; i++ {
		if err := r.runRepeat(ctx, trial, id, i); err != nil {
			return nil, err
		}
	}
	doc, err := score.ScoreTrialE(trialDir, trial)
	if err != nil {
		return nil, err
	}
	r.noteTrial(trial, id, repeats)
	if err := r.writeRUN(); err != nil {
		return nil, err
	}
	return doc, nil
}

// runRepeat runs one repeat: setup, marks start, collect, situation,
// teardown, setup.json, normalise + score.
func (r *Runner) runRepeat(ctx context.Context, trial map[string]any, id string, index int) error {
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
	guids := Guids{}
	collect := NewCollector(r.Client, repDir, marks, guids, r.Ledger)
	guard := NewGuard(r.Client, GuardOptions{Ledger: r.Ledger, Marks: marks, DryRun: r.dry(), Abort: r.Abort})
	rep := &Repeat{Index: index, Dir: repDir, Trial: trial, TrialID: id,
		Client: r.Client, Guard: guard, Marks: marks, Collect: collect, Ledger: r.Ledger}

	var setupDoc map[string]any = map[string]any{"ok": true, "checks": []any{}}
	if r.Setup != nil {
		doc, err := r.Setup.Run(ctx, rep)
		if err != nil {
			return err
		}
		if doc != nil {
			setupDoc = doc
		}
	}
	rep.Guids = GuidsOf(setupDoc)
	collect.guids = rep.Guids
	if err := r.writeSetup(repDir, setupDoc); err != nil {
		return err
	}

	marks.Add(map[string]any{"kind": "setup_ok", "guids": setupDoc["guids"]})
	collect.Start(ctx)
	outcome := map[string]any{"ended": "dry"}
	if r.Situation != nil {
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
	teardown := r.teardown(rep)
	marks.Add(map[string]any{"kind": "teardown", "rows": teardown})
	failed := failedChecks(setupDoc)
	setupDoc["ok"] = len(failed) == 0 && outcomeInvalid(outcome) == ""
	if reason := outcomeInvalid(outcome); reason != "" {
		setupDoc["invalid_reason"] = reason
	} else if len(failed) > 0 {
		setupDoc["invalid_reason"] = checkEvidence(failed[0])
	}
	setupDoc["outcome"] = outcome
	if err := r.writeSetup(repDir, setupDoc); err != nil {
		return err
	}
	if _, err := events.Normalise(repDir, trial); err != nil {
		return err
	}
	if _, err := score.ScoreRepE(repDir, trial, nil); err != nil {
		return err
	}
	r.noteRep(id, index, boolOf(setupDoc["ok"]), strOf(outcome["ended"]))
	r.logf("%s rep%d: %s", id, index, strOf(outcome["ended"]))
	return nil
}

// teardown kills the ledger's summons (gm_kill through the TB channel in
// later rounds; here a gm die line for --dry ledger rows) and writes the
// per-repeat teardown rows.
func (r *Runner) teardown(rep *Repeat) []map[string]any {
	rows := []map[string]any{}
	for _, guid := range rep.Ledger.SummonGUIDs() {
		if guid == "" {
			continue
		}
		rows = append(rows, map[string]any{"id": "mobs_dead", "ok": true, "value": guid,
			"evidence": "dry cleanup: summon " + guid})
	}
	if rep.Guids.Bot != "" {
		rows = append(rows, map[string]any{"id": "hire_removed", "ok": true,
			"value": rep.Guids.Bot, "evidence": "dry cleanup: bot released"})
	}
	return rows
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

func (r *Runner) dry() bool { return r.Setup == nil || isDrySetup(r.Setup) }

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
	return dumpDoc(joinPath(r.Out, "RUN.json"), doc)
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
