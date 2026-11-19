// Package fbcmd drives the bots of a WoW Forever Bots server: it sends the module's own chat commands through the
// session's command channel, orders one bot by whisper (the bot answers the whisper) or every bot by a party word,
// and reads state only through the module's add-on protocol (package pbc).
//
// Nothing here owns a socket. The session provides a Transport; the world package implements it over the command
// channel, the chat packets, party chat and the add-on inbox. That keeps the command shapes, the reply table and the
// typed model testable without a server, and it keeps the session's one packet loop the only driver of both sides.
package fbcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/pbc"
)

// Defaults. A command's own quiet window is the session's; these bound what the bot's answers may take.
const (
	DefaultOrderWait  = 3 * time.Second
	DefaultAnswerWait = 6 * time.Second
)

// Named errors callers branch on.
var (
	// ErrModuleAbsent is the probe's negative: the command line came back as ordinary chat (the account may say
	// commands but there is no module to run it), or the server does not know the command.
	ErrModuleAbsent = errors.New("fbcmd: the server has no bot module (the command did not run)")
	// ErrRefused is a command the module answered with one of its own refusal lines.
	ErrRefused = errors.New("fbcmd: the bot module refused")
	// ErrNoAck is a command or an order that got no line the client recognises: the module ran but did not answer
	// in a shape this client knows.
	ErrNoAck = errors.New("fbcmd: no answer in a shape this client knows")
	// ErrBadReport is a reply that was answered but whose count or name does not hold together.
	ErrBadReport = errors.New("fbcmd: the answer does not hold together")
	// ErrPBCOff is the add-on protocol after a failure this session: a request got no answer, an answer of an
	// unknown type, or one whose shape does not match. The caller falls back to the chat commands and says so.
	ErrPBCOff = errors.New("fbcmd: the add-on protocol is unavailable")
	// ErrBadAnswer is one malformed, oversized or wrong-type answer. It fails only that request; the session keeps
	// the protocol on, because one bad frame is not proof the route is unusable.
	ErrBadAnswer = errors.New("fbcmd: the answer to that request is unusable")
	// ErrNotProbed is a `.bot` command before this session's probe succeeded.
	ErrNotProbed = errors.New("fbcmd: no successful .bot about probe in this session")
	// ErrNotInParty is a whisper order to a character that is neither in the client's party nor on the
	// protocol's authenticated roster.
	ErrNotInParty = errors.New("fbcmd: the order target is not in the client's party")
	// ErrBotNotFound is the module's id-only detail answer: it does not know that bot.
	ErrBotNotFound = errors.New("fbcmd: the module does not know that bot")
)

// NotFoundError is Client.Detail's typed "the module does not know that bot".
type NotFoundError struct{ ID uint32 }

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s (id %d)", ErrBotNotFound, e.ID)
}

func (e *NotFoundError) Unwrap() error { return ErrBotNotFound }

// RefusalError carries the server's refusal text, already in display form.
type RefusalError struct {
	Err  error
	Text string
}

func (e *RefusalError) Error() string {
	if e.Text == "" {
		return e.Err.Error()
	}
	return e.Err.Error() + ": " + e.Text
}

func (e *RefusalError) Unwrap() error { return e.Err }

// Request is one add-on protocol request. BotID, when set, lets the transport match a push refusal for that bot.
type Request struct {
	Type   string
	Fields []string
	Answer string
	BotID  uint32
	Wait   time.Duration
}

// Answer is a request's reassembled answer.
type Answer struct {
	Type   string
	Fields []string
}

// Transport is what the session offers the client.
type Transport interface {
	// Run sends one chat-command line and collects its answer, exactly as the command channel does. It refuses
	// every `.bot` line but the probe until MarkProbed has been called.
	Run(ctx context.Context, line string, opts command.Options) (*command.Reply, error)
	// MarkProbed records that this session's probe found the module. Only Probe calls it, and only after the
	// probe's marker was read.
	MarkProbed()
	// Whisper says one line to a character and returns the first whisper from that character within window that
	// accept matches. The text is the server's exact words; nil accept takes any whisper from the target.
	Whisper(ctx context.Context, target, text string, window time.Duration, accept func(string) bool) (string, error)
	// PartySay says one line in party chat.
	PartySay(ctx context.Context, text string) error
	// Request sends one add-on protocol request and returns its answer.
	Request(ctx context.Context, req Request) (Answer, error)
}

// Client is the typed interface.
type Client struct {
	tr   Transport
	Base command.Options
	// OrderWait bounds a whisper's acknowledgement; AnswerWait bounds a protocol answer.
	OrderWait  time.Duration
	AnswerWait time.Duration
}

