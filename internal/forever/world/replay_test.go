package world

// Replay's contract: a capture's server packets go through the session's one dispatch and come back as events stamped
// from the run directory's own clock, a packet the writer never captures is counted and not dispatched, a handler's
// answers go to the recording sender and never to a socket, and no file is written beside the capture.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/death"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// replayStart is the clock of the run directories the tests write.
var replayStart = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

// replayFixturePacket is one packet of internal/forever/party/testdata/party.json.
type replayFixturePacket struct {
	Name    string `json:"name"`
	Opcode  string `json:"opcode"`
	Payload string `json:"payload"`
}

// replayPartyPacket reads one party fixture payload (the file is the fixture generator's output, payloads only).
func replayPartyPacket(t *testing.T, name string) (uint32, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "party", "testdata", "party.json"))
	if err != nil {
		t.Fatalf("the party fixtures: %v", err)
	}
	var doc struct {
		Packets []replayFixturePacket `json:"packets"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the party fixtures: %v", err)
	}
	for _, p := range doc.Packets {
		if p.Name != name {
			continue
		}
		op, err := hex.DecodeString(p.Opcode[2:])
		if err != nil || len(op) != 3 {
			t.Fatalf("%s: opcode %q", name, p.Opcode)
		}
		payload, err := hex.DecodeString(p.Payload)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return uint32(op[0])<<16 | uint32(op[1])<<8 | uint32(op[2]), payload
	}
	t.Fatalf("the party fixtures hold no %s", name)
	return 0, nil
}

// TestReplayPartyCaptureEmitsEvents: a captured party update yields the party event the live handler writes, the
// following chat packet yields the chat event, both carry the replay's own seq axis and clock, and the capture's
// directory is not touched.
func TestReplayPartyCaptureEmitsEvents(t *testing.T) {
	partyOp, partyPayload := replayPartyPacket(t, "party_update_joined")
	if partyOp != wire.SMSGPartyUpdate {
		t.Fatalf("the fixture's opcode is 0x%06X, want SMSG_PARTY_UPDATE", partyOp)
	}
	chatPayload := buildServerChat(chat.TypeSay, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID,
		"Replaychat", "", "", "", "SENTINEL-REPLAY-CHAT")
	recs := []capturedir.Record{
		{Run: "20261123T120000Z-01020304", Name: "0000_SMSG_PARTY_UPDATE.bin", Opcode: partyOp, Payload: partyPayload},
		{Run: "20261123T120000Z-01020304", Name: "0001_SMSG_CHAT.bin", Opcode: wire.SMSGChat, Payload: chatPayload},
	}

	events, rep, err := Replay(context.Background(), recs, ReplayOptions{Start: replayStart})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if rep.Records != 2 || rep.Handled != 2 || rep.Skipped != 0 || rep.ParseErrors != 0 || rep.Sent != 0 {
		t.Fatalf("report %+v", rep)
	}
	// The kinds the live handlers write: a party roster row and a chat row.
	var party, said *Event
	for i := range events {
		switch events[i].Kind {
		case "party":
			party = &events[i]
		case "chat":
			said = &events[i]
		}
	}
	if party == nil || said == nil {
		t.Fatalf("the events are %+v, want a party and a chat row", events)
	}
	if party.Data["state"] != "roster" {
		t.Fatalf("the party row is %+v", party)
	}
	if said.Text != "SENTINEL-REPLAY-CHAT" || said.Source != "Replaychat" {
		t.Fatalf("the chat row is %+v", said)
	}
	if party.Seq != 1 || said.Seq != 2 {
		t.Fatalf("the seq axis is %d, %d", party.Seq, said.Seq)
	}
	if !said.Time.Equal(replayStart.Add(replayStep)) {
		t.Fatalf("the chat row is stamped %s, want the replay clock %s", said.Time, replayStart.Add(replayStep))
	}
	// Nothing was captured again: the records carry no directory and Replay writes no file.
	if rep.Sent != 0 {
		t.Fatalf("the replay sent %d packet(s)", rep.Sent)
	}
}

// TestReplaySkipsUncapturedOpcodes: the writer never stores a time-sync request (the client's own answer would be
// the only traffic), so a capture holding one was edited: it is counted Skipped and never dispatched (the handler
// would answer it).
func TestReplaySkipsUncapturedOpcodes(t *testing.T) {
	recs := []capturedir.Record{{Name: "0000_SMSG_TIME_SYNC_REQ.bin", Opcode: wire.SMSGTimeSyncRequest, Payload: []byte{7, 0, 0, 0}}}
	events, rep, err := Replay(context.Background(), recs, ReplayOptions{Start: replayStart})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if rep.Skipped != 1 || rep.Handled != 0 || rep.Sent != 0 || len(events) != 0 {
		t.Fatalf("report %+v, %d event(s)", rep, len(events))
	}
	// The skip list names the time sync opcode, as the handle switch does.
	if !replaySkip[wire.SMSGTimeSyncRequest] {
		t.Fatalf("SMSG_TIME_SYNC_REQ is not in the skip list")
	}
	// SMSG_UPDATE_OBJECT is captured by the writer (session.go's handler), so it must never be skipped.
	if replaySkip[wire.SMSGUpdateObject] {
		t.Fatalf("SMSG_UPDATE_OBJECT is a skipped opcode")
	}
}

// TestReplayNeverTouchesASocket: the replay's session has no sockets; a resurrection offer the name rule asks the
// server about answers through the recording sender, so Sent counts it and nothing panics.
func TestReplayNeverTouchesASocket(t *testing.T) {
	ctx := context.Background()
	s, sender := newReplaySession(ctx, ReplayOptions{Character: "Replaychar", Start: replayStart})
	if s.realm != nil || s.instance != nil {
		t.Fatalf("the replay session has a socket")
	}
	// A death-check run that answers only an offer from a named unit asks for the offerer's name: the option is not
	// part of ReplayOptions, so the session is driven with it, as that run was.
	s.opts.AcceptResurrectFrom = "Replayoffer"
	self := wire.GUID{Low: 0x4401, High: 0x0800000000000000}
	for _, p := range []worldconn.Packet{
		{Core: wire.SMSGUpdateObject, Payload: fakeCreateBlockType(fakePackedGUID(self), 0, 1000, 2000, 50, 7, 6)},
		{Core: wire.SMSGUpdateObject, Payload: buildMaxHealthValues(self, 60)},
		{Core: wire.SMSGUpdateObject, Payload: buildHealthValues(self, 0)},
		{Core: wire.SMSGCorpseReclaimDelay, Payload: []byte{0x30, 0x75, 0, 0}},
		{Core: wire.SMSGResurrectRequest, Payload: buildResurrectOffer(death.ResurrectOffer{Offerer: wire.GUID{Low: 0x4402, High: 0x0800000000000000}, SpellID: 2008})},
	} {
		ev := event{side: sideRealm, p: p}
		ev.p.Name = wire.ServerName(p.Core)
		if err := s.handle(ctx, ev); err != nil {
			t.Fatalf("%s: %v", ev.p.Name, err)
		}
		s.observeSelf(s.Now())
	}
	if sender.sent != 1 {
		t.Fatalf("%d packet(s) sent, want the offerer's name query", sender.sent)
	}
}

// TestReplayClockFollowsRunName: the run directory's name is the capture's clock, and Replay stamps each record one
// step later; a name that is not a run's clock is the Unix epoch.
func TestReplayClockFollowsRunName(t *testing.T) {
	dir := "20261123T120000Z-01020304"
	if got := CaptureStart(dir); !got.Equal(replayStart) {
		t.Fatalf("CaptureStart(%q) = %s, want %s", dir, got, replayStart)
	}
	if got := CaptureStart("something-else"); !got.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("CaptureStart of a name that is no clock = %s, want the Unix epoch", got)
	}
	chatPayload := buildServerChat(chat.TypeSay, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "A", "", "", "", "one")
	recs := []capturedir.Record{
		{Run: dir, Opcode: wire.SMSGChat, Payload: chatPayload},
		{Run: dir, Opcode: wire.SMSGChat, Payload: chatPayload},
	}
	events, _, err := Replay(context.Background(), recs, ReplayOptions{Start: CaptureStart(dir)})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("%d event(s)", len(events))
	}
	if !events[0].Time.Equal(replayStart) || !events[1].Time.Equal(replayStart.Add(replayStep)) {
		t.Fatalf("event times %s, %s, want %s and %s", events[0].Time, events[1].Time, replayStart, replayStart.Add(replayStep))
	}
}

// TestReplayDispatchesOnRealmSide: the map-change gate holds map-content packets only on the instance side. With
// the gate closed, a record dispatched on the instance side would be held (and its handler never run), so the create
// followed by the water-walk change must still send the ack through the record's own dispatch.
func TestReplayDispatchesOnRealmSide(t *testing.T) {
	ctx := context.Background()
	self := wire.GUID{Low: 0x5501, High: 0x0800000000000000}
	recs := []capturedir.Record{
		{Opcode: wire.SMSGUpdateObject, Payload: fakeCreateBlockType(fakePackedGUID(self), 0, 1000, 2000, 50, 7, 6)},
		{Opcode: wire.SMSGMoveSetWaterWalk, Payload: buildMoveSetFlag(self, 4)},
	}
	s, sender := newReplaySession(ctx, ReplayOptions{Start: replayStart})
	s.mapChg.holdMapContent = true
	for _, rec := range recs {
		if err := replayDispatch(ctx, s, rec); err != nil {
			t.Fatalf("0x%06X: %v", rec.Opcode, err)
		}
	}
	if sender.sent != 1 {
		t.Fatalf("%d packet(s) sent, want the water-walk ack (an instance-side dispatch would be held)", sender.sent)
	}
}

// TestReplaySkipListMatchesHandle: every opcode the replay refuses to dispatch is a case of handle's own switch, so
// the skip list cannot grow stale behind a moved handler.
func TestReplaySkipListMatchesHandle(t *testing.T) {
	cases := handleSwitchCases(t)
	names := wireConstantNames(t)
	for core := range replaySkip {
		name := names[core]
		if name == "" {
			t.Errorf("replaySkip holds 0x%06X, which names no wire constant", core)
			continue
		}
		if !cases[name] {
			t.Errorf("replaySkip holds %s (0x%06X), which is no case of handle", name, core)
		}
	}
}
