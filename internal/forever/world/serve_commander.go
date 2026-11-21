package world

// The commander method: remote control of the module's TortoiseBots party over this session's own command
// channel. The lines are the typed `.tb` client's (internal/forever/command/tbcmd) and the controller is cmdctl
// (round C2): it binds the party to this character, reads the binding card, sends orders, roles and run verbs, and
// asks the module once per session whether a verb's package is installed. Every line goes through the session's
// command channel with its GM guard (serveCommandOptions, the guard half of serveCommandLine, which keeps the
// typed client's own token and end marker), and nothing here composes a command line.
//
// Params keep the old daemon's names (op, lead_bot, bots, verb, args, wait_ms) plus pause_run, so a caller written
// for it sends the same JSON; the result is typed instead of the 1.12 add-on protocol's reply. The op `view` is
// the one the port cut: it answers the port's own refusal, never an empty success.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/cmdctl"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveCommanderParams is the commander method's params: the old daemon's shape (a caller's JSON keeps working)
// with pause_run (D4: an order while a run runs is refused unless the call allows the module to pause it). On is
// the old view shape's flag: it is accepted and ignored, so {"op":"view","on":true} answers the port's own
// refusal, never an unknown-field error.
type serveCommanderParams struct {
	Op       string   `json:"op"`                 // bind, unbind, status, cmd, run or view
	LeadBot  string   `json:"lead_bot,omitempty"` // bind, unbind, cmd and run: the lead the call names
	Bots     []string `json:"bots,omitempty"`     // bind: the other party members
	Verb     string   `json:"verb,omitempty"`     // cmd: the order's verb; run: start, stop, pause, resume, status or events
	Args     []string `json:"args,omitempty"`     // cmd: the verb's arguments; run: the plan, the reason or the event sequence
	WaitMS   int      `json:"wait_ms,omitempty"`  // cmd: the per-bot poll, 0..30000 ms (clamped); other ops ignore it
	PauseRun bool     `json:"pause_run,omitempty"`
	On       *bool    `json:"on,omitempty"`
}

// ServeCommander is a commander call's answer: the lines that went out, the module's per-bot lines, the binding
// card (a mutation's own read, or every card of a status), the waited order states, and a run verb's records or
// events. A refusal is the typed RPC error instead; its Data carries the partial result (the lines that went out
// and the bots, card and final the call had read), and its message names each bot the module refused.
type ServeCommander struct {
	Self       string                `json:"self,omitempty"` // status: the character whose cards these are
	Sent       []string              `json:"sent,omitempty"`
	Bots       []ServeCommanderBot   `json:"bots,omitempty"`
	Card       *ServeCommanderCard   `json:"card,omitempty"`
	Cards      []ServeCommanderCard  `json:"cards,omitempty"`
	Final      map[string]string     `json:"final"`
	PartyError string                `json:"party_error,omitempty"` // status: why the party state read failed
	Runs       []ServeCommanderRun   `json:"runs,omitempty"`
	Events     []ServeCommanderEvent `json:"events,omitempty"`
	Notes      []string              `json:"notes,omitempty"` // what the call did on its own, beyond the lines it sent
}

// ServeCommanderBot is one bot's line of an answer.
type ServeCommanderBot struct {
	Bot    string `json:"bot"`
	Status string `json:"status"` // queued, refused, stored, bound, joined
	Seq    int    `json:"seq,omitempty"`
	Error  string `json:"error,omitempty"` // the module's refusal code
	Msg    string `json:"msg,omitempty"`
}

// ServeCommanderCard is one commander's binding card, in the RPC's own words (the module's §11 record, typed).
// A number the module did not send is absent, never a zero standing in for "not known".
type ServeCommanderCard struct {
	Cmd       string                 `json:"cmd"`
	CGUID     string                 `json:"cguid,omitempty"`
	Online    bool                   `json:"online"`
	Lead      string                 `json:"lead"`
	Group     *int                   `json:"group,omitempty"`
	Bound     int                    `json:"bound"`
	Bots      int                    `json:"bots"`
	System    bool                   `json:"system"`
	InParty   bool                   `json:"in_party"`
	Raid      bool                   `json:"raid"`
	PlayerLed bool                   `json:"player_led"`
	BoundAt   int                    `json:"bound_at,omitempty"`
	Orders    int                    `json:"orders"`
	Map       *uint32                `json:"map,omitempty"`
	Pos       *ServeCommanderPos     `json:"pos,omitempty"`
	Members   []ServeCommanderMember `json:"members,omitempty"`
	Log       []ServeCommanderOrder  `json:"log,omitempty"`
	Run       *ServeCommanderRun     `json:"run,omitempty"`
	Extra     map[string]string      `json:"extra,omitempty"`
}

