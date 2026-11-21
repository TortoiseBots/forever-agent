package live

// dummy.go: the live Situation for `dummy`, the training-dummy spell bench
// (module feature issue #18). One repeat: quiet arena, one durable target
// (the GM-spawned training dummy, or a passive high-health creature the
// teardown kills), the class bot's rotation for a fixed time, then teardown.
// Damage per spell and the reaction time come from the combat log rows the
// agent already parses; dummybench scores them against the per-class table.
//
// Every step reuses the A4 safety rules: server read-back before ok, unknown
// stays unknown, INVALID or SKIPPED never become PASS; the arena lock is
// held until Finish; a stranger ends the repeat INVALID real_player (checked
// at most every 5 s); teardown runs on every exit (repeat kills + refill,
// trial releases the hires).

import (
	"context"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// dummyRun is one repeat's dummy state.
type dummyRun struct {
	sit *LiveSituation
	r   *Repeat
	tb  *tbcmd.Client

	arena      *Arena
	dummy      string
	dummyEntry int
	fightStart time.Time
	// strangerWhy is set when a real player came within reach: nothing is
	// summoned or fought near them, and the repeat ends INVALID.
	strangerWhy string

	duration float64
}

// dummyEntry reads the trial's target: situation.dummy_entry, the entry id
// of the training dummy (or the passive high-health target) the GM spawns.
func dummyEntry(trial map[string]any) (int, string) {
	sit, _ := trial["situation"].(map[string]any)
	f, ok := numOpt(sit, "dummy_entry")
	if !ok || f < 1 {
		return 0, "situation.dummy_entry must be a creature entry id"
	}
	return int(f), ""
}

// run is the dummy driver: arena, one dummy, the fixed-time fight, fight
// marks. The bot's module rotation fights the whole window; the collector's
// combat log carries the casts, the damage and the readiness rows.
func (t *dummyRun) run(ctx context.Context) (map[string]any, error) {
	entry, why := dummyEntry(t.r.Trial)
	if why != "" {
		return map[string]any{"ok": false, "ended": "unsupported", "invalid_reason": why}, nil
	}
	t.dummyEntry = entry
	player, _ := t.r.Trial["player"].(map[string]any)
	level, _ := intOpt(player, "level")
	if level <= 0 {
		level = 1
	}
	t.duration, _ = numOpt(t.r.Trial, "duration_s")
	if t.duration <= 0 {
		t.duration = 60
	}

	arena, why := t.acquireArena(ctx, level)
	if arena == nil {
		return map[string]any{"ok": false, "ended": "no arena", "invalid_reason": why}, nil
	}
	// The arena lock is held until the runner calls Finish after the
	// repeat teardown: nothing is released here.

	if name := t.r.BotName; name != "" {
		if why := t.bringHire(ctx, name); why != "" {
			return hireNotBrought(why), nil
		}
		// The hire fights only with its engine on, and the move into the arena is where it can be lost: it is
		// set and proved by the bot's own state here, after the move, before anything is spawned.
		if why := proveHireEngines(ctx, t.tb, t.r, t.sit); why != "" {
			return map[string]any{"ok": false, "ended": "no_engine", "invalid_reason": why}, nil
		}
	}
	guid := t.summonDummy(ctx, entry)
	if guid == "" {
		if t.strangerWhy != "" {
			return map[string]any{"ok": false, "ended": "real_player", "invalid_reason": t.strangerWhy}, nil
		}
		return map[string]any{"ok": false, "ended": "no dummy",
			"invalid_reason": "no dummy (the GM spawn never showed a unit)"}, nil
	}
	t.dummy = guid
	t.r.ExtraChecks = append(t.r.ExtraChecks, t.dummyReadyRow(ctx))

	// The scored window opens at the engage order, before the first cast:
	// the attack order is the moment the bot can act (the dummy is proven
	// up, the bot has the target and is in range). Its first cast is thus
	// inside the window, and the engage order is its readiness floor.
	t.startFight(t.sit.now())
	// The pull: the bot attacks the dummy. The module's own rotation does
	// the casting from here; the serve attack call only starts contact.
	if name := t.r.BotName; name != "" {
		if _, err := t.tb.Attack(ctx, name, guid); err != nil {
			t.sit.logf("dummy: attack order refused: %v", err)
			t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "attack_refused"})
			return map[string]any{"ok": false, "ended": "attack_refused",
				"invalid_reason": "the attack order was refused: " + err.Error()}, nil
		}
	} else {
		_ = t.r.Client.Target(ctx, guid)
		_, _ = t.r.Client.Attack(ctx, guid, 2500)
	}
	fightSeen := t.waitDummyFight(ctx, 60*time.Second)
	if !fightSeen && t.strangerWhy != "" {
		t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "real_player"})
		return map[string]any{"ok": false, "ended": "real_player", "invalid_reason": t.strangerWhy}, nil
	}
	if !fightSeen {
		t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "no_fight"})
		return map[string]any{"ok": false, "ended": "no_fight",
			"invalid_reason": "the bot never engaged the dummy"}, nil
	}
	deadline := t.fightStart.Add(time.Duration(t.duration * float64(time.Second)))
	var lastStranger time.Time // zero: the first iteration checks at once
	for {
		if why := t.abortReason(); why != "" {
			t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "disconnected"})
			return map[string]any{"ok": false, "ended": "disconnected", "invalid_reason": why}, nil
		}
		if t.strangerWhy != "" {
			t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "real_player"})
			return map[string]any{"ok": false, "ended": "real_player", "invalid_reason": t.strangerWhy}, nil
		}
		now := t.sit.now()
		// A real player arriving mid-fight ends the repeat: checked at
		// most every 5 s (every iteration when iterations are slower).
		if t.strangerWhy == "" && (lastStranger.IsZero() || now.Sub(lastStranger) >= strangerEvery) {
			lastStranger = now
			if why, near := t.strangerNear(ctx); near {
				t.strangerWhy = why
			}
		}
		if t.strangerWhy != "" {
			t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "real_player"})
			return map[string]any{"ok": false, "ended": "real_player", "invalid_reason": t.strangerWhy}, nil
		}
		if alive, err := t.dummyAlive(ctx); err != nil || !alive {
			// The dummy died or vanished mid-fight: the GM respawns a
			// passive target; a bench that scores a dead dummy's
			// leftovers would claim damage the rotation did not deal.
			t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "dummy_down"})
			return map[string]any{"ok": false, "ended": "dummy_down",
				"invalid_reason": "the dummy died or despawned mid-fight"}, nil
		}
		if !now.Before(deadline) {
			break
		}
		sleepCtx(ctx, time.Second)
	}
	t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "duration"})
	return map[string]any{"ok": true, "ended": "duration", "dummy": normGUIDOf(t.dummy)}, nil
}

