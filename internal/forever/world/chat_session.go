package world

// The chat and party side of the session: two grouped dispatch cases at the end of session.go's switch call the
// handlers here, and the commands of forever_chat.go drive them. Everything a server string reaches the log or
// stdout through is sanitised (chat.SanitiseLine), bounded, and never a GUID.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// ChatSummary is what the run saw of chat, for the command's closing lines.
type ChatSummary struct {
	Total      int
	Types      map[string]int
	Unknown    map[uint8]int
	NotFound   int
	Ambiguous  int
	Restricted int
	Notices    int
	Emotes     int
	TextEmotes int
	Lines      []chat.Line
}

// PartySnapshot is the party model at the end of the run.
type PartySnapshot struct {
	InParty     bool
	Raid        bool
	Destroyed   bool
	MemberCount int
	LeaderName  string
	LootMethod  string
	Members     []party.Member
	Events      []party.Event
	InviteFrom  string
	Counters    map[string]int
}

// maxChatListen bounds --listen: a chat-check is a short probe, not a bot session.
const maxChatListen = 2 * time.Minute

// maxPartyStay bounds --stay for party-check.
const maxPartyStay = 2 * time.Minute

// partySayText is the line party-check says in party chat. It is plain text (the server runs a leading ., ! or /
// as a command, which ValidateText refuses) and well inside the 511-byte limit.
const partySayText = "forever party-check"

// inviteWait bounds the wait for an invite to arrive and acceptWait the wait for the party update after an accept.
const (
	inviteWait  = 30 * time.Second
	acceptWait  = 20 * time.Second
	commandWait = 10 * time.Second
)

func (s *session) initChat() {
	if s.chat == nil {
		s.chat = chat.NewLog(0)
	}
	if s.party == nil {
		s.party = party.NewState()
	}
}

// chatLanguage is the language the character speaks by default: its race's base language, which is the language
// the real client puts in every chat packet (chat.LanguageForRace cites the server side).
func (s *session) chatLanguage() (uint32, error) {
	lang, err := chat.LanguageForRace(s.report.Character.Race)
	if err != nil {
		return 0, fmt.Errorf("world: the character's race: %w", err)
	}
	return lang, nil
}

// SendCommand sends text that is meant to be a server command (a leading . or !). It is the only path that allows
// those prefixes: ValidateCommandText owns the rule, ValidateText refuses them for chat. Nothing in this round
// calls it; it exists so a server command can never be confused with a chat line.
func (s *session) SendCommand(text string) error { return s.SendCommandCtx(s.ctx, text) }

// SendCommandCtx is SendCommand on the caller's context: a stop or cleanup path whose own context was detached from
// the session's hands its bounded context here, so the line still leaves after an interrupt.
func (s *session) SendCommandCtx(ctx context.Context, text string) error {
	if err := chat.ValidateCommandText(text); err != nil {
		return err
	}
	lang, err := s.chatLanguage()
	if err != nil {
		return err
	}
	return s.SendCtx(ctx, wire.CMSGChatMessageSay, chat.BuildSay(lang, text))
}

