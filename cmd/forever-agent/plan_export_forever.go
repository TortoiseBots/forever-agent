package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/planexport"
)

// planExportForeverArgs is plan-export's Forever mode, parsed by planExportCmd.
type planExportForeverArgs struct {
	dir, out, mmaps, rev, stamp, lastLeg                         string
	allLive, allowDraft, force, report, floorAware, approachHops bool
	skipLegs, withOptional, withLegs, names                      []string
	stdout, stderr                                               io.Writer
}

// planExportForever writes the rows a server module that runs dungeon plans reads. In report mode it prints one record per
// plan file and writes nothing; in export mode it writes one migration per plan, named for the plan.
func planExportForever(a planExportForeverArgs) int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(a.stderr, "plan-export: "+format+"\n", args...)
		return exitUsage
	}
	if a.report && a.out != "" {
		return fail("--report writes nothing; leave --out out")
	}
	if a.allLive && len(a.names) > 0 {
		return fail("name the plans or pass --all-live, not both")
	}
	dirTarget := strings.HasSuffix(a.out, "/")
	if st, err := os.Stat(a.out); a.out != "" && err == nil && st.IsDir() {
		dirTarget = true
	}
	if !a.report && a.out == "" {
		fmt.Fprintln(a.stderr, planExportUsage)
		return fail("--out is required")
	}
	if a.report && len(a.names) == 0 {
		names, err := planFiles(a.dir)
		if err != nil {
			return fail("%v", err)
		}
		a.names = names
	}
	if a.allLive {
		names, err := livePlans(a.dir)
		if err != nil {
			return fail("%v", err)
		}
		a.names = names
	}
	if len(a.names) == 0 {
		return fail("no plan file to export")
	}
	if !a.report && !dirTarget && len(a.names) > 1 {
		return fail("--out %s is one file, and %d plans are named: pass a folder (a trailing / creates it) for one file per plan", a.out, len(a.names))
	}
	if a.stamp != "" {
		return fail("--forever migrations are named plan_<plan>.sql, without a date: --stamp is the run-leader export's")
	}
	var planner, naive *navmesh.Planner
	opt := planexport.Options{
		SkipLegs: a.skipLegs, WithOptional: a.withOptional, WithLegs: a.withLegs,
		ApproachHops: a.approachHops, LastLeg: a.lastLeg,
	}
	if a.mmaps != "" {
		planner = navmesh.NewForeverPlanner(a.mmaps, nil)
		defer planner.Close()
		opt.Cut = cutWith(planner, a.floorAware)
		naive = navmesh.NewForeverPlanner(a.mmaps, nil)
		defer naive.Close()
	} else if !a.report {
		fmt.Fprintln(a.stderr, "plan-export: note: --forever without --mmaps: no navmesh cuts; the plan's own points are exported")
	}
	revision := a.rev
	if revision == "" {
		revision = gitRev(a.dir)
	}
	type written struct {
		name string
		rep  planexport.ForeverReport
	}
	var done []written
	for _, n := range a.names {
		if !planName.MatchString(n) {
			return fail("%q is not a plan name (a-z, 0-9, _ and -)", n)
		}
		p, err := dungeon.Load(filepath.Join(a.dir, n+".json"))
		if err != nil {
			if a.report {
				fmt.Fprintf(a.stdout, "plan=%s map=0 status=refused why=%q\n", n, err.Error())
				continue
			}
			return fail("%v", err)
		}
		if msg := planexport.Refusal(p, a.allowDraft); msg != "" {
			if a.report {
				fmt.Fprintf(a.stdout, "plan=%s map=%d status=refused why=%q\n", n, p.Map, msg)
				continue
			}
			return fail("%s", msg)
		}
		o := opt
		o.Source = "forever-agent"
		if revision != "" {
			o.Source += " " + revision
		}
		o.Source += " dungeons/" + n + ".json"
		if planner != nil {
			boxes := dungeon.NavBoxes(p.Avoid)
			planner.SetAvoid(p.Map, boxes)
			if len(boxes) > 0 {
				o.Route = routeAround(naive, planner, boxes)
			}
		}
		rep, err := planexport.BuildForever(p, o)
		if a.report {
			printForeverReport(a.stdout, n, p.Map, rep, err)
			continue
		}
		if err != nil {
			for _, r := range rep.Refused {
				fmt.Fprintf(a.stderr, "plan-export: %s: refused %s (%s): %s\n", n, r.Field, r.Where, r.Why)
			}
			if a.allLive {
				fmt.Fprintf(a.stderr, "plan-export: %s: skipped: %v\n", n, err)
				continue
			}
			return fail("%v", err)
		}
		for _, w := range rep.Warnings {
			fmt.Fprintf(a.stderr, "plan-export: %s: %s\n", n, w)
		}
		for _, d := range rep.Dropped {
			fmt.Fprintf(a.stderr, "plan-export: %s: dropped %s (%s): %s\n", n, d.Field, d.Where, d.Why)
		}
		pulls := 0
		for _, s := range rep.Rows.Steps {
			if s.Kind == planexport.KindPull {
				pulls++
			}
		}
		fmt.Fprintf(a.stdout, "%s: map %d, %d legs, %d steps (%d pull, %d boss, %d move), %d pack units, %d unit rows, %d boss rows\n",
			n, p.Map, len(rep.Rows.Legs), len(rep.Rows.Steps), pulls, bossSteps(rep.Rows), moveSteps(rep.Rows), packUnits(rep.Units), len(rep.Units), len(rep.Bosses))
		done = append(done, written{name: n, rep: rep})
	}
	if a.report {
		return exitOK
	}
	if len(done) == 0 {
		return fail("no plan could be exported")
	}
	all := make([]planexport.ForeverReport, len(done))
	for i := range done {
		all[i] = done[i].rep
	}
	sql := planexport.WriteForeverSQL(all)
	back, err := planexport.ParseForever(sql)
	if err != nil {
		return fail("the migration does not read back: %v", err)
	}
	if err := planexport.EqualForever(all, back); err != nil {
		return fail("the migration does not read back as the plans: %v", err)
	}
	if dirTarget {
		if err := os.MkdirAll(a.out, 0o755); err != nil {
			return fail("%v", err)
		}
	} else if err := os.MkdirAll(filepath.Dir(a.out), 0o755); err != nil {
		return fail("%v", err)
	}
	for _, w := range done {
		target := a.out
		if dirTarget {
			target = filepath.Join(a.out, planFileName(w.name))
		}
		if _, err := os.Stat(target); err == nil && !a.force {
			return fail("%s exists: a migration that is applied is never edited; export to a new name, or pass --force", target)
		}
		if err := os.WriteFile(target, []byte(planexport.WriteForeverSQL([]planexport.ForeverReport{w.rep})), 0o644); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(a.stdout, "wrote %s\n", target)
	}
	return exitOK
}

