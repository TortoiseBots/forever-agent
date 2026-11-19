package world

// Round A1: the Forever Bots side of the session. `forever fb-check` adds bots with the module's chat commands,
// reads state through the module's add-on protocol (package pbc), orders bots by whisper and party chat, and
// dismisses them. The typed command layer is internal/forever/command/fbcmd; this file is its Transport: the
// command channel, the whisper line and its acknowledgement, party chat, and one protocol request over the merged
// add-on transport.
//
// The module answers the protocol only for an account that owns a bot and only when the client is grouped (the
// request travels as a party add-on message), so the first step of the run is `.bot add`: the server creates the
// bot, logs it in and groups it itself.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/fbcmd"
	"github.com/pjerra/forever-agent/internal/forever/pbc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// fbAddWait is how long an add waits for the bot to appear in the party. The server creates the character, logs
	// it in and groups it, which takes a few seconds on a real realm.
	fbAddWait = 30 * time.Second
	// fbDismissWait bounds the wait for the party to drop a dismissed bot.
	fbDismissWait = 10 * time.Second
	// fbWhisperPoll is one pump step while a whisper or a protocol answer is awaited.
	fbWhisperPoll = 100 * time.Millisecond
	// fbMaxFrames bounds the protocol's part bookkeeping for one request.
	fbMaxFrames = 256
)

// FBAdded is one add step.
type FBAdded struct {
	Name  string
	Class string
	Role  string
	Line  string
	Err   string
}

// FBOrder is one order step (whisper or party word).
type FBOrder struct {
	Bot     string
	Word    string
	Text    string
	Refused bool
	Err     string
}

// FBReport is what fb-check observed.
type FBReport struct {
	About        string
	Language     string
	LanguageErr  string
	Added        []FBAdded
	Roster       []pbc.RosterEntry
	RosterSource string // "pbc", "list" or "" (not read)
	Details      []pbc.BotDetail
	DetailErr    string
	Orders       []FBOrder
	PartyOrders  []FBOrder
	LastRule     string
	Dismissed    []string
	DismissErr   string
	PBC          string // "ok" or "unavailable"
	PBCReason    string
	Frames       int
}

// fbLink is fbcmd's Transport over the session.
type fbLink struct{ s *session }

// Run sends one chat-command line through the session's command channel. The module's commands need no permission
// probe: the run proves the module is there with `.bot about` instead (see fbCheck).
func (l fbLink) Run(ctx context.Context, line string, opts command.Options) (*command.Reply, error) {
	if err := l.s.initFB(); err != nil {
		return nil, err
	}
	return l.s.fbQueue.Run(ctx, line, l.s.commandOptions(opts))
}

// Whisper says one line to a character and returns the whisper that character answers with within window. A bot
// answers an accepted order with "<its name>: <text>"; the server fills a whisper's sender name, so the answer is
// matched by name.
func (l fbLink) Whisper(ctx context.Context, target, text string, window time.Duration) (string, error) {
	name, err := chat.NormaliseName(target)
	if err != nil {
		return "", fmt.Errorf("world: whisper target: %w", err)
	}
	if err := chat.ValidateText(text); err != nil {
		return "", err
	}
	lang, err := l.s.chatLanguage()
	if err != nil {
		return "", err
	}
	if err := l.s.Send(wire.CMSGChatMessageWhisper, chat.BuildWhisper(lang, name, text)); err != nil {
		return "", err
	}
	deadline := l.s.Now().Add(window)
	got := ""
	feed := func(line chat.Line) {
		if got != "" || line.Type != chat.TypeWhisper {
			return
		}
		if !strings.EqualFold(strings.TrimSpace(line.SenderName), name) {
			return
		}
		got = line.Text
	}
	l.s.cmdFeed = feed
	defer func() { l.s.cmdFeed = nil }()
	for got == "" {
		remaining := deadline.Sub(l.s.Now())
		if remaining <= 0 {
			return "", fmt.Errorf("fbcmd: %s answered no whisper within %s", chat.SanitiseName(name), window)
		}
		if remaining > fbWhisperPoll {
			remaining = fbWhisperPoll
		}
		if err := l.s.collect(ctx, remaining); err != nil {
			return "", err
		}
	}
	return got, nil
}

