package driver

import (
	"context"
	"errors"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The player run's own ports, beside the commander's four. The run leads the party itself: the bot module is reached
// only through Owner (bot-commands v1), the group commands through GroupPort, and the agent's own character through
// PlayerPort. World, Walker and Clock are the commander's, reused unchanged.

// PlayerPhase is the agent's own character's state, as the death watch sees it.
type PlayerPhase string

// The three phases of a character: alive, a corpse, or a ghost that may still be revived.
const (
	PhaseAlive PlayerPhase = "alive"
	PhaseDead  PlayerPhase = "dead"
	PhaseGhost PlayerPhase = "ghost"
)

// ErrCannotLoot is what a loot attempt answers for a corpse this character may not loot: the dynamic flag said no (a
// corpse that is another player's, or one whose window is gone). §3.7 loots only the corpses it may loot, so the run
// reads this as "nothing here" and goes on to the next one. A loot_object step reads it as the object's window never
// opening, which fails the step.
var ErrCannotLoot = errors.New("driver: the character may not loot that corpse")

// ErrNoGossipOption is what Gossip answers for a menu that lacks the option the plan names: the phase lags one update
// behind the event that adds it, so the run asks again for the plan's retry_s before the step fails (§3.11).
var ErrNoGossipOption = errors.New("driver: the gossip menu has no such option")

// PlayObject is one game object in the agent's view: what a use_object's and a wait's readback needs. State is the
// object's state as a plan names it ("ready", "active", "active_alt"); the view holds only objects the client has seen.
type PlayObject struct {
	GUID  wire.GUID
	Entry uint32
	Pos   Vec3
	State string
}

// RezOffer is a resurrect offer that is up for the agent's own character: who offers it (§3.10 answers a party
// member's offer and declines anyone else's).
type RezOffer struct {
	By string
}

// PartyMember is one row of the party the run leads: what FORM's online wait, the straggler rules and REST read.
// PowerType is the power the unit uses (0 mana, 1 rage, 2 focus, 3 energy), as the partial state sends it.
type PartyMember struct {
	Name      string
	GUID      wire.GUID
	Online    bool
	Dead      bool
	Ghost     bool
	HP        int
	MaxHP     int
	PowerType int
	Power     int
	MaxPower  int
	Pos       Vec3
	Map       uint32
	// Subgroup is the raid frame's subgroup, 1-8, or 0 when the group is a party (or the port cannot say): the
	// layout reads it to move a member into a subgroup that still has room.
	Subgroup int
}

// MemberFacts is one party member as the two sources read it: the roster's last member-stats push, and the client's
// own unit view when it sees the member. Roster merges the two; the recovery's one-time dump writes both, because the
// cause of Run B's ten-minute stall is not proven and the run log held no roster row at all.
type MemberFacts struct {
	Name string
	GUID wire.GUID
	// The roster: the last SMSG_GROUP_LIST or SMSG_PARTY_MEMBER_FULL_STATE push.
	RosterOnline bool
	RosterDead   bool
	RosterGhost  bool
	RosterHP     int
	RosterMaxHP  int
	RosterZone   uint16
	RosterPos    Vec3
	// The unit view, when the client holds the member as an object (InView). ViewHasUnit is false for an object whose
	// unit fields never arrived: its health and death are unknown, not zero.
	InView      bool
	ViewHasUnit bool
	ViewDead    bool
	ViewGhost   bool
	ViewHP      int
	ViewMaxHP   int
	ViewPos     Vec3
}

// GroupPort is the party this character leads: the roster the run reads and the group commands a player may send.
// Every command waits for the server's own readback (D-7) and answers an error when none comes, because this core
// refuses a group command silently: no readback is the only evidence a refusal leaves.
type GroupPort interface {
	// Roster is the party's members, this character included; empty when it leads no group.
	Roster() []PartyMember
	// MemberFacts is every member's raw roster row and unit-view reading, in roster order: what the recovery's
	// one-time dump writes, so a stall shows which source said what. Empty when there is no group.
	MemberFacts() []MemberFacts
	// RequestStats asks the server for fresh member states of the given members (CMSG_REQUEST_PARTY_MEMBER_STATS) and
	// waits, bounded by the port's own window, for the answers to merge. The roster is otherwise refreshed only by the
	// server's own pushes — a group update carries no health, position or zone — and a member that was released,
	// resurrected and summoned may get none at all (Run B, 2026-11-24: no fresh roster for ten minutes; the run log
	// cannot say what the roster held at the stop). A caller that needs a member's state asks here.
	RequestStats(ctx context.Context, guids []wire.GUID) error
	// Leader is the current leader's guid, empty when there is no group.
	Leader() wire.GUID
	// LootMethod sets the loot method and threshold and waits for the party update that shows it.
	LootMethod(method party.LootMethod, threshold int) error
	// Mark sets one raid target icon on a unit and waits for the echo; MarkRequestAll asks for the whole list.
	Mark(target wire.GUID, mark party.Mark) error
	// Disband uninvites every other member (D-5: this core has no disband packet) and waits for not-in-party.
	Disband() error
}

// PlayerPort is the agent's own character and what its client sees: the selection and attack the bot orders point at,
// the fight tick, the rest, the death watch, the loot, the two setup actions of the plan's steps and the game objects a
// step's readback needs. It is what the fight-check and the death watch already do, seen through the run's one
// goroutine.
type PlayerPort interface {
	// Select is CMSG_SET_SELECTION: the unit `.bot attack` takes for a bot.
	Select(guid wire.GUID) error
	// Attack starts the agent's own auto attack on the target and StopAttack ends it.
	Attack(guid wire.GUID) error
	StopAttack() error
	// FightTick keeps the agent's own fight going for one tick: auto attack plus the known spells between swings.
	FightTick(ctx context.Context, target wire.GUID) error
	// Rest sits the agent down until the party is ready or limit passes.
	Rest(ctx context.Context, limit time.Duration) error
	// Phase is the character's own state.
	Phase() PlayerPhase
	// Release sends CMSG_REPOP_REQUEST and AcceptRez takes the resurrect offer that is up.
	Release() error
	AcceptRez() error
	// RezOffer is the resurrect offer that is up, if any, and who offers it. The run answers a party member's offer and
	// declines anyone else's (§3.10), so it has to know both before it answers.
	RezOffer() (RezOffer, bool)
	// DeclineRez refuses the offer that is up, so the next one may come.
	DeclineRez() error
	// Loot opens one corpse or one game object's window the character may loot, takes everything and closes it.
	Loot(ctx context.Context, guid wire.GUID) error
	// UseObject uses a game object (a door, a lever) standing at stand, and Gossip picks one gossip option.
	UseObject(ctx context.Context, entry uint32, stand Vec3) error
	Gossip(ctx context.Context, entry uint32, option int) error
	// Objects is the game objects in the agent's own view: a use_object's and a wait_for's readback of a door's or a
	// lever's state. A caller must not keep the slice.
	Objects() []PlayObject
}

// Sight is the agent's stand-in for the server's line-of-sight test between two points (the module judges sight on the
// world's collision geometry, which the agent does not hold). known is false when it cannot say.
type Sight interface {
	InSight(from, to Vec3) (in, known bool)
}

// Refusals is the module's refusals of an attack order as the agent's client hears them. The module whispers "I dont
// see my rti attack target" (AttackAction.cpp:163) to the player who ordered when the bot finds no rti target: the
// mark out of AiPlayerbot.SightDistance, out of the core's line of sight from the bot, or not attackable
// (RtiTargetValue.h CalculateMark). SightRefusals is how many such whispers the bot has sent since the session began;
// the run compares the count before and after an order.
type Refusals interface {
	SightRefusals(bot string) int
}

// ServerSight is the server's own line-of-sight query (the bot module's read-only sight command): the same core call
// the module makes before it orders a bot onto a mark (RtiTargetValue.h's IsWithinLOSInMap from the BOT's own
// position). The agent's Sight port is a navmesh ray and not that test, so a pull reads this one where it can: the
// skull goes on a member the tank really sees, and a tank that sees none is walked into sight of one.
//
// known is false when the module cannot answer: no port, an older module without the command, a rate refusal, an
// access refusal, an unknown unit or an error. The caller then keeps its older flow exactly, and the module's own
// refusal of the order stays the only verdict on sight.
type ServerSight interface {
	// TankSees asks whether the bot sees the unit from where the bot stands, and how far apart they stand.
	TankSees(bot string, target wire.GUID) (in bool, dist float64, known bool)
	// PointSees asks whether a spot on the ground sees the unit. at is the eye's point, already raised by the caller
	// (PlayLosEyeYd); dist is what the server measured and 0 when it refused.
	PointSees(at Vec3, target wire.GUID) (in bool, dist float64, known bool)
}
