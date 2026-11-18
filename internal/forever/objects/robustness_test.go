package objects

// The robustness rules of the fix round: the model is bounded, movement packets never create objects, a block the
// parser cannot decode is skipped by its declared size (only broken outer framing is fatal), a decoded field is
// trusted only when it lies before the first skipped region, and a create spline interpolates from the position it
// was sent with.

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// buildUpdate frames n blocks into one SMSG_UPDATE_OBJECT. Each block already carries its update type and GUID.
func buildUpdate(blocks ...[]byte) []byte {
	var data []byte
	for _, b := range blocks {
		data = append(data, b...)
	}
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(uint32(len(blocks)))
	out.WriteBit(true)  // unk
	out.WriteBit(false) // no lists
	out.FlushBits()
	out.WriteU32(uint32(len(data)))
	out.WriteBytes(data)
	return out.Bytes()
}

// buildValuesBlock frames one values body for guid.
func buildValuesBlock(guid wire.GUID, body []byte) []byte {
	b := wire.NewWriter()
	b.WriteU8(UpdateValues)
	wire.WritePackedGUID(b, guid)
	b.WriteU32(uint32(len(body)))
	b.WriteBytes(body)
	return b.Bytes()
}

// buildCreateBlock frames one create block.
func buildCreateBlock(guid wire.GUID, objectType uint8, move, values []byte) []byte {
	b := wire.NewWriter()
	b.WriteU8(UpdateCreateObject)
	wire.WritePackedGUID(b, guid)
	b.WriteU8(objectType)
	b.WriteBytes(move)
	b.WriteU32(uint32(len(values)))
	b.WriteBytes(values)
	return b.Bytes()
}

func minimalObjectValues() []byte {
	v := wire.NewWriter()
	v.WriteU8(0)
	v.WriteU8(fragCGObject)
	v.WriteU8(0xFF)
	v.WriteU8(1)
	v.WriteI32(1)
	v.WriteU32(0)
	v.WriteFloat32(1)
	return v.Bytes()
}

// TestWorldBoundsStreamedGUIDs streams more unique GUIDs than the object bound, across many packets, and proves the
// model stays bounded and keeps the active mover. It also streams movement packets for unknown GUIDs: those must be
// counted, never create an object, and the name caches must stay bounded too.
func TestWorldBoundsStreamedGUIDs(t *testing.T) {
	w := NewWorld(0)
	now := time.Unix(1700000000, 0)
	active := wire.GUID{Low: 1, High: 0x0100000000000000}
	w.SetActiveMover(active)
	if _, err := ParseUpdateObject(buildUpdate(buildCreateBlock(active, TypeObject, buildMovementBlock(false, nil), minimalObjectValues()))); err != nil {
		t.Fatal(err)
	}
	w.Apply(mustUpdate(t, buildUpdate(buildCreateBlock(active, TypeObject, buildMovementBlock(false, nil), minimalObjectValues()))), now)

	total := maxObjects + 5000
	for i := 1; i <= total; i++ {
		guid := wire.GUID{Low: uint64(i + 1), High: 0x0100000000000000}
		payload := buildUpdate(buildCreateBlock(guid, TypeObject, buildMovementBlock(false, nil), minimalObjectValues()))
		w.Apply(mustUpdate(t, payload), now.Add(time.Duration(i)*time.Millisecond))
	}
	w.mu.RLock()
	held := len(w.objects)
	w.mu.RUnlock()
	if held > maxObjects {
		t.Fatalf("the model holds %d objects, the bound is %d", held, maxObjects)
	}
	if _, ok := w.Object(active); !ok {
		t.Fatal("the active mover was evicted")
	}

	// Movement for GUIDs the model never saw: counted, not created.
	before := w.Counts()
	for i := range 1000 {
		guid := wire.GUID{Low: uint64(1<<40) + uint64(i), High: 0x0200000000000000}
		w.ApplyMonsterMove(&MonsterMove{MoverGUID: guid, StartX: 1, StartY: 2, StartZ: 3}, now)
		w.ApplyMoveUpdate(MovementInfo{GUID: guid}, nil, now)
		w.ApplyTeleport(MoveTeleport{MoverGUID: guid}, now)
	}
	after := w.Counts()
	if after.UnknownMoves != before.UnknownMoves+3000 {
		t.Fatalf("unknown movement counted %d, want 3000", after.UnknownMoves-before.UnknownMoves)
	}
	if after.Objects != before.Objects {
		t.Fatalf("movement packets changed the object count from %d to %d", before.Objects, after.Objects)
	}

	// Both name caches are capped.
	for i := range maxNames + 100 {
		w.SetEntryName(int32(i), "n")
		w.SetName(wire.GUID{Low: uint64(i), High: 0x0200000000000000}, "p")
	}
	w.mu.RLock()
	entries, players := len(w.entries), len(w.players)
	w.mu.RUnlock()
	if entries > maxNames || players > maxNames {
		t.Fatalf("name caches grew to %d entries and %d players, the bound is %d", entries, players, maxNames)
	}
}