// PartySay says one line in party chat.
func (l fbLink) PartySay(ctx context.Context, text string) error {
	if err := chat.ValidateText(text); err != nil {
		return err
	}
	lang, err := l.s.chatLanguage()
	if err != nil {
		return err
	}
	if err := l.s.Send(wire.CMSGChatMessageParty, chat.BuildParty(lang, text)); err != nil {
		return err
	}
	return l.s.collect(ctx, 400*time.Millisecond)
}

// Request sends one protocol request on the party add-on channel and waits for its answer. Every failure that
// proves the protocol unusable turns it off for the session (fbcmd.ErrPBCOff): the caller then falls back to the
// chat commands and says so. The server's pushes are not answers; an error push that names the bot a detail
// request asked about is that request's refusal.
func (l fbLink) Request(ctx context.Context, req fbcmd.Request) (fbcmd.Answer, error) {
	s := l.s
	if s.pbcOff != "" {
		return fbcmd.Answer{}, fmt.Errorf("%w: %s", fbcmd.ErrPBCOff, s.pbcOff)
	}
	if err := s.initFB(); err != nil {
		return fbcmd.Answer{}, err
	}
	if s.addonState != addonRegistrationKnown || !s.addon.Has(pbc.Prefix) {
		return fbcmd.Answer{}, s.pbcUnavailable("the prefix is not registered")
	}
	seq := s.nextPBCSeq()
	parts, err := pbc.BuildRequest(seq, req.Type, req.Fields)
	if err != nil {
		return fbcmd.Answer{}, err
	}
	for _, part := range parts {
		if err := s.SendAddon(addonmsg.Party, "", pbc.Prefix, part, false); err != nil {
			return fbcmd.Answer{}, err
		}
	}
	wait := req.Wait
	if wait <= 0 {
		wait = fbcmd.DefaultAnswerWait
	}
	deadline := s.Now().Add(wait)
	for {
		for {
			m, ok := s.addon.Next()
			if !ok {
				break
			}
			if m.Prefix != pbc.Prefix {
				continue
			}
			part, err := pbc.ParsePart(m.Text)
			if err != nil {
				return fbcmd.Answer{}, s.pbcUnavailable(fmt.Sprintf("a malformed frame (%v)", err))
			}
			res, done, err := s.pbcAsm.Add(part, s.Now())
			if err != nil {
				return fbcmd.Answer{}, s.pbcUnavailable(fmt.Sprintf("a frame this client will not hold (%v)", err))
			}
			if !done {
				continue
			}
			if res.Seq >= pbc.FirstServerSeq {
				if res.Type == pbc.TypeEventPush {
					ev, err := pbc.ParseEvent(pbc.SplitFields(res.Body))
					if err != nil {
						return fbcmd.Answer{}, s.pbcUnavailable(fmt.Sprintf("an event this client cannot read (%v)", err))
					}
					if ev.Severity == "error" && req.BotID != 0 && ev.BotID == req.BotID {
						return fbcmd.Answer{}, &fbcmd.RefusalError{
							Err:  fbcmd.ErrRefused,
							Text: chat.Sanitise(ev.Name+": "+ev.Detail, 200),
						}
					}
				}
				continue
			}
			if res.Seq != seq {
				continue // an answer to an earlier request that timed out
			}
			if res.Type != req.Answer {
				return fbcmd.Answer{}, s.pbcUnavailable(fmt.Sprintf("an answer of type %s where %s was expected",
					chat.Sanitise(res.Type, 32), chat.Sanitise(req.Answer, 32)))
			}
			return fbcmd.Answer{Type: res.Type, Fields: pbc.SplitFields(res.Body)}, nil
		}
		s.pbcAsm.Expire(s.Now())
		remaining := deadline.Sub(s.Now())
		if remaining <= 0 {
			if s.pbcAsm.Pending() > fbMaxFrames {
				return fbcmd.Answer{}, s.pbcUnavailable("too many bodies under construction")
			}
			return fbcmd.Answer{}, s.pbcUnavailable(fmt.Sprintf("no answer within %s", wait))
		}
		if remaining > fbWhisperPoll {
			remaining = fbWhisperPoll
		}
		if err := s.collect(ctx, remaining); err != nil {
			return fbcmd.Answer{}, err
		}
	}
}

