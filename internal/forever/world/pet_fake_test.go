package world

// The pet layer (ticket #90, round P2) against the handler-boundary fake: the pet the character's own Summon names,
// the control bar the server sends, the orders pet_action writes, and the pet events S5's kinds name. The world
// model is fed fragments the way the dispatch feeds it, and the pet packets go through the session's own handler.

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/pet"
	"github.com/pjerra/forever-agent/internal/forever/tables"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

var (
	// petTestHunter is another player: the owner a pet's SummonedBy names in the units test.
	petTestHunter = wire.GUID{Low: 0x2002, High: 0x02000000000000F0}
	// petTestPet is the creature those fields name as a summon.
	petTestPet = wire.GUID{Low: 0x3003, High: uint64(objects.TypeUnit) << 58}
	// petTestPet2 is the pet that replaces it: a Summon that names another GUID is a dismissal and a summon.
	petTestPet2 = wire.GUID{Low: 0x3004, High: uint64(objects.TypeUnit) << 58}
	// petTestCreator is the GUID a pet's CreatedBy names: SummonedBy wins over it, so a creature carries both.
	petTestCreator = wire.GUID{Low: 0x4004, High: 0x02000000000000F0}
)

// petWorld is the item-world fake with the pet scenes' helpers: a unit create from decoded values, and the pet
// packets delivered through the session's own handler.
type petWorld struct {
	*itemWorld
}

// newPetWorld builds the session and its world.
func newPetWorld(t *testing.T) *petWorld {
	t.Helper()
	return &petWorld{itemWorld: newItemWorld(t)}
}

// fragmentUnitCreate is one unit's create block built from decoded values: the caller sets the fields a scene needs
// and Complete marks every one present, the way a create block carries them.
func fragmentUnitCreate(guid wire.GUID, info objects.Info) objects.WorldFragment {
	info.Type = objects.TypeUnit
	info.HasObject = true
	info.HasUnit = true
	info.Complete()
	return objects.WorldFragment{GUID: guid, ObjectType: objects.TypeUnit, Info: info, ValuesComplete: true}
}

// petRow is what the units JSON carries of one row's owner fields.
type petRow struct {
	Owner string `json:"owner"`
	Kind  string `json:"owner_kind"`
}

// TestServePetListsOwnerInUnits: a creature whose SummonedBy names the other player is listed with that owner and
// the owner's kind, and the owner filter keeps it and drops the rest.
func TestServePetListsOwnerInUnits(t *testing.T) {
	p := newPetWorld(t)
	p.apply(
		fragmentUnitCreate(petTestHunter, objects.Info{Entry: 1, Health: 100, MaxHealth: 100, Level: 10}),
		// Both owner GUIDs are set: the rule is SummonedBy first, so the answer must be summoned_by with the
		// hunter's GUID, not the creator's.
		fragmentUnitCreate(petTestPet, objects.Info{Entry: 299, Health: 80, MaxHealth: 80, Level: 10, SummonedBy: petTestHunter, CreatedBy: petTestCreator}),
	)
	units, err := p.s.serveUnits(serveUnitsParams{})
	if err != nil {
		t.Fatalf("units: %v", err)
	}
	var found *ServeUnit
	for i := range units {
		if units[i].GUID == GUID(petTestPet) {
			found = &units[i]
		}
	}
	if found == nil {
		t.Fatal("the pet is not in the units rows")
	}
	raw, _ := json.Marshal(*found)
	var row petRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("the row's JSON: %v", err)
	}
	if row.Owner != GUID(petTestHunter).String() || row.Kind != "summoned_by" {
		t.Fatalf("the pet's owner is %q (%q), want %q (summoned_by)", row.Owner, row.Kind, GUID(petTestHunter).String())
	}

	// owner=<the player> keeps the pet and drops everything whose owner is not that player.
	only, err := p.s.serveUnits(serveUnitsParams{Owner: GUID(petTestHunter)})
	if err != nil {
		t.Fatalf("units owner=: %v", err)
	}
	if len(only) != 1 || only[0].GUID != GUID(petTestPet) {
		t.Fatalf("units owner= answered %d row(s), want the pet alone: %+v", len(only), only)
	}
}

// petUnitEntry is the pet creature's entry in these scenes.
const petUnitEntry = 299

