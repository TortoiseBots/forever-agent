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

// readyCheck starts a ready check and waits for the start the core broadcasts to the group, returning that start's
// check. This character must lead the group; a raid is allowed, as GroupHandler.cpp's leader-or-assistant rule is the
// raid's own. The answers and the finish arrive later as party events (ready_check_response,
// ready_check_completed); the returned check is the one a reader must wait on, never a later check of the party.
func (s *session) readyCheck(ctx context.Context) (*party.ReadyCheck, error) {
	const what = "ready_check"
	if err := s.raidLeaderRank(what); err != nil {
		return nil, err
	}
	partyGUID, index := s.party.PartyGUID, s.party.Index
	before := s.party.ReadyChecks
	// The answers and a one-member finish the core sends before the start belong to the check asked for here, and
	// to no later one.
	s.party.BeginReadyCheck(index)
	if err := s.Send(wire.CMSGDoReadyCheck, party.BuildDoReadyCheck(index)); err != nil {
		s.party.CancelReadyCheck()
		return nil, err
	}
	s.opts.logf("party: sent a ready check for %d member(s) (index %d)", len(s.party.Members), index)
	err := s.waitGroupConfirm(ctx, what, groupConfirmWait, func() bool {
		rc := s.party.ReadyCheckOf(partyGUID)
		return s.party.ReadyChecks > before && rc != nil && rc.Initiator == s.party.SelfGUID && rc.PartyGUID == partyGUID
	})
	if err != nil {
		// The core refuses a second start while one runs and answers with nothing; its held packets must not reach
		// the next check.
		s.party.CancelReadyCheck()
		return nil, err
	}
	return s.party.ReadyCheckOf(partyGUID), nil
}
