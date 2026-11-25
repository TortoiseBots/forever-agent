package world

// The trainer steps against the handler-boundary fake: the fake answers the client's packets the way the core does —
// a gossip trainer opens only through its "Train me." option (F1, Player.cpp:15160-15312), a trainer without a
// gossip menu answers CMSG_TRAINER_LIST, and a purchase answers SMSG_LEARNED_SPELLS or SMSG_TRAINER_BUY_FAILED — and
// every step must read its outcome from the server's own words.

import (
	"context"
	"fmt"
	"testing"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

const (
	trainerEntry  = 51571
	trainerMenuID = 3107
	trainerOption = 7
	trainerID     = 99
	trainerSpellA = 1234
	trainerSpellB = 5678
)

var trainerNPCGUID = wire.GUID{Low: 0xABC0DEF0, High: uint64(8) << 58}

// trainerWorld is the item-world fake with one trainer in the world model and the trainer's own answers.
type trainerWorld struct {
	*itemWorld
	flags     uint32
	talked    bool
	selected  int32
	listSends int
	buys      int
	// refuse, when set, answers the purchase with a failure of that reason; noCharge sends no money update with the
	// learned answer (a purchase whose fare the step cannot read back).
	refuse    bool
	noCharge  bool
	lastSpell int32
	lastID    int32
	// noTrainerOption answers the hello with a menu that has no trainer option, so the gossip flow has nothing to
	// select.
	noTrainerOption bool
	// taught, when set, is the rank the learned answer names instead of the bought spell, with the bought spell in
	// its Superceded link (a castable list spell is taught by casting its next rank, Trainer::TeachSpell).
	taught int32
	// burst, when set, is one learned packet per spell id, delivered in this order after one charge: both land
	// before the step's first poll.
	burst []int32
	// twoOptions opens a menu with two "Train me." options (ids trainerOption and trainerOption+1); selecting the
	// second answers a list of trainerID+1.
	twoOptions bool
	// spells, when set, is the list the trainer answers with instead of trainerSpells (a zero-cost row, say).
	spells []npc.TrainerSpell
	// learnedAnswer, when set, is the learned-spell packet the purchase answers with instead of the default one.
	learnedAnswer []byte
}

// newTrainerWorld plants one trainer of the given flags at the character's own position and answers its packets. The
// character's level arrives as a UnitData values update, like every other piece of its own state here.
func newTrainerWorld(t *testing.T, flags uint32) *trainerWorld {
	t.Helper()
	return newTrainerWorldLevel(t, flags, 60)
}

// newTrainerWorldLevel is newTrainerWorld for a character whose level has not been read (level 0) or holds the given
// one.
func newTrainerWorldLevel(t *testing.T, flags uint32, level int32) *trainerWorld {
	t.Helper()
	f := newItemWorld(t)
	tr := &trainerWorld{itemWorld: f, flags: flags}
	payload := fakeUnitCreateBlock(fakePackedGUID(trainerNPCGUID), 0, 0, 0, 0, 0, trainerEntry, 100, 100, 5, flags)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the trainer create: %v", err)
	}
	f.world.Apply(u, f.now)
	f.onSend = tr.answer
	fundSelf(f, 1000)
	if level > 0 {
		f.server(wire.SMSGUpdateObject, levelUpdate(level))
	}
	return tr
}

// trainerObject returns the trainer's world model object.
func (tr *trainerWorld) trainerObject(t *testing.T) *objects.Object {
	t.Helper()
	o, ok := tr.world.Object(trainerNPCGUID)
	if !ok {
		t.Fatal("the trainer is not in the world model")
	}
	return o
}

// state is an act state with the trainer as the current NPC.
func (tr *trainerWorld) state(t *testing.T) *actState {
	t.Helper()
	return &actState{npc: tr.trainerObject(t), hasNPC: true, npcEntry: trainerEntry}
}

// deliver feeds one server packet through the session's own handlers.
func (tr *trainerWorld) deliver(core uint32, payload []byte) {
	tr.t.Helper()
	packet := worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}
	var err error
	switch core {
	case wire.SMSGGossipMessage, wire.SMSGGossipComplete:
		err = tr.s.handleNPC(packet)
	case wire.SMSGTrainerList, wire.SMSGTrainerBuyFail:
		err = tr.s.handleTrainer(packet)
	default:
		// A learned spell goes through the session's own dispatch (the spell book owns that packet).
		err = tr.s.handle(context.Background(), event{p: packet})
	}
	if err != nil {
		tr.t.Fatalf("%s: %v", wire.ServerName(core), err)
	}
}

