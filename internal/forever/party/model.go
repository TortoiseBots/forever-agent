package party

import (
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// MaxEvents bounds the group-event ring the model keeps for the command output.
const MaxEvents = 128

// MaxReadyChecks bounds the ready checks the model keeps: the parties this character is in (home and instance) plus
// slack for a group switch. A hostile server cannot grow the model with invented party GUIDs.
const MaxReadyChecks = 4

// EventKind names one group event.
type EventKind int

const (
	EventInvite EventKind = iota + 1
	EventInviteResult
	EventInviteDeclined
	EventJoined
	EventUpdate
	EventMemberState
	EventKicked
	EventLeft
	EventDestroyed
	EventLeaderChanged
	EventCommandResult
	EventLostInvite
	EventInvalidRoster
	EventUnexpectedRoster
	EventReadyCheckStarted
	EventReadyCheckResponse
	EventReadyCheckCompleted
)

// ReadyCheck is one ready check of one party: the core starts one per group state and broadcasts the answers until
// every member has one (Group.cpp:1472-1560). Answers keeps what each member said, Answered their order, so a
// reader can name who answered and who never did. Checks are keyed by PartyGUID (see State.ReadyCheckOf), so a home
// and an instance check never replace each other, and a reader that confirmed a start waits on that check object,
// never on whatever check happens to be the party's current one later.
type ReadyCheck struct {
	Active    bool
	Complete  bool
	Index     int8
	PartyGUID wire.GUID
	Initiator wire.GUID
	Duration  time.Duration
	Answers   map[wire.GUID]bool
	Answered  []wire.GUID
}

// Counts returns how many of the recorded answers are ready and how many are not.
func (r *ReadyCheck) Counts() (ready, notReady int) {
	if r == nil {
		return 0, 0
	}
	for _, isReady := range r.Answers {
		if isReady {
			ready++
		}
	}
	return ready, len(r.Answers) - ready
}

// Event is one change the model made from a packet.
type Event struct {
	Kind      EventKind
	Text      string
	Operation uint8 // the PartyOperation of a command result
	Result    uint8 // the PartyResult of a command result
	Name      string
}

// State is the party the client believes it is in. It is bounded: at most MaxRaidMembers members, MaxEvents
// events and MaxLog-sized strings (the caller sanitises before printing).
//
// Trust: an update replaces the roster only after validateRoster accepts it and, when a transition is pending, the
// transition's consistency check passes; a rejected or unexpected one is counted, ends the party (RosterValid false,
// nothing actionable) and leaves the members it displaced only in Stale, a diagnostic copy. A member state merges
// only into a member the last accepted roster listed, so a hostile server cannot grow the model with states for
// unknown GUIDs. A state with Status == StatusOffline marks the member disconnected and merges nothing else (the
// server sends zeros there, GroupHandler.cpp:617-634).
type State struct {
	InParty   bool
	Raid      bool // the roster carries the group's raid flag (FlagRaid), whatever its size
	Destroyed bool
	// SelfGUID is the character's own GUID, set from the world's ThisIsYou create block. A roster without it is
	// not the client's party, so a hostile roster cannot make the client act as if it were.
	SelfGUID wire.GUID
	// RosterValid is true only for the last roster that passed validateRoster and the pending transition's check
	// (this character in it exactly once at MyIndex, a normal party type, a party GUID, no empty or duplicate GUIDs,
	// the leader listed, at most MaxRaidMembers). Party chat, member-stat requests, the uninvite and CMSG_LEAVE_GROUP
	// are gated on it: an invalid or unexpected snapshot clears it, so a stale roster is never acted on.
	RosterValid        bool
	LastInvalid        string // why the most recent roster was rejected, "" when it was valid
	LastUnexpected     string // why the most recent roster did not match the transition that produced it, "" when none
	InvalidRosters     int    // rejected rosters, counted
	UnexpectedRosters  int    // rosters that failed the transition's check, counted
	Flags              uint16
	Index              uint8
	Type               uint8
	MyIndex            int32
	PartyGUID          wire.GUID
	LeaderGUID         wire.GUID
	Sequence           uint32
	LeaderFactionGroup uint8
	HasLoot            bool
	Loot               LootSettings
	HasDifficulty      bool
	Difficulty         Difficulty
	Listed             int // members the last update listed
	// Stale is the last roster a rejected or unexpected update displaced: a diagnostic copy only. RosterValid is
	// false and Members is empty while it is set, so nothing acts on it; it exists to say what was dropped.
	Stale *Roster

	Members map[wire.GUID]*Member
	Order   []wire.GUID

	Invite *Invite
	// ReadyCheck is the most recently started ready check, nil before the first one. The check of a given party is
	// ReadyCheckOf: a start for another party (the home and the instance group are both broadcast to a member)
	// replaces the field but not that party's tracked check.
	ReadyCheck *ReadyCheck
	// checks holds one check per party, oldest start first, at most MaxReadyChecks of them.
	checks []*ReadyCheck
	// readyReq is the check this client asked the core for and whose start has not arrived yet. The core may
	// broadcast the offline members' answers and finish a one-member check before it broadcasts the started packet
	// (Group.cpp:1484-1493); those packets belong to this request's check only. Without a request they are events,
	// never answers or a finish of a later check: a character that joined or reconnected during a check sees that
	// check's completion without its start.
	readyReq *readyRequest

	// pending is the first roster a transition must produce: the accepted invite's leader, or the name of an
	// outgoing invite the server confirmed. The first roster after the transition is checked against it, and a
	// roster that fails is "unexpected": not actionable, reported. This is a consistency check, not a defence
	// against a lying server (a server that lies can send a roster that matches).
	pending *pendingRoster

	// held is the roster that arrived before this character's own GUID was known: a character that logs in already
	// grouped gets SMSG_PARTY_UPDATE before its own create block (or the active-mover notice that names its GUID).
	// One roster is held (a newer one replaces it) and applied when SetSelf learns the GUID; it is not invalid.
	held *Update

	events []Event
	next   int

	Updates       int
	States        int
	OrphanStates  int
	PartialStates int
	// ReadyChecks counts the starts; ReadyCheckAnswers and ReadyCheckCompletes count the packets that follow.
	ReadyChecks         int
	ReadyCheckAnswers   int
	ReadyCheckCompletes int
	CommandResults      int
	Declines            int
	Kicks               int
	Destroys            int
	HeldRosters         int // rosters that arrived before this character's own GUID was known (applied by SetSelf)
	LeaderChanges       int
	Invites             int

	// Marks is the group's raid target icons, indexed by Mark: what the client shows and the mark commands read
	// back. The core sends one single update per change and the whole list on request (Group.cpp:820-852).
	Marks [8]wire.GUID
	// MarksKnown says a mark packet has been applied, so a reader can tell "no icons set" from "nothing heard yet".
	MarksKnown  bool
	MarkUpdates int // SMSG_SEND_RAID_TARGET_UPDATE_SINGLE packets applied (or refused for a bad symbol)
	MarkLists   int // SMSG_SEND_RAID_TARGET_UPDATE_ALL packets applied
}

// Roster is a diagnostic copy of a roster that was displaced. It is never actionable.
type Roster struct {
	LeaderGUID wire.GUID
	Members    []Member
}

// pendingRoster is what the first roster after a transition must contain: the inviter's GUID as the leader (an
// accepted invite), or the invited name somewhere in the roster (an outgoing invite the server confirmed).
type pendingRoster struct {
	leader wire.GUID
	name   string
	why    string
}

// readyRequest is the ready check this client asked the core for: its party and party index, and the answers and
// finish the core sent before that check's start. Group::StartReadyCheck sends the offline members' answers and may
// end a one-member check before it broadcasts SMSG_READY_CHECK_STARTED (Group.cpp:1484-1493), so those packets can
// precede their start; they belong to this request's check and to no later one. The answers are bounded like a
// check's own.
type readyRequest struct {
	party    wire.GUID
	index    int8
	answers  []ReadyCheckResponse
	complete *ReadyCheckCompleted
}

// NewState returns an empty party.
func NewState() *State {
	return &State{Members: make(map[wire.GUID]*Member, MaxPartyMembers), events: make([]Event, 0, MaxEvents)}
}

func (s *State) addEvent(e Event) {
	if len(s.events) < MaxEvents {
		s.events = append(s.events, e)
		return
	}
	s.events[s.next] = e
	s.next = (s.next + 1) % MaxEvents
}

// Events returns the retained events, oldest first.
func (s *State) Events() []Event {
	if len(s.events) < MaxEvents {
		return append([]Event(nil), s.events...)
	}
	out := make([]Event, 0, MaxEvents)
	out = append(out, s.events[s.next:]...)
	out = append(out, s.events[:s.next]...)
	return out
}

// SetSelf records the character's own GUID, from the ThisIsYou create block (or the active-mover notice). A roster
// is only valid once this is known, so a roster that arrived earlier was held (applyUpdate) and is applied here.
func (s *State) SetSelf(guid wire.GUID) {
	if guid.IsZero() {
		return
	}
	s.SelfGUID = guid
	held := s.held
	if held == nil {
		return
	}
	s.held = nil
	s.apply(*held)
}

// DropHeld discards a roster held before this character's own GUID was known and reports whether one was held. A map
// change calls it: a roster read before SMSG_NEW_WORLD belongs to the map the character left, and the server sends
// the party update again for the new map (docs/forever/MAPCHANGE-R1-NOTES.md), so applying the old snapshot would
// make the new map's model act on a stale group.
func (s *State) DropHeld() bool {
	if s.held == nil {
		return false
	}
	s.held = nil
	return true
}

// validateRoster checks a roster before it can become actionable. It returns "" for a valid roster, else the reason.
// Every field it checks is one Group::SendUpdateToPlayer writes from the group's own state: PartyType is
// GROUP_TYPE_NORMAL for a created group and GROUP_TYPE_NONE only in SendUpdateDestroyGroupToPlayer alongside the
// destroyed flag (Group.cpp:855-857, :927-940); PartyGUID is the group's m_guid, set in Create (Group.cpp:150);
// MyIndex is the written member slot's position in the list and -1 only when the player has no slot, which
// SendUpdateToPlayer never sends because it returns first (Group.cpp:843-849, :862-875). The remaining rules are
// this package's: no empty or duplicate GUIDs, this character present exactly once at MyIndex, the leader listed.
func (s *State) validateRoster(u Update) string {
	if len(u.Members) > MaxRaidMembers {
		return fmt.Sprintf("%d members is over the raid limit of %d", len(u.Members), MaxRaidMembers)
	}
	if u.Type != GroupTypeNormal {
		return fmt.Sprintf("party type %d is not a normal group", u.Type)
	}
	if u.PartyGUID.IsZero() {
		return "the party GUID is empty"
	}
	seen := make(map[wire.GUID]bool, len(u.Members))
	for i := range u.Members {
		g := u.Members[i].GUID
		if g.IsZero() {
			return fmt.Sprintf("member %d has an empty GUID", i+1)
		}
		if seen[g] {
			return fmt.Sprintf("member %d repeats a GUID", i+1)
		}
		seen[g] = true
	}
	if s.SelfGUID.IsZero() {
		return "this character's own GUID is not known yet"
	}
	if !seen[s.SelfGUID] {
		return "this character's own GUID is not in the roster"
	}
	if int(u.MyIndex) < 0 || int(u.MyIndex) >= len(u.Members) {
		return fmt.Sprintf("MyIndex %d is outside the %d member(s)", u.MyIndex, len(u.Members))
	}
	if u.Members[u.MyIndex].GUID != s.SelfGUID {
		return fmt.Sprintf("member %d (MyIndex) is not this character", u.MyIndex+1)
	}
	if !seen[u.LeaderGUID] {
		return "the leader GUID is not in the roster"
	}
	return ""
}

// rosterConsistency applies the pending transition's check to the first roster after it and consumes the
// expectation. A roster that fails is "unexpected": the caller ends the party and reports it. This is a consistency
// check (does the roster belong to the transition the client made?), not a defence against a lying server: a
// server that lies can send a roster that matches.
func (s *State) rosterConsistency(u Update) string {
	p := s.pending
	if p == nil {
		return ""
	}
	if !p.leader.IsZero() {
		s.pending = nil
		if u.LeaderGUID != p.leader {
			return p.why + ": the leader is not the inviter"
		}
		return ""
	}
	if p.name == "" {
		s.pending = nil
		return ""
	}
	for i := range u.Members {
		if strings.EqualFold(u.Members[i].Name, p.name) {
			s.pending = nil
			return ""
		}
	}
	if len(u.Members) == 1 {
		// The group the server creates when the invite goes out: a roster naming this character alone (the live
		// run's first roster: type normal, MyIndex 0, one member). The invitee has not answered yet, so the roster
		// is consistent with the transition and the check stays armed for the next one.
		return ""
	}
	s.pending = nil
	return p.why + ": the invited name is not in the roster"
}

// rejectRoster ends the party on a roster the client cannot act on. The members the last accepted roster held are
// kept only in Stale, a diagnostic copy; Members is emptied and RosterValid is false, so every outbound party action
// that is gated on it stops.
func (s *State) rejectRoster(reason string) {
	if s.RosterValid {
		s.Stale = &Roster{LeaderGUID: s.LeaderGUID, Members: s.SnapshotOf()}
	}
	s.InParty = false
	s.Raid = false
	s.RosterValid = false
	s.Members = make(map[wire.GUID]*Member, MaxPartyMembers)
	s.Order = nil
	s.LeaderGUID = wire.EmptyGUID
}

// ApplyUpdate merges SMSG_PARTY_UPDATE and reports whether it produced a valid, modelled roster. A destroyed update
// ends the party. A roster that fails validateRoster, or the pending transition's check, is counted, ends the party
// (see rejectRoster) and is reported: a hostile or stale roster must not make the client act on a group that is not
// its own. A raid-sized roster is validated by the same rules and modelled like a party, up to MaxRaidMembers;
// Raid is the roster's raid flag (FlagRaid), not the member count.
func (s *State) ApplyUpdate(u Update) bool {
	s.Updates++
	return s.apply(u)
}

// apply is ApplyUpdate's body once the update is counted.
func (s *State) apply(u Update) bool {
	s.Listed = len(u.Members)
	if u.Flags&FlagDestroyed != 0 {
		s.Destroys++
		s.markDestroyed("group destroyed update")
		return false
	}
	if s.SelfGUID.IsZero() {
		// Logging in already grouped: the roster comes before anything names this character's GUID. Hold it (one,
		// replaced by a newer one) instead of calling it invalid; SetSelf applies it.
		held := u
		s.held = &held
		s.HeldRosters++
		return false
	}
	if reason := s.validateRoster(u); reason != "" {
		s.InvalidRosters++
		s.LastInvalid = reason
		s.LastUnexpected = ""
		s.pending = nil
		s.rejectRoster(reason)
		s.addEvent(Event{Kind: EventInvalidRoster, Text: "invalid roster: " + reason})
		return false
	}
	if reason := s.rosterConsistency(u); reason != "" {
		s.UnexpectedRosters++
		s.LastInvalid = ""
		s.LastUnexpected = reason
		s.rejectRoster(reason)
		s.addEvent(Event{Kind: EventUnexpectedRoster, Text: "unexpected roster: " + reason})
		return false
	}
	s.LastUnexpected = ""
	s.Destroyed = false
	s.RosterValid = true
	s.LastInvalid = ""
	s.Stale = nil
	s.Flags = u.Flags
	s.Index = u.Index
	s.Type = u.Type
	s.MyIndex = u.MyIndex
	if s.PartyGUID != u.PartyGUID {
		// A roster of another group is another group's icon list: the slots cannot carry over. The ready checks are
		// keyed by party, so both groups' checks stay tracked: the core broadcasts a check to every member, whichever
		// of the member's groups is active (Group.cpp:975-985). Until the roster named the other group its start
		// could not be keyed to it, which is why an unmatched finish is never applied to a later start.
		s.clearMarks()
	}
	s.PartyGUID = u.PartyGUID
	s.LeaderGUID = u.LeaderGUID
	s.Sequence = u.Sequence
	s.LeaderFactionGroup = u.LeaderFactionGroup
	s.HasLoot = u.HasLoot
	if u.HasLoot {
		s.Loot = u.Loot
	}
	s.HasDifficulty = u.HasDifficulty
	if u.HasDifficulty {
		s.Difficulty = u.Difficulty
	}
	s.Raid = u.Flags&FlagRaid != 0
	old := s.Members
	members := make(map[wire.GUID]*Member, len(u.Members))
	order := make([]wire.GUID, 0, len(u.Members))
	for i := range u.Members {
		entry := u.Members[i]
		m := entry
		m.Assistant = m.Flags&FlagAssistant != 0
		if prev, ok := old[m.GUID]; ok && prev.Full {
			// Keep what a full state taught us about health, power, level and position.
			m.Full = prev.Full
			m.Status = prev.Status
			m.Level = prev.Level
			m.Health = prev.Health
			m.MaxHealth = prev.MaxHealth
			m.PowerType = prev.PowerType
			m.CurrentPower = prev.CurrentPower
			m.MaxPower = prev.MaxPower
			m.ZoneID = prev.ZoneID
			m.X, m.Y, m.Z = prev.X, prev.Y, prev.Z
			m.VehicleSeat = prev.VehicleSeat
			m.Auras = prev.Auras
		}
		members[m.GUID] = &m
		order = append(order, m.GUID)
	}
	s.InParty = true // a valid roster always lists this character
	s.Members = members
	s.Order = order
	s.addEvent(Event{Kind: EventUpdate, Text: fmt.Sprintf("%d member(s), leader %s", len(order), memberName(s.LeaderName()))})
	return true
}

// ApplyMemberState merges a full member state into a listed member. It returns false (and counts the state) when
// the roster does not hold the GUID: the update is the authority on who is in the party.
func (s *State) ApplyMemberState(ms MemberState) bool {
	s.States++
	if !s.InParty || !s.RosterValid {
		s.OrphanStates++
		return false
	}
	m, ok := s.Members[ms.GUID]
	if !ok {
		s.OrphanStates++
		return false
	}
	if ms.Status == StatusOffline {
		m.Connected = false
		m.Full = false
		s.addEvent(Event{Kind: EventMemberState, Text: "offline"})
		return true
	}
	m.Full = true
	// A state that is not the offline one is a member the party still has: the flag the roster updates carry can go
	// stale when a bot logs out and back in (a likely reading of Run B's ten-minute stall, 2026-11-24; the run log has
	// no roster dump, so the cause is not proven).
	m.Connected = true
	m.Status = ms.Status
	m.Level = ms.Level
	m.Health = ms.Health
	m.MaxHealth = ms.MaxHealth
	m.PowerType = ms.PowerType
	m.CurrentPower = ms.CurrentPower
	m.MaxPower = ms.MaxPower
	m.ZoneID = ms.ZoneID
	m.X, m.Y, m.Z = ms.X, ms.Y, ms.Z
	m.VehicleSeat = ms.VehicleSeat
	m.Auras = ms.Auras
	s.addEvent(Event{Kind: EventMemberState, Text: fmt.Sprintf("%s %d/%d hp, %d/%d power, level %d", memberName(m.Name), ms.Health, ms.MaxHealth, ms.CurrentPower, ms.MaxPower, ms.Level)})
	return true
}

// ApplyMarkSingle merges one raid target update. A non-empty target is first cleared from every other icon, as
// Group::SetTargetIcon does before it stores the new slot (Group.cpp:825-831): the core sends its own clear updates
// for the old icon, but a target must never show two icons even if one of them is lost. A symbol outside 0..7 is
// counted and refused (SetTargetIcon drops it too, Group.cpp:822-823).
func (s *State) ApplyMarkSingle(u MarkUpdate) bool {
	s.MarkUpdates++
	if u.Mark < MarkStar || u.Mark > MarkSkull {
		return false
	}
	if !u.Target.IsZero() {
		for i := range s.Marks {
			if Mark(i) != u.Mark && s.Marks[i] == u.Target {
				s.Marks[i] = wire.EmptyGUID
			}
		}
	}
	s.Marks[u.Mark] = u.Target
	s.MarksKnown = true
	return true
}

// ApplyMarkAll replaces every icon with the full list: the answer to a list request is the group's whole target
// state, so a slot the list omits (or names empty) is not set.
func (s *State) ApplyMarkAll(l MarkList) bool {
	s.MarkLists++
	for _, e := range l.Marks {
		if e.Mark < MarkStar || e.Mark > MarkSkull {
			return false
		}
	}
	var marks [MaxMarks]wire.GUID
	for _, e := range l.Marks {
		marks[e.Mark] = e.Target
	}
	s.Marks = marks
	s.MarksKnown = true
	return true
}

// MarkOf returns the icon a target holds.
func (s *State) MarkOf(guid wire.GUID) (Mark, bool) {
	if guid.IsZero() {
		return 0, false
	}
	for i := range s.Marks {
		if s.Marks[i] == guid {
			return Mark(i), true
		}
	}
	return 0, false
}

// clearMarks forgets every icon: the icons belong to one group, so the group's end drops them with it.
func (s *State) clearMarks() {
	s.Marks = [MaxMarks]wire.GUID{}
	s.MarksKnown = false
}

// ApplyInvite stores an incoming invite.
func (s *State) ApplyInvite(inv Invite) {
	s.Invites++
	stored := inv
	s.Invite = &stored
	s.addEvent(Event{Kind: EventInvite, Name: inv.InviterName, Text: fmt.Sprintf("from %s", inv.InviterName)})
}

// AcceptInvite answers the stored invite: it forgets it and arms the transition check, so the first roster after
// the accept must name the inviter as its leader. It returns the inviter's GUID and false when nothing is pending.
func (s *State) AcceptInvite() (wire.GUID, bool) {
	inv := s.Invite
	if inv == nil {
		return wire.EmptyGUID, false
	}
	s.Invite = nil
	s.pending = &pendingRoster{leader: inv.InviterGUID, why: "the accepted invite"}
	return inv.InviterGUID, true
}

// ClearInvite forgets the stored invite, after it was accepted, declined or answered.
func (s *State) ClearInvite() { s.Invite = nil }

// ApplyDeclined records SMSG_GROUP_DECLINE.
func (s *State) ApplyDeclined(name string) {
	s.Declines++
	s.Invite = nil
	// A decline answers the invite, so no roster will follow it: the outgoing invite's expectation is dropped.
	if s.pending != nil && s.pending.name != "" && strings.EqualFold(s.pending.name, name) {
		s.pending = nil
	}
	s.addEvent(Event{Kind: EventInviteDeclined, Name: name, Text: name + " declined"})
}

// ApplyKicked records SMSG_GROUP_UNINVITE.
func (s *State) ApplyKicked(reason uint8) {
	s.Kicks++
	s.InParty = false
	s.RosterValid = false
	s.LastInvalid = ""
	s.LastUnexpected = ""
	s.pending = nil
	s.held = nil
	s.Stale = nil
	s.Members = make(map[wire.GUID]*Member, MaxPartyMembers)
	s.Order = nil
	s.clearMarks()
	s.addEvent(Event{Kind: EventKicked, Text: fmt.Sprintf("kicked (reason %d)", reason)})
}

// ApplyDestroyed records SMSG_GROUP_DESTROYED.
func (s *State) ApplyDestroyed() {
	s.Destroys++
	s.markDestroyed("SMSG_GROUP_DESTROYED")
}

func (s *State) markDestroyed(why string) {
	s.Destroyed = true
	s.InParty = false
	s.Raid = false
	s.RosterValid = false
	s.LastInvalid = ""
	s.LastUnexpected = ""
	s.pending = nil
	// The group the held roster named no longer exists; applying it when this character's GUID arrives would put
	// the model in a party the server has destroyed.
	s.held = nil
	s.Stale = nil
	s.Members = make(map[wire.GUID]*Member, MaxPartyMembers)
	s.Order = nil
	s.LeaderGUID = wire.EmptyGUID
	if rc := s.ReadyCheckOf(s.PartyGUID); rc != nil {
		// The group that ran this check is gone; nothing will broadcast its finish, so the check is history.
		rc.Active = false
	}
	// The destroyed party was the one this client asked for a check in; a start that never came must not hand its
	// held packets to a later check.
	if s.readyReq != nil && s.readyReq.party == s.PartyGUID {
		s.readyReq = nil
	}
	s.clearMarks()
	s.addEvent(Event{Kind: EventDestroyed, Text: why})
}

// ApplyLeaderChanged records SMSG_GROUP_NEW_LEADER. Names compare case-insensitively, as the rest of the party
// code does (the server's player-name lookup normalises first, ObjectAccessor.cpp:89-97).
func (s *State) ApplyLeaderChanged(l LeaderChanged) {
	s.LeaderChanges++
	if s.LeaderGUID != wire.EmptyGUID {
		// The packet carries the name, not the GUID; find the member by name to keep the model's leader GUID right.
		for _, m := range s.Members {
			if strings.EqualFold(m.Name, l.Name) {
				s.LeaderGUID = m.GUID
				break
			}
		}
	}
	s.addEvent(Event{Kind: EventLeaderChanged, Name: l.Name, Text: "leader is now " + l.Name})
}

// BeginReadyCheck records that this client asked the core for a ready check, in the party at the given party index.
// The core may broadcast the offline members' answers and finish a one-member check before it broadcasts the start
// (Group.cpp:1484-1493); those packets are held and applied only to the start this request produces. A new request
// replaces an abandoned one, so an earlier request's packets never reach a new check.
func (s *State) BeginReadyCheck(index uint8) {
	if !s.RosterValid || s.PartyGUID.IsZero() {
		return
	}
	s.readyReq = &readyRequest{party: s.PartyGUID, index: int8(index)}
}

// CancelReadyCheck forgets a request whose start never came (the core refuses a second start while one runs and
// answers with nothing, Group.cpp:1474-1475), so its held packets cannot be applied to a later check.
func (s *State) CancelReadyCheck() { s.readyReq = nil }

// ReadyCheckOf returns the check of the given party, or nil when the model saw none. Checks are keyed by party, so a
// start in the home group never displaces the instance group's check or the other way round.
func (s *State) ReadyCheckOf(partyGUID wire.GUID) *ReadyCheck {
	for _, rc := range s.checks {
		if rc.PartyGUID == partyGUID {
			return rc
		}
	}
	return nil
}

// storeReadyCheck makes rc the party's current check, forgetting a previous check for the same party (a new start
// replaces it) and the oldest check when the bound is reached. The check just stored is never the one dropped.
func (s *State) storeReadyCheck(rc *ReadyCheck) {
	for i, c := range s.checks {
		if c.PartyGUID == rc.PartyGUID {
			s.checks = append(s.checks[:i], s.checks[i+1:]...)
			break
		}
	}
	if len(s.checks) >= MaxReadyChecks {
		drop := 0
		if s.checks[0].PartyGUID == s.PartyGUID {
			// Keep the check of the party the roster currently names; the next-oldest is the diagnostic history.
			drop = 1
		}
		s.checks = append(s.checks[:drop], s.checks[drop+1:]...)
	}
	s.checks = append(s.checks, rc)
}

// ApplyReadyCheckStarted records SMSG_READY_CHECK_STARTED: a new check replaces that party's last one and starts a
// fresh answer list. The core refuses a second start while one runs in a group (Group.cpp:1474-1475), so a start is
// authoritative for its party. The answers and the finish that belong to this start (the offline members' answers
// and a one-member check's immediate finish, Group.cpp:1484-1493) arrived before it only when this client asked for
// the check; they are applied now, without new events (the event ring already has those rows).
func (s *State) ApplyReadyCheckStarted(r ReadyCheckStarted) {
	s.ReadyChecks++
	rc := &ReadyCheck{
		Active:    true,
		Index:     r.Index,
		PartyGUID: r.PartyGUID,
		Initiator: r.Initiator,
		Duration:  r.Duration,
		Answers:   make(map[wire.GUID]bool, MaxPartyMembers),
	}
	s.storeReadyCheck(rc)
	s.ReadyCheck = rc
	if req := s.readyReq; req != nil && req.party == r.PartyGUID {
		// The start of the requested party answers the request: its held packets go to this check only when the
		// start is this client's own and names the requested group. A start another member got (the core refused
		// this request because their check already ran) is a different check and takes none of the held packets.
		s.readyReq = nil
		if req.index == r.Index && r.Initiator == s.SelfGUID {
			for _, a := range req.answers {
				s.recordReadyResponse(rc, a)
			}
			if req.complete != nil {
				rc.Active = false
				rc.Complete = true
			}
		}
	}
	s.addEvent(Event{Kind: EventReadyCheckStarted, Text: "ready check started"})
}

// ApplyReadyCheckResponse records SMSG_READY_CHECK_RESPONSE. An answer merges into its party's running check; with
// none, it is held only for the start this client's own request will produce and is otherwise just an event. The
// order matters: a member that joined or reconnected during a check sees its later answers and its finish with no
// start, and those must never be read as answers of the next check.
func (s *State) ApplyReadyCheckResponse(r ReadyCheckResponse) {
	s.ReadyCheckAnswers++
	text := "not ready"
	if r.Ready {
		text = "ready"
	}
	name := ""
	if m, ok := s.Members[r.Player]; ok {
		name = m.Name
	}
	s.addEvent(Event{Kind: EventReadyCheckResponse, Text: text, Name: name})
	if rc := s.ReadyCheckOf(r.PartyGUID); rc != nil && rc.Active {
		s.recordReadyResponse(rc, r)
		return
	}
	if req := s.readyReq; req != nil && req.party == r.PartyGUID {
		if req.complete != nil {
			// The core sends a check's finish after all of its answers (EndReadyCheck runs from
			// SetMemberReadyChecked, Group.cpp:1484-1493), so a finish that a later answer for the same party
			// follows is the previous check's, held while this request was in flight; it must not end this one.
			req.complete = nil
		}
		if len(req.answers) < MaxRaidMembers {
			req.answers = append(req.answers, r)
		}
	}
}

// recordReadyResponse merges one answer into a check, bounded to the roster and to MaxRaidMembers.
func (s *State) recordReadyResponse(rc *ReadyCheck, r ReadyCheckResponse) {
	// A valid roster bounds the answers to its members; the raid bound also keeps a hostile server from growing the
	// map with unknown GUIDs when no roster is held.
	if s.RosterValid {
		if _, ok := s.Members[r.Player]; !ok {
			return
		}
	}
	if _, seen := rc.Answers[r.Player]; !seen {
		if len(rc.Answers) >= MaxRaidMembers {
			return
		}
		rc.Answered = append(rc.Answered, r.Player)
	}
	rc.Answers[r.Player] = r.Ready
}

// ApplyReadyCheckCompleted records SMSG_READY_CHECK_COMPLETED: the core ends a check itself once every member of the
// group has answered or its timer ran out (Group.cpp:1496-1511), so the model keeps that check's answers and marks
// it finished. It reports whether a tracked check ended: a finish for a start that has not arrived is held only for
// the check this client asked for and whose start is still owed, and any other finish (a check whose start this
// client never saw) is an event, never the next check's finish.
func (s *State) ApplyReadyCheckCompleted(r ReadyCheckCompleted) bool {
	s.ReadyCheckCompletes++
	applied := false
	if rc := s.ReadyCheckOf(r.PartyGUID); rc != nil && rc.Active {
		rc.Active = false
		rc.Complete = true
		applied = true
	} else if req := s.readyReq; req != nil && req.party == r.PartyGUID && req.complete == nil {
		c := r
		req.complete = &c
	}
	s.addEvent(Event{Kind: EventReadyCheckCompleted, Text: "ready check finished"})
	return applied
}

// ApplyCommandResult records SMSG_PARTY_COMMAND_RESULT and, for a failed invite, records the failure. A successful
// invite arms the transition check: the first roster after it must list the invited name. A successful leave clears
// the model (the server sends the empty update too).
func (s *State) ApplyCommandResult(c CommandResult) {
	s.CommandResults++
	s.addEvent(Event{Kind: EventCommandResult, Name: c.Name, Operation: c.Operation, Result: c.Result,
		Text: fmt.Sprintf("%s %s: %s", OperationName(c.Operation), c.Name, resultText(c.Result))})
	switch {
	case c.Operation == 0 && c.Result == 0 && c.Name != "": // PARTY_OP_INVITE, ok
		s.pending = &pendingRoster{name: c.Name, why: "the confirmed invite"}
	case c.Operation == 2 && c.Result == 0: // PARTY_OP_LEAVE, ok
		s.InParty = false
		s.RosterValid = false
		s.LastInvalid = ""
		s.LastUnexpected = ""
		s.pending = nil
		s.Stale = nil
		s.Members = make(map[wire.GUID]*Member, MaxPartyMembers)
		s.Order = nil
		if rc := s.ReadyCheckOf(s.PartyGUID); rc != nil {
			// This client is no longer a member of the group, so its check can have no further answers or finish.
			rc.Active = false
		}
		if s.readyReq != nil && s.readyReq.party == s.PartyGUID {
			s.readyReq = nil
		}
		s.clearMarks()
		s.addEvent(Event{Kind: EventLeft, Text: "left the group"})
	}
}

func resultText(r uint8) string {
	if n := ResultName(r); n != "" {
		return n
	}
	return fmt.Sprintf("result %d", r)
}

// LeaderName returns the roster name of the leader, or the last name the leader-change packet carried.
func (s *State) LeaderName() string {
	for _, m := range s.Members {
		if m.GUID == s.LeaderGUID {
			return m.Name
		}
	}
	return ""
}

// SnapshotOf returns the members in roster order.
func (s *State) SnapshotOf() []Member {
	out := make([]Member, 0, len(s.Order))
	for _, g := range s.Order {
		if m, ok := s.Members[g]; ok {
			out = append(out, *m)
		}
	}
	return out
}

// memberName names a member in an event; an unnamed member is a dash, never a GUID.
func memberName(name string) string {
	if name == "" {
		return "-"
	}
	return name
}
