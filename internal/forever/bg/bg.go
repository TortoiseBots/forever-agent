// Package bg is the battleground wire layer of the Forever (Classic 1.60.1) client:
// the queue, port, leave, status, world-state, score and spirit-healer packets
// the bg-watch command sends and reads.
//
// Every layout is the core's own writer or reader at the pinned commit
// (d02645dd21); the file:line is cited at each field. The queue joining is
// CMSG_BATTLEMASTER_JOIN (BattleGroundHandler.cpp:69-78) through the typed
// BattlegroundQueueTypeId packing
// (SharedDefines.h:7200-7222): bits 0-15 the battlemaster list id, 16-19 the
// type, 20 the rated flag, 24-29 the team size, ORed with 0x1F10000000000000.
// The status answers and the port/leave/leak packets are BattlegroundPackets.h
// with their .cpp writers; the world states are WorldStatePackets.h/.cpp; the
// spirit queue is Handler BattleGroundHandler.cpp:847-860.
package bg

import (
	"errors"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Battleground battlemaster list ids for the queue type (BattlemasterList.db2;
// the join handler takes the packed id's list field as it comes,
// BattleGroundHandler.cpp:69-78).
const (
	// ListWSG is BattlemasterList id 2 ("Warsong Gulch - Classic").
	ListWSG = 2
	// ListAB is BattlemasterList id 3 ("Arathi Basin - Classic").
	ListAB = 3
)

// Maps for the two battlegrounds this round watches.
const (
	// MapWSG is Warsong Gulch (ClassicWarsongGulch::MapId,
	// battleground_warsong_gulch.cpp:616).
	MapWSG = 489
	// MapAB is Arathi Basin.
	MapAB = 529
)

// queueTypeShift is the fixed high half of a packed BattlegroundQueueTypeId
// (SharedDefines.h:7214-7221).
const queueTypeShift = uint64(0x1F10000000000000)

// PackQueueType builds the uint64 of BattlegroundQueueTypeId for battlemaster
// list id listID (BattleGroundHandler.cpp:77 reads QueueIDs[0] through it).
// WSG/AB joins are unrated with team size 0: the type field's TeamSize is the
// arena size and stays 0 for a battleground (BuildBattlegroundStatusHeader,
// BattlegroundMgr.cpp:174-187, writes queueId.TeamSize into the header).
func PackQueueType(listID uint16) uint64 {
	return PackQueueTypeFull(listID, 0, false, 0)
}

// PackQueueTypeFull builds the packed id with every field
// (SharedDefines.h:7214-7221).
func PackQueueTypeFull(listID uint16, queueType uint8, rated bool, teamSize uint8) uint64 {
	out := uint64(listID) | (uint64(queueType&0xF) << 16) | (uint64(teamSize&0x3F) << 24) | queueTypeShift
	if rated {
		out |= uint64(1) << 20
	}
	return out
}

// QueueType decodes a packed BattlegroundQueueTypeId
// (BattlegroundQueueTypeId::FromPacked, SharedDefines.h:7207-7212).
type QueueType struct {
	ListID   uint16
	Type     uint8
	Rated    bool
	TeamSize uint8
}

// UnpackQueueType decodes packed (SharedDefines.h:7207-7212).
func UnpackQueueType(packed uint64) QueueType {
	return QueueType{
		ListID:   uint16(packed & 0xFFFF),
		Type:     uint8((packed >> 16) & 0xF),
		Rated:    ((packed >> 20) & 1) != 0,
		TeamSize: uint8((packed >> 24) & 0x3F),
	}
}

// ListIDForMap names the battlemaster list of a map id.
func ListIDForMap(mapID uint32) (uint16, bool) {
	switch mapID {
	case MapWSG:
		return ListWSG, true
	case MapAB:
		return ListAB, true
	}
	return 0, false
}

// errShort is a packet that ended before its layout did.
var errShort = errors.New("bg: the packet ended before its layout")

// exactly requires the packet to have been consumed to its last byte. A parser
// that leaves bytes behind cannot be trusted with the fields it did read (the
// fixture rule), so it returns an error and the caller merges nothing.
func exactly(b *wire.Buffer) error {
	if b.Err() != nil {
		return errShort
	}
	if b.Remaining() != 0 {
		return errors.New("bg: the packet has trailing bytes past its layout")
	}
	return nil
}

// guid reads one packed GUID (ObjectGuid.cpp:1113-1134 through
// wire.ReadPackedGUID).
func guid(b *wire.Buffer) wire.GUID { return wire.ReadPackedGUID(b) }

// flush ends a bit group the way the server's FlushBits does; on a reader that
// discards the rest of the byte.
func flush(b *wire.Buffer) { b.FlushBits() }

// Ticket is the queued ticket of a battleground status header
// (RideTicket, LFGPacketsCommon.h:31-47, LFGPacketsCommon.cpp:23-47): the
// requester's packed GUID, the queue slot id, the ride type, the join time
// (unix seconds, Player::AddBattlegroundQueueId) and the cross-faction bit.
type Ticket struct {
	Requester wire.GUID
	ID        uint32
	Type      uint32
	Time      int64
	Cross     bool
}

// writeTicket writes a RideTicket (LFGPacketsCommon.cpp:35-47).
func writeTicket(b *wire.Buffer, t Ticket) {
	wire.WritePackedGUID(b, t.Requester)
	b.WriteU32(t.ID)
	b.WriteU32(t.Type)
	b.WriteU64(uint64(t.Time))
	b.WriteBit(t.Cross)
	b.FlushBits()
}

// readTicket reads a RideTicket (LFGPacketsCommon.cpp:23-33).
func readTicket(b *wire.Buffer) Ticket {
	var t Ticket
	t.Requester = guid(b)
	t.ID = b.ReadU32()
	t.Type = b.ReadU32()
	t.Time = int64(b.ReadU64())
	t.Cross = b.ReadBit()
	flush(b)
	return t
}

// StatusHeader is the status packets' shared header
// (BattlefieldStatusHeader, BattlegroundPackets.h:167-177,
// BattlegroundPackets.cpp:177-193): the ticket, the packed queue ids, the
// range/team-size bytes, the instance id, then the two presence bits.
type StatusHeader struct {
	Ticket     Ticket
	QueueIDs   []uint64
	RangeMin   uint8
	RangeMax   uint8
	TeamSize   uint8
	InstanceID uint32
	Registered bool
	Tournament bool
}

// readHeader reads a BattlefieldStatusHeader (BattlegroundPackets.cpp:177-193).
// The queue-id count is server data: a count past MaxQueueIDs (or one longer
// than the bytes left) is refused before the list is built.
func readHeader(b *wire.Buffer) (StatusHeader, error) {
	var h StatusHeader
	h.Ticket = readTicket(b)
	n := b.ReadU32()
	if n > MaxQueueIDs {
		return StatusHeader{}, errors.New("bg: too many queued queue ids")
	}
	h.RangeMin = b.ReadU8()
	h.RangeMax = b.ReadU8()
	h.TeamSize = b.ReadU8()
	h.InstanceID = b.ReadU32()
	if b.Err() != nil {
		return StatusHeader{}, errShort
	}
	if b.Remaining() < int(n)*8 {
		return StatusHeader{}, errShort
	}
	for range n {
		h.QueueIDs = append(h.QueueIDs, b.ReadU64())
	}
	h.Registered = b.ReadBit()
	h.Tournament = b.ReadBit()
	flush(b)
	if b.Err() != nil {
		return StatusHeader{}, errShort
	}
	return h, nil
}

// writeHeader writes a BattlefieldStatusHeader for hand-built fixtures
// (BattlegroundPackets.cpp:177-193).
func writeHeader(b *wire.Buffer, h StatusHeader) {
	writeTicket(b, h.Ticket)
	b.WriteU32(uint32(len(h.QueueIDs)))
	b.WriteU8(h.RangeMin)
	b.WriteU8(h.RangeMax)
	b.WriteU8(h.TeamSize)
	b.WriteU32(h.InstanceID)
	for _, q := range h.QueueIDs {
		b.WriteU64(q)
	}
	b.WriteBit(h.Registered)
	b.WriteBit(h.Tournament)
	b.FlushBits()
}

// MaxQueueIDs bounds the status header's packed queue-id list: a real header
// carries one queue (BuildBattlegroundStatusHeader, BattlegroundMgr.cpp:180).
// A count beyond it is server data refused before it can allocate.
const MaxQueueIDs = 8

// joinHeader is the shared header for the fixture-built status packets.
func joinHeader() StatusHeader {
	return StatusHeader{
		Ticket:     Ticket{Time: 1700000000},
		QueueIDs:   []uint64{PackQueueType(ListWSG)},
		RangeMin:   0,
		TeamSize:   0,
		InstanceID: 0,
	}
}

// StatusKind names which status packet arrived.
type StatusKind int

const (
	// StatusQueued is SMSG_BATTLEFIELD_STATUS_QUEUED: still waiting.
	StatusQueued StatusKind = iota + 1
	// StatusNeedConfirmation is SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION:
	// invited, accept before Timeout ms run out.
	StatusNeedConfirmation
	// StatusActive is SMSG_BATTLEFIELD_STATUS_ACTIVE: inside.
	StatusActive
	// StatusNone is SMSG_BATTLEFIELD_STATUS_NONE: not queued.
	StatusNone
	// StatusFailed is SMSG_BATTLEFIELD_STATUS_FAILED: the join failed.
	StatusFailed
)

// String names the status kind.
func (k StatusKind) String() string {
	switch k {
	case StatusQueued:
		return "queued"
	case StatusNeedConfirmation:
		return "need-confirmation"
	case StatusActive:
		return "active"
	case StatusNone:
		return "none"
	case StatusFailed:
		return "failed"
	}
	return "unknown"
}

// Status is one decoded battlefield-status packet.
type Status struct {
	Kind StatusKind
	// Queue is set for queued: the average and current wait.
	Queue QueuedStatus
	// Confirm is set for need-confirmation: the map and the accept timeout.
	Confirm ConfirmStatus
	// Active is set for active: the map, the shutdown/elapsed timers.
	Active ActiveStatus
	// Fail is set for failed: the reason code.
	Fail FailedStatus
}

// QueuedStatus is SMSG_BATTLEFIELD_STATUS_QUEUED
// (BattlegroundPackets.h:218-232, BattlegroundPackets.cpp:226-238).
type QueuedStatus struct {
	Header    StatusHeader
	AvgWait   uint32
	Wait      uint32
	Spec      int32
	AsGroup   bool
	Suspended bool
	Eligible  bool
}

// ConfirmStatus is SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION
// (BattlegroundPackets.h:189-200, BattlegroundPackets.cpp:202-210).
type ConfirmStatus struct {
	Header StatusHeader
	Map    uint32
	// Timeout is the accept window in milliseconds as the packet carries it:
	// INVITE_ACCEPT_WAIT_TIME (Battleground.h:136) on the first invite, and
	// the time left on a status answer (BattleGroundHandler.cpp:626).
	Timeout uint32
	Role    uint8
	// Expires is when the accept window closes, filled by the session when
	// the packet arrives (arrival plus Timeout). It is not on the wire; the
	// zero time means only Timeout is known.
	Expires time.Time
}

// ActiveStatus is SMSG_BATTLEFIELD_STATUS_ACTIVE
// (BattlegroundPackets.h:202-216, BattlegroundPackets.cpp:212-224).
type ActiveStatus struct {
	Header   StatusHeader
	Shutdown uint32
	Faction  int8
	Left     bool
	Brawl    bool
	Start    uint32
	Map      uint32
}

// FailedStatus is SMSG_BATTLEFIELD_STATUS_FAILED
// (BattlegroundPackets.h:234-245, BattlegroundPackets.cpp:240-248): the
// queue id, the reason, then the client id.
type FailedStatus struct {
	Queue  uint64
	Client wire.GUID
	Reason int32
	Ticket Ticket
}

// ParseStatusQueued reads SMSG_BATTLEFIELD_STATUS_QUEUED to its last byte
// (BattlegroundPackets.cpp:226-238).
func ParseStatusQueued(p []byte) (QueuedStatus, error) {
	b := wire.NewReader(p)
	var q QueuedStatus
	h, err := readHeader(b)
	if err != nil {
		return QueuedStatus{}, err
	}
	q.Header = h
	q.AvgWait = b.ReadU32()
	q.Wait = b.ReadU32()
	q.Spec = b.ReadI32()
	q.AsGroup = b.ReadBit()
	q.Eligible = b.ReadBit()
	q.Suspended = b.ReadBit()
	flush(b)
	if err := exactly(b); err != nil {
		return QueuedStatus{}, err
	}
	return q, nil
}

// ParseStatusNeedConfirmation reads SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION
// to its last byte (BattlegroundPackets.cpp:202-210).
func ParseStatusNeedConfirmation(p []byte) (ConfirmStatus, error) {
	b := wire.NewReader(p)
	var c ConfirmStatus
	h, err := readHeader(b)
	if err != nil {
		return ConfirmStatus{}, err
	}
	c.Header = h
	c.Map = b.ReadU32()
	c.Timeout = b.ReadU32()
	c.Role = b.ReadU8()
	if err := exactly(b); err != nil {
		return ConfirmStatus{}, err
	}
	return c, nil
}

// ParseStatusActive reads SMSG_BATTLEFIELD_STATUS_ACTIVE to its last byte
// (BattlegroundPackets.cpp:212-224).
func ParseStatusActive(p []byte) (ActiveStatus, error) {
	b := wire.NewReader(p)
	var a ActiveStatus
	h, err := readHeader(b)
	if err != nil {
		return ActiveStatus{}, err
	}
	a.Header = h
	a.Map = b.ReadU32()
	a.Shutdown = b.ReadU32()
	a.Start = b.ReadU32()
	a.Faction = b.ReadI8()
	a.Left = b.ReadBit()
	a.Brawl = b.ReadBit()
	flush(b)
	if err := exactly(b); err != nil {
		return ActiveStatus{}, err
	}
	return a, nil
}

// ParseStatusNone reads SMSG_BATTLEFIELD_STATUS_NONE to its last byte
// (BattlegroundPackets.cpp:195-200): the ticket alone.
func ParseStatusNone(p []byte) (Ticket, error) {
	b := wire.NewReader(p)
	t := readTicket(b)
	if err := exactly(b); err != nil {
		return Ticket{}, err
	}
	return t, nil
}

// ParseStatusFailed reads SMSG_BATTLEFIELD_STATUS_FAILED to its last byte
// (BattlegroundPackets.cpp:240-248).
func ParseStatusFailed(p []byte) (FailedStatus, error) {
	b := wire.NewReader(p)
	var f FailedStatus
	f.Ticket = readTicket(b)
	f.Queue = b.ReadU64()
	f.Reason = b.ReadI32()
	f.Client = guid(b)
	if err := exactly(b); err != nil {
		return FailedStatus{}, err
	}
	return f, nil
}

// BuildBattlemasterJoin is CMSG_BATTLEMASTER_JOIN: the queued packed ids, the
// roles byte and the two blacklist map ids, then the queue ids again
// (BattlemasterJoin::Read, BattlegroundPackets.cpp:159-168).
func BuildBattlemasterJoin(queueIDs []uint64, roles uint8, blacklist [2]int32) []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(queueIDs)))
	b.WriteU8(roles)
	b.WriteI32(blacklist[0])
	b.WriteI32(blacklist[1])
	for _, q := range queueIDs {
		b.WriteU64(q)
	}
	return b.Bytes()
}

