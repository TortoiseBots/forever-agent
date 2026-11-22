// Package serveclient is the typed Go client of a forever serve session
// daemon: the local JSON-RPC socket `forever serve --dir` serves and
// `forever rpc` talks to. Open takes a serve slot dir (slot.json plus the
// derived token file, the same files openSlotClient reads), New takes an
// address and a token file; every method below decodes one serve reply into
// its typed shape, so callers never juggle map[string]any.
//
// -32005 (reconnecting) is retried every 2 s for up to 60 s and surfaces as
// an *Error that errors.Is ErrReconnecting past the budget; -32002
// (disconnected) surfaces at once as an *Error that errors.Is ErrDisconnected.
// CodeOf reads the code of both. trials/live keeps its own client; this
// package must not grow one-off shapes for it.
//
// The client only ever talks to a loopback address, and sends the bearer
// token nowhere else (no redirect is followed).
//
// Methods the daemon registers today: status, units, unit, party, gm, bot,
// events, logout, commander, and since serve W2-W4 event_log (EventLog), act
// (Act) and teardown (Teardown). Events reads the combat log; EventLog the
// session's one ordered stream. A test in internal/forever/world pins the
// daemon's table and decodes each of these methods against the real daemon.
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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/runmap"
	"github.com/pjerra/forever-agent/internal/netutil"
	"github.com/pjerra/forever-agent/internal/privfile"
	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/slotdir"
)

// ErrReconnecting is a call the daemon answered with -32005 past the retry
// budget: its world link is down and being logged in again; the call did
// nothing and nothing was queued. Wait and call again. The error is an *Error
// (errors.Is matches this value, CodeOf gives -32005).
var ErrReconnecting = errors.New("serveclient: reconnecting")

// ErrDisconnected is a call the daemon answered with -32002: the session is
// gone (a spent reconnect budget), so nothing more goes out. The error is an
// *Error (errors.Is matches this value, CodeOf gives -32002).
var ErrDisconnected = errors.New("serveclient: disconnected")

// ErrUnauthorized is a call the daemon refused with HTTP 401: the token file
// no longer matches the daemon (it restarted) or the daemon wants a token the
// client has none for.
var ErrUnauthorized = errors.New("serveclient: unauthorized: the token does not match the daemon (restarted?)")

// ErrNotLoopback is an address New refuses: the client talks to a loopback
// daemon only, so a bearer token never leaves the machine.
var ErrNotLoopback = errors.New("serveclient: the daemon address must be loopback (127.0.0.0/8, ::1 or localhost)")

// ErrRequestTooLarge is a request over the daemon's 64 KB body cap, refused
// before it is sent.
var ErrRequestTooLarge = errors.New("serveclient: the request is over the daemon's 64 KB body limit")

// maxRequestBytes is the daemon's request body cap (rpc.Server).
const maxRequestBytes = 64 << 10

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

// Is makes errors.Is(err, ErrReconnecting) and (err, ErrDisconnected) match
// the daemon's -32005 and -32002 while the error keeps its code.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrReconnecting:
		return e.Code == rpc.CodeReconnecting
	case ErrDisconnected:
		return e.Code == rpc.CodeDisconnected
	}
	return false
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
	// MountDisplayID is the unit's own UNIT_FIELD_MOUNTDISPLAYID: non-zero while it is mounted.
	MountDisplayID int32 `json:"mount_display_id,omitempty"`
}

// Path is path_to/walk_path's answer: the session's route on its current map (serve's ServePath).
type Path struct {
	Map     uint32       `json:"map"`
	From    [3]float32   `json:"from"`
	To      [3]float32   `json:"to"`
	Status  string       `json:"status"` // complete, partial or none (navmesh.RouteKind)
	Reason  string       `json:"reason,omitempty"`
	Points  [][3]float32 `json:"points"`
	Corners int          `json:"corners"`
	Length  float32      `json:"length"`
}

