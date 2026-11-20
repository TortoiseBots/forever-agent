package objects

// SMSG_UPDATE_OBJECT, its per-block structure and the movement block of a create.
//
// Outer packet: UpdateData::BuildPacket (UpdateData.cpp:61-79) — uint16 map, uint32 block count, the "unk" bit and
// the out-of-range/destroy bit, the counts and the packed GUIDs of those lists, then uint32 size and the blocks.
// Every block declares its own size (BaseEntity.cpp:126-128 for a create, :190-192 for a values update), which is
// what keeps a parser in sync: each block is parsed inside a reader bounded by that size and must consume it to the
// last byte, or be skipped whole.

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Update block types (UpdateData.h:32-38).
const (
	UpdateValues        uint8 = 0
	UpdateCreateObject  uint8 = 1
	UpdateCreateObject2 uint8 = 2
)

// maxBlocks bounds the block count of one packet: the largest capture packet carries 63 blocks.
const maxBlocks = 512

// ParseReason classifies why a block's values body could not be decoded. The walk summary counts skipped blocks by
// (object type, ParseReason) and never by the parser's error text: the text can carry server-controlled counts, so
// counting it would mint a summary key per block, while this set is closed.
type ParseReason uint8

const (
	ReasonNone ParseReason = iota
	// ReasonShortRead: the values body ended before a field its own structure declared.
	ReasonShortRead
	// ReasonCountOverLimit: a declared count (or an id list that never ends) is past the cap this round walks.
	ReasonCountOverLimit
	// ReasonUnknownFlag: a block's flag bits contradict each other or the layout they select.
	ReasonUnknownFlag
	// ReasonMaskTooLarge: the fragment set needs more content-mask bits than the one-byte mask carries.
	ReasonMaskTooLarge
	// ReasonSizeMismatch: a block's declared size does not fit its frame.
	ReasonSizeMismatch
	// ReasonUnsupportedFragment: an updateable fragment this round has no layout for.
	ReasonUnsupportedFragment
	// ReasonOther: anything the parser did not classify.
	ReasonOther
	parseReasonCount
)

// String is the summary's name for the reason.
func (r ParseReason) String() string {
	switch r {
	case ReasonShortRead:
		return "short read"
	case ReasonCountOverLimit:
		return "count over limit"
	case ReasonUnknownFlag:
		return "unknown flag combination"
	case ReasonMaskTooLarge:
		return "mask too large"
	case ReasonSizeMismatch:
		return "size mismatch"
	case ReasonUnsupportedFragment:
		return "unsupported fragment"
	case ReasonOther:
		return "other"
	}
	return "none"
}

// parseError tags an error with its ParseReason. The text stays the parser's own; only the reason is counted.
type parseError struct {
	reason ParseReason
	err    error
}

func (e *parseError) Error() string { return e.err.Error() }
func (e *parseError) Unwrap() error { return e.err }

// parseFail builds a tagged parse error.
func parseFail(reason ParseReason, format string, args ...any) error {
	return &parseError{reason: reason, err: fmt.Errorf(format, args...)}
}

// reasonOf classifies a values-body error: the tag when it has one, a short read when the buffer ran out, other
// otherwise.
func reasonOf(err error) ParseReason {
	var pe *parseError
	if errors.As(err, &pe) {
		return pe.reason
	}
	if errors.Is(err, wire.ErrShort) {
		return ReasonShortRead
	}
	return ReasonOther
}

// WorldFragment is one update block.
type WorldFragment struct {
	// Values reports whether this is a values update (true) or a create (false).
	Values bool
	// NewObject is the create type 2 flag (UPDATETYPE_CREATE_OBJECT2: the object was already known to the client).
	NewObject bool
	GUID      wire.GUID
	// ObjectType is the block's object type byte (TYPEID_*, BlockCreateSorted's objectType at BaseEntity.cpp:103-112).
	ObjectType uint8
	// Movement is set on a create block.
	Movement *MoveBlock
	// Info holds the decoded values.
	Info Info
	// ValuesComplete is false when a fragment of the block was walked to the block's declared end without being
	// decoded (an item's, a container's, a player's fields after the unit block, an unfamiliar fragment id). The
	// block is still in sync: it was consumed to its size.
	ValuesComplete bool
	// Unparsed is ReasonNone when the block's values body decoded (possibly only its head). Otherwise it is the
	// finite reason the walk summary counts, and UnparsedDetail is the parser's own text, which the model keeps for
	// the first block of each (type, reason) key only.
	Unparsed       ParseReason
	UnparsedDetail string
	// UndecodedFragment is the updateable fragment at which the walk stopped without decoding it, in the wire
	// numbering (FragmentName): 0 when it stopped nowhere else. UndecodedTail is set instead when every fragment the
	// block listed was walked and bytes remained: a player's, an item's or a container's fields after the CGObject
	// fragment, which this round does not decode.
	UndecodedFragment uint8
	UndecodedTail     bool
	// ValuesStart and ValuesLen locate the block's values body in the packet; UndecodedFrom is the packet offset
	// where decoding stopped (0 when the whole body was walked). The fixture generator zeroes what this round does
	// not decode, so a real identifier cannot hide in a committed fixture.
	ValuesStart   int
	ValuesLen     int
	UndecodedFrom int
}

