package objects

// The world model: a map from GUID to what the client knows about an object, fed by one reader goroutine and read
// concurrently (the walker and the report read positions while the reader keeps applying updates).

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Speeds is the movement speed block of a create (BaseEntity.cpp:390-398), yards per second.
type Speeds struct {
	Walk, Run, RunBack, Swim, SwimBack, Flight, FlightBack, TurnRate, PitchRate float32
}

// Object is one entry of the world model.
type Object struct {
	GUID wire.GUID
	Type uint8
	Info Info

	X, Y, Z, O float32
	Speeds     Speeds
	// Flags is the object's last movement flags (the create block's, then the relays').
	Flags uint64
	// Spline is the last SMSG_ON_MONSTER_MOVE this object got, with the client time it arrived at; PositionAt
	// interpolates it. It is cleared by a later move update from the object itself.
	Spline   *MonsterMove
	SplineAt time.Time
	// LastSeen is when the last packet named this object.
	LastSeen time.Time
	// seq is the model's touch order. It orders eviction like LastSeen does but stays strict when two packets share
	// a timestamp.
	seq uint64
	// Partial is true when some block that named this object was not decoded to its declared end; the fields that
	// were decoded before the first skipped region are kept (see mergeInfo).
	Partial bool
	// Name is filled by the name lookups; EntryName belongs to Info.Entry (shared by all objects of that entry).
	Name string

	// Combat state (round 4): who this unit attacks and who attacks it (SMSG_ATTACK_START/STOP), the in-combat
	// flag, death (health zero in the values or an attack stop that says so), the auras by slot and the cast in
	// progress. Auras and Cast are written under the model's lock and read through Combat, which copies them.
	Attacking  wire.GUID
	AttackedBy wire.GUID
	InCombat   bool
	Dead       bool
	// Ghost is true while the unit has the SPELL_AURA_GHOST aura (8326) and is a player: a released spirit still
	// has 1 health, so Dead stays true while Ghost is (objects/death.go).
	Ghost bool
	// ResurrectedAt is the last time a ghost came back (its aura went and its health was above 0).
	ResurrectedAt time.Time
	Auras         map[uint16]*Aura
	Cast          *CastState
	Cooldowns     map[int32]time.Time
}

// Position returns the object's position, following the spline when it is moving and now is inside it (linear
// interpolation; see MonsterMove.PositionAt).
func (o *Object) Position(now time.Time) (x, y, z float32) {
	if o.Spline != nil && !o.SplineAt.IsZero() {
		elapsed := now.Sub(o.SplineAt)
		if elapsed > 0 {
			ms := uint32(elapsed.Milliseconds())
			if ms < o.Spline.MoveTime+monsterMoveSlackMs {
				return o.Spline.PositionAt(ms)
			}
			if ms >= o.Spline.MoveTime {
				// arrived: the spline's destination is where the server puts the object next
				return o.Spline.DestX, o.Spline.DestY, o.Spline.DestZ
			}
		}
	}
	return o.X, o.Y, o.Z
}

// monsterMoveSlackMs is how long past its duration a spline is still treated as the truth for a position: the
// server sends the next spline before the previous one ends, and a late packet must not snap the object back.
const monsterMoveSlackMs = 500

// World is the GUID-keyed model. One goroutine writes (the socket reader); any number read.
type World struct {
	mu          sync.RWMutex
	mapID       uint32
	objects     map[wire.GUID]*Object
	entries     map[int32]string
	goNames     map[int32]GameObjectName
	players     map[wire.GUID]string
	created     int
	updated     int
	destroyed   int
	outOfRange  int
	orphans     int
	partial     int
	unknownMove int
	// partialKinds counts the partial blocks by a bounded key (object type, block form, where the walk stopped): a
	// run that reports partial objects says which kinds they are, not only how many.
	partialKinds map[string]int
	// resurrections counts the units this model saw come back from the ghost state.
	resurrections int
	// deadSince holds the guids a merge just took to dead, in merge order, until a caller drains them: the combat
	// log writes one death row per guid. It is appended to only on a death, never on a plain update.
	deadSince []wire.GUID
	unparsed  map[string]int
	// unparsedDetail keeps the parser's own text for the first block of each unparsed key, so the walk summary
	// can name the failure without letting the server grow the summary: one string per key, never per block.
	unparsedDetail map[string]string
	// activeMover is never evicted: every walk reads it.
	activeMover wire.GUID
	// seq orders eviction; order holds (seq, guid) in touch order with stale entries skipped lazily.
	seq   uint64
	order []orderEntry
	head  int
	// lastUpdate is the client time of the last applied update, for the report.
	applied int
	// threat is the pushed per-mob threat table (threat.go) and encounters the undrained encounter events
	// (encounter.go). ended remembers the successful ends for the kill credit's dedupe and open the encounters a
	// start named and no end closed. All are rewritten by the packet loop under the model's lock.
	threat     map[wire.GUID]*ThreatMob
	encounters []EncounterEvent
	ended      map[uint32]time.Time
	open       map[uint32]bool
}

