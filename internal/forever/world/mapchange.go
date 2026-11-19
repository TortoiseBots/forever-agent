package world

// The map change: SMSG_TRANSFER_PENDING takes the character out of its map, SMSG_SUSPEND_TOKEN and
// SMSG_NEW_WORLD move it to the new one, and SMSG_WORLD_SERVER_INFO, the update-last-instance and the instance info
// describe the map it landed on. The sequences, the layouts and what the client clears are in
// docs/forever/MAPCHANGE-R1-NOTES.md; the server lines are cited next to each parse.
//
// The rules this file implements:
//
//   - Between the transfer pending and the new world the client is "between maps": the mover refuses every movement
//     (objects.ErrMapChange), so no packet carries the old map's coordinates, and a walk or a fight ends with that
//     error at its next step. The window is bounded by mapChangeLimit: a new world that never comes ends the run
//     with ErrMapChangeTimeout instead of refusing every later move on a guess.
//   - The new world takes the map and the position from the packet, clears the world model, resets the mover, drops
//     the navmesh planner and answers with the empty CMSG_WORLD_PORT_RESPONSE; the create burst that follows is
//     applied as after a login.
//   - A phase machine accepts exactly the sequences the server sends (see the table in the notes): a transfer
//     packet out of phase is counted and ignored, never applied.
//   - The map boundary is IN BAND, on the instance connection's own stream. The server writes SMSG_SUSPEND_TOKEN
//     and SMSG_RESUME_TOKEN there and nothing map-related in between (Player.cpp:1526-1527 removes the character
//     from its map before the token; MovementHandler.cpp:254-260 sends the resume before the first new-map packet).
//     So map content is held from the suspend answer to the resume token, dropped at the resume (it is not the new
//     map's) and applied only after the new world's clear when it was read after the resume. The new world itself
//     travels on the other socket, so the session never classifies by arrival time: it queues the new map's first
//     packets until the clear is done.
//   - An abort leaves everything where it was and reports the reason by name.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// ErrMapChange is the mover's between-maps refusal, named for the walker and the fighter: a walk or a fight that
// meets a map change ends with it (objects.ErrMapChange, so either name matches with errors.Is).
var ErrMapChange = objects.ErrMapChange

// ErrMapChangeTimeout is the end of a run whose transfer never resolved: the server took the character out of its
// map (or said it would) and no SMSG_NEW_WORLD followed within mapChangeLimit. Movement is not reopened on a guess.
var ErrMapChangeTimeout = errors.New("world: map change did not complete")

const (
	// mapObjectsWait is how long forever map-check watches a map it just landed on before it counts the objects
	// there: the create burst arrives right after the new world, but the last name and object packets trail it by a
	// tick.
	mapObjectsWait = 5 * time.Second
	// mapChangeLimit bounds the between-maps window. The server's whole far teleport is one round trip (the client
	// answers the suspend token, the server sends the new world at once, MovementHandler.cpp:397-421); a lost new
	// world on a live link would otherwise keep every move refused forever. A run that reaches it ends by name.
	mapChangeLimit = 60 * time.Second
	// transferReasonSeamless is the token reason of a seamless teleport (Player.cpp:1544, MovementHandler.cpp:257).
	transferReasonSeamless uint8 = 2
	// transferReasonFar is the token reason of a far teleport: the one that also has a transfer pending.
	transferReasonFar uint8 = 1
	// maxHeldMapPackets bounds the queue the between-maps gate holds. The server writes no map content on the
	// instance connection between the suspend answer and the resume token, so a gate that fills is a broken peer;
	// past the bound the packets are dropped and counted instead of growing the run's memory.
	maxHeldMapPackets = 256
)

// transferPhase is the session's place in a map change. Only the session goroutine touches it.
type transferPhase uint8

const (
	// transferIdle: on a map, no transfer in flight.
	transferIdle transferPhase = iota
	// transferPending: SMSG_TRANSFER_PENDING arrived; the suspend token and the new world are still to come.
	transferPending
	// transferSuspended: the suspend token was answered; the new world is still to come.
	transferSuspended
	// transferResumed: the new world was applied (or, for a seamless teleport, its resume token came first); the
	// resume token of the transfer (or its new world) is still to come.
	transferResumed
)

// String names the phase in the log lines.
func (p transferPhase) String() string {
	switch p {
	case transferIdle:
		return "idle"
	case transferPending:
		return "pending"
	case transferSuspended:
		return "suspended"
	case transferResumed:
		return "resumed"
	}
	return fmt.Sprintf("phase %d", uint8(p))
}

