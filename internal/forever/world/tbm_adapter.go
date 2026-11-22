package world

// The TBM client's side of the Forever session: the adapter the tbmclient.Conn interface needs (prefix registration,
// the add-on send, the whisper to self, and the next delivered message), and the tbm-check command's body. The
// protocol itself lives in internal/forever/tbmclient; this file only moves the session's packets and chat feed.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/tbmclient"
	"github.com/pjerra/forever-agent/internal/forever/tbmproto"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// tbmAdapter satisfies tbmclient.Conn on top of one session. The chat feed is the same cmdFeed the command runner
// uses: while a TBM check runs, every chat line the session receives is queued here for the client's Next.
type tbmAdapter struct {
	s    *session
	feed []chat.Line
	prev func(chat.Line)
}

// newTBMAdapter builds the adapter and takes over the session's chat feed. close restores the previous feed.
func newTBMAdapter(s *session) *tbmAdapter {
	a := &tbmAdapter{s: s, prev: s.cmdFeed}
	s.cmdFeed = func(line chat.Line) {
		a.feed = append(a.feed, line)
		if a.prev != nil {
			a.prev(line)
		}
	}
	return a
}

func (a *tbmAdapter) close() { a.s.cmdFeed = a.prev }

// RegisterAddonPrefixes sends the prefix registration packets for the TBM prefix.
func (a *tbmAdapter) RegisterAddonPrefixes(prefixes ...string) error {
	return a.s.RegisterAddonPrefixes(prefixes...)
}

// SendAddon sends one add-on message through the session's add-on path.
func (a *tbmAdapter) SendAddon(ch addonmsg.Channel, target, prefix, text string, logged bool) error {
	return a.s.SendAddon(ch, target, prefix, text, logged)
}

// WhisperSelf sends the chat command as a whisper to this character's own name. The chat transport has no other
// way to reach the module, and the core delivers a self whisper back to the sender.
func (a *tbmAdapter) WhisperSelf(text string) error {
	name := a.s.report.Character.Name
	if name == "" {
		return fmt.Errorf("tbm: the character's name is not known yet")
	}
	lang, err := a.s.chatLanguage()
	if err != nil {
		return err
	}
	return a.s.Send(wire.CMSGChatMessageWhisper, chat.BuildWhisper(lang, name, text))
}

// Next returns the next message for the client: a delivered add-on message first, then a chat line. It drives the
// session's packet pump for a short slice so the queues actually fill.
func (a *tbmAdapter) Next(ctx context.Context) (tbmproto.Incoming, error) {
	a.s.initAddon()
	for {
		if m, ok := a.s.addon.Next(); ok {
			return tbmproto.Incoming{
				Kind: "addon", Channel: addonChannelName(m), Prefix: m.Prefix, Text: m.Text,
			}, nil
		}
		if len(a.feed) > 0 {
			line := a.feed[0]
			a.feed = a.feed[1:]
			return tbmproto.Incoming{Kind: "system", Text: line.Text}, nil
		}
		if err := a.s.collect(ctx, 50*time.Millisecond); err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return tbmproto.Incoming{}, tbmclient.ErrTimeout
			}
			return tbmproto.Incoming{}, err
		}
	}
}

// tbmCheck is forever tbm-check: the client's hello and the requested commands, printed without identifiers.
func (s *session) tbmCheck(ctx context.Context) error {
	a := newTBMAdapter(s)
	defer a.close()

	cli := tbmclient.New(a)
	switch s.opts.TBMTransport {
	case "addon":
		cli.SetTransport(tbmproto.TransportAddon)
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
		if err != nil && !errors.Is(err, tbmclient.ErrTimeout) {
			return fmt.Errorf("tbm do %q: %w", cmd, err)
		}
	}
	return nil
}

// printTBMHello prints the trailer's non-identifier fields.
func printTBMHello(s *session, h tbmproto.Hello) {
	check := "ok"
	if err := h.Check(); err != nil {
		check = err.Error()
	}
	s.opts.logf("tbm hello: complete=%t transport=%q version=%q proto=%d min-addon=%d caps=%v latest=%q/%q check=%s",
		h.Complete, h.Transport, h.Version, h.Proto, h.MinAddon, h.Caps, h.LatestModule, h.LatestAddon, check)
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
