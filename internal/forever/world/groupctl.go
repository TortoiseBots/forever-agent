package world

// Group control for the party leader (#95, round G2): promote a member, set or clear an assistant, change the loot
// method, set a raid target, read the icons back, and disband. A refused group command is silent in this core: no
// packet comes back for a bad rank, an unknown member or a refused value (PartyPackets.cpp's handlers answer
// nothing), so every call sends its packet and then waits for the readback that packet produces — the roster's
// leader, the loot block, the member flag, the icon slot, the end of the party — and fails without it.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// groupConfirmWait bounds the wait for a group command's readback. A variable so the tests shorten it.
var groupConfirmWait = 5 * time.Second

// groupConfirmPoll bounds one packet wait, so the caller's context ends the wait promptly.
const groupConfirmPoll = 200 * time.Millisecond

// assistantFlag is MEMBER_FLAG_ASSISTANT (Group.h:65): the roster flag bit CMSG_SET_ASSISTANT_LEADER sets and clears
// (GroupHandler.cpp:549-558).
const assistantFlag = 0x01

// lootMethodNames are the words the loot method is named by: LootName's spelling plus the short form the commands
// use ("group", "need", ...).
var lootMethodNames = map[string]party.LootMethod{
	"free-for-all":      party.FreeForAll,
	"ffa":               party.FreeForAll,
	"round-robin":       party.RoundRobin,
	"round":             party.RoundRobin,
	"master-loot":       party.MasterLoot,
	"master":            party.MasterLoot,
	"group-loot":        party.GroupLoot,
	"group":             party.GroupLoot,
	"need-before-greed": party.NeedBeforeGreed,
	"need":              party.NeedBeforeGreed,
	"personal-loot":     party.PersonalLoot,
	"personal":          party.PersonalLoot,
}

// ParseLootMethod parses a loot method's name (LootName's spelling or the short form) or its number 0..5.
func ParseLootMethod(s string) (party.LootMethod, bool) {
	if m, ok := lootMethodNames[strings.ToLower(strings.TrimSpace(s))]; ok {
		return m, true
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 && n <= int(party.PersonalLoot) {
		return party.LootMethod(n), true
	}
	return 0, false
}

// ParseLootSpec parses "METHOD[:THRESHOLD]", the form the --loot flag and the loot_method method take: the method's
// name or number, and optionally the threshold, 2..6 (uncommon..artifact, the core's own bounds). An absent
// threshold is uncommon (2), the player run's setting.
func ParseLootSpec(s string) (party.LootMethod, int, error) {
	name, thr := strings.TrimSpace(s), ""
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name, thr = name[:i], name[i+1:]
	}
	method, ok := ParseLootMethod(name)
	if !ok {
		return 0, 0, fmt.Errorf("%q is not a loot method (free-for-all, round-robin, master-loot, group-loot, need-before-greed, personal-loot, or 0..5)", name)
	}
	threshold := 2
	if thr != "" {
		n, err := strconv.Atoi(thr)
		if err != nil || n < 2 || n > 6 {
			return 0, 0, fmt.Errorf("loot threshold %q is outside 2..6 (uncommon..artifact)", thr)
		}
		threshold = n
	}
	return method, threshold, nil
}

// ParseRaidMark parses a raid target icon's name, the form the mark method and --mark take: party.ParseMarkName,
// wrapped so the command layer needs no party import.
func ParseRaidMark(s string) (party.Mark, bool) { return party.ParseMarkName(s) }

// groupRoster checks the precondition every group call shares: a group the model holds a valid roster for. It takes
// a party and a raid: the roster model holds both (party.State), and the core's group commands read the group's
// leader and members whatever its size (GroupHandler.cpp:394-430). The ranks that differ in a raid are checked by the
// call itself where they matter (an assistant may mark icons, GroupHandler.cpp:468-474).
func (s *session) groupRoster(what string) error {
	s.initChat()
	switch {
	case !s.party.InParty:
		return fmt.Errorf("%s: not in a group", what)
	case !s.party.RosterValid:
		return fmt.Errorf("%s: no valid party roster (%s)", what, s.party.LastInvalid)
	}
	return nil
}

