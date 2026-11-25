package world

// Raid control (#108 R2): convert the leader's party into a raid and move members between the eight subgroups. The
// two calls share the group layer's shape (rank check, packet, roster readback), but their ranks are the raid's: the
// leader converts (GroupHandler.cpp:489-497), the leader or an assistant moves (GroupHandler.cpp:519-526), and this
// core answers a raid packet it refuses with nothing at all, so the readback is the only proof it was applied.

import (
	"context"
	"fmt"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// raidRoster checks the precondition the raid calls share: a group the model holds a valid roster for. Unlike
// groupRoster it takes a raid (that is the point) and a party too, since convert_raid starts from one.
func (s *session) raidRoster(what string) error {
	s.initChat()
	switch {
	case !s.party.InParty:
		return fmt.Errorf("%s: not in a group", what)
	case !s.party.RosterValid:
		return fmt.Errorf("%s: no valid party roster (%s)", what, s.party.LastInvalid)
	}
	return nil
}

// raidLeader reports whether this character leads the group.
func (s *session) raidLeader() bool { return s.party.LeaderGUID == s.party.SelfGUID }

// raidLeaderRank is raidRoster plus the leader rank the core asks of the raid commands only the leader may send:
// the assistant rank (CMSG_SET_ASSISTANT_LEADER) is taken only from Group::IsLeader (GroupHandler.cpp:545-555). The
// message is the party leader-only one, so a caller reads the same refusal in a party and in a raid.
func (s *session) raidLeaderRank(what string) error {
	if err := s.raidRoster(what); err != nil {
		return err
	}
	if !s.raidLeader() {
		return fmt.Errorf("%s: only the leader can (the leader is %s)", what, chat.SanitiseName(s.party.LeaderName()))
	}
	return nil
}

// raidAssistant reports whether this character holds MEMBER_FLAG_ASSISTANT (Group.h:59-72).
func (s *session) raidAssistant() bool {
	if m, ok := s.party.Members[s.party.SelfGUID]; ok {
		return m.Assistant
	}
	return false
}

// raidMember resolves the member a call names by guid or by name (case-insensitively). This character counts: the
// leader can move itself.
func (s *session) raidMember(what string, g wire.GUID, name string) (party.Member, error) {
	if !g.IsZero() {
		if m, ok := s.party.Members[g]; ok {
			return *m, nil
		}
		return party.Member{}, fmt.Errorf("%s: guid %s is not in the roster", what, g)
	}
	if strings.TrimSpace(name) == "" {
		return party.Member{}, fmt.Errorf("%s: needs a member's guid or name", what)
	}
	return s.groupMember(what, name)
}

// raidSubgroup is a member's subgroup as the raid frame and the serve number it, 1-8: the wire value is 0-based.
func raidSubgroup(m party.Member) int { return int(m.Subgroup) + 1 }

// raidOccupancy counts the roster's members in a 1-based subgroup.
func (s *session) raidOccupancy(subgroup int) int {
	n := 0
	for _, m := range s.party.SnapshotOf() {
		if raidSubgroup(m) == subgroup {
			n++
		}
	}
	return n
}

// raidInSubgroup reports whether the roster holds guid in the 1-based subgroup.
func (s *session) raidInSubgroup(guid wire.GUID, subgroup int) bool {
	if m, ok := s.party.Members[guid]; ok {
		return raidSubgroup(*m) == subgroup
	}
	return false
}

// convertRaid turns the leader's party into a raid (CMSG_CONVERT_RAID) and waits for the roster whose raid flag it
// is. The core converts only the leader's group of two or more members and answers a party result; a party that is
// a raid already is done without a packet.
func (s *session) convertRaid(ctx context.Context) error {
	const what = "convert_raid"
	if err := s.raidRoster(what); err != nil {
		return err
	}
	if !s.raidLeader() {
		return fmt.Errorf("%s: needs the group leader (the leader is %s)", what, chat.SanitiseName(s.party.LeaderName()))
	}
	if n := len(s.party.SnapshotOf()); n < 2 {
		return fmt.Errorf("%s: the group has %d member(s): the core converts a group of two or more", what, n)
	}
	if s.party.Raid {
		return nil
	}
	if err := s.Send(wire.CMSGConvertRaid, party.BuildConvertRaid(true)); err != nil {
		return err
	}
	s.opts.logf("raid: convert %d members", len(s.party.SnapshotOf()))
	return s.waitRaidConfirm(ctx, what, groupConfirmWait, func() bool { return s.party.Raid })
}

// raidMove puts a raid member in a subgroup (1-8, as the raid frame numbers them). The leader or an assistant may;
// the core drops a move into a full subgroup (five members, Group.cpp:1735-1738), so a swapWith names the member of
// the target subgroup who trades places instead (CMSG_SWAP_SUB_GROUPS), and a plain move sends
// CMSG_CHANGE_SUB_GROUP with the 0-based wire subgroup.
func (s *session) raidMove(ctx context.Context, g wire.GUID, name string, subgroup int, swapWith string) error {
	const what = "raid_move"
	if subgroup < 1 || subgroup > party.MaxSubgroups {
		return fmt.Errorf("%s: subgroup %d: use 1 to %d", what, subgroup, party.MaxSubgroups)
	}
	if err := s.raidRoster(what); err != nil {
		return err
	}
	if !s.raidLeader() && !s.raidAssistant() {
		return fmt.Errorf("%s: needs the group leader or an assistant", what)
	}
	if !s.party.Raid {
		return fmt.Errorf("%s: needs a raid: convert_raid first", what)
	}
	m, err := s.raidMember(what, g, name)
	if err != nil {
		return err
	}
	if swapWith != "" {
		o, err := s.groupMember(what, swapWith)
		if err != nil {
			return err
		}
		if o.GUID == m.GUID {
			return fmt.Errorf("%s: cannot swap a member with itself", what)
		}
		// The swap trades the two members' subgroups, so swap_with must name a member of the target subgroup in
		// another subgroup than the member being moved.
		if raidSubgroup(o) != subgroup || o.Subgroup == m.Subgroup {
			return fmt.Errorf("%s: %s is in subgroup %d: swap_with must name a member of subgroup %d in another subgroup than %s",
				what, chat.SanitiseName(o.Name), raidSubgroup(o), subgroup, chat.SanitiseName(m.Name))
		}
		from := raidSubgroup(m)
		if err := s.Send(wire.CMSGSwapSubGroups, party.BuildSwapSubGroups(m.GUID, o.GUID)); err != nil {
			return err
		}
		s.opts.logf("raid: swap %s with %s", chat.SanitiseName(m.Name), chat.SanitiseName(o.Name))
		return s.waitRaidConfirm(ctx, what, groupConfirmWait, func() bool {
			return s.raidInSubgroup(m.GUID, subgroup) && s.raidInSubgroup(o.GUID, from)
		})
	}
	if raidSubgroup(m) == subgroup {
		return nil
	}
	if n := s.raidOccupancy(subgroup); n >= party.MaxPartyMembers {
		return fmt.Errorf("%s: subgroup %d is full (%d members): pass swap_with to trade places with one of them", what, subgroup, n)
	}
	payload, err := party.BuildChangeSubGroup(m.GUID, uint8(subgroup-1))
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if err := s.Send(wire.CMSGChangeSubGroup, payload); err != nil {
		return err
	}
	s.opts.logf("raid: move %s to subgroup %d", chat.SanitiseName(m.Name), subgroup)
	return s.waitRaidConfirm(ctx, what, groupConfirmWait, func() bool { return s.raidInSubgroup(m.GUID, subgroup) })
}