// orderEntry is one touch of an object, for eviction. An entry whose seq is not the object's current seq is stale.
type orderEntry struct {
	seq  uint64
	guid wire.GUID
}

// The model's hard bounds. A hostile or merely busy server cannot grow the client's memory: objects are capped and
// the longest-unseen object that is not the active mover is evicted, and each name cache is capped (names are only
// needed for the report and a later answer replaces a cached one).
const (
	maxObjects = 20000
	maxNames   = 10000
)

// TypeOfUnknown marks an object this client only saw a values update for: its type never arrived.
const TypeOfUnknown uint8 = 0xFF

// NewWorld returns an empty model for a map.
func NewWorld(mapID uint32) *World {
	return &World{
		mapID:          mapID,
		objects:        make(map[wire.GUID]*Object),
		entries:        make(map[int32]string),
		goNames:        make(map[int32]GameObjectName),
		players:        make(map[wire.GUID]string),
		partialKinds:   make(map[string]int),
		unparsed:       make(map[string]int),
		unparsedDetail: make(map[string]string),
		threat:         make(map[wire.GUID]*ThreatMob),
	}
}

// MapID is the map this model belongs to.
func (w *World) MapID() uint32 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.mapID
}

// ResetForMap empties the model for a new map (SMSG_NEW_WORLD). Every object of the old map goes, with its eviction
// queue; the name caches stay (a name is not map state), the run's counters are cumulative and stay, and the active
// mover's GUID stays until the new burst's create block names it again.
func (w *World) ResetForMap(mapID uint32) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.objects = make(map[wire.GUID]*Object)
	w.order = w.order[:0]
	w.head = 0
	w.mapID = mapID
	// Threat and encounter state belong to one instance: the old instance's lists and events must not be read as
	// the new one's.
	w.threat = make(map[wire.GUID]*ThreatMob)
	w.encounters = w.encounters[:0]
	w.ended = nil
	w.open = nil
}

// Counts is a snapshot of how many blocks the model applied.
type Counts struct {
	Objects    int
	Created    int
	Updated    int
	Destroyed  int
	OutOfRange int
	// Orphans counts values updates for objects this client never saw created: the fields are kept (a later update
	// still applies) but the object is not counted, because its type is unknown.
	Orphans int
	// Partial counts objects with at least one block that was not decoded to its declared end.
	Partial int
	// PartialKinds counts those blocks by type, form and stop point (the key is a bounded closed set).
	PartialKinds map[string]int
	// UnknownMoves counts movement packets for a GUID the model does not hold: they never create an object.
	UnknownMoves int
	// Resurrections counts units this model saw come back from the ghost state (aura 8326 gone, health above 0).
	Resurrections int
	// Unparsed counts skipped values blocks by object type and reason code. Both halves are closed sets, so the
	// map cannot grow past maxUnparsedKeys however many blocks a server skips.
	Unparsed map[string]int
	// UnparsedDetail holds the parser's text for the first skipped block of each key.
	UnparsedDetail map[string]string
	Applied        int
	ByType         map[uint8]int
}

// Counts returns the snapshot.
func (w *World) Counts() Counts {
	w.mu.RLock()
	defer w.mu.RUnlock()
	c := Counts{
		Created:        w.created,
		Updated:        w.updated,
		Destroyed:      w.destroyed,
		OutOfRange:     w.outOfRange,
		Orphans:        w.orphans,
		Partial:        w.partial,
		PartialKinds:   make(map[string]int, len(w.partialKinds)),
		UnknownMoves:   w.unknownMove,
		Resurrections:  w.resurrections,
		Unparsed:       make(map[string]int, len(w.unparsed)),
		UnparsedDetail: make(map[string]string, len(w.unparsedDetail)),
		Applied:        w.applied,
		ByType:         make(map[uint8]int),
	}
	for kind, n := range w.partialKinds {
		c.PartialKinds[kind] = n
	}
	for reason, n := range w.unparsed {
		c.Unparsed[reason] = n
	}
	for key, text := range w.unparsedDetail {
		c.UnparsedDetail[key] = text
	}
	for _, o := range w.objects {
		if o.Type == TypeOfUnknown {
			continue
		}
		c.Objects++
		c.ByType[o.Type]++
	}
	return c
}