// MapChangeRow is one map change the client followed, for the report and the map-check output.
type MapChangeRow struct {
	FromMap    int32
	ToMap      int32
	Reason     uint32
	Difficulty int16
	// Position is the destination the server named (the new world's, or the aborting transfer's).
	Position [4]float32
	// Aborted is a transfer the server refused; the reason (its number and the core's name) and the argument are
	// the abort's.
	Aborted       bool
	AbortReasonID uint32
	AbortReason   string
	AbortArg      uint8
	// Objects is how many objects the model held after the map was watched for mapObjectsWait; it is set by
	// forever map-check (0 elsewhere).
	Objects int
}

// mapChange is the session's map change state. Only the session goroutine touches it.
type mapChange struct {
	phase transferPhase
	// between is the movement window: closed from the transfer pending (or a seamless suspend token) until the new
	// world.
	between bool
	// epoch counts the transfers the client saw (open, resolve). A wait that sees it change returns ErrMapChange:
	// it cannot know whether its leg survived the transition.
	epoch uint64
	// Since is when the movement window opened, for mapChangeLimit.
	since time.Time

	// The transfer in flight, kept apart from last so a pending never overwrites a resolved row that map-check has
	// not printed: pendingRow is the PENDING's destination, pendingMap the map it named (-1 when the transfer began
	// with a suspend token, the seamless path), hasPending whether one arrived at all.
	hasPending bool
	pendingRow MapChangeRow
	pendingMap int32
	// suspendSeq is the sequence of the last accepted suspend token; a resume token must carry the same one
	// (Player.cpp:1543 and MovementHandler.cpp:255 both use m_movementCounter).
	suspendSeq    uint32
	hasSuspendSeq bool
	// suspendReason is that token's reason: 1 for a far teleport, 2 for a seamless one (Player.cpp:1544). Only a
	// far transfer can have a transfer pending of its own: the server writes the pending before the token and skips
	// it for a seamless transfer (Player.cpp:1501-1522), so a pending read after a seamless token belongs to no
	// transfer of ours and must not name the map the new world is checked against.
	suspendReason uint8
	// nwSeen is whether the current transfer's new world was applied.
	nwSeen bool
	// counter is the last applied new world's counter: a duplicate new world must not clear the model again.
	counter     int32
	haveCounter bool

	// holdMapContent is the instance stream's between-maps gate. It closes when the accepted suspend token is
	// answered: the server removed the character from its map before that token (Player.cpp:1526-1527), so what
	// the instance connection carries from then until the resume token is not the model's map (in flight from the
	// old one or belonging to no map). While the gate is closed an instance packet that describes map content is
	// held, never applied; it is dropped when the resume token marks the new map's content (the writer sends the
	// resume before the first new-map packet, MovementHandler.cpp:254-260) and applied only after the new world's
	// clear, if it is the new map's.
	holdMapContent bool
	// resumeSeen is whether the open transfer's resume token was handled: everything after it on the instance
	// stream is the new map's content.
	resumeSeen bool
	// awaitResume is set by the new world's clear when the resume has not been read yet (the far path): the gate
	// stays closed for it, and clearAt bounds how long it may be missing (mapChangeLimit), so a lost resume cannot
	// leave the run silently dropping the new map's content.
	awaitResume bool
	clearAt     time.Time
	// held is the map-content packets the gate held, in instance-stream order (the channel keeps each reader's
	// order). It is bounded by maxHeldMapPackets; a flood past the bound is dropped and counted, never kept.
	held []event

	// stale and unsolicited count the packets the map boundary and the phase machine refused, for the report.
	stale       int
	unsolicited int
	// lastComplain and lastStaleLog rate-limit those log lines: a flooding server gets one line a second.
	lastComplain time.Time
	lastStaleLog time.Time

	// last is the most recent change (resolved or aborted) and report is set while map-check has not printed it.
	last   MapChangeRow
	report bool
}

// mapEpoch is the map change counter WaitUntil compares.
func (s *session) mapEpoch() uint64 { return s.mapChg.epoch }

// betweenMaps reports whether the movement window is closed.
func (s *session) betweenMaps() bool { return s.mapChg.between }

// mapChangeTimedOut ends a run whose between-maps window outlived mapChangeLimit: a lost new world is not something
// to guess about, because every later movement packet would be refused (or, worse, carry the old map) forever.
func (s *session) mapChangeTimedOut() error {
	if s.mapChg.awaitResume && s.Now().Sub(s.mapChg.clearAt) >= mapChangeLimit {
		return fmt.Errorf("%w: no SMSG_RESUME_TOKEN within %s of the new world on map %d",
			ErrMapChangeTimeout, mapChangeLimit, s.world.MapID())
	}
	if !s.mapChg.between {
		return nil
	}
	if s.Now().Sub(s.mapChg.since) < mapChangeLimit {
		return nil
	}
	where := "the seamless transfer"
	if s.mapChg.hasPending {
		where = fmt.Sprintf("map %d", s.mapChg.pendingMap)
	}
	return fmt.Errorf("%w: no SMSG_NEW_WORLD within %s of the transfer to %s (phase %s)",
		ErrMapChangeTimeout, mapChangeLimit, where, s.mapChg.phase)
}