// pbcUnavailable turns the protocol off for the rest of the session and names the reason.
func (s *session) pbcUnavailable(reason string) error {
	if s.pbcOff == "" {
		s.pbcOff = chat.Sanitise(reason, 200)
	}
	return fmt.Errorf("%w: %s", fbcmd.ErrPBCOff, s.pbcOff)
}

// initFB creates the protocol's state and the command queue on first use.
func (s *session) initFB() error {
	if s.pbcAsm == nil {
		s.pbcAsm = pbc.NewAssembler()
	}
	if s.fbQueue == nil {
		s.fbQueue = command.NewSession(&cmdLink{s: s, probe: true})
	}
	if !s.addon.Has(pbc.Prefix) {
		return fmt.Errorf("%w: the prefix is not registered", fbcmd.ErrPBCOff)
	}
	return nil
}

// nextPBCSeq hands out request sequence numbers in 1..LastRequestSeq, wrapping without 0.
func (s *session) nextPBCSeq() uint32 {
	s.pbcSeq++
	if s.pbcSeq == 0 || s.pbcSeq > pbc.LastRequestSeq {
		s.pbcSeq = 1
	}
	return s.pbcSeq
}

// fbCheck drives `forever fb-check`: probe, language, add, roster and detail, orders by whisper and party word,
// dismiss, report. A step that the module refuses is reported and the run goes on to the next; a lost link or a
// missing module ends it.
func (s *session) fbCheck(ctx context.Context) error {
	s.initChat()
	s.fbQuiet = true
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 4 * time.Second
	}
	if err := s.collect(ctx, settle); err != nil {
		return err
	}
	if err := s.RegisterAddonPrefixes(pbc.Prefix); err != nil {
		return fmt.Errorf("world: registering the add-on prefix: %w", err)
	}
	s.opts.logf("fb-check: registered the add-on prefix %s", pbc.Prefix)

	link := fbLink{s: s}
	c := fbcmd.NewClient(link)
	c.Base = s.commandOptions(command.Options{})

	about, err := c.Probe(ctx)
	if err != nil {
		s.report.FB.PBCReason = "the module is absent"
		s.summariseFB()
		return fmt.Errorf("world: the bot module probe: %w", err)
	}
	s.report.FB.About = about
	s.opts.logf("fb-check: module: %s", about)

	if line, err := c.Language(ctx, "en"); err != nil {
		s.report.FB.LanguageErr = chat.Sanitise(err.Error(), 300)
		s.opts.logf("fb-check: language: %v", s.report.FB.LanguageErr)
	} else {
		s.report.FB.Language = line
		s.opts.logf("fb-check: language: %s", line)
	}

	adds := s.opts.FBAdd
	if len(adds) == 0 {
		adds = []string{"warrior:dps"}
	}
	var added []string
	for _, spec := range adds {
		class, role, err := parseFBAdd(spec)
		if err != nil {
			return err
		}
		res, err := c.Add(ctx, class, role)
		if err != nil {
			step := FBAdded{Class: class, Role: role, Err: chat.Sanitise(err.Error(), 300)}
			s.report.FB.Added = append(s.report.FB.Added, step)
			s.opts.logf("fb-check: add %s/%s: %v", class, roleWord(role), step.Err)
			continue
		}
		step := FBAdded{Name: res.Name, Class: class, Role: role, Line: res.Line}
		s.report.FB.Added = append(s.report.FB.Added, step)
		s.opts.logf("fb-check: add %s/%s: %s", class, roleWord(role), res.Line)
		if err := s.fbWaitMember(ctx, res.Name, fbAddWait); err != nil {
			s.summariseFB()
			return err
		}
		added = append(added, res.Name)
		s.opts.logf("fb-check: %s is in the party (%d member(s))", chat.SanitiseName(res.Name), len(s.party.Members))
	}

	if err := s.fbRoster(ctx, c); err != nil {
		s.summariseFB()
		return err
	}
	if s.report.FB.RosterSource == "pbc" && len(s.report.FB.Roster) > 0 {
		s.fbDetails(ctx, c, added)
	}

	orders := s.opts.FBOrders
	if len(orders) == 0 {
		orders = []string{"follow", "stay"}
	}
	targets := added
	if len(targets) == 0 && s.report.FB.RosterSource == "pbc" {
		for _, r := range s.report.FB.Roster {
			if r.Online {
				targets = append(targets, r.Name)
			}
		}
	}
	for _, word := range orders {
		if partyWord, ok := strings.CutPrefix(word, "party:"); ok {
			if partyWord == "" {
				partyWord = "follow"
			}
			if err := c.PartyOrder(ctx, partyWord); err != nil {
				if errors.Is(err, ErrLinkLost) {
					s.summariseFB()
					return err
				}
				s.report.FB.PartyOrders = append(s.report.FB.PartyOrders, FBOrder{Word: partyWord, Err: chat.Sanitise(err.Error(), 300)})
				s.opts.logf("fb-check: party %s: %v", partyWord, err)
				continue
			}
			s.report.FB.PartyOrders = append(s.report.FB.PartyOrders, FBOrder{Word: partyWord})
			s.opts.logf("fb-check: party %s: sent", partyWord)
			continue
		}
		for _, name := range targets {
			ack, err := c.Order(ctx, name, word)
			step := FBOrder{Bot: name, Word: word, Text: ack.Text, Refused: ack.Refused}
			if err != nil {
				if errors.Is(err, ErrLinkLost) {
					s.summariseFB()
					return err
				}
				step.Err = chat.Sanitise(err.Error(), 300)
				s.opts.logf("fb-check: whisper %s %q: %v", chat.SanitiseName(name), word, step.Err)
			} else if ack.Refused {
				s.opts.logf("fb-check: whisper %s %q: refused: %s", chat.SanitiseName(name), word, ack.Text)
			} else {
				s.opts.logf("fb-check: whisper %s %q: %s", chat.SanitiseName(name), word, ack.Text)
			}
			s.report.FB.Orders = append(s.report.FB.Orders, step)
		}
	}
	if len(s.report.FB.PartyOrders) > 0 {
		s.fbLastRule(ctx, c, added)
	}

	dismiss := s.opts.FBDismiss
	if dismiss == "" {
		dismiss = "all"
	}
	if len(s.report.FB.Roster) == 0 && len(added) == 0 {
		s.opts.logf("fb-check: dismiss: nothing to dismiss")
	} else {
		res, err := c.Dismiss(ctx, dismiss)
		if err != nil {
			if errors.Is(err, ErrLinkLost) {
				s.summariseFB()
				return err
			}
			s.report.FB.DismissErr = chat.Sanitise(err.Error(), 300)
			s.opts.logf("fb-check: dismiss %s: %v", chat.SanitiseName(dismiss), err)
		} else {
			s.report.FB.Dismissed = append(s.report.FB.Dismissed, res.Line)
			s.opts.logf("fb-check: dismiss %s: %s", chat.SanitiseName(dismiss), res.Line)
			for _, name := range added {
				if err := s.fbWaitAbsent(ctx, name, fbDismissWait); err != nil {
					s.summariseFB()
					return err
				}
			}
			s.opts.logf("fb-check: party now holds %d member(s)", len(s.party.Members))
		}
	}
	s.summariseFB()
	return nil
}