// Apply applies one SMSG_UPDATE_OBJECT. received is the client time it arrived (the reference point of a spline
// a create block carries). Blocks are capped by maxObjects: a create for a new GUID past the bound evicts the
// longest-unseen object that is not the active mover. A block whose values body could not be decoded is still
// framed by its declared size: it is counted and skipped, and the run continues.
func (w *World) Apply(u *Update, received time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.applied++
	if u.MapID != 0 {
		w.mapID = uint32(u.MapID)
	}
	for _, g := range u.Destroy {
		if _, ok := w.objects[g]; ok {
			delete(w.objects, g)
			w.destroyed++
		}
	}
	for _, g := range u.OutOfRange {
		if _, ok := w.objects[g]; ok {
			delete(w.objects, g)
			w.outOfRange++
		}
	}
	for i := range u.Blocks {
		b := &u.Blocks[i]
		o, created := w.objectForLocked(b.GUID)
		if created {
			if b.Values {
				w.orphans++
			} else {
				w.created++
			}
		} else {
			w.updated++
		}
		w.touchLocked(o, received)
		if !b.Values {
			// The block header names the type even when its values cannot be decoded.
			o.Type = b.ObjectType
			o.Info.Type = b.ObjectType
		}
		if !b.ValuesComplete || b.Unparsed != ReasonNone {
			w.partialKinds[partialKind(b, o)]++
		}
		if b.Unparsed != ReasonNone {
			// Nothing decoded: the block is skipped by its size, the object is marked partial and the reason is
			// counted for the walk-check summary. The key is (type, reason), never the parser's text, and the text
			// of the first block of each key is the only one kept.
			w.markPartialLocked(o)
			key := unparsedKey(b)
			w.unparsed[key]++
			if _, ok := w.unparsedDetail[key]; !ok {
				w.unparsedDetail[key] = b.UnparsedDetail
			}
			continue
		}
		if !b.ValuesComplete {
			w.markPartialLocked(o)
		}
		// mergeInfo copies only the fields whose presence bit the parser set. Those bits are set strictly in wire
		// order and decoding stops at the first region it cannot decode, so every bit that is set names a field
		// that ended before the first skipped region: the block's tail may be skipped, its decoded head is proven.
		mergeInfo(&o.Info, b.Info)
		if applyDeathLocked(o) {
			w.deadSince = append(w.deadSince, o.GUID)
		}
		if b.Info.f.Flags && o.Info.HasUnit {
			// UNIT_FLAG_IN_COMBAT in the unit's flags is the server's own combat state for that unit.
			o.InCombat = InCombatFromFlags(o.Info.Flags)
		}
		if mv := b.Movement; mv != nil {
			if mv.MovementUpdate {
				o.X, o.Y, o.Z, o.O = mv.X, mv.Y, mv.Z, mv.O
				o.Flags = mv.Flags
				o.Speeds = Speeds{
					Walk: mv.Walk, Run: mv.Run, RunBack: mv.RunBack, Swim: mv.Swim,
					SwimBack: mv.SwimBack, Flight: mv.Flight, FlightBack: mv.FlightBack,
					TurnRate: mv.TurnRate, PitchRate: mv.PitchRate,
				}
				if mv.Spline != nil {
					// The packet's position is the spline's start *now*; the elapsed part is already behind it.
					// Interpolate from here for the remaining duration, not from elapsed 0 (that would count the
					// progress twice).
					elapsed := uint32(0)
					if mv.Spline.Elapsed > 0 {
						elapsed = uint32(mv.Spline.Elapsed)
					}
					remaining := uint32(0)
					if mv.Spline.Duration > elapsed {
						remaining = mv.Spline.Duration - elapsed
					}
					o.Spline = &MonsterMove{
						MoverGUID: b.GUID,
						StartX:    mv.X, StartY: mv.Y, StartZ: mv.Z,
						DestX: mv.Spline.DestX, DestY: mv.Spline.DestY, DestZ: mv.Spline.DestZ,
						MoveTime: remaining,
					}
					o.SplineAt = received
				} else {
					o.Spline = nil
				}
			} else if mv.Stationary {
				o.X, o.Y, o.Z, o.O = mv.StationaryPosition[0], mv.StationaryPosition[1], mv.StationaryPosition[2], mv.StationaryPosition[3]
			}
		}
	}
}