// unsolicitedPacket counts a transfer packet the server's own sequences cannot produce and, rate-limited to one line
// a second, names it. It changes no state, moves no mover and answers nothing.
func (s *session) unsolicitedPacket(packet, why string) {
	s.mapChg.unsolicited++
	s.report.UnsolicitedTransfers++
	now := s.Now()
	if now.Sub(s.mapChg.lastComplain) < time.Second {
		return
	}
	s.mapChg.lastComplain = now
	s.opts.logf("world: %s ignored in phase %s: %s (%d ignored transfer packet(s) so far)",
		packet, s.mapChg.phase, why, s.mapChg.unsolicited)
}

// openMapChange closes the movement window for a transfer the phase machine accepted and starts its epoch.
func (s *session) openMapChange(phase transferPhase) {
	s.mapChg.phase = phase
	s.mapChg.between = true
	s.mapChg.epoch++
	s.mapChg.since = s.Now()
	s.mapChg.nwSeen = false
	s.mapChg.suspendReason = 0
	s.mapChg.hasPending = false
	s.mapChg.pendingMap = -1
	s.mapChg.pendingRow = MapChangeRow{}
	// A new transfer starts with an open instance gate: between the pending and the suspend token the server has
	// not taken the character off its map yet (it removes it between the two packets, Player.cpp:1522-1527), so
	// map content read there is still the old map's and may be applied to the old model. The suspend token closes
	// the gate.
	s.mapChg.holdMapContent = false
	s.mapChg.resumeSeen = false
	s.mapChg.awaitResume = false
	s.countStaleMapPacket(len(s.mapChg.held))
	s.mapChg.held = s.mapChg.held[:0]
	s.report.MapChanges++
	if s.mover != nil {
		s.mover.BeginMapChange()
	}
}

// namePending records the pending transfer's destination (and the row that is not published until it resolves).
func (s *session) namePending(t objects.TransferPending) {
	s.mapChg.hasPending = true
	s.mapChg.pendingMap = t.MapID
	s.mapChg.pendingRow = MapChangeRow{
		FromMap:  int32(s.world.MapID()),
		ToMap:    t.MapID,
		Position: [4]float32{t.DestX, t.DestY, t.DestZ, 0},
	}
}

// sendLoadingScreen tells the server the loading screen is up, with the map it is loading and the Showing bit set
// (the captured client sends it on the realm socket right after the suspend token).
func (s *session) sendLoadingScreen(mapID int32) {
	if err := s.sendRealm(wire.CMSGLoadingScreenNotify, BuildLoadingScreenNotifyShowing(mapID, true)); err != nil {
		s.opts.logf("world: CMSG_LOADING_SCREEN_NOTIFY for the map change: %v", err)
	}
}

// beginMapChange handles SMSG_TRANSFER_PENDING: the character leaves the old map now (the server removed it from
// the map before it sent this, Player.cpp:1526-1527), so the mover is suspended and the transition is recorded.
//
// The server sends the pending on the realm socket and the suspend token on the instance socket, so the session can
// read the token first. A pending that arrives while a FAR transfer is already suspended is that same transfer's
// destination and is accepted there; the phase does not move back. A seamless transfer has no pending of its own
// (Player.cpp:1501-1522), so a pending read after its suspend token (reason 2, Player.cpp:1544) cannot name its
// destination: it is counted and ignored, which is what keeps it from refusing the real new world for the wrong map
// or from moving the window.
func (s *session) beginMapChange(t objects.TransferPending) {
	switch s.mapChg.phase {
	case transferIdle, transferResumed:
		s.openMapChange(transferPending)
		s.namePending(t)
		s.sendLoadingScreen(t.MapID)
		s.opts.logf("world: map change: SMSG_TRANSFER_PENDING for map %d (from map %d); the mover refuses movement until the new world",
			t.MapID, int32(s.world.MapID()))
	case transferSuspended:
		if s.mapChg.hasPending {
			s.unsolicitedPacket("SMSG_TRANSFER_PENDING", "the transfer already named its map")
			return
		}
		if s.mapChg.suspendReason != transferReasonFar {
			s.unsolicitedPacket("SMSG_TRANSFER_PENDING", "a seamless transfer sends no transfer pending")
			return
		}
		s.namePending(t)
		s.sendLoadingScreen(t.MapID)
		s.opts.logf("world: map change: SMSG_TRANSFER_PENDING for map %d (from map %d) after the suspend token; the transfer is the same",
			t.MapID, int32(s.world.MapID()))
	default:
		s.unsolicitedPacket("SMSG_TRANSFER_PENDING", "a transfer is already open")
	}
}