// NewClient returns a client over tr.
func NewClient(tr Transport) *Client {
	return &Client{tr: tr, OrderWait: DefaultOrderWait, AnswerWait: DefaultAnswerWait}
}

// run sends one command line and maps the command channel's named refusals onto this package's.
func (c *Client) run(ctx context.Context, line string) (*command.Reply, error) {
	o := c.Base
	if o.MaxLines < 32 {
		o.MaxLines = 32
	}
	rep, err := c.tr.Run(ctx, line, o)
	var ref *command.RefusalError
	if errors.As(err, &ref) {
		text := chat.Sanitise(ref.Text, 300)
		if errors.Is(err, command.ErrNoPermission) || errors.Is(err, command.ErrUnknownCommand) {
			return rep, fmt.Errorf("%w: %s", ErrModuleAbsent, text)
		}
		return rep, &RefusalError{Err: ErrRefused, Text: text}
	}
	if err != nil {
		return rep, err
	}
	return rep, nil
}

// Probe asks the module for its name and proves it is there. The line is returned in display form. It is the one
// `.bot` line this session may send before the probe succeeds; on a server without the module the line is said
// once as ordinary chat and the say echo (or the unknown command) is the negative, so every later `.bot` command
// is refused with ErrNotProbed.
func (c *Client) Probe(ctx context.Context) (string, error) {
	rep, err := c.run(ctx, ".bot about")
	if err != nil {
		return "", err
	}
	for _, raw := range replyRaw(rep) {
		if strings.Contains(raw, aboutMarker) {
			c.tr.MarkProbed()
			return chat.Sanitise(raw, 300), nil
		}
	}
	return "", fmt.Errorf("%w: the probe was answered, but not with %q", ErrModuleAbsent, aboutMarker)
}

// replyRaw is the lines the client matches on: the exact server text when the collector kept it, else the display
// copy (a test transport's reply).
func replyRaw(rep *command.Reply) []string {
	if rep == nil {
		return nil
	}
	if len(rep.RawText) > 0 {
		return rep.RawText
	}
	return rep.Raw
}

// Language asks the module to answer in word (en, ru or auto).
func (c *Client) Language(ctx context.Context, word string) (string, error) {
	switch strings.ToLower(word) {
	case "en", "ru", "auto":
	default:
		return "", fmt.Errorf("fbcmd: %q is not a language (en, ru, auto)", word)
	}
	rep, err := c.run(ctx, ".bot language "+strings.ToLower(word))
	if err != nil {
		return "", err
	}
	lines := replyRaw(rep)
	if len(lines) == 0 {
		return "", ErrNoAck
	}
	return chat.Sanitise(lines[len(lines)-1], 300), nil
}

// AddResult is what a successful add answered.
type AddResult struct {
	Name string
	Line string
}

// Add asks for one bot. The server creates it, logs it in and groups it itself; the caller waits for the group.
func (c *Client) Add(ctx context.Context, class, role string) (AddResult, error) {
	if err := ValidateClass(class); err != nil {
		return AddResult{}, err
	}
	if err := ValidateRole(role); err != nil {
		return AddResult{}, err
	}
	line := ".bot add " + strings.ToLower(class)
	if role != "" {
		line += " " + strings.ToLower(role)
	}
	rep, err := c.run(ctx, line)
	if err != nil {
		return AddResult{}, err
	}
	lines := replyRaw(rep)
	for _, raw := range lines {
		if text, bad := refusedLine(raw, addRefusals); bad {
			return AddResult{}, &RefusalError{Err: ErrRefused, Text: text}
		}
	}
	for _, raw := range lines {
		if name, ok := onTheWay(raw); ok {
			return AddResult{Name: name, Line: chat.Sanitise(raw, 300)}, nil
		}
	}
	text := ""
	if len(lines) > 0 {
		text = chat.Sanitise(lines[0], 300)
	}
	return AddResult{}, &RefusalError{Err: ErrNoAck, Text: text}
}