// fbRoster reads the roster. The protocol answers only inside a group and only for an owner; any failure turns it
// off (soft) and the chat list answers instead, with fewer fields and no ids.
func (s *session) fbRoster(ctx context.Context, c *fbcmd.Client) error {
	report := &s.report.FB
	if s.party.RosterValid {
		if err := c.Ping(ctx); err != nil {
			if errors.Is(err, ErrLinkLost) {
				return err
			}
			s.pbcUnavailable(err.Error())
			s.opts.logf("fb-check: pbc: %v", err)
		} else if rows, err := c.Roster(ctx); err != nil {
			if errors.Is(err, ErrLinkLost) {
				return err
			}
			s.pbcUnavailable(err.Error())
			s.opts.logf("fb-check: pbc: roster: %v", err)
		} else {
			report.Roster = rows
			report.RosterSource = "pbc"
			report.PBC = "ok"
			s.opts.logf("fb-check: pbc: roster of %d bot(s)", len(rows))
			s.fbPrintRoster()
			return nil
		}
	} else {
		s.pbcUnavailable("not grouped")
		s.opts.logf("fb-check: pbc: not grouped, so the protocol cannot answer")
	}
	report.PBC = "unavailable"
	report.PBCReason = s.pbcOff
	rows, err := c.List(ctx)
	if err != nil {
		if errors.Is(err, ErrLinkLost) {
			return err
		}
		s.opts.logf("fb-check: list: %v", err)
		return nil
	}
	for _, r := range rows {
		report.Roster = append(report.Roster, pbc.RosterEntry{
			Name: r.Name, Level: uint32(r.Level), Class: r.Class, Online: r.Online, Saved: r.Saved,
		})
	}
	report.RosterSource = "list"
	s.opts.logf("fb-check: list: %d bot(s) (fallback: pbc unavailable: %s)", len(rows), s.pbcOff)
	s.fbPrintRoster()
	return nil
}

