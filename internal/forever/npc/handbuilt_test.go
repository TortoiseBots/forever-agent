package npc

// Hand-built layouts for the three server answers no capture fixture of this round carries with fields set: the
// interaction-open result and the gossip complete need non-default values to prove the parse reads them, and the
// option-NPC interaction needs its optional faction block present and absent. Each payload is laid out from the
// server's writer, cited field by field, and every non-empty block is followed by a sentinel field whose value is
// known, so a reader that drops the optional or misreads a bit fails.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestHandBuiltInteractionOpenResult reads the open result with Success set and the merchant interaction type: the
// NPC GUID's own bytes are the sentinel before the type, and Success must be true after it.
func TestHandBuiltInteractionOpenResult(t *testing.T) {
	g := wire.GUID{Low: 0x40, High: uint64(8) << 58} // creature-shaped, like the gossip fixture's
	b := wire.NewWriter()
	wire.WritePackedGUID(b, g)
	b.WriteI32(5) // PlayerInteractionType::Merchant (DBCEnums.h)
	b.WriteBit(true)
	b.FlushBits()
	r, err := ParseInteractionOpenResult(b.Bytes())
	if err != nil {
		t.Fatalf("ParseInteractionOpenResult: %v", err)
	}
	if r.NPC != g {
		t.Fatalf("the NPC is %s, want %s", r.NPC, g)
	}
	if r.InteractionType != 5 {
		t.Fatalf("the interaction type is %d, want merchant 5", r.InteractionType)
	}
	if !r.Success {
		t.Fatal("the result is not a success")
	}
}