// PathTo asks the server's route planner for a route from the session's position to x, y, z on its
// current map. It needs the daemon's navmesh folder (a daemon started without one answers a typed
// not-configured error). An unreachable goal is a Path whose Status is partial or none, not an error.
func (c *Client) PathTo(ctx context.Context, x, y, z float32) (Path, error) {
	var p Path
	err := c.call(ctx, "path_to", map[string]float32{"x": x, "y": y, "z": z}, &p, defaultTimeout)
	return p, err
}

// WalkPath plans the same route and has the session walk it as one continuous movement; it returns
// when the walk ends (the daemon's own long deadline is bounded here at five minutes).
func (c *Client) WalkPath(ctx context.Context, x, y, z float32) (Path, error) {
	var p Path
	err := c.call(ctx, "walk_path", map[string]float32{"x": x, "y": y, "z": z}, &p, 5*time.Minute)
	return p, err
}

// WalkRay is the walk_ray method's answer (serve's ServeWalkRay): whether a point is in line of sight, judged on the navmesh's
// walkability ray. Known false means the mesh cannot say, and In is then meaningless.
type WalkRay struct {
	Map    uint32     `json:"map"`
	From   [3]float32 `json:"from"`
	To     [3]float32 `json:"to"`
	Known  bool       `json:"known"`
	In     bool       `json:"in"`
	Hit    float32    `json:"hit,omitempty"`
	Source string     `json:"source"`
}

