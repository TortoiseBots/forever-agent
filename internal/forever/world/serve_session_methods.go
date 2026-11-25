package world

// The RPC method table of forever serve. A method whose ForeverCore code exists in this package is registered with
// its typed params; every old name this server does not implement is registered in serve_rpc_methods.go with its
// documented refusal, so an old caller gets a name and its reason, never an empty success and never a bare
// "not ported".

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

type serveEmpty struct{}

// ServePath is path_to/walk_path's result.
type ServePath struct {
	Map     uint32       `json:"map"`
	From    [3]float32   `json:"from"`
	To      [3]float32   `json:"to"`
	Status  string       `json:"status"`
	Reason  string       `json:"reason,omitempty"`
	Points  [][3]float32 `json:"points"`
	Corners int          `json:"corners"`
	Length  float32      `json:"length"`
}

// ServePartyInfo is the party method's result.
type ServePartyInfo struct {
	InGroup bool `json:"in_group"`
	// Raid is the roster's raid flag; Assistant and Subgroup describe this character (its subgroup is 1-8, as the
	// raid frame numbers them, and 0 in a party).
	Raid          bool               `json:"raid,omitempty"`
	Assistant     bool               `json:"assistant,omitempty"`
	Subgroup      int                `json:"subgroup,omitempty"`
	Leader        GUID               `json:"leader,omitempty"`
	Members       []ServePartyMember `json:"members"`
	PendingInvite string             `json:"pending_invite,omitempty"`
	// The group's loot settings from the last roster that carried them: the method's name (party.LootName), the
	// threshold quality and the master looter when there is one.
	LootMethod    string `json:"loot_method,omitempty"`
	LootThreshold int    `json:"loot_threshold,omitempty"`
	LootMaster    GUID   `json:"loot_master,omitempty"`
	// Marks are the raid target icons the group holds, one row per set icon (the model's eight slots, empty ones
	// omitted).
	Marks []ServeMark `json:"marks,omitempty"`
}

// ServeAcceptInviteResult is accept_invite's result: the inviter the held invite named, so a scenario can tell who
// offered the group (the roster's leader may be another member).
type ServeAcceptInviteResult struct {
	Inviter     string `json:"inviter"`
	InviterGUID GUID   `json:"inviter_guid"`
}

// ServeMark is one raid target icon: the icon's name (star, circle, diamond, triangle, moon, square, cross, skull)
// and the unit it is on. It is the mark method's result and one row of marks and of the party method.
type ServeMark struct {
	Icon string `json:"icon"`
	GUID GUID   `json:"guid"`
}

// ServeMarks is the marks method's result: every icon the group holds, in slot order.
type ServeMarks struct {
	Marks []ServeMark `json:"marks"`
}

// serveMarks lists the icons a slot array holds: one row per set icon, in slot order.
func serveMarks(icons [party.MaxMarks]wire.GUID) []ServeMark {
	out := make([]ServeMark, 0, party.MaxMarks)
	for i := range icons {
		if icons[i].IsZero() {
			continue
		}
		if name := party.MarkName(party.Mark(i)); name != "" {
			out = append(out, ServeMark{Icon: name, GUID: GUID(icons[i])})
		}
	}
	return out
}

// ServePartyMember is one party row.
type ServePartyMember struct {
	Name      string `json:"name"`
	GUID      GUID   `json:"guid"`
	Online    bool   `json:"online"`
	Dead      bool   `json:"dead"`
	Leader    bool   `json:"leader"`
	Assistant bool   `json:"assistant,omitempty"` // a raid assistant
	Subgroup  int    `json:"subgroup,omitempty"`  // raid subgroup 1-8; 0 in a party
	Health    int32  `json:"health"`
	MaxHealth int32  `json:"max_health"`
	PowerType uint8  `json:"power_type,omitempty"`
	Power     int32  `json:"power"`
	MaxPower  int32  `json:"max_power"`
	Level     uint16 `json:"level"`
	Zone      uint16 `json:"zone"`
}

// ServeAttack is the attack method's result.
type ServeAttack struct {
	Target   GUID   `json:"target"`
	Duration int64  `json:"duration_ms"`
	Reason   string `json:"reason,omitempty"`
}

// ServeDeath is death_status's result.
type ServeDeath struct {
	Dead        bool    `json:"dead"`
	Ghost       bool    `json:"ghost"`
	Phase       string  `json:"phase"`
	Released    bool    `json:"released"`
	Resurrect   bool    `json:"resurrect"`
	ReclaimInMS int64   `json:"reclaim_in_ms,omitempty"`
	CorpseMap   int32   `json:"corpse_map,omitempty"`
	CorpseX     float32 `json:"corpse_x,omitempty"`
	CorpseY     float32 `json:"corpse_y,omitempty"`
	CorpseZ     float32 `json:"corpse_z,omitempty"`
}

// ServeAcceptSummon is accept_summon's result: the summoner whose offer was answered, and the bit that went out
// (the accept default is true).
type ServeAcceptSummon struct {
	Summoner GUID `json:"summoner"`
	Accepted bool `json:"accepted"`
}

// ServeCorpse is corpse_query's result.
type ServeCorpse struct {
	Found bool    `json:"found"`
	Map   int32   `json:"map"`
	X     float32 `json:"x"`
	Y     float32 `json:"y"`
	Z     float32 `json:"z"`
}

// ServeChatEvent is one chat_log row.
type ServeChatEvent struct {
	Seq     uint64 `json:"seq"`
	Kind    string `json:"kind"`
	Type    string `json:"type"`
	Source  string `json:"source,omitempty"`
	Channel string `json:"channel,omitempty"`
	Text    string `json:"text"`
}

// ServeChatPage is chat_log's result: the event page the old daemon returned, built from this session's chat log.
type ServeChatPage struct {
	Events  []ServeChatEvent `json:"events"`
	Next    uint64           `json:"next"`
	Oldest  uint64           `json:"oldest"`
	Dropped bool             `json:"dropped"`
	More    bool             `json:"more"`
}

// ServeCommand is gm/bot's result: the line sent and the server's visible answer.
type ServeCommand struct {
	Sent     string   `json:"sent"`
	Lines    []string `json:"lines"`
	Complete bool     `json:"complete"`
	End      string   `json:"end,omitempty"`
}

type serveLogoutResult struct {
	Outcome string `json:"outcome"`
}

// ServeTeardownStep is one teardown step, in the old daemon's shape: teardown_cmd.go's teardownCallAgent reads
// step, status and detail, and counts "fail" (and "ending") as a failed step.
type ServeTeardownStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// ServeTeardown is teardown's result. It answers before the daemon stops: the loop ends after this call returns, so
// a caller holding the reply knows the session is gone.
type ServeTeardown struct {
	Character string              `json:"character,omitempty"`
	Steps     []ServeTeardownStep `json:"steps"`
	Failed    int                 `json:"failed"`
	DryRun    bool                `json:"dry_run,omitempty"`
	Seconds   float64             `json:"seconds"`
}

type serveTeardownParams struct {
	// GM is "off" (the default), "on" or "keep": the GM line the teardown sends before the logout, or none at all.
	GM string `json:"gm,omitempty"`
	// DryRun plans the steps and sends nothing; the daemon keeps serving.
	DryRun bool `json:"dry_run,omitempty"`
}

type serveUnitsParams struct {
	Within  float32 `json:"within,omitempty"`
	Near    float32 `json:"near,omitempty"`
	Entry   int32   `json:"entry,omitempty"`
	Name    string  `json:"name,omitempty"`
	Hostile *bool   `json:"hostile,omitempty"`
	Limit   int     `json:"limit,omitempty"`
	// Owner keeps only the units whose owner (ServeUnit.Owner) is this GUID: a hunter's pet, a warlock's demon.
	Owner GUID `json:"owner,omitempty"`
}

type serveUnitParams struct {
	GUID GUID   `json:"guid,omitempty"`
	Name string `json:"name,omitempty"`
}

type serveChatParams struct {
	Type string `json:"type"`
	Text string `json:"text"`
	To   string `json:"to,omitempty"`
}

// serveChannelParams is the input of join_channel (name, password), leave_channel and channel_list (name).
type serveChannelParams struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
}

