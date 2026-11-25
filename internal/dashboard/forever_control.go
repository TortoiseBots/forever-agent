package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/serveclient"
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
	c.foreverCommander(ac, "cmd", verb, nil, nil)
}

// foreverPlaceRequest is the body of POST /api/control/<id>/place on a
// Forever agent: the Positions panel's choice. Verb is one of place (with
// the shape/gap/anchor below), place_follow, rally, fallback or resume.
type foreverPlaceRequest struct {
	Verb   string             `json:"verb"`
	Shape  string             `json:"shape,omitempty"`
	Gap    string             `json:"gap,omitempty"`
	Anchor string             `json:"anchor,omitempty"`
	Face   *int               `json:"face,omitempty"`
	Who    []string           `json:"who,omitempty"`
	Point  *foreverPlacePoint `json:"point,omitempty"`
	Map    *uint32            `json:"map,omitempty"` // the map the point was picked on
	Rally  string             `json:"rally,omitempty"`
	To     string             `json:"to,omitempty"`
}

// foreverPlacePoint is the map point the operator clicked, in game
// coordinates (x, y).
type foreverPlacePoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// place is the Positions panel handler: a placement on a Forever agent. The
// body is checked by the pure placeRequestArgs against the card's own
// members; anything else is a 400 and no call goes out.
func (c *Controller) place(ac *actionContext) {
	if ac.agent.Kind != AgentForever {
		jsonError(ac.w, 404, "no such control action")
		return
	}
	var req foreverPlaceRequest
	if !decodeBody(ac.w, ac.r, &req) {
		return
	}
	card, err := c.placeCard(ac)
	if err != nil {
		jsonError(ac.w, 502, "the commander card could not be read, so nothing was sent: %v", err)
		return
	}
	args, err := placeRequestArgs(req, card)
	if err != nil {
		jsonError(ac.w, 400, "%v", err)
		return
	}
	verb := req.Verb
	if verb == "place_follow" {
		verb = "place"
	}
	c.foreverCommander(ac, "cmd", verb, nil, args)
}

// placeCard reads the card the Positions panel was drawn from: the first
// commander {op:status} card, or nil when the character commands none.
func (c *Controller) placeCard(ac *actionContext) (*serveclient.CommanderCard, error) {
	cl, err := serveclient.New(ac.agent.Addr, ac.agent.TokenPath)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ac.r.Context()), controlTimeout)
	defer cancel()
	res, err := cl.Commander(ctx, serveclient.CommanderParams{Op: "status"})
	if err != nil {
		return nil, err
	}
	return foreverCommanderCard(res), nil
}

