package driver

import (
	"context"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
)

// OwnerBot is one bot the GoldenOwner answers about: the module state the player run's FORM, ENTER and END phases
// read. Level, Class, Move and Mode fill the `who` row; SummonCooldownUntil is on the scripted clock.
type OwnerBot struct {
	Name                string
	Online              bool
	InParty             bool
	Map                 uint32
	SummonCooldownUntil time.Time
	Dead                bool
	Level               int
	Class               string
	Move                string
	Mode                string
}

// GoldenOwner is the fake bot module of the player-run rounds: it answers contract bot-commands v1 from a scripted
// bot table, on the driver's virtual clock, and records every line it was asked. It stands in for the module side
// (TortoiseBots' player commands), which is not merged yet.
//
// Its lines are built only with botcmd.Line, so a case the contract cannot express cannot be scripted here either.
type GoldenOwner struct {
	Clock   *VClock
	SelfMap uint32 // the requester's map, for the summon rules
	Bots    []*OwnerBot
	seen    []string
}

// Lines returns the lines the fake was asked, in order.
func (o *GoldenOwner) Lines() []string { return nil }

// Add is `.bot add`.
func (o *GoldenOwner) Add(ctx context.Context, bot string) (botcmd.Reply, error) {
	return botcmd.Reply{}, nil
}

// Logout is `.bot logout`.
func (o *GoldenOwner) Logout(ctx context.Context, bot string) (botcmd.Reply, error) {
	return botcmd.Reply{}, nil
}

// Follow is `.bot follow`.
func (o *GoldenOwner) Follow(ctx context.Context, bot string) (botcmd.Reply, error) {
	return botcmd.Reply{}, nil
}

// Stay is `.bot stay`.
func (o *GoldenOwner) Stay(ctx context.Context, bot string) (botcmd.Reply, error) {
	return botcmd.Reply{}, nil
}

// Attack is `.bot attack`.
func (o *GoldenOwner) Attack(ctx context.Context, bot string) (botcmd.Reply, error) {
	return botcmd.Reply{}, nil
}

// Summon is `.bot summon`.
func (o *GoldenOwner) Summon(ctx context.Context, bot string) (botcmd.Reply, error) {
	return botcmd.Reply{}, nil
}

// Who is `.bot who`.
func (o *GoldenOwner) Who(ctx context.Context) (botcmd.Reply, error) { return botcmd.Reply{}, nil }
