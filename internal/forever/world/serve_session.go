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

// serveChat sends one chat line: say, yell, party, emote, whisper (with to), guild, officer, raid, raid_warning,
// battleground (the instance chat line) and channel (with to, the channel name). A leader's line is refused by name:
// the server marks it itself.
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
		return s.serveChannelSay(lang, to, text)
	default:
		kind := strings.ToLower(strings.TrimSpace(typ))
		if handled, err := s.serveGroupChat(kind, lang, text); handled {
			return err
		}
		return fmt.Errorf("chat: unknown type %q (say, yell, party, emote, whisper, guild, officer, raid, raid_warning, battleground, channel)", typ)
	}
}

// serveAddon sends one add-on message under one prefix, registering the prefix first when this session has not.
// The group kinds need a message the server accepts; channel:NAME goes to a channel this character joined.
func (s *session) serveAddon(ctx context.Context, channel, prefix, text, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.initAddon()
	if channel == "channel" {
		return errChannelSpec
	}
	if name, ok := strings.CutPrefix(channel, "channel:"); ok {
		if to != "" {
			name = to
		}
		return s.serveAddonChannel(ctx, name, prefix, text)
	}
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
	// A player met after the login has no name yet: ask for it, so the next call finds the unit.
	s.serveAskNames()
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

// serveCommandLine runs one command through the session's serialised command channel. A `.bot` line is an owner
// command: a normal player (a Battle.net owner) runs it, so it is sent without the GM probe. Every other line
// (`.tb`, the GM lines) confirms the GM guard on first use.
func (s *session) serveCommandLine(ctx context.Context, line string, wait time.Duration) (*command.Reply, error) {
	if !s.gmConfirmed && !isOwnerLine(line) {
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
	return s.serveRunLine(ctx, line, opts)
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
	return s.serveRunLine(ctx, line, opts)
}

// errPreflightBusy is what a session-side run answers while the dungeon_run preflight owns the command channel: these
// methods execute on the session goroutine, and the preflight's own queued calls (its send, its 50 ms pump slices) need
// that goroutine while its tokenless read holds the channel. Waiting here would stop every method (status included)
// and the packet loop for the read's whole window — forever when the read hangs — so the run is refused at once and
// nothing is sent.
var errPreflightBusy = errors.New("busy: a run preflight holds the command channel, retry")

// serveRunLine puts one line on the serialised command channel, whose link enforces the guard for every line but
// a `.bot` owner command. The channel is taken without waiting: only the preflight holds it off this goroutine, and a
// wait would deadlock the preflight's next slice against this call (errPreflightBusy).
func (s *session) serveRunLine(ctx context.Context, line string, opts command.Options) (*command.Reply, error) {
	rep, err := s.serveRT.st.q.TryRun(ctx, line, s.commandOptions(opts))
	if errors.Is(err, command.ErrBusy) {
		return nil, errPreflightBusy
	}
	return rep, err
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

// ServeWalkRay is the walk_ray method's result. It is the navmesh walkability ray, NOT true line of sight: the agent has
// no collision geometry (the server's vmaps), so In reads "blocked" for a cliff, a water edge or a target off the
// mesh, and "clear" through a ceiling or a bridge.
type ServeWalkRay struct {
	Map  uint32     `json:"map"`
	From [3]float32 `json:"from"`
	To   [3]float32 `json:"to"`
	// Known is false when the mesh cannot say (an end off the mesh, a tile file missing): In is then false and
	// means nothing.
	Known bool `json:"known"`
	// In says the straight line from From to To stays on ground the navmesh holds: nothing the mesh marks as a wall,
	// a pillar or a drop lies between the two points.
	In bool `json:"in"`
	// Hit is how far from From the line meets the mesh's edge (0 when In).
	Hit float32 `json:"hit,omitempty"`
	// Source names what answered: the agent holds no collision geometry (the server's vmaps), so the answer is the
	// navmesh walkability ray (navmesh.Planner.Raycast), which is the same on every machine and needs only --navmesh.
	Source string `json:"source"`
}

// serveWalkRay answers the walk_ray method: whether the straight line from the session's own position (or an explicit
// from point) to a point stays on ground the navmesh holds. Not true line of sight (see ServeWalkRay); a target with no
// mesh ground near it is Known false.
func (s *session) serveWalkRay(p serveWalkRayParams) (ServeWalkRay, error) {
	if s.serveRT == nil || s.serveRT.st.nav == nil {
		return ServeWalkRay{}, ErrNavmeshNotConfigured
	}
	from := navmesh.Vec3{s.MoverX(), s.MoverY(), s.MoverZ()}
	if p.From != nil {
		from = navmesh.Vec3(*p.From)
	}
	to := navmesh.Vec3{p.X, p.Y, p.Z}
	// A target the mesh holds no ground near is unknown, not out of sight: the scout leaves it unanswered.
	_, onMesh, err := s.serveRT.st.nav.Nearest(s.world.MapID(), to)
	if err != nil {
		return ServeWalkRay{}, err
	}
	ray, err := s.serveRT.st.nav.Raycast(s.world.MapID(), from, to)
	if err != nil {
		return ServeWalkRay{}, err
	}
	ray.Known = ray.Known && onMesh
	return ServeWalkRay{
		Map: s.world.MapID(), From: [3]float32(from), To: [3]float32(to),
		Known: ray.Known, In: ray.Known && ray.Clear, Hit: ray.Hit, Source: "navmesh_raycast",
	}, nil
}

// ServeWalkTo is walk_to's answer: empty on arrival, or the transfer the walk ended in.
type ServeWalkTo struct {
	// ChangedMap is the map the walk ended on and the position the server put the character at, when a map change
	// ended the leg (an instance portal the walk's own area trigger opened). That is not an error: the walk was
	// overtaken by the map change it asked for.
	ChangedMap *ServeWalkMap `json:"changed_map,omitempty"`
}

// ServeWalkMap is a map and a position on it, after the server moved the character.
type ServeWalkMap struct {
	Map uint32  `json:"map"`
	X   float32 `json:"x"`
	Y   float32 `json:"y"`
	Z   float32 `json:"z"`
}

// walkMapPoll is how long one wait of a walk's map-change watch lasts: the session answers the server's packets in it
// and the between-maps window is re-read after it.
const walkMapPoll = 100 * time.Millisecond

// serveWalkTo walks one straight line (walk_to). A map change that ends the leg is answered with the map and position
// the character landed on; only a walk still between maps when its deadline passes is an error (objects.ErrMapChange).
// The plan trigger volumes of the route are armed for the walk (armWalkTriggers).
func (s *session) serveWalkTo(ctx context.Context, x, y, z float32) (ServeWalkTo, error) {
	if s.mover == nil {
		return ServeWalkTo{}, errors.New("world: no mover (the character's create block never arrived)")
	}
	// A walk asked right after a teleport that left the character in the air waits for the landing first: teleport
	// answers at the fall's start (so status can see it), and a real client cannot walk in the air.
	if err := s.waitFallLanded(ctx); err != nil {
		return ServeWalkTo{}, err
	}
	disarm := s.armWalkTriggers([][3]float32{{x, y, z}})
	defer disarm()
	between := s.betweenMaps()
	epoch := s.mapEpoch()
	err := s.mover.MoveTo(ctx, x, y, z)
	if err == nil {
		return ServeWalkTo{}, nil
	}
	if !errors.Is(err, objects.ErrMapChange) {
		return ServeWalkTo{}, err
	}
	if between && s.mapEpoch() == epoch {
		// The window was open before the call: the walk never started, and no map change belongs to it.
		return ServeWalkTo{}, fmt.Errorf("%w: the walk did not start", objects.ErrMapChange)
	}
	landed, err := s.walkEndedInATransfer(ctx)
	if err != nil {
		return ServeWalkTo{}, err
	}
	return ServeWalkTo{ChangedMap: landed}, nil
}

// walkEndedInATransfer answers a walk whose leg a transfer ended: it waits, on the caller's context and bounded by the
// session's own map-change limit, for the between-maps window to close, then reports the map and position the server
// put the character at. A window still open when the caller's deadline passes is the error (objects.ErrMapChange): the
// walk ended in a transfer that never landed, and nothing about the new map may be guessed.
func (s *session) walkEndedInATransfer(ctx context.Context) (*ServeWalkMap, error) {
	for s.betweenMaps() {
		ev, err := s.nextCtx(ctx, "the map change", walkMapPoll)
		switch {
		case err == nil:
			if herr := s.handle(ctx, ev); herr != nil {
				return nil, herr
			}
		case errors.Is(err, errWaitTimeout):
		case errors.Is(err, ErrMapChangeTimeout):
			return nil, fmt.Errorf("%w: the walk ended in a transfer that did not land within %s", objects.ErrMapChange, mapChangeLimit)
		case ctx.Err() != nil:
			return nil, fmt.Errorf("%w: the character is still between maps at the walk's deadline", objects.ErrMapChange)
		default:
			return nil, err
		}
	}
	x, y, z, _ := s.mover.Position()
	return &ServeWalkMap{Map: s.world.MapID(), X: x, Y: y, Z: z}, nil
}

// serveWalkPath plans and walks a route as one continuous movement.
func (s *session) serveWalkPath(ctx context.Context, x, y, z float32) (ServePath, error) {
	if s.mover == nil {
		return ServePath{}, errors.New("world: no mover for the walk")
	}
	// A path walked right after a teleport that left the character in the air waits for the landing first (teleport
	// answers at the fall's start), as an ordinary walk does. The wait comes before the plan: the route's start must
	// be the ground the character lands on, not the spot in the air that Detour cannot place.
	if err := s.waitFallLanded(ctx); err != nil {
		return ServePath{}, err
	}
	path, err := s.servePath(x, y, z)
	if err != nil {
		return ServePath{}, err
	}
	switch path.Status {
	case string(navmesh.RoutePartial), string(navmesh.RouteNone):
		return ServePath{}, fmt.Errorf("walk_path: no full route to %.1f %.1f %.1f (%s)", x, y, z, path.Status)
	}
	disarm := s.armWalkTriggers(path.Points)
	defer disarm()
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
	out.Raid = s.party.Raid
	out.Leader = GUID(s.party.LeaderGUID)
	if s.party.Invite != nil {
		out.PendingInvite = s.party.Invite.InviterName
	}
	if !s.party.RosterValid {
		return out
	}
	for _, m := range s.party.SnapshotOf() {
		row := ServePartyMember{
			Name: m.Name, GUID: GUID(m.GUID), Online: m.Connected,
			Dead: m.Full && m.Health == 0, Leader: m.GUID == s.party.LeaderGUID,
			Health: m.Health, MaxHealth: m.MaxHealth, Level: m.Level,
			PowerType: m.PowerType, Power: int32(m.CurrentPower), MaxPower: int32(m.MaxPower),
			Zone: m.ZoneID,
		}
		if s.party.Raid {
			// The wire subgroup is 0-based; the raid frame numbers the subgroups 1-8. The assistant bit is a raid
			// rank (Group.h:59-72), so a party reports neither.
			row.Subgroup = int(m.Subgroup) + 1
			row.Assistant = m.Assistant
		}
		if m.GUID == s.party.SelfGUID {
			out.Subgroup = row.Subgroup
			out.Assistant = row.Assistant
		}
		out.Members = append(out.Members, row)
	}
	if s.party.HasLoot {
		out.LootMethod = party.LootName(s.party.Loot.Method)
		out.LootThreshold = int(s.party.Loot.Threshold)
		out.LootMaster = GUID(s.party.Loot.Master)
	}
	out.Marks = serveMarks(s.party.Marks)
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
// server that answers nothing is a timeout error. The result names the inviter the held invite carried.
func (s *session) serveAcceptInvite(ctx context.Context) (ServeAcceptInviteResult, error) {
	if s.party.Invite == nil {
		return ServeAcceptInviteResult{}, errors.New("no pending group invite")
	}
	inv := *s.party.Invite
	if !inv.CanAccept {
		return ServeAcceptInviteResult{}, errors.New("the pending group invite cannot be accepted")
	}
	resultsBefore := s.party.CommandResults
	badBefore := s.party.InvalidRosters + s.party.UnexpectedRosters
	if err := s.Send(wire.CMSGPartyInviteResponse, party.BuildInviteResponse(true)); err != nil {
		return ServeAcceptInviteResult{}, err
	}
	s.party.AcceptInvite()
	if err := s.waitAccepted(ctx, serveAcceptInviteWait, badBefore, resultsBefore, inv); err != nil {
		return ServeAcceptInviteResult{}, err
	}
	return ServeAcceptInviteResult{Inviter: chat.SanitiseName(inv.InviterName), InviterGUID: GUID(inv.InviterGUID)}, nil
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

// ServeGCD is the global cooldown block of cast_state.
type ServeGCD struct {
	Active      bool  `json:"active"`
	RemainingMS int64 `json:"remaining_ms"`
	TotalMS     int64 `json:"total_ms"`
}

// ServeLastCast is the most recent completed own cast.
type ServeLastCast struct {
	SpellID int32  `json:"spell_id"`
	Name    string `json:"name,omitempty"`
	AgoMS   int64  `json:"ago_ms"`
}

// ServeLastFailure is the most recent failed own cast.
type ServeLastFailure struct {
	SpellID    int32  `json:"spell_id"`
	Name       string `json:"name,omitempty"`
	ReasonName string `json:"reason_name"`
	AgoMS      int64  `json:"ago_ms"`
}

// ServeCooldown is one active spell cooldown reported by cast_state.
type ServeCooldown struct {
	SpellID     int32  `json:"spell_id"`
	Name        string `json:"name,omitempty"`
	RemainingMS int64  `json:"remaining_ms"`
	Category    uint16 `json:"category,omitempty"`
}

// ServeCastState is the cast_state result: the current cast, the global cooldown, history, combo points and active cooldowns.
type ServeCastState struct {
	Casting     bool              `json:"casting"`
	SpellID     int32             `json:"spell_id,omitempty"`
	CastID      GUID              `json:"cast_id,omitempty"`
	Target      GUID              `json:"target,omitempty"`
	RemainingMS int64             `json:"remaining_ms,omitempty"`
	InCombat    bool              `json:"in_combat"`
	Dead        bool              `json:"dead"`
	Ghost       bool              `json:"ghost"`
	Health      int64             `json:"health"`
	MaxHealth   int64             `json:"max_health"`
	Power       int32             `json:"power"`
	MaxPower    int32             `json:"max_power"`
	PowerType   uint8             `json:"power_type,omitempty"`
	Error       string            `json:"error,omitempty"`
	GCD         ServeGCD          `json:"gcd"`
	GCDKnown    bool              `json:"gcd_known"`
	LastCast    *ServeLastCast    `json:"last_cast,omitempty"`
	LastFailure *ServeLastFailure `json:"last_failure,omitempty"`
	Failures    uint64            `json:"failures"`
	ComboPoints uint8             `json:"combo_points"`
	ComboTarget GUID              `json:"combo_target,omitzero"`
	Cooldowns   []ServeCooldown   `json:"cooldowns"`
}

func (s *session) serveCastState() ServeCastState {
	now := s.Now()
	total, left, known := s.gcdState(now)
	pts, ctarget := s.comboPoints()
	out := ServeCastState{
		GCD: ServeGCD{
			Active:      left > 0,
			RemainingMS: left.Milliseconds(),
			TotalMS:     total.Milliseconds(),
		},
		GCDKnown:    known,
		ComboPoints: pts,
		Cooldowns:   s.activeCooldowns(now),
	}
	if pts > 0 {
		out.ComboTarget = GUID(ctarget)
	}
	if s.spellSt != nil {
		out.Failures = s.spellSt.failures
		if lc := s.spellSt.lastCast; lc != nil {
			out.LastCast = &ServeLastCast{
				SpellID: lc.spell,
				Name:    s.spellName(lc.spell),
				AgoMS:   now.Sub(lc.at).Milliseconds(),
			}
		}
		if lf := s.spellSt.lastFailure; lf != nil {
			out.LastFailure = &ServeLastFailure{
				SpellID:    lf.spell,
				Name:       s.spellName(lf.spell),
				ReasonName: combat.SpellFailureName(lf.reason),
				AgoMS:      now.Sub(lf.at).Milliseconds(),
			}
		}
	}
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
		if rem := cs.Cast.Ends().Sub(now); rem > 0 {
			out.RemainingMS = rem.Milliseconds()
		}
	}
	return out
}
