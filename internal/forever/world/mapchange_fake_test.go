package world

// The map change against the fake world server: the server sends the character's create on its map, then the
// transfer pending, the suspend token, the new world and the new map's create, and the client must answer at every
// step, clear the old map and put the mover on the new one. The abort, the map change in the middle of a walk and the
// lost link in the middle of the transition run the same harness.

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// The wire numbers of the map change, hand-computed for client build 70707 on realm build 70707.
const (
	fakeCMSGSuspendTokenResponse = 0x460008
	fakeCMSGWorldPortResponse    = 0x440025
	fakeCMSGAreaTrigger          = 0x3E008A

	fakeSMSGTransferPending    = 0x470071
	fakeSMSGSuspendToken       = 0x470041
	fakeSMSGNewWorld           = 0x47002B
	fakeSMSGTransferAborted    = 0x4701AE
	fakeSMSGUpdateLastInstance = 0x470135
	fakeSMSGWorldServerInfo    = 0x470046
	fakeSMSGInstanceInfo       = 0x4700DE
	fakeSMSGResumeToken        = 0x470042
)

// mapScenario is what the fake does for a map change, and what it observed.
type mapScenario struct {
	fromMap uint16
	toMap   uint16
	fromPos [3]float32
	toPos   [3]float32

	// abort makes the server refuse the transfer instead of moving the character: the abort scene only.
	abort       bool
	abortMap    uint32
	abortReason uint32
	abortArg    uint8
	// dropLink closes the instance socket right after the suspend token is answered.
	dropLink bool
	// walk lets the client walk before the transfer (the walk-check scene): movement packets before the transfer
	// are expected, after it they are violations.
	walk bool

	// seen, under the fake's mu
	mu            sync.Mutex
	open          bool // between the transfer pending and the new map's create
	stale         int  // movement packets while open: they carry the old map's coordinates
	suspendAcked  bool
	worldPortSeen bool
	worldPorts    int   // every world-port response, so a second map change can wait for its own
	areaTrigger   int32 // the id the client sent, -1 when none
	loadingScreen int32 // the map the client said it is loading, -1 when none
	worldPortCh   chan struct{}
}

// newMapScenario returns a scenario with its channels and the "not seen" markers.
func newMapScenario() *mapScenario {
	return &mapScenario{worldPortCh: make(chan struct{}), areaTrigger: -1, loadingScreen: -1}
}

func (m *mapScenario) markOpen(open bool) {
	m.mu.Lock()
	m.open = open
	m.mu.Unlock()
}

func (m *mapScenario) isOpen() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.open
}

