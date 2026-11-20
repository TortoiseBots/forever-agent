// Package serveclient is the typed Go client of a forever serve session
// daemon: the local JSON-RPC socket `forever serve --dir` serves and
// `forever rpc` talks to. Open takes a serve slot dir (slot.json plus the
// derived token file, the same files openSlotClient reads), New takes an
// address and a token file; every method below decodes one serve reply into
// its typed shape, so callers never juggle map[string]any.
//
// -32005 (reconnecting) is retried every 2 s for up to 60 s and surfaces as
// ErrReconnecting past the budget; -32002 (disconnected) surfaces as
// ErrDisconnected at once. trials/live keeps its own client; this package
// must not grow one-off shapes for it.
package serveclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/runmap"
	"github.com/pjerra/forever-agent/internal/privfile"
	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/tbm"
)

// ErrReconnecting is a call the daemon answered with -32005 past the retry
// budget: its world link is down and being logged in again; the call did
// nothing and nothing was queued. Wait and call again.
var ErrReconnecting = errors.New("serveclient: reconnecting")

// ErrDisconnected is a call the daemon answered with -32002: the session is
// gone (a spent reconnect budget), so nothing more goes out.
var ErrDisconnected = errors.New("serveclient: disconnected")

// Retry policy for -32005: one attempt every retryEvery for up to
// retryWindow. Vars (not consts) so the tests shrink them.
var (
	retryEvery  = 2 * time.Second
	retryWindow = 60 * time.Second
)

// defaultTimeout bounds one attempt of every method without its own wait.
const defaultTimeout = 30 * time.Second

// Error is one JSON-RPC error answer with the method that drew it.
type Error struct {
	Code    int
	Message string
	Method  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("serveclient: %s: rpc error %d: %s", e.Method, e.Code, e.Message)
}

// CodeOf unwraps the RPC status code of an *Error, for callers that branch
// on a code the retry policy does not own (a not-ported -32003, a -32601).
func CodeOf(err error) (int, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}
	return 0, false
}

// GUID is a serve guid in its JSON text: "0x" and hex, 32 digits for a
// 128-bit guid. It decodes what serve encodes, plus the old daemon's two
// spellings (null/"" for empty, a bare number for a uint64 low half).
type GUID string

// Key is the unit key the map views use: runmap.GUIDKey, so a client key and
// a run-map key for one guid are the same number. The high word matters: two
// guids that share their low 64 bits have different keys.
func (g GUID) Key() uint64 { return runmap.GUIDKey(string(g)) }

// MarshalJSON writes the guid text unchanged.
func (g GUID) MarshalJSON() ([]byte, error) { return json.Marshal(string(g)) }