// groupLeaderRank is groupRoster plus the rank the core asks of the leader-only party commands (promote, loot,
// disband): CMSG_SET_PARTY_LEADER, CMSG_SET_LOOT_METHOD and the uninvites of a disband are taken only from
// Group::IsLeader (GroupHandler.cpp:289-328, :407-427). The assistant rank is the raid's and lives in
// raidLeaderRank (raidctl.go).
func (s *session) groupLeaderRank(what string) error {
	if err := s.groupRoster(what); err != nil {
		return err
	}
	if s.party.LeaderGUID != s.party.SelfGUID {
		return fmt.Errorf("%s: only the leader can (the leader is %s)", what, chat.SanitiseName(s.party.LeaderName()))
	}
	return nil
}

// groupMember finds a roster member by name, case-insensitively (the server's own name lookups normalise first).
func (s *session) groupMember(what, name string) (party.Member, error) {
	norm, err := chat.NormaliseName(name)
	if err != nil {
		return party.Member{}, fmt.Errorf("%s: %w", what, err)
	}
	for _, m := range s.party.SnapshotOf() {
		if m.Name != "" && strings.EqualFold(m.Name, norm) {
			return m, nil
		}
	}
	return party.Member{}, fmt.Errorf("%s: no member named %s in the roster", what, chat.SanitiseName(norm))
}

// waitGroupConfirm waits for a group command's readback: check is polled after every packet, and a server that
// answers nothing is the error a refused command gets. ctx ends the wait with the caller's reason.
func (s *session) waitGroupConfirm(ctx context.Context, what string, timeout time.Duration, check func() bool) error {
	return s.waitGroupReadback(ctx, what, timeout, "the server did not confirm it", "a refused group command gets no answer", check)
}

// waitRaidConfirm is waitGroupConfirm for the raid commands: the core drops a raid packet it refuses without an
// answer (GroupHandler.cpp:519-526), so a readback that never comes is "the server did not apply it".
func (s *session) waitRaidConfirm(ctx context.Context, what string, timeout time.Duration, check func() bool) error {
	return s.waitGroupReadback(ctx, what, timeout, "the server did not apply it", "the core drops a refused raid command silently", check)
}

// errGroupUnconfirmed is a group command's readback that never came. The command may still have been applied (the
// echo was lost), so a caller that can afford the order being missed treats it as a miss, not a refusal.
var errGroupUnconfirmed = errors.New("the readback did not come")

