package live

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RPCError is one JSON-RPC error answer: its Code drives the retry policy,
// its Message is already scrubbed of the token by the caller.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// RPC is the serve surface the runner uses. The production implementation
// calls one slot agent's JSON-RPC over HTTP; the fake in fakeserve_test.go
// answers from a script.
type RPC interface {
	Call(ctx context.Context, method string, params, out any, timeout time.Duration) error
}

// Retry policy: -32005 (rate/transient) is retried every retryEvery for up
// to retryWindow; -32002 (the session is gone) ends the repeat at once.
// Anything else is returned to the caller.
const (
	retryEvery  = 2 * time.Second
	retryWindow = 60 * time.Second
)

// Client is the runner's typed view of one serve slot.
type Client struct {
	rpc RPC
}

// NewClient wraps rpc.
func NewClient(rpc RPC) *Client { return &Client{rpc: rpc} }

// codeOf unwraps the RPC status code from the production slot client's
// *slotRPCError (matched by shape: Code and Message fields) or an *RPCError.
func codeOf(err error) (int, bool) {
	var re *RPCError
	if errors.As(err, &re) {
		return re.Code, true
	}
	type shape struct {
		Code    int
		Message string
	}
	for e := err; e != nil; {
		if s, ok := e.(interface{ RPCStatusCode() int }); ok {
			return s.RPCStatusCode(), true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	_ = shape{}
	return 0, false
}

// call runs method with params into out, applying the retry policy. A nil
// ctx means Background. timeout bounds each single attempt.
func (c *Client) call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(retryWindow)
	for {
		err := c.rpc.Call(ctx, method, params, out, timeout)
		if err == nil {
			return nil
		}
		code, ok := codeOf(err)
		switch {
		case ok && code == -32005:
			if time.Now().Add(retryEvery).After(deadline) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryEvery):
			}
		case ok && code == -32002:
			return errDisconnected(err)
		default:
			return err
		}
	}
}

// Disconnected is the runner's repeat-ending error: the session went away
// (a lost lease or a disconnect), so nothing more goes out.
type Disconnected struct{ err error }

func (e *Disconnected) Error() string { return "disconnected: " + e.err.Error() }
func (e *Disconnected) Unwrap() error { return e.err }

func errDisconnected(err error) *Disconnected { return &Disconnected{err: err} }

