package driver

// The watch's GM combat-log watch (B of SPEC #109). The core's `.gm combatlog [group|off]` (cs_gm.cpp:195-232)
// makes the watching character receive the same combat-log packets a player in range would, so a watcher whose
// observer stands outside the fight still records it whole. The watch starts it on the tank once the party is in
// (the tank is the module's leader, and its group is the party the module runs), and stops it in the cleanup phase,
// whatever ended the watch. A core older than 0.1.38 answers "no such command": that is one warning line, the run
// goes on with the in-range log, and the summary says "unsupported".

import (
	"context"
	"strings"
)

// startCombatlogWatch starts the core's watch with the configured mode. It never ends the run: only a lost link
// does (and then nothing can be sent anyway). The summary's combatlog_watch keeps what the watch asked for, or
// "unsupported" when the core could not start one.
func (r *Run) startCombatlogWatch(ctx context.Context) {
	mode := strings.ToLower(strings.TrimSpace(r.cfg.CombatLogWatch))
	switch mode {
	case "":
		return
	case "off":
		// The operator turned the watch off: nothing is sent and the summary says so.
		r.combatlogMode = "off"
		return
	case "group", "unit":
	default:
		r.logf("watch: combatlog-watch %q is not group, unit or off; the log is in range only", r.cfg.CombatLogWatch)
		r.combatlogMode = "unsupported"
		return
	}
	rep, why := r.act(ctx, Action{Kind: ActionGMCombatLog, Who: r.cfg.Tank(), Mode: mode})
	if why == "" {
		r.combatlogMode = mode
		r.logf("watch: combat-log watch on the tank (%s)", mode)
		return
	}
	// Any end of the start without a watch (a refusal, an old core, a lost link) leaves only the in-range log;
	// "unsupported" is the summary's word for a watch that is not running.
	r.combatlogMode = "unsupported"
	switch why {
	case "link", "stop":
		return
	}
	if rep.Code == "unsupported-core" {
		r.logf("watch: the core has no .gm combatlog (needs 0.1.38 or later); the log is in range only")
		return
	}
	r.logf("watch: the combat-log watch (%s) was refused code=%s; the log is in range only", mode, nonEmpty(rep.Code, "refused"))
}

// stopCombatlogWatch stops a watch this run started. It runs in the cleanup phase, after the run's stop, so it is
// the one state action act admits while a stop is pending. A refusal is one line, never a failed cleanup.
func (r *Run) stopCombatlogWatch(ctx context.Context) {
	if r.combatlogMode != "group" && r.combatlogMode != "unit" {
		return
	}
	rep, why := r.act(ctx, Action{Kind: ActionGMCombatLog, Mode: "off"})
	switch why {
	case "":
	case "link":
	default:
		r.logf("watch: the combat-log watch stop answered %s", nonEmpty(rep.Code, reasonCode(why)))
	}
}