// answer is the fake server's read side for the trainer's steps.
func (tr *trainerWorld) answer(f *itemWorld, core uint32, payload []byte) {
	switch core {
	case wire.CMSGTalkToGossip:
		tr.talked = true
		if g := guidOf(payload); g != trainerNPCGUID {
			f.t.Fatalf("CMSG_TALK_TO_GOSSIP names %s", g)
		}
		if tr.twoOptions {
			tr.deliver(wire.SMSGGossipMessage, twoTrainerMenuPayload(trainerNPCGUID))
			return
		}
		if tr.noTrainerOption {
			tr.deliver(wire.SMSGGossipMessage, buildGossipMessage(trainerNPCGUID, trainerMenuID, trainerOption,
				npc.GossipOptionVendor, "Let me browse your goods."))
			return
		}
		tr.deliver(wire.SMSGGossipMessage, buildGossipMessage(trainerNPCGUID, trainerMenuID, trainerOption,
			npc.GossipOptionTrainer, "Train me."))
	case wire.CMSGGossipSelectOption:
		b := wire.NewReader(payload)
		g := wire.ReadPackedGUID(b)
		menu, option := b.ReadI32(), b.ReadI32()
		if g != trainerNPCGUID || menu != trainerMenuID {
			f.t.Fatalf("the select names %s menu %d option %d", g, menu, option)
		}
		if tr.twoOptions && option == trainerOption+1 {
			// The second row is another trainer service: its list carries its own id.
			tr.selected = option
			tr.deliver(wire.SMSGTrainerList, trainerListPayload(trainerNPCGUID, trainerID+1, tr.offers()))
			return
		}
		if option != trainerOption {
			f.t.Fatalf("the select names %s menu %d option %d", g, menu, option)
		}
		tr.selected = option
		tr.deliver(wire.SMSGTrainerList, trainerListPayload(trainerNPCGUID, trainerID, tr.offers()))
	case wire.CMSGTrainerList:
		tr.listSends++
		if g := guidOf(payload); g != trainerNPCGUID {
			f.t.Fatalf("CMSG_TRAINER_LIST names %s", g)
		}
		tr.deliver(wire.SMSGTrainerList, trainerListPayload(trainerNPCGUID, trainerID, tr.offers()))
	case wire.CMSGTrainerBuySpell:
		tr.buys++
		b := wire.NewReader(payload)
		if g := wire.ReadPackedGUID(b); g != trainerNPCGUID {
			f.t.Fatalf("CMSG_TRAINER_BUY_SPELL names %s", g)
		}
		tr.lastID, tr.lastSpell = b.ReadI32(), b.ReadI32()
		if b.Remaining() != 0 || b.Err() != nil {
			f.t.Fatalf("the purchase has %d byte(s) left: %v", b.Remaining(), b.Err())
		}
		if tr.refuse {
			tr.deliver(wire.SMSGTrainerBuyFail, trainerBuyFailedPayload(trainerNPCGUID, tr.lastSpell, npc.TrainerFailNotEnoughMoney))
			return
		}
		if !tr.noCharge {
			// The core charges before it learns (Trainer::TeachSpell: ModifyMoney, then LearnSpell), and the money
			// arrives as an ActivePlayerData update.
			if o, ok := tr.world.Object(itemSelf); ok && o.Info.Player != nil {
				cost := uint64(0)
				for _, sp := range tr.offers() {
					if sp.SpellID == tr.lastSpell {
						cost = uint64(sp.MoneyCost)
					}
				}
				coin := o.Info.Player.Coinage
				if coin >= cost {
					coin -= cost
				}
				tr.server(wire.SMSGUpdateObject, activeUpdate(nil, &coin))
			}
		}
		if len(tr.burst) != 0 {
			for _, id := range tr.burst {
				tr.deliver(wire.SMSGLearnedSpells, learnedSpellsPayload(id))
			}
			return
		}
		if tr.taught != 0 {
			tr.deliver(wire.SMSGLearnedSpells, learnedSpellsTaughtPayload(tr.taught, tr.lastSpell))
			return
		}
		if tr.learnedAnswer != nil {
			tr.deliver(wire.SMSGLearnedSpells, tr.learnedAnswer)
			return
		}
		tr.deliver(wire.SMSGLearnedSpells, learnedSpellsPayload(tr.lastSpell))
	}
}