// serveMapChange is the instance socket's side of the transition. The realm socket's reader answers the client's
// loading-screen notification and world-port response (see serveRealm); this side sends the tokens and the new map.
func (f *fakeServer) serveMapChange(c *fakeConn, realm *fakeConn) {
	f.mu.Lock()
	guid := append([]byte(nil), f.clientGUID...)
	sc := f.mapSc
	f.mu.Unlock()

	// The login map's self create, as serveWalk sends it.
	c.writeFrame(fakeSMSGUpdateObject, fakeCreateBlock(guid, sc.fromMap, sc.fromPos[0], sc.fromPos[1], sc.fromPos[2], 40))

	// Let the client settle (map-check's settle is 300 ms in these tests) and, in the walk scene, wait for the first
	// heartbeat: the transfer must arrive in the middle of a leg, not merely after the leg's first packets.
	if sc.walk {
		if err := sc.waitForHeartbeat(f, c); err != nil {
			f.failf("map change: %v", err)
			return
		}
	} else {
		time.Sleep(220 * time.Millisecond)
	}

	if sc.abort {
		f.sendTransferAborted(realm, sc)
		f.finishMapChange(c, realm, guid, sc)
		return
	}

	// TRANSFER_PENDING (realm, as the capture shows) and SUSPEND_TOKEN (instance).
	tp := appendU32(nil, uint32(sc.toMap))
	tp = append(tp, fakeF32(sc.toPos[0])...)
	tp = append(tp, fakeF32(sc.toPos[1])...)
	tp = append(tp, fakeF32(sc.toPos[2])...)
	tp = append(tp, 0) // the three optional bits, clear
	sc.markOpen(true)
	realm.writeFrame(fakeSMSGTransferPending, tp)
	c.writeFrame(fakeSMSGSuspendToken, append(appendU32(nil, 9), 0x40))

	// Read the client's answer to the token, then send the new world.
	deadline := time.Now().Add(5 * time.Second)
	for !sc.suspendAckedNow() {
		if time.Now().After(deadline) {
			f.failf("map change: the client never answered SMSG_SUSPEND_TOKEN")
			return
		}
		wire, payload, err := c.readFrame()
		if err != nil {
			f.failf("map change: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, wire)
		f.mu.Unlock()
		if err := sc.observeInstance(f, wire, payload); err != nil {
			f.failf("map change: %v", err)
			return
		}
	}
	if sc.dropLink {
		// The server dies in the middle of the transition: the client must report a lost link, not hang.
		f.mu.Lock()
		f.deliberateClose = true
		f.mu.Unlock()
		_ = c.nc.Close()
		return
	}

	realm.writeFrame(fakeSMSGUpdateLastInstance, appendU32(nil, uint32(sc.toMap)))
	nw := appendU32(nil, uint32(sc.toMap))
	nw = append(nw, fakeF32(sc.toPos[0])...)
	nw = append(nw, fakeF32(sc.toPos[1])...)
	nw = append(nw, fakeF32(sc.toPos[2])...)
	nw = append(nw, fakeF32(0.25)...)
	nw = appendU32(nw, 0xFFFFFFFF) // floor difficulty -1
	nw = appendU32(nw, 0xFFFFFFFF) // floor index -1
	nw = appendU32(nw, objects.NewWorldReasonNormal)
	for range 3 {
		nw = appendU32(nw, 0) // the movement offset
	}
	nw = appendU32(nw, 1) // counter
	nw = appendU64(nw, 0) // instance id
	realm.writeFrame(fakeSMSGNewWorld, nw)

	// The world-port response travels back on the realm socket: wait for it before the new map's burst.
	select {
	case <-sc.worldPortCh:
	case <-time.After(5 * time.Second):
		f.failf("map change: the client never answered SMSG_NEW_WORLD with CMSG_WORLD_PORT_RESPONSE")
		return
	}

	c.writeFrame(fakeSMSGResumeToken, append(appendU32(nil, 9), 0x40))
	// WORLD_SERVER_INFO: difficulty 1, four empty GUIDs, the group-size bit, then 5.
	wsi := make([]byte, 0, 15)
	wsi = append(wsi, 1, 0)
	wsi = append(wsi, 0, 0, 0, 0, 0, 0, 0, 0)
	wsi = append(wsi, 0x04)
	wsi = appendU32(wsi, 5)
	c.writeFrame(fakeSMSGWorldServerInfo, wsi)
	c.writeFrame(fakeSMSGInstanceInfo, appendU32(nil, 0))
	// The new map's self create, then the active mover again (which the client acks per map).
	c.writeFrame(fakeSMSGUpdateObject, fakeCreateBlock(guid, sc.toMap, sc.toPos[0], sc.toPos[1], sc.toPos[2], 40))
	c.writeFrame(fakeSMSGMoveSetActive, guid)
	sc.markOpen(false)

	f.finishMapChange(c, realm, guid, sc)
}

// finishMapChange reads the rest of the session: the client logs out (map-check) or aborts (a walk that ended with
// ErrMapChange), and the fake answers the logout so the command's own logout path runs.
func (f *fakeServer) finishMapChange(c *fakeConn, realm *fakeConn, guid []byte, sc *mapScenario) {
	for {
		wireNo, payload, err := c.readFrame()
		if err != nil {
			select {
			case <-f.logoutDone:
				return
			default:
			}
			if f.deliberate() {
				return
			}
			f.failf("map change: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, wireNo)
		f.mu.Unlock()
		switch wireNo {
		case fakeCMSGSuspendTokenResponse, fakeCMSGTimeSyncResponse, fakeCMSGPing, fakeCMSGMoveInitActiveMover:
			if err := sc.observeInstance(f, wireNo, payload); err != nil {
				f.failf("map change: %v", err)
				return
			}
		case fakeCMSGAreaTrigger:
			if err := sc.observeInstance(f, wireNo, payload); err != nil {
				f.failf("map change: %v", err)
				return
			}
		case fakeCMSGLogoutRequest:
			if sc.isOpen() {
				f.failf("map change: the client logged out between maps")
			}
			c.writeFrame(fakeSMSGLogoutResponse, []byte{0, 0, 0, 0, 0})
			time.Sleep(20 * time.Millisecond)
			realm.writeFrame(fakeSMSGLogoutComplete, []byte{0})
			f.logoutOnce.Do(func() { close(f.logoutDone) })
			time.Sleep(20 * time.Millisecond)
			c.nc.Close()
			return
		case fakeCMSGLogDisconnect:
			// the aborted walk's best-effort disconnect: the run already failed with ErrMapChange
			return
		default:
			if err := sc.observeInstance(f, wireNo, payload); err != nil {
				f.failf("map change: %v", err)
				return
			}
		}
	}
}

// waitForHeartbeat reads the walk's first packets and returns at its first heartbeat: the transfer pending then
// lands squarely inside the leg.
func (sc *mapScenario) waitForHeartbeat(f *fakeServer, c *fakeConn) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			return errors.New("the walk never sent a heartbeat")
		}
		wireNo, payload, err := c.readFrame()
		if err != nil {
			return err
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, wireNo)
		f.mu.Unlock()
		if wireNo == fakeCMSGMoveHeartbeat {
			return nil
		}
		if err := sc.observeInstance(f, wireNo, payload); err != nil {
			return err
		}
	}
}

// observeInstance records one client packet of the instance socket. A movement packet while the window is open is
// the failure this scene exists for: it would carry the old map's coordinates.
func (sc *mapScenario) observeInstance(f *fakeServer, wireNo uint32, payload []byte) error {
	switch wireNo {
	case fakeCMSGSuspendTokenResponse:
		if len(payload) != 4 || binary.LittleEndian.Uint32(payload) != 9 {
			return fmt.Errorf("CMSG_SUSPEND_TOKEN_RESPONSE = % X, want sequence 9", payload)
		}
		sc.mu.Lock()
		sc.suspendAcked = true
		sc.mu.Unlock()
	case fakeCMSGAreaTrigger:
		if len(payload) < 5 {
			return fmt.Errorf("CMSG_AREA_TRIGGER = % X", payload)
		}
		sc.mu.Lock()
		sc.areaTrigger = int32(binary.LittleEndian.Uint32(payload))
		sc.mu.Unlock()
	case fakeCMSGMoveSetFacing, fakeCMSGMoveStartForward, fakeCMSGMoveHeartbeat,
		fakeCMSGMoveStop, fakeCMSGMoveTeleportAck:
		if sc.isOpen() {
			sc.mu.Lock()
			sc.stale++
			sc.mu.Unlock()
			return fmt.Errorf("a movement packet (wire 0x%06X) went out between maps", wireNo)
		}
		if !sc.walk {
			return fmt.Errorf("a movement packet (wire 0x%06X) went out in a run that does not walk", wireNo)
		}
	}
	return nil
}

func (sc *mapScenario) suspendAckedNow() bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.suspendAcked
}

