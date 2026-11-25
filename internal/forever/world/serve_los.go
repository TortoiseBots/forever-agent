package world

// The los method: the bot module's one-line sight query, over the session's own command channel. It asks the server
// whether its line of sight is clear between two things and how far apart they are, and answers the parsed record.
// The query changes nothing (no move, no selection, no fight state).
//
// The line is built and read by the typed `.tb` client (tbcmd.LOS); nothing here composes a command line, and the
// session's GM guard runs before the line like every other `.tb` line. A refusal the module writes into its answer
// (`no-access`, `not-in-world`, `different-maps`, `rate`, ...) is the method's result with reason set, because a
// polling tool needs the code; a malformed line, and a module that does not answer the command at all, are typed
// errors. A rate refusal is returned as it arrived; nothing retries it here.

import (
	"context"
	"errors"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/sightcmd"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// ServeLOS is the los method's result: the module's answer, one field per key of its line.
type ServeLOS struct {
	// A and B are the sides the module answered, in its own words: the unit's NAME when it resolved one (a guid side
	// included), the two-decimal coordinate text for a spot, or the word it did not resolve.
	A string `json:"a"`
	B string `json:"b"`
	// Map is the map the answer is about; it is absent on a refusal that hides it (no-access).
	Map uint32 `json:"map,omitempty"`
	// Clear is the server's own line of sight (the core check the bots' attack gate uses).
	Clear bool `json:"clear"`
	// DistYD is the yards apart; it is absent on a refusal that states no distance (different-maps, no-access,
	// not-in-world, bad-argument, unknown-unit, rate).
	DistYD *float64 `json:"dist_yd,omitempty"`
	// SameMap is one map and instance for both sides, and InWorld is both in the world. Both read false on a
	// refusal that hid them, so read Reason first.
	SameMap bool `json:"same_map"`
	InWorld bool `json:"in_world"`
	// Reason is the module's refusal code: bad-argument, unknown-unit, not-in-world, different-maps, no-access or
	// rate. It is absent on an answer.
	Reason string `json:"reason,omitempty"`
}

// serveLOSParams is the los method's params: the two sides (a name, "target", a guid as the daemon prints it, or a
// ground spot "x,y,z") and the map two spots need.
type serveLOSParams struct {
	A   string  `json:"a"`
	B   string  `json:"b"`
	Map *uint32 `json:"map,omitempty"`
}

// serveLOS runs one sight query. The two sides and the map-two-spots rule are checked first, with the same readers
// the line builder uses, so a malformed call is a -32602 with nothing sent instead of a module answer problem.
func (st *serveState) serveLOS(ctx context.Context, p serveLOSParams) (ServeLOS, error) {
	sa, err := tbcmd.ParseLOSide("a", p.A)
	if err != nil {
		return ServeLOS{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "los: " + err.Error()}
	}
	sb, err := tbcmd.ParseLOSide("b", p.B)
	if err != nil {
		return ServeLOS{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "los: " + err.Error()}
	}
	if _, err := tbcmd.LOSLine(sa, sb, p.Map); err != nil {
		return ServeLOS{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "los: " + err.Error()}
	}
	client := tbcmd.NewClient(&serveTBRunner{st: st})
	rep, err := client.LOS(ctx, p.A, p.B, p.Map)
	if err != nil {
		return ServeLOS{}, losError(err)
	}
	out := ServeLOS{A: rep.A, B: rep.B, Clear: rep.Clear, SameMap: rep.SameMap, InWorld: rep.InWorld, Reason: rep.Reason}
	if rep.HasMap {
		out.Map = rep.Map
	}
	if rep.HasDist {
		d := rep.DistYD
		out.DistYD = &d
	}
	return out, nil
}

// losError names the two los failures a caller must tell apart from a module answer: a server whose module build has
// no los command at all (a clear "not supported by this module build", which a tool answers by falling back to what
// it had), and an answer that is not a los record (refused, never read as a zero). The texts name no command line: this
// package builds none (dungeon_send_test.go holds that).
func losError(err error) error {
	switch {
	case errors.Is(err, tbcmd.ErrLOSUnsupported):
		return &rpc.Error{Code: rpc.CodeGame,
			Message: "los: not supported by this module build (the server does not answer the los query)"}
	case errors.Is(err, tbcmd.ErrBadReport), errors.Is(err, tbcmd.ErrBadValue):
		return &rpc.Error{Code: rpc.CodeGame, Message: "los: " + sightcmd.NotARecord + ": " + err.Error()}
	}
	return err
}