func mustUpdate(t *testing.T, payload []byte) *Update {
	t.Helper()
	u, err := ParseUpdateObject(payload)
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	return u
}

// TestUnparsableBlockIsSkipped: a values body the parser cannot decode is still framed by its declared size. The
// packet parses, the block carries the reason, and the block that follows it decodes normally.
func TestUnparsableBlockIsSkipped(t *testing.T) {
	bad := wire.NewWriter()
	bad.WriteU8(0x00) // flags
	bad.WriteU8(0x01) // fragment ids changed
	bad.WriteU8(0x01) // serialization type
	bad.WriteU8(0x20) // an id no fragment list accepts, never terminated
	bad.WriteU8(0x21)
	bad.WriteU8(0x22)
	for range 16 {
		bad.WriteU8(0x23)
	}
	badY := wire.GUID{Low: 7, High: 0x0100000000000000}
	goodY := wire.GUID{Low: 8, High: 0x0100000000000000}
	payload := buildUpdate(
		buildValuesBlock(badY, bad.Bytes()),
		buildCreateBlock(goodY, TypeObject, buildMovementBlock(false, nil), minimalObjectValues()),
	)
	u, err := ParseUpdateObject(payload)
	if err != nil {
		t.Fatalf("the outer packet must survive an unparsable block: %v", err)
	}
	if len(u.Blocks) != 2 {
		t.Fatalf("%d blocks", len(u.Blocks))
	}
	if u.Blocks[0].Unparsed == ReasonNone {
		t.Fatal("the unparsable values block was not reported")
	}
	if u.Blocks[1].Unparsed != ReasonNone || u.Blocks[1].Info.Entry != 1 {
		t.Fatalf("the block after the skipped one: %+v", u.Blocks[1])
	}

	w := NewWorld(0)
	w.Apply(u, time.Unix(1700000000, 0))
	c := w.Counts()
	if c.Unparsed == nil || len(c.Unparsed) != 1 {
		t.Fatalf("unparsed summary %v", c.Unparsed)
	}
	count := 0
	for _, n := range c.Unparsed {
		count += n
	}
	if count != 1 {
		t.Fatalf("%d unparsed blocks counted, want 1", count)
	}
	if o, ok := w.Object(badY); !ok || !o.Partial {
		t.Fatalf("the object of the unparsable block: %+v (present %v)", o, ok)
	}
}

// overLimitCreateValues writes a create body of objectType whose first declared count is n: a unit's state world
// effects or a game object's. n is server-controlled and the parser puts it in the error verbatim.
func overLimitCreateValues(objectType uint8, n uint32) []byte {
	v := wire.NewWriter()
	v.WriteU8(0) // field flags: none
	v.WriteU8(fragCGObject)
	v.WriteU8(0xFF)   // end of the id list
	v.WriteU8(1)      // the CGObject fragment exists
	v.WriteI32(1)     // entry
	v.WriteU32(0)     // dynamic flags
	v.WriteFloat32(1) // scale
	switch objectType {
	case TypeUnit, TypePlayer, TypeActivePlayer: // UnitData::WriteCreate up to the state world effects count
		v.WriteI32(1) // DisplayID
		v.WriteU32(0) // NpcFlags
		v.WriteU32(0) // NpcFlags2
		for range 3 {
			v.WriteU32(0) // StateSpellVisualID, StateAnimID, StateAnimKitID
		}
		v.WriteU32(n)
	case TypeGameObject: // GameObjectData::WriteCreate up to the same count
		v.WriteI32(1) // GODisplayID
		v.WriteU32(0) // SpellVisualID
		for range 3 {
			v.WriteU32(0) // StateSpellVisualID, SpawnTrackingStateAnimID, SpawnTrackingStateAnimKitID
		}
		v.WriteU32(n)
	}
	return v.Bytes()
}

