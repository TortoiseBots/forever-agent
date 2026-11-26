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
	case "no arena", "no mobs", "no dummy", "no_fight", "dummy_down",
		"prepull", "meal", "debuff", "unsupported":
		return true
	}
	return false
}

// tbOf is the `.tb` client of a teardown: the runner's override (tests) or
// the serve-backed runner over the repeat's client.
func (r *Runner) tbOf(rep *Repeat) *tbcmd.Client {
	tbr := r.tbRunner
	if tbr == nil {
		tbr = NewTBRunner(rep.Client)
	}
	return tbcmd.NewClient(tbr)
}

// teardownCtx is a teardown's own context: it does not inherit the run's
// cancellation (Ctrl-C must not cancel the kills) and has its own deadline.
func (r *Runner) teardownCtx(parent context.Context) (context.Context, context.CancelFunc) {
	d := r.TeardownTimeout
	if d <= 0 {
		d = 2 * time.Minute
	}
	return context.WithTimeout(context.WithoutCancel(parent), d)
}

func (r *Runner) healWait() time.Duration {
	if r.TeardownWait > 0 {
		return r.TeardownWait
	}
	return 6 * time.Second
}

func (r *Runner) healPoll() time.Duration {
	if r.TeardownPoll > 0 {
		return r.TeardownPoll
	}
	return 500 * time.Millisecond
}

// repeatMobs is every guid the repeat must clean up: its own Guids.Mobs plus
// every ledgered summon not already torn down by an earlier repeat (the
// ledger is the kill list, an unclaimed spawn is in it too).
func (r *Runner) repeatMobs(rep *Repeat) []string {
	seen := map[string]bool{}
	var out []string
	add := func(g string) {
		v := normGUIDOf(g)
		if v == "" || seen[v] || r.reaped[v] {
			return
		}
		seen[v] = true
		out = append(out, g)
	}
	for _, g := range rep.Guids.Mobs {
		add(g)
	}
	for _, g := range rep.Ledger.SummonGUIDs() {
		add(g)
	}
	return out
}

