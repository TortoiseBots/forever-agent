package bg

// The server's battleground-queue answers, read field for field from the core's writers (BattlegroundPackets.cpp at
// d02645dd21). A packet of this round must be consumed to its last byte: a reader that stops early is an error,
// because the answer would be merged into a model it does not fully describe.

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// maxQueueIDs bounds the queue ids one status header may name. The server names the player's own queues (at most
// PLAYER_MAX_BATTLEGROUND_QUEUES, 2); the bound refuses a hostile count before allocating.
const maxQueueIDs = 8

// maxFields bounds the battlefields one list may name. The core always sends an empty list (BattlegroundMgr.cpp
// SendBattlegroundList: players cannot join a specific instance anymore); the bound refuses a hostile count.
const maxFields = 1024

// errShort is a packet that ended before its layout did.
var errShort = errors.New("bg: the packet ended before its layout")

// exactly requires the packet to have been consumed to its last byte.
func exactly(b *wire.Buffer) error {
	if err := b.Err(); err != nil {
		if errors.Is(err, wire.ErrShort) {
			return errShort
		}
		return fmt.Errorf("bg: %w", err)
	}
	if n := b.Remaining(); n != 0 {
		return fmt.Errorf("bg: %d byte(s) left after the layout", n)
	}
	return nil
}

// count reads a uint32 count and rejects it beyond max.
func count(b *wire.Buffer, what string, max int) (int, error) {
	n := int(b.ReadU32())
	if b.Err() != nil {
		return 0, fmt.Errorf("bg: %s count: %w", what, errShort)
	}
	if n < 0 || n > max {
		return 0, fmt.Errorf("bg: %s count %d over the limit %d", what, n, max)
	}
	return n, nil
}

// Header is BattlefieldStatusHeader (BattlegroundPackets.cpp:177-193): the ticket, the queue ids, the level range,
// the team size, the instance and two bits flushed.
type Header struct {
	Ticket          Ticket
	Queues          []uint64
	RangeMin        uint8
	RangeMax        uint8
	TeamSize        uint8
	InstanceID      uint32
	RegisteredMatch bool
	TournamentRules bool
}

func readHeader(b *wire.Buffer) (Header, error) {
	var h Header
	t, err := readTicket(b)
	if err != nil {
		return h, fmt.Errorf("bg header ticket: %w", errShort)
	}
	h.Ticket = t
	n, err := count(b, "bg header queue", maxQueueIDs)
	if err != nil {
		return h, err
	}
	h.RangeMin = b.ReadU8()
	h.RangeMax = b.ReadU8()
	h.TeamSize = b.ReadU8()
	h.InstanceID = b.ReadU32()
	h.Queues = make([]uint64, 0, n)
	for range n {
		h.Queues = append(h.Queues, b.ReadU64())
	}
	h.RegisteredMatch = b.ReadBit()
	h.TournamentRules = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return h, fmt.Errorf("bg header tail: %w", errShort)
	}
	return h, nil
}

// None is SMSG_BATTLEFIELD_STATUS_NONE (BattlefieldStatusNone::Write, BattlegroundPackets.cpp:195-200): the ticket
// alone. The port-leave answers one per queue it removed the player from.
type None struct {
	Ticket Ticket
}

// ParseNone parses one SMSG_BATTLEFIELD_STATUS_NONE and requires it to be consumed exactly.
func ParseNone(payload []byte) (None, error) {
	b := wire.NewReader(payload)
	var out None
	t, err := readTicket(b)
	if err != nil {
		return out, fmt.Errorf("bg none ticket: %w", errShort)
	}
	out.Ticket = t
	if err := exactly(b); err != nil {
		return out, err
	}
	return out, nil
}

// NeedConfirmation is SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION (BattlegroundPackets.cpp:202-210): the header, the
// map, the timeout and the role.
type NeedConfirmation struct {
	Header  Header
	MapID   uint32
	Timeout uint32
	Role    uint8
}

// ParseNeedConfirmation parses one SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION and requires it to be consumed exactly.
// The core writes the header first, then the map, the timeout and the role (BattlegroundPackets.cpp:202-210).
func ParseNeedConfirmation(payload []byte) (NeedConfirmation, error) {
	b := wire.NewReader(payload)
	var out NeedConfirmation
	h, err := readHeader(b)
	if err != nil {
		return out, err
	}
	out.Header = h
	out.MapID = b.ReadU32()
	out.Timeout = b.ReadU32()
	out.Role = b.ReadU8()
	if b.Err() != nil {
		return out, fmt.Errorf("bg need-confirmation role: %w", errShort)
	}
	if err := exactly(b); err != nil {
		return out, err
	}
	return out, nil
}

// Active is SMSG_BATTLEFIELD_STATUS_ACTIVE (BattlegroundPackets.cpp:212-224): the header, the map, the timers, the
// arena faction and two bits flushed.
type Active struct {
	Header       Header
	MapID        uint32
	Shutdown     uint32
	StartTimer   uint32
	ArenaFaction int8
	LeftEarly    bool
	Brawl        bool
}