// fbPrintRoster prints the roster in a display form.
func (s *session) fbPrintRoster() {
	for _, r := range s.report.FB.Roster {
		if r.ID == 0 && r.Role == "" {
			// The chat fallback: the fields it has.
			s.opts.logf("fb-check: roster: %s, level %d %s, %s%s",
				chat.SanitiseName(r.Name), r.Level, chat.Sanitise(r.Class, 32),
				onlineWord(r.Online), savedWord(r.Saved))
			continue
		}
		s.opts.logf("fb-check: roster: %s (id %d), level %d %s %s, %s, %s, %dhp %dmana, %s, %s, grouped=%s headless=%s saved=%s",
			chat.SanitiseName(r.Name), r.ID, r.Level, chat.Sanitise(r.Race, 32), chat.Sanitise(r.Class, 32),
			chat.Sanitise(r.Role, 32), onlineWord(r.Online), r.HPPercent, r.ManaPercent,
			chat.Sanitise(r.Intent, 64), chat.Sanitise(r.LastRule, 64),
			boolWord(r.Grouped), boolWord(r.Headless), boolWord(r.Saved))
	}
}

// fbDetails reads one detail per added bot, or the first online bot when nothing was added.
func (s *session) fbDetails(ctx context.Context, c *fbcmd.Client, added []string) {
	report := &s.report.FB
	want := map[string]bool{}
	for _, name := range added {
		want[strings.ToLower(name)] = true
	}
	var ids []uint32
	seen := map[uint32]bool{}
	for _, r := range report.Roster {
		if len(want) > 0 && !want[strings.ToLower(r.Name)] {
			continue
		}
		if len(want) == 0 && !r.Online {
			continue
		}
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		ids = append(ids, r.ID)
	}
	if len(want) == 0 && len(ids) > 1 {
		ids = ids[:1]
	}
	for _, id := range ids {
		d, err := c.Detail(ctx, id)
		if err != nil {
			s.pbcUnavailable(err.Error())
			report.DetailErr = chat.Sanitise(err.Error(), 300)
			s.opts.logf("fb-check: detail %d: %v", id, err)
			return
		}
		report.Details = append(report.Details, d)
		s.opts.logf("fb-check: detail %s (id %d): level %d %s %s, role %s, hp %d/%d, mana %d/%d, map %d at %.1f %.1f %.1f, zone %d area %d indoors=%s, spec %d, xp %d/%d, last rule %s",
			chat.SanitiseName(d.Name), d.ID, d.Level, chat.Sanitise(d.Race, 32), chat.Sanitise(d.Class, 32),
			chat.Sanitise(d.Role, 32), d.HP, d.HPMax, d.Mana, d.ManaMax, d.Map, d.X, d.Y, d.Z,
			d.ZoneID, d.AreaID, boolWord(d.Indoors), d.SpecID, d.XP, d.XPForLevel, chat.Sanitise(d.LastRule, 64))
		if len(d.Extra) > 0 {
			keys := make([]string, 0, len(d.Extra))
			for k := range d.Extra {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			shown := make([]string, 0, len(keys))
			for _, k := range keys {
				shown = append(shown, chat.Sanitise(k, 48)+"="+chat.Sanitise(d.Extra[k], 64))
			}
			s.opts.logf("fb-check: detail extras: %s", strings.Join(shown, ", "))
		}
	}
}

// fbLastRule rereads the first bot's detail after a party order: the party word is fire-and-forget, and the last
// rule the bot reports is its confirmation.
func (s *session) fbLastRule(ctx context.Context, c *fbcmd.Client, added []string) {
	var id uint32
	if len(s.report.FB.Details) > 0 {
		id = s.report.FB.Details[0].ID
	} else {
		for _, r := range s.report.FB.Roster {
			if len(added) > 0 && !strings.EqualFold(r.Name, added[0]) {
				continue
			}
			id = r.ID
			break
		}
	}
	if id == 0 {
		return
	}
	d, err := c.Detail(ctx, id)
	if err != nil {
		s.opts.logf("fb-check: party order confirmation: %v", err)
		return
	}
	s.report.FB.LastRule = chat.Sanitise(d.LastRule, 64)
	s.opts.logf("fb-check: party order confirmation: %s last rule %s", chat.SanitiseName(d.Name), s.report.FB.LastRule)
}

// summariseFB fills the report's protocol state from the run and prints the protocol's own totals.
func (s *session) summariseFB() {
	if s.pbcOff != "" {
		s.report.FB.PBC = "unavailable"
		s.report.FB.PBCReason = s.pbcOff
	} else if s.report.FB.RosterSource == "pbc" {
		s.report.FB.PBC = "ok"
	}
	s.report.FB.Frames = s.fbFrames
	if s.pbcOff != "" {
		s.opts.logf("fb-check: pbc: unavailable (%s)", s.pbcOff)
	}
}

// fbWaitMember handles packets until the party roster lists name.
func (s *session) fbWaitMember(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if s.memberListed(name) {
			s.printParty()
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("world: %s did not join the party within %s", chat.SanitiseName(name), timeout)
		}
		if err := s.collect(ctx, min(remaining, 200*time.Millisecond)); err != nil {
			return err
		}
	}
}