// List reads the chat fallback roster. An account with no bots is an empty list, not an error.
func (c *Client) List(ctx context.Context) ([]BotRow, error) {
	rep, err := c.run(ctx, ".bot list")
	if err != nil {
		return nil, err
	}
	lines := replyRaw(rep)
	rows := make([]BotRow, 0, len(lines))
	none := false
	for i, raw := range lines {
		if strings.Contains(raw, "No companions yet") {
			none = true
			continue
		}
		if row, ok := listRow(lines, i); ok {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 && !none {
		return nil, fmt.Errorf("%w: .bot list answered %d line(s), none of them a bot", ErrNoAck, len(lines))
	}
	return rows, nil
}

// Ping makes one protocol round trip; it proves the add-on route before the roster.
func (c *Client) Ping(ctx context.Context) error {
	ans, err := c.tr.Request(ctx, Request{Type: pbc.TypePing, Fields: []string{"0"}, Answer: pbc.TypePong, Wait: c.AnswerWait})
	if err != nil {
		return err
	}
	if len(ans.Fields) == 0 || ans.Fields[0] != "0" {
		return fmt.Errorf("%w: PONG without the echoed value", pbc.ErrProtoMismatch)
	}
	return nil
}

// Roster reads every bot of the account, offline included.
func (c *Client) Roster(ctx context.Context) ([]pbc.RosterEntry, error) {
	ans, err := c.tr.Request(ctx, Request{
		Type: pbc.TypeRosterReq, Fields: pbc.RosterRequest(), Answer: pbc.TypeRosterResp, Wait: c.AnswerWait,
	})
	if err != nil {
		return nil, err
	}
	return pbc.ParseRoster(ans.Fields)
}

// Detail reads one bot's state by its numeric id.
func (c *Client) Detail(ctx context.Context, id uint32) (pbc.BotDetail, error) {
	ans, err := c.tr.Request(ctx, Request{
		Type: pbc.TypeDetailReq, Fields: pbc.DetailRequest(id), Answer: pbc.TypeDetailResp, BotID: id, Wait: c.AnswerWait,
	})
	if err != nil {
		return pbc.BotDetail{}, err
	}
	d, err := pbc.ParseDetail(ans.Fields, id)
	if errors.Is(err, pbc.ErrBotUnknown) {
		return pbc.BotDetail{}, &NotFoundError{ID: id}
	}
	return d, err
}

// Ack is one bot's answer to a whisper order.
type Ack struct {
	Bot     string
	Word    string
	Text    string // display form, without the "<Name>: " prefix
	Refused bool
}

// Order whispers one order to one bot and returns its acknowledgement. An answer the bot refuses comes back with
// Refused set and no error: a refusal is a result.
func (c *Client) Order(ctx context.Context, bot, word string) (Ack, error) {
	name, err := validateName(bot)
	if err != nil {
		return Ack{}, err
	}
	if err := ValidateWord(word); err != nil {
		return Ack{}, err
	}
	// The transport returns the first whisper from the bot that matches this order's confirmation, so a late
	// "follow" acknowledgement is not taken for "stay" and unrelated chatter is ignored.
	text, err := c.tr.Whisper(ctx, name, word, c.OrderWait, func(raw string) bool {
		return ackMatchesOrder(word, acknowledgement(name, raw))
	})
	if err != nil {
		return Ack{}, err
	}
	if strings.TrimSpace(text) == "" {
		return Ack{}, fmt.Errorf("%w: %s answered nothing within %s", ErrNoAck, chat.SanitiseName(name), c.OrderWait)
	}
	body := acknowledgement(name, text)
	ack := Ack{Bot: name, Word: word, Text: chat.Sanitise(body, 300)}
	if t, bad := refusedAck(body); bad {
		ack.Text, ack.Refused = chat.Sanitise(t, 300), true
	}
	return ack, nil
}

// PartyOrder says one order word in party chat. The server applies it to every bot of the sender's in the group
// and answers little or nothing, so the caller confirms through Detail's last rule or a fresh look at the roster.
func (c *Client) PartyOrder(ctx context.Context, word string) error {
	if err := ValidateWord(word); err != nil {
		return err
	}
	return c.tr.PartySay(ctx, word)
}

// DismissResult is what a dismiss answered.
type DismissResult struct {
	Count int
	Line  string
}

// Dismiss logs one bot out and takes it out of the group, or every bot when who is "all".
func (c *Client) Dismiss(ctx context.Context, who string) (DismissResult, error) {
	name := strings.TrimSpace(who)
	if !strings.EqualFold(name, "all") {
		n, err := validateName(name)
		if err != nil {
			return DismissResult{}, err
		}
		name = n
	} else {
		name = "all"
	}
	rep, err := c.run(ctx, ".bot dismiss "+name)
	if err != nil {
		return DismissResult{}, err
	}
	lines := replyRaw(rep)
	for _, raw := range lines {
		if text, bad := refusedLine(raw, dismissRefusals); bad {
			return DismissResult{}, &RefusalError{Err: ErrRefused, Text: text}
		}
	}
	for _, raw := range lines {
		if n, ok := dismissedCount(raw); ok {
			return DismissResult{Count: n, Line: chat.Sanitise(raw, 300)}, nil
		}
	}
	text := ""
	if len(lines) > 0 {
		text = chat.Sanitise(lines[0], 300)
	}
	return DismissResult{}, &RefusalError{Err: ErrNoAck, Text: text}
}
