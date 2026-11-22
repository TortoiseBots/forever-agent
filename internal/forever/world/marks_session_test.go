package world

// The session's raid-target dispatch: the two SMSGs reach the party model and the event log, and a malformed one
// is counted and skipped instead of ending the session.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// markSinglePayload is SMSG_SEND_RAID_TARGET_UPDATE_SINGLE (PartyPackets.cpp:371-379) built by hand: the int8
// party index, the int8 symbol, the packed target, the packed changer.
func markSinglePayload(symbol int8, target, changedBy wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteU8(0) // party index
	b.WriteU8(uint8(symbol))
	wire.WritePackedGUID(b, target)
	wire.WritePackedGUID(b, changedBy)
	return b.Bytes()
}

// markAllPayload is SMSG_SEND_RAID_TARGET_UPDATE_ALL (PartyPackets.cpp:381-393): a uint8 party index, a uint32
// count, then each icon's packed GUID and uint8 symbol.
func markAllPayload(entries []party.MarkEntry) []byte {
	b := wire.NewWriter()
	b.WriteU8(0)
	b.WriteU32(uint32(len(entries)))
	for _, e := range entries {
		wire.WritePackedGUID(b, e.Target)
		b.WriteU8(uint8(e.Mark))
	}
	return b.Bytes()
}

// TestSessionAppliesMarkUpdates feeds the two mark packets to handleParty: the model takes them, the event log
// carries one raid_mark row each, and a truncated packet is counted and does not fail the call.
func TestSessionAppliesMarkUpdates(t *testing.T) {
	self := wire.GUID{Low: 0x2001, High: 0x0800000000000000}
	other := wire.GUID{Low: 0x2002, High: 0x0800000000000000}
	partyGUID := wire.GUID{Low: 0xBEEF, High: 0x1B00000000000000}
	s := &session{
		self:     self,
		selfSeen: true,
		party:    party.NewState(),
		report:   &Report{Counts: map[string]int{}},
		fight:    &fightLog{},
		world:    objects.NewWorld(0),
		opts:     Options{Logf: func(string, ...any) {}},
	}
	roster := fakePartyUpdate(partyGUID, self, self, []wire.GUID{self, other}, []string{"Self", "Other"}, false)
	if err := s.handleParty(worldconn.Packet{Core: wire.SMSGPartyUpdate, Payload: roster}); err != nil {
		t.Fatalf("the roster: %v", err)
	}
	if err := s.handleParty(worldconn.Packet{Core: wire.SMSGSendRaidTargetUpdateSingle, Payload: markSinglePayload(int8(party.MarkSkull), other, self)}); err != nil {
		t.Fatalf("the single update: %v", err)
	}
	if s.party.Marks[party.MarkSkull] != other {
		t.Fatalf("the skull slot is %v", s.party.Marks[party.MarkSkull])
	}
	if m, ok := s.party.MarkOf(other); !ok || m != party.MarkSkull {
		t.Fatalf("MarkOf = %d, %v", m, ok)
	}
	rows, _, _, _ := s.EventLog().Since(0, 16)
	row, ok := lastRaidMark(rows)
	if !ok {
		t.Fatal("no raid_mark row was written")
	}
	if row.TargetGUID != eventGUID(other) || row.SourceGUID != eventGUID(self) {
		t.Fatalf("the row names %q by %q", row.TargetGUID, row.SourceGUID)
	}
	if row.Data["state"] != "single" || row.Data["symbol"] != int(party.MarkSkull) {
		t.Fatalf("the row's data is %v", row.Data)
	}

	// A moved target: the cross lands on the same member and the skull slot is emptied.
	if err := s.handleParty(worldconn.Packet{Core: wire.SMSGSendRaidTargetUpdateSingle, Payload: markSinglePayload(int8(party.MarkCross), other, self)}); err != nil {
		t.Fatalf("the moved update: %v", err)
	}
	if s.party.Marks[party.MarkSkull] != wire.EmptyGUID || s.party.Marks[party.MarkCross] != other {
		t.Fatalf("after the move: skull %v cross %v", s.party.Marks[party.MarkSkull], s.party.Marks[party.MarkCross])
	}

	// The full list replaces every slot.
	if err := s.handleParty(worldconn.Packet{Core: wire.SMSGSendRaidTargetUpdateAll, Payload: markAllPayload([]party.MarkEntry{{Mark: party.MarkDiamond, Target: other}})}); err != nil {
		t.Fatalf("the list: %v", err)
	}
	if s.party.Marks[party.MarkCross] != wire.EmptyGUID || s.party.Marks[party.MarkDiamond] != other {
		t.Fatalf("after the list: cross %v diamond %v", s.party.Marks[party.MarkCross], s.party.Marks[party.MarkDiamond])
	}
	rows, _, _, _ = s.EventLog().Since(0, 16)
	if _, ok := lastRaidMark(rows); !ok {
		t.Fatal("the list wrote no raid_mark row")
	}

	// A truncated single update is counted and logged once, and handleParty returns no error: a hostile or broken
	// mark packet must not end the run.
	if err := s.handleParty(worldconn.Packet{Core: wire.SMSGSendRaidTargetUpdateSingle, Payload: []byte{0x00}}); err != nil {
		t.Fatalf("a truncated update failed the call: %v", err)
	}
	if s.report.ParseErrors != 1 {
		t.Fatalf("%d parse error(s), want 1", s.report.ParseErrors)
	}
	if m, ok := s.party.MarkOf(other); !ok || m != party.MarkDiamond {
		t.Fatal("the truncated packet changed the model")
	}
}

// lastRaidMark returns the newest raid_mark row.
func lastRaidMark(rows []Event) (Event, bool) {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Kind == events.KindRaidMark {
			return rows[i], true
		}
	}
	return Event{}, false
}
