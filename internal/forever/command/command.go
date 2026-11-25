// Package command is the Forever (Classic 1.60.1) command channel: it sends one chat-command line on purpose and
// collects the system chat lines that belong to it, then classifies the core's refusals into named errors.
//
// A command is an ordinary say chat line whose text starts with '.' or '!' (Chat.cpp:181-201, called from
// ChatHandler.cpp:233 before any chat validation), so there is no new packet here: the request is
// CMSG_CHAT_MESSAGE_SAY through the session's SendCommand, the answer is SMSG_CHAT type CHAT_MSG_SYSTEM (and
// SMSG_PRINT_NOTIFICATION for a few answers), both parsed by internal/forever/chat since round 5.
//
// The collector does not own the socket. The caller's Transport sends the line and pumps server packets while the
// collector reads the chat lines it is fed; the session pumps its own packet loop, and its chat log keeps every
// line, so nothing that arrives meanwhile is taken out of the log.
package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Defaults for a run whose caller states nothing. The quiet window is the end condition of a tokenless command
// (a core command, or an older module command); the deadline bounds every run.
const (
	DefaultQuiet    = 1500 * time.Millisecond
	DefaultDeadline = 15 * time.Second
	DefaultMaxLines = 64
)

// Bounds. One SMSG_CHAT text field is written with a 12-bit length (ChatPackets.cpp:196-238), so a reply line is at
// most 4095 bytes; the collector keeps a reply far below the chat log's own ring.
const (
	MaxLineBytes      = 4095
	MaxCollectedLines = 256
	MaxCollectedBytes = 64 << 10
	MaxTokenLength    = 32
)

// Errors Run returns for the core's refusals and for an unusable reply.
var (
	// ErrNoPermission is the core's fall-through path: the account lacks the command's permission (and does not
	// hold permission 33), so ParseCommands returns false and the line is processed as ordinary chat — it comes
	// back as this character's own say line.
	ErrNoPermission = errors.New("command: the server did not run it (the line came back as ordinary say chat: the account lacks the command permission)")
	// ErrUnknownCommand is trinity string 6, "Command '<text>' does not exist". The core hides a missing
	// permission behind the same answer when the account holds permission 33.
	ErrUnknownCommand = errors.New("command: the server says the command does not exist")
	// ErrSyntax is a command that exists and refused its arguments: "Incorrect syntax.", an unknown subcommand,
	// an ambiguous one, a value the typed parser refused, or the command's own usage text.
	ErrSyntax = errors.New("command: the server rejected the command's syntax or arguments")
	// ErrNoReply is a run that ended with no reply line at all.
	ErrNoReply = errors.New("command: the server sent no reply")
	// ErrIncomplete is a token command whose reply ended without the end marker (or an error line) carrying the
	// token: the report is not complete and must not be acted on.
	ErrIncomplete = errors.New("command: the reply ended without its end line")
	// ErrReplyTooLong is a reply past MaxCollectedLines/MaxCollectedBytes/MaxLineBytes.
	ErrReplyTooLong = errors.New("command: the reply exceeded the collector's bounds")
)

// RefusalError carries the line that made Run classify a refusal.
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

// Unwrap makes errors.Is(err, ErrUnknownCommand) and friends work.
func (e *RefusalError) Unwrap() error { return e.Err }

// Transport is what Run needs from the caller: one send, and a pump that answers server packets while the reply
// arrives. The pump must call feed for every chat line it wants the collector to see, and return as soon as done
// reports the reply complete; it returns the context's error when that ends, and the caller's link error when the
// connection dies.
type Transport interface {
	// SendCommand sends one line on ctx: the caller's context bounds the send, and a stop or cleanup path whose own
	// context was detached from the session's keeps the send possible after an interrupt.
	SendCommand(ctx context.Context, line string) error
	Pump(ctx context.Context, feed func(chat.Line), done func() bool) error
}

