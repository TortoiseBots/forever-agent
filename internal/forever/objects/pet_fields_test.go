package objects

// The pet fields of UnitData (TBF #168 round P1): PetNumber, CreatedBySpell, CreatureFamily and PetFlags were
// skipped bytes in the create and the update walk. The create must decode them from their documented widths
// (UpdateFields.cpp UnitData::WriteCreate at the pinned core) and a values update must change only the field its
// mask names.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestPetFieldsInCreate: a pet create with PetNumber 7, CreatedBySpell 883, CreatureFamily 1 and PetFlags 2 decodes
// all four, and the block is still consumed to its declared end (the sentinel after them proves no width changed).
func TestPetFieldsInCreate(t *testing.T) {
	guid := wire.GUID{Low: 0x0CFE, High: 0x0F00000000000000}
	values := buildUnitCreate(unitCreateSpec{petNumber: 7, createdBySpell: 883, creatureFamily: 1, petFlags: 2})
	payload := buildCreate(guid, TypeUnit, buildMovementBlock(false, nil), values)
	u, err := ParseUpdateObject(payload)
	if err != nil {
		t.Fatalf("the pet create: %v", err)
	}
	b := u.Blocks[0]
	if b.Unparsed != ReasonNone {
		t.Fatalf("unparsed (%s): %s", b.Unparsed, b.UnparsedDetail)
	}
	if !b.ValuesComplete {
		t.Fatal("the pet create was not walked to its declared end")
	}
	if b.Info.PetNumber != 7 || b.Info.CreatedBySpell != 883 {
		t.Fatalf("PetNumber %d CreatedBySpell %d, want 7 and 883", b.Info.PetNumber, b.Info.CreatedBySpell)
	}
	if b.Info.CreatureFamily != 1 || b.Info.PetFlags != 2 {
		t.Fatalf("CreatureFamily %d PetFlags %d, want 1 and 2", b.Info.CreatureFamily, b.Info.PetFlags)
	}
}

// TestPetNumberUpdateMerges: a values update naming only the PetNumber bit (retail 72) changes PetNumber and leaves
// every other field, SummonedBy included, exactly as the model had it.
func TestPetNumberUpdateMerges(t *testing.T) {
	guid := wire.GUID{Low: 0x0CFE, High: 0x0F00000000000000}
	body := unitValuesBody(72, func(v *wire.Buffer) { v.WriteU32(9) })
	payload := buildValuesUpdate(guid, body)
	u, err := ParseUpdateObject(payload)
	if err != nil {
		t.Fatalf("the PetNumber update: %v", err)
	}
	src := u.Blocks[0].Info
	if !u.Blocks[0].ValuesComplete {
		t.Fatal("the PetNumber update was not consumed")
	}
	if src.PetNumber != 9 {
		t.Fatalf("the update decoded PetNumber %d, want 9", src.PetNumber)
	}

	summoner := wire.GUID{Low: 0x1234, High: 0x0F00000000000000}
	dst := Info{SummonedBy: summoner}
	dst.f.SummonedBy = true
	mergeInfo(&dst, src)
	if dst.PetNumber != 9 {
		t.Fatalf("merged PetNumber = %d, want 9", dst.PetNumber)
	}
	if dst.SummonedBy != summoner {
		t.Fatalf("merged SummonedBy = %v, want the stored %v", dst.SummonedBy, summoner)
	}
}
