package world

import (
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// FarClip is the terrain visibility distance this client puts into CMSG_PLAYER_LOGIN. It is the client's video
// setting, not a protocol constant: the two logins in the packet capture sent 777.0 and 2439.5. The server reads
// the field (PlayerLogin::Read) and does not use it.
const FarClip = 777.0

// playerLoginStart is the reference point of the client time field of the time-sync answers: the real client sends
// its own uptime in milliseconds, and only its growth between packets matters to the server.
var playerLoginStart = time.Now()

func clientTimeMs() uint32 { return uint32(time.Since(playerLoginStart).Milliseconds()) }

// BuildPlayerLogin is CMSG_PLAYER_LOGIN: packed ObjectGuid, float FarClip, one bit RPE
// (CharacterPackets.cpp PlayerLogin::Read).
func BuildPlayerLogin(guid wire.GUID) []byte { return buildPlayerLogin(guid, FarClip) }

func buildPlayerLogin(guid wire.GUID, farClip float32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, guid)
	b.WriteFloat32(farClip)
	b.WriteBit(false) // RPE
	b.FlushBits()
	return b.Bytes()
}

// ParseLoginVerifyWorld is SMSG_LOGIN_VERIFY_WORLD: int32 MapID, XYZO position, uint32 Reason
// (CharacterPackets.cpp LoginVerifyWorld::Write). The client's readers take four floats.
type VerifyWorld struct {
	MapID      int32
	X, Y, Z, O float32
	Reason     uint32
}

func ParseLoginVerifyWorld(payload []byte) (VerifyWorld, error) {
	var v VerifyWorld
	if len(payload) < 24 {
		return v, fmt.Errorf("world: SMSG_LOGIN_VERIFY_WORLD is %d bytes, want 24", len(payload))
	}
	b := wire.NewReader(payload)
	v.MapID = b.ReadI32()
	v.X, v.Y, v.Z, v.O = b.ReadFloat32(), b.ReadFloat32(), b.ReadFloat32(), b.ReadFloat32()
	v.Reason = b.ReadU32()
	return v, nil
}

// BuildLoadingScreenNotify is CMSG_LOADING_SCREEN_NOTIFY: int32 MapID and one bit Showing
// (CharacterPackets.cpp LoadingScreenNotify::Read). The real client sends it on the realm socket with the map of
// SMSG_LOGIN_VERIFY_WORLD and Showing=false once the screen is gone.
func BuildLoadingScreenNotify(mapID int32) []byte {
	return BuildLoadingScreenNotifyShowing(mapID, false)
}

// BuildLoadingScreenNotifyShowing is the same packet with the Showing bit set by the caller. A map change's client
// sends it with Showing=true right after SMSG_TRANSFER_PENDING (the captured packet ends 0x80); the login's is false.
func BuildLoadingScreenNotifyShowing(mapID int32, showing bool) []byte {
	b := wire.NewWriter()
	b.WriteI32(mapID)
	b.WriteBit(showing)
	b.FlushBits()
	return b.Bytes()
}

// ParseTimeSyncRequest is SMSG_TIME_SYNC_REQUEST: uint32 SequenceIndex (MiscPackets.cpp TimeSyncRequest::Write).
func ParseTimeSyncRequest(payload []byte) (uint32, error) {
	if len(payload) < 4 {
		return 0, fmt.Errorf("world: SMSG_TIME_SYNC_REQUEST is %d bytes, want 4", len(payload))
	}
	return wire.NewReader(payload).ReadU32(), nil
}

// BuildTimeSyncResponse is CMSG_TIME_SYNC_RESPONSE: uint32 SequenceIndex, uint32 ClientTime
// (MiscPackets.cpp TimeSyncResponse::Read).
func BuildTimeSyncResponse(sequence, clientTime uint32) []byte {
	b := wire.NewWriter()
	b.WriteU32(sequence)
	b.WriteU32(clientTime)
	return b.Bytes()
}

