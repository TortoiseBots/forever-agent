package live

// a5_situations_test.go: the A5 situation kinds, fake-driven (no server).
// The cases the Python live tests cover (test_cc_target, test_drink_settle,
// test_ooc_hurt) are translated to the fake serve here.

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// newRepA5 builds a repeat over a fake serve's client with the common
// trial/state the situation tests need.
func newRepA5(t *testing.T, fake *fakeServe, trial map[string]any, player, bot string) (*Repeat, *Marks, *Ledger) {
	t.Helper()
	// These fakes have no `.tb` channel to read a bot's state from: the engine proof has its own tests.
	old := proveHireEngines
	proveHireEngines = func(context.Context, *tbcmd.Client, *Repeat, *LiveSituation) string { return "" }
	t.Cleanup(func() { proveHireEngines = old })
	dir := t.TempDir()
	marks := NewMarks(filepath.Join(dir, "marks.jsonl"))
	marks.Start("")
	ledger := NewLedger(filepath.Join(dir, "ledger.jsonl"))
	client := NewClient(fake.rpc())
	rep := &Repeat{Index: 1, Dir: dir, TrialID: strOf(trial["id"]), Trial: trial,
		Client: client, Ledger: ledger, Marks: marks, Guids: Guids{Player: player, Bot: bot}, BotName: bot}
	rep.Collect = NewCollector(client, dir, marks, &rep.Guids, ledger)
	rep.Guard = NewGuard(client, GuardOptions{Ledger: ledger, Marks: marks,
		PlayerGUID: player, Summons: ledger.SummonGUIDs})
	rep.Guard.SetHire(bot)
	return rep, marks, ledger
}

func arenaA5() []Arena {
	return []Arena{{ID: "field", Map: 0, X: -11926, Y: -1283, Z: 85, LevelMin: 1, LevelMax: 60}}
}

func trialA5(kind string, duration float64, sit map[string]any) map[string]any {
	base := map[string]any{
		"kind": kind, "mobs": map[string]any{"role": "melee", "count": float64(3),
			"level_delta": float64(1), "refill": false},
		"hp_band": []any{float64(40), float64(90)}, "floor_pct": float64(12),
		"victim": "player", "pull_after_s": float64(0), "ooc_wait_s": float64(0),
	}
	for k, v := range sit {
		base[k] = v
	}
	return map[string]any{"id": "a5-" + kind, "duration_s": duration,
		"player":    map[string]any{"level": float64(40), "stance": float64(71)},
		"situation": base}
}

// TestCasterMobProof is the A5 first test: the caster proof comes from the
// mob's own spell_start/spell_go combat-log rows, never from a cast the
// player or the bot made.
func TestCasterMobProof(t *testing.T) {
	fake := newFakeServe(t)
	mob := "0x0000000000000c01"
	other := "0x0000000000000c02"
	dir := t.TempDir()
	marks := NewMarks(filepath.Join(dir, "marks.jsonl"))
	col := NewCollector(NewClient(fake.rpc()), dir, marks, &Guids{}, nil)
	tr := &tankRun{r: &Repeat{Collect: col, Marks: marks}, summoned: []string{mob}}
	row := tr.casterCastRow()
	if boolOf(row["ok"]) {
		t.Fatalf("no row: caster proof must be false: %v", row)
	}
	col.mu.Lock()
	col.raw = append(col.raw, map[string]any{"seq": float64(1), "kind": "spell_start",
		"source": map[string]any{"guid": other}, "spell_id": float64(133)})
	col.mu.Unlock()
	if row = tr.casterCastRow(); boolOf(row["ok"]) {
		t.Fatalf("another unit's cast is not the summon's proof: %v", row)
	}
	for _, kind := range []string{"spell_start", "spell_go"} {
		col.mu.Lock()
		col.raw = append(col.raw, map[string]any{"seq": float64(2), "kind": kind,
			"source": map[string]any{"guid": mob}, "spell_id": float64(116)})
		col.mu.Unlock()
		row = tr.casterCastRow()
		if !boolOf(row["ok"]) {
			t.Fatalf("%s by the summon must prove the cast: %v", kind, row)
		}
		if int(intOf(row["value"])) != 116 {
			t.Fatalf("caster proof spell_id = %v, want 116", row["value"])
		}
	}
}