// update feeds one SMSG_UPDATE_OBJECT through the dispatch's own path: the session's models, then the world, then
// the pet check the dispatch runs beside it.
func (p *petWorld) update(payload []byte) {
	p.t.Helper()
	u, err := objects.ParseUpdateObjectForBuild(payload, 70707)
	if err != nil {
		p.t.Fatalf("the fake update: %v", err)
	}
	p.s.applyUpdate(u)
	p.world.Apply(u, p.now)
	p.s.petCheck()
}

// deliver feeds one server packet the way the dispatch does.
func (p *petWorld) deliver(core uint32, payload []byte) {
	p.t.Helper()
	switch core {
	case wire.SMSGPetSpellsMessage, wire.SMSGPetMode, wire.SMSGPetCastFailed, wire.SMSGPetActionFeedback:
		if err := p.s.handlePet(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}); err != nil {
			p.t.Fatalf("%s: %v", wire.ServerName(core), err)
		}
	case wire.SMSGUpdateObject:
		p.update(payload)
	default:
		p.itemWorld.server(core, payload)
	}
}

// summonSelf sets (or clears, with the empty GUID) the character's own Summon: retail bit 15 (UnitData).
func (p *petWorld) summonSelf(g wire.GUID) {
	p.t.Helper()
	p.update(buildUnitValuesUpdate(itemSelf, unitValuesBody(15, func(v *wire.Buffer) {
		wire.WritePackedGUID(v, g)
	})))
}

// mountSelf sets the character's own MountDisplayID: retail bit 61.
func (p *petWorld) mountSelf(display int32) {
	p.t.Helper()
	p.update(buildUnitValuesUpdate(itemSelf, unitValuesBody(61, func(v *wire.Buffer) { v.WriteI32(display) })))
}

// petSpellsBody is one SMSG_PET_SPELLS_MESSAGE (PetPackets.cpp:23-66): the pet, family, specialization, time limit,
// the command, flag and react bytes, the ten bar buttons and the Classic 1.60 tail of eight, then the action,
// cooldown and history counts and their rows.
func petSpellsBody(petGUID wire.GUID, command, flag, react uint8, bar [pet.BarSlots]uint32, extra [pet.BarExtra]uint32, actions []uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, petGUID)
	b.WriteU16(1) // family
	b.WriteU16(0) // specialization
	b.WriteU32(0) // time limit
	b.WriteU8(command)
	b.WriteU8(flag)
	b.WriteU8(react)
	for _, btn := range bar {
		b.WriteU32(btn)
	}
	for _, btn := range extra {
		b.WriteU32(btn)
	}
	b.WriteU32(uint32(len(actions)))
	b.WriteU32(1) // one cooldown
	b.WriteU32(1) // one history entry
	for _, a := range actions {
		b.WriteU32(a)
	}
	b.WriteI32(172)   // cooldown spell
	b.WriteI32(5000)  // duration
	b.WriteI32(9000)  // category duration
	b.WriteFloat32(1) // mod rate
	b.WriteU16(133)   // category
	b.WriteI32(133)   // history category
	b.WriteI32(10000) // recovery
	b.WriteFloat32(1) // charge mod rate
	b.WriteU8(0)      // consumed
	return b.Bytes()
}

// petSpellsWithSpells is the bar a hunter's pet gets: the command buttons 0-2, two spell buttons at 3 and 4, and
// one passive button at 5 (the core's own bar shape, PetPackets.cpp:36-41).
func petSpellsWithSpells(petGUID wire.GUID) []byte {
	var bar [pet.BarSlots]uint32
	bar[0] = pet.Button(pet.CmdStay, pet.ActCommand)
	bar[1] = pet.Button(pet.CmdFollow, pet.ActCommand)
	bar[2] = pet.Button(pet.CmdAttack, pet.ActCommand)
	bar[3] = pet.Button(172, pet.ActEnabled)
	bar[4] = pet.Button(2649, pet.ActDisabled)
	bar[5] = pet.Button(26064, pet.ActPassive)
	bar[7] = pet.Button(uint32(pet.ReactPassive), pet.ActReaction)
	bar[8] = pet.Button(uint32(pet.ReactDefensive), pet.ActReaction)
	bar[9] = pet.Button(uint32(pet.ReactAggressive), pet.ActReaction)
	var extra [pet.BarExtra]uint32
	extra[4] = 0x16000000 // the core's fixed tail word
	return petSpellsBody(petGUID, uint8(pet.CmdFollow), 0, pet.ReactAssist, bar, extra, nil)
}

