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
)

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
	// Run sends one chat-command line and collects its answer, exactly as the command channel does.
	Run(ctx context.Context, line string, opts command.Options) (*command.Reply, error)
	// Whisper says one line to a character and returns the whisper it answered with within window.
	Whisper(ctx context.Context, target, text string, window time.Duration) (string, error)
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

// Probe asks the module for its name and proves it is there. The line is returned in display form.
func (c *Client) Probe(ctx context.Context) (string, error) {
	rep, err := c.run(ctx, ".bot about")
	if err != nil {
		return "", err
	}
	for _, raw := range rep.Raw {
		if strings.Contains(raw, aboutMarker) {
			return chat.Sanitise(raw, 300), nil
		}
	}
	return "", fmt.Errorf("%w: the probe was answered, but not with %q", ErrModuleAbsent, aboutMarker)
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
	if len(rep.Raw) == 0 {
		return "", ErrNoAck
	}
	return chat.Sanitise(rep.Raw[len(rep.Raw)-1], 300), nil
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
	for _, raw := range rep.Raw {
		if text, bad := refusedLine(raw, addRefusals); bad {
			return AddResult{}, &RefusalError{Err: ErrRefused, Text: text}
		}
	}
	for _, raw := range rep.Raw {
		if name, ok := onTheWay(raw); ok {
			return AddResult{Name: name, Line: chat.Sanitise(raw, 300)}, nil
		}
	}
	text := ""
	if len(rep.Raw) > 0 {
		text = chat.Sanitise(rep.Raw[0], 300)
	}
	return AddResult{}, &RefusalError{Err: ErrNoAck, Text: text}
}

// List reads the chat fallback roster. An account with no bots is an empty list, not an error.
func (c *Client) List(ctx context.Context) ([]BotRow, error) {
	rep, err := c.run(ctx, ".bot list")
	if err != nil {
		return nil, err
	}
	rows := make([]BotRow, 0, len(rep.Raw))
	none := false
	for i, raw := range rep.Raw {
		if strings.Contains(raw, "No companions yet") {
			none = true
			continue
		}
		if row, ok := listRow(rep.Raw, i); ok {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 && !none {
		return nil, fmt.Errorf("%w: .bot list answered %d line(s), none of them a bot", ErrNoAck, len(rep.Raw))
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
	return pbc.ParseDetail(ans.Fields, id)
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
	text, err := c.tr.Whisper(ctx, name, word, c.OrderWait)
	if err != nil {
		return Ack{}, err
	}
	if strings.TrimSpace(text) == "" {
		return Ack{}, fmt.Errorf("%w: %s answered nothing within %s", ErrNoAck, chat.SanitiseName(name), c.OrderWait)
	}
	body := acknowledgement(name, chat.Sanitise(text, 300))
	ack := Ack{Bot: name, Word: word, Text: body}
	if t, bad := refusedAck(body); bad {
		ack.Text, ack.Refused = t, true
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
	for _, raw := range rep.Raw {
		if text, bad := refusedLine(raw, dismissRefusals); bad {
			return DismissResult{}, &RefusalError{Err: ErrRefused, Text: text}
		}
	}
	for _, raw := range rep.Raw {
		if n, ok := dismissedCount(raw); ok {
			return DismissResult{Count: n, Line: chat.Sanitise(raw, 300)}, nil
		}
	}
	text := ""
	if len(rep.Raw) > 0 {
		text = chat.Sanitise(rep.Raw[0], 300)
	}
	return DismissResult{}, &RefusalError{Err: ErrNoAck, Text: text}
}