// unparsedKind is the summary's type half of one skipped values block: the 15 object types TypeName names, "values
// update" for a values block (it carries no type) and one bucket for any other type byte. The type byte is
// server-controlled, so the unknown values share a key instead of minting 256 of them.
func unparsedKind(b *WorldFragment) string {
	if b.Values {
		return "values update"
	}
	if b.ObjectType > TypeMeshObject {
		return "unknown type"
	}
	return TypeName(b.ObjectType)
}

// unparsedKinds is the number of type buckets unparsedKind returns: the 15 object types, "values update" and
// "unknown type".
const unparsedKinds = 17

// maxUnparsedKeys bounds the skipped-block summary: one key per type bucket and reason code. It is checked by
// TestUnparsedSummaryIsBounded, which streams 100,000 blocks with distinct server-controlled counts.
const maxUnparsedKeys = unparsedKinds * int(parseReasonCount-1)

// unparsedKey is the summary key of one skipped values block. Both halves are closed sets (unparsedKinds kinds,
// parseReasonCount-1 reasons), so the map cannot grow with the count of skipped blocks.
func unparsedKey(b *WorldFragment) string {
	return unparsedKind(b) + ": " + b.Unparsed.String()
}

// partialKind is the summary key of one partially decoded block: the object type the block names (a values update
// carries none, so the model's type for the GUID stands in), whether it was a create or a values update, and where
// the walk stopped. Every part is a closed set, so the map stays bounded.
func partialKind(b *WorldFragment, o *Object) string {
	typ := b.ObjectType
	if b.Values {
		typ = o.Type
	}
	form := "create"
	if b.Values {
		form = "values update"
	}
	var where string
	switch {
	case b.Unparsed != ReasonNone:
		where = "an undecodable block (" + b.Unparsed.String() + ")"
	case b.UndecodedFragment != 0:
		where = "the " + FragmentName(b.UndecodedFragment) + " fragment, not decoded this round"
	default:
		where = "the type's own data after the walked blocks, not decoded this round"
	}
	return TypeName(typ) + " " + form + ": " + where
}

// markPartialLocked records that a block naming o was not decoded to its declared end.
func (w *World) markPartialLocked(o *Object) {
	if !o.Partial {
		o.Partial = true
		w.partial++
	}
}

// objectForLocked returns the entry for guid, creating it under maxObjects. created reports whether it is new.
func (w *World) objectForLocked(guid wire.GUID) (o *Object, created bool) {
	if o = w.objects[guid]; o != nil {
		return o, false
	}
	if len(w.objects) >= maxObjects {
		w.evictLocked()
	}
	o = &Object{GUID: guid, Type: TypeOfUnknown}
	w.objects[guid] = o
	return o, true
}

// touchLocked stamps an object as seen and appends its eviction-order entry. The queue is rebuilt from the live
// objects when it outgrows them, so it cannot grow without bound in a busy model where nothing is evicted.
func (w *World) touchLocked(o *Object, received time.Time) {
	w.seq++
	o.LastSeen = received
	o.seq = w.seq
	w.order = append(w.order, orderEntry{seq: o.seq, guid: o.GUID})
	if len(w.order)-w.head > 4*len(w.objects)+1024 {
		w.rebuildOrderLocked()
	}
}

func (w *World) rebuildOrderLocked() {
	w.order = w.order[:0]
	w.head = 0
	for _, o := range w.objects {
		w.order = append(w.order, orderEntry{seq: o.seq, guid: o.GUID})
	}
	sort.Slice(w.order, func(i, j int) bool { return w.order[i].seq < w.order[j].seq })
}

// evictLocked removes the longest-unseen object that is not the active mover. Stale queue entries (objects already
// removed or touched again since their entry) are skipped; when the queue holds no live entry, a scan finds it.
func (w *World) evictLocked() {
	for w.head < len(w.order) {
		e := w.order[w.head]
		w.head++
		o := w.objects[e.guid]
		if o == nil || o.seq != e.seq {
			continue
		}
		if o.GUID == w.activeMover && len(w.objects) > 1 {
			continue
		}
		delete(w.objects, e.guid)
		return
	}
	var oldest *Object
	for _, o := range w.objects {
		if o.GUID == w.activeMover && len(w.objects) > 1 {
			continue
		}
		if oldest == nil || o.seq < oldest.seq {
			oldest = o
		}
	}
	if oldest != nil {
		delete(w.objects, oldest.GUID)
	}
}