// guidOf reads the packed GUID that begins a client packet.
func guidOf(payload []byte) wire.GUID {
	return wire.ReadPackedGUID(wire.NewReader(payload))
}

// offers is the list the fake's trainer answers with: the fixed rows, or the test's own.
func (tr *trainerWorld) offers() []npc.TrainerSpell {
	if tr.spells != nil {
		return tr.spells
	}
	return trainerSpells()
}

// trainerSpells is the list the fake's trainer offers.
func trainerSpells() []npc.TrainerSpell {
	return []npc.TrainerSpell{
		{SpellID: trainerSpellA, MoneyCost: 250, ReqLevel: 5},
		{SpellID: trainerSpellB, MoneyCost: 55, ReqLevel: 8, ReqSkillLine: 171, ReqSkillRank: 75},
	}
}

// trainerListPayload writes SMSG_TRAINER_LIST the way TrainerList::Write does.
func trainerListPayload(trainer wire.GUID, id uint32, spells []npc.TrainerSpell) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, trainer)
	b.WriteU8(0) // TRAINER_TYPE_CLASS
	b.WriteU32(id)
	b.WriteU32(uint32(len(spells)))
	for _, s := range spells {
		b.WriteI32(s.SpellID)
		b.WriteI32(0)
		b.WriteU32(s.MoneyCost)
		b.WriteU8(s.ReqLevel)
		b.WriteU32(s.ReqSkillLine)
		b.WriteU32(s.ReqSkillRank)
		for _, a := range s.ReqAbility {
			b.WriteI32(a)
		}
	}
	const greeting = "Train with me."
	b.WriteBits(uint64(len(greeting)), 11)
	b.FlushBits()
	b.WriteString(greeting)
	return b.Bytes()
}

// trainerBuyFailedPayload writes SMSG_TRAINER_BUY_FAILED.
func trainerBuyFailedPayload(trainer wire.GUID, spell, reason int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, trainer)
	b.WriteI32(spell)
	b.WriteI32(reason)
	return b.Bytes()
}

