package world

// The map boundary is the instance connection's own stream (mapchange.go). These tests drive the session's real two
// readers over fabricated streams and use the reader hook to stop one reader between the read and the hand-off, so
// a packet can be held in a reader while the other socket overtakes it. Both interleavings of each race are run: the
// rule must decide by the instance stream's order, not by which reader ran first.

import (
	"context"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// streamStep is one packet of a fabricated socket stream; gate, when set, holds the reader before it hands the packet
// over (the test closes it to let the packet through).
type streamStep struct {
	p    worldconn.Packet
	gate <-chan struct{}
}

func streamPacket(core uint32, payload []byte) streamStep {
	return streamStep{p: worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}}
}

// streamReader returns the next function a socket reader runs over these steps.
func streamReader(steps []streamStep) func(context.Context) (worldconn.Packet, error) {
	i := 0
	return func(ctx context.Context) (worldconn.Packet, error) {
		if i >= len(steps) {
			<-ctx.Done()
			return worldconn.Packet{}, ctx.Err()
		}
		st := steps[i]
		i++
		if st.gate != nil {
			select {
			case <-st.gate:
			case <-ctx.Done():
				return worldconn.Packet{}, ctx.Err()
			}
		}
		return st.p, nil
	}
}

// streamHarness is a session with its two readers and a test-owned event loop playing the session goroutine.
type streamHarness struct {
	t *testing.T
	s *session
}

func newStreamHarness(t *testing.T) *streamHarness {
	t.Helper()
	s, _ := newTransferSession()
	s.events = make(chan event, 8)
	s.pumpCtx, s.pumpCancel = context.WithCancel(context.Background())
	return &streamHarness{t: t, s: s}
}

func (h *streamHarness) start(side int, steps []streamStep) {
	h.s.startPump(side, streamReader(steps))
}

// step handles one event the way the session goroutine does.
func (h *streamHarness) step() {
	h.t.Helper()
	ev, err := h.s.next("the test", 5*time.Second)
	if err != nil {
		h.t.Fatalf("the event: %v", err)
	}
	if err := h.s.handle(context.Background(), ev); err != nil {
		h.t.Fatalf("the event: %v", err)
	}
}

// wait steps until cond holds. It is checked before every step, so a wait for a state the last step produced does not
// consume the next packet.
func (h *streamHarness) wait(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s (phase %s, %d held, %d event(s) queued)",
				what, h.s.mapChg.phase, len(h.s.mapChg.held), len(h.s.events))
		}
		h.step()
	}
}

func (h *streamHarness) stop() {
	h.s.pumpCancel()
	for _, done := range h.s.pumpDone {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			h.t.Fatal("a reader did not stop")
		}
	}
}

// TestOldMapUpdateRacingTheNewWorldIsNotApplied: an update that belonged to the old map, read on the instance
// connection after the suspend token, is never applied to the fresh model. The two interleavings are the packet
// handled before the new world's clear and the packet held in its reader while the clear runs.
func TestOldMapUpdateRacingTheNewWorldIsNotApplied(t *testing.T) {
	cases := []struct {
		name string
		hold bool // stop the instance reader at the update instead of delivering it before the new world
	}{
		{"update handled before the new world", false},
		{"update held in its reader while the new world is handled", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newStreamHarness(t)
			defer h.stop()

			newWorldGate := make(chan struct{})
			resumeGate := make(chan struct{})
			var holdUpdate, updateRead chan struct{}
			if c.hold {
				holdUpdate, updateRead = make(chan struct{}), make(chan struct{})
				h.s.readHook = func(side int, p worldconn.Packet) {
					if side == sideInstance && p.Core == wire.SMSGUpdateObject {
						close(updateRead)
						<-holdUpdate
					}
				}
			}
			h.start(sideRealm, []streamStep{
				streamPacket(wire.SMSGTransferPending, pendingPayload(389, 1010, 2020, 55)),
				{
					gate: newWorldGate,
					p: worldconn.Packet{Core: wire.SMSGNewWorld, Name: wire.ServerName(wire.SMSGNewWorld),
						Payload: newWorldPayload(389, 1010, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1)},
				},
			})
			sentinel := []byte{0x01, 0x00, 0x77} // a packed GUID: uint16 mask 1, then Low 0x77
			h.start(sideInstance, []streamStep{
				streamPacket(wire.SMSGSuspendToken, tokenPayload(9, 1)),
				streamPacket(wire.SMSGUpdateObject, fakeCreateBlock(sentinel, 0, 1500, 2500, 60, 40)),
				{gate: resumeGate, p: worldconn.Packet{Core: wire.SMSGResumeToken, Name: wire.ServerName(wire.SMSGResumeToken),
					Payload: tokenPayload(9, 1)}},
			})

			h.wait("the suspend token answered", func() bool {
				return h.s.mapChg.phase == transferSuspended && h.s.mapChg.holdMapContent
			})
			if c.hold {
				<-updateRead // the update is out of the socket and stopped before the hand-off
				close(newWorldGate)
				h.wait("the new world applied", func() bool { return h.s.mapChg.nwSeen })
				close(holdUpdate)
			} else {
				h.wait("the old update held behind the closed gate", func() bool { return len(h.s.mapChg.held) == 1 })
				close(newWorldGate)
				h.wait("the new world applied", func() bool { return h.s.mapChg.nwSeen })
			}
			// The server writes the resume only after the client's world-port answer (MovementHandler.cpp:254-260).
			close(resumeGate)
			h.wait("the resume token handled", func() bool { return h.s.mapChg.phase == transferIdle })

			if got := h.s.world.Counts().Objects; got != 0 {
				t.Fatalf("the old map's create was applied to the new model: %d object(s)", got)
			}
			if got := h.s.report.StaleMapPackets; got != 1 {
				t.Fatalf("%d stale packet(s) counted, want 1", got)
			}
			if n := len(h.s.mapChg.held); n != 0 {
				t.Fatalf("%d packet(s) still held", n)
			}
		})
	}
}

