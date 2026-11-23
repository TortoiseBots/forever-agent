package world

// The add-on message side of the session (round A0): registration, sending on the group kinds and the whisper (a
// channel is received but not sent: see addonmsg.Build), the receive feed into the bounded inbox, and forever
// addon-check. The wire layouts and limits live in
// internal/forever/addonmsg; this file only moves them over the session and prints what arrived.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/pbc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// AddonSummary is what the run saw of the add-on transport, for the command's closing lines.
type AddonSummary struct {
	Registered []string
	// RegistrationUnknown is set when a registration send failed after the unregister-all packet: Registered is
	// then empty because nothing can be vouched for, not because the client registered nothing.
	RegistrationUnknown bool
	// Delivered is every message the command drained; Messages holds at most maxAddonKept of them and NotKept
	// counts the rest, so a long listen or a flood cannot grow the report without bound.
	Delivered    int
	NotKept      int
	Unregistered int
	Dropped      int
	Bad          int
	QueueCap     int
	Channels     map[string]int
	// Messages are the first drained messages in arrival order, at most maxAddonKept.
	Messages []addonmsg.Message
}

// maxAddonListen bounds --listen for add-on-check, like maxChatListen does for chat-check.
const maxAddonListen = 2 * time.Minute

// maxAddonKept is how many drained messages the report keeps; Delivered and NotKept still count every one.
const maxAddonKept = addonmsg.MaxQueue

// maxAddonLogLines bounds the per-message receive lines a run prints; the totals follow at the end.
const maxAddonLogLines = 12

// addonDrained is the command's take from the inbox: the first maxAddonKept messages and the total count.
type addonDrained struct {
	messages []addonmsg.Message
	total    int
}

// add records one drained message, keeping the first maxAddonKept.
func (d *addonDrained) add(m addonmsg.Message) {
	d.total++
	if len(d.messages) < maxAddonKept {
		d.messages = append(d.messages, m)
	}
}

// initAddon creates the inbox on first use.
func (s *session) initAddon() {
	if s.addon == nil {
		s.addon = addonmsg.NewInbox(addonmsg.MaxQueue)
	}
}

// addonRegistrationState is what this session knows of the prefix list the server holds. A fresh session knows it:
// its own list and the server's are both empty. The unregister-all packet empties the server's list, so the local
// list is emptied with it; a registration send that fails afterwards leaves the two lists possibly different (a
// write error does not prove the server refused the packet), and that is the unknown state: no prefix is reported
// as registered, an arrived message is counted with the unregistered ones instead of delivered, and a send that
// waits for an answer is refused until a full unregister+register succeeds.
type addonRegistrationState int

const (
	addonRegistrationKnown addonRegistrationState = iota
	addonRegistrationUnknown
)

// ErrAddonOff is a registration asked of a session started with the add-on off (--addon off): such a session never
// registers a prefix, and so never speaks TBM or a bot module.
var ErrAddonOff = errors.New("world: add-on is off for this session")

// RegisterAddonPrefixes replaces this client's registered prefix list. The server appends to its own list, so the
// unregister-all packet goes first (the order the core's own comment expects, WorldSession.cpp:1302-1313); the
// local list is cleared as soon as that packet left, and set to the new list only once the registration packet
// left too. A failed registration packet therefore never leaves the old prefixes exposed as registered: the state
// is unknown until a later call completes both packets. A session with the add-on off refuses everything here.
func (s *session) RegisterAddonPrefixes(prefixes ...string) error {
	if s.opts.AddonOff {
		return ErrAddonOff
	}
	s.initAddon()
	list, err := addonmsg.NormalisePrefixes(prefixes)
	if err != nil {
		return err
	}
	payload, err := addonmsg.BuildRegister(list)
	if err != nil {
		return err
	}
	if err := s.Send(wire.CMSGChatUnregisterAllAddonPrefixes, addonmsg.BuildUnregisterAll()); err != nil {
		return err
	}
	// The unregister-all packet left: the server's list is empty whatever happens next, so the local list goes
	// with it at once. Nothing is registered now, and a failed second send cannot be reported as a list.
	s.addon.UnregisterAll()
	s.addonState = addonRegistrationUnknown
	if err := s.Send(wire.CMSGChatRegisterAddonPrefixes, payload); err != nil {
		return err
	}
	if err := s.addon.SetPrefixes(list); err != nil {
		return err
	}
	s.addonState = addonRegistrationKnown
	return nil
}

