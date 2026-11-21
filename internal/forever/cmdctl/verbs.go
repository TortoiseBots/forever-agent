package cmdctl

// The verb table (spec section 2.3): the one place that says which module line a verb becomes, which module
// package it needs, and whether it is an order (and so waits for the run guard). Adding a module package is a
// table change: the rows point at a new path, never at a second verb.
//
// PathCmd rows go out on `.tb cmd` (rounds C5/C6): the module's one order path, whose answers the typed layer
// parses into the same report shape the slice-3 path uses. A row's Package is what the session's capability probe
// asks about before the first line of that package; a package the module lacks refuses the verb as not-available.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// Path is where a verb's words go.
type Path int

const (
	// PathLocal verbs are the controller's own methods: bind, unbind and status.
	PathLocal Path = iota
	// PathOrder is `.tb order party <verb> [arg]`, the module's slice-3 order path.
	PathOrder
	// PathRole is `.tb role <bot> <role>`.
	PathRole
	// PathRun is `.tb run <verb> ...`.
	PathRun
	// PathCmd is `.tb cmd <verb> ...`, the module's one order path (packages P3 and P6).
	PathCmd
	// PathCut verbs have no line at all: the features the port cut.
	PathCut
)

// ArgKind says what one argument word of a verb is, for the words the controller checks itself before a line goes
// out: a bot name must be one of the session character's own bots, a number must be a number.
type ArgKind int

const (
	// ArgWord is any word of the grammar: a target, a coordinate, a method.
	ArgWord ArgKind = iota
	// ArgBot is the name of a bot this character commands.
	ArgBot
	// ArgBotOrNumber is a bot name, or a decimal number where the module's grammar takes one in that place
	// (loot's itemId, gossip's option index). A number is not a bot name; every other word is.
	ArgBotOrNumber
	// ArgBotOrParty is a bot name, or the word `party` (revive's one form the module carries out).
	ArgBotOrParty
	// ArgNumber is a decimal number (loot's itemId after the bot).
	ArgNumber
)

// Verb is one row of the table. Package names the module package a PathCmd verb needs ("P3" or "P6"). Min and Max
// bound the argument words, taken from the module's own handlers for the PathCmd rows (the test pins each against
// a table copied from the module source); Kinds is what each argument position holds (positions past it are
// ArgWord); Guard marks the order verbs, which read the card first (D4).
type Verb struct {
	Name    string
	Path    Path
	Package string
	Min     int
	Max     int
	Kinds   []ArgKind
	Guard   bool
}

// Verbs is the table. The cut rows are here so that a caller asking about `view` or `scout` gets the port's answer
// instead of an unknown verb.
var Verbs = []Verb{
	// The controller's own calls.
	{Name: "bind", Path: PathLocal},
	{Name: "unbind", Path: PathLocal},
	{Name: "status", Path: PathLocal},

	// Day one: `.tb order party ...` (slice 3).
	{Name: "follow", Path: PathOrder, Min: 0, Max: 1, Guard: true},
	{Name: "stay", Path: PathOrder, Min: 0, Max: 0, Guard: true},
	{Name: "come", Path: PathOrder, Min: 0, Max: 0, Guard: true},
	{Name: "attack", Path: PathOrder, Min: 1, Max: 1, Guard: true},
	{Name: "pull", Path: PathOrder, Min: 1, Max: 1, Guard: true},
	{Name: "endpull", Path: PathOrder, Min: 0, Max: 0, Guard: true},
	{Name: "hold", Path: PathOrder, Min: 0, Max: 0, Guard: true},
	{Name: "free", Path: PathOrder, Min: 0, Max: 0, Guard: true},
	{Name: "assist", Path: PathOrder, Min: 1, Max: 1, Guard: true},
	{Name: "release", Path: PathOrder, Min: 0, Max: 0, Guard: true},

	// Day one: `.tb role <bot> <role>` and the run header.
	{Name: "role", Path: PathRole, Min: 2, Max: 2, Guard: true},
	{Name: "run", Path: PathRun},

	// P3, one order path (the module's `.tb cmd` order grammar, TbRunOrdersPure.h ParseOrder): `move <x> <y> <z>
	// [direct]`, `pullback <guid>`, `focus <guid>`, `rest`, `revive party`.
	{Name: "move", Path: PathCmd, Package: "P3", Min: 3, Max: 4, Guard: true},
	{Name: "pullback", Path: PathCmd, Package: "P3", Min: 1, Max: 1, Guard: true},
	{Name: "focus", Path: PathCmd, Package: "P3", Min: 1, Max: 1, Guard: true},
	{Name: "rest", Path: PathCmd, Package: "P3", Min: 0, Max: 0, Guard: true},
	{Name: "revive", Path: PathCmd, Package: "P3", Min: 1, Max: 1, Kinds: []ArgKind{ArgBotOrParty}, Guard: true},

	// P6, task orders (TbCmdTasks.cpp): `loot <target> [bot] [itemId]`, `open|use <target> [bot]`, `gossip <target>
	// [bot] [option...]` (the module's option is the rest of the line; the table takes up to gossipMaxWords words),
	// `state [bot]`, `lootmethod <ffa|group|rr> [threshold]`.
	{Name: "loot", Path: PathCmd, Package: "P6", Min: 1, Max: 3, Kinds: []ArgKind{ArgWord, ArgBotOrNumber, ArgNumber}, Guard: true},
	{Name: "open", Path: PathCmd, Package: "P6", Min: 1, Max: 2, Kinds: []ArgKind{ArgWord, ArgBot}, Guard: true},
	{Name: "use", Path: PathCmd, Package: "P6", Min: 1, Max: 2, Kinds: []ArgKind{ArgWord, ArgBot}, Guard: true},
	{Name: "gossip", Path: PathCmd, Package: "P6", Min: 1, Max: gossipMaxWords, Kinds: []ArgKind{ArgWord, ArgBotOrNumber}, Guard: true},
	{Name: "state", Path: PathCmd, Package: "P6", Min: 0, Max: 1, Kinds: []ArgKind{ArgBot}, Guard: true},
	{Name: "lootmethod", Path: PathCmd, Package: "P6", Min: 1, Max: 2, Guard: true},

	// PL, the placement package (COMMANDER-SHAPES-SPEC §3.2): the verbs that
	// hold the party in a shape (`place`, `place follow`), remember a
	// fall-back point (`rally`), form up on it (`fallback`) and end the hold
	// (`resume`). Their words are checked by placeArgs, not by Kinds.
	{Name: "place", Path: PathCmd, Package: "PL", Min: 1, Max: 12, Guard: true},
	{Name: "rally", Path: PathCmd, Package: "PL", Min: 0, Max: 1, Guard: false},
	{Name: "fallback", Path: PathCmd, Package: "PL", Min: 0, Max: 1, Guard: true},
	{Name: "resume", Path: PathCmd, Package: "PL", Min: 0, Max: 0, Guard: true},

	// Cut from the port (CPS section 5): no line, no package.
	{Name: "view", Path: PathCut},
	{Name: "scout", Path: PathCut},
	{Name: "ritual", Path: PathCut},
	{Name: "summon", Path: PathCut},
	{Name: "layout", Path: PathCut},
}