// waitGroupReadback polls check after every packet until it holds or timeout passes; a timeout is "<what>:
// <refusal> within <timeout> (<hint>)".
func (s *session) waitGroupReadback(ctx context.Context, what string, timeout time.Duration, refusal, hint string, check func() bool) error {
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
			return fmt.Errorf("%s: %s within %s (%s): %w", what, refusal, timeout, hint, errGroupUnconfirmed)
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

// groupPromote makes a member the party leader (CMSG_SET_PARTY_LEADER) and waits for the roster whose leader it is.
// The server also sends SMSG_GROUP_NEW_LEADER, which the model applies; either readback sets LeaderGUID.
func (s *session) groupPromote(ctx context.Context, name string) error {
	const what = "promote"
	if err := s.groupLeaderRank(what); err != nil {
		return err
	}
	m, err := s.groupMember(what, name)
	if err != nil {
		return err
	}
	if m.GUID == s.party.SelfGUID {
		return fmt.Errorf("%s: %s already leads the group", what, chat.SanitiseName(m.Name))
	}
	if !m.Connected {
		return fmt.Errorf("%s: %s is not online and the server takes a leader only from the online members", what, chat.SanitiseName(m.Name))
	}
	if err := s.Send(wire.CMSGSetPartyLeader, party.BuildSetPartyLeader(m.GUID)); err != nil {
		return err
	}
	s.opts.logf("group: promote %s", chat.SanitiseName(m.Name))
	return s.waitGroupConfirm(ctx, what, groupConfirmWait, func() bool { return s.party.LeaderGUID == m.GUID })
}

// groupAssistant sets or clears a raid member's assistant flag (CMSG_SET_ASSISTANT_LEADER) and waits for the roster
// that carries the flag. The flag is a raid rank: the core sets it only in a raid group and only from the leader,
// silently refusing both a party and anyone else (Group::SetGroupMemberFlag, Group.cpp:1758-1802, called by
// GroupHandler.cpp:545-555), so a party is refused here before the packet. The server broadcasts a fresh roster after
// the change: the member's flags byte in it is the readback (Group.h:59-72).
func (s *session) groupAssistant(ctx context.Context, name string, apply bool) error {
	const what = "assistant"
	if err := s.raidLeaderRank(what); err != nil {
		return err
	}
	if !s.party.Raid {
		return fmt.Errorf("%s: needs a raid: convert_raid first", what)
	}
	m, err := s.groupMember(what, name)
	if err != nil {
		return err
	}
	if m.Assistant == apply {
		s.opts.logf("group: assistant %s: already %v", chat.SanitiseName(m.Name), apply)
		return nil
	}
	if err := s.paceRaidPacket(ctx); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if err := s.Send(wire.CMSGSetAssistantLeader, party.BuildSetAssistantLeader(m.GUID, apply)); err != nil {
		return err
	}
	s.opts.logf("group: assistant %s: apply %v", chat.SanitiseName(m.Name), apply)
	return s.waitGroupConfirm(ctx, what, groupConfirmWait, func() bool {
		cur, ok := s.party.Members[m.GUID]
		if !ok {
			return false
		}
		return (cur.Flags&assistantFlag != 0) == apply
	})
}

// groupLootMethod sets the loot method, the master ("" for none) and the threshold (0 means uncommon), and waits for
// the roster's loot block. The core takes the packet only from the leader, refuses an LFG group and refuses a master
// loot without a member (GroupHandler.cpp:395-427); the builder refuses the same values before they are sent.
func (s *session) groupLootMethod(ctx context.Context, method party.LootMethod, master string, threshold int) error {
	const what = "loot_method"
	if err := s.groupLeaderRank(what); err != nil {
		return err
	}
	if s.party.Flags&party.FlagLFG != 0 {
		return fmt.Errorf("%s: the core refuses a loot change in an LFG group", what)
	}
	if threshold == 0 {
		threshold = 2 // uncommon, ItemQualities
	}
	var masterGUID wire.GUID
	if master != "" {
		m, err := s.groupMember(what, master)
		if err != nil {
			return err
		}
		masterGUID = m.GUID
	}
	payload, err := party.BuildSetLootMethod(method, masterGUID, uint32(threshold))
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if err := s.Send(wire.CMSGSetLootMethod, payload); err != nil {
		return err
	}
	s.opts.logf("group: loot %s, threshold %d, master %q", party.LootName(method), threshold, chat.SanitiseName(master))
	return s.waitGroupConfirm(ctx, what, groupConfirmWait, func() bool {
		return s.party.HasLoot && s.party.Loot.Method == method && int(s.party.Loot.Threshold) == threshold &&
			(method != party.MasterLoot || s.party.Loot.Master == masterGUID)
	})
}

// groupMark sets a raid target icon on a unit, or clears the icon with an empty target, and waits for the icon slot.
// Any member of a party may set one; in a raid only the leader or an assistant (GroupHandler.cpp:456-479 drops anyone
// else silently), so a raid checks that rank before the packet.
func (s *session) groupMark(ctx context.Context, target wire.GUID, mark party.Mark) error {
	const what = "mark"
	if err := s.groupRoster(what); err != nil {
		return err
	}
	if s.party.Raid && !s.raidLeader() && !s.raidAssistant() {
		return fmt.Errorf("%s: needs the group leader or an assistant", what)
	}
	if mark < party.MarkStar || mark > party.MarkSkull {
		return fmt.Errorf("%s: %d is not a raid target icon (0..7)", what, mark)
	}
	payload, err := party.BuildUpdateRaidTarget(target, mark)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	before := s.party.MarkUpdates
	if err := s.Send(wire.CMSGUpdateRaidTarget, payload); err != nil {
		return err
	}
	if target.IsZero() {
		s.opts.logf("group: clear raid mark %s", party.MarkName(mark))
	} else {
		s.opts.logf("group: raid mark %s on %s", party.MarkName(mark), target)
	}
	return s.waitGroupConfirm(ctx, what, groupConfirmWait, func() bool {
		return s.party.MarkUpdates > before && s.party.Marks[mark] == target
	})
}

// groupMarks asks for the group's whole icon list (CMSG_UPDATE_RAID_TARGET with the symbol -1) and returns the
// eight slots once SMSG_SEND_RAID_TARGET_UPDATE_ALL has arrived.
func (s *session) groupMarks(ctx context.Context) ([8]wire.GUID, error) {
	const what = "marks"
	var none [8]wire.GUID
	if err := s.groupRoster(what); err != nil {
		return none, err
	}
	before := s.party.MarkLists
	if err := s.Send(wire.CMSGUpdateRaidTarget, party.BuildRequestMarks()); err != nil {
		return none, err
	}
	if err := s.waitGroupConfirm(ctx, what, groupConfirmWait, func() bool { return s.party.MarkLists > before }); err != nil {
		return none, err
	}
	return s.party.Marks, nil
}

// groupDisband ends the party the way the core allows: there is no disband packet, so the leader uninvites every
// other member in roster order (D-5) and the last uninvite leaves this character alone, which is the group's end.
func (s *session) groupDisband(ctx context.Context) error {
	const what = "disband"
	if err := s.groupLeaderRank(what); err != nil {
		return err
	}
	var others []party.Member
	for _, g := range s.party.Order {
		if g == s.party.SelfGUID {
			continue
		}
		if m, ok := s.party.Members[g]; ok {
			others = append(others, *m)
		}
	}
	if len(others) == 0 {
		return fmt.Errorf("%s: the group has no other member", what)
	}
	for _, m := range others {
		if err := s.Send(wire.CMSGPartyUninvite, party.BuildUninvite(m.GUID, "")); err != nil {
			return err
		}
		s.opts.logf("group: disband: uninvited %s", chat.SanitiseName(m.Name))
	}
	return s.waitGroupConfirm(ctx, what, groupConfirmWait, func() bool { return !s.party.InParty })
}

// partyGroupControl runs party-check's group-control flags (--promote, --promote-back, --loot, --mark) and returns
// without leaving the group: a leave ends a two-member group, which is what these flags exist to avoid.
func (s *session) partyGroupControl(ctx context.Context) error {
	if !s.party.RosterValid {
		return fmt.Errorf("world: party-check: no valid party roster (%s)", s.party.LastInvalid)
	}
	if s.opts.PartyPromote != "" {
		if err := s.groupPromote(ctx, s.opts.PartyPromote); err != nil {
			return fmt.Errorf("world: party-check: %w", err)
		}
		s.opts.logf("party-check: promoted %s", chat.SanitiseName(s.opts.PartyPromote))
		if s.opts.PartyPromoteBack > 0 {
			// The other side is an agent session that promotes back: wait for the lead to come home.
			if err := s.waitGroupConfirm(ctx, "promote back", s.opts.PartyPromoteBack, func() bool {
				return s.party.LeaderGUID == s.party.SelfGUID
			}); err != nil {
				return fmt.Errorf("world: party-check: %w", err)
			}
			s.opts.logf("party-check: the new leader promoted back")
		}
	}
	if s.opts.PartyLoot != "" {
		method, threshold, err := ParseLootSpec(s.opts.PartyLoot)
		if err != nil {
			return fmt.Errorf("world: party-check: --loot: %w", err)
		}
		if err := s.groupLootMethod(ctx, method, "", threshold); err != nil {
			return fmt.Errorf("world: party-check: %w", err)
		}
	}
	if s.opts.PartyMark != "" {
		mark, ok := party.ParseMarkName(s.opts.PartyMark)
		if !ok {
			return fmt.Errorf("world: party-check: --mark: %q is not a raid target icon", s.opts.PartyMark)
		}
		guid, name, err := s.nearestAttackable()
		if err != nil {
			return fmt.Errorf("world: party-check: --mark: %w", err)
		}
		if err := s.groupMark(ctx, guid, mark); err != nil {
			return fmt.Errorf("world: party-check: %w", err)
		}
		icons, err := s.groupMarks(ctx)
		if err != nil {
			return fmt.Errorf("world: party-check: %w", err)
		}
		if icons[mark] != guid {
			return fmt.Errorf("world: party-check: --mark: the icon list does not hold %s on %s", party.MarkName(mark), chat.SanitiseName(name))
		}
		s.opts.logf("party-check: mark %s read back on %s", party.MarkName(mark), chat.SanitiseName(name))
	}
	return nil
}

// nearestAttackable returns the closest living unit hostile to this character, the target of party-check's --mark:
// the same filter the units method's hostile=true applies.
func (s *session) nearestAttackable() (wire.GUID, string, error) {
	hostile := true
	units, err := s.serveUnits(serveUnitsParams{Hostile: &hostile, Limit: 8})
	if err != nil {
		return wire.EmptyGUID, "", err
	}
	for _, u := range units {
		if u.Dead {
			continue
		}
		return wire.GUID(u.GUID), u.Name, nil
	}
	return wire.EmptyGUID, "", errors.New("no attackable creature nearby")
}