// SendAddon sends one add-on message. The target is the whisper name and is ignored for the group kinds. Everything
// the server checks before the switch is checked here first and refused with a named error, so a bad prefix, text
// or channel never reaches the socket. A send waits for the peer's answer, and a peer only answers a registered
// prefix: while the registration state is unknown the send is refused with ErrRegistrationUnknown instead of
// being sent into a list this client cannot name.
func (s *session) SendAddon(ch addonmsg.Channel, target, prefix, text string, logged bool) error {
	if s.addonState == addonRegistrationUnknown {
		return addonmsg.ErrRegistrationUnknown
	}
	core, payload, err := addonmsg.Build(ch, target, prefix, text, logged)
	if err != nil {
		return err
	}
	return s.Send(core, payload)
}

// offerAddon records one arrived add-on line. A message from a prefix this client did not register is counted and
// never reaches a caller. While the registration state is unknown nothing can be vouched for, so every arrived
// message is counted with the unregistered ones instead of being delivered. The display line names the channel,
// the prefix, the sender and the text; it carries no GUID. The stored message keeps the text's exact bytes; the
// printed text goes through the display sanitiser, and the per-message lines are rate-limited (see logAddon): a
// flooding peer costs at most maxAddonLogLines lines.
func (s *session) offerAddon(line chat.Line) {
	s.initAddon()
	m, err := addonmsg.FromLine(line)
	if err != nil {
		s.addon.Bad++
		s.logAddon("addon: refused a malformed message: %v", err)
		return
	}
	prefix := chat.Sanitise(m.Prefix, addonmsg.MaxPrefix)
	from := s.chatName(m.SenderName, m.Sender)
	if m.Prefix == pbc.Prefix && !s.fbFrameAllowed(m) {
		// The module answers with a self-addressed WHISPER add-on message (AddonControl.cpp:202-221: sender and
		// receiver are the same player, prefix PBC). A frame from another character, or on any other channel, is
		// not that answer: drop it before the assembler, count it, and leave the protocol on.
		s.addon.Total++
		s.pbcForged++
		s.logAddon("addon: %s %s from %s ignored (not this character's protocol whisper, counted)",
			addonChannelName(m), prefix, from)
		return
	}
	if s.addonState == addonRegistrationUnknown {
		s.addon.Total++
		s.addon.Unregistered++
		s.logAddon("addon: %s %s from %s ignored (the registration state is unknown, counted as unregistered)",
			addonChannelName(m), prefix, from)
		return
	}
	if !s.addon.Offer(m) {
		s.logAddon("addon: %s from %s ignored (prefix %s is not registered)", addonChannelName(m), from, prefix)
		return
	}
	if s.fbQuiet && m.Prefix == pbc.Prefix {
		// The bot module's protocol frames are data, not chat: a run counts them and prints its own one-line
		// totals instead of the frame text of every part.
		s.fbFrames++
		return
	}
	s.eventAddon(addonMessageEvent{
		senderName: from, sender: m.Sender, prefix: prefix,
		channel: addonChannelName(m), text: chat.Sanitise(m.Text, chat.MaxDisplayText),
	})
	s.logAddon("addon: %s %s from %s: %s", addonChannelName(m), prefix, from, chat.Sanitise(m.Text, chat.MaxDisplayText))
}

// logAddon prints one receive line while the run's budget lasts and counts the rest; summariseAddon prints the
// totals at the end. The budget is per run, not per peer, so a flood cannot push the useful lines out of the log.
func (s *session) logAddon(format string, args ...any) {
	if s.addonLogged >= maxAddonLogLines {
		s.addonSuppressed++
		return
	}
	s.addonLogged++
	s.opts.logf(format, args...)
}

