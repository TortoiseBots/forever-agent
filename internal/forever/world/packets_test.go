package world

import (
	"bytes"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The client packets are compared byte for byte with what the real 1.60.1 client sent (packet capture payloads).
func TestClientPacketsMatchCapture(t *testing.T) {
	list, err := ParseEnumCharactersResult(fixture(t, "enum_characters_result"))
	if err != nil || len(list.Regionwide) != 1 {
		t.Fatalf("character list: %v", err)
	}
	guid := list.Regionwide[0].GUID

	// the capture's far clip is that client's view distance; feed it back so the field order is what is compared
	captured := fixture(t, "player_login")
	farClip := wire.NewReader(captured[7:11]).ReadFloat32()
	if got, want := buildPlayerLogin(guid, farClip), captured; !bytes.Equal(got, want) {
		t.Fatalf("CMSG_PLAYER_LOGIN = % X, the client sent % X", got, want)
	}
	if got, want := BuildLoadingScreenNotify(0), fixture(t, "loading_screen_notify"); !bytes.Equal(got, want) {
		t.Fatalf("CMSG_LOADING_SCREEN_NOTIFY = % X, the client sent % X", got, want)
	}
	if got, want := BuildLogoutRequest(), fixture(t, "logout_request"); !bytes.Equal(got, want) {
		t.Fatalf("CMSG_LOGOUT_REQUEST = % X, the client sent % X", got, want)
	}
}

// The time-sync answers carry the client's own milliseconds; the capture's values are fed back in, so the field
// order (sequence index, then time) is what the capture pins.
func TestTimeSyncPacketsMatchCapture(t *testing.T) {
	response := fixture(t, "time_sync_response")
	seq := wire.NewReader(response).ReadU32()
	clientTime := wire.NewReader(response[4:]).ReadU32()
	if seq != 0 || clientTime == 0 {
		t.Fatalf("captured CMSG_TIME_SYNC_RESPONSE = % X", response)
	}
	if got := BuildTimeSyncResponse(seq, clientTime); !bytes.Equal(got, response) {
		t.Fatalf("CMSG_TIME_SYNC_RESPONSE = % X, the client sent % X", got, response)
	}
	ticks := wire.NewReader(fixture(t, "move_init_active_mover_complete")).ReadU32()
	if got, want := BuildMoveInitActiveMoverComplete(ticks), fixture(t, "move_init_active_mover_complete"); !bytes.Equal(got, want) {
		t.Fatalf("CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE = % X, the client sent % X", got, want)
	}
	queued := wire.NewReader(fixture(t, "queued_messages_end")).ReadU32()
	if queued == 0 {
		t.Fatalf("captured CMSG_QUEUED_MESSAGES_END has a zero timestamp")
	}
	if got, want := BuildQueuedMessagesEnd(queued), fixture(t, "queued_messages_end"); !bytes.Equal(got, want) {
		t.Fatalf("CMSG_QUEUED_MESSAGES_END = % X, the client sent % X", got, want)
	}
	request := fixture(t, "time_sync_request")
	wantSeq := wire.NewReader(request).ReadU32()
	if seq, err := ParseTimeSyncRequest(request); err != nil || seq != wantSeq {
		t.Fatalf("SMSG_TIME_SYNC_REQUEST parsed %d, %v; want %d", seq, err, wantSeq)
	}
}

func TestServerPacketsMatchCapture(t *testing.T) {
	v, err := ParseLoginVerifyWorld(fixture(t, "login_verify_world"))
	if err != nil {
		t.Fatal(err)
	}
	if v.MapID != 0 || v.O != 0 || v.Reason != 0 || v.X != 1.25 || v.Y != 2.5 || v.Z != 3.75 {
		t.Fatalf("SMSG_LOGIN_VERIFY_WORLD = %+v", v)
	}
	r, err := ParseLogoutResponse(fixture(t, "logout_response"))
	if err != nil || r.Result != 0 || r.Instant {
		t.Fatalf("SMSG_LOGOUT_RESPONSE = %+v, %v", r, err)
	}
	pong, err := ParsePong(fixture(t, "pong"))
	if err != nil || pong != 10 {
		t.Fatalf("SMSG_PONG serial %d, %v", pong, err)
	}
	list, err := ParseEnumCharactersResult(fixture(t, "enum_characters_result"))
	if err != nil || len(list.Regionwide) != 1 {
		t.Fatalf("character list: %v", err)
	}
	mover, err := ParseMoveSetActiveMover(fixture(t, "move_set_active_mover"))
	if err != nil || mover != list.Regionwide[0].GUID {
		t.Fatalf("SMSG_MOVE_SET_ACTIVE_MOVER %s, %v; character %s", mover, err, list.Regionwide[0].GUID)
	}
	// SMSG_LOGOUT_COMPLETE is one zero byte: OptionalInit(SwitchGameMode) false, flushed
	if got := fixture(t, "logout_complete"); len(got) != 1 || got[0] != 0 {
		t.Fatalf("SMSG_LOGOUT_COMPLETE = % X", got)
	}
}

func TestFindCharacter(t *testing.T) {
	list := []Character{{Name: "Testone"}, {Name: "Packet", Surname: "test"}}
	for _, want := range []string{"Testone", "testone", "TESTONE"} {
		if _, ok := findCharacter(list, want); !ok {
			t.Errorf("%q was not found", want)
		}
	}
	for _, want := range []string{"Packet test", "packet TEST", "Packet"} {
		if _, ok := findCharacter(list, want); !ok {
			t.Errorf("%q was not found", want)
		}
	}
	for _, want := range []string{"Test", "Testonex", ""} {
		if _, ok := findCharacter(list, want); ok {
			t.Errorf("%q matched a character", want)
		}
	}
}