// Options is one run's behaviour.
type Options struct {
	// Token is the caller's token; Run appends " #<token>" to the line. Empty means a core command: the reply is
	// ended by the quiet window instead of an end marker.
	Token string
	// EndKind, when set, is the end-line kind a token reply waits for ("state-end", "order-end", ...); an error
	// line always ends a report too. Empty accepts any "*-end".
	EndKind string
	// Quiet is the tokenless end condition: this long without a new system line ends the reply.
	Quiet time.Duration
	// Deadline bounds the whole run.
	Deadline time.Duration
	// MaxLines bounds the collected lines of a tokenless reply too.
	MaxLines int
	// SelfName and SelfGUID identify this character: a say line from it that repeats the command is the core's
	// fall-through (ErrNoPermission). The server fills SenderName only for a whisper, so the GUID is the reliable
	// half; either one matching is enough. Both empty disables the check.
	SelfName string
	SelfGUID wire.GUID
	// AllowEmpty lets a tokenless run end quietly with no line as a complete answer: some core commands print
	// nothing on success (HandleDebugBoundaryCommand, HandleNpcEvadeCommand), so the quiet window with nothing
	// collected is the success itself. Refusals, echoes, pump errors and overlong replies still fail.
	AllowEmpty bool
	// Silent marks a tokenless command the core answers with nothing when it works and with a line only when it
	// refuses (the GM teleport, .aura, .die, ...). Silence is not decided at the quiet window, because a refusal can
	// still be on its way: with no line, the run lasts to Deadline (or to Confirm) and that is the success; a line
	// ends it at the quiet window as any reply does, and the caller reads the line as what it is, a refusal.
	Silent bool
	// Confirm, when set on a tokenless run, is asked each time the pump looks: true ends the run at once as
	// confirmed (End "confirmed"), the effect having been seen by the caller itself.
	Confirm func() bool
	// BareBody, when set on a token run, also takes the module's untagged lines of that record kind ("bg" accepts
	// "bg: ..." and "bg bot=..." rows) as part of the reply. A module that tags only its end line (the battleground
	// status) would otherwise lose every body row to the "somebody else's line" rule. A line that carries another
	// token is still not ours.
	BareBody string
	// OneLine makes a token run's first collected line the whole reply: the module's `los` record is one line with
	// no begin/end markers (its documented exception to the report rule), so the run ends on the line that carries
	// its token (End "line") instead of waiting for a marker the module never sends. An error line still ends it.
	OneLine bool
	// Now is the clock; nil means time.Now. Tests pass a fake clock.
	Now func() time.Time

	// allowRefusal lets a tokened run take an untagged core refusal as its own. Only Session sets it, and only
	// after it has waited out the quiet window of an earlier tokenless run (the rule Session states); a caller
	// that uses Run directly leaves it false and must treat an untagged line as unrelated traffic.
	allowRefusal bool
}

func (o *Options) normalise() error {
	if o.Quiet <= 0 {
		o.Quiet = DefaultQuiet
	}
	if o.Deadline <= 0 {
		o.Deadline = DefaultDeadline
	}
	if o.MaxLines <= 0 {
		o.MaxLines = DefaultMaxLines
	}
	if o.MaxLines > MaxCollectedLines {
		o.MaxLines = MaxCollectedLines
	}
	if o.Token != "" && !ValidToken(o.Token) {
		return fmt.Errorf("command: %q is not a valid token (1..%d of A-Z a-z 0-9 _ . : -)", o.Token, MaxTokenLength)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return nil
}

// NewToken returns a fresh caller-chosen token: "t" and 12 random hex digits. It is inside the documented
// character set and short enough for every end line.
func NewToken() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("command: a token: %w", err)
	}
	return "t" + hex.EncodeToString(b[:]), nil
}

// ValidToken reports whether t is inside the token grammar: 1..32 characters of A-Z a-z 0-9 _ . : -.
func ValidToken(t string) bool {
	if t == "" || len(t) > MaxTokenLength {
		return false
	}
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == ':' || r == '-':
		default:
			return false
		}
	}
	return true
}

