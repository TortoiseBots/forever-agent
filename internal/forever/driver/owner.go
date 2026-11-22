package driver

import (
	"context"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
)

// Owner is the bot module as the player run reaches it: contract bot-commands v1 and nothing else. It is the only
// way the run talks to the module, so the vocabulary is closed by its methods and no caller can put another `.`
// line on the wire (the run's own send gate carries the same rule for the two whisper words).
//
// A refused request is both a reply and an error (botcmd.ErrRefused wrapped with the code), so the run can name the
// code in its stop reason. Who is the module's own answer to `.bot who`; the rows are read through botcmd.Client,
// which the live adapter wraps in a later round.
type Owner interface {
	Add(ctx context.Context, bot string) (botcmd.Reply, error)
	Logout(ctx context.Context, bot string) (botcmd.Reply, error)
	Follow(ctx context.Context, bot string) (botcmd.Reply, error)
	Stay(ctx context.Context, bot string) (botcmd.Reply, error)
	Attack(ctx context.Context, bot string) (botcmd.Reply, error)
	Summon(ctx context.Context, bot string) (botcmd.Reply, error)
	Who(ctx context.Context) (botcmd.Reply, error)
}