// handleChat decodes one chat or system packet. An unknown chat type is counted and skipped, never an error; a
// malformed packet is an error that names the packet.
func (s *session) handleChat(p worldconn.Packet) error {
	s.initChat()
	s.capturePacket(p)
	switch p.Core {
	case wire.SMSGChat:
		line, err := chat.Parse(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_CHAT: %w", err)
		}
		// An add-on message goes to the transport's own inbox and never to the command feed or the chat log: the
		// command channel owns ordinary chat lines, an add-on text that starts with a command word is not one, and
		// the add-on text is exact bytes rather than a display string. The chat log has no entry for it.
		if addonmsg.IsAddon(line) {
			s.offerAddon(line)
			return nil
		}
		// A running command sees the raw line through its own bound; the log keeps the display-sized copy.
		s.feedCommand(line)
		chat.SanitiseLine(&line)
		s.chat.Add(line)
		s.opts.logf("chat: %s", s.chatLineText(line))
	case wire.SMSGPrintNotification:
		text, err := chat.ParseNotification(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_PRINT_NOTIFICATION: %w", err)
		}
		s.addNotice(chat.Sanitise(text, chat.MaxDisplayText))
	case wire.SMSGMotd:
		lines, err := chat.ParseMotd(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_MOTD: %w", err)
		}
		for _, line := range lines {
			s.addNotice(chat.Sanitise(line, chat.MaxDisplayText))
		}
	case wire.SMSGChatServerMessage:
		m, err := chat.ParseServerMessage(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_CHAT_SERVER_MESSAGE: %w", err)
		}
		s.addNotice(chat.Sanitise(fmt.Sprintf("server message #%d: %s", m.ID, m.Param), chat.MaxDisplayText))
	case wire.SMSGChatPlayerNotFound:
		name, err := chat.ParsePlayerName(p.Payload, "SMSG_CHAT_PLAYER_NOTFOUND")
		if err != nil {
			return fmt.Errorf("world: SMSG_CHAT_PLAYER_NOTFOUND: %w", err)
		}
		s.report.Chat.NotFound++
		s.opts.logf("chat: player not found: %s", chat.SanitiseName(name))
	case wire.SMSGChatPlayerAmbiguous:
		name, err := chat.ParsePlayerName(p.Payload, "SMSG_CHAT_PLAYER_AMBIGUOUS")
		if err != nil {
			return fmt.Errorf("world: SMSG_CHAT_PLAYER_AMBIGUOUS: %w", err)
		}
		s.report.Chat.Ambiguous++
		s.opts.logf("chat: ambiguous player name: %s", chat.SanitiseName(name))
	case wire.SMSGChatRestricted:
		reason, err := chat.ParseRestricted(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_CHAT_RESTRICTED: %w", err)
		}
		s.report.Chat.Restricted++
		s.opts.logf("chat: restricted (reason %d)", reason)
	case wire.SMSGEmote:
		e, err := chat.ParseEmote(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_EMOTE: %w", err)
		}
		s.report.Chat.Emotes++
		s.opts.logf("chat: emote %d from %s (%d visual kit(s))", e.EmoteID, guidKind(e.GUID), len(e.SpellVisualKits))
	case wire.SMSGTextEmote:
		e, err := chat.ParseTextEmote(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_TEXT_EMOTE: %w", err)
		}
		s.report.Chat.TextEmotes++
		s.opts.logf("chat: text emote %d from %s to %s", e.EmoteID, guidKind(e.SourceGUID), guidKind(e.TargetGUID))
	}
	return nil
}

// addNotice records and prints one system line.
func (s *session) addNotice(text string) {
	s.report.Chat.Notices++
	s.feedCommand(chat.Line{Type: chat.TypeSystem, Text: text})
	s.chat.Add(chat.Line{Type: chat.TypeSystem, Text: text})
	s.opts.logf("chat: system: %s", text)
}

// chatLineText formats one chat line for stdout: the type, the sender, the target when one is named, the channel
// when one is named, and the text. The fields are already sanitised.
func (s *session) chatLineText(line chat.Line) string {
	var b strings.Builder
	name := chat.TypeName(line.Type)
	if name == "" {
		name = fmt.Sprintf("type-%d", line.Type)
	}
	b.WriteString(name)
	sender := s.chatName(line.SenderName, line.SenderGUID)
	if sender != "" {
		b.WriteString(" <")
		b.WriteString(sender)
		b.WriteByte('>')
	}
	target := s.chatName(line.TargetName, line.TargetGUID)
	if target != "" && target != sender {
		b.WriteString(" -> ")
		b.WriteString(target)
	}
	if line.Channel != "" {
		b.WriteString(" [")
		b.WriteString(line.Channel)
		b.WriteByte(']')
	}
	if line.Prefix != "" {
		b.WriteString(" (")
		b.WriteString(line.Prefix)
		b.WriteByte(')')
	}
	b.WriteString(": ")
	b.WriteString(line.Text)
	return b.String()
}

// chatName names a chat participant: the packet's own name when it has one (creatures, whispers), "you" for the
// character, the player name the world model cached, or the GUID's kind. It never prints the GUID.
func (s *session) chatName(packed string, guid wire.GUID) string {
	if packed != "" {
		return chat.SanitiseName(packed)
	}
	if guid.IsZero() {
		return ""
	}
	if s.selfSeen && guid == s.self {
		return "you"
	}
	if name := s.world.NameOf(guid); name != "" {
		return chat.SanitiseName(name)
	}
	// A party line from a member: the roster is the name source this run already has.
	if s.party != nil && s.party.RosterValid {
		if m, ok := s.party.Members[guid]; ok && m.Name != "" {
			return chat.SanitiseName(m.Name)
		}
	}
	return guidKind(guid)
}

