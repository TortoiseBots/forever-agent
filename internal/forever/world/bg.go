package world

// The battleground queue of round R9: the server's list and status answers that change what the client knows at a
// battlemaster. The probe starts from the battlemaster's own list: BattlemasterHello answers SMSG_BATTLEFIELD_LIST
// (BattleGroundHandler.cpp HandleBattlemasterHelloOpcode), and a list with no supported battlemaster type stops the
// round with a report instead of a queue. The queue then joins through the list's queue id, watches the statuses the
// server sends, accepts the invite through the port and leaves through the port with the accept bit clear (the
// core's queue-leave path; an in-match leave is only the explicit `match=yes` opt-in of bg-leave).
//
// A character may sit in several queues at once, and the server's status packets name the queue they update: a
// queued, invite or active status carries the packed queue id in its header (BattlegroundMgr.cpp
// BuildBattlegroundStatusHeader), a failed status carries it beside the ticket, and only NONE names the ticket slot
// alone (BuildBattlegroundStatusNone). The watch therefore keeps one status per ticket slot and remembers which packed
// queue id a status belongs to, so a status for another queue never selects, accepts or leaves the queue the steps
// asked for. The world keeps the answers since the last step cleared them; the steps in bg_act.go read the evidence
// back.

import (
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bg"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// maxBGStatuses bounds the NONE and FAILED answers kept for one step: the server never has more than
// PLAYER_MAX_BATTLEGROUND_QUEUES (2) queues, so a handful covers two interleaved leaves.
const maxBGStatuses = 8

// bgQueueState is one queue's latest status, keyed by the ticket slot the server named. The kinds are exclusive per
// queue: an invite replaces that queue's queued status, and an active status replaces its invite.
type bgQueueState struct {
	// slot is the ticket slot the status named (Ticket.Id in the core's writers).
	slot uint32
	// queueID is the packed queue id the status's header named (zero when the packet named none, as NONE does).
	queueID       uint64
	queued        *bg.Queued
	needConfirm   *bg.NeedConfirmation
	needConfirmAt time.Time
	active        *bg.Active
}

// bgWatch holds the battleground answers since the last step cleared them. The session goroutine is the only reader
// and writer, like the other watches.
type bgWatch struct {
	// list is the last SMSG_BATTLEFIELD_LIST.
	list *bg.List
	// queues holds the statuses by ticket slot, so two simultaneous queues keep independent state.
	queues map[uint32]*bgQueueState
	// nones are the recent SMSG_BATTLEFIELD_STATUS_NONE answers (they name only the slot), fails the recent FAILED
	// answers (they name the queue id). A step clears them before it sends and matches its own.
	nones []bg.None
	fails []bg.Failed
	// selected is the packed queue id the last `bg-queue` joined, and hasSelected that there is one: bg-accept and
	// bg-leave act only on that queue, never on whatever status arrived last.
	selected    uint64
	hasSelected bool
}

// queue returns the state of the queue with the packed id, or nil. A status whose packet named no queue id (or a
// queue the character is not in) never matches.
func (w *bgWatch) queue(queueID uint64) *bgQueueState {
	if queueID == 0 {
		return nil
	}
	for _, st := range w.queues {
		if st.queueID == queueID {
			return st
		}
	}
	return nil
}

// selectedQueue returns the state of the queue the last bg-queue joined.
func (w *bgWatch) selectedQueue() (*bgQueueState, error) {
	if !w.hasSelected {
		return nil, errNoBGQueue
	}
	st := w.queue(w.selected)
	if st == nil {
		return nil, errNoBGQueue
	}
	return st, nil
}

// queueIDOf returns the first packed queue id a status header named: the core writes one (BuildBattlegroundStatusHeader
// pushes exactly the queue it updates), and an empty list counts as no queue, never as a match.
func queueIDOf(queues []uint64) uint64 {
	if len(queues) == 0 {
		return 0
	}
	return queues[0]
}

// failedFor returns a recent FAILED answer for the queue with the packed id.
func (w *bgWatch) failedFor(queueID uint64) *bg.Failed {
	for n := range w.fails {
		if w.fails[n].QueueID == queueID {
			return &w.fails[n]
		}
	}
	return nil
}

// noneFor returns a recent NONE answer for the ticket slot.
func (w *bgWatch) noneFor(slot uint32) *bg.None {
	for n := range w.nones {
		if w.nones[n].Ticket.ID == slot {
			return &w.nones[n]
		}
	}
	return nil
}

// keepNone appends one NONE answer, dropping the oldest past the bound.
func (w *bgWatch) keepNone(st bg.None) {
	w.nones = append(w.nones, st)
	if len(w.nones) > maxBGStatuses {
		w.nones = w.nones[len(w.nones)-maxBGStatuses:]
	}
}

// keepFail appends one FAILED answer, dropping the oldest past the bound.
func (w *bgWatch) keepFail(st bg.Failed) {
	w.fails = append(w.fails, st)
	if len(w.fails) > maxBGStatuses {
		w.fails = w.fails[len(w.fails)-maxBGStatuses:]
	}
}

// isBGPacket reports the opcodes handleBG owns.
func isBGPacket(core uint32) bool {
	switch core {
	case wire.SMSGBattlefieldList, wire.SMSGBattlefieldStatusNeedConfirmation, wire.SMSGBattlefieldStatusActive,
		wire.SMSGBattlefieldStatusQueued, wire.SMSGBattlefieldStatusNone, wire.SMSGBattlefieldStatusFailed:
		return true
	}
	return false
}

// handleBG applies one battleground answer. A packet this round's parser cannot consume exactly is counted and
// merges nothing.
func (s *session) handleBG(p worldconn.Packet) error {
	s.capturePacket(p)
	if s.bg.queues == nil {
		s.bg.queues = make(map[uint32]*bgQueueState)
	}
	switch p.Core {
	case wire.SMSGBattlefieldList:
		list, err := bg.ParseList(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.bg.list = &list
		s.opts.logf("world: the battlemaster lists %d battlefield(s), levels %d-%d", len(list.Battlefields), list.MinLevel, list.MaxLevel)
	case wire.SMSGBattlefieldStatusNeedConfirmation:
		st, err := bg.ParseNeedConfirmation(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		// The status belongs to the queue its header names; an invite replaces that queue's queued status and leaves
		// every other queue's state alone.
		s.bg.queues[st.Header.Ticket.ID] = &bgQueueState{
			slot:          st.Header.Ticket.ID,
			queueID:       queueIDOf(st.Header.Queues),
			needConfirm:   &st,
			needConfirmAt: s.transport().Now(),
		}
		s.opts.logf("world: the battleground invite is ready on map %d", st.MapID)
	case wire.SMSGBattlefieldStatusActive:
		st, err := bg.ParseActive(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.bg.queues[st.Header.Ticket.ID] = &bgQueueState{
			slot:    st.Header.Ticket.ID,
			queueID: queueIDOf(st.Header.Queues),
			active:  &st,
		}
		s.opts.logf("world: inside the battleground on map %d", st.MapID)
	case wire.SMSGBattlefieldStatusQueued:
		st, err := bg.ParseQueued(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.bg.queues[st.Header.Ticket.ID] = &bgQueueState{
			slot:    st.Header.Ticket.ID,
			queueID: queueIDOf(st.Header.Queues),
			queued:  &st,
		}
		s.opts.logf("world: queued for the battleground (wait %ds)", st.WaitTime)
	case wire.SMSGBattlefieldStatusNone:
		st, err := bg.ParseNone(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		// NONE names only the slot: the queue that occupied it is gone, every other slot stays.
		delete(s.bg.queues, st.Ticket.ID)
		s.bg.keepNone(st)
		s.opts.logf("world: out of the battleground queue")
	case wire.SMSGBattlefieldStatusFailed:
		st, err := bg.ParseFailed(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		// A refusal ends the queue it names, not the other queue the character may still sit in.
		delete(s.bg.queues, st.Ticket.ID)
		s.bg.keepFail(st)
		s.opts.logf("world: the battleground queue refused: %s", bg.FailName(st.Reason))
	}
	return nil
}