// worldPortCount is how many world-port responses the realm reader saw, for a helper that waits on its own one.
func (sc *mapScenario) worldPortCount() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.worldPorts
}

// sendTransferAborted sends SMSG_TRANSFER_ABORTED as the server's writer lays it out (MovementPackets.cpp:779-787).
func (f *fakeServer) sendTransferAborted(realm *fakeConn, sc *mapScenario) {
	b := appendU32(nil, sc.abortMap)
	b = append(b, sc.abortArg)
	b = appendU32(b, 0) // the map-difficulty condition id
	// the reason in six bits, flushed into a byte
	b = append(b, byte(sc.abortReason<<2))
	realm.writeFrame(fakeSMSGTransferAborted, b)
}

// serveRealmMapCase answers one client packet of a map change on the realm socket; it reports whether it consumed
// the opcode.
func (f *fakeServer) serveRealmMapCase(c *fakeConn, wireNo uint32, payload []byte) bool {
	sc := f.mapSc
	if sc == nil {
		// The death flow's far release answers the same two realm-side packets; its scenario is the same shape.
		sc = f.deathMapSc
	}
	if sc == nil {
		sc = f.fbdMapSc
	}
	if sc == nil {
		return false
	}
	switch wireNo {
	case fakeCMSGLoadingScreenNotify:
		if len(payload) < 4 {
			f.failf("map change: CMSG_LOADING_SCREEN_NOTIFY = % X", payload)
			return true
		}
		sc.mu.Lock()
		sc.loadingScreen = int32(binary.LittleEndian.Uint32(payload))
		sc.mu.Unlock()
	case fakeCMSGWorldPortResponse:
		if len(payload) != 0 {
			f.failf("map change: CMSG_WORLD_PORT_RESPONSE carries % X, want nothing", payload)
			return true
		}
		sc.mu.Lock()
		seen := sc.worldPortSeen
		sc.worldPortSeen = true
		sc.worldPorts++
		sc.mu.Unlock()
		if !seen {
			close(sc.worldPortCh)
		}
	default:
		return false
	}
	return true
}