// BuildBattlefieldLeave is CMSG_BATTLEFIELD_LEAVE (BattlefieldLeave::Read,
// BattlegroundPackets.h:270-276): an empty packet.
func BuildBattlefieldLeave() []byte { return []byte{} }

// BuildBattlefieldPort is CMSG_BATTLEFIELD_PORT: the ticket then the accept
// bit (BattlefieldPort::Read, BattlegroundPackets.cpp:250-254).
func BuildBattlefieldPort(t Ticket, accept bool) []byte {
	b := wire.NewWriter()
	writeTicket(b, t)
	b.WriteBit(accept)
	b.FlushBits()
	return b.Bytes()
}

// BuildRequestBattlefieldStatus is CMSG_REQUEST_BATTLEFIELD_STATUS: an empty
// packet (RequestBattlefieldStatus, BattlegroundPackets.h:342-348).
func BuildRequestBattlefieldStatus() []byte { return []byte{} }

// SpiritTime is SMSG_AREA_SPIRIT_HEALER_TIME (AreaSpiritHealerTime::Write,
// BattlegroundPackets.cpp:73-81): the healer and the ms left.
type SpiritTime struct {
	Healer wire.GUID
	Left   int32
}

// ParseSpiritTime reads SMSG_AREA_SPIRIT_HEALER_TIME to its last byte
// (BattlegroundPackets.cpp:73-81).
func ParseSpiritTime(p []byte) (SpiritTime, error) {
	b := wire.NewReader(p)
	var s SpiritTime
	s.Healer = guid(b)
	s.Left = b.ReadI32()
	if err := exactly(b); err != nil {
		return SpiritTime{}, err
	}
	return s, nil
}