// suspendTransfer handles SMSG_SUSPEND_TOKEN. It is legal at the start of a seamless transfer (the server sends no
// transfer pending for one, Player.cpp:1501-1522) and as the pending transfer's next step. It is answered on the
// instance socket, as the captured client does; an out-of-phase token gets no answer.
func (s *session) suspendTransfer(tok objects.TransferToken) {
	switch s.mapChg.phase {
	case transferIdle, transferResumed:
		// A seamless teleport: the character leaves its map here and the client does not know the destination yet.
		s.openMapChange(transferSuspended)
	case transferPending:
		s.mapChg.phase = transferSuspended
	default:
		s.unsolicitedPacket("SMSG_SUSPEND_TOKEN", "the transfer is already suspended")
		return
	}
	s.mapChg.suspendSeq = tok.Sequence
	s.mapChg.hasSuspendSeq = true
	s.mapChg.suspendReason = tok.Reason
	// The gate closes here, on the instance stream itself: the server had already removed the character from its
	// map when it wrote this token (Player.cpp:1526-1527), and the next map's content on this connection comes only
	// after the resume token (MovementHandler.cpp:254-260). Everything in between is held and dropped at the
	// resume.
	s.mapChg.holdMapContent = true
	s.mapChg.resumeSeen = false
	s.mapChg.held = s.mapChg.held[:0]
	// The server sends the new world only after this answer (HandleSuspendTokenResponse, MovementHandler.cpp:
	// 397-421), and the answer goes back on the instance socket, as the capture's client does.
	if err := s.sendOn(sideInstance, wire.CMSGSuspendTokenResponse, objects.BuildSuspendTokenResponse(tok.Sequence)); err != nil {
		s.opts.logf("world: CMSG_SUSPEND_TOKEN_RESPONSE: %v", err)
		return
	}
	s.opts.logf("world: SMSG_SUSPEND_TOKEN (sequence %d, reason %d) answered", tok.Sequence, tok.Reason)
}

// resumeTransfer handles SMSG_RESUME_TOKEN: the character is on the new map and the server has resumed the client.
// A seamless teleport's resume can be read before its new world (they travel on different sockets), so it is legal
// once the transfer is suspended; otherwise it closes a transfer whose new world was applied.
func (s *session) resumeTransfer(tok objects.TransferToken) {
	if s.mapChg.hasSuspendSeq && tok.Sequence != s.mapChg.suspendSeq {
		s.unsolicitedPacket("SMSG_RESUME_TOKEN", fmt.Sprintf("sequence %d does not follow the suspend token's %d", tok.Sequence, s.mapChg.suspendSeq))
		return
	}
	switch s.mapChg.phase {
	case transferSuspended:
		if tok.Reason != transferReasonSeamless {
			s.unsolicitedPacket("SMSG_RESUME_TOKEN", "no new world has put the character on the new map")
			return
		}
		s.mapChg.phase = transferResumed
		s.markNewMapContent()
		s.opts.logf("world: SMSG_RESUME_TOKEN (sequence %d, reason %d) before the new world; the seamless transfer is in flight",
			tok.Sequence, tok.Reason)
	case transferResumed:
		if !s.mapChg.nwSeen {
			s.unsolicitedPacket("SMSG_RESUME_TOKEN", "the transfer's resume token already arrived")
			return
		}
		s.markNewMapContent()
		s.mapChg.phase = transferIdle
		s.mapChg.nwSeen = false
		s.opts.logf("world: SMSG_RESUME_TOKEN (sequence %d, reason %d); the character is on the new map", tok.Sequence, tok.Reason)
	case transferPending:
		// The worldport ack failed and the server teleported the character home: its resume token never comes, but
		// the previous transfer's token can still be in flight when the new pending arrives. It is not an error.
		if s.mapChg.hasSuspendSeq && tok.Sequence == s.mapChg.suspendSeq {
			s.opts.logf("world: SMSG_RESUME_TOKEN (sequence %d) belongs to the resolved transfer", tok.Sequence)
			return
		}
		s.unsolicitedPacket("SMSG_RESUME_TOKEN", "no new world has put the character on the new map")
	default:
		s.unsolicitedPacket("SMSG_RESUME_TOKEN", "no transfer is open")
	}
}

