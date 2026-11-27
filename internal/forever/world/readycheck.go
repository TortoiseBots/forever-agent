package world

// The ready check as the playing character (#288): the leader sends CMSG_DO_READY_CHECK and every member's answer and
// the finish reach the session's event stream from handleParty. The core takes the start from the leader or an
// assistant (GroupHandler.cpp:584-605) and answers a refused one with nothing, so the method waits for the broadcast
// start before it returns.

import (
	"context"

	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// readyCheck starts a ready check and waits for the start the core broadcasts to the group. This character must lead
// the group; a raid is allowed, as GroupHandler.cpp's leader-or-assistant rule is the raid's own. The answers and the
// finish arrive later as party events (ready_check_response, ready_check_completed).
func (s *session) readyCheck(ctx context.Context) error {
	const what = "ready_check"
	if err := s.raidLeaderRank(what); err != nil {
		return err
	}
	before := s.party.ReadyChecks
	if err := s.Send(wire.CMSGDoReadyCheck, party.BuildDoReadyCheck(s.party.Index)); err != nil {
		return err
	}
	s.opts.logf("party: sent a ready check for %d member(s) (index %d)", len(s.party.Members), s.party.Index)
	return s.waitGroupConfirm(ctx, what, groupConfirmWait, func() bool {
		rc := s.party.ReadyCheck
		return s.party.ReadyChecks > before && rc != nil && rc.Initiator == s.party.SelfGUID && rc.PartyGUID == s.party.PartyGUID
	})
}
