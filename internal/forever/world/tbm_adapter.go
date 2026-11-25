package world

// The TBM client's side of the Forever session: the adapter the tbmclient.Conn interface needs (prefix registration,
// the add-on send, the whisper to self, and the next delivered message), and the tbm-check command's body. The
// protocol itself lives in internal/forever/tbmclient; this file only moves the session's packets and chat feed.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
	"github.com/pjerra/forever-agent/internal/forever/tbmclient"
	"github.com/pjerra/forever-agent/internal/forever/tbmproto"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// tbmAdapter satisfies tbmclient.Conn on top of one session: the session's chat-line tap (cmdTaps) queues every
// delivered line for the client's Next, and the add-on inbox is drained the same way.
//
// The CLI's `forever tbm-check` runs the adapter on the session goroutine itself (st nil). The daemon's TBM methods
// run their handler off that goroutine, so they set st and every session effect (the send, the tap install and each
// pump slice) is posted to the session goroutine as its own short call: the queue keeps answering status and the
// dungeon verbs between slices, while packet access stays serialized.
type tbmAdapter struct {
	st      *serveState     // nil for the CLI's in-session adapter
	s       *session        // the session the tap was installed on
	ctx     context.Context // the daemon exchange's deadline (nil for the CLI)
	mu      sync.Mutex
	feed    []chat.Line         // appended on the session goroutine, read by pump
	pending []tbmproto.Incoming // the messages the last slice delivered, taken by Next
	closed  bool                // close has run: the tap stops queueing even if its removal had to wait
	// raw, when set, receives every accepted message as a case recv record for a live capture.
	raw    io.Writer
	remove func() // the tap's remover, session goroutine only
}

// newTBMAdapter builds the adapter on the session goroutine (the CLI's tbm-check) and registers its tap.
func newTBMAdapter(s *session) *tbmAdapter {
	a := &tbmAdapter{s: s}
	a.tap(s)
	return a
}

// newServeTBMAdapter builds the adapter the daemon's TBM methods use: open installs its tap, close removes it, and
// every session effect goes through the queue.
func newServeTBMAdapter(st *serveState) *tbmAdapter { return &tbmAdapter{st: st} }

// tap registers the chat-line tap on s. Session goroutine only.
func (a *tbmAdapter) tap(s *session) {
	a.s = s
	a.remove = s.addChatTap(a.deliver)
}

// deliver queues one chat line for Next. It runs on the session goroutine.
func (a *tbmAdapter) deliver(line chat.Line) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.feed = append(a.feed, line)
}

// open installs the tap on the current session and takes the exchange's context for its sends (the daemon's
// adapter only).
func (a *tbmAdapter) open(ctx context.Context) error {
	a.ctx = ctx
	_, err := a.st.call(ctx, func(s *session) (any, error) {
		a.tap(s)
		return nil, nil
	})
	return err
}

// close removes the tap: directly on the session goroutine (the CLI), or as one queued call (the daemon). A session
// that was replaced by a reconnect is not touched: its tap died with it, and its slice belongs to nobody now.
func (a *tbmAdapter) close() {
	a.mu.Lock()
	a.closed = true
	a.feed = nil
	a.mu.Unlock()
	if a.st == nil {
		if a.remove != nil {
			a.remove()
			a.remove = nil
		}
		return
	}
	s := a.s
	if s == nil || a.remove == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), serveTBMCloseWait)
	defer cancel()
	_, _ = a.st.call(ctx, func(cur *session) (any, error) {
		if cur != s {
			return nil, nil
		}
		a.remove()
		a.remove = nil
		return nil, nil
	})
}

// session runs fn on the session the adapter belongs to: directly on the session goroutine for the CLI, or as one
// queued call for the daemon. A call that lands on another session means the link was re-entered: the exchange is
// over, so this answers the link-lost error instead of sending through a session the tap is not on.
func (a *tbmAdapter) session(ctx context.Context, fn func(*session) error) error {
	if a.st == nil {
		return fn(a.s)
	}
	_, err := a.st.call(ctx, func(cur *session) (any, error) {
		if cur != a.s {
			return nil, ErrLinkLost
		}
		return nil, fn(cur)
	})
	return err
}

// exchangeCtx is the context a send waits on: the exchange's own (set by open), bounded by its deadline.
func (a *tbmAdapter) exchangeCtx() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