// UnmarshalJSON reads "0x"-prefixed hex (any case, any width), null or "" as
// the empty guid, and a bare number as the uint64 low half (the old daemon's
// guid, high half zero).
func (g *GUID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` {
		*g = ""
		return nil
	}
	if len(s) >= 2 && s[0] == '"' {
		var t string
		if err := json.Unmarshal(b, &t); err != nil {
			return fmt.Errorf("serveclient: guid: %v", err)
		}
		*g = GUID(t)
		return nil
	}
	var n uint64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("serveclient: guid: want a hex string or a number")
	}
	*g = GUID(fmt.Sprintf("0x%016X", n))
	return nil
}

// Status is the status method's result, field for field what serve encodes.
type Status struct {
	Character        string  `json:"character,omitempty"`
	GUID             GUID    `json:"guid"`
	Level            int32   `json:"level,omitempty"`
	Map              uint32  `json:"map"`
	X                float32 `json:"x"`
	Y                float32 `json:"y"`
	Z                float32 `json:"z"`
	O                float32 `json:"o"`
	Target           GUID    `json:"target,omitempty"`
	Health           int64   `json:"health"`
	MaxHealth        int64   `json:"max_health"`
	Power            int32   `json:"power"`
	MaxPower         int32   `json:"max_power"`
	PowerType        uint8   `json:"power_type,omitempty"`
	InCombat         bool    `json:"in_combat"`
	Dead             bool    `json:"dead"`
	Ghost            bool    `json:"ghost"`
	ServerPackets    int     `json:"server_packets"`
	ParseErrors      int     `json:"parse_errors"`
	MonoMS           int64   `json:"mono_ms"`
	State            string  `json:"state"`
	Error            string  `json:"error,omitempty"`
	ReconnectAttempt int     `json:"reconnect_attempt,omitempty"`
	ReconnectLimit   int     `json:"reconnect_limit,omitempty"`
}

// Aura is one aura a unit holds: the spell, the caster's guid, the stack
// count and how long it is still up (RemainingMS 0: no duration).
type Aura struct {
	SpellID     int32 `json:"spell_id"`
	Caster      GUID  `json:"caster"`
	Stacks      int32 `json:"stacks"`
	RemainingMS int64 `json:"remaining_ms"`
}

// Unit is one row of units/unit/target: the model's snapshot plus the
// distance from the mover.
type Unit struct {
	GUID       GUID    `json:"guid"`
	Type       string  `json:"type"`
	Entry      int32   `json:"entry,omitempty"`
	Name       string  `json:"name"`
	X          float32 `json:"x"`
	Y          float32 `json:"y"`
	Z          float32 `json:"z"`
	O          float32 `json:"o"`
	Distance   float32 `json:"distance"`
	Distance2D float32 `json:"distance_2d"`
	Health     int64   `json:"health"`
	MaxHealth  int64   `json:"max_health"`
	Power      int32   `json:"power"`
	MaxPower   int32   `json:"max_power"`
	PowerType  uint8   `json:"power_type,omitempty"`
	Level      int32   `json:"level"`
	Class      uint8   `json:"class,omitempty"`
	Target     GUID    `json:"target,omitempty"`
	InCombat   bool    `json:"in_combat"`
	Dead       bool    `json:"dead"`
	Ghost      bool    `json:"ghost,omitempty"`
	Moving     bool    `json:"moving"`
	Casting    bool    `json:"casting,omitempty"`
	SpellID    int32   `json:"spell_id,omitempty"`
	Auras      []Aura  `json:"auras"`
}

// UnitsParams is the units method's filter; every field is optional.
type UnitsParams struct {
	Within  float32 `json:"within,omitempty"`
	Near    float32 `json:"near,omitempty"`
	Entry   int32   `json:"entry,omitempty"`
	Name    string  `json:"name,omitempty"`
	Hostile *bool   `json:"hostile,omitempty"`
	Limit   int     `json:"limit,omitempty"`
}

// PartyMember is one party row.
type PartyMember struct {
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

// Party is the party method's result.
type Party struct {
	InGroup       bool          `json:"in_group"`
	Leader        GUID          `json:"leader,omitempty"`
	Members       []PartyMember `json:"members"`
	PendingInvite string        `json:"pending_invite,omitempty"`
}

// Command is a gm/bot method's result: the line sent and the server's
// visible answer.
type Command struct {
	Sent     string   `json:"sent"`
	Lines    []string `json:"lines"`
	Complete bool     `json:"complete"`
	End      string   `json:"end,omitempty"`
}

// Event is one event_log row: the internal/events Event JSON keys, guids as
// 0x and 32 hex digits.
type Event struct {
	Seq        uint64         `json:"seq"`
	Time       time.Time      `json:"time"`
	MonoMS     int64          `json:"mono_ms"`
	Kind       string         `json:"kind"`
	Source     string         `json:"source,omitempty"`
	SourceGUID GUID           `json:"source_guid,omitempty"`
	Target     string         `json:"target,omitempty"`
	TargetGUID GUID           `json:"target_guid,omitempty"`
	SpellID    uint32         `json:"spell_id,omitempty"`
	Amount     int64          `json:"amount,omitempty"`
	Crit       bool           `json:"crit,omitempty"`
	Party      bool           `json:"party,omitempty"`
	Text       string         `json:"text,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

// EventLogPage is one event_log answer: the rows above since, oldest first,
// and the ring's bounds. Oldest is zero when the daemon omits it.
type EventLogPage struct {
	Events  []Event `json:"events"`
	Next    uint64  `json:"next"`
	Oldest  uint64  `json:"oldest"`
	Dropped bool    `json:"dropped"`
	More    bool    `json:"more"`
}

// ActResult is an act call's answer: the step lines (`step N verb ok|fail
// reason`) and whether every step ran.
type ActResult struct {
	Lines []string `json:"lines"`
	OK    bool     `json:"ok"`
}

// CommanderParams is the commander method's params (the old daemon's shape,
// which serve keeps when it ports the method).
type CommanderParams struct {
	Op      string   `json:"op"`
	LeadBot string   `json:"lead_bot,omitempty"`
	Bots    []string `json:"bots,omitempty"`
	On      *bool    `json:"on,omitempty"`
	Verb    string   `json:"verb,omitempty"`
	Args    []string `json:"args,omitempty"`
	WaitMS  int      `json:"wait_ms,omitempty"`
}

// CommanderResult is the commander method's answer: the line sent, the
// module's reply, and the typed outcome of a loot/open/use/lootmethod order.
type CommanderResult struct {
	Sent  string           `json:"sent"`
	Reply tbm.Reply        `json:"reply"`
	Order *tbm.OrderResult `json:"order,omitempty"`
}

// Logout is a logout call's answer.
type Logout struct {
	Outcome string `json:"outcome"`
}

// TeardownStep is one teardown step. Result is the serve W2 shape
// ({step, result}); Status/Detail read the old daemon's shape
// ({step, status, detail}), so one type decodes both.
type TeardownStep struct {
	Step   string `json:"step"`
	Result string `json:"result,omitempty"`
	Status string `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Teardown is a teardown call's answer: every step ran, even after a failed
// one.
type Teardown struct {
	Steps []TeardownStep `json:"steps"`
}

// Client is a typed client of one serve daemon.
type Client struct {
	addr  string // the daemon's http://host:port/rpc URL
	token string // empty: the daemon runs without a bearer token
}

// New opens the serve daemon at addr (host:port, scheme optional) with the
// bearer token in tokenFile. An empty tokenFile means the daemon runs with
// authentication off and calls go out without a header. The token file must
// hold exactly the token the daemon generated, never a stray file.
func New(addr, tokenFile string) (*Client, error) {
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
	addr = strings.TrimSuffix(addr, "/rpc")
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("serveclient: bad address %q: %v", addr, err)
	}
	var token string
	if tokenFile != "" {
		raw, err := privfile.Read(tokenFile, 4096)
		if err != nil {
			return nil, fmt.Errorf("serveclient: token file: %v", err)
		}
		token = strings.TrimSpace(string(raw))
		if !validToken(token) {
			return nil, fmt.Errorf("serveclient: the token file is not an agent token")
		}
	}
	return &Client{addr: "http://" + net.JoinHostPort(host, port) + "/rpc", token: token}, nil
}

// Open opens the serve daemon behind a serve --dir: slot.json gives the
// port (and the token file when set), the token comes from that file. The
// dir must be the one slot.json names, so a client never opens a neighbour
// by mistake.
func Open(dir string) (*Client, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("serveclient: %v", err)
	}
	raw, err := privfile.Read(filepath.Join(abs, "slot.json"), 64*1024)
	if err != nil {
		return nil, fmt.Errorf("serveclient: %s/slot.json missing or unreadable: not a serve dir", abs)
	}
	var manifest struct {
		Port      int    `json:"port"`
		Dir       string `json:"dir"`
		TokenFile string `json:"token_file"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil ||
		manifest.Port < 1 || manifest.Port > 65535 {
		return nil, fmt.Errorf("serveclient: %s/slot.json missing or unreadable: not a serve dir", abs)
	}
	if manifest.Dir != "" && manifest.Dir != abs {
		return nil, fmt.Errorf("serveclient: slot.json dir %q is not this dir %q", manifest.Dir, abs)
	}
	tokenFile := manifest.TokenFile
	if tokenFile == "" {
		tokenFile = filepath.Join(abs, fmt.Sprintf("rpc-%d.token", manifest.Port))
	}
	return New(fmt.Sprintf("127.0.0.1:%d", manifest.Port), tokenFile)
}

// validToken reports whether tok has exactly the shape rpc.NewToken
// generates (32 random bytes in base64url without padding), so a stray file
// is refused before it is ever sent.
func validToken(tok string) bool {
	if len(tok) != 43 {
		return false
	}
	for i := range len(tok) {
		c := tok[i]
		if c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// call runs one method with params into out, applying the retry policy. A
// nil ctx means Background; timeout bounds each single attempt.
func (c *Client) call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("serveclient: %s: params: %v", method, err)
		}
		raw = b
	}
	deadline := time.Now().Add(retryWindow)
	for {
		result, rpcErr, err := c.post(ctx, method, raw, timeout)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("serveclient: %s: %w", method, ctxErr)
			}
			return fmt.Errorf("serveclient: %s: %w", method, err)
		}
		if rpcErr != nil {
			switch rpcErr.Code {
			case rpc.CodeReconnecting:
				if time.Now().Add(retryEvery).After(deadline) {
					return fmt.Errorf("serveclient: %s: %w after %s: %s", method, ErrReconnecting, retryWindow, rpcErr.Message)
				}
				select {
				case <-ctx.Done():
					return fmt.Errorf("serveclient: %s: %w", method, ctx.Err())
				case <-time.After(retryEvery):
				}
				continue
			case rpc.CodeDisconnected:
				return fmt.Errorf("serveclient: %s: %w: %s", method, ErrDisconnected, rpcErr.Message)
			default:
				return &Error{Code: rpcErr.Code, Message: rpcErr.Message, Method: method}
			}
		}
		if out != nil && len(bytes.TrimSpace(result)) > 0 && string(bytes.TrimSpace(result)) != "null" {
			if err := json.Unmarshal(result, out); err != nil {
				return fmt.Errorf("serveclient: %s: bad result: %v", method, err)
			}
		}
		return nil
	}
}

