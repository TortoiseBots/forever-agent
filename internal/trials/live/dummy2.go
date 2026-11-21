package live

// dummy2.go: the rest of the dummy repeat: read-backs, the fight clock and
// the engagement proof. The legs intentionally mirror the tank run's (arena,
// quiet probe, hire, claim) line for line, so the A4 safety rules cannot
// drift between the two kinds; a shared struct would hide the receiver the
// tests construct.

import (
	"context"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// OrderReport and ActionReport are the tbcmd reports the dummy legs use.
type OrderReport = tbcmd.OrderReport
type ActionReport = tbcmd.ActionReport

// acquireArena takes the first eligible arena whose lock this run can hold
// and that reads quiet, waiting at most arenaGiveUpS. The lock is held
// until the runner calls Finish after the repeat teardown: nothing is
// released here.
func (t *dummyRun) acquireArena(ctx context.Context, level int) (*Arena, string) {
	arenas := t.sit.Arenas
	if arenas == nil {
		arenas = LoadArenas()
	}
	var eligible []Arena
	for _, a := range arenas {
		lo, hi := a.LevelMin, a.LevelMax
		if lo == 0 {
			lo = 1
		}
		if hi == 0 {
			hi = 60
		}
		if lo <= level && level <= hi {
			eligible = append(eligible, a)
		}
	}
	if len(eligible) == 0 {
		return nil, fmt.Sprintf("no arena for level %d", level)
	}
	// The slot's rotation: two slots try different arenas first.
	if k := t.sit.SlotIndex % len(eligible); k > 0 {
		eligible = append(eligible[k:], eligible[:k]...)
	}
	giveUp := t.sit.now().Add(time.Duration(arenaGiveUpS * float64(time.Second)))
	for {
		for i := range eligible {
			a := &eligible[i]
			lock := NewArenaLock(t.sit.Server, a.ID, t.sit.ArenaStateDir)
			if !lock.Acquire() {
				t.sit.logf("arena %s locked by another run, next", a.ID)
				continue
			}
			if err := t.r.Guard.Send(ctx, "go_xyz", TargetNone, "",
				itoa(int(roundFloat(a.X))), itoa(int(roundFloat(a.Y))), itoa(int(roundFloat(a.Z))), itoa(a.Map)); err != nil {
				lock.Release()
				if IsDisconnected(err) {
					return nil, "disconnected"
				}
				t.sit.logf("arena %s: .go xyz refused: %v", a.ID, err)
				continue
			}
			sleepCtx(ctx, 2500*time.Millisecond)
			if t.quiet(ctx) {
				t.sit.logf("arena %s quiet", a.ID)
				t.arena = a
				t.sit.hold(lock)
				t.r.Marks.Add(map[string]any{"kind": "arena", "id": a.ID, "map": a.Map,
					"x": a.X, "y": a.Y, "z": a.Z})
				return a, ""
			}
			t.sit.logf("arena %s not quiet, next", a.ID)
			lock.Release()
		}
		if t.sit.now().After(giveUp) {
			return nil, "no quiet arena"
		}
		if ctx.Err() != nil {
			return nil, "disconnected"
		}
		sleepCtx(ctx, time.Duration(arenaWaitS*float64(time.Second)))
	}
}

// quiet reports whether the arena holds no unit but the party: the serve's
// unit rows carry no faction, so any live creature within 40 yards counts as
// not quiet, and so does EVERY player character that is not the tester or a
// hire of this trial (a stranger, dead or alive, whatever its row type). An
// unreadable list is not quiet.
func (t *dummyRun) quiet(ctx context.Context) bool {
	units, err := t.r.Client.Units(ctx, 40)
	if err != nil {
		return false
	}
	party := t.party()
	for _, u := range units {
		if party[normGUIDOf(u.GUID)] {
			continue
		}
		if isPlayerRow(u) || !u.Dead {
			return false
		}
	}
	return true
}

// party is the guids that belong to this trial: the tester, its own pet and
// the hired bots. Nobody else is a friend.
func (t *dummyRun) party() map[string]bool {
	party := map[string]bool{}
	add := func(g string) {
		if g = normGUIDOf(g); g != "" {
			party[g] = true
		}
	}
	add(t.r.Guids.Player)
	add(t.r.Guids.Bot)
	add(t.r.Guids.Pet)
	for _, g := range t.r.Guids.Extras {
		add(g)
	}
	if t.r.Guard != nil {
		add(t.r.Guard.PlayerGUID())
	}
	return party
}

// strangerNear reports a real player within 40 yards of the arena (or an
// unreadable list: nothing is done on a guess).
func (t *dummyRun) strangerNear(ctx context.Context) (string, bool) {
	units, err := t.r.Client.Units(ctx, 40)
	if err != nil {
		return "the unit list is unreadable, so a real player cannot be ruled out", true
	}
	party := t.party()
	for _, u := range units {
		if isPlayerRow(u) && !party[normGUIDOf(u.GUID)] {
			return "a real player (" + normGUIDOf(u.GUID) + ") came within reach of the arena", true
		}
	}
	return "", false
}

// bringHire puts the hire in the arena and proves it within 10 yards: the
// party teleport first, then the module's come order, then a failure log
// (the fight still runs; the hire's distance is not a scored row).
func (t *dummyRun) bringHire(ctx context.Context, name string) {
	a := t.arena
	if a != nil {
		if err := teleHires(ctx, t.tb, t.r, a); err != nil {
			t.sit.logf("hire teleport refused (%v); asking %s to come", err, name)
			_, _ = t.tb.Come(ctx, name)
		}
	}
	deadline := t.sit.now().Add(45 * time.Second)
	for {
		u, err := t.unitByName(ctx, name)
		if err == nil && u.Distance <= 10 {
			t.r.Marks.Add(map[string]any{"kind": "hire_brought", "bot": name, "distance": u.Distance})
			return
		}
		if t.sit.now().After(deadline) || ctx.Err() != nil {
			t.sit.logf("hire %s is not within 10 yd (distance %v)", name, distanceOf(u, err))
			return
		}
		sleepCtx(ctx, time.Second)
	}
}

func (t *dummyRun) unitByName(ctx context.Context, name string) (Unit, error) {
	units, err := t.r.Client.UnitsFilter(ctx, 100, 0, name)
	if err != nil {
		return Unit{}, err
	}
	for _, u := range units {
		if equalFold(u.Name, name) {
			return u, nil
		}
	}
	return Unit{}, errf("no unit named %s in view", name)
}

// claim finds the unit an `.npc add temp` produced: a fresh unit of the
// entry that was not in view before. Exactly one fresh unit is the summon
// wherever it stands in the 30-yard scan; two or more need a unit within 3
// yards of the player (the add spawns at the caster).
func (t *dummyRun) claim(ctx context.Context, wall string, entry int, timeout time.Duration, known, before map[string]float64) (string, []map[string]any) {
	_ = wall
	deadline := t.sit.now().Add(timeout)
	var seen []map[string]any
	for {
		seen = nil
		var fresh []Unit
		if rows, err := t.r.Client.UnitsFilter(ctx, 30, entry, ""); err == nil {
			for _, u := range rows {
				g := normGUIDOf(u.GUID)
				if g == "" || u.Dead {
					continue
				}
				if _, ok := known[g]; ok {
					continue
				}
				if _, ok := before[g]; ok {
					continue
				}
				fresh = append(fresh, u)
				seen = append(seen, map[string]any{"guid": g, "distance": u.Distance})
			}
		}
		sortUnits(fresh)
		if len(fresh) > 0 && fresh[0].Distance <= 3 {
			return normGUIDOf(fresh[0].GUID), seen
		}
		if len(fresh) == 1 {
			return normGUIDOf(fresh[0].GUID), seen
		}
		if !t.sit.now().Before(deadline) || ctx.Err() != nil {
			return "", seen
		}
		sleepCtx(ctx, 500*time.Millisecond)
	}
}

func (t *dummyRun) arenaRow() map[string]any {
	if t.arena == nil {
		return nil
	}
	a := t.arena
	return map[string]any{"id": a.ID, "map": a.Map, "x": a.X, "y": a.Y, "z": a.Z}
}

// startFight stamps the repeat's scored base once (t = 0 for every rule).
func (t *dummyRun) startFight(now time.Time) {
	if !t.fightStart.IsZero() {
		return
	}
	t.fightStart = now
	mobs := []any{normGUIDOf(t.dummy)}
	t.r.Marks.Add(map[string]any{"kind": "fight_start", "mobs": mobs, "dummy": normGUIDOf(t.dummy)})
}

// dummyReadyRow proves the dummy is up: alive, of the spawned entry. A
// training dummy never fights back, so combat is not required (unlike the
// tank's mobs_ready, where combat is the hostile proof).
func (t *dummyRun) dummyReadyRow(ctx context.Context) map[string]any {
	u, err := t.r.Client.Unit(ctx, t.dummy)
	ok := err == nil && !u.Dead && u.Entry == t.dummyEntry
	evidence := fmt.Sprintf("dummy %s entry %d (want entry %d)",
		normGUIDOf(t.dummy), unitEntry(u, err), t.dummyEntry)
	if err != nil {
		evidence = fmt.Sprintf("dummy %s unreadable: %v", normGUIDOf(t.dummy), err)
	}
	return map[string]any{"id": "mobs_ready", "ok": ok,
		"value": []any{normGUIDOf(t.dummy)}, "evidence": evidence}
}

// waitDummyFight waits up to timeout for the pull to put the dummy in
// combat (or the bot's casts to start landing: the pump's combat rows are
// the engagement proof when the dummy never fights back).
func (t *dummyRun) waitDummyFight(ctx context.Context, timeout time.Duration) bool {
	deadline := t.sit.now().Add(timeout)
	for {
		if t.dummyEngaged(ctx) {
			return true
		}
		if !t.sit.now().Before(deadline) || ctx.Err() != nil {
			return false
		}
		sleepCtx(ctx, time.Second)
	}
}

// dummyEngaged reports the fight started: the dummy is in combat, or the
// bot already acted on it (a cast, a swing or a damage row from the bot at
// the dummy in the combat log). A passive dummy that never turns InCombat
// is engaged by the bot's own first cast; the dummy's own casts prove
// nothing.
func (t *dummyRun) dummyEngaged(ctx context.Context) bool {
	if u, err := t.r.Client.Unit(ctx, t.dummy); err == nil && !u.Dead && u.InCombat {
		return true
	}
	bot := normGUIDOf(t.r.Guids.Bot)
	if bot == "" && t.r.Guard != nil {
		bot = normGUIDOf(t.r.Guard.PlayerGUID())
	}
	if bot == "" || t.r.Collect == nil {
		return false
	}
	return t.r.Collect.EngagedAt(bot, normGUIDOf(t.dummy)) != nil
}

// dummyAlive reports the target still stands: readable, not dead.
func (t *dummyRun) dummyAlive(ctx context.Context) (bool, error) {
	u, err := t.r.Client.Unit(ctx, t.dummy)
	if err != nil {
		return false, err
	}
	return !u.Dead, nil
}

// abortReason is the collector's lost reason (the runner's abort arrives as
// context cancellation, which the waits already observe).
func (t *dummyRun) abortReason() string {
	if t.r.Collect != nil {
		return t.r.Collect.Lost()
	}
	return ""
}