// TestPackPullsEverySummon: the pack kind attacks the second and third
// summons too, not only the first one the pull selected.
func TestPackPullsEverySummon(t *testing.T) {
	fake := newFakeServe(t)
	world := newSim(2734, 41, 1000, 0)
	var mu sync.Mutex
	var attacks []string
	fake.statusFn = world.status
	fake.unitsFn = world.unitRows
	fake.hook = func(method string, params map[string]any) {
		world.hook(method, params)
		if method == "attack" {
			mu.Lock()
			attacks = append(attacks, strAny(params["guid"]))
			mu.Unlock()
		}
	}
	rep, _, _ := newRepA5(t, fake, trialA5("pack", 2, nil), "0x0000000000000001", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep.Collect.Start(ctx)
	sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
		Mobs: MobTable{"melee": {{Entry: 2734, Name: "Test Stalker", LevelMin: 40, LevelMax: 42}}},
		Log:  func(line string) { t.Log(line) }}
	out, err := sit.Run(ctx, rep)
	rep.Collect.Stop()
	if err != nil {
		t.Fatalf("situation: %v", err)
	}
	if strOf(out["ended"]) != "duration" {
		t.Fatalf("ended = %v, want duration", out["ended"])
	}
	if len(rep.Guids.Mobs) != 3 {
		t.Fatalf("summoned %d mobs, want 3", len(rep.Guids.Mobs))
	}
	for _, guid := range rep.Guids.Mobs {
		found := 0
		for _, a := range attacks {
			if a == guid {
				found++
			}
		}
		if found < 1 {
			t.Fatalf("summon %s was never attacked; attacks %v", guid, attacks)
		}
	}
}

// TestDebuffAppliedAfterFightStart: a debuff repeat stamps fight_start
// BEFORE the apply and records the fresh aura_applied as the trigger.
func TestDebuffAppliedAfterFightStart(t *testing.T) {
	fake := newFakeServe(t)
	world := newSim(2734, 41, 1000, 0)
	fake.statusFn = world.status
	fake.unitsFn = world.unitRows
	fake.events = []map[string]any{}
	var seq int
	fake.hook = func(method string, params map[string]any) {
		world.hook(method, params)
		if method != "gm" {
			return
		}
		text := strAny(params["text"])
		if strings.HasPrefix(text, ".aura 770") {
			seq++
			fake.mu.Lock()
			fake.events = append(fake.events, map[string]any{"seq": float64(seq),
				"kind": "aura_applied", "spell_id": float64(770),
				"target": map[string]any{"guid": "0x0000000000000001"}})
			fake.mu.Unlock()
		}
	}
	trial := trialA5("debuff", 2, map[string]any{
		"mobs":   map[string]any{"role": "melee", "count": float64(1), "level_delta": float64(1), "refill": false},
		"debuff": map[string]any{"spell_id": float64(770)},
	})
	fake.ownUnit = map[string]any{"guid": "0x0000000000000001", "name": "Atank",
		"type": "player", "health": float64(1000), "max_health": float64(1000), "auras": []any{}}
	rep, marks, _ := newRepA5(t, fake, trial, "0x0000000000000001", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep.Collect.Start(ctx)
	sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
		Mobs: MobTable{"melee": {{Entry: 2734, Name: "Test Stalker", LevelMin: 40, LevelMax: 42}}},
		Log:  func(line string) { t.Log(line) }}
	out, err := sit.Run(ctx, rep)
	rep.Collect.Stop()
	if err != nil {
		t.Fatalf("situation: %v", err)
	}
	if strOf(out["ended"]) != "duration" {
		t.Fatalf("ended = %v, want duration (%v)", out["ended"], out)
	}
	start, applied := -1, -1
	all := marks.Rows()
	for i, row := range all {
		if strOf(row["kind"]) == "fight_start" && !boolOf(row["ooc"]) {
			start = i
		}
		if strOf(row["kind"]) == "debuff_applied" {
			applied = i
		}
	}
	if start < 0 || applied < 0 {
		t.Fatalf("fight_start %d, debuff_applied %d; rows %v", start, applied, all)
	}
	if applied < start {
		t.Fatalf("debuff_applied (row %d) precedes fight_start (row %d)", applied, start)
	}
	for _, row := range marks.Kinds("debuff_applied") {
		if !boolOf(row["fresh"]) {
			t.Fatalf("debuff_applied is not fresh: %v", row)
		}
	}
}