// RegisterAddonPrefixes sends the prefix registration packets for the TBM prefix.
func (a *tbmAdapter) RegisterAddonPrefixes(prefixes ...string) error {
	return a.session(a.exchangeCtx(), func(s *session) error { return s.RegisterAddonPrefixes(prefixes...) })
}

// SendAddon sends one add-on message through the session's add-on path.
func (a *tbmAdapter) SendAddon(ch addonmsg.Channel, target, prefix, text string, logged bool) error {
	return a.session(a.exchangeCtx(), func(s *session) error { return s.SendAddon(ch, target, prefix, text, logged) })
}

// SendAddonSelf sends one add-on whisper to this character's own name, the
// add-on whisper transport the ungrouped hello uses.
func (a *tbmAdapter) SendAddonSelf(prefix, text string) error {
	return a.session(a.exchangeCtx(), func(s *session) error {
		name := s.report.Character.Name
		if name == "" {
			return fmt.Errorf("tbm: the character's name is not known yet")
		}
		return s.SendAddon(addonmsg.Whisper, name, prefix, text, false)
	})
}

// WhisperSelf sends the chat command as a whisper to this character's own name. The chat transport has no other
// way to reach the module, and the core delivers a self whisper back to the sender.
func (a *tbmAdapter) WhisperSelf(text string) error {
	return a.session(a.exchangeCtx(), func(s *session) error {
		name := s.report.Character.Name
		if name == "" {
			return fmt.Errorf("tbm: the character's name is not known yet")
		}
		lang, err := s.chatLanguage()
		if err != nil {
			return err
		}
		return s.Send(wire.CMSGChatMessageWhisper, chat.BuildWhisper(lang, name, text))
	})
}

// Next returns the next message for the client: a delivered add-on message first, then a chat line. It drives the
// session's packet pump for one short slice (serveTBMPump) per pass so the queues actually fill, and each slice is
// its own queued call in the daemon, so the wait between slices leaves the session queue to every other method.
func (a *tbmAdapter) Next(ctx context.Context) (tbmproto.Incoming, error) {
	for {
		if in, ok := a.take(); ok {
			return in, nil
		}
		var err error
		if a.st == nil {
			err = a.pump(a.s, ctx)
		} else {
			err = a.session(ctx, func(s *session) error { return a.pump(s, ctx) })
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return tbmproto.Incoming{}, tbmclient.ErrTimeout
			}
			return tbmproto.Incoming{}, err
		}
	}
}

// pump drives the session's packets for one slice and queues every message that arrived. It runs on the session
// goroutine (directly for the CLI, inside the queued call for the daemon).
func (a *tbmAdapter) pump(s *session, ctx context.Context) error {
	s.initAddon()
	own := s.report.Character.GUID
	var got []tbmproto.Incoming
	for {
		m, ok := s.addon.Next()
		if !ok {
			break
		}
		if !tbmAcceptAddon(m, own) {
			continue
		}
		got = append(got, tbmproto.Incoming{Kind: "addon", Channel: addonChannelName(m), Prefix: m.Prefix, Text: m.Text})
	}
	a.mu.Lock()
	feed := a.feed
	a.feed = nil
	a.mu.Unlock()
	for _, line := range feed {
		if tbmAcceptChat(line) {
			got = append(got, tbmproto.Incoming{Kind: "system", Text: line.Text})
		}
	}
	for _, in := range got {
		a.record(in)
	}
	if len(got) > 0 {
		a.mu.Lock()
		a.pending = append(a.pending, got...)
		a.mu.Unlock()
	}
	if err := s.collect(ctx, serveTBMPump); err != nil {
		return err
	}
	return nil
}

// take pops one queued message.
func (a *tbmAdapter) take() (tbmproto.Incoming, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.pending) == 0 {
		return tbmproto.Incoming{}, false
	}
	in := a.pending[0]
	a.pending = a.pending[1:]
	return in, true
}

// tbmAcceptAddon keeps only what the module sends: the contract's replies arrive as add-on whispers whose sender
// is the character's own GUID. A party, raid or guild frame under the TBM prefix is another player's, and so is a
// whisper from any other sender; neither ever completes a hello or a command.
func tbmAcceptAddon(m addonmsg.Message, own wire.GUID) bool {
	return m.Channel == addonmsg.Whisper && m.Sender == own
}