// lastSent answers the last packet of one opcode the session wrote.
func (p *petWorld) lastSent(t *testing.T, core uint32) []byte {
	t.Helper()
	for i := len(p.sent) - 1; i >= 0; i-- {
		if p.sent[i].core == core {
			return p.sent[i].payload
		}
	}
	t.Fatalf("the session sent no %s", wire.ServerName(core))
	return nil
}

// sentCount counts the packets of one opcode the session wrote.
func (p *petWorld) sentCount(core uint32) int {
	n := 0
	for _, s := range p.sent {
		if s.core == core {
			n++
		}
	}
	return n
}

// actionButton reads the packed button out of a CMSG_PET_ACTION body.
func actionButton(t *testing.T, payload []byte) uint32 {
	t.Helper()
	r := wire.NewReader(payload)
	wire.ReadPackedGUID(r)
	btn := r.ReadU32()
	if r.Err() != nil {
		t.Fatalf("the pet action body: %v", r.Err())
	}
	return btn
}

// TestServePetBarAndOrders: the character's own Summon names a pet, the bar names two spells, and each order writes
// the core's own packet with the bar's button.
func TestServePetBarAndOrders(t *testing.T) {
	p := newPetWorld(t)
	p.apply(fragmentUnitCreate(itemSelf, objects.Info{Entry: 1, Health: 100, MaxHealth: 100, Level: 10, Summon: petTestPet}))
	p.apply(fragmentUnitCreate(petTestPet, objects.Info{Entry: petUnitEntry, Health: 80, MaxHealth: 80, Level: 10, SummonedBy: itemSelf}))

	// Without the bar: no bar, no spells.
	if info := p.s.servePet(); !info.Present || info.Bar || len(info.Spells) != 0 {
		t.Fatalf("pet before the bar: %+v", info)
	}
	p.deliver(wire.SMSGPetSpellsMessage, petSpellsWithSpells(petTestPet))

	info := p.s.servePet()
	if !info.Present || !info.Bar || !info.Enabled {
		t.Fatalf("pet with the bar: %+v", info)
	}
	if info.React != "assist" || info.Command != "follow" {
		t.Fatalf("the bar's react/command: %q/%q", info.React, info.Command)
	}
	if len(info.Spells) != 3 {
		t.Fatalf("the bar's spells: %+v", info.Spells)
	}
	if info.Spells[0].Slot != 3 || info.Spells[0].SpellID != 172 || !info.Spells[0].Autocast {
		t.Fatalf("slot 3: %+v", info.Spells[0])
	}
	if info.Spells[1].Slot != 4 || info.Spells[1].SpellID != 2649 || info.Spells[1].Autocast {
		t.Fatalf("slot 4: %+v", info.Spells[1])
	}
	if info.Spells[2].Slot != 5 || !info.Spells[2].Passive {
		t.Fatalf("slot 5: %+v", info.Spells[2])
	}

	// follow: the core's CMSG_PET_ACTION with the command button 0x07000001.
	if _, err := p.s.servePetAction(context.Background(), servePetActionParams{Action: "follow"}); err != nil {
		t.Fatalf("pet_action follow: %v", err)
	}
	want := pet.BuildAction(petTestPet, pet.Button(pet.CmdFollow, pet.ActCommand), wire.EmptyGUID)
	if got := p.lastSent(t, wire.CMSGPetAction); !bytes.Equal(got, want) {
		t.Fatalf("follow wrote %x, want %x", got, want)
	}
	if btn := actionButton(t, want); btn != 0x07000001 {
		t.Fatalf("the follow button is 0x%08X, want 0x07000001", btn)
	}

	// cast slot=3: the bar's own button at slot 3.
	slot := 3
	if _, err := p.s.servePetAction(context.Background(), servePetActionParams{Action: "cast", Slot: &slot}); err != nil {
		t.Fatalf("pet_action cast: %v", err)
	}
	wantCast := pet.BuildAction(petTestPet, pet.Button(172, pet.ActEnabled), wire.EmptyGUID)
	if got := p.lastSent(t, wire.CMSGPetAction); !bytes.Equal(got, wantCast) {
		t.Fatalf("cast wrote %x, want %x", got, wantCast)
	}

	// autocast_on slot=4: CMSG_PET_SPELL_AUTOCAST with the one flushed bit.
	slot4 := 4
	if _, err := p.s.servePetAction(context.Background(), servePetActionParams{Action: "autocast_on", Slot: &slot4}); err != nil {
		t.Fatalf("pet_action autocast_on: %v", err)
	}
	wantAuto := pet.BuildSpellAutocast(petTestPet, 2649, true)
	if got := p.lastSent(t, wire.CMSGPetSpellAutocast); !bytes.Equal(got, wantAuto) {
		t.Fatalf("autocast_on wrote %x, want %x", got, wantAuto)
	}

	// A react order updates the local copy, and the pet read shows it.
	if _, err := p.s.servePetAction(context.Background(), servePetActionParams{Action: "aggressive"}); err != nil {
		t.Fatalf("pet_action aggressive: %v", err)
	}
	if info := p.s.servePet(); info.React != "aggressive" {
		t.Fatalf("the copy after aggressive: %q", info.React)
	}
}

