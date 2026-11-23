package world

// Replay turns a `forever serve --capture-dir` capture back into session events: every captured server packet goes
// through the session's one dispatch (handle), on the realm side and against a session with no sockets, so the
// handlers build their models exactly as they did live. The only thing that leaves the session is a client packet a
// handler answers: it goes to a recording sender that counts it, so a replay never opens a connection.
//
// The clock is synthetic: the run directory's own start (its name's clock, else the Unix epoch) plus 100 ms per
// record, so event times follow the capture's order from the clock the writer recorded.

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// ReplayOptions is what a replay needs beyond the records: the character the capture was made for (empty when
// unknown) and the clock its first record is stamped with (zero is the Unix epoch).
type ReplayOptions struct {
	Character string
	Start     time.Time
}

// ReplayReport is what one replay did: how many records it read, dispatched, skipped and failed to parse, how many
// client packets the handlers sent, and the opcodes this build has no name for.
type ReplayReport struct {
	Records     int
	Handled     int
	Skipped     int
	ParseErrors int
	Sent        int
	Unknown     map[string]int
}

// replayStep is the synthetic clock's step per record.
const replayStep = 100 * time.Millisecond

// replaySkip is every opcode a capture cannot hold: handle dispatches these before its chat case, but the writer's
// capture call sites are all in the handlers the chat case and later entries call, so a capture holding one was
// edited. Dispatching it would answer it (the time sync would send a response, a teleport its ack), so the replay
// counts it and moves on. SMSG_UPDATE_OBJECT is captured by handle itself and is therefore not here.
var replaySkip = map[uint32]bool{
	wire.SMSGTimeSyncRequest:          true,
	wire.SMSGPong:                     true,
	wire.SMSGMoveSetActiveMover:       true,
	wire.SMSGCharacterLoginFailed:     true,
	wire.SMSGLogoutResponse:           true,
	wire.SMSGLogoutComplete:           true,
	wire.SMSGOnMonsterMove:            true,
	wire.SMSGMoveUpdate:               true,
	wire.SMSGMoveUpdateKnockBack:      true,
	wire.SMSGMoveKnockBack:            true,
	wire.SMSGMoveUpdateTeleport:       true,
	wire.SMSGMoveTeleport:             true,
	wire.SMSGMoveRoot:                 true,
	wire.SMSGMoveUnroot:               true,
	wire.SMSGQueryPlayerNamesResponse: true,
	wire.SMSGCreatureQueryResponse:    true,
	wire.SMSGGameObjectQueryResponse:  true,
	// handleTransfer's opcodes: the map change's own traffic, which the writer never captures.
	wire.SMSGTransferPending:    true,
	wire.SMSGSuspendToken:       true,
	wire.SMSGResumeToken:        true,
	wire.SMSGNewWorld:           true,
	wire.SMSGTransferAborted:    true,
	wire.SMSGUpdateLastInstance: true,
	wire.SMSGWorldServerInfo:    true,
	wire.SMSGInstanceInfo:       true,
}

// CaptureStart is the clock a run directory's name records: its first 16 bytes in captureRunLayout, else the Unix
// epoch (a directory that is no run's, or a name cut short).
func CaptureStart(dir string) time.Time {
	base := filepath.Base(filepath.Clean(dir))
	if len(base) >= len(captureRunLayout) {
		if t, err := time.Parse(captureRunLayout, base[:len(captureRunLayout)]); err == nil {
			return t.UTC()
		}
	}
	return time.Unix(0, 0).UTC()
}

// Replay dispatches recs through a fresh session and returns the events they emitted, oldest first. It builds the
// session with newSession and no CaptureDir (nothing is captured again), records every client packet a handler sends
// instead of writing it to a socket, and stamps each record with Start plus 100 ms per record. The skip list's
// opcodes are counted and not dispatched; a handler's error is counted and the replay goes on. A cancelled context
// ends the replay with its error.
func Replay(ctx context.Context, recs []capturedir.Record, o ReplayOptions) ([]Event, ReplayReport, error) {
	rep := ReplayReport{Records: len(recs), Unknown: map[string]int{}}
	s, sender := newReplaySession(ctx, o)
	var out []Event
	since := uint64(0)
	drain := func() {
		for {
			rows, next, _, more := s.EventLog().Since(since, 0)
			out = append(out, rows...)
			since = next
			if !more {
				return
			}
		}
	}
	for i := range recs {
		if err := ctx.Err(); err != nil {
			return out, rep, err
		}
		*sender.at = i
		core := recs[i].Opcode
		if replaySkip[core] {
			rep.Skipped++
			continue
		}
		name := wire.ServerName(core)
		if name == "" {
			rep.Unknown[fmt.Sprintf("0x%06X", core)]++
		}
		if err := replayDispatch(ctx, s, recs[i]); err != nil {
			rep.ParseErrors++
		} else {
			rep.Handled++
		}
		// The live session derives the death phase from the model in every wait; a replay has no waits, so it looks
		// after each record instead.
		s.observeSelf(s.Now())
		drain()
	}
	drain()
	rep.Sent = sender.sent
	return out, rep, nil
}

// replayDispatch hands one record to the session's one dispatch, on the realm side: the map-change gate holds only
// instance-side packets, and a record is not the socket it was read from.
func replayDispatch(ctx context.Context, s *session, rec capturedir.Record) error {
	return s.handle(ctx, event{side: sideRealm, p: worldconn.Packet{
		Core: rec.Opcode, Payload: rec.Payload, Name: wire.ServerName(rec.Opcode),
	}})
}

// replaySender is what a replay's handlers write through: it counts the packet and returns nil. It is also the
// replay's clock, so sent packets carry the record's own time.
type replaySender struct {
	sent  int
	start time.Time
	at    *int
}

// Send records one client packet.
func (r *replaySender) Send(core uint32, payload []byte) error {
	r.sent++
	return nil
}

// WaitUntil returns at once: a replay has no socket to wait on.
func (r *replaySender) WaitUntil(ctx context.Context, until time.Time) error { return nil }

// Now is the replay clock: Start plus one step per record.
func (r *replaySender) Now() time.Time {
	return r.start.Add(replayStep * time.Duration(*r.at))
}

// newReplaySession builds the session every replay runs on: no sockets, no capture, the replay clock and the
// recording sender.
func newReplaySession(ctx context.Context, o ReplayOptions) (*session, *replaySender) {
	start := o.Start
	if start.IsZero() {
		start = time.Unix(0, 0).UTC()
	}
	report := &Report{Counts: map[string]int{}, Character: Character{Name: o.Character}}
	s := newSession(Options{Character: o.Character}, report, ctx)
	s.start = start
	s.lastPacketAt = start
	at := 0
	sender := &replaySender{start: start, at: &at}
	s.fightSender = sender
	s.nowFn = sender.Now
	return s, sender
}