// levelUpdate is one UnitData values update of Level (retail bit 40) for the character.
func levelUpdate(level int32) []byte {
	body := unitValuesBody(40, func(v *wire.Buffer) { v.WriteI32(level) })
	block := wire.NewWriter()
	block.WriteU8(0) // UPDATETYPE_VALUES
	wire.WritePackedGUID(block, itemSelf)
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

// learnedSpellsPayload writes SMSG_LEARNED_SPELLS with one spell and no optional fields.
func learnedSpellsPayload(spell int32) []byte {
	b := wire.NewWriter()
	b.WriteU32(1) // count
	b.WriteU32(0) // SpecializationID
	b.WriteI32(0) // MinActionBarSlot
	b.WriteI32(spell)
	b.WriteBit(false) // Favorite
	b.WriteBit(false) // EquipableSpellInvSlot
	b.WriteBit(false) // Superceded
	b.WriteBit(false) // TraitDefinitionID
	b.FlushBits()
	b.WriteBit(false) // SuppressMessaging
	b.WriteBit(false) // TraitGrantedByAura
	b.FlushBits()
	return b.Bytes()
}

// learnedSpellsTaughtPayload writes SMSG_LEARNED_SPELLS with one spell whose Superceded link names the bought list
// spell: the rank a castable learn teaches (Trainer::TeachSpell casts it, and LearnSpell writes the chain back).
func learnedSpellsTaughtPayload(taught, superseded int32) []byte {
	b := wire.NewWriter()
	b.WriteU32(1) // count
	b.WriteU32(0) // SpecializationID
	b.WriteI32(0) // MinActionBarSlot
	b.WriteI32(taught)
	b.WriteBit(false) // Favorite
	b.WriteBit(false) // EquipableSpellInvSlot
	b.WriteBit(true)  // Superceded
	b.WriteBit(false) // TraitDefinitionID
	b.FlushBits()
	b.WriteI32(superseded)
	b.WriteBit(false) // SuppressMessaging
	b.WriteBit(false) // TraitGrantedByAura
	b.FlushBits()
	return b.Bytes()
}

// twoTrainerMenuPayload writes a gossip menu with two "Train me." options (ids trainerOption and trainerOption+1):
// the writer flushes each option's bit block before its strings (NPCPackets.cpp:42-72), so every option carries its
// own block, flush and text bytes.
func twoTrainerMenuPayload(unit wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	b.WriteI32(trainerMenuID)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteU32(2) // two options
	b.WriteU32(0) // no quest text
	const text = "Train me."
	for _, id := range []int32{trainerOption, trainerOption + 1} {
		b.WriteI32(id)
		b.WriteU32(npc.GossipOptionTrainer)
		b.WriteU8(0)  // option flags
		b.WriteU64(0) // cost
		b.WriteU32(0) // language
		b.WriteU32(0) // no treasure
		b.WriteI32(0) // flags
		b.WriteI32(0) // order
		b.WriteBits(uint64(len(text)), 12)
		b.WriteBits(0, 12) // confirm
		b.WriteBits(0, 2)  // status
		b.WriteBit(false)  // no spell
		b.WriteBit(false)  // no icon
		b.WriteBits(1, 8)  // empty SizedCString: length 1, no bytes
		b.FlushBits()
		b.WriteString(text)
	}
	b.WriteBit(false) // no random text
	b.WriteBit(false) // no broadcast text
	b.FlushBits()
	return b.Bytes()
}

// lastActLine is the final line the step printed.
func lastActLine(a *actState) string {
	if len(a.lines) == 0 {
		return ""
	}
	return a.lines[len(a.lines)-1]
}

// TestTrainerOpenUsesTheTrainMeOption: a gossip trainer opens through its option, and the list arrives without a
// CMSG_TRAINER_LIST.
func TestTrainerOpenUsesTheTrainMeOption(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.state(t)
	if err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer}); err != nil {
		t.Fatalf("the trainer step: %v", err)
	}
	if !tr.talked || tr.selected != trainerOption {
		t.Fatalf("the step talked %v and selected %d", tr.talked, tr.selected)
	}
	if tr.listSends != 0 {
		t.Fatalf("the step also sent CMSG_TRAINER_LIST %d time(s)", tr.listSends)
	}
	if !a.hasTrainer || a.trainer == nil || a.trainer.TrainerID != trainerID || len(a.trainer.Spells) != 2 {
		t.Fatalf("the step kept %+v", a.trainer)
	}
	var want string
	for _, line := range a.lines {
		if line == "trainer spell 1234: 250 copper, level 5" {
			want = line
		}
	}
	if want == "" {
		t.Fatalf("the spell line is missing: %q", a.lines)
	}
	if got := lastActLine(a); got != fmt.Sprintf("step 0 trainer ok entry %d, trainer %d, 2 spell(s)", trainerEntry, trainerID) {
		t.Fatalf("the step line is %q", got)
	}
	if kind := lastEventKind(tr.s); kind != events.KindTrainerList {
		t.Fatalf("the last event is %q, want %q", kind, events.KindTrainerList)
	}
}

// TestTrainerWithoutGossipAsksForTheList: a trainer with no gossip menu has no option to select, so the step sends
// the Hello-shaped ask and nothing else.
func TestTrainerWithoutGossipAsksForTheList(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagTrainer)
	a := tr.state(t)
	if err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer}); err != nil {
		t.Fatalf("the trainer step: %v", err)
	}
	if tr.listSends != 1 || tr.talked || tr.selected != 0 {
		t.Fatalf("the step sent %d lists, talked %v, selected %d", tr.listSends, tr.talked, tr.selected)
	}
	if !a.hasTrainer {
		t.Fatal("the step did not keep the list")
	}
}

