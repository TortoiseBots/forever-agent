// Package botcmd is the typed layer over the bot module's player commands: the `.bot` lines a player says and the
// system chat lines the module answers with (contract bot-commands v1, contracts/bot-commands/v1).
//
// The grammar is human text, not the key=value reports of `.tb`: a reply is `TortoiseBots: <verb> <Bot>: ok <text>`
// or `...: refused <code>: <text>`, and `who` is a header plus one row per bot. Nothing here acts on a reply that
// does not name the verb and the bot asked for, and the core's own unknown-command line is read as "no module",
// never as an answer.
package botcmd

import (
	"context"
	"errors"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// Prefix starts every line of a v1 reply.
const Prefix = "TortoiseBots: "

// Verb is one request verb. The set is closed: the module answers anything else refused unknown-verb.
type Verb string

// The v1 verbs, in the contract's order.
const (
	VerbHelp   Verb = "help"
	VerbWho    Verb = "who"
	VerbAdd    Verb = "add"
	VerbLogout Verb = "logout"
	VerbFollow Verb = "follow"
	VerbStay   Verb = "stay"
	VerbAttack Verb = "attack"
	VerbFlee   Verb = "flee"
	VerbSummon Verb = "summon"
	VerbStats  Verb = "stats"
)

// Errors this layer adds to the command channel's.
var (
	// ErrNoModule is the core's own unknown-command line: the bot module is not loaded, so nothing was asked of it.
	ErrNoModule = errors.New("botcmd: the server answered the core's unknown-command line (the bot module is not there)")
	// ErrNoReply is a collected answer with no line for the verb and bot asked for.
	ErrNoReply = errors.New("botcmd: the answer holds no line for this verb and bot")
	// ErrRefused is a module refusal; Reply.Err wraps it with the refusal code.
	ErrRefused = errors.New("botcmd: the bot module refused the request")
)

// Reply is one parsed reply line: the verb and bot it names, and either ok with its text or a refusal with its code.
type Reply struct {
	Verb string
	Bot  string
	OK   bool
	Code string
	Text string
}

// WhoRow is one row of a `who` answer.
type WhoRow struct {
	Bot   string
	Level int
	Class string
	Map   uint32
	Move  string
	Mode  string
}

// Runner is what a session offers: one command line, one collected reply. *command.Session implements it.
type Runner interface {
	Run(ctx context.Context, line string, opts command.Options) (*command.Reply, error)
}

// Client is the typed interface over one session's command channel.
type Client struct {
	run  Runner
	Base command.Options
}

// ParseVerb reports whether s is one of the ten v1 verbs.
func ParseVerb(s string) (Verb, bool) { return "", false }

// Line builds one request line.
func Line(v Verb, bot, arg string) (string, error) { return "", nil }

// ParseReply parses one reply line.
func ParseReply(line string) (Reply, bool) { return Reply{}, false }

// Err is nil for an ok reply and ErrRefused wrapped with the code otherwise.
func (r Reply) Err() error { return nil }

// ParseWho parses a `who` answer.
func ParseWho(lines []string) ([]WhoRow, error) { return nil, nil }

// Classify picks the reply line for v and bot.
func Classify(rep *command.Reply, v Verb, bot string) (Reply, error) { return Reply{}, nil }