// IsDisconnected reports whether err ends the repeat: a -32002 answer or a
// transport line that reads like the Python DISCONNECT pattern.
func IsDisconnected(err error) bool {
	var d *Disconnected
	if errors.As(err, &d) {
		return true
	}
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, sub := range []string{"disconnect", "logged out", "session lost", "session ended", "session closed", "left the world"} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// EventsPage is one `events` answer.
type EventsPage struct {
	Events  []map[string]any `json:"events"`
	Next    uint64           `json:"next"`
	Oldest  uint64           `json:"oldest"`
	Dropped bool             `json:"dropped"`
	More    bool             `json:"more"`
}

// Events drains one page: wait_ms up to 2000, limit up to 1000.
func (c *Client) Events(ctx context.Context, since uint64, limit, waitMS int) (EventsPage, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	if waitMS < 0 {
		waitMS = 0
	}
	if waitMS > 2000 {
		waitMS = 2000
	}
	var page EventsPage
	err := c.call(ctx, "events", map[string]any{"since": since, "limit": limit, "wait_ms": waitMS}, &page, time.Duration(waitMS)*time.Millisecond+20*time.Second)
	return page, err
}

// Status is the serve status row the sampler reads.
type Status struct {
	State     string         `json:"state"`
	GUID      string         `json:"guid"`
	Character string         `json:"character"`
	Health    uint32         `json:"health"`
	MaxHealth uint32         `json:"max_health"`
	Power     uint32         `json:"power"`
	MaxPower  uint32         `json:"max_power"`
	Target    string         `json:"target"`
	X         float32        `json:"x"`
	Y         float32        `json:"y"`
	Z         float32        `json:"z"`
	MonoMS    int64          `json:"mono_ms"`
	Dead      bool           `json:"dead"`
	Raw       map[string]any `json:"-"`
}

// Unit is one `units` row the sampler reads.
type Unit struct {
	GUID      string           `json:"guid"`
	Name      string           `json:"name"`
	Type      string           `json:"type"`
	Entry     int              `json:"entry"`
	Level     int              `json:"level"`
	Distance  float64          `json:"distance"`
	Health    uint32           `json:"health"`
	MaxHealth uint32           `json:"max_health"`
	Power     uint32           `json:"power"`
	MaxPower  uint32           `json:"max_power"`
	InCombat  bool             `json:"in_combat"`
	Dead      bool             `json:"dead"`
	Target    string           `json:"target"`
	X         float32          `json:"x"`
	Y         float32          `json:"y"`
	Z         float32          `json:"z"`
	Casting   any              `json:"casting"`
	Auras     []map[string]any `json:"auras"`
	Raw       map[string]any   `json:"-"`
}

// StatusOf reads `status`.
func (c *Client) StatusOf(ctx context.Context) (Status, error) {
	var st Status
	var raw map[string]any
	if err := c.call(ctx, "status", nil, &raw, 10*time.Second); err != nil {
		return Status{}, err
	}
	st.Raw = raw
	st.State, _ = raw["state"].(string)
	st.GUID = normGUID(raw["guid"])
	st.Character, _ = raw["character"].(string)
	st.Health = uintOf(raw["health"])
	st.MaxHealth = uintOf(raw["max_health"])
	st.Power = uintOf(raw["power"])
	st.MaxPower = uintOf(raw["max_power"])
	st.Target = normGUID(raw["target"])
	st.MonoMS = intOf(raw["mono_ms"])
	st.Dead = boolOf(raw["dead"])
	if x, ok := numOf(raw["x"]); ok {
		st.X = float32(x)
	}
	if y, ok := numOf(raw["y"]); ok {
		st.Y = float32(y)
	}
	if z, ok := numOf(raw["z"]); ok {
		st.Z = float32(z)
	}
	return st, nil
}

// Units reads `units` within yards of the player.
func (c *Client) Units(ctx context.Context, within float64) ([]Unit, error) {
	return c.UnitsFilter(ctx, within, 0, "")
}

// UnitsFilter reads `units` with the serve's entry and name filters (0 and
// "" leave them off).
func (c *Client) UnitsFilter(ctx context.Context, within float64, entry int, name string) ([]Unit, error) {
	params := map[string]any{"within": within}
	if entry != 0 {
		params["entry"] = entry
	}
	if name != "" {
		params["name"] = name
	}
	var rows []map[string]any
	if err := c.call(ctx, "units", params, &rows, 15*time.Second); err != nil {
		return nil, err
	}
	out := make([]Unit, 0, len(rows))
	for _, r := range rows {
		out = append(out, unitFrom(r))
	}
	return out, nil
}

// Unit reads one `unit` row by guid.
func (c *Client) Unit(ctx context.Context, guid string) (Unit, error) {
	var raw map[string]any
	if err := c.call(ctx, "unit", map[string]any{"guid": guid}, &raw, 10*time.Second); err != nil {
		return Unit{}, err
	}
	return unitFrom(raw), nil
}

// unitFrom parses one unit row (the shape `units` and `unit` share).
func unitFrom(raw map[string]any) Unit {
	u := Unit{Raw: raw}
	u.GUID = normGUID(raw["guid"])
	u.Name, _ = raw["name"].(string)
	u.Type, _ = raw["type"].(string)
	u.Entry = int(intOf(raw["entry"]))
	u.Level = int(intOf(raw["level"]))
	if d, ok := numOf(raw["distance"]); ok {
		u.Distance = d
	}
	u.Health = uintOf(raw["health"])
	u.MaxHealth = uintOf(raw["max_health"])
	u.Power = uintOf(raw["power"])
	u.MaxPower = uintOf(raw["max_power"])
	u.InCombat = boolOf(raw["in_combat"])
	u.Dead = boolOf(raw["dead"])
	u.Target = normGUID(raw["target"])
	if x, ok := numOf(raw["x"]); ok {
		u.X = float32(x)
	}
	if y, ok := numOf(raw["y"]); ok {
		u.Y = float32(y)
	}
	if z, ok := numOf(raw["z"]); ok {
		u.Z = float32(z)
	}
	u.Casting = raw["casting"]
	if list, ok := raw["auras"].([]any); ok {
		for _, a := range list {
			if m, ok := a.(map[string]any); ok {
				u.Auras = append(u.Auras, m)
			}
		}
	}
	return u
}

// GM sends one raw `gm` line. Only the guard may call it: a silent command
// answers after the serve's quiet window, but a heavy one (a level jump)
// can hold the session's serialised channel, so the call gets a minute.
func (c *Client) GM(ctx context.Context, line string) error {
	return c.call(ctx, "gm", map[string]string{"text": line}, nil, 60*time.Second)
}

// IsNoReply reports the serve's "the server sent no reply" answer: the line
// ran, but the core printed nothing (`.levelup`, `.npc add temp`, `.die` and
// `.learn` on this core are all silent). The caller proves the effect with a
// read-back; a proof failure is still a failure.
func IsNoReply(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "sent no reply")
}