// TestCCTargetNeverAttacksTheMarkedMob: every unmarked summon is attacked,
// the marked one only ever sees the Taunt, and mobs_ready carries the cc
// evidence.
func TestCCTargetNeverAttacksTheMarkedMob(t *testing.T) {
	fake := newFakeServe(t)
	world := newSim(2734, 41, 1000, 0)
	var mu sync.Mutex
	var attacks []string
	fake.statusFn = world.status
	fake.unitsFn = func() []map[string]any {
		rows := world.unitRows()
		// the bot's Polymorph shows on the second summon (the marked one)
		world.mu.Lock()
		marked := ""
		if len(world.units) > 1 {
			marked = world.units[1]
		}
		world.mu.Unlock()
		for i, u := range rows {
			if marked != "" && strAny(u["guid"]) == marked {
				cp := map[string]any{}
				for k, v := range u {
					cp[k] = v
				}
				cp["auras"] = []any{map[string]any{"spell_id": float64(118)}}
				rows[i] = cp
			}
		}
		return append(rows, map[string]any{"guid": "0x0000000000000002",
			"name": "Atank", "type": "player", "distance": float64(0), "level": float64(40),
			"health": float64(1000), "max_health": float64(1000), "auras": []any{}})
	}
	fake.hook = func(method string, params map[string]any) {
		world.hook(method, params)
		switch method {
		case "attack":
			mu.Lock()
			attacks = append(attacks, strAny(params["guid"]))
			mu.Unlock()
		case "cast":
			guid := strAny(params["target"])
			world.mu.Lock()
			if u, ok := world.mobs[guid]; ok {
				u["in_combat"] = true
			}
			world.mu.Unlock()
		}
	}
	trial := trialA5("cc_target", 2, nil)
	trial["situation"].(map[string]any)["mobs"] = map[string]any{"role": "humanoid", "count": float64(3),
		"level_delta": float64(1), "refill": false}
	rep, _, _ := newRepA5(t, fake, trial, "0x0000000000000001", "0x0000000000000002")
	rep.BotName = "Atank"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep.Collect.Start(ctx)
	sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
		Mobs: MobTable{"humanoid": {{Entry: 2734, Name: "Test Humanoid", LevelMin: 40, LevelMax: 42}}},
		Log:  func(line string) { t.Log(line) }}
	out, err := sit.Run(ctx, rep)
	rep.Collect.Stop()
	if err != nil {
		t.Fatalf("situation: %v", err)
	}
	if strOf(out["ended"]) != "duration" {
		t.Fatalf("ended = %v, want duration (%v)", out["ended"], out)
	}
	if len(world.units) != 3 {
		t.Fatalf("summoned %d humanoids, want 3", len(world.units))
	}
	marked := world.units[1]
	for _, a := range attacks {
		if a == marked {
			t.Fatalf("the marked mob %s was attacked; attacks %v", marked, attacks)
		}
	}
	for _, guid := range []string{world.units[0], world.units[2]} {
		hit := false
		for _, a := range attacks {
			if a == guid {
				hit = true
			}
		}
		if !hit {
			t.Fatalf("unmarked summon %s was not attacked; attacks %v", guid, attacks)
		}
	}
	var markRow, ready map[string]any
	for _, row := range rep.ExtraChecks {
		switch strOf(row["id"]) {
		case "cc_mark_set":
			markRow = row
		case "mobs_ready":
			ready = row
		}
	}
	if markRow == nil || !boolOf(markRow["ok"]) {
		t.Fatalf("cc_mark_set row missing or refused: %v", markRow)
	}
	if ready == nil || !boolOf(ready["ok"]) {
		t.Fatalf("mobs_ready failed: %v", ready)
	}
	ev := strOf(ready["evidence"])
	if !strings.Contains(ev, "2/2 unmarked summons in combat") || !strings.Contains(ev, "marked") ||
		!strings.Contains(ev, "in combat: yes") {
		t.Fatalf("mobs_ready evidence = %q", ev)
	}
}