// placeRequestArgs builds the `.tb cmd` words from the panel's choice, from
// fixed lists only: 8 shapes, tight/normal/wide or a number 1.5-12 printed
// with one fraction digit, anchor point to at:<x>,<y> (2 decimals), face
// 0-359, who keywords from the fixed list or names of the card's own
// members. place_follow becomes `follow [who...]`. Anything else is an
// error and no call goes out.
func placeRequestArgs(req foreverPlaceRequest, card *serveclient.CommanderCard) ([]string, error) {
	members := map[string]bool{}
	if card != nil {
		for _, m := range card.Members {
			members[strings.ToLower(m.Bot)] = true
		}
	}
	switch req.Verb {
	case "place":
		shape := strings.ToLower(strings.TrimSpace(req.Shape))
		switch shape {
		case "close", "circle", "arc", "spread", "line", "column", "spear", "arrow":
		default:
			return nil, fmt.Errorf("shape must be one of close, circle, arc, spread, line, column, spear, arrow")
		}
		args := []string{shape}
		if g := strings.TrimSpace(req.Gap); g != "" {
			switch gl := strings.ToLower(g); gl {
			case "tight", "normal", "wide":
				args = append(args, gl)
			default:
				n, err := strconv.ParseFloat(g, 64)
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 1.5 || n > 12 {
					return nil, fmt.Errorf("gap must be tight, normal, wide or 1.5-12")
				}
				args = append(args, strconv.FormatFloat(math.Round(n*10)/10, 'f', 1, 64))
			}
		}
		anchor := strings.ToLower(strings.TrimSpace(req.Anchor))
		if anchor == "" {
			anchor = "here"
		}
		switch anchor {
		case "here", "target", "tank":
			args = append(args, anchor)
		case "point":
			word, err := placeAtWord(req, card)
			if err != nil {
				return nil, err
			}
			args = append(args, word)
		default:
			return nil, fmt.Errorf("anchor must be one of here, target, tank, point")
		}
		if req.Face != nil {
			if *req.Face < 0 || *req.Face > 359 {
				return nil, fmt.Errorf("face must be 0-359")
			}
			args = append(args, fmt.Sprintf("face:%d", *req.Face))
		}
		who, err := placeWhoWords(req.Who, members)
		if err != nil {
			return nil, err
		}
		return append(args, who...), nil
	case "place_follow":
		who, err := placeWhoWords(req.Who, members)
		if err != nil {
			return nil, err
		}
		return append([]string{"follow"}, who...), nil
	case "rally":
		switch r := strings.ToLower(strings.TrimSpace(req.Rally)); r {
		case "", "here":
			return []string{"here"}, nil
		case "point":
			word, err := placeAtWord(req, card)
			if err != nil {
				return nil, err
			}
			return []string{word}, nil
		case "clear":
			return []string{"clear"}, nil
		case "show":
			return nil, nil
		default:
			return nil, fmt.Errorf("rally must be one of here, point, clear, show")
		}
	case "fallback":
		switch t := strings.ToLower(strings.TrimSpace(req.To)); t {
		case "", "default":
			return nil, nil
		case "rally":
			return []string{"rally"}, nil
		case "here":
			return []string{"here"}, nil
		case "point":
			word, err := placeAtWord(req, card)
			if err != nil {
				return nil, err
			}
			return []string{word}, nil
		default:
			return nil, fmt.Errorf("to must be one of default, rally, here, point")
		}
	case "resume":
		return nil, nil
	}
	return nil, fmt.Errorf("verb must be one of place, place_follow, rally, fallback, resume")
}

// mapCoordBound is the world's coordinate bound (Trinity::IsValidMapCoord):
// a point outside it is not on the map at all.
const mapCoordBound = 17066.0

// pointReachYd is the module's typed-point reach (TbPlacePure.h kPointReachYd):
// an at: point farther than this from the commander is refused server-side.
const pointReachYd = 100.0

// placeAtWord checks one clicked map point and prints it as the module's
// at:<x>,<y> word. The point must be finite, inside the map's bounds, on the
// map the page picked it on (the card's own map, so a click before a zone
// change never sends stale coordinates), and within the module's reach of the
// commander when the card carries his position.
func placeAtWord(req foreverPlaceRequest, card *serveclient.CommanderCard) (string, error) {
	p := req.Point
	if p == nil {
		return "", fmt.Errorf("the point needs {x, y}")
	}
	if math.IsNaN(p.X) || math.IsInf(p.X, 0) || math.IsNaN(p.Y) || math.IsInf(p.Y, 0) {
		return "", fmt.Errorf("the point is not a finite coordinate")
	}
	if p.X < -mapCoordBound || p.X > mapCoordBound || p.Y < -mapCoordBound || p.Y > mapCoordBound {
		return "", fmt.Errorf("the point is not on the map")
	}
	if req.Map == nil {
		return "", fmt.Errorf("the point needs the map it was picked on")
	}
	if card == nil || card.Map == nil {
		return "", fmt.Errorf("the card's map is not known, so no point is sent")
	}
	if *req.Map != *card.Map {
		return "", fmt.Errorf("the point is on another map than the card")
	}
	if pos := card.Pos; pos != nil {
		if d := math.Hypot(p.X-pos.X, p.Y-pos.Y); d > pointReachYd {
			return "", fmt.Errorf("the point is %.0f yd from the commander; the module reaches %g yd", d, pointReachYd)
		}
	}
	return fmt.Sprintf("at:%.2f,%.2f", p.X, p.Y), nil
}

