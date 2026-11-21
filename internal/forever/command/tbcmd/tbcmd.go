// Package tbcmd is the typed layer over a server module that answers `.tb` commands: the orders (follow, stay,
// come, attack, assist, pull, endpull, hold, free), role, master and party, and the read-only state, party and
// threat reports.
//
// Every call builds its line with a fresh token (command.NewToken by default), sends it through a Runner (the
// command collector), requires the command's begin and end markers, and returns a typed answer or the module's
// error line as a *CommandError. Nothing here acts on a report that did not end: a state line without its begin
// and end lines is not a complete report. The reply grammar is version 2 only (v2.go).
package tbcmd

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

// Runner is what a session offers: one command line, one collected reply. *command.Session implements it.
type Runner interface {
	Run(ctx context.Context, line string, opts command.Options) (*command.Reply, error)
}

// Errors the typed layer adds to the collector's.
var (
	// ErrNotRested is WaitRested's timeout: some bot still reports resting=yes.
	ErrNotRested = errors.New("tbcmd: the party did not report rested before the timeout")
	// ErrBadReport is an end line that disagrees with the lines before it (a count that does not match), or a
	// reply whose end line is missing from the collected lines.
	ErrBadReport = errors.New("tbcmd: the report's end line does not match its lines")
	// ErrBadValue is a documented key whose value cannot be read (a number that is not a number).
	ErrBadValue = errors.New("tbcmd: a value does not fit its key")
)

// CommandError is the module's own error line: `error code=<code> cmd=<command> msg=<text>` (one line, no
// begin/end).
type CommandError struct {
	Code   string
	Cmd    string
	Msg    string
	HasMsg bool // msg= was present (an empty msg is not the same as no msg)
}

func (e *CommandError) Error() string {
	var b strings.Builder
	b.WriteString("tb: ")
	b.WriteString(e.Code)
	if e.Cmd != "" {
		b.WriteString(" (")
		b.WriteString(e.Cmd)
		b.WriteString(")")
	}
	if e.Msg != "" {
		b.WriteString(": ")
		b.WriteString(e.Msg)
	}
	return b.String()
}

// Client is the typed interface. Base carries the session-wide options (SelfName for the say-echo check, the
// quiet window of tokenless calls, the clock); Token replaces command.NewToken for tests.
type Client struct {
	run   Runner
	Base  command.Options
	Token func() (string, error)
	// Poll is WaitRested's interval; 0 means 2 s.
	Poll time.Duration
}

// NewClient returns a client over r.
func NewClient(r Runner) *Client { return &Client{run: r} }

// call sends one `.tb` line with a fresh token and requires endKind. The module's error line is returned as a
// *CommandError (the Reply is still returned for the caller's diagnostics).
func (c *Client) call(ctx context.Context, line, endKind string) (*command.Reply, error) {
	return c.callMax(ctx, line, endKind, 0)
}

// callMax is call with the collector's line cap raised (the event ring is 200 lines): max of 0 keeps the floor of 16
// the typed layer has always used.
func (c *Client) callMax(ctx context.Context, line, endKind string, maxLines int) (*command.Reply, error) {
	return c.callBare(ctx, line, endKind, maxLines, "")
}

// callBare is callMax for a module reply whose body rows carry no token (only its end line does): bare names the
// record kind of those rows (command.Options.BareBody).
func (c *Client) callBare(ctx context.Context, line, endKind string, maxLines int, bare string) (*command.Reply, error) {
	tok := c.Token
	if tok == nil {
		tok = command.NewToken
	}
	t, err := tok()
	if err != nil {
		return nil, err
	}
	o := c.Base
	o.Token = t
	o.EndKind = endKind
	o.BareBody = bare
	if maxLines > o.MaxLines {
		o.MaxLines = maxLines
	}
	if o.MaxLines < 16 {
		o.MaxLines = 16
	}
	rep, err := c.run.Run(ctx, line, o)
	if err != nil {
		return rep, err
	}
	if e, ok, err := moduleError(rep); err != nil {
		return rep, err
	} else if ok {
		return rep, e
	}
	return rep, nil
}

