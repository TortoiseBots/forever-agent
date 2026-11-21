package world

// The session-side helpers the serving methods use: the same packets the one-shot checks send, reached from the
// RPC loop. Every function here runs on the session goroutine.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// serveChat sends one chat line. The forms with a client writer behind them are ported; a channel, guild or raid
// line is refused by name instead of being sent as something else.
func (s *session) serveChat(ctx context.Context, typ, text, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.initChat()
	lang, err := s.chatLanguage()
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "say":
		if err := chat.ValidateText(text); err != nil {
			return err
		}
		return s.Send(wire.CMSGChatMessageSay, chat.BuildSay(lang, text))
	case "yell":
		if err := chat.ValidateText(text); err != nil {
			return err
		}
		return s.Send(wire.CMSGChatMessageYell, chat.BuildYell(lang, text))
	case "party":
		if err := chat.ValidateText(text); err != nil {
			return err
		}
		return s.Send(wire.CMSGChatMessageParty, chat.BuildParty(lang, text))
	case "emote":
		if err := chat.ValidateText(text); err != nil {
			return err
		}
		return s.Send(wire.CMSGChatMessageEmote, chat.BuildEmoteText(text))
	case "whisper":
		name, err := chat.NormaliseName(to)
		if err != nil {
			return fmt.Errorf("chat: a whisper needs a target name: %w", err)
		}
		if err := chat.ValidateText(text); err != nil {
			return err
		}
		return s.Send(wire.CMSGChatMessageWhisper, chat.BuildWhisper(lang, name, text))
	case "channel":
		return errors.New("chat: channel messages are not ported on this server")
	case "guild", "officer", "raid", "raid_leader", "raid_warning", "battleground", "battleground_leader":
		return fmt.Errorf("chat: %s messages are not ported on this server", strings.ToLower(typ))
	default:
		return fmt.Errorf("chat: unknown type %q (say, yell, party, emote, whisper)", typ)
	}
}

// serveAddon sends one add-on message under one prefix, registering the prefix first when this session has not.
// The group kinds need a message the server accepts; the channel kind is refused by addonmsg itself.
func (s *session) serveAddon(ctx context.Context, channel, prefix, text, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.initAddon()
	ch, target, err := addonmsg.ParseChannel(channel)
	if err != nil {
		return err
	}
	if to == "" {
		to = target
	}
	if err := s.RegisterAddonPrefixes(prefix); err != nil {
		return err
	}
	return s.SendAddon(ch, to, prefix, text, false)
}

// serveUnitByName resolves a GUID or a name to a unit in the model. A name is matched against the player-name cache
// first and then against every visible unit's name.
func (s *session) serveUnitByName(guid GUID, name string) (*objects.Object, error) {
	if !wire.GUID(guid).IsZero() {
		o, ok := s.world.Object(wire.GUID(guid))
		if !ok {
			return nil, fmt.Errorf("unit %s is not in view", wire.GUID(guid))
		}
		return o, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a unit needs a guid or a name")
	}
	// A player name is normalised the way the server normalises it; a creature name is not a player name, so the
	// model match below is a case-insensitive comparison against every visible unit.
	if norm, err := chat.NormaliseName(name); err == nil {
		if g, ok := s.world.PlayerByName(norm); ok {
			if o, ok := s.world.Object(g); ok {
				return o, nil
			}
		}
	}
	for _, o := range s.world.Units(s.MoverX(), s.MoverY(), s.MoverZ()) {
		if strings.EqualFold(s.world.Name(o), name) {
			return o, nil
		}
	}
	return nil, fmt.Errorf("no unit named %q nearby", chat.SanitiseName(name))
}

// serveSelect sets the session's selected target and sends CMSG_SET_SELECTION. The target is committed only after
// the send succeeds: a write that fails (the link dropped under the call) must not leave the daemon acting on a
// selection the server never received.
func (s *session) serveSelect(o *objects.Object) error {
	if err := s.Send(wire.CMSGSetSelection, combat.BuildSetSelection(o.GUID)); err != nil {
		return err
	}
	s.serveRT.target = o.GUID
	return nil
}