// applyNewWorld handles SMSG_NEW_WORLD: the character is on the new map now. The packet's map and position are the
// truth; the old map's objects go, the mover is reset there, the navmesh planner is dropped, and the world-port
// response tells the server the client loaded (it is what makes the server add the player to the map).
//
// Everything the packet says is validated before any state changes, and a packet the sequence cannot produce is
// ignored with a counter: only a suspended transfer (or a seamless one whose resume token came first) has a new
// world to apply.
func (s *session) applyNewWorld(ctx context.Context, nw objects.NewWorldPacket) error {
	legal := s.mapChg.phase == transferSuspended ||
		(s.mapChg.phase == transferResumed && !s.mapChg.nwSeen)
	if !legal {
		s.unsolicitedPacket("SMSG_NEW_WORLD", "no suspend token put the transfer in flight")
		return nil
	}
	if err := nw.Validate(); err != nil {
		return fmt.Errorf("world: %w", err)
	}
	if s.mapChg.hasPending && nw.MapID != s.mapChg.pendingMap {
		s.unsolicitedPacket("SMSG_NEW_WORLD", fmt.Sprintf("it names map %d while the transfer pending named %d", nw.MapID, s.mapChg.pendingMap))
		return nil
	}
	if s.mapChg.haveCounter && nw.Counter == s.mapChg.counter {
		s.unsolicitedPacket("SMSG_NEW_WORLD", fmt.Sprintf("counter %d is the last applied new world's", nw.Counter))
		return nil
	}

	was := int32(s.world.MapID())
	from := was
	if s.mapChg.hasPending {
		from = s.mapChg.pendingRow.FromMap
	}
	pos := [4]float32{nw.X, nw.Y, nw.Z, nw.O}
	s.mapChg.epoch++
	s.mapChg.phase = transferResumed
	s.mapChg.nwSeen = true
	s.mapChg.between = false
	s.mapChg.counter, s.mapChg.haveCounter = nw.Counter, true
	s.mapChg.hasPending = false
	s.mapChg.pendingRow = MapChangeRow{}
	s.mapChg.pendingMap = -1
	s.mapChg.report = true
	s.mapChg.last = MapChangeRow{FromMap: from, ToMap: nw.MapID, Reason: nw.Reason, Position: pos}

	s.report.MapID = nw.MapID
	s.report.Position = pos
	s.report.WalkClientPos = pos
	s.world.ResetForMap(uint32(nw.MapID))
	// A roster held before this character's own GUID was known is the old map's: the new map's party update follows
	// the new world, so keeping it would apply a stale snapshot after the clear.
	if s.party != nil && s.party.DropHeld() {
		s.opts.logf("world: map change: a party roster held before this character's own GUID was known is dropped; the server sends the new map's roster")
	}
	if s.mover != nil {
		s.mover.EndMapChange(nw.X, nw.Y, nw.Z, nw.O)
	}
	// The planner caches Detour meshes per map; the old map's tiles must not be kept (or walk the new map).
	s.dropPlanner()
	// The capture shows the client acking the active mover again on every map: SendInitSelf registers a fresh
	// time sync for the new create (Map.cpp:1903-1909).
	s.activeMoverDone = false
	if err := s.sendRealm(wire.CMSGWorldPortResponse, objects.BuildWorldPortResponse()); err != nil {
		s.opts.logf("world: CMSG_WORLD_PORT_RESPONSE: %v", err)
	}
	// The resume token precedes the first new-map packet on the instance connection (MovementHandler.cpp:254-260),
	// so once it was handled every packet the gate still holds is new-map content and may be applied now that the
	// clear, the mover and the world-port answer are done. Before the resume the gate stays closed: content read
	// between the clear and the resume belongs to no map of ours and is dropped at the resume.
	if s.mapChg.resumeSeen {
		s.mapChg.holdMapContent = false
		if err := s.applyHeldMapPackets(ctx); err != nil {
			return err
		}
	} else {
		// The far path: the resume comes after the client's world-port answer, so the gate stays closed until it
		// arrives. clearAt bounds the wait; mapChangeTimedOut reports a resume that never came.
		s.mapChg.awaitResume = true
		s.mapChg.clearAt = s.Now()
	}
	s.opts.logf("world: map change: SMSG_NEW_WORLD map %d at %.2f %.2f %.2f (reason %d, counter %d); the world model is cleared, the mover is at the new position, CMSG_WORLD_PORT_RESPONSE sent",
		nw.MapID, nw.X, nw.Y, nw.Z, nw.Reason, nw.Counter)
	return nil
}