// guidKind names a GUID without printing it: the object type only.
func guidKind(g wire.GUID) string {
	if g.IsZero() {
		return "none"
	}
	switch g.Type() {
	case 2:
		return "player"
	case 8:
		return "creature"
	case 29:
		return "account"
	}
	return fmt.Sprintf("guid type %d", g.Type())
}

// handleParty decodes one party packet and merges it into the model.
func (s *session) handleParty(p worldconn.Packet) error {
	s.initChat()
	s.capturePacket(p)
	if s.selfSeen {
		s.party.SetSelf(s.self)
	}
	switch p.Core {
	case wire.SMSGPartyInvite:
		inv, err := party.ParseInvite(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_PARTY_INVITE: %w", err)
		}
		s.party.ApplyInvite(inv)
		s.opts.logf("party: invite from %s (can accept %v, roles %d, lfg slots %d)",
			chat.SanitiseName(inv.InviterName), inv.CanAccept, inv.ProposedRoles, len(inv.LfgSlots))
	case wire.SMSGPartyUpdate:
		u, err := party.ParseUpdate(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_PARTY_UPDATE: %w", err)
		}
		if !s.party.ApplyUpdate(u) {
			switch {
			case s.party.LastInvalid != "":
				s.opts.logf("party: invalid roster: %s", s.party.LastInvalid)
			case s.party.LastUnexpected != "":
				s.opts.logf("party: unexpected roster: %s", s.party.LastUnexpected)
			}
		}
		s.printParty()
	case wire.SMSGPartyMemberFullState:
		ms, err := party.ParseMemberState(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_PARTY_MEMBER_FULL_STATE: %w", err)
		}
		if s.party.ApplyMemberState(ms) {
			for _, m := range s.party.Members {
				if m.GUID == ms.GUID {
					s.opts.logf("party: member %s %d/%d hp, %d/%d power, level %d, %s, %d aura(s)",
						chat.SanitiseName(m.Name), ms.Health, ms.MaxHealth, ms.CurrentPower, ms.MaxPower,
						ms.Level, statusText(ms.Status), ms.Auras)
					break
				}
			}
		} else {
			s.opts.logf("party: member state for a GUID the roster does not list (not merged)")
		}
	case wire.SMSGPartyMemberPartialState:
		s.party.PartialStates++
		s.opts.logf("party: SMSG_PARTY_MEMBER_PARTIAL_STATE (this core never sends it; skipped)")
	case wire.SMSGGroupDecline:
		name, err := party.ParseDeclined(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_GROUP_DECLINE: %w", err)
		}
		s.party.ApplyDeclined(chat.SanitiseName(name))
		s.opts.logf("party: %s declined the invite", chat.SanitiseName(name))
	case wire.SMSGGroupUninvite:
		reason, err := party.ParseKicked(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_GROUP_UNINVITE: %w", err)
		}
		s.party.ApplyKicked(reason)
		s.opts.logf("party: kicked from the group (reason %d)", reason)
	case wire.SMSGGroupDestroyed:
		if err := party.ParseGroupDestroyed(p.Payload); err != nil {
			return fmt.Errorf("world: %w", err)
		}
		s.party.ApplyDestroyed()
		s.opts.logf("party: group destroyed")
	case wire.SMSGGroupNewLeader:
		l, err := party.ParseLeaderChanged(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_GROUP_NEW_LEADER: %w", err)
		}
		s.party.ApplyLeaderChanged(l)
		s.opts.logf("party: leader is now %s", chat.SanitiseName(l.Name))
	case wire.SMSGPartyCommandResult:
		c, err := party.ParseCommandResult(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_PARTY_COMMAND_RESULT: %w", err)
		}
		s.party.ApplyCommandResult(c)
		s.opts.logf("party: %s %s: %s", party.OperationName(c.Operation), chat.SanitiseName(c.Name), party.ResultName(c.Result))
	case wire.SMSGReadyCheckStarted, wire.SMSGReadyCheckResponse, wire.SMSGReadyCheckCompleted:
		s.party.ReadyChecks++
		s.opts.logf("party: %s (detected, not answered in this round)", wire.ServerName(p.Core))
		s.printParty()
	}
	return nil
}

