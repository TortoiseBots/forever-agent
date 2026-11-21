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
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
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
	blocked    bool
	// strangerWhy is set when a real player came within reach: nothing is
	// summoned or fought near them, and the repeat ends INVALID.
	strangerWhy string

	rotation []string
	duration float64
	sitOpts  map[string]any
}

// dummyRotation reads the trial's rotation: situation.rotation, a non-empty
// list of spell names the bot casts in order, looping for the fixed time.
func dummyRotation(trial map[string]any) ([]string, string) {
	sit, _ := trial["situation"].(map[string]any)
	list, ok := pyjson.List(sit["rotation"])
	if !ok || len(list) == 0 {
		return nil, "situation.rotation must be a non-empty list of spell names"
	}
	var out []string
	for _, item := range list {
		name, ok := item.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, "situation.rotation must be a non-empty list of spell names"
		}
		out = append(out, name)
	}
	return out, ""
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

// run is the dummy driver: arena, one dummy, rotation for the fixed time,
// fight marks. The bot fights the whole window; the collector's combat log
// carries the casts, the damage and the reaction clock.
func (t *dummyRun) run(ctx context.Context) (map[string]any, error) {
	t.sitOpts, _ = t.r.Trial["situation"].(map[string]any)
	rotation, why := dummyRotation(t.r.Trial)
	if why != "" {
		return map[string]any{"ok": false, "ended": "unsupported", "invalid_reason": why}, nil
	}
	entry, why := dummyEntry(t.r.Trial)
	if why != "" {
		return map[string]any{"ok": false, "ended": "unsupported", "invalid_reason": why}, nil
	}
	t.rotation, t.dummyEntry = rotation, entry
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
		t.bringHire(ctx, name)
	}
	guid := t.summonDummy(ctx, entry, level)
	if guid == "" {
		if t.strangerWhy != "" {
			return map[string]any{"ok": false, "ended": "real_player", "invalid_reason": t.strangerWhy}, nil
		}
		return map[string]any{"ok": false, "ended": "no dummy",
			"invalid_reason": "no dummy (the GM spawn never showed a unit)"}, nil
	}
	t.dummy = guid
	t.r.ExtraChecks = append(t.r.ExtraChecks, t.dummyReadyRow(ctx))

	// The pull: the bot attacks the dummy. The module's own rotation does
	// the casting from here; the serve attack call only starts contact.
	if name := t.r.BotName; name != "" {
		if _, err := t.tb.Attack(ctx, name, guid); err != nil {
			t.sit.logf("dummy: attack order refused: %v", err)
		}
	} else {
		_ = t.r.Client.Target(ctx, guid)
		_, _ = t.r.Client.Attack(ctx, guid, 2500)
	}
	fightSeen := t.waitDummyFight(ctx, 60*time.Second)
	if !fightSeen {
		t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": "no_fight"})
		return map[string]any{"ok": false, "ended": "no_fight",
			"invalid_reason": "the bot never engaged the dummy"}, nil
	}
	t.startFight(t.sit.now())
	deadline := t.fightStart.Add(time.Duration(t.duration * float64(time.Second)))
	lastStranger := t.sit.now()
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
		if t.strangerWhy == "" && now.Sub(lastStranger) >= strangerEvery {
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
func (t *dummyRun) summonDummy(ctx context.Context, entry, level int) string {
	mobs := t.sit.Mobs
	if mobs == nil {
		mobs = LoadMobs()
	}
	_ = level
	// Never act near a real player: no spawn while one is in reach.
	if why, near := t.strangerNear(ctx); near {
		t.sit.logf("no dummy spawn: %s", why)
		t.strangerWhy = why
		t.blocked = true
		return ""
	}
	known := map[string]bool{}
	for _, g := range t.r.Ledger.SummonGUIDs() {
		known[normGUIDOf(g)] = true
	}
	before := map[string]bool{}
	if rows, err := t.r.Client.UnitsFilter(ctx, 30, entry, ""); err == nil {
		for _, u := range rows {
			before[normGUIDOf(u.GUID)] = true
		}
	}
	wall := t.r.Marks.NowISO()
	if err := t.r.Guard.Send(ctx, "npc_add_temp", TargetNone, "", itoa(entry)); err != nil {
		if IsDisconnected(err) {
			t.blocked = true
			return ""
		}
		t.sit.logf("npc add temp %d refused: %v", entry, err)
		return ""
	}
	guid, seen := t.claim(ctx, wall, entry, 5*time.Second, known, before)
	_ = mobs
	if guid == "" {
		// The add went through, so something spawned: every fresh
		// candidate is ledgered at once and teardown kills it. A spawn
		// nobody can identify is counted and fails the teardown row.
		for _, c := range seen {
			if g, _ := c["guid"].(string); g != "" {
				t.ledgerSummon(g, entry, map[string]any{"unclaimed": true})
			}
		}
		if len(seen) == 0 {
			t.r.Unidentified++
		}
		row := map[string]any{"kind": "summon_unclaimed", "entry": entry,
			"new_units": seen, "arena": t.arenaRow()}
		t.r.Ledger.Add(row)
		t.r.Marks.Add(map[string]any{"kind": "summon_unclaimed", "entry": entry})
		t.sit.logf("dummy entry %d: no unit claimed (new units in view: %d)", entry, len(seen))
		t.blocked = true
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
