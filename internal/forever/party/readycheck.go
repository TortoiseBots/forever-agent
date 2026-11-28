package party

// The ready check (#288): the group leader's CMSG_DO_READY_CHECK and the three server packets it produces. The core
// reads the optional party index off the start packet (PartyPackets.cpp:416-421) and starts the check only for the
// leader or an assistant (GroupHandler.cpp:584-605); every member's answer is broadcast by Group::SetMemberReadyCheck
// (Group.cpp:1522-1541), and the core ends the check itself when the last member has answered or its 35 s timer runs
// out (Group.cpp:1462-1511).

import (
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// BuildDoReadyCheck is CMSG_DO_READY_CHECK. The optional party index is sent present, as this client's other
// optional-index packets do (see BuildInvite and BuildSetLootMethod); the caller passes the roster's index, which is
// GROUP_CATEGORY_HOME (0) or GROUP_CATEGORY_INSTANCE (1), so a check in an instance group names that group
// (Player::GetGroup(partyIndex), Player.cpp:26387-26402).
func BuildDoReadyCheck(index uint8) []byte {
	b := wire.NewWriter()
	b.WriteBit(true) // the party index follows
	b.FlushBits()
	b.WriteU8(index)
	return b.Bytes()
}

// ReadyCheckStarted is SMSG_READY_CHECK_STARTED (PartyPackets.cpp:423-431): the index, the party GUID, the member
// who started it and how long the check runs.
type ReadyCheckStarted struct {
	Index     int8
	PartyGUID wire.GUID
	Initiator wire.GUID
	Duration  time.Duration
}

// ParseReadyCheckStarted decodes SMSG_READY_CHECK_STARTED. The duration is the int64 milliseconds Duration writes
// (PacketUtilities.h:340-344).
func ParseReadyCheckStarted(p []byte) (ReadyCheckStarted, error) {
	b := wire.NewReader(p)
	var r ReadyCheckStarted
	r.Index = int8(b.ReadU8())
	r.PartyGUID = wire.ReadPackedGUID(b)
	r.Initiator = wire.ReadPackedGUID(b)
	r.Duration = time.Duration(b.ReadU64()) * time.Millisecond
	if err := fail("SMSG_READY_CHECK_STARTED", b); err != nil {
		return r, err
	}
	if b.Remaining() != 0 {
		return r, fmt.Errorf("party: SMSG_READY_CHECK_STARTED: %d trailing byte(s)", b.Remaining())
	}
	return r, nil
}

// ReadyCheckResponse is SMSG_READY_CHECK_RESPONSE (PartyPackets.cpp:441-450): the party GUID, the member and whether
// that member is ready.
type ReadyCheckResponse struct {
	PartyGUID wire.GUID
	Player    wire.GUID
	Ready     bool
}

// ParseReadyCheckResponse decodes SMSG_READY_CHECK_RESPONSE. The ready flag is one bit and is flushed before the
// payload's end.
func ParseReadyCheckResponse(p []byte) (ReadyCheckResponse, error) {
	b := wire.NewReader(p)
	var r ReadyCheckResponse
	r.PartyGUID = wire.ReadPackedGUID(b)
	r.Player = wire.ReadPackedGUID(b)
	r.Ready = b.ReadBit()
	b.FlushBits()
	if err := fail("SMSG_READY_CHECK_RESPONSE", b); err != nil {
		return r, err
	}
	if b.Remaining() != 0 {
		return r, fmt.Errorf("party: SMSG_READY_CHECK_RESPONSE: %d trailing byte(s)", b.Remaining())
	}
	return r, nil
}

// ReadyCheckCompleted is SMSG_READY_CHECK_COMPLETED (PartyPackets.cpp:452-460): the index and the party GUID.
type ReadyCheckCompleted struct {
	Index     int8
	PartyGUID wire.GUID
}

// ParseReadyCheckCompleted decodes SMSG_READY_CHECK_COMPLETED.
func ParseReadyCheckCompleted(p []byte) (ReadyCheckCompleted, error) {
	b := wire.NewReader(p)
	var r ReadyCheckCompleted
	r.Index = int8(b.ReadU8())
	r.PartyGUID = wire.ReadPackedGUID(b)
	if err := fail("SMSG_READY_CHECK_COMPLETED", b); err != nil {
		return r, err
	}
	if b.Remaining() != 0 {
		return r, fmt.Errorf("party: SMSG_READY_CHECK_COMPLETED: %d trailing byte(s)", b.Remaining())
	}
	return r, nil
}