// gossipMaxWords caps the words of a gossip call: the target, a bot and an option of up to six words. The module's
// option is the whole rest of its line; the cap is the controller's own.
const gossipMaxWords = 8

// Lookup returns the row for a verb name, ignoring case and surrounding space.
func Lookup(name string) (Verb, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, v := range Verbs {
		if v.Name == name {
			return v, true
		}
	}
	return Verb{}, false
}

// checkArgs enforces the row's argument count and each word's shape before any line is built. The order path's
// words are the module's own grammar (letters, digits and _ . : -); a cmd word may also carry '@' and ',' (an
// object's `entry:<id>@x,y,z`).
func (v Verb) checkArgs(args []string) error {
	if len(args) < v.Min || len(args) > v.Max {
		return &UsageError{Msg: fmt.Sprintf("%s takes %s, not %d", v.Name, v.count(), len(args))}
	}
	extra := ""
	if v.Path == PathCmd {
		extra = "@,"
	}
	for i, a := range args {
		if err := checkWord(a, extra); err != nil {
			return &UsageError{Msg: v.Name + ": " + err.Error()}
		}
		if v.kind(i) == ArgNumber && !isDecimal(a) {
			return &UsageError{Msg: fmt.Sprintf("%s: %q is not a number", v.Name, a)}
		}
	}
	return nil
}

// kind is what argument position i holds.
func (v Verb) kind(i int) ArgKind {
	if i < len(v.Kinds) {
		return v.Kinds[i]
	}
	return ArgWord
}

// botArgs returns the words of args that name a bot, in order: the ones the controller checks against the session
// character's own cards before the line goes out.
func (v Verb) botArgs(args []string) []string {
	var bots []string
	for i, a := range args {
		switch v.kind(i) {
		case ArgBot:
			bots = append(bots, a)
		case ArgBotOrNumber:
			if !isDecimal(a) {
				bots = append(bots, a)
			}
		case ArgBotOrParty:
			if !strings.EqualFold(a, "party") {
				bots = append(bots, a)
			}
		}
	}
	return bots
}

// isDecimal says whether s is a non-empty run of ASCII digits.
func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// count is the row's argument count in words.
func (v Verb) count() string {
	switch {
	case v.Min == v.Max && v.Min == 0:
		return "no argument"
	case v.Min == v.Max:
		return fmt.Sprintf("%d argument(s)", v.Min)
	default:
		return fmt.Sprintf("%d to %d arguments", v.Min, v.Max)
	}
}

// checkWord refuses an argument that could not be one word of the grammar: empty, over 96 bytes, or carrying a
// space, '#' (the token marker), '=' or a character the grammar does not use. extra lists the path's own
// characters, beyond the common set.
func checkWord(s, extra string) error {
	if s == "" {
		return errors.New("an empty argument")
	}
	if len(s) > 96 {
		return fmt.Errorf("argument %d bytes long (at most 96)", len(s))
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_.:-", r):
		case extra != "" && strings.ContainsRune(extra, r):
		default:
			return fmt.Errorf("argument %q has an unexpected character %q", s, r)
		}
	}
	return nil
}

