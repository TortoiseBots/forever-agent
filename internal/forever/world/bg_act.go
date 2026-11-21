package world

// The battleground steps of round R9: `bg-queue` (probe the battlemaster's list, join, watch the status),
// `bg-accept` (port in on the invite) and `bg-leave` (leave the queue or the match). The probe comes first: when
// the list from a battlemaster is empty, the round stops and reports instead of queueing (the spec's own rule).
// Every step reads the server's own words: the list, the queued status, the need-confirmation, the port's answer
// and the leave's none.

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
// the join and the status. An empty list stops the round with its report: no support (the spec's own rule). A
// failure status fails the step with the server's reason; the queued status is the proof of the queue.
func (s *session) actBGQueue(ctx context.Context, a *actState, step ActStep) error {
	master, err := s.bgTarget(a, step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	s.bg.list = nil
	s.bg.queued = nil
	s.bg.failed = nil
	s.bg.needConfirm = nil
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
	if len(list.Battlefields) == 0 && list.MinLevel == 0 && list.MaxLevel == 0 && list.ListID == 0 {
		s.actOK(a, step, "the battlemaster lists no battleground: no support, stopping")
		return nil
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

// bgTicketForPort builds the port ticket: the character's own GUID, the invited queue's slot and kind, and the
// current time. The slot is the status's own: the handler points the port at the queue the player really is in
// (BattleGroundHandler.cpp HandleBattleFieldPortOpcode), so the step echoes the ticket the invite carried.
func (s *session) bgTicketForPort() (bg.Ticket, error) {
	if s.bg.needConfirm == nil {
		return bg.Ticket{}, fmt.Errorf("bg-accept: no invite (a bg-queue with an invite comes first)")
	}
	t := s.bg.needConfirm.Header.Ticket
	t.Requester = s.selfOrMover()
	return t, nil
}

// actBGAccept ports into the battleground on the invite: the need-confirmation's own ticket with the accept bit.
// The active status is the proof of the port; a failure status fails the step with the server's reason.
func (s *session) actBGAccept(ctx context.Context, a *actState, step ActStep) error {
	t, err := s.bgTicketForPort()
	if err != nil {
		return s.actFail(a, step, err)
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
	s.actOK(a, step, fmt.Sprintf("inside the battleground on map %d", s.bg.active.MapID))
	return nil
}

// actBGLeave leaves the queue or the match: the explicit leave, with the none status as the proof. Declining an
// invite ports with the ticket's slot and a clear accept bit (BattleGroundHandler.cpp HandleBattleFieldPortOpcode
// leaves every queue the player really is in); inside or queued with no invite, the explicit leave is the packet.
func (s *session) actBGLeave(ctx context.Context, a *actState, step ActStep) error {
	if s.bg.needConfirm != nil {
		t, err := s.bgTicketForPort()
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
			return s.actFail(a, step, fmt.Errorf("bg-leave: no status after the decline"))
		}
		s.actOK(a, step, "the invite declined: out of the queue")
		return nil
	}
	s.bg.none = nil
	if err := s.transport().Send(wire.CMSGBattlefieldLeave, bg.BuildLeave()); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.waitBGAnswer(ctx, bgWait, func() bool { return s.bg.none != nil })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("bg-leave: no status after the leave"))
	}
	s.actOK(a, step, "out of the battleground queue")
	return nil
}