// WorldStates is SMSG_INIT_WORLD_STATES (InitWorldStates::Write,
// WorldStatePackets.cpp:23-40): the map, area and sub-area, then the
// id/value pairs.
type WorldStates struct {
	Map    int32
	Area   int32
	Sub    int32
	States map[int32]int32
}

// MaxWorldStates bounds an init packet's id list: a BG init carries its map's
// dozen ids (WSG 1581/1582/1545/1546/2338/2339, AB 1776/1777/plus nodes). A
// count beyond it is server data refused before it can allocate.
const MaxWorldStates = 1024

// ParseInitWorldStates reads SMSG_INIT_WORLD_STATES to its last byte
// (WorldStatePackets.cpp:23-40).
func ParseInitWorldStates(p []byte) (WorldStates, error) {
	b := wire.NewReader(p)
	var w WorldStates
	w.Map = b.ReadI32()
	w.Area = b.ReadI32()
	w.Sub = b.ReadI32()
	n := b.ReadU32()
	if n > MaxWorldStates {
		return WorldStates{}, errors.New("bg: too many world states")
	}
	w.States = make(map[int32]int32, n)
	for i := uint32(0); i < n; i++ {
		id := b.ReadI32()
		v := b.ReadI32()
		w.States[id] = v
	}
	if err := exactly(b); err != nil {
		return WorldStates{}, err
	}
	return w, nil
}

