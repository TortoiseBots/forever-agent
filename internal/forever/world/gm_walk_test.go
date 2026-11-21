package world

// The mark tap over hand-made chat lines: the GUID gate (never the name), fake sender names, positions from the
// GM's own object, and marks with no position.

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/gmwalk"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	gmWalkGM    = wire.GUID{Low: 0x1111, High: 0x0200000000000000}
	gmWalkOther = wire.GUID{Low: 0x2222, High: 0x0200000000000000}
)

// gmWalkSession is a session whose world model holds the GM at 100, 200, 30 on map 0.
func gmWalkSession() *session {
	s := &session{world: objects.NewWorld(0)}
	s.world.Apply(&objects.Update{Blocks: []objects.WorldFragment{{
		GUID:           gmWalkGM,
		ObjectType:     objects.TypePlayer,
		Movement:       &objects.MoveBlock{MovementUpdate: true, X: 100, Y: 200, Z: 30},
		ValuesComplete: true,
	}}}, time.Now())
	return s
}

func TestGMWalkTapMarks(t *testing.T) {
	s := gmWalkSession()
	rec := &gmwalk.Recorder{}
	o := (&GMWalkOptions{Follow: "Gm", MarksFrom: GMWalkFromGM}).withDefaults()
	tap := &gmWalkTap{}
	s.gmWalkTap(rec, &o, tap, gmWalkGM, chat.Line{Type: chat.TypeWhisper, SenderGUID: gmWalkGM, SenderName: "Gm", Text: "point gate"})
	s.gmWalkTap(rec, &o, tap, gmWalkGM, chat.Line{Type: chat.TypeSay, SenderGUID: gmWalkGM, SenderName: "Gm", Text: "zone lava 25"})
	s.gmWalkTap(rec, &o, tap, gmWalkGM, chat.Line{Type: chat.TypeWhisper, SenderGUID: gmWalkGM, SenderName: "Gm", Text: "hello there"})
	// A stranger using the same words — even with the GM's name on the line — is unknown chatter, never stored.
	s.gmWalkTap(rec, &o, tap, gmWalkGM, chat.Line{Type: chat.TypeWhisper, SenderGUID: gmWalkOther, SenderName: "Gm", Text: "point far"})
	s.gmWalkTap(rec, &o, tap, gmWalkGM, chat.Line{Type: chat.TypeParty, SenderGUID: gmWalkGM, SenderName: "Gm", Text: "point nope"})
	// A party line is not a whisper or a nearby say: it never reaches the mark parser and is not counted.
	if tap.marks != 2 || tap.unknown != 2 {
		t.Fatalf("marks %d unknown %d, want 2 and 2", tap.marks, tap.unknown)
	}
	d, err := rec.Build(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Points) != 1 || len(d.Zones) != 1 {
		t.Fatalf("data: %+v", d)
	}
	// The point lands at the GM's own object position, not at any sample: the recorder holds no samples here.
	if p := d.Points[0]; p.X != 100 || p.Y != 200 || p.Z != 30 || p.Map != 0 {
		t.Fatalf("point at %v %v %v map %d, want the GM's 100 200 30 map 0", p.X, p.Y, p.Z, p.Map)
	}
}

func TestGMWalkTapFakeSenderName(t *testing.T) {
	s := gmWalkSession()
	rec := &gmwalk.Recorder{}
	o := (&GMWalkOptions{Follow: "Gm", MarksFrom: GMWalkFromGM}).withDefaults()
	tap := &gmWalkTap{}
	// The GM's own guid, but the line says the sender name is fake: a system line wearing the GM's name.
	s.gmWalkTap(rec, &o, tap, gmWalkGM, chat.Line{Type: chat.TypeWhisper, SenderGUID: gmWalkGM, SenderName: "Gm", FakeSenderName: true, Text: "point gate"})
	if tap.marks != 0 || tap.unknown != 1 {
		t.Fatalf("fake sender name marked: %+v", tap)
	}
}

func TestGMWalkTapAnySender(t *testing.T) {
	s := gmWalkSession()
	rec := &gmwalk.Recorder{}
	o := (&GMWalkOptions{Follow: "Gm", MarksFrom: GMWalkFromAny}).withDefaults()
	tap := &gmWalkTap{}
	s.gmWalkTap(rec, &o, tap, gmWalkGM, chat.Line{Type: chat.TypeWhisper, SenderGUID: gmWalkOther, SenderName: "Other", Text: "point far"})
	if tap.marks != 1 {
		t.Fatalf("any-sender whisper must mark: %+v", tap)
	}
	d, err := rec.Build(0)
	if err != nil {
		t.Fatal(err)
	}
	// Any sender's mark still lands at the followed GM's object position.
	if len(d.Points) != 1 || d.Points[0].X != 100 {
		t.Fatalf("points: %+v", d.Points)
	}
}

func TestGMWalkSampleSkipsTransfers(t *testing.T) {
	s := gmWalkSession()
	smp, ok := s.gmWalkSample(gmWalkGM)
	if !ok || smp.Map != 0 || smp.X != 100 {
		t.Fatalf("sample %+v %v, want the GM's 100 200 30 map 0", smp, ok)
	}
	// The GM gone from the model (out of range): the sample is skipped, never the agent's position.
	empty := &session{world: objects.NewWorld(0)}
	if _, ok := empty.gmWalkSample(gmWalkGM); ok {
		t.Fatal("sample without the GM in the model, want a skip")
	}
	// A transfer open between the collect and the read: the map and the coordinates would be two updates'
	// content, so the sample is skipped — no stray road across the transfer.
	s.mapChg.between = true
	if _, ok := s.gmWalkSample(gmWalkGM); ok {
		t.Fatal("sample between maps, want a skip")
	}
}

