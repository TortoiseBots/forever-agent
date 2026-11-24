// Package roster is the typed client and the watch rules of a ForeverCore server's roster
// dashboard: who is logged in, the heartbeat, and the changes worth reporting (a death, a bot
// that stopped moving while its state says moving, a lone fight, a bot far from its group).
//
// The HTTP session, the cookie jar and the one re-login a 401 costs are tbdash's; this package
// only adds the typed answer and the rules. The password comes from a tbdash.PasswordSource
// (a private file or an environment variable), never a flag, and no error carries it.
package roster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/tbdash"
)

const (
	// DefaultTimeout bounds one roster read, the login included.
	DefaultTimeout = 15 * time.Second

	botsPath = "/api/v1/bots"
	// statusPath carries the same heartbeat this port keeps off the bots answer (#236 point 2): the module's
	// GET /api/v1/bots is a bare list and its status route holds bots, humans and the state ratios.
	statusPath = "/api/v1/status"
)

// serverStatus is the part of GET /api/v1/status the roster reads. The route also carries freshness and tick lag;
// an unknown key is ignored, so a later field costs nothing. The counts are pointers so a route that answers none of
// them is no heartbeat (another answer on the same path must not print "0 bot(s), 0 human(s)").
type serverStatus struct {
	Bots   *int               `json:"bots"`
	Humans *int               `json:"humans"`
	States map[string]float64 `json:"states"`
}

// heartbeat is the status route's answer as the roster's heartbeat, nil when the answer carries no count.
func (st serverStatus) heartbeat() *Heartbeat {
	if st.Bots == nil && st.Humans == nil {
		return nil
	}
	hb := &Heartbeat{}
	if st.Bots != nil {
		hb.Bots = *st.Bots
	}
	if st.Humans != nil {
		hb.Humans = *st.Humans
	}
	if len(st.States) > 0 {
		hb.States = st.States
	}
	return hb
}

// Bot kinds and states, as the dashboard's contract names them: the port's macro states (busy, stalled, resting) are
// the server's own words for a bot in a scripted block, a stalled movement and a rest, and a bot in one of them is
// still a bot (#236 point 2: the module's BotSnapshot.State names all seven).
const (
	KindPool  = "pool"
	KindHired = "hired"

	StateIdle    = "idle"
	StateMoving  = "moving"
	StateCombat  = "combat"
	StateDead    = "dead"
	StateBusy    = "busy"
	StateStalled = "stalled"
	StateResting = "resting"
)

// botStates is every state a record may carry, for the decoder's message.
var botStates = []string{StateIdle, StateMoving, StateCombat, StateDead, StateBusy, StateStalled, StateResting}

// Text is a dashboard field that may be a JSON string, a number or null: the port spells
// identifiers (a guid, a class id, a power type) either way, and an absent optional field is
// null or missing. A number keeps its decimal text. An object, array or bool is an error: that
// is a type change, not a new field.
type Text string

// UnmarshalJSON accepts a string, a number, or null.
func (t *Text) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return errors.New("empty value")
	}
	if bytes.Equal(b, []byte("null")) {
		*t = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = Text(s)
		return nil
	}
	if b[0] == '-' || (b[0] >= '0' && b[0] <= '9') {
		var n json.Number
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		*t = Text(n.String())
		return nil
	}
	return fmt.Errorf("expected a string or a number, got %s", clip(b))
}

func clip(b []byte) string {
	const max = 32
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}