// repeatTeardown cleans one repeat and returns its rows.
func (r *Runner) repeatTeardown(ctx context.Context, rep *Repeat) []map[string]any {
	rows := []map[string]any{}
	tb := r.tbOf(rep)
	// Only the lease decides whether teardown may touch the server: a Ctrl-C
	// is exactly when the kills must still go out.
	if rep.Guard != nil {
		rep.Guard.SetAbort(r.TeardownAbort)
	}
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
	// The core's GM combat-log watch ends with the repeat: left running it would keep the session receiving the
	// party's combat packets into the next trial. off needs no unit; a core without the command refuses it and the
	// teardown goes on.
	if rep.Guard != nil {
		if err := rep.Guard.CombatLogWatch(ctx, "", "off"); err != nil && !IsDisconnected(err) {
			r.logf("%s rep%d: combat-log watch off: %v", rep.TrialID, rep.Index, err)
		}
	}
	// The forced role is cleared before anything else: a stale role would
	// drive the bot's next fight.
	if _, err := tb.RoleSelf(ctx, "clear"); err != nil && !IsDisconnected(err) {
		r.logf("%s rep%d: role self clear: %v", rep.TrialID, rep.Index, err)
	}
	_ = rep.Client.StopAttack(ctx)

	// Every ledgered summon of this repeat (Guids.Mobs and the ledger, the
	// unclaimed and out-of-window ones included); earlier repeats' mobs that
	// teardown already proved gone are not read again.
	only := r.repeatMobs(rep)
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
		unidentified := rep.Unidentified > 0
		if unidentified {
			evidence += fmt.Sprintf("; UNIDENTIFIED: %d spawned unit(s) could not be claimed and cannot be killed", rep.Unidentified)
		}
		rows = append(rows, map[string]any{"id": "mobs_dead", "ok": len(still) == 0 && len(unknown) == 0 && stateErr == nil && !unidentified,
			"value": killed, "evidence": evidence, "verified": verified})
		for _, g := range verified {
			if v, _ := g.(string); v != "" {
				if r.reaped == nil {
					r.reaped = map[string]bool{}
				}
				r.reaped[v] = true
			}
		}
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
// the only value that leaves the maximum alone), the hire and the extras (the
// module's hurt command at 100 percent, by name). Every unit is then read
// back by its recorded guid and must be at full health: an acknowledged but
// unapplied refill, or an unreadable unit, fails the row.
func (r *Runner) healFull(ctx context.Context, rep *Repeat, tb *tbcmd.Client) map[string]any {
	ok := true
	var evidence []string
	fail := func(format string, args ...any) {
		ok = false
		evidence = append(evidence, fmt.Sprintf(format, args...))
	}
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
	type target struct{ label, name, guid string }
	var targets []target
	if self != "" {
		targets = append(targets, target{"self", "", self})
	}
	if rep.Guids.Bot != "" {
		targets = append(targets, target{"hire", rep.BotName, rep.Guids.Bot})
	}
	for i, g := range rep.Guids.Extras {
		name := ""
		if i < len(rep.ExtraNames) {
			name = rep.ExtraNames[i]
		}
		targets = append(targets, target{fmt.Sprintf("extra%d", i+1), name, g})
	}
	for _, tg := range targets {
		if tg.name == "" {
			continue
		}
		if _, err := tb.FreeTextHurt(ctx, tg.name, 100); err != nil {
			fail(".tb hurt %s 100: %s", tg.name, trimReason(err.Error()))
		}
	}
	for _, tg := range targets {
		if tg.name == "" && tg.label != "self" {
			fail("%s has no name to refill: not proved full", tg.label)
			continue
		}
		hp, max, rerr := r.waitFull(ctx, rep.Client, tg.guid)
		switch {
		case rerr != nil:
			fail("%s %s unreadable after the refill: %s", tg.label, tg.guid, trimReason(rerr.Error()))
		case hp < max:
			fail("%s %s at %d/%d after the refill", tg.label, tg.guid, hp, max)
		}
	}
	if ok {
		evidence = append(evidence, fmt.Sprintf("%d unit(s) read back at full health after the refill", len(targets)))
	}
	return map[string]any{"id": "healed_full", "ok": ok, "value": int(st.MaxHealth),
		"evidence": strings.Join(evidence, "; ")}
}

// waitFull reads one unit by guid until it is at full health or the wait is
// over; it returns the last health read (an error when no read worked).
func (r *Runner) waitFull(ctx context.Context, client *Client, guid string) (hp, max uint32, err error) {
	deadline := time.Now().Add(r.healWait())
	for {
		u, uerr := client.Unit(ctx, guid)
		if uerr != nil {
			err = uerr
		} else if u.MaxHealth > 0 {
			hp, max, err = u.Health, u.MaxHealth, nil
			if hp >= max {
				return hp, max, nil
			}
		} else {
			err = fmt.Errorf("no max health on the unit row")
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return hp, max, err
		}
		time.Sleep(r.healPoll())
	}
}

// trialTeardown releases the trial's hires at trial end: stay, free, logout
// for each, with the module's own answers ledgered. It returns what it could
// not prove (items not cleared, a logout the module refused, the forced role
// not cleared): the runner records it and a sweep stops on it. It never
// changes a verdict.
func (r *Runner) trialTeardown(ctx context.Context, rep *Repeat, trialID string) []string {
	if r.dry() {
		return nil
	}
	if rep.Guard != nil {
		rep.Guard.SetAbort(r.TeardownAbort)
	}
	var problems []string
	tb := r.tbOf(rep)
	names := []string{}
	if rep.BotName != "" {
		names = append(names, rep.BotName)
	}
	names = append(names, rep.ExtraNames...)
	prov := &Provisioner{Client: rep.Client, TB: tb, Ledger: rep.Ledger, Server: r.Server, RecoveryPath: rep.RecoveryPath}
	for _, name := range names {
		if name == "" {
			continue
		}
		// What the trial gave goes back out of the bags while the hire is
		// still online; a clear that cannot be proved is ledgered and the
		// recovery mark stays for the next run to clean first.
		clearEv, cleared := prov.ClearGiven(ctx, name)
		rep.Ledger.Add(map[string]any{"kind": "items_cleared", "bot": name, "ok": cleared, "evidence": clearEv})
		if !cleared {
			r.logf("%s: items of %s not cleared: %s", trialID, name, clearEv)
			problems = append(problems, "items of "+name+" not cleared: "+trimReason(clearEv))
		}
		if _, err := tb.Stay(ctx, name); err != nil {
			r.logf("%s: .tb order %s stay: %v", trialID, name, err)
		}
		if _, err := tb.Free(ctx, name); err != nil {
			r.logf("%s: .tb order %s free: %v", trialID, name, err)
		}
		// The module answers `.tb logout` before the character is gone: a
		// teardown that returned on the answer left the hire online and the
		// next trial's login found it "already logged in" (2026-11-25). A
		// roster drop alone is no proof (a kick or a disband drops it too), so
		// the hire's own unit row must stop answering as well.
		lines, ev, proved := r.logoutProved(ctx, rep.Client, tb, name, hireGUID(rep, name))
		if !proved {
			r.logf("%s: .tb logout %s: %s", trialID, name, ev)
			problems = append(problems, "logout of "+name+" not confirmed: "+ev)
		}
		rep.Ledger.Add(map[string]any{"kind": "logged_out", "bot": name, "reply": firstLines(lines),
			"error": ev})
	}
	if _, err := tb.RoleSelf(ctx, "clear"); err != nil {
		r.logf("%s: role self clear: %v", trialID, err)
		problems = append(problems, "forced role not cleared: "+trimReason(err.Error()))
	}
	return problems
}

// hireGUID is the guid setup recorded for the named hire (the main hire or an
// extra, aligned by name with rep.Guids.Extras): the logout proof's unit read
// keys on it.
func hireGUID(rep *Repeat, name string) string {
	if strings.EqualFold(name, rep.BotName) {
		return canonGUID(rep.Guids.Bot)
	}
	// The extras' names and guids are separate lists: only equal lengths
	// prove the same order (a slot without a guid drops out of the guid
	// list), so a mismatch is no guid at all rather than a neighbour's.
	if len(rep.Guids.Extras) != len(rep.ExtraNames) {
		return ""
	}
	for i, extra := range rep.ExtraNames {
		if strings.EqualFold(name, extra) {
			return canonGUID(rep.Guids.Extras[i])
		}
	}
	return ""
}

// logoutProved sends `.tb logout` and waits for the hire to leave both the
// party roster and the server's unit list. The module answers before the
// character is logged out, and a party roster drop alone is no proof (a kick
// or a party disband drops the name while the bot stays online), so the unit
// row of the setup's recorded guid must stop answering too. A refusal is
// returned at once; a hire still online after the wait gets one more send,
// then the caller records the problem. It returns the last send's lines, the
// evidence, and whether the logout was proved.
func (r *Runner) logoutProved(ctx context.Context, client *Client, tb *tbcmd.Client, name, guid string) ([]string, string, bool) {
	var lines []string
	last := ""
	for send := 1; send <= 2; send++ {
		l, err := tb.FreeTextLogout(ctx, name)
		lines = l
		if err != nil {
			// A second send can race the character's own logout: a refusal
			// is no problem when the roster and the unit row prove it left.
			if send > 1 {
				if ok, why := r.logoutSettled(ctx, client, name, guid); ok {
					return lines, why, true
				}
			}
			return lines, trimReason(tbErrText(err, l)), false
		}
		if guid == "" {
			// Without the hire's guid there is no second read to prove the
			// logout; one send and the problem, never a long wait.
			return lines, "logout sent; the hire has no recorded guid, so a roster drop alone cannot prove it", false
		}
		deadline := time.Now().Add(r.logoutWait())
		for {
			ok, why := r.logoutSettled(ctx, client, name, guid)
			last = why
			if ok {
				return lines, "logout sent; " + why, true
			}
			if !time.Now().Before(deadline) {
				break
			}
			select {
			case <-ctx.Done():
				return lines, "logout sent; the wait ended with " + last, false
			case <-time.After(r.logoutPoll()):
			}
		}
	}
	return lines, "still online after two logout sends: " + last, false
}

// logoutSettled reports whether the hire is proved gone: off the party roster
// AND the server no longer answers its unit row. A missing guid, an
// unreadable roster or a unit row that still answers keep it unproved.
func (r *Runner) logoutSettled(ctx context.Context, client *Client, name, guid string) (bool, string) {
	if guid == "" {
		return false, "the hire has no recorded guid"
	}
	online, err := r.rosterHas(ctx, client, name)
	if err != nil {
		return false, "the party roster is unreadable: " + trimReason(err.Error())
	}
	if online {
		return false, "still in the party roster"
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, uerr := client.Unit(tctx, guid)
	cancel()
	if uerr == nil {
		return false, "the roster dropped it but the server still answers its unit"
	}
	if !isNotInView(uerr) {
		return false, "the unit read failed: " + trimReason(uerr.Error())
	}
	return true, "the party roster dropped it and the server no longer answers its unit"
}

// isNotInView is the serve's answer for a guid the world no longer holds.
func isNotInView(err error) bool {
	t := strings.ToLower(err.Error())
	return strings.Contains(t, "not in view") || strings.Contains(t, "not found")
}

// rosterHas reads the party roster and reports whether name is still on it.
func (r *Runner) rosterHas(ctx context.Context, client *Client, name string) (bool, error) {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := client.Party(tctx)
	if err != nil {
		return false, err
	}
	for _, m := range info.Members {
		if strings.EqualFold(m.Name, name) {
			return true, nil
		}
	}
	return false, nil
}

// logoutWait bounds the roster wait after `.tb logout` (0 is 20 s).
func (r *Runner) logoutWait() time.Duration {
	if r.LogoutWait > 0 {
		return r.LogoutWait
	}
	return 20 * time.Second
}

// logoutPoll is the pause between roster reads during that wait.
func (r *Runner) logoutPoll() time.Duration {
	if r.TeardownPoll > 0 {
		return r.TeardownPoll
	}
	return 500 * time.Millisecond
}

func listOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}
