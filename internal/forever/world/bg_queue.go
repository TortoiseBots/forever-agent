package world

// The battleground queue state of round R9: the server's list and status answers that change what the client knows
// at a battlemaster. A character may sit in several queues at once, and the server's status packets name the queue
// they update: a queued, invite or active status carries the packed queue id in its header
// (BattlegroundMgr.cpp BuildBattlegroundStatusHeader), a failed status carries it beside the ticket, and only NONE
// names the ticket slot alone (BuildBattlegroundStatusNone). The session keeps one status per ticket slot and
// remembers which packed queue id it belongs to, so a status for another queue never selects, accepts or leaves the
// queue the steps asked for.
//
// The watcher's own invite/active fields (bg_session.go) stay what the bg-watch reads; this state is the queue
// steps' evidence, cleared by the step that sends.

import (
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bg"
)

// maxBGQueueStatuses bounds the NONE and FAILED answers kept: the server never has more than
// PLAYER_MAX_BATTLEGROUND_QUEUES (2) queues, so a handful covers two interleaved leaves.
const maxBGQueueStatuses = 8

// bgQueueState is one queue's latest status, keyed by the ticket slot the server named. The kinds are exclusive per
// queue: an invite replaces that queue's queued status, and an active status replaces its invite.
type bgQueueState struct {
	// slot is the ticket slot the status named (Ticket.ID in the core's writers).
	slot uint32
	// queueID is the packed queue id the status's header named (zero when the packet named none, as NONE does).
	queueID       uint64
	queued        *bg.QueuedStatus
	needConfirm   *bg.ConfirmStatus
	needConfirmAt time.Time
	active        *bg.ActiveStatus
}

// bgQueues is the queue state the steps read.
type bgQueues struct {
	// list is the last SMSG_BATTLEFIELD_LIST.
	list *bg.List
	// queues holds the statuses by ticket slot, so two simultaneous queues keep independent state.
	queues map[uint32]*bgQueueState
	// nones are the recent SMSG_BATTLEFIELD_STATUS_NONE answers (they name only the slot), fails the recent FAILED
	// answers (they name the queue id). A step clears them before it sends and matches its own.
	nones []bg.Ticket
	fails []bg.FailedStatus
	// selected is the packed queue id the last `bg-queue` joined, and hasSelected that there is one: bg-accept and
	// bg-leave act only on that queue, never on whatever status arrived last.
	selected    uint64
	hasSelected bool
}

// queue returns the state of the queue with the packed id, or nil. A status whose packet named no queue id (or a
// queue the character is not in) never matches.
func (q *bgQueues) queue(queueID uint64) *bgQueueState {
	if queueID == 0 {
		return nil
	}
	for _, st := range q.queues {
		if st.queueID == queueID {
			return st
		}
	}
	return nil
}

// selectedQueue returns the state of the queue the last bg-queue joined.
func (q *bgQueues) selectedQueue() (*bgQueueState, error) {
	if !q.hasSelected {
		return nil, errNoBGQueue
	}
	st := q.queue(q.selected)
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
func (q *bgQueues) failedFor(queueID uint64) *bg.FailedStatus {
	for n := range q.fails {
		if q.fails[n].Queue == queueID {
			return &q.fails[n]
		}
	}
	return nil
}

// noneFor returns a recent NONE answer for the ticket slot.
func (q *bgQueues) noneFor(slot uint32) *bg.Ticket {
	for n := range q.nones {
		if q.nones[n].ID == slot {
			return &q.nones[n]
		}
	}
	return nil
}

// keepNone appends one NONE answer, dropping the oldest past the bound.
func (q *bgQueues) keepNone(t bg.Ticket) {
	q.nones = append(q.nones, t)
	if len(q.nones) > maxBGQueueStatuses {
		q.nones = q.nones[len(q.nones)-maxBGQueueStatuses:]
	}
}

// keepFail appends one FAILED answer, dropping the oldest past the bound.
func (q *bgQueues) keepFail(f bg.FailedStatus) {
	q.fails = append(q.fails, f)
	if len(q.fails) > maxBGQueueStatuses {
		q.fails = q.fails[len(q.fails)-maxBGQueueStatuses:]
	}
}
