package world

// The full death-check run against round 2's fake world server over real sockets: the character's create and its
// death arrive as update objects, the release is answered with the ghost state and a same-map graveyard teleport,
// the client asks for the corpse, runs back, reclaims it after the announced delay and logs out alive. This is the
// one test that also proves the dispatch registration and the client wire numbers end to end.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The server wire numbers this scenario sends (client build 70707 on realm build 70707).
const (
	fakeSMSGAuraUpdate       = 0x690011
	fakeSMSGPreRessurect     = 0x470212
	fakeSMSGCorpseReclaim    = 0x4701F5
	fakeSMSGDeathReleaseLoc  = 0x47017F
	fakeSMSGCorpseLocation   = 0x4700F8
	fakeSMSGMoveSetRunSpeed  = 0x600020
	fakeSMSGCancelCombat     = 0x4D0025
	fakeSMSGStandStateUpdate = 0x4701C9
	fakeSMSGMoveWaterWalk    = 0x60002B
)

// The client acknowledgements the flow sends for the movement changes of the release.
const (
	fakeCMSGMoveForceRunSpeedAck  = 0x420029
	fakeCMSGMoveForceSwimSpeedAck = 0x42002B
	fakeCMSGMoveWaterWalkAck      = 0x42003B
)

// serveDeath is the fake's death scenario. It runs after the login burst, on the instance socket. With
// f.deathMapSc set, the release's graveyard is on map 1: the server sends the real transfer (pending, suspend, new
// world, resume), the new map's create, and then SMSG_DEATH_RELEASE_LOC there.
func (f *fakeServer) serveDeath(c *fakeConn, realm *fakeConn) {
	packed := append([]byte(nil), f.clientGUID...)
	guid := wire.ReadPackedGUID(wire.NewReader(packed))
	if guid.IsZero() {
		f.failf("death: the character list fixture carries no GUID")
		return
	}
	sc := f.deathMapSc
	crossMap := sc != nil
	// The character's own create, then its death: health 0 and a real maximum (Unit::setDeathState, Unit.cpp:9584).
	c.writeFrame(fakeSMSGUpdateObject, fakeCreateBlockType(packed, 0, 1000, 2000, 50, 7, 6))
	c.writeFrame(fakeSMSGUpdateObject, buildMaxHealthValues(guid, 60))
	c.writeFrame(fakeSMSGUpdateObject, buildHealthValues(guid, 0))
	c.writeFrame(fakeSMSGStandStateUpdate, []byte{0, 0, 0, 0, 0})
	c.writeFrame(fakeSMSGCancelCombat, nil)
	c.writeFrame(fakeSMSGCorpseReclaim, buildU32Payload(30000))

	graveyard := CorpsePoint{X: 980, Y: 1990, Z: 50, MapID: 0}
	if crossMap {
		graveyard.MapID = 1
	}
	corpse := CorpsePoint{X: 1000, Y: 2000, Z: 50, MapID: 0}
	var releasedAt time.Time
	sawAck, sawQuery, sawReclaim, sawMovement, sawStop := false, false, false, false, false
	sawSpeedAck, sawWaterAck := false, false
	for {
		code, payload, err := c.readFrame()
		if err != nil {
			select {
			case <-f.logoutDone:
				return
			default:
			}
			if f.deliberate() {
				return
			}
			f.failf("death: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, code)
		f.mu.Unlock()
		switch code {
		case fakeCMSGTimeSyncResponse:
		case fakeCMSGPing:
			c.writeFrame(fakeSMSGPong, payload[:4])
		case fakeCMSGMoveInitActiveMover:
		case fakeCMSGSetAdvancedCombatLogging:
			f.checkAdvancedLogging(payload)
		case fakeDeathCMSGRepop:
			if len(payload) != 1 || payload[0] != 0 {
				f.failf("death: CMSG_REPOP_REQUEST = % X", payload)
				return
			}
			releasedAt = time.Now()
			c.writeFrame(fakeSMSGPreRessurect, buildPreRessurect(guid))
			c.writeFrame(fakeSMSGAuraUpdate, buildAuraUpdate(guid, 2, 8326))
			c.writeFrame(fakeSMSGUpdateObject, buildHealthValues(guid, 1))
			c.writeFrame(fakeSMSGMoveSetRunSpeed, buildMoveSpeed(guid, 1, 8.75))
			c.writeFrame(fakeSMSGMoveWaterWalk, buildMoveSetFlag(guid, 4))
			c.writeFrame(fakeSMSGCorpseReclaim, buildU32Payload(400))
			if crossMap {
				// RepopAtGraveyard's far path: TeleportTo writes the pending on the realm socket, then the suspend
				// token on the instance socket, and SMSG_DEATH_RELEASE_LOC with the graveyard's own map
				// (Player.cpp:1501-1545, :5255-5264).
				sc.fromMap, sc.toMap = 0, 1
				sc.fromPos = [3]float32{1000, 2000, 50}
				sc.toPos = [3]float32{graveyard.X, graveyard.Y, graveyard.Z}
				sc.markOpen(true)
				realm.writeFrame(fakeSMSGTransferPending, pendingBytes(int32(sc.toMap), sc.toPos))
				c.writeFrame(fakeSMSGSuspendToken, append(appendU32(nil, 9), 0x40))
				continue
			}
			tp := append([]byte{}, packed...)
			tp = appendU32(tp, 9)
			tp = append(tp, fakeF32(graveyard.X)...)
			tp = append(tp, fakeF32(graveyard.Y)...)
			tp = append(tp, fakeF32(graveyard.Z)...)
			tp = append(tp, fakeF32(0)...)
			tp = append(tp, 0) // three zero bits: no transport, no vehicle, no preload
			c.writeFrame(fakeSMSGMoveTeleport, tp)
			c.writeFrame(fakeSMSGDeathReleaseLoc, buildDeathRelease(int32(graveyard.MapID), graveyard.X, graveyard.Y, graveyard.Z))
		case fakeCMSGSuspendTokenResponse:
			if !crossMap {
				f.failf("death: the client answered a suspend token in the same-map scenario")
				return
			}
			if len(payload) != 4 || binary.LittleEndian.Uint32(payload) != 9 {
				f.failf("death: CMSG_SUSPEND_TOKEN_RESPONSE = % X, want sequence 9", payload)
				return
			}
			sc.mu.Lock()
			sc.suspendAcked = true
			sc.mu.Unlock()
			realm.writeFrame(fakeSMSGUpdateLastInstance, appendU32(nil, uint32(sc.toMap)))
			realm.writeFrame(fakeSMSGNewWorld, newWorldBytes(int32(sc.toMap), sc.toPos))
			select {
			case <-sc.worldPortCh:
			case <-time.After(5 * time.Second):
				f.failf("death: the client never answered SMSG_NEW_WORLD with CMSG_WORLD_PORT_RESPONSE")
				return
			}
			c.writeFrame(fakeSMSGResumeToken, append(appendU32(nil, 9), 0x40))
			// The new map's own create (a ghost: health 1), then the active mover again, then the release's
			// location on this map.
			c.writeFrame(fakeSMSGUpdateObject, fakeCreateBlockType(packed, sc.toMap, sc.toPos[0], sc.toPos[1], sc.toPos[2], 40, 6))
			c.writeFrame(fakeSMSGUpdateObject, buildHealthValues(guid, 1))
			c.writeFrame(fakeSMSGMoveSetActive, packed)
			c.writeFrame(fakeSMSGDeathReleaseLoc, buildDeathRelease(int32(sc.toMap), sc.toPos[0], sc.toPos[1], sc.toPos[2]))
			sc.markOpen(false)
		case fakeCMSGMoveTeleportAck:
			mask := binary.LittleEndian.Uint16(payload)
			off := 2 + bits.OnesCount16(mask)
			if len(payload) < off+4 || binary.LittleEndian.Uint32(payload[off:]) != 9 {
				f.failf("death: the teleport ack does not carry sequence 9: % X", payload)
				return
			}
			sawAck = true
		case fakeDeathCMSGQueryCorpse:
			sawQuery = true
			c.writeFrame(fakeSMSGCorpseLocation, buildCorpseLocation(guid, corpse))
		case fakeDeathCMSGSetSelection:
		case fakeDeathCMSGTalkGossip:
		case fakeCMSGMoveForceRunSpeedAck:
			mi, index, speed, err := readSpeedAck(payload)
			if err != nil || index != 1 || speed != 8.75 || mi.GUID != guid {
				f.failf("death: CMSG_MOVE_FORCE_RUN_SPEED_CHANGE_ACK: %v (index %d, speed %v)", err, index, speed)
				return
			}
			sawSpeedAck = true
		case fakeCMSGMoveWaterWalkAck:
			mi, index, err := readMovementAck(payload)
			if err != nil || index != 4 || mi.GUID != guid {
				f.failf("death: CMSG_MOVE_WATER_WALK_ACK: %v (index %d)", err, index)
				return
			}
			if mi.Flags&0x04000000 == 0 {
				f.failf("death: CMSG_MOVE_WATER_WALK_ACK does not carry the water-walking flag: % X", payload)
				return
			}
			sawWaterAck = true
		case fakeCMSGMoveSetFacing, fakeCMSGMoveHeartbeat, fakeCMSGMoveStartForward:
			if crossMap && sc.isOpen() {
				f.failf("death: a movement packet went out between maps")
				return
			}
			sawMovement = true
		case fakeCMSGMoveStop:
			sawStop = true
		case fakeDeathCMSGReclaim:
			sawReclaim = true
			if releasedAt.IsZero() || time.Since(releasedAt) < 400*time.Millisecond {
				// The server's silent early refusal (MiscHandler.cpp:486-488): no answer at all.
				continue
			}
			c.writeFrame(fakeSMSGDeathReleaseLoc, buildDeathRelease(-1, 0, 0, 0))
			c.writeFrame(fakeSMSGAuraUpdate, buildAuraRemove(guid, 2))
			c.writeFrame(fakeSMSGUpdateObject, buildHealthValues(guid, 30))
		case fakeCMSGLogoutRequest:
			if crossMap {
				// The far release leaves the corpse on map 0, so the flow stops at the corpse run and logs out
				// there: the answers it owes are the query and the two movement acks, and the transfer's own.
				if !sawQuery || !sawSpeedAck || !sawWaterAck || !sc.suspendAckedNow() {
					f.failf("death: logout before the far release finished (query %v, speed ack %v, water ack %v)",
						sawQuery, sawSpeedAck, sawWaterAck)
					return
				}
				c.writeFrame(fakeSMSGLogoutResponse, []byte{0, 0, 0, 0, 0})
				time.Sleep(20 * time.Millisecond)
				realm.writeFrame(fakeSMSGLogoutComplete, []byte{0})
				f.logoutOnce.Do(func() { close(f.logoutDone) })
				time.Sleep(20 * time.Millisecond)
				c.nc.Close()
				return
			}
			if !sawAck || !sawQuery || !sawReclaim || !sawMovement || !sawStop || !sawSpeedAck || !sawWaterAck {
				f.failf("death: logout before the flow finished (ack %v, query %v, reclaim %v, movement %v, stop %v, "+
					"speed ack %v, water ack %v)", sawAck, sawQuery, sawReclaim, sawMovement, sawStop, sawSpeedAck, sawWaterAck)
				return
			}
			c.writeFrame(fakeSMSGLogoutResponse, []byte{0, 0, 0, 0, 0})
			time.Sleep(20 * time.Millisecond)
			realm.writeFrame(fakeSMSGLogoutComplete, []byte{0})
			f.logoutOnce.Do(func() { close(f.logoutDone) })
			time.Sleep(20 * time.Millisecond)
			c.nc.Close()
			return
		case fakeCMSGLogDisconnect:
			// the aborted run's best-effort disconnect after the logout request
		default:
			f.failf("death: unexpected client wire 0x%06X (%d bytes)", code, len(payload))
			return
		}
	}
}

// pendingBytes is SMSG_TRANSFER_PENDING's payload: the map, the destination, the three optional bits (MovementPackets.cpp:755-778).
func pendingBytes(mapID int32, pos [3]float32) []byte {
	b := appendU32(nil, uint32(mapID))
	for _, f := range pos {
		b = append(b, fakeF32(f)...)
	}
	return append(b, 0)
}

// newWorldBytes is SMSG_NEW_WORLD's payload: the map, the position, floor difficulty and index, the reason, the
// movement offset, the counter and the instance id (MovementPackets.cpp:799-811).
func newWorldBytes(mapID int32, pos [3]float32) []byte {
	b := appendU32(nil, uint32(mapID))
	for _, f := range []float32{pos[0], pos[1], pos[2], 0} {
		b = append(b, fakeF32(f)...)
	}
	b = appendU32(b, 0xFFFFFFFF) // floor difficulty
	b = appendU32(b, 0xFFFFFFFF) // floor index
	b = appendU32(b, objects.NewWorldReasonNormal)
	for range 3 {
		b = appendU32(b, 0)
	}
	b = appendU32(b, 1) // counter
	return appendU64(b, 0)
}

// buildMoveSetFlag is SMSG_MOVE_SET_WATER_WALK / SMSG_MOVE_SET_LAND_WALK (MoveSetFlag::Write): the mover and the
// sequence index.
func buildMoveSetFlag(guid wire.GUID, sequence uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, guid)
	b.WriteU32(sequence)
	return b.Bytes()
}

// readSpeedAck reads one CMSG_MOVE_FORCE_*_SPEED_CHANGE_ACK: a MovementInfo, the ack index and the speed.
func readSpeedAck(payload []byte) (objects.MovementInfo, uint32, float32, error) {
	b := wire.NewReader(payload)
	mi, err := objects.ReadMovementInfo(b)
	if err != nil {
		return mi, 0, 0, err
	}
	index, speed := b.ReadU32(), b.ReadFloat32()
	if b.Err() != nil || b.Remaining() != 0 {
		return mi, index, speed, fmt.Errorf("%d byte(s) left: %v", b.Remaining(), b.Err())
	}
	return mi, index, speed, nil
}

// readMovementAck reads one CMSG_MOVE_WATER_WALK_ACK: a MovementInfo and the ack index.
func readMovementAck(payload []byte) (objects.MovementInfo, uint32, error) {
	b := wire.NewReader(payload)
	mi, err := objects.ReadMovementInfo(b)
	if err != nil {
		return mi, 0, err
	}
	index := b.ReadU32()
	if b.Err() != nil || b.Remaining() != 0 {
		return mi, index, fmt.Errorf("%d byte(s) left: %v", b.Remaining(), b.Err())
	}
	return mi, index, nil
}

// buildMaxHealthValues is a UnitData values update for the max health (retail bit 39).
func buildMaxHealthValues(guid wire.GUID, max int64) []byte {
	body := unitValuesBody(39, func(v *wire.Buffer) { v.WriteU64(uint64(max)) })
	block := wire.NewWriter()
	block.WriteU8(0)
	wire.WritePackedGUID(block, guid)
	block.WriteU32(uint32(len(body)))
	block.WriteBytes(body)
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(len(block.Bytes())))
	out.WriteBytes(block.Bytes())
	return out.Bytes()
}