// Bot is one logged-in bot, as GET /api/v1/bots lists it. The fields the watch rules read
// (hp, max_hp, map, instance, x, y, z, state, and name, guid and kind for identity) must be
// present in the record; DecodeRoster rejects a record without them. Level, power, max_power,
// o, class, zone, target, group, role and strategy are optional. Role and Strategy are not
// filled yet; XP is not modelled. Unknown fields are ignored, so a later field costs nothing.
type Bot struct {
	Name      string  `json:"name"`
	GUID      Text    `json:"guid"`
	Class     Text    `json:"class"`
	Level     int     `json:"level"`
	HP        int     `json:"hp"`
	MaxHP     int     `json:"max_hp"`
	Power     int     `json:"power"`
	MaxPower  int     `json:"max_power"`
	PowerType Text    `json:"power_type"`
	Map       int     `json:"map"`
	Zone      Text    `json:"zone"`
	Instance  Text    `json:"instance"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Z         float64 `json:"z"`
	O         float64 `json:"o"`
	Target    Text    `json:"target"`
	Group     Text    `json:"group"`
	Kind      Text    `json:"kind"`
	State     Text    `json:"state"`
	Role      Text    `json:"role"`
	Strategy  Text    `json:"strategy"`
}

// Position returns the bot's world position.
func (b Bot) Position() (x, y, z float64) { return b.X, b.Y, b.Z }

// Heartbeat is the dashboard's own health: how many bots and humans are logged in and the
// share of the bots in each state. The ratios are whatever the dashboard sends (a fraction or
// a percentage), printed as they arrive.
type Heartbeat struct {
	Bots   int                `json:"bots"`
	Humans int                `json:"humans"`
	States map[string]float64 `json:"states"`
}

// Rejection is one bot record the decoder left out: its index in the answer, the name and guid
// when the record carried readable ones, and why. The name and guid are the record's identity
// for a watch, which must not key a standing warning on an array position. A rejected record
// never reaches the watch rules, so a missing field cannot turn into a death, a stuck flag or a
// distance.
type Rejection struct {
	Index  int
	Name   string
	GUID   Text
	Reason string
}

// Roster is one sample: every logged-in bot and, when the dashboard sends one, its heartbeat.
// Rejected holds the records that were refused and left out of Bots.
type Roster struct {
	Bots      []Bot       `json:"bots"`
	Heartbeat *Heartbeat  `json:"heartbeat"`
	Rejected  []Rejection `json:"-"`
}

// botWire is the presence-tracked form of one bot record. The fields the watch rules read are
// pointers, so a missing key (or a null) is distinguishable from a zero; the truly optional
// fields keep their plain value.
type botWire struct {
	Name      string   `json:"name"`
	GUID      Text     `json:"guid"`
	Class     Text     `json:"class"`
	Level     *int     `json:"level"`
	HP        *int     `json:"hp"`
	MaxHP     *int     `json:"max_hp"`
	Power     *int     `json:"power"`
	MaxPower  *int     `json:"max_power"`
	PowerType Text     `json:"power_type"`
	Map       *int     `json:"map"`
	Zone      Text     `json:"zone"`
	Instance  *Text    `json:"instance"`
	X         *float64 `json:"x"`
	Y         *float64 `json:"y"`
	Z         *float64 `json:"z"`
	O         *float64 `json:"o"`
	Target    Text     `json:"target"`
	Group     Text     `json:"group"`
	Kind      Text     `json:"kind"`
	State     Text     `json:"state"`
	Role      Text     `json:"role"`
	Strategy  Text     `json:"strategy"`
}

// DecodeRoster reads the answer of GET /api/v1/bots. The dashboard may send the roster as a
// bare array or as an object with a bots list and a heartbeat; both are accepted. A record is
// judged on its own: one that is malformed, or that omits or zeroes a field the watch rules
// read, is left out of Bots and described in Rejected, and the other records are kept. The
// answer as a whole is an error only when it cannot be a roster at all.
func DecodeRoster(raw []byte) (*Roster, error) {
	body := bytes.TrimSpace(raw)
	if len(body) == 0 {
		return nil, errors.New("empty answer")
	}
	var records []json.RawMessage
	var heartbeat *Heartbeat
	switch body[0] {
	case '[':
		if err := json.Unmarshal(body, &records); err != nil {
			return nil, fmt.Errorf("bots: %w", err)
		}
	case '{':
		var ans struct {
			Bots      []json.RawMessage `json:"bots"`
			Heartbeat *Heartbeat        `json:"heartbeat"`
		}
		if err := json.Unmarshal(body, &ans); err != nil {
			return nil, fmt.Errorf("bots: %w", err)
		}
		if ans.Bots == nil {
			return nil, errors.New("the answer has no bots list")
		}
		records, heartbeat = ans.Bots, ans.Heartbeat
	default:
		return nil, errors.New("the answer is neither a bots array nor an object")
	}
	r := &Roster{Bots: make([]Bot, 0, len(records)), Heartbeat: heartbeat}
	for i, record := range records {
		b, err := decodeBot(record)
		if err != nil {
			r.Rejected = append(r.Rejected, Rejection{Index: i, Name: recordName(record), GUID: recordGUID(record), Reason: err.Error()})
			continue
		}
		r.Bots = append(r.Bots, b)
	}
	return r, nil
}

// recordName pulls a name out of a record that may otherwise be unusable, for the rejection
// line; it is empty when the name is missing or not a string.
func recordName(raw json.RawMessage) string {
	var probe struct {
		Name json.RawMessage `json:"name"`
	}
	if json.Unmarshal(raw, &probe) != nil || len(probe.Name) == 0 {
		return ""
	}
	var name string
	if json.Unmarshal(probe.Name, &name) != nil {
		return ""
	}
	return name
}

// recordGUID pulls a guid out of a record that may otherwise be unusable, for the rejection
// line and its identity; it is empty when the guid is missing, null, or neither a string nor a
// number.
func recordGUID(raw json.RawMessage) Text {
	var probe struct {
		GUID json.RawMessage `json:"guid"`
	}
	if json.Unmarshal(raw, &probe) != nil || len(probe.GUID) == 0 {
		return ""
	}
	var guid Text
	if json.Unmarshal(probe.GUID, &guid) != nil {
		return ""
	}
	return guid
}

func decodeBot(raw json.RawMessage) (Bot, error) {
	var w botWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return Bot{}, fmt.Errorf("record: %w", err)
	}
	return w.bot()
}

// bot turns one wire record into a Bot, enforcing the fields the rules depend on: a missing
// value is an error, never a zero; hp and max_hp must agree; a live bot must have a maximum;
// the position must be finite.
func (w botWire) bot() (Bot, error) {
	if w.Name == "" {
		return Bot{}, errors.New("name is missing")
	}
	if w.GUID == "" {
		return Bot{}, errors.New("guid is missing")
	}
	if w.Kind == "" {
		return Bot{}, errors.New("kind is missing")
	}
	switch string(w.Kind) {
	case KindPool, KindHired:
	default:
		return Bot{}, fmt.Errorf("kind %q is not %s or %s", string(w.Kind), KindPool, KindHired)
	}
	if w.State == "" {
		return Bot{}, errors.New("state is missing")
	}
	switch string(w.State) {
	case StateIdle, StateMoving, StateCombat, StateDead, StateBusy, StateStalled, StateResting:
	default:
		return Bot{}, fmt.Errorf("state %q is not one of %s", string(w.State), strings.Join(botStates, ", "))
	}
	if w.HP == nil {
		return Bot{}, errors.New("hp is missing")
	}
	if w.MaxHP == nil {
		return Bot{}, errors.New("max_hp is missing")
	}
	if w.Map == nil {
		return Bot{}, errors.New("map is missing")
	}
	if w.Instance == nil {
		return Bot{}, errors.New("instance is missing")
	}
	if w.X == nil {
		return Bot{}, errors.New("x is missing")
	}
	if w.Y == nil {
		return Bot{}, errors.New("y is missing")
	}
	if w.Z == nil {
		return Bot{}, errors.New("z is missing")
	}
	if *w.HP < 0 {
		return Bot{}, errors.New("hp is negative")
	}
	if *w.MaxHP < 0 {
		return Bot{}, errors.New("max_hp is negative")
	}
	if *w.HP > *w.MaxHP {
		return Bot{}, fmt.Errorf("hp %d is over max_hp %d", *w.HP, *w.MaxHP)
	}
	if *w.MaxHP == 0 && w.State != StateDead {
		return Bot{}, errors.New("max_hp is 0 for a live bot")
	}
	if *w.Map < 0 {
		return Bot{}, errors.New("map is negative")
	}
	for _, f := range []struct {
		name string
		v    float64
	}{{"x", *w.X}, {"y", *w.Y}, {"z", *w.Z}} {
		if math.IsNaN(f.v) || math.IsInf(f.v, 0) {
			return Bot{}, fmt.Errorf("%s is not a finite number", f.name)
		}
	}

	b := Bot{
		Name: w.Name, GUID: w.GUID, Class: w.Class,
		HP: *w.HP, MaxHP: *w.MaxHP,
		PowerType: w.PowerType, Map: *w.Map,
		Zone: w.Zone, Instance: *w.Instance,
		X: *w.X, Y: *w.Y, Z: *w.Z,
		Target: w.Target, Group: w.Group, Kind: w.Kind, State: w.State,
		Role: w.Role, Strategy: w.Strategy,
	}
	if w.Level != nil {
		b.Level = *w.Level
	}
	if w.Power != nil {
		b.Power = *w.Power
	}
	if w.MaxPower != nil {
		b.MaxPower = *w.MaxPower
	}
	if w.O != nil {
		b.O = *w.O
	}
	return b, nil
}

// Client reads the roster dashboard through a tbdash session.
type Client struct {
	dash *tbdash.Client
}

// Options are the roster client's settings.
type Options struct {
	// Timeout bounds one request, the login included; zero or less takes DefaultTimeout.
	Timeout time.Duration
	// AllowInsecureHTTP permits a plain-http dashboard that is not on this machine. Off by
	// default; the caller that sets it must warn its operator (the command does).
	AllowInsecureHTTP bool
}

// New validates the dashboard URL and builds a client. A plain-http URL outside loopback is
// refused unless Options.AllowInsecureHTTP is set.
func New(base, user string, src tbdash.PasswordSource, opts Options) (*Client, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	dash, err := tbdash.NewWithOptions(base, user, src, tbdash.Options{AllowInsecureHTTP: opts.AllowInsecureHTTP})
	if err != nil {
		return nil, err
	}
	dash.SetTimeout(opts.Timeout)
	return &Client{dash: dash}, nil
}

// Login logs in once and keeps the session cookie. Roster renews the session by itself when a
// read meets a 401, so a watch only needs this for the first read to fail fast on bad
// credentials.
func (c *Client) Login(ctx context.Context) error { return c.dash.Login(ctx) }

// Roster reads GET /api/v1/bots and decodes it. A dashboard whose bots answer carries no heartbeat is asked
// GET /api/v1/status for one (#236 point 2: this port keeps the heartbeat off the bots list); a status answer that
// is missing or cannot be read leaves the roster without a heartbeat, as a dashboard that sends none always did.
func (c *Client) Roster(ctx context.Context) (*Roster, error) {
	raw, err := c.dash.GetJSON(ctx, botsPath, nil)
	if err != nil {
		return nil, err
	}
	r, err := DecodeRoster(raw)
	if err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	if r.Heartbeat == nil {
		r.Heartbeat = c.statusHeartbeat(ctx)
	}
	return r, nil
}

// statusHeartbeat reads the heartbeat off GET /api/v1/status. It is best effort: the bots roster is the read that
// matters, so a dashboard without the route (or a bad answer there) is a roster without a heartbeat.
func (c *Client) statusHeartbeat(ctx context.Context) *Heartbeat {
	raw, err := c.dash.GetJSON(ctx, statusPath, nil)
	if err != nil {
		return nil
	}
	var st serverStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil
	}
	return st.heartbeat()
}

// RosterSource is one sample of a roster. The watch takes it as a port, so the live watch hands
// in *Client and a test hands in a script.
type RosterSource interface {
	Roster(ctx context.Context) (*Roster, error)
}