// BuildQueuedMessagesEnd is CMSG_QUEUED_MESSAGES_END: int32 Timestamp (AuthenticationPackets.cpp
// QueuedMessagesEnd::Read). The client answers SMSG_RESUME_COMMS with it; the server matches it to the
// 0xFFFFFFFE time-sync counter.
func BuildQueuedMessagesEnd(timestamp uint32) []byte {
	b := wire.NewWriter()
	b.WriteU32(timestamp)
	return b.Bytes()
}

// ParseMoveSetActiveMover is SMSG_MOVE_SET_ACTIVE_MOVER: packed ObjectGuid MoverGUID
// (Unit.cpp SendMoveSetActiveMover). The server waits for the answer below before it reveals the world.
func ParseMoveSetActiveMover(payload []byte) (wire.GUID, error) {
	b := wire.NewReader(payload)
	g := wire.ReadPackedGUID(b)
	if b.Err() != nil {
		return g, fmt.Errorf("world: SMSG_MOVE_SET_ACTIVE_MOVER: %w", b.Err())
	}
	return g, nil
}

// BuildMoveInitActiveMoverComplete is CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE: uint32 Ticks (client milliseconds);
// the server completes the 0xFFFFFFFF time sync with it and updates object visibility
// (MovementHandler.cpp HandleMoveInitActiveMoverComplete).
func BuildMoveInitActiveMoverComplete(ticks uint32) []byte {
	b := wire.NewWriter()
	b.WriteU32(ticks)
	return b.Bytes()
}

// BuildLogoutRequest is CMSG_LOGOUT_REQUEST. The server's reader says the Classic 1.60.1 client sends an empty
// payload and only reads the IdleLogout bit when bytes are left (CharacterPackets.cpp LogoutRequest::Read); the
// capture's one byte is that false bit, flushed, and the server accepts either.
func BuildLogoutRequest() []byte {
	b := wire.NewWriter()
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// BuildLogDisconnect is CMSG_LOG_DISCONNECT: uint32 Reason (AuthenticationPackets.cpp LogDisconnect::Read). The
// client sends it when it drops the socket.
func BuildLogDisconnect() []byte {
	b := wire.NewWriter()
	b.WriteU32(0)
	return b.Bytes()
}

// LogoutResponse is SMSG_LOGOUT_RESPONSE: int32 LogoutResult and the Instant bit (CharacterPackets.cpp
// LogoutResponse::Write). A non-zero result names why the player cannot log out (combat, falling, a duel).
type LogoutResponse struct {
	Result  int32
	Instant bool
}

func ParseLogoutResponse(payload []byte) (LogoutResponse, error) {
	var r LogoutResponse
	if len(payload) < 5 {
		return r, fmt.Errorf("world: SMSG_LOGOUT_RESPONSE is %d bytes, want 5", len(payload))
	}
	b := wire.NewReader(payload)
	r.Result = b.ReadI32()
	r.Instant = b.ReadBit()
	return r, nil
}

// ParseCharacterLoginFailed is SMSG_CHARACTER_LOGIN_FAILED: one byte reason code
// (CharacterPackets.cpp CharacterLoginFailed::Write).
func ParseCharacterLoginFailed(payload []byte) (uint8, error) {
	if len(payload) < 1 {
		return 0, fmt.Errorf("world: SMSG_CHARACTER_LOGIN_FAILED is empty")
	}
	return payload[0], nil
}

// BuildPing is CMSG_PING: uint32 Serial, uint32 Latency (AuthenticationPackets.cpp Ping::Read).
func BuildPing(serial, latency uint32) []byte {
	b := wire.NewWriter()
	b.WriteU32(serial)
	b.WriteU32(latency)
	return b.Bytes()
}

// ParsePong is SMSG_PONG: uint32 Serial.
func ParsePong(payload []byte) (uint32, error) {
	if len(payload) < 4 {
		return 0, fmt.Errorf("world: SMSG_PONG is %d bytes, want 4", len(payload))
	}
	return wire.NewReader(payload).ReadU32(), nil
}
