package world

// The battleground steps of round R9: `bg-queue` (probe the battlemaster's list, join, watch the status),
// `bg-accept` (port in on the invite) and `bg-leave` (leave the queue or the match). The probe comes first: when
// the list from a battlemaster names no supported type, the round stops and reports instead of queueing (the
// spec's own rule). Every step reads the server's own words: the list, the queued status, the need-confirmation,
// the port's answer and the none. A queue is left through the core's queue-leave path (CMSG_BATTLEFIELD_PORT with
// the accept bit clear, BattleGroundHandler.cpp HandleBattleFieldPortOpcode); CMSG_BATTLEFIELD_LEAVE only leaves a
// match, and inside one it casts the Deserter debuff (Player.cpp:25691-25706), so bg-leave sends it only when the
// step says `match=yes`.

import (
	"context"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bg"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The battleground steps' bounds: one answer's timeout and how often a wait re-reads the state.
const (
	bgWait = 10 * time.Second
	bgPoll = 50 * time.Millisecond
)

// bgTarget checks the current NPC is a battlemaster within reach and returns it.
func (s *session) bgTarget(a *actState, verb string) (*objects.Object, error) {
	if !a.hasNPC || a.npc == nil {
		return nil, fmt.Errorf("%s: no current NPC (an npc step comes first)", verb)
	}
	now := s.transport().Now()
	nx, ny, nz := a.npc.Position(now)
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), nx, ny, nz); dist > actInteractReach {
		return nil, fmt.Errorf("%s: the battlemaster is %.1f yd away (walk closer first)", verb, dist)
	}
	if !npc.IsBattlemaster(a.npc.Info.NpcFlags) {
		return nil, fmt.Errorf("%s: entry %d is not a battlemaster (NpcFlags %#x has no battlemaster bit)", verb, a.npc.Info.Entry, a.npc.Info.NpcFlags)
	}
	return a.npc, nil
}

// waitBGAnswer polls cond until it is true or wait passes. It returns false when the wait ran out.
func (s *session) waitBGAnswer(ctx context.Context, wait time.Duration, cond func() bool) (bool, error) {
	end := s.transport().Now().Add(wait)
	for {
		if cond() {
			return true, nil
		}
		now := s.transport().Now()
		if !now.Before(end) {
			return false, nil
		}
		step := bgPoll
		if left := end.Sub(now); left < step {
			step = left
		}
		if err := s.transport().WaitUntil(ctx, now.Add(step)); err != nil {
			return false, err
		}
	}
}

// actBGQueue probes the battlemaster's list and queues for the battleground: hello, the list, the support probe,
// the join and the status. A list with no supported battlemaster type stops the round with its report: no support
// (the spec's own rule). A failure status fails the step with the server's reason; the queued status is the proof
// of the queue.
func (s *session) actBGQueue(ctx context.Context, a *actState, step ActStep) error {
	master, err := s.bgTarget(a, step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	s.bg.list = nil
	s.bg.queued = nil
	s.bg.failed = nil
	s.bg.needConfirm = nil
	s.bg.needConfirmAt = time.Time{}
	s.bg.active = nil
	if err := s.transport().Send(wire.CMSGBattlemasterHello, bg.BuildBattlemasterHello(master.GUID)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.waitBGAnswer(ctx, bgWait, func() bool { return s.bg.list != nil })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("bg-queue: the battlemaster sent no list"))
	}
	list := s.bg.list
	// The list id is the battlemaster type the server supports (BattlegroundMgr.cpp SendBattlegroundList); a
	// non-positive one is no support. A value the packed queue id cannot hold is a malformed answer, not a queue.
	if list.ListID <= 0 {
		s.actOK(a, step, "the battlemaster lists no battleground: no support, stopping")
		return nil
	}
	if list.ListID > 0xFFFF {
		return s.actFail(a, step, fmt.Errorf("bg-queue: the battlemaster names list id %d, which cannot be a queue", list.ListID))
	}
	a.bgMaster = master.GUID
	a.hasBGMaster = true
	a.bgList = *list
	queueID := bg.PackQueueID(uint16(list.ListID))
	if err := s.transport().Send(wire.CMSGBattlemasterJoin, bg.BuildJoin(queueID)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err = s.waitBGAnswer(ctx, bgWait, func() bool {
		return s.bg.queued != nil || s.bg.failed != nil || s.bg.needConfirm != nil
	})
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("bg-queue: no queue status after the join"))
	}
	if s.bg.failed != nil {
		return s.actFail(a, step, fmt.Errorf("bg-queue: the queue refused: %s", bg.FailName(s.bg.failed.Reason)))
	}
	if s.bg.needConfirm != nil {
		s.actOK(a, step, fmt.Sprintf("queued for list %d: the invite is ready on map %d", list.ListID, s.bg.needConfirm.MapID))
		return nil
	}
	s.actOK(a, step, fmt.Sprintf("queued for list %d: wait %ds", list.ListID, s.bg.queued.WaitTime))
	return nil
}

// bgTicketForPort builds the port ticket: the invited queue's slot and kind, the character's own GUID (the core
// overwrites RequesterGuid from the session) and the invite's own time. The slot is the status's own: the handler
// points the port at the queue the player really is in (BattleGroundHandler.cpp HandleBattleFieldPortOpcode), so
// the step echoes the ticket the invite carried.
func (s *session) bgTicketForPort(verb string) (bg.Ticket, error) {
	if s.bg.needConfirm == nil {
		return bg.Ticket{}, fmt.Errorf("%s: no invite (a bg-queue with an invite comes first)", verb)
	}
	t := s.bg.needConfirm.Header.Ticket
	t.Requester = s.selfOrMover()
	return t, nil
}