// TestOOCBuffsWaitsOutTheMeal: a Drink aura is never stripped; the window
// opens when the meal ends and the trial's mana level is re-applied.
func TestOOCBuffsWaitsOutTheMeal(t *testing.T) {
	fake := newFakeServe(t)
	hire := "0x0000000000000002"
	var mu sync.Mutex
	auras := []any{map[string]any{"spell_id": float64(24355)}}
	auraReads := 0
	fake.hook = func(method string, params map[string]any) {
		if method == "unit" {
			mu.Lock()
			auraReads++
			if auraReads >= 2 {
				auras = []any{}
			}
			mu.Unlock()
		}
	}
	fake.unitsFn = func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return []map[string]any{{"guid": hire, "name": "Bheal", "type": "player",
			"level": float64(40), "max_power": float64(2000), "power": float64(1000),
			"health": float64(1000), "max_health": float64(1000), "auras": auras}}
	}
	trial := map[string]any{"id": "a5-ooc", "duration_s": float64(1),
		"player": map[string]any{"level": float64(40)},
		"bot":    map[string]any{"mana_pct": float64(15)},
		"situation": map[string]any{"kind": "ooc_buffs", "ooc_wait_s": float64(1),
			"mobs": map[string]any{"count": float64(0)}}}
	rep, marks, _ := newRepA5(t, fake, trial, "0x0000000000000001", hire)
	rep.BotName = "Bheal"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
		MealWait: 2 * time.Second, Log: func(line string) { t.Log(line) }}
	out, err := sit.Run(ctx, rep)
	if err != nil {
		t.Fatalf("situation: %v", err)
	}
	if !boolOf(out["ok"]) || strOf(out["ended"]) != "ooc" {
		t.Fatalf("outcome = %v, want ok/ooc", out)
	}
	resets := marks.Kinds("ooc_mana_reset")
	if len(resets) != 1 {
		t.Fatalf("ooc_mana_reset marks = %d, want 1; marks %v", len(resets), marks.Rows())
	}
	if starts := marks.Kinds("fight_start"); len(starts) != 1 || !boolOf(starts[0]["ooc"]) {
		t.Fatalf("fight_start rows = %v", starts)
	}
	// the hire kept its auras: no unaura of the hire is provable from the
	// fake's logs, so pin the semantic mark instead: the window opened after
	// the meal read ended, and the mana was reset to the trial's pct.
	if pct, _ := pyjsonLikeNum(resets[0]["pct"]); pct != 15 {
		t.Fatalf("mana reset pct = %v, want 15", resets[0]["pct"])
	}
}

// TestOOCBuffsMealStillActiveInvalid: a meal that outlasts the wait opens no
// window and says so.
func TestOOCBuffsMealStillActiveInvalid(t *testing.T) {
	fake := newFakeServe(t)
	hire := "0x0000000000000002"
	fake.unitsFn = func() []map[string]any {
		return []map[string]any{{"guid": hire, "name": "Bheal", "type": "player",
			"level": float64(40), "max_power": float64(2000), "health": float64(1000),
			"max_health": float64(1000),
			"auras":      []any{map[string]any{"spell_id": float64(24355)}}}}
	}
	trial := map[string]any{"id": "a5-ooc", "duration_s": float64(1),
		"player": map[string]any{"level": float64(40)},
		"bot":    map[string]any{"mana_pct": float64(15)},
		"situation": map[string]any{"kind": "ooc_buffs", "ooc_wait_s": float64(1),
			"mobs": map[string]any{"count": float64(0)}}}
	rep, marks, _ := newRepA5(t, fake, trial, "0x0000000000000001", hire)
	rep.BotName = "Bheal"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
		MealWait: 200 * time.Millisecond, Log: func(line string) { t.Log(line) }}
	out, err := sit.Run(ctx, rep)
	if err != nil {
		t.Fatalf("situation: %v", err)
	}
	if boolOf(out["ok"]) || strOf(out["invalid_reason"]) != "meal still active" {
		t.Fatalf("outcome = %v, want invalid meal still active", out)
	}
	if starts := marks.Kinds("fight_start"); len(starts) != 0 {
		t.Fatalf("a window opened over a live meal: %v", starts)
	}
}