// ServeCommanderPos is a map position.
type ServeCommanderPos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// ServeCommanderMember is one bound bot of a card; the lead comes first.
type ServeCommanderMember struct {
	Bot       string            `json:"bot"`
	GUID      string            `json:"guid"`
	Class     string            `json:"class"`
	Role      string            `json:"role"`
	Level     int               `json:"level"`
	HP        int               `json:"hp"`
	MaxHP     int               `json:"max_hp"`
	Power     int               `json:"power"`
	MaxPower  int               `json:"max_power"`
	PowerType string            `json:"power_type,omitempty"`
	Alive     bool              `json:"alive"`
	Lead      bool              `json:"lead"`
	Map       uint32            `json:"map"`
	Zone      int               `json:"zone"`
	Pos       ServeCommanderPos `json:"pos"`
	Sub       *int              `json:"sub,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
}

// ServeCommanderOrder is one logged order of a card, oldest first.
type ServeCommanderOrder struct {
	At     int               `json:"at"`
	Verb   string            `json:"verb"`
	Detail string            `json:"detail,omitempty"`
	OK     bool              `json:"ok"`
	Result string            `json:"result,omitempty"`
	Extra  map[string]string `json:"extra,omitempty"`
}

// ServeCommanderRun is one `run` record of a card or of a run verb.
type ServeCommanderRun struct {
	Lead    string               `json:"lead"`
	Plan    string               `json:"plan"`
	State   string               `json:"state"`
	Step    int                  `json:"step"`
	Steps   int                  `json:"steps"`
	Leg     *int                 `json:"leg,omitempty"`
	LegName string               `json:"leg_name,omitempty"`
	Kind    string               `json:"kind,omitempty"`
	Phase   string               `json:"phase,omitempty"`
	Note    string               `json:"note,omitempty"`
	Pull    int                  `json:"pull,omitempty"`
	Pulls   int                  `json:"pulls,omitempty"`
	Bosses  []ServeCommanderBoss `json:"bosses,omitempty"`
	Deaths  int                  `json:"deaths"`
	Wipes   int                  `json:"wipes"`
	Pauses  int                  `json:"pauses"`
	Elapsed int                  `json:"elapsed"`
	Why     string               `json:"why,omitempty"`
	Ev      *int                 `json:"ev,omitempty"`
	Extra   map[string]string    `json:"extra,omitempty"`
}

// ServeCommanderBoss is one boss of a run record.
type ServeCommanderBoss struct {
	Entry uint32 `json:"entry"`
	Alive bool   `json:"alive"`
	State string `json:"state"`
}

// ServeCommanderEvent is one run event of a `run events` answer.
type ServeCommanderEvent struct {
	Seq   int               `json:"seq"`
	T     int               `json:"t"`
	Kind  string            `json:"kind"`
	Step  *int              `json:"step,omitempty"`
	Leg   *int              `json:"leg,omitempty"`
	Unit  *uint32           `json:"unit,omitempty"`
	Why   string            `json:"why,omitempty"`
	Extra map[string]string `json:"extra,omitempty"`
}

// commanderCtl is the commander method's one controller. The tbcmd client under it is not bound to a session: its
// runner reads the current one on every line, so a re-entry needs no new client, only Reset(), which drops what
// the old session knew about the module's packages.
type commanderCtl struct{ c cmdctl.Controller }

// commanderRunner is the tbcmd transport of the commander method. Every line goes through the session's own
// command channel and its GM guard, exactly as the `gm` and `bot` methods' lines do.
type commanderRunner struct{ st *serveState }

func (r *commanderRunner) Run(ctx context.Context, line string, o command.Options) (*command.Reply, error) {
	s := r.st.session.Load()
	if s == nil {
		return nil, ErrLinkLost
	}
	return s.serveCommandOptions(ctx, line, o)
}

// resetCommander builds the commander method's controller for the first session and resets it for every later
// one. A re-entry may face a module rebuilt between the two logins, so the capability answers it cached are
// dropped; nothing else is kept between sessions.
func (st *serveState) resetCommander(s *session) {
	if st.cmdr == nil {
		client := tbcmd.NewClient(&commanderRunner{st: st})
		st.cmdr = &commanderCtl{c: cmdctl.NewTB(client, s.report.Character.Name)}
		return
	}
	st.cmdr.c.Reset()
}

// serveCommander runs one commander call. A waited cmd carries one overall deadline: the clamped wait_ms plus a
// fixed 5 s read budget, capped at 15 s. The poll itself runs on the session goroutine like every other action, so
// a long wait still holds the loop (and every other call behind it) for that bounded time; callers that need the
// loop free use a small wait_ms and poll `status` themselves.
func (s *session) serveCommander(ctx context.Context, st *serveState, p serveCommanderParams) (ServeCommander, error) {
	if st.cmdr == nil {
		return ServeCommander{}, &rpc.Error{Code: rpc.CodeInternal, Message: "internal: the commander controller is not installed"}
	}
	c := st.cmdr.c
	switch strings.ToLower(strings.TrimSpace(p.Op)) {
	case "bind":
		if strings.TrimSpace(p.LeadBot) == "" {
			return ServeCommander{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "commander bind: lead_bot is required"}
		}
		// The module keeps a binding only for a commander who stands OUTSIDE the party: while the commander is a
		// member it releases the binding when the lead no longer leads the group (leader-changed). So a bind from
		// inside a group leaves the group first, and the answer says so.
		var notes []string
		if s.party != nil && s.party.RosterValid && s.party.InParty {
			if err := s.serveLeaveGroup(ctx); err != nil {
				return ServeCommander{}, &rpc.Error{Code: rpc.CodeGame, Message: "commander bind: leaving the group first failed: " + err.Error()}
			}
			notes = append(notes, "left the group first: the module keeps a binding only while the commander stands outside the party")
		}
		out, err := commanderAnswer(c.Bind(ctx, p.LeadBot, p.Bots))
		out.Notes = append(out.Notes, notes...)
		var re *rpc.Error
		if errors.As(err, &re) {
			if partial, ok := re.Data.(ServeCommander); ok {
				partial.Notes = append(partial.Notes, notes...)
				re.Data = partial
			}
		}
		return out, err
	case "unbind":
		if strings.TrimSpace(p.LeadBot) == "" {
			return ServeCommander{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "commander unbind: lead_bot is required"}
		}
		return commanderAnswer(c.Unbind(ctx, p.LeadBot))
	case "status":
		rep, err := c.Status(ctx)
		if err != nil {
			return ServeCommander{}, serveCommanderError(cmdctl.Result{}, err)
		}
		return commanderStatus(rep), nil
	case "cmd":
		if strings.TrimSpace(p.Verb) == "" {
			return ServeCommander{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "commander cmd: verb is required"}
		}
		wait, budget := commanderWait(p.WaitMS)
		callCtx, cancel := context.WithTimeout(ctx, wait+budget)
		defer cancel()
		return commanderAnswer(c.Order(callCtx, cmdctl.Order{
			Verb:     p.Verb,
			Args:     p.Args,
			Lead:     p.LeadBot,
			PauseRun: p.PauseRun,
			Wait:     wait,
		}))
	case "run":
		verb := strings.ToLower(strings.TrimSpace(p.Verb))
		if verb == "" {
			return ServeCommander{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "commander run: verb is required: start, stop, pause, resume, status or events"}
		}
		lead := p.LeadBot
		if strings.TrimSpace(lead) == "" {
			switch verb {
			case "status", "events":
				// A status or events read with no lead reads every run the module holds.
				lead = "all"
			case "start":
				// An old `run start` named no lead: the only lead is what it meant.
				var err error
				if lead, err = commanderOnlyLead(ctx, c); err != nil {
					return ServeCommander{}, err
				}
			}
		}
		// The reason words of `run stop` are several, the plan and the event sequence one; the controller's
		// checks are the module's own grammar's.
		return commanderAnswer(c.Run(ctx, verb, lead, strings.Join(p.Args, " ")))
	case "view":
		return ServeCommander{}, &rpc.Error{Code: rpc.CodeGame, Message: "commander view: not ported on this server"}
	default:
		return ServeCommander{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "commander op must be bind, unbind, status, cmd, run or view"}
	}
}

// commanderReadBudget is the fixed budget for a waited cmd's reads around its poll: the guard, the order and the
// post-order ownership reads. A waited cmd's overall deadline is the clamped wait plus this, capped at 15 s.
const commanderReadBudget = 5 * time.Second

// commanderWait clamps the caller's wait_ms to 0..30 s before the Duration conversion (an int millisecond count
// near 9e12 would otherwise wrap the Duration negative), and pairs it with the read budget; the pair is capped
// at 15 s.
func commanderWait(waitMS int) (wait, budget time.Duration) {
	if waitMS < 0 {
		waitMS = 0
	}
	if waitMS > 30000 {
		waitMS = 30000
	}
	wait = time.Duration(waitMS) * time.Millisecond
	budget = commanderReadBudget
	if total := wait + budget; total > 15*time.Second {
		budget = 15*time.Second - wait
	}
	return wait, budget
}

// commanderOnlyLead names the session character's one bound lead, for an old `run start` that names none. The
// module answers one card per commander, so anything but exactly one card is refused with a clear error instead
// of guessing.
func commanderOnlyLead(ctx context.Context, c cmdctl.Controller) (string, error) {
	rep, err := c.Status(ctx)
	if err != nil {
		return "", serveCommanderError(cmdctl.Result{}, err)
	}
	if len(rep.Cards) != 1 {
		return "", &rpc.Error{Code: rpc.CodeInvalidParams, Message: "commander run start: lead_bot is required when the character commands no lead or more than one"}
	}
	return rep.Cards[0].Lead, nil
}

// serveCommanderError maps the controller's and the module's refusals onto the RPC's codes, carrying the call's
// partial result in Data: the lines that went out and the bots, card and final the call had read. A caller that
// sees an order in Sent knows it went out and never retries it blindly. Every refusal is the game's answer
// (-32003) with its machine-readable code first in the message (not-available, run-active, not-owned, refused,
// bind-refused, or the module's own error code), so a caller keys off the message's first word; the link's own
// errors keep their codes (-32002 offline, -32005 reconnecting).
func serveCommanderError(res cmdctl.Result, err error) error {
	if err == nil {
		return nil
	}
	var na *cmdctl.NotAvailableError
	var ra *cmdctl.RunActiveError
	var no *cmdctl.NotOwnedError
	var rf *cmdctl.RefusedError
	var br *cmdctl.BindRefusedError
	var ue *cmdctl.UsageError
	var ce *tbcmd.CommandError
	partial := commanderResult(res)
	switch {
	case errors.As(err, &na):
		return commanderGame(na.Error(), partial)
	case errors.As(err, &ra):
		return commanderGame(ra.Error(), partial)
	case errors.As(err, &no):
		return commanderGame("not-owned: "+strings.TrimPrefix(no.Error(), "cmdctl: "), partial)
	case errors.As(err, &rf):
		return commanderGame(rf.Error(), partial)
	case errors.As(err, &br):
		return commanderGame("bind-refused: "+strings.TrimPrefix(br.Error(), "cmdctl: "), partial)
	case errors.As(err, &ue):
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: "commander: " + ue.Msg, Data: partial}
	case errors.As(err, &ce):
		return commanderGame(commanderModuleCode(ce), partial)
	}
	rerr := serveError(err)
	if re, ok := rerr.(*rpc.Error); ok {
		re.Data = partial
		return re
	}
	return rerr
}

// commanderGame is one refusal with a code first, carrying the call's partial result.
func commanderGame(msg string, partial ServeCommander) error {
	return &rpc.Error{Code: rpc.CodeGame, Message: msg, Data: partial}
}

// commanderModuleCode is the module's error line with its code first: the tbcmd text without the layer's "tb: "
// prefix (`no-binding`, `unknown-verb cmd=cmd`, ...), so the first word is the code.
func commanderModuleCode(e *tbcmd.CommandError) string {
	var b strings.Builder
	b.WriteString(e.Code)
	if e.Cmd != "" {
		b.WriteString(" (")
		b.WriteString(e.Cmd)
		b.WriteString(")")
	}
	if e.HasMsg {
		b.WriteString(": ")
		b.WriteString(e.Msg)
	}
	return b.String()
}

// commanderAnswer maps a controller call's answer, or its refusal, onto the RPC's result or typed error. The
// refusal carries the call's partial result in its Data (see serveCommanderError).
func commanderAnswer(res cmdctl.Result, err error) (ServeCommander, error) {
	if err != nil {
		return ServeCommander{}, serveCommanderError(res, err)
	}
	return commanderResult(res), nil
}

// commanderResult maps the controller's answer onto the RPC's result, presence flags included. Final is kept
// present on every answer ({} when no wait ran), so "waited, nothing answered" ({}) never looks like "no wait".
func commanderResult(res cmdctl.Result) ServeCommander {
	out := ServeCommander{Sent: res.Sent, Final: res.Final}
	if out.Final == nil {
		out.Final = map[string]string{}
	}
	for _, b := range res.Bots {
		out.Bots = append(out.Bots, ServeCommanderBot{
			Bot: b.Bot, Status: b.Status, Seq: b.Seq, Error: b.Error, Msg: b.Msg,
		})
	}
	if res.Card != nil {
		card := commanderCard(*res.Card)
		out.Card = &card
	}
	for _, run := range res.Runs {
		out.Runs = append(out.Runs, commanderRun(run))
	}
	for _, ev := range res.Events {
		out.Events = append(out.Events, commanderEvent(ev))
	}
	return out
}

// commanderStatus maps a status read: the character's binding cards, and why the party state read failed.
func commanderStatus(rep cmdctl.Status) ServeCommander {
	out := ServeCommander{Self: rep.Self, PartyError: rep.PartyError}
	for _, c := range rep.Cards {
		out.Cards = append(out.Cards, commanderCard(c))
	}
	return out
}

// commanderCard maps one binding card.
func commanderCard(c tbcmd.Card) ServeCommanderCard {
	out := ServeCommanderCard{
		Cmd: c.Cmd, CGUID: c.CGUID, Online: c.Online, Lead: c.Lead,
		Bound: c.Bound, Bots: c.Bots, System: c.System, InParty: c.InParty, Raid: c.Raid,
		PlayerLed: c.PlayerLed, BoundAt: c.BoundAt, Orders: c.Orders, Extra: c.Extra,
	}
	if c.HasGroup {
		group := c.Group
		out.Group = &group
	}
	if c.HasMap {
		id := c.Map
		out.Map = &id
	}
	if c.Pos != nil {
		out.Pos = &ServeCommanderPos{X: c.Pos.X, Y: c.Pos.Y, Z: c.Pos.Z}
	}
	for _, m := range c.Members {
		member := ServeCommanderMember{
			Bot: m.Bot, GUID: m.GUID, Class: m.Class, Role: m.Role, Level: m.Level,
			HP: m.HP, MaxHP: m.MaxHP, Power: m.Power, MaxPower: m.MaxPower, PowerType: m.PowerTyp,
			Alive: m.Alive, Lead: m.Lead, Map: m.Map, Zone: m.Zone,
			Pos: ServeCommanderPos{X: m.Pos.X, Y: m.Pos.Y, Z: m.Pos.Z}, Extra: m.Extra,
		}
		if m.HasSub {
			sub := m.Sub
			member.Sub = &sub
		}
		out.Members = append(out.Members, member)
	}
	for _, o := range c.Log {
		out.Log = append(out.Log, ServeCommanderOrder{At: o.At, Verb: o.Verb, Detail: o.Detail, OK: o.OK, Result: o.Result, Extra: o.Extra})
	}
	if c.Run != nil {
		run := commanderRun(*c.Run)
		out.Run = &run
	}
	return out
}

// commanderRun maps one run record.
func commanderRun(r tbcmd.RunState) ServeCommanderRun {
	out := ServeCommanderRun{
		Lead: r.Lead, Plan: r.Plan, State: r.State, Step: r.Step, Steps: r.Steps,
		LegName: r.LegName, Kind: r.Kind, Phase: r.Phase, Note: r.Note,
		Pull: r.Pull, Pulls: r.Pulls, Deaths: r.Deaths, Wipes: r.Wipes, Pauses: r.Pauses,
		Elapsed: r.Elapsed, Why: r.Why, Extra: r.Extra,
	}
	if r.HasLeg {
		leg := r.Leg
		out.Leg = &leg
	}
	if r.HasEv {
		ev := r.Ev
		out.Ev = &ev
	}
	for _, b := range r.Bosses {
		out.Bosses = append(out.Bosses, ServeCommanderBoss{Entry: b.Entry, Alive: b.Alive, State: b.State})
	}
	return out
}

// commanderEvent maps one run event.
func commanderEvent(e tbcmd.RunEvent) ServeCommanderEvent {
	out := ServeCommanderEvent{Seq: e.Seq, T: e.T, Kind: e.Kind, Why: e.Why, Extra: e.Extra}
	if e.HasStep {
		step := e.Step
		out.Step = &step
	}
	if e.HasLeg {
		leg := e.Leg
		out.Leg = &leg
	}
	if e.HasUnit {
		unit := e.Unit
		out.Unit = &unit
	}
	return out
}
