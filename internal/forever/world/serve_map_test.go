package world

// map_snapshot on the Forever serve: the frame the method draws, the recorder that keeps the run so far, and the
// two refusals (a plan name that is not a file name, a frame with nothing on the map).

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/tables"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// mapSelf is the character the map tests play, and mapMate a party member on the same map.
var (
	mapSelf = wire.GUID{Low: 0x1000, High: uint64(wire.TypePlayer) << 58}
	mapMate = wire.GUID{Low: 0x1001, High: uint64(wire.TypePlayer) << 58}
	mapMob  = wire.GUID{Low: 0x2000, High: uint64(1) << 58}
	// mapFar is a party member the world model never saw: another map (or out of range), so never drawn.
	mapFar = wire.GUID{Low: 0x1002, High: uint64(wire.TypePlayer) << 58}
)

// newMapSession builds an in-world session whose world model holds the character, a party of two and one hostile
// creature. fightSender is the DB fake, so the hostility lookup is answered like a live run's.
func newMapSession(t *testing.T) (*session, *dbFake) {
	t.Helper()
	fake := &dbFake{t: t}
	s := newSession(Options{Logf: func(string, ...any) {}}, &Report{Counts: map[string]int{}, Character: Character{Name: "Leader"}}, context.Background())
	t.Cleanup(s.pumpCancel)
	s.fightSender = fake
	s.self, s.selfSeen = mapSelf, true
	s.world.ResetForMap(389)
	s.mover = &objects.Mover{GUID: mapSelf}
	s.mover.X, s.mover.Y, s.mover.Z = 1000, 2000, 50
	mapPutUnit(s, mapSelf, objects.Info{FactionTemplate: 3}, 1000, 2000, 50)
	mapPutUnit(s, mapMate, objects.Info{FactionTemplate: 3}, 1010, 2000, 50)
	info := objects.Info{Type: objects.TypeUnit, HasUnit: true, FactionTemplate: 29, Health: 40, MaxHealth: 40, Level: 5}
	info.Complete()
	mapPutUnit(s, mapMob, info, 1020, 2000, 50)
	s.party = party.NewState()
	s.party.InParty, s.party.RosterValid = true, true
	s.party.SelfGUID, s.party.LeaderGUID = mapSelf, mapSelf
	for _, m := range []struct {
		g    wire.GUID
		name string
	}{{mapSelf, "Leader"}, {mapMate, "Mate"}, {mapFar, "Far"}} {
		s.party.Members[m.g] = &party.Member{GUID: m.g, Name: m.name, Connected: true, Full: true}
		s.party.Order = append(s.party.Order, m.g)
	}
	return s, fake
}

// mapPutUnit adds one unit to the session's world model at a position.
func mapPutUnit(s *session, g wire.GUID, info objects.Info, x, y, z float32) {
	info.Type, info.HasObject, info.HasUnit = objects.TypeUnit, true, true
	info.Complete()
	s.world.Apply(&objects.Update{Blocks: []objects.WorldFragment{{
		GUID: g, ObjectType: objects.TypeUnit, Info: info, ValuesComplete: true,
		Movement: &objects.MoveBlock{MovementUpdate: true, X: x, Y: y, Z: z},
	}}}, time.Now())
}

// answerFactions serves the two faction templates the map session's character and mob carry.
func answerFactions(t *testing.T, s *session) {
	go func() {
		dbAnswer(t, s, tables.TableFactionTemplate, 3, tables.StatusValid, hostileRecord(1|2, 2, 0))
		dbAnswer(t, s, tables.TableFactionTemplate, 29, tables.StatusValid, hostileRecord(1|4, 0, 2))
	}()
}

// TestServeMapSnapshotDrawsPartyAndHostiles: the frame holds the character and its visible party member, the one
// hostile creature, and draws an image from them.
func TestServeMapSnapshotDrawsPartyAndHostiles(t *testing.T) {
	s, _ := newMapSession(t)
	answerFactions(t, s)
	res, err := s.serveMapSnapshot(ServeMapParams{}, "dungeons")
	if err != nil {
		t.Fatalf("map_snapshot: %v", err)
	}
	if res.Members != 2 {
		t.Fatalf("members %d, want the character and its one mate", res.Members)
	}
	if res.Creatures != 1 {
		t.Fatalf("creatures %d, want the one hostile", res.Creatures)
	}
	if res.Map != 389 {
		t.Fatalf("map %d", res.Map)
	}
	if !strings.Contains(res.SVG, "<svg") || len(res.SVG) < 200 {
		t.Fatalf("svg is not an image (%d bytes)", len(res.SVG))
	}
	if res.Background {
		t.Fatalf("background %v without a navmesh folder", res.Background)
	}
}

// TestServeMapSnapshotNoNavmeshNote: without --navmesh the image is drawn and says why it has no background.
func TestServeMapSnapshotNoNavmeshNote(t *testing.T) {
	s, _ := newMapSession(t)
	answerFactions(t, s)
	res, err := s.serveMapSnapshot(ServeMapParams{}, "dungeons")
	if err != nil {
		t.Fatalf("map_snapshot: %v", err)
	}
	if res.Background {
		t.Fatal("background drawn without a navmesh folder")
	}
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, "navmesh") {
		t.Fatalf("notes do not say why there is no background: %q", res.Notes)
	}
}

