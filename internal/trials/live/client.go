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
	var rows []map[string]any
	if err := c.call(ctx, "units", map[string]any{"within": within}, &rows, 15*time.Second); err != nil {
		return nil, err
	}
	out := make([]Unit, 0, len(rows))
	for _, r := range rows {
		u := Unit{Raw: r}
		u.GUID = normGUID(r["guid"])
		u.Name, _ = r["name"].(string)
		u.Health = uintOf(r["health"])
		u.MaxHealth = uintOf(r["max_health"])
		u.Power = uintOf(r["power"])
		u.MaxPower = uintOf(r["max_power"])
		u.InCombat = boolOf(r["in_combat"])
		u.Dead = boolOf(r["dead"])
		u.Target = normGUID(r["target"])
		if x, ok := numOf(r["x"]); ok {
			u.X = float32(x)
		}
		if y, ok := numOf(r["y"]); ok {
			u.Y = float32(y)
		}
		if z, ok := numOf(r["z"]); ok {
			u.Z = float32(z)
		}
		u.Casting = r["casting"]
		if list, ok := r["auras"].([]any); ok {
			for _, a := range list {
				if m, ok := a.(map[string]any); ok {
					u.Auras = append(u.Auras, m)
				}
			}
		}
		out = append(out, u)
	}
	return out, nil
}

// Unit reads one `unit` row by guid.
func (c *Client) Unit(ctx context.Context, guid string) (Unit, error) {
	var raw map[string]any
	if err := c.call(ctx, "unit", map[string]any{"guid": guid}, &raw, 10*time.Second); err != nil {
		return Unit{}, err
	}
	u := Unit{Raw: raw}
	u.GUID = normGUID(raw["guid"])
	u.Health = uintOf(raw["health"])
	u.MaxHealth = uintOf(raw["max_health"])
	u.Power = uintOf(raw["power"])
	u.MaxPower = uintOf(raw["max_power"])
	u.InCombat = boolOf(raw["in_combat"])
	u.Dead = boolOf(raw["dead"])
	u.Target = normGUID(raw["target"])
	return u, nil
}

// GM sends one raw `gm` line. Only the guard may call it.
func (c *Client) GM(ctx context.Context, line string) error {
	return c.call(ctx, "gm", map[string]string{"text": line}, nil, 10*time.Second)
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
