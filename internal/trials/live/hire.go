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

// hireEngineWait is how long a hire has to show engine=yes after the bench set it on in the arena. The module builds
// the bot's engine on its own tick once the bot stands in the world next to its master, so a bot that was just
// teleported needs a few ticks.
const hireEngineWait = 20 * time.Second

// proveHireEngines is the check every situation runs after its hires are in the arena; the tests of the situations that
// have no `.tb` channel replace it.
var proveHireEngines = ensureHireEngines

// ensureHireEngines proves every hire runs the bot engine before the fight: `.tb engine <bot> on` (which also clears a
// failed attempt and makes the module build the engine again, TbBotMgr.cpp SetEngine) and then the bot's own `.tb state`
// engine=yes. A hire with no engine never attacks (the attack order is refused no-engine), so the repeat is INVALID
// with the state the module reported, never a fight nobody fights. It returns "" when every hire runs it.
func ensureHireEngines(ctx context.Context, tb *tbcmd.Client, r *Repeat, sit *LiveSituation) string {
	for _, h := range hireTargets(r) {
		if _, err := tb.FreeTextEngine(ctx, h.name, true); err != nil {
			why := fmt.Sprintf("SKIPPED needs=bot_engine: .tb engine %s on: %v", h.name, err)
			sit.logf("%s", why)
			return why
		}
		deadline := sit.now().Add(hireEngineWait)
		last := "its state was never read"
		for {
			states, err := tb.State(ctx, h.name)
			if err != nil {
				last = "the state read failed: " + err.Error()
			}
			up := false
			for _, b := range states {
				if !strings.EqualFold(b.Name, h.name) {
					continue
				}
				if b.Engine {
					up = true
					break
				}
				last = "engine=no"
				if b.NoEngine {
					last = "engine=no, stuck=noengine"
				}
			}
			if up {
				r.Marks.Add(map[string]any{"kind": "hire_engine", "bot": h.name, "engine": true})
				break
			}
			if sit.now().After(deadline) || ctx.Err() != nil {
				why := fmt.Sprintf("SKIPPED needs=bot_engine: %s in the arena after .tb engine on: the state stays %s (the engine starts only while the bot stands next to its master on the master's map, and the master is alive)", h.name, last)
				r.Marks.Add(map[string]any{"kind": "hire_engine", "bot": h.name, "engine": false, "state": last})
				sit.logf("%s", why)
				return why
			}
			sleepCtx(ctx, time.Second)
		}
	}
	return ""
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