// rpcWireError is the error half of a JSON-RPC reply.
type rpcWireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// post sends one JSON-RPC 2.0 request and returns the result or the error
// the daemon answered. Anything that is not a valid 2.0 envelope for our id
// is a transport error, so the caller can tell a bad reply from a dead
// daemon.
func (c *Client) post(ctx context.Context, method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, *rpcWireError, error) {
	reqBody := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		reqBody["params"] = params
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.addr, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, nil, errors.New("unauthorized: the token file no longer matches the daemon (restarted?)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, nil, err
	}
	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcWireError   `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, nil, fmt.Errorf("bad reply: %v", err)
	}
	if reply.Error != nil {
		return nil, reply.Error, nil
	}
	return reply.Result, nil, nil
}

// Status reads status: where the character is, what it is doing, and the
// link's lifecycle (state online/reconnecting/offline).
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	err := c.call(ctx, "status", nil, &st, defaultTimeout)
	return st, err
}

// Units reads units within the filter: every field optional, hostile nil
// means both.
func (c *Client) Units(ctx context.Context, p UnitsParams) ([]Unit, error) {
	var units []Unit
	err := c.call(ctx, "units", p, &units, defaultTimeout)
	return units, err
}

// Unit reads one unit row by guid or name (one of them must name the unit).
func (c *Client) Unit(ctx context.Context, guid, name string) (Unit, error) {
	var u Unit
	err := c.call(ctx, "unit", map[string]any{"guid": GUID(guid), "name": name}, &u, defaultTimeout)
	return u, err
}

// Party reads party: the roster the session holds now.
func (c *Client) Party(ctx context.Context) (Party, error) {
	var p Party
	err := c.call(ctx, "party", nil, &p, defaultTimeout)
	return p, err
}

// GM sends one raw gm line through the GM guard and returns the server's
// visible answer.
func (c *Client) GM(ctx context.Context, text string) (Command, error) {
	var cmd Command
	err := c.call(ctx, "gm", map[string]string{"text": text}, &cmd, defaultTimeout)
	return cmd, err
}

// Bot sends one .tb-style module line and waits up to wait for its answer.
func (c *Client) Bot(ctx context.Context, line string, wait time.Duration) (Command, error) {
	var cmd Command
	err := c.call(ctx, "bot", map[string]any{"line": line, "wait_ms": int(wait.Milliseconds())}, &cmd, wait+defaultTimeout)
	return cmd, err
}

// EventLog drains one event_log page: wait_ms up to 2000, limit up to 1000.
func (c *Client) EventLog(ctx context.Context, since uint64, limit, waitMS int) (EventLogPage, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	if waitMS < 0 {
		waitMS = 0
	}
	if waitMS > 2000 {
		waitMS = 2000
	}
	var page EventLogPage
	err := c.call(ctx, "event_log", map[string]any{"since": since, "limit": limit, "wait_ms": waitMS},
		&page, time.Duration(waitMS)*time.Millisecond+20*time.Second)
	return page, err
}

// Act runs steps (one `verb k=v` line each, as forever act takes them) in
// order on the session goroutine and returns the step lines and whether
// every step ran. timeout bounds the whole run on the daemon (at most 600 s)
// and the call waits that long plus a margin.
func (c *Client) Act(ctx context.Context, steps []string, timeout time.Duration) (ActResult, error) {
	ms := int(timeout.Milliseconds())
	if ms <= 0 {
		return ActResult{}, errors.New("serveclient: act needs a positive timeout")
	}
	if ms > 600000 {
		return ActResult{}, fmt.Errorf("serveclient: act timeout %s is over the 600 s the daemon allows", timeout)
	}
	var res ActResult
	err := c.call(ctx, "act", map[string]any{"steps": steps, "timeout_ms": ms}, &res, timeout+defaultTimeout)
	return res, err
}

// Commander runs one commander call (op bind/unbind/status/view/cmd/run)
// and returns the line sent with the module's reply.
func (c *Client) Commander(ctx context.Context, p CommanderParams) (CommanderResult, error) {
	var res CommanderResult
	wait := 4 * time.Second
	if p.WaitMS > 0 {
		wait = time.Duration(p.WaitMS) * time.Millisecond
	}
	err := c.call(ctx, "commander", p, &res, wait+defaultTimeout)
	return res, err
}

// Logout ends the session the way a real client does; the daemon stops.
func (c *Client) Logout(ctx context.Context) (Logout, error) {
	var out Logout
	err := c.call(ctx, "logout", nil, &out, 60*time.Second)
	return out, err
}

// Teardown ends the game presence best-effort, step by step (gm off, on or
// keep for the GM line); the daemon stops. Every step runs even after a
// failed one.
func (c *Client) Teardown(ctx context.Context, gm string) (Teardown, error) {
	var res Teardown
	err := c.call(ctx, "teardown", map[string]string{"gm": gm}, &res, 120*time.Second)
	return res, err
}
