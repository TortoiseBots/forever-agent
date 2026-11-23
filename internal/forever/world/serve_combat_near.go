package world

// The combat_near method (Forever #111, agent side): who else hit mobs near a point in the last N seconds. It reads
// the session event log, not a new wire packet: melee, spell damage, periodic and spell-go rows already carry both
// GUIDs and mono_ms (eventlog.go), so a camp's traffic can be counted without asking the server anything.
//
// A row counts when its source is a player GUID that is not this character, its target GUID is a creature the world
// model places within the radius (2D) of the point, and its mono_ms is inside the window. A creature the model does
// not hold (or holds without a position) is counted in Unplaced instead. who=others keeps rows outside the party
// (the default: strangers at the camp); who=party keeps party rows whose source is not this character.

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

const (
	combatNearRadiusDefault float32 = 30
	combatNearRadiusMin     float32 = 5
	combatNearRadiusMax     float32 = 100
	combatNearWindowDefault         = 60
	combatNearWindowMin             = 1
	combatNearWindowMax             = 600
	// combatNearCreature is ObjectGuid's high type for a creature (ObjectGuid.h:118, HighGuid::Creature).
	combatNearCreature = 8
)

type serveCombatNearParams struct {
	X       *float32 `json:"x,omitempty"`
	Y       *float32 `json:"y,omitempty"`
	Z       *float32 `json:"z,omitempty"`
	Radius  *float32 `json:"radius,omitempty"`
	WindowS *int     `json:"window_s,omitempty"`
	Who     string   `json:"who,omitempty"`
}

// ServeCombatNearPlayer is one player's hits in the window.
type ServeCombatNearPlayer struct {
	GUID    GUID   `json:"guid"`
	Name    string `json:"name"`
	Hits    int    `json:"hits"`
	FirstMS int64  `json:"first_ms"`
	LastMS  int64  `json:"last_ms"`
}

// ServeCombatNear is combat_near's answer: the hit rows counted (Count), the players they came from, the distinct
// mobs they landed on, and the rows whose target the model could not place (Unplaced).
type ServeCombatNear struct {
	Count    int                     `json:"count"`
	Players  []ServeCombatNearPlayer `json:"players"`
	Mobs     int                     `json:"mobs"`
	Unplaced int                     `json:"unplaced"`
}

// combatNearKind is the four event kinds a hit row can have (the event log's own names).
func combatNearKind(kind string) bool {
	switch kind {
	case events.KindMelee, events.KindSpellDamage, events.KindPeriodic, events.KindSpellGo:
		return true
	}
	return false
}

// serveCombatNear answers who hit mobs near a point in the last window_s seconds.
func (s *session) serveCombatNear(p serveCombatNearParams) (ServeCombatNear, error) {
	radius := combatNearRadiusDefault
	if p.Radius != nil {
		radius = *p.Radius
		if radius < combatNearRadiusMin || radius > combatNearRadiusMax {
			return ServeCombatNear{}, errors.New("combat_near: radius must be 5..100")
		}
	}
	window := combatNearWindowDefault
	if p.WindowS != nil {
		window = *p.WindowS
		if window < combatNearWindowMin || window > combatNearWindowMax {
			return ServeCombatNear{}, errors.New("combat_near: window_s must be 1..600")
		}
	}
	who := strings.ToLower(strings.TrimSpace(p.Who))
	if who == "" {
		who = "others"
	}
	if who != "others" && who != "party" {
		return ServeCombatNear{}, errors.New(`combat_near: who must be "others" or "party"`)
	}
	if (p.X == nil) != (p.Y == nil) || (p.X == nil) != (p.Z == nil) {
		return ServeCombatNear{}, errors.New("combat_near: x, y and z go together (or none: the agent's position)")
	}
	px, py := s.MoverX(), s.MoverY()
	if p.X != nil {
		px, py = *p.X, *p.Y
	}

	// The window's own clock is mono_ms, the steady axis a stepped wall clock cannot move.
	nowMS := events.MonoMS()
	cutoff := nowMS - int64(window)*1000
	now := s.Now()

	res := ServeCombatNear{Players: []ServeCombatNearPlayer{}}
	index := make(map[wire.GUID]int)
	mobs := make(map[wire.GUID]struct{})
	count := func(e Event) {
		if !combatNearKind(e.Kind) || e.MonoMS < cutoff || e.MonoMS > nowMS {
			return
		}
		src, ok := combatNearParseGUID(e.SourceGUID)
		if !ok || src.Type() != wire.TypePlayer || src == s.self {
			return
		}
		// who=others keeps rows outside the party; who=party keeps the party's rows (the character's own are gone).
		if e.Party != (who == "party") {
			return
		}
		tgt, ok := combatNearParseGUID(e.TargetGUID)
		if !ok || tgt.Type() != combatNearCreature {
			return
		}
		o, ok := s.world.Object(tgt)
		if !ok || !o.Positioned {
			res.Unplaced++
			return
		}
		x, y, _ := o.Position(now)
		// 2D only: a mob on a ledge above the camp is still at the camp (SPEC #111's mutation).
		if objects.Distance(px, py, 0, x, y, 0) > radius {
			return
		}
		res.Count++
		mobs[tgt] = struct{}{}
		i, seen := index[src]
		if !seen {
			name := e.Source
			if name == "" {
				name = s.world.NameOf(src)
			}
			i = len(res.Players)
			index[src] = i
			res.Players = append(res.Players, ServeCombatNearPlayer{
				GUID: GUID(src), Name: name, FirstMS: e.MonoMS, LastMS: e.MonoMS,
			})
		}
		row := &res.Players[i]
		if row.Name == "" {
			row.Name = e.Source
		}
		row.Hits++
		if e.MonoMS < row.FirstMS {
			row.FirstMS = e.MonoMS
		}
		if e.MonoMS > row.LastMS {
			row.LastMS = e.MonoMS
		}
	}
	// The ring holds more rows than one page: walk it page by page instead of copying it whole.
	for seq := uint64(0); ; {
		rows, next, _, more := s.EventLog().Since(seq, EventPageMax)
		for _, e := range rows {
			count(e)
		}
		if !more || len(rows) == 0 || next == seq {
			break
		}
		seq = next
	}
	res.Mobs = len(mobs)
	sort.Slice(res.Players, func(i, j int) bool {
		if res.Players[i].Hits != res.Players[j].Hits {
			return res.Players[i].Hits > res.Players[j].Hits
		}
		return res.Players[i].Name < res.Players[j].Name
	})
	return res, nil
}

// combatNearParseGUID reads an event's GUID text (eventGUID's "0x" and hex form) back to the wire GUID.
func combatNearParseGUID(text string) (wire.GUID, bool) {
	s := strings.TrimPrefix(strings.TrimPrefix(text, "0x"), "0X")
	if len(s) != 32 {
		return wire.GUID{}, false
	}
	high, err := strconv.ParseUint(s[:16], 16, 64)
	if err != nil {
		return wire.GUID{}, false
	}
	low, err := strconv.ParseUint(s[16:], 16, 64)
	if err != nil {
		return wire.GUID{}, false
	}
	return wire.GUID{Low: low, High: high}, true
}

// registerCombatNearMethods wires combat_near into the method table.
func registerCombatNearMethods(srv *rpc.Server, st *serveState) {
	srv.Register("combat_near", serveCallTyped(st, func(s *session, _ context.Context, p serveCombatNearParams) (ServeCombatNear, error) {
		return s.serveCombatNear(p)
	}))
}