// Update is one SMSG_UPDATE_OBJECT.
type Update struct {
	MapID      uint16
	Blocks     []WorldFragment
	Destroy    []wire.GUID
	OutOfRange []wire.GUID
	// BlockCount is the declared block count; it equals len(Blocks) in a well-formed packet.
	BlockCount uint32
	// GUIDs are every packed GUID the packet carried: the block GUIDs, the destroy and out-of-range lists and the
	// ones inside movement and values blocks (including fields this round does not keep).
	GUIDs []wire.GUID
}

// MoveBlock is the movement block of a create (BaseEntity::BuildMovementUpdate, BaseEntity.cpp:252-570): the 21
// create-object flag bits, the pause-time count, and the movement info, speeds and spline when MovementUpdate is
// set.
type MoveBlock struct {
	HasEntityPosition bool
	NoBirthAnim       bool
	EnablePortals     bool
	PlayHoverAnim     bool
	ThisIsYou         bool
	MovementUpdate    bool
	MovementTransport bool
	Stationary        bool
	CombatVictim      bool
	ServerTime        bool
	Vehicle           bool
	AnimKit           bool
	Rotation          bool
	GameObject        bool
	SmoothPhasing     bool
	SceneObject       bool
	ActivePlayer      bool
	Conversation      bool
	Room              bool
	Decor             bool
	MeshObject        bool

	// Set when MovementUpdate is true.
	MoverGUID            wire.GUID
	Flags                uint64
	Time                 uint32
	X, Y, Z, O           float32
	Pitch                float32
	StepUpStartElevation float32
	MoveIndex            uint32
	GravityModifier      float32
	Jump                 *JumpInfo
	Transport            *TransportInfo
	// Speeds in yards per second, MOVE_WALK .. MOVE_PITCH_RATE (BaseEntity.cpp:390-398).
	Walk, Run, RunBack, Swim, SwimBack, Flight, FlightBack, TurnRate, PitchRate float32
	// Spline is the create-object spline block when the unit is moving (WriteCreateObjectSplineDataBlock,
	// MovementPackets.cpp:398-530).
	Spline *CreateSpline
	// StationaryPosition is the Stationary block's XYZO (WorldObject::GetStationaryPosition).
	StationaryPosition [4]float32
	PauseTimes         []uint32
	// GUIDs are the packed GUIDs the block carried besides the mover's (standing-on, transport, victim, ...),
	// for the fixture audit.
	GUIDs []wire.GUID
}

// guid reads one packed GUID and records it for the fixture audit.
func (mv *MoveBlock) guid(b *wire.Buffer) wire.GUID {
	g := wire.ReadPackedGUID(b)
	mv.GUIDs = append(mv.GUIDs, g)
	return g
}

// CreateSpline is the spline a create block carries (WriteCreateObjectSplineDataBlock, MovementPackets.cpp:398-530).
type CreateSpline struct {
	ID       uint32
	DestX    float32
	DestY    float32
	DestZ    float32
	HasMove  bool
	Flags    uint32
	Face     uint8
	Elapsed  int32
	Duration uint32
	Points   [][3]float32
	// PackedDeltas are relative to the middle of start and destination (MovementPackets.cpp:640-670).
	PackedDeltas [][3]float32
}

// reader returns a reader over payload with the audit hook installed: every packed GUID the parse decodes, kept or
// dropped, passes through hook before the parser sees it (wire.Buffer.SetGUIDHook). hook is nil in production.
func reader(payload []byte, hook func(wire.GUID)) *wire.Buffer {
	b := wire.NewReader(payload)
	b.SetGUIDHook(hook)
	return b
}

// ParseUpdateObject parses one SMSG_UPDATE_OBJECT payload (without the opcode).
func ParseUpdateObject(payload []byte) (*Update, error) { return parseUpdateObject(payload, nil) }