// mapCheckOptions is a map-check run against the fake.
func mapCheckOptions(f *fakeServer, stay time.Duration) Options {
	return Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		MapCheck:        true,
		Settle:          300 * time.Millisecond,
		Stay:            stay,
		Deadline:        30 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		Logf:            func(string, ...any) {},
	}
}

// TestMapCheckFollowsAFarTeleport is the transition end to end: the character leaves map A, lands on map B, the old
// map's objects are gone, the mover is at the new position, and the client answered the suspend token and the world
// port response while sending no movement packet in between.
func TestMapCheckFollowsAFarTeleport(t *testing.T) {
	f := newFakeServer(t)
	sc := newMapScenario()
	sc.fromMap, sc.toMap = 0, 389
	sc.fromPos = [3]float32{1000, 2000, 50}
	sc.toPos = [3]float32{1010, 2020, 55}
	f.mapSc = sc
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := mapCheckOptions(f, 800*time.Millisecond)
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("the map-check run failed: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	if report.MapChanges != 1 || report.AbortedTransfers != 0 {
		t.Fatalf("transfers %d, aborts %d; want one resolved change", report.MapChanges, report.AbortedTransfers)
	}
	if len(report.MapTransitions) != 1 {
		t.Fatalf("%d transition row(s), want one:\n%s", len(report.MapTransitions), joinLines(steps))
	}
	row := report.MapTransitions[0]
	if row.FromMap != 0 || row.ToMap != 389 || row.Aborted {
		t.Fatalf("the transition row is %+v", row)
	}
	if row.Difficulty != 1 {
		t.Fatalf("the difficulty is %d, want the world server info's 1", row.Difficulty)
	}
	if row.Objects == 0 {
		t.Fatalf("the new map counted no objects:\n%s", joinLines(steps))
	}
	if report.MapID != 389 {
		t.Fatalf("the report is on map %d, want 389", report.MapID)
	}
	if !samePoint([4]float32{report.WalkClientPos[0], report.WalkClientPos[1], report.WalkClientPos[2], 0}, [4]float32{1010, 2020, 55, 0}) {
		t.Fatalf("the client believes it is at %.2f %.2f %.2f, want the new world's position", report.WalkClientPos[0], report.WalkClientPos[1], report.WalkClientPos[2])
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !sc.suspendAcked || !sc.worldPortSeen {
		t.Fatalf("suspend answered %v, world port answered %v", sc.suspendAcked, sc.worldPortSeen)
	}
	if sc.loadingScreen != 389 {
		t.Fatalf("CMSG_LOADING_SCREEN_NOTIFY named map %d, want the destination 389", sc.loadingScreen)
	}
	if sc.stale != 0 {
		t.Fatalf("%d movement packet(s) went out between the maps", sc.stale)
	}
	if !containsLine(steps, "world model is cleared") {
		t.Fatalf("the log does not say the model was cleared:\n%s", joinLines(steps))
	}
}

// TestMapCheckFollowsAGroupIntoAnInstance is the second half of the capture's scene: the same transition into a
// dungeon and back, ending on the login map with the old instance's objects gone.
func TestMapCheckFollowsAGroupIntoAnInstance(t *testing.T) {
	f := newFakeServer(t)
	sc := newMapScenario()
	sc.fromMap, sc.toMap = 1, 389
	sc.fromPos = [3]float32{1000, 2000, 50}
	sc.toPos = [3]float32{1005, 2005, 51}
	f.mapSc = sc
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	report, err := Enter(t.Context(), mapCheckOptions(f, 600*time.Millisecond))
	if err != nil {
		t.Fatalf("the map-check run failed: %v", err)
	}
	f.wait(t)
	if report.MapID != 389 || len(report.MapTransitions) != 1 || report.MapTransitions[0].Objects == 0 {
		t.Fatalf("the instance entry: map %d, transitions %+v", report.MapID, report.MapTransitions)
	}
	if report.Objects.Objects != 1 {
		// The model holds exactly the new map's own create; anything of map 1 would be a survivor.
		t.Fatalf("the model holds %d objects after the reset, want the new create only: %+v", report.Objects.Objects, report.Objects)
	}
}

// TestMapCheckReportsAnAbortedTransfer: the server refuses the transfer (the level requirement of a dungeon, as the
// live run measures it), and the client reports the reason by name, keeps its map and its position, and never sends
// the world-port response.
func TestMapCheckReportsAnAbortedTransfer(t *testing.T) {
	f := newFakeServer(t)
	sc := newMapScenario()
	sc.fromMap, sc.toMap = 0, 389
	sc.fromPos = [3]float32{1000, 2000, 50}
	sc.toPos = [3]float32{1010, 2020, 55}
	sc.abort = true
	sc.abortMap = 389
	sc.abortReason = 7 // TRANSFER_ABORT_INSUF_EXPAN_LVL
	sc.abortArg = 0
	f.mapSc = sc
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := mapCheckOptions(f, 500*time.Millisecond)
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("the map-check run failed: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	if report.AbortedTransfers != 1 || len(report.MapTransitions) != 1 {
		t.Fatalf("aborts %d, transitions %d; want one abort", report.AbortedTransfers, len(report.MapTransitions))
	}
	row := report.MapTransitions[0]
	if !row.Aborted || row.AbortReason != "INSUF_EXPAN_LVL" || row.ToMap != 389 {
		t.Fatalf("the abort row is %+v", row)
	}
	if report.MapID != 0 {
		t.Fatalf("the report says map %d after the abort, want the login map", report.MapID)
	}
	if !samePoint([4]float32{report.WalkClientPos[0], report.WalkClientPos[1], report.WalkClientPos[2], 0}, [4]float32{1000, 2000, 50, 0}) {
		t.Fatalf("the client moved to %.2f %.2f %.2f during the abort", report.WalkClientPos[0], report.WalkClientPos[1], report.WalkClientPos[2])
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.worldPortSeen {
		t.Fatal("the client answered a world port response for a transfer that never started")
	}
	if !containsLine(steps, "INSUF_EXPAN_LVL") {
		t.Fatalf("the log does not name the abort reason:\n%s", joinLines(steps))
	}
}

// TestMapChangeDuringAWalkEndsIt: walk-check is walking when the transfer pending arrives; the walk ends with
// ErrMapChange, the client sends no heartbeat or stop from the old map's coordinates, and the log says so.
func TestMapChangeDuringAWalkEndsIt(t *testing.T) {
	f := newFakeServer(t)
	sc := newMapScenario()
	sc.fromMap, sc.toMap = 0, 1
	sc.fromPos = [3]float32{1000, 2000, 50}
	sc.toPos = [3]float32{3000, 4000, 60}
	sc.walk = true
	f.mapSc = sc
	f.clientMayLeave = true // the walk ends on ErrMapChange and the client leaves
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		WalkCheck:       true,
		Dx:              200, // 28 s at the run speed: the transfer lands in the middle of the leg
		Dy:              0,
		Settle:          100 * time.Millisecond,
		WalkPause:       50 * time.Millisecond,
		Deadline:        30 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		Logf:            func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) },
	}
	_, err := Enter(t.Context(), opts)
	if !errors.Is(err, ErrMapChange) {
		t.Fatalf("the walk run ended with %v, want ErrMapChange\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.stale != 0 {
		t.Fatalf("%d movement packet(s) went out between the maps", sc.stale)
	}
}