func planFiles(dir string) ([]string, error) {
	m, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range m {
		base := strings.TrimSuffix(filepath.Base(f), ".json")
		if strings.Contains(base, ".") {
			continue
		}
		out = append(out, base)
	}
	sort.Strings(out)
	return out, nil
}

func livePlans(dir string) ([]string, error) {
	names, err := planFiles(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		p, err := dungeon.Load(filepath.Join(dir, n+".json"))
		if err != nil {
			return nil, err
		}
		if planexport.IsLive(p) {
			out = append(out, n)
		}
	}
	return out, nil
}

// printForeverReport prints one plan's record: its counts, what was dropped, and what was refused (or why it did not load).
func printForeverReport(w io.Writer, name string, mapID uint32, rep planexport.ForeverReport, err error) {
	status, why := "exported", ""
	if err != nil {
		status, why = "refused", err.Error()
		if len(rep.Refused) == 0 && rep.Rows.Plan.Plan == "" {
			fmt.Fprintf(w, "plan=%s map=%d status=%s why=%q\n", name, mapID, status, why)
			return
		}
	}
	pulls, moves, bosses := 0, 0, 0
	for _, s := range rep.Rows.Steps {
		switch s.Kind {
		case planexport.KindPull:
			pulls++
		case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact:
			moves++
		case planexport.KindBoss:
			bosses++
		}
	}
	m := rep.Rows.Plan.Map
	if m == 0 {
		m = mapID
	}
	fmt.Fprintf(w, "plan=%s map=%d status=%s legs=%d steps=%d pulls=%d moves=%d bosses=%d units=%d boss_rows=%d why=%q\n",
		name, m, status, len(rep.Rows.Legs), len(rep.Rows.Steps), pulls, moves, bosses, len(rep.Units), len(rep.Bosses), why)
	for _, d := range rep.Dropped {
		fmt.Fprintf(w, "  dropped %s (%s): %s\n", d.Field, d.Where, d.Why)
	}
	for _, r := range rep.Refused {
		fmt.Fprintf(w, "  refused %s (%s): %s\n", r.Field, r.Where, r.Why)
	}
}

func bossSteps(r planexport.Rows) int {
	n := 0
	for _, s := range r.Steps {
		if s.Kind == planexport.KindBoss {
			n++
		}
	}
	return n
}

func moveSteps(r planexport.Rows) int {
	n := 0
	for _, s := range r.Steps {
		switch s.Kind {
		case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact:
			n++
		}
	}
	return n
}

func packUnits(us []planexport.UnitRow) int {
	n := 0
	for _, u := range us {
		if u.Role == planexport.UnitPack {
			n++
		}
	}
	return n
}

// planFileName is the migration name of a plan for a server module that runs dungeon plans. It carries no date: the bytes
// of an export must not depend on when it ran, so the same plan and options always write the same file name.
func planFileName(plan string) string {
	return "plan_" + plan + ".sql"
}
