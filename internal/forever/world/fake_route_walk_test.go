package world

// The route-walk fake-server scenarios: a scripted server that stores the position of every movement packet and
// answers the core `.gps` line with it. By mode it confirms every move, freezes the stored position after a chosen
// number of moves (STUCK), shifts X (DEVIATED) or drops Z (FELL); the liquid case is a navmesh fixture, not a server
// answer.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
	"os"
)

// serveRouteWalk runs the instance socket for `forever route-walk`: it sends the character's own create block at
// routeWalkStart, stores the position of each movement packet as the server's position (until routeWalkFreezeAfter
// packets in "stuck" mode), answers `.gm`/`.gm on`/`.gm off` and `.gps`, and logs out on request.
func (f *fakeServer) serveRouteWalk(c *fakeConn, realm *fakeConn) {
	f.mu.Lock()
	guid := append([]byte(nil), f.clientGUID...)
	start := f.routeWalkStart
	f.routeWalkPos = start
	mode := f.routeWalkMode
	f.mu.Unlock()
	if mode == "" {
		mode = "confirm"
	}
	c.writeFrame(fakeSMSGUpdateObject, fakeCreateBlock(guid, f.mapID, start[0], start[1], start[2], 40))

	for {
		wireNum, payload, err := c.readFrame()
		if err != nil {
			select {
			case <-f.logoutDone:
				return
			default:
			}
			if f.deliberate() {
				return
			}
			f.failf("route-walk: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, wireNum)
		f.mu.Unlock()
		switch wireNum {
		case fakeCMSGTimeSyncResponse:
		case fakeCMSGPing:
			c.writeFrame(fakeSMSGPong, payload[:4])
		case fakeCMSGMoveInitActiveMover:
		case fakeCMSGSetSelection:
			// The client selects its own character before `.gps`, exactly as the real client does when the user
			// clicks it. The pack must name this character.
			if got, want := mustGUID(payload), mustGUID(guid); got != want {
				f.failf("route-walk: CMSG_SET_SELECTION names %v, want the character %v", got, want)
				return
			}
			f.mu.Lock()
			f.routeWalkSelects++
			f.mu.Unlock()
		case fakeCMSGMoveSetFacing, fakeCMSGMoveStartForward, fakeCMSGMoveHeartbeat, fakeCMSGMoveStop:
			mi, err := parseFakeMovementInfo(payload)
			if err != nil {
				f.failf("route-walk: movement: %v", err)
				return
			}
			f.mu.Lock()
			f.routeWalkSeen++
			// "stuck" stores the first routeWalkFreezeAfter packets and then stops storing anything: 0 rejects every
			// move (the position never leaves the start).
			if mode != "stuck" || f.routeWalkSeen <= f.routeWalkFreezeAfter {
				f.routeWalkPos = mi
			}
			f.mu.Unlock()
		case fakeCMSGMoveTeleportAck:
		case fakeCMSGChatSay:
			lang, secure, text, err := parseClientChatMessage(payload)
			if err != nil {
				f.failf("route-walk: SAY: %v", err)
				return
			}
			if !secure || lang != chat.LanguageCommon {
				f.failf("route-walk: SAY secure=%v language=%d", secure, lang)
				return
			}
			f.mu.Lock()
			f.instChats = append(f.instChats, text)
			f.mu.Unlock()
			if !f.answerRouteWalk(c, text, mode) {
				return
			}
		case fakeCMSGChatWhisper:
			// The `.gps` marker: a whisper to the character itself, answered by the server with the whisper from that
			// character. Player::Whisper fills SenderName and sends the line to the receiver before the inform copy,
			// so the echo below is the receiver's packet (Player.cpp:23706-23732).
			target, text, err := parseClientWhisper(payload)
			if err != nil {
				f.failf("route-walk: WHISPER: %v", err)
				return
			}
			if target == "" {
				f.failf("route-walk: WHISPER: an empty target")
				return
			}
			self := mustGUID(guid)
			c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeWhisper, chat.LanguageUniversal, self, self, target, target, "", "", text))
		case fakeCMSGLogDisconnect:
			return
		case fakeCMSGLogoutRequest:
			c.writeFrame(fakeSMSGLogoutResponse, []byte{0, 0, 0, 0, 0})
			time.Sleep(20 * time.Millisecond) // the completion travels on the other socket
			realm.writeFrame(fakeSMSGLogoutComplete, []byte{0})
			f.logoutOnce.Do(func() { close(f.logoutDone) })
			time.Sleep(20 * time.Millisecond)
			c.nc.Close()
			return
		default:
			f.failf("route-walk: unexpected client wire 0x%06X", wireNum)
			return
		}
	}
}