// TestLinkLostDuringTheTransition: the server drops the instance socket after the suspend token. The client must
// report a lost link (not a hang or a map change), and the run ends promptly.
func TestLinkLostDuringTheTransition(t *testing.T) {
	f := newFakeServer(t)
	sc := newMapScenario()
	sc.fromMap, sc.toMap = 0, 389
	sc.fromPos = [3]float32{1000, 2000, 50}
	sc.toPos = [3]float32{1010, 2020, 55}
	sc.dropLink = true
	f.mapSc = sc
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	start := time.Now()
	report, err := Enter(t.Context(), mapCheckOptions(f, 500*time.Millisecond))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("the run survived a link that died between maps")
	}
	if !errors.Is(err, ErrLinkLost) {
		t.Fatalf("the run ended with %v, want ErrLinkLost", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("the lost link took %s to report", elapsed.Round(time.Millisecond))
	}
	if report != nil && report.MapID == 389 {
		t.Fatal("the client claims the new map after a link that died before the new world")
	}
	f.wait(t)
}

// TestMapChangeDuringAFightEndsIt: a fight is chasing its target when the transfer pending arrives; Attack returns
// ErrMapChange and no movement packet follows the pending.
func TestMapChangeDuringAFightEndsIt(t *testing.T) {
	f := newFightWorld(t, 6)
	s := f.session()
	tp := appendU32(nil, 389)
	tp = append(tp, fakeF32(1010)...)
	tp = append(tp, fakeF32(2020)...)
	tp = append(tp, fakeF32(55)...)
	tp = append(tp, 0)
	injected := false
	f.beforeWait = func(time.Time) {
		if injected {
			return
		}
		injected = true
		if err := s.handle(context.Background(), event{p: worldconn.Packet{Core: wire.SMSGTransferPending, Payload: tp}}); err != nil {
			t.Logf("the transfer pending: %v", err)
		}
	}
	err := s.Attack(context.Background(), f.target)
	if !errors.Is(err, ErrMapChange) {
		t.Fatalf("the fight ended with %v, want ErrMapChange", err)
	}
	// Nothing but the transfer pending may have followed the injection: the attack was still chasing.
	transferAt := len(f.sent)
	for _, p := range f.sent {
		if p.core == wire.CMSGLoadingScreenNotify {
			transferAt = min(transferAt, indexOfSent(f.sent, p))
		}
	}
	for _, p := range f.sent[transferAt:] {
		switch p.core {
		case wire.CMSGMoveSetFacing, wire.CMSGMoveStartForward, wire.CMSGMoveHeartbeat, wire.CMSGMoveStop:
			t.Fatalf("%s went out after the transfer pending", wire.ClientName(p.core))
		}
	}
}

