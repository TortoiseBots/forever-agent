package live

// situations.go: the live Situation for `tank_damage`, the one situation A4
// ports from trl/live/situations.py. The arena and the summoning follow that
// file; the HP governor keeps the victim in its band, and its floor rescues
// are capped per rolling minute. Everything that fights another slot's unit
// is refused; every summon is ledgered before anything attacks it.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

const (
	summonCap        = 20
	refillCap        = 3
	maxRescuesPerMin = 6
	// A victim whose last fall would leave it at or under this many percent
	// on the next sample is rescued before it reaches the floor.
	deathMarginPct = 3.0
	// The governor's own damage line lands a sample or two later: that fall
	// is not a mob's.
	gmFallSamples = 2
	maxDropCapPct = 30.0
	// Only falls this recent drive an early rescue.
	dropWindowS = 20.0
	// arena: one exclusive lock per arena, held for the whole repeat.
	arenaWaitS   = 15.0
	arenaGiveUpS = 600.0
)

// LiveSituation is the real situation phase: it dispatches on the trial's
// `situation.kind`, takes the arena, summons, governs the victim's health
// and stamps the fight's marks. Only tank_damage is ported this round; any
// other kind ends the repeat INVALID instead of scoring a scripted fight.
type LiveSituation struct {
	Server        string // the slot's server name (the arena lock's word)
	ArenaStateDir string // "" is ArenaStateDir()
	Arenas        []Arena
	Mobs          MobTable
	SlotIndex     int
	Log           func(string)

	// Timeout bounds the per-step waits (0 is the constants above).
	Now func() time.Time
}

// kindOf reads the trial's situation kind, tank_damage when absent.
func kindOf(trial map[string]any) string {
	sit, _ := trial["situation"].(map[string]any)
	if k, _ := sit["kind"].(string); k != "" {
		return k
	}
	return "tank_damage"
}

// Run is one repeat's situation: the interface Runner.Situation.
func (s *LiveSituation) Run(ctx context.Context, r *Repeat) (map[string]any, error) {
	tb := tbcmd.NewClient(NewTBRunner(r.Client))
	t := &tankRun{sit: s, r: r, tb: tb}
	kind := kindOf(r.Trial)
	if kind != "tank_damage" {
		return map[string]any{"ok": false, "ended": "unsupported",
			"invalid_reason": "situation kind " + kind + " is not ported on Forever"}, nil
	}
	return t.run(ctx)
}

// Finish releases the arena the repeat held: the runner calls it after the
// repeat's teardown, on every path.
func (s *LiveSituation) Finish() {
	// One LiveSituation serves a whole run; the arena it held is released by
	// the tank run itself when its repeat ends, so nothing is left here.
}

func (s *LiveSituation) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *LiveSituation) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(fmt.Sprintf(format, args...))
	}
}

// tankRun is one repeat's tank_damage state.
type tankRun struct {
	sit *LiveSituation
	r   *Repeat
	tb  *tbcmd.Client

	arena      *Arena
	lock       *ArenaLock
	summoned   []string
	mobSource  string
	summonWall string
	fightStart time.Time
	blocked    bool

	// levelLo/levelHi are the trial's mob level window.
	levelLo, levelHi int
	role             string
	count            int
	refill           bool
	refillEvery      float64
	duration         float64
	sitOpts          map[string]any
	victim           string
	band             [2]float64
	floor            int
	maxRescues       int
	topUpTo          *float64
	damageInterval   float64
}

func numOpt(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	return pyjson.Num(v)
}