// WorldStateUpdate is SMSG_UPDATE_WORLD_STATE (UpdateWorldState::Write,
// WorldStatePackets.cpp:41-52): the id, value and the hidden bit.
type WorldStateUpdate struct {
	ID     uint32
	Value  int32
	Hidden bool
}

// ParseUpdateWorldState reads SMSG_UPDATE_WORLD_STATE to its last byte
// (WorldStatePackets.cpp:41-52).
func ParseUpdateWorldState(p []byte) (WorldStateUpdate, error) {
	b := wire.NewReader(p)
	var u WorldStateUpdate
	u.ID = b.ReadU32()
	u.Value = b.ReadI32()
	u.Hidden = b.ReadBit()
	flush(b)
	if err := exactly(b); err != nil {
		return WorldStateUpdate{}, err
	}
	return u, nil
}

// PlayerPosition is one flag-carrier row of
// SMSG_BATTLEGROUND_PLAYER_POSITIONS
// (BattlegroundPlayerPosition / BattlegroundPlayerPositions::Write,
// BattlegroundPackets.h:381-399, BattlegroundPackets.cpp:310-327): the
// carrier GUID, its X/Y (TaggedPosition<XY>, Position.h:217-229), the icon id
// and the arena slot (1 for a flag carrier; the 70291 client only draws
// slot 1, BattlegroundPackets.h:384-388).
type PlayerPosition struct {
	GUID wire.GUID
	X, Y float32
	Icon int8
	Slot int8
}

