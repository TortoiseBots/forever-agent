// Package cmdctl is the agent's remote control of a TortoiseBots party over the session's own command channel:
// the GM `.tb` lines the session character types, and the grammar-v2 answers the module sends back. One
// controller per session: it binds the party to the session's character, sends orders, starts and reads runs, and
// reads the commander's binding card.
//
// The decisions it implements (FOREVER-AGENT-CONTROL-REPLAY-SPEC section 2, D1-D4):
//
//   - D1: the transport is the session's command channel (`tbcmd`); no add-on bridge.
//   - D2: the session's own character is the commander. Binding names the lead and the character, and every order
//     addresses `party`, which is the bots bound to the sender. Every run mutation, every role and every order is
//     checked against a fresh `.tb commander status <self>` read first, and a target the character does not command
//     is refused with NotOwnedError before any line is sent. Only cards whose commander is the session character
//     count. The client cannot make read and act one step (the module is the authority for GM commands), so the
//     check is check-act-check: the read comes immediately before the line and again right after it, and a target
//     that was not the character's at either read is a NotOwnedError (Sent is true when the line had gone out).
//   - D3: a verb of a module package that may not be installed is asked about once per session (the capability
//     probe) and then refused as not-available; no verb is emulated with another one. Only an explicit answer from
//     the module is remembered: a transport failure, a timeout or a reconnect probes again.
//   - D4: before an order the controller reads the card, and a run that is running or paused refuses the order
//     unless the call sets PauseRun (the module pauses a run on an outside order itself).
//
// An order is sent once: a timeout is reported, never retried, because a resend doubles the order. The controller
// is not safe for concurrent use; a command.Session serialises its runs anyway.
package cmdctl

import (
	"context"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// Controller is the control backend: each call is one or two module commands and a typed answer.
type Controller interface {
	// Bind forms the lead's party from members (when any are given) and binds it to the session's character as
	// its commander. The answer's Card is the binding card afterwards, when the read answers.
	Bind(ctx context.Context, lead string, members []string) (Result, error)
	// Unbind releases the lead's party from this character. It never touches the party itself.
	Unbind(ctx context.Context, lead string) (Result, error)
	// Status reads this character's binding cards and the state of the party its orders address.
	Status(ctx context.Context) (Status, error)
	// Order sends one order (or a role or run verb; see Verbs). An order addresses `party`, the bots bound to this
	// character, and a role names one of them: a call whose target is outside the character's own bindings is
	// refused with NotOwnedError before the line is sent.
	Order(ctx context.Context, o Order) (Result, error)
	// Run sends one `.tb run` verb for a lead: start (arg = plan), stop (arg = reason), pause, resume,
	// status (lead may be "all"; arg empty) or events (arg = a sequence number, or empty). start, stop, pause and
	// resume name a lead this character commands; status and events are read-only and name any lead.
	Run(ctx context.Context, verb, lead, arg string) (Result, error)
	// Available says whether a verb can be sent now, and why not: a cut verb or one whose module package does not
	// answer the probe. An unknown verb and a probe that could not be answered are errors.
	Available(ctx context.Context, verb string) (bool, string, error)
	// Reset drops the answers held for the session: the link or the character was re-entered, so what is known
	// about the module's packages is no longer known.
	Reset()
}

// Order is one order call. Verb is a name of Verbs. Arg is a single argument (`attack <tgt>`); Args is a list for
// the verbs that take one (`move x y z`), and wins when both are set. PauseRun allows the order while a run runs
// on the card (D4). Wait, above zero, polls `.tb state party` for each queued bot's `ord=<seq>:...` and fills
// Result.Final.
type Order struct {
	Verb     string
	Arg      string
	Args     []string
	PauseRun bool
	Wait     time.Duration
}

// args is the argument words the verb gets.
func (o Order) args() []string {
	if len(o.Args) > 0 {
		return o.Args
	}
	if o.Arg != "" {
		return []string{o.Arg}
	}
	return nil
}

// Result is one call's answer: the lines the controller sent, the module's per-bot lines, and whatever the verb
// itself returned.
type Result struct {
	// Sent holds the command lines, in order (a bind sends its party line and then its master line).
	Sent []string
	// Bots holds the module's per-bot lines: an order's or a role's, a bind's joins and binding, an unbind's
	// releases.
	Bots []BotResult
	// Card is the binding card the run guard read (or, after a bind, the card of the lead that was bound).
	Card *tbcmd.Card
	// Final is each waited bot's last order state: "ok" or "fail:<code>". It is nil without a wait, and holds
	// only the bots whose `ord=` matched the sequence their order was queued with.
	Final map[string]string
	// Runs holds a `run` verb's run records.
	Runs []tbcmd.RunState
	// Events holds a `run events` answer's events.
	Events []tbcmd.RunEvent
}

// BotResult is one bot's line of an answer.
type BotResult struct {
	Bot    string
	Status string // queued, refused, stored, bound, joined
	Seq    int
	HasSeq bool
	// Error is the module's refusal code (no-engine, no-pull-strategy, ...), present on a refused line.
	Error    string
	HasError bool
	Msg      string
	HasMsg   bool
}

// Status is one status read. Cards is empty, not an error, when the character commands no bots. Party is the
// party's state, read only when there are cards; PartyError says why it is missing when the read failed.
type Status struct {
	Self       string
	Cards      []tbcmd.Card
	Party      tbcmd.StateReport
	PartyError string
}