func intOpt(m map[string]any, key string) (int, bool) {
	f, ok := numOpt(m, key)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// run is the tank_damage driver: arena, summon, pull, fight window, marks.
func (t *tankRun) run(ctx context.Context) (map[string]any, error) {
	t.sitOpts, _ = t.r.Trial["situation"].(map[string]any)
	if why := t.unportedOptions(); why != "" {
		return map[string]any{"ok": false, "ended": "unsupported", "invalid_reason": why}, nil
	}
	player, _ := t.r.Trial["player"].(map[string]any)
	level, _ := intOpt(player, "level")
	if level <= 0 {
		level = 1
	}
	mobs, _ := t.sitOpts["mobs"].(map[string]any)
	t.role, _ = mobs["role"].(string)
	if t.role == "" {
		t.role = "melee"
	}
	t.count, _ = intOpt(mobs, "count")
	if t.count == 0 {
		if _, ok := mobs["count"]; !ok {
			t.count = 1
		}
	}
	if t.count < 1 {
		t.count = 1
	}
	t.refill = true
	if v, ok := mobs["refill"].(bool); ok {
		t.refill = v
	}
	t.refillEvery, _ = numOpt(mobs, "refill_every_s")
	t.duration, _ = numOpt(t.r.Trial, "duration_s")
	if t.duration <= 0 {
		t.duration = 60
	}
	t.victim = "player"
	if v, _ := t.sitOpts["victim"].(string); v != "" {
		t.victim = v
	}
	t.band = [2]float64{40, 90}
	if list, ok := pyjson.List(t.sitOpts["hp_band"]); ok && len(list) == 2 {
		lo, _ := pyjson.Num(list[0])
		hi, _ := pyjson.Num(list[1])
		t.band = [2]float64{lo, hi}
	}
	t.floor, _ = intOpt(t.sitOpts, "floor_pct")
	if t.floor == 0 {
		t.floor = 30
	}
	t.maxRescues, _ = intOpt(t.sitOpts, "max_rescues_per_min")
	if t.maxRescues == 0 {
		t.maxRescues = maxRescuesPerMin
	}
	t.topUpTo = nil
	if f, ok := numOpt(t.sitOpts, "top_up_to_pct"); ok {
		t.topUpTo = &f
	}
	t.damageInterval, _ = numOpt(t.sitOpts, "damage_interval_s")
	if t.damageInterval <= 0 {
		t.damageInterval = 4
	}
	t.mobSource = "summon"
	lo, hi := levelWindow(level, mobs)
	t.levelLo, t.levelHi = lo, hi

	arena, why := t.acquireArena(ctx, level)
	if arena == nil {
		return map[string]any{"ok": false, "ended": "no arena", "invalid_reason": why}, nil
	}
	defer t.releaseArena()

	if name := t.r.BotName; name != "" {
		t.bringHire(ctx, name)
	}
	if t.count > 0 {
		made := t.summon(ctx, t.role, t.count)
		if len(made) == 0 && t.blocked {
			return map[string]any{"ok": false, "ended": "no mobs", "invalid_reason": "summon unclaimed"}, nil
		}
		if len(made) == 0 {
			return map[string]any{"ok": false, "ended": "no mobs",
				"invalid_reason": "no mobs (npc add temp refused and no world mob)"}, nil
		}
	}
	t.r.Guids.Mobs = append([]string(nil), t.summoned...)
	window := [2]int{t.levelLo, t.levelHi}

	wait, _ := numOpt(t.sitOpts, "pull_after_s")
	if wait == 0 {
		wait, _ = numOpt(t.sitOpts, "ooc_wait_s")
	}

	// The pull: target and fight every summon once. The serve's attack call
	// blocks the session for its for_ms, so it is kept short; the mob keeps
	// fighting on its own once contact is made (the bench measures the
	// healer's answer, not the player's swings).
	if wait > 0 {
		sleepCtx(ctx, time.Duration(wait*float64(time.Second)))
	}
	for _, guid := range t.summoned {
		_ = t.r.Client.Target(ctx, guid)
		_, _ = t.r.Client.Attack(ctx, guid, 2500)
	}
	fightSeen := t.waitFight(ctx, 60*time.Second)
	t.r.ExtraChecks = append(t.r.ExtraChecks, t.mobsReadyRow(ctx, window, fightSeen))

	gov := t.governor()
	hook := t.r.Collect.TickHook(gov.Tick)
	defer t.r.Collect.RemoveTickHook(hook)

	ended := ""
	entered := t.sit.now()
	var deadline time.Time
	lastRefill := t.sit.now()
	var nextRefill time.Time
	if t.refillEvery > 0 {
		nextRefill = t.sit.now().Add(time.Duration(t.refillEvery * float64(time.Second)))
	}
	for {
		if why := t.abortReason(); why != "" {
			ended = "disconnected"
			t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": ended})
			return map[string]any{"ok": false, "ended": ended, "invalid_reason": why}, nil
		}
		now := t.sit.now()
		if !fightSeen && t.fighting(ctx) {
			fightSeen = true
		}
		if t.fightStart.IsZero() && fightSeen {
			t.startFight(now)
		}
		if !t.fightStart.IsZero() && deadline.IsZero() {
			deadline = t.fightStart.Add(time.Duration(t.duration * float64(time.Second)))
		}
		if !fightSeen && now.Sub(entered) > 60*time.Second {
			ended = "no_fight"
			break
		}
		if gov.Ended != "" {
			ended = gov.Ended
			break
		}
		if !deadline.IsZero() && !now.Before(deadline) {
			break
		}
		if t.refill && now.Sub(lastRefill) > 3*time.Second && t.count > 0 && len(t.summoned) < summonCap {
			alive, ok := t.aliveCount(ctx)
			if ok && alive < t.count {
				lastRefill = now
				if added := t.summonOne(ctx, t.role); added != "" {
					t.addRefillMark(added, t.summonWall)
					_ = t.r.Client.Target(ctx, added)
					_, _ = t.r.Client.Attack(ctx, added, 2500)
				}
			}
		}
		if !nextRefill.IsZero() && !now.Before(nextRefill) {
			nextRefill = now.Add(time.Duration(t.refillEvery * float64(time.Second)))
			alive, ok := t.aliveCount(ctx)
			if ok && alive < refillCap {
				if added := t.summonOne(ctx, t.role); added != "" {
					t.addRefillMark(added, t.summonWall)
					_ = t.r.Client.Target(ctx, added)
					_, _ = t.r.Client.Attack(ctx, added, 2500)
				}
			}
		}
		sleepCtx(ctx, time.Second)
	}
	end := ended
	if end == "" {
		end = "duration"
	}
	t.r.Marks.Add(map[string]any{"kind": "fight_end", "ended": end})
	return map[string]any{"ok": true, "ended": end, "mob_source": t.mobSource}, nil
}

// unportedOptions names the first situation option this round leaves out, so
// a repeat is INVALID with the reason instead of silently dropping it.
func (t *tankRun) unportedOptions() string {
	if v, ok := t.sitOpts["debuff"]; ok && v != nil {
		return "situation option debuff is not ported for tank_damage this round"
	}
	if list, ok := pyjson.List(t.sitOpts["prepull_unaura"]); ok && len(list) > 0 {
		return "situation option prepull_unaura is not ported this round (the serve refuses .unaura on another player)"
	}
	if v, ok := t.sitOpts["reset_hire_cooldowns"].(bool); ok && v {
		return "situation option reset_hire_cooldowns is not ported this round (the serve refuses .cooldown on another player)"
	}
	if v, ok := t.sitOpts["energy_drain"]; ok && v != nil {
		return "situation option energy_drain is not ported this round (the serve refuses .modify energy on another player)"
	}
	if f, ok := numOpt(t.sitOpts, "min_rep_gap_s"); ok && f > 0 {
		return "situation option min_rep_gap_s is not ported this round"
	}
	return ""
}

// levelWindow is the mob level window: player level plus the mobs
// level_delta, one level either side (Python level_window).
func levelWindow(level int, mobs map[string]any) (int, int) {
	delta := 1
	if v, ok := mobs["level_delta"]; ok && v != nil {
		if f, ok := pyjson.Num(v); ok {
			delta = int(f)
		}
	}
	target := level + delta
	return maxInt(1, target-1), minInt(60, target+1)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sleepCtx(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// abortReason is the runner's abort or the collector's lost reason.
func (t *tankRun) abortReason() string {
	if t.r.Guard != nil {
		if why := t.r.Guard.AbortReason(); why != "" {
			return why
		}
	}
	return t.r.Collect.Lost()
}

// ---- arena --------------------------------------------------------------

// acquireArena takes the first eligible arena whose lock this run can hold
// and that reads quiet, waiting at most arenaGiveUpS. The lock and the
// in-process claim are released by releaseArena.
func (t *tankRun) acquireArena(ctx context.Context, level int) (*Arena, string) {
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
				itoa(int(math.Round(a.X))), itoa(int(math.Round(a.Y))), itoa(int(math.Round(a.Z))), itoa(a.Map)); err != nil {
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
				t.arena, t.lock = a, lock
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
// unit rows carry no faction, so any live creature or stranger within 40
// yards counts as not quiet (stricter than the module's own probe).
func (t *tankRun) quiet(ctx context.Context) bool {
	units, err := t.r.Client.Units(ctx, 40)
	if err != nil {
		return false
	}
	party := map[string]bool{}
	if t.r.Guids.Bot != "" {
		party[normGUIDOf(t.r.Guids.Bot)] = true
	}
	for _, e := range t.r.Guids.Extras {
		party[normGUIDOf(e)] = true
	}
	for _, u := range units {
		if u.Dead || party[normGUIDOf(u.GUID)] {
			continue
		}
		return false
	}
	return true
}

// releaseArena gives the arena back after the repeat's teardown.
func (t *tankRun) releaseArena() {
	if t.lock != nil {
		t.lock.Release()
		t.lock = nil
	}
}

// ---- hire ---------------------------------------------------------------

// bringHire puts the hire in the arena and proves it within 10 yards: the
// party teleport first, then the module's come order, then a failure log
// (the fight still runs; the hire's distance is not a scored row).
func (t *tankRun) bringHire(ctx context.Context, name string) {
	a := t.arena
	if a != nil {
		if _, err := t.tb.TeleParty(ctx, uint32(a.Map), a.X, a.Y, a.Z); err != nil {
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

func distanceOf(u Unit, err error) any {
	if err != nil {
		return "unreadable"
	}
	return u.Distance
}

func (t *tankRun) unitByName(ctx context.Context, name string) (Unit, error) {
	units, err := t.r.Client.UnitsFilter(ctx, 100, 0, name)
	if err != nil {
		return Unit{}, err
	}
	for _, u := range units {
		if strings.EqualFold(u.Name, name) {
			return u, nil
		}
	}
	return Unit{}, errf("no unit named %s in view", name)
}

// ---- summoning ----------------------------------------------------------

// summon summons count mobs of role and returns their guids. It stops after
// the first unclaimed summon: an orphan in the arena would make every later
// refill worse.
func (t *tankRun) summon(ctx context.Context, role string, count int) []string {
	var made []string
	for len(made) < count {
		guid := t.summonOne(ctx, role)
		if guid == "" {
			break
		}
		made = append(made, guid)
	}
	return made
}

// summonOne adds one creature of role, claims the fresh unit it produced and
// proves its level. An unclaimed summon is ledgered and blocks the repeat.
func (t *tankRun) summonOne(ctx context.Context, role string) string {
	if t.blocked {
		return ""
	}
	mobs := t.sit.Mobs
	if mobs == nil {
		mobs = LoadMobs()
	}
	entries := pickEntries(mobs, role, t.levelLo, t.levelHi)
	if len(entries) == 0 && role != "humanoid" {
		entries = pickEntries(mobs, "melee", t.levelLo, t.levelHi)
	}
	if len(entries) == 0 {
		t.sit.logf("no %s entry for level window %d-%d", role, t.levelLo, t.levelHi)
		t.blocked = true
		return ""
	}
	tries := minInt(5, maxInt(1, len(entries)*2))
	for i := range tries {
		entry := entries[i%len(entries)]
		known := map[string]bool{}
		for _, g := range t.r.Ledger.SummonGUIDs() {
			known[normGUIDOf(g)] = true
		}
		before := map[string]bool{}
		if rows, err := t.r.Client.UnitsFilter(ctx, 30, entry.Entry, ""); err == nil {
			for _, u := range rows {
				before[normGUIDOf(u.GUID)] = true
			}
		}
		t.summonWall = t.r.Marks.NowISO()
		if err := t.r.Guard.Send(ctx, "npc_add_temp", TargetNone, "", itoa(entry.Entry)); err != nil {
			if IsDisconnected(err) {
				t.blocked = true
				return ""
			}
			t.sit.logf("npc add temp %d refused: %v", entry.Entry, err)
			continue
		}
		guid, seen := t.claim(ctx, entry.Entry, 5*time.Second, known, before)
		if guid == "" {
			row := map[string]any{"kind": "summon_unclaimed", "entry": entry.Entry,
				"new_units": seen, "arena": t.arenaRow()}
			t.r.Ledger.Add(row)
			t.r.Marks.Add(map[string]any{"kind": "summon_unclaimed", "entry": entry.Entry})
			t.sit.logf("summon entry %d: no unit claimed (new units in view: %d)", entry.Entry, len(seen))
			t.blocked = true
			return ""
		}
		u, err := t.r.Client.Unit(ctx, guid)
		if err != nil {
			dc := &Disconnected{err: err}
			_ = dc
			t.r.Ledger.Add(map[string]any{"kind": "summon", "guid": guid, "entry": entry.Entry})
			t.r.Marks.Add(map[string]any{"kind": "summon", "guid": guid, "entry": entry.Entry, "wall": t.summonWall})
			t.summoned = append(t.summoned, guid)
			continue
		}
		if u.Level < t.levelLo || u.Level > t.levelHi || u.Dead {
			t.sit.logf("summon entry %d: guid %s level %d is outside %d-%d; killed", entry.Entry, guid, u.Level, t.levelLo, t.levelHi)
			_ = t.r.Guard.Kill(ctx, guid)
			continue
		}
		t.r.Ledger.Add(map[string]any{"kind": "summon", "guid": guid, "entry": entry.Entry,
			"level": u.Level, "wall": t.summonWall})
		t.r.Ledger.Add(map[string]any{"kind": "mob_ready", "guid": guid, "entry": entry.Entry, "level": u.Level})
		t.r.Marks.Add(map[string]any{"kind": "summon", "guid": guid, "entry": entry.Entry,
			"level": u.Level, "wall": t.summonWall})
		t.summoned = append(t.summoned, guid)
		return guid
	}
	t.blocked = true
	return ""
}

// claim finds the unit an `.npc add temp` produced: a fresh unit of the
// entry that was not in view before. Exactly one fresh unit is the summon
// wherever it stands in the 30-yard scan; two or more need a unit within 3
// yards of the player (the add spawns at the caster).
func (t *tankRun) claim(ctx context.Context, entry int, timeout time.Duration, known, before map[string]bool) (string, []map[string]any) {
	deadline := t.sit.now().Add(timeout)
	var seen []map[string]any
	for {
		seen = nil
		var fresh []Unit
		if rows, err := t.r.Client.UnitsFilter(ctx, 30, entry, ""); err == nil {
			for _, u := range rows {
				g := normGUIDOf(u.GUID)
				if g == "" || u.Dead || known[g] || before[g] {
					continue
				}
				fresh = append(fresh, u)
				seen = append(seen, map[string]any{"guid": g, "distance": u.Distance})
			}
		}
		sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Distance < fresh[j].Distance })
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

func (t *tankRun) arenaRow() map[string]any {
	if t.arena == nil {
		return nil
	}
	a := t.arena
	return map[string]any{"id": a.ID, "map": a.Map, "x": a.X, "y": a.Y, "z": a.Z}
}

func (t *tankRun) addRefillMark(guid, wall string, extra ...any) {
	row := map[string]any{"kind": "refill", "guid": guid}
	if wall != "" {
		row["wall"] = wall
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if k, ok := extra[i].(string); ok {
			row[k] = extra[i+1]
		}
	}
	t.r.Marks.Add(row)
}

// aliveCount is this repeat's own summons still up: a corpse out of view is
// gone, an unreadable row is not zero alive.
func (t *tankRun) aliveCount(ctx context.Context) (int, bool) {
	states, err := t.summonStates(ctx, t.summoned)
	if err != nil {
		return 0, false
	}
	n := 0
	for _, st := range states {
		if st == "alive" {
			n++
		}
	}
	return n, true
}

// summonStates maps guid -> alive|dead|gone for the given summons.
func (t *tankRun) summonStates(ctx context.Context, guids []string) (map[string]string, error) {
	rows, err := t.r.Client.Units(ctx, 60)
	if err != nil {
		return nil, err
	}
	byGUID := map[string]Unit{}
	for _, u := range rows {
		byGUID[normGUIDOf(u.GUID)] = u
	}
	out := map[string]string{}
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
		u, err := t.r.Client.Unit(ctx, v)
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

// notInView is the serve's answer for a unit whose despawned row is gone.
func notInView(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not in view")
}

// fighting reports whether any summon is in combat.
func (t *tankRun) fighting(ctx context.Context) bool {
	rows, err := t.r.Client.Units(ctx, 40)
	if err != nil {
		return false
	}
	want := map[string]bool{}
	for _, g := range t.summoned {
		want[normGUIDOf(g)] = true
	}
	for _, u := range rows {
		if want[normGUIDOf(u.GUID)] && u.InCombat && !u.Dead {
			return true
		}
	}
	return false
}

// waitFight waits up to timeout for the pull to put a summon in combat.
func (t *tankRun) waitFight(ctx context.Context, timeout time.Duration) bool {
	deadline := t.sit.now().Add(timeout)
	for {
		if t.fighting(ctx) {
			return true
		}
		if !t.sit.now().Before(deadline) || ctx.Err() != nil {
			return false
		}
		sleepCtx(ctx, time.Second)
	}
}

// startFight stamps the repeat's scored base once (t = 0 for every rule).
func (t *tankRun) startFight(now time.Time) {
	if !t.fightStart.IsZero() {
		return
	}
	t.fightStart = now
	mobs := make([]any, 0, len(t.summoned))
	for _, g := range t.summoned {
		mobs = append(mobs, normGUIDOf(g))
	}
	t.r.Marks.Add(map[string]any{"kind": "fight_start", "mobs": mobs})
}

// mobsReadyRow proves the summons are alive, of the summoned entry, inside
// the level window and (once the pull worked) in combat: the serve's unit
// row has no faction field, so the fight itself is the proof the creature is
// hostile.
func (t *tankRun) mobsReadyRow(ctx context.Context, window [2]int, fought bool) map[string]any {
	alive := []any{}
	fighting := 0
	for _, g := range t.summoned {
		u, err := t.r.Client.Unit(ctx, g)
		if err != nil || u.Dead {
			continue
		}
		if u.Level < window[0] || u.Level > window[1] {
			continue
		}
		alive = append(alive, normGUIDOf(g))
		if u.InCombat {
			fighting++
		}
	}
	ok := len(alive) > 0 && fought && fighting > 0
	evidence := fmt.Sprintf("%d/%d summons alive in level %d-%d and %d in combat after the pull; the unit row carries no faction, so combat is the hostile proof",
		len(alive), len(t.summoned), window[0], window[1], fighting)
	return map[string]any{"id": "mobs_ready", "ok": ok, "value": alive, "evidence": evidence}
}

// ---- the governor -------------------------------------------------------

// fall is one sample-to-sample drop inside the drop window.
type fall struct {
	at  time.Time
	pct float64
}

// Governor keeps the victim inside hp_band: a victim above the band's top is
// damaged down to the middle (the player through `.damage`, a hire through
// `.tb hurt`), a victim at the floor is rescued to full and the rescue is
// excluded from the scored window. No fewer than maxRescues rescues in a
// rolling minute ends the repeat as "floor".
type Governor struct {
	Guard      *Guard
	Marks      *Marks
	Victim     string
	Band       [2]float64
	Floor      int
	VictimGUID string
	TopUpTo    *float64
	Now        func() time.Time
	MaxRescues int
	TopUpGap   float64
	OOC        bool
	Stopped    func() bool
	Log        func(string)

	// Hurt puts a hire (or an extra) at a percent of its maximum; Refill
	// puts it back to full. The player uses Guard.SetHP for a refill and
	// Guard.Send("damage") for a top-up.
	Hurt   func(pct float64) error
	Refill func() error

	LastDamage   time.Time
	Rescues      []time.Time
	Ended        string
	PrevPct      *float64
	MaxDrop      float64
	Falls        []fall
	Pending      float64
	PendingSamps int
	EarlyRescues int
}

func (t *tankRun) governor() *Governor {
	g := &Governor{
		Guard: t.r.Guard, Marks: t.r.Marks, Victim: t.victim, Band: t.band, Floor: t.floor,
		VictimGUID: t.victimGUID(), TopUpTo: t.topUpTo, Now: t.sit.now,
		MaxRescues: t.maxRescues, TopUpGap: t.damageInterval,
		Stopped: func() bool { return t.r.Collect.Lost() != "" },
		Log:     func(line string) { t.sit.logf("%s", line) },
	}
	if t.victim != "player" {
		name := t.r.BotName
		if idx := extraIndex(t.victim); idx > 0 && idx-1 < len(t.r.ExtraNames) {
			name = t.r.ExtraNames[idx-1]
		}
		g.Hurt = func(pct float64) error {
			_, err := t.tb.FreeTextHurt(context.Background(), name, uint8(math.Round(pct)))
			return err
		}
		g.Refill = func() error {
			_, err := t.tb.FreeTextHurt(context.Background(), name, 100)
			return err
		}
	}
	return g
}

// extraIndex is 1-3 for a victim named extra1..extra3, else 0.
func extraIndex(victim string) int {
	v := strings.ToLower(strings.TrimSpace(victim))
	if len(v) == 6 && strings.HasPrefix(v, "extra") {
		if n := int(v[5] - '0'); n >= 1 && n <= 3 {
			return n
		}
	}
	return 0
}

func (t *tankRun) victimGUID() string {
	if idx := extraIndex(t.victim); idx > 0 {
		if idx-1 < len(t.r.Guids.Extras) {
			return t.r.Guids.Extras[idx-1]
		}
		return ""
	}
	if t.victim == "bot" {
		return t.r.Guids.Bot
	}
	return t.r.Guids.Player
}

// sampleKey is the collector's key of the victim.
func (g *Governor) sampleKey() string {
	if idx := extraIndex(g.Victim); idx > 0 {
		return "extra" + itoa(idx)
	}
	if g.Victim == "bot" {
		return "bot"
	}
	return "player"
}

// Tick is one sampler tick's governor step (the python Governor.tick).
func (g *Governor) Tick(sample map[string]any) {
	if g == nil || sample == nil {
		return
	}
	if g.Stopped != nil && g.Stopped() {
		return
	}
	victim, _ := sample[g.sampleKey()].(map[string]any)
	if victim == nil {
		return
	}
	hp := int(intOf(victim["hp"]))
	maxHP := int(intOf(victim["max_hp"]))
	if maxHP <= 0 {
		return
	}
	if hp <= 0 || boolOf(victim["dead"]) {
		if g.Victim == "player" {
			g.Ended = "player_dead"
		} else {
			g.Ended = "victim_dead"
		}
		return
	}
	percent := 100 * float64(hp) / float64(maxHP)
	now := g.now()
	floorHP := int(float64(g.Floor) * float64(maxHP) / 100)
	if g.PrevPct != nil && *g.PrevPct > percent {
		drop := *g.PrevPct - percent
		if g.PendingSamps > 0 && g.Pending > 0 {
			if drop >= 0.5*g.Pending {
				drop = math.Max(0, drop-g.Pending)
				g.Pending = 0
			}
		}
		if drop > 0.05 {
			g.Falls = append(g.Falls, fall{at: now, pct: math.Min(maxDropCapPct, drop)})
		}
	}
	if g.PendingSamps > 0 {
		g.PendingSamps--
		if g.PendingSamps == 0 {
			g.Pending = 0
		}
	}
	kept := g.Falls[:0]
	for _, f := range g.Falls {
		if now.Sub(f.at) <= time.Duration(dropWindowS*float64(time.Second)) {
			kept = append(kept, f)
		}
	}
	g.Falls = kept
	g.MaxDrop = 0
	for _, f := range g.Falls {
		if f.pct > g.MaxDrop {
			g.MaxDrop = f.pct
		}
	}
	prev := percent
	g.PrevPct = &prev
	early := hp >= floorHP && g.MaxDrop > 0 && percent-g.MaxDrop <= deathMarginPct
	if hp < floorHP || early {
		if early {
			g.EarlyRescues++
		}
		g.rescue(hp, maxHP, percent, now)
		recent := g.Rescues[:0]
		for _, at := range g.Rescues {
			if now.Sub(at) <= time.Minute {
				recent = append(recent, at)
			}
		}
		g.Rescues = recent
		if len(recent) >= g.MaxRescues {
			g.Ended = "floor"
		}
		return
	}
	if inCombat, ok := victim["in_combat"].(bool); ok && !inCombat {
		return
	}
	if percent > g.Band[1] && now.Sub(g.LastDamage) > time.Duration(g.TopUpGap*float64(time.Second)) {
		g.topUp(hp, maxHP)
	}
}

// rescue puts the victim back to full. The core's `.modify hp <hp>` sets
// current AND maximum health to its argument, so a player rescue passes the
// victim's own maximum (the only value that leaves the maximum alone); a
// hire is refilled through the module's own hurt command.
func (g *Governor) rescue(hp, maxHP int, percent float64, now time.Time) {
	var err error
	if g.Victim == "player" {
		err = g.Guard.SetHP(context.Background(), g.VictimGUID, maxHP)
	} else if g.Refill != nil {
		err = g.Refill()
	}
	if err != nil {
		g.logf("governor floor heal skipped: %v", err)
		return
	}
	wall := g.wall()
	g.Marks.Add(map[string]any{"kind": "gm_heal", "hp": hp, "max_hp": maxHP,
		"pct": round2(percent), "to": maxHP, "wall": wall})
	g.Marks.Excluded(wall, g.wallAt(3*time.Second), "floor")
	g.Rescues = append(g.Rescues, now)
	g.LastDamage = now
	if g.OOC {
		g.logf("!!! OOC RESCUE FIRED: %s fell to %.1f%% (%d/%d) out of combat", g.Victim, percent, hp, maxHP)
	}
}

// topUp brings a victim above the band down to the band's middle.
func (g *Governor) topUp(hp, maxHP int) {
	middlePct := (g.Band[0] + g.Band[1]) / 2
	if g.TopUpTo != nil {
		middlePct = *g.TopUpTo
	}
	if g.Victim == "player" {
		dmg := damageStep(hp, maxHP, g.Band[0], g.Band[1], g.TopUpTo)
		if dmg < 1 {
			return
		}
		if err := g.Guard.Send(context.Background(), "damage", TargetSelf, g.VictimGUID, itoa(dmg)); err != nil {
			g.logf("governor damage skipped: %v", err)
			return
		}
		g.LastDamage = g.now()
		g.Pending = float64(dmg) * 100 / float64(maxInt(1, maxHP))
		g.PendingSamps = gmFallSamples
		return
	}
	middle := int(middlePct * float64(maxHP) / 100)
	if hp <= middle {
		return
	}
	if g.Hurt == nil {
		return
	}
	if err := g.Hurt(float64(100 * middle / maxHP)); err != nil {
		g.logf("governor bot hurt skipped: %v", err)
		return
	}
	g.LastDamage = g.now()
	g.Pending = float64(hp-middle) * 100 / float64(maxInt(1, maxHP))
	g.PendingSamps = gmFallSamples
}

// damageStep is the largest GM damage that keeps the victim above bandLow.
func damageStep(hp, maxHP int, bandLow, bandTop float64, toPct *float64) int {
	if maxHP == 0 {
		return 0
	}
	percent := 100 * float64(hp) / float64(maxHP)
	if percent <= bandTop {
		return 0
	}
	var middle float64
	if toPct == nil {
		middle = (bandLow + bandTop) / 2 * float64(maxHP) / 100
	} else {
		middle = *toPct * float64(maxHP) / 100
	}
	cap := float64(hp) - bandLow*float64(maxHP)/100
	return maxInt(0, int(math.Min(float64(hp)-middle, cap)))
}

func (g *Governor) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Governor) wall() string {
	if g.Marks != nil {
		return g.Marks.NowISO()
	}
	return nowISO()
}

func (g *Governor) wallAt(offset time.Duration) string {
	if g.Marks != nil {
		return g.Marks.NowISOOffset(offset)
	}
	return time.Now().Add(offset).UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
}

func (g *Governor) logf(format string, args ...any) {
	if g.Log != nil {
		g.Log(fmt.Sprintf(format, args...))
	}
}