// TestDeathCheckAgainstFakeServer runs the whole command over real sockets: die, release, query the corpse, run
// back, reclaim, log out alive.
func TestDeathCheckAgainstFakeServer(t *testing.T) {
	f := newFakeServer(t)
	f.death = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	report, err := Enter(t.Context(), Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		DeathCheck:      true,
		Settle:          300 * time.Millisecond,
		DeathWait:       10 * time.Second,
		Deadline:        40 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		Logf: func(format string, args ...any) {
			steps = append(steps, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatalf("the death run failed: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	if !report.InWorld || !report.LogoutComplete {
		t.Fatalf("in world %v, logout complete %v", report.InWorld, report.LogoutComplete)
	}
	d := report.Death
	if !d.Revived || d.RevivedBy != "corpse" {
		t.Fatalf("the flow did not end by the corpse: %+v", d)
	}
	if d.Phase != "alive" {
		t.Fatalf("the final phase is %q", d.Phase)
	}
	if !d.Released || !d.HasCorpse {
		t.Fatalf("released %v, corpse %v", d.Released, d.HasCorpse)
	}
	if d.ReclaimDelay != 30000*time.Millisecond && d.ReclaimDelay != 400*time.Millisecond {
		t.Fatalf("the announced reclaim delay is %s", d.ReclaimDelay)
	}
	for _, want := range []string{"released the spirit", "running to the corpse", "reclaimed the corpse", "alive again"} {
		found := false
		for _, line := range steps {
			if contains(line, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no step says %q\nsteps:\n%s", want, joinLines(steps))
		}
	}
	if report.Objects.Resurrections != 1 {
		t.Fatalf("the model saw %d resurrection(s), parse errors %d, aura packets %d, types %v\nsteps:\n%s",
			report.Objects.Resurrections, report.ParseErrors, report.Counts["SMSG_AURA_UPDATE"], report.Objects.ByType, joinLines(steps))
	}
}

// TestDeathCheckReleasesToAGraveyardOnAnotherMap runs the far release end to end over the fake server's sockets: the
// character dies on map 0, the release's graveyard is on map 1, and the client follows the transfer there — the
// pending, the suspend answer, the new world, the world-port answer and the resume — and arrives as a ghost with the
// ghost's own speed. The corpse stays on map 0, so the corpse run names the entrance rule instead of walking; the
// command ends with that error and the report says where everything is.
func TestDeathCheckReleasesToAGraveyardOnAnotherMap(t *testing.T) {
	f := newFakeServer(t)
	sc := newMapScenario()
	f.death = true
	f.deathMapSc = sc
	f.clientMayLeave = true // the run ends on the corpse's map refusal and the client leaves
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	report, err := Enter(t.Context(), Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		DeathCheck:      true,
		Settle:          300 * time.Millisecond,
		DeathWait:       10 * time.Second,
		Deadline:        40 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		Logf: func(format string, args ...any) {
			steps = append(steps, fmt.Sprintf(format, args...))
		},
	})
	if !errors.Is(err, ErrCorpseOnAnotherMap) {
		t.Fatalf("the far release run ended with %v, want ErrCorpseOnAnotherMap\nsteps:\n%s", err, joinLines(steps))
	}
	if !contains(err.Error(), "walk to its entrance") {
		t.Fatalf("the error does not name the entrance rule: %v", err)
	}
	f.wait(t)
	if report.MapChanges != 1 || report.MapID != 1 {
		t.Fatalf("%d map change(s), report on map %d; want one change to map 1", report.MapChanges, report.MapID)
	}
	d := report.Death
	if d.Phase != "ghost" {
		t.Fatalf("the final phase is %q, want the ghost", d.Phase)
	}
	if !d.HasCorpse || d.Corpse.MapID != 0 {
		t.Fatalf("the corpse did not survive on map 0: %+v", d.Corpse)
	}
	for _, want := range []string{
		"the graveyard is on map 1",
		"at the graveyard on map 1",
		"run speed 8.75",
		"the corpse is on map 0",
	} {
		found := false
		for _, line := range steps {
			if contains(line, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no step says %q\nsteps:\n%s", want, joinLines(steps))
		}
	}
	sc.mu.Lock()
	open, stale := sc.open, sc.stale
	loading := sc.loadingScreen
	sc.mu.Unlock()
	if open {
		t.Fatal("the fake still has the transfer window open")
	}
	if stale != 0 {
		t.Fatalf("%d movement packet(s) went out between maps", stale)
	}
	if loading != 1 {
		t.Fatalf("the loading screen named map %d, want the graveyard's map 1", loading)
	}
}

// contains is a small substring test so the assertions above read as intent.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
