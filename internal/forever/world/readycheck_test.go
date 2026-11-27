package world

// #288: the leader's ready-check start and the answers in the event stream. The packets go through handleParty, the
// same dispatch the socket's frames take, and the rows are read from the session's event log.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

var (
	readyTestParty     = wire.GUID{Low: 0x1122, High: 0x0800000000000000}
	readyTestInitiator = wire.GUID{Low: 0x3344, High: 0x0100000000000000}
)

func readyStartedPayload() []byte {
	return []byte{
		0x00,                         // the party index
		0x03, 0x80, 0x22, 0x11, 0x08, // the party GUID
		0x03, 0x80, 0x44, 0x33, 0x01, // the initiator
		0x50, 0x46, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // 18,000 ms
	}
}

func readyResponsePayload(ready bool) []byte {
	// The two packed GUIDs this test uses are the fixed five-byte forms from party's packet tests.
	p := []byte{
		0x03, 0x80, 0x22, 0x11, 0x08, // the party GUID
		0x03, 0x80, 0x44, 0x33, 0x01, // the answering player
		0x00, // not ready
	}
	if ready {
		p[len(p)-1] = 0x80
	}
	return p
}

// TestReadyCheckPacketsReachTheEventStream feeds the start, one ready answer and the finish through handleParty and
// checks the three party rows and the model's state.
func TestReadyCheckPacketsReachTheEventStream(t *testing.T) {
	s := newSession(Options{Logf: func(string, ...any) {}}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	s.initChat()
	s.party.SelfGUID = readyTestInitiator

	for _, p := range []worldconn.Packet{
		{Core: wire.SMSGReadyCheckStarted, Payload: readyStartedPayload()},
		{Core: wire.SMSGReadyCheckResponse, Payload: readyResponsePayload(true)},
		{Core: wire.SMSGReadyCheckCompleted, Payload: []byte{0x00, 0x03, 0x80, 0x22, 0x11, 0x08}},
	} {
		if err := s.handleParty(p); err != nil {
			t.Fatalf("%s: %v", wire.ServerName(p.Core), err)
		}
	}

	rows, _, _, _ := s.EventLog().Since(0, 10)
	if len(rows) != 3 {
		t.Fatalf("%d event rows, want 3", len(rows))
	}
	for i, want := range []string{"ready_check_started", "ready_check_response", "ready_check_completed"} {
		if rows[i].Kind != events.KindParty {
			t.Errorf("row %d kind %q, want %q", i, rows[i].Kind, events.KindParty)
		}
		if got := rows[i].Data["state"]; got != want {
			t.Errorf("row %d state %v, want %q", i, got, want)
		}
	}
	if got := rows[0].Data["duration_ms"]; got != int64(18000) {
		t.Errorf("started duration_ms %v, want 18000", got)
	}
	if rows[0].SourceGUID != eventGUID(readyTestInitiator) {
		t.Errorf("started source %q, want the initiator", rows[0].SourceGUID)
	}
	if got := rows[1].Data["ready"]; got != true {
		t.Errorf("response ready %v, want true", got)
	}
	if rows[1].SourceGUID != eventGUID(readyTestInitiator) {
		t.Errorf("response source %q, want the answering player", rows[1].SourceGUID)
	}
	if got := rows[2].Data["ready"]; got != 1 || rows[2].Data["not_ready"] != 0 {
		t.Errorf("completed answers %v ready %v, want 1/0", rows[2].Data["ready"], rows[2].Data["not_ready"])
	}

	rc := s.party.ReadyCheck
	if rc == nil || rc.Active || !rc.Complete || rc.Duration != 18*time.Second {
		t.Fatalf("model ready check %+v", rc)
	}
}

// TestReadyCheckFinishBeforeTheStart: the core broadcasts a one-member check's finish before the started packet
// (Group.cpp:1484-1493). Both rows stay in that order and the model still ends the check as finished.
func TestReadyCheckFinishBeforeTheStart(t *testing.T) {
	s := newSession(Options{Logf: func(string, ...any) {}}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	s.initChat()
	for _, p := range []worldconn.Packet{
		{Core: wire.SMSGReadyCheckCompleted, Payload: []byte{0x00, 0x03, 0x80, 0x22, 0x11, 0x08}},
		{Core: wire.SMSGReadyCheckStarted, Payload: readyStartedPayload()},
	} {
		if err := s.handleParty(p); err != nil {
			t.Fatalf("%s: %v", wire.ServerName(p.Core), err)
		}
	}
	rows, _, _, _ := s.EventLog().Since(0, 10)
	if len(rows) != 2 || rows[0].Data["state"] != "ready_check_completed" || rows[1].Data["state"] != "ready_check_started" {
		t.Fatalf("rows: %+v", rows)
	}
	rc := s.party.ReadyCheck
	if rc == nil || rc.Active || !rc.Complete {
		t.Fatalf("the finish did not survive the late start: %+v", rc)
	}
}

// TestReadyCheckMalformedIsCounted: a malformed ready-check packet is a counted parse error, never an error that
// ends the session.
func TestReadyCheckMalformedIsCounted(t *testing.T) {
	s := newSession(Options{}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	if err := s.handleParty(worldconn.Packet{Core: wire.SMSGReadyCheckStarted, Payload: []byte{0x00, 0x03}}); err != nil {
		t.Fatalf("malformed start: %v", err)
	}
	if err := s.handleParty(worldconn.Packet{Core: wire.SMSGReadyCheckResponse, Payload: []byte{0x03}}); err != nil {
		t.Fatalf("malformed response: %v", err)
	}
	if s.report.ParseErrors != 2 {
		t.Fatalf("parse errors %d, want 2", s.report.ParseErrors)
	}
}

// TestReadyCheckSendsTheLeaderStart sends the command through the session's send path and checks the wire opcode
// and payload byte for byte: CMSG_DO_READY_CHECK with the optional party index present (0x80) and the roster's group
// category, then the core's broadcast start answered from the packet queue.
func TestReadyCheckSendsTheLeaderStart(t *testing.T) {
	s := newSession(Options{Logf: func(string, ...any) {}}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	s.initChat()
	s.party.SelfGUID = readyTestInitiator
	s.party.InParty = true
	s.party.RosterValid = true
	s.party.LeaderGUID = readyTestInitiator
	s.party.PartyGUID = readyTestParty
	s.party.Index = 1 // GROUP_CATEGORY_INSTANCE: the index the roster carried
	var sentCore uint32
	var sent []byte
	s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		sentCore, sent = core, append([]byte(nil), payload...)
		return nil
	}}
	s.events <- event{side: sideInstance, p: worldconn.Packet{Core: wire.SMSGReadyCheckStarted, Payload: readyStartedPayload()}}
	if err := s.readyCheck(context.Background()); err != nil {
		t.Fatalf("readyCheck: %v", err)
	}
	if sentCore != wire.CMSGDoReadyCheck {
		t.Fatalf("sent core 0x%06X, want 0x%06X (CMSG_DO_READY_CHECK)", sentCore, wire.CMSGDoReadyCheck)
	}
	if want := []byte{0x80, 0x01}; !bytes.Equal(sent, want) {
		t.Fatalf("sent % X, want % X", sent, want)
	}
	if rc := s.party.ReadyCheck; rc == nil || !rc.Active || rc.Initiator != readyTestInitiator {
		t.Fatalf("the start was not applied: %+v", rc)
	}
}

// TestReadyCheckRefusesOutsideAGroup: the call sends nothing without a roster, and its text names the method.
func TestReadyCheckRefusesOutsideAGroup(t *testing.T) {
	s := newSession(Options{}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	err := s.readyCheck(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ready_check: not in a group") {
		t.Fatalf("readyCheck outside a group: %v", err)
	}
}

// TestServeReadyCheckRegistered: the daemon's table carries the new method as implemented.
func TestServeReadyCheckRegistered(t *testing.T) {
	ported, _ := ServeRPCMethods()
	for _, name := range ported {
		if name == "ready_check" {
			return
		}
	}
	t.Fatalf("ready_check is not in the serve table: %v", ported)
}