// Run sends line through conn and collects its reply. The returned Reply is always the lines that arrived, even
// when an error is returned; the error is a named refusal (ErrUnknownCommand, ErrSyntax, ErrNoPermission, ...), a
// collector error, or the pump's own error (the link's death, the context ending).
func Run(ctx context.Context, conn Transport, line string, opts Options) (*Reply, error) {
	if conn == nil {
		return nil, errors.New("command: no transport")
	}
	if err := opts.normalise(); err != nil {
		return nil, err
	}
	full := line
	if opts.Token != "" {
		full = line + " #" + opts.Token
	}
	if err := chat.ValidateCommandText(full); err != nil {
		return nil, err
	}
	c := newCollector(full, opts)
	start := opts.Now()
	if err := conn.SendCommand(ctx, full); err != nil {
		return nil, err
	}
	pumpErr := conn.Pump(ctx, c.Feed, func() bool { return c.Complete(opts.Now()) })
	rep := c.Reply()
	rep.Elapsed = opts.Now().Sub(start)
	switch {
	case pumpErr != nil:
		return rep, pumpErr
	case rep.Refusal != nil:
		return rep, &RefusalError{Err: rep.Refusal, Text: rep.RefusalText}
	case c.tooMany || c.tooBig:
		return rep, ErrReplyTooLong
	case opts.Token != "" && !rep.Complete:
		return rep, ErrIncomplete
	case !rep.Complete && !c.got:
		// A silent-success command (AllowEmpty) ends quietly with no line: PSendSysMessage never ran, so
		// there is nothing to collect. Anything with a refusal, echo, overlong reply or pump error already
		// returned above; a deadline or quota end with no line is still no answer.
		if !opts.AllowEmpty || opts.Token != "" || rep.Echoed || rep.End != "quiet" {
			return rep, ErrNoReply
		}
		rep.Complete = true
	}
	return rep, nil
}

// Session serialises command runs on one connection: with an untokened command the reply cannot be told from
// another command's, so one command at a time is the default. It is a queue in the sense that a second caller
// waits for the first; it is not a pipeline.
//
// It also owns the quiet rule between a tokenless run and a tokened one: a tokenless reply ends at its quiet
// window or line cap, but its tail may still arrive. A tokened run sent before that quiet window has passed could
// take the tail's untagged "Syntax: ..." line for its own refusal. So a tokened run waits until Quiet after the
// last line of any earlier run that did not end with its own marker; only then is an untagged refusal in the new
// run accepted as its own (Options.allowRefusal).
type Session struct {
	// hold is the channel's one-run token: Run, TryRun and ExclusiveRun take it for as long as their lines may be on
	// the wire. It is a channel, not a sync.Mutex, so every wait can end on its context (a queued run whose caller
	// went away stops waiting) and TryRun can refuse instead of waiting at all.
	hold      chan struct{}
	conn      Transport
	tailUntil time.Time // no untagged line of an earlier run can still arrive before this
	// sleep waits for the tail window; nil means a real timer. Tests drive it with their clock.
	sleep func(time.Duration)
}

// ErrBusy is TryRun's answer when the channel is held by another run: it returns it at once and sends nothing.
var ErrBusy = errors.New("command: the channel is held by another run")

// NewSession wraps conn so Run calls are serialised.
func NewSession(conn Transport) *Session {
	return &Session{hold: make(chan struct{}, 1), conn: conn}
}