// parseUpdateObject is ParseUpdateObject with an audit hook: every GUID in the packet, including the ones inside the
// values and movement blocks that the decoded fields drop, passes through hook.
func parseUpdateObject(payload []byte, hook func(wire.GUID)) (*Update, error) {
	b := reader(payload, hook)
	u := &Update{}
	u.MapID = b.ReadU16()
	u.BlockCount = b.ReadU32()
	b.ReadBit() // the constant "unk" bit UpdateData.cpp:66 writes true
	hasLists := b.ReadBit()
	if hasLists {
		destroys := int(b.ReadU16())
		total := int(b.ReadU32())
		if destroys < 0 || total < destroys || total > maxBlocks*4 {
			return nil, fmt.Errorf("objects: update names %d destroy and %d out-of-range objects (out of bounds)", destroys, total)
		}
		for range destroys {
			u.Destroy = append(u.Destroy, wire.ReadPackedGUID(b))
		}
		for range total - destroys {
			u.OutOfRange = append(u.OutOfRange, wire.ReadPackedGUID(b))
		}
	}
	dataLen := int(b.ReadU32())
	if dataLen < 0 || dataLen > b.Remaining() {
		return nil, fmt.Errorf("objects: update declares %d bytes of blocks, %d are left", dataLen, b.Remaining())
	}
	if u.BlockCount > maxBlocks {
		return nil, fmt.Errorf("objects: update declares %d blocks", u.BlockCount)
	}
	data := b.ReadBytes(dataLen)
	if err := b.Err(); err != nil {
		return nil, err
	}
	if left := b.Remaining(); left != 0 {
		return nil, fmt.Errorf("objects: update has %d bytes after its blocks", left)
	}
	dataStart := len(payload) - dataLen

	u.GUIDs = append(u.GUIDs, u.Destroy...)
	u.GUIDs = append(u.GUIDs, u.OutOfRange...)
	br := wire.NewReader(data)
	br.SetGUIDHook(hook)
	for n := 0; n < int(u.BlockCount); n++ {
		if br.Remaining() == 0 {
			return nil, fmt.Errorf("objects: update declares %d blocks but has data for %d", u.BlockCount, n)
		}
		frag, err := parseBlock(br, dataStart, hook)
		if err != nil {
			return nil, fmt.Errorf("objects: block %d: %w", n, err)
		}
		u.Blocks = append(u.Blocks, frag)
		u.GUIDs = append(u.GUIDs, frag.GUID)
		if frag.Movement != nil {
			u.GUIDs = append(u.GUIDs, frag.Movement.MoverGUID)
			u.GUIDs = append(u.GUIDs, frag.Movement.GUIDs...)
		}
		u.GUIDs = append(u.GUIDs, frag.Info.audit...)
	}
	if br.Remaining() != 0 {
		return nil, fmt.Errorf("objects: update has %d bytes after %d blocks", br.Remaining(), u.BlockCount)
	}
	return u, nil
}

// parseBlock reads one update block. The update type and the GUID precede both layouts (BaseEntity.cpp:117-122 and
// BaseEntity::PrepareValuesUpdateBuffer, :213-220).
func parseBlock(b *wire.Buffer, base int, hook func(wire.GUID)) (WorldFragment, error) {
	updateType := b.ReadU8()
	guid := wire.ReadPackedGUID(b)
	switch updateType {
	case UpdateValues:
		size := int(b.ReadU32())
		if size < 0 || size > b.Remaining() {
			return WorldFragment{}, parseFail(ReasonSizeMismatch, "values block declares %d bytes, %d are left", size, b.Remaining())
		}
		bodyStart := base + b.Offset()
		body := reader(b.ReadBytes(size), hook)
		info, stop, err := parseValuesUpdate(body)
		if err != nil {
			// The body is size-framed: a structure this round cannot decode is skipped whole, never fatal.
			return WorldFragment{
				Values: true, GUID: guid, ValuesComplete: false,
				Unparsed: reasonOf(err), UnparsedDetail: err.Error(),
				ValuesStart: bodyStart, ValuesLen: size, UndecodedFrom: bodyStart,
			}, nil
		}
		return WorldFragment{
			Values: true, GUID: guid, Info: info, ValuesComplete: stop.complete(),
			UndecodedFragment: stop.fragment, UndecodedTail: stop.tail,
			ValuesStart: bodyStart, ValuesLen: size, UndecodedFrom: undecoded(bodyStart, size, body, stop.complete()),
		}, nil
	case UpdateCreateObject, UpdateCreateObject2:
		objType := b.ReadU8()
		mv, err := parseMovementBlock(b)
		if err != nil {
			return WorldFragment{}, err
		}
		size := int(b.ReadU32())
		if size < 0 || size > b.Remaining() {
			return WorldFragment{}, parseFail(ReasonSizeMismatch, "create block declares %d bytes of values, %d are left", size, b.Remaining())
		}
		bodyStart := base + b.Offset()
		body := reader(b.ReadBytes(size), hook)
		info, stop, err := parseValuesCreate(body, objType, mv.ThisIsYou)
		if err != nil {
			return WorldFragment{
				NewObject: updateType == UpdateCreateObject2, GUID: guid, ObjectType: objType,
				Movement: mv, ValuesComplete: false,
				Unparsed: reasonOf(err), UnparsedDetail: err.Error(),
				ValuesStart: bodyStart, ValuesLen: size, UndecodedFrom: bodyStart,
			}, nil
		}
		info.Type = objType
		return WorldFragment{
			NewObject:         updateType == UpdateCreateObject2,
			GUID:              guid,
			ObjectType:        objType,
			Movement:          mv,
			Info:              info,
			ValuesComplete:    stop.complete(),
			UndecodedFragment: stop.fragment,
			UndecodedTail:     stop.tail,
			ValuesStart:       bodyStart,
			ValuesLen:         size,
			UndecodedFrom:     undecoded(bodyStart, size, body, stop.complete()),
		}, nil
	}
	return WorldFragment{}, fmt.Errorf("unknown update type %d", updateType)
}