// TestHandBuiltInteractionOpenResultSilentFailure reads the same packet with Success clear: a refused open must
// parse, and Success must report it.
func TestHandBuiltInteractionOpenResultSilentFailure(t *testing.T) {
	g := wire.GUID{Low: 0x41, High: uint64(8) << 58}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, g)
	b.WriteI32(3) // PlayerInteractionType::Gossip
	b.WriteBit(false)
	b.FlushBits()
	r, err := ParseInteractionOpenResult(b.Bytes())
	if err != nil {
		t.Fatalf("ParseInteractionOpenResult: %v", err)
	}
	if r.Success {
		t.Fatal("the refused open parses as a success")
	}
	// A trailing byte is not the layout: the fixture rule refuses it.
	if _, err := ParseInteractionOpenResult(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

// TestHandBuiltGossipComplete reads the complete with the suppress-sound bit set: the one-bit packet must carry
// the value, and an empty reader state must follow it.
func TestHandBuiltGossipComplete(t *testing.T) {
	b := wire.NewWriter()
	b.WriteBit(true)
	b.FlushBits()
	c, err := ParseGossipComplete(b.Bytes())
	if err != nil {
		t.Fatalf("ParseGossipComplete: %v", err)
	}
	if !c.SuppressSound {
		t.Fatal("the complete does not suppress the sound")
	}
	b2 := wire.NewWriter()
	b2.WriteBit(false)
	b2.FlushBits()
	quiet, err := ParseGossipComplete(b2.Bytes())
	if err != nil {
		t.Fatalf("ParseGossipComplete: %v", err)
	}
	if quiet.SuppressSound {
		t.Fatal("the quiet complete suppresses the sound")
	}
}

// TestHandBuiltGossipOptionInteraction reads the option interaction with and without its optional faction id: the
// present faction is the sentinel that proves the optional block was walked, and the absent one must leave no
// trailing byte.
func TestHandBuiltGossipOptionInteraction(t *testing.T) {
	g := wire.GUID{Low: 0x42, High: uint64(8) << 58}
	with := wire.NewWriter()
	wire.WritePackedGUID(with, g)
	with.WriteI32(1) // Vendor
	with.WriteBit(true)
	with.FlushBits()
	with.WriteI32(17) // the optional friendship faction
	r, err := ParseGossipOptionInteraction(with.Bytes())
	if err != nil {
		t.Fatalf("ParseGossipOptionInteraction: %v", err)
	}
	if r.GossipGUID != g || r.OptionNPC != 1 {
		t.Fatalf("the interaction names %s option %d", r.GossipGUID, r.OptionNPC)
	}
	if !r.HasFaction || r.FriendshipFaction != 17 {
		t.Fatalf("the faction is %v/%d, want present 17", r.HasFaction, r.FriendshipFaction)
	}
	without := wire.NewWriter()
	wire.WritePackedGUID(without, g)
	without.WriteI32(3) // Trainer
	without.WriteBit(false)
	without.FlushBits()
	plain, err := ParseGossipOptionInteraction(without.Bytes())
	if err != nil {
		t.Fatalf("ParseGossipOptionInteraction: %v", err)
	}
	if plain.HasFaction || plain.FriendshipFaction != 0 {
		t.Fatalf("the factionless interaction carries %v/%d", plain.HasFaction, plain.FriendshipFaction)
	}
}

// TestQuestGiverHelloWritesOnlyTheGUID: CMSG_QUEST_GIVER_HELLO is the giver's packed GUID and nothing else
// (QuestPackets.cpp:69-72). The opcode on the wire is what tells it from TALK_TO_GOSSIP: the payloads share the
// one-GUID shape, so there is nothing to compare here beyond the single GUID.
func TestQuestGiverHelloWritesOnlyTheGUID(t *testing.T) {
	g := wire.GUID{Low: 0x77, High: uint64(8) << 58}
	got := BuildQuestGiverHello(g)
	b := wire.NewReader(got)
	if back := wire.ReadPackedGUID(b); back != g {
		t.Fatalf("the hello names %s, want %s", back, g)
	}
	if b.Err() != nil || b.Remaining() != 0 {
		t.Fatalf("the hello is not one GUID: %v, %d byte(s) left", b.Err(), b.Remaining())
	}
}

// questListPayload lays out SMSG_QUEST_GIVER_QUEST_LIST_MESSAGE from the server's writer (QuestPackets.cpp:619-635):
// the giver, two emote words, the quest count, each quest as a ClientGossipText (NPCPackets.cpp:74-92), the 11-bit
// greeting length flushed, then the greeting bytes.
func questListPayload(g wire.GUID, titles []string, greeting string) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, g)
	b.WriteU32(7) // GreetEmoteDelay
	b.WriteU32(3) // GreetEmoteType
	b.WriteU32(uint32(len(titles)))
	for i, title := range titles {
		b.WriteI32(int32(1000 + i)) // QuestID
		b.WriteI32(0)               // ContentTuningID
		b.WriteI32(0)               // QuestType
		b.WriteI32(0)               // QuestInfoID
		b.WriteBytes(make([]byte, 16))
		for range 4 {
			b.WriteBit(false)
		}
		b.WriteBits(uint64(len(title)), 9)
		b.FlushBits()
		b.WriteString(title)
	}
	b.WriteBits(uint64(len(greeting)), 11)
	b.FlushBits()
	b.WriteString(greeting)
	return b.Bytes()
}

// TestHandBuiltQuestGiverQuestList reads the quest-only NPC's answer with two quests and a greeting, the greeting
// being the sentinel after the variable quest blocks.
func TestHandBuiltQuestGiverQuestList(t *testing.T) {
	g := wire.GUID{Low: 0x42, High: uint64(8) << 58}
	p := questListPayload(g, []string{"A Small Favor", "Bigger Trouble"}, "Greetings, traveller.")
	l, err := ParseQuestGiverQuestList(p)
	if err != nil {
		t.Fatalf("ParseQuestGiverQuestList: %v", err)
	}
	if l.Giver != g || l.Quests != 2 || l.EmoteDelay != 7 || l.EmoteType != 3 || l.Greeting != "Greetings, traveller." {
		t.Fatalf("the list parsed as %+v", l)
	}
	if _, err := ParseQuestGiverQuestList(append(p, 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
	if _, err := ParseQuestGiverQuestList(p[:len(p)-3]); err == nil {
		t.Fatal("the parser accepted a short packet")
	}
}