// printParty prints the model after a change.
func (s *session) printParty() {
	if s.party.Raid {
		s.opts.logf("party: raid: not handled in this round (%d members listed)", s.party.Listed)
		return
	}
	if !s.party.InParty {
		switch {
		case s.party.Destroyed:
			s.opts.logf("party: no group (destroyed)")
		case s.party.LastInvalid != "":
			s.opts.logf("party: no group (invalid roster: %s)", s.party.LastInvalid)
			s.printStaleRoster()
		case s.party.LastUnexpected != "":
			s.opts.logf("party: no group (unexpected roster: %s)", s.party.LastUnexpected)
			s.printStaleRoster()
		default:
			s.opts.logf("party: no group")
		}
		return
	}
	loot := ""
	if s.party.HasLoot {
		loot = ", loot " + party.LootName(s.party.Loot.Method)
	}
	s.opts.logf("party: %d member(s), leader %s%s", len(s.party.Members), chat.SanitiseName(s.party.LeaderName()), loot)
	for _, m := range s.party.SnapshotOf() {
		line := fmt.Sprintf("  %s connected=%v subgroup=%d class=%d roles=%d",
			chat.SanitiseName(m.Name), m.Connected, m.Subgroup, m.Class, m.RolesAssigned)
		if m.Full {
			line += fmt.Sprintf(" %d/%d hp %d/%d power level %d auras %d", m.Health, m.MaxHealth, m.CurrentPower, m.MaxPower, m.Level, m.Auras)
		}
		s.opts.logf("party:%s", line)
	}
}

// printStaleRoster reports the roster an invalid or unexpected update displaced: the diagnostic copy only, never an
// actionable one.
func (s *session) printStaleRoster() {
	if s.party.Stale == nil {
		return
	}
	s.opts.logf("party: the displaced roster had %d member(s), leader %s (diagnostic only)", len(s.party.Stale.Members), chat.SanitiseName(memberNameOf(s.party.Stale.Members, s.party.Stale.LeaderGUID)))
}

// memberNameOf names the member with the GUID in a roster copy, or a dash.
func memberNameOf(members []party.Member, guid wire.GUID) string {
	for i := range members {
		if members[i].GUID == guid {
			return members[i].Name
		}
	}
	return "-"
}

func statusText(status uint32) string {
	var parts []string
	if status&party.StatusOnline == 0 {
		parts = append(parts, "offline")
	}
	if status&party.StatusDead != 0 {
		parts = append(parts, "dead")
	}
	if status&party.StatusGhost != 0 {
		parts = append(parts, "ghost")
	}
	if status&party.StatusAFK != 0 {
		parts = append(parts, "afk")
	}
	if status&party.StatusDND != 0 {
		parts = append(parts, "dnd")
	}
	if status&party.StatusVehicle != 0 {
		parts = append(parts, "vehicle")
	}
	if len(parts) == 0 {
		return "online"
	}
	return strings.Join(parts, "+")
}

// chatCheck sends what was asked, listens for --listen, and returns. Enter logs out after it.
func (s *session) chatCheck(ctx context.Context) error {
	s.initChat()
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 4 * time.Second
	}
	if err := s.collect(ctx, settle); err != nil {
		return err
	}
	lang, err := s.chatLanguage()
	if err != nil {
		return err
	}
	race := s.report.Character.Race
	if s.opts.Say != "" {
		if err := chat.ValidateText(s.opts.Say); err != nil {
			return err
		}
		if err := s.Send(wire.CMSGChatMessageSay, chat.BuildSay(lang, s.opts.Say)); err != nil {
			return err
		}
		s.opts.logf("chat-check: said %d byte(s) (language %s, race %d)", len(s.opts.Say), chat.LanguageName(lang), race)
	}
	if s.opts.EmoteText != "" {
		if err := chat.ValidateText(s.opts.EmoteText); err != nil {
			return err
		}
		if err := s.Send(wire.CMSGChatMessageEmote, chat.BuildEmoteText(s.opts.EmoteText)); err != nil {
			return err
		}
		s.opts.logf("chat-check: emote text %d byte(s)", len(s.opts.EmoteText))
	}
	if s.opts.Whisper != "" {
		raw, text, ok := strings.Cut(s.opts.Whisper, ":")
		if !ok {
			return fmt.Errorf("world: --whisper is NAME:TEXT")
		}
		name, err := chat.NormaliseName(raw)
		if err != nil {
			return err
		}
		if err := chat.ValidateText(text); err != nil {
			return err
		}
		if err := s.Send(wire.CMSGChatMessageWhisper, chat.BuildWhisper(lang, name, text)); err != nil {
			return err
		}
		s.opts.logf("chat-check: whispered %d byte(s) to %s (language %s, race %d)",
			len(text), chat.SanitiseName(name), chat.LanguageName(lang), race)
	}
	listen := s.opts.Listen
	if listen <= 0 {
		listen = 10 * time.Second
	}
	if listen > maxChatListen {
		return fmt.Errorf("world: --listen is over %s", maxChatListen)
	}
	if s.opts.Watch != "" {
		return s.watchPlayer(ctx, listen)
	}
	s.opts.logf("chat-check: listening %s", listen)
	if err := s.drain(ctx, "the chat listen", time.Now().Add(listen), nil); err != nil {
		return fmt.Errorf("world: the chat listen ended: %w", err)
	}
	return nil
}

