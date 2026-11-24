package bg

// The battleground queue of round R9: the battlemaster's list probe and the join the queue steps send. The status
// answers, the port and the leave are the watch's own packets (bg.go); this file adds what the battlemaster hello
// answers and what the join names.

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// BuildBattlemasterHello is CMSG_BATTLEMASTER_HELLO (NPC::Hello::Read, NPCPackets.cpp:94-97): the battlemaster's
// packed GUID. The handler checks UNIT_NPC_FLAG_BATTLEMASTER and answers SMSG_BATTLEFIELD_LIST
// (BattleGroundHandler.cpp HandleBattlemasterHelloOpcode).
func BuildBattlemasterHello(master wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, master)
	return b.Bytes()
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

// maxBattlefields bounds the list's battlefield rows: the list is a battlemaster's small menu (WSG and AB are two
// rows), and the count is a uint32 from the packet.
const maxBattlefields = 64

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
	n := int(b.ReadU32())
	if b.Err() != nil {
		return List{}, fmt.Errorf("bg list head: %w", errShort)
	}
	if n < 0 || n > maxBattlefields {
		return List{}, fmt.Errorf("bg: the list names %d battlefields, the bound is %d", n, maxBattlefields)
	}
	out.Battlefields = make([]int32, 0, n)
	for range n {
		out.Battlefields = append(out.Battlefields, b.ReadI32())
	}
	out.PvpAnywhere = b.ReadBit()
	out.HasRandomWin = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return List{}, fmt.Errorf("bg list tail: %w", errShort)
	}
	if err := exactly(b); err != nil {
		return List{}, err
	}
	return out, nil
}