// TestServePetRefusals: every refusal that needs no packet is an error before anything is sent.
func TestServePetRefusals(t *testing.T) {
	p := newPetWorld(t)
	ctx := context.Background()

	// No pet.
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "follow"}); err == nil || !strings.Contains(err.Error(), "no pet") {
		t.Fatalf("no pet: %v", err)
	}
	if info := p.s.servePet(); info.Present || info.Bar {
		t.Fatalf("pet without a pet: %+v", info)
	}

	p.apply(fragmentUnitCreate(petTestPet, objects.Info{Entry: petUnitEntry, Health: 80, MaxHealth: 80, Level: 10}))
	p.apply(fragmentUnitCreate(itemSelf, objects.Info{Entry: 1, Health: 100, MaxHealth: 100, Level: 10, Summon: petTestPet}))
	p.deliver(wire.SMSGPetSpellsMessage, petSpellsWithSpells(petTestPet))

	// A mounted character: the core drops the orders, so nothing is sent.
	p.mountSelf(14338)
	before := p.sentCount(wire.CMSGPetAction)
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "follow"}); err == nil || !strings.Contains(err.Error(), "mounted") {
		t.Fatalf("mounted: %v", err)
	}
	if n := p.sentCount(wire.CMSGPetAction); n != before {
		t.Fatalf("a mounted order sent %d packet(s)", n-before)
	}
	if info := p.s.servePet(); info.Enabled || !info.Bar {
		t.Fatalf("pet while mounted: bar %v enabled %v", info.Bar, info.Enabled)
	}
	p.mountSelf(0)

	// A passive spell cannot be autocast.
	slot5 := 5
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "autocast_on", Slot: &slot5}); err == nil || !strings.Contains(err.Error(), "passive") {
		t.Fatalf("autocast of a passive spell: %v", err)
	}
	if n := p.sentCount(wire.CMSGPetSpellAutocast); n != 0 {
		t.Fatalf("the passive autocast sent %d packet(s)", n)
	}
	// A passive spell is not a castable button either.
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "cast", Slot: &slot5}); err == nil || !strings.Contains(err.Error(), "castable") {
		t.Fatalf("cast of a passive spell: %v", err)
	}
	// An unknown action.
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "sit"}); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("unknown action: %v", err)
	}
	// Attack with no selection and no target.
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "attack"}); err == nil || !strings.Contains(err.Error(), "no target") {
		t.Fatalf("attack without a target: %v", err)
	}
	// An order the bar cannot name.
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "cast", SpellID: 999}); err == nil || !strings.Contains(err.Error(), "no such spell") {
		t.Fatalf("cast of an unknown spell: %v", err)
	}
}

