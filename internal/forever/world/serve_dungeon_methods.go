// The dungeon-run methods (#216): dungeon_run, dungeon_status, dungeon_stop, dungeon_pause and dungeon_resume,
// the old daemon's names for the run control this server already has. A ForeverCore run is the module's: its tank
// bot leads and its own policy starts, pauses, resumes and stops it. The daemon therefore leads nothing itself —
// every verb goes out as the module's own `.tb run` line through the command channel (internal/forever/cmdctl),
// exactly as the `commander` method's `run` op and the `forever dungeon-watch` / `forever dungeon-run` commands do.
// Nothing here composes a line, keeps a run's state or adds a behaviour: each verb is the existing controller call
// with the old method's name and shape.
//
//	old name          the line it sends
//	dungeon_run       .tb run start <lead> <plan>
//	dungeon_status    .tb run status <lead|all>
//	dungeon_stop      .tb run stop <lead> [reason]
//	dungeon_pause     .tb run pause <lead>
//	dungeon_resume    .tb run resume <lead>
//
// A run's party must already be bound to this character (the `commander bind` method, or `forever dungeon-watch`'s
// own bring-in): a start names a lead of one of the character's own bindings, and the module refuses a lead the
// character does not command (the typed `not-owned` refusal, as everywhere else).
//
// dungeon_run makes the CLI's start preflight first (serve_dungeon_preflight.go): the plan, the serve slot and its
// lease, the module link and the named lead, through the same fpreflight functions `forever dungeon-watch` runs. A
// failed check refuses the method and nothing is sent.
package world

