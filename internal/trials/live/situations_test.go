package live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// sim is a tiny scripted world for the situation tests: `.npc add temp`
// spawns a unit, `.damage` and gm_set_hp move the player's health, attack
// sets the mob in combat, and status decays the player a little on each
// read (a mob hitting the tank).
type sim struct {
	mu       sync.Mutex
	hp       int
	maxHP    int
	decay    int
	mobs     map[string]map[string]any
	nextID   int
	entry    int
	level    int
	lines    []string
	killed   []string
	units    []string
	target   string
	lastDrop time.Time
	trace    []string
}

func newSim(entry, level, hp, decay int) *sim {
	return &sim{hp: hp, maxHP: hp, decay: decay, mobs: map[string]map[string]any{},
		nextID: 0x1000, entry: entry, level: level}
}

func (s *sim) guid() string {
	s.nextID++
	return fmt.Sprintf("0x%016x", s.nextID)
}

func (s *sim) status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	// One hit a second, not one per read: the guard's selection polls read
	// status too.
	if s.decay > 0 && s.hp > 1 && (s.lastDrop.IsZero() || time.Since(s.lastDrop) >= time.Second) {
		s.hp -= s.decay
		if s.hp < 1 {
			s.hp = 1
		}
		s.lastDrop = time.Now()
		s.trace = append(s.trace, fmt.Sprintf("decay -> %d at %s", s.hp, time.Now().Format("15:04:05.000")))
	}
	return map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"character": "Atank", "health": s.hp, "max_health": s.maxHP, "target": s.target,
		"mono_ms": 1000, "x": -11926, "y": -1283, "z": 85, "dead": false}
}

func (s *sim) unitRows() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	for _, u := range s.mobs {
		out = append(out, u)
	}
	return out
}

// hook is the fake serve's per-call world step.
func (s *sim) hook(method string, params map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case "gm":
		text, _ := params["text"].(string)
		s.lines = append(s.lines, text)
		switch {
		case strings.HasPrefix(text, ".npc add temp "):
			guid := s.guid()
			s.mobs[guid] = map[string]any{"guid": guid, "name": "Atank's foe", "type": "unit",
				"entry": s.entry, "level": s.level, "distance": 0, "in_combat": false,
				"dead": false, "health": 500, "max_health": 500, "auras": []any{}}
			s.units = append(s.units, guid)
		case strings.HasPrefix(text, ".damage "):
			var dmg int
			_, _ = fmt.Sscanf(text, ".damage %d", &dmg)
			s.hp -= dmg
			if s.hp < 1 {
				s.hp = 1
			}
		}
	case "gm_set_hp":
		hp := int(numOrZero(params["hp"]))
		s.hp, s.maxHP = hp, hp
	case "gm_kill":
		guid, _ := params["guid"].(string)
		delete(s.mobs, guid)
		s.killed = append(s.killed, guid)
	case "attack", "target":
		guid, _ := params["guid"].(string)
		if method == "target" {
			s.target = guid
		}
		if u, ok := s.mobs[guid]; ok {
			u["in_combat"] = true
		}
		if method == "gm_set_hp" {
			s.lastDrop = time.Time{}
		}
	}
}