// TestServeMapSnapshotRejectsBadPlanName: a plan is a file name, and a name that is not one is refused before any
// file is read.
func TestServeMapSnapshotRejectsBadPlanName(t *testing.T) {
	s, _ := newMapSession(t)
	answerFactions(t, s)
	_, err := s.serveMapSnapshot(ServeMapParams{Plan: "../escape"}, "dungeons")
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeInvalidParams {
		t.Fatalf("bad plan name answered %v, want an invalid-params error", err)
	}
	if !strings.Contains(re.Message, "plan must be a plan file name") {
		t.Fatalf("message %q does not name the rule", re.Message)
	}
}

// TestServeMapSnapshotSkipsOtherMapMembers: a roster member this map's model never saw is not drawn; the model only
// holds the objects of the map the character is on.
func TestServeMapSnapshotSkipsOtherMapMembers(t *testing.T) {
	s, _ := newMapSession(t)
	answerFactions(t, s)
	res, err := s.serveMapSnapshot(ServeMapParams{}, "dungeons")
	if err != nil {
		t.Fatalf("map_snapshot: %v", err)
	}
	if res.Members != 2 {
		t.Fatalf("members %d, want 2: the far member is not on this map", res.Members)
	}
}

// TestServeMapRecorderSamplesBetweenTicks: the recorder draws the run so far, not only the call's own frame. A mate
// moved between two ticks is a second sample on its track.
func TestServeMapRecorderSamplesBetweenTicks(t *testing.T) {
	saved := serveMapSampleEvery
	serveMapSampleEvery = 20 * time.Millisecond
	defer func() { serveMapSampleEvery = saved }()
	s, _ := newMapSession(t)
	answerFactions(t, s)
	st := &serveState{calls: make(chan *serveCall, 8), done: make(chan struct{})}
	st.install(s, 1)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		for {
			select {
			case c := <-st.calls:
				st.run(c)
			case <-st.done:
				return
			}
		}
	}()
	defer func() {
		close(st.done)
		<-loopDone
		s.teardown()
	}()
	waitFor(t, "the first two samples", func() bool { return s.mapRec.samples() >= 2 })
	mapPutUnit(s, mapMate, objects.Info{}, 1040, 2000, 50)
	waitFor(t, "the mate's second sample", func() bool { return s.mapRec.samples() >= 3 })
}

// TestServeMapRecorderStopsOnTeardown: the recorder's goroutine ends with the session, so a reconnect leaves none
// behind.
func TestServeMapRecorderStopsOnTeardown(t *testing.T) {
	s, _ := newMapSession(t)
	before := runtime.NumGoroutine()
	st := &serveState{calls: make(chan *serveCall, 8), done: make(chan struct{})}
	st.install(s, 1)
	if s.mapRec == nil {
		t.Fatal("install started no recorder")
	}
	s.teardown()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("the recorder's goroutine is still running (%d > %d)", runtime.NumGoroutine(), before)
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-s.mapRec.done:
	default:
		t.Fatal("the recorder's done channel is not closed after teardown")
	}
}

// waitFor polls cond until it holds or the test's own short budget runs out.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestServeUnitsReaction is the 3D view's reading: with `reaction` each unit carries its faction's relation to the
// character (the mob true, the character and its mate false), and a faction the server cannot tell leaves the field
// out instead of guessing. Without the flag no row carries it (the lookup is not paid).
//
// Mutation this test catches: a reaction that is always filled from the hostile filter's table (a unit outside the
// filter has no reading), and one that guesses a missing template.
func TestServeUnitsReaction(t *testing.T) {
	s, _ := newMapSession(t)
	answerFactions(t, s)
	units, err := s.serveUnits(serveUnitsParams{Reaction: true})
	if err != nil {
		t.Fatalf("units: %v", err)
	}
	got := map[GUID]*bool{}
	for _, u := range units {
		got[u.GUID] = u.Hostile
	}
	if h := got[GUID(mapMob)]; h == nil || !*h {
		t.Fatalf("the mob's reaction is %v, want hostile", h)
	}
	if h := got[GUID(mapSelf)]; h == nil || *h {
		t.Fatalf("the character's reaction is %v, want not hostile", h)
	}
	// Without the flag nothing pays the lookup: no row carries a reaction.
	s2, _ := newMapSession(t) // no faction answers at all: the flag would have to fail
	plain, err := s2.serveUnits(serveUnitsParams{})
	if err != nil {
		t.Fatalf("units without the flag: %v", err)
	}
	for _, u := range plain {
		if u.Hostile != nil {
			t.Fatalf("a row without `reaction` carries a reaction: %+v", u)
		}
	}
	// A template the server does not hold is unknown: that row's field is left out, and the call still answers.
	s3, _ := newMapSession(t)
	go func() {
		dbAnswer(t, s3, tables.TableFactionTemplate, 3, tables.StatusValid, hostileRecord(1|2, 2, 0))
		dbAnswer(t, s3, tables.TableFactionTemplate, 29, tables.StatusInvalid, nil)
	}()
	unknown, err := s3.serveUnits(serveUnitsParams{Reaction: true})
	if err != nil {
		t.Fatalf("units with an unanswerable faction template: %v", err)
	}
	for _, u := range unknown {
		switch GUID(u.GUID) {
		case GUID(mapMob):
			if u.Hostile != nil {
				t.Fatalf("an unanswerable template produced a reaction: %+v", u)
			}
		case GUID(mapSelf):
			if u.Hostile == nil || *u.Hostile {
				t.Fatalf("the character's answered reaction is %v, want not hostile", u.Hostile)
			}
		}
	}
}