// fbWaitAbsent handles packets until the party no longer lists name.
func (s *session) fbWaitAbsent(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if !s.party.InParty || !s.memberListed(name) {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("world: %s is still in the party %s after the dismiss", chat.SanitiseName(name), timeout)
		}
		if err := s.collect(ctx, min(remaining, 200*time.Millisecond)); err != nil {
			return err
		}
	}
}

// parseFBAdd reads one --add value: "class" or "class:role".
func parseFBAdd(spec string) (class, role string, err error) {
	class, role, _ = strings.Cut(strings.TrimSpace(spec), ":")
	class, role = strings.ToLower(strings.TrimSpace(class)), strings.ToLower(strings.TrimSpace(role))
	if err := fbcmd.ValidateClass(class); err != nil {
		return "", "", err
	}
	if err := fbcmd.ValidateRole(role); err != nil {
		return "", "", err
	}
	return class, role, nil
}

func roleWord(role string) string {
	if role == "" {
		return "any"
	}
	return role
}

func onlineWord(online bool) string {
	if online {
		return "online"
	}
	return "offline"
}

func savedWord(saved bool) string {
	if saved {
		return ", saved"
	}
	return ""
}

func boolWord(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// FBCheckSummary is the command's closing block; the CLI prints it so every run words the result the same way.
func FBCheckSummary(a FBReport) []string {
	var out []string
	if a.PBC == "ok" {
		out = append(out, "fb-check: pbc: ok")
	} else if a.PBC != "" {
		out = append(out, fmt.Sprintf("fb-check: pbc: unavailable (%s)", a.PBCReason))
	}
	if a.RosterSource == "list" {
		out = append(out, "fb-check: roster from the chat fallback (.bot list): no ids, no live fields")
	}
	if a.About != "" {
		out = append(out, "fb-check: module: "+a.About)
	}
	refused, failed := 0, 0
	for _, o := range a.Orders {
		if o.Refused {
			refused++
		}
		if o.Err != "" {
			failed++
		}
	}
	if len(a.Orders) > 0 {
		out = append(out, fmt.Sprintf("fb-check: %d whisper order(s), %d refused, %d unanswered", len(a.Orders), refused, failed))
	}
	if len(a.Dismissed) > 0 {
		out = append(out, "fb-check: "+a.Dismissed[0])
	}
	if a.Frames > 0 {
		out = append(out, fmt.Sprintf("fb-check: %d protocol frame(s) received", a.Frames))
	}
	return out
}
