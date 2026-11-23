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
}

// GroupPort is the party this character leads: the roster the run reads and the group commands a player may send.
// Every command waits for the server's own readback (D-7) and answers an error when none comes, because this core
// refuses a group command silently: no readback is the only evidence a refusal leaves.
type GroupPort interface {
	// Roster is the party's members, this character included; empty when it leads no group.
	Roster() []PartyMember
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
