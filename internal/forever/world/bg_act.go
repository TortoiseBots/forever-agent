package world

// The battleground steps of round R9: `bg-queue` (probe the battlemaster's list, join, watch the status),
// `bg-accept` (port in on the invite) and `bg-leave` (leave the queue or the match). The probe comes first: when
// the list from a battlemaster names no supported type, the round stops and reports instead of queueing (the
// spec's own rule). Every step reads the server's own words: the list, the queued status, the need-confirmation,
// the port's answer and the none. A queue is left through the core's queue-leave path (CMSG_BATTLEFIELD_PORT with
// the accept bit clear, BattleGroundHandler.cpp HandleBattleFieldPortOpcode); CMSG_BATTLEFIELD_LEAVE only leaves a
// match, and inside one it casts the Deserter debuff (Player.cpp:25691-25706), so bg-leave sends it only when the
// step says `match=yes`.
//
// Every status is bound to the queue the server named: a queued, invite or active status's header carries the packed
// queue id (BattlegroundMgr.cpp BuildBattlegroundStatusHeader), a failed status carries it beside the ticket, and a
// NONE names only the ticket slot (BuildBattlegroundStatusNone). bg-queue records the packed id it joined in the
// watch's selection, and bg-accept and bg-leave act only on that queue: a status, invite or NONE for another queue
// the character is in never selects, accepts or leaves this one.

import (
	"context"
	"errors"
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
)

// errNoBGQueue is the refusal of bg-accept and bg-leave before a bg-queue selected a queue: there is no ticket to
// echo, and guessing one could remove a queue the caller did not name.
var errNoBGQueue = errors.New("no battleground queue was selected (a bg-queue step comes first)")

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

// actBGQueue probes the battlemaster's list and queues for the battleground: hello, the list, the support probe,
// the join and the status. A list with no supported battlemaster type stops the round with its report: no support
// (the spec's own rule). A failure status for the joined queue fails the step with the server's reason; only a
// queued or invited status that names the joined queue is the proof of the queue.
func (s *session) actBGQueue(ctx context.Context, a *actState, step ActStep) error {
	master, err := s.bgTarget(a, step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	st := &s.bgInit().queues
	st.list = nil
	st.nones = nil
	st.fails = nil
	if err := s.transport().Send(wire.CMSGBattlemasterHello, bg.BuildBattlemasterHello(master.GUID)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.actUntil(ctx, bgWait, func() bool { return st.list != nil })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("bg-queue: the battlemaster sent no list"))
	}
	list := st.list
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
	queueID := bg.PackQueueType(uint16(list.ListID))
	if err := s.transport().Send(wire.CMSGBattlemasterJoin, bg.BuildBattlemasterJoin([]uint64{queueID}, 0, [2]int32{})); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err = s.actUntil(ctx, bgWait, func() bool {
		if st.failedFor(queueID) != nil {
			return true
		}
		got := st.queue(queueID)
		return got != nil && (got.queued != nil || got.needConfirm != nil)
	})
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("bg-queue: no status for queue %#x after the join (another queue's status is not this join's answer)", queueID))
	}
	if failed := st.failedFor(queueID); failed != nil {
		return s.actFail(a, step, fmt.Errorf("bg-queue: the queue refused: %s", bg.FailName(uint32(failed.Reason))))
	}
	// The joined queue is now the one accept and leave act on.
	st.selected = queueID
	st.hasSelected = true
	got := st.queue(queueID)
	if got.needConfirm != nil {
		s.actOK(a, step, fmt.Sprintf("queued for list %d: the invite is ready on map %d", list.ListID, got.needConfirm.Map))
		return nil
	}
	s.actOK(a, step, fmt.Sprintf("queued for list %d: wait %ds", list.ListID, got.queued.Wait))
	return nil
}

// bgInviteExpired reports an invite the server's own timeout has passed. Timeout is the accept window in
// milliseconds (BattlegroundQueue.cpp:532 sends INVITE_ACCEPT_WAIT_TIME); counting it from the packet's arrival
// keeps a stale invite from being replayed.
func (s *session) bgInviteExpired(st *bgQueueState) bool {
	if st == nil || st.needConfirm == nil || st.needConfirm.Timeout == 0 || st.needConfirmAt.IsZero() {
		return false
	}
	return s.transport().Now().After(st.needConfirmAt.Add(time.Duration(st.needConfirm.Timeout) * time.Millisecond))
}

