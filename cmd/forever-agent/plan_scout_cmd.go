package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/planscout"
	"github.com/pjerra/forever-agent/internal/raidwalk"
	"github.com/pjerra/forever-agent/internal/routeedit"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// The scout's exit codes.
const (
	// exitPlanScoutFail: at least one step failed (or warned, with --fail-on warn), or --apply-fixes was refused.
	exitPlanScoutFail = 27
	// exitPlanScoutLiveAborted: the live pass stopped part-way (offline verdicts complete, live ones skipping).
	exitPlanScoutLiveAborted = 28
	// exitLivePhaseRefused: a live phase failed or was refused (no test server, GM mode, module, toolkit, instance).
	exitLivePhaseRefused = 18
	// exitPlanScoutNoNavmesh: the navmesh could not answer, so nothing was checked.
	exitPlanScoutNoNavmesh = 1
)

// planScoutNav is what plan-scout needs from the navmesh; navmesh.Planner is one and the tests inject a fake.
type planScoutNav interface {
	planscout.Nav
	Close()
}

// scoutForeverCheckNav wraps a Forever planner for dungeon.CheckPlan without implementing ServerQuerier,
// so the edited-plan check runs Plan instead of ServerPath on v16 tiles.
type scoutForeverCheckNav struct{ n planScoutNav }

func (s scoutForeverCheckNav) Plan(mapID uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	return s.n.Plan(mapID, from, to)
}
func (s scoutForeverCheckNav) Raycast(mapID uint32, from, to navmesh.Vec3) (navmesh.Ray, error) {
	return s.n.Raycast(mapID, from, to)
}
func (s scoutForeverCheckNav) Nearest(mapID uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	return s.n.Nearest(mapID, p)
}
func (s scoutForeverCheckNav) SetAvoid(mapID uint32, boxes []navmesh.Box) {
	if setter, ok := s.n.(dungeon.AvoidSetter); ok {
		setter.SetAvoid(mapID, boxes)
	}
}

// planScoutOpen opens the navmesh of one mmaps folder; the caller closes it.
var planScoutOpen = func(dir string) planScoutNav {
	return navmesh.NewPlanner(dir)
}

const planScoutUsage = `usage: forever-agent plan-scout <dungeon> [--dir dungeons] [--sql FILE] [--mmaps DIR] [--spawns DIR]
    [--from LEG] [--until LEG] [--party-level N] [--out DIR] [--json]
    [--fix-out FILE] [--fix-in FILE] [--apply-fixes DIR] [--fail-on fail|warn]
    [--live <server>] [--slot N] [--holder NAME] [--slots FILE] [--binary PATH]
    [--walk-budget 5m] [--max-minutes 20] [--instance-cap-per-account N] [--instance-cap-wait 20m] [--instance-cap-fail] [--instance-cap-reset]
    [--forever [--parent MAP=PARENT]] [--live --slot DIR]`

// planScoutOptions are the command's flags.
type planScoutOptions struct {
	dir         string
	sqlFile     string
	mmaps       string
	spawns      string
	from, until string
	partyLevel  int
	json        bool
	failOn      string
	// forever reads a ForeverCore export with LoadForeverDB on the v16 tiles; parents is its parent-map rule.
	forever bool
	parents foreverParents
	// serveSlot is the serve dir of the Forever live pass (--live --slot DIR).
	serveSlot string
	// live flags (parsed; the live phase is built by W3 and refuses here).
	live             string
	slot             int
	holder           string
	slots            string
	binary           string
	walkBudget       string
	maxMinutes       int
	instanceCapWait  string
	instanceCapFail  bool
	instanceCapReset bool
	// instanceCapPerAccount is the core's fresh instances per account and hour; instanceCapSet tells a flag from the
	// default (the flag wins over instance-cap-per-account.json's per-server value).
	instanceCapPerAccount int
	instanceCapSet        bool
	// walkBudgetD and capWaitD are walkBudget and instanceCapWait parsed (the live pass's waits).
	walkBudgetD time.Duration
	capWaitD    time.Duration
}

