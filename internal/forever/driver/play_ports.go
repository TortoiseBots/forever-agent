package driver

import (
	"context"
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

// PlayerPort is the agent's own character: the selection and attack the bot orders point at, the fight tick, the
// rest, the death watch, the loot and the two setup actions of the plan's steps. It is what the fight-check and the
// death watch already do, seen through the run's one goroutine.
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
	// Release sends CMSG_REPOP_REQUEST and AcceptRez takes a resurrect offer.
	Release() error
	AcceptRez() error
	// Loot opens one corpse the character may loot, takes everything and closes it.
	Loot(ctx context.Context, guid wire.GUID) error
	// UseObject uses a game object (a door, a lever) standing at stand, and Gossip picks one gossip option.
	UseObject(ctx context.Context, entry uint32, stand Vec3) error
	Gossip(ctx context.Context, entry uint32, option int) error
}