// MaxPositions bounds the flag-carrier list: the server sends one row per
// registered carrier (Battleground::_ProcessPlayerPositionBroadcast,
// Battleground.cpp:244-261). A count beyond it is refused before it can
// allocate.
const MaxPositions = 80

// CarrierSlot is the arena slot a real flag carrier carries (the 70291 client
// draws only this slot, BattlegroundPackets.h:384-388).
const CarrierSlot = 1

// ParsePlayerPositions reads SMSG_BATTLEGROUND_PLAYER_POSITIONS to its last
// byte (BattlegroundPackets.cpp:320-327).
func ParsePlayerPositions(p []byte) ([]PlayerPosition, error) {
	b := wire.NewReader(p)
	n := b.ReadU32()
	if n > MaxPositions {
		return nil, errors.New("bg: too many player positions")
	}
	out := make([]PlayerPosition, 0, n)
	for i := uint32(0); i < n; i++ {
		var r PlayerPosition
		r.GUID = guid(b)
		r.X = b.ReadFloat32()
		r.Y = b.ReadFloat32()
		r.Icon = b.ReadI8()
		r.Slot = b.ReadI8()
		out = append(out, r)
	}
	if err := exactly(b); err != nil {
		return nil, err
	}
	return out, nil
}

// ParsePlayerJoined reads SMSG_BATTLEGROUND_PLAYER_JOINED to its last byte:
// the joining guid (BattlegroundPlayerJoined::Write,
// BattlegroundPackets.cpp:329-334).
func ParsePlayerJoined(p []byte) (wire.GUID, error) {
	b := wire.NewReader(p)
	g := guid(b)
	if err := exactly(b); err != nil {
		return wire.GUID{}, err
	}
	return g, nil
}

// ParsePlayerLeft reads SMSG_BATTLEGROUND_PLAYER_LEFT to its last byte: the
// leaving guid (BattlegroundPlayerLeft::Write, BattlegroundPackets.cpp:336-341).
func ParsePlayerLeft(p []byte) (wire.GUID, error) {
	b := wire.NewReader(p)
	g := guid(b)
	if err := exactly(b); err != nil {
		return wire.GUID{}, err
	}
	return g, nil
}

// MatchState is the PVPMatchState enum (BattlegroundPackets.h:486-494).
type MatchState uint8

const (
	// MatchWaiting is PVPMatchState::Waiting.
	MatchWaiting MatchState = 0
	// MatchStartUp is PVPMatchState::StartUp (WAIT_JOIN).
	MatchStartUp MatchState = 1
	// MatchEngaged is PVPMatchState::Engaged (IN_PROGRESS).
	MatchEngaged MatchState = 2
	// MatchPostRound is PVPMatchState::PostRound.
	MatchPostRound MatchState = 3
	// MatchInactive is PVPMatchState::Inactive.
	MatchInactive MatchState = 4
	// MatchComplete is PVPMatchState::Complete (WAIT_LEAVE).
	MatchComplete MatchState = 5
)

// MatchInit is SMSG_PVP_MATCH_INITIALIZE (PVPMatchInitialize::Write,
// BattlegroundPackets.cpp:394-411): map, state, unix start, duration, arena
// faction, list id, the rated/affects-rating bits and the optional deserter
// penalty. A BG match has no penalty; the penalty tail is left unparsed and
// the parse fails closed when it is set: accepting blind int32s after an
// optional-presence bit is how a rating penalty would be misread as a score.
type MatchInit struct {
	Map        uint32
	State      MatchState
	Start      int64
	Duration   int64
	Faction    uint8
	ListID     uint32
	Registered bool
	Rated      bool
}

