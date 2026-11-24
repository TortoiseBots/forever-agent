package world

// The fighter against a fake world with a fake clock. The fake is the server at the packet level: every client
// packet is decoded here, and every answer the real server sends is built the way its writers build it and fed
// through the session's own combat handler — the swing error, the attack stop, the attack start, the attack round,
// the spell start, go and failures. The swing ticks follow Unit::DoMeleeAttackIfReady (Unit.cpp:2246-2270): a tick
// outside the server's reach or its 120-degree front arc is answered with SMSG_ATTACK_SWING_ERROR, never with a
// round. The clock only advances when the fighter waits, so the assertions are exact instead of timing-dependent.
// The creates the fake feeds the model are the committed capture-derived fixtures of round 3, parsed by the
// production parser.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// loadObjectFixture returns the payload of one packet of round 3's committed fixture file.
func loadObjectFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../objects/testdata/objects.json")
	if err != nil {
		t.Fatalf("reading round 3's fixtures: %v", err)
	}
	var doc struct {
		Packets []struct {
			Name    string `json:"name"`
			Payload string `json:"payload"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing round 3's fixtures: %v", err)
	}
	for _, p := range doc.Packets {
		if p.Name == name {
			payload, err := hex.DecodeString(p.Payload)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return payload
		}
	}
	t.Fatalf("round 3's fixtures have no %q", name)
	return nil
}

// parseCreateBlock parses one create payload and returns the model update and its first block.
func parseCreateBlock(t *testing.T, payload []byte) (*objects.Update, objects.WorldFragment) {
	t.Helper()
	u, err := objects.ParseUpdateObject(payload)
	if err != nil {
		t.Fatalf("the create fixture: %v", err)
	}
	if len(u.Blocks) != 1 {
		t.Fatalf("the create fixture has %d blocks", len(u.Blocks))
	}
	return u, u.Blocks[0]
}

// patchFloats replaces every occurrence of one float with another (the fixtures' positions are synthetic, so the
// rewrite is exact by construction).
func patchFloats(t *testing.T, payload []byte, from, to float32) []byte {
	t.Helper()
	old := make([]byte, 4)
	binary.LittleEndian.PutUint32(old, math.Float32bits(from))
	want := make([]byte, 4)
	binary.LittleEndian.PutUint32(want, math.Float32bits(to))
	if !bytes.Contains(payload, old) {
		t.Fatalf("the fixture has no %.1f to replace", from)
	}
	return bytes.ReplaceAll(payload, old, want)
}

// patchHealth zeroes the first health value of a create (health is written before max health, so the maximum stays
// and the model's death rule sees health 0 with a real maximum).
func patchHealth(t *testing.T, payload []byte, health int64) []byte {
	t.Helper()
	old := make([]byte, 8)
	binary.LittleEndian.PutUint64(old, uint64(health))
	i := bytes.Index(payload, old)
	if i < 0 {
		t.Fatalf("the fixture has no health %d", health)
	}
	out := append([]byte(nil), payload...)
	copy(out[i:i+8], make([]byte, 8))
	return out
}

// fightSent is one packet the fake server received.
type fightSent struct {
	core    uint32
	payload []byte
	at      time.Time
}

// errRefusedSwing is the fake's marker for a swing tick that produced an error packet instead of a round.
var errRefusedSwing = errors.New("the swing tick was refused")

// fightWorld is the fake server and the fake clock.
type fightWorld struct {
	t      *testing.T
	now    time.Time
	world  *objects.World
	report *Report
	opts   Options

	self   wire.GUID
	target wire.GUID
	fight  *fightLog
	s      *session

	selfCreate   []byte
	targetCreate []byte
	// selfHealthAt is the offset of the health field in selfCreate.
	selfHealthAt int

	sent []fightSent

	// Scenario knobs.
	rounds      int // attack rounds sent so far
	killAfter   int // the target dies after this many rounds (0: never)
	walkAwayAt  int // after this round the target moves away (0: never)
	walkAwayTo  float32
	castReason  int32
	castSpell   int32
	castPreCast bool // the failure is the pre-cast CAST_FAILED form (the client's cast id)
	damage      int32
	// lastCastTarget is the target block of the last CMSG_CAST_SPELL, so the server's own answers name the same one
	// the request did; castSpellID is the spell it named.
	lastCastTarget combat.SpellTarget
	castSpellID    int32

	// refuseCloserThan is the server's own reach for the fake's swing ticks: while the fighter is farther than it,
	// every tick answers "not in range" whatever the client believes (0: off). The target was placed farther than
	// the client's reach, so the client has to trust the server's answer over its own distance.
	refuseCloserThan float32
	// thirdParty is a unit that hits the fighter before the server's answer to the swing: a mob that pulled the
	// character must not pass for contact with the target.
	thirdParty       wire.GUID
	thirdPartyDamage int32

	// nextSwing is when the fake's swing timer fires next; a swing tick is only delivered then, so the fake keeps
	// the server's own weapon-speed cadence instead of one round per wait.
	nextSwing time.Time
	swingErrs int
	legs      int
	lastLeg   [3]float32
	castSeen  bool
	castCalls int

	// The server's own swings.
	refuseAttack     bool // HandleAttackSwingOpcode's invalid-target path: an immediate ATTACK_STOP
	forceSwingErrs   int  // the first N swing ticks answer "not in range" whatever the distance
	stopAfterContact bool // a stop naming the fighter as attacker after the first round
	stopThenDie      bool // that stop, then the target's death 250 ms later
	dieAt            time.Time
	attackActive     bool
	swingAt          time.Time
	swingFrom        [3]float32
	swingTargetAt    [3]float32

	// Failure injection.
	failMoveAfter int // the (n+1)th walk leg fails (-1: never)
	failWaitAfter int // the (n+1)th WaitUntil fails (-1: never)
	waits         int

	// The cast answers.
	castStartDelay time.Duration // when the start is delivered after CMSG_CAST_SPELL
	castGoDelay    time.Duration // and the go or failure after the start
	castStartTime  uint32        // the start's CastTime
	castNoAnswer   bool          // the fake never sends the go (a cast the server parks)
	castNoStart    bool          // the fake sends only the failure (a pre-cast refusal has no start)
	castFailID     wire.GUID     // the failure's cast id when it is not the client's (the server's own)
	castStartAt    time.Time
	castGoAt       time.Time
	pendingCast    *combat.SpellCast
	castSentAt     time.Time

	// beforeWait runs at the top of every WaitUntil: the tests put a hostile packet or a deadline probe there.
	beforeWait func(now time.Time)
}

// newFightWorld builds the world with the fighter and its target already known, from round 3's fixtures: the
// player create for the fighter and the creature create for the target, six yards east of the fighter.
func newFightWorld(t *testing.T, targetDistance float32) *fightWorld {
	t.Helper()
	f := &fightWorld{
		t:             t,
		now:           time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		world:         objects.NewWorld(0),
		report:        &Report{Counts: map[string]int{}},
		opts:          Options{FightFor: 10 * time.Second, Logf: func(string, ...any) {}},
		damage:        7,
		failMoveAfter: -1,
		failWaitAfter: -1,
	}
	selfPayload := loadObjectFixture(t, "create_player")
	selfUpdate, selfBlock := parseCreateBlock(t, selfPayload)
	f.self = selfBlock.GUID
	f.world.Apply(selfUpdate, f.now)

	targetPayload := patchFloats(t, loadObjectFixture(t, "create_creature"), 1000, 1000+targetDistance)
	targetUpdate, targetBlock := parseCreateBlock(t, targetPayload)
	f.target = targetBlock.GUID
	f.world.Apply(targetUpdate, f.now)

	f.selfCreate = selfPayload
	f.targetCreate = targetPayload
	// Where the fighter's create carries its health: the fixture's own value, so a later patch finds the field
	// whatever health the model holds by then.
	health := make([]byte, 8)
	binary.LittleEndian.PutUint64(health, uint64(selfBlock.Info.Health))
	f.selfHealthAt = bytes.Index(f.selfCreate, health)
	if f.selfHealthAt < 0 {
		t.Fatalf("the self create has no health %d", selfBlock.Info.Health)
	}
	f.fight = &fightLog{started: f.now, now: f.Now, self: f.self}
	f.fight.setTarget(f.target)
	return f
}

// session builds the session the fighter runs in.
func (f *fightWorld) session() *session {
	s := &session{
		opts:        f.opts,
		ctx:         context.Background(),
		world:       f.world,
		report:      f.report,
		fight:       f.fight,
		self:        f.self,
		selfSeen:    true,
		fightSender: f,
	}
	x, y, z := 1000.0, 2000.0, 50.0
	s.mover = &objects.Mover{
		GUID: f.self, Sender: f, X: float32(x), Y: float32(y), Z: float32(z), RunSpeed: 7, Flags: 0x80000000,
		ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(f.now).Milliseconds()) },
	}
	f.s = s
	return s
}

// server feeds one server packet through the session's own combat handler, exactly as the reader goroutine would.
func (f *fightWorld) server(core uint32, payload []byte) {
	f.s.handleCombat(worldconn.Packet{Core: core, Payload: payload})
}

// buildAttackStart writes SMSG_ATTACK_START: attacker, victim (CombatPackets.cpp:30-35).
func buildAttackStart(attacker, victim wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, attacker)
	wire.WritePackedGUID(b, victim)
	return b.Bytes()
}

// buildAttackStop writes SMSG_ATTACK_STOP: attacker, victim, the NowDead bit (CombatPackets.cpp:36-45).
func buildAttackStop(attacker, victim wire.GUID, nowDead bool) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, attacker)
	wire.WritePackedGUID(b, victim)
	b.WriteBit(nowDead)
	b.FlushBits()
	return b.Bytes()
}

// buildSwingError writes SMSG_ATTACK_SWING_ERROR: three bits of AttackSwingErr (CombatPackets.cpp:89-95).
func buildSwingError(reason combat.SwingErr) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(reason), 3)
	b.FlushBits()
	return b.Bytes()
}

// moverPos returns the fighter's position from the mover (the client's own belief, where the swing is sent from).
func (f *fightWorld) moverPos() (float32, float32, float32) {
	return f.s.mover.X, f.s.mover.Y, f.s.mover.Z
}

// Send is the fake server's read side.
func (f *fightWorld) Send(core uint32, payload []byte) error {
	// The production transport is the session, which refuses a packet once a socket has died; the fake does the
	// same so a wait that sends (a walk, a swing) fails the way it does live.
	if f.s != nil {
		if err := f.s.linkDead(); err != nil {
			return err
		}
	}
	f.sent = append(f.sent, fightSent{core: core, payload: append([]byte(nil), payload...), at: f.now})
	switch core {
	case wire.CMSGSetSelection:
		if g := packetGUID(payload); g != f.target {
			return errors.New("selection is not the target")
		}
	case wire.CMSGMoveSetFacing, wire.CMSGMoveStartForward:
		mi, err := objects.ParseMovementPacket(payload)
		if err != nil {
			return err
		}
		if core == wire.CMSGMoveStartForward {
			f.legs++
			f.lastLeg = [3]float32{mi.X, mi.Y, mi.Z}
			if f.failMoveAfter >= 0 && f.legs > f.failMoveAfter {
				return errors.New("fake: the walk leg was not sent")
			}
		}
	case wire.CMSGAttackSwing:
		if g := packetGUID(payload); g != f.target {
			return errors.New("attack swing at the wrong victim")
		}
		if f.refuseAttack {
			// A unit other than the target hits the character before the server answers the swing: a round with it is
			// not contact with the target, so the stop below must still read as a refused target.
			if !f.thirdParty.IsZero() {
				f.server(wire.SMSGAttackerStateUpdate, buildAttackerStateUpdate(f.thirdParty, f.self, f.thirdPartyDamage))
			}
			// HandleAttackSwingOpcode's !IsValidAttackTarget path: a stop at once, no attack state.
			f.server(wire.SMSGAttackStop, buildAttackStop(f.self, f.target, false))
			return nil
		}
		f.attackActive = true
		f.server(wire.SMSGAttackStart, buildAttackStart(f.self, f.target))
		f.nextSwing = f.now
	case wire.CMSGAttackStop:
		// Unit::AttackStop answers with SMSG_ATTACK_STOP naming this unit as the attacker (Unit.cpp:6262-6288).
		stopped := f.attackActive
		f.attackActive = false
		if stopped {
			f.server(wire.SMSGAttackStop, buildAttackStop(f.self, f.target, false))
		}
	case wire.CMSGCastSpell:
		f.castSeen = true
		f.castCalls++
		// The whole request, read the way the server reads it: a field out of place or a trailing byte fails here.
		req := readClientCast(f.t, payload)
		if f.castSpell != 0 && req.SpellID != f.castSpell {
			return errors.New("unexpected spell id")
		}
		// A request either names the unit target or carries no target at all (a self cast); nothing else.
		switch {
		case req.Target.Flags == 0 && req.Target.Unit.IsZero():
		case req.Target.Flags == combat.TargetFlagUnit && req.Target.Unit == f.target:
		default:
			return fmt.Errorf("the cast's target is %#x/%v, want the unit target or nothing", req.Target.Flags, req.Target.Unit)
		}
		f.lastCastTarget = req.Target
		f.castSpellID = req.SpellID
		f.pendingCast = &combat.SpellCast{CasterUnit: f.self, SpellID: req.SpellID, CastID: combat.CastGUID(0, req.SpellID, 900)}
		f.castSentAt = f.now
		f.castStartAt = f.now.Add(f.castStartDelay)
		if !f.castNoAnswer {
			f.castGoAt = f.castStartAt.Add(f.castGoDelay)
		}
	}
	return nil
}

// WaitUntil advances the fake clock and delivers what the server would send by then: the cast's answers, a delayed
// death, and one swing tick while the fighter is attacking.
func (f *fightWorld) WaitUntil(ctx context.Context, until time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.waits++
	if f.failWaitAfter >= 0 && f.waits > f.failWaitAfter {
		return errors.New("fake: the connection dropped")
	}
	if until.After(f.now) {
		f.now = until
	}
	if f.beforeWait != nil {
		f.beforeWait(f.now)
	}
	f.deliverCastAnswers(f.now)
	if !f.dieAt.IsZero() && !f.now.Before(f.dieAt) {
		f.dieAt = time.Time{}
		f.killTarget()
	}
	if f.attackActive && !f.now.Before(f.nextSwing) {
		if err := f.swingTick(); err != nil {
			f.nextSwing = f.now.Add(100 * time.Millisecond) // setAttackTimer(BASE_ATTACK, 100)
		} else {
			f.nextSwing = f.now.Add(2 * time.Second) // a level-1 weapon's swing time
		}
	}
	return nil
}

// deliverCastAnswers sends the pending cast's start and then its go or failure when their time has come. Every answer
// is the server's own packet, built here from SpellPackets.cpp and fed through the session's combat handler, so the
// parsers and the bookkeeping they feed are the production ones.
func (f *fightWorld) deliverCastAnswers(now time.Time) {
	if f.pendingCast != nil && !f.castStartAt.After(now) {
		if f.castNoStart {
			f.pendingCast = nil
			return
		}
		cast := *f.pendingCast
		f.server(wire.SMSGSpellStart, buildSpellStart(f.self, f.self, cast.CastID, cast.SpellID, f.castStartTime, f.castTarget()))
		f.pendingCast = nil
	}
	if !f.castGoAt.IsZero() && !f.castGoAt.After(now) {
		f.castGoAt = time.Time{}
		spell := f.castSpellID
		if spell == 0 {
			spell = 772
		}
		if f.castReason != 0 {
			castID := combat.CastGUID(0, spell, 900)
			if f.castPreCast {
				castID = f.clientCastID()
			}
			if !f.castFailID.IsZero() {
				castID = f.castFailID
			}
			if f.castPreCast {
				// SMSG_CAST_FAILED is the answer sent to the caster itself: no caster field, the reason is the wide
				// int32 pair (SpellPackets.cpp:556-567).
				f.server(wire.SMSGCastFailed, buildCastFailed(castID, spell, f.castReason))
				return
			}
			f.server(wire.SMSGSpellFailure, buildSpellFailure(f.self, castID, spell, f.castReason))
			return
		}
		f.server(wire.SMSGSpellGo, buildSpellGo(f.self, f.self, combat.CastGUID(0, spell, 900), spell, f.castStartTime, f.castTarget()))
	}
}

// castTarget is the target block the server's own spell packets carry: the one the request named.
func (f *fightWorld) castTarget() combat.SpellTarget {
	return f.lastCastTarget
}

// clientCastID is the id the session's current Cast sent, from the packet the fake received.
func (f *fightWorld) clientCastID() wire.GUID {
	for i := len(f.sent) - 1; i >= 0; i-- {
		if f.sent[i].core == wire.CMSGCastSpell {
			return castID(f.sent[i].payload)
		}
	}
	return wire.EmptyGUID
}

// Now is the fake clock.
func (f *fightWorld) Now() time.Time { return f.now }

// swingTick is one tick of the server's melee timer: Unit::DoMeleeAttackIfReady's range and arc test
// (Unit.cpp:2251-2266) and then the round.
func (f *fightWorld) swingTick() error {
	self, ok := f.world.Object(f.self)
	if !ok {
		return nil
	}
	target, ok := f.world.Object(f.target)
	if !ok {
		return nil
	}
	if f.forceSwingErrs > 0 {
		f.forceSwingErrs--
		f.swingErrs++
		f.server(wire.SMSGAttackSwingError, buildSwingError(combat.SwingNotInRange))
		return errRefusedSwing
	}
	mx, my, mz := f.moverPos()
	tx, ty, tz := target.Position(f.now)
	reach := meleeReach(self, target)
	dist := objects.Distance(mx, my, mz, tx, ty, tz)
	if f.refuseCloserThan > 0 && dist > f.refuseCloserThan {
		// The server's reach is shorter than the client's own: only a fighter that steps past what it believes is
		// the stop distance gets a round.
		f.swingErrs++
		f.server(wire.SMSGAttackSwingError, buildSwingError(combat.SwingNotInRange))
		return errRefusedSwing
	}
	if dist > reach {
		f.swingErrs++
		f.server(wire.SMSGAttackSwingError, buildSwingError(combat.SwingNotInRange))
		return errRefusedSwing
	}
	// 120 degrees: outside the victim's boundary radius the fighter must face it (Unit.cpp:2259-2261,
	// Position::HasInArc, Position.cpp:173-190).
	radius := target.Info.BoundingRadius
	if radius < 2 {
		radius = 2 // MIN_MELEE_REACH, UnitDefines.h:30
	}
	if dist > radius {
		angle := normalizeAngle(objects.OrientationTo(mx, my, tx, ty) - f.s.mover.O)
		if absf(angle) > float32(math.Pi/3) {
			f.swingErrs++
			f.server(wire.SMSGAttackSwingError, buildSwingError(combat.SwingBadFacing))
			return errRefusedSwing
		}
	}
	f.swingAt = f.now
	f.swingFrom = [3]float32{mx, my, mz}
	f.swingTargetAt = [3]float32{tx, ty, tz}
	f.deliverRound()
	return nil
}

// deliverRound is one swing round for the fighter and one for the target, then the target's movement, stop or death
// when the scenario says so. Each round is the server's own packet, so the world model and the session's current
// fight log (the command replaces it per candidate) are the ones the production handler feeds.
func (f *fightWorld) deliverRound() {
	f.rounds++
	f.server(wire.SMSGAttackerStateUpdate, buildAttackerStateUpdate(f.self, f.target, f.damage))
	f.server(wire.SMSGAttackerStateUpdate, buildAttackerStateUpdate(f.target, f.self, 3))
	if f.walkAwayAt != 0 && f.rounds == f.walkAwayAt {
		payload := patchFloats(f.t, f.targetCreate, 1000+float32(f.targetDistance()), f.walkAwayTo)
		u, err := objects.ParseUpdateObject(payload)
		if err != nil {
			f.t.Fatalf("the moved target's create: %v", err)
		}
		f.world.Apply(u, f.now)
	}
	if f.stopAfterContact && f.rounds == 1 {
		f.attackActive = false
		f.server(wire.SMSGAttackStop, buildAttackStop(f.self, f.target, false))
		if f.stopThenDie {
			f.dieAt = f.now.Add(250 * time.Millisecond)
			return
		}
	}
	if f.killAfter != 0 && f.rounds >= f.killAfter {
		f.killTarget()
	}
}

// killTarget writes the target's death the way the server does: its health falls to 0 in the values.
func (f *fightWorld) killTarget() {
	o, ok := f.world.Object(f.target)
	if !ok {
		return
	}
	payload := patchHealth(f.t, f.targetCreate, o.Info.Health)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil {
		f.t.Fatalf("the dead target's create: %v", err)
	}
	f.world.Apply(u, f.now)
	f.attackActive = false
}

// targetDistance is how far east the target was placed.
func (f *fightWorld) targetDistance() float32 {
	t, ok := f.world.Object(f.target)
	if !ok {
		return 0
	}
	return t.X - 1000
}

// packetGUID decodes one packed GUID payload.
func packetGUID(payload []byte) wire.GUID {
	b := wire.NewReader(payload)
	return wire.ReadPackedGUID(b)
}

// castID reads the CastID out of CMSG_CAST_SPELL: it is the request's first packed GUID (SpellPackets.cpp:
// SpellCastRequest::Read reads CastID first).
func castID(payload []byte) wire.GUID {
	b := wire.NewReader(payload)
	return wire.ReadPackedGUID(b)
}

// readClientCast reads the whole CMSG_CAST_SPELL request the way SpellPackets.cpp:243-288 does, independently of the
// writer: cast id, send flags, the three Misc ints, the spell, the visual, the target block, the missile trajectory,
// the crafting fields and the closing bit group. It requires the packet to end on that bit group, so a field the
// writer omits, adds or misorders fails here.
func readClientCast(t *testing.T, p []byte) combat.CastRequest {
	t.Helper()
	b := wire.NewReader(p)
	var r combat.CastRequest
	r.CastID = wire.ReadPackedGUID(b)
	r.SendCastFlags = b.ReadU8()
	for range 3 {
		if v := b.ReadI32(); v != 0 {
			t.Fatalf("Misc is not zero: %d", v)
		}
	}
	r.SpellID = b.ReadI32()
	r.Visual = combat.SpellVisual{SpellXSpellVisualID: b.ReadI32(), ScriptVisualID: b.ReadI32()}
	r.Target.Flags = b.ReadU32()
	r.Target.Unit = wire.ReadPackedGUID(b)
	r.Target.Item = wire.ReadPackedGUID(b)
	r.Target.Housing = wire.ReadPackedGUID(b)
	if b.ReadBit() {
		t.Fatal("HousingIsResident is set")
	}
	hasSrc, hasDst, hasOrient, hasMap := b.ReadBit(), b.ReadBit(), b.ReadBit(), b.ReadBit()
	nameLen := int(b.ReadBits(7))
	b.FlushBits()
	if hasSrc || hasDst || hasOrient || hasMap {
		t.Fatalf("an optional target part is set: %v %v %v %v", hasSrc, hasDst, hasOrient, hasMap)
	}
	r.Target.Name = b.ReadString(nameLen)
	// MissileTrajectory: Pitch and Speed (SpellPackets.cpp:243-288 reads both).
	if pitch := b.ReadFloat32(); pitch != 0 {
		t.Fatalf("trajectory pitch %v", pitch)
	}
	if speed := b.ReadFloat32(); speed != 0 {
		t.Fatalf("trajectory speed %v", speed)
	}
	if npc := wire.ReadPackedGUID(b); !npc.IsZero() {
		t.Fatalf("crafting NPC %v", npc)
	}
	for _, what := range []string{"extra currency", "crafting reagent", "removed reagent"} {
		if n := b.ReadU32(); n != 0 {
			t.Fatalf("%s count %d", what, n)
		}
	}
	if flags := b.ReadU8(); flags != 0 {
		t.Fatalf("crafting cast flags %d", flags)
	}
	if b.ReadBit() {
		t.Fatal("ReceiveTime present")
	}
	if b.ReadBit() {
		t.Fatal("MoveUpdate present")
	}
	if n := b.ReadBits(2); n != 0 {
		t.Fatalf("weight count %d", n)
	}
	if b.ReadBit() {
		t.Fatal("CraftingOrderID present")
	}
	b.FlushBits()
	if b.Remaining() != 0 {
		t.Fatalf("%d trailing byte(s) after the cast request", b.Remaining())
	}
	return r
}

// ---- the server's spell packets, hand-built from the server's own writers ----

// writeCastTarget writes SpellTargetData the way SpellPackets.cpp:325-355 writes it: the flags, the three packed
// GUIDs, the absent-optionals bit group and the name.
func writeCastTarget(b *wire.Buffer, t combat.SpellTarget) {
	b.WriteU32(t.Flags)
	wire.WritePackedGUID(b, t.Unit)
	wire.WritePackedGUID(b, t.Item)
	wire.WritePackedGUID(b, t.Housing)
	b.WriteBit(false) // HousingIsResident
	b.WriteBit(false) // Src
	b.WriteBit(false) // Dst
	b.WriteBit(false) // Orientation
	b.WriteBit(false) // MapID
	b.WriteBits(0, 7) // name length
	b.FlushBits()
}

// writeSpellCast writes SpellCastData the way Spell::SendSpellStart and SendSpellGo write it (SpellPackets.cpp:415-464):
// both GUID pairs, the cast id, the spell and its visual, the three cast-flag words, the cast time, the target, the
// missile trajectory, the ammo and destination index, the immunities, the heal prediction, and the closing bit group
// of the hit, miss, status, power and target-point counts. This client's casts carry none of those lists.
func writeSpellCast(b *wire.Buffer, caster, casterUnit, castID wire.GUID, spellID int32, castTime uint32, target combat.SpellTarget) {
	wire.WritePackedGUID(b, caster)
	wire.WritePackedGUID(b, casterUnit)
	wire.WritePackedGUID(b, castID)
	wire.WritePackedGUID(b, wire.EmptyGUID) // OriginalCast
	b.WriteI32(spellID)
	b.WriteI32(0) // SpellXSpellVisualID
	b.WriteI32(0) // ScriptVisualID
	b.WriteU32(0) // CastFlags
	b.WriteU32(0) // CastFlagsEx
	b.WriteU32(0) // CastFlagsEx2
	b.WriteU32(castTime)
	writeCastTarget(b, target)
	b.WriteU32(0)                           // MissileTrajectory.TravelTime
	b.WriteFloat32(0)                       // MissileTrajectory.Pitch
	b.WriteI32(0)                           // AmmoDisplayID
	b.WriteU8(0)                            // DestLocSpellCastIndex
	b.WriteI32(0)                           // Immunities[0]
	b.WriteI32(0)                           // Immunities[1]
	b.WriteI32(0)                           // Predict.Points
	b.WriteU32(0)                           // Predict.Type
	wire.WritePackedGUID(b, wire.EmptyGUID) // Predict.BeaconGUID
	b.WriteBits(0, 16)                      // hit target count
	b.WriteBits(0, 16)                      // miss target count
	b.WriteBits(0, 16)                      // hit status count
	b.WriteBits(0, 16)                      // miss status count
	b.WriteBits(0, 9)                       // remaining power count
	b.WriteBit(false)                       // runes present
	b.WriteBits(0, 16)                      // target point count
	b.FlushBits()
}

// buildSpellStart is SMSG_SPELL_START (SpellPackets.cpp:466-471).
func buildSpellStart(caster, casterUnit, castID wire.GUID, spellID int32, castTime uint32, target combat.SpellTarget) []byte {
	b := wire.NewWriter()
	writeSpellCast(b, caster, casterUnit, castID, spellID, castTime, target)
	return b.Bytes()
}

// buildSpellGo is SMSG_SPELL_GO: the same body, then the log-data bit and its flush (SpellPackets.cpp:473-483).
func buildSpellGo(caster, casterUnit, castID wire.GUID, spellID int32, castTime uint32, target combat.SpellTarget) []byte {
	b := wire.NewWriter()
	writeSpellCast(b, caster, casterUnit, castID, spellID, castTime, target)
	b.WriteBit(false) // no log data
	b.FlushBits()
	return b.Bytes()
}

// buildCastFailed is SMSG_CAST_FAILED (SpellPackets.cpp:556-567): the cast id, the spell and its visual, the reason
// with its two arguments, and the unit that failed it (none).
func buildCastFailed(castID wire.GUID, spellID, reason int32) []byte {
	return buildCastFailedArgs(castID, spellID, reason, -1, -1)
}

// buildCastFailedArgs is buildCastFailed with the two FillSpellCastFailedArgs arguments a refusal can carry (the
// missing reagent or tool item, the totem category, the spell focus).
func buildCastFailedArgs(castID wire.GUID, spellID, reason, arg1, arg2 int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, castID)
	b.WriteI32(spellID)
	b.WriteI32(0) // SpellXSpellVisualID
	b.WriteI32(0) // ScriptVisualID
	b.WriteI32(reason)
	b.WriteI32(arg1)
	b.WriteI32(arg2)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	return b.Bytes()
}

// buildSpellFailure is SMSG_SPELL_FAILURE (SpellPackets.cpp:532-542): the caster and the cast id first, the spell and
// its visual, a uint16 reason, and the unit that failed it (none).
func buildSpellFailure(caster, castID wire.GUID, spellID int32, reason int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, caster)
	wire.WritePackedGUID(b, castID)
	b.WriteI32(spellID)
	b.WriteI32(0) // SpellXSpellVisualID
	b.WriteI32(0) // ScriptVisualID
	b.WriteU16(uint16(reason))
	wire.WritePackedGUID(b, wire.EmptyGUID)
	return b.Bytes()
}

// buildAttackerStateUpdate is SMSG_ATTACKER_STATE_UPDATE: the absent log-data bit, the round's size, and the round
// itself (AttackerStateUpdate::Write, CombatLogPackets.cpp:392-406; the round at :371-425). The tuning block is the
// writer's fixed one with no scaling (CombatLogPacketsCommon.cpp:182-205).
func buildAttackerStateUpdate(attacker, victim wire.GUID, damage int32) []byte {
	r := wire.NewWriter()
	r.WriteU32(0x00800002) // hitInfoAffectsVictim | hitInfoRageGain
	wire.WritePackedGUID(r, attacker)
	wire.WritePackedGUID(r, victim)
	r.WriteI32(damage)
	r.WriteI32(damage)
	r.WriteI32(-1) // OverDamage
	r.WriteU8(1)   // HasSub
	r.WriteI32(1)  // SchoolMask
	r.WriteFloat32(float32(damage))
	r.WriteI32(damage)
	r.WriteU8(1)   // VictimState = HIT
	r.WriteU32(0)  // AttackerState
	r.WriteU32(0)  // MeleeSpellID
	r.WriteI32(10) // RageGained (hitInfoRageGain)
	r.WriteFloat32(0)
	r.WriteFloat32(0)
	r.WriteU16(0)     // PlayerLevelDelta
	r.WriteI32(0)     // ScalingHealthItemLevelCurve
	r.WriteI32(0)     // Unused1117
	r.WriteI32(0)     // ScalingHealthPrimaryStatCurve
	r.WriteU8(0)      // TargetLevel
	r.WriteU8(0)      // Expansion
	r.WriteU8(0)      // TargetScalingLevelDelta
	r.WriteU32(3)     // Flags: no level/item scaling
	r.WriteI32(0)     // PlayerContentTuningID
	r.WriteI32(0)     // TargetContentTuningID
	r.WriteI32(0)     // TargetHealingContentTuningID
	r.WriteFloat32(1) // PlayerPrimaryStatRatio
	r.WriteBits(0, 4) // Type
	r.FlushBits()

	out := wire.NewWriter()
	out.WriteBit(false) // no log data
	out.FlushBits()
	out.WriteU32(uint32(len(r.Bytes())))
	out.WriteBytes(r.Bytes())
	return out.Bytes()
}

// sentCores is the opcode sequence the fake saw.
func (f *fightWorld) sentCores() []uint32 {
	out := make([]uint32, 0, len(f.sent))
	for _, p := range f.sent {
		out = append(out, p.core)
	}
	return out
}

// countCore counts one opcode in what the fake saw.
func (f *fightWorld) countCore(core uint32) int {
	n := 0
	for _, p := range f.sent {
		if p.core == core {
			n++
		}
	}
	return n
}

// attackStartedDetail returns the detail of the "attack started" fight event.
func (f *fightWorld) attackStartedDetail() string {
	for _, e := range f.fight.events {
		if strings.HasPrefix(e.Detail, "attack started") {
			return e.Detail
		}
	}
	return ""
}

// TestAttackInRange: the target is 6 yards away, the client selects it, walks into the server's 5-yard reach, starts
// the swing, lands rounds and stops when the target dies. The fake's swing ticks enforce the server's reach, so a
// client that does not walk in gets errors instead of rounds and never kills.
func TestAttackInRange(t *testing.T) {
	f := newFightWorld(t, 6)
	f.killAfter = 2
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if !f.fight.killed {
		t.Fatal("the fight log does not say the target died")
	}
	if f.countCore(wire.CMSGSetSelection) != 1 {
		t.Fatalf("SET_SELECTION sent %d times", f.countCore(wire.CMSGSetSelection))
	}
	if f.countCore(wire.CMSGMoveStartForward) < 1 {
		t.Fatalf("the fighter did not walk into reach: %v", f.sentCores())
	}
	if f.countCore(wire.CMSGAttackSwing) != 1 {
		t.Fatalf("ATTACK_SWING sent %d times", f.countCore(wire.CMSGAttackSwing))
	}
	if f.legs < 1 {
		t.Fatal("no walk leg")
	}
	if f.swingErrs != 0 {
		t.Fatalf("the server refused %d swing(s) although the fighter walked in", f.swingErrs)
	}
	if f.fight.swings < 2 || f.fight.damageDealt != 2*f.damage {
		t.Fatalf("swings %d, damage dealt %d", f.fight.swings, f.fight.damageDealt)
	}
	if f.fight.damageTaken != 6 {
		t.Fatalf("damage taken %d, want the two 3-damage rounds", f.fight.damageTaken)
	}
	if f.countCore(wire.CMSGAttackStop) != 1 {
		t.Fatalf("the fighter left the server's attack state on: %v", f.sentCores())
	}
}

// TestAttackFacesTheTarget: the fighter starts with its back to the target and the fake's arc test refuses a swing
// from outside the 120-degree front arc. Only after a CMSG_MOVE_SET_FACING does a round land.
func TestAttackFacesTheTarget(t *testing.T) {
	f := newFightWorld(t, 4)
	f.killAfter = 1
	s := f.session()
	s.mover.O = objects.OrientationTo(1000, 2000, 900, 2000) // due west, the target is due east
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if !f.fight.killed {
		t.Fatalf("the target did not die; the swing errors were %d", f.swingErrs)
	}
	if f.countCore(wire.CMSGMoveSetFacing) == 0 {
		t.Fatal("the fighter never sent CMSG_MOVE_SET_FACING")
	}
}

// TestAttackFollowsAWalkingTarget: after the first round the target moves 10 yards further; the fighter must re-path
// instead of swinging at air. The fake's range check makes the old behavior visible: without the walk the swing
// ticks are errors and no round lands.
func TestAttackFollowsAWalkingTarget(t *testing.T) {
	f := newFightWorld(t, 8)
	f.walkAwayAt = 1
	f.walkAwayTo = 1025
	f.killAfter = 3
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if f.legs < 2 {
		t.Fatalf("the fighter did not walk again after the target moved (%d leg(s)): %v", f.legs, f.sentCores())
	}
	if !f.fight.killed {
		t.Fatal("the target did not die")
	}
}

// TestAttackPrintsTheDistanceAfterTheWalk: the line must carry the distance at the swing, not the one measured
// before the walk. The fighter starts 6 yards away and the walk into reach stops at 85% of the 5-yard reach.
func TestAttackPrintsTheDistanceAfterTheWalk(t *testing.T) {
	f := newFightWorld(t, 6)
	f.killAfter = 1
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	detail := f.attackStartedDetail()
	if detail == "" {
		t.Fatal("no attack-started event")
	}
	want := objects.Distance(f.swingFrom[0], f.swingFrom[1], f.swingFrom[2], f.swingTargetAt[0], f.swingTargetAt[1], f.swingTargetAt[2])
	at := fmt.Sprintf("at %.2f yd", want)
	if !strings.Contains(detail, at) {
		t.Fatalf("the printed distance %q is not the swing's %q", detail, at)
	}
	if f.swingFrom[0] == 1000 {
		t.Fatal("the fighter never walked, so the test cannot tell the two distances apart")
	}
}

// TestSwingErrorClosesIn: the server answers the swing with "not in range" even though the model says the fighter is
// in reach; the fighter must close in instead of repeating the swing.
func TestSwingErrorClosesIn(t *testing.T) {
	f := newFightWorld(t, 6)
	f.killAfter = 2
	f.forceSwingErrs = 1
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if f.fight.lastSwingErr != combat.SwingNotInRange {
		t.Fatalf("the fight log's swing error is %q", f.fight.lastSwingErr)
	}
	if f.legs < 2 {
		t.Fatalf("the swing error did not make the fighter close in (%d leg(s)): %v", f.legs, f.sentCores())
	}
	if f.swingErrs < 1 {
		t.Fatal("the fake never sent the swing error")
	}
}

// TestCastSucceeds: the cast at the target is answered with a start and a go, and Cast reports success.
func TestCastSucceeds(t *testing.T) {
	f := newFightWorld(t, 3)
	f.castSpell = 772
	f.castStartTime = 1500
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	if result != "success" {
		t.Fatalf("the cast's result is %q", result)
	}
	if !f.castSeen {
		t.Fatal("no CMSG_CAST_SPELL arrived")
	}
	if f.fight.castCount != 1 {
		t.Fatalf("the fight log counted %d casts", f.fight.castCount)
	}
}

// TestCastFailureNamed: the server refuses with SPELL_FAILED_OUT_OF_RANGE in the pre-cast form and Cast returns that
// name.
func TestCastFailureNamed(t *testing.T) {
	f := newFightWorld(t, 3)
	f.castSpell = 772
	f.castReason = 118
	f.castPreCast = true
	f.castNoStart = true
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	if result != "out of range" {
		t.Fatalf("the refusal is %q, want \"out of range\"", result)
	}
	if len(f.fight.casts) != 1 || f.fight.casts[len(f.fight.casts)-1].Result != "out of range" {
		t.Fatalf("the fight log's casts: %+v", f.fight.casts)
	}
}

// TestCastFailedWithTheServersCastID: the live 2026-11-18 capture's no-power refusal carried the server's own cast id
// (Spell.cpp:562, :4709), not the client's, and no start preceded it. It still has to end the wait: the packet is
// addressed to this client and names the spell it asked for.
func TestCastFailedWithTheServersCastID(t *testing.T) {
	f := newFightWorld(t, 3)
	f.castSpell = 772
	f.castReason = 108
	f.castPreCast = true
	f.castNoStart = true
	f.castFailID = combat.CastGUID(0, 772, 424242)
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	if result != "no power" {
		t.Fatalf("the refusal is %q, want \"no power\"", result)
	}
}

// TestCastIgnoresAnUnrelatedFailure: a failure for another spell while the cast is pending changes nothing. The old
// castFail closed the wait on any non-zero reason, so Cast reported the other spell's failure.
func TestCastIgnoresAnUnrelatedFailure(t *testing.T) {
	f := newFightWorld(t, 3)
	f.castSpell = 772
	f.castStartTime = 500
	f.castGoDelay = 1500 * time.Millisecond
	s := f.session()
	later := f.now.Add(300 * time.Millisecond)
	f.beforeWait = func(now time.Time) {
		if now.Before(later) {
			return
		}
		f.beforeWait = nil
		// SMSG_SPELL_FAILURE names the caster as the fighter and another spell.
		f.server(wire.SMSGSpellFailure, buildSpellFailure(f.self, combat.CastGUID(0, 445, 7), 445, 118))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	if result != "success" {
		t.Fatalf("an unrelated failure closed the cast: %q", result)
	}
}

// TestCastRepeatedStartsDoNotExtendTheWait: a start the client did not ask for — another spell's — must not move the
// deadline. The old castStart took every start naming the fighter and Cast extended its deadline to any cast time,
// so a hostile start parked the wait.
func TestCastRepeatedStartsDoNotExtendTheWait(t *testing.T) {
	f := newFightWorld(t, 3)
	f.castSpell = 772
	f.castNoAnswer = true
	s := f.session()
	sent := false
	f.beforeWait = func(now time.Time) {
		if sent {
			return
		}
		sent = true
		f.beforeWait = nil
		// Another spell's start naming a four-billion-millisecond cast time.
		f.server(wire.SMSGSpellStart, buildSpellStart(f.self, f.self, combat.CastGUID(0, 445, 1), 445, 4000000000, f.castTarget()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5})
	if err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("Cast returned %v, want the no-answer timeout", err)
	}
	if elapsed := f.now.Sub(f.castSentAt); elapsed < castWaitBase || elapsed > castWaitBase+time.Second {
		t.Fatalf("Cast waited %v, want about %v", elapsed, castWaitBase)
	}
}

// TestCastClampsAHostileStart: a matching start with a four-billion-millisecond cast time is clamped to maxCastTime,
// and the absolute bound of one Cast cannot be moved past castWaitMax.
func TestCastClampsAHostileStart(t *testing.T) {
	f := newFightWorld(t, 3)
	f.castSpell = 772
	f.castStartTime = 4000000000
	f.castNoAnswer = true
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5})
	if err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("Cast returned %v, want the no-answer timeout", err)
	}
	// 30 s clamp + the 2 s answer window, inside the 38 s absolute bound (which only a start late in the base
	// window could reach).
	elapsed := f.now.Sub(f.castSentAt)
	if elapsed < maxCastTime || elapsed > maxCastTime+3*time.Second {
		t.Fatalf("Cast waited %v, want about %v", elapsed, maxCastTime+2*time.Second)
	}
	if elapsed > castWaitMax {
		t.Fatalf("Cast waited %v, past the absolute %v bound", elapsed, castWaitMax)
	}
}

// TestCastSecondStartDoesNotExtend: the first matching start sets the deadline; a later start for the same spell
// with another cast id is a different cast and is ignored. The old castStart took it and extended the wait.
func TestCastSecondStartDoesNotExtend(t *testing.T) {
	f := newFightWorld(t, 3)
	f.castSpell = 772
	f.castStartTime = 10000 // past the 6 s base, so the deadline is visibly the start's
	f.castNoAnswer = true
	s := f.session()
	second := f.now.Add(500 * time.Millisecond)
	f.beforeWait = func(now time.Time) {
		if now.Before(second) {
			return
		}
		f.beforeWait = nil
		f.server(wire.SMSGSpellStart, buildSpellStart(f.self, f.self, combat.CastGUID(0, 772, 5), 772, 4000000000, f.castTarget()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5})
	if err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("Cast returned %v, want the no-answer timeout", err)
	}
	// 10 s cast + 2 s answer window, not the second start's 4e9 ms.
	if elapsed := f.now.Sub(f.castSentAt); elapsed < 11*time.Second || elapsed > 13*time.Second {
		t.Fatalf("Cast waited %v after the second start", elapsed)
	}
}

// TestCastMatchingStartExtendsTheWait: the cast's own start with a real cast time extends the wait past the base.
func TestCastMatchingStartExtendsTheWait(t *testing.T) {
	f := newFightWorld(t, 3)
	f.castSpell = 772
	f.castStartTime = 8000 // past the 6 s base, inside the 30 s clamp
	f.castGoDelay = 8200 * time.Millisecond
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5})
	if err != nil || result != "success" {
		t.Fatalf("Cast: %q, %v", result, err)
	}
}

// TestCastRefusesOutsideTheWindow: the caller's window is checked before anything is sent.
func TestCastRefusesOutsideTheWindow(t *testing.T) {
	f := newFightWorld(t, 30)
	s := f.session()
	if _, err := s.Cast(context.Background(), 772, f.target, RangeWindow{0, 5}); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("Cast at 30 yd with a 5-yard window: %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("the refused cast still sent %d packet(s)", len(f.sent))
	}
	if _, err := s.Cast(context.Background(), 100, f.target, RangeWindow{8, 25}); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("Cast at 30 yd with an 8-25 yard window: %v", err)
	}
}

// TestCastTooClose: a cast with a minimum range at 3 yards is "too close".
func TestCastTooClose(t *testing.T) {
	f := newFightWorld(t, 3)
	s := f.session()
	result, err := s.Cast(context.Background(), 100, f.target, RangeWindow{8, 25})
	if !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("Cast at 3 yd with an 8-yard minimum: %v", err)
	}
	if result != "too close" {
		t.Fatalf("the outcome is %q", result)
	}
}

// TestAttackRefusesUnusableTargetsBeforeSending: a GUID the model does not hold, a game object and a corpse are
// refused before CMSG_SET_SELECTION, so nothing is sent for them.
func TestAttackRefusesUnusableTargetsBeforeSending(t *testing.T) {
	f := newFightWorld(t, 4)
	s := f.session()

	if err := s.Attack(context.Background(), wire.GUID{Low: 0x7777, High: 0x2001180000000000}); !errors.Is(err, ErrTargetGone) {
		t.Fatalf("a stale GUID: %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("a stale GUID still sent %v", f.sentCores())
	}

	goGUID := wire.GUID{Low: 0x8888, High: 0x2801180000000000}
	f.world.Apply(&objects.Update{Blocks: []objects.WorldFragment{{GUID: goGUID, ObjectType: objects.TypeGameObject,
		ValuesComplete: true, Info: objects.Info{HasObject: true, HasGameObject: true}}}}, f.now)
	if err := s.Attack(context.Background(), goGUID); !errors.Is(err, ErrNotAUnit) {
		t.Fatalf("a game object: %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("a game object still got %v", f.sentCores())
	}

	// A corpse: the target's health goes to zero.
	o, _ := f.world.Object(f.target)
	u, err := objects.ParseUpdateObject(patchHealth(t, f.targetCreate, o.Info.Health))
	if err != nil {
		t.Fatalf("the corpse's create: %v", err)
	}
	f.world.Apply(u, f.now)
	if err := s.Attack(context.Background(), f.target); !errors.Is(err, ErrTargetGone) {
		t.Fatalf("a corpse: %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("a corpse still got %v", f.sentCores())
	}
}

// TestUnattackableFlags: the unit flags IsValidAttackTarget refuses on are read from the model's unit fields
// (Object.cpp:2429-2443); a selection is never sent for such a unit.
func TestUnattackableFlags(t *testing.T) {
	for _, tc := range []struct {
		flags uint32
		want  bool
	}{
		{0, false},
		{unitFlagNonAttackable, true},
		{unitFlagNotAttackable, true},
		{unitFlagNonAttackable2, true},
		{0x00080000, false}, // UNIT_FLAG_IN_COMBAT is not a refusal
	} {
		o := &objects.Object{Type: objects.TypeUnit, Info: objects.Info{HasUnit: true, Flags: tc.flags}}
		if got := unattackable(o); got != tc.want {
			t.Fatalf("flags %#x: unattackable %v, want %v", tc.flags, got, tc.want)
		}
	}
}

// TestCastRefusesANonUnit: a spell at a game object is refused before CMSG_CAST_SPELL.
func TestCastRefusesANonUnit(t *testing.T) {
	f := newFightWorld(t, 3)
	s := f.session()
	goGUID := wire.GUID{Low: 0x9999, High: 0x2801180000000000}
	f.world.Apply(&objects.Update{Blocks: []objects.WorldFragment{{GUID: goGUID, ObjectType: objects.TypeGameObject,
		ValuesComplete: true, Info: objects.Info{HasObject: true, HasGameObject: true}}}}, f.now)
	if _, err := s.Cast(context.Background(), 772, goGUID, RangeWindow{}); !errors.Is(err, ErrNotAUnit) {
		t.Fatalf("a cast at a game object: %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("the refused cast still sent %v", f.sentCores())
	}
}

// TestAttackStopsOnEveryExit: whatever ends the fight after the swing was sent, the client ends the server's attack
// state with CMSG_ATTACK_STOP — and only once, and not when the server already ended it.
func TestAttackStopsOnEveryExit(t *testing.T) {
	tests := []struct {
		name    string
		run     func(t *testing.T, f *fightWorld, s *session, ctx context.Context, cancel context.CancelFunc) error
		wantErr string
		stops   int
	}{
		{
			name: "context cancelled",
			run: func(t *testing.T, f *fightWorld, s *session, ctx context.Context, cancel context.CancelFunc) error {
				return s.Attack(ctx, f.target, func(ctx context.Context, s *session) error {
					cancel()
					return nil
				})
			},
			wantErr: context.Canceled.Error(),
			stops:   1,
		},
		{
			name: "hook failure",
			run: func(t *testing.T, f *fightWorld, s *session, ctx context.Context, cancel context.CancelFunc) error {
				return s.Attack(ctx, f.target, func(ctx context.Context, s *session) error {
					return errors.New("hook")
				})
			},
			wantErr: "hook",
			stops:   1,
		},
		{
			name: "wait failure",
			run: func(t *testing.T, f *fightWorld, s *session, ctx context.Context, cancel context.CancelFunc) error {
				f.failWaitAfter = 2
				return s.Attack(ctx, f.target)
			},
			wantErr: "connection dropped",
			stops:   1,
		},
		{
			name: "movement failure after the swing",
			run: func(t *testing.T, f *fightWorld, s *session, ctx context.Context, cancel context.CancelFunc) error {
				f.walkAwayAt = 1
				f.walkAwayTo = 1020
				f.failMoveAfter = 1
				return s.Attack(ctx, f.target)
			},
			wantErr: "walk leg",
			stops:   1,
		},
		{
			name: "deadline",
			run: func(t *testing.T, f *fightWorld, s *session, ctx context.Context, cancel context.CancelFunc) error {
				f.opts.FightFor = 300 * time.Millisecond
				return s.Attack(ctx, f.target)
			},
			wantErr: "of fighting",
			stops:   1,
		},
		{
			name: "the server refuses the target",
			run: func(t *testing.T, f *fightWorld, s *session, ctx context.Context, cancel context.CancelFunc) error {
				f.refuseAttack = true
				return s.Attack(ctx, f.target)
			},
			wantErr: ErrCannotAttack.Error(),
			stops:   0, // the server already ended it
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFightWorld(t, 4)
			s := f.session()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := tc.run(t, f, s, ctx, cancel)
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q, want %q", err, tc.wantErr)
			}
			if got := f.countCore(wire.CMSGAttackStop); got != tc.stops {
				t.Fatalf("CMSG_ATTACK_STOP sent %d times, want %d (%v)", got, tc.stops, f.sentCores())
			}
		})
	}
}

// TestAttackServerStopWithALivingTargetIsAnEvade: a stop after contact with the target alive is ErrTargetEvaded
// carrying its health, not a finished fight. The old code returned nil.
func TestAttackServerStopWithALivingTargetIsAnEvade(t *testing.T) {
	f := newFightWorld(t, 4)
	f.stopAfterContact = true
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.Attack(ctx, f.target)
	if !errors.Is(err, ErrTargetEvaded) {
		t.Fatalf("Attack: %v, want ErrTargetEvaded", err)
	}
	if !strings.Contains(err.Error(), "target alive at 12191 health") {
		t.Fatalf("the error does not carry the health: %q", err)
	}
	if f.fight.killed {
		t.Fatal("the log says the target died")
	}
}

// TestAttackServerStopAndThenTheDeath: the stop can precede the victim's death values by a tick; the grace period
// sees the death and the fight is a success.
func TestAttackServerStopAndThenTheDeath(t *testing.T) {
	f := newFightWorld(t, 4)
	f.stopAfterContact = true
	f.stopThenDie = true
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if !f.fight.killed {
		t.Fatal("the death values did not make the fight a success")
	}
}

// TestFightPlanPicksFromTheKnownList: the plan uses only spells the character has.
func TestFightPlanPicksFromTheKnownList(t *testing.T) {
	plan := planSpells([]int32{6603, 6673})
	if plan.Hostile != 0 {
		t.Fatalf("the plan picked hostile spell %d from a list without one", plan.Hostile)
	}
	if plan.Self != 6673 {
		t.Fatalf("the plan picked self spell %d, want Battle Shout", plan.Self)
	}
	plan = planSpells([]int32{6603, 100, 772, 6673})
	if plan.Hostile != 772 {
		t.Fatalf("the plan picked %d, want Rend (before Charge)", plan.Hostile)
	}
	if plan.HostileWindow.Max != 5 {
		t.Fatalf("Rend's window %+v", plan.HostileWindow)
	}
	plan = planSpells([]int32{100})
	if plan.Hostile != 100 || plan.HostileWindow.Min != 8 || plan.HostileWindow.Max != 25 {
		t.Fatalf("Charge's plan %+v", plan)
	}
	plan = planSpells(nil)
	if plan.Hostile != 0 || plan.Self != 0 {
		t.Fatalf("an empty list planned %+v", plan)
	}
}

// TestChooseTargetSkipsCrittersAndNpcs: the default target choice.
func TestChooseTargetSkipsCrittersAndNpcs(t *testing.T) {
	self := wire.GUID{Low: 1, High: 0x0801180000000000}
	unit := func(guid wire.GUID, entry, level int32, npcFlags uint32, creatureType uint8, x float32) *objects.Object {
		return &objects.Object{GUID: guid, Type: objects.TypeUnit, X: x, Y: 2000, Z: 50,
			Info: objects.Info{HasUnit: true, Entry: entry, Level: level, NpcFlags: npcFlags,
				CreatureType: creatureType, Health: 42, MaxHealth: 42}}
	}
	units := []*objects.Object{
		unit(self, 0, 1, 0, 0, 1000),                                           // the fighter itself is not a candidate
		unit(wire.GUID{Low: 2, High: 0x2001180000000000}, 721, 1, 0, 8, 1003),  // a critter
		unit(wire.GUID{Low: 3, High: 0x2001180000000000}, 823, 1, 1, 7, 1004),  // an NPC with flags
		unit(wire.GUID{Low: 4, High: 0x2001180000000000}, 300, 12, 0, 1, 1005), // too high a level
		unit(wire.GUID{Low: 5, High: 0x2001180000000000}, 299, 2, 0, 1, 1010),  // the wolf
		unit(wire.GUID{Low: 6, High: 0x2001180000000000}, 299, 2, 0, 1, 1100),  // beyond 40 yd
	}
	got := attackableCandidates(units, 1000, 2000, 50, self, 0, targetSearchRange, 1, 3)
	if len(got) != 1 || got[0].GUID.Low != 5 {
		t.Fatalf("candidates %v, want the wolf", got)
	}
	got = attackableCandidates(units, 1000, 2000, 50, self, 721, targetSearchRange, 1, 3)
	if len(got) != 1 || got[0].GUID.Low != 2 {
		t.Fatalf("--entry 721 chose %v", got)
	}
	if got := attackableCandidates(units, 1000, 2000, 50, self, 999, targetSearchRange, 1, 3); len(got) != 0 {
		t.Fatalf("--entry 999 matched %v", got)
	}
	// A dead unit is not a target.
	units[4].Dead = true
	if got := attackableCandidates(units, 1000, 2000, 50, self, 0, targetSearchRange, 1, 3); len(got) != 0 {
		t.Fatalf("a dead wolf was a candidate: %v", got)
	}
	// The band is a parameter: the level-12 unit is in it, the level-2 wolf below it.
	units[4].Dead = false
	if got := attackableCandidates(units, 1000, 2000, 50, self, 0, targetSearchRange, 10, 20); len(got) != 1 || got[0].Info.Level != 12 {
		t.Fatalf("band 10-20 chose %v, want the level-12 unit", got)
	}
}

// TestFightLevelWindow states the default band: the character's own level -3 to +2, clamped to the client's 1-60, so
// a level-1 character keeps the round-4 brief's 1-3; an explicit bound wins over that end of the band. A single
// explicit bound past the automatic other end widens the automatic end, so the band is never empty: an empty band
// made the run search for nothing and only say so after the fact.
func TestFightLevelWindow(t *testing.T) {
	cases := []struct {
		name             string
		min, max, self   int32
		wantMin, wantMax int32
	}{
		{"level 30", 0, 0, 30, 27, 32},
		{"level 1", 0, 0, 1, 1, 3},
		{"level 3", 0, 0, 3, 1, 5},
		{"level 60", 0, 0, 60, 57, 60},
		{"level unknown", 0, 0, 0, 1, 3},
		{"explicit both", 10, 20, 30, 10, 20},
		{"explicit low", 10, 0, 30, 10, 32},
		{"explicit high", 0, 28, 30, 27, 28},
		{"min above the automatic max", 40, 0, 1, 40, 40},
		{"max below the automatic min", 0, 1, 30, 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotMin, gotMax := fightLevelWindow(c.min, c.max, c.self)
			if gotMin != c.wantMin || gotMax != c.wantMax {
				t.Fatalf("band %d-%d, want %d-%d", gotMin, gotMax, c.wantMin, c.wantMax)
			}
		})
	}
}

// TestSpellListTextHasNoIdentifiers: the printed list is counts and ids only.
func TestSpellListText(t *testing.T) {
	got := spellListText([]int32{6603, 6673, 100})
	if got != " (3: 6603 6673 100)" {
		t.Fatalf("spellListText = %q", got)
	}
	if spellListText(nil) != "" {
		t.Fatal("an empty list printed text")
	}
	if strings.Contains(spellListText([]int32{1}), "0x") {
		t.Fatal("the spell list looks like an identifier")
	}
}

// TestEngagedNeedsARound: a swing the server refused is not an engagement; a landed round is. The old report counted
// the sent swing (mockSwung) as engaged.
func TestEngagedNeedsARound(t *testing.T) {
	f := newFightWorld(t, 4)
	f.refuseAttack = true
	s := f.session()
	if err := s.Attack(context.Background(), f.target); !errors.Is(err, ErrCannotAttack) {
		t.Fatalf("Attack: %v", err)
	}
	s.fillFightReport()
	if s.report.Fight.Engaged {
		t.Fatal("a refused swing counted as engaged")
	}

	g := newFightWorld(t, 4)
	g.killAfter = 1
	sg := g.session()
	if err := sg.Attack(context.Background(), g.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	sg.fillFightReport()
	if !sg.report.Fight.Engaged {
		t.Fatal("a landed round did not count as engaged")
	}
}

// TestRestIgnoresAnAttackerTheModelCannotSee: ApplyAttackStart records whatever GUID the server named; rest must not
// hand Attack an attacker that is not a live unit in the model (the old code returned it and Attack sent a selection
// for a GUID the model does not hold).
func TestRestIgnoresAnAttackerTheModelCannotSee(t *testing.T) {
	f := newFightWorld(t, 4)
	s := f.session()
	// Hurt: the fighter's own create again with its health byte-patched below half (the model merges only fields a
	// parsed block carried, so a hand-set Info.Health would be ignored).
	o, _ := f.world.Object(f.self)
	old := make([]byte, 8)
	binary.LittleEndian.PutUint64(old, uint64(o.Info.Health))
	hurt := make([]byte, 8)
	binary.LittleEndian.PutUint64(hurt, uint64(o.Info.Health/4))
	i := bytes.Index(f.selfCreate, old)
	if i < 0 {
		t.Fatalf("the self create has no health %d", o.Info.Health)
	}
	patched := append([]byte(nil), f.selfCreate...)
	copy(patched[i:i+8], hurt)
	u, err := objects.ParseUpdateObject(patched)
	if err != nil {
		t.Fatalf("the hurt self create: %v", err)
	}
	f.world.Apply(u, f.now)
	unknown := wire.GUID{Low: 0xBEEF, High: 0x2001180000000000}
	f.server(wire.SMSGAttackStart, buildAttackStart(unknown, f.self))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if got := s.rest(ctx, 0.5, 200*time.Millisecond); !got.IsZero() {
		t.Fatalf("rest returned the unseen attacker %v", got)
	}
}

// TestCastClearsPendingStateOnTimeout: a start that arrives after Cast gave up changes nothing, and the next Cast
// works. The old code left the pending state open, so a late start moved a deadline no one was waiting on.
func TestCastClearsPendingStateOnTimeout(t *testing.T) {
	f := newFightWorld(t, 3)
	f.castSpell = 772
	f.castNoAnswer = true
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5}); err == nil {
		t.Fatal("the parked cast returned no error")
	}
	f.server(wire.SMSGSpellStart, buildSpellStart(f.self, f.self, combat.CastGUID(0, 772, 77), 772, 4000, f.castTarget()))
	if !f.fight.castEnds.IsZero() || f.fight.castOpen || f.fight.castStarted {
		t.Fatalf("a late start reopened the cast: ends %v open %v started %v", f.fight.castEnds, f.fight.castOpen, f.fight.castStarted)
	}
	f.castNoAnswer = false
	if result, err := s.Cast(ctx, 772, f.target, RangeWindow{0, 5}); err != nil || result != "success" {
		t.Fatalf("the next cast: %q, %v", result, err)
	}
}

// TestAttackRecoversFromATeleportDuringTheWalk: the server moving the character mid-approach ends the leg; the fight
// re-approaches and still kills (the teleport is not a spin that burns the fight's deadline silently).
func TestAttackRecoversFromATeleportDuringTheWalk(t *testing.T) {
	f := newFightWorld(t, 20)
	f.killAfter = 1
	s := f.session()
	once := true
	f.beforeWait = func(now time.Time) {
		if !once {
			return
		}
		once = false
		s.mover.SetPosition(1000, 2000, 50, 0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if !f.fight.killed {
		t.Fatal("the fighter did not recover from the teleport")
	}
}

// setSelfClass rewrites the fighter's create with another class: the class byte is found by trying every byte that
// holds the fixture's class and keeping the one whose patched parse reports the new class (the create carries dozens
// of field values, and only one of them is the class). This is a hand-built unit state; no fixture has a rage class.
func (f *fightWorld) setSelfClass(t *testing.T, class uint8) {
	t.Helper()
	o, ok := f.world.Object(f.self)
	if !ok {
		t.Fatal("the fighter is not in the model")
	}
	old := o.Info.Class
	for i := range f.selfCreate {
		if f.selfCreate[i] != old {
			continue
		}
		patched := append([]byte(nil), f.selfCreate...)
		patched[i] = class
		u, err := objects.ParseUpdateObject(patched)
		if err != nil || len(u.Blocks) != 1 || u.Blocks[0].Info.Class != class {
			continue
		}
		f.selfCreate = patched
		f.world.Apply(u, f.now)
		return
	}
	t.Fatalf("the class %d field is not in the create", old)
}

// setSelfHealth writes the fighter's own create again with its health patched: the model merges only the fields a
// parsed block carried, so a hand-set Info.Health would be ignored.
func (f *fightWorld) setSelfHealth(t *testing.T, health int64) {
	t.Helper()
	patched := append([]byte(nil), f.selfCreate...)
	binary.LittleEndian.PutUint64(patched[f.selfHealthAt:f.selfHealthAt+8], uint64(health))
	u, err := objects.ParseUpdateObject(patched)
	if err != nil {
		t.Fatalf("the patched self create: %v", err)
	}
	f.world.Apply(u, f.now)
}

// commandSession is the session forever fight-check runs in against the fake: no settle wait (so collect returns at
// once), the report position at the mover, and the names the model already holds (queryNames then sends nothing the
// fake has no answer for). opts.Entry names the fixture's creature, so the level band does not filter it out.
func (f *fightWorld) commandSession(t *testing.T) *session {
	t.Helper()
	f.opts.Settle = time.Nanosecond
	f.opts.Entry = 1
	s := f.session()
	s.report.WalkClientPos = [4]float32{s.mover.X, s.mover.Y, s.mover.Z, s.mover.O}
	f.world.SetName(f.self, "the fighter")
	if o, ok := f.world.Object(f.target); ok {
		f.world.SetEntryName(o.Info.Entry, "a creature")
	}
	return s
}

// TestSwingErrorStepsPastTheStopDistance: the server's reach is shorter than the client's own formula, so every swing
// tick until the fighter is within two yards is refused. The forced approach must ask the mover for a real step, not
// a point inside its own stop distance, or the fighter repeats the refused swing until the fight deadline. The old
// 60%-of-reach point sat right where the fighter already stood (approachPoint returned its own point) and the fight
// never reached the server's reach.
func TestSwingErrorStepsPastTheStopDistance(t *testing.T) {
	f := newFightWorld(t, 6)
	f.killAfter = 1
	f.refuseCloserThan = 2
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if !f.fight.killed {
		t.Fatalf("the target did not die: %d refused swing(s), %d leg(s)", f.swingErrs, f.legs)
	}
	if f.swingErrs == 0 {
		t.Fatal("the fake never refused a swing, so the forced approach was not exercised")
	}
	if f.legs < 2 {
		t.Fatalf("the refused swings did not move the fighter (%d leg(s)): %v", f.legs, f.sentCores())
	}
	target, _ := f.world.Object(f.target)
	tx, ty, tz := target.Position(f.now)
	mx, my, mz := f.moverPos()
	if d := objects.Distance(mx, my, mz, tx, ty, tz); d > f.refuseCloserThan {
		t.Fatalf("the fighter stopped at %.2f yd, outside the server's %.2f", d, f.refuseCloserThan)
	}
}

// TestSwingErrorLimitIsBounded: a server that refuses every tick must not keep the fight spinning. The forced
// approaches are capped and Attack returns ErrStillOutOfRange, so the command can move on to the next candidate.
func TestSwingErrorLimitIsBounded(t *testing.T) {
	f := newFightWorld(t, 6)
	f.forceSwingErrs = 1000
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.Attack(ctx, f.target)
	if !errors.Is(err, ErrStillOutOfRange) {
		t.Fatalf("Attack: %v, want ErrStillOutOfRange", err)
	}
	// The fake ticks on the server's 100 ms retry while the fighter walks, so a forced approach can see two answers;
	// what must be bounded is the approaches, not the fake's swing timer.
	if f.swingErrs > 3*maxForcedApproaches {
		t.Fatalf("the fake answered %d swing(s): the forced approaches are not bounded", f.swingErrs)
	}
	// One leg for the first approach and at most one per forced approach.
	if f.legs > maxForcedApproaches+1 {
		t.Fatalf("%d walk leg(s): the forced approaches are not bounded", f.legs)
	}
	if f.countCore(wire.CMSGAttackStop) != 1 {
		t.Fatalf("the fight did not end the server's attack state: %v", f.sentCores())
	}
}

// TestThirdPartyRoundIsNotContact: another unit hits the character before the server answers the swing with the
// attack-stop of an unattackable target. That round says nothing about the target, so the stop must still be the
// server refusing the target (ErrCannotAttack), not a fight that ended in contact.
func TestThirdPartyRoundIsNotContact(t *testing.T) {
	f := newFightWorld(t, 4)
	f.refuseAttack = true
	f.thirdParty = wire.GUID{Low: 0x5151, High: 0x2001180000000000}
	f.thirdPartyDamage = 5
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.Attack(ctx, f.target)
	if !errors.Is(err, ErrCannotAttack) {
		t.Fatalf("Attack: %v, want ErrCannotAttack", err)
	}
	if f.fight.contact != 0 {
		t.Fatalf("a third-party round counted as contact (%d)", f.fight.contact)
	}
	if f.fight.attackedByOthers != 1 || f.fight.damageFromOthers != 5 {
		t.Fatalf("attacked by others %d for %d, want 1 for 5", f.fight.attackedByOthers, f.fight.damageFromOthers)
	}
	found := false
	for _, e := range f.fight.events {
		if e.Detail == "attacked by another unit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the fight log does not name the other unit: %+v", f.fight.events)
	}
}

// TestFightCheckStopsWhenTheCharacterDies: the character dies in the fight; the command returns ErrSelfDead at once
// and sends nothing more for the corpse — no retreat walk, no rest, no further selection. The old code filled the
// report, retreated and rested at zero health.
func TestFightCheckStopsWhenTheCharacterDies(t *testing.T) {
	f := newFightWorld(t, 4)
	f.killAfter = 2
	s := f.commandSession(t)
	f.beforeWait = func(now time.Time) {
		if f.rounds == 0 {
			return
		}
		f.beforeWait = nil
		f.setSelfHealth(t, 0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.fightCheck(ctx)
	if !errors.Is(err, ErrSelfDead) {
		t.Fatalf("fightCheck: %v, want ErrSelfDead", err)
	}
	if f.countCore(wire.CMSGSetSelection) != 1 {
		t.Fatalf("SET_SELECTION sent %d times after the death", f.countCore(wire.CMSGSetSelection))
	}
	if f.countCore(wire.CMSGMoveStartForward) != 0 {
		t.Fatalf("the command walked the corpse: %v", f.sentCores())
	}
	if s.report.Fight.Killed {
		t.Fatal("the report says the target died")
	}
}

// TestFightCheckDefensiveDeath: a hurt character resting with a unit on it fights back, and dying in that fight stops
// the command at once — no second rest cycle, no target pick while dead.
func TestFightCheckDefensiveDeath(t *testing.T) {
	f := newFightWorld(t, 30) // farther than retreat's 20-yard threat scan, so the rest runs where it stands
	s := f.commandSession(t)
	f.setSelfHealth(t, 24) // under half of the fixture's 97
	waits := 0
	f.beforeWait = func(now time.Time) {
		waits++
		switch waits {
		case 1: // the unit on the character: rest hands it to the defensive fight
			f.server(wire.SMSGAttackStart, buildAttackStart(f.target, f.self))
		case 2: // and it kills the character during the walk back
			f.setSelfHealth(t, 0)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := s.fightCheck(ctx)
	if !errors.Is(err, ErrSelfDead) {
		t.Fatalf("fightCheck: %v, want ErrSelfDead", err)
	}
	if f.countCore(wire.CMSGAttackSwing) != 0 {
		t.Fatalf("a swing was sent for the dead character: %v", f.sentCores())
	}
	if f.countCore(wire.CMSGMoveStartForward) != 1 {
		t.Fatalf("the command kept walking after the death: %v", f.sentCores())
	}
}

// TestFightCheckOutcomes: the command's three exits against the fake — a kill is success, a server stop with the
// target alive is its own error, and a refused target is not a fight at all.
func TestFightCheckOutcomes(t *testing.T) {
	t.Run("the kill", func(t *testing.T) {
		f := newFightWorld(t, 8)
		f.killAfter = 1
		s := f.commandSession(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.fightCheck(ctx); err != nil {
			t.Fatalf("fightCheck: %v", err)
		}
		if !s.report.Fight.Killed || !s.report.Fight.Engaged {
			t.Fatalf("the report: killed %v engaged %v", s.report.Fight.Killed, s.report.Fight.Engaged)
		}
	})
	t.Run("the evade", func(t *testing.T) {
		f := newFightWorld(t, 4)
		f.stopAfterContact = true
		s := f.commandSession(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := s.fightCheck(ctx)
		if !errors.Is(err, ErrTargetEvaded) {
			t.Fatalf("fightCheck: %v, want ErrTargetEvaded", err)
		}
		if !s.report.Fight.Evaded {
			t.Fatal("the report does not say the target got away")
		}
	})
	t.Run("a refused target", func(t *testing.T) {
		f := newFightWorld(t, 4)
		f.refuseAttack = true
		s := f.commandSession(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.fightCheck(ctx); err != nil {
			t.Fatalf("fightCheck: %v, want no error for a refused target", err)
		}
		if s.report.Fight.Killed || s.report.Fight.Engaged {
			t.Fatalf("the report: killed %v engaged %v", s.report.Fight.Killed, s.report.Fight.Engaged)
		}
	})
}

// TestFightCheckChoosesFromTheMoversPosition: the candidate list is built from where the mover is after the settle,
// not from the login position. The target is 50 yards from the mover and 250 from the report's position, past the
// walk's 200-yard fallback: only re-reading the mover finds it.
func TestFightCheckChoosesFromTheMoversPosition(t *testing.T) {
	f := newFightWorld(t, 250)
	f.killAfter = 1
	s := f.commandSession(t)
	s.mover.X = 1200 // 50 yd west of the target, 300 yd from the report's position
	f.opts.Entry = 1
	s.report.WalkClientPos = [4]float32{1000, 2000, 50, 0}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.fightCheck(ctx); err != nil {
		t.Fatalf("fightCheck: %v", err)
	}
	if !s.report.Fight.Killed {
		t.Fatalf("the target the mover can see was not fought: %+v", s.report.Fight.Events)
	}
}

// TestRetreatWalksAwayFromTheThreat: below half health the command walks 25 yards off the unit on it; at full health
// it does not move at all.
func TestRetreatWalksAwayFromTheThreat(t *testing.T) {
	f := newFightWorld(t, 6)
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.retreat(ctx)
	if f.countCore(wire.CMSGMoveStartForward) != 0 {
		t.Fatalf("a healthy character retreated: %v", f.sentCores())
	}
	f.setSelfHealth(t, 24)
	beforeX, _, _ := f.moverPos()
	s.retreat(ctx)
	if f.countCore(wire.CMSGMoveStartForward) != 1 {
		t.Fatalf("the hurt character did not retreat: %v", f.sentCores())
	}
	x, y, _ := f.moverPos()
	if x >= beforeX {
		t.Fatalf("the retreat did not move west away from the target: %.2f -> %.2f", beforeX, x)
	}
	target, _ := f.world.Object(f.target)
	tx, ty, tz := target.Position(f.now)
	if d := objects.Distance(x, y, 50, tx, ty, tz); d < 20 {
		t.Fatalf("the retreat ended %.1f yd from the threat, want about 25 more", d)
	}
}

// TestCastBetweenSwingsCastsAndStops: the hook casts the plan's hostile spell once the rage is there, and a success
// spends the plan (it is not a rotation); the self spell is cast the same way, at no target.
func TestCastBetweenSwingsCastsAndStops(t *testing.T) {
	f := newFightWorld(t, 4)
	f.castStartTime = 500
	s := f.session()
	// The fixture's player is a mage, whose class layout has no rage slot, and the plan's spells cost rage: the
	// create's class byte is set to the warrior's and the bar is filled the way the server fills it.
	f.setSelfClass(t, 1) // warrior: rage in slot 0
	f.world.ApplyPowerUpdate(&combat.PowerUpdate{GUID: f.self, Powers: []combat.PowerValue{{Type: ragePowerType, Value: 100}}}, f.now)
	plan := spellPlan{Hostile: 772, HostileWindow: RangeWindow{0, 5}, Self: 6673}
	hook := s.castBetweenSwings(&plan, f.target)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The hook's first window opens at its own construction; in a fight the first call is a swing tick later.
	link := s.transport()
	if err := link.WaitUntil(ctx, link.Now().Add(time.Millisecond)); err != nil {
		t.Fatalf("advancing past the first window: %v", err)
	}
	if err := hook(ctx, s); err != nil {
		t.Fatalf("the hook: %v", err)
	}
	// Both spells are due on this tick: the hostile one at the target and the self one with no target at all.
	if f.countCore(wire.CMSGCastSpell) != 2 {
		t.Fatalf("CMSG_CAST_SPELL sent %d times, want the two spells once each", f.countCore(wire.CMSGCastSpell))
	}
	if plan.Hostile != 0 || plan.Self != 0 {
		t.Fatalf("the landed casts left the plan at %d/%d", plan.Hostile, plan.Self)
	}
	var sawTarget, sawSelf bool
	for _, p := range f.sent {
		if p.core != wire.CMSGCastSpell {
			continue
		}
		req := readClientCast(t, p.payload)
		if req.Target.Flags == 0 && req.Target.Unit.IsZero() {
			sawSelf = true
		}
		if req.Target.Flags == combat.TargetFlagUnit && req.Target.Unit == f.target {
			sawTarget = true
		}
	}
	if !sawTarget || !sawSelf {
		t.Fatalf("the requests: unit target %v, self %v", sawTarget, sawSelf)
	}
	// Both are spent: a later tick sends nothing.
	if err := link.WaitUntil(ctx, link.Now().Add(2100*time.Millisecond)); err != nil {
		t.Fatalf("waiting out the retry window: %v", err)
	}
	if err := hook(ctx, s); err != nil {
		t.Fatalf("the hook: %v", err)
	}
	if got := f.countCore(wire.CMSGCastSpell); got != 2 {
		t.Fatalf("the spent plan cast again: %d request(s)", got)
	}
}

// TestChaseIsOneLegWithABoundedPacketRate: a target that wanders while the fighter closes in is followed by ONE
// continuous leg: one CMSG_MOVE_START_FORWARD, heartbeats every 500 ms, at most one re-aim per tick (≤4 movement
// packets per second, two per 500 ms tick), and one CMSG_MOVE_STOP on arrival. The old code sent a start/stop pair
// per position sample (13 pairs, 76-262 ms apart, on the live failure of 2026-11-18).
func TestChaseIsOneLegWithABoundedPacketRate(t *testing.T) {
	f := newFightWorld(t, 30)
	f.killAfter = 1
	s := f.session()
	// The target wanders: 2 yards sideways, then back, every 250 ms while the fighter approaches.
	o, _ := f.world.Object(f.target)
	baseX, baseY, _ := o.Position(f.now)
	move := 0
	f.beforeWait = func(now time.Time) {
		move++
		if move%3 != 1 || move > 30 {
			return
		}
		// Each update patches the fixture from its own position, so the create is the same one every time.
		nx, ny := baseX, baseY+2
		if move/6%2 == 1 {
			ny = baseY - 2
		}
		payload := patchFloats(t, f.targetCreate, baseX, nx)
		payload = patchFloats(t, payload, baseY, ny)
		u, err := objects.ParseUpdateObject(payload)
		if err != nil {
			t.Fatalf("the moving target: %v", err)
		}
		f.world.Apply(u, now)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if !f.fight.killed {
		t.Fatal("the target did not die")
	}
	if got := f.countCore(wire.CMSGMoveStartForward); got != 1 {
		t.Fatalf("%d START_FORWARD for one approach, want 1: %v", got, f.sentCores())
	}
	if got := f.countCore(wire.CMSGMoveStop); got != 1 {
		t.Fatalf("%d CMSG_MOVE_STOP for one approach, want 1: %v", got, f.sentCores())
	}
	if got := f.countCore(wire.CMSGMoveHeartbeat); got < 2 {
		t.Fatalf("%d heartbeat(s) for a 30-yard approach", got)
	}
	// The cap: no one-second window holds more than maxMovementPacketsPerSecond movement packets.
	var times []time.Time
	for _, p := range f.sent {
		switch p.core {
		case wire.CMSGMoveSetFacing, wire.CMSGMoveStartForward, wire.CMSGMoveHeartbeat, wire.CMSGMoveStop:
			times = append(times, p.at)
		}
	}
	for i := range times {
		n := 0
		for j := i; j < len(times) && times[j].Sub(times[i]) < time.Second; j++ {
			n++
		}
		if n > 4 {
			t.Fatalf("%d movement packets in one second at %v: %v", n, times[i], times)
		}
	}
}