// TestTankDamageSummonsAndBand is the A4 first test: three summons are
// ledgered, the governor never lets the victim fall under the floor without
// a rescue mark, and the rescue cap ends the repeat.
func TestTankDamageSummonsAndBand(t *testing.T) {
	const entry, floorPct = 2734, 15
	fake := newFakeServe(t)
	world := newSim(entry, 41, 1000, 0)
	fake.statusFn = world.status
	fake.unitsFn = world.unitRows
	fake.hook = world.hook
	repDir := t.TempDir()
	marks := NewMarks(filepath.Join(repDir, "marks.jsonl"))
	marks.Start("")
	ledger := NewLedger(filepath.Join(repDir, "ledger.jsonl"))
	client := NewClient(fake.rpc())
	rep := &Repeat{Index: 1, Dir: repDir, TrialID: "atank-heal",
		Trial: map[string]any{
			"id": "atank-heal", "duration_s": float64(20),
			"player": map[string]any{"level": float64(40), "stance": float64(71)},
			"situation": map[string]any{
				"kind": "tank_damage",
				"mobs": map[string]any{"role": "melee", "count": float64(3),
					"level_delta": float64(1), "refill": true},
				"hp_band":   []any{float64(40), float64(80)},
				"floor_pct": float64(floorPct), "victim": "player",
				"top_up_to_pct": float64(45), "damage_interval_s": float64(0.5),
				"max_rescues_per_min": float64(2),
			},
		},
		Client: client, Ledger: ledger, Marks: marks, Guids: Guids{Player: "0x0000000000000001"},
	}
	rep.Collect = NewCollector(client, repDir, marks, &rep.Guids, ledger)
	rep.Guard = NewGuard(client, GuardOptions{Ledger: ledger, Marks: marks,
		PlayerGUID: rep.Guids.Player, Summons: ledger.SummonGUIDs})
	// Once the fight starts, the mob's hits decay the player fast enough to
	// trip the floor (and its cap) inside the test's few seconds.
	world.decay = 200
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep.Collect.Start(ctx)
	sit := &LiveSituation{Arenas: []Arena{{ID: "field", Map: 0, X: -11926, Y: -1283, Z: 85, LevelMin: 1, LevelMax: 60}},
		Mobs:          MobTable{"melee": {{Entry: entry, Name: "Test Stalker", LevelMin: 40, LevelMax: 41}}},
		ArenaStateDir: t.TempDir(), Log: func(line string) { t.Log(line) }}
	out, err := sit.Run(ctx, rep)
	rep.Collect.Stop()
	if err != nil {
		t.Fatalf("situation: %v", err)
	}
	if got := strOf(out["ended"]); got != "floor" {
		t.Fatalf("ended = %q, want floor (2 rescues capped); outcome %v", got, out)
	}
	if n := len(ledger.OfKind("summon")); n != 3 {
		t.Fatalf("summons ledgered = %d, want 3", n)
	}
	for _, guid := range rep.Guids.Mobs {
		if _, ok := world.mobs[guid]; !ok {
			t.Fatalf("mob %s not in the world", guid)
		}
	}
	heals := marks.Kinds("gm_heal")
	if len(heals) != 2 {
		t.Fatalf("rescues = %d, want 2 (max_rescues_per_min)", len(heals))
	}
	for _, h := range heals {
		if h["wall"] == nil {
			t.Fatalf("gm_heal without a wall: %v", h)
		}
	}
	// The floor invariant: no sample under floor_pct without a rescue at or
	// before it (the rescue's excluded span covers 3 s).
	healWalls := []string{}
	for _, h := range heals {
		healWalls = append(healWalls, strOf(h["wall"]))
	}
	for _, line := range readLines(t, filepath.Join(repDir, "samples.jsonl")) {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if strOf(row["unit"]) != "player" {
			continue
		}
		hp := int(intOf(row["hp"]))
		maxHP := int(intOf(row["max_hp"]))
		if maxHP == 0 {
			continue
		}
		pct := 100 * float64(hp) / float64(maxHP)
		if pct >= float64(floorPct) {
			continue
		}
		if !rescuedAt(healWalls, strOf(row["wall"])) {
			world.mu.Lock()
			t.Logf("sim trace: %v", world.trace)
			world.mu.Unlock()
			for _, m := range marks.Rows() {
				t.Logf("mark: %v", m)
			}
			t.Fatalf("player at %.1f%% under the floor with no rescue mark: %v", pct, row)
		}
	}
	if len(world.killed) == 0 {
		t.Log("no summon was killed by the situation (teardown owns them)")
	}
}

// rescuedAt reports whether a gm_heal belongs to this sample's tick: the
// governor rescues AFTER the sampler wrote the row (the same order as the
// Python), so the heal may be a moment either side of the sample's wall.
func rescuedAt(heals []string, wall string) bool {
	at, err := time.Parse(time.RFC3339Nano, wall)
	if err != nil {
		return false
	}
	for _, h := range heals {
		ht, err := time.Parse(time.RFC3339Nano, h)
		if err != nil {
			continue
		}
		d := at.Sub(ht)
		if d < 0 {
			d = -d
		}
		if d <= 3*time.Second {
			return true
		}
	}
	return false
}