// ParseMatchInit reads SMSG_PVP_MATCH_INITIALIZE to its last byte
// (BattlegroundPackets.cpp:394-411).
func ParseMatchInit(p []byte) (MatchInit, error) {
	b := wire.NewReader(p)
	var m MatchInit
	m.Map = b.ReadU32()
	m.State = MatchState(b.ReadU8())
	m.Start = int64(b.ReadU64())
	m.Duration = int64(b.ReadU64())
	m.Faction = b.ReadU8()
	m.ListID = b.ReadU32()
	m.Registered = b.ReadBit()
	m.Rated = b.ReadBit()
	hasPenalty := b.ReadBit()
	flush(b)
	if hasPenalty {
		return MatchInit{}, errors.New("bg: rated deserter penalty is not decoded")
	}
	if err := exactly(b); err != nil {
		return MatchInit{}, err
	}
	return m, nil
}

// MatchEnd is SMSG_PVP_MATCH_COMPLETE (PVPMatchComplete::Write,
// BattlegroundPackets.cpp:420-432): the winning PvP team, the duration in
// seconds, the two solo-shuffle bits and, because the core always attaches
// them (Battleground.cpp:804), the log-data block.
type MatchEnd struct {
	// Winner is the PvPTeamId: 0 Horde, 1 Alliance, 2 Neutral.
	Winner int32
	// Duration is the match length in seconds (WorldPackets::Duration<Seconds>).
	Duration int64
	Solo     int
	// Stats is the log-data block, nil when the packet carried none or the
	// tail did not parse.
	Stats *MatchStats
	// TailErr is the log-data parse failure: the 12-byte head (winner and
	// duration) is still valid, so the caller ends the watch from it and
	// logs the tail instead of dropping the end.
	TailErr error
}

// WinnerName names a PvPTeamId: PVP_TEAM_HORDE is 0, PVP_TEAM_ALLIANCE 1 and
// PVP_TEAM_NEUTRAL 2 (SharedDefines.h:6936-6939). An empty name means no
// winner.
func WinnerName(winner int32) string {
	switch winner {
	case 0:
		return "horde"
	case 1:
		return "alliance"
	}
	return ""
}

// ParseMatchEnd reads SMSG_PVP_MATCH_COMPLETE (BattlegroundPackets.cpp:420-432):
// the head, then the log data when the optional bit is set (the same block
// PVPMatchStatisticsMessage carries). A log tail that does not parse leaves
// the head intact (MatchEnd.TailErr says why): the core always attaches the
// tail, and a layout detail in it must not cost the match's result. Only a
// packet too short for the head is an error.
func ParseMatchEnd(p []byte) (MatchEnd, error) {
	b := wire.NewReader(p)
	var m MatchEnd
	m.Winner = b.ReadI32()
	m.Duration = int64(b.ReadU64())
	hasLog := b.ReadBit()
	m.Solo = int(b.ReadBits(2))
	flush(b)
	if b.Err() != nil {
		return MatchEnd{}, errShort
	}
	var tailErr error
	if hasLog {
		s, err := readMatchStats(b)
		if err == nil {
			err = exactly(b)
		}
		if err != nil {
			tailErr = err
		} else {
			m.Stats = &s
		}
	} else {
		tailErr = exactly(b)
	}
	m.TailErr = tailErr
	return m, nil
}

// StatRow is one PVPMatchPlayerPVPStat (BattlegroundPackets.cpp:79-85).
type StatRow struct {
	ID    int32
	Value int32
}

// PlayerScore is one PVPMatchPlayerStatistics
// (BattlegroundPackets.cpp:87-133): the player, kills, faction, damage,
// healing, the stats list, the talent/sex/race/class bytes, and the optional
// honor and rating blocks. The honor and rating tails are decoded in full;
// the stats list is bounded.
type PlayerScore struct {
	GUID      wire.GUID
	Kills     uint32
	Faction   int32
	InWorld   bool
	Honor     bool
	Honors    uint32
	Deaths    uint32
	Bonus     uint32
	Damage    uint32
	Healing   uint32
	PreRate   uint32
	HasPre    bool
	RateDelta int32
	HasDelta  bool
	PreMMR    uint32
	HasMMR    bool
	MMRDelta  int32
	HasMMRD   bool
	PostMMR   uint32
	HasPost   bool
	Stats     []StatRow
	Talents   int32
	Sex       int8
	Race      int8
	Class     int8
	Role      int32
}

// MaxScores bounds the statistics' player list; MaxStats bounds one player's
// stat rows. Both are server data refused before they can allocate.
const (
	MaxScores = 80
	MaxStats  = 64
)