// TestUnparsedSummaryIsBounded: the walk summary counts skipped blocks by (object type, reason code), never by the
// parser's text. 100,000 blocks whose declared counts are all distinct (so the text differs every time) must leave
// one key per (type bucket, reason) and the summary must hold its size after the first pass.
func TestUnparsedSummaryIsBounded(t *testing.T) {
	const blocks = 100000
	types := []uint8{TypeUnit, TypePlayer, TypeActivePlayer, TypeGameObject}
	w := NewWorld(0)
	now := time.Unix(1700000000, 0)
	stream := func(lo, hi int) (lastText string) {
		for i := lo; i < hi; i++ {
			objectType := types[i%len(types)]
			guid := wire.GUID{Low: uint64(i + 1), High: 0x0100000000000000}
			payload := buildUpdate(buildCreateBlock(guid, objectType,
				buildMovementBlock(false, nil), overLimitCreateValues(objectType, uint32(4097+i))))
			u, err := ParseUpdateObject(payload)
			if err != nil {
				t.Fatalf("block %d: %v", i, err)
			}
			lastText = u.Blocks[0].UnparsedDetail
			if u.Blocks[0].Unparsed != ReasonCountOverLimit {
				t.Fatalf("block %d: reason %s (%s)", i, u.Blocks[0].Unparsed, lastText)
			}
			w.Apply(u, now)
		}
		return lastText
	}

	const first = 1000
	firstText := stream(0, first)
	c := w.Counts()
	keys := len(c.Unparsed)
	if keys > maxUnparsedKeys {
		t.Fatalf("%d summary keys, the finite set holds %d", keys, maxUnparsedKeys)
	}
	// One key per type bucket: the four types cycled above, all count over limit, 250 blocks each in the first pass.
	want := map[string]string{
		"unit: count over limit":          "4097",
		"player: count over limit":        "4098",
		"active player: count over limit": "4099",
		"game object: count over limit":   "4100",
	}
	if keys != len(want) {
		t.Fatalf("%d summary keys, want one per type: %v", keys, c.Unparsed)
	}
	detailBytes := 0
	for key, firstCount := range want {
		if n := c.Unparsed[key]; n != first/len(want) {
			t.Fatalf("%q counted %d blocks, want %d: %v", key, n, first/len(want), c.Unparsed)
		}
		text, ok := c.UnparsedDetail[key]
		if !ok || !strings.Contains(text, firstCount) {
			t.Fatalf("%q keeps %q (present %v), want the first block's text with %s", key, text, ok, firstCount)
		}
		detailBytes += len(text)
	}

	lastText := stream(first, blocks)
	if lastText == firstText {
		t.Fatal("the stream did not carry distinct parser texts")
	}
	after := w.Counts()
	if len(after.Unparsed) != keys || len(after.UnparsedDetail) != keys {
		t.Fatalf("the summary grew with the count: %d keys and %d texts, was %d",
			len(after.Unparsed), len(after.UnparsedDetail), keys)
	}
	total, bytesAfter := 0, 0
	for _, n := range after.Unparsed {
		total += n
	}
	for _, text := range after.UnparsedDetail {
		bytesAfter += len(text)
	}
	if total != blocks {
		t.Fatalf("%d blocks counted, want %d", total, blocks)
	}
	if bytesAfter != detailBytes {
		t.Fatalf("the summary text grew with the count: %d bytes, was %d", bytesAfter, detailBytes)
	}
}