// bgInviteExpired reports an invite the server's own timeout has passed. Timeout is the accept window in
// milliseconds (BattlegroundQueue.cpp:532 sends INVITE_ACCEPT_WAIT_TIME); counting it from the packet's arrival
// keeps a stale invite from being replayed.
func (s *session) bgInviteExpired() bool {
	st := s.bg.needConfirm
	if st == nil || st.Timeout == 0 || s.bg.needConfirmAt.IsZero() {
		return false
	}
	return s.transport().Now().After(s.bg.needConfirmAt.Add(time.Duration(st.Timeout) * time.Millisecond))
}

// actBGAccept ports into the battleground on the invite: the need-confirmation's own ticket with the accept bit.
// The active status is the proof of the port; a failure status fails the step with the server's reason. An invite
// the accept window already passed is refused before any packet, because the server would have removed the queue
// entry and the port would go unanswered.
func (s *session) actBGAccept(ctx context.Context, a *actState, step ActStep) error {
	t, err := s.bgTicketForPort(step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if s.bgInviteExpired() {
		return s.actFail(a, step, fmt.Errorf("%s: the invite expired after %d ms; a new bg-queue is needed", step.Verb, s.bg.needConfirm.Timeout))
	}
	s.bg.active = nil
	s.bg.failed = nil
	if err := s.transport().Send(wire.CMSGBattlefieldPort, bg.BuildPort(t, true)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.waitBGAnswer(ctx, bgWait, func() bool { return s.bg.active != nil || s.bg.failed != nil })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("bg-accept: no battleground status after the port"))
	}
	if s.bg.failed != nil {
		return s.actFail(a, step, fmt.Errorf("bg-accept: the port refused: %s", bg.FailName(s.bg.failed.Reason)))
	}
	// The active status cleared the invite in the watch; drop the step's own copy too, so a later bg-leave takes
	// the match branch and never replays the ticket.
	s.bg.needConfirm = nil
	s.bg.needConfirmAt = time.Time{}
	s.actOK(a, step, fmt.Sprintf("inside the battleground on map %d", s.bg.active.MapID))
	return nil
}

// actBGLeave leaves the queue through the core's queue-leave path: CMSG_BATTLEFIELD_PORT with the ticket of the
// queue the character is in (queued or invited) and the accept bit clear, answered by SMSG_BATTLEFIELD_STATUS_NONE
// (BattleGroundHandler.cpp HandleBattleFieldPortOpcode). Inside a match that path no longer exists, so the step
// sends CMSG_BATTLEFIELD_LEAVE only when it is asked with match=yes; the line warns that a started match casts the
// Deserter debuff (Player.cpp:25691-25706).
func (s *session) actBGLeave(ctx context.Context, a *actState, step ActStep) error {
	if raw, ok := step.Args["match"]; ok && raw != "yes" {
		return s.actFail(a, step, fmt.Errorf("%s: match=%q is not understood (only match=yes leaves a match)", step.Verb, raw))
	}
	if s.bg.active != nil {
		if step.Args["match"] != "yes" {
			return s.actFail(a, step, fmt.Errorf("%s: inside the battleground on map %d; leaving a started match casts the Deserter debuff, so pass match=yes to leave", step.Verb, s.bg.active.MapID))
		}
		s.bg.none = nil
		mapID := s.bg.active.MapID
		if err := s.transport().Send(wire.CMSGBattlefieldLeave, bg.BuildLeave()); err != nil {
			return s.actFail(a, step, err)
		}
		ok, err := s.waitBGAnswer(ctx, bgWait, func() bool { return s.bg.none != nil })
		if err != nil {
			return s.actFail(a, step, err)
		}
		if !ok {
			return s.actFail(a, step, fmt.Errorf("%s: no status after the match leave", step.Verb))
		}
		s.actOK(a, step, fmt.Sprintf("left the battleground on map %d (the Deserter debuff applies while the match runs)", mapID))
		return nil
	}
	t, err := s.bgQueueTicket(step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	s.bg.none = nil
	if err := s.transport().Send(wire.CMSGBattlefieldPort, bg.BuildPort(t, false)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.waitBGAnswer(ctx, bgWait, func() bool { return s.bg.none != nil })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("%s: no status after the queue leave", step.Verb))
	}
	s.actOK(a, step, "out of the battleground queue")
	return nil
}

// bgQueueTicket is the ticket of the queue the character is in: the invite's when one stands, otherwise the queued
// status's own (both carry the queue slot the port's ticket names).
func (s *session) bgQueueTicket(verb string) (bg.Ticket, error) {
	switch {
	case s.bg.needConfirm != nil:
		t := s.bg.needConfirm.Header.Ticket
		t.Requester = s.selfOrMover()
		return t, nil
	case s.bg.queued != nil:
		t := s.bg.queued.Header.Ticket
		t.Requester = s.selfOrMover()
		return t, nil
	}
	return bg.Ticket{}, fmt.Errorf("%s: not in a battleground queue (a bg-queue comes first)", verb)
}