// placeWhoWords checks the panel's Who list: the fixed keywords or names of
// the card's own members.
func placeWhoWords(who []string, members map[string]bool) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, w := range who {
		wl := strings.ToLower(strings.TrimSpace(w))
		if wl == "" {
			continue
		}
		switch wl {
		case "all", "tanks", "healers", "melee", "ranged", "dps",
			"warrior", "paladin", "hunter", "rogue", "priest", "shaman", "mage", "warlock", "druid",
			"group1", "group2", "group3", "group4", "group5", "group6", "group7", "group8":
			if !seen[wl] {
				seen[wl] = true
				out = append(out, wl)
			}
			continue
		}
		if !members[wl] {
			return nil, fmt.Errorf("who %q is not a keyword or a bound bot", w)
		}
		if !seen["bot:"+wl] {
			seen["bot:"+wl] = true
			out = append(out, strings.TrimSpace(w))
		}
	}
	return out, nil
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
	c.foreverCommander(ac, "run", verb, map[string]bool{"confirm": req.Confirm}, nil)
}

// requestArgs is what the control log's request line carries: the confirm
// flags when set, the commander's argument words when sent (the place
// handler's `args` words), so the log shows what actually went out.
func requestArgs(flags map[string]bool, cmdArgs []string) any {
	if len(cmdArgs) > 0 {
		return cmdArgs
	}
	if len(flags) == 0 {
		return nil
	}
	return flags
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
func (c *Controller) foreverCommander(ac *actionContext, op, verb string, args map[string]bool, cmdArgs []string) {
	ctx := ac.r.Context()
	if err := c.log.append(controlRecord{Phase: "request", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
		Agent: ac.agent.ID, Action: ac.action, Verb: verb, Args: requestArgs(args, cmdArgs)}); err != nil {
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
	params := serveclient.CommanderParams{Op: op, Verb: verb, Args: cmdArgs}
	var out serveclient.CommanderResult
	var callErr error
	if op == "run" {
		// The daemon's run verbs need the lead they act on: the dashboard's buttons name none, so the character's one
		// bound lead is read first. Nothing is sent when that read fails or the character commands none or several.
		params.LeadBot, callErr = foreverOnlyLead(wctx, cl)
	}
	if callErr == nil {
		out, callErr = cl.Commander(wctx, params)
	}
	rep := foreverReplyOf(out)
	res := controlRecord{Phase: "result", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
		Agent: ac.agent.ID, Action: ac.action, Verb: verb, RPC: "commander", Outcome: "ok"}
	okv := callErr == nil
	code, isRPC := serveclient.CodeOf(callErr)
	switch {
	case callErr == nil:
		var refusal string
		if refusal, okv = foreverRefusal(out); !okv {
			res.Outcome = "error"
			res.Message = redactMessage(refusal, cl.Token())
		} else {
			res.Message = redactMessage(foreverSummary(out), cl.Token())
		}
	case isRPC:
		res.Outcome = "error"
		res.Code = code
		res.Message = redactMessage(callErr.Error(), cl.Token())
	case errors.Is(callErr, errForeverNoLead), errors.Is(callErr, serveclient.ErrUnauthorized), errors.Is(callErr, serveclient.ErrReconnecting), errors.Is(callErr, serveclient.ErrDisconnected), neverSent(callErr):
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
			"executed": okv, "message": res.Message, "reply": rep, "result": json.RawMessage(scrubOrNull(jsonBytes(out), cl.Token()))})
		return
	case isRPC:
		// The daemon answered with an error (a refusal with its reason, an
		// unknown method, a lost link): a failed control, not status "ok".
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

// foreverReply is the module's answer to a control, as the page shows it: the lines that went out, the per-bot lines
// and the waited order states.
type foreverReply struct {
	Sent  []string                   `json:"sent,omitempty"`
	Bots  []serveclient.CommanderBot `json:"bots,omitempty"`
	Final map[string]string          `json:"final,omitempty"`
	Notes []string                   `json:"notes,omitempty"`
}

func foreverReplyOf(r serveclient.CommanderResult) foreverReply {
	return foreverReply{Sent: r.Sent, Bots: r.Bots, Final: r.Final, Notes: r.Notes}
}

// foreverRefusal says whether the module refused an answered control, with its words. The daemon answers a call the
// module refused outright as an RPC error, so what reaches here is a call that went out: it is a refusal when every
// bot line is refused or a waited bot's order state is "fail:<code>".
func foreverRefusal(r serveclient.CommanderResult) (string, bool) {
	var parts []string
	refused := 0
	for _, b := range r.Bots {
		if b.Status != "refused" {
			continue
		}
		refused++
		p := b.Bot + ": refused"
		if b.Error != "" {
			p += " " + b.Error
		}
		if b.Msg != "" {
			p += ": " + b.Msg
		}
		parts = append(parts, p)
	}
	if len(r.Bots) > 0 && refused == len(r.Bots) {
		return strings.Join(parts, "; "), false
	}
	names := make([]string, 0, len(r.Final))
	for bot := range r.Final {
		names = append(names, bot)
	}
	sort.Strings(names)
	var failed []string
	for _, bot := range names {
		if st := r.Final[bot]; st != "ok" {
			failed = append(failed, bot+": "+st)
		}
	}
	if len(failed) > 0 {
		return strings.Join(failed, "; "), false
	}
	return "", true
}

// foreverSummary is the module's answer in words for an executed control: the line sent and each bot's status.
// For a place/fallback call the summary is `placed N, refused M` plus `bot:
// code` per refused row (the page shows the counts first).
func foreverSummary(r serveclient.CommanderResult) string {
	if r.Place != nil && (r.Place.Verb == "place" || r.Place.Verb == "fallback") {
		msg := fmt.Sprintf("placed %d, refused %d", r.Place.Placed, r.Place.Refused)
		var bad []string
		for _, b := range r.Place.Bots {
			if b.Status == "refused" {
				p := b.Bot
				if b.Code != "" {
					p += ": " + b.Code
				}
				bad = append(bad, p)
			}
		}
		if len(bad) > 0 {
			msg += " (" + strings.Join(bad, ", ") + ")"
		}
		return strings.Join(r.Sent, "; ") + " -> " + msg
	}
	var parts []string
	for _, b := range r.Bots {
		parts = append(parts, b.Bot+": "+b.Status)
	}
	msg := strings.Join(r.Sent, "; ")
	if len(parts) > 0 {
		msg += " -> " + strings.Join(parts, ", ")
	}
	return msg
}

// errForeverNoLead is a run button pressed while the character commands no lead or several: nothing was sent.
var errForeverNoLead = errors.New("the character commands no lead or more than one, so there is no run to name")

// foreverOnlyLead is the lead of the character's one binding card, for the run buttons. A character that commands
// none or more than one has no lead the page can mean.
func foreverOnlyLead(ctx context.Context, cl *serveclient.Client) (string, error) {
	res, err := cl.Commander(ctx, serveclient.CommanderParams{Op: "status"})
	if err != nil {
		if _, ok := serveclient.CodeOf(err); ok {
			return "", err
		}
		return "", fmt.Errorf("%w: reading the lead failed: %v", errForeverNoLead, err)
	}
	if len(res.Cards) != 1 {
		return "", errForeverNoLead
	}
	return res.Cards[0].Lead, nil
}

func scrubOrNull(raw json.RawMessage, secret string) json.RawMessage {
	out, err := Scrub(orNull(raw), secret)
	if err != nil {
		return nil
	}
	return out
}