// TrialUnits replaces the serve's GM-unit allowlist (the guids the GM hands
// may act on) and returns it in force. A serve started without
// --allow-gm-units refuses with -32006.
func (c *Client) TrialUnits(ctx context.Context, guids []string) ([]string, error) {
	params := map[string]any{"guids": guids}
	var out struct {
		GUIDs []string `json:"guids"`
	}
	if err := c.call(ctx, "trial_units", params, &out, 10*time.Second); err != nil {
		return nil, err
	}
	return out.GUIDs, nil
}

// GMKill kills one unit with the serve's select-then-`.die` hand.
func (c *Client) GMKill(ctx context.Context, guid string) error {
	return c.call(ctx, "gm_kill", map[string]any{"guid": guid}, nil, 15*time.Second)
}

// GMSetHP sets one unit's health through the serve's select-then-`.modify hp`
// hand. The core sets current AND maximum health to hp.
func (c *Client) GMSetHP(ctx context.Context, guid string, hp int) error {
	return c.call(ctx, "gm_set_hp", map[string]any{"guid": guid, "hp": hp}, nil, 15*time.Second)
}

// GMAura puts spell on (or removes it from) one unit through the serve's
// select-then-`.aura`/`.unaura` hand.
func (c *Client) GMAura(ctx context.Context, guid string, spell int, remove bool) error {
	params := map[string]any{"guid": guid, "spell": spell}
	if remove {
		params["remove"] = true
	}
	return c.call(ctx, "gm_aura", params, nil, 15*time.Second)
}

// Target selects one unit and returns its row. The call gets 30 s: a heavy
// GM line already in the session's queue (a level jump) can hold it.
func (c *Client) Target(ctx context.Context, guid string) error {
	var out map[string]any
	return c.call(ctx, "target", map[string]any{"guid": guid}, &out, 30*time.Second)
}

// Attack runs the serve's fight loop on guid for at most forMS and returns
// its reason (dead, stopped, map change), "" when none was given.
func (c *Client) Attack(ctx context.Context, guid string, forMS int) (string, error) {
	params := map[string]any{"guid": guid}
	if forMS > 0 {
		params["for_ms"] = forMS
	}
	var out struct {
		Reason string `json:"reason"`
	}
	err := c.call(ctx, "attack", params, &out, time.Duration(forMS)*time.Millisecond+30*time.Second)
	return out.Reason, err
}

// StopAttack asks a running attack to stop.
func (c *Client) StopAttack(ctx context.Context) error {
	return c.call(ctx, "stop_attack", map[string]any{}, nil, 10*time.Second)
}

// PartyMember is one row of the serve's party roster (which carries the
// member guids, unlike the module's text answer).
type PartyMember struct {
	Name      string `json:"name"`
	GUID      string `json:"guid"`
	Online    bool   `json:"online"`
	Dead      bool   `json:"dead"`
	Leader    bool   `json:"leader"`
	Health    uint32 `json:"health"`
	MaxHealth uint32 `json:"max_health"`
	Power     uint32 `json:"power"`
	MaxPower  uint32 `json:"max_power"`
	Level     uint32 `json:"level"`
}

// PartyInfo reads the serve's party roster.
type PartyInfo struct {
	InGroup bool          `json:"in_group"`
	Leader  string        `json:"leader"`
	Members []PartyMember `json:"members"`
}

// Party reads the serve's `party` roster.
func (c *Client) Party(ctx context.Context) (PartyInfo, error) {
	var out PartyInfo
	err := c.call(ctx, "party", map[string]any{}, &out, 10*time.Second)
	return out, err
}

// Chat sends one `chat` line of type typ.
func (c *Client) Chat(ctx context.Context, typ, text, to string) error {
	params := map[string]string{"type": typ, "text": text}
	if to != "" {
		params["to"] = to
	}
	return c.call(ctx, "chat", params, nil, 10*time.Second)
}

// TBM sends one `.tb`-style module command through `chat` party text and
// returns nothing; the module answers in the event log. SayToBot addresses
// one bot: "<bot> <command>" in party chat.
func (c *Client) SayToBot(ctx context.Context, bot, command string) error {
	return c.Chat(ctx, "party", bot+" "+command, "")
}

// TBM sends a raw module command through the `tbm` method.
func (c *Client) TBM(ctx context.Context, command string) error {
	var out map[string]any
	return c.call(ctx, "tbm", map[string]string{"command": command}, &out, 10*time.Second)
}

// Cast casts spell at target (a guid, empty for no target).
func (c *Client) Cast(ctx context.Context, spellID uint32, target string) error {
	params := map[string]any{"spell_id": spellID}
	if target != "" {
		params["target"] = target
	}
	return c.call(ctx, "cast", params, nil, 10*time.Second)
}