// TestServePetEvents: the pet's summon, death, revival and dismissal, and the two refusals, reach the event log
// with the kinds S5 names.
func TestServePetEvents(t *testing.T) {
	p := newPetWorld(t)
	p.apply(
		fragmentUnitCreate(petTestPet, objects.Info{Entry: petUnitEntry, Health: 80, MaxHealth: 80, Level: 10, SummonedBy: itemSelf}),
		fragmentUnitCreate(petTestPet2, objects.Info{Entry: petUnitEntry + 1, Health: 90, MaxHealth: 90, Level: 10, SummonedBy: itemSelf}),
	)
	p.summonSelf(petTestPet)
	p.update(buildHealthValues(petTestPet, 0))
	p.update(buildHealthValues(petTestPet, 80))
	// A Summon that names another pet is the old one dismissed and the new one summoned.
	p.summonSelf(petTestPet2)
	p.summonSelf(wire.EmptyGUID)

	castID := wire.GUID{Low: 0x5001, High: 0x100000000000000}
	failed := wire.NewWriter()
	wire.WritePackedGUID(failed, castID)
	failed.WriteI32(26125)
	failed.WriteI32(26)
	failed.WriteI32(0)
	failed.WriteI32(0)
	p.deliver(wire.SMSGPetCastFailed, failed.Bytes())

	feedback := wire.NewWriter()
	feedback.WriteI32(3) // invalid target
	feedback.WriteI32(172)
	p.deliver(wire.SMSGPetActionFeedback, feedback.Bytes())

	rows, _, _, _ := p.s.EventLog().Since(0, 100)
	kinds := make([]string, 0, len(rows))
	for _, row := range rows {
		kinds = append(kinds, row.Kind)
	}
	want := []string{"pet_summoned", "pet_died", "pet_revived", "pet_dismissed", "pet_summoned", "pet_dismissed", "pet_error", "pet_error"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("the pet event kinds are %v, want %v", kinds, want)
	}
	if rows[0].SourceGUID != GUID(petTestPet).String() || rows[0].TargetGUID != GUID(itemSelf).String() {
		t.Fatalf("pet_summoned source/target: %q/%q", rows[0].SourceGUID, rows[0].TargetGUID)
	}
	if entry, ok := rows[0].Data["entry"].(int32); !ok || entry != petUnitEntry {
		t.Fatalf("pet_summoned entry: %v", rows[0].Data["entry"])
	}
	if rows[3].SourceGUID != GUID(petTestPet).String() {
		t.Fatalf("the replaced pet's dismissal names %q", rows[3].SourceGUID)
	}
	if dead, ok := rows[3].Data["was_dead"].(bool); !ok || dead {
		t.Fatalf("pet_dismissed was_dead: %v", rows[3].Data["was_dead"])
	}
	if rows[4].SourceGUID != GUID(petTestPet2).String() {
		t.Fatalf("the replacing pet's summon names %q", rows[4].SourceGUID)
	}
	if rows[5].SourceGUID != GUID(petTestPet2).String() {
		t.Fatalf("the cleared pet's dismissal names %q", rows[5].SourceGUID)
	}
	if reason, _ := rows[6].Data["reason"].(string); reason != "caster aurastate" || rows[6].SpellID != 26125 {
		t.Fatalf("pet_error (cast failed): %v %d", rows[6].Data, rows[6].SpellID)
	}
	if name, _ := rows[7].Data["feedback"].(string); name != "invalid_target" {
		t.Fatalf("pet_error (feedback): %v", rows[7].Data)
	}
}

// TestServePetClearedBarAndParseErrors: the remove-body clears the bar, and a malformed pet body is counted and
// merges nothing.
func TestServePetClearedBarAndParseErrors(t *testing.T) {
	p := newPetWorld(t)
	p.apply(fragmentUnitCreate(petTestPet, objects.Info{Entry: petUnitEntry, Health: 80, MaxHealth: 80, Level: 10}))
	p.apply(fragmentUnitCreate(itemSelf, objects.Info{Entry: 1, Health: 100, MaxHealth: 100, Level: 10, Summon: petTestPet}))
	p.deliver(wire.SMSGPetSpellsMessage, petSpellsWithSpells(petTestPet))
	if info := p.s.servePet(); !info.Bar {
		t.Fatalf("the bar did not arrive: %+v", info)
	}

	// The remove body: an empty pet GUID, zero fields, 18 zero buttons and three zero counts.
	var zero [pet.BarSlots]uint32
	var zeroExtra [pet.BarExtra]uint32
	p.deliver(wire.SMSGPetSpellsMessage, petSpellsBody(wire.EmptyGUID, 0, 0, 0, zero, zeroExtra, nil))
	if info := p.s.servePet(); info.Bar || info.Enabled || len(info.Spells) != 0 {
		t.Fatalf("the removed bar: %+v", info)
	}

	// A body with ten buttons instead of eighteen shifts the counts into the button block: counted, and the bar
	// stays unknown.
	before := p.s.report.ParseErrors
	p.deliver(wire.SMSGPetSpellsMessage, petTenButtonBody(petTestPet))
	if p.s.report.ParseErrors != before+1 {
		t.Fatalf("a malformed pet body counted %d parse error(s), want 1", p.s.report.ParseErrors-before)
	}
	if info := p.s.servePet(); info.Bar {
		t.Fatalf("a malformed pet body set the bar: %+v", info)
	}
}