// answerRouteWalk answers one command line. It returns false when it ended the connection.
func (f *fakeServer) answerRouteWalk(c *fakeConn, text, mode string) bool {
	switch text {
	case ".gm":
		for _, line := range gmHelp() {
			f.sysChat(c, line)
		}
		return true
	case ".gm on", ".gm off":
		f.sysChat(c, text+" done")
		return true
	case ".gps":
		if mode == "nogps" {
			// The core's hidden-command answer (ChatCommand.cpp:472-478): the account lacks RBAC permission 505.
			f.sysChat(c, "Command 'gps' does not exist")
			return true
		}
		f.mu.Lock()
		f.routeWalkGPSReads++
		reads := f.routeWalkGPSReads
		pos := f.routeWalkPos
		last, lastSet := f.routeWalkLastGPS, f.routeWalkLastGPSSet
		f.mu.Unlock()
		if reads == f.routeWalkSilentAt {
			// A block without its coordinates: no confirmation.
			f.sysChat(c, fmt.Sprintf("Map: %d (test map)", f.mapID))
			return true
		}
		if f.routeWalkStaleMoved > 0 && !f.routeWalkStaleUsed && lastSet && dist3f3(pos, last) > f.routeWalkStaleMoved {
			// The server was slow: the answer to the previous request is written now, after this request arrived, and
			// it names where the character stood before it walked. The request's own answer follows on the next read.
			f.mu.Lock()
			f.routeWalkStaleUsed = true
			f.mu.Unlock()
			pos = last
		}
		switch mode {
		case "deviate":
			pos[0] += 4
		case "fall":
			pos[2] -= 2.5
		}
		if f.routeWalkFlip && reads >= 2 {
			// Two consecutive reads that disagree: neither is the stop's own position.
			if reads%2 == 0 {
				pos[0] += 4
			} else {
				pos[0] += 6
			}
		}
		f.mu.Lock()
		f.routeWalkLastGPS, f.routeWalkLastGPSSet = pos, true
		f.mu.Unlock()
		// The core's `.gps` (cs_misc.cpp:275-307): the map line, the coordinates line, then the grid line whose
		// own "Map: 1" must not be taken for the position's map.
		f.sysChat(c, fmt.Sprintf("Map: %d (test map)", f.mapID))
		f.sysChat(c, fmt.Sprintf("X: %.6f Y: %.6f Z: %.6f Orientation: 0.000000", pos[0], pos[1], pos[2]))
		f.sysChat(c, "grid[32,31]cell[0,7] InstanceID: 0")
		f.sysChat(c, "GroundZ: 50.000000 FloorZ: 50.000000 Have height data (Map: 1 VMap: 1)")
		return true
	}
	f.failf("route-walk: unexpected command %q", text)
	return false
}

// routeWalkPlan writes a plan for the route-walk tests: map 0, the given legs on the navfixture mesh at z 50.
func routeWalkPlan(t *testing.T, legs ...dungeon.Point) *dungeon.Plan {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, `{"version":1,"name":"walk-test","kind":"dungeon","map":0,"entrance":{"x":1002,"y":2002,"z":50},"legs":[`)
	for i, p := range legs {
		if i > 0 {
			b.WriteByte(',')
		}
		// The plan loader refuses a leg that only walks to its To; a minimal pack the walk ignores keeps the leg
		// a leg. route-walk never pulls: it only walks the leg's To (and its via points).
		fmt.Fprintf(&b, `{"name":"leg%d","to":{"x":%g,"y":%g,"z":%g},"pull_groups":[{"entries":[1],"positions":[{"x":%g,"y":%g,"z":%g}]}]}`,
			i, p.X, p.Y, p.Z, p.X, p.Y, p.Z)
	}
	b.WriteString(`]}`)
	path := t.TempDir() + "/plan.json"
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := dungeon.Load(path)
	if err != nil {
		t.Fatalf("the test plan is refused: %v", err)
	}
	return plan
}