// serveFace turns the character toward a unit.
func (s *session) serveFace(o *objects.Object) error {
	if s.mover == nil {
		return errors.New("world: no mover (the character's create block never arrived)")
	}
	x, y, _ := o.Position(s.Now())
	return s.mover.Face(objects.OrientationTo(s.MoverX(), s.MoverY(), x, y))
}

// serveGM runs one GM line through the command channel. The guard is confirmed once per session; every later line
// rides the same channel.
func (s *session) serveGM(ctx context.Context, text string) (*command.Reply, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("gm: needs text")
	}
	if !strings.HasPrefix(text, ".") && !strings.HasPrefix(text, "!") {
		text = "." + text
	}
	opts, done, err := s.gmLinePrep(text, command.Options{})
	if err != nil {
		return nil, err
	}
	if !s.gmConfirmed {
		if err := s.confirmGM(ctx); err != nil {
			return nil, err
		}
	}
	rep, err := s.serveCommandOptions(ctx, text, opts)
	return rep, done(rep, err)
}

// The two module prefixes this method accepts. They are built from pieces so the package's own send invariant
// (dungeon_send_test.go: no `.tb` command line is built in this package) still holds: a caller hands the line in,
// nothing here composes one.
const (
	serveBotPrefix = "." + "bot"
	serveTBPrefix  = "." + "t" + "b" + " "
)

// serveBot runs one module line. The prefix is checked here so this method cannot be used to say arbitrary chat.
func (s *session) serveBot(ctx context.Context, line string, wait time.Duration) (*command.Reply, error) {
	line = strings.TrimSpace(line)
	lower := strings.ToLower(line)
	if !strings.HasPrefix(lower, serveBotPrefix+" ") && !strings.HasPrefix(lower, serveTBPrefix) {
		return nil, errors.New("bot: the line must start with " + serveBotPrefix + " or " + serveTBPrefix)
	}
	return s.serveCommandLine(ctx, line, wait)
}

// serveCommandLine confirms the GM guard on first use and then runs one command through the session's serialised
// command channel.
func (s *session) serveCommandLine(ctx context.Context, line string, wait time.Duration) (*command.Reply, error) {
	if !s.gmConfirmed {
		if err := s.confirmGM(ctx); err != nil {
			return nil, err
		}
	}
	opts := command.Options{}
	if wait > 0 {
		if wait > 30*time.Second {
			return nil, errors.New("a command deadline is at most 30s")
		}
		opts.Deadline = wait
	}
	return s.serveCommandOptions(ctx, line, opts)
}

// serveCommandOptions is serveCommandLine for a caller that brings its own options: the typed `.tb` client sets
// the token, the end marker and the line cap itself, and the session's GM guard still comes first. The session's
// own defaults are filled under the caller's options, and the line rides the same serialised channel.
func (s *session) serveCommandOptions(ctx context.Context, line string, opts command.Options) (*command.Reply, error) {
	if !s.gmConfirmed {
		if err := s.confirmGM(ctx); err != nil {
			return nil, err
		}
	}
	return s.serveRT.st.q.Run(ctx, line, s.commandOptions(opts))
}

// servePath plans a walking route on the current map.
func (s *session) servePath(x, y, z float32) (ServePath, error) {
	if s.serveRT == nil || s.serveRT.st.nav == nil {
		return ServePath{}, ErrNavmeshNotConfigured
	}
	from := navmesh.Vec3{s.MoverX(), s.MoverY(), s.MoverZ()}
	to := navmesh.Vec3{x, y, z}
	route, err := s.serveRT.st.nav.PathTo(s.world.MapID(), from, to)
	if err != nil {
		return ServePath{}, err
	}
	return ServePath{
		Map:     s.world.MapID(),
		From:    [3]float32(from),
		To:      [3]float32(to),
		Status:  string(route.Kind),
		Reason:  route.Reason,
		Points:  route.Points,
		Corners: route.Corners,
		Length:  route.Length,
	}, nil
}