// watchPlayer prints the world model's position of one player once a second for d. The first sample waits up to
// two seconds for the name lookup answer and the first movement relay; a name the model never learned is a named
// error.
func (s *session) watchPlayer(ctx context.Context, d time.Duration) error {
	name, err := chat.NormaliseName(s.opts.Watch)
	if err != nil {
		return err
	}
	// Resolve the name the way the server does (case-insensitively) from the names this client has learned. The
	// watched player may log in after this one, so the sweep is retried for a while: every round asks the server for
	// the names of the nearest players the model does not know yet.
	var guid wire.GUID
	resolveDeadline := time.Now().Add(20 * time.Second)
	for {
		s.queryNames(ctx)
		if g, ok := s.world.PlayerByName(name); ok {
			guid = g
			break
		}
		if time.Now().After(resolveDeadline) {
			return fmt.Errorf("world: no player named %s among the names the model knows", chat.SanitiseName(name))
		}
		if err := s.collect(ctx, time.Second); err != nil {
			return err
		}
	}
	s.opts.logf("chat-check: watching %s for %s", chat.SanitiseName(name), d)
	deadline := time.Now().Add(d)
	for {
		relays := s.report.MoveRelays
		if o, ok := s.world.Object(guid); ok {
			s.opts.logf("watch: %s at %.2f %.2f %.2f (orientation %.2f, %d move relay(s) so far)",
				chat.SanitiseName(name), o.X, o.Y, o.Z, o.O, relays)
		} else {
			s.opts.logf("watch: %s is not in the world model (no create block or out of range), %d move relay(s) so far",
				chat.SanitiseName(name), relays)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		if remaining > time.Second {
			remaining = time.Second
		}
		if err := s.collect(ctx, remaining); err != nil {
			return err
		}
	}
}

// partyCheck drives the party command: invite (or accept/decline an invite), print the model, say a party line,
// uninvite or leave, and let Enter log out. It reports an error when the outcome is not the one asked for, and it
// acts on the roster only when the model holds a VALID one: party chat, member-stat requests and CMSG_LEAVE_GROUP
// are gated on State.RosterValid, so a hostile roster cannot make the client leave its real group.
func (s *session) partyCheck(ctx context.Context) error {
	s.initChat()
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 4 * time.Second
	}
	if err := s.collect(ctx, settle); err != nil {
		return err
	}
	if s.opts.PartyInvite != "" {
		name, err := chat.NormaliseName(s.opts.PartyInvite)
		if err != nil {
			return err
		}
		before := s.party.CommandResults
		if err := s.Send(wire.CMSGPartyInvite, party.BuildInvite(name)); err != nil {
			return err
		}
		s.opts.logf("party-check: invited %s", chat.SanitiseName(name))
		if err := s.waitPartyCommand(ctx, "the invite answer", commandWait, before, 0, name); err != nil {
			return err
		}
		if s.opts.WaitJoin > 0 || s.opts.PartyUninvite != "" {
			// The group-forming wait: --wait-join asks for it, and the uninvite flow needs it.
			if !s.party.RosterValid {
				wait := s.opts.WaitJoin
				if wait <= 0 || wait > acceptWait {
					wait = acceptWait
				}
				s.opts.logf("party-check: waiting up to %s for the group to form", wait)
				// The inviting side waits for the invitee to appear, not just for its own one-member update: the server
				// sends an update naming the inviter alone as soon as the group exists.
				if err := s.waitPartyJoined(ctx, wait, s.party.InvalidRosters+s.party.UnexpectedRosters, 2); err != nil {
					return err
				}
			}
		}
	}
	want := s.opts.PartyAcceptFrom
	if want == "" {
		want = s.opts.PartyDeclineFrom
	}
	if want != "" {
		normalized, err := chat.NormaliseName(want)
		if err != nil {
			return err
		}
		want = normalized
		if s.party.Invite == nil || !strings.EqualFold(s.party.Invite.InviterName, want) {
			s.opts.logf("party-check: waiting up to %s for an invite from %s", inviteWait, chat.SanitiseName(want))
			if err := s.waitInvite(ctx, want, inviteWait); err != nil {
				return err
			}
		}
		accept := s.opts.PartyAcceptFrom != ""
		if accept && !s.party.Invite.CanAccept {
			return fmt.Errorf("world: the invite from %s says it cannot be accepted", chat.SanitiseName(want))
		}
		badBefore := s.party.InvalidRosters + s.party.UnexpectedRosters
		if err := s.Send(wire.CMSGPartyInviteResponse, party.BuildInviteResponse(accept)); err != nil {
			return err
		}
		if accept {
			// The first roster after the accept must name the inviter as its leader (State.AcceptInvite arms that
			// check); a roster that does not is reported as unexpected and is not acted on.
			s.party.AcceptInvite()
			s.opts.logf("party-check: accepted the invite from %s", chat.SanitiseName(want))
			if err := s.waitPartyJoined(ctx, acceptWait, badBefore, 1); err != nil {
				return err
			}
		} else {
			s.party.ClearInvite()
			s.opts.logf("party-check: declined the invite from %s", chat.SanitiseName(want))
			if err := s.collect(ctx, 2*time.Second); err != nil {
				return err
			}
			return nil
		}
	}
	if s.party.RosterValid {
		guids := append([]wire.GUID(nil), s.party.Order...)
		if err := s.Send(wire.CMSGRequestPartyMemberStats, party.BuildRequestMemberStats(guids)); err != nil {
			return err
		}
		s.opts.logf("party-check: asked for %d member state(s)", len(guids))
	} else if s.party.LastInvalid != "" {
		s.opts.logf("party-check: no member stats asked: the roster is invalid (%s)", s.party.LastInvalid)
	}
	// The party line goes out before the stay, so a two-client run exchanges both lines while both are in the group
	// (the first member to leave disbands a two-member group, Group.cpp:588-591).
	lang, err := s.chatLanguage()
	if err != nil {
		return err
	}
	if s.party.RosterValid {
		if err := chat.ValidateText(partySayText); err != nil {
			return err
		}
		if err := s.Send(wire.CMSGChatMessageParty, chat.BuildParty(lang, partySayText)); err != nil {
			return err
		}
		s.opts.logf("party-check: said %d byte(s) in party chat (language %s)",
			len(partySayText), chat.LanguageName(lang))
		if err := s.collect(ctx, 2*time.Second); err != nil {
			return err
		}
	} else if s.party.LastInvalid != "" {
		s.opts.logf("party-check: no party line sent: the roster is invalid (%s)", s.party.LastInvalid)
	}
	stay := s.opts.PartyStay
	if stay <= 0 {
		stay = 5 * time.Second
	}
	if stay > maxPartyStay {
		return fmt.Errorf("world: --stay is over %s", maxPartyStay)
	}
	if err := s.collect(ctx, stay); err != nil {
		return err
	}
	if s.opts.PartyUninvite != "" {
		want, err := chat.NormaliseName(s.opts.PartyUninvite)
		if err != nil {
			return err
		}
		if !s.party.RosterValid {
			s.opts.logf("party-check: no uninvite sent: no valid roster")
			return nil
		}
		var target wire.GUID
		found := false
		for _, m := range s.party.SnapshotOf() {
			if strings.EqualFold(m.Name, want) {
				target, found = m.GUID, true
				break
			}
		}
		if !found {
			return fmt.Errorf("world: no member named %s in the roster", chat.SanitiseName(want))
		}
		before := s.party.CommandResults
		if err := s.Send(wire.CMSGPartyUninvite, party.BuildUninvite(target, "")); err != nil {
			return err
		}
		s.opts.logf("party-check: uninvited %s", chat.SanitiseName(want))
		if err := s.waitUninvite(ctx, want, commandWait, before); err != nil {
			return err
		}
		// A two-member group is disbanded by the server when either member leaves (Group.cpp:588-591, 675-679),
		// so the client follows with the roster update and SMSG_GROUP_DESTROYED: drain them.
		if err := s.collect(ctx, 2*time.Second); err != nil {
			return err
		}
		return nil
	}
	// Leave: only when the client believes it holds a VALID party roster. Without a group the server ignores the
	// packet, so sending it would claim an outcome the run did not have; a raid is reported, not handled.
	switch {
	case s.party.Raid:
		s.opts.logf("party-check: raid: not handled in this round, so no leave was sent")
	case s.party.RosterValid:
		if err := s.Send(wire.CMSGLeaveGroup, party.BuildLeaveGroup()); err != nil {
			return err
		}
		s.opts.logf("party-check: left the group")
		if err := s.waitPartyLeft(ctx, 10*time.Second); err != nil {
			return err
		}
		// The server follows the leave result with the destroyed update and SMSG_GROUP_DESTROYED; drain them so
		// the report carries the outcome the server sent.
		if err := s.collect(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	case s.party.LastInvalid != "":
		s.opts.logf("party-check: no leave sent: the roster is invalid (%s)", s.party.LastInvalid)
	default:
		s.opts.logf("party-check: not in a group, so no leave was sent")
	}
	return nil
}

// memberListed reports whether a valid roster holds that name (case-insensitively).
func (s *session) memberListed(name string) bool {
	if !s.party.RosterValid {
		return false
	}
	for _, m := range s.party.Members {
		if m.Name != "" && strings.EqualFold(m.Name, name) {
			return true
		}
	}
	return false
}

// waitUninvite waits for the outcome of CMSG_PARTY_UNINVITE. This core answers a kick with
// SMSG_PARTY_COMMAND_RESULT (PARTY_OP_UNINVITE) only while the group survives the kick: a two-member group is
// disbanded by Group::RemoveMember instead (Group.cpp:588-591 → Disband, Group.cpp:753), so an ending party is the
// same outcome and is accepted. A timeout is a named error.
func (s *session) waitUninvite(ctx context.Context, name string, timeout time.Duration, before int) error {
	deadline := time.Now().Add(timeout)
	for {
		if s.party.CommandResults > before {
			events := s.party.Events()
			for i := len(events) - 1; i >= 0; i-- {
				e := events[i]
				if e.Kind != party.EventCommandResult || e.Operation != 1 || !strings.EqualFold(e.Name, name) {
					continue
				}
				if e.Result != 0 {
					reason := party.ResultName(e.Result)
					if reason == "" {
						reason = fmt.Sprintf("result %d", e.Result)
					}
					return fmt.Errorf("world: the uninvite: %s", reason)
				}
				return nil
			}
		}
		if !s.party.RosterValid || !s.memberListed(name) {
			// The group ended (this core's two-member disband) or the member is gone from a surviving roster: the
			// outcome is the one asked for.
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("world: no uninvite answer within %s", timeout)
		}
		ev, err := s.next("the uninvite", remaining)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				return fmt.Errorf("world: no uninvite answer within %s", timeout)
			}
			return err
		}
		if err := s.handle(ctx, ev); err != nil {
			return fmt.Errorf("world: the uninvite: %w", err)
		}
	}
}