// planScoutCmd checks a plan's exported rows offline (README, "Plan scout"): the placement lints, the four scout rules and
// the joiner estimate, a report and a fix file; with --live it adds the live pass on a test server (turtle: the toolkit;
// --forever: a serve slot).
func planScoutCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	// The Forever live pass reads --live as a bool and --slot as a serve dir; split them out before the flag package
	// takes them as the turtle pass's server and slot number.
	var slot string
	var ferr error
	if args, slot, ferr = foreverLiveArgs(args); ferr != nil {
		fmt.Fprintf(stderr, "plan-scout: %v\n", ferr)
		return exitUsage
	}
	fs := flag.NewFlagSet("plan-scout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := planScoutOptions{serveSlot: slot}
	var out, fixOut, fixIn, applyTo string
	fs.StringVar(&o.dir, "dir", "dungeons", "the folder with the plan files")
	fs.StringVar(&o.sqlFile, "sql", "", "check a deployed world migration's rows instead of the plan file's export")
	fs.StringVar(&o.mmaps, "mmaps", defaultMmapsDir(), "folder with the server's mmaps (<map>.mmap and <map>*.mmtile)")
	fs.StringVar(&o.spawns, "spawns", "", "the map's world export (default: <dir>/../tools/dungeongen/testdata/mapN)")
	fs.StringVar(&o.from, "from", "", "first leg to report (an exported leg number or a leg name)")
	fs.StringVar(&o.until, "until", "", "last leg to report (inclusive)")
	fs.IntVar(&o.partyLevel, "party-level", 0, "the party's level for the aggro radius (default: the plan's band high end)")
	fs.StringVar(&out, "out", "", "folder for scout-<plan>.json and scout-<plan>.txt (default ./plan-scout-<plan>-<stamp>)")
	fs.BoolVar(&o.json, "json", false, "print the report as JSON")
	fs.StringVar(&fixOut, "fix-out", "", "write the fix file (tb-scout-fix v1) here, also with no fixes")
	fs.StringVar(&fixIn, "fix-in", "", "use the fixes of this recorded fix file (tb-scout-fix v1) instead of this pass's own")
	fs.StringVar(&applyTo, "apply-fixes", "", "write the patched plan copy and its diff to this folder (must not exist or be empty)")
	fs.StringVar(&o.failOn, "fail-on", "fail", "fail or warn: which step verdict fails the run")
	fs.StringVar(&o.live, "live", "", "run the live pass on this test server (m910q, tortoise-test2, tortoise-test3)")
	fs.IntVar(&o.slot, "slot", 0, "the slot to use (3 is never allowed)")
	fs.StringVar(&o.holder, "holder", "", "the lease holder's name (default plan-scout-<plan>)")
	fs.StringVar(&o.slots, "slots", "", "the slots file")
	fs.StringVar(&o.binary, "binary", "", "the server binary path")
	fs.StringVar(&o.walkBudget, "walk-budget", "5m", "live walk budget")
	fs.IntVar(&o.maxMinutes, "max-minutes", 20, "live time ceiling in minutes")
	fs.IntVar(&o.instanceCapPerAccount, "instance-cap-per-account", raidwalk.InstanceCapPerAccount, "the core's limit of fresh instance ids one account may enter in an hour (stock 5; the server's entry in instance-cap-per-account.json is used when this flag is not given)")
	fs.StringVar(&o.instanceCapWait, "instance-cap-wait", "20m", "how long to wait for a free instance place")
	fs.BoolVar(&o.instanceCapFail, "instance-cap-fail", false, "fail when the instance ledger is at its cap")
	fs.BoolVar(&o.instanceCapReset, "instance-cap-reset", false, "reset the instance ledger entry")
	fs.BoolVar(&o.forever, "forever", false, "read a ForeverCore world export (LoadForeverDB) on the v16 tiles")
	fs.Var(&o.parents, "parent", "with --forever: the server's parent-map rule, MAP=PARENT (repeatable)")
	flags, pos, err := splitFlagArgs(fs, args)
	if err != nil {
		return exitUsage
	}
	if err := fs.Parse(flags); err != nil {
		return exitUsage
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "instance-cap-per-account" {
			o.instanceCapSet = true
		}
	})
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "plan-scout: "+format+"\n", a...)
		return exitUsage
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, planScoutUsage)
		return fail("name one plan")
	}
	name := pos[0]
	if !planName.MatchString(name) {
		return fail("%q is not a plan name (a-z, 0-9, _ and -)", name)
	}
	if o.failOn != "fail" && o.failOn != "warn" {
		return fail("--fail-on takes fail or warn")
	}
	if fixIn != "" && (o.live != "" || o.serveSlot != "") {
		return fail("--fix-in replays a recorded fix list and cannot be combined with --live")
	}
	if o.slot == 3 {
		fmt.Fprintln(stderr, "plan-scout: slot 3 is never used")
		return exitSlotRefused
	}
	if o.instanceCapPerAccount < 1 {
		return fail("--instance-cap-per-account must be 1 or more, not %d", o.instanceCapPerAccount)
	}
	// The Turtle live pass leases a slot on a test server: a Forever server is
	// not one, whatever its name (before the test-server list check, so the
	// refusal names the real reason).
	if o.live != "" && !o.forever {
		if refused, code := refuseTurtleOnly("plan-scout", o.live, o.slots, foreverPlanScoutLive, stderr); refused {
			return code
		}
	}
	switch {
	case o.forever && o.spawns == "":
		return fail("--forever needs --spawns DIR (the world export tools/dungeongen/export-forever.sh writes)")
	case o.forever && o.live != "" && o.live != "forever":
		return fail("--forever takes --live without a server name, with --slot DIR")
	case o.forever && o.live == "forever" && o.serveSlot == "":
		return fail("--forever --live needs --slot DIR (a running forever serve slot)")
	case o.forever && o.serveSlot != "" && o.live == "":
		return fail("--slot DIR needs --live")
	case !o.forever && o.serveSlot != "":
		return fail("--slot DIR is the Forever live pass: pass --forever")
	case !o.forever && o.live != "" && !placeServers[o.live]:
		fmt.Fprintf(stderr, "plan-scout: %q is not a test server (the live pass runs on %s only)\n", o.live, strings.Join(placeServerList(), ", "))
		return exitLivePhaseRefused
	}
	var derr error
	if o.walkBudgetD, derr = time.ParseDuration(o.walkBudget); derr != nil || o.walkBudgetD <= 0 {
		return fail("--walk-budget takes a duration like 5m")
	}
	if o.capWaitD, derr = time.ParseDuration(o.instanceCapWait); derr != nil || o.capWaitD < 0 {
		return fail("--instance-cap-wait takes a duration like 20m")
	}
	if o.maxMinutes < 1 {
		return fail("--max-minutes takes at least 1")
	}

	var planner planScoutNav
	if o.forever {
		planner = planScoutOpenForever(o.mmaps, o.parents)
	} else {
		planner = planScoutOpen(o.mmaps)
	}
	defer planner.Close()

	// The rows: the plan's export, or a migration's rows.
	var in planscout.Input
	planPath := filepath.Join(o.dir, name+".json")
	p, perr := dungeon.Load(planPath)
	if perr != nil && o.sqlFile == "" {
		return fail("%v", perr)
	}
	in.Plan = p
	source := planPath
	if o.sqlFile != "" {
		b, err := os.ReadFile(o.sqlFile)
		if err != nil {
			return fail("%v", err)
		}
		all, notes, err := planexport.ParseMigration(string(b))
		if err != nil {
			return fail("%s: %v", o.sqlFile, err)
		}
		var rows []planexport.Rows
		for _, r := range all {
			if r.Plan.Plan == name {
				rows = append(rows, r)
			}
		}
		if len(rows) == 0 {
			return fail("%s holds no plan %q", o.sqlFile, name)
		}
		if len(rows) > 1 {
			return fail("%s holds %d plans named %q", o.sqlFile, len(rows), name)
		}
		in.Rows = rows[0]
		source = o.sqlFile
		_ = notes
	} else {
		if p == nil {
			return fail("%v", perr)
		}
		server := ""
		if o.forever {
			server = planexport.ServerForever
		}
		res, err := planexport.Build(p, planexport.Options{Source: "plan-scout", Cut: cutWith(planner, false), Server: server})
		if err != nil {
			return fail("the exporter refused the plan: %v", err)
		}
		in.Rows, in.Origins = res.Rows, res.Origins
	}

	from, err := legNumber(in, o.from)
	if err != nil {
		return fail("%v", err)
	}
	until, err := legNumber(in, o.until)
	if err != nil {
		return fail("%v", err)
	}

	// One query tells whether the navmesh can answer at all: that is exit 1, not a report of every row off the mesh.
	// A v16 tile set cannot replay the 1.12 ServerPath query; the Forever planner answers its own Plan.
	if len(in.Rows.Steps) > 0 {
		s := in.Rows.Steps[0]
		p0 := navmesh.Vec3{s.X, s.Y, s.Z}
		var probeErr error
		if o.forever {
			_, probeErr = planner.Plan(in.Rows.Plan.Map, p0, p0)
		} else {
			_, probeErr = planner.ServerPath(in.Rows.Plan.Map, p0, p0)
		}
		if probeErr != nil {
			if reason, unavailable := planCheckUnavailable(probeErr); unavailable {
				fmt.Fprintf(stderr, "plan-scout: %s\n", reason)
			} else {
				fmt.Fprintf(stderr, "plan-scout: %v\n", probeErr)
			}
			return exitPlanScoutNoNavmesh
		}
	}

	// The world export: explicit --spawns must read; the default one is optional (the aggro and joiner lints then skip).
	spawnDir := o.spawns
	if spawnDir == "" {
		spawnDir = filepath.Join(o.dir, "..", "tools", "dungeongen", "testdata", fmt.Sprintf("map%d", in.Rows.Plan.Map))
	}
	var world *worldspawn.World
	// unknown are the spawns LoadForeverDB defaulted to level 60 for; the live pass replaces those from its units read.
	var unknown map[uint64]bool
	wnote := ""
	switch {
	case o.forever:
		w, notes, err := worldspawn.LoadForeverDB(spawnDir, in.Rows.Plan.Map)
		if err != nil {
			return fail("%v", err)
		}
		world = w
		if unknown, err = foreverUnknownLevels(spawnDir); err != nil {
			return fail("%v", err)
		}
		if len(notes) > 0 {
			wnote = strings.Join(notes, "; ")
		}
	default:
		if _, err := os.Stat(filepath.Join(spawnDir, "creatures.tsv")); err != nil {
			if o.spawns != "" {
				return fail("no world export in %s", spawnDir)
			}
			wnote = fmt.Sprintf("aggro, patrol and joiner lints skipped: no world export in %s (pass --spawns DIR)", spawnDir)
		} else {
			w, notes, err := worldspawn.Load(spawnDir, in.Rows.Plan.Map)
			if err != nil {
				if o.spawns != "" {
					return fail("%v", err)
				}
				wnote = fmt.Sprintf("aggro, patrol and joiner lints skipped: %v", err)
			} else {
				world = w
				if len(notes) > 0 {
					wnote = strings.Join(notes, "; ")
				}
			}
		}
	}

	scoutOpts := planscout.Options{Filter: o.from != "" || o.until != "", FromLeg: from, UntilLeg: until, PartyLevel: o.partyLevel}
	offline := func() (*planscout.Report, error) {
		r, err := planscout.Offline(in, planner, world, scoutOpts)
		if err != nil {
			return nil, err
		}
		r.Source, r.Mmaps = source, o.mmaps
		if from < 0 {
			r.From = 0
		}
		if until < 0 {
			r.Until = 0
		}
		if wnote != "" {
			r.Notes = append(r.Notes, wnote)
		}
		return r, nil
	}
	rep, err := offline()
	if err != nil {
		return fail("%v", err)
	}

	outDir := out
	if outDir == "" {
		outDir = fmt.Sprintf("./plan-scout-%s-%s", rep.Plan, time.Now().Format("20060102-150405"))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fail("%v", err)
	}

	// The live pass (spec 5.6): the offline report is done, the live spots and hops come from it.
	liveAborted := false
	if o.live != "" {
		if in.Plan == nil {
			return fail("--live needs the plan file (%s)", planPath)
		}
		var data planscout.LiveData
		var code int
		var reason string
		if o.forever {
			data, code, reason = runScoutForeverLive(ctx, &o, in, rep, world, planner)
			rep.Server = "forever"
			// The units read fills the levels the core has no row for; the report is rebuilt so its aggro and joiner
			// numbers use them (MergeLive only adds the live checks).
			if filled := scoutForeverFillLevels(world, data.Units, unknown); filled > 0 {
				fresh, err := offline()
				if err != nil {
					return fail("%v", err)
				}
				rep = fresh
				rep.Server = "forever"
				rep.Notes = append(rep.Notes, fmt.Sprintf("live levels: %d spawn(s) the core has no level for took their unit's level", filled))
			}
		} else {
			if in.Plan.Portal == nil {
				return fail("--live needs the plan file (%s) with its portal", planPath)
			}
			preFixes := planscout.ProposeFixes(rep, in, planner, world)
			data, code, reason = runScoutLive(ctx, &o, in, rep, world, planner, preFixes, outDir, stdout, stderr)
			rep.Server = o.live
		}
		planscout.MergeLive(rep, in, world, data, planscout.Options{PartyLevel: o.partyLevel, Raid: in.Plan.IsRaid(), KillFirst: in.Plan.KillFirst})
		switch code {
		case exitOK:
		case exitPlanScoutLiveAborted:
			liveAborted = true
			rep.Notes = append(rep.Notes, "live pass aborted: "+reason)
			fmt.Fprintf(stderr, "plan-scout: the live pass stopped part-way: %s\n", reason)
		default:
			fmt.Fprintf(stderr, "plan-scout: %s\n", reason)
			return code
		}
	}

	// The fix file (always when asked, also with no fixes).
	fixes := planscout.ProposeFixes(rep, in, planner, world)
	rowsSHA := scoutRowsSHA(in.Rows)
	// replayed are the recorded fixes a replay refused before applying: an unrecorded coordinate, a target its step's row
	// does not name, or a point off the mesh. They are refused here, not handed to the apply pass.
	var replayed []planscout.DroppedFix
	if fixIn != "" {
		ff, err := readFixFile(fixIn)
		if err != nil {
			return fail("--fix-in: %v", err)
		}
		if err := checkFixFile(ff, rep.Plan, rep.Map, rowsSHA); err != nil {
			return fail("--fix-in %s: %v", fixIn, err)
		}
		read := len(ff.Fixes)
		fixes, replayed = ff.VerifiedFixes(in, planner)
		for _, d := range replayed {
			fmt.Fprintf(stderr, "plan-scout: fix %d (step %d, %s) dropped: %s\n", d.ID, d.Step, d.Kind, d.Reason)
			rep.Notes = append(rep.Notes, fmt.Sprintf("fix %d (step %d, %s) dropped: %s", d.ID, d.Step, d.Kind, d.Reason))
		}
		note := fmt.Sprintf("fix-in: %d fix(es) read from %s, %d refused before applying; this pass's own proposals were not used", read, fixIn, len(replayed))
		rep.Notes = append(rep.Notes, note)
		fmt.Fprintf(stderr, "plan-scout: %s\n", note)
	}
	if fixOut != "" {
		ff := planscout.FixFile{Format: planscout.FixFormat, Version: planscout.FixVersion, Plan: rep.Plan, Map: rep.Map,
			RowsSHA256: rowsSHA, Scout: "scout-" + rep.Plan + ".json", Fixes: fixes}
		if ff.Fixes == nil {
			ff.Fixes = []planscout.Fix{}
		}
		if err := writeJSON(fixOut, ff); err != nil {
			return fail("--fix-out: %v", err)
		}
	}

	// The report files.
	jsonFile := filepath.Join(outDir, "scout-"+rep.Plan+".json")
	textFile := filepath.Join(outDir, "scout-"+rep.Plan+".txt")
	if err := writeJSON(jsonFile, rep); err != nil {
		return fail("%v", err)
	}
	var tb strings.Builder
	rep.Text(&tb)
	if err := os.WriteFile(textFile, []byte(tb.String()), 0o644); err != nil {
		return fail("%v", err)
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(rep)
	} else {
		io.WriteString(stdout, tb.String())
	}
	fmt.Fprintf(stdout, "report: %s\n", outDir)

	// --apply-fixes: a patched copy of the plan and its diff, never in place.
	applyRefused := false
	if applyTo != "" {
		switch {
		case in.Plan == nil:
			return fail("--apply-fixes needs the plan file (%s): a migration's rows have no plan refs", planPath)
		case !emptyDir(applyTo):
			return fail("%s exists and is not empty", applyTo)
		case underDungeons(applyTo):
			return fail("%s is inside a dungeons/ folder (or a slot's plan folder): the plan file is never written there", applyTo)
		}
		if o.sqlFile != "" {
			server := ""
			if o.forever {
				server = planexport.ServerForever
			}
			exp, err := planexport.Build(in.Plan, planexport.Options{Source: "plan-scout", Cut: cutWith(planner, false), Server: server})
			if err != nil {
				return fail("%v", err)
			}
			if h := scoutRowsSHA(exp.Rows); h != rowsSHA {
				return fail("the rows in %s do not equal the plan's export (sha256 %s vs %s): nothing to apply fixes to", o.sqlFile, rowsSHA, h)
			}
		}
		var checkNav dungeon.Navmesh = planner
		if o.forever {
			checkNav = scoutForeverCheckNav{planner}
		}
		edited, res, dropped, applyErr := planscout.ApplyFixes(in.Plan, fixes, checkNav)
		if applyErr == nil && countApplied(fixes) == 0 && len(replayed) > 0 {
			// Only advice fixes are left: the replay refused every fix it could have applied.
			applyErr = fmt.Errorf("every recorded fix (%d) was dropped before applying", len(replayed))
		}
		for _, d := range dropped {
			fmt.Fprintf(stderr, "plan-scout: fix %d (step %d, %s) dropped: %s\n", d.ID, d.Step, d.Kind, d.Reason)
			rep.Notes = append(rep.Notes, fmt.Sprintf("fix %d (step %d, %s) dropped: %s", d.ID, d.Step, d.Kind, d.Reason))
		}
		allDropped := append(append([]planscout.DroppedFix{}, replayed...), dropped...)
		if applyErr != nil {
			applyRefused = true
			rep.Notes = append(rep.Notes, "apply-fixes refused: "+applyErr.Error())
			fmt.Fprintf(stderr, "plan-scout: --apply-fixes refused: %v\n", applyErr)
			_ = res
		} else {
			if err := os.MkdirAll(applyTo, 0o755); err != nil {
				return fail("%v", err)
			}
			nb, err := dungeon.Marshal(edited)
			if err != nil {
				return fail("%v", err)
			}
			old, _ := os.ReadFile(planPath)
			if err := os.WriteFile(filepath.Join(applyTo, rep.Plan+".json"), nb, 0o644); err != nil {
				return fail("%v", err)
			}
			diff := routeedit.UnifiedDiff(planPath, filepath.Join(applyTo, rep.Plan+".json"), string(old), string(nb))
			if err := os.WriteFile(filepath.Join(applyTo, rep.Plan+".diff"), []byte(diff), 0o644); err != nil {
				return fail("%v", err)
			}
			if len(allDropped) > 0 {
				if err := writeJSON(filepath.Join(applyTo, "dropped.json"), allDropped); err != nil {
					return fail("dropped.json: %v", err)
				}
			}
			rep.Notes = append(rep.Notes, fmt.Sprintf("apply-fixes: %d fix(es) applied to a copy in %s (the plan check found no new issue)", countApplied(fixes)-len(dropped), applyTo))
		}
		// Rewrite the report with the apply note.
		if err := writeJSON(jsonFile, rep); err != nil {
			return fail("%v", err)
		}
		tb.Reset()
		rep.Text(&tb)
		_ = os.WriteFile(textFile, []byte(tb.String()), 0o644)
	}

	switch {
	case liveAborted:
		return exitPlanScoutLiveAborted
	case applyRefused, rep.Counts.Fail > 0, o.failOn == "warn" && rep.Counts.Warn > 0:
		return exitPlanScoutFail
	}
	return exitOK
}