// Send is the raw seam: one `.tb` line with a fresh token and the client's base options, the reply's end kind
// required, and the module's error line returned as a *CommandError. The typed methods above cover every command
// the agent acts on; this one exists for a caller that has to find out whether a command exists at all (the
// capability probe of cmdctl), whose answer is the error line of a command the agent does not act on yet.
func (c *Client) Send(ctx context.Context, line, endKind string) (*command.Reply, error) {
	return c.call(ctx, line, endKind)
}

// labelText decodes a structured label (target=party:Tbtank): the parts are split on ':' before decoding, so an
// encoded ':' inside a name stays in the name.
func labelText(rec record, key string) (string, error) {
	parts, err := rec.parts(key)
	if err != nil {
		return "", err
	}
	return strings.Join(parts, ":"), nil
}

// SplitLabel splits a decoded target label: "party:Tbtank" is kind "party" and value "Tbtank"; a bare name has an
// empty kind. Use it on the display text; labelText is the grammar-aware reader.
func SplitLabel(s string) (kind, value string) {
	if i := strings.IndexByte(s, ':'); i > 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// coordinate reads a three-number value (pos=x,y,z): the parts are split on ',' before any is decoded, because a
// value's decimal point and sign are percent-encoded while the separator is the literal grammar's.
func coordinate(rec record, key, raw string) (x, y, z float64, err error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("%w: %s=%q is not x,y,z", ErrBadValue, key, raw)
	}
	out := [3]float64{}
	for i, part := range parts {
		s, err := decodePart(part)
		if err != nil {
			return 0, 0, 0, rec.bad(key, err)
		}
		f, err := rec.floatValue(key, s)
		if err != nil {
			return 0, 0, 0, err
		}
		out[i] = f
	}
	return out[0], out[1], out[2], nil
}

// ---- targets and arguments ----

// targetWords checks the address of a command: a bot's name, "party", or "party <leader>". Names are the server's
// own character-name rule (ASCII letters, at most 12); nothing here can inject a token or another word.
func targetWords(target string) ([]string, error) {
	f := strings.Fields(target)
	switch len(f) {
	case 0:
		return nil, errors.New("tbcmd: empty target")
	case 1:
		if strings.EqualFold(f[0], "party") {
			return []string{"party"}, nil
		}
		name, err := chat.NormaliseName(f[0])
		if err != nil {
			return nil, fmt.Errorf("tbcmd: target: %w", err)
		}
		return []string{name}, nil
	case 2:
		if !strings.EqualFold(f[0], "party") {
			return nil, fmt.Errorf("tbcmd: target %q is not a name, \"party\" or \"party <leader>\"", target)
		}
		name, err := chat.NormaliseName(f[1])
		if err != nil {
			return nil, fmt.Errorf("tbcmd: party leader: %w", err)
		}
		return []string{"party", name}, nil
	}
	return nil, fmt.Errorf("tbcmd: target %q is not a name, \"party\" or \"party <leader>\"", target)
}

// argument checks one argument word. The grammar's words are names, numbers, raid-mark words, "spawn:<id>" and
// GUID hex; the allowed characters cover those and refuse a space, '#' (the token marker) and '='.
func argument(what, s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("tbcmd: %s is empty", what)
	}
	if len(s) > 64 {
		return "", fmt.Errorf("tbcmd: %s is over 64 bytes", what)
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == ':' || r == '-':
		default:
			return "", fmt.Errorf("tbcmd: %s %q has an unexpected character %q", what, s, r)
		}
	}
	return s, nil
}