// tbmAcceptChat keeps only system messages: the module's chat-transport replies are system lines, so a say, party
// or whisper line cannot forge a TBM: record.
func tbmAcceptChat(l chat.Line) bool { return l.Type == chat.TypeSystem }

// record appends one accepted message to the raw case capture, one recv record per message. A capture file is for
// a live run's evidence and never goes to stdout.
func (a *tbmAdapter) record(in tbmproto.Incoming) {
	if a.raw == nil {
		return
	}
	if in.Kind == "addon" {
		fmt.Fprintf(a.raw, "recv addon %s %s %s\n", in.Channel, in.Prefix, caseField(in.Text))
		return
	}
	fmt.Fprintf(a.raw, "recv system %s\n", caseField(in.Text))
}

// caseField escapes one case-file field: a backslash and a line feed, so a whole message stays one record.
var caseField = strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace

// tbmCheck is forever tbm-check: the setup step, then the client's hello and the requested commands, printed
// without identifiers. The module answers no-bots to every command while the character leads no party, so the
// party is formed before the hello and unmade after it.
func (s *session) tbmCheck(ctx context.Context) error {
	// The cleanup runs last (registration order: the adapter's close is deferred after this one), so the party's
	// removal has the command channel to itself.
	party := &tbmParty{s: s}
	defer party.cleanup(ctx)
	if err := party.form(ctx); err != nil {
		return err
	}

	a := newTBMAdapter(s)
	defer a.close()
	if path := s.opts.TBMRaw; path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("tbm raw: %w", err)
		}
		defer f.Close()
		a.raw = f
	}

	cli := tbmclient.New(a)
	switch s.opts.TBMTransport {
	case "addon":
		cli.SetTransport(tbmproto.TransportAddon)
	case "addon-self":
		cli.SetTransport(tbmproto.TransportAddonSelf)
	case "chat":
		cli.SetTransport(tbmproto.TransportChat)
	}

	timeout := s.opts.TBMTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	hello, err := cli.Hello(ctx, timeout)
	if err != nil {
		printTBMHello(s, hello)
		return fmt.Errorf("tbm hello: %w", err)
	}
	printTBMHello(s, hello)

	for _, cmd := range s.opts.TBMCommands {
		res, err := cli.Do(ctx, cmd, timeout)
		printTBMResult(s, cmd, res)
		if err != nil {
			return fmt.Errorf("tbm do %q: %w", cmd, err)
		}
	}
	return nil
}

// The setup step's bounds. A bot's login is the module's: dungeon-play waits a minute for the same `.bot add` to
// put it in the party, and a join not seen by then is a failure, not a slower run.
const (
	tbmBotWaitDefault = 60 * time.Second
	// tbmBotPoll is one pump slice of the join wait: the packet loop is driven this long before the roster is read
	// again, so a join is noticed within a fraction of a second.
	tbmBotPoll = 250 * time.Millisecond
)

// errTBMParty is the fail-fast answer of a check with no bots to add and no party to speak on: without a party the
// module answers no-bots to the hello's commands, so three no-bots reports are not a check.
var errTBMParty = errors.New("no party: pass --bots")

// tbmParty is tbm-check's setup step. The bots --bots names are added with the module's own `.bot add` (the
// owner command dungeon-play's FORM state uses), the wait polls the party model until every one is in it, and the
// cleanup removes the bots this check added with `.bot remove` unless --keep-bots. A bot that was already in the
// party is never removed: only this check's own adds are its to take back.
type tbmParty struct {
	s *session
	// added are the bots this check's own `.bot add` brought into the party, in order.
	added []string
}

// form settles the login and forms the party. No --bots with no party is the fail-fast case; with --bots the names
// are added in order and the party is read until every one joined, bounded by TBMBotWait.
func (p *tbmParty) form(ctx context.Context) error {
	s := p.s
	s.initChat()
	if settle := s.opts.Settle; settle > 0 {
		if err := s.collect(ctx, settle); err != nil {
			return err
		}
	}
	names := s.opts.TBMBots
	if len(names) == 0 {
		if !s.party.InParty {
			return errTBMParty
		}
		return nil
	}
	o := newTBMOwner(s)
	for i, name := range names {
		rep, err := o.add(ctx, name)
		switch {
		case err == nil:
			p.added = append(p.added, name)
			s.opts.logf("tbm bots: add bot%d: ok", i+1)
		case errors.Is(err, botcmd.ErrRefused):
			if rep.Code == "already-online" {
				if s.partyLists(name) {
					s.opts.logf("tbm bots: add bot%d: already in the party", i+1)
					continue
				}
				return fmt.Errorf("tbm bots: bot%d is already online but not in this party", i+1)
			}
			return fmt.Errorf("tbm bots: bot%d was refused (%s)", i+1, rep.Code)
		default:
			return fmt.Errorf("tbm bots: bot%d was not added: %w", i+1, err)
		}
	}
	wait := s.opts.TBMBotWait
	if wait <= 0 {
		wait = tbmBotWaitDefault
	}
	if err := s.tbmWaitParty(ctx, names, wait); err != nil {
		return err
	}
	s.opts.logf("tbm bots: added=%d in-party=%d", len(p.added), len(names))
	return nil
}

