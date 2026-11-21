package world

// Session handling of SMSG_AI_REACTION: only the hostile reaction becomes kind "hostile" (a pull moment);
// ALERT is the stealth-notice reaction (CreatureAI.cpp:156) and gets its own kind; the rest are ignored.
// A malformed response is logged and dropped, never an error that can end the session.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var reactionUnit = wire.GUID{Low: 0x0102030405060708, High: 0xF101112131415161}

func buildReaction(t *testing.T, unit wire.GUID, kind uint32) []byte {
	t.Helper()
	w := wire.NewWriter()
	wire.WritePackedGUID(w, unit)
	w.WriteU32(kind)
	return w.Bytes()
}

func reactionRows(s *session) []CombatRow {
	rows, _, _, _ := s.CombatLog().Since(0, 100)
	return rows
}

func reactionSession() (*session, *transferRecorder) {
	s, rec := newTransferSession()
	s.fight = &fightLog{started: s.Now()}
	return s, rec
}

// TestAIReactionHostileIsHostile: a HOSTILE reaction (Unit.cpp:6242, sent when a creature engages) writes one
// combat-log row of kind "hostile" with the reacting unit as source, and a fight-log line naming it.
func TestAIReactionHostileIsHostile(t *testing.T) {
	s, _ := reactionSession()
	if err := feed(t, s, sideInstance, wire.SMSGAIReaction, buildReaction(t, reactionUnit, 2)); err != nil {
		t.Fatalf("hostile: %v", err)
	}
	rows := reactionRows(s)
	if len(rows) != 1 || rows[0].Kind != "hostile" || rows[0].Reason != "hostile" {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].Source.GUID != reactionUnit.String() {
		t.Fatalf("source %+v, want %v", rows[0].Source, reactionUnit)
	}
	found := false
	for _, e := range s.fight.events {
		if e.Kind == "hostile" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no hostile fight event: %+v", s.fight.events)
	}
}

// TestAIReactionKinds: every reaction the server's enum names (SharedDefines.h:6636-6642) is classified by the
// session: hostile is hostile, alert is its own kind, and friendly/afraid/destroy write nothing. An unknown
// value is ignored, never a hostile row.
func TestAIReactionKinds(t *testing.T) {
	s, _ := reactionSession()
	if err := feed(t, s, sideInstance, wire.SMSGAIReaction, buildReaction(t, reactionUnit, 0)); err != nil {
		t.Fatalf("alert: %v", err)
	}
	for _, kind := range []uint32{1, 3, 4, 9} {
		if err := feed(t, s, sideInstance, wire.SMSGAIReaction, buildReaction(t, reactionUnit, kind)); err != nil {
			t.Fatalf("kind %d: %v", kind, err)
		}
	}
	rows := reactionRows(s)
	if len(rows) != 1 || rows[0].Kind != "alert" || rows[0].Reason != "alert" {
		t.Fatalf("rows %+v (want exactly one alert row)", rows)
	}
	for _, e := range s.fight.events {
		if e.Kind == "hostile" {
			t.Fatalf("non-hostile reaction wrote a hostile fight event: %+v", s.fight.events)
		}
	}
	if combat.AIReactionKind(2).String() != "hostile" || combat.AIReactionKind(0).String() != "alert" {
		t.Fatal("reaction words moved")
	}
}

// TestMalformedQueryIsDropped: a short gameobject/creature/player-names response is logged and dropped — it
// counts a parse error but ends no session — and an Allow=false answer is kept as a negative cache entry so
// the name round stops re-asking it.
func TestMalformedQueryIsDropped(t *testing.T) {
	s, _ := reactionSession()
	before := s.report.ParseErrors
	for _, tc := range []struct {
		name    string
		core    uint32
		payload []byte
	}{
		{"SMSG_QUERY_GAME_OBJECT_RESPONSE", wire.SMSGGameObjectQueryResponse, []byte{0x01}},
		{"SMSG_QUERY_CREATURE_RESPONSE", wire.SMSGCreatureQueryResponse, []byte{}},
		{"SMSG_QUERY_PLAYER_NAMES_RESPONSE", wire.SMSGQueryPlayerNamesResponse, []byte{0x01}},
	} {
		if err := feed(t, s, sideInstance, tc.core, tc.payload); err != nil {
			t.Fatalf("%s: malformed packet ended the session: %v", tc.name, err)
		}
	}
	if s.report.ParseErrors != before+3 {
		t.Fatalf("parse errors %d, want %d", s.report.ParseErrors, before+3)
	}

	allowOff := wire.NewWriter()
	allowOff.WriteU32(999001)
	wire.WritePackedGUID(allowOff, reactionUnit)
	allowOff.WriteBit(false)
	allowOff.FlushBits()
	allowOff.WriteU32(0)
	if err := feed(t, s, sideInstance, wire.SMSGGameObjectQueryResponse, allowOff.Bytes()); err != nil {
		t.Fatalf("Allow=false: %v", err)
	}
	if _, ok := s.world.GameObjectName(999001); !ok {
		t.Fatal("Allow=false answer was not cached as a negative entry")
	}
}

// TestAIReactionIsMapContent: a reaction from the old map read between the suspend and the resume is held and
// dropped at the resume, never merged into the fresh map's log.
func TestAIReactionIsMapContent(t *testing.T) {
	if !describesMapContent(wire.SMSGAIReaction) {
		t.Fatal("SMSG_AI_REACTION is not map content")
	}
	s, _ := reactionSession()
	mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(389, 1010, 2020, 55))
	mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(9, 1))
	if err := feed(t, s, sideInstance, wire.SMSGAIReaction, buildReaction(t, reactionUnit, 2)); err != nil {
		t.Fatalf("held reaction: %v", err)
	}
	if got := len(reactionRows(s)); got != 0 {
		t.Fatalf("%d reaction row(s) applied from the old map, want none", got)
	}
	mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(389, 1010, 2020, 55, 0.5, 1, 1))
	mustFeed(t, s, sideInstance, wire.SMSGResumeToken, tokenPayload(9, 1))
	if got := len(reactionRows(s)); got != 0 {
		t.Fatalf("%d reaction row(s) applied after the resume, want none (held packets drop)", got)
	}
	if s.report.StaleMapPackets != 1 {
		t.Fatalf("stale packets %d, want 1", s.report.StaleMapPackets)
	}
}