// summonDummy spawns the one fight target: a training dummy (or a passive
// high-health creature the teardown kills). It reuses the tank summon's
// claim: exactly one fresh unit of the entry, ledgered before anything
// attacks it, so teardown kills exactly what this repeat spawned.
//
// Every error path after the command may have gone out ledgers what it can:
// a lost reply is not proof the add did not run.
func (t *dummyRun) summonDummy(ctx context.Context, entry int) string {
	// Never act near a real player: no spawn while one is in reach.
	if why, near := t.strangerNear(ctx); near {
		t.sit.logf("no dummy spawn: %s", why)
		t.strangerWhy = why
		return ""
	}
	known := map[string]float64{}
	for _, g := range t.r.Ledger.SummonGUIDs() {
		known[normGUIDOf(g)] = 0
	}
	before, beforeOK := t.unitsOfEntry(ctx, 30, entry)
	if !beforeOK {
		// Without the pre-spawn view a fresh unit cannot be told from an
		// old one: nothing is claimed (an unknown spawn is counted, never
		// guessed).
		t.sit.logf("dummy entry %d: the pre-spawn unit scan failed", entry)
	}
	wall := t.r.Marks.NowISO()
	if err := t.r.Guard.Send(ctx, "npc_add_temp", TargetNone, "", itoa(entry)); err != nil {
		// The command may have run before the reply was lost (a refused
		// reply, a disconnect mid-send): the spawn state is unknown, so it
		// is counted unidentified and every fresh unit the entry shows is
		// ledgered now for teardown.
		t.r.Unidentified++
		t.ledgerUncertainSpawn(ctx, entry, known, before, err)
		if IsDisconnected(err) {
			return ""
		}
		t.sit.logf("npc add temp %d refused: %v", entry, err)
		return ""
	}
	guid := ""
	var seen []map[string]any
	if beforeOK {
		guid, seen = t.claim(ctx, wall, entry, 5*time.Second, known, before)
	}
	if guid == "" {
		// The add went through, so something spawned: every fresh
		// candidate is ledgered at once and teardown kills it. A spawn
		// nobody can identify is counted and fails the teardown row.
		t.ledgerCandidates(entry, seen, "")
		if len(seen) == 0 {
			t.r.Unidentified++
		}
		t.sit.logf("dummy entry %d: no unit claimed (new units in view: %d)", entry, len(seen))
		return ""
	}
	// The claim is ledgered before anything else is read or done to it: a
	// kill of an unledgered guid is refused.
	t.ledgerSummon(guid, entry, map[string]any{"dummy": true})
	t.r.Marks.Add(map[string]any{"kind": "summon", "guid": guid, "entry": entry, "wall": wall})
	if u, err := t.r.Client.Unit(ctx, guid); err == nil {
		t.r.Marks.Add(map[string]any{"kind": "summon", "guid": guid, "entry": entry,
			"level": u.Level, "wall": wall})
	}
	t.r.Guids.Mobs = append(t.r.Guids.Mobs, guid)
	return guid
}

