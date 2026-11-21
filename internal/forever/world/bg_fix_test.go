package world

// The review-round session tests: the match-complete packet the core really
// sends (head plus the log data it always attaches), its winner and duration
// units, the premature marker, the refused-status stop, the world-state cap
// and the death attribution (only the watcher's own death is Died()).

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bg"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

func bgTestSession() *session {
	return &session{report: &Report{Counts: map[string]int{}}}
}

// matchComplete builds SMSG_PVP_MATCH_COMPLETE from the core's writer
// (PVPMatchComplete::Write, BattlegroundPackets.cpp:420-432): winner,
// duration, the log-data bit, the two solo-shuffle bits, then the log data.
func matchComplete(winner int32, duration int64, withLog bool) []byte {
	b := wire.NewWriter()
	b.WriteI32(winner)
	b.WriteU64(uint64(duration))
	b.WriteBit(withLog)
	b.WriteBits(0, 2)
	b.FlushBits()
	if withLog {
		// PVPMatchStatistics: the ratings bit, the row count, the two player
		// counts, then no rows.
		b.WriteBit(false)
		b.WriteU32(0)
		b.WriteU8(0)
		b.WriteU8(0)
	}
	return b.Bytes()
}

func bgHandle(t *testing.T, s *session, core uint32, payload []byte) {
	t.Helper()
	if err := s.handleBG(worldconn.Packet{Core: core, Payload: payload}); err != nil {
		t.Fatalf("%s: %v", wire.ServerName(core), err)
	}
}

func TestMatchCompleteEndsTheWatch(t *testing.T) {
	s := bgTestSession()
	bgHandle(t, s, wire.SMSGPVPMatchComplete, matchComplete(1, 900, true))
	ports := bgPorts{s: s}
	end, ok := ports.Ended()
	if !ok {
		t.Fatal("the match-complete with log data did not end the watch")
	}
	if end.Winner != "alliance" || end.DurationS != 900 {
		t.Fatalf("end = %+v, want alliance in 900 s", end)
	}
	if end.Premature {
		t.Fatalf("a won match is premature: %+v", end)
	}
}

func TestMatchCompleteWinnerIDs(t *testing.T) {
	for _, tc := range []struct {
		winner int32
		name   string
	}{
		{0, "horde"},
		{1, "alliance"},
		{2, ""},
	} {
		s := bgTestSession()
		bgHandle(t, s, wire.SMSGPVPMatchComplete, matchComplete(tc.winner, 600, false))
		end, ok := bgPorts{s: s}.Ended()
		if !ok {
			t.Fatalf("winner %d: no end", tc.winner)
		}
		if end.Winner != tc.name {
			t.Fatalf("winner %d names %q, want %q", tc.winner, end.Winner, tc.name)
		}
		if (tc.name == "") != end.Premature {
			t.Fatalf("winner %d premature = %v", tc.winner, end.Premature)
		}
	}
}

// matchInit builds SMSG_PVP_MATCH_INITIALIZE from the core's writer
// (PVPMatchInitialize::Write, BattlegroundPackets.cpp:394-411): map, state,
// start, duration in seconds, arena faction, list id, the rated bits.
func matchInit(state uint8, duration int64) []byte {
	b := wire.NewWriter()
	b.WriteU32(489)
	b.WriteU8(state)
	b.WriteU64(1700000000)
	b.WriteU64(uint64(duration))
	b.WriteU8(0)
	b.WriteU32(2)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// bgConfirm builds SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION from the core's
// writers (BattlefieldStatusHeader, BattlegroundPackets.cpp:177-193, then
// :202-210): ticket, queue ids, range/team bytes, instance, bits, then map,
// timeout in ms and role.
func bgConfirm(timeout uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, wire.GUID{Low: 0x11})
	b.WriteU32(0) // ticket slot
	b.WriteU32(1) // ride type: battlegrounds
	b.WriteU64(1700000000)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteU32(1) // queue ids
	b.WriteU8(0)
	b.WriteU8(60)
	b.WriteU8(0)
	b.WriteU32(0) // instance: the header always writes 0
	b.WriteU64(bg.PackQueueType(bg.ListWSG))
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteU32(bg.MapWSG)
	b.WriteU32(timeout)
	b.WriteU8(0)
	return b.Bytes()
}