// TestTeardownKillsLedger checks the repeat teardown kills exactly the
// ledgered, still-alive summons through gm_kill and reports the ones it
// cannot prove dead.
func TestTeardownKillsLedger(t *testing.T) {
	fake := newFakeServe(t)
	world := newSim(2734, 41, 1000, 0)
	alive := "0x00000000000000aa"
	world.mobs[alive] = map[string]any{"guid": alive, "name": "Atank's foe", "type": "unit",
		"entry": 2734, "level": 41, "distance": 2, "in_combat": false, "dead": false,
		"health": 500, "max_health": 500, "auras": []any{}}
	fake.statusFn = world.status
	fake.unitsFn = world.unitRows
	fake.hook = world.hook
	repDir := t.TempDir()
	marks := NewMarks(filepath.Join(repDir, "marks.jsonl"))
	marks.Start("")
	ledger := NewLedger(filepath.Join(repDir, "ledger.jsonl"))
	// One alive summon and one that is not in view, so the teardown has a
	// verified kill and an UNKNOWN to report.
	ledger.Add(map[string]any{"kind": "summon", "guid": alive, "entry": 2734})
	ledger.Add(map[string]any{"kind": "summon", "guid": "0x00000000000000bb", "entry": 2734})
	client := NewClient(fake.rpc())
	rep := &Repeat{Index: 1, Dir: repDir, TrialID: "atank-heal", Trial: map[string]any{"id": "atank-heal"},
		Client: client, Ledger: ledger, Marks: marks, Guids: Guids{Player: "0x0000000000000001", Mobs: []string{alive, "0x00000000000000bb"}}}
	rep.Guard = NewGuard(client, GuardOptions{Ledger: ledger, Marks: marks,
		PlayerGUID: rep.Guids.Player, Summons: ledger.SummonGUIDs})
	r := &Runner{}
	rows := r.repeatTeardown(ctxBackground(), rep)
	if len(world.killed) != 1 || world.killed[0] != alive {
		t.Fatalf("gm_kill calls = %v, want only %s", world.killed, alive)
	}
	var row map[string]any
	for _, m := range rows {
		if strOf(m["id"]) == "mobs_dead" {
			row = m
		}
	}
	if row == nil {
		t.Fatalf("no mobs_dead row: %v", rows)
	}
	if !boolOf(row["ok"]) {
		t.Fatalf("mobs_dead failed with a killed summon and a gone one: %v", row)
	}
	if got := row["verified"]; got == nil || len(got.([]any)) != 2 {
		t.Fatalf("mobs_dead verified = %v, want both summons", got)
	}
	// An unreadable status is UNKNOWN: a scripted rpc failure on the absent
	// guid must fail the row instead of reading as "none alive".
	ledger2 := NewLedger(filepath.Join(repDir, "ledger2.jsonl"))
	ledger2.Add(map[string]any{"kind": "summon", "guid": "0x00000000000000dd", "entry": 2734})
	rep2 := &Repeat{Index: 1, Dir: repDir, TrialID: "atank-heal", Trial: rep.Trial,
		Client: client, Ledger: ledger2, Marks: marks,
		Guids: Guids{Player: "0x0000000000000001", Mobs: []string{"0x00000000000000dd"}}}
	rep2.Guard = NewGuard(client, GuardOptions{Ledger: ledger2, Marks: marks,
		PlayerGUID: rep2.Guids.Player, Summons: ledger2.SummonGUIDs})
	fake.mu.Lock()
	fake.errCodes["unit"] = []int{-32003}
	fake.mu.Unlock()
	rows = r.repeatTeardown(ctxBackground(), rep2)
	for _, m := range rows {
		if strOf(m["id"]) == "mobs_dead" {
			row = m
		}
	}
	if boolOf(row["ok"]) || !strings.Contains(strOf(row["evidence"]), "UNKNOWN") {
		t.Fatalf("an unreadable summon status must be UNKNOWN, not ok: %v", row)
	}
	// A guid outside the ledger is refused before any line goes out.
	if err := rep.Guard.Kill(ctxBackground(), "0x00000000000000cc"); err == nil {
		t.Fatal("kill of an unledgered guid was not refused")
	}
}