// unitsOfEntry reads the units of one entry in range: guid -> distance, and
// ok false when the list is unreadable (never an empty "none in view").
func (t *dummyRun) unitsOfEntry(ctx context.Context, rangeYards float64, entry int) (map[string]float64, bool) {
	rows, err := t.r.Client.UnitsFilter(ctx, rangeYards, entry, "")
	if err != nil {
		return nil, false
	}
	out := map[string]float64{}
	for _, u := range rows {
		if g := normGUIDOf(u.GUID); g != "" {
			out[g] = u.Distance
		}
	}
	return out, true
}

// ledgerUncertainSpawn records what a send error left behind: one read of
// the entry's units, every fresh one ledgered for teardown. An unreadable
// view ledgers nothing and says so.
func (t *dummyRun) ledgerUncertainSpawn(ctx context.Context, entry int, known, before map[string]float64, sendErr error) {
	after, ok := t.unitsOfEntry(ctx, 30, entry)
	if !ok {
		t.ledgerCandidates(entry, nil, "the unit scan after the lost spawn reply failed: "+sendErr.Error())
		return
	}
	var seen []map[string]any
	for g, distance := range after {
		if _, ok := known[g]; ok {
			continue
		}
		if _, old := before[g]; old {
			continue
		}
		seen = append(seen, map[string]any{"guid": g, "distance": distance})
	}
	t.ledgerCandidates(entry, seen, "the spawn reply was lost: "+sendErr.Error())
}

// ledgerCandidates ledgeres every fresh spawn candidate and records the
// repeat's summon_unclaimed row (the ledger's kill list is the only handle
// teardown has on an unclaimed spawn).
func (t *dummyRun) ledgerCandidates(entry int, seen []map[string]any, why string) {
	for _, c := range seen {
		if g, _ := c["guid"].(string); g != "" {
			t.ledgerSummon(g, entry, map[string]any{"unclaimed": true})
		}
	}
	row := map[string]any{"kind": "summon_unclaimed", "entry": entry,
		"new_units": seen, "arena": t.arenaRow()}
	if why != "" {
		row["why"] = why
	}
	t.r.Ledger.Add(row)
	t.r.Marks.Add(map[string]any{"kind": "summon_unclaimed", "entry": entry})
}

// ledgerSummon puts one summoned guid in the ledger at once (the ledger is
// the kill list: the guard kills only ledgered summons and teardown kills
// every ledgered one).
func (t *dummyRun) ledgerSummon(guid string, entry int, extra map[string]any) {
	row := map[string]any{"kind": "summon", "guid": guid, "entry": entry, "wall": t.r.Marks.NowISO()}
	for k, v := range extra {
		row[k] = v
	}
	t.r.Ledger.Add(row)
}