// TestOOCHurtSetsHealthOnSchedule: the hurt window damages the victim into
// hp_band on schedule, skips while it is inside, and never unauras the
// stance.
func TestOOCHurtSetsHealthOnSchedule(t *testing.T) {
	fake := newFakeServe(t)
	world := newSim(2734, 41, 1000, 0)
	hire := "0x0000000000000002"
	fake.statusFn = world.status
	fake.unitsFn = func() []map[string]any {
		rows := world.unitRows()
		return append(rows, map[string]any{"guid": hire, "name": "Bheal", "type": "player",
			"distance": float64(0), "level": float64(40), "health": float64(1000),
			"max_health": float64(1000), "auras": []any{}})
	}
	var mu sync.Mutex
	var gms []string
	var casts int
	fake.hook = func(method string, params map[string]any) {
		world.hook(method, params)
		switch method {
		case "gm":
			mu.Lock()
			gms = append(gms, strAny(params["text"]))
			mu.Unlock()
		case "cast":
			mu.Lock()
			casts++
			mu.Unlock()
		}
	}
	trial := trialA5("ooc_hurt", 3, map[string]any{
		"mobs":                map[string]any{"count": float64(0), "role": "melee"},
		"hp_band":             []any{float64(30), float64(40)},
		"ooc_wait_s":          float64(1.5),
		"hurt_every_s":        float64(0.2),
		"max_rescues_per_min": float64(12),
	})
	fake.ownUnit = map[string]any{"guid": "0x0000000000000001", "name": "Atank",
		"type": "player", "health": float64(1000), "max_health": float64(1000), "auras": []any{}}
	rep, marks, ledger := newRepA5(t, fake, trial, "0x0000000000000001", hire)
	rep.BotName = "Bheal"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep.Collect.Start(ctx)
	sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
		Log: func(line string) { t.Log(line) }}
	out, err := sit.Run(ctx, rep)
	rep.Collect.Stop()
	if err != nil {
		t.Fatalf("situation: %v", err)
	}
	if !boolOf(out["ok"]) || strOf(out["ended"]) != "ooc" {
		t.Fatalf("outcome = %v, want ok/ooc", out)
	}
	if n := len(marks.Kinds("hurt")); n < 1 {
		t.Fatalf("no hurt mark; marks %v", marks.Rows())
	}
	damage := 0
	for _, line := range gms {
		if strings.HasPrefix(line, ".damage ") {
			damage++
		}
		if strings.HasPrefix(line, ".unaura") {
			t.Fatalf("the hurt window unaura'd the stance: %q", line)
		}
	}
	if damage == 0 {
		t.Fatalf("no .damage line; gm lines %v", gms)
	}
	if casts == 0 {
		t.Fatalf("the stance was down out of combat and never re-cast")
	}
	if len(ledger.OfKind("summon")) != 0 {
		t.Fatalf("the hurt window summoned mobs")
	}
	starts, ends := marks.Kinds("fight_start"), marks.Kinds("fight_end")
	if len(starts) != 1 || !boolOf(starts[0]["ooc"]) || len(ends) != 1 ||
		strOf(ends[0]["ended"]) != "ooc" {
		t.Fatalf("fight marks: %v / %v", starts, ends)
	}
}

// TestOOCHurtBelowBandWaitsForTheHealer: the core's .modify hp sets the
// maximum too, so a victim under the band is left to its healer, never
// raised with a GM hand that would shrink the maximum.
func TestOOCHurtBelowBandWaitsForTheHealer(t *testing.T) {
	tr := &tankRun{sit: &LiveSituation{}, r: &Repeat{Guids: Guids{Player: "0x0000000000000001"}},
		victim: "player", band: [2]float64{30, 40}}
	sample := map[string]any{
		"player": map[string]any{"hp": float64(200), "max_hp": float64(1000), "in_combat": false},
		"bot":    map[string]any{"hp": float64(1000), "max_hp": float64(1000), "in_combat": false},
	}
	what, detail, _ := tr.hurtTick(context.Background(), sample)
	if what != "skip" || !strings.Contains(detail, "below the band") {
		t.Fatalf("hurtTick = %q %q, want a below-band skip", what, detail)
	}
}

// TestPrepullUnauraProvesTheAuraGone: an aura the unaura does not remove
// invalidates the repeat before the summon.
func TestPrepullUnauraProvesTheAuraGone(t *testing.T) {
	fake := newFakeServe(t)
	hire := "0x0000000000000002"
	fake.unitsFn = func() []map[string]any {
		return []map[string]any{{"guid": hire, "name": "Bheal", "type": "player",
			"level": float64(40), "auras": []any{map[string]any{"spell_id": float64(71)}}}}
	}
	trial := trialA5("tank_damage", 1, map[string]any{"prepull_unaura": []any{float64(71)}})
	fake.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"character": "Atank", "target": "", "health": float64(1000), "max_health": float64(1000)}
	rep, _, _ := newRepA5(t, fake, trial, "0x0000000000000001", hire)
	rep.BotName = ""
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
		Log: func(line string) { t.Log(line) }}
	out, err := sit.Run(ctx, rep)
	if err != nil {
		t.Fatalf("situation: %v", err)
	}
	if boolOf(out["ok"]) || strOf(out["ended"]) != "prepull" {
		t.Fatalf("outcome = %v, want prepull INVALID", out)
	}
	if !strings.Contains(strOf(out["invalid_reason"]), "left aura(s) 71") {
		t.Fatalf("reason = %q", out["invalid_reason"])
	}
}

func pyjsonLikeNum(v any) (float64, bool) {
	return numOf(v)
}
