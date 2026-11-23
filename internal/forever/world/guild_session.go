package world

// The guild invite flow (#113, agent side): the server's invite, player-joined event and command result, and the
// three hands — accept, decline, leave — that send the client's packets and wait for the state they change. The
// roster is not modelled: SMSG_GUILD_ROSTER's member rows depend on the realm build, and the join event plus the
// character's own UNIT_FIELD_GUILD_GUID (objects.Info.Guild, values.go:1020) prove membership.
//
// The core sends no answer to an accept, a decline or a leave; the readback is the join event or the unit field
// turning non-zero (accept), and the unit field turning zero (leave). A refused guild command arrives as
// SMSG_GUILD_COMMAND_RESULT, which the last hand's timeout names.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/guild"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// guildConfirmWait bounds the wait for an accept's join or a leave's readback. A variable so the tests shorten it;
// the spec's window is five seconds.
var guildConfirmWait = 5 * time.Second

// guildState is the session's guild invite state. Only the session goroutine touches it.
type guildState struct {
	// invite is the last SMSG_GUILD_INVITE decoded. Accept, decline and a joined event for this character clear it.
	invite *guild.Invite
	// lastResult is the last SMSG_GUILD_COMMAND_RESULT, which a hand's timeout names: a refused guild command is
	// the answer the core gives instead of the state change.
	lastResult *guild.CommandResult
	// accepted is the invite an accept confirmed: its guild name is what status.guild_name answers.
	accepted *guild.Invite
	// selfJoins counts the joined events for this character, so an accept confirms only a join that followed it.
	selfJoins int
}

// guildInit returns the state, making it on first use.
func (s *session) guildInit() *guildState {
	if s.guild == nil {
		s.guild = &guildState{}
	}
	return s.guild
}

// selfGuildGUID is this character's own UNIT_FIELD_GUILD_GUID: the model's unit field, never the held invite.
// Zero while the character is guildless or the create block has not arrived.
func (s *session) selfGuildGUID() wire.GUID {
	self := s.selfOrMover()
	if self.IsZero() || s.world == nil {
		return wire.EmptyGUID
	}
	o, ok := s.world.Object(self)
	if !ok || !o.Info.HasUnit {
		return wire.EmptyGUID
	}
	return o.Info.Guild
}

// handleGuild decodes one guild packet and merges it into the state.
func (s *session) handleGuild(p worldconn.Packet) error {
	s.capturePacket(p)
	st := s.guildInit()
	switch p.Core {
	case wire.SMSGGuildInvite:
		inv, err := guild.ParseInvite(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_GUILD_INVITE: %w", err)
		}
		st.invite = &inv
		s.event(Event{
			Kind: events.KindGuild,
			Text: "invite",
			Data: map[string]any{"guild_name": inv.GuildName, "inviter": inv.InviterName},
		})
		s.opts.logf("guild: invite to %s from %s (old guild %q)", inv.GuildName, inv.InviterName, inv.OldGuildName)
	case wire.SMSGGuildEventPlayerJoined:
		j, err := guild.ParseMemberJoined(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_GUILD_EVENT_PLAYER_JOINED: %w", err)
		}
		data := map[string]any{}
		if st.invite != nil {
			data["guild_name"] = st.invite.GuildName
		}
		s.event(Event{
			Kind:       events.KindGuild,
			Text:       "joined",
			Source:     s.eventName(j.Name, j.GUID),
			SourceGUID: eventGUID(j.GUID),
			Data:       data,
		})
		s.opts.logf("guild: %s joined the guild", s.eventName(j.Name, j.GUID))
		if s.guildJoinedSelf(j) {
			st.selfJoins++
			st.invite = nil
		}
	case wire.SMSGGuildCommandResult:
		r, err := guild.ParseCommandResult(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_GUILD_COMMAND_RESULT: %w", err)
		}
		st.lastResult = &r
		s.event(Event{
			Kind:   events.KindGuild,
			Text:   "result",
			Source: s.eventName(r.Name, wire.EmptyGUID),
			Data:   map[string]any{"result": int(r.Result), "command": int(r.Command)},
		})
		s.opts.logf("guild: command %d for %s: result %d", r.Command, s.eventName(r.Name, wire.EmptyGUID), r.Result)
	}
	return nil
}