// indexOfSent is the index of the first packet equal to p (by core and time), for the injection's position.
func indexOfSent(sent []fightSent, p fightSent) int {
	for i := range sent {
		if sent[i].core == p.core && sent[i].at.Equal(p.at) {
			return i
		}
	}
	return -1
}

// TestMapCheckSendsTheAreaTrigger: --area-trigger sends the client packet once after the settle, with the id the
// caller named, and the area trigger bits the capture shows.
func TestMapCheckSendsTheAreaTrigger(t *testing.T) {
	f := newFakeServer(t)
	sc := newMapScenario()
	sc.fromMap, sc.toMap = 1, 1
	sc.fromPos = [3]float32{1000, 2000, 50}
	sc.toPos = [3]float32{1000, 2000, 50}
	sc.abort = true // no transition: the trigger is the only thing the scene needs to see
	sc.abortMap = 389
	sc.abortReason = 1
	f.mapSc = sc
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	opts := mapCheckOptions(f, 400*time.Millisecond)
	opts.AreaTrigger = 2230
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("the map-check run failed: %v", err)
	}
	f.wait(t)
	if report.AreaTriggers != 1 {
		t.Fatalf("%d area trigger(s) sent, want one", report.AreaTriggers)
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.areaTrigger != 2230 {
		t.Fatalf("the area trigger id is %d, want 2230", sc.areaTrigger)
	}
}

