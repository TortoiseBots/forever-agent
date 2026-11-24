// bot_view (#216): a bot's state as the serve already knows it. The old daemon's bot_view read the 1.12 module's
// TBM:VIEW perception feed (a frame per second, with after_n/log_every cursors). The Forever module has no such
// feed, and it needs none: this character is in the same world as its bots, so the serve's own party roster and
// object store already hold what a bot is doing — its health, power, position, target, cast, auras and combat
// state. The method answers that model as one snapshot per call, joined per bot: the roster row (online, dead) and,
// when the unit is in view, the unit row the `unit` method returns.
//
// A bot out of view (another map, or far enough that the server sent no create block) keeps its roster row with
// in_view false: the party frame still knows it is there. A name that is in neither is answered with in_party and
// in_view false — the snapshot never guesses.
package world

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveBotViewWait is the longest wait_ms bot_view honours: the pump slice before the snapshot.
const serveBotViewWait = 2 * time.Second

// serveBotViewParams is bot_view's: the bots to look at (empty: every party member but this character) and how long
// to drive the packet loop before the snapshot, so a unit the server has only just sent is in the model.
type serveBotViewParams struct {
	Bots   []string `json:"bots,omitempty"`
	WaitMS int      `json:"wait_ms,omitempty"`
}

// ServeBotView is bot_view's answer: whether this character is in a party, its own name, and one row per bot.
type ServeBotView struct {
	Party bool              `json:"party"`
	Self  string            `json:"self,omitempty"`
	Bots  []ServeBotViewRow `json:"bots"`
	Notes []string          `json:"notes,omitempty"`
}

// ServeBotViewRow is one bot: the party roster's copy of it and, when the world model holds the unit, the unit row
// itself (the same shape `unit` returns, so its fields are read with the same code).
type ServeBotViewRow struct {
	Name    string     `json:"name"`
	GUID    GUID       `json:"guid"`
	InParty bool       `json:"in_party"`
	Online  bool       `json:"online,omitempty"`
	Dead    bool       `json:"dead,omitempty"`
	InView  bool       `json:"in_view"`
	Unit    *ServeUnit `json:"unit,omitempty"`
}

// registerBotViewMethods wires bot_view.
func registerBotViewMethods(srv *rpc.Server, st *serveState) {
	srv.Register("bot_view", serveCallTyped(st, func(s *session, ctx context.Context, p serveBotViewParams) (ServeBotView, error) {
		return s.serveBotView(ctx, p)
	}))
}

// serveBotView takes the snapshot: the named bots (or the party's, but this character).
func (s *session) serveBotView(ctx context.Context, p serveBotViewParams) (ServeBotView, error) {
	if len(p.Bots) > 32 {
		return ServeBotView{}, errors.New("bot_view: at most 32 names")
	}
	wait := time.Duration(p.WaitMS) * time.Millisecond
	switch {
	case wait < 0:
		wait = 0
	case wait > serveBotViewWait:
		wait = serveBotViewWait
	}
	if wait > 0 {
		if err := s.collect(ctx, wait); err != nil {
			return ServeBotView{}, err
		}
	}
	out := ServeBotView{Bots: []ServeBotViewRow{}}
	if s.party != nil && s.party.InParty && s.party.RosterValid {
		out.Party = true
	}
	if s.report != nil {
		out.Self = s.report.Character.Name
	}
	// The roster's rows, by lowercased name, and every party member but this character in the roster's order.
	type member struct {
		name   string
		guid   GUID
		online bool
		dead   bool
	}
	rows := map[string]member{}
	var order []string
	if out.Party {
		for _, m := range s.party.SnapshotOf() {
			key := strings.ToLower(m.Name)
			rows[key] = member{name: m.Name, guid: GUID(m.GUID), online: m.Connected, dead: m.Full && m.Health == 0}
			if m.GUID != s.party.SelfGUID {
				order = append(order, key)
			}
		}
	}
	var wanted []string
	if len(p.Bots) > 0 {
		seen := map[string]bool{}
		for _, n := range p.Bots {
			name := strings.TrimSpace(n)
			if name == "" {
				return ServeBotView{}, errors.New("bot_view: a bot name is empty")
			}
			key := strings.ToLower(name)
			if seen[key] {
				continue
			}
			seen[key] = true
			wanted = append(wanted, name)
		}
	} else {
		if len(order) == 0 {
			out.Notes = append(out.Notes, "the character is in no party: name the bots to view")
		}
		wanted = order
	}
	now := s.Now()
	for _, name := range wanted {
		key := strings.ToLower(name)
		row := ServeBotViewRow{Name: name}
		if m, ok := rows[key]; ok {
			row.Name, row.GUID, row.InParty, row.Online, row.Dead = m.name, m.guid, true, m.online, m.dead
		}
		var o *objects.Object
		if !wire.GUID(row.GUID).IsZero() {
			if u, ok := s.world.Object(wire.GUID(row.GUID)); ok {
				o = u
			}
		}
		if o == nil && !row.InParty {
			// A named bot the roster does not hold is looked up in the object store by name, the same lookup
			// `unit` and `face` do. A party bot is never matched by name: another unit that happens to share its
			// name is not that bot, so a party row whose own GUID the model does not hold stays in_view false.
			if u, err := s.serveUnitByName(GUID{}, name); err == nil {
				o = u
			}
		}
		if o != nil {
			unit := s.serveUnit(o, now)
			row.InView = true
			row.Unit = &unit
			if wire.GUID(row.GUID).IsZero() {
				row.GUID = unit.GUID
			}
		}
		out.Bots = append(out.Bots, row)
	}
	return out, nil
}
