package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
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

// foreverCommanderReply is the module reply the page shows: run-active and
// not-available are outcomes, not transport errors.
type foreverCommanderReply struct {
	Kind    string `json:"kind"`
	OK      bool   `json:"ok"`
	Line    string `json:"line,omitempty"`
	Message string `json:"message,omitempty"`
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
// module's reply (including run-active and not-available) is shown as the
// reply. A transport failure is a 502 like the turtle run handler.
func (c *Controller) foreverCommander(ac *actionContext, op, verb string, args map[string]bool) {
	cl := newForeverClient(ac.agent, controlTimeout)
	ctx := ac.r.Context()
	if err := c.log.append(controlRecord{Phase: "request", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
		Agent: ac.agent.ID, Action: ac.action, Verb: verb, Args: args}); err != nil {
		jsonError(ac.w, 500, "the control log could not be written, so nothing was sent: %v", err)
		return
	}
	params := map[string]any{"op": op, "verb": verb}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlTimeout)
	defer cancel()
	out, rpcErr, callErr := foreverCommanderCall(wctx, cl, params)
	okv := callErr == nil && rpcErr == nil
	res := controlRecord{Phase: "result", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
		Agent: ac.agent.ID, Action: ac.action, Verb: verb, RPC: "commander", OK: &okv, Outcome: "ok"}
	switch {
	case callErr != nil:
		if errors.Is(callErr, ErrIndeterminate) {
			res.Outcome, res.OK = "indeterminate", nil
		} else {
			res.Outcome = "error"
			res.Message = redactMessage(callErr.Error(), cl.Token())
		}
	case rpcErr != nil:
		res.Outcome = "error"
		res.Code = rpcErr.Code
		res.Message = redactMessage(rpcErr.Message, cl.Token())
	}
	if lerr := c.log.append(res); lerr != nil {
		logLost(res, lerr)
		if callErr == nil && rpcErr == nil {
			writeAny(ac.w, 200, map[string]any{"executed": true, "log_failed": true})
			return
		}
	}
	switch {
	case errors.Is(callErr, ErrIndeterminate):
		jsonError(ac.w, 502, "unknown: check the run status")
		return
	case callErr != nil:
		jsonError(ac.w, 502, "%s", res.Message)
		return
	case rpcErr != nil:
		// The daemon answered: run-active and not-available are the
		// module's words, shown as the reply with its code.
		writeAny(ac.w, 200, map[string]any{"status": "ok", "action": ac.action, "verb": verb, "rpc": "commander",
			"reply": map[string]any{"code": rpcErr.Code, "message": rpcErr.Message}, "result": json.RawMessage(scrubOrNull(out, cl.Token()))})
		return
	}
	scrubbed, serr := Scrub(orNull(out), cl.Token())
	if serr != nil {
		jsonError(ac.w, 500, "the agent's answer could not be scrubbed: %v", serr)
		return
	}
	writeAny(ac.w, 200, map[string]any{"status": "ok", "action": ac.action, "verb": verb, "rpc": "commander", "result": json.RawMessage(scrubbed)})
}

// foreverCommanderCall sends one commander call and splits the outcome:
// result, JSON-RPC error, or transport/Indeterminate failure.
func foreverCommanderCall(ctx context.Context, cl *foreverClient, params map[string]any) (json.RawMessage, *RPCError, error) {
	out, err := cl.Call(ctx, "commander", params)
	if err == nil {
		return out, nil, nil
	}
	var re *RPCError
	if errors.As(err, &re) {
		return nil, re, nil
	}
	if neverSent(err) {
		return nil, nil, err
	}
	return nil, nil, fmt.Errorf("%w: %v", ErrIndeterminate, err)
}

func scrubOrNull(raw json.RawMessage, secret string) json.RawMessage {
	out, err := Scrub(orNull(raw), secret)
	if err != nil {
		return nil
	}
	return out
}