// abortMapChange handles SMSG_TRANSFER_ABORTED: the server refused a transfer before it took the character out of
// its map, so everything stays as it was and the refusal is reported with the core's reason name, the argument and
// the map. This build sends every abort from the teleport's checks (Player.cpp:1317-1434, 19814) or its area-trigger
// handler (MiscHandler.cpp:698-739), all before the transfer pending; an abort while the client is between maps is
// therefore not something the server does, and it is counted instead of closing the window.
func (s *session) abortMapChange(a objects.TransferAborted) {
	if s.mapChg.between {
		s.unsolicitedPacket("SMSG_TRANSFER_ABORTED", "the character is already between maps")
		return
	}
	s.report.AbortedTransfers++
	reason := objects.TransferAbortReasonName(a.Reason)
	s.mapChg.last = MapChangeRow{
		FromMap:       int32(s.world.MapID()),
		ToMap:         int32(a.MapID),
		Position:      s.report.WalkClientPos,
		Aborted:       true,
		AbortReasonID: a.Reason,
		AbortReason:   reason,
		AbortArg:      a.Arg,
	}
	s.mapChg.report = true
	s.opts.logf("world: map change aborted: map %d refused, reason %d (%s), argument %d, difficulty condition %d; the character stays on map %d at %.2f %.2f %.2f",
		a.MapID, a.Reason, reason, a.Arg, a.MapDifficultyXConditionID, s.world.MapID(),
		s.report.WalkClientPos[0], s.report.WalkClientPos[1], s.report.WalkClientPos[2])
}

// describesMapContent reports whether a server packet carries the state of the map the client stands on (objects,
// movement, combat, auras). Those are the packets an old-map copy must not apply to a fresh model; time syncs, pongs
// and the informational packets are not.
func describesMapContent(core uint32) bool {
	switch core {
	case wire.SMSGUpdateObject, wire.SMSGOnMonsterMove, wire.SMSGMoveUpdate, wire.SMSGMoveUpdateTeleport,
		wire.SMSGMoveTeleport, wire.SMSGAttackStart, wire.SMSGAttackStop, wire.SMSGCancelCombat,
		wire.SMSGAttackerStateUpdate, wire.SMSGSpellStart, wire.SMSGSpellGo, wire.SMSGCastFailed,
		wire.SMSGSpellFailure, wire.SMSGSpellFailedOther, wire.SMSGSpellNonMeleeDamage, wire.SMSGAuraUpdate,
		wire.SMSGPowerUpdate,
		// The self movement-state packets: the four the death flow handles and answers (run and swim speed,
		// water and land walk). A copy written for the old map would move the mover, satisfy the release's ghost
		// speed wait, and make the client answer (CMSG_MOVE_FORCE_*_SPEED_CHANGE_ACK, MovementHandler.cpp:662-733;
		// CMSG_MOVE_WATER_WALK_ACK, MovementHandler.cpp:777-784) with the new side's movement info. The new map
		// restates what it needs: the player's own create at the add carries the movement flags, water walking
		// among them (Map.cpp:1882-1907, BaseEntity.cpp:296; the far teleport keeps that status bit,
		// Player.cpp:1357-1358 and MovementInfo.h:114-115), the aura-derived states go as
		// SMSG_MOVE_SET_COMPOUND_STATE (Player.cpp:26654-26695) and the ghost aura is re-sent to the client
		// (Player.cpp:26699). A speed rate is not re-sent, but neither is it reset by the teleport, so the mover
		// keeps the last rate it applied; only a change announced after the suspend would be missed, and that is
		// exactly the old map's copy the gate exists to refuse.
		wire.SMSGMoveSetRunSpeed, wire.SMSGMoveSetSwimSpeed, wire.SMSGMoveSetWaterWalk, wire.SMSGMoveSetLandWalk:
		return true
	}
	return false
}

// holdMapPacket holds one map-content packet read on the instance connection while the between-maps gate is closed,
// and reports whether it did. Ordering is the point: the socket reader reads byte by byte, so the gate's decisions
// are this connection's own stream positions (the suspend token closes it, MovementHandler.cpp:254-260's resume token
// opens it), never a counter shared with the other reader. A held packet is applied only after the new world's clear
// and only if it was read after the resume marker; anything else is dropped at the marker with a counted, rate-limited
// line. Past maxHeldMapPackets the packet is dropped at once: a gate this full is not a map change.
func (s *session) holdMapPacket(ev event) bool {
	if !s.mapChg.holdMapContent || !describesMapContent(ev.p.Core) {
		return false
	}
	if len(s.mapChg.held) >= maxHeldMapPackets {
		s.countStaleMapPacket(1)
		return true
	}
	s.mapChg.held = append(s.mapChg.held, ev)
	return true
}

// markNewMapContent resolves the instance stream's boundary at the accepted resume token: every packet the gate still
// holds was read before it and is therefore not the new map's, so it is dropped; if the new world is already applied
// the gate opens (the same function runs from applyNewWorld for the seamless resume-first order).
func (s *session) markNewMapContent() {
	s.mapChg.resumeSeen = true
	n := len(s.mapChg.held)
	s.mapChg.held = s.mapChg.held[:0]
	s.countStaleMapPacket(n)
	if s.mapChg.nwSeen {
		s.mapChg.holdMapContent = false
		s.mapChg.awaitResume = false
	}
}