// TestLearnBuyNamesTheListIDAndConfirmsWithLearnedSpells: the purchase carries the list's TrainerID and the spell,
// and only a learned-spell answer that names the spell confirms it.
func TestLearnBuyNamesTheListIDAndConfirmsWithLearnedSpells(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.state(t)
	if err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer}); err != nil {
		t.Fatalf("the trainer step: %v", err)
	}
	before := len(tr.sent)
	if err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}}); err != nil {
		t.Fatalf("the learn step: %v", err)
	}
	if tr.buys != 1 || tr.lastID != trainerID || tr.lastSpell != trainerSpellA {
		t.Fatalf("the purchase sent %d, id %d, spell %d", tr.buys, tr.lastID, tr.lastSpell)
	}
	if len(tr.sent) != before+1 || tr.sent[len(tr.sent)-1].core != wire.CMSGTrainerBuySpell {
		t.Fatalf("the learn step sent %v", cores(tr.sent[before:]))
	}
	if got := lastActLine(a); !contains(got, "spell 1234 learned") {
		t.Fatalf("the step line is %q", got)
	}
	found := false
	for _, spell := range tr.s.known {
		if spell == trainerSpellA {
			found = true
		}
	}
	if !found {
		t.Fatalf("the learned spell did not enter the spellbook: %v", tr.s.known)
	}
	if kind := lastEventKind(tr.s); kind != events.KindSpellLearned {
		t.Fatalf("the last event is %q, want %q", kind, events.KindSpellLearned)
	}
}

// TestLearnRefusalFailsWithTheReason: a refused purchase fails with the server's reason and never reports success.
func TestLearnRefusalFailsWithTheReason(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	tr.refuse = true
	a := tr.state(t)
	if err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer}); err != nil {
		t.Fatalf("the trainer step: %v", err)
	}
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "not_enough_money") {
		t.Fatalf("the learn step answered %v", err)
	}
	if got := lastActLine(a); !contains(got, "fail") || !contains(got, "not_enough_money") {
		t.Fatalf("the step line is %q", got)
	}
	for _, spell := range tr.s.known {
		if spell == trainerSpellA {
			t.Fatal("a refused purchase entered the spellbook")
		}
	}
}

// TestLearnWithoutTheListSendsNothing: a purchase names the list's id and needs the interaction the list started, so
// the step refuses before sending when no trainer step ran.
func TestLearnWithoutTheListSendsNothing(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.state(t)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "no open trainer list") {
		t.Fatalf("the learn step answered %v", err)
	}
	if len(tr.sent) != 0 {
		t.Fatalf("the learn step sent %v", cores(tr.sent))
	}
}

// TestLearnUnknownSpellRefusedBeforeSending: the spell must be one the list offered.
func TestLearnUnknownSpellRefusedBeforeSending(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.state(t)
	if err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer}); err != nil {
		t.Fatalf("the trainer step: %v", err)
	}
	before := len(tr.sent)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "9999"}})
	if err == nil || !contains(err.Error(), "has no spell 9999") {
		t.Fatalf("the learn step answered %v", err)
	}
	if len(tr.sent) != before {
		t.Fatalf("the learn step sent %v after the list", cores(tr.sent[before:]))
	}
}

// TestTrainerListFromAnotherUnitIsIgnored: the wait takes only this NPC's list, so another creature's answer cannot
// stand in for it.
func TestTrainerListFromAnotherUnitIsIgnored(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.state(t)
	// Answer the select with a list for another GUID and never with this NPC's.
	other := wire.GUID{Low: 0xFEEDFACE, High: uint64(8) << 58}
	tr.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGGossipSelectOption {
			tr.deliver(wire.SMSGTrainerList, trainerListPayload(other, trainerID, trainerSpells()))
			return
		}
		tr.answer(f, core, payload)
	}
	err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer})
	if err == nil || !contains(err.Error(), "no trainer list") {
		t.Fatalf("the step answered %v", err)
	}
	if a.hasTrainer {
		t.Fatal("the step kept another unit's list")
	}
}

// lastEventKind is the kind of the newest event in the session's log.
func lastEventKind(s *session) string {
	rows, _, _, _ := s.EventLog().Since(0, 1000)
	if len(rows) == 0 {
		return ""
	}
	return rows[len(rows)-1].Kind
}

// cores lists the client opcodes of sent packets.
func cores(sent []itemSent) []string {
	out := make([]string, 0, len(sent))
	for _, s := range sent {
		out = append(out, wire.ClientName(s.core))
	}
	return out
}

