package world

// The battleground queue of round R9: the server's list and status answers that change what the client knows at a
// battlemaster. The probe starts from the battlemaster's own list: BattlemasterHello answers SMSG_BATTLEFIELD_LIST
// (BattleGroundHandler.cpp HandleBattlemasterHelloOpcode), and a list with no supported battlemaster type stops the
// round with a report instead of a queue. The queue then joins through the list's queue id, watches the statuses the
// server sends, accepts the invite through the port and leaves through the port with the accept bit clear (the
// core's queue-leave path; an in-match leave is only the explicit `match=yes` opt-in of bg-leave). The world keeps
// the last answer of each kind since the last step cleared them; the steps in bg_act.go read the evidence back.

import (
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bg"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// bgWatch holds the battleground answers since the last step cleared them. The session goroutine is the only reader
// and writer, like the other watches. The statuses are exclusive: the server sends one kind per queue, so the last
// of each kind is kept for the steps to match.
type bgWatch struct {
	// list is the last SMSG_BATTLEFIELD_LIST.
	list *bg.List
	// needConfirm is the last SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION and needConfirmAt when it arrived: the
	// invite's own Timeout counts from then, and bg-accept refuses one that expired.
	needConfirm   *bg.NeedConfirmation
	needConfirmAt time.Time
	// active is the last SMSG_BATTLEFIELD_STATUS_ACTIVE.
	active *bg.Active
	// queued is the last SMSG_BATTLEFIELD_STATUS_QUEUED.
	queued *bg.Queued
	// none is the last SMSG_BATTLEFIELD_STATUS_NONE.
	none *bg.None
	// failed is the last SMSG_BATTLEFIELD_STATUS_FAILED.
	failed *bg.Failed
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
		// The statuses are exclusive per queue: an invite replaces the queue's own queued status.
		s.bg.queued = nil
		s.bg.needConfirm = &st
		s.bg.needConfirmAt = s.transport().Now()
		s.opts.logf("world: the battleground invite is ready on map %d", st.MapID)
	case wire.SMSGBattlefieldStatusActive:
		st, err := bg.ParseActive(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.bg.queued = nil
		s.bg.needConfirm = nil
		s.bg.needConfirmAt = time.Time{}
		s.bg.active = &st
		s.opts.logf("world: inside the battleground on map %d", st.MapID)
	case wire.SMSGBattlefieldStatusQueued:
		st, err := bg.ParseQueued(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.bg.needConfirm = nil
		s.bg.needConfirmAt = time.Time{}
		s.bg.queued = &st
		s.opts.logf("world: queued for the battleground (wait %ds)", st.WaitTime)
	case wire.SMSGBattlefieldStatusNone:
		st, err := bg.ParseNone(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		// The queue-leave path (and an in-match leave) answers none: the character is out of that queue.
		s.bg.queued = nil
		s.bg.needConfirm = nil
		s.bg.needConfirmAt = time.Time{}
		s.bg.active = nil
		s.bg.none = &st
		s.opts.logf("world: out of the battleground queue")
	case wire.SMSGBattlefieldStatusFailed:
		st, err := bg.ParseFailed(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		// A refusal ends the action it answers: no invite and no queued status stand after it.
		s.bg.queued = nil
		s.bg.needConfirm = nil
		s.bg.needConfirmAt = time.Time{}
		s.bg.failed = &st
		s.opts.logf("world: the battleground queue refused: %s", bg.FailName(st.Reason))
	}
	return nil
}