// TouchObjects stamps the named objects as most recently used, without changing their LastSeen, so the object cap
// evicts other objects before them. Unknown GUIDs are ignored.
func (w *World) TouchObjects(guids []wire.GUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, g := range guids {
		o := w.objects[g]
		if o == nil {
			continue
		}
		w.seq++
		o.seq = w.seq
		w.order = append(w.order, orderEntry{seq: o.seq, guid: o.GUID})
	}
	if len(w.order)-w.head > 4*len(w.objects)+1024 {
		w.rebuildOrderLocked()
	}
}

// SetActiveMover names the object a walk drives; it is never evicted.
func (w *World) SetActiveMover(guid wire.GUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.activeMover = guid
}

// mergeInfo copies the fields a block carried over the stored ones. It is field by field on purpose: a values
// update carries only the fields its mask named (Info.f), and every other field must survive.
func mergeInfo(dst *Info, src Info) {
	f := src.f
	if src.HasObject {
		dst.HasObject = true
	}
	if f.Entry {
		dst.Entry = src.Entry
	}
	if f.DynamicFlags {
		dst.DynamicFlags = src.DynamicFlags
	}
	if f.Scale {
		dst.Scale = src.Scale
	}
	if src.HasVendor {
		dst.HasVendor = true
	}
	if f.Vendor {
		dst.VendorFlags = src.VendorFlags
	}
	if src.HasUnit {
		dst.HasUnit = true
	}
	if f.DisplayID {
		dst.DisplayID = src.DisplayID
	}
	if f.NpcFlags {
		dst.NpcFlags = src.NpcFlags
	}
	if f.NpcFlags2 {
		dst.NpcFlags2 = src.NpcFlags2
	}
	if f.Charm {
		dst.Charm = src.Charm
	}
	if f.Summon {
		dst.Summon = src.Summon
	}
	if f.CharmedBy {
		dst.CharmedBy = src.CharmedBy
	}
	if f.SummonedBy {
		dst.SummonedBy = src.SummonedBy
	}
	if f.CreatedBy {
		dst.CreatedBy = src.CreatedBy
	}
	if f.Target {
		dst.Target = src.Target
	}
	if f.Race {
		dst.Race = src.Race
	}
	if f.Class {
		dst.Class = src.Class
	}
	if f.Gender {
		dst.Gender = src.Gender
	}
	if f.CreatureType {
		dst.CreatureType = src.CreatureType
	}
	if f.DisplayPower {
		dst.DisplayPower = src.DisplayPower
	}
	if f.Health {
		dst.Health = src.Health
	}
	if f.MaxHealth {
		dst.MaxHealth = src.MaxHealth
	}
	if f.Power {
		dst.Power = src.Power
	}
	if f.MaxPower {
		dst.MaxPower = src.MaxPower
	}
	if f.Level {
		dst.Level = src.Level
	}
	if f.FactionTemplate {
		dst.FactionTemplate = src.FactionTemplate
	}
	if f.Flags {
		dst.Flags = src.Flags
	}
	if f.Flags2 {
		dst.Flags2 = src.Flags2
	}
	if f.Flags3 {
		dst.Flags3 = src.Flags3
	}
	if f.Flags4 {
		dst.Flags4 = src.Flags4
	}
	if f.BoundingRadius {
		dst.BoundingRadius = src.BoundingRadius
	}
	if f.CombatReach {
		dst.CombatReach = src.CombatReach
	}
	if f.DisplayScale {
		dst.DisplayScale = src.DisplayScale
	}
	if f.StandState {
		dst.StandState = src.StandState
	}
	if f.VisFlags {
		dst.VisFlags = src.VisFlags
	}
	if f.AnimTier {
		dst.AnimTier = src.AnimTier
	}
	if f.Guild {
		dst.Guild = src.Guild
	}
	if f.ComboTarget {
		dst.ComboTarget = src.ComboTarget
	}
	if f.CurrentAreaID {
		dst.CurrentAreaID = src.CurrentAreaID
	}
	if f.MountDisplayID {
		dst.MountDisplayID = src.MountDisplayID
	}
	if f.NativeDisplayID {
		dst.NativeDisplayID = src.NativeDisplayID
	}
	if src.HasGameObject {
		dst.HasGameObject = true
	}
	if f.GOState {
		dst.GOState = src.GOState
	}
	if f.GOType {
		dst.GOType = src.GOType
	}
	if f.GODisplayID {
		dst.GODisplayID = src.GODisplayID
	}
	if f.GOFlags {
		dst.GOFlags = src.GOFlags
	}
	if f.GOFaction {
		dst.GOFaction = src.GOFaction
	}
	if src.HasCorpse {
		dst.HasCorpse = true
	}
	if f.Owner {
		dst.Owner = src.Owner
	}
	if f.CorpseDisplayID {
		dst.CorpseDisplayID = src.CorpseDisplayID
	}
	if src.HasDynamicObject {
		dst.HasDynamicObject = true
	}
	if f.Caster {
		dst.Caster = src.Caster
	}
	if f.SpellID {
		dst.SpellID = src.SpellID
	}
	if src.Item != nil {
		if f.ItemOwner {
			dst.item().Owner = src.Item.Owner
		}
		if f.ContainedIn {
			dst.item().ContainedIn = src.Item.ContainedIn
		}
		if f.StackCount {
			dst.item().StackCount = src.Item.StackCount
		}
		if f.Durability {
			dst.item().Durability = src.Item.Durability
		}
		if f.MaxDurability {
			dst.item().MaxDurability = src.Item.MaxDurability
		}
	}
	if src.Container != nil {
		if f.NumSlots {
			dst.container().NumSlots = src.Container.NumSlots
		}
		if f.Slots {
			dc, sc := dst.container(), src.Container
			if sc.delta {
				// A ContainerData update names only the changed slots; the others are not in the packet.
				for n, ch := range sc.changed {
					if ch {
						dc.Slots[n] = sc.Slots[n]
					}
				}
			} else {
				dc.Slots = sc.Slots
			}
		}
	}
	if src.Player != nil {
		if f.QuestLog {
			dst.player().QuestLog = src.Player.QuestLog
		}
		if f.VisibleItems {
			dst.player().VisibleItems = src.Player.VisibleItems
		}
		if f.InvSlots {
			dst.player().InvSlots = src.Player.InvSlots
		}
		if f.InvSlotDeltas {
			pl := dst.player()
			for slot, guid := range src.Player.InvSlotDeltas {
				if slot >= 0 && slot < len(pl.InvSlots) {
					pl.InvSlots[slot] = guid
				}
			}
		}
		if f.Coinage {
			dst.player().Coinage = src.Player.Coinage
		}
		if f.QuestLogDeltas {
			// Copy on write: a snapshot reader may still hold the slice the last create or update left.
			pl := dst.player()
			log := append([]QuestLogEntry(nil), pl.QuestLog...)
			for slot, d := range src.Player.QuestLogDeltas {
				if slot >= 0 && slot < len(log) {
					d.Apply(&log[slot])
				}
			}
			pl.QuestLog = log
		}
	}
}