// TestUnmodelledMovementBlocksAreWalked: inertia, advanced flying, a drive status, a movement force and a game
// object's assist data are skipped by their exact size rather than refused, and the values behind them still decode.
func TestUnmodelledMovementBlocksAreWalked(t *testing.T) {
	// A create with every optional movement block set. buildMovementBlock writes the plain form; the optional
	// blocks below follow the same fixed header, so the block is rebuilt here instead of patched.
	move := wire.NewWriter()
	for _, set := range []bool{true, false, false, false, false, true} {
		move.WriteBit(set)
	}
	for range 15 {
		move.WriteBit(false)
	}
	move.FlushBits()
	move.WriteU32(0)    // pause count
	writeZeroGUID(move) // MoverGUID
	move.WriteU64(1)    // flags
	move.WriteU32(0)    // time
	for range 4 {
		move.WriteFloat32(0) // XYZO
	}
	move.WriteFloat32(0) // pitch
	move.WriteFloat32(0) // step-up
	move.WriteU32(1)     // removed forces count
	move.WriteU32(0)     // move index
	move.WriteFloat32(1) // gravity
	// standingOn, transport, fall, spline, heightChangeFailed, remoteTimeValid, inertia, advFlying, drive
	for _, set := range []bool{false, false, false, false, false, false, true, true, true} {
		move.WriteBit(set)
	}
	move.FlushBits()
	writeZeroGUID(move) // the removed force's GUID
	move.WriteU32(7)    // inertia id
	for range 3 {
		move.WriteFloat32(0) // inertia force XYZ
	}
	move.WriteU32(9)     // inertia lifetime
	move.WriteFloat32(1) // advFlying forwardVelocity
	move.WriteFloat32(2) // advFlying upVelocity
	move.WriteFloat32(3) // drive speed
	move.WriteFloat32(4) // drive angle
	move.WriteBit(true)  // accelerating
	move.WriteBit(false) // drifting
	move.FlushBits()
	for range 9 {
		move.WriteFloat32(7) // speeds
	}
	move.WriteU32(1)     // one movement force
	move.WriteFloat32(1) // mod magnitude
	for range 17 {
		move.WriteFloat32(0) // advanced-flying speeds
	}
	writeZeroGUID(move) // force ID
	for range 6 {
		move.WriteFloat32(0) // origin XYZ + direction XYZ
	}
	move.WriteU32(0) // TransportID
	move.WriteFloat32(0)
	move.WriteI32(0) // MovementForceID
	move.WriteI32(0) // DurationMs
	move.WriteU32(0) // EndTimestamp
	move.WriteU32(0) // Flags
	move.WriteBits(0, 2)
	move.FlushBits()
	move.WriteBit(false) // HasSpline
	move.FlushBits()

	guid := wire.GUID{Low: 11, High: 0x0100000000000000}
	u := mustUpdate(t, buildUpdate(buildCreateBlock(guid, TypeUnit, move.Bytes(), buildUnitCreate(unitCreateSpec{}))))
	b := u.Blocks[0]
	if b.Unparsed != ReasonNone {
		t.Fatalf("the optional movement blocks were refused (%s): %s", b.Unparsed, b.UnparsedDetail)
	}
	if !b.ValuesComplete || b.Info.Power[0] != 800 {
		t.Fatalf("the values behind the optional blocks: complete %v power %v", b.ValuesComplete, b.Info.Power)
	}

	// A game object create with assist data (GameObjectAssistActionData::WriteCreate, UpdateFields.cpp:7493-7503).
	assist := wire.NewWriter()
	assist.WriteBits(3, 6)  // player name length
	assist.WriteBits(6, 11) // monster name length + 1
	assist.WriteU32(0)      // realm
	assist.WriteU8(0)       // sex
	assist.WriteU64(0)      // time
	assist.WriteI32(0)      // delve tier
	assist.WriteString("AAA")
	assist.WriteString("BBBBB")
	assist.WriteU8(0) // NUL
	assist.FlushBits()

	goValues := wire.NewWriter()
	goValues.WriteU8(0)
	goValues.WriteU8(fragCGObject)
	goValues.WriteU8(0xFF)
	goValues.WriteU8(1)
	goValues.WriteI32(11) // ObjectData entry
	goValues.WriteU32(0)
	goValues.WriteFloat32(1)
	// GameObjectData::WriteCreate (:7515-7569).
	goValues.WriteI32(100) // DisplayID
	goValues.WriteU32(0)   // SpellVisualID
	goValues.WriteU32(0)   // StateSpellVisualID
	goValues.WriteU32(0)   // SpawnTrackingStateAnimID
	goValues.WriteU32(0)   // SpawnTrackingStateAnimKitID
	goValues.WriteU32(0)   // StateWorldEffectIDs count
	goValues.WriteU32(0)   // StateWorldEffectsQuestObjectiveID
	writeZeroGUID(goValues)
	writeZeroGUID(goValues)
	goValues.WriteU32(0) // Flags
	goValues.WriteU32(0) // FlagsB
	for range 4 {
		goValues.WriteFloat32(0) // ParentRotation
	}
	goValues.WriteI32(0) // FactionTemplate
	goValues.WriteI32(0) // Level (Classic int32)
	goValues.WriteU8(0)  // State
	goValues.WriteU8(0)  // TypeID
	goValues.WriteU8(0)  // PercentHealth
	goValues.WriteU32(0) // ArtKit
	goValues.WriteU32(0) // EnableDoodadSets count
	goValues.WriteU32(0) // CustomParam
	goValues.WriteI32(0) // Level
	goValues.WriteU32(0) // AnimGroupInstance
	goValues.WriteU32(0) // UiWidgetItemID
	goValues.WriteU32(0) // UiWidgetItemQuality
	goValues.WriteU32(0) // UiWidgetItemCount
	goValues.WriteU32(0) // WorldEffects count
	goValues.WriteBit(true)
	goValues.FlushBits()
	goValues.WriteBytes(assist.Bytes())

	goGUID := wire.GUID{Low: 12, High: 0x0100000000000000}
	gu := mustUpdate(t, buildUpdate(buildCreateBlock(goGUID, TypeGameObject, buildMovementBlock(false, nil), goValues.Bytes())))
	if gu.Blocks[0].Unparsed != ReasonNone || !gu.Blocks[0].ValuesComplete || gu.Blocks[0].Info.Entry != 11 {
		t.Fatalf("the game object with assist data: %+v", gu.Blocks[0])
	}
}