// undecoded is the payload offset where a block's decoding stopped, or 0 when the whole body was walked.
func undecoded(bodyStart, size int, body *wire.Buffer, complete bool) int {
	if complete || body.Remaining() >= size {
		return 0
	}
	return bodyStart + (size - body.Remaining())
}

// fragmentIDs reads the fragment id list of a values block: uint8 ids terminated by 0xFF
// (BuildEntityFragments, Object.cpp:194-205).
func fragmentIDs(b *wire.Buffer, max int) ([]uint8, error) {
	var ids []uint8
	for range max {
		id := b.ReadU8()
		if err := b.Err(); err != nil {
			// A body that ends inside the list must not read as an endless list of zero ids.
			return nil, fmt.Errorf("fragment id list: %w", err)
		}
		if id == 0xFF {
			return ids, nil
		}
		ids = append(ids, id)
	}
	return nil, parseFail(ReasonCountOverLimit, "fragment id list is not terminated")
}

// The fragments this build can mark updateable, by their wire id (retail id + 1 for every non-tag fragment,
// Object.cpp:196-203; IsUpdateableFragment, WowCSEntityDefinitions.h:105-120).
const (
	fragCGObject        uint8 = 3
	fragVendor          uint8 = 18
	fragPlayerOwnership uint8 = 14
)

// FragmentName returns the core's name for an updateable fragment, given its wire id (the enum's value + 1 for
// every non-tag fragment, WowCSEntityDefinitions.h EntityFragment; Object.cpp:196-203).
func FragmentName(id uint8) string {
	switch id {
	case fragCGObject:
		return "CGObject"
	case 18:
		return "Vendor"
	case 20:
		return "MeshObjectData"
	case 21:
		return "HousingDecor"
	case 22:
		return "HousingRoom"
	case 23:
		return "HousingRoomComponentMesh"
	case 24:
		return "HousingPlayerHouse"
	case 28:
		return "JamHousingCornerstone"
	case 31:
		return "NeighborhoodMirrorData"
	case 32:
		return "MirroredPositionData"
	case 33:
		return "PlayerHouseInfoComponent"
	case 34:
		return "HousingStorage"
	case 35:
		return "HousingFixture"
	case 38:
		return "PlayerInitiativeComponent"
	}
	return fmt.Sprintf("fragment %d", id)
}

func isUpdateableFragment(id uint8) bool {
	switch id {
	case 3, 18, 20, 21, 22, 23, 24, 28, 31, 32, 33, 34, 35, 38:
		return true
	}
	return false
}

func isIndirectFragment(id uint8) bool {
	switch id {
	case 3, 16, 33, 38:
		return true
	}
	return false
}

// walkStop says where a values-block walk stopped when it did not cover the whole body. The zero value is a walk
// that reached the block's declared end.
type walkStop struct {
	// fragment is an updateable fragment whose contents this round does not walk (0 when none): the walk stops there
	// and the block is skipped by its declared size, so the next block stays in sync.
	fragment uint8
	// tail is set when every fragment the block listed was walked and bytes remained: the object type's own data
	// after the CGObject fragment (player, item, container), not decoded this round.
	tail bool
}

// complete reports a walk that covered the block's declared end.
func (s walkStop) complete() bool { return s.fragment == 0 && !s.tail }

// parseValuesCreate walks a create block's values: the field-flags byte, the fragment list, then every updateable
// fragment in id order, each indirect one preceded by its "exists" byte (BaseEntity.cpp:122-128).
func parseValuesCreate(b *wire.Buffer, objectType uint8, thisIsYou bool) (Info, walkStop, error) {
	var info Info
	flags := b.ReadU8()
	owner := flags&0x01 != 0
	partyMember := flags&0x02 != 0
	unitAll := flags&0x04 != 0
	empath := flags&0x08 != 0
	ids, err := fragmentIDs(b, 16)
	if err != nil {
		return info, walkStop{}, err
	}
	for _, id := range ids {
		if !isUpdateableFragment(id) {
			continue
		}
		if isIndirectFragment(id) {
			exists := b.ReadU8()
			if err := b.Err(); err != nil {
				return info, walkStop{}, fmt.Errorf("fragment %d existence byte: %w", id, err)
			}
			if exists == 0 {
				// not active; the server still wrote the byte
				continue
			}
		}
		switch id {
		case fragCGObject:
			if err := readObjectFragmentCreate(b, &info, objectType, owner, unitAll, empath, partyMember, thisIsYou); err != nil {
				return info, walkStop{}, err
			}
		case fragVendor:
			if err := readVendorCreate(b, &info); err != nil {
				return info, walkStop{}, err
			}
		default:
			return info, walkStop{fragment: id}, nil // the block is skipped by its declared size
		}
	}
	if err := b.Err(); err != nil {
		return info, walkStop{}, fmt.Errorf("values body: %w", err)
	}
	return info, walkStop{tail: b.Remaining() != 0}, nil
}

