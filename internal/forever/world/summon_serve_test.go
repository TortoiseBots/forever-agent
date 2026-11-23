package world

// The summon offer over the fake world server: the serving daemon holds the SMSG_SUMMON_REQUEST it read, answers it
// with accept_summon, and a second answer is refused because the offer is gone. The use_object ritual flag is the
// same scenario's object side (a ritual use that answers "none" is not a failure).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// serveSummonAnswer is one CMSG_SUMMON_RESPONSE the fake read, under the fake's mu.
type serveSummonAnswer struct {
	guid   wire.GUID
	accept bool
}

// fakeSummonRequest builds SMSG_SUMMON_REQUEST (MovementPackets.cpp:1100-1110).
func fakeSummonRequest(summoner wire.GUID, realm uint32, area int32, reason uint8, skip bool) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, summoner)
	b.WriteU32(realm)
	b.WriteI32(area)
	b.WriteU8(reason)
	b.WriteBit(skip)
	b.FlushBits()
	return b.Bytes()
}

// summonAnswer polls the fake for the answers it read.
func summonAnswer(t *testing.T, f *fakeServer, want int) []serveSummonAnswer {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := append([]serveSummonAnswer(nil), f.summonAnswers...)
		f.mu.Unlock()
		if len(got) >= want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]serveSummonAnswer(nil), f.summonAnswers...)
}

func TestServeAcceptSummonNoOffer(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	a.mustFail(t, "accept_summon", nil, -32003, "no summon offer")
	a.stop(t)
	f.wait(t)
}

func TestServeAcceptSummonSendsResponse(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSummon = true
	f.summonGUID = wire.GUID{Low: 0x5151, High: 0x0800000000000000}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	// The offer's event carries the summoner's name and the offer's fields.
	ev, _ := waitEvent(t, a, 0, func(e Event) bool { return e.Kind == "summon_request" })
	if ev.Source != "Summoner" {
		t.Fatalf("the offer's source is %q, want the cached name", ev.Source)
	}
	if ev.SourceGUID != eventGUID(f.summonGUID) {
		t.Fatalf("the offer's source guid is %q", ev.SourceGUID)
	}
	if area, _ := ev.Data["area"].(float64); area != 1519 {
		t.Fatalf("the offer's area: %v", ev.Data["area"])
	}
	if reason, _ := ev.Data["reason"].(float64); reason != 1 {
		t.Fatalf("the offer's reason: %v", ev.Data["reason"])
	}

	var accepted ServeAcceptSummon
	json.Unmarshal(a.mustCall(t, "accept_summon", nil), &accepted)
	if !accepted.Accepted || wire.GUID(accepted.Summoner) != f.summonGUID {
		t.Fatalf("accept_summon: %+v", accepted)
	}

	// The answer is the Accept bit; the fake reads it with the server's own reader.
	got := summonAnswer(t, f, 1)
	if len(got) != 1 || got[0].guid != f.summonGUID || !got[0].accept {
		t.Fatalf("the fake read %+v", got)
	}

	// The answer cleared the offer: a second answer is refused (and nothing more is sent).
	a.mustFail(t, "accept_summon", nil, -32003, "no summon offer")

	a.stop(t)
	f.wait(t)
}

func TestServeAcceptSummonDecline(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSummon = true
	f.summonGUID = wire.GUID{Low: 0x5152, High: 0x0800000000000000}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	_, _ = waitEvent(t, a, 0, func(e Event) bool { return e.Kind == "summon_request" })
	var declined ServeAcceptSummon
	json.Unmarshal(a.mustCall(t, "accept_summon", map[string]any{"accept": false}), &declined)
	if declined.Accepted {
		t.Fatalf("accept_summon accept=false: %+v", declined)
	}
	got := summonAnswer(t, f, 1)
	if len(got) != 1 || got[0].guid != f.summonGUID || got[0].accept {
		t.Fatalf("the fake read %+v", got)
	}

	a.stop(t)
	f.wait(t)
}

func TestUseObjectRitualFlag(t *testing.T) {
	l := newLootWorld(t)
	ritual := wire.GUID{Low: 0xB004, High: uint64(objects.TypeGameObject) << 58}
	// A ritual (game object type 18) that answers no use packet: the use waits, the outcome is "none", and the
	// object is a ritual, so the caller does not read "none" as a failure.
	l.place(fakeGameObjectCreate(fakePacked(ritual), 0, 2, 0, 0, 133234, 1, 18))
	res, err := l.s.serveUseObject(context.Background(), serveUseObjectParams{GUID: GUID(ritual), WaitMS: 30})
	if err != nil {
		t.Fatalf("the ritual use: %v", err)
	}
	if res.Outcome != "none" || res.OK || res.Event != nil {
		t.Fatalf("the ritual use: %+v", res)
	}
	if !res.Ritual || res.Object.Type != 18 {
		t.Fatalf("the ritual object is not flagged: %+v", res)
	}

	// A chest (game object type 3) that answers nothing is not a ritual.
	chest := wire.GUID{Low: 0xB005, High: uint64(objects.TypeGameObject) << 58}
	l.place(fakeGameObjectCreate(fakePacked(chest), 0, 2, 0, 0, 2842, 1, 3))
	res, err = l.s.serveUseObject(context.Background(), serveUseObjectParams{GUID: GUID(chest), WaitMS: 30})
	if err != nil {
		t.Fatalf("the chest use: %v", err)
	}
	if res.Ritual {
		t.Fatalf("a chest was flagged as a ritual: %+v", res)
	}
}