// TestTrainerGossipOnlyOpensTheList: the core opens a trainer's list through a gossip trainer option even when the
// creature carries no trainer bit (Player.cpp:15304-15312, NPCHandler.cpp:122-124), so the step must not require the
// bit before the menu has had its say.
func TestTrainerGossipOnlyOpensTheList(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip)
	a := tr.state(t)
	if err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer}); err != nil {
		t.Fatalf("the trainer step: %v", err)
	}
	if !tr.talked || tr.listSends != 0 {
		t.Fatalf("the step talked %v and sent %d lists", tr.talked, tr.listSends)
	}
	if !a.hasTrainer || len(a.trainer.Spells) != 2 {
		t.Fatalf("the step kept %+v", a.trainer)
	}
	if err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}}); err != nil {
		t.Fatalf("the learn step: %v", err)
	}
	if tr.buys != 1 {
		t.Fatalf("the step sent %d purchase(s)", tr.buys)
	}
}

// TestTrainerWithoutTheOptionRefused: a gossip NPC whose menu has no trainer option has no list to open, and the step
// fails with that reason before sending anything else.
func TestTrainerWithoutTheOptionRefused(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip)
	tr.noTrainerOption = true
	a := tr.state(t)
	err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer})
	if err == nil || !contains(err.Error(), "no trainer option") {
		t.Fatalf("the step answered %v", err)
	}
	if got := lastActLine(a); !contains(got, "fail") {
		t.Fatalf("the step line is %q", got)
	}
}

// TestTrainerNeitherFlagRefused: an NPC with neither the trainer bit nor a gossip menu has no trainer flow at all, so
// the step sends nothing.
func TestTrainerNeitherFlagRefused(t *testing.T) {
	tr := newTrainerWorld(t, 0)
	a := tr.state(t)
	err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer})
	if err == nil || !contains(err.Error(), "not a trainer") {
		t.Fatalf("the step answered %v", err)
	}
	if len(tr.sent) != 0 {
		t.Fatalf("the step sent %v", cores(tr.sent))
	}
}

// trainerListWith is the opened trainer's state with the fake's list.
func (tr *trainerWorld) openList(t *testing.T) *actState {
	t.Helper()
	a := tr.state(t)
	if err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer}); err != nil {
		t.Fatalf("the trainer step: %v", err)
	}
	return a
}

// TestLearnWithoutTheMoneySendsNothing: the list's price is compared with the coinage before the purchase, and the
// refusal names both.
func TestLearnWithoutTheMoneySendsNothing(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.openList(t)
	fundSelf(tr.itemWorld, 10)
	before := len(tr.sent)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "250") || !contains(err.Error(), "10") {
		t.Fatalf("the learn step answered %v", err)
	}
	if len(tr.sent) != before {
		t.Fatalf("the learn step sent %v", cores(tr.sent[before:]))
	}
}

// TestLearnKnownSpellSendsNothing: a spell the character already knows is not bought again.
func TestLearnKnownSpellSendsNothing(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.openList(t)
	tr.s.known = append(tr.s.known, trainerSpellA)
	before := len(tr.sent)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "already knows") {
		t.Fatalf("the learn step answered %v", err)
	}
	if len(tr.sent) != before {
		t.Fatalf("the learn step sent %v", cores(tr.sent[before:]))
	}
}

// TestLearnLevelTooLowSendsNothing: the row's required level is compared with the character's own before the
// purchase.
func TestLearnLevelTooLowSendsNothing(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.openList(t)
	// The character's own level arrives as a UnitData values update (retail bit 40, Level).
	tr.server(wire.SMSGUpdateObject, levelUpdate(1))
	before := len(tr.sent)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "level 1") || !contains(err.Error(), "level 5") {
		t.Fatalf("the learn step answered %v", err)
	}
	if len(tr.sent) != before {
		t.Fatalf("the learn step sent %v", cores(tr.sent[before:]))
	}
}