// addonChannelName names the channel without a wire number, except for a kind outside the table.
func addonChannelName(m addonmsg.Message) string {
	if m.Channel != addonmsg.Other {
		return m.Channel.String()
	}
	if name := chat.TypeName(m.ChatType); name != "" {
		return name
	}
	return fmt.Sprintf("chat type %d", m.ChatType)
}

// summariseAddon fills the report's add-on snapshot from the inbox and the messages the command drained. The
// totals are the run's last word on the receive feed: how many messages arrived and how many lines were withheld.
func (s *session) summariseAddon(drained addonDrained) {
	s.initAddon()
	sum := AddonSummary{
		Registered:          s.addon.Registered(),
		RegistrationUnknown: s.addonState == addonRegistrationUnknown,
		Delivered:           drained.total,
		NotKept:             drained.total - len(drained.messages),
		Unregistered:        s.addon.Unregistered,
		Dropped:             s.addon.Dropped,
		Bad:                 s.addon.Bad,
		QueueCap:            s.addon.Capacity(),
		Channels:            map[string]int{},
		Messages:            drained.messages,
	}
	for ch, n := range s.addon.Counts {
		sum.Channels[ch.String()] = n
	}
	if s.addon.Total > 0 {
		s.opts.logf("addon: %d message(s) seen; %d line(s) printed, %d not printed",
			s.addon.Total, s.addonLogged, s.addonSuppressed)
	}
	s.report.Addon = sum
}

// addonCheck drives forever addon-check: settle, register the prefix, send one message, then listen and print what
// arrives for the registered prefix. Link death ends the listen with the session's own error.
func (s *session) addonCheck(ctx context.Context) error {
	s.initAddon()
	if err := addonmsg.ValidatePrefix(s.opts.AddonPrefix); err != nil {
		return err
	}
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 4 * time.Second
	}
	if err := s.collect(ctx, settle); err != nil {
		return err
	}

	var drained addonDrained
	defer func() { s.summariseAddon(drained) }()

	if err := s.RegisterAddonPrefixes(s.opts.AddonPrefix); err != nil {
		return fmt.Errorf("world: registering the add-on prefix: %w", err)
	}
	s.opts.logf("addon-check: registered prefix %s (unregister-all, then register)", chat.Sanitise(s.opts.AddonPrefix, addonmsg.MaxPrefix))

	// Registration comes before the party step on purpose: both sessions are then registered by the time the
	// group exists, so the first party message cannot race the peer's registration.
	if err := s.formAddonParty(ctx); err != nil {
		return err
	}

	if s.opts.AddonSend != "" {
		spec := s.opts.AddonChannel
		if spec == "" {
			spec = "party"
		}
		ch, target, err := addonmsg.ParseChannel(spec)
		if err != nil {
			return err
		}
		if err := s.SendAddon(ch, target, s.opts.AddonPrefix, s.opts.AddonSend, false); err != nil {
			return fmt.Errorf("world: sending the add-on message: %w", err)
		}
		where := ch.String()
		if target != "" {
			where += ":" + chat.SanitiseName(target)
		}
		s.opts.logf("addon-check: sent %d byte(s) on %s, prefix %s", len(s.opts.AddonSend), where,
			chat.Sanitise(s.opts.AddonPrefix, addonmsg.MaxPrefix))
	}

	listen := s.opts.AddonListen
	if listen <= 0 {
		listen = 10 * time.Second
	}
	if listen > maxAddonListen {
		return fmt.Errorf("world: --listen is over %s", maxAddonListen)
	}
	// The deadline is on the session's own clock, like every other wait of the session; it never reads the wall
	// clock directly.
	deadline := s.Now().Add(listen)
	s.opts.logf("addon-check: listening %s", listen)
	for {
		for {
			m, ok := s.addon.Next()
			if !ok {
				break
			}
			drained.add(m)
		}
		remaining := deadline.Sub(s.Now())
		if remaining <= 0 {
			break
		}
		if remaining > 200*time.Millisecond {
			remaining = 200 * time.Millisecond
		}
		if err := s.collect(ctx, remaining); err != nil {
			return fmt.Errorf("world: the add-on listen ended: %w", err)
		}
	}
	for {
		m, ok := s.addon.Next()
		if !ok {
			break
		}
		drained.add(m)
	}
	if s.opts.AddonInvite != "" || s.opts.AddonAcceptFrom != "" {
		if err := s.leaveAddonParty(ctx); err != nil {
			return fmt.Errorf("world: leaving the add-on party: %w", err)
		}
	}
	return nil
}