// take waits for the channel until ctx ends.
func (s *Session) take(ctx context.Context) error {
	select {
	case s.hold <- struct{}{}:
		if err := ctx.Err(); err != nil {
			s.release()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) release() { <-s.hold }

// TryRun is Run for a caller that must not wait for the channel: it takes the channel only if it is free and answers
// ErrBusy at once otherwise, having sent nothing. The daemon's own run methods use it: they execute on the session
// goroutine that the preflight's queued calls need while the preflight holds the channel off the queue, so a wait
// here would stop every method and the packet loop until that read ended.
func (s *Session) TryRun(ctx context.Context, line string, opts Options) (*Reply, error) {
	select {
	case s.hold <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	defer s.release()
	return s.runLocked(ctx, line, opts)
}

// Run is Run on the session's connection, one at a time.
func (s *Session) Run(ctx context.Context, line string, opts Options) (*Reply, error) {
	if err := s.take(ctx); err != nil {
		return nil, err
	}
	defer s.release()
	return s.runLocked(ctx, line, opts)
}

// runLocked is Run's body with the channel held.
func (s *Session) runLocked(ctx context.Context, line string, opts Options) (*Reply, error) {
	if err := opts.normalise(); err != nil {
		return nil, err
	}
	if opts.Token != "" {
		if err := s.awaitTail(ctx, opts); err != nil {
			return nil, err
		}
		// The wait above is the whole rule: no earlier run's tail can still be pending now.
		opts.allowRefusal = true
	}
	rep, err := Run(ctx, s.conn, line, opts)
	s.noteTail(rep, opts)
	return rep, err
}

// awaitTail waits out an earlier run's quiet window, when one is still open, so a line of it that is already on the
// wire cannot be taken for the next run's. The caller holds the channel.
func (s *Session) awaitTail(ctx context.Context, opts Options) error {
	wait := s.tailUntil.Sub(opts.Now())
	if wait <= 0 {
		return ctx.Err()
	}
	if s.sleep != nil {
		s.sleep(wait)
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// noteTail records where this run's untagged lines can still arrive: the same rule Run applies to its own reply, so a
// run that follows it on this channel (queued or ExclusiveRun) can wait the tail out.
func (s *Session) noteTail(rep *Reply, opts Options) {
	terminated := false
	if rep != nil {
		switch rep.End {
		case "quiet", "echo":
			// A quiet end with no line at all (AllowEmpty) proves nothing about a reply still on its way.
			terminated = len(rep.Raw) != 0 || rep.End == "echo"
		case "maxlines", "deadline", "confirmed", "":
			terminated = false
		default:
			// A tokened run's own end marker or error line: nothing of it can follow.
			terminated = rep.Complete
		}
	}
	if terminated {
		s.tailUntil = time.Time{}
		return
	}
	last := time.Time{}
	if rep != nil {
		last = rep.LastAt
	}
	if last.IsZero() {
		last = opts.Now()
	}
	s.tailUntil = last.Add(opts.Quiet)
}

// ExclusiveRun runs fn as the channel's only run and keeps this Session's tail rule for it: an earlier run's open
// quiet window is waited out before fn sends, and fn's reply opens the window a later run waits out. It is for a caller
// that reads one reply off this channel with its own transport (the daemon's preflight, whose send and pump slices are
// queued calls and whose reply arrives through a tap): a plain take would let an earlier run's late line be read as
// fn's reply and fn's own late line as the next run's. fn must not call Run: the token is not reentrant. The wait for
// the lock ends with ctx (fn did not run when it expires first); a caller whose own code runs on a goroutine the
// holder's calls need must use TryRun instead of waiting.
func (s *Session) ExclusiveRun(ctx context.Context, opts Options, fn func() (*Reply, error)) (*Reply, error) {
	if err := opts.normalise(); err != nil {
		return nil, err
	}
	if err := s.take(ctx); err != nil {
		return nil, err
	}
	defer s.release()
	if err := s.awaitTail(ctx, opts); err != nil {
		return nil, err
	}
	rep, err := fn()
	s.noteTail(rep, opts)
	return rep, err
}

// Reply is what one command answered.
type Reply struct {
	Line        string // the full line as sent, token included
	Token       string
	Raw         []string    // the visible, sanitised text of each collected line, in order (display only)
	RawText     []string    // the same lines' exact server text, bounded at MaxLineBytes (matching only)
	Lines       []ReplyLine // the same lines, split into kind and key=value fields
	End         string      // the kind that ended the reply, or "quiet"/"maxlines"/"deadline"
	RawEnd      string      // the end line's visible text ("" when there was none)
	Complete    bool        // the reply ended the way its mode requires
	Echoed      bool        // the command came back as our own say chat
	Refusal     error       // nil, or one of the named refusal errors
	RefusalText string      // the line that made it a refusal
	LastAt      time.Time   // when the last collected line arrived; zero when none did
	Elapsed     time.Duration
}

// ReplyLine is one reply line: a kind word, then key=value fields. Values are kept as they arrived: the typed layer
// decodes them (percent-encoding, the v2 grammar).
type ReplyLine struct {
	Kind   string
	Fields map[string]string
	Keys   []string // field order, for diagnostics
	Words  []string // free words that were not key=value
	Token  string
	Raw    string
	// Bad is why the line is not a well-formed reply line: currently only a key that appears twice on one line.
	// The typed layer turns it into ErrBadReport; the collector still collects the line so the end marker is not
	// lost and the caller gets the reason.
	Bad string
}

// Get returns one field's raw value.
func (l ReplyLine) Get(key string) (string, bool) {
	v, ok := l.Fields[key]
	return v, ok
}

// ParseReplyLine splits one reply line. The last word may be the token ("#..."): it is removed from the fields. A
// first word that contains '=' is both the kind and a field, which is the version-1 error form ("error=<code> ...");
// the typed layer refuses such a line (version 2 writes `error code=...`). A line that is free text (an older
// module's answer) has no fields and keeps its words.
func ParseReplyLine(raw string) ReplyLine {
	line := ReplyLine{Fields: make(map[string]string), Raw: raw}
	words := strings.Fields(raw)
	if len(words) == 0 {
		return line
	}
	if tok, ok := trailingToken(raw); ok {
		line.Token = tok
		words = words[:len(words)-1]
	}
	if len(words) == 0 {
		return line
	}
	first := words[0]
	if i := strings.IndexByte(first, '='); i > 0 {
		line.Kind = first[:i]
		if line.Kind == "error" {
			line.setField("code", first[i+1:])
		} else {
			line.setField(first[:i], first[i+1:])
		}
	} else {
		line.Kind = first
	}
	for _, w := range words[1:] {
		if i := strings.IndexByte(w, '='); i > 0 {
			line.setField(w[:i], w[i+1:])
			continue
		}
		line.Words = append(line.Words, w)
	}
	return line
}

func (l *ReplyLine) setField(key, value string) {
	if _, seen := l.Fields[key]; seen {
		if l.Bad == "" {
			l.Bad = "key " + key + " appears twice"
		}
		return
	}
	l.Keys = append(l.Keys, key)
	l.Fields[key] = value
}

// ModulePrefix is what the module puts before a line it sends through the core's system message ("TortoiseBots: ").
const ModulePrefix = "TortoiseBots: "

// bareBodyLine reports whether raw is an untagged body line of record kind kind: the module prefix is optional, the
// kind is the first word and may end in a colon ("bg:" and "bg bot=..." both match "bg"). An empty kind matches
// nothing.
func bareBodyLine(raw, kind string) bool {
	if kind == "" {
		return false
	}
	w := strings.Fields(strings.TrimPrefix(raw, ModulePrefix))
	return len(w) > 0 && strings.TrimSuffix(w[0], ":") == kind
}

// IsEndKind reports whether kind is an end marker: the module's "*-end" line or its error line.
func IsEndKind(kind string) bool { return kind == "error" || strings.HasSuffix(kind, "-end") }

// Visible turns one server line into what the client shows: "|cAARRGGBB", "|r" and "|n" are colour codes and
// "|H...|h" is a link whose label is what stays. Every other bar is literal: the core builds the escaped copy of a
// system message but tokenises the ORIGINAL (Chat.cpp:111-125), so a single '|' arrives, and "||" means two bars
// (the old note that the core doubles them, and the collapse, were wrong). The raw text is never changed in the
// Reply.
func Visible(s string) string {
	if !strings.ContainsRune(s, '|') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '|' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) {
			b.WriteByte('|')
			break
		}
		switch s[i+1] {
		case 'c':
			if i+10 <= len(s) && isHex8(s[i+2:i+10]) {
				i += 10
			} else {
				b.WriteByte('|')
				i++
			}
		case 'r', 'n':
			i += 2
		case 'H':
			j := strings.Index(s[i+2:], "|h")
			if j < 0 {
				b.WriteString(s[i+2:])
				i = len(s)
				break
			}
			labelStart := i + 2 + j + 2
			k := strings.Index(s[labelStart:], "|h")
			if k < 0 {
				b.WriteString(s[labelStart:])
				i = len(s)
				break
			}
			b.WriteString(s[labelStart : labelStart+k])
			i = labelStart + k + 2
		case 'h':
			i += 2
		case 'T':
			j := strings.Index(s[i+2:], "|t")
			if j < 0 {
				b.WriteString(s[i+2:])
				i = len(s)
				break
			}
			i = i + 2 + j + 2
		default:
			b.WriteByte('|')
			i++
		}
	}
	return b.String()
}

func isHex8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for i := range 8 {
		switch {
		case s[i] >= '0' && s[i] <= '9', s[i] >= 'a' && s[i] <= 'f', s[i] >= 'A' && s[i] <= 'F':
		default:
			return false
		}
	}
	return true
}

// IsCommandText reports whether a chat line is a server command text: a leading '.' or '!' followed by more. This
// client never says such a line as chat (SendCommand is the only path that allows it), so a self say of one is
// the core's fall-through for a command the account may not run.
func IsCommandText(text string) bool {
	t := strings.TrimSpace(text)
	return len(t) >= 2 && (t[0] == '.' || t[0] == '!') && t[1] != t[0]
}

// trailingToken returns the token of a line whose last word is "#<token>". In a well-formed line the token is the
// only '#': a value that holds one writes it as %23.
func trailingToken(s string) (string, bool) {
	i := strings.LastIndexByte(s, '#')
	if i < 0 || (i > 0 && s[i-1] != ' ') {
		return "", false
	}
	t := s[i+1:]
	if !ValidToken(t) {
		return "", false
	}
	return t, true
}

// classifyRefusal recognises the core's refusal texts. The trinity strings are the world database's rows 6, 7, 8,
// 9, 10, 193, 194 and 1502; the stable parts are matched, not the whole sentence. It returns the text and the
// error, or "" and nil.
func classifyRefusal(s string) (string, error) {
	t := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(t, "Command '") && strings.HasSuffix(t, " does not exist"):
		return t, ErrUnknownCommand
	case strings.HasPrefix(t, "Subcommand '") && strings.HasSuffix(t, " does not exist."):
		return t, ErrSyntax
	case t == "Incorrect syntax.":
		return t, ErrSyntax
	case strings.HasPrefix(t, "Syntax: "):
		return t, ErrSyntax
	case strings.HasSuffix(t, " is ambiguous:"):
		return t, ErrSyntax
	case t == "Possible subcommands:" || t == "Commands available to you:":
		return t, ErrSyntax
	case strings.Contains(t, " is not valid for type "):
		return t, ErrSyntax
	}
	return "", nil
}

// collector is the state machine: Feed each chat line, ask Complete, then read Reply. Every end bound is a field,
// so a test can drive it with its own clock.
type collector struct {
	line     string
	o        Options
	token    bool
	deadline time.Time
	last     time.Time
	start    time.Time
	got      bool

	lines []ReplyLine
	raw   []string
	// rawText is each collected line's exact server text, bounded at MaxLineBytes: matching runs on it, while
	// raw (and ReplyLine) hold the display form the sanitiser makes.
	rawText []string
	bytes   int

	echo        bool
	complete    bool
	end         string
	rawEnd      string
	refusal     error
	refusalText string
	tooMany     bool
	tooBig      bool
}

func newCollector(line string, o Options) *collector {
	now := o.Now()
	return &collector{line: line, o: o, token: o.Token != "", start: now, deadline: now.Add(o.Deadline)}
}

// Feed takes one chat line. Only system text is a reply candidate; this character's own say line that is a
// command is the core's fall-through refusal. The echo is checked after the first collected line too: a
// notification, a late MOTD or an announcement can arrive first, and the say echo is the only proof that the
// command did not run. Everything else (another player speaking, an emote, a notification that is not a reply)
// is ignored here and stays in the chat log.
func (c *collector) Feed(line chat.Line) {
	if c.stopped() {
		return
	}
	now := c.o.Now()
	if line.Type == chat.TypeSay {
		self := (c.o.SelfName != "" && strings.EqualFold(strings.TrimSpace(line.SenderName), c.o.SelfName)) ||
			(!c.o.SelfGUID.IsZero() && line.SenderGUID == c.o.SelfGUID)
		// A self say line is either this run's echo (the line, exactly) or an earlier command's late echo: this
		// client never says a command text on purpose, so any self say of a command text proves the account's
		// commands are falling through as ordinary chat. The check stays active after the first collected line.
		if self && (strings.TrimSpace(line.Text) == strings.TrimSpace(c.line) || IsCommandText(line.Text)) {
			c.echo = true
			c.complete = true
			c.end = "echo"
			c.refusal = ErrNoPermission
			c.refusalText = chat.Sanitise(Visible(line.Text), MaxLineBytes)
			return
		}
	}
	if line.Type != chat.TypeSystem {
		return
	}
	raw := line.Text
	if len(raw) > MaxLineBytes {
		c.tooBig = true
		return
	}
	// Matching (the refusal table, the token, the module's markers) runs on the server's exact text; only what is
	// kept for display goes through Visible and the sanitiser.
	vis := Visible(raw)
	if txt, r := classifyRefusal(raw); r != nil {
		// A tokened run takes an untagged refusal only before any tokened line of its own, and only when the
		// session has established that no earlier command's tail can still be arriving. Everything else is
		// unrelated traffic (another command's answer, a stale tail, an announcement) and is not ours.
		if c.token && (!c.o.allowRefusal || c.got) {
			return
		}
		c.append(ParseReplyLine(chat.Sanitise(vis, MaxLineBytes)), raw)
		c.got = true
		c.last = now
		if c.refusal == nil {
			c.refusal = r
			c.refusalText = txt
		}
		// A core refusal is a complete block for a token command (nothing can follow it), but a tokenless
		// command's answer may be a multi-line help block: one of its lines ("Possible subcommands:") is matched
		// as a syntax refusal, and ending the run there would hand the block's tail to the next command. A
		// tokenless run keeps collecting until its quiet window or line cap.
		if c.token {
			c.complete = true
			c.end = "error"
		}
		return
	}
	if c.token {
		tok, ok := trailingToken(raw)
		if ok && tok != c.o.Token {
			return // somebody else's line: not this command's reply
		}
		if !ok && !bareBodyLine(raw, c.o.BareBody) {
			return // an untagged line that is not this command's body
		}
		// The module sends the battleground rows through its "TortoiseBots: " system message, end line
		// included; the record kind is the word after the prefix.
		vis = strings.TrimPrefix(vis, ModulePrefix)
	}
	ln := ParseReplyLine(chat.Sanitise(vis, MaxLineBytes))
	c.append(ln, raw)
	c.got = true
	c.last = now
	if c.token && IsEndKind(ln.Kind) && (c.o.EndKind == "" || ln.Kind == "error" || ln.Kind == c.o.EndKind) {
		c.complete = true
		c.end = ln.Kind
		c.rawEnd = chat.Sanitise(vis, MaxLineBytes)
		return
	}
	if c.token && c.o.OneLine {
		// The module's one-line answer: there is nothing after it.
		c.complete = true
		c.end = "line"
		c.rawEnd = chat.Sanitise(vis, MaxLineBytes)
	}
}

func (c *collector) append(ln ReplyLine, raw string) {
	if len(c.raw) >= c.o.MaxLines || len(c.raw) >= MaxCollectedLines {
		c.tooMany = true
		return
	}
	if n := max(len(ln.Raw), len(raw)); c.bytes+n > MaxCollectedBytes || n > MaxLineBytes {
		c.tooBig = true
		return
	} else {
		c.bytes += n + 1
	}
	c.raw = append(c.raw, ln.Raw)
	c.rawText = append(c.rawText, raw)
	c.lines = append(c.lines, ln)
}

func (c *collector) stopped() bool { return c.complete || c.tooMany || c.tooBig }

// Complete reports whether the reply has ended by now.
func (c *collector) Complete(now time.Time) bool {
	if c.stopped() {
		return true
	}
	if !now.Before(c.deadline) {
		c.end = "deadline"
		return true
	}
	if !c.token && c.o.Confirm != nil && c.o.Confirm() {
		c.complete = true
		c.end = "confirmed"
		return true
	}
	if !c.token {
		// A silent-success command (AllowEmpty) ends its quiet window with no line: PSendSysMessage never
		// ran, so there is nothing to collect. The deadline still ends it the same way, and Run tells the
		// two ends apart (quiet is success, deadline is no answer).
		if c.got && now.Sub(c.last) >= c.o.Quiet {
			c.complete = true
			c.end = "quiet"
			return true
		}
		if !c.got && c.o.AllowEmpty && !c.o.Silent && now.Sub(c.start) >= c.o.Quiet {
			c.complete = true
			c.end = "quiet"
			return true
		}
		if len(c.raw) >= c.o.MaxLines {
			c.complete = true
			c.end = "maxlines"
			return true
		}
	}
	return false
}

func (c *collector) Reply() *Reply {
	rep := &Reply{
		Line:        c.line,
		Token:       c.o.Token,
		Raw:         append([]string(nil), c.raw...),
		RawText:     append([]string(nil), c.rawText...),
		Lines:       append([]ReplyLine(nil), c.lines...),
		End:         c.end,
		RawEnd:      c.rawEnd,
		Echoed:      c.echo,
		Refusal:     c.refusal,
		RefusalText: c.refusalText,
		LastAt:      c.last,
	}
	// A token reply is complete only with its end marker; a tokenless reply is complete once it has any line and
	// ended by quiet or the line cap (the deadline only ends it with what it had). A silent-success command
	// (AllowEmpty) with nothing collected is complete when it ended quietly.
	if c.token {
		rep.Complete = c.complete && !c.echo && !c.tooMany && !c.tooBig
	} else if !c.got && (c.o.AllowEmpty || c.o.Silent || c.end == "confirmed") {
		// Nothing collected: complete when it ended quietly (AllowEmpty), at its deadline with nothing said (Silent:
		// no refusal came in the whole window) or confirmed by the caller's own read of the effect.
		okEnd := c.end == "confirmed" || (c.end == "quiet" && c.o.AllowEmpty && !c.o.Silent) || (c.end == "deadline" && c.o.Silent)
		rep.Complete = (c.complete || (c.end == "deadline" && c.o.Silent)) && okEnd && !c.echo && !c.tooMany && !c.tooBig
	} else {
		rep.Complete = c.got && c.complete && !c.tooMany && !c.tooBig
	}
	return rep
}

// Atoi is the typed layer's number parser: it refuses an empty or non-numeric value instead of reading it as 0.
func Atoi(key, s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("command: %s has no value", key)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("command: %s=%q is not a number", key, s)
	}
	return n, nil
}