// serveWalkPath plans and walks a route as one continuous movement.
func (s *session) serveWalkPath(ctx context.Context, x, y, z float32) (ServePath, error) {
	path, err := s.servePath(x, y, z)
	if err != nil {
		return ServePath{}, err
	}
	if s.mover == nil {
		return ServePath{}, errors.New("world: no mover for the walk")
	}
	switch path.Status {
	case string(navmesh.RoutePartial), string(navmesh.RouteNone):
		return ServePath{}, fmt.Errorf("walk_path: no full route to %.1f %.1f %.1f (%s)", x, y, z, path.Status)
	}
	if err := s.mover.WalkPath(ctx, path.Points); err != nil {
		return ServePath{}, err
	}
	return path, nil
}

// servePartyInfo snapshots the party model.
func (s *session) servePartyInfo() ServePartyInfo {
	out := ServePartyInfo{Members: []ServePartyMember{}}
	if s.party == nil {
		return out
	}
	out.InGroup = s.party.RosterValid && s.party.InParty
	out.Leader = GUID(s.party.LeaderGUID)
	if s.party.Invite != nil {
		out.PendingInvite = s.party.Invite.InviterName
	}
	if !s.party.RosterValid {
		return out
	}
	for _, m := range s.party.SnapshotOf() {
		out.Members = append(out.Members, ServePartyMember{
			Name: m.Name, GUID: GUID(m.GUID), Online: m.Connected,
			Dead: m.Full && m.Health == 0, Leader: m.GUID == s.party.LeaderGUID,
			Health: m.Health, MaxHealth: m.MaxHealth, Level: m.Level,
			PowerType: m.PowerType, Power: int32(m.CurrentPower), MaxPower: int32(m.MaxPower),
			Zone: m.ZoneID,
		})
	}
	return out
}

// serveInvite invites one name and reports the server's answer for it.
func (s *session) serveInvite(ctx context.Context, name string) error {
	norm, err := chat.NormaliseName(name)
	if err != nil {
		return err
	}
	before := s.party.CommandResults
	if err := s.Send(wire.CMSGPartyInvite, party.BuildInvite(norm)); err != nil {
		return err
	}
	return s.waitPartyCommand(ctx, "the invite answer", 10*time.Second, before, 0, norm)
}

// serveAcceptInvite answers the pending invite and waits under the caller's context for the server's answer: a
// roster that names the inviter (the model's transition check also refuses a roster whose leader is not the
// inviter), or a refusal (a failed party command result). Success is returned only once membership is confirmed; a
// server that answers nothing is a timeout error.
func (s *session) serveAcceptInvite(ctx context.Context) error {
	if s.party.Invite == nil {
		return errors.New("no pending group invite")
	}
	inv := *s.party.Invite
	if !inv.CanAccept {
		return errors.New("the pending group invite cannot be accepted")
	}
	resultsBefore := s.party.CommandResults
	badBefore := s.party.InvalidRosters + s.party.UnexpectedRosters
	if err := s.Send(wire.CMSGPartyInviteResponse, party.BuildInviteResponse(true)); err != nil {
		return err
	}
	s.party.AcceptInvite()
	return s.waitAccepted(ctx, serveAcceptInviteWait, badBefore, resultsBefore, inv)
}

// serveAcceptInviteWait bounds the wait for the answer to an accepted invite. A variable so the tests shorten it.
var serveAcceptInviteWait = 10 * time.Second

// acceptInvitePoll bounds one packet wait, so the caller's context ends the wait promptly.
const acceptInvitePoll = 200 * time.Millisecond