// petTenButtonBody is the Turtle-era bar shape: ten buttons and the three counts, with no Classic tail. The 1.60
// parser reads those counts as the tail's first words and runs out of bytes.
func petTenButtonBody(petGUID wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, petGUID)
	b.WriteU16(1)
	b.WriteU16(0)
	b.WriteU32(0)
	b.WriteU8(uint8(pet.CmdFollow))
	b.WriteU8(0)
	b.WriteU8(pet.ReactAssist)
	for i := range pet.BarSlots {
		b.WriteU32(pet.Button(uint32(172+i), pet.ActDisabled))
	}
	b.WriteU32(0) // actions
	b.WriteU32(0) // cooldowns
	b.WriteU32(0) // history
	return b.Bytes()
}

// TestServePetModeOffBarAndRPC covers the remaining P2 paths: RPC table registration, CreatedBy/CharmedBy and pet
// unit fields, SMSG_PET_MODE updates, off-bar Actions (slot -1), cached spell-name cast, attack target fallback,
// dead/no-bar refusals, and a pet announced already dead.
func TestServePetModeOffBarAndRPC(t *testing.T) {
	ported, notPorted := ServeRPCMethods()
	for _, name := range []string{"pet", "pet_action"} {
		if !slices.Contains(ported, name) {
			t.Fatalf("%s is not in ServeRPCMethods ported", name)
		}
		if slices.Contains(notPorted, name) {
			t.Fatalf("%s is still in notPortedMethods", name)
		}
	}

	p := newPetWorld(t)
	ctx := context.Background()
	p.apply(
		fragmentUnitCreate(petTestHunter, objects.Info{Entry: 1, Health: 100, MaxHealth: 100, Level: 10, Summon: petTestPet}),
		fragmentUnitCreate(petTestPet, objects.Info{
			Entry: petUnitEntry, Health: 80, MaxHealth: 80, Level: 10,
			CreatedBy: petTestCreator, PetNumber: 7, CreatedBySpell: 883, CreatureFamily: 1,
		}),
		fragmentUnitCreate(petTestPet2, objects.Info{
			Entry: petUnitEntry + 1, Health: 0, MaxHealth: 90, Level: 10, CharmedBy: petTestHunter,
		}),
	)

	// CreatedBy and CharmedBy owner_kind + pet fields on ServeUnit.
	u1, err := p.s.serveOneUnit(serveUnitParams{GUID: GUID(petTestPet)})
	if err != nil || u1.Owner != GUID(petTestCreator) || u1.OwnerKind != "created_by" ||
		u1.PetNumber != 7 || u1.CreatedBySpell != 883 || u1.CreatureFamily != 1 {
		t.Fatalf("unit created_by: %+v, err=%v", u1, err)
	}
	u2, err := p.s.serveOneUnit(serveUnitParams{GUID: GUID(petTestPet2)})
	if err != nil || u2.Owner != GUID(petTestHunter) || u2.OwnerKind != "charmed_by" {
		t.Fatalf("unit charmed_by: %+v, err=%v", u2, err)
	}
	hunterUnit, err := p.s.serveOneUnit(serveUnitParams{GUID: GUID(petTestHunter)})
	if err != nil || hunterUnit.Pet != GUID(petTestPet) {
		t.Fatalf("hunter pet field: %+v, err=%v", hunterUnit, err)
	}

	// Summon a pet that is already dead -> pet_summoned (dead:true) + pet_died, and dead pet refusal.
	p.apply(fragmentUnitCreate(itemSelf, objects.Info{Entry: 1, Health: 100, MaxHealth: 100, Level: 10}))
	p.summonSelf(petTestPet2)
	rows, _, _, _ := p.s.EventLog().Since(0, 20)
	if len(rows) != 2 || rows[0].Kind != "pet_summoned" || rows[1].Kind != "pet_died" {
		t.Fatalf("dead summon events: %+v", rows)
	}
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "follow"}); err == nil || !strings.Contains(err.Error(), "dead") {
		t.Fatalf("dead pet refusal: %v", err)
	}

	// Alive pet without a bar -> bar not arrived refusal on cast/autocast_on, then deliver bar with off-bar action.
	p.summonSelf(petTestPet)
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "cast", SpellID: 172}); err == nil || !strings.Contains(err.Error(), "bar has not arrived") {
		t.Fatalf("no-bar refusal: %v", err)
	}

	var bar [pet.BarSlots]uint32
	bar[3] = pet.Button(172, pet.ActEnabled)
	var extra [pet.BarExtra]uint32
	p.deliver(wire.SMSGPetSpellsMessage, petSpellsBody(petTestPet, uint8(pet.CmdFollow), 0, pet.ReactDefensive, bar, extra, []uint32{
		pet.Button(172, pet.ActEnabled),  // duplicated on bar -> deduplicated
		pet.Button(1742, pet.ActDisabled), // off-bar spell -> slot -1
	}))
	// Cached SpellName fills Spell.Name without fetching.
	p.s.dbCache().Put(tables.TableSpellName, 172, true, []byte("Growl\x00"))
	info := p.s.servePet()
	if len(info.Spells) != 2 || info.Spells[0].Name != "Growl" || info.Spells[1].Slot != -1 || info.Spells[1].SpellID != 1742 {
		t.Fatalf("spells with cached name and off-bar action: %+v", info.Spells)
	}

	// Off-bar spell cannot be cast, but autocast_on/off work on it.
	offSlot := -1
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "cast", Slot: &offSlot}); err == nil || !strings.Contains(err.Error(), "castable") {
		t.Fatalf("off-bar cast refusal: %v", err)
	}
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "autocast_off", SpellID: 1742}); err != nil {
		t.Fatalf("autocast_off off-bar: %v", err)
	}
	wantOff := pet.BuildSpellAutocast(petTestPet, 1742, false)
	if got := p.lastSent(t, wire.CMSGPetSpellAutocast); !bytes.Equal(got, wantOff) {
		t.Fatalf("autocast_off wrote %x, want %x", got, wantOff)
	}

	// Cast by cached name and attack using current selection.
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "cast", Name: "growl"}); err != nil {
		t.Fatalf("cast by name: %v", err)
	}
	p.s.serveRT = &serveRuntime{target: petTestHunter}
	if _, err := p.s.servePetAction(ctx, servePetActionParams{Action: "attack"}); err != nil {
		t.Fatalf("attack selection: %v", err)
	}
	wantAtk := pet.BuildAction(petTestPet, pet.Button(pet.CmdAttack, pet.ActCommand), petTestHunter)
	if got := p.lastSent(t, wire.CMSGPetAction); !bytes.Equal(got, wantAtk) {
		t.Fatalf("attack wrote %x, want %x", got, wantAtk)
	}

	// SMSG_PET_MODE updates the matching pet's bar and ignores a different GUID.
	mode := wire.NewWriter()
	wire.WritePackedGUID(mode, petTestPet)
	mode.WriteU8(uint8(pet.CmdStay))
	mode.WriteU8(0)
	mode.WriteU8(pet.ReactPassive)
	p.deliver(wire.SMSGPetMode, mode.Bytes())
	otherMode := wire.NewWriter()
	wire.WritePackedGUID(otherMode, petTestPet2)
	otherMode.WriteU8(uint8(pet.CmdFollow))
	otherMode.WriteU8(0)
	otherMode.WriteU8(pet.ReactAggressive)
	p.deliver(wire.SMSGPetMode, otherMode.Bytes())
	if got := p.s.servePet(); got.Command != "stay" || got.React != "passive" {
		t.Fatalf("after SMSG_PET_MODE: command=%q react=%q", got.Command, got.React)
	}
}