// ApplyMonsterMove applies SMSG_ON_MONSTER_MOVE for one object. A mover the model does not hold is counted, never
// created: a hostile server must not grow the model with movement packets alone.
func (w *World) ApplyMonsterMove(m *MonsterMove, received time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o, ok := w.objects[m.MoverGUID]
	if !ok {
		w.unknownMove++
		return
	}
	w.updated++
	w.touchLocked(o, received)
	o.X, o.Y, o.Z = m.StartX, m.StartY, m.StartZ
	o.Spline = m
	o.SplineAt = received.Add(-time.Duration(m.Elapsed) * time.Millisecond)
}

// ApplyMoveUpdate applies a relayed movement info of another mover (SMSG_MOVE_UPDATE or the teleport variant). An
// unknown mover is counted, never created.
func (w *World) ApplyMoveUpdate(mi MovementInfo, speeds map[string]float32, received time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o, ok := w.objects[mi.GUID]
	if !ok {
		w.unknownMove++
		return
	}
	w.updated++
	w.touchLocked(o, received)
	o.X, o.Y, o.Z, o.O = mi.X, mi.Y, mi.Z, mi.O
	o.Flags = mi.Flags
	o.Spline = nil
	if speeds != nil {
		if v, ok := speeds["walk"]; ok {
			o.Speeds.Walk = v
		}
		if v, ok := speeds["run"]; ok {
			o.Speeds.Run = v
		}
	}
}

