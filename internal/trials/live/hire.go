package live

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// hireTarget is one of the repeat's own hires: its name, and its guid when the hire reply gave one.
type hireTarget struct {
	name, guid string
}

// hireTargets lists the hire and the extras of a repeat, each with the guid the setup read from the hire reply (the
// extras' guids are in the setup's order).
func hireTargets(r *Repeat) []hireTarget {
	var out []hireTarget
	if strings.TrimSpace(r.BotName) != "" {
		out = append(out, hireTarget{name: r.BotName, guid: canonGUID(r.Guids.Bot)})
	}
	for i, name := range r.ExtraNames {
		if strings.TrimSpace(name) == "" {
			continue
		}
		g := ""
		if i < len(r.Guids.Extras) {
			g = canonGUID(r.Guids.Extras[i])
		}
		out = append(out, hireTarget{name: name, guid: g})
	}
	return out
}

// bringHires puts every hire of the repeat in the arena and proves each within 10 yards. Each hire is teleported by
// name: `.tb tele party` with no leader is refused ambiguous-party as soon as the sender's character leads or
// commands more than one party (live dummy bench), and a name-less party teleport would also move bots that are not
// this trial's. A hire whose teleport was refused is asked to come. Arrival is read by the hire's guid first (a
// player row carries no name on this core), by name second. It returns "" when every hire stands in the arena, or the
// reason the repeat cannot be scored: a hire that is not there is an INVALID repeat, never a fight with a missing
// healer or tank.
func bringHires(ctx context.Context, tb *tbcmd.Client, r *Repeat, sit *LiveSituation, a *Arena) string {
	targets := hireTargets(r)
	refused := map[string]error{}
	if a != nil {
		for _, h := range targets {
			if _, err := tb.Tele(ctx, h.name, uint32(a.Map), a.X, a.Y, a.Z); err != nil {
				refused[h.name] = err
				sit.logf("hire teleport of %s refused (%v); asking it to come", h.name, err)
				_, _ = tb.Come(ctx, h.name)
			}
		}
	}
	arrived := map[string]bool{}
	deadline := sit.now().Add(45 * time.Second)
	for {
		units, err := r.Client.Units(ctx, 100)
		for _, h := range targets {
			if arrived[h.name] || err != nil {
				continue
			}
			if u, ok := findHire(units, h); ok && u.Distance <= 10 {
				arrived[h.name] = true
				r.Marks.Add(map[string]any{"kind": "hire_brought", "bot": h.name, "distance": u.Distance})
			}
		}
		if len(arrived) == len(targets) {
			return ""
		}
		if sit.now().After(deadline) || ctx.Err() != nil {
			for _, h := range targets {
				if arrived[h.name] {
					continue
				}
				why := fmt.Sprintf("hire %s is not within 10 yd of the arena", h.name)
				if e := refused[h.name]; e != nil {
					why += fmt.Sprintf(" (the teleport was refused: %v)", e)
				}
				sit.logf("%s", why)
				return why
			}
		}
		sleepCtx(ctx, time.Second)
	}
}

// findHire finds one hire in a unit list: by guid when it has one, by name otherwise.
func findHire(units []Unit, h hireTarget) (Unit, bool) {
	for _, u := range units {
		if h.guid != "" && canonGUID(u.GUID) == h.guid {
			return u, true
		}
	}
	if h.guid == "" {
		for _, u := range units {
			if equalFold(u.Name, h.name) {
				return u, true
			}
		}
	}
	return Unit{}, false
}

// hireNotBrought is the outcome of a repeat whose hire never stood in the arena.
func hireNotBrought(why string) map[string]any {
	return map[string]any{"ok": false, "ended": "hire_not_brought", "invalid_reason": why}
}