// TestOuterStructureIsFatal: only a count or size that does not fit the packet is fatal.
func TestOuterStructureIsFatal(t *testing.T) {
	guid := wire.GUID{Low: 13, High: 0x0100000000000000}
	payload := buildUpdate(buildCreateBlock(guid, TypeObject, buildMovementBlock(false, nil), minimalObjectValues()))
	if _, err := ParseUpdateObject(payload[:len(payload)-1]); err == nil {
		t.Fatal("a block that overruns the packet was accepted")
	}
	bad := append([]byte(nil), payload...)
	// The block count sits at offset 2; make it larger than the data.
	bad[2] = 5
	if _, err := ParseUpdateObject(bad); err == nil {
		t.Fatal("a block count larger than the data was accepted")
	}
}

// TestPartialObjectMergesOnlyTheDecodedHead: a values update whose body cannot be decoded leaves the stored fields
// alone and marks the object partial, while a player create's unit head (decoded before the PlayerData tail) is
// merged and its tail is not.
func TestPartialObjectMergesOnlyTheDecodedHead(t *testing.T) {
	w := NewWorld(0)
	now := time.Unix(1700000000, 0)
	fx := loadFixtures(t)
	w.Apply(mustUpdate(t, fixturePayload(t, fx["create_player"])), now)
	b := mustUpdate(t, fixturePayload(t, fx["create_player"])).Blocks[0]
	o, ok := w.Object(b.GUID)
	if !ok || !o.Partial {
		t.Fatalf("the player create: %+v (present %v)", o, ok)
	}
	if o.Info.Health != 97 || o.Info.MaxHealth != 97 {
		t.Fatalf("the unit head before the player tail was not merged: %+v", o.Info)
	}

	// An unparsable values update for the same object must not touch its fields.
	bad := wire.NewWriter()
	bad.WriteU8(0)
	bad.WriteU8(0)
	bad.WriteU8(0x03) // CGObject exists and changed
	bad.WriteU32(1 << TypeUnit)
	bad.WriteBits(0x80, 8) // block 7 present...
	bad.WriteBits(0xFFFFFFFF, 32)
	bad.WriteBit(false)
	bad.FlushBits() // ...but nothing under it: the parser cannot decode this
	u := mustUpdate(t, buildUpdate(buildValuesBlock(b.GUID, bad.Bytes())))
	if u.Blocks[0].Unparsed == ReasonNone {
		t.Fatal("the malformed values update parsed")
	}
	w.Apply(u, now)
	after, _ := w.Object(b.GUID)
	if after.Info.Health != 97 || after.Info.MaxHealth != 97 {
		t.Fatalf("an undecoded block changed the stored fields: %+v", after.Info)
	}
	if !after.Partial {
		t.Fatal("the object was not marked partial")
	}
	if c := w.Counts(); c.Partial == 0 || len(c.Unparsed) == 0 {
		t.Fatalf("the report does not count the partial object or the skipped block: %+v", c)
	}
}

// TestCreateSplineStartsWhereThePacketSays: the create block's position is the spline's position at its elapsed
// time, so interpolation runs from that point for the remaining duration (not from elapsed 0, which counts the
// progress twice).
func TestCreateSplineStartsWhereThePacketSays(t *testing.T) {
	w := NewWorld(0)
	now := time.Unix(1700000000, 0)
	guid := wire.GUID{Low: 21, High: 0x0100000000000000}
	u := &Update{BlockCount: 1, Blocks: []WorldFragment{{
		GUID: guid, ObjectType: TypeUnit,
		Movement: &MoveBlock{
			ThisIsYou: false, MovementUpdate: true,
			MoverGUID: guid, X: 10, Y: 0, Z: 0,
			Spline: &CreateSpline{DestX: 20, DestY: 0, DestZ: 0, Duration: 1000, Elapsed: 400},
		},
	}}}
	w.Apply(u, now)
	o, ok := w.Object(guid)
	if !ok {
		t.Fatal("the create did not make the object")
	}
	if x, _, _ := o.Position(now); !near(x, 10) {
		t.Fatalf("the position at the packet's own time is %.2f, want the packet's 10 (progress counted once)", x)
	}
	if x, _, _ := o.Position(now.Add(600 * time.Millisecond)); !near(x, 20) {
		t.Fatalf("the position at the end of the remaining 600 ms is %.2f, want 20", x)
	}
}