// tbmWaitParty drives the packet loop until every named bot is in the party, or the bound runs out. The first
// slot the roster does not list is the one the failure names: a roster that never appears is the first bot's.
func (s *session) tbmWaitParty(ctx context.Context, names []string, limit time.Duration) error {
	deadline := s.Now().Add(limit)
	for {
		if i := s.tbmMissingParty(names); i > 0 {
			if !s.Now().Before(deadline) {
				return fmt.Errorf("tbm bots: bot%d did not join the party within %s", i, limit)
			}
			if err := s.collect(ctx, tbmBotPoll); err != nil {
				return err
			}
			continue
		}
		return nil
	}
}

// tbmMissingParty is the 1-based slot of the first named bot the party roster does not list, 0 when every one is
// listed. A party the model has not accepted yet (no roster, or one it refused) lists nobody.
func (s *session) tbmMissingParty(names []string) int {
	s.initChat()
	if !s.party.InParty || !s.party.RosterValid {
		return 1
	}
	for i, name := range names {
		if !s.partyLists(name) {
			return i + 1
		}
	}
	return 0
}

// partyLists reports whether the accepted party roster lists a member of that name.
func (s *session) partyLists(name string) bool {
	if !s.party.InParty || !s.party.RosterValid {
		return false
	}
	for _, m := range s.party.SnapshotOf() {
		if strings.EqualFold(m.Name, name) {
			return true
		}
	}
	return false
}

// cleanup removes the bots this check added, unless --keep-bots: `.bot remove` is the module's own-account removal
// (a character removes the bots it owns; a GM's remove reaches any bot). Best effort: the context is detached from
// the run's, so a failing hello or the run's deadline still cleans up, and a failed removal is counted and logged
// without hiding the check's own result. It never touches a bot the check did not add.
func (p *tbmParty) cleanup(ctx context.Context) {
	if len(p.added) == 0 {
		return
	}
	s := p.s
	if s.opts.TBMKeepBots {
		s.opts.logf("tbm bots: keep=true, %d bot(s) left in the party", len(p.added))
		return
	}
	ctx = context.WithoutCancel(ctx)
	o := newTBMOwner(s)
	removed, failed := 0, 0
	for i, name := range p.added {
		outcome := o.remove(ctx, name)
		if outcome == tbmRemoved || outcome == tbmGone {
			removed++
		} else {
			failed++
		}
		s.opts.logf("tbm bots: remove bot%d: %s", i+1, outcome)
	}
	s.opts.logf("tbm bots: cleanup removed=%d failed=%d", removed, failed)
}

// The cleanup's outcomes, printed by slot so a log line names no character.
const (
	tbmRemoved  = "removed"
	tbmGone     = "gone"
	tbmRefused  = "refused"
	tbmNoAnswer = "no-answer"
)

// tbmOwner is tbm-check's owner-command channel: the module's own `.bot add` and `.bot remove` a normal player
// (no GM) runs, built by the v1 builder so only the typed requests get through.
type tbmOwner struct {
	s *session
	q *command.Session
	c *botcmd.Client
}

func newTBMOwner(s *session) *tbmOwner {
	o := &tbmOwner{s: s}
	o.q = command.NewSession(&tbmOwnerLink{cmdLink: cmdLink{s: s}})
	o.c = botcmd.NewClient(o.q)
	o.c.Base = s.commandOptions(command.Options{Quiet: playReplyQuiet, Deadline: playReplyDeadline})
	return o
}

// add sends `.bot add` and reads its v1 reply: an ok, or a refusal the caller decides on.
func (o *tbmOwner) add(ctx context.Context, bot string) (botcmd.Reply, error) {
	return o.c.Add(ctx, bot)
}

