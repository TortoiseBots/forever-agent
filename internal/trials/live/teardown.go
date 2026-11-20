package live

// teardown.go: the repeat and trial teardown (a port of
// trl/live/teardown.py for the Forever serve).
//
// Per repeat: clear the forced role, kill every alive ledger summon and
// prove each state with a unit read, `.unaura all` on the player, leave the
// hire alone where the serve refuses hands on another player (recorded, never
// claimed), wait out of combat, and heal the player and the hire full.
// Per trial: the hire and the extras stand still (stay), drop their orders
// (free) and log out.
//
// A summon whose status cannot be read is UNKNOWN, never "none alive": the
// row fails and says why. Teardown rows live in the marks file; they never
// gate a repeat's verdict (Python's do not either).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// noFightEnd reports the situation ends that mean no scored fight happened.
func noFightEnd(ended string) bool {
	switch ended {
	case "no arena", "no mobs", "prepull", "meal", "debuff", "unsupported":
		return true
	}
	return false
}

// repeatTeardown cleans one repeat and returns its rows.
func (r *Runner) repeatTeardown(ctx context.Context, rep *Repeat) []map[string]any {
	rows := []map[string]any{}
	tb := tbcmd.NewClient(NewTBRunner(rep.Client))
	if r.dry() {
		for _, guid := range rep.Ledger.SummonGUIDs() {
			if guid == "" {
				continue
			}
			rows = append(rows, map[string]any{"id": "mobs_dead", "ok": true, "value": []any{guid},
				"evidence": "dry run: summon " + guid + " not sent"})
		}
		if rep.Guids.Bot != "" {
			rows = append(rows, map[string]any{"id": "hire_removed", "ok": true,
				"value": rep.Guids.Bot, "evidence": "dry run: bot released"})
		}
		return rows
	}
	// The forced role is cleared before anything else: a stale role would
	// drive the bot's next fight.
	if _, err := tb.RoleSelf(ctx, "clear"); err != nil && !IsDisconnected(err) {
		r.logf("%s rep%d: role self clear: %v", rep.TrialID, rep.Index, err)
	}
	_ = rep.Client.StopAttack(ctx)

	// This repeat's own summons: the arena only holds those, and an earlier
	// repeat's leftover may sit in another arena.
	only := append([]string(nil), rep.Guids.Mobs...)
	if len(only) == 0 {
		only = rep.Ledger.SummonGUIDs()
	}
	states, stateErr := r.summonStates(ctx, rep.Client, only)
	killed := []any{}
	if stateErr != nil {
		rows = append(rows, map[string]any{"id": "mobs_dead", "ok": false, "value": nil,
			"evidence": "summons status UNKNOWN (rpc error): " + trimReason(stateErr.Error())})
		rep.Ledger.Add(map[string]any{"kind": "mob_leftover", "guids": only,
			"reason": "unknown: " + trimReason(stateErr.Error())})
	} else {
		for attempt := 0; attempt < 4; attempt++ {
			if rep.Guard.AbortReason() != "" {
				break
			}
			var pending []string
			for _, g := range only {
				if states[normGUIDOf(g)] == "alive" {
					pending = append(pending, normGUIDOf(g))
				}
			}
			if len(pending) == 0 {
				break
			}
			for _, g := range pending {
				if err := rep.Guard.Kill(ctx, g); err != nil {
					r.logf("%s rep%d: gm_kill %s: %v", rep.TrialID, rep.Index, g, err)
					continue
				}
				killed = append(killed, g)
			}
			sleepCtx(ctx, 1500*time.Millisecond)
			if states, stateErr = r.summonStates(ctx, rep.Client, only); stateErr != nil {
				break
			}
		}
		still, unknown := []string{}, []string{}
		verified := []any{}
		for _, g := range only {
			switch states[normGUIDOf(g)] {
			case "alive":
				still = append(still, normGUIDOf(g))
			case "unknown", "":
				unknown = append(unknown, normGUIDOf(g))
			default:
				verified = append(verified, normGUIDOf(g))
			}
		}
		evidence := fmt.Sprintf("killed %d, still alive: %s", len(killed), listOrNone(still))
		if len(unknown) > 0 {
			evidence += "; UNKNOWN (not in view, not proved dead): " + strings.Join(unknown, ", ")
		}
		if stateErr != nil {
			evidence += "; last status read: " + trimReason(stateErr.Error())
		}
		rows = append(rows, map[string]any{"id": "mobs_dead", "ok": len(still) == 0 && len(unknown) == 0 && stateErr == nil,
			"value": killed, "evidence": evidence, "verified": verified})
		for _, g := range still {
			rep.Ledger.Add(map[string]any{"kind": "mob_leftover", "guids": []any{g}, "reason": "still alive"})
		}
		for _, g := range unknown {
			rep.Ledger.Add(map[string]any{"kind": "mob_leftover", "guids": []any{g}, "reason": "unknown: not in view"})
		}
	}

	// The player's own auras go through the raw `.unaura all` (the core reads
	// it off the selection; the player is the session's own character).
	if rep.Guids.Player != "" {
		if err := rep.Guard.Send(ctx, "unaura", TargetSelf, rep.Guids.Player, "all"); err != nil {
			rows = append(rows, map[string]any{"id": "player_unaura", "ok": false, "value": nil,
				"evidence": trimReason(err.Error())})
		} else {
			rows = append(rows, map[string]any{"id": "player_unaura", "ok": true, "value": "all",
				"evidence": ".unaura all on self"})
		}
	}
	// The hire's auras: the serve's GM hands act on this session's own
	// character only, so a hire's aura cannot be stripped. The row records
	// the server's own refusal; it is never claimed as done.
	if hire := rep.Guids.Bot; hire != "" {
		row := map[string]any{"id": "hire_unaura", "ok": false, "value": nil,
			"evidence": "not attempted: no aura read of the hire"}
		if u, err := rep.Client.Unit(ctx, hire); err == nil {
			if auras := sortedSpellIDs(u.Raw["auras"]); len(auras) > 0 {
				spell := int(intOf(auras[0]))
				if rerr := rep.Guard.RemoveAura(ctx, hire, spell); rerr != nil {
					row["evidence"] = "the serve refused gm_aura on the hire: " + trimReason(rerr.Error())
				} else {
					row["ok"], row["evidence"] = true, "gm_aura removed the hire's aura"
				}
			} else {
				row["evidence"] = "the hire holds no aura (unit read)"
			}
		}
		rows = append(rows, row)
	}

	// Out of combat before the refill: a heal in combat is a fight action.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if !anyInCombat(ctx, rep.Client) {
			break
		}
		sleepCtx(ctx, time.Second)
	}
	rows = append(rows, r.healFull(ctx, rep, tb))
	return rows
}

