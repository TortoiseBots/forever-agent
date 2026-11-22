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
	"fmt"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

// whoMaxLines is the collection bound for `.bot who`: the header and a row for every bot an owner can have online.
const whoMaxLines = command.DefaultMaxLines

// Prefix starts every line of a v1 reply.
const Prefix = "TortoiseBots: "

// coreAbsent is the core's own answer to a line no command handles: on a server without the module, `.bot` comes
// back as this, and it is not a reply at all.
const coreAbsent = "There is no such command"

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

// Err is nil for an ok reply; a refusal is ErrRefused wrapped with the code the module gave.
func (r Reply) Err() error {
	if r.OK {
		return nil
	}
	if r.Text != "" {
		return fmt.Errorf("%w: %s: %s", ErrRefused, r.Code, r.Text)
	}
	return fmt.Errorf("%w: %s", ErrRefused, r.Code)
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

// ParseVerb reports whether s is one of the ten v1 verbs.
func ParseVerb(s string) (Verb, bool) {
	switch Verb(s) {
	case VerbHelp, VerbWho, VerbAdd, VerbLogout, VerbFollow, VerbStay, VerbAttack, VerbFlee, VerbSummon, VerbStats:
		return Verb(s), true
	}
	return "", false
}

// Line builds one request line from the verb and the bot. arg is part of the signature for v2's argument verbs;
// no v1 verb takes one, so a non-empty arg is refused as firmly as an unknown verb.
func Line(v Verb, bot, arg string) (string, error) {
	if _, ok := ParseVerb(string(v)); !ok {
		return "", fmt.Errorf("botcmd: %q is not a v1 verb", v)
	}
	line := ".bot " + string(v)
	switch v {
	case VerbHelp, VerbWho:
		if bot != "" || arg != "" {
			return "", fmt.Errorf("botcmd: %s names no bot and takes no argument", v)
		}
	default:
		if arg != "" {
			return "", errors.New("botcmd: no v1 verb takes an argument")
		}
		name, err := chat.NormaliseName(bot)
		if err != nil {
			return "", err
		}
		line += " " + name
	}
	if strings.Contains(line, `\`) {
		return "", errors.New("botcmd: the module's chain separator never appears in a v1 line")
	}
	if err := chat.ValidateCommandText(line); err != nil {
		return "", err
	}
	return line, nil
}

// ParseReply parses one reply line. A line without the module's prefix, the `who` header or row, or a word where
// `ok` or `refused` belongs is not a reply; the caller then keeps looking instead of acting on it.
func ParseReply(line string) (Reply, bool) {
	rest, ok := strings.CutPrefix(line, Prefix)
	if !ok {
		return Reply{}, false
	}
	verb, rest, ok := cutWord(rest)
	if !ok {
		return Reply{}, false
	}
	rep := Reply{}
	if name, found := strings.CutSuffix(verb, ":"); found {
		// The module's refusals that name no bot (usage, unknown-verb, players-off) read `<verb>: refused <code>: <text>`.
		if name == "" || !strings.HasPrefix(rest, "refused ") {
			return Reply{}, false
		}
		rep.Verb = name
	} else {
		namedBot, after, ok := cutWord(rest)
		if !ok || !strings.HasSuffix(namedBot, ":") || len(namedBot) < 2 {
			return Reply{}, false
		}
		rep.Verb, rep.Bot, rest = verb, strings.TrimSuffix(namedBot, ":"), after
	}
	word, rest, _ := cutWord(rest)
	switch word {
	case "ok":
		if rep.Bot == "" {
			return Reply{}, false // an ok always names its bot
		}
		rep.OK, rep.Text = true, rest
		return rep, true
	case "refused":
		code, text, ok := cutWord(rest)
		if !ok || !strings.HasSuffix(code, ":") || len(code) < 2 {
			return Reply{}, false
		}
		rep.Code, rep.Text = strings.TrimSuffix(code, ":"), text
		return rep, true
	}
	return Reply{}, false
}

// cutWord splits the first blank-separated word off s and returns it with the rest of the line. A leading blank is
// not skipped: it would hide a missing field.
func cutWord(s string) (word, rest string, ok bool) {
	if s == "" {
		return "", "", false
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], s[i+1:], s[:i] != ""
	}
	return s, "", true
}

// ParseWho parses a `who` answer: the header's count, then one row per bot. Lines that are not who lines are
// ignored (the collector sees whatever else arrived), but a malformed who line fails the whole answer: a run that
// cannot read the roster must not act on half of it.
func ParseWho(lines []string) ([]WhoRow, error) {
	n := -1
	var rows []WhoRow
	for _, line := range lines {
		field, ok := strings.CutPrefix(line, Prefix+"who: ")
		if !ok {
			continue
		}
		if r, isReply := ParseReply(line); isReply && !r.OK {
			return nil, r.Err() // `who: refused usage: ...`
		}
		if count, ok := parseWhoCount(field); ok {
			if n >= 0 {
				return nil, errors.New("botcmd: the who answer has two headers")
			}
			n = count
			continue
		}
		if n < 0 {
			return nil, fmt.Errorf("botcmd: a who row came before the header (%q)", line)
		}
		row, ok := parseWhoRow(field)
		if !ok {
			return nil, fmt.Errorf("botcmd: who line %q does not fit the row shape", line)
		}
		rows = append(rows, row)
	}
	if n < 0 {
		return nil, fmt.Errorf("%w: no who header", ErrNoReply)
	}
	if n != len(rows) {
		return nil, fmt.Errorf("botcmd: the who header says %d bot(s) and %d rows came", n, len(rows))
	}
	return rows, nil
}

// parseWhoCount reads the header's `<n> bot(s)`.
func parseWhoCount(field string) (int, bool) {
	const suffix = " bot(s)"
	if !strings.HasSuffix(field, suffix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(field, suffix))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// parseWhoRow reads one row as the module prints it: `<Bot> level <n> <Class> map <id> <move> <mode>`. The class is
// the words between the level and `map` (a class name may hold a space); the move and mode words are closed sets.
func parseWhoRow(field string) (WhoRow, bool) {
	words := strings.Fields(field)
	if len(words) < 8 || words[1] != "level" {
		return WhoRow{}, false
	}
	level, err := strconv.Atoi(words[2])
	if err != nil || level < 0 {
		return WhoRow{}, false
	}
	mapAt := len(words) - 4
	if words[mapAt] != "map" || mapAt < 4 {
		return WhoRow{}, false
	}
	id, err := strconv.ParseUint(words[mapAt+1], 10, 32)
	if err != nil {
		return WhoRow{}, false
	}
	move, mode := words[mapAt+2], words[mapAt+3]
	if move != "follow" && move != "stay" && move != "free" {
		return WhoRow{}, false
	}
	if mode != "engine" && mode != "spike" {
		return WhoRow{}, false
	}
	return WhoRow{Bot: words[0], Level: level, Class: strings.Join(words[3:mapAt], " "), Map: uint32(id), Move: move, Mode: mode}, true
}

// Classify picks the answer line for v and bot. The core's unknown-command line is ErrNoModule; no matching line is
// ErrNoReply; a refusal is returned as a Reply with OK false and nil error, so the caller decides whether to call
// Reply.Err. Another bot's or another verb's line is never taken.
func Classify(rep *command.Reply, v Verb, bot string) (Reply, error) {
	if rep == nil {
		return Reply{}, ErrNoReply
	}
	if absent(rep) {
		return Reply{}, ErrNoModule
	}
	for _, line := range raw(rep) {
		r, ok := ParseReply(line)
		if !ok {
			continue
		}
		// A refusal that names no bot answers the verb it names ("bot" when the verb was empty: players-off).
		botless := r.Bot == "" && !r.OK && (r.Verb == string(v) || r.Verb == "bot")
		if !botless && (r.Verb != string(v) || !strings.EqualFold(r.Bot, bot)) {
			continue
		}
		return r, nil
	}
	return Reply{}, ErrNoReply
}

// absent reports the core's own unknown-command answer, in either form: the collector's classification of its
// string-table line, or the line itself.
func absent(rep *command.Reply) bool {
	if errors.Is(rep.Refusal, command.ErrUnknownCommand) {
		return true
	}
	for _, line := range raw(rep) {
		if strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(line), "."), coreAbsent) {
			return true
		}
	}
	return false
}

// raw is the lines matched on: the collector's exact server text when it kept one, else its display copy.
func raw(rep *command.Reply) []string {
	if len(rep.RawText) > 0 {
		return rep.RawText
	}
	return rep.Raw
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

// NewClient wraps s; Base carries the session-wide options (SelfName for the say-echo check, the quiet window).
func NewClient(s *command.Session) *Client { return &Client{run: s} }

// call sends one verb's line and classifies the answer. A refusal is returned as both a Reply and an error.
func (c *Client) call(ctx context.Context, v Verb, bot string) (Reply, error) {
	line, err := Line(v, bot, "")
	if err != nil {
		return Reply{}, err
	}
	rep, err := c.run.Run(ctx, line, c.Base)
	if err != nil {
		return Reply{}, err
	}
	r, err := Classify(rep, v, bot)
	if err != nil {
		return Reply{}, err
	}
	if err := r.Err(); err != nil {
		return r, err
	}
	return r, nil
}

// Add asks for one bot: it logs in and joins the requester's party.
func (c *Client) Add(ctx context.Context, bot string) (Reply, error) {
	return c.call(ctx, VerbAdd, bot)
}

// Logout logs one bot out.
func (c *Client) Logout(ctx context.Context, bot string) (Reply, error) {
	return c.call(ctx, VerbLogout, bot)
}

// Follow orders one bot to follow the requester.
func (c *Client) Follow(ctx context.Context, bot string) (Reply, error) {
	return c.call(ctx, VerbFollow, bot)
}

// Stay orders one bot to stay where it is.
func (c *Client) Stay(ctx context.Context, bot string) (Reply, error) {
	return c.call(ctx, VerbStay, bot)
}

// Attack orders one bot onto the requester's selected unit.
func (c *Client) Attack(ctx context.Context, bot string) (Reply, error) {
	return c.call(ctx, VerbAttack, bot)
}

// Flee orders one bot to leave the fight.
func (c *Client) Flee(ctx context.Context, bot string) (Reply, error) {
	return c.call(ctx, VerbFlee, bot)
}

// Summon moves one bot next to the requester, within the module's cooldown.
func (c *Client) Summon(ctx context.Context, bot string) (Reply, error) {
	return c.call(ctx, VerbSummon, bot)
}

// Stats asks for one bot's stats line.
func (c *Client) Stats(ctx context.Context, bot string) (Reply, error) {
	return c.call(ctx, VerbStats, bot)
}

// Who reads `.bot who`: the header's count and one row per bot. The rows are the roster check of the run's later
// phases, so a malformed answer is an error here rather than an empty list.
func (c *Client) Who(ctx context.Context) ([]WhoRow, error) {
	line, err := Line(VerbWho, "", "")
	if err != nil {
		return nil, err
	}
	o := c.Base
	if o.MaxLines < whoMaxLines {
		o.MaxLines = whoMaxLines // header plus one row per bot: a roster must never be cut short
	}
	rep, err := c.run.Run(ctx, line, o)
	if err != nil {
		return nil, err
	}
	if absent(rep) {
		return nil, ErrNoModule
	}
	return ParseWho(raw(rep))
}
