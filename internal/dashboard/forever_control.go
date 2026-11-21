package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/tbm"
)

// This file is the Forever controls (L5): on a serve slot the buttons (stay,
// follow, come, attack, pull, release) and the run pause/resume/stop all call
// commander {op:cmd|run}. No tbm* call is ever made on a Forever agent.
// `run-active` and `not-available` come back shown as the reply: the page
// tells the operator what the module said, verbatim.

// foreverOrderVerbs are the button verbs the page may send on a Forever
// agent, mapped to the commander cmd verb the module reads.
var foreverOrderVerbs = map[string]string{
	"stay":    "stay",
	"follow":  "follow",
	"come":    "come",
	"attack":  "attack",
	"pull":    "pull",
	"release": "release",
}

// foreverRunVerbs are the run verbs the page may send on a Forever agent,
// mapped to the commander run verb.
var foreverRunVerbs = map[string]string{
	"pause":  "pause",
	"resume": "resume",
	"stop":   "stop",
}

// foreverOrderRequest is the body of POST /api/control/<id>/order on a
// Forever agent.
type foreverOrderRequest struct {
	Verb string `json:"verb"`
}

// order is the order handler: a button press on a Forever agent. The action
// must be "order", the agent must be a Forever slot the dashboard watches
// (D6), and the verb must be one of the six buttons. The commander call is
// logged before it is sent, like the run controls.
func (c *Controller) order(ac *actionContext) {
	if ac.agent.Kind != AgentForever {
		jsonError(ac.w, 404, "no such control action")
		return
	}
	var req foreverOrderRequest
	if !decodeBody(ac.w, ac.r, &req) {
		return
	}
	verb, ok := foreverOrderVerbs[req.Verb]
	if !ok {
		jsonError(ac.w, 400, "verb must be one of stay, follow, come, attack, pull, release")
		return
	}
	c.foreverCommander(ac, "cmd", verb, nil)
}

// runForever is the run handler's Forever branch: pause/resume/stop on a
// serve slot go to commander {op:run}. The turtle path (dungeon_status live
// check, dungeon_* verbs) never runs on a Forever agent.
func (c *Controller) runForever(ac *actionContext) {
	var req runRequest
	if !decodeBody(ac.w, ac.r, &req) {
		return
	}
	verb, ok := foreverRunVerbs[req.Verb]
	if !ok {
		jsonError(ac.w, 400, "verb must be one of pause, resume, stop")
		return
	}
	if !req.Confirm && (req.Verb == "pause" || req.Verb == "stop") {
		jsonError(ac.w, 409, "%s needs \"confirm\": true", req.Verb)
		return
	}
	c.foreverCommander(ac, "run", verb, map[string]bool{"confirm": req.Confirm})
}

// foreverCommander is the shared body of the Forever controls: the params
// name commander {op:verb}, the call is logged before it is sent, and the
// module's answer is shown as the reply. A JSON-RPC error is a failure: the
// response is non-2xx with the code and the scrubbed message, never
// status "ok" (a rejected pause, stop, attack or pull must not look
// executed). A decoded module reply with ok false is a refusal: 200, but
// status "error" and executed false. A transport failure is a 502 like the
// turtle run handler; a call that never left is an error, one that may have
// run is indeterminate.
func (c *Controller) foreverCommander(ac *actionContext, op, verb string, args map[string]bool) {
	ctx := ac.r.Context()
	if err := c.log.append(controlRecord{Phase: "request", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
		Agent: ac.agent.ID, Action: ac.action, Verb: verb, Args: args}); err != nil {
		jsonError(ac.w, 500, "the control log could not be written, so nothing was sent: %v", err)
		return
	}
	cl, err := serveclient.New(ac.agent.Addr, ac.agent.TokenPath)
	if err != nil {
		res := controlRecord{Phase: "result", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
			Agent: ac.agent.ID, Action: ac.action, Verb: verb, RPC: "commander", Outcome: "error", Message: err.Error()}
		if lerr := c.log.append(res); lerr != nil {
			logLost(res, lerr)
		}
		jsonError(ac.w, 502, "the serve client could not be opened, so nothing was sent: %v", err)
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlTimeout)
	defer cancel()
	out, callErr := cl.Commander(wctx, serveclient.CommanderParams{Op: op, Verb: verb})
	res := controlRecord{Phase: "result", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
		Agent: ac.agent.ID, Action: ac.action, Verb: verb, RPC: "commander", Outcome: "ok"}
	okv := callErr == nil
	code, isRPC := serveclient.CodeOf(callErr)
	switch {
	case callErr == nil:
		if out.Reply.Kind != "" && !out.Reply.OK {
			okv = false
			res.Outcome = "error"
			res.Message = redactMessage(moduleRefusal(out.Reply), cl.Token())
		}
	case isRPC:
		res.Outcome = "error"
		res.Code = code
		res.Message = redactMessage(callErr.Error(), cl.Token())
	case errors.Is(callErr, serveclient.ErrUnauthorized), errors.Is(callErr, serveclient.ErrReconnecting), errors.Is(callErr, serveclient.ErrDisconnected), neverSent(callErr):
		res.Outcome = "error"
		res.Message = redactMessage(callErr.Error(), cl.Token())
	default:
		res.Outcome = "indeterminate"
	}
	res.OK = &okv
	if res.Outcome == "indeterminate" {
		res.OK = nil
	}
	if lerr := c.log.append(res); lerr != nil {
		logLost(res, lerr)
		if callErr == nil {
			status := "ok"
			if !okv {
				status = "error"
			}
			writeAny(ac.w, 200, map[string]any{"status": status, "executed": okv, "log_failed": true})
			return
		}
	}
	switch {
	case callErr == nil:
		// An answered call: the module's words, verbatim. A refusal (ok
		// false) is marked as one, so the page and a script can tell it
		// from an executed control.
		status := "ok"
		if !okv {
			status = "error"
		}
		writeAny(ac.w, 200, map[string]any{"status": status, "action": ac.action, "verb": verb, "rpc": "commander",
			"executed": okv, "message": res.Message, "reply": out.Reply, "result": json.RawMessage(scrubOrNull(jsonBytes(out), cl.Token()))})
		return
	case isRPC:
		// The daemon answered with an error (not-ported before C3, a
		// refusal, a lost link): a failed control, not status "ok".
		writeAny(ac.w, 502, map[string]any{"status": "error", "action": ac.action, "verb": verb, "rpc": "commander",
			"executed": false, "code": code, "message": redactMessage(res.Message, cl.Token())})
		return
	case res.Outcome == "indeterminate":
		jsonError(ac.w, 502, "unknown: check the run status")
		return
	default:
		jsonError(ac.w, 502, "%s", redactMessage(res.Message, cl.Token()))
		return
	}
}

// moduleRefusal is the module's own words for a reply it marked not ok.
func moduleRefusal(r tbm.Reply) string {
	if r.Err != nil && r.Err.Message != "" {
		return r.Err.Message
	}
	if r.Line != "" {
		return r.Line
	}
	if len(r.Lines) > 0 {
		return r.Lines[len(r.Lines)-1]
	}
	return "the module refused the control"
}

func scrubOrNull(raw json.RawMessage, secret string) json.RawMessage {
	out, err := Scrub(orNull(raw), secret)
	if err != nil {
		return nil
	}
	return out
}