// MatchStats is SMSG_PVP_MATCH_STATISTICS (PVPMatchStatisticsMessage::Write,
// BattlegroundPackets.cpp:150-156): the optional ratings pair, the player
// count pair (horde, alliance: Battleground::BuildPvPLogDataPacket,
// Battleground.cpp:1431-1451) and the player rows.
type MatchStats struct {
	Ratings  bool
	PreA     int32
	PostA    int32
	PreMMRA  int32
	PreB     int32
	PostB    int32
	PreMMRB  int32
	Horde    int8
	Alliance int8
	Players  []PlayerScore
}

// ParseMatchStats reads SMSG_PVP_MATCH_STATISTICS to its last byte
// (BattlegroundPackets.cpp:58-157).
func ParseMatchStats(p []byte) (MatchStats, error) {
	b := wire.NewReader(p)
	s, err := readMatchStats(b)
	if err != nil {
		return MatchStats{}, err
	}
	if err := exactly(b); err != nil {
		return MatchStats{}, err
	}
	return s, nil
}

// readMatchStats reads the PVPMatchStatistics block
// (BattlegroundPackets.cpp:58-157) from a reader, leaving the bytes after it
// to the caller: the stats packet ends there, the match-complete's log data
// does too.
func readMatchStats(b *wire.Buffer) (MatchStats, error) {
	var s MatchStats
	hasRatings := b.ReadBit()
	n := b.ReadU32()
	if n > MaxScores {
		return MatchStats{}, errors.New("bg: too many score rows")
	}
	pa := b.ReadI8()
	pb := b.ReadI8()
	s.Horde = pa
	s.Alliance = pb
	if hasRatings {
		s.Ratings = true
		s.PreA = b.ReadI32()
		s.PostA = b.ReadI32()
		s.PreMMRA = b.ReadI32()
		s.PreB = b.ReadI32()
		s.PostB = b.ReadI32()
		s.PreMMRB = b.ReadI32()
	}
	for range n {
		r, err := readPlayerScore(b)
		if err != nil {
			return MatchStats{}, err
		}
		s.Players = append(s.Players, r)
	}
	if b.Err() != nil {
		return MatchStats{}, errShort
	}
	return s, nil
}

// readPlayerScore reads one PVPMatchPlayerStatistics
// (BattlegroundPackets.cpp:87-133). The presence bits come first (bit
// buffer), flushed, then the present blocks in field order
// (:114-130).
func readPlayerScore(b *wire.Buffer) (PlayerScore, error) {
	var r PlayerScore
	r.GUID = guid(b)
	r.Kills = b.ReadU32()
	r.Faction = b.ReadI32()
	r.Damage = b.ReadU32()
	r.Healing = b.ReadU32()
	ns := b.ReadU32()
	if ns > MaxStats {
		return PlayerScore{}, errors.New("bg: too many stat rows")
	}
	r.Talents = b.ReadI32()
	r.Sex = b.ReadI8()
	r.Race = b.ReadI8()
	r.Class = b.ReadI8()
	b.ReadI32() // CreatureID: exercised by the fixture, not kept
	b.ReadI32() // HonorLevel: exercised by the fixture, not kept
	r.Role = b.ReadI32()
	for i := uint32(0); i < ns; i++ {
		r.Stats = append(r.Stats, StatRow{ID: b.ReadI32(), Value: b.ReadI32()})
	}
	r.InWorld = b.ReadBit()
	hasHonor := b.ReadBit()
	hasPre := b.ReadBit()
	hasDelta := b.ReadBit()
	hasMMR := b.ReadBit()
	hasMMRD := b.ReadBit()
	hasPost := b.ReadBit()
	flush(b)
	if hasHonor {
		r.Honor = true
		r.Honors = b.ReadU32()
		r.Deaths = b.ReadU32()
		r.Bonus = b.ReadU32()
	}
	if hasPre {
		r.HasPre = true
		r.PreRate = b.ReadU32()
	}
	if hasDelta {
		r.HasDelta = true
		r.RateDelta = b.ReadI32()
	}
	if hasMMR {
		r.HasMMR = true
		r.PreMMR = b.ReadU32()
	}
	if hasMMRD {
		r.HasMMRD = true
		r.MMRDelta = b.ReadI32()
	}
	if hasPost {
		r.HasPost = true
		r.PostMMR = b.ReadU32()
	}
	if b.Err() != nil {
		return PlayerScore{}, errShort
	}
	return r, nil
}

// CapturePoint is SMSG_UPDATE_CAPTURE_POINT
// (UpdateCapturePoint::Write, BattlegroundPackets.cpp:449-454): the banner
// guid, its X/Y (TaggedPosition<XY> like the carrier rows), the state byte
// and, for the two contested states only, the capture time pair. AB banners
// use the world-state path instead (SetNodeStatus writes 1767+ world
// states), so this packet is decoded and counted, not modelled.
type CapturePoint struct {
	GUID  wire.GUID
	X, Y  float32
	State uint8
}