// splitFlagArgs splits the command line into flags and the positional arguments: the plan name may stand anywhere (the
// usage shows it first, and the flag package stops at the first non-flag word).
func splitFlagArgs(fs *flag.FlagSet, args []string) (flags, pos []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) == 0 || a[0] != '-' || a == "-" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if j := strings.IndexByte(name, '='); j >= 0 {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // flag.Parse reports the unknown flag
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, pos, nil
}

// readFixFile reads a recorded fix file (tb-scout-fix).
func readFixFile(path string) (planscout.FixFile, error) {
	var ff planscout.FixFile
	b, err := os.ReadFile(path)
	if err != nil {
		return ff, err
	}
	if err := json.Unmarshal(b, &ff); err != nil {
		return ff, fmt.Errorf("not a fix file: %v", err)
	}
	if ff.Fixes == nil {
		ff.Fixes = []planscout.Fix{}
	}
	return ff, nil
}

// checkFixFile says a recorded fix file was made for this plan's own exported rows: its format and version, its plan and
// map, and the rows' sha256. A list made for other rows names elements this plan may not have, so it is refused, not used.
func checkFixFile(ff planscout.FixFile, plan string, mapID uint32, rowsSHA string) error {
	if ff.Format != planscout.FixFormat || ff.Version != planscout.FixVersion {
		return fmt.Errorf("format %q version %d; want %q version %d", ff.Format, ff.Version, planscout.FixFormat, planscout.FixVersion)
	}
	if ff.Plan != plan || ff.Map != mapID {
		return fmt.Errorf("it holds plan %q map %d; this one is %q map %d", ff.Plan, ff.Map, plan, mapID)
	}
	if ff.RowsSHA256 != rowsSHA {
		return fmt.Errorf("it was made for other rows (sha256 %s; this plan's export is %s)", ff.RowsSHA256, rowsSHA)
	}
	return nil
}