// TestNewMapCreateRacingTheNewWorldIsNotDropped: a create read after the resume token is the new map's and applies
// in both interleavings — handled before the clear (held until it) and handled after it. The seamless teleport is
// the order in which the new map's content and the new world can race on the two sockets (HandleSuspendTokenResponse
// runs HandleMoveWorldportAck itself, MovementHandler.cpp:420-421).
func TestNewMapCreateRacingTheNewWorldIsNotDropped(t *testing.T) {
	cases := []struct {
		name string
		hold bool // stop the instance reader at the create while the new world is handled
	}{
		{"create handled before the clear", false},
		{"create held in its reader while the clear runs", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newStreamHarness(t)
			defer h.stop()

			newWorldGate := make(chan struct{})
			var holdCreate, createRead chan struct{}
			if c.hold {
				holdCreate, createRead = make(chan struct{}), make(chan struct{})
				h.s.readHook = func(side int, p worldconn.Packet) {
					if side == sideInstance && p.Core == wire.SMSGUpdateObject {
						close(createRead)
						<-holdCreate
					}
				}
			}
			h.start(sideRealm, []streamStep{{
				gate: newWorldGate,
				p: worldconn.Packet{Core: wire.SMSGNewWorld, Name: wire.ServerName(wire.SMSGNewWorld),
					Payload: newWorldPayload(1, 1000, 2000, 50, 0, objects.NewWorldReasonSeamless, 1)},
			}})
			sentinel := []byte{0x01, 0x00, 0x77}
			h.start(sideInstance, []streamStep{
				streamPacket(wire.SMSGSuspendToken, tokenPayload(3, 2)),
				streamPacket(wire.SMSGResumeToken, tokenPayload(3, 2)),
				streamPacket(wire.SMSGUpdateObject, fakeCreateBlock(sentinel, 0, 1000, 2000, 50, 40)),
			})

			h.wait("the seamless resume token handled", func() bool {
				return h.s.mapChg.phase == transferResumed && h.s.mapChg.resumeSeen && !h.s.mapChg.nwSeen
			})
			if c.hold {
				<-createRead
				close(newWorldGate)
				h.wait("the new world applied", func() bool { return h.s.mapChg.nwSeen })
				close(holdCreate)
			} else {
				h.wait("the create held for the new world", func() bool { return len(h.s.mapChg.held) == 1 })
				close(newWorldGate)
				h.wait("the new world applied", func() bool { return h.s.mapChg.nwSeen })
			}
			h.wait("the create applied", func() bool { return h.s.world.Counts().Objects == 1 })

			if got := h.s.report.StaleMapPackets; got != 0 {
				t.Fatalf("the new map's create was dropped: %d stale packet(s)", got)
			}
			if h.s.world.MapID() != 1 {
				t.Fatalf("the model is on map %d", h.s.world.MapID())
			}
		})
	}
}