// cmdPath sends `.tb cmd <verb> [args...]`: the module's P3/P6 order path. The typed layer builds the line and
// parses the answer; this type only joins it to the table.
type cmdPath struct{ c *tbcmd.Client }

func (p *cmdPath) Send(ctx context.Context, verb string, args []string) (tbcmd.OrderReport, error) {
	return p.c.Cmd(ctx, verb, args...)
}

// NotAvailableError is a verb the module cannot take now: the package that carries it does not answer the
// session's probe, or the feature was cut from the port.
type NotAvailableError struct {
	Verb    string
	Package string // "P3" or "P6" when the module lacks the package
	Why     string // the reason when the refusal is not a missing package (a cut verb), or why the probe is undecided
	// Retry is true when the probe got no definite answer (a timeout, a bind the module has not seen, an answer
	// that is not the package's): the package is neither known present nor known absent, nothing was cached, and
	// the next call probes again.
	Retry bool
}

func (e *NotAvailableError) Error() string {
	switch {
	case e.Retry:
		return "not-available: " + e.Verb + ": the module's answer to the " + e.Package + " probe is not known (" + e.Why +
			"); nothing was remembered, ask again"
	case e.Package != "":
		return "not-available: " + e.Verb + ": needs module package " + e.Package
	case e.Why != "":
		return "not-available: " + e.Verb + ": " + e.Why
	}
	return "not-available: " + e.Verb
}

// LeadMismatchError is a `.tb cmd` order whose answer names another lead than the one the call addressed: the
// module acts on the caller's own binding and does not take a lead on the wire, so the answer is the only proof of
// which party the line reached. The line already went out; no bot of the answer is reported as queued.
type LeadMismatchError struct {
	// Named is the lead the call addressed. Answered is the lead the module's answer named, empty when it named none.
	Named, Answered string
}

func (e *LeadMismatchError) Error() string {
	got := e.Answered
	if got == "" {
		got = "no lead"
	}
	named := e.Named + "'s"
	if e.Named == "" {
		named = "the card's"
	}
	return "lead-mismatch: the order was meant for " + named + " party and the module answered for " +
		got + "; the line already went through"
}

// NotOwnedError is a mutation whose target is outside the session character's own bindings. The fresh
// `.tb commander status <self>` read named nobody by that name (or named no binding at all), so no command line
// was sent: the module's own check is not the trust boundary.
type NotOwnedError struct {
	// Target is the lead or bot the call named; an order's target is its address, "party".
	Target string
	// Kind is what the target had to be: "lead", "bot" or "order".
	Kind string
	// Sent is true when the check that failed was the read after the line went out: the bind, order, role or run
	// verb already went through. The client cannot make read and act one step (the module is the authority), so
	// this is how a change of ownership between the two shows.
	Sent bool
}

func (e *NotOwnedError) Error() string {
	if e.Kind == "order" {
		return "cmdctl: this character commands no bots: party has no target"
	}
	if e.Sent {
		return "cmdctl: " + e.Target + " was not a " + e.Kind + " this character commands when read after the line was sent; the line already went through"
	}
	return "cmdctl: " + e.Target + " is not a " + e.Kind + " this character commands"
}

// RefusedError is a call the module answered with every line refused: nothing was queued or changed. Bots holds the
// per-bot refusals.
type RefusedError struct {
	Verb string
	Bots []BotResult
	// Reason is the module's own words when it refused the whole call in one record (a `.tb cmd` answer).
	Reason string
}

func (e *RefusedError) Error() string {
	var parts []string
	for _, b := range e.Bots {
		p := b.Bot
		if b.HasError {
			p += ": " + b.Error
		}
		parts = append(parts, p)
	}
	msg := "refused: " + e.Verb + ": nothing was queued"
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	if len(parts) > 0 {
		msg += " (" + strings.Join(parts, ", ") + ")"
	}
	return msg
}

// BindRefusedError is a bind that names a lead or member this character may not bind: a real player or a real
// player's party, a commander, or a bot another commander leads or holds. Refused lists every such name. No line
// was sent.
type BindRefusedError struct {
	Refused []BindRefusal
}

// BindRefusal is one name a bind may not take, and why.
type BindRefusal struct {
	Name string
	Why  string
}

func (e *BindRefusedError) Error() string {
	parts := make([]string, 0, len(e.Refused))
	for _, r := range e.Refused {
		parts = append(parts, r.Name+" cannot be bound: "+r.Why)
	}
	return "cmdctl: " + strings.Join(parts, "; ")
}

// RunActiveError is an order refused because a run runs on the card (D4).
type RunActiveError struct {
	Lead  string
	Plan  string
	State string
}

func (e *RunActiveError) Error() string {
	return "run-active: " + e.Lead + "'s run of " + e.Plan + " is " + e.State +
		"; set pause_run to order while it runs"
}

// UsageError is a call the table or the arguments do not allow: an unknown verb, a wrong argument count, a name
// the server's rule refuses. It is the caller's mistake, not the module's.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return "cmdctl: " + e.Msg }