import (
	"context"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/cmdctl"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveDungeonRunParams is dungeon_run's: the plan the module runs (its own plan name, e.g. "ragefire_chasm") and
// the lead bot that drives it (empty: the character's only bound lead).
type serveDungeonRunParams struct {
	Plan    string `json:"plan"`
	LeadBot string `json:"lead_bot,omitempty"`
}

// serveDungeonLeadParams is dungeon_status, dungeon_stop, dungeon_pause and dungeon_resume's: the lead bot the verb
// names. dungeon_status with no lead reads every run the module holds (the run status `all`); the mutating verbs
// name a lead of the character's own bindings, or the character's only one.
type serveDungeonLeadParams struct {
	LeadBot string `json:"lead_bot,omitempty"`
	// Reason is dungeon_stop's: the reason words the module's `.tb run stop` takes after the lead.
	Reason string `json:"reason,omitempty"`
}

// ServeDungeonRun is a dungeon method's answer: the lines that went out and the module's run records (its own
// state, step, leg, counters and bosses), typed the way the commander method types them. A refusal is the typed RPC
// error instead (serve_commander.go's mapping: the module's code first, -32002/-32005 for the link).
type ServeDungeonRun struct {
	Sent []string            `json:"sent,omitempty"`
	Runs []ServeCommanderRun `json:"runs"`
}

// registerDungeonMethods wires the five run verbs and plan_check to the serve state's controller.
func registerDungeonMethods(srv *rpc.Server, st *serveState) {
	srv.Register("dungeon_run", rpc.Typed(func(ctx context.Context, p serveDungeonRunParams) (ServeDungeonRun, error) {
		plan := strings.TrimSpace(p.Plan)
		if !servePlanName.MatchString(plan) {
			return ServeDungeonRun{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: `dungeon_run: plan is the module's one-word plan name in the serve's plans folder (e.g. ragefire_chasm: a-z, 0-9, _ and -)`}
		}
		// The lead and the character are resolved on the session goroutine (the controller reads the module's
		// cards); the start preflight and the start itself are their own calls, so the preflight's waits never hold
		// the queue. Nothing is sent until the preflight has passed.
		character, lead, err := st.serveDungeonTarget(ctx, p.LeadBot)
		if err != nil {
			return ServeDungeonRun{}, serveError(err)
		}
		if err := st.serveRunPreflight(ctx, character, plan, lead); err != nil {
			return ServeDungeonRun{}, serveError(err)
		}
		v, err := st.call(ctx, func(s *session) (any, error) {
			c, err := s.serveDungeonCtl()
			if err != nil {
				return nil, err
			}
			return serveDungeonAnswer(c.Run(ctx, "start", lead, plan))
		})
		if err != nil {
			return ServeDungeonRun{}, serveError(err)
		}
		out, ok := v.(ServeDungeonRun)
		if !ok {
			return ServeDungeonRun{}, &rpc.Error{Code: rpc.CodeInternal, Message: "internal: dungeon_run result type"}
		}
		return out, nil
	}))
	srv.Register("dungeon_status", serveCallTyped(st, func(s *session, ctx context.Context, p serveDungeonLeadParams) (ServeDungeonRun, error) {
		c, err := s.serveDungeonCtl()
		if err != nil {
			return ServeDungeonRun{}, err
		}
		lead, err := serveDungeonLead(ctx, c, p.LeadBot, "status")
		if err != nil {
			return ServeDungeonRun{}, err
		}
		return serveDungeonAnswer(c.Run(ctx, "status", lead, ""))
	}))
	srv.Register("dungeon_stop", serveCallTyped(st, func(s *session, ctx context.Context, p serveDungeonLeadParams) (ServeDungeonRun, error) {
		c, err := s.serveDungeonCtl()
		if err != nil {
			return ServeDungeonRun{}, err
		}
		lead, err := serveDungeonLead(ctx, c, p.LeadBot, "stop")
		if err != nil {
			return ServeDungeonRun{}, err
		}
		return serveDungeonAnswer(c.Run(ctx, "stop", lead, strings.TrimSpace(p.Reason)))
	}))
	srv.Register("dungeon_pause", serveCallTyped(st, func(s *session, ctx context.Context, p serveDungeonLeadParams) (ServeDungeonRun, error) {
		return s.serveDungeonVerb(ctx, "pause", p.LeadBot)
	}))
	srv.Register("dungeon_resume", serveCallTyped(st, func(s *session, ctx context.Context, p serveDungeonLeadParams) (ServeDungeonRun, error) {
		return s.serveDungeonVerb(ctx, "resume", p.LeadBot)
	}))
	registerPlanCheck(srv, st)
}

// serveDungeonTarget resolves dungeon_run's lead and the character's name on the session goroutine: the lead from
// the character's own bindings (or the named one), the name the preflight's console reads.
func (st *serveState) serveDungeonTarget(ctx context.Context, leadBot string) (character, lead string, err error) {
	v, err := st.call(ctx, func(s *session) (any, error) {
		c, err := s.serveDungeonCtl()
		if err != nil {
			return nil, err
		}
		lead, err := serveDungeonLead(ctx, c, leadBot, "start")
		if err != nil {
			return nil, err
		}
		return [2]string{s.report.Character.Name, lead}, nil
	})
	if err != nil {
		return "", "", err
	}
	out, ok := v.([2]string)
	if !ok {
		return "", "", &rpc.Error{Code: rpc.CodeInternal, Message: "internal: dungeon_run target type"}
	}
	return out[0], out[1], nil
}

// serveDungeonVerb runs one of the two verbs that take no argument.
func (s *session) serveDungeonVerb(ctx context.Context, verb, leadBot string) (ServeDungeonRun, error) {
	c, err := s.serveDungeonCtl()
	if err != nil {
		return ServeDungeonRun{}, err
	}
	lead, err := serveDungeonLead(ctx, c, leadBot, verb)
	if err != nil {
		return ServeDungeonRun{}, err
	}
	return serveDungeonAnswer(c.Run(ctx, verb, lead, ""))
}

// serveDungeonCtl is the commander controller the run verbs go through. It is built with the first session and
// reset by every install, so a re-entry drops the old session's capability answers and nothing else.
func (s *session) serveDungeonCtl() (cmdctl.Controller, error) {
	if s.serveRT == nil || s.serveRT.st.cmdr == nil {
		return nil, &rpc.Error{Code: rpc.CodeDisconnected, Message: "dungeon: the session is not serving"}
	}
	return s.serveRT.st.cmdr.c, nil
}

// serveDungeonLead resolves the lead a verb names. A status with no lead reads every run the module holds; a
// mutating verb with no lead takes the character's only bound lead (the module answers one card per commander, so
// anything but exactly one card is refused with a clear error instead of guessing).
func serveDungeonLead(ctx context.Context, c cmdctl.Controller, lead, verb string) (string, error) {
	if l := strings.TrimSpace(lead); l != "" {
		return l, nil
	}
	if verb == "status" {
		return "all", nil
	}
	return commanderOnlyLead(ctx, c)
}

// serveDungeonAnswer maps the controller's answer onto the run method's result, reusing the commander method's own
// typing and refusal mapping.
func serveDungeonAnswer(res cmdctl.Result, err error) (ServeDungeonRun, error) {
	if err != nil {
		return ServeDungeonRun{}, serveCommanderError(res, err)
	}
	out := ServeDungeonRun{Sent: res.Sent, Runs: []ServeCommanderRun{}}
	for _, r := range res.Runs {
		out.Runs = append(out.Runs, commanderRun(r))
	}
	return out, nil
}