// summonStates maps guid -> alive|dead|gone|unknown. A read failure is the
// caller's UNKNOWN, never "gone".
func (r *Runner) summonStates(ctx context.Context, client *Client, guids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(guids) == 0 {
		return out, nil
	}
	rows, err := client.Units(ctx, 60)
	if err != nil {
		return out, err
	}
	byGUID := map[string]Unit{}
	for _, u := range rows {
		byGUID[normGUIDOf(u.GUID)] = u
	}
	for _, g := range guids {
		v := normGUIDOf(g)
		if v == "" {
			continue
		}
		if u, ok := byGUID[v]; ok {
			if u.Dead {
				out[v] = "dead"
			} else {
				out[v] = "alive"
			}
			continue
		}
		u, err := client.Unit(ctx, v)
		if err != nil {
			if notInView(err) {
				out[v] = "gone"
				continue
			}
			return out, err
		}
		if u.Dead {
			out[v] = "dead"
		} else {
			out[v] = "alive"
		}
	}
	return out, nil
}

func anyInCombat(ctx context.Context, client *Client) bool {
	rows, err := client.Units(ctx, 40)
	if err != nil {
		return true // an unreadable list is not "nobody fights"
	}
	for _, u := range rows {
		if u.InCombat {
			return true
		}
	}
	return false
}

// healFull refills the player (gm_set_hp with the character's own maximum,
// the only value that leaves the maximum alone) and the hire (the module's
// hurt command at 100 percent), each proved by a unit read.
func (r *Runner) healFull(ctx context.Context, rep *Repeat, tb *tbcmd.Client) map[string]any {
	self := rep.Guids.Player
	st, err := rep.Client.StatusOf(ctx)
	if err != nil {
		return map[string]any{"id": "healed_full", "ok": false, "value": nil,
			"evidence": "status unreadable: " + trimReason(err.Error())}
	}
	if self != "" && st.MaxHealth > 0 {
		if err := rep.Guard.SetHP(ctx, self, int(st.MaxHealth)); err != nil {
			return map[string]any{"id": "healed_full", "ok": false, "value": nil,
				"evidence": "gm_set_hp self: " + trimReason(err.Error())}
		}
	}
	if hire := rep.Guids.Bot; hire != "" {
		if _, err := tb.FreeTextHurt(ctx, hire, 100); err != nil {
			return map[string]any{"id": "healed_full", "ok": false, "value": nil,
				"evidence": ".tb hurt " + hire + " 100: " + trimReason(err.Error())}
		}
	}
	ok := true
	evidence := ".modify hp self full and .tb hurt hire 100"
	if self != "" {
		if u, uerr := rep.Client.Unit(ctx, self); uerr == nil && u.MaxHealth > 0 && u.Health < u.MaxHealth {
			ok, evidence = false, fmt.Sprintf("self at %d/%d after the refill", u.Health, u.MaxHealth)
		}
	}
	return map[string]any{"id": "healed_full", "ok": ok, "value": int(st.MaxHealth), "evidence": evidence}
}

// trialTeardown releases the trial's hires at trial end: stay, free, logout
// for each, with the module's own answers ledgered. A refusal is recorded;
// it never changes a verdict.
func (r *Runner) trialTeardown(ctx context.Context, rep *Repeat, trialID string) {
	if r.dry() {
		return
	}
	tb := tbcmd.NewClient(NewTBRunner(rep.Client))
	names := []string{}
	if rep.BotName != "" {
		names = append(names, rep.BotName)
	}
	names = append(names, rep.ExtraNames...)
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, err := tb.Stay(ctx, name); err != nil {
			r.logf("%s: .tb order %s stay: %v", trialID, name, err)
		}
		if _, err := tb.Free(ctx, name); err != nil {
			r.logf("%s: .tb order %s free: %v", trialID, name, err)
		}
		lines, err := tb.FreeTextLogout(ctx, name)
		ev := "logout sent"
		if err != nil {
			ev = trimReason(tbErrText(err, lines))
			r.logf("%s: .tb logout %s: %v", trialID, name, err)
		}
		rep.Ledger.Add(map[string]any{"kind": "logged_out", "bot": name, "reply": firstLines(lines),
			"error": ev})
	}
	if _, err := tb.RoleSelf(ctx, "clear"); err != nil {
		r.logf("%s: role self clear: %v", trialID, err)
	}
}

func listOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}