// waitPartyCommand waits for a command result for the wanted operation and checks its result.
func (s *session) waitPartyCommand(ctx context.Context, what string, timeout time.Duration, before int, operation uint8, name string) error {
	deadline := time.Now().Add(timeout)
	for {
		if s.party.CommandResults > before {
			events := s.party.Events()
			for i := len(events) - 1; i >= 0; i-- {
				e := events[i]
				if e.Kind != party.EventCommandResult || e.Operation != operation || !strings.EqualFold(e.Name, name) {
					continue
				}
				if e.Result != 0 {
					reason := party.ResultName(e.Result)
					if reason == "" {
						reason = fmt.Sprintf("result %d", e.Result)
					}
					return fmt.Errorf("world: %s: %s", what, reason)
				}
				return nil
			}
			return fmt.Errorf("world: %s: no result for that name", what)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("world: no %s within %s", what, timeout)
		}
		ev, err := s.next(what, remaining)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				return fmt.Errorf("world: no %s within %s", what, timeout)
			}
			return err
		}
		if err := s.handle(ctx, ev); err != nil {
			return fmt.Errorf("world: %s: %w", what, err)
		}
	}
}

// waitInvite handles packets until an invite from the name arrives.
func (s *session) waitInvite(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if s.party.Invite != nil && strings.EqualFold(s.party.Invite.InviterName, name) {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("world: no invite from %s within %s", chat.SanitiseName(name), timeout)
		}
		ev, err := s.next("the invite", remaining)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				return fmt.Errorf("world: no invite from %s within %s", chat.SanitiseName(name), timeout)
			}
			return err
		}
		if err := s.handle(ctx, ev); err != nil {
			return fmt.Errorf("world: waiting for the invite: %w", err)
		}
	}
}