func (c *Client) orderLine(target, verb, arg string) (string, error) {
	words, err := targetWords(target)
	if err != nil {
		return "", err
	}
	parts := append([]string{".tb", "order"}, words...)
	v, err := argument("verb", verb)
	if err != nil {
		return "", err
	}
	parts = append(parts, v)
	if arg != "" {
		a, err := argument("argument", arg)
		if err != nil {
			return "", err
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " "), nil
}

// ---- orders ----

// OrderResult is one bot's line of an order answer.
type OrderResult struct {
	Bot    string
	Verb   string
	Seq    int
	HasSeq bool
	Status string // queued, refused (a per-bot refusal carries the module's code and message)
	// Code is the refusal code (no-engine, no-pull-strategy, ...): the grammar's per-record refusal key, as the
	// module writes it. HasCode is false on a queued line.
	Code    string
	HasCode bool
	Msg     string
	HasMsg  bool
	// Target is a `.tb cmd` task answer's `target=` (the 32-hex guid the bot was sent to). HasTarget is false on
	// the slice-3 order lines, which carry no target.
	Target    string
	HasTarget bool
	// Extra holds every key this model does not know, decoded. Unknown keys are not an error.
	Extra map[string]string
}

var orderRecordKeys = map[string]bool{"bot": true, "verb": true, "seq": true, "status": true, "code": true, "msg": true}

// OrderReport is a whole order answer: the bot lines and the end line.
type OrderReport struct {
	Results []OrderResult
	Verb    string
	Target  string
	Count   int
	Queued  int
	Refused int
	Raw     []string
	// Lead, Status, Bots and Result are a `.tb cmd` answer's whole-call fields (rounds C5/C6). The cmd path
	// answers one record for the whole party, so Results names only the bots the answer itself accounts for (a
	// task verb's bot, an order the lead's bot executes), each `queued` where the module wrote `accepted`.
	Lead   string // the end line's lead= (an order verb)
	Status string // the cmd record's own word: accepted, ok
	Bots   int    // the bots the answer accounts for (the end line's bots=, else the result records)
	Result string // the order verb's result scope, decoded: "party 5" or "bot:<name> 1"
	// Fields is a whole-call record's keys this model does not type (lootmethod's method= and threshold=).
	Fields map[string]string
}

// Order sends one order. arg may be empty.
func (c *Client) Order(ctx context.Context, target, verb, arg string) (OrderReport, error) {
	line, err := c.orderLine(target, verb, arg)
	if err != nil {
		return OrderReport{}, err
	}
	rep, err := c.call(ctx, line, "order-end")
	if err != nil {
		return OrderReport{}, err
	}
	return parseOrder(rep)
}

func parseOrder(rep *command.Reply) (OrderReport, error) {
	r, err := parseReport(rep, "order-begin", "order-end", "order", "bot")
	if err != nil {
		return OrderReport{}, err
	}
	end := r.End
	out := OrderReport{Raw: append([]string(nil), rep.Raw...)}
	if out.Verb, err = end.requireText("verb"); err != nil {
		return out, err
	}
	if out.Target, err = labelText(end, "target"); err != nil {
		return out, err
	}
	if out.Count, err = end.requireInt("count", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Queued, err = end.requireInt("queued", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Refused, err = end.requireInt("refused", 0, math.MaxInt32); err != nil {
		return out, err
	}
	for _, rec := range r.Records {
		var res OrderResult
		if res.Bot, err = rec.requireText("bot"); err != nil {
			return out, err
		}
		if res.Verb, err = rec.requireText("verb"); err != nil {
			return out, err
		}
		if res.Status, err = requiredWord(rec, "status", "queued", "refused"); err != nil {
			return out, err
		}
		if res.Seq, res.HasSeq, err = rec.optionalInt("seq", 0, math.MaxInt32); err != nil {
			return out, err
		}
		if res.Code, res.HasCode, err = rec.text("code"); err != nil {
			return out, err
		}
		if res.Msg, res.HasMsg, err = rec.text("msg"); err != nil {
			return out, err
		}
		if res.Extra, err = rec.extra(orderRecordKeys); err != nil {
			return out, err
		}
		out.Results = append(out.Results, res)
	}
	q, f := 0, 0
	for _, res := range out.Results {
		switch res.Status {
		case "queued":
			q++
		case "refused":
			f++
			if !res.HasCode {
				return out, fmt.Errorf("%w: order bot=%s is refused without code=", ErrBadReport, res.Bot)
			}
		}
	}
	if q != out.Queued || f != out.Refused {
		return out, fmt.Errorf("%w: order-end queued=%d refused=%d, the lines say %d/%d", ErrBadReport, out.Queued, out.Refused, q, f)
	}
	return out, nil
}

// Follow orders follow; name "" (or "none") clears a manual follow target.
func (c *Client) Follow(ctx context.Context, target, name string) (OrderReport, error) {
	if name == "" {
		return c.Order(ctx, target, "follow", "")
	}
	return c.Order(ctx, target, "follow", name)
}

// Stay orders stay.
func (c *Client) Stay(ctx context.Context, target string) (OrderReport, error) {
	return c.Order(ctx, target, "stay", "")
}

// Come orders come: the bot moves to where the sender stands.
func (c *Client) Come(ctx context.Context, target string) (OrderReport, error) {
	return c.Order(ctx, target, "come", "")
}

// Attack orders an attack on what (a mark, "my", "mark:<word>", "spawn:<id>" or a GUID).
func (c *Client) Attack(ctx context.Context, target, what string) (OrderReport, error) {
	return c.Order(ctx, target, "attack", what)
}

// Assist orders an assist on the named party member.
func (c *Client) Assist(ctx context.Context, target, member string) (OrderReport, error) {
	return c.Order(ctx, target, "assist", member)
}

// Pull orders a pull of what (same forms as Attack).
func (c *Client) Pull(ctx context.Context, target, what string) (OrderReport, error) {
	return c.Order(ctx, target, "pull", what)
}

// EndPull ends the pull window.
func (c *Client) EndPull(ctx context.Context, target string) (OrderReport, error) {
	return c.Order(ctx, target, "endpull", "")
}

// Hold puts the passive strategy on.
func (c *Client) Hold(ctx context.Context, target string) (OrderReport, error) {
	return c.Order(ctx, target, "hold", "")
}

// Free drops every order.
func (c *Client) Free(ctx context.Context, target string) (OrderReport, error) {
	return c.Order(ctx, target, "free", "")
}

// ---- role and master ----

// RoleResult is one bot's line of a role answer.
type RoleResult struct {
	Bot    string
	Role   string
	Seq    int
	HasSeq bool
	Status string // queued, stored, refused
	// Code is the refusal code (no-tank-kit, ...), present on a refused line; Cleared says the refusal also
	// dropped a tank bit the bot already had.
	Code       string
	HasCode    bool
	Cleared    bool
	HasCleared bool
	Extra      map[string]string
}

var roleRecordKeys = map[string]bool{"bot": true, "role": true, "seq": true, "status": true, "code": true, "cleared": true}

// RoleReport is a whole role answer.
type RoleReport struct {
	Results []RoleResult
	Role    string
	Target  string
	Count   int
	Queued  int
	Raw     []string
}

// Role sets a bot's or party's role: tank, healer, dps or none.
func (c *Client) Role(ctx context.Context, target, role string) (RoleReport, error) {
	words, err := targetWords(target)
	if err != nil {
		return RoleReport{}, err
	}
	role = strings.ToLower(role)
	switch role {
	case "tank", "healer", "dps", "none":
	default:
		return RoleReport{}, fmt.Errorf("tbcmd: role %q is not tank, healer, dps or none", role)
	}
	line := strings.Join(append(append([]string{".tb", "role"}, words...), role), " ")
	rep, err := c.call(ctx, line, "role-end")
	if err != nil {
		return RoleReport{}, err
	}
	return parseRole(rep)
}

func parseRole(rep *command.Reply) (RoleReport, error) {
	r, err := parseReport(rep, "role-begin", "role-end", "role", "bot")
	if err != nil {
		return RoleReport{}, err
	}
	end := r.End
	out := RoleReport{Raw: append([]string(nil), rep.Raw...)}
	if out.Role, err = requiredWordOrEmpty(end, "role", "tank", "healer", "dps", "none"); err != nil {
		return out, err
	}
	if out.Target, err = labelText(end, "target"); err != nil {
		return out, err
	}
	if out.Count, err = end.requireInt("count", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Queued, err = end.requireInt("queued", 0, math.MaxInt32); err != nil {
		return out, err
	}
	for _, rec := range r.Records {
		var res RoleResult
		if res.Bot, err = rec.requireText("bot"); err != nil {
			return out, err
		}
		if res.Role, err = requiredWordOrEmpty(rec, "role", "tank", "healer", "dps", "none"); err != nil {
			return out, err
		}
		if res.Status, err = requiredWord(rec, "status", "queued", "stored", "refused"); err != nil {
			return out, err
		}
		if res.Code, res.HasCode, err = rec.text("code"); err != nil {
			return out, err
		}
		if res.Status == "refused" && !res.HasCode {
			return out, fmt.Errorf("%w: role bot=%s is refused without code=", ErrBadReport, res.Bot)
		}
		if res.Cleared, res.HasCleared, err = rec.optionalBool("cleared"); err != nil {
			return out, err
		}
		if res.Seq, res.HasSeq, err = rec.optionalInt("seq", 0, math.MaxInt32); err != nil {
			return out, err
		}
		if res.Extra, err = rec.extra(roleRecordKeys); err != nil {
			return out, err
		}
		out.Results = append(out.Results, res)
	}
	q := 0
	for _, res := range out.Results {
		if res.Status == "queued" {
			q++
		}
	}
	if q != out.Queued {
		return out, fmt.Errorf("%w: role-end queued=%d, %d line(s) queued", ErrBadReport, out.Queued, q)
	}
	return out, nil
}

// MasterResult is one bot's line of a master answer.
type MasterResult struct {
	Bot              string
	Commander        string
	HasCommander     bool
	CommanderInParty bool
	HasInParty       bool
	Seq              int
	HasSeq           bool
	Status           string // queued, stored, bound, refused
	// Code is the refusal code (bind-failed, release-failed, ...), present on a refused line.
	Code    string
	HasCode bool
	Extra   map[string]string
}

var masterRecordKeys = map[string]bool{"bot": true, "commander": true, "commander_in_party": true, "seq": true,
	"status": true, "code": true}

// MasterReport is a whole master answer.
type MasterReport struct {
	Results   []MasterResult
	Target    string
	Commander string
	Count     int
	Queued    int
	Raw       []string
}

// Master binds bots to a commander outside their party; commander "none" releases them.
func (c *Client) Master(ctx context.Context, target, commander string) (MasterReport, error) {
	words, err := targetWords(target)
	if err != nil {
		return MasterReport{}, err
	}
	if strings.EqualFold(commander, "none") {
		commander = "none"
	} else {
		name, err := chat.NormaliseName(commander)
		if err != nil {
			return MasterReport{}, fmt.Errorf("tbcmd: commander: %w", err)
		}
		commander = name
	}
	line := strings.Join(append(append([]string{".tb", "master"}, words...), commander), " ")
	rep, err := c.call(ctx, line, "master-end")
	if err != nil {
		return MasterReport{}, err
	}
	return parseMaster(rep)
}

func parseMaster(rep *command.Reply) (MasterReport, error) {
	r, err := parseReport(rep, "master-begin", "master-end", "master", "bot")
	if err != nil {
		return MasterReport{}, err
	}
	end := r.End
	out := MasterReport{Raw: append([]string(nil), rep.Raw...)}
	if out.Target, err = labelText(end, "target"); err != nil {
		return out, err
	}
	if out.Commander, err = end.requireText("commander"); err != nil {
		return out, err
	}
	if out.Count, err = end.requireInt("count", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Queued, err = end.requireInt("queued", 0, math.MaxInt32); err != nil {
		return out, err
	}
	for _, rec := range r.Records {
		var res MasterResult
		if res.Bot, err = rec.requireText("bot"); err != nil {
			return out, err
		}
		if res.Commander, res.HasCommander, err = rec.text("commander"); err != nil {
			return out, err
		}
		if res.Status, err = requiredWord(rec, "status", "queued", "stored", "bound", "refused"); err != nil {
			return out, err
		}
		if res.Code, res.HasCode, err = rec.text("code"); err != nil {
			return out, err
		}
		if res.Status == "refused" && !res.HasCode {
			return out, fmt.Errorf("%w: master bot=%s is refused without code=", ErrBadReport, res.Bot)
		}
		if res.Seq, res.HasSeq, err = rec.optionalInt("seq", 0, math.MaxInt32); err != nil {
			return out, err
		}
		if res.CommanderInParty, res.HasInParty, err = rec.optionalBool("commander_in_party"); err != nil {
			return out, err
		}
		if res.Extra, err = rec.extra(masterRecordKeys); err != nil {
			return out, err
		}
		out.Results = append(out.Results, res)
	}
	q := 0
	for _, res := range out.Results {
		if res.Status == "queued" {
			q++
		}
	}
	if q != out.Queued {
		return out, fmt.Errorf("%w: master-end queued=%d, %d line(s) queued", ErrBadReport, out.Queued, q)
	}
	return out, nil
}

// ---- party ----

// PartyMember is one line of a party roster.
type PartyMember struct {
	Name      string
	Leader    bool
	Online    bool
	Bot       bool
	HasBot    bool
	Class     string
	HasClass  bool
	Level     int
	HasLevel  bool
	Role      string
	HasRole   bool
	Engine    bool
	HasEngine bool
	Master    string
	HasMaster bool
	Commanded bool
	HasCmd    bool
	// Partial is set for an offline member: the core sends only member, leader and online.
	Partial bool
	Extra   map[string]string
}

var partyRecordKeys = map[string]bool{"member": true, "leader": true, "online": true, "bot": true,
	"class": true, "level": true, "role": true, "engine": true, "master": true, "commanded": true}

// PartyReport is a whole roster answer.
type PartyReport struct {
	Members   []PartyMember
	Leader    string
	Count     int
	Bots      int
	Raid      bool
	Commander string
	Target    string
	Raw       []string
}

// Party reads the roster of the party a character leads (target is the leader's name, or "party <leader>").
func (c *Client) Party(ctx context.Context, target string) (PartyReport, error) {
	words, err := targetWords(target)
	if err != nil {
		return PartyReport{}, err
	}
	rep, err := c.call(ctx, strings.Join(append([]string{".tb", "party"}, words...), " "), "party-end")
	if err != nil {
		return PartyReport{}, err
	}
	return parseParty(rep, strings.Join(words, " "))
}

func parseParty(rep *command.Reply, target string) (PartyReport, error) {
	r, err := parseReport(rep, "party-begin", "party-end", "party", "member")
	if err != nil {
		return PartyReport{}, err
	}
	end := r.End
	out := PartyReport{Target: target, Raw: append([]string(nil), rep.Raw...)}
	if out.Leader, err = end.requireText("leader"); err != nil {
		return out, err
	}
	if out.Commander, err = end.requireText("commander"); err != nil {
		return out, err
	}
	if out.Count, err = end.requireInt("count", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Bots, err = end.requireInt("bots", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Raid, err = end.requireBool("raid"); err != nil {
		return out, err
	}
	for _, rec := range r.Records {
		var m PartyMember
		if m.Name, err = rec.requireText("member"); err != nil {
			return out, err
		}
		if m.Leader, err = rec.requireBool("leader"); err != nil {
			return out, err
		}
		if m.Online, err = rec.requireBool("online"); err != nil {
			return out, err
		}
		if m.Bot, m.HasBot, err = rec.optionalBool("bot"); err != nil {
			return out, err
		}
		if m.Class, m.HasClass, err = rec.text("class"); err != nil {
			return out, err
		}
		if m.Level, m.HasLevel, err = rec.optionalInt("level", 1, 255); err != nil {
			return out, err
		}
		if m.Role, m.HasRole, err = rec.text("role"); err != nil {
			return out, err
		}
		if m.Engine, m.HasEngine, err = rec.optionalBool("engine"); err != nil {
			return out, err
		}
		if m.Master, m.HasMaster, err = rec.text("master"); err != nil {
			return out, err
		}
		if m.Commanded, m.HasCmd, err = rec.optionalBool("commanded"); err != nil {
			return out, err
		}
		m.Partial = !m.HasBot
		if m.Extra, err = rec.extra(partyRecordKeys); err != nil {
			return out, err
		}
		out.Members = append(out.Members, m)
	}
	bots := 0
	for _, m := range out.Members {
		if m.HasBot && m.Bot {
			bots++
		}
	}
	if bots != out.Bots {
		return out, fmt.Errorf("%w: party-end bots=%d, %d member line(s) bot=yes", ErrBadReport, out.Bots, bots)
	}
	return out, nil
}

// FormPartyReport is a whole forming answer.
type FormPartyReport struct {
	Joins   []PartyJoin
	Leader  string
	Joined  int
	Asked   int
	Note    string
	HasNote bool
	Raw     []string
}

// PartyJoin is one bot's line of a forming answer.
type PartyJoin struct {
	Bot    string
	Leader string
	Status string
	Extra  map[string]string
}

var joinRecordKeys = map[string]bool{"bot": true, "leader": true, "status": true}

// FormParty forms a party: the bots join the leader's group.
func (c *Client) FormParty(ctx context.Context, leader string, members ...string) (FormPartyReport, error) {
	name, err := chat.NormaliseName(leader)
	if err != nil {
		return FormPartyReport{}, fmt.Errorf("tbcmd: leader: %w", err)
	}
	words := []string{".tb", "party", name}
	for _, m := range members {
		n, err := chat.NormaliseName(m)
		if err != nil {
			return FormPartyReport{}, fmt.Errorf("tbcmd: member: %w", err)
		}
		words = append(words, n)
	}
	if len(members) == 0 {
		return FormPartyReport{}, errors.New("tbcmd: FormParty needs at least one member (use Party for the roster)")
	}
	rep, err := c.call(ctx, strings.Join(words, " "), "party-end")
	if err != nil {
		return FormPartyReport{}, err
	}
	return parseFormParty(rep)
}

func parseFormParty(rep *command.Reply) (FormPartyReport, error) {
	r, err := parseReport(rep, "party-begin", "party-end", "party-join", "bot")
	if err != nil {
		return FormPartyReport{}, err
	}
	end := r.End
	out := FormPartyReport{Raw: append([]string(nil), rep.Raw...)}
	if out.Leader, err = end.requireText("leader"); err != nil {
		return out, err
	}
	if out.Joined, err = end.requireInt("joined", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Asked, err = end.requireInt("asked", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Joined > out.Asked {
		return out, fmt.Errorf("%w: party-end joined=%d over asked=%d", ErrBadReport, out.Joined, out.Asked)
	}
	if out.Note, out.HasNote, err = end.text("note"); err != nil {
		return out, err
	}
	for _, rec := range r.Records {
		var j PartyJoin
		if j.Bot, err = rec.requireText("bot"); err != nil {
			return out, err
		}
		if j.Leader, err = rec.requireText("leader"); err != nil {
			return out, err
		}
		if j.Status, err = rec.requireText("status"); err != nil {
			return out, err
		}
		if j.Extra, err = rec.extra(joinRecordKeys); err != nil {
			return out, err
		}
		out.Joins = append(out.Joins, j)
	}
	if out.Asked != len(out.Joins) {
		return out, fmt.Errorf("%w: party-end asked=%d, %d party-join line(s)", ErrBadReport, out.Asked, len(out.Joins))
	}
	return out, nil
}

// ---- threat ----

// ThreatEntry is one (name, threat) pair of a threat line's top list.
type ThreatEntry struct {
	Name   string
	Threat int
}

// ThreatLine is one mob that holds a bot on its threat list.
type ThreatLine struct {
	Mob    string
	Entry  int
	Name   string
	HPPct  int
	Victim string
	Target string
	Size   int
	Top    []ThreatEntry
	HasTop bool
	// X, Y, Z are the mob's position when the line carries one (pos=x,y,z); HasPos is false for a module that
	// does not send it. A position is what lets a caller place the mob without the commander's own view.
	X, Y, Z float64
	HasPos  bool
	// Flee says the mob is fleeing (flee=yes); HasFlee is false when the key is absent, which is not "flee=no".
	Flee    bool
	HasFlee bool
	Extra   map[string]string
}

var threatRecordKeys = map[string]bool{"mob": true, "entry": true, "name": true, "hppct": true,
	"victim": true, "target": true, "size": true, "top": true, "pos": true, "flee": true}

// ThreatReport is a whole threat answer.
type ThreatReport struct {
	Mobs   []ThreatLine
	Count  int
	Bots   int
	Target string
	Raw    []string
}

// Threat reads the threat lists of the bots' mobs (read-only on the module side).
func (c *Client) Threat(ctx context.Context, target string) (ThreatReport, error) {
	words, err := targetWords(target)
	if err != nil {
		return ThreatReport{}, err
	}
	rep, err := c.call(ctx, strings.Join(append([]string{".tb", "threat"}, words...), " "), "threat-end")
	if err != nil {
		return ThreatReport{}, err
	}
	return parseThreat(rep)
}

func parseThreat(rep *command.Reply) (ThreatReport, error) {
	r, err := parseReport(rep, "threat-begin", "threat-end", "threat", "mob")
	if err != nil {
		return ThreatReport{}, err
	}
	end := r.End
	out := ThreatReport{Raw: append([]string(nil), rep.Raw...)}
	if out.Target, err = labelText(end, "target"); err != nil {
		return out, err
	}
	if out.Count, err = end.requireInt("count", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Bots, err = end.requireInt("bots", 0, math.MaxInt32); err != nil {
		return out, err
	}
	for _, rec := range r.Records {
		var m ThreatLine
		if m.Mob, err = rec.requireText("mob"); err != nil {
			return out, err
		}
		if m.Entry, err = rec.requireInt("entry", 0, math.MaxInt32); err != nil {
			return out, err
		}
		if m.Name, err = rec.requireText("name"); err != nil {
			return out, err
		}
		if m.HPPct, err = rec.requireInt("hppct", 0, 100); err != nil {
			return out, err
		}
		if m.Victim, err = rec.requireText("victim"); err != nil {
			return out, err
		}
		if m.Target, err = rec.requireText("target"); err != nil {
			return out, err
		}
		if m.Size, err = rec.requireInt("size", 0, math.MaxInt32); err != nil {
			return out, err
		}
		if raw, hasTop := rec.rawValue("top"); hasTop {
			m.HasTop = true
			for _, item := range strings.Split(raw, ";") {
				if item == "" {
					return out, fmt.Errorf("%w: top has an empty item", ErrBadReport)
				}
				parts := strings.Split(item, ":")
				if len(parts) != 2 {
					return out, fmt.Errorf("%w: top item %q is not name:number", ErrBadValue, item)
				}
				name, err := decodePart(parts[0])
				if err != nil {
					return out, rec.bad("top", err)
				}
				threat, err := strconv.Atoi(parts[1])
				if err != nil || threat < 0 {
					return out, fmt.Errorf("%w: top item %q has no threat", ErrBadValue, item)
				}
				m.Top = append(m.Top, ThreatEntry{Name: name, Threat: threat})
			}
		}
		if m.Size < len(m.Top) {
			return out, fmt.Errorf("%w: threat mob=%s size=%d with %d top entries", ErrBadReport, m.Mob, m.Size, len(m.Top))
		}
		if raw, hasPos := rec.rawValue("pos"); hasPos {
			x, y, z, err := coordinate(rec, "pos", raw)
			if err != nil {
				return out, err
			}
			m.X, m.Y, m.Z, m.HasPos = x, y, z, true
		}
		if m.Flee, m.HasFlee, err = rec.optionalBool("flee"); err != nil {
			return out, err
		}
		if m.Extra, err = rec.extra(threatRecordKeys); err != nil {
			return out, err
		}
		out.Mobs = append(out.Mobs, m)
	}
	return out, nil
}