// readObjectFragmentCreate is the CGObject fragment of a create: Object::BuildValuesCreate is virtual, so the
// object type decides which WriteCreate methods follow (Creature.cpp:4124-4128, Player.cpp:3955-3962,
// GameObject.cpp:4283-4287, Corpse.cpp:240-244, DynamicObject.cpp:252-256, Item.cpp:1867-1871).
func readObjectFragmentCreate(b *wire.Buffer, info *Info, objectType uint8, owner, unitAll, empath, partyMember, thisIsYou bool) error {
	info.readObjectCreate(b)
	switch objectType {
	case TypeUnit, TypePlayer, TypeActivePlayer:
		if err := info.readUnitCreate(b, owner, empath, unitAll); err != nil {
			return err
		}
		if objectType == TypeUnit {
			return nil
		}
		// PlayerData follows for any player (Player::BuildValuesCreate, Player.cpp:3955-3962); the receiver's own
		// block is TYPEID_ACTIVE_PLAYER and then carries ActivePlayerData too.
		if err := info.readPlayerCreate(b, partyMember); err != nil {
			return err
		}
		if objectType == TypeActivePlayer && thisIsYou {
			return info.readActivePlayerPrefix(b)
		}
		return nil
	case TypeGameObject:
		return info.readGameObjectCreate(b)
	case TypeCorpse:
		return info.readCorpseCreate(b)
	case TypeDynamicObject:
		return info.readDynamicObjectCreate(b)
	case TypeItem:
		return info.readItemCreate(b, owner)
	case TypeContainer:
		// Bag::BuildValuesCreate (Bag.cpp:185-190): ItemData, then ContainerData.
		if err := info.readItemCreate(b, owner); err != nil {
			return err
		}
		return info.readContainerCreate(b)
	case TypeAzeriteEmpItem, TypeAzeriteItem:
		// More than ItemData follows and this round decodes neither; the block is skipped by its declared size.
		return nil
	}
	return nil
}

// parseValuesUpdate walks a values update: field flags, the id list (only when the fragment set changed), the
// content mask, then each changed updateable fragment (BaseEntity.cpp:164-194,
// Object::BuildEntityFragmentsForValuesUpdateForPlayerWithMask, Object.cpp:114-130).
func parseValuesUpdate(b *wire.Buffer) (Info, walkStop, error) {
	var info Info
	flags := b.ReadU8()
	owner := flags&0x01 != 0
	idsChanged := b.ReadU8() != 0
	ids := []uint8{fragCGObject}
	if idsChanged {
		b.ReadU8() // EntityFragmentSerializationType::Full
		var err error
		ids, err = fragmentIDs(b, 16)
		if err != nil {
			return info, walkStop{}, err
		}
	}
	mask := b.ReadU8()

	// The content mask assigns each updateable fragment a bit, and an indirect fragment two (a "exists" bit and
	// the changed bit): WowCSEntityDefinitions.cpp:66-84. The mask is one byte, so a fragment set that needs more
	// than eight bits cannot be decoded: the block is skipped whole instead of misread.
	maskBits := 0
	for _, id := range ids {
		if !isUpdateableFragment(id) {
			continue
		}
		if isIndirectFragment(id) {
			maskBits += 2
		} else {
			maskBits++
		}
	}
	if maskBits > 8 {
		return info, walkStop{}, parseFail(ReasonMaskTooLarge, "values update needs %d content-mask bits", maskBits)
	}
	bit := 0
	for _, id := range ids {
		if !isUpdateableFragment(id) {
			continue
		}
		existsBit, changedBit := -1, bit
		if isIndirectFragment(id) {
			existsBit = bit
			changedBit = bit + 1
			bit += 2
		} else {
			bit++
		}
		if mask&(1<<uint(changedBit)) == 0 {
			continue
		}
		if existsBit >= 0 && mask&(1<<uint(existsBit)) == 0 {
			continue
		}
		switch id {
		case fragCGObject:
			if err := readObjectFragmentUpdate(b, &info, owner); err != nil {
				return info, walkStop{}, err
			}
		case fragVendor:
			if err := readVendorUpdate(b, &info); err != nil {
				return info, walkStop{}, err
			}
		default:
			return info, walkStop{fragment: id}, nil // the block is skipped by its declared size
		}
	}
	if err := b.Err(); err != nil {
		return info, walkStop{}, fmt.Errorf("values body: %w", err)
	}
	return info, walkStop{tail: b.Remaining() != 0}, nil
}