// remove sends `.bot remove` and classifies the module's answer: the removal requested (or a hire ended), the
// character already gone, a refusal, or no answer at all.
func (o *tbmOwner) remove(ctx context.Context, bot string) string {
	line := ".bot remove " + bot
	if !tbmOwnerLineAllowed(line) {
		return tbmRefused
	}
	rep, err := o.q.Run(ctx, line, o.s.commandOptions(command.Options{Quiet: playReplyQuiet, Deadline: playReplyDeadline}))
	if err != nil || rep == nil {
		return tbmNoAnswer
	}
	if rep.Echoed {
		return tbmRefused
	}
	lines := rep.RawText
	if len(lines) == 0 {
		lines = rep.Raw
	}
	for _, l := range lines {
		switch {
		case strings.Contains(l, "Removal requested for bot"), strings.Contains(l, "Hire ended for "):
			return tbmRemoved
		case strings.Contains(l, "not found"), strings.Contains(l, "not a module-owned bot"), strings.Contains(l, "not removable"):
			return tbmGone
		case strings.Contains(l, "You may only control characters on your account"):
			return tbmRefused
		}
	}
	return tbmNoAnswer
}

// tbmOwnerLink is tbm-check's command transport: the session's own pump, and a send that admits the two owner
// commands of the setup step. It is the serve channel's owner rule with this tool's narrower vocabulary: a `.bot`
// line of the two verbs passes with no GM confirmation (a player's own bots are the account's), every other command
// text is refused, and the name is the v1 builder's normalised form, so a typed or multi-word line never goes out.
type tbmOwnerLink struct{ cmdLink }

func (l tbmOwnerLink) SendCommand(ctx context.Context, text string) error {
	if l.s.gmStop != nil {
		return l.s.gmStop
	}
	if !tbmOwnerLineAllowed(text) {
		return fmt.Errorf("world: tbm-check sends only .bot add and .bot remove, not %q", chat.Sanitise(text, 64))
	}
	return l.s.SendCommandCtx(ctx, text)
}

// tbmOwnerLineAllowed is the setup step's whole grammar: `.bot add <name>` or `.bot remove <name>`, the name one
// word the v1 builder writes exactly as given (so it is a normalised character name and nothing else).
func tbmOwnerLineAllowed(text string) bool {
	fields := strings.Split(text, " ")
	if len(fields) != 3 || fields[0] != ".bot" || (fields[1] != "add" && fields[1] != "remove") {
		return false
	}
	line, err := botcmd.Line(botcmd.VerbAdd, fields[2], "")
	return err == nil && line == ".bot add "+fields[2]
}

// printTBMHello prints the trailer's non-identifier fields.
func printTBMHello(s *session, h tbmproto.Hello) {
	check := "ok"
	if err := h.Check(); err != nil {
		check = err.Error()
	}
	verbs := "(none)"
	if h.HasVerbs {
		verbs = strings.Join(h.Verbs, ",")
	}
	s.opts.logf("tbm hello: complete=%t transport=%q version=%q proto=%d min-addon=%d verbs=%s caps=%v latest=%q/%q check=%s",
		h.Complete, h.Transport, h.Version, h.Proto, h.MinAddon, verbs, h.Caps, h.LatestModule, h.LatestAddon, check)
}

// printTBMResult prints the result's counts and codes, never a name, a GUID or a location.
func printTBMResult(s *session, cmd string, r tbmproto.Result) {
	s.opts.logf("tbm %q: roster=%d committed=%t altbots=%d botstate=%d acks=%d errs=%d problems=%d unknown=%d ignored=%d",
		cmd, len(r.Roster), r.RosterCommitted, len(r.AltBots), len(r.BotState), len(r.Acks), len(r.Errs), len(r.Problems), len(r.Unknown), r.Ignored)
	for i, ack := range r.Acks {
		s.opts.logf("  ack[%d]: intent=%q scope=%q count=%d", i, ack.Intent, ack.Scope, ack.Count)
	}
	for i, e := range r.Errs {
		s.opts.logf("  err[%d]: intent=%q code=%q", i, e.Intent, e.Code)
	}
	for i, p := range r.Problems {
		s.opts.logf("  problem[%d]: %s", i, p.Code)
	}
	if r.RosterError != nil {
		s.opts.logf("  roster-error: code=%q", r.RosterError.Code)
	}
}