// WalkRay asks the navmesh walkability ray from the session's position, or from from when it is not nil, to x, y, z: In is
// true when the straight line stays on ground the mesh holds. It is not true line of sight (no collision geometry). It needs the
// daemon's navmesh folder like PathTo.
func (c *Client) WalkRay(ctx context.Context, from *[3]float32, x, y, z float32) (WalkRay, error) {
	var l WalkRay
	params := struct {
		X    float32     `json:"x"`
		Y    float32     `json:"y"`
		Z    float32     `json:"z"`
		From *[3]float32 `json:"from,omitempty"`
	}{x, y, z, from}
	err := c.call(ctx, "walk_ray", params, &l, defaultTimeout)
	return l, err
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

// PartyMark is one raid target icon the group holds: the icon's name and the unit it is on. It is one row of
// Party.Marks.
type PartyMark struct {
	Icon string `json:"icon"`
	GUID GUID   `json:"guid"`
}

// Party is the party method's result.
type Party struct {
	InGroup       bool          `json:"in_group"`
	Leader        GUID          `json:"leader,omitempty"`
	Members       []PartyMember `json:"members"`
	PendingInvite string        `json:"pending_invite,omitempty"`
	// The group's loot settings from the last roster that carried them, and the raid target icons it holds.
	LootMethod    string      `json:"loot_method,omitempty"`
	LootThreshold int         `json:"loot_threshold,omitempty"`
	LootMaster    GUID        `json:"loot_master,omitempty"`
	Marks         []PartyMark `json:"marks,omitempty"`
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

// EventLogPage is one event_log answer: the rows above since, oldest first.
// Dropped is how many events fell out of the ring above since, More says
// another page follows. Epoch names the log's life (a daemon restart is a new
// log) and Reset says the cursor was above the log's seq, so the page starts at
// the beginning of the new log.
type EventLogPage struct {
	Events  []Event `json:"events"`
	Next    uint64  `json:"next"`
	More    bool    `json:"more"`
	Dropped int     `json:"dropped"`
	Epoch   string  `json:"epoch"`
	Reset   bool    `json:"reset,omitempty"`
}

// CombatActor is one side of a combat row, as the daemon's events method
// encodes it: the guid and the name the client knows for the unit.
type CombatActor struct {
	GUID GUID   `json:"guid"`
	Name string `json:"name"`
}

// CombatRow is one row of the daemon's combat log (events): Time is the wall
// clock text with milliseconds and its offset, MonoMS the process's steady
// axis. Reason is set on a spell_fail, a miss, a resist, a melee row that did
// not land normally and an environmental row; SpellName is the server's own name for SpellID when the daemon
// resolved it (empty otherwise; SpellID is the fallback); the interrupt, absorb, dispel
// and sample fields are the core-gains round's.
type CombatRow struct {
	Seq                int            `json:"seq"`
	Kind               string         `json:"kind"`
	Time               string         `json:"time"`
	MonoMS             int64          `json:"mono_ms"`
	Source             CombatActor    `json:"source"`
	Target             CombatActor    `json:"target"`
	SpellID            int32          `json:"spell_id"`
	Amount             int32          `json:"amount"`
	Reason             string         `json:"reason,omitempty"`
	InterruptedSpellID int32          `json:"interrupted_spell_id,omitempty"`
	AbsorbedSpellID    int32          `json:"absorbed_spell_id,omitempty"`
	Dispelled          []CombatDispel `json:"dispelled,omitempty"`
	Sample             *CombatSample  `json:"sample,omitempty"`
	RemainingMS        int32          `json:"remaining_ms,omitempty"`
	SpellName          string         `json:"spell_name,omitempty"`
	Pos                *CombatPoint   `json:"pos,omitempty"`
	Self               bool           `json:"self,omitempty"`
	HorzSpeed          float32        `json:"horz_speed,omitempty"`
	VertSpeed          float32        `json:"vert_speed,omitempty"`
	DirX               float32        `json:"dir_x,omitempty"`
	DirY               float32        `json:"dir_y,omitempty"`
	AirMS              int32          `json:"air_ms,omitempty"`
	Drop               float32        `json:"drop,omitempty"`
	// Simulated marks a self landing the server never stated: its position is the client's flat arc, not a fact.
	Simulated bool `json:"simulated,omitempty"`
}

// CombatPoint is a knockback row's takeoff or landing position.
type CombatPoint struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// CombatDispel is one aura a dispel row removed.
type CombatDispel struct {
	SpellID int32 `json:"spell_id"`
	Harmful bool  `json:"harmful,omitempty"`
}

// CombatSample is the advanced combat log's unit block: the unit's health and
// current power (and which bar the power is).
type CombatSample struct {
	Health    int64 `json:"health"`
	Power     int32 `json:"power"`
	PowerType int8  `json:"power_type"`
}

// EventsPage is one events answer: the rows above since, oldest first, and
// the ring's bounds. Dropped is how many rows fell out of the ring above
// since.
type EventsPage struct {
	Events  []CombatRow `json:"events"`
	Next    int         `json:"next"`
	Oldest  int         `json:"oldest"`
	Dropped int         `json:"dropped"`
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

// CommanderResult is the commander method's answer, in the daemon's own shape (world.ServeCommander): the lines
// that went out, the module's per-bot lines, the binding card of a mutation's own read or every card of a status,
// the waited order states ("ok" or "fail:<code>" per bot) and a run verb's records. A refusal is the RPC error
// instead, whose Data carries the same shape.
type CommanderResult struct {
	Self       string            `json:"self,omitempty"`
	Sent       []string          `json:"sent,omitempty"`
	Bots       []CommanderBot    `json:"bots,omitempty"`
	Place      *CommanderPlace   `json:"place,omitempty"`
	Card       *CommanderCard    `json:"card,omitempty"`
	Cards      []CommanderCard   `json:"cards,omitempty"`
	Final      map[string]string `json:"final"`
	PartyError string            `json:"party_error,omitempty"`
	Notes      []string          `json:"notes,omitempty"`
}

// CommanderBot is one bot's line of a commander answer.
type CommanderBot struct {
	Bot    string `json:"bot"`
	Status string `json:"status"` // queued, refused, stored, bound, joined
	Seq    int    `json:"seq,omitempty"`
	Error  string `json:"error,omitempty"`
	Msg    string `json:"msg,omitempty"`
}

// CommanderPlace is a placement answer: the head in snake_case and the rows.
// A number the module did not send is absent, never zero.
type CommanderPlace struct {
	Verb      string              `json:"verb"`
	Status    string              `json:"status"`
	Follow    bool                `json:"follow,omitempty"`
	Shape     string              `json:"shape,omitempty"`
	Gap       string              `json:"gap,omitempty"`
	Anchor    string              `json:"anchor,omitempty"`
	AnchorBot string              `json:"anchor_bot,omitempty"`
	Face      *int                `json:"face,omitempty"`
	Radius    *float64            `json:"radius,omitempty"`
	Slots     *int                `json:"slots,omitempty"`
	At        *CommanderPos       `json:"at,omitempty"`
	Rally     *CommanderPos       `json:"rally,omitempty"`
	Map       *uint32             `json:"map,omitempty"`
	Inst      *uint32             `json:"inst,omitempty"`
	Set       *bool               `json:"set,omitempty"`
	Cleared   *bool               `json:"cleared,omitempty"`
	From      string              `json:"from,omitempty"`
	Lead      string              `json:"lead,omitempty"`
	Placed    int                 `json:"placed"`
	Released  int                 `json:"released"`
	Refused   int                 `json:"refused"`
	Bots      []CommanderPlaceBot `json:"bots,omitempty"`
}

// CommanderPlaceBot is one bot row of a placement answer, the daemon's
// ServeCommanderBot2: the refusal key is code=, and a queued row carries the
// slot, its position, the sequence and any nudge.
type CommanderPlaceBot struct {
	Bot    string        `json:"bot"`
	Role   string        `json:"role,omitempty"`
	Status string        `json:"status"`
	Slot   *int          `json:"slot,omitempty"`
	Pos    *CommanderPos `json:"pos,omitempty"`
	Seq    *int          `json:"seq,omitempty"`
	Code   string        `json:"code,omitempty"`
	Msg    string        `json:"msg,omitempty"`
	Nudged *float64      `json:"nudged,omitempty"`
	To     string        `json:"to,omitempty"`
}

// CommanderPos is a map position.
type CommanderPos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// CommanderMember is one bound bot of a card; the lead comes first.
type CommanderMember struct {
	Bot       string        `json:"bot"`
	GUID      string        `json:"guid"`
	Class     string        `json:"class"`
	Role      string        `json:"role"`
	Level     int           `json:"level"`
	HP        int64         `json:"hp"`
	MaxHP     int64         `json:"max_hp"`
	Power     int64         `json:"power"`
	MaxPower  int64         `json:"max_power"`
	PowerType string        `json:"power_type,omitempty"`
	Alive     bool          `json:"alive"`
	Lead      bool          `json:"lead"`
	Map       uint32        `json:"map"`
	Zone      int           `json:"zone"`
	Pos       *CommanderPos `json:"pos,omitempty"` // nil: the member has no position
	Hold      string        `json:"hold,omitempty"`
	Slot      *CommanderPos `json:"slot,omitempty"`
}

// CommanderRun is the run record of a card.
type CommanderRun struct {
	Lead    string `json:"lead"`
	Plan    string `json:"plan"`
	State   string `json:"state"`
	Step    int    `json:"step"`
	Steps   int    `json:"steps"`
	Leg     *int   `json:"leg,omitempty"`
	LegName string `json:"leg_name,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Note    string `json:"note,omitempty"`
	Elapsed int    `json:"elapsed"`
	Deaths  int    `json:"deaths"`
	Wipes   int    `json:"wipes"`
}

// CommanderCard is one commander's binding card.
type CommanderCard struct {
	Cmd       string            `json:"cmd"`
	Online    bool              `json:"online"`
	Lead      string            `json:"lead"`
	Bound     int               `json:"bound"`
	Bots      int               `json:"bots"`
	InParty   bool              `json:"in_party"`
	Map       *uint32           `json:"map,omitempty"`
	Pos       *CommanderPos     `json:"pos,omitempty"`
	Hold      string            `json:"hold,omitempty"`
	Rally     *CommanderPos     `json:"rally,omitempty"`
	RallyMap  *uint32           `json:"rally_map,omitempty"`
	RallyInst *uint32           `json:"rally_inst,omitempty"`
	Members   []CommanderMember `json:"members,omitempty"`
	Run       *CommanderRun     `json:"run,omitempty"`
	Log       []CommanderOrder  `json:"log,omitempty"`
}

// CommanderOrder is one logged order of a card, oldest first: the module's own words. OK says the order queued.
type CommanderOrder struct {
	At     int    `json:"at"`
	Verb   string `json:"verb"`
	Detail string `json:"detail,omitempty"`
	OK     bool   `json:"ok"`
	Result string `json:"result,omitempty"`
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
// one. Failed counts the failed steps, DryRun says nothing was sent.
type Teardown struct {
	Steps     []TeardownStep `json:"steps"`
	Failed    int            `json:"failed,omitempty"`
	Seconds   float64        `json:"seconds,omitempty"`
	Character string         `json:"character,omitempty"`
	DryRun    bool           `json:"dry_run,omitempty"`
}

// Client is a typed client of one serve daemon.
type Client struct {
	addr  string       // the daemon's http://host:port/rpc URL
	token string       // empty: the daemon runs without a bearer token
	hc    *http.Client // one per Client; never follows a redirect, so the header stays on addr
}

// Token is the bearer token the client was opened with ("" when the daemon
// runs with authentication off), so a caller can redact a reply that echoes
// it.
func (c *Client) Token() string { return c.token }

func newHTTPClient() *http.Client {
	return &http.Client{
		// No environment proxy: the token goes straight to the loopback daemon.
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// New opens the serve daemon at addr (host:port, scheme optional) with the
// bearer token in tokenFile. Only loopback addresses are accepted (127.0.0.0/8,
// ::1, localhost); anything else is ErrNotLoopback. An empty tokenFile means
// the daemon runs with authentication off and calls go out without a header.
// The token file must hold exactly the token the daemon generated, never a
// stray file.
func New(addr, tokenFile string) (*Client, error) {
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
	addr = strings.TrimSuffix(addr, "/rpc")
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("serveclient: bad address %q: %v", addr, err)
	}
	if !loopbackHost(host) {
		return nil, fmt.Errorf("%w: got %q", ErrNotLoopback, host)
	}
	if host == "localhost" {
		host = "127.0.0.1" // dial the literal: a name is resolved again at every dial
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("serveclient: bad port %q", port)
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
	return &Client{addr: "http://" + net.JoinHostPort(host, port) + "/rpc", token: token, hc: newHTTPClient()}, nil
}

// loopbackHost reports whether host is "localhost" or an IP literal in
// 127.0.0.0/8 or ::1. Any other name is refused without a lookup, so a
// hostname that resolves elsewhere never gets the token; New dials "localhost"
// as 127.0.0.1.
func loopbackHost(host string) bool {
	if _, _, err := netutil.CheckLoopbackAddr(net.JoinHostPort(host, "1")); err != nil {
		return false
	}
	return true
}

// Open opens the serve daemon behind a serve --dir. It runs the same slot.json
// checks the rpc command runs (internal/slotdir): server, slot, holder and
// port present, the dir the one slot.json names, every path inside the dir and
// no symlink. The token is read only from <dir>/rpc-<port>.token. Without a
// header only when slot.json says no_auth (a daemon started with --no-auth,
// which writes no token file); a dir that does not say so and has no token
// file is an error (a deleted token is never hidden), and no_auth next to a
// token file is refused as ambiguous. A no_auth dir whose daemon wants a token
// anyway answers ErrUnauthorized.
func Open(dir string) (*Client, error) {
	abs, manifest, err := slotdir.Read(dir)
	if err != nil {
		if errors.Is(err, slotdir.ErrUnreadable) {
			return nil, fmt.Errorf("serveclient: %s/slot.json missing or unreadable: not a serve dir", abs)
		}
		return nil, fmt.Errorf("serveclient: %v", err)
	}
	if err := slotdir.Check("serveclient", abs, manifest); err != nil {
		return nil, err
	}
	tokenFile := slotdir.TokenPath(abs, manifest.Port)
	_, statErr := os.Lstat(tokenFile)
	switch {
	case manifest.NoAuth && statErr == nil:
		return nil, fmt.Errorf("serveclient: slot.json says no_auth but %s exists: refusing to guess", tokenFile)
	case manifest.NoAuth && errors.Is(statErr, os.ErrNotExist):
		tokenFile = ""
	case errors.Is(statErr, os.ErrNotExist):
		return nil, fmt.Errorf("serveclient: token file %s is missing and slot.json does not say no_auth: not sending unauthenticated requests", tokenFile)
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
					return &Error{Code: rpcErr.Code, Method: method,
						Message: fmt.Sprintf("still reconnecting after %s: %s", retryWindow, rpcErr.Message)}
				}
				select {
				case <-ctx.Done():
					return fmt.Errorf("serveclient: %s: %w", method, ctx.Err())
				case <-time.After(retryEvery):
				}
				continue
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
	reqBody := map[string]any{"jsonrpc": "2.0", "id": requestID, "method": method}
	if params != nil {
		reqBody["params"] = params
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxRequestBytes {
		return nil, nil, fmt.Errorf("%w (%d bytes)", ErrRequestTooLarge, len(body))
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.addr, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	// Reading the (bounded) rest of the body lets the connection be reused.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, nil, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	if err != nil {
		return nil, nil, err
	}
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *rpcWireError   `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, nil, fmt.Errorf("bad reply: %v", err)
	}
	if reply.JSONRPC != "2.0" {
		return nil, nil, fmt.Errorf("bad reply: not a JSON-RPC 2.0 envelope")
	}
	hasResult := len(reply.Result) > 0
	if hasResult == (reply.Error != nil) {
		return nil, nil, fmt.Errorf("bad reply: want exactly one of result and error")
	}
	// A request the daemon could not parse is answered with id null (a parse
	// or invalid-request error); every other reply carries our id.
	if id := strings.TrimSpace(string(reply.ID)); id != strconv.Itoa(requestID) &&
		!(reply.Error != nil && (id == "null" || id == "")) {
		return nil, nil, fmt.Errorf("bad reply: id %s is not the request's %d", id, requestID)
	}
	if reply.Error != nil {
		return nil, reply.Error, nil
	}
	return reply.Result, nil, nil
}

// requestID is the id of every request: a client sends one call at a time
// per connection, so one id is enough, and a reply is checked against it.
const requestID = 1

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
	err := c.call(ctx, "unit", struct {
		GUID GUID   `json:"guid,omitempty"`
		Name string `json:"name,omitempty"`
	}{GUID(guid), name}, &u, defaultTimeout)
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

// Events drains one page of the daemon's combat log (the events method):
// the rows above since, oldest first. limit 0 is the daemon's page maximum
// (1000); waitMS 0..2000 parks the call until a row arrives or the wait ends.
func (c *Client) Events(ctx context.Context, since, limit, waitMS int) (EventsPage, error) {
	if since < 0 {
		since = 0
	}
	if limit < 0 || limit > 1000 {
		limit = 0
	}
	waitMS = min(max(waitMS, 0), 2000)
	var page EventsPage
	err := c.call(ctx, "events", map[string]any{"since": since, "limit": limit, "wait_ms": waitMS},
		&page, time.Duration(waitMS)*time.Millisecond+20*time.Second)
	return page, err
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

// Teardown ends the game presence best-effort, step by step (the follow loop, then gm off, on or keep for the GM
// line, then the logout); the daemon stops. Every step runs even after a failed one.
func (c *Client) Teardown(ctx context.Context, gm string) (Teardown, error) {
	var res Teardown
	err := c.call(ctx, "teardown", map[string]string{"gm": gm}, &res, 120*time.Second)
	return res, err
}
