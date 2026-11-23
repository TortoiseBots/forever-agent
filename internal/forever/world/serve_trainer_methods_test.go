package world

// The trainer methods over the trainer fake. The failing case is TestServeTrainThroughMethods: a train that returns
// before the learned-spell answer, or reports a refusal as an RPC error, or buys without reading the list first (the
// server refuses a purchase under a trainer id this session never opened), fails it.

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func trainerParams() serveTrainerParams { return serveTrainerParams{GUID: GUID(trainerNPCGUID)} }

func TestServeTrainerListRows(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagTrainer)
	res, err := tr.s.serveTrainerList(context.Background(), trainerParams())
	if err != nil || !res.OK {
		t.Fatalf("trainer_list: %+v %v\n%s", res, err, joinLines(tr.lines))
	}
	if res.Event == nil || res.Event.Kind != events.KindTrainerList || res.Since >= res.Event.Seq {
		t.Fatalf("event %+v", res.Event)
	}
	if tr.listSends != 1 {
		t.Fatalf("%d list asks", tr.listSends)
	}
	l := res.Trainer
	if l == nil || l.TrainerID != trainerID || l.Greeting != "Train with me." || len(l.Spells) != 2 {
		t.Fatalf("trainer %+v", l)
	}
	a, b := l.Spells[0], l.Spells[1]
	if a.SpellID != trainerSpellA || a.Cost != 250 || a.Level != 5 || a.State != "learnable" ||
		b.SpellID != trainerSpellB || b.Cost != 55 || b.ReqSkill != 171 || b.ReqSkillRank != 75 {
		t.Fatalf("rows %+v", l.Spells)
	}
}

func TestServeTrainerListStates(t *testing.T) {
	tr := newTrainerWorldLevel(t, npc.NPCFlagTrainer, 6)
	res, err := tr.s.serveTrainerList(context.Background(), trainerParams())
	if err != nil || res.Trainer.Spells[0].State != "learnable" || res.Trainer.Spells[1].State != "unavailable" {
		t.Fatalf("level 6: %+v %v", res.Trainer, err)
	}
	tr = newTrainerWorldLevel(t, npc.NPCFlagTrainer, 0)
	res, err = tr.s.serveTrainerList(context.Background(), trainerParams())
	if err != nil || res.Trainer.Spells[0].State != "unknown_level" {
		t.Fatalf("no level: %+v %v", res.Trainer, err)
	}
}

func TestServeTrainerListNeedsATrainer(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagVendor)
	if _, err := tr.s.serveTrainerList(context.Background(), trainerParams()); err == nil || !strings.Contains(err.Error(), "not a trainer") {
		t.Fatalf("a vendor answered %v", err)
	}
	if _, err := tr.s.serveTrainerList(context.Background(), serveTrainerParams{GUID: GUID(wire.GUID{Low: 5})}); err == nil {
		t.Fatal("an unknown unit passed")
	}
	if _, err := tr.s.serveTrainerList(context.Background(), serveTrainerParams{GUID: GUID(trainerNPCGUID), WaitMS: -1}); err == nil {
		t.Fatal("a negative wait_ms passed")
	}
	if len(tr.sent) != 0 {
		t.Fatalf("refused calls sent %d packets", len(tr.sent))
	}
}

func TestServeTrainThroughMethods(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagTrainer)
	p := trainerParams()
	p.SpellID = testI32(trainerSpellA)
	res, err := tr.s.serveTrain(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("train: %+v %v\n%s", res, err, joinLines(tr.lines))
	}
	if tr.listSends != 1 || tr.buys != 1 || tr.lastID != trainerID || tr.lastSpell != trainerSpellA {
		t.Fatalf("%d list asks, %d buys, id %d spell %d: the purchase did not follow the list", tr.listSends, tr.buys, tr.lastID, tr.lastSpell)
	}
	e := res.Event
	if e == nil || e.Kind != events.KindTrainerTrained || e.SpellID != trainerSpellA || res.Since >= e.Seq ||
		e.Data["cost"] != uint32(250) || e.Data["copper_before"] != uint64(1000) || e.Data["copper_after"] != uint64(750) {
		t.Fatalf("event %+v", e)
	}
	// A second spell reuses the cached list: no new ask.
	p.SpellID = testI32(trainerSpellB)
	if res, err = tr.s.serveTrain(context.Background(), p); err != nil || !res.OK || tr.listSends != 1 || tr.buys != 2 {
		t.Fatalf("second train: %+v %v (%d asks, %d buys)", res, err, tr.listSends, tr.buys)
	}
}

func TestServeTrainRefusalIsAResult(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagTrainer)
	tr.refuse = true
	p := trainerParams()
	p.SpellID = testI32(trainerSpellA)
	res, err := tr.s.serveTrain(context.Background(), p)
	if err != nil {
		t.Fatalf("a refused train was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindTrainerFailed || !strings.Contains(res.Event.Text, "not_enough_money") {
		t.Fatalf("answer %+v", res)
	}
}

// TestServeTrainClientSideRefusal: a spell the character's level rules out is refused by the step before any
// purchase goes out, and the answer is still a result with a trainer_failed event.
func TestServeTrainClientSideRefusal(t *testing.T) {
	tr := newTrainerWorldLevel(t, npc.NPCFlagTrainer, 3)
	p := trainerParams()
	p.SpellID = testI32(trainerSpellA)
	res, err := tr.s.serveTrain(context.Background(), p)
	if err != nil || res.OK || res.Event == nil || res.Event.Kind != events.KindTrainerFailed || res.Event.SpellID != trainerSpellA ||
		!strings.Contains(res.Event.Text, "level") {
		t.Fatalf("answer %+v %v", res, err)
	}
	if tr.buys != 0 {
		t.Fatal("a purchase went out")
	}
}

func TestServeTrainRefusesBadParamsBeforeAnySend(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagTrainer)
	for name, p := range map[string]serveTrainerParams{
		"no spell":   {GUID: GUID(trainerNPCGUID)},
		"zero spell": {GUID: GUID(trainerNPCGUID), SpellID: testI32(0)},
		"negative":   {GUID: GUID(trainerNPCGUID), SpellID: testI32(-3)},
		"no guid":    {SpellID: testI32(trainerSpellA)},
		"bad wait":   {GUID: GUID(trainerNPCGUID), SpellID: testI32(trainerSpellA), WaitMS: 99999},
	} {
		if _, err := tr.s.serveTrain(context.Background(), p); err == nil {
			t.Errorf("%s: the train passed", name)
		}
	}
	if len(tr.sent) != 0 {
		t.Fatalf("refused trains sent %d packets", len(tr.sent))
	}
	// A spell the cached list does not hold is a precondition too.
	if _, err := tr.s.serveTrainerList(context.Background(), trainerParams()); err != nil {
		t.Fatal(err)
	}
	sent := len(tr.sent)
	p := trainerParams()
	p.SpellID = testI32(31337)
	if _, err := tr.s.serveTrain(context.Background(), p); err == nil || !strings.Contains(err.Error(), "has no spell") {
		t.Fatalf("an unlisted spell: %v", err)
	}
	if len(tr.sent) != sent {
		t.Fatal("the refused train sent a packet")
	}
}