// TestLearnMissingAbilitySendsNothing: a row naming a required spell is refused while the character does not know it
// (the core's own check for a non-pet trainer, Trainer.cpp GetSpellState).
func TestLearnMissingAbilitySendsNothing(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	tr.onSend = func(f *itemWorld, core uint32, payload []byte) {
		switch core {
		case wire.CMSGGossipSelectOption:
			tr.deliver(wire.SMSGTrainerList, trainerListPayload(trainerNPCGUID, trainerID, []npc.TrainerSpell{
				{SpellID: trainerSpellA, MoneyCost: 10, ReqLevel: 1, ReqAbility: [3]int32{4242, 0, 0}},
			}))
		default:
			tr.answer(f, core, payload)
		}
	}
	a := tr.openList(t)
	before := len(tr.sent)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "4242") {
		t.Fatalf("the learn step answered %v", err)
	}
	if len(tr.sent) != before {
		t.Fatalf("the learn step sent %v", cores(tr.sent[before:]))
	}
	// With the ability known the same row is bought.
	tr.s.known = append(tr.s.known, 4242)
	if err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}}); err != nil {
		t.Fatalf("the learn step with the ability: %v", err)
	}
}

// TestLearnSeesAGMMoneyChangeAfterLogin: the pre-check reads the model's coinage, which a money change after login
// carries as an ActivePlayerData values update (the shape a GM money line sends); the login purse alone does not
// decide the purchase.
func TestLearnSeesAGMMoneyChangeAfterLogin(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.openList(t)
	low := uint64(10)
	tr.server(wire.SMSGUpdateObject, activeUpdate(nil, &low))
	before := len(tr.sent)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "250") || !contains(err.Error(), "10") {
		t.Fatalf("the learn step answered %v", err)
	}
	if len(tr.sent) != before {
		t.Fatalf("the learn step sent %v", cores(tr.sent[before:]))
	}
	high := uint64(1000)
	tr.server(wire.SMSGUpdateObject, activeUpdate(nil, &high))
	if err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}}); err != nil {
		t.Fatalf("the learn step after the money came back: %v", err)
	}
	if got := lastActLine(a); !contains(got, "1000 -> 750 copper (est., 250 spent)") {
		t.Fatalf("the step line is %q", got)
	}
}

// TestLearnUnknownLevelSendsNothing: without the character's own level the required level cannot be checked, so the
// purchase is refused instead of sent on a guess.
func TestLearnUnknownLevelSendsNothing(t *testing.T) {
	tr := newTrainerWorldLevel(t, npc.NPCFlagGossip|npc.NPCFlagTrainer, 0)
	a := tr.openList(t)
	before := len(tr.sent)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "level is not in the world model") {
		t.Fatalf("the learn step answered %v", err)
	}
	if len(tr.sent) != before {
		t.Fatalf("the learn step sent %v", cores(tr.sent[before:]))
	}
}

// TestLearnUnknownInventorySendsNothing: without the character's own create the price cannot be checked, so the
// purchase is refused instead of sent on a guess.
func TestLearnUnknownInventorySendsNothing(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.openList(t)
	tr.s.selfSeen = false
	tr.s.report.ActiveMoverGUID = wire.GUID{}
	before := len(tr.sent)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "own create has not arrived") {
		t.Fatalf("the learn step answered %v", err)
	}
	if len(tr.sent) != before {
		t.Fatalf("the learn step sent %v", cores(tr.sent[before:]))
	}
}

// TestLearnStaleInventorySendsNothing: an inventory that lost a change this client could not decode may show a
// wrong coinage, so the purchase is refused instead of sent on it.
func TestLearnStaleInventorySendsNothing(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.openList(t)
	tr.s.inv.MarkStale("a test update the client could not decode")
	before := len(tr.sent)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "lost a change") {
		t.Fatalf("the learn step answered %v", err)
	}
	if len(tr.sent) != before {
		t.Fatalf("the learn step sent %v", cores(tr.sent[before:]))
	}
}

// TestLearnWaitsForTheCharge: a learned-spell answer without the fare leaving the purse is not this purchase, and the
// step says so instead of reporting a free spell.
func TestLearnWaitsForTheCharge(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	tr.noCharge = true
	a := tr.openList(t)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "without the charge") {
		t.Fatalf("the learn step answered %v", err)
	}
}

// TestLearnSuccessLineReadsTheCoinageBack: a purchase prints the money the inventory reads back after it.
func TestLearnSuccessLineReadsTheCoinageBack(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.openList(t)
	if err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}}); err != nil {
		t.Fatalf("the learn step: %v", err)
	}
	if got := lastActLine(a); !contains(got, "spell 1234 learned") || !contains(got, "1000 -> 750 copper (est., 250 spent)") {
		t.Fatalf("the step line is %q", got)
	}
}