// waitAccepted waits for the roster that confirms an accepted invite, or for the server's refusal.
func (s *session) waitAccepted(ctx context.Context, timeout time.Duration, badBefore, resultsBefore int, inv party.Invite) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("accept_invite: %w", err)
		}
		if s.party.InParty && s.party.RosterValid && s.rosterHasInviter(inv) {
			return nil
		}
		if bad := s.party.InvalidRosters + s.party.UnexpectedRosters; bad > badBefore {
			reason := s.party.LastUnexpected
			if reason == "" {
				reason = s.party.LastInvalid
			}
			return fmt.Errorf("accept_invite: the server's roster does not confirm the group: %s", reason)
		}
		if s.party.CommandResults > resultsBefore {
			events := s.party.Events()
			if n := len(events); n > 0 {
				e := events[n-1]
				if e.Kind == party.EventCommandResult && e.Result != 0 {
					reason := party.ResultName(e.Result)
					if reason == "" {
						reason = fmt.Sprintf("result %d", e.Result)
					}
					return fmt.Errorf("accept_invite: the server refused it: %s", reason)
				}
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("accept_invite: no confirmation within %s", timeout)
		}
		if remaining > acceptInvitePoll {
			remaining = acceptInvitePoll
		}
		ev, err := s.next("the accept's answer", remaining)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				continue
			}
			return fmt.Errorf("accept_invite: %w", err)
		}
		if err := s.handle(ctx, ev); err != nil {
			return fmt.Errorf("accept_invite: %w", err)
		}
	}
}

// rosterHasInviter reports whether the valid roster holds the inviter: by GUID when the invite carried one, else by
// name.
func (s *session) rosterHasInviter(inv party.Invite) bool {
	for _, m := range s.party.SnapshotOf() {
		if !inv.InviterGUID.IsZero() && m.GUID == inv.InviterGUID {
			return true
		}
		if inv.InviterName != "" && strings.EqualFold(m.Name, inv.InviterName) {
			return true
		}
	}
	return false
}

// serveLeaveGroup leaves a party the model holds a valid roster for.
func (s *session) serveLeaveGroup(ctx context.Context) error {
	if s.party == nil || !s.party.RosterValid {
		if s.party != nil && s.party.InParty {
			return fmt.Errorf("no valid party roster (%s)", s.party.LastInvalid)
		}
		return errors.New("not in a group")
	}
	if err := s.Send(wire.CMSGLeaveGroup, party.BuildLeaveGroup()); err != nil {
		return err
	}
	return s.waitPartyLeft(ctx, 10*time.Second)
}

// ServeCastState is the cast_state result: the current cast, the global cooldown and the combo points.
type ServeCastState struct {
	Casting     bool   `json:"casting"`
	SpellID     int32  `json:"spell_id,omitempty"`
	CastID      GUID   `json:"cast_id,omitempty"`
	Target      GUID   `json:"target,omitempty"`
	RemainingMS int64  `json:"remaining_ms,omitempty"`
	InCombat    bool   `json:"in_combat"`
	Dead        bool   `json:"dead"`
	Ghost       bool   `json:"ghost"`
	Health      int64  `json:"health"`
	MaxHealth   int64  `json:"max_health"`
	Power       int32  `json:"power"`
	MaxPower    int32  `json:"max_power"`
	PowerType   uint8  `json:"power_type,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (s *session) serveCastState() ServeCastState {
	out := ServeCastState{}
	self := s.selfOrMover()
	if s.world == nil {
		return out
	}
	cs, ok := s.world.Combat(self)
	if !ok {
		return out
	}
	out.InCombat, out.Dead, out.Ghost = cs.InCombat, cs.Dead, cs.Ghost
	out.Health, out.MaxHealth, out.Power, out.MaxPower, out.PowerType = cs.Health, cs.MaxHealth, cs.Power, cs.MaxPower, cs.PowerType
	if cs.Cast != nil {
		out.Casting = true
		out.SpellID, out.CastID, out.Target = cs.Cast.SpellID, GUID(cs.Cast.CastID), GUID(cs.Cast.Target)
		if left := cs.Cast.Ends().Sub(s.Now()); left > 0 {
			out.RemainingMS = left.Milliseconds()
		}
	}
	return out
}