// ParseCapturePoint reads SMSG_UPDATE_CAPTURE_POINT to its last byte.
// The contested capture-time tail is refused: the watch would rather drop a
// contested icon than misread it.
func ParseCapturePoint(p []byte) (CapturePoint, error) {
	b := wire.NewReader(p)
	var c CapturePoint
	c.GUID = guid(b)
	c.X = b.ReadFloat32()
	c.Y = b.ReadFloat32()
	c.State = b.ReadU8()
	if c.State == 2 || c.State == 3 {
		return CapturePoint{}, errors.New("bg: contested capture-time tail is not decoded")
	}
	if err := exactly(b); err != nil {
		return CapturePoint{}, err
	}
	return c, nil
}

// ParseCaptureRemoved reads SMSG_CAPTURE_POINT_REMOVED to its last byte: the
// removed banner guid (CapturePointRemoved::Write,
// BattlegroundPackets.cpp:456-461).
func ParseCaptureRemoved(p []byte) (wire.GUID, error) {
	b := wire.NewReader(p)
	g := guid(b)
	if err := exactly(b); err != nil {
		return wire.GUID{}, err
	}
	return g, nil
}

// WSG world-state and scoring ids (ClassicWarsongGulch::WorldStates,
// battleground_warsong_gulch.cpp:73-89, 659-668).
const (
	// WSFlagAlliance is 1545: 0 in base, 1 carried, -1 on the ground.
	WSFlagAlliance = 1545
	// WSFlagHorde is 1546: 0 in base, 1 carried, -1 on the ground.
	WSFlagHorde = 1546
	// WSCapAlliance is 1581: Alliance captures.
	WSCapAlliance = 1581
	// WSCapHorde is 1582: Horde captures.
	WSCapHorde = 1582
)

// AB world-state and scoring ids (ClassicArathiBasin::WorldStates,
// battleground_arathi_basin.cpp:100-160, 614-618).
const (
	// ABResourcesAlliance is 1776.
	ABResourcesAlliance = 1776
	// ABResourcesHorde is 1777.
	ABResourcesHorde = 1777
	// ABOccupiedHorde is 1779/1778: occupied bases.
	ABOccupiedHorde  = 1778
	ABOccupiedAllies = 1779
)

// ABNodeNames are the five node names in NodeData order
// (battleground_arathi_basin.cpp:707-715).
var ABNodeNames = [5]string{"stables", "blacksmith", "farm", "lumber", "mine"}

// ABNodeBase holds one AB node's world-state ids (NodeInfo,
// battleground_arathi_basin.cpp:692-706): the neutral banner entry, the
// state id (Alliance +0, Horde +1, contested Alliance +2, contested Horde
// +3) and the neutral icon id.
type ABNodeBase struct {
	// Banner is the neutral banner entry.
	Banner uint32
	// State is the Alliance state id (+0); Horde is +1, Alliance contested
	// +2, Horde contested +3 (StateWorldStateOffset, :718).
	State int32
	// Icon is the neutral map-icon id.
	Icon int32
}

// ABNodes holds the five nodes in order (NodeData, :707-715): Stables,
// Blacksmith, Farm, Lumber Mill, Gold Mine.
var ABNodes = [5]ABNodeBase{
	{Banner: 180087, State: 1767, Icon: 1842},
	{Banner: 180088, State: 1782, Icon: 1846},
	{Banner: 180089, State: 1772, Icon: 1845},
	{Banner: 180090, State: 1792, Icon: 1844},
	{Banner: 180091, State: 1787, Icon: 1843},
}

// ABNodeState is one AB node's state from the world states.
type ABNodeState int

const (
	// ABNeutral is no state set (icon on, no occupied flag).
	ABNeutral ABNodeState = iota
	// ABAllianceHeld is occupied Alliance (+0).
	ABAllianceHeld
	// ABHordeHeld is occupied Horde (+1).
	ABHordeHeld
	// ABAllianceContested is contested Alliance (+2).
	ABAllianceContested
	// ABHordeContested is contested Horde (+3).
	ABHordeContested
)

// String names the node state.
func (s ABNodeState) String() string {
	switch s {
	case ABAllianceHeld:
		return "alliance"
	case ABHordeHeld:
		return "horde"
	case ABAllianceContested:
		return "alliance-contested"
	case ABHordeContested:
		return "horde-contested"
	}
	return "neutral"
}

// ABNode reads one AB node's state from the world states (SetNodeStatus,
// battleground_arathi_basin.cpp:1049-1090: icon 1 while neutral, else the
// offset's id 1).
func ABNode(states map[int32]int32, node int) ABNodeState {
	base := ABNodes[node]
	for off, st := range []ABNodeState{ABAllianceHeld, ABHordeHeld, ABAllianceContested, ABHordeContested} {
		if v, ok := states[base.State+int32(off)]; ok && v == 1 {
			return st
		}
	}
	return ABNeutral
}