// ApplyTeleport applies SMSG_MOVE_TELEPORT's destination for one mover. An unknown mover is counted, never created.
func (w *World) ApplyTeleport(t MoveTeleport, received time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o, ok := w.objects[t.MoverGUID]
	if !ok {
		w.unknownMove++
		return
	}
	w.updated++
	w.touchLocked(o, received)
	o.X, o.Y, o.Z, o.O = t.X, t.Y, t.Z, t.O
	o.Spline = nil
}

// SetName caches a player's name by GUID.
func (w *World) SetName(guid wire.GUID, name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.players[guid]; !ok && len(w.players) >= maxNames {
		return // the cache is full: a new name is dropped, an existing one still updates
	}
	w.players[guid] = name
	if o, ok := w.objects[guid]; ok {
		o.Name = name
	}
}

// NameOf returns the cached player name for a GUID, or "".
func (w *World) NameOf(guid wire.GUID) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.players[guid]
}

// PlayerByName returns the GUID of a player whose cached name matches, case-insensitively: the server normalises
// names before every lookup (ObjectMgr.cpp:157-185, PlayerNameMapHolder::Find, ObjectAccessor.cpp:89-97), so a
// name typed in any case names the same player.
func (w *World) PlayerByName(name string) (wire.GUID, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for guid, have := range w.players {
		if strings.EqualFold(have, name) {
			return guid, true
		}
	}
	return wire.EmptyGUID, false
}

// UniquePlayerByName is PlayerByName for a caller that must not label the wrong player: when two cached players bear
// the name (a bystander or a namesake from another realm in view, a stale entry of someone who left) it finds none,
// where PlayerByName returns whichever the map yields first.
func (w *World) UniquePlayerByName(name string) (wire.GUID, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var found wire.GUID
	n := 0
	for guid, have := range w.players {
		if strings.EqualFold(have, name) {
			found = guid
			n++
		}
	}
	if n != 1 {
		return wire.EmptyGUID, false
	}
	return found, true
}

// SetEntryName caches a creature's name by entry.
func (w *World) SetEntryName(entry int32, name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.entries[entry]; !ok && len(w.entries) >= maxNames {
		return // the cache is full: a new name is dropped, an existing one still updates
	}
	w.entries[entry] = name
}

// EntryName returns the cached creature name of an entry.
func (w *World) EntryName(entry int32) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.entries[entry]
}

// SetGameObjectName caches a game object's query answer by entry: its names and its type. An Allow=false
// answer (Found false: the core has no template for the entry) is kept as a negative entry under the same
// bound, so UnknownGameObjects stops re-asking it every round.
func (w *World) SetGameObjectName(g GameObjectName) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.goNames[int32(g.Entry)]; !ok && len(w.goNames) >= maxNames {
		return // the cache is full: a new entry is dropped, an existing one still updates
	}
	w.goNames[int32(g.Entry)] = g
}

// GameObjectName returns the cached query answer for an entry, or false.
func (w *World) GameObjectName(entry int32) (GameObjectName, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	g, ok := w.goNames[entry]
	return g, ok
}

// UnknownGameObjects lists up to limit game-object entries this client has seen but has no query answer for
// yet (including negative Allow=false answers), nearest first, each entry once: the doors, chests and gossip
// objects loot and route checks show.
func (w *World) UnknownGameObjects(x, y, z float32, limit int) []int32 {
	objs := w.GameObjects(x, y, z)
	w.mu.RLock()
	defer w.mu.RUnlock()
	var out []int32
	seen := make(map[int32]bool)
	for _, o := range objs {
		if len(out) >= limit {
			break
		}
		entry := o.Info.Entry
		if entry == 0 || seen[entry] {
			continue
		}
		if _, ok := w.goNames[entry]; !ok {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	return out
}

// Name returns the best name this client has for an object: the player name by GUID, else the creature name of
// its entry, else the game-object name of its entry from the query cache, else "".
func (w *World) Name(o *Object) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if o.Name != "" {
		return o.Name
	}
	if name, ok := w.players[o.GUID]; ok {
		return name
	}
	if o.Info.HasGameObject || o.Type == TypeGameObject {
		if g, ok := w.goNames[o.Info.Entry]; ok {
			return g.Name[0]
		}
		return ""
	}
	if !o.Info.HasUnit {
		return ""
	}
	return w.entries[o.Info.Entry]
}

// Object returns one object by GUID.
func (w *World) Object(guid wire.GUID) (*Object, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	o, ok := w.objects[guid]
	if !ok {
		return nil, false
	}
	cp := *o
	return &cp, true
}

// Units returns a snapshot of the units (players and creatures) around, nearest first to (x,y,z).
func (w *World) Units(x, y, z float32) []*Object {
	w.mu.RLock()
	out := make([]*Object, 0, len(w.objects))
	for _, o := range w.objects {
		if !o.Info.HasUnit {
			continue
		}
		cp := *o
		out = append(out, &cp)
	}
	w.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		di := distance2(out[i].X, out[i].Y, out[i].Z, x, y, z)
		dj := distance2(out[j].X, out[j].Y, out[j].Z, x, y, z)
		if di != dj {
			return di < dj
		}
		return out[i].GUID.Low < out[j].GUID.Low
	})
	return out
}

