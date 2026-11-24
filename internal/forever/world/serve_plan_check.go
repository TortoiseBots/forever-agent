// plan_check (#216): the plan check the CLI's `plan-check --forever` runs (dungeon.CheckPlanWith on the Forever
// planner), answered as a daemon method over the serve's own plans folder and navmesh. The old daemon's params
// (name, from_leg, from_pull_group) and result (the check plus checked/skipped) are kept, so a caller written for
// it sends the same JSON. Nothing new is checked here: the method reads the plan file and walks the same points the
// command walks.
//
// The check does not touch the session: it runs on the RPC handler's own goroutine with a scratch planner over the
// serve's mmaps folder (its own tile cache, so a walk or a fight on the session keeps its tiles), and answers while
// the daemon is reconnecting too. Without --navmesh the plan is left unchecked with the reason in skipped, as on
// the old daemon.
package world

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// servePlanName limits a plan name to what a file name in the plans folder can be (the CLI's rule).
var servePlanName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// servePlanCheckParams is plan_check's: the plan's file name and, optionally, the leg to check from, as
// dungeon_run's from_leg (a leg number from 0, or a leg name).
type servePlanCheckParams struct {
	Name          string         `json:"name"`
	FromLeg       *serveLegParam `json:"from_leg,omitempty"`
	FromPullGroup *int           `json:"from_pull_group,omitempty"`
}

// serveLegParam is a leg given as its index or its name.
type serveLegParam struct {
	index int
	name  string
}

// UnmarshalJSON reads a leg number (from 0) or a leg name.
func (l *serveLegParam) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		l.index, l.name = n, ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil || s == "" {
		return errors.New("from_leg must be a leg number (from 0) or a leg name")
	}
	l.name = s
	return nil
}

// ServePlanCheck is plan_check's answer: the check, or the reason it could not run (Checked false).
type ServePlanCheck struct {
	dungeon.PlanCheck
	Checked bool   `json:"checked"`
	Skipped string `json:"skipped,omitempty"`
}

// registerPlanCheck wires plan_check. It is a plain handler, not a session call: the check needs no packet.
func registerPlanCheck(srv *rpc.Server, st *serveState) {
	srv.Register("plan_check", rpc.Typed(func(_ context.Context, p servePlanCheckParams) (ServePlanCheck, error) {
		return st.servePlanCheck(p)
	}))
}

// servePlanCheck loads the plan from the serve's plans folder and checks it on a scratch Forever planner over the
// serve's mmaps folder.
func (st *serveState) servePlanCheck(p servePlanCheckParams) (ServePlanCheck, error) {
	if !servePlanName.MatchString(p.Name) {
		return ServePlanCheck{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: `plan_check: name must be a plan file name such as "zulfarrak" (a-z, 0-9, _ and -)`}
	}
	if p.FromPullGroup != nil && p.FromLeg == nil {
		return ServePlanCheck{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "plan_check: from_pull_group needs from_leg (a pull group number means nothing without its leg)"}
	}
	plan, err := dungeon.Load(filepath.Join(st.plansFolder(), p.Name+".json"))
	if err != nil {
		return ServePlanCheck{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "plan_check: dungeon plan: " + err.Error()}
	}
	from := 0
	if p.FromLeg != nil {
		if from = serveLegIndex(plan, p.FromLeg); from < 0 {
			return ServePlanCheck{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("plan_check: from_leg: the plan has %d legs (numbers 0-%d)", len(plan.Legs), len(plan.Legs)-1)}
		}
	}
	opt := dungeon.CheckOptions{FromLeg: from, FromPullGroup: p.FromPullGroup}
	if err := opt.Validate(plan); err != nil {
		return ServePlanCheck{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "plan_check: " + err.Error()}
	}
	if st.opts.NavmeshDir == "" {
		return ServePlanCheck{Skipped: "no navmesh: the serve was started without --navmesh"}, nil
	}
	planner := navmesh.NewPlannerFormat(st.opts.NavmeshDir, navmesh.Forever, st.opts.Parents)
	defer planner.Close()
	res, err := dungeon.CheckPlanWith(plan, planner, opt)
	if why, skip := servePlanCheckUnavailable(err); skip {
		return ServePlanCheck{Skipped: why}, nil
	}
	if err != nil {
		return ServePlanCheck{}, &rpc.Error{Code: rpc.CodeGame, Message: "plan_check: " + err.Error()}
	}
	out := ServePlanCheck{PlanCheck: *res, Checked: true}
	// A JSON consumer should read a list, never null, for the four collections.
	if out.Issues == nil {
		out.Issues = []dungeon.PlanIssue{}
	}
	if out.Warnings == nil {
		out.Warnings = []dungeon.PlanIssue{}
	}
	if out.Exempt == nil {
		out.Exempt = []dungeon.PlanIssue{}
	}
	if out.RunLeader == nil {
		out.RunLeader = []dungeon.PlanIssue{}
	}
	return out, nil
}

// serveLegIndex resolves a leg given as an index or a name; -1 when the plan has no such leg. nil is leg 0.
func serveLegIndex(plan *dungeon.Plan, l *serveLegParam) int {
	if l == nil {
		return 0
	}
	if l.name == "" {
		if l.index < 0 || l.index >= len(plan.Legs) {
			return -1
		}
		return l.index
	}
	for i, leg := range plan.Legs {
		if leg.Name == l.name {
			return i
		}
	}
	return -1
}

// servePlanCheckUnavailable reports the errors that mean "this mesh cannot check the plan" (no engine, a missing
// tile) as a skip with its reason, as the CLI's plan-check does: a plan for a map the serve's mmaps folder does not
// hold is not a failing plan.
func servePlanCheckUnavailable(err error) (string, bool) {
	var missing *navmesh.DataMissingError
	switch {
	case errors.Is(err, navmesh.ErrNoEngine), errors.As(err, &missing):
		return err.Error(), true
	}
	return "", false
}
