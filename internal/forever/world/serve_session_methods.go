package world

// The RPC method table of forever serve. A method whose ForeverCore code exists in this package is registered with
// its typed params; every other method the old daemon served is registered as a typed "not ported on this server"
// error, so an old caller gets a name and a reason, never an empty success.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/objects"
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

type serveEventsParams struct {
	Since  int `json:"since"`
	Limit  int `json:"limit,omitempty"`
	WaitMS int `json:"wait_ms,omitempty"`
}

type serveGMSetHPParams struct {
	GUID GUID   `json:"guid"`
	HP   *int64 `json:"hp"`
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
	srv.Register("gm_kill", serveCallTyped(st, func(s *session, ctx context.Context, p serveUnitParams) (ServeCommand, error) {
		return s.serveGMKill(ctx, p)
	}))
	srv.Register("gm_set_hp", serveCallTyped(st, func(s *session, ctx context.Context, p serveGMSetHPParams) (ServeCommand, error) {
		return s.serveGMSetHP(ctx, p)
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
	srv.Register("accept_invite", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.serveAcceptInvite(ctx)
	}))
	srv.Register("leave_group", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (serveEmpty, error) {
		return serveEmpty{}, s.serveLeaveGroup(ctx)
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
	rep, err := s.serveGM(ctx, text)
	if err != nil {
		return ServeCommand{}, err
	}
	return commandResult(strings.TrimSpace(text), rep), nil
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
// runner has not (re)registered yet all refuse.
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
)

// serveGMHandCommand classifies one raw gm text. It is the core's own command table that decides which lines act on
// a unit's life, health, power, auras or damage: `.die` (Chat.cpp:885), `.damage` (:939), `.aura`/`.unaura`
// (:874-875), `.revive` (:887) and the `.modify` subcommands `hp`, `mana`, `rage` and `energy` (:360-365; the
// ForeverCore table adds the generic `power` and `runicpower`, which this 1.12 client can still be pointed at).
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
		if serveGMNameIs(strings.ToLower(fields[1]), "hp", "mana", "rage", "energy", "power", "runicpower") {
			return serveGMHandSelected, ""
		}
		return serveGMHandNone, ""
	case serveGMNameIs(cmd, "revive"):
		if len(fields) > 1 {
			return serveGMHandNamed, fields[1]
		}
		return serveGMHandSelected, ""
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
// what gm_kill, gm_set_hp or gm_aura refuse. A refusal is the typed -32006 and sends nothing, the GM guard's probe
// included. A line that is not a unit command passes untouched.
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
		if s.serveGMSelfName(name) {
			self, ok := s.world.Object(s.selfOrMover())
			if !ok {
				return serveGMRefused(method, "the character's own unit is not in view, so the target cannot be checked")
			}
			return s.serveGMUnit(self, method)
		}
		return serveGMRefused(method, fmt.Sprintf("the line names a player other than this session's character (%s)", chat.SanitiseName(name)))
	case serveGMHandSelf:
		self, ok := s.world.Object(s.selfOrMover())
		if !ok {
			return serveGMRefused(method, "the character's own unit is not in view, so the target cannot be checked")
		}
		return s.serveGMUnit(self, method)
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

// serveGMSelfName says whether the name a .revive line carries is this session's own character: only that player
// passes the player rule, every other name (a bot of the trial included) is refused.
func (s *session) serveGMSelfName(name string) bool {
	name = strings.TrimSpace(strings.Trim(name, "\"'"))
	if name == "" {
		return false
	}
	if own := s.report.Character.Name; own != "" && strings.EqualFold(name, own) {
		return true
	}
	if o, ok := s.world.Object(s.selfOrMover()); ok {
		return strings.EqualFold(name, s.world.Name(o))
	}
	return false
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

// serveGMSetHP sets one unit's health with `.modify hp <hp>`. The core's command sets the selected player's health
// and its maximum to that one value (cs_modify.cpp:137-149); passing the unit's own maximum is therefore a refill
// and the maximum does not change.
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
	"buy", "convert_raid",
	"disband", "dismount",
	"dungeon_pause", "dungeon_resume", "dungeon_run", "dungeon_skip", "dungeon_stage", "dungeon_status", "dungeon_stop",
	"equip", "equipment",
	"follow_start", "follow_status", "follow_stop",
	"gameobjects",
	"gm_evade", "gm_respawn",
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