func countApplied(fixes []planscout.Fix) int {
	n := 0
	for _, f := range fixes {
		if f.Apply {
			n++
		}
	}
	return n
}

// legNumber reads --from/--until: a leg number (the exported leg, 0-based as the rows carry it) or a plan leg name.
func legNumber(in planscout.Input, v string) (int, error) {
	if v == "" {
		return -1, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("leg %d: numbers start at 0", n)
		}
		return n, nil
	}
	if in.Plan == nil {
		return 0, fmt.Errorf("leg name %q: --from/--until take a number without the plan file", v)
	}
	for li, l := range in.Plan.Legs {
		if l.Name != v {
			continue
		}
		n, ok := in.Rows.LegNumber(in.Plan, li)
		if !ok {
			return 0, fmt.Errorf("leg %q is not in the export", v)
		}
		return n, nil
	}
	return 0, fmt.Errorf("no leg named %q in the plan", v)
}

// rowsSHA256 pins the rows a fix file was made for.
func scoutRowsSHA(r planexport.Rows) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(planexport.WriteSQL([]planexport.Rows{r}))))
}

// writeJSON writes v as indented JSON, creating the file.
func writeJSON(path string, v any) error {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// emptyDir says the path does not exist, or is an empty directory.
func emptyDir(path string) bool {
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || !st.IsDir() {
		return false
	}
	ents, err := os.ReadDir(path)
	return err == nil && len(ents) == 0
}

// underDungeons says the path is inside a dungeons/ folder or a slot's plan folder.
func underDungeons(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(abs, string(filepath.Separator)) {
		if part == "dungeons" || part == "slots" {
			return true
		}
	}
	return false
}

// defaultMmapsDir is ~/forever-agent-mmaps.
func defaultMmapsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "forever-agent-mmaps"
	}
	return filepath.Join(home, "forever-agent-mmaps")
}