// ParseActive parses one SMSG_BATTLEFIELD_STATUS_ACTIVE and requires it to be consumed exactly.
func ParseActive(payload []byte) (Active, error) {
	b := wire.NewReader(payload)
	var out Active
	h, err := readHeader(b)
	if err != nil {
		return out, err
	}
	out.Header = h
	out.MapID = b.ReadU32()
	out.Shutdown = b.ReadU32()
	out.StartTimer = b.ReadU32()
	out.ArenaFaction = b.ReadI8()
	out.LeftEarly = b.ReadBit()
	out.Brawl = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return out, fmt.Errorf("bg active tail: %w", errShort)
	}
	if err := exactly(b); err != nil {
		return out, err
	}
	return out, nil
}

// Queued is SMSG_BATTLEFIELD_STATUS_QUEUED (BattlegroundPackets.cpp:226-238): the header, the average wait, the
// wait, the spec and three bits flushed.
type Queued struct {
	Header      Header
	AverageWait uint32
	WaitTime    uint32
	Spec        int32
	AsGroup     bool
	Suspended   bool
	Eligible    bool
}

// ParseQueued parses one SMSG_BATTLEFIELD_STATUS_QUEUED and requires it to be consumed exactly. The core writes the
// header first, then the average wait, the wait, the spec and the three bits (BattlegroundPackets.cpp:226-238).
func ParseQueued(payload []byte) (Queued, error) {
	b := wire.NewReader(payload)
	var out Queued
	h, err := readHeader(b)
	if err != nil {
		return out, err
	}
	out.Header = h
	out.AverageWait = b.ReadU32()
	out.WaitTime = b.ReadU32()
	out.Spec = b.ReadI32()
	out.AsGroup = b.ReadBit()
	out.Eligible = b.ReadBit()
	out.Suspended = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return out, fmt.Errorf("bg queued tail: %w", errShort)
	}
	if err := exactly(b); err != nil {
		return out, err
	}
	return out, nil
}

// Failed is SMSG_BATTLEFIELD_STATUS_FAILED (BattlegroundPackets.cpp:240-248): the ticket, the queue id, the reason
// and the client id. The reason is a GroupJoinBattlegroundResult (SharedDefines.h at d02645dd21).
type Failed struct {
	Ticket   Ticket
	QueueID  uint64
	Reason   uint32
	ClientID wire.GUID
}

// ParseFailed parses one SMSG_BATTLEFIELD_STATUS_FAILED and requires it to be consumed exactly.
func ParseFailed(payload []byte) (Failed, error) {
	b := wire.NewReader(payload)
	var out Failed
	t, err := readTicket(b)
	if err != nil {
		return out, fmt.Errorf("bg failed ticket: %w", errShort)
	}
	out.Ticket = t
	out.QueueID = b.ReadU64()
	out.Reason = b.ReadU32()
	out.ClientID = wire.ReadPackedGUID(b)
	if b.Err() != nil {
		return out, fmt.Errorf("bg failed tail: %w", errShort)
	}
	if err := exactly(b); err != nil {
		return out, err
	}
	return out, nil
}

// FailReasons names the GroupJoinBattlegroundResult values (SharedDefines.h at d02645dd21) the steps print.
var failReasons = map[uint32]string{
	0:  "none",
	2:  "deserter",
	4:  "too many queues",
	5:  "cannot queue for rated",
	6:  "queued for rated",
	7:  "team left queue",
	8:  "not in battleground",
	9:  "xp gain",
	10: "range index",
	11: "timed out",
	14: "cannot queue while using lfg",
	15: "in random bg",
	16: "in another queue",
	17: "developer only",
	22: "party size",
	33: "join levelup",
	35: "join failed",
	43: "dupe queue",
	47: "missing quest",
}

// FailName names one queue refusal for a report line; an unknown value is its number.
func FailName(v uint32) string {
	if n, ok := failReasons[v]; ok {
		return n
	}
	return fmt.Sprintf("reason %d", v)
}

// List is SMSG_BATTLEFIELD_LIST (BattlefieldList::Write, BattlegroundPackets.cpp:261-275): the master, the list
// id, the level range, the (always empty) battlefields and two bits flushed. The core sends the list only for a
// battlemaster whose type it supports (BattlegroundMgr.cpp SendBattlegroundList); the probe treats a non-positive
// list id as no support (the round's own rule).
type List struct {
	Master       wire.GUID
	ListID       int32
	MinLevel     uint8
	MaxLevel     uint8
	Battlefields []int32
	PvpAnywhere  bool
	HasRandomWin bool
}

// ParseList parses one SMSG_BATTLEFIELD_LIST and requires it to be consumed exactly.
func ParseList(payload []byte) (List, error) {
	b := wire.NewReader(payload)
	var out List
	out.Master = wire.ReadPackedGUID(b)
	out.ListID = b.ReadI32()
	out.MinLevel = b.ReadU8()
	out.MaxLevel = b.ReadU8()
	n, err := count(b, "bg list battlefield", maxFields)
	if err != nil {
		return out, err
	}
	out.Battlefields = make([]int32, 0, n)
	for range n {
		out.Battlefields = append(out.Battlefields, b.ReadI32())
	}
	out.PvpAnywhere = b.ReadBit()
	out.HasRandomWin = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return out, fmt.Errorf("bg list tail: %w", errShort)
	}
	if err := exactly(b); err != nil {
		return out, err
	}
	return out, nil
}