type serveChatLogParams struct {
	Since  uint64 `json:"since"`
	Limit  int    `json:"limit,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

type serveGMTextParams struct {
	Text string `json:"text"`
}

type serveNameParams struct {
	Name string `json:"name"`
}

type serveCastParams struct {
	SpellID   int32  `json:"spell_id"`
	Target    GUID   `json:"target,omitempty"`
	TradeSlot *uint8 `json:"trade_slot,omitempty"`
}

type serveCancelCastParams struct {
	SpellID int32 `json:"spell_id,omitempty"`
}

// serveAcceptSummonParams is accept_summon's input: the Accept bit, true when omitted.
type serveAcceptSummonParams struct {
	Accept *bool `json:"accept,omitempty"`
}

type serveWalkParams struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// serveWalkRayParams is the walk_ray method's input: the point to look at and, optionally, the point to look from (default:
// the session's own position).
type serveWalkRayParams struct {
	X    float32     `json:"x"`
	Y    float32     `json:"y"`
	Z    float32     `json:"z"`
	From *[3]float32 `json:"from,omitempty"`
}

type serveAreaTriggerParams struct {
	ID int32 `json:"id"`
}

type serveAttackParams struct {
	GUID  GUID   `json:"guid,omitempty"`
	Name  string `json:"name,omitempty"`
	ForMS int    `json:"for_ms,omitempty"`
}

type serveAddonParams struct {
	Channel string `json:"channel"`
	Prefix  string `json:"prefix"`
	Text    string `json:"text"`
	To      string `json:"to,omitempty"`
}

type serveBotParams struct {
	Line   string `json:"line"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

// serveAssistantParams is the assistant method's input: the member and whether to clear the flag instead of setting
// it.
type serveAssistantParams struct {
	Name  string `json:"name"`
	Clear bool   `json:"clear,omitempty"`
}

// serveRaidMoveParams is the raid_move method's input, the Turtle daemon's own params: the member by guid or by
// name, the target subgroup 1-8 and the member of that subgroup who trades places instead (a full subgroup).
type serveRaidMoveParams struct {
	GUID     GUID   `json:"guid,omitempty"`
	Name     string `json:"name,omitempty"`
	Subgroup int    `json:"subgroup"`
	SwapWith string `json:"swap_with,omitempty"`
}

// serveLootMethodParams is the loot_method method's input: the method's name (or 0..5), the master looter's name for
// master loot (optional) and the threshold quality 2..6 (optional, uncommon).
type serveLootMethodParams struct {
	Method    string `json:"method"`
	Master    string `json:"master,omitempty"`
	Threshold *int   `json:"threshold,omitempty"`
}

// serveMarkParams is the mark method's input: the unit (its guid, or its name) and the icon's name.
type serveMarkParams struct {
	GUID GUID   `json:"guid,omitempty"`
	Name string `json:"name,omitempty"`
	Icon string `json:"icon"`
}

type serveEventsParams struct {
	Since  int `json:"since"`
	Limit  int `json:"limit,omitempty"`
	WaitMS int `json:"wait_ms,omitempty"`
}

type serveEventLogParams struct {
	Since  uint64 `json:"since"`
	Limit  int    `json:"limit,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

// ServeEventPage is event_log's result: the events above since, oldest first, at most the limit; next is the seq to
// pass next time, more says another page follows, and dropped counts the events that fell out of the ring between
// since and the oldest it still holds.
type ServeEventPage struct {
	Events  []Event `json:"events"`
	Next    uint64  `json:"next"`
	More    bool    `json:"more"`
	Dropped int     `json:"dropped"`
	// Epoch names the log's life: a daemon restart is a new log, so a reader that keeps its cursor across one sees
	// the epoch change. Reset says the cursor was above the log's seq (the restart marker): the page starts at the
	// beginning of the new log instead of staying empty until its seq passes the old cursor.
	Epoch string `json:"epoch"`
	Reset bool   `json:"reset,omitempty"`
}

// serveActDefaultBudget is act's deadline when the caller names none and the script does not fly.
var serveActDefaultBudget = 60 * time.Second

// serveActSlack is the time a serve act script's other steps need around its flights: the selects and settles of
// the steps that lead to the flight master and the line the answer keeps.
const serveActSlack = 30 * time.Second

// serveActTimeoutMax is the largest run a caller may name (ten minutes), the method's old cap; a script with a
// flight may name up to its flight budget when that is longer.
const serveActTimeoutMax = 10 * time.Minute

// serveActFlightBudget is the budget a script with flights needs: each fly step at its worst (actFlightStepBudget)
// plus the slack of the steps around it. A flight is driven by the server for up to actTaxiFlightMax, so the
// method's own clock must cover it instead of ending the run while ON_TAXI is still set.
func serveActFlightBudget(steps []ActStep) time.Duration {
	return time.Duration(actScriptFlights(steps))*actFlightStepBudget + serveActSlack
}

type serveActParams struct {
	// Steps are act steps, one per entry, in the `--step 'verb k=v'` form (world.ParseActStep).
	Steps []string `json:"steps"`
	// TimeoutMS bounds the whole run. 0 or absent is the default budget: serveActDefaultBudget, or the flights'
	// own budget when the script has a fly step. It may not be above the default the script earns (serveAct's own
	// check), nor below what its flights need, and it is never "no deadline": a run holds the session goroutine,
	// so every other call waits behind it.
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// ServeActResult is act's result: the lines `forever act` prints (one per step, plus a menu's option lines), and ok,
// true only when every step succeeded.
type ServeActResult struct {
	Lines []string `json:"lines"`
	OK    bool     `json:"ok"`
}

type serveGMSetHPParams struct {
	GUID GUID   `json:"guid"`
	HP   *int64 `json:"hp"`
	// CurrentOnly sends `.modify currenthp <hp>`, which sets the current health and leaves the maximum alone
	// (cs_modify.cpp:152-175). Without it the line is `.modify hp <hp>`, which sets both.
	CurrentOnly bool `json:"current_only,omitempty"`
}

// serveGMCombatLogParams is gm_combatlog's call: the unit to watch through, or none for off, and the mode. A watch
// needs a selected unit that is not the GM; group also needs a grouped player (cs_gm.cpp:195-232).
type serveGMCombatLogParams struct {
	GUID GUID   `json:"guid,omitempty"`
	Name string `json:"name,omitempty"`
	Mode string `json:"mode"`
}

// ServeCombatLogWatch is gm_combatlog's result: the mode asked for, whether the core started a watch, and the core's
// own answer line. Watching is true only when that line is the watch's ("Combat log watch on ...").
type ServeCombatLogWatch struct {
	Mode     string `json:"mode"`
	Watching bool   `json:"watching"`
	Text     string `json:"text"`
}

type serveGMAuraParams struct {
	GUID   GUID  `json:"guid"`
	Spell  int32 `json:"spell"`
	Remove bool  `json:"remove,omitempty"`
}

// serveTrialUnitsParams is trial_units' call: the guids the GM hands may act on for the current trial. It replaces
// the previous list, so an empty list clears the allowlist (fail-closed).
type serveTrialUnitsParams struct {
	GUIDs []GUID `json:"guids"`
}

// ServeTrialUnits is trial_units' result: the allowlist in force after the call, in registration order.
type ServeTrialUnits struct {
	GUIDs []GUID `json:"guids"`
}

// ServeEventsPage is events' result: the combat rows above since, oldest first, and the ring's own bounds. Oldest
// is the lowest seq the ring still holds; Dropped is how many rows fell out of it above since.
type ServeEventsPage struct {
	Events  []CombatRow `json:"events"`
	Next    int         `json:"next"`
	Oldest  int         `json:"oldest"`
	Dropped int         `json:"dropped"`
}

// serveCallTyped posts one typed method to the session goroutine.
func serveCallTyped[P, R any](st *serveState, fn func(*session, context.Context, P) (R, error)) rpc.Method {
	return rpc.Typed(func(ctx context.Context, p P) (R, error) {
		var zero R
		v, err := st.call(ctx, func(s *session) (any, error) { return fn(s, ctx, p) })
		if err != nil {
			return zero, serveError(err)
		}
		out, ok := v.(R)
		if !ok {
			return zero, &rpc.Error{Code: rpc.CodeInternal, Message: "internal: method result type"}
		}
		return out, nil
	})
}

// serveCallTypedFirst is serveCallTyped with a hook that runs on the RPC goroutine before the call is queued. A running
// follow loop holds the session goroutine while it walks, so a method that must end it (follow_stop, teardown) cancels
// it here, not in the queued body: the cancel is what lets the queued body get its turn.
func serveCallTypedFirst[P, R any](st *serveState, first func(P), fn func(*session, context.Context, P) (R, error)) rpc.Method {
	return rpc.Typed(func(ctx context.Context, p P) (R, error) {
		first(p)
		var zero R
		v, err := st.call(ctx, func(s *session) (any, error) { return fn(s, ctx, p) })
		if err != nil {
			return zero, serveError(err)
		}
		out, ok := v.(R)
		if !ok {
			return zero, &rpc.Error{Code: rpc.CodeInternal, Message: "internal: method result type"}
		}
		return out, nil
	})
}

// serveReadTyped is serveCallTyped for a method that still answers after the link dropped (status, chat_log).
func serveReadTyped[P, R any](st *serveState, fn func(*session, context.Context, P) (R, error)) rpc.Method {
	return rpc.Typed(func(ctx context.Context, p P) (R, error) {
		var zero R
		v, err := st.callDead(ctx, func(s *session) (any, error) { return fn(s, ctx, p) })
		if err != nil {
			return zero, serveError(err)
		}
		out, ok := v.(R)
		if !ok {
			return zero, &rpc.Error{Code: rpc.CodeInternal, Message: "internal: method result type"}
		}
		return out, nil
	})
}

// callDead is call for the read-only methods: while the link is dead the call still runs on the loop goroutine, so
// status can show the disconnect without any field being read off it.
func (st *serveState) callDead(ctx context.Context, fn func(*session) (any, error)) (any, error) {
	c := &serveCall{ctx: ctx, fn: fn, out: make(chan serveResult, 1), readOnly: true}
	select {
	case st.calls <- c:
	case <-st.done:
		return nil, &rpc.Error{Code: rpc.CodeDisconnected, Message: "the connection is lost"}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-c.out:
		return r.v, r.err
	case <-st.done:
		return nil, &rpc.Error{Code: rpc.CodeDisconnected, Message: "the connection is lost"}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// registerServeMethods wires the method table.
func registerServeMethods(srv *rpc.Server, st *serveState) {
	srv.Register("status", serveReadTyped(st, func(s *session, _ context.Context, _ serveEmpty) (ServeStatus, error) {
		return s.serveStatus(st), nil
	}))
	srv.Register("inv", serveReadTyped(st, func(s *session, _ context.Context, _ serveEmpty) (ServeInventory, error) {
		return s.serveInventory(), nil
	}))
	srv.Register("units", serveReadTyped(st, func(s *session, _ context.Context, p serveUnitsParams) ([]ServeUnit, error) {
		return s.serveUnits(p)
	}))
	srv.Register("unit", serveCallTyped(st, func(s *session, _ context.Context, p serveUnitParams) (ServeUnit, error) {
		return s.serveOneUnit(p)
	}))
	srv.Register("target", serveCallTyped(st, func(s *session, _ context.Context, p serveUnitParams) (ServeUnit, error) {
		return s.serveTarget(p)
	}))
	srv.Register("chat", serveCallTyped(st, func(s *session, ctx context.Context, p serveChatParams) (serveEmpty, error) {
		return serveEmpty{}, s.serveChat(ctx, p.Type, p.Text, p.To)
	}))
	srv.Register("join_channel", serveCallTyped(st, func(s *session, ctx context.Context, p serveChannelParams) (ServeChannelJoin, error) {
		return s.serveJoinChannel(ctx, p.Name, p.Password)
	}))
	srv.Register("leave_channel", serveCallTyped(st, func(s *session, ctx context.Context, p serveChannelParams) (ServeChannelLeave, error) {
		return s.serveLeaveChannel(ctx, p.Name)
	}))
	srv.Register("channel_list", serveCallTyped(st, func(s *session, ctx context.Context, p serveChannelParams) (ServeChannelList, error) {
		return s.serveChannelList(ctx, p.Name)
	}))
	srv.Register("chat_log", serveReadTyped(st, func(s *session, ctx context.Context, p serveChatLogParams) (ServeChatPage, error) {
		return s.serveChatLog(ctx, p)
	}))
	srv.Register("events", rpc.Typed(func(ctx context.Context, p serveEventsParams) (ServeEventsPage, error) {
		page, err := st.serveEvents(ctx, p)
		if err != nil {
			return ServeEventsPage{}, serveError(err)
		}
		return page, nil
	}))
	srv.Register("event_log", rpc.Typed(func(ctx context.Context, p serveEventLogParams) (ServeEventPage, error) {
		page, err := st.serveEventLog(ctx, p)
		if err != nil {
			return ServeEventPage{}, serveError(err)
		}
		return page, nil
	}))
	srv.Register("act", serveCallTyped(st, func(s *session, ctx context.Context, p serveActParams) (ServeActResult, error) {
		return s.serveAct(ctx, p)
	}))
	srv.Register("mount", serveCallTyped(st, func(s *session, ctx context.Context, p serveMountParams) (ServeMount, error) {
		return s.serveMount(ctx, p)
	}))
	srv.Register("taxi", serveCallTyped(st, func(s *session, ctx context.Context, p serveTaxiParams) (ServeTaxi, error) {
		return s.serveTaxi(ctx, p)
	}))
	srv.Register("dismount", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (ServeMount, error) {
		return s.serveDismount(ctx)
	}))
	srv.Register("follow_start", serveCallTyped(st, func(s *session, _ context.Context, p serveFollowParams) (ServeFollow, error) {
		return st.serveFollowStart(s, p)
	}))
	srv.Register("follow_stop", serveCallTypedFirst(st, func(serveEmpty) { st.cancelFollow() }, func(s *session, ctx context.Context, _ serveEmpty) (ServeFollow, error) {
		return st.serveFollowStop(ctx, s)
	}))
	srv.Register("follow_status", serveReadTyped(st, func(s *session, _ context.Context, _ serveEmpty) (ServeFollow, error) {
		return st.followStatus(s, nil), nil
	}))
	srv.Register("trial_units", serveCallTyped(st, func(_ *session, _ context.Context, p serveTrialUnitsParams) (ServeTrialUnits, error) {
		return st.serveTrialUnits(p)
	}))
	srv.Register("gm", serveCallTyped(st, func(s *session, ctx context.Context, p serveGMTextParams) (ServeCommand, error) {
		if err := s.serveGMUnitLine("gm", p.Text); err != nil {
			return ServeCommand{}, err
		}
		return s.serveGMResult(ctx, p.Text)
	}))
	srv.Register("gm_revive", serveCallTyped(st, func(s *session, ctx context.Context, p serveNameParams) (ServeCommand, error) {
		name, err := chat.NormaliseName(p.Name)
		if err != nil {
			return ServeCommand{}, err
		}
		line := ".revive " + name
		if err := s.serveGMUnitLine("gm_revive", line); err != nil {
			return ServeCommand{}, err
		}
		return s.serveGMResult(ctx, line)
	}))
	registerGapMethods(srv, st) // #99: teleport, gm_evade, gm_respawn
	srv.Register("gm_kill", serveCallTyped(st, func(s *session, ctx context.Context, p serveUnitParams) (ServeCommand, error) {
		return s.serveGMKill(ctx, p)
	}))
	srv.Register("gm_set_hp", serveCallTyped(st, func(s *session, ctx context.Context, p serveGMSetHPParams) (ServeCommand, error) {
		return s.serveGMSetHP(ctx, p)
	}))
	srv.Register("gm_combatlog", serveCallTyped(st, func(s *session, ctx context.Context, p serveGMCombatLogParams) (ServeCombatLogWatch, error) {
		return s.serveGMCombatLog(ctx, p)
	}))
	srv.Register("gm_aura", serveCallTyped(st, func(s *session, ctx context.Context, p serveGMAuraParams) (ServeCommand, error) {
		return s.serveGMAura(ctx, p)
	}))
	srv.Register("bot", serveCallTyped(st, func(s *session, ctx context.Context, p serveBotParams) (ServeCommand, error) {
		rep, err := s.serveBot(ctx, p.Line, time.Duration(p.WaitMS)*time.Millisecond)
		if err != nil {
			return ServeCommand{}, err
		}
		return commandResult(p.Line, rep), nil
	}))
	srv.Register("commander", serveCallTyped(st, func(s *session, ctx context.Context, p serveCommanderParams) (ServeCommander, error) {
		return s.serveCommander(ctx, st, p)
	}))
	srv.Register("party", serveCallTyped(st, func(s *session, _ context.Context, _ serveEmpty) (ServePartyInfo, error) {
		return s.servePartyInfo(), nil
	}))
	srv.Register("invite", serveCallTyped(st, func(s *session, ctx context.Context, p serveNameParams) (serveEmpty, error) {
		return serveEmpty{}, s.serveInvite(ctx, p.Name)
	}))
	srv.Register("accept_invite", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (ServeAcceptInviteResult, error) {
		return s.serveAcceptInvite(ctx)
	}))
	srv.Register("leave_group", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.serveLeaveGroup(ctx)
	}))
	// The guild invite flow (#113): each hand sends its packet and answers only once the state it changes is read
	// back. The decline's answer is the core's silence, so it has no wait.
	srv.Register("accept_guild_invite", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.serveAcceptGuildInvite(ctx)
	}))
	srv.Register("decline_guild_invite", serveCallTyped(st, func(s *session, _ context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.serveDeclineGuildInvite()
	}))
	srv.Register("guild_leave", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.serveGuildLeave(ctx)
	}))
	// Group control (#95 G2): each sends its packet and answers only once the readback confirms it.
	srv.Register("promote", serveCallTyped(st, func(s *session, ctx context.Context, p serveNameParams) (serveEmpty, error) {
		return serveEmpty{}, s.groupPromote(ctx, p.Name)
	}))
	srv.Register("assistant", serveCallTyped(st, func(s *session, ctx context.Context, p serveAssistantParams) (serveEmpty, error) {
		return serveEmpty{}, s.groupAssistant(ctx, p.Name, !p.Clear)
	}))
	// The raid ranks (#108 R2): the leader converts, the leader or an assistant moves, and both wait for the roster.
	srv.Register("convert_raid", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.convertRaid(ctx)
	}))
	srv.Register("raid_move", serveCallTyped(st, func(s *session, ctx context.Context, p serveRaidMoveParams) (serveEmpty, error) {
		return serveEmpty{}, s.raidMove(ctx, wire.GUID(p.GUID), p.Name, p.Subgroup, p.SwapWith)
	}))
	srv.Register("loot_method", serveCallTyped(st, func(s *session, ctx context.Context, p serveLootMethodParams) (serveEmpty, error) {
		method, ok := ParseLootMethod(p.Method)
		if !ok {
			return serveEmpty{}, fmt.Errorf("loot_method: %q is not a loot method", p.Method)
		}
		threshold := 2 // uncommon, the player run's setting
		if p.Threshold != nil {
			threshold = *p.Threshold
		}
		return serveEmpty{}, s.groupLootMethod(ctx, method, p.Master, threshold)
	}))
	srv.Register("mark", serveCallTyped(st, func(s *session, ctx context.Context, p serveMarkParams) (ServeMark, error) {
		mark, ok := party.ParseMarkName(p.Icon)
		if !ok {
			return ServeMark{}, fmt.Errorf("mark: %q is not a raid target icon (star circle diamond triangle moon square cross skull)", p.Icon)
		}
		target := wire.GUID(p.GUID)
		if target.IsZero() {
			if err := s.groupRoster("mark"); err != nil {
				return ServeMark{}, err
			}
			m, err := s.groupMember("mark", p.Name)
			if err != nil {
				return ServeMark{}, err
			}
			target = m.GUID
		}
		if err := s.groupMark(ctx, target, mark); err != nil {
			return ServeMark{}, err
		}
		return ServeMark{Icon: party.MarkName(mark), GUID: GUID(target)}, nil
	}))
	srv.Register("marks", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (ServeMarks, error) {
		icons, err := s.groupMarks(ctx)
		if err != nil {
			return ServeMarks{}, err
		}
		return ServeMarks{Marks: serveMarks(icons)}, nil
	}))
	srv.Register("disband", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.groupDisband(ctx)
	}))
	srv.Register("attack", serveCallTyped(st, func(s *session, ctx context.Context, p serveAttackParams) (ServeAttack, error) {
		return s.serveAttack(ctx, p)
	}))
	srv.Register("stop_attack", rpc.Typed(func(ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		if err := st.actionError(); err != nil {
			return serveEmpty{}, err
		}
		if handled, err := st.stopFightFast(); handled {
			return serveEmpty{}, err
		}
		_, err := st.call(ctx, func(s *session) (any, error) {
			return serveEmpty{}, s.Send(wire.CMSGAttackStop, nil)
		})
		if err != nil {
			return serveEmpty{}, serveError(err)
		}
		return serveEmpty{}, nil
	}))
	srv.Register("cast", serveCallTyped(st, func(s *session, ctx context.Context, p serveCastParams) (serveEmpty, error) {
		if p.TradeSlot != nil {
			outcome, err := s.castTradeSlot(ctx, p.SpellID, *p.TradeSlot)
			if err != nil {
				return serveEmpty{}, err
			}
			if outcome != "success" {
				return serveEmpty{}, fmt.Errorf("cast: the server refused the spell: %s", outcome)
			}
			return serveEmpty{}, nil
		}
		if p.SpellID <= 0 {
			return serveEmpty{}, errors.New("cast needs a spell id")
		}
		target := wire.GUID(p.Target)
		if target.IsZero() && s.serveRT != nil {
			target = s.serveRT.target
		}
		outcome, err := s.Cast(ctx, p.SpellID, target, RangeWindow{})
		if err != nil {
			return serveEmpty{}, err
		}
		if outcome != "success" {
			return serveEmpty{}, fmt.Errorf("cast: the server refused the spell: %s", outcome)
		}
		return serveEmpty{}, nil
	}))
	srv.Register("cancel_cast", serveCallTyped(st, func(s *session, _ context.Context, p serveCancelCastParams) (serveEmpty, error) {
		self := s.selfOrMover()
		cs, ok := s.world.Combat(self)
		if !ok || cs.Cast == nil {
			return serveEmpty{}, errors.New("no cast in progress")
		}
		spell := cs.Cast.SpellID
		if p.SpellID > 0 {
			spell = p.SpellID
		}
		return serveEmpty{}, s.CancelCast(spell, cs.Cast.CastID)
	}))
	srv.Register("cast_state", serveCallTyped(st, func(s *session, _ context.Context, _ serveEmpty) (ServeCastState, error) {
		return s.serveCastState(), nil
	}))
	srv.Register("spells", serveReadTyped(st, func(s *session, _ context.Context, p serveSpellsParams) ([]ServeSpellRow, error) {
		return s.serveSpells(p)
	}))
	srv.Register("dynamic_objects", serveReadTyped(st, func(s *session, _ context.Context, _ serveEmpty) ([]ServeDynamicObject, error) {
		return s.serveDynamicObjects()
	}))
	srv.Register("map_snapshot", serveReadTyped(st, func(s *session, _ context.Context, p ServeMapParams) (ServeMapResult, error) {
		return s.serveMapSnapshot(p, st.plansFolder())
	}))
	srv.Register("face", serveCallTyped(st, func(s *session, _ context.Context, p serveUnitParams) (serveEmpty, error) {
		o, err := s.serveUnitByName(p.GUID, p.Name)
		if err != nil {
			return serveEmpty{}, err
		}
		return serveEmpty{}, s.serveFace(o)
	}))
	srv.Register("walk_to", serveCallTyped(st, func(s *session, ctx context.Context, p serveWalkParams) (serveEmpty, error) {
		if s.mover == nil {
			return serveEmpty{}, errors.New("world: no mover (the character's create block never arrived)")
		}
		return serveEmpty{}, s.mover.MoveTo(ctx, p.X, p.Y, p.Z)
	}))
	srv.Register("path_to", serveCallTyped(st, func(s *session, _ context.Context, p serveWalkParams) (ServePath, error) {
		return s.servePath(p.X, p.Y, p.Z)
	}))
	srv.Register("walk_ray", serveCallTyped(st, func(s *session, _ context.Context, p serveWalkRayParams) (ServeWalkRay, error) {
		return s.serveWalkRay(p)
	}))
	srv.Register("walk_path", serveCallTyped(st, func(s *session, ctx context.Context, p serveWalkParams) (ServePath, error) {
		return s.serveWalkPath(ctx, p.X, p.Y, p.Z)
	}))
	srv.Register("area_trigger", serveCallTyped(st, func(s *session, _ context.Context, p serveAreaTriggerParams) (serveEmpty, error) {
		if p.ID <= 0 {
			return serveEmpty{}, errors.New("area_trigger: needs a trigger id")
		}
		return serveEmpty{}, s.EnterAreaTrigger(p.ID)
	}))
	registerTriggerWatch(srv, st)
	srv.Register("death_status", serveReadTyped(st, func(s *session, _ context.Context, _ serveEmpty) (ServeDeath, error) {
		return s.serveDeath(), nil
	}))
	srv.Register("accept_resurrect", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (ServeDeath, error) {
		if err := s.AcceptResurrect(ctx); err != nil {
			return ServeDeath{}, err
		}
		return s.serveDeath(), nil
	}))
	srv.Register("accept_summon", serveCallTyped(st, func(s *session, _ context.Context, p serveAcceptSummonParams) (ServeAcceptSummon, error) {
		accept := true
		if p.Accept != nil {
			accept = *p.Accept
		}
		g, answer, err := s.AcceptSummon(accept)
		if err != nil {
			return ServeAcceptSummon{}, err
		}
		return ServeAcceptSummon{Summoner: GUID(g), Accepted: answer}, nil
	}))
	srv.Register("release_spirit", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.Release(ctx)
	}))
	srv.Register("corpse_query", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (ServeCorpse, error) {
		p, err := s.CorpseLocation(ctx)
		if err != nil {
			return ServeCorpse{}, err
		}
		return ServeCorpse{Found: true, Map: p.MapID, X: p.X, Y: p.Y, Z: p.Z}, nil
	}))
	srv.Register("reclaim_corpse", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.ReclaimCorpse(ctx)
	}))
	srv.Register("addon_send", serveCallTyped(st, func(s *session, ctx context.Context, p serveAddonParams) (serveEmpty, error) {
		return serveEmpty{}, s.serveAddon(ctx, p.Channel, p.Prefix, p.Text, p.To)
	}))
	srv.Register("logout", serveCallTyped(st, func(s *session, _ context.Context, _ serveEmpty) (serveLogoutResult, error) {
		if err := s.serveLogout(st); err != nil {
			return serveLogoutResult{}, err
		}
		st.stopped = true
		return serveLogoutResult{Outcome: "complete"}, nil
	}))
	srv.Register("teardown", serveCallTypedFirst(st, func(p serveTeardownParams) {
		// A real teardown (not a dry run, with a gm value it will accept, on a link that is up) ends the follow loop
		// before it queues, so a walk in flight cannot hold the logout behind it.
		if p.DryRun || !serveTeardownGMValid(p.GM) || st.actionError() != nil {
			return
		}
		st.cancelFollow()
	}, func(s *session, ctx context.Context, p serveTeardownParams) (ServeTeardown, error) {
		return s.serveTeardown(ctx, st, p)
	}))
	registerItemInfoMethods(srv, st)
	registerCombatNearMethods(srv, st)
	registerItemMethods(srv, st)
	registerLootMethods(srv, st)
	registerNPCMethods(srv, st)
	registerQuestMethods(srv, st)
	registerPetMethods(srv, st)
	registerTrainerMethods(srv, st)
	registerMailMethods(srv, st)
	registerTradeMethods(srv, st)
	registerAuctionMethods(srv, st)
	registerBankMethods(srv, st)
	registerAddonHostMethods(srv, st)

	registerDungeonMethods(srv, st) // #216: dungeon_run/status/stop/pause/resume and plan_check
	registerBotViewMethods(srv, st) // #216: bot_view
	registerTBMMethods(srv, st)     // #216: tbm, tbm_handshake, tbm_send
	// #216: the old names this server refuses answer -32003 with their
	// documented reason (serve_rpc_methods.go); no name is left "not ported".
	registerRefusedMethods(srv)
}

func (s *session) serveUnits(p serveUnitsParams) ([]ServeUnit, error) {
	within := p.Within
	if within == 0 {
		within = p.Near
	}
	limit := p.Limit
	if limit < 0 {
		return nil, errors.New("units: limit must not be negative")
	}
	if limit == 0 {
		limit = 100
	}
	// The hostile filter is a real lookup (CORE-GAINS-3): the mob's faction
	// template against the character's own, fetched once and cached. Only
	// callers that pass the filter pay the fetch.
	hostile := map[int32]bool{}
	if p.Hostile != nil {
		for _, o := range s.world.Units(s.MoverX(), s.MoverY(), s.MoverZ()) {
			id := o.Info.FactionTemplate
			if _, done := hostile[id]; done {
				continue
			}
			h, err := s.hostileToSelf(id)
			if err != nil {
				return nil, err
			}
			hostile[id] = h
		}
	}
	now := s.Now()
	pattern := strings.ToLower(p.Name)
	out := make([]ServeUnit, 0, limit)
	for _, o := range s.world.Units(s.MoverX(), s.MoverY(), s.MoverZ()) {
		u := s.serveUnit(o, now)
		if within > 0 && u.Distance > within {
			continue
		}
		if p.Entry != 0 && u.Entry != p.Entry {
			continue
		}
		if pattern != "" && !strings.Contains(strings.ToLower(u.Name), pattern) {
			continue
		}
		if p.Hostile != nil {
			h, ok := hostile[o.Info.FactionTemplate]
			if !ok || h != *p.Hostile {
				continue
			}
		}
		if !wire.GUID(p.Owner).IsZero() && u.Owner != p.Owner {
			continue
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		a, b := wire.GUID(out[i].GUID), wire.GUID(out[j].GUID)
		if a.Low != b.Low {
			return a.Low < b.Low
		}
		return a.High < b.High
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *session) serveOneUnit(p serveUnitParams) (ServeUnit, error) {
	o, err := s.serveUnitByName(p.GUID, p.Name)
	if err != nil {
		return ServeUnit{}, err
	}
	return s.serveUnit(o, s.Now()), nil
}

func (s *session) serveTarget(p serveUnitParams) (ServeUnit, error) {
	o, err := s.serveUnitByName(p.GUID, p.Name)
	if err != nil {
		return ServeUnit{}, err
	}
	if err := s.serveSelect(o); err != nil {
		return ServeUnit{}, err
	}
	return s.serveUnit(o, s.Now()), nil
}

func (s *session) serveAttack(ctx context.Context, p serveAttackParams) (ServeAttack, error) {
	var target wire.GUID
	if !wire.GUID(p.GUID).IsZero() || p.Name != "" {
		o, err := s.serveUnitByName(p.GUID, p.Name)
		if err != nil {
			return ServeAttack{}, err
		}
		if err := s.serveSelect(o); err != nil {
			return ServeAttack{}, err
		}
		target = o.GUID
	} else if s.serveRT != nil {
		target = s.serveRT.target
	}
	if target.IsZero() {
		return ServeAttack{}, errors.New("no target selected; call target first")
	}
	if p.ForMS < 0 || p.ForMS > 600000 {
		return ServeAttack{}, errors.New("attack: for_ms must be 0 to 600000")
	}
	d := time.Duration(p.ForMS) * time.Millisecond
	if d <= 0 {
		// Under the rpc client's default 30 s call timeout: a default call gets its own answer rather than a
		// cancellation that stops the fight.
		d = 25 * time.Second
	}
	start := s.Now()
	s.serveRT.fighting.Store(true)
	defer s.serveRT.fighting.Store(false)
	err := s.attackFor(ctx, target, d)
	out := ServeAttack{Target: GUID(target), Duration: s.Now().Sub(start).Milliseconds()}
	if err != nil {
		if errors.Is(err, ErrAttackStopped) {
			out.Reason = "stopped"
			return out, nil
		}
		if errors.Is(err, ErrMapChange) {
			out.Reason = "map change"
			return out, nil
		}
		return ServeAttack{}, err
	}
	out.Reason = "dead"
	return out, nil
}

// attackFor is Attack with a per-call time budget.
func (s *session) attackFor(ctx context.Context, guid wire.GUID, d time.Duration) error {
	old := s.opts.FightFor
	s.opts.FightFor = d
	defer func() { s.opts.FightFor = old }()
	return s.Attack(ctx, guid)
}

func (s *session) serveGMResult(ctx context.Context, text string) (ServeCommand, error) {
	out, err := s.serveGMLine(ctx, text)
	if err != nil {
		return ServeCommand{}, err
	}
	// A `.go xyz` above the navmesh's ground starts the fall (fall.go): the command line is done when the character
	// has landed, so the call that follows does not meet a mover that is still in the air. teleport does not use this
	// form: its answer reports the fall in flight instead, so status can see it (the flight runs on every wait).
	if err := s.waitFallLanded(ctx); err != nil {
		return ServeCommand{}, err
	}
	return out, nil
}

// serveGMLine runs one line through the GM path and answers what the server said, without waiting for a fall the line
// may have started.
func (s *session) serveGMLine(ctx context.Context, text string) (ServeCommand, error) {
	rep, err := s.serveGM(ctx, text)
	if err != nil {
		return ServeCommand{}, err
	}
	return commandResult(strings.TrimSpace(text), rep), nil
}

// serveTeardownGM is teardown's gm value, lower-cased, with the default.
func serveTeardownGM(raw string) string {
	gm := strings.ToLower(strings.TrimSpace(raw))
	if gm == "" {
		gm = "off"
	}
	return gm
}

// serveTeardownGMValid says whether teardown accepts the gm value.
func serveTeardownGMValid(raw string) bool {
	switch serveTeardownGM(raw) {
	case "off", "on", "keep":
		return true
	}
	return false
}

// serveTeardown ends the session the way a real client does, step by step, and answers the steps before the daemon
// stops: the loop ends after this call returns. The GM line (`.gm off` by default, `.gm on`, or none for "keep")
// goes through the same guard as every other command line, and the logout is the full protocol logout. A refused GM
// line is a failed step and the logout still runs, as the old daemon's teardown does. A dry run plans the steps,
// sends nothing and leaves the daemon serving.
func (s *session) serveTeardown(ctx context.Context, st *serveState, p serveTeardownParams) (ServeTeardown, error) {
	gm := serveTeardownGM(p.GM)
	if !serveTeardownGMValid(p.GM) {
		return ServeTeardown{}, fmt.Errorf("teardown: gm must be off, on or keep, not %q", p.GM)
	}
	start := s.Now()
	out := ServeTeardown{Character: s.report.Character.Name, Steps: []ServeTeardownStep{}, DryRun: p.DryRun}
	add := func(step, status, detail string) {
		out.Steps = append(out.Steps, ServeTeardownStep{Step: step, Status: status, Detail: detail})
		if status == "fail" || status == "ending" {
			out.Failed++
		}
	}
	finish := func() ServeTeardown {
		out.Seconds = s.Now().Sub(start).Seconds()
		return out
	}

	// The follow loop walks the character. It is stopped before the GM line and the logout, as the old daemon's
	// teardown does, so no movement of ours can go out while the logout is in flight.
	st.mu.Lock()
	follow := st.follow
	st.mu.Unlock()
	switch {
	case follow == nil || followEndedOnItsOwn(st, follow):
		add("follow", "skip", "no follow loop")
	case p.DryRun:
		add("follow", "plan", "would stop the follow loop ("+follow.lead+")")
	default:
		follow.cancel()
		detail, err := st.followStopResult(ctx, follow)
		if err != nil {
			add("follow", "fail", err.Error())
		} else {
			add("follow", "ok", detail)
		}
	}

	// A loot window a method left open is given back before the GM line and the logout; a character that logs out
	// with a window open leaves the corpse locked to it until the server's own timeout. Nothing is added to the
	// steps when no window is held.
	switch {
	case s.methodLoot == nil:
	case p.DryRun:
		add("loot", "plan", "would release the held loot window")
	default:
		if detail, ok := s.serveReleaseHeldLoot(); ok {
			add("loot", "ok", detail)
		} else {
			add("loot", "fail", detail)
		}
	}

	if gm == "keep" {
		add("gm", "skip", "left as it is")
	} else if p.DryRun {
		add("gm", "plan", "would send .gm "+gm)
	} else {
		line := ".gm " + gm
		// The line goes through the guard the raw gm method uses, so no teardown path can do what a refused gm line
		// cannot. `.gm` itself is not a unit command, so the guard passes it untouched.
		err := s.serveGMUnitLine("teardown", line)
		var rep *command.Reply
		if err == nil {
			rep, err = s.serveGM(ctx, line)
		}
		switch {
		case err != nil:
			add("gm", "fail", err.Error())
		case rep == nil:
			add("gm", "fail", "no answer to "+line)
		default:
			detail := line
			if len(rep.Raw) > 0 {
				detail = rep.Raw[len(rep.Raw)-1]
			}
			add("gm", "ok", detail)
		}
	}

	if p.DryRun {
		add("logout", "plan", "would log out")
		return finish(), nil
	}
	if err := s.serveLogout(st); err != nil {
		add("logout", "fail", err.Error())
		return finish(), nil
	}
	add("logout", "ok", "logged out")
	st.stopped = true
	return finish(), nil
}

// serveGMSelectWait bounds how long a GM hand waits for the server to echo the selection.
const serveGMSelectWait = time.Second

// serveSelectConfirmed selects a unit and waits until this client's own model shows it in the character's target
// field. The core applies a selection on the map thread and a GM line on the world thread, so a line sent right
// behind a select can act on the unit the server had before; the field is the server's own echo of the selection.
func (s *session) serveSelectConfirmed(ctx context.Context, o *objects.Object, wait time.Duration) error {
	if err := s.serveSelect(o); err != nil {
		return err
	}
	deadline := s.Now().Add(wait)
	self := s.selfOrMover()
	for {
		if cur, ok := s.world.Object(self); ok && cur.Info.Target == o.GUID {
			return nil
		}
		if !s.Now().Before(deadline) {
			return errors.New("selection not confirmed")
		}
		if err := s.collect(ctx, 20*time.Millisecond); err != nil {
			return err
		}
	}
}

// serveGMUnit is the GM hands' boundary, checked before a selection is sent or a line composed. A hand acts on one
// unit only when the daemon was started as a test bench (--allow-gm-units), the unit is on the trial allowlist the
// runner registered through trial_units, and, when it is a player, it is this session's own character. Every refusal
// is a typed rpc.Error, and it is fail-closed: a daemon without the flag, an empty allowlist, or an allowlist the
// runner has not (re)registered yet all refuse. The `.revive` line has its own boundary (serveGMReviveUnit): it
// raises players only, so the allowlist is not what protects them there.
func (s *session) serveGMUnit(o *objects.Object, method string) error {
	if err := s.serveGMUnitGate(method); err != nil {
		return err
	}
	rt := s.serveRT
	if o.Type == objects.TypePlayer || o.Type == objects.TypeActivePlayer {
		if o.GUID != s.selfOrMover() {
			return serveGMRefused(method, fmt.Sprintf("%s is a player other than this session's character", o.GUID))
		}
	}
	if _, ok := rt.st.gmUnits[o.GUID]; !ok {
		return serveGMRefused(method, fmt.Sprintf("%s is not registered for the trial", o.GUID))
	}
	return nil
}

// serveGMUnitGate is the outer half of serveGMUnit: the daemon must be serving and must have been started as a test
// bench. The raw gm path uses it for a line that names another player, where there is no unit object to resolve.
func (s *session) serveGMUnitGate(method string) error {
	rt := s.serveRT
	if rt == nil || rt.st == nil {
		return serveGMRefused(method, "the session is not serving")
	}
	if !rt.st.allowGMUnits {
		return serveGMRefused(method, "serve was started without --allow-gm-units")
	}
	return nil
}

// serveGMRefused is one GM hand's typed refusal: the call changed nothing.
func serveGMRefused(method, why string) error {
	return &rpc.Error{Code: rpc.CodeForbidden, Message: method + ": " + why}
}

// serveGMHandClass is what one raw gm line's command acts on, classified before the line is sent.
type serveGMHandClass uint8

const (
	serveGMHandNone     serveGMHandClass = iota // not a command that acts on a unit
	serveGMHandSelected                         // the selected unit (the character itself when nothing is selected)
	serveGMHandSelf                             // the ".aura self"/".unaura self" form
	serveGMHandNamed                            // a player the line names (".revive <name>")
	serveGMHandRevive                           // the bare ".revive" form, which the core applies to the selected player
)

// serveGMHandCommand classifies one raw gm text. It is the core's own command table that decides which lines act on
// a unit's life, health, power, auras or damage: `.die` (Chat.cpp:885), `.damage` (:939), `.aura`/`.unaura`
// (:874-875), `.revive` (:887) and the `.modify` subcommands `hp`, `mana`, `rage` and `energy` (:360-365; the
// ForeverCore table adds the generic `power` and `runicpower`, and its `currenthp` — cs_modify.cpp:73,152-175 — all
// of which this 1.12 client can still be pointed at).
// `.kill` is not in the 1.12 table but is kept as the fatal alias the trials tooling uses. Matching is the core's
// own:
// case-insensitive, and a typed token matches a command name when the name starts with it (hasStringAbbr, so
// `.mod`, `.dam` and `.revi` are the same commands). The text is one command, never a chain: only its first token
// names the command and, for `.modify`, its second names the subcommand; everything after is an argument, which is
// how the core parses it too. The text is classified the way serveGM normalises it: a missing `.`/`!` is implied.
func serveGMHandCommand(text string) (serveGMHandClass, string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return serveGMHandNone, ""
	}
	if t[0] == '.' || t[0] == '!' {
		t = t[1:]
	}
	// ParseCommands runs a command only when the mark is directly followed by its name: "..die" is chat, and so is
	// ". die" (the delimiter directly after the mark).
	if t == "" || t[0] == '.' || t[0] == '!' || t[0] == ' ' || t[0] == '\t' {
		return serveGMHandNone, ""
	}
	fields := strings.Fields(t)
	if len(fields) == 0 {
		return serveGMHandNone, ""
	}
	cmd := strings.ToLower(fields[0])
	switch {
	case serveGMNameIs(cmd, "die", "kill", "damage"):
		return serveGMHandSelected, ""
	case serveGMNameIs(cmd, "aura", "unaura"):
		if len(fields) > 1 && strings.HasPrefix("self", strings.ToLower(fields[1])) {
			return serveGMHandSelf, ""
		}
		return serveGMHandSelected, ""
	case serveGMNameIs(cmd, "modify"):
		// `.modify` alone is the subcommand container, which has no invoker of its own: nothing runs.
		if len(fields) < 2 {
			return serveGMHandNone, ""
		}
		if serveGMNameIs(strings.ToLower(fields[1]), "hp", "currenthp", "mana", "rage", "energy", "power", "runicpower") {
			return serveGMHandSelected, ""
		}
		return serveGMHandNone, ""
	case serveGMNameIs(cmd, "revive"):
		if len(fields) > 1 {
			return serveGMHandNamed, fields[1]
		}
		// The bare form raises the selected player, or the character itself when the selection is not a player
		// (Chat::getSelectedPlayerOrSelf), so it has a boundary of its own.
		return serveGMHandRevive, ""
	}
	return serveGMHandNone, ""
}

// serveGMNameIs reports whether typed would run one of names: the core matches a command when its full name starts
// with the typed token (hasStringAbbr), so a token is never a match for a name shorter than itself.
func serveGMNameIs(typed string, names ...string) bool {
	for _, name := range names {
		if strings.HasPrefix(name, typed) {
			return true
		}
	}
	return false
}

// serveGMUnitLine is the raw gm method's boundary: a line the core would run as one of the unit commands above is
// checked against the same boundary as the typed hands (serveGMUnit) before it is sent, so the raw path cannot do
// what gm_kill, gm_set_hp or gm_aura refuse. The one exception is `.revive`, which raises players and is held to
// its own boundary (serveGMReviveUnit): the session's own character and the trial units the runner registered. A
// refusal is the typed -32006 and sends nothing, the GM guard's probe included. A line that is not a unit command
// passes untouched.
//
// The selected-target lines act on the unit the server has selected. Two selections can be in play for one line:
// the one this session last sent and the one the server's own model last echoed, because the core applies a
// selection on the map thread and a GM line on the world thread. Both are checked, so a selection still in flight
// cannot open the gate for the unit it replaces; with neither set, the lines act on the character itself.
func (s *session) serveGMUnitLine(method, text string) error {
	class, name := serveGMHandCommand(text)
	switch class {
	case serveGMHandNone:
		return nil
	case serveGMHandNamed:
		if err := s.serveGMUnitGate(method); err != nil {
			return err
		}
		return s.serveGMReviveNamed(name, method)
	case serveGMHandSelf:
		self, ok := s.world.Object(s.selfOrMover())
		if !ok {
			return serveGMRefused(method, "the character's own unit is not in view, so the target cannot be checked")
		}
		return s.serveGMUnit(self, method)
	case serveGMHandRevive:
		units, err := s.serveGMSelectedUnits(method)
		if err != nil {
			return err
		}
		// The core's getSelectedPlayerOrSelf skips a selection that is not a player, so only players are checked;
		// with no player selected the character itself is the target, which its own rule allows.
		for _, o := range units {
			if o.Type != objects.TypePlayer && o.Type != objects.TypeActivePlayer {
				continue
			}
			if err := s.serveGMReviveUnit(o, method); err != nil {
				return err
			}
		}
		return nil
	}
	units, err := s.serveGMSelectedUnits(method)
	if err != nil {
		return err
	}
	for _, o := range units {
		if err := s.serveGMUnit(o, method); err != nil {
			return err
		}
	}
	return nil
}

// serveGMReviveNamed resolves the name a `.revive <name>` line carries. This session's own character is matched by
// the login report's name first, because the model may not have learned it yet (nothing has asked for it), then by
// the world's name; any other name is looked up in the model, so a trial bot the runner registered is found. A name
// the model does not hold is refused without sending the line.
func (s *session) serveGMReviveNamed(name, method string) error {
	name = strings.TrimSpace(strings.Trim(name, "\"'"))
	self, haveSelf := s.world.Object(s.selfOrMover())
	isSelf := name != "" && (strings.EqualFold(name, s.report.Character.Name) || (haveSelf && strings.EqualFold(name, s.world.Name(self))))
	if isSelf {
		if !haveSelf {
			return serveGMRefused(method, "the character's own unit is not in view, so the target cannot be checked")
		}
		return s.serveGMReviveUnit(self, method)
	}
	o, err := s.serveUnitByName(GUID{}, name)
	if err != nil {
		return serveGMRefused(method, fmt.Sprintf("the line names a player other than this session's character (%s)", chat.SanitiseName(name)))
	}
	return s.serveGMReviveUnit(o, method)
}

// serveGMReviveUnit is the `.revive` line's own boundary. A revive raises a player, so it cannot damage a unit the
// allowlist guards, but the line must still not raise a player this session has nothing to do with. The session's
// own character passes with the bench flag alone: a bench whose character died must be able to raise it before the
// runner has registered anything, which is what the live trials needed. A unit the runner registered through
// trial_units passes too, which is how a trial's own bots (its scope) are raised. Any other player is refused.
//
// The line is sent by the same serveGMResult as every other GM unit hand, so the core's own GM log carries the same
// audit line: nothing here bypasses the command channel.
func (s *session) serveGMReviveUnit(o *objects.Object, method string) error {
	if err := s.serveGMUnitGate(method); err != nil {
		return err
	}
	if o.Type != objects.TypePlayer && o.Type != objects.TypeActivePlayer {
		return serveGMRefused(method, fmt.Sprintf("%s is not a player; .revive raises players only", o.GUID))
	}
	if o.GUID == s.selfOrMover() {
		return nil
	}
	if _, ok := s.serveRT.st.gmUnits[o.GUID]; ok {
		return nil
	}
	return serveGMRefused(method, fmt.Sprintf("%s is a player other than this session's character", o.GUID))
}

// serveGMSelectedUnits resolves every unit a selected-target line can act on: the selection this session last sent
// (serveRT.target) and the one the server last echoed in the character's target field, deduplicated; the character
// itself when neither is set. A selection the model no longer holds is refused rather than assumed harmless.
func (s *session) serveGMSelectedUnits(method string) ([]*objects.Object, error) {
	selfGUID := s.selfOrMover()
	self, haveSelf := s.world.Object(selfGUID)
	seen := make(map[wire.GUID]struct{}, 2)
	var guids []wire.GUID
	add := func(g wire.GUID) {
		if g.IsZero() {
			return
		}
		if _, dup := seen[g]; dup {
			return
		}
		seen[g] = struct{}{}
		guids = append(guids, g)
	}
	if s.serveRT != nil {
		add(s.serveRT.target)
	}
	if haveSelf {
		add(self.Info.Target)
	}
	if len(guids) == 0 {
		add(selfGUID)
	}
	out := make([]*objects.Object, 0, len(guids))
	for _, g := range guids {
		o, ok := s.world.Object(g)
		if !ok {
			return nil, serveGMRefused(method, fmt.Sprintf("%s is selected but not in view", g))
		}
		out = append(out, o)
	}
	if len(out) == 0 {
		return nil, serveGMRefused(method, "the character's own unit is not in view, so the target cannot be checked")
	}
	return out, nil
}

// serveTrialUnits replaces the GM hands' allowlist with the guids the runner registered for the current trial. It is
// refused without --allow-gm-units, so a daemon that is not a test bench cannot be given one.
func (st *serveState) serveTrialUnits(p serveTrialUnitsParams) (ServeTrialUnits, error) {
	if !st.allowGMUnits {
		return ServeTrialUnits{}, serveGMRefused("trial_units", "serve was started without --allow-gm-units")
	}
	set := make(map[wire.GUID]struct{}, len(p.GUIDs))
	out := ServeTrialUnits{GUIDs: make([]GUID, 0, len(p.GUIDs))}
	for _, g := range p.GUIDs {
		if wire.GUID(g).IsZero() {
			return ServeTrialUnits{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "trial_units: a zero guid cannot be registered"}
		}
		if _, dup := set[wire.GUID(g)]; dup {
			continue
		}
		set[wire.GUID(g)] = struct{}{}
		out.GUIDs = append(out.GUIDs, g)
	}
	st.gmUnits = set
	return out, nil
}

// serveGMKill kills one unit with the core's own `.die`, which acts on the selected unit. A player is refused: that
// line kills a character (a bot of the pool included), and nothing in the trials kills a player this way.
func (s *session) serveGMKill(ctx context.Context, p serveUnitParams) (ServeCommand, error) {
	o, err := s.serveUnitByName(p.GUID, p.Name)
	if err != nil {
		return ServeCommand{}, fmt.Errorf("gm_kill: %w", err)
	}
	if err := s.serveGMUnit(o, "gm_kill"); err != nil {
		return ServeCommand{}, err
	}
	if o.Type == objects.TypePlayer || o.Type == objects.TypeActivePlayer {
		return ServeCommand{}, fmt.Errorf("gm_kill: %s is a player; only a creature or a pet is killed", o.GUID)
	}
	if err := s.serveSelectConfirmed(ctx, o, serveGMSelectWait); err != nil {
		return ServeCommand{}, fmt.Errorf("gm_kill: %w", err)
	}
	return s.serveGMResult(ctx, ".die")
}

// serveGMCombatLog starts or stops the core's GM combat-log watch (`.gm combatlog [group|off]`, cs_gm.cpp:195-232).
// unit and group select the unit first, as every unit hand does; off needs no unit. The result's Watching is the
// core's own verdict: only "Combat log watch on ..." is a watch, everything else is an error carrying the reply. A
// core older than 0.1.38 answers "no such command", which is the unsupported-core code the callers branch on.
func (s *session) serveGMCombatLog(ctx context.Context, p serveGMCombatLogParams) (ServeCombatLogWatch, error) {
	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	out := ServeCombatLogWatch{Mode: mode}
	line := ""
	switch mode {
	case "unit", "group":
		line = ".gm combatlog"
		if mode == "group" {
			line = ".gm combatlog group"
		}
	case "off":
		line = ".gm combatlog off"
	default:
		return ServeCombatLogWatch{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "gm_combatlog: mode must be unit, group or off"}
	}
	if mode != "off" {
		o, err := s.serveUnitByName(p.GUID, p.Name)
		if err != nil {
			return ServeCombatLogWatch{}, fmt.Errorf("gm_combatlog: %w", err)
		}
		if err := s.serveSelectConfirmed(ctx, o, serveGMSelectWait); err != nil {
			return ServeCombatLogWatch{}, fmt.Errorf("gm_combatlog: %w", err)
		}
	}
	rep, err := s.serveGM(ctx, line)
	if err != nil {
		return ServeCombatLogWatch{}, fmt.Errorf("gm_combatlog: %w", err)
	}
	text := combatLogReply(rep)
	out.Text = text
	if strings.Contains(strings.ToLower(text), "no such command") {
		return ServeCombatLogWatch{}, &rpc.Error{
			Code:    rpc.CodeGame,
			Message: "gm_combatlog: unsupported-core: the core has no .gm combatlog (needs 0.1.38 or later)",
			Data:    map[string]any{"code": "unsupported-core"},
		}
	}
	if text == "" {
		return ServeCombatLogWatch{}, fmt.Errorf("gm_combatlog: the server sent no answer")
	}
	if mode == "off" {
		// Both of the core's answers to off are a stop: "Combat log watch stopped." and "You have no combat log
		// watch." Nothing else is a stop.
		if strings.HasPrefix(text, "Combat log watch stopped") || strings.HasPrefix(text, "You have no combat log watch") {
			return out, nil
		}
		return ServeCombatLogWatch{}, fmt.Errorf("gm_combatlog: %s", text)
	}
	if !strings.HasPrefix(text, "Combat log watch on") {
		return ServeCombatLogWatch{}, fmt.Errorf("gm_combatlog: %s", text)
	}
	out.Watching = true
	return out, nil
}

// combatLogReply is the core's answer line: the first non-empty line of the reply, which is one line on this core.
func combatLogReply(rep *command.Reply) string {
	if rep == nil {
		return ""
	}
	for _, ln := range rep.Raw {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}

// serveGMSetHP sets one unit's health. `.modify hp <hp>` sets the selected player's health and its maximum to that
// one value (cs_modify.cpp:137-149), so passing the unit's own maximum is a refill; `.modify currenthp <hp>` (the
// current_only form) sets the current health and leaves the maximum alone, which is how a unit is hurt without
// replacing its maximum.
func (s *session) serveGMSetHP(ctx context.Context, p serveGMSetHPParams) (ServeCommand, error) {
	if p.HP == nil {
		return ServeCommand{}, errors.New("gm_set_hp needs hp")
	}
	hp := *p.HP
	if hp < 1 || hp > math.MaxInt32 {
		return ServeCommand{}, fmt.Errorf("gm_set_hp: hp must be 1 to %d", math.MaxInt32)
	}
	o, err := s.serveUnitByName(p.GUID, "")
	if err != nil {
		return ServeCommand{}, fmt.Errorf("gm_set_hp: %w", err)
	}
	if err := s.serveGMUnit(o, "gm_set_hp"); err != nil {
		return ServeCommand{}, err
	}
	if err := s.serveSelectConfirmed(ctx, o, serveGMSelectWait); err != nil {
		return ServeCommand{}, fmt.Errorf("gm_set_hp: %w", err)
	}
	if p.CurrentOnly {
		return s.serveGMResult(ctx, fmt.Sprintf(".modify currenthp %d", hp))
	}
	return s.serveGMResult(ctx, fmt.Sprintf(".modify hp %d", hp))
}

// serveGMAura puts one aura on a unit (`.aura <spell>`) or takes it off (`.unaura <spell>`), both of which act on the
// selected unit.
func (s *session) serveGMAura(ctx context.Context, p serveGMAuraParams) (ServeCommand, error) {
	if p.Spell <= 0 {
		return ServeCommand{}, errors.New("gm_aura needs a positive spell id")
	}
	o, err := s.serveUnitByName(p.GUID, "")
	if err != nil {
		return ServeCommand{}, fmt.Errorf("gm_aura: %w", err)
	}
	if err := s.serveGMUnit(o, "gm_aura"); err != nil {
		return ServeCommand{}, err
	}
	if err := s.serveSelectConfirmed(ctx, o, serveGMSelectWait); err != nil {
		return ServeCommand{}, fmt.Errorf("gm_aura: %w", err)
	}
	line := fmt.Sprintf(".aura %d", p.Spell)
	if p.Remove {
		line = fmt.Sprintf(".unaura %d", p.Spell)
	}
	return s.serveGMResult(ctx, line)
}

func commandResult(sent string, rep *command.Reply) ServeCommand {
	return ServeCommand{Sent: sent, Lines: rep.Raw, Complete: rep.Complete, End: rep.End}
}

// serveEventsBounds validates events' arguments and returns the page bound and the wait: a negative limit and a
// wait_ms outside 0..2,000 are refused, and a limit of 0 or above CombatPageMax is the page maximum.
func serveEventsBounds(p serveEventsParams) (limit int, wait time.Duration, err error) {
	if p.Limit < 0 {
		return 0, 0, errors.New("events: limit must not be negative")
	}
	limit = p.Limit
	if limit == 0 || limit > CombatPageMax {
		limit = CombatPageMax
	}
	wait = time.Duration(p.WaitMS) * time.Millisecond
	if wait < 0 || wait > 2*time.Second {
		return 0, 0, errors.New("events: wait_ms must be 0 to 2000")
	}
	return limit, wait, nil
}

// serveEventsPage is one read of the ring on the session goroutine: the rows
// above since, oldest first, at most limit, and the channel the ring's next
// Append closes. It fills each row's spell_name from the server's SpellName
// records (one batched fetch for the ids the page names): the page carries
// the server's own names, with the row's spell_id as the fallback.
func (s *session) serveEventsPage(since, limit int) (ServeEventsPage, <-chan struct{}) {
	rows, next, oldest, dropped, changed := s.CombatLog().SinceChanged(since, limit)
	if rows == nil {
		rows = []CombatRow{}
	}
	ids := make([]int32, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.SpellID)
	}
	names := s.spellNamesFor(ids)
	for i := range rows {
		if rows[i].SpellID > 0 {
			rows[i].SpellName = names[uint32(rows[i].SpellID)]
		}
	}
	return ServeEventsPage{Events: rows, Next: next, Oldest: oldest, Dropped: dropped}, changed
}

// serveEventsSnapshot is one page and its wake signal: page is what the caller gets, changed is the ring's channel
// the next Append closes (internal, never marshalled).
type serveEventsSnapshot struct {
	page    ServeEventsPage
	changed <-chan struct{}
}

// eventsSnapshot copies one page on the session goroutine and hands back the ring's wake signal read in the same
// lock, so the caller can wait for the next row off the loop without missing one appended in between.
func (st *serveState) eventsSnapshot(ctx context.Context, since, limit int) (serveEventsSnapshot, error) {
	v, err := st.callDead(ctx, func(s *session) (any, error) {
		page, changed := s.serveEventsPage(since, limit)
		return serveEventsSnapshot{page: page, changed: changed}, nil
	})
	if err != nil {
		return serveEventsSnapshot{}, err
	}
	snap, ok := v.(serveEventsSnapshot)
	if !ok {
		return serveEventsSnapshot{}, &rpc.Error{Code: rpc.CodeInternal, Message: "internal: method result type"}
	}
	return snap, nil
}

// serveEvents answers the session's combat log: the rows above since, oldest first, at most limit (1,000). A wait_ms
// makes the read wait for a row instead of answering an empty page at once, the same long-poll chat_log uses.
//
// The wait is spent off the session goroutine: the session's call queue is shared by every method, so an empty
// long-poll that slept in it would hold status, chats, GM hands and a logout for its whole wait, and concurrent
// long-polls would run one after another. Here only the two short page copies run on the loop; between them the
// call waits on the ring's own change signal, which new rows close.
func (st *serveState) serveEvents(ctx context.Context, p serveEventsParams) (ServeEventsPage, error) {
	limit, wait, err := serveEventsBounds(p)
	if err != nil {
		return ServeEventsPage{}, err
	}
	snap, err := st.eventsSnapshot(ctx, p.Since, limit)
	if err != nil {
		return ServeEventsPage{}, err
	}
	if len(snap.page.Events) > 0 || wait == 0 {
		return snap.page, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-snap.changed:
		case <-timer.C:
			// The wait is over: read once more, so a row appended as the timer fired is in this answer.
			snap, err = st.eventsSnapshot(ctx, p.Since, limit)
			if err != nil {
				return ServeEventsPage{}, err
			}
			return snap.page, nil
		case <-ctx.Done():
			return ServeEventsPage{}, ctx.Err()
		case <-st.done:
			return ServeEventsPage{}, ErrLinkLost
		}
		snap, err = st.eventsSnapshot(ctx, p.Since, limit)
		if err != nil {
			return ServeEventsPage{}, err
		}
		if len(snap.page.Events) > 0 {
			return snap.page, nil
		}
	}
}

// serveEventLogBounds validates event_log's arguments: a negative limit and a wait_ms outside 0..2,000 are refused
// and a limit of 0 or above EventPageMax is the page maximum.
func serveEventLogBounds(p serveEventLogParams) (limit int, wait time.Duration, err error) {
	if p.Limit < 0 {
		return 0, 0, errors.New("event_log: limit must not be negative")
	}
	limit = p.Limit
	if limit == 0 || limit > EventPageMax {
		limit = EventPageMax
	}
	wait = time.Duration(p.WaitMS) * time.Millisecond
	if wait < 0 || wait > 2*time.Second {
		return 0, 0, errors.New("event_log: wait_ms must be 0 to 2000")
	}
	return limit, wait, nil
}

// eventLogSnapshot is one page of the session event log with the ring's wake signal read in the same lock: an event
// appended between the page and the wait is either in the page or ends the wait. It does not go through the session
// queue: the ring has its own lock and outlives the link (a reconnect hands it to the next session), so a poll
// answers while an act, a walk or a follow step holds the session goroutine.
type eventLogSnapshot struct {
	page    ServeEventPage
	changed <-chan struct{}
}

func (st *serveState) eventLogSnapshot(since uint64, limit int) (eventLogSnapshot, error) {
	s := st.session.Load()
	if s == nil {
		return eventLogSnapshot{}, ErrLinkLost
	}
	r := s.EventLog().Read(since, limit)
	events := r.Rows
	if events == nil {
		events = []Event{}
	}
	return eventLogSnapshot{
		page:    ServeEventPage{Events: events, Next: r.Next, More: r.More, Dropped: r.Dropped, Epoch: r.Epoch, Reset: r.Reset},
		changed: r.Changed,
	}, nil
}

// serveEventLog answers the session's event log: the events above since, oldest first, at most limit (1,000). Like
// events, a wait_ms is spent off the session goroutine on the ring's own change signal, so an empty long-poll never
// holds the call queue, and it still answers while the link is reconnecting (the ring outlives the link).
func (st *serveState) serveEventLog(ctx context.Context, p serveEventLogParams) (ServeEventPage, error) {
	limit, wait, err := serveEventLogBounds(p)
	if err != nil {
		return ServeEventPage{}, err
	}
	snap, err := st.eventLogSnapshot(p.Since, limit)
	if err != nil {
		return ServeEventPage{}, err
	}
	if len(snap.page.Events) > 0 || wait == 0 || snap.page.Reset {
		return snap.page, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-snap.changed:
		case <-timer.C:
			snap, err = st.eventLogSnapshot(p.Since, limit)
			if err != nil {
				return ServeEventPage{}, err
			}
			return snap.page, nil
		case <-ctx.Done():
			return ServeEventPage{}, ctx.Err()
		case <-st.done:
			return ServeEventPage{}, ErrLinkLost
		}
		snap, err = st.eventLogSnapshot(p.Since, limit)
		if err != nil {
			return ServeEventPage{}, err
		}
		if len(snap.page.Events) > 0 || snap.page.Reset {
			return snap.page, nil
		}
	}
}

// serveAct parses a script, runs it in order on the session goroutine and answers the lines `forever act` prints. A
// step's own failure is the script's result (ok false, after its fail line), never an rpc.Error; a step that does not
// parse, or a bad timeout_ms, is refused before anything is sent.
func (s *session) serveAct(ctx context.Context, p serveActParams) (ServeActResult, error) {
	if len(p.Steps) == 0 {
		return ServeActResult{}, errors.New("act needs at least one step")
	}
	steps := make([]ActStep, 0, len(p.Steps))
	for _, line := range p.Steps {
		step, err := ParseActStep(line)
		if err != nil {
			return ServeActResult{}, fmt.Errorf("act: %w", err)
		}
		steps = append(steps, step)
	}
	// A script with a flight gets the flight's own budget by default; one below what the flights need is refused
	// before anything is sent, because a budget cannot cut a flight mid-air. The caller's cap keeps the method's old
	// ten minutes and grows to the flight's own budget when that is longer.
	budget := serveActDefaultBudget
	if flights := actScriptFlights(steps); flights > 0 {
		budget = serveActFlightBudget(steps)
	}
	maxBudget := serveActTimeoutMax
	if f := serveActFlightBudget(steps); f > maxBudget {
		maxBudget = f
	}
	if p.TimeoutMS < 0 || p.TimeoutMS > int(maxBudget/time.Millisecond) {
		return ServeActResult{}, fmt.Errorf("act: timeout_ms must be 0 to %d", maxBudget.Milliseconds())
	}
	if p.TimeoutMS > 0 {
		budget = time.Duration(p.TimeoutMS) * time.Millisecond
		if flights := actScriptFlights(steps); flights > 0 && budget < serveActFlightBudget(steps) {
			return ServeActResult{}, fmt.Errorf("act: timeout_ms %d is below what %d flight(s) need (%d ms): a flight may take %s",
				p.TimeoutMS, flights, serveActFlightBudget(steps).Milliseconds(), actTaxiFlightMax)
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	lines, err := s.RunActSteps(runCtx, steps)
	out := ServeActResult{Lines: lines, OK: err == nil}
	if err == nil {
		return out, nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		// The script's own budget ran out: a result, with a last line that says so.
		out.Lines = append(out.Lines, fmt.Sprintf("act: the run ran out of its %s budget: %v", budget, err))
		return out, nil
	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		return out, err
	}
	return out, nil
}

func (s *session) serveChatLog(ctx context.Context, p serveChatLogParams) (ServeChatPage, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	wait := time.Duration(p.WaitMS) * time.Millisecond
	if wait < 0 || wait > 25*time.Second {
		return ServeChatPage{}, errors.New("chat_log: wait_ms must be 0 to 25000")
	}
	deadline := s.Now().Add(wait)
	for {
		total := uint64(s.chat.Total())
		lines := s.chat.Lines()
		oldest := total - uint64(len(lines))
		since := p.Since
		dropped := false
		if since < oldest {
			since, dropped = oldest, true
		}
		if since < total || !s.Now().Before(deadline) {
			page := ServeChatPage{Oldest: oldest, Dropped: dropped}
			start := int(since - oldest)
			for i := start; i < len(lines) && len(page.Events) < limit; i++ {
				ln := lines[i]
				page.Events = append(page.Events, ServeChatEvent{
					Seq:  oldest + uint64(i) + 1,
					Kind: "chat", Type: chat.TypeName(ln.Type), Source: s.eventName(ln.SenderName, ln.SenderGUID),
					Channel: ln.Channel, Text: ln.Text,
				})
			}
			page.Next = since + uint64(len(page.Events))
			page.More = page.Next < total
			return page, nil
		}
		if err := s.collect(ctx, 100*time.Millisecond); err != nil {
			return ServeChatPage{}, err
		}
	}
}

func (s *session) serveDeath() ServeDeath {
	r := s.deathReport()
	out := ServeDeath{
		Phase: r.Phase, Released: r.Released, Resurrect: r.Offer != nil,
		CorpseMap: r.Corpse.MapID, CorpseX: r.Corpse.X, CorpseY: r.Corpse.Y, CorpseZ: r.Corpse.Z,
	}
	if s.world != nil {
		if d, ok := s.world.DeathOf(s.selfOrMover()); ok {
			out.Dead, out.Ghost = d.Dead, d.Ghost
		}
	}
	if !r.ReclaimEnd.IsZero() {
		if left := r.ReclaimEnd.Sub(s.Now()); left > 0 {
			out.ReclaimInMS = left.Milliseconds()
		}
	}
	return out
}