// Items returns a snapshot of the objects that carry item data (items and bags), in GUID order.
func (w *World) Items() []*Object {
	w.mu.RLock()
	out := make([]*Object, 0, 64)
	for _, o := range w.objects {
		if o.Info.Item == nil {
			continue
		}
		cp := *o
		out = append(out, &cp)
	}
	w.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].GUID.Low != out[j].GUID.Low {
			return out[i].GUID.Low < out[j].GUID.Low
		}
		return out[i].GUID.High < out[j].GUID.High
	})
	return out
}

// UnknownUnits lists up to limit units whose name this client does not have yet, nearest first: players (by GUID)
// and creatures (by entry, each entry once).
func (w *World) UnknownUnits(x, y, z float32, limit int) (players []wire.GUID, entries []int32) {
	units := w.Units(x, y, z)
	w.mu.RLock()
	defer w.mu.RUnlock()
	seen := make(map[int32]bool)
	for _, o := range units {
		if len(players)+len(entries) >= limit {
			break
		}
		if o.GUID.Type() == guidTypePlayer {
			if _, ok := w.players[o.GUID]; !ok && o.Name == "" {
				players = append(players, o.GUID)
			}
			continue
		}
		entry := o.Info.Entry
		if entry == 0 || seen[entry] {
			continue
		}
		if _, ok := w.entries[entry]; !ok {
			seen[entry] = true
			entries = append(entries, entry)
		}
	}
	return players, entries
}

// GameObjects returns a snapshot of the game objects around, nearest first to (x,y,z).
func (w *World) GameObjects(x, y, z float32) []*Object {
	return w.nearest(x, y, z, func(o *Object) bool { return o.Info.HasGameObject || o.Type == TypeGameObject })
}

// LootableUnits returns a snapshot of the dead units (corpses that can be looted) around, nearest first. A corpse is
// a unit whose health is zero or that the model saw die; the server's own answer to CMSG_LOOT_UNIT decides whether
// it may be looted.
func (w *World) LootableUnits(x, y, z float32) []*Object {
	return w.nearest(x, y, z, func(o *Object) bool {
		return o.Info.HasUnit && (o.Dead || (o.Info.Health <= 0 && o.Info.MaxHealth > 0))
	})
}

// nearest snapshots the objects keep accepts, ordered like Units: by distance from (x,y,z), then GUID.
func (w *World) nearest(x, y, z float32, keep func(*Object) bool) []*Object {
	w.mu.RLock()
	out := make([]*Object, 0, 16)
	for _, o := range w.objects {
		if !keep(o) {
			continue
		}
		cp := *o
		out = append(out, &cp)
	}
	w.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		di := distance2(out[i].X, out[i].Y, out[i].Z, x, y, z)
		dj := distance2(out[j].X, out[j].Y, out[j].Z, x, y, z)
		if di != dj {
			return di < dj
		}
		return out[i].GUID.Low < out[j].GUID.Low
	})
	return out
}

// guidTypePlayer is ObjectGuid's high type for a player (ObjectGuid.h:45-120, HighGuid::Player = 2).
const guidTypePlayer = 2

func distance2(x1, y1, z1, x2, y2, z2 float32) float32 {
	dx, dy, dz := x1-x2, y1-y2, z1-z2
	return dx*dx + dy*dy + dz*dz
}

// Distance is the 3D distance between two points.
func Distance(x1, y1, z1, x2, y2, z2 float32) float32 {
	return float32(math.Sqrt(float64(distance2(x1, y1, z1, x2, y2, z2))))
}