// guildJoinedSelf reports whether a player-joined event names this character: the GUID the server sent, or the
// character's own name when the packet carried no GUID.
func (s *session) guildJoinedSelf(j guild.MemberJoined) bool {
	if !j.GUID.IsZero() {
		self := s.selfOrMover()
		return !self.IsZero() && j.GUID == self
	}
	name := s.report.Character.Name
	return name != "" && j.Name != "" && strings.EqualFold(chat.SanitiseName(name), j.Name)
}

// guildLastResultText names the last guild command result for a hand's timeout: the core answers a refused guild
// command with SMSG_GUILD_COMMAND_RESULT and no state change.
func (s *session) guildLastResultText() string {
	if s.guild == nil || s.guild.lastResult == nil {
		return "the server sent no guild command result"
	}
	r := s.guild.lastResult
	return fmt.Sprintf("the last guild command result was result %d for command %d", r.Result, r.Command)
}

// waitGuildConfirm waits for a guild hand's readback: check is polled after every packet, and a server that answers
// nothing fails with the last command result named. ctx ends the wait with the caller's reason.
func (s *session) waitGuildConfirm(ctx context.Context, what string, timeout time.Duration, check func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		if check() {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("%s: the server did not confirm it within %s (%s)", what, timeout, s.guildLastResultText())
		}
		if remaining > groupConfirmPoll {
			remaining = groupConfirmPoll
		}
		ev, err := s.next(what, remaining)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				continue
			}
			return fmt.Errorf("%s: %w", what, err)
		}
		if err := s.handle(ctx, ev); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}
}

// serveAcceptGuildInvite accepts the held invite (CMSG_ACCEPT_GUILD_INVITE with the invite's guild) and answers
// only once the join is confirmed: a joined event for this character, or the character's guild field turning
// non-zero. No held invite is "no pending guild invite"; a server that never confirms fails with the last guild
// command result named.
func (s *session) serveAcceptGuildInvite(ctx context.Context) error {
	const what = "accept_guild_invite"
	st := s.guildInit()
	inv := st.invite
	if inv == nil {
		return fmt.Errorf("%s: no pending guild invite", what)
	}
	if err := s.Send(wire.CMSGGuildAcceptInvite, guild.BuildAccept(inv.Guild)); err != nil {
		return err
	}
	// The invite is consumed by the accept: a second accept must not resend it, and a decline after it has nothing
	// to decline.
	st.invite = nil
	accepted := *inv
	was := st.selfJoins
	guildBefore := s.selfGuildGUID()
	s.opts.logf("guild: accept the invite to %s", accepted.GuildName)
	err := s.waitGuildConfirm(ctx, what, guildConfirmWait, func() bool {
		return st.selfJoins != was || (guildBefore.IsZero() && !s.selfGuildGUID().IsZero())
	})
	if err != nil {
		return err
	}
	st.accepted = &accepted
	return nil
}

// serveDeclineGuildInvite declines the held invite (CMSG_GUILD_DECLINE_INVITATION, no body) and clears it. The
// core answers nothing, so there is no readback to wait for.
func (s *session) serveDeclineGuildInvite() error {
	const what = "decline_guild_invite"
	st := s.guildInit()
	inv := st.invite
	if inv == nil {
		return fmt.Errorf("%s: no pending guild invite", what)
	}
	if err := s.Send(wire.CMSGGuildDeclineInvitation, guild.BuildDecline()); err != nil {
		return err
	}
	st.invite = nil
	s.opts.logf("guild: decline the invite to %s", inv.GuildName)
	return nil
}

// serveGuildLeave leaves the guild (CMSG_GUILD_LEAVE, no body) and waits for the character's guild field to turn
// zero. A character that is not in a guild is "not in a guild"; a server that never clears the field times out.
func (s *session) serveGuildLeave(ctx context.Context) error {
	const what = "guild_leave"
	if s.selfGuildGUID().IsZero() {
		return fmt.Errorf("%s: not in a guild", what)
	}
	if err := s.Send(wire.CMSGGuildLeave, guild.BuildLeave()); err != nil {
		return err
	}
	s.opts.logf("guild: leave")
	if err := s.waitGuildConfirm(ctx, what, guildConfirmWait, func() bool { return s.selfGuildGUID().IsZero() }); err != nil {
		return err
	}
	// The guild is left: the accepted invite's name no longer describes the character.
	s.guildInit().accepted = nil
	return nil
}