// bgActive builds SMSG_BATTLEFIELD_STATUS_ACTIVE from the core's writers
// (BattlefieldStatusHeader then BattlegroundPackets.cpp:212-224): map,
// shutdown, start, faction and the two bits.
func bgActive() []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, wire.GUID{Low: 0x11})
	b.WriteU32(0)
	b.WriteU32(1)
	b.WriteU64(1700000000)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteU32(1)
	b.WriteU8(0)
	b.WriteU8(60)
	b.WriteU8(0)
	b.WriteU32(0)
	b.WriteU64(bg.PackQueueType(bg.ListWSG))
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteU32(bg.MapWSG)
	b.WriteU32(0) // shutdown timer
	b.WriteU32(0) // start timer
	b.WriteU8(0)  // arena faction
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// TestMatchInitCompleteWaitsForTheWinner is the ordering fix: the complete
// state an entry in WAIT_LEAVE reports names the length but no winner, so it
// must not finalise the record; the result packet that follows does.
func TestMatchInitCompleteWaitsForTheWinner(t *testing.T) {
	s := bgTestSession()
	ports := bgPorts{s: s}
	bgHandle(t, s, wire.SMSGPVPMatchInitialize, matchInit(uint8(bg.MatchComplete), 1200))
	if _, ok := ports.Ended(); ok {
		t.Fatal("the complete-state initialize finalised the record before the winner arrived")
	}
	bgHandle(t, s, wire.SMSGPVPMatchComplete, matchComplete(0, 1200, true))
	end, ok := ports.Ended()
	if !ok {
		t.Fatal("the result packet did not end the watch")
	}
	if end.Winner != "horde" || end.DurationS != 1200 {
		t.Fatalf("end = %+v, want horde in 1200 s", end)
	}
}

// TestCompleteInitIsTheFallback is the bounded fallback: a result packet that
// never comes (the character entered a finished match) still ends the watch
// from the initialize state's length, naming no winner.
func TestCompleteInitIsTheFallback(t *testing.T) {
	s := bgTestSession()
	bgHandle(t, s, wire.SMSGPVPMatchInitialize, matchInit(uint8(bg.MatchComplete), 1200))
	s.bgInit().completeInitAt = time.Now().Add(-bgCompleteInitGrace - time.Second)
	end, ok := bgPorts{s: s}.Ended()
	if !ok {
		t.Fatal("the complete initialize never ended the watch")
	}
	if end.Winner != "" || end.DurationS != 1200 {
		t.Fatalf("fallback end = %+v, want no winner and 1200 s", end)
	}
}

// TestMatchCompleteBadTailKeepsTheHead is the tail fallback: a log-data block
// this parser cannot read (the core always attaches one) must not drop the
// result the 12-byte head carries.
func TestMatchCompleteBadTailKeepsTheHead(t *testing.T) {
	s := bgTestSession()
	b := wire.NewWriter()
	b.WriteI32(1) // PVP_TEAM_ALLIANCE
	b.WriteU64(900)
	b.WriteBit(true) // log data present
	b.WriteBits(0, 2)
	b.FlushBits()
	b.WriteBit(false) // the log block, then one byte too many
	b.WriteU32(0)
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU8(7)
	bgHandle(t, s, wire.SMSGPVPMatchComplete, b.Bytes())
	end, ok := bgPorts{s: s}.Ended()
	if !ok {
		t.Fatal("a bad log tail dropped the match end")
	}
	if end.Winner != "alliance" || end.DurationS != 900 {
		t.Fatalf("end = %+v, want alliance in 900 s", end)
	}
	if s.report.ParseErrors == 0 {
		t.Fatal("the bad tail was not counted as a parse error")
	}
}