// formAddonParty is party-check's invite/accept pair without the party line, member states or the stay: the group
// has to exist while the add-on exchange runs. Only one side may be named.
func (s *session) formAddonParty(ctx context.Context) error {
	if s.opts.AddonInvite != "" && s.opts.AddonAcceptFrom != "" {
		return errors.New("world: --invite and --accept-from are different sides of the same invite; choose one")
	}
	if s.opts.AddonInvite != "" {
		name, err := chat.NormaliseName(s.opts.AddonInvite)
		if err != nil {
			return err
		}
		before := s.party.CommandResults
		if err := s.Send(wire.CMSGPartyInvite, party.BuildInvite(name)); err != nil {
			return err
		}
		s.opts.logf("addon-check: invited %s, waiting for the group", chat.SanitiseName(name))
		if err := s.waitPartyCommand(ctx, "the invite answer", commandWait, before, 0, name); err != nil {
			return err
		}
		wait := s.opts.WaitJoin
		if wait <= 0 || wait > acceptWait {
			wait = acceptWait
		}
		if err := s.waitPartyJoined(ctx, wait, s.party.InvalidRosters+s.party.UnexpectedRosters, 2); err != nil {
			return err
		}
		s.opts.logf("addon-check: grouped, %d member(s)", len(s.party.Members))
		return nil
	}
	if s.opts.AddonAcceptFrom == "" {
		return nil
	}
	want, err := chat.NormaliseName(s.opts.AddonAcceptFrom)
	if err != nil {
		return err
	}
	if s.party.Invite == nil || !strings.EqualFold(s.party.Invite.InviterName, want) {
		s.opts.logf("addon-check: waiting up to %s for an invite from %s", inviteWait, chat.SanitiseName(want))
		if err := s.waitInvite(ctx, want, inviteWait); err != nil {
			return err
		}
	}
	if !s.party.Invite.CanAccept {
		return fmt.Errorf("world: the invite from %s says it cannot be accepted", chat.SanitiseName(want))
	}
	badBefore := s.party.InvalidRosters + s.party.UnexpectedRosters
	if err := s.Send(wire.CMSGPartyInviteResponse, party.BuildInviteResponse(true)); err != nil {
		return err
	}
	s.party.AcceptInvite()
	s.opts.logf("addon-check: accepted the invite from %s", chat.SanitiseName(want))
	if err := s.waitPartyJoined(ctx, acceptWait, badBefore, 1); err != nil {
		return err
	}
	s.opts.logf("addon-check: grouped, %d member(s)", len(s.party.Members))
	return nil
}

// leaveAddonParty ends the group the add-on run formed, so the server is left as it was found. Only a valid
// roster is acted on, as everywhere else. A lost link is returned to the run: it must end with the link error, not
// with a clean report. A leave that simply times out stays a logged note, as it was.
func (s *session) leaveAddonParty(ctx context.Context) error {
	if !s.party.RosterValid {
		s.opts.logf("addon-check: no leave sent: no valid roster")
		return nil
	}
	if err := s.Send(wire.CMSGLeaveGroup, party.BuildLeaveGroup()); err != nil {
		if errors.Is(err, ErrLinkLost) {
			return err
		}
		s.opts.logf("addon-check: leave: %v", err)
		return nil
	}
	if err := s.waitPartyLeft(ctx, 10*time.Second); err != nil {
		if errors.Is(err, ErrLinkLost) {
			return err
		}
		s.opts.logf("addon-check: leave: %v", err)
		return nil
	}
	s.opts.logf("addon-check: left the group")
	if err := s.collect(ctx, 500*time.Millisecond); err != nil {
		if errors.Is(err, ErrLinkLost) {
			return err
		}
		s.opts.logf("addon-check: the leave's trailing packets: %v", err)
	}
	return nil
}