// routeWalkMesh is the L-shaped Forever mesh the walk tests use: east along y 2000..2004 from x 1000 to 1012,
// north to y 2012 at x 1008..1012. extra adds polygons over it (the liquid case's magma overlay).
func routeWalkMesh(t *testing.T, extra ...navfixture.Poly) string {
	t.Helper()
	polys := []navfixture.Poly{
		navfixture.Square(1000, 2000, 1004, 2004, 50),
		navfixture.Square(1004, 2000, 1008, 2004, 50),
		navfixture.Square(1008, 2000, 1012, 2004, 50),
		navfixture.Square(1008, 2004, 1012, 2008, 50),
		navfixture.Square(1008, 2008, 1012, 2012, 50),
	}
	flags := make([]uint16, 0, len(polys)+len(extra))
	for range polys {
		flags = append(flags, 0x01)
	}
	polys = append(polys, extra...)
	for range extra {
		flags = append(flags, 0x08) // ForeNav: magma/slime
	}
	return navfixture.WriteForeverDirAt(t, 0, polys, flags, 1000, 2000)
}

// routeWalkOptions is the login both session tests share.
func (f *fakeServer) routeWalkOptions(plan *dungeon.Plan, navmeshDir string, rw *RouteWalkOptions) Options {
	rw.Plan = plan
	if rw.Settle <= 0 {
		rw.Settle = 50 * time.Millisecond
	}
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
		Deadline:        30 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		NavmeshDir:      navmeshDir,
		Settle:          50 * time.Millisecond,
		RouteWalk:       rw,
		Logf:            func(string, ...any) {},
	}
}

// runRouteWalk starts a fake in the route-walk mode and enters it. The fake is returned for its counters.
func runRouteWalk(t *testing.T, mode string, freezeAfter int, start [3]float32, plan *dungeon.Plan, dir string, rw *RouteWalkOptions) (*fakeServer, *Report, error) {
	t.Helper()
	f := newFakeServer(t)
	f.routeWalk = true
	f.routeWalkMode = mode
	f.routeWalkFreezeAfter = freezeAfter
	f.routeWalkStart = start
	t.Cleanup(f.close)
	f.wg.Add(1)
	go f.serveRealm()
	opts := f.routeWalkOptions(plan, dir, rw)
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	return f, report, err
}