// readVendorCreate is VendorData::WriteCreate (UpdateFields.cpp:9254-9257): one int32 Flags. A body that ends
// inside the fragment is a parse failure, not a vendor with zero flags: the block must not be reported complete.
func readVendorCreate(b *wire.Buffer, info *Info) error {
	flags := b.ReadI32()
	if err := b.Err(); err != nil {
		return fmt.Errorf("vendor create fragment: %w", err)
	}
	info.HasVendor = true
	info.VendorFlags = flags
	info.f.Vendor = true
	return nil
}

// readVendorUpdate is VendorData::WriteUpdate (UpdateFields.cpp:9264-9277): two mask bits, flushed, then the int32
// Flags when both are set (bit 0 is the structure's presence, bit 1 the field's change). Truncation is an error in
// both reads, like the create form.
func readVendorUpdate(b *wire.Buffer, info *Info) error {
	mask := b.ReadBits(2)
	b.FlushBits()
	if err := b.Err(); err != nil {
		return fmt.Errorf("vendor update fragment mask: %w", err)
	}
	if mask&0x1 != 0 && mask&0x2 != 0 {
		flags := b.ReadI32()
		if err := b.Err(); err != nil {
			return fmt.Errorf("vendor update fragment: %w", err)
		}
		info.HasVendor = true
		info.VendorFlags = flags
		info.f.Vendor = true
	}
	return nil
}

// readObjectFragmentUpdate is the CGObject fragment of a values update: uint32 object-type mask, then the changed
// type blocks in ascending type order (Creature.cpp:4130-4139, Player.cpp:3964-3979).
func readObjectFragmentUpdate(b *wire.Buffer, info *Info, owner bool) error {
	typeMask := b.ReadU32()
	if typeMask&(1<<TypeObject) != 0 {
		info.readObjectUpdate(b)
	}
	if typeMask&(1<<TypeItem) != 0 {
		if err := info.readItemUpdate(b); err != nil {
			return err
		}
	}
	if typeMask&(1<<TypeContainer) != 0 {
		if err := info.readContainerUpdate(b); err != nil {
			return err
		}
	}
	if typeMask&(1<<TypeUnit) != 0 {
		if err := info.readUnitUpdate(b); err != nil {
			return err
		}
	}
	if typeMask&(1<<TypeGameObject) != 0 {
		if err := info.readGameObjectUpdate(b); err != nil {
			return err
		}
	}
	// Player (6) and ActivePlayer (7) updates are the tail of the block: ActivePlayerData's update writer is the
	// 1,300-line Classic bit remap decision 1 keeps out of this round, so they are left to the declared size.
	return b.Err()
}