// TestServeProbeIgnoresPort pins the Forever lease rule: a listening slot
// port is the serve the runner wants, so --any must not skip it.
func TestServeProbeIgnoresPort(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "slots.json")
	writeTestSlots(t, cfgPath)
	cfg := loadTestSlots(t, cfgPath)
	store := newTestStore(cfg, cfgPath)
	store.Probe = ServeProbe{Probe: store.Probe}
	serveDir := filepath.Join(dir, "serve")
	if err := os.MkdirAll(filepath.Join(serveDir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	sf := &SlotsFile{Servers: cfg.Servers, Forever: map[string]ServerExt{"s1": {ServeDirRoot: serveDir}}}
	hold, err := AcquireSlot(sf, store, "s1", "", true, "holder-a", func(string) error { return nil })
	if err != nil {
		t.Fatalf("acquire with a serving slot: %v", err)
	}
	_ = hold
}

// TestArenaLockExclusive checks one arena's lock excludes a second holder
// and releases cleanly, without blocking.
func TestArenaLockExclusive(t *testing.T) {
	dir := t.TempDir()
	a := NewArenaLock("s1", "unga-ridge", dir)
	b := NewArenaLock("s1", "unga-ridge", dir)
	if !a.Acquire() {
		t.Fatal("first acquire failed")
	}
	start := time.Now()
	if b.Acquire() {
		t.Fatal("second holder acquired an arena lock")
	}
	if time.Since(start) > time.Second {
		t.Fatal("acquire blocked")
	}
	a.Release()
	if !b.Acquire() {
		t.Fatal("lock not released")
	}
	b.Release()
	other := NewArenaLock("s2", "unga-ridge", dir)
	if !other.Acquire() {
		t.Fatal("another server's arena was excluded")
	}
	other.Release()
}

// TestOverlayDataWins checks a Forever overlay row replaces the base row of
// the same key and adds new ones.
func TestOverlayDataWins(t *testing.T) {
	base := []byte(`[{"id":"unga-ridge","map":0,"x":-11926.13,"y":-1283.2,"z":85.45,"level_min":20,"level_max":60},
		{"id":"legacy","map":0,"x":-12790,"y":-400,"z":18.5,"level_min":1,"level_max":60}]`)
	overlay := []byte(`[{"id":"unga-ridge","map":1,"x":-11920,"y":-1280,"z":90,"level_min":30,"level_max":60},
		{"id":"new-field","map":0,"x":1,"y":2,"z":3,"level_min":1,"level_max":60}]`)
	rows, err := ArenasFrom(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].ID != "unga-ridge" || rows[0].Map != 1 || rows[0].Z != 90 {
		t.Fatalf("overlay did not win: %+v", rows)
	}
	if rows[2].ID != "new-field" {
		t.Fatalf("new overlay arena not appended: %+v", rows)
	}
	mobs, err := MobsFrom(
		[]byte(`{"melee":[{"entry":1,"level_min":10,"level_max":12},{"entry":2,"level_min":20,"level_max":22}]}`),
		[]byte(`{"melee":[{"entry":2,"level_min":21,"level_max":23}],"caster":[{"entry":3,"level_min":30,"level_max":31}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(mobs["melee"]) != 2 || mobs["melee"][1].LevelMin != 21 {
		t.Fatalf("mob overlay did not win: %+v", mobs["melee"])
	}
	if len(mobs["caster"]) != 1 || mobs["caster"][0].Entry != 3 {
		t.Fatalf("caster overlay not merged: %+v", mobs["caster"])
	}
	kit := botGearKitFrom([]byte(`{"priest":{"blue":{"40":[{"entry":6324,"name":"Robes of Arugal","slot":"chest"}]}}}`),
		"priest", "blue", 43)
	if len(kit) != 1 || kit[0].Entry != 6324 {
		t.Fatalf("gear overlay kit = %+v", kit)
	}
	if got := botGearKitFrom([]byte(`{}`), "priest", "blue", 43); len(got) != 0 {
		t.Fatalf("empty gear overlay invented a kit: %+v", got)
	}
	var over any
	if err := json.Unmarshal([]byte(`{"priest":{"holy":[18530]}}`), &over); err != nil {
		t.Fatal(err)
	}
	got := specOverlay(over, "priest", "holy", []any{float64(52943)})
	if len(got) != 1 || int(numOrZero(got[0])) != 18530 {
		t.Fatalf("spec overlay did not win: %v", got)
	}
	// The embedded files load: the base tables are present and readable.
	if len(LoadArenas()) == 0 || len(LoadMobs()["melee"]) == 0 {
		t.Fatal("embedded arenas or mobs are empty")
	}
}