// waitPartyJoined handles packets until the model is in a valid party. badBefore is the sum of the invalid and
// unexpected roster counts at the start: a rejected roster after the accept ends the wait with its reason.
func (s *session) waitPartyJoined(ctx context.Context, timeout time.Duration, badBefore, minMembers int) error {
	deadline := time.Now().Add(timeout)
	for {
		if s.party.InParty && s.party.RosterValid && len(s.party.Members) >= minMembers {
			s.printParty()
			return nil
		}
		if bad := s.party.InvalidRosters + s.party.UnexpectedRosters; bad > badBefore && !s.party.InParty {
			switch {
			case s.party.LastUnexpected != "":
				return fmt.Errorf("world: the roster after the accept is unexpected: %s", s.party.LastUnexpected)
			default:
				return fmt.Errorf("world: the roster after the accept is invalid: %s", s.party.LastInvalid)
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("world: no SMSG_PARTY_UPDATE within %s after the accept", timeout)
		}
		ev, err := s.next("the party update", remaining)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				return fmt.Errorf("world: no SMSG_PARTY_UPDATE within %s after the accept", timeout)
			}
			return err
		}
		if err := s.handle(ctx, ev); err != nil {
			return fmt.Errorf("world: after the accept: %w", err)
		}
	}
}

// waitPartyLeft handles packets until the model has no party.
func (s *session) waitPartyLeft(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if !s.party.InParty {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("world: still in the party %s after the leave", timeout)
		}
		ev, err := s.next("the leave", remaining)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				return fmt.Errorf("world: still in the party %s after the leave", timeout)
			}
			return err
		}
		if err := s.handle(ctx, ev); err != nil {
			return fmt.Errorf("world: after the leave: %w", err)
		}
	}
}