// TestRouteWalkAgainstFakeServer is the happy path: two legs on the fixture mesh, every confirmed position equal to
// the planned point, and the second leg planning from where the first actually ended.
func TestRouteWalkAgainstFakeServer(t *testing.T) {
	plan := routeWalkPlan(t,
		dungeon.Point{X: 1010, Y: 2002, Z: 50},
		dungeon.Point{X: 1010, Y: 2010, Z: 50},
	)
	dir := routeWalkMesh(t)
	_, report, err := runRouteWalk(t, "confirm", 0, [3]float32{1002, 2002, 50}, plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 2,
		StuckAfter:   2 * time.Second,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	rw := report.RouteWalk
	if rw == nil || len(rw.Legs) != 2 {
		t.Fatalf("report: %+v", rw)
	}
	if rw.Verdict != RoutePass {
		t.Fatalf("verdict %s: %+v", rw.Verdict, rw.Legs)
	}
	for _, leg := range rw.Legs {
		if leg.Status != RoutePass {
			t.Fatalf("leg %d %s: %v", leg.Index, leg.Status, leg.Issues)
		}
		if len(leg.Waypoints) == 0 {
			t.Fatalf("leg %d has no waypoints", leg.Index)
		}
		if leg.PlannedYd <= 0 || leg.WalkedYd <= 0 {
			t.Fatalf("leg %d: planned %.2f, walked %.2f", leg.Index, leg.PlannedYd, leg.WalkedYd)
		}
		for _, wp := range leg.Waypoints {
			if wp.DeviationYd > 0.05 {
				t.Fatalf("leg %d: the server confirmed %.2f yd from the planned point: %+v", leg.Index, wp.DeviationYd, wp)
			}
			if !wp.At.Equal(time.Time{}) && wp.AtS <= 0 {
				t.Fatalf("leg %d: a waypoint has no time", leg.Index)
			}
		}
	}
	// The second leg starts where the first actually ended, not at the plan's own point.
	last := rw.Legs[0].Waypoints[len(rw.Legs[0].Waypoints)-1].Server
	from2 := rw.Legs[1].From
	if d := dist3f3(from2, last); d > 0.01 {
		t.Fatalf("leg 2 starts at %.2f %.2f %.2f, the server confirmed %.2f %.2f %.2f (%.2f yd)", from2[0], from2[1], from2[2], last[0], last[1], last[2], d)
	}
	if report.RouteWalk.Entry != "already on the instance map" {
		t.Fatalf("entry: %q", report.RouteWalk.Entry)
	}
}

// TestRouteWalkStuck: the server stops storing movement after a few packets; the leg is STUCK at the position it
// froze on, and the run goes on to the next leg (which is stuck too, from that same position).
func TestRouteWalkStuck(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1012, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	start := [3]float32{1000, 2002, 50}
	_, report, err := runRouteWalk(t, "stuck", 2, start, plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 50, // the stall is the finding; the growing distance from the frozen point is not
		StuckAfter:   150 * time.Millisecond,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	leg := report.RouteWalk.Legs[0]
	if leg.Status != RouteStuck {
		t.Fatalf("leg status %s, want STUCK: %v", leg.Status, leg.Issues)
	}
	last := leg.Waypoints[len(leg.Waypoints)-1]
	if last.Server != [3]float32{start[0], start[1], start[2]} {
		t.Fatalf("the stuck waypoint is at %v, want the frozen position %v", last.Server, start)
	}
	if !strings.Contains(strings.ToLower(leg.Reason), "did not move") {
		t.Fatalf("reason %q does not say the position stalled", leg.Reason)
	}
}

// TestRouteWalkRejectsMoves: the server stores no movement packet at all (a root, a stun, a transfer): the client
// walks, the confirmed position never leaves the start, and the leg is STUCK there.
func TestRouteWalkRejectsMoves(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1012, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	start := [3]float32{1000, 2002, 50}
	_, report, err := runRouteWalk(t, "stuck", 0, start, plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 50, // the rejection is the finding; the growing distance from the frozen point is not
		StuckAfter:   150 * time.Millisecond,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	leg := report.RouteWalk.Legs[0]
	if leg.Status != RouteStuck {
		t.Fatalf("leg status %s, want STUCK: %v", leg.Status, leg.Issues)
	}
	for _, wp := range leg.Waypoints {
		if wp.Server != [3]float32{start[0], start[1], start[2]} {
			t.Fatalf("the server confirmed a move to %v although it stored none", wp.Server)
		}
	}
}

// TestRouteWalkFalls: the server confirms a Z below the planned point; the leg is FELL with that position.
func TestRouteWalkFalls(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1010, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	_, report, err := runRouteWalk(t, "fall", 0, [3]float32{1002, 2002, 50}, plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 5, // above the 2.5-yd drop, so the deviation is not the finding
		StuckAfter:   2 * time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	leg := report.RouteWalk.Legs[0]
	if leg.Status != RouteFell {
		t.Fatalf("leg status %s, want FELL: %v", leg.Status, leg.Issues)
	}
	last := leg.Waypoints[len(leg.Waypoints)-1]
	if last.Dz > -2.4 {
		t.Fatalf("the fall waypoint's dz is %.2f, want about -2.5", last.Dz)
	}
}

// TestRouteWalkDeviates: the server confirms a position 4 yd east of the planned point; the leg is DEVIATED.
func TestRouteWalkDeviates(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1010, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	_, report, err := runRouteWalk(t, "deviate", 0, [3]float32{1002, 2002, 50}, plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 2,
		StuckAfter:   2 * time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	leg := report.RouteWalk.Legs[0]
	if leg.Status != RouteDeviated {
		t.Fatalf("leg status %s, want DEVIATED: %v", leg.Status, leg.Issues)
	}
	if leg.MaxDev < 3.9 {
		t.Fatalf("max deviation %.2f, want the server's 4-yd offset", leg.MaxDev)
	}
}

// TestRouteWalkLiquid: a magma polygon covers part of the planned corridor in the fixture mesh. The leg is LIQUID at
// the planned point before the character walks into it, and no movement packet was sent.
func TestRouteWalkLiquid(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1010, Y: 2002, Z: 50})
	dir := routeWalkMesh(t, navfixture.Square(1004, 2000, 1008, 2004, 50.5))
	_, report, err := runRouteWalk(t, "confirm", 0, [3]float32{1002, 2002, 50}, plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 2,
		StuckAfter:   2 * time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	leg := report.RouteWalk.Legs[0]
	if leg.Status != RouteLiquid {
		t.Fatalf("leg status %s, want LIQUID: %v", leg.Status, leg.Issues)
	}
	if !strings.Contains(leg.Reason, "magma/slime") {
		t.Fatalf("reason %q does not name the magma", leg.Reason)
	}
	if len(leg.Waypoints) != 0 {
		t.Fatalf("the leg sent %d movement confirmations into the liquid", len(leg.Waypoints))
	}
}

// TestRouteWalkGPShidden: a server whose account cannot run `.gps` answers the hidden-command refusal; the run stops
// at once and names the alternative, instead of recording client positions as server ones.
func TestRouteWalkGPShidden(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1010, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	_, _, err := runRouteWalk(t, "nogps", 0, [3]float32{1002, 2002, 50}, plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 2,
		StuckAfter:   time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "--confirm client") {
		t.Fatalf("err = %v, want the hidden .gps refusal and the --confirm client hint", err)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want the server's own refusal", err)
	}
}

// TestRouteWalkClientConfirmation: the no-correction source walks the whole plan without one `.gps` and marks every
// waypoint as not server-confirmed.
func TestRouteWalkClientConfirmation(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1010, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	f, report, err := runRouteWalk(t, "confirm", 0, [3]float32{1002, 2002, 50}, plan, dir, &RouteWalkOptions{
		Confirm:      RouteConfirmClient,
		Sample:       100 * time.Millisecond,
		MaxDeviation: 2,
		StuckAfter:   time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
	})
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	if report.RouteWalk.Confirm != RouteConfirmClient || report.RouteWalk.Verdict != RoutePass {
		t.Fatalf("report: %+v", report.RouteWalk)
	}
	for _, leg := range report.RouteWalk.Legs {
		for _, wp := range leg.Waypoints {
			if wp.ServerConfirmed {
				t.Fatalf("a waypoint claims a server confirmation in the client mode: %+v", wp)
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, line := range f.instChats {
		if line == ".gps" {
			t.Fatal("the client mode sent a .gps read")
		}
	}
}

// TestParseRouteGPS is the `.gps` reader against the core's line shapes.
func TestParseRouteGPS(t *testing.T) {
	line := "X: -158.440994 Y: 131.600998 Z: -74.255203 Orientation: 1.25"
	got, ok := parseRouteGPS(line)
	if !ok || got[0] != -158.440994 || got[1] != 131.600998 || got[2] != -74.255203 {
		t.Fatalf("parseRouteGPS(%q) = %+v, %v", line, got, ok)
	}
	if _, ok := parseRouteGPS("GroundZ: 50.000000 FloorZ: 50.000000 Have height data (Map: 1 VMap: 1)"); ok {
		t.Fatal("the grid line parsed as a position")
	}
	if m, ok := parseRouteGPSMap("Map: 389 (Ragefire Chasm), Zone: 0 (test)"); !ok || m != 389 {
		t.Fatalf("map line: %d, %v", m, ok)
	}
	if _, ok := parseRouteGPSMap("GroundZ: 50.000000 FloorZ: 50.000000"); ok {
		t.Fatal("a line without a map parsed as one")
	}
}

// TestRouteWalkSelectsItself: every `.gps` is preceded by a CMSG_SET_SELECTION of the character's own GUID (the core
// reports the selected unit), and the run leaves no select on another unit.
func TestRouteWalkSelectsItself(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1006, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	f, report, err := runRouteWalk(t, "confirm", 0, [3]float32{1002, 2002, 50}, plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 2,
		StuckAfter:   2 * time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	gps := 0
	for _, leg := range report.RouteWalk.Legs {
		gps += len(leg.Waypoints)
	}
	if gps == 0 {
		t.Fatal("no `.gps` confirmation was recorded")
	}
	f.mu.Lock()
	selects := f.routeWalkSelects
	f.mu.Unlock()
	if selects != 1 {
		t.Fatalf("%d CMSG_SET_SELECTION for %d .gps runs: the character selects itself once per map", selects, gps)
	}
}

// TestRouteWalkPlanWaypointNotSnap: the plan's point is 2.5 yd off the mesh, so the planner ends the route on the
// mesh point nearest it and the server confirms that mesh point. The leg must measure the confirmation against the
// PLAN point (DEVIATED here), report the snap separately, and keep the plan's point as the hop's goal.
func TestRouteWalkPlanWaypointNotSnap(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1014.5, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	_, report, err := runRouteWalk(t, "confirm", 0, [3]float32{1002, 2002, 50}, plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 2,
		StuckAfter:   2 * time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	leg := report.RouteWalk.Legs[0]
	if leg.Status != RouteDeviated {
		t.Fatalf("leg status %s, want DEVIATED: %v", leg.Status, leg.Issues)
	}
	if leg.MaxSnap < 2.4 {
		t.Fatalf("max snap %.2f, want the 2.5-yd snap of the plan point onto the mesh", leg.MaxSnap)
	}
	if leg.MaxDev < 2.4 {
		t.Fatalf("max deviation %.2f is the mesh point's own distance; the plan point is 2.5 yd away", leg.MaxDev)
	}
	last := leg.Waypoints[len(leg.Waypoints)-1]
	if last.Goal == nil {
		t.Fatal("the hop's goal is missing")
	}
	if d := dist3f3(*last.Goal, [3]float32{1014.5, 2002, 50}); d > 0.01 {
		t.Fatalf("the hop goal is %.2f %.2f %.2f, want the plan's point: the snapped endpoint was recorded", last.Goal[0], last.Goal[1], last.Goal[2])
	}
}

// TestRouteWalkCancelIsIncomplete: the run's context ends in the middle of a leg. That leg and the run are
// INCOMPLETE, not PASS, and the report records when it ended.
func TestRouteWalkCancelIsIncomplete(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1012, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	f := newFakeServer(t)
	f.routeWalk = true
	f.routeWalkStart = [3]float32{1000, 2002, 50}
	t.Cleanup(f.close)
	f.wg.Add(1)
	go f.serveRealm()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			f.mu.Lock()
			n := f.routeWalkSeen
			f.mu.Unlock()
			if n >= 3 {
				f.mu.Lock()
				f.deliberateClose = true
				f.mu.Unlock()
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	opts := f.routeWalkOptions(plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 2,
		StuckAfter:   2 * time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	opts.Settle = 20 * time.Millisecond
	opts.Deadline = 30 * time.Second
	report, err := Enter(ctx, opts)
	f.wait(t)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the run's cancel", err)
	}
	if report == nil || report.RouteWalk == nil {
		t.Fatal("no route-walk report")
	}
	rw := report.RouteWalk
	if rw.Verdict != RouteIncomplete {
		t.Fatalf("verdict %s, want INCOMPLETE", rw.Verdict)
	}
	if len(rw.Legs) == 0 || rw.Legs[len(rw.Legs)-1].Status != RouteIncomplete {
		t.Fatalf("legs: %+v, want the last leg INCOMPLETE", rw.Legs)
	}
	if rw.Ended.IsZero() {
		t.Fatal("the report has no end time")
	}
}

// TestRouteWalkLateBlockIsUnconfirmed: the first confirmation read of the leg is answered with a delayed duplicate of
// the previous answer (the entry block, 0.6 yd from the stop) and the request's own answer arrives only with the next
// read. That block is within a yard of the stop, so a single read could record it as the stop's own confirmation (and
// the leg could be PASS). A confirmation must come from the reply to this request, proven by the marker barrier, and
// two post-request reads must agree: the pair disagrees, so the leg and the run are UNCONFIRMED.
func TestRouteWalkLateBlockIsUnconfirmed(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1002.6, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	f := newFakeServer(t)
	f.routeWalk = true
	f.routeWalkStart = [3]float32{1002, 2002, 50}
	f.routeWalkStaleMoved = 0.5 // the stop is 0.6 yd from the entry, so the first answer after the walk is late
	t.Cleanup(f.close)
	f.wg.Add(1)
	go f.serveRealm()
	opts := f.routeWalkOptions(plan, dir, &RouteWalkOptions{
		Sample:       500 * time.Millisecond,
		MaxDeviation: 2,
		StuckAfter:   2 * time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	rw := report.RouteWalk
	if rw == nil || rw.Verdict != RouteUnconfirmed {
		t.Fatalf("verdict %v, want UNCONFIRMED: a late block from before the request must not confirm the stop", rw.Verdict)
	}
	if len(rw.Legs) == 0 || rw.Legs[0].Status != RouteUnconfirmed {
		t.Fatalf("legs: %+v, want the first leg UNCONFIRMED", rw.Legs)
	}
	issues := strings.Join(rw.Legs[0].Issues, " | ")
	if !strings.Contains(issues, "two reads disagree") {
		t.Fatalf("leg issues %q: want the disagreeing reads named", issues)
	}
}

// TestRouteWalkAmbiguousGPSIsUnconfirmed: two reads of one stop disagree beyond the reproducibility bound and
// neither is the stop's own position. Nothing may be recorded as confirmed: the leg and the run are UNCONFIRMED.
func TestRouteWalkAmbiguousGPSIsUnconfirmed(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1010, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	f := newFakeServer(t)
	f.routeWalk = true
	f.routeWalkStart = [3]float32{1002, 2002, 50}
	f.routeWalkFlip = true
	t.Cleanup(f.close)
	f.wg.Add(1)
	go f.serveRealm()
	opts := f.routeWalkOptions(plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 10, // the disagreement, not the offset, is the finding
		StuckAfter:   2 * time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	rw := report.RouteWalk
	if rw.Verdict != RouteUnconfirmed {
		t.Fatalf("verdict %s, want UNCONFIRMED", rw.Verdict)
	}
	leg := rw.Legs[0]
	if leg.Status != RouteUnconfirmed {
		t.Fatalf("leg status %s, want UNCONFIRMED: %v", leg.Status, leg.Issues)
	}
	for _, wp := range leg.Waypoints {
		if wp.ServerConfirmed && dist3f3(wp.Server, wp.Point) > 1 {
			t.Fatalf("an unconfirmed stop was recorded as confirmed: %+v", wp)
		}
	}
}

// TestRouteWalkMissingGPSIsUnconfirmed: the `.gps` answer has no coordinates. A missing confirmation is UNCONFIRMED,
// never PASS and never FAIL (the command path worked).
func TestRouteWalkMissingGPSIsUnconfirmed(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1010, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	f := newFakeServer(t)
	f.routeWalk = true
	f.routeWalkStart = [3]float32{1002, 2002, 50}
	f.routeWalkSilentAt = 2
	t.Cleanup(f.close)
	f.wg.Add(1)
	go f.serveRealm()
	opts := f.routeWalkOptions(plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 2,
		StuckAfter:   2 * time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	if err != nil {
		t.Fatalf("route-walk: %v", err)
	}
	rw := report.RouteWalk
	if rw.Verdict != RouteUnconfirmed {
		t.Fatalf("verdict %s, want UNCONFIRMED", rw.Verdict)
	}
	leg := rw.Legs[0]
	if leg.Status != RouteUnconfirmed {
		t.Fatalf("leg status %s, want UNCONFIRMED: %v", leg.Status, leg.Issues)
	}
}

// TestRouteWalkCancelBeforeFirstLeg: the run ends right after the entry read, before the walk starts. There is no
// leg to mark, so the report itself must say INCOMPLETE and never PASS.
func TestRouteWalkCancelBeforeFirstLeg(t *testing.T) {
	plan := routeWalkPlan(t, dungeon.Point{X: 1010, Y: 2002, Z: 50})
	dir := routeWalkMesh(t)
	f := newFakeServer(t)
	f.routeWalk = true
	f.routeWalkStart = [3]float32{1002, 2002, 50}
	t.Cleanup(f.close)
	f.wg.Add(1)
	go f.serveRealm()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			f.mu.Lock()
			n := f.routeWalkGPSReads
			f.mu.Unlock()
			if n >= 1 {
				f.mu.Lock()
				f.deliberateClose = true
				f.mu.Unlock()
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	opts := f.routeWalkOptions(plan, dir, &RouteWalkOptions{
		Sample:       100 * time.Millisecond,
		MaxDeviation: 2,
		StuckAfter:   2 * time.Second,
		StuckStep:    0.5,
		FallDrop:     2,
		GPSQuiet:     100 * time.Millisecond,
	})
	opts.Settle = 20 * time.Millisecond
	opts.Deadline = 30 * time.Second
	report, err := Enter(ctx, opts)
	f.wait(t)
	if err == nil {
		t.Fatal("the run ended without an error")
	}
	if report == nil || report.RouteWalk == nil {
		t.Fatalf("no report: %v", err)
	}
	rw := report.RouteWalk
	if rw.Verdict != RouteIncomplete {
		t.Fatalf("verdict %s, want INCOMPLETE", rw.Verdict)
	}
	for _, leg := range rw.Legs {
		if leg.Status == RoutePass {
			t.Fatalf("a leg of an unfinished run is PASS: %+v", leg)
		}
	}
	if rw.Ended.IsZero() {
		t.Fatal("the report has no end time")
	}
}
