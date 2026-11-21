package live

import (
	"context"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// teleHires moves the repeat's own hires (the hire and the extras, by name) to the arena and returns the first
// refusal. Each one is named: `.tb tele party` with no leader is refused ambiguous-party as soon as the sender's
// character leads or commands more than one party (live dummy bench: the module said "name the party leader"), and a
// name-less party teleport would also move bots that are not this trial's. A hire is a host bot of this run, so the
// module accepts it by name.
func teleHires(ctx context.Context, tb *tbcmd.Client, r *Repeat, a *Arena) error {
	var first error
	names := []string{r.BotName}
	names = append(names, r.ExtraNames...)
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		if _, err := tb.Tele(ctx, name, uint32(a.Map), a.X, a.Y, a.Z); err != nil && first == nil {
			first = err
		}
	}
	return first
}