// summariseChat fills the report's chat summary from the log.
func (s *session) summariseChat() {
	s.initChat()
	s.report.Chat.Total = s.chat.Total()
	counts := make(map[string]int, chat.TypeCount)
	for tpe, n := range s.chat.Counts() {
		name := chat.TypeName(tpe)
		if name == "" {
			name = fmt.Sprintf("type-%d", tpe)
		}
		counts[name] += n
	}
	s.report.Chat.Types = counts
	s.report.Chat.Unknown = s.chat.Unknown()
	s.report.Chat.Lines = s.chat.Lines()
}

// summariseParty fills the report's party snapshot.
func (s *session) summariseParty() {
	s.initChat()
	snap := PartySnapshot{
		InParty:     s.party.InParty,
		Raid:        s.party.Raid,
		Destroyed:   s.party.Destroyed,
		MemberCount: len(s.party.Members),
		LeaderName:  s.party.LeaderName(),
		Members:     s.party.SnapshotOf(),
		Events:      s.party.Events(),
		Counters: map[string]int{
			"updates":            s.party.Updates,
			"states":             s.party.States,
			"orphan states":      s.party.OrphanStates,
			"partial states":     s.party.PartialStates,
			"ready checks":       s.party.ReadyChecks,
			"command results":    s.party.CommandResults,
			"declines":           s.party.Declines,
			"kicks":              s.party.Kicks,
			"destroys":           s.party.Destroys,
			"leader changes":     s.party.LeaderChanges,
			"invites":            s.party.Invites,
			"invalid rosters":    s.party.InvalidRosters,
			"unexpected rosters": s.party.UnexpectedRosters,
		},
	}
	if s.party.HasLoot {
		snap.LootMethod = party.LootName(s.party.Loot.Method)
	}
	if s.party.Invite != nil {
		snap.InviteFrom = s.party.Invite.InviterName
	}
	s.report.Party = snap
}