func TestGMWalkTapNoPosition(t *testing.T) {
	// The GM is not in the world model (out of range, or the update has not arrived yet): a point or zone with
	// no position is ignored, never stamped at the agent's position.
	s := &session{world: objects.NewWorld(0)}
	rec := &gmwalk.Recorder{}
	o := (&GMWalkOptions{Follow: "Gm"}).withDefaults()
	tap := &gmWalkTap{}
	s.gmWalkTap(rec, &o, tap, gmWalkGM, chat.Line{Type: chat.TypeWhisper, SenderGUID: gmWalkGM, SenderName: "Gm", Text: "point early"})
	if tap.ignored != 1 || tap.marks != 0 {
		t.Fatalf("mark with no position: %+v", tap)
	}
	// A split carries no position: it cuts after the last sample, so with no samples at all it is ignored too.
	s.gmWalkTap(rec, &o, tap, gmWalkGM, chat.Line{Type: chat.TypeWhisper, SenderGUID: gmWalkGM, SenderName: "Gm", Text: "split"})
	if tap.ignored != 2 {
		t.Fatalf("split with no samples: %+v", tap)
	}
}

func TestGMWalkDist3IsYards(t *testing.T) {
	if got := dist3(0, 0, 0, 3, 4, 0); got != 5 {
		t.Fatalf("dist3(0,0,0, 3,4,0) = %v, want 5 yards (not 25 squared)", got)
	}
}

func TestGMWalkOptionsDefaults(t *testing.T) {
	o := (&GMWalkOptions{Follow: "Gm"}).withDefaults()
	if o.MarksFrom != GMWalkFromGM || o.Sample != GMWalkSample || o.ToleranceYd != GMWalkTolerance || o.FollowYd != GMWalkFollowYd {
		t.Fatalf("defaults: %+v", o)
	}
}

// gmWalkFakeSender is a virtual-clock sender: every wait moves the clock and walks the GM along an L-shaped road
// (east, then north) at a steady pace, as the server's object updates would while the agent runs after it.
type gmWalkFakeSender struct {
	now   time.Time
	onNow func(time.Time)
}

func (f *gmWalkFakeSender) Send(uint32, []byte) error { return nil }
func (f *gmWalkFakeSender) Now() time.Time            { return f.now }
func (f *gmWalkFakeSender) WaitUntil(ctx context.Context, until time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if until.After(f.now) {
		f.now = until
	}
	f.onNow(f.now)
	return nil
}

func TestGMWalkSamplesWhileChasing(t *testing.T) {
	const (
		leg   = 400.0 // yards per leg of the L
		pace  = 5.0   // GM yards per second
		start = 12.0  // the agent starts this far behind
	)
	t0 := time.Unix(1_000_000, 0)
	s := gmWalkSession()
	moveGM := func(now time.Time) {
		d := math.Min(pace*now.Sub(t0).Seconds(), 2*leg)
		x, y := float32(math.Min(d, leg)), float32(math.Max(d-leg, 0))
		s.world.Apply(&objects.Update{Blocks: []objects.WorldFragment{{
			GUID: gmWalkGM, ObjectType: objects.TypePlayer,
			Movement:       &objects.MoveBlock{MovementUpdate: true, X: x, Y: y, Z: 30},
			ValuesComplete: true,
		}}}, now)
	}
	fs := &gmWalkFakeSender{now: t0, onNow: moveGM}
	moveGM(t0)
	s.mover = &objects.Mover{Sender: fs, ClientTime: func(time.Time) uint32 { return 0 }, RunSpeed: 5.5, X: -start, Y: 0, Z: 30}
	rec := &gmwalk.Recorder{}
	smp := &gmWalkSampler{s: s, guid: gmWalkGM, rec: rec, sample: GMWalkSample, lastMove: t0, sampleAt: t0}
	walkEnd := t0.Add(time.Duration(2 * leg / pace * float64(time.Second)))
	// The main loop's shape: a chase whenever the agent is out of reach, retried after a failed leg.
	for fs.now.Before(walkEnd.Add(10 * time.Second)) {
		obj, _ := s.world.Object(gmWalkGM)
		ax, ay, az, _ := s.mover.Position()
		if objects.Distance(ax, ay, az, obj.X, obj.Y, obj.Z) > 2 {
			_ = s.mover.ChaseTo(context.Background(), s.gmWalkChaseGoal(gmWalkGM, 2, smp, obj.X, obj.Y, obj.Z))
		} else {
			fs.now = fs.now.Add(time.Second)
			moveGM(fs.now)
		}
		smp.tick(fs.now)
	}
	// 160 s of walking: one sample every 2 s, taken inside the chase, not only where chases end.
	if got := rec.Samples(); got < 70 {
		t.Fatalf("%d samples over an 160 s chase, want one per interval (>= 70)", got)
	}
	if smp.idle {
		t.Fatal("idle bound fired while the GM kept walking (the agent's chase time counted as idleness)")
	}
	d, err := rec.Build(1.0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Roads) != 1 {
		t.Fatalf("roads: %d, want 1", len(d.Roads))
	}
	// The corner at (leg, 0) survives the thinning: some kept point lies within the tolerance of it.
	near := false
	for _, p := range d.Roads[0].Points {
		if math.Hypot(float64(p[0])-leg, float64(p[1])) <= 1.0+pace*GMWalkSample.Seconds() {
			near = true
		}
	}
	if !near {
		t.Fatalf("corner (%v, 0) missing from the road: %+v", leg, d.Roads[0].Points)
	}
}