// parseMovementBlock is BaseEntity::BuildMovementUpdate (BaseEntity.cpp:252-570).
func parseMovementBlock(b *wire.Buffer) (*MoveBlock, error) {
	mv := &MoveBlock{}
	mv.HasEntityPosition = b.ReadBit()
	mv.NoBirthAnim = b.ReadBit()
	mv.EnablePortals = b.ReadBit()
	mv.PlayHoverAnim = b.ReadBit()
	mv.ThisIsYou = b.ReadBit()
	mv.MovementUpdate = b.ReadBit()
	mv.MovementTransport = b.ReadBit()
	mv.Stationary = b.ReadBit()
	mv.CombatVictim = b.ReadBit()
	mv.ServerTime = b.ReadBit()
	mv.Vehicle = b.ReadBit()
	mv.AnimKit = b.ReadBit()
	mv.Rotation = b.ReadBit()
	mv.GameObject = b.ReadBit()
	mv.SmoothPhasing = b.ReadBit()
	mv.SceneObject = b.ReadBit()
	mv.ActivePlayer = b.ReadBit()
	mv.Conversation = b.ReadBit()
	mv.Room = b.ReadBit()
	mv.Decor = b.ReadBit()
	mv.MeshObject = b.ReadBit()
	b.FlushBits()

	pauseTimes := int(b.ReadU32())
	if pauseTimes < 0 || pauseTimes > maxDynamicSize {
		return nil, parseFail(ReasonCountOverLimit, "movement block names %d pause times", pauseTimes)
	}

	if mv.MovementUpdate {
		mv.MoverGUID = mv.guid(b)
		mv.Flags = b.ReadU64()
		mv.Time = b.ReadU32()
		mv.X = b.ReadFloat32()
		mv.Y = b.ReadFloat32()
		mv.Z = b.ReadFloat32()
		mv.O = b.ReadFloat32()
		mv.Pitch = b.ReadFloat32()
		mv.StepUpStartElevation = b.ReadFloat32()
		removeForces := int(b.ReadU32())
		if removeForces < 0 || removeForces > maxDynamicSize {
			return nil, parseFail(ReasonCountOverLimit, "movement block names %d removed movement forces", removeForces)
		}
		mv.MoveIndex = b.ReadU32()
		mv.GravityModifier = b.ReadFloat32()

		hasStandingOn := b.ReadBit()
		hasTransport := b.ReadBit()
		hasFall := b.ReadBit()
		hasSpline := b.ReadBit()
		b.ReadBit() // HeightChangeFailed
		b.ReadBit() // RemoteTimeValid
		hasInertia := b.ReadBit()
		hasAdvFlying := b.ReadBit()
		hasDriveStatus := b.ReadBit()
		b.FlushBits()
		if hasStandingOn {
			mv.guid(b)
		}
		if hasTransport {
			t := &TransportInfo{}
			t.GUID = mv.guid(b)
			t.X = b.ReadFloat32()
			t.Y = b.ReadFloat32()
			t.Z = b.ReadFloat32()
			t.O = b.ReadFloat32()
			t.Seat = b.ReadI8()
			t.Time = b.ReadU32()
			if b.ReadBit() {
				t.PrevTime = b.ReadU32()
			}
			if b.ReadBit() {
				t.VehicleID = b.ReadU32()
			}
			mv.Transport = t
		}
		if hasFall {
			j := &JumpInfo{}
			j.FallTime = b.ReadU32()
			j.ZSpeed = b.ReadFloat32()
			if b.ReadBit() {
				j.SinAngle = b.ReadFloat32()
				j.CosAngle = b.ReadFloat32()
				j.XYSpeed = b.ReadFloat32()
			}
			mv.Jump = j
		}
		for range removeForces {
			mv.guid(b) // RemoveForcesIDs, written zero here (BaseEntity.cpp:307)
		}
		// Skipped by exact size, not refused: the position already arrived, and none of these blocks moves it
		// (MovementInfo.h:152-204, BaseEntity.cpp:345-365).
		if hasInertia {
			b.Skip(4)  // id
			b.Skip(12) // force XYZ
			b.Skip(4)  // lifetime
		}
		if hasAdvFlying {
			b.Skip(8) // forwardVelocity, upVelocity
		}
		if hasDriveStatus {
			b.Skip(8) // speed, movementAngle
			b.ReadBit()
			b.ReadBit()
			b.FlushBits()
		}
		mv.Walk = b.ReadFloat32()
		mv.Run = b.ReadFloat32()
		mv.RunBack = b.ReadFloat32()
		mv.Swim = b.ReadFloat32()
		mv.SwimBack = b.ReadFloat32()
		mv.Flight = b.ReadFloat32()
		mv.FlightBack = b.ReadFloat32()
		mv.TurnRate = b.ReadFloat32()
		mv.PitchRate = b.ReadFloat32()
		moveForces := int(b.ReadU32())
		b.ReadFloat32() // MovementForcesModMagnitude
		if moveForces < 0 || moveForces > maxDynamicSize {
			return nil, parseFail(ReasonCountOverLimit, "movement block names %d movement forces", moveForces)
		}
		b.Skip(17 * 4) // the advanced-flying speeds (BaseEntity.cpp:388-404)
		for range moveForces {
			// WriteMovementForceWithDirection (MovementPackets.cpp:522-562): packed GUID, origin XYZ and direction
			// XYZ (three floats each), TransportID, Magnitude, MovementForceID, DurationMs, EndTimestamp, Flags,
			// then the two type bits flushed.
			mv.guid(b)
			b.Skip(24)
			b.Skip(4) // TransportID
			b.Skip(4) // Magnitude
			b.Skip(4) // MovementForceID
			b.Skip(4) // DurationMs
			b.Skip(4) // EndTimestamp
			b.Skip(4) // Flags
			b.ReadBits(2)
			b.FlushBits()
		}
		hasSpline2 := b.ReadBit()
		b.FlushBits()
		if hasSpline2 != hasSpline {
			return nil, parseFail(ReasonUnknownFlag, "movement block disagrees with itself on the spline bit")
		}
		if hasSpline {
			s, err := readCreateSpline(b)
			if err != nil {
				return nil, err
			}
			mv.Spline = s
		}
	}

	if mv.MovementTransport {
		mv.guid(b) // the transport's GUID (WorldObject::m_movementInfo.transport)
		b.Skip(16) // its position
		b.Skip(1)  // seat
		b.Skip(4)  // time
		hasPrev := b.ReadBit()
		hasVehicle := b.ReadBit()
		if hasPrev {
			b.Skip(4)
		}
		if hasVehicle {
			b.Skip(4)
		}
	}
	if mv.Stationary {
		mv.StationaryPosition[0] = b.ReadFloat32()
		mv.StationaryPosition[1] = b.ReadFloat32()
		mv.StationaryPosition[2] = b.ReadFloat32()
		mv.StationaryPosition[3] = b.ReadFloat32()
	}
	if mv.CombatVictim {
		mv.guid(b)
	}
	if mv.ServerTime {
		b.Skip(4)
	}
	if mv.Vehicle {
		b.Skip(4) // RecID
		b.Skip(4) // InitialRawFacing
	}
	if mv.AnimKit {
		b.Skip(6) // AiID, MovementID, MeleeID
	}
	if mv.Rotation {
		b.Skip(8) // GetPackedLocalRotation
	}
	if mv.GameObject {
		b.Skip(4) // WorldEffectID
		bit8 := b.ReadBit()
		isTransport := b.ReadBit()
		hasPathProgress := b.ReadBit()
		b.FlushBits()
		if isTransport {
			b.Skip(4) // TimeOffset
			b.Skip(4) // NextStopTimestamp
			b.ReadBit()
			b.ReadBit()
			b.ReadBit()
			b.FlushBits()
		}
		if bit8 {
			b.Skip(4)
		}
		if hasPathProgress {
			b.Skip(4)
		}
	}
	if mv.SmoothPhasing {
		replaceActive := b.ReadBit()
		stopAnimKits := b.ReadBit()
		replaceObject := b.ReadBit()
		b.FlushBits()
		_ = replaceActive
		_ = stopAnimKits
		if replaceObject {
			mv.guid(b)
		}
	}
	if mv.SceneObject {
		b.ReadBit()
		b.ReadBit()
		b.FlushBits()
	}
	if mv.ActivePlayer {
		hasSceneInstanceIDs := b.ReadBit()
		hasRuneState := b.ReadBit()
		b.FlushBits()
		if hasSceneInstanceIDs {
			n := int(b.ReadU32())
			if n < 0 || n > maxDynamicSize {
				return nil, parseFail(ReasonCountOverLimit, "active player names %d scene instances", n)
			}
			b.Skip(4 * n)
		}
		if hasRuneState {
			b.Skip(1) // rune bitmask
			b.Skip(1) // runes state
			maxRunes := int(b.ReadU32())
			if maxRunes < 0 || maxRunes > 16 {
				return nil, parseFail(ReasonCountOverLimit, "active player names %d runes", maxRunes)
			}
			b.Skip(maxRunes)
		}
	}
	if mv.Conversation {
		if b.ReadBit() {
			b.Skip(4)
		}
		b.FlushBits()
	}
	// Pause times are not stored with the count: the writer holds the count at the top (:281) and appends the values
	// at the very end of the block through append(uint32 const*, size_t), which writes cnt * sizeof(uint32) bytes
	// (:694-696, ByteBuffer.h:601-609). Four bytes per entry, after every flag-gated block.
	if pauseTimes != 0 {
		mv.PauseTimes = make([]uint32, pauseTimes)
		for n := range mv.PauseTimes {
			mv.PauseTimes[n] = b.ReadU32()
		}
	}
	return mv, b.Err()
}