// countStaleMapPacket adds to the run's stale counter and writes at most one line a second. Only numbers go into the
// line; the packet names were already logged by the phase machine when it refused them, and a flood must not turn
// into a log flood.
func (s *session) countStaleMapPacket(n int) {
	if n <= 0 {
		return
	}
	s.mapChg.stale += n
	s.report.StaleMapPackets += n
	now := s.Now()
	if now.Sub(s.mapChg.lastStaleLog) < time.Second {
		return
	}
	s.mapChg.lastStaleLog = now
	s.opts.logf("world: %d map-content packet(s) read between the suspend token and the resume token were dropped; %d so far",
		n, s.mapChg.stale)
}

// applyHeldMapPackets applies the packets the gate held for the new map, in instance-stream order, after the clear
// and the mover placement are done. They were read after the resume token, so they are the new map's; a packet the
// gate still holds before the resume was already dropped by markNewMapContent.
func (s *session) applyHeldMapPackets(ctx context.Context) error {
	held := s.mapChg.held
	if len(held) == 0 {
		return nil
	}
	s.mapChg.held = nil
	s.opts.logf("world: %d held packet(s) of the new map applied after the clear", len(held))
	for _, ev := range held {
		if err := s.handle(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

// takeMapChangeReport returns the last resolved change once, so map-check prints each transition exactly once.
func (s *session) takeMapChangeReport() (MapChangeRow, bool) {
	if !s.mapChg.report {
		return MapChangeRow{}, false
	}
	s.mapChg.report = false
	return s.mapChg.last, true
}

// handleTransfer answers the packets of a map change. It is the one registration line the session's dispatch adds
// for this round (handle, at the end of the list).
func (s *session) handleTransfer(ctx context.Context, p worldconn.Packet) error {
	switch p.Core {
	case wire.SMSGTransferPending:
		t, err := objects.ParseTransferPending(p.Payload)
		if err != nil {
			return fmt.Errorf("world: %w", err)
		}
		s.beginMapChange(t)
	case wire.SMSGSuspendToken:
		tok, err := objects.ParseTransferToken(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_SUSPEND_TOKEN: %w", err)
		}
		s.suspendTransfer(tok)
	case wire.SMSGResumeToken:
		tok, err := objects.ParseTransferToken(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_RESUME_TOKEN: %w", err)
		}
		s.resumeTransfer(tok)
	case wire.SMSGNewWorld:
		nw, err := objects.ParseNewWorld(p.Payload)
		if err != nil {
			return fmt.Errorf("world: %w", err)
		}
		if err := s.applyNewWorld(ctx, nw); err != nil {
			return err
		}
	case wire.SMSGTransferAborted:
		a, err := objects.ParseTransferAborted(p.Payload)
		if err != nil {
			return fmt.Errorf("world: %w", err)
		}
		s.abortMapChange(a)
	case wire.SMSGUpdateLastInstance:
		mapID, err := objects.ParseUpdateLastInstance(p.Payload)
		if err != nil {
			return fmt.Errorf("world: %w", err)
		}
		s.opts.logf("world: SMSG_UPDATE_LAST_INSTANCE map %d", mapID)
	case wire.SMSGWorldServerInfo:
		w, err := objects.ParseWorldServerInfo(p.Payload)
		if err != nil {
			return fmt.Errorf("world: %w", err)
		}
		s.report.Difficulty = w.DifficultyID
		group := ""
		if w.GroupSize != nil {
			group = fmt.Sprintf(", instance group size %d", *w.GroupSize)
		}
		s.opts.logf("world: SMSG_WORLD_SERVER_INFO difficulty %d%s", w.DifficultyID, group)
	case wire.SMSGInstanceInfo:
		locks, err := objects.ParseInstanceInfo(p.Payload)
		if err != nil {
			return fmt.Errorf("world: %w", err)
		}
		s.opts.logf("world: SMSG_INSTANCE_INFO %d lock(s)", len(locks))
		for _, l := range locks {
			s.opts.logf("  instance lock: map %d difficulty %d, id %d, %d minute(s) left, locked %v extended %v",
				l.MapID, l.DifficultyID, l.InstanceID, l.TimeRemaining/60, l.Locked, l.Extended)
		}
	default:
		return fmt.Errorf("world: %s is not a transfer packet", p.Name)
	}
	return nil
}

// sendRealm writes one client packet on the realm socket, which is the socket the capture's client uses for the
// world-port handshake. A session without sockets (the fight tests' fake transport) writes through the transport
// instead, so the handler is exercisable without a link.
func (s *session) sendRealm(core uint32, payload []byte) error {
	if s.realm == nil {
		return s.transport().Send(core, payload)
	}
	return s.sendOn(sideRealm, core, payload)
}

// EnterAreaTrigger sends CMSG_AREA_TRIGGER for a portal, as a player entering one does: Entered and FromClient set
// (AreaTriggerPackets.cpp:23-28; the captured client sends both). The trigger id is the caller's — dungeons/
// ragefire_chasm.json carries the entrance trigger — because this round keeps no table of them. The server ignores
// the packet when the character is not standing in the trigger (MiscHandler.cpp:556-561), so a walker must walk
// there first.
func (s *session) EnterAreaTrigger(id int32) error {
	if err := s.sendOn(sideInstance, wire.CMSGAreaTrigger, objects.BuildAreaTrigger(id, true, true)); err != nil {
		return fmt.Errorf("world: CMSG_AREA_TRIGGER: %w", err)
	}
	s.report.AreaTriggers++
	s.opts.logf("world: CMSG_AREA_TRIGGER %d (entered, from the client)", id)
	return nil
}

// navPlanner returns the navmesh planner of this run, building it on first use. A map change drops it, so the next
// route is planned on a planner that builds the new map's meshes (with the parent-map rule of Options.Parents).
func (s *session) navPlanner() (*navmesh.Planner, error) {
	if s.planner != nil {
		return s.planner, nil
	}
	if s.opts.NavmeshDir == "" {
		return nil, errors.New("world: no navmesh folder given")
	}
	if _, err := os.Stat(s.opts.NavmeshDir); err != nil {
		return nil, err
	}
	s.planner = navmesh.NewForeverPlanner(s.opts.NavmeshDir, s.opts.Parents)
	return s.planner, nil
}

// dropPlanner frees the planner's Detour meshes (a map change, or the end of a navmesh walk).
func (s *session) dropPlanner() {
	if s.planner != nil {
		s.planner.Close()
		s.planner = nil
	}
}

// mapCheck is forever map-check: settle, print the map the client is on, optionally send one area trigger, then stay
// in the world and follow every map change the server makes, printing each one with the difficulty, the position
// and the objects the new map has five seconds later.
func (s *session) mapCheck(ctx context.Context) error {
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 3 * time.Second
	}
	if err := s.collect(ctx, settle); err != nil {
		return err
	}
	s.opts.logf("map-check: on map %d at %.2f %.2f %.2f, difficulty %d, %d object(s) in the model",
		s.world.MapID(), s.report.WalkClientPos[0], s.report.WalkClientPos[1], s.report.WalkClientPos[2],
		s.report.Difficulty, s.world.Counts().Objects)
	if s.opts.AreaTrigger != 0 {
		if err := s.EnterAreaTrigger(s.opts.AreaTrigger); err != nil {
			return err
		}
	}
	end := time.Now().Add(s.opts.Stay)
	for {
		if row, ok := s.takeMapChangeReport(); ok {
			if err := s.collect(ctx, mapObjectsWait); err != nil {
				return err
			}
			c := s.world.Counts()
			row.Objects = c.Objects
			row.Difficulty = s.report.Difficulty
			s.report.MapTransitions = append(s.report.MapTransitions, row)
			s.printMapChange(row)
			continue
		}
		remaining := time.Until(end)
		if remaining <= 0 {
			break
		}
		ev, err := s.next("the map check", remaining)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				break
			}
			return err
		}
		if err := s.handle(ctx, ev); err != nil {
			return err
		}
	}
	// A transition that resolved after the last loop pass still gets its line.
	if row, ok := s.takeMapChangeReport(); ok {
		row.Objects = s.world.Counts().Objects
		row.Difficulty = s.report.Difficulty
		s.report.MapTransitions = append(s.report.MapTransitions, row)
		s.printMapChange(row)
	}
	s.report.Stayed = s.opts.Stay
	s.report.Objects = s.objectSummary()
	s.opts.logf("map-check: %d map change(s), %d object(s) on map %d at the end, difficulty %d, %d map-content packet(s) read between the maps dropped, %d unsolicited transfer packet(s) ignored",
		len(s.report.MapTransitions), s.world.Counts().Objects, s.world.MapID(), s.report.Difficulty,
		s.report.StaleMapPackets, s.report.UnsolicitedTransfers)
	return nil
}

// printMapChange prints one transition and the state of the map it landed on.
func (s *session) printMapChange(row MapChangeRow) {
	if row.Aborted {
		s.opts.logf("map-check: map change aborted: map %d refused with reason %d (%s), argument %d; the character is still on map %d at %.2f %.2f %.2f (%d objects)",
			row.ToMap, row.AbortReasonID, row.AbortReason, row.AbortArg, row.FromMap,
			s.report.WalkClientPos[0], s.report.WalkClientPos[1], s.report.WalkClientPos[2], row.Objects)
		return
	}
	s.opts.logf("map-check: map change %d -> %d (reason %d), difficulty %d, own position %.2f %.2f %.2f, %d object(s) %s after the change",
		row.FromMap, row.ToMap, row.Reason, row.Difficulty, row.Position[0], row.Position[1], row.Position[2],
		row.Objects, mapObjectsWait)
}
