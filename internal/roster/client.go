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
	"time"

	"github.com/pjerra/forever-agent/internal/tbdash"
)

const (
	// DefaultTimeout bounds one roster read, the login included.
	DefaultTimeout = 15 * time.Second

	botsPath = "/api/v1/bots"
)

// Bot kinds and states, as the dashboard's contract names them.
const (
	KindPool  = "pool"
	KindHired = "hired"

	StateIdle   = "idle"
	StateMoving = "moving"
	StateCombat = "combat"
	StateDead   = "dead"
)

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

// Bot is one logged-in bot, as GET /api/v1/bots lists it. Role, Strategy and XP are not filled
// yet; Role and Strategy decode to "" and XP is not modelled, so either may arrive later
// without a client change. Unknown fields are ignored for the same reason.
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

// Roster is one sample: every logged-in bot and, when the dashboard sends one, its heartbeat.
type Roster struct {
	Bots      []Bot      `json:"bots"`
	Heartbeat *Heartbeat `json:"heartbeat"`
}

// DecodeRoster reads the answer of GET /api/v1/bots. The dashboard may send the roster as a
// bare array or as an object with a bots list and a heartbeat; both are accepted. A bot without
// a name or guid, or with a kind or state outside the contract, is refused: a silent zero would
// make the watch miss a death.
func DecodeRoster(raw []byte) (*Roster, error) {
	body := bytes.TrimSpace(raw)
	if len(body) == 0 {
		return nil, errors.New("empty answer")
	}
	switch body[0] {
	case '[':
		var bots []Bot
		if err := json.Unmarshal(body, &bots); err != nil {
			return nil, fmt.Errorf("bots: %w", err)
		}
		return newRoster(bots, nil)
	case '{':
		var ans struct {
			Bots      []Bot      `json:"bots"`
			Heartbeat *Heartbeat `json:"heartbeat"`
		}
		if err := json.Unmarshal(body, &ans); err != nil {
			return nil, fmt.Errorf("bots: %w", err)
		}
		if ans.Bots == nil {
			return nil, errors.New("the answer has no bots list")
		}
		return newRoster(ans.Bots, ans.Heartbeat)
	default:
		return nil, errors.New("the answer is neither a bots array nor an object")
	}
}

func newRoster(bots []Bot, heartbeat *Heartbeat) (*Roster, error) {
	for i := range bots {
		b := &bots[i]
		if b.Name == "" {
			return nil, fmt.Errorf("bot %d has no name", i)
		}
		if b.GUID == "" {
			return nil, fmt.Errorf("bot %q has no guid", b.Name)
		}
		switch string(b.Kind) {
		case KindPool, KindHired:
		default:
			return nil, fmt.Errorf("bot %q has kind %q, want %s or %s", b.Name, string(b.Kind), KindPool, KindHired)
		}
		switch string(b.State) {
		case StateIdle, StateMoving, StateCombat, StateDead:
		default:
			return nil, fmt.Errorf("bot %q has state %q, want one of %s, %s, %s, %s", b.Name, string(b.State), StateIdle, StateMoving, StateCombat, StateDead)
		}
	}
	return &Roster{Bots: bots, Heartbeat: heartbeat}, nil
}

// Client reads the roster dashboard through a tbdash session.
type Client struct {
	dash *tbdash.Client
}

// New validates the dashboard URL and builds a client. A timeout of zero or less takes
// DefaultTimeout; every request, the login included, is bounded by it.
func New(base, user string, src tbdash.PasswordSource, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	dash, err := tbdash.NewWithSecret(base, user, src)
	if err != nil {
		return nil, err
	}
	dash.SetTimeout(timeout)
	return &Client{dash: dash}, nil
}

// Login logs in once and keeps the session cookie. Roster renews the session by itself when a
// read meets a 401, so a watch only needs this for the first read to fail fast on bad
// credentials.
func (c *Client) Login(ctx context.Context) error { return c.dash.Login(ctx) }

// Roster reads GET /api/v1/bots and decodes it.
func (c *Client) Roster(ctx context.Context) (*Roster, error) {
	raw, err := c.dash.GetJSON(ctx, botsPath, nil)
	if err != nil {
		return nil, err
	}
	r, err := DecodeRoster(raw)
	if err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	return r, nil
}

// RosterSource is one sample of a roster. The watch takes it as a port, so the live watch hands
// in *Client and a test hands in a script.
type RosterSource interface {
	Roster(ctx context.Context) (*Roster, error)
}
