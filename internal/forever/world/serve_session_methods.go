package world

// The RPC method table of forever serve. A method whose ForeverCore code exists in this package is registered with
// its typed params; every other method the old daemon served is registered as a typed "not ported on this server"
// error, so an old caller gets a name and a reason, never an empty success.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
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
	InGroup       bool               `json:"in_group"`
	Leader        GUID               `json:"leader,omitempty"`
	Members       []ServePartyMember `json:"members"`
	PendingInvite string             `json:"pending_invite,omitempty"`
}

// ServePartyMember is one party row.
type ServePartyMember struct {
	Name      string `json:"name"`
	GUID      GUID   `json:"guid"`
	Online    bool   `json:"online"`
	Dead      bool   `json:"dead"`
	Leader    bool   `json:"leader"`
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

type serveUnitsParams struct {
	Within  float32 `json:"within,omitempty"`
	Near    float32 `json:"near,omitempty"`
	Entry   int32   `json:"entry,omitempty"`
	Name    string  `json:"name,omitempty"`
	Hostile *bool   `json:"hostile,omitempty"`
	Limit   int     `json:"limit,omitempty"`
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

type serveWalkParams struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
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
		return s.serveStatus(st.deadError()), nil
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
	srv.Register("chat_log", serveReadTyped(st, func(s *session, ctx context.Context, p serveChatLogParams) (ServeChatPage, error) {
		return s.serveChatLog(ctx, p)
	}))
	srv.Register("gm", serveCallTyped(st, func(s *session, ctx context.Context, p serveGMTextParams) (ServeCommand, error) {
		return s.serveGMResult(ctx, p.Text)
	}))
	srv.Register("gm_revive", serveCallTyped(st, func(s *session, ctx context.Context, p serveNameParams) (ServeCommand, error) {
		name, err := chat.NormaliseName(p.Name)
		if err != nil {
			return ServeCommand{}, err
		}
		return s.serveGMResult(ctx, ".revive "+name)
	}))
	srv.Register("bot", serveCallTyped(st, func(s *session, ctx context.Context, p serveBotParams) (ServeCommand, error) {
		rep, err := s.serveBot(ctx, p.Line, time.Duration(p.WaitMS)*time.Millisecond)
		if err != nil {
			return ServeCommand{}, err
		}
		return commandResult(p.Line, rep), nil
	}))
	srv.Register("party", serveCallTyped(st, func(s *session, _ context.Context, _ serveEmpty) (ServePartyInfo, error) {
		return s.servePartyInfo(), nil
	}))
	srv.Register("invite", serveCallTyped(st, func(s *session, ctx context.Context, p serveNameParams) (serveEmpty, error) {
		return serveEmpty{}, s.serveInvite(ctx, p.Name)
	}))
	srv.Register("accept_invite", serveCallTyped(st, func(s *session, _ context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.serveAcceptInvite(context.Background())
	}))
	srv.Register("leave_group", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.serveLeaveGroup(ctx)
	}))
	srv.Register("attack", serveCallTyped(st, func(s *session, ctx context.Context, p serveAttackParams) (ServeAttack, error) {
		return s.serveAttack(ctx, p)
	}))
	srv.Register("stop_attack", rpc.Typed(func(ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		if st.s.serveRT.fighting.Load() {
			st.s.serveRT.stopFight.Store(true)
			return serveEmpty{}, nil
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
			return serveEmpty{}, errors.New("cast: trade_slot is not ported on this server")
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
	srv.Register("walk_path", serveCallTyped(st, func(s *session, ctx context.Context, p serveWalkParams) (ServePath, error) {
		return s.serveWalkPath(ctx, p.X, p.Y, p.Z)
	}))
	srv.Register("area_trigger", serveCallTyped(st, func(s *session, _ context.Context, p serveAreaTriggerParams) (serveEmpty, error) {
		if p.ID <= 0 {
			return serveEmpty{}, errors.New("area_trigger: needs a trigger id")
		}
		return serveEmpty{}, s.EnterAreaTrigger(p.ID)
	}))
	srv.Register("death_status", serveReadTyped(st, func(s *session, _ context.Context, _ serveEmpty) (ServeDeath, error) {
		return s.serveDeath(), nil
	}))
	srv.Register("accept_resurrect", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (ServeDeath, error) {
		if err := s.AcceptResurrect(ctx); err != nil {
			return ServeDeath{}, err
		}
		return s.serveDeath(), nil
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

	for _, name := range notPortedMethods {
		name := name
		srv.Register(name, func(ctx context.Context, raw json.RawMessage) (any, error) {
			return nil, notPorted(name)
		})
	}
}

// deadError is the status error text while the link is gone.
func (st *serveState) deadError() string {
	if !st.dead || st.deadErr == nil {
		return ""
	}
	return st.deadErr.Error()
}

func (s *session) serveUnits(p serveUnitsParams) ([]ServeUnit, error) {
	if p.Hostile != nil {
		return nil, errors.New("units: the hostile filter is not ported on this server")
	}
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
		d = 60 * time.Second
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
	rep, err := s.serveGM(ctx, text)
	if err != nil {
		return ServeCommand{}, err
	}
	return commandResult(strings.TrimSpace(text), rep), nil
}

func commandResult(sent string, rep *command.Reply) ServeCommand {
	return ServeCommand{Sent: sent, Lines: rep.Raw, Complete: rep.Complete, End: rep.End}
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
					Kind: "chat", Type: chat.TypeName(ln.Type), Source: ln.SenderName,
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

// notPortedMethods are the old daemon's methods this server does not implement. The names are the daemon's own, so
// a caller that was written for it gets the reason instead of "method not found".
var notPortedMethods = []string{
	"addon_click", "addon_start", "addon_state", "addon_stop", "addon_tick",
	"assistant", "bot_view",
	"brain_start", "brain_status", "brain_stop",
	"buy", "commander", "convert_raid",
	"disband", "dismount",
	"dungeon_pause", "dungeon_resume", "dungeon_run", "dungeon_skip", "dungeon_stage", "dungeon_status", "dungeon_stop",
	"equip", "equipment", "events",
	"follow_start", "follow_status", "follow_stop",
	"gameobjects",
	"gm_aura", "gm_evade", "gm_kill", "gm_respawn", "gm_set_hp",
	"gossip_hello", "gossip_select",
	"inventory", "join_channel", "leave_channel",
	"lfg_auto", "lfg_join", "lfg_leave", "lfg_listing_role", "lfg_listings", "lfg_proposal_answer",
	"lfg_role_check_answer", "lfg_status",
	"loot", "loot_close", "loot_method", "loot_take",
	"map_snapshot", "mark", "mount", "open_object",
	"pet", "pet_action", "plan_check",
	"promote",
	"quest_abandon", "quest_accept", "quest_complete", "quest_hello", "quest_log", "quest_query", "quest_reward",
	"raid_move", "roll",
	"sell", "spells",
	"taxi",
	"tbm", "tbm_buttons", "tbm_click", "tbm_handshake", "tbm_send",
	"teardown", "teleport",
	"trade", "trade_accept", "trade_cancel", "trade_expect", "trade_open", "trade_set_item", "trade_status",
	"train", "trainer_list", "tt", "tune_list", "tune_set",
	"use_item", "use_object", "vendor_list",
}