// TestInviteCarriesItsExpiry is the accept-window fix: the invite the session
// hands over says when the server's window closes, so a late poll sees the
// time left rather than the first packet's full window.
func TestInviteCarriesItsExpiry(t *testing.T) {
	s := bgTestSession()
	ports := bgPorts{s: s}
	bgHandle(t, s, wire.SMSGBattlefieldStatusNeedConfirmation, bgConfirm(90000))
	inv, ok := ports.Invite(time.Now())
	if !ok {
		t.Fatal("the invite was not served")
	}
	if !inv.Expires.After(time.Now()) {
		t.Fatalf("invite expiry = %v, want in the future", inv.Expires)
	}
	if left := time.Until(inv.Expires); left > 90*time.Second || left < 80*time.Second {
		t.Fatalf("invite window = %v, want about 90 s", left)
	}
	// A status answer that arrives late carries the time left, not the
	// first window: the session rebases the expiry on each packet.
	bgHandle(t, s, wire.SMSGBattlefieldStatusNeedConfirmation, bgConfirm(7000))
	inv, _ = ports.Invite(time.Now())
	if left := time.Until(inv.Expires); left > 7*time.Second {
		t.Fatalf("late invite window = %v, want about 7 s", left)
	}
}

// TestActiveConfirmsTheEntry is the map-entry fix: ACTIVE is what confirms
// the queue entry, and it ends the invite's lifetime.
func TestActiveConfirmsTheEntry(t *testing.T) {
	s := bgTestSession()
	ports := bgPorts{s: s}
	bgHandle(t, s, wire.SMSGBattlefieldStatusNeedConfirmation, bgConfirm(90000))
	if ports.Active() {
		t.Fatal("an invite read as a confirmed entry")
	}
	bgHandle(t, s, wire.SMSGBattlefieldStatusActive, bgActive())
	if !ports.Active() {
		t.Fatal("ACTIVE did not confirm the entry")
	}
	if _, ok := ports.Invite(time.Now()); ok {
		t.Fatal("the invite outlived the entry")
	}
}

func TestStatusRefusedIsItsOwnStop(t *testing.T) {
	bad := fmt.Errorf("%w: no TBM:BG block", tbcmd.ErrBadReport)
	if !errors.Is(bgStatusError(bad), bg.ErrStatusRefused) {
		t.Fatalf("a refused poll reads as %v", bgStatusError(bad))
	}
	other := errors.New("closed")
	if errors.Is(bgStatusError(other), bg.ErrStatusRefused) {
		t.Fatal("a lost link reads as a refused poll")
	}
}

func TestWorldStateMapIsCapped(t *testing.T) {
	s := bgTestSession()
	ports := bgPorts{s: s}
	payload := func(id int32) []byte {
		b := wire.NewWriter()
		b.WriteU32(uint32(id))
		b.WriteI32(1)
		b.WriteBit(false)
		b.FlushBits()
		return b.Bytes()
	}
	for i := range MaxBGWorldStates {
		bgHandle(t, s, wire.SMSGUpdateWorldState, payload(int32(1000+i)))
	}
	bgHandle(t, s, wire.SMSGUpdateWorldState, payload(999999))
	if got := len(ports.WorldStates()); got != MaxBGWorldStates {
		t.Fatalf("world states = %d, want the cap %d", got, MaxBGWorldStates)
	}
	// A known id still takes its new value at the cap.
	bgHandle(t, s, wire.SMSGUpdateWorldState, payload(1000))
	if v := ports.WorldStates()[1000]; v != 1 {
		t.Fatalf("known id = %d", v)
	}
}

func TestOnlyTheWatcherDeathCounts(t *testing.T) {
	self := wire.GUID{Low: 0x1}
	other := wire.GUID{Low: 0x2}
	s := &session{self: self, selfSeen: true, report: &Report{Counts: map[string]int{}}}
	ports := bgPorts{s: s}
	s.bgOnDeaths([]wire.GUID{other})
	if ports.Died() {
		t.Fatal("another unit's death read as the watcher's")
	}
	s.bgOnDeaths([]wire.GUID{self})
	if !ports.Died() {
		t.Fatal("the watcher's own death was not counted")
	}
	if ports.Died() {
		t.Fatal("the death was counted twice")
	}
}