// readCreateSpline is CommonMovement::WriteCreateObjectSplineDataBlock (MovementPackets.cpp:398-530). It is not
// the SMSG_ON_MONSTER_MOVE layout: the destination is written up front, a facing target is a bare GUID (no angle),
// and the path is sent as plain points with no packed deltas.
func readCreateSpline(b *wire.Buffer) (*CreateSpline, error) {
	s := &CreateSpline{}
	s.ID = b.ReadU32()
	s.DestX = b.ReadFloat32()
	s.DestY = b.ReadFloat32()
	s.DestZ = b.ReadFloat32()
	s.HasMove = b.ReadBit()
	b.FlushBits()
	if !s.HasMove {
		return s, b.Err()
	}
	s.Flags = b.ReadU32()
	s.Face = b.ReadU8()
	s.Elapsed = b.ReadI32()
	s.Duration = b.ReadU32()
	b.Skip(8) // DurationModifier, NextDurationModifier
	switch s.Face {
	case 1: // MONSTER_MOVE_FACING_SPOT
		b.Skip(12)
	case 2: // MONSTER_MOVE_FACING_TARGET
		wire.ReadPackedGUID(b)
	case 3: // MONSTER_MOVE_FACING_ANGLE
		b.Skip(4)
	}
	hasFadeObjectTime := b.ReadBit()
	points := int(b.ReadBits(16))
	b.ReadBit() // HasSplineFilter (never written, MovementPackets.cpp:459)
	hasSpellEffect := b.ReadBit()
	hasJumpExtra := b.ReadBit()
	hasTurn := b.ReadBit()
	hasAnimTier := b.ReadBit()
	b.ReadBit() // HasSpellVisualData
	b.FlushBits()
	if hasFadeObjectTime {
		b.Skip(4)
	}
	if points < 0 || points > maxDynamicSize {
		return nil, parseFail(ReasonCountOverLimit, "spline names %d points", points)
	}
	s.Points = make([][3]float32, points)
	for n := range points {
		s.Points[n][0] = b.ReadFloat32()
		s.Points[n][1] = b.ReadFloat32()
		s.Points[n][2] = b.ReadFloat32()
	}
	if hasSpellEffect {
		wire.ReadPackedGUID(b)
		b.Skip(16)
	}
	if hasJumpExtra {
		b.Skip(12) // JumpGravity, StartTime, Duration (override)
	}
	if hasTurn {
		b.Skip(12)
	}
	if hasAnimTier {
		b.Skip(13) // TierTransitionID, AnimTier, StartTime, ExtraDuration
	}
	return s, b.Err()
}