// TestMapCheckLoadingScreenMatchesTheCapture checks the client packet against the captured bytes (the fixture lives
// in the objects package, where the capture cut it).
func TestMapCheckLoadingScreenMatchesTheCapture(t *testing.T) {
	blob, err := os.ReadFile("../objects/testdata/transfer.json")
	if err != nil {
		t.Fatalf("reading the transfer fixtures: %v", err)
	}
	var doc struct {
		Packets []struct {
			Name      string `json:"name"`
			Direction string `json:"direction"`
			Payload   string `json:"payload"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatalf("parsing the transfer fixtures: %v", err)
	}
	for _, p := range doc.Packets {
		if p.Name != "loading_screen_notify_dungeon" {
			continue
		}
		want, err := hex.DecodeString(p.Payload)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		got := BuildLoadingScreenNotifyShowing(389, true)
		if len(got) != len(want) || string(got) != string(want) {
			t.Fatalf("CMSG_LOADING_SCREEN_NOTIFY: the writer produced % X, the client sent % X", got, want)
		}
		return
	}
	t.Fatal("the transfer fixtures carry no loading-screen packet")
}

// containsLine reports whether any step contains what.
func containsLine(steps []string, what string) bool {
	for _, s := range steps {
		if len(s) >= len(what) && (s == what || indexOf(s, what) >= 0) {
			return true
		}
	}
	return false
}

func indexOf(s, what string) int {
	for i := 0; i+len(what) <= len(s); i++ {
		if s[i:i+len(what)] == what {
			return i
		}
	}
	return -1
}

// nopSender is a transport that keeps no packets, for a session without sockets.
type nopSender struct{}

func (n *nopSender) Send(uint32, []byte) error                  { return nil }
func (n *nopSender) WaitUntil(context.Context, time.Time) error { return nil }
func (n *nopSender) Now() time.Time                             { return time.Unix(0, 0) }

// TestNewWorldDropsTheNavmeshPlanner: the planner caches Detour meshes per map, so a map change frees it and the next
// route builds a new one (with the same parent rule). The session's planner is the only state that must follow the
// map here; the fake world server is not involved.
func TestNewWorldDropsTheNavmeshPlanner(t *testing.T) {
	dir := t.TempDir()
	s := &session{
		opts:        Options{NavmeshDir: dir, Parents: map[uint32]uint32{389: 1}},
		ctx:         context.Background(),
		world:       objects.NewWorld(0),
		report:      &Report{Counts: map[string]int{}},
		fightSender: &nopSender{},
	}
	first, err := s.navPlanner()
	if err != nil {
		t.Fatalf("building the planner: %v", err)
	}
	if first == nil {
		t.Fatal("no planner was built")
	}
	nw := appendU32(nil, 389)
	nw = append(nw, fakeF32(1000)...)
	nw = append(nw, fakeF32(2000)...)
	nw = append(nw, fakeF32(50)...)
	nw = append(nw, fakeF32(0.5)...)
	nw = appendU32(nw, 0xFFFFFFFF)
	nw = appendU32(nw, 0xFFFFFFFF)
	nw = appendU32(nw, objects.NewWorldReasonNormal)
	for range 3 {
		nw = appendU32(nw, 0)
	}
	nw = appendU32(nw, 1)
	nw = appendU64(nw, 0)
	// The new world is only legal once the transfer is suspended (the server sends it in answer to the suspend
	// token, MovementHandler.cpp:397-421): pending, suspend, new world.
	tp := appendU32(nil, 389)
	tp = append(tp, fakeF32(1000)...)
	tp = append(tp, fakeF32(2000)...)
	tp = append(tp, fakeF32(50)...)
	tp = append(tp, 0) // the three optional bits, clear
	if err := s.handle(context.Background(), event{p: worldconn.Packet{Core: wire.SMSGTransferPending, Payload: tp}}); err != nil {
		t.Fatalf("the transfer pending: %v", err)
	}
	if err := s.handle(context.Background(), event{p: worldconn.Packet{Core: wire.SMSGSuspendToken, Payload: append(appendU32(nil, 9), 0x40)}}); err != nil {
		t.Fatalf("the suspend token: %v", err)
	}
	if err := s.handle(context.Background(), event{p: worldconn.Packet{Core: wire.SMSGNewWorld, Payload: nw}}); err != nil {
		t.Fatalf("the new world: %v", err)
	}
	if s.planner != nil {
		t.Fatal("the planner of the old map survived the new world")
	}
	second, err := s.navPlanner()
	if err != nil {
		t.Fatalf("rebuilding the planner: %v", err)
	}
	if second == first {
		t.Fatal("the planner was not rebuilt for the new map")
	}
	s.dropPlanner()
}