// actBGAccept ports into the battleground on the invite: the selected queue's need-confirmation ticket with the
// accept bit. The active status that names the same queue is the proof of the port; a failure status for that
// queue fails the step with the server's reason. An invite the accept window already passed is refused before any
// packet, because the server would have removed the queue entry and the port would go unanswered.
func (s *session) actBGAccept(ctx context.Context, a *actState, step ActStep) error {
	st := &s.bgInit().queues
	got, err := st.selectedQueue()
	if err != nil {
		return s.actFail(a, step, fmt.Errorf("%s: %w", step.Verb, err))
	}
	if got.needConfirm == nil {
		return s.actFail(a, step, fmt.Errorf("%s: no invite for queue %#x (a bg-queue with an invite comes first)", step.Verb, st.selected))
	}
	if s.bgInviteExpired(got) {
		return s.actFail(a, step, fmt.Errorf("%s: the invite expired after %d ms; a new bg-queue is needed", step.Verb, got.needConfirm.Timeout))
	}
	t := got.needConfirm.Header.Ticket
	t.Requester = s.selfOrMover()
	st.fails = nil
	if err := s.transport().Send(wire.CMSGBattlefieldPort, bg.BuildBattlefieldPort(t, true)); err != nil {
		return s.actFail(a, step, err)
	}
	queueID := st.selected
	ok, err := s.actUntil(ctx, bgWait, func() bool {
		if st.failedFor(queueID) != nil {
			return true
		}
		now := st.queue(queueID)
		return now != nil && now.active != nil
	})
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("bg-accept: no battleground status for queue %#x after the port (another queue's status is not this port's answer)", queueID))
	}
	if failed := st.failedFor(queueID); failed != nil {
		return s.actFail(a, step, fmt.Errorf("bg-accept: the port refused: %s", bg.FailName(uint32(failed.Reason))))
	}
	active := st.queue(queueID).active
	s.actOK(a, step, fmt.Sprintf("inside the battleground on map %d", active.Map))
	return nil
}

// actBGLeave leaves the selected queue through the core's queue-leave path: CMSG_BATTLEFIELD_PORT with the ticket
// of that queue (queued or invited) and the accept bit clear, answered by SMSG_BATTLEFIELD_STATUS_NONE with the
// queue's ticket slot (BattleGroundHandler.cpp HandleBattleFieldPortOpcode). Inside a match that path no longer
// exists, so the step sends CMSG_BATTLEFIELD_LEAVE only when it is asked with match=yes; the line warns that a
// started match casts the Deserter debuff (Player.cpp:25691-25706). A NONE for another queue's slot never ends
// this step, and the watch drops only this queue's state.
func (s *session) actBGLeave(ctx context.Context, a *actState, step ActStep) error {
	if raw, ok := step.Args["match"]; ok && raw != "yes" {
		return s.actFail(a, step, fmt.Errorf("%s: match=%q is not understood (only match=yes leaves a match)", step.Verb, raw))
	}
	st := &s.bgInit().queues
	got, err := st.selectedQueue()
	if err != nil {
		return s.actFail(a, step, fmt.Errorf("%s: %w", step.Verb, err))
	}
	if got.active != nil {
		if step.Args["match"] != "yes" {
			return s.actFail(a, step, fmt.Errorf("%s: inside the battleground on map %d; leaving a started match casts the Deserter debuff, so pass match=yes to leave", step.Verb, got.active.Map))
		}
		slot := got.active.Header.Ticket.ID
		mapID := got.active.Map
		st.nones = nil
		if err := s.transport().Send(wire.CMSGBattlefieldLeave, bg.BuildBattlefieldLeave()); err != nil {
			return s.actFail(a, step, err)
		}
		ok, err := s.actUntil(ctx, bgWait, func() bool { return st.noneFor(slot) != nil })
		if err != nil {
			return s.actFail(a, step, err)
		}
		if !ok {
			return s.actFail(a, step, fmt.Errorf("%s: no status for slot %d after the match leave", step.Verb, slot))
		}
		s.actOK(a, step, fmt.Sprintf("left the battleground on map %d (the Deserter debuff applies while the match runs)", mapID))
		return nil
	}
	var ticket bg.Ticket
	switch {
	case got.needConfirm != nil:
		ticket = got.needConfirm.Header.Ticket
	case got.queued != nil:
		ticket = got.queued.Header.Ticket
	default:
		return s.actFail(a, step, fmt.Errorf("%s: queue %#x has no queued or invited status to leave", step.Verb, st.selected))
	}
	ticket.Requester = s.selfOrMover()
	slot := ticket.ID
	st.nones = nil
	if err := s.transport().Send(wire.CMSGBattlefieldPort, bg.BuildBattlefieldPort(ticket, false)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.actUntil(ctx, bgWait, func() bool { return st.noneFor(slot) != nil })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("%s: no status for slot %d after the queue leave", step.Verb, slot))
	}
	s.actOK(a, step, "out of the battleground queue")
	return nil
}
