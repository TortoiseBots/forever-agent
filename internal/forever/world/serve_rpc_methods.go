// ServeRPCMethods is the forever serve method table for the `forever rpc`
// client and the docs' method table (docs/forever/METHOD-TABLE.md): the
// implemented names and the refused names. Both lists come from the one
// registration site (registerServeMethods), so no list can drift from the
// socket.
//
// Every old daemon method name this server does not implement answers -32003
// with its documented reason (refusedMethods), never "not ported": a caller
// written for the old daemon learns why the feature has no Forever meaning,
// and what to use instead. The names still marked "not ported" are gone; a
// name is either implemented or refused with a reason here and in the doc
// (TestServeMethodTableDoc pins the two together).
package world

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/rpc"
)

// The reasons a refused name carries. Each is one sentence: what the feature
// was, why it has no Forever meaning, and the command or method that does the
// job. Docs/forever/METHOD-TABLE.md holds the same text, row by row.
const (
	reasonBrainRetired = "the client brain is retired on Forever: the module's bots fight on the server, so a client brain has nothing to drive"
	reasonBrainTuning  = "brain tuning: the client brain is retired on Forever (the module's bots fight on the server), so there is nothing to tune"
	reasonLFG          = "the client dungeon finder is not built on Forever: the module's own finder fills the party and queues (forever fb-dungeon)"
	reasonAddonClick   = "addon_click drives a button of the TortoiseBotsManager panel: the button table is that add-on's own UI, so addon_start runs the add-on's own events and slash commands instead"
	reasonAddonButtons = "the TBM buttons are the add-on's own UI table, so they need that add-on's panel; tbm_handshake and tbm_send speak the protocol without one"
	reasonDungeonStep  = "a module-led run has no client-side step control: the module owns its run (dungeon_pause, dungeon_resume and dungeon_stop are its verbs), and an agent-led run's start step is forever dungeon-run --from-leg/--legs"
	reasonTurtleTT     = "the Turtle test toolkit drives 1.12 test servers; a Forever test setup uses SOAP (forever soap)"
)

// serveRefusal is one old daemon method name and the documented reason it is
// refused.
type serveRefusal struct {
	name string
	why  string
}

// refusedMethods are the old daemon's method names this server does not
// implement, each with its reason. A name here is registered and answers the
// typed refusal; nothing is left "not ported".
var refusedMethods = []serveRefusal{
	// The client brain (rotations, pull tests, tuning): retired on Forever.
	{name: "brain_start", why: reasonBrainRetired},
	{name: "brain_status", why: reasonBrainRetired},
	{name: "brain_stop", why: reasonBrainRetired},
	{name: "tune_list", why: reasonBrainTuning},
	{name: "tune_set", why: reasonBrainTuning},

	// The client dungeon finder: the module has one.
	{name: "lfg_auto", why: reasonLFG},
	{name: "lfg_join", why: reasonLFG},
	{name: "lfg_leave", why: reasonLFG},
	{name: "lfg_listing_role", why: reasonLFG},
	{name: "lfg_listings", why: reasonLFG},
	{name: "lfg_proposal_answer", why: reasonLFG},
	{name: "lfg_role_check_answer", why: reasonLFG},
	{name: "lfg_status", why: reasonLFG},

	// The add-on's own buttons: the host runs the add-on's events and slash commands, not its panel.
	{name: "addon_click", why: reasonAddonClick},
	{name: "tbm_buttons", why: reasonAddonButtons},
	{name: "tbm_click", why: reasonAddonButtons},

	// The module-led run's step control: the module's verbs are pause, resume
	// and stop.
	{name: "dungeon_skip", why: reasonDungeonStep},
	{name: "dungeon_stage", why: reasonDungeonStep},

	{name: "tt", why: reasonTurtleTT},
}

// ServeRefusal returns the documented reason the named method is refused, and
// false for a method this server implements (or does not know).
func ServeRefusal(name string) (why string, refused bool) {
	for _, r := range refusedMethods {
		if r.name == name {
			return r.why, true
		}
	}
	return "", false
}

// ServeRefusalError is the typed answer of a refused method: -32003 with the
// method's name and its documented reason. A caller must be able to tell
// "refused, and why" from "done" and from "no such method".
func ServeRefusalError(name string) error {
	why, refused := ServeRefusal(name)
	if !refused {
		return &rpc.Error{Code: rpc.CodeGame, Message: name + ": not a refused method"}
	}
	return &rpc.Error{Code: rpc.CodeGame, Message: name + ": " + why}
}

// ServeRPCMethods returns the serve method table: the implemented names first,
// the refused names second, each sorted.
func ServeRPCMethods() (ported, refused []string) {
	srv := rpc.NewServer()
	registerServeMethods(srv, &serveState{})
	np := make(map[string]bool, len(refusedMethods))
	for _, r := range refusedMethods {
		np[r.name] = true
	}
	for _, n := range srv.Methods() {
		if np[n] {
			refused = append(refused, n)
		} else {
			ported = append(ported, n)
		}
	}
	sort.Strings(ported)
	sort.Strings(refused)
	return ported, refused
}

// ServeRPCLines is the `forever rpc list` output, built from the same table: an implemented name bare, a refused
// name with its documented reason (`name (refused: <why>)`), one line per method, sorted by name. The one list says
// what the socket does with every old name — the replaced "not ported" mark lived here and said nothing.
func ServeRPCLines() []string {
	ported, refused := ServeRPCMethods()
	lines := make([]string, 0, len(ported)+len(refused))
	lines = append(lines, ported...)
	for _, n := range refused {
		why, _ := ServeRefusal(n)
		lines = append(lines, n+" (refused: "+why+")")
	}
	sort.Slice(lines, func(i, j int) bool { return methodName(lines[i]) < methodName(lines[j]) })
	return lines
}

// methodName is a list line's method name, before the refused mark.
func methodName(line string) string {
	name, _, _ := strings.Cut(line, " ")
	return name
}

// registerRefusedMethods registers every refused name with the one refusal
// shape: the error is the typed answer, never an empty success, and the params
// are not decoded (a caller written for the old daemon sends its own JSON and
// must not get an invalid-params error instead of the reason).
func registerRefusedMethods(srv *rpc.Server) {
	for _, r := range refusedMethods {
		name := r.name
		srv.Register(name, func(context.Context, json.RawMessage) (any, error) {
			return nil, ServeRefusalError(name)
		})
	}
}
